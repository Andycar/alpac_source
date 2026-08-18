package litesrc

import (
	stdjson "encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/proxylink"
)

// kinovod (kinovod.pro) — Russian online cinema.
//
// Player flow reverse-engineered from the abandoned Kodi plugin
// `plugin.video.kinovod` v0.4.1 (https://github.com/virserg/kodirepo):
//
//  1. GET /search?query={text} — either redirects to the matching movie page
//     (single-result UX) or returns a search-listing page.
//  2. Movie page exposes JS globals: MOVIE_ID, IDENTIFIER, optional VOD_HASH /
//     VOD_TIME. Newer site versions hide these and require fetching /user_data
//     and decoding a "Hunter" packer that contains _vod_hash and _vod_time.
//  3. GET /vod/{movie_id}?identifier=X&st={hash}&e={time}&file_type=hls&player_type=new
//     returns either:
//     file|[1080p]URL.m3u8,[720p]URL.m3u8,...|<subs>           — single movie
//     pl|<python-literal array of seasons/episodes>|           — serial
//
// Quality string format: `[1080p]URL,[720p]URL,[480p]URL`.
// Translation string format: `{Voice A}URL_A;{Voice B}URL_B`.
//
// IP/JA3 fingerprint filtering at the network edge — uTLS via the per-balancer
// SOCKS5 hook (httpclient.NewUTLSForBalancer) is used so operators can route
// the balancer through a residential proxy without code changes.
var (
	kinovodMovieIDRe    = regexp.MustCompile(`var\s+MOVIE_ID\s*=\s*(\d+)\s*;`)
	kinovodIdentRe      = regexp.MustCompile(`var\s+IDENTIFIER\s*=\s*"([^"]+)"\s*;`)
	kinovodCUIDRe       = regexp.MustCompile(`var\s+PLAYER_CUID\s*=\s*"([^"]+)"\s*;`)
	kinovodVodHashRe    = regexp.MustCompile(`var\s+VOD_HASH\s*=\s*"([^"]+)"\s*;`)
	kinovodVodTimeRe    = regexp.MustCompile(`var\s+VOD_TIME\s*=\s*"([^"]+)"\s*;`)
	kinovodSearchItemRe = regexp.MustCompile(`<div class="title"><a href="(/(?:film|serial|cartoon)/[^"]+)"[^>]*>([^<]+)</a></div>\s*(?:<div class="desc">([^<]*)</div>)?`)
	kinovodSearchYearRe = regexp.MustCompile(`([12]\d{3})`)
	kinovodMetaURLRe    = regexp.MustCompile(`<meta\s+property="og:url"\s+content="([^"]+)"`)
	kinovodHunterRe     = regexp.MustCompile(`escape\(r\)\)\}\("([^"]+)",\s*(\d+),\s*"([^"]+)",\s*(\d+),\s*(\d+),\s*(\d+)\)\)`)
	kinovodVodInnerHash = regexp.MustCompile(`_vod_hash\s*=\s*"([^"]+)"`)
	kinovodVodInnerTime = regexp.MustCompile(`_vod_time\s*=\s*"(\d+)"`)
	kinovodHunterAlpha  = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ+/"
	// Serial-mode payload uses Python repr; this regex captures one of the two
	// shapes: `'comment'`/`'title'` for season, `'file'` for episode.
)

type kinovodChecker struct {
	client *http.Client
	host   string
	rhub   bool
}

type kinovodSearchHit struct {
	Title string
	Year  string
	Href  string
}

type kinovodMovieMeta struct {
	MovieID    string
	Identifier string
	CUID       string
	VodHash    string
	VodTime    string
}

type kinovodSeasonNode struct {
	Title    string              `json:"-"`
	Episodes []kinovodEpisodeRow `json:"-"`
}

type kinovodEpisodeRow struct {
	Title     string
	Voices    []kinovodVoiceURL // when set, file is a {voice}URL;{voice}URL map
	SingleURL string            // when Voices == nil, the resolved stream URL
	Subtitles []kinovodSubtitle
}

type kinovodVoiceURL struct {
	Voice string
	URL   string
}

type kinovodSubtitle struct {
	Label string
	URL   string
}

func NewKinovodChecker(cfg config.Config) *kinovodChecker {
	host := strings.TrimSpace(strings.TrimRight(cfg.Online.Kinovod.Host, "/"))
	if host == "" {
		host = "https://kinovod.pro"
	}
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}
	jar, _ := cookiejar.New(nil)
	// Use uTLS via per-balancer SOCKS5 — kinovod IP-filters non-CIS exits.
	c := httpclient.NewUTLSForBalancer("kinovod", 20*time.Second)
	c.Jar = jar
	return &kinovodChecker{
		client: c,
		host:   host,
		rhub:   cfg.Online.Kinovod.Rhub,
	}
}

func (k *kinovodChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show := k.checkSearch(req)
			writeCheckSearchResponse(w, show, pluginQualityBadgeGet("kinovod"))
			return
		}
		k.index(w, req, links)
	}
}

func (k *kinovodChecker) checkSearch(req *http.Request) bool {
	if k.host == "" {
		return false
	}
	if k.rhub {
		// With rhub the page fetch runs on the user's device (RU IP) at play
		// time. No device is bound to this server-side aggregation probe, so we
		// can't verify reachability here — advertise availability and let the
		// play-path handshake do the work.
		return true
	}
	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, k.host+"/", nil)
	if err != nil {
		return false
	}
	k.setBrowserHeaders(httpReq, "")
	resp, err := k.client.Do(httpReq)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode >= 200 && resp.StatusCode < 400
}

// kinovodBrowserHeaders is the single source of truth for the request headers,
// shared by the direct path (setBrowserHeaders) and the rch path (rch.Get).
func kinovodBrowserHeaders(referer string) map[string]string {
	h := map[string]string{
		"User-Agent":      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"Accept-Language": "ru-RU,ru;q=0.9,en;q=0.7",
		"X-Lampac-Go":     "1",
	}
	if referer != "" {
		h["Referer"] = referer
	}
	return h
}

func (k *kinovodChecker) setBrowserHeaders(req *http.Request, referer string) {
	for key, val := range kinovodBrowserHeaders(referer) {
		req.Header.Set(key, val)
	}
}

func (k *kinovodChecker) index(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	href := strings.TrimSpace(q.Get("href"))
	source := strings.ToLower(strings.TrimSpace(q.Get("source")))
	sourceID := strings.TrimSpace(q.Get("id"))
	year, _ := getsTVQueryInt(q.Get("year"))
	s, sSet := getsTVQueryInt(q.Get("s"))
	t, tSet := getsTVQueryInt(q.Get("t"))
	if !sSet {
		s = -1
	}
	if !tSet {
		t = -1
	}

	if href == "" && source == "kinovod" && sourceID != "" {
		href = strings.TrimRight(k.host, "/") + "/" + strings.TrimLeft(sourceID, "/")
	}

	if href == "" && title == "" && originalTitle == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// With rhub on, the page fetches below run on the user's device (the server
	// IP is geo-blocked by kinovod). If no device is bound to this request yet,
	// rchGate hands the client the hub details so it reconnects and retries with
	// ?nws_id — at which point fetchTextWithURL routes over the hub.
	if rchGate(w, req, k.rhub) {
		return
	}

	// Step 1: resolve movie URL.
	pageURL := href
	if pageURL == "" {
		query := title
		if query == "" {
			query = originalTitle
		}
		hit := k.search(req, query, year)
		if hit.Href == "" {
			// Try original title as a second pass if Russian title failed.
			if query != originalTitle && originalTitle != "" {
				hit = k.search(req, originalTitle, year)
			}
		}
		if hit.Href == "" {
			writeGetsTVEmpty(w, rjson)
			return
		}
		pageURL = k.absURL(hit.Href)
	}

	// Step 2: fetch movie page and extract player vars.
	pageHTML, ok := k.fetchText(req, pageURL, k.host+"/")
	if !ok {
		writeGetsTVEmpty(w, rjson)
		return
	}
	meta := k.parseMovieMeta(pageHTML)
	if meta.MovieID == "" || meta.Identifier == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Step 3: resolve vod_hash/vod_time. Prefer page-embedded values; fall back
	// to /user_data Hunter-encoded payload.
	if meta.VodHash == "" || meta.VodTime == "" {
		hash, vTime, derr := k.resolveVodSecret(req, pageURL, meta)
		if derr == nil && hash != "" && vTime != "" {
			meta.VodHash = hash
			meta.VodTime = vTime
		}
	}
	if meta.VodHash == "" || meta.VodTime == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Step 4: fetch /vod/{id} and parse stream payload.
	payload, ok := k.fetchVodPayload(req, pageURL, meta)
	if !ok {
		writeGetsTVEmpty(w, rjson)
		return
	}

	isSerial := strings.HasPrefix(strings.TrimSpace(payload), "pl|") || strings.Contains(pageURL, "/serial/")
	if isSerial {
		serial := kinovodParseSerial(payload)
		if len(serial) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}
		k.writeSerial(w, req, rjson, serial, title, originalTitle, year, pageURL, s, t, links)
		return
	}

	streams, subs := kinovodParseMovie(payload)
	if len(streams) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	k.writeMovie(w, req, rjson, streams, subs, title, originalTitle, links)
}

func (k *kinovodChecker) absURL(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if strings.Contains(path, "://") {
		return path
	}
	return strings.TrimRight(k.host, "/") + "/" + strings.TrimLeft(path, "/")
}

func (k *kinovodChecker) search(req *http.Request, query string, year int) kinovodSearchHit {
	query = strings.TrimSpace(query)
	if query == "" {
		return kinovodSearchHit{}
	}
	target := k.host + "/search?" + url.Values{"query": {query}}.Encode()
	body, finalURL, ok := k.fetchTextWithURL(req, target, k.host+"/")
	if !ok {
		return kinovodSearchHit{}
	}
	// If the server redirected straight to a movie page, the final URL points
	// at /film/... or /serial/...; pick it up directly.
	if !strings.Contains(finalURL, "/search?") {
		return kinovodSearchHit{Href: finalURL, Title: "", Year: ""}
	}
	matches := kinovodSearchItemRe.FindAllStringSubmatch(body, -1)
	want := normalizeSearchTitle(query)
	var bestExact, bestFuzzy kinovodSearchHit
	for _, m := range matches {
		href := strings.TrimSpace(m[1])
		title := strings.TrimSpace(m[2])
		desc := ""
		if len(m) >= 4 {
			desc = m[3]
		}
		hitYear := ""
		if y := kinovodSearchYearRe.FindString(desc); y != "" {
			hitYear = y
		}
		hit := kinovodSearchHit{Title: title, Year: hitYear, Href: href}
		if normalizeSearchTitle(title) == want {
			if year == 0 || hitYear == strconv.Itoa(year) {
				return hit
			}
			if bestExact.Href == "" {
				bestExact = hit
			}
		}
		if bestFuzzy.Href == "" {
			bestFuzzy = hit
		}
	}
	if bestExact.Href != "" {
		return bestExact
	}
	return bestFuzzy
}

func (k *kinovodChecker) parseMovieMeta(html string) kinovodMovieMeta {
	return kinovodMovieMeta{
		MovieID:    submatch1(kinovodMovieIDRe, html),
		Identifier: submatch1(kinovodIdentRe, html),
		CUID:       submatch1(kinovodCUIDRe, html),
		VodHash:    submatch1(kinovodVodHashRe, html),
		VodTime:    submatch1(kinovodVodTimeRe, html),
	}
}

// resolveVodSecret fetches /user_data?... and tries two paths: a JSON shape
// with explicit `vod_time`/`vod_hash`, then the Hunter-encoded fallback that
// embeds `_vod_hash` / `_vod_time` inside the decoded JS.
func (k *kinovodChecker) resolveVodSecret(req *http.Request, referer string, meta kinovodMovieMeta) (string, string, error) {
	if meta.MovieID == "" || meta.CUID == "" {
		return "", "", fmt.Errorf("missing movie_id or cuid")
	}
	target := k.host + "/user_data?" + url.Values{
		"page":     {"movie"},
		"movie_id": {meta.MovieID},
		"cuid":     {meta.CUID},
		"device":   {"DESKTOP"},
		"_":        {strconv.FormatInt(time.Now().Unix(), 10)},
	}.Encode()
	body, ok := k.fetchText(req, target, referer)
	if !ok {
		return "", "", fmt.Errorf("user_data fetch failed")
	}

	// Try JSON shape first ({"vod_hash":"...","vod_time":"..."}).
	trim := strings.TrimSpace(body)
	if strings.HasPrefix(trim, "{") {
		var jsonResp struct {
			VodHash string             `json:"vod_hash"`
			VodTime stdjson.RawMessage `json:"vod_time"`
		}
		if err := stdjson.Unmarshal([]byte(trim), &jsonResp); err == nil && jsonResp.VodHash != "" {
			vt := strings.TrimSpace(string(jsonResp.VodTime))
			vt = strings.Trim(vt, `"`)
			if vt != "" {
				return jsonResp.VodHash, vt, nil
			}
		}
	}

	// Hunter-packer fallback.
	if decoded, derr := kinovodHunterDecode(body); derr == nil && decoded != "" {
		hash := submatch1(kinovodVodInnerHash, decoded)
		vTime := submatch1(kinovodVodInnerTime, decoded)
		if hash != "" && vTime != "" {
			return hash, vTime, nil
		}
	}

	return "", "", fmt.Errorf("no vod credentials in /user_data response")
}

func (k *kinovodChecker) fetchVodPayload(req *http.Request, referer string, meta kinovodMovieMeta) (string, bool) {
	target := k.host + "/vod/" + meta.MovieID + "?" + url.Values{
		"identifier":  {meta.Identifier},
		"st":          {meta.VodHash},
		"e":           {meta.VodTime},
		"file_type":   {"hls"},
		"player_type": {"new"},
	}.Encode()
	return k.fetchText(req, target, referer)
}

func (k *kinovodChecker) fetchText(req *http.Request, target, referer string) (string, bool) {
	body, _, ok := k.fetchTextWithURL(req, target, referer)
	return body, ok
}

func (k *kinovodChecker) fetchTextWithURL(req *http.Request, target, referer string) (string, string, bool) {
	if k.rhub {
		if rch := newRchClient(req); rch.IsConnected() {
			if body, err := rch.Get(req.Context(), target, kinovodBrowserHeaders(referer)); err == nil && body != "" {
				return body, target, true
			}
			// Hub fetch failed (device dropped mid-session / timeout); fall back
			// to a direct attempt so a transient hub problem isn't fatal.
		}
	}
	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, target, nil)
	if err != nil {
		return "", "", false
	}
	k.setBrowserHeaders(httpReq, referer)
	resp, err := k.client.Do(httpReq)
	if err != nil {
		return "", "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return "", "", false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", "", false
	}
	finalURL := target
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	return string(body), finalURL, true
}

// kinovodParseMovie splits `file|<qualities>|<subs>` into ordered (label, url)
// entries and the subtitle list. Highest quality first.
func kinovodParseMovie(payload string) ([]kinovodQualityURL, []kinovodSubtitle) {
	trim := strings.TrimSpace(payload)
	if !strings.HasPrefix(trim, "file|") {
		return nil, nil
	}
	parts := strings.SplitN(trim, "|", 3)
	if len(parts) < 2 {
		return nil, nil
	}
	streams := kinovodParseQualityList(parts[1])
	var subs []kinovodSubtitle
	if len(parts) == 3 {
		subs = kinovodParseSubtitleList(parts[2])
	}
	return streams, subs
}

type kinovodQualityURL struct {
	Label string // "1080p"
	URL   string
}

// kinovodParseQualityList: `[1080p]URL1.m3u8,[720p]URL2.m3u8,[480p]URL3.m3u8`.
func kinovodParseQualityList(raw string) []kinovodQualityURL {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	chunks := strings.Split(raw, ",[")
	out := make([]kinovodQualityURL, 0, len(chunks))
	for i, c := range chunks {
		if i > 0 {
			c = "[" + c
		}
		c = strings.TrimSpace(c)
		if !strings.HasPrefix(c, "[") {
			continue
		}
		end := strings.Index(c, "]")
		if end < 0 {
			continue
		}
		label := strings.TrimSpace(c[1:end])
		uri := strings.TrimSpace(c[end+1:])
		if uri == "" {
			continue
		}
		out = append(out, kinovodQualityURL{Label: label, URL: uri})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return kinovodQualityRank(out[i].Label) > kinovodQualityRank(out[j].Label)
	})
	return out
}

func kinovodQualityRank(label string) int {
	l := strings.ToLower(strings.TrimSpace(label))
	switch {
	case strings.Contains(l, "4k") || strings.Contains(l, "2160"):
		return 2160
	case strings.Contains(l, "1080"):
		return 1080
	case strings.Contains(l, "720"):
		return 720
	case strings.Contains(l, "480"):
		return 480
	case strings.Contains(l, "360"):
		return 360
	default:
		// Strip trailing 'p' and try parseInt.
		l = strings.TrimSuffix(l, "p")
		if n, err := strconv.Atoi(l); err == nil {
			return n
		}
		return 0
	}
}

// kinovodParseSubtitleList: `[ru]URL1.vtt,[en]URL2.vtt`.
func kinovodParseSubtitleList(raw string) []kinovodSubtitle {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	chunks := strings.Split(raw, ",[")
	out := make([]kinovodSubtitle, 0, len(chunks))
	for i, c := range chunks {
		if i > 0 {
			c = "[" + c
		}
		c = strings.TrimSpace(c)
		if !strings.HasPrefix(c, "[") {
			continue
		}
		end := strings.Index(c, "]")
		if end < 0 {
			continue
		}
		label := strings.TrimSpace(c[1:end])
		uri := strings.TrimSpace(c[end+1:])
		if uri == "" {
			continue
		}
		out = append(out, kinovodSubtitle{Label: label, URL: uri})
	}
	return out
}

// kinovodParseTranslationList: `{Voice A}URL_A;{Voice B}URL_B`.
func kinovodParseTranslationList(raw string) []kinovodVoiceURL {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	chunks := strings.Split(raw, ";{")
	out := make([]kinovodVoiceURL, 0, len(chunks))
	for i, c := range chunks {
		if i > 0 {
			c = "{" + c
		}
		c = strings.TrimSpace(c)
		if !strings.HasPrefix(c, "{") {
			continue
		}
		end := strings.Index(c, "}")
		if end < 0 {
			continue
		}
		voice := strings.TrimSpace(c[1:end])
		uri := strings.TrimSpace(c[end+1:])
		if uri == "" {
			continue
		}
		// File for a single voice may still carry qualities; pick best.
		if strings.HasPrefix(uri, "[") {
			if qs := kinovodParseQualityList(uri); len(qs) > 0 {
				uri = qs[0].URL
			}
		}
		out = append(out, kinovodVoiceURL{Voice: voice, URL: uri})
	}
	return out
}

// kinovodParseSerial converts the `pl|<python-literal>|` payload into a list of
// seasons with episodes. The inner literal is Python-style (single quotes,
// None/True/False), so we coerce it to JSON before unmarshalling.
func kinovodParseSerial(payload string) []kinovodSeasonNode {
	trim := strings.TrimSpace(payload)
	if !strings.HasPrefix(trim, "pl|") {
		return nil
	}
	parts := strings.SplitN(trim, "|", 3)
	if len(parts) < 2 {
		return nil
	}
	body := kinovodPyLitToJSON(parts[1])
	var raw []map[string]any
	if err := stdjson.Unmarshal([]byte(body), &raw); err != nil {
		return nil
	}
	out := make([]kinovodSeasonNode, 0, len(raw))
	for _, season := range raw {
		seasonTitle := kinovodNodeTitle(season)
		episodesAny, _ := season["folder"]
		if episodesAny == nil {
			episodesAny = season["playlist"]
		}
		episodesList, ok := episodesAny.([]any)
		if !ok || len(episodesList) == 0 {
			continue
		}
		s := kinovodSeasonNode{Title: seasonTitle}
		for _, epAny := range episodesList {
			ep, ok := epAny.(map[string]any)
			if !ok {
				continue
			}
			s.Episodes = append(s.Episodes, kinovodEpisodeFromMap(ep))
		}
		if len(s.Episodes) > 0 {
			out = append(out, s)
		}
	}
	return out
}

func kinovodEpisodeFromMap(ep map[string]any) kinovodEpisodeRow {
	row := kinovodEpisodeRow{Title: kinovodNodeTitle(ep)}
	file, _ := ep["file"].(string)
	subtitle, _ := ep["subtitle"].(string)

	if strings.Contains(file, ";{") {
		row.Voices = kinovodParseTranslationList(file)
	} else if strings.HasPrefix(file, "[") {
		if qs := kinovodParseQualityList(file); len(qs) > 0 {
			row.SingleURL = qs[0].URL
		}
	} else {
		row.SingleURL = strings.TrimSpace(file)
	}
	if subtitle != "" {
		row.Subtitles = kinovodParseSubtitleList(subtitle)
	}
	return row
}

func kinovodNodeTitle(m map[string]any) string {
	for _, key := range []string{"comment", "title"} {
		if v, ok := m[key].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// kinovodPyLitToJSON converts a Python-repr-flavoured literal (single quotes,
// `None`/`True`/`False`, `\/`) into JSON. It's intentionally simple — kinovod
// strings rarely contain embedded apostrophes; if they do, we lose them.
func kinovodPyLitToJSON(s string) string {
	s = strings.ReplaceAll(s, `\/`, "/")
	// Replace Python keywords first to avoid mangling them when swapping quotes.
	s = strings.ReplaceAll(s, "None", "null")
	s = strings.ReplaceAll(s, "True", "true")
	s = strings.ReplaceAll(s, "False", "false")
	// Convert single-quoted strings to double-quoted. Walk char-by-char so we
	// can swap unescaped `'` to `"` while leaving `\"` alone.
	var b strings.Builder
	b.Grow(len(s))
	inString := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\\':
			if i+1 < len(s) {
				b.WriteByte(c)
				b.WriteByte(s[i+1])
				i++
				continue
			}
			b.WriteByte(c)
		case '\'':
			b.WriteByte('"')
			inString = !inString
		case '"':
			if inString {
				// Escape inner `"` characters that came from Python single-quoted
				// strings.
				b.WriteString(`\"`)
			} else {
				b.WriteByte(c)
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// kinovodHunterDecode unpacks the "Hunter" packer used by /user_data:
//
//	eval(function(h,u,n,t,e,r){...}("HEX",u,"alphabet",t,e,r))
//
// where `h` is the encoded text, `n` is the substitution alphabet, `e` is the
// base, and `t` is the per-character offset subtracted from each decoded byte.
func kinovodHunterDecode(html string) (string, error) {
	m := kinovodHunterRe.FindStringSubmatch(html)
	if len(m) < 7 {
		return "", fmt.Errorf("hunter signature not found")
	}
	hStr := m[1]
	nStr := m[3]
	t, terr := strconv.Atoi(m[4])
	e, eerr := strconv.Atoi(m[5])
	if terr != nil || eerr != nil {
		return "", fmt.Errorf("bad hunter integers")
	}
	if e <= 1 || e > len(kinovodHunterAlpha) {
		return "", fmt.Errorf("hunter base out of range: %d", e)
	}
	if e >= len(nStr) {
		return "", fmt.Errorf("hunter delim index out of range")
	}
	delim := nStr[e]
	indexInN := make(map[byte]int, len(nStr))
	for i := 0; i < len(nStr); i++ {
		indexInN[nStr[i]] = i
	}
	indexInBase := make(map[byte]int, e)
	for i := 0; i < e; i++ {
		indexInBase[kinovodHunterAlpha[i]] = i
	}

	var out strings.Builder
	var buf []byte
	for i := 0; i < len(hStr); i++ {
		c := hStr[i]
		if c != delim {
			buf = append(buf, c)
			continue
		}
		// Substitute each char in buf with its index in `n` (as ASCII digits).
		subst := make([]byte, 0, len(buf)*2)
		for _, ch := range buf {
			idx, ok := indexInN[ch]
			if !ok {
				continue
			}
			subst = append(subst, []byte(strconv.Itoa(idx))...)
		}
		// Decode base-e of the substituted digits (LSB-first reversed walk).
		val := 0
		j := 0
		for k := len(subst) - 1; k >= 0; k-- {
			d, ok := indexInBase[subst[k]]
			if !ok {
				j++
				continue
			}
			val += d * pow(e, j)
			j++
		}
		out.WriteByte(byte(val - t))
		buf = buf[:0]
	}
	return out.String(), nil
}

func pow(base, exp int) int {
	r := 1
	for i := 0; i < exp; i++ {
		r *= base
	}
	return r
}

// ---------------- Output rendering ----------------

func (k *kinovodChecker) writeMovie(
	w http.ResponseWriter,
	req *http.Request,
	rjson bool,
	streams []kinovodQualityURL,
	subs []kinovodSubtitle,
	title, originalTitle string,
	links *proxylink.Manager,
) {
	if len(streams) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	top := streams[0]
	hls := streamProxyURL(req, top.URL, "kinovod", links)
	row := map[string]any{
		"method": "play",
		"url":    hls,
		"stream": hls,
		"name":   top.Label,
		"title":  getsTVJoinName(title, originalTitle),
	}
	if rendered := kinovodRenderSubtitles(req, subs, links); len(rendered) > 0 {
		row["subtitles"] = rendered
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "movie",
			"data": []map[string]any{row},
		})
		return
	}
	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	getsTVAppendMovieHTML(&sb, row, top.Label, true, 0, 0)
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func kinovodRenderSubtitles(req *http.Request, subs []kinovodSubtitle, links *proxylink.Manager) []map[string]string {
	if len(subs) == 0 {
		return nil
	}
	out := make([]map[string]string, 0, len(subs))
	for _, s := range subs {
		uri := strings.TrimSpace(s.URL)
		if uri == "" {
			continue
		}
		// Take first URL from ` or `-separated mirror list.
		if idx := strings.Index(uri, " or "); idx >= 0 {
			uri = uri[:idx]
		}
		uri = streamProxyURL(req, uri, "kinovod", links)
		out = append(out, map[string]string{
			"label": s.Label,
			"url":   uri,
		})
	}
	return out
}

func (k *kinovodChecker) writeSerial(
	w http.ResponseWriter,
	req *http.Request,
	rjson bool,
	serial []kinovodSeasonNode,
	title, originalTitle string,
	year int,
	href string,
	s int,
	t int,
	links *proxylink.Manager,
) {
	host := hostFromRequest(req)
	encTitle := url.QueryEscape(title)
	encOriginal := url.QueryEscape(originalTitle)
	encHref := url.QueryEscape(href)
	baseQuery := fmt.Sprintf("rjson=%s&title=%s&original_title=%s&year=%d&href=%s",
		getsTVBool(rjson), encTitle, encOriginal, year, encHref)

	// Season selector.
	if s == -1 {
		type seasonItem struct {
			Idx   int
			Label string
		}
		items := make([]seasonItem, 0, len(serial))
		for i, season := range serial {
			label := strings.TrimSpace(season.Title)
			if label == "" {
				label = fmt.Sprintf("%d сезон", i+1)
			}
			items = append(items, seasonItem{Idx: i, Label: label})
		}
		if len(items) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}
		rows := make([]map[string]any, 0, len(items))
		labels := make([]string, 0, len(items))
		for _, item := range items {
			link := fmt.Sprintf("%s/lite/kinovod?%s&s=%d", host, baseQuery, item.Idx)
			rows = append(rows, map[string]any{
				"method": "link",
				"id":     item.Idx,
				"url":    link,
				"name":   item.Label,
			})
			labels = append(labels, item.Label)
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

	if s < 0 || s >= len(serial) {
		writeGetsTVEmpty(w, rjson)
		return
	}
	season := serial[s]
	if len(season.Episodes) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Build voice selector by inspecting first episode of the chosen season —
	// kinovod keeps the same voice set across episodes.
	first := season.Episodes[0]
	hasVoices := len(first.Voices) > 0
	if hasVoices && t == -1 {
		t = 0
	}
	if hasVoices && (t < 0 || t >= len(first.Voices)) {
		t = 0
	}

	var voiceRows []map[string]any
	if hasVoices {
		voiceRows = make([]map[string]any, 0, len(first.Voices))
		for i, v := range first.Voices {
			link := fmt.Sprintf("%s/lite/kinovod?%s&s=%d&t=%d", host, baseQuery, s, i)
			voiceRows = append(voiceRows, map[string]any{
				"method": "link",
				"name":   v.Voice,
				"active": t == i,
				"url":    link,
			})
		}
	}

	baseTitle := getsTVJoinName(title, originalTitle)
	rows := make([]map[string]any, 0, len(season.Episodes))
	labels := make([]string, 0, len(season.Episodes))
	episodesNum := make([]int, 0, len(season.Episodes))
	for i, ep := range season.Episodes {
		var streamURL string
		if hasVoices {
			if t >= 0 && t < len(ep.Voices) {
				streamURL = ep.Voices[t].URL
			}
		} else {
			streamURL = ep.SingleURL
		}
		if streamURL == "" {
			continue
		}
		streamURL = streamProxyURL(req, streamURL, "kinovod", links)
		label := strings.TrimSpace(ep.Title)
		if label == "" {
			label = fmt.Sprintf("%d серия", i+1)
		}
		row := map[string]any{
			"method": "play",
			"url":    streamURL,
			"stream": streamURL,
			"s":      s + 1,
			"e":      i + 1,
			"name":   label,
			"title":  fmt.Sprintf("%s (%s)", baseTitle, label),
		}
		if rendered := kinovodRenderSubtitles(req, ep.Subtitles, links); len(rendered) > 0 {
			row["subtitles"] = rendered
		}
		rows = append(rows, row)
		labels = append(labels, label)
		episodesNum = append(episodesNum, i+1)
	}

	if len(rows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type":  "episode",
			"data":  rows,
			"voice": voiceRows,
		})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for _, row := range voiceRows {
		getsTVAppendVoiceHTML(&sb, row)
	}
	if len(voiceRows) > 0 {
		sb.WriteString(`</div><div class="videos__line">`)
	}
	for i, row := range rows {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, s+1, episodesNum[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}
