//go:build !no_rod
// +build !no_rod

package browser

import (
	"os"
	"testing"
)

// TestRodEngine_Registered verifies the rod engine self-registered at
// init() so the registry exposes it to the admin UI dropdown.
func TestRodEngine_Registered(t *testing.T) {
	e, err := Get("rod")
	if err != nil {
		t.Fatalf("rod engine not registered: %v", err)
	}
	if e.Name() != "rod" {
		t.Fatalf("Name = %q, want %q", e.Name(), "rod")
	}
}

// TestRodEngine_AvailableNonPanicking probes Available() on the rod
// engine. First call may download Chromium (~50s) so we skip by
// default; set BROWSER_RUN_NETWORK_TESTS=1 to opt in.
func TestRodEngine_AvailableNonPanicking(t *testing.T) {
	if os.Getenv("BROWSER_RUN_NETWORK_TESTS") != "1" {
		t.Skip("rod Available() may download Chromium; set BROWSER_RUN_NETWORK_TESTS=1 to run")
	}
	e, _ := Get("rod")
	_ = e.Available()
	_ = e.Available()
}

// TestStealthJSExposed sanity-checks that the stealth payload is
// non-empty in this build. Tests with -tags no_rod use a different
// file (stealth_nop.go) which returns "".
func TestStealthJSExposed(t *testing.T) {
	js := stealthJS()
	if len(js) < 1000 {
		t.Fatalf("stealthJS payload is suspiciously small (%d bytes); expected ~150KB", len(js))
	}
}
