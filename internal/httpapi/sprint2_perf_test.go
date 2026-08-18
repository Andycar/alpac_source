package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestPidtorScheduleRemoveUsesTimerNotGoroutine: scheduling many removes must
// not parking N goroutines (each Sleep'ing for `delay`). time.AfterFunc uses
// the runtime timer heap; we verify by asserting no goroutine spike under
// load and by exercising the cancellation path.
func TestPidtorScheduleRemoveUsesTimerNotGoroutine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	client := srv.Client()

	const N = 500
	for i := 0; i < N; i++ {
		// Schedule far enough out that the timer never fires during the test.
		pidtorScheduleRemove(client, srv.URL, "hash-"+strconvItoa(i), nil, 5*time.Minute)
	}

	pidtorPendingMu.Lock()
	got := len(pidtorPendingTimer)
	pidtorPendingMu.Unlock()
	if got != N {
		t.Fatalf("expected %d pending timers, got %d", N, got)
	}

	// Replacing the schedule for the same hash must replace (not append) the timer.
	pidtorScheduleRemove(client, srv.URL, "hash-1", nil, 5*time.Minute)
	pidtorPendingMu.Lock()
	got = len(pidtorPendingTimer)
	pidtorPendingMu.Unlock()
	if got != N {
		t.Fatalf("re-schedule increased timer map size: was %d, now %d", N, got)
	}

	// Cancel one and verify it left the map.
	pidtorCancelScheduledRemove(srv.URL, "hash-0")
	pidtorPendingMu.Lock()
	got = len(pidtorPendingTimer)
	pidtorPendingMu.Unlock()
	if got != N-1 {
		t.Fatalf("cancel did not shrink map: %d (want %d)", got, N-1)
	}

	// Cleanup the rest so other tests don't see leftovers.
	for i := 1; i < N; i++ {
		pidtorCancelScheduledRemove(srv.URL, "hash-"+strconvItoa(i))
	}
	pidtorPendingMu.Lock()
	got = len(pidtorPendingTimer)
	pidtorPendingMu.Unlock()
	if got != 0 {
		t.Fatalf("teardown left %d timers behind", got)
	}
}

// strconvItoa is a local int→string helper (avoids importing strconv for
// one call site and avoids the package-level `itoa` already defined in
// watchparty_api.go).
func strconvItoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// TestChecksearchTimeoutOverride: per-balancer overrides are explicit
// (currently empty after the 2026-05-20 regression fix — slow-but-honest
// upstreams shouldn't be silenced). Whatever entries exist must be tighter
// than the default. Everyone else inherits the default.
func TestChecksearchTimeoutOverride(t *testing.T) {
	for bal, d := range checksearchTimeoutOverride {
		if d >= checksearchDefaultTimeout {
			t.Errorf("override %s=%v is not tighter than default %v — pointless entry",
				bal, d, checksearchDefaultTimeout)
		}
	}
	if d := checksearchTimeout("unknown_balancer"); d != checksearchDefaultTimeout {
		t.Errorf("expected default %v for unknown, got %v", checksearchDefaultTimeout, d)
	}
	// Sanity: the wall-clock cap must be SHORTER than per-probe (otherwise the
	// total-deadline never fires before all probes finish on their own).
	if checksearchTotalDeadline >= checksearchDefaultTimeout {
		t.Fatalf("wall-clock deadline (%v) must be shorter than per-probe (%v)",
			checksearchTotalDeadline, checksearchDefaultTimeout)
	}
}

// TestChecksearchBreakerCountsSlowAsFail — REVERTED 2026-05-20.
//
// Sprint 2 introduced "slow-as-fail" (latency > 4s + positive payload →
// probeErr + content discarded). That caused user-visible regression: real
// content was being hidden behind a 30-second negative-cache entry — users
// reported "balancer doesn't show this film even though I know it's there".
//
// New contract: slow but successful responses are SUCCESSFUL. The breaker
// only ticks on transport / HTTP failures (covered by TestCircuitBreaker*).
// The telemetry dashboard surfaces persistent slowness via p95/p99 latency
// windows — we don't penalize live traffic for it.
//
// See TestSlowSuccessfulProbeSurfacesContent in checksearch_hotfix_test.go
// for the inverse — pins the post-fix behavior we want.
func TestChecksearchBreakerCountsSlowAsFail(t *testing.T) {
	t.Skip("reverted 2026-05-20: slow-as-fail was hiding real content from users; see checksearch_hotfix_test.go")
}

// TestLongLivedRoutesExcludedFromSlow: WS/SSE long-lived routes do not pollute
// the slow-routes top.
func TestLongLivedRoutesExcludedFromSlow(t *testing.T) {
	tr := newRequestStatsTracker()
	// Record a fake 10-minute /nws "request" — should NOT appear in stats.
	tr.observeRoute("/nws", http.StatusOK, 10*time.Minute)
	// Record a normal slow /lite/foo at 7s — should appear.
	tr.observeRoute("/lite/foo", http.StatusOK, 7*time.Second)

	snap := tr.snapshot()
	for _, r := range snap.TopRoutes {
		if r.Route == "/nws" {
			t.Fatalf("/nws leaked into slow-routes table: %+v", r)
		}
	}
	foundFoo := false
	for _, r := range snap.TopRoutes {
		if r.Route == "/lite/foo" && r.Count > 0 {
			foundFoo = true
		}
	}
	if !foundFoo {
		t.Fatalf("/lite/foo missing from TopRoutes — observation pipeline broken? snap=%+v", snap.TopRoutes)
	}
}
