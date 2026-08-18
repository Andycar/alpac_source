package adminhttp

import (
	"net/http"

	"lampac-go/internal/config"
	"lampac-go/internal/tgauth"
)

// tgAdminJacredStatusHandler reports whether the self-hosted jacred parser is
// enabled and reachable. Both admin panels use it to decide whether to show
// the embedded jacred web UI at /jacred/ — clicking through to a disabled
// instance would only ever render the 503 page from jacredWebUIHandler.
//
// GET /{adminPath}/api/jacred/status →
//
//	{"enabled":true,"installed":true,"running":true,"healthy":true,
//	 "stage":"running","version":"v1.2.3","db_size_mb":812,"url":"/jacred/"}
func tgAdminJacredStatusHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := tgAdminAuthCheck(w, r, store, adminStore); !ok {
			return
		}
		resp := map[string]any{"enabled": false, "url": "/jacred/"}
		if serverReady() {
			cfg := liveConfig(config.Config{})
			st := jacredStatusSnapshot(cfg)
			resp["enabled"] = cfg.Parser.JacRedLocal
			resp["installed"] = st.Installed
			resp["running"] = st.Running
			resp["healthy"] = st.Healthy
			resp["stage"] = st.Stage
			resp["version"] = st.Version
			resp["db_size_mb"] = st.DBSizeMB
			resp["port_conflict"] = st.PortConflict
			if st.Error != "" {
				resp["error"] = st.Error
			}
		}
		writeJSON(w, http.StatusOK, resp)
	}
}
