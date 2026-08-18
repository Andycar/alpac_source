package litesrc

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"lampac-go/internal/browsergate"
	"lampac-go/internal/httpclient"
	"maps"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/browsertmp"

	stdjson "encoding/json"
	"lampac-go/internal/browser"

	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/rs/zerolog/log"
)

// mirageStealthJS is a self-contained anti-detection script injected before any
// page JavaScript runs. It patches the most common fingerprints that headless
// Chrome detection systems check. Derived from playwright_stealth / puppeteer-extra-plugin-stealth.
const mirageStealthJS = `(function(){
// 1. navigator.webdriver → false
Object.defineProperty(Object.getPrototypeOf(navigator),'webdriver',{
  set:undefined,enumerable:true,configurable:true,
  get:new Proxy(Object.getOwnPropertyDescriptor(Object.getPrototypeOf(navigator),'webdriver').get,{
    apply:(t,a,r)=>{Reflect.apply(t,a,r);return false}
  })
});
// 2. window.chrome stub
if(!window.chrome){Object.defineProperty(window,'chrome',{writable:true,enumerable:true,configurable:false,value:{}})}
if(!window.chrome.runtime){window.chrome.runtime={id:undefined,connect:null,sendMessage:null}}
if(!window.chrome.app){window.chrome.app={isInstalled:false,getDetails:function(){return null},getIsInstalled:function(){return false},runningState:function(){return'cannot_run'}}}
// 3. navigator.languages
Object.defineProperty(Object.getPrototypeOf(navigator),'languages',{get:()=>['ru-RU','ru','en-US','en']});
// 4. navigator.vendor
Object.defineProperty(Object.getPrototypeOf(navigator),'vendor',{get:()=>'Google Inc.'});
// 5. navigator.plugins (length > 0)
if(navigator.plugins.length===0){
  Object.defineProperty(Object.getPrototypeOf(navigator),'plugins',{get:()=>{
    const p={length:3,item:function(i){return this[i]||null},namedItem:function(n){return null},refresh:function(){}};
    p[0]={name:'Chrome PDF Plugin',filename:'internal-pdf-viewer',description:'Portable Document Format',length:1};
    p[1]={name:'Chrome PDF Viewer',filename:'mhjfbmdgcfjbbpaeojofohoefgiehjai',description:'',length:1};
    p[2]={name:'Native Client',filename:'internal-nacl-plugin',description:'',length:2};
    return p;
  }});
}
// 6. navigator.permissions.query patch
try{
  const origQuery=window.navigator.permissions.query;
  window.navigator.permissions.query=function(p){
    if(p&&p.name==='notifications'){return Promise.resolve({state:Notification.permission})}
    return origQuery.call(this,p);
  };
}catch(e){}
// 7. WebGL vendor/renderer
try{
  const gp=WebGLRenderingContext.prototype.getParameter;
  WebGLRenderingContext.prototype.getParameter=function(p){
    if(p===37445)return'Intel Inc.';if(p===37446)return'Intel Iris OpenGL Engine';return gp.call(this,p);
  };
  if(typeof WebGL2RenderingContext!=='undefined'){
    const gp2=WebGL2RenderingContext.prototype.getParameter;
    WebGL2RenderingContext.prototype.getParameter=function(p){
      if(p===37445)return'Intel Inc.';if(p===37446)return'Intel Iris OpenGL Engine';return gp2.call(this,p);
    };
  }
}catch(e){}
// 8. window.outerWidth/outerHeight
try{if(window.outerWidth&&window.outerHeight){window.outerWidth=window.innerWidth;window.outerHeight=window.innerHeight+85}}catch(e){}
// 9. navigator.hardwareConcurrency
Object.defineProperty(Object.getPrototypeOf(navigator),'hardwareConcurrency',{get:()=>8});
// 10. navigator.platform
Object.defineProperty(Object.getPrototypeOf(navigator),'platform',{get:()=>'Linux x86_64'});
// 11. Media codecs — ensure common codecs report correctly
try{
  const origCanPlay=HTMLMediaElement.prototype.canPlayType;
  HTMLMediaElement.prototype.canPlayType=function(t){
    if(t&&t.includes('video/mp4')&&t.includes('avc1'))return'probably';
    if(t==='audio/aac')return'probably';
    return origCanPlay.call(this,t);
  };
}catch(e){}
})();`

// mirageBrowserPool manages a shared headless Chrome allocator.
type mirageBrowserPool struct {
	once        sync.Once
	allocCtx    context.Context
	userDataDir string
	socksProxy  string // SOCKS5 proxy for Chrome (e.g. "127.0.0.1:40000")
	mu          sync.Mutex

	// masterCtx is a long-lived browser context that keeps Chrome alive.
	// Created once via ensureMaster(). All resolves create tabs from masterCtx
	// via NewContext(masterCtx), avoiding SingletonLock conflicts.
	masterCtx    context.Context
	masterCancel context.CancelFunc
	masterOnce   sync.Once
}

var mirageBrowser mirageBrowserPool

var moviesIDRe = regexp.MustCompile(`/movies/\d+`)

// kinogoSiteReferer is the parent page URL the CDN expects (Phantom in
// lampac-nextgen uses a bare kinogo-go.tv referer, no specific film path).
const kinogoSiteReferer = "https://kinogo-go.tv/"

// ensureMaster starts a persistent Chrome process with an about:blank tab.
// This process stays alive for the entire server lifetime. All resolves
// create tabs from masterCtx, avoiding SingletonLock conflicts.
//
// Startup is wrapped in a 10s timeout so a completely dead Chrome process
// can't make us hang indefinitely waiting for CDP to respond. Zombie-master
// detection (where ctx.Err()==nil but Chrome no longer responds to CDP) is
// handled reactively in the caller: when chromedp.Run in resolveViaBrowser
// fails with "deadline exceeded", the caller invalidates the master and
// the next resolve recreates it here.
func (bp *mirageBrowserPool) ensureMaster() {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	if bp.masterCtx != nil && bp.masterCtx.Err() == nil {
		return // already alive
	}

	// Clean up any prior cancelled master (ctx.Err() != nil case).
	if bp.masterCancel != nil {
		bp.masterCancel()
	}
	bp.masterCtx = nil
	bp.masterCancel = nil

	ctx, cancel := chromedp.NewContext(bp.allocCtx,
		chromedp.WithLogf(func(format string, args ...any) {
			log.Debug().Msgf("[chromedp-master] "+format, args...)
		}),
	)
	// Wrap Navigate in a 10s timeout so we don't hang forever on a
	// totally dead Chrome process. Normal startup takes ~1-2s.
	startCtx, startCancel := context.WithTimeout(ctx, 10*time.Second)
	err := chromedp.Run(startCtx, chromedp.Navigate("about:blank"))
	startCancel()
	if err != nil {
		log.Error().Err(err).Msg("mirage: failed to start master Chrome process")
		cancel()
		return
	}
	bp.masterCtx = ctx
	bp.masterCancel = cancel
	log.Info().Msg("mirage: master Chrome process started (persistent about:blank tab)")
}

// mirageMasterBrowserCtx returns the master Chrome browser context.
// Used by fetchSegmentViaBrowser to create fresh tabs for segment fetching.
func mirageMasterBrowserCtx() context.Context {
	mirageBrowser.mu.Lock()
	defer mirageBrowser.mu.Unlock()
	return mirageBrowser.masterCtx
}

func (bp *mirageBrowserPool) ensureDataDir() {
	if bp.userDataDir == "" {
		return
	}
	// Re-create the directory if it was removed (e.g., /tmp cleanup)
	if _, err := os.Stat(bp.userDataDir); os.IsNotExist(err) {
		log.Warn().Str("dir", bp.userDataDir).Msg("mirage: chrome data dir missing, recreating")
		_ = os.MkdirAll(bp.userDataDir, 0700)
		return
	}
	// Clean stale singleton lock/socket files that prevent Chrome from starting
	for _, name := range []string{"SingletonLock", "SingletonSocket", "SingletonCookie"} {
		p := filepath.Join(bp.userDataDir, name)
		if _, err := os.Stat(p); err == nil {
			_ = os.Remove(p)
		}
	}
}

func (bp *mirageBrowserPool) reinit() {
	bp.mu.Lock()
	defer bp.mu.Unlock()

	// Reuse the same fixed directory — don't create new temp dirs on each reinit
	userDataDir := bp.userDataDir
	if userDataDir == "" {
		home, _ := os.UserHomeDir()
		userDataDir = filepath.Join(home, ".cache", "lampac", "mirage-chrome")
		if home == "" {
			userDataDir = "/tmp/mirage-chrome"
		}
	}
	// Clean stale singleton lock files that prevent Chrome from starting
	for _, name := range []string{"SingletonLock", "SingletonCookie", "SingletonSocket"} {
		_ = os.Remove(filepath.Join(userDataDir, name))
	}
	_ = os.MkdirAll(userDataDir, 0700)
	bp.userDataDir = userDataDir
	log.Info().Str("userDataDir", userDataDir).Msg("mirage: chrome dirs (reinit)")
	bp.allocCtx, _ = chromedp.NewExecAllocator(context.Background(), bp.chromeOpts(userDataDir)...)
}

// chromeOptsForDir builds chromedp allocator options for a per-resolve Chrome
// process. Takes the user-data directory and SOCKS5 proxy address as
// parameters rather than reading from bp state — this keeps the new
// Spectre-style per-resolve flow decoupled from the legacy shared-Chrome
// state used by alloha_browser.go.
func (bp *mirageBrowserPool) chromeOptsForDir(userDataDir, socksProxy string) []chromedp.ExecAllocatorOption {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-sync", true),
		chromedp.Flag("disable-translate", true),
		chromedp.Flag("mute-audio", true),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("disable-default-apps", true),
		chromedp.Flag("disable-popup-blocking", true),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.UserDataDir(userDataDir),
		// Override UA to remove "HeadlessChrome" — headless Chrome v146 sends
		// "HeadlessChrome/146.0.0.0" by default, which CDN instantly rejects.
		// We match the real version (146) so sec-ch-ua (auto-generated by
		// Chrome from its binary version) stays in sync: both say v=146.
		chromedp.UserAgent("Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"),
		chromedp.ModifyCmdFunc(func(cmd *exec.Cmd) {
			cmd.Env = browser.ChromeProcessEnv(cmd.Env, userDataDir)
		}),
	)
	if socksProxy != "" {
		opts = append(opts, chromedp.ProxyServer("socks5://"+socksProxy))
	}
	// No proxy: Chrome goes direct. linkHost is reachable from the lampac
	// server without SOCKS5/V2Box, so no fallback proxy is needed.
	return opts
}

func (bp *mirageBrowserPool) chromeOpts(userDataDir string) []chromedp.ExecAllocatorOption {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-sync", true),
		chromedp.Flag("disable-translate", true),
		chromedp.Flag("mute-audio", true),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("disable-default-apps", true),
		chromedp.Flag("disable-popup-blocking", true),
		// Anti-detection: remove navigator.webdriver=true at Chrome level
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		// Isolated profile directory to avoid socket conflicts
		chromedp.UserDataDir(userDataDir),
		// Override UA to remove "HeadlessChrome" — see chromeOptsForDir.
		chromedp.UserAgent("Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"),
		// Set XDG_RUNTIME_DIR to userDataDir so Chrome can create its
		// singleton socket there instead of /run/user/<uid> which may not exist.
		chromedp.ModifyCmdFunc(func(cmd *exec.Cmd) {
			cmd.Env = browser.ChromeProcessEnv(cmd.Env, userDataDir)
			log.Debug().Int("envLen", len(cmd.Env)).Str("dataDir", userDataDir).Msg("mirage: chrome env set private tmp")
		}),
	)
	// Route Chrome traffic through proxy so browser and CDN
	// see the same IP. Without this, /movies/ resolves on one IP and
	// CDN segments fail with 403 when fetched from a different IP.
	if bp.socksProxy != "" {
		opts = append(opts, chromedp.ProxyServer("socks5://"+bp.socksProxy))
		log.Info().Str("socks", bp.socksProxy).Msg("mirage: chrome using SOCKS5 proxy")
	}
	// No proxy: Chrome goes direct. linkHost reaches CDN from the lampac
	// server's IP without any tunnel, so no fallback proxy is needed.
	return opts
}

func (bp *mirageBrowserPool) init() {
	bp.once.Do(func() {
		// Use home-based directory — snap Chromium can't write to /tmp due to
		// AppArmor confinement (process_singleton_posix.cc "Failed to create socket directory").
		home, _ := os.UserHomeDir()
		userDataDir := filepath.Join(home, ".cache", "lampac", "mirage-chrome")
		if home == "" {
			userDataDir = "/tmp/mirage-chrome" // fallback for non-snap environments
		}
		// Clean stale singleton locks from previous crashed sessions
		for _, name := range []string{"SingletonLock", "SingletonCookie", "SingletonSocket"} {
			_ = os.Remove(filepath.Join(userDataDir, name))
		}
		_ = os.MkdirAll(userDataDir, 0700)
		bp.userDataDir = userDataDir
		log.Info().Str("userDataDir", userDataDir).Msg("mirage: chrome dirs")
		bp.allocCtx, _ = chromedp.NewExecAllocator(context.Background(), bp.chromeOpts(userDataDir)...)
	})
}

// mirageFetchPlayerHTML fetches the stloadi.live player page HTML using Go's
// HTTP client with proper headers (Referer, Sec-Fetch-*, etc).
// The CDN blocks requests from headless Chrome but allows normal HTTP requests
// with the correct headers — so we fetch the HTML ourselves and feed it to Chrome.
func mirageFetchPlayerHTML(ctx context.Context, linkHost, token, tokenMovie, balancerName string) (string, string, error) {
	base := strings.TrimRight(linkHost, "/")
	pageURL := base + "/?token_movie=" + url.QueryEscape(tokenMovie) + "&token=" + url.QueryEscape(strings.TrimSpace(token))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Referer", "https://kinogo-go.tv/")
	req.Header.Set("Sec-Fetch-Dest", "iframe")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Upgrade-Insecure-Requests", "1")

	if balancerName == "" {
		balancerName = "mirage"
	}
	// Direct uTLS fetch — bypass flaky SOCKS5 sidecar (CDN-only, no geo-restriction
	// on player HTML).
	_ = balancerName
	client := httpclient.NewUTLS(12 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("fetch player HTML: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("fetch player HTML: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 6<<20))
	if err != nil {
		return "", "", fmt.Errorf("read player HTML: %w", err)
	}

	return string(body), pageURL, nil
}

// mirageProxyMoviesRequest intercepts a POST /movies/ request from the browser,
// rewrites the file ID, and executes it via Go HTTP client to bypass CDN
// headless browser detection. Returns the response body and status code.
func mirageProxyMoviesRequest(ctx context.Context, reqURL, method string, postEntries []*network.PostDataEntry, headers network.Headers, idFile int64, pageURL, balancerName string) ([]byte, int, error) {
	// Rewrite the URL to use the correct file ID
	idFileStr := strconv.FormatInt(idFile, 10)
	targetURL := moviesIDRe.ReplaceAllString(reqURL, "/movies/"+idFileStr)
	log.Info().Str("original", reqURL).Str("target", targetURL).Int64("idFile", idFile).Msg("mirage: proxying /movies/ request via Go HTTP")

	// Reconstruct POST body from PostDataEntries (each entry has base64-encoded Bytes)
	var bodyReader io.Reader
	if len(postEntries) > 0 {
		var combined []byte
		for _, entry := range postEntries {
			if entry.Bytes != "" {
				decoded, err := base64.StdEncoding.DecodeString(entry.Bytes)
				if err == nil {
					combined = append(combined, decoded...)
				}
			}
		}
		if len(combined) > 0 {
			bodyReader = bytes.NewReader(combined)
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, targetURL, bodyReader)
	if err != nil {
		return nil, 0, err
	}

	// Copy headers from the browser request (includes the JS-generated Borth auth token)
	for k, v := range headers {
		val := fmt.Sprint(v)
		// Skip headers that Go HTTP client sets automatically
		switch strings.ToLower(k) {
		case "host", "content-length":
			continue
		case "user-agent":
			// Use our own non-headless UA
			req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
			continue
		case "sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform":
			// Skip Client Hints that reveal headless Chrome
			continue
		}
		req.Header.Set(k, val)
	}

	// Ensure proper Referer
	if req.Header.Get("Referer") == "" {
		req.Header.Set("Referer", pageURL)
	}

	if balancerName == "" {
		balancerName = "mirage"
	}
	_ = balancerName
	// Direct uTLS — SOCKS5 sidecar (:40000/:40007) has been unstable in production.
	// linkHost is not geo-restricted, direct from the lampac server reaches CDN OK.
	client := httpclient.NewUTLS(15 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("proxy /movies/: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read /movies/ body: %w", err)
	}

	log.Info().Int("status", resp.StatusCode).Int("bodyLen", len(body)).Msg("mirage: /movies/ proxy response")
	return body, resp.StatusCode, nil
}

// mirageResolveViaBrowser uses headless Chrome to execute the stloadi.live player
// JS and intercept the POST /movies/{id_file} request it generates.
//
// Approach:
//  1. Fetch the player page HTML via Go HTTP client (CDN blocks headless Chrome)
//  2. Strip the iframe check so the page works outside an iframe
//  3. Navigate Chrome to the stloadi.live origin, intercept the Document request
//     via Fetch domain, and fulfill it with our pre-fetched HTML
//  4. The JS loads on the correct origin, reads fileList, generates auth headers,
//     and fires POST /movies/{id}
//  5. We intercept the POST via Fetch, proxy it through Go HTTP client (to bypass
//     headless Chrome detection), rewrite the file ID, and return the response
//  6. Extract hlsSource / m3u8 URL from the response
//
// mirageBrowserSem is a dynamic semaphore for limiting concurrent chromedp sessions.
// The capacity can be changed at runtime via SetMirageBrowserLimit().
//
// Wraps a per-pool dynSemaphore and the process-wide browserGate (shared
// mode). When fancdn-register takes the gate Exclusive, new Acquire calls
// here block until it releases — this gives the reCAPTCHA v3 subprocess
// a contention-free Chrome environment.
var mirageBrowserSem = browsergate.NewChromeSem(8, browsergate.Global)

// mirageExperimentKeepBrowser controls the "keep browser alive" experiment.
// When true, the browser's own WebSocket is NOT blocked and the browser context
// is kept alive after resolve. This tested whether the CDN binds the stream URL
// to the browser's native WS session.
//
// DISABLED (2026-04-15): the Spectre reference implementation proves the CDN
// does NOT need the browser WS — our Go WS client handles edge_hash perfectly.
// Keeping the browser alive was leaking tabs and memory: every resolve created
// a new chromedp.NewContext(masterCtx) without firing its cancel, accumulating
// open tabs in the master Chrome. Eventually Chrome OOM'd or wedged and all
// subsequent resolves failed with "chromedp enable failed: context canceled"
// because the master's underlying Chrome process was gone. We also observed
// "master Chrome not available" firing immediately after "master Chrome
// process started" — same root cause, Chrome exited between creation and use.
//
// Reverting to false means: one Chrome process per resolve, deferred cancel
// cleans up the tab on return, the Go WS client is the only session talking
// to the CDN's edge_hash stream. This matches how Spectre works.
const mirageExperimentKeepBrowser = false

// mirageResolveResult holds everything extracted from a browser resolve session.
type mirageResolveResult struct {
	RawBody    []byte            // raw /movies/ JSON response
	Stream     string            // best quality stream URL
	CDNHeaders map[string]string // JS-generated auth headers (Borth, Authorization, etc.)
	WSUrl      string            // WebSocket URL for edge_hash updates (wss://…/ws/)
	SID        string            // WebSocket session ID

	// Origin/Referer captured from the browser's m3u8 request — Spectre uses
	// these verbatim on every CDN proxy request (Service.cs:137,141). Hardcoding
	// our linkHost as Origin makes the CDN reject fresh URLs.
	RequestOrigin  string
	RequestReferer string

	// For lightweight refresh: stored POST parameters to re-call /movies/ API
	// directly without browser, getting fresh CDN URLs instantly.
	MoviesURL     string            // POST URL (e.g. https://linkHost/bnsi/movies/123)
	MoviesHeaders map[string]string // all request headers from browser POST
	MoviesBody    []byte            // POST body

	// Experiment: when mirageExperimentKeepBrowser is true, this cancel func
	// keeps the browser context alive. Caller stores it on mirageStreamEntry.
	// Call to clean up when entry is evicted.
	BrowserCancel context.CancelFunc
	BrowserCtx    context.Context // chromedp browser context for segment fetch fallback
}

// mirageResolveViaBrowser resolves video via headless Chrome.
// Returns the resolve result and ok flag. Result contains rawJSON, stream URL, CDN headers,
// and WS parameters (wsUrl/sid) for maintaining live edge_hash updates.
func mirageResolveViaBrowser(ctx context.Context, linkHost, token string, idFile int64, tokenMovie, balancerName string) (*mirageResolveResult, bool) {
	// Limit concurrent browser sessions to control memory usage (~50-200MB each)
	if !mirageBrowserSem.Acquire(ctx) {
		return nil, false
	}
	defer mirageBrowserSem.Release()
	log.Info().Int64("id_file", idFile).Str("token_movie", tokenMovie).Str("balancer", balancerName).Msg("mirage: resolving video via browser")

	// Step 1: Fetch the player HTML with Go's HTTP client.
	htmlBody, pageURL, err := mirageFetchPlayerHTML(ctx, linkHost, token, tokenMovie, balancerName)
	if err != nil {
		log.Warn().Err(err).Str("balancer", balancerName).Msg("mirage: failed to fetch player HTML")
		return nil, false
	}

	if !strings.Contains(htmlBody, "fileList") {
		log.Warn().Int("htmlLen", len(htmlBody)).Str("balancer", balancerName).Msg("mirage: player HTML has no fileList")
		return nil, false
	}

	// Step 2: Strip the iframe check so player JS runs outside an iframe.
	htmlBody = mirageStripIframeCheck(htmlBody)

	// Step 2b: Patch id_file in the fileList JSON so the player JS sends
	// POST /movies/{our_idFile} instead of the default one in HTML.
	// CDN returns HTML with the "active" file's id, which may differ from
	// the one the user selected (different voice/quality). Spectre handles
	// this by patching the POST URL in the route handler (Controller.cs:599),
	// which we also do (ContinueRequest.WithURL), but the player JS reads
	// id_file from fileList to build the URL — so we must patch HTML too.
	//
	// Three cases:
	//  1. "id_file":null       — CDN hides the active id (newer behavior)
	//  2. "id_file":1234567    — number without quotes
	//  3. "id_file":"1234567"  — string with quotes
	idFileStr := strconv.FormatInt(idFile, 10)
	idFilePatchRe := regexp.MustCompile(`"id_file"\s*:\s*(?:null|"?\d+"?)`)
	htmlBody = idFilePatchRe.ReplaceAllString(htmlBody, `"id_file":"`+idFileStr+`"`)

	log.Info().Int("htmlLen", len(htmlBody)).Int64("patchedIdFile", idFile).Str("balancer", balancerName).Msg("mirage: fetched and patched player HTML")

	// Direct approach: navigate to CDN linkhost, intercept the Document request
	// via Fetch domain, and fulfill it with our pre-fetched HTML (bypasses headless
	// detection). JS/CSS load from CDN via Chrome's ContinueRequest. JS executes
	// on the correct origin, reads fileList, generates auth headers, fires POST /movies/.
	var navURL string
	var htmlB64 string

	navURL = pageURL
	htmlB64 = base64.StdEncoding.EncodeToString([]byte(htmlBody))
	log.Info().Str("navURL", navURL).Msg("mirage: using direct navigation")

	// SOCKS5 lookup intentionally skipped: in production the registered
	// sidecars (xray VLESS / mihomo) for "mirage" balancer were unstable,
	// and linkHost is reachable directly from the lampac server. Chrome
	// goes direct; if you need to re-enable SOCKS5 routing for geo-locked
	// CDN nodes, restore the SocksAddrForBalancer/SocksAddrForLabel calls
	// here and at mirageFetchPlayerHTML / mirageProxyMoviesRequest.
	socksForBrowser := ""

	// --- Spectre-style per-resolve Chrome ---
	//
	// The C# reference does `using (var browser = new PlaywrightBrowser())`
	// and disposes it when the resolve completes. We do the same: fresh
	// allocator, fresh Chrome process, unique temp user-data dir, all
	// cancelled on return. Previously we kept a "master" Chrome alive
	// across resolves for performance, but it wedged after ~15min (CDP
	// debugger hang / OOM), and reusing it caused "context canceled"
	// races between concurrent resolves. Trading ~3s of Chrome startup
	// per play for reliability — exactly Spectre's tradeoff.
	tmpDir, tmpErr := browsertmp.New("mirage-chrome-")
	if tmpErr != nil {
		log.Error().Err(tmpErr).Msg("mirage: failed to create temp chrome data dir")
		return nil, false
	}
	// Removed off the request path: a just-cancelled Chrome keeps writing to its
	// profile for a moment and re-creates what RemoveAll deleted, which is how
	// /tmp filled up with orphaned *-chrome-* dirs (see internal/browsertmp).
	defer func() { go browsertmp.Remove(tmpDir) }()

	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(),
		mirageBrowser.chromeOptsForDir(tmpDir, socksForBrowser)...)
	defer allocCancel()

	browserCtx, cancel := chromedp.NewContext(allocCtx,
		chromedp.WithLogf(func(format string, args ...any) {
			log.Debug().Msgf("[chromedp] "+format, args...)
		}),
		chromedp.WithErrorf(func(format string, args ...any) {
			log.Error().Msgf("[chromedp-err] "+format, args...)
		}),
	)
	defer cancel()

	timeoutCtx, timeoutCancel := context.WithTimeout(browserCtx, 20*time.Second)
	defer timeoutCancel()

	go func() {
		select {
		case <-ctx.Done():
			if !mirageExperimentKeepBrowser {
				cancel()
			}
		case <-timeoutCtx.Done():
		}
	}()

	parsedURL, parseErr := url.Parse(pageURL)
	if parseErr != nil {
		log.Error().Err(parseErr).Msg("mirage: failed to parse pageURL")
		return nil, false
	}
	pageHost := parsedURL.Hostname()

	var (
		resultStream  string
		resultBody    []byte            // raw /movies/ JSON response for quality extraction
		resultHeaders map[string]string // JS-generated CDN auth headers
		resultMu      sync.Mutex
		done          = make(chan struct{}, 1)
		allHeadersCh  = make(chan struct{}, 1) // signaled when stream auth headers (Authorizations, Accepts-Controls) captured
		docFulfilled  int32

		// For lightweight refresh: capture /movies/ POST parameters
		capturedMoviesURL     string
		capturedMoviesHeaders map[string]string
		capturedMoviesBody    []byte

		// Origin/Referer from browser m3u8 request — Spectre uses these on
		// every CDN request (Service.cs:137,141). Hardcoded linkHost gets
		// 403 on fresh URLs.
		capturedRequestOrigin  string
		capturedRequestReferer string
	)

	signalDone := func(stream string, rawBody []byte, hdrs map[string]string) {
		resultMu.Lock()
		defer resultMu.Unlock()
		if resultStream == "" && stream != "" {
			resultStream = stream
			if len(rawBody) > 0 {
				resultBody = rawBody
			}
			select {
			case done <- struct{}{}:
			default:
			}
		}
		if len(hdrs) > 0 {
			if resultHeaders == nil {
				resultHeaders = make(map[string]string)
			}
			maps.Copy(resultHeaders, hdrs)
			// Signal when we have stream auth headers beyond just Borth.
			for k := range resultHeaders {
				kl := strings.ToLower(k)
				if kl == "authorizations" || strings.Contains(kl, "accepts-controls") {
					select {
					case allHeadersCh <- struct{}{}:
					default:
					}
					return
				}
			}
		}
	}

	// When keeping browser alive, use browserCtx for the listener so it
	// continues receiving WS frames after the 20s resolve timeout expires.
	listenCtx := timeoutCtx
	if mirageExperimentKeepBrowser {
		listenCtx = browserCtx
	}
	chromedp.ListenTarget(listenCtx, func(ev any) {
		switch e := ev.(type) {
		case *network.EventRequestWillBeSent:
			log.Debug().Str("method", e.Request.Method).Str("url", e.Request.URL).Str("type", string(e.Type)).Msg("mirage: request sent")

		case *network.EventResponseReceived:
			go func() {
				reqURL := e.Response.URL
				log.Debug().Str("url", reqURL).Str("type", string(e.Type)).Int("status", int(e.Response.Status)).Msg("mirage: response received")

				if strings.Contains(reqURL, "/movies/") && (e.Type == network.ResourceTypeXHR || e.Type == network.ResourceTypeFetch) {
					log.Info().Str("url", reqURL).Int("status", int(e.Response.Status)).Msg("mirage: got /movies/ response")

					time.Sleep(300 * time.Millisecond)
					var body []byte
					err := chromedp.Run(timeoutCtx, chromedp.ActionFunc(func(c context.Context) error {
						var err error
						body, err = network.GetResponseBody(e.RequestID).Do(c)
						return err
					}))
					if err != nil {
						log.Warn().Err(err).Msg("mirage: failed to read /movies/ body")
						return
					}

					if stream := mirageExtractHLSFromJSON(body); stream != "" {
						signalDone(stream, body, nil)
						return
					}
				}

				if strings.Contains(reqURL, ".m3u8") {
					log.Info().Str("url", reqURL).Msg("mirage: caught m3u8 request")
					signalDone(reqURL, nil, nil)
				}
			}()

		case *fetch.EventRequestPaused:
			go func() {
				reqURL := e.Request.URL
				log.Debug().Str("url", reqURL).Str("type", string(e.ResourceType)).Msg("mirage: fetch paused")

				if e.ResourceType == network.ResourceTypeDocument && strings.Contains(reqURL, pageHost) && docFulfilled == 0 {
					docFulfilled = 1
					log.Info().Str("url", reqURL).Msg("mirage: fulfilling Document with pre-fetched HTML")
					headers := []*fetch.HeaderEntry{
						{Name: "Content-Type", Value: "text/html; charset=utf-8"},
					}
					_ = chromedp.Run(timeoutCtx, chromedp.ActionFunc(func(c context.Context) error {
						return fetch.FulfillRequest(e.RequestID, 200).
							WithResponseHeaders(headers).
							WithBody(htmlB64).
							Do(c)
					}))
					return
				}

				if strings.Contains(reqURL, "/movies/") && (e.ResourceType == network.ResourceTypeXHR || e.ResourceType == network.ResourceTypeFetch) {
					// Capture JS-generated CDN auth headers for later use in streaming.
					capturedHeaders := make(map[string]string)
					for k, v := range e.Request.Headers {
						kl := strings.ToLower(k)
						// Capture auth-related headers that JS generates.
						if kl == "borth" || kl == "authorization" || kl == "authorizations" ||
							strings.Contains(kl, "accepts-controls") || strings.Contains(kl, "accept-control") {
							capturedHeaders[k] = fmt.Sprint(v)
						}
					}
					if len(capturedHeaders) > 0 {
						log.Info().Interface("capturedHeaders", capturedHeaders).Msg("mirage: captured CDN auth headers from browser")
					}

					// Store POST params for lightweight refresh (re-call API without browser)
					{
						var postBody []byte
						for _, pe := range e.Request.PostDataEntries {
							if pe.Bytes != "" {
								if decoded, decErr := base64.StdEncoding.DecodeString(pe.Bytes); decErr == nil {
									postBody = append(postBody, decoded...)
								}
							}
						}
						allH := make(map[string]string, len(e.Request.Headers))
						for k, v := range e.Request.Headers {
							allH[k] = fmt.Sprint(v)
						}
						resultMu.Lock()
						capturedMoviesURL = moviesIDRe.ReplaceAllString(reqURL, "/movies/"+strconv.FormatInt(idFile, 10))
						capturedMoviesHeaders = allH
						capturedMoviesBody = postBody
						resultMu.Unlock()
						log.Info().Str("moviesURL", capturedMoviesURL).Int("headersCount", len(allH)).Int("bodyLen", len(postBody)).Msg("mirage: captured /movies/ POST params for lightweight refresh")
					}

					// Go HTTP proxy for /movies/: intercept the POST, patch the
					// id_file in the URL, and send via Go HTTP with SOCKS5.
					// ContinueRequest.WithURL() doesn't work for XHR — Chrome
					// ignores the URL override and sends to the original.
					// Go HTTP proxy reliably patches the URL and the CDN accepts
					// it (tested: segments delivered OK at 07:27).
					body, status, err := mirageProxyMoviesRequest(
						timeoutCtx, reqURL, e.Request.Method, e.Request.PostDataEntries,
						e.Request.Headers, idFile, pageURL, balancerName,
					)
					if err != nil {
						log.Warn().Err(err).Msg("mirage: proxy /movies/ failed")
						// Fallback: let Chrome try the original URL
						_ = chromedp.Run(timeoutCtx, chromedp.ActionFunc(func(c context.Context) error {
							return fetch.ContinueRequest(e.RequestID).Do(c)
						}))
						return
					}

					if status >= 200 && status < 300 {
						if stream := mirageExtractHLSFromJSON(body); stream != "" {
							signalDone(stream, body, capturedHeaders)
						}
					}

					respHeaders := []*fetch.HeaderEntry{
						{Name: "Content-Type", Value: "application/json"},
						{Name: "Access-Control-Allow-Origin", Value: "*"},
					}
					bodyB64 := base64.StdEncoding.EncodeToString(body)
					_ = chromedp.Run(timeoutCtx, chromedp.ActionFunc(func(c context.Context) error {
						return fetch.FulfillRequest(e.RequestID, int64(status)).
							WithResponseHeaders(respHeaders).
							WithBody(bodyB64).
							Do(c)
					}))
					return
				}

				// Capture CDN auth headers from m3u8 stream requests.
				// The player JS sets Authorizations/Accepts-Controls on these
				// requests which we need for proxying segments later.
				if strings.Contains(reqURL, ".m3u8") {
					// OPTIONS preflight — respond with permissive CORS so the
					// actual GET (with auth headers) fires.
					if e.Request.Method == "OPTIONS" {
						log.Info().Str("url", reqURL).Msg("mirage: fulfilling m3u8 OPTIONS preflight with CORS")
						corsHeaders := []*fetch.HeaderEntry{
							{Name: "Access-Control-Allow-Origin", Value: "*"},
							{Name: "Access-Control-Allow-Methods", Value: "GET, OPTIONS"},
							{Name: "Access-Control-Allow-Headers", Value: "authorizations,accepts-controls,borth,origin,referer"},
							{Name: "Access-Control-Max-Age", Value: "86400"},
						}
						_ = chromedp.Run(timeoutCtx, chromedp.ActionFunc(func(c context.Context) error {
							return fetch.FulfillRequest(e.RequestID, 204).
								WithResponseHeaders(corsHeaders).
								Do(c)
						}))
						return
					}

					// Actual GET — capture CDN auth headers + Origin/Referer.
					m3u8Headers := make(map[string]string)
					var hdrOrigin, hdrReferer string
					for k, v := range e.Request.Headers {
						kl := strings.ToLower(k)
						if kl == "borth" || kl == "authorization" || kl == "authorizations" ||
							strings.Contains(kl, "accepts-controls") || strings.Contains(kl, "accept-control") {
							m3u8Headers[k] = fmt.Sprint(v)
						}
						if kl == "origin" {
							hdrOrigin = fmt.Sprint(v)
						}
						if kl == "referer" {
							hdrReferer = fmt.Sprint(v)
						}
					}
					if hdrOrigin != "" || hdrReferer != "" {
						resultMu.Lock()
						if capturedRequestOrigin == "" && hdrOrigin != "" {
							capturedRequestOrigin = hdrOrigin
						}
						if capturedRequestReferer == "" && hdrReferer != "" {
							capturedRequestReferer = hdrReferer
						}
						resultMu.Unlock()
						log.Info().Str("origin", hdrOrigin).Str("referer", hdrReferer).Msg("mirage: captured Origin/Referer from m3u8 request")
					}
					if len(m3u8Headers) > 0 {
						log.Info().Interface("m3u8Headers", m3u8Headers).Str("url", reqURL).Msg("mirage: captured CDN auth headers from m3u8 request")
						signalDone("", nil, m3u8Headers)
					}
					_ = chromedp.Run(timeoutCtx, chromedp.ActionFunc(func(c context.Context) error {
						return fetch.ContinueRequest(e.RequestID).Do(c)
					}))
					return
				}

				_ = chromedp.Run(timeoutCtx, chromedp.ActionFunc(func(c context.Context) error {
					return fetch.ContinueRequest(e.RequestID).Do(c)
				}))
			}()

		// ── WebSocket protocol discovery (Phase 1) ──
		// Log all WebSocket traffic so we can reverse-engineer the
		// edge_hash / config_update protocol for a future Go WS client.
		case *network.EventWebSocketCreated:
			log.Info().Str("url", e.URL).Str("reqID", string(e.RequestID)).Msg("mirage: WS created")

		case *network.EventWebSocketWillSendHandshakeRequest:
			if e.Request != nil {
				hdrs := make(map[string]string)
				for k, v := range e.Request.Headers {
					hdrs[k] = fmt.Sprint(v)
				}
				log.Info().Interface("headers", hdrs).Str("reqID", string(e.RequestID)).Msg("mirage: WS handshake request")
			}

		case *network.EventWebSocketHandshakeResponseReceived:
			if e.Response != nil {
				log.Info().Int64("status", e.Response.Status).Str("statusText", e.Response.StatusText).Str("reqID", string(e.RequestID)).Msg("mirage: WS handshake response")
			}

		case *network.EventWebSocketFrameReceived:
			if e.Response != nil {
				payload := e.Response.PayloadData
				if len(payload) > 2000 {
					payload = payload[:2000] + "…"
				}
				log.Info().Str("payload", payload).Str("reqID", string(e.RequestID)).Msg("mirage: WS frame received")

				// EXPERIMENT: parse config_update from browser WS for edge_hash
				if mirageExperimentKeepBrowser && strings.Contains(e.Response.PayloadData, "config_update") {
					var wsMsg mirageWSConfigUpdate
					if err := stdjson.Unmarshal([]byte(e.Response.PayloadData), &wsMsg); err == nil && wsMsg.EdgeHash != "" {
						storeGlobalEdgeHash(wsMsg.EdgeHash)
						log.Info().Str("edge_hash", wsMsg.EdgeHash).Int("ttl", wsMsg.TTL).Msg("mirage: EXPERIMENT — edge_hash from browser WS")
					}
				}
			}

		case *network.EventWebSocketFrameSent:
			if e.Response != nil {
				payload := e.Response.PayloadData
				if len(payload) > 2000 {
					payload = payload[:2000] + "…"
				}
				log.Info().Str("payload", payload).Str("reqID", string(e.RequestID)).Msg("mirage: WS frame sent")
			}

		case *network.EventWebSocketFrameError:
			log.Warn().Str("error", e.ErrorMessage).Str("reqID", string(e.RequestID)).Msg("mirage: WS frame error")

		case *network.EventWebSocketClosed:
			log.Info().Str("reqID", string(e.RequestID)).Msg("mirage: WS closed")

		case *runtime.EventConsoleAPICalled:
			var args []string
			for _, arg := range e.Args {
				if arg.Value != nil {
					args = append(args, string(arg.Value))
				} else if arg.Description != "" {
					args = append(args, arg.Description)
				}
			}
			log.Debug().Str("type", string(e.Type)).Strs("args", args).Msg("mirage: console")

		case *runtime.EventExceptionThrown:
			if e.ExceptionDetails != nil && e.ExceptionDetails.Exception != nil {
				desc := e.ExceptionDetails.Exception.Description
				log.Warn().Str("exception", desc).Msg("mirage: JS exception")
			}
		}
	})

	log.Info().Str("navURL", navURL).Str("pageHost", pageHost).Msg("mirage: starting browser")

	docPattern := "*" + pageHost + "/?token_movie=*"
	err = chromedp.Run(timeoutCtx,
		network.Enable(),
		runtime.Enable(),
		// Fetch interception patterns — ONLY intercept what we need:
		//  1. Document (the main page) — to fulfill with pre-fetched HTML
		//  2. /movies/ — to patch id_file in the URL via ContinueRequest
		//  3. .m3u8 — to capture CDN auth headers
		//
		// Previously docPattern was "*pageHost*" which matched ALL requests
		// to the host (JS, CSS, SVG, favicon). Each intercepted request
		// requires a ContinueRequest call via chromedp.Run(), and these
		// serialize on CDP — 5 concurrent requests deadlocked the 20s
		// timeout. Now docPattern only matches the Document URL specifically.
		// JS/CSS/images load directly without interception.
		fetch.Enable().
			WithPatterns([]*fetch.RequestPattern{
				{URLPattern: docPattern, RequestStage: fetch.RequestStageRequest},
				{URLPattern: "*/bnsi/movies/*", RequestStage: fetch.RequestStageRequest},
				{URLPattern: "*.m3u8*", RequestStage: fetch.RequestStageRequest},
			}).
			WithHandleAuthRequests(false),
		// Block browser WebSocket to prevent CDN from tying the stream token
		// to a temporary browser WS session. Without this, the CDN binds the
		// token to the browser's WS; when chromedp exits, the WS dies and
		// the token expires in ~1.5 min. Our Go WS client should be the only session.
		//
		// EXPERIMENT: When mirageExperimentKeepBrowser is true, we DON'T block
		// the browser WS — we let it run natively and keep the browser alive.
		chromedp.ActionFunc(func(ctx context.Context) error {
			if mirageExperimentKeepBrowser {
				log.Info().Msg("mirage: EXPERIMENT — browser WS NOT blocked (keep-browser-alive mode)")
				return nil
			}
			_, err := page.AddScriptToEvaluateOnNewDocument(`
				(function(){
					var OrigWS = window.WebSocket;
					window.WebSocket = function(url, proto) {
						if (url && url.indexOf('/ws') !== -1 && url.indexOf('echo.websocket') === -1) {
							console.warn('WS blocked: ' + url);
							var fake = {
								readyState: 1, url: url, send: function(){}, close: function(){},
								addEventListener: function(e, cb) { if (e === 'open') setTimeout(cb, 100); },
								removeEventListener: function(){},
								onopen: null, onmessage: null, onerror: null, onclose: null,
								CONNECTING: 0, OPEN: 1, CLOSING: 2, CLOSED: 3
							};
							setTimeout(function(){ if(fake.onopen) fake.onopen({type:'open'}); }, 100);
							return fake;
						}
						return proto ? new OrigWS(url, proto) : new OrigWS(url);
					};
					window.WebSocket.CONNECTING = 0;
					window.WebSocket.OPEN = 1;
					window.WebSocket.CLOSING = 2;
					window.WebSocket.CLOSED = 3;
				})();
			`).Do(ctx)
			return err
		}),
	)
	if err != nil {
		log.Error().Err(err).Msg("mirage: chromedp enable failed")
		// Distinguish two failure modes:
		//
		//  1. "context deadline exceeded" — the master Chrome is wedged
		//     (CDP WebSocket debugger hang, OOM, or silent Chrome crash
		//     with the process still alive). Cancel the master so the
		//     next call of ensureMaster sees masterCtx==nil and creates
		//     a fresh one. Without this, every subsequent resolve
		//     re-uses the same dead master and fails indefinitely.
		//
		//  2. "context canceled" — a concurrent goroutine just cancelled
		//     OUR derived context while we were mid-Run (can happen if
		//     another resolve's error handler decided to restart master,
		//     or if r.Context() was cancelled by the client). Do NOT
		//     touch the master here — it may still be perfectly healthy
		//     and other resolves may be using it. Just let this resolve
		//     fail and return; the caller retries.
		//
		// Matching on the error message is a bit ugly but context.Err()
		// is not accessible through the chromedp Run return value, and
		// errors.Is(err, context.DeadlineExceeded) isn't reliable because
		// chromedp may wrap the cause.
		errMsg := err.Error()
		if strings.Contains(errMsg, "deadline exceeded") {
			mirageBrowser.mu.Lock()
			if mirageBrowser.masterCancel != nil {
				mirageBrowser.masterCancel()
			}
			mirageBrowser.masterCtx = nil
			mirageBrowser.masterCancel = nil
			mirageBrowser.mu.Unlock()
			log.Warn().Msg("mirage: master Chrome invalidated after deadline-exceeded on chromedp enable (will be recreated on next resolve)")
		}
		return nil, false
	}

	// Inject stealth JS before any page script runs.
	if err := chromedp.Run(timeoutCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		_, err := page.AddScriptToEvaluateOnNewDocument(mirageStealthJS).Do(ctx)
		return err
	})); err != nil {
		log.Warn().Err(err).Msg("mirage: stealth JS injection failed (non-fatal)")
	}

	go func() {
		err := chromedp.Run(timeoutCtx, chromedp.Navigate(navURL))
		if err != nil {
			log.Warn().Err(err).Str("url", navURL).Msg("mirage: Navigate returned error (non-fatal)")
		} else {
			log.Info().Str("url", navURL).Msg("mirage: Navigate completed successfully")
		}
	}()
	log.Info().Str("navURL", navURL).Msg("mirage: navigation started, waiting for stream...")

	select {
	case <-done:
		// Wait for stream auth headers from m3u8 request (Authorizations, Accepts-Controls).
		// These are set by the player JS on stream requests, not on the /movies/ API call.
		select {
		case <-allHeadersCh:
			log.Info().Msg("mirage: received stream auth headers from m3u8 request")
		case <-time.After(3 * time.Second):
			log.Warn().Msg("mirage: timed out waiting for stream auth headers from m3u8")
		case <-timeoutCtx.Done():
		}
		resultMu.Lock()
		s := resultStream
		b := resultBody
		h := resultHeaders
		resultMu.Unlock()
		if s != "" {
			// Capture DDoS-Guard cookies from the browser session.
			// DDoS-Guard sets __ddg* cookies after JS challenge — without them,
			// our Go HTTP client looks like an unverified bot to the CDN.
			if cookies := mirageCaptureBrowserCookies(timeoutCtx, s); cookies != "" {
				if h == nil {
					h = make(map[string]string)
				}
				h["Cookie"] = cookies
				log.Info().Str("cookies", cookies).Msg("mirage: captured CDN cookies from browser")
			}
			// Extract WS parameters from /movies/ response for edge_hash updates.
			wsUrl, sid := mirageExtractWSParams(b)
			resultMu.Lock()
			mURL := capturedMoviesURL
			mHdrs := capturedMoviesHeaders
			mBody := capturedMoviesBody
			rOrigin := capturedRequestOrigin
			rReferer := capturedRequestReferer
			resultMu.Unlock()
			log.Info().Str("stream", s).Int("bodyLen", len(b)).Int("headerCount", len(h)).Str("wsUrl", wsUrl).Int("sidLen", len(sid)).Str("moviesURL", mURL).Str("reqOrigin", rOrigin).Str("reqReferer", rReferer).Msg("mirage: resolved video via browser")
			res := &mirageResolveResult{
				RawBody:        b,
				Stream:         s,
				CDNHeaders:     h,
				WSUrl:          wsUrl,
				SID:            sid,
				RequestOrigin:  rOrigin,
				RequestReferer: rReferer,
				MoviesURL:      mURL,
				MoviesHeaders:  mHdrs,
				MoviesBody:     mBody,
			}
			if mirageExperimentKeepBrowser {
				res.BrowserCancel = cancel
				res.BrowserCtx = browserCtx
				log.Info().Msg("mirage: EXPERIMENT — browser kept alive, WS session running natively")
			}
			return res, true
		}
	case <-timeoutCtx.Done():
		log.Warn().Msg("mirage: browser resolve timed out")
	}

	// If we reach here with keep-browser, still need to cancel since resolve failed.
	if mirageExperimentKeepBrowser {
		cancel()
	}
	return nil, false
}

// mirageExtractWSParams extracts WebSocket URL and session ID from the /movies/
// JSON response. Field names are obfuscated in the player JS, so we use heuristics:
//   - wsUrl: string value starting with "wss://"
//   - sid: longest string value (>100 chars, typically ~700 char base64url token)
func mirageExtractWSParams(body []byte) (wsUrl, sid string) {
	if len(body) == 0 {
		return "", ""
	}
	var root map[string]any
	if err := stdjson.Unmarshal(body, &root); err != nil {
		return "", ""
	}

	var longestKey string
	var longestVal string

	for k, v := range root {
		s, ok := v.(string)
		if !ok {
			continue
		}
		// WebSocket URL
		if strings.HasPrefix(s, "wss://") {
			wsUrl = s
			log.Debug().Str("key", k).Str("value", s).Msg("mirage: found wsUrl in /movies/ response")
		}
		// Session ID — longest string (skip known fields)
		if len(s) > 100 && len(s) > len(longestVal) {
			longestKey = k
			longestVal = s
		}
	}

	if longestVal != "" && longestVal != wsUrl {
		sid = longestVal
		log.Debug().Str("key", longestKey).Int("len", len(sid)).Msg("mirage: found sid in /movies/ response")
	}

	return wsUrl, sid
}

// mirageCaptureBrowserCookies extracts cookies from the browser session for
// the CDN host used in the stream URL. DDoS-Guard sets __ddg* cookies after
// passing its JS challenge — these cookies are essential for Go HTTP requests
// to look like a verified browser visitor rather than a bot.
func mirageCaptureBrowserCookies(ctx context.Context, streamURL string) string {
	parsed, err := url.Parse(streamURL)
	if err != nil || parsed.Host == "" {
		return ""
	}

	var cookies []*network.Cookie
	err = chromedp.Run(ctx, chromedp.ActionFunc(func(c context.Context) error {
		var e error
		cookies, e = network.GetCookies().WithURLs([]string{parsed.Scheme + "://" + parsed.Host + "/"}).Do(c)
		return e
	}))
	if err != nil {
		log.Debug().Err(err).Msg("mirage: failed to get browser cookies")
		return ""
	}

	var parts []string
	for _, c := range cookies {
		parts = append(parts, c.Name+"="+c.Value)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "; ")
}

// fetchSegmentViaBrowser uses the kept-alive chromedp browser to fetch a CDN
// segment URL via the browser's fetch() API. This ensures real Chrome TLS/HTTP2
// fingerprint is used. Returns the raw segment bytes.
// Only works when mirageExperimentKeepBrowser=true and entry.browserCancel != nil.
//
// For segments up to ~10 MB (1080p), base64 through CDP is acceptable.
// For 4K segments (50+ MB), this may be slow — consider fallback.
func fetchSegmentViaBrowser(browserCtx context.Context, segmentURL, origin string, cdnHeaders map[string]string) ([]byte, error) {
	// Use the master Chrome context (not the player tab which may have blocking JS).
	masterCtx := mirageMasterBrowserCtx()
	if masterCtx == nil {
		return nil, fmt.Errorf("no master browser context")
	}
	if err := masterCtx.Err(); err != nil {
		return nil, fmt.Errorf("master browser context dead: %w", err)
	}

	// Create a fresh tab and navigate to the player origin so fetch() has a
	// proper origin (not null from about:blank). This avoids CORS preflight
	// issues with custom CDN headers like Accepts-Controls.
	tabCtx, tabCancel := chromedp.NewContext(masterCtx)
	defer tabCancel()

	// Navigate to a minimal page on the origin to establish proper origin context.
	// Use a data URI with the origin set — or navigate to origin directly.
	navCtx, navCancel := context.WithTimeout(tabCtx, 5*time.Second)
	defer navCancel()
	if err := chromedp.Run(navCtx, chromedp.Navigate(origin)); err != nil {
		// If origin navigation fails (e.g. DDoS-Guard), fall back to about:blank
		log.Debug().Err(err).Str("origin", origin).Msg("mirage: fetchSegment: origin nav failed, using about:blank")
	}

	// Build headers JSON for the fetch call.
	hdrsJS := fmt.Sprintf(`'Origin': %q, 'Referer': %q`, origin, origin+"/")
	for k, v := range cdnHeaders {
		hdrsJS += fmt.Sprintf(`, %q: %q`, k, v)
	}

	// Use fetch() in the tab. The browser shares the same network stack
	// (TLS fingerprint, HTTP/2 settings, connection pool) as the player tabs.
	js := fmt.Sprintf(`
		(async () => {
			try {
				const resp = await fetch(%q, {
					mode: 'cors',
					credentials: 'omit',
					headers: {%s}
				});
				if (!resp.ok) {
					return {error: 'HTTP ' + resp.status, status: resp.status};
				}
				const buf = await resp.arrayBuffer();
				const bytes = new Uint8Array(buf);
				// Convert to base64 in chunks to avoid stack overflow
				let binary = '';
				const chunkSize = 32768;
				for (let i = 0; i < bytes.length; i += chunkSize) {
					const chunk = bytes.subarray(i, Math.min(i + chunkSize, bytes.length));
					binary += String.fromCharCode.apply(null, chunk);
				}
				return {ok: true, data: btoa(binary), size: bytes.length};
			} catch (e) {
				return {error: e.message};
			}
		})()
	`, segmentURL, hdrsJS)

	ctx, cancel := context.WithTimeout(tabCtx, 10*time.Second)
	defer cancel()

	var res struct {
		OK     bool   `json:"ok"`
		Data   string `json:"data"`
		Size   int    `json:"size"`
		Error  string `json:"error"`
		Status int    `json:"status"`
	}
	err := chromedp.Run(ctx, chromedp.Evaluate(js, &res, chromedp.EvalAsValue,
		func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
			return p.WithAwaitPromise(true)
		}))
	if err != nil {
		return nil, fmt.Errorf("chromedp eval: %w", err)
	}
	if res.Error != "" {
		return nil, fmt.Errorf("browser fetch: %s (status %d)", res.Error, res.Status)
	}
	if !res.OK {
		return nil, fmt.Errorf("browser fetch: unknown error")
	}

	// Decode base64
	data, err := base64.StdEncoding.DecodeString(res.Data)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}

	log.Info().Int("size", len(data)).Str("url", segmentURL[:min(len(segmentURL), 80)]).
		Msg("mirage: fetched segment via browser")
	return data, nil
}

// mirageStripIframeCheck removes or neutralizes the inline <script> that
// checks `window != window.top` and destroys the page body when loaded outside
// an iframe. We replace it with a no-op so the player JS runs normally.
func mirageStripIframeCheck(html string) string {
	const marker = "var isFramed"
	idx := strings.Index(html, marker)
	if idx < 0 {
		return html
	}

	scriptStart := strings.LastIndex(html[:idx], "<script>")
	if scriptStart < 0 {
		return html
	}
	rest := html[idx:]
	scriptEnd := strings.Index(rest, "</script>")
	if scriptEnd < 0 {
		return html
	}
	scriptEnd = idx + scriptEnd + len("</script>")

	return html[:scriptStart] + "<script>/* iframe check removed */</script>" + html[scriptEnd:]
}

// mirageExtractHLSFromJSON extracts the best HLS stream URL from a
// /movies/ JSON response containing hlsSource array.
func mirageExtractHLSFromJSON(body []byte) string {
	quals := mirageExtractAllHLSQualities(body)
	if len(quals) == 0 {
		return mirageExtractStream(body)
	}
	// Return best quality
	for _, q := range []string{"2160", "1440", "1080", "720", "480", "360"} {
		if u, ok := quals[q]; ok {
			return u
		}
	}
	// Fallback: return any
	for _, u := range quals {
		return u
	}
	return ""
}

// mirageExtractAllHLSQualities extracts ALL quality → URL mappings from a
// /movies/ JSON response containing hlsSource array.
// Returns map like {"2160": "https://...m3u8", "1080": "https://...m3u8", ...}
func mirageExtractAllHLSQualities(body []byte) map[string]string {
	var root map[string]any
	if err := stdjson.Unmarshal(body, &root); err != nil {
		return nil
	}

	hlsSources, ok := root["hlsSource"]
	if !ok {
		return nil
	}

	sources, ok := hlsSources.([]any)
	if !ok || len(sources) == 0 {
		return nil
	}

	result := make(map[string]string)

	for _, src := range sources {
		srcMap, ok := src.(map[string]any)
		if !ok {
			continue
		}

		quality, ok := srcMap["quality"]
		if !ok {
			continue
		}

		qualMap, ok := quality.(map[string]any)
		if !ok {
			continue
		}

		isDefault, _ := srcMap["default"].(bool)
		for q, urlVal := range qualMap {
			urlStr := fmt.Sprint(urlVal)
			// CDN may return "URL1 or URL2" — take the first one
			if idx := strings.Index(urlStr, " or "); idx > 0 {
				urlStr = urlStr[:idx]
			}
			if !strings.Contains(urlStr, ".m3u8") && !strings.HasPrefix(urlStr, "http") {
				continue
			}
			// Prefer default source's URLs over non-default
			if _, exists := result[q]; !exists || isDefault {
				result[q] = urlStr
			}
		}
	}

	if len(result) == 0 {
		return nil
	}
	return result
}

// MirageAllocatorActive reports whether the shared chromedp allocator is
// live (admin stats payload).
func MirageAllocatorActive() bool { return mirageBrowser.allocCtx != nil }
