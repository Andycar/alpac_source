package litesrc

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	stdjson "encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/proxyapi"
	"lampac-go/internal/proxylink"

	"github.com/rs/zerolog/log"
	"golang.org/x/net/proxy"
)

// ---------------------------------------------------------------------------
// Regex patterns
// ---------------------------------------------------------------------------
var (
	pidorezkaItemMarker  = `b-content__inline_item`
	pidorezkaLinkTitleRe = regexp.MustCompile(`href="https?://[^/]+/([^"]+)">([^<]+)</a>\s*<div>([0-9]{4})`)
	pidorezkaImgSearchRe = regexp.MustCompile(`<img src="([^"]+)"`)

	pidorezkaIDRe         = regexp.MustCompile(`/([0-9]+)-[^/]+\.html`)
	pidorezkaTranslatorRe = regexp.MustCompile(`(?s)<[^>]+ data-translator_id="([0-9]+)"([^>]*)>([^<]+)`)
	pidorezkaInitCDNRe    = regexp.MustCompile(`\.initCDNSeriesEvents\(\s*[0-9]+\s*,\s*([0-9]+)\s*,`)
	pidorezkaSeasonRe     = regexp.MustCompile(`data-tab_id="([0-9]+)"[^>]*>([^<]+)`)
	pidorezkaEpisodeRe    = regexp.MustCompile(`data-season_id="([0-9]+)"\s*data-episode_id="([0-9]+)"[^>]*>([^<]+)`)
	pidorezkaFavsRe       = regexp.MustCompile(`id="ctrl_favs"\s*value="([^"]+)"`)
	pidorezkaSubtitleRe   = regexp.MustCompile(`\[([^\]]+)\](https?://[^\n\r,']+\.vtt)`)
	pidorezkaStreamURLRe  = regexp.MustCompile(`(https?://[^\[\n\r, ]+)`)
	pidorezkaTrashInnerRe = regexp.MustCompile(`//[^/]+_//`)
	pidorezkaSerialRe     = regexp.MustCompile(`data-season_id=`)
	pidorezkaWaitingRe    = regexp.MustCompile(`(?i)Ожидаем фильм в хорошем качестве`)
	pidorezkaImgTitleRe   = regexp.MustCompile(`<img[^>]+title="([^"]+)"`)
	pidorezkaVoiceHrefRe  = regexp.MustCompile(`href="(?:https?://[^/]+)?/([^"]+)"`)
	pidorezkaStreamsRe    = regexp.MustCompile(`"streams"\s*:\s*"(.*?)"\s*,`)
	pidorezkaSubtitleJSON = regexp.MustCompile(`"subtitle":"([^"]+)"`)
	pidorezkaAnyURLRe     = regexp.MustCompile(`https?://[^\s"'\\<>,]+`)
	pidorezkaExtINFRe     = regexp.MustCompile(`(?i)#EXTINF:([0-9]+(?:\.[0-9]+)?)`)
	pidorezkaBWRe         = regexp.MustCompile(`(?i)\bBANDWIDTH=([0-9]+)`)
)

// pidorezkaUserAgents — Android UAs matching the APK's template.
// Chrome/120.0.0.0 is hardcoded in the APK (SettingsData.java:337).
// The APK uses Build.VERSION.RELEASE and Build.MANUFACTURER at runtime.
var pidorezkaUserAgents = []string{
	"Mozilla/5.0 (Linux; Android 14; samsung) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Linux; Android 13; samsung) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Linux; Android 14; Xiaomi) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
}

// voidboostCDNFallback rotates through known Voidboost CDN subdomains.
// Voidboost uses different subdomains for load balancing. The base domain
// "stream.voidboost.cc" often returns 404, so we replace it with a working subdomain.
// Only fiber and blitz are confirmed to serve video content.
// Other subdomains (phaeton, apollo, etc.) exist in DNS but may not accept stream tokens.
var voidboostCDNFallback = []string{
	"fiber.stream.voidboost.cc",
	"blitz.stream.voidboost.cc",
}
var voidboostCDNRand = rand.New(rand.NewSource(time.Now().UnixNano()))

func fixVoidboostURL(rawURL string) string {
	// Only fix URLs that use the bare stream.voidboost.cc domain.
	// Do NOT touch URLs that already have a subdomain (e.g. phaeton.stream.voidboost.cc).
	host := extractHostFromURL(rawURL)
	if host == "" {
		return rawURL
	}
	host = strings.ToLower(host)
	if host == "stream.voidboost.cc" {
		// Bare domain → pick a random CDN subdomain.
		subdomain := voidboostCDNFallback[voidboostCDNRand.Intn(len(voidboostCDNFallback))]
		return strings.Replace(rawURL, "stream.voidboost.cc", subdomain, 1)
	}
	// Already has a subdomain like phaeton.stream.voidboost.cc — leave it alone.
	return rawURL
}

// VoidboostCDNAlternatives returns a list of alternative CDN URLs by swapping the
// voidboost subdomain. Used by the proxy handler for 404 retry.
func VoidboostCDNAlternatives(rawURL string) []string {
	host := extractHostFromURL(rawURL)
	if host == "" {
		return nil
	}
	hostLower := strings.ToLower(host)
	if !strings.HasSuffix(hostLower, ".stream.voidboost.cc") && hostLower != "stream.voidboost.cc" {
		return nil
	}

	var alts []string
	for _, sub := range voidboostCDNFallback {
		alt := strings.Replace(rawURL, host, sub, 1)
		if alt != rawURL {
			alts = append(alts, alt)
		}
	}
	return alts
}

// extractHostFromURL extracts the hostname from a URL string without full url.Parse.
func extractHostFromURL(rawURL string) string {
	_, after, ok := strings.Cut(rawURL, "://")
	if !ok {
		return ""
	}
	rest := after
	// Strip path
	if slashIdx := strings.Index(rest, "/"); slashIdx >= 0 {
		rest = rest[:slashIdx]
	}
	// Strip port
	if colonIdx := strings.LastIndex(rest, ":"); colonIdx >= 0 {
		rest = rest[:colonIdx]
	}
	return rest
}

// ---------------------------------------------------------------------------
// Base64 trash patterns for stream URL decryption
// ---------------------------------------------------------------------------
var pidorezkaTrashBase = []string{
	"JCQhIUAkJEBeIUAjJCRA",
	"QEBAQEAhIyMhXl5e",
	"IyMjI14hISMjIUBA",
	"Xl5eIUAjIyEhIyM=",
	"JCQjISFAIyFAIyM=",
}

var pidorezkaTrashOld = []string{
	"QEA=", "QCM=", "QCE=", "QF4=", "QCQ=",
	"I0A=", "IyM=", "IyE=", "I14=", "IyQ=",
	"IUA=", "ISM=", "ISE=", "IV4=", "ISQ=",
	"XkA=", "XiM=", "XiE=", "Xl4=", "XiQ=",
	"JEA=", "JCM=", "JCE=", "JF4=", "JCQ=",
	"QEBA", "QEAj", "QEAh", "QEBe", "QEAk",
	"QCNA", "QCMj", "QCMh", "QCNe", "QCMk",
	"QCFA", "QCEj", "QCEh", "QCFe", "QCEk",
	"QF5A", "QF4j", "QF4h", "QF5e", "QF4k",
	"QCRA", "QCQj", "QCQh", "QCRe", "QCQk",
	"I0BA", "I0Aj", "I0Ah", "I0Be", "I0Ak",
	"IyNA", "IyMj", "IyMh", "IyNe", "IyMk",
	"IyFA", "IyEj", "IyEh", "IyFe", "IyEk",
	"I15A", "I14j", "I14h", "I15e", "I14k",
	"IyRA", "IyQj", "IyQh", "IyRe", "IyQk",
	"IUBA", "IUAj", "IUAh", "IUBe", "IUAk",
	"ISNA", "ISMj", "ISMh", "ISNe", "ISMk",
	"ISFA", "ISEj", "ISEh", "ISFe", "ISEk",
	"IV5A", "IV4j", "IV4h", "IV5e", "IV4k",
	"ISRA", "ISQj", "ISQh", "ISRe", "ISQk",
	"XkBA", "XkAj", "XkAh", "XkBe", "XkAk",
	"XiNA", "XiMj", "XiMh", "XiNe", "XiMk",
	"XiFA", "XiEj", "XiEh", "XiFe", "XiEk",
	"Xl5A", "Xl4j", "Xl4h", "Xl5e", "Xl4k",
	"XiRA", "XiQj", "XiQh", "XiRe", "XiQk",
	"JEBA", "JEAj", "JEAh", "JEBe", "JEAk",
	"JCNA", "JCMj", "JCMh", "JCNe", "JCMk",
	"JCFA", "JCEj", "JCEh", "JCFe", "JCEk",
	"JF5A", "JF4j", "JF4h", "JF5e", "JF4k",
	"JCRA", "JCQj", "JCQh", "JCRe", "JCQk",
}

// Quality levels in descending preference.
var pidorezkaQualities = []struct {
	key      string
	altKey   string
	label    string
	badgeKey string
}{
	{"2160p", "4K", "2160p", "4K"},
	{"1440p", "2K", "1440p", "2K"},
	{"1080p Ultra", "", "1080p", "FHD"},
	{"1080p", "", "1080p", "FHD"},
	{"720p", "", "720p", "HD"},
	{"480p", "", "480p", "SD"},
	{"360p", "", "360p", "SD"},
}

// ---------------------------------------------------------------------------
// pidorezkaChecker
// ---------------------------------------------------------------------------

type pidorezkaChecker struct {
	client      *http.Client
	proxyClient *http.Client
	host        string
	login       string
	password    string
	premium     bool
	hls         bool
	cookie      string

	// Auto-login state
	authMu     sync.RWMutex
	authDone   bool
	authCookie string // resolved cookie string after login
	lastLogin  time.Time

	// id -> full page URL. Some mirrors validate ajax referer against movie page.
	baseReferer sync.Map

	// id -> cached page HTML (with TTL). Avoids re-fetching serial pages
	// that hdrzk.org rate-limits on rapid sequential requests.
	pageCache   sync.Map // map[string]*pidorezkaPageCacheEntry
	janitorOnce sync.Once
}

type pidorezkaPageCacheEntry struct {
	html      string
	fetchedAt time.Time
}

const (
	pidorezkaPageCacheTTL    = 5 * time.Minute
	pidorezkaJanitorInterval = 10 * time.Minute
)

// startPageCacheJanitor evicts expired pageCache entries on a schedule.
// pageCache stores full HTML bodies (hundreds of KB each), so entries for
// never-re-requested IDs must be swept proactively — the on-read eviction
// alone leaks unbounded memory.
func (r *pidorezkaChecker) startPageCacheJanitor() {
	r.janitorOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(pidorezkaJanitorInterval)
			defer ticker.Stop()
			for range ticker.C {
				now := time.Now()
				r.pageCache.Range(func(k, v any) bool {
					if e, ok := v.(*pidorezkaPageCacheEntry); ok && now.Sub(e.fetchedAt) > pidorezkaPageCacheTTL {
						r.pageCache.Delete(k)
					}
					return true
				})
			}
		}()
	})
}

// getCachedPage returns cached page HTML if still valid.
func (r *pidorezkaChecker) getCachedPage(id string) (string, bool) {
	v, ok := r.pageCache.Load(id)
	if !ok {
		return "", false
	}
	entry := v.(*pidorezkaPageCacheEntry)
	if time.Since(entry.fetchedAt) > pidorezkaPageCacheTTL {
		r.pageCache.Delete(id)
		return "", false
	}
	return entry.html, true
}

// setCachedPage stores page HTML in cache. Only stores if HTML is large enough
// (small responses like 144 bytes are error pages).
func (r *pidorezkaChecker) setCachedPage(id, html string) {
	if len(html) < 1000 {
		return // don't cache error/empty pages
	}
	r.startPageCacheJanitor()
	r.pageCache.Store(id, &pidorezkaPageCacheEntry{html: html, fetchedAt: time.Now()})
}

func NewPidoRezkaChecker(cfg config.Config) *pidorezkaChecker {
	return newPidoRezkaCheckerWith(cfg.Online.PidoRezka, []string{"rezka", "rhs"})
}

func NewRhspremChecker(cfg config.Config) *pidorezkaChecker {
	return newPidoRezkaCheckerWith(cfg.Online.Rhsprem, []string{"rhsprem"})
}

func newPidoRezkaCheckerWith(src config.PidoRezkaSource, names []string) *pidorezkaChecker {
	host := strings.TrimSpace(src.Host)
	if host == "" {
		host = "https://hdrzk.org"
	}
	host = strings.TrimRight(host, "/")

	r := &pidorezkaChecker{
		host:     host,
		login:    src.Login,
		password: src.Password,
		premium:  src.Premium,
		hls:      src.HLS,
		cookie:   src.Cookie,
	}

	// Create HTTP client with cookie jar
	jar, _ := cookiejar.New(nil)
	r.client = &http.Client{
		Timeout: 25 * time.Second,
		Jar:     jar,
	}

	// If SOCKS5 proxy is configured, route all HTTP through it.
	// HDRezka blocks datacenter IPs; SOCKS5 (e.g. residential) bypasses this.
	socksAddr := strings.TrimSpace(src.SocksProxy)
	if socksAddr != "" {
		socksDialer, socksErr := proxy.SOCKS5("tcp", socksAddr, nil, proxy.Direct)
		if socksErr == nil {
			t := &http.Transport{
				TLSClientConfig:   &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"http/1.1"}},
				ForceAttemptHTTP2: false,
			}
			if cd, ok := socksDialer.(proxy.ContextDialer); ok {
				t.DialContext = cd.DialContext
			}
			r.client.Transport = t
			log.Info().Str("socks", socksAddr).Msg("pidorezka: HTTP client using SOCKS5 proxy")
		} else {
			log.Warn().Err(socksErr).Str("socks", socksAddr).Msg("pidorezka: failed to create SOCKS5 dialer")
		}
	}

	// Accept proxy mapping from any of the provided balancer names.
	// Force HTTP/1.1 only (no h2 ALPN).
	if r.client.Transport == nil {
		for _, name := range names {
			if transport := httpclient.TransportForBalancer(name); transport != nil {
				t := transport.Clone()
				if t.TLSClientConfig == nil {
					t.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
				}
				t.TLSClientConfig.NextProtos = []string{"http/1.1"}
				t.ForceAttemptHTTP2 = false
				r.client.Transport = t
				break
			}
		}
	}
	// Fallback: if no per-balancer transport found, try global ProxiedTransport.
	if r.proxyClient == nil && httpclient.ProxiedTransport != nil && r.client.Transport != httpclient.ProxiedTransport {
		t := httpclient.ProxiedTransport.Clone()
		if t.TLSClientConfig == nil {
			t.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		}
		t.TLSClientConfig.NextProtos = []string{"http/1.1"}
		t.ForceAttemptHTTP2 = false
		r.proxyClient = &http.Client{
			Timeout:   25 * time.Second,
			Jar:       jar,
			Transport: t,
		}
	}

	// If we have a static cookie string, seed the cookie jar with it
	if r.cookie != "" {
		r.seedCookieJar()
	}

	// Stream proxy for Rezka can require authenticated cookies.
	// Register dynamic cookie providers for all provided aliases.
	for _, name := range names {
		proxyapi.RegisterPluginHeaderProvider(name, r.proxyDynamicHeaders)
	}

	// Register SOCKS5 proxy for the /proxy/ handler so CDN stream requests
	// also go through the same proxy (CDN blocks datacenter IPs too).
	if socksAddr != "" {
		if err := httpclient.RegisterDirectProxy("socks5://"+socksAddr, names); err != nil {
			log.Warn().Err(err).Str("socks", socksAddr).Msg("pidorezka: failed to register SOCKS5 for proxy handler")
		} else {
			log.Info().Str("socks", socksAddr).Strs("names", names).Msg("pidorezka: registered SOCKS5 for proxy/CDN")
		}
	}

	// Initialize the persistent browser pool for fully legitimate browser sessions.
	initPidoRezkaBrowser(src.SocksProxy)

	return r
}

// seedCookieJar parses the static cookie string and adds cookies to the jar.
func (r *pidorezkaChecker) seedCookieJar() {
	u, err := url.Parse(r.host)
	if err != nil {
		return
	}

	raw := r.cookie
	// Ensure essential cookies
	if !strings.Contains(raw, "hdmbbs=") {
		raw = "hdmbbs=1; " + raw
	}
	if !strings.Contains(raw, "dle_user_taken") {
		raw = "dle_user_taken=1; " + raw
	}

	var cookies []*http.Cookie
	for part := range strings.SplitSeq(raw, ";") {
		part = strings.TrimSpace(part)
		if part == "" || !strings.Contains(part, "=") {
			continue
		}
		eqIdx := strings.Index(part, "=")
		name := strings.TrimSpace(part[:eqIdx])
		value := strings.TrimSpace(part[eqIdx+1:])
		if name == "" {
			continue
		}
		// Skip tracking cookies
		switch name {
		case "CLID", "MUID", "_clck", "_clsk":
			continue
		}
		if strings.HasPrefix(name, "_ym_") {
			continue
		}
		cookies = append(cookies, &http.Cookie{Name: name, Value: value})
	}
	if len(cookies) > 0 {
		r.client.Jar.SetCookies(u, cookies)
		r.authCookie = normalizePidoRezkaCookie(raw)
		r.applyAuthCookieToJar(r.authCookie)
		r.authDone = true
		log.Info().Str("host", r.host).Int("cookies", len(cookies)).Msg("pidorezka: seeded cookie jar from config")
	}
}

// ensureAuth performs auto-login if login/password are configured and we don't have cookies yet.
func (r *pidorezkaChecker) ensureAuth(ctx context.Context) {
	// If static cookie provided, nothing to do
	if r.cookie != "" {
		return
	}
	// If no login/password, nothing to do
	if r.login == "" || r.password == "" {
		return
	}

	r.authMu.RLock()
	done := r.authDone
	lastLogin := r.lastLogin
	r.authMu.RUnlock()

	// Don't retry login more than once per 2 minutes
	if done || (!lastLogin.IsZero() && time.Since(lastLogin) < 2*time.Minute) {
		return
	}

	r.authMu.Lock()
	defer r.authMu.Unlock()

	// Double-check
	if r.authDone {
		return
	}
	r.lastLogin = time.Now()

	log.Info().Str("host", r.host).Str("login", r.login).Msg("pidorezka: attempting browser login")

	// Primary: login via persistent browser (fully legitimate browser session).
	if err := r.browserLogin(); err != nil {
		log.Warn().Err(err).Msg("pidorezka: browser login failed, trying HTTP fallback")
		r.ensureAuthHTTP(ctx)
		return
	}
}

// ensureAuthHTTP is the legacy HTTP-based login fallback.
func (r *pidorezkaChecker) ensureAuthHTTP(ctx context.Context) {
	noRedirectClient := &http.Client{
		Timeout:   20 * time.Second,
		Transport: r.client.Transport,
		Jar:       r.client.Jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// Retry up to 3 times — SOCKS5 proxy may reset connections intermittently.
	for attempt := 1; attempt <= 3; attempt++ {
		form := url.Values{}
		form.Set("login_name", r.login)
		form.Set("login_password", r.password)
		form.Set("login_not_save", "0")

		loginCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		req, err := http.NewRequestWithContext(loginCtx, http.MethodPost, r.host+"/ajax/login/", strings.NewReader(form.Encode()))
		if err != nil {
			cancel()
			log.Warn().Err(err).Msg("pidorezka: login request creation failed")
			return
		}
		req.Header.Set("User-Agent", pidorezkaPickUserAgent(""))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", r.host)
		req.Header.Set("Referer", r.host+"/")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		req.Header.Set("X-Hdrezka-Android-App", "1")
		req.Header.Set("X-Hdrezka-Android-App-Version", "2.2.5")

		resp, err := noRedirectClient.Do(req)
		cancel()
		if err != nil {
			log.Warn().Err(err).Int("attempt", attempt).Msg("pidorezka: HTTP login request failed")
			if attempt < 3 {
				time.Sleep(time.Duration(attempt) * time.Second)
				continue
			}
			return
		}
		_ = resp.Body.Close()

		u, _ := url.Parse(r.host)
		var cookieParts []string
		for _, c := range r.client.Jar.Cookies(u) {
			cookieParts = append(cookieParts, c.Name+"="+c.Value)
		}
		cookieStr := strings.Join(cookieParts, "; ")

		if strings.Contains(cookieStr, "dle_user_id") && strings.Contains(cookieStr, "dle_password") {
			r.authCookie = normalizePidoRezkaCookie(cookieStr)
			r.applyAuthCookieToJar(r.authCookie)
			r.authDone = true
			log.Info().Str("host", r.host).Int("attempt", attempt).Msg("pidorezka: HTTP login successful")
			return
		}

		log.Warn().Str("host", r.host).Int("status", resp.StatusCode).Int("attempt", attempt).Msg("pidorezka: HTTP login no auth cookies, retrying")
		if attempt < 3 {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
	}
	log.Warn().Str("host", r.host).Msg("pidorezka: HTTP login failed after 3 attempts")
}

func (r *pidorezkaChecker) proxyDynamicHeaders() map[string]string {
	cookie := strings.TrimSpace(r.currentAuthCookie())
	if cookie == "" {
		return nil
	}
	return map[string]string{
		"Cookie": cookie,
	}
}

func (r *pidorezkaChecker) currentAuthCookie() string {
	r.authMu.RLock()
	cached := strings.TrimSpace(r.authCookie)
	r.authMu.RUnlock()

	if hasPidoRezkaAuthCookie(cached) {
		return cached
	}

	if r.client == nil || r.client.Jar == nil {
		return cached
	}
	u, err := url.Parse(r.host)
	if err != nil {
		return cached
	}
	jarCookies := r.client.Jar.Cookies(u)
	if len(jarCookies) == 0 {
		return cached
	}
	parts := make([]string, 0, len(jarCookies))
	for _, c := range jarCookies {
		name := strings.TrimSpace(c.Name)
		val := strings.TrimSpace(c.Value)
		if name == "" || val == "" {
			continue
		}
		switch name {
		case "CLID", "MUID", "_clck", "_clsk":
			continue
		}
		if strings.HasPrefix(name, "_ym_") {
			continue
		}
		parts = append(parts, name+"="+val)
	}
	if len(parts) == 0 {
		return cached
	}
	candidate := normalizePidoRezkaCookie(strings.Join(parts, "; "))
	if hasPidoRezkaAuthCookie(candidate) {
		r.authMu.Lock()
		r.authCookie = candidate
		r.authDone = true
		r.authMu.Unlock()
	}
	return candidate
}

// pidorezkaExtraCookies are tracking cookies that the APK appends to every request.
// Without them, Rezka may flag the session as non-app traffic.
const pidorezkaExtraCookies = "; allowed_comments=1; _ym_isad=1; _ym_visorc=b; dle_newpm=0"

// cookieStringWithExtras returns the auth cookie string with APK-style extra cookies appended.
func (r *pidorezkaChecker) cookieStringWithExtras() string {
	c := r.currentAuthCookie()
	if c == "" {
		return "hdmbbs=1; dle_user_taken=1" + pidorezkaExtraCookies
	}
	return c + pidorezkaExtraCookies
}

func hasPidoRezkaAuthCookie(cookie string) bool {
	cookie = strings.ToLower(cookie)
	return strings.Contains(cookie, "dle_user_id=") && strings.Contains(cookie, "dle_password=")
}

func normalizePidoRezkaCookie(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parts := make([]string, 0, 16)
	seen := make(map[string]bool, 16)
	hasAuth := false
	hasTaken := false
	hasHdmbbs := false

	for part := range strings.SplitSeq(raw, ";") {
		part = strings.TrimSpace(part)
		if part == "" || !strings.Contains(part, "=") {
			continue
		}
		eqIdx := strings.Index(part, "=")
		name := strings.TrimSpace(part[:eqIdx])
		value := strings.TrimSpace(part[eqIdx+1:])
		if name == "" || value == "" {
			continue
		}
		switch name {
		case "CLID", "MUID", "_clck", "_clsk":
			continue
		}
		if strings.HasPrefix(name, "_ym_") {
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		switch name {
		case "dle_user_id", "dle_password":
			hasAuth = true
		case "dle_user_taken":
			hasTaken = true
		case "hdmbbs":
			hasHdmbbs = true
		}
		parts = append(parts, name+"="+value)
	}

	if len(parts) == 0 {
		return ""
	}
	if !hasHdmbbs {
		parts = append([]string{"hdmbbs=1"}, parts...)
	}
	if hasAuth && !hasTaken {
		parts = append([]string{"dle_user_taken=1"}, parts...)
	}
	return strings.Join(parts, "; ")
}

func (r *pidorezkaChecker) applyAuthCookieToJar(cookie string) {
	cookie = strings.TrimSpace(cookie)
	if cookie == "" || r.client == nil || r.client.Jar == nil {
		return
	}
	u, err := url.Parse(r.host)
	if err != nil {
		return
	}
	out := make([]*http.Cookie, 0, 12)
	for part := range strings.SplitSeq(cookie, ";") {
		part = strings.TrimSpace(part)
		if part == "" || !strings.Contains(part, "=") {
			continue
		}
		eq := strings.Index(part, "=")
		name := strings.TrimSpace(part[:eq])
		value := strings.TrimSpace(part[eq+1:])
		if name == "" || value == "" {
			continue
		}
		out = append(out, &http.Cookie{Name: name, Value: value})
	}
	if len(out) == 0 {
		return
	}
	r.client.Jar.SetCookies(u, out)
}

func (r *pidorezkaChecker) Handle(cfg config.Config, plugin string, proxyLinks *proxylink.Manager) http.HandlerFunc {
	// Try auto-login on startup (background)
	go r.ensureAuth(context.Background())

	return func(w http.ResponseWriter, req *http.Request) {
		// Ensure we have auth cookies
		r.ensureAuth(req.Context())

		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show := r.checkSearch(req.Context(), strings.TrimSpace(req.URL.Query().Get("title")), strings.TrimSpace(req.URL.Query().Get("year")))
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			if show {
				quality := pluginQualityBadgeGet("rezka")
				if quality != "" {
					_, _ = fmt.Fprintf(w, `{"type":"movie","rch":true,"quality":"%s"}`, quality)
				} else {
					_, _ = w.Write([]byte(`{"rch":true}`))
				}
				return
			}
			_, _ = w.Write([]byte(`{"rch":false}`))
			return
		}

		// Route by path suffix.
		raw := strings.TrimPrefix(strings.ToLower(req.URL.Path), "/lite/")
		raw = strings.TrimPrefix(raw, "rc/")

		switch {
		case strings.HasPrefix(raw, "rezka/movie") || strings.HasPrefix(raw, "rhsprem/movie") || strings.HasPrefix(raw, "rhs/movie"):
			r.movie(w, req, plugin, proxyLinks)
		case strings.HasPrefix(raw, "rezka/serial") || strings.HasPrefix(raw, "rhsprem/serial") || strings.HasPrefix(raw, "rhs/serial"):
			r.serial(w, req, plugin, proxyLinks)
		default:
			r.embed(w, req, plugin, proxyLinks)
		}
	}
}

// ---------------------------------------------------------------------------
// checkSearch (kept from original)
// ---------------------------------------------------------------------------

func (r *pidorezkaChecker) checkSearch(ctx context.Context, title, year string) bool {
	if strings.TrimSpace(title) == "" {
		return false
	}

	log.Debug().Str("title", title).Str("host", r.host).Msg("pidorezka: checkSearch start")

	// Primary: browser-based search.
	var html string
	if h, ok := r.browserSearch(ctx, title); ok {
		html = h
	}

	// Fallback: HTTP search.
	if html == "" {
		searchURL := r.host + "/search/?do=search&subaction=search&q=" + url.QueryEscape(title)
		clients := r.httpClients()
		if r.proxyClient != nil {
			clients = []*http.Client{r.proxyClient, r.client}
		}

		for i, client := range clients {
			cCtx, cCancel := context.WithTimeout(ctx, 20*time.Second)
			cReq, _ := http.NewRequestWithContext(cCtx, http.MethodGet, searchURL, nil)
			r.setPageHeaders(cReq)
			resp, err := client.Do(cReq)
			if err != nil {
				cCancel()
				log.Debug().Err(err).Int("attempt", i+1).Str("url", searchURL).Msg("pidorezka: checkSearch request failed")
				continue
			}
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
			resp.Body.Close()
			cCancel()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				log.Debug().Int("status", resp.StatusCode).Int("attempt", i+1).Msg("pidorezka: checkSearch bad status, trying next client")
				continue
			}
			h := string(body)
			if strings.Contains(strings.ToLower(h), `class="error-code"`) && strings.Contains(strings.ToLower(h), "ошибка доступа") {
				log.Debug().Int("attempt", i+1).Msg("pidorezka: checkSearch access denied, trying next client")
				continue
			}
			html = h
			break
		}
	}
	if html == "" {
		return false
	}
	if !strings.Contains(html, pidorezkaItemMarker) {
		return false
	}

	matches := pidorezkaLinkTitleRe.FindAllStringSubmatch(html, -1)
	if len(matches) == 0 {
		return true
	}
	if year == "" {
		return true
	}

	wantTitle := normalizeSearchTitle(title)
	for _, m := range matches {
		if len(m) < 4 {
			continue
		}
		gotTitle := normalizeSearchTitle(strings.TrimSpace(m[2]))
		gotYear := strings.TrimSpace(m[3])
		if gotTitle == "" {
			continue
		}
		if gotYear == year && gotTitle == wantTitle {
			return true
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// embed — main page: search → similar → translators list
// ---------------------------------------------------------------------------

type pidorezkaSimilar struct {
	Title string
	Year  string
	Href  string
	Img   string
}

func (r *pidorezkaChecker) embed(w http.ResponseWriter, req *http.Request, plugin string, proxyLinks *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	similar := parseBoolParam(q.Get("similar"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	clarification, _ := strconv.Atoi(q.Get("clarification"))
	href := strings.TrimSpace(q.Get("href"))
	year := strings.TrimSpace(q.Get("year"))
	seasonParam := strings.TrimSpace(q.Get("s"))
	idParam := strings.TrimSpace(q.Get("id"))

	// If id provided instead of href, construct href.
	// Only use id as href if it looks like a rezka path (contains "-" or ".html"),
	// not a bare TMDB/KP numeric id.
	if href == "" && idParam != "" && (strings.Contains(idParam, "-") || strings.Contains(idParam, ".html") || strings.Contains(idParam, "/")) {
		href = idParam
	}

	// Search if no href.
	if href == "" {
		searchQuery := originalTitle
		if clarification == 1 || searchQuery == "" {
			searchQuery = title
		}
		if searchQuery == "" {
			writeGetsTVEmpty(w, rjson)
			return
		}

		searchHTML, ok := r.doSearch(req, searchQuery)
		if !ok {
			writeGetsTVEmpty(w, rjson)
			return
		}

		results := r.parseSearchResults(searchHTML, title, originalTitle, year)
		if similar || results.exactHref == "" {
			if len(results.items) == 0 {
				writeGetsTVEmpty(w, rjson)
				return
			}
			r.writeSimilar(w, req, rjson, plugin, results.items, title, originalTitle, year)
			return
		}
		href = results.exactHref
	}

	// Fetch page.
	pageURL := href
	if !strings.HasPrefix(pageURL, "http") {
		pageURL = r.host + "/" + strings.TrimLeft(pageURL, "/")
	}

	pageHTML, ok := r.fetchPage(req, pageURL)
	if !ok {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Cache page HTML for serial handler reuse (avoids rate-limited re-fetch).
	if id := submatch1(pidorezkaIDRe, pageURL); id != "" {
		r.setCachedPage(id, pageHTML)
	}

	if pidorezkaWaitingRe.MatchString(pageHTML) {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Extract ID from URL.
	id := submatch1(pidorezkaIDRe, pageURL)
	if id == "" {
		id = submatch1(pidorezkaIDRe, href)
	}
	if id == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}
	r.baseReferer.Store(id, pageURL)

	// Extract favs.
	favs := submatch1(pidorezkaFavsRe, pageHTML)

	isSerial := pidorezkaSerialRe.MatchString(pageHTML)

	// Extract translators.
	type translator struct {
		id       string
		name     string
		director bool
		voice    string
	}

	matches := pidorezkaTranslatorRe.FindAllStringSubmatch(pageHTML, -1)
	translators := make([]translator, 0, len(matches))
	for _, m := range matches {
		if len(m) < 4 {
			continue
		}
		tID := strings.TrimSpace(m[1])
		attrs := m[2]
		rawName := strings.TrimSpace(m[3])
		if rawName == "" || rawName == "-" {
			rawName = "Оригинал"
		}
		// Check prem_translator
		if !r.premium && strings.Contains(attrs, "prem_translator") {
			continue
		}
		isDirector := strings.Contains(attrs, `data-director="1"`)
		voiceHref := ""
		if mHref := pidorezkaVoiceHrefRe.FindStringSubmatch(attrs); len(mHref) >= 2 {
			voiceHref = strings.TrimSpace(mHref[1])
		}

		// Check for img title to append.
		if imgTitle := submatch1(pidorezkaImgTitleRe, m[0]); imgTitle != "" {
			if !strings.Contains(rawName, imgTitle) {
				rawName += " (" + imgTitle + ")"
			}
		}

		translators = append(translators, translator{id: tID, name: rawName, director: isDirector, voice: voiceHref})
	}

	// If serial, try to get default translator from initCDNSeriesEvents.
	defaultTrs := ""
	if isSerial {
		defaultTrs = submatch1(pidorezkaInitCDNRe, pageHTML)
	}

	// If no translators found, create a default one.
	if len(translators) == 0 {
		trs := defaultTrs
		if trs == "" {
			trs = "0"
		}
		translators = []translator{{id: trs, name: "Оригинал"}}
	}

	host := hostFromRequest(req)
	encTitle := url.QueryEscape(title)
	encOriginal := url.QueryEscape(originalTitle)
	encHref := url.QueryEscape(href)

	data := make([]map[string]any, 0, len(translators))
	labels := make([]string, 0, len(translators))

	for _, tr := range translators {
		var link string
		var method string
		if isSerial {
			method = "link"
			link = fmt.Sprintf("%s/lite/%s/serial?title=%s&original_title=%s&href=%s&id=%s&t=%s",
				host, plugin, encTitle, encOriginal, encHref, id, tr.id)
			if seasonParam != "" {
				link += "&s=" + seasonParam
			}
			if rjson {
				link += "&rjson=true"
			}
		} else {
			method = "call"
			link = fmt.Sprintf("%s/lite/%s/movie?title=%s&original_title=%s&href=%s&id=%s&t=%s&favs=%s&voice_name=%s&rjson=true&call=true",
				host, plugin, encTitle, encOriginal, encHref, id, tr.id, url.QueryEscape(favs), url.QueryEscape(tr.name))
			if tr.voice != "" {
				link += "&voice=" + url.QueryEscape(tr.voice)
			}
			if tr.director {
				link += "&director=1"
			}
		}

		row := map[string]any{
			"method": method,
			"url":    link,
			"name":   tr.name,
			"active": tr.id == defaultTrs,
		}
		data = append(data, row)
		labels = append(labels, tr.name)
	}

	if rjson {
		tp := "voice"
		writeJSON(w, http.StatusOK, map[string]any{
			"type": tp,
			"data": data,
		})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i := range data {
		getsTVAppendMovieHTML(&sb, data[i], labels[i], i == 0, 0, 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// ---------------------------------------------------------------------------
// serial — seasons and episodes navigation
// ---------------------------------------------------------------------------

func (r *pidorezkaChecker) serial(w http.ResponseWriter, req *http.Request, plugin string, proxyLinks *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	href := strings.TrimSpace(q.Get("href"))
	id := strings.TrimSpace(q.Get("id"))
	t := strings.TrimSpace(q.Get("t"))
	s, sOK := getsTVQueryInt(q.Get("s"))

	if id == "" || t == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}
	r.storeBaseReferer(id, href)

	// Get episodes list — try AJAX first, fall back to page HTML parsing.
	var seasonMatches [][]string
	var episodesHTML string // raw HTML with episode data
	epResp, ok := r.ajaxPost(req, id, t, "get_episodes", "", "", "", "")
	if ok {
		var epData struct {
			Episodes string `json:"episodes"`
			Seasons  string `json:"seasons"`
		}
		if err := stdjson.Unmarshal([]byte(epResp), &epData); err == nil {
			seasonMatches = pidorezkaSeasonRe.FindAllStringSubmatch(epData.Seasons, -1)
			episodesHTML = epData.Episodes
		}
	}

	// Fallback: parse seasons/episodes from the page HTML directly.
	// hdrzk.org returns {} for get_episodes but embeds all data in the page.
	// Try cache first (embed already fetched this page), then re-fetch.
	if len(seasonMatches) == 0 {
		var pageHTML string
		var pageOK bool

		// Check cache first — embed already fetched this page.
		if cached, cacheHit := r.getCachedPage(id); cacheHit {
			pageHTML = cached
			pageOK = true
			log.Debug().Str("id", id).Int("htmlLen", len(cached)).Msg("pidorezka: serial using cached page HTML")
		}

		// Cache miss — fetch from browser.
		if !pageOK {
			pageURL := r.resolveBaseReferer(id, href)
			if pageURL == "" && href != "" {
				pageURL = r.host + "/" + strings.TrimLeft(href, "/")
			}
			if pageURL != "" {
				if html, fetchOK := r.fetchPage(req, pageURL); fetchOK {
					pageHTML = html
					pageOK = true
					r.setCachedPage(id, html)
				}
			}
		}

		if pageOK {
			seasonMatches = pidorezkaSeasonRe.FindAllStringSubmatch(pageHTML, -1)
			if episodesHTML == "" {
				episodesHTML = pageHTML
			}
			log.Debug().Int("seasons", len(seasonMatches)).Str("id", id).Msg("pidorezka: serial fallback parsed seasons from page HTML")
		}
	}
	if len(seasonMatches) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(req)
	encTitle := url.QueryEscape(title)
	encOriginal := url.QueryEscape(originalTitle)
	encHref := url.QueryEscape(href)

	// Build voice (translator) list by fetching the page.
	voiceURLs := r.extractVoiceList(req, href, id, plugin, host, encTitle, encOriginal, encHref, t, s, sOK, rjson)

	// If no season selected, show season list.
	if !sOK || s <= 0 {
		sData := make([]map[string]any, 0, len(seasonMatches))
		sLabels := make([]string, 0, len(seasonMatches))
		for _, m := range seasonMatches {
			if len(m) < 3 {
				continue
			}
			sID := strings.TrimSpace(m[1])
			sName := strings.TrimSpace(m[2])
			link := fmt.Sprintf("%s/lite/%s/serial?title=%s&original_title=%s&href=%s&id=%s&t=%s&s=%s",
				host, plugin, encTitle, encOriginal, encHref, id, t, sID)
			if rjson {
				link += "&rjson=true"
			}
			sData = append(sData, map[string]any{
				"method": "link",
				"url":    link,
				"name":   sName,
			})
			sLabels = append(sLabels, sName)
		}

		if rjson {
			payload := map[string]any{
				"type": "season",
				"data": sData,
			}
			if len(voiceURLs) > 0 {
				payload["voice"] = voiceURLs
			}
			writeJSON(w, http.StatusOK, payload)
			return
		}

		var sb strings.Builder
		if len(voiceURLs) > 0 {
			sb.WriteString(`<div class="videos__line">`)
			for _, row := range voiceURLs {
				getsTVAppendVoiceHTML(&sb, row)
			}
			sb.WriteString(`</div>`)
		}
		sb.WriteString(`<div class="videos__line">`)
		for i, row := range sData {
			getsTVAppendSeasonHTML(&sb, row, sLabels[i], i == 0)
		}
		sb.WriteString(`</div>`)
		writeHTML(w, http.StatusOK, sb.String())
		return
	}

	// Season selected — show episodes.
	baseTitle := getsTVJoinName(title, originalTitle)

	epMatches := pidorezkaEpisodeRe.FindAllStringSubmatch(episodesHTML, -1)
	eData := make([]map[string]any, 0, len(epMatches))
	eLabels := make([]string, 0, len(epMatches))
	eSeason := make([]int, 0, len(epMatches))
	eNums := make([]int, 0, len(epMatches))

	for _, m := range epMatches {
		if len(m) < 4 {
			continue
		}
		epSeason, _ := strconv.Atoi(strings.TrimSpace(m[1]))
		if epSeason != s {
			continue
		}
		epNum, _ := strconv.Atoi(strings.TrimSpace(m[2]))
		epName := strings.TrimSpace(m[3])
		if epName == "" {
			epName = fmt.Sprintf("Серия %d", epNum)
		}

		link := fmt.Sprintf("%s/lite/%s/movie?title=%s&original_title=%s&href=%s&id=%s&t=%s&s=%d&e=%d&rjson=true&call=true",
			host, plugin, encTitle, encOriginal, encHref, id, t, s, epNum)
		if mHref := pidorezkaVoiceHrefRe.FindStringSubmatch(m[0]); len(mHref) >= 2 {
			voice := strings.TrimSpace(mHref[1])
			if voice != "" {
				link += "&voice=" + url.QueryEscape(voice)
			}
		}

		row := map[string]any{
			"method": "call",
			"url":    link,
			"s":      s,
			"e":      epNum,
			"name":   epName,
			"title":  fmt.Sprintf("%s (%s)", baseTitle, epName),
		}
		eData = append(eData, row)
		eLabels = append(eLabels, epName)
		eSeason = append(eSeason, s)
		eNums = append(eNums, epNum)
	}

	if len(eData) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		payload := map[string]any{
			"type": "episode",
			"data": eData,
		}
		if len(voiceURLs) > 0 {
			payload["voice"] = voiceURLs
		}
		writeJSON(w, http.StatusOK, payload)
		return
	}

	var sb strings.Builder
	if len(voiceURLs) > 0 {
		sb.WriteString(`<div class="videos__line">`)
		for _, row := range voiceURLs {
			getsTVAppendVoiceHTML(&sb, row)
		}
		sb.WriteString(`</div>`)
	}
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range eData {
		getsTVAppendMovieHTML(&sb, row, eLabels[i], i == 0, eSeason[i], eNums[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// extractVoiceList fetches the page and extracts translators for serial voice filter.
func (r *pidorezkaChecker) extractVoiceList(req *http.Request, href, id, plugin, host, encTitle, encOriginal, encHref, currentT string, s int, sOK, rjson bool) []map[string]any {
	if href == "" {
		return nil
	}

	pageURL := href
	if !strings.HasPrefix(pageURL, "http") {
		pageURL = r.host + "/" + strings.TrimLeft(pageURL, "/")
	}

	pageHTML, ok := r.fetchPage(req, pageURL)
	if !ok {
		return nil
	}
	if id != "" {
		r.baseReferer.Store(id, pageURL)
	}

	matches := pidorezkaTranslatorRe.FindAllStringSubmatch(pageHTML, -1)
	if len(matches) < 2 {
		// Only one or no translators — no need for a voice selector.
		return nil
	}

	voiceURLs := make([]map[string]any, 0, len(matches))
	for _, m := range matches {
		if len(m) < 4 {
			continue
		}
		tID := strings.TrimSpace(m[1])
		attrs := m[2]
		rawName := strings.TrimSpace(m[3])
		if rawName == "" || rawName == "-" {
			rawName = "Оригинал"
		}
		if !r.premium && strings.Contains(attrs, "prem_translator") {
			continue
		}
		if imgTitle := submatch1(pidorezkaImgTitleRe, m[0]); imgTitle != "" {
			if !strings.Contains(rawName, imgTitle) {
				rawName += " (" + imgTitle + ")"
			}
		}

		link := fmt.Sprintf("%s/lite/%s/serial?title=%s&original_title=%s&href=%s&id=%s&t=%s",
			host, plugin, encTitle, encOriginal, encHref, id, tID)
		if sOK && s > 0 {
			link += "&s=" + strconv.Itoa(s)
		}
		if rjson {
			link += "&rjson=true"
		}

		voiceURLs = append(voiceURLs, map[string]any{
			"method": "link",
			"name":   rawName,
			"active": tID == currentT,
			"url":    link,
		})
	}

	return voiceURLs
}

// ---------------------------------------------------------------------------
// movie — get stream URL
// ---------------------------------------------------------------------------

func (r *pidorezkaChecker) movie(w http.ResponseWriter, req *http.Request, plugin string, proxyLinks *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	href := strings.TrimSpace(q.Get("href"))
	voice := strings.TrimSpace(q.Get("voice"))
	id := strings.TrimSpace(q.Get("id"))
	t := strings.TrimSpace(q.Get("t"))
	directorStr := strings.TrimSpace(q.Get("director"))
	favs := strings.TrimSpace(q.Get("favs"))
	sStr := strings.TrimSpace(q.Get("s"))
	eStr := strings.TrimSpace(q.Get("e"))

	if id == "" || t == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}
	r.storeBaseReferer(id, href)

	s, _ := strconv.Atoi(sStr)
	e, _ := strconv.Atoi(eStr)

	var action string
	if s > 0 && e > 0 {
		action = "get_stream"
	} else {
		action = "get_movie"
	}

	urlStr := ""
	subtitleStr := ""

	// Run ajaxPost and movieFromPage concurrently.
	// ajaxPost often hangs for 25s when Rezka is blocking, while page extraction
	// succeeds in ~5s. Running them in parallel cuts response time significantly.
	//
	// IMPORTANT: For get_stream (serial episodes), movieFromPage ALWAYS returns the
	// default/first episode stream, so we must NOT use it — only ajaxPost (which uses
	// browserGetStreamViaClick to get the correct episode).
	type ajaxResult struct {
		body string
		ok   bool
	}
	type pageResult struct {
		url, sub string
		ok       bool
	}
	ajaxCh := make(chan ajaxResult, 1)
	pageCh := make(chan pageResult, 1)

	// Ajax with shorter timeout for get_movie (original 25s is too long when blocked).
	go func() {
		ajaxCtx := req.Context()
		if action == "get_movie" {
			var cancel context.CancelFunc
			ajaxCtx, cancel = context.WithTimeout(req.Context(), 8*time.Second)
			defer cancel()
		}
		ajaxReq := req.Clone(ajaxCtx)
		body, ok := r.ajaxPost(ajaxReq, id, t, action, directorStr, favs, sStr, eStr)
		ajaxCh <- ajaxResult{body, ok}
	}()

	// Skip movieFromPage for serial episodes — it always returns the default/first episode.
	if action == "get_stream" {
		pageCh <- pageResult{}
	} else {
		go func() {
			u, s, ok := r.movieFromPage(req, id, href, voice)
			pageCh <- pageResult{u, s, ok}
		}()
	}

	// Wait for both results.
	ajaxRes := <-ajaxCh
	pageRes := <-pageCh

	if ajaxRes.ok {
		var result struct {
			URL      any `json:"url"`
			Subtitle any `json:"subtitle"`
		}
		if err := stdjson.Unmarshal([]byte(ajaxRes.body), &result); err != nil {
			log.Debug().Err(err).Str("id", id).Int("bodyLen", len(ajaxRes.body)).Msg("pidorezka: movie unmarshal error")
		} else {
			urlStr, _ = result.URL.(string)
			subtitleStr, _ = result.Subtitle.(string)
		}
	}
	referer := r.resolveBaseReferer(id, href)

	// Fallback/override with direct page extraction.
	// Page streams correspond to the *default* translator, not the selected one (t param),
	// so we prefer the ajax result (translator-specific) and use page only as fallback.
	if fbURL, fbSub, fbOK := pageRes.url, pageRes.sub, pageRes.ok; fbOK {
		log.Info().Str("fbURL", fbURL).Msg("DEBUG: movieFromPage ok")
		choosePage := false // default: prefer ajax (translator-specific)
		if urlStr == "" || urlStr == "false" {
			// Ajax returned nothing — page is our only option.
			choosePage = true
		} else if action == "get_movie" {
			ajaxDecoded := pidorezkaDecodeBase64(urlStr)
			pageDecoded := pidorezkaDecodeBase64(fbURL)

			ajaxPromo := pidorezkaLooksLikePromo(ajaxDecoded)
			pagePromo := pidorezkaLooksLikePromo(pageDecoded)
			log.Info().Bool("ajaxPromo", ajaxPromo).Bool("pagePromo", pagePromo).Msg("DEBUG: promo check results")

			// If ajax looks like promo but page does not, prefer page.
			if ajaxPromo && !pagePromo {
				choosePage = true
			}

			// If ajax returned no qualities but page has some, prefer page.
			ajaxQual := pidorezkaCountQualities(ajaxDecoded)
			pageQual := pidorezkaCountQualities(pageDecoded)
			if ajaxQual == 0 && pageQual > 0 {
				choosePage = true
			}

			// Fast path: if neither looks like promo by URL and the chosen
			// source has ≥3 qualities, skip expensive HLS duration probes.
			// This saves 10-20s per request in the common case.
			chosenQual := ajaxQual
			if choosePage {
				chosenQual = pageQual
			}
			skipDurationProbe := !ajaxPromo && !pagePromo && chosenQual >= 3

			if !skipDurationProbe {
				if !choosePage && ajaxDecoded != "" {
					ajaxDur := r.probeDecodedDuration(req, ajaxDecoded, referer)
					if ajaxDur > 0 && ajaxDur < 120 {
						pageDur := r.probeDecodedDuration(req, pageDecoded, referer)
						if pageDur > ajaxDur {
							choosePage = true
							log.Info().
								Float64("pageDur", pageDur).
								Float64("ajaxDur", ajaxDur).
								Str("id", id).
								Msg("pidorezka: ajax stream looks like promo (short duration), preferring page")
						}
					}
				} else if choosePage && pageDecoded != "" {
					pageDur := r.probeDecodedDuration(req, pageDecoded, referer)
					if pageDur > 0 && pageDur < 120 {
						ajaxDur := r.probeDecodedDuration(req, ajaxDecoded, referer)
						if ajaxDur > pageDur {
							choosePage = false
							log.Info().
								Float64("pageDur", pageDur).
								Float64("ajaxDur", ajaxDur).
								Str("id", id).
								Msg("pidorezka: page stream looks like promo (short duration), preferring ajax")
						}
					}
				}
			} else {
				log.Debug().Int("chosenQual", chosenQual).Msg("pidorezka: skipping duration probe (not promo, enough qualities)")
			}
		}

		log.Info().Bool("choosePage", choosePage).Msg("DEBUG: chosen source")
		if choosePage {
			urlStr = fbURL
			if strings.TrimSpace(fbSub) != "" {
				subtitleStr = fbSub
			}
			log.Info().
				Str("id", id).
				Str("action", action).
				Msg("pidorezka: selected page streams over ajax")
		}
	}

	// Last-resort browser fallback (Mirage-style): execute the same ajax call
	// from a real Chromium context to capture any JS/session-dependent tokens.
	// Skip entirely when we already have valid multi-quality streams (not promo).
	if action == "get_movie" {
		currentDecoded := ""
		currentQual := 0
		if urlStr != "" && urlStr != "false" {
			currentDecoded = pidorezkaDecodeBase64(urlStr)
			currentQual = pidorezkaCountQualities(currentDecoded)
		}

		// If we have ≥3 qualities and URL doesn't look like promo, skip browser entirely.
		skipBrowser := currentQual >= 3 && !pidorezkaLooksLikePromo(currentDecoded)

		currentDur := 0.0
		if !skipBrowser && currentDecoded != "" {
			currentDur = r.probeDecodedDuration(req, currentDecoded, referer)
		}

		needsBrowser := !skipBrowser && ((urlStr == "" || urlStr == "false") || (currentDur > 0 && currentDur < 120))
		if needsBrowser {
			if bURL, bSub, bOK := r.movieFromBrowser(req.Context(), id, t, action, directorStr, favs, sStr, eStr, href); bOK {
				bDecoded := pidorezkaDecodeBase64(bURL)
				bDur := 0.0
				if bDecoded != "" {
					bDur = r.probeDecodedDuration(req, bDecoded, referer)
				}

				useBrowser := false
				if urlStr == "" || urlStr == "false" {
					useBrowser = bDecoded != ""
				} else if currentDur > 0 && currentDur < 120 {
					if bDur >= 120 || bDur > currentDur+30 {
						useBrowser = true
					}
				} else if currentDur == 0 && bDur > 0 {
					useBrowser = true
				}

				if useBrowser {
					urlStr = bURL
					if strings.TrimSpace(bSub) != "" {
						subtitleStr = bSub
					}
					log.Info().
						Str("id", id).
						Str("t", t).
						Float64("prevDur", currentDur).
						Float64("browserDur", bDur).
						Msg("pidorezka: browser fallback selected")
				} else {
					log.Info().
						Str("id", id).
						Str("t", t).
						Float64("prevDur", currentDur).
						Float64("browserDur", bDur).
						Msg("pidorezka: browser fallback did not improve stream")
				}
			}
		}
	}

	log.Info().Str("urlStr", urlStr).Msg("DEBUG: final urlStr before check")
	if urlStr == "" || urlStr == "false" {
		if ajaxRes.ok {

			log.Warn().
				Str("id", id).
				Str("t", t).
				Str("action", action).
				Str("href", href).
				Str("voice", voice).
				Str("ajax_preview", ajaxRes.body[:min(300, len(ajaxRes.body))]).
				Msg("pidorezka: movie failed (ajax/page empty)")
		} else {
			log.Warn().
				Str("id", id).
				Str("t", t).
				Str("action", action).
				Str("href", href).
				Str("voice", voice).
				Msg("pidorezka: movie failed (ajax request failed, page fallback failed)")
		}
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Decode Base64 stream data.
	decoded := pidorezkaDecodeBase64(urlStr)
	if decoded == "" {
		log.Warn().
			Str("id", id).
			Str("t", t).
			Int("urlLen", len(urlStr)).
			Str("urlPrefix", urlStr[:min(180, len(urlStr))]).
			Msg("pidorezka: movie decodeBase64 returned empty")
		writeGetsTVEmpty(w, rjson)
		return
	}

	preferredRaw := ""
	if action == "get_movie" {
		// Skip expensive playable-URL probing when we have enough qualities
		// and the URL doesn't look like promo. pickPlayableRawURL probes up to
		// 8 HLS URLs which adds 10-20s.
		qual := pidorezkaCountQualities(decoded)
		if qual < 3 || pidorezkaLooksLikePromo(decoded) {
			preferredRaw = r.pickPlayableRawURL(req, decoded, referer)
		}
	}

	// Extract all qualities.
	baseTitle := getsTVJoinName(title, originalTitle)
	streams, qualMap := r.extractQualities(req, decoded, plugin, proxyLinks, referer)
	if len(streams) == 0 {
		log.Warn().
			Str("id", id).
			Str("t", t).
			Int("decodedLen", len(decoded)).
			Str("decodedPreview", decoded[:min(300, len(decoded))]).
			Msg("pidorezka: movie extractQualities returned 0 streams")
		writeGetsTVEmpty(w, rjson)
		return
	}
	if preferredRaw != "" {
		preferredRaw = fixVoidboostURL(preferredRaw)
		preferredURL := streamProxyDirectURL(req, preferredRaw, plugin)
		if preferredURL != "" {
			streams[0]["url"] = preferredURL
			if _, ok := streams[0]["label"]; !ok {
				streams[0]["label"] = "auto"
			}
			qualMap["auto"] = preferredURL
		}
	}

	// Parse subtitles.
	var subs []map[string]any
	if subtitleStr != "" && subtitleStr != "false" {
		subs = pidorezkaParseSubtitles(subtitleStr)
	}

	// Build response.
	bestURL := streams[0]["url"].(string)
	voiceName := strings.TrimSpace(q.Get("voice_name"))
	if voiceName == "" {
		voiceName = "Оригинал"
	}

	row := map[string]any{
		"method": "play",
		"url":    bestURL,
		"stream": bestURL,
		"name":   voiceName,
		"title":  baseTitle,
	}
	if len(qualMap) > 0 {
		row["quality"] = qualMap
		row["qualitys"] = qualMap
	}
	if len(subs) > 0 {
		row["subtitles"] = subs
	}
	if s > 0 {
		row["s"] = s
	}
	if e > 0 {
		row["e"] = e
	}

	data := []map[string]any{row}

	if rjson {
		// When called via method:"call" from embed, Lampa expects a flat
		// play-object ({method,url,quality,...}), not wrapped in {type,data}.
		if parseBoolParam(q.Get("call")) && len(data) > 0 {
			writeJSON(w, http.StatusOK, data[0])
			return
		}
		tp := "movie"
		if s > 0 && e > 0 {
			tp = "episode"
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"type": tp,
			"data": data,
		})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	season := 0
	episode := 0
	if s > 0 {
		season = s
	}
	if e > 0 {
		episode = e
	}
	getsTVAppendMovieHTML(&sb, row, voiceName, true, season, episode)
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (r *pidorezkaChecker) storeBaseReferer(id, href string) {
	id = strings.TrimSpace(id)
	href = strings.TrimSpace(href)
	if id == "" || href == "" {
		return
	}
	pageURL := href
	if !strings.HasPrefix(pageURL, "http") {
		pageURL = r.host + "/" + strings.TrimLeft(pageURL, "/")
	}
	r.baseReferer.Store(id, pageURL)
}

func (r *pidorezkaChecker) resolveBaseReferer(id, href string) string {
	id = strings.TrimSpace(id)
	if id != "" {
		if v, ok := r.baseReferer.Load(id); ok {
			if ref, ok := v.(string); ok && strings.HasPrefix(ref, "http") {
				return ref
			}
		}
	}
	href = strings.TrimSpace(href)
	if href == "" {
		return r.host + "/"
	}
	if strings.HasPrefix(href, "http") {
		return href
	}
	return r.host + "/" + strings.TrimLeft(href, "/")
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

func (r *pidorezkaChecker) setPageHeaders(req *http.Request) {
	ua := pidorezkaPickUserAgent("")
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,*/*;q=0.8")
	req.Header.Set("Accept-Language", "ru,en-US;q=0.9,en;q=0.8")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
	req.Header.Set("DNT", "1")
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	req.Header.Set("sec-ch-ua", pidorezkaSecCHUA(ua))
	req.Header.Set("sec-ch-ua-mobile", "?1")
	req.Header.Set("sec-ch-ua-platform", `"Android"`)
	req.Header.Set("Referer", r.host+"/")
	req.Header.Set("X-Hdrezka-Android-App", "1")
	req.Header.Set("X-Hdrezka-Android-App-Version", "2.2.5")
	req.Header.Set("X-Requested-With", "com.falcofemoralis.hdrezkaapp")
	// Force normalized auth cookies with extra tracking cookies (matches APK behavior).
	req.Header.Set("Cookie", r.cookieStringWithExtras())
}

func (r *pidorezkaChecker) setAjaxHeaders(req *http.Request, id string) {
	ua := pidorezkaPickUserAgent("")
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")
	req.Header.Set("Accept-Language", "ru,en-US;q=0.9,en;q=0.8")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
	req.Header.Set("DNT", "1")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("sec-ch-ua", pidorezkaSecCHUA(ua))
	req.Header.Set("sec-ch-ua-mobile", "?1")
	req.Header.Set("sec-ch-ua-platform", `"Android"`)
	req.Header.Set("Origin", r.host)
	req.Header.Set("Referer", r.host+"/")
	req.Header.Set("X-Hdrezka-Android-App", "1")
	req.Header.Set("X-Hdrezka-Android-App-Version", "2.2.5")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	if id != "" {
		if v, ok := r.baseReferer.Load(id); ok {
			if ref, ok := v.(string); ok && strings.HasPrefix(ref, "http") {
				req.Header.Set("Referer", ref)
			}
		}
	}
	// Force normalized auth cookies with extra tracking cookies (matches APK behavior).
	req.Header.Set("Cookie", r.cookieStringWithExtras())
}

func (r *pidorezkaChecker) httpClients() []*http.Client {
	if r.proxyClient == nil {
		return []*http.Client{r.client}
	}
	return []*http.Client{r.client, r.proxyClient}
}

func pidorezkaAccessDenied(status int, body string) bool {
	if status == http.StatusForbidden || status == http.StatusTooManyRequests {
		return true
	}
	lb := strings.ToLower(body)
	if strings.Contains(lb, `class="error-code"`) && strings.Contains(lb, "ошибка доступа") {
		return true
	}
	if strings.Contains(lb, "ошибка доступа (105)") {
		return true
	}
	return false
}

func (r *pidorezkaChecker) doSearch(req *http.Request, query string) (string, bool) {
	log.Debug().Str("query", query).Msg("pidorezka: doSearch start")

	// Primary: browser-based search (fully legitimate).
	if html, ok := r.browserSearch(req.Context(), query); ok {
		log.Debug().Int("htmlLen", len(html)).Msg("pidorezka: doSearch browser success")
		return html, true
	}
	log.Debug().Msg("pidorezka: doSearch browser failed, trying HTTP fallback")

	// Fallback: direct HTTP.
	searchURL := r.host + "/search/?do=search&subaction=search&q=" + url.QueryEscape(query)
	for i, client := range r.httpClients() {
		httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, searchURL, nil)
		if err != nil {
			log.Debug().Err(err).Msg("pidorezka: doSearch request create failed")
			return "", false
		}
		r.setPageHeaders(httpReq)

		resp, err := client.Do(httpReq)
		if err != nil {
			log.Debug().Err(err).Str("url", searchURL).Int("attempt", i+1).Msg("pidorezka: doSearch request failed")
			continue
		}

		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			log.Debug().Err(readErr).Int("attempt", i+1).Msg("pidorezka: doSearch read body failed")
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 && !pidorezkaAccessDenied(resp.StatusCode, string(body)) {
			log.Debug().Int("bodyLen", len(body)).Int("attempt", i+1).Msg("pidorezka: doSearch HTTP success")
			return string(body), true
		}

		if i == 0 && r.proxyClient != nil && pidorezkaAccessDenied(resp.StatusCode, string(body)) {
			log.Warn().Int("status", resp.StatusCode).Msg("pidorezka: doSearch blocked, retrying via proxy transport")
		}
	}

	return "", false
}

type pidorezkaSearchResult struct {
	exactHref string
	items     []pidorezkaSimilar
}

func (r *pidorezkaChecker) parseSearchResults(htmlBody, title, originalTitle, year string) pidorezkaSearchResult {
	if !strings.Contains(htmlBody, pidorezkaItemMarker) {
		return pidorezkaSearchResult{}
	}

	matches := pidorezkaLinkTitleRe.FindAllStringSubmatch(htmlBody, -1)
	if len(matches) == 0 {
		return pidorezkaSearchResult{}
	}

	wantTitle := normalizeSearchTitle(title)
	wantOriginal := normalizeSearchTitle(originalTitle)

	out := pidorezkaSearchResult{
		items: make([]pidorezkaSimilar, 0, len(matches)),
	}

	for _, m := range matches {
		if len(m) < 4 {
			continue
		}
		gotHref := strings.TrimSpace(m[1])
		gotName := strings.TrimSpace(m[2])
		gotYear := strings.TrimSpace(m[3])
		if gotHref == "" || gotName == "" {
			continue
		}

		// Find img near this match.
		img := ""
		idx := strings.Index(htmlBody, gotHref)
		if idx > 0 {
			block := htmlBody[max(0, idx-500):idx]
			img = submatch1(pidorezkaImgSearchRe, block)
		}

		out.items = append(out.items, pidorezkaSimilar{
			Title: gotName,
			Year:  gotYear,
			Href:  gotHref,
			Img:   img,
		})

		gotNorm := normalizeSearchTitle(gotName)
		if out.exactHref == "" && (gotNorm == wantTitle || gotNorm == wantOriginal) {
			if year == "" || gotYear == year {
				out.exactHref = gotHref
			}
		}
	}

	return out
}

func (r *pidorezkaChecker) fetchPage(req *http.Request, target string) (string, bool) {
	// Primary: browser-based fetch (fully legitimate).
	if html, ok := r.browserFetchPage(req.Context(), target); ok {
		return html, true
	}
	log.Debug().Str("url", target).Msg("pidorezka: browser fetchPage failed, trying HTTP fallback")

	// Fallback: direct HTTP.
	return r.fetchPageHTTP(req, target)
}

// fetchPageHTTP is the legacy HTTP-based page fetch fallback.
func (r *pidorezkaChecker) fetchPageHTTP(req *http.Request, target string) (string, bool) {
	for i, client := range r.httpClients() {
		httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, target, nil)
		if err != nil {
			return "", false
		}
		r.setPageHeaders(httpReq)

		resp, err := client.Do(httpReq)
		if err != nil {
			continue
		}

		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			continue
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 && !pidorezkaAccessDenied(resp.StatusCode, string(body)) {
			return string(body), true
		}

		if i == 0 && r.proxyClient != nil && pidorezkaAccessDenied(resp.StatusCode, string(body)) {
			log.Warn().Str("url", target).Int("status", resp.StatusCode).Msg("pidorezka: page blocked, retrying via proxy transport")
		}
	}

	return "", false
}

func (r *pidorezkaChecker) ajaxPost(req *http.Request, id, t, action, director, favs, s, e string) (string, bool) {
	href := ""
	if v, ok := r.baseReferer.Load(id); ok {
		href, _ = v.(string)
	}

	// Primary: browser-based AJAX (fully legitimate browser session).
	if result, ok := r.browserAjaxPost(req.Context(), id, t, action, director, favs, s, e, href); ok {
		return result, true
	}
	log.Debug().Str("id", id).Str("action", action).Msg("pidorezka: browser ajaxPost failed, trying HTTP fallback")

	// Fallback: direct HTTP (may be fingerprinted).
	return r.ajaxPostHTTP(req, id, t, action, director, favs, s, e)
}

// ajaxPostHTTP is the legacy HTTP-based AJAX fallback.
func (r *pidorezkaChecker) ajaxPostHTTP(req *http.Request, id, t, action, director, favs, s, e string) (string, bool) {
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
	target := r.host + "/ajax/get_cdn_series/?t=" + ts

	form := url.Values{}
	form.Set("id", id)
	form.Set("translator_id", t)
	form.Set("action", action)

	switch action {
	case "get_movie":
		form.Set("is_camrip", "0")
		form.Set("is_ads", "0")
		if director == "1" {
			form.Set("is_director", "1")
		} else {
			form.Set("is_director", "0")
		}
		if favs != "" {
			form.Set("favs", favs)
		}
	case "get_stream":
		if s != "" {
			form.Set("season", s)
		}
		if e != "" {
			form.Set("episode", e)
		}
		if favs != "" {
			form.Set("favs", favs)
		}
	case "get_episodes":
		// No extra fields needed.
	}

	for i, client := range r.httpClients() {
		httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodPost, target, strings.NewReader(form.Encode()))
		if err != nil {
			return "", false
		}
		httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.setAjaxHeaders(httpReq, id)

		resp, err := client.Do(httpReq)
		if err != nil {
			log.Debug().Err(err).Str("target", target).Int("attempt", i+1).Msg("pidorezka: ajaxPostHTTP request failed")
			continue
		}

		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			log.Debug().Err(readErr).Int("attempt", i+1).Msg("pidorezka: ajaxPostHTTP read body failed")
			continue
		}

		result := string(body)
		if resp.StatusCode >= 200 && resp.StatusCode < 300 && !pidorezkaAccessDenied(resp.StatusCode, result) && result != "" && result != "false" && result != `"false"` {
			log.Debug().Str("action", action).Str("id", id).Int("bodyLen", len(result)).Int("attempt", i+1).Msg("pidorezka: ajaxPostHTTP success")
			return result, true
		}

		if i == 0 && r.proxyClient != nil && pidorezkaAccessDenied(resp.StatusCode, result) {
			log.Warn().Int("status", resp.StatusCode).Str("action", action).Str("id", id).Msg("pidorezka: ajax blocked, retrying via proxy transport")
		}
	}

	return "", false
}

func (r *pidorezkaChecker) movieFromPage(req *http.Request, id, href, voice string) (string, string, bool) {
	target := strings.TrimSpace(voice)
	if target == "" {
		target = strings.TrimSpace(href)
	}
	if target == "" {
		return "", "", false
	}

	pageURL := target
	if !strings.HasPrefix(pageURL, "http") {
		pageURL = r.host + "/" + strings.TrimLeft(pageURL, "/")
	}

	htmlBody, ok := r.fetchPage(req, pageURL)
	log.Info().Str("pageURL", pageURL).Bool("ok", ok).Int("html_len", len(htmlBody)).Msg("DEBUG: movieFromPage fetchPage result")
	if !ok {
		return "", "", false
	}
	if id != "" {
		r.baseReferer.Store(id, pageURL)
	}

	stream := strings.TrimSpace(submatch1(pidorezkaStreamsRe, htmlBody))
	log.Info().Str("stream_raw", stream).Msg("DEBUG: movieFromPage stream match")
	if stream == "" || strings.EqualFold(stream, "false") {
		return "", "", false
	}
	stream = pidorezkaUnescapeJSONString(stream)
	if stream == "" {
		return "", "", false
	}

	subtitle := strings.TrimSpace(submatch1(pidorezkaSubtitleJSON, htmlBody))
	if subtitle != "" && !strings.EqualFold(subtitle, "false") {
		subtitle = pidorezkaUnescapeJSONString(subtitle)
	} else {
		subtitle = ""
	}

	return stream, subtitle, true
}

func pidorezkaUnescapeJSONString(v string) string {
	if v == "" {
		return ""
	}

	var out string
	if err := stdjson.Unmarshal([]byte(`"`+v+`"`), &out); err == nil {
		return out
	}
	if unquoted, err := strconv.Unquote(`"` + v + `"`); err == nil {
		return unquoted
	}

	v = strings.ReplaceAll(v, `\/`, `/`)
	v = strings.ReplaceAll(v, `\\`, `\`)
	v = strings.ReplaceAll(v, `\"`, `"`)
	return v
}

// ---------------------------------------------------------------------------
// Base64 decoding with trash pattern removal
// ---------------------------------------------------------------------------

func pidorezkaDecodeBase64(data string) string {
	if data == "" {
		return ""
	}

	// Sometimes Rezka returns raw unencoded streams directly (e.g., `[360p]https://...`).
	// If it looks like a qualities string or URL list, return it as-is.
	if strings.HasPrefix(data, "[") || strings.HasPrefix(data, "http") {
		return data
	}

	// Strip leading "#" + one char (e.g., "#h", "#2", etc.).
	if strings.HasPrefix(data, "#") && len(data) > 2 {
		data = data[2:]
	}

	// Primary: APK v2.2.5 algorithm — remove 21-char chunks at each //_// position.
	// The APK removes exactly 21 characters starting from the //_// marker
	// (5 chars "//_//" + 16 chars of trash), up to 20 iterations.
	if result := pidorezkaDecodeAPK(data); result != "" {
		return result
	}

	// Fallback: trashListBase pattern removal.
	cleaned := data
	for _, trash := range pidorezkaTrashBase {
		cleaned = strings.ReplaceAll(cleaned, "//_//"+trash, "")
	}
	cleaned = strings.ReplaceAll(cleaned, "//_//", "")

	decoded, err := base64.StdEncoding.DecodeString(cleaned)
	if err == nil && len(decoded) > 0 {
		return string(decoded)
	}

	// Fallback: regex cleanup + trashListBase.
	cleaned = pidorezkaTrashInnerRe.ReplaceAllString(data, "")
	cleaned = strings.ReplaceAll(cleaned, "//_//", "")
	for _, trash := range pidorezkaTrashBase {
		cleaned = strings.ReplaceAll(cleaned, trash, "")
	}

	decoded, err = base64.StdEncoding.DecodeString(cleaned)
	if err == nil && len(decoded) > 0 {
		return string(decoded)
	}

	// Fallback: trashListOld.
	cleaned = strings.ReplaceAll(data, "//_//", "")
	for _, trash := range pidorezkaTrashOld {
		cleaned = strings.ReplaceAll(cleaned, trash, "")
	}
	cleaned = pidorezkaTrashInnerRe.ReplaceAllString(cleaned, "")
	cleaned = strings.ReplaceAll(cleaned, "//_//", "")

	decoded, err = base64.StdEncoding.DecodeString(cleaned)
	if err == nil && len(decoded) > 0 {
		return string(decoded)
	}

	return ""
}

// pidorezkaDecodeAPK implements the exact decoding algorithm from HDRezka APK v2.2.5.
// It removes 21-character chunks starting at each "//_//" marker (up to 20 times),
// then base64-decodes the result.
func pidorezkaDecodeAPK(data string) string {
	cleaned := data
	for i := 0; i < 20; i++ {
		idx := strings.Index(cleaned, "//_//")
		if idx < 0 {
			break
		}
		end := idx + 21
		if end > len(cleaned) {
			end = len(cleaned)
		}
		cleaned = cleaned[:idx] + cleaned[end:]
	}

	decoded, err := base64.StdEncoding.DecodeString(cleaned)
	if err == nil && len(decoded) > 0 {
		return string(decoded)
	}
	// Try RawStdEncoding (no padding).
	decoded, err = base64.RawStdEncoding.DecodeString(cleaned)
	if err == nil && len(decoded) > 0 {
		return string(decoded)
	}
	return ""
}

// ---------------------------------------------------------------------------
// Quality extraction from decoded stream data
// ---------------------------------------------------------------------------

func (r *pidorezkaChecker) extractQualities(req *http.Request, decoded, plugin string, proxyLinks *proxylink.Manager, referer string) ([]map[string]any, map[string]string) {
	streams := make([]map[string]any, 0, 6)
	qualMap := make(map[string]string, 6)

	for _, q := range pidorezkaQualities {
		// Skip Ultra if not premium.
		if q.key == "1080p Ultra" && !r.premium {
			continue
		}

		rawURL := pidorezkaExtractQualityURL(decoded, q.key)
		if rawURL == "" && q.altKey != "" {
			rawURL = pidorezkaExtractQualityURL(decoded, q.altKey)
		}
		if rawURL == "" {
			continue
		}

		// Apply HLS suffix if needed.
		if r.hls && !strings.HasSuffix(rawURL, ".m3u8") {
			rawURL += ":hls:manifest.m3u8"
		}

		// Fix Voidboost CDN domain (stream.voidboost.cc → fiber./blitz./etc.)
		rawURL = fixVoidboostURL(rawURL)

		// Use direct /proxy/<urlencoded-url>?pl=rezka mode to avoid
		// cross-instance proxylink key mismatches behind load balancers.
		// Rezka-specific upstream headers are applied by proxy handler via plugin.
		proxied := streamProxyDirectURL(req, rawURL, plugin)

		streams = append(streams, map[string]any{
			"url":   proxied,
			"label": q.label,
		})
		qualMap[q.label] = proxied
	}

	// Some mirrors return a direct URL list without [quality] tags.
	if len(streams) == 0 {
		seen := map[string]struct{}{}
		rawURLs := pidorezkaExtractAnyURLs(decoded)
		for i, rawURL := range rawURLs {
			rawURL = strings.TrimSpace(rawURL)
			if rawURL == "" {
				continue
			}

			if r.hls && !strings.HasSuffix(rawURL, ".m3u8") {
				rawURL += ":hls:manifest.m3u8"
			}
			// Fix Voidboost CDN domain
			rawURL = fixVoidboostURL(rawURL)
			proxied := streamProxyDirectURL(req, rawURL, plugin)
			if _, ok := seen[proxied]; ok {
				continue
			}
			seen[proxied] = struct{}{}

			label := "auto"
			if i > 0 {
				label = fmt.Sprintf("alt-%d", i+1)
			}
			streams = append(streams, map[string]any{
				"url":   proxied,
				"label": label,
			})
			qualMap[label] = proxied
		}
	}

	return streams, qualMap
}

func pidorezkaPickUserAgent(incoming string) string {
	incoming = strings.TrimSpace(incoming)
	if incoming != "" && !strings.HasPrefix(incoming, "Go-http-client") {
		return incoming
	}
	if len(pidorezkaUserAgents) == 0 {
		return pidorezkaAndroidUA
	}
	return pidorezkaUserAgents[rand.Intn(len(pidorezkaUserAgents))]
}

func pidorezkaSecCHUA(ua string) string {
	major := "122"
	if m := regexp.MustCompile(`Chrome/([0-9]+)\.`).FindStringSubmatch(ua); len(m) == 2 && m[1] != "" {
		major = m[1]
	}
	return fmt.Sprintf(`"Not.A/Brand";v="99", "Chromium";v="%s", "Google Chrome";v="%s"`, major, major)
}

func (r *pidorezkaChecker) streamHeaders(req *http.Request, referer string) map[string]string {
	ua := ""
	if req != nil {
		ua = req.Header.Get("User-Agent")
	}
	ua = pidorezkaPickUserAgent(ua)
	referer = strings.TrimSpace(referer)
	if referer == "" {
		referer = r.host + "/"
	}
	origin := r.host
	if u, err := url.Parse(referer); err == nil && u.Scheme != "" && u.Host != "" {
		origin = u.Scheme + "://" + u.Host
	}
	return map[string]string{
		"accept":                        "*/*",
		"cache-control":                 "no-cache",
		"dnt":                           "1",
		"origin":                        origin,
		"pragma":                        "no-cache",
		"referer":                       referer,
		"sec-fetch-dest":                "empty",
		"sec-fetch-mode":                "cors",
		"sec-fetch-site":                "cross-site",
		"sec-ch-ua":                     pidorezkaSecCHUA(ua),
		"sec-ch-ua-mobile":              "?1",
		"sec-ch-ua-platform":            `"Android"`,
		"X-Hdrezka-Android-App":         "1",
		"X-Hdrezka-Android-App-Version": "2.2.5",
		"user-agent":                    ua,
	}
}

// pickPlayableRawURL probes HLS candidates and prefers one with a plausible
// full-length duration. This avoids selecting short intro/promo playlists.
func (r *pidorezkaChecker) pickPlayableRawURL(req *http.Request, decoded, referer string) string {
	candidates := pidorezkaExtractAnyURLs(decoded)
	if len(candidates) == 0 {
		return ""
	}
	if len(candidates) > 8 {
		candidates = candidates[:8]
	}

	fallback := ""
	bestURL := ""
	bestDur := 0.0

	for _, raw := range candidates {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if fallback == "" && !pidorezkaURLLooksLikePromo(raw) {
			fallback = raw
		}
		probeURL := fixVoidboostURL(raw)
		if !strings.Contains(strings.ToLower(probeURL), ".m3u8") {
			// Always try to probe .mp4 URLs as HLS — Rezka proxies them through :hls:
			probeURL += ":hls:manifest.m3u8"
		}

		dur, ok := r.hlsDurationSeconds(req, probeURL, referer)
		if !ok {
			continue
		}
		if dur > bestDur {
			bestDur = dur
			bestURL = raw
		}
	}

	if bestURL != "" {
		// 2+ minutes is enough to reject known Rezka short intro clips.
		if bestDur >= 120 || fallback == "" {
			return bestURL
		}
	}
	if fallback != "" {
		return fallback
	}
	return strings.TrimSpace(candidates[0])
}

// probeDecodedDuration extracts the first HLS URL from a decoded stream payload
// and probes its total duration in seconds. Returns 0 if no probe was possible.
func (r *pidorezkaChecker) probeDecodedDuration(req *http.Request, decoded, referer string) float64 {
	urls := pidorezkaExtractAnyURLs(decoded)
	for _, u := range urls {
		u = strings.TrimSpace(u)
		probeURL := fixVoidboostURL(u)
		if !strings.Contains(strings.ToLower(probeURL), ".m3u8") {
			// Always try to probe .mp4 URLs as HLS — Rezka proxies them through :hls:
			probeURL += ":hls:manifest.m3u8"
		}
		dur, ok := r.hlsDurationSeconds(req, probeURL, referer)
		if ok && dur > 0 {
			return dur
		}
	}
	return 0
}

func (r *pidorezkaChecker) hlsDurationSeconds(req *http.Request, rawURL, referer string) (float64, bool) {
	headers := r.streamHeaders(req, referer)
	ctx := context.Background()
	if req != nil {
		ctx = req.Context()
	}

	text, ok := r.fetchPlaylistText(ctx, rawURL, headers)
	if !ok {
		return 0, false
	}

	if variantURL := pidorezkaPickBestVariantURL(rawURL, text); variantURL != "" {
		text, ok = r.fetchPlaylistText(ctx, variantURL, headers)
		if !ok {
			return 0, false
		}
	}

	dur := pidorezkaPlaylistDuration(text)
	if dur <= 0 {
		return 0, false
	}
	return dur, true
}

func (r *pidorezkaChecker) fetchPlaylistText(ctx context.Context, target string, headers map[string]string) (string, bool) {
	for _, client := range r.httpClients() {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return "", false
		}
		for k, v := range headers {
			if strings.TrimSpace(v) == "" {
				continue
			}
			httpReq.Header.Set(k, v)
		}

		resp, err := client.Do(httpReq)
		if err != nil {
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
		_ = resp.Body.Close()
		if readErr != nil {
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			continue
		}

		text := string(body)
		if strings.Contains(text, "#EXTM3U") {
			return text, true
		}
	}
	return "", false
}

func pidorezkaPickBestVariantURL(baseURL, master string) string {
	lines := strings.Split(master, "\n")
	bestBW := -1
	bestURL := ""

	for i := range lines {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(strings.ToUpper(line), "#EXT-X-STREAM-INF:") {
			continue
		}

		bw := 0
		if m := pidorezkaBWRe.FindStringSubmatch(line); len(m) >= 2 {
			bw, _ = strconv.Atoi(strings.TrimSpace(m[1]))
		}

		uri := ""
		for j := i + 1; j < len(lines); j++ {
			next := strings.TrimSpace(lines[j])
			if next == "" {
				continue
			}
			if strings.HasPrefix(next, "#") {
				break
			}
			uri = next
			break
		}
		if uri == "" {
			continue
		}
		resolved := pidorezkaResolveRelativeURL(baseURL, uri)
		if resolved == "" {
			continue
		}

		if bw > bestBW {
			bestBW = bw
			bestURL = resolved
		}
	}

	return bestURL
}

func pidorezkaResolveRelativeURL(baseURL, uri string) string {
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return ""
	}
	if strings.HasPrefix(uri, "http://") || strings.HasPrefix(uri, "https://") {
		return uri
	}
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return ""
	}
	ref, err := url.Parse(uri)
	if err != nil {
		return ""
	}
	return base.ResolveReference(ref).String()
}

func pidorezkaPlaylistDuration(playlist string) float64 {
	matches := pidorezkaExtINFRe.FindAllStringSubmatch(playlist, -1)
	if len(matches) == 0 {
		return 0
	}
	total := 0.0
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(m[1]), 64)
		if err != nil {
			continue
		}
		total += v
	}
	return total
}

func pidorezkaExtractAnyURLs(decoded string) []string {
	if decoded == "" {
		return nil
	}
	matches := pidorezkaAnyURLRe.FindAllString(decoded, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]string, 0, len(matches))
	nonPromo := make([]string, 0, len(matches))
	seen := map[string]struct{}{}
	for _, m := range matches {
		m = strings.TrimSpace(m)
		m = strings.TrimRight(m, ")]}")
		if m == "" {
			continue
		}
		if _, ok := seen[m]; ok {
			continue
		}
		seen[m] = struct{}{}
		out = append(out, m)
		if !pidorezkaURLLooksLikePromo(m) {
			nonPromo = append(nonPromo, m)
		}
	}
	if len(nonPromo) > 0 {
		return nonPromo
	}
	return out
}

func pidorezkaCountQualities(decoded string) int {
	if decoded == "" {
		return 0
	}
	seen := map[string]struct{}{}
	for _, q := range []string{"2160p", "4K", "1440p", "2K", "1080p Ultra", "1080p", "720p", "480p", "360p"} {
		if u := pidorezkaExtractQualityURL(decoded, q); u != "" {
			seen[u] = struct{}{}
		}
	}
	if len(seen) > 0 {
		return len(seen)
	}
	return len(pidorezkaExtractAnyURLs(decoded))
}

func pidorezkaLooksLikePromo(decoded string) bool {
	s := strings.ToLower(strings.TrimSpace(decoded))
	if s == "" {
		return false
	}
	for _, marker := range []string{
		"trailer", "teaser", "promo", "preroll", "advert",
		"/trailers/", "/trailer/", "тизер", "трейлер", "реклама",
		"/splash/", "/intro/", "/greeting/", "заставка",
		"/preview/", "anons",
	} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

func pidorezkaExtractQualityURL(decoded, quality string) string {
	// Pattern: [quality]url or [prefix quality suffix]url
	re := regexp.MustCompile(`\[(` + regexp.QuoteMeta(quality) + `|[^\]]+` + regexp.QuoteMeta(quality) + `[^\]]*)\]([^,\[]+)`)
	matches := re.FindAllStringSubmatch(decoded, -1)
	if len(matches) == 0 {
		return ""
	}

	for _, m := range matches {
		if len(m) < 3 {
			continue
		}
		urlLine := strings.TrimSpace(m[2])
		if urlLine == "" {
			continue
		}

		if best := pidorezkaPickBestStreamURL(pidorezkaAnyURLRe.FindAllString(urlLine, -1)); best != "" {
			return best
		}

		// Fallback to legacy behavior.
		urlMatch := pidorezkaStreamURLRe.FindStringSubmatch(urlLine)
		if len(urlMatch) >= 2 {
			u := strings.TrimSpace(urlMatch[1])
			if u != "" {
				return u
			}
		}
	}
	return ""
}

func pidorezkaPickBestStreamURL(urls []string) string {
	if len(urls) == 0 {
		return ""
	}

	best := ""
	bestScore := -1 << 30
	seen := make(map[string]struct{}, len(urls))

	for i, u := range urls {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		u = strings.TrimRight(u, ")]}")
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}

		lu := strings.ToLower(u)
		score := 0
		if strings.Contains(lu, ".m3u8") {
			score += 10
		}
		if strings.Contains(lu, ".mp4") {
			score += 6
		}
		if strings.Contains(lu, "token=") || strings.Contains(lu, "expires=") || strings.Contains(lu, "validto=") {
			score += 3
		}
		if pidorezkaURLLooksLikePromo(lu) {
			score -= 100
		}

		// Prefer later URL on equal score — reserve mirrors are often the last one.
		if score > bestScore || (score == bestScore && i > 0) {
			bestScore = score
			best = u
		}
	}
	return best
}

func pidorezkaURLLooksLikePromo(u string) bool {
	lu := strings.ToLower(strings.TrimSpace(u))
	if lu == "" {
		return false
	}
	for _, marker := range []string{
		"trailer", "teaser", "promo", "preroll", "advert",
		"/trailers/", "/trailer/", "/promo/", "/teaser/",
		"тизер", "трейлер", "реклама", "anons",
		"/splash/", "/intro/", "/greeting/", "заставка",
		"/preview/",
	} {
		if strings.Contains(lu, marker) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Subtitle parsing
// ---------------------------------------------------------------------------

func pidorezkaParseSubtitles(subtitle string) []map[string]any {
	matches := pidorezkaSubtitleRe.FindAllStringSubmatch(subtitle, -1)
	if len(matches) == 0 {
		return nil
	}

	subs := make([]map[string]any, 0, len(matches))
	for _, m := range matches {
		if len(m) < 3 {
			continue
		}
		subs = append(subs, map[string]any{
			"label": strings.TrimSpace(m[1]),
			"url":   strings.TrimSpace(m[2]),
		})
	}
	return subs
}

// ---------------------------------------------------------------------------
// Similar results
// ---------------------------------------------------------------------------

func (r *pidorezkaChecker) writeSimilar(
	w http.ResponseWriter,
	req *http.Request,
	rjson bool,
	plugin string,
	items []pidorezkaSimilar,
	title, originalTitle, year string,
) {
	host := hostFromRequest(req)
	data := make([]map[string]any, 0, len(items))
	labels := make([]string, 0, len(items))

	for _, item := range items {
		link := fmt.Sprintf("%s/lite/%s?href=%s&rjson=%s&title=%s&original_title=%s&year=%s",
			host, plugin,
			url.QueryEscape(item.Href),
			getsTVBool(rjson),
			url.QueryEscape(title),
			url.QueryEscape(originalTitle),
			url.QueryEscape(year),
		)
		row := map[string]any{
			"method":  "link",
			"url":     link,
			"similar": true,
			"year":    item.Year,
			"details": "",
			"title":   item.Title,
			"img":     item.Img,
		}
		data = append(data, row)
		labels = append(labels, item.Title)
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
