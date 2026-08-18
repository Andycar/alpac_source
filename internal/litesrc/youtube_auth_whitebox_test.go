package litesrc

import "testing"

// TestYouTubeCheckerAuthHelpersAtomic confirms the atomic.Pointer wrappers
// give back what we stored — and that nil round-trips cleanly. The race
// detector would flag the OLD pattern (direct field mutation after the
// struct was published in a global).
func TestYouTubeCheckerAuthHelpersAtomic(t *testing.T) {
	y := &YoutubeChecker{}
	if y.ytAPI() != nil || y.tgStore() != nil {
		t.Fatalf("zero-value YoutubeChecker should have nil auth")
	}
	// Set + read.
	y.SetAuth(nil, nil)
	if y.ytAPI() != nil || y.tgStore() != nil {
		t.Fatalf("explicit nil SetAuth should round-trip nil")
	}
}
