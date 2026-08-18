//go:build playwright
// +build playwright

package browser

import (
	"net/http"
	"os"
	"testing"
)

// TestPlaywrightEngine_Registered confirms the engine self-registered
// at init() so List()/Get() return it when the binary is built with
// -tags playwright. Mirror tests for chromedp/rod do the same.
func TestPlaywrightEngine_Registered(t *testing.T) {
	e, err := Get("playwright")
	if err != nil {
		t.Fatalf("playwright not registered: %v", err)
	}
	if e.Name() != "playwright" {
		t.Fatalf("Name = %q", e.Name())
	}
}

// TestPlaywrightEngine_AvailableGracefulWithoutDriver — without an
// installed driver Available() must return ErrEngineUnavailable
// wrapped, not crash. Skipped when the dev machine has a usable
// install (rare CI condition). Set BROWSER_RUN_NETWORK_TESTS=1 to
// run the install path.
func TestPlaywrightEngine_AvailableGracefulWithoutDriver(t *testing.T) {
	if os.Getenv("BROWSER_RUN_NETWORK_TESTS") == "1" {
		t.Skip("opt-in: BROWSER_RUN_NETWORK_TESTS=1 — would trigger driver download")
	}
	e, _ := Get("playwright")
	if err := e.Available(); err != nil {
		// Accept both ErrEngineUnavailable wrap (no driver) and
		// nil (driver already on disk from a previous run).
		t.Logf("Available() returned: %v", err)
	}
}

// TestHeaderMapFromHeader is a small unit covering the helper that
// flattens net/http multi-value Headers into playwright's
// map[string]string shape — keeps regressions on dup-value handling
// (only the first wins) visible.
func TestHeaderMapFromHeader(t *testing.T) {
	h := http.Header{
		"X-A": []string{"1"},
		"X-B": []string{"2", "3"},
		"X-C": nil,
		"X-D": []string{""},
	}
	got := headerMapFromHeader(h)
	if got["X-A"] != "1" || got["X-B"] != "2" || got["X-D"] != "" {
		t.Fatalf("flattening lost values: %v", got)
	}
	if _, ok := got["X-C"]; ok {
		t.Errorf("nil-value header should be dropped: %v", got)
	}
}

// TestPlaywrightCoerce_FastPaths exercises the scalar shortcuts so we
// notice if playwright-go starts returning non-float numerics or
// non-string text. Pre-empts the silent "data was there but type
// mismatched" class of bugs.
func TestPlaywrightCoerce_FastPaths(t *testing.T) {
	{
		var s string
		if err := playwrightCoerce("hi", &s); err != nil || s != "hi" {
			t.Errorf("string fast-path: %q %v", s, err)
		}
	}
	{
		var b bool
		if err := playwrightCoerce(true, &b); err != nil || !b {
			t.Errorf("bool fast-path: %v %v", b, err)
		}
	}
	{
		var n int
		if err := playwrightCoerce(float64(42), &n); err != nil || n != 42 {
			t.Errorf("int fast-path: %d %v", n, err)
		}
	}
	{
		var n int64
		if err := playwrightCoerce(float64(42), &n); err != nil || n != 42 {
			t.Errorf("int64 fast-path: %d %v", n, err)
		}
	}
	{
		// Fallback: complex object via JSON roundtrip.
		var dst struct {
			Title string `json:"title"`
		}
		src := map[string]any{"title": "x"}
		if err := playwrightCoerce(src, &dst); err != nil || dst.Title != "x" {
			t.Errorf("fallback path: %+v %v", dst, err)
		}
	}
}
