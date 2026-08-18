package tgauth

import (
	"errors"
	"testing"
	"time"
)

// TestAddDevice_CrossTokenUIDRejected verifies that AddDevice refuses to bind
// a UID to a token when that UID is already on a different token. This is the
// core defense against the cub-backup-clones-UID scenario where two physical
// devices end up with the same lampac_unic_id and the byUID index gets
// silently overwritten.
func TestAddDevice_CrossTokenUIDRejected(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	defer s.Close()

	exp := time.Now().UTC().Add(30 * 24 * time.Hour)
	if err := s.Add(ApprovedToken{Token: "tok-A", TelegramID: 1, ExpiresAt: exp}); err != nil {
		t.Fatalf("Add tok-A: %v", err)
	}
	if err := s.Add(ApprovedToken{Token: "tok-B", TelegramID: 2, ExpiresAt: exp}); err != nil {
		t.Fatalf("Add tok-B: %v", err)
	}

	now := time.Now().UTC()
	devA := DeviceInfo{UID: "shared01", Fingerprint: "fp-phone", Label: "Android", BoundAt: now, LastSeen: now}
	if added, err := s.AddDevice("tok-A", devA); err != nil || !added {
		t.Fatalf("AddDevice tok-A: added=%v err=%v", added, err)
	}

	// Same UID, different token — should be refused with ErrUIDCrossToken.
	devB := DeviceInfo{UID: "shared01", Fingerprint: "fp-tv", Label: "Android TV", BoundAt: now, LastSeen: now}
	added, err := s.AddDevice("tok-B", devB)
	if added {
		t.Fatal("AddDevice tok-B should not have added the device")
	}
	if !errors.Is(err, ErrUIDCrossToken) {
		t.Fatalf("AddDevice tok-B: expected ErrUIDCrossToken, got %v", err)
	}

	// tok-B must remain device-less.
	if s.DeviceCount("tok-B") != 0 {
		t.Errorf("tok-B device count = %d, want 0", s.DeviceCount("tok-B"))
	}

	// FindTokenByDeviceUID must consistently report tok-A as the owner.
	if got := s.FindTokenByDeviceUID("shared01"); got != "tok-A" {
		t.Errorf("FindTokenByDeviceUID = %q, want %q", got, "tok-A")
	}
}

// TestAddDevice_SameTokenIsIdempotent ensures that re-adding a UID to its own
// token still works (just updates fingerprint) and does NOT trip the cross-
// token check.
func TestAddDevice_SameTokenIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	defer s.Close()

	exp := time.Now().UTC().Add(30 * 24 * time.Hour)
	if err := s.Add(ApprovedToken{Token: "tok-A", TelegramID: 1, ExpiresAt: exp}); err != nil {
		t.Fatalf("Add tok-A: %v", err)
	}

	now := time.Now().UTC()
	dev := DeviceInfo{UID: "uid01", Fingerprint: "fp1", BoundAt: now, LastSeen: now}
	if added, err := s.AddDevice("tok-A", dev); err != nil || !added {
		t.Fatalf("first AddDevice: added=%v err=%v", added, err)
	}

	// Re-add same UID to same token with new fingerprint.
	dev2 := DeviceInfo{UID: "uid01", Fingerprint: "fp2", BoundAt: now, LastSeen: now}
	added, err := s.AddDevice("tok-A", dev2)
	if err != nil {
		t.Fatalf("second AddDevice err = %v, want nil", err)
	}
	if added {
		t.Error("second AddDevice should report added=false (already bound)")
	}
	if s.DeviceCount("tok-A") != 1 {
		t.Errorf("device count = %d, want 1", s.DeviceCount("tok-A"))
	}

	// Fingerprint should have been updated.
	_, dev3, ok := s.FindDeviceByUID("uid01")
	if !ok {
		t.Fatal("FindDeviceByUID: ok=false after update")
	}
	if dev3.Fingerprint != "fp2" {
		t.Errorf("FindDeviceByUID fp = %q, want fp2", dev3.Fingerprint)
	}
}

// TestFindDeviceByUID_ReturnsTokenAndDevice verifies the new lookup that
// the auth-gate middleware uses for fp-aware UID auto-auth.
func TestFindDeviceByUID_ReturnsTokenAndDevice(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	defer s.Close()

	exp := time.Now().UTC().Add(30 * 24 * time.Hour)
	if err := s.Add(ApprovedToken{Token: "tok-A", TelegramID: 1, ExpiresAt: exp}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	now := time.Now().UTC()
	if _, err := s.AddDevice("tok-A", DeviceInfo{UID: "uid01", Fingerprint: "fp1", BoundAt: now, LastSeen: now}); err != nil {
		t.Fatalf("AddDevice: %v", err)
	}

	tok, dev, ok := s.FindDeviceByUID("uid01")
	if !ok {
		t.Fatal("FindDeviceByUID: ok=false, want true")
	}
	if tok != "tok-A" {
		t.Errorf("token = %q, want tok-A", tok)
	}
	if dev == nil || dev.Fingerprint != "fp1" {
		t.Errorf("device fingerprint mismatch: %+v", dev)
	}

	// Missing UID
	if _, _, ok := s.FindDeviceByUID("nope"); ok {
		t.Error("FindDeviceByUID for unknown UID should return ok=false")
	}
	// Empty UID
	if _, _, ok := s.FindDeviceByUID(""); ok {
		t.Error("FindDeviceByUID for empty UID should return ok=false")
	}
}

// TestFindDeviceByUID_ExpiredTokenIgnored ensures we don't auto-auth via a UID
// pointing at an expired token.
func TestFindDeviceByUID_ExpiredTokenIgnored(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	defer s.Close()

	expired := time.Now().UTC().Add(-1 * time.Hour)
	if err := s.Add(ApprovedToken{Token: "tok-old", TelegramID: 1, ExpiresAt: expired}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Manually inject device since AddDevice may filter expired tokens later;
	// AddDevice currently just looks up by token and adds, so this works.
	now := time.Now().UTC()
	_, _ = s.AddDevice("tok-old", DeviceInfo{UID: "uidExp", BoundAt: now, LastSeen: now})

	if _, _, ok := s.FindDeviceByUID("uidExp"); ok {
		t.Error("FindDeviceByUID should ignore expired tokens")
	}
}
