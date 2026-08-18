package litesrc

import (
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/proxylink"

	"github.com/rs/zerolog/log"
)

var (
	animebesstItemRe   = regexp.MustCompile(`(?is)class="shortstory-listab-title"><a href="(https?://[^"]+\.html)">([^<]+)</a>`)
	animebesstYearRe   = regexp.MustCompile(`">([0-9]{4})</a>`)
	animebesstImgRe    = regexp.MustCompile(`(?i)<img[^>]+class="img-fit[^"]*lozad[^"]*"[^>]+data-src="([^"]+)"`)
	animebesstSeasonRe = regexp.MustCompile(`([0-9]+)\s*сезон`)
	animebesstVideoRe  = regexp.MustCompile(`"id":"([0-9]+)(\s*[^"]*)?","link":"(https?:)?\\?/\\?/([^"]+)"`)
	animebesstHLSRe    = regexp.MustCompile(`(?i)file:\s*"(https?://[^"]+\.m3u8[^"]*)"`)
)

type animebesstChecker struct {
	client *http.Client
	host   string
}

func NewAnimebesstChecker(cfg config.Config) *animebesstChecker {
	host := strings.TrimSpace(cfg.Online.Animebesst.Host)
	if host == "" {
		host = "https://anime1.best"
	}
	host = strings.TrimRight(host, "/")

	return &animebesstChecker{
		client: httpclient.NewForBalancer("animebesst", 10*time.Second),
		host:   host,
	}
}

func (a *animebesstChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show := a.checkSearch(req)
			writeCheckSearchResponseNoRCH(w, show, pluginQualityBadgeGet("animebesst"))
			return
		}

		a.index(w, req, links)
	}
}

func (a *animebesstChecker) checkSearch(req *http.Request) bool {
	q := req.URL.Query()
	title := strings.TrimSpace(q.Get("title"))
	if title == "" {
		return false
	}
	year, _ := strconv.Atoi(strings.TrimSpace(q.Get("year")))

	search, ok := a.fetchSearch(req, title)
	if !ok {
		return false
	}
	if !strings.Contains(strings.ToLower(search), "поиск") {
		return false
	}

	want := normalizeSearchTitle(title)
	for _, m := range animebesstItemRe.FindAllStringSubmatch(search, -1) {
		if len(m) < 3 {
			continue
		}
		rowTitle := normalizeSearchTitle(m[2])
		if rowTitle == "" || !strings.Contains(rowTitle, want) {
			continue
		}
		if year > 0 {
			ym := animebesstYearRe.FindStringSubmatch(search)
			if len(ym) >= 2 {
				y, _ := strconv.Atoi(strings.TrimSpace(ym[1]))
				if y > 0 && y != year && y != year-1 && y != year+1 {
					continue
				}
			}
		}
		return true
	}

	return false
}

// ---------------------------------------------------------------------------
// Index: search or episodes
// ---------------------------------------------------------------------------

func (a *animebesstChecker) index(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	similar := parseBoolParam(q.Get("similar"))
	title := strings.TrimSpace(q.Get("title"))
	uri := strings.TrimSpace(q.Get("uri"))
	season, _ := strconv.Atoi(strings.TrimSpace(q.Get("s")))

	if uri != "" {
		// Episodes mode
		a.episodes(w, req, rjson, title, uri, season, links)
		return
	}

	if title == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Search mode
	html, ok := a.fetchSearch(req, title)
	if !ok || html == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Parse search results
	type searchItem struct {
		title  string
		year   string
		uri    string
		season string
		img    string
	}

	// Split by sidebar to get main content
	parts := strings.SplitN(html, `id="sidebar"`, 2)
	mainContent := html
	if len(parts) >= 2 {
		mainContent = parts[0]
	}

	// Split by shortstory-listab
	rows := strings.Split(mainContent, `class="shortstory-listab"`)
	if len(rows) <= 1 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	var items []searchItem
	for _, row := range rows[1:] {
		if strings.Contains(row, "Новости") {
			continue
		}

		m := animebesstItemRe.FindStringSubmatch(row)
		if len(m) < 3 || strings.TrimSpace(m[1]) == "" || strings.TrimSpace(m[2]) == "" {
			continue
		}

		itemURI := strings.TrimSpace(m[1])
		itemTitle := strings.TrimSpace(m[2])

		itemSeason := "0"
		if strings.Contains(itemTitle, "сезон") {
			sm := animebesstSeasonRe.FindStringSubmatch(itemTitle)
			if len(sm) >= 2 {
				itemSeason = sm[1]
			} else {
				itemSeason = "1"
			}
		}

		ym := animebesstYearRe.FindStringSubmatch(row)
		itemYear := ""
		if len(ym) >= 2 {
			itemYear = ym[1]
		}

		imgM := animebesstImgRe.FindStringSubmatch(row)
		itemImg := ""
		if len(imgM) >= 2 {
			itemImg = strings.TrimSpace(imgM[1])
		}

		items = append(items, searchItem{
			title:  itemTitle,
			year:   itemYear,
			uri:    itemURI,
			season: itemSeason,
			img:    itemImg,
		})
	}

	if len(items) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Auto-redirect if single result and not similar mode
	if !similar && len(items) == 1 {
		host := hostFromRequest(req)
		redirect := host + "/lite/animebesst?title=" + url.QueryEscape(title) +
			"&uri=" + url.QueryEscape(items[0].uri) + "&s=" + items[0].season
		if rjson {
			redirect += "&rjson=true"
		}
		http.Redirect(w, req, redirect, http.StatusFound)
		return
	}

	// Similar list
	host := hostFromRequest(req)
	data := make([]map[string]any, 0, len(items))
	labels := make([]string, 0, len(items))
	for _, item := range items {
		link := host + "/lite/animebesst?title=" + url.QueryEscape(title) +
			"&uri=" + url.QueryEscape(item.uri) + "&s=" + item.season
		if rjson {
			link += "&rjson=true"
		}

		data = append(data, map[string]any{
			"method":  "link",
			"url":     link,
			"similar": true,
			"year":    item.year,
			"title":   item.title,
			"img":     item.img,
		})
		labels = append(labels, item.title)
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

// ---------------------------------------------------------------------------
// Episodes
// ---------------------------------------------------------------------------

func (a *animebesstChecker) episodes(w http.ResponseWriter, req *http.Request, rjson bool, title, uri string, season int, links *proxylink.Manager) {
	body, ok := a.fetchPage(req, uri)
	if !ok {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Parse videoList
	videoListIdx := strings.Index(body, "var videoList")
	if videoListIdx < 0 {
		videoListIdx = strings.Index(body, "var videoList ")
	}
	if videoListIdx < 0 {
		log.Debug().Msg("animebesst: no videoList found")
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Get the line containing videoList
	rest := body[videoListIdx:]
	nlIdx := strings.IndexAny(rest, "\r\n")
	if nlIdx > 0 {
		rest = rest[:nlIdx]
	}

	type episode struct {
		num  string
		name string
		uri  string
	}

	var episodes []episode
	for _, m := range animebesstVideoRe.FindAllStringSubmatch(rest, -1) {
		if len(m) < 5 {
			continue
		}
		epNum := strings.TrimSpace(m[1])
		epName := strings.TrimSpace(m[2])
		epURI := strings.ReplaceAll(m[4], `\`, "")
		if epNum != "" && epURI != "" {
			episodes = append(episodes, episode{num: epNum, name: epName, uri: epURI})
		}
	}

	if len(episodes) == 0 {
		log.Debug().Msg("animebesst: no episodes found in videoList")
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(req)
	data := make([]map[string]any, 0, len(episodes))
	for _, ep := range episodes {
		name := ep.num + " серия"
		if ep.name != "" {
			name = ep.num + " " + ep.name
		}
		voiceName := ""
		if ep.name != "" {
			voiceName = strings.Trim(ep.name, "()")
		}

		link := host + "/lite/animebesst/video.m3u8?uri=" + url.QueryEscape(ep.uri) + "&title=" + url.QueryEscape(title)
		epN, _ := strconv.Atoi(ep.num)

		row := map[string]any{
			"method": "call",
			"url":    link,
			"stream": link + "&play=true",
			"s":      season,
			"e":      epN,
			"name":   name,
			"title":  title + " / " + name,
		}
		if voiceName != "" {
			row["voice_name"] = voiceName
		}
		data = append(data, row)
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
		name := row["name"].(string)
		s, _ := row["s"].(int)
		e, _ := row["e"].(int)
		getsTVAppendMovieHTML(&sb, row, name, i == 0, s, e)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// ---------------------------------------------------------------------------
// Video handler
// ---------------------------------------------------------------------------

func AnimebesstVideoHandler(a *animebesstChecker, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		uri := strings.TrimSpace(q.Get("uri"))
		title := strings.TrimSpace(q.Get("title"))
		play := parseBoolParam(q.Get("play"))

		if uri == "" {
			writeGetsTVEmpty(w, false)
			return
		}

		// Fetch iframe page
		target := "https://" + uri
		httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, target, nil)
		if err != nil {
			writeGetsTVEmpty(w, false)
			return
		}
		httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
		httpReq.Header.Set("Referer", a.host+"/")

		resp, err := a.client.Do(httpReq)
		if err != nil {
			log.Debug().Err(err).Msg("animebesst: video iframe fetch failed")
			writeGetsTVEmpty(w, false)
			return
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
			writeGetsTVEmpty(w, false)
			return
		}

		// Extract HLS URL
		m := animebesstHLSRe.FindStringSubmatch(string(body))
		if len(m) < 2 || m[1] == "" {
			log.Debug().Msg("animebesst: no HLS found in iframe")
			writeGetsTVEmpty(w, false)
			return
		}

		hlsURL := streamProxyURL(req, m[1], "animebesst", links)

		if play {
			http.Redirect(w, req, hlsURL, http.StatusFound)
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"method": "play",
			"url":    hlsURL,
			"title":  title,
		})
	}
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

func (a *animebesstChecker) fetchSearch(req *http.Request, title string) (string, bool) {
	u, err := url.Parse(a.host + "/index.php")
	if err != nil {
		return "", false
	}
	qs := url.Values{}
	qs.Set("do", "search")
	u.RawQuery = qs.Encode()

	form := url.Values{}
	form.Set("do", "search")
	form.Set("subaction", "search")
	form.Set("search_start", "0")
	form.Set("full_search", "0")
	form.Set("result_from", "1")
	form.Set("story", title)

	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodPost, u.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return "", false
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	httpReq.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", false
	}
	return string(body), true
}

func (a *animebesstChecker) fetchPage(req *http.Request, uri string) (string, bool) {
	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, uri, nil)
	if err != nil {
		return "", false
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	httpReq.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", false
	}
	return string(body), true
}
