package litesrc

import (
	stdjson "encoding/json"
	"fmt"
	"io"
	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/proxylink"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type rutubeMovieChecker struct {
	client *http.Client
	host   string
	links  *proxylink.Manager
}

type rutubeSearchResponse struct {
	Results []rutubeSearchItem `json:"results"`
}

type rutubeSearchItem struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Duration     int64  `json:"duration"`
	IsHidden     bool   `json:"is_hidden"`
	IsDeleted    bool   `json:"is_deleted"`
	IsAdult      bool   `json:"is_adult"`
	IsLocked     bool   `json:"is_locked"`
	IsAudio      bool   `json:"is_audio"`
	IsPaid       bool   `json:"is_paid"`
	IsLivestream bool   `json:"is_livestream"`
	Category     struct {
		ID int `json:"id"`
	} `json:"category"`
}

type rutubePlayResponse struct {
	VideoBalancer struct {
		M3U8 string `json:"m3u8"`
	} `json:"video_balancer"`
}

func NewRutubeMovieChecker(cfg config.Config) *rutubeMovieChecker {
	host := strings.TrimSpace(cfg.Online.RutubeMovie.Host)
	if host == "" {
		host = "https://rutube.ru"
	}
	host = strings.TrimRight(host, "/")

	return &rutubeMovieChecker{
		client: httpclient.NewForBalancer("rutubemovie", 10*time.Second),
		host:   host,
	}
}

func (r *rutubeMovieChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	r.links = links

	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show := r.checkSearch(req)
			writeCheckSearchResponseNoRCH(w, show, pluginQualityBadgeGet("rutubemovie"))
			return
		}

		// Dispatch: /lite/rutubemovie/play → play(), else → index()
		if strings.HasSuffix(req.URL.Path, "/play") {
			r.play(w, req)
			return
		}

		r.index(w, req)
	}
}

// index searches Rutube and returns movie items with method:"call"
func (r *rutubeMovieChecker) index(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))

	if strings.TrimSpace(q.Get("serial")) == "1" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	title := strings.TrimSpace(q.Get("title"))
	if title == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	year, err := strconv.Atoi(strings.TrimSpace(q.Get("year")))
	if err != nil || year == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Search Rutube API
	results, err := r.searchRutube(req, title, year)
	if err != nil {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(req)
	compactSearch := compactTitle(title)

	type movieRow struct {
		data  map[string]any
		label string
	}
	var rows []movieRow

	for _, item := range results {
		if !rutubeItemMatch(item, compactSearch, year) {
			continue
		}

		rows = append(rows, movieRow{
			data: map[string]any{
				"method":    "call",
				"url":       host + "/lite/rutubemovie/play?linkid=" + item.ID,
				"title":     item.Title,
				"translate": item.Title,
			},
			label: item.Title,
		})
	}

	if len(rows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		jsonRows := make([]map[string]any, len(rows))
		for i, row := range rows {
			jsonRows[i] = row.data
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "movie",
			"data": jsonRows,
		})
		return
	}

	// HTML response for Lampa client
	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range rows {
		getsTVAppendMovieHTML(&sb, row.data, row.label, i == 0, 0, 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// play fetches the m3u8 stream URL for a given Rutube video
func (r *rutubeMovieChecker) play(w http.ResponseWriter, req *http.Request) {
	linkid := strings.TrimSpace(req.URL.Query().Get("linkid"))
	if linkid == "" {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	apiURL := fmt.Sprintf("%s/api/play/options/%s/?no_404=true&referer=&pver=v2&client=wdp", r.host, url.PathEscape(linkid))

	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, apiURL, nil)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")

	resp, err := r.client.Do(httpReq)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	var playResp rutubePlayResponse
	if err := stdjson.Unmarshal(body, &playResp); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	m3u8 := strings.TrimSpace(playResp.VideoBalancer.M3U8)
	if m3u8 == "" {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	proxyURL := streamProxyURL(req, m3u8, "rutubemovie", r.links)

	writeJSON(w, http.StatusOK, map[string]any{
		"method":  "play",
		"url":     proxyURL,
		"title":   "auto",
		"quality": map[string]string{"auto": proxyURL},
	})
}

// searchRutube calls the Rutube search API and returns results.
func (r *rutubeMovieChecker) searchRutube(req *http.Request, title string, year int) ([]rutubeSearchItem, error) {
	u, err := url.Parse(r.host + "/api/search/video/")
	if err != nil {
		return nil, err
	}
	qs := url.Values{}
	qs.Set("content_type", "video")
	qs.Set("duration", "movie")
	qs.Set("query", title+" "+strconv.Itoa(year))
	u.RawQuery = qs.Encode()

	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	httpReq.Header.Set("X-Lampac-Go", "1")

	resp, err := r.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("rutube search: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}

	var root rutubeSearchResponse
	if err := stdjson.Unmarshal(body, &root); err != nil {
		return nil, err
	}

	return root.Results, nil
}

func (r *rutubeMovieChecker) checkSearch(req *http.Request) bool {
	q := req.URL.Query()
	if strings.TrimSpace(q.Get("serial")) == "1" {
		return false
	}

	title := strings.TrimSpace(q.Get("title"))
	if title == "" {
		return false
	}

	year, err := strconv.Atoi(strings.TrimSpace(q.Get("year")))
	if err != nil || year == 0 {
		return false
	}

	results, err := r.searchRutube(req, title, year)
	if err != nil || len(results) == 0 {
		return false
	}

	compactSearch := compactTitle(title)
	for _, item := range results {
		if rutubeItemMatch(item, compactSearch, year) {
			return true
		}
	}

	return false
}

// rutubeItemMatch checks whether a Rutube search result matches the search criteria.
// Uses compactTitle (C#-style SearchName) for fuzzy title matching.
func rutubeItemMatch(item rutubeSearchItem, compactSearch string, year int) bool {
	compact := compactTitle(item.Title)
	if compact == "" || !strings.Contains(compact, compactSearch) {
		return false
	}
	if !(strings.Contains(compact, strconv.Itoa(year)) ||
		strings.Contains(compact, strconv.Itoa(year+1)) ||
		strings.Contains(compact, strconv.Itoa(year-1))) {
		return false
	}
	if item.Duration <= 3000 {
		return false
	}
	// Use normalizeSearchTitle for keyword filtering (preserves word boundaries)
	name := normalizeSearchTitle(item.Title)
	if nameContainsAny(name, "трейлер", "trailer", "премьера", "обзор", "сезон", "сериал", "серия", "серий") {
		return false
	}
	if item.Category.ID != 4 {
		return false
	}
	if item.IsHidden || item.IsDeleted || item.IsAdult || item.IsLocked || item.IsAudio || item.IsPaid || item.IsLivestream {
		return false
	}
	return true
}

func nameContainsAny(name string, words ...string) bool {
	for _, word := range words {
		if strings.Contains(name, word) {
			return true
		}
	}
	return false
}

// compactTitle strips all non-letter/non-digit characters, lowercases,
// and normalises ё→е, matching the C# StringConvert.SearchName() behaviour.
// This allows fuzzy matching regardless of dashes, spaces, punctuation, etc.
func compactTitle(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9',
			r >= 'a' && r <= 'z',
			r >= 'а' && r <= 'я':
			b.WriteRune(r)
		case r == 'ё':
			b.WriteRune('е')
		}
	}
	return b.String()
}
