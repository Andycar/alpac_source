// lite_events.go — the /lite/events resolver and its caches.
//
// What you'll find here:
//
//   - liteEventsHandler: thin coordinator that calls resolveEventsPlugins →
//     buildEventItems → maybeLifeMode / maybeChecksearch → write.
//   - eventsResponseCache: serialized response cache, keyed by query+group,
//     versioned so config saves invalidate in O(1) without map wipe.
//   - csCache: per-balancer probe result cache with asymmetric TTL —
//     positive (show=true) 10m, negative 90s, error 30s. Stale negatives
//     used to leave dead balancers visible for 10m after recovery.
//   - csBreaker: per-balancer circuit breaker. Opens on 3 consecutive
//     transport errors. probeEmpty (200 OK + show=false) is NOT counted
//     as failure — it's a legitimate "no content for this title".
//   - lifeTasks: long-running checksearch tasks polled via memkey.
//     Bounded by lifeTaskMax + janitor + TTL.
//
// Concurrency limits are constants at the top of the file:
// checksearchPerRequestConcurrency, checksearchGlobalConcurrency,
// checksearchGlobalAcquireWait, lifeChecksConcurrency.
package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	stdjson "encoding/json"
	"fmt"
	"io"
	"lampac-go/internal/balancerhealth"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/litesrc"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/kit"
	"lampac-go/internal/proxylink"
	"lampac-go/internal/tgauth"

	"golang.org/x/sync/singleflight"

	"github.com/rs/zerolog/log"
)

type liteEventItem struct {
	Name     string  `json:"name"`
	URL      string  `json:"url"`
	Balanser string  `json:"balanser"`
	Show     *bool   `json:"show,omitempty"`
	Rch      *bool   `json:"rch,omitempty"`
	Index    *int    `json:"index,omitempty"`
	Quality  *string `json:"quality,omitempty"` // dynamic quality badge (e.g. "4K", "FHD")
}

const lifeTaskTTL = 5 * time.Minute
const lifeTaskMax = 10000 // hard cap; bursts beyond this drop oldest tasks

type lifeOnlineTask struct {
	createdAt time.Time
	items     []liteEventItem
	done      []bool
}

type lifeTaskStore struct {
	mu    sync.RWMutex
	tasks map[string]*lifeOnlineTask
}

var lifeTasks = &lifeTaskStore{tasks: make(map[string]*lifeOnlineTask)}

// init starts the background janitor for lifeTasks. The store already
// cleans up lazily inside create()/snapshot(), but those only fire on
// active traffic; if a user opens a film and never polls /lite/events
// again, the entry would linger until the next create(). The janitor
// guarantees eviction within ~30s of TTL expiry regardless of traffic.
func init() {
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for range t.C {
			lifeTasks.cleanup()
		}
	}()
}

// checksearch result cache — avoids re-probing the same balancer+film within TTL.
// Asymmetric TTL: positive results (show=true) are stable; negative results
// (show=false) might just mean "balancer was momentarily down" — re-probe
// sooner so users don't see a dead-balancer label for 10 minutes after the
// balancer comes back.
const checksearchCacheTTL = 10 * time.Minute    // positive results
const checksearchCacheNegTTL = 90 * time.Second // negative results (no content found)
// Error TTL kept short — one transient transport blip (CDN edge swap, brief
// rate limit) shouldn't lock the user out of the source for 30s. The earlier
// 30s value caused noticeable "balancer disappears" gaps that users reported
// as the source going dark after a single hiccup.
const checksearchCacheErrorTTL = 10 * time.Second // probe errors (network/HTTP)
const checksearchCacheMax = 10000                 // max entries to prevent unbounded growth

type checksearchCacheEntry struct {
	show      bool
	rch       bool
	quality   string
	fromDrill bool // written by a /capi full drill (setFromDrill), not a checksearch probe
	expires   time.Time
}

type checksearchCache struct {
	mu      sync.RWMutex
	entries map[string]checksearchCacheEntry
}

var csCache = &checksearchCache{entries: make(map[string]checksearchCacheEntry)}

// Per-balancer circuit breaker: after N consecutive HARD failures (network
// error / 5xx / timeout — NOT just "show=false"), skip the balancer entirely
// for `csBreakerCooldown`. Single slow upstream no longer holds the whole
// response — we skip it instantly.
//
// Tuning notes (post-regression 2026-05-17):
//
//	threshold=5 (was 3): some balancers naturally tail past the slow-threshold
//	  for individual films without being broken. Three fails was too eager and
//	  was visibly removing live balancers from the list for 2 minutes.
//	cooldown=30s (was 2m): if we're wrong about the breaker, 30s recovery is
//	  much less painful than 2 minutes of missing sources. Half-open probe is
//	  cheap (one request per balancer per cooldown).
const csBreakerThreshold = 5
const csBreakerCooldown = 30 * time.Second

type csBreakerEntry struct {
	failCount int
	openUntil time.Time
}

type csBreakerStore struct {
	mu      sync.Mutex
	entries map[string]*csBreakerEntry
}

var csBreaker = &csBreakerStore{entries: make(map[string]*csBreakerEntry)}

// isOpen reports whether the breaker is open (should skip) for the balancer.
func (b *csBreakerStore) isOpen(balancer string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.entries[balancer]
	if e == nil {
		return false
	}
	if time.Now().Before(e.openUntil) {
		return true
	}
	// Cooldown elapsed — half-open: clear and let one probe through.
	if !e.openUntil.IsZero() {
		e.openUntil = time.Time{}
		e.failCount = 0
	}
	return false
}

func (b *csBreakerStore) recordFail(balancer string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.entries[balancer]
	if e == nil {
		e = &csBreakerEntry{}
		b.entries[balancer] = e
	}
	e.failCount++
	if e.failCount >= csBreakerThreshold {
		e.openUntil = time.Now().Add(csBreakerCooldown)
	}
}

func (b *csBreakerStore) recordSuccess(balancer string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if e := b.entries[balancer]; e != nil {
		e.failCount = 0
		e.openUntil = time.Time{}
	}
}

// inspect returns a read-only snapshot of the breaker state for the balancer.
// Used by the admin telemetry dashboard.
func (b *csBreakerStore) inspect(balancer string) (open bool, openUntil time.Time, failCount int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.entries[balancer]
	if e == nil {
		return false, time.Time{}, 0
	}
	open = time.Now().Before(e.openUntil)
	return open, e.openUntil, e.failCount
}

// reset clears the breaker for the balancer (used by admin "reset" button).
func (b *csBreakerStore) reset(balancer string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.entries, balancer)
}

// CSBreakerInspect is the public helper the admin telemetry handler uses.
func CSBreakerInspect(balancer string) (open bool, openUntil time.Time, failCount int) {
	return csBreaker.inspect(balancer)
}

// CSBreakerReset clears the breaker for a balancer (admin action).
func CSBreakerReset(balancer string) {
	csBreaker.reset(balancer)
}

// singleflight deduplicates concurrent checksearch probes for the same target.
// Prevents thundering herd when many users open the same film simultaneously.
var csSingleflight singleflight.Group

// checksearchLiteOverride lets tests substitute the in-process /lite handler
// the probes drill into (production uses the memoized capiLiteSource).
// Always nil outside tests.
var checksearchLiteOverride http.HandlerFunc

// csCacheJanitor runs once per minute and drops all expired entries from the
// checksearch cache. The write-path lazy cleanup (every 100 writes) covers
// hot traffic, but on quiet servers expired entries can accumulate for hours
// before the next write trips the threshold. The janitor guarantees eviction
// within ~60 s of TTL expiry regardless of traffic.
func init() {
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for range t.C {
			csCache.evictExpired()
		}
	}()
}

func (c *checksearchCache) evictExpired() {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.entries {
		if now.After(e.expires) {
			delete(c.entries, k)
		}
	}
}

func (c *checksearchCache) get(key string) (show, rch bool, quality string, ok bool) {
	c.mu.RLock()
	e, found := c.entries[key]
	c.mu.RUnlock()
	if !found || time.Now().After(e.expires) {
		return false, false, "", false
	}
	// Drill-negatives are capi-private: a /capi drill treats an unmatched
	// "similar" candidate list as empty, while a human Lampa user can pick a
	// candidate manually — serving that negative here would hide a usable
	// source from the lite client. Drill-POSITIVES are fine (content verified
	// deeper than a probe string-match).
	if e.fromDrill && !e.show {
		return false, false, "", false
	}
	return e.show, e.rch, e.quality, true
}

// setWithTTL stores a checksearch result with a caller-chosen TTL.
// Callers pass the asymmetric TTL constants based on what the probe returned.
func (c *checksearchCache) setWithTTL(key string, show, rch bool, quality string, ttl time.Duration) {
	c.mu.Lock()
	c.storeLocked(key, checksearchCacheEntry{
		show: show, rch: rch, quality: quality,
		expires: time.Now().Add(ttl),
	})
	c.mu.Unlock()
}

// setFromDrill stores an availability verdict derived from a /capi full drill.
// A drill is a stronger signal than a probe (it extracts actual streams and
// retries alt titles), so positives overwrite freely; a drill-NEGATIVE never
// clobbers a live probe-positive — the probe reflects what a Lampa client will
// actually see (e.g. a "similar" candidate list counts as content for a human,
// while the capi drill dropped it for not matching the exact title).
func (c *checksearchCache) setFromDrill(key string, show bool, quality string, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !show {
		if e, ok := c.entries[key]; ok && e.show && !e.fromDrill && time.Now().Before(e.expires) {
			return
		}
	}
	c.storeLocked(key, checksearchCacheEntry{
		show: show, quality: quality, fromDrill: true,
		expires: time.Now().Add(ttl),
	})
}

// drillNegativeActive reports whether a fresh drill-origin negative exists for
// the key — the /capi resolver uses it to skip re-drilling a source it itself
// found empty moments ago. Probe-origin negatives are deliberately NOT
// consulted: a probe misses content the drill's alt-title retry would find.
func (c *checksearchCache) drillNegativeActive(key string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[key]
	return ok && e.fromDrill && !e.show && time.Now().Before(e.expires)
}

func (c *checksearchCache) delete(key string) {
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
}

// storeLocked inserts the entry and runs the periodic size cleanup.
// Caller must hold c.mu.
func (c *checksearchCache) storeLocked(key string, e checksearchCacheEntry) {
	c.entries[key] = e
	// Cleanup: every 100 writes, drop expired entries and enforce max size.
	if len(c.entries)%100 == 0 {
		now := time.Now()
		for k, e := range c.entries {
			if now.After(e.expires) {
				delete(c.entries, k)
			}
		}
		// Hard limit: if still over max, drop oldest half.
		if len(c.entries) > checksearchCacheMax {
			count := 0
			for k := range c.entries {
				delete(c.entries, k)
				count++
				if count >= len(c.entries)/2 {
					break
				}
			}
		}
	}
}

// vokinoSubBalancers defines the sub-balancers exposed when VoKino split mode is enabled.
var vokinoSubBalancers = []struct{ key, display string }{
	{"vokino", "VoKino"},
	{"filmix", "VoKino | Filmix"},
	{"alloha", "VoKino | Alloha"},
	{"hdvb", "VoKino | Hdvb"},
	{"ashdi", "VoKino | Ashdi"},
}

// localCorePlugins lists balancers that have full local Go implementations
// (not just checksearch stubs). When running in LocalCore mode without a
// legacy proxy, only these should be advertised to the client.
var localCorePlugins = map[string]struct{}{
	"kinotochka":   {},
	"kinobase":     {},
	"rezka":        {},
	"rhsprem":      {},
	"ahuerezka":    {},
	"filmix":       {},
	"filmixpro":    {},
	"collaps":      {},
	"collaps-dash": {},
	"lift":         {},
	"redheadsound": {},
	"vdbmovies":    {},
	"ashdi":        {},
	"eneyida":      {},
	"kinogo":       {},
	"kinovod":      {},
	"fancdn":       {},
	"kinoukr":      {},
	"zetflix":      {},
	"cdnmovies":    {},
	"vibix":        {},
	"turbo":        {},
	"zona":         {},
	"videoseed":    {},
	"mirage":       {},
	"rutubemovie":  {},
	"anwap":        {},
	"vkmovie":      {},
	"anilibria":    {},
	"aniliberty":   {}, // full local Go impl (aniliberty.go) + animePlugins member — was missing here
	"animevost":    {},
	"animelib":     {},
	"kodik":        {},
	"animebesst":   {},
	"animedia":     {},
	"moonanime":    {},
	"vokino":       {},
	"videodb":      {},
	"zetflixdb":    {},
	"uakino":       {},
	"kinopub":      {},
	"veoveo":       {},
	"hdvb":         {},
	"animego":      {},
	"getstv":       {},
	"iframevideo":  {},
	"cdnvideohub":  {},
	"kubikvkube":   {},
	"iptvonline":   {},
	"youtube":      {},
	"remux":        {},
	"mirkino":      {},
	"aladdin":      {},
	"pidtor":       {},
	"bamboo":       {},
	"uafilm":       {},
	"vidlink":      {},
	"videasy":      {},
	"hydraflix":    {},
	"twoembed":     {},
	"unimay":       {},
	"starlight":    {},
	"klonfun":      {},
	"uaflix":       {},
	"animeon":      {},
	"mikai":        {},
	"lumex":        {},
	"gencit":       {},
	"femd":         {},
	"kinobadi":     {},
	"alloha":       {},
	"leproduction": {},
	"flixcdn":      {},
	"sakhtv":       {},
}

// balancerNameAliases maps alternate balancer keys to their canonical registry
// name. The config section, admin panel and (user-written) with_search use
// "iremux" — matching [online.iremux] — but the Go route/registry/handler are
// keyed "remux". Without normalisation a with_search entry of "iremux" fails the
// localCorePlugins intersection in resolveEventsPlugins and the source silently
// vanishes from /lite/events. Collapsing aliases to the canonical key lets either
// spelling resolve to the working /lite/remux handler and avoids a duplicate row
// when both spellings are present.
var balancerNameAliases = map[string]string{
	"iremux": "remux",
}

// canonicalBalancerName resolves a balancer alias to its registry key.
func canonicalBalancerName(name string) string {
	if canon, ok := balancerNameAliases[name]; ok {
		return canon
	}
	return name
}

// balancerAliasSpellings returns every spelling (canonical + registered aliases)
// that refers to the same balancer as name. Used so a per-user kit visibility
// entry saved under any spelling (e.g. "iremux", which is what PluginKeyFor and
// the /bkit page use) is honoured when the events pipeline checks the canonical
// key ("remux").
func balancerAliasSpellings(name string) []string {
	canon := canonicalBalancerName(strings.ToLower(strings.TrimSpace(name)))
	spellings := []string{canon}
	for alias, target := range balancerNameAliases {
		if target == canon && alias != canon {
			spellings = append(spellings, alias)
		}
	}
	return spellings
}

// kitVisibleWithAliases resolves per-user kit visibility across all spellings of
// a balancer: visible when ANY spelling is whitelisted/enabled; hidden only when
// the user configured visibility (whitelist mode, or an explicit false) and no
// spelling is enabled. This prevents a whitelist-mode client from burying a
// source the user enabled under a different-but-equivalent name.
func kitVisibleWithAliases(ctx context.Context, plugin string) (visible bool, configured bool) {
	anyConfigured := false
	for _, name := range balancerAliasSpellings(plugin) {
		vis, cfgd := kit.BalancerVisible(ctx, name)
		if cfgd {
			anyConfigured = true
			if vis {
				return true, true
			}
		}
	}
	return false, anyConfigured
}

// pluginQualityBadge maps balancer names to their maximum supported quality badge.
// The badge is appended to the display name shown in the Lampa client UI.
var pluginQualityBadgeMu sync.RWMutex

var pluginQualityBadge = map[string]string{
	"kinotochka":    "4K",
	"kinobase":      "FHD",
	"filmix":        "4K",
	"filmixpro":     "4K",
	"filmixtv":      "4K",
	"collaps":       "FHD",
	"collaps-dash":  "FHD",
	"lift":          "FHD",
	"redheadsound":  "FHD",
	"vdbmovies":     "FHD",
	"eneyida":       "FHD",
	"zetflix":       "FHD",
	"vibix":         "FHD",
	"turbo":         "FHD",
	"zona":          "FHD",
	"zona-mobilink": "SD",
	"zona-hdvb":     "FHD",
	"zona-filmix":   "FHD",
	"zona-takedwn":  "FHD",
	"videoseed":     "FHD",
	"ashdi":         "FHD",
	"kinogo":        "FHD",
	"kinovod":       "FHD",
	"fancdn":        "FHD",
	"kinoukr":       "FHD",
	"rezka":         "FHD",
	"rhsprem":       "FHD",
	// HD, not FHD: the worker's labels are inflated one step (its "1080p" is a
	// 1280x720 file — measured, see ahueRezkaTrueLabel), and the genuinely
	// higher tiers are premium-only stubs we never serve.
	"ahuerezka":   "HD",
	"kinopub":     "4K",
	"alloha":      "4K",
	"lumex":       "FHD",
	"gencit":      "FHD",
	"femd":        "FHD",
	"kinobadi":    "FHD",
	"flixcdn":     "FHD",
	"vcdn":        "FHD",
	"videocdn":    "FHD",
	"hdvb":        "FHD",
	"cdnmovies":   "SD",
	"fxapi":       "4K",
	"mirage":      "4K",
	"rutubemovie": "FHD",
	// No quality ladder at all: one VOD playlist per title, measured 576x432 on
	// older films and ~720 wide on newer ones.
	"anwap":        "SD",
	"vkmovie":      "4K",
	"anilibria":    "FHD",
	"animevost":    "SD",
	"animelib":     "FHD",
	"kodik":        "FHD",
	"animebesst":   "FHD",
	"animedia":     "FHD",
	"moonanime":    "FHD",
	"vokino":       "4K",
	"videodb":      "4K",
	"zetflixdb":    "4K",
	"uakino":       "FHD",
	"animego":      "FHD",
	"getstv":       "FHD",
	"iframevideo":  "FHD",
	"cdnvideohub":  "FHD",
	"kubikvkube":   "FHD",
	"iptvonline":   "FHD",
	"youtube":      "4K",
	"remux":        "SD",
	"mirkino":      "4K",
	"aladdin":      "4K",
	"pidtor":       "4K",
	"bamboo":       "FHD",
	"uafilm":       "FHD",
	"vidlink":      "FHD",
	"videasy":      "FHD",
	"hydraflix":    "FHD",
	"twoembed":     "FHD",
	"unimay":       "FHD",
	"starlight":    "FHD",
	"klonfun":      "FHD",
	"uaflix":       "FHD",
	"animeon":      "FHD",
	"mikai":        "FHD",
	"leproduction": "FHD",
	"sakhtv":       "FHD",
}

func pluginQualityBadgeGet(plugin string) string {
	pluginQualityBadgeMu.RLock()
	badge := pluginQualityBadge[plugin]
	pluginQualityBadgeMu.RUnlock()
	return badge
}

func pluginQualityBadgeSet(plugin, badge string) {
	plugin = strings.TrimSpace(plugin)
	badge = strings.TrimSpace(badge)
	if plugin == "" {
		return
	}
	pluginQualityBadgeMu.Lock()
	if badge == "" {
		delete(pluginQualityBadge, plugin)
	} else {
		pluginQualityBadge[plugin] = badge
	}
	pluginQualityBadgeMu.Unlock()
}

var clarificationPlugins = map[string]struct{}{
	"filmix":       {},
	"filmixtv":     {},
	"fxapi":        {},
	"kinoukr":      {},
	"rezka":        {},
	"rhsprem":      {},
	"redheadsound": {},
	"kinopub":      {},
	"alloha":       {},
	"lumex":        {},
	"vcdn":         {},
	"videocdn":     {},
	"fancdn":       {},
	"kinotochka":   {},
	"remux":        {},
}

var clarificationLanguages = map[string]struct{}{
	"ru": {},
	"ja": {},
	"ko": {},
	"zh": {},
	"cn": {},
}

// animePlugins lists balancer keys that only serve anime content.
// They are hidden when original_language is NOT Asian (ja/ko/zh/cn).
var animePlugins = map[string]struct{}{
	"anilibria":  {},
	"aniliberty": {},
	"animebesst": {},
	"animedia":   {},
	"animevost":  {},
	"animego":    {},
	"animelib":   {},
	"moonanime":  {},
}

// animeLanguages are original_language values that indicate anime/Asian content.
var animeLanguages = map[string]struct{}{
	"ja": {},
	"ko": {},
	"zh": {},
	"cn": {},
}

// eventsResponseCache caches fully computed /lite/events responses (with checksearch results).
// Key: normalized query string (id+title+year+serial+source). TTL: 2 min.
var eventsResponseCache = struct {
	sync.RWMutex
	items map[string]eventsResponseCacheEntry
}{items: make(map[string]eventsResponseCacheEntry, 256)}

type eventsResponseCacheEntry struct {
	body      []byte
	expiresAt time.Time
	version   uint64 // value of eventsResponseCacheVersion at write time
}

const eventsResponseCacheTTL = 10 * time.Minute

// Short TTL for partial responses (deadline hit before all balancers answered).
// 30s is enough that high-traffic movies get steady results from the
// background csCache fills, but short enough that slow-but-honest balancers
// surface on the very next request.
const eventsResponseCachePartialTTL = 30 * time.Second
const eventsResponseCacheMax = 5000

// eventsResponseSF deduplicates concurrent /lite/events checksearch computations.
var eventsResponseSF singleflight.Group

func eventsResponseCacheKey(q url.Values) string {
	// Build a deterministic cache key from the query parameters that affect the response.
	var sb strings.Builder
	for _, k := range []string{"id", "imdb_id", "kinopoisk_id", "tmdb_id", "title", "original_title", "year", "serial", "source", "original_language", "checksearch", "rchtype"} {
		if v := q.Get(k); v != "" {
			sb.WriteString(k)
			sb.WriteByte('=')
			sb.WriteString(v)
			sb.WriteByte('&')
		}
	}
	return sb.String()
}

// liteEventsHandler is the entry point for /lite/events. The flow is split
// into discrete stages so each step can be reasoned about (and tested)
// independently:
//
//	resolvePlugins → buildItems → maybeLifeMode → maybeChecksearch → write
//
// The handler itself is now a thin coordinator; the heavy lifting lives in
// the resolve*/build*/run* helpers below.
func liteEventsHandler(cfg config.Config, proxyLinks *proxylink.Manager, dynRoutes *DynamicRouteRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Use live config so balancer changes from admin panel take effect immediately.
		if serverReady() {
			cfg = liveConfig(config.Config{})
		}

		// Source-discovery gate was rolled back 2026-05-28: the auth flow
		// for cross-origin Lampa installs couldn't be reliably detected
		// on /lite/events, and the synthetic banner produced "Ошибка"
		// instead of a clean banner across Lampa forks. The middle path
		// (per-balancer signed URLs) is deferred. /lite/events is open
		// again — matches pre-2026-05-28 behaviour. The global
		// tgAuthGateMiddleware still applies for cookie-bearing browsers.
		plugins := resolveEventsPlugins(r, cfg, dynRoutes)
		if len(plugins) == 0 {
			writeJSON(w, http.StatusOK, []liteEventItem{})
			return
		}

		originalLanguage := firstLang(r.URL.Query().Get("original_language"))
		checkOnlineSearch := cfg.Online.CheckOnlineSearch && strings.TrimSpace(r.URL.Query().Get("id")) != ""

		items := buildEventItems(r, cfg, plugins, hostFromRequest(r), originalLanguage, checkOnlineSearch, dynRoutes)

		if parseBoolParam(r.URL.Query().Get("life")) {
			writeLifeEventsResponse(w, cfg, proxyLinks, dynRoutes, items, r.URL.Query(), checkOnlineSearch)
			return
		}

		if checkOnlineSearch && len(items) > 0 {
			writeChecksearchEventsResponse(w, r, cfg, proxyLinks, dynRoutes, items)
			return
		}

		writeJSON(w, http.StatusOK, items)
	}
}

// resolveEventsPlugins returns the final ordered list of plugin keys to
// surface to the client. It applies, in order:
//   - intersection with localCorePlugins (drop anything we can't actually serve)
//   - additive merge of dynamic-route custom balancers
//   - additive merge of kit-explicitly-enabled balancers (respecting group deny)
//   - default sort (group → quality → known-balancer index) when CustomOrder is off
func resolveEventsPlugins(r *http.Request, cfg config.Config, dynRoutes *DynamicRouteRegistry) []string {
	plugins := cfg.Online.WithSearch

	combined := make(map[string]struct{}, len(localCorePlugins)+8)
	maps.Copy(combined, localCorePlugins)
	dynSet := map[string]struct{}{}
	if dynRoutes != nil {
		for _, name := range dynRoutes.Names() {
			lp := strings.ToLower(name)
			combined[lp] = struct{}{}
			dynSet[lp] = struct{}{}
		}
	}

	seen := make(map[string]struct{}, len(plugins))
	filtered := make([]string, 0, len(plugins))
	// Keep with_search entries that have Go implementations. Alias spellings
	// (e.g. "iremux" → "remux") are normalised to the canonical registry key so
	// they survive the intersection and resolve to the right /lite/<name> route.
	for _, p := range plugins {
		lp := canonicalBalancerName(strings.ToLower(strings.TrimSpace(p)))
		if _, ok := combined[lp]; ok {
			if _, dup := seen[lp]; dup {
				continue
			}
			filtered = append(filtered, lp)
			seen[lp] = struct{}{}
		}
	}
	// Auto-add only dynamically registered custom balancers not in with_search.
	for p := range dynSet {
		if _, ok := seen[p]; !ok {
			filtered = append(filtered, p)
			seen[p] = struct{}{}
		}
	}
	// Additively include balancers the user has explicitly enabled in their
	// kit visibility map but which the admin omitted from with_search.
	// Admin global-disable and group-deny both beat kit-include.
	var groupForKit interface{ BalancerAllowed(string) bool }
	if groupFilteringActive() {
		if g := resolveUserGroup(r); g != nil {
			groupForKit = g
		}
	}
	for rawKit := range kit.BalancersExplicitlyEnabled(r.Context()) {
		// Normalise alias spellings here too (a client that whitelists
		// "iremux" must resolve to the canonical "remux" registry key).
		p := canonicalBalancerName(rawKit)
		if _, alreadyIn := seen[p]; alreadyIn {
			continue
		}
		if _, isLocal := combined[p]; !isLocal {
			continue // unknown balancer — don't make up routes
		}
		// Admin global-disable is absolute: a source the admin turned off must
		// never be resurfaced by a user's personal kit-enable. eventPluginVisible
		// enforces this downstream too, but excluding it here keeps the resolved
		// list itself honest for EVERY consumer (/lite/events, capi, admin preview).
		if isBalancerDisabled(p) {
			continue
		}
		if groupForKit != nil && !groupForKit.BalancerAllowed(p) {
			continue
		}
		filtered = append(filtered, p)
		seen[p] = struct{}{}
	}

	if !cfg.Online.CustomOrder {
		sortPluginsByDefault(filtered)
	}
	return filtered
}

// buildEventItems materialises the plugin list into [].liteEventItem ready
// for JSON serialisation. Applies per-plugin filters (filmixpro token,
// disabled flag, anime language gate, group deny, kit deny) and expands
// vokino/zona split modes.
func buildEventItems(r *http.Request, cfg config.Config, plugins []string, host, originalLanguage string, checkOnlineSearch bool, dynRoutes *DynamicRouteRegistry) []liteEventItem {
	items := make([]liteEventItem, 0, len(plugins))
	seen := make(map[string]struct{}, len(plugins))

	for _, rawPlugin := range plugins {
		plugin := strings.ToLower(strings.TrimSpace(rawPlugin))
		if plugin == "" {
			continue
		}
		if _, ok := seen[plugin]; ok {
			continue
		}

		// A source backed by a dynamic route (JS module / custom balancer
		// subprocess) is treated as user-installed: it must not be hidden by
		// kit whitelist-mode auto-detection (see eventPluginVisible).
		isDynamic := false
		if dynRoutes != nil {
			if _, ok := dynRoutes.Lookup(plugin); ok {
				isDynamic = true
			}
		}

		if !eventPluginVisible(r, cfg, plugin, originalLanguage, isDynamic) {
			continue
		}
		seen[plugin] = struct{}{}

		// VoKino split mode.
		if plugin == "vokino" && cfg.Online.Vokino.SplitBalancers {
			items = appendVokinoSplitItems(items, host, checkOnlineSearch)
			continue
		}
		// Zona split mode.
		if plugin == "zona" && cfg.Online.Zona.SplitBalancers {
			items = appendZonaSplitItems(items, host, checkOnlineSearch)
			continue
		}

		items = append(items, makeEventItem(plugin, host, originalLanguage, len(items), checkOnlineSearch))
	}
	return items
}

// eventPluginVisible centralises the per-plugin "should we surface this?"
// checks. Returns true when the plugin is allowed for this request.
//
// isDynamic marks sources backed by a dynamic route (JS module / custom
// balancer). Those bypass kit whitelist-mode auto-hiding: they are only hidden
// when the user explicitly set them to false, since a dynamic source installed
// after the user saved their /bkit preferences would otherwise be buried by
// whitelist mode despite being enabled in the admin panel.
func eventPluginVisible(r *http.Request, cfg config.Config, plugin, originalLanguage string, isDynamic bool) bool {
	// filmixpro requires a token and pro mode.
	if plugin == "filmixpro" && (strings.TrimSpace(cfg.Online.Filmix.Token) == "" || !cfg.Online.Filmix.Pro) {
		return false
	}
	// Admin-disabled or auto-disabled by healthcheck.
	if isBalancerDisabled(plugin) {
		return false
	}
	// Anime balancers hidden for non-anime content.
	if _, isAnime := animePlugins[plugin]; isAnime && originalLanguage != "" {
		if _, isAnimeLang := animeLanguages[originalLanguage]; !isAnimeLang {
			return false
		}
	}
	// Group deny — a Lampa-client concept, like the kit whitelist below.
	//
	// Skipped for a /capi aggregation drill so that `resolve_sources` is the
	// SINGLE authority for what alpac tv may serve. Two reasons:
	//
	//  1. Consistency. liteSourceHandler already exempts capi drills from the
	//     same check, so without this the two gates disagree: the drill is let
	//     through at the route but its item never makes it into the list, and
	//     the source is silently absent instead of denied.
	//  2. It makes "premium-only in Lampa, available to everyone on TV" a thing
	//     the admin can express: keep the source out of the group whitelist and
	//     list it in resolve_sources. Access to /capi as a whole stays gated by
	//     capi.require_premium.
	//
	// Granularity note: the lever is resolve_sources, not the group. Anything
	// listed there is offered to every TV client regardless of group.
	if !capiResolveRequest(r) && groupFilteringActive() {
		if group := resolveUserGroup(r); group != nil && !group.BalancerAllowed(plugin) {
			return false
		}
	}
	// Per-user kit visibility — a Lampa-client concept (each user picks which sources THEY see).
	// It must NOT apply to a /capi aggregation drill: there the admin's `resolve_sources` is the
	// authority, and a per-user Lampa whitelist would silently hide a source the admin explicitly
	// listed (e.g. a freshly-enabled built-in like ahuerezka that postdates the saved map).
	if !capiResolveRequest(r) {
		if isDynamic {
			// Dynamic sources (JS modules, custom balancers) are opt-out, not
			// opt-in: hide only when the user explicitly set them to false. This
			// keeps a source the user just enabled in the admin panel from being
			// swallowed by whitelist mode just because it postdates their saved
			// _balancerVisibility map.
			if kit.BalancerExplicitlyHidden(r.Context(), plugin) {
				return false
			}
		} else if vis, configured := kitVisibleWithAliases(r.Context(), plugin); configured && !vis {
			return false
		}
	}
	return true
}

// makeEventItem builds a single liteEventItem with the optional checksearch
// scaffolding (show/rch/index pointers when CheckOnlineSearch is active).
func makeEventItem(plugin, host, originalLanguage string, idx int, checkOnlineSearch bool) liteEventItem {
	url := host + "/lite/" + plugin
	if needClarification(originalLanguage, plugin) {
		url += "?clarification=1"
	}
	item := liteEventItem{
		Name:     pluginDisplayName(plugin),
		URL:      url,
		Balanser: plugin,
	}
	if checkOnlineSearch {
		show := false
		rch := false
		i := idx
		item.Show = &show
		item.Rch = &rch
		item.Index = &i
	}
	return item
}

// appendVokinoSplitItems expands the vokino balancer into one row per
// sub-source (Filmix/Alloha/Hdvb/Ashdi/raw). Used only when
// cfg.Online.Vokino.SplitBalancers is true.
func appendVokinoSplitItems(items []liteEventItem, host string, checkOnlineSearch bool) []liteEventItem {
	for _, sub := range vokinoSubBalancers {
		badge := pluginQualityBadgeGet(sub.key)
		if badge == "" {
			badge = pluginQualityBadgeGet("vokino")
		}
		name := sub.display
		if badge != "" {
			name += " 「" + badge + "」"
		}
		item := liteEventItem{
			Name:     name,
			URL:      host + "/lite/vokino?balancer=" + sub.key,
			Balanser: "vokino|" + sub.key,
		}
		if checkOnlineSearch {
			show := false
			rch := false
			idx := len(items)
			item.Show = &show
			item.Rch = &rch
			item.Index = &idx
		}
		items = append(items, item)
	}
	return items
}

// appendZonaSplitItems expands the zona aggregator into one row per supported
// extractor (MOBILINK/HDVB; FILMIX/TAKEDWN skipped — can't play). Used only
// when cfg.Online.Zona.SplitBalancers is true.
func appendZonaSplitItems(items []liteEventItem, host string, checkOnlineSearch bool) []liteEventItem {
	for _, sub := range litesrc.ZonaSubBalancers {
		if sub.Extractor == "" || !sub.Supported {
			continue
		}
		badge := pluginQualityBadgeGet(sub.Key)
		if badge == "" {
			badge = pluginQualityBadgeGet("zona")
		}
		name := sub.Display
		if badge != "" {
			name += " 「" + badge + "」"
		}
		item := liteEventItem{
			Name:     name,
			URL:      host + "/lite/" + sub.Key,
			Balanser: "zona|" + sub.Extractor,
		}
		if checkOnlineSearch {
			show := false
			rch := false
			idx := len(items)
			item.Show = &show
			item.Rch = &rch
			item.Index = &idx
		}
		items = append(items, item)
	}
	return items
}

// writeLifeEventsResponse handles the life=1 path. Returns either the plain
// items array (when there's nothing to poll for) or a {life, memkey} object
// that the client polls via /lite/events?memkey=…
//
// IMPORTANT: when checkOnlineSearch is off we MUST return the array form —
// online.js treats falsy json.life as a direct array and crashes on
// .forEach if it sees an object instead.
func writeLifeEventsResponse(w http.ResponseWriter, cfg config.Config, proxyLinks *proxylink.Manager, dynRoutes *DynamicRouteRegistry, items []liteEventItem, query url.Values, checkOnlineSearch bool) {
	if !checkOnlineSearch || len(items) == 0 {
		writeJSON(w, http.StatusOK, items)
		return
	}
	memkey := startLifeChecks(cfg, proxyLinks, dynRoutes, items, query)
	writeJSON(w, http.StatusOK, map[string]any{
		"life":   true,
		"memkey": memkey,
	})
}

// writeChecksearchEventsResponse handles the checksearch=true path: cache
// lookup, singleflight, probe fan-out, sort, serialize, cache write.
// Splitting this out makes the cache-key/group-suffix interaction much
// easier to reason about — the cache key MUST include the group ID so two
// users with different balancer allowlists don't poison each other.
func writeChecksearchEventsResponse(w http.ResponseWriter, r *http.Request, cfg config.Config, proxyLinks *proxylink.Manager, dynRoutes *DynamicRouteRegistry, items []liteEventItem) {
	cacheKey := eventsResponseCacheKey(r.URL.Query())
	// Include resolved group ID so different group memberships don't share
	// cache entries. Anonymous requests get an explicit "|g=" suffix so they
	// can't collide with a named group either.
	groupSuffix := "|g="
	if group := resolveUserGroup(r); group != nil {
		groupSuffix += group.ID
	}
	cacheKey += groupSuffix

	// Try cache first. Entry is stale if its version is older than the
	// current global version (admin saved config since).
	eventsResponseCache.RLock()
	currentVersion := eventsResponseCacheVersion
	ce, ok := eventsResponseCache.items[cacheKey]
	eventsResponseCache.RUnlock()
	if ok && time.Now().Before(ce.expiresAt) && ce.version == currentVersion {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("X-Cache", "hit")
		w.WriteHeader(http.StatusOK)
		w.Write(ce.body)
		return
	}

	// Singleflight: only one goroutine probes for the same (query, group).
	result, _, _ := eventsResponseSF.Do(cacheKey, func() (any, error) {
		complete := applyCheckOnlineSearch(r.Context(), cfg, proxyLinks, dynRoutes, items, r.URL.Query())
		sortItemsByShowAndQuality(items, !cfg.Online.CustomOrder)
		body, _ := stdjson.Marshal(items)
		storeEventsResponseCache(cacheKey, body, !complete)
		return body, nil
	})
	body := result.([]byte)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}

// sortItemsByShowAndQuality re-orders items so:
//  1. show=true comes before show=false
//  2. within "show=true" rows, sort by quality rank (when sortByQuality is on)
//  3. otherwise preserve the original index order
func sortItemsByShowAndQuality(items []liteEventItem, sortByQuality bool) {
	sort.Slice(items, func(i, j int) bool {
		is := itemShow(items[i])
		js := itemShow(items[j])
		if is != js {
			return is && !js
		}
		if sortByQuality && is && js {
			iq := qualityRank(items[i].Quality)
			jq := qualityRank(items[j].Quality)
			if iq != jq {
				return iq > jq
			}
		}
		return itemIndex(items[i]) < itemIndex(items[j])
	})
}

// storeEventsResponseCache writes a serialized response to the events cache,
// evicting expired and version-stale entries when over capacity.
// `partial` selects the short TTL (30s) used when the probe phase hit its
// wall-clock deadline before all balancers answered — see runCheckOnlineSearch.
func storeEventsResponseCache(key string, body []byte, partial bool) {
	eventsResponseCache.Lock()
	defer eventsResponseCache.Unlock()
	if len(eventsResponseCache.items) >= eventsResponseCacheMax {
		now := time.Now()
		curVer := eventsResponseCacheVersion
		for k, v := range eventsResponseCache.items {
			if now.After(v.expiresAt) || v.version != curVer {
				delete(eventsResponseCache.items, k)
			}
		}
	}
	ttl := eventsResponseCacheTTL
	if partial {
		ttl = eventsResponseCachePartialTTL
	}
	eventsResponseCache.items[key] = eventsResponseCacheEntry{
		body:      body,
		expiresAt: time.Now().Add(ttl),
		version:   eventsResponseCacheVersion,
	}
}

func lifeEventsHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		memkey := strings.TrimSpace(r.URL.Query().Get("memkey"))
		if memkey != "" {
			if ready, tasks, online, ok := lifeTasks.snapshot(memkey); ok {
				writeJSON(w, http.StatusOK, map[string]any{
					"ready":  ready,
					"tasks":  tasks,
					"online": online,
				})
				return
			}
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"ready":  false,
			"tasks":  0,
			"online": []any{},
		})
	}
}

func parseBoolParam(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func firstLang(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if i := strings.Index(v, "|"); i > 0 {
		v = v[:i]
	}
	return strings.ToLower(strings.TrimSpace(v))
}

// normalizeQualityBadge converts various quality strings (HDRip, 1080p, 4K, WEBRip, etc.)
// to standard badges: "4K", "FHD", "HD", "SD". Returns "" for unrecognized input.
func normalizeQualityBadge(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	up := strings.ToUpper(raw)

	switch up {
	case "4K", "UHD", "2160P", "2160", "ULTRA HD", "ULTRA":
		return "4K"
	case "2K", "QHD", "WQHD", "1440P", "1440":
		return "2K"
	case "FHD", "FULLHD", "FULL HD", "1080P", "1080":
		return "FHD"
	case "HD", "720P", "720":
		return "HD"
	case "SD", "480P", "360P", "240P", "480", "360", "240":
		return "SD"
	}

	if strings.Contains(up, "2160") || strings.Contains(up, "4K") || strings.Contains(up, "UHD") {
		return "4K"
	}
	if strings.Contains(up, "1440") || strings.Contains(up, "QHD") {
		return "2K"
	}
	if strings.Contains(up, "1080") {
		return "FHD"
	}
	if strings.Contains(up, "720") {
		return "HD"
	}

	if strings.Contains(up, "BDRIP") || strings.Contains(up, "BDREMUX") || strings.Contains(up, "BLURAY") || strings.Contains(up, "BLU-RAY") {
		return "FHD"
	}
	if strings.Contains(up, "HDRIP") || strings.Contains(up, "WEBRIP") || strings.Contains(up, "WEB-DL") || strings.Contains(up, "WEBDL") {
		return "HD"
	}
	if strings.Contains(up, "DVDRIP") || strings.Contains(up, "TVRIP") || strings.Contains(up, "CAMRIP") || strings.Contains(up, "CAM") {
		return "SD"
	}

	return ""
}

// qualityBadgeRank returns a numeric rank for standard badges: 4K=4, FHD=3, HD=2, SD=1, ""=0.
func qualityBadgeRank(badge string) int {
	switch badge {
	case "4K":
		return 5
	case "2K":
		return 4
	case "FHD":
		return 3
	case "HD":
		return 2
	case "SD":
		return 1
	default:
		return 0
	}
}

// pluginPrettyNames maps internal plugin keys to user-facing display names.
// If a plugin is not in this map, the raw key is shown (e.g. "filmix").
var pluginPrettyNames = map[string]string{
	"rezka":     "PidoRezka",
	"rhsprem":   "RHS Премиум",
	"ahuerezka": "AhueRezka",
	"remux":     "iRemux",
	"iremux":    "iRemux",
	"mirkino":   "Мир Кино",
	"anwap":     "Anwap",
	"zetflixdb": "ZetflixDB",
	"uakino":    "UaKino (Українською)",
}

func pluginDisplayName(plugin string) string {
	name := plugin
	if pretty, ok := pluginPrettyNames[plugin]; ok {
		name = pretty
	}
	badge := pluginQualityBadgeGet(plugin)
	if badge == "" {
		return name
	}
	return name + " 「" + badge + "」"
}

// pluginDisplayNameDynamic uses the dynamic quality badge from checksearch response,
// falling back to the static pluginQualityBadge if empty.
func pluginDisplayNameDynamic(plugin string, dynamicQuality string) string {
	name := plugin
	if pretty, ok := pluginPrettyNames[plugin]; ok {
		name = pretty
	}
	badge := dynamicQuality
	if badge == "" {
		badge = pluginQualityBadgeGet(plugin)
	}
	if badge == "" {
		return name
	}
	return name + " 「" + badge + "」"
}

// sanitizeQualityBadge normalizes a balancer-supplied quality string so it
// cannot break the response JSON. Accepts only the canonical badges; falls
// back to "" otherwise (display name then carries no badge).
func sanitizeQualityBadge(raw string) string {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "4K", "FHD", "HD", "SD":
		return strings.ToUpper(strings.TrimSpace(raw))
	}
	return normalizeQualityBadge(raw)
}

// writeCheckSearchResponse writes a standard checksearch JSON response with optional quality.
// Quality is sanitized to the canonical badge set ("4K"/"FHD"/"HD"/"SD") to prevent
// JSON injection from upstream-controlled values.
func writeCheckSearchResponse(w http.ResponseWriter, show bool, quality string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if show {
		q := sanitizeQualityBadge(quality)
		if q != "" {
			_, _ = fmt.Fprintf(w, `{"type":"movie","rch":true,"quality":"%s"}`, q)
		} else {
			_, _ = w.Write([]byte(`{"type":"movie","rch":true}`))
		}
		return
	}
	_, _ = w.Write([]byte(`{"rch":false}`))
}

// writeCheckSearchResponseNoRCH is like writeCheckSearchResponse but sets rch:false.
// Used by balancers that appear in search results without auto-recommendation.
func writeCheckSearchResponseNoRCH(w http.ResponseWriter, show bool, quality string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if show {
		q := sanitizeQualityBadge(quality)
		if q != "" {
			_, _ = fmt.Fprintf(w, `{"type":"movie","rch":false,"quality":"%s"}`, q)
		} else {
			_, _ = w.Write([]byte(`{"type":"movie","rch":false}`))
		}
		return
	}
	_, _ = w.Write([]byte(`{"rch":false}`))
}

func needClarification(lang, plugin string) bool {
	if _, ok := clarificationLanguages[lang]; !ok {
		return false
	}
	_, ok := clarificationPlugins[strings.ToLower(strings.TrimSpace(plugin))]
	return ok
}

// applyCheckOnlineSearch runs checksearch probes for each item and applies the
// results in place. Returns true when the probe phase finished completely
// (every balancer answered within the wall-clock deadline); false when the
// deadline triggered partial results — the caller should use a shorter cache
// TTL so slow balancers re-evaluate quickly on the next request.
func applyCheckOnlineSearch(ctx context.Context, cfg config.Config, proxyLinks *proxylink.Manager, dynRoutes *DynamicRouteRegistry, items []liteEventItem, query url.Values) bool {
	extra := buildCheckSearchQuery(query)
	if len(extra) == 0 {
		return true
	}

	return runCheckOnlineSearch(ctx, cfg, proxyLinks, dynRoutes, items, extra, func(idx int, item liteEventItem) {
		items[idx] = item
	})
}

// Checksearch concurrency caps and waits — extracted as named constants so
// tuning is one-stop and doesn't require grepping for raw integers.
//
//   - PerRequestConcurrency: max probes in flight for ONE /lite/events call.
//     ~25-30 active balancers, so 32 fits the whole set in a single batch.
//   - GlobalConcurrency:     max probes across ALL concurrent requests.
//     Without this, N users × per-request cap = floods. 256 keeps latency
//     low while preventing thundering-herd amplification.
//   - GlobalAcquireWait:     give up acquiring the global slot after this
//     long (returns show=false without penalising the breaker — it's our
//     local capacity that failed, not the upstream).
//   - LifeChecksConcurrency: max background life-check goroutines.
const (
	checksearchPerRequestConcurrency = 32
	checksearchGlobalConcurrency     = 256
	checksearchGlobalAcquireWait     = 2 * time.Second
	lifeChecksConcurrency            = 32
)

// checksearchGlobalSem implements the global cap described above.
var checksearchGlobalSem = make(chan struct{}, checksearchGlobalConcurrency)

// Default checksearch timeout per balancer. Overridable from
// checksearchTimeoutOverride below for known-slow upstreams.
const (
	// 10s per probe (was 6s): users reported live balancers dropping out of
	// the list. Production p95 hovers at 5-7s for healthy-but-slow upstreams
	// (overseas CDN, residential proxy), and 6s was clipping them mid-response.
	// 10s gives enough headroom for honest answers; circuit breaker still
	// catches truly dead balancers within seconds of consecutive hard fails.
	checksearchDefaultTimeout = 10 * time.Second
)

// checksearchTimeoutOverride is intentionally empty after the 2026-05-20
// regression fix. The previous 3-second per-balancer overrides for known-slow
// upstreams (fancdn / videoseed / getstv / moonanime / videodb) were silently
// dropping legitimate hits that arrived at 4-6s — exactly the "пользователи
// говорят что у них есть контент но мы его не находим" complaint.
//
// The default 10s timeout is now applied to every balancer. If a specific
// upstream is observed in telemetry to have a stable p99 > 10s AND zero
// content delivery (broken, not just slow), add it here with a value tighter
// than 10s. Don't add entries based on raw p95 — slow-but-honest sources are
// fine, the user's wait is bounded by the WaitGroup, not by per-balancer.
var checksearchTimeoutOverride = map[string]time.Duration{}

func checksearchTimeout(balancer string) time.Duration {
	if d, ok := checksearchTimeoutOverride[balancer]; ok && d > 0 {
		return d
	}
	return checksearchDefaultTimeout
}

// checksearchTotalDeadline caps the wall-clock time the user waits for the
// /lite/events response. Probes that don't finish by this point are written
// as show=false for this round, BUT they continue running in the background
// (their singleflight.Do goroutine + csCache write keep going). The next
// request for the same movie either hits eventsResponseCache (10min TTL) or
// re-runs runCheckOnlineSearch where slow balancers now hit csCache instantly.
//
// 6s is the sweet spot: long enough for healthy-but-slow upstreams (residential
// proxy, overseas CDN) to honestly answer, short enough that the user doesn't
// stare at "Идёт поиск" for the full 10s per-balancer timeout when one upstream
// is stuck.
const checksearchTotalDeadline = 6 * time.Second

// runCheckOnlineSearch returns true when probes completed without the
// wall-clock deadline firing; false when partial.
func runCheckOnlineSearch(ctx context.Context, cfg config.Config, proxyLinks *proxylink.Manager, dynRoutes *DynamicRouteRegistry, items []liteEventItem, extra url.Values, onResult func(idx int, item liteEventItem)) bool {
	// Fast path: if every probe target is already in csCache (positive or
	// negative), apply the cached values inline and return. Skips the whole
	// goroutine fan-out — common path when a popular movie is re-opened.
	allCached := true
	cachedItems := make([]liteEventItem, len(items))
	for i := range items {
		target := checksearchTargetPath(appendCheckQuery(items[i].URL, extra))
		s, r, q, ok := csCache.get(target)
		if !ok {
			allCached = false
			break
		}
		item := items[i]
		show := s
		rch := r
		indexVal := i
		item.Show = &show
		item.Rch = &rch
		item.Index = &indexVal
		if q != "" {
			qv := q
			item.Quality = &qv
		}
		item.Name = pluginDisplayNameDynamic(items[i].Balanser, q)
		cachedItems[i] = item
	}
	if allCached {
		for i := range items {
			onResult(i, cachedItems[i])
		}
		return true
	}

	// Local probes are served IN-PROCESS via liteSource.ServeHTTP — no loopback
	// TCP, no contention with real client traffic on the accept queue, and no
	// dependency on how the listener is bound. Same mechanism the /capi drill
	// has used all along (capiFetchLite). The handler is memoized per live-config
	// generation, so building it here is a map lookup, not an 80-checker rebuild.
	lite := checksearchLiteOverride
	if lite == nil {
		lite = capiLiteSource(cfg, proxyLinks, dynRoutes)
	}
	// req.Host for the synthetic probe requests — balancers that bake
	// self-referential URLs into responses must resolve back to this server.
	lbHost := loopbackHostPort(cfg.Server.Addr)

	// Bound the whole fan-out with a wall-clock deadline (default 6s). When
	// the deadline fires, the child ctx is cancelled — probes still running
	// see ctx.Done and exit fast (their http.Do returns immediately on cancel,
	// their semaphore acquires give up). The result: the user gets a partial
	// response in at most ~6s + cleanup, instead of waiting for the slowest
	// balancer's full per-probe budget (10s). Slow balancers keep updating
	// csCache in any singleflight workers that already started — next request
	// for the same movie hits the warm cache.
	probesCtx, cancelProbes := context.WithTimeout(ctx, checksearchTotalDeadline)
	defer cancelProbes()

	// Per-request probe cap — see checksearchPerRequestConcurrency for the
	// rationale (we want every balancer probed in a single batch).
	sem := make(chan struct{}, checksearchPerRequestConcurrency)
	var wg sync.WaitGroup

	// Collect healthy remote nodes if cluster is active (primary mode).
	cp := liveClusterPool()
	var remoteNodes []string // node host URLs
	if cp != nil {
		for _, n := range cp.Nodes() {
			if n.IsHealthy() {
				remoteNodes = append(remoteNodes, n.Host)
			}
		}
	}
	clusterAPIKey := ""
	var clusterClient *http.Client // remote-node probes still go over the network
	if len(remoteNodes) > 0 && cp != nil {
		clusterAPIKey = cp.APIKey()
		clusterClient = httpclient.New(checksearchDefaultTimeout)
	}

	for i := range items {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			// Circuit breaker: if this balancer has been failing recently, skip
			// the probe entirely — instant `show=false` instead of a 6s wait.
			// Plain balancer key; vokino|sub splits share the parent's breaker.
			balKey := items[idx].Balanser
			if i := strings.IndexByte(balKey, '|'); i > 0 {
				balKey = balKey[:i]
			}
			if open, breakerOpenUntil, breakerFails := csBreaker.inspect(balKey); open {
				// Say it out loud. This skip produces a plain show=false with no
				// trace anywhere — not in the balancer's own logs, not in the
				// probe telemetry — so a tripped breaker looks exactly like
				// "this source has no content for this card", for every card, until
				// the cooldown expires.
				log.Warn().
					Str("balancer", balKey).
					Time("open_until", breakerOpenUntil).
					Int("fails", breakerFails).
					Msg("checksearch: probe SKIPPED — circuit breaker open (source shows as unavailable without being asked)")
				item := items[idx]
				show := false
				rch := false
				indexVal := idx
				item.Show = &show
				item.Rch = &rch
				item.Index = &indexVal
				onResult(idx, item)
				return
			}

			// Per-request semaphore (max 32 in flight for THIS request).
			select {
			case sem <- struct{}{}:
			case <-probesCtx.Done():
				return
			}
			defer func() { <-sem }()

			// Global semaphore (max 256 in flight across ALL requests). Bounded
			// wait — if we can't acquire within the per-probe timeout, skip.
			select {
			case checksearchGlobalSem <- struct{}{}:
				defer func() { <-checksearchGlobalSem }()
			case <-probesCtx.Done():
				return
			case <-time.After(checksearchGlobalAcquireWait):
				// Global pool saturated for too long — return show=false instead
				// of blocking the user. Don't penalize the breaker — the balancer
				// itself didn't fail, our local capacity did.
				item := items[idx]
				show := false
				rch := false
				indexVal := idx
				item.Show = &show
				item.Rch = &rch
				item.Index = &indexVal
				onResult(idx, item)
				return
			}

			// Probe locally, in-process. Breaker accounting happens INSIDE
			// probeCheckSearch (once per actual probe) — recording it here per
			// caller multiplied one shared singleflight failure by the number
			// of concurrent viewers and opened the breaker after a single bad
			// probe on a popular film.
			target := checksearchTargetPath(appendCheckQuery(items[idx].URL, extra))
			show, rch, quality, _ := probeCheckSearch(probesCtx, lite, lbHost, target, balKey)

			// Probe remote cluster nodes in parallel and merge.
			if len(remoteNodes) > 0 {
				balanser := items[idx].Balanser
				remoteURL := "/lite/" + balanser
				if needClarification(extra.Get("original_language"), balanser) {
					remoteURL += "?clarification=1"
				}
				remoteTarget := appendCheckQuery(remoteURL, extra)

				type nodeResult struct {
					show    bool
					rch     bool
					quality string
				}
				ch := make(chan nodeResult, len(remoteNodes))
				for _, host := range remoteNodes {
					go func(h string) {
						s, r, q := probeClusterNodeCheckSearch(probesCtx, clusterClient, h+remoteTarget, clusterAPIKey)
						ch <- nodeResult{s, r, q}
					}(host)
				}
				// Clamp the remote-reported quality to the maximum advertised
				// for this balancer. Prevents a malicious or buggy remote node
				// from inflating quality (e.g. claiming "4K" for a Collaps
				// stream that's advertised as FHD).
				localCap := qualityBadgeRank(pluginQualityBadgeGet(balKey))
				for range remoteNodes {
					nr := <-ch
					if nr.show {
						show = true
					}
					if nr.rch {
						rch = true
					}
					remoteQ := sanitizeQualityBadge(nr.quality)
					if localCap > 0 && qualityBadgeRank(remoteQ) > localCap {
						remoteQ = pluginQualityBadgeGet(balKey)
					}
					if qualityBadgeRank(remoteQ) > qualityBadgeRank(quality) {
						quality = remoteQ
					}
				}
			}

			item := items[idx]
			showVal := show
			rchVal := rch
			indexVal := idx
			item.Show = &showVal
			item.Rch = &rchVal
			item.Index = &indexVal
			if quality != "" {
				q := quality
				item.Quality = &q
			}
			// Update the display name with dynamic quality badge.
			item.Name = pluginDisplayNameDynamic(items[idx].Balanser, quality)
			onResult(idx, item)
		}(i)
	}

	wg.Wait()

	// Post-wait cleanup: any item whose probe goroutine exited at the deadline
	// (via probesCtx.Done before calling onResult) has Show == nil. Mark those
	// as show=false explicitly so the JSON serialization is consistent and
	// the client renders them greyed-out instead of an unstable mix.
	deadlinePassed := probesCtx.Err() != nil
	if deadlinePassed {
		for i := range items {
			if items[i].Show == nil {
				show := false
				rch := false
				indexVal := i
				items[i].Show = &show
				items[i].Rch = &rch
				items[i].Index = &indexVal
				onResult(i, items[i])
			}
		}
	}
	return !deadlinePassed
}

// probeClusterNodeCheckSearch sends a checksearch probe to a remote cluster node.
func probeClusterNodeCheckSearch(ctx context.Context, client *http.Client, target, apiKey string) (show, rch bool, quality string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return false, false, ""
	}
	req.Header.Set("X-Lampac-Go", "1")
	if apiKey != "" {
		req.Header.Set("X-Cluster-Key", apiKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return false, false, ""
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, false, ""
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return false, false, ""
	}
	return evaluateSearchResult(string(body))
}

var lifeCheckSem = make(chan struct{}, lifeChecksConcurrency)

func startLifeChecks(cfg config.Config, proxyLinks *proxylink.Manager, dynRoutes *DynamicRouteRegistry, items []liteEventItem, query url.Values) string {
	memkey := lifeTasks.create(items)
	extra := buildCheckSearchQuery(query)
	if len(extra) == 0 {
		return memkey
	}

	go func() {
		select {
		case lifeCheckSem <- struct{}{}:
			defer func() { <-lifeCheckSem }()
		default:
			return // too many concurrent life checks, skip
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		runCheckOnlineSearch(ctx, cfg, proxyLinks, dynRoutes, items, extra, func(idx int, item liteEventItem) {
			lifeTasks.update(memkey, idx, item)
		})
	}()

	return memkey
}

// checksearchProbeCtxKey marks in-process availability probes issued by
// runCheckOnlineSearch.
type checksearchProbeCtxKey struct{}

func checksearchProbeContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, checksearchProbeCtxKey{}, true)
}

// isChecksearchProbe reports whether the request is our own availability probe
// rather than a client call.
func isChecksearchProbe(r *http.Request) bool {
	if r == nil {
		return false
	}
	v, _ := r.Context().Value(checksearchProbeCtxKey{}).(bool)
	return v
}

func buildCheckSearchQuery(q url.Values) url.Values {
	keys := []string{
		"id",
		"imdb_id",
		"kinopoisk_id",
		"tmdb_id",
		"title",
		"original_title",
		"original_language",
		"source",
		"year",
		"serial",
		"rchtype",
	}
	out := make(url.Values, len(keys)+1)
	for _, key := range keys {
		val := strings.TrimSpace(q.Get(key))
		if val != "" {
			out.Set(key, val)
		}
	}
	out.Set("checksearch", "true")
	return out
}

func appendCheckQuery(rawURL string, extra url.Values) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := u.Query()
	for key, vals := range extra {
		for _, val := range vals {
			q.Set(key, val)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// probeOutcome classifies the result of a checksearch probe.
//   - probeOk:        upstream returned a valid response (regardless of show)
//   - probeEmpty:     upstream returned a valid response indicating no content
//   - probeErr:       network/transport error or non-2xx HTTP status
//
// The circuit breaker treats probeErr as a failure but NOT probeEmpty —
// "balancer says no content" is a legitimate negative answer, not a failure.
type probeOutcome int

const (
	probeErr probeOutcome = iota
	probeEmpty
	probeOk
)

type csResult struct {
	show    bool
	rch     bool
	quality string
	outcome probeOutcome
	status  int    // HTTP status (0 if no response)
	errMsg  string // truncated error message for telemetry
}

func probeCheckSearch(ctx context.Context, lite http.HandlerFunc, lbHost, target, balancer string) (show bool, rch bool, quality string, outcome probeOutcome) {
	// Check cache first.
	if s, r, q, ok := csCache.get(target); ok {
		if s {
			return s, r, q, probeOk
		}
		// Negative cache hit — we don't know if the original probe was empty
		// or errored, but either way we shouldn't penalize the breaker again.
		return s, r, q, probeEmpty
	}

	// DoChan (not Do): the probe body is DETACHED from the caller's deadline on
	// purpose. When the 6s wall fires, the caller below returns show=false for
	// this round while the probe keeps running — bounded by its own per-balancer
	// budget — and fills csCache, so the next request for the same movie hits
	// the warm cache. (The loopback-HTTP era promised this in comments but the
	// client.Do cancel actually aborted the server-side handler too.)
	// Breaker accounting also lives here: once per actual probe, not per caller.
	ch := csSingleflight.DoChan(target, func() (v any, err error) {
		// The in-process handler runs on OUR stack — a balancer panic must not
		// take the whole probe fan-out (or the process) down. The router's
		// Recoverer middleware is not in this path.
		start := time.Now()
		defer func() {
			if rec := recover(); rec != nil {
				msg := fmt.Sprintf("panic: %v", rec)
				recordChecksearchAttempt(balancer, false, time.Since(start), 0, msg, target)
				csBreaker.recordFail(balancer)
				v = csResult{outcome: probeErr, errMsg: truncErr(msg)}
				err = nil
			}
		}()

		// Double-check cache inside singleflight (another caller may have populated it).
		if s, r, q, ok := csCache.get(target); ok {
			oc := probeEmpty
			if s {
				oc = probeOk
			}
			return csResult{show: s, rch: r, quality: q, outcome: oc}, nil
		}

		// Per-balancer budget from Background, NOT from the caller's ctx — see
		// the DoChan rationale above.
		probeCtx, cancel := context.WithTimeout(context.Background(), checksearchTimeout(balancer))
		defer cancel()

		// Mark the request as an internal availability probe. It carries no user
		// token, so the group gate in liteSourceHandler would resolve it to the
		// DEFAULT group and `{}` every source that group denies — even for users
		// whose own group allows it. That is exactly what kept "Мир Кино" (denied
		// in the default group) permanently greyed out for Premium users: the
		// LIST is built with the caller's rights, the PROBE ran with the default
		// group's. Access itself is already decided upstream — a plugin only
		// reaches this point after resolveEventsPlugins + eventPluginVisible
		// approved it for this caller. Same rationale as the capi-drill and
		// stream-path exemptions there.
		probeCtx = checksearchProbeContext(probeCtx)

		req := httptest.NewRequest(http.MethodGet, target, nil)
		req = req.WithContext(probeCtx)
		if lbHost != "" {
			req.Host = lbHost // self-referential URLs in responses resolve back to this server
		}
		req.Header.Set("X-Lampac-Go", "1")
		req.RemoteAddr = "127.0.0.1:1234" // probes used to arrive over loopback; keep clientIP() identical
		rec := httptest.NewRecorder()
		lite.ServeHTTP(rec, req)
		latency := time.Since(start)

		if rec.Code < 200 || rec.Code >= 300 {
			recordChecksearchAttempt(balancer, false, latency, rec.Code, http.StatusText(rec.Code), target)
			// Cache the failure briefly to avoid hammering a dead balancer,
			// but with a short TTL so it recovers quickly.
			csCache.setWithTTL(target, false, false, "", checksearchCacheErrorTTL)
			csBreaker.recordFail(balancer)
			return csResult{outcome: probeErr, status: rec.Code}, nil
		}

		s, r, q := evaluateSearchResult(rec.Body.String())
		// Tell telemetry the probe succeeded (transport-wise) even if show=false —
		// "no content found" is not a balancer failure. Same for the breaker.
		recordChecksearchAttempt(balancer, true, latency, rec.Code, "", target)
		csBreaker.recordSuccess(balancer)

		// REGRESSION FIX 2026-05-20 (still in force): never hide a successful
		// response, even a slow one — a balancer that took 7.2s to honestly say
		// "yes I have this film" must not be silenced. Persistent slowness is
		// visible in the telemetry dashboard, not penalized here.
		if s {
			csCache.setWithTTL(target, s, r, q, checksearchCacheTTL)
			return csResult{show: s, rch: r, quality: q, outcome: probeOk, status: rec.Code}, nil
		}
		csCache.setWithTTL(target, s, r, q, checksearchCacheNegTTL)
		return csResult{show: s, rch: r, quality: q, outcome: probeEmpty, status: rec.Code}, nil
	})

	select {
	case res := <-ch:
		r := res.Val.(csResult)
		return r.show, r.rch, r.quality, r.outcome
	case <-ctx.Done():
		// Wall deadline fired — this round reports show=false while the probe
		// finishes in the background and warms csCache. Not a balancer failure:
		// the breaker is updated inside the probe body when it actually ends.
		return false, false, "", probeEmpty
	}
}

// truncErr trims an error message to a sane length for storage in metrics/cache.
func truncErr(s string) string {
	const max = 240
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

var checksearchQualityRe = regexp.MustCompile(`"quality"\s*:\s*"([^"]+)"`)

func evaluateSearchResult(body string) (show bool, rch bool, quality string) {
	rch = strings.Contains(body, "\"rch\":true")
	show = rch ||
		strings.Contains(body, "data-json=") ||
		strings.Contains(body, "\"type\":\"movie\"") ||
		strings.Contains(body, "\"type\":\"episode\"") ||
		strings.Contains(body, "\"type\":\"season\"")

	if m := checksearchQualityRe.FindStringSubmatch(body); len(m) >= 2 {
		quality = strings.TrimSpace(m[1])
	}
	return show, rch, quality
}

// checksearchTargetPath normalizes a probe target to its path+query form —
// this is both the in-process ServeHTTP target and the csCache key. Keys are
// host-independent, so /lite/events (built against the client's Host) and the
// /capi drill (built against its own) land on the SAME cache entries.
func checksearchTargetPath(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	if u.RawQuery == "" {
		return u.Path
	}
	return u.Path + "?" + u.RawQuery
}

func loopbackHostPort(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if strings.HasPrefix(addr, ":") {
		return "127.0.0.1" + addr
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	host = strings.TrimSpace(host)
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func (s *lifeTaskStore) create(items []liteEventItem) string {
	s.cleanup()
	memkey := newMemKey()

	copied := make([]liteEventItem, len(items))
	copy(copied, items)

	s.mu.Lock()
	s.tasks[memkey] = &lifeOnlineTask{
		createdAt: time.Now().UTC(),
		items:     copied,
		done:      make([]bool, len(copied)),
	}
	s.mu.Unlock()
	return memkey
}

func (s *lifeTaskStore) update(memkey string, idx int, item liteEventItem) {
	s.mu.Lock()
	defer s.mu.Unlock()

	task, ok := s.tasks[memkey]
	if !ok {
		return
	}
	if idx < 0 || idx >= len(task.items) {
		return
	}
	task.items[idx] = item
	task.done[idx] = true
}

func (s *lifeTaskStore) snapshot(memkey string) (ready bool, tasks int, online []liteEventItem, ok bool) {
	s.cleanup()

	s.mu.RLock()
	task, exists := s.tasks[memkey]
	if !exists {
		s.mu.RUnlock()
		return false, 0, nil, false
	}

	tasks = len(task.items)
	if tasks == 0 {
		s.mu.RUnlock()
		return false, 0, []liteEventItem{}, true
	}

	completed := 0
	out := make([]liteEventItem, 0, tasks)
	for i := range task.items {
		if !task.done[i] {
			continue
		}
		completed++
		out = append(out, task.items[i])
	}
	s.mu.RUnlock()

	sortByQuality := true
	if serverReady() {
		sortByQuality = !liveConfig(config.Config{}).Online.CustomOrder
	}
	sort.Slice(out, func(i, j int) bool {
		is := itemShow(out[i])
		js := itemShow(out[j])
		if is != js {
			return is && !js
		}
		if sortByQuality && is && js {
			iq := qualityRank(out[i].Quality)
			jq := qualityRank(out[j].Quality)
			if iq != jq {
				return iq > jq
			}
		}
		return itemIndex(out[i]) < itemIndex(out[j])
	})

	return completed == tasks, tasks, out, true
}

func (s *lifeTaskStore) cleanup() {
	cutoff := time.Now().UTC().Add(-lifeTaskTTL)
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, task := range s.tasks {
		if task.createdAt.Before(cutoff) {
			delete(s.tasks, key)
		}
	}
	// Hard cap: if the store is still oversize after TTL eviction, drop the
	// oldest entries until we're back under the cap. Protects against burst
	// floods that could otherwise OOM the process before the next janitor tick.
	if len(s.tasks) > lifeTaskMax {
		type kv struct {
			k string
			t time.Time
		}
		all := make([]kv, 0, len(s.tasks))
		for k, t := range s.tasks {
			all = append(all, kv{k, t.createdAt})
		}
		sort.Slice(all, func(i, j int) bool { return all[i].t.Before(all[j].t) })
		drop := len(s.tasks) - lifeTaskMax
		for i := 0; i < drop; i++ {
			delete(s.tasks, all[i].k)
		}
	}
}

func newMemKey() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "")
	}
	return hex.EncodeToString(buf)
}

func itemShow(item liteEventItem) bool {
	return item.Show != nil && *item.Show
}

func itemIndex(item liteEventItem) int {
	if item.Index == nil {
		return 0
	}
	return *item.Index
}

// --- Default plugin sort (matches admin panel group+quality order) ---

var (
	pluginSortOnce sync.Once
	pluginGroupIdx map[string]int // lowercase plugin key → group order index
	pluginKnownIdx map[string]int // lowercase plugin key → position in knownBalancers
)

func ensurePluginSortMaps() {
	pluginSortOnce.Do(func() {
		// Build group key → order index map.
		groupOrder := make(map[string]int, len(balancerGroupOrder))
		for i, g := range balancerGroupOrder {
			groupOrder[g.Key] = i
		}
		otherIdx := len(balancerGroupOrder) - 1

		pluginGroupIdx = make(map[string]int, len(balancerGroupMap))
		for name, group := range balancerGroupMap {
			key := PluginKeyFor(name)
			if idx, ok := groupOrder[group]; ok {
				pluginGroupIdx[key] = idx
			} else {
				pluginGroupIdx[key] = otherIdx
			}
		}

		// Build knownBalancers position map for stable tie-breaking.
		pluginKnownIdx = make(map[string]int, len(knownBalancers))
		for i, name := range knownBalancers {
			pluginKnownIdx[PluginKeyFor(name)] = i
		}
	})
}

// pluginDefaultGroupRank returns the group display order for a plugin.
func pluginDefaultGroupRank(plugin string) int {
	ensurePluginSortMaps()
	if idx, ok := pluginGroupIdx[plugin]; ok {
		return idx
	}
	return len(balancerGroupOrder) - 1 // "other"
}

// pluginStaticQualityRank returns quality rank for sorting (lower = better).
// Uses adminQualityRank from admin_panel_balancers.go: 4K=0, FHD=1, SD=2, ""=3.
func pluginStaticQualityRank(plugin string) int {
	badge := pluginQualityBadgeGet(plugin)
	if r, ok := adminQualityRank[badge]; ok {
		return r
	}
	return 3
}

// pluginKnownOrder returns the position in knownBalancers for stable tie-breaking.
func pluginKnownOrder(plugin string) int {
	ensurePluginSortMaps()
	if idx, ok := pluginKnownIdx[plugin]; ok {
		return idx
	}
	return len(knownBalancers) // unknown → after all known
}

// sortPluginsByDefault sorts the plugins slice to match the admin panel display
// order: group (ru→anime→ua→video→en→other), then quality (4K→FHD→SD),
// then knownBalancers position for stable tie-breaking.
func sortPluginsByDefault(plugins []string) {
	ensurePluginSortMaps()
	sort.SliceStable(plugins, func(i, j int) bool {
		gi := pluginDefaultGroupRank(plugins[i])
		gj := pluginDefaultGroupRank(plugins[j])
		if gi != gj {
			return gi < gj
		}
		qi := pluginStaticQualityRank(plugins[i])
		qj := pluginStaticQualityRank(plugins[j])
		if qi != qj {
			return qi < qj
		}
		return pluginKnownOrder(plugins[i]) < pluginKnownOrder(plugins[j])
	})
}

// qualityRank returns a numeric rank for sorting: higher = better quality.
func qualityRank(q *string) int {
	if q == nil {
		return 0
	}
	switch *q {
	case "4K":
		return 5
	case "2K":
		return 4
	case "FHD":
		return 3
	case "HD":
		return 2
	case "SD":
		return 1
	default:
		return 0
	}
}

// --- Balancer disable check ---

// cachedMergedConf caches loadMergedConf() for 10 seconds to avoid
// reading JSON files from disk on every /lite/events request.
var (
	mergedConfCache     map[string]any
	mergedConfCacheTime time.Time
	mergedConfCacheMu   sync.RWMutex
)

func cachedMergedConf() map[string]any {
	mergedConfCacheMu.RLock()
	if time.Since(mergedConfCacheTime) < 10*time.Second && mergedConfCache != nil {
		defer mergedConfCacheMu.RUnlock()
		return mergedConfCache
	}
	mergedConfCacheMu.RUnlock()

	mergedConfCacheMu.Lock()
	defer mergedConfCacheMu.Unlock()
	// Double-check after acquiring write lock.
	if time.Since(mergedConfCacheTime) < 10*time.Second && mergedConfCache != nil {
		return mergedConfCache
	}
	mergedConfCache = loadMergedConf()
	mergedConfCacheTime = time.Now()
	return mergedConfCache
}

// eventsResponseCacheVersion is bumped on every config-related invalidate.
// Cached entries store the version they were computed at; on read, if the
// current version is newer, the entry is treated as stale. This lets us
// "invalidate" without an O(N) wipe of the map — admins who save the config
// repeatedly (drag-and-drop reorder, slider adjustments) used to trigger a
// 5000-entry zero-fill on every keystroke; now they just bump an integer.
var eventsResponseCacheVersion uint64

// invalidateMergedConfCache forces the next cachedMergedConf() call to re-read
// from disk. Called after admin panel saves balancer/plugin config changes.
//
// Cheap O(1) invalidation: bumps a version counter that cache reads check.
// Stale entries are evicted lazily on the next access for their key or on
// the regular size-based cleanup pass.
func invalidateMergedConfCache() {
	mergedConfCacheMu.Lock()
	mergedConfCache = nil
	mergedConfCacheTime = time.Time{}
	mergedConfCacheMu.Unlock()

	// Bump the response-cache version atomically. Existing entries will be
	// treated as stale on the next read, but the map itself stays — no big
	// allocation, no GC churn under rapid-fire admin saves.
	eventsResponseCache.Lock()
	eventsResponseCacheVersion++
	eventsResponseCache.Unlock()
}

// isBalancerDisabled checks both config-level enable/disable and healthcheck auto-disable.
func isBalancerDisabled(plugin string) bool {
	raw := strings.ToLower(strings.TrimSpace(plugin))
	// Reconcile alias spellings: the events pipeline canonicalises "iremux" →
	// "remux" (balancerNameAliases), but the config section is "iRemux" and
	// PluginKeyFor maps it to "iremux". Matching on PluginKeyFor alone would
	// therefore MISS the disable for any aliased balancer (iRemux was reachable
	// despite being globally disabled). Canonicalise BOTH sides so "remux",
	// "iremux" and the "iRemux" section all resolve to the same key.
	canon := canonicalBalancerName(raw)

	// 1. Check config-level disable (from init.conf / current.conf). Scan every
	// knownBalancer whose canonical key matches — a balancer is disabled if ANY
	// of its spellings' config sections says so.
	root := cachedMergedConf()
	for _, name := range knownBalancers {
		if canonicalBalancerName(PluginKeyFor(name)) != canon {
			continue
		}
		section, ok := root[name].(map[string]any)
		if !ok {
			continue // this spelling has no config section — try the others
		}
		// .NET admin panel may save both "enable" and "enabled" with
		// conflicting values (e.g. enable:true, enabled:false).
		// Treat balancer as disabled only when the "enable" field
		// (primary in .NET BaseSettings) is explicitly false.
		// Fall back to "enabled" only when "enable" is absent.
		if v, ok := section["enable"]; ok {
			if !toBoolAny(v) {
				return true
			}
		} else if v, ok := section["enabled"]; ok {
			if !toBoolAny(v) {
				return true
			}
		}
	}

	// 2. Check healthcheck auto-disable (keyed by either the raw or canonical
	// spelling depending on how the checker recorded it).
	if hc := balancerhealth.GetGlobalHealthChecker(); hc != nil && (hc.IsAutoDisabled(raw) || hc.IsAutoDisabled(canon)) {
		return true
	}

	return false
}

// resolveUserGroup resolves the user's group from the request.
//
// Tries every credential source the tgauth gate accepts, in order:
//  1. "lampac_token" cookie  (browser auth)
//  2. "_lampac_auth" cookie  (HttpOnly tamper-protection cookie)
//  3. "?token=" URL param    (TV / Android Lampa clients that can't keep
//     cookies across cross-origin XHR — they append
//     the token to the URL instead, see plugins.go
//     addToken() and auth_tg_api.go gate)
//
// Returns nil only if the group store itself is disabled. If none of the
// sources match a known token, falls back to the default group so anonymous
// requests still respect default-group restrictions (and can't bypass
// per-group balancer denials by simply omitting the cookie).
// groupFilteringActive reports whether per-user group source filtering should
// run: locally when both the group + TG token stores exist, or on a mirror
// edge where the group is delegated from the origin.
func groupFilteringActive() bool {
	return mirrorClientRef != nil || (groupStoreRef != nil && tgTokenStoreRef != nil)
}

func resolveUserGroup(r *http.Request) *tgauth.UserGroup {
	// Mirror edge: the effective group is delegated by the origin via
	// /api/auth/validate and cached on the mirror client (Фаза 5). No local
	// token/group store is consulted. nil = unknown → treated as unrestricted.
	if mirrorClientRef != nil {
		return mirrorClientRef.groupForRequest(r)
	}
	if groupStoreRef == nil || tgTokenStoreRef == nil {
		return nil
	}
	for _, tok := range collectLampacTokenCandidates(r) {
		approved, ok := tgTokenStoreRef.Lookup(tok)
		if !ok || approved == nil {
			// Token not in the store — skip; another candidate might be valid.
			// (We use Lookup, not GetGroupID, because GetGroupID returns "" for
			// both "token unknown" and "token belongs to default group" — we
			// only want to skip the first case.)
			continue
		}
		// Effective group accounts for the time-limited premium overlay:
		// while PremiumUntil is in the future, the user sees the premium
		// group's balancer allow-list; after that, automatically falls
		// back to their base GroupID (default for most users).
		effective := approved.EffectiveGroupID(currentPremiumGroupID())
		g, _ := groupStoreRef.Get(effective)
		return &g
	}
	// No valid token recognized — apply default group restrictions.
	g := groupStoreRef.GetDefault()
	return &g
}

// collectLampacTokenCandidates returns lampac auth tokens carried by the
// request, in priority order, de-duplicated and trimmed. Mirrors the
// resolution order used by the tgauth gate so /lite/* group checks see the
// same identity that the gate granted access to.
func collectLampacTokenCandidates(r *http.Request) []string {
	out := make([]string, 0, 3)
	push := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		for _, ex := range out {
			if ex == v {
				return
			}
		}
		out = append(out, v)
	}
	if c, err := r.Cookie("alpac_token"); err == nil {
		push(c.Value)
	}
	if c, err := r.Cookie("lampac_token"); err == nil {
		push(c.Value)
	}
	if c, err := r.Cookie("_lampac_auth"); err == nil {
		push(c.Value)
	}
	// Header pair mirrors auth.extractAuthToken: the local shell (webOS/Tizen
	// Home) and the store build authenticate ONLY via X-Lampac-Token — the
	// page lives on a local origin, so no cookie ever reaches us. Without
	// these the gate admits the user (auth middleware reads the header) while
	// everything identity-shaped built on THIS function — capiAccount,
	// premium, resolveUserGroup (→ default-group deny, dead sources) — saw an
	// anonymous request.
	push(r.Header.Get("X-Alpac-Token"))
	push(r.Header.Get("X-Lampac-Token"))
	push(r.URL.Query().Get("token"))
	return out
}
