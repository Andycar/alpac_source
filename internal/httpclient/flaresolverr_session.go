package httpclient

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"
)

// FlareSolverr is slow (5-15s per call) and Chrome instances are expensive.
// Once it solves a Cloudflare/DDoS-Guard challenge, the resulting cookies
// (`cf_clearance`, `__ddg1_`, `__ddg2_`, etc.) are valid for 30+ minutes —
// during that window we can fetch with a regular HTTP client using those
// cookies and skip FlareSolverr entirely. The session cache here implements
// that pattern.

// FlareSolverrSession is a solved-and-cached browser session for a host.
type FlareSolverrSession struct {
	Cookies   []FlareSolverrCookie
	UserAgent string
	SolvedAt  time.Time
	ExpiresAt time.Time
}

// fsSessionStore caches sessions keyed by lowercase host. Separate from the
// balancer concept so multiple balancers hitting the same host share a session.
var fsSessionStore = struct {
	sync.RWMutex
	m map[string]*FlareSolverrSession
}{m: map[string]*FlareSolverrSession{}}

// flareSessionTTL: how long we trust a solved session before re-solving.
// Conservative — cf_clearance commonly lasts 30-60 min but TSPU/DDoS-Guard
// can rotate sooner; 25 min is a sweet spot.
const flareSessionTTL = 25 * time.Minute

// hostKey extracts the lowercase host from a URL string. Trailing port and
// scheme are dropped so https://foo.bar:443/... and http://foo.bar/... share.
func hostKey(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return strings.ToLower(rawURL)
	}
	h := u.Host
	if i := strings.IndexByte(h, ':'); i >= 0 {
		h = h[:i]
	}
	return strings.ToLower(h)
}

// GetFlareSolverrSession returns a cached session for the host extracted from
// rawURL, or nil if no fresh session exists.
func GetFlareSolverrSession(rawURL string) *FlareSolverrSession {
	key := hostKey(rawURL)
	fsSessionStore.RLock()
	s := fsSessionStore.m[key]
	fsSessionStore.RUnlock()
	if s == nil {
		return nil
	}
	if time.Now().After(s.ExpiresAt) {
		// Stale — drop it lazily.
		fsSessionStore.Lock()
		if cur := fsSessionStore.m[key]; cur == s {
			delete(fsSessionStore.m, key)
		}
		fsSessionStore.Unlock()
		return nil
	}
	return s
}

// PutFlareSolverrSession stores or refreshes a session for the host derived
// from rawURL.
func PutFlareSolverrSession(rawURL string, sess *FlareSolverrSession) {
	if sess == nil {
		return
	}
	if sess.ExpiresAt.IsZero() {
		sess.ExpiresAt = time.Now().Add(flareSessionTTL)
	}
	if sess.SolvedAt.IsZero() {
		sess.SolvedAt = time.Now()
	}
	key := hostKey(rawURL)
	fsSessionStore.Lock()
	fsSessionStore.m[key] = sess
	fsSessionStore.Unlock()
}

// InvalidateFlareSolverrSession drops the session for the host so the next
// call goes through FlareSolverr again. Call when a 403/503 indicates the
// CF clearance has expired.
func InvalidateFlareSolverrSession(rawURL string) {
	key := hostKey(rawURL)
	fsSessionStore.Lock()
	delete(fsSessionStore.m, key)
	fsSessionStore.Unlock()
}

// EnsureFlareSolverrSession returns a usable session, solving via FlareSolverr
// only when no fresh cache entry exists. balancer is used both for the opt-in
// check and to look up the SOCKS5 proxy. Returns (session, ok=false) when
// FlareSolverr is unavailable or the solve fails.
func EnsureFlareSolverrSession(ctx context.Context, balancer, rawURL string, opts FlareSolverrFetchOptions) (*FlareSolverrSession, bool) {
	if s := GetFlareSolverrSession(rawURL); s != nil {
		return s, true
	}
	res, ok := FlareSolverrFetch(ctx, balancer, rawURL, opts)
	if !ok {
		return nil, false
	}
	sess := &FlareSolverrSession{
		Cookies:   res.Solution.Cookies,
		UserAgent: res.Solution.UserAgent,
		SolvedAt:  time.Now(),
		ExpiresAt: time.Now().Add(flareSessionTTL),
	}
	PutFlareSolverrSession(rawURL, sess)
	return sess, true
}

// AttachSessionToJar populates a cookiejar with the session's cookies for the
// given URL. Returns an http.Client wired with that jar so subsequent calls
// from this client carry the CF clearance.
func AttachSessionToJar(sess *FlareSolverrSession, rawURL string, jar *cookiejar.Jar) {
	if sess == nil || jar == nil {
		return
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return
	}
	cookies := make([]*http.Cookie, 0, len(sess.Cookies))
	for _, c := range sess.Cookies {
		if c.Name == "" {
			continue
		}
		cookies = append(cookies, &http.Cookie{Name: c.Name, Value: c.Value, Domain: u.Host, Path: "/"})
	}
	jar.SetCookies(u, cookies)
}

// FlareSolverrSessionClient is the high-level helper most callers want. It:
//  1. Ensures a solved session exists (cache hit or fresh solve).
//  2. Builds an http.Client with a fresh cookiejar carrying the session.
//  3. Sets the session's UserAgent on the returned client (via a transport
//     wrapper) so server-side fingerprinting matches what Chrome used.
//
// Returns ok=false when FlareSolverr is unavailable; in that case the caller
// should fall back to a direct uTLS/SOCKS5 client.
func FlareSolverrSessionClient(ctx context.Context, balancer, rawURL string, base *http.Client) (*http.Client, *FlareSolverrSession, bool) {
	sess, ok := EnsureFlareSolverrSession(ctx, balancer, rawURL, FlareSolverrFetchOptions{Force: true})
	if !ok {
		return nil, nil, false
	}
	jar, _ := cookiejar.New(nil)
	AttachSessionToJar(sess, rawURL, jar)

	src := base
	if src == nil {
		// Default: uTLS Chrome through the balancer's SOCKS5 (or direct uTLS
		// if no proxy registered). Mirrors the Chrome inside FlareSolverr.
		src = NewUTLSForBalancer(balancer, 30*time.Second)
	}
	clone := *src
	clone.Jar = jar
	if sess.UserAgent != "" {
		clone.Transport = uaInjectingTransport{ua: sess.UserAgent, base: src.Transport}
	}
	return &clone, sess, true
}

// uaInjectingTransport sets the User-Agent header (when not already set by the
// caller) so requests with the FS-derived jar look identical to Chrome.
type uaInjectingTransport struct {
	ua   string
	base http.RoundTripper
}

func (t uaInjectingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", t.ua)
	}
	rt := t.base
	if rt == nil {
		rt = http.DefaultTransport
	}
	return rt.RoundTrip(req)
}

// SnapshotFlareSolverrSessions returns a serialisable view of the session
// cache for the admin UI. Each entry is {host, ua, solved_at_unix, expires_at_unix}.
func SnapshotFlareSolverrSessions() []map[string]any {
	fsSessionStore.RLock()
	defer fsSessionStore.RUnlock()
	out := make([]map[string]any, 0, len(fsSessionStore.m))
	for host, s := range fsSessionStore.m {
		if s == nil {
			continue
		}
		out = append(out, map[string]any{
			"host":            host,
			"user_agent":      s.UserAgent,
			"solved_at_unix":  s.SolvedAt.Unix(),
			"expires_at_unix": s.ExpiresAt.Unix(),
		})
	}
	return out
}
