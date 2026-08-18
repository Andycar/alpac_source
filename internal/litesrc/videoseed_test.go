package litesrc

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestVideoseedNormalizeIframe(t *testing.T) {
	const realTok = "e3d256e33a0bc89bb6f5b97ddcf89de0"
	const cfgTok = "aaaabbbbccccddddeeeeffff00001111"
	zeroTok := strings.Repeat("0", 32)

	cases := []struct {
		name, iframe, playerToken, want string
	}{
		{
			// API/player keys diverged 2026-08: the embed 404s on the API key,
			// so an API-supplied token must survive normalization untouched.
			name:        "real token kept even when config token differs",
			iframe:      "https://tv-1-kinoserial.net/embed/2641/?token=" + realTok,
			playerToken: cfgTok,
			want:        "https://tv-1-kinoserial.net/embed/2641/?token=" + realTok,
		},
		{
			name:        "zeroed token replaced with player token",
			iframe:      "https://tv-2-kinoserial.net/embed/2641/?token=" + zeroTok,
			playerToken: cfgTok,
			want:        "https://tv-2-kinoserial.net/embed/2641/?token=" + cfgTok,
		},
		{
			name:        "zeroed token without player token left as-is",
			iframe:      "https://tv-2-kinoserial.net/embed/2641/?token=" + zeroTok,
			playerToken: "",
			want:        "https://tv-2-kinoserial.net/embed/2641/?token=" + zeroTok,
		},
		{
			name:        "missing token appended",
			iframe:      "https://tv-2-kinoserial.net/embed/2641/",
			playerToken: cfgTok,
			want:        "https://tv-2-kinoserial.net/embed/2641/?token=" + cfgTok,
		},
		{
			name:        "missing token appended with ampersand",
			iframe:      "https://tv-2-kinoserial.net/embed/2641/?x=1",
			playerToken: cfgTok,
			want:        "https://tv-2-kinoserial.net/embed/2641/?x=1&token=" + cfgTok,
		},
		{
			// Host must stay as the API returned it (tv-N no longer rewritten).
			name:        "host untouched",
			iframe:      "https://tv-3-kinolot.net/embed/2641/?token=" + realTok,
			playerToken: cfgTok,
			want:        "https://tv-3-kinolot.net/embed/2641/?token=" + realTok,
		},
	}
	for _, tc := range cases {
		if got := videoseedNormalizeIframe(tc.iframe, tc.playerToken); got != tc.want {
			t.Errorf("%s:\n got  %s\n want %s", tc.name, got, tc.want)
		}
	}
}

func TestVideoseedLearnPlayerToken(t *testing.T) {
	const tok = "e3d256e33a0bc89bb6f5b97ddcf89de0"
	v := &videoseedChecker{}

	v.learnPlayerToken(videoseedDataNode{Iframe: "https://tv-1-kinoserial.net/embed/1/?token=" + tok})
	if got := v.playerTokenFor(); got != tok {
		t.Fatalf("movie iframe: learned %q, want %q", got, tok)
	}

	v2 := &videoseedChecker{}
	v2.learnPlayerToken(videoseedDataNode{Seasons: map[string]videoseedSeasonVM{
		"1": {Videos: map[string]videoseedVideoVM{
			"1": {Iframe: "https://tv-1-kinoserial.net/embed/2/?token=" + tok},
		}},
	}})
	if got := v2.playerTokenFor(); got != tok {
		t.Fatalf("episode iframe: learned %q, want %q", got, tok)
	}

	// Zeroed tokens must not be learned.
	v3 := &videoseedChecker{}
	v3.learnPlayerToken(videoseedDataNode{Iframe: "https://x/embed/1/?token=" + strings.Repeat("0", 32)})
	if got := v3.playerTokenFor(); got != "" {
		t.Fatalf("zero token learned: %q", got)
	}

	// Explicit config token wins over learned.
	v4 := &videoseedChecker{playerToken: "cfg"}
	v4.learnPlayerToken(videoseedDataNode{Iframe: "https://x/embed/1/?token=" + tok})
	if got := v4.playerTokenFor(); got != "cfg" {
		t.Fatalf("config token overridden: %q", got)
	}
}

// obfuscate builds a PlayerJS payload the way live embeds do: base64 of the
// JSON with |||GARBAGE== blocks spliced in at arbitrary offsets.
func videoseedObfuscate(json string, blocks map[int]string) string {
	b64 := base64.StdEncoding.EncodeToString([]byte(json))
	var sb strings.Builder
	for i := 0; i < len(b64); i++ {
		if g, ok := blocks[i]; ok {
			sb.WriteString(g)
		}
		sb.WriteByte(b64[i])
	}
	return "#2" + sb.String()
}

func TestVideoseedParsePlayerJSVariants(t *testing.T) {
	json := `{"id":"Video2641","file":"{ Voize } https://storage.videoseedcdn.com/movies/a/b:2026080321/hls.m3u8;{ LostFilm } https://storage.videoseedcdn.com/movies/c/d:2026080321/hls-v1-a2.m3u8"}`

	checkTracks := func(t *testing.T, tracks []videoseedTrack) {
		t.Helper()
		if len(tracks) != 2 {
			t.Fatalf("got %d tracks, want 2: %+v", len(tracks), tracks)
		}
		if tracks[0].Name != "Voize" || !strings.HasSuffix(tracks[0].URL, "/hls.m3u8") {
			t.Fatalf("track 0 wrong: %+v", tracks[0])
		}
		if tracks[1].Name != "LostFilm" || !strings.HasSuffix(tracks[1].URL, "/hls-v1-a2.m3u8") {
			t.Fatalf("track 1 wrong: %+v", tracks[1])
		}
	}

	t.Run("plain garbage blocks (kinoserial)", func(t *testing.T) {
		payload := videoseedObfuscate(json, map[int]string{
			40:  "|||d3ozV3FsSlNMRWp3NnRSNmNPMVdN==",
			120: "|||S0xncDlhZGhubGRkYTJGd0dnUDR0==",
		})
		html := `<script>var player = new Playerjs("` + payload + `");</script>`
		checkTracks(t, videoseedParsePlayerJS(html))
	})

	t.Run("nested garbage blocks (kinolot)", func(t *testing.T) {
		// Live tv-3-kinolot.net embeds nest one garbage block INSIDE another:
		// |||A1|||B==A2== — the outer block's terminator comes after the inner
		// block, so it only becomes matchable once the inner one is removed.
		// (The old walk-based cleaner cut at B's "==" and left "A2==" in the
		// payload, corrupting the base64.)
		payload := videoseedObfuscate(json, map[int]string{
			60: "|||R1gyUWl4N3JHYWJvVnd1M0hZN3Bl|||ZFJyT25COTM3MndRUnRLZ3A5YWRo==eDBRa1F2NzNDVg==",
		})
		html := `new Playerjs("` + payload + `")`
		checkTracks(t, videoseedParsePlayerJS(html))
	})

	t.Run("legacy segmented base64 falls back", func(t *testing.T) {
		// Old format: independently padded base64 segments concatenated.
		half := len(json) / 2
		payload := "#2" + base64.StdEncoding.EncodeToString([]byte(json[:half])) +
			base64.StdEncoding.EncodeToString([]byte(json[half:]))
		html := `new Playerjs("` + payload + `")`
		checkTracks(t, videoseedParsePlayerJS(html))
	})

	t.Run("no playerjs call", func(t *testing.T) {
		if tracks := videoseedParsePlayerJS("<html>nothing here</html>"); tracks != nil {
			t.Fatalf("expected nil, got %+v", tracks)
		}
	})
}
