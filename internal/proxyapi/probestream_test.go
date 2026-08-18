package proxyapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"lampac-go/internal/config"
	"lampac-go/internal/hlsprobe"
)

// End-to-end through the real fetch path: the probe must follow the same
// manifest → variant → segment walk a player does, and report the stage that
// broke rather than the manifest's cheerful 200.
func TestProbeStreamWalksToSegment(t *testing.T) {
	var segmentStatus = http.StatusOK
	var segmentRange string

	mux := http.NewServeMux()
	mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte("#EXTM3U\n" +
			"#EXT-X-STREAM-INF:BANDWIDTH=800000,CODECS=\"avc1.42c01e\"\n" +
			"360/index.m3u8\n"))
	})
	mux.HandleFunc("/360/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte("#EXTM3U\n#EXTINF:6.0,\nseg1.ts\n#EXT-X-ENDLIST\n"))
	})
	mux.HandleFunc("/360/seg1.ts", func(w http.ResponseWriter, r *http.Request) {
		segmentRange = r.Header.Get("Range")
		w.WriteHeader(segmentStatus)
		if segmentStatus < 300 {
			_, _ = w.Write([]byte("ts-bytes"))
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	h := New(config.Config{}, nil)

	res, final, _ := h.ProbeStream(context.Background(), srv.URL+"/master.m3u8", "testsrc", nil)
	if !res.OK {
		t.Fatalf("expected a healthy walk, got %+v", res)
	}
	if res.Stage != hlsprobe.StageSegment || res.Variants != 1 || res.SegmentBytes == 0 {
		t.Errorf("res = %+v", res)
	}
	if segmentRange == "" {
		t.Error("segment should be fetched with a Range so probes stay cheap")
	}
	if final != srv.URL+"/master.m3u8" {
		t.Errorf("final = %q", final)
	}

	// Now the failure that a HEAD check — and the old first-bytes probe — both
	// call healthy: manifests fine, segments forbidden.
	segmentStatus = http.StatusForbidden
	res2, _, _ := h.ProbeStream(context.Background(), srv.URL+"/master.m3u8", "testsrc", nil)
	if res2.OK {
		t.Fatal("segment 403 must fail the probe")
	}
	if res2.Stage != hlsprobe.StageSegment || res2.SegmentStatus != http.StatusForbidden {
		t.Errorf("res2 = %+v", res2)
	}
	if res2.ManifestStatus != http.StatusOK {
		t.Errorf("manifest status should still read 200: %+v", res2)
	}
}

// A pre-minted /proxy/<token> url must be decrypted and probed against the REAL
// upstream, not against ourselves.
func TestProbeStreamResolvesProxyToken(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("\x00\x00\x00\x18ftypmp42"))
	}))
	defer upstream.Close()

	h := New(config.Config{}, nil)
	// decodeDirectTarget handles the url-escaped form used by SISI sources.
	token := url.PathEscape(upstream.URL + "/movie.mp4")
	res, final, _ := h.ProbeStream(context.Background(), "https://our.host/proxy/"+token, "testsrc", nil)

	if !res.OK || res.Stage != hlsprobe.StageDirect {
		t.Fatalf("res = %+v", res)
	}
	if final != upstream.URL+"/movie.mp4" {
		t.Errorf("probe did not resolve the token to the real upstream: %q", final)
	}
}
