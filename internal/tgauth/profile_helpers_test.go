package tgauth

import (
	"strings"
	"testing"
	"time"
)

// TestRenderBar covers edge cases of the percent-to-bar mapping. The
// width must always equal the requested width (no shrinking) and the
// rounding must not drop the last cell when percent==100.
func TestRenderBar(t *testing.T) {
	tests := []struct {
		percent, width int
		want           string
	}{
		{0, 10, "▱▱▱▱▱▱▱▱▱▱"},
		{100, 10, "▰▰▰▰▰▰▰▰▰▰"},
		{50, 10, "▰▰▰▰▰▱▱▱▱▱"},
		// 12.5 % of 10 cells = 1.25 → rounds to 1 cell filled.
		{13, 10, "▰▱▱▱▱▱▱▱▱▱"},
		// Negative clamps to 0, > 100 clamps to width.
		{-5, 6, "▱▱▱▱▱▱"},
		{150, 6, "▰▰▰▰▰▰"},
		// Width 0 → empty string (the only case where width != output len).
		{50, 0, ""},
	}
	for _, tc := range tests {
		got := renderBar(tc.percent, tc.width)
		if got != tc.want {
			t.Errorf("renderBar(%d, %d) = %q, want %q", tc.percent, tc.width, got, tc.want)
		}
		// Visible cells = unicode codepoints, not bytes.
		if tc.width > 0 {
			cells := strings.Count(got, "▰") + strings.Count(got, "▱")
			if cells != tc.width {
				t.Errorf("renderBar(%d, %d): got %d cells, want %d", tc.percent, tc.width, cells, tc.width)
			}
		}
	}
}

// TestRemainingPercent — the bar should show "100 %" right at issue,
// "0 %" at expiry, and a sane proportion in between.
func TestRemainingPercent(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	start := now.Add(-30 * 24 * time.Hour)
	end := now.Add(30 * 24 * time.Hour) // 60-day span, halfway through

	if got := remainingPercent(now, start, end); got != 50 {
		t.Errorf("halfway: got %d %%, want 50", got)
	}
	// Just issued.
	if got := remainingPercent(start, start, end); got != 100 {
		t.Errorf("issue moment: got %d %%, want 100", got)
	}
	// Just expired.
	if got := remainingPercent(end, start, end); got != 0 {
		t.Errorf("expiry moment: got %d %%, want 0", got)
	}
	// Past expiry → clamped to 0.
	if got := remainingPercent(end.Add(24*time.Hour), start, end); got != 0 {
		t.Errorf("post-expiry: got %d %%, want 0", got)
	}
	// Inverted range — should not panic, returns 0.
	if got := remainingPercent(now, end, start); got != 0 {
		t.Errorf("inverted: got %d %%, want 0", got)
	}
}

// TestCeilDays — sub-day duration must show as 1 day so "ещё 1 день"
// doesn't lie about there being "ещё 0 дней".
func TestCeilDays(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want int
	}{
		{0, 0},
		{-1 * time.Hour, 0},
		{1 * time.Hour, 1},
		{23*time.Hour + 59*time.Minute, 1},
		{24 * time.Hour, 1},
		{24*time.Hour + 1*time.Nanosecond, 2},
		{48 * time.Hour, 2},
		{72*time.Hour - 1*time.Second, 3},
		{365 * 24 * time.Hour, 365},
	}
	for _, tc := range tests {
		got := ceilDays(tc.d)
		if got != tc.want {
			t.Errorf("ceilDays(%v) = %d, want %d", tc.d, got, tc.want)
		}
	}
}

// TestPluralDays_Russian covers the full Russian rule set including
// the 11–14 special case where all three values use form[2] regardless
// of last digit.
func TestPluralDays_Russian(t *testing.T) {
	forms := [3]string{"день", "дня", "дней"}
	cases := map[int]string{
		1:   "день",
		2:   "дня",
		3:   "дня",
		4:   "дня",
		5:   "дней",
		10:  "дней",
		11:  "дней", // special — last digit 1 BUT mod100 in [11..14] → form[2]
		12:  "дней",
		13:  "дней",
		14:  "дней",
		15:  "дней",
		21:  "день", // mod100=21, last digit 1, NOT in [11..14] → form[0]
		22:  "дня",
		25:  "дней",
		111: "дней",
		121: "день",
		122: "дня",
	}
	for n, want := range cases {
		if got := pluralDays(n, forms); got != want {
			t.Errorf("pluralDays(%d, ru) = %q, want %q", n, got, want)
		}
	}
}

// TestPluralDays_English — English has 2 effective forms; we encode
// {"day", "days", "days"} so the helper still works with the same API.
func TestPluralDays_English(t *testing.T) {
	forms := [3]string{"day", "days", "days"}
	cases := map[int]string{
		0: "days", 1: "day", 2: "days", 21: "day", 25: "days",
	}
	for n, want := range cases {
		if got := pluralDays(n, forms); got != want {
			t.Errorf("pluralDays(%d, en) = %q, want %q", n, got, want)
		}
	}
}

// TestDeviceIcon — the heuristic should pick the most specific match
// even when the label has multiple keywords ("Android Chrome" → 🤖,
// not 💻, because OS wins over browser).
func TestDeviceIcon(t *testing.T) {
	cases := map[string]string{
		"Android Phone":    "🤖",
		"android-tv-stick": "🤖", // android wins over " tv"
		"iPhone 15 Pro":    "🍎",
		"iPad Air":         "🍎",
		"iOS 18":           "🍎",
		"Web Chrome":       "💻", // android not present → web wins
		"Firefox 124":      "💻",
		"Safari 17":        "💻",
		"Samsung Smart TV": "📺",
		"LG WebOS TV":      "📺",
		"Apple TV 4K":      "📺", // "appletv" only matches if lowercased+joined; substring " tv" hits first
		"Random Unknown":   "📱",
		"":                 "📱",
	}
	for label, want := range cases {
		got := deviceIcon(label)
		if got != want {
			t.Errorf("deviceIcon(%q) = %q, want %q", label, got, want)
		}
	}
}
