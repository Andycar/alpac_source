package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakePasswordChecker satisfies PasswordTokenChecker without dragging
// in the real tgauth store dependency.
type fakePasswordChecker struct {
	expectToken string
	username    string
	uid         string
	expires     time.Time
}

func (f *fakePasswordChecker) LookupAuth(token string) (string, string, time.Time, bool) {
	if token != f.expectToken {
		return "", "", time.Time{}, false
	}
	return f.username, f.uid, f.expires, true
}

type fakeAnonChecker struct {
	expectToken string
	uid         string
	expires     time.Time
	lastIP      string
}

func (f *fakeAnonChecker) LookupAuth(token, ip string) (string, time.Time, bool) {
	if token != f.expectToken {
		return "", time.Time{}, false
	}
	f.lastIP = ip
	return f.uid, f.expires, true
}

func TestMiddlewareResolvesPasswordToken(t *testing.T) {
	exp := time.Now().UTC().Add(24 * time.Hour)
	fake := &fakePasswordChecker{
		expectToken: "pw-session-123",
		username:    "alice",
		uid:         "deadbeef",
		expires:     exp,
	}

	var got *User
	handler := New(WithPasswordStore(fake)).Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, ok := UserFromContext(r.Context()); ok {
			got = u
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "lampac_token", Value: "pw-session-123"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got == nil {
		t.Fatal("password token should resolve to a user")
	}
	if got.ID != "pw:deadbeef" {
		t.Fatalf("user ID = %q, want pw:deadbeef", got.ID)
	}
	if len(got.Aliases) != 1 || got.Aliases[0] != "alice" {
		t.Fatalf("Aliases = %v, want [alice]", got.Aliases)
	}
	if !got.Expires.Equal(exp) {
		t.Fatalf("Expires = %v, want %v", got.Expires, exp)
	}
}

func TestMiddlewareResolvesAnonCookie(t *testing.T) {
	exp := time.Now().UTC().Add(7 * 24 * time.Hour)
	fake := &fakeAnonChecker{
		expectToken: "anon-token-1",
		uid:         "cafebabe",
		expires:     exp,
	}
	mode := func() string { return "none" }

	var got *User
	handler := New(WithAnonAuth(fake, nil, nil, mode)).Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, ok := UserFromContext(r.Context()); ok {
			got = u
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "lampac_token", Value: "anon-token-1"})
	req.Header.Set("X-Forwarded-For", "9.9.9.9")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got == nil {
		t.Fatal("anon cookie should resolve to a user")
	}
	if got.ID != "anon:cafebabe" {
		t.Fatalf("user ID = %q, want anon:cafebabe", got.ID)
	}
	if !got.Anonymous {
		t.Fatal("user.Anonymous should be true for anon resolution")
	}
	if fake.lastIP != "9.9.9.9" {
		t.Fatalf("anon checker should be called with client IP, got %q", fake.lastIP)
	}
}

func TestMiddlewareIssuesAnonOnHTMLNavigation(t *testing.T) {
	// Simulate the full anon issuance flow — no cookie present, mode=none.
	var issued bool
	var cookieSet string
	issuer := func(ip, ua string) (string, string, time.Time) {
		issued = true
		return "fresh-anon-token", "newuid01", time.Now().UTC().Add(7 * 24 * time.Hour)
	}
	setCookie := func(w http.ResponseWriter, r *http.Request, token string, expiresAt time.Time) {
		cookieSet = token
		http.SetCookie(w, &http.Cookie{Name: "lampac_token", Value: token, Path: "/"})
	}
	mode := func() string { return "none" }

	var got *User
	handler := New(WithAnonAuth(nil, issuer, setCookie, mode)).Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, ok := UserFromContext(r.Context()); ok {
			got = u
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !issued {
		t.Fatal("anon issuer should be called on HTML navigation")
	}
	if cookieSet != "fresh-anon-token" {
		t.Fatalf("cookie writer should receive fresh token, got %q", cookieSet)
	}
	if got == nil || got.ID != "anon:newuid01" || !got.Anonymous {
		t.Fatalf("issued user not threaded into context: %+v", got)
	}
}

func TestMiddlewareSkipsAnonIssueForXHR(t *testing.T) {
	issued := false
	issuer := func(ip, ua string) (string, string, time.Time) {
		issued = true
		return "tok", "uid", time.Now()
	}
	mode := func() string { return "none" }
	handler := New(WithAnonAuth(nil, issuer, func(w http.ResponseWriter, r *http.Request, token string, expiresAt time.Time) {}, mode)).
		Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))

	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.Header.Set("Accept", "application/json")
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if issued {
		t.Fatal("anon issuer should not fire for non-HTML Accept")
	}

	// POST should not issue either.
	req = httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Accept", "text/html")
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if issued {
		t.Fatal("anon issuer should not fire for POST navigation")
	}
}

func TestMiddlewareSkipsAnonIssueWhenModeNotNone(t *testing.T) {
	issued := false
	issuer := func(ip, ua string) (string, string, time.Time) {
		issued = true
		return "tok", "uid", time.Now()
	}
	mode := func() string { return "tg" }
	handler := New(WithAnonAuth(nil, issuer, func(w http.ResponseWriter, r *http.Request, token string, expiresAt time.Time) {}, mode)).
		Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept", "text/html")
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if issued {
		t.Fatal("anon issuer should not fire when mode != none")
	}
}

func TestMiddlewareTGTakesPriorityOverPassword(t *testing.T) {
	// If both TG and password stores recognize the same token (artificial
	// edge case — collision is statistically impossible with 64-char hex
	// tokens — but we still pin the priority), TG wins.
	tg := &stubTG{token: "shared", tgID: 42, expires: time.Now().UTC().Add(time.Hour)}
	pw := &fakePasswordChecker{
		expectToken: "shared",
		username:    "alice",
		uid:         "deadbeef",
		expires:     time.Now().UTC().Add(time.Hour),
	}

	var got *User
	handler := New(WithTGStore(tg), WithPasswordStore(pw)).Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, ok := UserFromContext(r.Context()); ok {
			got = u
		}
		w.WriteHeader(200)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "lampac_token", Value: "shared"})
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if got == nil || !strings.HasPrefix(got.ID, "tg:") {
		t.Fatalf("TG should win over password, got %+v", got)
	}
}

type stubTG struct {
	token   string
	tgID    int64
	expires time.Time
}

func (s *stubTG) LookupAuth(token string) (int64, time.Time, bool) {
	if token != s.token {
		return 0, time.Time{}, false
	}
	return s.tgID, s.expires, true
}
