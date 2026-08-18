package httpapi

import (
	stdjson "encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/auth"
	"lampac-go/internal/profile"
)

// profileStoreRef holds the singleton profile.Store. Wired in server.go at
// startup; nil-safe — when nil, profile endpoints return 503 and the auth
// middleware silently skips the profile-session check.
var profileStoreRef *profile.Store

// profileChecker adapts profile.Store to the auth.ProfileSessionChecker
// interface so internal/auth doesn't have to import internal/profile.
type profileChecker struct{ s *profile.Store }

func (c profileChecker) LookupProfileSession(token string) (string, string, time.Time, bool) {
	if c.s == nil {
		return "", "", time.Time{}, false
	}
	sess, p, err := c.s.LookupSession(token)
	if err != nil || sess == nil || p == nil {
		return "", "", time.Time{}, false
	}
	return p.ID, p.Username, time.UnixMilli(sess.ExpiresAt), true
}

// ProfileSessionChecker returns the auth-package adapter, suitable for
// passing into auth.WithProfileStore at server construction.
func ProfileSessionChecker(s *profile.Store) auth.ProfileSessionChecker {
	return profileChecker{s: s}
}

// SetProfileStore wires the store reference. Called from server.New.
func SetProfileStore(s *profile.Store) { profileStoreRef = s }

const (
	profileSessionCookie = "lampac_profile_session"

	// Rate limit: at most 5 register or 10 login attempts per remote IP
	// per minute. Naive in-process token bucket — sufficient for the
	// realistic threat (manual spray); for a coordinated attack the
	// upstream reverse proxy is the right place to rate-limit.
	rateLimitRegisterPerMin = 5
	rateLimitLoginPerMin    = 10
)

type profileRateBucket struct {
	count     int
	windowEnd time.Time
}

var (
	profileRateMu       sync.Mutex
	profileRegisterRate = map[string]*profileRateBucket{}
	profileLoginRate    = map[string]*profileRateBucket{}
)

func profileRateAllow(table map[string]*profileRateBucket, key string, limit int) bool {
	profileRateMu.Lock()
	defer profileRateMu.Unlock()
	now := time.Now()
	b := table[key]
	if b == nil || now.After(b.windowEnd) {
		table[key] = &profileRateBucket{count: 1, windowEnd: now.Add(time.Minute)}
		return true
	}
	if b.count >= limit {
		return false
	}
	b.count++
	return true
}

// readJSONBody parses a small request body into v. We cap at 8KB — profile
// payloads are tiny (username+password+code) so anything larger is abuse
// or a misbehaving client.
func readJSONBody(r *http.Request, v any) error {
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, 8192))
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return errors.New("empty body")
	}
	return stdjson.Unmarshal(data, v)
}

func profileRegisterHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if profileStoreRef == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "error": "profile_store_disabled"})
			return
		}
		if !profileRateAllow(profileRegisterRate, clientIP(r), rateLimitRegisterPerMin) {
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"success": false, "error": "rate_limited"})
			return
		}

		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := readJSONBody(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "bad_request"})
			return
		}

		p, err := profileStoreRef.Register(req.Username, req.Password)
		if err != nil {
			code, slug := mapProfileError(err)
			writeJSON(w, code, map[string]any{"success": false, "error": slug})
			return
		}

		// Issue a session immediately so the client doesn't need a second
		// round-trip — feels native.
		sess, _, loginErr := profileStoreRef.Login(req.Username, req.Password, r.UserAgent())
		if loginErr != nil {
			// Shouldn't happen, but degrade gracefully.
			writeJSON(w, http.StatusOK, map[string]any{
				"success":    true,
				"profile_id": p.ID,
				"username":   p.Username,
				"login":      "pending",
			})
			return
		}
		issueProfileSessionCookie(w, r, sess)
		writeJSON(w, http.StatusOK, map[string]any{
			"success":    true,
			"profile_id": p.ID,
			"username":   p.Username,
			"expires_at": sess.ExpiresAt,
			// session_token mirrors the Set-Cookie value so clients in
			// environments that strip cookies from XHR responses (Android
			// WebView, some embedded browsers) can persist it themselves
			// and resend via the X-Lampac-Profile-Session header. See
			// auth.go resolveUser for the header path.
			"session_token": sess.Token,
		})
	}
}

func profileLoginHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if profileStoreRef == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "error": "profile_store_disabled"})
			return
		}
		if !profileRateAllow(profileLoginRate, clientIP(r), rateLimitLoginPerMin) {
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"success": false, "error": "rate_limited"})
			return
		}

		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := readJSONBody(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "bad_request"})
			return
		}

		sess, p, err := profileStoreRef.Login(req.Username, req.Password, r.UserAgent())
		if err != nil {
			code, slug := mapProfileError(err)
			writeJSON(w, code, map[string]any{"success": false, "error": slug})
			return
		}
		issueProfileSessionCookie(w, r, sess)
		writeJSON(w, http.StatusOK, map[string]any{
			"success":       true,
			"profile_id":    p.ID,
			"username":      p.Username,
			"expires_at":    sess.ExpiresAt,
			"session_token": sess.Token, // see register handler for rationale
		})
	}
}

func profileLogoutHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if profileStoreRef != nil {
			// Look up the live token from any of our cookie names plus the
			// header path — same priority order as auth.go resolveUser so a
			// client that arrived via header still gets its session purged.
			var tok string
			for _, name := range profileSessionCookieNames {
				if c, err := r.Cookie(name); err == nil && c.Value != "" {
					tok = c.Value
					break
				}
			}
			if tok == "" {
				if h := strings.TrimSpace(r.Header.Get("X-Alpac-Profile-Session")); h != "" {
					tok = h
				} else if h := strings.TrimSpace(r.Header.Get("X-Lampac-Profile-Session")); h != "" {
					tok = h
				}
			}
			if tok != "" {
				profileStoreRef.Logout(tok)
			}
		}
		// Always clear the cookie even if the store is nil — keeps the
		// client deterministic.
		clearProfileSessionCookie(w, r)
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
	}
}

func profileMeHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := auth.UserFromContext(r.Context())
		if !ok || user == nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"success":       true,
				"authenticated": false,
				"auth_method":   "none",
			})
			return
		}
		method := "other"
		switch {
		case strings.HasPrefix(user.ID, "tg:"):
			method = "telegram"
		case strings.HasPrefix(user.ID, "profile:"):
			method = "profile"
		}
		resp := map[string]any{
			"success":       true,
			"authenticated": true,
			"auth_method":   method,
			"user_id":       user.ID,
		}
		if len(user.Aliases) > 0 {
			resp["username"] = user.Aliases[0]
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func profileChangePasswordHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if profileStoreRef == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "error": "profile_store_disabled"})
			return
		}
		user, ok := auth.UserFromContext(r.Context())
		if !ok || user == nil || !strings.HasPrefix(user.ID, "profile:") {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "error": "not_authenticated"})
			return
		}
		profileID := strings.TrimPrefix(user.ID, "profile:")

		var req struct {
			Old string `json:"old_password"`
			New string `json:"new_password"`
		}
		if err := readJSONBody(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "bad_request"})
			return
		}

		if err := profileStoreRef.ChangePassword(profileID, req.Old, req.New); err != nil {
			code, slug := mapProfileError(err)
			writeJSON(w, code, map[string]any{"success": false, "error": slug})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
	}
}

func profileIssueSyncCodeHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if profileStoreRef == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "error": "profile_store_disabled"})
			return
		}
		user, ok := auth.UserFromContext(r.Context())
		if !ok || user == nil || !strings.HasPrefix(user.ID, "profile:") {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "error": "not_authenticated"})
			return
		}
		profileID := strings.TrimPrefix(user.ID, "profile:")

		code, err := profileStoreRef.IssueSyncCode(profileID)
		if err != nil {
			httpCode, slug := mapProfileError(err)
			writeJSON(w, httpCode, map[string]any{"success": false, "error": slug})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success":    true,
			"code":       code.Code,
			"expires_at": code.ExpiresAt,
		})
	}
}

func profileRedeemSyncCodeHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if profileStoreRef == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "error": "profile_store_disabled"})
			return
		}
		if !profileRateAllow(profileLoginRate, clientIP(r), rateLimitLoginPerMin) {
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"success": false, "error": "rate_limited"})
			return
		}

		var req struct {
			Code string `json:"code"`
		}
		if err := readJSONBody(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "bad_request"})
			return
		}

		sess, p, err := profileStoreRef.RedeemSyncCode(req.Code, r.UserAgent())
		if err != nil {
			code, slug := mapProfileError(err)
			writeJSON(w, code, map[string]any{"success": false, "error": slug})
			return
		}
		issueProfileSessionCookie(w, r, sess)
		writeJSON(w, http.StatusOK, map[string]any{
			"success":       true,
			"profile_id":    p.ID,
			"username":      p.Username,
			"expires_at":    sess.ExpiresAt,
			"session_token": sess.Token, // see register handler for rationale
		})
	}
}

// mapProfileError translates a profile sentinel error to an HTTP status +
// machine-readable slug. Clients render a localized message based on the slug.
func mapProfileError(err error) (int, string) {
	switch {
	case errors.Is(err, profile.ErrUsernameTaken):
		return http.StatusConflict, "username_taken"
	case errors.Is(err, profile.ErrUsernameInvalid):
		return http.StatusBadRequest, "username_invalid"
	case errors.Is(err, profile.ErrPasswordWeak):
		return http.StatusBadRequest, "password_weak"
	case errors.Is(err, profile.ErrInvalidCredentials):
		return http.StatusUnauthorized, "invalid_credentials"
	case errors.Is(err, profile.ErrProfileNotFound):
		return http.StatusNotFound, "profile_not_found"
	case errors.Is(err, profile.ErrSessionExpired):
		return http.StatusUnauthorized, "session_expired"
	case errors.Is(err, profile.ErrSyncCodeInvalid):
		return http.StatusBadRequest, "sync_code_invalid"
	case errors.Is(err, profile.ErrSyncCodeUsed):
		return http.StatusBadRequest, "sync_code_used"
	case errors.Is(err, profile.ErrPINInvalid):
		return http.StatusBadRequest, "pin_invalid"
	case errors.Is(err, profile.ErrPINNotSet):
		return http.StatusBadRequest, "pin_not_set"
	case errors.Is(err, profile.ErrNotOwner):
		return http.StatusForbidden, "not_owner"
	default:
		return http.StatusInternalServerError, "internal"
	}
}

// --- PIN auth (no TG required) ---

// profilePinLoginHandler authenticates with {username, pin}. Same rate-limit
// table as password login; the 10/min cap applies per IP across both forms.
func profilePinLoginHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if profileStoreRef == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "error": "profile_store_disabled"})
			return
		}
		if !profileRateAllow(profileLoginRate, clientIP(r), rateLimitLoginPerMin) {
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"success": false, "error": "rate_limited"})
			return
		}

		var req struct {
			Username string `json:"username"`
			PIN      string `json:"pin"`
		}
		if err := readJSONBody(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "bad_request"})
			return
		}

		sess, p, err := profileStoreRef.LoginByPIN(req.Username, req.PIN, r.UserAgent())
		if err != nil {
			code, slug := mapProfileError(err)
			writeJSON(w, code, map[string]any{"success": false, "error": slug})
			return
		}
		issueProfileSessionCookie(w, r, sess)
		writeJSON(w, http.StatusOK, map[string]any{
			"success":       true,
			"profile_id":    p.ID,
			"username":      p.Username,
			"expires_at":    sess.ExpiresAt,
			"session_token": sess.Token, // see register handler for rationale
		})
	}
}

// --- TG-owned profile management ---
//
// These handlers all require the caller to be authenticated as a TG user.
// The shared `tgOwnerFromRequest` helper extracts the TG ID from the auth
// context. Profile mutations check OwnerTGID on every operation so a TG
// user can never act on a profile they don't own.

// tgOwnerFromRequest returns the caller's TG ID (>0) when the request was
// authenticated via TG. Returns 0 when the caller is anonymous or signed
// in via a different method (profile session, etc.).
func tgOwnerFromRequest(r *http.Request) int64 {
	user, ok := auth.UserFromContext(r.Context())
	if !ok || user == nil || !strings.HasPrefix(user.ID, "tg:") {
		return 0
	}
	var id int64
	for _, c := range user.ID[3:] {
		if c < '0' || c > '9' {
			return 0
		}
		id = id*10 + int64(c-'0')
	}
	return id
}

func profileOwnedListHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if profileStoreRef == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "error": "profile_store_disabled"})
			return
		}
		tgID := tgOwnerFromRequest(r)
		if tgID == 0 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "error": "tg_required"})
			return
		}
		list := profileStoreRef.ListByOwnerTG(tgID)
		out := make([]map[string]any, 0, len(list))
		for _, p := range list {
			out = append(out, map[string]any{
				"id":             p.ID,
				"username":       p.Username,
				"has_pin":        p.PINHash != "",
				"pin_updated_at": p.PINUpdatedAt,
				"created_at":     p.CreatedAt,
				"last_login_at":  p.LastLoginAt,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "profiles": out})
	}
}

func profileOwnedCreateHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if profileStoreRef == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "error": "profile_store_disabled"})
			return
		}
		tgID := tgOwnerFromRequest(r)
		if tgID == 0 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "error": "tg_required"})
			return
		}
		var req struct {
			Username string `json:"username"`
			PIN      string `json:"pin"`
		}
		if err := readJSONBody(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "bad_request"})
			return
		}
		p, err := profileStoreRef.CreateForTGOwner(tgID, req.Username, req.PIN)
		if err != nil {
			code, slug := mapProfileError(err)
			writeJSON(w, code, map[string]any{"success": false, "error": slug})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success":    true,
			"profile_id": p.ID,
			"username":   p.Username,
			"has_pin":    p.PINHash != "",
		})
	}
}

// profileOwnedSetPINHandler accepts the target profile ID in the JSON body
// rather than a path parameter so the chi router stays flat
// (registerRoutes already has many path-paramed routes). Body shape:
//
//	{"profile_id": "abc", "pin": "1234"}
//
// Empty pin clears the PIN — disables PIN login but keeps the profile.
func profileOwnedSetPINHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if profileStoreRef == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "error": "profile_store_disabled"})
			return
		}
		tgID := tgOwnerFromRequest(r)
		if tgID == 0 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "error": "tg_required"})
			return
		}
		var req struct {
			ProfileID string `json:"profile_id"`
			PIN       string `json:"pin"`
		}
		if err := readJSONBody(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "bad_request"})
			return
		}
		if err := profileStoreRef.SetPIN(req.ProfileID, tgID, req.PIN); err != nil {
			code, slug := mapProfileError(err)
			writeJSON(w, code, map[string]any{"success": false, "error": slug})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
	}
}

func profileOwnedDeleteHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if profileStoreRef == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "error": "profile_store_disabled"})
			return
		}
		tgID := tgOwnerFromRequest(r)
		if tgID == 0 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "error": "tg_required"})
			return
		}
		var req struct {
			ProfileID string `json:"profile_id"`
		}
		if err := readJSONBody(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "error": "bad_request"})
			return
		}
		if err := profileStoreRef.DeleteOwned(req.ProfileID, tgID); err != nil {
			code, slug := mapProfileError(err)
			writeJSON(w, code, map[string]any{"success": false, "error": slug})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
	}
}

// issueProfileSessionCookie sets the lampac_profile_session cookie.
//
// SameSite tuning:
//   - HTTPS request → SameSite=None, Secure=true. Required for the cookie
//     to be sent on cross-origin XHRs to this host. The canonical
//     scenario is the Lampa player loaded from lampa.mx talking to a
//     self-hosted lampac at beta.l-vid.online: with SameSite=Lax the
//     browser would withhold the cookie and the user would appear
//     signed-out on every page load even after a successful login.
//   - Plain HTTP → SameSite=Lax. `SameSite=None` mandates Secure, which
//     plain-HTTP dev installs can't satisfy; fall back to Lax (same-origin
//     still works, cross-origin doesn't, which is expected for HTTP).
//
// HttpOnly stays on either way — the token is sent back via the
// X-Lampac-Profile-Session header path (auth.go resolveUser) when
// JavaScript needs to manually replay it, so we don't have to expose it
// to the page's scripts.
// profileSessionCookieNames lists every name we ship the profile session
// token under, in priority order (alpac_* is our brand-scoped namespace,
// lampac_* is the legacy/external one we still publish for backward
// compatibility). auth.go resolveUser walks the same list when reading.
var profileSessionCookieNames = []string{
	"alpac_profile_session",
	"lampac_profile_session", // kept in sync with profileSessionCookie const
}

func issueProfileSessionCookie(w http.ResponseWriter, r *http.Request, sess *profile.Session) {
	if sess == nil {
		return
	}
	maxAge := int(time.Until(time.UnixMilli(sess.ExpiresAt)).Seconds())
	if maxAge < 60 {
		maxAge = 60
	}
	sameSite := http.SameSiteLaxMode
	secure := false
	if isHTTPSRequest(r) {
		sameSite = http.SameSiteNoneMode
		secure = true
	}
	for _, name := range profileSessionCookieNames {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    sess.Token,
			Path:     "/",
			MaxAge:   maxAge,
			HttpOnly: true,
			SameSite: sameSite,
			Secure:   secure,
		})
	}
}

func clearProfileSessionCookie(w http.ResponseWriter, r *http.Request) {
	sameSite := http.SameSiteLaxMode
	secure := false
	if isHTTPSRequest(r) {
		sameSite = http.SameSiteNoneMode
		secure = true
	}
	for _, name := range profileSessionCookieNames {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
			SameSite: sameSite,
			Secure:   secure,
		})
	}
}

// isHTTPSRequest detects HTTPS even behind a reverse proxy: r.TLS != nil
// means the Go server itself accepted the TLS connection; the
// X-Forwarded-Proto header is the standard signal from an upstream LB.
func isHTTPSRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return true
	}
	return false
}
