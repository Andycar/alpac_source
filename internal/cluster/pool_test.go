package cluster

import (
	"testing"
	"time"
)

// TestNodeRecordProbeRingBuffer: probe history is bounded by probeHistCap
// and only the most recent results are kept.
func TestNodeRecordProbeRingBuffer(t *testing.T) {
	n := &Node{probeHistCap: 5}
	// Push 7 outcomes — only the last 5 should survive.
	for i := 0; i < 7; i++ {
		// alternate true/false so we can tell window position.
		n.recordProbe(i%2 == 0)
	}
	if got := len(n.probeHist); got != 5 {
		t.Fatalf("history size: %d (want 5)", got)
	}
	// Last entry corresponds to i=6, which is true (6%2==0).
	if !n.probeHist[len(n.probeHist)-1] {
		t.Fatalf("last entry should be true")
	}
}

// TestNodeUptimePctEmptyHistoryIsOptimistic: with no probes recorded we
// default to 100% — better than reporting "0%" for a newly-added node.
func TestNodeUptimePctEmptyHistoryIsOptimistic(t *testing.T) {
	n := &Node{}
	if got := n.UptimePct(); got != 100.0 {
		t.Fatalf("empty history UptimePct = %.1f (want 100.0)", got)
	}
}

// TestNodeUptimePctMixedHistory: with 4 of 5 successes, UptimePct = 80%.
func TestNodeUptimePctMixedHistory(t *testing.T) {
	n := &Node{}
	for _, ok := range []bool{true, true, false, true, true} {
		n.recordProbe(ok)
	}
	got := n.UptimePct()
	if got < 79.9 || got > 80.1 {
		t.Fatalf("UptimePct = %.2f, want ~80.0", got)
	}
}

// TestNodeHealthyAtomicGetSet: the underlying atomic.Bool round-trips.
func TestNodeHealthyAtomicGetSet(t *testing.T) {
	n := &Node{}
	if n.IsHealthy() {
		t.Fatalf("zero-value Node should NOT be healthy")
	}
	n.healthy.Store(true)
	if !n.IsHealthy() {
		t.Fatalf("after Store(true), IsHealthy should be true")
	}
	n.healthy.Store(false)
	if n.IsHealthy() {
		t.Fatalf("after Store(false), IsHealthy should be false")
	}
}

// TestNodeUpdateEWMACoarseRanges sanity-checks the EWMA formula:
//   - first sample populates avgLatency exactly
//   - subsequent samples move toward the new value but not all the way
func TestNodeUpdateEWMACoarseRanges(t *testing.T) {
	n := &Node{}
	n.updateEWMA(100) // first sample → avg=100
	if got := n.AvgLatencyMs(); got != 100 {
		t.Fatalf("first sample avg=%d (want 100)", got)
	}
	n.updateEWMA(200) // EWMA: 0.8*100 + 0.2*200 = 120
	if got := n.AvgLatencyMs(); got != 120 {
		t.Fatalf("second sample avg=%d (want 120, EWMA α=0.2)", got)
	}
	// Drive several large samples and confirm the value rises but doesn't jump.
	for i := 0; i < 10; i++ {
		n.updateEWMA(500)
	}
	// After many samples toward 500, EWMA should be close to but below 500.
	got := n.AvgLatencyMs()
	if got < 400 || got > 500 {
		t.Fatalf("convergent EWMA toward 500: got %d (expect 400-500)", got)
	}
}

// TestNodeRecordRequestAggregatesByBalancer: per-balancer counters increment
// independently; client tracker registers fresh IPs.
func TestNodeRecordRequestAggregatesByBalancer(t *testing.T) {
	n := &Node{}
	n.RecordRequest("filmix", "1.1.1.1", 1024)
	n.RecordRequest("filmix", "1.1.1.1", 512)
	n.RecordRequest("collaps", "2.2.2.2", 2048)
	n.RecordRequest("filmix", "3.3.3.3", 0) // zero bytes still counts

	unique, byBal := n.statsSnapshot()
	if unique != 3 {
		t.Fatalf("unique clients: %d (want 3)", unique)
	}
	if byBal["filmix"] != 3 || byBal["collaps"] != 1 {
		t.Fatalf("byBalancer = %+v (want filmix=3, collaps=1)", byBal)
	}
	// Bytes out: 1024 + 512 + 2048 + 0 = 3584
	if got := n.bytesOut.Load(); got != 3584 {
		t.Fatalf("bytesOut = %d (want 3584)", got)
	}
}

// TestNodeSweepClientsExpiresOldEntries: clients last seen before the
// cutoff are removed so the unique-client tracker doesn't grow forever.
func TestNodeSweepClientsExpiresOldEntries(t *testing.T) {
	n := &Node{}

	// Manually seed the recentClients map with a mix of fresh + stale entries.
	now := time.Now()
	n.statsMu.Lock()
	n.recentClients = map[string]time.Time{
		"fresh":   now,
		"old":     now.Add(-2 * time.Hour),
		"ancient": now.Add(-24 * time.Hour),
	}
	n.statsMu.Unlock()

	n.sweepClients(now.Add(-1 * time.Hour))

	n.statsMu.RLock()
	left := len(n.recentClients)
	_, freshOK := n.recentClients["fresh"]
	_, oldOK := n.recentClients["old"]
	_, ancientOK := n.recentClients["ancient"]
	n.statsMu.RUnlock()

	if left != 1 || !freshOK || oldOK || ancientOK {
		t.Fatalf("sweep result: left=%d fresh=%v old=%v ancient=%v",
			left, freshOK, oldOK, ancientOK)
	}
}
