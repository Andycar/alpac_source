package adminhttp

import (
	"io"
	"net/http"
	"strings"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/tgauth"
	"lampac-go/internal/updater"
)

// ---------------------------------------------------------------------------
// Self-update module — replaces the running lampac-go binary with a new
// build from the alcopa-site update server. See internal/updater.
//
// Routes (mounted by server.go):
//
//	GET  /{admin}/api/update/status    — current version, mode, cached latest
//	POST /{admin}/api/update/check     — force refresh from the update server
//	POST /{admin}/api/update/apply     — download + activate + restart
//	POST /{admin}/api/update/rollback  — swap `<exe>.old` back in
//	POST /{admin}/api/update/channel   — set update channel (+password)
//
// All routes require a super-admin TG session.
// ---------------------------------------------------------------------------

func tgAdminUpdateStatusHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := tgAdminAuthCheck(w, r, store, adminStore); !ok {
			return
		}
		svc := liveUpdater()
		if svc == nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"error":           "updater not initialized",
				"mode":            updater.DetectMode().String(),
				"can_self_update": false,
			})
			return
		}
		writeJSON(w, http.StatusOK, svc.Info())
	}
}

func tgAdminUpdateCheckHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, isSuper := tgAdminAuthCheck(w, r, store, adminStore); !isSuper {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "super admin required"})
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "POST required"})
			return
		}
		svc := liveUpdater()
		if svc == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "updater not initialized"})
			return
		}
		if _, err := svc.Check(); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":    false,
				"error": err.Error(),
				"info":  svc.Info(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":   true,
			"info": svc.Info(),
		})
	}
}

func tgAdminUpdateApplyHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, isSuper := tgAdminAuthCheck(w, r, store, adminStore); !isSuper {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "super admin required"})
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "POST required"})
			return
		}
		svc := liveUpdater()
		if svc == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "updater not initialized"})
			return
		}
		if !svc.Mode().CanSelfUpdate() {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": "self-update not allowed in this run mode",
				"mode":  svc.Mode().String(),
				"hint":  dockerHint(svc.Config().ServerURL),
				"info":  svc.Info(),
			})
			return
		}

		// Run the download + swap synchronously so failures (asset missing,
		// SHA mismatch, permission denied, ...) surface to the client instead
		// of being swallowed inside a goroutine. Only the actual re-exec is
		// deferred: it tears down the HTTP server, so we delay it briefly to
		// let this response flush first.
		if err := svc.Apply(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"ok":    false,
				"error": err.Error(),
				"info":  svc.Info(),
			})
			return
		}

		go func() {
			time.Sleep(500 * time.Millisecond)
			_ = svc.Restart()
		}()

		writeJSON(w, http.StatusAccepted, map[string]any{
			"ok":      true,
			"message": "binary swapped — process is re-executing now",
		})
	}
}

func tgAdminUpdateRollbackHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, isSuper := tgAdminAuthCheck(w, r, store, adminStore); !isSuper {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "super admin required"})
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "POST required"})
			return
		}
		svc := liveUpdater()
		if svc == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "updater not initialized"})
			return
		}
		if !svc.Mode().CanSelfUpdate() {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "rollback not allowed in this run mode"})
			return
		}

		go func() {
			_ = svc.RollbackNow()
		}()

		writeJSON(w, http.StatusAccepted, map[string]any{
			"ok":      true,
			"message": "rollback started — process will re-exec when done",
		})
	}
}

// tgAdminUpdateChannelHandler persists an update-channel override
// (database/updater/settings.json) and hot-reloads the updater service, so
// the server can join a password-protected channel without a config edit or
// restart. Empty channel + token clears the override back to config.toml.
func tgAdminUpdateChannelHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, isSuper := tgAdminAuthCheck(w, r, store, adminStore); !isSuper {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "super admin required"})
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "POST required"})
			return
		}
		svc := liveUpdater()
		if svc == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "updater not initialized"})
			return
		}
		var body struct {
			Channel string `json:"channel"`
			Token   string `json:"token"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
			return
		}
		s := updater.Settings{
			Channel:     strings.ToLower(strings.TrimSpace(body.Channel)),
			ServerToken: strings.TrimSpace(body.Token),
		}
		cfg := svc.Config()
		if err := updater.SaveSettings(updater.SettingsPath(cfg.HistoryPath), s); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "persist: " + err.Error()})
			return
		}
		// Rebuild from config.toml + fresh override and hot-swap.
		newCfg := buildUpdaterConfig(liveConfig(config.Config{}), liveVersion())
		svc.Reload(newCfg)
		// Verify immediately so the admin sees whether the channel accepts us.
		var checkErr string
		if _, err := svc.Check(); err != nil {
			checkErr = err.Error()
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      checkErr == "",
			"error":   checkErr,
			"channel": newCfg.Channel,
			"info":    svc.Info(),
		})
	}
}

// dockerHint returns a copy-pasteable update command for Docker users.
// We no longer know the image path (release-source is now alcopa-site, not
// GitHub Container Registry), so just suggest a generic compose refresh.
func dockerHint(_ string) string {
	return "docker compose pull && docker compose up -d"
}
