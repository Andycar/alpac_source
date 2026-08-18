package tgauth

// AuthModeStore is the runtime override for the auth mode declared in
// TOML. The admin v2 panel writes this file when the mode is flipped;
// the file's content takes precedence over config.Auth.Mode so a flip
// survives a config reload but doesn't require editing TOML by hand.
//
// File: database/auth/auth_mode.json
//
//	{"mode":"password","updated_at":"2026-05-27T22:00:00Z"}
//
// Missing file = fall back to whatever TOML says. Empty mode field = same.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Mode constants — duplicated here from config to keep this package
// import-cycle-free. Caller compares to config.AuthMode* directly.
const (
	AuthModeTG       = "tg"
	AuthModePassword = "password"
	AuthModeNone     = "none"
)

// ErrInvalidAuthMode is returned by Set when the input isn't one of
// the recognized mode strings.
var ErrInvalidAuthMode = errors.New("tgauth: invalid auth mode")

type authModeData struct {
	Mode      string    `json:"mode"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AuthModeStore is concurrency-safe and writes atomically.
type AuthModeStore struct {
	mu       sync.RWMutex
	filePath string
	mode     string
	fallback string
}

// NewAuthModeStore opens (or creates) database/auth/auth_mode.json.
// fallback is the TOML-declared mode used when the file is missing or
// empty. An invalid value in the file is ignored and falls back to TOML.
func NewAuthModeStore(dbDir, fallback string) *AuthModeStore {
	fallback = normalizeMode(fallback)
	s := &AuthModeStore{
		filePath: filepath.Join(dbDir, "database", "auth", "auth_mode.json"),
		fallback: fallback,
		mode:     fallback,
	}
	_ = s.load()
	return s
}

// Get returns the effective auth mode.
func (s *AuthModeStore) Get() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.mode == "" {
		return s.fallback
	}
	return s.mode
}

// Set persists a new mode. Returns ErrInvalidAuthMode for unrecognized
// input. The empty string is interpreted as "revert to TOML".
func (s *AuthModeStore) Set(mode string) error {
	mode = strings.TrimSpace(mode)
	if mode != "" && !validMode(mode) {
		return ErrInvalidAuthMode
	}
	s.mu.Lock()
	s.mode = mode
	s.mu.Unlock()
	if mode == "" {
		// Delete the override file so future loads fall back to TOML.
		if err := os.Remove(s.filePath); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return s.save()
}

// SetFallback updates the TOML default the store falls back to. Used at
// startup when the TOML changes via /etc/lampac config hot-reload.
func (s *AuthModeStore) SetFallback(mode string) {
	s.mu.Lock()
	s.fallback = normalizeMode(mode)
	s.mu.Unlock()
}

func (s *AuthModeStore) load() error {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return err
	}
	var d authModeData
	if err := json.Unmarshal(data, &d); err != nil {
		return err
	}
	if d.Mode != "" && validMode(d.Mode) {
		s.mu.Lock()
		s.mode = d.Mode
		s.mu.Unlock()
	}
	return nil
}

func (s *AuthModeStore) save() error {
	s.mu.RLock()
	d := authModeData{Mode: s.mode, UpdatedAt: time.Now().UTC()}
	s.mu.RUnlock()
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(s.filePath, data)
}

func validMode(m string) bool {
	switch m {
	case AuthModeTG, AuthModePassword, AuthModeNone:
		return true
	}
	return false
}

func normalizeMode(m string) string {
	m = strings.TrimSpace(m)
	if validMode(m) {
		return m
	}
	return AuthModeTG
}
