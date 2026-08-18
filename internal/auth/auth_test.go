package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestMiddlewareResolvesAccountUser(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", home)
	initConf := `{"accsdb":{"enable":true,"accounts":{"kitty":"2040-10-17T00:00:00"}}}`
	if err := os.WriteFile(filepath.Join(home, "init.conf"), []byte(initConf), 0o644); err != nil {
		t.Fatalf("write init.conf: %v", err)
	}

	var gotID string
	handler := New().Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, ok := UserFromContext(r.Context()); ok {
			gotID = user.ID
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/privateinit.js?account_email=kitty", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if gotID != "kitty" {
		t.Fatalf("expected user kitty, got %q", gotID)
	}
}

func TestMiddlewareResolvesUsersJSONAlias(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", home)
	_ = os.WriteFile(filepath.Join(home, "init.conf"), []byte(`{"accsdb":{"enable":true}}`), 0o644)
	_ = os.WriteFile(filepath.Join(home, "users.json"), []byte(`[
	  {"id":"main","ids":["alt1","alt2"],"expires":"2040-10-17T00:00:00","ban":false}
	]`), 0o644)

	var gotID string
	handler := New().Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, ok := UserFromContext(r.Context()); ok {
			gotID = user.ID
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/privateinit.js?uid=alt2", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if gotID != "main" {
		t.Fatalf("expected main from alias, got %q", gotID)
	}
}
