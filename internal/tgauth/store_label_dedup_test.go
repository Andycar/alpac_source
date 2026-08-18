package tgauth

import (
	"testing"
	"time"
)

// Two physical devices of the same platform (e.g. two Android phones) carry the
// same UA-derived label but different hardware fingerprints. They must occupy
// TWO device slots — the old label-dedup merged them into one, and the devices
// then stole the slot from each other on every request (ping-pong UID
// migrations + a bot notification per source switch, device count stuck at 1).
func TestAddDevice_SameLabelDifferentFingerprintsKeepsBoth(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	defer s.Close()

	exp := time.Now().UTC().Add(30 * 24 * time.Hour)
	if err := s.Add(ApprovedToken{Token: "tok-A", TelegramID: 1, ExpiresAt: exp}); err != nil {
		t.Fatalf("Add tok-A: %v", err)
	}

	now := time.Now().UTC()
	phone1 := DeviceInfo{UID: "uid-ph1", Fingerprint: "fp-phone-1", Label: "Android", BoundAt: now, LastSeen: now}
	phone2 := DeviceInfo{UID: "uid-ph2", Fingerprint: "fp-phone-2", Label: "Android", BoundAt: now, LastSeen: now}

	if added, err := s.AddDevice("tok-A", phone1); err != nil || !added {
		t.Fatalf("AddDevice phone1: added=%v err=%v", added, err)
	}
	added, err := s.AddDevice("tok-A", phone2)
	if err != nil {
		t.Fatalf("AddDevice phone2: %v", err)
	}
	if !added {
		t.Fatal("phone2 (same label, different fingerprint) must be ADDED, not merged into phone1's slot")
	}
	if got := s.DeviceCount("tok-A"); got != 2 {
		t.Errorf("device count = %d, want 2", got)
	}
	// Both UIDs stay resolvable — neither phone stole the other's slot.
	if got := s.FindTokenByDeviceUID("uid-ph1"); got != "tok-A" {
		t.Errorf("uid-ph1 lost its binding (got %q)", got)
	}
	if got := s.FindTokenByDeviceUID("uid-ph2"); got != "tok-A" {
		t.Errorf("uid-ph2 not bound (got %q)", got)
	}
}

// A fingerprint-less client re-adding the same label is still treated as the
// same physical device reconnecting after a localStorage wipe (the legacy
// dedup), because there is no fingerprint evidence to tell the devices apart.
func TestAddDevice_SameLabelNoFingerprintsStillMerges(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	defer s.Close()

	exp := time.Now().UTC().Add(30 * 24 * time.Hour)
	if err := s.Add(ApprovedToken{Token: "tok-A", TelegramID: 1, ExpiresAt: exp}); err != nil {
		t.Fatalf("Add tok-A: %v", err)
	}

	now := time.Now().UTC()
	if added, err := s.AddDevice("tok-A", DeviceInfo{UID: "uid-old", Label: "LG TV", BoundAt: now, LastSeen: now}); err != nil || !added {
		t.Fatalf("AddDevice old: added=%v err=%v", added, err)
	}
	added, err := s.AddDevice("tok-A", DeviceInfo{UID: "uid-new", Label: "LG TV", BoundAt: now, LastSeen: now})
	if err != nil {
		t.Fatalf("AddDevice new: %v", err)
	}
	if added {
		t.Fatal("fp-less same-label device must MERGE into the existing slot (wipe recovery), not add a duplicate")
	}
	if got := s.DeviceCount("tok-A"); got != 1 {
		t.Errorf("device count = %d, want 1", got)
	}
	// The slot must now answer to the new UID.
	if got := s.FindTokenByDeviceUID("uid-new"); got != "tok-A" {
		t.Errorf("uid-new not bound after merge (got %q)", got)
	}
	if got := s.FindTokenByDeviceUID("uid-old"); got != "" {
		t.Errorf("uid-old should be gone after merge (got %q)", got)
	}
}

// A fingerprint-less request must NOT merge into a slot that HAS a fingerprint:
// the typical pair is a browser (sends fp) and a native client (no fp) on the
// same platform — different apps, possibly different devices.
func TestAddDevice_NoFingerprintDoesNotMergeIntoFingerprintedSlot(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	defer s.Close()

	exp := time.Now().UTC().Add(30 * 24 * time.Hour)
	if err := s.Add(ApprovedToken{Token: "tok-A", TelegramID: 1, ExpiresAt: exp}); err != nil {
		t.Fatalf("Add tok-A: %v", err)
	}

	now := time.Now().UTC()
	if added, err := s.AddDevice("tok-A", DeviceInfo{UID: "uid-web", Fingerprint: "fp-web", Label: "Android", BoundAt: now, LastSeen: now}); err != nil || !added {
		t.Fatalf("AddDevice web: added=%v err=%v", added, err)
	}
	added, err := s.AddDevice("tok-A", DeviceInfo{UID: "uid-native", Label: "Android", BoundAt: now, LastSeen: now})
	if err != nil {
		t.Fatalf("AddDevice native: %v", err)
	}
	if !added {
		t.Fatal("fp-less device must not merge into a fingerprinted slot of the same label")
	}
	if got := s.DeviceCount("tok-A"); got != 2 {
		t.Errorf("device count = %d, want 2", got)
	}
}
