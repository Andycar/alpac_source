package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lampac-go/internal/auth"
	"lampac-go/internal/config"
	"lampac-go/internal/modules"
)

func TestLampainitLiteTemplate(t *testing.T) {
	resetHTTPAPIGlobals(t)
	root := t.TempDir()
	plugins := filepath.Join(root, "plugins")
	if err := os.MkdirAll(plugins, 0o755); err != nil {
		t.Fatalf("mkdir plugins: %v", err)
	}

	liteTpl := `init=[{initiale}]; country="{country}"; host="{localhost}"; invc="{lampainit-invc}";`
	if err := os.WriteFile(filepath.Join(plugins, "liteinit.js"), []byte(liteTpl), 0o644); err != nil {
		t.Fatalf("write liteinit: %v", err)
	}
	if err := os.WriteFile(filepath.Join(plugins, "lampainit-invc.js"), []byte("invc-code"), 0o644); err != nil {
		t.Fatalf("write lampainit-invc: %v", err)
	}

	cfg := config.Config{
		Compat: config.CompatConfig{RepoRoot: root},
		Web: config.WebConfig{
			InitPlugins: config.InitPluginsConfig{
				Online: true,
				SISI:   true,
				Sync:   true,
			},
		},
	}
	manifest := []modules.RootModule{
		{Dll: "Online.dll", Enable: true},
		{Dll: "SISI.dll", Enable: true},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lampainit.js?lite=true", nil)
	lampainitJSHandler(cfg, manifest, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"{localhost}/lite.js"`) && !strings.Contains(body, `"http://lampac.local/lite.js"`) {
		t.Fatalf("lite plugins list missing online: %s", body)
	}
	if !strings.Contains(body, "invc-code") {
		t.Fatalf("invc snippet missing: %s", body)
	}
	if !strings.Contains(body, "http://lampac.local") {
		t.Fatalf("host replacement missing: %s", body)
	}
}

func TestLampainitFullTemplateReplacements(t *testing.T) {
	resetHTTPAPIGlobals(t)
	root := t.TempDir()
	plugins := filepath.Join(root, "plugins")
	if err := os.MkdirAll(plugins, 0o755); err != nil {
		t.Fatalf("mkdir plugins: %v", err)
	}

	fullTpl := `{initiale}|{lampainit-invc}|{country}|{localhost}|{deny}|{pirate_store}|jac="{jachost}"|key="{jackett_key}"|pdh="{parser_defaults_hash}"|{full_btn_priority_hash}|{btn_priority_forced}|{token}|{ major: 0, minor: 0 }`
	if err := os.WriteFile(filepath.Join(plugins, "lampainit.js"), []byte(fullTpl), 0o644); err != nil {
		t.Fatalf("write lampainit: %v", err)
	}
	_ = os.WriteFile(filepath.Join(plugins, "lampainit-invc.js"), []byte("invc"), 0o644)
	_ = os.WriteFile(filepath.Join(plugins, "pirate_store.js"), []byte("pirate"), 0o644)
	_ = os.WriteFile(filepath.Join(plugins, "online.js"), []byte("version: '3.14'"), 0o644)

	home := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", home)
	initConf := `{
		"pirate_store": true,
		"online": {"name":"Lampac","version":false,"btn_priority_forced":false},
		"accsdb": {"domainId_pattern":"^(.*)$"}
	}`
	if err := os.WriteFile(filepath.Join(home, "init.conf"), []byte(initConf), 0o644); err != nil {
		t.Fatalf("write init.conf: %v", err)
	}

	cfg := config.Config{
		Compat: config.CompatConfig{RepoRoot: root},
		Parser: config.ParserConfig{JacRedKey: "pp"},
		Web: config.WebConfig{
			InitPlugins: config.InitPluginsConfig{
				Online: true,
			},
		},
	}
	manifest := []modules.RootModule{
		{Dll: "Online.dll", Enable: true},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lampainit.js", nil)
	lampainitJSHandler(cfg, manifest, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "{initiale}") || strings.Contains(body, "{localhost}") {
		t.Fatalf("template placeholders left unresolved: %s", body)
	}
	// Parser defaults must point the client at THIS server (the built-in
	// jacred proxy answers /api/v2.0/indexers/*), and the defaults hash must
	// be rendered (non-numeric "p" prefix so Lampa.Storage.get keeps it a string).
	if !strings.Contains(body, `jac="lampac.local"`) {
		t.Fatalf("jachost must be the serving host: %s", body)
	}
	// The configured [parser] jacred_apikey must reach the client (was hardcoded "1").
	if !strings.Contains(body, `key="pp"`) {
		t.Fatalf("jackett_key must be the configured apikey: %s", body)
	}
	if !strings.Contains(body, `pdh="`+parserDefaultsHash("lampac.local", "pp")+`"`) {
		t.Fatalf("parser_defaults_hash not rendered: %s", body)
	}
	if strings.Contains(body, "{parser_defaults_hash}") {
		t.Fatalf("parser_defaults_hash placeholder left unresolved: %s", body)
	}
	if !strings.Contains(body, "false") {
		t.Fatalf("btn_priority_forced replacement expected: %s", body)
	}
	// publicBrandMajor / publicBrandMinor are package-level vars set in
	// branding.go (currently 153/0 for Chrome 153). The test pins on the
	// *replacement* shape — i.e. no spaces around the colons — rather
	// than on the version numbers themselves, so a Chrome bump doesn't
	// require touching this assertion.
	if !strings.Contains(body, "{major: ") || !strings.Contains(body, ", minor: ") {
		t.Fatalf("version object replacement missing: %s", body)
	}
	if strings.Contains(body, "{ major: 0, minor: 0 }") {
		t.Fatalf("raw template still present: %s", body)
	}
}

func TestLampainitFallbackWhenTemplateMissing(t *testing.T) {
	resetHTTPAPIGlobals(t)
	cfg := config.Config{
		Compat: config.CompatConfig{RepoRoot: t.TempDir()},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lampainit.js", nil)
	lampainitJSHandler(cfg, nil, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when no template, got %d", rec.Code)
	}
}

func TestPrivateInitJSHandler(t *testing.T) {
	resetHTTPAPIGlobals(t)
	root := t.TempDir()
	plugins := filepath.Join(root, "plugins")
	if err := os.MkdirAll(plugins, 0o755); err != nil {
		t.Fatalf("mkdir plugins: %v", err)
	}
	if err := os.WriteFile(filepath.Join(plugins, "privateinit.js"), []byte(`P {country} {localhost} {jachost}`), 0o644); err != nil {
		t.Fatalf("write privateinit: %v", err)
	}

	cfg := config.Config{Compat: config.CompatConfig{RepoRoot: root}}
	manifest := []modules.RootModule{{Dll: "JacRed.dll", Enable: true}}

	// unauthenticated: empty response
	recEmpty := httptest.NewRecorder()
	reqEmpty := httptest.NewRequest(http.MethodGet, "http://lampac.local/privateinit.js", nil)
	privateInitJSHandler(cfg, manifest).ServeHTTP(recEmpty, reqEmpty)
	if strings.TrimSpace(recEmpty.Body.String()) != "" {
		t.Fatalf("expected empty privateinit for anonymous, got: %s", recEmpty.Body.String())
	}

	// authenticated: rendered template
	recAuth := httptest.NewRecorder()
	reqAuth := httptest.NewRequest(http.MethodGet, "http://lampac.local/privateinit.js", nil)
	reqAuth = reqAuth.WithContext(auth.WithUser(reqAuth.Context(), &auth.User{
		ID:      "u1",
		Ban:     false,
		Expires: time.Now().Add(24 * time.Hour),
	}))
	privateInitJSHandler(cfg, manifest).ServeHTTP(recAuth, reqAuth)
	body := recAuth.Body.String()
	if !strings.Contains(body, "http://lampac.local") {
		t.Fatalf("host replacement missing: %s", body)
	}
	if !strings.Contains(body, "lampac.local") {
		t.Fatalf("jachost replacement missing: %s", body)
	}
}
