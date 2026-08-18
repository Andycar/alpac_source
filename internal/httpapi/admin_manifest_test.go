package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lampac-go/internal/adminhttp"
	"lampac-go/internal/modules"
)

// wireManifestDeps injects the real host functions the /admin/manifest handler
// (now in internal/adminhttp) reaches for, so this integration test exercises
// the true behavior.
func wireManifestDeps() {
	adminhttp.SetDeps(adminhttp.Deps{
		AuthorizeAdmin:    authorizeAdmin,
		ReadRootPasswd:    readRootPasswd,
		RandomLowerAlnum:  randomLowerAlnum,
		UpdateInitConfMap: updateInitConfMap,
	})
}

func TestAdminManifestInstallFirstSetup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", home)

	if err := os.WriteFile(filepath.Join(home, "passwd"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("write passwd: %v", err)
	}

	wireManifestDeps()
	handler := adminhttp.AdminManifestInstallHandler()

	recGet := httptest.NewRecorder()
	reqGet := httptest.NewRequest(http.MethodGet, "http://lampac.local/admin/manifest/install", nil)
	handler.ServeHTTP(recGet, reqGet)
	if recGet.Code != http.StatusOK {
		t.Fatalf("unexpected GET status: %d", recGet.Code)
	}
	if !strings.Contains(recGet.Body.String(), "Установка модулей") {
		t.Fatalf("unexpected GET body: %s", recGet.Body.String())
	}

	form := url.Values{}
	form.Set("online", "on")
	form.Set("sisi", "on")
	form.Set("jac", "fdb")
	form.Set("catalog", "on")
	form.Set("dlna", "on")
	form.Set("tracks", "on")
	form.Set("ts", "on")
	// eng intentionally disabled to verify init.conf update.

	recPost := httptest.NewRecorder()
	reqPost := httptest.NewRequest(http.MethodPost, "http://lampac.local/admin/manifest/install", strings.NewReader(form.Encode()))
	reqPost.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqPost.Header.Set("CF-Connecting-IP", "203.0.113.10")
	handler.ServeHTTP(recPost, reqPost)

	if recPost.Code != http.StatusOK {
		t.Fatalf("unexpected POST status: %d", recPost.Code)
	}
	if !strings.Contains(recPost.Body.String(), "Настройка завершена") {
		t.Fatalf("unexpected POST body: %s", recPost.Body.String())
	}

	manifestPath := filepath.Join(home, "module", "manifest.json")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest []modules.RootModule
	if err := stdjson.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatalf("manifest is not valid json: %v", err)
	}
	if len(manifest) != 7 {
		t.Fatalf("unexpected module count: %d (%s)", len(manifest), string(manifestData))
	}

	hasDLL := func(name string) bool {
		for _, mod := range manifest {
			if strings.EqualFold(mod.Dll, name) {
				return true
			}
		}
		return false
	}
	for _, dll := range []string{"Online.dll", "SISI.dll", "JacRed.dll", "Catalog.dll", "DLNA.dll", "Tracks.dll", "TorrServer.dll"} {
		if !hasDLL(dll) {
			t.Fatalf("expected module %s in manifest: %s", dll, string(manifestData))
		}
	}

	initData, err := os.ReadFile(filepath.Join(home, "init.conf"))
	if err != nil {
		t.Fatalf("read init.conf: %v", err)
	}
	var initObj map[string]any
	if err := stdjson.Unmarshal(initData, &initObj); err != nil {
		t.Fatalf("invalid init.conf json: %v", err)
	}

	if disableEng, _ := initObj["disableEng"].(bool); !disableEng {
		t.Fatalf("disableEng must be true in init.conf: %s", string(initData))
	}

	listen, _ := initObj["listen"].(map[string]any)
	if listen == nil || toString(listen["frontend"]) != "cloudflare" {
		t.Fatalf("listen.frontend must be cloudflare: %s", string(initData))
	}

	accsdb, _ := initObj["accsdb"].(map[string]any)
	if accsdb == nil {
		t.Fatalf("accsdb missing in init.conf: %s", string(initData))
	}
	if enabled, _ := accsdb["enable"].(bool); !enabled {
		t.Fatalf("accsdb.enable must be true: %s", string(initData))
	}
	shared := strings.TrimSpace(toString(accsdb["shared_passwd"]))
	if len(shared) != 8 {
		t.Fatalf("shared_passwd must have length 8, got %q", shared)
	}

	jacredData, err := os.ReadFile(filepath.Join(home, "module", "JacRed.conf"))
	if err != nil {
		t.Fatalf("read JacRed.conf: %v", err)
	}
	var jacred map[string]any
	if err := stdjson.Unmarshal(jacredData, &jacred); err != nil {
		t.Fatalf("invalid JacRed.conf json: %v", err)
	}
	if toString(jacred["typesearch"]) != "red" {
		t.Fatalf("JacRed.conf typesearch must be red: %s", string(jacredData))
	}
}

func TestAdminManifestInstallEditModeRequiresAuth(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", home)

	if err := os.MkdirAll(filepath.Join(home, "module"), 0o755); err != nil {
		t.Fatalf("mkdir module: %v", err)
	}
	initialManifest := []byte(`[{"enable":true,"dll":"Online.dll"}]`)
	if err := os.WriteFile(filepath.Join(home, "module", "manifest.json"), initialManifest, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "passwd"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("write passwd: %v", err)
	}

	wireManifestDeps()
	handler := adminhttp.AdminManifestInstallHandler()

	recNoAuth := httptest.NewRecorder()
	reqNoAuth := httptest.NewRequest(http.MethodGet, "http://lampac.local/admin/manifest/install", nil)
	handler.ServeHTTP(recNoAuth, reqNoAuth)
	if recNoAuth.Code != http.StatusFound {
		t.Fatalf("unexpected no-auth status: %d", recNoAuth.Code)
	}
	if loc := recNoAuth.Header().Get("Location"); loc != "/admin/auth" {
		t.Fatalf("unexpected no-auth redirect: %s", loc)
	}

	recGet := httptest.NewRecorder()
	reqGet := httptest.NewRequest(http.MethodGet, "http://lampac.local/admin/manifest/install", nil)
	reqGet.AddCookie(&http.Cookie{Name: "passwd", Value: "secret"})
	handler.ServeHTTP(recGet, reqGet)
	if recGet.Code != http.StatusOK {
		t.Fatalf("unexpected auth GET status: %d", recGet.Code)
	}

	form := url.Values{}
	form.Set("online", "on")
	form.Set("eng", "on")
	form.Set("jac", "webapi")
	recPost := httptest.NewRecorder()
	reqPost := httptest.NewRequest(http.MethodPost, "http://lampac.local/admin/manifest/install", strings.NewReader(form.Encode()))
	reqPost.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqPost.AddCookie(&http.Cookie{Name: "passwd", Value: "secret"})
	handler.ServeHTTP(recPost, reqPost)
	if recPost.Code != http.StatusOK {
		t.Fatalf("unexpected auth POST status: %d", recPost.Code)
	}
	if !strings.Contains(recPost.Body.String(), "Перезагрузите lampac для изменения настроек") {
		t.Fatalf("unexpected auth POST body: %s", recPost.Body.String())
	}

	manifestData, err := os.ReadFile(filepath.Join(home, "module", "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if !strings.Contains(string(manifestData), "Online.dll") || !strings.Contains(string(manifestData), "JacRed.dll") {
		t.Fatalf("manifest not updated: %s", string(manifestData))
	}
}
