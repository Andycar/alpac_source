package httpapi

// Password authentication HTTP layer.
//
// Three endpoints under /auth/password:
//
//   POST /auth/password/login    json {"username","password"} → sets cookies
//   POST /auth/password/logout   clears cookies + invalidates session
//   GET  /auth/password/me       returns the current logged-in user as json
//
// Plus a status endpoint that the /bkit page polls to decide which login
// surface to render:
//
//   GET  /auth/mode              returns {"mode":"tg|password|none","password_enabled":bool}
//
// Cookies are issued via setAuthCookies (the same helper TG auth uses),
// so a password session reaches all the same proxies/middleware paths
// — there's no parallel cookie namespace to keep in sync.

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/tgauth"

	"github.com/rs/zerolog/log"
)

// Globals set by server.go during boot so the legacy tg_auth page handler
// can route to the new surfaces without a giant signature change.
var (
	globalPwUserStore   *tgauth.PasswordUserStore
	globalAnonIssuer    *tgauth.AnonIssuer
	globalAuthModeStore *tgauth.AuthModeStore
)

// SetPasswordAuthState is called by server.go after the user-auth stores
// are constructed, so package-level helpers (renderPasswordAuthPage,
// currentAuthMode, etc.) can resolve them without dependency injection
// through every handler. Mirrors the pattern used by kitTGTokenStore /
// bkitSessions in server_routes_userauth.go.
func SetPasswordAuthState(pwUserStore *tgauth.PasswordUserStore, anonIssuer *tgauth.AnonIssuer, modeStore *tgauth.AuthModeStore) {
	globalPwUserStore = pwUserStore
	globalAnonIssuer = anonIssuer
	globalAuthModeStore = modeStore
}

// currentAuthMode returns the effective mode at request time. Returns
// AuthModeTG when the store isn't wired (boot race / tests).
func currentAuthMode() string {
	if globalAuthModeStore == nil {
		return config.AuthModeTG
	}
	return globalAuthModeStore.Get()
}

// passwordLoginLimiter is a per-IP attempt counter used by passwordLoginHandler.
// Hits reset after LockoutMinutes from the FIRST attempt in the current
// window — keeps the implementation simple (no sliding window). Memory
// footprint is bounded by the IP space hitting the endpoint; old entries
// are purged in-place when their reset time passes.
type passwordLoginLimiter struct {
	mu      sync.Mutex
	entries map[string]*passwordLoginEntry
}

type passwordLoginEntry struct {
	attempts int
	firstAt  time.Time
}

var globalPasswordLoginLimiter = &passwordLoginLimiter{entries: make(map[string]*passwordLoginEntry)}

// allow returns (false, retryAfterSeconds) when the IP exceeds maxAttempts
// within lockoutMinutes of the first attempt. The first allow() call after
// the window expires resets the counter. Counter increments on failure
// only — callers call recordFailure() after a bad password.
func (l *passwordLoginLimiter) allow(ip string, maxAttempts, lockoutMinutes int) (bool, int) {
	if maxAttempts <= 0 || ip == "" {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now().UTC()
	window := time.Duration(lockoutMinutes) * time.Minute
	if window <= 0 {
		window = 10 * time.Minute
	}
	e, ok := l.entries[ip]
	if !ok || now.Sub(e.firstAt) > window {
		l.entries[ip] = &passwordLoginEntry{firstAt: now}
		return true, 0
	}
	if e.attempts >= maxAttempts {
		remain := int(window.Seconds()) - int(now.Sub(e.firstAt).Seconds())
		if remain < 1 {
			remain = 1
		}
		return false, remain
	}
	return true, 0
}

func (l *passwordLoginLimiter) recordFailure(ip string) {
	if ip == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.entries[ip]; ok {
		e.attempts++
	} else {
		l.entries[ip] = &passwordLoginEntry{attempts: 1, firstAt: time.Now().UTC()}
	}
}

func (l *passwordLoginLimiter) clear(ip string) {
	if ip == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, ip)
}

// passwordLoginHandler handles POST /auth/password/login.
func passwordLoginHandler(store *tgauth.PasswordUserStore, cfgPtr func() *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		cfg := cfgPtr()
		// Gate the endpoint on the runtime mode + enable flag. We still
		// accept POSTs when mode="tg" but the password sub-section is
		// enabled — the admin v2 panel needs a way to set up users
		// before flipping the mode.
		pwCfg := cfg.Auth.Password
		if cfg.Auth.Mode != config.AuthModePassword && !pwCfg.Enable {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "password auth disabled"})
			return
		}

		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
			return
		}
		req.Username = strings.TrimSpace(req.Username)

		ip := clientIP(r)
		maxAttempts := pwCfg.MaxLoginAttempts
		if maxAttempts <= 0 {
			maxAttempts = 5
		}
		lockoutMin := pwCfg.LockoutMinutes
		if lockoutMin <= 0 {
			lockoutMin = 10
		}
		if ok, retry := globalPasswordLoginLimiter.allow(ip, maxAttempts, lockoutMin); !ok {
			w.Header().Set("Retry-After", time.Now().Add(time.Duration(retry)*time.Second).Format(time.RFC1123))
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"error":           "too many attempts",
				"retry_after_sec": retry,
			})
			return
		}

		sessionDays := pwCfg.SessionDays
		if sessionDays <= 0 {
			sessionDays = 365
		}
		user, token, err := store.Login(req.Username, req.Password, ip, r.Header.Get("User-Agent"), sessionDays)
		if err != nil {
			globalPasswordLoginLimiter.recordFailure(ip)
			status := http.StatusUnauthorized
			msg := "invalid credentials"
			switch err {
			case tgauth.ErrUserBanned:
				msg = "account banned"
				status = http.StatusForbidden
			case tgauth.ErrUserExpired:
				msg = "subscription expired"
				status = http.StatusForbidden
			}
			log.Info().Err(err).Str("ip", ip).Str("username", req.Username).Msg("password login failed")
			writeJSON(w, status, map[string]string{"error": msg})
			return
		}
		globalPasswordLoginLimiter.clear(ip)

		setAuthCookies(w, r, token)
		log.Info().Str("username", user.Username).Str("ip", ip).Msg("password login success")
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":       true,
			"token":    token,
			"username": user.Username,
			"uid":      user.UID,
			"expires":  user.ExpiresAt.Format(time.RFC3339),
		})
	}
}

// passwordLogoutHandler handles POST /auth/password/logout.
func passwordLogoutHandler(store *tgauth.PasswordUserStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Logout is idempotent — we don't reject GET/POST methods. The
		// only side effect is clearing cookies + invalidating one session.
		var token string
		if c, err := r.Cookie("alpac_token"); err == nil {
			token = strings.TrimSpace(c.Value)
		}
		if token == "" {
			if c, err := r.Cookie("lampac_token"); err == nil {
				token = strings.TrimSpace(c.Value)
			}
		}
		if token != "" {
			_ = store.Logout(token)
		}
		clearAuthCookies(w, r)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

// passwordMeHandler returns the currently logged-in password user.
// Used by the bkit page after login to refresh its UI state.
func passwordMeHandler(store *tgauth.PasswordUserStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var token string
		if c, err := r.Cookie("alpac_token"); err == nil {
			token = strings.TrimSpace(c.Value)
		}
		if token == "" {
			if c, err := r.Cookie("lampac_token"); err == nil {
				token = strings.TrimSpace(c.Value)
			}
		}
		if token == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "no session"})
			return
		}
		user, _, ok := store.LookupSession(token)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "session expired"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"username":      user.Username,
			"uid":           user.UID,
			"group_id":      user.GroupID,
			"premium_until": formatTimeOrEmpty(user.PremiumUntil),
			"expires_at":    formatTimeOrEmpty(user.ExpiresAt),
			"max_devices":   user.MaxDevices,
			"device_count":  len(user.Devices),
			"ban":           user.Ban,
			"created_at":    user.CreatedAt.Format(time.RFC3339),
			"last_login_at": formatTimeOrEmpty(user.LastLoginAt),
		})
	}
}

// authModeHandler exposes the current auth mode so the public login pages
// can render the correct surface. No auth required — the modes themselves
// are public information; what's gated is the user list (admin only).
func authModeHandler(cfgPtr func() *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := cfgPtr()
		mode := cfg.Auth.Mode
		if mode == "" {
			mode = config.AuthModeTG
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"mode":             mode,
			"password_enabled": cfg.Auth.Password.Enable || mode == config.AuthModePassword,
			"tg_enabled":       cfg.TelegramAuth.Enable,
			"min_password_len": cfg.Auth.Password.MinPasswordLen,
		})
	}
}

func formatTimeOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}
