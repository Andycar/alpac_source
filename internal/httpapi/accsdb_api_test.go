package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTestAccsdbSharedPasswdHandshake(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "init.conf"), []byte(`{"accsdb":{"shared_passwd":"secret","shared_daytime":2}}`), 0o644); err != nil {
		t.Fatalf("write init.conf: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/testaccsdb?uid=secret", nil)
	testAccsdbHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"accsdb":true`) {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestTestAccsdbAddAndDuplicateUser(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "init.conf"), []byte(`{"accsdb":{"shared_passwd":"secret","shared_daytime":1}}`), 0o644); err != nil {
		t.Fatalf("write init.conf: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "users.json"), []byte(`[]`), 0o644); err != nil {
		t.Fatalf("write users.json: %v", err)
	}

	recAdd := httptest.NewRecorder()
	reqAdd := httptest.NewRequest(http.MethodGet, "http://lampac.local/testaccsdb?account_email=secret&uid=u100", nil)
	testAccsdbHandler().ServeHTTP(recAdd, reqAdd)
	if !strings.Contains(recAdd.Body.String(), `"success":true`) || !strings.Contains(recAdd.Body.String(), `"uid":"u100"`) {
		t.Fatalf("unexpected add body: %s", recAdd.Body.String())
	}

	usersData, err := os.ReadFile(filepath.Join(home, "users.json"))
	if err != nil {
		t.Fatalf("read users.json: %v", err)
	}
	if !strings.Contains(string(usersData), `"id": "u100"`) {
		t.Fatalf("user was not persisted: %s", string(usersData))
	}

	recDup := httptest.NewRecorder()
	reqDup := httptest.NewRequest(http.MethodGet, "http://lampac.local/testaccsdb?account_email=secret&uid=u100", nil)
	testAccsdbHandler().ServeHTTP(recDup, reqDup)
	if strings.Contains(recDup.Body.String(), `"uid":"u100"`) {
		t.Fatalf("duplicate add should not return uid payload: %s", recDup.Body.String())
	}
	if !strings.Contains(recDup.Body.String(), `"accsdb":false`) {
		t.Fatalf("unexpected duplicate body: %s", recDup.Body.String())
	}
}
