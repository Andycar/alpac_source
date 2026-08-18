package adminhttp

import (
	"context"
	"net/http"
	"strings"
	"time"

	"lampac-go/internal/tgauth"
)

// tgAdminRuTrackerStatusHandler exposes the native RuTracker indexer to the
// admin panel.
//
//	GET /{adminPath}/api/rutracker/status  → enabled / logged_in / caches / last error
//	GET /{adminPath}/api/rutracker/test?q= → staged live probe:
//	    config → connect → login → search → resolve
//
// The staged probe exists because "rutracker returns nothing" has four
// unrelated causes (no credentials, blocked egress, Cloudflare challenge, dead
// session) that look identical in the merged search answer.
func tgAdminRuTrackerStatusHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := tgAdminAuthCheck(w, r, store, adminStore); !ok {
			return
		}
		if !serverReady() {
			writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "reason": "server not ready"})
			return
		}
		writeJSON(w, http.StatusOK, ruTrackerStatus())
	}
}

func tgAdminRuTrackerTestHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := tgAdminAuthCheck(w, r, store, adminStore); !ok {
			return
		}
		if !serverReady() {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "server not ready"})
			return
		}
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		// A live probe logs in and resolves a magnet — well beyond a normal
		// request, hence its own generous deadline.
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		writeJSON(w, http.StatusOK, ruTrackerTest(ctx, query))
	}
}
