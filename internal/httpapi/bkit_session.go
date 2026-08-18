package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// BKitSession represents a browser Kit access session created by the admin.
type BKitSession struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Token     string    `json:"token"`
	CreatedAt time.Time `json:"created_at"`
}

// BKitSessionStore persists browser Kit sessions to a JSON file.
type BKitSessionStore struct {
	mu       sync.RWMutex
	sessions []BKitSession
	filePath string
}

// NewBKitSessionStore creates a store backed by database/kit/browser_sessions.json.
func NewBKitSessionStore(repoRoot string) *BKitSessionStore {
	dir := filepath.Join(repoRoot, "database", "kit")
	_ = os.MkdirAll(dir, 0755)
	s := &BKitSessionStore{
		filePath: filepath.Join(dir, "browser_sessions.json"),
	}
	_ = s.load()
	return s
}

// Create generates a new browser Kit session with a random token.
func (s *BKitSessionStore) Create(name string) BKitSession {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess := BKitSession{
		ID:        randomHex(8),
		Name:      name,
		Token:     randomHex(32),
		CreatedAt: time.Now().UTC(),
	}
	s.sessions = append(s.sessions, sess)
	_ = s.saveLocked()
	return sess
}

// Lookup finds a session by its token.
func (s *BKitSessionStore) Lookup(token string) (*BKitSession, bool) {
	if token == "" {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.sessions {
		if s.sessions[i].Token == token {
			sess := s.sessions[i]
			return &sess, true
		}
	}
	return nil, false
}

// List returns all sessions.
func (s *BKitSessionStore) List() []BKitSession {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]BKitSession, len(s.sessions))
	copy(out, s.sessions)
	return out
}

// Delete removes a session by ID.
func (s *BKitSessionStore) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.sessions {
		if s.sessions[i].ID == id {
			s.sessions = append(s.sessions[:i], s.sessions[i+1:]...)
			_ = s.saveLocked()
			return true
		}
	}
	return false
}

func (s *BKitSessionStore) load() error {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &s.sessions)
}

func (s *BKitSessionStore) saveLocked() error {
	data, err := json.MarshalIndent(s.sessions, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.filePath, data, 0644)
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
