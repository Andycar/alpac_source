package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"lampac-go/internal/auth"
	"lampac-go/internal/profile"
)

// setupProfileStore wires a clean store + auth middleware for the duration
// of one test. Returns a helper that issues a request through the auth
// middleware (so the session cookie resolves into the request context).
func setupProfileStore(t *testing.T) (*profile.Store, func(req *http.Request, h http.HandlerFunc) *httptest.ResponseRecorder) {
	t.Helper()
	root := t.TempDir()
	store := profile.NewStore(root)
	prev := profileStoreRef
	SetProfileStore(store)
	// Reset rate-limit buckets so tests don't accumulate across each other
	// (they all hit the same httptest RemoteAddr).
	profileRateMu.Lock()
	profileRegisterRate = map[string]*profileRateBucket{}
	profileLoginRate = map[string]*profileRateBucket{}
	profileRateMu.Unlock()
	t.Cleanup(func() { SetProfileStore(prev) })

	mw := auth.New(auth.WithProfileStore(ProfileSessionChecker(store)))
	through := func(req *http.Request, h http.HandlerFunc) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		// Auth middleware populates context based on cookies.
		mw.Handler(h).ServeHTTP(rec, req)
		return rec
	}
	return store, through
}

func decodeProfileJSON(t *testing.T, body string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := stdjson.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("invalid json: %v body=%q", err, body)
	}
	return out
}

func TestProfileRegisterLoginFlow(t *testing.T) {
	_, through := setupProfileStore(t)

	// Register
	regBody := `{"username":"jamie","password":"secret123"}`
	reqReg := httptest.NewRequest(http.MethodPost, "/api/profile/register", strings.NewReader(regBody))
	reqReg.Header.Set("Content-Type", "application/json")
	recReg := through(reqReg, profileRegisterHandler())
	if recReg.Code != http.StatusOK {
		t.Fatalf("register status %d body=%s", recReg.Code, recReg.Body.String())
	}
	regResp := decodeProfileJSON(t, recReg.Body.String())
	if ok, _ := regResp["success"].(bool); !ok {
		t.Fatalf("register success=false: %+v", regResp)
	}
	cookieHeader := recReg.Result().Cookies()
	var sessCookie *http.Cookie
	for _, c := range cookieHeader {
		if c.Name == profileSessionCookie {
			sessCookie = c
		}
	}
	if sessCookie == nil || sessCookie.Value == "" {
		t.Fatalf("register did not issue session cookie")
	}

	// /me with cookie returns authenticated=true, method=profile
	reqMe := httptest.NewRequest(http.MethodGet, "/api/profile/me", nil)
	reqMe.AddCookie(sessCookie)
	recMe := through(reqMe, profileMeHandler())
	meResp := decodeProfileJSON(t, recMe.Body.String())
	if ok, _ := meResp["authenticated"].(bool); !ok {
		t.Fatalf("/me should be authenticated: %+v", meResp)
	}
	if meResp["auth_method"] != "profile" {
		t.Fatalf("auth_method want profile, got %v", meResp["auth_method"])
	}
	if meResp["username"] != "jamie" {
		t.Fatalf("username want jamie, got %v", meResp["username"])
	}

	// /me without cookie returns authenticated=false
	reqMeNoAuth := httptest.NewRequest(http.MethodGet, "/api/profile/me", nil)
	recMeNoAuth := through(reqMeNoAuth, profileMeHandler())
	meNoAuthResp := decodeProfileJSON(t, recMeNoAuth.Body.String())
	if auth, _ := meNoAuthResp["authenticated"].(bool); auth {
		t.Fatalf("/me without cookie should be unauthenticated: %+v", meNoAuthResp)
	}

	// Login with wrong password
	loginBody := `{"username":"jamie","password":"wrong"}`
	reqBadLogin := httptest.NewRequest(http.MethodPost, "/api/profile/login", strings.NewReader(loginBody))
	reqBadLogin.Header.Set("Content-Type", "application/json")
	recBadLogin := through(reqBadLogin, profileLoginHandler())
	if recBadLogin.Code != http.StatusUnauthorized {
		t.Fatalf("bad login status: %d", recBadLogin.Code)
	}
	badResp := decodeProfileJSON(t, recBadLogin.Body.String())
	if badResp["error"] != "invalid_credentials" {
		t.Fatalf("bad login error: %+v", badResp)
	}

	// Login correctly
	loginOK := `{"username":"jamie","password":"secret123"}`
	reqLogin := httptest.NewRequest(http.MethodPost, "/api/profile/login", strings.NewReader(loginOK))
	reqLogin.Header.Set("Content-Type", "application/json")
	recLogin := through(reqLogin, profileLoginHandler())
	if recLogin.Code != http.StatusOK {
		t.Fatalf("good login status %d body=%s", recLogin.Code, recLogin.Body.String())
	}

	// Logout
	reqLogout := httptest.NewRequest(http.MethodPost, "/api/profile/logout", nil)
	reqLogout.AddCookie(sessCookie)
	recLogout := through(reqLogout, profileLogoutHandler())
	if recLogout.Code != http.StatusOK {
		t.Fatalf("logout status: %d", recLogout.Code)
	}
	// Cookie should be cleared (Max-Age < 0)
	hasClear := false
	for _, c := range recLogout.Result().Cookies() {
		if c.Name == profileSessionCookie && c.MaxAge < 0 {
			hasClear = true
		}
	}
	if !hasClear {
		t.Fatalf("logout did not emit clearing cookie")
	}

	// Old session is now dead
	reqMe2 := httptest.NewRequest(http.MethodGet, "/api/profile/me", nil)
	reqMe2.AddCookie(sessCookie)
	recMe2 := through(reqMe2, profileMeHandler())
	me2Resp := decodeProfileJSON(t, recMe2.Body.String())
	if auth, _ := me2Resp["authenticated"].(bool); auth {
		t.Fatalf("post-logout session should be invalid: %+v", me2Resp)
	}
}

func TestProfileSyncCodeFlow(t *testing.T) {
	_, through := setupProfileStore(t)

	// First device: register + issue sync code
	regBody := `{"username":"kelly","password":"secret123"}`
	reqReg := httptest.NewRequest(http.MethodPost, "/api/profile/register", strings.NewReader(regBody))
	reqReg.Header.Set("Content-Type", "application/json")
	recReg := through(reqReg, profileRegisterHandler())
	var sessCookie *http.Cookie
	for _, c := range recReg.Result().Cookies() {
		if c.Name == profileSessionCookie {
			sessCookie = c
		}
	}
	if sessCookie == nil {
		t.Fatalf("no session cookie issued")
	}

	reqIssue := httptest.NewRequest(http.MethodPost, "/api/profile/sync-code/issue", nil)
	reqIssue.AddCookie(sessCookie)
	recIssue := through(reqIssue, profileIssueSyncCodeHandler())
	if recIssue.Code != http.StatusOK {
		t.Fatalf("issue status %d body=%s", recIssue.Code, recIssue.Body.String())
	}
	issueResp := decodeProfileJSON(t, recIssue.Body.String())
	code, _ := issueResp["code"].(string)
	if code == "" {
		t.Fatalf("no code in issue response: %+v", issueResp)
	}

	// Second device: no cookie, redeems the code, gets a fresh session
	redeemBody, _ := stdjson.Marshal(map[string]string{"code": code})
	reqRedeem := httptest.NewRequest(http.MethodPost, "/api/profile/sync-code/redeem", strings.NewReader(string(redeemBody)))
	reqRedeem.Header.Set("Content-Type", "application/json")
	recRedeem := through(reqRedeem, profileRedeemSyncCodeHandler())
	if recRedeem.Code != http.StatusOK {
		t.Fatalf("redeem status %d body=%s", recRedeem.Code, recRedeem.Body.String())
	}
	redeemResp := decodeProfileJSON(t, recRedeem.Body.String())
	if redeemResp["username"] != "kelly" {
		t.Fatalf("redeem username wrong: %+v", redeemResp)
	}
	var device2Cookie *http.Cookie
	for _, c := range recRedeem.Result().Cookies() {
		if c.Name == profileSessionCookie {
			device2Cookie = c
		}
	}
	if device2Cookie == nil || device2Cookie.Value == "" {
		t.Fatalf("redeem did not issue session for device 2")
	}
	if device2Cookie.Value == sessCookie.Value {
		t.Fatalf("device 2 should get its own session token")
	}

	// Both cookies resolve to the same profile ID
	for label, c := range map[string]*http.Cookie{"device1": sessCookie, "device2": device2Cookie} {
		req := httptest.NewRequest(http.MethodGet, "/api/profile/me", nil)
		req.AddCookie(c)
		rec := through(req, profileMeHandler())
		resp := decodeProfileJSON(t, rec.Body.String())
		if resp["username"] != "kelly" {
			t.Fatalf("%s saw wrong username: %+v", label, resp)
		}
	}

	// Second redeem of the same code fails (one-shot)
	reqRedeem2 := httptest.NewRequest(http.MethodPost, "/api/profile/sync-code/redeem", strings.NewReader(string(redeemBody)))
	reqRedeem2.Header.Set("Content-Type", "application/json")
	recRedeem2 := through(reqRedeem2, profileRedeemSyncCodeHandler())
	if recRedeem2.Code != http.StatusBadRequest {
		t.Fatalf("second redeem should fail, got %d", recRedeem2.Code)
	}
	resp2 := decodeProfileJSON(t, recRedeem2.Body.String())
	if resp2["error"] != "sync_code_used" {
		t.Fatalf("second redeem error: %+v", resp2)
	}
}

func TestProfileRegisterValidation(t *testing.T) {
	_, through := setupProfileStore(t)

	cases := []struct {
		name, body string
		wantCode   int
		wantSlug   string
	}{
		{"short username", `{"username":"ab","password":"secret123"}`, http.StatusBadRequest, "username_invalid"},
		{"weak password", `{"username":"linda","password":"abc"}`, http.StatusBadRequest, "password_weak"},
		{"bad json", `not json`, http.StatusBadRequest, "bad_request"},
		{"empty body", ``, http.StatusBadRequest, "bad_request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/profile/register", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := through(req, profileRegisterHandler())
			if rec.Code != tc.wantCode {
				t.Fatalf("status %d want %d body=%s", rec.Code, tc.wantCode, rec.Body.String())
			}
			resp := decodeProfileJSON(t, rec.Body.String())
			if resp["error"] != tc.wantSlug {
				t.Fatalf("error %v want %q", resp["error"], tc.wantSlug)
			}
		})
	}
}

// fakeTGAuth lets owned-* tests pretend the request was authenticated via
// Telegram, without standing up a real tgauth.Store. Used as auth.Option so
// the middleware populates User{ID: "tg:<id>"} for any request.
type fakeTGAuth struct{ id int64 }

func (f fakeTGAuth) LookupAuth(token string) (int64, time.Time, bool) {
	// We don't actually check the token — every cookie value is accepted as
	// the configured TG ID. Tests just need an authenticated context.
	if token == "" {
		return 0, time.Time{}, false
	}
	return f.id, time.Now().Add(24 * time.Hour), true
}

func setupProfileStoreWithTG(t *testing.T, tgID int64) (*profile.Store, func(req *http.Request, h http.HandlerFunc) *httptest.ResponseRecorder) {
	t.Helper()
	root := t.TempDir()
	store := profile.NewStore(root)
	prev := profileStoreRef
	SetProfileStore(store)
	profileRateMu.Lock()
	profileRegisterRate = map[string]*profileRateBucket{}
	profileLoginRate = map[string]*profileRateBucket{}
	profileRateMu.Unlock()
	t.Cleanup(func() { SetProfileStore(prev) })

	mw := auth.New(
		auth.WithProfileStore(ProfileSessionChecker(store)),
		auth.WithTGStore(fakeTGAuth{id: tgID}),
	)
	through := func(req *http.Request, h http.HandlerFunc) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mw.Handler(h).ServeHTTP(rec, req)
		return rec
	}
	return store, through
}

func tgCookie() *http.Cookie {
	return &http.Cookie{Name: "lampac_token", Value: "any-value"}
}

func TestProfileOwnedCreateAndPINLoginFlow(t *testing.T) {
	store, through := setupProfileStoreWithTG(t, 555)

	// Owner creates a PIN-only profile via the bot/owned endpoint.
	reqCreate := httptest.NewRequest(http.MethodPost, "/api/profile/owned/create",
		strings.NewReader(`{"username":"kids","pin":"1234"}`))
	reqCreate.AddCookie(tgCookie())
	recCreate := through(reqCreate, profileOwnedCreateHandler())
	if recCreate.Code != http.StatusOK {
		t.Fatalf("create status %d body=%s", recCreate.Code, recCreate.Body.String())
	}
	createResp := decodeProfileJSON(t, recCreate.Body.String())
	if ok, _ := createResp["success"].(bool); !ok || createResp["has_pin"] != true {
		t.Fatalf("create resp: %+v", createResp)
	}
	profileID, _ := createResp["profile_id"].(string)
	if profileID == "" {
		t.Fatalf("profile_id missing in create response")
	}

	// List owned shows the new one.
	reqList := httptest.NewRequest(http.MethodGet, "/api/profile/owned", nil)
	reqList.AddCookie(tgCookie())
	recList := through(reqList, profileOwnedListHandler())
	listResp := decodeProfileJSON(t, recList.Body.String())
	profs, _ := listResp["profiles"].([]any)
	if len(profs) != 1 {
		t.Fatalf("owned list len=%d, want 1: %+v", len(profs), listResp)
	}

	// PIN login from an anon device (no TG cookie) succeeds.
	reqLogin := httptest.NewRequest(http.MethodPost, "/api/profile/pin-login",
		strings.NewReader(`{"username":"kids","pin":"1234"}`))
	recLogin := through(reqLogin, profilePinLoginHandler())
	if recLogin.Code != http.StatusOK {
		t.Fatalf("pin-login status %d body=%s", recLogin.Code, recLogin.Body.String())
	}
	loginResp := decodeProfileJSON(t, recLogin.Body.String())
	if loginResp["username"] != "kids" {
		t.Fatalf("login resp: %+v", loginResp)
	}
	var sessCookie *http.Cookie
	for _, c := range recLogin.Result().Cookies() {
		if c.Name == profileSessionCookie {
			sessCookie = c
		}
	}
	if sessCookie == nil {
		t.Fatalf("pin-login did not issue session cookie")
	}

	// Wrong PIN — invalid_credentials.
	reqBad := httptest.NewRequest(http.MethodPost, "/api/profile/pin-login",
		strings.NewReader(`{"username":"kids","pin":"9999"}`))
	recBad := through(reqBad, profilePinLoginHandler())
	if recBad.Code != http.StatusUnauthorized {
		t.Fatalf("wrong pin status %d, want 401", recBad.Code)
	}

	// Foreign TG user can't see or mutate this profile.
	_, through2 := setupProfileStoreWithTG(t, 999)
	SetProfileStore(store) // share state
	reqList2 := httptest.NewRequest(http.MethodGet, "/api/profile/owned", nil)
	reqList2.AddCookie(tgCookie())
	recList2 := through2(reqList2, profileOwnedListHandler())
	listResp2 := decodeProfileJSON(t, recList2.Body.String())
	profs2, _ := listResp2["profiles"].([]any)
	if len(profs2) != 0 {
		t.Fatalf("foreign TG should see 0 profiles, got %d", len(profs2))
	}

	// Foreign TG cannot delete.
	reqDel := httptest.NewRequest(http.MethodPost, "/api/profile/owned/delete",
		strings.NewReader(`{"profile_id":"`+profileID+`"}`))
	reqDel.AddCookie(tgCookie())
	recDel := through2(reqDel, profileOwnedDeleteHandler())
	if recDel.Code != http.StatusForbidden {
		t.Fatalf("foreign delete status %d, want 403", recDel.Code)
	}
}

func TestProfileOwnedRequiresTG(t *testing.T) {
	_, through := setupProfileStore(t) // no TG auth installed

	req := httptest.NewRequest(http.MethodGet, "/api/profile/owned", nil)
	rec := through(req, profileOwnedListHandler())
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("without TG auth got %d, want 401", rec.Code)
	}
	body := decodeProfileJSON(t, rec.Body.String())
	if body["error"] != "tg_required" {
		t.Fatalf("error %v want tg_required", body["error"])
	}
}
