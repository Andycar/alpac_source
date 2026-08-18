package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lampac-go/internal/config"
	"lampac-go/internal/modules"
)

func TestResolveLampaType(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "init.conf"), []byte(`{"LampaWeb":{"path":"lampa-main","index":"lampa-main/index.html"}}`), 0o644); err != nil {
		t.Fatalf("write init.conf: %v", err)
	}

	reqByPath := httptest.NewRequest(http.MethodGet, "http://lampac.local/x/app.min.js?type=demo", nil)
	if got := resolveLampaType(reqByPath, root); got != "demo" {
		t.Fatalf("expected demo from query param, got %q", got)
	}

	reqDefault := httptest.NewRequest(http.MethodGet, "http://lampac.local/app.min.js", nil)
	if got := resolveLampaType(reqDefault, root); got != "lampa-main" {
		t.Fatalf("expected lampa-main from init.conf path, got %q", got)
	}
}

func TestAppMinJSHandler(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", home)

	_ = os.WriteFile(filepath.Join(home, "init.conf"), []byte(`{
	  "LampaWeb":{"path":"lampa-main"},
	  "playerInner":"vlc://",
	  "transcoding":{"enable":false}
	}`), 0o644)

	appDir := filepath.Join(root, "wwwroot", "lampa-main")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatalf("mkdir app dir: %v", err)
	}
	pluginsDir := filepath.Join(root, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatalf("mkdir plugins dir: %v", err)
	}

	app := `A=http://lite.lampa.mx B=https://yumata.github.io/lampa-lite C=http://lampa.mx D=https://yumata.github.io/lampa E={localhost} J={jachost} Player.play(element);`
	if err := os.WriteFile(filepath.Join(appDir, "app.min.js"), []byte(app), 0o644); err != nil {
		t.Fatalf("write app.min.js: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginsDir, "player-inner.js"), []byte(`PI {useplayer} {notUseTranscoding}`), 0o644); err != nil {
		t.Fatalf("write player-inner.js: %v", err)
	}

	cfg := config.Config{Compat: config.CompatConfig{RepoRoot: root}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/app.min.js", nil)
	appMinJSHandler(cfg, []modules.RootModule{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "http://lampac.local/lampa-main") {
		t.Fatalf("host replacements not applied: %s", body)
	}
	if !strings.Contains(body, "PI true true") {
		t.Fatalf("player-inner snippet was not injected: %s", body)
	}
}

func TestAppCSSHandler(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", home)
	_ = os.WriteFile(filepath.Join(home, "init.conf"), []byte(`{"LampaWeb":{"index":"lampa-main/index.html"}}`), 0o644)

	cssDir := filepath.Join(root, "wwwroot", "lampa-main", "css")
	if err := os.MkdirAll(cssDir, 0o755); err != nil {
		t.Fatalf("mkdir css dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cssDir, "app.css"), []byte(`A:{localhost};B:{host};`), 0o644); err != nil {
		t.Fatalf("write app.css: %v", err)
	}

	cfg := config.Config{Compat: config.CompatConfig{RepoRoot: root}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/css/app.css", nil)
	appCSSHandler(cfg).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "http://lampac.local") || !strings.Contains(body, "lampac.local") {
		t.Fatalf("localhost/host replacements missing: %s", body)
	}
}

func TestAppAssetFallbackWhenMissing(t *testing.T) {
	cfg := config.Config{Compat: config.CompatConfig{RepoRoot: t.TempDir()}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/app.min.js?type=x", nil)
	appMinJSHandler(cfg, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 on missing asset, got %d", rec.Code)
	}
}
