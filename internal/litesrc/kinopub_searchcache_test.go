package litesrc

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"
)

func resetKPSearchCache() {
	kpSearchMu.Lock()
	kpSearchCache = map[string]kpSearchEntry{}
	kpSearchMu.Unlock()
}

// The whole point: the same title asked twice must cost one upstream call. Prod 2026-08-27 was
// asking «Холод» 37 times an hour, and that volume is what earns the 429/403 throttling.
func TestSearchCacheServesRepeatQueries(t *testing.T) {
	t.Cleanup(resetKPSearchCache)
	resetKPSearchCache()

	kpSearchStore(kpSearchKey("Холод"), []kinoPubItem{{ID: 1}}, true)

	e, hit := kpSearchLookup(kpSearchKey("  холод  "))
	if !hit {
		t.Fatal("a repeat of the same title missed the cache")
	}
	if len(e.items) != 1 || e.items[0].ID != 1 {
		t.Fatalf("cache returned %+v, want the stored item", e.items)
	}
	if !e.ok {
		t.Fatal("the stored success flag was lost")
	}
}

// A miss must be remembered for far less time than a hit: an empty answer is usually the
// throttling talking, not a real absence, and pinning it for ten minutes would hide a title
// that is actually there.
func TestMissExpiresSoonerThanHit(t *testing.T) {
	t.Cleanup(resetKPSearchCache)
	resetKPSearchCache()

	if kpSearchNegTTL >= kpSearchTTL {
		t.Fatalf("negative TTL %v must be shorter than the positive one %v", kpSearchNegTTL, kpSearchTTL)
	}

	kpSearchMu.Lock()
	kpSearchCache["hit"] = kpSearchEntry{items: []kinoPubItem{{ID: 1}}, ok: true, at: time.Now().Add(-kpSearchNegTTL - time.Second)}
	kpSearchCache["miss"] = kpSearchEntry{items: nil, ok: true, at: time.Now().Add(-kpSearchNegTTL - time.Second)}
	kpSearchMu.Unlock()

	if _, hit := kpSearchLookup("hit"); !hit {
		t.Fatal("a real result expired at the negative TTL")
	}
	if _, hit := kpSearchLookup("miss"); hit {
		t.Fatal("an empty result outlived the negative TTL")
	}
}

// The cache must not grow without bound.
func TestSearchCacheIsBounded(t *testing.T) {
	t.Cleanup(resetKPSearchCache)
	resetKPSearchCache()
	for i := 0; i < kpSearchMaxKeys+50; i++ {
		kpSearchStore(string(rune(i))+"q", []kinoPubItem{{ID: i}}, true)
	}
	kpSearchMu.Lock()
	n := len(kpSearchCache)
	kpSearchMu.Unlock()
	if n > kpSearchMaxKeys {
		t.Fatalf("cache holds %d entries, above the %d cap", n, kpSearchMaxKeys)
	}
}

// The fallback endpoint must not be able to spend the caller's whole budget. v1.1 nodes accept
// the connection and then never answer; without its own deadline that stalls the entire resolve.
func TestFallbackRequestCarriesItsOwnDeadline(t *testing.T) {
	req := httptest.NewRequest("GET", "/x", nil)
	bounded, cancel := kpBoundedReq(req, kpFallbackBudget)
	defer cancel()

	dl, ok := bounded.Context().Deadline()
	if !ok {
		t.Fatal("the fallback request carries no deadline")
	}
	if d := time.Until(dl); d > kpFallbackBudget+time.Second {
		t.Fatalf("fallback deadline is %v away, want at most %v", d, kpFallbackBudget)
	}
	if kpFallbackBudget >= 10*time.Second {
		t.Fatalf("fallback budget %v is too generous to be a fallback", kpFallbackBudget)
	}
}

// A caller's own deadline must still win when it is shorter than the fallback budget.
func TestFallbackNeverExtendsTheCallersDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest("GET", "/x", nil).WithContext(ctx)

	bounded, cancel2 := kpBoundedReq(req, kpFallbackBudget)
	defer cancel2()
	dl, _ := bounded.Context().Deadline()
	if time.Until(dl) > time.Second {
		t.Fatal("the fallback budget extended a caller deadline that was shorter")
	}
}
