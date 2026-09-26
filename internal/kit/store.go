package kit

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// Store persists per-user kit configurations as JSON files.
// Storage layout: {repoRoot}/database/kit/{md5(telegramID)}
//
// When a FieldCrypto is configured, sensitive fields (token, cookie) are
// encrypted at rest using AES-256-GCM. In-memory cache and context always
// hold decrypted values so balancer handlers work transparently.
type Store struct {
	dir    string
	cache  sync.Map // tgID → *cacheEntry
	ttl    time.Duration
	crypto *FieldCrypto // nil = no encryption (backward compat)
}

type cacheEntry struct {
	data     map[string]json.RawMessage
	loadedAt time.Time
}

// NewStore creates a kit store and ensures the directory exists.
// If encryption is enabled, the AES-256 key is loaded (or auto-generated)
// from {dir}/.kitkey.
func NewStore(repoRoot string, cacheTTL time.Duration, encrypt bool) *Store {
	dir := filepath.Join(repoRoot, "database", "kit")
	_ = os.MkdirAll(dir, 0755)
	if cacheTTL <= 0 {
		cacheTTL = 60 * time.Second
	}

	s := &Store{dir: dir, ttl: cacheTTL}

	if encrypt {
		key, err := LoadOrCreateKey(dir)
		if err != nil {
			log.Error().Err(err).Msg("kit: failed to load encryption key — data will NOT be encrypted")
		} else {
			fc, err := NewFieldCrypto(key)
			if err != nil {
				log.Error().Err(err).Msg("kit: failed to init AES-GCM — data will NOT be encrypted")
			} else {
				s.crypto = fc
				log.Info().Msg("kit: field-level encryption enabled (AES-256-GCM)")
			}
		}
	}

	return s
}

// Load reads the user's kit config from disk (with in-memory cache).
// Returns nil map and nil error if no config exists.
// Returned map always contains decrypted values.
func (s *Store) Load(tgID string) (map[string]json.RawMessage, error) {
	tgID = normID(tgID)
	if tgID == "" {
		return nil, nil
	}

	// Check cache first (cache holds decrypted data).
	if v, ok := s.cache.Load(tgID); ok {
		ce := v.(*cacheEntry)
		if time.Since(ce.loadedAt) < s.ttl {
			return ce.data, nil
		}
		s.cache.Delete(tgID)
	}

	path := s.filePath(tgID)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("kit: read %s: %w", path, err)
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("kit: parse %s: %w", path, err)
	}

	// Decrypt sensitive fields if encryption is enabled.
	if s.crypto != nil {
		decrypted, err := s.crypto.DecryptSections(m)
		if err != nil {
			return nil, fmt.Errorf("kit: decrypt %s: %w", path, err)
		}
		m = decrypted
	}

	s.cache.Store(tgID, &cacheEntry{data: m, loadedAt: time.Now()})
	return m, nil
}

// Save writes raw JSON to the user's kit config file.
// Validates that raw is valid JSON object. Sensitive fields are encrypted
// before writing to disk (if encryption is enabled).
func (s *Store) Save(tgID string, raw []byte) error {
	tgID = normID(tgID)
	if tgID == "" {
		return fmt.Errorf("kit: empty user ID")
	}

	// Validate JSON is an object.
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("kit: invalid JSON: %w", err)
	}

	// Рядом с выбором видимости кладём снимок каталога: без него каждый
	// источник, добавленный ПОСЛЕ этого сохранения, навсегда прятался бы
	// белым списком (см. kit/catalog.go).
	stampCatalog(m)

	// Write encrypted data to disk.
	diskData := m
	if s.crypto != nil {
		encrypted, err := s.crypto.EncryptSections(m)
		if err != nil {
			return fmt.Errorf("kit: encrypt: %w", err)
		}
		diskData = encrypted
	}

	encoded, err := json.MarshalIndent(diskData, "", "  ")
	if err != nil {
		return fmt.Errorf("kit: marshal: %w", err)
	}

	path := s.filePath(tgID)
	if err := os.WriteFile(path, encoded, 0644); err != nil {
		return fmt.Errorf("kit: write %s: %w", path, err)
	}

	// Cache holds decrypted data.
	s.cache.Store(tgID, &cacheEntry{data: m, loadedAt: time.Now()})
	return nil
}

// UpdateSection loads existing config, updates one section, and saves.
func (s *Store) UpdateSection(tgID, section string, value any) error {
	tgID = normID(tgID)
	if tgID == "" {
		return fmt.Errorf("kit: empty user ID")
	}

	m, err := s.Load(tgID)
	if err != nil {
		return err
	}
	if m == nil {
		m = make(map[string]json.RawMessage)
	}

	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("kit: marshal section %s: %w", section, err)
	}
	m[section] = raw

	full, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("kit: marshal full config: %w", err)
	}

	return s.Save(tgID, full)
}

// DeleteSection removes a section from the user's config.
func (s *Store) DeleteSection(tgID, section string) error {
	tgID = normID(tgID)
	m, err := s.Load(tgID)
	if err != nil {
		return err
	}
	if m == nil {
		return nil
	}
	delete(m, section)

	full, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return s.Save(tgID, full)
}

// Invalidate removes a user from the cache (e.g. after external update).
func (s *Store) Invalidate(tgID string) {
	s.cache.Delete(normID(tgID))
}

func (s *Store) filePath(tgID string) string {
	return filepath.Join(s.dir, userHash(tgID))
}

// userHash returns the lowercase MD5 hex of the normalized user ID.
func userHash(tgID string) string {
	h := md5.Sum([]byte(strings.ToLower(strings.TrimSpace(tgID))))
	return hex.EncodeToString(h[:])
}

func normID(tgID string) string {
	return strings.TrimSpace(tgID)
}
