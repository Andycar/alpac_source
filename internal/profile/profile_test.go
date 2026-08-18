package profile

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRegisterAndLogin(t *testing.T) {
	store := NewStore(t.TempDir())

	p, err := store.Register("Alice", "secret123")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if p.Username != "alice" {
		t.Fatalf("username should be lowercased, got %q", p.Username)
	}
	if p.ID == "" || len(p.ID) < 16 {
		t.Fatalf("profile id looks malformed: %q", p.ID)
	}
	if p.PasswordHash == "secret123" {
		t.Fatalf("password stored in clear")
	}

	// Duplicate (case-insensitive)
	if _, err := store.Register("ALICE", "another"); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("expected ErrUsernameTaken, got %v", err)
	}

	// Wrong password
	if _, _, err := store.Login("alice", "wrong", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password: expected ErrInvalidCredentials, got %v", err)
	}

	// Login by exact username
	sess, p2, err := store.Login("alice", "secret123", "test-agent")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if sess.Token == "" || len(sess.Token) < 32 {
		t.Fatalf("session token looks malformed: %q", sess.Token)
	}
	if p2.ID != p.ID {
		t.Fatalf("profile id mismatch after login")
	}
	if p2.LastLoginAt == 0 {
		t.Fatalf("last_login should be updated on successful login")
	}

	// Login by mixed case (we normalize)
	if _, _, err := store.Login("ALICE", "secret123", ""); err != nil {
		t.Fatalf("case-insensitive login: %v", err)
	}

	// Lookup session
	gotSess, gotProfile, err := store.LookupSession(sess.Token)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if gotSess == nil || gotProfile == nil {
		t.Fatalf("lookup returned nil")
	}
	if gotProfile.ID != p.ID {
		t.Fatalf("lookup profile id mismatch")
	}

	// Logout invalidates
	store.Logout(sess.Token)
	gotSess2, _, err := store.LookupSession(sess.Token)
	if err != nil {
		t.Fatalf("post-logout lookup err: %v", err)
	}
	if gotSess2 != nil {
		t.Fatalf("logout should invalidate session, got %+v", gotSess2)
	}
}

func TestRegisterValidation(t *testing.T) {
	store := NewStore(t.TempDir())

	cases := []struct {
		name, user, pass string
		wantErr          error
	}{
		{"too short", "ab", "secret123", ErrUsernameInvalid},
		{"bad chars", "alice!", "secret123", ErrUsernameInvalid},
		{"uppercase ok (lowercased)", "ALICE", "secret123", nil},
		{"weak password", "bob", "12345", ErrPasswordWeak},
		{"ok", "carol_99", "longenough", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := store.Register(tc.user, tc.pass)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("got %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestSyncCodeLifecycle(t *testing.T) {
	store := NewStore(t.TempDir())
	p, _ := store.Register("dave", "secret123")

	code, err := store.IssueSyncCode(p.ID)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if len(code.Code) != 9 || code.Code[4] != '-' {
		t.Fatalf("code shape wrong: %q", code.Code)
	}

	// First redeem succeeds — also accepts a lower-cased no-dash variant
	// because we want TV remotes to be forgiving.
	dirty := strings.ToLower(strings.Replace(code.Code, "-", "", -1))
	sess, p2, err := store.RedeemSyncCode(dirty, "remote-tv")
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if p2.ID != p.ID || sess.ProfileID != p.ID {
		t.Fatalf("redeem returned wrong profile")
	}

	// Second redeem fails — one-shot
	if _, _, err := store.RedeemSyncCode(code.Code, ""); !errors.Is(err, ErrSyncCodeUsed) {
		t.Fatalf("second redeem: expected ErrSyncCodeUsed, got %v", err)
	}

	// Garbage redeem fails
	if _, _, err := store.RedeemSyncCode("AAAA-BBBB", ""); !errors.Is(err, ErrSyncCodeInvalid) {
		t.Fatalf("garbage redeem: expected ErrSyncCodeInvalid, got %v", err)
	}

	// IssueSyncCode for unknown profile rejects
	if _, err := store.IssueSyncCode("not-a-real-id"); !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("issue unknown: expected ErrProfileNotFound, got %v", err)
	}
}

func TestSyncCodeExpires(t *testing.T) {
	prevTTL := SyncCodeTTL
	SyncCodeTTL = 50 * time.Millisecond
	defer func() { SyncCodeTTL = prevTTL }()

	store := NewStore(t.TempDir())
	p, err := store.Register("eddie", "secret123")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	code, _ := store.IssueSyncCode(p.ID)

	time.Sleep(120 * time.Millisecond)
	if _, _, err := store.RedeemSyncCode(code.Code, ""); !errors.Is(err, ErrSyncCodeInvalid) {
		t.Fatalf("expired: expected ErrSyncCodeInvalid, got %v", err)
	}
}

func TestSessionExpiryAndRollover(t *testing.T) {
	prevTTL, prevRoll := SessionTTL, SessionRollover
	SessionTTL = 200 * time.Millisecond
	SessionRollover = 150 * time.Millisecond
	defer func() { SessionTTL, SessionRollover = prevTTL, prevRoll }()

	store := NewStore(t.TempDir())
	store.Register("fay", "secret123")
	sess, _, _ := store.Login("fay", "secret123", "")

	// Immediately within the rollover window: lookup should refresh expiry.
	time.Sleep(80 * time.Millisecond)
	if _, _, err := store.LookupSession(sess.Token); err != nil {
		t.Fatalf("lookup early: %v", err)
	}

	// Wait beyond original TTL — rollover should have kept it alive.
	time.Sleep(150 * time.Millisecond)
	if _, _, err := store.LookupSession(sess.Token); err != nil {
		t.Fatalf("post-rollover lookup: %v", err)
	}

	// Now wait long enough that the rolled expiry also passes (no further lookups).
	time.Sleep(220 * time.Millisecond)
	if _, _, err := store.LookupSession(sess.Token); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("expected ErrSessionExpired, got %v", err)
	}
	// And the expired session is now gone from memory.
	if got, _, _ := store.LookupSession(sess.Token); got != nil {
		t.Fatalf("expired session should be evicted, got %+v", got)
	}
}

func TestChangePassword(t *testing.T) {
	store := NewStore(t.TempDir())
	p, _ := store.Register("gus", "oldsecret")

	if err := store.ChangePassword(p.ID, "wrong", "newsecret"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong old: got %v", err)
	}
	if err := store.ChangePassword(p.ID, "oldsecret", "12345"); !errors.Is(err, ErrPasswordWeak) {
		t.Fatalf("weak new: got %v", err)
	}
	if err := store.ChangePassword(p.ID, "oldsecret", "newsecret"); err != nil {
		t.Fatalf("change: %v", err)
	}

	// Old password no longer works
	if _, _, err := store.Login("gus", "oldsecret", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("old still works")
	}
	if _, _, err := store.Login("gus", "newsecret", ""); err != nil {
		t.Fatalf("new login: %v", err)
	}
}

func TestPersistenceRoundtrip(t *testing.T) {
	dir := t.TempDir()
	s1 := NewStore(dir)
	p, _ := s1.Register("hank", "secret123")
	sess, _, _ := s1.Login("hank", "secret123", "")
	code, _ := s1.IssueSyncCode(p.ID)

	// New store from the same dir must see everything.
	s2 := NewStore(dir)
	gotP := s2.GetProfile(p.ID)
	if gotP == nil || gotP.Username != "hank" {
		t.Fatalf("profile lost across reload: %+v", gotP)
	}
	gotSess, _, err := s2.LookupSession(sess.Token)
	if err != nil || gotSess == nil {
		t.Fatalf("session lost across reload (err=%v sess=%+v)", err, gotSess)
	}
	if _, _, err := s2.RedeemSyncCode(code.Code, ""); err != nil {
		t.Fatalf("sync code lost across reload: %v", err)
	}
}

func TestNormalizeSyncCode(t *testing.T) {
	cases := []struct{ in, want string }{
		{"abcd-efgh", "ABCD-EFGH"},
		{"abcdefgh", "ABCD-EFGH"},
		{"ABCD-EFGH", "ABCD-EFGH"},
		{"  abcd efgh  ", "ABCD-EFGH"},
		{"abc", "ABC"},     // clearly too short → cleaned but not dashified
		{"abcde", "ABCDE"}, // 5 chars → cleaned but not dashified
	}
	for _, tc := range cases {
		if got := normalizeSyncCode(tc.in); got != tc.want {
			t.Errorf("normalize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestConcurrentRegisterDedup(t *testing.T) {
	store := NewStore(t.TempDir())

	const N = 16
	var wg sync.WaitGroup
	wg.Add(N)
	successes := make([]bool, N)
	for i := 0; i < N; i++ {
		go func(idx int) {
			defer wg.Done()
			_, err := store.Register("racer", "secret123")
			if err == nil {
				successes[idx] = true
			}
		}(i)
	}
	wg.Wait()

	count := 0
	for _, ok := range successes {
		if ok {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 successful register under race, got %d", count)
	}
}

func TestCreateForTGOwnerAndPINLogin(t *testing.T) {
	store := NewStore(t.TempDir())
	const tgID int64 = 12345

	// 1) Create profile owned by a TG user, with PIN.
	p, err := store.CreateForTGOwner(tgID, "kids", "1234")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if p.OwnerTGID != tgID || p.PINHash == "" || p.PasswordHash != "" {
		t.Fatalf("unexpected profile shape: %+v", p)
	}

	// 2) PIN login works
	sess, p2, err := store.LoginByPIN("kids", "1234", "tv-remote")
	if err != nil || sess == nil || p2.ID != p.ID {
		t.Fatalf("pin login: err=%v sess=%+v p2=%+v", err, sess, p2)
	}

	// 3) Wrong PIN — ErrInvalidCredentials, NOT a leaky error
	if _, _, err := store.LoginByPIN("kids", "9999", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong pin: got %v", err)
	}

	// 4) Username right but PIN-less profile → ErrPINNotSet
	store.Register("nopinuser", "secret123")
	if _, _, err := store.LoginByPIN("nopinuser", "1234", ""); !errors.Is(err, ErrPINNotSet) {
		t.Fatalf("no-pin: got %v", err)
	}

	// 5) Non-digit PIN rejected during create
	if _, err := store.CreateForTGOwner(tgID, "second", "abcd"); !errors.Is(err, ErrPINInvalid) {
		t.Fatalf("bad pin format: got %v", err)
	}

	// 6) Without TG owner → ErrNotOwner
	if _, err := store.CreateForTGOwner(0, "stray", "1234"); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("zero tgID: got %v", err)
	}
}

func TestSetPINOwnershipGuard(t *testing.T) {
	store := NewStore(t.TempDir())
	const ownerA, ownerB int64 = 100, 200
	p, _ := store.CreateForTGOwner(ownerA, "alice", "")

	// Owner A can set
	if err := store.SetPIN(p.ID, ownerA, "5678"); err != nil {
		t.Fatalf("owner set: %v", err)
	}
	// Owner B refused
	if err := store.SetPIN(p.ID, ownerB, "9999"); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("other owner: got %v", err)
	}
	// ownerTGID==0 bypass (used by web flow)
	if err := store.SetPIN(p.ID, 0, "0000"); err != nil {
		t.Fatalf("bypass set: %v", err)
	}
	// Confirm latest PIN works for login
	if _, _, err := store.LoginByPIN("alice", "0000", ""); err != nil {
		t.Fatalf("login after bypass set: %v", err)
	}
	// Clear PIN: empty string → no PIN
	if err := store.SetPIN(p.ID, 0, ""); err != nil {
		t.Fatalf("clear pin: %v", err)
	}
	if _, _, err := store.LoginByPIN("alice", "0000", ""); !errors.Is(err, ErrPINNotSet) {
		t.Fatalf("after clear: got %v", err)
	}
}

func TestListByOwnerAndDeleteOwned(t *testing.T) {
	store := NewStore(t.TempDir())
	const ownerA, ownerB int64 = 11, 22
	store.CreateForTGOwner(ownerA, "kid1", "1111")
	pB, _ := store.CreateForTGOwner(ownerA, "kid2", "2222")
	store.CreateForTGOwner(ownerB, "other", "3333")

	listA := store.ListByOwnerTG(ownerA)
	if len(listA) != 2 {
		t.Fatalf("owner A should see 2 profiles, got %d", len(listA))
	}
	listB := store.ListByOwnerTG(ownerB)
	if len(listB) != 1 {
		t.Fatalf("owner B should see 1 profile, got %d", len(listB))
	}
	if store.ListByOwnerTG(0) != nil {
		t.Fatalf("zero tgID should not list anything")
	}

	// Login to create a session, then delete — session must be invalidated.
	sess, _, _ := store.LoginByPIN("kid2", "2222", "")
	if err := store.DeleteOwned(pB.ID, ownerB); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("wrong owner delete: got %v", err)
	}
	if err := store.DeleteOwned(pB.ID, ownerA); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// Session of deleted profile should not resolve anymore.
	got, _, err := store.LookupSession(sess.Token)
	if got != nil || err != nil {
		t.Fatalf("dead session lookup: got %+v err=%v", got, err)
	}
	// Username freed (can re-create with same name).
	if _, err := store.CreateForTGOwner(ownerA, "kid2", "4444"); err != nil {
		t.Fatalf("reuse username after delete: %v", err)
	}
}

func TestPurgeExpired(t *testing.T) {
	prevTTL, prevCodeTTL := SessionTTL, SyncCodeTTL
	SessionTTL = 30 * time.Millisecond
	SyncCodeTTL = 30 * time.Millisecond
	defer func() { SessionTTL, SyncCodeTTL = prevTTL, prevCodeTTL }()

	store := NewStore(t.TempDir())
	p, _ := store.Register("ivan", "secret123")
	store.Login("ivan", "secret123", "")
	store.IssueSyncCode(p.ID)

	time.Sleep(80 * time.Millisecond)
	store.PurgeExpired()

	store.mu.RLock()
	defer store.mu.RUnlock()
	if len(store.sessions) != 0 {
		t.Fatalf("sessions not purged: %d remain", len(store.sessions))
	}
	if len(store.codes) != 0 {
		t.Fatalf("codes not purged: %d remain", len(store.codes))
	}
}
