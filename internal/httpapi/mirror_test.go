package httpapi

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/kit"
	"lampac-go/internal/tgauth"
)

// TestGatePreAuthAllowed guards the whitelist extracted from
// tgAuthGateMiddleware: both the local gate and the mirror gate depend on it
// returning identical decisions, so a representative regression matters.
func TestGatePreAuthAllowed(t *testing.T) {
	cases := []struct {
		method, path, hdrKey, hdrVal string
		want                         bool
	}{
		{http.MethodOptions, "/lite/events", "", "", true}, // CORS preflight
		{http.MethodGet, "/", "", "", true},                // Lampa web app root
		{http.MethodGet, "/proxy/abc.mp4", "", "", true},   // stream
		{http.MethodGet, "/online.js", "", "", true},       // static .js
		{http.MethodGet, "/tmdb/api/3/x", "", "", true},    // TMDB proxy
		{http.MethodGet, "/tg/auth/status", "", "", true},  // auth flow
		{http.MethodGet, "/api/auth/validate", "", "", true},
		{http.MethodPost, "/api/cluster/authgate", "", "", true},
		{http.MethodGet, "/lite/events", "X-Lampac-Go", "1", true}, // loopback probe
		// /ts/* — TorrServer authenticates itself (cookie / ?token= / Basic
		// "uid:ts"). It MUST bypass the app-wide gate: a Lampa hosted on another
		// origin (lampa.mx) sends TorrServer XHRs without withCredentials, so no
		// cookie arrives and the gate would answer the liveness probe with an
		// auth payload → "не удалось подключиться к TorrServer".
		{http.MethodGet, "/ts", "", "", true},
		{http.MethodGet, "/ts/echo", "", "", true},
		{http.MethodPost, "/ts/settings", "", "", true},
		{http.MethodPost, "/ts/torrents", "", "", true}, // gated downstream, not here
		{http.MethodGet, "/ts/stream/Movie.mkv", "", "", true},
		{http.MethodGet, "/ts/download/300", "", "", true},
		// Denied — these require auth.
		{http.MethodGet, "/lite/events", "", "", false},
		{http.MethodGet, "/lite/rezka/video", "", "", false},
		{http.MethodGet, "/sisi", "", "", false},
		{http.MethodGet, "/api/something", "", "", false},
		{http.MethodGet, "/tsunami", "", "", false}, // /ts prefix must not over-match
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, c.path, nil)
		if c.hdrKey != "" {
			r.Header.Set(c.hdrKey, c.hdrVal)
		}
		if got := gatePreAuthAllowed(r); got != c.want {
			t.Errorf("gatePreAuthAllowed(%s %s hdr=%s) = %v, want %v", c.method, c.path, c.hdrKey, got, c.want)
		}
	}
}

// originRequest builds a loopback request so isMirrorOriginRequest passes via
// its localhost branch (no serverRef / secret needed in the test).
func originRequest(method, target string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	r.RemoteAddr = "127.0.0.1:5555"
	return r
}

func newTestStoreWithToken(t *testing.T) *tgauth.Store {
	t.Helper()
	store := tgauth.NewStore(t.TempDir())
	if err := store.Add(tgauth.ApprovedToken{
		Token:      "GOODTOKEN",
		TelegramID: 4242,
		TGUsername: "tester",
		ExpiresAt:  time.Now().Add(24 * time.Hour),
	}); err != nil {
		t.Fatalf("store.Add: %v", err)
	}
	if _, err := store.AddDevice("GOODTOKEN", tgauth.DeviceInfo{UID: "dev1", Label: "Linux"}); err != nil {
		t.Fatalf("store.AddDevice: %v", err)
	}
	return store
}

func TestMirrorOriginValidateHandler(t *testing.T) {
	h := mirrorOriginValidateHandler(newTestStoreWithToken(t), nil)

	// By token.
	rec := httptest.NewRecorder()
	h(rec, originRequest(http.MethodGet, "/api/auth/validate?token=GOODTOKEN"))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) || !strings.Contains(rec.Body.String(), `"tg_id":4242`) {
		t.Fatalf("by token: code=%d body=%s", rec.Code, rec.Body.String())
	}

	// By device UID.
	rec = httptest.NewRecorder()
	h(rec, originRequest(http.MethodGet, "/api/auth/validate?uid=dev1"))
	if !strings.Contains(rec.Body.String(), `"ok":true`) || !strings.Contains(rec.Body.String(), `"tg_id":4242`) {
		t.Fatalf("by uid: body=%s", rec.Body.String())
	}

	// Unknown token → ok:false.
	rec = httptest.NewRecorder()
	h(rec, originRequest(http.MethodGet, "/api/auth/validate?token=NOPE"))
	if !strings.Contains(rec.Body.String(), `"ok":false`) {
		t.Fatalf("unknown: body=%s", rec.Body.String())
	}

	// Forbidden when not a loopback/secret request.
	rec = httptest.NewRecorder()
	bad := httptest.NewRequest(http.MethodGet, "/api/auth/validate?token=GOODTOKEN", nil)
	bad.RemoteAddr = "8.8.8.8:1234"
	h(rec, bad)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-origin request, got %d", rec.Code)
	}
}

// stubGate emulates a real gate for the synthetic-replay handler: token
// "GOODTOKEN" → allow (next); ?issue=1 → allow after issuing a fresh cookie;
// otherwise → deny with an accsdb body.
func stubGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := ""
		if c, err := r.Cookie("lampac_token"); err == nil {
			tok = c.Value
		}
		switch {
		case tok == "GOODTOKEN":
			next.ServeHTTP(w, r)
		case r.URL.Query().Get("issue") == "1":
			http.SetCookie(w, &http.Cookie{Name: "lampac_token", Value: "ISSUED", Path: "/"})
			next.ServeHTTP(w, r)
		default:
			writeJSON(w, http.StatusOK, accsdbResponse("need auth"))
		}
	})
}

// runAuthGate posts in to the authgate handler and decodes the verdict.
func runAuthGate(t *testing.T, h http.HandlerFunc, in mirrorGateRequest) mirrorGateVerdict {
	t.Helper()
	body, _ := json.Marshal(in)
	r := httptest.NewRequest(http.MethodPost, "/api/cluster/authgate", strings.NewReader(string(body)))
	r.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	h(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("authgate status %d body=%s", rec.Code, rec.Body.String())
	}
	var v mirrorGateVerdict
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode verdict: %v body=%s", err, rec.Body.String())
	}
	return v
}

func TestMirrorOriginAuthGateHandler(t *testing.T) {
	h := mirrorOriginAuthGateHandler(stubGate)
	post := func(in mirrorGateRequest) mirrorGateVerdict { return runAuthGate(t, h, in) }

	// Valid token → allow, no fresh token.
	if v := post(mirrorGateRequest{Path: "/lite/events", Token: "GOODTOKEN", ClientIP: "1.2.3.4"}); !v.Allow || v.SetToken != "" {
		t.Fatalf("good token: %+v", v)
	}

	// issue=1 → allow + the gate's freshly-issued token captured.
	if v := post(mirrorGateRequest{Path: "/lite/events", RawQuery: "issue=1", ClientIP: "1.2.3.4"}); !v.Allow || v.SetToken != "ISSUED" {
		t.Fatalf("issue: %+v", v)
	}

	// No token → deny, accsdb body replayed.
	v := post(mirrorGateRequest{Path: "/lite/events", ClientIP: "1.2.3.4"})
	if v.Allow {
		t.Fatalf("expected deny, got allow")
	}
	decoded, _ := base64.StdEncoding.DecodeString(v.BodyB64)
	if !strings.Contains(string(decoded), `"accsdb":true`) {
		t.Fatalf("deny body missing accsdb: %s", decoded)
	}
}

// TestMirrorOriginAuthGateRealGate exercises the synthetic-replay against the
// REAL tgAuthGateMiddleware (not a stub): the highest-risk integration. Proves
// clientIP resolution, device-bound allow, and the accsdb auth-card path all
// survive being run through a reconstructed request.
func TestMirrorOriginAuthGateRealGate(t *testing.T) {
	store := newTestStoreWithToken(t) // GOODTOKEN + bound dev1
	pending := tgauth.NewPendingStore()
	var cfg config.Config
	cfg.TelegramAuth.BotName = "testbot"
	cfg.TelegramAuth.MaxDevicesPerUser = 3
	// nil bot/banStore/geoDB/memberChecker/dp are all guarded inside the gate.
	gateMW := tgAuthGateMiddleware(store, nil, pending, nil, nil, nil, nil, cfg)
	h := mirrorOriginAuthGateHandler(gateMW)

	// Valid token + already-bound uid → allow.
	if v := runAuthGate(t, h, mirrorGateRequest{
		Path: "/lite/events", RawQuery: "uid=dev1", Token: "GOODTOKEN",
		ClientIP: "1.2.3.4", UserAgent: "Mozilla/5.0 (X11; Linux x86_64)",
	}); !v.Allow {
		t.Fatalf("real gate, valid bound token: %+v", v)
	}

	// No token + new uid → accsdb auth card carrying a pending code.
	v := runAuthGate(t, h, mirrorGateRequest{
		Path: "/lite/events", RawQuery: "uid=brandnew", ClientIP: "5.6.7.8",
		UserAgent: "Mozilla/5.0 (X11; Linux x86_64)", Accept: "application/json",
		XRequestedWith: "XMLHttpRequest",
	})
	if v.Allow {
		t.Fatalf("real gate, no token: expected auth card, got allow")
	}
	decoded, _ := base64.StdEncoding.DecodeString(v.BodyB64)
	if !strings.Contains(string(decoded), `"accsdb":true`) || !strings.Contains(string(decoded), `"code"`) {
		t.Fatalf("real gate, no token: expected accsdb card with code, got: %s", decoded)
	}
	// The pending code must have been created on the origin's store.
	if _, ok := pending.FindByUID("brandnew"); !ok {
		t.Fatalf("real gate: pending code not created on origin for uid")
	}
}

func TestMirrorAuthProxyMiddleware(t *testing.T) {
	var (
		gotPath string
		gotXFF  string
	)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		gotXFF = r.Header.Get("X-Forwarded-For")
		http.SetCookie(w, &http.Cookie{Name: "lampac_token", Value: "FROMBETA", Path: "/"})
		writeJSON(w, http.StatusOK, map[string]any{"status": "approved", "token": "FROMBETA"})
	}))
	defer origin.Close()

	c := newMirrorClient(mirrorCfg(origin.URL))
	var nextCalled bool
	mw := mirrorProxyMiddleware(c)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
	}))

	// /tg/auth/status → proxied to origin, response + Set-Cookie relayed.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/tg/auth/status?uid=u1", nil)
	req.RemoteAddr = "203.0.113.9:4444"
	mw.ServeHTTP(rec, req)
	if nextCalled {
		t.Fatalf("/tg/auth should be proxied, not passed to next")
	}
	if gotPath != "/tg/auth/status?uid=u1" {
		t.Fatalf("origin saw wrong path/query: %q", gotPath)
	}
	if gotXFF != "203.0.113.9" {
		t.Fatalf("client IP not forwarded: X-Forwarded-For=%q", gotXFF)
	}
	if !strings.Contains(rec.Body.String(), `"token":"FROMBETA"`) {
		t.Fatalf("origin body not relayed: %s", rec.Body.String())
	}
	if !strings.Contains(strings.Join(rec.Header().Values("Set-Cookie"), ";"), "lampac_token=FROMBETA") {
		t.Fatalf("origin Set-Cookie not relayed: %v", rec.Header().Values("Set-Cookie"))
	}

	// Non-/tg path → falls through to next.
	nextCalled = false
	rec = httptest.NewRecorder()
	mw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/lite/events", nil))
	if !nextCalled {
		t.Fatalf("/lite path should fall through to next")
	}
}

func mirrorCfg(host string) func() config.MirrorConfig {
	return func() config.MirrorConfig {
		return config.MirrorConfig{Enable: true, APIHost: host, APIPasswd: "secret", CacheTTLSec: 60, GraceTTLSec: 900, SyncPluginsMin: 5}
	}
}

// TestMirrorEndToEndAuthDelegation connects both halves over real HTTP: the
// mirror gate asks an origin httptest server running the REAL gate behind the
// authgate RPC, and replays the verdict. Proves the full delegated-auth path.
func TestMirrorEndToEndAuthDelegation(t *testing.T) {
	store := newTestStoreWithToken(t) // GOODTOKEN + bound dev1
	pending := tgauth.NewPendingStore()
	var cfg config.Config
	cfg.TelegramAuth.BotName = "testbot"
	cfg.TelegramAuth.MaxDevicesPerUser = 3
	realGate := tgAuthGateMiddleware(store, nil, pending, nil, nil, nil, nil, cfg)

	origin := httptest.NewServer(mirrorOriginAuthGateHandler(realGate))
	defer origin.Close()

	c := newMirrorClient(mirrorCfg(origin.URL))
	var allowed bool
	mw := mirrorGateMiddleware(c)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowed = true
		w.WriteHeader(http.StatusOK)
	}))

	// Valid token + bound device → origin allows → mirror forwards to next.
	allowed = false
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/lite/events?uid=dev1", nil)
	req.AddCookie(&http.Cookie{Name: "lampac_token", Value: "GOODTOKEN"})
	mw.ServeHTTP(rec, req)
	if !allowed {
		t.Fatalf("valid token: expected allow, body=%s", rec.Body.String())
	}

	// No token + new uid → origin returns accsdb card → mirror replays it.
	allowed = false
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/lite/events?uid=freshdev", nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	mw.ServeHTTP(rec, req)
	if allowed {
		t.Fatalf("no token: expected card, got allow")
	}
	if !strings.Contains(rec.Body.String(), `"accsdb":true`) || !strings.Contains(rec.Body.String(), `"code"`) {
		t.Fatalf("no token: expected accsdb card with code, got: %s", rec.Body.String())
	}
	// The origin's bot is the one in the card (single bot for the fleet).
	if !strings.Contains(rec.Body.String(), "testbot") {
		t.Fatalf("card should reference the origin bot: %s", rec.Body.String())
	}
}

func TestMirrorShouldProxy(t *testing.T) {
	// Auth control-plane — proxied regardless of the state toggle.
	for _, p := range []string{"/tg/auth/status", "/tg/device/verify"} {
		if !mirrorShouldProxy(p, false) || !mirrorShouldProxy(p, true) {
			t.Errorf("%s should always be proxied", p)
		}
	}
	// Per-user state — proxied only when centralized.
	state := []string{"/bookmark/list", "/timecode/all", "/storage/get", "/api/iptv/playlists", "/sisi/bookmarks"}
	for _, p := range state {
		if !mirrorShouldProxy(p, true) {
			t.Errorf("%s should be proxied when centralized", p)
		}
		if mirrorShouldProxy(p, false) {
			t.Errorf("%s should NOT be proxied when state centralization is off", p)
		}
	}
	// Data-plane — never proxied (served + streamed locally).
	for _, p := range []string{"/lite/events", "/proxy/abc.mp4", "/online.js", "/tmdb/api/3/x"} {
		if mirrorShouldProxy(p, true) {
			t.Errorf("%s must be handled locally, not proxied", p)
		}
	}
}

func TestMirrorDelegatedGroup(t *testing.T) {
	c := newMirrorClient(mirrorCfg("http://origin"))
	grp := &tgauth.UserGroup{ID: "vip", Name: "VIP", Balancers: map[string]bool{"rezka": false}}
	c.valCache["t:tok1"] = mirrorValEntry{
		resp: mirrorValidateResp{OK: true, TGID: 5, GroupDef: grp}, ok: true, at: time.Now(),
	}

	req := httptest.NewRequest(http.MethodGet, "/lite/events", nil)
	req.AddCookie(&http.Cookie{Name: "lampac_token", Value: "tok1"})

	// groupForRequest finds the cached delegated group; BalancerAllowed reflects it.
	g := c.groupForRequest(req)
	if g == nil || g.BalancerAllowed("rezka") || !g.BalancerAllowed("collaps") {
		t.Fatalf("groupForRequest: %+v", g)
	}

	// resolveUserGroup uses the delegated group when mirrorClientRef is set
	// (origin store absent on a mirror).
	prev := mirrorClientRef
	mirrorClientRef = c
	defer func() { mirrorClientRef = prev }()
	if g2 := resolveUserGroup(req); g2 == nil || g2.ID != "vip" || g2.BalancerAllowed("rezka") {
		t.Fatalf("resolveUserGroup delegated: %+v", g2)
	}

	// Unknown token → nil (treated as unrestricted).
	req2 := httptest.NewRequest(http.MethodGet, "/lite/events", nil)
	req2.AddCookie(&http.Cookie{Name: "lampac_token", Value: "nope"})
	if c.groupForRequest(req2) != nil {
		t.Fatal("unknown token should yield nil group")
	}
}

func TestMirrorDelegatedKit(t *testing.T) {
	c := newMirrorClient(mirrorCfg("http://origin"))
	c.valCache["t:tok1"] = mirrorValEntry{
		resp: mirrorValidateResp{OK: true, TGID: 5, KitVisibility: map[string]bool{"rezka": false}},
		ok:   true, at: time.Now(),
	}

	req := httptest.NewRequest(http.MethodGet, "/lite/events", nil)
	req.AddCookie(&http.Cookie{Name: "lampac_token", Value: "tok1"})

	// The cached personal visibility is found.
	if vis := c.kitVisibilityForRequest(req); vis == nil || vis["rezka"] {
		t.Fatalf("kitVisibilityForRequest: %+v", vis)
	}

	// The middleware injects it; kit.BalancerVisible reflects the user's hide.
	var hidden bool
	mirrorKitContextMiddleware(c)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, configured := kit.BalancerVisible(r.Context(), "rezka")
		hidden = configured && !v
	})).ServeHTTP(httptest.NewRecorder(), req)
	if !hidden {
		t.Fatal("rezka should be hidden via delegated kit visibility")
	}

	// Non-/lite path → no injection (kit context absent).
	var configuredOnRoot bool
	rootReq := httptest.NewRequest(http.MethodGet, "/", nil)
	rootReq.AddCookie(&http.Cookie{Name: "lampac_token", Value: "tok1"})
	mirrorKitContextMiddleware(c)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, configuredOnRoot = kit.BalancerVisible(r.Context(), "rezka")
	})).ServeHTTP(httptest.NewRecorder(), rootReq)
	if configuredOnRoot {
		t.Fatal("kit context should not be injected on non-/lite paths")
	}
}

func TestMirrorValidateGroupRoundTrip(t *testing.T) {
	// The effective group must survive JSON marshal (origin) → unmarshal (mirror).
	src := mirrorValidateResp{
		OK: true, TGID: 7,
		GroupDef: &tgauth.UserGroup{ID: "free", Balancers: map[string]bool{"kinopub": false}, StrictBalancers: true},
	}
	b, _ := json.Marshal(src)
	var got mirrorValidateResp
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.GroupDef == nil || got.GroupDef.ID != "free" || !got.GroupDef.StrictBalancers {
		t.Fatalf("group def lost over wire: %+v", got.GroupDef)
	}
	// Strict allowlist: kinopub explicitly false → denied; unlisted → denied too.
	if got.GroupDef.BalancerAllowed("kinopub") || got.GroupDef.BalancerAllowed("rezka") {
		t.Fatalf("strict allowlist not honored after round-trip")
	}
}

func TestMirrorAssetSyncRoundtrip(t *testing.T) {
	// Origin's plugins dir.
	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "online.js"), []byte("// stock v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(srcDir, "custom"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "custom", "x.js"), []byte("stock-custom"), 0o644); err != nil {
		t.Fatal(err)
	}

	cache := &assetTarballCache{name: "plugins", dirFn: func() string { return srcDir }}
	srv := httptest.NewServer(mirrorOriginAssetHandler(cache))
	defer srv.Close()

	// Mirror's (initially empty-ish) plugins dir with a local-only plugin.
	dstDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dstDir, "local.js"), []byte("mirror-local"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := newMirrorAssetSyncer("plugins", "/api/cluster/plugins.tar.gz", dstDir, mirrorCfg(srv.URL))

	// First sync — stock files land, local-only file preserved.
	s.syncOnce()
	if b, _ := os.ReadFile(filepath.Join(dstDir, "online.js")); string(b) != "// stock v1" {
		t.Fatalf("online.js not synced: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dstDir, "custom", "x.js")); string(b) != "stock-custom" {
		t.Fatalf("custom/x.js not synced: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dstDir, "local.js")); string(b) != "mirror-local" {
		t.Fatalf("local-only plugin not preserved: %q", b)
	}
	firstSha := s.lastSha
	if firstSha == "" {
		t.Fatal("lastSha not set after first sync")
	}

	// Second sync, unchanged → 304, sha unchanged, files intact.
	s.syncOnce()
	if s.lastSha != firstSha {
		t.Fatalf("sha changed on unchanged content: %s → %s", firstSha, s.lastSha)
	}

	// Change a stock file on the origin → new content + sha propagate.
	// (sleep a hair so the dir mtime advances and the cache rebuilds.)
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(srcDir, "online.js"), []byte("// stock v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.syncOnce()
	if b, _ := os.ReadFile(filepath.Join(dstDir, "online.js")); string(b) != "// stock v2" {
		t.Fatalf("update not propagated: %q", b)
	}
	if s.lastSha == firstSha {
		t.Fatal("sha did not change after content update")
	}
	// Local plugin still there after the second swap.
	if b, _ := os.ReadFile(filepath.Join(dstDir, "local.js")); string(b) != "mirror-local" {
		t.Fatalf("local plugin lost after update: %q", b)
	}
}

func TestMirrorClientValidateCaches(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		writeJSON(w, http.StatusOK, mirrorValidateResp{OK: true, Type: "tg", TGID: 99})
	}))
	defer srv.Close()

	c := newMirrorClient(mirrorCfg(srv.URL))
	for i := 0; i < 3; i++ {
		resp, ok := c.validate("token", "abc")
		if !ok || resp.TGID != 99 {
			t.Fatalf("validate %d: ok=%v resp=%+v", i, ok, resp)
		}
	}
	if hits != 1 {
		t.Fatalf("expected 1 origin hit (cached), got %d", hits)
	}
}

func TestMirrorClientValidateGrace(t *testing.T) {
	// Origin always errors (500) → fetch fails.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := newMirrorClient(mirrorCfg(srv.URL))

	// Seed a stale-but-within-grace positive entry (3 min old; TTL 60s, grace 900s).
	c.valCache["t:tok"] = mirrorValEntry{
		resp: mirrorValidateResp{OK: true, TGID: 7},
		ok:   true,
		at:   time.Now().Add(-3 * time.Minute),
	}
	if resp, ok := c.validate("token", "tok"); !ok || resp.TGID != 7 {
		t.Fatalf("grace honor: ok=%v resp=%+v", ok, resp)
	}

	// Beyond grace → not honored.
	c.valCache["t:old"] = mirrorValEntry{
		resp: mirrorValidateResp{OK: true, TGID: 8},
		ok:   true,
		at:   time.Now().Add(-30 * time.Minute),
	}
	if _, ok := c.validate("token", "old"); ok {
		t.Fatalf("beyond grace should not be honored")
	}
}

func TestMirrorGateMiddlewareReplaysVerdict(t *testing.T) {
	var verdict mirrorGateVerdict
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, verdict)
	}))
	defer srv.Close()

	c := newMirrorClient(mirrorCfg(srv.URL))
	var nextCalled bool
	mw := mirrorGateMiddleware(c)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	}))

	// Allow + token → cookie set, next called.
	verdict = mirrorGateVerdict{Allow: true, SetToken: "T"}
	nextCalled = false
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/lite/events?uid=u1", nil)
	req.AddCookie(&http.Cookie{Name: "lampac_token", Value: "clienttok"})
	mw.ServeHTTP(rec, req)
	if !nextCalled {
		t.Fatalf("allow: next not called")
	}
	if !strings.Contains(strings.Join(rec.Header().Values("Set-Cookie"), ";"), "lampac_token=T") {
		t.Fatalf("allow: cookie not set: %v", rec.Header().Values("Set-Cookie"))
	}

	// Deny → body replayed, next NOT called. Use a fresh client to avoid the
	// allow cache from the previous case.
	c2 := newMirrorClient(mirrorCfg(srv.URL))
	mw2 := mirrorGateMiddleware(c2)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
	}))
	verdict = mirrorGateVerdict{Allow: false, Status: http.StatusOK, ContentType: "application/json",
		BodyB64: base64.StdEncoding.EncodeToString([]byte(`{"accsdb":true}`))}
	nextCalled = false
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/lite/events?uid=u2", nil)
	req.AddCookie(&http.Cookie{Name: "lampac_token", Value: "clienttok2"})
	mw2.ServeHTTP(rec, req)
	if nextCalled {
		t.Fatalf("deny: next should not be called")
	}
	if !strings.Contains(rec.Body.String(), `"accsdb":true`) {
		t.Fatalf("deny: body not replayed: %s", rec.Body.String())
	}
}
