package httpapi

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"lampac-go/internal/transcodesvc"
)

// PidTor + transcoder wiring (alpac/capi). The bare resolve URL (/lite/pidtor/s...) is NOT a media
// URL, so capi's capiIsStream filter drops it → PidTor never reaches the alpac SPA. Routing through
// /transcoding/start.m3u8 makes the URL end in .m3u8 (capi accepts it) AND lets the server-side
// transcoder play incompatible codecs.
func TestPidtorClientStreamURL(t *testing.T) {
	host := "https://tv.example.cc"
	resolve := host + "/lite/pidtor/sABC123?tr=udp%3A%2F%2Ftracker"

	// Off → resolve URL unchanged (direct Lampa behaviour preserved).
	if got := pidtorClientStreamURL(host, resolve, false); got != resolve {
		t.Fatalf("transcode off: got %q, want %q", got, resolve)
	}

	// On → /transcoding/start.m3u8?src=<encoded resolve>.
	got := pidtorClientStreamURL(host, resolve, true)
	want := host + "/transcoding/start.m3u8?src=" + url.QueryEscape(resolve)
	if got != want {
		t.Fatalf("transcode on: got %q, want %q", got, want)
	}

	// The whole point: capi must accept the wrapped URL (otherwise PidTor is invisible in alpac).
	if !capiIsStream(got) {
		t.Errorf("capiIsStream must accept the transcoded URL, but rejected: %q", got)
	}
	// ...while the bare resolve URL is correctly rejected (the bug we're fixing).
	if capiIsStream(resolve) {
		t.Errorf("capiIsStream should reject the bare resolve URL: %q", resolve)
	}
}

// capi must hand the transcoder HLS to the client as-is (same-origin relative), NOT wrap it in a
// /proxy token — it's a multi-URL playlist (start → master → segments) a single token can't carry.
// It also stamps tcpool=capi so the job draws from the separate capi/TV concurrency pool.
func TestCapiSameOriginTranscoding(t *testing.T) {
	raw := "https://tv.example.cc/transcoding/start.m3u8?src=https%3A%2F%2Ftv.example.cc%2Flite%2Fpidtor%2FsABC"
	got := capiSameOrigin(raw, nil, "1.2.3.4", "pidtor")
	want := "/transcoding/start.m3u8?src=https%3A%2F%2Ftv.example.cc%2Flite%2Fpidtor%2FsABC&tcpool=capi"
	if got != want {
		t.Fatalf("transcoding pass-through: got %q, want %q", got, want)
	}
	if strings.Contains(got, "/proxy/") {
		t.Errorf("transcoding URL must not be wrapped in /proxy: %q", got)
	}
	// The marker must survive into the start handler's pool routing.
	r := httptest.NewRequest("GET", got, nil)
	if !transcodesvc.TranscodingWantsCapiPool(r) {
		t.Errorf("tagged URL must route to the capi pool: %q", got)
	}
}
