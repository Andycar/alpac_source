package adminhttp

import (
	"context"
	stdjson "encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"lampac-go/internal/tgauth"
	"lampac-go/internal/torrbalancer"
)

// tgAdminTorrBalancerHandler powers the admin "TS Балансер" tab.
//
// GET  /{adminPath}/api/torrbalancer
//   - returns settings + backends (live runtime stats; passwords never exposed)
//
// POST /{adminPath}/api/torrbalancer
//   - action=add        — add backend {name, host, login, password, weight, notes, enabled?, ssh_*}
//   - action=update     — patch backend {id, patch:{name?,host?,login?,password?,weight?,enabled?,notes?,ssh_host?,ssh_port?,ssh_user?,ssh_password?,ssh_cmd?}}
//   - action=delete     — remove backend {id}
//   - action=probe      — manual health probe {id} (returns refreshed status)
//   - action=sshrestart — restart the backend's machine over SSH {id}
//   - action=settings   — replace settings (whole torrbalancer.Settings struct)
//   - action=test       — probe a host that isn't in the pool yet {host, login, password}
func tgAdminTorrBalancerHandler(tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := tgAdminAuthCheck(w, r, tgStore, adminStore); !ok {
			return
		}
		if !serverReady() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "not ready"})
			return
		}

		store := liveTSBalancerStore()
		pool := liveTSBalancerPool()

		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, buildTorrBalancerSnapshot(store, pool))
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			Action   string                 `json:"action"`
			Name     string                 `json:"name"`
			Host     string                 `json:"host"`
			Login    string                 `json:"login"`
			Password string                 `json:"password"`
			Weight   int                    `json:"weight"`
			Enabled  *bool                  `json:"enabled"`
			Notes    string                 `json:"notes"`
			ID       string                 `json:"id"`
			Patch    map[string]interface{} `json:"patch"`
			Settings *torrbalancer.Settings `json:"settings"`

			SSHHost     string `json:"ssh_host"`
			SSHPort     int    `json:"ssh_port"`
			SSHUser     string `json:"ssh_user"`
			SSHPassword string `json:"ssh_password"`
			SSHCmd      string `json:"ssh_cmd"`
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 256<<10))
		_ = r.Body.Close()
		if len(body) > 0 {
			if err := stdjson.Unmarshal(body, &req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json: " + err.Error()})
				return
			}
		}

		if store == nil || pool == nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "torrbalancer not initialised"})
			return
		}

		switch strings.ToLower(strings.TrimSpace(req.Action)) {
		case "add":
			enabled := true
			if req.Enabled != nil {
				enabled = *req.Enabled
			}
			b, err := store.Add(req.Name, req.Host, req.Login, req.Password, req.Weight, enabled, req.Notes)
			if err != nil {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			// SSH-поля живут вторым шагом через Update — Add их не принимает,
			// а раздувать его позиционную сигнатуру ради опциональных полей не стоит.
			if req.SSHHost != "" || req.SSHPort != 0 || req.SSHUser != "" || req.SSHPassword != "" || req.SSHCmd != "" {
				b, err = store.Update(b.ID, torrbalancer.UpdatePatch{
					SSHHost: &req.SSHHost, SSHPort: &req.SSHPort, SSHUser: &req.SSHUser,
					SSHPassword: &req.SSHPassword, SSHCmd: &req.SSHCmd,
				})
				if err != nil {
					writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
					return
				}
			}
			pool.Reconcile(store.Snapshot())
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "backend": redactBackend(b)})

		case "update":
			patch := torrbalancer.UpdatePatch{}
			if v, ok := req.Patch["name"].(string); ok {
				patch.Name = &v
			}
			if v, ok := req.Patch["host"].(string); ok {
				patch.Host = &v
			}
			if v, ok := req.Patch["login"].(string); ok {
				patch.Login = &v
			}
			if v, ok := req.Patch["password"].(string); ok {
				patch.Password = &v
			}
			if v, ok := req.Patch["weight"].(float64); ok {
				iv := int(v)
				patch.Weight = &iv
			}
			if v, ok := req.Patch["enabled"].(bool); ok {
				patch.Enabled = &v
			}
			if v, ok := req.Patch["notes"].(string); ok {
				patch.Notes = &v
			}
			if v, ok := req.Patch["ssh_host"].(string); ok {
				patch.SSHHost = &v
			}
			if v, ok := req.Patch["ssh_port"].(float64); ok {
				iv := int(v)
				patch.SSHPort = &iv
			}
			if v, ok := req.Patch["ssh_user"].(string); ok {
				patch.SSHUser = &v
			}
			if v, ok := req.Patch["ssh_password"].(string); ok {
				patch.SSHPassword = &v
			}
			if v, ok := req.Patch["ssh_cmd"].(string); ok {
				patch.SSHCmd = &v
			}
			id := req.ID
			if id == "" {
				if v, ok := req.Patch["id"].(string); ok {
					id = v
				}
			}
			b, err := store.Update(id, patch)
			if err != nil {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			pool.Reconcile(store.Snapshot())
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "backend": redactBackend(b)})

		case "delete":
			if err := store.Delete(req.ID); err != nil {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			pool.Reconcile(store.Snapshot())
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})

		case "probe":
			if req.ID == "" {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "id required"})
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()
			st, ok := pool.ProbeNow(ctx, req.ID)
			if !ok {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "backend not found"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": st})

		case "sshrestart":
			// Ручной SSH-рестарт машины бэкенда из админки. Долгий (dial+команда
			// до ~1 мин) — но это осознанное действие оператора, ждём синхронно.
			b := pool.FindByID(req.ID)
			if b == nil {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "backend not found"})
				return
			}
			out, err := pool.SSHRestartBackend(b)
			if err != nil {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error(), "output": out})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": out})

		case "settings":
			if req.Settings == nil {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "settings missing"})
				return
			}
			if err := pool.SetSettings(*req.Settings); err != nil {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "settings": pool.CurrentSettings()})

		case "test":
			host := strings.TrimRight(strings.TrimSpace(req.Host), "/")
			if host == "" {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "host required"})
				return
			}
			if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
				host = "http://" + host
			}
			ok, msg := checkTSHealthWithAuth(host, req.Login, req.Password)
			writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "message": msg})

		default:
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unknown action"})
		}
	}
}

// buildTorrBalancerSnapshot returns settings + backends for the admin UI.
// Passwords are never included — only a has_auth flag.
func buildTorrBalancerSnapshot(store *torrbalancer.Store, pool *torrbalancer.Pool) map[string]any {
	out := map[string]any{"backends": []any{}, "active": false}
	if pool != nil {
		out["settings"] = pool.CurrentSettings()
		out["backends"] = pool.Snapshot() // BackendStatus: no password, has_auth flag
		out["active"] = pool.HasEnabledBackends()
	} else if store != nil {
		out["settings"] = store.Settings()
		bs := store.Snapshot()
		red := make([]map[string]any, len(bs))
		for i, b := range bs {
			red[i] = redactBackend(b)
		}
		out["backends"] = red
	} else {
		out["settings"] = torrbalancer.DefaultSettings()
	}
	return out
}

// redactBackend converts a StoredBackend to a password-free map for responses.
func redactBackend(b torrbalancer.StoredBackend) map[string]any {
	return map[string]any{
		"id":       b.ID,
		"name":     b.Name,
		"host":     b.Host,
		"login":    b.Login,
		"has_auth": strings.TrimSpace(b.Password) != "",
		"weight":   b.Weight,
		"enabled":  b.Enabled,
		"notes":    b.Notes,
		"has_ssh":  strings.TrimSpace(b.SSHPassword) != "",
		"ssh_host": b.SSHHost,
		"ssh_port": b.SSHPort,
		"ssh_user": b.SSHUser,
		"ssh_cmd":  b.SSHCmd,
	}
}
