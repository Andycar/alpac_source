package sisihttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestSisiListStubLocalCoreResponse(t *testing.T) {
	cfg := config.Config{}
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/xds", nil)
	rec := httptest.NewRecorder()

	sisiListStubHandler(cfg).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"list":[]`) || !strings.Contains(body, `"total_pages":1`) {
		t.Fatalf("unexpected list stub response: %s", body)
	}
}

func TestSisiViewStubRelatedAndM3U8(t *testing.T) {
	cfg := config.Config{}

	relatedReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/xds/vidosik?related=true", nil)
	relatedRec := httptest.NewRecorder()
	sisiViewStubHandler(cfg).ServeHTTP(relatedRec, relatedReq)
	if relatedRec.Code != http.StatusOK {
		t.Fatalf("unexpected related status: %d", relatedRec.Code)
	}
	if !strings.Contains(relatedRec.Body.String(), `"list":[]`) {
		t.Fatalf("unexpected related body: %s", relatedRec.Body.String())
	}

	m3uReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/bgs/potok.m3u8", nil)
	m3uRec := httptest.NewRecorder()
	sisiViewStubHandler(cfg).ServeHTTP(m3uRec, m3uReq)
	if m3uRec.Code != http.StatusOK {
		t.Fatalf("unexpected m3u8 status: %d", m3uRec.Code)
	}
	if !strings.Contains(m3uRec.Body.String(), "#EXTM3U") {
		t.Fatalf("expected m3u8 header, got: %s", m3uRec.Body.String())
	}
}
