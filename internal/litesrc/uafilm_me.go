package litesrc

import (
	stdjson "encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"lampac-go/internal/config"
	"lampac-go/internal/proxylink"
)

// UAFilm (uafilm.me) — Ukrainian balancer with a clean JSON API. Ported from
// lampac-nextgen Modules/OnlineUKR/UAFilm. Unlike the DLE/PlayerJS UKR sources
// (eneyida/ashdi/kinoukr, which scrape HTML), UAFilm exposes /api/v1 JSON:
//
//   - search:  GET /api/v1/search/{query}?loader=searchPage
//   - title:   GET /api/v1/titles/{orid}?loader=titlePage        (movie videos / series meta)
//   - season:  GET /api/v1/titles/{orid}/seasons/{s}?loader=seasonPage
//   - watch:   GET /api/v1/watch/{videoId}                        (episode → direct HLS)
//
// A title's `videos[]` (movies) and an episode's watch `src` are direct
// index.m3u8 URLs on uafilm.me — no referer needed — proxied through /proxy/
// like every other balancer. Only videos whose origin is a known UA CDN
// (ashdi/tortuga/hdvbua) are offered; trailers (origin "tmdb") are skipped.
//
// `orid` is UAFilm's internal title id. The first request resolves it from the
// TMDB id / IMDb id / original_title+year match, then carries it forward in the
// season/episode links so subsequent calls skip the search.

type uafilmChecker struct {
	client *http.Client
	host   string
}

type uafilmSearchRoot struct {
	Results []uafilmResult `json:"results"`
}

type uafilmResult struct {
	ID            uafilmFlexInt `json:"id"`
	IMDBID        string        `json:"imdb_id"`
	TMDBID        uafilmFlexInt `json:"tmdb_id"`
	Name          string        `json:"name"`
	OriginalTitle string        `json:"original_title"`
	Year          uafilmFlexInt `json:"year"`
	Poster        string        `json:"poster"`
	Type          string        `json:"type"`
}

// uafilmFlexInt unmarshals from either a JSON number or string. UAFilm's search
// mixes real library titles (integer id) with TMDB catalog suggestions whose id
// is a base64 composite string and whose year may be absent — those aren't
// playable, so they decode to 0 and are filtered out rather than failing the
// whole response (encoding/json would otherwise return a type error).
type uafilmFlexInt int64

func (f *uafilmFlexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		*f = 0
		return nil
	}
	*f = uafilmFlexInt(n)
	return nil
}

type uafilmTitleRoot struct {
	Title    uafilmTitle    `json:"title"`
	Episodes uafilmEpisodes `json:"episodes"`
}

type uafilmTitle struct {
	IsSeries     bool          `json:"is_series"`
	SeasonsCount int           `json:"seasons_count"`
	Videos       []uafilmVideo `json:"videos"`
}

type uafilmVideo struct {
	Name   string `json:"name"`
	Origin string `json:"origin"`
	Src    string `json:"src"`
	Type   string `json:"type"`
}

type uafilmEpisodes struct {
	Data []uafilmEpisodeData `json:"data"`
}

type uafilmEpisodeData struct {
	SeasonNumber  int               `json:"season_number"`
	EpisodeNumber int               `json:"episode_number"`
	PrimaryVideo  *uafilmPrimaryVid `json:"primary_video"`
}

type uafilmPrimaryVid struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type uafilmWatchRoot struct {
	Video *uafilmVideo `json:"video"`
}

// uafilmNameEquals compares two titles case- and punctuation-insensitively
// (letters/digits only), so "Thunderbolts*" matches "Thunderbolts".
func uafilmNameEquals(a, b string) bool {
	norm := func(v string) string {
		v = strings.ToLower(strings.TrimSpace(v))
		var sb strings.Builder
		sb.Grow(len(v))
		for _, r := range v {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				sb.WriteRune(r)
			}
		}
		return sb.String()
	}
	na, nb := norm(a), norm(b)
	return na != "" && na == nb
}

// uafilmStreamOrigins are the CDN origins whose `src` is a playable HLS. TMDB
// trailers (origin "tmdb", type "embed") are excluded.
func uafilmIsStreamOrigin(origin string) bool {
	switch strings.ToLower(strings.TrimSpace(origin)) {
	case "ashdi", "tortuga", "hdvbua":
		return true
	}
	return false
}

func NewUAFilmChecker(cfg config.Config) *uafilmChecker {
	host := strings.TrimSpace(strings.TrimRight(cfg.Online.UAFilm.Host, "/"))
	if host == "" {
		host = "https://uafilm.me"
	}
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}
	return &uafilmChecker{
		client: newUAFilmClient(),
		host:   host,
	}
}

// newUAFilmClient builds a plain HTTP/2 client. uafilm.me's WAF blocks both the
// shared uTLS transport AND Go's HTTP/1.1-only shared transport (403 "Доступ
// заборонено"), but accepts a standard HTTP/2 handshake — so we clone Go's
// default transport (which negotiates h2 via ALPN) instead of
// httpclient.NewForBalancer.
func newUAFilmClient() *http.Client {
	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		// DefaultTransport can be swapped for a non-*http.Transport (tests
		// do this) — fall back to an equivalent h2-capable transport.
		tr = &http.Transport{Proxy: http.ProxyFromEnvironment, ForceAttemptHTTP2: true}
	}
	transport := tr.Clone() // preserves ForceAttemptHTTP2 + ALPN so h2 negotiates
	transport.MaxIdleConns = 20
	transport.IdleConnTimeout = 90 * time.Second
	return &http.Client{
		Timeout:   15 * time.Second,
		Transport: transport,
	}
}

func (u *uafilmChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			writeCheckSearchResponse(w, u.probe(req), pluginQualityBadgeGet("uafilm"))
			return
		}
		// /lite/uafilm/video[.m3u8] resolves an episode's primary video to HLS.
		if strings.Contains(req.URL.Path, "/video") {
			u.video(w, req, links)
			return
		}
		u.index(w, req, links)
	}
}

func (u *uafilmChecker) probe(req *http.Request) bool {
	target := u.host + "/api/v1/search/thunderbolts?loader=searchPage"
	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, target, nil)
	if err != nil {
		return false
	}
	u.setHeaders(httpReq)
	resp, err := u.client.Do(httpReq)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 400
}

func (u *uafilmChecker) setHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("Referer", u.host+"/")
	req.Header.Set("X-Lampac-Go", "1")
}

func (u *uafilmChecker) getJSON(req *http.Request, path string, out any) bool {
	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, u.host+path, nil)
	if err != nil {
		return false
	}
	u.setHeaders(httpReq)
	resp, err := u.client.Do(httpReq)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return false
	}
	return stdjson.Unmarshal(body, out) == nil
}

func (u *uafilmChecker) index(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	similar := parseBoolParam(q.Get("similar"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	imdbID := strings.TrimSpace(q.Get("imdb_id"))
	clarification, _ := getsTVQueryInt(q.Get("clarification"))
	year, _ := getsTVQueryInt(q.Get("year"))
	orid, _ := getsTVQueryInt(q.Get("orid"))
	s, sSet := getsTVQueryInt(q.Get("s"))
	if !sSet {
		s = -1
	}

	tmdbID, _ := getsTVQueryInt(q.Get("id"))
	if v, ok := getsTVQueryInt(q.Get("tmdb_id")); ok && v > 0 {
		tmdbID = v
	}

	// Resolve UAFilm's internal title id (orid) on the first request.
	if orid == 0 {
		query := originalTitle
		if similar || clarification == 1 {
			query = title
		}
		if query == "" {
			query = title
		}
		if query == "" {
			writeGetsTVEmpty(w, rjson)
			return
		}

		var sr uafilmSearchRoot
		if !u.getJSON(req, "/api/v1/search/"+url.PathEscape(query)+"?loader=searchPage", &sr) || len(sr.Results) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}

		if !similar {
			for _, r := range sr.Results {
				if r.ID <= 0 { // TMDB catalog suggestion, not a playable uafilm title
					continue
				}
				if (imdbID != "" && r.IMDBID == imdbID) ||
					(tmdbID > 0 && int64(r.TMDBID) == int64(tmdbID)) ||
					(year > 0 && int(r.Year) == year && uafilmNameEquals(r.OriginalTitle, originalTitle)) {
					orid = int(r.ID)
					break
				}
			}
		}

		if orid == 0 {
			u.writeSimilar(w, req, rjson, sr.Results, title, originalTitle)
			return
		}
	}

	// Series episode listing: orid is known and it's a series (Lampa only asks
	// for s>0 after we returned seasons), so go straight to the season page.
	if s > 0 {
		var sr uafilmTitleRoot
		if !u.getJSON(req, fmt.Sprintf("/api/v1/titles/%d/seasons/%d?loader=seasonPage", orid, s), &sr) {
			writeGetsTVEmpty(w, rjson)
			return
		}
		u.writeEpisodes(w, req, rjson, sr.Episodes.Data, orid, imdbID, title, originalTitle, s)
		return
	}

	// Title page — decides movie vs series.
	var tr uafilmTitleRoot
	if !u.getJSON(req, fmt.Sprintf("/api/v1/titles/%d?loader=titlePage", orid), &tr) {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if tr.Title.IsSeries {
		u.writeSeasons(w, req, rjson, tr.Title.SeasonsCount, orid, imdbID, title, originalTitle)
		return
	}
	u.writeMovie(w, req, rjson, tr.Title.Videos, title, originalTitle, links)
}

// video resolves an episode's primary video id to its HLS stream.
func (u *uafilmChecker) video(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	vid := strings.TrimSpace(req.URL.Query().Get("vid"))
	if vid == "" {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	var wr uafilmWatchRoot
	if !u.getJSON(req, "/api/v1/watch/"+url.PathEscape(vid), &wr) || wr.Video == nil || strings.TrimSpace(wr.Video.Src) == "" {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	stream := streamProxyURL(req, strings.TrimSpace(wr.Video.Src), "uafilm", links)

	if parseBoolParam(req.URL.Query().Get("play")) {
		http.Redirect(w, req, stream, http.StatusFound)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"method": "play",
		"url":    stream,
	})
}

func (u *uafilmChecker) writeMovie(w http.ResponseWriter, req *http.Request, rjson bool, videos []uafilmVideo, title, originalTitle string, links *proxylink.Manager) {
	baseTitle := getsTVJoinName(title, originalTitle)
	rows := make([]map[string]any, 0, len(videos))
	labels := make([]string, 0, len(videos))

	for _, v := range videos {
		if !uafilmIsStreamOrigin(v.Origin) {
			continue
		}
		src := strings.TrimSpace(v.Src)
		if src == "" {
			continue
		}
		stream := streamProxyURL(req, src, "uafilm", links)
		label := strings.TrimSpace(v.Name)
		if label == "" {
			label = "Українською"
		}
		rows = append(rows, map[string]any{
			"method": "play",
			"url":    stream,
			"stream": stream,
			"name":   label,
			"title":  fmt.Sprintf("%s (%s)", baseTitle, label),
		})
		labels = append(labels, label)
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

func (u *uafilmChecker) writeSeasons(w http.ResponseWriter, req *http.Request, rjson bool, seasonsCount, orid int, imdbID, title, originalTitle string) {
	if seasonsCount < 1 {
		seasonsCount = 1
	}
	host := hostFromRequest(req)
	encTitle := url.QueryEscape(title)
	encOriginal := url.QueryEscape(originalTitle)
	encImdb := url.QueryEscape(imdbID)

	rows := make([]map[string]any, 0, seasonsCount)
	labels := make([]string, 0, seasonsCount)
	for i := 1; i <= seasonsCount; i++ {
		link := fmt.Sprintf("%s/lite/uafilm?rjson=%s&serial=1&orid=%d&imdb_id=%s&title=%s&original_title=%s&s=%d",
			host, getsTVBool(rjson), orid, encImdb, encTitle, encOriginal, i)
		label := fmt.Sprintf("%d сезон", i)
		rows = append(rows, map[string]any{
			"method": "link",
			"id":     i,
			"url":    link,
			"name":   label,
		})
		labels = append(labels, label)
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "season", "data": rows})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range rows {
		getsTVAppendSeasonHTML(&sb, row, labels[i], i == 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (u *uafilmChecker) writeEpisodes(w http.ResponseWriter, req *http.Request, rjson bool, episodes []uafilmEpisodeData, orid int, imdbID, title, originalTitle string, s int) {
	host := hostFromRequest(req)
	baseTitle := getsTVJoinName(title, originalTitle)

	rows := make([]map[string]any, 0, len(episodes))
	labels := make([]string, 0, len(episodes))
	seasons := make([]int, 0, len(episodes))
	epNums := make([]int, 0, len(episodes))

	for _, ep := range episodes {
		if ep.SeasonNumber != s || ep.PrimaryVideo == nil || ep.PrimaryVideo.ID == 0 {
			continue
		}
		vid := strconv.FormatInt(ep.PrimaryVideo.ID, 10)
		link := fmt.Sprintf("%s/lite/uafilm/video?vid=%s", host, vid)
		stream := fmt.Sprintf("%s/lite/uafilm/video.m3u8?vid=%s&play=true", host, vid)
		label := fmt.Sprintf("%d серія", ep.EpisodeNumber)
		name := strings.TrimSpace(ep.PrimaryVideo.Name)
		if name == "" {
			name = baseTitle
		}
		rows = append(rows, map[string]any{
			"method": "call",
			"url":    link,
			"stream": stream,
			"s":      s,
			"e":      ep.EpisodeNumber,
			"name":   label,
			"title":  fmt.Sprintf("%s (%s)", baseTitle, label),
		})
		labels = append(labels, label)
		seasons = append(seasons, s)
		epNums = append(epNums, ep.EpisodeNumber)
	}

	if len(rows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "episode", "data": rows})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range rows {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, seasons[i], epNums[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (u *uafilmChecker) writeSimilar(w http.ResponseWriter, req *http.Request, rjson bool, items []uafilmResult, title, originalTitle string) {
	if len(items) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	host := hostFromRequest(req)
	encTitle := url.QueryEscape(title)
	encOriginal := url.QueryEscape(originalTitle)

	data := make([]map[string]any, 0, len(items))
	labels := make([]string, 0, len(items))
	for _, item := range items {
		if item.ID <= 0 { // skip TMDB suggestions with no playable uafilm id
			continue
		}
		link := fmt.Sprintf("%s/lite/uafilm?orid=%d&title=%s&original_title=%s&rjson=%s",
			host, int64(item.ID), encTitle, encOriginal, getsTVBool(rjson))
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = strings.TrimSpace(item.OriginalTitle)
		}
		yearStr := ""
		if item.Year > 0 {
			yearStr = strconv.FormatInt(int64(item.Year), 10)
		}
		data = append(data, map[string]any{
			"method":  "link",
			"url":     link,
			"similar": true,
			"year":    yearStr,
			"details": "",
			"title":   name,
			"img":     item.Poster,
		})
		labels = append(labels, name)
	}

	if len(data) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "similar", "data": data})
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
