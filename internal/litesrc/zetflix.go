package litesrc

import (
	"context"
	"encoding/base64"
	stdjson "encoding/json"
	"fmt"
	"io"
	"lampac-go/internal/httpclient"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/proxylink"

	"github.com/rs/zerolog/log"
)

var (
	zetflixMovieFileRe   = regexp.MustCompile(`file\s*:\s*"([^"]+)"`)
	zetflixQualityURLRe  = regexp.MustCompile(`\[(1080|720|480|360|240)p?\](https?://[a-zA-Z0-9_./:?&=%+~-]+)`)
	zetflixEpisodeNumRe  = regexp.MustCompile(`^([0-9]+)`)
	zetflixGoDomainRe    = regexp.MustCompile(`"([^"]+)"\);</script>`)
	zetflixGoPrefixRe    = regexp.MustCompile(`^https?://go\.`)
	zetflixJSKeyQuoteRe  = regexp.MustCompile(`(\{|,)\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*:`)
	zetflixNoSpaceKeyRe  = regexp.MustCompile(`(?s)(\{|,)([a-zA-Z_][a-zA-Z0-9_]*)\s*:`)
	zetflixTmdbSeasonsRe = regexp.MustCompile(`"number_of_seasons"\s*:\s*([0-9]+)`)
	// Match the HDVB player config object literal. Two variable names seen:
	//   - `let|var playerConfigs = {…};` (older HDVBPlayer movie/serial iframes)
	//   - `let p2aCon = {…}; var p2a = new Playerjs(p2aCon)` (current, 2026-06 —
	//     HDVB switched the player from HDVBPlayer to Playerjs and renamed the
	//     config var). Same JSON shape (file/key/href). Missing this name made
	//     hdvb/zetflix resolve fail silently ("movie stream resolve failed").
	zetflixPlayerConfigsRe  = regexp.MustCompile(`(?:let|var)\s+(?:playerConfigs|p2aCon)\s*=\s*(\{.*?\})\s*;`)
	zetflixFallbackSrcRe    = regexp.MustCompile(`data-fallback-src="([^"]+)"`)
	zetflixObrutIframeSrcRe = regexp.MustCompile(`<iframe[^>]+src="(https://[^"]*obrut\.show/embed/[^"]+)"`)
	zetflixObritQualityRe   = regexp.MustCompile(`\[(\d+)p?\](https://cdn-[a-zA-Z0-9_./:?&=%+~-]+)`)
)

const zetflixObrutHost = "54243ba5.obrut.show"

// zetflixEncodeObrutKPID encodes a kinopoisk ID for the obrut.show embed URL.
// Algorithm: base64(strconv(kpID)) → STRIP the '=' padding → reverse.
//
// Order matters: padding must go before the reverse. Reversing first moves the
// '=' to the FRONT, where TrimRight cannot see it — the id then goes out as
// "==wM2AjNzITM" and obrut answers 404. Since padding appears whenever the id's
// digit count is not a multiple of 3, that silently killed the obrut path for
// most ids (7- and 8-digit ones especially), leaving only 3/6/9-digit ids working.
func zetflixEncodeObrutKPID(kpID int64) string {
	b64 := base64.RawStdEncoding.EncodeToString([]byte(strconv.FormatInt(kpID, 10)))
	runes := []rune(b64)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}

type zetflixChecker struct {
	client      *http.Client
	host        string
	useHLS      bool
	cdnReplace  string // if set, replace "prosto.hdvideobox.me" with this host in stream URLs
	streamProxy bool   // if true, proxy all stream URLs through /proxy/

	goHostMu   sync.RWMutex
	goHostVal  string
	goHostTill time.Time

	// obrutCookieCache stores obrut.show session cookies keyed by obrut host.
	// CDN URLs (cdn-*.obrut.show) require these cookies for 302 redirect.
	// Updated each time fetchViaObrut is called; used by manifest() endpoint.
	obrutCookieCache sync.Map // map[string]obrutCookieEntry

	// obrutEmbedCache caches parsed obrut embed results keyed by "kpID:season".
	// Obrut CDN is inconsistent — different nodes return different episode counts.
	// We keep the result with the most episodes and only update if a new result
	// has more episodes (= came from a better CDN node).
	obrutEmbedCache sync.Map // map[string]obrutEmbedCacheEntry

	janitorOnce sync.Once
}

// zetflixJanitorInterval: sweep expired obrutEmbedCache entries. Without this,
// entries for never-re-requested kp:season pairs live for the process lifetime.
const zetflixJanitorInterval = 20 * time.Minute

func (z *zetflixChecker) startJanitor() {
	z.janitorOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(zetflixJanitorInterval)
			defer ticker.Stop()
			for range ticker.C {
				now := time.Now()
				z.obrutEmbedCache.Range(func(k, v any) bool {
					if ce, ok := v.(obrutEmbedCacheEntry); ok && now.After(ce.expires) {
						z.obrutEmbedCache.Delete(k)
					}
					return true
				})
			}
		}()
	})
}

type obrutCookieEntry struct {
	cookies string    // "key=val; key2=val2"
	expires time.Time // when to consider stale
}

type obrutEmbedCacheEntry struct {
	embed    zetflixEmbed
	episodes int       // number of episodes (for comparison)
	expires  time.Time // TTL
}

type zetflixRoot struct {
	Title  string          `json:"title"`
	File   string          `json:"file"`
	Folder []zetflixFolder `json:"folder"`
}

type zetflixFolder struct {
	Comment string `json:"comment"`
	File    string `json:"file"`
}

type zetflixEmbed struct {
	PL      []zetflixRoot
	Movie   bool
	Quality string
	// Estimated season count from provider payload (if available).
	SeasonCount int
	// Cookies from the obrut.show session — needed by cdn-*.obrut.show for 302 redirect.
	// Stored as "key=val; key2=val2" ready for Cookie header.
	ObrutCookies string
}

// ZetflixPlayerConfig holds fields from the HDVBPlayer playerConfigs JSON
// embedded in the zetflix HTML page.
type ZetflixPlayerConfig struct {
	File       string `json:"file"`
	Key        string `json:"key"`
	Href       string `json:"href"`
	HLS        int    `json:"hls"`
	Translator string `json:"translator"`
	Referer    string `json:"referer"`
}

func NewZetflixChecker(cfg config.Config) *zetflixChecker {
	host := strings.TrimSpace(strings.TrimRight(cfg.Online.Zetflix.Host, "/"))
	if host == "" {
		host = "https://go.zet-flix.online"
	}
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}

	cdnReplace := strings.TrimSpace(cfg.Online.Zetflix.CDN)
	if cdnReplace != "" {
		cdnReplace = strings.TrimRight(cdnReplace, "/")
		if !strings.Contains(cdnReplace, "://") {
			cdnReplace = "https://" + cdnReplace
		}
	}

	// Use proxied client (via SOCKS5/VLESS) when available — zetflix CDN
	// blocks non-RU IPs, so all API+CDN requests must go through the proxy.
	client := httpclient.NewForBalancer("zetflix", 14*time.Second)

	return &zetflixChecker{
		client:      client,
		host:        host,
		useHLS:      cfg.Online.Zetflix.HLS,
		cdnReplace:  cdnReplace,
		streamProxy: cfg.Online.Zetflix.StreamProxy,
	}
}

func (z *zetflixChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := strings.Trim(strings.TrimPrefix(strings.ToLower(r.URL.Path), "/lite/"), "/")

		// /lite/zetflix/manifest — resolve CDN redirect server-side (like videodb).
		if raw == "zetflix/manifest" || raw == "zetflix/manifest.m3u8" || raw == "zetflix/manifest.mp4" {
			z.manifest(w, r, links)
			return
		}

		if parseBoolParam(r.URL.Query().Get("checksearch")) {
			show, quality := z.checkSearch(r)
			writeCheckSearchResponse(w, show, quality)
			return
		}

		z.index(w, r, links)
	}
}

// manifest handles /lite/zetflix/manifest — resolves CDN 302 redirects server-side.
// CDN URLs (cdn-*.obrut.show) return 302 → superdupercdn, but only with proper cookies+headers.
//
// Query params:
//   - link=CDN_URL — single link (legacy, fallback)
//   - links=JSON — all quality links: {"240p":"cdn_url","720p":"cdn_url",...}
//
// If the URL path ends in .m3u8, it returns an HTTP 302 redirect (for HLS players).
// Otherwise it returns JSON with quality map:
//
//	{"method":"play","url":"best_url","quality":{"240p":"url","720p":"url",...},"qualitys":{...}}
func (z *zetflixChecker) manifest(w http.ResponseWriter, r *http.Request, links *proxylink.Manager) {
	lower := strings.ToLower(r.URL.Path)
	play := strings.HasSuffix(lower, ".m3u8") || strings.HasSuffix(lower, ".mp4")
	obrutCookies := z.currentObrutCookies()

	// Try multi-quality links param first.
	linksJSON := strings.TrimSpace(r.URL.Query().Get("links"))
	if linksJSON != "" {
		var qualLinks map[string]string
		if err := stdjson.Unmarshal([]byte(linksJSON), &qualLinks); err != nil {
			log.Warn().Err(err).Msg("zetflix: manifest bad links JSON")
		} else if len(qualLinks) > 0 {
			z.manifestMulti(w, r, links, qualLinks, play)
			return
		}
	}

	// Fallback: single link param.
	link := strings.TrimSpace(r.URL.Query().Get("link"))
	if link == "" {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	linkSnippet := link
	if len(linkSnippet) > 80 {
		linkSnippet = linkSnippet[:80]
	}
	log.Info().Str("link", linkSnippet).Msg("zetflix: manifest request (single)")

	target := strings.TrimSpace(link)
	if strings.Contains(target, "obrut.show") {
		// Resolve fresh redirect to superduper before proxy wrapping.
		if resolved := z.resolveObrutLocation(r, target); resolved != "" {
			target = resolved
		}
	}
	// Strip HLS suffix for MP4 mode — superdupercdn serves direct MP4 without the suffix.
	if !z.useHLS {
		target = strings.ReplaceAll(target, ":hls:manifest.m3u8", "")
	}
	if links != nil && target != "" {
		target = z.proxyWithHeaders(r, target, links, obrutCookies)
	} else {
		// Legacy/no-proxylink mode: resolve redirect once and return direct URL.
		if resolved := z.resolveObrutLocation(r, target); resolved != "" {
			target = resolved
		}
		if !z.useHLS {
			target = strings.ReplaceAll(target, ":hls:manifest.m3u8", "")
		}
	}

	if play {
		http.Redirect(w, r, target, http.StatusFound)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"method":   "play",
		"url":      target,
		"quality":  map[string]any{"auto": target},
		"qualitys": map[string]any{"auto": target},
	})
}

// manifestMulti resolves multiple CDN quality links and returns a quality map.
func (z *zetflixChecker) manifestMulti(w http.ResponseWriter, r *http.Request, proxyLinks *proxylink.Manager, qualLinks map[string]string, play bool) {
	log.Info().Int("count", len(qualLinks)).Msg("zetflix: manifest request (multi)")
	obrutCookies := z.currentObrutCookies()
	host := hostFromRequest(r)

	qualityMap := make(map[string]any, len(qualLinks))
	var bestURL string
	var bestRes int

	for rawLabel, cdnURL := range qualLinks {
		// Parse numeric resolution before normalizing label.
		res := 0
		fmt.Sscanf(rawLabel, "%dp", &res)
		label := normalizeQualityLabel(rawLabel)

		target := strings.TrimSpace(cdnURL)
		if strings.Contains(target, "obrut.show") {
			// For obrut links, return per-quality on-demand manifest endpoint.
			// This refreshes redirect resolution at play time and avoids stale CDN URLs.
			// Use .mp4 extension when HLS is disabled so the player uses MP4 source, not HLS parser.
			ext := ".m3u8"
			if !z.useHLS {
				ext = ".mp4"
			}
			target = host + "/lite/zetflix/manifest" + ext + "?link=" + url.QueryEscape(target)
			qualityMap[label] = target
			if res > bestRes {
				bestRes = res
				bestURL = target
			}
			continue
		}
		// Strip HLS suffix for MP4 mode.
		if !z.useHLS {
			target = strings.ReplaceAll(target, ":hls:manifest.m3u8", "")
		}
		if proxyLinks != nil && target != "" {
			target = z.proxyWithHeaders(r, target, proxyLinks, obrutCookies)
		} else {
			if resolved := z.resolveObrutLocation(r, target); resolved != "" {
				target = resolved
			}
			if !z.useHLS {
				target = strings.ReplaceAll(target, ":hls:manifest.m3u8", "")
			}
		}
		qualityMap[label] = target

		// Track best resolution for default URL.
		if res > bestRes {
			bestRes = res
			bestURL = target
		}
	}

	if bestURL == "" {
		// Pick any.
		for _, u := range qualityMap {
			bestURL, _ = u.(string)
			break
		}
	}

	log.Info().Int("qualities", len(qualityMap)).Msg("zetflix: manifest response (multi)")

	if play {
		http.Redirect(w, r, bestURL, http.StatusFound)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"method":   "play",
		"url":      bestURL,
		"quality":  qualityMap,
		"qualitys": qualityMap,
	})
}

// resolveObrutLocation follows CDN 302 redirect (cdn-*.obrut.show → superdupercdn).
// Uses cached obrut session cookies and proper sec-fetch-* headers.
func (z *zetflixChecker) resolveObrutLocation(r *http.Request, link string) string {
	baseURL, err := url.Parse(link)
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		return ""
	}

	// Get cached cookies for obrut CDN.
	cookies := z.currentObrutCookies()

	hasCookies := cookies != ""
	hasProxy := httpclient.IsBalancerProxied("zetflix")
	log.Debug().Bool("hasCookies", hasCookies).Bool("hasProxy", hasProxy).Msg("zetflix: resolving CDN location")

	resolveClient := httpclient.NewForBalancerNoRedirect("zetflix", 12*time.Second)

	try := func(method string) (string, bool) {
		httpReq, err := http.NewRequestWithContext(r.Context(), method, link, nil)
		if err != nil {
			return "", false
		}
		httpReq.Header.Set("sec-fetch-dest", "empty")
		httpReq.Header.Set("sec-fetch-mode", "cors")
		httpReq.Header.Set("sec-fetch-site", "same-site")
		httpReq.Header.Set("origin", "https://"+zetflixObrutHost)
		httpReq.Header.Set("referer", "https://"+zetflixObrutHost+"/")
		httpReq.Header.Set("User-Agent", zetflixUserAgent)
		if cookies != "" {
			httpReq.Header.Set("Cookie", cookies)
		}

		resp, err := resolveClient.Do(httpReq)
		if err != nil {
			log.Debug().Err(err).Str("method", method).Msg("zetflix: manifest resolve failed")
			return "", false
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			loc := strings.TrimSpace(resp.Header.Get("Location"))
			if loc == "" {
				return "", false
			}
			locURL, err := url.Parse(loc)
			if err != nil {
				return "", false
			}
			resolved := baseURL.ResolveReference(locURL).String()
			log.Info().Str("from", link[:min(len(link), 80)]).Str("to", resolved[:min(len(resolved), 80)]).Msg("zetflix: CDN redirect resolved")
			return resolved, true
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return link, true
		}
		log.Debug().Int("status", resp.StatusCode).Str("method", method).Msg("zetflix: manifest resolve unexpected status")
		return "", false
	}

	if location, ok := try(http.MethodHead); ok {
		return location
	}
	if location, ok := try(http.MethodGet); ok {
		return location
	}
	return ""
}

func (z *zetflixChecker) currentObrutCookies() string {
	if entry, ok := z.obrutCookieCache.Load(zetflixObrutHost); ok {
		ce := entry.(obrutCookieEntry)
		if time.Now().Before(ce.expires) {
			return ce.cookies
		}
		log.Debug().Msg("zetflix: cached obrut cookies expired")
		return ""
	}
	log.Debug().Msg("zetflix: no cached obrut cookies (embed not yet called?)")
	return ""
}

func (z *zetflixChecker) checkSearch(r *http.Request) (bool, string) {
	kinopoiskID, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("kinopoisk_id")), 10, 64)
	serial := strings.TrimSpace(r.URL.Query().Get("serial")) == "1"
	s, set := getsTVQueryInt(r.URL.Query().Get("s"))
	if !set {
		s = -1
	}
	rs := s
	if serial && s == -1 {
		rs = 1
	}

	// --- Phase 1: cheap probes first (< 5s budget) ---
	// Resolve API host with a short timeout so we don't burn the
	// checksearch 15 s budget on DNS/connect.
	apiHost, ok := z.resolveAPIHost(r.Context())
	if !ok {
		apiHost = z.host
	}

	// Probe a concrete endpoint — fast confirmation that the CDN is up.
	if kinopoiskID > 0 && strings.TrimSpace(apiHost) != "" {
		probeURL := strings.TrimRight(apiHost, "/") + "/iplayer/videodb.php?kp=" + strconv.FormatInt(kinopoiskID, 10)
		if serial && rs > 0 {
			probeURL += "&season=" + strconv.Itoa(rs)
		}
		if z.probe(r.Context(), http.MethodGet, probeURL) {
			return true, ""
		}
	}

	if z.probe(r.Context(), http.MethodHead, apiHost) || z.probe(r.Context(), http.MethodGet, apiHost) {
		return true, ""
	}

	// If we resolved a non-empty API host, keep zetflix visible.
	if strings.TrimSpace(apiHost) != "" {
		return true, ""
	}

	// --- Phase 2: heavy fetchEmbed path (only if budget remains) ---
	// fetchEmbed runs player.php → obrut.show → anti-bot chain which
	// can take 30+ seconds.  Only attempt if the context has not been
	// cancelled yet (cheap probes might have succeeded above).
	if r.Context().Err() != nil {
		return false, ""
	}

	if kinopoiskID > 0 {
		embed, ok := z.fetchEmbed(r.Context(), kinopoiskID, rs)
		if ok && len(embed.PL) > 0 {
			return true, normalizeQualityBadge(embed.Quality)
		}
		if serial && rs == 1 {
			embed, ok = z.fetchEmbed(r.Context(), kinopoiskID, 0)
			if ok && len(embed.PL) > 0 {
				return true, normalizeQualityBadge(embed.Quality)
			}
		}
	}

	return false, ""
}

func (z *zetflixChecker) index(w http.ResponseWriter, r *http.Request, links *proxylink.Manager) {
	q := r.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	kinopoiskID, _ := strconv.ParseInt(strings.TrimSpace(q.Get("kinopoisk_id")), 10, 64)
	if kinopoiskID <= 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	serial := strings.TrimSpace(q.Get("serial")) == "1"
	voice := strings.TrimSpace(q.Get("t"))
	s, sSet := getsTVQueryInt(q.Get("s"))
	if !sSet {
		s = -1
	}

	rs := s
	if serial && s == -1 {
		rs = 1
	}

	embed, ok := z.fetchEmbed(r.Context(), kinopoiskID, rs)
	// Some serials only respond without &season= param for season 1.
	// For s>1 this fallback can return season-1 episodes.
	if (!ok || len(embed.PL) == 0) && serial && rs == 1 {
		embed, ok = z.fetchEmbed(r.Context(), kinopoiskID, 0)
	}
	if !ok || len(embed.PL) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	log.Debug().Bool("movie", embed.Movie).Int("pl", len(embed.PL)).Int("s", s).Str("quality", embed.Quality).Msg("zetflix: index embed result")

	if embed.Movie {
		z.writeMovie(w, r, rjson, title, originalTitle, embed, links)
		return
	}

	if s == -1 {
		id, _ := strconv.ParseInt(strings.TrimSpace(q.Get("id")), 10, 64)
		if id <= 0 {
			id, _ = strconv.ParseInt(strings.TrimSpace(q.Get("tmdb_id")), 10, 64)
		}
		seasonCount := max(z.numberOfSeasons(r.Context(), id), 1)
		if serial && embed.SeasonCount > seasonCount {
			seasonCount = embed.SeasonCount
		}
		log.Debug().Int("seasonCount", seasonCount).Int64("tmdb_id", id).Msg("zetflix: writeSeason")
		z.writeSeason(w, r, rjson, kinopoiskID, title, originalTitle, seasonCount, embed.Quality)
		return
	}

	z.writeEpisode(w, r, rjson, title, originalTitle, s, voice, embed, links)
}

func (z *zetflixChecker) writeMovie(w http.ResponseWriter, r *http.Request, rjson bool, title, originalTitle string, embed zetflixEmbed, links *proxylink.Manager) {
	rows := make([]map[string]any, 0, len(embed.PL))
	labels := make([]string, 0, len(embed.PL))
	baseTitle := getsTVJoinName(title, originalTitle)

	for _, item := range embed.PL {
		name := strings.TrimSpace(item.Title)
		if name == "" {
			continue
		}
		streams := z.buildStreamQuality(item.File)
		isObrut := z.proxyStreams(r, streams, links, embed.ObrutCookies)
		if len(streams) == 0 {
			continue
		}
		method := "play"
		if isObrut {
			method = "call"
		}
		row := map[string]any{
			"method":        method,
			"url":           streams[0]["url"],
			"stream":        streams[0]["url"],
			"name":          name,
			"title":         fmt.Sprintf("%s (%s)", baseTitle, name),
			"streamquality": streams,
		}
		// For non-obrut streams, add quality map directly.
		// For obrut, manifest endpoint returns quality map via method:"call".
		if !isObrut && len(streams) > 1 {
			qualMap := zetflixBuildQualityMap(streams)
			row["quality"] = qualMap
			row["qualitys"] = qualMap
		}
		rows = append(rows, row)
		labels = append(labels, name)
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
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, 0, 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (z *zetflixChecker) writeSeason(
	w http.ResponseWriter,
	r *http.Request,
	rjson bool,
	kinopoiskID int64,
	title string,
	originalTitle string,
	seasons int,
	quality string,
) {
	host := hostFromRequest(r)
	rows := make([]map[string]any, 0, seasons)
	labels := make([]string, 0, seasons)

	for i := 1; i <= seasons; i++ {
		link := host + "/lite/zetflix?rjson=" + getsTVBool(rjson) +
			"&kinopoisk_id=" + strconv.FormatInt(kinopoiskID, 10) +
			"&title=" + url.QueryEscape(title) +
			"&original_title=" + url.QueryEscape(originalTitle) +
			"&s=" + strconv.Itoa(i)

		name := strconv.Itoa(i) + " сезон"
		rows = append(rows, map[string]any{
			"method":  "link",
			"id":      i,
			"url":     link,
			"name":    name,
			"quality": quality,
		})
		labels = append(labels, name)
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
		getsTVAppendSeasonHTML(&sb, row, labels[i], i == 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (z *zetflixChecker) writeEpisode(
	w http.ResponseWriter,
	r *http.Request,
	rjson bool,
	title string,
	originalTitle string,
	season int,
	voice string,
	embed zetflixEmbed,
	links *proxylink.Manager,
) {
	host := hostFromRequest(r)

	voiceRows := make([]map[string]any, 0, len(embed.PL))
	seenVoice := make(map[string]struct{}, len(embed.PL))

	type block struct {
		Voice  string
		Folder []zetflixFolder
	}
	blocks := make([]block, 0, len(embed.PL))

	// First pass: collect blocks and find the voice with most episodes for default.
	bestVoiceEpCount := 0
	for i := len(embed.PL) - 1; i >= 0; i-- {
		item := embed.PL[i]
		v := strings.TrimSpace(item.Title)
		if v == "" || len(item.Folder) == 0 {
			continue
		}
		blocks = append(blocks, block{Voice: v, Folder: item.Folder})
		if voice == "" && len(item.Folder) > bestVoiceEpCount {
			bestVoiceEpCount = len(item.Folder)
			voice = v
		}
	}

	// Second pass: build voice rows (after default voice is determined).
	for _, blk := range blocks {
		v := blk.Voice
		if _, ok := seenVoice[v]; !ok {
			seenVoice[v] = struct{}{}
			voiceRows = append(voiceRows, map[string]any{
				"method": "link",
				"name":   v,
				"active": v == voice,
				"url": host + "/lite/zetflix?rjson=" + getsTVBool(rjson) +
					"&kinopoisk_id=" + strings.TrimSpace(r.URL.Query().Get("kinopoisk_id")) +
					"&title=" + url.QueryEscape(title) +
					"&original_title=" + url.QueryEscape(originalTitle) +
					"&s=" + strconv.Itoa(season) +
					"&t=" + url.QueryEscape(v),
			})
		}
	}

	type episodeRow struct {
		data      map[string]any
		label     string
		seasonNum int
		episode   int
	}
	rows := make([]episodeRow, 0, 32)
	baseTitle := getsTVJoinName(title, originalTitle)

	for _, blk := range blocks {
		if blk.Voice != voice {
			continue
		}
		for _, ep := range blk.Folder {
			name := strings.TrimSpace(ep.Comment)
			if name == "" {
				continue
			}
			streams := z.buildStreamQuality(ep.File)
			isObrut := z.proxyStreams(r, streams, links, embed.ObrutCookies)
			if len(streams) == 0 {
				continue
			}
			method := "play"
			if isObrut {
				method = "call"
			}
			epNum, _ := cdnmoviesNumber(zetflixEpisodeNumRe, name)
			epData := map[string]any{
				"method":        method,
				"url":           streams[0]["url"],
				"stream":        streams[0]["url"],
				"s":             season,
				"e":             epNum,
				"name":          name,
				"title":         fmt.Sprintf("%s (%s)", baseTitle, name),
				"streamquality": streams,
			}
			if !isObrut && len(streams) > 1 {
				qualMap := zetflixBuildQualityMap(streams)
				epData["quality"] = qualMap
				epData["qualitys"] = qualMap
			}
			rows = append(rows, episodeRow{
				data:      epData,
				label:     name,
				seasonNum: season,
				episode:   epNum,
			})
		}
	}

	if len(rows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].episode == rows[j].episode {
			return rows[i].label < rows[j].label
		}
		return rows[i].episode < rows[j].episode
	})

	data := make([]map[string]any, 0, len(rows))
	labels := make([]string, 0, len(rows))
	seasons := make([]int, 0, len(rows))
	episodes := make([]int, 0, len(rows))
	for _, row := range rows {
		data = append(data, row.data)
		labels = append(labels, row.label)
		seasons = append(seasons, row.seasonNum)
		episodes = append(episodes, row.episode)
	}

	if rjson {
		payload := map[string]any{
			"type": "episode",
			"data": data,
		}
		if len(voiceRows) > 0 {
			payload["voice"] = voiceRows
		}
		writeJSON(w, http.StatusOK, payload)
		return
	}

	var sb strings.Builder
	if len(voiceRows) > 0 {
		sb.WriteString(`<div class="videos__line">`)
		for _, row := range voiceRows {
			getsTVAppendVoiceHTML(&sb, row)
		}
		sb.WriteString(`</div>`)
	}
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range data {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, seasons[i], episodes[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (z *zetflixChecker) fetchEmbed(ctx context.Context, kinopoiskID int64, season int) (zetflixEmbed, bool) {
	apiHost, ok := z.resolveAPIHost(ctx)
	if !ok || apiHost == "" {
		apiHost = strings.TrimRight(z.host, "/")
	}
	if apiHost == "" || kinopoiskID <= 0 {
		return zetflixEmbed{}, false
	}

	// Primary path: direct anti-bot bypass on videodb.php (matches nextgen
	// Zetflix flow). player.php and obrut.show wrappers went 404/403 in May
	// 2026 — the anti-bot videodb.php endpoint is the only one that survives.
	if fileData, ok := zetflixFetchEmbedBrowserWithHTML(ctx, apiHost, kinopoiskID, season, ""); ok && fileData != "" {
		if embed, pok := z.parsePlayerHTML(fileData); pok && len(embed.PL) > 0 {
			if season <= 0 || !embed.Movie {
				return embed, true
			}
			log.Debug().Int64("kp", kinopoiskID).Int("season", season).Msg("zetflix: serial requested, videodb returned flat movie; trying legacy fallback")
		}
	}

	// Legacy fallback 1: player.php → fotpro CDN. May return 404 today; kept
	// in case the wrapper revives.
	if embed, ok := z.fetchViaPlayerPHP(ctx, apiHost, kinopoiskID, season); ok {
		if season <= 0 || !embed.Movie {
			return embed, true
		}
	}

	// Legacy fallback 2: direct obrut.show embed.
	{
		encodedID := zetflixEncodeObrutKPID(kinopoiskID)
		obrutURL := "https://" + zetflixObrutHost + "/embed/AO/kinopoisk/" + encodedID + "/"
		if embed, ok := z.fetchViaObrut(ctx, apiHost, obrutURL, season); ok {
			if season <= 0 || !embed.Movie {
				return embed, true
			}
		}
	}

	return zetflixEmbed{}, false
}

// parsePlayerHTML processes raw HTML or file data obtained from the embed page.
func (z *zetflixChecker) parsePlayerHTML(fileData string) (zetflixEmbed, bool) {
	log.Debug().
		Int("len", len(fileData)).
		Bool("hasPlayerConfigs", strings.Contains(fileData, "playerConfigs")).
		Bool("hasFile", strings.Contains(fileData, "file:") || strings.Contains(fileData, `file"`)).
		Bool("hasHTML", strings.Contains(fileData, "<html")).
		Msg("zetflix: parsePlayerHTML")

	// HDVBPlayer format: HTML with playerConfigs/p2aCon and encrypted file field (starts with ~).
	if strings.Contains(fileData, "playerConfigs") || strings.Contains(fileData, "p2aCon") {
		if pc, ok := ZetflixExtractPlayerConfig(fileData); ok && strings.HasPrefix(pc.File, "~") {
			log.Info().Str("href", pc.Href).Str("file", pc.File[:min(len(pc.File), 40)]).Msg("zetflix: found playerConfigs with encrypted file")
			resolved, rok := z.fetchPlaylistURL(context.Background(), pc)
			if rok && resolved != "" {
				log.Info().Str("url", resolved[:min(len(resolved), 120)]).Msg("zetflix: resolved playlist URL")
				return z.buildEmbedFromStreamURL(resolved, "")
			}
			log.Warn().Msg("zetflix: failed to resolve playlist URL from playerConfigs")
		}
	}

	// fileData can be:
	// 1. JSON string (from Playerjs extraction) — parse as player file
	// 2. Rendered HTML containing "file:" — parse with parseEmbed
	if strings.Contains(fileData, "file:") || strings.Contains(fileData, "file\"") || strings.Contains(fileData, "<html") {
		embed, ok := z.parseEmbed(fileData)
		log.Debug().Bool("ok", ok).Int("pl", len(embed.PL)).Msg("zetflix: parseEmbed result")
		return embed, ok
	}

	// Try parsing as direct file string or JSON array
	embed, ok := z.parseFileData(fileData)
	log.Debug().Bool("ok", ok).Msg("zetflix: parseFileData result")
	return embed, ok
}

// fetchViaPlayerPHP resolves video through the zetflix player.php wrapper.
//
// Flow:
//  1. GET /iplayer/player.php?id=<base64(urlencode(/iplayer/videodb.php?kp=<id>))>
//  2. Parse data-fallback-src from the returned iframe — this is the CDN embed URL
//     (e.g. https://vid*.fotpro135alto.com/movie/<hash>/iframe?d=...)
//  3. GET the CDN embed URL — returns HTML with HDVBPlayer playerConfigs
//  4. Extract playerConfigs and resolve stream URL via playlist API
func (z *zetflixChecker) fetchViaPlayerPHP(ctx context.Context, apiHost string, kinopoiskID int64, season int) (zetflixEmbed, bool) {
	// Build the inner path that player.php expects as a base64-encoded id param.
	innerPath := "/iplayer/videodb.php?kp=" + strconv.FormatInt(kinopoiskID, 10)
	if season > 0 {
		innerPath += "&season=" + strconv.Itoa(season)
	}
	encodedPath := url.QueryEscape(innerPath)
	idParam := base64.StdEncoding.EncodeToString([]byte(encodedPath))

	playerURL := strings.TrimRight(apiHost, "/") + "/iplayer/player.php?id=" + idParam

	log.Debug().Str("url", playerURL).Msg("zetflix: fetching player.php")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, playerURL, nil)
	if err != nil {
		return zetflixEmbed{}, false
	}
	req.Header.Set("User-Agent", zetflixUserAgent)
	req.Header.Set("Referer", strings.TrimRight(apiHost, "/")+"/")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Sec-Fetch-Dest", "iframe")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "same-origin")

	resp, err := z.client.Do(req)
	if err != nil {
		log.Warn().Err(err).Msg("zetflix: player.php request failed")
		return zetflixEmbed{}, false
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil || resp.StatusCode != http.StatusOK {
		return zetflixEmbed{}, false
	}
	html := string(body)

	// Try obrut.show iframe first (same as working version).
	obrutSrc := submatch1(zetflixObrutIframeSrcRe, html)
	if obrutSrc != "" {
		if embed, ok := z.fetchViaObrut(ctx, apiHost, obrutSrc, season); ok {
			return embed, true
		}
	}

	// Extract data-fallback-src — fotpro CDN (works for films).
	fallback := submatch1(zetflixFallbackSrcRe, html)
	if fallback == "" {
		log.Debug().Msg("zetflix: player.php returned no fallback-src")
		return zetflixEmbed{}, false
	}
	if !strings.HasPrefix(fallback, "http") {
		log.Debug().Str("fallback", fallback).Msg("zetflix: fallback is relative (not on CDN)")
		return zetflixEmbed{}, false
	}

	log.Info().Str("fallback", fallback[:min(len(fallback), 120)]).Msg("zetflix: got CDN embed URL from player.php")

	// Fetch the CDN embed page (fotpro) — contains HDVBPlayer playerConfigs.
	req2, err := http.NewRequestWithContext(ctx, http.MethodGet, fallback, nil)
	if err != nil {
		return zetflixEmbed{}, false
	}
	req2.Header.Set("User-Agent", zetflixUserAgent)
	req2.Header.Set("Referer", strings.TrimRight(apiHost, "/")+"/")

	resp2, err := z.client.Do(req2)
	if err != nil {
		log.Warn().Err(err).Msg("zetflix: CDN embed request failed")
		return zetflixEmbed{}, false
	}
	defer resp2.Body.Close()

	body2, err := io.ReadAll(io.LimitReader(resp2.Body, 1<<20))
	if err != nil || resp2.StatusCode != http.StatusOK {
		return zetflixEmbed{}, false
	}
	cdnHTML := string(body2)

	log.Debug().Int("len", len(cdnHTML)).Msg("zetflix: fetched CDN embed page")

	return z.parsePlayerHTML(cdnHTML)
}

// fetchViaObrut resolves video through the obrut.show embed player.
// Handles both movies (flat: voice → file) and serials (3-level: season → episode → voice).
//
// Flow:
//  1. GET obrut.show embed URL → HTML with base64-encoded player JSON
//  2. Extract player JSON from new Player("...") initialization
//  3. Detect movie vs serial structure
//  4. For movies: parse quality URLs from cdn-*.obrut.show
//  5. For serials: filter requested season, invert tree to (voice → episodes)
//
// CDN URLs (cdn-*.obrut.show) are returned as-is. The /proxy/ handler will follow
// 302 redirect to superdupercdn server-side when the client plays the stream.
func (z *zetflixChecker) fetchViaObrut(ctx context.Context, apiHost, obrutURL string, season int) (zetflixEmbed, bool) {
	log.Debug().Str("url", obrutURL[:min(len(obrutURL), 120)]).Int("season", season).Msg("zetflix: fetching obrut.show embed")

	// Create HTTP client with cookie jar — cdn-*.obrut.show requires session
	// cookies (turbo_player_session, XSRF-TOKEN, etc.) to return the 302 redirect.
	jar, err := cookiejar.New(nil)
	if err != nil {
		return zetflixEmbed{}, false
	}
	transport := httpclient.TransportForBalancer("zetflix")
	if transport == nil {
		transport = httpclient.SharedTransport
	}
	obrutClient := &http.Client{
		Transport: transport,
		Jar:       jar,
		Timeout:   14 * time.Second,
	}

	referer := strings.TrimRight(apiHost, "/") + "/"

	// Step 1: GET the obrut embed page to get cookies + player data.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, obrutURL, nil)
	if err != nil {
		return zetflixEmbed{}, false
	}
	req.Header.Set("User-Agent", zetflixUserAgent)
	req.Header.Set("Referer", referer)

	resp, err := obrutClient.Do(req)
	if err != nil {
		log.Warn().Err(err).Msg("zetflix: obrut embed request failed")
		return zetflixEmbed{}, false
	}
	defer resp.Body.Close()

	// For serials, obrut can return 1.5MB+ of data — increase limit.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil || resp.StatusCode != http.StatusOK {
		return zetflixEmbed{}, false
	}
	embedHTML := string(body)

	log.Debug().Int("len", len(embedHTML)).Msg("zetflix: fetched obrut embed page")

	// Extract session cookies set by obrut — needed for CDN 302 redirect.
	var cookieParts []string
	if u, err := url.Parse(obrutURL); err == nil {
		for _, c := range jar.Cookies(u) {
			cookieParts = append(cookieParts, c.Name+"="+c.Value)
		}
	}
	cookies := strings.Join(cookieParts, "; ")
	if cookies != "" {
		log.Debug().Int("count", len(cookieParts)).Msg("zetflix: obrut session cookies collected")
		// Cache cookies for manifest() endpoint — CDN URLs need them for 302 redirect.
		z.obrutCookieCache.Store(zetflixObrutHost, obrutCookieEntry{
			cookies: cookies,
			expires: time.Now().Add(20 * time.Minute),
		})
	}

	// Step 2: Decode base64 player data and detect structure.
	text := ZetflixDecodeObrutBase64(embedHTML)
	if text == "" {
		log.Debug().Msg("zetflix: no data in obrut player")
		return zetflixEmbed{}, false
	}
	text = strings.ReplaceAll(text, `\/`, `/`)

	// Check if it's a serial (season/episode titles) or a flat movie.
	// Some responses are already season-scoped and contain only episodes.
	isSerial := ZetflixObrutIsSerial(text)
	if !isSerial && season > 0 && obrutEpisodeTitleRe.MatchString(text) {
		isSerial = true
	}
	log.Info().Bool("serial", isSerial).Int("textLen", len(text)).Msg("zetflix: parsed obrut player data")

	var embed zetflixEmbed
	var ok bool
	if !isSerial {
		embed, ok = z.buildObrutMovieEmbed(embedHTML)
	} else {
		embed, ok = z.buildObrutSerialEmbed(text, season)
	}
	if !ok {
		// Check cache — CDN might have returned incomplete data this time.
		cacheKey := obrutURL + ":" + strconv.Itoa(season)
		if cached, found := z.obrutEmbedCache.Load(cacheKey); found {
			entry := cached.(obrutEmbedCacheEntry)
			if time.Now().Before(entry.expires) {
				log.Info().Int("cached_eps", entry.episodes).Msg("zetflix: obrut using cached embed (new result empty)")
				entry.embed.ObrutCookies = cookies
				return entry.embed, true
			}
		}
		return embed, false
	}
	embed.ObrutCookies = cookies

	// Cache the result. Only update if new result has more episodes (better CDN node).
	cacheKey := obrutURL + ":" + strconv.Itoa(season)
	newEpCount := 0
	if !embed.Movie {
		// For serials, count total episodes across all voices.
		// This better captures CDN completeness than just the first voice.
		for _, r := range embed.PL {
			newEpCount += len(r.Folder)
		}
	} else {
		newEpCount = len(embed.PL)
	}
	shouldCache := true
	if cached, found := z.obrutEmbedCache.Load(cacheKey); found {
		entry := cached.(obrutEmbedCacheEntry)
		if time.Now().Before(entry.expires) && entry.episodes > newEpCount {
			// Cached result has more episodes — use it instead.
			log.Info().Int("cached_eps", entry.episodes).Int("new_eps", newEpCount).Msg("zetflix: obrut using cached embed (more episodes)")
			entry.embed.ObrutCookies = cookies
			return entry.embed, true
		}
		shouldCache = entry.episodes <= newEpCount
	}
	if shouldCache {
		z.startJanitor()
		z.obrutEmbedCache.Store(cacheKey, obrutEmbedCacheEntry{
			embed:    embed,
			episodes: newEpCount,
			expires:  time.Now().Add(30 * time.Minute),
		})
	}

	return embed, true
}

// buildObrutMovieEmbed handles flat movie structure from obrut (voice → file).
// CDN URLs (cdn-*.obrut.show) are kept as-is — the /proxy/ handler will follow
// the 302 redirect to superdupercdn server-side (because the Location contains .m3u8).
func (z *zetflixChecker) buildObrutMovieEmbed(embedHTML string) (zetflixEmbed, bool) {
	voices, cdnURLs := zetflixParseObrutPlayerData(embedHTML)
	if len(cdnURLs) == 0 {
		log.Debug().Msg("zetflix: no CDN URLs found in obrut movie data")
		return zetflixEmbed{}, false
	}

	type voiceURLs struct {
		title string
		items []obrutCDNURL
	}
	voiceMap := make(map[string]*voiceURLs)

	for _, cu := range cdnURLs {
		vu, ok := voiceMap[cu.voice]
		if !ok {
			vu = &voiceURLs{title: cu.voice}
			voiceMap[cu.voice] = vu
		}
		vu.items = append(vu.items, cu)
	}

	var roots []zetflixRoot
	bestQuality := ""
	for _, voice := range voices {
		vu, ok := voiceMap[voice]
		if !ok || len(vu.items) == 0 {
			continue
		}
		var parts []string
		for _, item := range vu.items {
			parts = append(parts, "["+item.quality+"]"+item.url)
			if bestQuality == "" || zetflixQualityRank(item.quality) > zetflixQualityRank(bestQuality) {
				bestQuality = item.quality
			}
		}
		roots = append(roots, zetflixRoot{
			Title: ZetflixCleanVoiceName(voice),
			File:  strings.Join(parts, ","),
		})
	}

	if len(roots) == 0 {
		return zetflixEmbed{}, false
	}
	log.Info().Int("voices", len(roots)).Str("quality", bestQuality).Msg("zetflix: obrut movie resolved")
	return zetflixEmbed{PL: roots, Movie: true, Quality: bestQuality}, true
}

// obrutEpData holds a single episode's comment and file string for voice→episode inversion.
type obrutEpData struct {
	comment string
	file    string
}

// obrutNode is a recursive JSON structure used by obrut.show player data.
// Matches videodb's approach: Season → Episode → Voice, with nested "folder" arrays.
type obrutNode struct {
	Title  string      `json:"title"`
	File   string      `json:"file"`
	Folder []obrutNode `json:"folder"`
}

// buildObrutSerialEmbed handles 3-level serial structure from obrut.
// Obrut format: Season N → Episode N → Voice (with "file":"[q]url,...").
// We invert into: Voice → Episodes (with Comment + File), which is what
// writeEpisode expects in zetflixEmbed.PL.
//
// Primary: JSON parsing (like videodb) — reliable, gets all episodes.
// Fallback: regex parsing — for payloads with binary noise.
func (z *zetflixChecker) buildObrutSerialEmbed(
	text string,
	season int,
) (zetflixEmbed, bool) {
	seasonCount := zetflixEstimateObrutSeasonCount(text)

	// Try JSON parsing first (like videodb).
	if embed, ok := z.buildObrutSerialJSON(text, season, seasonCount); ok {
		return embed, true
	}

	// Fallback: regex-based parsing.
	log.Debug().Msg("zetflix: obrut JSON parse failed, falling back to regex")
	return z.buildObrutSerialRegex(text, season, seasonCount)
}

// buildObrutSerialJSON tries to extract and parse the "file" JSON array from decoded text.
// Returns parsed serial embed if successful.
func (z *zetflixChecker) buildObrutSerialJSON(
	text string,
	season int,
	seasonCount int,
) (zetflixEmbed, bool) {
	fileJSON := zetflixExtractFileJSON(text)
	if fileJSON == "" {
		return zetflixEmbed{}, false
	}

	// Sanitize: base64 decode can produce control characters (\x05, \v, etc.)
	// that break json.Unmarshal. Strip all bytes < 0x20 except tab/newline/CR.
	fileJSON = zetflixSanitizeJSON(fileJSON)

	var nodes []obrutNode
	if err := stdjson.Unmarshal([]byte(fileJSON), &nodes); err != nil {
		log.Debug().Err(err).Msg("zetflix: obrut file JSON unmarshal failed")
		return zetflixEmbed{}, false
	}
	if len(nodes) == 0 {
		return zetflixEmbed{}, false
	}

	// Find season node. Obrut structure: nodes = seasons, each with folder = episodes.
	var seasonNode *obrutNode
	for i := range nodes {
		n := zetflixEpisodeNumFromTitle(nodes[i].Title)
		if n == season {
			seasonNode = &nodes[i]
			break
		}
	}
	// Positional fallback: if season not found by number, use index.
	if seasonNode == nil && season > 0 && season <= len(nodes) {
		seasonNode = &nodes[season-1]
	}
	// Single-season content: if text has episode markers but no season structure.
	if seasonNode == nil && len(nodes) > 0 && len(nodes[0].Folder) > 0 {
		// Check if nodes themselves are episodes (no season wrapper).
		if hasVoiceFolders(nodes[0]) {
			seasonNode = &obrutNode{Folder: nodes}
		}
	}
	if seasonNode == nil || len(seasonNode.Folder) == 0 {
		return zetflixEmbed{}, false
	}

	// Invert: Episode → Voice → file  ⟹  Voice → []Episode{Comment, File}
	voiceOrder := make([]string, 0, 16)
	voiceEpisodes := make(map[string][]obrutEpData)

	for epIdx, epNode := range seasonNode.Folder {
		epNum := zetflixEpisodeNumFromTitle(epNode.Title)
		if epNum == 0 {
			epNum = epIdx + 1
		}
		comment := strconv.Itoa(epNum) + " серия"

		for _, voiceNode := range epNode.Folder {
			voiceName := strings.TrimSpace(voiceNode.Title)
			if voiceName == "" || voiceNode.File == "" {
				continue
			}
			if _, ok := voiceEpisodes[voiceName]; !ok {
				voiceOrder = append(voiceOrder, voiceName)
			}
			voiceEpisodes[voiceName] = append(voiceEpisodes[voiceName], obrutEpData{
				comment: comment,
				file:    voiceNode.File,
			})
		}
	}
	if len(voiceOrder) == 0 {
		return zetflixEmbed{}, false
	}

	roots, bestQuality := zetflixBuildObrutRoots(voiceOrder, voiceEpisodes)
	if len(roots) == 0 {
		return zetflixEmbed{}, false
	}

	log.Info().Int("voices", len(roots)).Str("quality", bestQuality).Int("season", season).
		Int("episodes", len(seasonNode.Folder)).Msg("zetflix: obrut serial resolved (JSON)")

	return zetflixEmbed{
		PL:          roots,
		Movie:       false,
		Quality:     bestQuality,
		SeasonCount: seasonCount,
	}, true
}

// hasVoiceFolders checks if a node's sub-folders contain voice-level data (File fields).
func hasVoiceFolders(node obrutNode) bool {
	for _, child := range node.Folder {
		for _, voice := range child.Folder {
			if voice.File != "" {
				return true
			}
		}
	}
	return false
}

// zetflixExtractFileJSON extracts the "file":[...] JSON array from obrut decoded text.
// Uses bracket matching to find the complete array, handling nested objects/arrays.
// zetflixSanitizeJSON strips all control characters (< 0x20) that break json.Unmarshal.
// Base64 decode can produce noise bytes (\x05, \v, \t, etc.) inside JSON string values
// where they are invalid (must be escaped as \uXXXX). Since obrut JSON doesn't use
// meaningful whitespace, stripping all control chars is safe.
func zetflixSanitizeJSON(s string) string {
	clean := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x20 {
			clean = append(clean, s[i])
		}
	}
	return string(clean)
}

var obrutTopFileRe = regexp.MustCompile(`"file"\s*:\s*\[`)

func zetflixExtractFileJSON(text string) string {
	// Find "file":[ at the top level (the main content array).
	// Use regex to handle whitespace variations.
	loc := obrutTopFileRe.FindStringIndex(text)
	if loc == nil {
		return ""
	}
	// Find the opening bracket within the match.
	arrStart := strings.IndexByte(text[loc[0]:loc[1]], '[')
	if arrStart < 0 {
		return ""
	}
	arrStart += loc[0]

	// Bracket-match to find the closing bracket.
	depth := 0
	inStr := false
	escaped := false
	for i := arrStart; i < len(text); i++ {
		if escaped {
			escaped = false
			continue
		}
		c := text[i]
		if c == '\\' && inStr {
			escaped = true
			continue
		}
		if c == '"' {
			inStr = !inStr
			continue
		}
		if inStr {
			continue
		}
		if c == '[' || c == '{' {
			depth++
		} else if c == ']' || c == '}' {
			depth--
			if depth == 0 {
				return text[arrStart : i+1]
			}
		}
	}
	return ""
}

// buildObrutSerialRegex is the regex-based fallback for obrut serial parsing.
func (z *zetflixChecker) buildObrutSerialRegex(
	text string,
	season int,
	seasonCount int,
) (zetflixEmbed, bool) {
	episodes := zetflixParseObrutSerialEpisodes(text, season)
	if len(episodes) == 0 {
		log.Debug().Int("season", season).Msg("zetflix: obrut serial no episodes found for season")
		return zetflixEmbed{}, false
	}

	log.Info().Int("season", season).Int("episodes", len(episodes)).Msg("zetflix: obrut serial season found (regex)")

	// Invert: Episode → Voice → file  ⟹  Voice → []Episode{Comment, File}
	voiceOrder := make([]string, 0, 8)
	voiceEpisodes := make(map[string][]obrutEpData)

	for _, ep := range episodes {
		epNum := zetflixEpisodeNumFromTitle(ep.title)
		comment := ep.title
		if epNum > 0 {
			comment = strconv.Itoa(epNum) + " серия"
		}

		for _, v := range ep.voices {
			voiceName := strings.TrimSpace(v.voice)
			if voiceName == "" {
				continue
			}
			if _, ok := voiceEpisodes[voiceName]; !ok {
				voiceOrder = append(voiceOrder, voiceName)
			}
			voiceEpisodes[voiceName] = append(voiceEpisodes[voiceName], obrutEpData{
				comment: comment,
				file:    v.file,
			})
		}
	}

	if len(voiceOrder) == 0 {
		log.Debug().Msg("zetflix: obrut serial has no voices")
		return zetflixEmbed{}, false
	}

	roots, bestQuality := zetflixBuildObrutRoots(voiceOrder, voiceEpisodes)
	if len(roots) == 0 {
		log.Warn().Msg("zetflix: obrut serial resolved no streams")
		return zetflixEmbed{}, false
	}

	log.Info().Int("voices", len(roots)).Str("quality", bestQuality).Int("season", season).Msg("zetflix: obrut serial resolved (regex)")

	return zetflixEmbed{
		PL:          roots,
		Movie:       false,
		Quality:     bestQuality,
		SeasonCount: seasonCount,
	}, true
}

// zetflixBuildObrutRoots converts voice→episodes map into zetflixRoot slice.
// Shared by both JSON and regex parsers.
func zetflixBuildObrutRoots(voiceOrder []string, voiceEpisodes map[string][]obrutEpData) ([]zetflixRoot, string) {
	var roots []zetflixRoot
	bestQuality := ""

	for _, voiceName := range voiceOrder {
		eps := voiceEpisodes[voiceName]
		var folders []zetflixFolder

		for _, ep := range eps {
			matches := zetflixObritQualityRe.FindAllStringSubmatch(ep.file, -1)
			var parts []string
			for _, m := range matches {
				if len(m) < 3 {
					continue
				}
				q := m[1] + "p"
				cdnURL := m[2]
				if _, err := url.Parse(cdnURL); err != nil {
					continue
				}
				parts = append(parts, "["+q+"]"+cdnURL)
				if bestQuality == "" || zetflixQualityRank(q) > zetflixQualityRank(bestQuality) {
					bestQuality = q
				}
			}
			if len(parts) == 0 {
				continue
			}
			folders = append(folders, zetflixFolder{
				Comment: ep.comment,
				File:    strings.Join(parts, ","),
			})
		}

		if len(folders) > 0 {
			roots = append(roots, zetflixRoot{
				Title:  ZetflixCleanVoiceName(voiceName),
				Folder: folders,
			})
		}
	}
	return roots, bestQuality
}

// zetflixEpisodeNumFromTitle extracts episode number from obrut title like "Episode 1".
func zetflixEpisodeNumFromTitle(title string) int {
	m := zetflixEpisodeNumRe.FindStringSubmatch(title)
	if len(m) >= 2 {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	// Try extracting number from end: "Episode 1"
	parts := strings.Fields(title)
	for i := len(parts) - 1; i >= 0; i-- {
		if n, err := strconv.Atoi(parts[i]); err == nil {
			return n
		}
	}
	return 0
}

type obrutCDNURL struct {
	voice   string
	quality string
	url     string
}

// obrutVoiceFile holds a parsed voice → CDN file pair from obrut serial data.
type obrutVoiceFile struct {
	voice string
	file  string // "[240p]url,[360p]url,..."
}

// obrutEpisode holds parsed episode data from obrut serial data.
type obrutEpisode struct {
	title  string           // "Episode 1"
	voices []obrutVoiceFile // voices with CDN URLs
}

// ZetflixDecodeObrutBase64 extracts and decodes the base64-encoded data from obrut HTML.
// Obrut inserts '=' signs in the middle of the base64 stream as obfuscation;
// we strip them all and re-pad properly.
func ZetflixDecodeObrutBase64(html string) string {
	idx := strings.Index(html, `new Player("`)
	if idx < 0 {
		return ""
	}
	start := idx + len(`new Player("`)
	end := strings.Index(html[start:], `"`)
	if end < 0 {
		return ""
	}
	raw := html[start : start+end]

	b64Idx := strings.Index(raw, "eyJs")
	if b64Idx < 0 {
		return ""
	}
	b64Part := raw[b64Idx:]
	// Remove obfuscation markers like //...= that videodb also strips.
	// These contain valid base64 chars that decode to garbage if kept.
	b64Part = zetflixObrutGarbageRe.ReplaceAllString(b64Part, "")
	// Strip everything that is not valid base64 alphabet.
	// Also handle URL-safe base64 (- → +, _ → /).
	var clean strings.Builder
	clean.Grow(len(b64Part))
	for i := 0; i < len(b64Part); i++ {
		c := b64Part[i]
		switch {
		case (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '+' || c == '/':
			clean.WriteByte(c)
		case c == '-':
			clean.WriteByte('+')
		case c == '_':
			clean.WriteByte('/')
		}
	}
	b64Part = clean.String()
	if m := len(b64Part) % 4; m != 0 {
		b64Part += strings.Repeat("=", 4-m)
	}

	decoded, err := base64.StdEncoding.DecodeString(b64Part)
	if err != nil {
		// Truncate to last valid 4-byte boundary and retry — obrut sometimes
		// appends garbage at the end of the base64 stream.
		trimmed := strings.TrimRight(b64Part, "=")
		trimmed = trimmed[:len(trimmed)-(len(trimmed)%4)]
		if len(trimmed) > 0 {
			decoded, err = base64.RawStdEncoding.DecodeString(trimmed)
		}
	}
	if err != nil {
		log.Debug().Err(err).Int("b64len", len(b64Part)).Msg("zetflix: obrut base64 decode failed")
		return ""
	}
	// Unescape \uXXXX sequences — obrut encodes Cyrillic as JSON Unicode escapes.
	// Without this, regexes for "Сезон"/"Серия"/"Эпизод" won't match, causing
	// missing seasons/episodes, and voice titles display as raw \uXXXX.
	return zetflixUnescapeUnicode(string(decoded))
}

// ZetflixCleanVoiceName strips obrut-style prefixes like "(RU) MVO | " from voice names.
// Obrut returns names like "(RU) MVO | LE-Production" — we keep only the studio name after "|".
func ZetflixCleanVoiceName(name string) string {
	if _, after, ok := strings.Cut(name, "|"); ok {
		cleaned := strings.TrimSpace(after)
		if cleaned != "" {
			return cleaned
		}
	}
	return strings.TrimSpace(name)
}

// zetflixUnescapeUnicode replaces \uXXXX sequences in s with actual UTF-8 runes.
// Obrut base64-encoded player data uses JSON-style Unicode escapes for Cyrillic
// text (season titles, episode titles, voice names). Without unescaping, regexes
// looking for "Сезон" / "Серия" / "Эпизод" won't match, causing missing episodes.
var reUnicodeEscape = regexp.MustCompile(`\\u([0-9a-fA-F]{4})`)

func zetflixUnescapeUnicode(s string) string {
	if !strings.Contains(s, `\u`) {
		return s
	}
	return reUnicodeEscape.ReplaceAllStringFunc(s, func(match string) string {
		r, err := strconv.ParseUint(match[2:], 16, 32)
		if err != nil {
			return match
		}
		return string(rune(r))
	})
}

var (
	obrutSeasonTitleRe    = regexp.MustCompile(`"title"\s*:\s*"((?:Season|Сезон)\s*\d+)"`)
	ObrutSeasonNumRe      = regexp.MustCompile(`"title"\s*:\s*"(?:Season|Сезон)\s*(\d+)"`)
	obrutEpisodeTitleRe   = regexp.MustCompile(`"title"\s*:\s*"((?:Episode|Эпизод|Серия)\s*\d+)"`)
	ObrutVoiceTitleRe     = regexp.MustCompile(`"title"\s*:\s*"([^"]+)"`)
	obrutVoiceFileRe      = regexp.MustCompile(`"title"\s*:\s*"([^"]+)"[^}]*?"file"\s*:\s*"(\[[^"]+)"`)
	obrutFileRe           = regexp.MustCompile(`"file"\s*:\s*"(\[[^"]+)"`)
	zetflixObrutGarbageRe = regexp.MustCompile(`//[^=]+=`)
)

// zetflixParseObrutPlayerData extracts voice titles and CDN URLs from the obrut.show
// embed page for MOVIES (flat structure: voice → file).
func zetflixParseObrutPlayerData(html string) (voices []string, cdnURLs []obrutCDNURL) {
	text := ZetflixDecodeObrutBase64(html)
	if text == "" {
		return nil, nil
	}
	text = strings.ReplaceAll(text, `\/`, `/`)

	// Extract voice titles: "title":"..." patterns
	titleMatches := ObrutVoiceTitleRe.FindAllStringSubmatch(text, -1)
	seenVoices := make(map[string]bool)
	for _, m := range titleMatches {
		title := m[1]
		if !seenVoices[title] {
			voices = append(voices, title)
			seenVoices[title] = true
		}
	}

	// Split by file entries to associate URLs with voices.
	fileSections := strings.Split(text, `"title"`)
	for _, section := range fileSections[1:] {
		tm := ObrutVoiceTitleRe.FindStringSubmatch(`"title"` + section)
		if len(tm) < 2 {
			continue
		}
		voice := tm[1]

		fileIdx := strings.Index(section, `"file":"`)
		if fileIdx < 0 {
			continue
		}
		fileStart := fileIdx + len(`"file":"`)
		fileEnd := strings.Index(section[fileStart:], `"`)
		if fileEnd < 0 {
			continue
		}
		fileData := section[fileStart : fileStart+fileEnd]

		matches := zetflixObritQualityRe.FindAllStringSubmatch(fileData, -1)
		for _, m := range matches {
			if len(m) >= 3 {
				cdnURL := m[2]
				// Validate URL — base64 decode can produce binary noise.
				if _, err := url.Parse(cdnURL); err != nil {
					continue
				}
				cdnURLs = append(cdnURLs, obrutCDNURL{
					voice:   voice,
					quality: m[1] + "p",
					url:     cdnURL,
				})
			}
		}
	}
	return voices, cdnURLs
}

// ZetflixObrutIsSerial checks if the decoded obrut player data has a serial
// structure (contains "Season N" titles) vs. a flat movie structure.
func ZetflixObrutIsSerial(text string) bool {
	return obrutSeasonTitleRe.MatchString(text)
}

func zetflixEstimateObrutSeasonCount(text string) int {
	// Extract unique season numbers and return the highest one.
	// Obrut may have gaps (e.g. Season 1 and Season 3 but no Season 2),
	// so we return max number, not count of matches.
	matches := ObrutSeasonNumRe.FindAllStringSubmatch(text, -1)
	maxSeason := 0
	for _, m := range matches {
		if n, err := strconv.Atoi(m[1]); err == nil && n > maxSeason {
			maxSeason = n
		}
	}
	if maxSeason > 0 {
		return maxSeason
	}
	if obrutEpisodeTitleRe.MatchString(text) {
		return 1
	}
	return 0
}

// zetflixParseObrutSerialEpisodes parses the 3-level obrut serial structure.
// JSON parsing fails due to binary noise from base64 obfuscation, so we use
// a structural approach based on "folder":[ boundaries:
//
//	Season folder → Episode folders → Voice objects {title, file}
//
// Each "folder":[ at the episode level marks an episode boundary, regardless
// of whether "Episode N" titles are present. This is robust against inconsistent
// CDN responses where some episode markers are missing.
func zetflixParseObrutSerialEpisodes(text string, season int) []obrutEpisode {
	// Step 1: Extract season chunk.
	chunk := zetflixExtractSeasonChunk(text, season)
	if chunk == "" {
		return nil
	}

	// Step 2: Find episode boundaries using "folder":[ markers.
	// In the season chunk, each "folder":[ marks either:
	//   - An episode folder (contains voice objects with "file" fields)
	//   - A nested sub-folder (rare)
	// We split by "folder":[ and extract voice+file pairs from each segment.
	folderRe := regexp.MustCompile(`"folder"\s*:\s*\[`)
	folderLocs := folderRe.FindAllStringIndex(chunk, -1)

	if len(folderLocs) == 0 {
		return nil
	}

	// Step 3: For each folder segment, extract voice+file pairs using proximity.
	var episodes []obrutEpisode
	epTitleLocs := obrutEpisodeTitleRe.FindAllStringSubmatch(chunk, -1)

	for i, fl := range folderLocs {
		segStart := fl[1] // after "folder":[
		var segEnd int
		if i+1 < len(folderLocs) {
			segEnd = folderLocs[i+1][0]
		} else {
			segEnd = len(chunk)
		}
		seg := chunk[segStart:segEnd]

		// Find all title and file positions within this segment.
		titleLocs := ObrutVoiceTitleRe.FindAllStringSubmatchIndex(seg, -1)
		fileLocs := obrutFileRe.FindAllStringSubmatchIndex(seg, -1)
		if len(fileLocs) == 0 {
			continue
		}

		// Pair each file with closest preceding title.
		var voices []obrutVoiceFile
		for _, fileLoc := range fileLocs {
			fileStart := fileLoc[0]
			fileContent := seg[fileLoc[2]:fileLoc[3]]

			var bestTitle string
			bestDist := -1
			for _, tl := range titleLocs {
				titleEnd := tl[1]
				if titleEnd <= fileStart {
					dist := fileStart - titleEnd
					if bestDist < 0 || dist < bestDist {
						bestDist = dist
						bestTitle = seg[tl[2]:tl[3]]
					}
				}
			}
			if bestTitle == "" {
				continue
			}

			// Skip structural titles.
			lc := strings.ToLower(strings.TrimSpace(bestTitle))
			if strings.HasPrefix(lc, "season ") || strings.HasPrefix(lc, "episode ") ||
				strings.HasPrefix(lc, "сезон ") || strings.HasPrefix(lc, "эпизод ") ||
				strings.HasPrefix(lc, "серия ") {
				continue
			}

			voices = append(voices, obrutVoiceFile{
				voice: bestTitle,
				file:  fileContent,
			})
		}

		if len(voices) == 0 {
			continue
		}

		epIdx := len(episodes)
		title := fmt.Sprintf("Episode %d", epIdx+1)
		if epIdx < len(epTitleLocs) {
			title = epTitleLocs[epIdx][1]
		}

		episodes = append(episodes, obrutEpisode{
			title:  title,
			voices: voices,
		})
	}

	log.Info().Int("episodes", len(episodes)).Int("folders", len(folderLocs)).Int("ep_markers", len(epTitleLocs)).Msg("zetflix: obrut serial parsed (folder-based)")
	return episodes
}

// zetflixExtractSeasonChunk extracts the text portion for a specific season.
func zetflixExtractSeasonChunk(text string, season int) string {
	seasonMatches := ObrutSeasonNumRe.FindAllStringSubmatchIndex(text, -1)

	if len(seasonMatches) == 0 {
		// No season markers — the entire text is one season.
		if obrutEpisodeTitleRe.MatchString(text) || obrutFileRe.MatchString(text) {
			return text
		}
		return ""
	}

	// Find the match whose captured season number equals the requested season.
	foundIdx := -1
	for i, m := range seasonMatches {
		numStr := text[m[2]:m[3]]
		n, _ := strconv.Atoi(numStr)
		if n == season {
			foundIdx = i
			break
		}
	}
	if foundIdx < 0 {
		// Fallback: use positional index (season-1).
		seasonIdx := season - 1
		if seasonIdx >= 0 && seasonIdx < len(seasonMatches) {
			foundIdx = seasonIdx
		} else {
			return ""
		}
	}

	seasonStart := seasonMatches[foundIdx][1]
	var seasonEnd int
	if foundIdx+1 < len(seasonMatches) {
		seasonEnd = seasonMatches[foundIdx+1][0]
	} else {
		seasonEnd = len(text)
	}
	return text[seasonStart:seasonEnd]
}

func containsString(ss []string, s string) bool {
	return slices.Contains(ss, s)
}

func zetflixQualityRank(q string) int {
	switch q {
	case "2160p":
		return 5
	case "1080p":
		return 4
	case "720p":
		return 3
	case "480p":
		return 2
	case "360p":
		return 1
	case "240p":
		return 0
	default:
		return -1
	}
}

func (z *zetflixChecker) parseEmbed(html string) (zetflixEmbed, bool) {
	if strings.TrimSpace(html) == "" {
		return zetflixEmbed{}, false
	}

	quality := "480p"
	if strings.Contains(html, "1080p") {
		quality = "1080p"
	} else if strings.Contains(html, "720p") {
		quality = "720p"
	}

	fileArray := zetflixExtractFileArray(html)
	if fileArray != "" {
		raw := strings.TrimSpace(fileArray)
		hasComment := strings.Contains(raw, "comment:") || strings.Contains(raw, `"comment"`)
		hasFolder := strings.Contains(raw, "folder:") || strings.Contains(raw, `"folder"`)
		movie := !hasComment && !hasFolder

		jsonData := zetflixJSKeyQuoteRe.ReplaceAllString(raw, `$1"$2":`)
		jsonData = zetflixNoSpaceKeyRe.ReplaceAllString(jsonData, `$1"$2":`)
		jsonData = strings.ReplaceAll(jsonData, "},]", "}]")

		var pl []zetflixRoot
		if err := stdjson.Unmarshal([]byte(jsonData), &pl); err == nil && len(pl) > 0 {
			return zetflixEmbed{
				PL:      pl,
				Movie:   movie,
				Quality: quality,
			}, true
		}
	}

	encodedMovie := strings.TrimSpace(submatch1(zetflixMovieFileRe, html))
	if encodedMovie == "" {
		return zetflixEmbed{}, false
	}
	return zetflixEmbed{
		PL: []zetflixRoot{
			{
				Title: "Дубляж",
				File:  encodedMovie,
			},
		},
		Movie:   true,
		Quality: quality,
	}, true
}

// parseFileData parses file data extracted by chromedp.
// The data can be a JSON string (stringified array) or a raw file URL string with quality markers.
func (z *zetflixChecker) parseFileData(fileData string) (zetflixEmbed, bool) {
	fileData = strings.TrimSpace(fileData)
	if fileData == "" {
		return zetflixEmbed{}, false
	}

	quality := "480p"
	if strings.Contains(fileData, "1080") {
		quality = "1080p"
	} else if strings.Contains(fileData, "720") {
		quality = "720p"
	}

	// Try parsing as JSON array first (stringified by browser extraction)
	if strings.HasPrefix(fileData, "[") {
		var pl []zetflixRoot
		if err := stdjson.Unmarshal([]byte(fileData), &pl); err == nil && len(pl) > 0 {
			movie := true
			for _, item := range pl {
				if len(item.Folder) > 0 {
					movie = false
					break
				}
			}
			return zetflixEmbed{PL: pl, Movie: movie, Quality: quality}, true
		}
		// Maybe it's a JSON string containing the array
		var s string
		if err := stdjson.Unmarshal([]byte(fileData), &s); err == nil {
			return z.parseFileData(s)
		}
	}

	// Try as a JSON string wrapping a value
	if strings.HasPrefix(fileData, `"`) {
		var s string
		if err := stdjson.Unmarshal([]byte(fileData), &s); err == nil {
			return z.parseFileData(s)
		}
	}

	// Plain URL string with quality markers like [1080p]https://...
	if strings.Contains(fileData, "[") && strings.Contains(fileData, "://") {
		return zetflixEmbed{
			PL: []zetflixRoot{{
				Title: "Дубляж",
				File:  fileData,
			}},
			Movie:   true,
			Quality: quality,
		}, true
	}

	return zetflixEmbed{}, false
}

func zetflixExtractFileArray(html string) string {
	idx := strings.Index(html, "file:")
	if idx < 0 {
		return ""
	}
	s := html[idx:]
	start := strings.Index(s, "[")
	if start < 0 {
		return ""
	}
	start += idx

	depth := 0
	inQuote := byte(0)
	escape := false
	for i := start; i < len(html); i++ {
		ch := html[i]
		if inQuote != 0 {
			if escape {
				escape = false
				continue
			}
			if ch == '\\' {
				escape = true
				continue
			}
			if ch == inQuote {
				inQuote = 0
			}
			continue
		}

		if ch == '"' || ch == '\'' {
			inQuote = ch
			continue
		}

		if ch == '[' {
			depth++
			continue
		}
		if ch == ']' {
			depth--
			if depth == 0 {
				return html[start : i+1]
			}
		}
	}
	return ""
}

// zetflixDateBasedHost returns a fallback API host based on the current date.
// The zetflix API uses date-prefixed subdomains like "10feb.zet-flix.online".
func zetflixDateBasedHost() string {
	now := time.Now().UTC().Add(3 * time.Hour) // Moscow time (UTC+3)
	months := [12]string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}
	mon := months[now.Month()-1]
	return fmt.Sprintf("https://%d%s.zet-flix.online", now.Day(), mon)
}

func (z *zetflixChecker) resolveAPIHost(ctx context.Context) (string, bool) {
	host := strings.TrimRight(strings.TrimSpace(z.host), "/")
	if host == "" {
		return "", false
	}
	if !zetflixGoPrefixRe.MatchString(host) {
		return host, true
	}

	now := time.Now()
	z.goHostMu.RLock()
	if z.goHostVal != "" && now.Before(z.goHostTill) {
		val := z.goHostVal
		z.goHostMu.RUnlock()
		return val, true
	}
	z.goHostMu.RUnlock()

	// Use a short timeout for the go.zet-flix.online probe so that
	// checksearch doesn't burn its 15s budget waiting for DNS/connect.
	probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, host, nil)
	if err != nil {
		return z.cacheAndReturnDateHost(), true
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("X-Lampac-Go", "1")

	resp, err := z.client.Do(req)
	if err != nil {
		return z.cacheAndReturnDateHost(), true
	}
	defer resp.Body.Close()

	// If go.zet-flix.online returns 404, fall back to date-based host
	if resp.StatusCode == http.StatusNotFound {
		return z.cacheAndReturnDateHost(), true
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return z.cacheAndReturnDateHost(), true
	}
	raw := submatch1(zetflixGoDomainRe, string(body))
	if raw == "" {
		return z.cacheAndReturnDateHost(), true
	}
	resolved := "https://" + strings.TrimSpace(raw)

	z.goHostMu.Lock()
	z.goHostVal = resolved
	z.goHostTill = time.Now().Add(20 * time.Minute)
	z.goHostMu.Unlock()

	return resolved, true
}

func (z *zetflixChecker) cacheAndReturnDateHost() string {
	resolved := zetflixDateBasedHost()
	z.goHostMu.Lock()
	z.goHostVal = resolved
	z.goHostTill = time.Now().Add(20 * time.Minute)
	z.goHostMu.Unlock()
	return resolved
}

func (z *zetflixChecker) numberOfSeasons(ctx context.Context, id int64) int {
	if id <= 0 {
		return 1
	}

	target := "https://tmdb.mirror-kurwa.men/3/tv/" + strconv.FormatInt(id, 10) + "?api_key=4ef0d7355d9ffb5151e987764708ce96"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 1
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("X-Lampac-Go", "1")

	resp, err := z.client.Do(req)
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 1
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 1
	}

	if m := submatch1(zetflixTmdbSeasonsRe, string(raw)); m != "" {
		if n, err := strconv.Atoi(m); err == nil && n > 0 {
			return n
		}
	}
	return 1
}

func (z *zetflixChecker) buildStreamQuality(file string) []map[string]any {
	matches := zetflixQualityURLRe.FindAllStringSubmatch(file, -1)
	if len(matches) == 0 {
		// Fallback: if file is a single URL (no quality markers), use it directly
		trimmed := strings.TrimSpace(file)
		if strings.HasPrefix(trimmed, "http") {
			quality := "auto"
			if strings.Contains(trimmed, "1080") {
				quality = "FHD"
			} else if strings.Contains(trimmed, "720") {
				quality = "HD"
			} else if strings.Contains(trimmed, "480") {
				quality = "SD"
			}
			return []map[string]any{{"quality": quality, "url": trimmed}}
		}
		return nil
	}

	seen := make(map[string]struct{}, len(matches))
	out := make([]map[string]any, 0, len(matches))
	for _, m := range matches {
		if len(m) < 3 {
			continue
		}
		label := strings.TrimSpace(m[1]) + "p"
		link := strings.TrimSpace(m[2])
		if link == "" {
			continue
		}
		// Don't append HLS suffix to obrut CDN URLs — they go through manifest
		// endpoint which resolves the 302 redirect; superdupercdn handles the suffix.
		if !strings.Contains(link, "obrut.show") {
			if z.useHLS && !strings.Contains(link, ".m3u") {
				link += ":hls:manifest.m3u8"
			} else if !z.useHLS && strings.Contains(link, ".m3u") {
				link = strings.ReplaceAll(link, ":hls:manifest.m3u8", "")
			}
		}
		if _, ok := seen[label]; ok {
			continue
		}
		seen[label] = struct{}{}
		out = append(out, map[string]any{
			"quality": normalizeQualityLabel(label),
			"url":     link,
		})
	}
	return out
}

// zetflixBuildQualityMap converts a streamquality slice into a quality map for Lampa.
// Input:  [{"quality":"240p","url":"..."}, {"quality":"720p","url":"..."}]
// Output: {"SD":"...","HD":"..."}
func zetflixBuildQualityMap(streams []map[string]any) map[string]any {
	m := make(map[string]any, len(streams))
	for _, s := range streams {
		label, _ := s["quality"].(string)
		u, _ := s["url"].(string)
		if label != "" && u != "" {
			m[normalizeQualityLabel(label)] = u
		}
	}
	return m
}

// zetflixDeadCDN is the original CDN domain that is now dead (NXDOMAIN since Nov 2025).
const zetflixDeadCDN = "prosto.hdvideobox.me"

// replaceCDN replaces dead CDN host with configured alternative.
func (z *zetflixChecker) replaceCDN(rawURL string) string {
	if z.cdnReplace == "" || rawURL == "" {
		return rawURL
	}
	return strings.ReplaceAll(rawURL, "https://"+zetflixDeadCDN, z.cdnReplace)
}

// proxyStreams applies CDN replacement and stream proxying to all quality entries.
// For obrut.show CDN URLs: wraps ALL qualities in a single manifest endpoint call
// (method:"call" will be set by caller) so Lampa can resolve + display quality map.
// For other CDN URLs: wraps in /proxy/ with headers (method:"play").
// Returns true if streams contain obrut CDN URLs (caller should use method:"call").
func (z *zetflixChecker) proxyStreams(r *http.Request, streams []map[string]any, links *proxylink.Manager, obrutCookies string) bool {
	hasObrut := false
	host := hostFromRequest(r)

	// Check if any stream is obrut and collect quality→CDN URL mapping.
	qualLinks := make(map[string]string, len(streams))
	for _, s := range streams {
		u, _ := s["url"].(string)
		if u == "" {
			continue
		}
		u = z.replaceCDN(u)
		if strings.Contains(u, "obrut.show") {
			hasObrut = true
			label, _ := s["quality"].(string)
			if label == "" {
				label = "auto"
			}
			qualLinks[label] = u
		}
	}

	if hasObrut && len(qualLinks) > 0 {
		// Build one manifest URL with all quality links encoded as JSON.
		linksJSON, _ := stdjson.Marshal(qualLinks)
		manifestURL := host + "/lite/zetflix/manifest?links=" + url.QueryEscape(string(linksJSON))

		// Set manifest URL on all obrut streams (main url uses first/best quality).
		for i := range streams {
			u, _ := streams[i]["url"].(string)
			if u == "" {
				continue
			}
			u = z.replaceCDN(u)
			if strings.Contains(u, "obrut.show") {
				streams[i]["url"] = manifestURL
			} else if links != nil {
				streams[i]["url"] = z.proxyWithHeaders(r, u, links, obrutCookies)
			}
		}
	} else {
		// No obrut URLs — proxy directly.
		for i := range streams {
			u, _ := streams[i]["url"].(string)
			if u == "" {
				continue
			}
			u = z.replaceCDN(u)
			if links != nil {
				u = z.proxyWithHeaders(r, u, links, obrutCookies)
			}
			streams[i]["url"] = u
		}
	}
	return hasObrut
}

// proxyWithHeaders wraps a zetflix stream URL through /proxy/ with
// Origin/Referer headers that the CDN requires.
// obrutCookies are included for obrut.show CDN URLs (needed for 302 redirect).
func (z *zetflixChecker) proxyWithHeaders(r *http.Request, rawURL string, links *proxylink.Manager, obrutCookies string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" || links == nil {
		return rawURL
	}
	if isStreamProxyDisabled("zetflix") {
		return rawURL
	}
	host := streamHostFromRequest(r)
	reqIP := clientIP(r)

	// For superdupercdn/obrut CDN URLs, use the obrut host as Origin/Referer
	// (the CDN expects the embed origin, not the zetflix site).
	// For other CDNs (fotpro, etc.), use the zetflix date-based host.
	var refDomain string
	isObrut := strings.Contains(rawURL, "obrut.show")
	if strings.Contains(rawURL, "superdupercdn") || isObrut {
		refDomain = zetflixObrutHost
	} else {
		refHost := ""
		z.goHostMu.RLock()
		if z.goHostVal != "" {
			refHost = z.goHostVal
		}
		z.goHostMu.RUnlock()
		if refHost == "" {
			refHost = zetflixDateBasedHost()
		}
		refDomain = refHost
		if idx := strings.Index(refDomain, "://"); idx >= 0 {
			refDomain = refDomain[idx+3:]
		}
		refDomain = strings.TrimRight(refDomain, "/")
	}

	headers := map[string]string{
		"Origin":  "https://" + refDomain,
		"Referer": "https://" + refDomain + "/",
	}
	// cdn-*.obrut.show requires session cookies for the 302 redirect.
	if isObrut && obrutCookies != "" {
		headers["Cookie"] = obrutCookies
	}
	encrypted := links.EncryptURIWithHeaders(rawURL, reqIP, "zetflix", headers)
	if encrypted == "" {
		return rawURL
	}
	return host + "/proxy/" + encrypted
}

func (z *zetflixChecker) probe(ctx context.Context, method, target string) bool {
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/json,*/*")
	req.Header.Set("X-Lampac-Go", "1")

	resp, err := z.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if method == http.MethodHead && resp.StatusCode == http.StatusMethodNotAllowed {
		return false
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return false
	}
	if method == http.MethodHead {
		return true
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(body))) > 0
}

// ZetflixExtractPlayerConfig parses the HDVBPlayer playerConfigs JSON from HTML.
func ZetflixExtractPlayerConfig(html string) (ZetflixPlayerConfig, bool) {
	m := zetflixPlayerConfigsRe.FindStringSubmatch(html)
	if len(m) < 2 {
		return ZetflixPlayerConfig{}, false
	}
	var pc ZetflixPlayerConfig
	if err := stdjson.Unmarshal([]byte(m[1]), &pc); err != nil {
		log.Warn().Err(err).Msg("zetflix: failed to parse playerConfigs JSON")
		return ZetflixPlayerConfig{}, false
	}
	return pc, true
}

// fetchPlaylistURL resolves the encrypted file field by POSTing to the
// HDVBPlayer playlist API. When file starts with "~", the player makes:
//
//	POST https://vid11.{href}/playlist/{file[1:]}.txt
//	X-CSRF-TOKEN: {key}
//
// and receives the real stream URL(s).
func (z *zetflixChecker) fetchPlaylistURL(ctx context.Context, pc ZetflixPlayerConfig) (string, bool) {
	href := strings.TrimSpace(pc.Href)
	if href == "" {
		return "", false
	}
	fileParam := pc.File[1:] // strip leading ~
	target := "https://vid11." + href + "/playlist/" + fileParam + ".txt"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-TOKEN", pc.Key)
	req.Header.Set("User-Agent", zetflixUserAgent)
	if pc.Referer != "" {
		req.Header.Set("Referer", "https://"+pc.Referer+"/")
		req.Header.Set("Origin", "https://"+pc.Referer)
	}

	resp, err := z.client.Do(req)
	if err != nil {
		log.Warn().Err(err).Msg("zetflix: playlist API request failed")
		return "", false
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", false
	}
	result := strings.TrimSpace(string(body))
	if result == "" || resp.StatusCode != http.StatusOK {
		log.Warn().Int("status", resp.StatusCode).Int("len", len(result)).Msg("zetflix: playlist API returned empty/error")
		return "", false
	}
	if !strings.HasPrefix(result, "http") {
		log.Warn().Str("response", result).Msg("zetflix: playlist API returned non-URL response")
		return "", false
	}
	return result, true
}

// buildEmbedFromStreamURL wraps a resolved stream URL (typically m3u8) into
// a zetflixEmbed for the player. title is the voice/translator label.
func (z *zetflixChecker) buildEmbedFromStreamURL(streamURL, title string) (zetflixEmbed, bool) {
	streamURL = strings.TrimSpace(streamURL)
	if streamURL == "" {
		return zetflixEmbed{}, false
	}
	if title == "" {
		title = "Дубляж"
	}

	// fotpro streams are adaptive HLS with multiple qualities up to 1080p;
	// the URL itself rarely contains quality markers, so default to 1080p.
	quality := "1080p"
	if strings.Contains(streamURL, "480") {
		quality = "480p"
	} else if strings.Contains(streamURL, "720") {
		quality = "720p"
	}

	return zetflixEmbed{
		PL: []zetflixRoot{{
			Title: title,
			File:  streamURL,
		}},
		Movie:   true,
		Quality: quality,
	}, true
}
