package httpapi

// CMCD collection wiring.
//
// The proxy sees every manifest and segment fetch already; CMCD (CTA-5004) is
// the player telling it, in that same request, how playback is actually going.
// Aggregating it per balancer answers the question our health checks cannot:
// not "is the source up" — every dead-for-viewers source we ever shipped was
// answering 200 — but "does watching it stall".
//
// No identity is collected: the aggregate is keyed by (plugin, platform), the
// session id is a player-minted string used only to group one playback's
// requests, and it never leaves memory.

import (
	"net/http"
	"strconv"

	"lampac-go/internal/cmcd"
	"lampac-go/internal/config"
	"lampac-go/internal/proxyapi"
	"lampac-go/internal/tgauth"
)

var cmcdStatsStore *cmcd.Store

// initCMCDStats creates the aggregate and hooks it into the proxy. Safe to call
// once at wiring; a nil store simply means no collection.
func initCMCDStats(cfg config.Config) {
	if cmcdStatsStore != nil {
		return
	}
	cmcdStatsStore = cmcd.New(cfg.Compat.RepoRoot)
	proxyapi.CMCDRecorder = func(plugin, ua string, rep cmcd.Report) {
		cmcdStatsStore.Add(plugin, ua, rep)
	}
}

// FlushCMCDStats folds in-flight sessions into the aggregate and persists it.
// Called on shutdown: without it everything watched since the last save — up to
// half a minute of traffic plus every unfinished session — is lost on restart.
func FlushCMCDStats() {
	if cmcdStatsStore != nil {
		cmcdStatsStore.Flush()
	}
}

// GET /{adminPath}/api/cmcd-stats?limit=&live=1
//
// Two views on purpose: the aggregate answers "which source should we fix",
// the live list answers "what is happening to the person complaining right now".
func adminCMCDStatsHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := tgAdminAuthCheck(w, r, store, adminStore); !ok {
			return
		}
		if cmcdStatsStore == nil {
			writeJSON(w, http.StatusOK, map[string]any{"summary": map[string]any{}, "rows": []any{}, "live": []any{}})
			return
		}
		limit := 200
		if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 2000 {
			limit = v
		}
		resp := map[string]any{
			"summary": cmcdStatsStore.Summary(),
			"rows":    cmcdStatsStore.Rows(limit),
		}
		if parseBoolParam(r.URL.Query().Get("live")) {
			resp["live"] = cmcdStatsStore.Live(limit)
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// POST /{adminPath}/api/cmcd-stats/reset — start fresh after changing a source.
func adminCMCDStatsResetHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := tgAdminAuthCheck(w, r, store, adminStore); !ok {
			return
		}
		if cmcdStatsStore != nil {
			cmcdStatsStore.Reset()
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}
