package httpapi

import (
	"crypto/rand"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"lampac-go/internal/adminhttp"
	"lampac-go/internal/tgauth"
)

// --- Admin path generation & persistence ---

const adminPathFile = "database/tgauth/admin_path.txt"

func loadOrGenerateAdminPath() string {
	path := relToRuntime(adminPathFile)
	if data, err := os.ReadFile(path); err == nil {
		if v := strings.TrimSpace(string(data)); v != "" {
			return v
		}
	}
	ap := "cp_" + randomAlphaNum(10)
	dir := filepath.Dir(path)
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(path, []byte(ap), 0o644)
	return ap
}

func randomAlphaNum(n int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	for i := range b {
		idx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		b[i] = charset[idx.Int64()]
	}
	return string(b)
}

// --- Admin auth (three-tier) ---

// Package-level vars set by server.go during initialization.
// Used by tgAdminAuthCheck for password-based auth (Tier 2).
var (
	pwAuthStore     *tgauth.PasswordAuthStore // nil when password auth not configured
	adminSessionKey []byte                    // HMAC signing key for admin sessions
	activeAdminPath string                    // current admin panel path (e.g. "cp_abc123")
)

// tgAdminAuthCheck verifies the request belongs to an admin.
// Returns telegramID, isSuperAdmin, ok. Writes error/redirect if not ok.
//
// Three tiers (checked in order):
//  1. TG auth (store != nil) — cookie/token lookup via TG bot
//  2. Password auth (pwAuthStore configured) — HMAC session cookie
//  3. Localhost fallback — allows 127.0.0.1 / ::1 without credentials
func tgAdminAuthCheck(w http.ResponseWriter, r *http.Request, store *tgauth.Store, adminStore *tgauth.AdminIDStore) (int64, bool, bool) {
	// Tier 1: TG auth enabled.
	if store != nil {
		// Primary credential: lampac_token cookie. If absent but the URL
		// carries ?token=, treat that as a *one-shot promotion* — set the
		// cookie and redirect to the same URL without the token in it.
		// This keeps deep links from TG bot working, but the token never
		// stays in the address bar / browser history / access logs after
		// the first hit.
		var tokenStr string
		if c, err := r.Cookie("lampac_token"); err == nil {
			tokenStr = strings.TrimSpace(c.Value)
		}
		if tokenStr == "" {
			if urlTok := strings.TrimSpace(r.URL.Query().Get("token")); urlTok != "" {
				if _, ok := store.Lookup(urlTok); ok {
					setAuthCookies(w, r, urlTok)
					q := r.URL.Query()
					q.Del("token")
					target := r.URL.Path
					if enc := q.Encode(); enc != "" {
						target += "?" + enc
					}
					http.Redirect(w, r, target, http.StatusFound)
					return 0, false, false
				}
				// URL token didn't validate — fall through to /tg/auth redirect.
			}
		}
		if tokenStr == "" {
			http.Redirect(w, r, "/tg/auth", http.StatusFound)
			return 0, false, false
		}
		t, ok := store.Lookup(tokenStr)
		if !ok {
			http.Redirect(w, r, "/tg/auth", http.StatusFound)
			return 0, false, false
		}
		if !adminStore.IsAdmin(t.TelegramID) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("403 Forbidden: not admin"))
			return 0, false, false
		}
		return t.TelegramID, adminStore.IsSuperAdmin(t.TelegramID), true
	}

	// Tier 2: Password auth configured.
	if pwAuthStore != nil && pwAuthStore.IsConfigured() && len(adminSessionKey) > 0 {
		cookie, err := r.Cookie(adminhttp.AdminSessionCookie)
		if err != nil || cookie.Value == "" {
			http.Redirect(w, r, "/"+activeAdminPath+"/login", http.StatusFound)
			return 0, false, false
		}
		partial, ok := adminhttp.ValidateAdminSession(cookie.Value, adminSessionKey)
		if !ok {
			http.Redirect(w, r, "/"+activeAdminPath+"/login", http.StatusFound)
			return 0, false, false
		}
		if partial {
			// Password verified but TOTP code not yet entered. Two cases:
			//  - TOTP is enabled → user still owes the second factor; bounce
			//    to /login so they can enter the code.
			//  - TOTP is disabled → 2FA is optional; this path shouldn't happen
			//    after the login flow normalises to a full session, but stale
			//    cookies still hit it. Send them through /login fresh.
			http.Redirect(w, r, "/"+activeAdminPath+"/login", http.StatusFound)
			return 0, false, false
		}
		// Full session — grant super admin access.
		superID := int64(0)
		if adminStore != nil {
			superID = adminStore.SuperAdminID()
		}
		return superID, true, true
	}

	// Tier 3: Localhost fallback (no auth configured). Honoured ONLY for a
	// genuine direct loopback connection. If the request carries proxy
	// forwarding headers it arrived through a reverse proxy (e.g. nginx →
	// 127.0.0.1), where r.RemoteAddr reads 127.0.0.1 for *all* external
	// traffic — granting super-admin there would open the panel to the
	// whole internet. In that topology, configure real auth instead.
	forwarded := r.Header.Get("X-Forwarded-For") != "" ||
		r.Header.Get("X-Real-IP") != "" ||
		r.Header.Get("Forwarded") != ""
	remoteIP := r.RemoteAddr
	if idx := strings.LastIndex(remoteIP, ":"); idx >= 0 {
		remoteIP = remoteIP[:idx]
	}
	remoteIP = strings.Trim(remoteIP, "[]")
	if !forwarded && (remoteIP == "127.0.0.1" || remoteIP == "::1" || remoteIP == "localhost") {
		superID := int64(0)
		if adminStore != nil {
			superID = adminStore.SuperAdminID()
		}
		return superID, true, true
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte("403 Forbidden: auth not configured, admin panel available on localhost only"))
	return 0, false, false
}

// --- Whoami ---

func tgAdminWhoamiHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tid, isSuper, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"telegram_id": tid,
			"is_super":    isSuper,
			"role":        string(adminStore.RoleOf(tid)),
		})
	}
}
