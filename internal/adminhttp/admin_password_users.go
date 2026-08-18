package adminhttp

// Admin-facing CRUD for password users.
//
// Wired at /{adminPath}/api/password-users (GET = list, POST = action).
// Mirrors the action-dispatch pattern of admin_panel_users.go (TG users)
// so the v2 frontend can drive both with a single component.

import (
	stdjson "encoding/json"
	"net/http"
	"strings"
	"time"

	"lampac-go/internal/tgauth"
)

// adminPasswordUsersHandler dispatches both GET (list) and POST (mutations).
func adminPasswordUsersHandler(store *tgauth.PasswordUserStore, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := tgAdminAuthCheck(w, r, tgStore, adminStore); !ok {
			return
		}
		if store == nil {
			writeJSON(w, http.StatusOK, map[string]any{"users": []any{}})
			return
		}

		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, map[string]any{"users": serializePasswordUsers(store.List())})
			return
		case http.MethodPost:
			adminPasswordUsersPostHandler(w, r, store)
			return
		default:
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		}
	}
}

func adminPasswordUsersPostHandler(w http.ResponseWriter, r *http.Request, store *tgauth.PasswordUserStore) {
	var body struct {
		Action       string `json:"action"`
		Username     string `json:"username"`
		Password     string `json:"password"`
		GroupID      string `json:"group_id"`
		ExpiresAt    string `json:"expires_at"`
		Days         int    `json:"days"`
		PremiumUntil string `json:"premium_until"`
		MaxDevices   int    `json:"max_devices"`
		Disabled     bool   `json:"torrserver_disabled"`
		Ban          bool   `json:"ban"`
		BanReason    string `json:"ban_reason"`
		Comment      string `json:"comment"`
		DeviceUID    string `json:"device_uid"`
	}
	if err := stdjson.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	body.Username = strings.TrimSpace(body.Username)
	if body.Action == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing action"})
		return
	}

	switch body.Action {
	case "create":
		opts := tgauth.CreateOpts{
			GroupID:    body.GroupID,
			MaxDevices: body.MaxDevices,
			Comment:    body.Comment,
		}
		if body.ExpiresAt != "" {
			if t, ok := parseAdminTime(body.ExpiresAt); ok {
				opts.ExpiresAt = t
			}
		}
		if body.PremiumUntil != "" {
			if t, ok := parseAdminTime(body.PremiumUntil); ok {
				opts.PremiumUntil = t
			}
		}
		user, err := store.Create(body.Username, body.Password, opts)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": serializePasswordUser(user)})

	case "delete":
		if err := store.Delete(body.Username); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})

	case "reset_password":
		if err := store.ResetPassword(body.Username, body.Password, 8, 128); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})

	case "extend":
		if err := store.Extend(body.Username, body.Days); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})

	case "set_expires":
		t, _ := parseAdminTime(body.ExpiresAt) // zero = unlimited
		if err := store.SetExpires(body.Username, t); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})

	case "set_group":
		if err := store.SetGroup(body.Username, body.GroupID); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})

	case "set_premium":
		t, _ := parseAdminTime(body.PremiumUntil) // zero = clear premium
		if err := store.SetPremium(body.Username, t); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})

	case "ban":
		if err := store.SetBan(body.Username, true, body.BanReason); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})

	case "unban":
		if err := store.SetBan(body.Username, false, ""); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})

	case "set_max_devices":
		if err := store.SetMaxDevices(body.Username, body.MaxDevices); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})

	case "set_torrserver_disabled":
		if err := store.SetTorrServerDisabled(body.Username, body.Disabled); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})

	case "set_comment":
		if err := store.SetComment(body.Username, body.Comment); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})

	case "remove_device":
		if err := store.RemoveDevice(body.Username, body.DeviceUID); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})

	case "logout_all":
		if err := store.LogoutAll(body.Username); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})

	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown action: " + body.Action})
	}
}

// adminAuthModeHandler exposes / mutates the runtime auth mode.
func adminAuthModeHandler(modeStore *tgauth.AuthModeStore, anonIssuer *tgauth.AnonIssuer, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := tgAdminAuthCheck(w, r, tgStore, adminStore); !ok {
			return
		}
		switch r.Method {
		case http.MethodGet:
			mode := ""
			if modeStore != nil {
				mode = modeStore.Get()
			}
			writeJSON(w, http.StatusOK, map[string]string{"mode": mode})
		case http.MethodPost:
			var body struct {
				Mode string `json:"mode"`
			}
			if err := stdjson.NewDecoder(r.Body).Decode(&body); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
				return
			}
			body.Mode = strings.TrimSpace(body.Mode)
			if modeStore == nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "auth mode store unavailable"})
				return
			}
			old := modeStore.Get()
			if err := modeStore.Set(body.Mode); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			// Switching AWAY from "none" — wipe anon cookies so leftover
			// tokens don't keep granting access in the new mode.
			if old == tgauth.AuthModeNone && body.Mode != tgauth.AuthModeNone && anonIssuer != nil {
				_ = anonIssuer.Purge()
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mode": modeStore.Get()})
		default:
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		}
	}
}

// --- helpers ---

type passwordUserJSON struct {
	Username           string               `json:"username"`
	UID                string               `json:"uid"`
	CreatedAt          string               `json:"created_at"`
	ExpiresAt          string               `json:"expires_at"`
	Expired            bool                 `json:"expired"`
	GroupID            string               `json:"group_id"`
	PremiumUntil       string               `json:"premium_until"`
	PremiumActive      bool                 `json:"premium_active"`
	EffectiveGroupID   string               `json:"effective_group_id"`
	MaxDevices         int                  `json:"max_devices"`
	DeviceCount        int                  `json:"device_count"`
	Devices            []passwordDeviceJSON `json:"devices"`
	TorrServerDisabled bool                 `json:"torrserver_disabled"`
	Ban                bool                 `json:"ban"`
	BanReason          string               `json:"ban_reason,omitempty"`
	Comment            string               `json:"comment,omitempty"`
	SessionCount       int                  `json:"session_count"`
	LastLoginAt        string               `json:"last_login_at,omitempty"`
	LastLoginIP        string               `json:"last_login_ip,omitempty"`
}

type passwordDeviceJSON struct {
	UID         string `json:"uid"`
	Label       string `json:"label"`
	BoundAt     string `json:"bound_at"`
	LastSeen    string `json:"last_seen"`
	LastIP      string `json:"last_ip,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

func serializePasswordUsers(users []tgauth.PasswordUser) []passwordUserJSON {
	out := make([]passwordUserJSON, 0, len(users))
	for _, u := range users {
		out = append(out, serializePasswordUser(u))
	}
	return out
}

func serializePasswordUser(u tgauth.PasswordUser) passwordUserJSON {
	now := time.Now().UTC()
	devs := make([]passwordDeviceJSON, 0, len(u.Devices))
	for _, d := range u.Devices {
		devs = append(devs, passwordDeviceJSON{
			UID:         d.UID,
			Label:       d.Label,
			BoundAt:     d.BoundAt.Format("2006-01-02 15:04"),
			LastSeen:    d.LastSeen.Format("2006-01-02 15:04"),
			LastIP:      d.LastIP,
			Fingerprint: d.Fingerprint,
		})
	}
	expired := !u.ExpiresAt.IsZero() && now.After(u.ExpiresAt)
	premiumActive := !u.PremiumUntil.IsZero() && u.PremiumUntil.After(now)
	premium := ""
	if !u.PremiumUntil.IsZero() {
		premium = u.PremiumUntil.Format("2006-01-02 15:04")
	}
	expStr := ""
	if !u.ExpiresAt.IsZero() {
		expStr = u.ExpiresAt.Format("2006-01-02 15:04")
	}
	lastLogin := ""
	if !u.LastLoginAt.IsZero() {
		lastLogin = u.LastLoginAt.Format("2006-01-02 15:04")
	}
	effective := u.GroupID
	if premiumActive {
		effective = u.EffectiveGroupID(currentPremiumGroupID())
	}
	return passwordUserJSON{
		Username:           u.Username,
		UID:                u.UID,
		CreatedAt:          u.CreatedAt.Format("2006-01-02 15:04"),
		ExpiresAt:          expStr,
		Expired:            expired,
		GroupID:            u.GroupID,
		PremiumUntil:       premium,
		PremiumActive:      premiumActive,
		EffectiveGroupID:   effective,
		MaxDevices:         u.MaxDevices,
		DeviceCount:        len(u.Devices),
		Devices:            devs,
		TorrServerDisabled: u.TorrServerDisabled,
		Ban:                u.Ban,
		BanReason:          u.BanReason,
		Comment:            u.Comment,
		SessionCount:       len(u.Sessions),
		LastLoginAt:        lastLogin,
		LastLoginIP:        u.LastLoginIP,
	}
}

// parseAdminTime accepts RFC3339 or "YYYY-MM-DD" or "YYYY-MM-DD HH:MM".
func parseAdminTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	layouts := []string{
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
