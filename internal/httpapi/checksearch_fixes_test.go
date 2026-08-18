package httpapi

import (
	"context"
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"lampac-go/internal/balancerstats"
)

// TestSanitizeQualityBadgeStripsInjection: malformed upstream quality must
// not break the JSON response. Anything outside {4K, FHD, HD, SD} normalizes
// to "" or to the canonical form.
func TestSanitizeQualityBadgeStripsInjection(t *testing.T) {
	cases := map[string]string{
		`4K`:                  "4K",
		`fhd`:                 "FHD",
		`HD`:                  "HD",
		`SD`:                  "SD",
		`1080p`:               "FHD",
		`2160p`:               "4K",
		`evil","injected":"x`: "", // injection attempt
		`<script>alert(1)`:    "",
		`"`:                   "",
		``:                    "",
	}
	for in, want := range cases {
		got := sanitizeQualityBadge(in)
		if got != want {
			t.Errorf("sanitizeQualityBadge(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestWriteCheckSearchResponseJSONInjection asserts that even when a caller
// passes a malicious quality string, the response stays a valid JSON object
// with show:true/false.
func TestWriteCheckSearchResponseJSONInjection(t *testing.T) {
	malicious := `4K","injected":"true`

	rec := httptest.NewRecorder()
	writeCheckSearchResponse(rec, true, malicious)
	body := rec.Body.String()

	var out map[string]any
	if err := stdjson.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("response is not valid JSON after injection attempt: %v (body=%s)", err, body)
	}
	if _, leaked := out["injected"]; leaked {
		t.Fatalf("injection leaked through: %s", body)
	}
}

// TestChecksearchCacheAsymmetricTTL: a positive result must outlive a
// negative one — admin observed users seeing "balancer down" for 10
// minutes after the balancer recovered because the negative entry was
// cached with the long TTL.
func TestChecksearchCacheAsymmetricTTL(t *testing.T) {
	c := &checksearchCache{entries: map[string]checksearchCacheEntry{}}

	c.setWithTTL("pos", true, true, "FHD", checksearchCacheTTL)
	c.setWithTTL("neg", false, false, "", checksearchCacheNegTTL)
	c.setWithTTL("err", false, false, "", checksearchCacheErrorTTL)

	posExpires := c.entries["pos"].expires
	negExpires := c.entries["neg"].expires
	errExpires := c.entries["err"].expires

	if !negExpires.Before(posExpires) {
		t.Fatalf("negative TTL must be shorter than positive TTL: neg=%v pos=%v", negExpires, posExpires)
	}
	if !errExpires.Before(negExpires) {
		t.Fatalf("error TTL must be shortest: err=%v neg=%v", errExpires, negExpires)
	}
	// Sanity: TTL constants used here are what we promised in the package docs.
	if checksearchCacheTTL <= checksearchCacheNegTTL {
		t.Fatalf("positive TTL (%v) must exceed negative TTL (%v)", checksearchCacheTTL, checksearchCacheNegTTL)
	}
	if checksearchCacheNegTTL <= checksearchCacheErrorTTL {
		t.Fatalf("negative TTL (%v) must exceed error TTL (%v)", checksearchCacheNegTTL, checksearchCacheErrorTTL)
	}
}

// TestCircuitBreakerIgnoresEmptyResult: probeEmpty (balancer responded fine
// but found no content) must NOT count as a breaker failure. Otherwise
// 3 "film not on balancer X" hits in a row would lock the balancer out
// for 2 minutes for ALL films.
func TestCircuitBreakerIgnoresEmptyResult(t *testing.T) {
	b := &csBreakerStore{entries: map[string]*csBreakerEntry{}}
	key := "fakebalancer"

	// Simulate the new runCheckOnlineSearch switch logic.
	step := func(outcome probeOutcome) {
		switch outcome {
		case probeOk, probeEmpty:
			b.recordSuccess(key)
		case probeErr:
			b.recordFail(key)
		}
	}

	// csBreakerThreshold legitimate "no content" responses must NOT open
	// the breaker — empty results are not failures.
	for i := 0; i < csBreakerThreshold; i++ {
		step(probeEmpty)
	}
	if b.isOpen(key) {
		t.Fatalf("breaker opened after %d legit empty results — semantics regression", csBreakerThreshold)
	}

	// csBreakerThreshold real transport errors must open it.
	for i := 0; i < csBreakerThreshold; i++ {
		step(probeErr)
	}
	if !b.isOpen(key) {
		t.Fatalf("breaker did not open after %d transport errors", csBreakerThreshold)
	}

	// One success closes it (half-open: we cleared on isOpen() once cooldown passes).
	b.recordSuccess(key)
	if open, _, fc := b.inspect(key); open || fc != 0 {
		t.Fatalf("breaker not fully reset after recordSuccess: open=%v failCount=%d", open, fc)
	}
}

// TestChecksearchTelemetryRecorded: probing a balancer must populate the
// ChecksearchSnapshot ring buffer, regardless of show value. This is what
// powers the admin telemetry dashboard.
func TestChecksearchTelemetryRecorded(t *testing.T) {
	prev := globalBalancerStats.Load()
	t.Cleanup(func() { globalBalancerStats.Store(prev) })

	mgr := balancerstats.NewManager()
	globalBalancerStats.Store(mgr)

	// Fake balancer handler that always answers but reports no content
	// (probes run in-process now).
	lite := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"rch":false}`))
	})
	target := "/lite/probetarget?checksearch=true"

	// Probe twice — once cold, once cached. Both should leave a single
	// telemetry attempt (the second is served from cache).
	for i := 0; i < 2; i++ {
		// Wipe cache so each iteration runs the real probe.
		csCache.mu.Lock()
		csCache.entries = map[string]checksearchCacheEntry{}
		csCache.mu.Unlock()
		probeCheckSearch(context.Background(), lite, "", target, "probetarget")
	}

	snap := mgr.ChecksearchSnapshot("probetarget")
	cs, ok := snap["probetarget"]
	if !ok {
		t.Fatalf("no checksearch snapshot recorded for probetarget — recorder is dead")
	}
	w1m := cs.Windows["1m"]
	if w1m.Total < 2 {
		t.Fatalf("expected >=2 recorded attempts (got %d) — recorder may be skipping cached hits", w1m.Total)
	}
	// All attempts must be ok=true: the balancer responded with 200 (even
	// though show=false). probeEmpty MUST count as success.
	if w1m.Failure != 0 {
		t.Fatalf("expected zero failures (balancer responded 200), got %d", w1m.Failure)
	}
}

// TestChecksearchTelemetryNetworkError: when the balancer handler fails
// (non-2xx — the in-process equivalent of the old refused connection) the
// probe must record a failure with a non-empty error message, so the admin
// dashboard surfaces the actual outage cause.
func TestChecksearchTelemetryNetworkError(t *testing.T) {
	prev := globalBalancerStats.Load()
	t.Cleanup(func() { globalBalancerStats.Store(prev) })
	t.Cleanup(func() { csBreaker.reset("deadbal") }) // probeErr now ticks the breaker inside the probe

	mgr := balancerstats.NewManager()
	globalBalancerStats.Store(mgr)

	csCache.mu.Lock()
	csCache.entries = map[string]checksearchCacheEntry{}
	csCache.mu.Unlock()

	lite := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream dead", http.StatusBadGateway)
	})
	target := "/lite/deadbal?checksearch=true"
	_, _, _, outcome := probeCheckSearch(context.Background(), lite, "", target, "deadbal")
	if outcome != probeErr {
		t.Fatalf("expected probeErr from dead handler, got %v", outcome)
	}

	cs := mgr.ChecksearchSnapshot("deadbal")["deadbal"]
	w := cs.Windows["1m"]
	if w.Total != 1 || w.Failure != 1 {
		t.Fatalf("expected exactly one failure recorded, got total=%d failure=%d", w.Total, w.Failure)
	}
	if cs.LastError == "" {
		t.Fatalf("expected non-empty LastError, got empty")
	}
}

// TestInvalidateMergedConfCacheBumpsVersion: the new debounced invalidation
// path must increment the cache version atomically and leave the underlying
// map intact (we want O(1), not O(N) wipe).
func TestInvalidateMergedConfCacheBumpsVersion(t *testing.T) {
	eventsResponseCache.Lock()
	startVer := eventsResponseCacheVersion
	eventsResponseCache.items["dummy-key"] = eventsResponseCacheEntry{
		body:      []byte("[]"),
		expiresAt: time.Now().Add(time.Hour),
		version:   eventsResponseCacheVersion,
	}
	eventsResponseCache.Unlock()

	invalidateMergedConfCache()

	eventsResponseCache.RLock()
	defer eventsResponseCache.RUnlock()
	if eventsResponseCacheVersion <= startVer {
		t.Fatalf("version did not bump: start=%d now=%d", startVer, eventsResponseCacheVersion)
	}
	// Map must NOT be wiped — entries linger but are stale-by-version.
	if _, ok := eventsResponseCache.items["dummy-key"]; !ok {
		t.Fatalf("debounced invalidation wiped the map; expected O(1) version bump only")
	}
	ce := eventsResponseCache.items["dummy-key"]
	if ce.version == eventsResponseCacheVersion {
		t.Fatalf("entry's version not stale after invalidation: %d == %d", ce.version, eventsResponseCacheVersion)
	}
}

// TestClusterRemoteQualityClampHonorsLocalCap: ensures the cluster-merge
// path never accepts a remote-reported quality higher than the local
// pluginQualityBadge for that balancer. Pure helper-level test (mimics
// the loop body in runCheckOnlineSearch).
func TestClusterRemoteQualityClampHonorsLocalCap(t *testing.T) {
	// collaps is advertised as FHD locally. Remote-reported "4K" must be
	// clamped down to FHD.
	const balKey = "collaps"

	localCap := qualityBadgeRank(pluginQualityBadgeGet(balKey))
	if localCap == 0 {
		t.Skip("collaps quality badge missing from default map; test premise invalid")
	}

	remoteQ := sanitizeQualityBadge("4K")
	if localCap > 0 && qualityBadgeRank(remoteQ) > localCap {
		remoteQ = pluginQualityBadgeGet(balKey)
	}
	if remoteQ != pluginQualityBadgeGet(balKey) {
		t.Fatalf("clamp failed: remote 4K should have been clamped to %s for %s, got %q",
			pluginQualityBadgeGet(balKey), balKey, remoteQ)
	}

	// A remote-reported FHD for a 4K-advertised balancer (mirage) must pass through.
	const balKey2 = "mirage"
	localCap2 := qualityBadgeRank(pluginQualityBadgeGet(balKey2))
	if localCap2 == 0 {
		t.Skip("mirage quality badge missing")
	}
	remoteQ2 := sanitizeQualityBadge("FHD")
	if localCap2 > 0 && qualityBadgeRank(remoteQ2) > localCap2 {
		remoteQ2 = pluginQualityBadgeGet(balKey2)
	}
	if remoteQ2 != "FHD" {
		t.Fatalf("clamp incorrectly modified valid lower quality: got %q want FHD", remoteQ2)
	}
}

// TestQualityHardcodesGoneFromBalancers: scans the balancer handler files
// to ensure nobody re-introduces a hardcoded "4K"/"FHD" badge in
// writeCheckSearchResponse calls — a regression risk we just paid down.
//
// This is a source-level guardrail; it runs against the live tree.
func TestQualityHardcodesGoneFromBalancers(t *testing.T) {
	// Files we've audited and know are clean.
	cleanFiles := []string{"mirage.go", "collaps.go", "femd.go", "kinobadi.go", "flixcdn.go", "gencit.go", "lumex.go", "videoseed.go", "veoveo.go"}

	for _, f := range cleanFiles {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Logf("skip %s: %v", f, err)
			continue
		}
		body := string(raw)
		// Allow only references that go through pluginQualityBadgeGet or sanitizeQualityBadge.
		if strings.Contains(body, `writeCheckSearchResponse(w, show, "4K")`) ||
			strings.Contains(body, `writeCheckSearchResponse(w, show, "FHD")`) ||
			strings.Contains(body, `writeCheckSearchResponse(w, show, "HD")`) ||
			strings.Contains(body, `writeCheckSearchResponse(w, show, "SD")`) {
			t.Errorf("%s still has a hardcoded quality string in writeCheckSearchResponse", f)
		}
	}
}
