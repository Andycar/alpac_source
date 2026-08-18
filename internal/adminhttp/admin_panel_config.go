package adminhttp

import (
	stdjson "encoding/json"
	"io"
	"net/http"
	"strings"

	"lampac-go/internal/tgauth"
)

// --- Config API (legacy JSON view of TOML config) ---

func tgAdminConfigHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		switch r.Method {
		case http.MethodGet:
			// Return the TOML config as a JSON object (legacy admin compatibility).
			result := loadMergedConf()
			out, _ := stdjson.MarshalIndent(result, "", "  ")
			writeRawJSON(w, out)

		case http.MethodPost:
			body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			var req struct {
				JSON string `json:"json"`
			}
			if stdjson.Unmarshal(body, &req) != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
				return
			}
			payload := strings.TrimSpace(req.JSON)
			if payload == "" {
				writeJSON(w, http.StatusOK, map[string]any{"error": true, "ex": "json is empty"})
				return
			}
			var legacyRoot map[string]any
			if err := stdjson.Unmarshal([]byte(payload), &legacyRoot); err != nil {
				writeJSON(w, http.StatusOK, map[string]any{"error": true, "ex": err.Error()})
				return
			}
			// Extract users separately if present.
			if accsdb, ok := legacyRoot["accsdb"].(map[string]any); ok {
				if users, ok := accsdb["users"]; ok {
					writePrettyJSON("users.json", users)
					delete(accsdb, "users")
				}
			}
			// Apply legacy JSON keys to TOML config.
			if err := updateConfigTOMLMap(func(tomlRoot map[string]any) {
				applyLegacyMapToTOML(legacyRoot, tomlRoot)
			}); err != nil {
				writeJSON(w, http.StatusOK, map[string]any{"error": true, "ex": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"success": true})

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}
