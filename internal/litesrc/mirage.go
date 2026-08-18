package litesrc

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"lampac-go/internal/httpclient"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/browser"
	"lampac-go/internal/config"
	"lampac-go/internal/kit"
	"lampac-go/internal/proxylink"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

var (
	mirageFileListSingleRe = regexp.MustCompile(`(?s)fileList\s*=\s*JSON\.parse\('([^\n\r]+)'\);`)
	mirageFileListDoubleRe = regexp.MustCompile(`(?s)fileList\s*=\s*JSON\.parse\("([^\n\r]+)"\);`)
	mirageAnyM3URe         = regexp.MustCompile(`https?://[^"'\\\s]+\.m3u8[^"'\\\s]*`)
	mirageQualityURLRe     = regexp.MustCompile(`\[(2160|1440|1080|720|480|360)\][^h]*(https?://[^,\s"'\\]+)`)

	// HLS m3u8 rewrite patterns (local copies for use by streamHLS rewriter).
	mirageReHTTPLinks  = regexp.MustCompile(`(https?://[^\n\r"\# ]+)`)
	mirageReLineURI    = regexp.MustCompile(`([\n\r])([^\n\r]+)`)
	mirageReQuotedURI  = regexp.MustCompile(`(URI=")([^"]+)`)
	mirageRePathNoFile = regexp.MustCompile(`(?i)(https?://[^\n\r]+/)([^/]+)$`)
)

// mirageStreamEntry caches resolved CDN stream URLs for a given file.
// When the CDN token expires (typically ~15 min), the stream endpoint
// can re-resolve transparently without dropping playback.
type mirageStreamEntry struct {
	quals      map[string]string // quality → raw CDN m3u8 URL
	cdnHeaders map[string]string // JS-generated headers (Borth, Authorization, etc.)
	cdnCookies string            // DDoS-Guard cookies from browser session ("k=v; k2=v2")
	tokenMovie string
	resolvedAt time.Time
	mu         sync.Mutex // guards mutable per-entry fields (e.g. /movies/ params)

	// WebSocket client for live edge_hash updates. Its config_update messages
	// feed the Accepts-Controls header on CDN requests and keep the session
	// authorized — the Spectre mechanism that replaces proxy rotation/re-resolve.
	wsClient *mirageWSClient

	// Headless-browser context (chromedp). Used by alloha's segment-fetch path;
	// nil for mirage. browserCancel tears it down on eviction.
	browserCancel context.CancelFunc
	browserCtx    context.Context

	// Unix nano of the last segment fetch (alloha throttle).
	lastSegmentAt int64

	// /movies/ POST params captured at resolve (used by alloha lightweight refresh).
	moviesURL     string
	moviesHeaders map[string]string
	moviesBody    []byte
	idFile        int64

	// Guard client link host for proof-of-browser token fallback.
	guardLinkHost string

	// Origin/Referer captured from the browser's actual m3u8 request after the
	// /movies/ POST. Spectre (Service.cs:137,141) replays these verbatim on every
	// CDN request — hardcoding linkHost as Origin gets fresh URLs 403'd even when
	// Accepts-Controls/Authorizations are valid.
	requestOrigin  string
	requestReferer string
}

// mirageStaticBearer is the CDN's static Authorizations Bearer token. Reference
// implementation lampac-nextgen hardcodes this exact value for every user,
// every movie, every request — Modules/OnlineRUS/Spectre/Controller.cs:90.
//
// Hypothesis: the Bearer is CDN-wide, not per-tokenMovie as MEMORY.md claimed.
// If our browser-captured Bearer matches this constant, the claim is confirmed
// and the re-resolve machinery (doReResolve, warmQuals, swapWarmStandby, ...)
// exists purely to refresh a value that never actually changes. See cacheStream
// for the comparison log.
const mirageStaticBearer = "Bearer pXzvbyDGLYyB6VkwsWZDv3iMKZtsXNzpzRyxZUcsKHXxsSeaYakbo3hw9mBFRc5VQTpqAX6BW8aDEqyLaHYcXSQiV6KHYTVTK6MYRphNAy5sBjtrevqkDzKmLqNdfMZGEU9NELjmtKfZy3RNGzCd767sNh1mXEj4tCcvqndHtzmwAbZNkhm4ghDEasodotMBewypNQ56uotJAQGX11csfeRfBAPk8DcUWWkkqzxca8vbnEw12vUFbBzT6hz8ZB3F3dzUhUXoL2cr1WM1bXQArRCS1MUNMz3X5WDMMQoZKxj2AMTRqp7QQX4dDB9B7VzEZTmyFULhm1AcHHMkoMvSVvKYoBoAKLycYAgMHeD4ECJcGEAGpnkJhrV57zQ7"

// cdnHeadersWithPCHash returns CDN headers with the live auth token replacing
// the static Accepts-Controls value.
//
// Priority for Accepts-Controls value:
//  1. THIS entry's own WS client edge_hash — guaranteed to belong to the
//     same browser session that produced this entry's CDN URLs. Critical
//     when multiple movies are streaming concurrently: each movie has its
//     own browser session + its own WS, so the global edge_hash bounces
//     between sessions, and using the wrong one causes CDN 403.
//  2. Registry lookup keyed by tokenMovie + linkHost — falls through to a
//     sibling entry for the same movie if the entry's own client is mid-
//     reconnect (the seed mechanism in RegisterEdgeHashClient ensures the
//     new client returns the previous client's hash until config_update
//     arrives).
//  3. Global edge_hash from ANY active client — last-resort fallback when
//     this movie has no live WS at all (e.g. browser resolve succeeded but
//     WS dial failed). Better than no Accepts-Controls header.
//  4. Guard-verified token — final fallback. CDN production logs show this
//     is often rejected (256-char base64url vs 32-char hex expected) but
//     marginally better than empty.
//
// The real browser player's xhrSetup does exactly this:
//
//	var token = config.getStreamToken?.() || null;
//	xhr.setRequestHeader('Accepts-Controls', token || config.query.query);
//
// Authorizations is always replaced with mirageStaticBearer — BEARER PROBE
// in production confirmed the Bearer is CDN-wide static (see mirageStaticBearer
// const doc).
func (e *mirageStreamEntry) cdnHeadersWithPCHash() map[string]string {
	hdrs := e.cdnHeaders

	// Priority 1: THIS entry's own WS client — same session as the URL.
	var hash string
	if e.wsClient != nil {
		hash = e.wsClient.EdgeHash()
	}

	// Priority 2: registry lookup for this movie's keys.
	if hash == "" {
		hash = GetEdgeHash(e.tokenMovie, e.guardLinkHost)
	}

	// Priority 3: any active client — global fallback.
	if hash == "" {
		hash = GetAnyEdgeHash()
	}

	// Priority 4: Guard-verified token — last resort, known to be rejected by
	// CDN in at least some cases but better than nothing when WS hasn't warmed.
	if hash == "" {
		hash = GetGuardToken(e.guardLinkHost)
	}

	out := make(map[string]string, len(hdrs)+3)
	for k, v := range hdrs {
		// Drop every case-variant of Authorizations — we override it below.
		kl := strings.ToLower(k)
		if kl == "authorizations" || kl == "authorization" {
			continue
		}
		out[k] = v
	}
	// Always send the static Bearer, regardless of what the browser captured.
	out["Authorizations"] = mirageStaticBearer
	if hash != "" {
		out["Accepts-Controls"] = hash
	}
	// Override Origin/Referer with the values the browser actually used on its
	// m3u8 request. Spectre Service.cs:137,141 does the same — the CDN binds
	// fresh URLs to the exact origin the player JS was running under.
	if e.requestOrigin != "" {
		out["Origin"] = e.requestOrigin
	}
	if e.requestReferer != "" {
		out["Referer"] = e.requestReferer
	}
	return out
}

// cdnHeadersForSegment returns headers for segment (.ts/.m4s) requests.
//
// Spectre's ProxyApiCreateHttpRequest hook (Controller.cs:80-97) clears the
// request headers and sets the same full set for EVERY CDN proxy request,
// regardless of whether the URL is an m3u8 manifest or a segment. That
// includes Authorizations (static Bearer) and Accepts-Controls (live
// edge_hash). Borth is NOT sent by Spectre on either manifests or segments
// — it's only used inside the browser's /movies/ POST, not on the
// subsequent CDN stream requests.
//
// We strip Borth here to match Spectre exactly, and keep Authorizations
// (the static Bearer) so segments authorize correctly. Previous behavior
// stripped Authorizations under the assumption that "browser JS only
// sends it on m3u8", which was wrong.
func (e *mirageStreamEntry) cdnHeadersForSegment() map[string]string {
	full := e.cdnHeadersWithPCHash()
	out := make(map[string]string, len(full))
	for k, v := range full {
		if strings.EqualFold(k, "Borth") {
			continue
		}
		out[k] = v
	}
	return out
}

// mirageUA / mirageSecChUA must be kept in sync — CDN anti-bot checks that
// the Chrome version in User-Agent matches sec-ch-ua. Production logs showed
// Chrome/131 in UA but v=146 in sec-ch-ua → mismatch → CDN flagged as bot.
// Spectre keeps them in sync via Http.UserAgent / Http.defaultUaHeaders.
const mirageUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"
const mirageSecChUA = `"Chromium";v="146", "Not-A.Brand";v="24", "Google Chrome";v="146"`

// miragSetBrowserHeaders sets a full set of Chrome-like request headers
// so CDN / DDoS-Guard sees us as a real browser, not a Go HTTP bot.
// Matches Spectre's ProxyApiCreateHttpRequest hook (Controller.cs:80-97).
func miragSetBrowserHeaders(req *http.Request, origin string) {
	miragSetBrowserHeadersFull(req, origin, "")
}

// miragSetBrowserHeadersFull is like miragSetBrowserHeaders but accepts an
// explicit Referer. Spectre Service.cs:137,141 sends watch.requestOrigin and
// watch.requestReferer captured from the browser's actual m3u8 request — when
// our Origin/Referer don't match those, the CDN rejects fresh URLs with 403
// even if Accepts-Controls and Authorizations are valid.
func miragSetBrowserHeadersFull(req *http.Request, origin, referer string) {
	req.Header.Set("Origin", origin)
	if referer == "" {
		referer = origin + "/"
	}
	req.Header.Set("Referer", referer)
	req.Header.Set("User-Agent", mirageUA)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,uk-UA;q=0.8,uk;q=0.7,en-US;q=0.6,en;q=0.5")
	req.Header.Set("sec-ch-ua", mirageSecChUA)
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", `"Windows"`)
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
}

type MirageChecker struct {
	client   *http.Client
	apiHost  string
	linkHost string
	token    string
	prefix   string          // route prefix ("mirage", "exmirage", "aladdin")
	kitName  string          // Kit token override key (e.g. "Mirage", "Aladdin")
	fileLog  *zerolog.Logger // optional file logger (nil = no file log)
	logFile  *os.File        // kept for graceful close

	// Stream URL cache: idFile → resolved stream entry.
	// Allows the /stream endpoint to re-resolve CDN URLs when tokens expire.
	streamMu    sync.RWMutex
	streamCache map[int64]*mirageStreamEntry

	// In-flight resolves: idFile → channel closed when the /video resolve finishes.
	// Lets concurrent /stream.m3u8 cache-miss requests wait for the parallel /video
	// instead of returning a fatal 404. Lampa treats 404 from /stream.m3u8 as
	// "stop playback", so any cache-miss between /video's start and cacheStream
	// (1-2 sec window) used to break playback for hls.js refresh / replay /
	// quality switch.
	inflightMu       sync.Mutex
	inflightResolves map[int64]chan struct{}

	// CDN clients. Mirage → direct uTLS; Aladdin → balancer proxy (its geo
	// bypass lives here, set by the constructor). The CDN session stays
	// authorized via the live WS edge_hash, so no per-failure proxy rotation
	// is needed (Spectre-style).
	defaultPlaylistClient *http.Client
	defaultSegmentClient  *http.Client

	// Background-refresh guard: idFile → struct{} marker, set via LoadOrStore.
	// Prevents launching duplicate browser resolves for the same stream while
	// a proactive refresh is already in flight.
	refreshInFlight sync.Map
}

// cancelOnClose wraps an io.ReadCloser and calls a cancel function when closed.
// Used to tie a context cancellation to the lifetime of an HTTP response body.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// initFileLog opens (or creates) a log file at {repoRoot}/{prefix}.log and
// attaches a zerolog.Logger that writes JSON entries there.
// It is safe to call with repoRoot="" — it will use current directory.
func (m *MirageChecker) initFileLog(repoRoot string) {
	if repoRoot == "" {
		repoRoot = "."
	}
	logPath := filepath.Join(repoRoot, m.prefix+".log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Warn().Err(err).Str("path", logPath).Msg("failed to open " + m.prefix + " log file")
		return
	}
	l := zerolog.New(f).With().Timestamp().Str("plugin", m.prefix).Logger()
	m.fileLog = &l
	m.logFile = f
	log.Info().Str("path", logPath).Msg(m.prefix + ": file logging enabled")
}

// flog returns the file logger if set, otherwise the global logger.
func (m *MirageChecker) flog() *zerolog.Logger {
	if m.fileLog != nil {
		return m.fileLog
	}
	gl := log.Logger
	return &gl
}

type mirageSearchItem struct {
	TokenMovie string
	Name       string
	Original   string
	Country    string
	Poster     string
	Year       int
	CategoryID int
}

type mirageView struct {
	TokenMovie string
	Category   int
}

type mirageEpisodeEntry struct {
	TranslationID int
	Translation   string
	Episode       int
	ID            int64
}

// Token exposes the configured token (xsearch adapter gate).
func (m *MirageChecker) Token() string { return m.token }

func NewMirageChecker(cfg config.Config) *MirageChecker {
	apiHost := strings.TrimSpace(strings.TrimRight(cfg.Online.Mirage.APIHost, "/"))
	if apiHost == "" {
		apiHost = "https://api.apbugall.org"
	}
	if !strings.Contains(apiHost, "://") {
		apiHost = "https://" + apiHost
	}

	linkHost := strings.TrimSpace(strings.TrimRight(cfg.Online.Mirage.LinkHost, "/"))
	if linkHost == "" {
		linkHost = "https://aport-as.allarknow.online"
	}
	if !strings.Contains(linkHost, "://") {
		linkHost = "https://" + linkHost
	}
	// Phantom migration 2026-05-12: Mirage переехал с quadrillion-as.stloadi.live
	// на aport-as.allarknow.online. Старый хост отдаёт несколько минут потом 403.
	if strings.Contains(linkHost, "quadrillion-as.stloadi.live") {
		linkHost = "https://aport-as.allarknow.online"
	}

	m := &MirageChecker{
		client:                httpclient.NewUTLS(14 * time.Second),
		defaultPlaylistClient: httpclient.NewUTLS(15 * time.Second),
		defaultSegmentClient:  &http.Client{Transport: httpclient.UTLSTransport},
		apiHost:               apiHost,
		linkHost:              linkHost,
		// Mirage needs an affiliate token — set [online.mirage] token in the
		// config; without it the source stays off.
		token: strings.TrimSpace(cfg.Online.Mirage.Token),
		prefix:           "mirage",
		kitName:          "Mirage",
		streamCache:      make(map[int64]*mirageStreamEntry),
		inflightResolves: make(map[int64]chan struct{}),
	}

	// Initialize Guard client for proof-of-browser tokens.
	// This provides fresh Accepts-Controls tokens that the CDN trusts,
	// preventing stream drops after ~4 minutes.
	getOrCreateGuardClient(linkHost, "mirage")

	return m
}

// tokenForCtx returns the per-user kit token if available, otherwise the global token.
func (m *MirageChecker) tokenForCtx(ctx context.Context) string {
	name := m.kitName
	if name == "" {
		name = "Mirage"
	}
	if kitToken, ok := kit.TokenOverride(ctx, name); ok {
		return kitToken
	}
	return m.token
}

func (m *MirageChecker) Handle(cfg config.Config, proxyLinks *proxylink.Manager) http.HandlerFunc {
	return m.HandleWithPrefix("mirage", cfg, proxyLinks)
}

func (m *MirageChecker) HandleWithPrefix(prefix string, cfg config.Config, proxyLinks *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := strings.Trim(strings.TrimPrefix(strings.ToLower(r.URL.Path), "/lite/"), "/")

		switch raw {
		case prefix:
			if parseBoolParam(r.URL.Query().Get("checksearch")) {
				show := m.checkSearch(r)
				writeCheckSearchResponse(w, show, pluginQualityBadgeGet(prefix))
				return
			}

			if strings.TrimSpace(m.tokenForCtx(r.Context())) == "" {
				writeGetsTVEmpty(w, parseBoolParam(r.URL.Query().Get("rjson")))
				return
			}
			m.index(w, r)
			return

		case prefix + "/video", prefix + "/video.m3u8":
			if strings.TrimSpace(m.tokenForCtx(r.Context())) == "" {
				writeJSON(w, http.StatusOK, map[string]any{})
				return
			}
			m.video(w, r, raw == prefix+"/video.m3u8", proxyLinks)
			return

		case prefix + "/stream", prefix + "/stream.m3u8":
			// Smart HLS proxy: serves m3u8 playlists with automatic CDN token refresh.
			// When the CDN returns 403 (token expired), re-resolves the stream URL
			// and retries transparently — prevents playback drops after ~15 min.
			m.streamHLS(w, r, proxyLinks)
			return

		case prefix + "/edge_hash":
			// Returns current CDN edge_hash for client-side direct streaming.
			// Client polls this every 30s to keep Accepts-Controls header fresh.
			hash := GetAnyEdgeHash()
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Access-Control-Allow-Origin", "*")
			fmt.Fprintf(w, `{"hash":%q,"ts":%d}`, hash, time.Now().UnixMilli())
			return

		case prefix + "-search":
			if parseBoolParam(r.URL.Query().Get("checksearch")) {
				show := m.checkSearch(r)
				writeCheckSearchResponse(w, show, pluginQualityBadgeGet(prefix))
				return
			}

			if strings.TrimSpace(m.tokenForCtx(r.Context())) == "" {
				writeGetsTVEmpty(w, parseBoolParam(r.URL.Query().Get("rjson")))
				return
			}
			m.spiderSearch(w, r)
			return
		}

		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error":    prefix + " route is not implemented in local mode",
			"balanser": raw,
		})
	}
}

func (m *MirageChecker) checkSearch(r *http.Request) bool {
	if strings.TrimSpace(m.apiHost) == "" {
		return false
	}
	if strings.TrimSpace(m.tokenForCtx(r.Context())) == "" {
		return m.probe(r, http.MethodHead, m.apiHost) || m.probe(r, http.MethodGet, m.apiHost)
	}

	q := r.URL.Query()
	orid := strings.TrimSpace(q.Get("orid"))
	imdbID := strings.TrimSpace(q.Get("imdb_id"))
	kinopoiskID, _ := strconv.ParseInt(strings.TrimSpace(q.Get("kinopoisk_id")), 10, 64)
	title := strings.TrimSpace(q.Get("title"))
	year, _ := strconv.Atoi(strings.TrimSpace(q.Get("year")))
	serial := strings.TrimSpace(q.Get("serial")) == "1"
	originalLanguage := strings.ToLower(strings.TrimSpace(q.Get("original_language")))

	if orid != "" || imdbID != "" || kinopoiskID > 0 {
		view, ok := m.viewByIDs(r, orid, imdbID, kinopoiskID)
		if ok && view.Category > 0 {
			return true
		}
		return m.probe(r, http.MethodHead, m.apiHost) || m.probe(r, http.MethodGet, m.apiHost)
	}

	if title == "" || year == 0 {
		return m.probe(r, http.MethodHead, m.apiHost) || m.probe(r, http.MethodGet, m.apiHost)
	}

	items, ok := m.SearchByTitle(r, title, serial)
	if !ok {
		return m.probe(r, http.MethodHead, m.apiHost) || m.probe(r, http.MethodGet, m.apiHost)
	}
	for _, item := range items {
		if item.CategoryID <= 0 || strings.TrimSpace(item.TokenMovie) == "" {
			continue
		}
		if normalizeSearchTitle(item.Name) != normalizeSearchTitle(title) {
			continue
		}
		if item.Year > 0 && item.Year != year && item.Year != year-1 && item.Year != year+1 {
			continue
		}
		if originalLanguage == "ru" && normalizeSearchTitle(item.Country) != normalizeSearchTitle("россия") {
			continue
		}
		return true
	}
	return len(items) > 0
}

func (m *MirageChecker) index(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	similar := parseBoolParam(q.Get("similar"))
	serial := strings.TrimSpace(q.Get("serial")) == "1"
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	originalLanguage := strings.ToLower(strings.TrimSpace(q.Get("original_language")))
	year, _ := strconv.Atoi(strings.TrimSpace(q.Get("year")))
	t, tSet := getsTVQueryInt(q.Get("t"))
	if !tSet {
		t = -1
	}
	s, sSet := getsTVQueryInt(q.Get("s"))
	if !sSet {
		s = -1
	}

	orid := strings.TrimSpace(q.Get("orid"))
	imdbID := strings.TrimSpace(q.Get("imdb_id"))
	kinopoiskID, _ := strconv.ParseInt(strings.TrimSpace(q.Get("kinopoisk_id")), 10, 64)

	if similar {
		m.spiderSearch(w, r)
		return
	}

	var (
		view  mirageView
		found bool
	)

	if orid != "" || imdbID != "" || kinopoiskID > 0 {
		view, found = m.viewByIDs(r, orid, imdbID, kinopoiskID)
	}

	if !found && title != "" {
		items, ok := m.SearchByTitle(r, title, serial)
		if !ok || len(items) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}

		selected := miragePickByTitle(items, title, year, originalLanguage)
		if selected.TokenMovie == "" {
			if len(items) > 1 {
				m.writeSimilar(w, r, rjson, title, originalTitle, items)
				return
			}
			selected = items[0]
		}

		view = mirageView{
			TokenMovie: selected.TokenMovie,
			Category:   selected.CategoryID,
		}
		found = view.TokenMovie != ""
	}

	if !found || strings.TrimSpace(view.TokenMovie) == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}
	if view.Category <= 0 {
		if serial {
			view.Category = 2
		} else {
			view.Category = 1
		}
	}

	frame, ok := m.fetchFrame(r, view.TokenMovie)
	if !ok {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(r)
	if view.Category == 1 || view.Category == 3 {
		m.writeMovie(w, rjson, host, title, originalTitle, view.TokenMovie, frame)
		return
	}
	m.writeSerial(w, rjson, host, title, originalTitle, view.TokenMovie, s, t, frame)
}

func (m *MirageChecker) spiderSearch(w http.ResponseWriter, r *http.Request) {
	title := strings.TrimSpace(r.URL.Query().Get("title"))
	rjson := parseBoolParam(r.URL.Query().Get("rjson"))
	if title == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	items, ok := m.searchByRawList(r, title)
	if !ok || len(items) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	m.writeSimilar(w, r, rjson, title, "", items)
}

func (m *MirageChecker) video(w http.ResponseWriter, r *http.Request, forcePlay bool, proxyLinks *proxylink.Manager) {
	q := r.URL.Query()
	idFile := mirageInt64(q.Get("id_file"))
	tokenMovie := strings.TrimSpace(q.Get("token_movie"))
	if idFile <= 0 || tokenMovie == "" {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	// Mark this idFile as "resolve in progress" so concurrent /stream.m3u8
	// cache-miss requests can wait for us instead of fast-failing with 404.
	// Lampa treats a 404 from /stream.m3u8 as fatal — without this, any
	// hls.js refresh / replay / quality switch firing a stream.m3u8 in the
	// 1-2 sec resolve window would kill playback.
	//
	// We deliberately do NOT pre-evict the existing cache entry here. The
	// previous code did `delete(m.streamCache, idFile)` before resolve to
	// stop old hls.js sessions (anti-429), but that left a hole where new
	// stream.m3u8 requests would 404. cacheStream() atomically replaces the
	// entry and closes the previous wsClient, which is enough — old CDN
	// URLs will 403 on their own (token mismatch) and hls.js will reload
	// the playlist, picking up fresh URLs from the new entry.
	m.beginInflightResolve(idFile)
	defer m.endInflightResolve(idFile)

	quals, cdnHeaders, wsUrl, wsSID, ok, res := m.resolveVideoQualities(r, idFile, tokenMovie)
	if !ok || len(quals) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	// Cache resolved qualities + CDN headers + start WS client for edge_hash.
	// cacheStream atomically replaces the previous entry (closes its wsClient,
	// preserves cooldown / proxy state). Concurrent stream.m3u8 readers that
	// were waiting on the inflight channel will see the fresh entry the moment
	// endInflightResolve() (deferred above) closes the channel.
	m.cacheStream(idFile, tokenMovie, quals, cdnHeaders, wsUrl, wsSID)

	// Store /movies/ POST params and BrowserCancel for lightweight refresh.
	if res != nil {
		if entry, eok := m.getCachedStream(idFile); eok {
			entry.mu.Lock()
			if res.MoviesURL != "" {
				entry.moviesURL = res.MoviesURL
				entry.moviesHeaders = res.MoviesHeaders
				entry.moviesBody = res.MoviesBody
				entry.idFile = idFile
			}
			// Store the browser-captured Origin/Referer so CDN segment requests
			// can use the exact values Spectre uses (Service.cs:137,141).
			if res.RequestOrigin != "" {
				entry.requestOrigin = res.RequestOrigin
			}
			if res.RequestReferer != "" {
				entry.requestReferer = res.RequestReferer
			}
			// EXPERIMENT: store browser cancel func for cleanup.
			if res.BrowserCancel != nil {
				// Close previous browser if any.
				if entry.browserCancel != nil {
					entry.browserCancel()
				}
				entry.browserCancel = res.BrowserCancel
				entry.browserCtx = res.BrowserCtx
				log.Info().Int64("idFile", idFile).Msg("mirage: EXPERIMENT — stored browser cancel + ctx on stream entry")
				// Watchdog: log when this browser context dies.
				go func(ctx context.Context, id int64) {
					<-ctx.Done()
					log.Warn().Int64("idFile", id).Err(ctx.Err()).
						Msg("mirage: WATCHDOG — browser context DIED")
				}(res.BrowserCtx, idFile)
			}
			entry.mu.Unlock()
			if res.MoviesURL != "" {
				log.Info().Str("moviesURL", res.MoviesURL).Msg("mirage: stored /movies/ params for lightweight refresh")
			}
		}
	}

	host := streamHostFromRequest(r)

	// Always route through /lite/mirage/stream.m3u8 (server-side proxy).
	//
	// Direct-CDN was attempted (handing raw rtbcdn.cloud URLs to the client)
	// but failed: Phantom CDN signs URLs against the **resolver's IP**, not
	// the requester's. Our server resolves → CDN binds URL to server IP →
	// client tries to fetch directly → IP mismatch → 403 even with right
	// Origin. Confirmed live (2026-05-17): same Origin on different IPs
	// gets 200 (cached on edge) vs 403 (cache miss + IP check).
	bestLabel := mirageBestQualityLabel(quals)
	proxiedQuals := make(map[string]string, len(quals))
	for qLabel := range quals {
		proxiedQuals[qLabel] = m.streamURL(host, idFile, qLabel, proxyLinks)
	}
	bestProxied := m.streamURL(host, idFile, bestLabel, proxyLinks)

	// Also wrap through legacy proxy for forcePlay (direct redirect).
	if forcePlay || parseBoolParam(q.Get("play")) {
		http.Redirect(w, r, bestProxied, http.StatusFound)
		return
	}

	// Build qualitys map for Lampa player quality selector.
	qualitysOut := make(map[string]any, len(proxiedQuals))
	for qLabel, pURL := range proxiedQuals {
		qualitysOut[qLabel] = pURL
	}

	resp := map[string]any{
		"method":   "play",
		"url":      bestProxied,
		"qualitys": qualitysOut,
		"quality":  qualitysOut,
	}

	writeJSON(w, http.StatusOK, resp)
}

// proxyStream wraps a raw CDN m3u8 URL through /proxy/ with custom
// Origin/Referer headers that the CDN requires. Without these headers
// the CDN returns 403 and the player shows manifestLoadError.
func (m *MirageChecker) proxyStream(r *http.Request, rawURL string, links *proxylink.Manager) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" || links == nil {
		return rawURL
	}
	if isStreamProxyDisabled("mirage") {
		return rawURL
	}
	origin := mirageOrigin(m.linkHost)
	headers := map[string]string{
		"Origin":  origin,
		"Referer": origin + "/",
	}
	host := streamHostFromRequest(r)
	reqIP := clientIP(r)
	encrypted := links.EncryptURIWithHeaders(rawURL, reqIP, "mirage", headers)
	if encrypted == "" {
		return rawURL
	}
	proxyURL := host + "/proxy/" + encrypted
	return proxyURL
}

// cacheStream stores resolved CDN quality URLs and headers for later use by streamHLS.
// cdnHeaders may be nil for fallback resolve paths; in that case, existing headers are preserved.
func (m *MirageChecker) cacheStream(idFile int64, tokenMovie string, quals map[string]string, cdnHeaders map[string]string, wsUrl, wsSID string) {
	m.streamMu.Lock()
	defer m.streamMu.Unlock()

	// Lazy cleanup: remove entries older than configured TTL, cap at configured max.
	now := time.Now()
	cacheMax := GetMirageStreamCacheMax()
	cacheTTL := GetMirageStreamCacheTTL()
	if len(m.streamCache) > cacheMax {
		for k, v := range m.streamCache {
			if now.Sub(v.resolvedAt) > cacheTTL {
				if v.wsClient != nil {
					v.wsClient.Close()
				}
				if v.browserCancel != nil {
					v.browserCancel()
				}
				delete(m.streamCache, k)
			}
		}
	}

	// Carry over state from the existing entry (if any).
	var prevWSClient *mirageWSClient
	var prevBrowserCancel context.CancelFunc
	var prevBrowserCtx context.Context
	var prevRequestOrigin, prevRequestReferer string
	if existing, ok := m.streamCache[idFile]; ok {
		// Use provided cdnHeaders; fall back to existing if nil (fallback resolve paths).
		if cdnHeaders == nil {
			cdnHeaders = existing.cdnHeaders
		}
		prevWSClient = existing.wsClient
		// Preserve browser context (alloha segment-fetch path).
		prevBrowserCancel = existing.browserCancel
		prevBrowserCtx = existing.browserCtx
		// Preserve browser-captured Origin/Referer across re-resolves.
		prevRequestOrigin = existing.requestOrigin
		prevRequestReferer = existing.requestReferer
	}

	// Start or reuse the WS client for edge_hash updates.
	var wsClient *mirageWSClient
	if wsUrl != "" && wsSID != "" {
		// Close old WS client if we have new credentials.
		if prevWSClient != nil {
			prevWSClient.Close()
		}
		// Mirage dials WS direct — its browser/CDN go direct too, so they
		// share the same exit IP (the lampac VPS). Do NOT pass a SOCKS5 here:
		// global mirageBrowser.socksProxy may have been set by alloha and would
		// route mirage WS through alloha's IP, mismatching CDN segments.
		wsClient = newMirageWSClient(wsUrl, wsSID, m.linkHost, "")
		go wsClient.Run()
		// Register in global registry so proxy handler (alloha etc.) can use it too.
		RegisterEdgeHashClient(wsClient, tokenMovie, m.linkHost)
		log.Info().Str("wsUrl", wsUrl).Int("sidLen", len(wsSID)).Msg("mirage: started WS client for edge_hash")
	} else {
		wsClient = prevWSClient // keep existing
	}

	newEntry := &mirageStreamEntry{
		quals:          quals,
		cdnHeaders:     cdnHeaders,
		tokenMovie:     tokenMovie,
		resolvedAt:     now,
		wsClient:       wsClient,
		browserCancel:  prevBrowserCancel,
		browserCtx:     prevBrowserCtx,
		guardLinkHost:  m.linkHost,
		requestOrigin:  prevRequestOrigin,
		requestReferer: prevRequestReferer,
	}
	m.streamCache[idFile] = newEntry
}

// getCachedStream returns the cached stream entry for a given idFile.
func (m *MirageChecker) getCachedStream(idFile int64) (*mirageStreamEntry, bool) {
	m.streamMu.RLock()
	defer m.streamMu.RUnlock()
	e, ok := m.streamCache[idFile]
	if !ok {
		return nil, false
	}
	return e, true
}

// mirageInflightResolveWait is the maximum time a /stream.m3u8 cache-miss
// request will block waiting for a parallel /video resolve to populate the
// cache. Resolve typically completes in 1-3s; we cap a bit higher than the
// browser timeout so a slightly slow resolve still wins over a fatal 404.
const mirageInflightResolveWait = 6 * time.Second

// beginInflightResolve registers idFile as having a /video resolve in flight.
// Concurrent /stream.m3u8 cache-miss requests can call waitForInflightResolve
// to block until the resolve finishes (channel closed). If a resolve is
// already in flight for this idFile, this is a no-op — the existing channel
// is kept and the second resolver simply joins the same waiters.
func (m *MirageChecker) beginInflightResolve(idFile int64) {
	m.inflightMu.Lock()
	defer m.inflightMu.Unlock()
	if _, ok := m.inflightResolves[idFile]; ok {
		// Resolve already in flight — keep the existing channel so anyone
		// already waiting still gets notified when the original finishes.
		return
	}
	m.inflightResolves[idFile] = make(chan struct{})
}

// endInflightResolve closes the inflight channel and removes the entry.
// Idempotent: safe to call multiple times for the same idFile (defensive).
func (m *MirageChecker) endInflightResolve(idFile int64) {
	m.inflightMu.Lock()
	ch, ok := m.inflightResolves[idFile]
	if ok {
		delete(m.inflightResolves, idFile)
	}
	m.inflightMu.Unlock()
	if ok {
		close(ch)
	}
}

// waitForInflightResolve blocks until the parallel /video resolve for idFile
// finishes (returns true) or the context / timeout fires (returns false). If
// no resolve is in flight, returns false immediately so the caller can
// fast-fail. Used by /stream.m3u8 to recover from the brief window where the
// cache is empty but a fresh resolve is about to populate it.
func (m *MirageChecker) waitForInflightResolve(ctx context.Context, idFile int64) bool {
	m.inflightMu.Lock()
	ch, ok := m.inflightResolves[idFile]
	m.inflightMu.Unlock()
	if !ok {
		return false
	}
	timer := time.NewTimer(mirageInflightResolveWait)
	defer timer.Stop()
	select {
	case <-ch:
		return true
	case <-timer.C:
		return false
	case <-ctx.Done():
		return false
	}
}

// streamURL builds a URL pointing to the smart /stream endpoint.
// Uses .m3u8 extension so HLS players recognize it as a playlist.
func (m *MirageChecker) streamURL(host string, idFile int64, quality string, _ *proxylink.Manager) string {
	return host + "/lite/" + m.prefix + "/stream.m3u8?id=" + strconv.FormatInt(idFile, 10) + "&q=" + url.QueryEscape(quality)
}

// mirageBestQualityLabel returns the label of the best quality in the map.
func mirageBestQualityLabel(quals map[string]string) string {
	for _, q := range []string{"2160p", "2160", "1440p", "1440", "1080p", "1080", "720p", "720", "480p", "480", "360p", "360", "auto"} {
		if _, ok := quals[q]; ok {
			return q
		}
	}
	for q := range quals {
		return q
	}
	return "auto"
}

// mirageStreamRefreshAt is the entry age at which streamHLS / streamCDNSegment
// kicks off a non-blocking background re-resolve of the CDN URLs. Phantom CDN
// signs URLs with an absolute TTL (~10 min) that the WS edge_hash does NOT
// extend — the live token authorizes a still-valid signed URL but cannot revive
// an expired one. Spectre runs on a CDN that apparently lacks this absolute
// limit (their stream lives indefinitely); we hit it at ~8 min in production,
// so refreshing at 7 min keeps a ~1-3 min safety margin and is invisible to the
// player (current request continues with valid URLs, the next playlist reload
// after the goroutine finishes picks up the fresh ones).
const mirageStreamRefreshAt = 7 * time.Minute

// maybeBackgroundRefresh kicks off a fresh browser resolve in a goroutine if
// the cached entry is older than mirageStreamRefreshAt and no refresh is
// already in flight for this idFile. Safe to call on every request — the
// LoadOrStore guard collapses concurrent triggers to a single launch.
func (m *MirageChecker) maybeBackgroundRefresh(r *http.Request, idFile int64, entry *mirageStreamEntry) {
	if time.Since(entry.resolvedAt) < mirageStreamRefreshAt {
		return
	}
	if _, loaded := m.refreshInFlight.LoadOrStore(idFile, struct{}{}); loaded {
		return
	}
	tokenMovie := entry.tokenMovie
	// Detach from the client request: r.Context() is cancelled the moment the
	// handler returns, but the resolve needs minutes to complete.
	bgReq := r.Clone(context.Background())
	go func() {
		defer m.refreshInFlight.Delete(idFile)
		m.flog().Info().Int64("idFile", idFile).
			Str("age", time.Since(entry.resolvedAt).Round(time.Second).String()).
			Msg(m.prefix + ": proactive background refresh starting")
		newQuals, cdnHeaders, wsUrl, wsSID, ok, _ := m.resolveVideoQualities(bgReq, idFile, tokenMovie)
		if !ok || len(newQuals) == 0 {
			m.flog().Warn().Int64("idFile", idFile).Msg(m.prefix + ": proactive background refresh failed")
			return
		}
		m.cacheStream(idFile, tokenMovie, newQuals, cdnHeaders, wsUrl, wsSID)
		m.flog().Info().Int64("idFile", idFile).Int("quals", len(newQuals)).
			Msg(m.prefix + ": proactive background refresh ok")
	}()
}

// streamHLS is a smart HLS proxy endpoint that handles ALL HLS requests
// (master playlist, variant playlists, and .ts/.m4s segments).
//
// URL format: /lite/exmirage/stream.m3u8?id=838987&q=2160p[&sub=index-f1-v1.m3u8]
//
// - Without &sub: fetches the master/quality m3u8 from CDN
// - With &sub: fetches a sub-resource (variant playlist or segment) relative to the base CDN path
//
// Stays Spectre-faithful in the hot path (no retry/rotation/cache); the only
// extra is a proactive background refresh before the absolute URL TTL fires,
// which Spectre itself doesn't need but Phantom's URL signing apparently does.
func (m *MirageChecker) streamHLS(w http.ResponseWriter, r *http.Request, proxyLinks *proxylink.Manager) {
	fl := m.flog()
	q := r.URL.Query()
	idFile := mirageInt64(q.Get("id"))
	quality := strings.TrimSpace(q.Get("q"))
	sub := strings.TrimSpace(q.Get("sub"))
	if quality == "" {
		quality = "auto"
	}

	if idFile <= 0 {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}

	entry, ok := m.getCachedStream(idFile)
	if !ok {
		// Cache miss: a /video resolve for this idFile may be in flight (1-2s
		// window between browser launch and cacheStream). Wait for it instead
		// of 404 — Lampa treats a 404 from stream.m3u8 as fatal. If there's no
		// resolve to wait for, 502 lets hls.js retry (manifestLoadError).
		if m.waitForInflightResolve(r.Context(), idFile) {
			entry, ok = m.getCachedStream(idFile)
		}
		if !ok {
			http.Error(w, "stream not found", http.StatusBadGateway)
			return
		}
	}

	// Proactive refresh before Phantom's absolute URL TTL (~8-10 min).
	m.maybeBackgroundRefresh(r, idFile, entry)

	baseURL := entry.quals[quality]
	if baseURL == "" {
		baseURL = mirageBestQuality(entry.quals)
	}
	if baseURL == "" {
		http.Error(w, "quality not found", http.StatusNotFound)
		return
	}

	// base = https://cdn/55/TOKEN/master.m3u8
	// sub=""  → fetch master.m3u8
	// sub="index-f1-v1.m3u8" → fetch https://cdn/55/TOKEN/index-f1-v1.m3u8
	targetURL := baseURL
	if sub != "" {
		targetURL = mirageHLSPatch(baseURL) + sub
	}

	isSubSegment := sub != "" &&
		!strings.HasSuffix(strings.ToLower(sub), ".m3u8") &&
		!strings.HasSuffix(strings.ToLower(sub), ".m3u")

	origin := mirageOrigin(m.linkHost)

	// CORS headers for cross-origin player access.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Range, Content-Type")
	w.Header().Set("Access-Control-Expose-Headers", "Content-Length, Content-Range")

	if isSubSegment {
		m.streamCDNSegment(w, r, targetURL, origin, entry, idFile, quality, sub)
		return
	}

	// Playlist: fetch into memory (small) for rewriting. The CDN auth headers
	// (static Bearer + live edge_hash + captured Origin/Referer) are applied by
	// fetchCDN — same per-request discipline as Spectre's proxy hook.
	body, statusCode, contentType, sentH, finalURL, err := m.fetchCDN(r.Context(), targetURL, origin, entry.cdnHeadersWithPCHash())
	if err != nil {
		http.Error(w, "CDN fetch failed", http.StatusBadGateway)
		return
	}

	// CDN may redirect to a URL with a fresh token — use it as the new quals
	// base so segment requests derive from the post-redirect path.
	if statusCode == http.StatusOK && finalURL != "" && finalURL != targetURL {
		fl.Info().Str("redirected", finalURL).Str("original", targetURL).Msg(m.prefix + ": CDN redirected playlist, updating quals base")
		m.streamMu.Lock()
		if e, ok := m.streamCache[idFile]; ok && e.quals != nil {
			e.quals[quality] = finalURL
		}
		m.streamMu.Unlock()
		targetURL = finalURL
	}

	if statusCode != http.StatusOK {
		// Let hls.js retry — no server-side re-resolve (Spectre relies on the
		// WS edge_hash keeping the session alive instead).
		http.Error(w, fmt.Sprintf("CDN status %d", statusCode), statusCode)
		return
	}

	mirageSetPxOrigHeaders(w, targetURL, origin, sentH)

	// Rewrite m3u8: sub-playlists/segments route back through this endpoint so
	// each carries fresh headers. When no_stream_proxy lists this plugin,
	// rewrite to the direct CDN base instead.
	if strings.Contains(contentType, "mpegurl") || strings.Contains(contentType, "m3u") ||
		strings.Contains(contentType, "text/plain") || strings.HasSuffix(targetURL, ".m3u8") || strings.HasSuffix(targetURL, ".m3u") {
		cdnBase := ""
		if isStreamProxyDisabled(m.prefix) {
			cdnBase = mirageHLSPatch(targetURL)
		}
		rewritten := m.rewriteM3UForStreamSelfWithBase(string(body), r, idFile, quality, cdnBase)
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Content-Length", strconv.Itoa(len(rewritten)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(rewritten))
		return
	}

	// Non-m3u8 content: pass through as-is.
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// streamCDNSegment streams a .ts/.m4s segment from the CDN straight to the
// client via io.Copy (no buffering). It injects the CDN segment headers (static
// Bearer + live edge_hash + captured Origin/Referer) and advances the WS
// playback position from the segment number — mirroring Spectre's
// ProxyApiCreateHttpRequest hook (Service.cs:92-146). No retry / rotation /
// fallback: hls.js retries any transient CDN error on its own, and the WS
// edge_hash keeps the CDN session authorized.
func (m *MirageChecker) streamCDNSegment(w http.ResponseWriter, r *http.Request,
	targetURL, origin string, entry *mirageStreamEntry, idFile int64, quality, sub string) {

	fl := m.flog()

	// Spectre current_time tracking: derive playback position from /seg-N-/ so
	// the next WS "playing" heartbeat (and any "seeked") matches what the player
	// is actually fetching, keeping the CDN session alive / edge_hash rotating.
	// The entry owns its WS client, so this updates its own session's position.
	if entry != nil && entry.wsClient != nil {
		entry.wsClient.updateCurrentTimeFromURL(targetURL)
	}

	resp, sentH, err := m.fetchCDNStream(r.Context(), targetURL, origin, entry.cdnHeadersForSegment())
	if err != nil {
		fl.Warn().Err(err).Str("segment", sub).Int64("idFile", idFile).Msg(m.prefix + ": CDN segment fetch failed")
		http.Error(w, "CDN fetch failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fl.Warn().Int("cdnStatus", resp.StatusCode).Str("segment", sub).Int64("idFile", idFile).
			Dur("urlAge", time.Since(entry.resolvedAt)).Msg(m.prefix + ": CDN segment non-200")
		http.Error(w, fmt.Sprintf("CDN status %d", resp.StatusCode), resp.StatusCode)
		return
	}

	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		if strings.HasSuffix(sub, ".m4s") {
			ct = "video/iso.segment"
		} else {
			ct = "video/mp2t"
		}
	}
	w.Header().Set("Content-Type", ct)
	// Forward Content-Length only when it matches the bytes we'll write. If Go
	// transparently decompressed the body (resp.Uncompressed), the header is the
	// compressed length and would truncate the segment — let Go use chunked.
	if resp.ContentLength > 0 && !resp.Uncompressed {
		w.Header().Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	}
	mirageSetPxOrigHeaders(w, targetURL, origin, sentH)
	w.WriteHeader(http.StatusOK)

	written, copyErr := io.Copy(w, resp.Body)
	if copyErr != nil {
		if isStreamCopyErr(copyErr) {
			fl.Info().Err(copyErr).Str("segment", sub).Int64("written", written).Msg(m.prefix + ": segment client disconnected (normal)")
		} else {
			fl.Warn().Err(copyErr).Str("segment", sub).Int64("written", written).Msg(m.prefix + ": segment stream copy failed")
		}
	}
}

// flatHeaders converts http.Header to a simple map[string]string for logging.
// Multi-value headers are joined with "; ".
func flatHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, vv := range h {
		out[k] = strings.Join(vv, "; ")
	}
	return out
}

// mirageSetPxOrigHeaders sets the Px-Orig-Headers response header with a JSON
// object containing the CDN target host, origin, referer, and all upstream
// request headers. Called BEFORE WriteHeader. Always set (no debug flag needed).
func mirageSetPxOrigHeaders(w http.ResponseWriter, cdnURL, origin string, sentHeaders http.Header) {
	u, _ := url.Parse(cdnURL)
	h := map[string]string{
		"host":   "",
		"origin": origin,
	}
	if u != nil {
		h["host"] = u.Host
	}
	// Flatten all headers sent to CDN into the map.
	for k, vv := range sentHeaders {
		key := strings.ToLower(k)
		if key == "host" || key == "origin" {
			continue // already set above
		}
		h[key] = strings.Join(vv, ", ")
	}
	if b, err := stdjson.Marshal(h); err == nil {
		w.Header().Set("Px-Orig-Headers", string(b))
	}
}

// isStreamCopyErr returns true for errors that are normal during HLS streaming:
// player disconnects, quality switches, seeks — all cause the client connection
// to close mid-transfer which is expected behavior, not a CDN issue.
func isStreamCopyErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "context canceled") ||
		strings.Contains(msg, "context deadline exceeded") ||
		strings.Contains(msg, "client disconnected")
}

// fetchCDN fetches a URL from the CDN with the required Origin/Referer headers
// plus any additional JS-generated CDN auth headers (Borth, Authorization, etc.).
// Used for playlists (small responses that need body in memory for rewriting).
// proxyIdx selects which proxy from the rotation pool to use.
// Returns: body, statusCode, contentType, reqHeaders (sent to CDN), error.
func (m *MirageChecker) fetchCDN(ctx context.Context, rawURL, origin string, extraHeaders map[string]string) ([]byte, int, string, http.Header, string, error) {
	fl := m.flog()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, "", nil, "", err
	}
	miragSetBrowserHeaders(req, origin)

	// Apply JS-generated CDN auth headers (Authorizations, Accepts-Controls).
	// Spectre Service.cs:128-142 sends only Accepts-Controls (= edge_hash) and
	// the static Authorizations Bearer — pc_hash is NOT in the Spectre set, so
	// we drop it; sending it appears to flag the request as bot-like on newer
	// CDN nodes that have hardened header checks.
	for k, v := range extraHeaders {
		if strings.EqualFold(k, "pc_hash") {
			continue
		}
		req.Header.Set(k, v)
	}

	// Snapshot request headers before Do (for Px-Orig-Headers).
	sentHeaders := req.Header.Clone()

	// Log request headers sent to CDN
	fl.Info().Str("url", rawURL).Interface("reqHeaders", flatHeaders(req.Header)).Msg(m.prefix + ": fetchCDN request")

	resp, err := m.defaultPlaylistClient.Do(req)
	if err != nil {
		return nil, 0, "", sentHeaders, "", err
	}
	defer resp.Body.Close()

	// Log CDN response headers
	fl.Info().Str("url", rawURL).Int("status", resp.StatusCode).Interface("respHeaders", flatHeaders(resp.Header)).Msg(m.prefix + ": fetchCDN response")

	body, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	if err != nil {
		return nil, resp.StatusCode, "", sentHeaders, "", err
	}

	ct := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]))

	// Return the final URL after any redirects — CDN may redirect to a URL
	// with a fresh token, which must be used as the base for segment requests.
	finalURL := rawURL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	return body, resp.StatusCode, ct, sentHeaders, finalURL, nil
}

// fetchCDNStream opens a streaming connection to CDN for segments (.ts/.m4s).
// Returns the http.Response and the request headers sent to CDN (for Px-Orig-Headers).
// Caller MUST close resp.Body. proxyIdx selects the proxy from the rotation pool.
//
// Uses context.Background() (detached from client request) so HLS player
// cancellations (quality switches, seeks) don't abort the CDN connection.
// No fixed timeout — large 4K segments (20-50 MB) can stream as long as needed.
func (m *MirageChecker) fetchCDNStream(ctx context.Context, rawURL, origin string, extraHeaders map[string]string) (*http.Response, http.Header, error) {
	fl := m.flog()

	// Detached from the client request: HLS player cancellations (quality
	// switches, seeks) must not abort an in-flight CDN segment download.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, err
	}
	miragSetBrowserHeaders(req, origin)

	// Apply JS-generated CDN auth headers (Authorizations, Accepts-Controls).
	// Spectre Service.cs:128-142 doesn't send pc_hash — drop it.
	for k, v := range extraHeaders {
		if strings.EqualFold(k, "pc_hash") {
			continue
		}
		req.Header.Set(k, v)
	}

	// Snapshot request headers before Do (for Px-Orig-Headers).
	sentHeaders := req.Header.Clone()

	// Log request headers sent to CDN
	fl.Info().Str("url", rawURL).Interface("reqHeaders", flatHeaders(req.Header)).Msg(m.prefix + ": fetchCDNStream request")

	resp, err := m.defaultSegmentClient.Do(req)
	if err != nil {
		return nil, sentHeaders, err
	}

	// Log CDN response headers
	fl.Info().Str("url", rawURL).Int("status", resp.StatusCode).Interface("respHeaders", flatHeaders(resp.Header)).Msg(m.prefix + ": fetchCDNStream response")

	return resp, sentHeaders, nil
}

// mirageHLSHostRe extracts the scheme://host prefix of an URL. Pre-compiled
// at package init — mirageHLSHost is called per HLS segment (hot path).
var mirageHLSHostRe = regexp.MustCompile(`(?i)(https?://[^/]+)/`)

// mirageHLSHost extracts scheme://host from a URL.
func mirageHLSHost(uri string) string {
	m := mirageHLSHostRe.FindStringSubmatch(uri)
	if len(m) > 1 {
		return m[1]
	}
	return ""
}

// mirageHLSPatch extracts the base path (without filename) from a URL.
func mirageHLSPatch(uri string) string {
	m := mirageRePathNoFile.FindStringSubmatch(uri)
	if len(m) > 1 {
		return m[1]
	}
	if strings.HasSuffix(uri, "/") {
		return uri
	}
	return ""
}

// mirageResolveRelURI resolves a relative URI against an HLS base.
func mirageResolveRelURI(uri, hlsHost, hlsPatch string) string {
	switch {
	case strings.HasPrefix(uri, "//"):
		return "https:" + uri
	case strings.HasPrefix(uri, "/"):
		return hlsHost + uri
	case strings.HasPrefix(uri, "./"):
		return hlsPatch + strings.TrimPrefix(uri, "./")
	default:
		return hlsPatch + uri
	}
}

// rewriteM3UForStream rewrites an m3u8 playlist so that all segment and variant
// URLs go through /proxy/ with proper CDN headers embedded.
func (m *MirageChecker) rewriteM3UForStream(src string, r *http.Request, baseURL string, links *proxylink.Manager) string {
	if links == nil {
		return src
	}

	origin := mirageOrigin(m.linkHost)
	headers := map[string]string{
		"Origin":  origin,
		"Referer": origin + "/",
	}
	reqIP := clientIP(r)
	host := streamHostFromRequest(r)
	proxyBase := host + "/proxy"

	hlsHost := mirageHLSHost(baseURL)
	hlsPatch := mirageHLSPatch(baseURL)

	out := mirageReHTTPLinks.ReplaceAllStringFunc(src, func(link string) string {
		enc := links.EncryptURIWithHeaders(link, reqIP, "mirage", headers)
		if enc == "" {
			return link
		}
		return proxyBase + "/" + enc
	})

	out = mirageReLineURI.ReplaceAllStringFunc(out, func(match string) string {
		g := mirageReLineURI.FindStringSubmatch(match)
		if len(g) < 3 {
			return match
		}
		uri := g[2]
		if strings.Contains(uri, "#") || strings.Contains(uri, "\"") || strings.HasPrefix(strings.ToLower(uri), "http") {
			return match
		}
		abs := mirageResolveRelURI(uri, hlsHost, hlsPatch)
		enc := links.EncryptURIWithHeaders(abs, reqIP, "mirage", headers)
		if enc == "" {
			return match
		}
		return g[1] + proxyBase + "/" + enc
	})

	out = mirageReQuotedURI.ReplaceAllStringFunc(out, func(match string) string {
		g := mirageReQuotedURI.FindStringSubmatch(match)
		if len(g) < 3 {
			return match
		}
		uri := g[2]
		if strings.Contains(uri, "\"") || strings.HasPrefix(strings.ToLower(uri), "http") {
			return match
		}
		abs := mirageResolveRelURI(uri, hlsHost, hlsPatch)
		enc := links.EncryptURIWithHeaders(abs, reqIP, "mirage", headers)
		if enc == "" {
			return match
		}
		return g[1] + proxyBase + "/" + enc
	})

	return out
}

// rewriteM3UForStreamSelf rewrites an m3u8 playlist so that all sub-URLs
// (variant playlists, segments) route through /stream.m3u8?sub= instead of /proxy/.
// This way ALL requests go through our smart endpoint and benefit from
// proactive CDN token/header refresh every 5 min.
func (m *MirageChecker) rewriteM3UForStreamSelf(src string, r *http.Request, idFile int64, quality string) string {
	return m.rewriteM3UForStreamSelfWithBase(src, r, idFile, quality, "")
}

// rewriteM3UForStreamSelfWithBase rewrites an m3u8 playlist.
// If cdnBasePath is non-empty, media segments (.ts/.m4s/.mp4/.vtt) are rewritten
// to absolute CDN URLs so clients download them directly (no server proxy).
// Sub-playlists (.m3u8) always route through /stream.m3u8?sub= so the server
// can keep refreshing tokens and injecting headers.
func (m *MirageChecker) rewriteM3UForStreamSelfWithBase(src string, r *http.Request, idFile int64, quality, cdnBasePath string) string {
	host := streamHostFromRequest(r)
	streamBase := host + "/lite/" + m.prefix + "/stream.m3u8?id=" + strconv.FormatInt(idFile, 10) + "&q=" + url.QueryEscape(quality)

	rewriteURI := func(prefix, uri string) string {
		// Skip comments (#...) and quoted strings
		if strings.HasPrefix(uri, "#") || strings.Contains(uri, "\"") {
			return prefix + uri
		}
		// Skip absolute URLs — they have their own tokens
		if strings.HasPrefix(strings.ToLower(uri), "http") {
			return prefix + uri
		}

		// If cdnBasePath is set and this is a media segment (not a playlist),
		// rewrite to nginx /cdn-stream/ reverse proxy so browser downloads via
		// nginx (which injects correct Origin header) instead of Go proxy.
		// cdnBasePath example: "https://9a6-f80-500gv.stream-balancer-allo-1.live/0/TOKEN/"
		// → "/cdn-stream/9a6-f80-500gv/0/TOKEN/seg.ts"
		if cdnBasePath != "" && !isM3U8URI(uri) {
			// When no_stream_proxy is active: direct CDN URL (client browser
			// fetches with injected edge_hash header via cdn_direct.js plugin).
			if isStreamProxyDisabled(m.prefix) {
				return prefix + cdnBasePath + uri
			}
			// Otherwise: nginx /cdn-stream/ reverse proxy.
			if proxyPath := cdnToNginxProxy(cdnBasePath, host); proxyPath != "" {
				return prefix + proxyPath + uri
			}
			return prefix + cdnBasePath + uri
		}

		// Sub-playlists (.m3u8) and all URIs when cdnBasePath is empty
		// route through our server endpoint.
		return prefix + streamBase + "&sub=" + url.QueryEscape(uri)
	}

	// Strip VOD markers from variant playlists so the HLS player treats them
	// as LIVE and re-fetches periodically (~every target duration = 6s).
	// This is critical because CDN URL tokens in the playlist expire after ~10 min.
	// Without this, the player caches the VOD playlist forever and never gets fresh URLs.
	if cdnBasePath != "" && strings.Contains(src, "EXTINF") {
		src = strings.Replace(src, "#EXT-X-PLAYLIST-TYPE:VOD\n", "", 1)
		src = strings.Replace(src, "#EXT-X-PLAYLIST-TYPE:VOD\r\n", "", 1)
		src = strings.Replace(src, "#EXT-X-ENDLIST\n", "", 1)
		src = strings.Replace(src, "#EXT-X-ENDLIST\r\n", "", 1)
		src = strings.Replace(src, "#EXT-X-ENDLIST", "", 1)
	}

	// Replace relative URIs (like "index-f1-v1.m3u8" or "seg-1-v1-a1.ts")
	out := mirageReLineURI.ReplaceAllStringFunc(src, func(match string) string {
		g := mirageReLineURI.FindStringSubmatch(match)
		if len(g) < 3 {
			return match
		}
		return rewriteURI(g[1], g[2])
	})

	// Also handle URI="" attributes (like audio group URIs)
	out = mirageReQuotedURI.ReplaceAllStringFunc(out, func(match string) string {
		g := mirageReQuotedURI.FindStringSubmatch(match)
		if len(g) < 3 {
			return match
		}
		uri := g[2]
		if strings.Contains(uri, "\"") || strings.HasPrefix(strings.ToLower(uri), "http") {
			return match
		}
		return rewriteURI(g[1], uri)
	})

	return out
}

// isM3U8URI returns true if the URI looks like an HLS playlist (not a segment).
func isM3U8URI(uri string) bool {
	lower := strings.ToLower(uri)
	return strings.HasSuffix(lower, ".m3u8") || strings.HasSuffix(lower, ".m3u")
}

// cdnToNginxProxy converts a CDN base URL to a /cdn-stream/ nginx proxy path.
// Input:  "https://9a6-f80-500gv.stream-balancer-allo-1.live/0/TOKEN/"
// Output: "https://beta.l-vid.online/cdn-stream/9a6-f80-500gv/0/TOKEN/"
// The nginx location /cdn-stream/{node}/ proxies to https://{node}.stream-balancer-allo-1.live/
// with the correct Origin header.
func cdnToNginxProxy(cdnBasePath, serverHost string) string {
	// Parse the CDN URL to extract the node name
	// Host format: "9a6-f80-500gv.stream-balancer-allo-1.live"
	u, err := url.Parse(cdnBasePath)
	if err != nil || u.Host == "" {
		return ""
	}

	// Extract node: everything before ".stream-balancer-"
	idx := strings.Index(u.Host, ".stream-balancer-")
	if idx <= 0 {
		return ""
	}
	node := u.Host[:idx]

	// Build: serverHost + /cdn-stream/NODE + path
	return serverHost + "/cdn-stream/" + node + u.Path
}

func (m *MirageChecker) viewByIDs(r *http.Request, orid, imdbID string, kinopoiskID int64) (mirageView, bool) {
	params := url.Values{}
	params.Set("token", m.tokenForCtx(r.Context()))
	if strings.TrimSpace(orid) != "" {
		params.Set("token_movie", strings.TrimSpace(orid))
	}
	if strings.TrimSpace(imdbID) != "" {
		params.Set("imdb", strings.TrimSpace(imdbID))
	}
	if kinopoiskID > 0 {
		params.Set("kp", strconv.FormatInt(kinopoiskID, 10))
	}

	root, ok := m.fetchAPIMap(r, params)
	if !ok {
		return mirageView{}, false
	}

	data := mirageMap(root["data"])
	if len(data) == 0 {
		return mirageView{}, false
	}

	tokenMovie := strings.TrimSpace(mirageStr(data["token_movie"]))
	if tokenMovie == "" {
		tokenMovie = strings.TrimSpace(orid)
	}
	if tokenMovie == "" {
		return mirageView{}, false
	}

	category := mirageInt(data["category"])
	if category <= 0 {
		category = mirageInt(data["category_id"])
	}
	return mirageView{
		TokenMovie: tokenMovie,
		Category:   category,
	}, true
}

func (m *MirageChecker) SearchByTitle(r *http.Request, title string, serial bool) ([]mirageSearchItem, bool) {
	params := url.Values{}
	params.Set("token", m.tokenForCtx(r.Context()))
	params.Set("name", title)
	if serial {
		params.Set("list", "serial")
	} else {
		params.Set("list", "movie")
	}
	return m.fetchSearchItems(r, params)
}

func (m *MirageChecker) searchByRawList(r *http.Request, title string) ([]mirageSearchItem, bool) {
	params := url.Values{}
	params.Set("token", m.tokenForCtx(r.Context()))
	params.Set("name", title)
	params.Set("list", "")
	return m.fetchSearchItems(r, params)
}

func (m *MirageChecker) fetchSearchItems(r *http.Request, params url.Values) ([]mirageSearchItem, bool) {
	root, ok := m.fetchAPIMap(r, params)
	if !ok {
		return nil, false
	}

	rows := mirageSlice(root["data"])
	items := make([]mirageSearchItem, 0, len(rows))
	for _, row := range rows {
		obj := mirageMap(row)
		if len(obj) == 0 {
			continue
		}
		item := mirageSearchItem{
			TokenMovie: strings.TrimSpace(mirageStr(obj["token_movie"])),
			Name:       strings.TrimSpace(mirageStr(obj["name"])),
			Original:   strings.TrimSpace(mirageStr(obj["original_name"])),
			Country:    strings.TrimSpace(mirageStr(obj["country"])),
			Poster:     strings.TrimSpace(mirageStr(obj["poster"])),
			Year:       mirageInt(obj["year"]),
			CategoryID: mirageInt(obj["category_id"]),
		}
		if item.CategoryID <= 0 {
			item.CategoryID = mirageInt(obj["category"])
		}
		if item.TokenMovie == "" {
			continue
		}
		items = append(items, item)
	}
	return items, true
}

func (m *MirageChecker) fetchAPIMap(r *http.Request, params url.Values) (map[string]any, bool) {
	target := strings.TrimRight(m.apiHost, "/") + "/?" + params.Encode()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("X-Lampac-Go", "1")

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false
	}

	var root map[string]any
	if err := decodeJSONLimited(resp.Body, 4<<20, &root); err != nil {
		return nil, false
	}
	return root, true
}

func (m *MirageChecker) fetchFrame(r *http.Request, tokenMovie string) (map[string]any, bool) {
	tokenMovie = strings.TrimSpace(tokenMovie)
	if tokenMovie == "" || strings.TrimSpace(m.linkHost) == "" {
		return nil, false
	}

	target := strings.TrimRight(m.linkHost, "/") + "/?token_movie=" + url.QueryEscape(tokenMovie) + "&token=" + url.QueryEscape(strings.TrimSpace(m.tokenForCtx(r.Context())))
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("Referer", "https://kinogo-go.tv/")
	req.Header.Set("Sec-Fetch-Dest", "iframe")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	req.Header.Set("X-Lampac-Go", "1")

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 6<<20))
	if err != nil {
		return nil, false
	}
	htmlBody := string(body)

	encoded := strings.TrimSpace(submatch1(mirageFileListSingleRe, htmlBody))
	if encoded == "" {
		encoded = strings.TrimSpace(submatch1(mirageFileListDoubleRe, htmlBody))
	}
	if encoded == "" {
		return nil, false
	}

	decoded := mirageDecodeJSONString(encoded)
	if decoded == "" {
		return nil, false
	}

	var root map[string]any
	if err := stdjson.Unmarshal([]byte(decoded), &root); err != nil {
		return nil, false
	}

	all := mirageMap(root["all"])
	if len(all) == 0 {
		return nil, false
	}
	return all, true
}

// resolveVideo returns the best stream URL for backwards compatibility.
func (m *MirageChecker) resolveVideo(r *http.Request, idFile int64, tokenMovie string) (string, bool) {
	quals, _, _, _, ok, _ := m.resolveVideoQualities(r, idFile, tokenMovie)
	if !ok || len(quals) == 0 {
		return "", false
	}
	return mirageBestQuality(quals), true
}

// resolveVideoQualities resolves all available quality → stream URL mappings.
// Returns map like {"2160p": "https://...m3u8", "1080p": "https://...m3u8"}.
// resolveVideoQualities returns (quals, cdnHeaders, ok).
// cdnHeaders may be nil for fallback paths that don't capture browser-generated headers.
func (m *MirageChecker) resolveVideoQualities(r *http.Request, idFile int64, tokenMovie string) (quals map[string]string, cdnHeaders map[string]string, wsUrl string, sid string, ok bool, browserRes *mirageResolveResult) {
	if idFile <= 0 || strings.TrimSpace(m.linkHost) == "" {
		return
	}

	// Primary: use headless Chrome to resolve video (like .NET Playwright).
	// The CDN requires JS-generated auth headers (Authorizations, Accepts-Controls)
	// that cannot be replicated via plain HTTP.
	//
	// Dispatch: chromedp engine → existing direct path (unchanged
	// 1440-line flow with WS experiment, edge_hash extraction, etc.).
	// Any other engine (rod / playwright) → mirageResolveViaFacade,
	// which is a 1:1 port through the internal/browser facade with the
	// experimental WS bits omitted.
	resolveFn := mirageResolveViaBrowser
	if eng := browser.ForBalancer("mirage"); eng != nil && eng.Name() != "chromedp" {
		resolveFn = mirageResolveViaFacade
	}
	if bRes, resOK := resolveFn(r.Context(), m.linkHost, m.tokenForCtx(r.Context()), idFile, tokenMovie, m.prefix); resOK {
		browserRes = bRes
		wsUrl = bRes.WSUrl
		sid = bRes.SID
		cdnHeaders = bRes.CDNHeaders
		// Try to extract all qualities from raw /movies/ JSON body
		if q := mirageExtractAllHLSQualities(bRes.RawBody); len(q) > 0 {
			normalized := make(map[string]string, len(q))
			for qk, u := range q {
				key := qk
				if _, err := strconv.Atoi(qk); err == nil {
					key = qk + "p"
				}
				normalized[key] = u
			}
			quals = normalized
			ok = true
			return
		}
		quals = map[string]string{"auto": bRes.Stream}
		ok = true
		return
	}

	// Fallback 1: fetch the iframe page and try to extract stream URLs from HTML.
	base := strings.TrimRight(m.linkHost, "/")
	pageURL := base + "/?token_movie=" + url.QueryEscape(tokenMovie) + "&token=" + url.QueryEscape(strings.TrimSpace(m.tokenForCtx(r.Context())))

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, pageURL, nil)
	if err != nil {
		return
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Referer", "https://kinogo-go.tv/")
	req.Header.Set("X-Lampac-Go", "1")

	resp, err := m.client.Do(req)
	if err != nil {
		return
	}
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 6<<20))
	resp.Body.Close()
	if readErr != nil || resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return
	}

	if stream := mirageExtractStream(raw); stream != "" {
		quals = map[string]string{"auto": stream}
		ok = true
		return
	}

	// Fallback 2: Extract viewporti (Borth token) from HTML and POST directly
	// to /bnsi/movies/{id} — works without headless Chrome.
	borthToken := mirageExtractViewporti(raw)
	if borthToken == "" {
		return
	}

	if q, qOK := m.mirageDirectMoviesRequestQualities(r.Context(), idFile, borthToken, pageURL); qOK {
		quals = q
		ok = true
		return
	}

	return
}

// mirageBestQuality picks the best URL from a quality map.
func mirageBestQuality(quals map[string]string) string {
	for _, q := range []string{"2160p", "2160", "1440p", "1440", "1080p", "1080", "720p", "720", "480p", "480", "360p", "360", "auto"} {
		if u, ok := quals[q]; ok {
			return u
		}
	}
	for _, u := range quals {
		return u
	}
	return ""
}

// mirageQualRank returns a numeric rank for a quality string: higher = better.
// Used to deterministically pick the best entry from a voice group
// (Go map iteration is non-deterministic unlike C#'s First()).
func mirageQualRank(quality string, uhd bool) int {
	if uhd {
		return 7
	}
	q := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(quality)), "p")
	switch q {
	case "2160", "4k", "uhd":
		return 6
	case "1440", "2k":
		return 5
	case "1080", "fhd":
		return 4
	case "720", "hd":
		return 3
	case "480":
		return 2
	case "360":
		return 1
	default:
		return 0
	}
}

// mirageDirectMoviesRequestQualities performs a direct POST to /bnsi/movies/{id}
// with the Borth token and returns ALL quality variants.
func (m *MirageChecker) mirageDirectMoviesRequestQualities(ctx context.Context, idFile int64, borthToken, referer string) (map[string]string, bool) {
	base := strings.TrimRight(m.linkHost, "/")
	targetURL := base + "/bnsi/movies/" + strconv.FormatInt(idFile, 10)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("Origin", base)
	req.Header.Set("Referer", referer)
	req.Header.Set("Borth", borthToken)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/json")

	balName := m.prefix
	if balName == "" {
		balName = "mirage"
	}
	client := httpclient.NewForBalancer(balName, 15*time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false
	}

	// Try to extract ALL qualities first
	quals := mirageExtractAllHLSQualities(body)
	if len(quals) > 0 {
		// Normalize keys: "2160" → "2160p"
		normalized := make(map[string]string, len(quals))
		for q, u := range quals {
			key := q
			if _, err := strconv.Atoi(q); err == nil {
				key = q + "p"
			}
			normalized[key] = u
		}
		return normalized, true
	}

	// Fallback: single best stream
	if stream := mirageExtractHLSFromJSON(body); stream != "" {
		return map[string]string{"auto": stream}, true
	}

	return nil, false
}

// mirageViewportiRe matches the <meta name="viewporti"> tag carrying the
// Borth auth token. Pre-compiled — called per browser-session resolution.
var mirageViewportiRe = regexp.MustCompile(`<meta\s+name="viewporti"\s+content="([^"]+)"`)

// mirageExtractViewporti extracts the Borth authentication token from the
// <meta name="viewporti" content="..."> tag in the player HTML.
func mirageExtractViewporti(html []byte) string {
	m := mirageViewportiRe.FindSubmatch(html)
	if len(m) < 2 {
		return ""
	}
	return string(m[1])
}

func (m *MirageChecker) writeMovie(w http.ResponseWriter, rjson bool, host, title, originalTitle, tokenMovie string, all map[string]any) {
	data, labels := mirageBuildMovieRows(host, title, originalTitle, tokenMovie, all, m.prefix)
	if len(data) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "movie",
			"data": data,
		})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range data {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, 0, 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (m *MirageChecker) writeSerial(w http.ResponseWriter, rjson bool, host, title, originalTitle, tokenMovie string, s int, t int, all map[string]any) {
	seasons := mirageCollectSeasons(all)
	if len(seasons) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if s == -1 {
		data := make([]map[string]any, 0, len(seasons))
		labels := make([]string, 0, len(seasons))
		for _, season := range seasons {
			name := strconv.Itoa(season) + " сезон"
			data = append(data, map[string]any{
				"method": "link",
				"id":     season,
				"name":   name,
				"url": host + "/lite/" + m.prefix + "?rjson=" + getsTVBool(rjson) +
					"&s=" + strconv.Itoa(season) +
					"&orid=" + url.QueryEscape(tokenMovie) +
					"&title=" + url.QueryEscape(title) +
					"&original_title=" + url.QueryEscape(originalTitle),
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
		return
	}

	entries := mirageCollectSeasonEntries(all, s)
	if len(entries) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	voiceOrder, voiceNames := mirageCollectVoices(entries)
	if len(voiceOrder) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	if t < 0 || voiceNames[t] == "" {
		t = voiceOrder[0]
	}

	voiceRows := make([]map[string]any, 0, len(voiceOrder))
	for _, trID := range voiceOrder {
		name := voiceNames[trID]
		voiceRows = append(voiceRows, map[string]any{
			"method": "link",
			"name":   name,
			"active": trID == t,
			"url": host + "/lite/" + m.prefix + "?rjson=" + getsTVBool(rjson) +
				"&s=" + strconv.Itoa(s) +
				"&t=" + strconv.Itoa(trID) +
				"&orid=" + url.QueryEscape(tokenMovie) +
				"&title=" + url.QueryEscape(title) +
				"&original_title=" + url.QueryEscape(originalTitle),
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Episode == entries[j].Episode {
			return entries[i].ID < entries[j].ID
		}
		return entries[i].Episode < entries[j].Episode
	})

	baseTitle := getsTVJoinName(title, originalTitle)
	data := make([]map[string]any, 0, len(entries))
	labels := make([]string, 0, len(entries))
	seasonsData := make([]int, 0, len(entries))
	episodes := make([]int, 0, len(entries))
	for _, entry := range entries {
		if entry.TranslationID != t || entry.ID <= 0 {
			continue
		}
		ep := entry.Episode
		if ep <= 0 {
			ep = len(data) + 1
		}
		name := strconv.Itoa(ep) + " серия"
		link := host + "/lite/" + m.prefix + "/video?id_file=" + strconv.FormatInt(entry.ID, 10) + "&token_movie=" + url.QueryEscape(tokenMovie)
		stream := host + "/lite/" + m.prefix + "/video.m3u8?id_file=" + strconv.FormatInt(entry.ID, 10) + "&token_movie=" + url.QueryEscape(tokenMovie) + "&play=true"

		data = append(data, map[string]any{
			"method": "call",
			"url":    link,
			"stream": stream,
			"s":      s,
			"e":      ep,
			"name":   name,
			"title":  fmt.Sprintf("%s (%d серия)", baseTitle, ep),
		})
		labels = append(labels, name)
		seasonsData = append(seasonsData, s)
		episodes = append(episodes, ep)
	}
	if len(data) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type":  "episode",
			"voice": voiceRows,
			"data":  data,
		})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for _, row := range voiceRows {
		getsTVAppendVoiceHTML(&sb, row)
	}
	sb.WriteString(`</div>`)
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range data {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, seasonsData[i], episodes[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (m *MirageChecker) writeSimilar(w http.ResponseWriter, r *http.Request, rjson bool, title, originalTitle string, items []mirageSearchItem) {
	host := hostFromRequest(r)
	data := make([]map[string]any, 0, len(items))
	labels := make([]string, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.TokenMovie) == "" {
			continue
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = strings.TrimSpace(item.Original)
		}
		if name == "" {
			name = item.TokenMovie
		}

		link := host + "/lite/" + m.prefix + "?orid=" + url.QueryEscape(item.TokenMovie) +
			"&title=" + url.QueryEscape(title) +
			"&original_title=" + url.QueryEscape(originalTitle)
		data = append(data, map[string]any{
			"method":  "link",
			"url":     link,
			"similar": true,
			"year":    item.Year,
			"details": strings.TrimSpace(item.Country),
			"title":   name,
			"img":     strings.TrimSpace(item.Poster),
		})
		labels = append(labels, name)
	}
	if len(data) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "similar",
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

func miragePickByTitle(items []mirageSearchItem, title string, year int, originalLanguage string) mirageSearchItem {
	want := normalizeSearchTitle(title)
	for _, item := range items {
		if item.CategoryID <= 0 || strings.TrimSpace(item.TokenMovie) == "" {
			continue
		}
		if normalizeSearchTitle(item.Name) != want {
			continue
		}
		if year > 0 && item.Year > 0 && item.Year != year && item.Year != year-1 && item.Year != year+1 {
			continue
		}
		if originalLanguage == "ru" && normalizeSearchTitle(item.Country) != normalizeSearchTitle("россия") {
			continue
		}
		return item
	}
	return mirageSearchItem{}
}

func mirageBuildMovieRows(host, title, originalTitle, tokenMovie string, all map[string]any, prefix string) ([]map[string]any, []string) {
	theatrical := mirageMap(all["theatrical"])
	if len(theatrical) == 0 {
		return nil, nil
	}

	type rowItem struct {
		sort  string
		label string
		data  map[string]any
	}

	baseTitle := getsTVJoinName(title, originalTitle)
	rows := make([]rowItem, 0, len(theatrical))
	for _, node := range theatrical {
		inner := mirageMap(node)
		if len(inner) == 0 {
			continue
		}

		// Pick the entry with the best quality from the voice group.
		// Go map iteration is non-deterministic, so we must compare all entries
		// instead of taking the first one (C# uses First() which gets the
		// highest-quality entry by JSON insertion order).
		var voice map[string]any
		bestRank := -1
		for _, v := range inner {
			candidate := mirageMap(v)
			if len(candidate) == 0 || mirageInt64(candidate["id"]) <= 0 {
				continue
			}
			rank := mirageQualRank(mirageStr(candidate["quality"]), mirageBool(candidate["uhd"]))
			if voice == nil || rank > bestRank {
				voice = candidate
				bestRank = rank
			}
		}
		if len(voice) == 0 {
			continue
		}

		id := mirageInt64(voice["id"])
		if id <= 0 {
			continue
		}

		translation := strings.TrimSpace(mirageStr(voice["translation"]))
		if translation == "" {
			translation = "Озвучка"
		}

		quality := strings.TrimSpace(mirageStr(voice["quality"]))
		if mirageBool(voice["uhd"]) {
			quality = "2160p"
		}
		if quality == "" {
			quality = translation
		}

		link := host + "/lite/" + prefix + "/video?id_file=" + strconv.FormatInt(id, 10) + "&token_movie=" + url.QueryEscape(tokenMovie)
		stream := host + "/lite/" + prefix + "/video.m3u8?id_file=" + strconv.FormatInt(id, 10) + "&token_movie=" + url.QueryEscape(tokenMovie) + "&play=true"

		row := map[string]any{
			"method":    "call",
			"url":       link,
			"stream":    stream,
			"translate": translation,
			"details":   quality,
			"title":     fmt.Sprintf("%s (%s)", baseTitle, translation),
		}

		rows = append(rows, rowItem{
			sort:  strings.ToLower(strings.TrimSpace(translation)),
			label: translation,
			data:  row,
		})
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].sort < rows[j].sort })
	data := make([]map[string]any, 0, len(rows))
	labels := make([]string, 0, len(rows))
	for _, row := range rows {
		data = append(data, row.data)
		labels = append(labels, row.label)
	}
	return data, labels
}

func mirageCollectSeasons(all map[string]any) []int {
	seasonSet := make(map[int]struct{}, 8)

	for key := range all {
		if n, ok := mirageParseSeasonKey(key); ok {
			seasonSet[n] = struct{}{}
		}
	}

	if seasonsMap := mirageMap(all["seasons"]); len(seasonsMap) > 0 {
		for key := range seasonsMap {
			if n, ok := mirageParseSeasonKey(key); ok {
				seasonSet[n] = struct{}{}
			}
		}
	}

	for key, node := range all {
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(key)), "t") {
			continue
		}
		fileMap := mirageMap(mirageMap(node)["file"])
		for seasonKey := range fileMap {
			if n, ok := mirageParseSeasonKey(seasonKey); ok {
				seasonSet[n] = struct{}{}
			}
		}
	}

	seasons := make([]int, 0, len(seasonSet))
	for season := range seasonSet {
		seasons = append(seasons, season)
	}
	sort.Ints(seasons)
	return seasons
}

func mirageCollectSeasonEntries(all map[string]any, season int) []mirageEpisodeEntry {
	seasonKey := strconv.Itoa(season)
	out := make([]mirageEpisodeEntry, 0, 16)

	if direct, ok := all[seasonKey]; ok {
		out = append(out, mirageParseSeasonNode(direct)...)
	}

	if seasonsMap := mirageMap(all["seasons"]); len(seasonsMap) > 0 {
		if node, ok := seasonsMap[seasonKey]; ok {
			out = append(out, mirageParseSeasonNode(node)...)
		}
	}

	for key, node := range all {
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(key)), "t") {
			continue
		}
		fileMap := mirageMap(mirageMap(node)["file"])
		if len(fileMap) == 0 {
			continue
		}
		if seasonNode, ok := fileMap[seasonKey]; ok {
			out = append(out, mirageParseSeasonNode(seasonNode)...)
		}
	}

	uniq := make(map[int64]mirageEpisodeEntry, len(out))
	for _, entry := range out {
		if entry.ID <= 0 {
			continue
		}
		if entry.TranslationID <= 0 {
			entry.TranslationID = 1
		}
		if strings.TrimSpace(entry.Translation) == "" {
			entry.Translation = "Озвучка"
		}
		uniq[entry.ID] = entry
	}

	res := make([]mirageEpisodeEntry, 0, len(uniq))
	for _, entry := range uniq {
		res = append(res, entry)
	}
	return res
}

func mirageParseSeasonNode(node any) []mirageEpisodeEntry {
	out := make([]mirageEpisodeEntry, 0, 8)
	switch n := node.(type) {
	case []any:
		for _, item := range n {
			out = append(out, mirageParseVoiceContainer(item)...)
		}
	case map[string]any:
		if entry, ok := mirageEntryFromVoiceObject(n); ok {
			return append(out, entry)
		}
		for _, v := range n {
			out = append(out, mirageParseVoiceContainer(v)...)
		}
	}
	return out
}

func mirageParseVoiceContainer(v any) []mirageEpisodeEntry {
	out := make([]mirageEpisodeEntry, 0, 4)
	switch node := v.(type) {
	case []any:
		for _, item := range node {
			out = append(out, mirageParseVoiceContainer(item)...)
		}
	case map[string]any:
		if entry, ok := mirageEntryFromVoiceObject(node); ok {
			out = append(out, entry)
		}
		for _, nested := range node {
			if voice, ok := mirageEntryFromVoiceObject(mirageMap(nested)); ok {
				out = append(out, voice)
				continue
			}
			if nestedMap := mirageMap(nested); len(nestedMap) > 0 {
				for _, second := range nestedMap {
					if voice, ok := mirageEntryFromVoiceObject(mirageMap(second)); ok {
						out = append(out, voice)
					}
				}
			}
		}
	}
	return out
}

func mirageEntryFromVoiceObject(obj map[string]any) (mirageEpisodeEntry, bool) {
	if len(obj) == 0 {
		return mirageEpisodeEntry{}, false
	}
	id := mirageInt64(obj["id"])
	if id <= 0 {
		return mirageEpisodeEntry{}, false
	}

	translationID := mirageInt(obj["id_translation"])
	translation := strings.TrimSpace(mirageStr(obj["translation"]))
	episode := mirageInt(obj["episode"])
	return mirageEpisodeEntry{
		TranslationID: translationID,
		Translation:   translation,
		Episode:       episode,
		ID:            id,
	}, true
}

func mirageCollectVoices(entries []mirageEpisodeEntry) ([]int, map[int]string) {
	voiceMap := make(map[int]string, 4)
	for _, entry := range entries {
		id := entry.TranslationID
		if id <= 0 {
			id = 1
		}
		name := strings.TrimSpace(entry.Translation)
		if name == "" {
			name = "Озвучка"
		}
		if _, exists := voiceMap[id]; !exists {
			voiceMap[id] = name
		}
	}

	order := make([]int, 0, len(voiceMap))
	for id := range voiceMap {
		order = append(order, id)
	}
	sort.Ints(order)
	return order, voiceMap
}

func mirageParseSeasonKey(key string) (int, bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		return 0, false
	}
	n, err := strconv.Atoi(key)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func mirageDecodeJSONString(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	quoted := `"` + strings.ReplaceAll(raw, `"`, `\"`) + `"`
	if decoded, err := strconv.Unquote(quoted); err == nil {
		return decoded
	}

	raw = strings.ReplaceAll(raw, `\\'`, `'`)
	raw = strings.ReplaceAll(raw, `\"`, `"`)
	raw = strings.ReplaceAll(raw, `\\`, `\`)
	return raw
}

func mirageExtractStream(raw []byte) string {
	body := strings.TrimSpace(string(raw))
	if body == "" {
		return ""
	}

	if direct := mirageFirstURLFromString(body); direct != "" {
		return direct
	}

	var root any
	if err := stdjson.Unmarshal(raw, &root); err == nil {
		if stream := mirageFindStream(root, 0); stream != "" {
			return stream
		}
	}

	return mirageFirstURLFromString(body)
}

func mirageFindStream(v any, depth int) string {
	if depth > 8 || v == nil {
		return ""
	}

	switch node := v.(type) {
	case string:
		return mirageFirstURLFromString(node)

	case []any:
		for _, item := range node {
			if stream := mirageFindStream(item, depth+1); stream != "" {
				return stream
			}
		}

	case map[string]any:
		for _, key := range []string{"src", "url", "hls", "file", "stream"} {
			if stream := mirageFirstURLFromString(mirageStr(node[key])); stream != "" {
				return stream
			}
		}

		if list := mirageSlice(node["hlsSource"]); len(list) > 0 {
			for _, wanted := range []string{"2160", "1440", "1080", "720", "480", "360"} {
				for _, item := range list {
					q := mirageMap(mirageMap(item)["quality"])
					if stream := mirageFirstURLFromString(mirageStr(q[wanted])); stream != "" {
						return stream
					}
				}
			}
			for _, item := range list {
				if stream := mirageFindStream(item, depth+1); stream != "" {
					return stream
				}
			}
		}

		for _, item := range node {
			if stream := mirageFindStream(item, depth+1); stream != "" {
				return stream
			}
		}
	}

	return ""
}

func mirageFirstURLFromString(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}

	if m := strings.TrimSpace(mirageAnyM3URe.FindString(s)); m != "" {
		return m
	}
	if match := mirageQualityURLRe.FindAllStringSubmatch(s, -1); len(match) > 0 {
		sort.Slice(match, func(i, j int) bool {
			qi, _ := strconv.Atoi(match[i][1])
			qj, _ := strconv.Atoi(match[j][1])
			return qi > qj
		})
		return strings.TrimSpace(match[0][2])
	}
	return ""
}

func mirageOrigin(host string) string {
	u, err := url.Parse(host)
	if err != nil {
		return host
	}
	if u.Scheme == "" || u.Host == "" {
		return host
	}
	return u.Scheme + "://" + u.Host
}

func (m *MirageChecker) probe(r *http.Request, method, target string) bool {
	req, err := http.NewRequestWithContext(r.Context(), method, target, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/json,*/*")
	req.Header.Set("X-Lampac-Go", "1")

	resp, err := m.client.Do(req)
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

func mirageMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func mirageSlice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	return nil
}

func mirageStr(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case stdjson.Number:
		return x.String()
	default:
		return fmt.Sprint(x)
	}
}

func mirageInt(v any) int {
	return int(mirageInt64(v))
}

func mirageInt64(v any) int64 {
	switch x := v.(type) {
	case nil:
		return 0
	case int:
		return int64(x)
	case int64:
		return x
	case float64:
		return int64(x)
	case float32:
		return int64(x)
	case stdjson.Number:
		if n, err := x.Int64(); err == nil {
			return n
		}
		if f, err := x.Float64(); err == nil {
			return int64(f)
		}
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n
	}
	return 0
}

func mirageBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "1", "true", "yes", "on":
			return true
		}
	case float64:
		return x != 0
	case int:
		return x != 0
	}
	return false
}
