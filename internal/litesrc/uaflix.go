package litesrc

import (
	stdjson "encoding/json"
	"fmt"
	"html"
	"io"
	"maps"
	"math"
	"net/http"
	"net/http/cookiejar"
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

	"github.com/rs/zerolog/log"
)

// uaflixChecker implements the Uaflix Ukrainian balancer.
// Complex multi-player aggregator: DLE search, ashdi/zetvideo player parsing,
// serial structure aggregation with multiple player types.
// Supports DLE cookie-based authentication for accessing player iframes.
type uaflixChecker struct {
	client *http.Client
	host   string
	login  string
	passwd string
	cookie string // manual cookie override

	// DLE auth state
	authMu     sync.Mutex
	authCookie string
	authExpiry time.Time

	// Cache for aggregated serial structures
	structureCache sync.Map // map[string]*uaflixCachedStructure
	// Cache for episode player probes
	probeCache sync.Map // map[string]*uaflixProbeResult
}

type uaflixCachedStructure struct {
	data    *uaflixSerialStructure
	created time.Time
}

type uaflixProbeResult struct {
	iframeURL  string
	playerType string
	created    time.Time
}

// --- Models ---

type uaflixSearchResult struct {
	title      string
	urlStr     string
	year       int
	posterURL  string
	category   string
	isAnime    bool
	matchScore int
	titleMatch bool
	yearMatch  bool
}

type uaflixEpisodeInfo struct {
	number   int
	title    string
	file     string
	id       string
	subtitle string
}

type uaflixVoiceInfo struct {
	name        string
	playerType  string
	displayName string
	seasons     map[int][]uaflixEpisodeInfo
}

type uaflixSerialStructure struct {
	voices map[string]*uaflixVoiceInfo
}

type uaflixPlayStream struct {
	link    string
	quality string
	title   string
}

type uaflixEpisodeLinkInfo struct {
	url        string
	title      string
	season     int
	episode    int
	playerType string
	iframeURL  string
}

// --- Regexes ---

var (
	uaflixSresWrapRe       = regexp.MustCompile(`(?is)<a[^>]*class="[^"]*sres-wrap[^"]*"[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)
	uaflixSresH2Re         = regexp.MustCompile(`(?is)<h[23][^>]*>(.*?)</h[23]>`)
	uaflixSresDescRe       = regexp.MustCompile(`(?is)<div[^>]*class="[^"]*sres-desc[^"]*"[^>]*>(.*?)</div>`)
	uaflixSresPosterRe     = regexp.MustCompile(`(?is)<img[^>]*(?:src|data-src)="([^"]+)"`)
	uaflixYearRe           = regexp.MustCompile(`((?:19|20)\d{2})`)
	uaflixIframeRe         = regexp.MustCompile(`(?is)<iframe[^>]*(?:src|data-src)="([^"]+)"`)
	uaflixVideoBoxIframeRe = regexp.MustCompile(`(?is)<div[^>]*class="[^"]*video-box[^"]*"[^>]*>.*?<iframe[^>]*(?:src|data-src)="([^"]+)"`)
	uaflixOgVideoRe        = regexp.MustCompile(`(?is)<meta[^>]*property="og:video:iframe"[^>]*content="([^"]*)"`)
	uaflixOgVideoSrcRe     = regexp.MustCompile(`(?is)src=['"]([^'"]+)['"]`)
	uaflixSeasonLinksRe    = regexp.MustCompile(`(?is)<div[^>]*class="[^"]*(?:sez-wr|fss-box)[^"]*"[^>]*>(.*?)</div>`)
	uaflixSeasonAnchorRe   = regexp.MustCompile(`(?is)<a[^>]*href="([^"]+)"`)
	uaflixEpLinkRe         = regexp.MustCompile(`(?is)<a[^>]*class="[^"]*vi-img[^"]*"[^>]*href="([^"]+)"`)
	uaflixSeasonEpRe       = regexp.MustCompile(`season-(\d+).*?episode-(\d+)`)
	uaflixFileJSONRe       = regexp.MustCompile(`(?s)file\s*:\s*'(\[.+?\])'`)
	uaflixFileM3u8Re       = regexp.MustCompile(`(?is)file\s*:\s*"([^"]+\.m3u8)"`)
	uaflixFileM3u8Re2      = regexp.MustCompile(`(?is)file\s*:\s*'([^']+\.m3u8)'`)
	uaflixVideoSrcRe       = regexp.MustCompile(`(?is)<video[^>]*src="([^"]+)"`)
	uaflixSeasonNumRe      = regexp.MustCompile(`(?i)Сезон\s+(\d+)`)
	uaflixHTMLTagRe        = regexp.MustCompile(`<[^>]+>`)
	uaflixQuality4kRe      = regexp.MustCompile(`(?i)(^|[^0-9])(2160p?)([^0-9]|$)|\b4k\b|\buhd\b`)
	uaflixQualityFhdRe     = regexp.MustCompile(`(?i)(^|[^0-9])(1080p?)([^0-9]|$)|\bfhd\b`)
	uaflixLampacArgsRe     = regexp.MustCompile(`(?i)([?&])(account_email|uid|nws_id)=[^&]*`)
	uaflixTitleNormRe      = regexp.MustCompile(`[^\p{L}\p{Nd}\s]+`)
	uaflixTitleExtraRe     = regexp.MustCompile(`(?i)\b(season|сезон|частина|part|ova|special|movie|film)\b`)
	uaflixMultiSpaceRe     = regexp.MustCompile(`\s+`)
	uaflixAshdiSerialKeyRe = regexp.MustCompile(`(https://ashdi\.vip/serial/\d+)`)
)

func NewUaflixChecker(cfg config.Config) *uaflixChecker {
	host := strings.TrimSpace(cfg.Online.Uaflix.Host)
	if host == "" {
		host = "https://uafix.net"
	}
	host = strings.TrimRight(host, "/")

	c := &uaflixChecker{
		client: httpclient.New(15 * time.Second),
		host:   host,
		login:  strings.TrimSpace(cfg.Online.Uaflix.Login),
		passwd: strings.TrimSpace(cfg.Online.Uaflix.Passwd),
		cookie: strings.TrimSpace(cfg.Online.Uaflix.Cookie),
	}

	// Start background DLE auth if credentials are configured.
	if c.login != "" && c.passwd != "" {
		go c.dleLogin()
	}

	return c
}

func (c *uaflixChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show := c.checkSearch(req)
			writeCheckSearchResponse(w, show, pluginQualityBadgeGet("uaflix"))
			return
		}
		c.index(w, req, links)
	}
}

// --- Main handler ---

func (c *uaflixChecker) index(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	originalLang := strings.TrimSpace(q.Get("original_language"))
	source := strings.TrimSpace(q.Get("source"))
	imdbID := strings.TrimSpace(q.Get("imdb_id"))
	kpID := strings.TrimSpace(q.Get("kinopoisk_id"))
	yearStr := strings.TrimSpace(q.Get("year"))
	year, _ := strconv.Atoi(yearStr)
	serial, _ := getsTVQueryInt(q.Get("serial"))
	seasonNum, seasonSet := getsTVQueryInt(q.Get("s"))
	if !seasonSet {
		seasonNum = -1
	}
	t := strings.TrimSpace(q.Get("t"))
	href := strings.TrimSpace(q.Get("href"))
	episodeURL := strings.TrimSpace(q.Get("episode_url"))
	host := hostFromRequest(req)

	// Handle episode_url (call method for vod episodes)
	if episodeURL != "" {
		streams := c.parseEpisode(req, episodeURL)
		if len(streams) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}
		streamURL := uaflixStripLampacArgs(streams[0].link)
		streamURL = uaflixProxyStreamURL(req, streamURL, "uaflix", links)
		writeJSON(w, http.StatusOK, map[string]any{
			"method": "play",
			"url":    streamURL,
			"title":  title,
		})
		return
	}

	// Search for film URL
	if href == "" {
		results := c.searchAll(req, title, originalTitle, year, serial, originalLang, source)
		if len(results) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}

		// Auto-select best match
		best := c.selectBestResult(results, title, originalTitle, year)
		if best != nil {
			href = best.urlStr
		} else if len(results) == 1 {
			href = results[0].urlStr
		} else {
			c.writeSimilar(w, rjson, host, results, imdbID, kpID, title, originalTitle, yearStr, serial)
			return
		}
	}

	if serial == 1 {
		c.writeSerial(w, req, rjson, host, href, title, originalTitle, imdbID, kpID, yearStr, t, seasonNum, links)
	} else {
		c.writeMovie(w, req, rjson, title, originalTitle, href, links)
	}
}

// --- Search ---

func (c *uaflixChecker) checkSearch(req *http.Request) bool {
	q := req.URL.Query()
	title := strings.TrimSpace(q.Get("title"))
	if title == "" {
		title = strings.TrimSpace(q.Get("original_title"))
	}
	if title == "" {
		return false
	}

	searchURL := fmt.Sprintf("%s/index.php?do=search&subaction=search&story=%s",
		c.host, url.QueryEscape(title))
	body, ok := c.fetch(req, searchURL, c.host)
	if !ok {
		log.Debug().Str("url", searchURL).Msg("uaflix: checkSearch fetch failed")
		return false
	}
	htmlStr := string(body)
	hasSres := strings.Contains(htmlStr, "sres-wrap") ||
		strings.Contains(htmlStr, "sres-item") ||
		strings.Contains(htmlStr, "search-results")
	log.Debug().Bool("result", hasSres).Int("bodyLen", len(body)).Str("title", title).Msg("uaflix: checkSearch")
	return hasSres
}

func (c *uaflixChecker) searchAll(req *http.Request, title, originalTitle string, year, serial int, originalLang, source string) []*uaflixSearchResult {
	// Deduplicate queries
	queries := make([]string, 0, 2)
	seen := make(map[string]struct{})
	for _, q := range []string{originalTitle, title} {
		q = strings.TrimSpace(q)
		if q == "" {
			continue
		}
		lower := strings.ToLower(q)
		if _, ok := seen[lower]; ok {
			continue
		}
		seen[lower] = struct{}{}
		queries = append(queries, q)
	}
	if len(queries) == 0 {
		return nil
	}

	uniqueByURL := make(map[string]*uaflixSearchResult)
	for _, query := range queries {
		searchURL := fmt.Sprintf("%s/index.php?do=search&subaction=search&story=%s",
			c.host, url.QueryEscape(query))
		body, ok := c.fetch(req, searchURL, c.host)
		if !ok {
			continue
		}
		results := c.parseSearchHTML(string(body))
		for _, r := range results {
			if _, exists := uniqueByURL[r.urlStr]; !exists {
				uniqueByURL[r.urlStr] = r
			}
		}
	}

	if len(uniqueByURL) == 0 {
		return nil
	}

	// Filter by content type
	allowAnime := uaflixIsAnimeRequest(title, originalTitle, originalLang, source)
	results := make([]*uaflixSearchResult, 0, len(uniqueByURL))
	for _, r := range uniqueByURL {
		results = append(results, r)
	}
	results = uaflixFilterByContentType(results, serial, allowAnime)
	if len(results) == 0 {
		return nil
	}

	// Score results
	for _, r := range results {
		r.titleMatch = uaflixHasStrongTitleMatch(r, title, originalTitle)
		r.yearMatch = year > 0 && r.year == year
		r.matchScore = uaflixBuildMatchScore(r, title, originalTitle, year, serial, allowAnime)
	}

	// Sort by score
	sort.Slice(results, func(i, j int) bool {
		if results[i].matchScore != results[j].matchScore {
			return results[i].matchScore > results[j].matchScore
		}
		if results[i].titleMatch != results[j].titleMatch {
			return results[i].titleMatch
		}
		if results[i].yearMatch != results[j].yearMatch {
			return results[i].yearMatch
		}
		return results[i].title < results[j].title
	})

	return results
}

func (c *uaflixChecker) parseSearchHTML(htmlStr string) []*uaflixSearchResult {
	matches := uaflixSresWrapRe.FindAllStringSubmatch(htmlStr, -1)
	if len(matches) == 0 {
		return nil
	}

	var results []*uaflixSearchResult
	for _, m := range matches {
		filmURL := strings.TrimSpace(m[1])
		block := m[2]

		if filmURL == "" {
			continue
		}
		filmURL = html.UnescapeString(filmURL)
		if !strings.HasPrefix(filmURL, "http") {
			filmURL = c.host + filmURL
		}

		// Extract title from h2/h3
		h2 := uaflixSresH2Re.FindStringSubmatch(block)
		if len(h2) < 2 {
			continue
		}
		itemTitle := uaflixCleanText(h2[1])
		if itemTitle == "" {
			continue
		}

		// Extract year from description
		filmYear := 0
		if desc := uaflixSresDescRe.FindStringSubmatch(block); len(desc) >= 2 {
			filmYear = uaflixExtractYear(desc[1])
		}

		// Extract poster
		poster := ""
		if pm := uaflixSresPosterRe.FindStringSubmatch(block); len(pm) >= 2 {
			poster = strings.TrimSpace(pm[1])
			if poster != "" && !strings.HasPrefix(poster, "http") {
				poster = c.host + poster
			}
		}

		category := uaflixExtractCategoryFromURL(filmURL)
		results = append(results, &uaflixSearchResult{
			title:     itemTitle,
			urlStr:    filmURL,
			year:      filmYear,
			posterURL: poster,
			category:  category,
			isAnime:   strings.EqualFold(category, "anime"),
		})
	}

	return results
}

func (c *uaflixChecker) selectBestResult(results []*uaflixSearchResult, title, originalTitle string, year int) *uaflixSearchResult {
	if len(results) <= 1 {
		return nil // caller handles single-result case
	}

	if year > 0 {
		var strict []*uaflixSearchResult
		for _, r := range results {
			if r.titleMatch && r.yearMatch {
				strict = append(strict, r)
			}
		}
		if len(strict) == 1 {
			return strict[0]
		}
	} else {
		var titleOnly []*uaflixSearchResult
		for _, r := range results {
			if r.titleMatch {
				titleOnly = append(titleOnly, r)
			}
		}
		if len(titleOnly) == 1 {
			return titleOnly[0]
		}
	}

	return nil
}

// --- Similar items ---

func (c *uaflixChecker) writeSimilar(w http.ResponseWriter, rjson bool, host string, results []*uaflixSearchResult, imdbID, kpID, title, originalTitle, yearStr string, serial int) {
	data := make([]map[string]any, 0, len(results))
	for _, res := range results {
		link := fmt.Sprintf("%s/lite/uaflix?imdb_id=%s&kinopoisk_id=%s&title=%s&original_title=%s&year=%s&serial=%d&href=%s",
			host, url.QueryEscape(imdbID), url.QueryEscape(kpID),
			url.QueryEscape(title), url.QueryEscape(originalTitle),
			url.QueryEscape(yearStr), serial, url.QueryEscape(res.urlStr))
		yr := ""
		if res.year > 0 {
			yr = strconv.Itoa(res.year)
		}

		details := ""
		switch res.category {
		case "films", "film":
			details = "Фільм"
		case "serials", "serial":
			details = "Серіал"
		case "anime":
			details = "Аніме"
		}

		data = append(data, map[string]any{
			"title":   res.title,
			"year":    yr,
			"details": details,
			"url":     link,
			"poster":  res.posterURL,
		})
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "similar", "data": data})
	} else {
		var sb strings.Builder
		sb.WriteString(`<div class="videos__line">`)
		for _, item := range data {
			titleStr := item["title"].(string)
			if yr, ok := item["year"].(string); ok && yr != "" {
				titleStr += " (" + yr + ")"
			}
			sb.WriteString(fmt.Sprintf(`<a class="videos__button selector" href="%s">%s</a> `, item["url"], titleStr))
		}
		sb.WriteString(`</div>`)
		writeHTML(w, http.StatusOK, sb.String())
	}
}

// --- Movie ---

func (c *uaflixChecker) writeMovie(w http.ResponseWriter, req *http.Request, rjson bool, title, originalTitle, href string, links *proxylink.Manager) {
	streams := c.parseEpisode(req, href)
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

		streamURL := uaflixStripLampacArgs(s.link)
		streamURL = uaflixProxyStreamURL(req, streamURL, "uaflix", links)

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

// --- Serial ---

func (c *uaflixChecker) writeSerial(w http.ResponseWriter, req *http.Request, rjson bool, host, href, title, originalTitle, imdbID, kpID, yearStr, t string, seasonNum int, links *proxylink.Manager) {
	structure := c.aggregateSerialStructure(req, href)
	if structure == nil || len(structure.voices) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if seasonNum == -1 {
		c.writeSeasons(w, rjson, host, structure, href, title, originalTitle, imdbID, kpID, yearStr, t)
		return
	}

	c.writeEpisodes(w, req, rjson, host, structure, href, title, originalTitle, imdbID, kpID, yearStr, t, seasonNum, links)
}

func (c *uaflixChecker) writeSeasons(w http.ResponseWriter, rjson bool, host string, structure *uaflixSerialStructure, href, title, originalTitle, imdbID, kpID, yearStr, t string) {
	// Collect all seasons
	seasonSet := make(map[int]struct{})

	// If voice is selected and it's an ashdi voice, restrict to its seasons
	restrictByVoice := false
	if t != "" {
		if v, ok := structure.voices[t]; ok && uaflixIsAshdiVoice(v) {
			restrictByVoice = true
			for sn, eps := range v.seasons {
				if uaflixHasValidEpisodes(eps) {
					seasonSet[sn] = struct{}{}
				}
			}
		}
	}

	if !restrictByVoice {
		for _, v := range structure.voices {
			for sn, eps := range v.seasons {
				if uaflixHasValidEpisodes(eps) {
					seasonSet[sn] = struct{}{}
				}
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
		link := fmt.Sprintf("%s/lite/uaflix?imdb_id=%s&kinopoisk_id=%s&title=%s&original_title=%s&year=%s&serial=1&s=%d&href=%s",
			host, url.QueryEscape(imdbID), url.QueryEscape(kpID),
			url.QueryEscape(title), url.QueryEscape(originalTitle),
			url.QueryEscape(yearStr), sn, url.QueryEscape(href))
		if restrictByVoice && t != "" {
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
}

func (c *uaflixChecker) writeEpisodes(w http.ResponseWriter, req *http.Request, rjson bool, host string, structure *uaflixSerialStructure, href, title, originalTitle, imdbID, kpID, yearStr, t string, seasonNum int, links *proxylink.Manager) {
	// Get voices that have this season
	type voiceEntry struct {
		key  string
		info *uaflixVoiceInfo
	}

	var voicesForSeason []voiceEntry
	for name, v := range structure.voices {
		if _, ok := v.seasons[seasonNum]; ok {
			voicesForSeason = append(voicesForSeason, voiceEntry{name, v})
		}
	}

	if len(voicesForSeason) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Sort voices for consistent order
	sort.Slice(voicesForSeason, func(i, j int) bool {
		return voicesForSeason[i].key < voicesForSeason[j].key
	})

	// Auto-select first voice if not specified
	if t == "" {
		t = voicesForSeason[0].key
	} else {
		found := false
		for _, v := range voicesForSeason {
			if v.key == t {
				found = true
				break
			}
		}
		if !found {
			t = voicesForSeason[0].key
		}
	}

	selectedVoice := structure.voices[t]
	if selectedVoice == nil {
		writeGetsTVEmpty(w, rjson)
		return
	}

	selectedSeasonSet := uaflixGetSeasonSet(selectedVoice)
	selectedIsAshdi := uaflixIsAshdiVoice(selectedVoice)

	// Voice buttons
	voiceData := make([]map[string]any, 0, len(voicesForSeason))
	for _, v := range voicesForSeason {
		targetIsAshdi := uaflixIsAshdiVoice(v.info)
		targetSeasonSet := uaflixGetSeasonSet(v.info)
		sameSeasonSet := uaflixSetsEqual(selectedSeasonSet, targetSeasonSet)
		needSeasonReset := (selectedIsAshdi || targetIsAshdi) && !sameSeasonSet

		voiceLink := fmt.Sprintf("%s/lite/uaflix?imdb_id=%s&kinopoisk_id=%s&title=%s&original_title=%s&year=%s&serial=1&href=%s",
			host, url.QueryEscape(imdbID), url.QueryEscape(kpID),
			url.QueryEscape(title), url.QueryEscape(originalTitle),
			url.QueryEscape(yearStr), url.QueryEscape(href))

		if needSeasonReset {
			voiceLink += fmt.Sprintf("&s=-1&t=%s", url.QueryEscape(v.key))
		} else {
			voiceLink += fmt.Sprintf("&s=%d&t=%s", seasonNum, url.QueryEscape(v.key))
		}

		voiceData = append(voiceData, map[string]any{
			"name":   v.info.displayName,
			"active": v.key == t,
			"url":    voiceLink,
		})
	}

	episodes := selectedVoice.seasons[seasonNum]
	if len(episodes) == 0 {
		// If ashdi voice and season not found, redirect to season selector
		if uaflixIsAshdiVoice(selectedVoice) {
			redirectURL := fmt.Sprintf("%s/lite/uaflix?imdb_id=%s&kinopoisk_id=%s&title=%s&original_title=%s&year=%s&serial=1&s=-1&t=%s&href=%s",
				host, url.QueryEscape(imdbID), url.QueryEscape(kpID),
				url.QueryEscape(title), url.QueryEscape(originalTitle),
				url.QueryEscape(yearStr), url.QueryEscape(t), url.QueryEscape(href))
			http.Redirect(w, req, redirectURL, http.StatusFound)
			return
		}
		writeGetsTVEmpty(w, rjson)
		return
	}

	sort.Slice(episodes, func(i, j int) bool { return episodes[i].number < episodes[j].number })

	baseTitle := getsTVJoinName(title, originalTitle)
	isVod := selectedVoice.playerType == "zetvideo-vod" || selectedVoice.playerType == "ashdi-vod"

	data := make([]map[string]any, 0, len(episodes))
	labels := make([]string, 0, len(episodes))
	seasons := make([]int, 0, len(episodes))
	epNums := make([]int, 0, len(episodes))

	for _, ep := range episodes {
		epTitle := ep.title
		if epTitle == "" {
			epTitle = fmt.Sprintf("Серія %d", ep.number)
		}

		if isVod && ep.file != "" {
			// For vod episodes: use call method — episode_url points to the page
			callURL := fmt.Sprintf("%s/lite/uaflix?episode_url=%s&imdb_id=%s&kinopoisk_id=%s&title=%s&original_title=%s&year=%s&serial=1&s=%d&e=%d",
				host, url.QueryEscape(ep.file),
				url.QueryEscape(imdbID), url.QueryEscape(kpID),
				url.QueryEscape(title), url.QueryEscape(originalTitle),
				url.QueryEscape(yearStr), seasonNum, ep.number)

			data = append(data, map[string]any{
				"method": "call",
				"url":    callURL,
				"stream": callURL + "&play=true",
				"s":      seasonNum,
				"e":      ep.number,
				"name":   epTitle,
				"title":  fmt.Sprintf("%s (%s)", baseTitle, epTitle),
			})
		} else {
			// For serial player episodes: direct m3u8 links
			streamURL := uaflixStripLampacArgs(ep.file)
			streamURL = uaflixProxyStreamURL(req, streamURL, "uaflix", links)

			data = append(data, map[string]any{
				"method": "play",
				"url":    streamURL,
				"stream": streamURL,
				"s":      seasonNum,
				"e":      ep.number,
				"name":   epTitle,
				"title":  fmt.Sprintf("%s (%s)", baseTitle, epTitle),
			})
		}

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

// --- Serial structure aggregation ---

func (c *uaflixChecker) aggregateSerialStructure(req *http.Request, serialURL string) *uaflixSerialStructure {
	// Check cache
	if cached, ok := c.structureCache.Load(serialURL); ok {
		entry := cached.(*uaflixCachedStructure)
		if time.Since(entry.created) < 40*time.Minute {
			return entry.data
		}
		c.structureCache.Delete(serialURL)
	}

	episodes := c.getPaginationEpisodes(req, serialURL)

	structure := &uaflixSerialStructure{
		voices: make(map[string]*uaflixVoiceInfo),
	}

	serialPlayersProcessed := make(map[string]struct{})

	if len(episodes) > 0 {
		// Group episodes by season
		episodesBySeason := make(map[int][]*uaflixEpisodeLinkInfo)
		for _, ep := range episodes {
			episodesBySeason[ep.season] = append(episodesBySeason[ep.season], ep)
		}

		for season, seasonEps := range episodesBySeason {
			sort.Slice(seasonEps, func(i, j int) bool { return seasonEps[i].episode < seasonEps[j].episode })

			// Probe first episode to determine player type
			iframeURL, playerType := c.probeSeasonPlayer(req, seasonEps)
			if playerType == "" || playerType == "trailer" {
				continue
			}

			if playerType == "ashdi-serial" || playerType == "zetvideo-serial" {
				serialKey := uaflixNormalizeSerialPlayerKey(playerType, iframeURL)
				if _, done := serialPlayersProcessed[serialKey]; done {
					continue
				}
				serialPlayersProcessed[serialKey] = struct{}{}

				voices := c.parseMultiEpisodePlayer(req, iframeURL, playerType)
				c.mergeVoices(structure, voices)
				continue
			}

			if playerType == "ashdi-vod" || playerType == "zetvideo-vod" {
				c.addVodSeasonEpisodes(structure, playerType, season, seasonEps)
				continue
			}
		}
	} else {
		// No pagination, fallback to page iframe
		iframeURL, playerType := c.probeEpisodePlayer(req, serialURL)
		if playerType == "" || playerType == "trailer" {
			return nil
		}

		if playerType == "ashdi-serial" || playerType == "zetvideo-serial" {
			voices := c.parseMultiEpisodePlayer(req, iframeURL, playerType)
			c.mergeVoices(structure, voices)
		} else if playerType == "ashdi-vod" || playerType == "zetvideo-vod" {
			syntheticEps := []*uaflixEpisodeLinkInfo{{
				url:     serialURL,
				title:   "Епізод 1",
				season:  1,
				episode: 1,
			}}
			c.addVodSeasonEpisodes(structure, playerType, 1, syntheticEps)
		}
	}

	if len(structure.voices) == 0 {
		return nil
	}

	// Check episodes exist
	hasEpisodes := false
	for _, v := range structure.voices {
		for _, eps := range v.seasons {
			if len(eps) > 0 {
				hasEpisodes = true
				break
			}
		}
		if hasEpisodes {
			break
		}
	}
	if !hasEpisodes {
		return nil
	}

	uaflixNormalizeVoiceNames(structure)

	// Cache
	c.structureCache.Store(serialURL, &uaflixCachedStructure{
		data:    structure,
		created: time.Now(),
	})

	return structure
}

func (c *uaflixChecker) getPaginationEpisodes(req *http.Request, filmURL string) []*uaflixEpisodeLinkInfo {
	body, ok := c.fetch(req, filmURL, c.host)
	if !ok {
		return nil
	}

	htmlStr := string(body)

	// Find season page URLs
	seasonURLs := make(map[string]struct{})

	seasonBlocks := uaflixSeasonLinksRe.FindAllStringSubmatch(htmlStr, -1)
	for _, block := range seasonBlocks {
		anchors := uaflixSeasonAnchorRe.FindAllStringSubmatch(block[1], -1)
		for _, a := range anchors {
			pageURL := strings.TrimSpace(html.UnescapeString(a[1]))
			if pageURL != "" {
				if !strings.HasPrefix(pageURL, "http") {
					pageURL = c.host + pageURL
				}
				seasonURLs[pageURL] = struct{}{}
			}
		}
	}

	if len(seasonURLs) == 0 {
		seasonURLs[filmURL] = struct{}{}
	}

	// Fetch all season pages concurrently
	pages := make([]string, 0, len(seasonURLs))
	for u := range seasonURLs {
		pages = append(pages, u)
	}

	var mu sync.Mutex
	var allEpisodes []*uaflixEpisodeLinkInfo

	var wg sync.WaitGroup
	for _, pageURL := range pages {
		wg.Add(1)
		go func(u string) {
			defer wg.Done()
			pageBody, ok := c.fetch(req, u, c.host)
			if !ok {
				return
			}
			eps := c.parseEpisodeLinks(string(pageBody))
			if len(eps) > 0 {
				mu.Lock()
				allEpisodes = append(allEpisodes, eps...)
				mu.Unlock()
			}
		}(pageURL)
	}
	wg.Wait()

	// Also parse main page if it wasn't in seasonURLs
	if _, inSet := seasonURLs[filmURL]; !inSet {
		eps := c.parseEpisodeLinks(htmlStr)
		allEpisodes = append(allEpisodes, eps...)
	}

	sort.Slice(allEpisodes, func(i, j int) bool {
		if allEpisodes[i].season != allEpisodes[j].season {
			return allEpisodes[i].season < allEpisodes[j].season
		}
		return allEpisodes[i].episode < allEpisodes[j].episode
	})

	return allEpisodes
}

func (c *uaflixChecker) parseEpisodeLinks(htmlStr string) []*uaflixEpisodeLinkInfo {
	var episodes []*uaflixEpisodeLinkInfo

	linkMatches := uaflixEpLinkRe.FindAllStringSubmatch(htmlStr, -1)
	for _, lm := range linkMatches {
		epURL := strings.TrimSpace(html.UnescapeString(lm[1]))
		if epURL == "" {
			continue
		}
		if !strings.HasPrefix(epURL, "http") {
			epURL = c.host + epURL
		} else {
			// Detach from HTML buffer — epURL is cached downstream via
			// structureCache (40 min TTL) and would otherwise pin the
			// whole page HTML through a small substring reference.
			epURL = strings.Clone(epURL)
		}

		seMatch := uaflixSeasonEpRe.FindStringSubmatch(epURL)
		if len(seMatch) < 3 {
			continue
		}

		season, _ := strconv.Atoi(seMatch[1])
		episode, _ := strconv.Atoi(seMatch[2])

		episodes = append(episodes, &uaflixEpisodeLinkInfo{
			url:     epURL,
			title:   fmt.Sprintf("Епізод %d", episode),
			season:  season,
			episode: episode,
		})
	}

	return episodes
}

// --- Player probing ---

func (c *uaflixChecker) probeSeasonPlayer(req *http.Request, seasonEpisodes []*uaflixEpisodeLinkInfo) (iframeURL, playerType string) {
	sorted := make([]*uaflixEpisodeLinkInfo, len(seasonEpisodes))
	copy(sorted, seasonEpisodes)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].episode < sorted[j].episode })

	for _, ep := range sorted {
		if ep.url == "" {
			continue
		}
		iframe, pt := c.probeEpisodePlayer(req, ep.url)
		ep.iframeURL = iframe
		ep.playerType = pt
		if pt == "" || pt == "trailer" {
			continue
		}
		return iframe, pt
	}
	return "", ""
}

func (c *uaflixChecker) probeEpisodePlayer(req *http.Request, pageURL string) (iframeURL, playerType string) {
	if cached, ok := c.probeCache.Load(pageURL); ok {
		entry := cached.(*uaflixProbeResult)
		if time.Since(entry.created) < 20*time.Minute {
			return entry.iframeURL, entry.playerType
		}
		c.probeCache.Delete(pageURL)
	}

	body, ok := c.fetch(req, pageURL, c.host)
	if !ok {
		return "", ""
	}

	iframe := c.extractIframeURL(string(body))
	pt := uaflixDeterminePlayerType(iframe)

	// strings.Clone: regex substrings share the underlying HTML buffer
	// (~100KB–1MB). Without cloning, caching the tiny iframe URL pins the
	// whole HTML body in heap for the cache TTL.
	c.probeCache.Store(pageURL, &uaflixProbeResult{
		iframeURL:  strings.Clone(iframe),
		playerType: pt,
		created:    time.Now(),
	})

	return iframe, pt
}

func (c *uaflixChecker) extractIframeURL(htmlStr string) string {
	// Try video-box iframe first
	if m := uaflixVideoBoxIframeRe.FindStringSubmatch(htmlStr); len(m) >= 2 {
		return uaflixNormalizeIframeURL(m[1])
	}

	// Try any iframe
	if m := uaflixIframeRe.FindStringSubmatch(htmlStr); len(m) >= 2 {
		return uaflixNormalizeIframeURL(m[1])
	}

	// Try og:video:iframe meta
	if m := uaflixOgVideoRe.FindStringSubmatch(htmlStr); len(m) >= 2 {
		content := html.UnescapeString(m[1])
		if srcM := uaflixOgVideoSrcRe.FindStringSubmatch(content); len(srcM) >= 2 {
			return uaflixNormalizeIframeURL(srcM[1])
		}
		return uaflixNormalizeIframeURL(content)
	}

	return ""
}

// --- Multi-episode player parsing ---

func (c *uaflixChecker) parseMultiEpisodePlayer(req *http.Request, iframeURL, playerType string) []*uaflixVoiceInfo {
	requestURL := iframeURL
	// For ashdi serial, use base URL without parameters
	if playerType == "ashdi-serial" && strings.Contains(iframeURL, "ashdi.vip/serial/") {
		if m := uaflixAshdiSerialKeyRe.FindStringSubmatch(iframeURL); len(m) >= 2 {
			requestURL = m[1]
		}
	}

	referer := "https://uafix.net/"
	body, ok := c.fetch(req, requestURL, referer)
	if !ok {
		return nil
	}

	htmlStr := string(body)

	// Find JSON array in file:'[...]'
	m := uaflixFileJSONRe.FindStringSubmatch(htmlStr)
	if len(m) < 2 {
		return nil
	}

	jsonStr := strings.ReplaceAll(m[1], "\\'", "'")
	jsonStr = strings.ReplaceAll(jsonStr, "\\\"", "\"")

	var voicesArray []map[string]any
	if err := stdjson.Unmarshal([]byte(jsonStr), &voicesArray); err != nil {
		return nil
	}

	var voices []*uaflixVoiceInfo
	voiceCounts := make(map[string]int)

	for _, voiceObj := range voicesArray {
		voiceName := uaflixMapStr(voiceObj, "title")
		if voiceName == "" {
			continue
		}

		// Handle duplicate voice names
		originalName := voiceName
		if count, exists := voiceCounts[originalName]; exists {
			voiceCounts[originalName] = count + 1
			voiceName = fmt.Sprintf("%s %d", voiceName, count+1)
		} else {
			voiceCounts[originalName] = 1
		}

		voice := &uaflixVoiceInfo{
			name:        originalName,
			playerType:  playerType,
			displayName: voiceName,
			seasons:     make(map[int][]uaflixEpisodeInfo),
		}

		if folder, ok := voiceObj["folder"]; ok {
			if seasons, ok := folder.([]any); ok {
				for _, seasonRaw := range seasons {
					seasonObj, ok := seasonRaw.(map[string]any)
					if !ok {
						continue
					}
					seasonTitle := uaflixMapStr(seasonObj, "title")
					snMatch := uaflixSeasonNumRe.FindStringSubmatch(seasonTitle)
					if len(snMatch) < 2 {
						continue
					}
					seasonNum, _ := strconv.Atoi(snMatch[1])

					var episodes []uaflixEpisodeInfo
					if epFolder, ok := seasonObj["folder"]; ok {
						if epArray, ok := epFolder.([]any); ok {
							epNum := 1
							for _, epRaw := range epArray {
								epObj, ok := epRaw.(map[string]any)
								if !ok {
									continue
								}
								episodes = append(episodes, uaflixEpisodeInfo{
									number:   epNum,
									title:    uaflixMapStr(epObj, "title"),
									file:     uaflixMapStr(epObj, "file"),
									id:       uaflixMapStr(epObj, "id"),
									subtitle: uaflixMapStr(epObj, "subtitle"),
								})
								epNum++
							}
						}
					}

					voice.seasons[seasonNum] = episodes
				}
			}
		}

		voices = append(voices, voice)
	}

	return voices
}

func (c *uaflixChecker) mergeVoices(structure *uaflixSerialStructure, voices []*uaflixVoiceInfo) {
	for _, v := range voices {
		if v == nil || v.displayName == "" {
			continue
		}
		if existing, ok := structure.voices[v.displayName]; ok {
			maps.Copy(existing.seasons, v.seasons)
		} else {
			structure.voices[v.displayName] = v
		}
	}
}

func (c *uaflixChecker) addVodSeasonEpisodes(structure *uaflixSerialStructure, playerType string, season int, seasonEps []*uaflixEpisodeLinkInfo) {
	displayName := "Uaflix #2"
	if playerType == "ashdi-vod" {
		displayName = "Uaflix #3"
	}

	if _, ok := structure.voices[displayName]; !ok {
		structure.voices[displayName] = &uaflixVoiceInfo{
			name:        "Uaflix",
			playerType:  playerType,
			displayName: displayName,
			seasons:     make(map[int][]uaflixEpisodeInfo),
		}
	}

	episodes := make([]uaflixEpisodeInfo, 0, len(seasonEps))
	for _, ep := range seasonEps {
		episodes = append(episodes, uaflixEpisodeInfo{
			number: ep.episode,
			title:  ep.title,
			file:   ep.url, // For vod, file = page URL (resolved via episode_url)
			id:     ep.url,
		})
	}

	sort.Slice(episodes, func(i, j int) bool { return episodes[i].number < episodes[j].number })
	structure.voices[displayName].seasons[season] = episodes
}

// --- Episode parsing (for movie and vod episode resolution) ---

func (c *uaflixChecker) parseEpisode(req *http.Request, pageURL string) []uaflixPlayStream {
	body, ok := c.fetch(req, pageURL, c.host)
	if !ok {
		return nil
	}

	htmlStr := string(body)

	// Try <video> tag first
	if m := uaflixVideoSrcRe.FindStringSubmatch(htmlStr); len(m) >= 2 {
		videoURL := strings.TrimSpace(m[1])
		if videoURL != "" {
			return []uaflixPlayStream{{
				link:    videoURL,
				quality: "1080p",
				title:   "Основне джерело",
			}}
		}
	}

	// Extract iframe
	iframeURL := c.extractIframeURL(htmlStr)
	if iframeURL == "" {
		return nil
	}

	// Ignore YouTube trailers
	lower := strings.ToLower(iframeURL)
	if strings.Contains(lower, "youtube.com/embed/") || strings.Contains(lower, "youtu.be/") {
		return nil
	}

	// Parse by player type
	if strings.Contains(lower, "zetvideo.net") {
		return c.parseZetvideoStreams(req, iframeURL)
	}
	if strings.Contains(lower, "ashdi.vip") {
		if strings.Contains(lower, "/vod/") {
			return c.parseAshdiVodStreams(req, iframeURL)
		}
		return c.parseAshdiStreams(req, iframeURL)
	}

	return nil
}

func (c *uaflixChecker) parseZetvideoStreams(req *http.Request, iframeURL string) []uaflixPlayStream {
	body, ok := c.fetch(req, iframeURL, "https://zetvideo.net/")
	if !ok {
		return nil
	}

	htmlStr := string(body)

	// Try file:"url.m3u8" pattern
	if m := uaflixFileM3u8Re.FindStringSubmatch(htmlStr); len(m) >= 2 {
		link := m[1]
		return []uaflixPlayStream{{
			link:    link,
			quality: "1080p",
			title:   uaflixBuildDisplayTitle("Основне джерело", link, 1),
		}}
	}

	return nil
}

func (c *uaflixChecker) parseAshdiVodStreams(req *http.Request, iframeURL string) []uaflixPlayStream {
	requestURL := uaflixWithAshdiMultivoice(iframeURL)
	body, ok := c.fetch(req, requestURL, "https://uafix.net/")
	if !ok {
		return nil
	}

	htmlStr := string(body)

	// Try to extract file:[...] JSON array
	rawArray := uaflixExtractPlayerFileArray(htmlStr)
	if rawArray != "" {
		jsonStr := html.UnescapeString(rawArray)
		jsonStr = strings.ReplaceAll(jsonStr, "\\/", "/")
		jsonStr = strings.ReplaceAll(jsonStr, "\\'", "'")
		jsonStr = strings.ReplaceAll(jsonStr, "\\\"", "\"")

		var items []map[string]any
		if err := stdjson.Unmarshal([]byte(jsonStr), &items); err == nil && len(items) > 0 {
			var streams []uaflixPlayStream
			idx := 1
			for _, item := range items {
				fileURL := uaflixMapStr(item, "file")
				if fileURL == "" {
					continue
				}
				rawTitle := uaflixMapStr(item, "title")
				streams = append(streams, uaflixPlayStream{
					link:    fileURL,
					quality: uaflixDetectQualityTag(rawTitle + " " + fileURL),
					title:   uaflixBuildDisplayTitle(rawTitle, fileURL, idx),
				})
				idx++
			}
			if len(streams) > 0 {
				return streams
			}
		}
	}

	// Fallback: single file pattern
	if m := uaflixFileM3u8Re.FindStringSubmatch(htmlStr); len(m) >= 2 {
		return []uaflixPlayStream{{
			link:    m[1],
			quality: uaflixDetectQualityTag(m[1]),
			title:   uaflixBuildDisplayTitle("", m[1], 1),
		}}
	}
	if m := uaflixFileM3u8Re2.FindStringSubmatch(htmlStr); len(m) >= 2 {
		return []uaflixPlayStream{{
			link:    m[1],
			quality: uaflixDetectQualityTag(m[1]),
			title:   uaflixBuildDisplayTitle("", m[1], 1),
		}}
	}

	return nil
}

func (c *uaflixChecker) parseAshdiStreams(req *http.Request, iframeURL string) []uaflixPlayStream {
	body, ok := c.fetch(req, iframeURL, "https://ashdi.vip/")
	if !ok {
		return nil
	}

	htmlStr := string(body)

	if m := uaflixFileM3u8Re.FindStringSubmatch(htmlStr); len(m) >= 2 {
		return []uaflixPlayStream{{
			link:    m[1],
			quality: "1080p",
			title:   uaflixBuildDisplayTitle("", m[1], 1),
		}}
	}
	if m := uaflixFileM3u8Re2.FindStringSubmatch(htmlStr); len(m) >= 2 {
		return []uaflixPlayStream{{
			link:    m[1],
			quality: "1080p",
			title:   uaflixBuildDisplayTitle("", m[1], 1),
		}}
	}

	return nil
}

// --- HTTP helpers ---

// --- DLE Authentication ---

// dleLogin performs DLE login and stores session cookies.
func (c *uaflixChecker) dleLogin() {
	c.authMu.Lock()
	defer c.authMu.Unlock()

	if c.login == "" || c.passwd == "" {
		return
	}

	jar, _ := cookiejar.New(nil)
	authClient := &http.Client{
		Timeout: 20 * time.Second,
		Jar:     jar,
	}

	hostURL := c.host + "/"
	form := url.Values{
		"login_name":     {c.login},
		"login_password": {c.passwd},
		"login":          {"submit"},
		"login_not_save": {"1"},
	}

	req, err := http.NewRequest(http.MethodPost, hostURL, strings.NewReader(form.Encode()))
	if err != nil {
		log.Warn().Err(err).Msg("uaflix: DLE login request creation failed")
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Referer", hostURL)
	req.Header.Set("Origin", c.host)

	resp, err := authClient.Do(req)
	if err != nil {
		log.Warn().Err(err).Msg("uaflix: DLE login failed")
		return
	}
	defer resp.Body.Close()
	// Drain body
	_, _ = io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	// Extract cookies
	parsedHost, _ := url.Parse(c.host)
	if parsedHost == nil {
		return
	}
	cookies := jar.Cookies(parsedHost)
	hasDLEAuth := false
	var parts []string
	for _, ck := range cookies {
		if ck.Name == "" || ck.Value == "" || ck.Value == "deleted" {
			continue
		}
		parts = append(parts, ck.Name+"="+ck.Value)
		if strings.HasPrefix(ck.Name, "dle_") {
			hasDLEAuth = true
		}
	}

	if !hasDLEAuth {
		log.Warn().Msg("uaflix: DLE login did not return auth cookies")
		return
	}

	c.authCookie = strings.Join(parts, "; ")
	c.authExpiry = time.Now().Add(6 * time.Hour)
	log.Info().Msg("uaflix: DLE login successful")
}

// getCookie returns the cookie header to use for requests to the site host.
func (c *uaflixChecker) getCookie() string {
	// Manual cookie override takes priority.
	if c.cookie != "" {
		return c.cookie
	}

	c.authMu.Lock()
	if c.authCookie != "" && time.Now().Before(c.authExpiry) {
		ck := c.authCookie
		c.authMu.Unlock()
		return ck
	}
	c.authMu.Unlock()

	// Cookie expired or not set — try to re-login synchronously.
	if c.login != "" && c.passwd != "" {
		c.dleLogin() // dleLogin takes its own lock
		c.authMu.Lock()
		ck := c.authCookie
		c.authMu.Unlock()
		return ck
	}

	return ""
}

// shouldUseCookie returns true if the target URL is on the same host as the site.
func (c *uaflixChecker) shouldUseCookie(targetURL string) bool {
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return false
	}
	hostParsed, err := url.Parse(c.host)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Host, hostParsed.Host)
}

func (c *uaflixChecker) fetch(req *http.Request, targetURL, referer string) ([]byte, bool) {
	r, err := http.NewRequestWithContext(req.Context(), http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, false
	}
	r.Header.Set("User-Agent", "Mozilla/5.0")
	if referer != "" {
		r.Header.Set("Referer", referer)
	}

	// Add auth cookies for requests to site host.
	if c.shouldUseCookie(targetURL) {
		if ck := c.getCookie(); ck != "" {
			r.Header.Set("Cookie", ck)
		}
	}

	resp, err := c.client.Do(r)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()

	// If 403 and we have credentials, try re-login and retry once.
	if resp.StatusCode == http.StatusForbidden && c.shouldUseCookie(targetURL) && c.login != "" && c.passwd != "" {
		_, _ = io.ReadAll(io.LimitReader(resp.Body, 1<<10))
		resp.Body.Close()

		c.dleLogin() // refresh cookies
		ck := c.getCookie()
		if ck != "" {
			r2, err := http.NewRequestWithContext(req.Context(), http.MethodGet, targetURL, nil)
			if err != nil {
				return nil, false
			}
			r2.Header.Set("User-Agent", "Mozilla/5.0")
			if referer != "" {
				r2.Header.Set("Referer", referer)
			}
			r2.Header.Set("Cookie", ck)
			resp2, err := c.client.Do(r2)
			if err != nil {
				return nil, false
			}
			defer resp2.Body.Close()
			if resp2.StatusCode != http.StatusOK {
				return nil, false
			}
			data, err := io.ReadAll(resp2.Body)
			if err != nil {
				return nil, false
			}
			return data, true
		}
		return nil, false
	}

	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	// Cap at 8 MB — uaflix fetches HTML pages (player pages, season pages).
	// Legitimate responses are under 1 MB; cap guards against misbehaving upstream.
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, false
	}
	return data, true
}

// --- Utility functions ---

func uaflixDeterminePlayerType(iframeURL string) string {
	if iframeURL == "" {
		return ""
	}
	lower := strings.ToLower(iframeURL)

	if strings.Contains(lower, "ashdi.vip/serial/") {
		return "ashdi-serial"
	}
	if strings.Contains(lower, "ashdi.vip/vod/") {
		return "ashdi-vod"
	}
	if strings.Contains(lower, "zetvideo.net/serial/") {
		return "zetvideo-serial"
	}
	if strings.Contains(lower, "zetvideo.net/vod/") {
		return "zetvideo-vod"
	}

	// Trailers
	if strings.Contains(lower, "youtube.com/embed/") || strings.Contains(lower, "youtu.be/") ||
		strings.Contains(lower, "vimeo.com/") || strings.Contains(lower, "dailymotion.com/") {
		return "trailer"
	}

	return ""
}

func uaflixNormalizeIframeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	raw = html.UnescapeString(raw)
	raw = strings.ReplaceAll(raw, "&amp;", "&")
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	return raw
}

func uaflixNormalizeSerialPlayerKey(playerType, iframeURL string) string {
	if playerType == "ashdi-serial" {
		if m := uaflixAshdiSerialKeyRe.FindStringSubmatch(iframeURL); len(m) >= 2 {
			return m[1]
		}
	}
	return iframeURL
}

func uaflixIsAshdiVoice(v *uaflixVoiceInfo) bool {
	return v != nil && (v.playerType == "ashdi-serial" || v.playerType == "ashdi-vod")
}

func uaflixGetSeasonSet(v *uaflixVoiceInfo) map[int]struct{} {
	result := make(map[int]struct{})
	if v == nil {
		return result
	}
	for sn, eps := range v.seasons {
		if uaflixHasValidEpisodes(eps) {
			result[sn] = struct{}{}
		}
	}
	return result
}

func uaflixHasValidEpisodes(episodes []uaflixEpisodeInfo) bool {
	for _, ep := range episodes {
		if ep.file != "" {
			return true
		}
	}
	return false
}

func uaflixSetsEqual(a, b map[int]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

func uaflixNormalizeVoiceNames(structure *uaflixSerialStructure) {
	const baseName = "Uaflix"
	const zetName = "Uaflix #2"
	const ashdiName = "Uaflix #3"

	_, hasBase := structure.voices[baseName]
	_, hasZet := structure.voices[zetName]
	_, hasAshdi := structure.voices[ashdiName]

	if hasBase {
		return
	}

	if hasZet && !hasAshdi {
		v := structure.voices[zetName]
		v.displayName = baseName
		delete(structure.voices, zetName)
		structure.voices[baseName] = v
	} else if hasAshdi && !hasZet {
		v := structure.voices[ashdiName]
		v.displayName = baseName
		delete(structure.voices, ashdiName)
		structure.voices[baseName] = v
	}
}

// uaflixProxyStreamURL wraps a stream URL through the proxy.
// For zetvideo.net CDN URLs, adds Origin/Referer headers (the CDN requires them).
func uaflixProxyStreamURL(req *http.Request, rawURL, plugin string, links *proxylink.Manager) string {
	if strings.Contains(rawURL, "zetvideo.net") {
		hdrs := map[string]string{
			"Origin":  "https://zetvideo.net",
			"Referer": "https://zetvideo.net/",
		}
		return streamProxyURLWithHeaders(req, rawURL, plugin, links, hdrs)
	}
	return streamProxyURL(req, rawURL, plugin, links)
}

func uaflixStripLampacArgs(u string) string {
	if u == "" {
		return u
	}
	cleaned := uaflixLampacArgsRe.ReplaceAllString(u, "$1")
	cleaned = strings.ReplaceAll(cleaned, "?&", "?")
	cleaned = strings.ReplaceAll(cleaned, "&&", "&")
	cleaned = strings.TrimRight(cleaned, "?&")
	return cleaned
}

func uaflixWithAshdiMultivoice(u string) string {
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

func uaflixExtractPlayerFileArray(htmlStr string) string {
	searchIndex := 0
	for searchIndex < len(htmlStr) {
		idx := strings.Index(strings.ToLower(htmlStr[searchIndex:]), "file")
		if idx < 0 {
			return ""
		}
		fileIndex := searchIndex + idx

		colonIdx := strings.IndexByte(htmlStr[fileIndex:], ':')
		if colonIdx < 0 {
			return ""
		}
		colonIndex := fileIndex + colonIdx

		startIndex := colonIndex + 1
		for startIndex < len(htmlStr) && (htmlStr[startIndex] == ' ' || htmlStr[startIndex] == '\t' || htmlStr[startIndex] == '\n' || htmlStr[startIndex] == '\r') {
			startIndex++
		}
		if startIndex < len(htmlStr) && (htmlStr[startIndex] == '\'' || htmlStr[startIndex] == '"') {
			startIndex++
			for startIndex < len(htmlStr) && (htmlStr[startIndex] == ' ' || htmlStr[startIndex] == '\t' || htmlStr[startIndex] == '\n' || htmlStr[startIndex] == '\r') {
				startIndex++
			}
		}

		if startIndex >= len(htmlStr) || htmlStr[startIndex] != '[' {
			searchIndex = fileIndex + 4
			continue
		}

		return uaflixExtractBracketArray(htmlStr, startIndex)
	}
	return ""
}

func uaflixExtractBracketArray(text string, startIndex int) string {
	if startIndex < 0 || startIndex >= len(text) || text[startIndex] != '[' {
		return ""
	}

	depth := 0
	inString := false
	escaped := false
	var quoteChar byte

	for i := startIndex; i < len(text); i++ {
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
				return text[startIndex : i+1]
			}
		}
	}

	return ""
}

func uaflixBuildDisplayTitle(rawTitle, link string, index int) string {
	normalized := strings.TrimSpace(rawTitle)
	if normalized == "" {
		normalized = fmt.Sprintf("Варіант %d", index)
	} else {
		normalized = html.UnescapeString(normalized)
	}

	qualityTag := uaflixDetectQualityTag(normalized + " " + link)
	if qualityTag == "" {
		return normalized
	}
	if strings.HasPrefix(normalized, "[4K]") || strings.HasPrefix(normalized, "[FHD]") {
		return normalized
	}
	return qualityTag + " " + normalized
}

func uaflixDetectQualityTag(value string) string {
	if value == "" {
		return ""
	}
	if uaflixQuality4kRe.MatchString(value) {
		return "[4K]"
	}
	if uaflixQualityFhdRe.MatchString(value) {
		return "[FHD]"
	}
	return ""
}

func uaflixCleanText(s string) string {
	s = uaflixHTMLTagRe.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	return strings.TrimSpace(s)
}

func uaflixExtractYear(text string) int {
	m := uaflixYearRe.FindStringSubmatch(text)
	if len(m) < 2 {
		return 0
	}
	y, _ := strconv.Atoi(m[1])
	return y
}

func uaflixExtractCategoryFromURL(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) == 0 {
		return ""
	}
	first := strings.ToLower(parts[0])
	switch first {
	case "film":
		return "films"
	case "serial":
		return "serials"
	default:
		return first
	}
}

// uaflixMapStr safely extracts a string value from a map, returning "" for nil.
func uaflixMapStr(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	s := fmt.Sprint(v)
	if s == "<nil>" {
		return ""
	}
	return strings.TrimSpace(s)
}

// --- Search scoring ---

func uaflixIsAnimeRequest(title, originalTitle, originalLang, source string) bool {
	combined := strings.ToLower(title + " " + originalTitle + " " + source)
	if strings.Contains(combined, "anime") || strings.Contains(combined, "аніме") {
		return true
	}
	return strings.EqualFold(originalLang, "ja")
}

func uaflixFilterByContentType(results []*uaflixSearchResult, serial int, allowAnime bool) []*uaflixSearchResult {
	expected := "films"
	if serial == 1 {
		expected = "serials"
	}

	var filtered []*uaflixSearchResult
	for _, r := range results {
		if !allowAnime && r.isAnime {
			continue
		}
		if r.category == "" || r.category == expected || (allowAnime && r.isAnime) {
			filtered = append(filtered, r)
		}
	}
	if len(filtered) > 0 {
		return filtered
	}

	// Fallback: all non-anime
	filtered = nil
	for _, r := range results {
		if !allowAnime && r.isAnime {
			continue
		}
		filtered = append(filtered, r)
	}
	return filtered
}

func uaflixBuildMatchScore(r *uaflixSearchResult, title, originalTitle string, year, serial int, allowAnime bool) int {
	score := 0
	if r.titleMatch {
		score += 100
	} else {
		score += uaflixComputePartialTitleScore(r.title, title, originalTitle)
	}

	if year > 0 {
		if r.year == year {
			score += 60
		} else if r.year > 0 && int(math.Abs(float64(r.year-year))) == 1 {
			score += 10
		} else if r.year > 0 {
			score -= 15
		}
	}

	if serial == 1 {
		if strings.EqualFold(r.category, "serials") {
			score += 25
		} else if strings.EqualFold(r.category, "films") {
			score -= 10
		}
	} else {
		if strings.EqualFold(r.category, "films") {
			score += 25
		} else if strings.EqualFold(r.category, "serials") {
			score -= 10
		}
	}

	if r.isAnime && !allowAnime {
		score -= 80
	}

	return score
}

func uaflixComputePartialTitleScore(candidateTitle, title, originalTitle string) int {
	candidateTokens := uaflixTitleTokens(candidateTitle)
	if len(candidateTokens) == 0 {
		return 0
	}

	targetTokens := uaflixTitleTokens(title)
	for t := range uaflixTitleTokens(originalTitle) {
		targetTokens[t] = struct{}{}
	}
	if len(targetTokens) == 0 {
		return 0
	}

	overlap := 0
	for t := range candidateTokens {
		if _, ok := targetTokens[t]; ok {
			overlap++
		}
	}

	maxLen := max(len(targetTokens), len(candidateTokens))

	ratio := float64(overlap) / float64(maxLen)
	if ratio >= 0.85 {
		return 70
	}
	if ratio >= 0.65 {
		return 50
	}
	if ratio >= 0.45 {
		return 30
	}
	if ratio >= 0.30 {
		return 15
	}
	return 0
}

func uaflixHasStrongTitleMatch(r *uaflixSearchResult, title, originalTitle string) bool {
	targets := make(map[string]struct{})
	for _, candidate := range []string{title, originalTitle} {
		n := uaflixNormalizeTitle(candidate)
		if n != "" {
			targets[strings.ToLower(n)] = struct{}{}
		}
	}
	if len(targets) == 0 {
		return false
	}

	parts := uaflixSplitTitleParts(r.title)
	for _, part := range parts {
		n := uaflixNormalizeTitle(part)
		if n == "" {
			continue
		}
		nLower := strings.ToLower(n)
		if _, ok := targets[nLower]; ok {
			return true
		}
		for target := range targets {
			if len(nLower) >= 6 && len(target) >= 6 {
				if strings.Contains(nLower, target) || strings.Contains(target, nLower) {
					return true
				}
			}
		}
	}

	return false
}

func uaflixSplitTitleParts(title string) []string {
	if title == "" {
		return nil
	}
	parts := strings.FieldsFunc(title, func(r rune) bool {
		return r == '/' || r == '|' || r == '•'
	})
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(html.UnescapeString(p))
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

func uaflixNormalizeTitle(value string) string {
	if value == "" {
		return ""
	}
	text := strings.ToLower(html.UnescapeString(value))
	text = uaflixTitleNormRe.ReplaceAllString(text, " ")
	text = uaflixTitleExtraRe.ReplaceAllString(text, " ")
	text = uaflixMultiSpaceRe.ReplaceAllString(text, " ")
	return strings.TrimSpace(text)
}

func uaflixTitleTokens(value string) map[string]struct{} {
	n := uaflixNormalizeTitle(value)
	if n == "" {
		return make(map[string]struct{})
	}
	tokens := make(map[string]struct{})
	for t := range strings.FieldsSeq(n) {
		if len(t) > 1 {
			tokens[strings.ToLower(t)] = struct{}{}
		}
	}
	return tokens
}
