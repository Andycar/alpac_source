package litesrc

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/browser"
	"lampac-go/internal/browsergate"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/rs/zerolog/log"
)

// ---------------------------------------------------------------------------
// Android WebView stealth JS — mimics the official HDRezka APK v2.2.5.
// Fingerprint: Android Linux armv8l, Qualcomm Adreno GPU, 8 cores, etc.
// ---------------------------------------------------------------------------

const pidorezkaStealthJS = `(function(){
// 1. navigator.webdriver → false
Object.defineProperty(Object.getPrototypeOf(navigator),'webdriver',{
  set:undefined,enumerable:true,configurable:true,
  get:new Proxy(Object.getOwnPropertyDescriptor(Object.getPrototypeOf(navigator),'webdriver').get,{
    apply:(t,a,r)=>{Reflect.apply(t,a,r);return false}
  })
});
// 2. window.chrome stub (Android WebView has limited chrome object)
if(!window.chrome){Object.defineProperty(window,'chrome',{writable:true,enumerable:true,configurable:false,value:{}})}
if(!window.chrome.runtime){window.chrome.runtime={id:undefined,connect:null,sendMessage:null}}
if(!window.chrome.app){window.chrome.app={isInstalled:false,getDetails:function(){return null},getIsInstalled:function(){return false},runningState:function(){return'cannot_run'}}}
// 3. navigator.languages
Object.defineProperty(Object.getPrototypeOf(navigator),'languages',{get:()=>['ru-RU','ru','en-US','en']});
// 4. navigator.vendor — Google Inc. on Android Chrome
Object.defineProperty(Object.getPrototypeOf(navigator),'vendor',{get:()=>'Google Inc.'});
// 5. navigator.plugins — empty on Android (mobile browsers have no plugins)
// 6. navigator.permissions.query patch
try{
  const origQuery=window.navigator.permissions.query;
  window.navigator.permissions.query=function(p){
    if(p&&p.name==='notifications'){return Promise.resolve({state:'default'})}
    return origQuery.call(this,p);
  };
}catch(e){}
// 7. WebGL vendor/renderer — Qualcomm Adreno (typical Android flagship)
try{
  const gp=WebGLRenderingContext.prototype.getParameter;
  WebGLRenderingContext.prototype.getParameter=function(p){
    if(p===37445)return'Qualcomm';if(p===37446)return'Adreno (TM) 740';return gp.call(this,p);
  };
  if(typeof WebGL2RenderingContext!=='undefined'){
    const gp2=WebGL2RenderingContext.prototype.getParameter;
    WebGL2RenderingContext.prototype.getParameter=function(p){
      if(p===37445)return'Qualcomm';if(p===37446)return'Adreno (TM) 740';return gp2.call(this,p);
    };
  }
}catch(e){}
// 8. Screen dimensions — typical Android phone (1080x2400)
try{
  Object.defineProperty(screen,'width',{get:()=>1080});
  Object.defineProperty(screen,'height',{get:()=>2400});
  Object.defineProperty(screen,'availWidth',{get:()=>1080});
  Object.defineProperty(screen,'availHeight',{get:()=>2274});
  if(window.outerWidth&&window.outerHeight){window.outerWidth=1080;window.outerHeight=2274}
}catch(e){}
// 9. navigator.hardwareConcurrency — 8 cores (Snapdragon typical)
Object.defineProperty(Object.getPrototypeOf(navigator),'hardwareConcurrency',{get:()=>8});
// 10. navigator.platform — Linux armv8l (Android)
Object.defineProperty(Object.getPrototypeOf(navigator),'platform',{get:()=>'Linux armv8l'});
// 11. Media codecs
try{
  const origCanPlay=HTMLMediaElement.prototype.canPlayType;
  HTMLMediaElement.prototype.canPlayType=function(t){
    if(t&&t.includes('video/mp4')&&t.includes('avc1'))return'probably';
    if(t==='audio/aac')return'probably';
    return origCanPlay.call(this,t);
  };
}catch(e){}
// 12. navigator.deviceMemory — 8 GB (flagship Android)
try{Object.defineProperty(Object.getPrototypeOf(navigator),'deviceMemory',{get:()=>8})}catch(e){}
// 13. navigator.maxTouchPoints — touch device
try{Object.defineProperty(Object.getPrototypeOf(navigator),'maxTouchPoints',{get:()=>5})}catch(e){}
// 14. navigator.connection — mobile network info
try{
  if(!navigator.connection){
    Object.defineProperty(navigator,'connection',{get:()=>({effectiveType:'4g',rtt:50,downlink:10,saveData:false})});
  }
}catch(e){}
// 15. Inject APK's advert blocker (blocks ad XHRs like the real app)
try{
  const origOpen=XMLHttpRequest.prototype.open;
  XMLHttpRequest.prototype.open=function(method,url,async){
    if(url&&(url.match(/franecki\.net/)||url.match(/strosin\.biz/)||url.match(/serv01001\.xyz/)||url.match(/biocdn\.net/)||url.match(/franeski\.net/)||url.match(/reichelcormier\.bid/))){
      return;
    }
    return origOpen.apply(this,arguments);
  };
}catch(e){}
// 16. $.ajaxSetup — inject APK headers into jQuery AJAX (matches PlayerWebViewClient.java:154)
try{
  if(typeof $!=='undefined'&&$.ajaxSetup){
    $.ajaxSetup({
      beforeSend:function(xhr){
        xhr.setRequestHeader('X-Hdrezka-Android-App','1');
        xhr.setRequestHeader('X-Hdrezka-Android-App-Version','2.2.5');
      }
    });
  }
}catch(e){}
})();`

// pidorezkaAndroidUA — matches APK v2.2.5 SettingsData.updateUserAgent() exactly.
// Template: "Mozilla/5.0 (Linux; Android {VERSION}; {MANUFACTURER}) AppleWebKit/537.36 ..."
// Chrome/120.0.0.0 is hardcoded in the APK.
const pidorezkaAndroidUA = "Mozilla/5.0 (Linux; Android 14; samsung) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// ---------------------------------------------------------------------------
// pidorezkaBrowserPool — persistent headless Chrome for all Rezka interactions.
// ---------------------------------------------------------------------------

type pidorezkaBrowserPool struct {
	once        sync.Once
	allocCtx    context.Context
	userDataDir string
	socksProxy  string

	masterCtx    context.Context
	masterCancel context.CancelFunc
	mu           sync.Mutex
}

var pidorezkaBrowserInst pidorezkaBrowserPool

// pidorezkaBrowserSem caps concurrent rezka chromedp sessions and also
// routes through globalBrowserGate (shared mode), so fancdn-register's
// exclusive-mode lock can drain it for a clean reCAPTCHA v3 environment.
var pidorezkaBrowserSem = browsergate.NewChromeSem(4, browsergate.Global)

func (bp *pidorezkaBrowserPool) chromeOpts(userDataDir string) []chromedp.ExecAllocatorOption {
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
		chromedp.UserAgent(pidorezkaAndroidUA),
		chromedp.ModifyCmdFunc(func(cmd *exec.Cmd) {
			cmd.Env = browser.ChromeProcessEnv(cmd.Env, userDataDir)
		}),
	)
	if bp.socksProxy != "" {
		opts = append(opts, chromedp.ProxyServer("socks5://"+bp.socksProxy))
		log.Info().Str("socks", bp.socksProxy).Msg("pidorezka-browser: using SOCKS5 proxy")
	}
	return opts
}

func (bp *pidorezkaBrowserPool) init(socksProxy string) {
	bp.once.Do(func() {
		bp.socksProxy = socksProxy
		home, _ := os.UserHomeDir()
		userDataDir := filepath.Join(home, ".cache", "lampac", "rezka-chrome")
		if home == "" {
			userDataDir = "/tmp/rezka-chrome"
		}
		for _, name := range []string{"SingletonLock", "SingletonCookie", "SingletonSocket"} {
			_ = os.Remove(filepath.Join(userDataDir, name))
		}
		_ = os.MkdirAll(userDataDir, 0700)
		bp.userDataDir = userDataDir
		log.Info().Str("userDataDir", userDataDir).Msg("pidorezka-browser: chrome dirs")
		bp.allocCtx, _ = chromedp.NewExecAllocator(context.Background(), bp.chromeOpts(userDataDir)...)
	})
}

func (bp *pidorezkaBrowserPool) ensureMaster() {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	if bp.masterCtx != nil && bp.masterCtx.Err() == nil {
		return
	}
	ctx, cancel := chromedp.NewContext(bp.allocCtx,
		chromedp.WithLogf(func(format string, args ...any) {
			log.Debug().Msgf("[chromedp-rezka-master] "+format, args...)
		}),
	)
	if err := chromedp.Run(ctx, chromedp.Navigate("about:blank")); err != nil {
		log.Error().Err(err).Msg("pidorezka-browser: failed to start master Chrome")
		cancel()
		return
	}
	bp.masterCtx = ctx
	bp.masterCancel = cancel
	log.Info().Msg("pidorezka-browser: master Chrome started (persistent about:blank tab)")
}

func (bp *pidorezkaBrowserPool) reinit() {
	bp.mu.Lock()
	defer bp.mu.Unlock()

	userDataDir := bp.userDataDir
	if userDataDir == "" {
		home, _ := os.UserHomeDir()
		userDataDir = filepath.Join(home, ".cache", "lampac", "rezka-chrome")
		if home == "" {
			userDataDir = "/tmp/rezka-chrome"
		}
	}
	for _, name := range []string{"SingletonLock", "SingletonCookie", "SingletonSocket"} {
		_ = os.Remove(filepath.Join(userDataDir, name))
	}
	_ = os.MkdirAll(userDataDir, 0700)
	bp.userDataDir = userDataDir
	bp.allocCtx, _ = chromedp.NewExecAllocator(context.Background(), bp.chromeOpts(userDataDir)...)
}

// newTab creates a new browser tab from the master context.
// Caller must call cancel() when done with the tab.
func (bp *pidorezkaBrowserPool) newTab(timeout time.Duration) (context.Context, context.CancelFunc, error) {
	bp.init("")
	bp.ensureMaster()

	bp.mu.Lock()
	master := bp.masterCtx
	bp.mu.Unlock()
	if master == nil || master.Err() != nil {
		return nil, nil, fmt.Errorf("rezka-browser: master Chrome not available")
	}

	tabCtx, tabCancel := chromedp.NewContext(master)
	ctx, timeoutCancel := context.WithTimeout(tabCtx, timeout)
	cancel := func() {
		timeoutCancel()
		tabCancel()
	}

	// Inject stealth JS before any page loads.
	if err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		_, err := page.AddScriptToEvaluateOnNewDocument(pidorezkaStealthJS).Do(ctx)
		return err
	})); err != nil {
		cancel()
		return nil, nil, fmt.Errorf("rezka-browser: stealth inject failed: %w", err)
	}

	// Set extra HTTP headers on ALL requests (navigation, fetch, XHR).
	// HDRezka returns 403 without these APK headers.
	if err := chromedp.Run(ctx, network.Enable(), chromedp.ActionFunc(func(ctx context.Context) error {
		return network.SetExtraHTTPHeaders(network.Headers{
			"X-Hdrezka-Android-App":         "1",
			"X-Hdrezka-Android-App-Version": "2.2.5",
			"X-Requested-With":              "com.falcofemoralis.hdrezkaapp",
		}).Do(ctx)
	})); err != nil {
		cancel()
		return nil, nil, fmt.Errorf("rezka-browser: extra headers failed: %w", err)
	}

	return ctx, cancel, nil
}

// ---------------------------------------------------------------------------
// Browser methods on pidorezkaChecker
// ---------------------------------------------------------------------------

// browserLogin performs login via the persistent browser and captures cookies.
// Uses document.cookie capture (not CDP GetCookies) to avoid "invalid context"
// when the page auto-redirects after login.
func (r *pidorezkaChecker) browserLogin() error {
	if !pidorezkaBrowserSem.Acquire(context.Background()) {
		return fmt.Errorf("rezka-browser: login semaphore timeout")
	}
	defer pidorezkaBrowserSem.Release()

	ctx, cancel, err := pidorezkaBrowserInst.newTab(25 * time.Second)
	if err != nil {
		return err
	}
	defer cancel()

	// Navigate to host main page.
	if err := chromedp.Run(ctx, chromedp.Navigate(r.host+"/")); err != nil {
		log.Debug().Err(err).Msg("rezka-browser: navigate failed (may be 403, continuing)")
		// 403 might still set some cookies, try login anyway via about:blank approach
	}

	// Small delay for page to settle.
	_ = chromedp.Run(ctx, chromedp.Sleep(800*time.Millisecond))

	// Execute login via fetch and immediately capture document.cookie in the same JS call.
	// This avoids the "invalid context" race when page JS does location.reload() after login.
	loginJS := fmt.Sprintf(`(async () => {
  try {
    const resp = await fetch('%s/ajax/login/', {
      method: 'POST',
      credentials: 'include',
      headers: {
        'content-type': 'application/x-www-form-urlencoded; charset=UTF-8',
        'x-requested-with': 'XMLHttpRequest',
        'x-hdrezka-android-app': '1',
        'x-hdrezka-android-app-version': '2.2.5'
      },
      body: 'login_name=%s&login_password=%s&login_not_save=0'
    });
    const text = await resp.text();
    return JSON.stringify({body: text, cookies: document.cookie});
  } catch(e) {
    return JSON.stringify({error: String(e)});
  }
})()`, r.host, url.QueryEscape(r.login), url.QueryEscape(r.password))

	var loginRaw any
	if err := chromedp.Run(ctx, chromedp.Evaluate(loginJS, &loginRaw)); err != nil {
		return fmt.Errorf("rezka-browser: login eval failed: %w", err)
	}

	// Parse the combined result.
	rawStr := ""
	switch v := loginRaw.(type) {
	case string:
		rawStr = v
	default:
		if b, err := stdjson.Marshal(v); err == nil {
			rawStr = string(b)
		}
	}

	var result struct {
		Body    string `json:"body"`
		Cookies string `json:"cookies"`
		Error   string `json:"error"`
	}
	if err := stdjson.Unmarshal([]byte(rawStr), &result); err != nil {
		log.Debug().Str("raw", rawStr).Msg("rezka-browser: login result parse failed")
		return fmt.Errorf("rezka-browser: login result parse failed: %w", err)
	}
	if result.Error != "" {
		return fmt.Errorf("rezka-browser: login JS error: %s", result.Error)
	}
	log.Debug().Str("body", result.Body).Str("cookies", result.Cookies).Msg("rezka-browser: login response")

	// Also try CDP cookie capture (may work if context is still valid).
	cdpCookies := ""
	if cookies, err := r.captureBrowserCookies(ctx); err == nil && cookies != "" {
		cdpCookies = cookies
	}

	// Merge: prefer CDP cookies (more complete), fall back to document.cookie.
	allCookies := cdpCookies
	if !hasPidoRezkaAuthCookie(allCookies) {
		allCookies = result.Cookies
	}

	if hasPidoRezkaAuthCookie(allCookies) {
		r.authMu.Lock()
		r.authCookie = normalizePidoRezkaCookie(allCookies)
		r.applyAuthCookieToJar(r.authCookie)
		r.authDone = true
		r.authMu.Unlock()
		log.Info().Str("host", r.host).Msg("pidorezka-browser: login successful")
		return nil
	}

	return fmt.Errorf("rezka-browser: login failed — no auth cookies (cdp=%d, js=%d)", len(cdpCookies), len(result.Cookies))
}

// browserFetchPage fetches a page via the persistent browser and returns HTML.
func (r *pidorezkaChecker) browserFetchPage(reqCtx context.Context, pageURL string) (string, bool) {
	if !pidorezkaBrowserSem.Acquire(reqCtx) {
		return "", false
	}
	defer pidorezkaBrowserSem.Release()

	ctx, cancel, err := pidorezkaBrowserInst.newTab(20 * time.Second)
	if err != nil {
		log.Debug().Err(err).Msg("pidorezka-browser: fetchPage newTab failed")
		return "", false
	}
	defer cancel()

	// Set auth cookies before navigation.
	r.setBrowserAuthCookies(ctx)

	if err := chromedp.Run(ctx, chromedp.Navigate(pageURL)); err != nil {
		log.Debug().Err(err).Str("url", pageURL).Msg("pidorezka-browser: fetchPage navigate failed")
		return "", false
	}
	if err := chromedp.Run(ctx, chromedp.WaitReady("body", chromedp.ByQuery)); err != nil {
		log.Debug().Err(err).Msg("pidorezka-browser: fetchPage wait body failed")
		return "", false
	}
	if err := chromedp.Run(ctx, chromedp.Sleep(1200*time.Millisecond)); err != nil {
		return "", false
	}

	var html string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.documentElement.outerHTML`, &html)); err != nil {
		log.Debug().Err(err).Msg("pidorezka-browser: fetchPage eval outerHTML failed")
		return "", false
	}

	if html == "" || pidorezkaAccessDenied(200, html) {
		return "", false
	}

	// Sync cookies back.
	if cookies, err := r.captureBrowserCookies(ctx); err == nil && cookies != "" {
		r.syncCookiesFromBrowser(cookies)
	}

	// Debug: check for key content markers in browser-rendered HTML.
	hasTranslator := strings.Contains(html, "data-translator_id")
	hasInitCDN := strings.Contains(html, "initCDNSeriesEvents") || strings.Contains(html, "CDNPlayerInfo")
	hasStreams := strings.Contains(html, `"streams"`)
	log.Debug().
		Str("url", pageURL).
		Int("htmlLen", len(html)).
		Bool("hasTranslator", hasTranslator).
		Bool("hasInitCDN", hasInitCDN).
		Bool("hasStreams", hasStreams).
		Msg("pidorezka-browser: fetchPage success")
	return html, true
}

// browserSearch performs search via the persistent browser.
func (r *pidorezkaChecker) browserSearch(reqCtx context.Context, query string) (string, bool) {
	searchURL := r.host + "/search/?do=search&subaction=search&q=" + url.QueryEscape(query)
	return r.browserFetchPage(reqCtx, searchURL)
}

// browserAjaxPost performs the AJAX call inside a real browser context.
func (r *pidorezkaChecker) browserAjaxPost(
	reqCtx context.Context,
	id, t, action, director, favs, s, e, href string,
) (string, bool) {
	pageURL := r.resolveBaseReferer(id, href)
	if pageURL == "" || id == "" || t == "" {
		return "", false
	}

	if !pidorezkaBrowserSem.Acquire(reqCtx) {
		return "", false
	}
	defer pidorezkaBrowserSem.Release()

	ctx, cancel, err := pidorezkaBrowserInst.newTab(20 * time.Second)
	if err != nil {
		log.Debug().Err(err).Msg("pidorezka-browser: ajaxPost newTab failed")
		return "", false
	}
	defer cancel()

	r.setBrowserAuthCookies(ctx)

	if err := chromedp.Run(ctx, chromedp.Navigate(pageURL)); err != nil {
		log.Debug().Err(err).Str("url", pageURL).Msg("pidorezka-browser: ajaxPost navigate failed")
		return "", false
	}
	if err := chromedp.Run(ctx, chromedp.WaitReady("body", chromedp.ByQuery)); err != nil {
		return "", false
	}
	if err := chromedp.Run(ctx, chromedp.Sleep(1500*time.Millisecond)); err != nil {
		return "", false
	}

	// For get_stream on serials: click the episode via page JS and intercept stream.
	// hdrzk.org's AJAX API returns {} for direct calls but works through the page's own JS.
	if action == "get_stream" && s != "" && e != "" {
		return r.browserGetStreamViaClick(ctx, id, t, s, e)
	}

	// For get_movie: extract streams+subtitle from the page's embedded CDNPlayerInfo.
	if action == "get_movie" {
		return r.browserGetMovieFromPage(ctx, id)
	}

	// For other actions (get_episodes, etc.): try jQuery $.ajax.
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
	_ = "/ajax/get_cdn_series/?t=" + ts
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
	}

	// If favs is empty, try to get it from page's ctrl_favs element
	// (required by hdrzk.org to return valid stream data).
	if form.Get("favs") == "" {
		var pageFavs string
		_ = chromedp.Run(ctx, chromedp.Evaluate(`(function(){ var e=document.getElementById('ctrl_favs'); return e?e.value:''; })()`, &pageFavs))
		if pageFavs != "" {
			form.Set("favs", pageFavs)
		}
	}

	// Build $.ajax data object (not URL-encoded string — jQuery handles encoding).
	formJSON, _ := stdjson.Marshal(form)

	// Use jQuery's $.ajax from the page context.
	// The page's $.ajaxSetup already injects X-Hdrezka-Android-App headers.
	// $.ajax uses the page's cookie jar and session context, which works
	// even when raw fetch() returns {} on hdrzk.org.
	// Fallback: if jQuery is not available, use native fetch().
	js := `(function(){
  var url = '/ajax/get_cdn_series/?t=` + ts + `';
  var data = ` + string(formJSON) + `;
  return new Promise(function(resolve) {
    if (typeof $ !== 'undefined' && $.ajax) {
      $.ajax({
        url: url,
        type: 'POST',
        data: data,
        cache: false,
        success: function(resp) {
          resolve(typeof resp === 'string' ? resp : JSON.stringify(resp));
        },
        error: function(xhr, status, err) {
          resolve('ERR:jquery:' + status + ':' + err);
        }
      });
    } else {
      fetch(url, {
        method: 'POST',
        credentials: 'include',
        headers: {
          'content-type': 'application/x-www-form-urlencoded; charset=UTF-8',
          'x-requested-with': 'XMLHttpRequest'
        },
        body: new URLSearchParams(data).toString()
      }).then(function(r){ return r.text(); }).then(resolve).catch(function(e){ resolve('ERR:fetch:'+e); });
    }
  });
})()`

	var rawAny any
	if err := chromedp.Run(ctx, chromedp.Evaluate(js, &rawAny)); err != nil {
		log.Debug().Err(err).Str("id", id).Msg("pidorezka-browser: ajaxPost eval failed")
		return "", false
	}
	// Convert result to string — chromedp may return string or parsed JSON object.
	var raw string
	switch v := rawAny.(type) {
	case string:
		raw = v
	default:
		if b, err := stdjson.Marshal(v); err == nil {
			raw = string(b)
		}
	}
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "ERR:") {
		log.Debug().Str("id", id).Str("raw", raw).Msg("pidorezka-browser: ajaxPost empty/error")
		return "", false
	}

	// Sync cookies.
	if cookies, err := r.captureBrowserCookies(ctx); err == nil && cookies != "" {
		r.syncCookiesFromBrowser(cookies)
	}

	// Log first 300 chars of response for debugging.
	snippet := raw
	if len(snippet) > 300 {
		snippet = snippet[:300]
	}
	log.Info().Str("id", id).Str("t", t).Str("action", action).Int("bodyLen", len(raw)).Str("snippet", snippet).Msg("pidorezka-browser: ajaxPost success")
	return raw, true
}

// browserGetStreamViaClick clicks an episode on the page and intercepts the
// resulting AJAX response. hdrzk.org's AJAX API returns {} for direct calls,
// but the page's own JS (initCDNSeriesEvents) makes the same call with proper
// session context and gets a real response. We intercept via $.ajaxSetup.
func (r *pidorezkaChecker) browserGetStreamViaClick(ctx context.Context, id, t, s, e string) (string, bool) {
	// 1. Set up interceptor: patch $.ajax to capture the next get_cdn_series response.
	// 2. Click the correct translator tab (if needed), then season tab, then episode.
	// 3. Wait for the intercepted response.
	// Step 1: Click translator and season FIRST (these trigger their own AJAX calls
	// that we don't want to intercept). Wait for them to settle.
	clickPrepJS := `(function(){
  var trTab = document.querySelector('#translators-list [data-translator_id="` + t + `"]');
  if (trTab && !trTab.classList.contains('active')) trTab.click();
})();`
	_ = chromedp.Run(ctx, chromedp.Evaluate(clickPrepJS, nil))
	_ = chromedp.Run(ctx, chromedp.Sleep(1200*time.Millisecond))

	clickSeasonJS := `(function(){
  var sTab = document.querySelector('#simple-seasons-tabs [data-tab_id="` + s + `"]');
  if (sTab) sTab.click();
})();`
	_ = chromedp.Run(ctx, chromedp.Evaluate(clickSeasonJS, nil))
	_ = chromedp.Run(ctx, chromedp.Sleep(1200*time.Millisecond))

	// Step 2: NOW set up interceptor — translator and season AJAX are done.
	// Only the episode click AJAX will be captured.
	interceptJS := `(function(){
  window.__rezka_captured = null;
  if (typeof $ !== 'undefined' && $.ajax) {
    var origAjax = $.ajax;
    $.ajax = function(opts) {
      var origSuccess = opts.success;
      if (opts.url && opts.url.indexOf('get_cdn_series') !== -1) {
        opts.success = function(data) {
          if (!window.__rezka_captured) {
            window.__rezka_captured = typeof data === 'string' ? data : JSON.stringify(data);
          }
          if (origSuccess) origSuccess.apply(this, arguments);
        };
      }
      return origAjax.apply(this, [opts]);
    };
  }
  var origXHROpen = XMLHttpRequest.prototype.open;
  var origXHRSend = XMLHttpRequest.prototype.send;
  XMLHttpRequest.prototype.open = function(m, url) { this.__rz_url = url; return origXHROpen.apply(this, arguments); };
  XMLHttpRequest.prototype.send = function() {
    if (this.__rz_url && this.__rz_url.indexOf('get_cdn_series') !== -1) {
      var x = this;
      x.addEventListener('load', function() {
        if (!window.__rezka_captured && x.responseText && x.responseText.length > 10) {
          window.__rezka_captured = x.responseText;
        }
      });
    }
    return origXHRSend.apply(this, arguments);
  };
})();`
	if err := chromedp.Run(ctx, chromedp.Evaluate(interceptJS, nil)); err != nil {
		log.Debug().Err(err).Str("id", id).Msg("pidorezka-browser: getStreamViaClick inject failed")
		return "", false
	}

	// Step 3: Click the episode — this is the ONLY AJAX call we'll capture.
	clickEpJS := `(function(){
  var epEl = document.querySelector('#simple-episodes-tabs [data-season_id="` + s + `"][data-episode_id="` + e + `"]');
  if (epEl) {
    epEl.click();
  } else {
    window.__rezka_captured = 'ERR:episode_not_found_s` + s + `e` + e + `';
  }
})();`
	if err := chromedp.Run(ctx, chromedp.Evaluate(clickEpJS, nil)); err != nil {
		log.Debug().Err(err).Str("id", id).Msg("pidorezka-browser: getStreamViaClick click failed")
		return "", false
	}

	// Step 4: Wait for response (up to 10 seconds).
	var captured string
	for i := 0; i < 20; i++ {
		_ = chromedp.Run(ctx, chromedp.Sleep(500*time.Millisecond))
		var val any
		_ = chromedp.Run(ctx, chromedp.Evaluate(`window.__rezka_captured`, &val))
		if sv, ok := val.(string); ok && sv != "" {
			captured = sv
			break
		}
	}

	if captured == "" {
		log.Debug().Str("id", id).Str("s", s).Str("e", e).Msg("pidorezka-browser: getStreamViaClick no response captured")
		return "", false
	}

	log.Info().Str("id", id).Str("t", t).Str("s", s).Str("e", e).Int("bodyLen", len(captured)).Msg("pidorezka-browser: getStreamViaClick success")
	return captured, true
}

// browserGetMovieFromPage extracts movie stream data from the page's embedded
// JavaScript (CDNPlayerInfo / streams field) without relying on AJAX.
func (r *pidorezkaChecker) browserGetMovieFromPage(ctx context.Context, id string) (string, bool) {
	js := `(function(){
  // Try to find streams in CDNPlayerInfo or inline script
  var scripts = document.querySelectorAll('script');
  for (var i = 0; i < scripts.length; i++) {
    var txt = scripts[i].textContent || '';
    // Look for streams:"..." pattern
    var m = txt.match(/"streams"\s*:\s*"((?:[^"\\]|\\.)*)"/);
    if (m && m[1]) {
      var sub = '';
      var sm = txt.match(/"subtitle"\s*:\s*"((?:[^"\\]|\\.)*)"/);
      if (sm && sm[1]) sub = sm[1];
      return JSON.stringify({success:true, url: m[1], subtitle: sub});
    }
  }
  // Try CDNPlayerInfo global
  if (typeof CDNPlayerInfo !== 'undefined' && CDNPlayerInfo.media) {
    return JSON.stringify({success:true, url: CDNPlayerInfo.media, subtitle: CDNPlayerInfo.subtitle || ''});
  }
  return '{}';
})()`

	var rawAny any
	if err := chromedp.Run(ctx, chromedp.Evaluate(js, &rawAny)); err != nil {
		log.Debug().Err(err).Str("id", id).Msg("pidorezka-browser: getMovieFromPage eval failed")
		return "", false
	}
	raw := ""
	switch v := rawAny.(type) {
	case string:
		raw = v
	default:
		if b, err := stdjson.Marshal(v); err == nil {
			raw = string(b)
		}
	}
	if raw == "" || raw == "{}" {
		return "", false
	}
	log.Info().Str("id", id).Int("bodyLen", len(raw)).Msg("pidorezka-browser: getMovieFromPage success")
	return raw, true
}

// captureBrowserCookies extracts all cookies for the Rezka host from the browser.
func (r *pidorezkaChecker) captureBrowserCookies(ctx context.Context) (string, error) {
	u, err := url.Parse(r.host)
	if err != nil {
		return "", err
	}

	cookies, err := network.GetCookies().WithURLs([]string{r.host + "/"}).Do(ctx)
	if err != nil {
		return "", err
	}

	var parts []string
	for _, c := range cookies {
		if !strings.HasSuffix("."+u.Hostname(), "."+c.Domain) && u.Hostname() != c.Domain {
			continue
		}
		name := strings.TrimSpace(c.Name)
		value := strings.TrimSpace(c.Value)
		if name == "" || value == "" {
			continue
		}
		parts = append(parts, name+"="+value)
	}
	return strings.Join(parts, "; "), nil
}

// syncCookiesFromBrowser merges browser cookies into Go's cookie jar.
func (r *pidorezkaChecker) syncCookiesFromBrowser(cookies string) {
	if !hasPidoRezkaAuthCookie(cookies) {
		return
	}
	r.authMu.Lock()
	r.authCookie = normalizePidoRezkaCookie(cookies)
	r.applyAuthCookieToJar(r.authCookie)
	if !r.authDone {
		r.authDone = true
	}
	r.authMu.Unlock()
}

// SetPidoRezkaBrowserLimit changes the max concurrent browser tabs at runtime.
func SetPidoRezkaBrowserLimit(n int) {
	pidorezkaBrowserSem.SetLimit(n)
}

// PidoRezkaBrowserStats returns (active, limit) for the Rezka browser semaphore.
func PidoRezkaBrowserStats() (active int, limit int) {
	return pidorezkaBrowserSem.Stats()
}

// initPidoRezkaBrowser initializes the browser pool. Called from newPidoRezkaCheckerWith.
func initPidoRezkaBrowser(socksProxy string) {
	pidorezkaBrowserInst.init(socksProxy)
}
