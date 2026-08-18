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
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/proxylink"

	"github.com/rs/zerolog/log"
)

// ---------- API response types ----------

type hdvbAPIItem struct {
	Translator     string              `json:"translator"`
	Type           string              `json:"type"` // "movie" or "serial"
	IframeURL      string              `json:"iframe_url"`
	Quality        string              `json:"quality"` // "HDRip", "WEBRip", etc.
	SerialEpisodes []hdvbSerialEpisode `json:"serial_episodes"`
}

type hdvbSerialEpisode struct {
	SeasonNumber int   `json:"season_number"`
	Episodes     []int `json:"episodes"`
}

// HDVBPlayer playlist response — nested Folder structure for serials.
// Note: id and episode arrive as JSON strings (e.g. "1", "1-1",
// "3a781b..."), not numbers, so the struct fields are strings even though
// the season/episode discriminators inside are usually numeric.
type hdvbFolder struct {
	ID      string       `json:"id"`
	Episode string       `json:"episode"`
	Title   string       `json:"title"`
	File    string       `json:"file"`
	Folder  []hdvbFolder `json:"folder"`
}

// hdvbFolderEpisodeNum extracts the episode number from a folder. Prefers
// the explicit Episode field; falls back to the leading integer in ID
// (e.g. "1-1" → 1, "1" → 1).
func hdvbFolderEpisodeNum(f hdvbFolder) int {
	if n, err := strconv.Atoi(strings.TrimSpace(f.Episode)); err == nil && n > 0 {
		return n
	}
	id := strings.TrimSpace(f.ID)
	if dash := strings.Index(id, "-"); dash > 0 {
		id = id[:dash]
	}
	if n, err := strconv.Atoi(id); err == nil && n > 0 {
		return n
	}
	return 0
}

// hdvbFolderSeasonNum returns the leading integer from a folder's ID
// (e.g. "1" → 1, "10" → 10).
func hdvbFolderSeasonNum(f hdvbFolder) int {
	id := strings.TrimSpace(f.ID)
	if dash := strings.Index(id, "-"); dash > 0 {
		id = id[:dash]
	}
	n, _ := strconv.Atoi(id)
	return n
}

// ---------- checker ----------

type hdvbChecker struct {
	client     *http.Client
	apiHost    string
	playerHost string // optional rewrite target for iframe_url (e.g. https://vid1733431681.entouaedon.com)
	token      string
}

// hdvbIframeHostRe matches the scheme+host prefix of a URL so we can swap it
// out. Anchored at the start so we never touch a path component shaped like
// a URL.
var hdvbIframeHostRe = regexp.MustCompile(`^https?://[^/]+`)

// hdvbVidPrefixRe matches the `vidNNNN.` subdomain prefix HDVB rotates onto
// content hosts (e.g. vid1733431681.entouaedon.com).
var hdvbVidPrefixRe = regexp.MustCompile(`^vid\d+\.`)

// hdvbExtractOrigin returns the scheme+host of a URL, e.g.
// "https://vid1733431681.entouaedon.com/serial/.../iframe" → "https://vid1733431681.entouaedon.com".
func hdvbExtractOrigin(rawURL string) string {
	return hdvbIframeHostRe.FindString(rawURL)
}

// hdvbDeriveBareHref returns the API domain without the `vidNNNN.` prefix,
// e.g. "https://vid1733431681.entouaedon.com/..." → "entouaedon.com". Used
// as a fallback when playerConfigs.href is missing (serial flow).
func hdvbDeriveBareHref(rawURL string) string {
	host := hdvbExtractOrigin(rawURL)
	if host == "" {
		return ""
	}
	// strip scheme
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimPrefix(host, "http://")
	return hdvbVidPrefixRe.ReplaceAllString(host, "")
}

func NewHDVBChecker(cfg config.Config) *hdvbChecker {
	apiHost := strings.TrimSpace(cfg.Online.HDVB.APIHost)
	if apiHost == "" {
		apiHost = "https://apivb.com"
	}
	apiHost = strings.TrimRight(apiHost, "/")

	playerHost := strings.TrimSpace(cfg.Online.HDVB.PlayerHost)
	playerHost = strings.TrimRight(playerHost, "/")

	return &hdvbChecker{
		client:     httpclient.NewForBalancer("hdvb", 12*time.Second),
		apiHost:    apiHost,
		playerHost: playerHost,
		token:      strings.TrimSpace(cfg.Online.HDVB.Token),
	}
}

// fixframe rewrites the host of an iframe URL to playerHost. The HDVB API
// keeps returning the iframe_url with the host that was current when the
// movie was indexed (e.g. vid1778586857.fotpro135alto.com), but those rotate
// out and only the active host (configured via player_host) reliably resolves
// requests from non-RU IPs. Same behaviour as lampac-nextgen's static
// helper.
func (h *hdvbChecker) fixframe(iframe string) string {
	if h.playerHost == "" || iframe == "" {
		return iframe
	}
	return hdvbIframeHostRe.ReplaceAllString(iframe, h.playerHost)
}

func (h *hdvbChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := strings.Trim(strings.TrimPrefix(strings.ToLower(r.URL.Path), "/lite/"), "/")

		// /lite/hdvb/serial — on-demand episode stream resolution (fallback).
		if raw == "hdvb/serial" {
			if h.token == "" {
				writeJSON(w, http.StatusOK, map[string]any{})
				return
			}
			h.serialResolve(w, r, links)
			return
		}

		if parseBoolParam(r.URL.Query().Get("checksearch")) {
			show, quality := h.checkSearch(r)
			writeCheckSearchResponse(w, show, quality)
			return
		}

		if h.token == "" {
			writeGetsTVEmpty(w, parseBoolParam(r.URL.Query().Get("rjson")))
			return
		}

		h.index(w, r, links)
	}
}

// ---------- checksearch ----------

func (h *hdvbChecker) checkSearch(req *http.Request) (bool, string) {
	if h.token == "" {
		return false, ""
	}

	kinopoiskID := strings.TrimSpace(req.URL.Query().Get("kinopoisk_id"))
	if kinopoiskID == "" {
		return false, ""
	}
	if _, err := strconv.ParseInt(kinopoiskID, 10, 64); err != nil {
		return false, ""
	}

	items, ok := h.fetchAPI(req.Context(), kinopoiskID)
	if !ok || len(items) == 0 {
		return false, ""
	}

	best := ""
	for _, it := range items {
		q := normalizeQualityBadge(it.Quality)
		if qualityBadgeRank(q) > qualityBadgeRank(best) {
			best = q
		}
	}
	return true, best
}

// ---------- API fetch ----------

func (h *hdvbChecker) fetchAPI(ctx context.Context, kinopoiskID string) ([]hdvbAPIItem, bool) {
	u, err := url.Parse(h.apiHost + "/api/videos.json")
	if err != nil {
		return nil, false
	}
	qs := url.Values{}
	qs.Set("token", h.token)
	qs.Set("id_kp", kinopoiskID)
	u.RawQuery = qs.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, false
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	httpReq.Header.Set("X-Lampac-Go", "1")

	resp, err := h.client.Do(httpReq)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, false
	}

	var items []hdvbAPIItem
	if err := stdjson.Unmarshal(body, &items); err != nil {
		return nil, false
	}
	return items, true
}

// ---------- iframe → playerConfigs → playlist ----------

// hdvbFetchIframe fetches the iframe page and extracts playerConfigs JSON.
func (h *hdvbChecker) hdvbFetchIframe(ctx context.Context, iframeURL string) (ZetflixPlayerConfig, bool) {
	iframeURL = h.fixframe(iframeURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, iframeURL, nil)
	if err != nil {
		return ZetflixPlayerConfig{}, false
	}
	// Headers mirror lampac-nextgen HDVB Controller.cs — sec-fetch-*
	// signals this is a cross-site iframe navigation (matches what a real
	// browser would send when loading the player frame). Referer is the
	// HDVB upstream site (moviehot.one), set on the module headers in
	// nextgen as `referer: encrypt:kwwsv=22prylhode1rqh2` (Caesar-3 cipher
	// of `https://moviehot.one/`).
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7")
	req.Header.Set("Referer", "https://moviehot.one/")
	req.Header.Set("Sec-Fetch-Dest", "iframe")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "cross-site")

	// CDN replies 503 intermittently when the iframe host is busy or our
	// IP is rate-limited; retry briefly before giving up so a transient
	// burst doesn't surface as a hard error to the user.
	var resp *http.Response
	var body []byte
	for attempt := 1; attempt <= 3; attempt++ {
		var err error
		resp, err = h.client.Do(req.Clone(ctx))
		if err != nil {
			log.Warn().Err(err).Int("attempt", attempt).Str("url", iframeURL[:min(len(iframeURL), 80)]).Msg("hdvb: iframe request failed")
			if ctx.Err() != nil {
				return ZetflixPlayerConfig{}, false
			}
			time.Sleep(time.Duration(attempt*300) * time.Millisecond)
			continue
		}
		body, err = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if err != nil {
			log.Warn().Err(err).Int("attempt", attempt).Msg("hdvb: iframe body read failed")
			time.Sleep(time.Duration(attempt*300) * time.Millisecond)
			continue
		}
		if resp.StatusCode >= 500 && resp.StatusCode < 600 {
			log.Warn().Int("status", resp.StatusCode).Int("attempt", attempt).Str("url", iframeURL[:min(len(iframeURL), 80)]).Msg("hdvb: iframe 5xx, retrying")
			time.Sleep(time.Duration(attempt*300) * time.Millisecond)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			log.Warn().Int("status", resp.StatusCode).Str("url", iframeURL[:min(len(iframeURL), 80)]).Msg("hdvb: iframe non-200")
			return ZetflixPlayerConfig{}, false
		}
		return ZetflixExtractPlayerConfig(string(body))
	}
	if resp != nil {
		log.Warn().Int("status", resp.StatusCode).Str("url", iframeURL[:min(len(iframeURL), 80)]).Msg("hdvb: iframe failed after retries")
	}
	return ZetflixPlayerConfig{}, false
}

// hdvbFetchPlaylist POSTs to the HDVBPlayer playlist API and returns the raw response body.
//
// HDVB iframes come in two shapes:
//   - Movie / leaf episode: pc.File starts with "~" (encoded blob), pc.Href is
//     the bare API domain (e.g. "entouaedon.com"). POST goes to
//     https://vid11.{href}/playlist/{file[1:]}.txt and returns the m3u8 URL.
//   - Serial top-level: pc.File is already a path like "/playlist/X.txt",
//     pc.Href is absent. POST goes to the same host as the iframe and
//     returns a nested JSON tree of seasons/episodes.
func (h *hdvbChecker) hdvbFetchPlaylist(ctx context.Context, pc ZetflixPlayerConfig, iframeURL string) (string, bool) {
	var target, origin string
	switch {
	case strings.HasPrefix(pc.File, "~"):
		href := strings.TrimSpace(pc.Href)
		if href == "" {
			// Derive href from iframe URL by stripping `vidNNNN.` prefix
			// (e.g. vid1733431681.entouaedon.com → entouaedon.com).
			if h := hdvbDeriveBareHref(iframeURL); h != "" {
				href = h
			} else {
				return "", false
			}
		}
		fileParam := pc.File[1:]
		target = "https://vid11." + href + "/playlist/" + fileParam + ".txt"
		origin = "https://" + href
	case strings.HasPrefix(pc.File, "/"):
		// Serial top-level: POST directly to iframe host.
		ifHost := hdvbExtractOrigin(iframeURL)
		if ifHost == "" {
			return "", false
		}
		target = ifHost + pc.File
		origin = ifHost
	default:
		return "", false
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-TOKEN", pc.Key)
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-site")
	req.Header.Set("Referer", origin+"/")
	req.Header.Set("Origin", origin)

	// Retry on 5xx — the playlist API often returns 503 when the CDN host
	// is hot. Each retry forms a fresh request via Clone so headers stay
	// identical. We deliberately do NOT regenerate file/key here: the pair
	// is single-use in some windows, but a 5xx means the server didn't
	// accept it at all, so it's safe to replay.
	var result string
	for attempt := 1; attempt <= 3; attempt++ {
		resp, err := h.client.Do(req.Clone(ctx))
		if err != nil {
			log.Warn().Err(err).Int("attempt", attempt).Str("url", target).Msg("hdvb: playlist API request failed")
			if ctx.Err() != nil {
				return "", false
			}
			time.Sleep(time.Duration(attempt*300) * time.Millisecond)
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		_ = resp.Body.Close()

		if resp.StatusCode >= 500 && resp.StatusCode < 600 {
			log.Warn().Int("status", resp.StatusCode).Int("attempt", attempt).Str("url", target).Msg("hdvb: playlist API 5xx, retrying")
			time.Sleep(time.Duration(attempt*300) * time.Millisecond)
			continue
		}
		result = strings.TrimSpace(string(body))
		if result == "" || resp.StatusCode != http.StatusOK {
			log.Warn().Int("status", resp.StatusCode).Str("url", target).Msg("hdvb: playlist API empty/error")
			return "", false
		}
		// Anything that's not an HTTP(S) URL or JSON array is an upstream
		// error code (e.g. literal "10" for rate-limit / IP block). Log a
		// short snippet so we can diagnose remotely without hosing the
		// logs on a normal stream payload.
		if !strings.HasPrefix(result, "http") && !strings.HasPrefix(result, "[") {
			snippet := result
			if len(snippet) > 120 {
				snippet = snippet[:120]
			}
			log.Debug().Str("body", snippet).Int("len", len(result)).Msg("hdvb: playlist API non-stream response")
		}
		return result, true
	}
	log.Warn().Str("url", target).Msg("hdvb: playlist API failed after retries")
	return "", false
}

// hdvbResolveMovieStream: iframe → playerConfigs → playlist → stream URL.
func (h *hdvbChecker) hdvbResolveMovieStream(ctx context.Context, iframeURL string) (string, bool) {
	iframeURL = h.fixframe(iframeURL)
	pc, ok := h.hdvbFetchIframe(ctx, iframeURL)
	if !ok || !strings.HasPrefix(pc.File, "~") {
		return "", false
	}
	result, ok := h.hdvbFetchPlaylist(ctx, pc, iframeURL)
	if !ok {
		return "", false
	}
	// For movies, the playlist response is a direct URL.
	if strings.HasPrefix(result, "http") {
		return result, true
	}
	return "", false
}

// hdvbResolveSerialPlaylist: iframe → playerConfigs → playlist → folder tree.
//
// On the returned ZetflixPlayerConfig: when the upstream iframe omitted the
// `href` field (typical for top-level serial iframes), we fill it in from
// the iframe URL so subsequent leaf-file resolves (hdvbResolveFile) point
// at the right vid11.HOST.
func (h *hdvbChecker) hdvbResolveSerialPlaylist(ctx context.Context, iframeURL string) ([]hdvbFolder, ZetflixPlayerConfig, bool) {
	iframeURL = h.fixframe(iframeURL)
	pc, ok := h.hdvbFetchIframe(ctx, iframeURL)
	if !ok {
		return nil, pc, false
	}
	// Serial top-level file is "/playlist/...txt"; movie/leaf file is "~...".
	if !strings.HasPrefix(pc.File, "~") && !strings.HasPrefix(pc.File, "/") {
		return nil, pc, false
	}

	// Backfill href for serial iframes where playerConfigs lacks it. Leaf
	// resolution (translator → m3u8) needs it to address vid11.{href}.
	if pc.Href == "" {
		pc.Href = hdvbDeriveBareHref(iframeURL)
	}

	result, ok := h.hdvbFetchPlaylist(ctx, pc, iframeURL)
	if !ok {
		return nil, pc, false
	}

	// For serials, the playlist response is a JSON array of Folder objects.
	var folders []hdvbFolder
	if err := stdjson.Unmarshal([]byte(result), &folders); err != nil {
		// Maybe it's a direct URL (fallback).
		if strings.HasPrefix(result, "http") {
			log.Debug().Msg("hdvb: serial playlist returned URL instead of folder tree")
			return nil, pc, false
		}
		log.Warn().Err(err).Msg("hdvb: serial playlist JSON parse failed")
		return nil, pc, false
	}
	return folders, pc, true
}

// hdvbResolveFile: resolve a Folder file field (starts with ~) into a stream URL.
func (h *hdvbChecker) hdvbResolveFile(ctx context.Context, pc ZetflixPlayerConfig, file string) (string, bool) {
	if !strings.HasPrefix(file, "~") || pc.Href == "" {
		return "", false
	}
	fileParam := file[1:]
	target := "https://vid11." + pc.Href + "/playlist/" + fileParam + ".txt"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-TOKEN", pc.Key)
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-site")
	req.Header.Set("Referer", "https://"+pc.Href+"/")
	req.Header.Set("Origin", "https://"+pc.Href)

	// Retry on 5xx — same rationale as hdvbFetchPlaylist above.
	for attempt := 1; attempt <= 3; attempt++ {
		resp, err := h.client.Do(req.Clone(ctx))
		if err != nil {
			if ctx.Err() != nil {
				return "", false
			}
			log.Warn().Err(err).Int("attempt", attempt).Str("url", target).Msg("hdvb: leaf playlist request failed")
			time.Sleep(time.Duration(attempt*300) * time.Millisecond)
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
		_ = resp.Body.Close()

		if resp.StatusCode >= 500 && resp.StatusCode < 600 {
			log.Warn().Int("status", resp.StatusCode).Int("attempt", attempt).Str("url", target).Msg("hdvb: leaf playlist 5xx, retrying")
			time.Sleep(time.Duration(attempt*300) * time.Millisecond)
			continue
		}
		result := strings.TrimSpace(string(body))
		if result == "" || resp.StatusCode != http.StatusOK || !strings.HasPrefix(result, "http") {
			log.Warn().Int("status", resp.StatusCode).Str("url", target).Int("len", len(result)).Msg("hdvb: leaf playlist non-URL response")
			return "", false
		}
		return result, true
	}
	return "", false
}

// ---------- index (main handler) ----------

func (h *hdvbChecker) index(w http.ResponseWriter, r *http.Request, links *proxylink.Manager) {
	q := r.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	kinopoiskID := strings.TrimSpace(q.Get("kinopoisk_id"))
	if kinopoiskID == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	serial := strings.TrimSpace(q.Get("serial")) == "1"
	t, tSet := getsTVQueryInt(q.Get("t"))
	if !tSet {
		t = -1
	}
	s, sSet := getsTVQueryInt(q.Get("s"))
	if !sSet {
		s = -1
	}

	items, ok := h.fetchAPI(r.Context(), kinopoiskID)
	if !ok || len(items) == 0 {
		// distinguishes a token/catalog miss (apivb has nothing for this kp under THIS token) from a
		// downstream resolve failure — the usual «есть в Лампе, нет по capi» split.
		log.Debug().Str("kp", kinopoiskID).Bool("apiOK", ok).Int("items", len(items)).Bool("tok", h.token != "").Msg("hdvb: empty API result")
		writeGetsTVEmpty(w, rjson)
		return
	}

	log.Debug().Int("items", len(items)).Str("type", items[0].Type).Msg("hdvb: API response")

	// Determine type from API response.
	isSerial := serial
	for _, item := range items {
		if strings.EqualFold(item.Type, "serial") {
			isSerial = true
			break
		}
	}

	if !isSerial {
		h.writeMovie(w, r, rjson, title, originalTitle, items, links)
		return
	}

	if s == -1 {
		h.writeSeason(w, r, rjson, kinopoiskID, title, originalTitle, items)
		return
	}

	h.writeEpisode(w, r, rjson, kinopoiskID, title, originalTitle, s, t, items, links)
}

// ---------- movie ----------

func (h *hdvbChecker) writeMovie(
	w http.ResponseWriter, r *http.Request, rjson bool,
	title, originalTitle string,
	items []hdvbAPIItem,
	links *proxylink.Manager,
) {
	baseTitle := getsTVJoinName(title, originalTitle)
	rows := make([]map[string]any, 0, len(items))
	labels := make([]string, 0, len(items))

	for _, item := range items {
		iframeURL := strings.TrimSpace(item.IframeURL)
		if iframeURL == "" {
			continue
		}
		translator := strings.TrimSpace(item.Translator)
		if translator == "" {
			translator = "Неизвестный"
		}

		streamURL, ok := h.hdvbResolveMovieStream(r.Context(), iframeURL)
		if !ok || streamURL == "" {
			log.Debug().Str("translator", translator).Msg("hdvb: movie stream resolve failed")
			continue
		}

		proxyURL := h.proxyStream(r, streamURL, links)

		row := map[string]any{
			"method": "play",
			"url":    proxyURL,
			"stream": proxyURL,
			"name":   translator,
			"title":  fmt.Sprintf("%s (%s)", baseTitle, translator),
		}
		rows = append(rows, row)
		labels = append(labels, translator)
	}

	if len(rows) == 0 {
		// apivb HAD the film (items>0) but every translator's iframe→playlist resolve failed — i.e. the
		// CDN (sevstar/…) likely blocked the server IP or changed its scheme. NOT a kp-id/token problem.
		log.Debug().Int("items", len(items)).Msg("hdvb: movie — all translators failed to resolve (CDN block/scheme change?)")
		writeGetsTVEmpty(w, rjson)
		return
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

// ---------- serial: seasons ----------

func (h *hdvbChecker) writeSeason(
	w http.ResponseWriter, r *http.Request, rjson bool,
	kinopoiskID, title, originalTitle string,
	items []hdvbAPIItem,
) {
	host := hostFromRequest(r)

	// Collect all unique seasons across all voices.
	seasonSet := make(map[int]struct{})
	for _, item := range items {
		for _, se := range item.SerialEpisodes {
			if se.SeasonNumber > 0 {
				seasonSet[se.SeasonNumber] = struct{}{}
			}
		}
	}

	seasonNums := make([]int, 0, len(seasonSet))
	for s := range seasonSet {
		seasonNums = append(seasonNums, s)
	}
	sort.Ints(seasonNums)

	if len(seasonNums) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	data := make([]map[string]any, 0, len(seasonNums))
	sLabels := make([]string, 0, len(seasonNums))
	for _, sn := range seasonNums {
		name := strconv.Itoa(sn) + " сезон"
		link := host + "/lite/hdvb?rjson=" + getsTVBool(rjson) +
			"&kinopoisk_id=" + url.QueryEscape(kinopoiskID) +
			"&title=" + url.QueryEscape(title) +
			"&original_title=" + url.QueryEscape(originalTitle) +
			"&serial=1" +
			"&s=" + strconv.Itoa(sn)
		data = append(data, map[string]any{
			"method": "link",
			"id":     sn,
			"url":    link,
			"name":   name,
		})
		sLabels = append(sLabels, name)
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "season",
			"data": data,
		})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range data {
		getsTVAppendSeasonHTML(&sb, row, sLabels[i], i == 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// ---------- serial: episodes ----------

func (h *hdvbChecker) writeEpisode(
	w http.ResponseWriter, r *http.Request, rjson bool,
	kinopoiskID, title, originalTitle string,
	season, voiceIdx int,
	items []hdvbAPIItem,
	links *proxylink.Manager,
) {
	host := hostFromRequest(r)
	baseTitle := getsTVJoinName(title, originalTitle)

	// Filter voices that have the requested season.
	type voiceInfo struct {
		idx        int
		translator string
		episodes   []int
		iframeURL  string
	}
	voices := make([]voiceInfo, 0, len(items))
	for i, item := range items {
		for _, se := range item.SerialEpisodes {
			if se.SeasonNumber == season && len(se.Episodes) > 0 {
				voices = append(voices, voiceInfo{
					idx:        i,
					translator: strings.TrimSpace(item.Translator),
					episodes:   se.Episodes,
					iframeURL:  strings.TrimSpace(item.IframeURL),
				})
				break
			}
		}
	}

	if len(voices) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Default voice.
	if voiceIdx < 0 || voiceIdx >= len(voices) {
		voiceIdx = 0
	}
	activeVoice := voices[voiceIdx]

	// Build voice selector.
	voiceRows := make([]map[string]any, 0, len(voices))
	for i, v := range voices {
		voiceRows = append(voiceRows, map[string]any{
			"method": "link",
			"name":   v.translator,
			"active": i == voiceIdx,
			"url": host + "/lite/hdvb?rjson=" + getsTVBool(rjson) +
				"&kinopoisk_id=" + url.QueryEscape(kinopoiskID) +
				"&title=" + url.QueryEscape(title) +
				"&original_title=" + url.QueryEscape(originalTitle) +
				"&serial=1" +
				"&s=" + strconv.Itoa(season) +
				"&t=" + strconv.Itoa(i),
		})
	}

	// Resolve the playlist tree for the active voice.
	if activeVoice.iframeURL == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	folders, pc, ok := h.hdvbResolveSerialPlaylist(r.Context(), activeVoice.iframeURL)
	if !ok || len(folders) == 0 {
		log.Debug().Str("translator", activeVoice.translator).Msg("hdvb: serial playlist resolve failed, falling back to episode list from API")
		// Fallback: output episode list without resolved streams — user can try each.
		h.writeEpisodeFallback(w, r, rjson, baseTitle, season, voiceIdx, activeVoice, voiceRows, links)
		return
	}

	log.Debug().Int("folders", len(folders)).Msg("hdvb: serial playlist resolved")

	// Navigate the folder tree to find episodes for the requested season and voice.
	// Tree structure: [season_folder] → [episode_folder] → [voice_folder with file]
	var episodeFolders []hdvbFolder
	for _, sf := range folders {
		if hdvbFolderSeasonNum(sf) == season && len(sf.Folder) > 0 {
			episodeFolders = sf.Folder
			break
		}
	}
	// If no season match, try flat structure (some playlists skip season nesting).
	if len(episodeFolders) == 0 {
		episodeFolders = folders
	}

	type episodeRow struct {
		data    map[string]any
		label   string
		episode int
	}
	rows := make([]episodeRow, 0, len(episodeFolders))

	for _, epFolder := range episodeFolders {
		epNum := hdvbFolderEpisodeNum(epFolder)

		// Find the voice folder matching our translator.
		var file string
		for _, vf := range epFolder.Folder {
			if strings.EqualFold(strings.TrimSpace(vf.Title), activeVoice.translator) {
				file = vf.File
				break
			}
		}
		// If no voice match, try the first one with a file.
		if file == "" {
			for _, vf := range epFolder.Folder {
				if vf.File != "" {
					file = vf.File
					break
				}
			}
		}
		// If episode folder itself has a file (flat structure).
		if file == "" && epFolder.File != "" {
			file = epFolder.File
		}
		if file == "" {
			continue
		}

		// Resolve the file to a stream URL.
		var streamURL string
		if strings.HasPrefix(file, "~") {
			resolved, ok := h.hdvbResolveFile(r.Context(), pc, file)
			if ok {
				streamURL = resolved
			}
		} else if strings.HasPrefix(file, "http") {
			streamURL = file
		}

		if streamURL == "" {
			continue
		}

		proxyURL := h.proxyStream(r, streamURL, links)
		name := strconv.Itoa(epNum) + " серия"

		rows = append(rows, episodeRow{
			data: map[string]any{
				"method": "play",
				"url":    proxyURL,
				"stream": proxyURL,
				"s":      season,
				"e":      epNum,
				"name":   name,
				"title":  fmt.Sprintf("%s (%s)", baseTitle, name),
			},
			label:   name,
			episode: epNum,
		})
	}

	sort.Slice(rows, func(i, j int) bool {
		return rows[i].episode < rows[j].episode
	})

	if len(rows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	data := make([]map[string]any, 0, len(rows))
	labels := make([]string, 0, len(rows))
	seasons := make([]int, 0, len(rows))
	episodes := make([]int, 0, len(rows))
	for _, row := range rows {
		data = append(data, row.data)
		labels = append(labels, row.label)
		seasons = append(seasons, season)
		episodes = append(episodes, row.episode)
	}

	if rjson {
		payload := map[string]any{
			"type": "episode",
			"data": data,
		}
		if len(voiceRows) > 0 {
			payload["voice"] = voiceRows
		}
		writeJSON(w, http.StatusOK, payload)
		return
	}

	var sb strings.Builder
	if len(voiceRows) > 0 {
		sb.WriteString(`<div class="videos__line">`)
		for _, row := range voiceRows {
			getsTVAppendVoiceHTML(&sb, row)
		}
		sb.WriteString(`</div>`)
	}
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range data {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, seasons[i], episodes[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// writeEpisodeFallback writes episodes from API data when playlist tree resolution fails.
// Each episode links to an iframe that will be resolved on-demand (method:"call").
func (h *hdvbChecker) writeEpisodeFallback(
	w http.ResponseWriter, r *http.Request, rjson bool,
	baseTitle string,
	season, voiceIdx int,
	voice struct {
		idx        int
		translator string
		episodes   []int
		iframeURL  string
	},
	voiceRows []map[string]any,
	links *proxylink.Manager,
) {
	host := hostFromRequest(r)

	epNums := make([]int, len(voice.episodes))
	copy(epNums, voice.episodes)
	sort.Ints(epNums)

	type episodeRow struct {
		data    map[string]any
		label   string
		episode int
	}
	rows := make([]episodeRow, 0, len(epNums))
	for _, ep := range epNums {
		name := strconv.Itoa(ep) + " серия"
		link := host + "/lite/hdvb/serial?kinopoisk_id=" + url.QueryEscape(r.URL.Query().Get("kinopoisk_id")) +
			"&title=" + url.QueryEscape(r.URL.Query().Get("title")) +
			"&original_title=" + url.QueryEscape(r.URL.Query().Get("original_title")) +
			"&s=" + strconv.Itoa(season) +
			"&e=" + strconv.Itoa(ep) +
			"&t=" + strconv.Itoa(voiceIdx)

		rows = append(rows, episodeRow{
			data: map[string]any{
				"method": "call",
				"url":    link,
				"s":      season,
				"e":      ep,
				"name":   name,
				"title":  fmt.Sprintf("%s (%s)", baseTitle, name),
			},
			label:   name,
			episode: ep,
		})
	}

	if len(rows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	data := make([]map[string]any, 0, len(rows))
	labels := make([]string, 0, len(rows))
	seasons := make([]int, 0, len(rows))
	episodes := make([]int, 0, len(rows))
	for _, row := range rows {
		data = append(data, row.data)
		labels = append(labels, row.label)
		seasons = append(seasons, season)
		episodes = append(episodes, row.episode)
	}

	if rjson {
		payload := map[string]any{
			"type": "episode",
			"data": data,
		}
		if len(voiceRows) > 0 {
			payload["voice"] = voiceRows
		}
		writeJSON(w, http.StatusOK, payload)
		return
	}

	var sb strings.Builder
	if len(voiceRows) > 0 {
		sb.WriteString(`<div class="videos__line">`)
		for _, row := range voiceRows {
			getsTVAppendVoiceHTML(&sb, row)
		}
		sb.WriteString(`</div>`)
	}
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range data {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, seasons[i], episodes[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// ---------- serial resolve (on-demand) ----------

// serialResolve handles /lite/hdvb/serial — resolves a single episode stream on demand.
// Used as fallback when the playlist tree couldn't be resolved upfront.
func (h *hdvbChecker) serialResolve(w http.ResponseWriter, r *http.Request, links *proxylink.Manager) {
	q := r.URL.Query()
	kinopoiskID := strings.TrimSpace(q.Get("kinopoisk_id"))
	season, _ := strconv.Atoi(strings.TrimSpace(q.Get("s")))
	episode, _ := strconv.Atoi(strings.TrimSpace(q.Get("e")))
	voiceIdx, _ := strconv.Atoi(strings.TrimSpace(q.Get("t")))

	if kinopoiskID == "" || season <= 0 || episode <= 0 {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	items, ok := h.fetchAPI(r.Context(), kinopoiskID)
	if !ok || len(items) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	// Find the voice with the requested season.
	idx := 0
	for i, item := range items {
		for _, se := range item.SerialEpisodes {
			if se.SeasonNumber == season {
				if idx == voiceIdx {
					// Found our voice — resolve its iframe → playlist → episode stream.
					iframeURL := strings.TrimSpace(item.IframeURL)
					if iframeURL == "" {
						writeJSON(w, http.StatusOK, map[string]any{})
						return
					}
					folders, pc, ok := h.hdvbResolveSerialPlaylist(r.Context(), iframeURL)
					if !ok || len(folders) == 0 {
						log.Debug().Int("voice", i).Msg("hdvb: serial resolve failed for on-demand request")
						writeJSON(w, http.StatusOK, map[string]any{})
						return
					}

					// Navigate tree: season → episode → voice.
					streamURL := h.findEpisodeStream(r.Context(), folders, pc, season, episode, strings.TrimSpace(item.Translator))
					if streamURL == "" {
						writeJSON(w, http.StatusOK, map[string]any{})
						return
					}

					proxyURL := h.proxyStream(r, streamURL, links)
					writeJSON(w, http.StatusOK, map[string]any{
						"method": "play",
						"url":    proxyURL,
						"title":  strings.TrimSpace(q.Get("title")),
					})
					return
				}
				idx++
				break
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{})
}

// findEpisodeStream navigates the folder tree to find a stream URL for a specific episode.
func (h *hdvbChecker) findEpisodeStream(ctx context.Context, folders []hdvbFolder, pc ZetflixPlayerConfig, season, episode int, translator string) string {
	// Find season folder.
	var episodeFolders []hdvbFolder
	for _, sf := range folders {
		if hdvbFolderSeasonNum(sf) == season && len(sf.Folder) > 0 {
			episodeFolders = sf.Folder
			break
		}
	}
	if len(episodeFolders) == 0 {
		episodeFolders = folders
	}

	for _, epFolder := range episodeFolders {
		epNum := hdvbFolderEpisodeNum(epFolder)
		if epNum != episode {
			continue
		}

		// Find voice folder.
		var file string
		for _, vf := range epFolder.Folder {
			if strings.EqualFold(strings.TrimSpace(vf.Title), translator) {
				file = vf.File
				break
			}
		}
		if file == "" {
			for _, vf := range epFolder.Folder {
				if vf.File != "" {
					file = vf.File
					break
				}
			}
		}
		if file == "" && epFolder.File != "" {
			file = epFolder.File
		}
		if file == "" {
			return ""
		}

		if strings.HasPrefix(file, "~") {
			resolved, ok := h.hdvbResolveFile(ctx, pc, file)
			if ok {
				return resolved
			}
		} else if strings.HasPrefix(file, "http") {
			return file
		}
		return ""
	}
	return ""
}

// ---------- proxy helper ----------

func (h *hdvbChecker) proxyStream(r *http.Request, rawURL string, links *proxylink.Manager) string {
	// HDVB's CDN edge (cdnNNNN.sevstar933krop.com, reached via a 307 from the
	// b-NNN master host) returns 404 unless the request carries a Referer on
	// the same domain — exactly like filmix/cdnsqu. /proxy replays these H
	// headers across the server-side m3u8 redirect and onto segment fetches.
	return streamProxyURLWithHeaders(r, rawURL, "hdvb", links, hdvbStreamHeaders(rawURL))
}

// hdvbStreamHeaders builds the Referer/Origin the sevstar933krop CDN requires
// on stream/segment requests. Derived from the stream URL's own origin so it
// tracks HDVB's rotating CDN hosts automatically (no headers → CDN 404).
func hdvbStreamHeaders(rawURL string) map[string]string {
	origin := hdvbExtractOrigin(rawURL)
	if origin == "" {
		return nil
	}
	return map[string]string{
		"Referer": origin + "/",
		"Origin":  origin,
	}
}
