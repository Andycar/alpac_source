package litesrc

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/proxylink"

	"github.com/rs/zerolog/log"
)

// flixcdnUA is the User-Agent for HTTP requests to FlixCDN.
const flixcdnUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// flixcdnPayloadRe extracts the __PLAYER_PAYLOAD__ JSON object from the player HTML.
// As of 2026-05 the player switched from player21.js (`var p_id = ...`, <option> dropdown,
// `var translations_episodes = ...`) to player.bundle.js, which embeds all metadata in
// a single JSON blob: `window.__PLAYER_PAYLOAD__ = {...};`.
var flixcdnPayloadRe = regexp.MustCompile(`(?s)__PLAYER_PAYLOAD__\s*=\s*(\{.*?\});`)

type flixcdnChecker struct {
	client      *http.Client
	playerHost  string // e.g. "https://player0.flixcdn.space"
	refererHost string // e.g. "hdplayer.click"
	refererURL  string // e.g. "https://hdplayer.click/"

	// Stream URL cache: cacheKey → *flixcdnCacheEntry
	cacheMu sync.RWMutex
	cache   map[string]*flixcdnCacheEntry
}

type flixcdnCacheEntry struct {
	fileString string
	resolvedAt time.Time
}

const flixcdnCacheTTL = 10 * time.Minute

// flixcdnPayload mirrors the subset of window.__PLAYER_PAYLOAD__ we consume.
// The bundle declares many more fields (cover_url, autoplay, player_view, ...) that
// are irrelevant for resolving streams.
type flixcdnPayload struct {
	ID                      int64                 `json:"id"`
	Type                    string                `json:"type"` // "movie" | "serial"
	IsSerial                bool                  `json:"is_serial"`
	Domain                  string                `json:"domain"` // e.g. "hdplayer.click"
	CloudflareCaptchaPublic string                `json:"cloudflare_captcha_public"`
	CaptchaRequired         bool                  `json:"captcha_required"`
	Translations            []flixcdnPayloadTrans `json:"translations"`
	SeasonsEpisodes         map[string][]int      `json:"seasons_episodes"` // season -> episode numbers (default translator)
	Episodes                []int                 `json:"episodes"`         // episodes of the currently selected translation+season
	ForceCDN                string                `json:"force_cdn"`
}

type flixcdnPayloadTrans struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	// EpisodesQty is total count across all seasons (not per-season). The new API
	// no longer publishes a per-translator/per-season grid — we assume every translator
	// covers every episode the show advertises and let the upstream fall back when missing.
	EpisodesQty int `json:"episodes_qty"`
}

// flixcdnMeta is the normalised view used by the handlers.
type flixcdnMeta struct {
	ID              int64
	ContentType     string // "serial" or "movie"
	Translators     []flixcdnTranslator
	SeasonsEpisodes map[string][]int // season -> episodes; empty for movies
	ForceCDN        string
	CaptchaSitekey  string
	Domain          string
}

type flixcdnTranslator struct {
	ID   string
	Name string
}

func NewFlixcdnChecker(cfg config.Config) *flixcdnChecker {
	playerHost := strings.TrimSpace(cfg.Online.FlixCDN.PlayerHost)
	if playerHost == "" {
		playerHost = "https://player0.flixcdn.space"
	}
	playerHost = strings.TrimRight(playerHost, "/")
	if !strings.Contains(playerHost, "://") {
		playerHost = "https://" + playerHost
	}

	refererHost := strings.TrimSpace(cfg.Online.FlixCDN.RefererHost)
	if refererHost == "" {
		refererHost = "hdplayer.click"
	}

	return &flixcdnChecker{
		client:      httpclient.NewForBalancer("flixcdn", 12*time.Second),
		playerHost:  playerHost,
		refererHost: refererHost,
		refererURL:  "https://" + refererHost + "/",
		cache:       make(map[string]*flixcdnCacheEntry),
	}
}

func (f *flixcdnChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := strings.Trim(strings.TrimPrefix(strings.ToLower(r.URL.Path), "/lite/"), "/")

		switch raw {
		case "flixcdn":
			if parseBoolParam(r.URL.Query().Get("checksearch")) {
				show := f.checkSearch(r)
				writeCheckSearchResponse(w, show, pluginQualityBadgeGet("flixcdn"))
				return
			}
			f.index(w, r, links)
		case "flixcdn/video":
			f.video(w, r, links)
		default:
			writeJSON(w, http.StatusNotImplemented, map[string]any{
				"error":    "flixcdn route not implemented",
				"balanser": raw,
			})
		}
	}
}

// checkSearch returns true if FlixCDN has content for the given kinopoisk_id.
func (f *flixcdnChecker) checkSearch(r *http.Request) bool {
	q := r.URL.Query()
	kpID := parseInt64(q.Get("kinopoisk_id"))
	if kpID == 0 {
		kpID = parseInt64(q.Get("id"))
	}
	if kpID == 0 {
		return false
	}

	html, err := f.fetchHTML(r.Context(), fmt.Sprintf("/show/kinopoisk/%d", kpID))
	if err != nil {
		log.Debug().Err(err).Int64("kp", kpID).Msg("flixcdn: checkSearch fetch failed")
		return false
	}

	meta, ok := f.parseMeta(html)
	if !ok {
		log.Debug().Int64("kp", kpID).Int("html_len", len(html)).
			Bool("has_payload_marker", strings.Contains(html, "__PLAYER_PAYLOAD__")).
			Msg("flixcdn: checkSearch parseMeta failed")
		return false
	}
	got := meta.ID != 0 && len(meta.Translators) > 0
	if !got {
		log.Debug().Int64("kp", kpID).Int64("id", meta.ID).
			Str("type", meta.ContentType).Int("translators", len(meta.Translators)).
			Msg("flixcdn: checkSearch empty meta")
	}
	return got
}

// fetchHTML fetches a page from the FlixCDN player host with correct Referer.
func (f *flixcdnChecker) fetchHTML(ctx context.Context, path string) (string, error) {
	u := f.playerHost + path

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", flixcdnUA)
	req.Header.Set("Referer", f.refererURL)

	resp, err := f.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("flixcdn: %s returned %d", u, resp.StatusCode)
	}

	// Cap at 4 MB: player HTML is typically 2–10 KB; any larger is a
	// misbehaving upstream and should not allocate unbounded heap.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// parseMeta extracts metadata from the FlixCDN player HTML.
func (f *flixcdnChecker) parseMeta(html string) (*flixcdnMeta, bool) {
	m := flixcdnPayloadRe.FindStringSubmatch(html)
	if len(m) < 2 {
		return nil, false
	}

	var p flixcdnPayload
	if err := stdjson.Unmarshal([]byte(m[1]), &p); err != nil {
		log.Debug().Err(err).Msg("flixcdn: payload unmarshal failed")
		return nil, false
	}
	if p.ID == 0 {
		return nil, false
	}

	meta := &flixcdnMeta{
		ID:              p.ID,
		ContentType:     p.Type,
		ForceCDN:        p.ForceCDN,
		CaptchaSitekey:  p.CloudflareCaptchaPublic,
		Domain:          p.Domain,
		SeasonsEpisodes: p.SeasonsEpisodes,
	}
	if meta.ContentType == "" {
		if p.IsSerial {
			meta.ContentType = "serial"
		} else {
			meta.ContentType = "movie"
		}
	}

	for _, t := range p.Translations {
		if t.ID == 0 {
			continue
		}
		name := strings.TrimSpace(t.Title)
		if name == "" {
			continue
		}
		meta.Translators = append(meta.Translators, flixcdnTranslator{
			ID:   strconv.FormatInt(t.ID, 10),
			Name: name,
		})
	}

	return meta, true
}

// fetchTranslatorEpisodes returns {translatorID -> []episode} for the given season
// by issuing one fetchHTML per translator in parallel. If any request fails or its
// payload has no episodes, we fall back to defaultEps so the user can still try
// (the /api/player/files call may still succeed; worst case the upstream errors out).
//
// Requests share a 4-wide semaphore so we never hammer the upstream with N
// concurrent Chrome-fingerprint fetches for shows with many voices.
func (f *flixcdnChecker) fetchTranslatorEpisodes(ctx context.Context, id int64, translators []flixcdnTranslator, season string, defaultEps []int) map[string][]int {
	out := make(map[string][]int, len(translators))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)

	for _, tr := range translators {
		tr := tr
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			eps := f.fetchEpisodesForTranslator(ctx, id, tr.ID, season, defaultEps)
			mu.Lock()
			out[tr.ID] = eps
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

func (f *flixcdnChecker) fetchEpisodesForTranslator(ctx context.Context, id int64, translation, season string, defaultEps []int) []int {
	q := url.Values{}
	q.Set("translation", translation)
	if season != "" {
		q.Set("season", season)
	}
	path := fmt.Sprintf("/show/%d?%s", id, q.Encode())

	html, err := f.fetchHTML(ctx, path)
	if err != nil {
		log.Debug().Err(err).Int64("id", id).Str("t", translation).Str("s", season).
			Msg("flixcdn: per-translator fetch failed, falling back to default eps")
		return defaultEps
	}
	m := flixcdnPayloadRe.FindStringSubmatch(html)
	if len(m) < 2 {
		return defaultEps
	}
	var p flixcdnPayload
	if err := stdjson.Unmarshal([]byte(m[1]), &p); err != nil {
		return defaultEps
	}
	// Prefer the explicit episodes[] for (translation, season). If empty (e.g.
	// the translator simply doesn't cover this season), return an empty list so
	// the season-episode card is filtered out entirely for this voice.
	if len(p.Episodes) > 0 {
		return append([]int(nil), p.Episodes...)
	}
	// As a secondary cue, seasons_episodes for this season — server sometimes
	// fills it even when episodes[] is absent.
	if eps, ok := p.SeasonsEpisodes[season]; ok {
		return append([]int(nil), eps...)
	}
	return nil
}

// index handles the main /lite/flixcdn endpoint.
func (f *flixcdnChecker) index(w http.ResponseWriter, r *http.Request, links *proxylink.Manager) {
	q := r.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	kpID := parseInt64(q.Get("kinopoisk_id"))
	if kpID == 0 {
		kpID = parseInt64(q.Get("id"))
	}
	season, seasonSet := getsTVQueryInt(q.Get("s"))
	if !seasonSet {
		season = -1
	}
	selectedT := strings.TrimSpace(q.Get("t"))

	if kpID == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	html, err := f.fetchHTML(r.Context(), fmt.Sprintf("/show/kinopoisk/%d", kpID))
	if err != nil {
		log.Warn().Err(err).Int64("kp", kpID).Msg("flixcdn: fetch failed")
		writeGetsTVEmpty(w, rjson)
		return
	}

	meta, ok := f.parseMeta(html)
	if !ok {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if meta.ContentType == "movie" {
		f.writeMovie(w, r, rjson, title, originalTitle, meta, links)
		return
	}

	// Serial
	f.writeSerial(w, r, rjson, title, originalTitle, kpID, season, selectedT, meta, links)
}

// writeMovie renders movie translations list.
func (f *flixcdnChecker) writeMovie(w http.ResponseWriter, r *http.Request, rjson bool, title, originalTitle string, meta *flixcdnMeta, links *proxylink.Manager) {
	if len(meta.Translators) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(r)
	baseTitle := getsTVJoinName(title, originalTitle)
	rows := make([]map[string]any, 0, len(meta.Translators))
	labels := make([]string, 0, len(meta.Translators))

	for _, tr := range meta.Translators {
		videoURL := host + "/lite/flixcdn/video?" + url.Values{
			"id":    {strconv.FormatInt(meta.ID, 10)},
			"t":     {tr.ID},
			"title": {title},
		}.Encode()

		row := map[string]any{
			"method":    "call",
			"url":       videoURL,
			"stream":    videoURL,
			"name":      tr.Name,
			"translate": tr.Name,
			"title":     fmt.Sprintf("%s (%s)", baseTitle, tr.Name),
		}
		rows = append(rows, row)
		labels = append(labels, tr.Name)
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "movie",
			"data": rows,
		})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range rows {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, 0, 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// writeSerial renders seasons or episodes list.
//
// The new payload exposes a single top-level seasons_episodes map for the default
// translator only, so we re-fetch /show/<id>?translation=T&season=S for each
// translator to discover their actual episode coverage. The episode list shown
// to the client is for the *selected* translator (param `t`); other translators
// appear in a top-level `voice` array as switcher links.
func (f *flixcdnChecker) writeSerial(w http.ResponseWriter, r *http.Request, rjson bool, title, originalTitle string, kpID int64, season int, selectedT string, meta *flixcdnMeta, links *proxylink.Manager) {
	if len(meta.Translators) == 0 || len(meta.SeasonsEpisodes) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	seasonNums := make([]int, 0, len(meta.SeasonsEpisodes))
	for s := range meta.SeasonsEpisodes {
		if n, err := strconv.Atoi(s); err == nil {
			seasonNums = append(seasonNums, n)
		}
	}
	sort.Ints(seasonNums)

	if len(seasonNums) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(r)

	// If season not selected, show season list
	if season == -1 {
		rows := make([]map[string]any, 0, len(seasonNums))
		labels := make([]string, 0, len(seasonNums))
		for _, s := range seasonNums {
			name := strconv.Itoa(s) + " сезон"
			link := host + "/lite/flixcdn?" + url.Values{
				"rjson":          {getsTVBool(rjson)},
				"kinopoisk_id":   {strconv.FormatInt(kpID, 10)},
				"title":          {title},
				"original_title": {originalTitle},
				"s":              {strconv.Itoa(s)},
			}.Encode()

			rows = append(rows, map[string]any{
				"method": "link",
				"id":     s,
				"url":    link,
				"name":   name,
			})
			labels = append(labels, name)
		}

		if rjson {
			writeJSON(w, http.StatusOK, map[string]any{
				"type": "season",
				"data": rows,
			})
			return
		}
		var sb strings.Builder
		sb.WriteString(`<div class="videos__line">`)
		for i, row := range rows {
			getsTVAppendSeasonHTML(&sb, row, labels[i], i == 0)
		}
		sb.WriteString(`</div>`)
		writeHTML(w, http.StatusOK, sb.String())
		return
	}

	// Season selected — show episodes grouped by translator
	seasonStr := strconv.Itoa(season)
	baseTitle := getsTVJoinName(title, originalTitle)

	defaultEps, ok := meta.SeasonsEpisodes[seasonStr]
	if !ok || len(defaultEps) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Fetch per-translator episode lists in parallel. The default payload only
	// exposes seasons_episodes for the default translator, so we ask the server
	// "what episodes does translator T have for season S" by re-fetching
	// /show/<id>?translation=T&season=S and reading the top-level episodes array.
	trEps := f.fetchTranslatorEpisodes(r.Context(), meta.ID, meta.Translators, seasonStr, defaultEps)

	// Filter translators down to those that actually carry this season.
	type voiceEntry struct {
		tr  flixcdnTranslator
		eps []int
	}
	available := make([]voiceEntry, 0, len(meta.Translators))
	for _, tr := range meta.Translators {
		eps := trEps[tr.ID]
		if len(eps) == 0 {
			continue
		}
		available = append(available, voiceEntry{tr: tr, eps: eps})
	}
	if len(available) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Resolve selected translator: explicit `t`, else first available.
	selectedIdx := 0
	if selectedT != "" {
		for i, v := range available {
			if v.tr.ID == selectedT {
				selectedIdx = i
				break
			}
		}
	}
	selected := available[selectedIdx]

	// Build voice switcher rows (one per available translator).
	voiceRows := make([]map[string]any, 0, len(available))
	for i, v := range available {
		voiceURL := host + "/lite/flixcdn?" + url.Values{
			"rjson":          {getsTVBool(rjson)},
			"kinopoisk_id":   {strconv.FormatInt(kpID, 10)},
			"title":          {title},
			"original_title": {originalTitle},
			"s":              {seasonStr},
			"t":              {v.tr.ID},
		}.Encode()
		voiceRows = append(voiceRows, map[string]any{
			"method": "link",
			"name":   v.tr.Name,
			"active": i == selectedIdx,
			"url":    voiceURL,
		})
	}

	// Episode rows for the selected translator only — Lampa already groups voices
	// via the top-level `voice` array, so emitting one card per (episode, voice)
	// would just duplicate every series N times in the UI.
	epNums := append([]int(nil), selected.eps...)
	sort.Ints(epNums)

	rows := make([]map[string]any, 0, len(epNums))
	labels := make([]string, 0, len(epNums))
	for _, ep := range epNums {
		videoURL := host + "/lite/flixcdn/video?" + url.Values{
			"id":    {strconv.FormatInt(meta.ID, 10)},
			"t":     {selected.tr.ID},
			"s":     {seasonStr},
			"e":     {strconv.Itoa(ep)},
			"title": {title},
		}.Encode()

		name := strconv.Itoa(ep) + " серия"
		row := map[string]any{
			"method":     "call",
			"url":        videoURL,
			"stream":     videoURL,
			"s":          season,
			"e":          ep,
			"name":       name,
			"title":      fmt.Sprintf("%s / %s / %s (%s)", baseTitle, name, selected.tr.Name, seasonStr+" сезон"),
			"voice_name": selected.tr.Name,
		}
		rows = append(rows, row)
		labels = append(labels, name)
	}

	if len(rows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type":  "episode",
			"voice": voiceRows,
			"data":  rows,
		})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for _, v := range voiceRows {
		getsTVAppendVoiceHTML(&sb, v)
	}
	sb.WriteString(`</div><div class="videos__line">`)
	for i, row := range rows {
		s, _ := row["s"].(int)
		e, _ := row["e"].(int)
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, s, e)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// video resolves stream URLs via the Turnstile solver microservice and returns them.
func (f *flixcdnChecker) video(w http.ResponseWriter, r *http.Request, links *proxylink.Manager) {
	q := r.URL.Query()
	id := parseInt64(q.Get("id"))
	if id == 0 {
		// Back-compat: old client URLs used pid=
		id = parseInt64(q.Get("pid"))
	}
	translation := strings.TrimSpace(q.Get("t"))
	seasonStr := strings.TrimSpace(q.Get("s"))
	episodeStr := strings.TrimSpace(q.Get("e"))

	if id == 0 {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	// Build the player URL with translation/season/episode in the query string.
	// The upstream Turnstile token is bound to the combination the page was loaded
	// with — POSTing /api/player/files with a different translation/episode against
	// a token issued for the page default returns HTTP 403 "captcha failed".
	pagePath := fmt.Sprintf("/show/%d", id)
	pageParams := url.Values{}
	if translation != "" {
		pageParams.Set("translation", translation)
	}
	if seasonStr != "" {
		pageParams.Set("season", seasonStr)
	}
	if episodeStr != "" {
		pageParams.Set("episode", episodeStr)
	}
	if len(pageParams) > 0 {
		pagePath += "?" + pageParams.Encode()
	}
	pageURL := f.playerHost + pagePath

	// Check cache
	cacheKey := fmt.Sprintf("%d:%s:%s:%s", id, translation, seasonStr, episodeStr)
	f.cacheMu.RLock()
	if entry, ok := f.cache[cacheKey]; ok && time.Since(entry.resolvedAt) < flixcdnCacheTTL {
		f.cacheMu.RUnlock()
		f.writeVideoResponse(w, r, entry.fileString, links)
		return
	}
	f.cacheMu.RUnlock()

	req := flixcdnSolverRequest{
		PageURL:     pageURL,
		RefererURL:  f.refererURL,
		ID:          id,
		Translation: translation,
		Season:      seasonStr,
		Episode:     episodeStr,
	}

	fileString, ok := flixcdnResolveViaBrowser(r.Context(), req)
	if !ok || fileString == "" {
		log.Warn().Str("url", pageURL).Msg("flixcdn: browser resolve failed")
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	// Cache the result
	f.cacheMu.Lock()
	f.cache[cacheKey] = &flixcdnCacheEntry{
		fileString: fileString,
		resolvedAt: time.Now(),
	}
	f.cacheMu.Unlock()

	f.writeVideoResponse(w, r, fileString, links)
}

// writeVideoResponse parses the Playerjs file_string and writes the response.
func (f *flixcdnChecker) writeVideoResponse(w http.ResponseWriter, r *http.Request, fileString string, links *proxylink.Manager) {
	streams := flixcdnParseFileString(fileString)
	if len(streams) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	// Proxy streams through /proxy/ with correct headers
	reqIP := clientIP(r)
	for i, s := range streams {
		if links != nil && s.URL != "" {
			streams[i].URL = links.EncryptURIWithHeaders(s.URL, reqIP, "flixcdn", map[string]string{
				"Origin":  f.playerHost,
				"Referer": f.playerHost + "/",
			})
			streams[i].URL = streamHostFromRequest(r) + "/proxy/" + streams[i].URL
		}
	}

	// Build quality map
	qualMap := make(map[string]string)
	for _, s := range streams {
		qualMap[s.Quality] = s.URL
	}

	// Pick first (best) stream as main
	row := map[string]any{
		"method": "play",
		"url":    streams[0].URL,
		"stream": streams[0].URL,
	}
	if len(qualMap) > 1 {
		row["quality"] = qualMap
		row["qualitys"] = qualMap
	}

	writeJSON(w, http.StatusOK, row)
}

type flixcdnStream struct {
	Quality string
	URL     string
}

// flixcdnParseFileString parses Playerjs format file strings.
// Format: [1080p]https://...m3u8,[720p]https://...m3u8,...
// Or: [1080p]https://...m3u8 or https://...m3u8 (single quality)
func flixcdnParseFileString(fs string) []flixcdnStream {
	fs = strings.TrimSpace(fs)
	if fs == "" {
		return nil
	}

	var streams []flixcdnStream

	// Split by comma, but be careful with URLs containing commas (unlikely for m3u8)
	parts := strings.Split(fs, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		// Check for [quality]url format
		if strings.HasPrefix(part, "[") {
			end := strings.Index(part, "]")
			if end > 0 {
				quality := part[1:end]
				rawURL := strings.TrimSpace(part[end+1:])
				if rawURL != "" {
					streams = append(streams, flixcdnStream{
						Quality: quality,
						URL:     rawURL,
					})
				}
				continue
			}
		}

		// Plain URL without quality label
		if strings.HasPrefix(part, "http") {
			streams = append(streams, flixcdnStream{
				Quality: "auto",
				URL:     part,
			})
		}
	}

	return streams
}
