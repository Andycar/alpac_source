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
)

func TestAdminInitRequiresAuth(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", root)
	if err := os.WriteFile(filepath.Join(root, "passwd"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("write passwd: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/admin/init/current", nil)
	adminInitCurrentHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/admin/auth" {
		t.Fatalf("unexpected redirect location: %s", loc)
	}
}

func TestAdminAuthAndInitSaveFlow(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", root)

	if err := os.WriteFile(filepath.Join(root, "passwd"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("write passwd: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "current.conf"), []byte(`{"current":true}`), 0o644); err != nil {
		t.Fatalf("write current.conf: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "example.conf"), []byte(`{"example":true}`), 0o644); err != nil {
		t.Fatalf("write example.conf: %v", err)
	}

	recAuthPage := httptest.NewRecorder()
	reqAuthPage := httptest.NewRequest(http.MethodGet, "http://lampac.local/admin/auth", nil)
	adminAuthHandler().ServeHTTP(recAuthPage, reqAuthPage)
	if recAuthPage.Code != http.StatusOK || !strings.Contains(recAuthPage.Body.String(), "<form") {
		t.Fatalf("unexpected auth page response: code=%d body=%s", recAuthPage.Code, recAuthPage.Body.String())
	}

	formAuth := url.Values{}
	formAuth.Set("parol", "secret")
	recAuth := httptest.NewRecorder()
	reqAuth := httptest.NewRequest(http.MethodPost, "http://lampac.local/admin/auth", strings.NewReader(formAuth.Encode()))
	reqAuth.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	adminAuthHandler().ServeHTTP(recAuth, reqAuth)
	if recAuth.Code != http.StatusOK {
		t.Fatalf("unexpected auth status: %d", recAuth.Code)
	}
	if !strings.Contains(recAuth.Header().Get("Set-Cookie"), "passwd=secret") {
		t.Fatalf("expected passwd cookie, got: %s", recAuth.Header().Get("Set-Cookie"))
	}

	payload := `{"accsdb":{"enable":true,"users":[{"id":1}]},"listen":{"port":9118}}`
	formSave := url.Values{}
	formSave.Set("json", payload)
	recSave := httptest.NewRecorder()
	reqSave := httptest.NewRequest(http.MethodPost, "http://lampac.local/admin/init/save", strings.NewReader(formSave.Encode()))
	reqSave.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqSave.AddCookie(&http.Cookie{Name: "passwd", Value: "secret"})
	adminInitSaveHandler().ServeHTTP(recSave, reqSave)

	if recSave.Code != http.StatusOK {
		t.Fatalf("unexpected save status: %d", recSave.Code)
	}
	if !strings.Contains(recSave.Body.String(), `"success":true`) {
		t.Fatalf("unexpected save body: %s", recSave.Body.String())
	}

	initData, err := os.ReadFile(filepath.Join(root, "init.conf"))
	if err != nil {
		t.Fatalf("read init.conf: %v", err)
	}
	var initObj map[string]any
	if err := stdjson.Unmarshal(initData, &initObj); err != nil {
		t.Fatalf("invalid init.conf json: %v", err)
	}
	if accsdb, ok := initObj["accsdb"].(map[string]any); ok {
		if _, exists := accsdb["users"]; exists {
			t.Fatalf("users must be stripped from init.conf")
		}
	} else {
		t.Fatalf("accsdb is missing in init.conf: %+v", initObj)
	}

	usersData, err := os.ReadFile(filepath.Join(root, "users.json"))
	if err != nil {
		t.Fatalf("read users.json: %v", err)
	}
	if !strings.Contains(string(usersData), `"id": 1`) {
		t.Fatalf("unexpected users.json: %s", string(usersData))
	}

	recCurrent := httptest.NewRecorder()
	reqCurrent := httptest.NewRequest(http.MethodGet, "http://lampac.local/admin/init/current", nil)
	reqCurrent.AddCookie(&http.Cookie{Name: "passwd", Value: "secret"})
	adminInitCurrentHandler().ServeHTTP(recCurrent, reqCurrent)
	if recCurrent.Code != http.StatusOK || !strings.Contains(recCurrent.Body.String(), `"current":true`) {
		t.Fatalf("unexpected current response: code=%d body=%s", recCurrent.Code, recCurrent.Body.String())
	}

	recCustom := httptest.NewRecorder()
	reqCustom := httptest.NewRequest(http.MethodGet, "http://lampac.local/admin/init/custom", nil)
	reqCustom.AddCookie(&http.Cookie{Name: "passwd", Value: "secret"})
	adminInitCustomHandler().ServeHTTP(recCustom, reqCustom)
	if recCustom.Code != http.StatusOK || !strings.Contains(recCustom.Body.String(), `"listen":{"port":9118}`) {
		t.Fatalf("unexpected custom response: code=%d body=%s", recCustom.Code, recCustom.Body.String())
	}

	recDefault := httptest.NewRecorder()
	reqDefault := httptest.NewRequest(http.MethodGet, "http://lampac.local/admin/init/default", nil)
	reqDefault.AddCookie(&http.Cookie{Name: "passwd", Value: "secret"})
	adminInitDefaultHandler().ServeHTTP(recDefault, reqDefault)
	if recDefault.Code != http.StatusOK || !strings.Contains(recDefault.Body.String(), `"example":true`) {
		t.Fatalf("unexpected default response: code=%d body=%s", recDefault.Code, recDefault.Body.String())
	}

	recExample := httptest.NewRecorder()
	reqExample := httptest.NewRequest(http.MethodGet, "http://lampac.local/admin/init/example", nil)
	reqExample.AddCookie(&http.Cookie{Name: "passwd", Value: "secret"})
	adminInitExampleHandler().ServeHTTP(recExample, reqExample)
	if recExample.Code != http.StatusOK || strings.TrimSpace(recExample.Body.String()) != `{"example":true}` {
		t.Fatalf("unexpected example response: code=%d body=%s", recExample.Code, recExample.Body.String())
	}

	recSyncPage := httptest.NewRecorder()
	reqSyncPage := httptest.NewRequest(http.MethodGet, "http://lampac.local/admin/sync/init", nil)
	reqSyncPage.AddCookie(&http.Cookie{Name: "passwd", Value: "secret"})
	adminSyncInitHandler().ServeHTTP(recSyncPage, reqSyncPage)
	if recSyncPage.Code != http.StatusOK || !strings.Contains(recSyncPage.Body.String(), "Редактор sync.conf") {
		t.Fatalf("unexpected sync page response: code=%d body=%s", recSyncPage.Code, recSyncPage.Body.String())
	}

	formSyncSave := url.Values{}
	formSyncSave.Set("json", `{"sync":{"enable":true}}`)
	recSyncSave := httptest.NewRecorder()
	reqSyncSave := httptest.NewRequest(http.MethodPost, "http://lampac.local/admin/sync/init/save", strings.NewReader(formSyncSave.Encode()))
	reqSyncSave.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqSyncSave.AddCookie(&http.Cookie{Name: "passwd", Value: "secret"})
	adminSyncSaveHandler().ServeHTTP(recSyncSave, reqSyncSave)
	if recSyncSave.Code != http.StatusOK || !strings.Contains(recSyncSave.Body.String(), `"success":true`) {
		t.Fatalf("unexpected sync save response: code=%d body=%s", recSyncSave.Code, recSyncSave.Body.String())
	}

	syncData, err := os.ReadFile(filepath.Join(root, "sync.conf"))
	if err != nil {
		t.Fatalf("read sync.conf: %v", err)
	}
	if strings.TrimSpace(string(syncData)) != `{"sync":{"enable":true}}` {
		t.Fatalf("unexpected sync.conf: %s", string(syncData))
	}
}
