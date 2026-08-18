package litesrc

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/browser"
	"lampac-go/internal/proxyapi"

	"github.com/chromedp/chromedp"
	"github.com/rs/zerolog/log"
)

// zona_browser.go drives headless Chrome (reusing the same :9222 instance
// that turbo/mirage/vibix already share) to call the player's private
// getStreams API and return raw stream JSON chunks.
//
// The player lives at https://kinoserial.online/?projectId=1&projectName=zona
// and, after loader.js initialises, exposes
//
//	window['ru.zona.stream.site'].getStreams(kpId, isTrailer, episodeKey, cbs)
//
// The cbs object must have onStreamsReceived(jsonString) and onCompletion(n).
// Each chunk is already a complete JSON source definition, so we just append
// chunks to a window-scoped array and read it back once done.

var zonaBrowser struct {
	mu        sync.Mutex
	allocCtx  context.Context
	allocStop context.CancelFunc
	// port is the CDP port of a remote Chrome (positive) OR the requested
	// port that the allocator was created for (even in local mode — see
	// isLocal for the distinction).
	port    int
	isLocal bool   // true if allocCtx was created by a local ExecAllocator
	wsURL   string // resolved WebSocketDebuggerUrl for the remote allocator

	// browserCtx is the ROOT chromedp context for the current allocator.
	// It owns the actual Chrome process/connection and is created once via
	// chromedp.NewContext(allocCtx) + a no-op Run to boot the browser.
	// Each resolve then spawns a fresh TAB context via
	// chromedp.NewContext(browserCtx) — tabs share the same Chrome so
	// cold-start cost is paid once per process.
	browserCtx    context.Context
	browserCancel context.CancelFunc

	localOnce sync.Once
}

// zonaBrowserPortDefault is the fallback CDP port used when config.zona.chrome_port
// is unset. 9222 is the same port turbo/mirage/vibix assume, so one Chrome
// instance can serve all of them.
const zonaBrowserPortDefault = 9222

// resolveStreams calls getStreams for (kpID, episodeKey). episodeKey is
// empty for movies and "S01E01" for serials.
//
// Three layers of deduplication keep the Chrome semaphore cheap:
//  1. 30 min TTL cache keyed by "kp:episodeKey".
//  2. Singleflight — concurrent callers for the same key wait on the
//     first resolver's `done` channel, then read from cache.
//  3. Detached Chrome context — the chromedp work runs on
//     `context.Background()` so that when a caller's HTTP client
//     disconnects mid-request we still finish the resolve and populate
//     the cache for the next caller.
func (z *zonaChecker) resolveStreams(parentCtx context.Context, kpID int64, episodeKey string) ([]zonaStream, bool) {
	cacheKey := "kp:" + strconv.FormatInt(kpID, 10) + ":" + episodeKey
	if streams, ok := z.lookupStreamsCache(cacheKey); ok {
		return streams, streams != nil
	}

	// Singleflight: either start a new resolve or join an existing one.
	z.inflightMu.Lock()
	if ifl, ok := z.inflight[cacheKey]; ok {
		z.inflightMu.Unlock()
		// Another goroutine is already resolving this. Wait on its done
		// channel (or give up if OUR caller has gone away).
		select {
		case <-ifl.done:
		case <-parentCtx.Done():
			return nil, false
		}
		streams, ok := z.lookupStreamsCache(cacheKey)
		return streams, ok && streams != nil
	}
	ifl := &zonaInflight{done: make(chan struct{})}
	z.inflight[cacheKey] = ifl
	z.inflightMu.Unlock()

	// Run the Chrome resolve on a DETACHED context. The caller's timeout
	// must not kill the browser session — otherwise every cancelled Lampa
	// probe leaves a dead-cold Chrome behind and we never get streams.
	go func() {
		defer func() {
			z.inflightMu.Lock()
			delete(z.inflight, cacheKey)
			z.inflightMu.Unlock()
			close(ifl.done)
		}()

		bgCtx, cancel := context.WithTimeout(context.Background(), zonaOpBudget)
		defer cancel()

		raw, ok := z.fetchStreamsViaChrome(bgCtx, kpID, episodeKey)
		streams := zonaParseStreams(raw)
		ttl := zonaStreamsCacheTTL
		if !ok || len(streams) == 0 {
			// Cache negative result briefly to avoid slamming Chrome for
			// films Zona doesn't have.
			streams = nil
			ttl = 5 * time.Minute
		}
		z.streamsCache.Store(cacheKey, zonaStreamsCacheEntry{
			streams: streams,
			expires: time.Now().Add(ttl),
		})
	}()

	// Wait for the resolve to complete, or give up if OUR caller leaves.
	select {
	case <-ifl.done:
	case <-parentCtx.Done():
		// Caller bailed — the background goroutine will still finish and
		// populate the cache so the NEXT caller gets an instant hit.
		return nil, false
	}

	streams, ok := z.lookupStreamsCache(cacheKey)
	return streams, ok && streams != nil
}

// lookupStreamsCache returns a cached entry if present and unexpired. The
// ok bool distinguishes "cache hit, no streams (negative cache)" from
// "cache miss" — both cases return a nil slice.
func (z *zonaChecker) lookupStreamsCache(cacheKey string) ([]zonaStream, bool) {
	v, ok := z.streamsCache.Load(cacheKey)
	if !ok {
		return nil, false
	}
	ce := v.(zonaStreamsCacheEntry)
	if time.Now().After(ce.expires) {
		return nil, false
	}
	return ce.streams, true
}

// fetchStreamsViaChrome is the production entry point. It uses z.chromePort
// (config) and falls back to launching a local headless Chrome if the remote
// port is unreachable.
func (z *zonaChecker) fetchStreamsViaChrome(parentCtx context.Context, kpID int64, episodeKey string) ([]string, bool) {
	port := z.chromePort
	if port <= 0 {
		port = zonaBrowserPortDefault
	}
	return z.fetchStreamsViaChromePort(parentCtx, kpID, episodeKey, port)
}

// Tuning knobs for the Chrome-driven getStreams loop.
//
// Budgets are cumulative:
//
//	opBudget       — 75 s (whole operation, detached from caller context)
//	tabBudget      — 70 s (Chrome navigate + eval loop)
//	navBudget      — 30 s (page load — cold ExecAllocator + loader.js)
//	pollBudget     — 35 s (onCompletion or first stream grace window)
//	dumpBudget     —  5 s (extra budget reserved for the final eval)
//
// Rationale: local ExecAllocator on a cold profile takes ~10-20 s just to
// launch Chrome and another ~5-10 s to load loader.js. Each extractor has
// its own 60 s internal timeout. We can't wait for all of them, but we DO
// want to collect every stream that arrives before the poll cap — even if
// onCompletion never fires. The dump eval uses a separate context so we
// still pull partial results on poll timeout.
const (
	zonaOpBudget   = 60 * time.Second
	zonaTabBudget  = 55 * time.Second
	zonaNavBudget  = 25 * time.Second
	zonaPollBudget = 30 * time.Second
	zonaDumpBudget = 5 * time.Second
)

// zonaResolveMu serialises browser resolves within a single lampac process.
// Cold Chrome struggles with parallel navigations (they fight for main
// thread, and loader.js uses globals), so we process one at a time. The
// singleflight/inflight map handles dedup per (kp, episode) — this mutex
// handles cross-film serialisation.
var zonaResolveMu sync.Mutex

// fetchStreamsViaChromePort opens a tab at kinoserial.online and invokes
// getStreams(kpID, false, episodeKey, {...}). It polls window.__zona_done
// and returns the accumulated stream JSON strings. The port parameter lets
// integration tests target a separate Chrome instance.
func (z *zonaChecker) fetchStreamsViaChromePort(parentCtx context.Context, kpID int64, episodeKey string, port int) ([]string, bool) {
	// Serialise across films — cold Chrome can't handle parallel navigates.
	zonaResolveMu.Lock()
	defer zonaResolveMu.Unlock()

	ctx, cancel := context.WithTimeout(parentCtx, zonaOpBudget)
	defer cancel()

	if !mirageSem().Acquire(ctx) {
		log.Warn().Msg("zona: browser semaphore acquire failed")
		return nil, false
	}
	defer mirageSem().Release()

	browserCtx, err := zonaGetBrowserCtx(port)
	if err != nil {
		log.Warn().Err(err).Int64("kp", kpID).Msg("zona: browser context unavailable")
		return nil, false
	}
	if err := browserCtx.Err(); err != nil {
		log.Warn().Err(err).Int64("kp", kpID).Msg("zona: browserCtx errored — resetting")
		zonaResetAllocator()
		return nil, false
	}

	// The tab's Context must NOT be wrapped with WithTimeout between
	// NewContext and the first Run: chromedp binds the tab lifecycle to
	// whatever context is first Run'd against. When that wrapper cancels
	// (e.g. via defer), chromedp tears down the tab even though the
	// browser is fine. We therefore derive exactly ONE timeout context
	// from the tab, and use THAT for every subsequent Run.
	tabCtx, tabCancel := chromedp.NewContext(browserCtx)
	defer tabCancel()

	workCtx, workCancel := context.WithTimeout(tabCtx, zonaTabBudget)
	defer workCancel()

	t0 := time.Now()
	// Step 1: navigate (no WaitReady — it can cancel the tab in some cases).
	// loader.js finishes async; kick eval probes window['ru.zona.stream.site'].
	navErr := chromedp.Run(workCtx, chromedp.Navigate(zonaKinoserialURL))
	if navErr != nil {
		log.Warn().Err(navErr).Int64("kp", kpID).Dur("elapsed", time.Since(t0)).Msg("zona: navigate failed")
		return nil, false
	}
	log.Info().Int64("kp", kpID).Dur("elapsed", time.Since(t0)).Msg("zona: navigate ok")

	// Step 2: kick off getStreams.
	kickJS := fmt.Sprintf(`(function(){
		window.__zona_streams = [];
		window.__zona_done = false;
		window.__zona_err = '';
		window.__zona_count = 0;
		var api = window['ru.zona.stream.site'];
		if (!api || typeof api.getStreams !== 'function') {
			window.__zona_err = 'no api: ' + (typeof api);
			window.__zona_done = true;
			return 'no api';
		}
		try {
			var ep = %q || null;
			api.getStreams(%d, false, ep, {
				onStreamsReceived: function(s){ if (s) window.__zona_streams.push(s); },
				onCompletion: function(n){ window.__zona_done = true; window.__zona_count = n || 0; }
			});
			return 'called';
		} catch (e) {
			window.__zona_err = e && e.message || String(e);
			window.__zona_done = true;
			return 'err';
		}
	})()`, episodeKey, kpID)

	var kickResult string
	if err := chromedp.Run(workCtx, chromedp.Evaluate(kickJS, &kickResult)); err != nil {
		log.Warn().Err(err).
			Bool("workCtxCancelled", workCtx.Err() != nil).
			Bool("tabCtxCancelled", tabCtx.Err() != nil).
			Bool("browserCtxCancelled", browserCtx.Err() != nil).
			Int64("kp", kpID).
			Dur("elapsed", time.Since(t0)).
			Msg("zona: kick eval failed")
		return z.dumpStreamsPartial(tabCtx, kpID, episodeKey, "kick eval failed")
	}
	if kickResult == "no api" {
		// loader.js hasn't initialised yet — give it another 3 s and retry once.
		chromedp.Run(workCtx, chromedp.Sleep(3*time.Second))
		if err := chromedp.Run(workCtx, chromedp.Evaluate(kickJS, &kickResult)); err != nil || kickResult == "no api" {
			log.Warn().Str("kick", kickResult).Msg("zona: loader.js never initialised")
			return nil, false
		}
	}
	if kickResult != "called" {
		log.Warn().Str("kick", kickResult).Msg("zona: getStreams kick failed")
		return nil, false
	}

	// Step 3: poll for chunks. Zona's loader.js fires onCompletion when ALL
	// extractors finish — but we'd rather not wait on a stuck extractor
	// (e.g. FILMIX hanging for 60 s) when we already have usable streams.
	//
	// Heuristics:
	//  1. window.__zona_done == true → onCompletion fired, we're done. (preferred)
	//  2. ≥2 USABLE chunks AND no new chunks for stableWindow (5 s) → the
	//     remaining extractors are probably FILMIX retries; bail early.
	//  3. chunks stabilised for stableIdle (12 s) → nothing new coming.
	//  4. pollBudget elapsed (hard cap at 30 s).
	//
	// The wider stableWindow is the key fix: MOBILINK usually arrives
	// within 2-3 s, HDVB within 6-10 s. Waiting 5 s after the first usable
	// chunk gives HDVB room to arrive before we bail.
	const (
		stableWindow = 5 * time.Second
		stableIdle   = 12 * time.Second
		minUsable    = 2
	)
	pollDeadline := time.Now().Add(zonaPollBudget)
	lastLen := -1
	lastChange := time.Now()
	for time.Now().Before(pollDeadline) {
		var raw string
		if err := chromedp.Run(workCtx, chromedp.Evaluate(`(function(){
			var arr = window.__zona_streams || [];
			var usable = 0;
			for (var i = 0; i < arr.length; i++) {
				try {
					var o = JSON.parse(arr[i]);
					if (o.error) continue;
					var vs = o.videoStreams || [];
					if (vs.length === 0) continue;
					if (vs[0].parameters && vs[0].parameters.urlTransform) continue;
					usable++;
				} catch(e) {}
			}
			return JSON.stringify({length:arr.length, usable:usable, done:!!window.__zona_done});
		})()`, &raw)); err != nil {
			break
		}
		var state struct {
			Length int
			Usable int
			Done   bool
		}
		_ = stdjson.Unmarshal([]byte(raw), &state)
		// Preferred exit: loader.js fired onCompletion.
		if state.Done {
			break
		}
		if state.Length != lastLen {
			lastLen = state.Length
			lastChange = time.Now()
		}
		// Early bail when we already have enough usable chunks and nothing
		// new has arrived recently. Saves ~10 s on the cold path.
		if state.Usable >= minUsable && time.Since(lastChange) >= stableWindow {
			break
		}
		// Slow path: any chunk arrived but nothing new for stableIdle.
		if state.Length > 0 && time.Since(lastChange) >= stableIdle {
			break
		}
		if err := chromedp.Run(workCtx, chromedp.Sleep(700*time.Millisecond)); err != nil {
			break
		}
	}

	// Step 4: read results. Use a fresh context (NOT workCtx) so that even
	// if the poll consumed the entire tabCtx budget we can still pull
	// whatever streams have already been pushed into window.__zona_streams.
	return z.dumpStreamsPartial(tabCtx, kpID, episodeKey, "")
}

// dumpStreamsPartial reads window.__zona_streams from the given tab context
// using a short dedicated budget. Returns the accumulated chunks, logging
// the final stream count. The partialReason parameter is used for debug
// logging when dump is called from an error path (poll timeout etc.).
func (z *zonaChecker) dumpStreamsPartial(tabCtx context.Context, kpID int64, episodeKey, partialReason string) ([]string, bool) {
	dumpCtx, dumpCancel := context.WithTimeout(tabCtx, zonaDumpBudget)
	defer dumpCancel()

	// Pull dump + metadata (done flag, onCompletion count, extractor
	// breakdown) in one Evaluate so logs show exactly what finished.
	var raw string
	if err := chromedp.Run(dumpCtx, chromedp.Evaluate(`(function(){
		var arr = window.__zona_streams || [];
		var kinds = [];
		for (var i = 0; i < arr.length; i++) {
			try {
				var o = JSON.parse(arr[i]);
				var name = (o.extractorType && o.extractorType.name) || '?';
				var err = o.error || '';
				var ok = Array.isArray(o.videoStreams) ? o.videoStreams.length : 0;
				var needTransform = false;
				if (ok > 0) {
					var first = o.videoStreams[0];
					needTransform = !!(first && first.parameters && first.parameters.urlTransform);
				}
				kinds.push({name:name, err:err, n:ok, tx:needTransform});
			} catch (e) {
				kinds.push({name:'?', err:'parse:'+e.message});
			}
		}
		return JSON.stringify({
			done: !!window.__zona_done,
			count: window.__zona_count | 0,
			err: window.__zona_err || '',
			kinds: kinds,
			chunks: arr
		});
	})()`, &raw)); err != nil {
		log.Warn().Err(err).Str("reason", partialReason).Int64("kp", kpID).Msg("zona: streams dump failed")
		return nil, false
	}
	var payload struct {
		Done   bool     `json:"done"`
		Count  int      `json:"count"`
		Err    string   `json:"err"`
		Kinds  []any    `json:"kinds"`
		Chunks []string `json:"chunks"`
	}
	if err := stdjson.Unmarshal([]byte(raw), &payload); err != nil {
		log.Warn().Err(err).Int("len", len(raw)).Msg("zona: dump parse failed")
		return nil, false
	}
	ev := log.Info().
		Int64("kp", kpID).
		Str("ep", episodeKey).
		Bool("done", payload.Done).
		Int("completion_count", payload.Count).
		Int("chunks", len(payload.Chunks)).
		Interface("kinds", payload.Kinds)
	if payload.Err != "" {
		ev = ev.Str("js_err", payload.Err)
	}
	if partialReason != "" {
		ev = ev.Str("partial", partialReason)
	}
	ev.Msg("zona: streams resolved")
	if len(payload.Chunks) == 0 {
		return nil, false
	}
	return payload.Chunks, true
}

// zonaGetBrowserCtx returns the persistent browser-root chromedp context
// for the current allocator. On first call it creates the allocator (via
// zonaGetAllocator), wraps it in a browser context, and boots Chrome via
// a no-op Run. Subsequent calls return the cached browser context — all
// resolves spawn their own tabs from it via chromedp.NewContext.
func zonaGetBrowserCtx(port int) (context.Context, error) {
	allocCtx := zonaGetAllocator(port)
	if allocCtx == nil {
		return nil, fmt.Errorf("zona: no allocator")
	}

	zonaBrowser.mu.Lock()
	defer zonaBrowser.mu.Unlock()

	if zonaBrowser.browserCtx != nil && zonaBrowser.browserCtx.Err() == nil {
		return zonaBrowser.browserCtx, nil
	}
	if zonaBrowser.browserCancel != nil {
		zonaBrowser.browserCancel()
	}
	zonaBrowser.browserCtx = nil
	zonaBrowser.browserCancel = nil

	browserCtx, browserCancel := chromedp.NewContext(allocCtx)
	// NB: we deliberately do NOT boot Chrome here with a throwaway
	// chromedp.Run. Running on a child of browserCtx with a deadline seems
	// to make chromedp tear down the browser when the child cancels, even
	// though the browser context itself is still alive. The first real
	// resolve pays the cold-start cost via its own tab context; subsequent
	// resolves reuse the running Chrome.
	zonaBrowser.browserCtx = browserCtx
	zonaBrowser.browserCancel = browserCancel
	log.Info().Msg("zona: browser context cached (lazy init)")
	return browserCtx, nil
}

// zonaGetAllocator returns a cached allocator suitable for the requested
// CDP port. If the remote Chrome is reachable it wraps it via
// NewRemoteAllocator; otherwise it lazily creates a local ExecAllocator
// ONCE (via sync.Once) and keeps it alive for the lifetime of the process.
// A failed navigate does NOT tear down the allocator — subsequent calls
// just get a fresh tab from the same browser, which keeps cold-start to
// a single event per lampac run.
func zonaGetAllocator(port int) context.Context {
	zonaBrowser.mu.Lock()
	defer zonaBrowser.mu.Unlock()

	// Reuse cached allocator if the port matches and it's still alive.
	if zonaBrowser.allocCtx != nil && zonaBrowser.allocCtx.Err() == nil && zonaBrowser.port == port {
		return zonaBrowser.allocCtx
	}

	// Port changed — tear down the old one (only happens if admin switches
	// chrome_port at runtime, which is rare).
	if zonaBrowser.allocCtx != nil && zonaBrowser.port != port {
		if zonaBrowser.allocStop != nil {
			zonaBrowser.allocStop()
		}
		zonaBrowser.allocCtx = nil
		zonaBrowser.allocStop = nil
		zonaBrowser.port = 0
		zonaBrowser.wsURL = ""
		zonaBrowser.localOnce = sync.Once{}
	}

	// Try remote Chrome first.
	if wsURL, ok := zonaProbeRemoteChrome(port); ok {
		allocCtx, allocCancel := chromedp.NewRemoteAllocator(context.Background(), wsURL)
		zonaBrowser.allocCtx = allocCtx
		zonaBrowser.allocStop = allocCancel
		zonaBrowser.port = port
		zonaBrowser.wsURL = wsURL
		log.Info().Str("ws", wsURL).Msg("zona: connected to remote Chrome")
		return allocCtx
	}

	// Fall back to local headless Chrome. Create ExecAllocator ONCE per
	// process — see the comment on zonaBrowser.localOnce. We keep
	// zonaBrowser.port set to the requested CDP port so the "is this
	// allocator for the requested port" comparison at the top still works
	// on subsequent calls. isLocal distinguishes the flavour for logging.
	zonaBrowser.localOnce.Do(func() {
		opts, userDataDir := zonaLocalChromeOpts()
		log.Info().Str("userDataDir", userDataDir).Int("remote_port", port).Msg("zona: remote Chrome unreachable, launching local headless Chrome")
		allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
		zonaBrowser.allocCtx = allocCtx
		zonaBrowser.allocStop = allocCancel
		zonaBrowser.port = port
		zonaBrowser.isLocal = true
	})
	return zonaBrowser.allocCtx
}

// zonaProbeRemoteChrome performs a GET /json/version on the CDP port and
// returns the resolved WebSocketDebuggerUrl if reachable.
func zonaProbeRemoteChrome(port int) (string, bool) {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json/version", port))
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", false
	}
	var ver struct {
		WS string `json:"webSocketDebuggerUrl"`
	}
	if err := stdjson.NewDecoder(resp.Body).Decode(&ver); err != nil || ver.WS == "" {
		return "", false
	}
	return ver.WS, true
}

// zonaResetAllocator tears down the cached allocator (and its browser
// context) so the next call re-runs the probe. Kept for tests that need a
// fresh browser.
func zonaResetAllocator() {
	zonaBrowser.mu.Lock()
	defer zonaBrowser.mu.Unlock()
	if zonaBrowser.browserCancel != nil {
		zonaBrowser.browserCancel()
	}
	zonaBrowser.browserCtx = nil
	zonaBrowser.browserCancel = nil
	if zonaBrowser.allocStop != nil {
		zonaBrowser.allocStop()
	}
	zonaBrowser.allocCtx = nil
	zonaBrowser.allocStop = nil
	zonaBrowser.port = 0
	zonaBrowser.wsURL = ""
	zonaBrowser.isLocal = false
	zonaBrowser.localOnce = sync.Once{}
}

// zonaCleanupOldProfiles removes timestamped profile subdirectories that
// are older than maxAge. Best-effort — failures are logged at debug level
// and do not abort allocator creation.
func zonaCleanupOldProfiles(baseDir string, maxAge time.Duration) {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "p") {
			continue
		}
		full := filepath.Join(baseDir, e.Name())
		info, err := os.Stat(full)
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.RemoveAll(full)
		}
	}
}

// zonaLocalChromeOpts builds ExecAllocator options for a private headless
// Chrome instance. Each call returns a UNIQUE profile directory (timestamp
// suffix) so that zombie Chrome processes from previous runs cannot block
// us via stale SingletonLock files. The parent directory is swept of
// pre-existing timestamped subdirs older than 24 h to bound disk usage.
func zonaLocalChromeOpts() ([]chromedp.ExecAllocatorOption, string) {
	home, _ := os.UserHomeDir()
	baseDir := filepath.Join(home, ".cache", "lampac", "zona-chrome-profiles")
	if home == "" {
		baseDir = "/tmp/zona-chrome-profiles"
	}
	_ = os.MkdirAll(baseDir, 0o700)
	zonaCleanupOldProfiles(baseDir, 24*time.Hour)

	userDataDir := filepath.Join(baseDir, fmt.Sprintf("p%d", time.Now().UnixNano()))
	_ = os.MkdirAll(userDataDir, 0o700)

	// Start from a minimal base — chromedp's DefaultExecAllocatorOptions
	// enables the OLD --headless which Zona's extractor service
	// fingerprints and rejects. We need --headless=new plus the same
	// window-size / site-isolation flags that the dev-probe Chrome used.
	opts := []chromedp.ExecAllocatorOption{
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.Flag("headless", "new"),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-sync", true),
		chromedp.Flag("disable-translate", true),
		chromedp.Flag("mute-audio", true),
		chromedp.Flag("disable-default-apps", true),
		chromedp.Flag("disable-popup-blocking", true),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("disable-features", "IsolateOrigins,site-per-process"),
		chromedp.Flag("window-size", "1280,800"),
		chromedp.Flag("remote-allow-origins", "*"),
		chromedp.UserDataDir(userDataDir),
		chromedp.UserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/147.0.0.0 Safari/537.36"),
		chromedp.ModifyCmdFunc(func(cmd *exec.Cmd) {
			cmd.Env = browser.ChromeProcessEnv(cmd.Env, userDataDir)
		}),
	}
	return opts, userDataDir
}

// zonaParseStreams turns the raw JSON chunks from getStreams into our
// internal zonaStream slice, skipping extractors that require urlTransform
// (TAKEDWN/REZKA variants) and error-marked entries.
func zonaParseStreams(chunks []string) []zonaStream {
	type rawSource struct {
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
	}
	type rawVideo struct {
		Source      rawSource `json:"source"`
		Translation string    `json:"translation"`
		Language    string    `json:"language"`
		Resolution  string    `json:"resolution"`
		Quality     string    `json:"quality"`
		IsTrailer   bool      `json:"isTrailer"`
		Parameters  struct {
			URLTransform string `json:"urlTransform"`
		} `json:"parameters"`
	}
	type rawChunk struct {
		VideoStreams  []rawVideo `json:"videoStreams"`
		ExtractorType struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"extractorType"`
		Error string `json:"error"`
	}

	var out []zonaStream
	for _, raw := range chunks {
		var c rawChunk
		if err := stdjson.Unmarshal([]byte(raw), &c); err != nil {
			continue
		}
		if c.Error != "" {
			continue
		}
		for _, vs := range c.VideoStreams {
			if vs.IsTrailer {
				continue
			}
			if vs.Source.URL == "" {
				continue
			}
			// Skip DASH/MPD — Lampa players prefer HLS / direct mp4.
			lower := strings.ToLower(vs.Source.URL)
			if strings.Contains(lower, ".mpd") {
				continue
			}
			s := zonaStream{
				Extractor:   c.ExtractorType.Name,
				URL:         vs.Source.URL,
				Referer:     zonaPlayerReferer,
				Resolution:  vs.Resolution,
				Quality:     vs.Quality,
				Translation: vs.Translation,
				Headers:     map[string]string{},
			}
			// Copy upstream headers (often just the UA zona wants us to
			// impersonate) — we'll augment them with our own metadata.
			for k, v := range vs.Source.Headers {
				s.Headers[k] = v
			}
			if ua, ok := vs.Source.Headers["User-Agent"]; ok {
				s.UserAgent = ua
			}
			// TAKEDWN / REZKA segments must go through a JS-ported URL
			// cipher — parse the bake-in P and E out of urlTransform and
			// pass them through to the /proxy/ handler as private headers.
			// The X-Zona-Cipher-* headers are stripped before upstream
			// fetch so they never leak to the CDN.
			if vs.Parameters.URLTransform != "" {
				params := proxyapi.ZonaParseCipherParams(vs.Parameters.URLTransform)
				if params.Hours != 0 && params.Alphabet != "" {
					s.Headers[proxyapi.ZonaCipherHoursHeader] = strconv.FormatInt(params.Hours, 10)
					s.Headers[proxyapi.ZonaCipherAlphabetHeader] = params.Alphabet
				} else {
					// Cipher params couldn't be parsed — skip this stream
					// rather than serving URLs that will 410.
					log.Warn().Str("extractor", c.ExtractorType.Name).Msg("zona: urlTransform parse failed, skipping stream")
					continue
				}
			}
			out = append(out, s)
		}
	}
	return out
}
