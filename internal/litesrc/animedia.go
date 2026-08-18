package litesrc

import (
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

var (
	animediaItemRe   = regexp.MustCompile(`<a href="https?://[^/]+/([^"]+)" class="poster__link"><h3 class="poster__title line-clamp">([^<]+)</h3></a>`)
	animediaImgRe    = regexp.MustCompile(`<img src="([^"]+)"`)
	animediaTitleRe  = regexp.MustCompile(`(?is)class="poster__title line-clamp">([^<]+)<`)
	animediaEpRe     = regexp.MustCompile(`data-vid="([0-9]+)"[\t ]+data-vlnk="([^"]+)"`)
	animediaSeasonRe = regexp.MustCompile(`(?i)Season[\t ]+([0-9]+)`)
	animediaHLSRe    = regexp.MustCompile(`(?i)file:\s*"(https?://[^"]+)"`)
)

type animediaChecker struct {
	client *http.Client
	host   string
}

func NewAnimediaChecker(cfg config.Config) *animediaChecker {
	host := strings.TrimSpace(cfg.Online.Animedia.Host)
	if host == "" {
		host = "https://amd.online"
	}
	host = strings.TrimRight(host, "/")

	return &animediaChecker{
		client: httpclient.NewForBalancer("animedia", 10*time.Second),
		host:   host,
	}
}

func (a *animediaChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show := a.checkSearch(req)
			writeCheckSearchResponseNoRCH(w, show, pluginQualityBadgeGet("animedia"))
			return
		}

		a.index(w, req, links)
	}
}

func (a *animediaChecker) checkSearch(req *http.Request) bool {
	title := strings.TrimSpace(req.URL.Query().Get("title"))
	if title == "" {
		return false
	}

	search, ok := a.fetchSearch(req, title)
	if !ok {
		return false
	}

	want := normalizeSearchTitle(title)
	for _, m := range animediaTitleRe.FindAllStringSubmatch(search, -1) {
		if len(m) < 2 {
			continue
		}
		rowTitle := normalizeSearchTitle(m[1])
		if rowTitle != "" && strings.Contains(rowTitle, want) {
			return true
		}
	}

	return false
}

// ---------------------------------------------------------------------------
// Index: search or episodes
// ---------------------------------------------------------------------------

func (a *animediaChecker) index(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	similar := parseBoolParam(q.Get("similar"))
	title := strings.TrimSpace(q.Get("title"))
	news := strings.TrimSpace(q.Get("news"))

	if news != "" {
		// Episodes mode
		a.episodes(w, req, rjson, title, news, links)
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

	// Check if search was successful (DLE search page marker)
	if !strings.Contains(html, `id="dosearch"`) {
		log.Debug().Msg("animedia: search page marker not found")
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Split by </article> to get main content area
	articleParts := strings.SplitN(html, "</article>", 2)
	if len(articleParts) < 2 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	mainContent := articleParts[1]

	// Split by grid-item to get individual items
	rows := strings.Split(mainContent, "grid-item d-flex fd-column")
	if len(rows) <= 1 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	type searchItem struct {
		title string
		uri   string // relative path like "anime-russkie-subtitry/1234-name.html"
		img   string
	}

	want := normalizeSearchTitle(title)
	var items []searchItem
	for _, row := range rows[1:] {
		m := animediaItemRe.FindStringSubmatch(row)
		if len(m) < 3 || strings.TrimSpace(m[1]) == "" || strings.TrimSpace(m[2]) == "" {
			continue
		}

		itemPath := strings.TrimSpace(m[1])
		itemTitle := strings.TrimSpace(m[2])

		// Filter by title match unless similar mode
		if !similar {
			rowNorm := normalizeSearchTitle(itemTitle)
			if rowNorm == "" || !strings.Contains(rowNorm, want) {
				continue
			}
		}

		itemImg := ""
		imgM := animediaImgRe.FindStringSubmatch(row)
		if len(imgM) >= 2 {
			img := strings.TrimSpace(imgM[1])
			if img != "" && !strings.HasPrefix(img, "http") {
				img = a.host + img
			}
			itemImg = img
		}

		items = append(items, searchItem{
			title: itemTitle,
			uri:   itemPath,
			img:   itemImg,
		})
	}

	if len(items) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Auto-redirect if single result and not similar mode
	if !similar && len(items) == 1 {
		host := hostFromRequest(req)
		redirect := host + "/lite/animedia?title=" + url.QueryEscape(title) +
			"&news=" + url.QueryEscape(items[0].uri)
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
		link := host + "/lite/animedia?title=" + url.QueryEscape(title) +
			"&news=" + url.QueryEscape(item.uri)
		if rjson {
			link += "&rjson=true"
		}

		data = append(data, map[string]any{
			"method":  "link",
			"url":     link,
			"similar": true,
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

func (a *animediaChecker) episodes(w http.ResponseWriter, req *http.Request, rjson bool, title, news string, links *proxylink.Manager) {
	pageURL := a.host + "/" + strings.TrimLeft(news, "/")
	body, ok := a.fetchPage(req, pageURL)
	if !ok {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Parse data-vid / data-vlnk
	matches := animediaEpRe.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		log.Debug().Msg("animedia: no data-vid/data-vlnk found")
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Extract season from page
	season := 1
	mainInfoIdx := strings.Index(body, `class="pmovie__main-info ws-nowrap">`)
	if mainInfoIdx >= 0 {
		chunk := body[mainInfoIdx:min(mainInfoIdx+200, len(body))]
		sm := animediaSeasonRe.FindStringSubmatch(chunk)
		if len(sm) >= 2 {
			if s, err := strconv.Atoi(sm[1]); err == nil && s > 0 {
				season = s
			}
		}
	}

	type episode struct {
		num int
		vod string
	}

	seen := make(map[int]bool)
	var episodes []episode
	for _, m := range matches {
		if len(m) < 3 {
			continue
		}
		epNum, err := strconv.Atoi(strings.TrimSpace(m[1]))
		if err != nil || epNum <= 0 {
			continue
		}
		vod := strings.TrimSpace(m[2])
		if vod == "" || !strings.Contains(vod, "/vod/") {
			continue
		}
		if seen[epNum] {
			continue
		}
		seen[epNum] = true
		episodes = append(episodes, episode{num: epNum, vod: vod})
	}

	if len(episodes) == 0 {
		log.Debug().Msg("animedia: no valid episodes with /vod/")
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Sort by episode number
	sort.Slice(episodes, func(i, j int) bool {
		return episodes[i].num < episodes[j].num
	})

	host := hostFromRequest(req)
	data := make([]map[string]any, 0, len(episodes))
	for _, ep := range episodes {
		name := strconv.Itoa(ep.num) + " серия"
		link := host + "/lite/animedia/video.m3u8?vod=" + url.QueryEscape(ep.vod) + "&title=" + url.QueryEscape(title)

		row := map[string]any{
			"method": "call",
			"url":    link,
			"stream": link + "&play=true",
			"s":      season,
			"e":      ep.num,
			"name":   name,
			"title":  title + " / " + name,
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

func AnimediaVideoHandler(a *animediaChecker, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		vod := strings.TrimSpace(q.Get("vod"))
		title := strings.TrimSpace(q.Get("title"))
		play := parseBoolParam(q.Get("play"))

		if vod == "" {
			writeGetsTVEmpty(w, false)
			return
		}

		// Ensure full URL
		target := vod
		if !strings.HasPrefix(target, "http") {
			target = "https://" + target
		}

		httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, target, nil)
		if err != nil {
			writeGetsTVEmpty(w, false)
			return
		}
		httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
		httpReq.Header.Set("Referer", a.host+"/")

		resp, err := a.client.Do(httpReq)
		if err != nil {
			log.Debug().Err(err).Msg("animedia: video fetch failed")
			writeGetsTVEmpty(w, false)
			return
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
			writeGetsTVEmpty(w, false)
			return
		}

		// Extract HLS/stream URL
		m := animediaHLSRe.FindStringSubmatch(string(body))
		if len(m) < 2 || m[1] == "" {
			log.Debug().Msg("animedia: no file URL found in vod embed")
			writeGetsTVEmpty(w, false)
			return
		}

		hlsURL := streamProxyURL(req, m[1], "animedia", links)

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

func (a *animediaChecker) fetchSearch(req *http.Request, title string) (string, bool) {
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
	form.Set("from_page", "0")
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

func (a *animediaChecker) fetchPage(req *http.Request, uri string) (string, bool) {
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
