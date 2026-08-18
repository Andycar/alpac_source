package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNotFoundServesWWWRootStatic(t *testing.T) {
	resetHTTPAPIGlobals(t)
	root := t.TempDir()
	assetPath := filepath.Join(root, "wwwroot", "lampa-main", "vender", "jquery")
	if err := os.MkdirAll(assetPath, 0o755); err != nil {
		t.Fatalf("mkdir asset path: %v", err)
	}
	if err := os.WriteFile(filepath.Join(assetPath, "jquery.js"), []byte("window.jQuery={};"), 0o644); err != nil {
		t.Fatalf("write asset: %v", err)
	}

	t.Setenv("LAMPAC_GO_REPO_ROOT", root)

	srv, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lampa-main/vender/jquery/jquery.js", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "window.jQuery") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}
