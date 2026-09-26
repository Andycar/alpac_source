package auth

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// fileParseCache caches the parsed result of a JSON file on disk, keyed by
// file path. The cache invalidates when the file's mtime changes — that way
// hot-reload via the admin UI (which rewrites these files) still takes effect
// without a process restart.
//
// Without this cache, loadAccsdbConfig + loadUsers each did a fresh os.ReadFile
// + json.Unmarshal per HTTP request. Under heavy proxy traffic (MPV/vokino
// split mode hitting 10+ /proxy requests per second per client) the JSON
// decoder accumulated tens of GB of allocations in minutes and spent ~30%
// of CPU on map[string]any churn. See pprof 20260519-181652.
type fileParseCacheEntry struct {
	mtime  time.Time
	size   int64
	parsed any
}
type fileParseCache struct {
	mu    sync.RWMutex
	items map[string]fileParseCacheEntry
}

var (
	accsdbCache = &fileParseCache{items: map[string]fileParseCacheEntry{}}
	usersCache  = &fileParseCache{items: map[string]fileParseCacheEntry{}}
)

// loadParsedFile reads the file at path and JSON-unmarshals it into a fresh
// instance of T using parseFn. Returns the parsed value (cached) and ok=false
// if the file was missing or parsing failed. Cache is invalidated when the
// file's (size, mtime) tuple changes.
func loadParsedFile[T any](cache *fileParseCache, path string, parse func([]byte) (T, error)) (T, bool) {
	var zero T
	fi, err := os.Stat(path)
	if err != nil {
		return zero, false
	}
	mtime, size := fi.ModTime(), fi.Size()
	cache.mu.RLock()
	if e, ok := cache.items[path]; ok && e.mtime.Equal(mtime) && e.size == size {
		cache.mu.RUnlock()
		v, _ := e.parsed.(T)
		return v, true
	}
	cache.mu.RUnlock()
	data, err := os.ReadFile(path)
	if err != nil {
		return zero, false
	}
	parsed, err := parse(data)
	if err != nil {
		return zero, false
	}
	cache.mu.Lock()
	cache.items[path] = fileParseCacheEntry{mtime: mtime, size: size, parsed: parsed}
	cache.mu.Unlock()
	return parsed, true
}

// TGTokenChecker verifies a device token issued via Telegram auth.
// Returns (telegramID, expiresAt, found).
type TGTokenChecker interface {
	LookupAuth(token string) (telegramID int64, expiresAt time.Time, ok bool)
}

// TGDeviceUIDChecker resolves a TG user from a bound device UID. Used
// by the middleware as a fallback when the request carries `?uid=`
// but no cookie/?token= (cross-origin Lampa where Set-Cookie is
// dropped on XHR, see middleware.go CORS rationale). Same security
// model as tgAuthGateMiddleware's UID-auto-reauth path: a leaked UID
// grants access; mitigated by fingerprint check at the gate layer
// (which still writes Set-Cookie for the cookie-capable case).
type TGDeviceUIDChecker interface {
	LookupAuthByDeviceUID(uid string) (telegramID int64, expiresAt time.Time, ok bool)
}

// ProfileSessionChecker verifies a self-hosted profile session token (cookie
// `lampac_profile_session`). Returns the durable profile ID (without the
// `profile:` prefix), the cosmetic username for User.Aliases, and the
// session's absolute expiry. ok=false means the token is unknown or expired.
//
// Implementations live in internal/profile.Store.LookupSession — the
// adapter at internal/httpapi.profileChecker wraps it to the shape this
// interface needs so the auth package stays free of profile-store imports.
type ProfileSessionChecker interface {
	LookupProfileSession(token string) (profileID, username string, expiresAt time.Time, ok bool)
}

// PasswordTokenChecker verifies a session token issued via the password
// auth flow (alternative to TG). Returns the username (for cosmetic
// User.Aliases) and the stable UID stamped into User.ID.
type PasswordTokenChecker interface {
	LookupAuth(token string) (username, uid string, expiresAt time.Time, ok bool)
}

// AnonChecker is the read side of the anon issuer — used to resolve an
// existing anon cookie. Issuing is done via AnonIssuerFunc below so the
// caller controls when (and whether) to mint new cookies.
type AnonChecker interface {
	LookupAuth(token, ip string) (uid string, expiresAt time.Time, ok bool)
}

// AnonIssuerFunc mints a fresh anon cookie. Called by Handler when
// AuthMode() == "none" and no existing credential resolves. Caller
// is responsible for writing the cookie via SetAnonCookieFunc.
type AnonIssuerFunc func(ip, ua string) (token, uid string, expiresAt time.Time)

// SetAnonCookieFunc writes the freshly minted anon cookie to the response.
// Pulled out so the auth package stays free of cookie-encoding details
// (SameSite tuning, dual-namespace etc.) — httpapi owns that policy.
type SetAnonCookieFunc func(w http.ResponseWriter, r *http.Request, token string, expiresAt time.Time)

// AuthModeFunc returns the current auth mode ("tg", "password", "none").
// Read each request because the admin can flip it at runtime.
type AuthModeFunc func() string

// Middleware resolves users from various credential sources.
type Middleware struct {
	tgStore       TGTokenChecker
	tgUIDLookup   TGDeviceUIDChecker
	profileStore  ProfileSessionChecker
	pwStore       PasswordTokenChecker
	anonChecker   AnonChecker
	anonIssuer    AnonIssuerFunc
	setAnonCookie SetAnonCookieFunc
	authMode      AuthModeFunc
}

// Option configures the auth middleware.
type Option func(*Middleware)

type User struct {
	ID        string
	Aliases   []string
	Ban       bool
	Expires   time.Time
	Anonymous bool // true when sourced from the anon issuer (mode="none" flow)
}

type contextKey string

const userContextKey contextKey = "lampac.auth.user"

// WithTGStore attaches a Telegram token checker to the middleware.
// If the checker also satisfies TGDeviceUIDChecker, the middleware
// can resolve a user from `?uid=<bound device>` even when no cookie
// or ?token= is present — needed for cross-origin Lampa where XHR
// drops Set-Cookie. Optional; nil checker leaves UID-fallback off.
func WithTGStore(checker TGTokenChecker) Option {
	return func(m *Middleware) {
		m.tgStore = checker
		if uid, ok := checker.(TGDeviceUIDChecker); ok {
			m.tgUIDLookup = uid
		}
	}
}

// WithProfileStore attaches a self-hosted profile session checker. When
// supplied, the middleware reads `lampac_profile_session` from cookies and
// produces User{ID: "profile:<uuid>"} for valid sessions.
func WithProfileStore(checker ProfileSessionChecker) Option {
	return func(m *Middleware) { m.profileStore = checker }
}

// WithPasswordStore attaches a password-session checker. Reads from the
// same cookie/header sources as the TG token; resolution order in the
// middleware is TG first, password second — so users who happen to have
// both an active TG account and a password account stay bound to TG.
func WithPasswordStore(checker PasswordTokenChecker) Option {
	return func(m *Middleware) { m.pwStore = checker }
}

// WithAnonAuth wires the anon-mode plumbing: the read-side checker, the
// mint-on-demand issuer, the cookie writer, and the mode oracle. All four
// must be supplied for the open-access flow to take effect; missing any
// of them disables anon issuing (existing anon cookies still resolve).
func WithAnonAuth(checker AnonChecker, issuer AnonIssuerFunc, setCookie SetAnonCookieFunc, mode AuthModeFunc) Option {
	return func(m *Middleware) {
		m.anonChecker = checker
		m.anonIssuer = issuer
		m.setAnonCookie = setCookie
		m.authMode = mode
	}
}

// WithAuthMode attaches a mode oracle without enabling anon issuing.
// Useful when callers want resolveUser to see "the current mode is
// none/password/tg" for non-anon decisions (e.g. an extra gate that
// rejects password tokens when mode is forced back to "tg").
func WithAuthMode(mode AuthModeFunc) Option {
	return func(m *Middleware) { m.authMode = mode }
}

// New creates an auth middleware with optional configuration.
func New(opts ...Option) *Middleware {
	m := &Middleware{}
	for _, opt := range opts {
		if opt != nil {
			opt(m)
		}
	}
	return m
}

func (m *Middleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := m.resolveUser(r)
		if user == nil && m.shouldIssueAnon(r) {
			user = m.issueAnon(w, r)
		}
		if user != nil {
			r = r.WithContext(WithUser(r.Context(), user))
		}
		next.ServeHTTP(w, r)
	})
}

// shouldIssueAnon gates mint-on-demand: only when (a) mode is "none",
// (b) the issuer chain is wired, and (c) the request looks like a
// top-level browser navigation. We don't want to mint a fresh anon
// cookie on every XHR/API call — that would spam anon_users.json with
// thousands of entries per browser session.
func (m *Middleware) shouldIssueAnon(r *http.Request) bool {
	if m.anonIssuer == nil || m.setAnonCookie == nil || m.authMode == nil {
		return false
	}
	if m.authMode() != "none" {
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	// HTML navigation requests carry "text/html" in Accept. XHR/JSON/
	// stream/image requests do not.
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

func (m *Middleware) issueAnon(w http.ResponseWriter, r *http.Request) *User {
	ip := clientIP(r)
	ua := r.Header.Get("User-Agent")
	token, uid, exp := m.anonIssuer(ip, ua)
	m.setAnonCookie(w, r, token, exp)
	return &User{
		ID:        "anon:" + uid,
		Expires:   exp,
		Anonymous: true,
	}
}

// clientIP extracts the originating IP from forward headers. Trust
// model is loose — only used for audit fields (LastIP); the gate
// uses internal/httpapi.clientIP with the trusted_proxies allowlist.
func clientIP(r *http.Request) string {
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		if i := strings.Index(v, ","); i > 0 {
			return strings.TrimSpace(v[:i])
		}
		return strings.TrimSpace(v)
	}
	if v := r.Header.Get("X-Real-IP"); v != "" {
		return strings.TrimSpace(v)
	}
	if i := strings.LastIndex(r.RemoteAddr, ":"); i > 0 {
		return r.RemoteAddr[:i]
	}
	return r.RemoteAddr
}

func WithUser(ctx context.Context, user *User) context.Context {
	if user == nil {
		return ctx
	}
	return context.WithValue(ctx, userContextKey, user)
}

func UserFromContext(ctx context.Context) (*User, bool) {
	if ctx == nil {
		return nil, false
	}
	user, ok := ctx.Value(userContextKey).(*User)
	if !ok || user == nil {
		return nil, false
	}
	return user, true
}

type accsdbConfig struct {
	Enable   bool
	Accounts map[string]string
}

// extractAuthToken pulls the auth token out of the request. Dual-namespace
// (alpac_/lampac_) cookies, optional header overrides, last-ditch query
// param. Documented in the resolveUser comment block above.
func extractAuthToken(r *http.Request) string {
	// `_lampac_auth` — HttpOnly-якорь, его ставит сервер и не может переписать
	// ни один плагин со страницы. Читаем ПЕРВЫМ: на телевизорах (Tizen, Android
	// WebView) при перезапуске приложения вычищается именно JS-видимая банка
	// кук, а HttpOnly переживает — и до 20.09.2026 в этом состоянии человек
	// выглядел авторизованным для гейта (тот читал этот кук сам) и анонимом для
	// всех, кто пользовался общим разбором.
	if c, err := r.Cookie("_lampac_auth"); err == nil {
		if v := strings.TrimSpace(c.Value); v != "" {
			return v
		}
	}
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

// ExtractToken — тот же разбор источников токена, что использует middleware.
// Вынесен наружу, чтобы гейт не держал СВОЙ список: он знал только куки
// `_lampac_auth`, `lampac_token` и `?token=`, и запрос, принёсший токен
// заголовком `X-Alpac-Token`/`X-Lampac-Token` (а заголовок заведён ровно для
// Android WebView, который не переигрывает куки в XHR), для middleware был
// авторизован, а для гейта — нет: он уходил в ветку `uid` и получал QR-код.
func ExtractToken(r *http.Request) string { return extractAuthToken(r) }

func (m *Middleware) resolveUser(r *http.Request) *User {
	cfg := loadAccsdbConfig()
	if cfg.Enable {
		candidates := collectCandidates(r)

		// First pass: accounts from init.conf.
		for _, c := range candidates {
			if expires, ok := cfg.Accounts[c]; ok {
				exp := parseDateTime(expires)
				if exp.IsZero() || time.Now().UTC().Before(exp) {
					return &User{
						ID:      c,
						Ban:     false,
						Expires: exp,
					}
				}
			}
		}

		// Second pass: users.json.
		users := loadUsers()
		for _, c := range candidates {
			for _, u := range users {
				if userMatches(u, c) {
					return u
				}
			}
		}
	}

	// Telegram auth — five sources, scanned in this order:
	//
	//   1. Cookie `alpac_token`      — our brand-scoped cookie. Prefer it
	//      over `lampac_token` so a third-party plugin that overwrites
	//      `lampac_token` via document.cookie (some forks accidentally
	//      share the name) doesn't kick the user out. Set in parallel
	//      with lampac_token by setAuthCookies.
	//   2. Cookie `lampac_token`     — legacy/external name, what the
	//      original lampac-net ecosystem uses. Kept for backward compat
	//      and for browsers that arrived with this cookie already set.
	//   3. Header `X-Alpac-Token`    — same namespace fork applied to
	//      the header fallback path (see #4 for why this header exists).
	//   4. Header `X-Lampac-Token`   — explicit fallback for clients whose
	//      runtime won't send the cookie back. Android WebView inside the
	//      Lampa player is the canonical case: navigation responses set
	//      the cookie fine, but `withCredentials` XHR requests don't
	//      always replay it. Plugins read the token from Lampa.Storage
	//      (or document.cookie when accessible) and forward it here.
	//   5. Query  `?token=`          — last-ditch fallback used by older
	//      Lampa plugins that get the token baked into their script URL
	//      via the templated `/<plugin>/js/<token>` route. We trust
	//      LookupAuth to drop anything that doesn't match an issued row.
	// Token sources are shared between TG, password, and anon checkers —
	// each writes the same cookie pair (alpac_token + lampac_token) so a
	// single read covers all of them.
	token := extractAuthToken(r)

	if token != "" && m.tgStore != nil {
		if tgID, exp, ok := m.tgStore.LookupAuth(token); ok {
			return &User{
				ID:      fmt.Sprintf("tg:%d", tgID),
				Expires: exp,
			}
		}
	}

	// Password store — fallback after TG. Same token; different backing
	// table. Reverse priority (password before TG) would force the auth
	// gate to know which mode is active before reading a cookie; cheaper
	// to just check both and let whichever resolves win.
	if token != "" && m.pwStore != nil {
		if username, uid, exp, ok := m.pwStore.LookupAuth(token); ok {
			user := &User{
				ID:      "pw:" + uid,
				Expires: exp,
			}
			if username != "" {
				user.Aliases = []string{username}
			}
			return user
		}
	}

	// Anon — only read side here. Mint-on-demand is handled in Handler
	// because resolveUser doesn't have access to the response writer.
	if token != "" && m.anonChecker != nil {
		if uid, exp, ok := m.anonChecker.LookupAuth(token, clientIP(r)); ok {
			return &User{
				ID:        "anon:" + uid,
				Expires:   exp,
				Anonymous: true,
			}
		}
	}

	// UID-based fallback for TG auth — XHRs from cross-origin Lampa
	// builds (lampa.mx player on a different host) often arrive with
	// neither cookie nor ?token=, because the browser refuses to send
	// our Set-Cookie on the cross-site XHR. The Lampa online.js plugin
	// always stamps ?uid=<lampac_unic_id> though, and if that UID is
	// already bound to a token in the TG store the request belongs to
	// that user. Without this branch /lite/events sees nil and returns
	// the synthetic "auth required" banner even though the user signed
	// in successfully via the TG bot a minute ago.
	if m.tgUIDLookup != nil {
		if uid := strings.TrimSpace(r.URL.Query().Get("uid")); uid != "" {
			if tgID, exp, ok := m.tgUIDLookup.LookupAuthByDeviceUID(uid); ok {
				return &User{
					ID:      fmt.Sprintf("tg:%d", tgID),
					Expires: exp,
				}
			}
		}
	}

	// Self-hosted profile session — checked AFTER TG so that users who have
	// both bind their data to their TG identity (the primary one). The
	// profile session is the fallback when no TG cookie is present (e.g.
	// TV box without TG bot access, or a deployment that doesn't expose
	// TG auth at all).
	//
	// Token sources, in order:
	//   1. Cookie `lampac_profile_session`   — set by issueProfileSessionCookie.
	//   2. Header  `X-Lampac-Profile-Session` — explicit fallback for clients
	//      whose runtime drops Set-Cookie from XHR responses. Android WebView
	//      inside the Lampa player is the canonical case: it persists cookies
	//      from navigation responses (so `lampac_token` from the TG OAuth
	//      redirect survives) but silently discards Set-Cookie from
	//      `XMLHttpRequest` responses unless `CookieManager.setAcceptThirdPartyCookies`
	//      is set, which Lampa doesn't toggle. Without this header path the
	//      profile session cookie set by /api/profile/login was lost between
	//      calls and /api/profile/me would report `authenticated:false` right
	//      after a successful login (see syncpro.js doLogin → refreshProfileState).
	if m.profileStore != nil {
		// Same dual-namespace dance as the TG token above: alpac_* is the
		// brand-isolated cookie, lampac_* is the legacy fallback. Header
		// path mirrors cookie path so clients on Android WebView (where
		// cross-origin XHRs sometimes drop cookies) can resend the token.
		var token string
		if c, err := r.Cookie("alpac_profile_session"); err == nil {
			token = strings.TrimSpace(c.Value)
		}
		if token == "" {
			if c, err := r.Cookie("lampac_profile_session"); err == nil {
				token = strings.TrimSpace(c.Value)
			}
		}
		if token == "" {
			token = strings.TrimSpace(r.Header.Get("X-Alpac-Profile-Session"))
		}
		if token == "" {
			token = strings.TrimSpace(r.Header.Get("X-Lampac-Profile-Session"))
		}
		if token != "" {
			if pid, uname, exp, ok := m.profileStore.LookupProfileSession(token); ok {
				user := &User{
					ID:      "profile:" + pid,
					Expires: exp,
				}
				if uname != "" {
					user.Aliases = []string{uname}
				}
				return user
			}
		}
	}

	return nil
}

func collectCandidates(r *http.Request) []string {
	out := make([]string, 0, 8)
	q := r.URL.Query()

	push := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		for _, ex := range out {
			if strings.EqualFold(ex, v) {
				return
			}
		}
		out = append(out, v)
	}

	push(q.Get("account_email"))
	push(q.Get("uid"))
	push(q.Get("token"))
	push(q.Get("auth_token"))
	push(r.Header.Get("X-User-Uid"))
	push(r.Header.Get("X-Account-Email"))
	if c, err := r.Cookie("uid"); err == nil {
		push(c.Value)
	}
	if c, err := r.Cookie("account_email"); err == nil {
		push(c.Value)
	}

	return out
}

func loadAccsdbConfig() accsdbConfig {
	path := relToRuntime("init.conf")
	cfg, ok := loadParsedFile(accsdbCache, path, parseAccsdbConfig)
	if !ok {
		return accsdbConfig{Accounts: map[string]string{}}
	}
	return cfg
}

// parseAccsdbConfig extracts the [accsdb] section from init.conf JSON.
// Stored in the file cache so we don't re-decode 5MB of init.conf on every
// HTTP request.
func parseAccsdbConfig(data []byte) (accsdbConfig, error) {
	out := accsdbConfig{Accounts: map[string]string{}}
	var root map[string]any
	if err := stdjson.Unmarshal(data, &root); err != nil {
		return out, err
	}
	node, ok := root["accsdb"].(map[string]any)
	if !ok {
		return out, nil
	}
	if enabled, ok := node["enable"].(bool); ok {
		out.Enable = enabled
	}
	if accounts, ok := node["accounts"].(map[string]any); ok {
		for k, v := range accounts {
			key := strings.TrimSpace(k)
			if key == "" {
				continue
			}
			out.Accounts[key] = strings.TrimSpace(toString(v))
		}
	}
	return out, nil
}

func loadUsers() []*User {
	path := relToRuntime("users.json")
	users, ok := loadParsedFile(usersCache, path, parseUsersJSON)
	if !ok {
		return nil
	}
	return users
}

// parseUsersJSON is the cached parser for users.json. Same rationale as
// parseAccsdbConfig — was called on every request before caching.
func parseUsersJSON(data []byte) ([]*User, error) {
	var raw []map[string]any
	if err := stdjson.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	out := make([]*User, 0, len(raw))
	for _, obj := range raw {
		id := strings.TrimSpace(toString(obj["id"]))
		if id == "" {
			continue
		}
		aliases := make([]string, 0, 2)
		if ids, ok := obj["ids"].([]any); ok {
			for _, one := range ids {
				alias := strings.TrimSpace(toString(one))
				if alias != "" {
					aliases = append(aliases, alias)
				}
			}
		}
		user := &User{
			ID:      id,
			Aliases: aliases,
			Ban:     toBool(obj["ban"]),
			Expires: parseDateTime(toString(obj["expires"])),
		}
		if user.Expires.IsZero() {
			// legacy often treats zero/invalid as unrestricted
			user.Expires = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
		}
		out = append(out, user)
	}
	return out, nil
}

func userMatches(u *User, candidate string) bool {
	if u == nil || strings.TrimSpace(candidate) == "" {
		return false
	}
	if !strings.EqualFold(u.ID, candidate) {
		matched := false
		for _, alias := range u.Aliases {
			if strings.EqualFold(alias, candidate) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if u.Ban {
		return false
	}
	if !u.Expires.IsZero() && time.Now().UTC().After(u.Expires) {
		return false
	}
	return true
}

func parseDateTime(v string) time.Time {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}
	}
	layouts := []string{
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if tm, err := time.Parse(layout, v); err == nil {
			return tm.UTC()
		}
	}
	return time.Time{}
}

func readFileAny(rel string) ([]byte, bool) {
	path := relToRuntime(rel)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return data, true
}

func relToRuntime(rel string) string {
	if root := strings.TrimSpace(os.Getenv("LAMPAC_GO_HOME")); root != "" {
		return filepath.Join(root, rel)
	}
	return rel
}

func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strings.TrimSpace(strconv.FormatFloat(t, 'f', -1, 64))
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		if v == nil {
			return ""
		}
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func toBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		t = strings.TrimSpace(strings.ToLower(t))
		return t == "1" || t == "true" || t == "yes" || t == "on"
	default:
		return strings.TrimSpace(toString(v)) == "1"
	}
}
