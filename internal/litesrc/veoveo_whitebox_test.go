package litesrc

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"lampac-go/internal/config"
)

func TestVeoVeoBadgeFromMaster(t *testing.T) {
	cases := []struct {
		name, master, want string
	}{
		{"720+480 -> HD", "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=2500000,RESOLUTION=1280x720\na.m3u8\n#EXT-X-STREAM-INF:RESOLUTION=854x480\nb.m3u8\n", "HD"},
		{"1080 -> FHD", "#EXT-X-STREAM-INF:RESOLUTION=1920x1080\nx.m3u8\n", "FHD"},
		{"2160 -> 4K", "#EXT-X-STREAM-INF:RESOLUTION=3840x2160\nx.m3u8\n", "4K"},
		{"mixed picks max", "#EXT-X-STREAM-INF:RESOLUTION=854x480\na\n#EXT-X-STREAM-INF:RESOLUTION=1920x1080\nb\n", "FHD"},
		{"none -> empty", "#EXTM3U\n#EXT-X-ENDLIST\n", ""},
	}
	for _, c := range cases {
		if got := veoVeoBadgeFromMaster(c.master); got != c.want {
			t.Errorf("%s: veoVeoBadgeFromMaster = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestVeoVeoLiveResolution(t *testing.T) {
	if os.Getenv("VEOVEO_LIVE") != "1" {
		t.Skip("set VEOVEO_LIVE=1 to run the live network test")
	}

	v := NewVeoVeoChecker(config.Config{}) // empty cfg -> default host + token
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	const kp = "5698586" // Паук-Нуар
	id := v.resolveMovieIDOnline(ctx, kp)
	if id == 0 {
		t.Fatalf("live: failed to resolve kp=%s via iframe", kp)
	}
	t.Logf("live: kp=%s -> movieID=%d", kp, id)

	episodes, ok := v.fetchEpisodes(ctx, id)
	if !ok || len(episodes) == 0 {
		t.Fatalf("live: no episodes for movieID=%d", id)
	}
	file := veoVeoBestFilepath(episodes[0].EpisodeVariants)
	if !strings.Contains(file, ".m3u8") {
		t.Fatalf("live: expected m3u8 filepath, got %q", file)
	}
	t.Logf("live: %d episodes, first stream: %s", len(episodes), file)

	// Voice extraction against real data: Паук-Нуар ships two dubs with distinct
	// streams (hdrezka studio -> 91270, hdrezka -> 91271).
	voices := veoVeoVoiceNames(episodes)
	if len(voices) < 2 {
		t.Fatalf("live: expected >=2 dubs, got %v", voices)
	}
	t.Logf("live: voices=%v", voices)
	for _, name := range voices {
		f := veoVeoVariantFile(episodes[0].EpisodeVariants, name)
		if !strings.Contains(f, ".m3u8") {
			t.Fatalf("live: voice %q resolved to non-m3u8 %q", name, f)
		}
		t.Logf("live: voice %-16q -> %s", name, f)
	}
	if veoVeoVariantFile(episodes[0].EpisodeVariants, voices[0]) == veoVeoVariantFile(episodes[0].EpisodeVariants, voices[1]) {
		t.Fatalf("live: two dubs resolved to the same stream")
	}

	// Quality badge parsed from the real master playlist (Паук-Нуар ships 720p).
	badge := v.detectQualityBadge(ctx, id, episodes)
	if badge == "" {
		t.Fatalf("live: failed to detect quality badge from master")
	}
	t.Logf("live: quality badge = %q", badge)
}
