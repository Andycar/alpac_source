package opensubs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"

	"github.com/go-chi/chi/v5"
)

// withChiParams attaches chi URL params to a request for handler unit tests.
func withChiParams(r *http.Request, kv map[string]string) *http.Request {
	rctx := chi.NewRouteContext()
	for k, v := range kv {
		rctx.URLParams.Add(k, v)
	}
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func TestOpenSubsRenditionPlaylist(t *testing.T) {
	svc := newOpenSubsService(config.Config{})

	// dur=25s, 10s segments → ceil(25/10)=3 segments.
	req := httptest.NewRequest(http.MethodGet, "/api/opensubs/rendition/123/index.m3u8?dur=25", nil)
	req = withChiParams(req, map[string]string{"fileID": "123"})
	rec := httptest.NewRecorder()
	svc.handleRendition(rec, req)

	body := rec.Body.String()
	if !strings.HasPrefix(body, "#EXTM3U") || !strings.Contains(body, "#EXT-X-ENDLIST") {
		t.Fatalf("not a valid HLS playlist:\n%s", body)
	}
	if n := strings.Count(body, "#EXTINF:"); n != 3 {
		t.Errorf("expected 3 segments, got %d:\n%s", n, body)
	}
	for _, want := range []string{"seg_0.vtt?dur=25", "seg_1.vtt?dur=25", "seg_2.vtt?dur=25", "#EXT-X-TARGETDURATION:10"} {
		if !strings.Contains(body, want) {
			t.Errorf("playlist missing %q:\n%s", want, body)
		}
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "mpegurl") {
		t.Errorf("wrong content-type: %q", ct)
	}
}

func TestOpenSubsRenditionUnknownDuration(t *testing.T) {
	svc := newOpenSubsService(config.Config{})
	req := httptest.NewRequest(http.MethodGet, "/api/opensubs/rendition/123/index.m3u8", nil)
	req = withChiParams(req, map[string]string{"fileID": "123"})
	rec := httptest.NewRecorder()
	svc.handleRendition(rec, req)
	// Unknown dur → a single big-window segment (still a valid playlist).
	if n := strings.Count(rec.Body.String(), "#EXTINF:"); n < 1 {
		t.Errorf("expected at least 1 segment:\n%s", rec.Body.String())
	}
}

func TestOpenSubsSegmentServesFragment(t *testing.T) {
	svc := newOpenSubsService(config.Config{})
	// Pre-seed the raw cache so fetchSub returns without hitting the network.
	srt := "1\n00:00:05,000 --> 00:00:15,000\nStraddling cue\n"
	svc.subs.set("123", []byte(srt))

	seg := func(i string) string {
		req := httptest.NewRequest(http.MethodGet, "/api/opensubs/rendition/123/seg_"+i+".vtt?dur=25", nil)
		req = withChiParams(req, map[string]string{"fileID": "123", "i": i})
		rec := httptest.NewRecorder()
		svc.handleSegment(rec, req)
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/vtt") {
			t.Errorf("seg %s wrong content-type: %q", i, ct)
		}
		return rec.Body.String()
	}

	// Segment 0 = [0,10): cue clipped to end at 10s.
	if s0 := seg("0"); !strings.Contains(s0, "Straddling cue") || !strings.Contains(s0, "00:00:05.000 --> 00:00:10.000") {
		t.Errorf("segment 0 wrong:\n%s", s0)
	}
	// Segment 1 = [10,20): cue clipped to start at 10s, end 15s.
	if s1 := seg("1"); !strings.Contains(s1, "Straddling cue") || !strings.Contains(s1, "00:00:10.000 --> 00:00:15.000") {
		t.Errorf("segment 1 wrong:\n%s", s1)
	}
	// Segment 2 = [20,25): no cues → empty but valid VTT.
	if s2 := seg("2"); !strings.HasPrefix(s2, "WEBVTT") || strings.Contains(s2, "Straddling cue") {
		t.Errorf("segment 2 should be empty VTT:\n%s", s2)
	}
}

func TestOpenSubsSegmentOutOfRange(t *testing.T) {
	svc := newOpenSubsService(config.Config{})
	svc.subs.set("123", []byte("1\n00:00:01,000 --> 00:00:02,000\nHi\n"))
	req := httptest.NewRequest(http.MethodGet, "/api/opensubs/rendition/123/seg_99.vtt?dur=25", nil)
	req = withChiParams(req, map[string]string{"fileID": "123", "i": "99"})
	rec := httptest.NewRecorder()
	svc.handleSegment(rec, req)
	// Out-of-range index → empty valid VTT, not an error.
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Body.String(), "WEBVTT") {
		t.Errorf("out-of-range should be empty VTT 200, got %d:\n%s", rec.Code, rec.Body.String())
	}
}
