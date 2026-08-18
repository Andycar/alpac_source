package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Old Android/TV WebViews (Chromium <67) reject SameSite=None cookies outright, so the TG login
// "succeeded" but never persisted past a refresh. The auth cookies must default to SameSite=Lax.
func TestAuthCookieSameSiteDefaultsToLax(t *testing.T) {
	prev := authCookieSameSiteNone
	authCookieSameSiteNone = false
	defer func() { authCookieSameSiteNone = prev }()

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/tg/auth/complete", nil)
	r.Header.Set("X-Forwarded-Proto", "https") // behind nginx TLS termination

	setAuthCookies(w, r, "tok123")

	cookies := w.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("setAuthCookies emitted no cookies")
	}
	for _, c := range cookies {
		if c.SameSite != http.SameSiteLaxMode {
			t.Errorf("cookie %q: SameSite=%v, want Lax (None is rejected by old WebViews)", c.Name, c.SameSite)
		}
		if !c.Secure {
			t.Errorf("cookie %q: Secure=false over https, want true", c.Name)
		}
	}
}

// Cross-origin Lampa-fork deploys opt back into SameSite=None via cfg.Auth.CookieSameSite="none".
func TestAuthCookieSameSiteNoneWhenConfigured(t *testing.T) {
	prev := authCookieSameSiteNone
	authCookieSameSiteNone = true
	defer func() { authCookieSameSiteNone = prev }()

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("X-Forwarded-Proto", "https")

	setAuthCookies(w, r, "tok123")

	var found bool
	for _, c := range w.Result().Cookies() {
		if c.Name == "lampac_token" {
			found = true
			if c.SameSite != http.SameSiteNoneMode {
				t.Errorf("SameSite=%v, want None when cookie_samesite=none", c.SameSite)
			}
		}
	}
	if !found {
		t.Fatal("lampac_token cookie not set")
	}
}
