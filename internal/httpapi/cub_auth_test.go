package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"lampac-go/internal/tgauth"
)

// fakeCubUpstream serves /api/users/get accepting only the given token.
func fakeCubUpstream(t *testing.T, validToken string, user string, hits *atomic.Int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/users/get" {
			http.NotFound(w, r)
			return
		}
		if hits != nil {
			hits.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("token") != validToken {
			// Real CUB rejects a bad token with HTTP 500 + an app-level error
			// payload (NOT 401/403) — mirror that shape here.
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":true,"code":700,"text":"Вход не выполнен"}`))
			return
		}
		_, _ = w.Write([]byte(`{"secuses":true,"user":` + user + `}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testCubValidator(srv *httptest.Server) *cubAuthValidator {
	return &cubAuthValidator{
		client:   srv.Client(),
		scheme:   "http",
		domain:   strings.TrimPrefix(srv.URL, "http://"),
		cache:    make(map[string]cubValCacheEntry),
		inflight: make(map[string]bool),
	}
}

func TestCubValidator_ParsesNumericID(t *testing.T) {
	srv := fakeCubUpstream(t, "cubtok", `{"id":12345,"email":"u@example.com"}`, nil)
	v := testCubValidator(srv)

	info, err := v.Validate(context.Background(), "cubtok")
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if info.ID != "12345" || info.Email != "u@example.com" {
		t.Errorf("info = %+v, want ID=12345 Email=u@example.com", info)
	}
}

func TestCubValidator_ParsesStringID(t *testing.T) {
	srv := fakeCubUpstream(t, "cubtok", `{"id":"abc-1","email":""}`, nil)
	v := testCubValidator(srv)

	info, err := v.Validate(context.Background(), "cubtok")
	if err != nil || info.ID != "abc-1" {
		t.Errorf("Validate = (%+v, %v), want ID=abc-1", info, err)
	}
}

func TestCubValidator_EmailFallbackWhenNoID(t *testing.T) {
	srv := fakeCubUpstream(t, "cubtok", `{"email":"Only@Mail.com"}`, nil)
	v := testCubValidator(srv)

	info, err := v.Validate(context.Background(), "cubtok")
	if err != nil || info.ID != "only@mail.com" {
		t.Errorf("Validate = (%+v, %v), want ID=only@mail.com", info, err)
	}
}

func TestCubValidator_RejectsInvalidToken(t *testing.T) {
	srv := fakeCubUpstream(t, "cubtok", `{"id":1}`, nil)
	v := testCubValidator(srv)

	if _, err := v.Validate(context.Background(), "wrong"); !errors.Is(err, errCubTokenInvalid) {
		t.Errorf("Validate(wrong) err = %v, want errCubTokenInvalid", err)
	}
	if _, err := v.Validate(context.Background(), ""); !errors.Is(err, errCubTokenInvalid) {
		t.Errorf("Validate(empty) err = %v, want errCubTokenInvalid", err)
	}
}

func TestCubValidator_CachesPositive(t *testing.T) {
	var hits atomic.Int64
	srv := fakeCubUpstream(t, "cubtok", `{"id":7}`, &hits)
	v := testCubValidator(srv)

	for range 3 {
		if _, err := v.Validate(context.Background(), "cubtok"); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	}
	if hits.Load() != 1 {
		t.Errorf("upstream hits = %d, want 1 (cached)", hits.Load())
	}
}

func TestCubValidator_UpstreamDownFailsClosed(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	v := testCubValidator(srv)
	srv.Close() // dead upstream

	_, err := v.Validate(context.Background(), "cubtok")
	if err == nil {
		t.Fatal("Validate succeeded against a dead upstream")
	}
	if errors.Is(err, errCubTokenInvalid) {
		t.Error("outage classified as invalid token — callers must be able to tell them apart")
	}
}

func waitForCubLink(t *testing.T, store *tgauth.Store, token, wantUID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if uid, _ := store.GetCubUser(token); uid == wantUID {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	uid, _ := store.GetCubUser(token)
	t.Fatalf("cub link not learned: GetCubUser = %q, want %q", uid, wantUID)
}

func TestCubValidator_LearnAsyncLinks(t *testing.T) {
	srv := fakeCubUpstream(t, "cubtok", `{"id":55,"email":"l@example.com"}`, nil)
	v := testCubValidator(srv)
	store, _ := seedStore(t)

	v.LearnAsync(store, "tok-A", "cubtok", "1.2.3.4")
	waitForCubLink(t, store, "tok-A", "55")

	// Invalid token must never link.
	v.LearnAsync(store, "tok-B", "wrong", "1.2.3.4")
	time.Sleep(150 * time.Millisecond)
	if uid, _ := store.GetCubUser("tok-B"); uid != "" {
		t.Errorf("invalid cub token linked to tok-B as %q", uid)
	}
}

// --- /tg/auth/status CUB rung ---

func TestStatus_CubRecovery(t *testing.T) {
	srv := fakeCubUpstream(t, "cubtok", `{"id":77}`, nil)
	v := testCubValidator(srv)
	store, pending := seedStore(t)
	store.SetCubUser("tok-A", "77", "")

	h := tgAuthStatusHandler(store, pending, "lampacbot", v)
	r := httptest.NewRequest(http.MethodGet, "/tg/auth/status?cub=cubtok&uid=uid-new&sfp=sfp-new", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	body := parseBody(t, w)
	if body["authorized"] != true || body["token"] != "tok-A" {
		t.Fatalf("cub recovery failed: %v", body)
	}
	if !store.HasDevice("tok-A", "uid-new") {
		t.Error("recovered device uid-new not bound to tok-A")
	}
}

func TestStatus_CubRecovery_NoUID_BindsSynthetic(t *testing.T) {
	srv := fakeCubUpstream(t, "cubtok", `{"id":77}`, nil)
	v := testCubValidator(srv)
	store, pending := seedStore(t)
	store.SetCubUser("tok-A", "77", "")

	h := tgAuthStatusHandler(store, pending, "lampacbot", v)
	// Old cached gate JS / external Lampa: cub + sfp, no uid, no fp.
	r := httptest.NewRequest(http.MethodGet, "/tg/auth/status?cub=cubtok&sfp=deadbeef99", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	body := parseBody(t, w)
	if body["authorized"] != true || body["token"] != "tok-A" {
		t.Fatalf("cub recovery without uid failed: %v", body)
	}
	if !store.HasDevice("tok-A", "sfp-deadbeef") {
		t.Error("device not bound with synthetic sfp- uid")
	}
}

func TestStatus_CubInvalidToken_NoAuth(t *testing.T) {
	srv := fakeCubUpstream(t, "cubtok", `{"id":77}`, nil)
	v := testCubValidator(srv)
	store, pending := seedStore(t)
	store.SetCubUser("tok-A", "77", "")

	h := tgAuthStatusHandler(store, pending, "lampacbot", v)
	r := httptest.NewRequest(http.MethodGet, "/tg/auth/status?cub=stolen-or-wrong", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	body := parseBody(t, w)
	if body["authorized"] != false {
		t.Fatalf("invalid cub token authorized: %v", body)
	}
	if _, ok := body["code"]; !ok {
		t.Errorf("response missing pending code: %v", body)
	}
}

func TestStatus_CubUpstreamDown_FailsClosed(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	v := testCubValidator(srv)
	srv.Close()
	store, pending := seedStore(t)
	store.SetCubUser("tok-A", "77", "")

	h := tgAuthStatusHandler(store, pending, "lampacbot", v)
	r := httptest.NewRequest(http.MethodGet, "/tg/auth/status?cub=cubtok", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	body := parseBody(t, w)
	if body["authorized"] != false {
		t.Fatalf("authorized during CUB outage: %v", body)
	}
}

func TestStatus_CubAmbiguousLink_NoAuth(t *testing.T) {
	srv := fakeCubUpstream(t, "cubtok", `{"id":77}`, nil)
	v := testCubValidator(srv)
	store, pending := seedStore(t)
	// Ambiguity via the passive path — explicit links steal the anchor.
	store.SetCubUser("tok-A", "77", "")
	store.SetCubUserAuto("tok-B", "77", "")

	h := tgAuthStatusHandler(store, pending, "lampacbot", v)
	r := httptest.NewRequest(http.MethodGet, "/tg/auth/status?cub=cubtok", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	body := parseBody(t, w)
	if body["authorized"] != false {
		t.Fatalf("ambiguous cub link authorized someone: %v", body)
	}
}

func TestStatus_AuthorizedRung_LearnsCubLink(t *testing.T) {
	srv := fakeCubUpstream(t, "cubtok", `{"id":91,"email":"z@example.com"}`, nil)
	v := testCubValidator(srv)
	store, pending := seedStore(t)

	h := tgAuthStatusHandler(store, pending, "lampacbot", v)
	r := httptest.NewRequest(http.MethodGet, "/tg/auth/status?cub=cubtok", nil)
	r.AddCookie(&http.Cookie{Name: "lampac_token", Value: "tok-A"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	body := parseBody(t, w)
	if body["authorized"] != true {
		t.Fatalf("cookie auth failed: %v", body)
	}
	waitForCubLink(t, store, "tok-A", "91")
}

// --- /cub/ proxy passive learning ---

func TestCubProxy_PassiveLearnsLink(t *testing.T) {
	up := fakeCubUpstream(t, "cubtok", `{"id":33}`, nil)
	v := testCubValidator(up)
	store, _ := seedStore(t)

	h := &cubProxy{
		client:  up.Client(),
		scheme:  "http",
		tgStore: store,
		cubVal:  v,
	}

	domain := strings.TrimPrefix(up.URL, "http://")
	r := httptest.NewRequest(http.MethodGet, "/cub/"+domain+"/api/notifications/all", nil)
	r.Header.Set("token", "cubtok")
	r.AddCookie(&http.Cookie{Name: "_lampac_auth", Value: "tok-A"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	waitForCubLink(t, store, "tok-A", "33")
}

// TestStatus_AuthorizedRungs_BindDevice covers the lampa.mx scenario: an
// external Lampa authorizes via localStorage token (?token=) or cookie, but
// /lite/* there resolves the user by ?uid= alone — so the authorized rungs
// must bind the device, not just answer "authorized".
func TestStatus_AuthorizedRungs_BindDevice(t *testing.T) {
	store, pending := seedStore(t)
	h := tgAuthStatusHandler(store, pending, "lampacbot", nil)

	// Query-token rung.
	r := httptest.NewRequest(http.MethodGet, "/tg/auth/status?token=tok-A&uid=mx-uid-1&sfp=mx-sfp", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if body := parseBody(t, w); body["authorized"] != true {
		t.Fatalf("query-token auth failed: %v", body)
	}
	if !store.HasDevice("tok-A", "mx-uid-1") {
		t.Error("query-token rung did not bind the device")
	}

	// Cookie rung.
	r = httptest.NewRequest(http.MethodGet, "/tg/auth/status?uid=mx-uid-2", nil)
	r.AddCookie(&http.Cookie{Name: "lampac_token", Value: "tok-B"})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if body := parseBody(t, w); body["authorized"] != true {
		t.Fatalf("cookie auth failed: %v", body)
	}
	if !store.HasDevice("tok-B", "mx-uid-2") {
		t.Error("cookie rung did not bind the device")
	}
}

// TestWhoami_QueryTokenFallback: on external Lampa hosts the middleware
// resolves nobody (no cross-site cookie), so whoami must honor ?token=
// directly — same contract as /api/user/info.
func TestWhoami_QueryTokenFallback(t *testing.T) {
	store, _ := seedStore(t)
	old := tgTokenStoreRef
	tgTokenStoreRef = store
	t.Cleanup(func() { tgTokenStoreRef = old })

	h := apiAuthWhoamiHandler(nil)

	r := httptest.NewRequest(http.MethodGet, "/api/auth/whoami?token=tok-A", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	body := parseBody(t, w)
	if body["authenticated"] != true || body["type"] != "tg" {
		t.Fatalf("whoami query-token fallback failed: %v", body)
	}

	r = httptest.NewRequest(http.MethodGet, "/api/auth/whoami?token=nonexistent", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if body = parseBody(t, w); body["authenticated"] != false {
		t.Errorf("whoami authenticated an unknown token: %v", body)
	}
}

// --- link / unlink endpoints ---

func TestCubLinkUnlinkEndpoints(t *testing.T) {
	srv := fakeCubUpstream(t, "cubtok", `{"id":21,"email":"e@example.com"}`, nil)
	v := testCubValidator(srv)
	store, _ := seedStore(t)

	link := apiAuthCubLinkHandler(store, v)
	unlink := apiAuthCubUnlinkHandler(store)

	// Unauthorized: no token at all.
	w := httptest.NewRecorder()
	link.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/auth/cub/link?cub=cubtok", nil))
	if w.Code != http.StatusForbidden {
		t.Errorf("link without auth: code = %d, want 403", w.Code)
	}

	// Explicit link via cookie auth.
	r := httptest.NewRequest(http.MethodGet, "/api/auth/cub/link?cub=cubtok", nil)
	r.AddCookie(&http.Cookie{Name: "lampac_token", Value: "tok-A"})
	w = httptest.NewRecorder()
	link.ServeHTTP(w, r)
	body := parseBody(t, w)
	if body["ok"] != true || body["linked"] != true {
		t.Fatalf("link failed: %v", body)
	}
	if uid, email := store.GetCubUser("tok-A"); uid != "21" || email != "e@example.com" {
		t.Errorf("after link GetCubUser = (%q, %q)", uid, email)
	}

	// Invalid cub token → ok:false, no 500.
	r = httptest.NewRequest(http.MethodGet, "/api/auth/cub/link?cub=wrong", nil)
	r.AddCookie(&http.Cookie{Name: "lampac_token", Value: "tok-B"})
	w = httptest.NewRecorder()
	link.ServeHTTP(w, r)
	if body = parseBody(t, w); body["ok"] != false {
		t.Errorf("link with invalid cub token: %v", body)
	}

	// Unlink (header auth path) → cleared and opted out of auto re-link.
	r = httptest.NewRequest(http.MethodGet, "/api/auth/cub/unlink", nil)
	r.Header.Set("X-Alpac-Token", "tok-A")
	w = httptest.NewRecorder()
	unlink.ServeHTTP(w, r)
	if body = parseBody(t, w); body["ok"] != true || body["cleared"] != true {
		t.Fatalf("unlink failed: %v", body)
	}
	if uid, _ := store.GetCubUser("tok-A"); uid != "" {
		t.Errorf("link survived unlink: %q", uid)
	}

	// Passive learner must not re-link after the explicit unlink.
	v.LearnAsync(store, "tok-A", "cubtok", "1.2.3.4")
	time.Sleep(150 * time.Millisecond)
	if uid, _ := store.GetCubUser("tok-A"); uid != "" {
		t.Errorf("passive learner re-linked after unlink: %q", uid)
	}
}

func TestCubProxy_NoOwnAuth_NoLearn(t *testing.T) {
	up := fakeCubUpstream(t, "cubtok", `{"id":33}`, nil)
	v := testCubValidator(up)
	store, _ := seedStore(t)

	h := &cubProxy{
		client:  up.Client(),
		scheme:  "http",
		tgStore: store,
		cubVal:  v,
	}

	domain := strings.TrimPrefix(up.URL, "http://")
	r := httptest.NewRequest(http.MethodGet, "/cub/"+domain+"/api/notifications/all", nil)
	r.Header.Set("token", "cubtok")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	time.Sleep(150 * time.Millisecond)
	if uid, _ := store.GetCubUser("tok-A"); uid != "" {
		t.Errorf("link learned without our auth cookie: %q", uid)
	}
}
