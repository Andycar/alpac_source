package litesrc

import (
	"context"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

// ---------------------------------------------------------------------
// OAuth client credentials
//
// Extracted from the official kino.pub Android APK v1.34 (May 2026).
// Same credentials accept device_code, device_token and refresh_token
// grants, so we keep one pair globally. The legacy XBMC client
// (client_id=xbmc, secret=cgg3gt…) also works for device flows but
// reportedly does not honour refresh_token — that's the entire reason
// we migrated.
// ---------------------------------------------------------------------

const (
	kinopubOAuthClientID     = "android"
	kinopubOAuthClientSecret = "rcaqh7wodackn9ll1uggvqkx2iib6umh"

	// refreshLeeway is how early before ExpiresAt we proactively refresh.
	// API token TTL is on the order of 24h; one minute of leeway is
	// generous without burning refreshes.
	kinopubRefreshLeeway = 60 * time.Second
)

// kinopubDefaultHosts are the known kino.pub API mirrors (list from
// lampac-nextgen, 2026-07; the site itself now lives at kino.watch):
// srvkp = standard, cdn32/cdn4t = APK builds, kpapp = smart-tv,
// service-kp = old. Mirrors die individually (srvkp stopped resolving
// from some networks in July 2026), so requests fail over: a sticky
// index selects the active mirror and advances on TRANSPORT errors
// only (4xx/5xx mean the host is alive — no rotation).
var kinopubDefaultHosts = []string{
	"https://api.srvkp.com",
	"https://cdn32.lol/api",
	"https://cdn4t.store/api",
	"https://kpapp.link/api",
	"https://api.service-kp.com",
}

// kinopubHostIdx is the process-wide sticky mirror index (mod len(hosts)).
var kinopubHostIdx atomic.Int32

// errKinopubTransport marks a transport-level failure (dial/TLS/timeout) —
// the only class of error that rotates the mirror.
var errKinopubTransport = errors.New("kinopub: transport error")

// kinopubAPIHosts returns the failover-ordered host list: the configured
// host pinned first (when set), then the default mirrors, deduped.
func kinopubAPIHosts(configured string) []string {
	configured = strings.TrimRight(strings.TrimSpace(configured), "/")
	hosts := make([]string, 0, len(kinopubDefaultHosts)+1)
	if configured != "" {
		hosts = append(hosts, configured)
	}
	for _, h := range kinopubDefaultHosts {
		if h != configured {
			hosts = append(hosts, h)
		}
	}
	return hosts
}

// kinopubActiveHost returns the sticky-selected mirror.
func kinopubActiveHost(hosts []string) string {
	if len(hosts) == 0 {
		return kinopubDefaultHosts[0]
	}
	return hosts[int(kinopubHostIdx.Load())%len(hosts)]
}

// kinopubAdvanceHost rotates to the next mirror after a transport-level
// failure of `failed`. CAS keeps concurrent failures from skipping
// mirrors: only the goroutine that saw the CURRENT host fail advances.
func kinopubAdvanceHost(hosts []string, failed string) {
	if len(hosts) < 2 {
		return
	}
	idx := kinopubHostIdx.Load()
	if hosts[int(idx)%len(hosts)] != failed {
		return // someone already advanced past the dead mirror
	}
	if kinopubHostIdx.CompareAndSwap(idx, idx+1) {
		log.Warn().Str("failed", failed).Str("next", hosts[int(idx+1)%len(hosts)]).Msg("kinopub: API mirror failover")
	}
}

// KinopubAPIHost keeps the legacy single-host call sites working: it
// resolves the failover-aware active mirror for the given config value.
func KinopubAPIHost(configured string) string {
	return kinopubActiveHost(kinopubAPIHosts(configured))
}

// kinopubTokenSet is the (access, refresh, expires_at) triple returned
// by device_token / refresh_token responses. We store this on disk so
// the access token can be transparently refreshed without re-running
// the device-code dance every TTL window.
type kinopubTokenSet struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
}

func (t kinopubTokenSet) empty() bool { return t.AccessToken == "" }
func (t kinopubTokenSet) expiresSoon() bool {
	if t.ExpiresAt.IsZero() {
		return false // no TTL info — caller decides
	}
	return time.Until(t.ExpiresAt) <= kinopubRefreshLeeway
}

// ---------------------------------------------------------------------
// OAuth helpers
//
// Stateless functions that drive the device-flow. Used by both
// kinopub.go's web-UI activation page (/lite/kinopubpro) and
// kit_bind.go's per-user binding endpoint.
// ---------------------------------------------------------------------

// KinopubRequestDeviceCode issues the first call: returns user_code
// (shown to the user, entered at kino.pub/device) and code (opaque
// handle we pass to KinopubExchangeDeviceToken once the user finishes).
func KinopubRequestDeviceCode(ctx context.Context, client *http.Client, host string) (userCode, code string, err error) {
	uri := host + "/oauth2/device?grant_type=device_code&client_id=" + kinopubOAuthClientID + "&client_secret=" + kinopubOAuthClientSecret
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uri, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "lampac-go/kinopub")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	var out struct {
		UserCode string `json:"user_code"`
		Code     string `json:"code"`
	}
	if err := stdjson.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return "", "", err
	}
	if out.UserCode == "" {
		return "", "", errors.New("kinopub: empty user_code in device_code response")
	}
	return out.UserCode, out.Code, nil
}

// KinopubExchangeDeviceToken polls the device_token endpoint after the
// user enters the user_code at kino.pub/device. Returns the full token
// triple including refresh_token and computed ExpiresAt.
func KinopubExchangeDeviceToken(ctx context.Context, client *http.Client, host, code string) (kinopubTokenSet, error) {
	uri := fmt.Sprintf("%s/oauth2/device?grant_type=device_token&client_id=%s&client_secret=%s&code=%s",
		host, kinopubOAuthClientID, kinopubOAuthClientSecret, code)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uri, nil)
	if err != nil {
		return kinopubTokenSet{}, err
	}
	req.Header.Set("User-Agent", "lampac-go/kinopub")
	resp, err := client.Do(req)
	if err != nil {
		return kinopubTokenSet{}, err
	}
	defer resp.Body.Close()

	var raw struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := stdjson.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&raw); err != nil {
		return kinopubTokenSet{}, err
	}
	if raw.AccessToken == "" {
		return kinopubTokenSet{}, errors.New("kinopub: empty access_token in device_token response — code not yet confirmed at kino.watch/device")
	}
	return kinopubTokenSet{
		AccessToken:  raw.AccessToken,
		RefreshToken: raw.RefreshToken,
		ExpiresAt:    expiresAtFromIn(raw.ExpiresIn),
	}, nil
}

// kinopubRefreshAccessToken trades a refresh_token for a fresh access +
// refresh pair. Kino.pub rotates the refresh on every call, so we
// always overwrite the stored value with what came back.
func kinopubRefreshAccessToken(ctx context.Context, client *http.Client, host, refresh string) (kinopubTokenSet, error) {
	if strings.TrimSpace(refresh) == "" {
		return kinopubTokenSet{}, errors.New("kinopub: refresh token is empty")
	}
	uri := fmt.Sprintf("%s/oauth2/device?grant_type=refresh_token&client_id=%s&client_secret=%s&refresh_token=%s",
		host, kinopubOAuthClientID, kinopubOAuthClientSecret, refresh)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uri, nil)
	if err != nil {
		return kinopubTokenSet{}, err
	}
	req.Header.Set("User-Agent", "lampac-go/kinopub")
	resp, err := client.Do(req)
	if err != nil {
		return kinopubTokenSet{}, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var raw struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Error        string `json:"error"`
	}
	if err := stdjson.Unmarshal(body, &raw); err != nil {
		return kinopubTokenSet{}, fmt.Errorf("decode refresh response: %w (body=%q)", err, string(body))
	}
	if raw.AccessToken == "" {
		if raw.Error != "" {
			return kinopubTokenSet{}, fmt.Errorf("kinopub refresh: %s", raw.Error)
		}
		return kinopubTokenSet{}, fmt.Errorf("kinopub refresh: no access_token in response (body=%q)", string(body))
	}
	return kinopubTokenSet{
		AccessToken:  raw.AccessToken,
		RefreshToken: raw.RefreshToken,
		ExpiresAt:    expiresAtFromIn(raw.ExpiresIn),
	}, nil
}

func expiresAtFromIn(secs int64) time.Time {
	if secs <= 0 {
		return time.Time{}
	}
	return time.Now().Add(time.Duration(secs) * time.Second)
}

// ---------------------------------------------------------------------
// Persistent token store
//
// File layout: <repoRoot>/database/kinopub_token.json
//
//	{
//	  "access_token": "...",
//	  "refresh_token": "...",
//	  "expires_at": "2026-05-19T12:34:56Z"
//	}
//
// Used by the *global* kinopub configuration only. Per-user kit-bound
// tokens are kept in the Kit store and refreshed on demand by the
// kinopub handler when kit.TokenOverride supplies a refresh_token.
// ---------------------------------------------------------------------

type kinopubTokenStore struct {
	mu      sync.Mutex
	path    string
	loaded  bool
	current kinopubTokenSet
}

func newKinopubTokenStore(repoRoot string) *kinopubTokenStore {
	return &kinopubTokenStore{path: kinopubTokenPath(repoRoot)}
}

func kinopubTokenPath(repoRoot string) string {
	if strings.TrimSpace(repoRoot) == "" {
		repoRoot = "."
	}
	return filepath.Join(repoRoot, "database", "kinopub_token.json")
}

// Load reads the on-disk file if present. Missing file is not an
// error — the store stays empty and the caller may seed it from
// config.toml's token field (legacy single-string mode).
func (s *kinopubTokenStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loaded = true

	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var t kinopubTokenSet
	if err := stdjson.Unmarshal(data, &t); err != nil {
		return err
	}
	s.current = t
	return nil
}

// Seed installs an access token coming from somewhere outside the
// OAuth flow (typically config.toml's [online.kinopub].token field).
// We *don't* overwrite an existing on-disk record — that one came
// from a real device-flow and has refresh capability we want to keep.
func (s *kinopubTokenStore) Seed(accessToken string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current.AccessToken != "" {
		return
	}
	s.current = kinopubTokenSet{AccessToken: strings.TrimSpace(accessToken)}
}

// Set replaces the current token triple and persists to disk.
func (s *kinopubTokenStore) Set(t kinopubTokenSet) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = t
	return s.saveLocked()
}

// Snapshot returns a copy of the current token triple for read-only
// inspection (admin UI, debug endpoints).
func (s *kinopubTokenStore) Snapshot() kinopubTokenSet {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}

// Get returns a usable access token, refreshing if the current one is
// within kinopubRefreshLeeway of expiry. If the access token has no
// known TTL (e.g. seeded from config.toml without going through the
// OAuth flow) it is returned as-is — caller will discover expiry by
// hitting a 401 from the API.
func (s *kinopubTokenStore) Get(ctx context.Context, client *http.Client, host string) (string, error) {
	s.mu.Lock()
	cur := s.current
	s.mu.Unlock()

	if cur.empty() {
		return "", errors.New("kinopub: no access token available — activate via /lite/kinopubpro")
	}
	if !cur.expiresSoon() {
		return cur.AccessToken, nil
	}
	if cur.RefreshToken == "" {
		// Token is about to expire but we don't have a way to renew.
		// Return what we have; the caller will see 401 and the user
		// will re-activate manually.
		return cur.AccessToken, nil
	}

	fresh, err := kinopubRefreshAccessToken(ctx, client, host, cur.RefreshToken)
	if err != nil {
		// On refresh failure return the old token — better to try a
		// stale request than to hard-fail.
		return cur.AccessToken, fmt.Errorf("kinopub: refresh failed, using stale token: %w", err)
	}
	if err := s.Set(fresh); err != nil {
		return fresh.AccessToken, fmt.Errorf("kinopub: persist refreshed token failed: %w", err)
	}
	return fresh.AccessToken, nil
}

func (s *kinopubTokenStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := stdjson.MarshalIndent(s.current, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
