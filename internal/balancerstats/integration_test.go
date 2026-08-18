package balancerstats

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// stubNotifier captures alert messages for assertions.
type stubNotifier struct {
	count atomic.Int32
	last  atomic.Value // string
}

func (s *stubNotifier) NotifyAdmins(text string) {
	s.count.Add(1)
	s.last.Store(text)
}

func TestAlertEngineFiresOnDegradation(t *testing.T) {
	mgr := NewManager()
	notif := &stubNotifier{}
	cfg := DefaultAlertConfig()
	cfg.Window = time.Minute // we'll record with current timestamps; 1m covers them
	cfg.MinAttempts = 5
	cfg.FailRateThreshold = 0.50
	cfg.Cooldown = 1 * time.Second
	cfg.CheckInterval = 50 * time.Millisecond
	cfg.RecoveryHysteresis = 0.80

	eng := NewAlertEngine(cfg, mgr, notif)

	// Manually call evaluate (bypasses warmup) — start a controlled context.
	// 8 attempts, 6 failures → 25% success → below threshold.
	for i := 0; i < 6; i++ {
		mgr.Record("collaps", false, 1000, 502, "bad gateway", "primary.local", "https://primary.local/x")
	}
	for i := 0; i < 2; i++ {
		mgr.Record("collaps", true, 100, 200, "", "primary.local", "https://primary.local/x")
	}

	eng.evaluate()
	if notif.count.Load() != 1 {
		t.Fatalf("expected 1 alert after degradation, got %d", notif.count.Load())
	}
	got, _ := notif.last.Load().(string)
	if got == "" {
		t.Error("expected non-empty alert message")
	}

	// Calling evaluate again immediately should NOT re-fire (still active).
	eng.evaluate()
	if notif.count.Load() != 1 {
		t.Errorf("expected no re-fire while active, got %d", notif.count.Load())
	}

	// Now record many successes — recovery. Need enough to push success rate
	// above RecoveryHysteresis (0.80) given the 6 prior failures still in the
	// window. 6 fails + N success → success_rate = N/(N+6) ≥ 0.80 → N ≥ 24.
	for i := 0; i < 30; i++ {
		mgr.Record("collaps", true, 100, 200, "", "primary.local", "https://primary.local/x")
	}
	eng.evaluate()
	if notif.count.Load() != 2 {
		t.Errorf("expected 2 alerts (degraded + recovered), got %d", notif.count.Load())
	}
}

func TestAlertEngineSkipsLowTraffic(t *testing.T) {
	mgr := NewManager()
	notif := &stubNotifier{}
	cfg := DefaultAlertConfig()
	cfg.Window = time.Minute
	cfg.MinAttempts = 50 // require lots of traffic
	cfg.FailRateThreshold = 0.50

	eng := NewAlertEngine(cfg, mgr, notif)

	// Only a handful of failures — below MinAttempts.
	for i := 0; i < 5; i++ {
		mgr.Record("kinotochka", false, 500, 502, "x", "h.local", "u")
	}
	eng.evaluate()
	if notif.count.Load() != 0 {
		t.Errorf("expected no alert below MinAttempts, got %d", notif.count.Load())
	}
}

func TestSubscribeReceivesNotifications(t *testing.T) {
	mgr := NewManager()
	id, ch := mgr.Subscribe()
	defer mgr.Unsubscribe(id)

	mgr.Record("test", true, 50, 200, "", "host.local", "u")
	select {
	case <-ch:
		// ok
	case <-time.After(time.Second):
		t.Error("expected subscriber to receive a notification")
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	mgr := NewManager()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		mgr.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
		// ok
	case <-time.After(time.Second):
		t.Error("Run did not return after context cancel")
	}
}
