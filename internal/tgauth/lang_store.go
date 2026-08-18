package tgauth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/rs/zerolog/log"
)

// LangStore persists per-user language preferences.
type LangStore struct {
	mu       sync.RWMutex
	langs    map[int64]Lang
	filePath string
}

// NewLangStore creates a LangStore backed by database/tgauth/user_langs.json inside dbDir.
func NewLangStore(dbDir string) *LangStore {
	s := &LangStore{
		langs:    make(map[int64]Lang),
		filePath: filepath.Join(dbDir, "database", "tgauth", "user_langs.json"),
	}
	s.load()
	return s
}

// Get returns the user's language preference, defaulting to LangRU.
func (s *LangStore) Get(tgID int64) (Lang, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	l, ok := s.langs[tgID]
	return l, ok
}

// Set stores the user's language preference and persists to disk.
func (s *LangStore) Set(tgID int64, lang Lang) {
	s.mu.Lock()
	s.langs[tgID] = lang
	s.mu.Unlock()
	s.save()
}

func (s *LangStore) load() {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return // file doesn't exist yet — that's fine
	}
	// File format: {"12345":"uk","67890":"en"}
	raw := make(map[string]string)
	if err := json.Unmarshal(data, &raw); err != nil {
		log.Warn().Err(err).Msg("tgauth: failed to parse user_langs.json")
		return
	}
	for k, v := range raw {
		var id int64
		if _, err := fmt.Sscanf(k, "%d", &id); err == nil && ValidLang(Lang(v)) {
			s.langs[id] = Lang(v)
		}
	}
	log.Info().Int("users", len(s.langs)).Msg("tgauth: loaded language preferences")
}

func (s *LangStore) save() {
	s.mu.RLock()
	raw := make(map[string]string, len(s.langs))
	for id, lang := range s.langs {
		raw[fmt.Sprintf("%d", id)] = string(lang)
	}
	s.mu.RUnlock()

	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		log.Error().Err(err).Msg("tgauth: failed to marshal user_langs.json")
		return
	}

	dir := filepath.Dir(s.filePath)
	_ = os.MkdirAll(dir, 0755)
	if err := os.WriteFile(s.filePath, data, 0644); err != nil {
		log.Error().Err(err).Msg("tgauth: failed to save user_langs.json")
	}
}
