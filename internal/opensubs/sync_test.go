package opensubs

import (
	"net/http/httptest"
	"testing"
	"time"

	"lampac-go/internal/subsync"
)

func subsyncResultFor(offsetMS int) subsync.Result {
	return subsync.Result{
		Offset:     time.Duration(offsetMS) * time.Millisecond,
		Confidence: 0.9,
		Score:      0.7,
	}
}

// safeSyncSource is the SSRF gate: whatever the client sends, ffmpeg must only
// ever be pointed at our own stream paths, rebuilt from our own host.
func TestSafeSyncSource(t *testing.T) {
	r := httptest.NewRequest("GET", "http://tv.example/api/opensubs/sync", nil)
	r.Host = "tv.example"

	allowed := map[string]string{
		"/proxy/abc123":                      "http://tv.example/proxy/abc123",
		"/transcoding/xyz/live.m3u8":         "http://tv.example/transcoding/xyz/live.m3u8",
		"/ts/stream?link=1":                  "http://tv.example/ts/stream?link=1",
		"https://tv.example/proxy/abc":       "http://tv.example/proxy/abc",
		"https://tv.example/proxy/a?b=1&c=2": "http://tv.example/proxy/a?b=1&c=2",
	}
	for in, want := range allowed {
		if got := safeSyncSource(r, in); got != want {
			t.Errorf("safeSyncSource(%q) = %q, want %q", in, got, want)
		}
	}

	rejected := []string{
		"",
		"http://169.254.169.254/latest/meta-data/", // cloud metadata
		"https://evil.example/proxy/abc",           // someone else's host
		"file:///etc/passwd",
		"/etc/passwd",      // our host, but not a stream path
		"/admin/api/users", // our host, not a stream path — must not be fetchable
		"proxy/abc",        // not rooted
		"//evil.example/proxy/x",
	}
	for _, in := range rejected {
		if got := safeSyncSource(r, in); got != "" {
			t.Errorf("safeSyncSource(%q) = %q, want rejection", in, got)
		}
	}
}

func TestSyncCacheRoundTrip(t *testing.T) {
	syncMu.Lock()
	syncCache = map[string]syncEntry{}
	syncMu.Unlock()

	if _, ok := syncCacheGet("k"); ok {
		t.Fatal("empty cache returned a hit")
	}
	syncCacheSet("k", subsyncResultFor(-2500))
	got, ok := syncCacheGet("k")
	if !ok || got.Offset.Milliseconds() != -2500 {
		t.Errorf("cached = %+v ok=%v", got, ok)
	}
}
