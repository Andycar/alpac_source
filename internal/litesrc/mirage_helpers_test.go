package litesrc

import (
	"testing"
)

// TestMirageHLSHostExtractsHostPrefix: extract the scheme://host[:port]
// prefix from an HLS segment URL — this is the hot-path helper called per
// segment for relative-URI rewriting.
func TestMirageHLSHostExtractsHostPrefix(t *testing.T) {
	cases := map[string]string{
		"https://cdn.example.com/seg-001.ts":          "https://cdn.example.com",
		"http://cdn.example.com:8080/path/index.m3u8": "http://cdn.example.com:8080",
		"https://cdn.example.com":                     "", // no trailing slash → no match
		"":                                            "",
		"not-a-url":                                   "",
		"//proto-relative.example/path":               "",
		"HTTPS://Upper.Example.Com/foo":               "HTTPS://Upper.Example.Com", // case-insensitive regex
	}
	for in, want := range cases {
		if got := mirageHLSHost(in); got != want {
			t.Errorf("mirageHLSHost(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestMirageExtractViewporti reads the Borth auth token out of the player
// HTML's <meta name="viewporti" content="…"> tag. Pre-compiled regex.
func TestMirageExtractViewporti(t *testing.T) {
	cases := []struct {
		name string
		html string
		want string
	}{
		{"present", `<head><meta name="viewporti" content="borth-tok-abc"></head>`, "borth-tok-abc"},
		{"present with spaces", `<meta   name="viewporti"   content="token-x" />`, "token-x"},
		{"missing", `<head><title>no viewporti</title></head>`, ""},
		{"wrong meta name", `<meta name="viewport" content="width=1080">`, ""},
		{"empty content", `<meta name="viewporti" content="">`, ""},
	}
	for _, c := range cases {
		got := mirageExtractViewporti([]byte(c.html))
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// TestMirageUpdateCurrentTimeFromURLSegmentParsing: segment URLs of the
// form /seg-<N>- map to current_time = (N-25)*6 seconds, with N<=25
// resetting both counters to 0. The counters belong to ONE WS client.
func TestMirageUpdateCurrentTimeFromURLSegmentParsing(t *testing.T) {
	c := newTestWSClient("")

	// seg <= 25 should reset both to 0.
	c.updateCurrentTimeFromURL("https://cdn.example/seg-10-quality.ts")
	if got := c.currentSec.Load(); got != 0 {
		t.Fatalf("seg<=25 should reset current_time to 0, got %d", got)
	}
	if got := c.lastSec.Load(); got != 0 {
		t.Fatalf("seg<=25 should reset last_time to 0, got %d", got)
	}

	// seg = 30 → current_time = (30-25)*6 = 30.
	c.updateCurrentTimeFromURL("https://cdn.example/seg-30-quality.ts")
	if got := c.currentSec.Load(); got != 30 {
		t.Fatalf("seg=30 current_time = %d, want 30", got)
	}
	// First non-reset sample also seeds last_time so we don't fire a
	// false-positive "seeked".
	if got := c.lastSec.Load(); got != 30 {
		t.Fatalf("seg=30 last_time seed = %d, want 30", got)
	}

	// seg = 35 → current_time = 60; difference from last=30 is 30 (< 90), no seek.
	c.updateCurrentTimeFromURL("https://cdn.example/seg-35-quality.ts")
	if got := c.currentSec.Load(); got != 60 {
		t.Fatalf("seg=35 current_time = %d, want 60", got)
	}
	if got := c.lastSec.Load(); got != 60 {
		t.Fatalf("seg=35 last_time = %d, want 60", got)
	}
}

// TestMirageUpdateCurrentTimeFromURLSeekDetection: a jump > 90 seconds of
// content signals THIS session's heartbeat to send "seeked".
func TestMirageUpdateCurrentTimeFromURLSeekDetection(t *testing.T) {
	c := newTestWSClient("")

	// Seed last_time at seg=30 (current_time=30).
	c.updateCurrentTimeFromURL("https://cdn.example/seg-30-quality.ts")

	// Big jump: seg=200 → current_time=(200-25)*6 = 1050. last=30 → delta=1020 (>90).
	c.updateCurrentTimeFromURL("https://cdn.example/seg-200-quality.ts")

	select {
	case <-c.seekCh:
		// expected
	default:
		t.Fatalf("expected seek signal to be sent on >90s jump")
	}
}

// TestMirageUpdateCurrentTimeIsPerSession: one session's segments must not move
// another's reported position. They used to share process-wide globals, so two
// concurrent viewers reported each other's playback curve to the CDN.
func TestMirageUpdateCurrentTimeIsPerSession(t *testing.T) {
	a := newTestWSClient("")
	b := newTestWSClient("")

	a.updateCurrentTimeFromURL("https://cdn.example/seg-30-quality.ts")  // → 30
	b.updateCurrentTimeFromURL("https://cdn.example/seg-200-quality.ts") // → 1050

	if got := a.currentSec.Load(); got != 30 {
		t.Fatalf("session A position = %d, want 30 (B's fetches leaked in)", got)
	}
	if got := b.currentSec.Load(); got != 1050 {
		t.Fatalf("session B position = %d, want 1050", got)
	}
	select {
	case <-a.seekCh:
		t.Fatal("B's jump fired a spurious seeked on session A")
	default:
	}
}

// TestMirageUpdateCurrentTimeFromURLNoMatchDoesNothing: URLs that don't
// match the /seg-N- pattern leave counters untouched.
func TestMirageUpdateCurrentTimeFromURLNoMatchDoesNothing(t *testing.T) {
	c := newTestWSClient("")
	c.currentSec.Store(42)
	c.lastSec.Store(42)

	c.updateCurrentTimeFromURL("https://cdn.example/no-segment-here.ts")
	c.updateCurrentTimeFromURL("not-a-url")
	c.updateCurrentTimeFromURL("")

	if got := c.currentSec.Load(); got != 42 {
		t.Fatalf("current_time mutated on non-matching URL: %d", got)
	}
	if got := c.lastSec.Load(); got != 42 {
		t.Fatalf("last_time mutated on non-matching URL: %d", got)
	}
}
