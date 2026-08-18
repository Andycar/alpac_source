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
)

// starlightChecker implements the StarLight Ukrainian balancer.
// Uses tp-back.starlight.digital API for search, project info, and
// vcms-api2.starlight.digital for video stream resolution.
type starlightChecker struct {
	client    *http.Client
	host      string
	playerAPI string
	referer   string
	lang      string
}

var starlightQualityRe = regexp.MustCompile(`(\d{3,4})p`)

func NewStarlightChecker(cfg config.Config) *starlightChecker {
	host := strings.TrimSpace(cfg.Online.StarLight.Host)
	if host == "" {
		host = "https://tp-back.starlight.digital"
	}
	host = strings.TrimRight(host, "/")

	return &starlightChecker{
		client:    httpclient.NewForBalancer("starlight", 12*time.Second),
		host:      host,
		playerAPI: "https://vcms-api2.starlight.digital/player-api",
		referer:   "https://teleportal.ua/",
		lang:      "ua",
	}
}

func (c *starlightChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		raw := strings.TrimPrefix(strings.ToLower(req.URL.Path), "/lite/")

		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show := c.checkSearch(req)
			writeCheckSearchResponse(w, show, pluginQualityBadgeGet("starlight"))
			return
		}

		if raw == "starlight/play" {
			c.play(w, req, links)
			return
		}

		c.index(w, req, links)
	}
}

// --- Models ---

type starlightSearchResult struct {
	title    string
	typeSlug string
	href     string
}

type starlightSeasonInfo struct {
	title string
	slug  string
}

type starlightEpisodeInfo struct {
	title      string
	hash       string
	seasonSlug string
	number     int
	date       string
}

type starlightProject struct {
	title    string
	hash     string
	typeSlug string
	seasons  []starlightSeasonInfo
	episodes []starlightEpisodeInfo
}

type starlightStreamResult struct {
	stream  string
	name    string
	streams []starlightQualityStream
}

type starlightQualityStream struct {
	link    string
	quality string
}

// --- Handlers ---

func (c *starlightChecker) index(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	serial, _ := getsTVQueryInt(q.Get("serial"))
	seasonIdx, seasonSet := getsTVQueryInt(q.Get("s"))
	if !seasonSet {
		seasonIdx = -1
	}
	href := strings.TrimSpace(q.Get("href"))
	host := hostFromRequest(req)

	// Search if no href
	if href == "" {
		results := c.search(req, title, originalTitle)
		if len(results) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}

		if len(results) > 1 {
			data := make([]map[string]any, 0, len(results))
			for _, res := range results {
				link := fmt.Sprintf("%s/lite/starlight?title=%s&original_title=%s&year=%s&serial=%s&href=%s",
					host, url.QueryEscape(title), url.QueryEscape(originalTitle),
					q.Get("year"), q.Get("serial"), url.QueryEscape(res.href))
				data = append(data, map[string]any{
					"title": res.title,
					"url":   link,
				})
			}
			if rjson {
				writeJSON(w, http.StatusOK, map[string]any{"type": "similar", "data": data})
			} else {
				var sb strings.Builder
				sb.WriteString(`<div class="videos__line">`)
				for _, item := range data {
					sb.WriteString(fmt.Sprintf(`<a class="videos__button selector" href="%s">%s</a> `, item["url"], item["title"]))
				}
				sb.WriteString(`</div>`)
				writeHTML(w, http.StatusOK, sb.String())
			}
			return
		}

		href = results[0].href
	}

	// Get project info
	project := c.getProject(req, href)
	if project == nil {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if serial == 1 && len(project.seasons) > 0 {
		if seasonIdx == -1 {
			// Show season list
			data := make([]map[string]any, 0, len(project.seasons))
			labels := make([]string, 0, len(project.seasons))
			for i, s := range project.seasons {
				name := s.title
				if name == "" {
					name = fmt.Sprintf("Сезон %d", i+1)
				}
				link := fmt.Sprintf("%s/lite/starlight?title=%s&original_title=%s&serial=1&s=%d&href=%s",
					host, url.QueryEscape(title), url.QueryEscape(originalTitle), i, url.QueryEscape(href))
				data = append(data, map[string]any{
					"method": "link",
					"id":     i,
					"url":    link,
					"name":   name,
				})
				labels = append(labels, name)
			}
			if rjson {
				writeJSON(w, http.StatusOK, map[string]any{"type": "season", "data": data})
			} else {
				var sb strings.Builder
				sb.WriteString(`<div class="videos__line">`)
				for i, row := range data {
					getsTVAppendSeasonHTML(&sb, row, labels[i], i == 0)
				}
				sb.WriteString(`</div>`)
				writeHTML(w, http.StatusOK, sb.String())
			}
			return
		}

		// Show episodes for season
		if seasonIdx < 0 || seasonIdx >= len(project.seasons) {
			writeGetsTVEmpty(w, rjson)
			return
		}

		season := project.seasons[seasonIdx]
		episodes := c.getEpisodes(project, season.slug)
		if len(episodes) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}

		// Sort episodes by number, then date
		sort.Slice(episodes, func(i, j int) bool {
			ni := episodes[i].number
			nj := episodes[j].number
			if ni == 0 {
				ni = 999999
			}
			if nj == 0 {
				nj = 999999
			}
			if ni != nj {
				return ni < nj
			}
			return episodes[i].date < episodes[j].date
		})

		// Extract season number from title
		seasonNum := starlightSeasonNumber(season, seasonIdx)
		baseTitle := getsTVJoinName(title, originalTitle)

		data := make([]map[string]any, 0, len(episodes))
		labels := make([]string, 0, len(episodes))
		seasons := make([]int, 0, len(episodes))
		epNums := make([]int, 0, len(episodes))
		idx := 1

		for _, ep := range episodes {
			if ep.hash == "" {
				continue
			}
			epName := ep.title
			if epName == "" {
				epName = fmt.Sprintf("Епізод %d", idx)
			}
			callURL := fmt.Sprintf("%s/lite/starlight/play?hash=%s&title=%s",
				host, url.QueryEscape(ep.hash), url.QueryEscape(title))

			sn, _ := strconv.Atoi(seasonNum)
			data = append(data, map[string]any{
				"method": "call",
				"url":    callURL,
				"name":   epName,
				"title":  fmt.Sprintf("%s (%s)", baseTitle, epName),
				"s":      sn,
				"e":      idx,
			})
			labels = append(labels, epName)
			seasons = append(seasons, sn)
			epNums = append(epNums, idx)
			idx++
		}

		if len(data) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}

		if rjson {
			writeJSON(w, http.StatusOK, map[string]any{"type": "episode", "data": data})
		} else {
			var sb strings.Builder
			sb.WriteString(`<div class="videos__line">`)
			for i, row := range data {
				getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, seasons[i], epNums[i])
			}
			sb.WriteString(`</div>`)
			writeHTML(w, http.StatusOK, sb.String())
		}
		return
	}

	// Movie
	hash := project.hash
	if hash == "" {
		for _, ep := range project.episodes {
			if ep.hash != "" {
				hash = ep.hash
				break
			}
		}
	}
	if hash == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	callURL := fmt.Sprintf("%s/lite/starlight/play?hash=%s&title=%s",
		host, url.QueryEscape(hash), url.QueryEscape(title))

	movieTitle := title
	if movieTitle == "" {
		movieTitle = "StarLight"
	}

	data := []map[string]any{{
		"method": "call",
		"url":    callURL,
		"name":   movieTitle,
		"title":  getsTVJoinName(title, originalTitle),
	}}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "movie", "data": data})
	} else {
		var sb strings.Builder
		sb.WriteString(`<div class="videos__line">`)
		getsTVAppendMovieHTML(&sb, data[0], movieTitle, true, 0, 0)
		sb.WriteString(`</div>`)
		writeHTML(w, http.StatusOK, sb.String())
	}
}

func (c *starlightChecker) play(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	hash := strings.TrimSpace(q.Get("hash"))
	title := strings.TrimSpace(q.Get("title"))

	if hash == "" {
		writeGetsTVEmpty(w, true)
		return
	}

	result := c.resolveStream(req, hash)
	if result == nil || result.stream == "" {
		writeGetsTVEmpty(w, true)
		return
	}

	videoTitle := title
	if videoTitle == "" {
		videoTitle = result.name
	}

	if len(result.streams) > 0 {
		qualMap := make(map[string]any, len(result.streams))
		var firstURL string
		for _, s := range result.streams {
			streamURL := streamProxyURL(req, s.link, "starlight", links)
			qualMap[s.quality] = streamURL
			if firstURL == "" {
				firstURL = streamURL
			}
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"method":   "play",
			"url":      firstURL,
			"title":    videoTitle,
			"quality":  qualMap,
			"qualitys": qualMap,
		})
		return
	}

	streamURL := streamProxyURL(req, result.stream, "starlight", links)
	writeJSON(w, http.StatusOK, map[string]any{
		"method": "play",
		"url":    streamURL,
		"title":  videoTitle,
	})
}

// --- API methods ---

func (c *starlightChecker) search(req *http.Request, title, originalTitle string) []starlightSearchResult {
	query := title
	if query == "" {
		query = originalTitle
	}
	if query == "" {
		return nil
	}

	searchURL := fmt.Sprintf("%s/%s/live-search?q=%s", c.host, c.lang, url.QueryEscape(query))
	body, ok := c.fetchAPI(req, searchURL, c.host)
	if !ok {
		return nil
	}

	var rawItems []map[string]any
	if err := json.Unmarshal(body, &rawItems); err != nil {
		return nil
	}

	var results []starlightSearchResult
	for _, item := range rawItems {
		typeSlug, _ := item["typeSlug"].(string)
		channelSlug, _ := item["channelSlug"].(string)
		projectSlug, _ := item["projectSlug"].(string)
		if typeSlug == "" || channelSlug == "" || projectSlug == "" {
			continue
		}

		itemTitle, _ := item["title"].(string)
		href := fmt.Sprintf("%s/%s/%s/%s/%s", c.host, c.lang, typeSlug, channelSlug, projectSlug)

		results = append(results, starlightSearchResult{
			title:    itemTitle,
			typeSlug: typeSlug,
			href:     href,
		})
	}

	return results
}

func (c *starlightChecker) getProject(req *http.Request, href string) *starlightProject {
	body, ok := c.fetchAPI(req, href, c.host)
	if !ok {
		return nil
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil
	}

	project := &starlightProject{
		title:    strVal(raw, "title"),
		hash:     strVal(raw, "hash"),
		typeSlug: strVal(raw, "typeSlug"),
	}

	// Parse seasons from "seasons" array
	if seasonsList, ok := raw["seasons"].([]any); ok {
		for _, s := range seasonsList {
			if sm, ok := s.(map[string]any); ok {
				slug := strVal(sm, "seasonSlug")
				if slug != "" {
					c.addSeason(project, strVal(sm, "title"), slug)
				}
			}
		}
	}

	// Parse seasonsGallery — contains items (episodes) per season
	if gallery, ok := raw["seasonsGallery"].([]any); ok {
		for _, sg := range gallery {
			sm, ok := sg.(map[string]any)
			if !ok {
				continue
			}
			slug := strVal(sm, "seasonSlug")
			c.addSeason(project, strVal(sm, "title"), slug)

			if items, ok := sm["items"].([]any); ok {
				for _, it := range items {
					im, ok := it.(map[string]any)
					if !ok {
						continue
					}
					ep := starlightEpisodeInfo{
						title:      strVal(im, "title"),
						hash:       strVal(im, "hash"),
						seasonSlug: slug,
						date:       strVal(im, "dateOfBroadcast"),
					}
					if ep.date == "" {
						ep.date = strVal(im, "timeUploadVideo")
					}
					if seriesTitle := strVal(im, "seriesTitle"); seriesTitle != "" {
						if n, err := strconv.Atoi(seriesTitle); err == nil {
							ep.number = n
						}
					}
					project.episodes = append(project.episodes, ep)
				}
			}
		}
	}

	// Load missing season episodes
	c.loadMissingSeasonEpisodes(req, project, href)

	return project
}

func (c *starlightChecker) addSeason(project *starlightProject, title, slug string) {
	if slug == "" {
		return
	}
	for _, s := range project.seasons {
		if s.slug == slug {
			return
		}
	}
	project.seasons = append(project.seasons, starlightSeasonInfo{title: title, slug: slug})
}

func (c *starlightChecker) loadMissingSeasonEpisodes(req *http.Request, project *starlightProject, href string) {
	for _, season := range project.seasons {
		if season.slug == "" {
			continue
		}
		// Check if we already have episodes for this season
		hasEpisodes := false
		for _, ep := range project.episodes {
			if ep.seasonSlug == season.slug {
				hasEpisodes = true
				break
			}
		}
		if hasEpisodes {
			continue
		}

		// Fetch season page
		seasonURL := href + "/" + season.slug
		body, ok := c.fetchAPI(req, seasonURL, c.host)
		if !ok {
			continue
		}

		var raw map[string]any
		if err := json.Unmarshal(body, &raw); err != nil {
			continue
		}

		if items, ok := raw["items"].([]any); ok {
			for _, it := range items {
				im, ok := it.(map[string]any)
				if !ok {
					continue
				}
				hash := strVal(im, "hash")
				if hash == "" {
					continue
				}
				ep := starlightEpisodeInfo{
					title:      strVal(im, "title"),
					hash:       hash,
					seasonSlug: season.slug,
					date:       strVal(im, "dateOfBroadcast"),
				}
				if ep.date == "" {
					ep.date = strVal(im, "timeUploadVideo")
				}
				if seriesTitle := strVal(im, "seriesTitle"); seriesTitle != "" {
					if n, err := strconv.Atoi(seriesTitle); err == nil {
						ep.number = n
					}
				}
				project.episodes = append(project.episodes, ep)
			}
		}
	}
}

func (c *starlightChecker) getEpisodes(project *starlightProject, seasonSlug string) []starlightEpisodeInfo {
	if seasonSlug == "" {
		return project.episodes
	}
	var out []starlightEpisodeInfo
	for _, ep := range project.episodes {
		if ep.seasonSlug == seasonSlug {
			out = append(out, ep)
		}
	}
	return out
}

func (c *starlightChecker) resolveStream(req *http.Request, hash string) *starlightStreamResult {
	streamURL := fmt.Sprintf("%s/%s?referer=%s&lang=%s",
		c.playerAPI, hash, url.QueryEscape(c.referer), c.lang)

	body, ok := c.fetchAPI(req, streamURL, c.referer)
	if !ok {
		return nil
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil
	}

	var stream string
	if videos, ok := raw["video"].([]any); ok && len(videos) > 0 {
		if v, ok := videos[0].(map[string]any); ok {
			stream, _ = v["mediaHlsNoAdv"].(string)
			if stream == "" {
				stream, _ = v["mediaHls"].(string)
			}
			if stream == "" {
				if media, ok := v["media"].([]any); ok && len(media) > 0 {
					if m, ok := media[0].(map[string]any); ok {
						stream, _ = m["url"].(string)
					}
				}
			}
		}
	}

	if stream == "" {
		return nil
	}

	result := &starlightStreamResult{
		stream: stream,
		name:   strVal(raw, "name"),
	}

	// Parse multi-HLS streams from /hls/multi URLs
	result.streams = starlightParseMultiHLS(stream)
	if len(result.streams) > 0 {
		result.stream = result.streams[0].link
	}

	return result
}

func starlightParseMultiHLS(streamURL string) []starlightQualityStream {
	if !strings.Contains(strings.ToLower(streamURL), "/hls/multi") {
		return nil
	}

	u, err := url.Parse(streamURL)
	if err != nil {
		return nil
	}

	files := u.Query()["file"]
	if len(files) == 0 {
		return nil
	}

	qualCounts := make(map[string]int)
	var result []starlightQualityStream

	for _, f := range files {
		if f == "" {
			continue
		}
		decoded, err := url.QueryUnescape(f)
		if err != nil {
			decoded = f
		}

		quality := starlightDetectQuality(decoded)
		qualCounts[quality]++
		if qualCounts[quality] > 1 {
			quality = fmt.Sprintf("%s_%d", quality, qualCounts[quality])
		}

		result = append(result, starlightQualityStream{link: decoded, quality: quality})
	}

	return result
}

func starlightDetectQuality(u string) string {
	lower := strings.ToLower(u)
	switch {
	case strings.Contains(lower, "/lq."):
		return "360"
	case strings.Contains(lower, "/mq."):
		return "480"
	case strings.Contains(lower, "/hq."):
		return "720"
	case strings.Contains(lower, "/sd."):
		return "480"
	case strings.Contains(lower, "/hd."):
		return "720"
	}

	if m := starlightQualityRe.FindStringSubmatch(lower); len(m) >= 2 {
		return m[1]
	}

	return "auto"
}

func (c *starlightChecker) checkSearch(req *http.Request) bool {
	q := req.URL.Query()
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	results := c.search(req, title, originalTitle)
	return len(results) > 0
}

func (c *starlightChecker) fetchAPI(req *http.Request, target, referer string) ([]byte, bool) {
	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, target, nil)
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

// starlightSeasonNumber extracts season number from title (digits) or uses fallback index+1
func starlightSeasonNumber(season starlightSeasonInfo, fallbackIdx int) string {
	if season.title != "" {
		var digits strings.Builder
		for _, ch := range season.title {
			if ch >= '0' && ch <= '9' {
				digits.WriteRune(ch)
			}
		}
		if digits.Len() > 0 {
			return digits.String()
		}
	}
	return strconv.Itoa(fallbackIdx + 1)
}

// strVal is a helper to extract a string from a map[string]any
func strVal(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}
