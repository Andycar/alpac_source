package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestLampaIndexAutoDetectServesInline(t *testing.T) {
	root := t.TempDir()
	www := filepath.Join(root, "wwwroot", "lampa-main")
	if err := os.MkdirAll(www, 0o755); err != nil {
		t.Fatalf("mkdir wwwroot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(www, "index.html"), []byte(`<html><head><title>x</title></head><body>ok</body></html>`), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "init.conf"), []byte(`{"LampaWeb":{"index":""}}`), 0o644); err != nil {
		t.Fatalf("write init.conf: %v", err)
	}

	cfg := config.Config{Compat: config.CompatConfig{RepoRoot: root}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/", nil)
	req.Header.Set("Accept", "text/html") // browsers send this; MSX/non-browser clients don't and get JSON
	lampaIndexHandler(cfg).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `<base href="/" />`) {
		t.Fatalf("base tag not injected: %s", body)
	}
	if !strings.Contains(body, `<script src="/lampainit.js"></script>`) {
		t.Fatalf("lampainit.js not injected: %s", body)
	}
}

func TestLampaIndexBaseTagMode(t *testing.T) {
	root := t.TempDir()
	www := filepath.Join(root, "wwwroot", "lampa-main")
	if err := os.MkdirAll(www, 0o755); err != nil {
		t.Fatalf("mkdir wwwroot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "init.conf"), []byte(`{"LampaWeb":{"index":"lampa-main/index.html","basetag":true}}`), 0o644); err != nil {
		t.Fatalf("write init.conf: %v", err)
	}
	if err := os.WriteFile(filepath.Join(www, "index.html"), []byte(`<html><head><title>x</title></head><body>ok</body></html>`), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}

	cfg := config.Config{Compat: config.CompatConfig{RepoRoot: root}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/", nil)
	req.Header.Set("Accept", "text/html") // browsers send this; MSX/non-browser clients don't and get JSON
	lampaIndexHandler(cfg).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `<base href="/" />`) {
		t.Fatalf("base tag not injected: %s", body)
	}
	if !strings.Contains(body, `<script src="/lampainit.js"></script>`) {
		t.Fatalf("lampainit.js not injected: %s", body)
	}
}

func TestLampaIndexAPIWorkWhenNoDefaultFound(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "init.conf"), []byte(`{"LampaWeb":{"index":""}}`), 0o644); err != nil {
		t.Fatalf("write init.conf: %v", err)
	}

	cfg := config.Config{Compat: config.CompatConfig{RepoRoot: root}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/", nil)
	req.Header.Set("Accept", "text/html") // browsers send this; MSX/non-browser clients don't and get JSON
	lampaIndexHandler(cfg).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if strings.TrimSpace(rec.Body.String()) != "api work" {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestLampaIndexServesInlineWhenIndexSet(t *testing.T) {
	root := t.TempDir()
	www := filepath.Join(root, "wwwroot", "lampa-main")
	if err := os.MkdirAll(www, 0o755); err != nil {
		t.Fatalf("mkdir wwwroot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "init.conf"), []byte(`{"LampaWeb":{"index":"lampa-main/index.html","basetag":false}}`), 0o644); err != nil {
		t.Fatalf("write init.conf: %v", err)
	}
	if err := os.WriteFile(filepath.Join(www, "index.html"), []byte(`<html><head><title>x</title></head><body>ok</body></html>`), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}

	cfg := config.Config{Compat: config.CompatConfig{RepoRoot: root}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/", nil)
	req.Header.Set("Accept", "text/html") // browsers send this; MSX/non-browser clients don't and get JSON
	lampaIndexHandler(cfg).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `<base href="/" />`) {
		t.Fatalf("base tag not injected: %s", body)
	}
}

func TestLampaIndexSkipsLampainitIfAlreadyPresent(t *testing.T) {
	root := t.TempDir()
	www := filepath.Join(root, "wwwroot", "lampa-main")
	if err := os.MkdirAll(www, 0o755); err != nil {
		t.Fatalf("mkdir wwwroot: %v", err)
	}
	html := `<html><head></head><body><script src="/lampainit.js"></script></body></html>`
	if err := os.WriteFile(filepath.Join(www, "index.html"), []byte(html), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "init.conf"), []byte(`{"LampaWeb":{"index":"lampa-main/index.html"}}`), 0o644); err != nil {
		t.Fatalf("write init.conf: %v", err)
	}

	cfg := config.Config{Compat: config.CompatConfig{RepoRoot: root}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/", nil)
	req.Header.Set("Accept", "text/html") // browsers send this; MSX/non-browser clients don't and get JSON
	lampaIndexHandler(cfg).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	body := rec.Body.String()
	// Should NOT have double injection
	count := strings.Count(body, `lampainit.js`)
	if count != 1 {
		t.Fatalf("lampainit.js appears %d times (expected 1): %s", count, body)
	}
}
