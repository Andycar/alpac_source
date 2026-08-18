package adminhttp

import (
	stdjson "encoding/json"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"lampac-go/internal/config"
	"lampac-go/internal/tgauth"
)

// --- TG Settings API (super admin only) ---

func tgAdminTGSettingsHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore, bot *tgauth.Bot, memberChecker *tgauth.MembershipChecker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, isSuper, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}
		if !isSuper {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "super admin only"})
			return
		}

		switch r.Method {
		case http.MethodGet:
			root := loadMergedConf()
			tgConf := map[string]any{}
			if tg, ok := root["TelegramAuth"].(map[string]any); ok {
				maps.Copy(tgConf, tg)
			}
			// Add admin path
			path := relToRuntime(adminPathFile)
			if data, err := os.ReadFile(path); err == nil {
				tgConf["admin_path"] = strings.TrimSpace(string(data))
			}
			// Include kit settings
			kitConf := map[string]any{}
			if k, ok := root["kit"].(map[string]any); ok {
				maps.Copy(kitConf, k)
			}
			tgConf["kit"] = kitConf
			writeJSON(w, http.StatusOK, tgConf)

		case http.MethodPost:
			body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
			var req map[string]any
			if stdjson.Unmarshal(body, &req) != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
				return
			}
			// Remove admin_path — not editable here
			delete(req, "admin_path")
			delete(req, "admin_id") // admin_id is immutable
			// Extract kit settings separately
			var kitReq map[string]any
			if k, ok := req["kit"].(map[string]any); ok {
				kitReq = k
			}
			delete(req, "kit")

			// Pull the binds kill-switch out of the kit blob: it needs a
			// hot-reload (bind guard + page injection read live config), which
			// the legacy-map kit write below does not do. Written separately via
			// updateConfigTOMLMap (which reloads) only when it actually changed.
			var kitBindsDisabled *bool
			if kitReq != nil {
				if v, ok := kitReq["binds_disabled"].(bool); ok {
					b := v
					kitBindsDisabled = &b
				}
				delete(kitReq, "binds_disabled")
			}

			// Parse required_chats once — used for both TOML save and hot-reload.
			var parsedChats []config.RequiredChat
			if rawChats, ok := req["required_chats"].([]any); ok {
				// Convert float64→int64 for chat_id so TOML writes integers, not floats.
				for i, item := range rawChats {
					if m, ok := item.(map[string]any); ok {
						if v, ok := m["chat_id"].(float64); ok {
							m["chat_id"] = int64(v)
						}
						rawChats[i] = m
					}
				}
				// Build typed slice for hot-reload.
				for _, item := range rawChats {
					if m, ok := item.(map[string]any); ok {
						var c config.RequiredChat
						if v, ok := m["chat_id"].(int64); ok {
							c.ChatID = v
						}
						if v, ok := m["title"].(string); ok {
							c.Title = v
						}
						if v, ok := m["link"].(string); ok {
							c.Link = v
						}
						if c.ChatID != 0 {
							parsedChats = append(parsedChats, c)
						}
					}
				}
			}
			// Convert check_interval_min from float64 to int for TOML.
			if v, ok := req["check_interval_min"].(float64); ok {
				req["check_interval_min"] = int(v)
			}

			err := updateInitConfMap(func(root map[string]any) {
				tg := ensureMapChild(root, "TelegramAuth")
				maps.Copy(tg, req)
				if kitReq != nil {
					kitSection := ensureMapChild(root, "kit")
					maps.Copy(kitSection, kitReq)
				}
			})
			if err != nil {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			// Hot-reload auto-approve into running bot (no restart needed for this setting)
			if bot != nil {
				if aa, ok := req["auto_approve"].(bool); ok {
					days := 30
					if d, ok := req["auto_approve_days"].(float64); ok && int(d) > 0 {
						days = int(d)
					}
					bot.SetAutoApprove(aa, days)
				}
			}
			// Hot-reload required chats into membership checker
			if memberChecker != nil {
				intervalMin := 60
				if v, ok := req["check_interval_min"].(int); ok && v > 0 {
					intervalMin = v
				}
				memberChecker.SetChats(parsedChats, intervalMin)
			}
			// Persist the "Привязки" kill-switch to [kit] binds_disabled with a
			// hot-reload, only when it changed (Reload restarts proxy pools etc.,
			// too heavy to run on every routine save).
			if kitBindsDisabled != nil {
				cur := false
				if serverReady() {
					cur = liveConfig(config.Config{}).Kit.BindsDisabled
				}
				if *kitBindsDisabled != cur {
					if err := updateConfigTOMLMap(func(root map[string]any) {
						setNestedMap(root, "kit")["binds_disabled"] = *kitBindsDisabled
					}); err != nil {
						writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
						return
					}
				}
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "restart_needed": false})

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

// --- Regen admin path (super admin only) ---

func tgAdminRegenPathHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, isSuper, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}
		if !isSuper {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "super admin only"})
			return
		}
		newPath := "cp_" + randomAlphaNum(10)
		path := relToRuntime(adminPathFile)
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(newPath), 0o644); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "new_path": newPath, "restart_needed": true})
	}
}

// --- Broadcast API (super admin only) ---

func tgAdminBroadcastHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore, bot *tgauth.Bot) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, isSuper, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}
		if !isSuper {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "super admin only"})
			return
		}
		if bot == nil {
			writeJSON(w, http.StatusOK, map[string]any{"error": "bot not configured"})
			return
		}

		switch r.Method {
		case http.MethodGet:
			count := bot.ActiveUserCount(store)
			writeJSON(w, http.StatusOK, map[string]any{"user_count": count})

		case http.MethodPost:
			body, _ := io.ReadAll(io.LimitReader(r.Body, 65536))
			var req struct {
				Text string `json:"text"`
			}
			if stdjson.Unmarshal(body, &req) != nil || strings.TrimSpace(req.Text) == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "text required"})
				return
			}
			res := bot.Broadcast(store, req.Text)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "total": res.Total, "sent": res.Sent, "failed": res.Failed})

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

// --- Admins API (super admin only) ---

func tgAdminAdminsHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, isSuper, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		switch r.Method {
		case http.MethodGet:
			// Viewing the admin list is allowed for ANY admin — regular admins
			// should be able to see who else has access. Mutations (add/remove)
			// below remain super-admin-only. Previously the whole handler was
			// super-gated, so a regular admin opening the "Админы" tab got a 403
			// and an empty list.
			writeJSON(w, http.StatusOK, adminStore.List())

		case http.MethodPost:
			if !isSuper {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "super admin only"})
				return
			}
			body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
			var req struct {
				Action     string `json:"action"` // "add" or "remove"
				TelegramID int64  `json:"telegram_id"`
				Note       string `json:"note"`
			}
			if stdjson.Unmarshal(body, &req) != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
				return
			}
			switch req.Action {
			case "add":
				if req.TelegramID <= 0 {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "telegram_id required"})
					return
				}
				_ = adminStore.Add(tgauth.AdminEntry{
					TelegramID: req.TelegramID,
					Role:       tgauth.RoleAdmin,
					AddedBy:    0, // will be filled by handler context if needed
					Note:       req.Note,
				})
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			case "remove":
				_ = adminStore.Remove(req.TelegramID)
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			default:
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown action"})
			}

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}
