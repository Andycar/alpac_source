package litesrc

import (
	"fmt"
	"html"
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

// klonfunChecker implements the KlonFUN Ukrainian balancer.
// HTML search (DLE-based), Ashdi-style player with voice/season/episode structure.
type klonfunChecker struct {
	client *http.Client
	host   string
}

var (
	klonfunItemHrefRe      = regexp.MustCompile(`(?is)<a[^>]*href="([^"]+)"[^>]*class="[^"]*(?:short-news__small-card__link|card-link__style)[^"]*"|<a[^>]*class="[^"]*(?:short-news__small-card__link|card-link__style)[^"]*"[^>]*href="([^"]+)"`)
	klonfunItemTitleRe     = regexp.MustCompile(`(?is)class="[^"]*card-link__text[^"]*"[^>]*>(.*?)</div>`)
	klonfunItemPosterRe    = regexp.MustCompile(`(?is)<img[^>]*class="[^"]*card-poster__img[^"]*"[^>]*(?:data-src|src)="([^"]+)"`)
	klonfunIframeRe        = regexp.MustCompile(`(?is)<div[^>]*class="[^"]*film-player[^"]*"[^>]*>.*?<iframe[^>]*(?:data-src|src)="([^"]+)"`)
	klonfunFileDirectRe    = regexp.MustCompile(`(?is)file\s*:\s*['"](https?://[^'">\s]+\.m3u8[^'">\s]*)['"]`)
	klonfunYearRe          = regexp.MustCompile(`(19|20)\d{2}`)
	klonfunNumberRe        = regexp.MustCompile(`(\d+)`)
	klonfunH1Re            = regexp.MustCompile(`(?is)<h1[^>]*class="[^"]*seo-h1__position[^"]*"[^>]*>(.*?)</h1>`)
	klonfunIsSerialRe      = regexp.MustCompile(`(?i)/serial/`)
	klonfunSearchHeadingRe = regexp.MustCompile(`(?is)<a[^>]*href="([^"]+)"[^>]*>.*?<span[^>]*class="[^"]*searchheading[^"]*"[^>]*>(.*?)</span>`)
)

func NewKlonFUNChecker(cfg config.Config) *klonfunChecker {
	host := strings.TrimSpace(cfg.Online.KlonFUN.Host)
	if host == "" {
		host = "https://klon.fun"
	}
	host = strings.TrimRight(host, "/")

	return &klonfunChecker{
		client: httpclient.NewForBalancer("klonfun", 12*time.Second),
		host:   host,
	}
}

func (c *klonfunChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show := c.checkSearch(req)
			writeCheckSearchResponse(w, show, pluginQualityBadgeGet("klonfun"))
			return
		}
		c.index(w, req, links)
	}
}

// --- Models ---

type klonfunSearchResult struct {
	title  string
	urlStr string
	poster string
	year   int
}

type klonfunItem struct {
	title          string
	playerURL      string
	isSerialPlayer bool
}

type klonfunMovieStream struct {
	title string
	link  string
}

type klonfunSerialEpisode struct {
	number int
	title  string
	link   string
}

type klonfunSerialVoice struct {
	key         string
	displayName string
	seasons     map[int][]klonfunSerialEpisode
}

type klonfunSerialStructure struct {
	voices []klonfunSerialVoice
}

// --- Handler ---

func (c *klonfunChecker) index(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	imdbID := strings.TrimSpace(q.Get("imdb_id"))
	serial, _ := getsTVQueryInt(q.Get("serial"))
	seasonNum, seasonSet := getsTVQueryInt(q.Get("s"))
	if !seasonSet {
		seasonNum = -1
	}
	t := strings.TrimSpace(q.Get("t"))
	href := strings.TrimSpace(q.Get("href"))
	host := hostFromRequest(req)

	if href == "" {
		results := c.searchAll(req, imdbID, title, originalTitle)
		if len(results) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}

		if len(results) > 1 {
			data := make([]map[string]any, 0, len(results))
			for _, res := range results {
				link := fmt.Sprintf("%s/lite/klonfun?imdb_id=%s&title=%s&original_title=%s&serial=%s&href=%s",
					host, url.QueryEscape(imdbID), url.QueryEscape(title),
					url.QueryEscape(originalTitle), q.Get("serial"), url.QueryEscape(res.urlStr))
				yr := ""
				if res.year > 0 {
					yr = strconv.Itoa(res.year)
				}
				data = append(data, map[string]any{
					"title":  res.title,
					"year":   yr,
					"url":    link,
					"poster": res.poster,
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

		href = results[0].urlStr
	}

	// Get item page
	item := c.getItem(req, href)
	if item == nil || item.playerURL == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	contentTitle := title
	if contentTitle == "" {
		contentTitle = item.title
	}
	if contentTitle == "" {
		contentTitle = "KlonFUN"
	}

	isSerial := serial == 1 || item.isSerialPlayer
	if isSerial {
		c.writeSerial(w, req, rjson, host, item, contentTitle, originalTitle, imdbID, href, t, seasonNum, links)
	} else {
		c.writeMovie(w, req, rjson, contentTitle, originalTitle, item, links)
	}
}

func (c *klonfunChecker) writeSerial(w http.ResponseWriter, req *http.Request, rjson bool, host string, item *klonfunItem, title, originalTitle, imdbID, href, t string, seasonNum int, links *proxylink.Manager) {
	structure := c.getSerialStructure(req, item.playerURL)
	if structure == nil || len(structure.voices) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if seasonNum == -1 {
		// Collect all seasons
		seasonSet := make(map[int]struct{})
		if t != "" {
			for _, v := range structure.voices {
				if strings.EqualFold(v.key, t) {
					for sn := range v.seasons {
						seasonSet[sn] = struct{}{}
					}
					break
				}
			}
		}
		if len(seasonSet) == 0 {
			for _, v := range structure.voices {
				for sn := range v.seasons {
					seasonSet[sn] = struct{}{}
				}
			}
		}

		seasonNums := make([]int, 0, len(seasonSet))
		for sn := range seasonSet {
			seasonNums = append(seasonNums, sn)
		}
		sort.Ints(seasonNums)

		if len(seasonNums) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}

		data := make([]map[string]any, 0, len(seasonNums))
		labels := make([]string, 0, len(seasonNums))
		for _, sn := range seasonNums {
			link := fmt.Sprintf("%s/lite/klonfun?imdb_id=%s&title=%s&original_title=%s&serial=1&s=%d&href=%s",
				host, url.QueryEscape(imdbID), url.QueryEscape(title),
				url.QueryEscape(originalTitle), sn, url.QueryEscape(href))
			if t != "" {
				link += "&t=" + url.QueryEscape(t)
			}
			name := fmt.Sprintf("%d", sn)
			data = append(data, map[string]any{
				"method": "link",
				"id":     sn,
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

	// Episodes for season
	voicesForSeason := make([]klonfunSerialVoice, 0)
	for _, v := range structure.voices {
		if _, ok := v.seasons[seasonNum]; ok {
			voicesForSeason = append(voicesForSeason, v)
		}
	}

	if len(voicesForSeason) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Select voice
	selected := voicesForSeason[0]
	for _, v := range voicesForSeason {
		if strings.EqualFold(v.key, t) {
			selected = v
			break
		}
	}

	// Voice buttons
	voiceData := make([]map[string]any, 0, len(voicesForSeason))
	for _, v := range voicesForSeason {
		voiceLink := fmt.Sprintf("%s/lite/klonfun?imdb_id=%s&title=%s&original_title=%s&serial=1&s=%d&t=%s&href=%s",
			host, url.QueryEscape(imdbID), url.QueryEscape(title),
			url.QueryEscape(originalTitle), seasonNum, url.QueryEscape(v.key), url.QueryEscape(href))
		voiceData = append(voiceData, map[string]any{
			"name":   v.displayName,
			"active": strings.EqualFold(v.key, selected.key),
			"url":    voiceLink,
		})
	}

	episodes, ok := selected.seasons[seasonNum]
	if !ok || len(episodes) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	sort.Slice(episodes, func(i, j int) bool { return episodes[i].number < episodes[j].number })

	baseTitle := getsTVJoinName(title, originalTitle)
	data := make([]map[string]any, 0, len(episodes))
	labels := make([]string, 0, len(episodes))
	seasons := make([]int, 0, len(episodes))
	epNums := make([]int, 0, len(episodes))

	for _, ep := range episodes {
		epTitle := ep.title
		if epTitle == "" {
			epTitle = fmt.Sprintf("Серія %d", ep.number)
		}

		streamURL := klonfunStripLampacArgs(ep.link)
		streamURL = streamProxyURL(req, streamURL, "klonfun", links)

		data = append(data, map[string]any{
			"method": "play",
			"url":    streamURL,
			"stream": streamURL,
			"s":      seasonNum,
			"e":      ep.number,
			"name":   epTitle,
			"title":  fmt.Sprintf("%s (%s)", baseTitle, epTitle),
		})
		labels = append(labels, epTitle)
		seasons = append(seasons, seasonNum)
		epNums = append(epNums, ep.number)
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type":  "episode",
			"data":  data,
			"voice": voiceData,
		})
	} else {
		var sb strings.Builder
		sb.WriteString(`<div class="videos__line">`)
		for _, v := range voiceData {
			activeClass := ""
			if v["active"].(bool) {
				activeClass = " active"
			}
			sb.WriteString(fmt.Sprintf(`<a class="videos__button selector%s" href="%s">%s</a> `, activeClass, v["url"], v["name"]))
		}
		sb.WriteString(`</div><div class="videos__line">`)
		for i, row := range data {
			getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, seasons[i], epNums[i])
		}
		sb.WriteString(`</div>`)
		writeHTML(w, http.StatusOK, sb.String())
	}
}

func (c *klonfunChecker) writeMovie(w http.ResponseWriter, req *http.Request, rjson bool, title, originalTitle string, item *klonfunItem, links *proxylink.Manager) {
	streams := c.getMovieStreams(req, item.playerURL)
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

		streamURL := klonfunStripLampacArgs(s.link)
		streamURL = streamProxyURL(req, streamURL, "klonfun", links)

		data = append(data, map[string]any{
			"method": "play",
			"url":    streamURL,
			"stream": streamURL,
			"name":   label,
			"title":  movieTitle,
		})
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "movie", "data": data})
	} else {
		var sb strings.Builder
		sb.WriteString(`<div class="videos__line">`)
		for i, row := range data {
			getsTVAppendMovieHTML(&sb, row, row["name"].(string), i == 0, 0, 0)
		}
		sb.WriteString(`</div>`)
		writeHTML(w, http.StatusOK, sb.String())
	}
}

// --- API methods ---

func (c *klonfunChecker) searchAll(req *http.Request, imdbID, title, originalTitle string) []klonfunSearchResult {
	// Try IMDB first
	if imdbID != "" {
		if results := c.searchByQuery(req, imdbID); len(results) > 0 {
			return results
		}
	}

	// Try original title, then title
	seen := make(map[string]struct{})
	for _, query := range []string{originalTitle, title} {
		query = strings.TrimSpace(query)
		if query == "" {
			continue
		}
		if _, ok := seen[strings.ToLower(query)]; ok {
			continue
		}
		seen[strings.ToLower(query)] = struct{}{}
		if results := c.searchByQuery(req, query); len(results) > 0 {
			return results
		}
	}
	return nil
}

func (c *klonfunChecker) searchByQuery(req *http.Request, query string) []klonfunSearchResult {
	form := url.Values{}
	form.Set("do", "search")
	form.Set("subaction", "search")
	form.Set("story", query)

	body, ok := c.fetchPost(req, c.host, form.Encode(), c.host)
	if !ok {
		return nil
	}

	htmlStr := string(body)

	// Try main search pattern
	results := klonfunParseSearchResults(htmlStr, c)

	// Fallback: searchheading pattern
	if len(results) == 0 {
		matches := klonfunSearchHeadingRe.FindAllStringSubmatch(htmlStr, -1)
		seen := make(map[string]struct{})
		for _, m := range matches {
			href := c.normalizeURL(strings.TrimSpace(m[1]))
			if href == "" {
				continue
			}
			if _, ok := seen[href]; ok {
				continue
			}
			seen[href] = struct{}{}
			itemTitle := bambooCleanText(m[2])
			if itemTitle != "" {
				results = append(results, klonfunSearchResult{
					title:  itemTitle,
					urlStr: href,
				})
			}
		}
	}

	return results
}

func klonfunParseSearchResults(htmlStr string, c *klonfunChecker) []klonfunSearchResult {
	// Split HTML at each slide-item boundary to get individual card blocks.
	// This avoids lazy-match issues with nested </div> tags inside cards.
	const marker = `short-news__slide-item`
	parts := strings.Split(htmlStr, marker)
	if len(parts) <= 1 {
		return nil
	}

	seen := make(map[string]struct{})
	var results []klonfunSearchResult

	for _, block := range parts[1:] {
		hrefMatch := klonfunItemHrefRe.FindStringSubmatch(block)
		href := ""
		if len(hrefMatch) >= 3 {
			// Two capture groups: (href-before-class) | (href-after-class)
			href = hrefMatch[1]
			if href == "" {
				href = hrefMatch[2]
			}
		}
		if href == "" {
			continue
		}
		href = c.normalizeURL(strings.TrimSpace(html.UnescapeString(href)))
		if href == "" {
			continue
		}
		if _, ok := seen[href]; ok {
			continue
		}
		seen[href] = struct{}{}

		itemTitle := ""
		if tm := klonfunItemTitleRe.FindStringSubmatch(block); len(tm) >= 2 {
			itemTitle = bambooCleanText(tm[1])
		}
		if itemTitle == "" {
			continue
		}

		poster := ""
		if pm := klonfunItemPosterRe.FindStringSubmatch(block); len(pm) >= 2 {
			poster = c.normalizeURL(strings.TrimSpace(pm[1]))
		}

		year := 0
		if ym := klonfunYearRe.FindString(block); ym != "" {
			year, _ = strconv.Atoi(ym)
		}

		results = append(results, klonfunSearchResult{
			title:  itemTitle,
			urlStr: href,
			poster: poster,
			year:   year,
		})
	}

	return results
}

func (c *klonfunChecker) getItem(req *http.Request, href string) *klonfunItem {
	body, ok := c.fetchGet(req, href, c.host)
	if !ok {
		return nil
	}

	htmlStr := string(body)

	title := ""
	if m := klonfunH1Re.FindStringSubmatch(htmlStr); len(m) >= 2 {
		title = bambooCleanText(m[1])
	}

	playerURL := ""
	if m := klonfunIframeRe.FindStringSubmatch(htmlStr); len(m) >= 2 {
		playerURL = c.normalizeURL(strings.TrimSpace(html.UnescapeString(m[1])))
	}

	return &klonfunItem{
		title:          title,
		playerURL:      playerURL,
		isSerialPlayer: klonfunIsSerialRe.MatchString(playerURL),
	}
}

func (c *klonfunChecker) getMovieStreams(req *http.Request, playerURL string) []klonfunMovieStream {
	// Add multivoice param for ashdi
	playerURL = klonfunWithAshdiMultivoice(playerURL)

	body, ok := c.fetchGet(req, playerURL, c.host)
	if !ok {
		return nil
	}

	htmlStr := string(body)

	// Try to parse file:[...] JSON array
	fileJSON := klonfunExtractFileArray(htmlStr)
	if fileJSON != "" {
		fileJSON = html.UnescapeString(fileJSON)
		fileJSON = strings.ReplaceAll(fileJSON, `\/`, "/")

		var arr []map[string]any
		if err := json.Unmarshal([]byte(fileJSON), &arr); err == nil {
			var streams []klonfunMovieStream
			for i, item := range arr {
				link, _ := item["file"].(string)
				if link == "" {
					continue
				}
				itemTitle, _ := item["title"].(string)
				itemTitle = klonfunFormatMovieTitle(bambooCleanText(itemTitle), link, i+1)
				streams = append(streams, klonfunMovieStream{title: itemTitle, link: link})
			}
			if len(streams) > 0 {
				return streams
			}
		}
	}

	// Fallback: direct file:"url.m3u8"
	if m := klonfunFileDirectRe.FindStringSubmatch(htmlStr); len(m) >= 2 {
		return []klonfunMovieStream{{
			title: "Основне джерело",
			link:  m[1],
		}}
	}

	return nil
}

func (c *klonfunChecker) getSerialStructure(req *http.Request, playerURL string) *klonfunSerialStructure {
	body, ok := c.fetchGet(req, playerURL, c.host)
	if !ok {
		return nil
	}

	htmlStr := string(body)
	fileJSON := klonfunExtractFileArray(htmlStr)
	if fileJSON == "" {
		return nil
	}

	fileJSON = html.UnescapeString(fileJSON)
	fileJSON = strings.ReplaceAll(fileJSON, `\/`, "/")

	var arr []map[string]any
	if err := json.Unmarshal([]byte(fileJSON), &arr); err != nil {
		return nil
	}

	structure := &klonfunSerialStructure{}
	voiceCounter := make(map[string]int)

	for _, voiceObj := range arr {
		seasonsRaw, ok := voiceObj["folder"].([]any)
		if !ok || len(seasonsRaw) == 0 {
			continue
		}

		baseName := bambooCleanText(fmt.Sprint(voiceObj["title"]))
		if baseName == "" {
			baseName = "Озвучення"
		}

		displayName := klonfunUniqueVoice(baseName, voiceCounter)
		voice := klonfunSerialVoice{
			key:         displayName,
			displayName: displayName,
			seasons:     make(map[int][]klonfunSerialEpisode),
		}

		seasonFallback := 1
		for _, s := range seasonsRaw {
			seasonObj, ok := s.(map[string]any)
			if !ok {
				continue
			}

			seasonTitle := fmt.Sprint(seasonObj["title"])
			seasonNumber := klonfunParseNumber(seasonTitle, seasonFallback)

			episodesRaw, ok := seasonObj["folder"].([]any)
			if !ok || len(episodesRaw) == 0 {
				seasonFallback++
				continue
			}

			var episodes []klonfunSerialEpisode
			epFallback := 1

			for _, e := range episodesRaw {
				epObj, ok := e.(map[string]any)
				if !ok {
					continue
				}
				link, _ := epObj["file"].(string)
				if link == "" {
					continue
				}

				epTitle := bambooCleanText(fmt.Sprint(epObj["title"]))
				epNumber := klonfunParseNumber(epTitle, epFallback)

				if epTitle == "" {
					epTitle = fmt.Sprintf("Серія %d", epNumber)
				}

				episodes = append(episodes, klonfunSerialEpisode{
					number: epNumber,
					title:  epTitle,
					link:   link,
				})
				epFallback++
			}

			if len(episodes) > 0 {
				sort.Slice(episodes, func(i, j int) bool { return episodes[i].number < episodes[j].number })
				voice.seasons[seasonNumber] = episodes
			}
			seasonFallback++
		}

		if len(voice.seasons) > 0 {
			structure.voices = append(structure.voices, voice)
		}
	}

	// Sort voices alphabetically
	sort.Slice(structure.voices, func(i, j int) bool {
		return strings.ToLower(structure.voices[i].displayName) < strings.ToLower(structure.voices[j].displayName)
	})

	if len(structure.voices) > 0 {
		return structure
	}
	return nil
}

func (c *klonfunChecker) checkSearch(req *http.Request) bool {
	q := req.URL.Query()
	imdbID := strings.TrimSpace(q.Get("imdb_id"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	results := c.searchAll(req, imdbID, title, originalTitle)
	return len(results) > 0
}

// --- Helpers ---

func klonfunExtractFileArray(htmlStr string) string {
	searchIdx := 0
	for searchIdx >= 0 && searchIdx < len(htmlStr) {
		fileIdx := strings.Index(strings.ToLower(htmlStr[searchIdx:]), "file")
		if fileIdx < 0 {
			return ""
		}
		fileIdx += searchIdx

		colonIdx := strings.Index(htmlStr[fileIdx:], ":")
		if colonIdx < 0 {
			return ""
		}
		colonIdx += fileIdx

		startIdx := colonIdx + 1
		for startIdx < len(htmlStr) && (htmlStr[startIdx] == ' ' || htmlStr[startIdx] == '\t' || htmlStr[startIdx] == '\n' || htmlStr[startIdx] == '\r') {
			startIdx++
		}

		if startIdx < len(htmlStr) && (htmlStr[startIdx] == '\'' || htmlStr[startIdx] == '"') {
			startIdx++
			for startIdx < len(htmlStr) && (htmlStr[startIdx] == ' ' || htmlStr[startIdx] == '\t' || htmlStr[startIdx] == '\n' || htmlStr[startIdx] == '\r') {
				startIdx++
			}
		}

		if startIdx >= len(htmlStr) || htmlStr[startIdx] != '[' {
			searchIdx = fileIdx + 4
			continue
		}

		// Find matching ]
		depth := 0
		inStr := false
		escaped := false

		for i := startIdx; i < len(htmlStr); i++ {
			ch := htmlStr[i]
			if inStr {
				if escaped {
					escaped = false
					continue
				}
				if ch == '\\' {
					escaped = true
					continue
				}
				if ch == '"' {
					inStr = false
				}
				continue
			}
			if ch == '"' {
				inStr = true
				continue
			}
			if ch == '[' {
				depth++
				continue
			}
			if ch == ']' {
				depth--
				if depth == 0 {
					return htmlStr[startIdx : i+1]
				}
			}
		}
		return ""
	}
	return ""
}

func klonfunWithAshdiMultivoice(u string) string {
	if u == "" {
		return u
	}
	if !strings.Contains(strings.ToLower(u), "ashdi.vip/vod/") {
		return u
	}
	if strings.Contains(strings.ToLower(u), "multivoice") {
		return u
	}
	if strings.Contains(u, "?") {
		return u + "&multivoice"
	}
	return u + "?multivoice"
}

func klonfunFormatMovieTitle(rawTitle, streamURL string, index int) string {
	title := rawTitle
	if title == "" {
		title = fmt.Sprintf("Варіант %d", index)
	}

	// Detect quality tag
	combined := title + " " + streamURL
	lower := strings.ToLower(combined)
	tag := ""
	if regexp.MustCompile(`(^|[^0-9])(2160p?)([^0-9]|$)|\b4k\b|\buhd\b`).MatchString(lower) {
		tag = "[4K]"
	} else if regexp.MustCompile(`(^|[^0-9])(1080p?)([^0-9]|$)|\bfhd\b`).MatchString(lower) {
		tag = "[FHD]"
	}

	if tag != "" && !strings.HasPrefix(strings.ToUpper(title), "[4K]") && !strings.HasPrefix(strings.ToUpper(title), "[FHD]") {
		return tag + " " + title
	}
	return title
}

func klonfunParseNumber(s string, fallback int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	m := klonfunNumberRe.FindString(s)
	if m != "" {
		if n, err := strconv.Atoi(m); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

func klonfunUniqueVoice(baseName string, counter map[string]int) string {
	count, exists := counter[baseName]
	if !exists {
		counter[baseName] = 1
		return baseName
	}
	count++
	counter[baseName] = count
	return fmt.Sprintf("%s #%d", baseName, count)
}

func klonfunStripLampacArgs(rawURL string) string {
	return bambooStripLampacArgs(rawURL)
}

func (c *klonfunChecker) normalizeURL(u string) string {
	if u == "" {
		return ""
	}
	u = html.UnescapeString(u)
	if strings.HasPrefix(u, "//") {
		return "https:" + u
	}
	if strings.HasPrefix(u, "/") {
		return c.host + u
	}
	if !strings.HasPrefix(strings.ToLower(u), "http") {
		return c.host + "/" + strings.TrimLeft(u, "/")
	}
	return u
}

func (c *klonfunChecker) fetchGet(req *http.Request, target, referer string) ([]byte, bool) {
	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, target, nil)
	if err != nil {
		return nil, false
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	httpReq.Header.Set("Referer", referer)
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

func (c *klonfunChecker) fetchPost(req *http.Request, target, formBody, referer string) ([]byte, bool) {
	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodPost, target, strings.NewReader(formBody))
	if err != nil {
		return nil, false
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("Referer", referer)
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
