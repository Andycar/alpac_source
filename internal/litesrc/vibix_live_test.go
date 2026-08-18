package litesrc

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// VIBIX_LIVE=1 go test ./internal/litesrc/ -run TestVibixLiveResolve -v -timeout 180s
func TestVibixLiveResolve(t *testing.T) {
	if os.Getenv("VIBIX_LIVE") == "" {
		t.Skip("set VIBIX_LIVE=1 to run the live browser resolve")
	}

	resolveMovie := func(label string, kp int64) []vibixSeason {
		start := time.Now()
		playlist, ok := vibixResolveViaColdfilm(context.Background(), "", kp)
		t.Logf("[%s] resolve kp=%d took %s ok=%v items=%d", label, kp, time.Since(start), ok, len(playlist))
		if !ok || len(playlist) == 0 {
			t.Fatalf("[%s] resolve failed", label)
		}
		for i, it := range playlist {
			file := it.File
			if len(file) > 140 {
				file = file[:140] + "…"
			}
			t.Logf("  item[%d] title=%q folders=%d file=%q", i, it.Title, len(it.Folder), file)
		}
		return playlist
	}

	// First resolve — cold browser launch.
	pl := resolveMovie("cold", 1236063) // Tenet

	// Cache hit — instant.
	start := time.Now()
	_, ok := vibixResolveViaColdfilm(context.Background(), "", 1236063)
	t.Logf("[cache] took %s ok=%v", time.Since(start), ok)

	// Different title — should reuse the WARM browser (no relaunch).
	resolveMovie("warm", 301) // The Matrix (kp 301)

	// Fetch the first stream m3u8 through the handler's direct path.
	if groups := vibixParseVoiceGroups(pl[0].File); len(groups) > 0 && len(groups[0].streams) > 0 {
		url := groups[0].streams[0]["url"]
		req := httptest.NewRequest("GET", "http://lampac.local/x", nil)
		m3u8, fok := vibixFetchM3U8Direct(req, url, "")
		var seg string
		for _, ln := range strings.Split(m3u8, "\n") {
			ln = strings.TrimSpace(ln)
			if ln != "" && !strings.HasPrefix(ln, "#") {
				seg = ln
				break
			}
		}
		t.Logf("m3u8 ok=%v len=%d firstSeg=%s", fok, len(m3u8), seg)
		if !fok {
			t.Errorf("m3u8 fetch failed")
		}
	}
}
