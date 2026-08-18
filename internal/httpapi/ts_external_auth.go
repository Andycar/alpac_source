package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"lampac-go/internal/config"

	"github.com/rs/zerolog/log"
)

// contextWithExternalAuth marks the context as carrying an authenticated
// external (Basic-Auth) caller.
func contextWithExternalAuth(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKeyExternalAuth{}, true)
}

// ctxKeyExternalAuth is set on the request context when an external client
// (Basic-Auth, non-cookie) was accepted by tsExternalAuthMiddleware.
type ctxKeyExternalAuth struct{}

// tsExternalAuthState holds the live config + a small in-memory rate limiter
// shared across all external-access middleware invocations.  Updated by
// reloadTSExternalAuth on hot reload.
type tsExternalAuthState struct {
	cfg config.TorrServerExternalAccess

	// authFails tracks failed Basic-Auth attempts per remote IP.  The map is
	// pruned lazily — see attemptFailed/withinThrottle.
	mu        sync.Mutex
	authFails map[string]*tsAuthCounter
}

type tsAuthCounter struct {
	count int
	until time.Time
}

// tsExternalAuthPtr is hot-reloadable like tsProxyPtr.
var tsExternalAuthPtr atomic.Pointer[tsExternalAuthState]

// Brute-force tuning.  Five misses in a minute earns the client a 60 s
// cool-down where every request gets 401 without even checking creds.
const (
	tsAuthMaxAttempts = 5
	tsAuthWindow      = time.Minute
	tsAuthThrottle    = time.Minute
)

// reloadTSExternalAuth swaps in a fresh state from cfg.TorrServer.ExternalAccess.
// Called from server bootstrap and the admin-reload path.
func reloadTSExternalAuth(cfg config.Config) {
	ext := cfg.TorrServer.ExternalAccess
	state := &tsExternalAuthState{
		cfg:       ext,
		authFails: make(map[string]*tsAuthCounter),
	}
	tsExternalAuthPtr.Store(state)

	if ext.Enable {
		log.Info().
			Bool("listen_extra", ext.ListenAddr != "").
			Int("allowlist_cidrs", len(ext.AllowFrom)).
			Msg("ts-external: Basic Auth enabled")
	}
}

// tsExternalAuthEnabled returns the live state ptr or nil when off.
func tsExternalAuthState_load() *tsExternalAuthState {
	return tsExternalAuthPtr.Load()
}

// clientIP for /ts/* comes from the package-level helper in diag_endpoints.go
// which already honours trusted_proxies — same behaviour we want here.
//
// ipMatchesCIDRList returns true if any CIDR matches.  Empty list = open.
// Bare-IP entries (no /mask) match exactly.
func ipMatchesCIDRList(ip string, list []string) bool {
	if len(list) == 0 {
		return true
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, entry := range list {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if !strings.Contains(entry, "/") {
			// Bare IP — compare directly.
			if pe := net.ParseIP(entry); pe != nil && pe.Equal(parsed) {
				return true
			}
			continue
		}
		_, ipnet, err := net.ParseCIDR(entry)
		if err != nil {
			continue
		}
		if ipnet.Contains(parsed) {
			return true
		}
	}
	return false
}

// withinThrottle returns true when this IP is currently locked out.
// Side-effect: prunes long-expired throttle counters opportunistically.
// A counter with a zero `until` is "accumulating fails" — never prune those
// here, attemptFailed must keep counting them.
func (s *tsExternalAuthState) withinThrottle(ip string) bool {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.authFails[ip]
	if !ok {
		return false
	}
	if c.until.IsZero() {
		// Still accumulating — not throttled yet.
		return false
	}
	if c.until.Before(now) {
		// Throttle window already lapsed — drop the counter so the next
		// fail starts a fresh accumulation cycle.
		delete(s.authFails, ip)
		return false
	}
	return true
}

// attemptFailed records a failed Basic-Auth attempt and triggers throttle
// after tsAuthMaxAttempts misses inside tsAuthWindow.
func (s *tsExternalAuthState) attemptFailed(ip string) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.authFails[ip]
	// Reset only when the existing counter both has a previous lockout AND
	// that lockout (plus window) is already in the past.  A counter whose
	// until is still zero-value is "fresh, accumulating" — keep counting.
	if !ok || (!c.until.IsZero() && c.until.Before(now.Add(-tsAuthWindow))) {
		c = &tsAuthCounter{}
		s.authFails[ip] = c
	}
	c.count++
	if c.count >= tsAuthMaxAttempts {
		c.until = now.Add(tsAuthThrottle)
		log.Warn().Str("ip", ip).Int("attempts", c.count).
			Msg("ts-external: brute-force throttle activated")
		c.count = 0 // reset for next window after throttle expires
	}
}

// attemptOK clears the failure counter on a successful login.
func (s *tsExternalAuthState) attemptOK(ip string) {
	s.mu.Lock()
	delete(s.authFails, ip)
	s.mu.Unlock()
}

// checkBasic returns true when the Authorization header parses to a valid
// login:password pair against the configured credentials.  Constant-time
// compare prevents timing oracle.
func (s *tsExternalAuthState) checkBasic(r *http.Request) bool {
	if s.cfg.Login == "" || s.cfg.Password == "" {
		return false
	}
	hdr := r.Header.Get("Authorization")
	if hdr == "" {
		return false
	}
	const prefix = "Basic "
	if !strings.HasPrefix(hdr, prefix) {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(hdr[len(prefix):]))
	if err != nil {
		return false
	}
	idx := strings.IndexByte(string(raw), ':')
	if idx < 0 {
		return false
	}
	user := string(raw[:idx])
	pass := string(raw[idx+1:])
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(s.cfg.Login)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(s.cfg.Password)) == 1
	return userOK && passOK
}

// tsExternalAuthMiddleware wraps a handler so Basic-Auth-bearing requests
// from external clients are recognised and tagged.  The downstream
// require-token middleware checks the context flag and skips the cookie
// check when the request was already authorised here.
//
// Behaviour:
//   - external_access.Enable=false       → middleware is transparent
//   - IP not in AllowFrom (when set)     → 403 (no auth challenge leaked)
//   - too many recent fails              → 401 with empty challenge (throttle)
//   - Authorization header valid         → ctxKeyExternalAuth set, next.ServeHTTP
//   - Authorization header invalid/missing → next.ServeHTTP (cookie/token path may still let it in)
func tsExternalAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state := tsExternalAuthPtr.Load()
		if state == nil || !state.cfg.Enable {
			next.ServeHTTP(w, r)
			return
		}

		ip := clientIP(r)
		if !ipMatchesCIDRList(ip, state.cfg.AllowFrom) {
			http.Error(w, "external access not permitted from this network", http.StatusForbidden)
			return
		}

		if state.withinThrottle(ip) {
			w.Header().Set("WWW-Authenticate", `Basic realm="TorrServer"`)
			http.Error(w, "too many auth failures, try again later", http.StatusUnauthorized)
			return
		}

		// If an Authorization header is present, validate it.  Either pass
		// (mark ctx) or fail (count + 401 challenge).  No header → fall
		// through; cookie auth might still authorise.
		if r.Header.Get("Authorization") != "" {
			if state.checkBasic(r) {
				state.attemptOK(ip)
				r = r.WithContext(contextWithExternalAuth(r.Context()))
			} else {
				state.attemptFailed(ip)
				w.Header().Set("WWW-Authenticate", `Basic realm="TorrServer"`)
				http.Error(w, "invalid credentials", http.StatusUnauthorized)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

// hasExternalAuth reports whether the request was already authorised via
// Basic-Auth higher up the chain.
func hasExternalAuth(r *http.Request) bool {
	v := r.Context().Value(ctxKeyExternalAuth{})
	return v != nil
}
