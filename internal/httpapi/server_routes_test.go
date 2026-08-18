package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServerPluginRouteAliases(t *testing.T) {
	resetHTTPAPIGlobals(t)
	pluginsDir := t.TempDir()

	onTemplate := `window._plugins=[{plugins}];window._country="{country}";window._host="{localhost}";`
	liteTemplate := `window._lite="{localhost}";window._token="{token}";`
	genericTemplate := `window._endpoint="{localhost}";window._token="{token}";`

	if err := os.WriteFile(filepath.Join(pluginsDir, "on.js"), []byte(onTemplate), 0o644); err != nil {
		t.Fatalf("write on.js: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginsDir, "lite.js"), []byte(liteTemplate), 0o644); err != nil {
		t.Fatalf("write lite.js: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginsDir, "online.js"), []byte(genericTemplate), 0o644); err != nil {
		t.Fatalf("write online.js: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(pluginsDir, "sync_v2"), 0o755); err != nil {
		t.Fatalf("mkdir sync_v2: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginsDir, "sync.js"), []byte(`sync-v1 {sync-invc} {localhost} {token}`), 0o644); err != nil {
		t.Fatalf("write sync.js: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginsDir, "sync_lite.js"), []byte(`sync-lite {sync-invc} {localhost} {token}`), 0o644); err != nil {
		t.Fatalf("write sync_lite.js: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginsDir, "sync_v2", "sync.js"), []byte(`sync-v2 {sync-invc} {localhost} {token}`), 0o644); err != nil {
		t.Fatalf("write sync_v2/sync.js: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginsDir, "sync-invc.js"), []byte(`SYNC_INVC_SNIPPET`), 0o644); err != nil {
		t.Fatalf("write sync-invc.js: %v", err)
	}
	for _, name := range []string{
		"backup.js",
		"bookmark.js",
		"timecode.js",
		"tmdbproxy.js",
		"cubproxy.js",
		"sisi.js",
		"dlna.js",
		"tracks.js",
		"transcoding.js",
		"ts.js",
		"catalog.js",
		"torrent_styles_v2.js",
	} {
		if err := os.WriteFile(filepath.Join(pluginsDir, name), []byte(genericTemplate), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	t.Setenv("LAMPAC_GO_PLUGINS_DIR", pluginsDir)
	t.Setenv("LAMPAC_GO_REPO_ROOT", t.TempDir())

	srv, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	checkJS := func(path string, mustContain []string) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://lampac.local"+path, nil)
		srv.httpServer.Handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s unexpected status: %d body=%s", path, rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/javascript") {
			t.Fatalf("%s unexpected content-type: %s", path, ct)
		}
		body := rec.Body.String()
		for _, part := range mustContain {
			if !strings.Contains(body, part) {
				t.Fatalf("%s expected body to contain %q, got: %s", path, part, body)
			}
		}
	}

	checkJS("/online.js", []string{`window._endpoint="http://lampac.local"`})
	checkJS("/online/js/tok123", []string{`window._token="tok123"`})
	checkJS("/lite/js", []string{`window._lite="http://lampac.local/lite"`})
	checkJS("/lite/js/tok123", []string{`window._token="tok123"`})
	checkJS("/sync.js", []string{`sync-v2`, `SYNC_INVC_SNIPPET`, `http://lampac.local`})
	checkJS("/sync/js/tok123", []string{`sync-v2`, `tok123`})
	checkJS("/backup.js", []string{`window._endpoint="http://lampac.local"`})
	checkJS("/bookmark.js", []string{`window._endpoint="http://lampac.local"`})
	checkJS("/timecode.js", []string{`window._endpoint="http://lampac.local"`})
	checkJS("/tmdbproxy.js", []string{`window._endpoint="http://lampac.local"`})
	checkJS("/cubproxy.js", []string{`window._endpoint="http://lampac.local"`})
	checkJS("/sisi.js", []string{`window._endpoint="http://lampac.local"`})
	checkJS("/dlna.js", []string{`window._endpoint="http://lampac.local"`})
	checkJS("/tracks.js", []string{`window._endpoint="http://lampac.local"`})
	checkJS("/transcoding.js", []string{`window._endpoint="http://lampac.local"`})
	checkJS("/ts.js", []string{`window._endpoint="http://lampac.local"`})
	checkJS("/catalog.js", []string{`window._endpoint="http://lampac.local"`})
	checkJS("/torrent_styles_v2.js", []string{`window._endpoint="http://lampac.local"`})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/_lampac/routes/local", nil)
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/_lampac/routes/local unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	var payload struct {
		Total  int              `json:"total"`
		Routes []LocalRouteSpec `json:"routes"`
	}
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid routes/local payload: %v", err)
	}
	if payload.Total == 0 || len(payload.Routes) == 0 {
		t.Fatalf("unexpected empty routes/local payload: %+v", payload)
	}

	has := func(path string) bool {
		for _, r := range payload.Routes {
			if r.Path == path {
				return true
			}
		}
		return false
	}
	if !has("/online.js") || !has("/online/js/{token}") || !has("/lite/js") || !has("/sync.js") || !has("/sync/js/{token}") || !has("/bookmark.js") || !has("/timecode.js") || !has("/tmdbproxy.js") || !has("/cubproxy.js") || !has("/sisi.js") || !has("/dlna.js") || !has("/tracks.js") || !has("/transcoding.js") || !has("/ts.js") || !has("/catalog.js") || !has("/torrent_styles_v2.js") {
		t.Fatalf("routes/local missing expected plugin aliases: %+v", payload.Routes)
	}
}

// TestServerPluginShortPathAlias verifies the optional [web] plugin_short_path
// config registers a short alias (e.g. /m) that serves /on.js in external-plugin
// mode (only online.js et al, never tmdbproxy/sync that would break a host Lampa).
func TestServerPluginShortPathAlias(t *testing.T) {
	resetHTTPAPIGlobals(t)
	pluginsDir := t.TempDir()
	repoRoot := t.TempDir()

	onTemplate := `window._plugins=[{plugins}];window._country="{country}";window._host="{localhost}";`
	genericTemplate := `window._endpoint="{localhost}";window._token="{token}";`
	if err := os.WriteFile(filepath.Join(pluginsDir, "on.js"), []byte(onTemplate), 0o644); err != nil {
		t.Fatalf("write on.js: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginsDir, "online.js"), []byte(genericTemplate), 0o644); err != nil {
		t.Fatalf("write online.js: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "config.toml"), []byte("[web]\nplugin_short_path = \"m\"\n"), 0o644); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}

	t.Setenv("LAMPAC_GO_PLUGINS_DIR", pluginsDir)
	t.Setenv("LAMPAC_GO_REPO_ROOT", repoRoot)

	srv, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://lampac.local"+path, nil)
		srv.httpServer.Handler.ServeHTTP(rec, req)
		return rec
	}

	short := get("/m")
	if short.Code != http.StatusOK {
		t.Fatalf("/m unexpected status: %d body=%s", short.Code, short.Body.String())
	}
	if ct := short.Header().Get("Content-Type"); !strings.Contains(ct, "application/javascript") {
		t.Fatalf("/m unexpected content-type: %s", ct)
	}
	// External-plugin mode: online.js present, tmdbproxy absent.
	if !strings.Contains(short.Body.String(), "/online.js") {
		t.Fatalf("/m expected online.js plugin in body, got: %s", short.Body.String())
	}
	if strings.Contains(short.Body.String(), "tmdbproxy") {
		t.Fatalf("/m leaked non-external plugins (tmdbproxy): %s", short.Body.String())
	}
	// /m must produce the same output as bare /on.js (same external-plugin path).
	if canonical := get("/on.js"); short.Body.String() != canonical.Body.String() {
		t.Fatalf("/m body differs from /on.js:\n/m=%s\n/on.js=%s", short.Body.String(), canonical.Body.String())
	}
}

// TestServerPluginShortPathDisabledByDefault verifies the alias is opt-in:
// without [web] plugin_short_path, /m is not registered (falls through to 404)
// while /on.js still serves normally.
func TestServerPluginShortPathDisabledByDefault(t *testing.T) {
	resetHTTPAPIGlobals(t)
	pluginsDir := t.TempDir()
	repoRoot := t.TempDir()

	if err := os.WriteFile(filepath.Join(pluginsDir, "on.js"), []byte(`window._plugins=[{plugins}];`), 0o644); err != nil {
		t.Fatalf("write on.js: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginsDir, "online.js"), []byte(`x`), 0o644); err != nil {
		t.Fatalf("write online.js: %v", err)
	}

	t.Setenv("LAMPAC_GO_PLUGINS_DIR", pluginsDir)
	t.Setenv("LAMPAC_GO_REPO_ROOT", repoRoot)

	srv, err := NewServer(Options{})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://lampac.local/m", nil))
	if ct := rec.Header().Get("Content-Type"); strings.Contains(ct, "application/javascript") {
		t.Fatalf("/m must not be served as a plugin when plugin_short_path is unset (content-type=%s status=%d)", ct, rec.Code)
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("/m expected 404 when alias disabled, got %d body=%s", rec.Code, rec.Body.String())
	}

	canonical := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(canonical, httptest.NewRequest(http.MethodGet, "http://lampac.local/on.js", nil))
	if canonical.Code != http.StatusOK {
		t.Fatalf("/on.js should still work with alias disabled, got %d", canonical.Code)
	}
}
