//go:build torrs

package torrs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/rs/zerolog/log"
	bolt "go.etcd.io/bbolt"
)

// Store persists torrents and settings as JSON files alongside the data dir.
// File layout (under homeDir):
//
//	store/torrents.json   — map[hashLowercase]TorrentDB
//	store/settings.json   — Settings
//
// Atomic-rename writes prevent corruption.  Loading at startup migrates a
// legacy bbolt-backed torrs.db file (one-shot) when present.
type Store struct {
	mu       sync.RWMutex
	saveMu   sync.Mutex // serializes file writes across goroutines

	torrentsPath string
	settingsPath string

	torrents map[string]TorrentDB
	settings Settings
}

// NewStore opens (or creates) the JSON store at homeDir/store/.
//
// Backward compatibility: if a legacy bbolt-format torrs.db is found at
// homeDir/torrs.db and no JSON store exists yet, it is migrated once.
func NewStore(homeDir string) (*Store, error) {
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		return nil, fmt.Errorf("create home dir: %w", err)
	}
	storeDir := filepath.Join(homeDir, "store")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}

	s := &Store{
		torrentsPath: filepath.Join(storeDir, "torrents.json"),
		settingsPath: filepath.Join(storeDir, "settings.json"),
		torrents:     map[string]TorrentDB{},
		settings:     defaultSettings(),
	}

	// Load torrents JSON if present.
	if data, err := os.ReadFile(s.torrentsPath); err == nil {
		if uerr := json.Unmarshal(data, &s.torrents); uerr != nil {
			log.Warn().Err(uerr).Str("path", s.torrentsPath).Msg("torrs-store: corrupt torrents.json, starting empty")
			s.torrents = map[string]TorrentDB{}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		log.Warn().Err(err).Str("path", s.torrentsPath).Msg("torrs-store: cannot read torrents.json")
	}

	// Load settings JSON if present.
	if data, err := os.ReadFile(s.settingsPath); err == nil {
		var st Settings
		if uerr := json.Unmarshal(data, &st); uerr == nil {
			s.settings = st
		} else {
			log.Warn().Err(uerr).Str("path", s.settingsPath).Msg("torrs-store: corrupt settings.json, using defaults")
		}
	}

	// One-shot migration from legacy bbolt store.
	legacy := filepath.Join(homeDir, "torrs.db")
	jsonEmpty := len(s.torrents) == 0
	if jsonEmpty {
		if _, err := os.Stat(legacy); err == nil {
			migrated := migrateFromBBolt(legacy, s)
			if migrated > 0 {
				log.Info().Int("count", migrated).Str("legacy", legacy).Msg("torrs-store: migrated bbolt → JSON")
				_ = s.flushAll()
				bak := legacy + ".bbolt-bak"
				if err := os.Rename(legacy, bak); err != nil {
					log.Warn().Err(err).Str("path", legacy).Msg("torrs-store: could not rename legacy bbolt file")
				} else {
					log.Info().Str("renamed", bak).Msg("torrs-store: legacy bbolt file moved aside")
				}
			}
		}
	}

	log.Debug().Str("path", s.torrentsPath).Int("count", len(s.torrents)).Msg("torrs-store: opened")
	return s, nil
}

// migrateFromBBolt reads the bucket layout used by the previous bbolt-backed
// store and copies entries into the JSON store.  Returns the number of
// torrents copied; settings are also migrated when present.
func migrateFromBBolt(path string, s *Store) int {
	db, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true})
	if err != nil {
		log.Warn().Err(err).Str("path", path).Msg("torrs-store: cannot open legacy bbolt — skipping migration")
		return 0
	}
	defer db.Close()

	var count int
	bucketTorrents := []byte("torrents")
	bucketSettings := []byte("settings")
	keyMain := []byte("main")

	_ = db.View(func(tx *bolt.Tx) error {
		if b := tx.Bucket(bucketTorrents); b != nil {
			_ = b.ForEach(func(k, v []byte) error {
				var t TorrentDB
				if err := json.Unmarshal(v, &t); err != nil {
					log.Warn().Str("key", string(k)).Err(err).Msg("torrs-store: legacy entry corrupt, skipping")
					return nil
				}
				if t.Hash == "" {
					t.Hash = strings.ToLower(string(k))
				}
				s.torrents[strings.ToLower(t.Hash)] = t
				count++
				return nil
			})
		}
		if b := tx.Bucket(bucketSettings); b != nil {
			if v := b.Get(keyMain); v != nil {
				var st Settings
				if err := json.Unmarshal(v, &st); err == nil {
					s.settings = st
				}
			}
		}
		return nil
	})
	return count
}

// Close persists any unsaved state.  Idempotent — safe to call after errors.
func (s *Store) Close() error {
	return s.flushAll()
}

// flushAll writes both torrents and settings.
func (s *Store) flushAll() error {
	if err := s.flushTorrents(); err != nil {
		return err
	}
	return s.flushSettings()
}

func (s *Store) flushTorrents() error {
	s.mu.RLock()
	data, err := json.MarshalIndent(s.torrents, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	return writeAtomic(s.torrentsPath, data)
}

func (s *Store) flushSettings() error {
	s.mu.RLock()
	data, err := json.MarshalIndent(s.settings, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	return writeAtomic(s.settingsPath, data)
}

// SaveTorrent persists a torrent entry.
func (s *Store) SaveTorrent(t TorrentDB) error {
	if t.Hash == "" {
		return errors.New("torrs-store: empty hash")
	}
	key := strings.ToLower(t.Hash)
	t.Hash = key
	s.mu.Lock()
	s.torrents[key] = t
	s.mu.Unlock()
	return s.flushTorrents()
}

// GetTorrent retrieves a single torrent by hash.
func (s *Store) GetTorrent(hash string) (*TorrentDB, error) {
	key := strings.ToLower(hash)
	s.mu.RLock()
	defer s.mu.RUnlock()
	if t, ok := s.torrents[key]; ok {
		return &t, nil
	}
	return nil, errors.New("not found")
}

// ListTorrents returns all saved torrents.  Order is unspecified.
func (s *Store) ListTorrents() ([]TorrentDB, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]TorrentDB, 0, len(s.torrents))
	for _, t := range s.torrents {
		out = append(out, t)
	}
	return out, nil
}

// RemoveTorrent deletes a torrent entry by hash.
func (s *Store) RemoveTorrent(hash string) error {
	key := strings.ToLower(hash)
	s.mu.Lock()
	if _, ok := s.torrents[key]; !ok {
		s.mu.Unlock()
		return nil
	}
	delete(s.torrents, key)
	s.mu.Unlock()
	return s.flushTorrents()
}

// GetSettings loads settings, returning defaults when not previously saved.
func (s *Store) GetSettings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

// SaveSettings persists settings.
func (s *Store) SaveSettings(st Settings) error {
	s.mu.Lock()
	s.settings = st
	s.mu.Unlock()
	return s.flushSettings()
}

func defaultSettings() Settings {
	return Settings{
		CacheSize:       64 << 20, // 64 MB
		PreloadSize:     5 << 20,  // 5 MB
		ReaderReadAHead: 95,
	}
}

// writeAtomic writes data to a temp file then renames, preventing corruption
// if the process is killed mid-write.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
