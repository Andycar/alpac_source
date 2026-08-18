package adminhttp

import (
	stdjson "encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"lampac-go/internal/kit"
	"lampac-go/internal/tgauth"

	"github.com/google/uuid"
)

// --- Users API ---

func TgAdminUsersHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore, kitStore *kit.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tid, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		switch r.Method {
		case http.MethodGet:
			if store == nil {
				writeJSON(w, http.StatusOK, map[string]any{
					"users":     []any{},
					"self_id":   tid,
					"tg_active": false,
				})
				return
			}
			tokens := store.List()
			type deviceRow struct {
				UID         string `json:"uid"`
				Label       string `json:"label"`
				BoundAt     string `json:"bound_at"`
				LastSeen    string `json:"last_seen"`
				LastIP      string `json:"last_ip,omitempty"`
				Fingerprint string `json:"fingerprint,omitempty"`
			}
			type userRow struct {
				Token              string      `json:"token"`
				TGUsername         string      `json:"tg_username"`
				TelegramID         int64       `json:"telegram_id"`
				CreatedAt          string      `json:"created_at"`
				ExpiresAt          string      `json:"expires_at"`
				Expired            bool        `json:"expired"`
				Devices            []deviceRow `json:"devices"`
				DeviceCount        int         `json:"device_count"`
				MaxDevices         int         `json:"max_devices"`
				TorrServerDisabled bool        `json:"torrserver_disabled"`
				GroupID            string      `json:"group_id"`           // BASE group (стандарт)
				PremiumUntil       string      `json:"premium_until"`      // "2026-08-26 15:04" or "" if no premium
				PremiumActive      bool        `json:"premium_active"`     // PremiumUntil > now
				EffectiveGroupID   string      `json:"effective_group_id"` // GroupID OR premium overlay if active
			}
			// premiumGroupID is read once for the whole list so each row can
			// resolve its effective group consistently.
			premiumGroupID := currentPremiumGroupID()
			rows := make([]userRow, 0, len(tokens))
			now := time.Now().UTC()
			for _, t := range tokens {
				devs := make([]deviceRow, 0, len(t.Devices))
				for _, d := range t.Devices {
					devs = append(devs, deviceRow{
						UID:         d.UID,
						Label:       d.Label,
						BoundAt:     d.BoundAt.Format("2006-01-02 15:04"),
						LastSeen:    d.LastSeen.Format("2006-01-02 15:04"),
						LastIP:      d.LastIP,
						Fingerprint: d.Fingerprint,
					})
				}
				premiumStr := ""
				premiumActive := false
				if !t.PremiumUntil.IsZero() {
					premiumStr = t.PremiumUntil.Format("2006-01-02 15:04")
					premiumActive = t.PremiumUntil.After(now)
				}
				effectiveGroup := t.GroupID
				if premiumActive && premiumGroupID != "" {
					effectiveGroup = premiumGroupID
				}
				rows = append(rows, userRow{
					Token:              t.Token,
					TGUsername:         t.TGUsername,
					TelegramID:         t.TelegramID,
					CreatedAt:          t.CreatedAt.Format("2006-01-02 15:04"),
					ExpiresAt:          t.ExpiresAt.Format("2006-01-02 15:04"),
					Expired:            now.After(t.ExpiresAt),
					Devices:            devs,
					DeviceCount:        len(t.Devices),
					MaxDevices:         t.MaxDevices,
					TorrServerDisabled: t.TorrServerDisabled,
					GroupID:            t.GroupID,
					PremiumUntil:       premiumStr,
					PremiumActive:      premiumActive,
					EffectiveGroupID:   effectiveGroup,
				})
			}
			writeJSON(w, http.StatusOK, rows)

		case http.MethodPost:
			if store == nil {
				writeJSON(w, http.StatusOK, map[string]any{"error": "user management requires TG auth"})
				return
			}
			body, _ := io.ReadAll(io.LimitReader(r.Body, 65536))
			var req struct {
				Action             string          `json:"action"`
				Token              string          `json:"token"`
				Days               int             `json:"days"`
				UID                string          `json:"uid"`
				MaxDevices         int             `json:"max_devices"`
				TorrServerDisabled bool            `json:"torrserver_disabled"`
				Visibility         map[string]bool `json:"visibility,omitempty"` // for save_balancers
				GroupID            string          `json:"group_id,omitempty"`
				PremiumUntil       string          `json:"premium_until,omitempty"` // "YYYY-MM-DD" or "YYYY-MM-DD HH:MM" for set_premium
			}
			if stdjson.Unmarshal(body, &req) != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
				return
			}
			switch req.Action {
			case "remove":
				_ = store.Remove(req.Token)
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			case "extend":
				if req.Days <= 0 {
					req.Days = 30
				}
				_ = store.Extend(req.Token, req.Days)
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			case "remove_device":
				if req.Token == "" || req.UID == "" {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "token and uid required"})
					return
				}
				_ = store.RemoveDevice(req.Token, req.UID)
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			case "remove_all_devices":
				// «Удалить отпечатки всех устройств»: RemoveAllDevices чистит записи
				// вместе с fp/sfp и стреляет OnDeviceRemoved на каждый UID (capistore
				// забывает per-device профиль/PIN-гейт). Токен остаётся — юзер просто
				// привяжет устройства заново.
				if req.Token == "" {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "token required"})
					return
				}
				n := store.RemoveAllDevices(req.Token)
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": n})
			case "set_device_limit":
				if req.Token == "" {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "token required"})
					return
				}
				if req.MaxDevices < -1 {
					req.MaxDevices = -1 // -1 = unlimited
				}
				_ = store.SetMaxDevices(req.Token, req.MaxDevices)
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			case "toggle_torrserver":
				if req.Token == "" {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "token required"})
					return
				}
				store.SetTorrServerDisabled(req.Token, req.TorrServerDisabled)
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			case "assign_group":
				if req.Token == "" {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "token required"})
					return
				}
				store.SetGroupID(req.Token, req.GroupID)
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			case "extend_premium":
				// Pushes PremiumUntil forward by Days (stacks on active premium).
				// Effectively the same as the payment flow but triggered manually.
				if req.Token == "" {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "token required"})
					return
				}
				if req.Days <= 0 {
					req.Days = 30
				}
				until := store.ExtendPremium(req.Token, req.Days)
				if until.IsZero() {
					writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "token not found"})
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{
					"ok":            true,
					"premium_until": until.Format("2006-01-02 15:04"),
				})
			case "set_premium_until":
				// Sets PremiumUntil to a specific absolute date — used when the
				// operator wants to fix a date manually (e.g. "до конца месяца")
				// rather than extend by N days. Empty string clears.
				if req.Token == "" {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "token required"})
					return
				}
				var until time.Time
				if s := req.PremiumUntil; s != "" {
					// Accept both "2006-01-02" and "2006-01-02 15:04".
					formats := []string{"2006-01-02 15:04", "2006-01-02"}
					var parseErr error
					for _, f := range formats {
						if tt, err := time.ParseInLocation(f, s, time.UTC); err == nil {
							until = tt
							parseErr = nil
							break
						} else {
							parseErr = err
						}
					}
					if until.IsZero() {
						writeJSON(w, http.StatusBadRequest, map[string]any{
							"error": "invalid date (use YYYY-MM-DD or YYYY-MM-DD HH:MM): " + parseErr.Error(),
						})
						return
					}
				}
				store.SetPremiumUntil(req.Token, until)
				resp := map[string]any{"ok": true}
				if !until.IsZero() {
					resp["premium_until"] = until.Format("2006-01-02 15:04")
				} else {
					resp["premium_until"] = ""
				}
				writeJSON(w, http.StatusOK, resp)
			case "clear_premium":
				// Convenience action — zeroes out PremiumUntil so the user
				// immediately falls back to the base GroupID on next request.
				if req.Token == "" {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "token required"})
					return
				}
				store.SetPremiumUntil(req.Token, time.Time{})
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			case "create":
				days := req.Days
				if days <= 0 {
					days = 30
				}
				newToken := uuid.New().String()
				approved := tgauth.ApprovedToken{
					Token:      newToken,
					TelegramID: tid,
					TGUsername: "device",
					CreatedAt:  time.Now().UTC(),
					ExpiresAt:  time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour),
					ApprovedBy: tid,
				}
				if err := store.Add(approved); err != nil {
					writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
					return
				}
				host := hostFromRequest(r)
				writeJSON(w, http.StatusOK, map[string]any{
					"ok":        true,
					"token":     newToken,
					"lampa_url": host + "/on/js/" + newToken,
					"expires":   approved.ExpiresAt.Format("2006-01-02 15:04"),
				})
			case "get_balancers":
				if req.Token == "" {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "token required"})
					return
				}
				t, found := store.Lookup(req.Token)
				if !found {
					writeJSON(w, http.StatusOK, map[string]any{"error": "token not found"})
					return
				}
				tgID := fmt.Sprintf("%d", t.TelegramID)
				// Load user's kit config for balancer visibility.
				var userVis map[string]bool
				if kitStore != nil {
					kitCfg, _ := kitStore.Load(tgID)
					if kitCfg != nil {
						if raw, ok := kitCfg["_balancerVisibility"]; ok {
							_ = stdjson.Unmarshal(raw, &userVis)
						}
					}
				}
				if userVis == nil {
					userVis = make(map[string]bool)
				}
				mergedConf := loadMergedConf()
				type balInfo struct {
					Key           string `json:"key"`
					Name          string `json:"name"`
					Group         string `json:"group"`
					Quality       string `json:"quality,omitempty"`
					GlobalEnabled bool   `json:"global_enabled"`
					UserVisible   *bool  `json:"user_visible"` // nil = not configured (inherits global)
				}
				bals := make([]balInfo, 0, len(knownBalancers()))
				for _, name := range knownBalancers() {
					key := PluginKeyFor(name)
					grp := balancerGroupMap()[name]
					if grp == "" {
						grp = "other"
					}
					q := pluginQualityBadgeGet(key)
					globalEnabled := true
					if sec, ok := mergedConf[name].(map[string]any); ok {
						if e, ok := sec["enable"]; ok {
							if b, ok := e.(bool); ok {
								globalEnabled = b
							}
						} else if e, ok := sec["enabled"]; ok {
							if b, ok := e.(bool); ok {
								globalEnabled = b
							}
						}
					}
					bi := balInfo{Key: key, Name: name, Group: grp, Quality: q, GlobalEnabled: globalEnabled}
					if v, ok := userVis[key]; ok {
						vis := v
						bi.UserVisible = &vis
					}
					bals = append(bals, bi)
				}
				writeJSON(w, http.StatusOK, map[string]any{"balancers": bals})

			case "save_balancers":
				if req.Token == "" {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "token required"})
					return
				}
				t, found := store.Lookup(req.Token)
				if !found {
					writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "token not found"})
					return
				}
				if kitStore == nil {
					writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "kit not enabled"})
					return
				}
				tgID := fmt.Sprintf("%d", t.TelegramID)
				if req.Visibility == nil {
					// Reset — remove visibility section entirely.
					_ = kitStore.DeleteSection(tgID, "_balancerVisibility")
				} else {
					_ = kitStore.UpdateSection(tgID, "_balancerVisibility", req.Visibility)
				}
				kitStore.Invalidate(tgID)
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})

			default:
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown action"})
			}
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}
