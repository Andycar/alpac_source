package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"lampac-go/internal/config"
	"lampac-go/internal/kit"
)

// TestDynamicSourceSurvivesKitWhitelist is a regression test for the bug where
// JS modules / custom balancers vanished from /lite/events for users who had a
// kit `_balancerVisibility` map in whitelist mode.
//
// The map auto-detects whitelist mode as soon as any entry is true, and then
// hides everything NOT in the map. A JS module installed AFTER the user saved
// their /bkit preferences is absent from the map, so it was buried — even
// though the admin enabled it and it works for users without a whitelist.
//
// Fix: dynamic sources (registered in dynRoutes) are opt-out, not opt-in — they
// stay visible unless the user explicitly set them to false.
func TestDynamicSourceSurvivesKitWhitelist(t *testing.T) {
	// Mirror the user's real config: a mixed whitelist with built-ins enabled,
	// one JS module explicitly disabled (uaflix), and other JS modules absent.
	vis := map[string]bool{
		"filmix":     true,
		"kinotochka": true,
		"rezka":      true,
		"uaflix":     false, // JS module the user explicitly turned OFF
		"animeon":    false,
	}
	visJSON, _ := stdjson.Marshal(vis)
	kitCfg := map[string]stdjson.RawMessage{
		"_balancerVisibility": stdjson.RawMessage(visJSON),
	}

	// Register JS modules: krasview/scts are NOT in the map (installed later),
	// uaflix is explicitly false.
	dyn := NewDynamicRouteRegistry()
	for _, name := range []string{"krasview", "scts", "uaflix"} {
		dyn.RegisterHandler(name, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	}

	cfg := config.Config{
		Online: config.OnlineConfig{
			WithSearch:        []string{"filmix", "kinotochka", "rezka"},
			CustomOrder:       true,
			CheckOnlineSearch: false,
		},
	}

	r := httptest.NewRequest("GET", "/lite/events", nil)
	r = r.WithContext(kit.WithConfig(r.Context(), kitCfg))

	plugins := resolveEventsPlugins(r, cfg, dyn)
	items := buildEventItems(r, cfg, plugins, "http://lampa", "", false, dyn)

	got := map[string]bool{}
	for _, it := range items {
		got[it.Balanser] = true
	}

	// JS modules absent from the whitelist must now be visible.
	if !got["krasview"] {
		t.Errorf("krasview (dynamic, unlisted) should be visible under whitelist mode; got %v", got)
	}
	if !got["scts"] {
		t.Errorf("scts (dynamic, unlisted) should be visible under whitelist mode; got %v", got)
	}
	// Explicitly-disabled JS module stays hidden (respects user's choice).
	if got["uaflix"] {
		t.Errorf("uaflix (dynamic, explicit false) must stay hidden; got %v", got)
	}
	// Whitelisted built-ins still visible.
	if !got["filmix"] || !got["kinotochka"] || !got["rezka"] {
		t.Errorf("whitelisted built-ins should be visible; got %v", got)
	}
}

// TestIRemuxKitVisibilityAlias covers the prod failure where iRemux stayed
// hidden for a whitelist-mode user even after enabling it: the /bkit page and
// PluginKeyFor store visibility under "iremux", but the events pipeline
// normalises the balancer to "remux", so a plain vis["remux"] lookup missed the
// user's "iremux" opt-in and whitelist mode buried it. The alias-aware check
// must honour any spelling.
func TestIRemuxKitVisibilityAlias(t *testing.T) {
	kitEventVisible := func(vis map[string]bool, plugin string) bool {
		visJSON, _ := stdjson.Marshal(vis)
		kitCfg := map[string]stdjson.RawMessage{"_balancerVisibility": stdjson.RawMessage(visJSON)}
		r := httptest.NewRequest("GET", "/lite/events", nil)
		r = r.WithContext(kit.WithConfig(r.Context(), kitCfg))
		return eventPluginVisible(r, config.Config{}, plugin, "", false)
	}

	// Whitelist mode; user enabled iRemux, stored under "iremux" (PluginKeyFor).
	// The canonical "remux" must be visible via the alias.
	if !kitEventVisible(map[string]bool{"filmix": true, "iremux": true}, "remux") {
		t.Errorf("remux should be visible when the user whitelisted 'iremux'")
	}
	// Symmetric: enabled under canonical "remux", queried as "remux".
	if !kitEventVisible(map[string]bool{"filmix": true, "remux": true}, "remux") {
		t.Errorf("remux should be visible when whitelisted under 'remux'")
	}
	// Whitelist mode but neither spelling enabled → still hidden (correct: the
	// user must opt in; this is why enabling iRemux in /bkit is required).
	if kitEventVisible(map[string]bool{"filmix": true}, "remux") {
		t.Errorf("remux must stay hidden when whitelist omits both spellings")
	}
	// No whitelist (blacklist/empty) → default visible.
	if !kitEventVisible(map[string]bool{"uaflix": false}, "remux") {
		t.Errorf("remux should be visible by default when no whitelist is active")
	}
}

// TestBuiltinWhitelistStillHidesUnlisted guards the unchanged half: a built-in
// balancer NOT in the user's whitelist map (and not a dynamic route) must still
// be hidden by whitelist mode.
func TestBuiltinWhitelistStillHidesUnlisted(t *testing.T) {
	vis := map[string]bool{"filmix": true}
	visJSON, _ := stdjson.Marshal(vis)
	kitCfg := map[string]stdjson.RawMessage{"_balancerVisibility": stdjson.RawMessage(visJSON)}

	cfg := config.Config{Online: config.OnlineConfig{WithSearch: []string{"filmix", "kinotochka"}, CustomOrder: true}}
	r := httptest.NewRequest("GET", "/lite/events", nil)
	r = r.WithContext(kit.WithConfig(r.Context(), kitCfg))

	// kinotochka is a built-in, not in the whitelist → hidden (isDynamic=false).
	if eventPluginVisible(r, cfg, "kinotochka", "", false) {
		t.Errorf("built-in kinotochka should be hidden by whitelist mode")
	}
	// Same key, but if it were dynamic → visible (opt-out semantics).
	if !eventPluginVisible(r, cfg, "kinotochka", "", true) {
		t.Errorf("dynamic source should not be hidden by whitelist auto-detect")
	}
}
