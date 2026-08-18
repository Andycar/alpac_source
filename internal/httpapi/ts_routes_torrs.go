//go:build torrs

package httpapi

import (
	"crypto/rand"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/torrs"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

// torrsServer is the in-process torrent server instance.
var torrsServer *torrs.BTServer

// maxTorrsBodyBytes caps POST body size on the in-process torrs endpoints.
// 1 MiB is comfortably above any legitimate TorrentsRequest / SettingsRequest /
// CacheRequest payload but bounds OOM exposure from a hostile client.
const maxTorrsBodyBytes int64 = 1 << 20

// registerTSRoutes registers TorrServer routes.
// When built with torrs tag AND no external URL: direct in-process handlers.
// Otherwise: falls back to reverse proxy.
func registerTSRoutes(router chi.Router, cfg config.Config) {
	inProcess := initTorrs(cfg)
	if !inProcess {
		registerTSProxyRoutes(router, cfg)
		return
	}

	// In-process mode: direct handlers on /ts/* paths with access control.
	// External Basic-Auth runs first so its ctx tag is visible to the
	// per-token guard below.
	router.Group(func(r chi.Router) {
		r.Use(tsExternalAuthMiddleware)
		r.Use(tsAccessMiddleware)
		// Read-only endpoints — open to anon (current behaviour).
		r.Get("/ts/echo", tsDirectEchoHandler)
		r.Get("/ts/stream", tsDirectStreamHandler())
		r.Get("/ts/stream/*", tsDirectStreamHandler())
		r.Get("/ts/playlist", tsDirectPlaylistHandler())
		r.Get("/ts/download/{size}", tsDirectDownloadHandler())
		r.Get("/ts", tsInProcessUIHandler)

		// /settings is registered OUTSIDE the blanket auth group: the read
		// probe ({action:"get"}) — Lampa's TorrServer liveness check — must
		// answer openly like a real MatriX server, while {action:"set"} is
		// gated inside tsDirectSettingsHandler via tsAuthOK.
		r.Post("/ts/settings", tsDirectSettingsHandler())

		// Mutating endpoints — require an authenticated user when TG auth is wired.
		r.Group(func(rr chi.Router) {
			rr.Use(tsRequireTokenMiddleware)
			rr.Post("/ts/torrents", tsDirectTorrentsHandler())
			rr.Post("/ts/cache", tsDirectCacheHandler())
			rr.Get("/ts/playlistall/all.m3u", tsDirectPlaylistAllHandler())
		})

		r.HandleFunc("/ts/*", func(w http.ResponseWriter, rr *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{})
		})

		// Without /ts prefix (pidtor and external clients).
		r.Get("/echo", tsDirectEchoHandler)
		r.Get("/stream", tsDirectStreamHandler())
		r.Get("/stream/*", tsDirectStreamHandler())
		r.Get("/playlist", tsDirectPlaylistHandler())
		r.Get("/download/{size}", tsDirectDownloadHandler())
		r.Post("/settings", tsDirectSettingsHandler())
		r.Group(func(rr chi.Router) {
			rr.Use(tsRequireTokenMiddleware)
			rr.Post("/torrents", tsDirectTorrentsHandler())
			rr.Post("/cache", tsDirectCacheHandler())
			rr.Get("/playlistall/all.m3u", tsDirectPlaylistAllHandler())
		})
	})

	log.Info().Msg("torrs: registered in-process TorrServer routes")
}

func shutdownTSRoutes() {
	if torrsServer != nil {
		torrsServer.Close()
		torrsServer = nil
	}
	stopTorrServerProcess()
}

func torrsIsInProcess() bool {
	return torrsServer != nil
}

// getTorrsServer returns the in-process torrent server (or nil).
func getTorrsServer() *torrs.BTServer {
	return torrsServer
}

// initTorrs initializes the in-process torrent server.
func initTorrs(cfg config.Config) bool {
	if cfg.TorrServer.URL != "" {
		log.Debug().Str("url", cfg.TorrServer.URL).Msg("torrs: external URL configured, using proxy mode")
		return false
	}

	homeDir := cfg.TorrServer.HomeDir
	if homeDir == "" {
		homeDir = filepath.Join(cfg.Compat.RepoRoot, "torrserver")
	}

	tCfg := torrs.TorrsConfig{
		HomeDir:             homeDir,
		RAMCache:            cfg.TorrServer.RAMCache,
		CacheSizeMB:         cfg.TorrServer.CacheSizeMB,
		DiskCacheMB:         cfg.TorrServer.DiskCacheMB,
		PreloadMB:           cfg.TorrServer.PreloadMB,
		DisableDHT:          cfg.TorrServer.DisableDHT,
		DisableUpload:       cfg.TorrServer.DisableUpload,
		MaxDownloadSpeedMBs: cfg.TorrServer.MaxDownloadSpeedMB,
		MaxUploadSpeedMBs:   cfg.TorrServer.MaxUploadSpeedMB,
		MaxActiveTorrents:   cfg.TorrServer.MaxActiveTorrents,
		CacheCleanupEnable:  cfg.TorrServer.CacheCleanupEnable,
		CacheCleanupDays:    cfg.TorrServer.CacheCleanupDays,
		CacheCleanupMaxGB:   cfg.TorrServer.CacheCleanupMaxGB,
	}

	srv, err := torrs.New(homeDir, tCfg)
	if err != nil {
		log.Error().Err(err).Msg("torrs: failed to start in-process torrent server")
		return false
	}

	torrsServer = srv
	return true
}

// tsRequireTokenMiddleware blocks mutating /torrents, /settings, /cache and
// /playlistall endpoints when TG auth is enabled and no valid user token is
// present.  External clients that passed Basic-Auth higher up the chain are
// allowed through without a cookie.  Single-user installs (TG auth disabled)
// keep the legacy open behaviour.
func tsRequireTokenMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !tsAuthOK(r) {
			http.Error(w, `{"error":"authentication required"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
//  Handlers
// ---------------------------------------------------------------------------

// maxTSDownloadMB caps the synthetic speed-test payload. Lampa requests
// /download/300 and aborts the XHR after 10s or 300 MB (whichever comes
// first), so the stream is never drained to completion in practice — the cap
// only bounds abuse of an unauthenticated bandwidth source.
const maxTSDownloadMB = 512

// tsDirectDownloadHandler serves MatriX's /download/{megabytes} speed-test
// endpoint: a stream of incompressible bytes the client measures throughput
// against (Lampa's "Тест скорости" in TorrServer settings). Without it the
// /ts/* catch-all answered "{}" instantly and the speed check reported a
// meaningless number. Left unauthenticated to match a real TorrServer (and
// the proxy path, which never gated it); per-user/group TorrServer bans still
// apply via tsAccessMiddleware.
func tsDirectDownloadHandler() http.HandlerFunc {
	// One random block, reused for every chunk. Random rather than zeros so
	// any transparent compression on the path can't inflate the measured speed.
	block := make([]byte, 256<<10)
	if _, err := rand.Read(block); err != nil {
		for i := range block {
			block[i] = byte(i)
		}
	}
	return func(w http.ResponseWriter, r *http.Request) {
		mb, _ := strconv.Atoi(chi.URLParam(r, "size"))
		if mb <= 0 {
			mb = 1
		}
		if mb > maxTSDownloadMB {
			mb = maxTSDownloadMB
		}
		total := int64(mb) << 20

		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(total, 10))
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)

		ctx := r.Context()
		for sent := int64(0); sent < total; {
			select {
			case <-ctx.Done():
				return // client aborted — normal end of a speed test
			default:
			}
			n := int64(len(block))
			if remaining := total - sent; remaining < n {
				n = remaining
			}
			if _, err := w.Write(block[:n]); err != nil {
				return // client went away mid-stream
			}
			sent += n
		}
	}
}

func tsDirectEchoHandler(w http.ResponseWriter, _ *http.Request) {
	// Stay on the MatriX.API prefix that third-party clients (TorrServe
	// Android, Vimu, MatriX Desktop UI) check on connect.  A custom suffix
	// keeps the lampac-go branding visible without breaking the prefix.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("MatriX.API (lampac-go)"))
}

func tsDirectTorrentsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if torrsServer == nil {
			writeJSON(w, http.StatusOK, []any{})
			return
		}

		var req torrs.TorrentsRequest
		r.Body = http.MaxBytesReader(w, r.Body, maxTorrsBodyBytes)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}

		owner := requestUserTokenFromAny(r)

		switch strings.ToLower(req.Action) {
		case "add":
			info, err := torrsServer.AddWithOwner(req.Link, req.Title, req.Poster, req.Data, owner, req.SaveToDB)
			if err != nil {
				writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, info)

		case "get":
			info, err := torrsServer.Get(req.Hash)
			if err != nil {
				writeJSON(w, http.StatusOK, map[string]any{})
				return
			}
			writeJSON(w, http.StatusOK, info)

		case "list":
			list := torrsServer.ListByOwner(owner)
			if list == nil {
				list = []torrs.TorrentInfo{}
			}
			writeJSON(w, http.StatusOK, list)

		case "rem":
			torrsServer.Remove(req.Hash)
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("ok"))

		case "drop":
			torrsServer.Drop(req.Hash)
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("ok"))

		case "set":
			torrsServer.Set(req.Hash, req.Title, req.Poster, req.Data)
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("ok"))

		default:
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unknown action"})
		}
	}
}

func tsDirectSettingsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if torrsServer == nil {
			writeJSON(w, http.StatusOK, map[string]any{})
			return
		}

		var req torrs.SettingsRequest
		r.Body = http.MaxBytesReader(w, r.Body, maxTorrsBodyBytes)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusOK, torrsServer.GetSettings())
			return
		}

		switch strings.ToLower(req.Action) {
		case "set":
			// Mutation — gate here (the route is registered outside the blanket
			// auth group so the read probe stays open for the liveness check).
			if !tsAuthOK(r) {
				http.Error(w, `{"error":"authentication required"}`, http.StatusUnauthorized)
				return
			}
			_ = torrsServer.SetSettings(req.Settings)
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("ok"))
		default: // "get" or anything else
			writeJSON(w, http.StatusOK, torrsServer.GetSettings())
		}
	}
}

func tsDirectCacheHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if torrsServer == nil {
			writeJSON(w, http.StatusOK, torrs.CacheStatus{})
			return
		}

		var req torrs.CacheRequest
		r.Body = http.MaxBytesReader(w, r.Body, maxTorrsBodyBytes)
		_ = json.NewDecoder(r.Body).Decode(&req)

		writeJSON(w, http.StatusOK, torrsServer.GetCacheStatus(req.Hash))
	}
}

func tsDirectStreamHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if torrsServer == nil {
			http.Error(w, "torrent server not available", http.StatusServiceUnavailable)
			return
		}

		q := r.URL.Query()
		hash := q.Get("link")
		rawIdxStr := q.Get("index")
		rawIdx, _ := strconv.Atoi(rawIdxStr) // 1-based (MatriX convention)

		// MatriX uses 1-based file indices: index=1 means first file.
		// 0 is reserved for "unspecified" in some clients; treat as 1 for back-compat
		// rather than rejecting outright, since pidtor builds URLs with index=0 in
		// movie-fallback mode.
		if rawIdx <= 0 {
			rawIdx = 1
		}
		idx := rawIdx - 1

		// Preload mode: trigger preload and return 200.
		if q.Has("preload") {
			torrsServer.Preload(hash, idx)
			w.WriteHeader(http.StatusOK)
			return
		}

		// Stat mode: return JSON with file info + cache/peer stats.
		// TorrServer MatriX clients call ?stat before streaming to check readiness.
		if q.Has("stat") {
			info, err := torrsServer.Get(hash)
			if err != nil {
				writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
				return
			}

			var fileStat *torrs.FileStat
			if info.FileStats != nil && idx >= 0 && idx < len(info.FileStats) {
				fileStat = &info.FileStats[idx]
			}

			cache := torrsServer.GetCacheStatus(hash)

			host := hostFromRequest(r)
			prefix := host
			if strings.HasPrefix(r.URL.Path, "/ts/") {
				prefix = host + "/ts"
			}

			resp := map[string]any{
				"hash":      hash,
				"torrent":   info,
				"file_stat": fileStat,
				// MatriX-compatible top-level fields (some clients read these).
				"name":         info.Name,
				"torrent_size": info.TorrentSize,
				"stat":         info.Stat,
				"stat_string":  info.StatString,
			}
			if fileStat != nil {
				resp["length"] = fileStat.Length
				resp["url"] = fmt.Sprintf("%s/stream/%s?link=%s&index=%d&play",
					prefix,
					url.PathEscape(filepath.Base(fileStat.Path)),
					hash, rawIdx) // keep 1-based in URLs for MatriX compat
			}
			if cache.Torrent != nil {
				resp["preloaded_bytes"] = cache.Torrent.PreloadedBytes
				resp["preload_size"] = cache.Torrent.PreloadSize
				resp["total_peers"] = cache.Torrent.TotalPeers
				resp["active_peers"] = cache.Torrent.ActivePeers
				resp["connected_seeders"] = cache.Torrent.ConnectedSeeders
				resp["download_speed"] = cache.Torrent.DownloadSpeed
				resp["upload_speed"] = cache.Torrent.UploadSpeed
			}

			writeJSON(w, http.StatusOK, resp)
			return
		}

		// Stream mode: serve content with range support.
		log.Info().
			Str("hash", hash).
			Int("rawIdx", rawIdx).
			Int("idx", idx).
			Msg("torrs: stream request")

		reader, size, filename, err := torrsServer.Stream(hash, idx)
		if err != nil {
			log.Warn().Err(err).Str("hash", hash).Int("idx", idx).Msg("torrs: stream failed")
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		// Drop download priority when the connection ends — Stream returns a
		// cancelReader whose Close() flips f.SetPriority(none).
		if closer, ok := reader.(interface{ Close() error }); ok {
			defer closer.Close()
		}

		log.Info().
			Str("hash", hash).
			Str("filename", filename).
			Int64("size", size).
			Int("idx", idx).
			Msg("torrs: streaming file")

		// Content-Type from extension (http.ServeContent will also do its own
		// sniffing if the header is unset, but we provide the canonical mapping).
		ct := mime.TypeByExtension(filepath.Ext(filename))
		if ct == "" {
			ct = contentTypeByExt(filename)
		}
		w.Header().Set("Content-Type", ct)

		// http.ServeContent handles Range/206 and Content-Length itself.
		http.ServeContent(w, r, filename, time.Time{}, reader)
	}
}

func tsDirectPlaylistHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if torrsServer == nil {
			http.Error(w, "not available", http.StatusServiceUnavailable)
			return
		}

		hash := r.URL.Query().Get("hash")
		host := hostFromRequest(r)
		// Determine prefix: if accessed via /ts/playlist, use /ts; otherwise root.
		prefix := host
		if strings.HasPrefix(r.URL.Path, "/ts/") {
			prefix = host + "/ts"
		}

		content, err := torrsServer.Playlist(hash, prefix)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "audio/x-mpegurl")
		_, _ = w.Write([]byte(content))
	}
}

func tsDirectPlaylistAllHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if torrsServer == nil {
			http.Error(w, "not available", http.StatusServiceUnavailable)
			return
		}

		host := hostFromRequest(r)
		prefix := host
		if strings.HasPrefix(r.URL.Path, "/ts/") {
			prefix = host + "/ts"
		}

		// Per-user filter: each user sees only their own torrents.  Anonymous
		// (no token) sees torrents without an owner (legacy / single-user).
		owner := requestUserTokenFromAny(r)

		w.Header().Set("Content-Type", "audio/x-mpegurl")
		_, _ = w.Write([]byte(torrsServer.PlaylistByOwner(owner, prefix)))
	}
}

func tsInProcessUIHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<!doctype html>
<html><head><title>TorrServer</title></head>
<body style="background:#1a1a2e;color:#e0e0e0;font-family:sans-serif;display:flex;align-items:center;justify-content:center;height:100vh;margin:0">
<div style="text-align:center">
<h1 style="color:#00d4ff">TorrServer</h1>
<p style="margin-top:8px">Встроенный торрент-сервер работает</p>
</div>
</body></html>`))
}
