package tgauth

import (
	"testing"
	"time"
)

// TestFindTokenByDeviceFingerprint_AmbiguousRefused is the security core of
// full-wipe recovery: when the same fingerprint appears on TWO different
// accounts, recovery must restore NOBODY rather than silently log the user
// into a stranger's account. A single-account match still resolves.
func TestFindTokenByDeviceFingerprint_AmbiguousRefused(t *testing.T) {
	s := NewStore(t.TempDir())
	defer s.Close()
	exp := time.Now().UTC().Add(30 * 24 * time.Hour)
	now := time.Now().UTC()
	_ = s.Add(ApprovedToken{Token: "tok-A", TelegramID: 1, ExpiresAt: exp})
	_ = s.Add(ApprovedToken{Token: "tok-B", TelegramID: 2, ExpiresAt: exp})

	// Unique fingerprint on A → resolves.
	if _, err := s.AddDevice("tok-A", DeviceInfo{UID: "a1", Fingerprint: "fp-unique", BoundAt: now, LastSeen: now}); err != nil {
		t.Fatalf("AddDevice a1: %v", err)
	}
	if tok, _, ok := s.FindTokenByDeviceFingerprint("fp-unique"); !ok || tok != "tok-A" {
		t.Fatalf("unique fp: got tok=%q ok=%v, want tok-A", tok, ok)
	}

	// Same fingerprint now also on B (two physical devices that happen to hash
	// the same) → ambiguous → refuse.
	if _, err := s.AddDevice("tok-B", DeviceInfo{UID: "b1", Fingerprint: "fp-unique", BoundAt: now, LastSeen: now}); err != nil {
		t.Fatalf("AddDevice b1: %v", err)
	}
	if tok, _, ok := s.FindTokenByDeviceFingerprint("fp-unique"); ok {
		t.Fatalf("ambiguous fp must refuse, got tok=%q", tok)
	}
}

// TestFindTokenByStableFP mirrors the precise-fp behavior for the coarse
// stable anchor and confirms an expired token is ignored.
func TestFindTokenByStableFP(t *testing.T) {
	s := NewStore(t.TempDir())
	defer s.Close()
	now := time.Now().UTC()
	_ = s.Add(ApprovedToken{Token: "tok-live", TelegramID: 1, ExpiresAt: now.Add(24 * time.Hour)})
	_ = s.Add(ApprovedToken{Token: "tok-dead", TelegramID: 2, ExpiresAt: now.Add(-time.Hour)})

	if _, err := s.AddDevice("tok-live", DeviceInfo{UID: "l1", StableFP: "sfp-tv", BoundAt: now, LastSeen: now}); err != nil {
		t.Fatalf("AddDevice l1: %v", err)
	}
	if tok, _, ok := s.FindTokenByStableFP("sfp-tv"); !ok || tok != "tok-live" {
		t.Fatalf("stable fp: got tok=%q ok=%v, want tok-live", tok, ok)
	}
	if _, _, ok := s.FindTokenByStableFP("sfp-missing"); ok {
		t.Fatal("unknown stable fp must not resolve")
	}

	// A device on an EXPIRED token must never resolve.
	_ = s.Add(ApprovedToken{Token: "tok-dead2", TelegramID: 3, ExpiresAt: now.Add(-time.Hour), Devices: []DeviceInfo{{UID: "d1", StableFP: "sfp-dead", BoundAt: now, LastSeen: now}}})
	if _, _, ok := s.FindTokenByStableFP("sfp-dead"); ok {
		t.Fatal("expired token stable fp must not resolve")
	}
}

// TestAddDevice_StableFPMigration verifies that a client whose precise fp
// drifted (firmware update) but whose stable fp is unchanged re-attaches to
// the SAME device record (UID migrated) instead of piling up duplicates.
func TestAddDevice_StableFPMigration(t *testing.T) {
	s := NewStore(t.TempDir())
	defer s.Close()
	now := time.Now().UTC()
	_ = s.Add(ApprovedToken{Token: "tok", TelegramID: 1, ExpiresAt: now.Add(24 * time.Hour)})

	if added, err := s.AddDevice("tok", DeviceInfo{UID: "uid-old", Fingerprint: "fp-v1", StableFP: "sfp-tv", BoundAt: now, LastSeen: now}); err != nil || !added {
		t.Fatalf("first bind: added=%v err=%v", added, err)
	}
	// Relaunch after firmware update: new UID, new precise fp, SAME stable fp.
	if added, err := s.AddDevice("tok", DeviceInfo{UID: "uid-new", Fingerprint: "fp-v2", StableFP: "sfp-tv", BoundAt: now, LastSeen: now}); err != nil {
		t.Fatalf("second bind: err=%v", err)
	} else if added {
		t.Fatal("second bind must MIGRATE (not add) via stable fp")
	}
	if s.DeviceCount("tok") != 1 {
		t.Fatalf("device count = %d, want 1 (migrated, not duplicated)", s.DeviceCount("tok"))
	}
	// New UID and refreshed precise fp must both resolve to the token.
	if tok := s.FindTokenByDeviceUID("uid-new"); tok != "tok" {
		t.Fatalf("uid-new lookup = %q, want tok", tok)
	}
	if tok, _, ok := s.FindTokenByDeviceFingerprint("fp-v2"); !ok || tok != "tok" {
		t.Fatalf("fp-v2 lookup = %q ok=%v, want tok", tok, ok)
	}
}
