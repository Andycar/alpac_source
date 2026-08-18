package httpx

import (
	"net/http/httptest"
	"testing"
)

func TestWriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteJSON(rec, 201, map[string]any{"ok": true, "n": 5})
	if rec.Code != 201 {
		t.Errorf("code = %d, want 201", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("content-type = %q", ct)
	}
	if body := rec.Body.String(); body != "{\"n\":5,\"ok\":true}\n" {
		t.Errorf("body = %q", body)
	}
}

func TestWriteHTML(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteHTML(rec, 200, "<h1>hi</h1>")
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("content-type = %q", ct)
	}
	if rec.Body.String() != "<h1>hi</h1>" {
		t.Errorf("body = %q", rec.Body.String())
	}
}
