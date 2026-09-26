package adminhttp

import (
	"context"
	"net/http"
	"os"
	"time"

	"lampac-go/internal/antidpi"
	"lampac-go/internal/balancerstats"
	"lampac-go/internal/cluster"
	"lampac-go/internal/config"
	"lampac-go/internal/httpx"
	"lampac-go/internal/jacred"
	"lampac-go/internal/sidecar"
	"lampac-go/internal/tgauth"
	"lampac-go/internal/torrbalancer"
	"lampac-go/internal/torrs"
	"lampac-go/internal/transcodesvc"
	"lampac-go/internal/updater"

	jsoniter "github.com/json-iterator/go"
)

// json mirrors the host's package-level jsoniter var so moved handlers keep their
// json.Marshal/Unmarshal call sites unchanged.
var json = jsoniter.ConfigCompatibleWithStandardLibrary

// deps.go — the injected seam from the former host (httpapi). The admin panel is
// being decomposed out of httpapi one cohesive slice at a time; this package must
// never import httpapi, so everything the moved handlers reach into the running
// server for is injected once via Deps (SetDeps, called from registerAdminRoutes
// before any slice registers). Pure helpers (writeJSON) are copied.
//
// Deps grows as slices move: each new slice adds only the fields it needs.

// Deps bundles host dependencies injected at registration time.
type Deps struct {
	LiveConfig      func(fallback config.Config) config.Config
	ServerReady     func() bool
	StoreLiveConfig func(cfg config.Config)

	// AdminAuthCheck is the host's tgAdminAuthCheck — the admin-auth gate. It
	// runs in the host's context (closing over the admin path / session key /
	// password store globals), so this package needs only the exported tgauth
	// arg types.
	AdminAuthCheck func(w http.ResponseWriter, r *http.Request, store *tgauth.Store, adminStore *tgauth.AdminIDStore) (int64, bool, bool)

	// UpdateConfigTOMLMapNoReload does an atomic read-modify-write of config.toml
	// without triggering a Server.Reload (for live settings tweaks).
	UpdateConfigTOMLMapNoReload func(modify func(root map[string]any)) error

	// JacredStatusSnapshot reports the self-hosted jacred parser status. Returns
	// the external jacred.Status type, so no cycle.
	JacredStatusSnapshot func(cfg config.Config) jacred.Status

	// RuTrackerStatus / RuTrackerTest expose the native rutracker indexer.
	// Both return already JSON-shaped maps so this package needs no import of
	// internal/rutracker.
	RuTrackerStatus func() map[string]any
	RuTrackerTest   func(ctx context.Context, query string) map[string]any

	// Live-server accessors + helpers (all return external/primitive types).
	LiveUpdater           func() *updater.Service
	LiveVersion           func() string
	LiveTSBalancerPool    func() *torrbalancer.Pool
	LiveTSBalancerStore   func() *torrbalancer.Store
	BuildUpdaterConfig    func(cfg config.Config, commit string) updater.Config
	CheckTSHealthWithAuth func(baseURL, login, password string) (bool, string)

	// Config-TOML bridge helpers (return primitives → no cycle). readFileAny
	// serves init.conf/current.conf from TOML when the server is ready, else
	// reads the raw file; loadMergedConf returns the merged runtime config map.
	ReadFileAny    func(rel string) ([]byte, bool)
	LoadMergedConf func() map[string]any

	// More config-TOML writers (atomic read-modify-write of config.toml /
	// init.conf; write a pretty JSON file; merge a legacy JSON map into TOML).
	UpdateConfigTOMLMap  func(modify func(root map[string]any)) error
	UpdateInitConfMap    func(modify func(root map[string]any)) error
	WritePrettyJSON      func(rel string, value any) error
	ApplyLegacyMapToTOML func(legacyRoot, tomlRoot map[string]any)

	// Misc live-server accessors (external/primitive returns) + a static set.
	LiveAntiDPI             func() *antidpi.Server
	ReloadServer            func() error
	TorrServerProcessStatus func() map[string]any
	LocalCorePlugins        map[string]struct{}

	// Weblog viewer. WebLogSnapshot returns the log slice as `any` (the handler
	// only serializes it), so the host's internal StoredWebLog type need not be
	// relocated — the "serialize-only → inject as any" trick.
	WebLogSnapshot func(limit int, lvl, tag, ip string) (any, int64)
	WebLogClear    func()

	// Cluster admin. EnsureClusterSecrets returns the mirror ClusterSecrets
	// (field-flatten); the rest are external/primitive returns.
	EnsureClusterSecrets       func() (ClusterSecrets, error)
	ForceRewriteClusterSecrets func(apiKey, sharedSecret string) error
	BuildPeerConfigSnippet     func(apiKey, sharedSecret string) string
	LiveClusterPool            func() *cluster.Pool
	LiveClusterStore           func() *cluster.Store

	// Branding admin — JSON-passthrough: reads returned as `any` (serialize
	// only), save takes the raw body (host unmarshals into its Branding), so the
	// host's internal Branding type need not be relocated. BrandingDefaults is a
	// static value set in SetDeps.
	CurrentBranding          func() any
	CurrentBrandingName      func() string
	LoadBrandingOverride     func(repoRoot string) (any, bool)
	SaveBrandingOverrideJSON func(repoRoot string, body []byte) error
	BrandingDefaults         any

	// Manifest install-flow auth/helpers (defined in host PIN files admin_init.go).
	AuthorizeAdmin   func(w http.ResponseWriter, r *http.Request) bool
	ReadRootPasswd   func() string
	RandomLowerAlnum func(n int) string

	// WAF admin. CurrentWafState/ReloadWAF return the WafState interface (the
	// host's *wafState satisfies it); SaveWafConfigJSON takes the raw body (host
	// unmarshals into wafConfig). WafConfigDefault is the zero-value config shown
	// when WAF isn't initialised. Both state getters MUST return a nil interface
	// (not a typed nil) when the underlying state is nil.
	CurrentWafState   func() WafState
	ReloadWAF         func() WafState
	SaveWafConfigJSON func(body []byte) error
	WafConfigDefault  any

	// Torrs admin. TSExternalAuthConfig returns the external-access config (from
	// the hot-reloadable atomic pointer) + ok=false when unset — the wrapper
	// tsExternalAuthState stays internal, but its .cfg is the external
	// config.TorrServerExternalAccess.
	GetTorrsServer       func() *torrs.BTServer
	TSExternalAuthConfig func() (config.TorrServerExternalAccess, bool)

	// Community-plugins catalog flows — injected as high-level closures built
	// host-side (they touch the internal catalog types + concrete registry/cron),
	// so this package needs neither. errMsg=="" means success; status is the HTTP
	// code to return on error.
	CommunityStatusPayload func() any
	CommunityInstallByName func(name string) (status int, errMsg string)
	CommunityUpdateByName  func(name string) (status int, updated bool, errMsg string)

	// Stats-action handlers (restart/reload/transcoding) split out of the host's
	// admin_stats.go grab-bag.
	RequestSelfRestart func()
	LiveTransSvc       func() *transcodesvc.TranscodingService

	// Browser-pool settings — the GET/POST state payload (Mirage stats + engine
	// info + capable balancers) + the engine-persist are built host-side; the
	// Mirage runtime setters are injected. Keeps browserEngineInfo internal.
	BrowserPoolPayload      func() map[string]any
	SetMirageBrowserLimit   func(n int)
	SetMirageStreamCacheMax func(n int)
	SetMirageStreamCacheTTL func(hours int)
	BrowserEnginePersist    func() (status int, errMsg string)

	// CurrentPremiumGroupID reports the configured premium TG group id (for the
	// password-users admin). Returns a string.
	CurrentPremiumGroupID func() string

	// Inspector diagnostics — host helpers the moved admin_inspector reaches for.
	CheckTCPPort        func(host string, port int) bool
	CheckTSHealth       func(baseURL string) bool
	FreeDiskSpaceBytes  func(path string) (int64, error)
	LiveProxyPool       func() *sidecar.Pool
	LoadConfigTOMLAsMap func() map[string]any
	ReloadProxies       func() error

	// ClientIP resolves the real client IP (honoring trusted-proxy headers) for
	// the admin login/webauthn handlers' rate-limit + audit logging.
	ClientIP func(r *http.Request) string

	// v2 premium panel (TG WebApp auth): the live admin-path prefix + the shared
	// auth-cookie setter, both owned by the host's admin_panel_auth.
	ActiveAdminPath func() string
	SetAuthCookies  func(w http.ResponseWriter, r *http.Request, token string)

	// PluginQualityBadgeSet records a per-plugin quality badge (constructor/custbal
	// admin). Host-owned global map in lite_events.
	PluginQualityBadgeSet func(plugin, badge string)

	// Balancer-stats core — shared by the balancer/telemetry/users/healthcheck
	// admin slices. The 3 static lists are exposed as getters (single source in
	// the host, divergence-proof); the rest are runtime accessors. GetGlobalBalancerStats
	// returns the leaf *balancerstats.Manager (adminhttp imports that leaf pkg directly).
	KnownBalancers         func() []string
	BalancerGroupMap       func() map[string]string
	BalancerStatusTags     func() map[string]string
	GetGlobalBalancerStats func() *balancerstats.Manager
	CSBreakerInspect       func(balancer string) (open bool, openUntil time.Time, failCount int)
	CSBreakerReset         func(balancer string)
	PluginKeyFor           func(name string) string
	PluginQualityBadgeGet  func(plugin string) string
	LiveAlertEngine        func() *balancerstats.AlertEngine
}

var deps Deps

// SetDeps installs the host dependency bundle. Called once from
// registerAdminRoutes before any admin slice registers its routes.
func SetDeps(d Deps) {
	deps = d
	localCorePlugins = d.LocalCorePlugins
	brandingDefaults = d.BrandingDefaults
	wafConfigDefault = d.WafConfigDefault
}

func writeJSON(w http.ResponseWriter, code int, payload any) { httpx.WriteJSON(w, code, payload) }

// --- forwarders so moved handlers keep their call sites unchanged ---

func liveConfig(fallback config.Config) config.Config {
	if deps.LiveConfig != nil {
		return deps.LiveConfig(fallback)
	}
	return fallback
}

func serverReady() bool { return deps.ServerReady != nil && deps.ServerReady() }

func storeLiveConfig(cfg config.Config) {
	if deps.StoreLiveConfig != nil {
		deps.StoreLiveConfig(cfg)
	}
}

// tgAdminAuthCheck forwards to the injected host gate; the unwired default
// fail-closes (403, ok=false).
// TrustedRequest — проверка «запрос пришёл изнутри кластера» (X-Cluster-Key).
// Ставится из httpapi: у этого пакета нет доступа к настройкам кластера.
// Нужна, чтобы снимать показания панели скриптом, не заводя сессию админа.
var TrustedRequest func(*http.Request) bool

func tgAdminAuthCheck(w http.ResponseWriter, r *http.Request, store *tgauth.Store, adminStore *tgauth.AdminIDStore) (int64, bool, bool) {
	if TrustedRequest != nil && TrustedRequest(r) {
		return 0, true, true
	}
	if deps.AdminAuthCheck != nil {
		return deps.AdminAuthCheck(w, r, store, adminStore)
	}
	http.Error(w, "forbidden", http.StatusForbidden)
	return 0, false, false
}

func updateConfigTOMLMapNoReload(modify func(root map[string]any)) error {
	if deps.UpdateConfigTOMLMapNoReload != nil {
		return deps.UpdateConfigTOMLMapNoReload(modify)
	}
	return nil
}

func jacredStatusSnapshot(cfg config.Config) jacred.Status {
	if deps.JacredStatusSnapshot != nil {
		return deps.JacredStatusSnapshot(cfg)
	}
	return jacred.Status{}
}

func ruTrackerStatus() map[string]any {
	if deps.RuTrackerStatus != nil {
		return deps.RuTrackerStatus()
	}
	return map[string]any{"enabled": false, "reason": "not wired"}
}

func ruTrackerTest(ctx context.Context, query string) map[string]any {
	if deps.RuTrackerTest != nil {
		return deps.RuTrackerTest(ctx, query)
	}
	return map[string]any{"ok": false, "error": "not wired"}
}

func liveUpdater() *updater.Service {
	if deps.LiveUpdater != nil {
		return deps.LiveUpdater()
	}
	return nil
}

func liveVersion() string {
	if deps.LiveVersion != nil {
		return deps.LiveVersion()
	}
	return ""
}

func liveTSBalancerPool() *torrbalancer.Pool {
	if deps.LiveTSBalancerPool != nil {
		return deps.LiveTSBalancerPool()
	}
	return nil
}

func liveTSBalancerStore() *torrbalancer.Store {
	if deps.LiveTSBalancerStore != nil {
		return deps.LiveTSBalancerStore()
	}
	return nil
}

func buildUpdaterConfig(cfg config.Config, commit string) updater.Config {
	if deps.BuildUpdaterConfig != nil {
		return deps.BuildUpdaterConfig(cfg, commit)
	}
	return updater.Config{}
}

func checkTSHealthWithAuth(baseURL, login, password string) (bool, string) {
	if deps.CheckTSHealthWithAuth != nil {
		return deps.CheckTSHealthWithAuth(baseURL, login, password)
	}
	return false, ""
}

func readFileAny(rel string) ([]byte, bool) {
	if deps.ReadFileAny != nil {
		return deps.ReadFileAny(rel)
	}
	data, err := os.ReadFile(relToRuntime(rel))
	if err != nil {
		return nil, false
	}
	return data, true
}

func loadMergedConf() map[string]any {
	if deps.LoadMergedConf != nil {
		return deps.LoadMergedConf()
	}
	return map[string]any{}
}

func updateConfigTOMLMap(modify func(root map[string]any)) error {
	if deps.UpdateConfigTOMLMap != nil {
		return deps.UpdateConfigTOMLMap(modify)
	}
	return nil
}

func updateInitConfMap(modify func(root map[string]any)) error {
	if deps.UpdateInitConfMap != nil {
		return deps.UpdateInitConfMap(modify)
	}
	return nil
}

func writePrettyJSON(rel string, value any) error {
	if deps.WritePrettyJSON != nil {
		return deps.WritePrettyJSON(rel, value)
	}
	return nil
}

func applyLegacyMapToTOML(legacyRoot, tomlRoot map[string]any) {
	if deps.ApplyLegacyMapToTOML != nil {
		deps.ApplyLegacyMapToTOML(legacyRoot, tomlRoot)
	}
}

func liveAntiDPI() *antidpi.Server {
	if deps.LiveAntiDPI != nil {
		return deps.LiveAntiDPI()
	}
	return nil
}

func reloadServer() error {
	if deps.ReloadServer != nil {
		return deps.ReloadServer()
	}
	return nil
}

func torrServerProcessStatus() map[string]any {
	if deps.TorrServerProcessStatus != nil {
		return deps.TorrServerProcessStatus()
	}
	return map[string]any{}
}

// localCorePlugins mirrors deps.LocalCorePlugins (set in SetDeps) — the static
// set of built-in core plugin names.
var localCorePlugins map[string]struct{}

func webLogSnapshot(limit int, lvl, tag, ip string) (any, int64) {
	if deps.WebLogSnapshot != nil {
		return deps.WebLogSnapshot(limit, lvl, tag, ip)
	}
	return nil, 0
}

func webLogClear() {
	if deps.WebLogClear != nil {
		deps.WebLogClear()
	}
}

func ensureClusterSecrets() (ClusterSecrets, error) {
	if deps.EnsureClusterSecrets != nil {
		return deps.EnsureClusterSecrets()
	}
	return ClusterSecrets{}, nil
}

func forceRewriteClusterSecrets(apiKey, sharedSecret string) error {
	if deps.ForceRewriteClusterSecrets != nil {
		return deps.ForceRewriteClusterSecrets(apiKey, sharedSecret)
	}
	return nil
}

func buildPeerConfigSnippet(apiKey, sharedSecret string) string {
	if deps.BuildPeerConfigSnippet != nil {
		return deps.BuildPeerConfigSnippet(apiKey, sharedSecret)
	}
	return ""
}

func liveClusterPool() *cluster.Pool {
	if deps.LiveClusterPool != nil {
		return deps.LiveClusterPool()
	}
	return nil
}

func liveClusterStore() *cluster.Store {
	if deps.LiveClusterStore != nil {
		return deps.LiveClusterStore()
	}
	return nil
}

func currentBranding() any {
	if deps.CurrentBranding != nil {
		return deps.CurrentBranding()
	}
	return nil
}

func currentBrandingName() string {
	if deps.CurrentBrandingName != nil {
		return deps.CurrentBrandingName()
	}
	return ""
}

func loadBrandingOverride(repoRoot string) (any, bool) {
	if deps.LoadBrandingOverride != nil {
		return deps.LoadBrandingOverride(repoRoot)
	}
	return nil, false
}

func saveBrandingOverrideJSON(repoRoot string, body []byte) error {
	if deps.SaveBrandingOverrideJSON != nil {
		return deps.SaveBrandingOverrideJSON(repoRoot, body)
	}
	return nil
}

// brandingDefaults mirrors deps.BrandingDefaults (set in SetDeps).
var brandingDefaults any

func authorizeAdmin(w http.ResponseWriter, r *http.Request) bool {
	if deps.AuthorizeAdmin != nil {
		return deps.AuthorizeAdmin(w, r)
	}
	http.Error(w, "forbidden", http.StatusForbidden)
	return false
}

func readRootPasswd() string {
	if deps.ReadRootPasswd != nil {
		return deps.ReadRootPasswd()
	}
	return ""
}

func randomLowerAlnum(n int) string {
	if deps.RandomLowerAlnum != nil {
		return deps.RandomLowerAlnum(n)
	}
	return ""
}

func currentWafState() WafState {
	if deps.CurrentWafState != nil {
		return deps.CurrentWafState()
	}
	return nil
}

func reloadWAF() WafState {
	if deps.ReloadWAF != nil {
		return deps.ReloadWAF()
	}
	return nil
}

func saveWafConfigJSON(body []byte) error {
	if deps.SaveWafConfigJSON != nil {
		return deps.SaveWafConfigJSON(body)
	}
	return nil
}

// wafConfigDefault mirrors deps.WafConfigDefault (set in SetDeps).
var wafConfigDefault any

func getTorrsServer() *torrs.BTServer {
	if deps.GetTorrsServer != nil {
		return deps.GetTorrsServer()
	}
	return nil
}

func tsExternalAuthConfig() (config.TorrServerExternalAccess, bool) {
	if deps.TSExternalAuthConfig != nil {
		return deps.TSExternalAuthConfig()
	}
	return config.TorrServerExternalAccess{}, false
}

func communityStatusPayload() any {
	if deps.CommunityStatusPayload != nil {
		return deps.CommunityStatusPayload()
	}
	return []any{}
}

func communityInstallByName(name string) (int, string) {
	if deps.CommunityInstallByName != nil {
		return deps.CommunityInstallByName(name)
	}
	return http.StatusServiceUnavailable, "community plugins unavailable"
}

func communityUpdateByName(name string) (int, bool, string) {
	if deps.CommunityUpdateByName != nil {
		return deps.CommunityUpdateByName(name)
	}
	return http.StatusServiceUnavailable, false, "community plugins unavailable"
}

func requestSelfRestart() {
	if deps.RequestSelfRestart != nil {
		deps.RequestSelfRestart()
	}
}

func liveTransSvc() *transcodesvc.TranscodingService {
	if deps.LiveTransSvc != nil {
		return deps.LiveTransSvc()
	}
	return nil
}

func browserPoolPayload() map[string]any {
	if deps.BrowserPoolPayload != nil {
		return deps.BrowserPoolPayload()
	}
	return map[string]any{}
}

func setMirageBrowserLimit(n int) {
	if deps.SetMirageBrowserLimit != nil {
		deps.SetMirageBrowserLimit(n)
	}
}

func setMirageStreamCacheMax(n int) {
	if deps.SetMirageStreamCacheMax != nil {
		deps.SetMirageStreamCacheMax(n)
	}
}

func setMirageStreamCacheTTL(hours int) {
	if deps.SetMirageStreamCacheTTL != nil {
		deps.SetMirageStreamCacheTTL(hours)
	}
}

func browserEnginePersist() (int, string) {
	if deps.BrowserEnginePersist != nil {
		return deps.BrowserEnginePersist()
	}
	return http.StatusOK, ""
}

func currentPremiumGroupID() string {
	if deps.CurrentPremiumGroupID != nil {
		return deps.CurrentPremiumGroupID()
	}
	return ""
}

func checkTCPPort(host string, port int) bool {
	if deps.CheckTCPPort != nil {
		return deps.CheckTCPPort(host, port)
	}
	return false
}

func checkTSHealth(baseURL string) bool {
	if deps.CheckTSHealth != nil {
		return deps.CheckTSHealth(baseURL)
	}
	return false
}

func freeDiskSpaceBytes(path string) (int64, error) {
	if deps.FreeDiskSpaceBytes != nil {
		return deps.FreeDiskSpaceBytes(path)
	}
	return 0, nil
}

func liveProxyPool() *sidecar.Pool {
	if deps.LiveProxyPool != nil {
		return deps.LiveProxyPool()
	}
	return nil
}

func loadConfigTOMLAsMap() map[string]any {
	if deps.LoadConfigTOMLAsMap != nil {
		return deps.LoadConfigTOMLAsMap()
	}
	return nil
}

func reloadProxies() error {
	if deps.ReloadProxies != nil {
		return deps.ReloadProxies()
	}
	return nil
}

func clientIP(r *http.Request) string {
	if deps.ClientIP != nil {
		return deps.ClientIP(r)
	}
	return ""
}

func activeAdminPath() string {
	if deps.ActiveAdminPath != nil {
		return deps.ActiveAdminPath()
	}
	return ""
}

func setAuthCookies(w http.ResponseWriter, r *http.Request, token string) {
	if deps.SetAuthCookies != nil {
		deps.SetAuthCookies(w, r, token)
	}
}

func pluginQualityBadgeSet(plugin, badge string) {
	if deps.PluginQualityBadgeSet != nil {
		deps.PluginQualityBadgeSet(plugin, badge)
	}
}

// Balancer-stats forwarders. The static-list getters mirror the host vars via a
// closure (single source). The func names match the host API so moved files call
// them unchanged; the 3 static ones become getter-calls at their (few) use sites.

func knownBalancers() []string {
	if deps.KnownBalancers != nil {
		return deps.KnownBalancers()
	}
	return nil
}

func balancerGroupMap() map[string]string {
	if deps.BalancerGroupMap != nil {
		return deps.BalancerGroupMap()
	}
	return nil
}

func balancerStatusTags() map[string]string {
	if deps.BalancerStatusTags != nil {
		return deps.BalancerStatusTags()
	}
	return nil
}

func GetGlobalBalancerStats() *balancerstats.Manager {
	if deps.GetGlobalBalancerStats != nil {
		return deps.GetGlobalBalancerStats()
	}
	return nil
}

func CSBreakerInspect(balancer string) (bool, time.Time, int) {
	if deps.CSBreakerInspect != nil {
		return deps.CSBreakerInspect(balancer)
	}
	return false, time.Time{}, 0
}

func CSBreakerReset(balancer string) {
	if deps.CSBreakerReset != nil {
		deps.CSBreakerReset(balancer)
	}
}

func PluginKeyFor(name string) string {
	if deps.PluginKeyFor != nil {
		return deps.PluginKeyFor(name)
	}
	return name
}

func pluginQualityBadgeGet(plugin string) string {
	if deps.PluginQualityBadgeGet != nil {
		return deps.PluginQualityBadgeGet(plugin)
	}
	return ""
}

func liveAlertEngine() *balancerstats.AlertEngine {
	if deps.LiveAlertEngine != nil {
		return deps.LiveAlertEngine()
	}
	return nil
}

