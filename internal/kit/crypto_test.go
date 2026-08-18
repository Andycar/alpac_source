package kit

import (
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return key
}

func TestFieldCrypto_EncryptDecrypt(t *testing.T) {
	fc, err := NewFieldCrypto(testKey(t))
	if err != nil {
		t.Fatal(err)
	}

	tests := []string{
		"simple_token_123",
		"dle_user_id=12345; dle_password=abc",
		"", // empty should round-trip
		"a",
		strings.Repeat("x", 10000),
	}

	for _, plaintext := range tests {
		encrypted, err := fc.Encrypt(plaintext)
		if err != nil {
			t.Fatalf("Encrypt(%q) error: %v", plaintext, err)
		}
		if plaintext == "" && encrypted != "" {
			t.Fatal("empty string should encrypt to empty")
		}
		if plaintext != "" && !strings.HasPrefix(encrypted, encPrefix) {
			t.Fatalf("encrypted should start with %q, got %q", encPrefix, encrypted[:10])
		}

		decrypted, err := fc.Decrypt(encrypted)
		if err != nil {
			t.Fatalf("Decrypt error: %v", err)
		}
		if decrypted != plaintext {
			t.Fatalf("round-trip failed: got %q, want %q", decrypted, plaintext)
		}
	}
}

func TestFieldCrypto_DecryptPlaintext(t *testing.T) {
	fc, err := NewFieldCrypto(testKey(t))
	if err != nil {
		t.Fatal(err)
	}

	// Plaintext values (no "enc:" prefix) should pass through as-is.
	plain := "just_a_token_abc123"
	result, err := fc.Decrypt(plain)
	if err != nil {
		t.Fatal(err)
	}
	if result != plain {
		t.Fatalf("plaintext passthrough failed: got %q, want %q", result, plain)
	}
}

func TestFieldCrypto_UniqueNonces(t *testing.T) {
	fc, err := NewFieldCrypto(testKey(t))
	if err != nil {
		t.Fatal(err)
	}

	// Same plaintext should produce different ciphertext due to random nonce.
	enc1, _ := fc.Encrypt("same_token")
	enc2, _ := fc.Encrypt("same_token")
	if enc1 == enc2 {
		t.Fatal("two encryptions of same plaintext should differ")
	}
}

func TestFieldCrypto_WrongKey(t *testing.T) {
	fc1, _ := NewFieldCrypto(testKey(t))
	fc2, _ := NewFieldCrypto(testKey(t))

	encrypted, _ := fc1.Encrypt("secret")
	_, err := fc2.Decrypt(encrypted)
	if err == nil {
		t.Fatal("decrypt with wrong key should fail")
	}
}

func TestEncryptDecryptSections(t *testing.T) {
	fc, err := NewFieldCrypto(testKey(t))
	if err != nil {
		t.Fatal(err)
	}

	original := map[string]json.RawMessage{
		"Filmix":              json.RawMessage(`{"enable":true,"token":"filmix_secret_123","pro":true}`),
		"Rezka":               json.RawMessage(`{"enable":true,"cookie":"dle_user_id=1; dle_password=abc"}`),
		"_balancerVisibility": json.RawMessage(`{"filmix":true,"rezka":false}`),
		"NoSensitive":         json.RawMessage(`{"enable":true}`),
	}

	encrypted, err := fc.EncryptSections(original)
	if err != nil {
		t.Fatal(err)
	}

	// Verify sensitive fields are encrypted.
	var filmixEnc map[string]any
	json.Unmarshal(encrypted["Filmix"], &filmixEnc)
	tokenEnc, _ := filmixEnc["token"].(string)
	if !strings.HasPrefix(tokenEnc, encPrefix) {
		t.Fatalf("Filmix.token should be encrypted, got %q", tokenEnc)
	}
	// "enable" and "pro" should remain plaintext.
	if enable, _ := filmixEnc["enable"].(bool); !enable {
		t.Fatal("Filmix.enable should remain true")
	}

	var rezkaEnc map[string]any
	json.Unmarshal(encrypted["Rezka"], &rezkaEnc)
	cookieEnc, _ := rezkaEnc["cookie"].(string)
	if !strings.HasPrefix(cookieEnc, encPrefix) {
		t.Fatalf("Rezka.cookie should be encrypted, got %q", cookieEnc)
	}

	// _balancerVisibility should be untouched.
	if string(encrypted["_balancerVisibility"]) != string(original["_balancerVisibility"]) {
		t.Fatal("_balancerVisibility should not be modified")
	}

	// Decrypt back and verify round-trip.
	decrypted, err := fc.DecryptSections(encrypted)
	if err != nil {
		t.Fatal(err)
	}

	var filmixDec map[string]any
	json.Unmarshal(decrypted["Filmix"], &filmixDec)
	if filmixDec["token"] != "filmix_secret_123" {
		t.Fatalf("Filmix.token round-trip failed: got %v", filmixDec["token"])
	}

	var rezkaDec map[string]any
	json.Unmarshal(decrypted["Rezka"], &rezkaDec)
	if rezkaDec["cookie"] != "dle_user_id=1; dle_password=abc" {
		t.Fatalf("Rezka.cookie round-trip failed: got %v", rezkaDec["cookie"])
	}
}

func TestLoadOrCreateKey(t *testing.T) {
	dir := t.TempDir()

	// First call should create a new key.
	key1, err := LoadOrCreateKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(key1) != 32 {
		t.Fatalf("key should be 32 bytes, got %d", len(key1))
	}

	// Verify key file permissions.
	path := filepath.Join(dir, ".kitkey")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("key file should be 0600, got %o", perm)
	}

	// Second call should load the same key.
	key2, err := LoadOrCreateKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(key1) != string(key2) {
		t.Fatal("loaded key should match created key")
	}
}

func TestStoreEncryptedRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir, 0, true) // encryption enabled

	tgID := "123456"
	config := []byte(`{
		"Filmix": {"enable": true, "token": "my_secret_token", "pro": false},
		"Rezka": {"enable": true, "cookie": "dle_user_id=99; dle_password=xyz"}
	}`)

	if err := store.Save(tgID, config); err != nil {
		t.Fatal(err)
	}

	// Read raw file — sensitive fields should be encrypted.
	rawPath := filepath.Join(dir, "database", "kit", userHash(tgID))
	rawData, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatal(err)
	}

	rawStr := string(rawData)
	if strings.Contains(rawStr, "my_secret_token") {
		t.Fatal("raw file should NOT contain plaintext token")
	}
	if strings.Contains(rawStr, "dle_password=xyz") {
		t.Fatal("raw file should NOT contain plaintext cookie")
	}
	if !strings.Contains(rawStr, encPrefix) {
		t.Fatal("raw file should contain encrypted markers")
	}

	// Load should return decrypted data.
	loaded, err := store.Load(tgID)
	if err != nil {
		t.Fatal(err)
	}

	var filmix map[string]any
	json.Unmarshal(loaded["Filmix"], &filmix)
	if filmix["token"] != "my_secret_token" {
		t.Fatalf("loaded Filmix.token should be decrypted, got %v", filmix["token"])
	}

	var rezka map[string]any
	json.Unmarshal(loaded["Rezka"], &rezka)
	if rezka["cookie"] != "dle_user_id=99; dle_password=xyz" {
		t.Fatalf("loaded Rezka.cookie should be decrypted, got %v", rezka["cookie"])
	}
}

func TestStoreNoEncryption(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir, 0, false) // encryption disabled

	tgID := "789"
	config := []byte(`{"Filmix": {"enable": true, "token": "plain_token"}}`)

	if err := store.Save(tgID, config); err != nil {
		t.Fatal(err)
	}

	// Raw file should contain plaintext.
	rawPath := filepath.Join(dir, "database", "kit", userHash(tgID))
	rawData, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(rawData), "plain_token") {
		t.Fatal("without encryption, raw file should contain plaintext token")
	}
}

func TestStoreUpdateSectionEncrypted(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir, 0, true)

	tgID := "456"

	// UpdateSection should encrypt.
	section := map[string]any{
		"enable": true,
		"token":  "kinopub_access_token_123",
	}
	if err := store.UpdateSection(tgID, "KinoPub", section); err != nil {
		t.Fatal(err)
	}

	// Verify raw file is encrypted.
	rawPath := filepath.Join(dir, "database", "kit", userHash(tgID))
	rawData, _ := os.ReadFile(rawPath)
	if strings.Contains(string(rawData), "kinopub_access_token_123") {
		t.Fatal("UpdateSection should encrypt sensitive fields on disk")
	}

	// Load should decrypt.
	loaded, err := store.Load(tgID)
	if err != nil {
		t.Fatal(err)
	}
	var kp map[string]any
	json.Unmarshal(loaded["KinoPub"], &kp)
	if kp["token"] != "kinopub_access_token_123" {
		t.Fatalf("UpdateSection round-trip failed: got %v", kp["token"])
	}
}

func TestBackwardCompatUnencrypted(t *testing.T) {
	dir := t.TempDir()

	// Write a plaintext file (simulating pre-encryption data).
	kitDir := filepath.Join(dir, "database", "kit")
	os.MkdirAll(kitDir, 0755)
	tgID := "old_user"
	path := filepath.Join(kitDir, userHash(tgID))
	plainData := `{"Filmix":{"enable":true,"token":"old_plain_token"}}`
	os.WriteFile(path, []byte(plainData), 0644)

	// Create store with encryption enabled.
	store := NewStore(dir, 0, true)

	// Load should work fine with plaintext data.
	loaded, err := store.Load(tgID)
	if err != nil {
		t.Fatal(err)
	}
	var filmix map[string]any
	json.Unmarshal(loaded["Filmix"], &filmix)
	if filmix["token"] != "old_plain_token" {
		t.Fatalf("backward compat failed: got %v", filmix["token"])
	}

	// Save will re-encrypt.
	full, _ := json.Marshal(loaded)
	if err := store.Save(tgID, full); err != nil {
		t.Fatal(err)
	}

	// Verify data is now encrypted on disk.
	rawData, _ := os.ReadFile(path)
	if strings.Contains(string(rawData), "old_plain_token") {
		t.Fatal("after save, data should be encrypted on disk")
	}
}
