package iptvhttp

import (
	"context"
	"net/http"
	"os/exec"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/iptv"
	"lampac-go/internal/proxylink"
	"lampac-go/internal/tgauth"
	"lampac-go/internal/transcodesvc"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

// activeStore holds the IPTV store built at startup so config-reload can update
// its global playlists without a full restart (see ApplyConfigReload). nil when
// IPTV was disabled at startup — enabling it from off still needs a restart,
// since the routes/store/EPG engine are only wired when cfg.IPTV.Enable is set.
var activeStore *iptv.Store

// ApplyConfigReload pushes reloaded [iptv] settings into the live store so edits
// to global_playlists / merge_global take effect immediately. Called from the
// server's config-reload path. No-op if IPTV was off at startup.
func ApplyConfigReload(cfg config.Config) {
	if activeStore == nil {
		return
	}
	activeStore.SetGlobalPlaylists(cfg.IPTV.GlobalPlaylists, cfg.IPTV.MergeGlobalEnabled())
}

// RegisterRoutes wires /api/iptv/* (playlists + channels + EPG + previews) when
// cfg.IPTV.Enable. hostDeps injects the live config / server-ready gate / client-IP
// resolver the moved handlers reach for; the trailing params are the long-lived
// stores/managers they close over. The EPG engine starts immediately with
// background updates on its own interval ticker.
func RegisterRoutes(router chi.Router, cfg config.Config, hostDeps Deps, tgTokenStore *tgauth.Store, proxyLinks *proxylink.Manager, transSvc *transcodesvc.TranscodingService) {
	setDeps(hostDeps)

	// «Свой TorrServer» — НАРОЧНО до гейта [iptv].enable: настройка торрентов, не IPTV, просто
	// живёт рядом с kit-авторизацией. Пишет мини-апп бота, читают клиенты по cookie.
	tsPrefs := newTsPrefStore(cfg.Compat.RepoRoot)
	router.Get("/api/torrserver/pref", tsPrefGetHandler(tsPrefs, func(r *http.Request) int64 { return iptvTgID(r, tgTokenStore) }))
	router.Get("/api/kit/torrserver", tsPrefGetHandler(tsPrefs, resolveKit))
	router.Post("/api/kit/torrserver", tsPrefSetHandler(tsPrefs, resolveKit))

	if !cfg.IPTV.Enable {
		return
	}
	store := iptv.NewStore(cfg.Compat.RepoRoot, iptv.StoreConfig{
		MaxPlaylists:    cfg.IPTV.MaxPlaylists,
		DefaultProxy:    cfg.IPTV.DefaultProxy,
		GlobalPlaylists: cfg.IPTV.GlobalPlaylists,
		MergeGlobal:     cfg.IPTV.MergeGlobalEnabled(),
		Registry:        cfg.IPTV.Registry,
		RegistryName:    cfg.IPTV.RegistryName,
		RegistryProxy:   cfg.IPTV.RegistryProxy,
		RegistryAutoAdd: cfg.IPTV.RegistryAutoAdd,
		RegistryOnly:    cfg.IPTV.RegistryOnly,
		PreferOfficial:  cfg.IPTV.PreferOfficialEnabled(),
		OfficialHosts:   cfg.IPTV.OfficialHosts,
	})
	activeStore = store

	// Аттестация официального приложения (X-App-Proof) на клиентских ручках —
	// off/log/enforce, см. [iptv].app_attest. Ключ подписи стрим-ссылок —
	// серверный секрет, живёт в database/iptv/stream.key.
	attest := appProofMiddleware(cfg.IPTV)
	streamKey := loadStreamKey(cfg.Compat.RepoRoot)

	// Cookie-авторизация: web-клиент и нативы.
	byCookie := func(r *http.Request) int64 { return iptvTgID(r, tgTokenStore) }
	// Личные настройки IPTV (пока одна: прятать встроенные плейлисты).
	iptvPrefs := newIptvPrefStore(cfg.Compat.RepoRoot)
	router.Get("/api/iptv/prefs", iptvPrefsHandler(iptvPrefs, byCookie))
	router.Post("/api/iptv/prefs", iptvPrefsHandler(iptvPrefs, byCookie))
	router.Get("/api/kit/iptv/prefs", iptvPrefsHandler(iptvPrefs, resolveKit))
	router.Post("/api/kit/iptv/prefs", iptvPrefsHandler(iptvPrefs, resolveKit))
	router.Get("/api/iptv/playlists", iptvPlaylistsHandler(store, byCookie, iptvPrefs))
	router.Post("/api/iptv/playlists", iptvAddPlaylistHandler(store, byCookie))
	router.Delete("/api/iptv/playlists/{id}", iptvDeletePlaylistHandler(store, byCookie))
	router.Post("/api/iptv/playlists/{id}/refresh", iptvRefreshPlaylistHandler(store, byCookie))
	router.Patch("/api/iptv/playlists/{id}", iptvPatchPlaylistHandler(store, byCookie))

	// Те же операции для мини-аппа бота — вводить ссылки и разбираться со списками
	// в Telegram удобнее, чем на телевизоре. Регистрируем безусловно: роуты
	// фиксируются на старте, а до вызова SetKitResolver они отвечают 401.
	router.Get("/api/kit/iptv/playlists", iptvPlaylistsHandler(store, resolveKit, iptvPrefs))
	router.Post("/api/kit/iptv/playlists", iptvAddPlaylistHandler(store, resolveKit))
	router.Delete("/api/kit/iptv/playlists/{id}", iptvDeletePlaylistHandler(store, resolveKit))
	router.Post("/api/kit/iptv/playlists/{id}/refresh", iptvRefreshPlaylistHandler(store, resolveKit))
	router.Patch("/api/kit/iptv/playlists/{id}", iptvPatchPlaylistHandler(store, resolveKit))

	epgUpdateHours := cfg.IPTV.EPGUpdateHours
	if epgUpdateHours <= 0 {
		epgUpdateHours = 6
	}
	epg := iptv.NewEPGEngine(store, iptv.EPGConfig{
		URLs:           cfg.IPTV.EPGUrls,
		UpdateInterval: time.Duration(epgUpdateHours) * time.Hour,
	})
	// epg.Start НЕ здесь: первый refresh строит tvg-фильтр и алиас-подсказки из
	// каналов доноров/реестра — стартуем его ПОСЛЕ RefreshGlobal + посева (ниже),
	// иначе первые 6 часов гид работает по пустым/протухшим кэшам.

	router.With(attest).Get("/api/iptv/channels", iptvChannelsHandler(store, tgTokenStore, epg, proxyLinks))
	router.With(attest).Get("/api/iptv/groups", iptvGroupsHandler(store, tgTokenStore))
	router.With(attest).Get("/api/iptv/play", iptvPlayHandler(cfg, store, tgTokenStore, epg, proxyLinks, transSvc))
	// Дисковый кэш логотипов нужен только каналам реестра (own_*).
	var logos *logoCache
	if store.Registry() != nil {
		logos = newLogoCache(cfg.Compat.RepoRoot)
	}
	router.Get("/api/iptv/logo", iptvLogoHandler(store, tgTokenStore, epg, logos))

	// Свой реестр каналов: стабильные стрим-URL + экспорт M3U. Обе ручки закрыты
	// токеном и HMAC-подписью (см. iptv_registry.go); экспорт дополнительно
	// требует [iptv].registry_export=true.
	if store.Registry() != nil {
		router.With(attest).Get("/api/iptv/stream/{id}", iptvStreamHandler(store, tgTokenStore, streamKey, proxyLinks))
		router.With(attest).Get("/api/iptv/export.m3u", iptvExportM3UHandler(cfg, store, tgTokenStore, streamKey))
	}

	// Live channel-card thumbnails (opt-in): grab one frame per channel via ffmpeg.
	// Only build the cache when enabled AND ffmpeg resolves; a nil cache → 404.
	var previewCache *iptvPreviewCache
	if cfg.IPTV.Previews {
		ffmpeg := cfg.Transcoding.FFmpeg
		if ffmpeg == "" {
			ffmpeg = "ffmpeg"
		}
		if _, err := exec.LookPath(ffmpeg); err != nil {
			log.Warn().Str("ffmpeg", ffmpeg).Msg("iptv: previews enabled but ffmpeg not found — disabling")
		} else {
			previewCache = newIPTVPreviewCache(ffmpeg, cfg.IPTV.PreviewConcurrency, cfg.IPTV.PreviewTTLSec)
			log.Info().Int("concurrency", cfg.IPTV.PreviewConcurrency).Int("ttl_sec", cfg.IPTV.PreviewTTLSec).Msg("iptv: live previews enabled")
		}
	}
	router.Get("/api/iptv/preview", iptvPreviewHandler(previewCache, store, tgTokenStore))

	router.Get("/api/iptv/epg/now", iptvEPGNowHandler(epg, store))
	router.Get("/api/iptv/epg/timeline", iptvEPGTimelineHandler(epg, store))
	router.Get("/api/iptv/epg/range", iptvEPGRangeHandler(epg, store))
	router.Get("/api/iptv/epg/search", iptvEPGSearchHandler(epg))
	router.Get("/api/iptv/epg/status", iptvEPGStatusHandler(epg))

	go func() {
		store.RefreshGlobal()
		// Пустой включённый реестр — первый запуск: засеваем из глобальных
		// списков, чтобы «Мои каналы» появились без единого ручного шага.
		store.SeedRegistryIfEmpty()
		// Доноры скачаны, реестр посеян — теперь EPG видит настоящие tvg-id и
		// имена каналов для алиасинга чужих id-схем.
		epg.Start(context.Background())
		// Периодический перечит всех плейлистов — иначе токены Xtream-панелей в
		// URL каналов протухают, и каналы «умирают» до ручного обновления
		// (другие IPTV-плееры перечитывают m3u при каждом запуске — мы теперь тоже).
		if cfg.IPTV.RefreshHours < 0 {
			return
		}
		interval := time.Duration(cfg.IPTV.RefreshHours) * time.Hour
		if interval == 0 {
			interval = 6 * time.Hour
		}
		for {
			time.Sleep(interval)
			store.RefreshAll()
		}
	}()

	// Opt-in liveness health-check: probe global streams, drop dead/duplicate ones from the merged list.
	if cfg.IPTV.HealthCheck {
		hcMin := cfg.IPTV.HealthCheckMin
		if hcMin <= 0 {
			hcMin = 30
		}
		// Deep by default: walk an HLS channel to its first segment. The shallow
		// two-byte read calls a channel alive as long as its web server answers,
		// which is how dead channels stayed in the list for weeks.
		store.SetHealthLimits(cfg.IPTV.HealthCheckMax, cfg.IPTV.HealthCheckConc)
		store.SetHealthDepth(!cfg.IPTV.HealthCheckShallow, cfg.IPTV.HealthCheckStale)
		store.StartHealthCheck(context.Background(), time.Duration(hcMin)*time.Minute)
	}

	// Playlist-sharing protection: when default_proxy != "all", channels that are https with no
	// User-Agent/Referer requirement are handed to the client as raw URLs → the user's device hits
	// the upstream provider directly → many client IPs on one account → the provider bans the
	// playlist as "shared". "all" forces every channel through the server's single egress IP.
	if cfg.IPTV.DefaultProxy != "all" {
		log.Warn().Str("default_proxy", cfg.IPTV.DefaultProxy).Msg("iptv: default_proxy != \"all\" — client IPs may reach upstream providers directly (playlist-sharing risk). Set [iptv] default_proxy=\"all\" to appear as one device.")
	}

	users, playlists := store.Stats()
	log.Info().Int("users", users).Int("playlists", playlists).Bool("merge_global", cfg.IPTV.MergeGlobalEnabled()).Msg("iptv: loaded")
}
