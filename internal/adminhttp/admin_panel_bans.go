package adminhttp

import (
	stdjson "encoding/json"
	"io"
	"net/http"
	"time"

	"lampac-go/internal/tgauth"

	"github.com/google/uuid"
)

// tgAdminBansHandler handles GET/POST for the admin bans management API.
func tgAdminBansHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore, banStore *tgauth.BanStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tid, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		switch r.Method {
		case http.MethodGet:
			rules := banStore.List()
			stats := banStore.CountByType()
			writeJSON(w, http.StatusOK, map[string]any{
				"rules": rules,
				"stats": stats,
				"total": len(rules),
			})

		case http.MethodPost:
			body, _ := io.ReadAll(io.LimitReader(r.Body, 65536))
			var req struct {
				Action string `json:"action"`
				// For add_ban
				Type   string `json:"type"`
				Value  string `json:"value"`
				Reason string `json:"reason"`
				// For remove_ban
				ID string `json:"id"`
			}
			if stdjson.Unmarshal(body, &req) != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
				return
			}

			switch req.Action {
			case "add":
				if req.Type == "" || req.Value == "" {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "type and value required"})
					return
				}
				// Validate type
				switch req.Type {
				case "uid", "ip", "cidr", "tg_id", "fingerprint", "country":
					// ok
				default:
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid ban type"})
					return
				}
				rule := tgauth.BanRule{
					ID:        uuid.New().String(),
					Type:      req.Type,
					Value:     req.Value,
					Reason:    req.Reason,
					CreatedAt: time.Now().UTC(),
					CreatedBy: tid,
				}
				if err := banStore.Add(rule); err != nil {
					writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": rule.ID})

			case "remove":
				if req.ID == "" {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id required"})
					return
				}
				_ = banStore.Remove(req.ID)
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})

			default:
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown action"})
			}

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}
