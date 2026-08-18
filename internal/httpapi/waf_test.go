package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ok200 is a trivial handler that always responds 200.
var ok200 = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
})

// makeWafState creates a wafState from config for testing (no geo DB).
func makeWafState(cfg wafConfig) *wafState {
	return newWafState(cfg, nil)
}

func doReq(handler http.Handler, ip, path string, headers map[string]string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local"+path, nil)
	req.RemoteAddr = ip + ":12345"
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	handler.ServeHTTP(rec, req)
	return rec
}

// ---------------------------------------------------------------------------

func TestWafDisabled(t *testing.T) {
	state := makeWafState(wafConfig{Enable: false})
	handler := wafMiddlewareFromState(state)(ok200)

	rec := doReq(handler, "1.2.3.4", "/test", nil)
	// With enable=false, wafMiddlewareFromState still runs check — but the
	// main wafMiddleware() returns next directly when disabled. For the state-
	// based test helper, all checks pass because no rules are configured.
	if rec.Code != http.StatusOK {
		t.Fatalf("disabled WAF should pass, got %d", rec.Code)
	}
}

func TestWafIPWhitelist(t *testing.T) {
	state := makeWafState(wafConfig{
		Enable:   true,
		WhiteIPs: []string{"5.5.5.5"},
		// Even with country deny — whitelist should bypass.
		CountryDeny: []string{"XX"},
		IPsDeny:     []string{"5.5.5.5"},
	})
	handler := wafMiddlewareFromState(state)(ok200)

	rec := doReq(handler, "5.5.5.5", "/test", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("whitelisted IP should pass, got %d", rec.Code)
	}
}

func TestWafBypassLocalIP(t *testing.T) {
	state := makeWafState(wafConfig{
		Enable:        true,
		BypassLocalIP: true,
		IPsDeny:       []string{"127.0.0.1"},
	})
	handler := wafMiddlewareFromState(state)(ok200)

	rec := doReq(handler, "127.0.0.1", "/test", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("local IP with bypass should pass, got %d", rec.Code)
	}

	rec2 := doReq(handler, "192.168.1.100", "/test", nil)
	if rec2.Code != http.StatusOK {
		t.Fatalf("private IP with bypass should pass, got %d", rec2.Code)
	}
}

func TestWafIPDenyExact(t *testing.T) {
	state := makeWafState(wafConfig{
		Enable:  true,
		IPsDeny: []string{"6.6.6.6", "7.7.7.7"},
	})
	handler := wafMiddlewareFromState(state)(ok200)

	rec := doReq(handler, "6.6.6.6", "/test", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("denied IP should get 403, got %d", rec.Code)
	}

	rec2 := doReq(handler, "8.8.8.8", "/test", nil)
	if rec2.Code != http.StatusOK {
		t.Fatalf("allowed IP should pass, got %d", rec2.Code)
	}
}

func TestWafIPDenyCIDR(t *testing.T) {
	state := makeWafState(wafConfig{
		Enable:  true,
		IPsDeny: []string{"10.0.0.0/8"},
	})
	handler := wafMiddlewareFromState(state)(ok200)

	rec := doReq(handler, "10.5.3.1", "/test", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("IP in denied CIDR should get 403, got %d", rec.Code)
	}

	rec2 := doReq(handler, "11.0.0.1", "/test", nil)
	if rec2.Code != http.StatusOK {
		t.Fatalf("IP outside CIDR should pass, got %d", rec2.Code)
	}
}

func TestWafIPAllowOnly(t *testing.T) {
	state := makeWafState(wafConfig{
		Enable:   true,
		IPsAllow: []string{"1.1.1.1", "2.2.0.0/16"},
	})
	handler := wafMiddlewareFromState(state)(ok200)

	rec := doReq(handler, "1.1.1.1", "/test", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("IP in allow list should pass, got %d", rec.Code)
	}

	rec2 := doReq(handler, "2.2.5.5", "/test", nil)
	if rec2.Code != http.StatusOK {
		t.Fatalf("IP in allow CIDR should pass, got %d", rec2.Code)
	}

	rec3 := doReq(handler, "3.3.3.3", "/test", nil)
	if rec3.Code != http.StatusForbidden {
		t.Fatalf("IP NOT in allow list should get 403, got %d", rec3.Code)
	}
}

func TestWafCountryDeny(t *testing.T) {
	// We can't test with real GeoIP in unit tests without a DB file.
	// Instead, test the logic by calling check() directly with a mock geo.
	// For this test, we verify config parsing and the deny set.
	state := makeWafState(wafConfig{
		Enable:      true,
		CountryDeny: []string{"CN", "ru"},
	})

	if !state.countryDenySet["CN"] {
		t.Fatal("CN should be in deny set")
	}
	if !state.countryDenySet["RU"] {
		t.Fatal("RU should be in deny set (uppercased)")
	}
}

func TestWafCountryAllow(t *testing.T) {
	state := makeWafState(wafConfig{
		Enable:       true,
		CountryAllow: []string{"US", "de"},
	})

	if !state.countryAllowSet["US"] {
		t.Fatal("US should be in allow set")
	}
	if !state.countryAllowSet["DE"] {
		t.Fatal("DE should be in allow set (uppercased)")
	}
}

func TestWafHeaderDeny(t *testing.T) {
	state := makeWafState(wafConfig{
		Enable:      true,
		HeadersDeny: map[string]string{"User-Agent": "(?i)badbot"},
	})
	handler := wafMiddlewareFromState(state)(ok200)

	rec := doReq(handler, "1.2.3.4", "/test", map[string]string{"User-Agent": "BadBot/1.0"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("denied header should get 403, got %d", rec.Code)
	}

	rec2 := doReq(handler, "1.2.3.4", "/test", map[string]string{"User-Agent": "Chrome/120"})
	if rec2.Code != http.StatusOK {
		t.Fatalf("normal header should pass, got %d", rec2.Code)
	}
}

func TestWafRateLimit(t *testing.T) {
	state := makeWafState(wafConfig{
		Enable:   true,
		LimitReq: 5, // 5 requests per 60s (default)
	})
	handler := wafMiddlewareFromState(state)(ok200)

	for i := range 5 {
		rec := doReq(handler, "9.9.9.9", "/anything", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d should pass, got %d", i+1, rec.Code)
		}
	}

	rec := doReq(handler, "9.9.9.9", "/anything", nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("request 6 should get 429, got %d", rec.Code)
	}

	// Different IP should still work.
	rec2 := doReq(handler, "8.8.8.8", "/anything", nil)
	if rec2.Code != http.StatusOK {
		t.Fatalf("different IP should pass, got %d", rec2.Code)
	}
}

func TestWafRateLimitPathRules(t *testing.T) {
	state := makeWafState(wafConfig{
		Enable: true,
		LimitMap: map[string]wafLimitMapRule{
			"^/lite/": {Limit: 3, Second: 60},
			"^/proxy": {Limit: 10, Second: 1},
		},
	})
	handler := wafMiddlewareFromState(state)(ok200)

	// /lite/ path — 3 request limit.
	for i := range 3 {
		rec := doReq(handler, "4.4.4.4", "/lite/events", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("/lite request %d should pass, got %d", i+1, rec.Code)
		}
	}
	rec := doReq(handler, "4.4.4.4", "/lite/collaps", nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("/lite request 4 should get 429, got %d", rec.Code)
	}

	// /api path — higher limit, same IP. Use /api/ (not /proxy/) because
	// /proxy/ matches the built-in static resource exemption.
	state2 := makeWafState(wafConfig{
		Enable: true,
		LimitMap: map[string]wafLimitMapRule{
			"^/api": {Limit: 10, Second: 1},
		},
	})
	handler2 := wafMiddlewareFromState(state2)(ok200)
	for i := range 10 {
		rec := doReq(handler2, "4.4.4.4", "/api/abc", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("/api request %d should pass, got %d", i+1, rec.Code)
		}
	}
	recP := doReq(handler2, "4.4.4.4", "/api/def", nil)
	if recP.Code != http.StatusTooManyRequests {
		t.Fatalf("/api request 11 should get 429, got %d", recP.Code)
	}
}

func TestWafRateLimitSkipsStaticJS(t *testing.T) {
	state := makeWafState(wafConfig{
		Enable:   true,
		LimitReq: 3, // very low limit
	})
	handler := wafMiddlewareFromState(state)(ok200)

	// Exhaust the default limit with non-JS requests.
	for i := range 3 {
		rec := doReq(handler, "7.7.7.7", "/api/data", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d should pass, got %d", i+1, rec.Code)
		}
	}

	// Next non-JS request should be blocked.
	rec := doReq(handler, "7.7.7.7", "/api/other", nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("non-JS request should get 429, got %d", rec.Code)
	}

	// But JS plugins should still work (bypassed).
	jsPages := []string{"/sync.js", "/bookmark.js", "/timecode.js", "/ts.js", "/backup.js", "/online.js", "/Torrents.js"}
	for _, p := range jsPages {
		rec := doReq(handler, "7.7.7.7", p, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("JS plugin %s should bypass rate limit, got %d", p, rec.Code)
		}
	}

	// CSS should also bypass.
	rec2 := doReq(handler, "7.7.7.7", "/css/app.css", nil)
	if rec2.Code != http.StatusOK {
		t.Fatalf("CSS should bypass rate limit, got %d", rec2.Code)
	}

	// TMDB images should bypass.
	for i := range 5 {
		rec := doReq(handler, "7.7.7.7", fmt.Sprintf("/tmdb/img/t/p/w300/poster%d.jpg", i), nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("TMDB img request %d should bypass rate limit, got %d", i+1, rec.Code)
		}
	}

	// TMDB API/metadata (proxy fallback) should ALSO bypass — a card grid / Details page fans out
	// many of these at once and the default limit was returning 429 for half of them.
	for i := range 30 {
		rec := doReq(handler, "7.7.7.7", fmt.Sprintf("/tmdb/api/3/movie/%d", i), nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("TMDB api request %d should bypass rate limit, got %d", i+1, rec.Code)
		}
	}

	// proxyimg should bypass.
	rec3 := doReq(handler, "7.7.7.7", "/proxyimg/https://image.tmdb.org/t/p/w300/test.jpg", nil)
	if rec3.Code != http.StatusOK {
		t.Fatalf("proxyimg should bypass rate limit, got %d", rec3.Code)
	}

	// The YouTube HLS mux (playlist + a segment every ~4s + init.mp4) is the one media stream
	// served under /lite/ — it must bypass like /proxy/ does, or playback 429s a minute in
	// (ffmpeg: «Server returned 4XX Client Error, but not one of 40{0,1,3,4}»).
	muxPaths := []string{
		"/lite/youtube/mux/index.m3u8", "/lite/youtube/mux/master.m3u8",
		"/lite/youtube/mux/seg", "/lite/youtube/mux/init.mp4", "/lite/youtube/mux",
	}
	for round := range 10 {
		for _, p := range muxPaths {
			rec := doReq(handler, "7.7.7.7", p, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("yt mux %s (round %d) should bypass rate limit, got %d", p, round+1, rec.Code)
			}
		}
	}
}

func TestWafBruteForce(t *testing.T) {
	state := makeWafState(wafConfig{
		Enable:               true,
		BruteForceProtection: true,
		BruteForceLimit:      3, // max 3 devices per IP
	})
	handler := wafMiddlewareFromState(state)(ok200)

	// 3 different devices from same IP.
	for i := range 3 {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet,
			fmt.Sprintf("http://lampac.local/test?uid=device%d", i), nil)
		req.RemoteAddr = "5.5.5.5:9999"
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("device %d should pass, got %d", i, rec.Code)
		}
	}

	// 4th device — should be blocked.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"http://lampac.local/test?uid=device99", nil)
	req.RemoteAddr = "5.5.5.5:9999"
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("4th device should get 429, got %d", rec.Code)
	}

	// Different IP — should work.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet,
		"http://lampac.local/test?uid=device99", nil)
	req2.RemoteAddr = "6.6.6.6:9999"
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("different IP should pass, got %d", rec2.Code)
	}
}

// TestWafBruteForceSkipsLoopback covers the 2026-05-28 fix: loopback
// and RFC1918 addresses bypass the per-IP device limit unconditionally.
// Two scenarios collapse all real-user IPs onto one local address —
// reverse proxy without X-Forwarded-For, and shared NAT — and would
// otherwise produce false-positive 429 bans for legitimate users.
func TestWafBruteForceSkipsLoopback(t *testing.T) {
	state := makeWafState(wafConfig{
		Enable:               true,
		BruteForceProtection: true,
		BruteForceLimit:      3, // tight limit, would otherwise trip
	})
	handler := wafMiddlewareFromState(state)(ok200)

	// 10 distinct devices on loopback — must ALL pass even with limit=3.
	for i := 0; i < 10; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet,
			fmt.Sprintf("http://lampac.local/test?uid=family%d", i), nil)
		req.RemoteAddr = "127.0.0.1:9999"
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("loopback device %d should pass (got %d) — brute-force must skip loopback", i, rec.Code)
		}
	}

	// Same for an RFC1918 private address (10.0.0.x).
	for i := 0; i < 10; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet,
			fmt.Sprintf("http://lampac.local/test?uid=office%d", i), nil)
		req.RemoteAddr = "10.0.0.42:9999"
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("RFC1918 device %d should pass (got %d) — brute-force must skip private nets", i, rec.Code)
		}
	}
}

func TestWafCustomWhitelistPath(t *testing.T) {
	state := makeWafState(wafConfig{
		Enable:               true,
		LimitReq:             1,
		CustomWhitelistPaths: []string{"/healthz"},
	})
	handler := wafMiddlewareFromState(state)(ok200)

	// /healthz must always pass — even repeatedly — because it's whitelisted.
	for i := 0; i < 5; i++ {
		rec := doReq(handler, "9.9.9.9", "/healthz", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("/healthz request %d should pass, got %d", i+1, rec.Code)
		}
	}
	// Other paths still hit the default limit (1).
	rec1 := doReq(handler, "9.9.9.9", "/api/data", nil)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first /api request should pass, got %d", rec1.Code)
	}
	rec2 := doReq(handler, "9.9.9.9", "/api/data", nil)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second /api request should 429, got %d", rec2.Code)
	}
}

func TestWafCustomWhitelistPrefix(t *testing.T) {
	state := makeWafState(wafConfig{
		Enable:                  true,
		LimitReq:                1,
		CustomWhitelistPrefixes: []string{"/special/"},
	})
	handler := wafMiddlewareFromState(state)(ok200)

	for i := 0; i < 5; i++ {
		rec := doReq(handler, "1.1.1.1", fmt.Sprintf("/special/sub/%d", i), nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("/special prefix %d should pass, got %d", i, rec.Code)
		}
	}
}

func TestWafManualBan(t *testing.T) {
	t.Setenv("LAMPAC_GO_HOME", t.TempDir())

	state := makeWafState(wafConfig{Enable: true})
	if err := state.AddManualBan("3.3.3.3", "test-ban", time.Hour); err != nil {
		t.Fatalf("add ban: %v", err)
	}
	handler := wafMiddlewareFromState(state)(ok200)

	rec := doReq(handler, "3.3.3.3", "/test", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("banned IP should 403, got %d", rec.Code)
	}
	if rec.Body.String() == "" {
		t.Fatalf("403 body should mention reason")
	}

	// Other IP unaffected.
	rec2 := doReq(handler, "4.4.4.4", "/test", nil)
	if rec2.Code != http.StatusOK {
		t.Fatalf("non-banned IP should pass, got %d", rec2.Code)
	}

	if err := state.RemoveManualBan("3.3.3.3"); err != nil {
		t.Fatalf("remove ban: %v", err)
	}
	rec3 := doReq(handler, "3.3.3.3", "/test", nil)
	if rec3.Code != http.StatusOK {
		t.Fatalf("unbanned IP should pass, got %d", rec3.Code)
	}
}

func TestWafManualBanExpires(t *testing.T) {
	t.Setenv("LAMPAC_GO_HOME", t.TempDir())

	state := makeWafState(wafConfig{Enable: true})
	if err := state.AddManualBan("5.5.5.5", "ttl-test", time.Millisecond); err != nil {
		t.Fatalf("add ban: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	handler := wafMiddlewareFromState(state)(ok200)
	rec := doReq(handler, "5.5.5.5", "/test", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expired ban should not block, got %d", rec.Code)
	}
}

func TestWafStatsRecord(t *testing.T) {
	state := makeWafState(wafConfig{
		Enable:  true,
		IPsDeny: []string{"6.6.6.6"},
	})
	handler := wafMiddlewareFromState(state)(ok200)

	for i := 0; i < 3; i++ {
		doReq(handler, "6.6.6.6", fmt.Sprintf("/path%d", i), nil)
	}
	snap := state.Stats()
	if total, _ := snap["total_24h"].(int); total < 3 {
		t.Fatalf("expected total_24h>=3, got %v", snap["total_24h"])
	}
	reasons, _ := snap["by_reason"].(map[string]int)
	if reasons["403 Forbidden"] < 3 {
		t.Fatalf("expected 403 reason count>=3, got %v", reasons)
	}
}

func TestWafReloadSwapsState(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", tmp)

	// Initial: no init.conf — WAF disabled.
	first := ReloadWAF()
	if first == nil {
		t.Fatalf("ReloadWAF returned nil")
	}
	if currentWafState() != first {
		t.Fatalf("currentWafState not updated")
	}

	// Write a config that bans 7.7.7.7 and reload.
	cfg := wafConfig{Enable: true, IPsDeny: []string{"7.7.7.7"}}
	if err := saveWafConfig(cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	second := ReloadWAF()
	if second == first {
		t.Fatalf("ReloadWAF did not swap the state pointer")
	}
	if !second.ipDeny["7.7.7.7"] {
		t.Fatalf("new state did not load deny list, got %v", second.cfg.IPsDeny)
	}
}

// Mirror/cluster control-plane paths must never be rate-limited: they self-authenticate with the
// mirror key, and a mirror validating its users' tokens against the origin exceeds any per-IP
// budget on its own. Prod 2026-08-17 saw 4242 × 429 on /api/auth/validate in three hours — the
// client reads 429 as «unauthorized», drops the token and loops.
func TestAuthPathsExemptFromRateLimit(t *testing.T) {
	exempt := []string{
		"/api/auth/validate",
		"/api/cluster/authgate",
		"/api/cluster/plugins.tar.gz",
		"/api/cluster/wwwroot.tar.gz",
		"/tg/auth/check",
		"/tg/device/verify",
	}
	for _, p := range exempt {
		if !isAuthPath(p) {
			t.Errorf("%s must be exempt from the rate limit", p)
		}
	}

	// Everything else keeps its budget — the exemption is a list, not a wildcard.
	limited := []string{
		"/api/auth/login",
		"/api/cluster",
		"/capi/collection",
		"/lite/events",
		"/",
	}
	for _, p := range limited {
		if isAuthPath(p) {
			t.Errorf("%s must stay rate-limited", p)
		}
	}
}
