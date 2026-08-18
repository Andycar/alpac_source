package httpapi

import (
	"lampac-go/internal/adminhttp"
	"lampac-go/internal/config"
	"lampac-go/internal/kit"
	"lampac-go/internal/tgauth"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

// registerKitRoutes wires /kit, /bkit, and /api/kit/* when Kit is enabled.
// Returns the BKit session store so admin routes can inspect/delete sessions.
// Also wires package-level bkitSessions and kitTGTokenStore for middleware.
func registerKitRoutes(router chi.Router, cfg config.Config, kitStore *kit.Store, tgTokenStore *tgauth.Store, tgLangStore *tgauth.LangStore) *BKitSessionStore {
	if kitStore == nil || !cfg.Kit.Enable {
		return nil
	}

	router.Get("/kit", kitPageHandler(kitStore, cfg))
	router.Get("/api/kit/config", kitConfigGetHandler(kitStore, cfg, tgLangStore))
	router.Post("/api/kit/config", kitConfigSaveHandler(kitStore, cfg))

	// Source-account binding routes ("Привязки" tab). Grouped behind a live
	// kill-switch: when [kit] binds_disabled is set, kitBindsGuard rejects the
	// whole group with 403 so the feature is truly unavailable, not just hidden
	// in the UI. Registered unconditionally so an admin can flip the toggle
	// without a restart (route registration is startup-fixed).
	router.Group(func(br chi.Router) {
		br.Use(kitBindsGuard(cfg))
		br.Delete("/api/kit/bind/{service}", kitDeleteBindHandler(kitStore, cfg))
		br.Post("/api/kit/bind/filmix/start", kitBindFilmixStartHandler(kitStore, cfg))
		br.Post("/api/kit/bind/filmix/finish", kitBindFilmixFinishHandler(kitStore, cfg))
		br.Post("/api/kit/bind/kinopub/start", kitBindKinopubStartHandler(kitStore, cfg))
		br.Post("/api/kit/bind/kinopub/finish", kitBindKinopubFinishHandler(kitStore, cfg))
		br.Post("/api/kit/bind/rezka", kitBindRezkaHandler(kitStore, cfg))
		br.Post("/api/kit/bind/rezka/cookie", kitBindRezkaCookieHandler(kitStore, cfg))
		br.Post("/api/kit/bind/vokino", kitBindVokinoHandler(kitStore, cfg))
		br.Post("/api/kit/bind/getstv", kitBindGetsTVHandler(kitStore, cfg))
		br.Post("/api/kit/bind/iptvonline", kitBindIptvOnlineHandler(kitStore, cfg))
		br.Post("/api/kit/bind/pornlab", kitBindPornLabHandler(kitStore, cfg))
	})

	router.Get("/api/kit/balancers", kitBalancersHandler(kitStore, cfg))
	router.Get("/api/kit/profile", kitProfileHandler(cfg))

	// Sync-profile CRUD — kit-auth wrappers around the profile store.
	// See kit_api.go for why these duplicate /api/profile/owned/* (TG
	// WebApp uses initData header, /api/profile/* uses lampac_token).
	router.Get("/api/kit/profile-owned", kitProfileOwnedListHandler(cfg))
	router.Post("/api/kit/profile-owned/create", kitProfileOwnedCreateHandler(cfg))
	router.Post("/api/kit/profile-owned/set-pin", kitProfileOwnedSetPINHandler(cfg))
	router.Post("/api/kit/profile-owned/delete", kitProfileOwnedDeleteHandler(cfg))

	// Browser Kit (standalone web UI, no TG bot required).
	bkitStore := NewBKitSessionStore(cfg.Compat.RepoRoot)
	bkitSessions = bkitStore       // set package-level var for kitAuthFromRequest
	kitTGTokenStore = tgTokenStore // allow TG cookie auth in Kit API
	router.Get("/bkit", browserKitPageHandler(kitStore, cfg))
	log.Info().Msg("browser kit enabled at /bkit")
	return bkitStore
}

// registerRemoteControlRoutes wires /remote and /api/remote/* (TG Mini App).
// Works without Kit — only requires a TG token store to validate sessions.
func registerRemoteControlRoutes(router chi.Router, cfg config.Config, tgTokenStore *tgauth.Store) {
	router.Get("/remote", remotePageHandler(cfg))
	if tgTokenStore != nil {
		router.Post("/api/remote/auth", remoteAuthHandler(tgTokenStore, cfg))
		router.Post("/api/remote/devices", remoteDevicesHandler(tgTokenStore, cfg))
		log.Info().Msg("remote control enabled at /remote")
	}
}

// registerTGAuthRoutes wires /tg/auth/* + /api/user/info + /tg/device/* +
// promo redemption. Promo is registered in the tgPending branch OR
// separately when standalone TG auth is on.
func registerTGAuthRoutes(router chi.Router, cfg config.Config, tgPending *tgauth.PendingStore, tgDevicePending *tgauth.DevicePendingStore, tgTokenStore *tgauth.Store, promoStore *tgauth.PromoStore, banStore *tgauth.BanStore, version string, cubVal *cubAuthValidator) {
	if tgPending != nil {
		router.Get("/tg/auth", tgAuthPageHandler(tgPending, cfg.TelegramAuth.BotName))
		router.Get("/tg/auth/code", tgAuthCodeHandler(tgPending, cfg.TelegramAuth.BotName))
		router.Get("/tg/auth/check", tgAuthCheckHandler(tgPending))
		router.Get("/tg/auth/check.js", tgAuthCheckJSHandler(tgPending))
		router.Get("/tg/auth/complete", tgAuthCompleteHandler(tgTokenStore))
		router.Get("/tg/auth/logout", tgAuthLogoutHandler())
		router.Get("/tg/auth/status", tgAuthStatusHandler(tgTokenStore, tgPending, cfg.TelegramAuth.BotName, cubVal))
		router.Get("/tg/auth/bind-device", tgAuthBindDeviceHandler(tgTokenStore))
		router.Post("/tg/auth/promo", adminhttp.PromoRedeemHandler(promoStore, tgTokenStore, banStore))
	}
	// User info endpoint — works with or without TG auth (for SURS and similar plugins).
	router.Get("/api/user/info", userInfoHandler(tgTokenStore, version))
	// Promo redemption also works without TG auth (standalone mode).
	if tgPending == nil && tgTokenStore != nil {
		router.Post("/tg/auth/promo", adminhttp.PromoRedeemHandler(promoStore, tgTokenStore, banStore))
	}
	if tgDevicePending != nil {
		router.Get("/tg/device/verify", tgDeviceVerifyHandler(tgDevicePending, cfg.TelegramAuth.BotName))
		router.Get("/tg/device/check", tgDeviceCheckHandler(tgDevicePending, tgTokenStore))
	}
}

// registerPasswordAuthRoutes wires the password-login endpoints + the
// public auth-mode probe. Called regardless of the active mode — the
// handlers themselves gate on cfg.Auth.{Mode, Password.Enable} so the
// admin can flip the mode without re-registering routes.
func registerPasswordAuthRoutes(router chi.Router, pwUserStore *tgauth.PasswordUserStore, cfgPtr func() *config.Config) {
	if pwUserStore == nil {
		return
	}
	router.Post("/auth/password/login", passwordLoginHandler(pwUserStore, cfgPtr))
	router.Post("/auth/password/logout", passwordLogoutHandler(pwUserStore))
	router.Get("/auth/password/logout", passwordLogoutHandler(pwUserStore))
	router.Get("/auth/password/me", passwordMeHandler(pwUserStore))
	router.Get("/auth/mode", authModeHandler(cfgPtr))
	log.Info().Msg("password auth routes enabled at /auth/password/*")
}

