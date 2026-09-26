// Package skipdb manages intro/outro skip segment data for TV episodes.
// Data is stored in a JSON file and looked up by IMDb ID + season + episode.
package skipdb

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sync"

	"github.com/rs/zerolog/log"
)

// Segment represents a skippable region in a video.
type Segment struct {
	Type  string  `json:"type"`  // "intro", "outro", "recap"
	Start float64 `json:"start"` // seconds
	End   float64 `json:"end"`   // seconds
	// Provider — откуда метка: "" (ручная/админ), "auto-chromaprint" (introdetect по звуку);
	// Confidence — доля совпавших отпечатков у авто-меток, 0..1. Ручная метка всегда важнее.
	Provider   string  `json:"provider,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
}

// UserMark is a user-submitted skip marker awaiting moderation.
type UserMark struct {
	ImdbID  string  `json:"imdb_id"`
	Season  int     `json:"season"`
	Episode int     `json:"episode"`
	Type    string  `json:"type"`
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	UserID  string  `json:"user_id,omitempty"`
}

// Defaults controls fallback behavior when no data is found.
type Defaults struct {
	IntroDefaultSec int  // 0 = disabled. If >0, return intro segment [0, N] for series episodes with no data.
	OutroOffsetSec  int  // 0 = disabled. If >0, return outro segment [duration-N, duration].
	EnableUserMarks bool // Allow POST /api/skip/mark.
}

// episodeData is the per-episode storage unit.
// Key format: "s1" → "e1" → []Segment
type showData map[string]map[string][]Segment

// DB is the skip segment database.
type DB struct {
	mu       sync.RWMutex
	data     map[string]showData // imdbID → showData
	marks    []UserMark          // pending user marks
	filePath string
	marksDir string
	defaults Defaults
}

// New creates a skipdb loaded from {repoRoot}/database/skipdb/community.json.
func New(repoRoot string, defaults Defaults) *DB {
	dir := filepath.Join(repoRoot, "database", "skipdb")
	_ = os.MkdirAll(dir, 0o755)

	db := &DB{
		data:     make(map[string]showData),
		filePath: filepath.Join(dir, "community.json"),
		marksDir: dir,
		defaults: defaults,
	}
	if err := db.load(); err != nil && !os.IsNotExist(err) {
		log.Warn().Err(err).Msg("skipdb: failed to load community.json")
	}

	// Load user marks.
	db.loadMarks()

	return db
}

// Lookup returns skip segments for the given content.
func (db *DB) Lookup(imdbID string, season, episode int) []Segment {
	db.mu.RLock()
	defer db.mu.RUnlock()

	show, ok := db.data[imdbID]
	if !ok {
		return nil
	}

	seasonKey := fmt.Sprintf("s%d", season)
	epMap, ok := show[seasonKey]
	if !ok {
		return nil
	}

	epKey := fmt.Sprintf("e%d", episode)
	segs, ok := epMap[epKey]
	if !ok {
		return nil
	}

	// Return a copy.
	out := make([]Segment, len(segs))
	copy(out, segs)
	return out
}

// Set stores skip segments for a specific episode.
func (db *DB) Set(imdbID string, season, episode int, segments []Segment) error {
	db.mu.Lock()

	show, ok := db.data[imdbID]
	if !ok {
		show = make(showData)
		db.data[imdbID] = show
	}

	seasonKey := fmt.Sprintf("s%d", season)
	epMap, ok := show[seasonKey]
	if !ok {
		epMap = make(map[string][]Segment)
		show[seasonKey] = epMap
	}

	epKey := fmt.Sprintf("e%d", episode)
	epMap[epKey] = segments
	db.mu.Unlock()

	return db.save()
}

// Delete removes skip data for a specific episode.
func (db *DB) Delete(imdbID string, season, episode int) error {
	db.mu.Lock()

	show, ok := db.data[imdbID]
	if ok {
		seasonKey := fmt.Sprintf("s%d", season)
		if epMap, ok := show[seasonKey]; ok {
			epKey := fmt.Sprintf("e%d", episode)
			delete(epMap, epKey)
			if len(epMap) == 0 {
				delete(show, seasonKey)
			}
		}
		if len(show) == 0 {
			delete(db.data, imdbID)
		}
	}
	db.mu.Unlock()

	return db.save()
}

// AddUserMark stores a user-submitted skip marker.
func (db *DB) AddUserMark(mark UserMark) error {
	db.mu.Lock()
	db.marks = append(db.marks, mark)
	db.mu.Unlock()
	return db.saveMarks()
}

// UserMarks returns all pending user marks.
func (db *DB) UserMarks() []UserMark {
	db.mu.RLock()
	defer db.mu.RUnlock()
	out := make([]UserMark, len(db.marks))
	copy(out, db.marks)
	return out
}

// ApproveUserMark moves a user mark into the community DB and removes it from pending.
func (db *DB) ApproveUserMark(idx int) error {
	db.mu.Lock()
	if idx < 0 || idx >= len(db.marks) {
		db.mu.Unlock()
		return fmt.Errorf("invalid mark index %d", idx)
	}
	m := db.marks[idx]
	db.marks = append(db.marks[:idx], db.marks[idx+1:]...)
	db.mu.Unlock()

	// Save marks first, then set the segment.
	if err := db.saveMarks(); err != nil {
		return err
	}

	// Merge into community DB (append, don't overwrite existing segments).
	existing := db.Lookup(m.ImdbID, m.Season, m.Episode)
	seg := Segment{Type: m.Type, Start: m.Start, End: m.End}
	existing = append(existing, seg)
	return db.Set(m.ImdbID, m.Season, m.Episode, existing)
}

// RejectUserMark removes a user mark from pending.
func (db *DB) RejectUserMark(idx int) error {
	db.mu.Lock()
	if idx < 0 || idx >= len(db.marks) {
		db.mu.Unlock()
		return fmt.Errorf("invalid mark index %d", idx)
	}
	db.marks = append(db.marks[:idx], db.marks[idx+1:]...)
	db.mu.Unlock()
	return db.saveMarks()
}

// Stats returns total shows and episodes with skip data.
func (db *DB) Stats() (totalShows, totalEpisodes int) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	totalShows = len(db.data)
	for _, show := range db.data {
		for _, eps := range show {
			totalEpisodes += len(eps)
		}
	}
	return
}

// GetDefaults returns the configured defaults.
func (db *DB) GetDefaults() Defaults {
	return db.defaults
}

// Import replaces the entire DB from a JSON map.
func (db *DB) Import(data map[string]showData) error {
	db.mu.Lock()
	db.data = data
	db.mu.Unlock()
	return db.save()
}

// Export returns the raw data map (for admin API).
func (db *DB) Export() map[string]showData {
	db.mu.RLock()
	defer db.mu.RUnlock()
	out := make(map[string]showData, len(db.data))
	maps.Copy(out, db.data)
	return out
}

// ListShow returns all seasons/episodes for a given IMDb ID.
func (db *DB) ListShow(imdbID string) showData {
	db.mu.RLock()
	defer db.mu.RUnlock()
	show, ok := db.data[imdbID]
	if !ok {
		return nil
	}
	return show
}

// ---------------------------------------------------------------------------
// Persistence
// ---------------------------------------------------------------------------

func (db *DB) load() error {
	raw, err := os.ReadFile(db.filePath)
	if err != nil {
		return err
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	return json.Unmarshal(raw, &db.data)
}

func (db *DB) save() error {
	db.mu.RLock()
	raw, err := json.MarshalIndent(db.data, "", "  ")
	db.mu.RUnlock()
	if err != nil {
		return err
	}
	return os.WriteFile(db.filePath, raw, 0o644)
}

func (db *DB) loadMarks() {
	fp := filepath.Join(db.marksDir, "user_marks.json")
	raw, err := os.ReadFile(fp)
	if err != nil {
		return
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	_ = json.Unmarshal(raw, &db.marks)
}

func (db *DB) saveMarks() error {
	db.mu.RLock()
	raw, err := json.MarshalIndent(db.marks, "", "  ")
	db.mu.RUnlock()
	if err != nil {
		return err
	}
	fp := filepath.Join(db.marksDir, "user_marks.json")
	return os.WriteFile(fp, raw, 0o644)
}
