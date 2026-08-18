package httpapi

import (
	"lampac-go/internal/config"
	"lampac-go/internal/watchparty"

	"github.com/go-chi/chi/v5"
)

// registerPluginJSRoutes wires all client-side Lampa plugin .js routes.
// Two route shapes are registered for each plugin:
//   - /{name}.js              — plain URL, no auth token
//   - /{name}/js/{token}      — token-embedded URL (TG auth / device binding)
//
// genericPluginJSHandler is stateless (captures only cfg + filename) so one
// handler instance is shared between both routes. Plugins with custom
// per-request logic (external_player, sync, tmdbproxy, ads, sisi)
// keep dedicated handlers.
func registerPluginJSRoutes(router chi.Router, cfg config.Config) {
	generic := func(name string) {
		h := genericPluginJSHandler(name, "", cfg)
		router.Get("/"+name, h)
		stem := name
		if n := len(name) - len(".js"); n > 0 && name[n:] == ".js" {
			stem = name[:n]
		}
		router.Get("/"+stem+"/js/{token}", h)
	}

	generic("ts.js")
	generic("catalog.js")
	generic("torrent_styles_v2.js")

	router.Get("/external_player.js", externalPlayerJSHandler(cfg))
	router.Get("/external_player/js/{token}", externalPlayerJSHandler(cfg))

	router.Get("/sync.js", syncJSHandler(cfg))
	router.Get("/sync/js/{token}", syncJSHandler(cfg))

	generic("backup.js")
	generic("bookmark.js")
	generic("timecode.js")
	// syncpro.js — unified plugin that supersedes sync.js + bookmark.js +
	// timecode.js + backup.js. Served generically because the only
	// templating it needs is {localhost} + {token}, both handled by
	// genericPluginJSHandler.
	generic("syncpro.js")

	router.Get("/tmdbproxy.js", tmdbProxyJSHandler(cfg))
	router.Get("/tmdbproxy/js/{token}", tmdbProxyJSHandler(cfg))

	generic("cubproxy.js")
	generic("youtube_feed.js")
	generic("migrate.js")
	generic("skip_intro.js")
	generic("calendar.js")
	generic("xsearch.js")
	generic("collections.js")
	generic("iptv2.js")
	generic("theme.js")
	generic("netflix_ui.js")
	generic("screensaver.js")
	generic("remote.js")
	generic("stats.js")
	generic("opensubs.js")
	generic("failover.js")
	generic("dualsubs.js")
	generic("player_redesign.js")
	generic("hls_tracks.js")
	generic("voice_switcher.js")
	generic("webplayer.js")

	// Static runtime assets for the web player (JASSUB worker + wasm for
	// on-device ASS subtitle rendering). Served from plugins/webplayer-assets/
	// next to webplayer.js; the path is already whitelisted in the tg-auth gate.
	router.Get("/webplayer/assets/{name}", webplayerAssetHandler(cfg))

	// Watchparty WS relay — see internal/httpapi/watchparty_api.go.
	router.Get("/webplayer/ws/watchparty/{roomId}", watchparty.WatchpartyHandler)
	// Health probe — client pre-flight so it can distinguish "server has no
	// watchparty route" (404) from "WS upgrade blocked" (200 ok here but WS
	// still fails = reverse proxy dropping Upgrade headers). Also register
	// HEAD so `curl -I` returns 200 for operator sanity checks.
	router.Get("/webplayer/health/watchparty", watchparty.WatchpartyHealthHandler)
	router.Head("/webplayer/health/watchparty", watchparty.WatchpartyHealthHandler)

	generic("cdn_direct.js")
	generic("anti-dmca.js")
	generic("auth_gate_plugin.js")
	generic("backup_sync_key.js")
	generic("server_widget.js")

	router.Get("/ads.js", adsJSHandler())
	router.Get("/ads/js/{token}", adsJSHandler())

	router.Get("/sisi.js", sisiJSHandler(cfg))
	router.Get("/sisi/js/{token}", sisiJSHandler(cfg))
}
