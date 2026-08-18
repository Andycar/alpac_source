package litesrc

import (
	"context"
	"os"
	"testing"
)

// Live check for the Shorts classifier — it asks youtube.com, so it only runs on demand:
//
//	LIVE_YT=1 go test ./internal/litesrc/ -run TestIsShortLive -v
//
// Worth keeping: the detector rests on an undocumented behaviour (GET /shorts/<id> answers 200 for
// a Short and 303 for a regular video). If YouTube ever changes that, every feed silently stops
// classifying — this test is how we find out, and the sample IDs are the calibration set.
func TestIsShortLive(t *testing.T) {
	if os.Getenv("LIVE_YT") == "" {
		t.Skip("set LIVE_YT=1 to run (hits youtube.com)")
	}

	y := &YoutubeChecker{}
	// The probe needs YouTube's consent cookies, or every request bounces to consent.youtube.com
	// and the 200/303 distinction disappears. Same file yt-dlp uses.
	for _, p := range []string{"/opt/lampac/cookies.txt", "cookies.txt"} {
		if _, err := os.Stat(p); err == nil {
			y.cookiePath = p
			break
		}
	}
	if y.cookiePath == "" {
		t.Skip("no cookies.txt — the probe cannot pass YouTube's consent gate")
	}

	cases := []struct {
		id   string
		want bool
		note string
	}{
		{"f7y2XikE7sY", true, "MrBeast Short"},
		{"Df5Y-2ndQyU", true, "MrBeast Short"},
		{"rxyzDz3nWKc", false, "regular 10-minute video"},
		{"8QjrSb5YOmM", false, "59-second video that is NOT a Short (4:3)"},
	}
	for _, tc := range cases {
		got := y.IsShort(context.Background(), tc.id)
		if got != tc.want {
			t.Errorf("IsShort(%s) = %v, want %v — %s", tc.id, got, tc.want, tc.note)
		} else {
			t.Logf("ok %s = %v (%s)", tc.id, got, tc.note)
		}
	}
}
