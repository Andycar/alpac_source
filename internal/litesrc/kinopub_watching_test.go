package litesrc

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"lampac-go/internal/httpclient"
	"lampac-go/internal/kit"
)

// newKinopubTestChecker returns a checker hard-wired to a test
// upstream so handlers can be exercised without standing up an
// embedded kino.pub.
func newKinopubTestChecker(host, token string) *kinoPubChecker {
	return &kinoPubChecker{
		client:   httpclient.New(5 * time.Second),
		hosts:    []string{host},
		token:    token,
		filetype: "hls",
	}
}

// kitCtx attaches a Kit config to a request, simulating a
// TG-authenticated user. kpToken="" → user is authenticated but has
// NOT bound their kinopub account (the noop case).
func kitCtx(req *http.Request, kpToken string) *http.Request {
	cfg := map[string]stdjson.RawMessage{}
	if kpToken != "" {
		cfg["KinoPub"] = stdjson.RawMessage([]byte(`{"enable":true,"token":"` + kpToken + `"}`))
	} else {
		// Empty section — user authed but didn't bind kinopub.
		cfg["KinoPub"] = stdjson.RawMessage([]byte(`{}`))
	}
	return req.WithContext(kit.WithConfig(req.Context(), cfg))
}

// ---------------------------------------------------------------------
// Timeline emitter — per-user gating
// ---------------------------------------------------------------------

// TestKpAttachTimeline_Movie_GlobalMode: no Kit context (single-user
// lampac) — we attach the server callback so progress syncs to the
// global account.
func TestKpAttachTimeline_Movie_GlobalMode(t *testing.T) {
	k := newKinopubTestChecker("https://x", "GLOBAL")
	row := map[string]any{}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	k.kpAttachTimeline(req, row, &kpWatching{Time: 137}, 999, 42)

	tl := row["timeline"].(map[string]any)
	if tl["hash"] != "kp_999_42" {
		t.Errorf("hash = %v", tl["hash"])
	}
	if tl["time"] != int64(137) {
		t.Errorf("time = %v", tl["time"])
	}
	cb, _ := tl["callback"].(string)
	if !strings.Contains(cb, "/lite/kinopub/watching/marktime") {
		t.Errorf("callback expected in single-user mode: tl=%v", tl)
	}
	if row["callback"] == nil {
		t.Error("row-level callback missing in global mode")
	}
}

// TestKpAttachTimeline_Movie_KitMode: TG-authenticated user WITH a
// kinopub binding — server callback is emitted; marktime will route
// to their personal account.
func TestKpAttachTimeline_Movie_KitMode(t *testing.T) {
	k := newKinopubTestChecker("https://x", "GLOBAL")
	row := map[string]any{}
	req := kitCtx(httptest.NewRequest(http.MethodGet, "/", nil), "USER_TOK")
	k.kpAttachTimeline(req, row, &kpWatching{Time: 60}, 5, 6)

	tl := row["timeline"].(map[string]any)
	cb, _ := tl["callback"].(string)
	if cb == "" {
		t.Error("kit-bound user must get a server callback")
	}
}

// TestKpAttachTimeline_Movie_NoopMode: TG-authenticated user
// WITHOUT a kinopub binding — server callback is OMITTED so the
// frontend falls back to Lampa.Timeline's per-device localStorage
// and the shared global account stays clean.
func TestKpAttachTimeline_Movie_NoopMode(t *testing.T) {
	k := newKinopubTestChecker("https://x", "GLOBAL")
	row := map[string]any{}
	req := kitCtx(httptest.NewRequest(http.MethodGet, "/", nil), "")
	k.kpAttachTimeline(req, row, &kpWatching{Time: 60}, 5, 6)

	tl := row["timeline"].(map[string]any)
	if _, has := tl["callback"]; has {
		t.Errorf("kit-context-without-binding must NOT advertise server callback; tl=%v", tl)
	}
	if _, has := row["callback"]; has {
		t.Error("row.callback must be absent in noop mode")
	}
	if tl["hash"] == nil {
		t.Error("hash must still be emitted (Lampa localStorage uses it)")
	}
}

// TestKpAttachTimelineEpisode_HashSeriesAware verifies the serial
// hash is built from (season, episode) regardless of video ID — two
// different episodes get different hashes, same episode under
// different qualities keeps the hash stable.
func TestKpAttachTimelineEpisode_HashSeriesAware(t *testing.T) {
	k := newKinopubTestChecker("https://x", "G")
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	r1 := map[string]any{}
	r2 := map[string]any{}
	k.kpAttachTimelineEpisode(req, r1, nil, 100, 11, 2, 3)
	k.kpAttachTimelineEpisode(req, r2, nil, 100, 12, 2, 4)
	h1 := r1["timeline"].(map[string]any)["hash"]
	h2 := r2["timeline"].(map[string]any)["hash"]
	if h1 == h2 {
		t.Fatalf("episodes share hash: %v == %v", h1, h2)
	}
	r3 := map[string]any{}
	k.kpAttachTimelineEpisode(req, r3, nil, 100, 999, 2, 3)
	h3 := r3["timeline"].(map[string]any)["hash"]
	if h1 != h3 {
		t.Fatalf("same (season, episode) should share hash: %v != %v", h1, h3)
	}
}

// ---------------------------------------------------------------------
// /watching/marktime handler — per-user routing
// ---------------------------------------------------------------------

// runMarktime stands up a stub upstream + checker and invokes the
// handler. Returns (recorder, upstream-calls, last-captured-query)
// so tests can assert.
func runMarktime(t *testing.T, ctxToken string, hasKitCtx bool, queryString string, ckToken string) (*httptest.ResponseRecorder, int, url.Values) {
	t.Helper()
	calls := 0
	var captured url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		captured = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":200}`))
	}))
	t.Cleanup(srv.Close)

	checker := newKinopubTestChecker(srv.URL, ckToken)
	req := httptest.NewRequest(http.MethodPost, "/lite/kinopub/watching/marktime"+queryString, nil)
	if hasKitCtx {
		req = kitCtx(req, ctxToken)
	}
	rec := httptest.NewRecorder()
	checker.watchingMarktime(rec, req)
	return rec, calls, captured
}

// TestWatchingMarktime_KitBound_UsesPersonalToken — Kit-bound user
// gets their token forwarded, never the global one.
func TestWatchingMarktime_KitBound_UsesPersonalToken(t *testing.T) {
	rec, calls, q := runMarktime(t, "PERSONAL_TOK", true, "?id=10&time=42", "GLOBAL_TOK")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 1 {
		t.Fatalf("expected 1 upstream call, got %d", calls)
	}
	if q.Get("access_token") != "PERSONAL_TOK" {
		t.Errorf("expected personal token to be forwarded, got %q", q.Get("access_token"))
	}
}

// TestWatchingMarktime_TGAuthNoBinding_NoopsSilently — authenticated
// TG user who never bound kinopub doesn't trigger upstream and
// doesn't leak progress to the admin's account.
func TestWatchingMarktime_TGAuthNoBinding_NoopsSilently(t *testing.T) {
	rec, calls, _ := runMarktime(t, "", true, "?id=10&time=42", "GLOBAL_TOK")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 0 {
		t.Fatalf("expected zero upstream calls when user has no kinopub binding, got %d", calls)
	}
	if !strings.Contains(rec.Body.String(), "skipped") {
		t.Errorf("expected 'skipped' marker in body: %s", rec.Body.String())
	}
}

// TestWatchingMarktime_SingleUser_UsesGlobal — no Kit context at all
// (TG auth disabled) → global token is used. This is the original
// single-user-lampac behaviour.
func TestWatchingMarktime_SingleUser_UsesGlobal(t *testing.T) {
	rec, calls, q := runMarktime(t, "", false, "?id=10&time=42", "GLOBAL_TOK")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if calls != 1 {
		t.Fatalf("expected 1 upstream call, got %d", calls)
	}
	if q.Get("access_token") != "GLOBAL_TOK" {
		t.Errorf("expected global token, got %q", q.Get("access_token"))
	}
}

// TestWatchingMarktime_MissingID rejects calls without a postid so
// noisy frontend bugs don't get silently forwarded to kino.pub.
func TestWatchingMarktime_MissingID(t *testing.T) {
	rec, _, _ := runMarktime(t, "", false, "?time=10", "GLOBAL_TOK")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// TestWatchingMarktime_NoToken — single-user mode with no global
// token configured returns 401 so the frontend can stop polling.
func TestWatchingMarktime_NoToken(t *testing.T) {
	rec, calls, _ := runMarktime(t, "", false, "?id=1&time=10", "")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if calls != 0 {
		t.Error("should not have hit upstream without a token")
	}
}

// ---------------------------------------------------------------------
// v1 → v1.1 fallback
// ---------------------------------------------------------------------

func TestSearchAPI_V11Fallback(t *testing.T) {
	v1Hits := 0
	v11Hits := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/items/search", func(w http.ResponseWriter, r *http.Request) {
		v1Hits++
		http.Error(w, "v1 down", http.StatusInternalServerError)
	})
	mux.HandleFunc("/api2/v1.1/items/search", func(w http.ResponseWriter, r *http.Request) {
		v11Hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"id":123,"title":"Found"}]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	checker := newKinopubTestChecker(srv.URL, "T")
	req := httptest.NewRequest(http.MethodGet, "/lite/kinopub", nil)
	items, ok := checker.searchAPI(req, "T", "Test")
	if !ok || len(items) != 1 || items[0].ID != 123 {
		t.Fatalf("fallback failed: ok=%v items=%v", ok, items)
	}
	if v1Hits != 1 || v11Hits != 1 {
		t.Errorf("expected 1 v1 hit and 1 v11 hit, got %d and %d", v1Hits, v11Hits)
	}
}

func TestFetchItem_V11Fallback(t *testing.T) {
	v1Hits := 0
	v11Hits := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/items/", func(w http.ResponseWriter, r *http.Request) {
		v1Hits++
		http.NotFound(w, r)
	})
	mux.HandleFunc("/api2/v1.1/items/", func(w http.ResponseWriter, r *http.Request) {
		v11Hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":200,"item":{"videos":[{"id":1,"files":[]}],"quality":1080}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	checker := newKinopubTestChecker(srv.URL, "T")
	req := httptest.NewRequest(http.MethodGet, "/lite/kinopub", nil)
	root, ok := checker.fetchItem(req, "T", 555)
	if !ok {
		t.Fatalf("fetch failed; v1=%d v11=%d", v1Hits, v11Hits)
	}
	if root.Item.Quality != 1080 {
		t.Errorf("Quality field not decoded: %+v", root.Item)
	}
	if v1Hits != 1 || v11Hits != 1 {
		t.Errorf("call counts: v1=%d v11=%d", v1Hits, v11Hits)
	}
}

func TestFetchItem_BothFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	checker := newKinopubTestChecker(srv.URL, "T")
	req := httptest.NewRequest(http.MethodGet, "/lite/kinopub", nil)
	if _, ok := checker.fetchItem(req, "T", 1); ok {
		t.Fatal("fetchItem should fail when both API versions are down")
	}
}
