package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"lampac-go/internal/auth"
	"lampac-go/internal/config"
	"lampac-go/internal/tgauth"
)

// recoveryStore creates a store with one approved token, no devices.
func recoveryStore(t *testing.T) (*tgauth.Store, string) {
	t.Helper()
	dir := t.TempDir()
	s := tgauth.NewStore(dir)
	t.Cleanup(s.Close)
	tok := "rec-token-123"
	if err := s.Add(tgauth.ApprovedToken{
		Token: tok, TelegramID: 42,
		ExpiresAt: time.Now().UTC().Add(30 * 24 * time.Hour),
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	return s, tok
}

// buildGate wires the auth gate with minimal stubs so we can hit it in tests.
// The downstream handler always 200s so we can inspect Set-Cookie headers.
func buildGate(t *testing.T, store *tgauth.Store) http.Handler {
	t.Helper()
	cfg := config.Config{}
	pending := tgauth.NewPendingStore()
	dp := tgauth.NewDevicePendingStore()
	gate := tgAuthGateMiddleware(store, dp, pending, nil, nil, nil, nil, cfg)
	return gate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
}

// countCookieIn parses `Set-Cookie` headers on a recorder and returns how
// many cookies of the given name carry the expected value. Multiple
// Set-Cookie headers per response are allowed.
func countCookieIn(rec *httptest.ResponseRecorder, name, want string) int {
	hits := 0
	for _, c := range rec.Result().Cookies() {
		if c.Name == name && c.Value == want {
			hits++
		}
	}
	return hits
}

// TestGate_RecoverWhenOnlyHttpOnlyCookiePresent: the gate authenticates the
// request via _lampac_auth (HttpOnly), but the JS-visible lampac_token is
// missing. The recovery must re-issue BOTH so the client UI sees itself
// logged in on the next render.
func TestGate_RecoverWhenOnlyHttpOnlyCookiePresent(t *testing.T) {
	store, tok := recoveryStore(t)
	gate := buildGate(t, store)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/lite/events?id=550", nil)
	req.AddCookie(&http.Cookie{Name: "_lampac_auth", Value: tok})
	// NO lampac_token — the asymmetric drop scenario.
	gate.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if countCookieIn(rec, "lampac_token", tok) == 0 {
		t.Fatalf("recovery did NOT issue lampac_token; Set-Cookie headers: %v", rec.Result().Header.Values("Set-Cookie"))
	}
	if countCookieIn(rec, "_lampac_auth", tok) == 0 {
		t.Fatalf("recovery did NOT issue _lampac_auth; Set-Cookie headers: %v", rec.Result().Header.Values("Set-Cookie"))
	}
}

// TestGate_RecoverWhenOnlyJSCookiePresent: mirror case — JS-visible cookie
// alive, HttpOnly cookie dropped. Both must be re-issued.
func TestGate_RecoverWhenOnlyJSCookiePresent(t *testing.T) {
	store, tok := recoveryStore(t)
	gate := buildGate(t, store)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/lite/events?id=550", nil)
	req.AddCookie(&http.Cookie{Name: "lampac_token", Value: tok})
	gate.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if countCookieIn(rec, "lampac_token", tok) == 0 {
		t.Fatalf("recovery did NOT issue lampac_token; got: %v", rec.Result().Header.Values("Set-Cookie"))
	}
	if countCookieIn(rec, "_lampac_auth", tok) == 0 {
		t.Fatalf("recovery did NOT issue _lampac_auth; got: %v", rec.Result().Header.Values("Set-Cookie"))
	}
}

// TestGate_NoRecoveryWhenBothCookiesPresent: when the client already has
// both cookies set to the right value, we must NOT spam Set-Cookie on every
// request — that would re-issue 365-day expiry on every request, fine in
// itself but adds two extra response headers per call. We just want to check
// for asymmetric loss.
func TestGate_NoRecoveryWhenBothCookiesPresent(t *testing.T) {
	store, tok := recoveryStore(t)
	gate := buildGate(t, store)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/lite/events?id=550", nil)
	req.AddCookie(&http.Cookie{Name: "lampac_token", Value: tok})
	req.AddCookie(&http.Cookie{Name: "_lampac_auth", Value: tok})
	gate.ServeHTTP(rec, req)

	for _, sc := range rec.Result().Header.Values("Set-Cookie") {
		if strings.Contains(sc, "lampac_token=") || strings.Contains(sc, "_lampac_auth=") {
			t.Fatalf("unexpected Set-Cookie when both cookies already present: %s", sc)
		}
	}
}

// TestGate_RecoveryAlsoWorksFromQueryToken: token in URL → gate sets BOTH
// cookies via the existing query-token path AND symmetric recovery is a
// no-op since both are now present. End state: both cookies issued exactly
// once (no duplicate).
func TestGate_RecoveryAlsoWorksFromQueryToken(t *testing.T) {
	store, tok := recoveryStore(t)
	gate := buildGate(t, store)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/lite/events?id=550&token="+tok, nil)
	gate.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if countCookieIn(rec, "lampac_token", tok) != 1 {
		t.Fatalf("expected exactly one lampac_token Set-Cookie, got %d", countCookieIn(rec, "lampac_token", tok))
	}
	if countCookieIn(rec, "_lampac_auth", tok) != 1 {
		t.Fatalf("expected exactly one _lampac_auth Set-Cookie, got %d", countCookieIn(rec, "_lampac_auth", tok))
	}
}

// TestGate_StaleCookieValueDoesNotPreventRecovery: cookie carries a token
// that's no longer in the store (e.g. admin force-deleted it). validToken
// must stay empty and no spurious recovery should fire.
func TestGate_StaleCookieValueDoesNotPreventRecovery(t *testing.T) {
	store, _ := recoveryStore(t)
	gate := buildGate(t, store)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/lite/events?id=550", nil)
	req.AddCookie(&http.Cookie{Name: "lampac_token", Value: "tok-that-was-deleted"})
	gate.ServeHTTP(rec, req)

	// The handler should NOT have re-issued the deleted token as a cookie.
	for _, sc := range rec.Result().Header.Values("Set-Cookie") {
		if strings.Contains(sc, "tok-that-was-deleted") {
			t.Fatalf("recovery wrongly re-issued a deleted/unknown token: %s", sc)
		}
	}
}

// TestGate_HonorsNonTGContextUser: a password / anon / profile user resolved
// by authMiddleware into the request context (no TG cookie at all) must pass
// the TG gate instead of being bounced into the TG pending-code flow. This is
// the mode-aware fix (2026-05-28) — before it, mode=password with TG also
// enabled blocked every password user behind a TG "Требуется авторизация".
func TestGate_HonorsNonTGContextUser(t *testing.T) {
	store, _ := recoveryStore(t)
	gate := buildGate(t, store)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/lite/events?id=550", nil)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.User{ID: "pw:abc123"}))
	gate.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "accsdb") {
		t.Fatalf("non-TG context user was blocked by the TG gate: %s", rec.Body.String())
	}
}

// TestGate_BlocksWhenNoUserAndNoToken: negative control — without any context
// user and without a TG token the gate must still gate source discovery
// (accsdb auth-required response), so the mode-aware allow above didn't open
// a hole for unauthenticated clients.
func TestGate_BlocksWhenNoUserAndNoToken(t *testing.T) {
	store, _ := recoveryStore(t)
	gate := buildGate(t, store)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/lite/events?id=550", nil)
	gate.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), "accsdb") {
		t.Fatalf("expected accsdb auth-required response, got: %s (code=%d)", rec.Body.String(), rec.Code)
	}
}

// TestAccsdbAuthRequired_CarriesCodeBotQR pins the enriched auth-wall payload
// online.js renders as the QR card: code, bot handle, deep link and a
// server-generated PNG QR data-URI, plus the results:[] forEach guard.
func TestAccsdbAuthRequired_CarriesCodeBotQR(t *testing.T) {
	resp := accsdbAuthRequired("ABC123", "showybot")
	if resp["accsdb"] != true {
		t.Fatalf("accsdb flag missing: %v", resp["accsdb"])
	}
	if resp["code"] != "ABC123" {
		t.Fatalf("code = %v, want ABC123", resp["code"])
	}
	if resp["bot"] != "@showybot" {
		t.Fatalf("bot = %v, want @showybot", resp["bot"])
	}
	if dl, _ := resp["deep_link"].(string); dl != "https://telegram.me/showybot?start=ABC123" {
		t.Fatalf("deep_link = %v", resp["deep_link"])
	}
	if qr, _ := resp["qr"].(string); !strings.HasPrefix(qr, "data:image/png;base64,") {
		t.Fatalf("qr is not a png data-uri: %.40q", resp["qr"])
	}
	if _, ok := resp["results"]; !ok {
		t.Fatal("results missing — clients call response.results.forEach()")
	}
}

// TestAccsdbAuthRequired_NoBotNoQR: without a configured bot there's no deep
// link to encode, so qr/deep_link are omitted but the code still ships.
func TestAccsdbAuthRequired_NoBotNoQR(t *testing.T) {
	resp := accsdbAuthRequired("ABC123", "")
	if _, ok := resp["deep_link"]; ok {
		t.Fatal("deep_link should be absent without a bot name")
	}
	if _, ok := resp["qr"]; ok {
		t.Fatal("qr should be absent without a deep link")
	}
	if resp["code"] != "ABC123" {
		t.Fatalf("code = %v, want ABC123", resp["code"])
	}
}

// TestGate_AuthCardCarriesCodeAndQR: an unauthenticated /lite/events with a
// device uid must produce the enriched QR auth-card payload (code + qr +
// deep link) so online.js can render the scan-to-login card.
func TestGate_AuthCardCarriesCodeAndQR(t *testing.T) {
	store, _ := recoveryStore(t)
	cfg := config.Config{}
	cfg.TelegramAuth.BotName = "showybot"
	pending := tgauth.NewPendingStore()
	dp := tgauth.NewDevicePendingStore()
	gate := tgAuthGateMiddleware(store, dp, pending, nil, nil, nil, nil, cfg)(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/lite/events?id=550&uid=devicexyz", nil)
	gate.ServeHTTP(rec, req)

	body := rec.Body.String()
	for _, want := range []string{`"accsdb":true`, `"code":`, `"qr":"data:image/png;base64,`, `"deep_link":"https://telegram.me/showybot?start=`} {
		if !strings.Contains(body, want) {
			t.Fatalf("auth-card response missing %q; body=%.200s", want, body)
		}
	}
}
