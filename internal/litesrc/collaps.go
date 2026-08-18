package litesrc

import (
	"context"
	stdjson "encoding/json"
	"io"
	"lampac-go/internal/httpclient"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/kit"
	"lampac-go/internal/proxylink"
)

// collapsEmbedUA is the User-Agent used for embed/API requests to Collaps.
// The interkh.com CDN binds DASH stream URLs to the UA that fetched the embed,
// returning 410 Gone for mismatched UAs. This UA must be used both for embed
// requests (fetchText) and for proxying DASH streams (via EncryptURIWithHeaders).
const collapsEmbedUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36"

// collapsOrigin is the Origin header required by the Collaps embed API.
const collapsOrigin = "https://kinokrad.my"

var (
	collapsSeasonsRe    = regexp.MustCompile(`(?is)seasons:\s*(\[[^\n\r]+)`)
	collapsMakePlayerRe = regexp.MustCompile(`(?is)makePlayer\(\{`)
	collapsHLSRe        = regexp.MustCompile(`(?is)hls:\s*"([^"]+)"`)
	collapsDashRe       = regexp.MustCompile(`(?is)dasha?:\s*"([^"]+)"`)
	collapsAudioFirstRe = regexp.MustCompile(`(?is)audio:\s*\{"names":\[\s*"([^"]+)"`)
	collapsAudioListRe  = regexp.MustCompile(`(?is)audio:\s*\{"names":\[\s*([^\]]+)\]`)
	// Trailing comma is optional: when `cc` is the last key of `source: {…}`
	// (kinogo/ortified movies) there is none, and requiring it dropped subtitles.
	collapsCCRe         = regexp.MustCompile(`(?is)cc:\s*(\[[^\n\r]+\])`)
	collapsEpisodeNumRe = regexp.MustCompile(`([0-9]+)`)
)

type CollapsChecker struct {
	client   *http.Client
	apiHost  string
	listHost string
	token    string
}

type collapsListResponse struct {
	Results []collapsSearchResult `json:"results"`
}

type collapsSearchResult struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	OriginName  string `json:"origin_name"`
	Year        int    `json:"year"`
	Poster      string `json:"poster"`
	IframeURL   string `json:"iframe_url"`
	KinopoiskID string `json:"kinopoisk_id"`
	IMDBID      string `json:"imdb_id"`
}

type collapsEmbed struct {
	Content string
	Serial  []collapsSeason
}

type collapsSeason struct {
	Season   int              `json:"season"`
	Episodes []collapsEpisode `json:"episodes"`
}

type collapsEpisode struct {
	Episode string            `json:"episode"`
	HLS     string            `json:"hls"`
	Dasha   string            `json:"dasha"`
	Dash    string            `json:"dash"`
	CC      []collapsCC       `json:"cc"`
	Audio   collapsEpisodeAud `json:"audio"`
}

type collapsEpisodeAud struct {
	Names []string `json:"names"`
}

type collapsCC struct {
	URL  string `json:"url"`
	Name string `json:"name"`
}

func NewCollapsChecker(cfg config.Config) *CollapsChecker {
	apiHost := strings.TrimSpace(cfg.Online.Collaps.APIHost)
	if apiHost == "" || strings.Contains(apiHost, "initem.ws") ||
		strings.Contains(apiHost, "variyt.ws") {
		// Collaps/interkh rotates embed hosts; api.luxembd.ws is the current
		// live endpoint (api.initem.ws and api.variyt.ws went offline 2026-05).
		apiHost = "https://api.luxembd.ws"
	}
	apiHost = strings.TrimRight(apiHost, "/")

	listHost := strings.TrimSpace(cfg.Online.Collaps.ListHost)
	if listHost == "" {
		listHost = "https://api.bhcesh.me"
	}
	listHost = strings.TrimRight(listHost, "/")

	return &CollapsChecker{
		client:   httpclient.NewForBalancer("collaps", 10*time.Second),
		apiHost:  apiHost,
		listHost: listHost,
		token:    strings.TrimSpace(cfg.Online.Collaps.Token),
	}
}

// tokenForCtx returns the per-user kit token if available, otherwise the global token.
func (c *CollapsChecker) tokenForCtx(ctx context.Context) string {
	if kitToken, ok := kit.TokenOverride(ctx, "Collaps"); ok {
		return kitToken
	}
	return c.token
}

func (c *CollapsChecker) Handle(cfg config.Config, plugin string, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show := c.checkSearch(req)
			badge := pluginQualityBadgeGet(strings.ToLower(plugin))
			if badge == "" {
				badge = pluginQualityBadgeGet("collaps")
			}
			writeCheckSearchResponse(w, show, badge)
			return
		}

		c.index(w, req, plugin, links)
	}
}

func (c *CollapsChecker) index(w http.ResponseWriter, req *http.Request, plugin string, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	similar := parseBoolParam(q.Get("similar"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))

	orid := parseInt64(q.Get("orid"))
	if orid == 0 {
		orid = parseInt64(q.Get("id"))
	}
	kinopoiskID := parseInt64(q.Get("kinopoisk_id"))
	imdbID := strings.TrimSpace(q.Get("imdb_id"))

	season, seasonSet := getsTVQueryInt(q.Get("s"))
	if !seasonSet {
		season = -1
	}

	route := c.routeFor(plugin, req)
	dashMode := route == "/lite/collaps-dash"

	if similar || (orid == 0 && kinopoiskID == 0 && imdbID == "") {
		c.routeSearch(w, req, route, title, rjson)
		return
	}

	// When we have a token, prefer the searchList path (fast, reliable)
	// over direct embed (may 422 on some IPs). Direct embed is fallback only.
	var embed collapsEmbed
	var ok bool

	if c.tokenForCtx(req.Context()) != "" && title != "" {
		// Primary path: searchList → iframe_url → fetchEmbedByURL.
		if results, found := c.SearchList(req.Context(), title); found && len(results) > 0 {
			match := c.matchSearchResult(results, kinopoiskID, imdbID, title, originalTitle)
			if match != nil {
				embedURL := c.normalizeEmbedURL(req.Context(), match.IframeURL, match.ID)
				if embedURL != "" {
					embed, ok = c.fetchEmbedByURL(req.Context(), embedURL)
				}
			}
		}
	}

	if !ok || (embed.Content == "" && len(embed.Serial) == 0) {
		// Fallback: direct embed by kp/imdb/orid (no token path or searchList failed).
		embed, ok = c.fetchEmbed(req.Context(), orid, kinopoiskID, imdbID)
	}

	if !ok || (embed.Content == "" && len(embed.Serial) == 0) {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if embed.Content != "" {
		c.writeMovie(w, req, rjson, title, originalTitle, embed.Content, dashMode, links)
		return
	}

	c.writeSerial(w, req, route, season, rjson, title, originalTitle, orid, kinopoiskID, imdbID, embed.Serial, dashMode, links)
}

func (c *CollapsChecker) routeFor(plugin string, req *http.Request) string {
	if strings.Contains(strings.ToLower(req.URL.Path), "collaps-dash") {
		return "/lite/collaps-dash"
	}
	if strings.EqualFold(strings.TrimSpace(plugin), "collaps-dash") {
		return "/lite/collaps-dash"
	}
	return "/lite/collaps"
}

func (c *CollapsChecker) checkSearch(req *http.Request) bool {
	q := req.URL.Query()

	// For checksearch, prefer kinopoisk_id/imdb_id over "id".
	// The generic "id" param from events is a kinopoisk ID, NOT a Collaps orid.
	// Using it as orid produces wrong embed paths (/embed/movie/<kp> instead of /embed/kp/<kp>).
	orid := parseInt64(q.Get("orid"))
	kinopoiskID := parseInt64(q.Get("kinopoisk_id"))
	if kinopoiskID == 0 {
		kinopoiskID = parseInt64(q.Get("id"))
	}
	imdbID := strings.TrimSpace(q.Get("imdb_id"))
	title := strings.TrimSpace(q.Get("title"))

	// Prefer searchList (fast, token-based) over direct embed (may 422).
	if title != "" && c.tokenForCtx(req.Context()) != "" {
		if c.checkSearchList(req, title) {
			return true
		}
	}

	if orid > 0 || kinopoiskID > 0 || imdbID != "" {
		return c.checkEmbed(req, orid, kinopoiskID, imdbID)
	}

	if title != "" {
		return c.checkSearchList(req, title)
	}

	return false
}

func parseInt64(v string) int64 {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func (c *CollapsChecker) checkEmbed(req *http.Request, orid, kinopoiskID int64, imdbID string) bool {
	embed, ok := c.fetchEmbed(req.Context(), orid, kinopoiskID, imdbID)
	if !ok {
		return false
	}
	return embed.Content != "" || len(embed.Serial) > 0
}

func (c *CollapsChecker) fetchEmbed(ctx context.Context, orid, kinopoiskID int64, imdbID string) (collapsEmbed, bool) {
	path, ok := c.embedPath(orid, kinopoiskID, imdbID)
	if !ok {
		return collapsEmbed{}, false
	}
	embedURL := c.apiHost + path
	token := c.tokenForCtx(ctx)
	if token != "" {
		if strings.Contains(embedURL, "?") {
			embedURL += "&token=" + url.QueryEscape(token)
		} else {
			embedURL += "?token=" + url.QueryEscape(token)
		}
	}
	return c.fetchEmbedByURL(ctx, embedURL)
}

func (c *CollapsChecker) fetchEmbedByURL(ctx context.Context, fullURL string) (collapsEmbed, bool) {
	body, ok := c.fetchText(ctx, fullURL)
	if !ok {
		return collapsEmbed{}, false
	}

	if raw := strings.TrimSpace(submatch1(collapsSeasonsRe, body)); raw != "" {
		if serial, ok := collapsParseSeasons(raw); ok && len(serial) > 0 {
			return collapsEmbed{Serial: serial}, true
		}
	}

	loc := collapsMakePlayerRe.FindStringIndex(body)
	if len(loc) == 2 && loc[1] < len(body) {
		content := strings.TrimSpace(body[loc[1]:])
		if content != "" {
			return collapsEmbed{Content: content}, true
		}
	}

	return collapsEmbed{}, false
}

func (c *CollapsChecker) embedPath(orid, kinopoiskID int64, imdbID string) (string, bool) {
	switch {
	case orid > 0:
		return "/embed/movie/" + strconv.FormatInt(orid, 10), true
	case kinopoiskID > 0:
		return "/embed/kp/" + strconv.FormatInt(kinopoiskID, 10), true
	case imdbID != "":
		return "/embed/imdb/" + url.PathEscape(imdbID), true
	default:
		return "", false
	}
}

// normalizeEmbedURL takes an iframe_url from the list API and returns a
// fetchable URL with the token appended. Trust the host from iframe_url —
// bhcesh.me returns the host that actually serves the content (e.g.
// api.zenithjs.ws, api.tobaco.ws). Only rewrite the host if iframe_url
// points to a known-dead host. Falls back to /embed/movie/{id} on apiHost.
func (c *CollapsChecker) normalizeEmbedURL(ctx context.Context, iframeURL string, id int64) string {
	raw := strings.TrimSpace(iframeURL)

	if raw == "" && id > 0 {
		raw = c.apiHost + "/embed/movie/" + strconv.FormatInt(id, 10)
	}
	if raw == "" {
		return ""
	}

	// Only rewrite host when it's in the known-dead list; otherwise trust
	// what bhcesh.me returned — different films live on different *.ws hosts.
	if parsed, err := url.Parse(raw); err == nil && parsed.Host != "" {
		host := strings.ToLower(parsed.Host)
		if strings.Contains(host, "bhcesh.me") ||
			strings.Contains(host, "variyt.ws") ||
			strings.Contains(host, "luxembd.ws") {
			raw = c.apiHost + parsed.Path
			if parsed.RawQuery != "" {
				raw += "?" + parsed.RawQuery
			}
		}
	}

	// Append token (kit override → global fallback).
	token := c.tokenForCtx(ctx)
	if token != "" {
		if strings.Contains(raw, "?") {
			raw += "&token=" + url.QueryEscape(token)
		} else {
			raw += "?token=" + url.QueryEscape(token)
		}
	}

	return raw
}

func collapsParseSeasons(raw string) ([]collapsSeason, bool) {
	clean := strings.TrimSpace(raw)
	if clean == "" {
		return nil, false
	}

	clean = strings.TrimSuffix(clean, ",")
	clean = strings.TrimSuffix(clean, ";")
	if i := strings.LastIndex(clean, "]"); i >= 0 {
		clean = clean[:i+1]
	}

	var serial []collapsSeason
	if err := stdjson.Unmarshal([]byte(clean), &serial); err == nil && len(serial) > 0 {
		return serial, true
	}
	return nil, false
}

func (c *CollapsChecker) routeSearch(w http.ResponseWriter, req *http.Request, route, title string, rjson bool) {
	title = strings.TrimSpace(title)
	if title == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	rows, ok := c.SearchList(req.Context(), title)
	if !ok || len(rows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(req)
	data := make([]map[string]any, 0, len(rows))
	labels := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.ID <= 0 {
			continue
		}
		name := strings.TrimSpace(row.Name)
		if name == "" {
			name = strings.TrimSpace(row.OriginName)
		}
		if name == "" {
			name = "Untitled"
		}
		data = append(data, map[string]any{
			"method":  "link",
			"url":     host + route + "?orid=" + strconv.FormatInt(row.ID, 10),
			"similar": true,
			"year":    row.Year,
			"details": "",
			"title":   name,
			"img":     c.normalizePoster(row.Poster),
		})
		labels = append(labels, name)
	}

	if len(data) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "similar",
			"data": data,
		})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range data {
		getsTVAppendSeasonHTML(&sb, row, labels[i], i == 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// Token exposes the configured API token (xsearch adapter gate).
func (c *CollapsChecker) Token() string { return c.token }

func (c *CollapsChecker) SearchList(ctx context.Context, title string) ([]collapsSearchResult, bool) {
	token := c.tokenForCtx(ctx)
	if token == "" {
		return nil, false
	}

	host := c.listHost
	if host == "" {
		host = c.apiHost
	}
	u, err := url.Parse(host + "/list")
	if err != nil {
		return nil, false
	}
	qs := url.Values{}
	qs.Set("token", token)
	qs.Set("name", title)
	u.RawQuery = qs.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, false
	}
	collapsSetEmbedHeaders(httpReq)

	resp, err := balancerDoWithRetry(ctx, c.client, httpReq, 2)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false
	}

	var root collapsListResponse
	if err := stdjson.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&root); err != nil {
		return nil, false
	}
	return root.Results, true
}

func (c *CollapsChecker) checkSearchList(req *http.Request, title string) bool {
	rows, ok := c.SearchList(req.Context(), title)
	return ok && len(rows) > 0
}

// matchSearchResult finds the best match from searchList results by kp_id, imdb_id, or title.
func (c *CollapsChecker) matchSearchResult(results []collapsSearchResult, kinopoiskID int64, imdbID, title, originalTitle string) *collapsSearchResult {
	// First pass: exact match by kp_id or imdb_id.
	for i := range results {
		r := &results[i]
		if kinopoiskID > 0 && r.KinopoiskID != "" {
			if kp, err := strconv.ParseInt(r.KinopoiskID, 10, 64); err == nil && kp == kinopoiskID {
				return r
			}
		}
		if imdbID != "" && r.IMDBID != "" {
			rid := r.IMDBID
			if !strings.HasPrefix(rid, "tt") {
				rid = "tt" + rid
			}
			wid := imdbID
			if !strings.HasPrefix(wid, "tt") {
				wid = "tt" + wid
			}
			if strings.EqualFold(rid, wid) {
				return r
			}
		}
	}
	// Second pass: title match.
	wantTitle := strings.ToLower(strings.TrimSpace(title))
	wantOriginal := strings.ToLower(strings.TrimSpace(originalTitle))
	for i := range results {
		r := &results[i]
		name := strings.ToLower(strings.TrimSpace(r.Name))
		origin := strings.ToLower(strings.TrimSpace(r.OriginName))
		if (wantTitle != "" && name == wantTitle) || (wantOriginal != "" && origin == wantOriginal) {
			return r
		}
	}
	// Fallback: return first result if any.
	if len(results) > 0 {
		return &results[0]
	}
	return nil
}

func (c *CollapsChecker) fetchText(ctx context.Context, target string) (string, bool) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", false
	}
	collapsSetEmbedHeaders(httpReq)

	resp, err := balancerDoWithRetry(ctx, c.client, httpReq, 2)
	if err != nil {
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

// collapsSetEmbedHeaders applies the full set of browser-like headers
// required by the Collaps embed API (Origin, sec-ch-ua, etc.).
func collapsSetEmbedHeaders(r *http.Request) {
	r.Header.Set("User-Agent", collapsEmbedUA)
	r.Header.Set("Origin", collapsOrigin)
	r.Header.Set("Cache-Control", "no-cache")
	r.Header.Set("Pragma", "no-cache")
	r.Header.Set("DNT", "1")
	r.Header.Set("sec-ch-ua", `"Chromium";v="142", "Google Chrome";v="142", "Not_A Brand";v="99"`)
	r.Header.Set("sec-ch-ua-mobile", "?0")
	r.Header.Set("sec-ch-ua-platform", `"Windows"`)
	r.Header.Set("Priority", "u=0, i")
}

// collapsStreamHeaders returns the headers required for proxying Collaps streams.
func collapsStreamHeaders() map[string]string {
	return map[string]string{
		"User-Agent":         collapsEmbedUA,
		"Origin":             collapsOrigin,
		"Cache-Control":      "no-cache",
		"Pragma":             "no-cache",
		"DNT":                "1",
		"sec-ch-ua":          `"Chromium";v="142", "Google Chrome";v="142", "Not_A Brand";v="99"`,
		"sec-ch-ua-mobile":   "?0",
		"sec-ch-ua-platform": `"Windows"`,
		"sec-fetch-dest":     "empty",
		"sec-fetch-mode":     "cors",
		"sec-fetch-site":     "cross-site",
		"Accept":             "*/*",
		"Priority":           "u=0, i",
	}
}

// collapsHLSQualities fetches an HLS manifest and returns quality variant URLs.
// If the manifest is a master playlist, returns {"1080p":"url", "720p":"url"}.
// Returns nil if not a master playlist or fetch fails.
func (c *CollapsChecker) collapsHLSQualities(ctx context.Context, manifestURL string) map[string]string {
	body, ok := c.fetchText(ctx, manifestURL)
	if !ok {
		return nil
	}
	return parseHLSQualities(body, manifestURL)
}

func (c *CollapsChecker) writeMovie(w http.ResponseWriter, req *http.Request, rjson bool, title, originalTitle, content string, dashMode bool, links *proxylink.Manager) {
	stream := c.pickMovieStream(content, dashMode)
	if stream == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Try to parse HLS quality variants from the master manifest.
	var qualMapAny map[string]any
	if !dashMode && strings.Contains(stream, ".m3u8") {
		if quals := c.collapsHLSQualities(req.Context(), stream); len(quals) > 0 {
			qualMapAny = make(map[string]any, len(quals))
			for label, variantURL := range quals {
				qualMapAny[label] = streamProxyURLWithHeaders(req, variantURL, "collaps", links, collapsStreamHeaders())
			}
		}
	}

	stream = collapsProxyStream(req, stream, dashMode, links)

	name := strings.TrimSpace(submatch1(collapsAudioFirstRe, content))
	if name == "" {
		name = "По умолчанию"
	}

	row := map[string]any{
		"method": "play",
		"url":    stream,
		"stream": stream,
		"name":   name,
		"title":  getsTVJoinName(title, originalTitle),
	}
	if qualMapAny != nil {
		row["quality"] = qualMapAny
		row["qualitys"] = qualMapAny
	}
	if subtitles := collapsParseSubtitlesRaw(submatch1(collapsCCRe, content)); len(subtitles) > 0 {
		row["subtitles"] = subtitles
	}
	if voice := collapsAudioNames(content); voice != "" {
		row["voice_name"] = voice
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
	getsTVAppendMovieHTML(&sb, row, name, true, 0, 0)
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (c *CollapsChecker) writeSerial(
	w http.ResponseWriter,
	req *http.Request,
	route string,
	season int,
	rjson bool,
	title string,
	originalTitle string,
	orid int64,
	kinopoiskID int64,
	imdbID string,
	serial []collapsSeason,
	dashMode bool,
	links *proxylink.Manager,
) {
	if len(serial) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(req)
	ordered := make([]collapsSeason, 0, len(serial))
	for _, s := range serial {
		if s.Season <= 0 {
			continue
		}
		ordered = append(ordered, s)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Season < ordered[j].Season })
	if len(ordered) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if season == -1 {
		rows := make([]map[string]any, 0, len(ordered))
		labels := make([]string, 0, len(ordered))
		for _, s := range ordered {
			name := strconv.Itoa(s.Season) + " сезон"
			rows = append(rows, map[string]any{
				"method": "link",
				"id":     s.Season,
				"url":    c.buildSeasonLink(host, route, rjson, title, originalTitle, orid, kinopoiskID, imdbID, s.Season),
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

	var episodes []collapsEpisode
	for _, s := range ordered {
		if s.Season == season {
			episodes = s.Episodes
			break
		}
	}
	if len(episodes) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	type episodeRow struct {
		data  map[string]any
		label string
		num   int
	}
	baseTitle := getsTVJoinName(title, originalTitle)
	epRows := make([]episodeRow, 0, len(episodes))

	for _, ep := range episodes {
		rawStream := c.pickEpisodeStream(ep, dashMode)
		if rawStream == "" {
			continue
		}

		stream := collapsProxyStream(req, rawStream, dashMode, links)

		epTitle := strings.TrimSpace(ep.Episode)
		if epTitle == "" {
			continue
		}
		epNum := collapsEpisodeNumber(epTitle)
		label := epTitle
		if !strings.Contains(strings.ToLower(label), "сер") {
			label += " серия"
		}

		row := map[string]any{
			"method": "play",
			"url":    stream,
			"stream": stream,
			"s":      season,
			"e":      epNum,
			"name":   label,
			"title":  baseTitle + " (" + label + ")",
		}
		if subs := collapsSubtitleRows(ep.CC); len(subs) > 0 {
			row["subtitles"] = subs
		}
		if voice := collapsJoinEpisodeVoices(ep.Audio.Names); voice != "" {
			row["voice_name"] = voice
		}
		epRows = append(epRows, episodeRow{
			data:  row,
			label: label,
			num:   epNum,
		})
	}
	sort.Slice(epRows, func(i, j int) bool {
		if epRows[i].num == epRows[j].num {
			return epRows[i].label < epRows[j].label
		}
		return epRows[i].num < epRows[j].num
	})
	if len(epRows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	data := make([]map[string]any, 0, len(epRows))
	labels := make([]string, 0, len(epRows))
	seasons := make([]int, 0, len(epRows))
	episodesNum := make([]int, 0, len(epRows))
	for _, row := range epRows {
		data = append(data, row.data)
		labels = append(labels, row.label)
		seasons = append(seasons, season)
		episodesNum = append(episodesNum, row.num)
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "episode",
			"data": data,
		})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range data {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, seasons[i], episodesNum[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (c *CollapsChecker) pickMovieStream(content string, dashMode bool) string {
	hls := collapsNormalizeURL(submatch1(collapsHLSRe, content))
	dash := collapsNormalizeURL(submatch1(collapsDashRe, content))

	if dashMode && dash != "" {
		return dash
	}
	if hls != "" {
		return hls
	}
	return dash
}

func (c *CollapsChecker) pickEpisodeStream(ep collapsEpisode, dashMode bool) string {
	hls := collapsNormalizeURL(ep.HLS)
	dash := collapsNormalizeURL(ep.Dasha)
	if dash == "" {
		dash = collapsNormalizeURL(ep.Dash)
	}

	if dashMode && dash != "" {
		return dash
	}
	if hls != "" {
		return hls
	}
	return dash
}

func (c *CollapsChecker) buildSeasonLink(
	host, route string,
	rjson bool,
	title, originalTitle string,
	orid, kinopoiskID int64,
	imdbID string,
	season int,
) string {
	q := url.Values{}
	q.Set("rjson", getsTVBool(rjson))
	if orid > 0 {
		q.Set("orid", strconv.FormatInt(orid, 10))
	}
	if kinopoiskID > 0 {
		q.Set("kinopoisk_id", strconv.FormatInt(kinopoiskID, 10))
	}
	if imdbID != "" {
		q.Set("imdb_id", imdbID)
	}
	if title != "" {
		q.Set("title", title)
	}
	if originalTitle != "" {
		q.Set("original_title", originalTitle)
	}
	q.Set("s", strconv.Itoa(season))
	return host + route + "?" + q.Encode()
}

// collapsProxyStream wraps a Collaps stream URL through /proxy/.
// Embeds the full set of stream headers (Origin, User-Agent, sec-fetch-*)
// because CDN binds streams to the headers that fetched the embed page.
func collapsProxyStream(req *http.Request, rawURL string, dashMode bool, links *proxylink.Manager) string {
	return streamProxyURLWithHeaders(req, rawURL, "collaps", links, collapsStreamHeaders())
}

func collapsNormalizeURL(link string) string {
	link = strings.TrimSpace(link)
	if link == "" {
		return ""
	}
	link = strings.ReplaceAll(link, `\u0026`, "&")
	link = strings.ReplaceAll(link, `\u003d`, "=")
	link = strings.ReplaceAll(link, `\/`, "/")
	return link
}

func collapsAudioNames(content string) string {
	raw := strings.TrimSpace(submatch1(collapsAudioListRe, content))
	if raw == "" {
		return ""
	}
	raw = strings.ReplaceAll(raw, `"`, "")
	raw = strings.ReplaceAll(raw, "delete", "")
	parts := strings.Split(raw, ",")
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		clean = append(clean, part)
	}
	return strings.Join(clean, ", ")
}

func collapsJoinEpisodeVoices(names []string) string {
	clean := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		clean = append(clean, name)
	}
	return strings.Join(clean, ", ")
}

func collapsParseSubtitlesRaw(raw string) []map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	var rows []collapsCC
	if err := stdjson.Unmarshal([]byte(raw), &rows); err != nil {
		return nil
	}
	return collapsSubtitleRows(rows)
}

func collapsSubtitleRows(rows []collapsCC) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		link := collapsNormalizeURL(row.URL)
		if link == "" {
			continue
		}
		label := strings.TrimSpace(row.Name)
		if label == "" {
			label = "Субтитры"
		}
		out = append(out, map[string]any{
			"method": "link",
			"label":  label,
			"url":    link,
		})
	}
	return out
}

func collapsEpisodeNumber(v string) int {
	m := collapsEpisodeNumRe.FindStringSubmatch(strings.TrimSpace(v))
	if len(m) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(m[1]))
	return n
}

func (c *CollapsChecker) normalizePoster(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") {
		return v
	}
	if strings.HasPrefix(v, "//") {
		return "https:" + v
	}
	if strings.HasPrefix(v, "/") {
		return c.apiHost + v
	}
	return v
}
