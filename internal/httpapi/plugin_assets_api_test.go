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

func TestPluginAssetsInvcRchAndWs(t *testing.T) {
	root := t.TempDir()
	pluginsDir := filepath.Join(root, "plugins")
	if err := os.MkdirAll(filepath.Join(pluginsDir, "sync_v2"), 0o755); err != nil {
		t.Fatalf("mkdir plugins: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginsDir, "invc-rch.js"), []byte("const h='{localhost}';"), 0o644); err != nil {
		t.Fatalf("write invc-rch: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginsDir, "invc-rch_nws.js"), []byte("const n='nws';"), 0o644); err != nil {
		t.Fatalf("write invc-rch_nws: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginsDir, "invc-ws.js"), []byte("v1 {invc-rch} {invc-rch_nws} {localhost} {token}"), 0o644); err != nil {
		t.Fatalf("write invc-ws: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginsDir, "sync_v2", "invc-ws.js"), []byte("v2 {invc-rch} {invc-rch_nws} {localhost} {token}"), 0o644); err != nil {
		t.Fatalf("write sync_v2/invc-ws: %v", err)
	}

	home := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "init.conf"), []byte(`{"sync_user":{"version":2}}`), 0o644); err != nil {
		t.Fatalf("write init.conf: %v", err)
	}

	cfg := config.Config{Compat: config.CompatConfig{RepoRoot: root}}

	recRch := httptest.NewRecorder()
	reqRch := httptest.NewRequest(http.MethodGet, "http://lampac.local/invc-rch.js", nil)
	invcRchJSHandler(cfg).ServeHTTP(recRch, reqRch)
	if recRch.Code != http.StatusOK {
		t.Fatalf("unexpected invc-rch status: %d", recRch.Code)
	}
	if !strings.Contains(recRch.Body.String(), "http://lampac.local") {
		t.Fatalf("localhost not replaced in invc-rch: %s", recRch.Body.String())
	}
	if !strings.HasPrefix(recRch.Body.String(), "(function(){'use strict';") {
		t.Fatalf("invc-rch should be wrapped: %s", recRch.Body.String())
	}

	recWs := httptest.NewRecorder()
	reqWs := httptest.NewRequest(http.MethodGet, "http://lampac.local/invc-ws.js?token=tok%201", nil)
	invcWsJSHandler(cfg).ServeHTTP(recWs, reqWs)
	if recWs.Code != http.StatusOK {
		t.Fatalf("unexpected invc-ws status: %d", recWs.Code)
	}
	body := recWs.Body.String()
	if !strings.Contains(body, "v2") {
		t.Fatalf("expected sync_v2 template: %s", body)
	}
	if !strings.Contains(body, "tok+1") {
		t.Fatalf("token was not query-escaped: %s", body)
	}
}

func TestPluginAssetsInvcWsVersion1(t *testing.T) {
	root := t.TempDir()
	pluginsDir := filepath.Join(root, "plugins")
	if err := os.MkdirAll(filepath.Join(pluginsDir, "sync_v2"), 0o755); err != nil {
		t.Fatalf("mkdir plugins: %v", err)
	}
	_ = os.WriteFile(filepath.Join(pluginsDir, "invc-rch.js"), []byte("rch"), 0o644)
	_ = os.WriteFile(filepath.Join(pluginsDir, "invc-rch_nws.js"), []byte("nws"), 0o644)
	_ = os.WriteFile(filepath.Join(pluginsDir, "invc-ws.js"), []byte("v1"), 0o644)
	_ = os.WriteFile(filepath.Join(pluginsDir, "sync_v2", "invc-ws.js"), []byte("v2"), 0o644)

	home := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", home)
	_ = os.WriteFile(filepath.Join(home, "init.conf"), []byte(`{"sync_user":{"version":1}}`), 0o644)

	cfg := config.Config{Compat: config.CompatConfig{RepoRoot: root}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/invc-ws.js", nil)
	invcWsJSHandler(cfg).ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "v1") {
		t.Fatalf("expected v1 template, got: %s", rec.Body.String())
	}
}

func TestPluginAssetsNWSStartpageMSXAndPersonal(t *testing.T) {
	root := t.TempDir()
	pluginsDir := filepath.Join(root, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatalf("mkdir plugins: %v", err)
	}
	_ = os.WriteFile(filepath.Join(pluginsDir, "nws-client-es5.js"), []byte("nws {localhost}"), 0o644)
	_ = os.WriteFile(filepath.Join(pluginsDir, "signalr-6.0.25_es5.js"), []byte("signalr"), 0o644)
	_ = os.WriteFile(filepath.Join(pluginsDir, "startpage.js"), []byte("start {localhost}"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "msx.json"), []byte(`{"url":"{localhost}/x"}`), 0o644)

	cfg := config.Config{Compat: config.CompatConfig{RepoRoot: root}}

	recNWS := httptest.NewRecorder()
	reqNWS := httptest.NewRequest(http.MethodGet, "http://lampac.local/nws-client-es5.js", nil)
	nwsClientJSHandler(cfg).ServeHTTP(recNWS, reqNWS)
	if !strings.Contains(recNWS.Body.String(), "http://lampac.local") {
		t.Fatalf("nws localhost not replaced: %s", recNWS.Body.String())
	}

	recSignal := httptest.NewRecorder()
	reqSignal := httptest.NewRequest(http.MethodGet, "http://lampac.local/signalr-6.0.25_es5.js", nil)
	signalrES5Handler(cfg).ServeHTTP(recSignal, reqSignal)
	if recSignal.Code != http.StatusOK || !strings.Contains(recSignal.Body.String(), "signalr") {
		t.Fatalf("unexpected signalr response: %d %s", recSignal.Code, recSignal.Body.String())
	}

	recStart := httptest.NewRecorder()
	reqStart := httptest.NewRequest(http.MethodGet, "http://lampac.local/startpage.js", nil)
	startpageJSHandler(cfg).ServeHTTP(recStart, reqStart)
	if !strings.Contains(recStart.Body.String(), "http://lampac.local") {
		t.Fatalf("startpage localhost not replaced: %s", recStart.Body.String())
	}

	recMSX := httptest.NewRecorder()
	reqMSX := httptest.NewRequest(http.MethodGet, "http://lampac.local/msx/start.json", nil)
	msxStartJSONHandler(cfg).ServeHTTP(recMSX, reqMSX)
	if !strings.Contains(recMSX.Body.String(), "http://lampac.local/x") {
		t.Fatalf("msx localhost not replaced: %s", recMSX.Body.String())
	}

	recPersonal := httptest.NewRecorder()
	reqPersonal := httptest.NewRequest(http.MethodGet, "http://lampac.local/personal.lampa", nil)
	personalLampaHandler().ServeHTTP(recPersonal, reqPersonal)
	if recPersonal.Code != http.StatusOK {
		t.Fatalf("unexpected personal.lampa status: %d", recPersonal.Code)
	}
}

func TestMSXStartJSONFallbackDefault(t *testing.T) {
	root := t.TempDir()
	// No msx.json file — handler should serve the built-in default.
	cfg := config.Config{
		Compat: config.CompatConfig{RepoRoot: root},
		Capi:   config.CapiConfig{AppDir: t.TempDir()},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "https://myserver.tv/msx/start.json", nil)
	msxStartJSONHandler(cfg).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	body := rec.Body.String()

	// {localhost} should be replaced with the request host.
	if strings.Contains(body, "{localhost}") {
		t.Fatal("template variable {localhost} was not replaced")
	}
	if !strings.Contains(body, "https://myserver.tv") {
		t.Fatalf("host not injected into response: %s", body)
	}

	// Default launches OUR app (the /app SPA) plus the classic Lampa entry.
	if !strings.Contains(body, "link:https://myserver.tv/app") {
		t.Fatalf("default msx.json missing ALPAC /app link: %s", body)
	}
	if !strings.Contains(body, "Lampa") {
		t.Fatalf("default msx.json missing Lampa entry: %s", body)
	}

	// The built-in default is never persisted — writing it to disk is what
	// froze v1 defaults on servers forever.
	if _, err := os.Stat(filepath.Join(root, "msx.json")); !os.IsNotExist(err) {
		t.Fatal("built-in msx default must not be persisted to disk")
	}
}

func TestMSXStartJSONLampaOnlyWithoutAppDir(t *testing.T) {
	root := t.TempDir()
	// No /app SPA configured → the launcher must not offer a dead ALPAC tile.
	cfg := config.Config{Compat: config.CompatConfig{RepoRoot: root}}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "https://myserver.tv/msx/start.json", nil)
	msxStartJSONHandler(cfg).ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "myserver.tv/app") {
		t.Fatalf("ALPAC /app tile offered without app_dir configured: %s", body)
	}
	if !strings.Contains(body, "link:https://myserver.tv") {
		t.Fatalf("Lampa-only msx.json missing root link: %s", body)
	}
}

func TestMSXStartJSONAppHostGetsAppOnlyLauncher(t *testing.T) {
	cfg := config.Config{}
	cfg.Compat.RepoRoot = t.TempDir()
	cfg.Capi.AppDir = t.TempDir()
	cfg.Capi.PublicHost = "https://tv.alcopa.cc"

	// MSX start parameter = the app domain (over http — old TVs): pure ALPAC
	// launcher, no Lampa tile, links keep the request scheme.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://tv.alcopa.cc/msx/start.json", nil)
	msxStartJSONHandler(cfg).ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, "link:http://tv.alcopa.cc/app") {
		t.Fatalf("app-only launcher missing /app link: %s", body)
	}
	if strings.Contains(body, "Lampa") {
		t.Fatalf("app host must not offer a Lampa tile: %s", body)
	}

	// Same instance, other host (the Lampa entry point) → full launcher.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "http://beta.l-vid.online:888/msx/start.json", nil)
	msxStartJSONHandler(cfg).ServeHTTP(rec2, req2)
	body2 := rec2.Body.String()
	if !strings.Contains(body2, "link:http://beta.l-vid.online:888/app") || !strings.Contains(body2, "Lampa") {
		t.Fatalf("lampa host must offer ALPAC+Lampa tiles: %s", body2)
	}

	// X-Forwarded-Host (nginx in front) must match the app host too.
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9118/msx/start.json", nil)
	req3.Header.Set("X-Forwarded-Host", "tv.alcopa.cc")
	req3.Header.Set("X-Forwarded-Proto", "http")
	msxStartJSONHandler(cfg).ServeHTTP(rec3, req3)
	if body3 := rec3.Body.String(); strings.Contains(body3, "Lampa") {
		t.Fatalf("forwarded app host must get the app-only launcher: %s", body3)
	}
}

func TestMSXStartJSONIgnoresStaleV1Default(t *testing.T) {
	root := t.TempDir()
	// A v1 auto-default persisted by an older build (both markers present) is
	// generated content, not an admin override — serve the current built-in
	// launcher instead. The file itself is left alone (no disk writes on GET).
	staleV1 := `{"name":"Загрузчик приложений","pages":[{"items":[{"label":"FXMLPlayer"}]}]}`
	if err := os.WriteFile(filepath.Join(root, "msx.json"), []byte(staleV1), 0o644); err != nil {
		t.Fatalf("write stale msx.json: %v", err)
	}
	cfg := config.Config{
		Compat: config.CompatConfig{RepoRoot: root},
		Capi:   config.CapiConfig{AppDir: t.TempDir()},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "https://myserver.tv/msx/start.json", nil)
	msxStartJSONHandler(cfg).ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "FXMLPlayer") {
		t.Fatalf("stale v1 default was served instead of the built-in: %s", body)
	}
	if !strings.Contains(body, "link:https://myserver.tv/app") {
		t.Fatalf("built-in msx.json missing ALPAC /app link: %s", body)
	}
}

func TestMSXStartJSONKeepsCustomFile(t *testing.T) {
	root := t.TempDir()
	// An admin-customized msx.json (no v1 marker pair) must be served verbatim.
	custom := `{"name":"Мой сервер","pages":[]}`
	if err := os.WriteFile(filepath.Join(root, "msx.json"), []byte(custom), 0o644); err != nil {
		t.Fatalf("write custom msx.json: %v", err)
	}
	cfg := config.Config{Compat: config.CompatConfig{RepoRoot: root}}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "https://myserver.tv/msx/start.json", nil)
	msxStartJSONHandler(cfg).ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), "Мой сервер") {
		t.Fatalf("custom msx.json was not served: %s", rec.Body.String())
	}
}
