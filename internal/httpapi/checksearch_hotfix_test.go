package httpapi

import (
	"context"
	stdjson "encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestSlowSuccessfulProbeSurfacesContent locks in the 2026-05-20 fix: when a
// balancer takes longer than the (former) slow-threshold to respond AND
// returns a positive payload, the content MUST reach the user. Earlier the
// branch hid the content + wrote a negative-cache entry, causing user reports
// of "sources don't show even though they have the film".
func TestSlowSuccessfulProbeSurfacesContent(t *testing.T) {
	// Reset csCache so this test's URL is uncontaminated by prior cases.
	csCache.mu.Lock()
	csCache.entries = map[string]checksearchCacheEntry{}
	csCache.mu.Unlock()
	csBreaker.reset("slowbal")

	// Balancer handler that intentionally dawdles — past the old slow threshold
	// — but answers with a real positive movie hit. Probes are in-process now,
	// so the "upstream" is just a handler func.
	lite := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(750 * time.Millisecond) // keep test fast; logic doesn't read latency anymore
		_, _ = w.Write([]byte(`{"type":"movie","rch":true,"quality":"FHD"}`))
	})

	show, rch, quality, outcome := probeCheckSearch(context.Background(), lite, "", "/lite/slowbal?checksearch=true", "slowbal")

	if !show {
		t.Fatalf("slow but positive probe must surface show=true; got false")
	}
	if !rch {
		t.Fatalf("slow but positive probe must surface rch=true; got false")
	}
	if quality != "FHD" {
		t.Fatalf("quality lost on slow positive: got %q", quality)
	}
	if outcome != probeOk {
		t.Fatalf("slow positive must be probeOk (not probeErr); got %v", outcome)
	}

	// Cache should hold the positive — next caller within TTL gets it immediately.
	csCache.mu.RLock()
	defer csCache.mu.RUnlock()
	hit := false
	for _, e := range csCache.entries {
		if e.show && e.quality == "FHD" {
			hit = true
			break
		}
	}
	if !hit {
		t.Fatalf("slow positive was NOT cached as positive — would re-probe every time")
	}
}

// TestPerBalancerTimeoutOverrideEmpty pins the regression: the empty map
// guards against the 3s overrides for fancdn/videoseed/getstv/moonanime/videodb
// being silently re-added. The default 10s applies to everyone.
func TestPerBalancerTimeoutOverrideEmpty(t *testing.T) {
	if len(checksearchTimeoutOverride) != 0 {
		t.Fatalf("checksearchTimeoutOverride must stay empty after hotfix; got %d entries: %v",
			len(checksearchTimeoutOverride), checksearchTimeoutOverride)
	}
	if got := checksearchTimeout("fancdn"); got != checksearchDefaultTimeout {
		t.Fatalf("fancdn must use default %s; got %s", checksearchDefaultTimeout, got)
	}
}

// TestChecksearchErrorTTLIsShort: keep error TTL at 10s so a single transient
// hiccup doesn't lock the source out for half a minute.
func TestChecksearchErrorTTLIsShort(t *testing.T) {
	if checksearchCacheErrorTTL > 15*time.Second {
		t.Fatalf("error TTL too long (%s) — single hiccup will hide source from user", checksearchCacheErrorTTL)
	}
	if checksearchCacheErrorTTL < 5*time.Second {
		t.Fatalf("error TTL too short (%s) — pointless cache; we'll re-probe immediately every call", checksearchCacheErrorTTL)
	}
}

// TestSlowPositiveDoesNotTripBreaker: also check the breaker doesn't open on
// a sequence of slow-but-successful probes. Otherwise the cumulative effect
// would be the same as the old bug (source disappears).
func TestSlowPositiveDoesNotTripBreaker(t *testing.T) {
	csBreaker.reset("ballin")
	csCache.mu.Lock()
	csCache.entries = map[string]checksearchCacheEntry{}
	csCache.mu.Unlock()

	var hits int32
	lite := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		time.Sleep(500 * time.Millisecond) // not really "slow" but exercises the path
		_, _ = w.Write([]byte(`{"type":"movie","rch":true}`))
	})

	for i := 0; i < 6; i++ {
		// Different target each time so csCache doesn't short-circuit.
		target := "/lite/ballin?checksearch=true&id=" + string(rune('a'+i))
		_, _, _, _ = probeCheckSearch(context.Background(), lite, "", target, "ballin")
	}

	if open, _, fc := csBreaker.inspect("ballin"); open || fc > 0 {
		t.Fatalf("slow-but-positive probes should NOT tick breaker; got open=%v failCount=%d", open, fc)
	}
	if got := atomic.LoadInt32(&hits); got < 6 {
		t.Fatalf("expected ≥6 backend hits; got %d (cache/breaker may have suppressed)", got)
	}
}

// TestEvaluateSearchResultStillParsesQualityFromJSON sanity-pins the
// underlying parser — we want to make sure the regression fix didn't
// accidentally regress the parse path.
func TestEvaluateSearchResultStillParsesQualityFromJSON(t *testing.T) {
	body := `{"type":"movie","rch":true,"quality":"4K"}`
	s, r, q := evaluateSearchResult(body)
	if !s || !r || q != "4K" {
		t.Fatalf("evaluateSearchResult regression: s=%v r=%v q=%q", s, r, q)
	}
	var raw map[string]any
	_ = stdjson.Unmarshal([]byte(body), &raw) // not strictly needed; uses regex inside
	_ = strings.Contains
}
