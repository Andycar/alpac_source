package litesrc

import (
	"context"
	"encoding/base64"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"lampac-go/internal/browsertmp"

	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"

	"github.com/rs/zerolog/log"
)

// ---------------------------------------------------------------------------
// Vibix resolve via coldfilm.ink + rendex SDK (canonical upstream flow)
//
// Vibix's publisher API stopped returning `iframe_url` (now always empty), so
// the old direct-API path can't find a playable frame. Instead we load
// coldfilm.ink (a whitelisted publisher of the shared rendex embed), inject an
// <ins> widget with the RAW kinopoisk/imdb id, let the rendex SDK build the
// Kinescope player iframe, and read the playlist the PlayerJS instance exposes
// via `pljssglobal[0].api('file')`. The browser computes the CDN `sign` itself,
// so we don't reverse-engineer it.
//
// A small poller is injected into every frame (Page.addScriptToEvaluateOnNew-
// Document): the cross-origin Kinescope sub-frame reads api('file') and
// postMessages it to the stable top frame, which we read back. This needs site
// isolation disabled (so the injected script reaches the sub-frame and the
// postMessage stays in-process) — hence a dedicated Chrome, not the shared one.
//
// The Chrome is kept WARM across resolves (a fresh process cold-starts ~2-3s):
// resolves are serialized through vibixWarmMu and reuse one browser, opening a
// fresh tab per resolve. The browser is recycled on failure or after a TTL /
// use cap so it can't wedge the way a long-lived shared Chrome did historically.
// ---------------------------------------------------------------------------

const vibixColdfilmPublisherID = "674784070"

// vibixPlayerFilePollJS is injected into EVERY frame. Top frame (coldfilm.ink):
// receives the playlist onto window.__vibixFile. Kinescope sub-frame: polls the
// PlayerJS playlist (pljssglobal[0].api('file')) and forwards it to the top.
const vibixPlayerFilePollJS = `(function(){
try{
if(window.top===window.self){
  window.__vibixFile=window.__vibixFile||"";
  window.addEventListener('message',function(e){
    try{ if(typeof e.data==='string' && e.data.indexOf('__VIBIXFILE__')===0){ window.__vibixFile=e.data.slice(13); } }catch(_){}
  });
}else{
  var tries=0;
  var iv=setInterval(function(){
    tries++;
    try{
      var P=window.pljssglobal&&window.pljssglobal[0];
      if(P&&typeof P.api==='function'){
        var f=P.api('file');
        if(f){ f=String(f); if(f.length>5){ clearInterval(iv); try{ window.top.postMessage('__VIBIXFILE__'+f,'*'); }catch(_){} } }
      }
    }catch(_){}
    if(tries>240){ clearInterval(iv); }
  },150);
}
}catch(_){}
})();`

type vibixPlaylistCacheEntry struct {
	playlist []vibixSeason
	expires  time.Time
}

var vibixPlaylistCache sync.Map // "kp:123" / "imdb:tt.." → vibixPlaylistCacheEntry

// ---- warm browser --------------------------------------------------------

type vibixWarmBrowser struct {
	allocCancel   context.CancelFunc
	browserCtx    context.Context
	browserCancel context.CancelFunc
	tmpDir        string
	createdAt     time.Time
	uses          int
}

const (
	vibixWarmTTL     = 10 * time.Minute
	vibixWarmUseCap  = 30
	vibixWarmMaxOpen = 25 * time.Second
)

var (
	vibixWarmMu sync.Mutex // serializes resolves and guards the warm browser
	vibixWarm   *vibixWarmBrowser
)

func vibixWarmTeardown(b *vibixWarmBrowser) {
	if b == nil {
		return
	}
	// chromedp.Cancel closes the browser gracefully AND waits for the process to
	// exit; the bare cancel funcs return while Chrome is still writing to its
	// profile, so removing the directory used to race it — Chrome re-created the
	// files right after deletion and /tmp filled up with vibix-chrome-*
	// leftovers. Bounded: a wedged browser must not hold vibixWarmMu.
	if b.browserCtx != nil {
		closed := make(chan struct{})
		go func() {
			defer close(closed)
			if err := chromedp.Cancel(b.browserCtx); err != nil {
				log.Debug().Err(err).Msg("vibix: graceful chrome shutdown failed")
			}
		}()
		select {
		case <-closed:
		case <-time.After(10 * time.Second):
			log.Warn().Msg("vibix: chrome did not exit in time, forcing teardown")
		}
	}
	if b.browserCancel != nil {
		b.browserCancel()
	}
	if b.allocCancel != nil {
		b.allocCancel()
	}
	// Off the lock: Remove retries for a few seconds when Chrome is still dying.
	if b.tmpDir != "" {
		go browsertmp.Remove(b.tmpDir)
	}
}

// vibixEnsureWarmLocked returns a live warm browser context, launching or
// recycling as needed. Caller holds vibixWarmMu.
func vibixEnsureWarmLocked() (context.Context, bool) {
	if b := vibixWarm; b != nil {
		if b.browserCtx.Err() == nil && time.Since(b.createdAt) < vibixWarmTTL && b.uses < vibixWarmUseCap {
			b.uses++
			return b.browserCtx, true
		}
		vibixWarmTeardown(b)
		vibixWarm = nil
	}

	tmpDir, err := browsertmp.New("vibix-chrome-")
	if err != nil {
		log.Error().Err(err).Msg("vibix: temp dir")
		return nil, false
	}
	// Site isolation OFF so the injected poller reaches the cross-origin
	// Kinescope iframe and its postMessage to the top frame stays in-process.
	allocOpts := append(mirageBrowser.chromeOptsForDir(tmpDir, ""),
		chromedp.Flag("disable-site-isolation-trials", true),
		chromedp.Flag("disable-features", "IsolateOrigins,site-per-process"),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	browserCtx, browserCancel := chromedp.NewContext(allocCtx,
		chromedp.WithLogf(func(format string, args ...any) {
			log.Debug().Msgf("[chromedp-vibix] "+format, args...)
		}),
	)
	// Launch Chrome now (the anchor browserCtx keeps the process alive between
	// resolves). Run directly on browserCtx — wrapping it in a timeout child and
	// cancelling that child would poison the reused browser context. A watchdog
	// bounds the cold start instead.
	launched := make(chan error, 1)
	go func() { launched <- chromedp.Run(browserCtx, chromedp.Navigate("about:blank")) }()
	select {
	case err := <-launched:
		if err != nil {
			log.Warn().Err(err).Msg("vibix: warm chrome launch failed")
			browserCancel()
			allocCancel()
			go browsertmp.Remove(tmpDir)
			return nil, false
		}
	case <-time.After(25 * time.Second):
		log.Warn().Msg("vibix: warm chrome launch timed out")
		browserCancel()
		allocCancel()
		go browsertmp.Remove(tmpDir)
		return nil, false
	}
	vibixWarm = &vibixWarmBrowser{
		allocCancel:   allocCancel,
		browserCtx:    browserCtx,
		browserCancel: browserCancel,
		tmpDir:        tmpDir,
		createdAt:     time.Now(),
		uses:          1,
	}
	return browserCtx, true
}

func vibixInvalidateWarmLocked() {
	if vibixWarm != nil {
		vibixWarmTeardown(vibixWarm)
		vibixWarm = nil
	}
}

// ---- resolve -------------------------------------------------------------

// vibixSanitizeID keeps only characters valid in a kinopoisk/imdb id so the
// value can be embedded safely in the injected HTML attribute.
func vibixSanitizeID(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// vibixResolveViaColdfilm returns the PlayerJS playlist for a title (movie: one
// item per voice with .file; serial: items with .folder). Results cache 20 min.
func vibixResolveViaColdfilm(parentCtx context.Context, imdbID string, kinopoiskID int64) ([]vibixSeason, bool) {
	imdbID = vibixSanitizeID(strings.TrimSpace(imdbID))
	if imdbID == "" && kinopoiskID <= 0 {
		return nil, false
	}

	cacheKey := "imdb:" + imdbID
	dataType, dataID := "imdb", imdbID
	if kinopoiskID > 0 {
		cacheKey = "kp:" + strconv.FormatInt(kinopoiskID, 10)
		dataType, dataID = "kp", strconv.FormatInt(kinopoiskID, 10)
	}

	if pl, ok := vibixCacheGet(cacheKey); ok {
		return pl, len(pl) > 0
	}

	// Serialize resolves through the warm browser. checksearch uses the API (no
	// browser), and results cache, so contention here is rare.
	vibixWarmMu.Lock()
	defer vibixWarmMu.Unlock()

	// Another goroutine may have resolved this while we waited for the lock.
	if pl, ok := vibixCacheGet(cacheKey); ok {
		return pl, len(pl) > 0
	}

	// The client may have disconnected while queued behind the lock.
	if parentCtx.Err() != nil {
		return nil, false
	}

	browserCtx, ok := vibixEnsureWarmLocked()
	if !ok {
		return nil, false
	}

	start := time.Now()
	playlist, ok := vibixResolveOnBrowser(parentCtx, browserCtx, dataType, dataID)
	if !ok {
		// Recycle on failure — a wedged browser would otherwise fail every resolve.
		vibixInvalidateWarmLocked()
		log.Warn().Str("id", dataID).Dur("took", time.Since(start)).Msg("vibix: coldfilm resolve failed")
		return nil, false
	}

	vibixPlaylistCache.Store(cacheKey, vibixPlaylistCacheEntry{
		playlist: playlist,
		expires:  time.Now().Add(20 * time.Minute),
	})
	log.Info().Int("items", len(playlist)).Str("id", dataID).Dur("took", time.Since(start)).Msg("vibix: resolved playlist via coldfilm/rendex")
	return playlist, true
}

func vibixCacheGet(key string) ([]vibixSeason, bool) {
	if cached, ok := vibixPlaylistCache.Load(key); ok {
		if ce, ok := cached.(vibixPlaylistCacheEntry); ok && time.Now().Before(ce.expires) {
			return ce.playlist, true
		}
	}
	return nil, false
}

// vibixResolveOnBrowser opens a fresh tab on the warm browser, loads the
// coldfilm embed and returns the PlayerJS playlist.
func vibixResolveOnBrowser(parentCtx, browserCtx context.Context, dataType, dataID string) ([]vibixSeason, bool) {
	coldHTML := `<!DOCTYPE html><html lang="ru"><head><meta charset="UTF-8">` +
		`<script src="https://graphicslab.io/sdk/v2/rendex-sdk.min.js"></script></head>` +
		`<body><ins data-publisher-id="` + vibixColdfilmPublisherID + `" data-type="` + dataType + `" data-id="` + dataID + `"></ins></body></html>`
	coldB64 := base64.StdEncoding.EncodeToString([]byte(coldHTML))

	tabCtx, tabCancel := chromedp.NewContext(browserCtx)
	// chromedp.Cancel closes the tab target on the warm browser; a plain cancel
	// would leak the tab (a whole renderer) and balloon memory.
	defer func() {
		_ = chromedp.Cancel(tabCtx)
		tabCancel()
	}()

	timeoutCtx, timeoutCancel := context.WithTimeout(tabCtx, vibixWarmMaxOpen)
	defer timeoutCancel()

	go func() {
		select {
		case <-parentCtx.Done():
			tabCancel()
		case <-timeoutCtx.Done():
		}
	}()

	var docFulfilled int32
	chromedp.ListenTarget(timeoutCtx, func(ev any) {
		e, ok := ev.(*fetch.EventRequestPaused)
		if !ok {
			return
		}
		go func() {
			reqURL := e.Request.URL
			if e.ResourceType == network.ResourceTypeDocument &&
				strings.Contains(reqURL, "coldfilm.ink") &&
				atomic.CompareAndSwapInt32(&docFulfilled, 0, 1) {
				_ = chromedp.Run(timeoutCtx, chromedp.ActionFunc(func(c context.Context) error {
					return fetch.FulfillRequest(e.RequestID, 200).
						WithResponseHeaders([]*fetch.HeaderEntry{
							{Name: "Content-Type", Value: "text/html; charset=utf-8"},
						}).
						WithBody(coldB64).
						Do(c)
				}))
				return
			}
			_ = chromedp.Run(timeoutCtx, chromedp.ActionFunc(func(c context.Context) error {
				return fetch.ContinueRequest(e.RequestID).Do(c)
			}))
		}()
	})

	if err := chromedp.Run(timeoutCtx,
		network.Enable(),
		fetch.Enable().WithPatterns([]*fetch.RequestPattern{
			{URLPattern: "*coldfilm.ink*", RequestStage: fetch.RequestStageRequest},
		}),
	); err != nil {
		log.Warn().Err(err).Msg("vibix: chromedp enable failed")
		return nil, false
	}

	if err := chromedp.Run(timeoutCtx, chromedp.ActionFunc(func(c context.Context) error {
		_, err := page.AddScriptToEvaluateOnNewDocument(vibixPlayerFilePollJS).Do(c)
		return err
	})); err != nil {
		log.Warn().Err(err).Msg("vibix: poll JS injection failed")
	}

	if err := chromedp.Run(timeoutCtx, chromedp.ActionFunc(func(c context.Context) error {
		_, _, _, _, err := page.Navigate("https://coldfilm.ink/").Do(c)
		return err
	})); err != nil {
		log.Warn().Err(err).Msg("vibix: coldfilm navigation failed (non-fatal)")
	}

	// Poll window.__vibixFile on the stable top frame until the injected poller
	// forwards the Kinescope player's playlist.
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-timeoutCtx.Done():
			return nil, false
		case <-ticker.C:
		}

		var fileStr string
		if err := chromedp.Run(timeoutCtx, chromedp.Evaluate(`window.__vibixFile||""`, &fileStr)); err != nil {
			continue
		}
		fileStr = strings.TrimSpace(fileStr)
		if len(fileStr) < 5 {
			continue
		}
		if playlist, ok := vibixParsePlayerFile(fileStr); ok && len(playlist) > 0 {
			return playlist, true
		}
	}
}
