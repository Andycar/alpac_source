package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"lampac-go/internal/tgauth"
)

// helper: build a Store + Pending pair seeded with one approved token + device
func seedStore(t *testing.T) (*tgauth.Store, *tgauth.PendingStore) {
	t.Helper()
	dir := t.TempDir()
	store := tgauth.NewStore(dir)
	t.Cleanup(store.Close)

	exp := time.Now().UTC().Add(30 * 24 * time.Hour)
	if err := store.Add(tgauth.ApprovedToken{Token: "tok-A", TelegramID: 1, ExpiresAt: exp}); err != nil {
		t.Fatalf("seed Add: %v", err)
	}
	now := time.Now().UTC()
	if _, err := store.AddDevice("tok-A", tgauth.DeviceInfo{
		UID: "uid-A", Fingerprint: "fp-A", Label: "Android", BoundAt: now, LastSeen: now,
	}); err != nil {
		t.Fatalf("seed AddDevice: %v", err)
	}

	if err := store.Add(tgauth.ApprovedToken{Token: "tok-B", TelegramID: 2, ExpiresAt: exp}); err != nil {
		t.Fatalf("seed Add B: %v", err)
	}

	return store, tgauth.NewPendingStore()
}

func parseBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := stdjson.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("unmarshal body: %v\nbody: %s", err, w.Body.String())
	}
	return m
}

// TestStatus_UIDFpMismatch_ReturnsConflict verifies that an unauthenticated
// request with a known UID but wrong fingerprint is NOT auto-authorized and
// instead receives uid_conflict so the client regenerates its UID.
func TestStatus_UIDFpMismatch_ReturnsConflict(t *testing.T) {
	store, pending := seedStore(t)
	h := tgAuthStatusHandler(store, pending, "lampacbot", nil)

	r := httptest.NewRequest(http.MethodGet, "/tg/auth/status?uid=uid-A&fp=fp-different", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	body := parseBody(t, w)
	if body["authorized"] != false {
		t.Errorf("authorized = %v, want false", body["authorized"])
	}
	if body["uid_conflict"] != true {
		t.Errorf("uid_conflict = %v, want true (body=%v)", body["uid_conflict"], body)
	}
	if _, ok := body["code"]; !ok {
		t.Errorf("response missing code: %v", body)
	}
}

// TestStatus_UIDFpMatch_AutoAuth verifies legitimate UID+fp match still
// auto-authenticates (no regression in the cache-clear recovery path).
func TestStatus_UIDFpMatch_AutoAuth(t *testing.T) {
	store, pending := seedStore(t)
	h := tgAuthStatusHandler(store, pending, "lampacbot", nil)

	r := httptest.NewRequest(http.MethodGet, "/tg/auth/status?uid=uid-A&fp=fp-A", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	body := parseBody(t, w)
	if body["authorized"] != true {
		t.Errorf("authorized = %v, want true", body["authorized"])
	}
	if body["token"] != "tok-A" {
		t.Errorf("token = %v, want tok-A", body["token"])
	}
}

// TestStatus_CookieValid_CrossTokenUID_SignalsConflict verifies that a user
// with valid cookies for token B who is sending a UID belonging to token A
// (cub-backup-clones-UID scenario) gets uid_conflict so the client regen.
func TestStatus_CookieValid_CrossTokenUID_SignalsConflict(t *testing.T) {
	store, pending := seedStore(t)
	h := tgAuthStatusHandler(store, pending, "lampacbot", nil)

	r := httptest.NewRequest(http.MethodGet, "/tg/auth/status?uid=uid-A&fp=fp-other", nil)
	r.AddCookie(&http.Cookie{Name: "lampac_token", Value: "tok-B"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	body := parseBody(t, w)
	if body["authorized"] != true {
		t.Errorf("authorized = %v, want true (cookie still valid)", body["authorized"])
	}
	if body["uid_conflict"] != true {
		t.Errorf("uid_conflict = %v, want true (UID belongs to tok-A but cookie is tok-B)", body["uid_conflict"])
	}
}

// TestBindDevice_CrossTokenUID_RefusedWithConflict verifies the bind-device
// endpoint refuses to bind a UID that's on another token and signals the
// client to regenerate.
func TestBindDevice_CrossTokenUID_RefusedWithConflict(t *testing.T) {
	store, _ := seedStore(t)
	h := tgAuthBindDeviceHandler(store)

	// Try to bind uid-A (already on tok-A) to tok-B with different fp.
	r := httptest.NewRequest(http.MethodGet,
		"/tg/auth/bind-device?token=tok-B&uid=uid-A&fp=fp-tv", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	body := parseBody(t, w)
	if body["uid_conflict"] != true {
		t.Errorf("uid_conflict = %v, want true (body=%v)", body["uid_conflict"], body)
	}
	if body["ok"] != false {
		t.Errorf("ok = %v, want false", body["ok"])
	}

	// tok-B should remain device-less.
	if c := store.DeviceCount("tok-B"); c != 0 {
		t.Errorf("tok-B device count after conflicting bind = %d, want 0", c)
	}
}

// TestBindDevice_NewUID_OK verifies binding a fresh UID to a token works
// (no regression for the regenerate-and-rebind path on the client).
func TestBindDevice_NewUID_OK(t *testing.T) {
	store, _ := seedStore(t)
	h := tgAuthBindDeviceHandler(store)

	r := httptest.NewRequest(http.MethodGet,
		"/tg/auth/bind-device?token=tok-B&uid=fresh-uid&fp=fp-tv", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	body := parseBody(t, w)
	if body["ok"] != true {
		t.Errorf("ok = %v, want true (body=%v)", body["ok"], body)
	}
	if body["uid_conflict"] != nil {
		t.Errorf("uid_conflict = %v, want nil for fresh UID", body["uid_conflict"])
	}
	if c := store.DeviceCount("tok-B"); c != 1 {
		t.Errorf("tok-B device count = %d, want 1", c)
	}
}

// TestStatus_StableFPRecovery covers a FULL client wipe (no cookie, no
// localStorage → no UID and a drifted precise fp) where only the coarse stable
// fingerprint survives: the server must recover the token via sfp and re-bind
// the freshly-generated UID for cheaper subsequent recovery.
func TestStatus_StableFPRecovery(t *testing.T) {
	store, pending := seedStore(t)
	// Give tok-A's device a stable fp.
	store.UpdateDeviceStableFP("tok-A", "uid-A", "n:sfp-tv")
	h := tgAuthStatusHandler(store, pending, "lampacbot", nil)

	// Fresh boot after wipe: brand-new UID, unknown precise fp, but the same
	// stable fp the device bound earlier.
	r := httptest.NewRequest(http.MethodGet, "/tg/auth/status?uid=brand-new&fp=fp-drifted&sfp=n:sfp-tv", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	body := parseBody(t, w)
	if body["authorized"] != true {
		t.Fatalf("authorized = %v, want true (sfp recovery)", body["authorized"])
	}
	if body["token"] != "tok-A" {
		t.Fatalf("token = %v, want tok-A", body["token"])
	}
	// The new UID must now resolve to tok-A so the next boot uses the cheap path.
	if tok := store.FindTokenByDeviceUID("brand-new"); tok != "tok-A" {
		t.Errorf("new uid not re-bound: got %q, want tok-A", tok)
	}
}

// TestStatus_StableFPAmbiguous_Refused ensures a stable-fp collision across two
// accounts does NOT log the user into the wrong one.
func TestStatus_StableFPAmbiguous_Refused(t *testing.T) {
	store, pending := seedStore(t)
	store.UpdateDeviceStableFP("tok-A", "uid-A", "sfp-shared")
	// Bind the SAME stable fp to a device on tok-B.
	if _, err := store.AddDevice("tok-B", tgauth.DeviceInfo{UID: "uid-B", StableFP: "sfp-shared", BoundAt: time.Now().UTC(), LastSeen: time.Now().UTC()}); err != nil {
		t.Fatalf("AddDevice tok-B: %v", err)
	}
	h := tgAuthStatusHandler(store, pending, "lampacbot", nil)

	r := httptest.NewRequest(http.MethodGet, "/tg/auth/status?uid=fresh&sfp=sfp-shared", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	body := parseBody(t, w)
	if body["authorized"] != false {
		t.Fatalf("authorized = %v, want false (ambiguous sfp must refuse)", body["authorized"])
	}
}

// TestBindDevice_WithStableFP verifies the bind endpoint persists the sfp so it
// can be used for later recovery.
func TestBindDevice_WithStableFP(t *testing.T) {
	store, _ := seedStore(t)
	h := tgAuthBindDeviceHandler(store)

	r := httptest.NewRequest(http.MethodGet,
		"/tg/auth/bind-device?token=tok-B&uid=tv-uid&fp=fp-tv&sfp=n:sfp-tv", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if body := parseBody(t, w); body["ok"] != true {
		t.Fatalf("ok = %v, want true", body["ok"])
	}
	if tok, _, ok := store.FindTokenByStableFP("n:sfp-tv"); !ok || tok != "tok-B" {
		t.Errorf("sfp not bound: tok=%q ok=%v, want tok-B", tok, ok)
	}
}
