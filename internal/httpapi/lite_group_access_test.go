package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/kit"
	"lampac-go/internal/tgauth"
)

// TestAnonymousRequestRespectsDefaultGroupBalancerDeny is a regression test for
// the group access bypass: a request without a lampac_token cookie used to skip
// the group check entirely (resolveUserGroup returned nil → both the events
// filter and the direct /lite/<balancer> handler treated the request as
// unrestricted). After the fix, anonymous requests are evaluated against the
// default group, so an admin can deny a balancer for "anonymous" users by
// denying it on the default group.
func TestAnonymousRequestRespectsDefaultGroupBalancerDeny(t *testing.T) {
	dir := t.TempDir()
	groupStoreRef = tgauth.NewGroupStore(dir)
	tgTokenStoreRef = tgauth.NewStore(dir)
	t.Cleanup(func() {
		groupStoreRef = nil
		tgTokenStoreRef = nil
	})

	// Deny kinotochka on the default group.
	def := groupStoreRef.GetDefault()
	def.Balancers = map[string]bool{"kinotochka": false}
	if err := groupStoreRef.Update(def); err != nil {
		t.Fatalf("update default group: %v", err)
	}

	cfg := config.Config{
		Online: config.OnlineConfig{WithSearch: []string{"kinotochka", "filmix"}},
	}

	// Direct /lite/kinotochka — anonymous request (no cookie) must be blocked.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/kinotochka?id=550", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)
	body := strings.TrimSpace(rec.Body.String())
	if body != "{}" {
		t.Fatalf("anonymous /lite/kinotochka should be blocked (empty {}), got: %q", body)
	}

	// /lite/events — anonymous request should not see kinotochka in the list.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/events?id=550&original_language=ru", nil)
	liteEventsHandler(cfg, nil, nil).ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), `"kinotochka"`) {
		t.Fatalf("anonymous events list must not include denied balancer; body: %s", rec.Body.String())
	}
}

// TestAdminDisabledBalancerNotReachableDirectly is a regression test for the
// admin-disable bypass: isBalancerDisabled() used to be gated on checksearch=true
// in lite_sources.go, so a user could hit /lite/<disabled_bal>?id=... directly
// (e.g. after force-enabling it via kit _balancerVisibility=true) and the
// handler would run despite admin disable. Fix: enforce isBalancerDisabled
// unconditionally on direct routes.
func TestAdminDisabledBalancerNotReachableDirectly(t *testing.T) {
	// Inject a merged-conf snapshot that marks kinotochka as disabled.
	mergedConfCacheMu.Lock()
	prevCache := mergedConfCache
	prevTime := mergedConfCacheTime
	mergedConfCache = map[string]any{
		"Kinotochka": map[string]any{"enable": false},
	}
	mergedConfCacheTime = time.Now()
	mergedConfCacheMu.Unlock()
	t.Cleanup(func() {
		mergedConfCacheMu.Lock()
		mergedConfCache = prevCache
		mergedConfCacheTime = prevTime
		mergedConfCacheMu.Unlock()
	})

	cfg := config.Config{
		Online: config.OnlineConfig{WithSearch: []string{"kinotochka"}},
	}

	// Direct /lite/kinotochka without checksearch must NOT run the handler.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/kinotochka?id=550", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)
	body := strings.TrimSpace(rec.Body.String())
	if body != "{}" {
		t.Fatalf("admin-disabled /lite/kinotochka should return {}, got: %q", body)
	}

	// And checksearch=true still returns the rch:false signal so the client
	// doesn't show the balancer as available.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/kinotochka?checksearch=true&id=550", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"rch":false`) {
		t.Fatalf("checksearch on admin-disabled balancer should signal rch:false, got: %s", rec.Body.String())
	}
}

// TestGlobalDisableBeatsUserKitEnable enforces the product rule: a source the
// admin globally disabled must NOT appear in search, even when the user enables
// it in their personal kit visibility. The kit-additive path in
// resolveEventsPlugins (which surfaces user-enabled sources the admin omitted
// from with_search) must yield to the global disable.
func TestGlobalDisableBeatsUserKitEnable(t *testing.T) {
	// Admin-disable kinotochka globally (section keyed by PascalCase config name).
	mergedConfCacheMu.Lock()
	prevCache := mergedConfCache
	prevTime := mergedConfCacheTime
	mergedConfCache = map[string]any{
		"Kinotochka": map[string]any{"enable": false},
	}
	mergedConfCacheTime = time.Now()
	mergedConfCacheMu.Unlock()
	t.Cleanup(func() {
		mergedConfCacheMu.Lock()
		mergedConfCache = prevCache
		mergedConfCacheTime = prevTime
		mergedConfCacheMu.Unlock()
	})

	// User personally enables kinotochka in whitelist mode; the admin omitted it
	// from with_search, so it can only enter the list via the kit-additive path.
	vis := map[string]bool{"filmix": true, "kinotochka": true}
	visJSON, _ := stdjson.Marshal(vis)
	kitCfg := map[string]stdjson.RawMessage{"_balancerVisibility": stdjson.RawMessage(visJSON)}

	cfg := config.Config{Online: config.OnlineConfig{WithSearch: []string{"filmix"}, CustomOrder: true}}
	r := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/events?id=550", nil)
	r = r.WithContext(kit.WithConfig(r.Context(), kitCfg))

	// The resolved plugin list must not surface the globally-disabled source.
	for _, p := range resolveEventsPlugins(r, cfg, nil) {
		if p == "kinotochka" {
			t.Fatalf("globally-disabled kinotochka leaked into resolveEventsPlugins despite kit-enable")
		}
	}
	// And the rendered list the client actually sees must exclude it too.
	items := buildEventItems(r, cfg, resolveEventsPlugins(r, cfg, nil), "http://lampa", "", false, nil)
	for _, it := range items {
		if it.Balanser == "kinotochka" {
			t.Fatalf("globally-disabled kinotochka leaked into events list despite kit-enable")
		}
	}
}

// TestAliasedBalancerDisableIsEnforced is a regression test for the iRemux
// global-disable bypass. The events pipeline canonicalises "iremux" → "remux"
// (balancerNameAliases), but the config section is "iRemux" and PluginKeyFor
// maps it to "iremux". isBalancerDisabled() used to match on PluginKeyFor alone,
// so a disabled iRemux slipped through when queried by its canonical key
// ("remux") — it stayed in /lite/events AND served /lite/remux directly, which
// is exactly the "globally disabled but still searches" report. isBalancerDisabled
// now canonicalises both sides, so every spelling of an aliased balancer is gated.
func TestAliasedBalancerDisableIsEnforced(t *testing.T) {
	mergedConfCacheMu.Lock()
	prevCache := mergedConfCache
	prevTime := mergedConfCacheTime
	// Admin-disabled iRemux — section keyed by the PascalCase config name.
	mergedConfCache = map[string]any{
		"iRemux": map[string]any{"enable": false},
	}
	mergedConfCacheTime = time.Now()
	mergedConfCacheMu.Unlock()
	t.Cleanup(func() {
		mergedConfCacheMu.Lock()
		mergedConfCache = prevCache
		mergedConfCacheTime = prevTime
		mergedConfCacheMu.Unlock()
	})

	// Every spelling the request pipeline might use must read as disabled.
	for _, spelling := range []string{"remux", "iremux", "iRemux"} {
		if !isBalancerDisabled(spelling) {
			t.Errorf("isBalancerDisabled(%q) = false, want true (iRemux is globally disabled)", spelling)
		}
	}

	// A different, non-disabled balancer must stay enabled.
	if isBalancerDisabled("kinopub") {
		t.Errorf("isBalancerDisabled(\"kinopub\") = true, want false (only iRemux was disabled)")
	}
}

// TestResolveUserGroupAcceptsURLParamAndAltCookie is a regression test for a
// regression introduced when resolveUserGroup started falling back to the
// default group on missing lampac_token cookie: TV / Android Lampa clients
// that can't keep cross-origin cookies pass the token via "?token=" URL param
// or via the HttpOnly "_lampac_auth" cookie (see auth_tg_api.go gate). Both
// must resolve to the user's real group, not the default.
func TestResolveUserGroupAcceptsURLParamAndAltCookie(t *testing.T) {
	dir := t.TempDir()
	groupStoreRef = tgauth.NewGroupStore(dir)
	tgTokenStoreRef = tgauth.NewStore(dir)
	t.Cleanup(func() {
		groupStoreRef = nil
		tgTokenStoreRef = nil
	})

	// Premium group: kinotochka explicitly allowed (true) so the user
	// can play it even if the default group denies it.
	premium := tgauth.UserGroup{
		ID:        "premium",
		Name:      "Premium",
		Balancers: map[string]bool{"kinotochka": true},
	}
	if err := groupStoreRef.Create(premium); err != nil {
		t.Fatalf("create premium group: %v", err)
	}

	// Default group denies kinotochka. With my earlier fix, requests with NO
	// recognized token fall back to this group. We need to make sure that
	// requests carrying the premium user's token via ?token= or _lampac_auth
	// still get the premium group.
	def := groupStoreRef.GetDefault()
	def.Balancers = map[string]bool{"kinotochka": false}
	if err := groupStoreRef.Update(def); err != nil {
		t.Fatalf("update default group: %v", err)
	}

	premiumToken := "tv-premium-token"
	if err := tgTokenStoreRef.Add(tgauth.ApprovedToken{
		Token:      premiumToken,
		TelegramID: 1234567,
		GroupID:    "premium",
		ExpiresAt:  time.Now().Add(24 * time.Hour),
	}); err != nil {
		t.Fatalf("add premium token: %v", err)
	}

	// Sanity: a request with NO token uses default → denies kinotochka.
	anon := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/kinotochka", nil)
	if g := resolveUserGroup(anon); g == nil || g.BalancerAllowed("kinotochka") {
		t.Fatalf("anonymous should fall back to default group (kinotochka denied), got: %+v", g)
	}

	// Token via ?token= URL param — must resolve to premium → allows kinotochka.
	tvURL := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/kinotochka?token="+premiumToken, nil)
	if g := resolveUserGroup(tvURL); g == nil || g.ID != "premium" || !g.BalancerAllowed("kinotochka") {
		t.Fatalf("token via ?token= URL param should resolve to premium group, got: %+v", g)
	}

	// Token via _lampac_auth cookie — must resolve to premium.
	tvCookie := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/kinotochka", nil)
	tvCookie.AddCookie(&http.Cookie{Name: "_lampac_auth", Value: premiumToken})
	if g := resolveUserGroup(tvCookie); g == nil || g.ID != "premium" || !g.BalancerAllowed("kinotochka") {
		t.Fatalf("token via _lampac_auth cookie should resolve to premium group, got: %+v", g)
	}

	// Token via lampac_token cookie — must resolve to premium (regression).
	tvLegacy := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/kinotochka", nil)
	tvLegacy.AddCookie(&http.Cookie{Name: "lampac_token", Value: premiumToken})
	if g := resolveUserGroup(tvLegacy); g == nil || g.ID != "premium" || !g.BalancerAllowed("kinotochka") {
		t.Fatalf("token via lampac_token cookie should resolve to premium group, got: %+v", g)
	}
}

// TestKitConfigSaveDropsForbiddenVisibility is a regression test for the
// defense-in-depth sanitize step on /api/kit/config save. A user POSTing
// _balancerVisibility: {kinotochka: true, filmix: true, rezka: true} where the
// default group denies kinotochka and admin disabled rezka should land in
// storage with only filmix kept.
func TestKitConfigSaveDropsForbiddenVisibility(t *testing.T) {
	dir := t.TempDir()
	groupStoreRef = tgauth.NewGroupStore(dir)
	tgTokenStoreRef = tgauth.NewStore(dir)
	t.Cleanup(func() {
		groupStoreRef = nil
		tgTokenStoreRef = nil
	})

	// Default group denies kinotochka.
	def := groupStoreRef.GetDefault()
	def.Balancers = map[string]bool{"kinotochka": false}
	if err := groupStoreRef.Update(def); err != nil {
		t.Fatalf("update default group: %v", err)
	}

	// Admin-disable rezka via the merged-conf cache.
	mergedConfCacheMu.Lock()
	prevCache := mergedConfCache
	prevTime := mergedConfCacheTime
	mergedConfCache = map[string]any{
		"Rezka": map[string]any{"enable": false},
	}
	mergedConfCacheTime = time.Now()
	mergedConfCacheMu.Unlock()
	t.Cleanup(func() {
		mergedConfCacheMu.Lock()
		mergedConfCache = prevCache
		mergedConfCacheTime = prevTime
		mergedConfCacheMu.Unlock()
	})

	in := []byte(`{"_balancerVisibility":{"kinotochka":true,"filmix":true,"rezka":true,"vokino":false}}`)
	req := httptest.NewRequest(http.MethodPost, "http://lampac.local/api/kit/config", strings.NewReader(""))
	out := sanitizeKitBalancerVisibility(req, "0", in)

	var root map[string]stdjson.RawMessage
	if err := stdjson.Unmarshal(out, &root); err != nil {
		t.Fatalf("invalid sanitized json: %v", err)
	}
	var vis map[string]bool
	if err := stdjson.Unmarshal(root["_balancerVisibility"], &vis); err != nil {
		t.Fatalf("invalid _balancerVisibility: %v", err)
	}

	if _, ok := vis["kinotochka"]; ok {
		t.Errorf("group-denied kinotochka must be stripped, got: %v", vis)
	}
	if _, ok := vis["rezka"]; ok {
		t.Errorf("admin-disabled rezka must be stripped, got: %v", vis)
	}
	if v, ok := vis["filmix"]; !ok || !v {
		t.Errorf("filmix should be preserved as true, got: %v", vis)
	}
	// An explicit false (vokino) is harmless — it just hides — so we keep it.
	if v, ok := vis["vokino"]; !ok || v {
		t.Errorf("explicit-false vokino should be preserved, got: %v", vis)
	}
}
