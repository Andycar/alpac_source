package rutracker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// hashStore is the permanent topicID → magnet cache. A topic's info-hash never
// changes (a re-upload gets a new topic), so entries never expire — this is
// what makes the "one request per release, ever" cost model hold across
// restarts.
type hashStore struct {
	mu    sync.RWMutex
	m     map[int]string
	dirty bool

	dir     string
	timer   *time.Timer
	timerMu sync.Mutex
}

func newHashStore(dir string) *hashStore {
	s := &hashStore{m: map[int]string{}, dir: dir}
	s.load()
	return s
}

func (s *hashStore) path() string {
	if s.dir == "" {
		return ""
	}
	return filepath.Join(s.dir, "magnets.json")
}

func (s *hashStore) load() {
	path := s.path()
	if path == "" {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var m map[int]string
	if json.Unmarshal(b, &m) != nil {
		return
	}
	s.mu.Lock()
	for k, v := range m {
		if k > 0 && v != "" {
			s.m[k] = v
		}
	}
	n := len(s.m)
	s.mu.Unlock()
	log.Debug().Int("magnets", n).Str("path", path).Msg("rutracker: magnet cache loaded")
}

func (s *hashStore) get(topicID int) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.m[topicID]
}

func (s *hashStore) put(topicID int, magnet string) {
	if topicID <= 0 || magnet == "" {
		return
	}
	s.mu.Lock()
	if s.m[topicID] == magnet {
		s.mu.Unlock()
		return
	}
	s.m[topicID] = magnet
	s.dirty = true
	s.mu.Unlock()
	s.scheduleFlush()
}

func (s *hashStore) len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.m)
}

// scheduleFlush debounces disk writes — a search resolves a dozen magnets in a
// burst and should cost one write, not a dozen.
func (s *hashStore) scheduleFlush() {
	if s.path() == "" {
		return
	}
	s.timerMu.Lock()
	defer s.timerMu.Unlock()
	if s.timer != nil {
		return
	}
	s.timer = time.AfterFunc(5*time.Second, func() {
		s.timerMu.Lock()
		s.timer = nil
		s.timerMu.Unlock()
		s.flush()
	})
}

// flush writes the cache when dirty. Tests MUST call it before asserting on
// the file — the debounce timer will not have fired yet.
func (s *hashStore) flush() {
	path := s.path()
	if path == "" {
		return
	}
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return
	}
	snapshot := make(map[int]string, len(s.m))
	for k, v := range s.m {
		snapshot[k] = v
	}
	s.dirty = false
	s.mu.Unlock()

	b, err := json.Marshal(snapshot)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		log.Debug().Err(err).Msg("rutracker: magnet cache write failed")
		s.mu.Lock()
		s.dirty = true
		s.mu.Unlock()
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		s.mu.Lock()
		s.dirty = true
		s.mu.Unlock()
	}
}
