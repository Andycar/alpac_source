package httpapi

import (
	"context"
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"lampac-go/internal/auth"
	"lampac-go/internal/config"
)

// withTestAuthUser injects a fake authenticated user so tests of source-
// discovery handlers ( /lite/events, /lite/<balancer> ) clear the gate
// added in 2026-05-28 (lite_auth_gate.go).
func withTestAuthUser(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := auth.WithUser(r.Context(), &auth.User{
			ID:      "tg:1",
			Expires: time.Now().Add(time.Hour),
		})
		h.ServeHTTP(w, r.WithContext(ctx))
	}
}

// authedTestRequest returns the request with an injected test user in its
// context — for handlers driven via ServeHTTP rather than mux registration.
func authedTestRequest(r *http.Request) *http.Request {
	ctx := auth.WithUser(r.Context(), &auth.User{
		ID:      "tg:1",
		Expires: time.Now().Add(time.Hour),
	})
	return r.WithContext(ctx)
}

// authedHandler wraps any http.Handler so it sees a test user in context.
// Use for balancer regression tests that call liteSourceHandler directly
// — saves rewriting every request construction call site.
func authedHandler(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, authedTestRequest(r))
	})
}

var _ = context.TODO

func TestLiteEventsLocalMode(t *testing.T) {
	resetHTTPAPIGlobals(t)
	cfg := config.Config{
		Online: config.OnlineConfig{
			CheckOnlineSearch: false,
			WithSearch:        []string{"filmix", "filmix", "kinotochka", "kinobase"},
		},
	}

	rec := httptest.NewRecorder()
	req := authedTestRequest(httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/events?id=550&original_language=ru", nil))
	liteEventsHandler(cfg, nil, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}

	var items []map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("invalid json: %v", err)
	}

	// Order is the default balancer sort (group → quality → known index), not
	// with_search order, so look items up by balanser rather than by position.
	// The two filmix entries dedupe to one → filmix, kinotochka, kinobase.
	byBalanser := make(map[string]map[string]any, len(items))
	for _, it := range items {
		name, _ := it["balanser"].(string)
		byBalanser[name] = it
	}

	if len(items) != 3 {
		t.Fatalf("unexpected items count: %d (%v)", len(items), byBalanser)
	}

	filmix, ok := byBalanser["filmix"]
	if !ok {
		t.Fatalf("expected a filmix item, got: %v", byBalanser)
	}
	filmixURL, _ := filmix["url"].(string)
	if !strings.Contains(filmixURL, "clarification=1") {
		t.Fatalf("expected clarification for filmix, got: %s", filmixURL)
	}

	kinobase, ok := byBalanser["kinobase"]
	if !ok {
		t.Fatalf("expected a kinobase item, got: %v", byBalanser)
	}
	kinobaseURL, _ := kinobase["url"].(string)
	if strings.Contains(kinobaseURL, "clarification=1") {
		t.Fatalf("unexpected clarification for kinobase: %s", kinobaseURL)
	}
	if _, ok := filmix["show"]; ok {
		t.Fatalf("did not expect show field when checkOnlineSearch=false")
	}
}

// TestLiteEventsIRemuxAlias guards the config/admin spelling "iremux"
// (matching [online.iremux] and admin panel "iRemux") against the internal
// registry key "remux". Before the alias normalisation a with_search entry of
// "iremux" was silently dropped by the localCorePlugins intersection and the
// source vanished from /lite/events. Both spellings must now resolve to the
// canonical "remux" balanser / /lite/remux route and display as "iRemux".
func TestLiteEventsIRemuxAlias(t *testing.T) {
	for _, spelling := range []string{"iremux", "remux"} {
		cfg := config.Config{
			Online: config.OnlineConfig{
				CheckOnlineSearch: false,
				WithSearch:        []string{spelling},
			},
		}
		rec := httptest.NewRecorder()
		req := authedTestRequest(httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/events?id=550", nil))
		liteEventsHandler(cfg, nil, nil).ServeHTTP(rec, req)

		var items []map[string]any
		if err := stdjson.Unmarshal(rec.Body.Bytes(), &items); err != nil {
			t.Fatalf("[%s] invalid json: %v", spelling, err)
		}
		if len(items) != 1 {
			t.Fatalf("[%s] expected 1 item, got %d: %s", spelling, len(items), rec.Body.String())
		}
		if items[0]["balanser"] != "remux" {
			t.Fatalf("[%s] expected canonical balanser 'remux', got %v", spelling, items[0]["balanser"])
		}
		url0, _ := items[0]["url"].(string)
		if !strings.Contains(url0, "/lite/remux") {
			t.Fatalf("[%s] expected /lite/remux route, got %s", spelling, url0)
		}
		name0, _ := items[0]["name"].(string)
		if !strings.Contains(name0, "iRemux") {
			t.Fatalf("[%s] expected display name to contain 'iRemux', got %q", spelling, name0)
		}
	}
}

func TestLiteEventsLifeDisabledWithoutID(t *testing.T) {
	resetHTTPAPIGlobals(t)
	cfg := config.Config{
		Online: config.OnlineConfig{WithSearch: []string{"kinobase"}},
	}

	rec := httptest.NewRecorder()
	req := authedTestRequest(httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/events?life=1", nil))
	liteEventsHandler(cfg, nil, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}

	// Without an id, checkOnlineSearch is off, so life mode is NOT started:
	// the handler returns the plain items array (an object {life,memkey} would
	// crash online.js, which treats a falsy json.life as a direct array).
	// See writeLifeEventsResponse.
	if life := strings.TrimSpace(rec.Body.String()); !strings.HasPrefix(life, "[") {
		t.Fatalf("expected array (life mode disabled), got: %s", life)
	}
	var items []map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(items) != 1 || items[0]["balanser"] != "kinobase" {
		t.Fatalf("expected single kinobase item, got: %v", items)
	}
}

func TestLiteEventsCheckOnlineSearch(t *testing.T) {
	cfg := config.Config{
		Online: config.OnlineConfig{
			CheckOnlineSearch: true,
			WithSearch:        []string{"kinobase", "filmix"},
		},
	}

	var (
		mu             sync.Mutex
		kinobaseHit    bool
		filmixHit      bool
		checksearchHit bool
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/lite/events", withTestAuthUser(liteEventsHandler(cfg, nil, nil)))
	mux.HandleFunc("/lite/kinobase", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		kinobaseHit = true
		if r.URL.Query().Get("checksearch") == "true" && r.URL.Query().Get("id") == "550" {
			checksearchHit = true
		}
		mu.Unlock()
		_, _ = w.Write([]byte(`{"type":"movie"}`))
	})
	mux.HandleFunc("/lite/filmix", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		filmixHit = true
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	// Probes run in-process now — route them into this test's stub balancers,
	// and reset the global probe caches/breakers (keys are host-independent, so
	// entries from other tests would suppress the probes this test asserts on).
	checksearchLiteOverride = mux.ServeHTTP
	t.Cleanup(func() { checksearchLiteOverride = nil })
	csCache.mu.Lock()
	csCache.entries = map[string]checksearchCacheEntry{}
	csCache.mu.Unlock()
	csBreaker.reset("kinobase")
	csBreaker.reset("filmix")
	invalidateMergedConfCache()

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/lite/events?id=550&serial=0")
	if err != nil {
		t.Fatalf("events request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: %d", resp.StatusCode)
	}

	var items []map[string]any
	if err := stdjson.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("unexpected items count: %d", len(items))
	}

	if show, _ := items[0]["show"].(bool); !show {
		t.Fatalf("expected kinobase show=true, got: %v", items[0]["show"])
	}
	if show, _ := items[1]["show"].(bool); show {
		t.Fatalf("expected filmix show=false, got: %v", items[1]["show"])
	}

	mu.Lock()
	defer mu.Unlock()
	if !kinobaseHit || !filmixHit || !checksearchHit {
		t.Fatalf("checksearch probes were not executed properly")
	}
}

func TestLiteEventsLifeModeAndPolling(t *testing.T) {
	cfg := config.Config{
		Online: config.OnlineConfig{
			CheckOnlineSearch: true,
			WithSearch:        []string{"kinobase"},
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/lite/events", withTestAuthUser(liteEventsHandler(cfg, nil, nil)))
	mux.HandleFunc("/lifeevents", lifeEventsHandler(cfg))
	mux.HandleFunc("/lite/kinobase", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(40 * time.Millisecond)
		_, _ = w.Write([]byte(`{"type":"movie"}`))
	})

	// In-process probes: point them at the stub balancer and clear shared state
	// (see TestLiteEventsCheckOnlineSearch).
	checksearchLiteOverride = mux.ServeHTTP
	t.Cleanup(func() { checksearchLiteOverride = nil })
	csCache.mu.Lock()
	csCache.entries = map[string]checksearchCacheEntry{}
	csCache.mu.Unlock()
	csBreaker.reset("kinobase")

	srv := httptest.NewServer(mux)
	defer srv.Close()

	startResp, err := http.Get(srv.URL + "/lite/events?life=1&id=550&serial=0")
	if err != nil {
		t.Fatalf("life start request failed: %v", err)
	}
	defer startResp.Body.Close()

	if startResp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: %d", startResp.StatusCode)
	}

	var startPayload map[string]any
	if err := stdjson.NewDecoder(startResp.Body).Decode(&startPayload); err != nil {
		t.Fatalf("invalid start payload: %v", err)
	}
	memkey, _ := startPayload["memkey"].(string)
	if memkey == "" {
		t.Fatalf("expected non-empty memkey")
	}
	if life, _ := startPayload["life"].(bool); !life {
		t.Fatalf("expected life=true")
	}

	var (
		ready  bool
		tasks  float64
		online []map[string]any
	)
	for range 30 {
		pollResp, err := http.Get(srv.URL + "/lifeevents?memkey=" + memkey)
		if err != nil {
			t.Fatalf("poll request failed: %v", err)
		}

		var pollPayload map[string]any
		if err := stdjson.NewDecoder(pollResp.Body).Decode(&pollPayload); err != nil {
			_ = pollResp.Body.Close()
			t.Fatalf("invalid poll payload: %v", err)
		}
		_ = pollResp.Body.Close()

		ready, _ = pollPayload["ready"].(bool)
		tasks, _ = pollPayload["tasks"].(float64)
		if rawOnline, ok := pollPayload["online"].([]any); ok {
			online = make([]map[string]any, 0, len(rawOnline))
			for _, item := range rawOnline {
				if m, ok := item.(map[string]any); ok {
					online = append(online, m)
				}
			}
		}
		if ready {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !ready {
		t.Fatalf("expected ready=true eventually")
	}
	if tasks != 1 {
		t.Fatalf("unexpected tasks count: %v", tasks)
	}
	if len(online) != 1 {
		t.Fatalf("unexpected online count: %d", len(online))
	}
	if show, _ := online[0]["show"].(bool); !show {
		t.Fatalf("expected show=true, got: %v", online[0]["show"])
	}
}
