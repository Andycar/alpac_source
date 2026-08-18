package ytauth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	testAccess  = "ya29.PLAINTEXT-ACCESS-TOKEN-SHOULD-NEVER-HIT-DISK"
	testRefresh = "1//PLAINTEXT-REFRESH-TOKEN-SHOULD-NEVER-HIT-DISK"
)

func tokensPath(dbDir string) string {
	return filepath.Join(dbDir, "database", "ytauth", "tokens.json")
}

func readTokensFile(t *testing.T, dbDir string) string {
	t.Helper()
	data, err := os.ReadFile(tokensPath(dbDir))
	if err != nil {
		t.Fatalf("read tokens.json: %v", err)
	}
	return string(data)
}

// Tokens must survive a process restart and must never sit on disk in the clear.
func TestStoreEncryptsAtRestAndRoundTrips(t *testing.T) {
	dbDir := t.TempDir()

	s := NewStore(dbDir)
	if !s.Encrypted() {
		t.Fatal("expected encryption to be active on a writable dir")
	}
	if err := s.Put(UserToken{
		TelegramID:   42,
		AccessToken:  testAccess,
		RefreshToken: testRefresh,
		ExpiresAt:    time.Now().Add(time.Hour),
		ChannelTitle: "Test Channel",
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	onDisk := readTokensFile(t, dbDir)
	if strings.Contains(onDisk, testAccess) || strings.Contains(onDisk, testRefresh) {
		t.Fatal("plaintext credential found in tokens.json")
	}
	if !strings.Contains(onDisk, "enc:") {
		t.Fatal("expected enc: prefixed ciphertext on disk")
	}

	// A fresh store (process restart) must decrypt back to the original.
	reloaded := NewStore(dbDir).Get(42)
	if reloaded == nil {
		t.Fatal("token missing after reload")
	}
	if reloaded.AccessToken != testAccess || reloaded.RefreshToken != testRefresh {
		t.Fatalf("round-trip mismatch: got access=%q refresh=%q", reloaded.AccessToken, reloaded.RefreshToken)
	}
	if reloaded.ChannelTitle != "Test Channel" {
		t.Fatalf("channel title lost: %q", reloaded.ChannelTitle)
	}
}

// The 91 live bindings are plaintext today: loading them must keep them working
// and rewrite the file encrypted, without a re-auth.
func TestStoreMigratesLegacyPlaintextFile(t *testing.T) {
	dbDir := t.TempDir()
	dir := filepath.Join(dbDir, "database", "ytauth")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	legacy := `[{"telegram_id":7,"access_token":"` + testAccess +
		`","refresh_token":"` + testRefresh + `","expires_at":"2030-01-01T00:00:00Z","channel_title":"Legacy"}]`
	if err := os.WriteFile(tokensPath(dbDir), []byte(legacy), 0o644); err != nil {
		t.Fatalf("write legacy file: %v", err)
	}

	got := NewStore(dbDir).Get(7)
	if got == nil {
		t.Fatal("legacy token lost during migration")
	}
	if got.AccessToken != testAccess || got.RefreshToken != testRefresh {
		t.Fatal("legacy token corrupted during migration")
	}

	onDisk := readTokensFile(t, dbDir)
	if strings.Contains(onDisk, testAccess) || strings.Contains(onDisk, testRefresh) {
		t.Fatal("migration left plaintext credentials on disk")
	}
}

// Credentials must not be world-readable, and an older 0644 file must be fixed.
func TestStoreTightensPermissions(t *testing.T) {
	dbDir := t.TempDir()
	dir := filepath.Join(dbDir, "database", "ytauth")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(tokensPath(dbDir), []byte(`[]`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	s := NewStore(dbDir)
	if err := s.Put(UserToken{TelegramID: 1, AccessToken: "a", RefreshToken: "r"}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	fi, err := os.Stat(tokensPath(dbDir))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("tokens.json mode = %04o, want 0600", perm)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Fatalf("ytauth dir mode = %04o, want 0700", perm)
	}
	ki, err := os.Stat(filepath.Join(dir, ".tokenkey"))
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if perm := ki.Mode().Perm(); perm != 0o600 {
		t.Fatalf(".tokenkey mode = %04o, want 0600", perm)
	}
}

// Deleting a binding must erase the credential from disk, not just from memory.
func TestDeleteRemovesCredentialFromDisk(t *testing.T) {
	dbDir := t.TempDir()
	s := NewStore(dbDir)
	if err := s.Put(UserToken{TelegramID: 5, AccessToken: testAccess, RefreshToken: testRefresh}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Delete(5); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if NewStore(dbDir).Get(5) != nil {
		t.Fatal("token still present after Delete")
	}
	if strings.TrimSpace(readTokensFile(t, dbDir)) != "[]" {
		t.Fatalf("expected empty store on disk, got %q", readTokensFile(t, dbDir))
	}
}
