package httpapi

import (
	"testing"
	"time"

	"lampac-go/internal/httpclient"
)

// resetHTTPAPIGlobals snapshots every package-level mutable global that
// production code writes through and registers a t.Cleanup that restores
// the prior value. Without this, tests that exercise NewServer leak
// `serverRef`, `tgTokenStoreRef`, `pluginTemplateCache`, etc. into every
// subsequent test, causing TestStorageDisabledAndMaxSize to read config
// from the previous test's tempdir (because readFileAny → serverRef →
// readConfigFromTOMLAsJSON) and TestSyncJSHandler* tests to read each
// other's plugin template files from a shared cache.
//
// Call this helper at the top of any test that:
//   - instantiates a Server (NewServer / serverRef = …),
//   - mutates tgTokenStoreRef / groupStoreRef / tsTokenStoreRef directly,
//   - relies on a clean pluginTemplateCache for a fresh on-disk template.
//
// Idempotent — safe to call multiple times. The restore order is the
// reverse of registration, but each global is independent so order
// doesn't matter here.
func resetHTTPAPIGlobals(t *testing.T) {
	t.Helper()

	prevServer := serverRef
	prevGroupStore := groupStoreRef
	prevTGStore := tgTokenStoreRef
	prevTSStore := tsTokenStoreRef
	prevProfile := profileStoreRef

	// Clear the httpclient proxy registries. Sources like vibix and rezka
	// register a residential SOCKS5 proxy on construction, and the first
	// registration also becomes the global ProxiedTransport (first-wins).
	// Since NewProxied() falls back to ProxiedTransport, a leaked registration
	// silently reroutes every later NewProxied-based test (all sisi/* live
	// sources) through a dead SOCKS5 → empty responses. Clearing here (and in
	// Cleanup) keeps each test on a direct transport unless it registers its
	// own proxy.
	httpclient.ClearRegistry()

	// Snapshot the plugin template cache so tests with their own RepoRoot
	// don't share entries. The cache is structured so we can swap the
	// items map under the lock.
	pluginTemplateCache.Lock()
	prevPluginItems := pluginTemplateCache.items
	pluginTemplateCache.items = make(map[string]pluginTemplateCacheEntry, 16)
	pluginTemplateCache.Unlock()

	// Live module-manifest cache (manifest_live.go) — keyed by resolved
	// paths, but reset anyway so tests never observe a previous test's
	// 60s TTL entry.
	liveManifestCache.Lock()
	prevManifestKey, prevManifestMods, prevManifestExp := liveManifestCache.key, liveManifestCache.mods, liveManifestCache.expiresAt
	liveManifestCache.key, liveManifestCache.mods, liveManifestCache.expiresAt = "", nil, time.Time{}
	liveManifestCache.Unlock()

	// nws_events.go warn-once flags: in production they're a real
	// once-per-process latch, but in tests each scenario expects to
	// re-evaluate the warning condition. Snapshot and reset.
	prevWarnNoTG := nwsWarnNoTGRef.Load()
	prevWarnNoDev := nwsWarnNoDevices.Load()
	nwsWarnNoTGRef.Store(false)
	nwsWarnNoDevices.Store(false)

	// Profile rate-limit buckets — see profile_api_test.go for the same
	// reset pattern. httptest re-uses one RemoteAddr per request so
	// successive tests would accumulate hits and trip the 5/min cap.
	profileRateMu.Lock()
	prevRegisterRate := profileRegisterRate
	prevLoginRate := profileLoginRate
	profileRegisterRate = map[string]*profileRateBucket{}
	profileLoginRate = map[string]*profileRateBucket{}
	profileRateMu.Unlock()

	// Reset to nil at start so a test that depends on globals being unset
	// (e.g. TestStorageDisabledAndMaxSize, which expects readFileAny to
	// hit disk rather than serverRef.cfg) sees a clean slate.
	serverRef = nil
	groupStoreRef = nil
	tgTokenStoreRef = nil
	tsTokenStoreRef = nil
	profileStoreRef = nil

	t.Cleanup(func() {
		serverRef = prevServer
		groupStoreRef = prevGroupStore
		tgTokenStoreRef = prevTGStore
		tsTokenStoreRef = prevTSStore
		profileStoreRef = prevProfile

		pluginTemplateCache.Lock()
		pluginTemplateCache.items = prevPluginItems
		pluginTemplateCache.Unlock()

		liveManifestCache.Lock()
		liveManifestCache.key, liveManifestCache.mods, liveManifestCache.expiresAt = prevManifestKey, prevManifestMods, prevManifestExp
		liveManifestCache.Unlock()

		nwsWarnNoTGRef.Store(prevWarnNoTG)
		nwsWarnNoDevices.Store(prevWarnNoDev)

		profileRateMu.Lock()
		profileRegisterRate = prevRegisterRate
		profileLoginRate = prevLoginRate
		profileRateMu.Unlock()

		httpclient.ClearRegistry()
	})

	// A tiny pause lets goroutines spawned by a leaked Server (e.g. its
	// janitors / cleanup tickers) settle before the next test starts
	// allocating tempdirs. 1ms is invisible in CI but prevents flaky
	// races against still-running cleanups. The real fix is for Server
	// to expose a Close() — see TODO in server.go — but until then this
	// is a pragmatic shield.
	time.Sleep(1 * time.Millisecond)
}
