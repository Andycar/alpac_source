package litesrc

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"lampac-go/internal/proxylink"

	"github.com/rs/zerolog/log"
)

var (
	animevostDataRe    = regexp.MustCompile(`var data = ([^\n\r]+)`)
	animevostEpRe      = regexp.MustCompile(`"([^"]+)":"([0-9]+)",`)
	animevostSeasonRe  = regexp.MustCompile(`(?i)([0-9 ]+) ?nd `)
	animevostSeasonRe2 = regexp.MustCompile(`(?i)Season ([0-9]+)`)
	animevostLinkRe    = regexp.MustCompile(`(?is)class="shortstory".*?<a href="(https?://[^"]+\.html)">([^<]+)</a>.*?(?:<strong>Год выхода: ?</strong>\s*([0-9]{4})</p>)?`)
	animevostImgRe     = regexp.MustCompile(`src="(/uploads/[^"]+)"`)
	// Legacy: download="invoice" links (deprecated by upstream).
	animevostMP4_720 = regexp.MustCompile(`download="invoice"[^>]+href="(https?://[^"]+)">720p`)
	animevostMP4_480 = regexp.MustCompile(`download="invoice"[^>]+href="(https?://[^"]+)">480p`)
	// New: PlayerJS file field — [Quality Label]URL1 or URL2,...
	// Note: 720р uses Cyrillic "р" (U+0440), not Latin "p".
	animevostPJSFile    = regexp.MustCompile(`"file"\s*:\s*"([^"]+)"`)
	animevostPJSQuality = regexp.MustCompile(`\[([^\]]+)\](https?://[^ ,]+)`)
)

// animevostIndex implements the full local core index handler for animevost.
func (a *animevostChecker) indexLocal(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	title := strings.TrimSpace(q.Get("title"))
	if title == "" {
		writeGetsTVEmpty(w, parseBoolParam(q.Get("rjson")))
		return
	}

	rjson := parseBoolParam(q.Get("rjson"))
	uri := strings.TrimSpace(q.Get("uri"))
	sParam := strings.TrimSpace(q.Get("s"))
	similar := parseBoolParam(q.Get("similar"))
	year, _ := strconv.Atoi(strings.TrimSpace(q.Get("year")))

	host := hostFromRequest(req)

	if uri == "" {
		// Search
		a.animevostSearch(w, req, host, title, year, rjson, similar)
		return
	}

	// Episodes
	a.animevostEpisodes(w, req, host, title, uri, sParam, rjson, links)
}

func (a *animevostChecker) animevostSearch(w http.ResponseWriter, req *http.Request, host, title string, year int, rjson, similar bool) {
	searchHTML, ok := a.fetchSearch(req, title)
	if !ok || searchHTML == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	type searchResult struct {
		title   string
		year    string
		uri     string
		season  string
		img     string
		matched bool
	}

	matches := animevostLinkRe.FindAllStringSubmatch(searchHTML, -1)
	if len(matches) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	var results []searchResult
	var catalog []searchResult

	for _, m := range matches {
		if len(m) < 3 {
			continue
		}
		rURI := strings.TrimSpace(m[1])
		rTitle := strings.TrimSpace(m[2])
		rYear := ""
		if len(m) >= 4 {
			rYear = strings.TrimSpace(m[3])
		}

		// Extract season
		season := "1"
		if sm := animevostSeasonRe.FindStringSubmatch(rTitle); len(sm) >= 2 {
			season = strings.TrimSpace(sm[1])
		} else if sm := animevostSeasonRe2.FindStringSubmatch(rTitle); len(sm) >= 2 {
			season = strings.TrimSpace(sm[1])
		}

		// Image
		// Find img near this result in the HTML (simplified — just look for first img in same block)
		img := ""
		// Use img from entire search — since we don't have per-block isolation, skip img for now

		sr := searchResult{
			title:  rTitle,
			year:   rYear,
			uri:    rURI,
			season: season,
			img:    img,
		}
		results = append(results, sr)

		// Check exact match
		if year > 0 && rYear == strconv.Itoa(year) && strings.Contains(normalizeSearchTitle(rTitle), normalizeSearchTitle(title)) {
			sr.matched = true
			catalog = append(catalog, sr)
		}
	}

	if len(results) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// If not similar and we have exactly 1 catalog match, redirect to it
	if !similar && len(catalog) == 1 {
		a.animevostEpisodes(w, req, host, title, catalog[0].uri, catalog[0].season, rjson, nil)
		return
	}

	// Prefer catalog if we have exact matches, else show all
	showResults := results
	if !similar && len(catalog) > 0 {
		showResults = catalog
	}

	rows := make([]map[string]any, 0, len(showResults))
	for _, r := range showResults {
		link := fmt.Sprintf("%s/lite/animevost?rjson=%v&title=%s&uri=%s&s=%s",
			host, rjson, url.QueryEscape(title), url.QueryEscape(r.uri), url.QueryEscape(r.season))
		rows = append(rows, map[string]any{
			"method":  "link",
			"url":     link,
			"similar": true,
			"title":   r.title,
			"year":    r.year,
		})
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

func (a *animevostChecker) animevostEpisodes(w http.ResponseWriter, req *http.Request, host, title, uri, sParam string, rjson bool, links *proxylink.Manager) {
	// Fetch the anime page to extract episode data
	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, uri, nil)
	if err != nil {
		writeGetsTVEmpty(w, rjson)
		return
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		log.Warn().Err(err).Msg("animevost: page fetch failed")
		writeGetsTVEmpty(w, rjson)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	html := string(body)

	// Extract var data = {"Episode 1":"12345","Episode 2":"12346",...}
	dataMatch := animevostDataRe.FindStringSubmatch(html)
	if len(dataMatch) < 2 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	eps := animevostEpRe.FindAllStringSubmatch(dataMatch[1], -1)
	if len(eps) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	s, _ := strconv.Atoi(sParam)

	rows := make([]map[string]any, 0, len(eps))
	for _, ep := range eps {
		if len(ep) < 3 {
			continue
		}
		epName := strings.TrimSpace(ep[1])
		epID := strings.TrimSpace(ep[2])

		link := fmt.Sprintf("%s/lite/animevost/video?id=%s&title=%s", host, url.QueryEscape(epID), url.QueryEscape(title))

		eNum := ""
		if m := regexp.MustCompile(`^([0-9]+)`).FindString(epName); m != "" {
			eNum = m
		}
		e, _ := strconv.Atoi(eNum)

		rows = append(rows, map[string]any{
			"method": "call",
			"url":    link,
			"name":   epName,
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
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "episode",
			"data": rows,
		})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range rows {
		name := fmt.Sprint(row["name"])
		getsTVAppendMovieHTML(&sb, row, name, i == 0, s, vokinoIntFromMap(row, "e"))
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// AnimevostVideoHandler handles GET /lite/animevost/video?id={id}&title={title}
func AnimevostVideoHandler(a *animevostChecker, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		id := strings.TrimSpace(q.Get("id"))
		title := strings.TrimSpace(q.Get("title"))
		play := parseBoolParam(q.Get("play"))

		if id == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing id"})
			return
		}

		frameURL := fmt.Sprintf("%s/frame5.php?play=%s&old=1", a.host, url.QueryEscape(id))

		httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, frameURL, nil)
		if err != nil {
			writeGetsTVEmpty(w, false)
			return
		}
		httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")

		resp, err := a.client.Do(httpReq)
		if err != nil {
			log.Warn().Err(err).Msg("animevost: frame5 fetch failed")
			writeGetsTVEmpty(w, false)
			return
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
			writeGetsTVEmpty(w, false)
			return
		}

		html := string(body)

		type stream struct {
			url     string
			quality string
		}
		var streams []stream

		// Try legacy download="invoice" links first.
		if m := animevostMP4_720.FindStringSubmatch(html); len(m) >= 2 {
			streams = append(streams, stream{url: m[1], quality: "720p"})
		}
		if m := animevostMP4_480.FindStringSubmatch(html); len(m) >= 2 {
			streams = append(streams, stream{url: m[1], quality: "480p"})
		}

		// Fallback: parse PlayerJS "file" field.
		// Format: [SD (480p)]URL1 or URL2,[HD (720р)]URL3 or URL4
		if len(streams) == 0 {
			if fm := animevostPJSFile.FindStringSubmatch(html); len(fm) >= 2 {
				fileStr := fm[1]
				matches := animevostPJSQuality.FindAllStringSubmatch(fileStr, -1)
				for _, m := range matches {
					if len(m) >= 3 {
						label := m[1]  // e.g. "HD (720р)" or "SD (480p)"
						rawURL := m[2] // first URL before " or "
						// Pick best URL (first one, before " or ").
						if idx := strings.Index(rawURL, " or "); idx > 0 {
							rawURL = rawURL[:idx]
						}
						// Normalize quality label.
						q := "480p"
						if strings.Contains(label, "720") || strings.Contains(label, "HD") {
							q = "720p"
						} else if strings.Contains(label, "1080") || strings.Contains(label, "FHD") {
							q = "1080p"
						}
						streams = append(streams, stream{url: rawURL, quality: q})
					}
				}
				// Put best quality first.
				for i, s := range streams {
					if s.quality == "1080p" || s.quality == "720p" {
						streams[0], streams[i] = streams[i], streams[0]
						break
					}
				}
			}
		}

		if len(streams) == 0 {
			writeGetsTVEmpty(w, false)
			return
		}

		bestURL := streamProxyURL(req, streams[0].url, "animevost", links)

		if play {
			http.Redirect(w, req, bestURL, http.StatusFound)
			return
		}

		sq := make([]map[string]any, len(streams))
		for i, s := range streams {
			sq[i] = map[string]any{
				"quality": s.quality,
				"url":     streamProxyURL(req, s.url, "animevost", links),
			}
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"method":        "play",
			"url":           bestURL,
			"title":         title,
			"streamquality": sq,
		})
	}
}
