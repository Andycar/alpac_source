package litesrc

import (
	"testing"
)

func TestRezkaPromoDetection(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		isURL  bool
		expect bool
	}{
		// URL-based promo detection
		{"promo url trailer", "https://cdn.example/trailers/movie.m3u8", true, true},
		{"promo url teaser", "https://cdn.example/teaser/video.mp4", true, true},
		{"promo url splash", "https://cdn.example/splash/intro.m3u8", true, true},
		{"promo url intro", "https://cdn.example/intro/video.m3u8", true, true},
		{"promo url greeting", "https://cdn.example/greeting/vid.mp4", true, true},
		{"promo url preview", "https://cdn.example/preview/sample.m3u8", true, true},
		{"promo url anons", "https://cdn.example/anons_clip.mp4", true, true},
		{"promo url заставка", "https://cdn.example/заставка.mp4", true, true},
		{"normal url movie", "https://cdn.example/streams/movie.m3u8", true, false},
		{"normal url hls", "https://stream.example/video/1080p.m3u8", true, false},

		// Decoded content promo detection
		{"promo content trailer", "some decoded data with trailer reference", false, true},
		{"promo content заставка", "контент с заставка в нем", false, true},
		{"promo content splash", "/splash/video.m3u8 some more content", false, true},
		{"promo content anons", "anons clip", false, true},
		{"normal content", "[1080p]https://cdn.example/movie/stream.m3u8", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got bool
			if tt.isURL {
				got = pidorezkaURLLooksLikePromo(tt.input)
			} else {
				got = pidorezkaLooksLikePromo(tt.input)
			}
			if got != tt.expect {
				t.Errorf("promo(%q) = %v, want %v", tt.input, got, tt.expect)
			}
		})
	}
}
