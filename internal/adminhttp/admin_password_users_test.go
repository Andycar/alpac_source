package adminhttp

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"lampac-go/internal/tgauth"
)

// TestMain wires a permissive AdminAuthCheck so the moved password-users handlers
// (which call the injected tgAdminAuthCheck gate) let requests through — mirroring
// the host gate's localhost bypass these tests relied on. They exercise the CRUD
// logic, not the auth gate.
func TestMain(m *testing.M) {
	SetDeps(Deps{
		AdminAuthCheck: func(w http.ResponseWriter, r *http.Request, store *tgauth.Store, adminStore *tgauth.AdminIDStore) (int64, bool, bool) {
			return 1, true, true
		},
	})
	m.Run()
}

// adminPasswordUsersTestHandler skips the auth gate so we can drive the
// handler directly. Pattern copied from existing admin handler tests
// where tgAdminAuthCheck has the localhost fallback.
func adminPasswordUsersTestHandler(store *tgauth.PasswordUserStore) http.HandlerFunc {
	return adminPasswordUsersHandler(store, nil, tgauth.NewAdminIDStore(t_tempRoot(), 1))
}

var _tempRootForTests string

func t_tempRoot() string {
	if _tempRootForTests == "" {
		_tempRootForTests = "/tmp/lampac-go-admin-test"
	}
	return _tempRootForTests
}

func newAdminPwTestStore(t *testing.T) *tgauth.PasswordUserStore {
	t.Helper()
	store := tgauth.NewPasswordUserStore(t.TempDir())
	t.Cleanup(store.Close)
	return store
}

func adminPwPost(t *testing.T, handler http.HandlerFunc, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/admin/api/password-users", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1234" // localhost fallback in auth check
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestAdminPasswordUsersCreate(t *testing.T) {
	store := newAdminPwTestStore(t)
	handler := adminPasswordUsersTestHandler(store)

	rec := adminPwPost(t, handler, `{"action":"create","username":"alice","password":"secret-pw-1","comment":"Test"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, ok := store.LookupUser("alice"); !ok {
		t.Fatal("user should be created")
	}

	// Duplicate create fails.
	rec = adminPwPost(t, handler, `{"action":"create","username":"alice","password":"another-pw"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("duplicate create want 400, got %d", rec.Code)
	}
}

func TestAdminPasswordUsersList(t *testing.T) {
	store := newAdminPwTestStore(t)
	_, _ = store.Create("alice", "secret-pw-1", tgauth.CreateOpts{Comment: "first"})
	_, _ = store.Create("bob", "secret-pw-2", tgauth.CreateOpts{Comment: "second"})

	handler := adminPasswordUsersTestHandler(store)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/password-users", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d", rec.Code)
	}
	var resp struct {
		Users []passwordUserJSON `json:"users"`
	}
	_ = stdjson.NewDecoder(rec.Body).Decode(&resp)
	if len(resp.Users) != 2 {
		t.Fatalf("expected 2 users, got %d", len(resp.Users))
	}
	for _, u := range resp.Users {
		if u.UID == "" {
			t.Fatalf("user %s missing UID", u.Username)
		}
	}
}

func TestAdminPasswordUsersResetAndDelete(t *testing.T) {
	store := newAdminPwTestStore(t)
	_, _ = store.Create("carol", "secret-pw-1", tgauth.CreateOpts{})
	handler := adminPasswordUsersTestHandler(store)

	// Reset password.
	rec := adminPwPost(t, handler, `{"action":"reset_password","username":"carol","password":"new-pw-here"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, ok := store.CheckPassword("carol", "new-pw-here"); !ok {
		t.Fatal("password should match new one after reset")
	}

	// Delete.
	rec = adminPwPost(t, handler, `{"action":"delete","username":"carol"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status=%d", rec.Code)
	}
	if _, ok := store.LookupUser("carol"); ok {
		t.Fatal("user should be deleted")
	}
}

func TestAdminPasswordUsersBanUnban(t *testing.T) {
	store := newAdminPwTestStore(t)
	_, _ = store.Create("dave", "secret-pw-1", tgauth.CreateOpts{})
	handler := adminPasswordUsersTestHandler(store)

	rec := adminPwPost(t, handler, `{"action":"ban","username":"dave","ban_reason":"spam"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("ban status=%d body=%s", rec.Code, rec.Body.String())
	}
	u, _ := store.LookupUser("dave")
	if !u.Ban || u.BanReason != "spam" {
		t.Fatalf("ban not applied: %+v", u)
	}

	rec = adminPwPost(t, handler, `{"action":"unban","username":"dave"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("unban status=%d", rec.Code)
	}
	u, _ = store.LookupUser("dave")
	if u.Ban {
		t.Fatal("ban should be cleared")
	}
}

func TestAdminPasswordUsersExtend(t *testing.T) {
	store := newAdminPwTestStore(t)
	_, _ = store.Create("erin", "secret-pw-1", tgauth.CreateOpts{})
	handler := adminPasswordUsersTestHandler(store)

	rec := adminPwPost(t, handler, `{"action":"extend","username":"erin","days":90}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("extend status=%d body=%s", rec.Code, rec.Body.String())
	}
	u, _ := store.LookupUser("erin")
	if u.ExpiresAt.IsZero() {
		t.Fatal("ExpiresAt should be set after extend")
	}
	if time.Until(u.ExpiresAt) < 89*24*time.Hour {
		t.Fatalf("extend should land ~90d out, got %v", u.ExpiresAt)
	}
}

func TestAdminPasswordUsersUnknownAction(t *testing.T) {
	store := newAdminPwTestStore(t)
	handler := adminPasswordUsersTestHandler(store)
	rec := adminPwPost(t, handler, `{"action":"frobnicate","username":"x"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown action want 400, got %d", rec.Code)
	}
}

func TestAdminAuthModeReadWrite(t *testing.T) {
	dir := t.TempDir()
	store := tgauth.NewAuthModeStore(dir, "tg")
	handler := adminAuthModeHandler(store, nil, nil, tgauth.NewAdminIDStore(dir, 1))

	// GET initial.
	req := httptest.NewRequest(http.MethodGet, "/admin/api/auth-mode", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status=%d", rec.Code)
	}
	var get struct{ Mode string }
	_ = stdjson.NewDecoder(rec.Body).Decode(&get)
	if get.Mode != "tg" {
		t.Fatalf("initial mode=%q want tg", get.Mode)
	}

	// POST to password.
	req = httptest.NewRequest(http.MethodPost, "/admin/api/auth-mode", strings.NewReader(`{"mode":"password"}`))
	req.RemoteAddr = "127.0.0.1:1234"
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("set status=%d body=%s", rec.Code, rec.Body.String())
	}
	if store.Get() != "password" {
		t.Fatalf("mode not updated, got %q", store.Get())
	}

	// Invalid rejected.
	req = httptest.NewRequest(http.MethodPost, "/admin/api/auth-mode", strings.NewReader(`{"mode":"bogus"}`))
	req.RemoteAddr = "127.0.0.1:1234"
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid mode want 400, got %d", rec.Code)
	}
}
