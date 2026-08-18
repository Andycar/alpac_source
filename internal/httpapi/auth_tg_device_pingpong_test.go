package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"lampac-go/internal/tgauth"
)

// Two Android phones share the UA-derived label "Android" but have different
// hardware fingerprints. The second phone's request must NOT steal the first
// phone's device slot via label migration — that ping-pong re-migrated the
// slot on EVERY request from the "other" phone (bot notification per source
// switch, profile stuck at «Устройства 1»).
func TestGate_TwoPhonesSameLabelDontPingPong(t *testing.T) {
	store, tok := recoveryStore(t)
	gate := buildGate(t, store)

	now := time.Now().UTC()
	if added, err := store.AddDevice(tok, tgauth.DeviceInfo{
		UID: "uid-phone1", Fingerprint: "fp-phone1", Label: "Android",
		BoundAt: now, LastSeen: now,
	}); err != nil || !added {
		t.Fatalf("seed phone1: added=%v err=%v", added, err)
	}

	// Phone 2: same platform (UA → label "Android"), different uid + fingerprint.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/lite/events?id=550&uid=uid-phone2&fp=fp-phone2", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 13; Pixel 7) Chrome/120 Mobile")
	req.AddCookie(&http.Cookie{Name: "lampac_token", Value: tok})
	req.AddCookie(&http.Cookie{Name: "_lampac_auth", Value: tok})
	gate.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("phone2 request: expected 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}

	// Phone 2 must occupy its OWN slot; phone 1 keeps its binding.
	if got := store.DeviceCount(tok); got != 2 {
		t.Fatalf("device count = %d, want 2 (label migration stole the slot?)", got)
	}
	if got := store.FindTokenByDeviceUID("uid-phone1"); got != tok {
		t.Errorf("phone1 lost its slot (FindTokenByDeviceUID = %q)", got)
	}
	if got := store.FindTokenByDeviceUID("uid-phone2"); got != tok {
		t.Errorf("phone2 not bound (FindTokenByDeviceUID = %q)", got)
	}

	// Phone 1 comes back (e.g. switching a source) — must be a plain known-device
	// pass-through: same count, no slot churn.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/lite/events?id=550&uid=uid-phone1&fp=fp-phone1", nil)
	req2.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 12; Galaxy S21) Chrome/118 Mobile")
	req2.AddCookie(&http.Cookie{Name: "lampac_token", Value: tok})
	req2.AddCookie(&http.Cookie{Name: "_lampac_auth", Value: tok})
	gate.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("phone1 follow-up: expected 200, got %d", rec2.Code)
	}
	if got := store.DeviceCount(tok); got != 2 {
		t.Errorf("device count after phone1 follow-up = %d, want 2", got)
	}
}

// Legacy clients that never send a fingerprint still get the label-based slot
// reuse after a localStorage wipe (otherwise every wipe would burn a device
// slot until the limit locks the user out).
func TestGate_FingerprintlessLabelMigrationStillWorks(t *testing.T) {
	store, tok := recoveryStore(t)
	gate := buildGate(t, store)

	now := time.Now().UTC()
	if added, err := store.AddDevice(tok, tgauth.DeviceInfo{
		UID: "uid-old", Label: "LG TV", BoundAt: now, LastSeen: now,
	}); err != nil || !added {
		t.Fatalf("seed: added=%v err=%v", added, err)
	}

	// Same TV after a wipe: new uid, still no fingerprint, same webOS UA.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/lite/events?id=550&uid=uid-new", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Web0S; Linux/SmartTV) Chrome/87 Safari/537.36 WebAppManager")
	req.AddCookie(&http.Cookie{Name: "lampac_token", Value: tok})
	req.AddCookie(&http.Cookie{Name: "_lampac_auth", Value: tok})
	gate.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}

	if got := store.DeviceCount(tok); got != 1 {
		t.Fatalf("device count = %d, want 1 (fp-less wipe recovery must reuse the slot)", got)
	}
	if got := store.FindTokenByDeviceUID("uid-new"); got != tok {
		t.Errorf("uid-new not bound after migration (got %q)", got)
	}
	if got := store.FindTokenByDeviceUID("uid-old"); got != "" {
		t.Errorf("uid-old should be gone after migration (got %q)", got)
	}
}
