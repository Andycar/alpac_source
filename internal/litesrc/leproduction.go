package litesrc

import (
	stdjson "encoding/json"
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

var (
	// Extract iframe src: <iframe ... src="https://fsst.online/playlist_iframe/31433/" ...>
	leprodIframeRe = regexp.MustCompile(`<iframe[^>]*\bsrc="(https?://[^"]*playlist_iframe/[^"]*)"`)

	// Extract <select id="selectFilmN"> ... </select> blocks.
	leprodSelectRe = regexp.MustCompile(`(?s)<select[^>]*id="selectFilm1"[^>]*>(.*?)</select>`)

	// Extract <option value="URL">TEXT</option> from select block.
	leprodOptionRe = regexp.MustCompile(`<option[^>]*value="([^"]*)"[^>]*>([^<]*)</option>`)

	// Extract search result links: href="https://...le-production.tv/anime/123-slug.html" or relative "/serial/..."
	leprodSearchLinkRe = regexp.MustCompile(`href="((?:https?://[^"]*)?/(?:anime|serial|filmy|film|dorama|cartoon|multserials)/[^"]*\.html)"`)

	// Extract title from search results: <h3><a href="URL">TITLE</a></h3> or <a href="URL">TITLE</a>
	leprodSearchTitleRe = regexp.MustCompile(`(?s)<a[^>]*href="((?:https?://[^"]*)?/(?:anime|serial|filmy|film|dorama|cartoon|multserials)/[^"]*\.html)"[^>]*>(.*?)</a>`)

	// Extract PlayerJS file field: file: [{...}] (JSON array) or file: 'URL' (string).
	leprodPJSFileArrayRe  = regexp.MustCompile(`(?s)file:\s*(\[\s*\{.*?\}\s*\])\s*,`)
	leprodPJSFileStringRe = regexp.MustCompile(`file:\s*'([^']+)'`)

	// Extract quality URLs: [1080p]https://..., [720p]https://...
	leprodQualityRe = regexp.MustCompile(`\[(\d+p)\](https?://[^,\s"']+)`)

	// Extract page title from <h1> or og:title.
	leprodPageTitleRe = regexp.MustCompile(`<h1[^>]*>(.*?)</h1>`)

	// Extract season number from URL slug: "7-sezon" → 7
	leprodSeasonNumRe = regexp.MustCompile(`(\d+)-sezon`)
)

// leprodEpisode holds a parsed PlayerJS episode entry.
type leprodEpisode struct {
	Comment string // episode title/name
	File    string // quality-tagged URLs: [1080p]URL,[720p]URL,...
	Poster  string
}

// leprodSearchResult holds a parsed search result entry.
type leprodSearchResult struct {
	URL   string // relative: /anime/123-slug.html
	Title string
}

type leprodSeasonEntry struct {
	num int
	url string // relative URL on le-production.tv
}

type leproductionChecker struct {
	client *http.Client
	host   string
}

func NewLeproductionChecker(cfg config.Config) *leproductionChecker {
	host := strings.TrimSpace(strings.TrimRight(cfg.Online.LeProduction.Host, "/"))
	if host == "" {
		host = "https://www.le-production.tv"
	}
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}
	return &leproductionChecker{
		client: httpclient.New(15 * time.Second),
		host:   host,
	}
}

func (lp *leproductionChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		log.Debug().Str("url", req.URL.String()).Msg("leproduction: request")
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show := lp.checkSearch(req)
			writeCheckSearchResponse(w, show, pluginQualityBadgeGet("leproduction"))
			return
		}
		lp.index(w, req, links)
	}
}

// ── CHECKSEARCH ─────────────────────────────────────────────────────

func (lp *leproductionChecker) checkSearch(req *http.Request) bool {
	q := req.URL.Query()
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	if title == "" {
		title = originalTitle
	}
	if title == "" {
		return false
	}
	results := lp.search(req, title)
	if len(results) == 0 && originalTitle != "" && originalTitle != title {
		results = lp.search(req, originalTitle)
	}
	if len(results) == 0 {
		results = lp.search(req, title+" сезон")
	}
	return len(results) > 0
}

// ── INDEX ───────────────────────────────────────────────────────────

func (lp *leproductionChecker) index(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))

	// Direct content page URL (for serial episode navigation).
	pageURL := strings.TrimSpace(q.Get("page_url"))

	s, sSet := getsTVQueryInt(q.Get("s"))
	e, _ := getsTVQueryInt(q.Get("e"))
	if !sSet {
		s = -1
	}
	_ = e // episode number used for serial episode selection

	// If we have a direct page URL, use it (from serial navigation callbacks).
	if pageURL != "" {
		lp.renderPage(w, req, rjson, pageURL, title, originalTitle, s, links)
		return
	}

	// Search by title.
	searchTitle := title
	if searchTitle == "" {
		searchTitle = originalTitle
	}
	if searchTitle == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	results := lp.search(req, searchTitle)
	if len(results) == 0 && originalTitle != "" && originalTitle != searchTitle {
		results = lp.search(req, originalTitle)
	}
	// DLE ignores short words (<4 chars). Retry with "сезон" suffix which helps
	// find serial pages like "ФБР 8 сезон" when searching for "ФБР" alone.
	if len(results) == 0 {
		results = lp.search(req, searchTitle+" сезон")
	}
	if len(results) == 0 && originalTitle != "" && originalTitle != searchTitle {
		results = lp.search(req, originalTitle+" season")
	}
	if len(results) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Group results by season number (extracted from URL slug like "8-sezon").
	var seasons []leprodSeasonEntry
	seen := make(map[int]struct{})
	for _, r := range results {
		m := leprodSeasonNumRe.FindStringSubmatch(r.URL)
		if m == nil {
			continue
		}
		num, _ := strconv.Atoi(m[1])
		if num <= 0 {
			continue
		}
		if _, ok := seen[num]; ok {
			continue
		}
		seen[num] = struct{}{}
		seasons = append(seasons, leprodSeasonEntry{num: num, url: r.URL})
	}

	// Pre-verify: only keep seasons that have Le-Production's own player (playlist_iframe).
	// Pages using Alloha or other third-party players are not supported by this balancer.
	if len(seasons) > 1 {
		seasons = lp.filterSeasonsWithPlayer(req, seasons)
	}

	log.Debug().
		Int("results", len(results)).
		Int("seasons", len(seasons)).
		Int("s", s).
		Msg("leproduction: index after filter")

	// If multiple seasons found and no specific season selected — show season list.
	if len(seasons) > 1 && s == -1 {
		sort.Slice(seasons, func(i, j int) bool { return seasons[i].num < seasons[j].num })
		host := hostFromRequest(req)
		encTitle := url.QueryEscape(title)
		encOriginal := url.QueryEscape(originalTitle)

		rows := make([]map[string]any, 0, len(seasons))
		labels := make([]string, 0, len(seasons))
		for _, se := range seasons {
			name := strconv.Itoa(se.num) + " сезон"
			link := fmt.Sprintf("%s/lite/leproduction?rjson=%s&title=%s&original_title=%s&s=%d&page_url=%s",
				host, getsTVBool(rjson), encTitle, encOriginal, se.num, url.QueryEscape(lp.host+se.url))
			rows = append(rows, map[string]any{
				"method": "link",
				"id":     se.num,
				"url":    link,
				"name":   name,
			})
			labels = append(labels, name)
		}

		if rjson {
			writeJSON(w, http.StatusOK, map[string]any{
				"type": "season",
				"data": rows,
			})
			return
		}

		var sb strings.Builder
		sb.WriteString(`<div class="videos__line">`)
		for i, row := range rows {
			getsTVAppendSeasonHTML(&sb, row, labels[i], i == 0)
		}
		sb.WriteString(`</div>`)
		writeHTML(w, http.StatusOK, sb.String())
		return
	}

	// If a specific season is selected and we have season URLs — pick the right page.
	if s > 0 && len(seasons) > 0 {
		for _, se := range seasons {
			if se.num == s {
				contentURL := lp.host + se.url
				lp.renderPage(w, req, rjson, contentURL, title, originalTitle, s, links)
				return
			}
		}
	}

	// If exactly 1 season passed the filter, use it directly (skip season selector).
	if len(seasons) == 1 {
		contentURL := lp.host + seasons[0].url
		lp.renderPage(w, req, rjson, contentURL, title, originalTitle, seasons[0].num, links)
		return
	}

	// Default: use first result.
	contentURL := lp.host + results[0].URL
	lp.renderPage(w, req, rjson, contentURL, title, originalTitle, s, links)
}

// ── SEARCH ──────────────────────────────────────────────────────────

func (lp *leproductionChecker) search(req *http.Request, title string) []leprodSearchResult {
	searchURL := lp.host + "/index.php?do=search&subaction=search&story=" + url.QueryEscape(title)

	body, ok := lp.fetchText(req, searchURL)
	if !ok {
		return nil
	}
	matches := leprodSearchTitleRe.FindAllStringSubmatch(body, 20)
	if len(matches) == 0 {
		// Fallback: just extract links.
		links := leprodSearchLinkRe.FindAllStringSubmatch(body, 20)
		results := make([]leprodSearchResult, 0, len(links))
		seen := make(map[string]struct{}, len(links))
		for _, m := range links {
			href := leprodNormalizeURL(m[1], lp.host)
			if _, ok := seen[href]; ok {
				continue
			}
			seen[href] = struct{}{}
			results = append(results, leprodSearchResult{URL: href})
		}
		return leprodFilterByRelevance(results, title)
	}

	results := make([]leprodSearchResult, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		href := leprodNormalizeURL(m[1], lp.host)
		t := strings.TrimSpace(stripHTMLTags(m[2]))
		if t == "" {
			continue
		}
		// Skip image-only links (thumbnails repeat the same URL).
		if strings.Contains(m[2], "<img") {
			continue
		}
		if _, ok := seen[href]; ok {
			continue
		}
		seen[href] = struct{}{}
		results = append(results, leprodSearchResult{URL: href, Title: t})
	}
	return leprodFilterByRelevance(results, title)
}

// leprodFilterByRelevance filters search results to only include those whose
// title or URL slug matches the search query. DLE fulltext search returns
// pages mentioning the query anywhere (description, tags), not just title matches.
func leprodFilterByRelevance(results []leprodSearchResult, query string) []leprodSearchResult {
	if len(results) == 0 {
		return nil
	}
	queryLower := strings.ToLower(strings.TrimSpace(query))
	if queryLower == "" {
		return results
	}

	// Remove common suffixes used in retry searches.
	queryCore := queryLower
	for _, suffix := range []string{" сезон", " season"} {
		queryCore = strings.TrimSuffix(queryCore, suffix)
	}
	queryCore = strings.TrimSpace(queryCore)
	if queryCore == "" {
		queryCore = queryLower
	}

	// Build query words for matching (skip common words like "сезон", numbers).
	queryWords := make([]string, 0, 4)
	for w := range strings.FieldsSeq(queryCore) {
		if len(w) < 2 {
			continue
		}
		if _, err := strconv.Atoi(w); err == nil {
			continue // skip pure numbers
		}
		queryWords = append(queryWords, w)
	}
	if len(queryWords) == 0 {
		// Very short query (e.g. "ФБР" = 3 chars single word) — use as-is.
		queryWords = []string{queryCore}
	}

	filtered := make([]leprodSearchResult, 0, len(results))
	for _, r := range results {
		titleLower := strings.ToLower(r.Title)

		// Check if all query words appear in the title.
		if r.Title != "" && leprodAllWordsInText(queryWords, titleLower) {
			filtered = append(filtered, r)
			continue
		}
		// Check URL slug match.
		slug := leprodExtractSlug(r.URL)
		if slug != "" && leprodSlugMatchesQuery(slug, queryWords) {
			filtered = append(filtered, r)
			continue
		}
	}
	return filtered
}

// leprodAllWordsInText checks that every query word is found in the text.
func leprodAllWordsInText(words []string, text string) bool {
	for _, w := range words {
		if !strings.Contains(text, w) {
			return false
		}
	}
	return true
}

// leprodExtractSlug extracts the hyphenated slug from a content URL.
// "/serial/761-novobranec-novichok-salaga-7-sezon.html" → "novobranec-novichok-salaga-7-sezon"
func leprodExtractSlug(urlPath string) string {
	// Remove .html suffix.
	slug := strings.TrimSuffix(urlPath, ".html")
	// Take the last path segment.
	if idx := strings.LastIndex(slug, "/"); idx >= 0 {
		slug = slug[idx+1:]
	}
	// Remove leading numeric ID: "761-rest" → "rest"
	if idx := strings.Index(slug, "-"); idx >= 0 {
		prefix := slug[:idx]
		if _, err := strconv.Atoi(prefix); err == nil {
			slug = slug[idx+1:]
		}
	}
	return strings.ToLower(slug)
}

// leprodSlugMatchesQuery checks if the URL slug contains all query words
// (transliterated Russian → Latin slug parts).
func leprodSlugMatchesQuery(slug string, queryWords []string) bool {
	slugParts := strings.Split(slug, "-")
	for _, qw := range queryWords {
		found := false
		for _, sp := range slugParts {
			if strings.HasPrefix(sp, qw) || strings.HasPrefix(qw, sp) {
				found = true
				break
			}
		}
		if !found {
			// Also check if query word is contained as substring in the full slug.
			if strings.Contains(slug, qw) {
				continue
			}
			return false
		}
	}
	return true
}

// ── RENDER PAGE ─────────────────────────────────────────────────────

func (lp *leproductionChecker) renderPage(
	w http.ResponseWriter, req *http.Request, rjson bool,
	contentURL, title, originalTitle string,
	season int, links *proxylink.Manager,
) {
	body, ok := lp.fetchText(req, contentURL)
	if !ok {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Extract page title if we don't have one.
	if title == "" {
		if m := leprodPageTitleRe.FindStringSubmatch(body); len(m) >= 2 {
			title = strings.TrimSpace(stripHTMLTags(m[1]))
		}
	}

	// Extract select options (for episode list).
	selectOptions := lp.parseSelectOptions(body)

	// Extract primary iframe src.
	iframeSrc := ""
	if m := leprodIframeRe.FindStringSubmatch(body); len(m) >= 2 {
		iframeSrc = m[1]
	}

	log.Debug().
		Int("selectOptions", len(selectOptions)).
		Str("iframeSrc", iframeSrc).
		Str("contentURL", contentURL).
		Msg("leproduction: renderPage")

	if len(selectOptions) > 1 {
		// Serial: multiple episodes in select dropdown.
		lp.writeSerial(w, req, rjson, selectOptions, iframeSrc, contentURL, title, originalTitle, season, links)
		return
	}

	// Single video (movie) or single-episode content.
	if iframeSrc == "" && len(selectOptions) == 1 {
		iframeSrc = selectOptions[0].iframeURL
	}
	if iframeSrc == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	episodes := lp.fetchPlayerJS(req, iframeSrc)
	if len(episodes) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if len(episodes) == 1 {
		lp.writeMovie(w, req, rjson, episodes[0], title, originalTitle, links)
	} else {
		// Multiple episodes from a single playlist (all in one iframe).
		lp.writeEpisodeList(w, req, rjson, episodes, title, originalTitle, links)
	}
}

// ── MOVIE RENDERER ──────────────────────────────────────────────────

func (lp *leproductionChecker) writeMovie(
	w http.ResponseWriter, req *http.Request, rjson bool,
	ep leprodEpisode, title, originalTitle string,
	links *proxylink.Manager,
) {
	qualityMap, bestURL := lp.buildQualityMap(req, ep.File, links)
	if bestURL == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	row := map[string]any{
		"method": "play",
		"url":    bestURL,
		"stream": bestURL,
		"name":   "Le-Production",
		"title":  getsTVJoinName(title, originalTitle),
	}
	if len(qualityMap) > 1 {
		row["quality"] = qualityMap
	}
	if ep.Poster != "" {
		row["img"] = ep.Poster
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "movie",
			"data": []map[string]any{row},
		})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	getsTVAppendMovieHTML(&sb, row, "Le-Production", true, 0, 0)
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// ── SERIAL RENDERER ─────────────────────────────────────────────────

type leprodSelectOption struct {
	iframeURL string
	label     string
}

func (lp *leproductionChecker) parseSelectOptions(body string) []leprodSelectOption {
	selectBlock := submatch1(leprodSelectRe, body)
	if selectBlock == "" {
		return nil
	}

	matches := leprodOptionRe.FindAllStringSubmatch(selectBlock, -1)
	options := make([]leprodSelectOption, 0, len(matches))
	for _, m := range matches {
		iframeURL := strings.TrimSpace(m[1])
		label := strings.TrimSpace(m[2])
		if iframeURL == "" {
			continue
		}
		options = append(options, leprodSelectOption{iframeURL: iframeURL, label: label})
	}
	return options
}

func (lp *leproductionChecker) writeSerial(
	w http.ResponseWriter, req *http.Request, rjson bool,
	options []leprodSelectOption, defaultIframe, contentURL, title, originalTitle string,
	season int, links *proxylink.Manager,
) {
	host := hostFromRequest(req)
	encTitle := url.QueryEscape(title)
	encOriginal := url.QueryEscape(originalTitle)

	// If season == -1, show episode list (Le-Production doesn't have explicit seasons;
	// all episodes are in the select dropdown).
	// Fetch the playlist for each option to get actual episode data.

	// For simplicity: each select option is treated as an episode.
	// The option's iframe points to a playlist which may have 1 or more episodes.
	// Fetch the first option's playlist to check if it has multiple episodes.

	// First, try: fetch the default/first iframe to see if it has a multi-episode playlist.
	targetIframe := defaultIframe
	if targetIframe == "" && len(options) > 0 {
		targetIframe = options[0].iframeURL
	}

	if targetIframe != "" {
		episodes := lp.fetchPlayerJS(req, targetIframe)
		if len(episodes) > 1 {
			// The playlist has all episodes — use this directly.
			lp.writeEpisodeList(w, req, rjson, episodes, title, originalTitle, links)
			return
		}
	}

	// Fallback: each select option is one episode.
	// When season == -1 (no selection), return the episode list.
	baseTitle := getsTVJoinName(title, originalTitle)
	type episodeRow struct {
		data  map[string]any
		label string
		ep    int
	}
	rows := make([]episodeRow, 0, len(options))

	for i, opt := range options {
		epNum := i + 1
		label := opt.label
		if label == "" {
			label = fmt.Sprintf("%d серия", epNum)
		}

		// Fetch episode stream from iframe.
		episodes := lp.fetchPlayerJS(req, opt.iframeURL)
		if len(episodes) == 0 {
			continue
		}

		ep := episodes[0]
		qualityMap, bestURL := lp.buildQualityMap(req, ep.File, links)
		if bestURL == "" {
			continue
		}

		row := map[string]any{
			"method": "play",
			"url":    bestURL,
			"stream": bestURL,
			"s":      1,
			"e":      epNum,
			"name":   label,
			"title":  fmt.Sprintf("%s (%s)", baseTitle, label),
		}
		if len(qualityMap) > 1 {
			row["quality"] = qualityMap
		}
		if ep.Poster != "" {
			row["img"] = ep.Poster
		}
		rows = append(rows, episodeRow{data: row, label: label, ep: epNum})
	}

	if len(rows) == 0 {
		// Try to show as search results with links.
		lp.writeSearchLinks(w, req, rjson, options, host, encTitle, encOriginal, links)
		return
	}

	sort.Slice(rows, func(i, j int) bool {
		return rows[i].ep < rows[j].ep
	})

	dataRows := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		dataRows = append(dataRows, r.data)
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "episode",
			"data": dataRows,
		})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, r := range rows {
		getsTVAppendMovieHTML(&sb, r.data, r.label, i == 0, 1, r.ep)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// writeEpisodeList renders episodes from a single PlayerJS playlist (all in one iframe).
func (lp *leproductionChecker) writeEpisodeList(
	w http.ResponseWriter, req *http.Request, rjson bool,
	episodes []leprodEpisode, title, originalTitle string,
	links *proxylink.Manager,
) {
	baseTitle := getsTVJoinName(title, originalTitle)

	type episodeRow struct {
		data  map[string]any
		label string
		ep    int
	}
	rows := make([]episodeRow, 0, len(episodes))

	for i, ep := range episodes {
		epNum := i + 1
		label := strings.TrimSpace(ep.Comment)
		if label == "" {
			label = fmt.Sprintf("%d серия", epNum)
		}
		// Try to extract episode number from comment.
		if num := leprodExtractEpNum(label); num > 0 {
			epNum = num
		}

		qualityMap, bestURL := lp.buildQualityMap(req, ep.File, links)
		if bestURL == "" {
			continue
		}

		row := map[string]any{
			"method": "play",
			"url":    bestURL,
			"stream": bestURL,
			"s":      1,
			"e":      epNum,
			"name":   label,
			"title":  fmt.Sprintf("%s (%s)", baseTitle, label),
		}
		if len(qualityMap) > 1 {
			row["quality"] = qualityMap
		}
		if ep.Poster != "" {
			row["img"] = ep.Poster
		}
		rows = append(rows, episodeRow{data: row, label: label, ep: epNum})
	}

	if len(rows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	sort.Slice(rows, func(i, j int) bool {
		return rows[i].ep < rows[j].ep
	})

	dataRows := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		dataRows = append(dataRows, r.data)
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "episode",
			"data": dataRows,
		})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, r := range rows {
		getsTVAppendMovieHTML(&sb, r.data, r.label, i == 0, 1, r.ep)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// writeSearchLinks renders search results as link items when we can't extract streams.
func (lp *leproductionChecker) writeSearchLinks(
	w http.ResponseWriter, req *http.Request, rjson bool,
	options []leprodSelectOption,
	host, encTitle, encOriginal string,
	links *proxylink.Manager,
) {
	writeGetsTVEmpty(w, rjson)
}

// ── PLAYERJS FETCHER ────────────────────────────────────────────────

func (lp *leproductionChecker) fetchPlayerJS(req *http.Request, iframeURL string) []leprodEpisode {
	// The iframe URL may be on fsst.online which redirects to secvideo1.online.
	body, ok := lp.fetchTextWithRedirects(req, iframeURL)
	if !ok {
		return nil
	}

	// Try JSON array format first: file: [{...}, {...}]
	if m := leprodPJSFileArrayRe.FindStringSubmatch(body); len(m) >= 2 {
		return lp.parseFileArray(m[1])
	}

	// Try string format: file: '[1080p]URL,[720p]URL'
	if m := leprodPJSFileStringRe.FindStringSubmatch(body); len(m) >= 2 {
		return []leprodEpisode{{File: m[1]}}
	}

	return nil
}

// parseFileArray parses PlayerJS file JSON array.
// Format: [{"comment":"Ep 1","file":"[1080p]URL,[720p]URL","poster":"URL"},...]
func (lp *leproductionChecker) parseFileArray(raw string) []leprodEpisode {
	// Manual parsing since the JSON may have JS-specific quirks.
	// We look for "comment":"...", "file":"..." pairs.
	type jsObj struct {
		Comment string `json:"comment"`
		File    string `json:"file"`
		Poster  string `json:"poster"`
	}

	// Clean up potential JS issues: trailing commas, single quotes.
	raw = strings.TrimSpace(raw)

	var items []jsObj
	if err := stdjson.Unmarshal([]byte(raw), &items); err != nil {
		log.Debug().Err(err).Msg("leproduction: failed to parse PlayerJS file array")
		// Fallback: regex extraction.
		return lp.parseFileArrayRegex(raw)
	}

	episodes := make([]leprodEpisode, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.File) == "" {
			continue
		}
		episodes = append(episodes, leprodEpisode{
			Comment: item.Comment,
			File:    item.File,
			Poster:  item.Poster,
		})
	}
	return episodes
}

// Pre-compiled fallback regexes for parseFileArrayRegex.
var (
	leprodFileArrayCommentRe = regexp.MustCompile(`"comment"\s*:\s*"([^"]*)"`)
	leprodFileArrayFileRe    = regexp.MustCompile(`"file"\s*:\s*"([^"]*)"`)
	leprodFileArrayPosterRe  = regexp.MustCompile(`"poster"\s*:\s*"([^"]*)"`)
)

// parseFileArrayRegex extracts episodes using regex when JSON parsing fails.
func (lp *leproductionChecker) parseFileArrayRegex(raw string) []leprodEpisode {
	comments := leprodFileArrayCommentRe.FindAllStringSubmatch(raw, -1)
	files := leprodFileArrayFileRe.FindAllStringSubmatch(raw, -1)
	posters := leprodFileArrayPosterRe.FindAllStringSubmatch(raw, -1)

	n := min(len(comments), len(files))

	episodes := make([]leprodEpisode, 0, n)
	for i := range files {
		comment := ""
		if i < len(comments) {
			comment = comments[i][1]
		}
		poster := ""
		if i < len(posters) {
			poster = posters[i][1]
		}
		episodes = append(episodes, leprodEpisode{
			Comment: comment,
			File:    files[i][1],
			Poster:  poster,
		})
	}
	return episodes
}

// ── QUALITY MAP BUILDER ─────────────────────────────────────────────

// buildQualityMap parses "[1080p]URL,[720p]URL,[360p]URL" into a quality map
// and returns the best URL. All URLs are proxied with Referer header and #t=25 ad skip.
func (lp *leproductionChecker) buildQualityMap(
	req *http.Request, fileField string, links *proxylink.Manager,
) (qualityMap map[string]string, bestURL string) {
	headers := map[string]string{
		"Referer": "https://secvideo1.online/",
		"Origin":  "https://secvideo1.online",
		"Cookie":  "",
	}

	matches := leprodQualityRe.FindAllStringSubmatch(fileField, -1)
	if len(matches) == 0 {
		// No quality tags — treat the whole field as a single URL.
		rawURL := strings.TrimSpace(fileField)
		if rawURL == "" {
			return nil, ""
		}
		proxyURL := streamProxyURLWithHeaders(req, rawURL, "leproduction", links, headers)
		proxyURL += "#t=25"
		return nil, proxyURL
	}

	qualityMap = make(map[string]string, len(matches))
	type qualEntry struct {
		label string
		url   string
		rank  int
	}
	entries := make([]qualEntry, 0, len(matches))

	for _, m := range matches {
		label := m[1] // e.g. "1080p"
		rawURL := strings.TrimSpace(m[2])
		if rawURL == "" {
			continue
		}
		proxyURL := streamProxyURLWithHeaders(req, rawURL, "leproduction", links, headers)
		proxyURL += "#t=25"

		qualityMap[label] = proxyURL
		entries = append(entries, qualEntry{label: label, url: proxyURL, rank: leprodQualityRank(label)})
	}

	if len(entries) == 0 {
		return nil, ""
	}

	// Find best quality URL.
	best := entries[0]
	for _, e := range entries[1:] {
		if e.rank > best.rank {
			best = e
		}
	}
	return qualityMap, best.url
}

// leprodQualityRank returns a numeric rank for quality labels.
func leprodQualityRank(label string) int {
	switch label {
	case "2160p", "4k":
		return 4
	case "1080p":
		return 3
	case "720p":
		return 2
	case "480p":
		return 1
	case "360p":
		return 0
	default:
		return -1
	}
}

// filterSeasonsWithPlayer fetches each season page in parallel and keeps only
// those that embed Le-Production's own player (playlist_iframe on secvideo1/fsst/csst).
// Pages using third-party players (Alloha, etc.) are filtered out.
func (lp *leproductionChecker) filterSeasonsWithPlayer(req *http.Request, seasons []leprodSeasonEntry) []leprodSeasonEntry {
	type checkResult struct {
		idx int
		ok  bool
	}
	ch := make(chan checkResult, len(seasons))
	for i, se := range seasons {
		go func(idx int, pageURL string) {
			body, fetched := lp.fetchText(req, lp.host+pageURL)
			hasPlayer := fetched && leprodIframeRe.MatchString(body)
			ch <- checkResult{idx: idx, ok: hasPlayer}
		}(i, se.url)
	}

	valid := make([]bool, len(seasons))
	for range seasons {
		r := <-ch
		valid[r.idx] = r.ok
	}

	filtered := make([]leprodSeasonEntry, 0, len(seasons))
	for i, se := range seasons {
		if valid[i] {
			filtered = append(filtered, se)
		}
	}
	return filtered
}

// ── FETCH HELPERS ───────────────────────────────────────────────────

func (lp *leproductionChecker) fetchText(req *http.Request, target string) (string, bool) {
	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, target, nil)
	if err != nil {
		log.Debug().Err(err).Str("url", target).Msg("leproduction: request create failed")
		return "", false
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	httpReq.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	httpReq.Header.Set("Referer", lp.host+"/")

	resp, err := balancerDoWithRetry(httpReq.Context(), lp.client, httpReq, 2)
	if err != nil {
		log.Debug().Err(err).Str("url", target).Msg("leproduction: fetch failed")
		return "", false
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Debug().Int("status", resp.StatusCode).Str("url", target).Msg("leproduction: bad status")
		return "", false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		log.Debug().Err(err).Str("url", target).Msg("leproduction: read body failed")
		return "", false
	}
	log.Debug().Int("bodyLen", len(body)).Str("url", target).Msg("leproduction: fetched OK")
	return string(body), true
}

// fetchTextWithRedirects is like fetchText but follows redirects (for iframe URLs
// that redirect from fsst.online to secvideo1.online).
func (lp *leproductionChecker) fetchTextWithRedirects(req *http.Request, target string) (string, bool) {
	client := &http.Client{
		Timeout: 15 * time.Second,
	}

	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, target, nil)
	if err != nil {
		return "", false
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	httpReq.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	httpReq.Header.Set("Referer", lp.host+"/")

	resp, err := client.Do(httpReq)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Debug().Int("status", resp.StatusCode).Str("url", target).Msg("leproduction: iframe bad status")
		return "", false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", false
	}
	return string(body), true
}

// ── UTIL ────────────────────────────────────────────────────────────

var leprodEpNumRe = regexp.MustCompile(`(\d+)\s*(?:серия|эпизод|episode|ep\.?)`)

// leprodNormalizeURL converts an absolute URL to a relative path, or keeps it relative.
// e.g. "https://www.le-production.tv/serial/123-slug.html" → "/serial/123-slug.html"
func leprodNormalizeURL(href, host string) string {
	href = strings.TrimSpace(href)
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		if u, err := url.Parse(href); err == nil {
			return u.Path
		}
	}
	return href
}

// leprodExtractEpNum extracts episode number from a comment string like "1 серия (Title)".
func leprodExtractEpNum(comment string) int {
	lower := strings.ToLower(comment)
	m := leprodEpNumRe.FindStringSubmatch(lower)
	if len(m) >= 2 {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

// stripHTMLTags removes HTML tags from a string.
func stripHTMLTags(s string) string {
	var sb strings.Builder
	inTag := false
	for _, r := range s {
		if r == '<' {
			inTag = true
			continue
		}
		if r == '>' {
			inTag = false
			continue
		}
		if !inTag {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}
