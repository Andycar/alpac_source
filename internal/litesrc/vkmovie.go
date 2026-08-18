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
	"sync"
	"time"
)

const (
	vkMovieClientID = 52461373
	vkMovieAppID    = 6287487
)

type vkMovieChecker struct {
	client   *http.Client
	host     string
	tokenURL string
	links    *proxylink.Manager

	mu           sync.Mutex
	accessToken  string
	tokenExpires time.Time
}

type vkMovieTokenResponse struct {
	Data struct {
		AccessToken string `json:"access_token"`
		Expires     int64  `json:"expires"`
		ExpiredAt   int64  `json:"expired_at"`
	} `json:"data"`
}

type vkMovieSearchResponse struct {
	Response struct {
		CatalogVideos []vkMovieCatalogItem `json:"catalog_videos"`
	} `json:"response"`
}

type vkMovieCatalogItem struct {
	Video *vkMovieVideo `json:"video"`
}

type vkMovieVideo struct {
	Title     string            `json:"title"`
	Duration  int64             `json:"duration"`
	Files     vkMovieVideoFile  `json:"files"`
	Subtitles []vkMovieSubtitle `json:"subtitles"`
}

type vkMovieVideoFile struct {
	MP4_2160 string `json:"mp4_2160"`
	MP4_1440 string `json:"mp4_1440"`
	MP4_1080 string `json:"mp4_1080"`
	MP4_720  string `json:"mp4_720"`
	MP4_480  string `json:"mp4_480"`
	MP4_360  string `json:"mp4_360"`
	MP4_240  string `json:"mp4_240"`
	MP4_144  string `json:"mp4_144"`
}

type vkMovieSubtitle struct {
	Lang         string `json:"lang"`
	Title        string `json:"title"`
	URL          string `json:"url"`
	ManifestName string `json:"manifest_name"`
}

func NewVKMovieChecker(cfg config.Config) *vkMovieChecker {
	host := strings.TrimSpace(cfg.Online.VKMovie.Host)
	if host == "" {
		host = "https://api.vkvideo.ru"
	}
	host = strings.TrimRight(host, "/")

	tokenURL := strings.TrimSpace(cfg.Online.VKMovie.TokenURL)
	if tokenURL == "" {
		tokenURL = "https://login.vk.com/?act=get_anonym_token"
	}

	return &vkMovieChecker{
		client:   httpclient.NewForBalancer("vkmovie", 10*time.Second),
		host:     host,
		tokenURL: tokenURL,
	}
}

func (v *vkMovieChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	v.links = links

	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show := v.checkSearch(req)
			writeCheckSearchResponseNoRCH(w, show, pluginQualityBadgeGet("vkmovie"))
			return
		}

		v.index(w, req)
	}
}

// index searches VK Video and returns movie items with method:"play" and quality map.
func (v *vkMovieChecker) index(w http.ResponseWriter, req *http.Request) {
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

	results, err := v.searchVK(req, title, year)
	if err != nil {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(req)
	reqIP := clientIP(req)
	compactSearch := compactTitle(title)

	// VK stream headers — User-Agent MUST match srcAg= in the CDN URL.
	// VK CDN checks srcAg=CHROME and rejects non-Chrome User-Agents with 400.
	vkHeaders := map[string]string{
		"Origin":     "https://vkvideo.ru",
		"Referer":    "https://vkvideo.ru/",
		"User-Agent": "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36",
	}

	type movieRow struct {
		data  map[string]any
		label string
	}
	var rows []movieRow

	for _, item := range results {
		video := item.Video
		if video == nil {
			continue
		}

		if !vkItemMatch(*video, compactSearch, year) {
			continue
		}

		// Build quality streams (ordered from highest to lowest)
		streams, qualityMap := v.buildStreams(video.Files, host, reqIP, vkHeaders)
		if len(streams) == 0 {
			continue
		}

		bestURL := streams[0]["url"].(string)
		bestLabel := streams[0]["quality"].(string)

		// Build subtitles
		var subtitles []map[string]any
		for _, sub := range video.Subtitles {
			if sub.URL == "" {
				continue
			}
			label := sub.ManifestName
			if label == "" {
				label = sub.Title
			}
			if label == "" {
				label = sub.Lang
			}
			proxySubURL := v.proxyURL(sub.URL, host, reqIP, vkHeaders)
			subtitles = append(subtitles, map[string]any{
				"method": "link",
				"label":  label,
				"url":    proxySubURL,
			})
		}

		row := map[string]any{
			"method":        "play",
			"url":           bestURL,
			"stream":        bestURL,
			"title":         video.Title,
			"name":          video.Title,
			"translate":     video.Title,
			"quality":       qualityMap,
			"qualitys":      qualityMap,
			"streamquality": streams,
			"maxquality":    bestLabel,
		}
		if len(subtitles) > 0 {
			row["subtitles"] = subtitles
		}

		rows = append(rows, movieRow{
			data:  row,
			label: video.Title,
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

// buildStreams creates ordered stream list and quality map for all available streams.
// Returns (streamquality array, quality map).
func (v *vkMovieChecker) buildStreams(files vkMovieVideoFile, host, reqIP string, headers map[string]string) ([]map[string]any, map[string]string) {
	type qEntry struct {
		url   string
		label string
	}
	entries := []qEntry{
		{files.MP4_2160, "2160p"},
		{files.MP4_1440, "1440p"},
		{files.MP4_1080, "1080p"},
		{files.MP4_720, "720p"},
		{files.MP4_480, "480p"},
		{files.MP4_360, "360p"},
		{files.MP4_240, "240p"},
		{files.MP4_144, "144p"},
	}

	var streams []map[string]any
	quals := make(map[string]string, 8)

	for _, e := range entries {
		if e.url != "" {
			proxyURL := v.proxyURL(e.url, host, reqIP, headers)
			streams = append(streams, map[string]any{
				"quality": e.label,
				"url":     proxyURL,
			})
			quals[e.label] = proxyURL
		}
	}

	return streams, quals
}

// proxyURL encrypts a stream URL with VK headers through the proxy.
// Appends .mp4 extension so HTML5 <video> can detect the MIME type.
func (v *vkMovieChecker) proxyURL(rawURL, host, reqIP string, headers map[string]string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" || v.links == nil {
		return rawURL
	}
	if isStreamProxyDisabled("vkmovie") {
		return rawURL
	}
	encrypted := v.links.EncryptURIWithHeaders(rawURL, reqIP, "vkmovie", headers)
	if encrypted == "" {
		return rawURL
	}
	return host + "/proxy/" + encrypted + ".mp4"
}

// searchVK calls the VK Video search API and returns results.
func (v *vkMovieChecker) searchVK(req *http.Request, title string, year int) ([]vkMovieCatalogItem, error) {
	token := v.ensureAnonymToken(req)
	if token == "" {
		return nil, fmt.Errorf("vkmovie: failed to get anonymous token")
	}

	searchURL := v.host + "/method/catalog.getVideoSearchWeb2?v=5.264&client_id=" + strconv.Itoa(vkMovieClientID)
	data := url.Values{}
	data.Set("screen_ref", "search_video_service")
	data.Set("input_method", "keyboard_search_button")
	data.Set("q", title+" "+strconv.Itoa(year))
	data.Set("access_token", token)

	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodPost, searchURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	httpReq.Header.Set("X-Lampac-Go", "1")

	resp, err := v.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("vkmovie search: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}

	var root vkMovieSearchResponse
	if err := stdjson.Unmarshal(body, &root); err != nil {
		return nil, err
	}

	return root.Response.CatalogVideos, nil
}

func (v *vkMovieChecker) checkSearch(req *http.Request) bool {
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

	results, err := v.searchVK(req, title, year)
	if err != nil || len(results) == 0 {
		return false
	}

	compactSearch := compactTitle(title)
	for _, item := range results {
		if item.Video == nil {
			continue
		}
		if vkItemMatch(*item.Video, compactSearch, year) {
			return true
		}
	}

	return false
}

// vkItemMatch checks whether a VK video result matches the search criteria.
func vkItemMatch(video vkMovieVideo, compactSearch string, year int) bool {
	compact := compactTitle(video.Title)
	if compact == "" || !strings.Contains(compact, compactSearch) {
		return false
	}
	if !(strings.Contains(compact, strconv.Itoa(year)) ||
		strings.Contains(compact, strconv.Itoa(year+1)) ||
		strings.Contains(compact, strconv.Itoa(year-1))) {
		return false
	}
	if video.Duration < 3000 {
		return false
	}
	// Use normalizeSearchTitle for keyword filtering (preserves word boundaries)
	name := normalizeSearchTitle(video.Title)
	if nameContainsAny(name, "трейлер", "trailer", "премьера", "обзор", "сезон", "сериал", "серия", "серий") {
		return false
	}
	if video.Files.MP4_2160 == "" &&
		video.Files.MP4_1440 == "" &&
		video.Files.MP4_1080 == "" &&
		video.Files.MP4_720 == "" {
		return false
	}
	return true
}

func (v *vkMovieChecker) ensureAnonymToken(req *http.Request) string {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.accessToken != "" && time.Now().UTC().Before(v.tokenExpires) {
		return v.accessToken
	}

	body := url.Values{}
	body.Set("client_secret", "o557NLIkAErNhakXrQ7A")
	body.Set("client_id", strconv.Itoa(vkMovieClientID))
	body.Set("scopes", "audio_anonymous,video_anonymous,photos_anonymous,profile_anonymous")
	body.Set("isApiOauthAnonymEnabled", "false")
	body.Set("version", "1")
	body.Set("app_id", strconv.Itoa(vkMovieAppID))

	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodPost, v.tokenURL, strings.NewReader(body.Encode()))
	if err != nil {
		return ""
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	httpReq.Header.Set("X-Lampac-Go", "1")

	resp, err := v.client.Do(httpReq)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}

	var root vkMovieTokenResponse
	if err := stdjson.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&root); err != nil {
		return ""
	}
	if root.Data.AccessToken == "" {
		return ""
	}

	exp := int64(0)
	if root.Data.Expires > 0 {
		exp = root.Data.Expires
	} else if root.Data.ExpiredAt > 0 {
		exp = root.Data.ExpiredAt
	}

	if exp > 0 {
		// Keep the same safety behavior as legacy: refresh token earlier.
		v.tokenExpires = time.Unix(exp, 0).UTC().Add(-4 * time.Hour)
	} else {
		v.tokenExpires = time.Now().UTC().Add(10 * time.Hour)
	}
	v.accessToken = root.Data.AccessToken
	return v.accessToken
}
