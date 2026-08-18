package tgauth

import (
	"testing"
	"time"
)

func newTestAnonIssuer(t *testing.T) *AnonIssuer {
	t.Helper()
	dir := t.TempDir()
	a := NewAnonIssuer(dir, 7)
	t.Cleanup(a.Close)
	return a
}

func TestAnonIssueAssignsTokenAndUID(t *testing.T) {
	a := newTestAnonIssuer(t)
	tok, uid, exp := a.Issue("1.2.3.4", "Mozilla/UA")
	if len(tok) != 64 {
		t.Fatalf("token should be 64-char hex, got len=%d", len(tok))
	}
	if len(uid) != 8 {
		t.Fatalf("uid should be 8-char hex, got %q", uid)
	}
	if time.Until(exp) < 6*24*time.Hour {
		t.Fatalf("expires_at should be ~7 days out, got %v", exp)
	}
}

func TestAnonLookupReturnsUserAndUpdatesLastSeen(t *testing.T) {
	a := newTestAnonIssuer(t)
	tok, uid, _ := a.Issue("9.9.9.9", "ua1")
	uid2, _, ok := a.LookupAuth(tok, "8.8.8.8")
	if !ok || uid2 != uid {
		t.Fatalf("LookupAuth ok=%v uid=%q want %q", ok, uid2, uid)
	}
	u, ok := a.Lookup(tok)
	if !ok || u.LastIP != "8.8.8.8" || u.LastSeen.IsZero() {
		t.Fatalf("LookupAuth should touch last_ip/last_seen: %+v", u)
	}
}

func TestAnonUnknownTokenReturnsFalse(t *testing.T) {
	a := newTestAnonIssuer(t)
	if _, ok := a.Lookup("nope"); ok {
		t.Fatal("unknown token should not resolve")
	}
	if _, _, ok := a.LookupAuth("nope", ""); ok {
		t.Fatal("unknown token in LookupAuth should not resolve")
	}
}

func TestAnonExpiredSessionTreatedAsMissing(t *testing.T) {
	a := newTestAnonIssuer(t)
	tok, _, _ := a.Issue("1.1.1.1", "")
	a.mu.Lock()
	idx := a.byToken[tok]
	a.users[idx].ExpiresAt = time.Now().UTC().Add(-time.Minute)
	a.mu.Unlock()
	if _, ok := a.Lookup(tok); ok {
		t.Fatal("expired session should not resolve via Lookup")
	}
	if _, _, ok := a.LookupAuth(tok, ""); ok {
		t.Fatal("expired session should not resolve via LookupAuth")
	}
}

func TestAnonRevokeRemovesToken(t *testing.T) {
	a := newTestAnonIssuer(t)
	tok, _, _ := a.Issue("1.1.1.1", "")
	if err := a.Revoke(tok); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, ok := a.Lookup(tok); ok {
		t.Fatal("revoked session should not resolve")
	}
	// Revoking again is a no-op.
	if err := a.Revoke(tok); err != nil {
		t.Fatalf("Revoke idempotent expected nil, got %v", err)
	}
}

func TestAnonPurgeWipesAll(t *testing.T) {
	a := newTestAnonIssuer(t)
	tok1, _, _ := a.Issue("1.1.1.1", "")
	tok2, _, _ := a.Issue("2.2.2.2", "")
	if err := a.Purge(); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if _, ok := a.Lookup(tok1); ok {
		t.Fatal("tok1 should be purged")
	}
	if _, ok := a.Lookup(tok2); ok {
		t.Fatal("tok2 should be purged")
	}
	if len(a.List()) != 0 {
		t.Fatalf("List should be empty after Purge: %d", len(a.List()))
	}
}

func TestAnonCleanExpired(t *testing.T) {
	a := newTestAnonIssuer(t)
	tok1, _, _ := a.Issue("1.1.1.1", "")
	tok2, _, _ := a.Issue("2.2.2.2", "")
	a.mu.Lock()
	a.users[a.byToken[tok1]].ExpiresAt = time.Now().UTC().Add(-time.Hour)
	a.mu.Unlock()
	removed := a.CleanExpired()
	if removed != 1 {
		t.Fatalf("CleanExpired removed=%d want 1", removed)
	}
	if _, ok := a.Lookup(tok2); !ok {
		t.Fatal("tok2 should still resolve")
	}
}

func TestAnonPersistsAcrossReload(t *testing.T) {
	dir := t.TempDir()
	a := NewAnonIssuer(dir, 7)
	tok, uid, _ := a.Issue("1.1.1.1", "ua")
	a.Close()

	a2 := NewAnonIssuer(dir, 7)
	defer a2.Close()
	if _, ok := a2.Lookup(tok); !ok {
		t.Fatal("anon session should survive restart")
	}
	uid2, _, ok := a2.LookupAuth(tok, "")
	if !ok || uid2 != uid {
		t.Fatalf("LookupAuth after reload: ok=%v uid=%q want %q", ok, uid2, uid)
	}
}

func TestAnonSetSessionDaysAffectsNewIssues(t *testing.T) {
	a := newTestAnonIssuer(t)
	a.SetSessionDays(60)
	_, _, exp := a.Issue("1.1.1.1", "")
	delta := time.Until(exp)
	if delta < 59*24*time.Hour || delta > 61*24*time.Hour {
		t.Fatalf("SetSessionDays(60) should produce ~60d expiry, got %v", delta)
	}
}
