package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCatalogAndTSStubRoutes(t *testing.T) {
	resetHTTPAPIGlobals(t)
	t.Setenv("LAMPAC_GO_REPO_ROOT", t.TempDir())
	// Force TorrServer proxy off so /ts serves the built-in stub instead of
	// reverse-proxying to a (nonexistent) backend on the default port 9080.
	t.Setenv("LAMPAC_GO_TORRSERVER_PORT", "0")

	srv, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	checkJSON := func(path string) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://lampac.local"+path, nil)
		srv.httpServer.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
		if ct := strings.ToLower(rec.Header().Get("Content-Type")); !strings.Contains(ct, "application/json") {
			t.Fatalf("%s content-type mismatch: %s", path, ct)
		}
		var out map[string]any
		if err := stdjson.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s invalid json: %v", path, err)
		}
		return out
	}

	_ = checkJSON("/catalog")
	_ = checkJSON("/catalog/catalog")
	list := checkJSON("/catalog/list")
	if _, ok := list["results"]; !ok {
		t.Fatalf("/catalog/list missing results: %+v", list)
	}
	_ = checkJSON("/catalog/card")

	{
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://lampac.local/ts", nil)
		srv.httpServer.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("/ts status=%d body=%s", rec.Code, rec.Body.String())
		}
		if ct := strings.ToLower(rec.Header().Get("Content-Type")); !strings.Contains(ct, "text/html") {
			t.Fatalf("/ts content-type mismatch: %s", ct)
		}
	}

	{
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://lampac.local/ts/echo", nil)
		srv.httpServer.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("/ts/echo status=%d body=%s", rec.Code, rec.Body.String())
		}
		if strings.TrimSpace(rec.Body.String()) != "MatriX.API (lampac-go)" {
			t.Fatalf("/ts/echo body mismatch: %q", rec.Body.String())
		}
	}

	{
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://lampac.local/ts/ts", nil)
		srv.httpServer.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("/ts/ts status=%d body=%s", rec.Code, rec.Body.String())
		}
	}

	{
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "http://lampac.local/ts/settings", strings.NewReader("{}"))
		srv.httpServer.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("POST /ts/settings status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
}
