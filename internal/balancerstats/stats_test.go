package balancerstats

import (
	"testing"
	"time"
)

func TestRecordAndSnapshot(t *testing.T) {
	m := NewManager()
	for i := 0; i < 10; i++ {
		m.Record("collaps", true, 100, 200, "", "api.example.com", "https://api.example.com/list")
	}
	for i := 0; i < 5; i++ {
		m.Record("collaps", false, 5000, 503, "service unavailable", "api.example.com", "https://api.example.com/embed/123")
	}

	snaps := m.Snapshot("")
	snap, ok := snaps["collaps"]
	if !ok {
		t.Fatal("expected snapshot for 'collaps'")
	}
	w := snap.Windows["1m"]
	if w.Total != 15 {
		t.Errorf("expected total=15, got %d", w.Total)
	}
	if w.Success != 10 {
		t.Errorf("expected success=10, got %d", w.Success)
	}
	if w.Failure != 5 {
		t.Errorf("expected failure=5, got %d", w.Failure)
	}
	if w.SuccessRate < 0.66 || w.SuccessRate > 0.67 {
		t.Errorf("expected success_rate ≈ 0.667, got %f", w.SuccessRate)
	}
	if len(snap.RecentErrs) == 0 {
		t.Error("expected at least one recent error")
	}
}

func TestFallbackRotation(t *testing.T) {
	m := NewManager()
	m.fallbackFailLimit = 3
	m.SetFallbacks("zetflix", []string{"primary.local", "fallback1.local", "fallback2.local"})

	if got := m.CurrentHost("zetflix"); got != "primary.local" {
		t.Fatalf("expected primary.local, got %q", got)
	}

	// Three consecutive failures on primary should rotate to fallback1.
	for i := 0; i < 3; i++ {
		m.Record("zetflix", false, 1000, 502, "bad gateway", "primary.local", "https://primary.local/x")
	}
	if got := m.CurrentHost("zetflix"); got != "fallback1.local" {
		t.Errorf("expected rotation to fallback1.local, got %q", got)
	}

	// Next 3 fails on fallback1 → fallback2.
	for i := 0; i < 3; i++ {
		m.Record("zetflix", false, 1000, 502, "bad gateway", "fallback1.local", "https://fallback1.local/x")
	}
	if got := m.CurrentHost("zetflix"); got != "fallback2.local" {
		t.Errorf("expected rotation to fallback2.local, got %q", got)
	}

	// A success on the active host should NOT rotate.
	m.Record("zetflix", true, 100, 200, "", "fallback2.local", "https://fallback2.local/x")
	if got := m.CurrentHost("zetflix"); got != "fallback2.local" {
		t.Errorf("expected to stay on fallback2.local after success, got %q", got)
	}
}

func TestFallbackRotationRecentSuccessBlocks(t *testing.T) {
	m := NewManager()
	m.fallbackFailLimit = 3
	m.SetFallbacks("collaps", []string{"a.local", "b.local"})

	m.Record("collaps", false, 100, 502, "x", "a.local", "https://a.local/y")
	m.Record("collaps", true, 100, 200, "", "a.local", "https://a.local/z")
	m.Record("collaps", false, 100, 502, "x", "a.local", "https://a.local/y")
	// Only 1 fail since last success on a.local — should not rotate.
	if got := m.CurrentHost("collaps"); got != "a.local" {
		t.Errorf("expected a.local (recent success blocks rotation), got %q", got)
	}
}

func TestPercentile(t *testing.T) {
	sorted := []int64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	if p := percentile(sorted, 0.50); p != 50 {
		t.Errorf("p50: expected 50, got %d", p)
	}
	// nearest-rank: idx = floor((n-1)*p) = floor(9*0.95) = 8 → sorted[8] = 90
	if p := percentile(sorted, 0.95); p != 90 {
		t.Errorf("p95: expected 90, got %d", p)
	}
	if p := percentile(nil, 0.5); p != 0 {
		t.Errorf("empty slice: expected 0, got %d", p)
	}
}

func TestSinceWindow(t *testing.T) {
	r := newBalancerRing(64)
	now := time.Now()
	r.attempts[0] = Attempt{Time: now.Add(-10 * time.Minute), Success: true, LatencyMs: 100}
	r.attempts[1] = Attempt{Time: now.Add(-3 * time.Minute), Success: true, LatencyMs: 200}
	r.attempts[2] = Attempt{Time: now.Add(-30 * time.Second), Success: false, LatencyMs: 5000}
	r.head = 3
	r.count = 3

	w1m := computeWindow(r.since(time.Minute))
	if w1m.Total != 1 {
		t.Errorf("1m window: expected 1, got %d", w1m.Total)
	}
	w5m := computeWindow(r.since(5 * time.Minute))
	if w5m.Total != 2 {
		t.Errorf("5m window: expected 2, got %d", w5m.Total)
	}
	w1h := computeWindow(r.since(time.Hour))
	if w1h.Total != 3 {
		t.Errorf("1h window: expected 3, got %d", w1h.Total)
	}
}
