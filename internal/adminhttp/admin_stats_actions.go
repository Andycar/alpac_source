package adminhttp

import (
	"fmt"
	"net/http"
	"time"

	"lampac-go/internal/tgauth"

	"github.com/go-chi/chi/v5"
)

// admin_stats_actions.go — the self-contained super-admin action handlers split
// out of the host's admin_stats.go grab-bag (restart / reload / transcoding).
// The stats/dashboard/browser-pool handlers + shared helpers (YT globals, health
// checks, getYtdlpVersion, collectProcessOSStats) stay in httpapi — they carry
// reverse coupling + internal types (HealthStatus/ProcessOSStats). requestSelf
// Restart + liveTransSvc are injected.

func tgAdminRestartHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, isSuper := tgAdminAuthCheck(w, r, store, adminStore)
		if !isSuper {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "super admin required"})
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "POST required"})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "Server restarting..."})

		go func() {
			time.Sleep(500 * time.Millisecond)
			requestSelfRestart()
		}()
	}
}

// tgAdminReloadHandler hot-reloads the configuration without restarting.
// POST /{adminPath}/api/reload
func tgAdminReloadHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, isSuper := tgAdminAuthCheck(w, r, store, adminStore)
		if !isSuper {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "super admin required"})
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "POST required"})
			return
		}

		if !serverReady() {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "server not initialized"})
			return
		}

		if err := reloadServer(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "Config reloaded successfully"})
	}
}

// tgAdminTranscodingKillHandler kills a specific transcoding job by ID.
// POST /{adminPath}/api/transcoding/kill/{jobId}
func tgAdminTranscodingKillHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, isSuper := tgAdminAuthCheck(w, r, store, adminStore)
		if !isSuper {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "super admin required"})
			return
		}

		jobID := chi.URLParam(r, "jobId")
		if jobID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "jobId required"})
			return
		}

		ts := liveTransSvc()
		if ts == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "transcoding not enabled"})
			return
		}

		if ts.StopJobByID(jobID) {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "Job " + jobID + " stopped"})
		} else {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "job not found"})
		}
	}
}

// tgAdminTranscodingCleanupHandler removes all dead (exited) transcoding jobs.
// POST /{adminPath}/api/transcoding/cleanup
func tgAdminTranscodingCleanupHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, isSuper := tgAdminAuthCheck(w, r, store, adminStore)
		if !isSuper {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "super admin required"})
			return
		}

		ts := liveTransSvc()
		if ts == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "transcoding not enabled"})
			return
		}

		count := ts.CleanupDead()
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"removed": count,
			"message": fmt.Sprintf("Removed %d dead jobs", count),
		})
	}
}
