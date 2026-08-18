package adminhttp

import (
	"net/http"

	"lampac-go/internal/envpresets"
	"lampac-go/internal/jsmodules"
	"lampac-go/internal/kit"
	"lampac-go/internal/logbuf"
	"lampac-go/internal/proxycore"
	"lampac-go/internal/tgauth"

	"github.com/go-chi/chi/v5"
)

// RegisterUsersRoutes wires GET/POST /{adminPath}/api/users (kit user overview).
func RegisterUsersRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore, kitStore *kit.Store) {
	h := TgAdminUsersHandler(tgStore, adminStore, kitStore)
	router.Get("/"+adminPath+"/api/users", h)
	router.Post("/"+adminPath+"/api/users", h)
}

// RegisterPromoRoutes wires GET/POST /{adminPath}/api/promo (admin promo-code CRUD).
// The user-facing redeem endpoint (PromoRedeemHandler) is registered separately by
// the host's user-auth routes, since the promo feature (admin + redeem + shared
// PromoStore helpers) is one cohesive slice.
func RegisterPromoRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore, promoStore *tgauth.PromoStore) {
	h := tgAdminPromoHandler(tgStore, adminStore, promoStore)
	router.Get("/"+adminPath+"/api/promo", h)
	router.Post("/"+adminPath+"/api/promo", h)
}

// RegisterInspectorRoutes wires POST /{adminPath}/api/inspector/{run,fix} — the
// system self-diagnostics + auto-fix panel.
func RegisterInspectorRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore) {
	router.Post("/"+adminPath+"/api/inspector/run", tgAdminInspectorRunHandler(tgStore, adminStore))
	router.Post("/"+adminPath+"/api/inspector/fix", tgAdminInspectorFixHandler(tgStore, adminStore))
}

// RegisterHealthcheckRoutes wires GET/POST /{adminPath}/api/healthcheck (balancer
// health tunables + per-balancer status + manual re-enable/reset).
func RegisterHealthcheckRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore) {
	h := tgAdminHealthcheckHandler(tgStore, adminStore)
	router.Get("/"+adminPath+"/api/healthcheck", h)
	router.Post("/"+adminPath+"/api/healthcheck", h)
}

// RegisterTelemetryRoutes wires the balancer-telemetry dashboard + SSE stream.
func RegisterTelemetryRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore) {
	h := tgAdminTelemetryHandler(tgStore, adminStore)
	router.Get("/"+adminPath+"/api/telemetry", h)
	router.Post("/"+adminPath+"/api/telemetry", h)
	router.Get("/"+adminPath+"/api/telemetry/stream", tgAdminTelemetryStreamHandler(tgStore, adminStore))
}

// RegisterFlaresolverrRoutes wires the admin FlareSolverr live-config endpoint
// (GET/PUT/DELETE /{adminPath}/api/flaresolverr). Call after SetDeps.
func RegisterFlaresolverrRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore) {
	fsAdmin := tgAdminFlareSolverrHandler(tgStore, adminStore)
	router.Get("/"+adminPath+"/api/flaresolverr", fsAdmin)
	router.Put("/"+adminPath+"/api/flaresolverr", fsAdmin)
	router.Delete("/"+adminPath+"/api/flaresolverr", fsAdmin)
}

// RegisterJacredStatusRoutes wires GET /{adminPath}/api/jacred/status. Call after SetDeps.
func RegisterJacredStatusRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore) {
	router.Get("/"+adminPath+"/api/jacred/status", tgAdminJacredStatusHandler(tgStore, adminStore))
}

// RegisterRuTrackerRoutes wires the native rutracker indexer endpoints:
// GET /{adminPath}/api/rutracker/status and /{adminPath}/api/rutracker/test.
func RegisterRuTrackerRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore) {
	router.Get("/"+adminPath+"/api/rutracker/status", tgAdminRuTrackerStatusHandler(tgStore, adminStore))
	router.Get("/"+adminPath+"/api/rutracker/test", tgAdminRuTrackerTestHandler(tgStore, adminStore))
}

// RegisterBansRoutes wires GET/POST /{adminPath}/api/bans. Call after SetDeps.
func RegisterBansRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore, banStore *tgauth.BanStore) {
	bansFn := tgAdminBansHandler(tgStore, adminStore, banStore)
	router.Get("/"+adminPath+"/api/bans", bansFn)
	router.Post("/"+adminPath+"/api/bans", bansFn)
}

// RegisterTorrBalancerRoutes wires GET/POST /{adminPath}/api/torrbalancer.
func RegisterTorrBalancerRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore) {
	h := tgAdminTorrBalancerHandler(tgStore, adminStore)
	router.Get("/"+adminPath+"/api/torrbalancer", h)
	router.Post("/"+adminPath+"/api/torrbalancer", h)
}

// RegisterEnvPresetsRoutes wires /{adminPath}/api/env-presets* when a manager exists.
func RegisterEnvPresetsRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore, envPresets *envpresets.Manager) {
	if envPresets == nil {
		return
	}
	h := tgAdminEnvPresetsHandler(tgStore, adminStore, envPresets)
	router.Get("/"+adminPath+"/api/env-presets", h)
	router.Get("/"+adminPath+"/api/env-presets/*", h)
	router.Post("/"+adminPath+"/api/env-presets", h)
	router.Post("/"+adminPath+"/api/env-presets/*", h)
	router.Put("/"+adminPath+"/api/env-presets/*", h)
}

// RegisterGroupsRoutes wires GET/POST /{adminPath}/api/groups.
func RegisterGroupsRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore, groupStore *tgauth.GroupStore) {
	h := tgAdminGroupsHandler(tgStore, adminStore, groupStore)
	router.Get("/"+adminPath+"/api/groups", h)
	router.Post("/"+adminPath+"/api/groups", h)
}

// RegisterSelfUpdateRoutes wires the /{adminPath}/api/update/* self-update module.
func RegisterSelfUpdateRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore) {
	router.Get("/"+adminPath+"/api/update/status", tgAdminUpdateStatusHandler(tgStore, adminStore))
	router.Post("/"+adminPath+"/api/update/check", tgAdminUpdateCheckHandler(tgStore, adminStore))
	router.Post("/"+adminPath+"/api/update/apply", tgAdminUpdateApplyHandler(tgStore, adminStore))
	router.Post("/"+adminPath+"/api/update/rollback", tgAdminUpdateRollbackHandler(tgStore, adminStore))
	router.Post("/"+adminPath+"/api/update/channel", tgAdminUpdateChannelHandler(tgStore, adminStore))
}

// RegisterRefConfigRoutes wires the reference-config proxy endpoints.
func RegisterRefConfigRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore) {
	router.Get("/"+adminPath+"/api/refconfig", tgAdminRefConfigFetchHandler(tgStore, adminStore))
	router.Post("/"+adminPath+"/api/refconfig/save", tgAdminRefConfigSaveHandler(tgStore, adminStore))
}

// RegisterTgPanelRoute wires GET /{adminPath} — the main admin panel HTML page.
func RegisterTgPanelRoute(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore, llmEnabled bool) {
	router.Get("/"+adminPath, tgAdminPageHandler(tgStore, adminStore, llmEnabled))
}

// RegisterPasswordUsersRoutes wires GET/POST /{adminPath}/api/password-users.
func RegisterPasswordUsersRoutes(router chi.Router, adminPath string, pwUserStore *tgauth.PasswordUserStore, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore) {
	h := adminPasswordUsersHandler(pwUserStore, tgStore, adminStore)
	router.Get("/"+adminPath+"/api/password-users", h)
	router.Post("/"+adminPath+"/api/password-users", h)
}

// RegisterAuthModeRoutes wires GET/POST /{adminPath}/api/auth-mode.
func RegisterAuthModeRoutes(router chi.Router, adminPath string, modeStore *tgauth.AuthModeStore, anonIssuer *tgauth.AnonIssuer, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore) {
	h := adminAuthModeHandler(modeStore, anonIssuer, tgStore, adminStore)
	router.Get("/"+adminPath+"/api/auth-mode", h)
	router.Post("/"+adminPath+"/api/auth-mode", h)
}

// RegisterBrowserPoolRoutes wires GET/POST /{adminPath}/api/browserpool.
func RegisterBrowserPoolRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore) {
	h := tgAdminBrowserPoolHandler(tgStore, adminStore)
	router.Get("/"+adminPath+"/api/browserpool", h)
	router.Post("/"+adminPath+"/api/browserpool", h)
}

// RegisterBrowserInstallRoute wires POST /{adminPath}/api/browser/install.
func RegisterBrowserInstallRoute(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore) {
	router.Post("/"+adminPath+"/api/browser/install", tgAdminBrowserInstallHandler(tgStore, adminStore))
}

// RegisterRestartReloadRoutes wires POST /{adminPath}/api/restart + /api/reload.
func RegisterRestartReloadRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore) {
	router.Post("/"+adminPath+"/api/restart", tgAdminRestartHandler(tgStore, adminStore))
	router.Post("/"+adminPath+"/api/reload", tgAdminReloadHandler(tgStore, adminStore))
}

// RegisterTranscodingRoutes wires the /{adminPath}/api/transcoding/* admin endpoints.
func RegisterTranscodingRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore) {
	router.Post("/"+adminPath+"/api/transcoding/kill/{jobId}", tgAdminTranscodingKillHandler(tgStore, adminStore))
	router.Post("/"+adminPath+"/api/transcoding/cleanup", tgAdminTranscodingCleanupHandler(tgStore, adminStore))
}

// RegisterFeedbackRoutes wires the admin /{adminPath}/api/feedback + /feedback/stats
// endpoints. ops carries the host-side store operations.
func RegisterFeedbackRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore, ops FeedbackOps) {
	fbAdminFn := tgAdminFeedbackHandler(tgStore, adminStore, ops)
	router.Get("/"+adminPath+"/api/feedback", fbAdminFn)
	router.Post("/"+adminPath+"/api/feedback", fbAdminFn)
	router.Get("/"+adminPath+"/api/feedback/stats", tgAdminFeedbackStatsHandler(tgStore, adminStore, ops))
}

// RegisterCommunityPluginsRoutes wires GET/POST /{adminPath}/api/communityplugins.
func RegisterCommunityPluginsRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore, registry CustomPlugins, cron CommunityCron) {
	h := tgAdminCommunityPluginsHandler(tgStore, adminStore, registry, cron)
	router.Get("/"+adminPath+"/api/communityplugins", h)
	router.Post("/"+adminPath+"/api/communityplugins", h)
}

// RegisterCustomPluginsRoutes wires GET/POST /{adminPath}/api/customplugins.
func RegisterCustomPluginsRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore, registry CustomPlugins) {
	h := tgAdminCustomPluginsHandler(tgStore, adminStore, registry)
	router.Get("/"+adminPath+"/api/customplugins", h)
	router.Post("/"+adminPath+"/api/customplugins", h)
}

// RegisterTorrsRoutes wires the /{adminPath}/api/torrs/* TorrServer admin endpoints.
func RegisterTorrsRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore) {
	router.Get("/"+adminPath+"/api/torrs/list", tgAdminTorrsListHandler(tgStore, adminStore))
	router.Post("/"+adminPath+"/api/torrs/action", tgAdminTorrsActionHandler(tgStore, adminStore))
	router.Get("/"+adminPath+"/api/torrs/health", tgAdminTorrsHealthHandler(tgStore, adminStore))
	router.Get("/"+adminPath+"/api/torrs/stream", tgAdminTorrsStreamHandler(tgStore, adminStore))
	router.Post("/"+adminPath+"/api/torrs/cleanup", tgAdminTorrsCleanupHandler(tgStore, adminStore))
	router.Get("/"+adminPath+"/api/torrs/external", tgAdminTorrsExternalHandler(tgStore, adminStore))
}

// RegisterWAFRoutes wires the WAF admin API at both the global /api/admin/waf/*
// path and /{adminPath}/api/waf/* (super-admin gated inside the handler).
func RegisterWAFRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore) {
	wafFn := tgAdminWAFHandler(tgStore, adminStore)
	router.Get("/api/admin/waf/state", wafFn)
	router.Post("/api/admin/waf/config", wafFn)
	router.Post("/api/admin/waf/reload", wafFn)
	router.Post("/api/admin/waf/ban/remove", wafFn) // body-based remove (CIDR-safe)
	router.Post("/api/admin/waf/ban", wafFn)
	router.Delete("/api/admin/waf/ban/{ip}", wafFn)
	router.Get("/"+adminPath+"/api/waf/state", wafFn)
	router.Post("/"+adminPath+"/api/waf/config", wafFn)
	router.Post("/"+adminPath+"/api/waf/reload", wafFn)
	router.Post("/"+adminPath+"/api/waf/ban/remove", wafFn)
	router.Post("/"+adminPath+"/api/waf/ban", wafFn)
	router.Delete("/"+adminPath+"/api/waf/ban/{ip}", wafFn)
}

// RegisterBrandingRoutes wires GET/POST /{adminPath}/api/branding.
func RegisterBrandingRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore) {
	h := tgAdminBrandingHandler(tgStore, adminStore)
	router.Get("/"+adminPath+"/api/branding", h)
	router.Post("/"+adminPath+"/api/branding", h)
}

// RegisterClusterRoutes wires GET/POST /{adminPath}/api/cluster (node cascade admin).
func RegisterClusterRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore) {
	h := tgAdminClusterHandler(tgStore, adminStore)
	router.Get("/"+adminPath+"/api/cluster", h)
	router.Post("/"+adminPath+"/api/cluster", h)
}

// RegisterCapilogRoutes wires the /{adminPath}/capilog viewer page + /api/capilog data endpoint.
func RegisterCapilogRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore) {
	router.Get("/"+adminPath+"/capilog", capiLogPageHandler(tgStore, adminStore))
	capiLogFn := capiLogAPIHandler(tgStore, adminStore)
	router.Get("/"+adminPath+"/api/capilog", capiLogFn)
	router.Post("/"+adminPath+"/api/capilog", capiLogFn)
}

// RegisterConfigTOMLRoutes wires the /{adminPath}/api/config/{toml,validate,backups,rollback} editor.
func RegisterConfigTOMLRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore) {
	h := tgAdminConfigTOMLHandler(tgStore, adminStore)
	router.Get("/"+adminPath+"/api/config/toml", h)
	router.Post("/"+adminPath+"/api/config/toml", h)
	router.Post("/"+adminPath+"/api/config/validate", h)
	router.Get("/"+adminPath+"/api/config/backups", h)
	router.Post("/"+adminPath+"/api/config/rollback", h)
}

// RegisterProxyRoutes wires GET/POST /{adminPath}/api/proxy. reloadProxies is the
// host's proxy-pool reloader (closure over the deps.go accessors).
func RegisterProxyRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore, reloadProxies func() error) {
	h := tgAdminProxyHandler(tgStore, adminStore, reloadProxies)
	router.Get("/"+adminPath+"/api/proxy", h)
	router.Post("/"+adminPath+"/api/proxy", h)
}

// RegisterProxycoreRoutes wires the /{adminPath}/api/proxycore/* built-in proxy engine admin API.
func RegisterProxycoreRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore, poolFn func() *proxycore.Pool, reloadFn func() error, repoRoot string) {
	h := tgAdminProxycoreHandler(tgStore, adminStore, poolFn, reloadFn, proxycoreWARPBinDir(repoRoot))
	router.Get("/"+adminPath+"/api/proxycore/*", h)
	router.Post("/"+adminPath+"/api/proxycore/*", h)
	router.Delete("/"+adminPath+"/api/proxycore/*", h)
}

// RegisterPluginsRoutes wires /{adminPath}/api/plugins + /api/torrserver/test.
func RegisterPluginsRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore) {
	h := tgAdminPluginsHandler(tgStore, adminStore)
	router.Get("/"+adminPath+"/api/plugins", h)
	router.Post("/"+adminPath+"/api/plugins", h)
	router.Post("/"+adminPath+"/api/torrserver/test", tgAdminTorrServerTestHandler(tgStore, adminStore))
}

// RegisterLampaRoutes wires the /{adminPath}/api/lampa/{version,update} endpoints.
func RegisterLampaRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore) {
	router.Get("/"+adminPath+"/api/lampa/version", tgAdminLampaVersionHandler(tgStore, adminStore))
	router.Post("/"+adminPath+"/api/lampa/update", tgAdminLampaUpdateHandler(tgStore, adminStore))
}

// RegisterJSModulesRoutes wires the /{adminPath}/api/{modules,sisi-sources,music-sources}* editors.
func RegisterJSModulesRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore, jsMgr, sisiSources, musicSources *jsmodules.Manager) {
	mount := func(prefix string, h http.HandlerFunc) {
		router.Get("/"+adminPath+"/api/"+prefix, h)
		router.Get("/"+adminPath+"/api/"+prefix+"/", h)
		router.Get("/"+adminPath+"/api/"+prefix+"/*", h)
		router.Post("/"+adminPath+"/api/"+prefix, h)
		router.Post("/"+adminPath+"/api/"+prefix+"/*", h)
		router.Put("/"+adminPath+"/api/"+prefix+"/*", h)
		router.Delete("/"+adminPath+"/api/"+prefix+"/*", h)
	}
	mount("modules", tgAdminJSModulesHandler(tgStore, adminStore, jsMgr))
	mount("sisi-sources", tgAdminSISISourcesHandler(tgStore, adminStore, sisiSources))
	mount("music-sources", tgAdminMusicSourcesHandler(tgStore, adminStore, musicSources))
}

// RegisterConfigRoutes wires GET/POST /{adminPath}/api/config.
func RegisterConfigRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore) {
	h := tgAdminConfigHandler(tgStore, adminStore)
	router.Get("/"+adminPath+"/api/config", h)
	router.Post("/"+adminPath+"/api/config", h)
}

// RegisterAppReplaceRoutes wires /{adminPath}/api/appreplace + /api/customcode.
func RegisterAppReplaceRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore) {
	appReplaceFn := tgAdminAppReplaceHandler(tgStore, adminStore)
	router.Get("/"+adminPath+"/api/appreplace", appReplaceFn)
	router.Post("/"+adminPath+"/api/appreplace", appReplaceFn)
	customCodeFn := tgAdminCustomCodeHandler(tgStore, adminStore)
	router.Get("/"+adminPath+"/api/customcode", customCodeFn)
	router.Post("/"+adminPath+"/api/customcode", customCodeFn)
}

// RegisterTelegramRoutes wires the /{adminPath}/api/tgsettings|broadcast|admins panel.
func RegisterTelegramRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore, tgBot *tgauth.Bot, memberChecker *tgauth.MembershipChecker) {
	router.Get("/"+adminPath+"/api/tgsettings", tgAdminTGSettingsHandler(tgStore, adminStore, tgBot, memberChecker))
	router.Post("/"+adminPath+"/api/tgsettings", tgAdminTGSettingsHandler(tgStore, adminStore, tgBot, memberChecker))
	router.Post("/"+adminPath+"/api/tgsettings/regen-path", tgAdminRegenPathHandler(tgStore, adminStore))
	router.Get("/"+adminPath+"/api/broadcast", tgAdminBroadcastHandler(tgStore, adminStore, tgBot))
	router.Post("/"+adminPath+"/api/broadcast", tgAdminBroadcastHandler(tgStore, adminStore, tgBot))
	router.Get("/"+adminPath+"/api/admins", tgAdminAdminsHandler(tgStore, adminStore))
	router.Post("/"+adminPath+"/api/admins", tgAdminAdminsHandler(tgStore, adminStore))
}

// RegisterLogsRoutes wires the /{adminPath}/api/logs* viewer when a buffer exists.
func RegisterLogsRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore, logBuffer *logbuf.Buffer) {
	if logBuffer == nil {
		return
	}
	router.Get("/"+adminPath+"/api/logs", tgAdminLogsHandler(tgStore, adminStore, logBuffer))
	router.Get("/"+adminPath+"/api/logs/stream", tgAdminLogsStreamHandler(tgStore, adminStore, logBuffer))
	router.Get("/"+adminPath+"/api/logs/export", tgAdminLogsExportHandler(tgStore, adminStore, logBuffer))
}
