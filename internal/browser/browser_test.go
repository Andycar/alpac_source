package browser

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// withCleanRegistry snapshots and restores the package-level registry
// around t. The browser registry is process-global, so tests must not
// leak state into each other.
func withCleanRegistry(t *testing.T) {
	t.Helper()
	regMu.Lock()
	savedEngines := engines
	savedDefault := defaultE
	engines = map[string]Engine{}
	defaultE = ""
	regMu.Unlock()

	overMu.Lock()
	savedOverrides := overrides
	overrides = map[string]string{}
	overMu.Unlock()

	t.Cleanup(func() {
		regMu.Lock()
		engines = savedEngines
		defaultE = savedDefault
		regMu.Unlock()
		overMu.Lock()
		overrides = savedOverrides
		overMu.Unlock()
	})
}

func TestRegisterAndGet(t *testing.T) {
	withCleanRegistry(t)
	e := NewMockEngine("chromedp")
	Register(e)

	got, err := Get("chromedp")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != e {
		t.Fatal("Get returned a different engine")
	}

	if _, err := Get("missing"); !errors.Is(err, ErrUnknownEngine) {
		t.Fatalf("Get missing: want ErrUnknownEngine, got %v", err)
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	withCleanRegistry(t)
	Register(NewMockEngine("rod"))
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate register")
		}
	}()
	Register(NewMockEngine("rod"))
}

func TestList(t *testing.T) {
	withCleanRegistry(t)
	Register(NewMockEngine("rod"))
	Register(NewMockEngine("chromedp"))
	Register(NewMockEngine("playwright"))
	got := List()
	want := []string{"chromedp", "playwright", "rod"}
	if len(got) != len(want) {
		t.Fatalf("List len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("List[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestDefault_FallbackToChromedp(t *testing.T) {
	withCleanRegistry(t)
	chromedp := NewMockEngine("chromedp")
	rod := NewMockEngine("rod")
	Register(chromedp)
	Register(rod)

	// No SetDefault → chromedp wins by convention.
	if Default() != chromedp {
		t.Fatal("Default should fall back to chromedp")
	}

	SetDefault("rod")
	if Default() != rod {
		t.Fatal("SetDefault(rod) not honoured")
	}

	// Unknown default → falls back to chromedp again.
	SetDefault("nonsuch")
	if Default() != chromedp {
		t.Fatal("Unknown default should fall back to chromedp")
	}
}

func TestDefault_NoEngines(t *testing.T) {
	withCleanRegistry(t)
	if Default() != nil {
		t.Fatal("Default() should be nil when no engines are registered")
	}
}

func TestForBalancer(t *testing.T) {
	withCleanRegistry(t)
	chromedp := NewMockEngine("chromedp")
	rod := NewMockEngine("rod")
	Register(chromedp)
	Register(rod)
	SetDefault("chromedp")

	// No overrides → default.
	if ForBalancer("mirage") != chromedp {
		t.Fatal("ForBalancer without override should return default")
	}

	SetBalancerOverrides(map[string]string{"mirage": "rod"})
	if ForBalancer("mirage") != rod {
		t.Fatal("ForBalancer should honour override")
	}
	if ForBalancer("turbo") != chromedp {
		t.Fatal("Non-overridden balancer must still get default")
	}

	// Override pointing at an engine that isn't compiled in → fall back.
	SetBalancerOverrides(map[string]string{"mirage": "playwright"})
	if ForBalancer("mirage") != chromedp {
		t.Fatal("Unknown override engine should fall back to default")
	}
}

func TestSessionLifecycle(t *testing.T) {
	withCleanRegistry(t)
	e := NewMockEngine("rod")
	Register(e)

	e.ProgramNavigate("https://example.com", nil)
	e.ProgramEval("document.title", "Example Domain")

	s, err := e.NewSession(context.Background(), SessionOptions{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := s.Navigate("https://example.com"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	var title string
	if err := s.Eval("document.title", &title); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if title != "Example Domain" {
		t.Fatalf("title = %q, want %q", title, "Example Domain")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := e.Navigations(); len(got) != 1 || got[0] != "https://example.com" {
		t.Fatalf("Navigations = %v", got)
	}
}

func TestEngineUnavailable(t *testing.T) {
	withCleanRegistry(t)
	e := NewMockEngine("playwright")
	e.SetAvailable(ErrEngineUnavailable)
	Register(e)

	if _, err := e.NewSession(context.Background(), SessionOptions{}); !errors.Is(err, ErrEngineUnavailable) {
		t.Fatalf("NewSession: want ErrEngineUnavailable, got %v", err)
	}
}

func TestHijackEmit_Fulfill(t *testing.T) {
	withCleanRegistry(t)
	e := NewMockEngine("rod")
	e.ProgramHijack(HijackScript{
		ByPrefix: map[string]HijackAction{
			"https://cdn.example/m3u8/": {Fulfill: &HijackFulfill{
				Status:  204,
				Headers: http.Header{"Content-Type": []string{"text/plain"}},
			}},
		},
	})

	s, _ := e.NewSession(context.Background(), SessionOptions{})
	ms := s.(*mockSession)

	called := 0
	handler := func(req HijackRequest) {
		called++
		if req.Method() == "OPTIONS" {
			req.Fulfill(204, http.Header{"Access-Control-Allow-Origin": []string{"*"}}, nil)
			return
		}
		req.Continue()
	}

	ms.EmitHijack(handler, MockHijackInput{
		URL:    "https://cdn.example/m3u8/index.m3u8",
		Method: "OPTIONS",
	})
	if called != 1 {
		t.Fatal("handler not invoked")
	}
}

func TestHijackEmit_DefaultsToContinue(t *testing.T) {
	withCleanRegistry(t)
	e := NewMockEngine("rod")
	s, _ := e.NewSession(context.Background(), SessionOptions{})
	ms := s.(*mockSession)

	// Handler forgets to resolve — EmitHijack must call Continue itself.
	called := false
	ms.EmitHijack(func(req HijackRequest) { called = true }, MockHijackInput{URL: "https://x"})
	if !called {
		t.Fatal("handler not invoked")
	}
}
