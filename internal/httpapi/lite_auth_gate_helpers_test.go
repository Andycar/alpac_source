package httpapi

// Test-only helpers for swapping the package-level auth-gate dependencies.
// These manipulate the same globals that are populated by server.go on
// boot (SetPasswordAuthState + registerKitRoutes); production code
// reaches them via the lite_auth_gate.go compat check.
//
// The "_test.go" suffix keeps this file out of production builds.
//
// Build tags are NOT used here on purpose: most _test.go files in this
// package import these helpers, so they need to live in a regular _test
// file (no extra build constraints) — the Go test runner compiles them
// in but skips them otherwise.

import (
	"testing"

	"lampac-go/internal/tgauth"
)

// withAuthConfiguredForTest seeds the package globals so
// isAnyUserAuthConfigured() returns true for the duration of the test.
// Use this before any test that asserts the source-discovery gate
// blocks unauth requests.
func withAuthConfiguredForTest(t *testing.T) {
	t.Helper()
	prev := globalPwUserStore
	store := tgauth.NewPasswordUserStore(t.TempDir())
	if _, err := store.Create("test-seeded", "test-password-12345", tgauth.CreateOpts{}); err != nil {
		t.Fatalf("seed password user: %v", err)
	}
	globalPwUserStore = store
	t.Cleanup(func() {
		store.Close()
		globalPwUserStore = prev
	})
}

// resetAuthGlobalsForTest forces isAnyUserAuthConfigured() to return
// false for the duration of the test. Use this to assert the compat
// fallback (gate falls open when nothing is configured).
func resetAuthGlobalsForTest(t *testing.T) {
	t.Helper()
	prevPw := globalPwUserStore
	prevTG := kitTGTokenStore
	prevMode := globalAuthModeStore
	globalPwUserStore = nil
	kitTGTokenStore = nil
	globalAuthModeStore = nil
	t.Cleanup(func() {
		globalPwUserStore = prevPw
		kitTGTokenStore = prevTG
		globalAuthModeStore = prevMode
	})
}
