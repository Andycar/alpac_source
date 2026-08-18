//go:build torrs

package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"
)

// serve routes a GET through a chi router so chi.URLParam("size") resolves.
func serveDownload(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/ts/download/{size}", tsDirectDownloadHandler())
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// TestTSDownloadSpeedTest covers MatriX's /download/{MB} speed-test endpoint
// that Lampa's "Тест скорости" hits. Without it the /ts/* catch-all answered
// "{}" and the gauge showed a meaningless number.
func TestTSDownloadSpeedTest(t *testing.T) {
	// 1 MB streams exactly 1 MB with a matching Content-Length.
	rec := serveDownload(t, "/ts/download/1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	want := int64(1) << 20
	if got := rec.Header().Get("Content-Length"); got != strconv.FormatInt(want, 10) {
		t.Errorf("Content-Length = %q, want %d", got, want)
	}
	if int64(rec.Body.Len()) != want {
		t.Errorf("body = %d bytes, want %d", rec.Body.Len(), want)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type = %q", ct)
	}

	// Oversized request is capped, not honored verbatim (abuse bound).
	rec = serveDownload(t, "/ts/download/99999")
	capped := int64(maxTSDownloadMB) << 20
	if int64(rec.Body.Len()) != capped {
		t.Errorf("capped body = %d bytes, want %d", rec.Body.Len(), capped)
	}

	// Garbage / zero size degrades to 1 MB rather than 0 or a negative length.
	rec = serveDownload(t, "/ts/download/abc")
	if int64(rec.Body.Len()) != 1<<20 {
		t.Errorf("non-numeric size body = %d bytes, want %d", rec.Body.Len(), 1<<20)
	}

	// Payload must be incompressible-ish: a run of identical bytes would let a
	// transparent gzip layer inflate the measured speed. Assert the block is
	// not all-zero.
	body := rec.Body.Bytes()
	allZero := true
	for _, b := range body[:4096] {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Error("payload starts with 4 KiB of zeros — compressible, would skew the speed test")
	}
}

// TestTSDownloadClientAbort verifies the stream stops when the client goes
// away (Lampa aborts the XHR after 10s / 300 MB) instead of pushing the whole
// payload into a dead connection.
func TestTSDownloadClientAbort(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already-aborted client

	r := chi.NewRouter()
	r.Get("/ts/download/{size}", tsDirectDownloadHandler())
	req := httptest.NewRequest(http.MethodGet, "/ts/download/512", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Body.Len() != 0 {
		t.Errorf("aborted client received %d bytes, want 0", rec.Body.Len())
	}
}
