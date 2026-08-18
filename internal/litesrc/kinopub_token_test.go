package litesrc

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestKinopubAPIHost_Default(t *testing.T) {
	cases := map[string]string{
		"":                         "https://api.srvkp.com",
		"  ":                       "https://api.srvkp.com",
		"https://api.srvkp.com":    "https://api.srvkp.com",
		"https://api.srvkp.com/":   "https://api.srvkp.com",
		"https://api.srvkp.com///": "https://api.srvkp.com",
		"https://custom.example/":  "https://custom.example",
	}
	for in, want := range cases {
		if got := KinopubAPIHost(in); got != want {
			t.Errorf("KinopubAPIHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExpiresAtFromIn(t *testing.T) {
	if !expiresAtFromIn(0).IsZero() {
		t.Error("zero TTL should yield zero time")
	}
	if !expiresAtFromIn(-1).IsZero() {
		t.Error("negative TTL should yield zero time")
	}
	got := expiresAtFromIn(3600)
	if d := time.Until(got); d < 50*time.Minute || d > 70*time.Minute {
		t.Errorf("3600s TTL → %v from now, want ~1h", d)
	}
}

func TestKinopubRequestDeviceCode_Roundtrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth2/device" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		q := r.URL.Query()
		if got := q.Get("grant_type"); got != "device_code" {
			t.Errorf("grant_type = %q", got)
		}
		if got := q.Get("client_id"); got != kinopubOAuthClientID {
			t.Errorf("client_id = %q, want android-derived %q", got, kinopubOAuthClientID)
		}
		if got := q.Get("client_secret"); got != kinopubOAuthClientSecret {
			t.Errorf("client_secret mismatch")
		}
		_ = stdjson.NewEncoder(w).Encode(map[string]string{
			"user_code": "ABC123",
			"code":      "internal-code-xyz",
		})
	}))
	defer srv.Close()

	userCode, code, err := KinopubRequestDeviceCode(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("RequestDeviceCode: %v", err)
	}
	if userCode != "ABC123" || code != "internal-code-xyz" {
		t.Errorf("got user_code=%q code=%q", userCode, code)
	}
}

func TestKinopubExchangeDeviceToken_PopulatesExpiry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("grant_type") != "device_token" {
			t.Errorf("grant_type = %q", q.Get("grant_type"))
		}
		if q.Get("code") != "device-handle" {
			t.Errorf("code = %q", q.Get("code"))
		}
		_ = stdjson.NewEncoder(w).Encode(map[string]any{
			"access_token":  "AT-fresh",
			"refresh_token": "RT-fresh",
			"expires_in":    7200,
		})
	}))
	defer srv.Close()

	tok, err := KinopubExchangeDeviceToken(context.Background(), srv.Client(), srv.URL, "device-handle")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if tok.AccessToken != "AT-fresh" || tok.RefreshToken != "RT-fresh" {
		t.Errorf("token = %+v", tok)
	}
	if d := time.Until(tok.ExpiresAt); d < 90*time.Minute || d > 130*time.Minute {
		t.Errorf("ExpiresAt off: %v from now", d)
	}
}

func TestKinopubExchangeDeviceToken_ErrorOnEmptyAccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	if _, err := KinopubExchangeDeviceToken(context.Background(), srv.Client(), srv.URL, "x"); err == nil {
		t.Fatal("expected error on empty access_token")
	}
}

func TestKinopubRefreshAccessToken_RotatesAndStores(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		q := r.URL.Query()
		if q.Get("grant_type") != "refresh_token" {
			t.Errorf("grant_type = %q", q.Get("grant_type"))
		}
		if q.Get("refresh_token") != "OLD-RT" {
			t.Errorf("refresh_token = %q", q.Get("refresh_token"))
		}
		_ = stdjson.NewEncoder(w).Encode(map[string]any{
			"access_token":  "NEW-AT",
			"refresh_token": "NEW-RT",
			"expires_in":    3600,
		})
	}))
	defer srv.Close()

	got, err := kinopubRefreshAccessToken(context.Background(), srv.Client(), srv.URL, "OLD-RT")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got.AccessToken != "NEW-AT" || got.RefreshToken != "NEW-RT" {
		t.Errorf("rotated set = %+v", got)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

func TestKinopubRefreshAccessToken_EmptyRefreshRejected(t *testing.T) {
	if _, err := kinopubRefreshAccessToken(context.Background(), http.DefaultClient, "https://x", ""); err == nil {
		t.Fatal("empty refresh token must be rejected without HTTP roundtrip")
	}
}

func TestKinopubTokenStore_SaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	store := newKinopubTokenStore(dir)
	expires := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	if err := store.Set(kinopubTokenSet{
		AccessToken:  "AT",
		RefreshToken: "RT",
		ExpiresAt:    expires,
	}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// File present at expected path.
	want := filepath.Join(dir, "database", "kinopub_token.json")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("file not written: %v", err)
	}

	// Fresh store reads the same value.
	store2 := newKinopubTokenStore(dir)
	if err := store2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	snap := store2.Snapshot()
	if snap.AccessToken != "AT" || snap.RefreshToken != "RT" || !snap.ExpiresAt.Equal(expires) {
		t.Errorf("roundtrip mismatch: %+v", snap)
	}
}

func TestKinopubTokenStore_SeedRespectsExisting(t *testing.T) {
	dir := t.TempDir()
	store := newKinopubTokenStore(dir)
	_ = store.Set(kinopubTokenSet{AccessToken: "preserved"})
	store.Seed("legacy-from-config")
	if got := store.Snapshot().AccessToken; got != "preserved" {
		t.Errorf("Seed should not clobber existing token: got %q", got)
	}
}

func TestKinopubTokenStore_SeedFillsEmpty(t *testing.T) {
	dir := t.TempDir()
	store := newKinopubTokenStore(dir)
	store.Seed("legacy-from-config")
	if got := store.Snapshot().AccessToken; got != "legacy-from-config" {
		t.Errorf("Seed must fill empty store: got %q", got)
	}
}

func TestKinopubTokenStore_GetTriggersRefresh(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always return a fresh triple — the test asserts that Get
		// reaches us when the current access is near expiry.
		_ = stdjson.NewEncoder(w).Encode(map[string]any{
			"access_token":  "refreshed",
			"refresh_token": "refreshed-rt",
			"expires_in":    7200,
		})
	}))
	defer srv.Close()

	store := newKinopubTokenStore(dir)
	_ = store.Set(kinopubTokenSet{
		AccessToken:  "stale",
		RefreshToken: "valid-rt",
		ExpiresAt:    time.Now().Add(10 * time.Second), // inside leeway
	})

	got, err := store.Get(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "refreshed" {
		t.Errorf("Get returned stale token: %q", got)
	}
	if snap := store.Snapshot(); snap.RefreshToken != "refreshed-rt" {
		t.Errorf("refresh token not rotated: %+v", snap)
	}
}

func TestKinopubTokenStore_GetReturnsFreshWhenInDate(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("refresh should NOT be called when token is still valid; request %s", r.URL.RawQuery)
	}))
	defer srv.Close()

	store := newKinopubTokenStore(dir)
	_ = store.Set(kinopubTokenSet{
		AccessToken:  "still-good",
		RefreshToken: "rt",
		ExpiresAt:    time.Now().Add(2 * time.Hour),
	})

	got, err := store.Get(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "still-good" {
		t.Errorf("Get = %q", got)
	}
}

func TestKinopubTokenStore_GetEmptyReturnsError(t *testing.T) {
	dir := t.TempDir()
	store := newKinopubTokenStore(dir)
	if _, err := store.Get(context.Background(), http.DefaultClient, "https://x"); err == nil {
		t.Fatal("expected error on empty store")
	} else if !strings.Contains(err.Error(), "no access token") {
		t.Errorf("error text changed: %v", err)
	}
}

func TestKinopubTokenStore_LoadMissingFileIsOK(t *testing.T) {
	dir := t.TempDir()
	store := newKinopubTokenStore(dir)
	if err := store.Load(); err != nil {
		t.Fatalf("Load on missing file: %v", err)
	}
	if !store.Snapshot().empty() {
		t.Error("Load of missing file should leave store empty")
	}
}

// TestKinopubTokenStore_GetReturnsStaleOnRefreshFailure documents the
// graceful-degradation contract: if the refresh endpoint is down we
// hand back the (possibly stale) cached token rather than failing
// hard. Caller will see a 401 from the API and surface it normally.
func TestKinopubTokenStore_GetReturnsStaleOnRefreshFailure(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	store := newKinopubTokenStore(dir)
	_ = store.Set(kinopubTokenSet{
		AccessToken:  "stale-but-handed-back",
		RefreshToken: "rt",
		ExpiresAt:    time.Now().Add(5 * time.Second),
	})

	got, err := store.Get(context.Background(), srv.Client(), srv.URL)
	if got != "stale-but-handed-back" {
		t.Errorf("got = %q, want stale token", got)
	}
	if err == nil {
		t.Error("expected non-nil error documenting the refresh failure")
	}
	// Sanity-check the error mentions "refresh"; future changes should
	// keep the phrase so log filters still pick it up.
	if err != nil && !strings.Contains(err.Error(), "refresh") {
		t.Errorf("error should mention refresh: %v", err)
	}
}

// _ keeps fmt imported even when individual debug calls are removed.
var _ = fmt.Sprintf

// kino.pub caps concurrent playback sessions per ACCOUNT (403 "user session
// limit reached"), so a multi-viewer server must spread load over the accounts
// the admin configured. The device-flow store holds one account's token and
// must not shadow that list — it joins the pool instead.
func TestKinopubGetTokenRotatesConfiguredTokens(t *testing.T) {
	k := &kinoPubChecker{
		token:  "cfg-a",
		tokens: []string{"cfg-a", "cfg-b", "cfg-c"},
	}

	seen := map[string]int{}
	for i := 0; i < 30; i++ {
		seen[k.getToken(context.Background())]++
	}
	for _, want := range []string{"cfg-a", "cfg-b", "cfg-c"} {
		if seen[want] == 0 {
			t.Fatalf("token %q never selected: %v", want, seen)
		}
	}

	// Single configured token: always that one, no rotation state to leak.
	single := &kinoPubChecker{token: "solo", tokens: []string{"solo"}}
	if got := single.getToken(context.Background()); got != "solo" {
		t.Fatalf("single token: got %q", got)
	}

	// No tokens list at all → legacy single-token field.
	legacy := &kinoPubChecker{token: "legacy"}
	if got := legacy.getToken(context.Background()); got != "legacy" {
		t.Fatalf("legacy token: got %q", got)
	}
}
