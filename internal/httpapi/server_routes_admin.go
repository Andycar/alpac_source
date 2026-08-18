package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"lampac-go/internal/adminhttp"
	"lampac-go/internal/browser"
	"lampac-go/internal/calendar"
	"lampac-go/internal/calendarhttp"
	"lampac-go/internal/config"
	"lampac-go/internal/custbal"
	"lampac-go/internal/envpresets"
	"lampac-go/internal/feedback"
	"lampac-go/internal/jsmodules"
	"lampac-go/internal/kit"
	"lampac-go/internal/litesrc"
	"lampac-go/internal/logbuf"
	"lampac-go/internal/proxyapi"
	"lampac-go/internal/proxycore"
	"lampac-go/internal/proxylink"
	"lampac-go/internal/skipdb"
	"lampac-go/internal/skiphttp"
	"lampac-go/internal/tgauth"
	"lampac-go/internal/tmdbcache"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

// adminRouteDeps bundles the state needed to wire the admin-panel routes.
// Grouped into a struct because the alternative — 25+ positional params —
// produces worse call-sites than the type declaration.
type adminRouteDeps struct {
	cfg           config.Config
	tgTokenStore  *tgauth.Store
	tgBot         *tgauth.Bot
	kitStore      *kit.Store
	custBalPool   *custbal.Pool
	dynRoutes     *DynamicRouteRegistry
	jsMgr         *jsmodules.Manager
	sisiSources   *jsmodules.Manager
	musicSources  *jsmodules.Manager
	envPresets    *envpresets.Manager
	customPlugins *CustomPluginRegistry
	commCron      *CommunityUpdateCron
	groupStore    *tgauth.GroupStore
	promoStore    *tgauth.PromoStore
	banStore      *tgauth.BanStore
	memberChecker *tgauth.MembershipChecker
	bkitStore     *BKitSessionStore
	skipDB        *skipdb.DB
	calStore      *calendar.Store
	calCron       *calendar.Cron
	pwStore       *tgauth.PasswordAuthStore
	pwUserStore   *tgauth.PasswordUserStore // separate: user-facing password auth
	authModeStore *tgauth.AuthModeStore
	anonIssuer    *tgauth.AnonIssuer
	webauthnStore *tgauth.WebAuthnStore
	sessionSecret []byte
	tmdbCacheInst *tmdbcache.Cache
	tmdbPoolInst  *tmdbcache.Pool
	proxyLinks    *proxylink.Manager
	proxyAPI      *proxyapi.Handler // live /proxy handler — for the capi-sources 403 probe
	logBuffer     *logbuf.Buffer
	hasLLM        bool
}

// registerAdminRoutes wires the /{adminPath}/* admin-panel endpoints plus
// /api/feedback/* (public feedback submission). The admin path is generated
// or loaded on first call; it is returned so Server.adminPath can be set.
// When TG auth is off and password auth is not configured, routes are
// localhost-only at the handler layer.
func registerAdminRoutes(router chi.Router, deps adminRouteDeps) (adminPath string, feedbackStore *feedback.FeedbackStore, adminIDStore *tgauth.AdminIDStore) {
	adminPath = loadOrGenerateAdminPath()
	superAdminID := deps.cfg.TelegramAuth.AdminID
	if superAdminID == 0 {
		superAdminID = 1 // placeholder for password-only mode
	}
	adminIDStore = tgauth.NewAdminIDStore(deps.cfg.Compat.RepoRoot, superAdminID)

	// Inject the host seam for admin slices already moved to internal/adminhttp.
	// Called once, before any adminhttp.Register*Routes below.
	adminhttp.SetDeps(adminhttp.Deps{
		LiveConfig:                  liveConfig,
		ServerReady:                 serverReady,
		StoreLiveConfig:             storeLiveConfig,
		AdminAuthCheck:              tgAdminAuthCheck,
		UpdateConfigTOMLMapNoReload: updateConfigTOMLMapNoReload,
		JacredStatusSnapshot:        jacredStatusSnapshot,
		RuTrackerStatus:             rutrackerAdminStatus,
		RuTrackerTest:               rutrackerAdminTest,
		LiveUpdater:                 liveUpdater,
		LiveVersion:                 liveVersion,
		LiveTSBalancerPool:          liveTSBalancerPool,
		LiveTSBalancerStore:         liveTSBalancerStore,
		BuildUpdaterConfig:          buildUpdaterConfig,
		CheckTSHealthWithAuth:       checkTSHealthWithAuth,
		ReadFileAny:                 readFileAny,
		LoadMergedConf:              loadMergedConf,
		UpdateConfigTOMLMap:         updateConfigTOMLMap,
		UpdateInitConfMap:           updateInitConfMap,
		WritePrettyJSON:             writePrettyJSON,
		ApplyLegacyMapToTOML:        applyLegacyMapToTOML,
		LiveAntiDPI:                 liveAntiDPI,
		ReloadServer:                reloadServer,
		TorrServerProcessStatus:     torrServerProcessStatus,
		LocalCorePlugins:            localCorePlugins,
		WebLogSnapshot: func(limit int, lvl, tag, ip string) (any, int64) {
			return webLogSnapshot(limit, lvl, tag, ip)
		},
		WebLogClear: webLogClear,
		EnsureClusterSecrets: func() (adminhttp.ClusterSecrets, error) {
			r, err := ensureClusterSecrets()
			return adminhttp.ClusterSecrets{
				APIKeyGenerated:       r.APIKeyGenerated,
				SharedSecretGenerated: r.SharedSecretGenerated,
				APIKey:                r.APIKey,
				SharedSecret:          r.SharedSecret,
			}, err
		},
		ForceRewriteClusterSecrets: forceRewriteClusterSecrets,
		BuildPeerConfigSnippet:     buildPeerConfigSnippet,
		LiveClusterPool:            liveClusterPool,
		LiveClusterStore:           liveClusterStore,
		CurrentBranding:            func() any { return currentBranding() },
		CurrentBrandingName:        func() string { return currentBranding().Name },
		LoadBrandingOverride: func(repoRoot string) (any, bool) {
			o, has, _ := loadBrandingOverride(repoRoot)
			return o, has
		},
		SaveBrandingOverrideJSON: func(repoRoot string, body []byte) error {
			var b Branding
			if len(body) > 0 {
				if err := json.Unmarshal(body, &b); err != nil {
					return err
				}
			}
			return saveBrandingOverride(repoRoot, b)
		},
		BrandingDefaults: brandingDefaults,
		AuthorizeAdmin:   authorizeAdmin,
		ReadRootPasswd:   readRootPasswd,
		RandomLowerAlnum: randomLowerAlnum,
		CurrentWafState: func() adminhttp.WafState {
			s := currentWafState()
			if s == nil {
				return nil
			}
			return s
		},
		ReloadWAF: func() adminhttp.WafState {
			s := ReloadWAF()
			if s == nil {
				return nil
			}
			return s
		},
		SaveWafConfigJSON: func(body []byte) error {
			var cfg wafConfig
			if err := json.Unmarshal(body, &cfg); err != nil {
				return err
			}
			return saveWafConfig(cfg)
		},
		WafConfigDefault: wafConfig{},
		GetTorrsServer:   getTorrsServer,
		TSExternalAuthConfig: func() (config.TorrServerExternalAccess, bool) {
			s := tsExternalAuthPtr.Load()
			if s == nil {
				return config.TorrServerExternalAccess{}, false
			}
			return s.cfg, true
		},
		CommunityStatusPayload: func() any {
			catalog, err := deps.commCron.CachedCatalog(5 * time.Minute)
			if err != nil {
				catalog = &CatalogResponse{}
			}
			return buildCommunityStatus(deps.customPlugins, catalog)
		},
		CommunityInstallByName: func(name string) (int, string) {
			catalog, err := deps.commCron.CachedCatalog(5 * time.Minute)
			if err != nil {
				return http.StatusBadGateway, "catalog unavailable: " + err.Error()
			}
			var entry *CatalogEntry
			for i := range catalog.Plugins {
				if catalog.Plugins[i].Name == name {
					entry = &catalog.Plugins[i]
					break
				}
			}
			if entry == nil {
				return http.StatusNotFound, "plugin not found in catalog"
			}
			for _, p := range deps.customPlugins.List() {
				if p.Name == name && !p.IsCommunity {
					return http.StatusConflict, "plugin with this name already exists (uploaded manually)"
				}
			}
			if err := installCommunityPlugin(deps.customPlugins, *entry); err != nil {
				return http.StatusInternalServerError, err.Error()
			}
			return http.StatusOK, ""
		},
		CommunityUpdateByName: func(name string) (int, bool, string) {
			catalog, err := deps.commCron.CachedCatalog(5 * time.Minute)
			if err != nil {
				return http.StatusBadGateway, false, "catalog unavailable: " + err.Error()
			}
			var entry *CatalogEntry
			for i := range catalog.Plugins {
				if catalog.Plugins[i].Name == name {
					entry = &catalog.Plugins[i]
					break
				}
			}
			if entry == nil {
				return http.StatusNotFound, false, "plugin not found in catalog"
			}
			updated, err := updateCommunityPlugin(deps.customPlugins, *entry)
			if err != nil {
				return http.StatusInternalServerError, false, err.Error()
			}
			return http.StatusOK, updated, ""
		},
		RequestSelfRestart:     requestSelfRestart,
		LiveTransSvc:           liveTransSvc,
		CurrentPremiumGroupID:  currentPremiumGroupID,
		CheckTCPPort:           checkTCPPort,
		CheckTSHealth:          checkTSHealth,
		FreeDiskSpaceBytes:     freeDiskSpaceBytes,
		LiveProxyPool:          liveProxyPool,
		LoadConfigTOMLAsMap:    loadConfigTOMLAsMap,
		ReloadProxies:          reloadProxies,
		ClientIP:               clientIP,
		ActiveAdminPath:        func() string { return activeAdminPath },
		SetAuthCookies:         setAuthCookies,
		PluginQualityBadgeSet:  pluginQualityBadgeSet,
		KnownBalancers:         func() []string { return knownBalancers },
		BalancerGroupMap:       func() map[string]string { return balancerGroupMap },
		BalancerStatusTags:     func() map[string]string { return balancerStatusTags },
		GetGlobalBalancerStats: GetGlobalBalancerStats,
		CSBreakerInspect:       CSBreakerInspect,
		CSBreakerReset:         CSBreakerReset,
		PluginKeyFor:           PluginKeyFor,
		PluginQualityBadgeGet:  pluginQualityBadgeGet,
		LiveAlertEngine:        liveAlertEngine,
		BrowserPoolPayload: func() map[string]any {
			active, limit := litesrc.MirageBrowserStats()
			info := collectBrowserEngineInfo()
			return map[string]any{
				"max_concurrent":     limit,
				"active_sessions":    active,
				"stream_cache_max":   litesrc.GetMirageStreamCacheMax(),
				"stream_cache_ttl_h": litesrc.GetMirageStreamCacheTTLHours(),
				"engine":             info.engine,
				"balancer_engines":   info.balancerEngines,
				"available_engines":  info.available,
				"engine_status":      info.status,
				"balancers":          browserCapableBalancers,
			}
		},
		SetMirageBrowserLimit:   litesrc.SetMirageBrowserLimit,
		SetMirageStreamCacheMax: litesrc.SetMirageStreamCacheMax,
		SetMirageStreamCacheTTL: litesrc.SetMirageStreamCacheTTL,
		BrowserEnginePersist: func() (int, string) {
			cur := collectBrowserEngineInfo()
			path := browserEnginePersistPath(liveConfig(config.Config{}).Compat.RepoRoot)
			if err := browser.SavePersisted(path, cur.engine, cur.balancerEngines); err != nil {
				return http.StatusInternalServerError, "persist failed: " + err.Error()
			}
			return http.StatusOK, ""
		},
	})

	authMode := "localhost-only"
	if deps.tgTokenStore != nil {
		authMode = "tg-auth"
	} else if deps.pwStore != nil && deps.pwStore.IsConfigured() {
		authMode = "password+2fa"
	}
	log.Info().Str("path", "/"+adminPath).Str("auth", authMode).Msg("admin panel enabled")

	// activeAdminPath must always reflect the current admin URL prefix so
	// downstream handlers (login redirects, TG WebApp auth response) can build
	// correct URLs. Was previously gated on pwStore (password auth), which
	// meant TG-only deployments shipped an empty value and the WebApp ended up
	// fetching //api/whoami → protocol-relative URL → host="api" → 404.
	activeAdminPath = adminPath
	if deps.pwStore != nil {
		pwAuthStore = deps.pwStore
		adminSessionKey = deps.sessionSecret
	}

	if deps.tgBot != nil {
		deps.tgBot.SetAdminPath(adminPath)
	}

	// adminIDStore is returned so the constructor can stash it on the Server
	// (s.adminIDStore) and wire the AlertEngine after NewServer returns and the
	// server singleton is set — it can't be wired here because that singleton is
	// nil during registerAdminRoutes (called from inside NewServer).

	// tgTokenStore may be nil when TG auth is disabled — handlers will
	// allow localhost access in that case.
	adminhttp.RegisterTgPanelRoute(router, adminPath, deps.tgTokenStore, adminIDStore, deps.hasLLM)
	router.Get("/"+adminPath+"/api/whoami", tgAdminWhoamiHandler(deps.tgTokenStore, adminIDStore))
	// Playback capability histogram — what the built-in players fail to decode.
	router.Get("/"+adminPath+"/api/playback-stats", adminPlaybackStatsHandler(deps.tgTokenStore, adminIDStore))
	router.Post("/"+adminPath+"/api/playback-stats/reset", adminPlaybackStatsResetHandler(deps.tgTokenStore, adminIDStore))
	// Playback QUALITY per source — rebuffer rate, buffer level, throughput —
	// straight from the players, via CMCD on proxied segment requests.
	router.Get("/"+adminPath+"/api/cmcd-stats", adminCMCDStatsHandler(deps.tgTokenStore, adminIDStore))
	router.Post("/"+adminPath+"/api/cmcd-stats/reset", adminCMCDStatsResetHandler(deps.tgTokenStore, adminIDStore))
	adminhttp.RegisterUsersRoutes(router, adminPath, deps.tgTokenStore, adminIDStore, deps.kitStore)

	// Password users (alternative auth) + auth-mode toggle. Always wired —
	// admin can create users while still in tg mode, then flip the mode.
	adminhttp.RegisterPasswordUsersRoutes(router, adminPath, deps.pwUserStore, deps.tgTokenStore, adminIDStore)
	adminhttp.RegisterAuthModeRoutes(router, adminPath, deps.authModeStore, deps.anonIssuer, deps.tgTokenStore, adminIDStore)

	adminhttp.RegisterPromoRoutes(router, adminPath, deps.tgTokenStore, adminIDStore, deps.promoStore)

	router.Get("/"+adminPath+"/api/balancers", tgAdminBalancersHandler(deps.tgTokenStore, adminIDStore, deps.custBalPool, deps.dynRoutes))
	router.Post("/"+adminPath+"/api/balancers", tgAdminBalancersHandler(deps.tgTokenStore, adminIDStore, deps.custBalPool, deps.dynRoutes))
	adminhttp.RegisterPluginsRoutes(router, adminPath, deps.tgTokenStore, adminIDStore)
	// Telegram settings / broadcast / admins (moved to internal/adminhttp).
	adminhttp.RegisterTelegramRoutes(router, adminPath, deps.tgTokenStore, adminIDStore, deps.tgBot, deps.memberChecker)
	adminhttp.RegisterConfigRoutes(router, adminPath, deps.tgTokenStore, adminIDStore)

	// capilog — live viewer of the client weblog (moved to internal/adminhttp).
	adminhttp.RegisterCapilogRoutes(router, adminPath, deps.tgTokenStore, adminIDStore)

	// incidents — client «Сообщить о проблеме» reports: device snapshot + client logs + correlated
	// server logs, in one place (incident.go)
	router.Get("/"+adminPath+"/incidents", incidentAdminPageHandler(deps.tgTokenStore, adminIDStore))
	incidentFn := incidentAdminAPIHandler(deps.tgTokenStore, adminIDStore)
	router.Get("/"+adminPath+"/api/incidents", incidentFn)
	router.Post("/"+adminPath+"/api/incidents", incidentFn)

	// TOML config editor + /api/proxy + /api/proxycore/* (moved to internal/adminhttp).
	adminhttp.RegisterConfigTOMLRoutes(router, adminPath, deps.tgTokenStore, adminIDStore)
	adminhttp.RegisterProxyRoutes(router, adminPath, deps.tgTokenStore, adminIDStore, func() error { return reloadProxies() })
	adminhttp.RegisterProxycoreRoutes(router, adminPath, deps.tgTokenStore, adminIDStore,
		func() *proxycore.Pool { return liveProxyCorePool() },
		func() error { return reloadProxyCore() },
		deps.cfg.Compat.RepoRoot,
	)

	router.Get("/"+adminPath+"/api/stats", tgAdminStatsHandler(deps.tgTokenStore, adminIDStore, deps.proxyLinks))
	// Live SSE companion to /api/stats — used by admin v2 Dashboard for real-time tiles.
	router.Get("/"+adminPath+"/api/stats/stream", tgAdminStatsStreamHandler(deps.tgTokenStore, adminIDStore, deps.proxyLinks))
	router.Get("/"+adminPath+"/api/dashboard", tgAdminDashboardHandler(deps.tgTokenStore, adminIDStore, deps.proxyLinks))

	// Balancer telemetry — live metrics dashboard, fallback host control (moved to internal/adminhttp).
	adminhttp.RegisterTelemetryRoutes(router, adminPath, deps.tgTokenStore, adminIDStore)

	// Healthcheck — tunables (interval, thresholds), per-balancer status,
	// manual re-enable & reset of auto-disable streaks.
	adminhttp.RegisterHealthcheckRoutes(router, adminPath, deps.tgTokenStore, adminIDStore)

	// Branding — public-facing brand strings (online plugin name in 4
	// locales, HTML title, version, …). Saves persist to
	// database/branding.json and take effect immediately.
	adminhttp.RegisterBrandingRoutes(router, adminPath, deps.tgTokenStore, adminIDStore)

	// Media gateway — ffprobe wrapper + transcoding queue overview.
	router.Get("/"+adminPath+"/api/media/probe", tgAdminMediaProbeHandler(deps.tgTokenStore, adminIDStore))
	router.Get("/"+adminPath+"/api/media/jobs", tgAdminMediaJobsHandler(deps.tgTokenStore, adminIDStore))
	router.Post("/"+adminPath+"/api/media/jobs/kill", tgAdminMediaKillHandler(deps.tgTokenStore, adminIDStore))

	// Marketplace v2 — plugin sandbox validator (pre-install check).
	router.Get("/"+adminPath+"/api/plugins/validate", tgAdminPluginValidateHandler(deps.tgTokenStore, adminIDStore))
	adminhttp.RegisterRestartReloadRoutes(router, adminPath, deps.tgTokenStore, adminIDStore)
	adminhttp.RegisterTorrsRoutes(router, adminPath, deps.tgTokenStore, adminIDStore)

	// Bans admin (moved to internal/adminhttp).
	adminhttp.RegisterBansRoutes(router, adminPath, deps.tgTokenStore, adminIDStore, deps.banStore)

	// WAF — hot-reloadable middleware config + manual bans + stats.
	// Exposed at both the global /api/admin/waf/* path (so the UI shipped
	// in this codebase can address it without knowing the admin path) and
	// under /{adminPath}/api/waf/* for consistency with the other admin
	// endpoints. Both routes share the same auth gate.
	adminhttp.RegisterWAFRoutes(router, adminPath, deps.tgTokenStore, adminIDStore)

	adminhttp.RegisterTranscodingRoutes(router, adminPath, deps.tgTokenStore, adminIDStore)

	adminhttp.RegisterBrowserPoolRoutes(router, adminPath, deps.tgTokenStore, adminIDStore)
	adminhttp.RegisterBrowserInstallRoute(router, adminPath, deps.tgTokenStore, adminIDStore)

	// Plugin constructor + custom balancers + porter (moved to internal/adminhttp;
	// dynRoutes via the DynRoutes interface).
	adminhttp.RegisterConstructorRoutes(router, adminPath, deps.tgTokenStore, adminIDStore, deps.custBalPool, deps.dynRoutes, deps.cfg)

	// Cluster — manage cascade of lampac-go nodes (moved to internal/adminhttp).
	adminhttp.RegisterClusterRoutes(router, adminPath, deps.tgTokenStore, adminIDStore)

	// TorrServer balancer — pool of backend TorrServers (moved to internal/adminhttp).
	adminhttp.RegisterTorrBalancerRoutes(router, adminPath, deps.tgTokenStore, adminIDStore)

	// JS modules / SISI sources / Music sources editors (moved to internal/adminhttp).
	adminhttp.RegisterJSModulesRoutes(router, adminPath, deps.tgTokenStore, adminIDStore, deps.jsMgr, deps.sisiSources, deps.musicSources)

	// Environment presets (PAC, systemd, helper-скрипты) — moved to internal/adminhttp.
	adminhttp.RegisterEnvPresetsRoutes(router, adminPath, deps.tgTokenStore, adminIDStore, deps.envPresets)

	// FlareSolverr live config (URL + opt-in balancers + cached sessions) —
	// moved to internal/adminhttp.
	adminhttp.RegisterFlaresolverrRoutes(router, adminPath, deps.tgTokenStore, adminIDStore)

	// FanCDN admin endpoints (account injection, ban-clear, status) were
	// part of the old fanserial.me era — removed in 2026-05-25 when the
	// balancer was rewritten on top of lomont.site, which needs no auth.

	adminhttp.RegisterCustomPluginsRoutes(router, adminPath, deps.tgTokenStore, adminIDStore, deps.customPlugins)

	// Groups admin (moved to internal/adminhttp).
	adminhttp.RegisterGroupsRoutes(router, adminPath, deps.tgTokenStore, adminIDStore, deps.groupStore)

	// App-replace + custom-code (moved to internal/adminhttp).
	adminhttp.RegisterAppReplaceRoutes(router, adminPath, deps.tgTokenStore, adminIDStore)

	adminhttp.RegisterCommunityPluginsRoutes(router, adminPath, deps.tgTokenStore, adminIDStore, deps.customPlugins, deps.commCron)

	adminhttp.RegisterLampaRoutes(router, adminPath, deps.tgTokenStore, adminIDStore)

	// Self-update module + reference-config proxy (moved to internal/adminhttp).
	adminhttp.RegisterSelfUpdateRoutes(router, adminPath, deps.tgTokenStore, adminIDStore)
	adminhttp.RegisterRefConfigRoutes(router, adminPath, deps.tgTokenStore, adminIDStore)
	// Self-hosted jacred: status feeds the embedded web UI tab in both panels
	// (moved to internal/adminhttp).
	adminhttp.RegisterJacredStatusRoutes(router, adminPath, deps.tgTokenStore, adminIDStore)
	// Native rutracker indexer: status + staged live probe.
	adminhttp.RegisterRuTrackerRoutes(router, adminPath, deps.tgTokenStore, adminIDStore)
	router.Get("/"+adminPath+"/api/deps", tgAdminDepsHandler(deps.tgTokenStore, adminIDStore))
	router.Post("/"+adminPath+"/api/deps/update", tgAdminDepsUpdateHandler(deps.tgTokenStore, adminIDStore))
	router.Get("/"+adminPath+"/api/deps/update/status", tgAdminDepsUpdateStatusHandler(deps.tgTokenStore, adminIDStore))
	router.Post("/"+adminPath+"/api/deps/install-all", tgAdminDepsInstallAllHandler(deps.tgTokenStore, adminIDStore))
	adminhttp.RegisterInspectorRoutes(router, adminPath, deps.tgTokenStore, adminIDStore)

	// Browser Kit session management.
	// Always register routes so the admin panel doesn't get 404.
	// When Kit is disabled, handlers return empty results.
	adminPathGlobal = adminPath
	if deps.bkitStore != nil {
		bkitSessionsFn := bkitAdminSessionsHandler(deps.bkitStore)
		router.Get("/"+adminPath+"/api/bkit/sessions", bkitSessionsFn)
		router.Post("/"+adminPath+"/api/bkit/sessions", bkitSessionsFn)
		router.Delete("/"+adminPath+"/api/bkit/sessions/*", bkitAdminDeleteSessionHandler(deps.bkitStore))
	} else {
		emptyBkit := func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				writeJSON(w, http.StatusOK, map[string]any{"error": "Kit is disabled. Enable [kit] enable=true in config.toml"})
				return
			}
			writeJSON(w, http.StatusOK, []any{})
		}
		router.Get("/"+adminPath+"/api/bkit/sessions", http.HandlerFunc(emptyBkit))
		router.Post("/"+adminPath+"/api/bkit/sessions", http.HandlerFunc(emptyBkit))
		router.Delete("/"+adminPath+"/api/bkit/sessions/*", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{"error": "Kit is disabled"})
		}))
	}

	// Feedback / Tickets — public submission + admin management.
	feedbackStore = feedback.NewFeedbackStore()
	if deps.tgBot != nil {
		deps.tgBot.SetFeedbackStore(feedbackStore)
	}
	router.Post("/api/feedback", feedback.FeedbackCreateHandler(feedbackStore))
	router.Get("/api/feedback/my", feedback.FeedbackMyHandler(feedbackStore))
	router.Get("/api/feedback/{id}", feedback.FeedbackGetHandler(feedbackStore))
	// Admin feedback endpoints (moved to internal/adminhttp) — the internal
	// Feedback* types stay here, injected via host-side FeedbackOps closures.
	fbOps := adminhttp.FeedbackOps{
		List: func(status, category, priority, search string) any {
			filter := feedback.FeedbackFilter{
				Status:   feedback.FeedbackStatus(status),
				Category: feedback.FeedbackCategory(category),
				Priority: feedback.FeedbackPriority(priority),
				Search:   search,
			}
			tickets := feedbackStore.ListAll(filter)
			return map[string]any{"tickets": tickets, "total": len(tickets)}
		},
		SetStatus: func(ticketID, status string) (int, string) {
			fs := feedback.FeedbackStatus(status)
			if !feedback.ValidFBStatuses[fs] {
				return http.StatusBadRequest, "invalid status"
			}
			if err := feedbackStore.UpdateStatus(ticketID, fs); err != nil {
				return http.StatusNotFound, err.Error()
			}
			return http.StatusOK, ""
		},
		AddReply: func(ticketID, message string) (int, string) {
			if message == "" {
				return http.StatusBadRequest, "message required"
			}
			reply := feedback.FeedbackReply{IsAdmin: true, AdminName: "Администратор", Message: message}
			if err := feedbackStore.AddReply(ticketID, reply); err != nil {
				return http.StatusNotFound, err.Error()
			}
			if deps.tgBot != nil {
				if t, ok := feedbackStore.Get(ticketID); ok {
					if tgID, err := strconv.ParseInt(t.UserID, 10, 64); err == nil && tgID > 0 {
						deps.tgBot.NotifyFeedbackReply(tgID, t.Subject, message)
					}
				}
			}
			return http.StatusOK, ""
		},
		SetPriority: func(ticketID, priority string) (int, string) {
			fp := feedback.FeedbackPriority(priority)
			if !feedback.ValidFBPriorities[fp] {
				return http.StatusBadRequest, "invalid priority"
			}
			if err := feedbackStore.UpdatePriority(ticketID, fp); err != nil {
				return http.StatusNotFound, err.Error()
			}
			return http.StatusOK, ""
		},
		Delete: func(ticketID string) (int, string) {
			if err := feedbackStore.Delete(ticketID); err != nil {
				return http.StatusNotFound, err.Error()
			}
			return http.StatusOK, ""
		},
		Stats: func() any { return feedbackStore.Stats() },
	}
	adminhttp.RegisterFeedbackRoutes(router, adminPath, deps.tgTokenStore, adminIDStore, fbOps)

	// Log viewer API (moved to internal/adminhttp).
	adminhttp.RegisterLogsRoutes(router, adminPath, deps.tgTokenStore, adminIDStore, deps.logBuffer)

	// TMDB cache stats.
	router.Get("/"+adminPath+"/api/tmdb-stats", tmdbStatsHandler(deps.tmdbCacheInst, deps.tmdbPoolInst))

	// Skip Intro admin API (moved to internal/skiphttp).
	if deps.skipDB != nil {
		skiphttp.RegisterAdminRoutes(router, adminPath, deps.tgTokenStore, adminIDStore, deps.skipDB)
	}
	if deps.calStore != nil {
		calendarhttp.RegisterAdminRoutes(router, adminPath, deps.tgTokenStore, adminIDStore, deps.calStore, deps.calCron)
	}

	// Password Auth login/2FA routes (only when TG auth is off).
	if deps.tgTokenStore == nil && deps.pwStore != nil && len(deps.sessionSecret) > 0 {
		adminhttp.RegisterLoginRoutes(router, adminPath, deps.pwStore, deps.sessionSecret)
		adminhttp.RegisterWebAuthnRoutes(router, adminPath, deps.webauthnStore, deps.sessionSecret, deps.cfg.Server.Addr)
	}

	// Mount the v2 premium admin panel + Telegram WebApp under the same
	// admin auth. Coexists with the legacy panel served above — admins can
	// pick whichever they prefer until v2 reaches feature parity.
	adminhttp.RegisterAdminV2Routes(router, adminPath, deps.tgTokenStore, adminIDStore)

	return adminPath, feedbackStore, adminIDStore
}
