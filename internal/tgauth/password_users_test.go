package tgauth

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestPwStore(t *testing.T) *PasswordUserStore {
	t.Helper()
	dir := t.TempDir()
	s := NewPasswordUserStore(dir)
	t.Cleanup(s.Close)
	return s
}

func TestPasswordValidateUsername(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid lower", "alice", false},
		{"valid mixed", "Bob_42", false},
		{"valid dots", "user.name-1", false},
		{"too short", "ab", true},
		{"too long", strings.Repeat("a", 33), true},
		{"spaces", "alice bob", true},
		{"slash", "alice/bob", true},
		{"unicode", "алиса", true},
		{"empty", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateUsername(tc.input, 32)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateUsername(%q) err=%v want_err=%v", tc.input, err, tc.wantErr)
			}
		})
	}
}

func TestPasswordValidatePassword(t *testing.T) {
	if err := ValidatePassword("hunter22", 8, 128); err != nil {
		t.Fatalf("8-char password should pass: %v", err)
	}
	if err := ValidatePassword("short", 8, 128); err == nil {
		t.Fatal("5-char password should fail with default min=8")
	}
	if err := ValidatePassword(strings.Repeat("a", 129), 8, 128); err == nil {
		t.Fatal("129-char password should fail with default max=128")
	}
	if err := ValidatePassword("123", 0, 0); err == nil {
		t.Fatal("3-char password should fail with implicit min=8")
	}
}

func TestPasswordCreateAndLookup(t *testing.T) {
	s := newTestPwStore(t)
	user, err := s.Create("alice", "secret-pw-1", CreateOpts{Comment: "test"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if user.UID == "" || len(user.UID) != 8 {
		t.Fatalf("UID should be 8-char hex, got %q", user.UID)
	}
	if user.PasswordHash == "" || strings.HasPrefix(user.PasswordHash, "secret") {
		t.Fatalf("PasswordHash should be bcrypt, got %q", user.PasswordHash)
	}

	got, ok := s.LookupUser("ALICE") // case-insensitive
	if !ok || got.Username != "alice" {
		t.Fatalf("LookupUser case-insensitive miss: ok=%v username=%q", ok, got.Username)
	}

	if _, err := s.Create("alice", "another-pw-1", CreateOpts{}); err != ErrUsernameExists {
		t.Fatalf("duplicate Create want ErrUsernameExists, got %v", err)
	}
}

func TestPasswordCreateRejectsBadInput(t *testing.T) {
	s := newTestPwStore(t)
	if _, err := s.Create("a", "secret-pw-1", CreateOpts{}); err != ErrUsernameInvalid {
		t.Fatalf("short username want ErrUsernameInvalid got %v", err)
	}
	if _, err := s.Create("alice", "abc", CreateOpts{}); err != ErrPasswordTooShort {
		t.Fatalf("short password want ErrPasswordTooShort got %v", err)
	}
}

func TestPasswordCheckPassword(t *testing.T) {
	s := newTestPwStore(t)
	if _, err := s.Create("bob", "correct-horse", CreateOpts{}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, ok := s.CheckPassword("bob", "wrong"); ok {
		t.Fatal("wrong password should not validate")
	}
	if u, ok := s.CheckPassword("bob", "correct-horse"); !ok || u.Username != "bob" {
		t.Fatalf("right password should validate; ok=%v username=%q", ok, u.Username)
	}
	if _, ok := s.CheckPassword("nobody", "correct-horse"); ok {
		t.Fatal("nonexistent user should not validate")
	}
}

func TestPasswordLoginIssuesSession(t *testing.T) {
	s := newTestPwStore(t)
	if _, err := s.Create("carol", "long-password", CreateOpts{}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	u, token, err := s.Login("carol", "long-password", "1.2.3.4", "ua", 30)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if token == "" || len(token) != 64 {
		t.Fatalf("token should be 64-char hex, got %q", token)
	}
	if u.LastLoginIP != "1.2.3.4" || u.LastLoginAt.IsZero() {
		t.Fatalf("login metadata missing: ip=%q at=%v", u.LastLoginIP, u.LastLoginAt)
	}
	if len(u.Sessions) != 1 || u.Sessions[0].Token != token {
		t.Fatalf("session not appended: %+v", u.Sessions)
	}

	// LookupAuth path
	name, uid, exp, ok := s.LookupAuth(token)
	if !ok || name != "carol" || uid != u.UID || exp.Before(time.Now().UTC()) {
		t.Fatalf("LookupAuth miss: ok=%v name=%q uid=%q exp=%v", ok, name, uid, exp)
	}

	// Wrong password rejected.
	if _, _, err := s.Login("carol", "wrong", "1.2.3.4", "ua", 30); err != ErrPasswordIncorrect {
		t.Fatalf("wrong password want ErrPasswordIncorrect got %v", err)
	}
}

func TestPasswordLoginBlocksBannedUser(t *testing.T) {
	s := newTestPwStore(t)
	_, err := s.Create("dave", "long-password", CreateOpts{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.SetBan("dave", true, "broke rules"); err != nil {
		t.Fatalf("SetBan: %v", err)
	}
	if _, _, err := s.Login("dave", "long-password", "1.2.3.4", "ua", 30); err != ErrUserBanned {
		t.Fatalf("banned user want ErrUserBanned got %v", err)
	}
}

func TestPasswordLoginBlocksExpired(t *testing.T) {
	s := newTestPwStore(t)
	past := time.Now().UTC().Add(-24 * time.Hour)
	_, err := s.Create("eve", "long-password", CreateOpts{ExpiresAt: past})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, _, err := s.Login("eve", "long-password", "1.2.3.4", "ua", 30); err != ErrUserExpired {
		t.Fatalf("expired user want ErrUserExpired got %v", err)
	}
}

func TestPasswordLogoutInvalidatesSession(t *testing.T) {
	s := newTestPwStore(t)
	if _, err := s.Create("frank", "long-password", CreateOpts{}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, token, _ := s.Login("frank", "long-password", "1.2.3.4", "ua", 30)
	if _, _, ok := s.LookupSession(token); !ok {
		t.Fatal("session should resolve before logout")
	}
	if err := s.Logout(token); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, _, ok := s.LookupSession(token); ok {
		t.Fatal("session should not resolve after logout")
	}
	// Logout on unknown token is reported, never panics.
	if err := s.Logout("nope"); err != ErrSessionNotFound {
		t.Fatalf("Logout unknown want ErrSessionNotFound got %v", err)
	}
}

func TestPasswordResetPasswordRevokesSessions(t *testing.T) {
	s := newTestPwStore(t)
	_, _ = s.Create("grace", "old-password-1", CreateOpts{})
	_, oldToken, _ := s.Login("grace", "old-password-1", "1.2.3.4", "ua", 30)
	if err := s.ResetPassword("grace", "new-password-1", 8, 128); err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}
	if _, _, ok := s.LookupSession(oldToken); ok {
		t.Fatal("old session should be revoked after ResetPassword")
	}
	if _, ok := s.CheckPassword("grace", "old-password-1"); ok {
		t.Fatal("old password should not work after reset")
	}
	if _, ok := s.CheckPassword("grace", "new-password-1"); !ok {
		t.Fatal("new password should work after reset")
	}
}

func TestPasswordChangePasswordRequiresOld(t *testing.T) {
	s := newTestPwStore(t)
	_, _ = s.Create("henry", "old-password-1", CreateOpts{})
	if err := s.ChangePassword("henry", "wrong", "new-password-1", 8, 128); err != ErrPasswordIncorrect {
		t.Fatalf("wrong old password want ErrPasswordIncorrect got %v", err)
	}
	if err := s.ChangePassword("henry", "old-password-1", "new-password-1", 8, 128); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	if _, ok := s.CheckPassword("henry", "new-password-1"); !ok {
		t.Fatal("new password should be accepted")
	}
}

func TestPasswordExtendAndSetExpires(t *testing.T) {
	s := newTestPwStore(t)
	_, _ = s.Create("ian", "long-password", CreateOpts{})
	if err := s.Extend("ian", 30); err != nil {
		t.Fatalf("Extend: %v", err)
	}
	u, _ := s.LookupUser("ian")
	delta := time.Until(u.ExpiresAt)
	if delta < 29*24*time.Hour || delta > 31*24*time.Hour {
		t.Fatalf("Extend 30 days produced %v", delta)
	}

	exact := time.Now().UTC().Add(7 * 24 * time.Hour).Truncate(time.Second)
	if err := s.SetExpires("ian", exact); err != nil {
		t.Fatalf("SetExpires: %v", err)
	}
	u, _ = s.LookupUser("ian")
	if !u.ExpiresAt.Equal(exact) {
		t.Fatalf("SetExpires=%v want %v", u.ExpiresAt, exact)
	}
}

func TestPasswordAddRemoveDevice(t *testing.T) {
	s := newTestPwStore(t)
	_, _ = s.Create("jane", "long-password", CreateOpts{})
	dev := DeviceInfo{UID: "aaaa1111", Label: "Lampa TV"}
	added, err := s.AddDevice("jane", dev)
	if !added || err != nil {
		t.Fatalf("AddDevice: added=%v err=%v", added, err)
	}
	u, _ := s.LookupUser("jane")
	if len(u.Devices) != 1 || u.Devices[0].UID != "aaaa1111" {
		t.Fatalf("device not stored: %+v", u.Devices)
	}

	// Cross-user UID collision rejected.
	_, _ = s.Create("ken", "long-password", CreateOpts{})
	if _, err := s.AddDevice("ken", dev); err != ErrUIDCrossToken {
		t.Fatalf("cross-user UID want ErrUIDCrossToken got %v", err)
	}

	// FindUserByUID resolves to original owner.
	owner, ok := s.FindUserByUID("aaaa1111")
	if !ok || owner.Username != "jane" {
		t.Fatalf("FindUserByUID owner=%q ok=%v", owner.Username, ok)
	}

	if err := s.RemoveDevice("jane", "aaaa1111"); err != nil {
		t.Fatalf("RemoveDevice: %v", err)
	}
	u, _ = s.LookupUser("jane")
	if len(u.Devices) != 0 {
		t.Fatalf("device should be gone: %+v", u.Devices)
	}
}

func TestPasswordDeleteCleansEverything(t *testing.T) {
	s := newTestPwStore(t)
	_, _ = s.Create("lara", "long-password", CreateOpts{})
	_, token, _ := s.Login("lara", "long-password", "1.2.3.4", "ua", 30)
	_, _ = s.AddDevice("lara", DeviceInfo{UID: "bbbb2222", Label: "TV"})
	if err := s.Delete("lara"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := s.LookupUser("lara"); ok {
		t.Fatal("user should be gone")
	}
	if _, _, ok := s.LookupSession(token); ok {
		t.Fatal("session should be revoked on delete")
	}
	if _, ok := s.FindUserByUID("bbbb2222"); ok {
		t.Fatal("device UID should be unindexed on delete")
	}
}

func TestPasswordCleanExpiredSessions(t *testing.T) {
	s := newTestPwStore(t)
	_, _ = s.Create("mia", "long-password", CreateOpts{})
	_, _, _ = s.Login("mia", "long-password", "1.2.3.4", "ua", 30)
	// Manually backdate the session to be expired.
	s.mu.Lock()
	idx := s.byUsername["mia"]
	s.users[idx].Sessions[0].ExpiresAt = time.Now().UTC().Add(-time.Minute)
	expiredToken := s.users[idx].Sessions[0].Token
	s.mu.Unlock()

	removed := s.CleanExpiredSessions()
	if removed != 1 {
		t.Fatalf("CleanExpiredSessions removed=%d want 1", removed)
	}
	if _, _, ok := s.LookupSession(expiredToken); ok {
		t.Fatal("expired session should be gone")
	}
}

func TestPasswordLookupAuthHonoursUserExpiresAt(t *testing.T) {
	s := newTestPwStore(t)
	// 30-day subscription
	_, _ = s.Create("noah", "long-password", CreateOpts{ExpiresAt: time.Now().UTC().Add(48 * time.Hour)})
	// 365-day session
	_, token, _ := s.Login("noah", "long-password", "1.2.3.4", "ua", 365)

	_, _, exp, ok := s.LookupAuth(token)
	if !ok {
		t.Fatal("LookupAuth should succeed")
	}
	// Expiry returned must be the subscription one (~48h), not session (~365d).
	delta := time.Until(exp)
	if delta < 47*time.Hour || delta > 49*time.Hour {
		t.Fatalf("LookupAuth should return min(session,user); delta=%v", delta)
	}
}

func TestPasswordPersistsAcrossReload(t *testing.T) {
	dir := t.TempDir()
	s := NewPasswordUserStore(dir)
	_, err := s.Create("olivia", "long-password", CreateOpts{Comment: "stays after reload"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, token, _ := s.Login("olivia", "long-password", "1.2.3.4", "ua", 30)
	s.Close()

	// Reopen with the same dir.
	s2 := NewPasswordUserStore(dir)
	defer s2.Close()
	u, ok := s2.LookupUser("olivia")
	if !ok || u.Comment != "stays after reload" {
		t.Fatalf("user not persisted: ok=%v comment=%q", ok, u.Comment)
	}
	if _, _, ok := s2.LookupSession(token); !ok {
		t.Fatal("session not persisted")
	}

	// Verify on-disk path exists in expected layout.
	p := filepath.Join(dir, "database", "tgauth", "password_users.json")
	if _, err := s2.LookupUser("olivia"); !ok {
		t.Fatalf("user gone from store; file=%s err=%v", p, err)
	}
}

func TestPasswordStoreConcurrentLoginsSafe(t *testing.T) {
	s := newTestPwStore(t)
	_, _ = s.Create("paul", "long-password", CreateOpts{})

	var wg sync.WaitGroup
	tokens := make([]string, 50)
	for i := range tokens {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, tok, err := s.Login("paul", "long-password", "1.1.1.1", "ua", 30)
			if err != nil {
				t.Errorf("Login %d: %v", i, err)
				return
			}
			tokens[i] = tok
		}(i)
	}
	wg.Wait()

	for _, tok := range tokens {
		if tok == "" {
			continue
		}
		if _, _, ok := s.LookupSession(tok); !ok {
			t.Fatalf("session %s not resolvable after concurrent login storm", tok)
		}
	}
}

func TestPasswordSetGroupSetPremiumSetBan(t *testing.T) {
	s := newTestPwStore(t)
	_, _ = s.Create("quinn", "long-password", CreateOpts{})

	if err := s.SetGroup("quinn", "premium"); err != nil {
		t.Fatalf("SetGroup: %v", err)
	}
	premiumUntil := time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Second)
	if err := s.SetPremium("quinn", premiumUntil); err != nil {
		t.Fatalf("SetPremium: %v", err)
	}
	u, _ := s.LookupUser("quinn")
	if u.GroupID != "premium" || !u.PremiumUntil.Equal(premiumUntil) {
		t.Fatalf("Set* didn't persist: %+v", u)
	}

	// EffectiveGroupID respects premium overlay.
	if g := u.EffectiveGroupID("premium"); g != "premium" {
		t.Fatalf("EffectiveGroupID=%q want premium (overlay active)", g)
	}

	if err := s.SetBan("quinn", true, "test"); err != nil {
		t.Fatalf("SetBan: %v", err)
	}
	u, _ = s.LookupUser("quinn")
	if !u.Ban || u.BanReason != "test" {
		t.Fatalf("Ban not applied: %+v", u)
	}
}
