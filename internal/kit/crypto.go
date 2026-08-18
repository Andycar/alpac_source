package kit

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	// encPrefix marks encrypted field values on disk: "enc:<base64(nonce+ciphertext)>".
	encPrefix = "enc:"

	// sensitiveFields are JSON keys inside balancer sections whose values
	// must be encrypted at rest. All other fields (enable, pro, etc.) stay plaintext.
	// NOTE: these are leaf keys inside each section object, not top-level keys.
)

// sensitiveFieldSet is a fast lookup for fields that need encryption.
var sensitiveFieldSet = map[string]bool{
	"token":  true,
	"cookie": true,
}

// FieldCrypto provides AES-256-GCM encryption for individual string values.
// Nonce is random and prepended to the ciphertext, so each encryption produces
// a unique output even for the same plaintext.
type FieldCrypto struct {
	gcm cipher.AEAD
}

// NewFieldCrypto creates a FieldCrypto from a 32-byte AES-256 key.
func NewFieldCrypto(key []byte) (*FieldCrypto, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("kit/crypto: key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("kit/crypto: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("kit/crypto: %w", err)
	}
	return &FieldCrypto{gcm: gcm}, nil
}

// Encrypt encrypts plaintext and returns "enc:<base64(nonce+ciphertext)>".
func (fc *FieldCrypto) Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	nonce := make([]byte, fc.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("kit/crypto: generate nonce: %w", err)
	}
	ciphertext := fc.gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return encPrefix + base64.RawURLEncoding.EncodeToString(ciphertext), nil
}

// Decrypt decrypts "enc:<base64(nonce+ciphertext)>" back to plaintext.
// If the value doesn't start with "enc:", returns it as-is (backward compat).
func (fc *FieldCrypto) Decrypt(value string) (string, error) {
	if !strings.HasPrefix(value, encPrefix) {
		return value, nil // plaintext (legacy/unencrypted)
	}
	data, err := base64.RawURLEncoding.DecodeString(value[len(encPrefix):])
	if err != nil {
		return "", fmt.Errorf("kit/crypto: base64 decode: %w", err)
	}
	nonceSize := fc.gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("kit/crypto: ciphertext too short")
	}
	plaintext, err := fc.gcm.Open(nil, data[:nonceSize], data[nonceSize:], nil)
	if err != nil {
		return "", fmt.Errorf("kit/crypto: decrypt: %w", err)
	}
	return string(plaintext), nil
}

// EncryptSections encrypts sensitive fields in each section of a kit config map.
// Returns the encrypted map suitable for writing to disk.
func (fc *FieldCrypto) EncryptSections(m map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	out := make(map[string]json.RawMessage, len(m))
	for k, raw := range m {
		if k == "_balancerVisibility" || raw == nil {
			out[k] = raw
			continue
		}
		encrypted, err := fc.encryptSection(raw)
		if err != nil {
			return nil, fmt.Errorf("section %q: %w", k, err)
		}
		out[k] = encrypted
	}
	return out, nil
}

// DecryptSections decrypts sensitive fields in each section.
// Returns the decrypted map suitable for in-memory use.
func (fc *FieldCrypto) DecryptSections(m map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	out := make(map[string]json.RawMessage, len(m))
	for k, raw := range m {
		if k == "_balancerVisibility" || raw == nil {
			out[k] = raw
			continue
		}
		decrypted, err := fc.decryptSection(raw)
		if err != nil {
			return nil, fmt.Errorf("section %q: %w", k, err)
		}
		out[k] = decrypted
	}
	return out, nil
}

// encryptSection encrypts sensitive string fields inside a JSON object.
func (fc *FieldCrypto) encryptSection(raw json.RawMessage) (json.RawMessage, error) {
	var section map[string]json.RawMessage
	if err := json.Unmarshal(raw, &section); err != nil {
		// Not an object (e.g. scalar) — leave as-is.
		return raw, nil
	}

	changed := false
	for field := range sensitiveFieldSet {
		val, exists := section[field]
		if !exists || len(val) == 0 {
			continue
		}
		// Parse the JSON string value.
		var str string
		if err := json.Unmarshal(val, &str); err != nil {
			continue // not a string — skip
		}
		if str == "" || strings.HasPrefix(str, encPrefix) {
			continue // already encrypted or empty
		}
		encrypted, err := fc.Encrypt(str)
		if err != nil {
			return nil, err
		}
		enc, _ := json.Marshal(encrypted)
		section[field] = enc
		changed = true
	}

	if !changed {
		return raw, nil
	}
	return json.Marshal(section)
}

// decryptSection decrypts sensitive string fields inside a JSON object.
func (fc *FieldCrypto) decryptSection(raw json.RawMessage) (json.RawMessage, error) {
	var section map[string]json.RawMessage
	if err := json.Unmarshal(raw, &section); err != nil {
		return raw, nil
	}

	changed := false
	for field := range sensitiveFieldSet {
		val, exists := section[field]
		if !exists || len(val) == 0 {
			continue
		}
		var str string
		if err := json.Unmarshal(val, &str); err != nil {
			continue
		}
		if !strings.HasPrefix(str, encPrefix) {
			continue // plaintext — nothing to decrypt
		}
		decrypted, err := fc.Decrypt(str)
		if err != nil {
			return nil, err
		}
		dec, _ := json.Marshal(decrypted)
		section[field] = dec
		changed = true
	}

	if !changed {
		return raw, nil
	}
	return json.Marshal(section)
}

// LoadOrCreateKey loads an AES-256 key from {kitDir}/.kitkey or generates one.
// The key file is stored as 64-char hex (32 bytes) with mode 0600.
func LoadOrCreateKey(kitDir string) ([]byte, error) {
	return LoadOrCreateKeyFile(kitDir, ".kitkey")
}

// LoadOrCreateKeyFile is LoadOrCreateKey with an explicit key filename, so
// other at-rest-encrypted stores (e.g. ytauth's OAuth tokens) can keep their
// own key next to their own data instead of sharing kit's.
func LoadOrCreateKeyFile(dir, filename string) ([]byte, error) {
	path := filepath.Join(dir, filename)

	if data, err := os.ReadFile(path); err == nil {
		hexStr := strings.TrimSpace(string(data))
		if len(hexStr) == 64 {
			key, err := hex.DecodeString(hexStr)
			if err == nil && len(key) == 32 {
				return key, nil
			}
		}
		// Corrupt key file — regenerate.
	}

	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("kit/crypto: generate key: %w", err)
	}

	hexStr := hex.EncodeToString(key)
	if err := os.WriteFile(path, []byte(hexStr+"\n"), 0600); err != nil {
		return nil, fmt.Errorf("kit/crypto: write key file: %w", err)
	}

	return key, nil
}
