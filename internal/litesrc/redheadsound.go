package litesrc

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/proxylink"

	"github.com/rs/zerolog/log"
)

var (
	redheadsoundHashRe       = regexp.MustCompile(`dle_login_hash\s*=\s*['"]([a-f0-9]+)['"]`)
	redheadsoundVideoURLRe   = regexp.MustCompile(`(?is)videoUrl[\t ]*=[\t ]*'(?P<uri>https?://[^']+)'`)
	redheadsoundContentURLRe = regexp.MustCompile(`(?is)"contentUrl":\s*"([^"]+)"`)
	redheadsoundHLSStreamRe  = regexp.MustCompile(`#EXT-X-STREAM-INF:[^\n]*RESOLUTION=(\d+)x(\d+)[^\n]*\n([^\n]+)`)

	// iframe src extraction
	redheadsoundIframeSrcRe = regexp.MustCompile(`(?i)<iframe[^>]+src="([^"]+)"`)

	// video_id for response.php
	redheadsoundVideoIDRe  = regexp.MustCompile(`response\.php\?video_id=(\d+)`)
	redheadsoundVideoIDRe2 = regexp.MustCompile(`video_id\s*[:=]\s*"?(\d+)"?`)

	// m3u8 extraction from response bodies
	redheadsoundM3u8Re  = regexp.MustCompile(`https?:\\/\\/[^\"\s]+\.m3u8[^\"\s]*`)
	redheadsoundM3u8Re2 = regexp.MustCompile(`https?://[^\"\s]+\.m3u8[^\"\s]*`)

	// Block-level splitter for search results
	redheadsoundBlockRe = regexp.MustCompile(`(?is)<div[^>]*class="[^"]*move-item[^"]*"[^>]*>`)
	// Fallback block-level splitter (article/shortstory templates)
	redheadsoundBlockFallbackRe = regexp.MustCompile(`(?is)<(?:article|div)[^>]*class="[^"]*(?:shortstory|short-story|shortstory-card)[^"]*"[^>]*>`)
	// Per-block field extractors
	redheadsoundBlockHrefRe  = regexp.MustCompile(`(?i)<a[^>]+href="([^"]+)"`)
	redheadsoundBlockTitleRe = regexp.MustCompile(`(?is)<h[234][^>]*(?:class="[^"]*title[^"]*")?[^>]*>\s*<a[^>]*>([^<]+)</a>`)
	redheadsoundBlockYearRe  = regexp.MustCompile(`(?is)<span[^>]*class="[^"]*year[^"]*"[^>]*>.*?(\d{4})`)
	redheadsoundBlockYear2Re = regexp.MustCompile(`(\d{4})`)

	// Bearer token extraction from response body
	redheadsoundBearerRe  = regexp.MustCompile(`(?i)bearer\s+([A-Za-z0-9\-._~+/=]{40,})`)
	redheadsoundBearerRe2 = regexp.MustCompile(`(?i)"(?:access_token|accessToken)"\s*:\s*"([^"]{40,})"`)
	redheadsoundBearerRe3 = regexp.MustCompile(`(?i)"token"\s*:\s*"([A-Za-z0-9\-._~+/=]{60,})"`)
	// Accepts-Controls header
	redheadsoundAcceptsRe  = regexp.MustCompile(`(?i)"accepts[-_]?controls"\s*:\s*"([^"]{20,})"`)
	redheadsoundAcceptsRe2 = regexp.MustCompile(`(?i)accepts[-_]?controls\s*[:=]\s*"?([A-Za-z0-9\-._~+/=]{20,})`)

	// movieId for bnsi endpoint
	redheadsoundMovieIDRe  = regexp.MustCompile(`/bnsi/movies/(\d+)`)
	redheadsoundMovieIDRe2 = regexp.MustCompile(`"active"\s*:\s*\{[^}]*"id"\s*:\s*(\d+)`)

	// Series: season links  — <a href="…/serialy/season/{id}-{slug}.html" class="… seazon-btn">01</a>
	redheadsoundSeasonLinkRe = regexp.MustCompile(`(?i)<a[^>]+href="([^"]*serialy/season/[^"]+)"[^>]*class="[^"]*seazon-btn[^"]*"[^>]*>(\d+)</a>`)
	// Series: episode number  — <span class="episode-number">Серия 3</span>
	redheadsoundEpisodeNumRe = regexp.MustCompile(`(?is)<span[^>]*class="[^"]*episode-number[^"]*"[^>]*>\s*(?:Серия|Episode|Эпизод)\s*(\d+)`)
	// Series: video-card blocks
	redheadsoundVideoCardRe = regexp.MustCompile(`(?is)<div[^>]*class="[^"]*video-card[^"]*"[^>]*>`)
	// Series: detect if page is a series (has season navigation)
	redheadsoundIsSeriesRe = regexp.MustCompile(`(?i)class="[^"]*seazon-btn[^"]*"`)
	// Series: episode Kinescope video URLs inside initMainPlayer (for PRO users).
	// Pattern: map/switch with episode → kinescope URL.  We look for all kinescope embed URLs.
	redheadsoundKinescopeEmbedRe = regexp.MustCompile(`https?://kinescope\.io/embed/([A-Za-z0-9_-]+)`)
	// Series: detect per-episode video URLs from the inline JS
	// Pattern like:  case 1: videoUrl = 'https://kinescope.io/embed/xxx'; break;
	//           or:  episodeUrls[1] = 'https://kinescope.io/embed/xxx'
	//           or:  {1: 'https://kinescope.io/embed/xxx', 2: ...}
	redheadsoundEpisodeVideoMapRe = regexp.MustCompile(`(?:case\s+)?(\d+)\s*[}:=]\s*['"]?(https?://kinescope\.io/embed/[A-Za-z0-9_?=&-]+)['"]?`)
)

// redheadsoundEmbedResult holds the result of embed() with optional stream headers.
type redheadsoundEmbedResult struct {
	streamURL string            // the m3u8/contentUrl to stream (for movies)
	headers   map[string]string // custom headers for stream requests (Bearer, Origin, etc.)
	quality   string            // best quality label
	isSeries  bool              // true if the result is a series
	seasons   []rhsSeason       // populated for series
}

type rhsSeason struct {
	num int    // season number (1, 2, …)
	url string // absolute URL of the season page
}

type rhsEpisode struct {
	num       int               // episode number (1, 2, …)
	streamURL string            // m3u8/contentUrl for this episode
	headers   map[string]string // stream headers
}

type redheadsoundChecker struct {
	client    *http.Client
	cdnClient *http.Client // for CDN requests (may go through SOCKS5 proxy)
	host      string
	login     string
	password  string
	loggedIn  bool
}

func NewRedheadsoundChecker(cfg config.Config) *redheadsoundChecker {
	host := strings.TrimSpace(cfg.Online.Redheadsound.Host)
	if host == "" {
		host = "https://redheadsound.studio"
	}
	host = strings.TrimRight(host, "/")

	// RedHeadSound uses DDoS-Guard which requires cookies to persist
	// between requests. The user_hash from the main page is tied to
	// the session cookies — without a cookie jar, search requests fail
	// with "session expired".
	jar, _ := cookiejar.New(nil)

	// DDoS-Guard blocks many VPS IPs on redheadsound.studio. Route site
	// fetches through a plain Go SOCKS5 transport (HTTP/1.1, no Chrome
	// fingerprinting — DDoS-Guard gates on IP, not TLS fingerprint).
	// Default address matches turbo's residential SOCKS5; configurable via
	// [online.redheadsound] socks_proxy = '...'. 'none'/'off' = direct.
	// Timeout is 45s because the residential SOCKS5 + DDoS-Guard handshake
	// routinely takes 10-16s (manual curl measurements).
	socksAddr := strings.TrimSpace(cfg.Online.Redheadsound.SocksProxy)
	if socksAddr == "" {
		socksAddr = "127.0.0.1:40007"
	}

	var siteClient *http.Client
	if strings.EqualFold(socksAddr, "none") || strings.EqualFold(socksAddr, "off") {
		siteClient = httpclient.NewForBalancer("redheadsound", 60*time.Second)
	} else {
		// Use uTLS (Chrome fingerprint) through SOCKS5. Plain Go TLS through
		// our xray SOCKS5 repeatedly hits "TLS handshake timeout" even with
		// 60s budget — something about Go's net/http TLS handshake path
		// doesn't mesh with xray's residential tunnel. uTLS goes through a
		// different code path (manual TLS handshake over the SOCKS5 conn).
		siteClient = httpclient.NewUTLSViaSOCKS5(socksAddr, 90*time.Second)
		log.Info().Str("socks", socksAddr).Msg("redheadsound: site via uTLS+SOCKS5 (DDoS-Guard bypass)")
	}
	siteClient.Jar = jar

	// Kinescope CDN is NOT geoblocked on most VPS — keep direct for bandwidth.
	cdnClient := httpclient.NewForBalancer("redheadsound", 15*time.Second)

	return &redheadsoundChecker{
		client:    siteClient,
		cdnClient: cdnClient,
		host:      host,
		login:     strings.TrimSpace(cfg.Online.Redheadsound.Login),
		password:  strings.TrimSpace(cfg.Online.Redheadsound.Password),
	}
}

func (r *redheadsoundChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show, isSerial := r.checkSearchEx(req.Context(), req.URL.Query())
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			if show {
				tp := "movie"
				if isSerial {
					tp = "serial"
				}
				quality := pluginQualityBadgeGet("redheadsound")
				if quality != "" {
					_, _ = fmt.Fprintf(w, `{"type":"%s","rch":false,"quality":"%s"}`, tp, quality)
				} else {
					_, _ = fmt.Fprintf(w, `{"type":"%s","rch":false}`, tp)
				}
				return
			}
			_, _ = w.Write([]byte(`{"rch":false}`))
			return
		}

		r.index(w, req, links)
	}
}

func (r *redheadsoundChecker) index(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	title := strings.TrimSpace(q.Get("title"))
	origTitle := strings.TrimSpace(q.Get("original_title"))
	year, _ := strconv.Atoi(strings.TrimSpace(q.Get("year")))

	// year==0 is OK now (for series year may not be passed)
	if title == "" && origTitle == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// For series: s=season, e=episode, serial_url=season page URL
	seasonNum, _ := strconv.Atoi(strings.TrimSpace(q.Get("s")))
	episodeNum, _ := strconv.Atoi(strings.TrimSpace(q.Get("e")))
	serialURL := strings.TrimSpace(q.Get("serial_url"))

	host := hostFromRequest(req)
	displayTitle := title
	if displayTitle == "" {
		displayTitle = origTitle
	}
	defaultArgs := "&title=" + url.QueryEscape(title) +
		"&original_title=" + url.QueryEscape(origTitle) +
		"&year=" + strconv.Itoa(year)

	// Fast path: if serial_url is provided, skip the embed() search.
	// This happens when Lampa navigates to a season or episode.
	if serialURL != "" {
		r.ensureLoggedIn(req.Context())

		if episodeNum == 0 {
			// Show episode list
			targetURL := redheadsoundAbsoluteURL(r.host, serialURL)
			episodes := r.parseSeasonPage(req.Context(), targetURL)
			if len(episodes) == 0 {
				log.Debug().Str("url", targetURL).Msg("redheadsound: no episodes found on season page")
				writeGetsTVEmpty(w, rjson)
				return
			}
			r.writeEpisodeList(w, req, rjson, host, defaultArgs, seasonNum, targetURL, displayTitle, episodes, links)
			return
		}

		// Play specific episode
		targetURL := redheadsoundAbsoluteURL(r.host, serialURL)
		episodes := r.parseSeasonPage(req.Context(), targetURL)
		for _, ep := range episodes {
			if ep.num == episodeNum && ep.streamURL != "" {
				r.writeEpisodePlay(w, req, rjson, ep, links, displayTitle, seasonNum)
				return
			}
		}
		log.Debug().Int("episode", episodeNum).Msg("redheadsound: episode not found or no video URL")
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Full path: search → embed → decide movie or series
	result, ok := r.embed(req.Context(), title, origTitle, year)
	if !ok || result == nil {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Movie — direct playback (same as before)
	if !result.isSeries {
		r.writeMovieResult(w, req, rjson, result, links, title, origTitle)
		return
	}

	// Series — show season list
	if seasonNum == 0 {
		r.writeSeasonList(w, rjson, host, defaultArgs, result.seasons)
		return
	}

	// Series — season selected but no serial_url (shouldn't happen normally)
	for _, s := range result.seasons {
		if s.num == seasonNum {
			targetURL := redheadsoundAbsoluteURL(r.host, s.url)
			episodes := r.parseSeasonPage(req.Context(), targetURL)
			if len(episodes) == 0 {
				log.Debug().Str("url", targetURL).Msg("redheadsound: no episodes found on season page")
				writeGetsTVEmpty(w, rjson)
				return
			}
			r.writeEpisodeList(w, req, rjson, host, defaultArgs, seasonNum, targetURL, displayTitle, episodes, links)
			return
		}
	}
	writeGetsTVEmpty(w, rjson)
}

func (r *redheadsoundChecker) writeMovieResult(w http.ResponseWriter, req *http.Request, rjson bool, result *redheadsoundEmbedResult, links *proxylink.Manager, title, origTitle string) {
	streamURL := result.streamURL
	if links != nil && !isStreamProxyDisabled("redheadsound") {
		hdrs := result.headers
		if hdrs == nil {
			hdrs = map[string]string{}
		}
		streamURL = hostFromRequest(req) + "/proxy/" + links.EncryptURIWithHeaders(streamURL, clientIP(req), "redheadsound", hdrs)
	}

	displayTitle := title
	if displayTitle == "" {
		displayTitle = origTitle
	}

	bestLabel := result.quality
	if bestLabel == "" {
		bestLabel = "1080p"
	}

	row := map[string]any{
		"method": "play",
		"url":    streamURL,
		"stream": streamURL,
		"name":   bestLabel,
		"title":  fmt.Sprintf("%s (%s)", displayTitle, bestLabel),
		"streamquality": []map[string]any{
			{
				"quality": bestLabel,
				"url":     streamURL,
			},
		},
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
	getsTVAppendMovieHTML(&sb, row, bestLabel, true, 0, 0)
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (r *redheadsoundChecker) writeSeasonList(w http.ResponseWriter, rjson bool, host, defaultArgs string, seasons []rhsSeason) {
	if len(seasons) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	data := make([]map[string]any, 0, len(seasons))
	labels := make([]string, 0, len(seasons))
	for _, s := range seasons {
		name := strconv.Itoa(s.num) + " сезон"
		link := host + "/lite/redheadsound?rjson=" + getsTVBool(rjson) +
			"&s=" + strconv.Itoa(s.num) +
			"&serial_url=" + url.QueryEscape(s.url) +
			defaultArgs
		data = append(data, map[string]any{
			"method": "link",
			"id":     s.num,
			"url":    link,
			"name":   name,
		})
		labels = append(labels, name)
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

func (r *redheadsoundChecker) writeEpisodeList(w http.ResponseWriter, req *http.Request, rjson bool, host, defaultArgs string, seasonNum int, seasonURL, displayTitle string, episodes []rhsEpisode, links *proxylink.Manager) {
	data := make([]map[string]any, 0, len(episodes))
	labels := make([]string, 0, len(episodes))
	seasons := make([]int, 0, len(episodes))
	episodesIdx := make([]int, 0, len(episodes))

	for _, ep := range episodes {
		name := strconv.Itoa(ep.num) + " серия"

		var streamURL string
		if ep.streamURL != "" && links != nil && !isStreamProxyDisabled("redheadsound") {
			hdrs := ep.headers
			if hdrs == nil {
				hdrs = map[string]string{}
			}
			streamURL = hostFromRequest(req) + "/proxy/" + links.EncryptURIWithHeaders(ep.streamURL, clientIP(req), "redheadsound", hdrs)
		}

		link := host + "/lite/redheadsound?rjson=" + getsTVBool(rjson) +
			"&s=" + strconv.Itoa(seasonNum) +
			"&e=" + strconv.Itoa(ep.num) +
			"&serial_url=" + url.QueryEscape(seasonURL) +
			defaultArgs

		row := map[string]any{
			"method": "call",
			"url":    link,
			"name":   name,
			"title":  fmt.Sprintf("%s (%d серия)", displayTitle, ep.num),
		}
		if streamURL != "" {
			row["stream"] = streamURL
		}

		data = append(data, row)
		labels = append(labels, name)
		seasons = append(seasons, seasonNum)
		episodesIdx = append(episodesIdx, ep.num)
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
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, seasons[i], episodesIdx[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (r *redheadsoundChecker) writeEpisodePlay(w http.ResponseWriter, req *http.Request, rjson bool, ep rhsEpisode, links *proxylink.Manager, displayTitle string, seasonNum int) {
	streamURL := ep.streamURL
	if links != nil && !isStreamProxyDisabled("redheadsound") {
		hdrs := ep.headers
		if hdrs == nil {
			hdrs = map[string]string{}
		}
		streamURL = hostFromRequest(req) + "/proxy/" + links.EncryptURIWithHeaders(streamURL, clientIP(req), "redheadsound", hdrs)
	}

	bestLabel := "1080p"
	row := map[string]any{
		"method": "play",
		"url":    streamURL,
		"stream": streamURL,
		"name":   bestLabel,
		"title":  fmt.Sprintf("%s (S%02dE%02d, %s)", displayTitle, seasonNum, ep.num, bestLabel),
		"streamquality": []map[string]any{
			{
				"quality": bestLabel,
				"url":     streamURL,
			},
		},
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
	getsTVAppendMovieHTML(&sb, row, bestLabel, true, seasonNum, ep.num)
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (r *redheadsoundChecker) checkSearch(ctx context.Context, q url.Values) bool {
	show, _ := r.checkSearchEx(ctx, q)
	return show
}

func (r *redheadsoundChecker) checkSearchEx(ctx context.Context, q url.Values) (show bool, isSerial bool) {
	title := strings.TrimSpace(q.Get("title"))
	origTitle := strings.TrimSpace(q.Get("original_title"))
	if title == "" && origTitle == "" {
		return false, false
	}
	year, _ := strconv.Atoi(strings.TrimSpace(q.Get("year")))

	search := r.doSearch(ctx, title, origTitle)
	if search == "" {
		return false, false
	}

	link, ok := r.selectSearchLinkEx(search, title, origTitle, year)
	if !ok {
		return false, false
	}
	// Detect if it's a series link
	isSerial = strings.Contains(link, "/serialy/")
	return true, isSerial
}

// doSearch performs the search. On redheadsound.studio the AJAX endpoint
// `/engine/ajax/controller.php?mod=search` (DLE QSearch with user_hash) is
// the only one that actually filters by query — the GET/POST DLE fallbacks
// return the full 140KB homepage regardless of `story=`, which silently
// poisons selectSearchLinkEx into picking a random promoted film.
//
// The site's database stores Russian titles, so AJAX search returns
// notfound for English queries. We try `title` (typically RU from Lampa)
// first; if that returns empty, fall back to `origTitle` (EN) just in case
// the user's Lampa is configured with English UI and the title field
// contains EN. Both empty → return "".
func (r *redheadsoundChecker) doSearch(ctx context.Context, title, origTitle string) string {
	// Get main page first (for cookies and user_hash)
	mainHTML, ok := r.fetchMain(ctx)
	if !ok {
		log.Debug().Msg("redheadsound: fetchMain failed")
		return ""
	}
	hashMatch := redheadsoundHashRe.FindStringSubmatch(mainHTML)
	userHash := ""
	if len(hashMatch) >= 2 {
		userHash = hashMatch[1]
	}
	if userHash == "" {
		log.Debug().Msg("redheadsound: no user_hash on main page — AJAX search disabled")
		return ""
	}
	log.Debug().Str("hash", userHash).Msg("redheadsound: got hash")

	// Try queries in order: title (usually Russian), origTitle (English fallback).
	queries := make([]string, 0, 2)
	if q := strings.TrimSpace(title); q != "" {
		queries = append(queries, q)
	}
	if q := strings.TrimSpace(origTitle); q != "" && q != strings.TrimSpace(title) {
		queries = append(queries, q)
	}

	for _, q := range queries {
		search := r.fetchSearchOld(ctx, q, userHash)
		if search != "" && r.hasSearchResults(search) {
			log.Debug().Int("len", len(search)).Str("q", q).Msg("redheadsound: ajax search OK")
			return search
		}
		log.Debug().Int("len", len(search)).Str("q", q).Msg("redheadsound: ajax search empty")
	}

	return ""
}

// hasSearchResults checks if the search HTML contains any result rows.
// Returns false when DLE's notfound marker is present — RHS sometimes wraps
// notfound responses inside an HTML that also has the homepage's move-item
// carousel, so a bare "move-item" check is unsafe.
func (r *redheadsoundChecker) hasSearchResults(html string) bool {
	lower := strings.ToLower(html)
	if strings.Contains(lower, `class="notfound"`) || strings.Contains(lower, `class='notfound'`) {
		return false
	}
	if strings.Contains(lower, "похожих статей на сайте не найдено") {
		return false
	}
	if strings.Contains(lower, "move-item") {
		return true
	}
	if strings.Contains(lower, "shortstory") || strings.Contains(lower, "short-story") {
		return true
	}
	return false
}

func (r *redheadsoundChecker) embed(ctx context.Context, title, origTitle string, year int) (*redheadsoundEmbedResult, bool) {
	if strings.TrimSpace(title) == "" && strings.TrimSpace(origTitle) == "" {
		return nil, false
	}

	// Ensure login for PRO content (series, etc.)
	r.ensureLoggedIn(ctx)

	search := r.doSearch(ctx, title, origTitle)
	if search == "" {
		return nil, false
	}

	link, ok := r.selectSearchLinkEx(search, title, origTitle, year)
	if !ok {
		log.Debug().Str("title", title).Str("origTitle", origTitle).Int("year", year).Msg("redheadsound: no search link found")
		return nil, false
	}
	link = redheadsoundAbsoluteURL(r.host, link)
	if link == "" {
		return nil, false
	}
	log.Debug().Str("link", link).Msg("redheadsound: selected link")

	news, ok := r.fetchURL(ctx, link)
	if !ok {
		log.Debug().Str("link", link).Msg("redheadsound: fetchURL (news) failed")
		return nil, false
	}

	// === Check if this is a series page ===
	if redheadsoundIsSeriesRe.MatchString(news) || strings.Contains(link, "/serialy/") {
		seasons := r.parseSeasonsFromHTML(news)
		if len(seasons) > 0 {
			log.Debug().Int("seasons", len(seasons)).Msg("redheadsound: detected series")
			return &redheadsoundEmbedResult{
				isSeries: true,
				seasons:  seasons,
			}, true
		}
		log.Debug().Msg("redheadsound: series page but no seasons found")
	}

	// === Try videoUrl (old way, Kinescope direct) ===
	iframeURI := strings.TrimSpace(submatch1(redheadsoundVideoURLRe, news))
	if iframeURI != "" {
		log.Debug().Str("iframeURI", iframeURI).Msg("redheadsound: found videoUrl")
		return r.embedViaKinescope(ctx, iframeURI)
	}

	// === Try iframe src with priorities: ladoni/lat > stloadi/token > any ===
	iframeURI = r.findBestIframe(news)
	if iframeURI == "" {
		// === Try response.php?video_id ===
		return r.embedViaVideoID(ctx, news, link)
	}
	log.Debug().Str("iframeURI", iframeURI).Msg("redheadsound: found iframe src")

	// === Try Kinescope contentUrl from iframe ===
	iframe, ok := r.fetchURLWithReferer(ctx, iframeURI, r.host+"/")
	if !ok {
		log.Debug().Str("iframeURI", iframeURI).Msg("redheadsound: fetch iframe failed")
		return nil, false
	}

	// Check for contentUrl (Kinescope)
	contentURL := strings.TrimSpace(submatch1(redheadsoundContentURLRe, iframe))
	if contentURL != "" {
		contentURL = strings.ReplaceAll(contentURL, "&amp;", "&")
		contentURL = strings.ReplaceAll(contentURL, `\u0026`, "&")
		if strings.HasPrefix(contentURL, "//") {
			contentURL = "https:" + contentURL
		}
		if strings.HasPrefix(contentURL, "/") {
			contentURL = redheadsoundAbsoluteURL(iframeURI, contentURL)
		}
		log.Debug().Str("contentURL", contentURL).Msg("redheadsound: found contentUrl (Kinescope)")
		headers := r.buildStreamHeaders(iframe, iframeURI)
		return &redheadsoundEmbedResult{
			streamURL: contentURL,
			headers:   headers,
			quality:   "1080p",
		}, true
	}

	// Check for m3u8 directly in iframe response
	m3u8 := r.extractM3u8(iframe)
	if m3u8 != "" {
		log.Debug().Str("m3u8", m3u8).Msg("redheadsound: found m3u8 in iframe")
		headers := r.buildStreamHeaders(iframe, iframeURI)
		return &redheadsoundEmbedResult{
			streamURL: m3u8,
			headers:   headers,
			quality:   "1080p",
		}, true
	}

	// === Try token_movie / stloadi lists.php ===
	result := r.embedViaTokenMovie(ctx, iframe, iframeURI)
	if result != nil {
		return result, true
	}

	// === Try bnsi/movies endpoint ===
	result = r.embedViaBNSI(ctx, iframe, iframeURI)
	if result != nil {
		return result, true
	}

	log.Debug().Msg("redheadsound: no stream source found in iframe")
	return nil, false
}

// findBestIframe finds the best iframe src from the page HTML.
// Priority: ladoni/lat > stloadi/token_movie > kinescope > any http iframe.
func (r *redheadsoundChecker) findBestIframe(html string) string {
	matches := redheadsoundIframeSrcRe.FindAllStringSubmatch(html, -1)
	if len(matches) == 0 {
		return ""
	}

	var stloadi, any string
	for _, m := range matches {
		u := strings.TrimSpace(m[1])
		if u == "" {
			continue
		}
		u = strings.ReplaceAll(u, "&amp;", "&")
		if strings.HasPrefix(u, "//") {
			u = "https:" + u
		}
		if !strings.HasPrefix(u, "http") {
			continue
		}

		lower := strings.ToLower(u)
		if strings.Contains(lower, "ladoni") && strings.Contains(lower, "/lat/") {
			return u // highest priority
		}
		if stloadi == "" && (strings.Contains(lower, "stloadi") ||
			strings.Contains(lower, "token_movie") ||
			strings.Contains(lower, "token=")) {
			stloadi = u
		}
		if any == "" {
			any = u
		}
	}

	if stloadi != "" {
		return stloadi
	}
	return any
}

// embedViaKinescope fetches a Kinescope embed and extracts contentUrl.
func (r *redheadsoundChecker) embedViaKinescope(ctx context.Context, iframeURI string) (*redheadsoundEmbedResult, bool) {
	iframe, ok := r.fetchURLWithReferer(ctx, iframeURI, r.host+"/")
	if !ok {
		return nil, false
	}
	contentURL := strings.TrimSpace(submatch1(redheadsoundContentURLRe, iframe))
	if contentURL == "" {
		// Try m3u8 in iframe body
		m3u8 := r.extractM3u8(iframe)
		if m3u8 != "" {
			return &redheadsoundEmbedResult{
				streamURL: m3u8,
				headers:   r.buildStreamHeaders(iframe, iframeURI),
				quality:   "1080p",
			}, true
		}
		return nil, false
	}
	contentURL = strings.ReplaceAll(contentURL, "&amp;", "&")
	contentURL = strings.ReplaceAll(contentURL, `\u0026`, "&")
	if strings.HasPrefix(contentURL, "//") {
		contentURL = "https:" + contentURL
	}
	if strings.HasPrefix(contentURL, "/") {
		contentURL = redheadsoundAbsoluteURL(iframeURI, contentURL)
	}
	headers := r.buildStreamHeaders(iframe, iframeURI)
	return &redheadsoundEmbedResult{
		streamURL: contentURL,
		headers:   headers,
		quality:   "1080p",
	}, true
}

// embedViaVideoID tries response.php?video_id= endpoint.
func (r *redheadsoundChecker) embedViaVideoID(ctx context.Context, newsHTML, pageURL string) (*redheadsoundEmbedResult, bool) {
	videoID := submatch1(redheadsoundVideoIDRe, newsHTML)
	if videoID == "" {
		videoID = submatch1(redheadsoundVideoIDRe2, newsHTML)
	}
	if videoID == "" {
		log.Debug().Msg("redheadsound: no iframe, no video_id")
		return nil, false
	}
	log.Debug().Str("videoID", videoID).Msg("redheadsound: trying response.php")

	respBody, ok := r.fetchURL(ctx, r.host+"/response.php?video_id="+videoID)
	if !ok {
		return nil, false
	}

	m3u8 := r.extractM3u8(respBody)
	if m3u8 == "" {
		log.Debug().Msg("redheadsound: no m3u8 in response.php")
		return nil, false
	}
	log.Debug().Str("m3u8", m3u8).Msg("redheadsound: got m3u8 from response.php")

	headers := r.buildStreamHeaders(respBody, pageURL)
	return &redheadsoundEmbedResult{
		streamURL: m3u8,
		headers:   headers,
		quality:   "1080p",
	}, true
}

// embedViaTokenMovie tries lists.php with token_movie parameter.
func (r *redheadsoundChecker) embedViaTokenMovie(ctx context.Context, iframeHTML, iframeURI string) *redheadsoundEmbedResult {
	u, err := url.Parse(iframeURI)
	if err != nil {
		return nil
	}
	q := u.Query()
	tokenMovie := q.Get("token_movie")
	if tokenMovie == "" {
		tokenMovie = q.Get("tokenMovie")
	}
	token := q.Get("token")
	if tokenMovie == "" || token == "" {
		return nil
	}

	baseURL := u.Scheme + "://" + u.Host
	listURLs := []string{
		baseURL + "/lists.php?token_movie=" + url.QueryEscape(tokenMovie) + "&token=" + url.QueryEscape(token),
		baseURL + "/lists.php?token=" + url.QueryEscape(token) + "&token_movie=" + url.QueryEscape(tokenMovie),
		baseURL + "/index.php?token_movie=" + url.QueryEscape(tokenMovie) + "&token=" + url.QueryEscape(token) + "&action=lists",
	}

	for _, listURL := range listURLs {
		body, ok := r.fetchURL(ctx, listURL)
		if !ok || body == "" {
			continue
		}
		m3u8 := r.extractM3u8(body)
		if m3u8 == "" {
			// Try relative m3u8 paths
			m3u8Re := regexp.MustCompile(`(?:(/[^\"\s]+\.m3u8[^\"\s]*)|(?:index-[^\"\s]+\.m3u8[^\"\s]*))`)
			if match := m3u8Re.FindString(body); match != "" {
				m3u8 = match
			}
		}
		if m3u8 != "" {
			if strings.HasPrefix(m3u8, "/") {
				m3u8 = baseURL + m3u8
			} else if !strings.HasPrefix(m3u8, "http") {
				m3u8 = baseURL + "/" + m3u8
			}
			log.Debug().Str("m3u8", m3u8).Msg("redheadsound: got m3u8 from lists.php")
			headers := r.buildStreamHeaders(body, iframeURI)
			return &redheadsoundEmbedResult{
				streamURL: m3u8,
				headers:   headers,
				quality:   "1080p",
			}
		}
	}
	return nil
}

// embedViaBNSI tries the /bnsi/movies/ endpoint with token.
func (r *redheadsoundChecker) embedViaBNSI(ctx context.Context, iframeHTML, iframeURI string) *redheadsoundEmbedResult {
	u, err := url.Parse(iframeURI)
	if err != nil {
		return nil
	}
	token := u.Query().Get("token")
	if token == "" {
		return nil
	}

	movieID := submatch1(redheadsoundMovieIDRe, iframeHTML)
	if movieID == "" {
		movieID = submatch1(redheadsoundMovieIDRe2, iframeHTML)
	}
	if movieID == "" {
		return nil
	}

	baseURL := u.Scheme + "://" + u.Host
	movieURL := baseURL + "/bnsi/movies/" + movieID

	tokenMovie := u.Query().Get("token_movie")
	if tokenMovie == "" {
		tokenMovie = u.Query().Get("tokenMovie")
	}

	postData := "token=" + url.QueryEscape(token) + "&av1=true&autoplay=0&audio=&subtitle="
	referer := baseURL + "/?token_movie=" + url.QueryEscape(tokenMovie) + "&token=" + url.QueryEscape(token)

	body, ok := r.fetchPost(ctx, movieURL, postData, map[string]string{
		"Accept":           "*/*",
		"X-Requested-With": "XMLHttpRequest",
		"Content-Type":     "application/x-www-form-urlencoded; charset=UTF-8",
		"Origin":           baseURL,
		"Referer":          referer,
	})
	if !ok || body == "" {
		return nil
	}

	m3u8 := r.extractM3u8(body)
	if m3u8 != "" {
		if strings.HasPrefix(m3u8, "/") {
			m3u8 = baseURL + m3u8
		} else if !strings.HasPrefix(m3u8, "http") {
			m3u8 = baseURL + "/" + m3u8
		}
		log.Debug().Str("m3u8", m3u8).Msg("redheadsound: got m3u8 from bnsi/movies")
		headers := r.buildStreamHeaders(body, iframeURI)
		return &redheadsoundEmbedResult{
			streamURL: m3u8,
			headers:   headers,
			quality:   "1080p",
		}
	}
	return nil
}

// extractM3u8 finds m3u8 URL in response body.
func (r *redheadsoundChecker) extractM3u8(body string) string {
	// Try escaped URLs first (JSON responses)
	m := redheadsoundM3u8Re.FindString(body)
	if m != "" {
		return strings.ReplaceAll(m, `\/`, "/")
	}
	// Try plain URLs
	m = redheadsoundM3u8Re2.FindString(body)
	if m != "" {
		return m
	}
	return ""
}

// buildStreamHeaders extracts Bearer token, Accepts-Controls header, and
// Origin/Referer from the response body. Returns nil if nothing found.
func (r *redheadsoundChecker) buildStreamHeaders(body, referer string) map[string]string {
	if body == "" {
		return nil
	}

	// Extract Bearer token
	bearer := submatch1(redheadsoundBearerRe, body)
	if bearer == "" {
		bearer = submatch1(redheadsoundBearerRe2, body)
	}
	if bearer == "" {
		bearer = submatch1(redheadsoundBearerRe3, body)
	}

	// Extract Accepts-Controls
	acceptsControls := submatch1(redheadsoundAcceptsRe, body)
	if acceptsControls == "" {
		acceptsControls = submatch1(redheadsoundAcceptsRe2, body)
	}

	// Build Origin/Referer from iframe URL
	var origin, ref string
	if referer != "" {
		if u, err := url.Parse(referer); err == nil {
			origin = u.Scheme + "://" + u.Host
			ref = referer
		}
	}

	if bearer == "" && acceptsControls == "" && origin == "" {
		return nil
	}

	hdrs := map[string]string{}
	if bearer != "" {
		hdrs["Authorization"] = "Bearer " + bearer
	}
	if acceptsControls != "" {
		hdrs["Accepts-Controls"] = acceptsControls
	}
	if origin != "" {
		hdrs["Origin"] = origin
		if ref != "" {
			hdrs["Referer"] = ref
		} else {
			hdrs["Referer"] = origin + "/"
		}
	}
	return hdrs
}

func (r *redheadsoundChecker) selectSearchLink(search, title string, year int) (string, bool) {
	return r.selectSearchLinkEx(search, title, "", year)
}

// selectSearchLinkEx selects the best matching link from search results.
// Tries title first, then origTitle if no match.  Prefers exact year match.
func (r *redheadsoundChecker) selectSearchLinkEx(search, title, origTitle string, year int) (string, bool) {
	rows := parseSearchBlocks(search, redheadsoundBlockRe)
	if len(rows) == 0 {
		rows = parseSearchBlocks(search, redheadsoundBlockFallbackRe)
	}
	if len(rows) == 0 {
		return "", false
	}

	// Build list of normalized search needles — try title and original_title
	var needles []string
	if t := normalizeSearchTitle(title); t != "" {
		needles = append(needles, t)
	}
	if t := normalizeSearchTitle(origTitle); t != "" {
		needles = append(needles, t)
	}
	if len(needles) == 0 {
		return "", false
	}

	// Pass 1: exact title + exact year
	for _, want := range needles {
		for _, row := range rows {
			rowTitle := normalizeSearchTitle(row.title)
			if rowTitle == "" {
				continue
			}
			if rowTitle == want && (year <= 0 || row.year == year) {
				log.Debug().Str("link", row.link).Str("title", row.title).Msg("redheadsound: search match (exact+year)")
				return row.link, true
			}
		}
	}

	// Pass 2: title contains + exact year
	for _, want := range needles {
		for _, row := range rows {
			rowTitle := normalizeSearchTitle(row.title)
			if rowTitle == "" {
				continue
			}
			if strings.Contains(rowTitle, want) && (year <= 0 || row.year == year) {
				log.Debug().Str("link", row.link).Str("title", row.title).Msg("redheadsound: search match (contains+year)")
				return row.link, true
			}
		}
	}

	// Pass 3: title contains (any year)
	for _, want := range needles {
		for _, row := range rows {
			rowTitle := normalizeSearchTitle(row.title)
			if rowTitle == "" {
				continue
			}
			if strings.Contains(rowTitle, want) {
				log.Debug().Str("link", row.link).Str("title", row.title).Msg("redheadsound: search match (contains)")
				return row.link, true
			}
		}
	}

	// Pass 4: check if search query appears in the URL slug
	for _, want := range needles {
		for _, row := range rows {
			slug := strings.ToLower(row.link)
			if strings.Contains(slug, want) {
				log.Debug().Str("link", row.link).Msg("redheadsound: search match (URL slug)")
				return row.link, true
			}
		}
	}

	// No fallback to "first result" — RHS returns the full homepage carousel
	// when search has no real results, which would silently pick a random film
	// for English-only queries.
	return "", false
}

type rhsSearchRow struct {
	link  string
	title string
	year  int
}

// parseSearchBlocks splits HTML by block regex and extracts link/title/year from each block.
func parseSearchBlocks(html string, blockRe *regexp.Regexp) []rhsSearchRow {
	locs := blockRe.FindAllStringIndex(html, -1)
	if len(locs) == 0 {
		return nil
	}

	var rows []rhsSearchRow
	for i, loc := range locs {
		start := loc[0]
		end := len(html)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		block := html[start:end]

		// Extract first href
		hrefM := redheadsoundBlockHrefRe.FindStringSubmatch(block)
		if len(hrefM) < 2 || strings.TrimSpace(hrefM[1]) == "" {
			continue
		}
		link := strings.TrimSpace(hrefM[1])

		// Extract title from h2/h3/h4 > a
		titleStr := ""
		if m := redheadsoundBlockTitleRe.FindStringSubmatch(block); len(m) >= 2 {
			titleStr = strings.TrimSpace(m[1])
		}

		// Extract year from span.year or fallback to any 4-digit year
		yearVal := 0
		if m := redheadsoundBlockYearRe.FindStringSubmatch(block); len(m) >= 2 {
			yearVal, _ = strconv.Atoi(m[1])
		}

		rows = append(rows, rhsSearchRow{link: link, title: titleStr, year: yearVal})
	}
	return rows
}

// ============== Series parsing ==============

// parseSeasonsFromHTML extracts season links from a series/season page.
func (r *redheadsoundChecker) parseSeasonsFromHTML(html string) []rhsSeason {
	matches := redheadsoundSeasonLinkRe.FindAllStringSubmatch(html, -1)
	if len(matches) == 0 {
		return nil
	}

	var seasons []rhsSeason
	seen := make(map[int]bool)
	for _, m := range matches {
		href := strings.TrimSpace(m[1])
		numStr := strings.TrimSpace(m[2])
		num, err := strconv.Atoi(numStr)
		if err != nil || num < 1 {
			continue
		}
		if seen[num] {
			continue
		}
		seen[num] = true
		seasons = append(seasons, rhsSeason{
			num: num,
			url: redheadsoundAbsoluteURL(r.host, href),
		})
	}
	return seasons
}

// parseSeasonPage fetches a season page and extracts episodes with their
// Kinescope video URLs.  For episodes to have video URLs, the user must
// be logged in (PRO subscription) — otherwise only episode numbers are
// returned.
func (r *redheadsoundChecker) parseSeasonPage(ctx context.Context, seasonURL string) []rhsEpisode {
	// Ensure we're logged in for PRO content
	r.ensureLoggedIn(ctx)

	body, ok := r.fetchURL(ctx, seasonURL)
	if !ok {
		log.Debug().Str("url", seasonURL).Msg("redheadsound: failed to fetch season page")
		return nil
	}

	// Parse episode numbers from video-card blocks
	episodes := r.parseEpisodesFromHTML(body)
	if len(episodes) == 0 {
		return nil
	}

	// Try to extract Kinescope video URLs from the page.
	// For PRO users, initMainPlayer contains the video URL(s).
	r.extractEpisodeVideoURLs(ctx, body, episodes)

	return episodes
}

// parseEpisodesFromHTML extracts episode numbers from video-card blocks.
func (r *redheadsoundChecker) parseEpisodesFromHTML(html string) []rhsEpisode {
	// Find all video-card blocks
	cardLocs := redheadsoundVideoCardRe.FindAllStringIndex(html, -1)
	if len(cardLocs) == 0 {
		return nil
	}

	var episodes []rhsEpisode
	for i, loc := range cardLocs {
		start := loc[0]
		end := len(html)
		if i+1 < len(cardLocs) {
			end = cardLocs[i+1][0]
		}
		block := html[start:end]

		// Extract episode number
		m := redheadsoundEpisodeNumRe.FindStringSubmatch(block)
		if len(m) < 2 {
			continue
		}
		num, err := strconv.Atoi(strings.TrimSpace(m[1]))
		if err != nil || num < 1 {
			continue
		}
		episodes = append(episodes, rhsEpisode{num: num})
	}
	return episodes
}

// extractEpisodeVideoURLs tries to extract Kinescope video URLs from the page JS.
// For PRO users, the page contains initMainPlayer() with video URL(s).
// Falls back to the full movie embed pipeline (iframes, response.php, etc.)
// just like embed() does for movies.
func (r *redheadsoundChecker) extractEpisodeVideoURLs(ctx context.Context, pageHTML string, episodes []rhsEpisode) {
	if len(episodes) == 0 {
		return
	}

	// Method 1: single videoUrl (like movies) — applies to all episodes if
	// the page uses a playlist-style Kinescope player
	singleURL := strings.TrimSpace(submatch1(redheadsoundVideoURLRe, pageHTML))
	if singleURL != "" {
		log.Debug().Str("videoUrl", singleURL).Msg("redheadsound: series has single videoUrl (playlist)")
		// This is a Kinescope embed URL; fetch it to get contentUrl
		result, ok := r.embedViaKinescope(ctx, singleURL)
		if ok && result != nil && result.streamURL != "" {
			// Single playlist URL — assign to all episodes
			for i := range episodes {
				episodes[i].streamURL = result.streamURL
				episodes[i].headers = result.headers
			}
			return
		}
	}

	// Method 2: per-episode video URLs in JS (switch/case or map)
	embedMatches := redheadsoundEpisodeVideoMapRe.FindAllStringSubmatch(pageHTML, -1)
	if len(embedMatches) > 0 {
		episodeURLMap := make(map[int]string)
		for _, m := range embedMatches {
			epNum, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			episodeURLMap[epNum] = m[2]
		}
		log.Debug().Int("mapped", len(episodeURLMap)).Msg("redheadsound: found per-episode video URLs")

		for i := range episodes {
			embedURL, ok := episodeURLMap[episodes[i].num]
			if !ok {
				continue
			}
			result, ok := r.embedViaKinescope(ctx, embedURL)
			if ok && result != nil {
				episodes[i].streamURL = result.streamURL
				episodes[i].headers = result.headers
			}
		}
		return
	}

	// Method 3: find ALL Kinescope embed URLs on the page and match by order
	allEmbeds := redheadsoundKinescopeEmbedRe.FindAllStringSubmatch(pageHTML, -1)
	if len(allEmbeds) > 0 {
		// Filter unique embed URLs (skip trailer URL if present)
		seen := make(map[string]bool)
		var uniqueEmbeds []string
		for _, m := range allEmbeds {
			fullURL := m[0]
			if seen[fullURL] {
				continue
			}
			seen[fullURL] = true
			uniqueEmbeds = append(uniqueEmbeds, fullURL)
		}
		log.Debug().Int("embeds", len(uniqueEmbeds)).Msg("redheadsound: found Kinescope embed URLs on page")

		// If we have exactly episodes+1 URLs, the first one might be the trailer
		startIdx := 0
		if len(uniqueEmbeds) == len(episodes)+1 {
			startIdx = 1 // skip trailer
		}

		for i := range episodes {
			idx := startIdx + i
			if idx >= len(uniqueEmbeds) {
				break
			}
			embedURL := uniqueEmbeds[idx]
			if !strings.HasPrefix(embedURL, "http") {
				embedURL = "https://" + embedURL
			}
			result, ok := r.embedViaKinescope(ctx, embedURL)
			if ok && result != nil {
				episodes[i].streamURL = result.streamURL
				episodes[i].headers = result.headers
			}
		}
		// Check if at least one episode got a stream
		for _, ep := range episodes {
			if ep.streamURL != "" {
				return
			}
		}
	}

	// Method 4: Full movie embed pipeline fallback.
	// Same extraction methods as embed() uses for movies — iframe, video_id,
	// response.php, token_movie, bnsi.  This handles cases where the season
	// page embeds video the same way a movie page does.
	log.Debug().Msg("redheadsound: trying full movie embed pipeline on season page")
	result := r.embedFromPageHTML(ctx, pageHTML)
	if result != nil && result.streamURL != "" {
		log.Debug().Str("stream", result.streamURL).Msg("redheadsound: got stream from movie pipeline for series")
		for i := range episodes {
			episodes[i].streamURL = result.streamURL
			episodes[i].headers = result.headers
		}
	}
}

// embedFromPageHTML applies the full movie embed extraction pipeline to
// already-fetched page HTML.  Reuses the same logic as embed() but skips
// the search step (we already have the page content).
func (r *redheadsoundChecker) embedFromPageHTML(ctx context.Context, pageHTML string) *redheadsoundEmbedResult {
	// === Try iframe src with priorities: ladoni/lat > stloadi/token > any ===
	iframeURI := r.findBestIframe(pageHTML)
	if iframeURI == "" {
		// === Try response.php?video_id from the page itself ===
		videoID := submatch1(redheadsoundVideoIDRe, pageHTML)
		if videoID == "" {
			videoID = submatch1(redheadsoundVideoIDRe2, pageHTML)
		}
		if videoID != "" {
			log.Debug().Str("videoID", videoID).Msg("redheadsound: series page has video_id, trying response.php")
			respBody, ok := r.fetchURL(ctx, r.host+"/response.php?video_id="+videoID)
			if ok {
				m3u8 := r.extractM3u8(respBody)
				if m3u8 != "" {
					log.Debug().Str("m3u8", m3u8).Msg("redheadsound: got m3u8 from response.php (series)")
					headers := r.buildStreamHeaders(respBody, r.host+"/")
					return &redheadsoundEmbedResult{
						streamURL: m3u8,
						headers:   headers,
						quality:   "1080p",
					}
				}
			}
		}
		// === Try m3u8 directly in page HTML ===
		m3u8 := r.extractM3u8(pageHTML)
		if m3u8 != "" {
			log.Debug().Str("m3u8", m3u8).Msg("redheadsound: found m3u8 directly in series page")
			return &redheadsoundEmbedResult{
				streamURL: m3u8,
				quality:   "1080p",
			}
		}
		return nil
	}

	log.Debug().Str("iframeURI", iframeURI).Msg("redheadsound: series page has iframe src")

	// Fetch iframe content
	iframe, ok := r.fetchURLWithReferer(ctx, iframeURI, r.host+"/")
	if !ok {
		log.Debug().Str("iframeURI", iframeURI).Msg("redheadsound: fetch series iframe failed")
		return nil
	}

	// Check for contentUrl (Kinescope)
	contentURL := strings.TrimSpace(submatch1(redheadsoundContentURLRe, iframe))
	if contentURL != "" {
		contentURL = strings.ReplaceAll(contentURL, "&amp;", "&")
		contentURL = strings.ReplaceAll(contentURL, `\u0026`, "&")
		if strings.HasPrefix(contentURL, "//") {
			contentURL = "https:" + contentURL
		}
		if strings.HasPrefix(contentURL, "/") {
			contentURL = redheadsoundAbsoluteURL(iframeURI, contentURL)
		}
		log.Debug().Str("contentURL", contentURL).Msg("redheadsound: found contentUrl in series iframe")
		headers := r.buildStreamHeaders(iframe, iframeURI)
		return &redheadsoundEmbedResult{
			streamURL: contentURL,
			headers:   headers,
			quality:   "1080p",
		}
	}

	// Check for m3u8 directly in iframe response
	m3u8 := r.extractM3u8(iframe)
	if m3u8 != "" {
		log.Debug().Str("m3u8", m3u8).Msg("redheadsound: found m3u8 in series iframe")
		headers := r.buildStreamHeaders(iframe, iframeURI)
		return &redheadsoundEmbedResult{
			streamURL: m3u8,
			headers:   headers,
			quality:   "1080p",
		}
	}

	// === Try token_movie / stloadi lists.php ===
	result := r.embedViaTokenMovie(ctx, iframe, iframeURI)
	if result != nil {
		return result
	}

	// === Try bnsi/movies endpoint ===
	result = r.embedViaBNSI(ctx, iframe, iframeURI)
	if result != nil {
		return result
	}

	log.Debug().Msg("redheadsound: no stream source found in series iframe")
	return nil
}

// ensureLoggedIn performs DLE login if credentials are configured and we
// haven't logged in yet.
func (r *redheadsoundChecker) ensureLoggedIn(ctx context.Context) {
	if r.loggedIn || r.login == "" || r.password == "" {
		return
	}

	form := url.Values{}
	form.Set("login_name", r.login)
	form.Set("login_password", r.password)
	form.Set("login", "submit")

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, r.host+"/", strings.NewReader(form.Encode()))
	if err != nil {
		log.Warn().Err(err).Msg("redheadsound: login request build failed")
		return
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("Origin", r.host)
	httpReq.Header.Set("Referer", r.host+"/")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")

	resp, err := r.client.Do(httpReq)
	if err != nil {
		log.Warn().Err(err).Msg("redheadsound: login request failed")
		return
	}
	defer resp.Body.Close()
	io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // drain

	// Check if login succeeded by looking for dle_user_id cookie
	if resp.Request != nil && resp.Request.URL != nil {
		for _, cookie := range r.client.Jar.Cookies(resp.Request.URL) {
			if cookie.Name == "dle_user_id" && cookie.Value != "" {
				r.loggedIn = true
				log.Info().Msg("redheadsound: login successful (PRO)")
				return
			}
		}
	}
	// Also check response cookies
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "dle_user_id" && cookie.Value != "" {
			r.loggedIn = true
			log.Info().Msg("redheadsound: login successful (PRO)")
			return
		}
	}
	log.Warn().Msg("redheadsound: login may have failed (no dle_user_id cookie)")
	r.loggedIn = true // proceed anyway — might work with partial cookies
}

// ============== HTTP helpers ==============

func (r *redheadsoundChecker) fetchMain(ctx context.Context) (string, bool) {
	// xray residential SOCKS5 intermittently resets connections on the first
	// request (especially for the 140KB homepage). Retry up to 3 times with
	// short backoff before giving up.
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, r.host, nil)
		if err != nil {
			log.Debug().Err(err).Msg("redheadsound: fetchMain: NewRequest failed")
			return "", false
		}
		httpReq.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		httpReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36")

		resp, err := r.client.Do(httpReq)
		if err != nil {
			lastErr = err
			log.Debug().Err(err).Int("attempt", attempt).Str("host", r.host).Msg("redheadsound: fetchMain: request failed, retrying")
			time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			log.Debug().Int("status", resp.StatusCode).Int("attempt", attempt).Str("server", resp.Header.Get("Server")).Msg("redheadsound: fetchMain: non-2xx")
			_ = resp.Body.Close()
			if resp.StatusCode == 403 {
				// DDoS-Guard geoblock — retrying won't help, SOCKS5 isn't effective.
				return "", false
			}
			time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		_ = resp.Body.Close()
		if err != nil {
			lastErr = err
			log.Debug().Err(err).Int("attempt", attempt).Msg("redheadsound: fetchMain: read body failed, retrying")
			time.Sleep(time.Duration(attempt) * 500 * time.Millisecond)
			continue
		}
		return string(body), true
	}
	log.Debug().Err(lastErr).Msg("redheadsound: fetchMain: all attempts failed")
	return "", false
}

// fetchSearchOld performs the old ajax search: POST /engine/ajax/controller.php?mod=search
func (r *redheadsoundChecker) fetchSearchOld(ctx context.Context, query, userHash string) string {
	form := url.Values{}
	form.Set("query", query)
	form.Set("skin", "rhs_new")
	form.Set("user_hash", userHash)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		r.host+"/engine/ajax/controller.php?mod=search",
		strings.NewReader(form.Encode()))
	if err != nil {
		return ""
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	httpReq.Header.Set("Accept", "*/*")
	// X-Requested-With is required — DLE checks it to distinguish a real
	// in-browser AJAX call from a scraper; without it the endpoint replies
	// "session expired" (218 bytes) even with a valid user_hash and cookies.
	httpReq.Header.Set("X-Requested-With", "XMLHttpRequest")
	httpReq.Header.Set("Referer", r.host+"/")
	httpReq.Header.Set("Origin", r.host)
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36")

	resp, err := r.client.Do(httpReq)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return ""
	}
	return string(body)
}

func (r *redheadsoundChecker) fetchURL(ctx context.Context, target string) (string, bool) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", false
	}
	httpReq.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")

	resp, err := r.client.Do(httpReq)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", false
	}
	return string(body), true
}

func (r *redheadsoundChecker) fetchURLWithReferer(ctx context.Context, target, referer string) (string, bool) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", false
	}
	httpReq.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	if referer != "" {
		httpReq.Header.Set("Referer", referer)
	}

	// Use CDN client (may route through SOCKS5 proxy) for CDN/player requests.
	resp, err := r.cdnClient.Do(httpReq)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", false
	}
	return string(body), true
}

func (r *redheadsoundChecker) fetchPost(ctx context.Context, target, postData string, headers map[string]string) (string, bool) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(postData))
	if err != nil {
		return "", false
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := r.cdnClient.Do(httpReq)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", false
	}
	return string(body), true
}

func redheadsoundAbsoluteURL(base, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "://") {
		return raw
	}
	if strings.HasPrefix(raw, "/") {
		if u, err := url.Parse(base); err == nil {
			return u.Scheme + "://" + u.Host + raw
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	baseURL, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return raw
	}
	return baseURL.ResolveReference(u).String()
}
