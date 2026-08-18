package adminhttp

import (
	"fmt"
	"net/http"
	"time"

	"lampac-go/internal/tgauth"
	"lampac-go/internal/torrs"
)

// tgAdminTorrsListHandler returns the list of active torrents with stats.
// GET /admin/api/torrs/list
func tgAdminTorrsListHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		srv := getTorrsServer()
		if srv == nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"mode":     "proxy",
				"embedded": torrs.IsAvailable(),
				"torrents": []any{},
			})
			return
		}

		list := srv.List()
		if list == nil {
			list = []torrs.TorrentInfo{}
		}

		// Add cache status for each torrent.
		type enriched struct {
			torrs.TorrentInfo
			Cache torrs.CacheStatus `json:"cache"`
		}

		items := make([]enriched, len(list))
		for i, t := range list {
			items[i] = enriched{
				TorrentInfo: t,
				Cache:       srv.GetCacheStatus(t.Hash),
			}
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"mode":     "inprocess",
			"embedded": true,
			"settings": srv.GetSettings(),
			"torrents": items,
		})
	}
}

// tgAdminTorrsActionHandler performs admin actions on torrents.
// POST /admin/api/torrs/action {action, hash}
func tgAdminTorrsActionHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		srv := getTorrsServer()
		if srv == nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "torrs not in-process"})
			return
		}

		var req struct {
			Action string `json:"action"` // remove, drop
			Hash   string `json:"hash"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}

		switch req.Action {
		case "remove":
			srv.Remove(req.Hash)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		case "drop":
			srv.Drop(req.Hash)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		default:
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "unknown action"})
		}
	}
}

// tgAdminTorrsHealthHandler returns a server-wide health snapshot.
// GET /admin/api/torrs/health
func tgAdminTorrsHealthHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		srv := getTorrsServer()
		if srv == nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"mode":     "proxy",
				"embedded": torrs.IsAvailable(),
				"healthy":  false,
			})
			return
		}

		h := srv.Health()
		writeJSON(w, http.StatusOK, map[string]any{
			"mode":     "inprocess",
			"embedded": true,
			"healthy":  true,
			"health":   h,
		})
	}
}

// tgAdminTorrsExternalHandler returns the external-access configuration so
// the admin panel can show users the URL + credentials they need to plug
// into TorrServe / Vimu / etc.  Password is masked for non-super admins.
func tgAdminTorrsExternalHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		cfg, cfgOK := tsExternalAuthConfig()
		if !cfgOK {
			writeJSON(w, http.StatusOK, map[string]any{"enable": false})
			return
		}

		host := r.Host
		if host == "" {
			host = "your-server"
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"enable":       cfg.Enable,
			"login":        cfg.Login,
			"password_set": cfg.Password != "",
			"allow_from":   cfg.AllowFrom,
			"listen_addr":  cfg.ListenAddr,
			"sample_url":   "http://" + cfg.Login + ":<password>@" + host + "/ts",
			"sample_curl":  "curl -u '" + cfg.Login + ":<password>' http://" + host + "/ts/echo",
		})
	}
}

// tgAdminTorrsCleanupHandler triggers a synchronous disk-cleanup pass and
// returns a report.  POST /admin/api/torrs/cleanup
func tgAdminTorrsCleanupHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		srv := getTorrsServer()
		if srv == nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "torrs not in-process"})
			return
		}

		// DiskCleanup can take several seconds on a large data dir (walking
		// thousands of files) — keep it on the request goroutine but cap with
		// a generous timeout so the client doesn't sit forever.
		done := make(chan torrs.CleanupReport, 1)
		go func() { done <- srv.DiskCleanupReport() }()

		select {
		case rep := <-done:
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":     true,
				"report": rep,
			})
		case <-time.After(2 * time.Minute):
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":    false,
				"error": "cleanup running >2min; check logs and try again",
			})
		}
	}
}

// tgAdminTorrsStreamHandler streams live torrent/health stats over SSE.
// GET /admin/api/torrs/stream — emits an event every second until the
// client disconnects.
func tgAdminTorrsStreamHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		srv := getTorrsServer()
		if srv == nil {
			http.Error(w, "torrent server not available", http.StatusServiceUnavailable)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering

		flusher, _ := w.(http.Flusher)
		if flusher == nil {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		ctx := r.Context()
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		// Send initial frame immediately so the client doesn't wait 1s.
		emit := func() bool {
			list := srv.List()
			if list == nil {
				list = []torrs.TorrentInfo{}
			}
			caches := make([]torrs.CacheStatus, len(list))
			for i, t := range list {
				caches[i] = srv.GetCacheStatus(t.Hash)
			}
			payload := map[string]any{
				"health":   srv.Health(),
				"torrents": list,
				"caches":   caches,
			}
			b, err := json.Marshal(payload)
			if err != nil {
				return false
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
				return false
			}
			flusher.Flush()
			return true
		}

		if !emit() {
			return
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !emit() {
					return
				}
			}
		}
	}
}
