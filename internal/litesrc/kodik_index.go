package litesrc

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	stdjson "encoding/json"

	"lampac-go/internal/proxylink"

	"github.com/rs/zerolog/log"
)

// Kodik data structures
type kodikResult struct {
	ID           string                 `json:"id"`
	Title        string                 `json:"title"`
	TitleOrig    string                 `json:"title_orig"`
	Type         string                 `json:"type"`
	Year         *int                   `json:"year"`
	Link         string                 `json:"link"`
	IMDBID       string                 `json:"imdb_id"`
	KinopoiskID  string                 `json:"kinopoisk_id"`
	Translation  kodikTranslation       `json:"translation"`
	LastSeason   int                    `json:"last_season"`
	Seasons      map[string]kodikSeason `json:"seasons"`
	MaterialData struct {
		PosterURL string `json:"poster_url"`
	} `json:"material_data"`
}

type kodikTranslation struct {
	Title string `json:"title"`
}

type kodikSeason struct {
	Link     string            `json:"link"`
	Episodes map[string]string `json:"episodes"`
}

type kodikFullSearchResponse struct {
	Results []kodikResult `json:"results"`
}

var (
	kodikDomainRe   = regexp.MustCompile(`domain="([^"]+)"`)
	kodikDSignRe    = regexp.MustCompile(`d_sign="([^"]+)"`)
	kodikPdRe       = regexp.MustCompile(`pd="([^"]+)"`)
	kodikPdSignRe   = regexp.MustCompile(`pd_sign="([^"]+)"`)
	kodikRefRe      = regexp.MustCompile(`ref="([^"]+)"`)
	kodikRefSignRe  = regexp.MustCompile(`ref_sign="([^"]+)"`)
	kodikTypeRe     = regexp.MustCompile(`(?:videoInfo|vInfo)\.type\s*=\s*'([^']+)'`)
	kodikHashRe     = regexp.MustCompile(`(?:videoInfo|vInfo)\.hash\s*=\s*'([^']+)'`)
	kodikIDRe       = regexp.MustCompile(`(?:videoInfo|vInfo)\.id\s*=\s*'([^']+)'`)
	kodikPlayerJSRe = regexp.MustCompile(`src="/(assets/js/app\.player_[^"]+\.js)"`)
	kodikPostURLRe  = regexp.MustCompile(`type:"POST",url:atob\("([^"]+)"\)`)
	kodikStreamRe   = regexp.MustCompile(`"([0-9]+)p?":\[\{"src":"([^"]+)`)
	kodikAlphaRe    = regexp.MustCompile(`[a-zA-Z]`)
)

// kodikIsMovie returns true if the Kodik type represents a movie.
func kodikIsMovie(t string) bool {
	switch t {
	case "foreign-movie", "soviet-cartoon", "foreign-cartoon", "russian-cartoon", "anime", "russian-movie":
		return true
	}
	return false
}

// indexLocal handles the full Kodik index in local mode.
func (k *KodikChecker) indexLocal(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	imdbID := strings.TrimSpace(q.Get("imdb_id"))
	kpIDStr := strings.TrimSpace(q.Get("kinopoisk_id"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	clarification, _ := strconv.Atoi(strings.TrimSpace(q.Get("clarification")))
	pick := strings.TrimSpace(q.Get("pick"))
	kid := strings.TrimSpace(q.Get("kid"))
	sParam, _ := strconv.Atoi(strings.TrimSpace(q.Get("s")))
	rjson := parseBoolParam(q.Get("rjson"))
	similar := parseBoolParam(q.Get("similar"))
	kpID, _ := strconv.ParseInt(kpIDStr, 10, 64)

	host := hostFromRequest(req)

	if k.token == "" {
		log.Warn().Msg("kodik: no token configured")
		writeGetsTVEmpty(w, rjson)
		return
	}

	var results []kodikResult

	if similar || clarification == 1 || (kpID == 0 && imdbID == "") {
		// Search by title
		searchTitle := originalTitle
		if searchTitle == "" {
			searchTitle = title
		}
		if searchTitle == "" {
			writeGetsTVEmpty(w, rjson)
			return
		}

		results = k.searchByTitle(req, searchTitle)
		if len(results) == 0 && searchTitle == originalTitle && title != "" {
			results = k.searchByTitle(req, title)
		}

		if pick == "" {
			// Show similar list
			k.writeSimilarList(w, host, title, originalTitle, clarification, rjson, results)
			return
		}

		// Filter by pick
		filtered := make([]kodikResult, 0)
		pickLower := strings.ToLower(strings.TrimSpace(pick))
		for _, r := range results {
			if strings.ToLower(strings.TrimSpace(r.Title)) == pickLower {
				filtered = append(filtered, r)
			}
		}
		results = filtered
	} else {
		// Search by kinopoisk_id / imdb_id
		results = k.searchByID(req, imdbID, kpID, sParam)
		if len(results) == 0 {
			// Fallback to title search
			k.indexLocal(w, &http.Request{
				URL: mustParseURL(fmt.Sprintf("/lite/kodik?rjson=%v&title=%s&original_title=%s",
					rjson, url.QueryEscape(title), url.QueryEscape(originalTitle))),
			}, links)
			return
		}
	}

	if len(results) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	encTitle := url.QueryEscape(title)
	encOrigTitle := url.QueryEscape(originalTitle)
	encPick := url.QueryEscape(pick)

	if kodikIsMovie(results[0].Type) {
		// Movie mode
		k.writeMovieResults(w, req, host, title, originalTitle, encTitle, encOrigTitle, rjson, results, links)
	} else {
		// Serial mode
		k.writeSerialResults(w, req, host, imdbID, kpIDStr, title, originalTitle, encTitle, encOrigTitle, encPick, clarification, kid, sParam, rjson, results, links)
	}
}

func (k *KodikChecker) searchByTitle(req *http.Request, title string) []kodikResult {
	u := fmt.Sprintf("%s/search?token=%s&limit=100&title=%s&with_episodes=true&with_material_data=true",
		k.apiHost, k.token, url.QueryEscape(title))

	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, u, nil)
	if err != nil {
		return nil
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")

	resp, err := k.client.Do(httpReq)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}

	var root kodikFullSearchResponse
	if err := stdjson.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&root); err != nil {
		return nil
	}
	return root.Results
}

func (k *KodikChecker) searchByID(req *http.Request, imdbID string, kpID int64, season int) []kodikResult {
	u := fmt.Sprintf("%s/search?token=%s&limit=100&with_episodes=true", k.apiHost, k.token)
	if kpID > 0 {
		u += fmt.Sprintf("&kinopoisk_id=%d", kpID)
	}
	if imdbID != "" {
		u += "&imdb_id=" + url.QueryEscape(imdbID)
	}
	if season > 0 {
		u += fmt.Sprintf("&season=%d", season)
	}

	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, u, nil)
	if err != nil {
		return nil
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")

	resp, err := k.client.Do(httpReq)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}

	var root kodikFullSearchResponse
	if err := stdjson.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&root); err != nil {
		return nil
	}
	return root.Results
}

func (k *KodikChecker) writeSimilarList(w http.ResponseWriter, host, title, originalTitle string, clarification int, rjson bool, results []kodikResult) {
	if len(results) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	encTitle := url.QueryEscape(title)
	encOrigTitle := url.QueryEscape(originalTitle)

	seen := make(map[string]bool)
	rows := make([]map[string]any, 0)

	for _, r := range results {
		pickVal := strings.ToLower(strings.TrimSpace(r.Title))
		if pickVal == "" || seen[pickVal] {
			continue
		}
		seen[pickVal] = true

		name := r.Title
		if r.TitleOrig != "" {
			name = r.Title + " / " + r.TitleOrig
		}

		details := r.Translation.Title
		if r.LastSeason > 0 {
			details += fmt.Sprintf(" | %dй сезон", r.LastSeason)
		}

		link := fmt.Sprintf("%s/lite/kodik?title=%s&original_title=%s&clarification=%d&pick=%s",
			host, encTitle, encOrigTitle, clarification, url.QueryEscape(pickVal))

		yearStr := ""
		if r.Year != nil {
			yearStr = strconv.Itoa(*r.Year)
		}

		rows = append(rows, map[string]any{
			"method":  "link",
			"url":     link,
			"similar": true,
			"title":   name,
			"year":    yearStr,
			"details": details,
		})
	}

	if len(rows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "similar",
			"data": rows,
		})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range rows {
		getsTVAppendSeasonHTML(&sb, row, fmt.Sprint(row["title"]), i == 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (k *KodikChecker) writeMovieResults(w http.ResponseWriter, req *http.Request, host, title, originalTitle, encTitle, encOrigTitle string, rjson bool, results []kodikResult, links *proxylink.Manager) {
	rows := make([]map[string]any, 0, len(results))
	for _, data := range results {
		videoURL := fmt.Sprintf("%s/lite/kodik/video?title=%s&original_title=%s&link=%s",
			host, encTitle, encOrigTitle, url.QueryEscape(data.Link))
		streamURL := videoURL + "&play=true"

		rows = append(rows, map[string]any{
			"method": "call",
			"url":    videoURL,
			"stream": streamURL,
			"name":   data.Translation.Title,
			"title":  title,
		})
	}

	if len(rows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "movie",
			"data": rows,
		})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range rows {
		name := fmt.Sprint(row["name"])
		if name == "" {
			name = title
		}
		getsTVAppendMovieHTML(&sb, row, name, i == 0, 0, 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (k *KodikChecker) writeSerialResults(w http.ResponseWriter, req *http.Request, host, imdbID, kpIDStr, title, originalTitle, encTitle, encOrigTitle, encPick string, clarification int, kid string, s int, rjson bool, results []kodikResult, links *proxylink.Manager) {
	if s <= 0 {
		// Season list
		seen := make(map[int]bool)
		rows := make([]map[string]any, 0)

		// Iterate in reverse to get earliest seasons first
		for i := len(results) - 1; i >= 0; i-- {
			item := results[i]
			season := item.LastSeason
			if seen[season] {
				continue
			}
			seen[season] = true

			link := fmt.Sprintf("%s/lite/kodik?rjson=%v&imdb_id=%s&kinopoisk_id=%s&title=%s&original_title=%s&clarification=%d&pick=%s&s=%d",
				host, rjson, url.QueryEscape(imdbID), url.QueryEscape(kpIDStr), encTitle, encOrigTitle, clarification, encPick, season)

			rows = append(rows, map[string]any{
				"method": "link",
				"url":    link,
				"title":  fmt.Sprintf("%d сезон", season),
				"s":      season,
			})
		}

		if len(rows) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
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
			getsTVAppendSeasonHTML(&sb, row, fmt.Sprint(row["title"]), i == 0)
		}
		sb.WriteString(`</div>`)
		writeHTML(w, http.StatusOK, sb.String())
		return
	}

	// Episode list for season s
	// Build voice selector
	seen := make(map[string]bool)
	type voiceEntry struct {
		name string
		id   string
	}
	var voiceEntries []voiceEntry

	for _, item := range results {
		if item.ID == "" {
			continue
		}
		name := item.Translation.Title
		if name == "" {
			name = "оригинал"
		}
		if seen[name] {
			continue
		}
		// Check if this result has the season
		if item.LastSeason != s {
			if item.Seasons == nil {
				continue
			}
			if _, ok := item.Seasons[strconv.Itoa(s)]; !ok {
				continue
			}
		}
		seen[name] = true
		if kid == "" {
			kid = item.ID
		}
		voiceEntries = append(voiceEntries, voiceEntry{name: name, id: item.ID})
	}

	// Find selected result
	var selected *kodikResult
	for i := range results {
		if results[i].ID == kid {
			selected = &results[i]
			break
		}
	}
	if selected == nil && len(results) > 0 {
		selected = &results[0]
	}
	if selected == nil {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Get episodes for season
	sKey := strconv.Itoa(s)
	var episodes map[string]string
	if selected.Seasons != nil {
		if seasonData, ok := selected.Seasons[sKey]; ok && len(seasonData.Episodes) > 0 {
			episodes = seasonData.Episodes
		}
	}

	if len(episodes) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Sort episode keys
	epKeys := make([]string, 0, len(episodes))
	for k := range episodes {
		epKeys = append(epKeys, k)
	}
	sort.Slice(epKeys, func(i, j int) bool {
		a, _ := strconv.Atoi(epKeys[i])
		b, _ := strconv.Atoi(epKeys[j])
		return a < b
	})

	rows := make([]map[string]any, 0, len(epKeys))
	for _, epNum := range epKeys {
		epLink := episodes[epNum]
		videoURL := fmt.Sprintf("%s/lite/kodik/video?title=%s&original_title=%s&link=%s&episode=%s",
			host, encTitle, encOrigTitle, url.QueryEscape(epLink), url.QueryEscape(epNum))
		// stream URL with ?play=true → redirect for external players
		streamURL := videoURL + "&play=true"

		e, _ := strconv.Atoi(epNum)
		rows = append(rows, map[string]any{
			"method": "call",
			"url":    videoURL,
			"stream": streamURL,
			"name":   epNum + " серия",
			"title":  title,
			"s":      s,
			"e":      e,
		})
	}

	if len(rows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		result := map[string]any{
			"type": "episode",
			"data": rows,
		}
		if len(voiceEntries) > 0 {
			vRows := make([]map[string]any, 0, len(voiceEntries))
			for _, ve := range voiceEntries {
				vLink := fmt.Sprintf("%s/lite/kodik?rjson=%v&imdb_id=%s&kinopoisk_id=%s&title=%s&original_title=%s&clarification=%d&pick=%s&s=%d&kid=%s",
					host, rjson, url.QueryEscape(imdbID), url.QueryEscape(kpIDStr), encTitle, encOrigTitle, clarification, encPick, s, url.QueryEscape(ve.id))
				vRows = append(vRows, map[string]any{
					"method":   "link",
					"url":      vLink,
					"title":    ve.name,
					"selected": ve.id == kid,
				})
			}
			result["voice"] = vRows
		}
		writeJSON(w, http.StatusOK, result)
		return
	}

	var sb strings.Builder
	// Voice buttons
	if len(voiceEntries) > 0 {
		sb.WriteString(`<div class="videos__line">`)
		for _, ve := range voiceEntries {
			sel := ""
			if ve.id == kid {
				sel = " active"
			}
			vLink := fmt.Sprintf("%s/lite/kodik?rjson=%v&imdb_id=%s&kinopoisk_id=%s&title=%s&original_title=%s&clarification=%d&pick=%s&s=%d&kid=%s",
				host, rjson, url.QueryEscape(imdbID), url.QueryEscape(kpIDStr), encTitle, encOrigTitle, clarification, encPick, s, url.QueryEscape(ve.id))
			sb.WriteString(fmt.Sprintf(`<div class="videos__button selector%s" data-json='{"method":"link","url":"%s"}'>`, sel, vLink))
			sb.WriteString(`<div class="videos__button-text">`)
			sb.WriteString(ve.name)
			sb.WriteString(`</div></div>`)
		}
		sb.WriteString(`</div>`)
	}

	sb.WriteString(`<div class="videos__line">`)
	for i, row := range rows {
		name := fmt.Sprint(row["name"])
		getsTVAppendMovieHTML(&sb, row, name, i == 0, s, vokinoIntFromMap(row, "e"))
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// kodikStreamCache caches resolved Kodik stream URLs to avoid re-resolving
// on auto-next from external players (Vimu, MX Player).
// Key: kodik episode link, Value: {streams, expiry}.
var kodikStreamCache = struct {
	sync.RWMutex
	m map[string]kodikCachedStreams
}{m: make(map[string]kodikCachedStreams)}

type kodikCachedStream struct {
	quality string
	url     string
}

type kodikCachedStreams struct {
	streams []kodikCachedStream
	expires time.Time
}

const kodikStreamCacheTTL = 10 * time.Minute

func kodikStreamCacheGet(key string) ([]kodikCachedStream, bool) {
	kodikStreamCache.RLock()
	defer kodikStreamCache.RUnlock()
	entry, ok := kodikStreamCache.m[key]
	if !ok || time.Now().After(entry.expires) {
		return nil, false
	}
	return entry.streams, true
}

func kodikStreamCacheSet(key string, streams []kodikCachedStream) {
	kodikStreamCache.Lock()
	defer kodikStreamCache.Unlock()
	// Evict old entries if cache is too large.
	if len(kodikStreamCache.m) > 2000 {
		now := time.Now()
		for k, v := range kodikStreamCache.m {
			if now.After(v.expires) {
				delete(kodikStreamCache.m, k)
			}
		}
	}
	kodikStreamCache.m[key] = kodikCachedStreams{
		streams: streams,
		expires: time.Now().Add(kodikStreamCacheTTL),
	}
}

// KodikVideoHandler handles GET /lite/kodik/video?link={link}&title={title}&original_title={orig}&episode={ep}
func KodikVideoHandler(k *KodikChecker, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		link := strings.TrimSpace(q.Get("link"))
		title := strings.TrimSpace(q.Get("title"))
		originalTitle := strings.TrimSpace(q.Get("original_title"))
		episode, _ := strconv.Atoi(strings.TrimSpace(q.Get("episode")))
		play := parseBoolParam(q.Get("play"))

		if link == "" {
			writeGetsTVEmpty(w, false)
			return
		}

		// Check cache first — critical for external player auto-next.
		if cached, ok := kodikStreamCacheGet(link); ok && len(cached) > 0 {
			bestURL := streamProxyURL(req, cached[0].url, "kodik", links)
			if play {
				http.Redirect(w, req, bestURL, http.StatusFound)
				return
			}
			name := title
			if name == "" {
				name = originalTitle
			}
			if name == "" {
				name = "auto"
			}
			if episode > 0 {
				name += fmt.Sprintf(" (%d серия)", episode)
			}
			sq := make([]map[string]any, len(cached))
			for i, s := range cached {
				sq[i] = map[string]any{
					"quality": s.quality,
					"url":     streamProxyURL(req, s.url, "kodik", links),
				}
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"method":        "play",
				"url":           bestURL,
				"title":         name,
				"streamquality": sq,
			})
			return
		}

		// Step 1: GET the iframe page
		iframeURL := link
		if !strings.HasPrefix(iframeURL, "http") {
			iframeURL = "https:" + iframeURL
		}

		httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, iframeURL, nil)
		if err != nil {
			writeGetsTVEmpty(w, false)
			return
		}
		httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")

		resp, err := k.client.Do(httpReq)
		if err != nil {
			log.Warn().Err(err).Msg("kodik: iframe fetch failed")
			writeGetsTVEmpty(w, false)
			return
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
			log.Warn().Err(err).Int("status", resp.StatusCode).Msg("kodik video: iframe read/status fail")
			writeGetsTVEmpty(w, false)
			return
		}

		iframe := string(body)
		log.Debug().Int("len", len(iframe)).Str("link", link).Msg("kodik video: iframe fetched")

		// Parse iframe fields
		playerJS := findMatch(kodikPlayerJSRe, iframe)
		// Extract fields between advertDebug and preview-icons
		advertParts := strings.SplitN(iframe, "advertDebug", 2)
		if len(advertParts) < 2 {
			log.Warn().Str("iframe_prefix", iframe[:min(len(iframe), 300)]).Msg("kodik: no advertDebug section")
			writeGetsTVEmpty(w, false)
			return
		}
		previewParts := strings.SplitN(advertParts[1], "preview-icons", 2)
		if len(previewParts) < 1 {
			log.Warn().Msg("kodik: no preview-icons section")
			writeGetsTVEmpty(w, false)
			return
		}

		frame := strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}, previewParts[0])

		domain := findMatch(kodikDomainRe, frame)
		dSign := findMatch(kodikDSignRe, frame)
		pd := findMatch(kodikPdRe, frame)
		pdSign := findMatch(kodikPdSignRe, frame)
		refDomain := findMatch(kodikRefRe, frame)
		refSign := findMatch(kodikRefSignRe, frame)
		vType := findMatch(kodikTypeRe, frame)
		vHash := findMatch(kodikHashRe, frame)
		vID := findMatch(kodikIDRe, frame)

		log.Debug().Str("domain", domain).Str("playerJS", playerJS).Str("type", vType).Str("hash", vHash).Str("id", vID).Msg("kodik video: iframe parsed")

		if domain == "" || playerJS == "" {
			log.Warn().Str("frame_prefix", frame[:min(len(frame), 300)]).Msg("kodik: missing domain or playerJS")
			writeGetsTVEmpty(w, false)
			return
		}

		// Step 2: Fetch player JS to get POST URL
		linkHost := strings.TrimRight(iframeURL, "/")
		// Extract scheme+host from iframeURL
		if pu, err := url.Parse(iframeURL); err == nil {
			linkHost = pu.Scheme + "://" + pu.Host
		}

		postURI := k.fetchPostURI(req, linkHost+"/"+playerJS)
		if postURI == "" {
			log.Warn().Str("jsURL", linkHost+"/"+playerJS).Msg("kodik: failed to extract POST URL from player JS")
			writeGetsTVEmpty(w, false)
			return
		}

		// Step 3: POST to get video links
		formData := fmt.Sprintf("d=%s&d_sign=%s&pd=%s&pd_sign=%s&ref=%s&ref_sign=%s&bad_user=false&cdn_is_working=true&type=%s&hash=%s&id=%s&info=%%7B%%7D",
			url.QueryEscape(domain), url.QueryEscape(dSign), url.QueryEscape(pd),
			url.QueryEscape(pdSign), url.QueryEscape(refDomain), url.QueryEscape(refSign),
			url.QueryEscape(vType), url.QueryEscape(vHash), url.QueryEscape(vID))

		log.Debug().Str("postURL", linkHost+postURI).Str("form", formData[:min(len(formData), 200)]).Msg("kodik video: POST request")

		postReq, err := http.NewRequestWithContext(req.Context(), http.MethodPost, linkHost+postURI, strings.NewReader(formData))
		if err != nil {
			writeGetsTVEmpty(w, false)
			return
		}
		postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		postReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
		postReq.Header.Set("Referer", iframeURL)

		postResp, err := k.client.Do(postReq)
		if err != nil {
			log.Warn().Err(err).Msg("kodik: POST failed")
			writeGetsTVEmpty(w, false)
			return
		}
		defer postResp.Body.Close()

		postBody, err := io.ReadAll(io.LimitReader(postResp.Body, 1<<20))
		if err != nil || postResp.StatusCode < 200 || postResp.StatusCode >= 300 {
			log.Warn().Err(err).Int("status", postResp.StatusCode).Msg("kodik video: POST read/status fail")
			writeGetsTVEmpty(w, false)
			return
		}

		log.Debug().Int("len", len(postBody)).Str("body_prefix", string(postBody[:min(len(postBody), 300)])).Msg("kodik video: POST response")

		// Parse streams
		type stream struct {
			quality string
			url     string
		}
		var streams []stream

		for _, m := range kodikStreamRe.FindAllStringSubmatch(string(postBody), -1) {
			if len(m) < 3 || m[2] == "" {
				continue
			}
			src := m[2]

			// Decode: if not already manifest.m3u8, apply ROT+base64 decode
			if !strings.Contains(src, "manifest.m3u8") {
				decoded := kodikDecodeURL(src)
				if decoded != "" {
					src = decoded
				}
			}

			if strings.HasPrefix(src, "//") {
				src = "https:" + src
			}

			streams = append(streams, stream{
				quality: m[1] + "p",
				url:     src,
			})
		}

		if len(streams) == 0 {
			log.Warn().Msg("kodik: no streams parsed")
			writeGetsTVEmpty(w, false)
			return
		}

		// Reverse (highest quality first)
		for i, j := 0, len(streams)-1; i < j; i, j = i+1, j-1 {
			streams[i], streams[j] = streams[j], streams[i]
		}

		// Cache resolved streams for external player auto-next.
		cachedStreams := make([]kodikCachedStream, len(streams))
		for i, s := range streams {
			cachedStreams[i] = kodikCachedStream{quality: s.quality, url: s.url}
		}
		kodikStreamCacheSet(link, cachedStreams)

		bestURL := streamProxyURL(req, streams[0].url, "kodik", links)

		if play {
			http.Redirect(w, req, bestURL, http.StatusFound)
			return
		}

		name := title
		if name == "" {
			name = originalTitle
		}
		if name == "" {
			name = "auto"
		}
		if episode > 0 {
			name += fmt.Sprintf(" (%d серия)", episode)
		}

		sq := make([]map[string]any, len(streams))
		for i, s := range streams {
			sq[i] = map[string]any{
				"quality": s.quality,
				"url":     streamProxyURL(req, s.url, "kodik", links),
			}
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"method":        "play",
			"url":           bestURL,
			"title":         name,
			"streamquality": sq,
		})
	}
}

func (k *KodikChecker) fetchPostURI(req *http.Request, jsURL string) string {
	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, jsURL, nil)
	if err != nil {
		log.Debug().Err(err).Msg("kodik: fetchPostURI request build failed")
		return ""
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")

	resp, err := k.client.Do(httpReq)
	if err != nil {
		log.Debug().Err(err).Str("url", jsURL).Msg("kodik: fetchPostURI request failed")
		return ""
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Debug().Err(err).Int("status", resp.StatusCode).Str("url", jsURL).Msg("kodik: fetchPostURI read/status fail")
		return ""
	}

	b64 := findMatch(kodikPostURLRe, string(body))
	if b64 == "" {
		log.Debug().Str("url", jsURL).Int("body_len", len(body)).Msg("kodik: fetchPostURI no POST URL found in JS")
		return ""
	}

	decoded, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		log.Debug().Err(err).Str("b64", b64).Msg("kodik: fetchPostURI base64 decode failed")
		return ""
	}
	log.Debug().Str("postURI", string(decoded)).Msg("kodik: fetchPostURI OK")
	return string(decoded)
}

// kodikDecodeURL decodes the obfuscated Kodik URL (ROT cipher + URL-safe base64).
func kodikDecodeURL(s string) string {
	if s == "" {
		return ""
	}

	// ROT18 cipher: shift each letter by 18 positions
	decoded := kodikAlphaRe.ReplaceAllStringFunc(s, func(ch string) string {
		c := rune(ch[0])
		upper := rune('Z')
		limit := 90 // 'Z'
		if c > upper {
			limit = 122 // 'z'
		}
		shifted := int(c) + 18
		if shifted > limit {
			shifted -= 26
		}
		return string(rune(shifted))
	})

	// URL-safe base64 decode
	decoded = strings.ReplaceAll(decoded, "-", "+")
	decoded = strings.ReplaceAll(decoded, "_", "/")

	// Pad to multiple of 4
	if mod := len(decoded) % 4; mod != 0 {
		decoded += strings.Repeat("=", 4-mod)
	}

	result, err := base64.StdEncoding.DecodeString(decoded)
	if err != nil {
		return ""
	}
	return string(result)
}

func findMatch(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}

func mustParseURL(raw string) *url.URL {
	u, _ := url.Parse(raw)
	return u
}
