package httpapi

// /api/auth/whoami — single source of truth for the client-side account
// plugin (ubuntu/plugins/account.js). The legacy /api/user/info is
// TG-only and shaped for SURS — we don't want to churn it. This handler
// is generic: reports whoever the middleware resolved (TG, password, or
// anon), plus the current auth mode so the plugin can hide/show the
// "Login" button correctly.
//
// Response shape:
//
//	{
//	  "authenticated": true,
//	  "type":          "tg" | "password" | "anon",
//	  "id":            "tg:123" | "pw:<uid>" | "anon:<uid>",
//	  "username":      "@kirill" | "alice" | "",
//	  "uid":           "deadbeef",
//	  "expires_at":    "2027-01-01T00:00:00Z",
//	  "days_left":     200,
//	  "premium_until":     "2026-08-09T00:00:00Z",  // omitted if never had premium
//	  "premium_active":    true,
//	  "premium_days_left": 29,                       // only when premium_active
//	  "mode":          "tg" | "password" | "none"
//	}
//
// When authenticated=false, only `mode` is meaningful; the plugin uses
// it to render the right "How to log in" text.

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"lampac-go/internal/auth"
)

// addPremiumFields mirrors the bot's premium overlay into the JSON response:
// premium_until/premium_active always when the user ever had premium, plus
// premium_days_left while it is still active. Zero premiumUntil = never had
// premium → no fields at all, so old clients see an unchanged shape.
func addPremiumFields(resp map[string]any, premiumUntil time.Time) {
	if premiumUntil.IsZero() {
		return
	}
	resp["premium_until"] = premiumUntil.Format(time.RFC3339)
	active := premiumUntil.After(time.Now().UTC())
	resp["premium_active"] = active
	if active {
		daysLeft := int(time.Until(premiumUntil).Hours() / 24)
		if daysLeft < 0 {
			daysLeft = 0
		}
		resp["premium_days_left"] = daysLeft
	}
}

func apiAuthWhoamiHandler(cfgPtr func() string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mode := "tg"
		if cfgPtr != nil {
			if m := cfgPtr(); m != "" {
				mode = m
			}
		}

		resp := map[string]any{
			"authenticated": false,
			"mode":          mode,
		}

		user, ok := auth.UserFromContext(r.Context())
		if !ok || user == nil {
			// Cross-origin fallback (external Lampa hosts like lampa.mx):
			// cookies never travel cross-site, so when the middleware
			// resolved nobody, honor an explicit token the same way
			// /api/user/info does — account.js sends it as ?token= and in
			// the X-Alpac-Token/X-Lampac-Token headers.
			// ★Запасной путь по устройству — тот же, что уже есть у
			// /api/user/info. Клиент, потерявший токен при перезапуске
			// (WebView выбрасывает Set-Cookie из XHR, а localStorage на части
			// коробок не переживает перезапуск), продолжает знать свой uid —
			// и сессия по нему восстанавливается. Без этого ответ был
			// «не авторизован» у человека, которому /tg/auth/status в ту же
			// секунду отвечал «авторизован»: отсюда пропавшая иконка профиля.
			tok := resolveOurToken(r)
			if tok == "" && tgTokenStoreRef != nil {
				if uid := strings.TrimSpace(r.URL.Query().Get("uid")); uid != "" {
					tok = tgTokenStoreRef.FindTokenByDeviceUID(uid)
				}
			}
			if tok != "" && tgTokenStoreRef != nil {
				if t, found := tgTokenStoreRef.Lookup(tok); found {
					resp["authenticated"] = true
					resp["type"] = "tg"
					resp["id"] = fmt.Sprintf("tg:%d", t.TelegramID)
					resp["uid"] = fmt.Sprintf("%d", t.TelegramID)
					if t.TGUsername != "" {
						resp["username"] = t.TGUsername
					}
					resp["expires_at"] = t.ExpiresAt.Format(time.RFC3339)
					daysLeft := int(time.Until(t.ExpiresAt).Hours() / 24)
					if daysLeft < 0 {
						daysLeft = 0
					}
					resp["days_left"] = daysLeft
					addPremiumFields(resp, t.PremiumUntil)
					writeJSON(w, http.StatusOK, resp)
					return
				}
			}
			writeJSON(w, http.StatusOK, resp)
			return
		}

		resp["authenticated"] = true
		resp["id"] = user.ID

		// Derive type + uid + username from the User.ID prefix. This is
		// a public-facing API — keep the contract stable even if the
		// internal ID format changes by mapping here, not at the call
		// site.
		switch {
		case strings.HasPrefix(user.ID, "tg:"):
			resp["type"] = "tg"
			resp["uid"] = strings.TrimPrefix(user.ID, "tg:")
		case strings.HasPrefix(user.ID, "pw:"):
			resp["type"] = "password"
			resp["uid"] = strings.TrimPrefix(user.ID, "pw:")
		case strings.HasPrefix(user.ID, "anon:"):
			resp["type"] = "anon"
			resp["uid"] = strings.TrimPrefix(user.ID, "anon:")
		default:
			resp["type"] = "other"
			resp["uid"] = user.ID
		}
		if len(user.Aliases) > 0 {
			resp["username"] = user.Aliases[0]
		}
		if !user.Expires.IsZero() {
			resp["expires_at"] = user.Expires.Format(time.RFC3339)
			daysLeft := int(time.Until(user.Expires).Hours() / 24)
			if daysLeft < 0 {
				daysLeft = 0
			}
			resp["days_left"] = daysLeft
		}

		// Premium overlay lives on the TG token, which auth.User doesn't
		// carry — re-resolve the token from the same request sources the
		// middleware used (cookies/headers/?token=) and look it up.
		if resp["type"] == "tg" && tgTokenStoreRef != nil {
			if tok := resolveOurToken(r); tok != "" {
				if t, found := tgTokenStoreRef.Lookup(tok); found {
					addPremiumFields(resp, t.PremiumUntil)
				}
			}
		}

		writeJSON(w, http.StatusOK, resp)
	}
}
