package browser

import (
	"context"
	"testing"
	"time"
)

// chromedp ties a tab's lifetime to the context its FIRST Run executed on.
// NewSession used to run that first Navigate on a `context.WithTimeout(tabCtx…)`
// child and cancel it via defer — which closed the tab as soon as NewSession
// returned. Everything after (Hijack, Navigate, Eval) then failed with
// "context canceled", which is what silently killed every browser-sniff source.
//
// This test asserts the session is usable AFTER NewSession returns, i.e. the tab
// is bound to a context that outlives the constructor.
func TestChromedpSessionSurvivesConstructor(t *testing.T) {
	eng := &chromedpEngine{}
	if err := eng.Available(); err != nil {
		t.Skipf("chrome unavailable here: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	s, err := eng.NewSession(ctx, SessionOptions{
		Headless:          true,
		NavigationTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer s.Close()

	// The tab must still be alive: Hijack is the operation that regressed.
	cancelHijack, err := s.Hijack([]string{"*"}, func(r HijackRequest) { _ = r.Continue() })
	if err != nil {
		t.Fatalf("Hijack right after NewSession: %v (tab was closed by the constructor)", err)
	}
	cancelHijack()
}
