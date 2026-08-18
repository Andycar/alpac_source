package httpapi

// mirror.go — the EDGE ("mirror") side of mirror mode. See mirror_origin.go
// for the origin side and config.MirrorConfig for the rationale.
//
// A mirror serves catalog + /proxy video locally but delegates the auth
// control-plane to the origin ("beta"):
//
//   layer 1 (identity)  — mirrorAuthChecker resolves *User from the origin's
//                         /api/auth/validate, wired into the auth middleware
//                         via auth.WithTGStore. Populates whoami/premium/kit.
//
//   layer 2 (the gate)  — mirrorGateMiddleware replaces the local TG gate:
//                         it applies the IDENTICAL pre-auth allowlist, then
//                         asks the origin's /api/cluster/authgate for every
//                         decision and replays the verdict. The origin runs
//                         the REAL gate against its store, so device-binding,
//                         pending codes, bans and the bot all live there.
//
// Both layers cache positive results for CacheTTLSec and keep honoring a
// stale positive for GraceTTLSec when the origin is unreachable, so a brief
// origin outage doesn't log the whole fleet out.

import (
	"bytes"
	"encoding/base64"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/auth"
	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/kit"
	"lampac-go/internal/tgauth"

	"github.com/rs/zerolog/log"
)

var errMirrorNoHost = errors.New("mirror: api_host not configured")

// mirrorClientRef is the live mirror client when this instance runs as a mirror
// edge, else nil. Read by lite_events.go's group filtering so /lite/events
// visibility follows the origin's per-user group (delegated via validate).
var mirrorClientRef *mirrorClient

const mirrorCacheCap = 20000 // entries before a lazy sweep kicks in

// mirrorClient talks to the origin control-plane and caches its answers.
type mirrorClient struct {
	cfgFn func() config.MirrorConfig
	http  *http.Client

	valMu    sync.Mutex
	valCache map[string]mirrorValEntry

	gateMu    sync.Mutex
	gateCache map[string]mirrorGateEntry
}

type mirrorValEntry struct {
	resp mirrorValidateResp
	ok   bool
	at   time.Time
}

type mirrorGateEntry struct {
	at time.Time // positive (allow) verdicts only
}

func newMirrorClient(cfgFn func() config.MirrorConfig) *mirrorClient {
	return &mirrorClient{
		cfgFn:     cfgFn,
		http:      httpclient.New(8 * time.Second),
		valCache:  map[string]mirrorValEntry{},
		gateCache: map[string]mirrorGateEntry{},
	}
}

// ----------------------------- layer 1 ------------------------------------

// mirrorAuthChecker adapts mirrorClient to auth.TGTokenChecker +
// auth.TGDeviceUIDChecker so it drops into auth.WithTGStore unchanged.
type mirrorAuthChecker struct{ c *mirrorClient }

func (a mirrorAuthChecker) LookupAuth(token string) (int64, time.Time, bool) {
	resp, ok := a.c.validate("token", token)
	if !ok {
		return 0, time.Time{}, false
	}
	return resp.TGID, parseRFC3339(resp.Expires), true
}

func (a mirrorAuthChecker) LookupAuthByDeviceUID(uid string) (int64, time.Time, bool) {
	resp, ok := a.c.validate("uid", uid)
	if !ok {
		return 0, time.Time{}, false
	}
	return resp.TGID, parseRFC3339(resp.Expires), true
}

// validate resolves token/uid against the origin with cache + grace.
func (m *mirrorClient) validate(kind, val string) (mirrorValidateResp, bool) {
	if strings.TrimSpace(val) == "" {
		return mirrorValidateResp{}, false
	}
	cfg := m.cfgFn()
	key := kind[:1] + ":" + val
	now := time.Now()

	m.valMu.Lock()
	ent, has := m.valCache[key]
	m.valMu.Unlock()
	if has && now.Sub(ent.at) < dur(cfg.CacheTTLSec) {
		return ent.resp, ent.ok
	}

	resp, ok, err := m.fetchValidate(cfg, kind, val)
	if err != nil {
		// Origin down — honor a positive cached entry within grace.
		if has && ent.ok && now.Sub(ent.at) < dur(cfg.GraceTTLSec) {
			log.Warn().Err(err).Str("key", kind).Msg("mirror: origin unreachable, honoring cached identity within grace")
			return ent.resp, true
		}
		return mirrorValidateResp{}, false
	}

	m.valMu.Lock()
	if len(m.valCache) > mirrorCacheCap {
		m.sweepValLocked(now, dur(cfg.GraceTTLSec))
	}
	m.valCache[key] = mirrorValEntry{resp: resp, ok: ok, at: now}
	m.valMu.Unlock()
	return resp, ok
}

func (m *mirrorClient) fetchValidate(cfg config.MirrorConfig, kind, val string) (mirrorValidateResp, bool, error) {
	if cfg.APIHost == "" {
		return mirrorValidateResp{}, false, errMirrorNoHost
	}
	target := cfg.APIHost + "/api/auth/validate?" + kind + "=" + url.QueryEscape(val)
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return mirrorValidateResp{}, false, err
	}
	req.Header.Set("localrequest", cfg.APIPasswd)
	resp, err := m.http.Do(req)
	if err != nil {
		return mirrorValidateResp{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return mirrorValidateResp{}, false, fmt.Errorf("origin validate status %d", resp.StatusCode)
	}
	var out mirrorValidateResp
	if err := stdjson.NewDecoder(resp.Body).Decode(&out); err != nil {
		return mirrorValidateResp{}, false, err
	}
	return out, out.OK, nil
}

func (m *mirrorClient) sweepValLocked(now time.Time, ttl time.Duration) {
	for k, e := range m.valCache {
		if now.Sub(e.at) > ttl {
			delete(m.valCache, k)
		}
	}
}

// groupForRequest returns the effective group the origin reported for this
// request's token/uid (cached by validate), or nil if unknown. Used by
// lite_events.go to filter source visibility exactly like the origin would.
// By the time /lite/events filtering runs, the auth middleware has already
// called validate for this request, so the cache is warm.
func (m *mirrorClient) groupForRequest(r *http.Request) *tgauth.UserGroup {
	keys := make([]string, 0, 4)
	for _, tok := range collectLampacTokenCandidates(r) {
		keys = append(keys, "t:"+tok)
	}
	if uid := strings.TrimSpace(r.URL.Query().Get("uid")); uid != "" {
		keys = append(keys, "u:"+uid)
	}
	m.valMu.Lock()
	defer m.valMu.Unlock()
	for _, k := range keys {
		if ent, ok := m.valCache[k]; ok && ent.ok && ent.resp.GroupDef != nil {
			return ent.resp.GroupDef
		}
	}
	return nil
}

// kitVisibilityForRequest returns the user's personal balancer-visibility map
// the origin reported (cached by validate), or nil. Mirrors groupForRequest.
func (m *mirrorClient) kitVisibilityForRequest(r *http.Request) map[string]bool {
	keys := make([]string, 0, 4)
	for _, tok := range collectLampacTokenCandidates(r) {
		keys = append(keys, "t:"+tok)
	}
	if uid := strings.TrimSpace(r.URL.Query().Get("uid")); uid != "" {
		keys = append(keys, "u:"+uid)
	}
	m.valMu.Lock()
	defer m.valMu.Unlock()
	for _, k := range keys {
		if ent, ok := m.valCache[k]; ok && ent.ok && len(ent.resp.KitVisibility) > 0 {
			return ent.resp.KitVisibility
		}
	}
	return nil
}

// mirrorKitContextMiddleware injects the origin-delegated personal kit
// visibility into the request context (via kit.WithConfig) on /lite/ and
// /plab paths, so the existing kit.BalancerVisible/ExplicitlyHidden readers in
// eventPluginVisible apply the user's own hides exactly like the origin
// (Фаза 6). Runs after the auth middleware, so validate's cache is already
// warm for this request.
func mirrorKitContextMiddleware(c *mirrorClient) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := r.URL.Path
			if strings.HasPrefix(p, "/lite/") || strings.HasPrefix(p, "/plab") {
				if vis := c.kitVisibilityForRequest(r); len(vis) > 0 {
					raw, err := stdjson.Marshal(vis)
					if err == nil {
						ctx := kit.WithConfig(r.Context(), map[string]stdjson.RawMessage{"_balancerVisibility": raw})
						r = r.WithContext(ctx)
					}
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ----------------------------- layer 2 ------------------------------------

// mirrorGateMiddleware delegates the TG-auth gate decision to the origin.
func mirrorGateMiddleware(c *mirrorClient) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Identical pre-auth allowlist to the local gate.
			if gatePreAuthAllowed(r) {
				next.ServeHTTP(w, r)
				return
			}
			path := r.URL.Path

			// Tokenized plugin paths (/on/js/{token}, /…/h/{token}) — plugin
			// JS, not user data. The origin would need its store to validate
			// the path token; on a mirror we serve plugins (Фаза 3) and let
			// these through.
			if strings.Contains(path, "/js/") || strings.Contains(path, "/h/") {
				next.ServeHTTP(w, r)
				return
			}

			// Mode-aware allow: layer 1 may have resolved a non-TG user
			// (password/anon/profile) into context — mirror it like the local
			// gate does, so they aren't bounced into the TG pending flow.
			if u, ok := auth.UserFromContext(r.Context()); ok && u != nil && !strings.HasPrefix(u.ID, "tg:") {
				next.ServeHTTP(w, r)
				return
			}

			token := extractMirrorToken(r)
			uid := strings.TrimSpace(r.URL.Query().Get("uid"))

			// Fast path: a recent positive verdict for this token+uid.
			if token != "" && c.gateAllowed(token, uid, false) {
				next.ServeHTTP(w, r)
				return
			}

			verdict, err := c.authGate(r, token)
			if err != nil {
				// Origin down — honor a cached allow within grace, else soft-deny.
				if token != "" && c.gateAllowed(token, uid, true) {
					log.Warn().Err(err).Msg("mirror: origin authgate unreachable, honoring cached allow within grace")
					next.ServeHTTP(w, r)
					return
				}
				log.Warn().Err(err).Str("path", path).Msg("mirror: origin authgate unreachable")
				writeJSON(w, http.StatusOK, accsdbResponse("Сервис временно недоступен, повторите позже"))
				return
			}

			if verdict.Allow {
				if verdict.SetToken != "" {
					setAuthCookies(w, r, verdict.SetToken)
				}
				c.gateCachePut(firstNonEmpty(verdict.SetToken, token), uid)
				next.ServeHTTP(w, r)
				return
			}

			// Replay the origin's verdict verbatim: redirect, or the QR auth
			// card / ban JSON / device-limit message.
			if verdict.Location != "" {
				http.Redirect(w, r, verdict.Location, statusOr(verdict.Status, http.StatusFound))
				return
			}
			body, _ := base64.StdEncoding.DecodeString(verdict.BodyB64)
			if verdict.ContentType != "" {
				w.Header().Set("Content-Type", verdict.ContentType)
			}
			w.WriteHeader(statusOr(verdict.Status, http.StatusOK))
			_, _ = w.Write(body)
		})
	}
}

func (m *mirrorClient) authGate(r *http.Request, token string) (mirrorGateVerdict, error) {
	cfg := m.cfgFn()
	if cfg.APIHost == "" {
		return mirrorGateVerdict{}, errMirrorNoHost
	}
	reqBody := mirrorGateRequest{
		Path:           r.URL.Path,
		RawQuery:       r.URL.RawQuery,
		UserAgent:      r.UserAgent(),
		Accept:         r.Header.Get("Accept"),
		XRequestedWith: r.Header.Get("X-Requested-With"),
		ClientIP:       clientIP(r),
		Token:          token,
	}
	b, _ := stdjson.Marshal(reqBody)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, cfg.APIHost+"/api/cluster/authgate", bytes.NewReader(b))
	if err != nil {
		return mirrorGateVerdict{}, err
	}
	req.Header.Set("localrequest", cfg.APIPasswd)
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.http.Do(req)
	if err != nil {
		return mirrorGateVerdict{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return mirrorGateVerdict{}, fmt.Errorf("origin authgate status %d", resp.StatusCode)
	}
	var v mirrorGateVerdict
	if err := stdjson.NewDecoder(resp.Body).Decode(&v); err != nil {
		return mirrorGateVerdict{}, err
	}
	return v, nil
}

func mirrorGateKey(token, uid string) string { return token + "|" + uid }

// gateAllowed reports whether a cached positive verdict for token+uid is still
// honorable. grace=false uses CacheTTLSec (fast path); grace=true uses
// GraceTTLSec (origin-unreachable fallback).
func (m *mirrorClient) gateAllowed(token, uid string, grace bool) bool {
	cfg := m.cfgFn()
	window := dur(cfg.CacheTTLSec)
	if grace {
		window = dur(cfg.GraceTTLSec)
	}
	m.gateMu.Lock()
	defer m.gateMu.Unlock()
	ent, ok := m.gateCache[mirrorGateKey(token, uid)]
	return ok && time.Since(ent.at) < window
}

func (m *mirrorClient) gateCachePut(token, uid string) {
	if token == "" {
		return
	}
	now := time.Now()
	m.gateMu.Lock()
	defer m.gateMu.Unlock()
	if len(m.gateCache) > mirrorCacheCap {
		grace := dur(m.cfgFn().GraceTTLSec)
		for k, e := range m.gateCache {
			if now.Sub(e.at) > grace {
				delete(m.gateCache, k)
			}
		}
	}
	m.gateCache[mirrorGateKey(token, uid)] = mirrorGateEntry{at: now}
}

// --------------------- origin proxy (Фаза 2 + 4) --------------------------

// mirrorProxyMiddleware reverse-proxies two classes of request to the origin:
//
//	Фаза 2 — the auth control-plane (/tg/auth*, /tg/device*). The bot, pending
//	codes and token issuance live ONLY on the origin, so status polling, the
//	post-approval token handoff and device verification go there. The pending
//	code shown in the auth card was created on the origin during the authgate
//	replay; the client polls /tg/auth/status here, we forward it, and once the
//	bot approves the origin returns the token. The origin's Set-Cookie headers
//	pass back and the browser scopes them to THIS mirror's host (host-only),
//	so the token works locally afterwards.
//
//	Фаза 4 — per-user state (bookmarks, resume timecodes, the Lampa sync
//	storage, IPTV playlists) when CentralizeState is on. The origin is the
//	single source of truth so a user's profile follows them across mirrors.
//
// Everything else falls through to the local handlers / the delegating gate.
func mirrorProxyMiddleware(c *mirrorClient) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if mirrorShouldProxy(r.URL.Path, c.cfgFn().CentralizeStateEnabled()) {
				c.proxyToOrigin(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// mirrorShouldProxy decides whether a path is served by the origin rather than
// locally. Auth paths are always proxied; per-user state only when centralized.
func mirrorShouldProxy(path string, centralizeState bool) bool {
	// Auth control-plane — always.
	if strings.HasPrefix(path, "/tg/auth") || strings.HasPrefix(path, "/tg/device") {
		return true
	}
	if !centralizeState {
		return false
	}
	// Centralized per-user state. These are small JSON read/writes keyed by the
	// user's token (resolved on the origin); IPTV /play returns a stream URL
	// (not bytes) so video still flows mirror→client, not through the origin.
	return strings.HasPrefix(path, "/bookmark/") ||
		strings.HasPrefix(path, "/timecode/") ||
		strings.HasPrefix(path, "/storage/") ||
		strings.HasPrefix(path, "/api/iptv/") ||
		strings.HasPrefix(path, "/sisi/bookmark")
}

// proxyToOrigin forwards r to the origin verbatim (method, path, query, body,
// headers) with the real client IP + scheme/host so the origin keys pending
// codes / bans on the client and emits correctly-scoped cookies.
func (c *mirrorClient) proxyToOrigin(w http.ResponseWriter, r *http.Request) {
	cfg := c.cfgFn()
	if cfg.APIHost == "" {
		http.Error(w, "mirror: origin not configured", http.StatusBadGateway)
		return
	}
	target := cfg.APIHost + r.URL.Path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	copyProxyHeaders(req.Header, r.Header) // (dst, src)
	ip := clientIP(r)
	if ip != "" {
		if ex := strings.TrimSpace(req.Header.Get("X-Forwarded-For")); ex != "" {
			req.Header.Set("X-Forwarded-For", ex+", "+ip)
		} else {
			req.Header.Set("X-Forwarded-For", ip)
		}
		req.Header.Set("X-Real-IP", ip)
	}
	req.Header.Set("X-Forwarded-Host", r.Host)
	req.Header.Set("X-Forwarded-Proto", requestScheme(r))

	resp, err := c.http.Do(req)
	if err != nil {
		log.Warn().Err(err).Str("path", r.URL.Path).Msg("mirror: /tg/auth proxy to origin failed")
		http.Error(w, "origin unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	// Same hazard as the TorrServer proxy: the origin already answered with its
	// own Access-Control-* headers, and globalCORSMiddleware has set ours on
	// this ResponseWriter. Copying both yields a duplicate
	// Access-Control-Allow-Origin, which browsers reject on any cross-origin
	// call to a mirror.
	for k, vals := range resp.Header {
		if skipUpstreamHeader(k) {
			continue
		}
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// ----------------------------- helpers ------------------------------------

// extractMirrorToken mirrors auth.extractAuthToken + the gate's cookie order
// (_lampac_auth, lampac_token, alpac_token, headers, ?token=). Kept local so
// the auth package stays free of mirror concerns.
func extractMirrorToken(r *http.Request) string {
	for _, name := range []string{"_lampac_auth", "alpac_token", "lampac_token"} {
		if c, err := r.Cookie(name); err == nil {
			if v := strings.TrimSpace(c.Value); v != "" {
				return v
			}
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

func dur(sec int) time.Duration {
	if sec <= 0 {
		sec = 60
	}
	return time.Duration(sec) * time.Second
}

func statusOr(s, fallback int) int {
	if s == 0 {
		return fallback
	}
	return s
}

func parseRFC3339(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}
