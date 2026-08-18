package tgauth

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

// WebAuthnCred stores a single WebAuthn credential (passkey).
type WebAuthnCred struct {
	CredentialID    []byte    `json:"credential_id"`
	PublicKey       []byte    `json:"public_key"`
	AttestationType string    `json:"attestation_type"`
	SignCount       uint32    `json:"sign_count"`
	AAGUID          []byte    `json:"aaguid"`
	DisplayName     string    `json:"display_name"`
	CreatedAt       time.Time `json:"created_at"`
	LastUsedAt      time.Time `json:"last_used_at"`
	BackupEligible  bool      `json:"backup_eligible"`
	BackupState     bool      `json:"backup_state"`
}

// ToLibCredential converts to the go-webauthn library Credential type.
func (c *WebAuthnCred) ToLibCredential() webauthn.Credential {
	return webauthn.Credential{
		ID:              c.CredentialID,
		PublicKey:       c.PublicKey,
		AttestationType: c.AttestationType,
		Flags: webauthn.CredentialFlags{
			BackupEligible: c.BackupEligible,
			BackupState:    c.BackupState,
		},
		Authenticator: webauthn.Authenticator{
			AAGUID:    c.AAGUID,
			SignCount: c.SignCount,
		},
	}
}

// WebAuthnStore manages passkey credentials on disk.
// File: database/tgauth/webauthn_credentials.json, chmod 0600.
type WebAuthnStore struct {
	mu          sync.RWMutex
	credentials []WebAuthnCred
	filePath    string
}

// NewWebAuthnStore creates or loads the WebAuthn credential store.
func NewWebAuthnStore(repoRoot string) *WebAuthnStore {
	s := &WebAuthnStore{
		filePath: filepath.Join(repoRoot, "database", "tgauth", "webauthn_credentials.json"),
	}
	_ = s.load()
	return s
}

// HasCredentials returns true if at least one passkey is registered.
func (s *WebAuthnStore) HasCredentials() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.credentials) > 0
}

// AddCredential persists a new credential after registration.
func (s *WebAuthnStore) AddCredential(cred WebAuthnCred) error {
	s.mu.Lock()
	cred.CreatedAt = time.Now()
	cred.LastUsedAt = cred.CreatedAt
	s.credentials = append(s.credentials, cred)
	s.mu.Unlock()
	return s.save()
}

// RemoveCredential removes a credential by its ID.
func (s *WebAuthnStore) RemoveCredential(credID []byte) error {
	s.mu.Lock()
	for i, c := range s.credentials {
		if bytes.Equal(c.CredentialID, credID) {
			s.credentials = append(s.credentials[:i], s.credentials[i+1:]...)
			s.mu.Unlock()
			return s.save()
		}
	}
	s.mu.Unlock()
	return nil
}

// ListCredentials returns a copy of all registered credentials (for UI display).
func (s *WebAuthnStore) ListCredentials() []WebAuthnCred {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]WebAuthnCred, len(s.credentials))
	copy(out, s.credentials)
	return out
}

// FindCredential looks up a credential by its ID.
func (s *WebAuthnStore) FindCredential(credID []byte) *WebAuthnCred {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.credentials {
		if bytes.Equal(s.credentials[i].CredentialID, credID) {
			cpy := s.credentials[i]
			return &cpy
		}
	}
	return nil
}

// UpdateSignCount updates the signature counter and last-used timestamp.
func (s *WebAuthnStore) UpdateSignCount(credID []byte, count uint32) error {
	s.mu.Lock()
	for i := range s.credentials {
		if bytes.Equal(s.credentials[i].CredentialID, credID) {
			s.credentials[i].SignCount = count
			s.credentials[i].LastUsedAt = time.Now()
			s.mu.Unlock()
			return s.save()
		}
	}
	s.mu.Unlock()
	return nil
}

// WebAuthnCredentials returns credentials in the go-webauthn library format.
// Implements the data source for AdminWebAuthnUser.WebAuthnCredentials().
func (s *WebAuthnStore) WebAuthnCredentials() []webauthn.Credential {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]webauthn.Credential, len(s.credentials))
	for i, c := range s.credentials {
		out[i] = c.ToLibCredential()
	}
	return out
}

// --- persistence ---

func (s *WebAuthnStore) load() error {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return json.Unmarshal(data, &s.credentials)
}

func (s *WebAuthnStore) save() error {
	s.mu.RLock()
	data, err := json.MarshalIndent(s.credentials, "", "  ")
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
