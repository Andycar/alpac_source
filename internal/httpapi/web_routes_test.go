package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtensionsHandler(t *testing.T) {
	root := t.TempDir()
	pluginsDir := filepath.Join(root, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatalf("mkdir plugins: %v", err)
	}
	content := `{"url":"{localhost}/on.js"}`
	if err := os.WriteFile(filepath.Join(pluginsDir, "extensions.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("write extensions: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/extensions", nil)
	rec := httptest.NewRecorder()
	extensionsHandler(root, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "http://lampac.local/on.js") {
		t.Fatalf("host placeholder not replaced: %s", body)
	}
}

func TestWeblogHandlerGuardsAndHTML(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", home)

	if err := os.WriteFile(filepath.Join(home, "init.conf"), []byte(`{"weblog":{"enable":false}}`), 0o644); err != nil {
		t.Fatalf("write init.conf: %v", err)
	}
	recDisabled := httptest.NewRecorder()
	reqDisabled := httptest.NewRequest(http.MethodGet, "http://lampac.local/weblog", nil)
	weblogHandler().ServeHTTP(recDisabled, reqDisabled)
	if !strings.Contains(recDisabled.Body.String(), "Включите weblog") {
		t.Fatalf("unexpected disabled response: %s", recDisabled.Body.String())
	}

	if err := os.WriteFile(filepath.Join(home, "init.conf"), []byte(`{"weblog":{"enable":true,"token":"tok1"}}`), 0o644); err != nil {
		t.Fatalf("rewrite init.conf: %v", err)
	}
	recDenied := httptest.NewRecorder()
	reqDenied := httptest.NewRequest(http.MethodGet, "http://lampac.local/weblog", nil)
	weblogHandler().ServeHTTP(recDenied, reqDenied)
	if !strings.Contains(recDenied.Body.String(), "Используйте /weblog?token=my_key") {
		t.Fatalf("unexpected token guard response: %s", recDenied.Body.String())
	}

	recOK := httptest.NewRecorder()
	reqOK := httptest.NewRequest(http.MethodGet, "http://lampac.local/weblog?token=tok1&receive=request", nil)
	weblogHandler().ServeHTTP(recOK, reqOK)
	if recOK.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", recOK.Code)
	}
	if !strings.Contains(recOK.Body.String(), "/js/nws-client-es5.js") {
		t.Fatalf("missing nws script in html")
	}
	if !strings.Contains(recOK.Body.String(), "RegistryWebLog") {
		t.Fatalf("missing RegistryWebLog hook in html")
	}
}

func TestExtensionsFallbackWhenTemplateMissing(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/extensions", nil)
	rec := httptest.NewRecorder()

	extensionsHandler(filepath.Join(t.TempDir(), "missing"), nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 without legacy, got %d", rec.Code)
	}
}
