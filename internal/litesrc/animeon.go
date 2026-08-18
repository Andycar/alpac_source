package litesrc

import (
	stdjson "encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/proxylink"
)

// animeonChecker implements the AnimeON Ukrainian balancer.
// Uses animeon.club REST API for anime search, translations (fundubs),
// episodes, and stream resolution via moonanime/ashdi players.
type animeonChecker struct {
	client *http.Client
	host   string

	// cache for aggregated serial structures
	structureCache sync.Map // key: "animeId:season" -> *animeonAggregatedStructure
}

// --- JSON API models ---

type animeonSearchResponse struct {
	Result []animeonSearchModel `json:"result"`
	Count  int                  `json:"count"`
}

type animeonSearchModel struct {
	ID      int    `json:"id"`
	TitleUa string `json:"title_ua"`
	TitleEn string `json:"title_en"`
	Year    int    `json:"year"`
	ImdbID  string `json:"imdb_id"`
	Season  int    `json:"season"`
}

type animeonFundubsResponse struct {
	Translations []animeonTranslation `json:"translations"`
}

type animeonTranslation struct {
	Translation animeonFundub   `json:"translation"`
	Player      []animeonPlayer `json:"player"`
}

type animeonFundub struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type animeonPlayer struct {
	Name          string `json:"name"`
	ID            int    `json:"id"`
	EpisodesCount int    `json:"episodes_count"`
}

type animeonEpisodeResponse struct {
	Episodes []animeonEpisode `json:"episodes"`
}

type animeonEpisode struct {
	ID         int    `json:"id"`
	EpisodeNum int    `json:"episode_num"`
	Hls        string `json:"hls"`
	VideoUrl   string `json:"video_url"`
	Name       string `json:"name"`
}

// --- Aggregated serial structure ---

type animeonAggregatedStructure struct {
	animeID  int
	season   int
	voices   map[string]*animeonVoiceInfo
	cachedAt time.Time
}

type animeonVoiceInfo struct {
	name        string
	playerType  string
	displayName string
	playerID    int
	fundubID    int
	episodes    []animeonEpisodeInfo
}

type animeonEpisodeInfo struct {
	number    int
	title     string
	hls       string
	videoUrl  string
	episodeID int
}

// --- Quality detection ---

var (
	animeonQuality4kRe  = regexp.MustCompile(`(?i)(^|[^0-9])(2160p?)([^0-9]|$)|\b4k\b|\buhd\b`)
	animeonQualityFhdRe = regexp.MustCompile(`(?i)(^|[^0-9])(1080p?)([^0-9]|$)|\bfhd\b`)
	animeonFileArrayRe  = regexp.MustCompile(`(?i)file\s*:\s*['"]?\s*\[`)
	animeonFileSingleRe = regexp.MustCompile(`(?i)file\s*:\s*['"](https?://[^'"]+)['"]`)
	animeonMoonFileRe   = regexp.MustCompile(`(?i)file:\s*"([^"]+\.m3u8)"`)
)

func NewAnimeonChecker(cfg config.Config) *animeonChecker {
	host := strings.TrimSpace(cfg.Online.AnimeON.Host)
	if host == "" {
		host = "https://animeon.club"
	}
	host = strings.TrimRight(host, "/")

	return &animeonChecker{
		client: httpclient.NewForBalancer("animeon", 14*time.Second),
		host:   host,
	}
}

func (c *animeonChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		raw := strings.TrimPrefix(strings.ToLower(req.URL.Path), "/lite/")

		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show := c.checkSearch(req)
			writeCheckSearchResponse(w, show, pluginQualityBadgeGet("animeon"))
			return
		}

		if raw == "animeon/play" {
			c.play(w, req, links)
			return
		}

		c.index(w, req, links)
	}
}

// --- checksearch ---

func (c *animeonChecker) checkSearch(req *http.Request) bool {
	q := req.URL.Query()
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	imdbID := strings.TrimSpace(q.Get("imdb_id"))
	serial, _ := getsTVQueryInt(q.Get("serial"))

	seasons := c.search(req, title, originalTitle, imdbID, serial)
	return len(seasons) > 0
}

// --- Search ---

func (c *animeonChecker) search(req *http.Request, title, originalTitle, imdbID string, serial int) []animeonSearchModel {
	findAnime := func(query string) []animeonSearchModel {
		if query == "" {
			return nil
		}
		searchURL := fmt.Sprintf("%s/api/anime/search?text=%s", c.host, url.QueryEscape(query))
		httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, searchURL, nil)
		if err != nil {
			return nil
		}
		httpReq.Header.Set("User-Agent", "Mozilla/5.0")
		httpReq.Header.Set("Referer", c.host)

		resp, err := c.client.Do(httpReq)
		if err != nil {
			return nil
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		if err != nil {
			return nil
		}
		var sr animeonSearchResponse
		if err := stdjson.Unmarshal(body, &sr); err != nil {
			return nil
		}
		return sr.Result
	}

	results := findAnime(title)
	if len(results) == 0 {
		results = findAnime(originalTitle)
	}
	if len(results) == 0 {
		return nil
	}

	// For serials, try additional search by TitleEn to get more seasons
	if serial == 1 && len(results) > 0 {
		fallbackEN := results[0].TitleEn
		if fallbackEN != "" {
			extra := findAnime(fallbackEN)
			if len(extra) > 0 {
				seen := make(map[int]struct{}, len(results))
				for _, r := range results {
					seen[r.ID] = struct{}{}
				}
				for _, e := range extra {
					if _, ok := seen[e.ID]; !ok {
						results = append(results, e)
						seen[e.ID] = struct{}{}
					}
				}
			}
		}
	}

	// Filter by IMDB if available
	if imdbID != "" {
		var matched []animeonSearchModel
		for _, r := range results {
			if r.ImdbID == imdbID {
				matched = append(matched, r)
			}
		}
		if len(matched) > 0 {
			return matched
		}
	}

	// Fallback to first result
	return results[:1]
}

// --- Fundubs (translations) ---

func (c *animeonChecker) getFundubs(req *http.Request, animeID int) []animeonTranslation {
	apiURL := fmt.Sprintf("%s/api/player/%d/translations", c.host, animeID)
	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, apiURL, nil)
	if err != nil {
		return nil
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0")
	httpReq.Header.Set("Referer", c.host)

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil
	}
	var fr animeonFundubsResponse
	if err := stdjson.Unmarshal(body, &fr); err != nil {
		return nil
	}
	return fr.Translations
}

// --- Episodes ---

func (c *animeonChecker) getEpisodes(req *http.Request, animeID, playerID, fundubID int) []animeonEpisode {
	apiURL := fmt.Sprintf("%s/api/player/%d/episodes?take=100&skip=-1&playerId=%d&translationId=%d",
		c.host, animeID, playerID, fundubID)
	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, apiURL, nil)
	if err != nil {
		return nil
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0")
	httpReq.Header.Set("Referer", c.host)

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil
	}
	var er animeonEpisodeResponse
	if err := stdjson.Unmarshal(body, &er); err != nil {
		return nil
	}
	return er.Episodes
}

// --- Aggregate serial structure ---

func (c *animeonChecker) aggregateStructure(req *http.Request, animeID, season int) *animeonAggregatedStructure {
	cacheKey := fmt.Sprintf("%d:%d", animeID, season)
	if v, ok := c.structureCache.Load(cacheKey); ok {
		if s, ok := v.(*animeonAggregatedStructure); ok && time.Since(s.cachedAt) < 20*time.Minute {
			return s
		}
	}

	translations := c.getFundubs(req, animeID)
	if len(translations) == 0 {
		return nil
	}

	structure := &animeonAggregatedStructure{
		animeID:  animeID,
		season:   season,
		voices:   make(map[string]*animeonVoiceInfo),
		cachedAt: time.Now(),
	}

	for _, tr := range translations {
		if tr.Translation.Name == "" {
			continue
		}
		for _, player := range tr.Player {
			display := fmt.Sprintf("[%s] %s", player.Name, tr.Translation.Name)

			episodes := c.getEpisodes(req, animeID, player.ID, tr.Translation.ID)
			if len(episodes) == 0 {
				continue
			}

			sort.Slice(episodes, func(i, j int) bool {
				return episodes[i].EpisodeNum < episodes[j].EpisodeNum
			})

			epInfos := make([]animeonEpisodeInfo, 0, len(episodes))
			for _, ep := range episodes {
				epInfos = append(epInfos, animeonEpisodeInfo{
					number:    ep.EpisodeNum,
					title:     ep.Name,
					hls:       ep.Hls,
					videoUrl:  ep.VideoUrl,
					episodeID: ep.ID,
				})
			}

			structure.voices[display] = &animeonVoiceInfo{
				name:        tr.Translation.Name,
				playerType:  strings.ToLower(player.Name),
				displayName: display,
				playerID:    player.ID,
				fundubID:    tr.Translation.ID,
				episodes:    epInfos,
			}
		}
	}

	if len(structure.voices) == 0 {
		return nil
	}

	c.structureCache.Store(cacheKey, structure)
	return structure
}

// --- Index handler ---

func (c *animeonChecker) index(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	imdbID := strings.TrimSpace(q.Get("imdb_id"))
	serial, _ := getsTVQueryInt(q.Get("serial"))
	s, _ := getsTVQueryInt(q.Get("s"))
	t := strings.TrimSpace(q.Get("t"))
	host := hostFromRequest(req)

	seasons := c.search(req, title, originalTitle, imdbID, serial)
	if len(seasons) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if serial == 1 {
		c.indexSerial(w, req, rjson, host, title, originalTitle, imdbID, s, t, seasons, links)
	} else {
		c.indexMovie(w, req, rjson, host, title, originalTitle, seasons, links)
	}
}

// --- Serial flow ---

func (c *animeonChecker) indexSerial(w http.ResponseWriter, req *http.Request, rjson bool,
	host, title, originalTitle, imdbID string, s int, t string,
	seasons []animeonSearchModel, links *proxylink.Manager) {

	// Build unique season items
	type seasonItem struct {
		anime        animeonSearchModel
		seasonNumber int
	}
	seen := make(map[int]struct{})
	var seasonItems []seasonItem
	for idx, anime := range seasons {
		sn := anime.Season
		if sn <= 0 {
			sn = idx + 1
		}
		if _, ok := seen[sn]; ok {
			continue
		}
		seen[sn] = struct{}{}
		seasonItems = append(seasonItems, seasonItem{anime: anime, seasonNumber: sn})
	}
	sort.Slice(seasonItems, func(i, j int) bool {
		return seasonItems[i].seasonNumber < seasonItems[j].seasonNumber
	})

	if s == -1 || s == 0 {
		// Step 1: Season selector (each anime = season)
		var sb strings.Builder
		for _, item := range seasonItems {
			link := fmt.Sprintf("%s/lite/animeon?imdb_id=%s&kinopoisk_id=%s&title=%s&original_title=%s&year=%s&serial=1&s=%d",
				host, url.QueryEscape(imdbID),
				url.QueryEscape(req.URL.Query().Get("kinopoisk_id")),
				url.QueryEscape(title),
				url.QueryEscape(originalTitle),
				url.QueryEscape(req.URL.Query().Get("year")),
				item.seasonNumber)
			getsTVAppendSeasonHTML(&sb, map[string]any{
				"method": "link",
				"url":    link,
			}, strconv.Itoa(item.seasonNumber), false)
		}
		if rjson {
			writeJSON(w, http.StatusOK, map[string]any{"html": sb.String()})
			return
		}
		writeHTML(w, http.StatusOK, sb.String())
		return
	}

	// Step 2/3: Voice + episodes for selected season
	var selected *seasonItem
	for i := range seasonItems {
		if seasonItems[i].seasonNumber == s {
			selected = &seasonItems[i]
			break
		}
	}
	if selected == nil && s >= 0 && s < len(seasons) {
		sn := seasons[s].Season
		if sn <= 0 {
			sn = s + 1
		}
		selected = &seasonItem{anime: seasons[s], seasonNumber: sn}
	}
	if selected == nil {
		writeGetsTVEmpty(w, rjson)
		return
	}

	structure := c.aggregateStructure(req, selected.anime.ID, selected.seasonNumber)
	if structure == nil || len(structure.voices) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Build sorted voice list
	type voiceEntry struct {
		key     string
		display string
	}
	var voiceList []voiceEntry
	for k, v := range structure.voices {
		d := v.displayName
		if d == "" {
			d = k
		}
		if d == "" {
			d = "Озвучка"
		}
		voiceList = append(voiceList, voiceEntry{key: k, display: d})
	}
	sort.Slice(voiceList, func(i, j int) bool {
		return voiceList[i].display < voiceList[j].display
	})

	// Auto-select first voice if t is not set
	if t == "" && len(voiceList) > 0 {
		t = voiceList[0].key
	}

	selectedVoice, ok := structure.voices[t]
	if !ok {
		writeGetsTVEmpty(w, rjson)
		return
	}

	var sb strings.Builder

	// Voice buttons
	for _, v := range voiceList {
		voiceLink := fmt.Sprintf("%s/lite/animeon?imdb_id=%s&kinopoisk_id=%s&title=%s&original_title=%s&year=%s&serial=1&s=%d&t=%s",
			host, url.QueryEscape(imdbID),
			url.QueryEscape(req.URL.Query().Get("kinopoisk_id")),
			url.QueryEscape(title),
			url.QueryEscape(originalTitle),
			url.QueryEscape(req.URL.Query().Get("year")),
			s, url.QueryEscape(v.key))
		getsTVAppendVoiceHTML(&sb, map[string]any{
			"name":   v.display,
			"url":    voiceLink,
			"active": v.key == t,
		})
	}

	// Episodes
	seasonStr := strconv.Itoa(selected.seasonNumber)
	displayTitle := title
	if displayTitle == "" {
		displayTitle = originalTitle
	}

	for _, ep := range selectedVoice.episodes {
		epName := ep.title
		if epName == "" {
			epName = fmt.Sprintf("Епізод %d", ep.number)
		}
		epStr := strconv.Itoa(ep.number)

		streamLink := ep.hls
		if streamLink == "" {
			streamLink = ep.videoUrl
		}

		needsResolve := selectedVoice.playerType == "moon" || selectedVoice.playerType == "ashdi"

		if streamLink == "" && ep.episodeID > 0 {
			callURL := fmt.Sprintf("%s/lite/animeon/play?episode_id=%d", host, ep.episodeID)
			getsTVAppendMovieHTML(&sb, map[string]any{
				"method": "call",
				"url":    callURL,
				"title":  fmt.Sprintf("%s / %s", displayTitle, epName),
			}, epName, false, intOrZero(seasonStr), intOrZero(epStr))
			continue
		}

		if streamLink == "" {
			continue
		}

		if needsResolve || strings.Contains(streamLink, "moonanime.art") || strings.Contains(streamLink, "ashdi.vip/vod") {
			callURL := fmt.Sprintf("%s/lite/animeon/play?url=%s", host, url.QueryEscape(streamLink))
			getsTVAppendMovieHTML(&sb, map[string]any{
				"method": "call",
				"url":    callURL,
				"title":  fmt.Sprintf("%s / %s", displayTitle, epName),
			}, epName, false, intOrZero(seasonStr), intOrZero(epStr))
		} else {
			proxyURL := streamProxyURL(req, streamLink, "animeon", links)
			getsTVAppendMovieHTML(&sb, map[string]any{
				"method": "play",
				"url":    proxyURL,
				"title":  fmt.Sprintf("%s / %s", displayTitle, epName),
			}, epName, false, intOrZero(seasonStr), intOrZero(epStr))
		}
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"html": sb.String()})
		return
	}
	writeHTML(w, http.StatusOK, sb.String())
}

// --- Movie flow ---

func (c *animeonChecker) indexMovie(w http.ResponseWriter, req *http.Request, rjson bool,
	host, title, originalTitle string,
	seasons []animeonSearchModel, links *proxylink.Manager) {

	if len(seasons) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	firstAnime := seasons[0]

	translations := c.getFundubs(req, firstAnime.ID)
	if len(translations) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	displayTitle := title
	if displayTitle == "" {
		displayTitle = originalTitle
	}

	var sb strings.Builder
	count := 0

	for _, tr := range translations {
		if tr.Translation.Name == "" {
			continue
		}
		for _, player := range tr.Player {
			episodes := c.getEpisodes(req, firstAnime.ID, player.ID, tr.Translation.ID)
			if len(episodes) == 0 {
				continue
			}
			firstEp := episodes[0]

			streamLink := firstEp.Hls
			if streamLink == "" {
				streamLink = firstEp.VideoUrl
			}
			if streamLink == "" {
				continue
			}

			translationName := fmt.Sprintf("[%s] %s", player.Name, tr.Translation.Name)
			needsResolve := strings.EqualFold(player.Name, "moon") || strings.EqualFold(player.Name, "ashdi")

			// For ashdi VOD, try to expand to multiple streams
			if strings.Contains(strings.ToLower(streamLink), "ashdi.vip/vod") {
				ashdiStreams := c.parseAshdiPageStreams(req, streamLink)
				if len(ashdiStreams) > 0 {
					for _, as := range ashdiStreams {
						optionName := fmt.Sprintf("%s %s", translationName, as.title)
						callURL := fmt.Sprintf("%s/lite/animeon/play?url=%s", host, url.QueryEscape(as.link))
						getsTVAppendMovieHTML(&sb, map[string]any{
							"method": "call",
							"url":    callURL,
							"title":  displayTitle,
						}, optionName, count == 0, 0, 0)
						count++
					}
					continue
				}
			}

			if needsResolve || strings.Contains(streamLink, "moonanime.art/iframe/") || strings.Contains(streamLink, "ashdi.vip/vod") {
				callURL := fmt.Sprintf("%s/lite/animeon/play?url=%s", host, url.QueryEscape(streamLink))
				getsTVAppendMovieHTML(&sb, map[string]any{
					"method": "call",
					"url":    callURL,
					"title":  displayTitle,
				}, translationName, count == 0, 0, 0)
			} else {
				proxyURL := streamProxyURL(req, streamLink, "animeon", links)
				getsTVAppendMovieHTML(&sb, map[string]any{
					"method": "play",
					"url":    proxyURL,
					"title":  displayTitle,
				}, translationName, count == 0, 0, 0)
			}
			count++
		}
	}

	if count == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"html": sb.String()})
		return
	}
	writeHTML(w, http.StatusOK, sb.String())
}

// --- Play sub-route ---

func (c *animeonChecker) play(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	rawURL := strings.TrimSpace(q.Get("url"))
	episodeID, _ := strconv.Atoi(q.Get("episode_id"))
	title := strings.TrimSpace(q.Get("title"))

	var streamLink string

	if episodeID > 0 {
		streamLink = c.resolveEpisodeStream(req, episodeID)
	} else if rawURL != "" {
		streamLink = c.resolveVideoURL(req, rawURL)
	}

	if streamLink == "" {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	// Apply proxy based on stream host
	var proxyURL string
	if strings.Contains(strings.ToLower(streamLink), "ashdi.vip") {
		proxyURL = streamProxyURLWithHeaders(req, streamLink, "animeon", links, map[string]string{
			"User-Agent": "Mozilla/5.0",
			"Referer":    "https://ashdi.vip/",
		})
	} else {
		proxyURL = streamProxyURL(req, streamLink, "animeon", links)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"method": "play",
		"url":    proxyURL,
		"title":  title,
	})
}

// --- Episode stream resolution ---

func (c *animeonChecker) resolveEpisodeStream(req *http.Request, episodeID int) string {
	apiURL := fmt.Sprintf("%s/api/player/%d/episode", c.host, episodeID)
	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, apiURL, nil)
	if err != nil {
		return ""
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0")
	httpReq.Header.Set("Referer", c.host)

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ""
	}

	var doc map[string]any
	if err := stdjson.Unmarshal(body, &doc); err != nil {
		return ""
	}

	if fileURL, ok := doc["fileUrl"].(string); ok && fileURL != "" {
		return fileURL
	}
	if videoURL, ok := doc["videoUrl"].(string); ok && videoURL != "" {
		return c.resolveVideoURL(req, videoURL)
	}
	return ""
}

// --- URL resolution ---

func (c *animeonChecker) resolveVideoURL(req *http.Request, rawURL string) string {
	if rawURL == "" {
		return ""
	}
	if strings.Contains(rawURL, "moonanime.art") {
		return c.parseMoonAnimePage(req, rawURL)
	}
	if strings.Contains(rawURL, "ashdi.vip/vod") {
		return c.parseAshdiPage(req, rawURL)
	}
	return rawURL
}

// --- MoonAnime parser ---

func (c *animeonChecker) parseMoonAnimePage(req *http.Request, rawURL string) string {
	requestURL := rawURL
	if !strings.Contains(requestURL, "player=") {
		if strings.Contains(requestURL, "?") {
			requestURL += "&player=animeon.club"
		} else {
			requestURL += "?player=animeon.club"
		}
	}

	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, requestURL, nil)
	if err != nil {
		return ""
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0")
	httpReq.Header.Set("Referer", "https://animeon.club/")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ""
	}

	m := animeonMoonFileRe.FindSubmatch(body)
	if len(m) >= 2 {
		return string(m[1])
	}
	return ""
}

// --- Ashdi parser ---

type animeonAshdiStream struct {
	title string
	link  string
}

func (c *animeonChecker) parseAshdiPage(req *http.Request, rawURL string) string {
	streams := c.parseAshdiPageStreams(req, rawURL)
	if len(streams) > 0 {
		return streams[0].link
	}
	return ""
}

func (c *animeonChecker) parseAshdiPageStreams(req *http.Request, rawURL string) []animeonAshdiStream {
	fetchURL := withAshdiMultivoice(rawURL)

	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, fetchURL, nil)
	if err != nil {
		return nil
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0")
	httpReq.Header.Set("Referer", "https://ashdi.vip/")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil
	}
	htmlStr := string(body)

	// Try to extract file:[...] array
	rawArray := extractPlayerFileArray(htmlStr)
	if rawArray != "" {
		decoded := html.UnescapeString(rawArray)
		decoded = strings.ReplaceAll(decoded, "\\/", "/")
		decoded = strings.ReplaceAll(decoded, "\\'", "'")
		decoded = strings.ReplaceAll(decoded, "\\\"", "\"")

		var items []map[string]any
		if err := stdjson.Unmarshal([]byte(decoded), &items); err == nil && len(items) > 0 {
			var streams []animeonAshdiStream
			for idx, item := range items {
				file, _ := item["file"].(string)
				if file == "" {
					continue
				}
				rawTitle, _ := item["title"].(string)
				displayTitle := buildAnimeonDisplayTitle(rawTitle, file, idx+1)
				streams = append(streams, animeonAshdiStream{title: displayTitle, link: file})
			}
			if len(streams) > 0 {
				return streams
			}
		}
	}

	// Fallback: single file:'url'
	m := animeonFileSingleRe.FindStringSubmatch(htmlStr)
	if len(m) >= 2 {
		return []animeonAshdiStream{{title: "Основне джерело", link: m[1]}}
	}

	return nil
}

// --- Helpers ---

func withAshdiMultivoice(rawURL string) string {
	if rawURL == "" {
		return rawURL
	}
	lower := strings.ToLower(rawURL)
	if !strings.Contains(lower, "ashdi.vip/vod/") {
		return rawURL
	}
	if strings.Contains(lower, "multivoice") {
		return rawURL
	}
	if strings.Contains(rawURL, "?") {
		return rawURL + "&multivoice"
	}
	return rawURL + "?multivoice"
}

func buildAnimeonDisplayTitle(rawTitle, link string, index int) string {
	normalized := rawTitle
	if strings.TrimSpace(normalized) == "" {
		normalized = fmt.Sprintf("Варіант %d", index)
	} else {
		normalized = html.UnescapeString(strings.TrimSpace(normalized))
		normalized = stripMoviePrefix(normalized)
	}

	qualTag := detectAnimeonQualityTag(normalized + " " + link)
	if qualTag == "" {
		return normalized
	}
	if strings.HasPrefix(strings.ToUpper(normalized), "[4K]") || strings.HasPrefix(strings.ToUpper(normalized), "[FHD]") {
		return normalized
	}
	return qualTag + " " + normalized
}

func stripMoviePrefix(title string) string {
	if strings.TrimSpace(title) == "" {
		return title
	}
	// Normalize whitespace
	normalized := strings.Join(strings.Fields(title), " ")
	sepIdx := strings.LastIndex(normalized, " - ")
	if sepIdx <= 0 || sepIdx >= len(normalized)-3 {
		return normalized
	}
	prefix := strings.TrimSpace(normalized[:sepIdx])
	suffix := strings.TrimSpace(normalized[sepIdx+3:])
	if suffix == "" {
		return normalized
	}
	// If prefix contains a year-like pattern, return just the suffix
	if animeonYearRe.MatchString(prefix) {
		return suffix
	}
	return normalized
}

// animeonYearRe matches a 4-digit year (19xx or 20xx). Pre-compiled — was
// re-built inside the title-normalization helper called per search hit.
var animeonYearRe = regexp.MustCompile(`(19|20)\d{2}`)

func detectAnimeonQualityTag(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	if animeonQuality4kRe.MatchString(value) {
		return "[4K]"
	}
	if animeonQualityFhdRe.MatchString(value) {
		return "[FHD]"
	}
	return ""
}

func extractPlayerFileArray(htmlStr string) string {
	searchIdx := 0
	for searchIdx < len(htmlStr) {
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

		// Skip optional quote char
		if startIdx < len(htmlStr) && (htmlStr[startIdx] == '\'' || htmlStr[startIdx] == '"') {
			startIdx++
			for startIdx < len(htmlStr) && (htmlStr[startIdx] == ' ' || htmlStr[startIdx] == '\t') {
				startIdx++
			}
		}

		if startIdx >= len(htmlStr) || htmlStr[startIdx] != '[' {
			searchIdx = fileIdx + 4
			continue
		}

		return extractBracketArray(htmlStr, startIdx)
	}
	return ""
}

func extractBracketArray(text string, startIdx int) string {
	if startIdx < 0 || startIdx >= len(text) || text[startIdx] != '[' {
		return ""
	}

	depth := 0
	inString := false
	escaped := false
	var quoteChar byte

	for i := startIdx; i < len(text); i++ {
		ch := text[i]

		if inString {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == quoteChar {
				inString = false
				quoteChar = 0
			}
			continue
		}

		if ch == '"' || ch == '\'' {
			inString = true
			quoteChar = ch
			continue
		}

		if ch == '[' {
			depth++
			continue
		}
		if ch == ']' {
			depth--
			if depth == 0 {
				return text[startIdx : i+1]
			}
		}
	}
	return ""
}

func intOrZero(s string) int {
	v, _ := strconv.Atoi(s)
	return v
}
