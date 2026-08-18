package ytauth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"lampac-go/internal/kit"
)

// UserToken holds per-user YouTube OAuth tokens.
type UserToken struct {
	TelegramID   int64     `json:"telegram_id"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	ChannelTitle string    `json:"channel_title,omitempty"`
}

// Store persists YouTube tokens per Telegram user.
//
// Access and refresh tokens are Google user credentials, so they are encrypted
// at rest with AES-256-GCM (kit.FieldCrypto) and the file is written 0600 in a
// 0700 directory. The key lives beside the data in .tokenkey (also 0600).
// Google's OAuth verification requires the privacy policy to name a concrete
// data-protection mechanism for sensitive data — this is that mechanism, so
// /privacy and the policy page must keep describing what actually happens here.
type Store struct {
	mu       sync.RWMutex
	tokens   map[int64]*UserToken
	filePath string

	// crypto is nil only if key setup failed (unwritable disk). The store then
	// degrades to the pre-encryption plaintext behaviour rather than locking
	// every user out of YouTube; Encrypted() reports which mode is live.
	crypto *kit.FieldCrypto
}

// NewStore loads tokens from {dbDir}/database/ytauth/tokens.json, decrypting
// them and transparently migrating any legacy plaintext file to encrypted.
func NewStore(dbDir string) *Store {
	dir := filepath.Join(dbDir, "database", "ytauth")
	_ = os.MkdirAll(dir, 0o700)
	// Tighten a directory created by an older build with 0755.
	_ = os.Chmod(dir, 0o700)
	fp := filepath.Join(dir, "tokens.json")

	s := &Store{
		tokens:   make(map[int64]*UserToken),
		filePath: fp,
	}
	if key, err := kit.LoadOrCreateKeyFile(dir, ".tokenkey"); err == nil {
		if fc, err := kit.NewFieldCrypto(key); err == nil {
			s.crypto = fc
		}
	}
	s.load()
	return s
}

// Encrypted reports whether tokens are encrypted at rest.
func (s *Store) Encrypted() bool { return s != nil && s.crypto != nil }

func (s *Store) load() {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return
	}
	var list []*UserToken
	if json.Unmarshal(data, &list) != nil {
		return
	}
	// A token that arrives already in plaintext comes from a pre-encryption
	// file; decrypting is a no-op for it, so re-save once to migrate.
	needsMigration := false
	for _, t := range list {
		if s.crypto != nil {
			access, errA := s.crypto.Decrypt(t.AccessToken)
			refresh, errR := s.crypto.Decrypt(t.RefreshToken)
			if errA != nil || errR != nil {
				// Undecryptable (lost/rotated key) — drop rather than keep a
				// broken entry; the user re-links via /youtube_auth.
				continue
			}
			if access == t.AccessToken && refresh == t.RefreshToken {
				needsMigration = true
			}
			t.AccessToken, t.RefreshToken = access, refresh
		}
		s.tokens[t.TelegramID] = t
	}
	if needsMigration && s.crypto != nil {
		_ = s.save()
	}
}

func (s *Store) save() error {
	list := make([]*UserToken, 0, len(s.tokens))
	for _, t := range s.tokens {
		out := *t // copy: the in-memory map keeps plaintext
		if s.crypto != nil {
			access, err := s.crypto.Encrypt(out.AccessToken)
			if err != nil {
				return err
			}
			refresh, err := s.crypto.Encrypt(out.RefreshToken)
			if err != nil {
				return err
			}
			out.AccessToken, out.RefreshToken = access, refresh
		}
		list = append(list, &out)
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.filePath, data, 0o600); err != nil {
		return err
	}
	// Tighten a file created by an older build with 0644.
	return os.Chmod(s.filePath, 0o600)
}

// Get returns the token for the given Telegram user, or nil.
func (s *Store) Get(tgID int64) *UserToken {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tokens[tgID]
}

// Put inserts or updates a user token and persists to disk.
func (s *Store) Put(t UserToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[t.TelegramID] = &t
	return s.save()
}

// Delete removes a user token and persists. Called when a user unbinds their
// YouTube account — the credential is erased from disk, not just forgotten.
func (s *Store) Delete(tgID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, tgID)
	return s.save()
}
