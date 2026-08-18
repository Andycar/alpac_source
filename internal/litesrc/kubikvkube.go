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

// KubikVKube (kubikvkube.com) is a DLE site whose "плеер2" embeds the tomion.org
// player (CDNvideoHub engine). The flow is entirely scrape-based:
//
//  1. Search kubikvkube by Kinopoisk id: /?do=search&subaction=search&story=<kp>.
//     The kp is indexed in the page, so a numeric query returns the title page.
//  2. The result page carries <iframe data-src="//tomion.org/lat/<id>"> — the
//     tomion content id (NOT the kp). tomion is series-oriented; movie pages
//     usually have no tomion embed and resolve to {}.
//  3. tomion's /lat/<id>?season=&episode=&voice= SPA embed (2026-06 format)
//     inlines everything in `window.playerData = {…}`:
//     - "kp_id":N elsewhere in the HTML              (match verification)
//     - playlist.serial.list[season][episode]{num, voices:[{video_id,voice_id}]}
//     + a top-level voices{voice_id:name} map      (the season/episode tree)
//     - config.video = "<m3u8>"                      (the SELECTED s/e/voice)
//     Re-requesting with a different voice/episode returns a fresh signed hls
//     on cdn8.tomion.org (no Referer needed; IP-bound via the /x/<token>/ path).
var (
	kubikvkubeTomionIDRe   = regexp.MustCompile(`tomion\.org/lat/(\d+)`)
	kubikvkubeSearchLinkRe = regexp.MustCompile(`href="(https?://[^"]+/(?:series|serial|movie|film|cartoon|anime|dorama)/\d+-[^"]+\.html)"`)
	// tomion's SPA player (2026-06, replaced the old #inputData/data-config
	// inline div) carries the signed hls in playerData.config.video.
	kubikvkubeHLSRe  = regexp.MustCompile(`"video":"(https:[^"]+\.m3u8[^"]*)"`)
	kubikvkubeKpIDRe = regexp.MustCompile(`"kp_id":(\d+)`)
)

// kubikvkubePlayerData mirrors the `window.playerData = {…}` object tomion's SPA
// player inlines in /lat/<id>. The playlist tree is serial.list[season][episode],
// each episode listing its voices; voiceName comes from the top-level voices map.
type kubikvkubePlayerData struct {
	Voices   map[string]string `json:"voices"`
	Playlist struct {
		Serial struct {
			List [][]struct {
				Num    int `json:"num"`
				Voices []struct {
					VideoID int `json:"video_id"`
					VoiceID int `json:"voice_id"`
				} `json:"voices"`
			} `json:"list"`
		} `json:"serial"`
	} `json:"playlist"`
}

// kubikvkubeExtractPlayerData pulls the `window.playerData = {…}` object out of
// the embed HTML via a balanced-brace scan — it's too nested for a regex, and
// its string values (URLs, voice names) never contain braces so the scan is safe.
func kubikvkubeExtractPlayerData(html string) string {
	i := strings.Index(html, "window.playerData")
	if i < 0 {
		return ""
	}
	start := strings.IndexByte(html[i:], '{')
	if start < 0 {
		return ""
	}
	start += i
	depth := 0
	for j := start; j < len(html); j++ {
		switch html[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return html[start : j+1]
			}
		}
	}
	return ""
}

const (
	kubikvkubeUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"
	// Positive resolutions are stable, so cache them for a while. Negative
	// results get a short TTL: a title genuinely absent from kubikvkube costs
	// one cheap search per few minutes, but a transient fetch failure (cold
	// DNS right after boot, upstream blip) recovers quickly instead of hiding
	// the source for half an hour.
	kubikvkubeIDCacheTTL  = 30 * time.Minute
	kubikvkubeNegCacheTTL = 3 * time.Minute
	kubikvkubePlCacheTTL  = 10 * time.Minute
)

type kubikvkubeChecker struct {
	client     *http.Client
	siteHost   string   // https://kubikvkube.com
	playerHost string   // https://tomion.org
	idCache    sync.Map // kp(string) -> *kubikvkubeIDEntry
	plCache    sync.Map // tomionID(string) -> *kubikvkubePlEntry
}

// kubikvkubePlEntry caches the parsed playlist tree (structure only — the
// signed hls is resolved live elsewhere), so checksearch/index stay fast and
// within the /lite/events wall-clock deadline instead of re-fetching tomion.
type kubikvkubePlEntry struct {
	pl      kubikvkubePlaylist
	expires time.Time
}

// kubikvkubeIDEntry caches the kp -> tomion-id resolution (found=false is also
// cached so we don't re-scrape titles tomion doesn't carry on every browse).
type kubikvkubeIDEntry struct {
	tomionID string
	expires  time.Time
}

// kubikvkubeItem is one playable variant (a single voice of a single episode).
type kubikvkubeItem struct {
	VideoID   int    `json:"video_id"`
	Season    int    `json:"season"`
	Episode   int    `json:"episode"`
	VoiceName string `json:"voice_name"`
	VoiceID   int    `json:"voice_id"`
}

// kubikvkubePlaylist is the parsed tomion playerData tree plus the verified kp.
type kubikvkubePlaylist struct {
	kpID    int
	seasons map[int]map[int][]kubikvkubeItem // season -> episode -> voices
}

func NewKubikvkubeChecker(cfg config.Config) *kubikvkubeChecker {
	site := strings.TrimSpace(strings.TrimRight(cfg.Online.Kubikvkube.Host, "/"))
	if site == "" {
		site = "https://kubikvkube.com"
	}
	if !strings.Contains(site, "://") {
		site = "https://" + site
	}

	player := strings.TrimSpace(strings.TrimRight(cfg.Online.Kubikvkube.PlayerHost, "/"))
	if player == "" {
		player = "https://tomion.org"
	}
	if !strings.Contains(player, "://") {
		player = "https://" + player
	}

	return &kubikvkubeChecker{
		client:     httpclient.NewForBalancer("kubikvkube", 15*time.Second),
		siteHost:   site,
		playerHost: player,
	}
}

func (k *kubikvkubeChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := strings.Trim(strings.TrimPrefix(strings.ToLower(r.URL.Path), "/lite/"), "/")

		switch raw {
		case "kubikvkube":
			if parseBoolParam(r.URL.Query().Get("checksearch")) {
				show, quality := k.checkSearch(r)
				writeCheckSearchResponse(w, show, quality)
				return
			}
			k.index(w, r)
			return

		case "kubikvkube/video", "kubikvkube/video.m3u8":
			k.video(w, r, links)
			return
		}

		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error":    "kubikvkube route is not implemented in local mode",
			"balanser": raw,
		})
	}
}

// ---------- checksearch ----------

func (k *kubikvkubeChecker) checkSearch(r *http.Request) (bool, string) {
	kp := strings.TrimSpace(r.URL.Query().Get("kinopoisk_id"))
	if kp == "" {
		return false, ""
	}
	if _, err := strconv.ParseInt(kp, 10, 64); err != nil {
		return false, ""
	}

	// Full resolution: search → page → tomion embed with kp verification.
	// findTomionID caches the tomion id, and a search-only probe gave false
	// positives (the kp number coincidentally appearing on an unrelated page →
	// "show" but index resolves empty). Accuracy matters more than appearing on
	// the very first /lite/events poll: a slow cold probe simply fills in on the
	// next poll once the cache is warm. tomion serves up to 1080p.
	tomionID, ok := k.findTomionID(r.Context(), kp)
	if !ok || tomionID == "" {
		return false, ""
	}
	return true, "FHD"
}

// ---------- index ----------

func (k *kubikvkubeChecker) index(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	kp := strings.TrimSpace(q.Get("kinopoisk_id"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	voice := strings.TrimSpace(q.Get("t"))
	season, seasonSet := getsTVQueryInt(q.Get("s"))
	if !seasonSet {
		season = -1
	}

	if kp == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	tomionID, ok := k.findTomionID(r.Context(), kp)
	if !ok || tomionID == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}
	pl, ok := k.fetchPlaylist(r.Context(), tomionID, kp)
	if !ok || len(pl.seasons) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(r)

	// Movie shape: a single season with a single episode → render the voices
	// directly rather than forcing a season/episode drill-down.
	if seasonNums := pl.sortedSeasons(); len(seasonNums) == 1 {
		if eps := pl.sortedEpisodes(seasonNums[0]); len(eps) == 1 {
			k.writeMovie(w, rjson, host, tomionID, title, originalTitle, pl, seasonNums[0], eps[0])
			return
		}
	}

	k.writeSerial(w, rjson, host, tomionID, kp, title, originalTitle, season, voice, pl)
}

func (k *kubikvkubeChecker) writeMovie(
	w http.ResponseWriter, rjson bool, host, tomionID, title, originalTitle string,
	pl kubikvkubePlaylist, season, episode int,
) {
	baseTitle := getsTVJoinName(title, originalTitle)
	rows := make([]map[string]any, 0, 8)
	labels := make([]string, 0, 8)

	for _, item := range pl.seasons[season][episode] {
		voiceName := kubikvkubeVoiceLabel(item)
		link := k.videoLink(host, tomionID, season, episode, item.VoiceID, title)
		rows = append(rows, map[string]any{
			"method": "call",
			"url":    link,
			"stream": link + "&play=true",
			"name":   voiceName,
			"title":  baseTitle + " (" + voiceName + ")",
		})
		labels = append(labels, voiceName)
	}

	if len(rows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "movie", "data": rows})
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

func (k *kubikvkubeChecker) writeSerial(
	w http.ResponseWriter, rjson bool, host, tomionID, kp, title, originalTitle string,
	season int, voice string, pl kubikvkubePlaylist,
) {
	defaultArgs := "&rjson=" + getsTVBool(rjson) +
		"&kinopoisk_id=" + url.QueryEscape(kp) +
		"&title=" + url.QueryEscape(title) +
		"&original_title=" + url.QueryEscape(originalTitle)

	// Season list.
	if season == -1 {
		seasons := pl.sortedSeasons()
		data := make([]map[string]any, 0, len(seasons))
		labels := make([]string, 0, len(seasons))
		for _, s := range seasons {
			name := strconv.Itoa(s) + " сезон"
			data = append(data, map[string]any{
				"method": "link",
				"id":     s,
				"url":    host + "/lite/kubikvkube?s=" + strconv.Itoa(s) + defaultArgs,
				"name":   name,
			})
			labels = append(labels, name)
		}
		if len(data) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}
		if rjson {
			writeJSON(w, http.StatusOK, map[string]any{"type": "season", "data": data})
			return
		}
		var sb strings.Builder
		sb.WriteString(`<div class="videos__line">`)
		for i, row := range data {
			getsTVAppendSeasonHTML(&sb, row, labels[i], i == 0)
		}
		sb.WriteString(`</div>`)
		writeHTML(w, http.StatusOK, sb.String())
		return
	}

	// Voice list for the season.
	voiceOrder, voiceNames := pl.voicesForSeason(season)
	if len(voiceOrder) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	// Resolve the requested voice (by name) to a voice_id; default to the first.
	selVoiceID := voiceOrder[0]
	selVoiceName := voiceNames[selVoiceID]
	if voice != "" {
		for _, vid := range voiceOrder {
			if voiceNames[vid] == voice {
				selVoiceID = vid
				selVoiceName = voiceNames[vid]
				break
			}
		}
	}

	voiceRows := make([]map[string]any, 0, len(voiceOrder))
	for _, vid := range voiceOrder {
		name := voiceNames[vid]
		voiceRows = append(voiceRows, map[string]any{
			"method": "link",
			"name":   name,
			"active": vid == selVoiceID,
			"url": host + "/lite/kubikvkube?s=" + strconv.Itoa(season) +
				"&t=" + url.QueryEscape(name) + defaultArgs,
		})
	}

	// Episodes for the selected voice.
	baseTitle := getsTVJoinName(title, originalTitle)
	episodes := pl.sortedEpisodes(season)
	data := make([]map[string]any, 0, len(episodes))
	labels := make([]string, 0, len(episodes))
	seasons := make([]int, 0, len(episodes))
	epNums := make([]int, 0, len(episodes))
	for _, ep := range episodes {
		if !pl.hasVoice(season, ep, selVoiceID) {
			continue
		}
		link := k.videoLink(host, tomionID, season, ep, selVoiceID, title)
		name := strconv.Itoa(ep) + " серия"
		data = append(data, map[string]any{
			"method": "call",
			"url":    link,
			"stream": link + "&play=true",
			"s":      season,
			"e":      ep,
			"name":   name,
			"title":  fmt.Sprintf("%s (%d серия)", baseTitle, ep),
		})
		labels = append(labels, name)
		seasons = append(seasons, season)
		epNums = append(epNums, ep)
	}
	if len(data) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	_ = selVoiceName
	if rjson {
		payload := map[string]any{"type": "episode", "data": data}
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
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, seasons[i], epNums[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// ---------- stream resolution ----------

func (k *kubikvkubeChecker) video(w http.ResponseWriter, r *http.Request, links *proxylink.Manager) {
	q := r.URL.Query()
	tomionID := strings.TrimSpace(q.Get("id"))
	if tomionID == "" {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	season, _ := getsTVQueryInt(q.Get("s"))
	episode, _ := getsTVQueryInt(q.Get("e"))
	voiceID := strings.TrimSpace(q.Get("voice"))

	hls, ok := k.resolveHLS(r.Context(), tomionID, season, episode, voiceID)
	if !ok || hls == "" {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	// cdn8.tomion.org binds the signed /x/<token>/ URL to the resolver IP; the
	// /proxy strips X-Forwarded-For so the CDN sees only our egress. Referer is
	// not strictly required but matches what a real browser sends.
	proxied := streamProxyURLWithHeaders(r, hls, "kubikvkube", links, map[string]string{
		"Referer": k.playerHost + "/",
		"Origin":  k.playerHost,
	})

	if parseBoolParam(q.Get("play")) {
		http.Redirect(w, r, proxied, http.StatusFound)
		return
	}
	payload := map[string]any{"method": "play", "url": proxied}
	if title := strings.TrimSpace(q.Get("title")); title != "" {
		payload["title"] = title
	}
	writeJSON(w, http.StatusOK, payload)
}

func (k *kubikvkubeChecker) resolveHLS(ctx context.Context, tomionID string, season, episode int, voiceID string) (string, bool) {
	if season <= 0 {
		season = 1
	}
	if episode <= 0 {
		episode = 1
	}
	if voiceID == "" {
		voiceID = "0"
	}
	target := fmt.Sprintf("%s/lat/%s?season=%d&episode=%d&voice=%s&adult_mode=2",
		k.playerHost, url.PathEscape(tomionID), season, episode, url.QueryEscape(voiceID))

	body, ok := k.fetch(ctx, target, k.siteHost+"/")
	if !ok {
		return "", false
	}
	hls := submatch1(kubikvkubeHLSRe, body)
	if hls == "" {
		log.Debug().Str("id", tomionID).Int("s", season).Int("e", episode).Str("voice", voiceID).Msg("kubikvkube: no hls in embed")
		return "", false
	}
	hls = kubikvkubeUnescapeURL(hls)
	return hls, hls != ""
}

// ---------- kp -> tomion id ----------

func (k *kubikvkubeChecker) findTomionID(ctx context.Context, kp string) (string, bool) {
	if v, ok := k.idCache.Load(kp); ok {
		entry := v.(*kubikvkubeIDEntry)
		if time.Now().Before(entry.expires) {
			return entry.tomionID, entry.tomionID != ""
		}
	}

	tomionID := k.searchTomionID(ctx, kp)
	if tomionID == "" && ctx.Err() != nil {
		// Aborted by the caller's deadline (e.g. the /lite/events wall-clock
		// budget) — that's not a real "absent on kubikvkube" verdict, so don't
		// poison the cache; let the next probe retry and warm it positively.
		return "", false
	}
	ttl := kubikvkubeIDCacheTTL
	if tomionID == "" {
		ttl = kubikvkubeNegCacheTTL
	}
	k.idCache.Store(kp, &kubikvkubeIDEntry{tomionID: tomionID, expires: time.Now().Add(ttl)})
	return tomionID, tomionID != ""
}

// searchTomionID searches kubikvkube by Kinopoisk id and returns the tomion
// content id of the first candidate page that carries a tomion embed whose
// data-film kp_id matches the requested kp.
func (k *kubikvkubeChecker) searchTomionID(ctx context.Context, kp string) string {
	searchURL := k.siteHost + "/?do=search&subaction=search&story=" + url.QueryEscape(kp)
	body, ok := k.fetch(ctx, searchURL, k.siteHost+"/")
	if !ok {
		return ""
	}

	links := kubikvkubeSearchLinkRe.FindAllStringSubmatch(body, -1)
	if len(links) == 0 {
		return ""
	}

	// Prefer series pages (tomion is series-oriented), then the rest. Cap the
	// number of candidate page fetches to keep checksearch cheap.
	ordered := make([]string, 0, len(links))
	seen := make(map[string]struct{}, len(links))
	for _, pass := range []bool{true, false} {
		for _, m := range links {
			u := m[1]
			if _, dup := seen[u]; dup {
				continue
			}
			isSeries := strings.Contains(u, "/series/") || strings.Contains(u, "/serial/")
			if isSeries == pass {
				ordered = append(ordered, u)
				seen[u] = struct{}{}
			}
		}
	}

	want, _ := strconv.Atoi(kp)
	const maxCandidates = 3
	for i, pageURL := range ordered {
		if i >= maxCandidates {
			break
		}
		pageBody, ok := k.fetch(ctx, pageURL, k.siteHost+"/")
		if !ok {
			continue
		}
		tomionID := submatch1(kubikvkubeTomionIDRe, pageBody)
		if tomionID == "" {
			continue
		}
		// Verify the embed's kp matches (guards against a coincidental
		// full-text number hit on the wrong title).
		if pl, ok := k.fetchPlaylist(ctx, tomionID, kp); ok && (pl.kpID == 0 || want == 0 || pl.kpID == want) {
			return tomionID
		}
	}
	return ""
}

// ---------- tomion playlist ----------

func (k *kubikvkubeChecker) fetchPlaylist(ctx context.Context, tomionID, kp string) (kubikvkubePlaylist, bool) {
	if v, ok := k.plCache.Load(tomionID); ok {
		entry := v.(*kubikvkubePlEntry)
		if time.Now().Before(entry.expires) {
			return entry.pl, true
		}
	}

	var pl kubikvkubePlaylist
	target := fmt.Sprintf("%s/lat/%s?season=1&episode=1&voice=0&adult_mode=2", k.playerHost, url.PathEscape(tomionID))
	body, ok := k.fetch(ctx, target, k.siteHost+"/")
	if !ok {
		return pl, false
	}

	raw := kubikvkubeExtractPlayerData(body)
	if raw == "" {
		return pl, false
	}

	var pd kubikvkubePlayerData
	if err := stdjson.Unmarshal([]byte(raw), &pd); err != nil {
		log.Debug().Err(err).Str("id", tomionID).Msg("kubikvkube: playerData parse failed")
		return pl, false
	}

	// serial.list is [season][episode]; season number is the 1-based index.
	pl.seasons = make(map[int]map[int][]kubikvkubeItem, len(pd.Playlist.Serial.List))
	for sIdx, eps := range pd.Playlist.Serial.List {
		season := sIdx + 1
		epMap := make(map[int][]kubikvkubeItem, len(eps))
		for _, ep := range eps {
			if ep.Num <= 0 {
				continue
			}
			items := make([]kubikvkubeItem, 0, len(ep.Voices))
			for _, v := range ep.Voices {
				items = append(items, kubikvkubeItem{
					VideoID:   v.VideoID,
					Season:    season,
					Episode:   ep.Num,
					VoiceID:   v.VoiceID,
					VoiceName: pd.Voices[strconv.Itoa(v.VoiceID)],
				})
			}
			if len(items) > 0 {
				epMap[ep.Num] = items
			}
		}
		if len(epMap) > 0 {
			pl.seasons[season] = epMap
		}
	}
	if v := submatch1(kubikvkubeKpIDRe, body); v != "" {
		pl.kpID, _ = strconv.Atoi(v)
	}
	if len(pl.seasons) == 0 {
		return pl, false
	}
	k.plCache.Store(tomionID, &kubikvkubePlEntry{pl: pl, expires: time.Now().Add(kubikvkubePlCacheTTL)})
	return pl, true
}

func (k *kubikvkubeChecker) fetch(ctx context.Context, target, referer string) (string, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("User-Agent", kubikvkubeUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}

	resp, err := k.client.Do(req)
	if err != nil {
		log.Debug().Err(err).Str("url", truncURL(target)).Msg("kubikvkube: fetch failed")
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", false
	}
	return string(body), true
}

func (k *kubikvkubeChecker) videoLink(host, tomionID string, season, episode, voiceID int, title string) string {
	return host + "/lite/kubikvkube/video.m3u8?id=" + url.QueryEscape(tomionID) +
		"&s=" + strconv.Itoa(season) +
		"&e=" + strconv.Itoa(episode) +
		"&voice=" + strconv.Itoa(voiceID) +
		"&title=" + url.QueryEscape(title)
}

// ---------- playlist helpers ----------

func (pl kubikvkubePlaylist) sortedSeasons() []int {
	out := make([]int, 0, len(pl.seasons))
	for s := range pl.seasons {
		out = append(out, s)
	}
	sort.Ints(out)
	return out
}

func (pl kubikvkubePlaylist) sortedEpisodes(season int) []int {
	eps := pl.seasons[season]
	out := make([]int, 0, len(eps))
	for e := range eps {
		out = append(out, e)
	}
	sort.Ints(out)
	return out
}

// voicesForSeason returns the voice_ids present in a season (ordered by first
// appearance across episodes) and a voice_id -> name map.
func (pl kubikvkubePlaylist) voicesForSeason(season int) ([]int, map[int]string) {
	order := make([]int, 0, 8)
	names := make(map[int]string, 8)
	for _, ep := range pl.sortedEpisodes(season) {
		for _, item := range pl.seasons[season][ep] {
			if _, ok := names[item.VoiceID]; ok {
				continue
			}
			names[item.VoiceID] = kubikvkubeVoiceLabel(item)
			order = append(order, item.VoiceID)
		}
	}
	return order, names
}

func (pl kubikvkubePlaylist) hasVoice(season, episode, voiceID int) bool {
	for _, item := range pl.seasons[season][episode] {
		if item.VoiceID == voiceID {
			return true
		}
	}
	return false
}

func kubikvkubeVoiceLabel(item kubikvkubeItem) string {
	name := strings.TrimSpace(item.VoiceName)
	if name == "" {
		return "По умолчанию"
	}
	return name
}

func kubikvkubeUnescapeURL(s string) string {
	s = strings.ReplaceAll(s, `\/`, "/")
	s = strings.ReplaceAll(s, "&amp;", "&")
	return strings.TrimSpace(s)
}
