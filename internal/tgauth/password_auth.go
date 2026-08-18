package tgauth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
)

// PasswordAuthData is persisted to admin_auth.json.
type PasswordAuthData struct {
	PasswordHash string `json:"password_hash"` // bcrypt hash
	TOTPSecret   string `json:"totp_secret"`   // base32-encoded TOTP secret
	TOTPEnabled  bool   `json:"totp_enabled"`  // true after first successful TOTP verification
}

// PasswordAuthStore manages password-based admin authentication with TOTP 2FA.
// Stored in database/tgauth/admin_auth.json.
type PasswordAuthStore struct {
	mu       sync.RWMutex
	data     PasswordAuthData
	filePath string
}

// NewPasswordAuthStore loads or creates a PasswordAuthStore.
func NewPasswordAuthStore(repoRoot string) *PasswordAuthStore {
	s := &PasswordAuthStore{
		filePath: filepath.Join(repoRoot, "database", "tgauth", "admin_auth.json"),
	}
	_ = s.load()
	return s
}

// IsConfigured returns true if a password hash exists.
func (s *PasswordAuthStore) IsConfigured() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.PasswordHash != ""
}

// SetPasswordFromPlaintext hashes a plaintext password and saves it.
// Used on first boot when the installer provides a plaintext password in config.
func (s *PasswordAuthStore) SetPasswordFromPlaintext(plaintext string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("password_auth: bcrypt hash: %w", err)
	}
	s.mu.Lock()
	s.data.PasswordHash = string(hash)
	s.mu.Unlock()
	return s.save()
}

// CheckPassword verifies a plaintext password against the stored bcrypt hash.
func (s *PasswordAuthStore) CheckPassword(plaintext string) bool {
	s.mu.RLock()
	hash := s.data.PasswordHash
	s.mu.RUnlock()
	if hash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext)) == nil
}

// IsTOTPEnabled returns true if TOTP 2FA has been set up and verified.
func (s *PasswordAuthStore) IsTOTPEnabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.TOTPEnabled
}

// IsTOTPRequired returns true if password is set but TOTP is not yet configured.
// This means the user must complete 2FA setup on their next login.
func (s *PasswordAuthStore) IsTOTPRequired() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.PasswordHash != "" && !s.data.TOTPEnabled
}

// GenerateTOTP creates a new TOTP secret and returns the base32 secret
// and the provisioning URI (otpauth://...) for QR code generation.
// The secret is stored but NOT enabled until EnableTOTP is called.
func (s *PasswordAuthStore) GenerateTOTP() (secret string, provisioningURI string, err error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      "Alpac",
		AccountName: "admin",
	})
	if err != nil {
		return "", "", fmt.Errorf("password_auth: generate TOTP: %w", err)
	}

	s.mu.Lock()
	s.data.TOTPSecret = key.Secret()
	s.data.TOTPEnabled = false
	s.mu.Unlock()

	if err := s.save(); err != nil {
		return "", "", err
	}

	return key.Secret(), key.URL(), nil
}

// EnableTOTP verifies the provided TOTP code and enables 2FA if correct.
// Must be called after GenerateTOTP to complete the setup.
func (s *PasswordAuthStore) EnableTOTP(code string) error {
	s.mu.RLock()
	secret := s.data.TOTPSecret
	s.mu.RUnlock()

	if secret == "" {
		return fmt.Errorf("password_auth: no TOTP secret generated")
	}

	if !totp.Validate(code, secret) {
		return fmt.Errorf("password_auth: invalid TOTP code")
	}

	s.mu.Lock()
	s.data.TOTPEnabled = true
	s.mu.Unlock()

	return s.save()
}

// VerifyTOTP validates a 6-digit TOTP code.
func (s *PasswordAuthStore) VerifyTOTP(code string) bool {
	s.mu.RLock()
	secret := s.data.TOTPSecret
	enabled := s.data.TOTPEnabled
	s.mu.RUnlock()

	if secret == "" || !enabled {
		return false
	}

	return totp.Validate(code, secret)
}

// ChangePassword updates the password hash. Requires the old password for verification.
func (s *PasswordAuthStore) ChangePassword(oldPlaintext, newPlaintext string) error {
	if !s.CheckPassword(oldPlaintext) {
		return fmt.Errorf("password_auth: old password incorrect")
	}
	return s.SetPasswordFromPlaintext(newPlaintext)
}

func (s *PasswordAuthStore) load() error {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return err // file may not exist yet, that's ok
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return json.Unmarshal(data, &s.data)
}

func (s *PasswordAuthStore) save() error {
	s.mu.RLock()
	data, err := json.MarshalIndent(s.data, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(s.filePath, data, 0600)
}
