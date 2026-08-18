package transcode

import "testing"

// TestPickVideoBitrate_Tiers pins the resolution→bitrate buckets. Relocated from
// internal/httpapi when PickVideoBitrate was extracted into this package.
func TestPickVideoBitrate_Tiers(t *testing.T) {
	cases := []struct {
		w, h, min, max int
	}{
		{640, 480, 1500, 1500},
		{1280, 720, 3000, 3000},
		{1920, 1080, 6000, 6000},
		{2560, 1440, 10000, 10000},
		{3840, 2160, 16000, 16000},
	}
	for _, c := range cases {
		got := PickVideoBitrate(c.w, c.h)
		if got < c.min || got > c.max {
			t.Fatalf("%dx%d → %d, want %d..%d", c.w, c.h, got, c.min, c.max)
		}
	}
}
