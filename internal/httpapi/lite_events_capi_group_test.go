package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"lampac-go/internal/config"
	"lampac-go/internal/tgauth"
)

// TestCapiDrillIgnoresGroupDeny: a source kept out of a group's whitelist must
// still be visible to a /capi aggregation drill, so `resolve_sources` is the
// single authority for what alpac tv serves.
//
// This is what makes "premium-only in Lampa, available to everyone on TV"
// expressible: leave the source out of the group whitelist, list it in
// resolve_sources. It also aligns the two gates — liteSourceHandler already
// exempts capi drills from the same check, so before this the drill was let
// through at the route while its item never entered the list.
func TestCapiDrillIgnoresGroupDeny(t *testing.T) {
	dir := t.TempDir()
	groupStoreRef = tgauth.NewGroupStore(dir)
	tgTokenStoreRef = tgauth.NewStore(dir)
	t.Cleanup(func() {
		groupStoreRef = nil
		tgTokenStoreRef = nil
	})

	// Deny mirkino on the default group (what an anonymous / non-premium
	// request resolves to).
	def := groupStoreRef.GetDefault()
	def.Balancers = map[string]bool{"mirkino": false, "kinotochka": true}
	if err := groupStoreRef.Update(def); err != nil {
		t.Fatalf("update default group: %v", err)
	}

	cfg := config.Config{}
	newReq := func(capi bool) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/events?id=550", nil)
		if capi {
			r = r.WithContext(context.WithValue(r.Context(), capiResolveCtxKey{}, true))
		}
		return r
	}

	// Lampa path: the group deny still applies.
	if eventPluginVisible(newReq(false), cfg, "mirkino", "", false) {
		t.Error("group-denied source must stay hidden for a normal Lampa request")
	}
	// capi path: resolve_sources decides, not the per-user group.
	if !eventPluginVisible(newReq(true), cfg, "mirkino", "", false) {
		t.Error("group deny must NOT hide a source from a /capi drill")
	}

	// A source the group allows is unaffected on both paths.
	if !eventPluginVisible(newReq(false), cfg, "kinotochka", "", false) {
		t.Error("allowed source hidden for Lampa")
	}
	if !eventPluginVisible(newReq(true), cfg, "kinotochka", "", false) {
		t.Error("allowed source hidden for capi")
	}
}

// TestCapiDrillStillRespectsAdminDisable: loosening the group gate must not
// loosen the global kill switch — a balancer the admin turned off stays off
// everywhere, capi included.
func TestCapiDrillStillRespectsAdminDisable(t *testing.T) {
	dir := t.TempDir()
	groupStoreRef = tgauth.NewGroupStore(dir)
	tgTokenStoreRef = tgauth.NewStore(dir)
	t.Cleanup(func() {
		groupStoreRef = nil
		tgTokenStoreRef = nil
	})

	r := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/events?id=550", nil)
	r = r.WithContext(context.WithValue(r.Context(), capiResolveCtxKey{}, true))

	// filmixpro is gated on a token + pro mode; with neither it must stay hidden
	// even for a capi drill.
	if eventPluginVisible(r, config.Config{}, "filmixpro", "", false) {
		t.Error("filmixpro without token/pro must stay hidden for capi too")
	}
}
