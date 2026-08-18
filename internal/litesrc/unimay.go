package litesrc

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/proxylink"
)

// unimayChecker implements the Unimay Ukrainian balancer.
// Uses REST API at api.unimay.media/v1 for search and release info.
type unimayChecker struct {
	client *http.Client
	host   string
}

func NewUnimayChecker(cfg config.Config) *unimayChecker {
	host := strings.TrimSpace(cfg.Online.Unimay.Host)
	if host == "" {
		host = "https://api.unimay.media/v1"
	}
	host = strings.TrimRight(host, "/")

	return &unimayChecker{
		client: httpclient.NewForBalancer("unimay", 10*time.Second),
		host:   host,
	}
}

func (c *unimayChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show := c.checkSearch(req)
			writeCheckSearchResponse(w, show, pluginQualityBadgeGet("unimay"))
			return
		}
		c.index(w, req, links)
	}
}

// --- JSON models ---

type unimaySearchResponse struct {
	Content []unimayReleaseInfo `json:"content"`
}

type unimayReleaseInfo struct {
	Code  string      `json:"code"`
	Title string      `json:"title"`
	Year  int         `json:"year"`
	Type  string      `json:"type"` // "Фільм" або "Телесеріал"
	Names unimayNames `json:"names"`
}

type unimayNames struct {
	Ukr string `json:"ukr"`
	Eng string `json:"eng"`
}

type unimayReleaseResponse struct {
	Code     string          `json:"code"`
	Title    string          `json:"title"`
	Year     int             `json:"year"`
	Type     string          `json:"type"`
	Playlist []unimayEpisode `json:"playlist"`
}

type unimayEpisode struct {
	Number int       `json:"number"`
	Title  string    `json:"title"`
	Hls    unimayHls `json:"hls"`
}

type unimayHls struct {
	Master string `json:"master"`
}

func (c *unimayChecker) index(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	code := strings.TrimSpace(q.Get("code"))
	serial, _ := getsTVQueryInt(q.Get("serial"))
	seasonNum, _ := getsTVQueryInt(q.Get("s"))
	epNum, _ := getsTVQueryInt(q.Get("e"))
	play := parseBoolParam(q.Get("play"))
	host := hostFromRequest(req)

	if code != "" {
		c.release(w, req, rjson, host, code, title, originalTitle, serial, seasonNum, epNum, play, links)
	} else {
		c.searchAndShow(w, req, rjson, host, title, originalTitle, serial)
	}
}

func (c *unimayChecker) searchAndShow(w http.ResponseWriter, req *http.Request, rjson bool, host, title, originalTitle string, serial int) {
	searchResp := c.searchAPI(req, title, originalTitle, serial)
	if searchResp == nil || len(searchResp.Content) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	data := make([]map[string]any, 0, len(searchResp.Content))
	for _, item := range searchResp.Content {
		// Filter by type if serial specified
		if serial != -1 {
			isMovie := item.Type == "Фільм"
			if (serial == 0 && !isMovie) || (serial == 1 && isMovie) {
				continue
			}
		}

		itemTitle := item.Names.Ukr
		if itemTitle == "" {
			itemTitle = item.Names.Eng
		}
		if itemTitle == "" {
			itemTitle = item.Title
		}

		releaseURL := fmt.Sprintf("%s/lite/unimay?code=%s&title=%s&original_title=%s&serial=%d",
			host,
			url.QueryEscape(item.Code),
			url.QueryEscape(itemTitle),
			url.QueryEscape(originalTitle),
			serial,
		)

		data = append(data, map[string]any{
			"title": itemTitle,
			"year":  item.Year,
			"type":  item.Type,
			"url":   releaseURL,
		})
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
	for _, item := range data {
		sb.WriteString(fmt.Sprintf(`<a class="videos__button selector" href="%s">%s (%v)</a> `,
			item["url"], item["title"], item["year"]))
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (c *unimayChecker) release(w http.ResponseWriter, req *http.Request, rjson bool, host, code, title, originalTitle string, serial, seasonNum, epNum int, play bool, links *proxylink.Manager) {
	releaseResp := c.releaseAPI(req, code)
	if releaseResp == nil || len(releaseResp.Playlist) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Play mode — redirect to stream
	if play {
		var ep *unimayEpisode
		if releaseResp.Type == "Телесеріал" {
			if seasonNum <= 0 || epNum <= 0 {
				writeGetsTVEmpty(w, rjson)
				return
			}
			for i := range releaseResp.Playlist {
				if releaseResp.Playlist[i].Number == epNum {
					ep = &releaseResp.Playlist[i]
					break
				}
			}
		} else {
			ep = &releaseResp.Playlist[0]
		}

		if ep == nil || ep.Hls.Master == "" {
			writeGetsTVEmpty(w, rjson)
			return
		}

		streamURL := unimayStripLampacArgs(strings.TrimSpace(ep.Hls.Master))
		streamURL = streamProxyURL(req, streamURL, "unimay", links)
		http.Redirect(w, req, streamURL, http.StatusFound)
		return
	}

	// Movie
	if releaseResp.Type == "Фільм" {
		movieEp := releaseResp.Playlist[0]
		movieLink := fmt.Sprintf("%s/lite/unimay?code=%s&title=%s&original_title=%s&serial=0&play=true",
			host,
			url.QueryEscape(releaseResp.Code),
			url.QueryEscape(title),
			url.QueryEscape(originalTitle),
		)
		movieTitle := movieEp.Title
		if movieTitle == "" {
			movieTitle = title
		}

		data := []map[string]any{{
			"method": "play",
			"url":    movieLink,
			"name":   movieTitle,
			"title":  getsTVJoinName(title, originalTitle),
		}}

		if rjson {
			writeJSON(w, http.StatusOK, map[string]any{
				"type": "movie",
				"data": data,
			})
			return
		}
		var sb strings.Builder
		sb.WriteString(`<div class="videos__line">`)
		getsTVAppendMovieHTML(&sb, data[0], movieTitle, true, 0, 0)
		sb.WriteString(`</div>`)
		writeHTML(w, http.StatusOK, sb.String())
		return
	}

	// Serial
	if releaseResp.Type == "Телесеріал" {
		if seasonNum == -1 {
			// Show season list (single season assumed)
			seasonURL := fmt.Sprintf("%s/lite/unimay?code=%s&title=%s&original_title=%s&serial=1&s=1",
				host,
				url.QueryEscape(code),
				url.QueryEscape(title),
				url.QueryEscape(originalTitle),
			)
			data := []map[string]any{{
				"method": "link",
				"id":     1,
				"url":    seasonURL,
				"name":   "Сезон 1",
			}}

			if rjson {
				writeJSON(w, http.StatusOK, map[string]any{
					"type": "season",
					"data": data,
				})
				return
			}
			var sb strings.Builder
			sb.WriteString(`<div class="videos__line">`)
			getsTVAppendSeasonHTML(&sb, data[0], "Сезон 1", true)
			sb.WriteString(`</div>`)
			writeHTML(w, http.StatusOK, sb.String())
			return
		}

		// Show episode list
		episodes := make([]unimayEpisode, 0, len(releaseResp.Playlist))
		for _, ep := range releaseResp.Playlist {
			if ep.Number >= 1 && ep.Number <= 9999 {
				episodes = append(episodes, ep)
			}
		}
		sort.Slice(episodes, func(i, j int) bool {
			return episodes[i].Number < episodes[j].Number
		})

		baseTitle := getsTVJoinName(title, originalTitle)
		data := make([]map[string]any, 0, len(episodes))
		labels := make([]string, 0, len(episodes))
		seasons := make([]int, 0, len(episodes))
		epNums := make([]int, 0, len(episodes))

		for _, ep := range episodes {
			epTitle := ep.Title
			if epTitle == "" {
				epTitle = fmt.Sprintf("Епізод %d", ep.Number)
			}
			epLink := fmt.Sprintf("%s/lite/unimay?code=%s&title=%s&original_title=%s&serial=1&s=1&e=%d&play=true",
				host,
				url.QueryEscape(releaseResp.Code),
				url.QueryEscape(title),
				url.QueryEscape(originalTitle),
				ep.Number,
			)
			data = append(data, map[string]any{
				"method": "play",
				"url":    epLink,
				"name":   epTitle,
				"title":  fmt.Sprintf("%s (%s)", baseTitle, epTitle),
				"s":      1,
				"e":      ep.Number,
			})
			labels = append(labels, epTitle)
			seasons = append(seasons, 1)
			epNums = append(epNums, ep.Number)
		}

		if len(data) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
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
			getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, seasons[i], epNums[i])
		}
		sb.WriteString(`</div>`)
		writeHTML(w, http.StatusOK, sb.String())
		return
	}

	writeGetsTVEmpty(w, rjson)
}

func (c *unimayChecker) searchAPI(req *http.Request, title, originalTitle string, serial int) *unimaySearchResponse {
	// Build query list: try original_title (English) first since unimay is a Ukrainian
	// service that indexes English/Ukrainian titles but not Russian ones.
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
		searchURL := fmt.Sprintf("%s/release/search?page=0&page_size=10&title=%s",
			c.host, url.QueryEscape(query))

		body, ok := c.fetchJSON(req, searchURL)
		if !ok {
			continue
		}

		var resp unimaySearchResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			continue
		}
		if len(resp.Content) > 0 {
			return &resp
		}
	}
	return nil
}

func (c *unimayChecker) releaseAPI(req *http.Request, code string) *unimayReleaseResponse {
	releaseURL := fmt.Sprintf("%s/release?code=%s", c.host, url.QueryEscape(code))
	body, ok := c.fetchJSON(req, releaseURL)
	if !ok {
		return nil
	}

	var resp unimayReleaseResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil
	}
	return &resp
}

func (c *unimayChecker) checkSearch(req *http.Request) bool {
	q := req.URL.Query()
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	serial, _ := getsTVQueryInt(q.Get("serial"))
	resp := c.searchAPI(req, title, originalTitle, serial)
	return resp != nil && len(resp.Content) > 0
}

func (c *unimayChecker) fetchJSON(req *http.Request, target string) ([]byte, bool) {
	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, target, nil)
	if err != nil {
		return nil, false
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	httpReq.Header.Set("Referer", c.host)
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("X-Lampac-Go", "1")

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

func unimayStripLampacArgs(rawURL string) string {
	if rawURL == "" {
		return rawURL
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}

	q := u.Query()
	changed := false
	for _, key := range []string{"account_email", "uid", "nws_id"} {
		if q.Has(key) {
			q.Del(key)
			changed = true
		}
	}
	if !changed {
		return rawURL
	}
	u.RawQuery = q.Encode()
	return u.String()
}
