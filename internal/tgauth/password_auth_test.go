package tgauth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPasswordAuthStore_SetAndCheck(t *testing.T) {
	dir := t.TempDir()
	store := NewPasswordAuthStore(dir)

	if store.IsConfigured() {
		t.Fatal("store should not be configured initially")
	}

	if err := store.SetPasswordFromPlaintext("my-secret-password"); err != nil {
		t.Fatalf("SetPasswordFromPlaintext: %v", err)
	}

	if !store.IsConfigured() {
		t.Fatal("store should be configured after setting password")
	}

	if !store.CheckPassword("my-secret-password") {
		t.Error("correct password should validate")
	}

	if store.CheckPassword("wrong-password") {
		t.Error("wrong password should not validate")
	}

	// Verify persistence.
	store2 := NewPasswordAuthStore(dir)
	if !store2.IsConfigured() {
		t.Fatal("store should load from disk")
	}
	if !store2.CheckPassword("my-secret-password") {
		t.Error("password should persist across store reload")
	}
}

func TestPasswordAuthStore_TOTP(t *testing.T) {
	dir := t.TempDir()
	store := NewPasswordAuthStore(dir)
	_ = store.SetPasswordFromPlaintext("password123")

	if store.IsTOTPEnabled() {
		t.Fatal("TOTP should not be enabled initially")
	}
	if !store.IsTOTPRequired() {
		t.Fatal("TOTP should be required after setting password")
	}

	secret, uri, err := store.GenerateTOTP()
	if err != nil {
		t.Fatalf("GenerateTOTP: %v", err)
	}
	if secret == "" {
		t.Error("secret should not be empty")
	}
	if uri == "" {
		t.Error("provisioning URI should not be empty")
	}
	if !contains(uri, "otpauth://") {
		t.Errorf("URI should be otpauth://, got %s", uri)
	}

	// Enabling with wrong code should fail.
	if err := store.EnableTOTP("000000"); err == nil {
		t.Error("EnableTOTP with wrong code should fail")
	}

	if store.IsTOTPEnabled() {
		t.Fatal("TOTP should not be enabled after failed code")
	}
}

func TestPasswordAuthStore_FilePermissions(t *testing.T) {
	dir := t.TempDir()
	store := NewPasswordAuthStore(dir)
	_ = store.SetPasswordFromPlaintext("test")

	filePath := filepath.Join(dir, "database", "tgauth", "admin_auth.json")
	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("file permissions = %o, want 0600", perm)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsInner(s, sub))
}

func containsInner(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
