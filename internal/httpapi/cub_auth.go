package httpapi

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/tgauth"

	"github.com/rs/zerolog/log"
)

// cubUserInfo is the subset of the CUB users/get response we care about.
type cubUserInfo struct {
	ID    string
	Email string
}

type cubValCacheEntry struct {
	info      cubUserInfo
	err       error
	expiresAt time.Time
}

// cubAuthValidator verifies CUB account tokens against the upstream CUB API
// (GET /api/users/get with the token header — the same call Lampa itself makes)
// and resolves them to a stable CUB user id. That id is the durable identity
// anchor for device binding: a CUB account is server-side state that survives
// any client wipe.
//
// Results are cached (positive 10m / negative 2m) because the auth gate polls
// /tg/auth/status and the /cub/ proxy sees the same token on every CUB call —
// without the cache each poll would turn into an upstream request.
type cubAuthValidator struct {
	client *http.Client
	scheme string
	domain string
	mirror string

	mu       sync.Mutex
	cache    map[string]cubValCacheEntry
	inflight map[string]bool // learn dedupe: ourToken+cubToken pairs being processed
}

const (
	cubValPositiveTTL = 10 * time.Minute
	cubValNegativeTTL = 2 * time.Minute
	cubTokenMaxLen    = 512
)

var errCubTokenInvalid = errors.New("cub token rejected by upstream")

func newCubAuthValidator(cfg config.Config) *cubAuthValidator {
	scheme := strings.ToLower(strings.TrimSpace(cfg.Cub.Scheme))
	if scheme == "" {
		scheme = "https"
	}
	domain := strings.TrimSpace(cfg.Cub.Domain)
	if domain == "" {
		domain = "cub.red"
	}

	// Same transport policy as the /cub/ proxy: tolerate mirror CUBs on
	// self-signed certs unless the admin opted into strict TLS.
	tlsCfg := &tls.Config{InsecureSkipVerify: !cfg.Security.StrictAdminTLS}
	var transport http.RoundTripper
	if t := httpclient.TransportForBalancer("cub"); t != nil {
		t.TLSClientConfig = tlsCfg
		transport = t
	} else {
		transport = &http.Transport{
			Proxy:           http.ProxyFromEnvironment,
			TLSClientConfig: tlsCfg,
		}
	}

	return &cubAuthValidator{
		scheme:   scheme,
		domain:   domain,
		mirror:   strings.TrimSpace(cfg.Cub.Mirror),
		cache:    make(map[string]cubValCacheEntry),
		inflight: make(map[string]bool),
		client: &http.Client{
			Timeout:   10 * time.Second,
			Transport: transport,
		},
	}
}

// Validate resolves a CUB token to its account. Returns errCubTokenInvalid
// when upstream explicitly rejects the token; other errors mean upstream was
// unreachable (callers must fail closed — no binding on outage).
func (v *cubAuthValidator) Validate(ctx context.Context, cubToken string) (cubUserInfo, error) {
	cubToken = strings.TrimSpace(cubToken)
	if cubToken == "" || len(cubToken) > cubTokenMaxLen {
		return cubUserInfo{}, errCubTokenInvalid
	}

	v.mu.Lock()
	if e, ok := v.cache[cubToken]; ok && time.Now().Before(e.expiresAt) {
		v.mu.Unlock()
		return e.info, e.err
	}
	v.mu.Unlock()

	info, err := v.fetch(ctx, v.domain, cubToken)
	if err != nil && !errors.Is(err, errCubTokenInvalid) && v.mirror != "" && v.mirror != v.domain {
		info, err = v.fetch(ctx, v.mirror, cubToken)
	}

	ttl := cubValPositiveTTL
	if err != nil {
		ttl = cubValNegativeTTL
	}
	v.mu.Lock()
	v.cache[cubToken] = cubValCacheEntry{info: info, err: err, expiresAt: time.Now().Add(ttl)}
	// Opportunistic bound: the cache is keyed by secret tokens from real
	// clients, but don't let a flood grow it without limit.
	if len(v.cache) > 4096 {
		v.cache = map[string]cubValCacheEntry{cubToken: v.cache[cubToken]}
	}
	v.mu.Unlock()

	return info, err
}

func (v *cubAuthValidator) fetch(ctx context.Context, domain, cubToken string) (cubUserInfo, error) {
	url := v.scheme + "://" + domain + "/api/users/get"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return cubUserInfo{}, err
	}
	req.Header.Set("token", cubToken)
	req.Header.Set("User-Agent", "Mozilla/5.0 (SMART-TV; Linux) lampa")

	resp, err := v.client.Do(req)
	if err != nil {
		return cubUserInfo{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return cubUserInfo{}, errCubTokenInvalid
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return cubUserInfo{}, err
	}

	var data struct {
		Error   bool           `json:"error"`
		Code    int            `json:"code"`
		Secuses *bool          `json:"secuses"`
		User    map[string]any `json:"user"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		if resp.StatusCode != http.StatusOK {
			return cubUserInfo{}, fmt.Errorf("cub users/get: status %d", resp.StatusCode)
		}
		return cubUserInfo{}, fmt.Errorf("cub users/get: bad json: %w", err)
	}
	// CUB signals a bad token as HTTP 500 + {"error":true,"code":700,
	// "text":"Вход не выполнен"} — an application-level rejection, NOT an
	// outage. Misclassifying it as "unavailable" would hide the real cause
	// in logs and make the negative cache useless.
	if data.Error {
		return cubUserInfo{}, errCubTokenInvalid
	}
	if resp.StatusCode != http.StatusOK {
		return cubUserInfo{}, fmt.Errorf("cub users/get: status %d", resp.StatusCode)
	}
	if data.Secuses != nil && !*data.Secuses {
		return cubUserInfo{}, errCubTokenInvalid
	}
	if len(data.User) == 0 {
		return cubUserInfo{}, errCubTokenInvalid
	}

	info := cubUserInfo{
		ID:    cubJSONString(data.User["id"]),
		Email: cubJSONString(data.User["email"]),
	}
	if info.ID == "" {
		// No stable id — fall back to email as the anchor key.
		info.ID = strings.ToLower(info.Email)
	}
	if info.ID == "" {
		return cubUserInfo{}, errCubTokenInvalid
	}
	return info, nil
}

// cubJSONString normalizes a JSON value (string or number) to a string key.
func cubJSONString(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%v", t)
	default:
		return ""
	}
}

// LearnAsync links a CUB account to an already-authorized token in the
// background. Called whenever an authorized device presents a CUB token
// (auth-gate status polls, /cub/ proxy traffic) — this is what makes recovery
// zero-touch: the link forms passively while the user simply uses CUB.
func (v *cubAuthValidator) LearnAsync(store *tgauth.Store, ourToken, cubToken, ip string) {
	cubToken = strings.TrimSpace(cubToken)
	if store == nil || ourToken == "" || cubToken == "" || len(cubToken) > cubTokenMaxLen {
		return
	}

	key := ourToken + "\x00" + cubToken
	v.mu.Lock()
	// Fast path: token already validated and linked to this account.
	if e, ok := v.cache[cubToken]; ok && time.Now().Before(e.expiresAt) {
		if e.err != nil {
			v.mu.Unlock()
			return
		}
		v.mu.Unlock()
		if uid, _ := store.GetCubUser(ourToken); uid == e.info.ID {
			return
		}
		v.applyLink(store, ourToken, e.info, ip)
		return
	}
	if v.inflight[key] {
		v.mu.Unlock()
		return
	}
	v.inflight[key] = true
	v.mu.Unlock()

	go func() {
		defer func() {
			v.mu.Lock()
			delete(v.inflight, key)
			v.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		info, err := v.Validate(ctx, cubToken)
		if err != nil {
			return
		}
		v.applyLink(store, ourToken, info, ip)
	}()
}

func (v *cubAuthValidator) applyLink(store *tgauth.Store, ourToken string, info cubUserInfo, ip string) {
	// Auto variant: respects the user's explicit unlink (CubAutoLinkOptOut).
	if store.SetCubUserAuto(ourToken, info.ID, info.Email) {
		log.Info().Str("cub_uid", info.ID).Str("cub_email", info.Email).Str("ip", ip).
			Str("token_prefix", ourToken[:min(8, len(ourToken))]).
			Msg("tgauth: linked CUB account as recovery anchor")
	}
}

// resolveOurToken extracts the caller's ALPAC auth token from cookies, the
// cross-origin header escape hatch, or ?token= (in that order).
func resolveOurToken(r *http.Request) string {
	for _, cookieName := range []string{"_lampac_auth", "lampac_token"} {
		if c, err := r.Cookie(cookieName); err == nil && strings.TrimSpace(c.Value) != "" {
			return strings.TrimSpace(c.Value)
		}
	}
	if t := strings.TrimSpace(r.Header.Get("X-Alpac-Token")); t != "" {
		return t
	}
	if t := strings.TrimSpace(r.Header.Get("X-Lampac-Token")); t != "" {
		return t
	}
	return strings.TrimSpace(r.URL.Query().Get("token"))
}

// apiAuthCubLinkHandler explicitly links the caller's CUB account (token in
// ?cub=, validated live upstream) to their ALPAC account, clearing a previous
// unlink opt-out. Used by the account.js "Привязать CUB" action.
func apiAuthCubLinkHandler(store *tgauth.Store, cubVal *cubAuthValidator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil || cubVal == nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "auth disabled"})
			return
		}
		token := resolveOurToken(r)
		if token == "" {
			writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "not authorized"})
			return
		}
		if _, ok := store.Lookup(token); !ok {
			writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "not authorized"})
			return
		}
		cub := strings.TrimSpace(r.URL.Query().Get("cub"))
		if cub == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "cub token required"})
			return
		}
		info, err := cubVal.Validate(r.Context(), cub)
		if err != nil {
			msg := "cub unavailable"
			if errors.Is(err, errCubTokenInvalid) {
				msg = "cub token invalid"
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": msg})
			return
		}
		store.SetCubUser(token, info.ID, info.Email)
		log.Info().Str("cub_uid", info.ID).Str("ip", clientIP(r)).
			Str("token_prefix", token[:min(8, len(token))]).
			Msg("tgauth: CUB anchor explicitly linked by user")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "linked": true, "email": info.Email})
	}
}

// apiAuthCubUnlinkHandler removes the CUB recovery anchor from the caller's
// account. GET+POST (old TV WebViews block credentialed POSTs, same as
// /api/auth/logout). The caller must already be authorized — the token comes
// from our auth cookies (or ?token= for cookie-less WebViews).
func apiAuthCubUnlinkHandler(store *tgauth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "auth disabled"})
			return
		}
		token := resolveOurToken(r)
		if token == "" {
			writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "not authorized"})
			return
		}
		if _, ok := store.Lookup(token); !ok {
			writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "not authorized"})
			return
		}
		cleared := store.ClearCubUser(token)
		if cleared {
			log.Info().Str("token_prefix", token[:min(8, len(token))]).Str("ip", clientIP(r)).
				Msg("tgauth: CUB anchor unlinked by user")
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cleared": cleared})
	}
}
