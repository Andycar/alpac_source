package httpapi

import (
	stdjson "encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
	"lampac-go/internal/tgauth"
)

func newTestPwAPIStore(t *testing.T) *tgauth.PasswordUserStore {
	t.Helper()
	store := tgauth.NewPasswordUserStore(t.TempDir())
	t.Cleanup(store.Close)
	_, err := store.Create("alice", "secret-password", tgauth.CreateOpts{})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return store
}

func pwCfg(mode string, enable bool) *config.Config {
	return &config.Config{
		Auth: config.AuthConfig{
			Mode: mode,
			Password: config.PasswordAuthConfig{
				Enable:           enable,
				MinPasswordLen:   8,
				MaxLoginAttempts: 5,
				LockoutMinutes:   10,
				SessionDays:      30,
			},
		},
	}
}

func TestPasswordLoginSuccessSetsCookies(t *testing.T) {
	store := newTestPwAPIStore(t)
	cfg := pwCfg(config.AuthModePassword, true)
	handler := passwordLoginHandler(store, func() *config.Config { return cfg })

	body := strings.NewReader(`{"username":"alice","password":"secret-password"}`)
	req := httptest.NewRequest(http.MethodPost, "/auth/password/login", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	var hasAlpac, hasLampac bool
	for _, c := range cookies {
		if c.Name == "alpac_token" && c.Value != "" {
			hasAlpac = true
		}
		if c.Name == "lampac_token" && c.Value != "" {
			hasLampac = true
		}
	}
	if !hasAlpac || !hasLampac {
		t.Fatalf("expected both alpac_token and lampac_token cookies; got %d cookies", len(cookies))
	}

	var resp map[string]any
	_ = stdjson.NewDecoder(rec.Body).Decode(&resp)
	if resp["ok"] != true || resp["username"] != "alice" {
		t.Fatalf("unexpected response body: %v", resp)
	}
}

func TestPasswordLoginRejectsBadPassword(t *testing.T) {
	store := newTestPwAPIStore(t)
	cfg := pwCfg(config.AuthModePassword, true)
	handler := passwordLoginHandler(store, func() *config.Config { return cfg })

	body := strings.NewReader(`{"username":"alice","password":"wrong"}`)
	req := httptest.NewRequest(http.MethodPost, "/auth/password/login", body)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestPasswordLoginRejectsWhenDisabled(t *testing.T) {
	store := newTestPwAPIStore(t)
	cfg := pwCfg(config.AuthModeTG, false) // mode=tg AND enable=false
	handler := passwordLoginHandler(store, func() *config.Config { return cfg })

	body := strings.NewReader(`{"username":"alice","password":"secret-password"}`)
	req := httptest.NewRequest(http.MethodPost, "/auth/password/login", body)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when disabled, got %d", rec.Code)
	}
}

func TestPasswordLoginAllowedWhenEnabledFlagSetEvenInTGMode(t *testing.T) {
	// Admin sets up users (enable=true) before flipping mode away from tg.
	store := newTestPwAPIStore(t)
	cfg := pwCfg(config.AuthModeTG, true)
	handler := passwordLoginHandler(store, func() *config.Config { return cfg })

	body := strings.NewReader(`{"username":"alice","password":"secret-password"}`)
	req := httptest.NewRequest(http.MethodPost, "/auth/password/login", body)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 when Password.Enable=true even in tg mode, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPasswordLoginRateLimits(t *testing.T) {
	// Reset the shared limiter so prior tests don't leak state.
	globalPasswordLoginLimiter.entries = make(map[string]*passwordLoginEntry)

	store := newTestPwAPIStore(t)
	cfg := pwCfg(config.AuthModePassword, true)
	cfg.Auth.Password.MaxLoginAttempts = 3
	handler := passwordLoginHandler(store, func() *config.Config { return cfg })

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/auth/password/login", strings.NewReader(`{"username":"alice","password":"wrong"}`))
		req.RemoteAddr = "1.2.3.4:1234"
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}
	// 4th attempt should be locked.
	req := httptest.NewRequest(http.MethodPost, "/auth/password/login", strings.NewReader(`{"username":"alice","password":"secret-password"}`))
	req.RemoteAddr = "1.2.3.4:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after exceeding attempts, got %d", rec.Code)
	}
}

func TestPasswordLoginRejectsBannedUser(t *testing.T) {
	store := newTestPwAPIStore(t)
	if err := store.SetBan("alice", true, "test"); err != nil {
		t.Fatalf("SetBan: %v", err)
	}
	cfg := pwCfg(config.AuthModePassword, true)
	handler := passwordLoginHandler(store, func() *config.Config { return cfg })

	body := strings.NewReader(`{"username":"alice","password":"secret-password"}`)
	req := httptest.NewRequest(http.MethodPost, "/auth/password/login", body)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for banned user, got %d", rec.Code)
	}
}

func TestPasswordLogoutClearsCookies(t *testing.T) {
	store := newTestPwAPIStore(t)
	_, token, _ := store.Login("alice", "secret-password", "1.2.3.4", "ua", 30)

	handler := passwordLogoutHandler(store)
	req := httptest.NewRequest(http.MethodPost, "/auth/password/logout", nil)
	req.AddCookie(&http.Cookie{Name: "lampac_token", Value: token})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	// Session should be invalidated.
	if _, _, ok := store.LookupSession(token); ok {
		t.Fatal("logout should invalidate session")
	}
	// Cookies should be cleared (MaxAge<0 or expired).
	var cleared int
	for _, c := range rec.Result().Cookies() {
		if (c.Name == "alpac_token" || c.Name == "lampac_token") && (c.MaxAge < 0 || c.Value == "") {
			cleared++
		}
	}
	if cleared < 2 {
		t.Fatalf("expected both cookies cleared, only %d found", cleared)
	}
}

func TestPasswordMeReturnsCurrentUser(t *testing.T) {
	store := newTestPwAPIStore(t)
	_, token, _ := store.Login("alice", "secret-password", "1.2.3.4", "ua", 30)

	handler := passwordMeHandler(store)
	req := httptest.NewRequest(http.MethodGet, "/auth/password/me", nil)
	req.AddCookie(&http.Cookie{Name: "lampac_token", Value: token})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body, _ := io.ReadAll(rec.Body)
	var resp map[string]any
	_ = stdjson.Unmarshal(body, &resp)
	if resp["username"] != "alice" {
		t.Fatalf("expected username=alice, got %v", resp)
	}
	if resp["uid"] == nil || resp["uid"] == "" {
		t.Fatalf("expected uid present, got %v", resp["uid"])
	}
}

func TestPasswordMeReturns401WithoutCookie(t *testing.T) {
	store := newTestPwAPIStore(t)
	handler := passwordMeHandler(store)
	req := httptest.NewRequest(http.MethodGet, "/auth/password/me", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestAuthModeHandlerReportsMode(t *testing.T) {
	cfg := pwCfg(config.AuthModePassword, true)
	cfg.TelegramAuth.Enable = false
	handler := authModeHandler(func() *config.Config { return cfg })

	req := httptest.NewRequest(http.MethodGet, "/auth/mode", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	var resp map[string]any
	_ = stdjson.NewDecoder(rec.Body).Decode(&resp)
	if resp["mode"] != "password" {
		t.Fatalf("mode=%v want password", resp["mode"])
	}
	if resp["password_enabled"] != true {
		t.Fatalf("password_enabled=%v want true", resp["password_enabled"])
	}
	if resp["tg_enabled"] != false {
		t.Fatalf("tg_enabled=%v want false", resp["tg_enabled"])
	}
}
