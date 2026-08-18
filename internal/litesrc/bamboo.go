package litesrc

import (
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

// bambooChecker implements the BambooUA balancer — Ukrainian balancer
// that parses bambooua.com HTML pages for video streams.
type bambooChecker struct {
	client *http.Client
	host   string
}

var (
	bambooSearchItemRe = regexp.MustCompile(`(?is)<li[^>]*class="[^"]*slide-item[^"]*"[^>]*>(.*?)</li>`)
	bambooItemTitleRe  = regexp.MustCompile(`(?is)<h6[^>]*>(.*?)</h6>`)
	bambooItemHrefRe   = regexp.MustCompile(`(?is)<a[^>]*href="([^"]+)"`)
	bambooItemPosterRe = regexp.MustCompile(`(?is)<img[^>]*(?:src|data-src)="([^"]+)"`)
	// bambooBlockRe/bambooBlockHeaderRe removed — using data-type attribute instead
	// Captures: (1) full opening tag attributes, (2) data-file URL, (3) inner content
	bambooDataFileSpanRe  = regexp.MustCompile(`(?is)<span([^>]*data-file\s*=\s*"([^"]+)"[^>]*)>(.*?)</span>`)
	bambooDataTitleAttrRe = regexp.MustCompile(`(?i)data-title\s*=\s*"([^"]*)"`)
	bambooDataTypeAttrRe  = regexp.MustCompile(`(?i)data-type\s*=\s*"([^"]*)"`)
	bambooMovieSpanRe     = regexp.MustCompile(`(?is)<span([^>]*data-file\s*=\s*"([^"]+)"[^>]*)>(.*?)</span>`)
	bambooEpNumRe         = regexp.MustCompile(`(\d+)`)
	bambooHTMLTagRe       = regexp.MustCompile(`<[^>]+>`)
)

func NewBambooChecker(cfg config.Config) *bambooChecker {
	host := strings.TrimSpace(cfg.Online.Bamboo.Host)
	if host == "" {
		host = "https://bambooua.com"
	}
	host = strings.TrimRight(host, "/")

	return &bambooChecker{
		client: httpclient.NewForBalancer("bamboo", 12*time.Second),
		host:   host,
	}
}

func (c *bambooChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show := c.checkSearch(req)
			writeCheckSearchResponse(w, show, pluginQualityBadgeGet("bamboo"))
			return
		}
		c.index(w, req, links)
	}
}

func (c *bambooChecker) index(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	serial, _ := getsTVQueryInt(q.Get("serial"))
	href := strings.TrimSpace(q.Get("href"))
	t := strings.TrimSpace(q.Get("t"))
	host := hostFromRequest(req)

	// If no href, search for it
	if href == "" {
		results := c.search(req, title, originalTitle)
		if len(results) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}

		// Multiple results → similar template
		if len(results) > 1 {
			data := make([]map[string]any, 0, len(results))
			for _, res := range results {
				link := fmt.Sprintf("%s/lite/bamboo?title=%s&original_title=%s&year=%s&serial=%s&href=%s",
					host,
					url.QueryEscape(title),
					url.QueryEscape(originalTitle),
					q.Get("year"),
					q.Get("serial"),
					url.QueryEscape(res.url),
				)
				data = append(data, map[string]any{
					"title":  res.title,
					"url":    link,
					"poster": res.poster,
				})
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
			for _, item := range data {
				sb.WriteString(fmt.Sprintf(`<a class="videos__button selector" href="%s">%s</a> `,
					item["url"], item["title"]))
			}
			sb.WriteString(`</div>`)
			writeHTML(w, http.StatusOK, sb.String())
			return
		}

		href = results[0].url
	}

	if serial == 1 {
		c.writeSerial(w, req, rjson, title, originalTitle, href, t, host, links)
	} else {
		c.writeMovie(w, req, rjson, title, originalTitle, href, links)
	}
}

type bambooSearchResult struct {
	title  string
	url    string
	poster string
}

func (c *bambooChecker) search(req *http.Request, title, originalTitle string) []bambooSearchResult {
	// Try original_title first (often matches English/Korean names on bamboo),
	// then fall back to title (Russian/Ukrainian names from TMDB).
	queries := make([]string, 0, 2)
	if originalTitle != "" {
		queries = append(queries, originalTitle)
	}
	if title != "" && title != originalTitle {
		queries = append(queries, title)
	}
	if len(queries) == 0 {
		return nil
	}

	for _, query := range queries {
		results := c.searchOne(req, query)
		if len(results) > 0 {
			return results
		}
	}
	return nil
}

func (c *bambooChecker) searchOne(req *http.Request, query string) []bambooSearchResult {
	searchURL := fmt.Sprintf("%s/index.php?do=search&subaction=search&story=%s", c.host, url.QueryEscape(query))
	body, ok := c.fetch(req, http.MethodGet, searchURL, "", c.host)
	if !ok {
		log.Debug().Str("url", searchURL).Msg("bamboo: search fetch failed")
		return nil
	}

	html := string(body)
	items := bambooSearchItemRe.FindAllStringSubmatch(html, -1)
	if len(items) == 0 {
		return nil
	}

	var results []bambooSearchResult
	for _, m := range items {
		block := m[1]

		// Extract title
		titleMatch := bambooItemTitleRe.FindStringSubmatch(block)
		if len(titleMatch) < 2 {
			continue
		}
		itemTitle := bambooCleanText(titleMatch[1])
		if itemTitle == "" {
			continue
		}

		// Extract href
		hrefMatch := bambooItemHrefRe.FindStringSubmatch(block)
		if len(hrefMatch) < 2 {
			continue
		}
		itemHref := c.normalizeURL(strings.TrimSpace(hrefMatch[1]))
		if itemHref == "" {
			continue
		}

		// Extract poster
		poster := ""
		posterMatch := bambooItemPosterRe.FindStringSubmatch(block)
		if len(posterMatch) >= 2 {
			poster = c.normalizeURL(strings.TrimSpace(posterMatch[1]))
		}

		results = append(results, bambooSearchResult{
			title:  itemTitle,
			url:    itemHref,
			poster: poster,
		})
	}

	return results
}

type bambooEpisode struct {
	title   string
	url     string
	episode int
}

type bambooSeriesData struct {
	sub []bambooEpisode
	dub []bambooEpisode
}

func (c *bambooChecker) getSeriesEpisodes(req *http.Request, href string) *bambooSeriesData {
	body, ok := c.fetch(req, http.MethodGet, href, "", c.host)
	if !ok {
		log.Debug().Str("href", href).Msg("bamboo: getSeriesEpisodes fetch failed")
		return nil
	}

	html := string(body)
	// Parse all data-file spans and group by data-type attribute.
	spans := bambooDataFileSpanRe.FindAllStringSubmatch(html, -1)
	if len(spans) == 0 {
		return nil
	}

	result := &bambooSeriesData{}

	for _, sm := range spans {
		attrs := sm[1]
		dataFile := strings.TrimSpace(sm[2])
		if dataFile == "" {
			continue
		}

		// Extract data-title
		title := ""
		if tm := bambooDataTitleAttrRe.FindStringSubmatch(attrs); len(tm) >= 2 {
			title = strings.TrimSpace(tm[1])
		}
		if title == "" {
			title = bambooCleanText(sm[3]) // inner text
		}
		if title == "" {
			title = "Episode"
		}

		epNum := 0
		if nm := bambooEpNumRe.FindStringSubmatch(title); len(nm) >= 2 {
			if n, err := strconv.Atoi(nm[1]); err == nil {
				epNum = n
			}
		}

		ep := bambooEpisode{
			title:   title,
			url:     c.normalizeURL(dataFile),
			episode: epNum,
		}

		// Group by data-type: "sub" → subtitles, "dub" → dubbing, anything else → dub fallback
		dataType := ""
		if dtm := bambooDataTypeAttrRe.FindStringSubmatch(attrs); len(dtm) >= 2 {
			dataType = strings.TrimSpace(strings.ToLower(dtm[1]))
		}

		switch dataType {
		case "sub":
			result.sub = append(result.sub, ep)
		case "dub":
			result.dub = append(result.dub, ep)
		default:
			// No data-type attribute — put in dub as fallback
			result.dub = append(result.dub, ep)
		}
	}

	if len(result.sub) == 0 && len(result.dub) == 0 {
		return nil
	}

	return result
}

func bambooParseEpisodeSpans(html string, c *bambooChecker) []bambooEpisode {
	spans := bambooDataFileSpanRe.FindAllStringSubmatch(html, -1)
	if len(spans) == 0 {
		return nil
	}

	var episodes []bambooEpisode
	for _, sm := range spans {
		// Groups: (1) attrs, (2) data-file, (3) inner text
		attrs := sm[1]
		dataFile := strings.TrimSpace(sm[2])
		if dataFile == "" {
			continue
		}

		// Extract data-title from attributes
		title := ""
		if tm := bambooDataTitleAttrRe.FindStringSubmatch(attrs); len(tm) >= 2 {
			title = strings.TrimSpace(tm[1])
		}
		if title == "" {
			title = bambooCleanText(sm[3]) // inner text
		}
		if title == "" {
			title = "Episode"
		}

		epNum := 0
		if nm := bambooEpNumRe.FindStringSubmatch(title); len(nm) >= 2 {
			if n, err := strconv.Atoi(nm[1]); err == nil {
				epNum = n
			}
		}

		episodes = append(episodes, bambooEpisode{
			title:   title,
			url:     c.normalizeURL(dataFile),
			episode: epNum,
		})
	}

	return episodes
}

type bambooStreamInfo struct {
	title string
	url   string
}

func (c *bambooChecker) getMovieStreams(req *http.Request, href string) []bambooStreamInfo {
	body, ok := c.fetch(req, http.MethodGet, href, "", c.host)
	if !ok {
		return nil
	}

	html := string(body)
	spans := bambooMovieSpanRe.FindAllStringSubmatch(html, -1)

	// Fallback: try all data-file spans
	if len(spans) == 0 {
		spans = bambooDataFileSpanRe.FindAllStringSubmatch(html, -1)
	}

	if len(spans) == 0 {
		return nil
	}

	var streams []bambooStreamInfo
	for _, sm := range spans {
		// Groups: (1) attrs, (2) data-file, (3) inner text
		attrs := sm[1]
		dataFile := strings.TrimSpace(sm[2])
		if dataFile == "" {
			continue
		}

		// Extract data-title from attributes
		title := ""
		if tm := bambooDataTitleAttrRe.FindStringSubmatch(attrs); len(tm) >= 2 {
			title = strings.TrimSpace(tm[1])
		}
		if title == "" {
			title = bambooCleanText(sm[3]) // inner text
		}

		streams = append(streams, bambooStreamInfo{
			title: title,
			url:   c.normalizeURL(dataFile),
		})
	}

	return streams
}

func (c *bambooChecker) writeSerial(w http.ResponseWriter, req *http.Request, rjson bool, title, originalTitle, href, t, host string, links *proxylink.Manager) {
	series := c.getSeriesEpisodes(req, href)
	if series == nil || (len(series.sub) == 0 && len(series.dub) == 0) {
		writeGetsTVEmpty(w, rjson)
		return
	}

	type voiceEntry struct {
		key      string
		name     string
		episodes []bambooEpisode
	}

	var voices []voiceEntry
	if len(series.sub) > 0 {
		voices = append(voices, voiceEntry{"sub", "Субтитри", series.sub})
	}
	if len(series.dub) > 0 {
		voices = append(voices, voiceEntry{"dub", "Озвучення", series.dub})
	}
	if len(voices) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if t == "" {
		t = voices[0].key
	}

	// Build voice selector
	voiceData := make([]map[string]any, 0, len(voices))
	for _, v := range voices {
		voiceLink := fmt.Sprintf("%s/lite/bamboo?title=%s&original_title=%s&serial=1&t=%s&href=%s",
			host,
			url.QueryEscape(title),
			url.QueryEscape(originalTitle),
			v.key,
			url.QueryEscape(href),
		)
		voiceData = append(voiceData, map[string]any{
			"name":   v.name,
			"active": v.key == t,
			"url":    voiceLink,
		})
	}

	// Find selected voice episodes
	var selected []bambooEpisode
	for _, v := range voices {
		if v.key == t {
			selected = v.episodes
			break
		}
	}
	if len(selected) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Sort episodes
	sort.Slice(selected, func(i, j int) bool {
		ei := selected[i].episode
		ej := selected[j].episode
		if ei == 0 {
			ei = 999999
		}
		if ej == 0 {
			ej = 999999
		}
		return ei < ej
	})

	baseTitle := getsTVJoinName(title, originalTitle)
	data := make([]map[string]any, 0, len(selected))
	for idx, ep := range selected {
		epNum := ep.episode
		if epNum == 0 {
			epNum = idx + 1
		}
		epName := ep.title
		if epName == "" || epName == "Episode" {
			epName = fmt.Sprintf("Епізод %d", epNum)
		}

		streamURL := bambooStripLampacArgs(ep.url)
		streamURL = streamProxyURL(req, streamURL, "bamboo", links)

		data = append(data, map[string]any{
			"method": "play",
			"url":    streamURL,
			"stream": streamURL,
			"s":      1,
			"e":      epNum,
			"name":   epName,
			"title":  fmt.Sprintf("%s (%s)", baseTitle, epName),
		})
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type":  "episode",
			"data":  data,
			"voice": voiceData,
		})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	// Voice buttons
	for _, v := range voiceData {
		activeClass := ""
		if v["active"].(bool) {
			activeClass = " active"
		}
		sb.WriteString(fmt.Sprintf(`<a class="videos__button selector%s" href="%s">%s</a> `,
			activeClass, v["url"], v["name"]))
	}
	sb.WriteString(`</div><div class="videos__line">`)
	// Episodes
	for _, row := range data {
		sb.WriteString(fmt.Sprintf(`<div class="videos__item videos__movie selector" data-json='{"method":"play","url":"%s","stream":"%s","e":%v,"s":1,"title":"%s"}'>`,
			row["url"], row["stream"], row["e"], strings.ReplaceAll(fmt.Sprint(row["title"]), `"`, `\"`)))
		sb.WriteString(fmt.Sprintf(`<div class="videos__item-imgbox videos__movie-imgbox"></div>`))
		sb.WriteString(fmt.Sprintf(`<div class="videos__item-title">%s</div></div> `, row["name"]))
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (c *bambooChecker) writeMovie(w http.ResponseWriter, req *http.Request, rjson bool, title, originalTitle, href string, links *proxylink.Manager) {
	streams := c.getMovieStreams(req, href)
	if len(streams) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	movieTitle := getsTVJoinName(title, originalTitle)
	data := make([]map[string]any, 0, len(streams))
	for i, s := range streams {
		label := s.title
		if label == "" {
			label = fmt.Sprintf("Варіант %d", i+1)
		}

		streamURL := bambooStripLampacArgs(s.url)
		streamURL = streamProxyURL(req, streamURL, "bamboo", links)

		data = append(data, map[string]any{
			"method": "play",
			"url":    streamURL,
			"stream": streamURL,
			"name":   label,
			"title":  movieTitle,
		})
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "movie",
			"data": data,
		})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range data {
		getsTVAppendMovieHTML(&sb, row, row["name"].(string), i == 0, 0, 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (c *bambooChecker) checkSearch(req *http.Request) bool {
	q := req.URL.Query()
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	results := c.search(req, title, originalTitle)
	return len(results) > 0
}

func (c *bambooChecker) fetch(req *http.Request, method, target, body, referer string) ([]byte, bool) {
	var requestBody io.Reader
	if body != "" {
		requestBody = strings.NewReader(body)
	}

	httpReq, err := http.NewRequestWithContext(req.Context(), method, target, requestBody)
	if err != nil {
		return nil, false
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	httpReq.Header.Set("X-Lampac-Go", "1")
	if referer != "" {
		httpReq.Header.Set("Referer", referer)
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, false
	}
	return data, true
}

func (c *bambooChecker) normalizeURL(u string) string {
	if u == "" {
		return ""
	}
	if strings.HasPrefix(u, "//") {
		return "https:" + u
	}
	if strings.HasPrefix(u, "/") {
		return c.host + u
	}
	return u
}

func bambooCleanText(s string) string {
	s = bambooHTMLTagRe.ReplaceAllString(s, "")
	s = strings.TrimSpace(s)
	return s
}

// bambooLampacArgsRe strips lampac-internal query params injected by the
// proxy (account_email/uid/nws_id) before forwarding URLs upstream. Compiled
// once; was previously re-compiled on every request.
var bambooLampacArgsRe = regexp.MustCompile(`([?&])(account_email|uid|nws_id)=[^&]*`)

func bambooStripLampacArgs(rawURL string) string {
	if rawURL == "" {
		return rawURL
	}
	cleaned := bambooLampacArgsRe.ReplaceAllString(rawURL, "$1")
	cleaned = strings.ReplaceAll(cleaned, "?&", "?")
	cleaned = strings.ReplaceAll(cleaned, "&&", "&")
	cleaned = strings.TrimRight(cleaned, "?&")
	return cleaned
}
