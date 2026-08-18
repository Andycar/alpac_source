package litesrc

import (
	"context"
	stdjson "encoding/json"
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

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/rs/zerolog/log"
)

// turboQualityRe extracts quality and CDN URL from obrut player file fields.
var turboQualityRe = regexp.MustCompile(`\[(\d+)p?\](https://cdn-[a-zA-Z0-9_./:?&=%+~-]+)`)

// turboChecker implements the "Плеер Turbo" balancer.
// Flow (no Chrome on the lampac host — FlareSolverr runs its own Chrome):
//  1. uTLS GET https://api.kinobox.tv/api/players?kinopoisk={kp} → JSON. Find
//     {type:"Turbo", iframeUrl:...}. iframeUrl looks like
//     https://92d73433.obrut.show/embed/MjM/content/{id} (host may rotate).
//  2. FlareSolverrFetch(iframeUrl) with SOCKS5 attached → solves Wallarm
//     wsdk.js + DDoS-Guard wd_trust and returns the obrut embed HTML.
//  3. Decode base64 Player payload → CDN URLs (cdn-*.obrut.show → 302 →
//     superdupercdn.com).
//  4. Proxy CDN streams through /proxy/ with Origin/Referer of the obrut host
//     the API just gave us; SOCKS5 40007 is registered for the "turbo" label.
//
// Previously kinomix.web.app's <select> exposed Turbo and we drove it via
// Chrome on :9222. In 2026-05 kinomix removed Turbo from the dropdown — the
// kinomix Chrome path no longer works for this balancer. The kinomix helpers
// further down this file are kept solely because vibix.go still uses them.
type turboChecker struct {
	client    *http.Client // generic HTTP, used for probe()
	obrutHost string       // default obrut host from config; per-content host is read from the kinobox API iframeUrl

	cookieCache sync.Map
	embedCache  sync.Map
	janitorOnce sync.Once

	// FlareSolverr session reused across embed fetches so the Chrome cookie
	// jar (wd_trust + post-wsdk tokens) persists between obrut requests.
	// Without this the very first request to a fresh session always returns
	// the Wallarm challenge page.
	fsSession   string
	fsSessionTS time.Time
	fsMu        sync.Mutex
}

// turboEmbed carries the resolved obrut host so that header-aware proxying
// uses the host the kinobox.tv API returned for this specific content — the
// configured default is only a fallback used by probe() before any resolve.
type turboEmbed struct {
	movie     bool
	obrutHost string
	voices    []turboVoice
	seasons   []int
}

// turboEmbedJanitorInterval controls sweep cadence for turbo.embedCache —
// without this, entries for never-re-requested kp:season pairs accumulate.
const turboEmbedJanitorInterval = 20 * time.Minute

func (t *turboChecker) startEmbedJanitor() {
	t.janitorOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(turboEmbedJanitorInterval)
			defer ticker.Stop()
			for range ticker.C {
				now := time.Now()
				t.embedCache.Range(func(k, v any) bool {
					if ce, ok := v.(turboEmbedCacheEntry); ok && now.After(ce.expires) {
						t.embedCache.Delete(k)
					}
					return true
				})
			}
		}()
	})
}

type turboCookieEntry struct {
	cookies string
	expires time.Time
}

type turboEmbedCacheEntry struct {
	result  turboEmbed
	expires time.Time
}

type turboVoice struct {
	title    string
	file     string
	episodes []turboEpisode
	quals    map[string]string
}

type turboEpisode struct {
	title string
	num   int
	file  string
}

func NewTurboChecker(cfg config.Config) *turboChecker {
	host := strings.TrimSpace(cfg.Online.Turbo.Host)
	if host == "" {
		host = "92d73433.obrut.show"
	}
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimRight(host, "/")

	// Opt turbo into FlareSolverr as a *system* requirement — kinomix UI
	// dropped the "Turbo" option in 2026-05, so the only way to obtain the
	// obrut embed HTML is via FlareSolverr (Wallarm wsdk.js + DDoS-Guard
	// wd_trust on obrut requires a real browser to solve the challenge).
	// System registration survives config reload and admin save, and shows up
	// in the admin UI as a non-removable badge.
	httpclient.RegisterFlareSolverrBalancerSystem("turbo")

	// Turbo can need TWO different proxies because the two upstream protections
	// have opposite preferences:
	//
	//   * api.kinobox.tv → DDoS-Guard. Wants Chrome JA3 over an opaque tunnel
	//     (xtls-rprx-vision works because our Go uTLS rides on top of vision).
	//     A plain VLESS exit often fails here (TLS RST / EOF).
	//
	//   * 92d73433.obrut.show via FlareSolverr → headless Chrome's real TLS.
	//     xtls-rprx-vision MANGLES Chrome's outer TLS fingerprint, so FS Chrome
	//     gets reset. A plain VLESS exit works.
	//
	// Map them separately in the admin Proxy panel:
	//   "turbo"     → plain VLESS / SOCKS5 used by FlareSolverr's Chrome
	//   "turbo_api" → vision VLESS or any proxy that lets DDoS-Guard's JA3
	//                 check pass; falls back to "turbo" if not set.
	if addr := httpclient.SocksAddrForBalancer("turbo"); addr == "" {
		log.Warn().Msg("turbo: no SOCKS5 mapped to balancer \"turbo\" — add one in the admin Proxy panel, FlareSolverr will see a geo-blocked obrut otherwise")
	} else {
		log.Info().Str("socks", addr).Msg("turbo: using SOCKS5 for FlareSolverr (Chrome) — balancer=\"turbo\"")
	}
	if apiAddr := httpclient.SocksAddrForBalancer("turbo_api"); apiAddr != "" {
		log.Info().Str("socks", apiAddr).Msg("turbo: using separate SOCKS5 for kinobox API — balancer=\"turbo_api\"")
	} else {
		log.Info().Msg("turbo: no dedicated \"turbo_api\" SOCKS5 — kinobox API will share the \"turbo\" SOCKS5")
	}

	return &turboChecker{
		client:    httpclient.NewForBalancerNoRedirect("turbo", 12*time.Second),
		obrutHost: host,
	}
}

// kinoboxAPIClient constructs the uTLS HTTP client on each call so it picks
// up the current SOCKS5 mapping. Prefers the dedicated "turbo_api" mapping
// (set when the operator splits proxies — kinobox needs the vision exit that
// passes DDoS-Guard's JA3 check, while FlareSolverr needs a plain exit).
// Falls back to "turbo" when no split is configured, so single-proxy setups
// still work without any admin action.
func (t *turboChecker) kinoboxAPIClient() *http.Client {
	balancer := "turbo"
	if httpclient.SocksAddrForBalancer("turbo_api") != "" {
		balancer = "turbo_api"
	}
	return httpclient.NewUTLSForBalancer(balancer, 12*time.Second)
}

// turboFSSessionTTL is how long we reuse a single FlareSolverr session before
// rotating. Each session is a real Chrome browser context — they accumulate
// state and bot-detection fingerprint over time, so periodic rotation keeps
// Wallarm from flagging us. The TTL also acts as a hard upper bound on how
// long a stuck/broken session can stay sticky.
const turboFSSessionTTL = 30 * time.Minute

// flareSolverrSession returns a cached session ID, creating one on first use
// or rotating it after turboFSSessionTTL. Returns "" if FlareSolverr is not
// configured or session creation failed — callers should still attempt the
// request without a session (FS will spawn a one-off Chrome that won't carry
// the wsdk cookie, but the second-pass retry will still get one shot).
func (t *turboChecker) flareSolverrSession(parentCtx context.Context) string {
	fsURL := httpclient.FlareSolverrURL()
	if fsURL == "" {
		return ""
	}
	t.fsMu.Lock()
	defer t.fsMu.Unlock()
	if t.fsSession != "" && time.Since(t.fsSessionTS) < turboFSSessionTTL {
		return t.fsSession
	}
	// Best-effort tear down the old session before creating a new one — keeps
	// FlareSolverr from leaking Chrome processes if rotation kicks in often.
	if t.fsSession != "" {
		go func(old string) {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = httpclient.FlareSolverrDestroySession(cleanupCtx, fsURL, old)
		}(t.fsSession)
		t.fsSession = ""
	}

	createCtx, cancel := context.WithTimeout(parentCtx, 15*time.Second)
	defer cancel()
	proxy := ""
	if addr := httpclient.SocksAddrForBalancer("turbo"); addr != "" {
		proxy = "socks5://" + addr
	}
	id, err := httpclient.FlareSolverrCreateSession(createCtx, fsURL, proxy)
	if err != nil {
		log.Warn().Err(err).Msg("turbo: FlareSolverr session create failed")
		return ""
	}
	t.fsSession = id
	t.fsSessionTS = time.Now()
	log.Info().Str("session", id).Msg("turbo: FlareSolverr session created")
	return id
}

func (t *turboChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		if parseBoolParam(q.Get("checksearch")) {
			show := t.checkSearch(req)
			writeCheckSearchResponseNoRCH(w, show, pluginQualityBadgeGet("turbo"))
			return
		}
		t.index(w, req, links)
	}
}

func (t *turboChecker) checkSearch(req *http.Request) bool {
	kinopoiskID, _ := strconv.ParseInt(strings.TrimSpace(req.URL.Query().Get("kinopoisk_id")), 10, 64)
	if kinopoiskID == 0 {
		return t.probe(req)
	}
	embed, ok := t.resolveEmbed(req.Context(), kinopoiskID, -1)
	if !ok {
		return t.probe(req)
	}
	return len(embed.voices) > 0 || len(embed.seasons) > 0
}

func (t *turboChecker) probe(req *http.Request) bool {
	target := "https://" + t.obrutHost
	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodHead, target, nil)
	if err != nil {
		return false
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := httpclient.New(5 * time.Second).Do(httpReq)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 500
}

func (t *turboChecker) index(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	kinopoiskID, _ := strconv.ParseInt(strings.TrimSpace(q.Get("kinopoisk_id")), 10, 64)
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	s, sOK := getsTVQueryInt(q.Get("s"))
	if !sOK {
		s = -1
	}
	voiceFilter := strings.TrimSpace(q.Get("t"))

	if kinopoiskID == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	embed, ok := t.resolveEmbed(req.Context(), kinopoiskID, s)
	if !ok || (len(embed.voices) == 0 && len(embed.seasons) == 0) {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if embed.movie {
		t.writeMovie(w, req, rjson, title, originalTitle, embed, links)
		return
	}
	if s == -1 {
		t.writeSeasons(w, req, rjson, kinopoiskID, title, originalTitle, embed)
		return
	}
	t.writeEpisodes(w, req, rjson, kinopoiskID, title, originalTitle, s, voiceFilter, embed, links)
}

// ---------------------------------------------------------------------------
// Embed resolve
//
// As of 2026-05 kinomix.web.app removed Turbo from the player <select>, so the
// previous Chrome-on-kinomix path can no longer discover the obrut iframe URL.
// Instead we go straight to the kinobox.tv API (which still returns Turbo) and
// then load the obrut embed via FlareSolverr (Wallarm wsdk.js + DDoS-Guard
// wd_trust on obrut requires a real browser to solve the JS challenge).
//
// Shared helpers kinomixGetPlayerIframe/turboBrowser/turboEvalAsync are kept
// below because vibix.go still uses them.
// ---------------------------------------------------------------------------

var turboBrowser struct {
	mu        sync.Mutex
	allocCtx  context.Context
	allocStop context.CancelFunc
	port      int
}

func (t *turboChecker) resolveEmbed(ctx context.Context, kpID int64, season int) (turboEmbed, bool) {
	cacheKey := "kp:" + strconv.FormatInt(kpID, 10) + ":s:" + strconv.Itoa(season)
	if cached, ok := t.embedCache.Load(cacheKey); ok {
		ce := cached.(turboEmbedCacheEntry)
		if time.Now().Before(ce.expires) {
			return ce.result, true
		}
	}

	embedHTML, obrutHost, cookies, ok := t.fetchEmbed(ctx, kpID)
	if !ok {
		return turboEmbed{}, false
	}

	if cookies != "" {
		t.cookieCache.Store(obrutHost, turboCookieEntry{
			cookies: cookies,
			expires: time.Now().Add(90 * time.Minute),
		})
	}

	embed := t.parsePlayerHTML(embedHTML, season)
	embed.obrutHost = obrutHost
	if len(embed.voices) == 0 && len(embed.seasons) == 0 {
		return turboEmbed{}, false
	}

	t.startEmbedJanitor()
	t.embedCache.Store(cacheKey, turboEmbedCacheEntry{
		result:  embed,
		expires: time.Now().Add(30 * time.Minute),
	})
	return embed, true
}

// ---------------------------------------------------------------------------
// fetchEmbed: kinobox.tv API → FlareSolverr → obrut embed HTML
// ---------------------------------------------------------------------------

// turboKinoboxResp is the subset of api.kinobox.tv/api/players we care about.
type turboKinoboxResp struct {
	Data []struct {
		Type      string `json:"type"`
		IframeURL string `json:"iframeUrl"`
	} `json:"data"`
}

// fetchEmbed resolves the Turbo iframe URL for kpID via the kinobox.tv API,
// then loads that obrut embed page via FlareSolverr. Returns the player HTML,
// the host parsed out of the iframe URL (so proxy headers can use the exact
// origin the embed served), and any cookies FlareSolverr captured.
func (t *turboChecker) fetchEmbed(parentCtx context.Context, kpID int64) (html, obrutHost, cookies string, ok bool) {
	iframeURL, ok := t.fetchIframeFromKinobox(parentCtx, kpID)
	if !ok {
		return "", "", "", false
	}

	if u, err := url.Parse(iframeURL); err == nil && u.Host != "" {
		obrutHost = u.Host
	} else {
		obrutHost = t.obrutHost
	}

	if httpclient.FlareSolverrURL() == "" {
		log.Warn().Int64("kp", kpID).Msg("turbo: FlareSolverr URL not configured — set online.flaresolverr in config")
		return "", "", "", false
	}

	// Budget covers: kinobox API (~12s max) + up to 3 FlareSolverr fetches
	// (~6s each) interleaved with wsdk-cooling sleeps (8+12+16=36s).
	ctx, cancel := context.WithTimeout(parentCtx, 90*time.Second)
	defer cancel()

	session := t.flareSolverrSession(ctx)
	fsOpts := httpclient.FlareSolverrFetchOptions{
		// Force so we don't depend on the operator remembering to add
		// "turbo" to online.flaresolverr_balancers — we already require FS
		// for this balancer to work at all.
		Force:        true,
		MaxTimeoutMS: 40000,
		Session:      session,
		Headers: map[string]string{
			"Referer":         "https://kinomix.web.app/",
			"Accept-Language": "en-US,en;q=0.9,ru;q=0.8",
		},
	}

	res, fsOK := httpclient.FlareSolverrFetch(ctx, "turbo", iframeURL, fsOpts)
	// Same-session retries for Wallarm wsdk.js: it loads with async/defer,
	// computes a fingerprint via JS (canvas/webgl/audio), POSTs to
	// /include/collect, and the response sets/upgrades the wd_trust cookie.
	// FlareSolverr returns the page on domcontentloaded — typically before
	// wsdk has even finished POSTing. So we sleep, refetch in the same
	// session (Chrome cookie jar persists), and check whether the cookie
	// has matured into a real allow. A few retries cover slow fingerprint
	// solves; we cap them so a permanently-blocked exit fails fast.
	if fsOK && session != "" {
		for attempt := 0; attempt < 3; attempt++ {
			if strings.Contains(res.Solution.Response, "new Player") {
				break
			}
			if !strings.Contains(res.Solution.Response, "wsdk.js") {
				break
			}
			waitMS := 8000 + attempt*4000 // 8s, 12s, 16s
			log.Info().Int64("kp", kpID).Int("attempt", attempt+1).Int("wait_ms", waitMS).Msg("turbo: wsdk challenge — sleeping and retrying within same session")
			select {
			case <-time.After(time.Duration(waitMS) * time.Millisecond):
			case <-ctx.Done():
				return "", "", "", false
			}
			res, fsOK = httpclient.FlareSolverrFetch(ctx, "turbo", iframeURL, fsOpts)
			if !fsOK {
				break
			}
		}
	}
	if !fsOK {
		// Surface whatever FlareSolverr told us so the operator can tell the
		// difference between "FS not reachable", "FS Chrome can't reach the
		// SOCKS5", "challenge failed", "upstream HTTP 4xx", etc. Without
		// these fields the warn is useless.
		socksHint := httpclient.SocksAddrForBalancer("turbo")
		if socksHint == "" {
			socksHint = "<none>"
		}
		log.Warn().
			Str("iframe", iframeURL).
			Int64("kp", kpID).
			Str("fs_status", res.Status).
			Str("fs_message", res.Message).
			Int("fs_upstream_status", res.Solution.Status).
			Int("fs_html_len", len(res.Solution.Response)).
			Str("fs_socks", socksHint).
			Msg("turbo: FlareSolverr fetch failed")
		return "", "", "", false
	}

	embedHTML := res.Solution.Response
	if !strings.Contains(embedHTML, "new Player") {
		// Dump a snippet so we can tell whether FS handed us the Wallarm
		// challenge page, a Cloudflare interstitial, an obrut 404, etc.
		snippet := embedHTML
		if len(snippet) > 500 {
			snippet = snippet[:500]
		}
		log.Warn().
			Int("len", len(embedHTML)).
			Int64("kp", kpID).
			Int("status", res.Solution.Status).
			Bool("has_wsdk", strings.Contains(embedHTML, "wsdk.js")).
			Bool("has_ddosguard", strings.Contains(embedHTML, "ddos-guard")).
			Str("snippet", snippet).
			Msg("turbo: FlareSolverr returned HTML without Player payload")
		return "", "", "", false
	}

	cookies = httpclient.CookiesAsHeader(res.Solution.Cookies)

	log.Info().Int64("kp", kpID).Str("host", obrutHost).Int("html_len", len(embedHTML)).Int("cookies", len(res.Solution.Cookies)).Msg("turbo: embed resolved via kinobox API + FlareSolverr")
	return embedHTML, obrutHost, cookies, true
}

// fetchIframeFromKinobox calls api.kinobox.tv/api/players?kinopoisk={kpID} via
// uTLS (the API sits behind DDoS-Guard, plain net/http fails the TLS
// fingerprint check) and returns the iframeUrl of the Turbo entry, if any.
func (t *turboChecker) fetchIframeFromKinobox(parentCtx context.Context, kpID int64) (string, bool) {
	ctx, cancel := context.WithTimeout(parentCtx, 12*time.Second)
	defer cancel()

	apiURL := "https://api.kinobox.tv/api/players?kinopoisk=" + strconv.FormatInt(kpID, 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Origin", "https://kinomix.web.app")
	req.Header.Set("Referer", "https://kinomix.web.app/")

	resp, err := t.kinoboxAPIClient().Do(req)
	if err != nil {
		log.Warn().Err(err).Int64("kp", kpID).Msg("turbo: kinobox API request failed")
		return "", false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Warn().Int("status", resp.StatusCode).Int64("kp", kpID).Msg("turbo: kinobox API returned non-200")
		return "", false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return "", false
	}

	var parsed turboKinoboxResp
	if err := stdjson.Unmarshal(body, &parsed); err != nil {
		log.Warn().Err(err).Int64("kp", kpID).Msg("turbo: kinobox API json parse failed")
		return "", false
	}

	for _, p := range parsed.Data {
		if strings.EqualFold(p.Type, "Turbo") && p.IframeURL != "" {
			log.Debug().Int64("kp", kpID).Str("iframe", p.IframeURL).Msg("turbo: kinobox API gave iframe URL")
			return p.IframeURL, true
		}
	}
	log.Debug().Int64("kp", kpID).Int("players", len(parsed.Data)).Msg("turbo: kinobox API has no Turbo entry for this kpID")
	return "", false
}

// ---------------------------------------------------------------------------
// kinomixGetPlayerIframe navigates Chrome to kinomix.web.app/movie/{kpID},
// waits for the SPA to load players, switches to the requested player,
// and returns the iframe URL. Shared by Turbo and Vibix.
//
// Results are cached for 2 hours — Chrome is only used on cache miss.
// At steady state with warm cache, zero Chrome tabs are needed.
// ---------------------------------------------------------------------------

// kinomixIframeCache stores iframe URLs keyed by "playerType:kpID".
var (
	kinomixIframeCache     sync.Map // map[string]kinomixIframeCacheEntry
	kinomixJanitorOnce     sync.Once
	kinomixJanitorInterval = 30 * time.Minute
)

type kinomixIframeCacheEntry struct {
	url     string
	expires time.Time
}

// kinomixStartJanitor removes expired entries periodically — otherwise
// entries for never-re-requested KP IDs accumulate indefinitely.
func kinomixStartJanitor() {
	kinomixJanitorOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(kinomixJanitorInterval)
			defer ticker.Stop()
			for range ticker.C {
				now := time.Now()
				kinomixIframeCache.Range(func(k, v any) bool {
					if ce, ok := v.(kinomixIframeCacheEntry); ok && now.After(ce.expires) {
						kinomixIframeCache.Delete(k)
					}
					return true
				})
			}
		}()
	})
}

func kinomixGetPlayerIframe(parentCtx context.Context, playerType string, kpID int64) (iframeURL string, ok bool) {
	// Check cache first — avoid Chrome entirely for cached KP IDs.
	cacheKey := playerType + ":" + strconv.FormatInt(kpID, 10)
	if cached, found := kinomixIframeCache.Load(cacheKey); found {
		ce := cached.(kinomixIframeCacheEntry)
		if time.Now().Before(ce.expires) {
			log.Debug().Str("player", playerType).Int64("kp", kpID).Msg("kinomix: iframe URL from cache")
			return ce.url, true
		}
	}

	// Cache miss — need Chrome.
	url, found := kinomixGetPlayerIframeUncached(parentCtx, playerType, kpID)
	if found && url != "" {
		kinomixStartJanitor()
		kinomixIframeCache.Store(cacheKey, kinomixIframeCacheEntry{
			url:     url,
			expires: time.Now().Add(2 * time.Hour),
		})
	}
	return url, found
}

func kinomixGetPlayerIframeUncached(parentCtx context.Context, playerType string, kpID int64) (iframeURL string, ok bool) {
	log.Info().Str("player", playerType).Int64("kp", kpID).Msg("kinomix: starting resolve via Chrome")

	ctx, cancel := context.WithTimeout(parentCtx, 25*time.Second)
	defer cancel()

	if !mirageSem().Acquire(ctx) {
		log.Warn().Str("player", playerType).Msg("kinomix: browser semaphore acquire failed")
		return "", false
	}
	defer mirageSem().Release()

	// Connect to external Chrome.
	port := 9222
	turboBrowser.mu.Lock()
	if turboBrowser.allocCtx == nil || turboBrowser.allocCtx.Err() != nil || turboBrowser.port != port {
		if turboBrowser.allocStop != nil {
			turboBrowser.allocStop()
		}
		wsURL := "ws://127.0.0.1:" + strconv.Itoa(port) + "/devtools/browser/"
		if verResp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/json/version"); err == nil {
			var ver struct {
				WS string `json:"webSocketDebuggerUrl"`
			}
			if stdjson.NewDecoder(verResp.Body).Decode(&ver) == nil && ver.WS != "" {
				wsURL = ver.WS
			}
			verResp.Body.Close()
		}
		allocCtx, allocCancel := chromedp.NewRemoteAllocator(context.Background(), wsURL)
		turboBrowser.allocCtx = allocCtx
		turboBrowser.allocStop = allocCancel
		turboBrowser.port = port
		log.Info().Str("ws", wsURL).Msg("kinomix: connected to remote Chrome")
	}
	allocCtx := turboBrowser.allocCtx
	turboBrowser.mu.Unlock()

	tabCtx, tabCancel := chromedp.NewContext(allocCtx)
	// Remote-allocator tabs are NOT closed by cancelling the context — Target.closeTarget is only
	// sent by chromedp.Cancel. With plain cancel every resolve leaked one OPEN TAB (a playing
	// video-CDN page = a whole renderer process) in the persistent turbo Chrome; renderers
	// accumulated until the box ran out of RAM («turbo-chrome-pac жрёт всю память»).
	defer func() {
		_ = chromedp.Cancel(tabCtx)
		tabCancel()
	}()

	timeoutCtx, timeoutCancel := context.WithTimeout(tabCtx, 22*time.Second)
	defer timeoutCancel()

	// Step 1: Navigate to kinomix movie page.
	kpStr := strconv.FormatInt(kpID, 10)
	movieURL := "https://kinomix.web.app/movie/" + kpStr

	err := chromedp.Run(timeoutCtx,
		chromedp.Navigate(movieURL),
		chromedp.WaitReady("body"),
	)
	if err != nil {
		log.Warn().Err(err).Str("player", playerType).Msg("kinomix: Chrome navigate failed, resetting")
		turboBrowser.mu.Lock()
		if turboBrowser.allocStop != nil {
			turboBrowser.allocStop()
		}
		turboBrowser.allocCtx = nil
		turboBrowser.allocStop = nil
		turboBrowser.port = 0
		turboBrowser.mu.Unlock()
		return "", false
	}

	// Step 2: Wait for SPA to load players, switch to requested player.
	pollJS := `(function() {
		var sel = document.querySelector("select");
		if (!sel) return "";
		for (var i = 0; i < sel.options.length; i++) {
			if (sel.options[i].text.indexOf("` + playerType + `") !== -1) {
				sel.selectedIndex = i;
				sel.dispatchEvent(new Event("change", {bubbles: true}));
				return "found";
			}
		}
		return "no_player";
	})()`

	for i := 0; i < 10; i++ {
		time.Sleep(2 * time.Second)
		var result string
		if err := chromedp.Run(timeoutCtx, chromedp.Evaluate(pollJS, &result)); err != nil {
			continue
		}
		if result == "found" {
			time.Sleep(2 * time.Second)
			var raw string
			chromedp.Run(timeoutCtx, chromedp.Evaluate(
				`Array.from(document.querySelectorAll("iframe")).map(f=>f.src).filter(s=>s.length>10)[0] || ""`,
				&raw,
			))
			if raw != "" {
				log.Info().Str("iframe", raw).Str("player", playerType).Msg("kinomix: got embed URL")
				return raw, true
			}
			break
		}
		if result == "no_player" {
			log.Debug().Str("player", playerType).Int64("kp", kpID).Msg("kinomix: player not available for this movie")
			return "", false
		}
	}

	log.Warn().Str("player", playerType).Int64("kp", kpID).Msg("kinomix: player not found on page")
	return "", false
}

// turboEvalAsync evaluates async JS via CDP with awaitPromise=true.
func turboEvalAsync(ctx context.Context, js string) (string, error) {
	var result string
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		val, excp, err := runtime.Evaluate(js).WithAwaitPromise(true).Do(ctx)
		if err != nil {
			return err
		}
		if excp != nil {
			return stdjson.Unmarshal([]byte(excp.Text), &result)
		}
		return stdjson.Unmarshal(val.Value, &result)
	}))
	return result, err
}

// ---------------------------------------------------------------------------
// Player HTML parsing (reuses zetflix obrut decode logic)
// ---------------------------------------------------------------------------

func (t *turboChecker) parsePlayerHTML(html string, season int) turboEmbed {
	text := ZetflixDecodeObrutBase64(html)
	if text == "" {
		log.Warn().Int("html_len", len(html)).Bool("hasPlayer", strings.Contains(html, "new Player")).Msg("turbo: base64 decode returned empty")
		return turboEmbed{}
	}
	text = strings.ReplaceAll(text, `\/`, `/`)

	cdnCount := strings.Count(text, "cdn-")
	isSerial := ZetflixObrutIsSerial(text)
	snippet := text
	if len(snippet) > 200 {
		snippet = snippet[:200]
	}
	log.Info().Int("decoded_len", len(text)).Int("cdn_urls", cdnCount).Bool("serial", isSerial).Str("snippet", snippet).Msg("turbo: player data decoded")

	if isSerial {
		return t.parseSerial(text, season)
	}
	return t.parseMovie(text)
}

func (t *turboChecker) parseMovie(text string) turboEmbed {
	voices := t.extractVoicesFromText(text)
	if len(voices) == 0 {
		return turboEmbed{}
	}
	return turboEmbed{movie: true, voices: voices}
}

func (t *turboChecker) parseSerial(text string, season int) turboEmbed {
	seasonNums := zetflixExtractSeasonNums(text)
	if len(seasonNums) == 0 {
		return turboEmbed{}
	}
	if season == -1 {
		return turboEmbed{movie: false, seasons: seasonNums}
	}
	voices := t.extractSerialVoices(text, season)
	return turboEmbed{movie: false, voices: voices}
}

func (t *turboChecker) extractVoicesFromText(text string) []turboVoice {
	sections := strings.Split(text, `"title"`)
	var voices []turboVoice
	seen := make(map[string]bool)

	for _, section := range sections[1:] {
		tm := ObrutVoiceTitleRe.FindStringSubmatch(`"title"` + section)
		if len(tm) < 2 {
			continue
		}
		voice := ZetflixCleanVoiceName(tm[1])
		if seen[voice] {
			continue
		}

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

		quals := turboExtractQualities(fileData)
		if len(quals) == 0 {
			continue
		}

		seen[voice] = true
		voices = append(voices, turboVoice{
			title: voice,
			file:  fileData,
			quals: quals,
		})
	}
	return voices
}

func (t *turboChecker) extractSerialVoices(text string, season int) []turboVoice {
	type node struct {
		Title  string `json:"title"`
		File   string `json:"file"`
		Folder []node `json:"folder"`
	}

	fileIdx := strings.Index(text, `"file":[`)
	if fileIdx < 0 {
		return nil
	}
	arrStart := fileIdx + len(`"file":`)
	depth := 0
	arrEnd := arrStart
	for i := arrStart; i < len(text); i++ {
		switch text[i] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				arrEnd = i + 1
				goto found
			}
		}
	}
	return nil
found:
	arrJSON := text[arrStart:arrEnd]

	var nodes []node
	if err := stdjson.Unmarshal([]byte(arrJSON), &nodes); err != nil {
		log.Debug().Err(err).Msg("turbo: serial JSON parse failed")
		return nil
	}

	var seasonNode *node
	seasonStr := strconv.Itoa(season)
	for i := range nodes {
		title := nodes[i].Title
		if strings.Contains(title, seasonStr) &&
			(strings.Contains(strings.ToLower(title), "season") ||
				strings.Contains(strings.ToLower(title), "сезон")) {
			seasonNode = &nodes[i]
			break
		}
	}
	if seasonNode == nil && season <= len(nodes) && season > 0 {
		seasonNode = &nodes[season-1]
	}
	if seasonNode == nil || len(seasonNode.Folder) == 0 {
		return nil
	}

	type voiceEp struct {
		ep   turboEpisode
		file string
	}
	voiceMap := make(map[string][]voiceEp)
	var voiceOrder []string

	for _, epNode := range seasonNode.Folder {
		epTitle := strings.TrimSpace(epNode.Title)
		epNum, _ := vibixEpisodeNumber(epTitle)

		if len(epNode.Folder) > 0 {
			for _, voiceNode := range epNode.Folder {
				vName := ZetflixCleanVoiceName(voiceNode.Title)
				file := voiceNode.File
				if file == "" && len(voiceNode.Folder) > 0 {
					file = voiceNode.Folder[0].File
				}
				if file == "" {
					continue
				}
				if _, ok := voiceMap[vName]; !ok {
					voiceOrder = append(voiceOrder, vName)
				}
				voiceMap[vName] = append(voiceMap[vName], voiceEp{
					ep:   turboEpisode{title: epTitle, num: epNum},
					file: file,
				})
			}
		} else if epNode.File != "" {
			vName := "По умолчанию"
			if _, ok := voiceMap[vName]; !ok {
				voiceOrder = append(voiceOrder, vName)
			}
			voiceMap[vName] = append(voiceMap[vName], voiceEp{
				ep:   turboEpisode{title: epTitle, num: epNum},
				file: epNode.File,
			})
		}
	}

	var voices []turboVoice
	for _, vName := range voiceOrder {
		eps := voiceMap[vName]
		var episodes []turboEpisode
		for _, ve := range eps {
			ve.ep.file = ve.file
			episodes = append(episodes, ve.ep)
		}
		sort.Slice(episodes, func(i, j int) bool { return episodes[i].num < episodes[j].num })
		voices = append(voices, turboVoice{title: vName, episodes: episodes})
	}
	return voices
}

func zetflixExtractSeasonNums(text string) []int {
	matches := ObrutSeasonNumRe.FindAllStringSubmatch(text, -1)
	seen := make(map[int]bool)
	var nums []int
	for _, m := range matches {
		n, err := strconv.Atoi(m[1])
		if err == nil && n > 0 && !seen[n] {
			seen[n] = true
			nums = append(nums, n)
		}
	}
	sort.Ints(nums)
	return nums
}

func turboExtractQualities(fileData string) map[string]string {
	quals := make(map[string]string)
	for _, m := range turboQualityRe.FindAllStringSubmatch(fileData, -1) {
		if len(m) >= 3 {
			quals[m[1]+"p"] = m[2]
		}
	}
	return quals
}

// ---------------------------------------------------------------------------
// Proxy helpers
// ---------------------------------------------------------------------------

// hostForEmbed picks the obrut host that was active at resolve time, falling
// back to the configured default — which is what writeSeasons/probe use when
// no embed has been resolved yet for the kpID.
func (t *turboChecker) hostForEmbed(embed turboEmbed) string {
	if embed.obrutHost != "" {
		return embed.obrutHost
	}
	return t.obrutHost
}

// cookiesForHost returns the FlareSolverr-captured cookies for host, if still
// fresh. Empty string is a safe default — obrut still serves stream segments,
// the wd_trust cookie just buys us fewer 403 retries.
func (t *turboChecker) cookiesForHost(host string) string {
	if host == "" {
		return ""
	}
	if v, ok := t.cookieCache.Load(host); ok {
		ce := v.(turboCookieEntry)
		if time.Now().Before(ce.expires) {
			return ce.cookies
		}
	}
	return ""
}

func turboProxyStream(rawURL string, req *http.Request, links *proxylink.Manager, obrutHost, cookies string) string {
	if links == nil || rawURL == "" {
		return rawURL
	}
	hdrs := map[string]string{
		"Origin":  "https://" + obrutHost,
		"Referer": "https://" + obrutHost + "/",
	}
	if cookies != "" {
		hdrs["Cookie"] = cookies
	}
	return streamHostFromRequest(req) + "/proxy/" + links.EncryptURIWithHeaders(rawURL, clientIP(req), "turbo", hdrs)
}

func turboSortedStreams(fileData string, req *http.Request, links *proxylink.Manager, obrutHost, cookies string) []map[string]string {
	quals := turboExtractQualities(fileData)
	type qs struct {
		q    string
		url  string
		rank int
	}
	rankMap := map[string]int{"2160p": 6, "1080p": 5, "720p": 4, "480p": 3, "360p": 2, "240p": 1}
	var items []qs
	for q, u := range quals {
		items = append(items, qs{q: q, url: u, rank: rankMap[q]})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].rank > items[j].rank })
	var out []map[string]string
	for _, it := range items {
		out = append(out, map[string]string{
			"quality": it.q,
			"url":     turboProxyStream(it.url, req, links, obrutHost, cookies),
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// Write responses
// ---------------------------------------------------------------------------

func (t *turboChecker) writeMovie(w http.ResponseWriter, req *http.Request, rjson bool, title, originalTitle string, embed turboEmbed, links *proxylink.Manager) {
	baseTitle := getsTVJoinName(title, originalTitle)
	data := make([]map[string]any, 0, len(embed.voices))
	labels := make([]string, 0, len(embed.voices))

	host := t.hostForEmbed(embed)
	cookies := t.cookiesForHost(host)
	for _, voice := range embed.voices {
		streams := turboSortedStreams(voice.file, req, links, host, cookies)
		if len(streams) == 0 {
			continue
		}
		best := streams[0]["url"]
		name := voice.title
		if name == "" {
			name = "По умолчанию"
		}
		row := map[string]any{
			"method":        "play",
			"url":           best,
			"stream":        best,
			"name":          name,
			"title":         baseTitle + " (" + name + ")",
			"streamquality": streams,
		}
		data = append(data, row)
		labels = append(labels, name)
	}

	if len(data) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "movie", "data": data})
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

func (t *turboChecker) writeSeasons(w http.ResponseWriter, req *http.Request, rjson bool, kpID int64, title, originalTitle string, embed turboEmbed) {
	host := hostFromRequest(req)
	encTitle := url.QueryEscape(title)
	encOriginal := url.QueryEscape(originalTitle)

	data := make([]map[string]any, 0, len(embed.seasons))
	labels := make([]string, 0, len(embed.seasons))

	for _, sn := range embed.seasons {
		name := strconv.Itoa(sn) + " сезон"
		link := host + "/lite/turbo?rjson=" + getsTVBool(rjson) +
			"&kinopoisk_id=" + strconv.FormatInt(kpID, 10) +
			"&title=" + encTitle +
			"&original_title=" + encOriginal +
			"&s=" + strconv.Itoa(sn)
		data = append(data, map[string]any{
			"method": "link",
			"id":     sn,
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
		writeJSON(w, http.StatusOK, map[string]any{"type": "season", "data": data})
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

func (t *turboChecker) writeEpisodes(w http.ResponseWriter, req *http.Request, rjson bool, kpID int64, title, originalTitle string, s int, voice string, embed turboEmbed, links *proxylink.Manager) {
	baseTitle := getsTVJoinName(title, originalTitle)
	obrutHost := t.hostForEmbed(embed)
	cookies := t.cookiesForHost(obrutHost)
	host := hostFromRequest(req)

	if len(embed.voices) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if voice == "" {
		best := embed.voices[0]
		for _, v := range embed.voices[1:] {
			if len(v.episodes) > len(best.episodes) {
				best = v
			}
		}
		voice = best.title
	}

	voiceRows := make([]map[string]any, 0, len(embed.voices))
	seenVoice := make(map[string]struct{})
	for _, v := range embed.voices {
		if _, ok := seenVoice[v.title]; ok {
			continue
		}
		seenVoice[v.title] = struct{}{}
		voiceRows = append(voiceRows, map[string]any{
			"method": "link",
			"name":   v.title,
			"active": v.title == voice,
			"url": host + "/lite/turbo?rjson=" + getsTVBool(rjson) +
				"&kinopoisk_id=" + strconv.FormatInt(kpID, 10) +
				"&title=" + url.QueryEscape(title) +
				"&original_title=" + url.QueryEscape(originalTitle) +
				"&s=" + strconv.Itoa(s) +
				"&t=" + url.QueryEscape(v.title),
		})
	}

	type episodeRow struct {
		data  map[string]any
		label string
		s, e  int
	}
	var rows []episodeRow

	for _, v := range embed.voices {
		if v.title != voice {
			continue
		}
		for _, ep := range v.episodes {
			streams := turboSortedStreams(ep.file, req, links, obrutHost, cookies)
			if len(streams) == 0 {
				continue
			}
			bestURL := streams[0]["url"]
			name := ep.title
			if name == "" {
				name = strconv.Itoa(ep.num) + " серия"
			}
			rows = append(rows, episodeRow{
				data: map[string]any{
					"method":        "play",
					"url":           bestURL,
					"stream":        bestURL,
					"s":             s,
					"e":             ep.num,
					"name":          name,
					"title":         baseTitle + " (" + name + ")",
					"voice_name":    voice,
					"streamquality": streams,
				},
				label: name,
				s:     s,
				e:     ep.num,
			})
		}
	}

	if len(rows) == 0 && len(voiceRows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		data := make([]map[string]any, 0, len(voiceRows)+len(rows))
		data = append(data, voiceRows...)
		for _, r := range rows {
			data = append(data, r.data)
		}
		writeJSON(w, http.StatusOK, map[string]any{"type": "episode", "data": data})
		return
	}

	var sb strings.Builder
	if len(voiceRows) > 1 {
		sb.WriteString(`<div class="videos__line">`)
		for _, vr := range voiceRows {
			active := ""
			if vr["active"] == true {
				active = " active"
			}
			sb.WriteString(`<div class="videos__button selector` + active + `" data-json='`)
			b, _ := stdjson.Marshal(vr)
			sb.WriteString(string(b))
			sb.WriteString(`'>` + vr["name"].(string) + `</div>`)
		}
		sb.WriteString(`</div>`)
	}
	sb.WriteString(`<div class="videos__line">`)
	for i, r := range rows {
		getsTVAppendMovieHTML(&sb, r.data, r.label, i == 0, r.s, r.e)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}
