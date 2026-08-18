package httpapi

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRchCheckConnectedFallbackMessage(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/rch/check/connected", nil)
	rchCheckConnectedHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"rch":true`)) {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"ws":"http://lampac.local/ws"`)) || !bytes.Contains(rec.Body.Bytes(), []byte(`"nws":"ws://lampac.local/nws"`)) {
		t.Fatalf("unexpected connection payload: %s", rec.Body.String())
	}
}

func TestRchResultHandlers(t *testing.T) {
	recNoID := httptest.NewRecorder()
	reqNoID := httptest.NewRequest(http.MethodPost, "http://lampac.local/rch/result", bytes.NewBufferString("x"))
	rchResultHandler().ServeHTTP(recNoID, reqNoID)
	if recNoID.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for missing id, got %d", recNoID.Code)
	}

	recUnknown := httptest.NewRecorder()
	reqUnknown := httptest.NewRequest(http.MethodPost, "http://lampac.local/rch/result?id=missing", bytes.NewBufferString("x"))
	rchResultHandler().ServeHTTP(recUnknown, reqUnknown)
	if recUnknown.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown id, got %d", recUnknown.Code)
	}

	id := "rch-test-id"
	entry := rchRegisterPending(id)
	defer rchDeletePending(id)

	recOK := httptest.NewRecorder()
	reqOK := httptest.NewRequest(http.MethodPost, "http://lampac.local/rch/result?id="+id, bytes.NewBufferString("plain-body"))
	rchResultHandler().ServeHTTP(recOK, reqOK)
	if recOK.Code != http.StatusOK {
		t.Fatalf("unexpected result status: %d", recOK.Code)
	}
	if !rchWaitDone(entry, time.Second) {
		t.Fatalf("pending entry did not complete")
	}
	if got := string(rchPendingBytes(entry)); got != "plain-body" {
		t.Fatalf("unexpected pending payload: %q", got)
	}

	idGz := "rch-test-gz"
	entryGz := rchRegisterPending(idGz)
	defer rchDeletePending(idGz)

	var gzBody bytes.Buffer
	zw := gzip.NewWriter(&gzBody)
	_, _ = zw.Write([]byte("gzip-body"))
	_ = zw.Close()

	recGz := httptest.NewRecorder()
	reqGz := httptest.NewRequest(http.MethodPost, "http://lampac.local/rch/gzresult?id="+idGz, &gzBody)
	rchGzipResultHandler().ServeHTTP(recGz, reqGz)
	if recGz.Code != http.StatusOK {
		t.Fatalf("unexpected gzresult status: %d", recGz.Code)
	}
	if !rchWaitDone(entryGz, time.Second) {
		t.Fatalf("gzip pending entry did not complete")
	}
	if got := string(rchPendingBytes(entryGz)); got != "gzip-body" {
		t.Fatalf("unexpected gzip pending payload: %q", got)
	}
}
