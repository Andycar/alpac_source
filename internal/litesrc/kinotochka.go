package litesrc

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"html"
	"io"
	"lampac-go/internal/httpclient"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/proxylink"
)

var (
	kinotochkaHTMLLinkRe         = regexp.MustCompile(`href="https?://[^"]+\.html"`)
	kinotochkaHTMLLinkCaptureRe  = regexp.MustCompile(`href="(https?://[^"]+\.html)"`)
	kinotochkaSeasonHeaderRe     = regexp.MustCompile(`(?is)<h2>([^<]+)\s+(([0-9]+)\s+Сезон)\s+\([0-9]{4}\)</h2>`)
	kinotochkaSeasonNumRe        = regexp.MustCompile(`-([0-9]+)-sezon`)
	kinotochkaPlaylistFileRe     = regexp.MustCompile(`file:"(https?://[^"]+\.txt)"`)
	kinotochkaPlaylistTailRe     = regexp.MustCompile(`\[[^\]]+,([0-9]+)\]\.mp4$`)
	kinotochkaEpisodeNumRe       = regexp.MustCompile(`^\s*([0-9]+)`)
	kinotochkaMovieFileByIDRe    = regexp.MustCompile(`(?is)id\s*:\s*['"]playerjshd['"].{0,800}?file\s*:\s*['"]([^'"]+)['"]`)
	kinotochkaMovieFileByFileRe  = regexp.MustCompile(`(?is)file\s*:\s*['"]([^'"]+)['"].{0,800}?id\s*:\s*['"]playerjshd['"]`)
	kinotochkaMovieFileGenericRe = regexp.MustCompile(`(?is)\bfile\s*:\s*['"]([^'"]+)['"]`)
	kinotochkaMovieURLRe         = regexp.MustCompile(`https?://[^\s"'<>\\,]+`)
)

type kinotochkaChecker struct {
	client       *http.Client
	host         string
	cookie       string
	clientStream bool
}

// kinotochkaBudget bounds one request's upstream fetch chain. The movie path can
// legally issue a dozen+ sequential fetches (embed → find-by-kinopoisk → candidate
// pages → title search × 2 → more pages → per-quality playlist resolves); when the
// site is slow or blackholed each hop eats the full client timeout, and one card's
// resolve burned the entire 25s capi drill budget while returning nothing. A total
// fetch cap plus a consecutive-transport-failure cutoff makes the chain fail fast
// instead — a dead upstream now costs ~2 timeouts, not the whole budget.
type kinotochkaBudget struct {
	fetches int
	failRun int // consecutive transport failures (timeouts, refused) — resets on any success
}

type kinotochkaBudgetKey struct{}

const (
	kinotochkaMaxFetches = 10
	kinotochkaMaxFailRun = 2
)

func kinotochkaWithBudget(req *http.Request) *http.Request {
	if _, ok := req.Context().Value(kinotochkaBudgetKey{}).(*kinotochkaBudget); ok {
		return req
	}
	return req.WithContext(context.WithValue(req.Context(), kinotochkaBudgetKey{}, &kinotochkaBudget{}))
}

func kinotochkaBudgetOf(req *http.Request) *kinotochkaBudget {
	b, _ := req.Context().Value(kinotochkaBudgetKey{}).(*kinotochkaBudget)
	return b
}

type kinotochkaSearchItem struct {
	URL string `json:"url"`
}

type kinotochkaSeasonLink struct {
	Name   string
	Season int
	URL    string
}

type kinotochkaPlaylistRoot struct {
	Playlist []kinotochkaPlaylistItem `json:"playlist"`
}

type kinotochkaPlaylistItem struct {
	Comment string `json:"comment"`
	File    string `json:"file"`
}

func NewKinotochkaChecker(cfg config.Config) *kinotochkaChecker {
	host := strings.TrimSpace(cfg.Online.Kinotochka.Host)
	if host == "" {
		// kinovibe.vip 301-redirects here now; following that redirect turns the DLE
		// search POST into a body-less GET (Go rewrites 301 POST→GET), so the search
		// silently returns nothing — point straight at the live mirror.
		host = "https://kinovibe.cc"
	}
	host = strings.TrimRight(host, "/")

	return &kinotochkaChecker{
		client:       kinotochkaHTTPClient(),
		host:         host,
		cookie:       strings.TrimSpace(cfg.Online.Kinotochka.Cookie),
		clientStream: cfg.Online.Kinotochka.ClientStream,
	}
}

// kinotochkaHTTPClient mirrors anwapHTTPClient: do NOT fall back to the shared
// http/1.1 transport — this edge (like anwap's and uafilm's) stalls the TLS
// handshake for clients that offer http/1.1 only, so every fetch died on the 10s
// timeout and one card's resolve burned the whole capi drill budget. A proxy
// explicitly mapped to the balancer still wins — only the fallback changes.
func kinotochkaHTTPClient() *http.Client {
	if t := httpclient.TransportForBalancer("kinotochka"); t != nil {
		return &http.Client{Transport: t, Timeout: 10 * time.Second}
	}
	t, _ := http.DefaultTransport.(*http.Transport)
	if t == nil {
		return &http.Client{Timeout: 10 * time.Second}
	}
	tr := t.Clone()
	tr.ForceAttemptHTTP2 = true
	return &http.Client{Transport: tr, Timeout: 10 * time.Second}
}

// kinotochkaUserAgent matches what /proxy/ sends upstream by default, so a
// client playing the raw CDN URL directly looks identical to the proxied path.
const kinotochkaUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"

func kinotochkaStreamHeaders() map[string]string {
	return map[string]string{"User-Agent": kinotochkaUserAgent}
}

// streamURL picks the stream URL form. When direct (client_stream on + an
// Android apk client, per ?rchtype), it returns the raw CDN URL so the device
// fetches the stream itself (CDN->device, server out of the byte path; the UA
// rides in the play object's headers). Otherwise it wraps through /proxy/ as
// before — so non-apk clients and client_stream=false stay byte-for-byte
// unchanged.
func (k *kinotochkaChecker) streamURL(req *http.Request, rawURL string, links *proxylink.Manager, direct bool) string {
	if direct {
		return strings.TrimSpace(rawURL)
	}
	return streamProxyURL(req, rawURL, "kinotochka", links)
}

func (k *kinotochkaChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		req = kinotochkaWithBudget(req) // bound the whole fetch chain of this request
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show, quality := k.checkSearchWithQuality(req)
			writeCheckSearchResponse(w, show, quality)
			return
		}

		k.index(w, req, links)
	}
}

func (k *kinotochkaChecker) index(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))

	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	if title == "" {
		title = originalTitle
	}

	serial, _ := getsTVQueryInt(q.Get("serial"))
	seasonNum, seasonSet := getsTVQueryInt(q.Get("s"))
	if !seasonSet {
		seasonNum = -1
	}

	kinopoiskID := parseInt64(q.Get("kinopoisk_id"))
	if serial == 1 {
		if seasonNum == -1 {
			k.writeSeasons(w, req, rjson, title, originalTitle, kinopoiskID)
			return
		}
		k.writeEpisodes(w, req, rjson, title, originalTitle, seasonNum, links)
		return
	}

	k.writeMovie(w, req, rjson, title, originalTitle, kinopoiskID, links)
}

func (k *kinotochkaChecker) writeSeasons(
	w http.ResponseWriter,
	req *http.Request,
	rjson bool,
	title string,
	originalTitle string,
	kinopoiskID int64,
) {
	if title == "" {
		title = originalTitle
	}
	if title == "" && kinopoiskID <= 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	var links []kinotochkaSeasonLink
	if kinopoiskID > 0 {
		links = k.seasonsByKinopoisk(req, kinopoiskID)
	} else {
		links = k.seasonsByTitle(req, title)
	}
	if len(links) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(req)
	encTitle := url.QueryEscape(title)
	encOriginalTitle := url.QueryEscape(originalTitle)

	data := make([]map[string]any, 0, len(links))
	labels := make([]string, 0, len(links))
	for _, item := range links {
		if item.Season < 1 || strings.TrimSpace(item.URL) == "" {
			continue
		}
		link := fmt.Sprintf(
			"%s/lite/kinotochka?rjson=%s&title=%s&original_title=%s&serial=1&s=%d&newsuri=%s",
			host,
			getsTVBool(rjson),
			encTitle,
			encOriginalTitle,
			item.Season,
			url.QueryEscape(item.URL),
		)
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = fmt.Sprintf("%d сезон", item.Season)
		}
		data = append(data, map[string]any{
			"method": "link",
			"id":     item.Season,
			"url":    link,
			"name":   name,
		})
		labels = append(labels, name)
	}

	if len(data) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "season",
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

func (k *kinotochkaChecker) seasonsByKinopoisk(req *http.Request, kinopoiskID int64) []kinotochkaSeasonLink {
	u, err := url.Parse(k.host + "/api/find-by-kinopoisk.php")
	if err != nil {
		return nil
	}
	qs := url.Values{}
	qs.Set("kinopoisk", strconv.FormatInt(kinopoiskID, 10))
	u.RawQuery = qs.Encode()

	body, ok := k.fetch(req, http.MethodGet, u.String(), "", "")
	if !ok {
		return nil
	}

	var root []kinotochkaSearchItem
	if err := stdjson.Unmarshal(body, &root); err != nil || len(root) == 0 {
		return nil
	}

	out := make([]kinotochkaSeasonLink, 0, len(root))
	for _, item := range root {
		uri := strings.TrimSpace(item.URL)
		if uri == "" {
			continue
		}
		m := kinotochkaSeasonNumRe.FindStringSubmatch(uri)
		if len(m) != 2 {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(m[1]))
		if err != nil || n < 1 {
			continue
		}
		out = append(out, kinotochkaSeasonLink{
			Name:   fmt.Sprintf("%d сезон", n),
			Season: n,
			URL:    uri,
		})
	}

	// Legacy code returns links in reverse insertion order.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func (k *kinotochkaChecker) seasonsByTitle(req *http.Request, title string) []kinotochkaSeasonLink {
	searchHTML, ok := k.searchPage(req, title)
	if !ok {
		return nil
	}

	want := normalizeSearchTitle(title)
	parts := strings.Split(searchHTML, "sres-wrap clearfix")
	if len(parts) < 2 {
		return nil
	}

	out := make([]kinotochkaSeasonLink, 0, 8)
	for _, block := range parts[1:] {
		head := kinotochkaSeasonHeaderRe.FindStringSubmatch(block)
		if len(head) != 4 {
			continue
		}
		if normalizeSearchTitle(html.UnescapeString(strings.TrimSpace(head[1]))) != want {
			continue
		}

		uri := strings.TrimSpace(submatch1(kinotochkaHTMLLinkCaptureRe, block))
		if uri == "" {
			continue
		}

		n, err := strconv.Atoi(strings.TrimSpace(head[3]))
		if err != nil || n < 1 {
			continue
		}

		name := strings.ToLower(strings.TrimSpace(head[2]))
		if name == "" {
			name = fmt.Sprintf("%d сезон", n)
		}
		out = append(out, kinotochkaSeasonLink{
			Name:   name,
			Season: n,
			URL:    uri,
		})
	}

	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func (k *kinotochkaChecker) writeEpisodes(
	w http.ResponseWriter,
	req *http.Request,
	rjson bool,
	title string,
	originalTitle string,
	season int,
	links *proxylink.Manager,
) {
	newsURI := strings.TrimSpace(req.URL.Query().Get("newsuri"))
	if newsURI == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	newsBody, ok := k.fetch(req, http.MethodGet, newsURI, "", "")
	if !ok {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Client-direct stream: apk client plays the CDN itself (raw URL + UA header).
	direct := k.clientStream && rchClientStreamCapable(req)

	playlistURI := strings.TrimSpace(submatch1(kinotochkaPlaylistFileRe, string(newsBody)))
	if playlistURI == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	playlistBody, ok := k.fetch(req, http.MethodGet, playlistURI, "", newsURI)
	if !ok {
		writeGetsTVEmpty(w, rjson)
		return
	}

	var root kinotochkaPlaylistRoot
	if err := stdjson.Unmarshal(playlistBody, &root); err != nil || len(root.Playlist) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	baseTitle := getsTVJoinName(title, originalTitle)
	data := make([]map[string]any, 0, len(root.Playlist))
	labels := make([]string, 0, len(root.Playlist))
	seasons := make([]int, 0, len(root.Playlist))
	episodes := make([]int, 0, len(root.Playlist))

	for i, item := range root.Playlist {
		name := strings.TrimSpace(item.Comment)
		if pos := strings.Index(name, "<"); pos >= 0 {
			name = strings.TrimSpace(name[:pos])
		}
		if name == "" {
			continue
		}

		rawFile := strings.TrimSpace(item.File)
		if rawFile == "" {
			continue
		}

		epNum := i + 1
		if m := kinotochkaEpisodeNumRe.FindStringSubmatch(name); len(m) == 2 {
			if n, err := strconv.Atoi(strings.TrimSpace(m[1])); err == nil && n > 0 {
				epNum = n
			}
		}

		// Try to extract multiple quality URLs from the episode file field.
		epURLs := kinotochkaExtractMovieURLs(rawFile)
		qualMap := kinotochkaAllQualities(epURLs)

		row := map[string]any{
			"method": "play",
			"s":      season,
			"e":      epNum,
			"name":   name,
			"title":  fmt.Sprintf("%s (%s)", baseTitle, name),
		}
		if direct {
			row["headers"] = kinotochkaStreamHeaders()
		}

		if len(qualMap) > 1 {
			qualMapAny := make(map[string]any, len(qualMap))
			bestLabel := bestQualityLabel(qualMap)
			var bestURL string
			for label, rawURL := range qualMap {
				resolved := kinotochkaPlaylistTailRe.ReplaceAllString(rawURL, `$1.mp4`)
				proxied := k.streamURL(req, resolved, links, direct)
				qualMapAny[label] = proxied
				if label == bestLabel {
					bestURL = proxied
				}
			}
			if bestURL == "" {
				for _, u := range qualMapAny {
					bestURL = u.(string)
					break
				}
			}
			row["url"] = bestURL
			row["stream"] = bestURL
			row["quality"] = qualMapAny
			row["qualitys"] = qualMapAny
		} else {
			// Single URL — use as-is.
			file := kinotochkaPlaylistTailRe.ReplaceAllString(rawFile, `$1.mp4`)
			streamURL := k.streamURL(req, file, links, direct)
			row["url"] = streamURL
			row["stream"] = streamURL
		}

		data = append(data, row)
		labels = append(labels, name)
		seasons = append(seasons, season)
		episodes = append(episodes, epNum)
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
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, seasons[i], episodes[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (k *kinotochkaChecker) writeMovie(
	w http.ResponseWriter,
	req *http.Request,
	rjson bool,
	title string,
	originalTitle string,
	kinopoiskID int64,
	links *proxylink.Manager,
) {
	if title == "" {
		title = originalTitle
	}
	if title == "" && kinopoiskID <= 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Try to get ALL quality URLs first.
	var allURLs []string
	if kinopoiskID > 0 {
		allURLs = k.movieAllURLsByKinopoisk(req, kinopoiskID)
	}
	if len(allURLs) == 0 {
		allURLs = k.movieAllURLsByTitle(req, title, originalTitle)
	}

	// Fallback to single best URL if no URLs found.
	if len(allURLs) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Client-direct stream: apk client plays the CDN itself (raw URL + UA header).
	direct := k.clientStream && rchClientStreamCapable(req)

	movieTitle := getsTVJoinName(title, originalTitle)
	if strings.TrimSpace(movieTitle) == "" {
		movieTitle = "Kinotochka"
	}

	// Build quality map.
	qualityMap := kinotochkaAllQualities(allURLs)
	var bestURL string
	var qualMapAny map[string]any

	if len(qualityMap) > 1 {
		// Multiple qualities: resolve & proxy each.
		qualMapAny = make(map[string]any, len(qualityMap))
		bestLabel := bestQualityLabel(qualityMap)
		for label, rawURL := range qualityMap {
			resolved := k.resolveMovieStream(req, rawURL)
			proxied := streamProxyURL(req, resolved, "kinotochka", links)
			qualMapAny[label] = proxied
			if label == bestLabel {
				bestURL = proxied
			}
		}
		if bestURL == "" {
			for _, u := range qualMapAny {
				bestURL = u.(string)
				break
			}
		}
	} else {
		// Single quality or no quality hints: use best URL.
		best := kinotochkaBestMovieURL(allURLs)
		if best == "" {
			writeGetsTVEmpty(w, rjson)
			return
		}
		bestURL = k.streamURL(req, k.resolveMovieStream(req, best), links, direct)
	}

	row := map[string]any{
		"method": "play",
		"url":    bestURL,
		"stream": bestURL,
		"name":   "По умолчанию",
		"title":  movieTitle,
	}
	if qualMapAny != nil {
		row["quality"] = qualMapAny
		row["qualitys"] = qualMapAny
	}
	if direct {
		row["headers"] = kinotochkaStreamHeaders()
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
	getsTVAppendMovieHTML(&sb, row, "По умолчанию", true, 0, 0)
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (k *kinotochkaChecker) movieByKinopoisk(req *http.Request, kinopoiskID int64) string {
	target := fmt.Sprintf("%s/embed/kinopoisk/%d", k.host, kinopoiskID)
	if body, ok := k.fetch(req, http.MethodGet, target, "", ""); ok {
		if file := k.movieFileFromBody(string(body)); file != "" {
			return file
		}
	}

	for _, uri := range k.linksByKinopoisk(req, kinopoiskID) {
		if body, ok := k.fetch(req, http.MethodGet, uri, "", ""); ok {
			if file := k.movieFileFromBody(string(body)); file != "" {
				return file
			}
		}
	}
	return ""
}

func (k *kinotochkaChecker) movieByTitle(req *http.Request, title, originalTitle string) string {
	seenTitle := make(map[string]struct{}, 2)
	for _, story := range []string{title, originalTitle} {
		story = strings.TrimSpace(story)
		if story == "" {
			continue
		}
		if _, ok := seenTitle[story]; ok {
			continue
		}
		seenTitle[story] = struct{}{}

		links := k.linksByTitle(req, story)
		for i, uri := range links {
			// Avoid excessive upstream requests on noisy search pages.
			if i >= 12 {
				break
			}
			if body, ok := k.fetch(req, http.MethodGet, uri, "", ""); ok {
				if file := k.movieFileFromBody(string(body)); file != "" {
					return file
				}
			}
		}
	}
	return ""
}

func (k *kinotochkaChecker) movieFileFromBody(body string) string {
	candidates := make([]string, 0, 4)
	appendMatches := func(re *regexp.Regexp) {
		all := re.FindAllStringSubmatch(body, -1)
		for _, m := range all {
			if len(m) < 2 {
				continue
			}
			v := strings.TrimSpace(m[1])
			if v != "" {
				candidates = append(candidates, v)
			}
		}
	}
	appendMatches(kinotochkaMovieFileByIDRe)
	appendMatches(kinotochkaMovieFileByFileRe)
	appendMatches(kinotochkaMovieFileGenericRe)
	if len(candidates) == 0 {
		return ""
	}
	return k.pickMovieCandidate(candidates)
}

func (k *kinotochkaChecker) linksByKinopoisk(req *http.Request, kinopoiskID int64) []string {
	u, err := url.Parse(k.host + "/api/find-by-kinopoisk.php")
	if err != nil {
		return nil
	}
	qs := url.Values{}
	qs.Set("kinopoisk", strconv.FormatInt(kinopoiskID, 10))
	u.RawQuery = qs.Encode()

	body, ok := k.fetch(req, http.MethodGet, u.String(), "", "")
	if !ok {
		return nil
	}

	var root []kinotochkaSearchItem
	if err := stdjson.Unmarshal(body, &root); err != nil || len(root) == 0 {
		return nil
	}

	out := make([]string, 0, len(root))
	seen := make(map[string]struct{}, len(root))
	for _, item := range root {
		uri := strings.TrimSpace(item.URL)
		if uri == "" {
			continue
		}
		if _, ok := seen[uri]; ok {
			continue
		}
		seen[uri] = struct{}{}
		out = append(out, uri)
	}
	return prioritizeMovieLinks(out)
}

func (k *kinotochkaChecker) linksByTitle(req *http.Request, story string) []string {
	searchHTML, ok := k.searchPage(req, story)
	if !ok {
		return nil
	}

	matches := kinotochkaHTMLLinkCaptureRe.FindAllStringSubmatch(searchHTML, -1)
	if len(matches) == 0 {
		return nil
	}

	out := make([]string, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		uri := strings.TrimSpace(html.UnescapeString(m[1]))
		if uri == "" {
			continue
		}
		if _, ok := seen[uri]; ok {
			continue
		}
		seen[uri] = struct{}{}
		out = append(out, uri)
	}
	return prioritizeMovieLinks(out)
}

func prioritizeMovieLinks(links []string) []string {
	if len(links) < 2 {
		return links
	}

	out := make([]string, 0, len(links))
	for _, uri := range links {
		if !kinotochkaSeasonNumRe.MatchString(strings.ToLower(uri)) {
			out = append(out, uri)
		}
	}
	for _, uri := range links {
		if kinotochkaSeasonNumRe.MatchString(strings.ToLower(uri)) {
			out = append(out, uri)
		}
	}
	return out
}

func (k *kinotochkaChecker) pickMovieFile(raw string) string {
	if picked := kinotochkaBestMovieURL(kinotochkaExtractMovieURLs(raw)); picked != "" {
		return picked
	}
	return k.pickMovieFileLegacy(raw)
}

func (k *kinotochkaChecker) pickMovieCandidate(candidates []string) string {
	if len(candidates) == 0 {
		return ""
	}

	urls := make([]string, 0, len(candidates)*2)
	for _, raw := range candidates {
		urls = append(urls, kinotochkaExtractMovieURLs(raw)...)
	}
	if picked := kinotochkaBestMovieURL(urls); picked != "" {
		return picked
	}

	for _, raw := range candidates {
		if picked := k.pickMovieFileLegacy(raw); picked != "" {
			return picked
		}
	}
	return ""
}

func (k *kinotochkaChecker) pickMovieFileLegacy(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	parts := strings.Split(raw, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		part := strings.TrimSpace(parts[i])
		if part == "" {
			continue
		}
		part = strings.Trim(part, `"'`)
		part = strings.ReplaceAll(part, `\/`, `/`)

		if !strings.HasPrefix(part, "http://") && !strings.HasPrefix(part, "https://") {
			if idx := strings.Index(part, "https://"); idx >= 0 {
				part = part[idx:]
			} else if idx := strings.Index(part, "http://"); idx >= 0 {
				part = part[idx:]
			}
		}
		if strings.HasPrefix(part, "//") {
			part = "https:" + part
		}
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		return part
	}
	return ""
}

func kinotochkaExtractMovieURLs(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	raw = html.UnescapeString(raw)
	raw = strings.ReplaceAll(raw, `\\`, `\`)
	raw = strings.ReplaceAll(raw, `\/`, `/`)
	raw = strings.ReplaceAll(raw, `\\u0026`, "&")
	raw = strings.ReplaceAll(raw, `\u0026`, "&")

	urls := kinotochkaMovieURLRe.FindAllString(raw, -1)
	if len(urls) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(urls))
	out := make([]string, 0, len(urls))
	for _, item := range urls {
		item = strings.TrimSpace(item)
		item = strings.TrimRight(item, ",")
		item = strings.Trim(item, `"'`)
		if strings.HasPrefix(item, "//") {
			item = "https:" + item
		}
		if item == "" {
			continue
		}
		if strings.Contains(item, "%2F") || strings.Contains(item, "%3A") {
			if decoded, err := url.QueryUnescape(item); err == nil && strings.HasPrefix(decoded, "http") {
				item = decoded
			}
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func kinotochkaBestMovieURL(urls []string) string {
	best := ""
	bestScore := -1 << 30
	for i, u := range urls {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		score := kinotochkaMovieURLScore(u)*1000 + i
		if score > bestScore {
			bestScore = score
			best = u
		}
	}
	return best
}

func kinotochkaMovieURLScore(u string) int {
	value := strings.ToLower(strings.TrimSpace(u))
	score := 0

	if strings.Contains(value, "trailer") {
		score -= 120
	}
	if strings.Contains(value, "preview") {
		score -= 60
	}
	if strings.Contains(value, "/video_mp4/films/") {
		score += 20
	}

	switch {
	case strings.Contains(value, "2160") || strings.Contains(value, "4k"):
		score += 60
	case strings.Contains(value, "1440"):
		score += 45
	case strings.Contains(value, "1080"):
		score += 35
	case strings.Contains(value, "720"):
		score += 25
	case strings.Contains(value, "480"):
		score += 15
	case strings.Contains(value, "360"):
		score += 10
	}

	base := value
	if i := strings.Index(base, "?"); i >= 0 {
		base = base[:i]
	}
	switch {
	case strings.HasSuffix(base, ".mp4"):
		score += 8
	case strings.HasSuffix(base, ".m3u8"):
		score += 6
	case strings.HasSuffix(base, ".txt"):
		score += 2
	}

	return score
}

// kinotochkaQualityLabel extracts a quality label ("2160p", "1080p", etc.) from a URL.
// Returns "" if no quality hint is found.
func kinotochkaQualityLabel(u string) string {
	lower := strings.ToLower(u)
	switch {
	case strings.Contains(lower, "2160") || strings.Contains(lower, "4k"):
		return "2160p"
	case strings.Contains(lower, "1440"):
		return "1440p"
	case strings.Contains(lower, "1080"):
		return "1080p"
	case strings.Contains(lower, "720"):
		return "720p"
	case strings.Contains(lower, "480"):
		return "480p"
	case strings.Contains(lower, "360"):
		return "360p"
	default:
		return ""
	}
}

// kinotochkaAllQualities groups extracted URLs by quality label.
// For each quality, keeps the URL with the highest score.
// Returns map like {"2160p": "url", "1080p": "url"}.
func kinotochkaAllQualities(urls []string) map[string]string {
	type scored struct {
		url   string
		score int
	}
	best := make(map[string]scored, len(urls))
	for _, u := range urls {
		label := kinotochkaQualityLabel(u)
		if label == "" {
			continue
		}
		s := kinotochkaMovieURLScore(u)
		if prev, ok := best[label]; !ok || s > prev.score {
			best[label] = scored{url: u, score: s}
		}
	}
	if len(best) == 0 {
		return nil
	}
	result := make(map[string]string, len(best))
	for label, sc := range best {
		result[label] = sc.url
	}
	return result
}

// movieAllURLsFromBody extracts ALL video URLs from an embed page body
// (instead of just the best one like movieFileFromBody).
func (k *kinotochkaChecker) movieAllURLsFromBody(body string) []string {
	candidates := make([]string, 0, 4)
	appendMatches := func(re *regexp.Regexp) {
		all := re.FindAllStringSubmatch(body, -1)
		for _, m := range all {
			if len(m) < 2 {
				continue
			}
			v := strings.TrimSpace(m[1])
			if v != "" {
				candidates = append(candidates, v)
			}
		}
	}
	appendMatches(kinotochkaMovieFileByIDRe)
	appendMatches(kinotochkaMovieFileByFileRe)
	appendMatches(kinotochkaMovieFileGenericRe)
	if len(candidates) == 0 {
		return nil
	}
	// Extract all URLs from all candidates (not just pick best).
	urls := make([]string, 0, len(candidates)*2)
	for _, raw := range candidates {
		urls = append(urls, kinotochkaExtractMovieURLs(raw)...)
	}
	return urls
}

// movieAllURLsByKinopoisk fetches ALL video URLs for a movie by kinopoisk ID.
func (k *kinotochkaChecker) movieAllURLsByKinopoisk(req *http.Request, kinopoiskID int64) []string {
	target := fmt.Sprintf("%s/embed/kinopoisk/%d", k.host, kinopoiskID)
	if body, ok := k.fetch(req, http.MethodGet, target, "", ""); ok {
		if urls := k.movieAllURLsFromBody(string(body)); len(urls) > 0 {
			return urls
		}
	}

	for _, uri := range k.linksByKinopoisk(req, kinopoiskID) {
		if body, ok := k.fetch(req, http.MethodGet, uri, "", ""); ok {
			if urls := k.movieAllURLsFromBody(string(body)); len(urls) > 0 {
				return urls
			}
		}
	}
	return nil
}

// movieAllURLsByTitle fetches ALL video URLs for a movie by title search.
func (k *kinotochkaChecker) movieAllURLsByTitle(req *http.Request, title, originalTitle string) []string {
	seenTitle := make(map[string]struct{}, 2)
	for _, story := range []string{title, originalTitle} {
		story = strings.TrimSpace(story)
		if story == "" {
			continue
		}
		if _, ok := seenTitle[story]; ok {
			continue
		}
		seenTitle[story] = struct{}{}

		links := k.linksByTitle(req, story)
		for i, uri := range links {
			if i >= 12 {
				break
			}
			if body, ok := k.fetch(req, http.MethodGet, uri, "", ""); ok {
				if urls := k.movieAllURLsFromBody(string(body)); len(urls) > 0 {
					return urls
				}
			}
		}
	}
	return nil
}

func (k *kinotochkaChecker) resolveMovieStream(req *http.Request, file string) string {
	trimmed := strings.TrimSpace(file)
	if trimmed == "" {
		return file
	}
	base := strings.ToLower(trimmed)
	if i := strings.Index(base, "?"); i >= 0 {
		base = base[:i]
	}
	if !strings.HasSuffix(base, ".txt") {
		return trimmed
	}

	body, ok := k.fetch(req, http.MethodGet, trimmed, "", k.host+"/")
	if !ok {
		return trimmed
	}

	var root kinotochkaPlaylistRoot
	if err := stdjson.Unmarshal(body, &root); err != nil || len(root.Playlist) == 0 {
		return trimmed
	}

	for i := len(root.Playlist) - 1; i >= 0; i-- {
		candidate := strings.TrimSpace(root.Playlist[i].File)
		if candidate == "" {
			continue
		}
		candidate = kinotochkaPlaylistTailRe.ReplaceAllString(candidate, `$1.mp4`)
		if picked := k.pickMovieFile(candidate); picked != "" {
			return picked
		}
	}

	return trimmed
}

func (k *kinotochkaChecker) checkSearch(req *http.Request) bool {
	show, _ := k.checkSearchWithQuality(req)
	return show
}

func (k *kinotochkaChecker) checkSearchWithQuality(req *http.Request) (bool, string) {
	q := req.URL.Query()

	if kp := strings.TrimSpace(q.Get("kinopoisk_id")); kp != "" {
		if kpID, err := strconv.ParseInt(kp, 10, 64); err == nil {
			if k.searchByKinopoisk(req, kp) {
				// Determine quality from embed page URLs (movies + serials).
				quality := k.probeMovieQuality(req, kpID)
				if quality == "" {
					quality = "SD" // Conservative default when probe can't determine quality.
				}
				return true, quality
			}
		}
	}

	for _, story := range buildFilmixStories(q) {
		if story == "" {
			continue
		}
		if k.searchByTitle(req, story) {
			return true, "SD" // Title search: skip quality probe (too expensive), conservative default.
		}
	}
	return false, ""
}

// probeMovieQuality determines the max quality badge for a movie by kinopoisk ID.
// Fetches the embed page and extracts quality hints from URLs.
// For movies, parses quality from URL filenames (e.g. _720.mp4, _1080.mp4).
// For serials, fetches the playlist and checks episode file URLs.
func (k *kinotochkaChecker) probeMovieQuality(req *http.Request, kinopoiskID int64) string {
	// Try movie URLs first.
	urls := k.movieAllURLsByKinopoisk(req, kinopoiskID)
	if len(urls) > 0 {
		quals := kinotochkaAllQualities(urls)
		if q := maxQualityFromMap(quals); q != "" {
			return q
		}
	}

	// Fallback: try serial playlist — fetch embed, get playlist, check first episode URL.
	return k.probeSerialQuality(req, kinopoiskID)
}

// probeSerialQuality determines quality for a serial by checking episode file URLs.
func (k *kinotochkaChecker) probeSerialQuality(req *http.Request, kinopoiskID int64) string {
	target := fmt.Sprintf("%s/embed/kinopoisk/%d", k.host, kinopoiskID)
	body, ok := k.fetch(req, http.MethodGet, target, "", "")
	if !ok {
		return ""
	}

	playlistURI := strings.TrimSpace(submatch1(kinotochkaPlaylistFileRe, string(body)))
	if playlistURI == "" {
		return ""
	}

	plBody, ok := k.fetch(req, http.MethodGet, playlistURI, "", target)
	if !ok {
		return ""
	}

	var root kinotochkaPlaylistRoot
	if err := stdjson.Unmarshal(plBody, &root); err != nil || len(root.Playlist) == 0 {
		return ""
	}

	// Check first episode file URL for quality markers.
	firstFile := strings.TrimSpace(root.Playlist[0].File)
	if firstFile == "" {
		return ""
	}

	// Try to extract quality from the episode URLs (may have _720, _1080 etc.)
	epURLs := kinotochkaExtractMovieURLs(firstFile)
	if quals := kinotochkaAllQualities(epURLs); len(quals) > 0 {
		return maxQualityFromMap(quals)
	}

	// No quality markers in URL — single-file serial.
	// Use "SD" as safe default (kinotochka typically serves 480p for unmarked serial files).
	return "SD"
}

func (k *kinotochkaChecker) searchByKinopoisk(req *http.Request, kp string) bool {
	u, err := url.Parse(k.host + "/api/find-by-kinopoisk.php")
	if err != nil {
		return false
	}
	qs := url.Values{}
	qs.Set("kinopoisk", kp)
	u.RawQuery = qs.Encode()

	body, ok := k.fetch(req, http.MethodGet, u.String(), "", "")
	if !ok {
		return false
	}

	var arr []stdjson.RawMessage
	if err := stdjson.Unmarshal(body, &arr); err != nil {
		return false
	}
	return len(arr) > 0
}

func (k *kinotochkaChecker) searchByTitle(req *http.Request, story string) bool {
	htmlBody, ok := k.searchPage(req, story)
	if !ok {
		return false
	}
	if !strings.Contains(strings.ToLower(htmlBody), "поиск по сайту") {
		return false
	}
	if !strings.Contains(strings.ToLower(htmlBody), "sres-wrap clearfix") {
		return false
	}
	return kinotochkaHTMLLinkRe.MatchString(htmlBody)
}

func (k *kinotochkaChecker) searchPage(req *http.Request, story string) (string, bool) {
	form := url.Values{}
	form.Set("do", "search")
	form.Set("subaction", "search")
	form.Set("search_start", "0")
	form.Set("full_search", "0")
	form.Set("result_from", "1")
	form.Set("story", story)

	body, ok := k.fetch(req, http.MethodPost, k.host+"/index.php?do=search", form.Encode(), "")
	if !ok {
		return "", false
	}
	return string(body), true
}

func (k *kinotochkaChecker) fetch(req *http.Request, method, target, body, referer string) ([]byte, bool) {
	budget := kinotochkaBudgetOf(req)
	if budget != nil {
		if budget.fetches >= kinotochkaMaxFetches || budget.failRun >= kinotochkaMaxFailRun {
			return nil, false // chain budget spent — fail fast, don't queue another timeout
		}
		budget.fetches++
	}

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
	if body != "" {
		httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if referer != "" {
		httpReq.Header.Set("Referer", referer)
	}
	if k.cookie != "" {
		httpReq.Header.Set("Cookie", k.cookie)
	}

	resp, err := k.client.Do(httpReq)
	if err != nil {
		if budget != nil {
			budget.failRun++ // transport-level failure (timeout/refused) — the slow-site signal
		}
		return nil, false
	}
	if budget != nil {
		budget.failRun = 0
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, false
	}
	// Strip UTF-8 BOM — some kinotochka endpoints (e.g. playlist .txt)
	// prepend it, which breaks json.Unmarshal.
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		data = data[3:]
	}
	return data, true
}
