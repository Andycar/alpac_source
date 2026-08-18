package litesrc

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"lampac-go/internal/browser"

	"github.com/rs/zerolog/log"
)

// kinobaseBrowser.ExtractFacade is the internal/browser-based port of
// the Node.js+puppeteer-core subprocess flow in ExtractNode. Reuses
// the same per-balancer engine selection as mirage — admin override
// for "kinobase" picks chromedp / rod / playwright.
//
// The Node.js flow is preserved as a fallback (Extract) and stays the
// default in the chromedp engine case until we explicitly test the
// facade path against live kinobase. Once verified, ExtractFacade can
// take over and the Node bundle goes away.
//
// Reference flow (mirrors kinobase_browser.go):
//  1. Set cookie player_settings=new|hls|0 on the film host
//  2. Set a Windows Chrome 120 user-agent
//  3. Intercept requests:
//     - */playerjs.js / */playerjs/*  →  Fulfill with a stub that
//     captures o.file into #playerjsfile
//     - /comments / images / fonts / media / stylesheets  →  Abort
//     - everything else                                    →  Continue
//  4. Navigate to the film URL, wait for #playerjsfile to appear,
//     read its textContent.
func (kb *kinobaseBrowser) ExtractFacade(ctx context.Context, filmURL, proxyAddr string) (string, error) {
	eng := browser.ForBalancer("kinobase")
	if eng == nil {
		return "", fmt.Errorf("kinobase facade: no browser engine registered")
	}
	if err := eng.Available(); err != nil {
		return "", fmt.Errorf("kinobase facade engine unavailable: %w", err)
	}

	parsed, err := url.Parse(filmURL)
	if err != nil {
		return "", fmt.Errorf("kinobase facade: parse film URL: %w", err)
	}
	host := parsed.Hostname()

	sessionCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	session, err := eng.NewSession(sessionCtx, browser.SessionOptions{
		Headless:          true,
		UseStealth:        true,
		UserAgent:         "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		SocksProxy:        socksProxyHostFromArg(proxyAddr),
		NavigationTimeout: 20 * time.Second,
	})
	if err != nil {
		return "", fmt.Errorf("kinobase facade: new session: %w", err)
	}
	defer session.Close()

	// player_settings cookie tells the kinobase player to use HLS (new
	// player, no fallback). Without it the page renders a Flash player
	// that doesn't trigger Playerjs.
	if err := session.SetCookie(&http.Cookie{
		Name:  "player_settings",
		Value: "new|hls|0",
		Path:  "/",
	}, host); err != nil {
		log.Debug().Err(err).Msg("kinobase facade: SetCookie failed (non-fatal)")
	}

	hijackCancel, err := session.Hijack(
		[]string{"*"},
		func(req browser.HijackRequest) {
			kinobaseFacadeHandle(req)
		},
	)
	if err != nil {
		return "", fmt.Errorf("kinobase facade: hijack setup: %w", err)
	}
	defer hijackCancel()

	if err := session.Navigate(filmURL); err != nil {
		// Navigate often returns "context deadline exceeded" once we
		// have the data; treat as non-fatal and proceed to selector
		// wait.
		log.Debug().Err(err).Msg("kinobase facade: Navigate returned (continuing)")
	}

	// Poll #playerjsfile via Eval — the facade interface intentionally
	// doesn't ship a WaitForSelector primitive (lowest-common
	// denominator across engines). 200ms granularity, 15s ceiling.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-sessionCtx.Done():
			return "", fmt.Errorf("kinobase facade: context cancelled")
		default:
		}
		var file string
		err := session.Eval(`(function(){
			var el = document.getElementById('playerjsfile');
			return el ? (el.textContent || '') : '';
		})()`, &file)
		if err == nil && strings.TrimSpace(file) != "" {
			return file, nil
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Try to surface the error message kinobase shows in an alert.
	var msg string
	_ = session.Eval(`(function(){
		var el = document.querySelector('.alert h3');
		return el ? (el.textContent || '').trim() : '';
	})()`, &msg)
	if msg != "" {
		return "", fmt.Errorf("kinobase facade: %s", msg)
	}
	return "", fmt.Errorf("kinobase facade: playerjsfile not found within 15s")
}

// kinobaseFacadeHandle is the request-paused dispatcher. Same three
// branches as the Node.js script (playerjs stub, dead-weight abort,
// continue).
func kinobaseFacadeHandle(req browser.HijackRequest) {
	url := req.URL()
	resType := strings.ToLower(req.ResourceType())

	// Match every playerjs flavour kinobase ships: playerjs.js,
	// playerjs.min.js, playerjs.uncompress.js, /playerjs/, etc. The
	// stub captures Playerjs(o) → #playerjsfile, so we MUST intercept
	// before the real script runs. The old Node.js path matched only
	// /playerjs.js literally and that's why kinobase "didn't work"
	// for months — the bundle has been called playerjs.uncompress.js
	// for a while.
	if mirrorMatchKinobasePlayerJS(url) {
		body := []byte(kinobasePlayerJSStub)
		hdrs := http.Header{"Content-Type": []string{"application/javascript"}}
		_ = req.Fulfill(200, hdrs, body)
		return
	}

	// Skip dead weight to speed up navigation. resourceType comes from
	// the underlying CDP / Rod / Playwright event and is normalised to
	// lowercase by our engine wrappers.
	if strings.Contains(url, "/comments") {
		_ = req.Abort("aborted")
		return
	}
	switch resType {
	case "image", "font", "media", "stylesheet":
		_ = req.Abort("aborted")
		return
	}

	_ = req.Continue()
}

// mirrorMatchKinobasePlayerJS returns true for any URL whose final
// path component starts with "playerjs" and ends in ".js" — covers
// /playerjs.js, /playerjs.min.js, /static/player/620/playerjs.uncompress.js,
// /playerjs/foo.js, etc. Standalone helper so it can be unit-tested.
func mirrorMatchKinobasePlayerJS(rawURL string) bool {
	// Cheap path: substring "playerjs" (case-insensitive) must appear.
	if !strings.Contains(strings.ToLower(rawURL), "playerjs") {
		return false
	}
	// Strip query/fragment, then check basename.
	if i := strings.IndexAny(rawURL, "?#"); i >= 0 {
		rawURL = rawURL[:i]
	}
	// Walk back to last slash; the trailing segment must look like a
	// playerjs script (or a /playerjs/ directory containing a .js).
	base := rawURL
	if i := strings.LastIndex(rawURL, "/"); i >= 0 {
		base = rawURL[i+1:]
	}
	if strings.HasPrefix(strings.ToLower(base), "playerjs") && strings.HasSuffix(base, ".js") {
		return true
	}
	// Directory-style: /playerjs/foo.js
	if strings.Contains(rawURL, "/playerjs/") && strings.HasSuffix(strings.ToLower(rawURL), ".js") {
		return true
	}
	return false
}

// kinobasePlayerJSStub is the same stub the Node.js script injects: it
// intercepts the Playerjs constructor and exposes the file URL via a
// hidden DOM element that our Eval poll reads.
const kinobasePlayerJSStub = `function Playerjs(o){var e=document.getElementById("playerjsfile");if(!e){e=document.createElement("div");e.id="playerjsfile";e.style.display="none";document.body.appendChild(e);}e.textContent=o.file||"";}var pljssglobal=null,pljssglobalid=null;`

// socksProxyHostFromArg trims an optional socks5:// prefix the legacy
// kinobase API passes — internal/browser.SessionOptions expects bare
// host:port.
func socksProxyHostFromArg(p string) string {
	if p == "" {
		return ""
	}
	p = strings.TrimPrefix(p, "socks5://")
	p = strings.TrimPrefix(p, "socks://")
	return p
}
