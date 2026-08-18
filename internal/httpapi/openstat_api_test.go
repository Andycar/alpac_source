package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenstatDisabledMessage(t *testing.T) {
	writeOpenstatInitConf(t, `{"openstat":{"enable":false}}`)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/stats/request", nil)
	openstatRequestsHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Включите openstat") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestOpenstatTokenGuard(t *testing.T) {
	writeOpenstatInitConf(t, `{"openstat":{"enable":true,"token":"tok1"}}`)

	recDenied := httptest.NewRecorder()
	reqDenied := httptest.NewRequest(http.MethodGet, "http://lampac.local/stats/request", nil)
	openstatRequestsHandler().ServeHTTP(recDenied, reqDenied)

	if recDenied.Code != http.StatusOK {
		t.Fatalf("unexpected denied status: %d", recDenied.Code)
	}
	if !strings.Contains(recDenied.Body.String(), "Используйте /stats/request?token=my_key") {
		t.Fatalf("unexpected denied body: %s", recDenied.Body.String())
	}

	done := runtimeRequestStats.begin()
	done(120 * time.Millisecond)

	recOK := httptest.NewRecorder()
	reqOK := httptest.NewRequest(http.MethodGet, "http://lampac.local/stats/request?token=tok1", nil)
	openstatRequestsHandler().ServeHTTP(recOK, reqOK)

	if recOK.Code != http.StatusOK {
		t.Fatalf("unexpected status with token: %d", recOK.Code)
	}
	if ct := recOK.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("unexpected content type: %q", ct)
	}
	if !strings.Contains(recOK.Body.String(), `"req_min"`) {
		t.Fatalf("missing req_min in response: %s", recOK.Body.String())
	}
}

func writeOpenstatInitConf(t *testing.T, content string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", home)
	path := filepath.Join(home, "init.conf")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write init.conf: %v", err)
	}
}
