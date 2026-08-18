package adminhttp

import (
	stdjson "encoding/json"
	"io"
	"maps"
	"net/http"
	"strings"

	"lampac-go/internal/config"
	"lampac-go/internal/tgauth"
	"lampac-go/internal/torrs"
)

// --- Plugins API ---

func tgAdminPluginsHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		switch r.Method {
		case http.MethodGet:
			root := loadMergedConf()
			// initPlugins
			ip := map[string]bool{}
			if lw, ok := root["LampaWeb"].(map[string]any); ok {
				if plugins, ok := lw["initPlugins"].(map[string]any); ok {
					for _, name := range []string{
						"dlna", "tracks", "transcoding", "tmdb_proxy", "online", "catalog", "sisi",
						"torrserver", "backup", "sync", "bookmark", "timecode", "ads_free", "youtube_feed",
						"stats", "opensubs", "failover", "dualsubs", "player_redesign", "hls_tracks", "voice_switcher",
						"theme", "screensaver", "remote", "external_player", "migrate",
						"web_player", "web_player_android",
					} {
						ip[name] = toBoolAny(plugins[name])
					}
				}
			}
			// sisi settings
			sisi := map[string]any{}
			if ss, ok := root["sisi"].(map[string]any); ok {
				maps.Copy(sisi, ss)
			}
			// torrserver settings
			ts := map[string]any{}
			if tsConf, ok := root["TorrServer"].(map[string]any); ok {
				maps.Copy(ts, tsConf)
			}
			// Inject live config values so the admin panel shows actual state.
			ts["_embedded"] = torrs.IsAvailable()
			if serverReady() {
				tsCfg := liveConfig(config.Config{}).TorrServer
				if tsCfg.Password != "" {
					ts["_password_loaded"] = true
				}
				// Fill from live config if not already in JSON.
				if tsCfg.URL != "" {
					if _, ok := ts["url"]; !ok {
						ts["url"] = tsCfg.URL
					}
				}
				// Always expose TOML-sourced fields for the UI.
				if _, ok := ts["port"]; !ok {
					ts["port"] = tsCfg.Port
				}
				if _, ok := ts["cache_size_mb"]; !ok {
					ts["cache_size_mb"] = tsCfg.CacheSizeMB
				}
				if _, ok := ts["disk_cache_mb"]; !ok {
					ts["disk_cache_mb"] = tsCfg.DiskCacheMB
				}
				if _, ok := ts["ram_cache"]; !ok {
					ts["ram_cache"] = tsCfg.RAMCache
				}
				if _, ok := ts["preload_mb"]; !ok {
					ts["preload_mb"] = tsCfg.PreloadMB
				}
				if _, ok := ts["disable_dht"]; !ok {
					ts["disable_dht"] = tsCfg.DisableDHT
				}
				if _, ok := ts["disable_upload"]; !ok {
					ts["disable_upload"] = tsCfg.DisableUpload
				}
				if _, ok := ts["max_download_speed_mb"]; !ok {
					ts["max_download_speed_mb"] = tsCfg.MaxDownloadSpeedMB
				}
				if _, ok := ts["max_upload_speed_mb"]; !ok {
					ts["max_upload_speed_mb"] = tsCfg.MaxUploadSpeedMB
				}
				if _, ok := ts["max_active_torrents"]; !ok {
					ts["max_active_torrents"] = tsCfg.MaxActiveTorrents
				}
				if _, ok := ts["cache_cleanup_enable"]; !ok {
					ts["cache_cleanup_enable"] = tsCfg.CacheCleanupEnable
				}
				if _, ok := ts["cache_cleanup_days"]; !ok {
					ts["cache_cleanup_days"] = tsCfg.CacheCleanupDays
				}
				if _, ok := ts["cache_cleanup_max_gb"]; !ok {
					ts["cache_cleanup_max_gb"] = tsCfg.CacheCleanupMaxGB
				}
			}
			// sync settings
			syncConf := map[string]any{}
			if sc, ok := root["sync"].(map[string]any); ok {
				maps.Copy(syncConf, sc)
			}
			// transcoding settings
			transConf := map[string]any{}
			if tc, ok := root["transcoding"].(map[string]any); ok {
				maps.Copy(transConf, tc)
			}
			// tmdb_proxy settings (from TOML config)
			tmdbConf := map[string]any{"mode": "self", "host": "tmdb.alcopa.cc"}
			if serverReady() {
				tp := liveConfig(config.Config{}).TMDBProxy
				if tp.Mode != "" {
					tmdbConf["mode"] = tp.Mode
				}
				if tp.Host != "" {
					tmdbConf["host"] = tp.Host
				}
				tmdbConf["api_key"] = tp.APIKey
				tmdbConf["api_host"] = tp.APIHost
				tmdbConf["img_host"] = tp.IMGHost
				tmdbConf["cache_max_items"] = tp.CacheMaxItems
				tmdbConf["cache_ttl_min"] = tp.CacheTTLMin
				tmdbConf["upstream_timeout_sec"] = tp.UpstreamTimeoutSec
			}
			// iptv settings
			iptvConf := map[string]any{}
			if serverReady() {
				ic := liveConfig(config.Config{}).IPTV
				iptvConf["enable"] = ic.Enable
				iptvConf["epg_urls"] = ic.EPGUrls
				iptvConf["epg_update_hours"] = ic.EPGUpdateHours
				iptvConf["max_playlists"] = ic.MaxPlaylists
				iptvConf["default_proxy"] = ic.DefaultProxy
				iptvConf["global_playlists"] = ic.GlobalPlaylists
			}
			// antidpi settings
			antidpiConf := map[string]any{
				"enable":   false,
				"strategy": "auto",
				"listen":   "127.0.0.1:9898",
			}
			if serverReady() {
				ac := liveConfig(config.Config{}).AntiDPI
				antidpiConf["enable"] = ac.Enable
				if ac.Strategy != "" {
					antidpiConf["strategy"] = ac.Strategy
				}
				if ac.Listen != "" {
					antidpiConf["listen"] = ac.Listen
				}
				if ad := liveAntiDPI(); ad != nil {
					antidpiConf["status"] = ad.Stats()
				}
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"initPlugins": ip,
				"sisi":        sisi,
				"torrserver":  ts,
				"sync":        syncConf,
				"transcoding": transConf,
				"tmdb_proxy":  tmdbConf,
				"iptv":        iptvConf,
				"antidpi":     antidpiConf,
			})

		case http.MethodPost:
			body, _ := io.ReadAll(io.LimitReader(r.Body, 65536))
			var req struct {
				InitPlugins map[string]bool `json:"initPlugins,omitempty"`
				Sisi        map[string]any  `json:"sisi,omitempty"`
				TorrServer  map[string]any  `json:"torrserver,omitempty"`
				Sync        map[string]any  `json:"sync,omitempty"`
				Transcoding map[string]any  `json:"transcoding,omitempty"`
				TMDBProxy   map[string]any  `json:"tmdb_proxy,omitempty"`
				IPTV        map[string]any  `json:"iptv,omitempty"`
				AntiDPI     map[string]any  `json:"antidpi,omitempty"`
			}
			if stdjson.Unmarshal(body, &req) != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
				return
			}

			// TMDB proxy settings go to TOML config (not legacy JSON).
			if len(req.TMDBProxy) > 0 {
				if err := updateConfigTOMLMap(func(root map[string]any) {
					section := setNestedMap(root, "tmdb_proxy")
					if m, ok := req.TMDBProxy["mode"].(string); ok && m != "" {
						section["mode"] = strings.TrimSpace(m)
					}
					if h, ok := req.TMDBProxy["host"].(string); ok && h != "" {
						section["host"] = strings.TrimSpace(h)
					}
					if v, ok := req.TMDBProxy["api_key"].(string); ok {
						section["api_key"] = strings.TrimSpace(v)
					}
					if v, ok := req.TMDBProxy["api_host"].(string); ok {
						section["api_host"] = strings.TrimSpace(v)
					}
					if v, ok := req.TMDBProxy["img_host"].(string); ok {
						section["img_host"] = strings.TrimSpace(v)
					}
					if v, ok := req.TMDBProxy["cache_max_items"].(float64); ok && v > 0 {
						section["cache_max_items"] = int(v)
					}
					if v, ok := req.TMDBProxy["cache_ttl_min"].(float64); ok && v > 0 {
						section["cache_ttl_min"] = int(v)
					}
					if v, ok := req.TMDBProxy["upstream_timeout_sec"].(float64); ok && v > 0 {
						section["upstream_timeout_sec"] = int(v)
					}
				}); err != nil {
					writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
					return
				}
			}

			// IPTV settings go to TOML config.
			if len(req.IPTV) > 0 {
				if err := updateConfigTOMLMap(func(root map[string]any) {
					section := setNestedMap(root, "iptv")
					if v, ok := req.IPTV["enable"].(bool); ok {
						section["enable"] = v
					}
					if v, ok := req.IPTV["epg_update_hours"].(float64); ok && v > 0 {
						section["epg_update_hours"] = int(v)
					}
					if v, ok := req.IPTV["max_playlists"].(float64); ok && v > 0 {
						section["max_playlists"] = int(v)
					}
					if v, ok := req.IPTV["default_proxy"].(string); ok {
						section["default_proxy"] = strings.TrimSpace(v)
					}
					if urls, ok := req.IPTV["epg_urls"].([]any); ok {
						var out []string
						for _, u := range urls {
							if s, ok := u.(string); ok && strings.TrimSpace(s) != "" {
								out = append(out, strings.TrimSpace(s))
							}
						}
						section["epg_urls"] = out
					}
					if gp, ok := req.IPTV["global_playlists"].([]any); ok {
						var out []string
						for _, p := range gp {
							if s, ok := p.(string); ok && strings.TrimSpace(s) != "" {
								out = append(out, strings.TrimSpace(s))
							}
						}
						section["global_playlists"] = out
					}
				}); err != nil {
					writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
					return
				}
			}

			// TorrServer settings go to TOML config (new fields: cache, limits, cleanup).
			if len(req.TorrServer) > 0 {
				if err := updateConfigTOMLMap(func(root map[string]any) {
					section := setNestedMap(root, "torrserver")
					// Strings
					for _, k := range []string{"url", "login", "password"} {
						if v, ok := req.TorrServer[k].(string); ok {
							section[k] = v
						}
					}
					// Ints
					for _, k := range []string{
						"port", "cache_size_mb", "disk_cache_mb", "preload_mb",
						"max_download_speed_mb", "max_upload_speed_mb", "max_active_torrents",
						"cache_cleanup_days", "cache_cleanup_max_gb",
					} {
						if v, ok := req.TorrServer[k].(float64); ok {
							section[k] = int(v)
						}
					}
					// Bools
					for _, k := range []string{"disable_dht", "disable_upload", "cache_cleanup_enable", "ram_cache"} {
						if v, ok := req.TorrServer[k].(bool); ok {
							section[k] = v
						}
					}
				}); err != nil {
					writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
					return
				}
			}

			// AntiDPI settings go to TOML config.
			if len(req.AntiDPI) > 0 {
				if err := updateConfigTOMLMap(func(root map[string]any) {
					section := setNestedMap(root, "antidpi")
					if v, ok := req.AntiDPI["enable"].(bool); ok {
						section["enable"] = v
					}
					if v, ok := req.AntiDPI["strategy"].(string); ok && v != "" {
						section["strategy"] = strings.TrimSpace(v)
					}
					if v, ok := req.AntiDPI["listen"].(string); ok && v != "" {
						section["listen"] = strings.TrimSpace(v)
					}
				}); err != nil {
					writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
					return
				}
			}

			err := updateInitConfMap(func(root map[string]any) {
				if len(req.InitPlugins) > 0 {
					lw := ensureMapChild(root, "LampaWeb")
					plugins := ensureMapChild(lw, "initPlugins")
					for k, v := range req.InitPlugins {
						plugins[k] = v
					}
				}
				if len(req.Sisi) > 0 {
					sisi := ensureMapChild(root, "sisi")
					maps.Copy(sisi, req.Sisi)
				}
				if len(req.TorrServer) > 0 {
					ts := ensureMapChild(root, "TorrServer")
					maps.Copy(ts, req.TorrServer)
					// JSON decodes numbers as float64 — numeric fields must be int for TOML.
					for _, k := range []string{
						"port", "cache_size_mb", "disk_cache_mb", "preload_mb",
						"max_download_speed_mb", "max_upload_speed_mb", "max_active_torrents",
						"cache_cleanup_days", "cache_cleanup_max_gb",
					} {
						if v, ok := ts[k].(float64); ok {
							ts[k] = int(v)
						}
					}
					// Remove internal-only fields from API response meta.
					delete(ts, "_embedded")
					delete(ts, "_password_loaded")
				}
				if len(req.Sync) > 0 {
					sc := ensureMapChild(root, "sync")
					maps.Copy(sc, req.Sync)
				}
				if len(req.Transcoding) > 0 {
					tc := ensureMapChild(root, "transcoding")
					maps.Copy(tc, req.Transcoding)
					// JSON decodes numbers as float64 — numeric fields must be int.
					for _, k := range []string{"maxConcurrentJobs", "idleTimeoutSec", "idleTimeoutSec_live"} {
						if v, ok := tc[k].(float64); ok {
							tc[k] = int(v)
						}
					}
				}
			})
			if err != nil {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "restart_needed": false})

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

// tgAdminTorrServerTestHandler tests connectivity to a TorrServer instance.
// POST /admin/api/torrserver/test {url, login, password}
func tgAdminTorrServerTestHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		var req struct {
			URL      string `json:"url"`
			Login    string `json:"login"`
			Password string `json:"password"`
		}
		if stdjson.Unmarshal(body, &req) != nil || strings.TrimSpace(req.URL) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "url is required"})
			return
		}

		alive, msg := checkTSHealthWithAuth(
			strings.TrimSpace(req.URL),
			strings.TrimSpace(req.Login),
			strings.TrimSpace(req.Password),
		)

		resp := map[string]any{
			"ok":      alive,
			"message": msg,
		}

		// If not alive, include process manager status for diagnostics.
		if !alive {
			resp["process"] = torrServerProcessStatus()
		}

		writeJSON(w, http.StatusOK, resp)
	}
}
