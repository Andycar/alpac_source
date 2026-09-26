package litesrc

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/proxylink"
)

type allohaChecker struct {
	client       *http.Client
	apiHost      string
	linkHost     string
	token        string
	keepopen     bool
	directStream bool
	keepalive    int  // seconds to keep browser tab alive (default 600)
	iframeMode   bool // skip browser-resolve, return raw iframe URL from API response
}

// allohaStreamEntry caches a resolved CDN stream for dynamic proxying.
// CDN URLs expire every ~3-4 min; lightweight refresh updates them.
type allohaStreamEntry struct {
	mu         sync.RWMutex
	quals      map[string]string // quality → CDN m3u8 URL
	cdnHeaders map[string]string // Borth, Authorizations from browser
	resolvedAt time.Time
	tokenMovie string
	kpID       string // kinopoisk id — used for kinomix pre-navigation during browser re-resolve
	// cacheKey is the allohaStreamKey this entry is stored under, so a loop that
	// gives up on the entry can evict it instead of leaving a zombie that every
	// later request cache-hits (dead URL, no refresher) until the process restarts.
	cacheKey string
	// translation/season/episode identify WHICH stream this entry holds. A
	// re-resolve MUST replay them: token_movie alone resolves to the default dub
	// and S1E1, so refreshing without them silently swaps the user's stream for a
	// different one (wrong dub / wrong episode), and its segment paths don't match
	// what the player is asking for.
	translation string
	season      int
	episode     int
	refreshNow  chan struct{} // signal immediate refresh (e.g. on 403)
	// keepopen=true: browser tab stays alive and pushes header updates directly.
	// In this mode the background refresh loop is replaced by allohaKeepopenLoop.
	keepopen bool
	// For lightweight refresh (re-call /bnsi/movies/ without Chrome)
	moviesURL     string
	moviesHeaders map[string]string
	moviesBody    []byte
	// Segment throttle: prevent burst downloading that triggers CDN rate limits.
	// Browser downloads segments at playback speed (~1 per 6s). Without throttle
	// our proxy fires all segments as fast as possible → CDN bans.
	lastSegmentAt int64 // atomic: unix nano of last segment request
	// Token bucket behind allohaTakeSegmentToken: a burst of allohaSegBurst
	// segments passes untouched, then one per allohaSegInterval. Replaces the
	// fixed 2 s sleep per segment, which added 1.5–2 s of latency to EVERY
	// fetch — on a 10 Mbit/s TV link a 6 s 1080p segment then took ~5 s to land
	// and the buffer never rose above one segment (incident 0901-234541: «рывки
	// по секунде», buf 2.4 s, stalls 2).
	segMu     sync.Mutex
	segTokens float64
	segLast   time.Time
	// lastNoWSSignalAt throttles the "no live edge_hash" re-resolve request.
	// Since alloha stopped shipping wsUrl/sid in /bnsi/movies/, a re-resolve can
	// no longer bring the WS back, so an unthrottled signal turns EVERY segment
	// into a browser resolve (measured: 35 in 10 minutes → Chrome pool starved →
	// 403/502 for everyone). Refreshing the URL is still worth doing, just rarely.
	lastNoWSSignalAt int64 // atomic: unix nano
	// Chrome browser context from keepopen — used for segment fetch via Chrome.
	// When set, segments are downloaded via fetch() in the live player tab which
	// has Guard verified, WS connected, and proper TLS fingerprint.
	browserCtx context.Context
}

var (
	allohaStreamCacheMu sync.RWMutex
	allohaStreamCache   = make(map[string]*allohaStreamEntry) // key = tokenMovie
)

// Alloha needs affiliate credentials: get your own token (and, optionally, a
// custom player domain) from the Alloha affiliate program and put them in
// [online.alloha] token / link_host. Without a token the source stays off.
//
// IMPORTANT: the CDN binds signed stream URLs to the player-session origin — a
// URL resolved under one player host serves segments ONLY with Referer/Origin
// of that same host. linkHost must therefore be the same host for resolve AND
// every CDN request; never mix hosts across the two.

// allohaMigrateLinkHost normalizes the configured player host (dead player
// domains from earlier eras are not auto-migrated in this build — set
// [online.alloha] link_host to your affiliate player domain).
func allohaMigrateLinkHost(linkHost string) string {
	return strings.TrimRight(strings.TrimSpace(linkHost), "/")
}

// allohaIframeURL builds the player-page URL a client embeds as an <iframe> for
// direct-iframe playback. The alloha player runs from linkHost's own origin,
// resolves the CDN itself and handles Borth / WS edge_hash / ads client-side —
// so this path needs nothing from our server (no headless-Chrome resolve, no
// segment proxy). The page accepts ANY non-empty Referer (an embedded iframe
// always sends one); an empty Referer 404s, so it must be embedded, not fetched.
func (a *allohaChecker) allohaIframeURL(tokenMovie, translation string, season, episode int) string {
	u := fmt.Sprintf("%s/?token_movie=%s&token=%s",
		strings.TrimRight(a.linkHost, "/"),
		url.QueryEscape(tokenMovie),
		url.QueryEscape(a.token))
	if translation != "" {
		u += "&translation=" + url.QueryEscape(translation)
	}
	if season > 0 {
		u += "&season=" + strconv.Itoa(season) + "&episode=" + strconv.Itoa(episode)
	}
	return u
}

// allohaIframeScheme marks a capi quality URL as an embed-me iframe page rather
// than an HLS stream. capi ByQuality is a flat {label→url} map with no per-entry
// method field, and voices from several sources merge by name — so the signal
// has to travel ON the url itself to survive the merge. Clients strip the scheme
// (→ https), open the page in a webview/iframe overlay, and never feed it to the
// native/Shaka player. capiSameOrigin passes these through untouched.
const allohaIframeScheme = "iframe://"

// allohaCapiIframeURL wraps allohaIframeURL in the iframe:// scheme for capi.
func (a *allohaChecker) allohaCapiIframeURL(tokenMovie, translation string, season, episode int) string {
	raw := a.allohaIframeURL(tokenMovie, translation, season, episode)
	return allohaIframeScheme + strings.TrimPrefix(strings.TrimPrefix(raw, "https://"), "http://")
}

func NewAllohaChecker(cfg config.Config) *allohaChecker {
	apiHost := strings.TrimSpace(cfg.Online.Alloha.APIHost)
	if apiHost == "" {
		apiHost = "https://apbugall.org"
	}
	// v2 API lives at apbugall.org (no "api." prefix). Legacy api.apbugall.org/?token=
	// still works but is undocumented and may be retired. Auto-migrate stale configs.
	if strings.Contains(apiHost, "api.apbugall.org") {
		apiHost = strings.ReplaceAll(apiHost, "api.apbugall.org", "apbugall.org")
	}
	apiHost = strings.TrimRight(apiHost, "/")

	linkHost := allohaMigrateLinkHost(cfg.Online.Alloha.LinkHost)

	token := strings.TrimSpace(cfg.Online.Alloha.Token)

	// Register CDN proxy rotating transport for /proxy/ handler.
	if len(cfg.Online.Alloha.CDNProxies) > 0 {
		rotateMin := cfg.Online.Alloha.RotateMin
		if rotateMin <= 0 {
			rotateMin = 3.5
		}
		rt := httpclient.NewRotatingRoundTripper("alloha", cfg.Online.Alloha.CDNProxies, rotateMin)
		httpclient.RegisterRoundTripper("alloha", rt)
	}

	keepalive := cfg.Online.Alloha.KeepAlive
	if cfg.Online.Alloha.KeepOpen && keepalive <= 0 {
		keepalive = 600
	}

	// Initialize Guard client for proof-of-browser tokens.
	getOrCreateGuardClient(linkHost, "alloha")

	return &allohaChecker{
		client:       httpclient.NewForBalancer("alloha", 10*time.Second),
		apiHost:      apiHost,
		linkHost:     linkHost,
		token:        token,
		keepopen:     cfg.Online.Alloha.KeepOpen,
		keepalive:    keepalive,
		iframeMode:   cfg.Online.Alloha.IframeMode,
		directStream: cfg.Online.Alloha.DirectStream,
	}
}

func (a *allohaChecker) Handle(cfg config.Config, proxyLinks *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := strings.Trim(strings.TrimPrefix(strings.ToLower(r.URL.Path), "/lite/"), "/")

		switch raw {
		case "alloha":
			if parseBoolParam(r.URL.Query().Get("checksearch")) {
				show, quality := a.checkSearch(r)
				writeCheckSearchResponse(w, show, quality)
				return
			}
			if parseBoolParam(r.URL.Query().Get("similar")) {
				a.spiderSearch(w, r)
				return
			}
			a.index(w, r)
			return

		case "alloha-search":
			a.spiderSearch(w, r)
			return

		case "alloha/video", "alloha/video.m3u8":
			a.video(w, r, raw == "alloha/video.m3u8", proxyLinks)
			return

		case "alloha/stream", "alloha/stream.m3u8":
			a.streamHLS(w, r)
			return

		case "alloha/auth":
			// Direct mode only: refresh the rotating Accepts-Controls without
			// re-resolving the stream.
			a.allohaAuthHandler(w, r)
			return
		}

		// Resolved stream URL — раньше его опрашивал клиентский cdn_direct.js (удалён 2026-08-22).
		if raw == "alloha/resolved" {
			resolvedStreamsMu.Lock()
			entry, ok := resolvedStreams["latest"]
			var bestURL string
			if ok && time.Since(entry.ts) < 5*time.Minute {
				bestURL = entry.url
			}
			resolvedStreamsMu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Access-Control-Allow-Origin", "*")
			if bestURL != "" {
				fmt.Fprintf(w, `{"url":%q}`, bestURL)
			} else {
				w.Write([]byte(`{}`))
			}
			return
		}

		// Prefix match: all /lite/alloha/player/* paths → reverse proxy
		if strings.HasPrefix(raw, "alloha/player") {
			a.servePlayerProxy(w, r)
			return
		}

		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
	}
}

// ---------- Index (translation list) ----------

func (a *allohaChecker) index(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	serial := strings.TrimSpace(q.Get("serial")) == "1"
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	originalLanguage := strings.ToLower(strings.TrimSpace(q.Get("original_language")))
	year, _ := strconv.Atoi(strings.TrimSpace(q.Get("year")))
	t := strings.TrimSpace(q.Get("t"))
	s, _ := strconv.Atoi(strings.TrimSpace(q.Get("s")))
	e, _ := strconv.Atoi(strings.TrimSpace(q.Get("e")))

	orid := strings.TrimSpace(q.Get("orid"))
	imdbID := strings.TrimSpace(q.Get("imdb_id"))
	kinopoiskID := strings.TrimSpace(q.Get("kinopoisk_id"))
	tmdbID := strings.TrimSpace(firstNonEmpty(q.Get("tmdb_id"), q.Get("id")))

	data, category, tokenMovie := a.search(r, orid, imdbID, kinopoiskID, tmdbID, title, serial, originalLanguage, year)
	if data == nil || tokenMovie == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(r)
	capi := capiResolveRequest(r)
	// Direct-iframe for /capi clients: emit an embed-me iframe voice instead of
	// the deferred server-resolved stream, but ONLY when the source is in iframe
	// mode AND this client advertised it can embed a webview (iframe=1). tvOS has
	// no WKWebView, so its client never sends the flag and keeps the native path.
	wantIframe := a.iframeMode && parseBoolParam(q.Get("iframe"))
	// Тот же приём, что у iframe: возможность объявляет КЛИЕНТ. Браузеру Origin/Referer подменить
	// нельзя (forbidden headers), поэтому веб флаг не шлёт и остаётся на проксировании, а нативные
	// плееры (ExoPlayer/AVPlayer) заголовки ставят и качают сегменты сами.
	wantDirect := a.directStream && parseBoolParam(q.Get("direct"))

	// Category alone lies for anime: v2 slugs (anime/cartoons) don't split movie vs serial —
	// «Наруто» came back movie-categorized WITH a full seasons payload, the movie-shaped answer
	// then died on capi's movie↔serial type guard (весь сериал «не находился» в capi). The
	// payload knows best: a seasons map ⇒ serial, regardless of category.
	if len(allohaMapField(data, "seasons")) == 0 && (category == 1 || category == 3) {
		a.writeMovie(w, rjson, capi, wantIframe, wantDirect, host, title, originalTitle, tokenMovie, kinopoiskID, data)
		return
	}

	// Serial
	a.writeSerial(w, rjson, capi, wantIframe, wantDirect, host, title, originalTitle, tokenMovie, kinopoiskID, s, e, t, data)
}

// ---------- Movie: list translations ----------

// allohaResLabel maps an integer height (2160, 1080, …) to a Lampa quality label.
func allohaResLabel(h int) string {
	if h <= 0 {
		return ""
	}
	return strconv.Itoa(h) + "p"
}

// allohaResolutionLabels turns the v2 `resolutions` array (+uhd/quality fallback)
// into descending quality labels ["2160p","1080p",…]. Used to advertise Alloha's
// available qualities in the /capi voice merge WITHOUT resolving the stream.
func allohaResolutionLabels(resolutions any, uhd bool, quality string) []string {
	var heights []int
	switch arr := resolutions.(type) {
	case []any:
		for _, v := range arr {
			switch n := v.(type) {
			case float64:
				heights = append(heights, int(n))
			case int:
				heights = append(heights, n)
			case string:
				if h, err := strconv.Atoi(strings.TrimSuffix(n, "p")); err == nil {
					heights = append(heights, h)
				}
			}
		}
	case []int:
		heights = append(heights, arr...)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(heights)))
	var labels []string
	for _, h := range heights {
		if l := allohaResLabel(h); l != "" {
			labels = append(labels, l)
		}
	}
	if len(labels) == 0 {
		// Fallback: uhd flag or a bare quality string ("2160p"/"FHD"/…).
		if uhd {
			labels = append(labels, "2160p")
		} else if quality != "" {
			labels = append(labels, quality)
		}
	}
	return labels
}

// allohaCapiDeferredQuality builds the /capi `quality` object for one voice:
// {label → deferred /lite/alloha/stream.m3u8 URL}. capiParseQuality reads it as a
// {quality→streamURL} map. Every label points at the SAME deferred endpoint
// (streamHLS bootstraps the headless-Chrome resolve on first hit); the ?q= just
// carries the label the client picked. This surfaces Alloha voices+qualities in
// the /capi merge from the fast search — the expensive resolve runs on playback.
func allohaCapiDeferredQuality(host, tokenMovie, tKey, kpParam string, s, e int, labels []string, direct bool) map[string]string {
	base := host + "/lite/alloha/stream.m3u8?token_movie=" + url.QueryEscape(tokenMovie) + "&t=" + url.QueryEscape(tKey) + kpParam
	if s > 0 {
		base += "&s=" + strconv.Itoa(s) + "&e=" + strconv.Itoa(e)
	}
	// Клиент подтвердил, что умеет сам ставить CDN-заголовки → отдаём манифест, сегменты которого
	// указывают прямо на CDN. Байты пойдут мимо нас; заголовки клиент берёт из /lite/alloha/auth.
	if direct {
		base += "&direct=1"
	}
	byq := map[string]string{}
	for _, l := range labels {
		byq[l] = base + "&q=" + url.QueryEscape(l)
	}
	if len(byq) == 0 {
		byq["auto"] = base + "&q=auto"
	}
	return byq
}

func (a *allohaChecker) writeMovie(w http.ResponseWriter, rjson, capi, wantIframe, wantDirect bool, host, title, originalTitle, tokenMovie, kpID string, data map[string]any) {
	translations := allohaMapField(data, "translation_iframe")
	if len(translations) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	baseTitle := getsTVJoinName(title, originalTitle)
	var rows []map[string]any
	var labels []string

	for tKey, tVal := range translations {
		inner, ok := tVal.(map[string]any)
		if !ok {
			continue
		}
		name, _ := inner["name"].(string)
		if name == "" {
			name = "Озвучка"
		}
		quality, _ := inner["quality"].(string)
		uhdVal, _ := inner["uhd"]
		uhd := fmt.Sprint(uhdVal) == "true"
		if uhd {
			quality = "2160p"
		}

		kpParam := ""
		if kpID != "" {
			kpParam = "&kp=" + url.QueryEscape(kpID)
		}

		// /capi merge: emit a deferred play-node (voice + advertised qualities,
		// stream resolves on playback) so Alloha joins the voice list without the
		// 15-20s browser resolve blocking aggregation. See allohaCapiDeferredQuality.
		if capi {
			if wantIframe {
				// Direct-iframe: one embed-me url per voice (the alloha player picks
				// quality itself, so a single "auto" entry). capiSameOrigin passes
				// the iframe:// marker through; the client embeds it in a webview.
				rows = append(rows, map[string]any{
					"translate": name,
					"quality":   map[string]string{"auto": a.allohaCapiIframeURL(tokenMovie, tKey, 0, 0)},
					"title":     fmt.Sprintf("%s (%s)", baseTitle, name),
				})
				labels = append(labels, name)
				continue
			}
			resLabels := allohaResolutionLabels(inner["resolutions"], uhd, quality)
			row := map[string]any{
				"translate": name,
				"quality":   allohaCapiDeferredQuality(host, tokenMovie, tKey, kpParam, 0, 0, resLabels, wantDirect),
				"title":     fmt.Sprintf("%s (%s)", baseTitle, name),
			}
			rows = append(rows, row)
			labels = append(labels, name)
			continue
		}

		link := host + "/lite/alloha/video?t=" + url.QueryEscape(tKey) + "&token_movie=" + url.QueryEscape(tokenMovie) + kpParam
		stream := host + "/lite/alloha/video.m3u8?t=" + url.QueryEscape(tKey) + "&token_movie=" + url.QueryEscape(tokenMovie) + kpParam + "&play=true"

		row := map[string]any{
			"method":    "call",
			"url":       link,
			"stream":    stream,
			"translate": name,
			"details":   quality,
			"title":     fmt.Sprintf("%s (%s)", baseTitle, name),
		}
		if quality != "" {
			row["voice_name"] = quality
		}
		rows = append(rows, row)
		labels = append(labels, name)
	}

	if len(rows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "movie", "data": rows})
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

// ---------- Serial: seasons → episodes ----------

func (a *allohaChecker) writeSerial(w http.ResponseWriter, rjson, capi, wantIframe, wantDirect bool, host, title, originalTitle, tokenMovie, kpID string, s, e int, t string, data map[string]any) {
	kpParam := ""
	if kpID != "" {
		kpParam = "&kp=" + url.QueryEscape(kpID)
	}
	seasons := allohaMapField(data, "seasons")
	if len(seasons) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// /capi with a concrete season+episode: emit deferred play-nodes (one per
	// voice available for THIS episode) so Alloha joins the voice merge from the
	// fast search — the 15-20s browser resolve runs on playback (streamHLS). No
	// Voice array → capi reads the play-nodes directly instead of re-drilling each
	// dub (which would fire N browser resolves and time out → 0 voices).
	if capi && s > 0 && e > 0 {
		seasonData := allohaMapField(seasons, strconv.Itoa(s))
		episodes := allohaMapField(seasonData, "episodes")
		epMap := allohaMapField(episodes, strconv.Itoa(e))
		transMap := allohaMapField(epMap, "translation")
		baseTitle := getsTVJoinName(title, originalTitle)
		var rows []map[string]any
		for tKey, tVal := range transMap {
			inner, ok := tVal.(map[string]any)
			if !ok {
				continue
			}
			name, _ := inner["translation"].(string)
			if name == "" {
				name, _ = inner["name"].(string)
			}
			if name == "" {
				name = "Озвучка"
			}
			if strings.Contains(strings.ToLower(name), "субтитры") {
				continue
			}
			if wantIframe {
				rows = append(rows, map[string]any{
					"translate": name,
					"quality":   map[string]string{"auto": a.allohaCapiIframeURL(tokenMovie, tKey, s, e)},
					"title":     fmt.Sprintf("%s / %d сезон %d серия (%s)", baseTitle, s, e, name),
					"s":         s,
					"e":         e,
				})
				continue
			}
			quality, _ := inner["quality"].(string)
			uhd := fmt.Sprint(inner["uhd"]) == "true"
			labels := allohaResolutionLabels(inner["resolutions"], uhd, quality)
			rows = append(rows, map[string]any{
				"translate": name,
				"quality":   allohaCapiDeferredQuality(host, tokenMovie, tKey, kpParam, s, e, labels, wantDirect),
				"title":     fmt.Sprintf("%s / %d сезон %d серия (%s)", baseTitle, s, e, name),
				"s":         s,
				"e":         e,
			})
		}
		if len(rows) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"type": "serial", "data": rows})
		return
	}

	// List seasons
	if s <= 0 {
		seasonNums := make([]int, 0, len(seasons))
		for k := range seasons {
			n, _ := strconv.Atoi(k)
			if n > 0 {
				seasonNums = append(seasonNums, n)
			}
		}
		sort.Ints(seasonNums) // ascending: 1 сезон, 2 сезон, …

		var rows []map[string]any
		var lbls []string
		for _, sn := range seasonNums {
			name := strconv.Itoa(sn) + " сезон"
			row := map[string]any{
				"method": "link",
				"id":     sn,
				"name":   name,
				"url": host + "/lite/alloha?rjson=" + getsTVBool(rjson) +
					"&s=" + strconv.Itoa(sn) +
					"&orid=" + url.QueryEscape(tokenMovie) +
					"&title=" + url.QueryEscape(title) +
					"&original_title=" + url.QueryEscape(originalTitle) +
					func() string {
						if kpID != "" {
							return "&kinopoisk_id=" + url.QueryEscape(kpID)
						}
						return ""
					}(),
			}
			rows = append(rows, row)
			lbls = append(lbls, name)
		}

		if rjson {
			writeJSON(w, http.StatusOK, map[string]any{"type": "season", "data": rows})
			return
		}
		var sb strings.Builder
		sb.WriteString(`<div class="videos__line">`)
		for i, row := range rows {
			getsTVAppendSeasonHTML(&sb, row, lbls[i], i == 0)
		}
		sb.WriteString(`</div>`)
		writeHTML(w, http.StatusOK, sb.String())
		return
	}

	// List episodes for season s
	seasonData := allohaMapField(seasons, strconv.Itoa(s))
	episodes := allohaMapField(seasonData, "episodes")
	if len(episodes) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Collect translations from all episodes
	translations := make(map[string]string) // key → display name
	for _, epVal := range episodes {
		epMap, ok := epVal.(map[string]any)
		if !ok {
			continue
		}
		transMap := allohaMapField(epMap, "translation")
		for tKey, tVal := range transMap {
			if _, exists := translations[tKey]; exists {
				continue
			}
			inner, ok := tVal.(map[string]any)
			if !ok {
				continue
			}
			tName, _ := inner["translation"].(string)
			if tName == "" {
				tName = "Озвучка"
			}
			if strings.Contains(strings.ToLower(tName), "субтитры") {
				continue
			}
			translations[tKey] = tName
		}
	}

	activeTranslate := t
	if activeTranslate == "" {
		for k := range translations {
			activeTranslate = k
			break
		}
	}

	// Build voice tabs
	var voiceHTML strings.Builder
	for tKey, tName := range translations {
		active := tKey == activeTranslate
		voiceURL := host + "/lite/alloha?rjson=" + getsTVBool(rjson) +
			"&s=" + strconv.Itoa(s) +
			"&t=" + url.QueryEscape(tKey) +
			"&orid=" + url.QueryEscape(tokenMovie) +
			"&title=" + url.QueryEscape(title) +
			"&original_title=" + url.QueryEscape(originalTitle)
		if kpID != "" {
			voiceURL += "&kinopoisk_id=" + url.QueryEscape(kpID)
		}

		voiceHTML.WriteString(`<div class="videos__button selector`)
		if active {
			voiceHTML.WriteString(` active`)
		}
		voiceHTML.WriteString(`" data-json='`)
		voiceHTML.WriteString(getsTVAttrJSON(map[string]any{
			"method": "link",
			"url":    voiceURL,
		}))
		voiceHTML.WriteString(`'>`)
		voiceHTML.WriteString(tName)
		voiceHTML.WriteString(`</div>`)
	}

	// Build episode list
	epNums := make([]int, 0, len(episodes))
	for k := range episodes {
		n, _ := strconv.Atoi(k)
		if n > 0 {
			epNums = append(epNums, n)
		}
	}
	sort.Ints(epNums) // ascending: 1 серия, 2 серия, … (not 10→1)

	var epRows []map[string]any
	var epLabels []string
	for _, epNum := range epNums {
		epMap, ok := episodes[strconv.Itoa(epNum)].(map[string]any)
		if !ok {
			continue
		}
		// Check if this episode has the active translation
		transMap := allohaMapField(epMap, "translation")
		if activeTranslate != "" {
			if _, hasTrans := transMap[activeTranslate]; !hasTrans {
				continue
			}
		}

		link := host + "/lite/alloha/video?t=" + url.QueryEscape(activeTranslate) +
			"&s=" + strconv.Itoa(s) +
			"&e=" + strconv.Itoa(epNum) +
			"&token_movie=" + url.QueryEscape(tokenMovie) + kpParam
		stream := host + "/lite/alloha/video.m3u8?t=" + url.QueryEscape(activeTranslate) +
			"&s=" + strconv.Itoa(s) +
			"&e=" + strconv.Itoa(epNum) +
			"&token_movie=" + url.QueryEscape(tokenMovie) + kpParam + "&play=true"

		name := strconv.Itoa(epNum) + " серия"
		row := map[string]any{
			"method":  "call",
			"url":     link,
			"stream":  stream,
			"title":   fmt.Sprintf("%s / %d сезон %d серия", getsTVJoinName(title, originalTitle), s, epNum),
			"season":  s,
			"episode": epNum,
		}
		epRows = append(epRows, row)
		epLabels = append(epLabels, name)
	}

	if rjson {
		var voices []map[string]any
		for tKey, tName := range translations {
			voices = append(voices, map[string]any{
				"name":   tName,
				"active": tKey == activeTranslate,
				"url": host + "/lite/alloha?rjson=true&s=" + strconv.Itoa(s) +
					"&t=" + url.QueryEscape(tKey) +
					"&orid=" + url.QueryEscape(tokenMovie) +
					"&title=" + url.QueryEscape(title) +
					"&original_title=" + url.QueryEscape(originalTitle) +
					func() string {
						if kpID != "" {
							return "&kinopoisk_id=" + url.QueryEscape(kpID)
						}
						return ""
					}(),
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"type":   "episode",
			"data":   epRows,
			"voices": voices,
		})
		return
	}

	var sb strings.Builder
	if voiceHTML.Len() > 0 {
		sb.WriteString(`<div class="videos__line">`)
		sb.WriteString(voiceHTML.String())
		sb.WriteString(`</div>`)
	}
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range epRows {
		getsTVAppendMovieHTML(&sb, row, epLabels[i], i == 0, s, row["episode"].(int))
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// ---------- Video: resolve stream via chromedp ----------

func (a *allohaChecker) video(w http.ResponseWriter, r *http.Request, forcePlay bool, proxyLinks *proxylink.Manager) {
	q := r.URL.Query()
	tokenMovie := strings.TrimSpace(q.Get("token_movie"))
	t := strings.TrimSpace(q.Get("t"))
	s, _ := strconv.Atoi(strings.TrimSpace(q.Get("s")))
	e, _ := strconv.Atoi(strings.TrimSpace(q.Get("e")))
	kpID := strings.TrimSpace(q.Get("kp"))

	if tokenMovie == "" {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	_ = hostFromRequest(r)

	// iframe_mode=true: skip browser-resolve entirely and return the raw
	// CDN iframe URL straight to the client. The Lampa player must embed it
	// as <iframe>; inside the iframe Phantom CDN sees its own origin
	// (sansa.stravers.live) and accepts segment requests without per-IP
	// signing checks. This matches kinomix.web.app + nextgen lampac (C#).
	if a.iframeMode {
		iframeURL := a.allohaIframeURL(tokenMovie, t, s, e)
		if forcePlay || parseBoolParam(q.Get("play")) {
			http.Redirect(w, r, iframeURL, http.StatusFound)
			return
		}
		// method:"iframe" tells Lampa-style players to <iframe>-embed the URL
		// instead of fetching it as m3u8. Plugins that don't understand it
		// will fall back to `url` which is the same iframe page (HTML, not
		// m3u8 — those will fail; user must enable iframe-aware plugin).
		writeJSON(w, http.StatusOK, map[string]any{
			"method": "iframe",
			"url":    iframeURL,
			"iframe": iframeURL,
		})
		return
	}

	// Dynamic stream endpoint: browser resolve → cache entry → return /lite/alloha/stream.m3u8 URL.
	// CDN URLs expire in ~2 min; background refresh loop keeps them fresh.
	res := allohaResolveViaBrowserFull(r.Context(), a.linkHost, a.token, tokenMovie, t, s, e, a.keepalive, kpID)
	if res != nil && res.hlsURL != "" {
		fmt.Printf("[alloha] browser resolved: url=%s hdrs_keys=%v hasBorth=%v moviesURL=%s keepopen=%v\n",
			res.hlsURL[:min(len(res.hlsURL), 80)],
			func() []string {
				var ks []string
				for k := range res.headers {
					ks = append(ks, k)
				}
				return ks
			}(),
			res.headers["Borth"] != "",
			res.moviesURL,
			res.headerUpdates != nil)

		// Cache the stream entry for the dynamic stream endpoint.
		entry := &allohaStreamEntry{
			quals:         map[string]string{"auto": res.hlsURL},
			cdnHeaders:    res.headers,
			resolvedAt:    time.Now(),
			tokenMovie:    tokenMovie,
			kpID:          kpID,
			cacheKey:      allohaStreamKey(tokenMovie, t, s, e),
			translation:   t,
			season:        s,
			episode:       e,
			keepopen:      res.headerUpdates != nil,
			refreshNow:    make(chan struct{}, 1),
			moviesURL:     res.moviesURL,
			moviesHeaders: res.moviesHeaders,
			moviesBody:    res.moviesBody,
			browserCtx:    res.browserCtx,
		}
		allohaStreamCacheMu.Lock()
		allohaStreamCache[entry.cacheKey] = entry
		allohaStreamCacheMu.Unlock()

		if res.headerUpdates != nil {
			// keepopen mode: browser stays alive and pushes fresh headers directly.
			// The background refresh loop is not needed.
			go a.allohaKeepopenLoop(entry, res.headerUpdates)
		} else {
			go a.allohaRefreshLoop(entry)
		}

		host := hostFromRequest(r)
		// Three modes for the URL we return to the client:
		//   1. no_stream_proxy=['alloha']: hand the raw CDN m3u8 URL straight
		//      to the player. Phantom CDN signs URLs against the resolver IP,
		//      so this works only when the client and server share an outbound
		//      IP (e.g. both behind the same residential V2Box) — but when it
		//      does work, the server is out of the playback path entirely.
		//   2. default: route through /lite/alloha/stream.m3u8 (server-proxy
		//      with WS edge_hash + segment throttle + hard-reset cascade).
		var streamURL string
		if isStreamProxyDisabled("alloha") && res.hlsURL != "" {
			streamURL = res.hlsURL
		} else {
			// Carry t/s/e so streamHLS reconstructs the SAME cache key this resolve
			// wrote under (allohaStreamKey) — otherwise every playlist fetch misses
			// the cache and re-bootstraps a headless Chrome resolve (endless
			// cancelled/pending stream.m3u8 fetches).
			streamURL = host + "/lite/alloha/stream.m3u8?token_movie=" + url.QueryEscape(tokenMovie) + "&t=" + url.QueryEscape(t)
			if s > 0 {
				streamURL += "&s=" + strconv.Itoa(s) + "&e=" + strconv.Itoa(e)
			}
			if kpID != "" {
				streamURL += "&kp=" + url.QueryEscape(kpID)
			}
			streamURL += "&q=auto"
		}

		if forcePlay || parseBoolParam(q.Get("play")) {
			http.Redirect(w, r, streamURL, http.StatusFound)
			return
		}
		out := map[string]any{
			"method": "play",
			"url":    streamURL,
		}
		// Direct delivery: the playlist still comes from us (so URL refresh stays
		// centralised), but its segment URLs point at the CDN and the client pulls
		// the bytes itself — with these headers, which it just told us it can set.
		if hdrs := a.directHeaders(r, tokenMovie, t, s, e); hdrs != nil {
			out["headers"] = hdrs
			out["direct"] = true
		}
		writeJSON(w, http.StatusOK, out)
		return
	}

	// Fallback: browser-resolve failed (timeout / kinomix pre-nav blocked /
	// Phantom CDN refused to issue m3u8 to our server IP). Instead of returning
	// `{}` (which leaves the client with no link), hand back the raw iframe URL
	// so iframe-aware Lampa clients can still play. Plain hls.js clients will
	// fail on HTML, but at least they see an error rather than silence.
	iframeURL := a.allohaIframeURL(tokenMovie, t, s, e)
	if forcePlay || parseBoolParam(q.Get("play")) {
		http.Redirect(w, r, iframeURL, http.StatusFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"method": "iframe",
		"url":    iframeURL,
		"iframe": iframeURL,
	})
}

// proxyStream wraps a CDN m3u8 URL through /proxy/ with the CDN auth headers.
func (a *allohaChecker) proxyStream(r *http.Request, rawURL string, cdnHeaders map[string]string, links *proxylink.Manager) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" || links == nil {
		return rawURL
	}
	if isStreamProxyDisabled("alloha") {
		return rawURL
	}
	return a.forceProxyStream(r, rawURL, cdnHeaders, links)
}

// forceProxyStream wraps a CDN m3u8 URL through /proxy/ unconditionally
// (ignores no_stream_proxy). Used by direct resolve where the browser
// cannot set the correct Origin header for CDN auth.
func (a *allohaChecker) forceProxyStream(r *http.Request, rawURL string, cdnHeaders map[string]string, links *proxylink.Manager) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" || links == nil {
		return rawURL
	}

	// Build headers map: CDN auth + Origin/Referer.
	// CDN expects Origin/Referer from the player host — mirrors the real
	// browser where the iframe player itself issues the CDN requests. Our own
	// affiliate token accepts any referer, so no third-party whitelist needed.
	headers := make(map[string]string)
	maps.Copy(headers, cdnHeaders)
	headers["Origin"] = a.linkHost
	headers["Referer"] = a.linkHost + "/"

	// Log what's being encrypted into proxylink.
	logHdrs := make(map[string]string, len(headers))
	for k, v := range headers {
		if len(v) > 60 {
			logHdrs[k] = v[:60] + "..."
		} else {
			logHdrs[k] = v
		}
	}
	fmt.Printf("[alloha-proxy] forceProxyStream: url=%s headers=%v\n",
		rawURL[:min(len(rawURL), 100)], logHdrs)

	ip := clientIP(r)
	encrypted := links.EncryptURIWithHeaders(rawURL, ip, "alloha", headers)
	if encrypted == "" {
		return rawURL
	}
	return streamHostFromRequest(r) + "/proxy/" + encrypted
}

// ---------- Dynamic HLS stream endpoint ----------

// streamHLS proxies m3u8 playlists and segments from CDN with fresh URLs.
// URL: /lite/alloha/stream.m3u8?token_movie=X&q=auto[&sub=segment.ts]
// allohaBootstrapMu serializes on-demand resolves per token_movie so concurrent
// playlist requests (master → variant) don't each spawn a headless Chrome.
var allohaBootstrapMu sync.Map // stream key → *sync.Mutex

// allohaStreamKey composes the stream-cache key. token_movie ALONE collides
// across a title's translations (t) and a serial's episodes (s/e): they share
// one token_movie but are DIFFERENT streams, so the voice/episode the user
// picked must be part of the key — otherwise a later pick serves the earlier
// cached stream (wrong dub / wrong episode).
func allohaStreamKey(tokenMovie, t string, s, e int) string {
	return tokenMovie + "|t=" + t + "|s=" + strconv.Itoa(s) + "|e=" + strconv.Itoa(e)
}

// allohaEdgeHash returns the live edge_hash of THIS stream's WS session, or ""
// so the caller falls back to the Guard token. It deliberately does NOT borrow
// another stream's hash any more: the hash is per session, and one request
// carrying a foreign hash made the CDN answer 403 to every later request on
// that URL — even with the correct hash (2026-09-02 00:30, 4K «Prada 2»).
func allohaEdgeHash(streamKey, tokenMovie string) string {
	return GetEdgeHash(streamKey, tokenMovie)
}

// bootstrapStreamEntry resolves a stream on demand (headless Chrome) and caches
// it, for /capi deferred playback where the stream URL was handed out without
// resolving. Mirrors the cache-entry build in video(). Returns nil on failure.
func (a *allohaChecker) bootstrapStreamEntry(ctx context.Context, tokenMovie, t string, s, e int, kpID string) *allohaStreamEntry {
	key := allohaStreamKey(tokenMovie, t, s, e)
	muAny, _ := allohaBootstrapMu.LoadOrStore(key, &sync.Mutex{})
	mu := muAny.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()

	// Another request may have resolved while we waited on the lock.
	allohaStreamCacheMu.RLock()
	entry, ok := allohaStreamCache[key]
	allohaStreamCacheMu.RUnlock()
	if ok && entry != nil {
		return entry
	}

	res := allohaResolveViaBrowserFull(ctx, a.linkHost, a.token, tokenMovie, t, s, e, a.keepalive, kpID)
	if res == nil || res.hlsURL == "" {
		return nil
	}
	entry = &allohaStreamEntry{
		quals:         map[string]string{"auto": res.hlsURL},
		cdnHeaders:    res.headers,
		resolvedAt:    time.Now(),
		tokenMovie:    tokenMovie,
		kpID:          kpID,
		cacheKey:      key,
		translation:   t,
		season:        s,
		episode:       e,
		keepopen:      res.headerUpdates != nil,
		refreshNow:    make(chan struct{}, 1),
		moviesURL:     res.moviesURL,
		moviesHeaders: res.moviesHeaders,
		moviesBody:    res.moviesBody,
		browserCtx:    res.browserCtx,
	}
	allohaStreamCacheMu.Lock()
	allohaStreamCache[key] = entry
	allohaStreamCacheMu.Unlock()
	if res.headerUpdates != nil {
		go a.allohaKeepopenLoop(entry, res.headerUpdates)
	} else {
		go a.allohaRefreshLoop(entry)
	}
	return entry
}

func (a *allohaChecker) streamHLS(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tokenMovie := strings.TrimSpace(q.Get("token_movie"))
	quality := strings.TrimSpace(q.Get("q"))
	sub := strings.TrimSpace(q.Get("sub"))
	t := strings.TrimSpace(q.Get("t"))
	s, _ := strconv.Atoi(strings.TrimSpace(q.Get("s")))
	e, _ := strconv.Atoi(strings.TrimSpace(q.Get("e")))
	if quality == "" {
		quality = "auto"
	}
	if tokenMovie == "" {
		http.Error(w, "missing token_movie", http.StatusBadRequest)
		return
	}

	// Key by token_movie + translation + season/episode so a serial's episodes and
	// a title's dubs don't collide on one entry (see allohaStreamKey).
	streamKey := allohaStreamKey(tokenMovie, t, s, e)
	allohaStreamCacheMu.RLock()
	entry, ok := allohaStreamCache[streamKey]
	allohaStreamCacheMu.RUnlock()
	if !ok || entry == nil {
		// /capi deferred playback: the stream URL was handed out without resolving
		// (fast search only). Resolve NOW via headless Chrome, cache it, then serve.
		// A background context lets the resolve finish + cache even if this client
		// gives up on the slow first request — the retry hits the warm cache.
		kpID := strings.TrimSpace(q.Get("kp"))
		bctx, bcancel := context.WithTimeout(context.Background(), 60*time.Second)
		entry = a.bootstrapStreamEntry(bctx, tokenMovie, t, s, e, kpID)
		bcancel()
		if entry == nil {
			http.Error(w, "alloha resolve failed", http.StatusBadGateway)
			return
		}
	}

	entry.mu.RLock()
	baseURL := entry.quals[quality]
	if baseURL == "" {
		// Fallback to any quality.
		for _, u := range entry.quals {
			baseURL = u
			break
		}
	}
	entry.mu.RUnlock()

	if baseURL == "" {
		http.Error(w, "quality not found", http.StatusNotFound)
		return
	}

	// Build target URL: base m3u8 or sub-resource relative to base path.
	// CDN segment URLs are relative to the base directory (e.g. https://cdn/0/TOKEN/seg-N.ts).
	// Auth is entirely via headers (Borth, Authorizations, Accepts-Controls).
	//
	// IMPORTANT: For absolute sub URLs we always rebuild from the fresh baseURL.
	// The CDN token embedded in the absolute path expires in ~70s.
	// entry.quals[quality] (= baseURL) is updated by the refresh loop every 15s,
	// so rebuilding from baseURL always uses a fresh token.
	// Using the stale absolute URL directly causes 403 at ~seg-36 (70s ÷ 2s/seg).
	targetURL := baseURL
	if sub != "" {
		if strings.HasPrefix(sub, "https://") || strings.HasPrefix(sub, "http://") {
			// Absolute sub URL: extract the segment filename and rebuild from fresh baseURL.
			segFile := sub
			if lastSlash := strings.LastIndex(sub, "/"); lastSlash >= 0 {
				segFile = sub[lastSlash+1:]
			}
			idx := strings.LastIndex(baseURL, "/")
			if idx > 0 && segFile != sub {
				targetURL = baseURL[:idx+1] + segFile
			} else {
				// Can't extract filename (unusual) — fall back to absolute sub.
				targetURL = sub
			}
		} else {
			idx := strings.LastIndex(baseURL, "/")
			if idx > 0 {
				targetURL = baseURL[:idx+1] + sub
			}
		}
	}

	isSegment := sub != "" &&
		!strings.HasSuffix(strings.ToLower(sub), ".m3u8") &&
		!strings.HasSuffix(strings.ToLower(sub), ".m3u")

	// CORS.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Range, Content-Type")
	w.Header().Set("Access-Control-Expose-Headers", "Content-Length, Content-Range")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method == "HEAD" {
		// Content-Type MUST match the resource kind. Shaka HEADs a segment to sniff
		// its MIME before appending to MSE; if we answer "application/vnd.apple.mpegurl"
		// for a .m4s/.mp4 segment it treats the segment as a nested HLS playlist,
		// never appends it, and buffers forever (no picture/sound). Only real .m3u8
		// sub-playlists (and the base manifest) are the HLS type.
		ct := "application/vnd.apple.mpegurl"
		if isSegment {
			low := strings.ToLower(sub)
			switch {
			case strings.HasSuffix(low, ".ts"):
				ct = "video/mp2t"
			case strings.HasSuffix(low, ".m4a"), strings.HasSuffix(low, ".mp3"), strings.HasSuffix(low, ".aac"):
				ct = "audio/mp4"
			default: // .m4s / .mp4 (fragmented MP4 segment or init)
				ct = "video/mp4"
			}
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Accept-Ranges", "bytes")
		w.WriteHeader(http.StatusOK)
		return
	}

	// Spectre current_time tracking: update WS heartbeat position from segment URL.
	// Same CDN as Mirage — shared WS edge_hash, shared current_time.
	if isSegment {
		mirageUpdateCurrentTimeForKeys(targetURL, streamKey, tokenMovie)
	}

	// Segment throttle: prevent burst downloads that trigger CDN rate limits —
	// but only real bursts. A player refilling its buffer at up to
	// allohaSegBurst segments, or fetching just-in-time, must not pay latency.
	if isSegment {
		if wait := allohaTakeSegmentToken(entry, time.Now()); wait > 0 {
			time.Sleep(wait)
		}
		atomic.StoreInt64(&entry.lastSegmentAt, time.Now().UnixNano())
	}

	// NOTE: browser fetch in player tab was tested but unreliable (context timeouts).
	// Using Go HTTP with Guard token + no-Borth on segments + throttle instead.

	// Build CDN request (Go HTTP fallback).
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, targetURL, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	req.Header.Set("User-Agent", r.Header.Get("User-Agent"))
	if ua := req.Header.Get("User-Agent"); ua == "" {
		req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	}
	// Use same Origin/Referer as working iframe mode (servePlayerProxy):
	// the player host itself, like a real in-iframe player would send.
	req.Header.Set("Origin", a.linkHost)
	req.Header.Set("Referer", a.linkHost+"/")
	req.Header.Set("Accept", "*/*")

	// CDN auth headers from browser resolve (Borth, Authorizations, Accepts-Controls).
	entry.mu.RLock()
	for k, v := range entry.cdnHeaders {
		req.Header.Set(k, v)
	}
	entry.mu.RUnlock()

	// Override Authorizations with static Bearer (same value as kinomix/Spectre).
	req.Header.Set("Authorizations", mirageStaticBearer)

	// Override Accepts-Controls with the best available token.
	// Priority (Spectre-matching): WS edge_hash > Guard token > browser value.
	// pc_hash dropped to match Spectre Service.cs:128 exactly — only Accepts-Controls.
	if liveHash := allohaEdgeHash(streamKey, tokenMovie); liveHash != "" {
		req.Header.Set("Accepts-Controls", liveHash)
	} else {
		// No live WS anywhere: nothing rotates edge_hash, so this session is on
		// borrowed time (measured lifetime ~6-9 min before the CDN 403s it).
		// A re-resolve refreshes the signed URL — but it can NO LONGER restart the
		// WS, because /bnsi/movies/ stopped carrying wsUrl/sid. So throttle hard:
		// unthrottled, every segment queued a browser resolve and the Chrome pool
		// (8 slots) starved, which 403/502'd playback outright.
		const noWSSignalEvery = int64(2 * time.Minute)
		now := time.Now().UnixNano()
		if last := atomic.LoadInt64(&entry.lastNoWSSignalAt); now-last > noWSSignalEvery &&
			atomic.CompareAndSwapInt64(&entry.lastNoWSSignalAt, last, now) {
			select {
			case entry.refreshNow <- struct{}{}:
				fmt.Printf("[alloha-stream] no live edge_hash — requested URL refresh (throttled)\n")
			default:
			}
		}
		if guardTok := GetGuardToken(a.linkHost); guardTok != "" {
			req.Header.Set("Accepts-Controls", guardTok)
		}
	}
	req.Header.Del("pc_hash")

	// Spectre/kinomix: Borth is NOT sent on segments, only on manifests.
	// Authorizations IS sent on both (static Bearer).
	if isSegment {
		req.Header.Del("Borth")
	}

	borthVal := req.Header.Get("Borth")
	acVal := req.Header.Get("Accepts-Controls")
	fmt.Printf("[alloha-stream] req: borth=%s... ac=%s... sub=%q segment=%v\n",
		borthVal[:min(len(borthVal), 32)], acVal[:min(len(acVal), 32)], sub, isSegment)

	// Forward Range header for segments.
	if rng := r.Header.Get("Range"); rng != "" {
		req.Header.Set("Range", rng)
	}

	// Diagnostic for the direct-delivery work: emit the FULL outgoing auth next
	// to the exact URL it belongs to, in one line. Harvesting these separately
	// from the log kept pairing a fresh URL with a stale Accepts-Controls, which
	// looked like "the CDN rejects other IPs" when the request was simply unsigned.
	// Manifests only: a per-segment line would dump a ~2KB signed URL for every
	// fragment of every stream.
	if !isSegment {
		fmt.Printf("[alloha-cdnauth] ac=%s ref=%s origin=%s auth=%v url=%s\n",
			req.Header.Get("Accepts-Controls"), req.Header.Get("Referer"),
			req.Header.Get("Origin"), req.Header.Get("Authorizations") != "", req.URL.String())
	}

	client := getPlayerProxyClient()
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("[alloha-stream] fetch error: %v\n", err)
		http.Error(w, "cdn fetch: "+err.Error(), http.StatusBadGateway)
		return
	}

	// Quick retry for TRANSIENT garbage: the CDN intermittently answers a segment
	// with 200 + a small JSON error instead of the .ts (seen interspersed with
	// good segments — a transient hiccup / rate-limit, not a stale token).
	// Re-fetching the same URL a couple of times usually returns the real
	// segment; only persistent garbage falls through to the re-resolve below.
	for gi := 0; isSegment && gi < 3 &&
		resp.StatusCode >= 200 && resp.StatusCode < 300 &&
		allohaNonVideoContentType(resp.Header.Get("Content-Type")); gi++ {
		resp.Body.Close()
		fmt.Printf("[alloha-stream] transient garbage (ct=%q) quick-retry %d sub=%s\n", resp.Header.Get("Content-Type"), gi+1, sub[:min(len(sub), 60)])
		time.Sleep(300 * time.Millisecond)
		rr, rerr := http.NewRequestWithContext(r.Context(), http.MethodGet, targetURL, nil)
		if rerr != nil {
			break
		}
		rr.Header = req.Header.Clone()
		if liveHash := allohaEdgeHash(streamKey, tokenMovie); liveHash != "" {
			rr.Header.Set("Accepts-Controls", liveHash)
		}
		nresp, nerr := client.Do(rr)
		if nerr != nil {
			break
		}
		resp = nresp
	}

	// On 403 (CDN auth expired) OR a "garbage segment" — the CDN sometimes returns
	// 200 + a small JSON error body instead of the .ts when a per-segment token
	// goes stale (common on slow connections, where the player lags behind the
	// playlist token TTL). Forwarding that JSON makes hls.js demux garbage →
	// stutter / decode error. Both cases trigger a re-resolve for a fresh URL.
	garbageSeg := isSegment && resp.StatusCode >= 200 && resp.StatusCode < 300 &&
		allohaNonVideoContentType(resp.Header.Get("Content-Type"))
	if resp.StatusCode == http.StatusForbidden || garbageSeg {
		failedStatus := resp.StatusCode
		failedCT := resp.Header.Get("Content-Type")
		resp.Body.Close()
		// Drop the reference: its body is now closed, so it must never reach
		// allohaForward. Leaving it non-nil made the "URL unchanged" fallback
		// below unreachable dead code, and the dead response was forwarded to
		// the player — a 403 manifest with a 1-byte body, or a 502 segment.
		resp = nil
		if garbageSeg {
			fmt.Printf("[alloha-stream] garbage segment ct=%q → re-resolve sub=%s\n", failedCT, sub[:min(len(sub), 80)])
		} else {
			fmt.Printf("[alloha-stream] 403 on targetURL=%s sub=%s\n", targetURL[:min(len(targetURL), 120)], sub[:min(len(sub), 80)])
		}

		// Quick same-URL retry: Phantom CDN routinely 403s the FIRST fetch right after
		// a fresh resolve (edge_hash not yet propagated to the edge) then accepts the
		// IDENTICAL URL a moment later. A few cheap retries with the freshest edge_hash
		// catch that before the expensive 40s re-resolve wait below — which the refresh
		// "too fresh" cooldown can skip, leaving the client with a hard 403.
		if failedStatus == http.StatusForbidden {
			for qi := 0; qi < 3; qi++ {
				time.Sleep(700 * time.Millisecond)
				rq, rqe := http.NewRequestWithContext(r.Context(), http.MethodGet, targetURL, nil)
				if rqe != nil {
					break
				}
				rq.Header = req.Header.Clone()
				if liveHash := allohaEdgeHash(streamKey, tokenMovie); liveHash != "" {
					rq.Header.Set("Accepts-Controls", liveHash)
				}
				rr, rre := client.Do(rq)
				if rre == nil && rr.StatusCode >= 200 && rr.StatusCode < 300 {
					fmt.Printf("[alloha-stream] 403 healed by quick same-URL retry #%d\n", qi+1)
					resp = rr
					goto allohaForward
				}
				if rr != nil {
					rr.Body.Close()
				}
			}
		}

		// Record timestamp before signaling so we can detect when entry is refreshed.
		entry.mu.RLock()
		was403At := entry.resolvedAt
		entry.mu.RUnlock()

		// Signal background refresh to run immediately.
		select {
		case entry.refreshNow <- struct{}{}:
		default:
		}

		// Wait for the background refresh to provide fresh Borth/headers.
		// We check both URL change AND resolvedAt change — if only headers changed
		// (e.g. edge_hash rotated but URL token still valid), we still retry.
		//
		// The budget is deliberately short for SEGMENTS. A player asks for the next
		// segment while its buffer drains; holding that request for 40s (plus the
		// pre-warm below, which pushed real requests past 90s) guarantees a stall and
		// wastes the work, since the client has long since cancelled — and r.Context()
		// cancellation then fails every retry we make. Failing fast lets the player
		// re-request; the refresh keeps running in the background either way, so the
		// retry a few seconds later hits a healed entry.
		waitTicks := 20 // 10s — segments
		if !isSegment {
			waitTicks = 50 // 25s — manifests: no buffer draining, worth waiting out a resolve
		}
		for i := 0; i < waitTicks; i++ {
			if r.Context().Err() != nil {
				// Client gave up; the background refresh continues without us.
				fmt.Printf("[alloha-stream] client cancelled while awaiting refresh\n")
				return
			}
			time.Sleep(500 * time.Millisecond)
			entry.mu.RLock()
			newBase := entry.quals[quality]
			if newBase == "" {
				for _, u := range entry.quals {
					newBase = u
					break
				}
			}
			freshAt := entry.resolvedAt
			entry.mu.RUnlock()
			if newBase != baseURL || freshAt.After(was403At) {
				// Cache has a fresh URL.
				// CDN requires master m3u8 + variant playlists fetched before segments.
				// Extract segment filename for URL reconstruction (segment token differs from master token).
				segFile := sub
				if strings.HasPrefix(sub, "http://") || strings.HasPrefix(sub, "https://") {
					if lastSlash := strings.LastIndex(sub, "/"); lastSlash >= 0 {
						segFile = sub[lastSlash+1:]
					}
				}
				var newSegmentURL string

				if sub != "" {
					entry.mu.RLock()
					freshHdrs := make(map[string]string, len(entry.cdnHeaders))
					for k, v := range entry.cdnHeaders {
						freshHdrs[k] = v
					}
					entry.mu.RUnlock()

					// Bound the whole pre-warm phase. Warming master + every variant
					// serially on a 30s-timeout client is what turned a single failed
					// segment into a 90s+ hung request; the player is waiting on this.
					warmCtx, warmCancel := context.WithTimeout(r.Context(), 8*time.Second)
					defer warmCancel()

					doStreamWarm := func(u string) ([]byte, bool) {
						warmReq, werr := http.NewRequestWithContext(warmCtx, http.MethodGet, u, nil)
						if werr != nil {
							return nil, false
						}
						warmReq.Header.Set("Origin", a.linkHost)
						warmReq.Header.Set("Referer", a.linkHost+"/")
						warmReq.Header.Set("Accept", "*/*")
						for k, v := range freshHdrs {
							warmReq.Header.Set(k, v)
						}
						// Same Accepts-Controls priority as the real fetch below
						// (edge_hash first). This used to prefer the Guard token,
						// so the CDN could see the playlist warmed under one token
						// and the segment requested under another.
						if liveHash := allohaEdgeHash(streamKey, tokenMovie); liveHash != "" {
							warmReq.Header.Set("Accepts-Controls", liveHash)
						} else if guardTok := GetGuardToken(a.linkHost); guardTok != "" {
							warmReq.Header.Set("Accepts-Controls", guardTok)
						}
						warmReq.Header.Del("pc_hash")
						wr, werr2 := client.Do(warmReq)
						if werr2 != nil {
							return nil, false
						}
						body, _ := io.ReadAll(io.LimitReader(wr.Body, 512*1024))
						wr.Body.Close()
						fmt.Printf("[alloha-stream] pre-warm before retry: %d\n", wr.StatusCode)
						return body, wr.StatusCode == 200
					}

					// Warm master.m3u8 then each variant playlist.
					// While warming variants, find the fresh segment URL that matches segFile.
					masterBody, ok := doStreamWarm(newBase)
					if ok {
						warmBase := newBase
						if idx := strings.LastIndex(warmBase, "/"); idx > 0 {
							warmBase = warmBase[:idx+1]
						}
						for _, line := range strings.Split(string(masterBody), "\n") {
							line = strings.TrimSpace(line)
							if line == "" || strings.HasPrefix(line, "#") {
								continue
							}
							variantURL := line
							if !strings.HasPrefix(line, "http") {
								variantURL = warmBase + line
							}
							variantBody, _ := doStreamWarm(variantURL)
							// Search variant for a line ending with the segment filename.
							// Needed because variant segment URLs have a different CDN token than master.
							if newSegmentURL == "" && segFile != sub {
								for _, vl := range strings.Split(string(variantBody), "\n") {
									vl = strings.TrimSpace(vl)
									if strings.HasSuffix(vl, segFile) {
										newSegmentURL = vl
										break
									}
								}
							}
							if warmCtx.Err() != nil {
								break
							}
						}
					}
				}

				// Rebuild target URL and retry.
				// If sub was an absolute CDN URL with an expired token, use the fresh
				// segment URL found in the new variant playlist instead.
				retryURL := newBase
				if sub != "" {
					if newSegmentURL != "" {
						// Fresh absolute segment URL from newly warmed variant playlist.
						retryURL = newSegmentURL
					} else if strings.HasPrefix(sub, "https://") || strings.HasPrefix(sub, "http://") {
						retryURL = sub
					} else {
						idx := strings.LastIndex(newBase, "/")
						if idx > 0 {
							retryURL = newBase[:idx+1] + sub
						}
					}
				}
				fmt.Printf("[alloha-stream] retrying with fresh URL: %s\n", retryURL[:min(len(retryURL), 80)])

				req2, err2 := http.NewRequestWithContext(r.Context(), http.MethodGet, retryURL, nil)
				if err2 != nil {
					http.Error(w, "retry: "+err2.Error(), http.StatusBadGateway)
					return
				}
				// Use FRESH auth headers from the updated entry.
				req2.Header.Set("User-Agent", req.Header.Get("User-Agent"))
				req2.Header.Set("Origin", a.linkHost)
				req2.Header.Set("Referer", a.linkHost+"/")
				req2.Header.Set("Accept", "*/*")
				entry.mu.RLock()
				for k, v := range entry.cdnHeaders {
					req2.Header.Set(k, v)
				}
				entry.mu.RUnlock()
				// Same Accepts-Controls priority as the original request
				// (edge_hash first, Guard token as fallback) — the pre-warm above
				// and this retry must present the CDN one consistent token.
				if liveHash := allohaEdgeHash(streamKey, tokenMovie); liveHash != "" {
					req2.Header.Set("Accepts-Controls", liveHash)
				} else if guardTok := GetGuardToken(a.linkHost); guardTok != "" {
					req2.Header.Set("Accepts-Controls", guardTok)
				}
				req2.Header.Del("pc_hash")
				// Browser fetchSetup strips Borth/Authorizations from segment requests.
				if sub != "" && !strings.HasSuffix(sub, ".m3u8") {
					req2.Header.Del("Borth")
					req2.Header.Del("Authorizations")
					req2.Header.Del("Authorization")
				}
				if rng := r.Header.Get("Range"); rng != "" {
					req2.Header.Set("Range", rng)
				}
				b2 := req2.Header.Get("Borth")
				ac2 := req2.Header.Get("Accepts-Controls")
				fmt.Printf("[alloha-stream] retry: borth=%s... ac=%s... url=%s\n",
					b2[:min(len(b2), 32)], ac2[:min(len(ac2), 32)], retryURL[:min(len(retryURL), 80)])
				resp, err = client.Do(req2)
				if err != nil {
					http.Error(w, "retry fetch: "+err.Error(), http.StatusBadGateway)
					return
				}
				targetURL = retryURL
				break
			}
		}
		// The refresh didn't publish a new URL within our wait budget (or the CDN
		// re-issued the same token for this session). Still retry once with FRESH
		// headers from the entry — the refresh may have updated Borth /
		// Accepts-Controls even when the URL token stayed the same. Reaching this
		// with a real request beats forwarding the dead 403 we started with.
		if resp == nil {
			retryURL2 := targetURL
			entry.mu.RLock()
			curBase := entry.quals[quality]
			if curBase == "" {
				for _, u := range entry.quals {
					curBase = u
					break
				}
			}
			freshHdrs2 := make(map[string]string, len(entry.cdnHeaders))
			for k, v := range entry.cdnHeaders {
				freshHdrs2[k] = v
			}
			entry.mu.RUnlock()
			// Reconstruct segment URL using the current base if sub is relative.
			if sub != "" && !strings.HasPrefix(sub, "https://") && !strings.HasPrefix(sub, "http://") {
				if curBase != "" {
					if idx := strings.LastIndex(curBase, "/"); idx > 0 {
						retryURL2 = curBase[:idx+1] + sub
					}
				}
			}
			fmt.Printf("[alloha-stream] URL unchanged fallback retry: %s\n", retryURL2[:min(len(retryURL2), 80)])
			req2fb, err2fb := http.NewRequestWithContext(r.Context(), http.MethodGet, retryURL2, nil)
			if err2fb != nil {
				http.Error(w, "cdn fetch: "+err2fb.Error(), http.StatusBadGateway)
				return
			}
			req2fb.Header.Set("User-Agent", req.Header.Get("User-Agent"))
			req2fb.Header.Set("Origin", a.linkHost)
			req2fb.Header.Set("Referer", a.linkHost+"/")
			req2fb.Header.Set("Accept", "*/*")
			for k, v := range freshHdrs2 {
				req2fb.Header.Set(k, v)
			}
			// Apply live WS edge_hash for AC on fallback retry, Guard token if the
			// WS is gone. pc_hash dropped — Spectre Service.cs:128 doesn't send it.
			if liveHash := allohaEdgeHash(streamKey, tokenMovie); liveHash != "" {
				req2fb.Header.Set("Accepts-Controls", liveHash)
			} else if guardTok := GetGuardToken(a.linkHost); guardTok != "" {
				req2fb.Header.Set("Accepts-Controls", guardTok)
			}
			req2fb.Header.Del("pc_hash")
			if rng := r.Header.Get("Range"); rng != "" {
				req2fb.Header.Set("Range", rng)
			}
			resp, err = client.Do(req2fb)
			if err != nil {
				http.Error(w, "cdn fetch: "+err.Error(), http.StatusBadGateway)
				return
			}
		}
	}
allohaForward:
	defer resp.Body.Close()

	// For m3u8: read body, rewrite URLs, return.
	ct := resp.Header.Get("Content-Type")
	if !isSegment && (strings.Contains(ct, "mpegurl") || strings.Contains(ct, "m3u") || strings.HasSuffix(strings.ToLower(sub), ".m3u8") || sub == "") {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
		rewritten := a.allohaRewriteM3U(string(body), r, tokenMovie, quality, req.URL.String())
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(resp.StatusCode)
		w.Write([]byte(rewritten))
		return
	}

	// Backstop: never forward a non-video body as a segment. If the re-resolve +
	// retry above still yielded garbage (JSON/HTML/text error), return 502 so
	// hls.js retries the fragment instead of demuxing garbage into the decoder
	// (which is what surfaced as mid-playback "decode error" / stutter).
	if isSegment && allohaNonVideoContentType(resp.Header.Get("Content-Type")) {
		fmt.Printf("[alloha-stream] still garbage after retry (ct=%q) → 502\n", resp.Header.Get("Content-Type"))
		http.Error(w, "garbage segment", http.StatusBadGateway)
		return
	}

	// For segments: stream through.
	for _, h := range []string{"Content-Type", "Content-Length", "Content-Range",
		"Accept-Ranges", "Cache-Control", "ETag"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// allohaNonVideoContentType reports whether a Content-Type indicates a non-video
// payload (JSON/HTML/text) — i.e. a CDN error body returned with 200 instead of
// the real .ts segment. Empty Content-Type is treated as video so segments that
// arrive without one are not dropped.
func allohaNonVideoContentType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if ct == "" {
		return false
	}
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return strings.Contains(ct, "json") ||
		strings.Contains(ct, "html") ||
		strings.HasPrefix(ct, "text/")
}

// allohaRewriteM3U rewrites all sub-URLs in an m3u8 playlist to route through
// our /lite/alloha/stream.m3u8?sub= endpoint.
const (
	// allohaSegInterval is the steady-state segment rate the CDN tolerates
	// (segments are 6 s, so this is still 3× realtime).
	allohaSegInterval = 2 * time.Second
	// allohaSegBurst is how many segments may be fetched back-to-back before
	// the rate applies — enough for a player to prime one buffer window.
	allohaSegBurst = 3.0
)

// allohaTakeSegmentToken implements the per-stream token bucket described on
// the entry fields. It returns how long the caller must sleep before fetching
// (0 = go now). Callers that have to wait consume the token that accrues during
// the wait, so concurrent fetches queue at the steady rate instead of firing
// together when the sleep ends.
func allohaTakeSegmentToken(e *allohaStreamEntry, now time.Time) time.Duration {
	e.segMu.Lock()
	defer e.segMu.Unlock()
	if e.segLast.IsZero() {
		e.segTokens = allohaSegBurst
		e.segLast = now
	}
	if el := now.Sub(e.segLast); el > 0 {
		e.segTokens += el.Seconds() / allohaSegInterval.Seconds()
		if e.segTokens > allohaSegBurst {
			e.segTokens = allohaSegBurst
		}
		e.segLast = now
	}
	if e.segTokens >= 1 {
		e.segTokens--
		return 0
	}
	// Reserve the next token: it becomes available (1-tokens) intervals after
	// the last accounting point, which may already lie in the future when
	// earlier callers are queued — so concurrent fetches line up one interval
	// apart instead of all waking at once.
	readyAt := e.segLast.Add(time.Duration((1 - e.segTokens) * float64(allohaSegInterval)))
	e.segTokens = 0
	e.segLast = readyAt
	wait := readyAt.Sub(now)
	if wait < 0 {
		wait = 0
	}
	return wait
}

func (a *allohaChecker) allohaRewriteM3U(src string, r *http.Request, tokenMovie, quality, upstreamURL string) string {
	host := hostFromRequest(r)
	streamBase := host + "/lite/alloha/stream.m3u8?token_movie=" + url.QueryEscape(tokenMovie) + "&q=" + url.QueryEscape(quality)
	// Carry t/s/e onto every rewritten variant/segment URL so each fetch computes
	// the SAME cache key (allohaStreamKey) as the master. Without this the variant
	// and segment fetches miss the cache (keyed by token_movie|t|s|e) and re-bootstrap
	// a headless Chrome resolve per request → endless failing stream.m3u8 fetches.
	q := r.URL.Query()
	if t := strings.TrimSpace(q.Get("t")); t != "" {
		streamBase += "&t=" + url.QueryEscape(t)
	}
	if s := strings.TrimSpace(q.Get("s")); s != "" && s != "0" {
		streamBase += "&s=" + url.QueryEscape(s)
		if e := strings.TrimSpace(q.Get("e")); e != "" {
			streamBase += "&e=" + url.QueryEscape(e)
		}
	}

	// The manifests we hand back point at ourselves, so the client's NEXT hop must
	// stay in direct mode too — otherwise the media playlist comes back fully
	// proxied and the segments never leave our pipe.
	if a.directRequested(r) {
		streamBase += "&direct=1"
	}

	// Direct mode: hand out the CDN's own URLs so the player fetches media
	// itself. Relative entries are resolved against the upstream playlist, since
	// the client has no other way to know the CDN base.
	direct := a.directRequested(r)
	var cdnBase string
	if direct {
		if u, err := url.Parse(upstreamURL); err == nil && u.Host != "" {
			u.RawQuery = ""
			cdnBase = strings.TrimSuffix(u.String(), path.Base(u.Path))
		} else {
			direct = false // no base to resolve against — stay on the proxy path
		}
	}
	abs := func(ref string) string {
		if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
			return ref
		}
		return cdnBase + strings.TrimPrefix(ref, "/")
	}

	var out strings.Builder
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)

		// Comments/tags pass through, except URI= attributes.
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			// Rewrite URI="..." in tags like #EXT-X-MEDIA.
			if strings.Contains(trimmed, "URI=\"") {
				// URI= points at a rendition playlist — a manifest, so proxied
				// in both modes.
				trimmed = allohaRewriteURIAttr(trimmed, streamBase)
			}
			out.WriteString(trimmed)
			out.WriteByte('\n')
			continue
		}

		// Direct mode covers SEGMENTS only. Manifests still go through us: the CDN
		// demands Borth on .m3u8 requests (and only on those — see the header
		// comment further down), and Borth is minted per player session, so a
		// client could never produce one. Manifests are kilobytes anyway; the
		// megabytes are the segments, and those now leave our pipe entirely.
		if direct && !allohaIsManifestRef(trimmed) {
			out.WriteString(abs(trimmed))
			out.WriteByte('\n')
			continue
		}

		// Absolute https:// URLs → route through our endpoint.
		if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
			// Extract relative path from absolute URL.
			// We can't use it directly — route through &sub= so we control headers.
			out.WriteString(streamBase + "&sub=" + url.QueryEscape(trimmed))
			out.WriteByte('\n')
			continue
		}

		// Relative URL → route through &sub=.
		out.WriteString(streamBase + "&sub=" + url.QueryEscape(trimmed))
		out.WriteByte('\n')
	}
	return out.String()
}

// allohaRewriteURIAttr rewrites URI="..." attribute values in HLS tags.
func allohaRewriteURIAttr(line, streamBase string) string {
	const marker = "URI=\""
	idx := strings.Index(line, marker)
	if idx < 0 {
		return line
	}
	start := idx + len(marker)
	end := strings.Index(line[start:], "\"")
	if end < 0 {
		return line
	}
	uri := line[start : start+end]
	return line[:start] + streamBase + "&sub=" + url.QueryEscape(uri) + line[start+end:]
}

// allohaEvictStreamEntry drops an entry from the cache so the next request
// bootstraps a fresh one. Without this a loop that gives up (entry too old,
// browser tab gone) leaves a zombie behind: streamHLS still cache-HITs it, serves
// its long-dead URL, and nothing is left running to ever refresh it — that
// token_movie/dub/episode is then broken until the process restarts. It also
// bounded nothing, so the map grew for the lifetime of the process.
func allohaEvictStreamEntry(entry *allohaStreamEntry) {
	if entry == nil || entry.cacheKey == "" {
		return
	}
	allohaStreamCacheMu.Lock()
	if cur, ok := allohaStreamCache[entry.cacheKey]; ok && cur == entry {
		delete(allohaStreamCache, entry.cacheKey)
	}
	allohaStreamCacheMu.Unlock()
}

// refreshEntryOnce re-resolves this entry's stream and, if the fresh URL warms
// up, swaps it in. Returns false when the entry was left untouched.
//
// The resolve REPLAYS the entry's translation/season/episode. Resolving by
// token_movie alone returns the default dub and S1E1, i.e. a different stream
// than the one being played — its segment paths don't line up with what the
// player asks for, so "recovery" turned a temporary 403 into a permanent one.
func (a *allohaChecker) refreshEntryOnce(entry *allohaStreamEntry) bool {
	entry.mu.RLock()
	tm := entry.tokenMovie
	kp := entry.kpID
	tr := entry.translation
	season := entry.season
	episode := entry.episode
	age := time.Since(entry.resolvedAt)
	entry.mu.RUnlock()

	// Cheapest path first: REPLAY the /bnsi/movies/ POST exactly as the browser
	// made it. The entry already carries that request (url+headers+body) — it was
	// captured for this purpose but never used, so every refresh fell through to
	// Chrome (measured: 162 direct failures / 175 browser resolves per 10 min).
	//
	// This beats allohaDirectResolve because Borth CANNOT be rebuilt server-side:
	// it's {edge_hash}|base64(sid|ts|innerToken) assembled by the player JS, not
	// the raw <meta viewporti> the direct path sends — which is why that path
	// answered 404 «контент не найден» 1415 times in two hours.
	fmt.Printf("[alloha-refresh] re-resolving (age=%s t=%q s=%d e=%d)...\n",
		age.Truncate(time.Second), tr, season, episode)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	hlsURL, hdrs, ok := a.allohaReplayMovies(ctx, entry)
	if !ok || hlsURL == "" {
		hlsURL, hdrs, ok = a.allohaDirectResolve(ctx, tm, tr, season, episode)
	}
	cancel()

	if !ok || hlsURL == "" {
		fmt.Printf("[alloha-refresh] direct resolve failed, trying browser...\n")
		ctx2, cancel2 := context.WithTimeout(context.Background(), 60*time.Second)
		hlsURL, hdrs, ok = allohaResolveViaBrowserKP(ctx2, a.linkHost, a.token, tm, tr, season, episode, kp)
		cancel2()
		if !ok || hlsURL == "" {
			fmt.Printf("[alloha-refresh] browser re-resolve also failed\n")
			return false
		}
	}

	fmt.Printf("[alloha-refresh] resolved: %s\n", hlsURL[:min(len(hlsURL), 80)])

	// Pre-warm the CDN SYNCHRONOUSLY before updating entry.
	// CDN requires master.m3u8 + variant playlists fetched before segments.
	// If pre-warm fails (500), DON'T update entry — keep the old working URL.
	// Previously entry was updated before pre-warm (async), causing the
	// player to get a 500-node URL and fail.
	client := getPlayerProxyClient()
	linkHost := a.linkHost

	doWarm := func(u string) ([]byte, int) {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, u, nil)
		if err != nil {
			return nil, 0
		}
		req.Header.Set("Origin", linkHost)
		req.Header.Set("Referer", linkHost+"/")
		req.Header.Set("Accept", "*/*")
		for k, v := range hdrs {
			req.Header.Set(k, v)
		}
		req.Header.Set("Authorizations", mirageStaticBearer)
		// pc_hash dropped — Spectre Service.cs:128 doesn't send it.
		if liveHash := allohaEdgeHash(entry.cacheKey, tm); liveHash != "" {
			req.Header.Set("Accepts-Controls", liveHash)
		} else if guardTok := GetGuardToken(linkHost); guardTok != "" {
			req.Header.Set("Accepts-Controls", guardTok)
		}
		req.Header.Del("pc_hash")
		resp, err := client.Do(req)
		if err != nil {
			fmt.Printf("[alloha-refresh] pre-warm error %s: %v\n", u[:min(len(u), 40)], err)
			return nil, 0
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
		resp.Body.Close()
		return body, resp.StatusCode
	}

	body, code := doWarm(hlsURL)
	fmt.Printf("[alloha-refresh] pre-warm master: %d\n", code)
	if code != 200 || len(body) == 0 {
		// CDN node is broken (500) — don't update entry, try again next cycle.
		fmt.Printf("[alloha-refresh] pre-warm FAILED (%d), keeping old URL\n", code)
		return false
	}

	// Parse master.m3u8 to find variant playlist URLs and warm them too.
	base := hlsURL
	if idx := strings.LastIndex(base, "/"); idx > 0 {
		base = base[:idx+1]
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		variantURL := line
		if !strings.HasPrefix(line, "http") {
			variantURL = base + line
		}
		_, vCode := doWarm(variantURL)
		fmt.Printf("[alloha-refresh] pre-warm variant %s: %d\n", line, vCode)
	}

	// All pre-warms succeeded — NOW update entry with fresh URL.
	entry.mu.Lock()
	// Every quality key pointed at the stream we just replaced; leaving any of
	// them would keep serving the dead URL (e.g. to ?q=2160p requests).
	for k := range entry.quals {
		entry.quals[k] = hlsURL
	}
	entry.quals["auto"] = hlsURL
	entry.cdnHeaders = hdrs
	entry.resolvedAt = time.Now()
	entry.mu.Unlock()

	fmt.Printf("[alloha-refresh] refreshed: %s\n", hlsURL[:min(len(hlsURL), 80)])
	return true
}

// allohaRefreshLoop keeps one stream entry alive: on a timer, and reactively
// whenever a request hits a CDN 403 (entry.refreshNow).
func (a *allohaChecker) allohaRefreshLoop(entry *allohaStreamEntry) {
	// CDN URL tokens live much longer than 3 min when WS edge_hash keeps
	// rotating (Accepts-Controls is refreshed every ~2 min by the WS client).
	// The old 3-min interval caused playlist sbrос to seg-1 every cycle, and
	// hls.js treated it as a stream reset → stopped after ~4 min.
	// kinomix plays for hours on a single URL with WS edge_hash — proving
	// the URL itself doesn't expire that fast.
	// Set to 30 min (same as Mirage). On real 403, entry.refreshNow triggers
	// immediate re-resolve reactively.
	const refreshInterval = 30 * time.Minute
	// …but that whole argument rests on the WS being there. Alloha stopped
	// shipping wsUrl/sid in /bnsi/movies/ (2026-08-03: the body now carries only
	// hlsSource/tracks/skipTime/…), so no session ever starts, Accepts-Controls
	// never rotates, and a measured stream dies between the 6th and 9th minute —
	// «играет 6 минут и отваливается». While no edge_hash is available, refresh
	// AHEAD of that expiry instead of napping for 30 minutes. Cheap path first:
	// refreshEntryOnce tries the direct (no-Chrome) resolve before the browser.
	const noEdgeHashRefreshInterval = 5 * time.Minute
	const maxAge = 2 * time.Hour
	// Cooldowns guard against back-to-back browser sessions. The reactive one is
	// short on purpose: a 403 is PROOF the current URL is dead, so "we resolved
	// recently" is the wrong reason to skip — the old 30s gate silently swallowed
	// the signal and left the waiting request to stall for its full timeout.
	const minCooldown = 30 * time.Second
	const reactiveCooldown = 5 * time.Second
	// A timer refresh is only worth its cost while someone is actually pulling
	// segments. Every entry lingers in allohaStreamCache long after its viewer
	// left, and with the direct (no-Chrome) resolve failing in practice, each
	// tick spends a Chrome slot: on production that was 139 refreshes / 175
	// browser resolves per 10 min across the cache, starving the 8-slot pool and
	// 403ing the streams that WERE being watched. Reactive (403) refreshes are
	// never skipped — those come from a live request.
	const idleAfter = 3 * time.Minute

	for {
		reactive := false
		interval := refreshInterval
		if allohaEdgeHash(allohaStreamKey(entry.tokenMovie, entry.translation, entry.season, entry.episode), entry.tokenMovie) == "" {
			interval = noEdgeHashRefreshInterval
		}
		select {
		case <-time.After(interval):
		case <-entry.refreshNow:
			// Immediate refresh requested (e.g. after 403).
			reactive = true
			// Drain any duplicate signals.
		drain:
			for {
				select {
				case <-entry.refreshNow:
				default:
					break drain
				}
			}
		}

		// Idle gate: nobody has pulled a segment for a while → don't spend a
		// Chrome slot keeping this URL warm. The next viewer's request refreshes
		// it reactively (or bootstraps a fresh entry).
		if !reactive {
			if last := atomic.LoadInt64(&entry.lastSegmentAt); last > 0 &&
				time.Since(time.Unix(0, last)) > idleAfter {
				continue
			}
		}

		entry.mu.RLock()
		age := time.Since(entry.resolvedAt)
		entry.mu.RUnlock()

		if age > maxAge {
			// Stop refreshing AND evict, so the next request bootstraps a new
			// entry instead of cache-hitting this one forever with a dead URL.
			fmt.Printf("[alloha-refresh] entry too old (%s), stopping + evicting\n", age)
			allohaEvictStreamEntry(entry)
			return
		}

		cooldown := minCooldown
		if reactive {
			cooldown = reactiveCooldown
		}
		if age < cooldown {
			fmt.Printf("[alloha-refresh] too fresh (age=%s, reactive=%v), skipping re-resolve\n", age.Truncate(time.Second), reactive)
			continue
		}

		a.refreshEntryOnce(entry)
	}
}

// allohaKeepopenLoop receives header updates directly from the live browser tab
// (keepopen mode). When the player's WS detects edge_hash rotation it generates
// new Borth+AC and the XHR hook fires again — we capture this and update the entry
// without doing a full browser re-resolve. When the keepalive channel closes (tab
// expires), we fall back to the normal timed refresh loop for the rest of the session.
//
// It must ALSO serve entry.refreshNow. Header pushes cannot fix an expired URL
// token, and with no reader on that channel a 403 in keepopen mode never
// triggered a re-resolve at all: the 403 handler waited out its timeout, saw
// only resolvedAt moving (bumped by header pushes), retried the same dead URL
// and handed the client the 403.
func (a *allohaChecker) allohaKeepopenLoop(entry *allohaStreamEntry, updates <-chan map[string]string) {
	fmt.Printf("[alloha-keepopen] started for %s\n", entry.tokenMovie)
	for {
		select {
		case hdrs, ok := <-updates:
			if !ok {
				updates = nil
				break
			}
			entry.mu.Lock()
			entry.cdnHeaders = hdrs
			entry.resolvedAt = time.Now()
			entry.mu.Unlock()
			borth := hdrs["Borth"]
			ac := hdrs["Accepts-Controls"]
			fmt.Printf("[alloha-keepopen] headers updated: borth=%s... ac=%s...\n",
				borth[:min(len(borth), 16)], ac[:min(len(ac), 16)])
		case <-entry.refreshNow:
		drain:
			for {
				select {
				case <-entry.refreshNow:
				default:
					break drain
				}
			}
			fmt.Printf("[alloha-keepopen] 403 signalled — re-resolving URL for %s\n", entry.tokenMovie)
			a.refreshEntryOnce(entry)
		}
		if updates == nil {
			break
		}
	}
	// Keepalive expired — browser tab closed. Fall back to timed refresh loop.
	fmt.Printf("[alloha-keepopen] keepalive expired for %s, switching to refresh loop\n", entry.tokenMovie)
	entry.mu.Lock()
	entry.keepopen = false
	entry.mu.Unlock()
	go a.allohaRefreshLoop(entry)
}

// ---------- Spider search (similar) ----------

func (a *allohaChecker) spiderSearch(w http.ResponseWriter, r *http.Request) {
	title := strings.TrimSpace(r.URL.Query().Get("title"))
	rjson := parseBoolParam(r.URL.Query().Get("rjson"))
	if title == "" || a.token == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	items := a.allohaV2SearchByName(r.Context(), title, 0)
	if len(items) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(r)
	var rows []map[string]any
	for _, item := range items {
		name, _ := item["name"].(string)
		if name == "" {
			name, _ = item["original_name"].(string)
		}
		tm, _ := item["token_movie"].(string)
		// poster lives on v2 item but our list-converter doesn't propagate it; pull directly.
		poster, _ := item["poster"].(string)
		rows = append(rows, map[string]any{
			"url":    host + "/lite/alloha?orid=" + url.QueryEscape(tm),
			"name":   name,
			"year":   allohaInt(item["year"]),
			"poster": poster,
		})
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "similar", "data": rows})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for _, item := range rows {
		name, _ := item["name"].(string)
		sb.WriteString(`<div class="videos__item videos__movie selector" data-json='`)
		sb.WriteString(getsTVAttrJSON(item))
		sb.WriteString(`'><div class="videos__item-imgbox videos__movie-imgbox"></div><div class="videos__item-title">`)
		sb.WriteString(name)
		sb.WriteString(`</div></div>`)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// ---------- Search API ----------

func (a *allohaChecker) search(r *http.Request, orid, imdbID, kinopoiskID, tmdbID, title string, serial bool, originalLanguage string, year int) (data map[string]any, category int, tokenMovie string) {
	if a.token == "" {
		return nil, 0, ""
	}

	// Try by IDs first
	if orid != "" || imdbID != "" || kinopoiskID != "" || tmdbID != "" {
		d, cat, tm := a.searchByIDs(r, orid, imdbID, kinopoiskID, tmdbID)
		if d != nil {
			return d, cat, tm
		}
	}

	// Then by title. Year is a SOFT filter, not a gate: /capi often passes a
	// title with no year (kp/tmdb resolution missed), and requiring year>0 here
	// made Alloha bail instantly (0 voices, ms:0) while title-only sources like
	// filmix found the same card. searchByTitle applies the year filter only
	// when a year is actually present.
	if title != "" {
		return a.searchByTitle(r, title, year, serial, originalLanguage)
	}

	return nil, 0, ""
}

func (a *allohaChecker) searchByIDs(r *http.Request, orid, imdbID, kinopoiskID, tmdbID string) (map[string]any, int, string) {
	ctx := r.Context()

	// v2 API: dedicated lookup endpoints per ID type. Each returns a single
	// movie/serial pre-converted to legacy map shape (via allohaV2ToLegacy)
	// so writeMovie/writeSerial/lookupByIDsWithQuality keep working unchanged.
	if imdbID != "" {
		if d := a.allohaV2GetByIMDB(ctx, imdbID); d != nil {
			cat := allohaInt(d["category"])
			tm, _ := d["token_movie"].(string)
			if cat > 0 {
				return d, cat, tm
			}
		}
	}
	if kinopoiskID != "" && isInt64(kinopoiskID) {
		if d := a.allohaV2GetByKP(ctx, kinopoiskID); d != nil {
			cat := allohaInt(d["category"])
			tm, _ := d["token_movie"].(string)
			if cat > 0 {
				return d, cat, tm
			}
		}
	}
	// TMDB id is the one every /capi & Lampa card carries. Try it before falling
	// back to title search — it resolves without capi's TMDB→Kinopoisk step,
	// which fails for some titles and left Alloha with 0 voices (ms:0).
	if tmdbID != "" && isInt64(tmdbID) {
		if d := a.allohaV2GetByTMDB(ctx, tmdbID); d != nil {
			cat := allohaInt(d["category"])
			tm, _ := d["token_movie"].(string)
			if cat > 0 {
				return d, cat, tm
			}
		}
	}
	if orid != "" {
		if d := a.allohaV2GetByToken(ctx, orid); d != nil {
			cat := allohaInt(d["category"])
			tm, _ := d["token_movie"].(string)
			if cat > 0 {
				return d, cat, tm
			}
		}
	}

	return nil, 0, ""
}

func (a *allohaChecker) searchByTitle(r *http.Request, title string, year int, serial bool, originalLanguage string) (map[string]any, int, string) {
	// v2 /v2/movies/name/list returns matches pre-shaped via allohaV2ToLegacyListItem.
	// "serial" flag is filtered client-side (v2 doesn't accept category filter on this endpoint).
	items := a.allohaV2SearchByName(r.Context(), title, year)
	if len(items) == 0 {
		return nil, 0, ""
	}

	wantTitle := normalizeSearchTitle(title)
	if wantTitle == "" {
		return nil, 0, ""
	}

	wantCat := 1
	if serial {
		wantCat = 2
	}

	for _, item := range items {
		name, _ := item["name"].(string)
		if normalizeSearchTitle(name) != wantTitle {
			continue
		}
		itemYear := allohaInt(item["year"])
		if year > 0 && itemYear > 0 && itemYear != year && itemYear != year-1 && itemYear != year+1 {
			continue
		}
		if originalLanguage == "ru" {
			country, _ := item["country"].(string)
			if normalizeSearchTitle(country) != normalizeSearchTitle("россия") {
				continue
			}
		}
		cat := allohaInt(item["category_id"])
		if cat <= 0 || cat != wantCat {
			continue
		}
		tm, _ := item["token_movie"].(string)
		// /name/list items don't carry seasons/episodes. For movies the listed
		// translation_iframe is enough for writeMovie. For serials we must
		// promote to a full detail fetch by KP id (if we have it).
		if cat == 2 {
			if kpInt := allohaInt(item["ids_kp"]); kpInt > 0 {
				if full := a.allohaV2GetByKP(r.Context(), strconv.Itoa(kpInt)); full != nil {
					if tm2, _ := full["token_movie"].(string); tm2 != "" {
						return full, allohaInt(full["category"]), tm2
					}
				}
			}
			// Fallback: use token_movie to fetch full detail.
			if tm != "" {
				if full := a.allohaV2GetByToken(r.Context(), tm); full != nil {
					return full, allohaInt(full["category"]), tm
				}
			}
		}
		return item, cat, tm
	}

	return nil, 0, ""
}

// ---------- checksearch ----------

func (a *allohaChecker) checkSearch(req *http.Request) (bool, string) {
	if a.token == "" {
		return false, ""
	}

	q := req.URL.Query()
	orid := strings.TrimSpace(q.Get("orid"))
	imdbID := strings.TrimSpace(q.Get("imdb_id"))
	kinopoiskID := strings.TrimSpace(q.Get("kinopoisk_id"))
	tmdbID := strings.TrimSpace(firstNonEmpty(q.Get("tmdb_id"), q.Get("id")))
	title := strings.TrimSpace(q.Get("title"))
	originalLanguage := strings.ToLower(strings.TrimSpace(q.Get("original_language")))
	serial := strings.TrimSpace(q.Get("serial")) == "1"

	year, _ := strconv.Atoi(strings.TrimSpace(q.Get("year")))

	if orid != "" || imdbID != "" || kinopoiskID != "" || tmdbID != "" {
		if found, quality := a.lookupByIDsWithQuality(req, orid, imdbID, kinopoiskID, tmdbID); found {
			return true, quality
		}
	}

	if title == "" {
		return false, ""
	}
	if a.lookupByTitle(req, title, year, serial, originalLanguage) {
		return true, ""
	}
	return false, ""
}

// lookupByIDsWithQuality tries to find content by IDs and decodes the best
// quality badge from translation_iframe. Uses the same v2 endpoints as
// searchByIDs — the result is the same legacy shape, so allohaMaxQuality works.
func (a *allohaChecker) lookupByIDsWithQuality(req *http.Request, orid, imdbID, kinopoiskID, tmdbID string) (bool, string) {
	ctx := req.Context()

	if imdbID != "" {
		if d := a.allohaV2GetByIMDB(ctx, imdbID); d != nil {
			return true, allohaMaxQuality(d)
		}
	}
	if kinopoiskID != "" && isInt64(kinopoiskID) {
		if d := a.allohaV2GetByKP(ctx, kinopoiskID); d != nil {
			return true, allohaMaxQuality(d)
		}
	}
	if tmdbID != "" && isInt64(tmdbID) {
		if d := a.allohaV2GetByTMDB(ctx, tmdbID); d != nil {
			return true, allohaMaxQuality(d)
		}
	}
	if orid != "" {
		if d := a.allohaV2GetByToken(ctx, orid); d != nil {
			return true, allohaMaxQuality(d)
		}
	}
	return false, ""
}

// allohaMaxQuality extracts the best quality badge from translation_iframe data.
func allohaMaxQuality(data map[string]any) string {
	translations := allohaMapField(data, "translation_iframe")
	best := ""
	for _, tVal := range translations {
		inner, ok := tVal.(map[string]any)
		if !ok {
			continue
		}
		uhdVal, _ := inner["uhd"]
		if fmt.Sprint(uhdVal) == "true" {
			return "4K"
		}
		q, _ := inner["quality"].(string)
		normalized := normalizeQualityBadge(q)
		if qualityBadgeRank(normalized) > qualityBadgeRank(best) {
			best = normalized
		}
	}
	return best
}

func (a *allohaChecker) lookupByTitle(req *http.Request, title string, year int, serial bool, originalLanguage string) bool {
	items := a.allohaV2SearchByName(req.Context(), title, year)
	if len(items) == 0 {
		return false
	}

	wantTitle := normalizeSearchTitle(title)
	if wantTitle == "" {
		return false
	}
	wantCat := 1
	if serial {
		wantCat = 2
	}

	for _, item := range items {
		name, _ := item["name"].(string)
		if normalizeSearchTitle(name) != wantTitle {
			continue
		}
		itemYear := allohaInt(item["year"])
		if year > 0 && itemYear > 0 && itemYear != year && itemYear != year-1 && itemYear != year+1 {
			continue
		}
		if originalLanguage == "ru" {
			country, _ := item["country"].(string)
			if normalizeSearchTitle(country) != normalizeSearchTitle("россия") {
				continue
			}
		}
		cat := allohaInt(item["category_id"])
		if cat == wantCat {
			return true
		}
	}
	return false
}

// ---------- Helpers ----------

func allohaMapField(data map[string]any, key string) map[string]any {
	v, ok := data[key]
	if !ok {
		return nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return m
}

func allohaInt(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case string:
		n, _ := strconv.Atoi(x)
		return n
	}
	return 0
}

// playerProxyClient returns a shared HTTP client with cookie jar for CDN player
// reverse proxy. All requests for the same player session share cookies.
var (
	playerProxyClientMu   sync.Mutex
	playerProxyClientInst *http.Client
)

// resolvedStreams caches m3u8 URLs extracted from /bnsi/movies/ responses.
// Key is the API path (e.g. /bnsi/movies/1281159), value is the cdn-proxy URL.
type resolvedStreamEntry struct {
	url string
	ts  time.Time
}

var (
	resolvedStreamsMu sync.Mutex
	resolvedStreams   = make(map[string]resolvedStreamEntry)
)

// allohaActiveIDRe extracts the movie ID from the player HTML JSON config.
// HTML contains: "active":{"id":1281159,...} — we capture the id value.
// Fallback: /bnsi/movies/{id} literal in JS.
var allohaActiveIDRe = regexp.MustCompile(`"active"\s*:\s*\{\s*"id"\s*:\s*(\d+)`)
var allohaMoviesIDRe = regexp.MustCompile(`/bnsi/movies/(\d+)`)

// allohaDirectResolve tries to resolve m3u8 URL without headless Chrome:
// 1. Fetch player HTML
// 2. Extract viewporti (Borth token) + idFile from HTML
// 3. POST /bnsi/movies/{id} directly with Borth
// Returns m3u8 URL, CDN headers, and ok flag.
func (a *allohaChecker) allohaDirectResolve(ctx context.Context, tokenMovie, translation string, season, episode int) (string, map[string]string, bool) {
	htmlBody, pageURL, err := allohaFetchPlayerHTML(ctx, a.linkHost, a.token, tokenMovie, translation, season, episode)
	if err != nil {
		fmt.Printf("[alloha-direct] fetch HTML failed: %v\n", err)
		return "", nil, false
	}

	// Extract Borth token from <meta name="viewporti" content="...">
	borth := mirageExtractViewporti([]byte(htmlBody))
	if borth == "" {
		fmt.Printf("[alloha-direct] no viewporti in HTML (%d bytes)\n", len(htmlBody))
		return "", nil, false
	}

	// Extract movie ID from HTML config JSON: "active":{"id":1281159,...}
	idMatch := allohaActiveIDRe.FindStringSubmatch(htmlBody)
	if len(idMatch) < 2 {
		// Fallback: /bnsi/movies/{id} literal in JS
		idMatch = allohaMoviesIDRe.FindStringSubmatch(htmlBody)
	}
	if len(idMatch) < 2 {
		fmt.Printf("[alloha-direct] no movie ID in HTML (%d bytes)\n", len(htmlBody))
		return "", nil, false
	}
	idFile := idMatch[1]

	// POST /bnsi/movies/{id} with Borth header
	base := strings.TrimRight(a.linkHost, "/")
	targetURL := base + "/bnsi/movies/" + idFile

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, nil)
	if err != nil {
		return "", nil, false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Origin", base)
	req.Header.Set("Referer", pageURL)
	req.Header.Set("Borth", borth)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/json")

	client := httpclient.NewForBalancer("alloha", 15*time.Second)
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("[alloha-direct] POST /movies/ failed: %v\n", err)
		return "", nil, false
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Printf("[alloha-direct] /movies/ status=%d err=%v\n", resp.StatusCode, err)
		return "", nil, false
	}

	// Start a WS client for THIS stream's session, unless it already has a live
	// one (re-resolve of the same stream reuses it — restarting churns a gap
	// where no config_update has arrived yet and Accepts-Controls goes stale).
	//
	// The liveness check is per-stream on purpose. It used to also accept
	// a.linkHost — the same constant for every alloha stream — so ONE WS session
	// served all viewers: only that one stream sent "playing" heartbeats, and
	// every other stream pulled segments with no telemetry behind its own sid,
	// which is what the CDN cuts off with 403 after a few minutes. One session
	// per stream is also simply what N real players would produce.
	streamKey := allohaStreamKey(tokenMovie, translation, season, episode)
	if GetEdgeHash(streamKey) == "" {
		if wsURL, wsSID := mirageExtractWSParams(body); wsURL != "" && wsSID != "" {
			// Alloha WS shares the same SOCKS5 as its browser/CDN so all three
			// have the same exit IP — required by the CDN session binding.
			allohaSocks := httpclient.SocksAddrForBalancer("alloha")
			if allohaSocks == "" {
				allohaSocks = httpclient.SocksAddrForBalancer("mirage")
			}
			wsClient := newMirageWSClient(wsURL, wsSID, a.linkHost, allohaSocks)
			go wsClient.Run()
			// Identity = this stream; tokenMovie/linkHost are lookup aliases only
			// (see RegisterEdgeHashClient — aliases never close a live session).
			RegisterEdgeHashClient(wsClient, streamKey, tokenMovie, a.linkHost)
			fmt.Printf("[alloha-direct] started WS client for edge_hash (%s)\n", streamKey)
		}
	} else {
		fmt.Printf("[alloha-direct] WS client alive, skipping restart\n")
	}

	// Extract m3u8 URL.
	m3u8 := allohaExtractHLSFromJSON(body)
	if m3u8 == "" {
		fmt.Printf("[alloha-direct] no m3u8 in /movies/ response (%d bytes)\n", len(body))
		return "", nil, false
	}

	headers := map[string]string{"Borth": borth}
	fmt.Printf("[alloha-direct] resolved: %s\n", m3u8[:min(len(m3u8), 80)])
	return m3u8, headers, true
}

func getPlayerProxyClient() *http.Client {
	playerProxyClientMu.Lock()
	defer playerProxyClientMu.Unlock()
	if playerProxyClientInst == nil {
		jar, _ := cookiejar.New(nil)

		// Use HTTP proxy only if explicitly configured (CDNHTTPProxy in config).
		// Do NOT default to 127.0.0.1:8888 — that may be Charles/Fiddler debug proxy
		// which drops large segment transfers.
		proxyAddr := ""
		if serverReady() {
			proxyAddr = strings.TrimSpace(liveConfig(config.Config{}).Online.CDNHTTPProxy)
		}
		var transport *http.Transport
		if proxyAddr != "" {
			proxyURL, _ := url.Parse(proxyAddr)
			transport = &http.Transport{Proxy: http.ProxyURL(proxyURL)}
		} else {
			transport = &http.Transport{}
		}

		playerProxyClientInst = &http.Client{
			Timeout:   30 * time.Second,
			Jar:       jar,
			Transport: transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				// Carry the original referer across redirects (player host).
				if len(via) > 0 {
					if ref := via[len(via)-1].Header.Get("Referer"); ref != "" {
						req.Header.Set("Referer", ref)
					}
				}
				return nil
			},
		}
	}
	return playerProxyClientInst
}

// servePlayerProxy is a full reverse proxy for the CDN player (stloadi.live).
// It proxies the initial HTML page and ALL subsequent requests (JS, CSS, API, WS)
// through our server, injecting the correct Referer/Origin headers that the CDN
// requires (whitelist check). This is how kinomix.web.app works — but they're
// whitelisted, so we proxy everything through our server instead.
//
// Routes:
//
//	/lite/alloha/player?token_movie=X           → HTML page
//	/lite/alloha/player/some/path                → proxy to stloadi.live/some/path
//	/lite/alloha/player/__ws__/?sid=...          → WebSocket proxy
func (a *allohaChecker) servePlayerProxy(w http.ResponseWriter, r *http.Request) {
	// Extract sub-path after /lite/alloha/player
	fullPath := r.URL.Path
	subPath := strings.TrimPrefix(fullPath, "/lite/alloha/player")
	if subPath == "" || subPath == "/" {
		subPath = "/"
	}

	linkHost := strings.TrimRight(a.linkHost, "/")

	// WebSocket proxy
	if subPath == "/__ws__/" || strings.HasPrefix(subPath, "/__ws__/") {
		a.proxyPlayerWS(w, r, linkHost)
		return
	}

	// Build target URL
	targetURL := linkHost + subPath
	if subPath == "/" {
		// Main page — add token params
		tokenMovie := r.URL.Query().Get("token_movie")
		if tokenMovie == "" {
			http.Error(w, "token_movie required", http.StatusBadRequest)
			return
		}
		targetURL = linkHost + "/?token_movie=" + url.QueryEscape(tokenMovie) +
			"&token=" + url.QueryEscape(a.token)
	} else if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	// Build proxy request
	var bodyReader io.Reader
	if r.Body != nil {
		bodyReader = r.Body
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, bodyReader)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Copy relevant headers from client, including CDN auth tokens.
	for _, h := range []string{"Accept", "Accept-Language", "Content-Type", "Range",
		"Cache-Control", "Pragma", "Borth", "X-Requested-With",
		"Accepts-Controls", "Authorizations"} {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	req.Header.Set("User-Agent", r.Header.Get("User-Agent"))

	// Critical: non-empty Referer + correct Origin (own token accepts any referer)
	req.Header.Set("Referer", linkHost+"/")
	req.Header.Set("Origin", linkHost)

	// Use shared client with cookie jar so DDoS-Guard cookies persist
	// across HTML page load → JS/CSS → API calls (same CDN session).
	client := getPlayerProxyClient()
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "proxy failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Cap at 16 MB — this proxies HTML/JS/CSS/fonts for the embedded player.
	// Larger responses indicate a CDN misconfig or attack; don't blow heap.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		http.Error(w, "read failed", http.StatusBadGateway)
		return
	}

	// Copy response headers
	for _, h := range []string{"Content-Type", "Cache-Control", "ETag", "Last-Modified"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")

	// Handle CORS preflight
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// For HTML page: rewrite URLs to route through our proxy
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/html") {
		html := string(body)
		proxyBase := "/lite/alloha/player"

		// Strip SRI integrity attributes — we modify JS content (URL rewrites)
		// which changes the hash, causing browser to block the scripts.
		reIntegrity := regexp.MustCompile(`\s+integrity="[^"]*"`)
		html = reIntegrity.ReplaceAllString(html, "")

		// Rewrite absolute URLs to linkHost → our proxy
		html = strings.ReplaceAll(html, linkHost+"/", proxyBase+"/")
		html = strings.ReplaceAll(html, linkHost, proxyBase)

		// Rewrite absolute paths: /build/, /js/, /t? → through our proxy
		// These are root-relative URLs that bypass <base> tag.
		html = strings.ReplaceAll(html, `href="/build/`, `href="`+proxyBase+`/build/`)
		html = strings.ReplaceAll(html, `src="/build/`, `src="`+proxyBase+`/build/`)
		html = strings.ReplaceAll(html, `src="/js/`, `src="`+proxyBase+`/js/`)
		html = strings.ReplaceAll(html, `src="./js/`, `src="`+proxyBase+`/js/`)
		html = strings.ReplaceAll(html, `href="/t?`, `href="`+proxyBase+`/t?`)
		html = strings.ReplaceAll(html, `"/images/`, `"`+proxyBase+`/images/`)

		// Rewrite WebSocket URL: wss://linkHost/ws/ → ws://localhost/lite/alloha/player/__ws__/
		wsHost := strings.TrimPrefix(linkHost, "https://")
		wsHost = strings.TrimPrefix(wsHost, "http://")
		html = strings.ReplaceAll(html, "wss://"+wsHost+"/ws/", "ws://"+r.Host+proxyBase+"/__ws__/")
		html = strings.ReplaceAll(html, `"ws/`, `"`+proxyBase+`/__ws__/`)
		html = strings.ReplaceAll(html, `"/ws/`, `"`+proxyBase+`/__ws__/`)

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(resp.StatusCode)
		w.Write([]byte(html))
		return
	}

	// For JS: rewrite CDN host references so API calls go through proxy
	if strings.Contains(ct, "javascript") || strings.Contains(ct, "ecmascript") {
		js := string(body)
		proxyBase := "/lite/alloha/player"

		// Rewrite fetch/XHR calls to linkHost
		js = strings.ReplaceAll(js, linkHost+"/", proxyBase+"/")
		js = strings.ReplaceAll(js, linkHost, proxyBase)

		// Rewrite absolute API paths: /bnsi/ → /lite/alloha/player/bnsi/
		js = strings.ReplaceAll(js, `"/bnsi/`, `"`+proxyBase+`/bnsi/`)
		js = strings.ReplaceAll(js, `'/bnsi/`, `'`+proxyBase+`/bnsi/`)
		// Rewrite /ws/ paths for WebSocket
		js = strings.ReplaceAll(js, `"/ws/`, `"`+proxyBase+`/__ws__/`)
		js = strings.ReplaceAll(js, `'/ws/`, `'`+proxyBase+`/__ws__/`)

		// Rewrite WebSocket connections
		wsHost := strings.TrimPrefix(linkHost, "https://")
		wsHost = strings.TrimPrefix(wsHost, "http://")
		js = strings.ReplaceAll(js, "wss://"+wsHost, "ws://"+r.Host+proxyBase+"/__ws__")

		// Rewrite CDN stream URLs so segments go through our proxy
		// (browser sends wrong Origin header for cross-origin CDN requests).
		// CDN URLs: https://NODE.stream-balancer-allo-1.live/...
		// → /cdn-stream/NODE/...
		// Our /cdn-stream/ handler proxies with correct Origin/Referer.
		js = strings.ReplaceAll(js, "https://", "http://"+r.Host+"/cdn-proxy/https/")
		js = strings.ReplaceAll(js, "http://"+r.Host+"/cdn-proxy/https/"+r.Host, "http://"+r.Host)

		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.WriteHeader(resp.StatusCode)
		w.Write([]byte(js))
		return
	}

	// All other resources: pass through as-is
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

// NewCDNStreamProxy proxies CDN stream requests (m3u8/segments) with correct
// Origin/Referer headers. URL format: /cdn-proxy/https/HOST/PATH
func NewCDNStreamProxy(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Extract real URL from path: /cdn-proxy/https/host.com/path → https://host.com/path
		path := strings.TrimPrefix(r.URL.Path, "/cdn-proxy/")
		if path == "" {
			http.Error(w, "missing URL", http.StatusBadRequest)
			return
		}
		// Reconstruct: https/host.com/path → https://host.com/path
		targetURL := strings.Replace(path, "/", "://", 1)
		if r.URL.RawQuery != "" {
			targetURL += "?" + r.URL.RawQuery
		}

		var bodyReader io.Reader
		if r.Body != nil {
			bodyReader = r.Body
		}
		req, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, bodyReader)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		// Copy headers from client
		for _, h := range []string{"Accept", "Accept-Language", "Range",
			"Accepts-Controls", "Borth", "Authorizations"} {
			if v := r.Header.Get(h); v != "" {
				req.Header.Set(h, v)
			}
		}
		req.Header.Set("User-Agent", r.Header.Get("User-Agent"))

		// Set correct Origin/Referer that CDN expects
		u, _ := url.Parse(targetURL)
		if u != nil {
			origin := u.Scheme + "://" + u.Host
			// CDN expects Origin from the player host, not from CDN itself
			linkHost := allohaMigrateLinkHost(cfg.Online.Alloha.LinkHost)
			req.Header.Set("Origin", linkHost)
			req.Header.Set("Referer", linkHost+"/")
			_ = origin
		}

		// Handle CORS preflight
		if r.Method == "OPTIONS" {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Headers", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.WriteHeader(http.StatusNoContent)
			return
		}

		client := getPlayerProxyClient()
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, "cdn proxy: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		// Forward response headers
		for _, h := range []string{"Content-Type", "Content-Length", "Content-Range",
			"Accept-Ranges", "Cache-Control", "ETag"} {
			if v := resp.Header.Get(h); v != "" {
				w.Header().Set(h, v)
			}
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		w.Header().Set("Access-Control-Expose-Headers", "Content-Length, Content-Range")

		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}
}

// NewAllohaPlayerAPIProxy creates a handler that proxies absolute-path
// requests from the CDN player iframe (/bnsi/*, /ws/*, /images/*) to the linkhost.
func NewAllohaPlayerAPIProxy(cfg config.Config) http.HandlerFunc {
	linkHost := allohaMigrateLinkHost(cfg.Online.Alloha.LinkHost)

	return func(w http.ResponseWriter, r *http.Request) {
		targetURL := linkHost + r.URL.Path
		if r.URL.RawQuery != "" {
			targetURL += "?" + r.URL.RawQuery
		}

		var bodyReader io.Reader
		if r.Body != nil {
			bodyReader = r.Body
		}
		req, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, bodyReader)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		// Forward ALL relevant headers including CDN auth tokens.
		for _, h := range []string{"Accept", "Accept-Language", "Content-Type", "Range",
			"Borth", "X-Requested-With", "Cache-Control", "Pragma",
			"Accepts-Controls", "Authorizations"} {
			if v := r.Header.Get(h); v != "" {
				req.Header.Set(h, v)
			}
		}
		// Forward lowercase borth (JS may send it lowercase)
		if v := r.Header.Get("borth"); v != "" {
			req.Header.Set("Borth", v)
		}
		req.Header.Set("User-Agent", r.Header.Get("User-Agent"))
		req.Header.Set("Referer", linkHost+"/")
		req.Header.Set("Origin", linkHost)
		// Don't send Accept-Encoding — we need plain text for URL rewriting
		req.Header.Del("Accept-Encoding")

		client := getPlayerProxyClient()
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, "proxy: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		for _, h := range []string{"Content-Type", "Cache-Control"} {
			if v := resp.Header.Get(h); v != "" {
				w.Header().Set(h, v)
			}
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// For JSON API responses (/bnsi/movies/): rewrite CDN stream URLs
		// so segments go through our /cdn-proxy/ (fixes Origin header).
		ct := resp.Header.Get("Content-Type")
		if strings.Contains(ct, "json") || strings.Contains(r.URL.Path, "/bnsi/") {
			// Handle gzip-compressed responses
			reader := resp.Body
			if resp.Header.Get("Content-Encoding") == "gzip" {
				gz, gzerr := gzip.NewReader(resp.Body)
				if gzerr == nil {
					defer gz.Close()
					reader = gz
				}
			}
			body, _ := io.ReadAll(reader)
			text := string(body)

			// Rewrite all CDN stream URLs to go through our proxy.
			// JSON may use escaped slashes: https:\/\/ or plain https://
			// Replace both formats.
			proxyPrefix := "http://" + r.Host + "/cdn-proxy/https/"
			text = strings.ReplaceAll(text, `https:\/\/`, `HTTPSPROTO`)
			text = strings.ReplaceAll(text, `https://`, proxyPrefix)
			text = strings.ReplaceAll(text, `HTTPSPROTO`, proxyPrefix)
			// Don't proxy our own host
			text = strings.ReplaceAll(text, proxyPrefix+r.Host, "http://"+r.Host)

			// Extract and cache m3u8 URL for Shaka player.
			// URLs in JSON may have escaped slashes: http:\/\/host\/cdn-proxy\/...
			// Unescape first, then search.
			unescaped := strings.ReplaceAll(text, `\/`, `/`)
			reMaster := regexp.MustCompile(`(http://[^"'\s]+/cdn-proxy/[^"'\s]+master\.m3u8)`)
			if match := reMaster.FindString(unescaped); match != "" {
				resolvedStreamsMu.Lock()
				resolvedStreams["latest"] = resolvedStreamEntry{url: match, ts: time.Now()}
				resolvedStreamsMu.Unlock()
				fmt.Printf("[alloha-proxy] captured m3u8: %s\n", match[:min(len(match), 100)])
			}
			fmt.Printf("[alloha-proxy] rewritten API response: len=%d hasCdnProxy=%v\n",
				len(text), strings.Contains(text, "cdn-proxy"))

			w.Header().Set("Content-Type", ct)
			w.Header().Del("Content-Length")
			w.Header().Del("Content-Encoding") // we decompressed
			w.WriteHeader(resp.StatusCode)
			w.Write([]byte(text))
		} else {
			w.WriteHeader(resp.StatusCode)
			io.Copy(w, resp.Body)
		}
	}
}

// proxyPlayerWS proxies WebSocket connections for the CDN player.
func (a *allohaChecker) proxyPlayerWS(w http.ResponseWriter, r *http.Request, linkHost string) {
	// For now, redirect WS to the CDN directly.
	// TODO: implement full WS proxy if CDN checks Origin on WS handshake.
	wsURL := strings.Replace(linkHost, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	wsPath := strings.TrimPrefix(r.URL.Path, "/lite/alloha/player/__ws__")
	if wsPath == "" {
		wsPath = "/"
	}
	target := wsURL + "/ws" + wsPath
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusTemporaryRedirect)
}

// allohaReplayMovies re-issues the /bnsi/movies/ POST captured during the
// browser resolve, byte for byte: same URL, same headers (the player-generated
// Borth among them), same form body. No Chrome, one HTTP round trip.
//
// Returns false whenever the replay isn't usable (nothing captured, non-2xx,
// no stream in the response) so the caller can fall back to a real resolve —
// Borth embeds a timestamp, so the upstream is free to reject it at any point.
func (a *allohaChecker) allohaReplayMovies(ctx context.Context, entry *allohaStreamEntry) (string, map[string]string, bool) {
	entry.mu.RLock()
	moviesTarget := entry.moviesURL
	body := entry.moviesBody
	hdrs := make(map[string]string, len(entry.moviesHeaders))
	maps.Copy(hdrs, entry.moviesHeaders)
	prevStreamHdrs := make(map[string]string, len(entry.cdnHeaders))
	maps.Copy(prevStreamHdrs, entry.cdnHeaders)
	entry.mu.RUnlock()

	if strings.TrimSpace(moviesTarget) == "" || len(hdrs) == 0 {
		return "", nil, false
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, moviesTarget, strings.NewReader(string(body)))
	if err != nil {
		return "", nil, false
	}
	for k, v := range hdrs {
		req.Header.Set(k, v)
	}

	resp, err := httpclient.NewForBalancer("alloha", 15*time.Second).Do(req)
	if err != nil {
		fmt.Printf("[alloha-replay] POST failed: %v\n", err)
		return "", nil, false
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Printf("[alloha-replay] status=%d (falling back to resolve)\n", resp.StatusCode)
		return "", nil, false
	}

	hls := allohaExtractHLSFromJSON(respBody)
	if hls == "" {
		fmt.Printf("[alloha-replay] 200 but no stream in body (%d bytes)\n", len(respBody))
		return "", nil, false
	}

	fmt.Printf("[alloha-replay] refreshed URL without Chrome\n")
	// Keep the CDN auth headers we already had: the replay refreshes the signed
	// URL, it doesn't mint new Authorizations/Accepts-Controls.
	return hls, prevStreamHdrs, true
}

// directRequested reports whether this request should get CDN-direct media
// URLs: the admin enabled direct_stream AND the client advertised (`direct=1`)
// that it can attach the CDN headers to its own requests. Browsers can't —
// Origin/Referer are forbidden headers there — so this stays opt-in per client
// instead of being guessed from a User-Agent.
func (a *allohaChecker) directRequested(r *http.Request) bool {
	return a.directStream && parseBoolParam(r.URL.Query().Get("direct"))
}

// directHeaders returns the header set a client must replay to fetch segments
// straight from the CDN, or nil when direct delivery isn't in play.
//
// Every one of these is mandatory — dropping any single one answers 403 (and
// Referer/Origin must name the PLAYER host: the affiliate's own whitelisted
// domains are refused by the CDN, only the player origin passes).
func (a *allohaChecker) directHeaders(r *http.Request, tokenMovie, t string, s, e int) map[string]string {
	if !a.directRequested(r) {
		return nil
	}
	streamKey := allohaStreamKey(tokenMovie, t, s, e)
	ac := allohaEdgeHash(streamKey, tokenMovie)
	if ac == "" {
		ac = GetGuardToken(a.linkHost)
	}
	if ac == "" {
		return nil // nothing to authorise with — keep proxying rather than hand out 403s
	}
	base := strings.TrimRight(a.linkHost, "/")
	return map[string]string{
		"Accepts-Controls": ac,
		"Authorizations":   mirageStaticBearer,
		"Referer":          base + "/",
		"Origin":           base,
		"User-Agent":       "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	}
}

// allohaAbsoluteURIAttr rewrites a URI="..." attribute to an absolute CDN URL
// (direct mode), leaving the rest of the tag untouched.
func allohaAbsoluteURIAttr(line string, abs func(string) string) string {
	const marker = "URI=\""
	idx := strings.Index(line, marker)
	if idx < 0 {
		return line
	}
	start := idx + len(marker)
	end := strings.Index(line[start:], "\"")
	if end < 0 {
		return line
	}
	return line[:start] + abs(line[start:start+end]) + line[start+end:]
}

// allohaAuthHandler re-issues the direct-mode headers on demand. Accepts-Controls
// rotates, so a player that started with a direct stream needs somewhere cheap to
// pick up a fresh one mid-playback instead of re-resolving the whole stream.
func (a *allohaChecker) allohaAuthHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tokenMovie := strings.TrimSpace(q.Get("token_movie"))
	if tokenMovie == "" {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	season, _ := strconv.Atoi(strings.TrimSpace(q.Get("s")))
	episode, _ := strconv.Atoi(strings.TrimSpace(q.Get("e")))
	// The caller already proved it wants direct mode by coming here; honour the
	// admin switch only.
	if !a.directStream {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	r2 := r.Clone(r.Context())
	rq := r2.URL.Query()
	rq.Set("direct", "1")
	r2.URL.RawQuery = rq.Encode()
	hdrs := a.directHeaders(r2, tokenMovie, strings.TrimSpace(q.Get("t")), season, episode)
	if hdrs == nil {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"headers": hdrs})
}

// allohaIsManifestRef reports whether a playlist line points at another playlist
// rather than at media. Only media may be handed to the client directly — the
// CDN requires the per-session Borth token on manifest requests.
func allohaIsManifestRef(ref string) bool {
	clean := ref
	if i := strings.IndexAny(clean, "?#"); i >= 0 {
		clean = clean[:i]
	}
	return strings.HasSuffix(strings.ToLower(clean), ".m3u8")
}
