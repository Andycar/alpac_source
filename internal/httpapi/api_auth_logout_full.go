package httpapi

// Full logout — Settings → Аккаунт «Выйти» entry point.
//
// What the user expects: signing out makes the server forget this
// device. The legacy tgAuthLogoutHandler only cleared cookies; the
// device record stayed in tgauth.Store, so the next request that
// carried `?uid=<bound>` would silently re-auth via the gate's
// UID-auto-reauth branch (auth_tg_api.go ~1420) or the middleware
// UID fallback added 2026-05-28. The user reports it as «logout
// нет полный — устройство всё равно показывается в аккаунте».
//
// This handler:
//
//   1. Reads the active token from cookie / header / ?token=.
//   2. Reads the device UID from ?uid= or X-User-Uid (Lampa stamps it
//      automatically — same channel online.js's account() helper uses).
//   3. Removes the device from the TG token's device list. If the
//      token has zero devices left and the request opts in via
//      ?revoke_token=1, the token itself is removed too.
//   4. Invalidates the matching password-store session if the token
//      belongs to a password user.
//   5. Wipes the anon record if the token came from anon issuance.
//   6. Clears all auth cookies.
//
// Response: {"ok":true, "removed":{"device":bool,"token":bool,"password_session":bool,"anon":bool}}.

import (
	"net/http"
	"strings"

	"lampac-go/internal/tgauth"

	"github.com/rs/zerolog/log"
)

func apiAuthLogoutFullHandler(tgStore *tgauth.Store, pwUserStore *tgauth.PasswordUserStore, anonIssuer *tgauth.AnonIssuer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Read the credentials before we wipe the cookies.
		token := readAnyAuthToken(r)
		uid := strings.TrimSpace(r.URL.Query().Get("uid"))
		if uid == "" {
			uid = strings.TrimSpace(r.Header.Get("X-User-Uid"))
		}
		revokeToken := r.URL.Query().Get("revoke_token") == "1"

		removed := map[string]bool{
			"device":           false,
			"token":            false,
			"password_session": false,
			"anon":             false,
		}

		// --- TG store path ---
		if tgStore != nil {
			// Try to resolve token from UID if the request didn't carry one.
			if token == "" && uid != "" {
				token = tgStore.FindTokenByDeviceUID(uid)
			}
			if token != "" {
				if uid != "" {
					if err := tgStore.RemoveDevice(token, uid); err == nil {
						removed["device"] = true
					}
				}
				if revokeToken {
					if t, ok := tgStore.Lookup(token); ok && len(t.Devices) == 0 {
						if err := tgStore.Remove(token); err == nil {
							removed["token"] = true
						}
					}
				}
			}
		}

		// --- Password store path ---
		if pwUserStore != nil && token != "" {
			if err := pwUserStore.Logout(token); err == nil {
				removed["password_session"] = true
			}
		}

		// --- Anon issuer path ---
		if anonIssuer != nil && token != "" {
			if err := anonIssuer.Revoke(token); err == nil {
				// Revoke is silent on miss — only count if the token
				// matched. Lookup before to avoid false-positive.
				if _, found := anonIssuer.Lookup(token); !found {
					removed["anon"] = true
				}
			}
		}

		clearAuthCookies(w, r)
		log.Info().Str("ip", clientIP(r)).Str("uid", uid).
			Bool("device_removed", removed["device"]).
			Bool("token_removed", removed["token"]).
			Bool("pw_session_revoked", removed["password_session"]).
			Bool("anon_revoked", removed["anon"]).
			Msg("auth logout full")

		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"removed": removed,
		})
	}
}

// readAnyAuthToken extracts the token from cookie / header / ?token=
// — same priority list as the auth middleware's resolver. Pulled
// out here so logout doesn't drag in the auth package directly.
func readAnyAuthToken(r *http.Request) string {
	if c, err := r.Cookie("alpac_token"); err == nil {
		if v := strings.TrimSpace(c.Value); v != "" {
			return v
		}
	}
	if c, err := r.Cookie("lampac_token"); err == nil {
		if v := strings.TrimSpace(c.Value); v != "" {
			return v
		}
	}
	if v := strings.TrimSpace(r.Header.Get("X-Alpac-Token")); v != "" {
		return v
	}
	if v := strings.TrimSpace(r.Header.Get("X-Lampac-Token")); v != "" {
		return v
	}
	return strings.TrimSpace(r.URL.Query().Get("token"))
}
