//go:build torrs

package torrs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	"github.com/rs/zerolog/log"
	"golang.org/x/time/rate"
)

// dhtTotalGoodNodes walks all DHT servers, asks each for Stats(), and tries
// to pull a "GoodNodes" integer via reflection.  Concrete type lives in
// `github.com/anacrolix/dht/v2`; we avoid a hard dep so this stays a
// best-effort metric — returns 0 if the field is missing or unreadable.
func dhtTotalGoodNodes(servers []torrent.DhtServer) int {
	var total int
	for _, s := range servers {
		total += extractIntField(s.Stats(), "GoodNodes")
	}
	return total
}

func extractIntField(v any, name string) int {
	if v == nil {
		return 0
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return 0
	}
	f := rv.FieldByName(name)
	if !f.IsValid() || !f.CanInterface() {
		return 0
	}
	switch f.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return int(f.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int(f.Uint())
	}
	return 0
}

// addMetadataTimeout is how long Add/AddFromBytes wait for torrent metadata
// before giving up and rolling back the active+DB entry.
const addMetadataTimeout = 30 * time.Second

// restoreConcurrency caps parallel restore goroutines on startup so we don't
// flood DHT and OOM the server when many torrents are saved.
const restoreConcurrency = 8

// addFromURLMaxBytes is the upper bound on a downloaded .torrent file.
const addFromURLMaxBytes int64 = 10 << 20 // 10 MB

// addFromURLMaxRedirects caps HTTP redirect chains for .torrent fetches.
const addFromURLMaxRedirects = 5

// ephemeralGracePeriod protects torrents added with saveToDB=false (e.g.
// pidtor probes; Timestamp=0) from being evicted by disk-cleanup or
// enforceMaxActive in the first window after creation.
const ephemeralGracePeriod = 10 * time.Minute

// torrent stat codes (MatriX-compatible).
const (
	StatGettingMetadata = 1
	StatPreload         = 2
	StatWorking         = 3
	StatClosed          = 4
)

// BTServer is the in-process torrent server.
type BTServer struct {
	mu       sync.RWMutex
	client   *torrent.Client
	store    *Store
	settings atomic.Pointer[Settings] // read concurrently, written by SetSettings.
	homeDir  string
	cfg      TorrsConfig
	// ram is non-nil in RAM-cache mode (ram_cache = true): pieces live in
	// memory and nothing is written to <home>/data. nil ⇒ file storage.
	ram       *ramCache
	startedAt time.Time

	// active tracks torrent objects by lowercase hex hash.
	active map[string]*ActiveTorrent

	// speedSnaps stores previous bytes-read snapshots for delta-based speed calculation.
	// Cleaned up by Remove/Drop to avoid unbounded growth.
	speedSnaps map[string]speedSnap
}

type speedSnap struct {
	bytesRead     int64
	bytesWritten  int64
	at            time.Time
	downloadSpeed int64
	uploadSpeed   int64
}

// ActiveTorrent wraps a live anacrolix torrent with its DB metadata.
type ActiveTorrent struct {
	T          *torrent.Torrent
	DB         TorrentDB
	lastAccess time.Time // last time Stream/Preload was called; used by DiskCleanup
}

// New creates and starts the in-process torrent server.
func New(homeDir string, cfg TorrsConfig) (*BTServer, error) {
	if homeDir == "" {
		return nil, fmt.Errorf("torrs: homeDir is required")
	}

	dataDir := filepath.Join(homeDir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	// Open persistence.
	st, err := NewStore(homeDir)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}

	settings := st.GetSettings()

	// Override from config if set.
	if cfg.CacheSizeMB > 0 {
		settings.CacheSize = int64(cfg.CacheSizeMB) << 20
	}
	if cfg.PreloadMB > 0 {
		settings.PreloadSize = int64(cfg.PreloadMB) << 20
	}

	// Configure anacrolix client.
	clientCfg := torrent.NewDefaultClientConfig()
	clientCfg.DataDir = dataDir

	// Storage: RAM-only (nothing hits the disk, pieces evicted LRU around the
	// reader) or the classic file-per-infohash cache.
	var ram *ramCache
	if cfg.RAMCache {
		budget := int64(cfg.CacheSizeMB) << 20
		if budget < ramMinBudget {
			log.Warn().Int("requested_mb", cfg.CacheSizeMB).Int64("using_mb", int64(ramMinBudget)>>20).
				Msg("torrs: cache_size_mb too small for RAM mode — raised to the floor")
			budget = ramMinBudget
		}
		ram = newRAMCache(budget)
		clientCfg.DefaultStorage = ram
		log.Info().Int64("budget_mb", budget>>20).Msg("torrs: RAM cache enabled — pieces are never written to disk")
	} else {
		clientCfg.DefaultStorage = storage.NewFileByInfoHash(dataDir)
	}
	clientCfg.Seed = false
	clientCfg.NoUpload = cfg.DisableUpload
	clientCfg.ListenPort = 0 // random port
	clientCfg.NoDHT = cfg.DisableDHT
	clientCfg.NoDefaultPortForwarding = true

	// Rate limits. anacrolix uses rate.Limiter where one token = one byte.
	// Burst must exceed the largest single read; 1 MiB is comfortably above
	// the 16 KiB chunk size used by BitTorrent.
	if cfg.MaxDownloadSpeedMBs > 0 {
		bytesPerSec := float64(cfg.MaxDownloadSpeedMBs) * (1 << 20)
		clientCfg.DownloadRateLimiter = rate.NewLimiter(rate.Limit(bytesPerSec), 1<<20)
	}
	if cfg.MaxUploadSpeedMBs > 0 {
		bytesPerSec := float64(cfg.MaxUploadSpeedMBs) * (1 << 20)
		clientCfg.UploadRateLimiter = rate.NewLimiter(rate.Limit(bytesPerSec), 1<<20)
	}

	client, err := torrent.NewClient(clientCfg)
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("torrent client: %w", err)
	}

	srv := &BTServer{
		client:     client,
		store:      st,
		homeDir:    homeDir,
		cfg:        cfg,
		ram:        ram,
		startedAt:  time.Now(),
		active:     make(map[string]*ActiveTorrent),
		speedSnaps: make(map[string]speedSnap),
	}
	// MatriX clients read UseDisk to show the storage mode; keep it honest.
	settings.UseDisk = ram == nil
	if ram != nil {
		settings.CacheSize = ram.Budget()
	}
	srv.settings.Store(&settings)

	if ram != nil {
		go srv.ramEvictionLoop()
	}

	// Restore saved torrents from DB, bounded by restoreConcurrency.
	saved, _ := st.ListTorrents()
	if len(saved) > 0 {
		log.Info().Int("count", len(saved)).Msg("torrs: restoring saved torrents")
		go srv.restoreAll(saved)
	}

	// Start disk cleanup goroutine.
	go srv.diskCleanupLoop()

	log.Info().Str("homeDir", homeDir).Msg("torrs: server started")
	return srv, nil
}

// IsAvailable returns true when built with torrs tag.
func IsAvailable() bool { return true }

// Close shuts down the torrent server.
func (s *BTServer) Close() {
	s.mu.Lock()
	for hash, at := range s.active {
		at.T.Drop()
		delete(s.active, hash)
	}
	s.speedSnaps = nil
	s.mu.Unlock()

	if s.client != nil {
		s.client.Close()
	}
	if s.ram != nil {
		s.ram.Close() // ends ramEvictionLoop
	}
	if s.store != nil {
		s.store.Close()
	}
	log.Info().Msg("torrs: server stopped")
}

// ramMode reports whether pieces live in RAM instead of on disk.
func (s *BTServer) ramMode() bool { return s.ram != nil }

// ramEvictionLoop tells a torrent to re-read the completion state of pieces the
// cache dropped, so anacrolix re-requests them instead of believing it still
// has the data.
//
// It runs on its own goroutine on purpose: Piece.UpdateCompletion takes the
// client lock, and anacrolix calls storage callbacks (Completion, MarkComplete)
// while holding it — invalidating inline would invert the lock order. Missing a
// notice is survivable, not fatal: storage.Piece.ReadAt turns the evicted
// piece's io.EOF into MarkNotComplete and the reader re-syncs on its own.
func (s *BTServer) ramEvictionLoop() {
	for ev := range s.ram.Evictions() {
		hash := ev.Hash.HexString()
		s.mu.RLock()
		at := s.active[hash]
		s.mu.RUnlock()
		if at == nil || at.T == nil {
			continue
		}
		select {
		case <-at.T.GotInfo():
		default:
			continue // no info yet ⇒ no pieces to invalidate
		}
		if ev.Piece >= 0 && ev.Piece < at.T.NumPieces() {
			at.T.Piece(ev.Piece).UpdateCompletion()
		}
	}
}

// currentSettings returns the live settings snapshot.
func (s *BTServer) currentSettings() Settings {
	if p := s.settings.Load(); p != nil {
		return *p
	}
	return defaultSettings()
}

// readahead returns the bytes window to use for torrent file readers.
// Honors ReaderReadAheadMB (config), falls back to ReaderReadAHead percentage
// of file size, and finally caps at a sensible default.
func (s *BTServer) readahead(fileLen int64) int64 {
	if mb := s.cfg.ReaderReadAheadMB; mb > 0 {
		return int64(mb) << 20
	}
	st := s.currentSettings()
	pct := st.ReaderReadAHead
	if pct <= 0 || pct > 100 {
		pct = 95
	}
	v := fileLen * int64(pct) / 100
	// Bound: at least 16 MiB so HD streams don't stall; at most 256 MiB.
	if v < 16<<20 {
		v = 16 << 20
	}
	if v > 256<<20 {
		v = 256 << 20
	}
	if v > fileLen && fileLen > 0 {
		v = fileLen
	}
	// RAM mode: the readahead window IS the memory footprint, so it must leave
	// room for the pieces being written and for eviction to lag behind. A third
	// of the budget keeps the window comfortably resident instead of racing the
	// evictor for the same bytes.
	if s.ram != nil {
		if maxWin := s.ram.Budget() / 3; v > maxWin {
			v = maxWin
		}
		if v < 8<<20 {
			v = 8 << 20
		}
		if v > fileLen && fileLen > 0 {
			v = fileLen
		}
	}
	return v
}

// ---------------------------------------------------------------------------
//  Disk cleanup
// ---------------------------------------------------------------------------

// diskCleanupLoop runs every 5 minutes and enforces the configured cleanup
// policy.  Disabled when CacheCleanupEnable=false.
func (s *BTServer) diskCleanupLoop() {
	if !s.cleanupEnabled() {
		log.Info().Msg("torrs: disk cleanup disabled by CacheCleanupEnable=false")
		return
	}
	// Run once on startup after a short delay.
	time.Sleep(30 * time.Second)
	s.DiskCleanup()

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		if !s.cleanupEnabled() {
			continue
		}
		s.DiskCleanup()
	}
}

// cleanupEnabled returns true when disk-cleanup should run.
//
// The flag is opt-out: applyDefaults() sets CacheCleanupEnable=true, so
// only explicitly disabling it in TOML turns the loop off.  We also auto-
// disable when every cleanup-related field is zero (cfg with no torrserver
// section at all) to keep tests / minimal-config setups quiet — but the
// production binary always lands here with CacheCleanupEnable=true.
func (s *BTServer) cleanupEnabled() bool {
	c := s.cfg
	if !c.CacheCleanupEnable && c.CacheCleanupDays == 0 && c.CacheCleanupMaxGB == 0 && c.DiskCacheMB == 0 {
		return false // pure-zero config → no cleanup
	}
	return c.CacheCleanupEnable
}

// diskLimitMB returns the disk usage cap in MB, reconciling DiskCacheMB
// (preferred) with the legacy CacheCleanupMaxGB alias.
func (s *BTServer) diskLimitMB() int {
	if s.cfg.DiskCacheMB > 0 {
		return s.cfg.DiskCacheMB
	}
	if s.cfg.CacheCleanupMaxGB > 0 {
		return s.cfg.CacheCleanupMaxGB * 1024
	}
	return 1024 // 1 GiB historical default
}

// DiskCleanup is the original ticker-driven cleanup hook.  Returns nothing
// to keep backward compat with the goroutine loop.
func (s *BTServer) DiskCleanup() {
	_ = s.DiskCleanupReport()
}

// DiskCleanupReport runs a full cleanup pass and reports what was freed.
// Used by the manual trigger endpoint so the operator sees concrete numbers.
func (s *BTServer) DiskCleanupReport() CleanupReport {
	limitMB := s.diskLimitMB()
	limitBytes := int64(limitMB) << 20

	dataDir := filepath.Join(s.homeDir, "data")

	// Calculate total disk usage and per-hash sizes.
	totalSize, hashSizes := s.dataDirUsage(dataDir)
	startSize := totalSize

	// Pass 1: age-based cleanup (CacheCleanupDays).  Independent of disk usage —
	// stale torrents are removed even if disk is well under the limit.
	now := time.Now()
	var ageCutoff int64
	if s.cfg.CacheCleanupDays > 0 {
		ageCutoff = now.Add(-time.Duration(s.cfg.CacheCleanupDays) * 24 * time.Hour).Unix()
	}

	saved, _ := s.store.ListTorrents()
	dbHashes := make(map[string]bool, len(saved))
	for _, t := range saved {
		dbHashes[strings.ToLower(t.Hash)] = true
	}

	s.mu.RLock()
	// "Recent" protects torrents streamed in the last 30 min from any cleanup.
	const recentWindow = 30 * time.Minute
	recentHashes := make(map[string]bool, len(s.active))
	creationTimes := make(map[string]time.Time, len(s.active))
	for h, at := range s.active {
		if !at.lastAccess.IsZero() && now.Sub(at.lastAccess) < recentWindow {
			recentHashes[h] = true
		}
		// Approximate creation time: DB.Timestamp for persisted entries,
		// or lastAccess for ephemeral (saveToDB=false) entries where it
		// happens to be the first time the torrent was added.
		if at.DB.Timestamp > 0 {
			creationTimes[h] = time.Unix(at.DB.Timestamp, 0)
		} else if !at.lastAccess.IsZero() {
			creationTimes[h] = at.lastAccess
		}
	}
	s.mu.RUnlock()

	agedRemoved := 0
	if ageCutoff > 0 {
		for _, t := range saved {
			h := strings.ToLower(t.Hash)
			if t.Timestamp <= 0 || t.Timestamp > ageCutoff {
				continue // newer than cutoff
			}
			if recentHashes[h] {
				continue // protect actively-streamed
			}
			lastUse := t.Timestamp
			if t.LastAccess > t.Timestamp {
				lastUse = t.LastAccess
			}
			if lastUse > ageCutoff {
				continue // recently touched, even if originally old
			}
			s.Remove(h)
			delete(hashSizes, h)
			agedRemoved++
			log.Info().Str("hash", h).Int64("age_days", (now.Unix()-t.Timestamp)/86400).
				Msg("torrs: disk cleanup — removed (age)")
		}
		if agedRemoved > 0 {
			// Re-tally; size-based pass below should see the slimmer state.
			totalSize, hashSizes = s.dataDirUsage(dataDir)
		}
	}

	// Pass 2: size-based cleanup.  Only runs if we're over the byte cap.
	if totalSize <= limitBytes {
		if agedRemoved > 0 {
			log.Info().Int("aged_removed", agedRemoved).Int64("remaining_mb", totalSize>>20).
				Msg("torrs: disk cleanup done (age only)")
		}
		return CleanupReport{
			StartMB: startSize >> 20, EndMB: totalSize >> 20,
			LimitMB: limitBytes >> 20, AgeRemoved: agedRemoved,
		}
	}

	log.Info().
		Int64("total_mb", totalSize>>20).
		Int64("limit_mb", limitBytes>>20).
		Int("hashes", len(hashSizes)).
		Msg("torrs: disk cleanup — over limit")

	var entries []cleanupEntry
	for _, t := range saved {
		h := strings.ToLower(t.Hash)
		sz, ok := hashSizes[h]
		if !ok || sz == 0 {
			continue
		}
		entries = append(entries, cleanupEntry{hash: h, timestamp: t.Timestamp, size: sz})
	}

	// Also add orphaned dirs (hash on disk but not in DB).  These get
	// timestamp 0 → first to be evicted.
	for h, sz := range hashSizes {
		if !dbHashes[h] {
			entries = append(entries, cleanupEntry{hash: h, timestamp: 0, size: sz})
		}
	}

	// Sort: orphans first (timestamp 0), then oldest first.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].timestamp < entries[j].timestamp
	})

	// Remove until under limit.  Skip recently accessed and ephemeral
	// torrents still within their grace period.
	removed := 0
	for _, e := range entries {
		if totalSize <= limitBytes {
			break
		}
		if recentHashes[e.hash] {
			continue // don't remove recently streamed torrents
		}
		if e.timestamp == 0 {
			// Ephemeral (pidtor probe) — protect within grace window.
			if created, ok := creationTimes[e.hash]; ok && now.Sub(created) < ephemeralGracePeriod {
				continue
			}
		}

		s.Remove(e.hash)
		totalSize -= e.size
		removed++
		log.Info().Str("hash", e.hash).Int64("size_mb", e.size>>20).Msg("torrs: disk cleanup — removed (size)")
	}

	if removed > 0 || agedRemoved > 0 {
		log.Info().
			Int("aged_removed", agedRemoved).
			Int("size_removed", removed).
			Int64("remaining_mb", totalSize>>20).
			Msg("torrs: disk cleanup done")
	}
	return CleanupReport{
		StartMB: startSize >> 20, EndMB: totalSize >> 20,
		LimitMB:    limitBytes >> 20,
		AgeRemoved: agedRemoved, SizeRemoved: removed,
	}
}

// dataDirUsage calculates total and per-hash disk usage in the data directory.
func (s *BTServer) dataDirUsage(dataDir string) (int64, map[string]int64) {
	hashSizes := make(map[string]int64)
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return 0, hashSizes
	}

	var total int64
	for _, de := range entries {
		if !de.IsDir() {
			continue
		}
		hash := strings.ToLower(de.Name())
		dirPath := filepath.Join(dataDir, de.Name())
		var sz int64
		_ = filepath.WalkDir(dirPath, func(_ string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			sz += info.Size()
			return nil
		})
		hashSizes[hash] = sz
		total += sz
	}
	return total, hashSizes
}

type cleanupEntry struct {
	hash      string
	timestamp int64
	size      int64
}

// ---------------------------------------------------------------------------
//  Torrent operations
// ---------------------------------------------------------------------------

// Add adds a torrent by magnet link or HTTP URL. If the torrent is already
// active, the existing entry is reused (metadata refresh only) and lastAccess
// is preserved.
func (s *BTServer) Add(link, title, poster, data string, saveToDB bool) (*TorrentInfo, error) {
	return s.addWithOwner(link, title, poster, data, "", saveToDB)
}

// AddWithOwner adds a torrent and records the owner token, used to scope
// /playlistall/all.m3u to the requesting user.
func (s *BTServer) AddWithOwner(link, title, poster, data, owner string, saveToDB bool) (*TorrentInfo, error) {
	return s.addWithOwner(link, title, poster, data, owner, saveToDB)
}

func (s *BTServer) addWithOwner(link, title, poster, data, owner string, saveToDB bool) (*TorrentInfo, error) {
	t, hash, err := s.addLink(link)
	if err != nil {
		return nil, err
	}

	// Dedup: reuse existing ActiveTorrent if present.
	s.mu.Lock()
	at, existed := s.active[hash]
	if existed {
		// Refresh metadata fields without clobbering lastAccess.
		if title != "" {
			at.DB.Title = title
		}
		if poster != "" {
			at.DB.Poster = poster
		}
		if data != "" {
			at.DB.Data = data
		}
		if owner != "" && at.DB.Owner == "" {
			at.DB.Owner = owner
		}
	} else {
		at = &ActiveTorrent{
			T: t,
			DB: TorrentDB{
				Hash:      hash,
				Title:     title,
				Poster:    poster,
				Data:      data,
				Magnet:    link,
				Owner:     owner,
				Timestamp: time.Now().Unix(),
			},
			lastAccess: time.Now(),
		}
		s.active[hash] = at
	}
	if saveToDB {
		at.DB.LastAccess = time.Now().Unix()
	}
	dbCopy := at.DB
	s.mu.Unlock()

	if saveToDB {
		if err := s.store.SaveTorrent(dbCopy); err != nil {
			log.Warn().Err(err).Str("hash", hash).Msg("torrs: save to db failed")
		}
	}

	// Wait for metadata with timeout. On timeout, roll back so the caller
	// doesn't end up with a zombie in the active map / DB.
	ctx, cancel := context.WithTimeout(context.Background(), addMetadataTimeout)
	defer cancel()

	select {
	case <-t.GotInfo():
	case <-ctx.Done():
		// Roll back only if WE just added this entry; pre-existing torrents
		// that already had metadata-failed elsewhere stay put.
		if !existed {
			s.rollbackAdd(hash, saveToDB)
		}
		return nil, fmt.Errorf("metadata timeout for %s", hash)
	}

	// Enforce MaxActiveTorrents after metadata is in, so /list reflects truth.
	s.enforceMaxActive(hash)

	return s.buildTorrentInfoLocked(hash, t, at.DB), nil
}

// rollbackAdd undoes a partial Add when metadata fails to arrive.
func (s *BTServer) rollbackAdd(hash string, savedToDB bool) {
	s.mu.Lock()
	at, ok := s.active[hash]
	if ok {
		delete(s.active, hash)
	}
	delete(s.speedSnaps, hash)
	s.mu.Unlock()

	if ok && at.T != nil {
		at.T.Drop()
	}
	if savedToDB {
		_ = s.store.RemoveTorrent(hash)
	}
}

// enforceMaxActive drops the oldest non-recently-accessed torrent when the
// MaxActiveTorrents cap is exceeded. The just-added `keepHash` is never
// considered for eviction.
func (s *BTServer) enforceMaxActive(keepHash string) {
	limit := s.cfg.MaxActiveTorrents
	if limit <= 0 {
		return
	}
	const recentWindow = 5 * time.Minute
	now := time.Now()

	s.mu.RLock()
	if len(s.active) <= limit {
		s.mu.RUnlock()
		return
	}
	type ent struct {
		hash       string
		lastAccess time.Time
	}
	candidates := make([]ent, 0, len(s.active))
	for h, at := range s.active {
		if h == keepHash {
			continue
		}
		if !at.lastAccess.IsZero() && now.Sub(at.lastAccess) < recentWindow {
			continue
		}
		// Ephemeral entries (saveToDB=false, e.g. pidtor probes) stay
		// untouchable within the grace period — otherwise a freshly-added
		// probe could be dropped before the ffmpeg process even starts
		// reading from it.
		if at.DB.Timestamp == 0 && !at.lastAccess.IsZero() && now.Sub(at.lastAccess) < ephemeralGracePeriod {
			continue
		}
		candidates = append(candidates, ent{hash: h, lastAccess: at.lastAccess})
	}
	overflow := len(s.active) - limit
	s.mu.RUnlock()

	if overflow <= 0 || len(candidates) == 0 {
		return
	}

	sort.Slice(candidates, func(i, j int) bool {
		// Zero (never accessed since restore) sorts first.
		if candidates[i].lastAccess.IsZero() && !candidates[j].lastAccess.IsZero() {
			return true
		}
		if !candidates[i].lastAccess.IsZero() && candidates[j].lastAccess.IsZero() {
			return false
		}
		return candidates[i].lastAccess.Before(candidates[j].lastAccess)
	})

	if overflow > len(candidates) {
		overflow = len(candidates)
	}
	for i := 0; i < overflow; i++ {
		log.Info().Str("hash", candidates[i].hash).Msg("torrs: MaxActiveTorrents — dropping oldest")
		s.Drop(candidates[i].hash)
	}
}

// Get returns torrent info by hash.
func (s *BTServer) Get(hash string) (*TorrentInfo, error) {
	hash = strings.ToLower(hash)

	s.mu.RLock()
	at := s.active[hash]
	s.mu.RUnlock()

	if at == nil {
		return nil, fmt.Errorf("torrent not found: %s", hash)
	}

	// Wait briefly for metadata.
	select {
	case <-at.T.GotInfo():
	case <-time.After(15 * time.Second):
		return nil, fmt.Errorf("metadata timeout for %s", hash)
	}

	return s.buildTorrentInfoLocked(hash, at.T, at.DB), nil
}

// List returns info for all active torrents.
func (s *BTServer) List() []TorrentInfo {
	return s.listFiltered(func(*ActiveTorrent) bool { return true })
}

// ListByOwner returns torrents whose Owner matches the given token, plus
// any torrents with no owner (legacy / shared). Empty token returns all.
func (s *BTServer) ListByOwner(owner string) []TorrentInfo {
	if owner == "" {
		return s.List()
	}
	return s.listFiltered(func(at *ActiveTorrent) bool {
		return at.DB.Owner == "" || at.DB.Owner == owner
	})
}

func (s *BTServer) listFiltered(keep func(*ActiveTorrent) bool) []TorrentInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]TorrentInfo, 0, len(s.active))
	for hash, at := range s.active {
		if !keep(at) {
			continue
		}
		select {
		case <-at.T.GotInfo():
			result = append(result, *s.buildTorrentInfoLocked(hash, at.T, at.DB))
		default:
			// Metadata not ready — include with minimal info.
			result = append(result, TorrentInfo{
				Hash:       hash,
				Title:      at.DB.Title,
				Poster:     at.DB.Poster,
				Data:       at.DB.Data,
				Timestamp:  at.DB.Timestamp,
				Stat:       StatGettingMetadata,
				StatString: "Torrent fetching metadata",
			})
		}
	}
	return result
}

// Remove stops and permanently deletes a torrent (DB entry + disk data).
func (s *BTServer) Remove(hash string) {
	hash = strings.ToLower(hash)

	s.mu.Lock()
	at, ok := s.active[hash]
	delete(s.active, hash)
	delete(s.speedSnaps, hash)
	s.mu.Unlock()

	if ok && at.T != nil {
		at.T.Drop()
	}
	_ = s.store.RemoveTorrent(hash)

	// Delete disk data directory.
	dataPath := filepath.Join(s.homeDir, "data", hash)
	if err := os.RemoveAll(dataPath); err != nil && !os.IsNotExist(err) {
		log.Warn().Err(err).Str("hash", hash).Msg("torrs: failed to remove data dir")
	} else if err == nil {
		log.Info().Str("hash", hash).Str("path", dataPath).Msg("torrs: removed data dir")
	}
}

// Drop stops a torrent but keeps it in the DB for later restoration.
func (s *BTServer) Drop(hash string) {
	hash = strings.ToLower(hash)

	s.mu.Lock()
	at, ok := s.active[hash]
	delete(s.active, hash)
	delete(s.speedSnaps, hash)
	s.mu.Unlock()

	if ok && at.T != nil {
		at.T.Drop()
	}
}

// Set updates torrent metadata.
func (s *BTServer) Set(hash, title, poster, data string) {
	hash = strings.ToLower(hash)

	s.mu.Lock()
	at := s.active[hash]
	if at != nil {
		if title != "" {
			at.DB.Title = title
		}
		if poster != "" {
			at.DB.Poster = poster
		}
		if data != "" {
			at.DB.Data = data
		}
	}
	s.mu.Unlock()

	if at != nil {
		_ = s.store.SaveTorrent(at.DB)
	}
}

// ---------------------------------------------------------------------------
//  Streaming
// ---------------------------------------------------------------------------

// Stream returns an io.ReadSeeker for the specified file in the torrent.
// fileIdx is 0-based. If fileIdx is out of range and the torrent has files,
// the largest file is returned (MatriX TorrServer fallback behavior).
func (s *BTServer) Stream(hash string, fileIdx int) (io.ReadSeeker, int64, string, error) {
	hash = strings.ToLower(hash)

	s.mu.RLock()
	at := s.active[hash]
	s.mu.RUnlock()

	if at == nil {
		return nil, 0, "", fmt.Errorf("torrent not found: %s", hash)
	}

	select {
	case <-at.T.GotInfo():
	case <-time.After(15 * time.Second):
		return nil, 0, "", fmt.Errorf("metadata timeout for %s", hash)
	}

	files := at.T.Files()
	if len(files) == 0 {
		return nil, 0, "", fmt.Errorf("torrent has no files: %s", hash)
	}

	// MatriX fallback: if index is out of range, pick the largest file.
	// This handles pidtor movie mode (default tsid=1) with single-file torrents
	// where valid index is 0, or multi-file torrents where tsid may exceed count.
	if fileIdx < 0 || fileIdx >= len(files) {
		best := 0
		for i, ff := range files {
			if ff.Length() > files[best].Length() {
				best = i
			}
		}
		fileIdx = best
	}

	f := files[fileIdx]
	// Whole-file download is a disk-cache behaviour: it prioritises every piece
	// so the file lands in <home>/data. In RAM mode that would pull a 40 GB
	// remux through a 2 GB cache — each piece evicted before it is ever read and
	// then re-requested. There the reader's own window (SetReadahead below) is
	// the only thing we want prioritised.
	if !s.ramMode() {
		f.Download()
	}

	// Track last access for disk cleanup. Persist asynchronously — disk-cleanup
	// ordering across restarts depends on this.
	now := time.Now()
	s.mu.Lock()
	if cur := s.active[hash]; cur != nil {
		cur.lastAccess = now
		cur.DB.LastAccess = now.Unix()
	}
	s.mu.Unlock()
	go s.persistLastAccess(hash, now.Unix())

	reader := f.NewReader()
	reader.SetReadahead(s.readahead(f.Length()))
	reader.SetResponsive()

	// Wrap so closing the reader (or client disconnect via http.ServeContent)
	// downgrades the file's piece priority — stops eager download once playback
	// ends.  anacrolix Reader implements io.Closer.
	rs := &cancelReader{ReadSeeker: reader, file: f}
	return rs, f.Length(), filepath.Base(f.DisplayPath()), nil
}

// persistLastAccess updates only the lastAccess field of a stored TorrentDB,
// avoiding clobbering concurrent Set() changes.
func (s *BTServer) persistLastAccess(hash string, ts int64) {
	cur, err := s.store.GetTorrent(hash)
	if err != nil {
		return // not persisted (saveToDB=false on Add)
	}
	if cur.LastAccess >= ts {
		return
	}
	cur.LastAccess = ts
	_ = s.store.SaveTorrent(*cur)
}

// cancelReader wraps a torrent file reader so that closing it drops the
// download priority — when the HTTP client disconnects we stop eager fetching.
type cancelReader struct {
	io.ReadSeeker
	file   *torrent.File
	closed atomic.Bool
}

func (c *cancelReader) Close() error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	if closer, ok := c.ReadSeeker.(io.Closer); ok {
		_ = closer.Close()
	}
	// Stop eager prefetch.  The file remains available for the next reader.
	c.file.SetPriority(torrent.PiecePriorityNone)
	return nil
}

// Preload triggers downloading the first N bytes of a file for buffering.
// fileIdx is 0-based after MatriX→internal conversion in the caller.
func (s *BTServer) Preload(hash string, fileIdx int) {
	hash = strings.ToLower(hash)

	s.mu.RLock()
	at := s.active[hash]
	s.mu.RUnlock()

	if at == nil {
		return
	}

	select {
	case <-at.T.GotInfo():
	default:
		return
	}

	files := at.T.Files()
	if len(files) == 0 {
		return
	}

	// MatriX fallback: if index is out of range, pick the largest file.
	if fileIdx < 0 || fileIdx >= len(files) {
		best := 0
		for i, ff := range files {
			if ff.Length() > files[best].Length() {
				best = i
			}
		}
		fileIdx = best
	}

	f := files[fileIdx]
	// Same as Stream: in RAM mode only the preload reader's window is fetched.
	if !s.ramMode() {
		f.Download()
	}

	now := time.Now()
	s.mu.Lock()
	if a := s.active[hash]; a != nil {
		a.lastAccess = now
		a.DB.LastAccess = now.Unix()
	}
	s.mu.Unlock()
	go s.persistLastAccess(hash, now.Unix())

	preloadSize := s.currentSettings().PreloadSize
	if preloadSize <= 0 {
		preloadSize = 5 << 20
	}

	// Background preload goroutine.
	go func() {
		reader := f.NewReader()
		defer reader.Close()
		reader.SetReadahead(preloadSize)
		reader.SetResponsive()

		buf := make([]byte, 32*1024)
		var total int64
		for total < preloadSize {
			n, err := reader.Read(buf)
			total += int64(n)
			if err != nil {
				break
			}
		}
		log.Debug().Str("hash", hash).Int64("bytes", total).Msg("torrs: preload complete")
	}()
}

// GetCacheStatus returns preload/peer statistics for a torrent.
func (s *BTServer) GetCacheStatus(hash string) CacheStatus {
	hash = strings.ToLower(hash)

	s.mu.RLock()
	at := s.active[hash]
	s.mu.RUnlock()

	if at == nil {
		return CacheStatus{}
	}

	select {
	case <-at.T.GotInfo():
	default:
		return CacheStatus{}
	}

	stats := at.T.Stats()
	preloadSize := s.currentSettings().PreloadSize
	if preloadSize <= 0 {
		preloadSize = 5 << 20
	}

	preloaded := at.T.BytesCompleted()
	if preloaded > preloadSize {
		preloaded = preloadSize
	}

	// Delta-based throughput (bytes/sec) for download + upload.
	bytesDown := stats.ConnStats.BytesReadUsefulData.Int64()
	bytesUp := stats.ConnStats.BytesWrittenData.Int64()
	now := time.Now()
	var dlSpeed, ulSpeed int64

	s.mu.Lock()
	if prev, ok := s.speedSnaps[hash]; ok {
		dt := now.Sub(prev.at).Seconds()
		if dt > 0.5 {
			if v := int64(float64(bytesDown-prev.bytesRead) / dt); v > 0 {
				dlSpeed = v
			}
			if v := int64(float64(bytesUp-prev.bytesWritten) / dt); v > 0 {
				ulSpeed = v
			}
			s.speedSnaps[hash] = speedSnap{
				bytesRead: bytesDown, bytesWritten: bytesUp,
				at: now, downloadSpeed: dlSpeed, uploadSpeed: ulSpeed,
			}
		} else {
			dlSpeed = prev.downloadSpeed
			ulSpeed = prev.uploadSpeed
		}
	} else {
		s.speedSnaps[hash] = speedSnap{bytesRead: bytesDown, bytesWritten: bytesUp, at: now}
	}
	s.mu.Unlock()

	return CacheStatus{
		Torrent: &CacheTorrentInfo{
			ActivePeers:      stats.ActivePeers,
			PendingPeers:     stats.PendingPeers,
			TotalPeers:       stats.TotalPeers,
			ConnectedSeeders: stats.ConnectedSeeders,
			PreloadedBytes:   preloaded,
			PreloadSize:      preloadSize,
			DownloadSpeed:    dlSpeed,
			UploadSpeed:      ulSpeed,
		},
	}
}

// ---------------------------------------------------------------------------
//  Settings
// ---------------------------------------------------------------------------

// GetSettings returns current settings.
func (s *BTServer) GetSettings() Settings { return s.currentSettings() }

// SetSettings updates and persists settings.  Logs a one-line warning when
// the caller attempts to set MatriX-compat shim fields that this server
// can't honor at runtime — useful for operators wondering why their CacheSize
// change had no effect.
func (s *BTServer) SetSettings(st Settings) error {
	prev := s.currentSettings()
	if st.CacheSize != prev.CacheSize && st.CacheSize > 0 {
		if s.ramMode() {
			// RAM mode: CacheSize is the real budget, so apply it live.
			s.ram.SetBudget(st.CacheSize)
			st.CacheSize = s.ram.Budget() // report back what was actually applied (floor)
		} else {
			log.Warn().Int64("requested_bytes", st.CacheSize).
				Msg("torrs: Settings.CacheSize is a MatriX-compat shim in disk mode, no piece-cache cap is applied")
		}
	}
	if st.TorrentsSavePath != prev.TorrentsSavePath && strings.TrimSpace(st.TorrentsSavePath) != "" {
		log.Warn().Str("requested", st.TorrentsSavePath).Str("active", filepath.Join(s.homeDir, "data")).
			Msg("torrs: Settings.TorrentsSavePath is fixed at startup; runtime change ignored")
	}
	if st.UseDisk != prev.UseDisk {
		log.Warn().Bool("requested", st.UseDisk).Bool("active_use_disk", !s.ramMode()).
			Msg("torrs: storage mode is fixed at startup by torrserver.ram_cache; change ignored")
	}
	// The active storage mode is not the caller's to flip.
	st.UseDisk = !s.ramMode()
	s.settings.Store(&st)
	return s.store.SaveSettings(st)
}

// sanitizeHostPrefix strips trailing slash and verifies hostPrefix parses
// as an http(s) URL prefix.  Returns "" on garbage so callers emit a m3u
// with relative URLs rather than splicing junk into the output.
func sanitizeHostPrefix(hostPrefix string) string {
	hostPrefix = strings.TrimRight(strings.TrimSpace(hostPrefix), "/")
	if hostPrefix == "" {
		return ""
	}
	u, err := url.Parse(hostPrefix)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return hostPrefix
}

// ---------------------------------------------------------------------------
//  M3U
// ---------------------------------------------------------------------------

// Playlist generates an M3U playlist for a single torrent.
func (s *BTServer) Playlist(hash, hostPrefix string) (string, error) {
	hash = strings.ToLower(hash)
	hostPrefix = sanitizeHostPrefix(hostPrefix)

	s.mu.RLock()
	at := s.active[hash]
	s.mu.RUnlock()

	if at == nil {
		return "", fmt.Errorf("torrent not found")
	}

	select {
	case <-at.T.GotInfo():
	case <-time.After(10 * time.Second):
		return "", fmt.Errorf("metadata timeout")
	}

	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	for i, f := range at.T.Files() {
		name := filepath.Base(f.DisplayPath())
		fmt.Fprintf(&b, "#EXTINF:-1,%s\n", name)
		fmt.Fprintf(&b, "%s/stream/%s?link=%s&index=%d&play\n",
			hostPrefix, url.PathEscape(name), hash, i+1) // 1-based (MatriX compat)
	}
	return b.String(), nil
}

// PlaylistByOwner generates an M3U playlist for active torrents owned by the
// given token. Empty token returns all torrents (single-user mode).
func (s *BTServer) PlaylistByOwner(owner, hostPrefix string) string {
	hostPrefix = sanitizeHostPrefix(hostPrefix)

	s.mu.RLock()
	defer s.mu.RUnlock()

	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	for hash, at := range s.active {
		if owner != "" && at.DB.Owner != "" && at.DB.Owner != owner {
			continue
		}
		// Hide ephemeral entries (saveToDB=false, e.g. pidtor probes) from /playlistall.
		if at.DB.Timestamp == 0 {
			continue
		}
		select {
		case <-at.T.GotInfo():
			for i, f := range at.T.Files() {
				name := filepath.Base(f.DisplayPath())
				fmt.Fprintf(&b, "#EXTINF:-1,[%s] %s\n", at.DB.Title, name)
				fmt.Fprintf(&b, "%s/stream/%s?link=%s&index=%d&play\n",
					hostPrefix, url.PathEscape(name), hash, i+1) // 1-based (MatriX compat)
			}
		default:
			// metadata not ready yet, skip
		}
	}
	return b.String()
}

// PlaylistAll is preserved for backwards compatibility; equivalent to
// PlaylistByOwner("", ...).
func (s *BTServer) PlaylistAll(hostPrefix string) string {
	return s.PlaylistByOwner("", hostPrefix)
}

// ---------------------------------------------------------------------------
//  Internals
// ---------------------------------------------------------------------------

func (s *BTServer) addLink(link string) (*torrent.Torrent, string, error) {
	link = strings.TrimSpace(link)
	if link == "" {
		return nil, "", fmt.Errorf("empty link")
	}

	// Magnet link.
	if strings.HasPrefix(link, "magnet:") {
		t, err := s.client.AddMagnet(link)
		if err != nil {
			return nil, "", fmt.Errorf("add magnet: %w", err)
		}
		t.AddTrackers(announceTiers())
		return t, strings.ToLower(t.InfoHash().HexString()), nil
	}

	// HTTP(S) URL to .torrent file.
	if strings.HasPrefix(link, "http://") || strings.HasPrefix(link, "https://") {
		return s.addFromURL(link)
	}

	// Raw info hash (40-char hex).
	if len(link) == 40 {
		magnet := "magnet:?xt=urn:btih:" + link
		t, err := s.client.AddMagnet(magnet)
		if err != nil {
			return nil, "", fmt.Errorf("add hash: %w", err)
		}
		t.AddTrackers(announceTiers())
		return t, strings.ToLower(t.InfoHash().HexString()), nil
	}

	return nil, "", fmt.Errorf("unsupported link format")
}

// safeTorrentClient is reused for .torrent fetches so connection pool keeps
// alive between requests; capped redirects + size + private-IP blocking.
var safeTorrentClient = &http.Client{
	Timeout: 30 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= addFromURLMaxRedirects {
			return errors.New("too many redirects")
		}
		if isPrivateHost(req.URL.Host) {
			return fmt.Errorf("redirect to private host blocked: %s", req.URL.Host)
		}
		return nil
	},
}

// isPrivateHost returns true if the host resolves to loopback / link-local /
// private network ranges.  Blocks SSRF via redirect.
func isPrivateHost(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
	}
	addrs, err := net.LookupIP(h)
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() {
			return true
		}
	}
	return false
}

func (s *BTServer) addFromURL(rawURL string) (*torrent.Torrent, string, error) {
	if parsed, err := url.Parse(rawURL); err == nil {
		if isPrivateHost(parsed.Host) {
			return nil, "", fmt.Errorf("private host blocked: %s", parsed.Host)
		}
	}

	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "lampac-go/torrs")
	req.Header.Set("Accept", "application/x-bittorrent, application/octet-stream")

	resp, err := safeTorrentClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download torrent: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("download torrent: HTTP %d", resp.StatusCode)
	}

	// Cap body size — malicious server could otherwise stream gigabytes
	// into metainfo.Load and OOM the process.
	body, err := io.ReadAll(io.LimitReader(resp.Body, addFromURLMaxBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("read torrent: %w", err)
	}
	if int64(len(body)) > addFromURLMaxBytes {
		return nil, "", fmt.Errorf("torrent file too large (>%d bytes)", addFromURLMaxBytes)
	}

	mi, err := metainfo.Load(bytes.NewReader(body))
	if err != nil {
		return nil, "", fmt.Errorf("parse torrent: %w", err)
	}

	t, err := s.client.AddTorrent(mi)
	if err != nil {
		return nil, "", fmt.Errorf("add torrent: %w", err)
	}
	t.AddTrackers(announceTiers())

	return t, strings.ToLower(t.InfoHash().HexString()), nil
}

// AddFromBytes adds a torrent from raw .torrent file bytes (no HTTP fetch needed).
// Useful when the .torrent file was already downloaded with custom auth.
func (s *BTServer) AddFromBytes(data []byte, title, poster, dataMeta string, saveToDB bool) (*TorrentInfo, error) {
	if int64(len(data)) > addFromURLMaxBytes {
		return nil, fmt.Errorf("torrent file too large (>%d bytes)", addFromURLMaxBytes)
	}
	mi, err := metainfo.Load(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("parse torrent bytes: %w", err)
	}

	t, err := s.client.AddTorrent(mi)
	if err != nil {
		return nil, fmt.Errorf("add torrent: %w", err)
	}
	t.AddTrackers(announceTiers())

	hash := strings.ToLower(t.InfoHash().HexString())

	s.mu.Lock()
	at, existed := s.active[hash]
	if existed {
		if title != "" {
			at.DB.Title = title
		}
		if poster != "" {
			at.DB.Poster = poster
		}
		if dataMeta != "" {
			at.DB.Data = dataMeta
		}
	} else {
		at = &ActiveTorrent{
			T: t,
			DB: TorrentDB{
				Hash:      hash,
				Title:     title,
				Poster:    poster,
				Data:      dataMeta,
				Magnet:    "magnet:?xt=urn:btih:" + hash,
				Timestamp: time.Now().Unix(),
			},
			lastAccess: time.Now(),
		}
		s.active[hash] = at
	}
	if saveToDB {
		at.DB.LastAccess = time.Now().Unix()
	}
	dbCopy := at.DB
	s.mu.Unlock()

	if saveToDB {
		if err := s.store.SaveTorrent(dbCopy); err != nil {
			log.Warn().Err(err).Str("hash", hash).Msg("torrs: save to db failed")
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), addMetadataTimeout)
	defer cancel()

	select {
	case <-t.GotInfo():
	case <-ctx.Done():
		if !existed {
			s.rollbackAdd(hash, saveToDB)
		}
		return nil, fmt.Errorf("metadata timeout for %s", hash)
	}

	s.enforceMaxActive(hash)
	return s.buildTorrentInfoLocked(hash, t, at.DB), nil
}

// restoreAll re-registers saved torrents on startup with bounded concurrency.
func (s *BTServer) restoreAll(saved []TorrentDB) {
	sem := make(chan struct{}, restoreConcurrency)
	var wg sync.WaitGroup
	for _, db := range saved {
		if db.Magnet == "" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(db TorrentDB) {
			defer wg.Done()
			defer func() { <-sem }()
			s.restoreTorrent(db)
		}(db)
	}
	wg.Wait()
}

func (s *BTServer) restoreTorrent(db TorrentDB) {
	t, hash, err := s.addLink(db.Magnet)
	if err != nil {
		log.Warn().Err(err).Str("hash", db.Hash).Msg("torrs: failed to restore torrent")
		return
	}

	lastAccess := time.Time{}
	if db.LastAccess > 0 {
		lastAccess = time.Unix(db.LastAccess, 0)
	}

	s.mu.Lock()
	if _, dup := s.active[hash]; !dup {
		s.active[hash] = &ActiveTorrent{T: t, DB: db, lastAccess: lastAccess}
	}
	s.mu.Unlock()

	log.Debug().Str("hash", hash).Str("title", db.Title).Msg("torrs: restored torrent")
}

// buildTorrentInfoLocked builds the API response. Safe to call with or without
// s.mu held — only reads from `t` and the provided `db` snapshot.
func (s *BTServer) buildTorrentInfoLocked(hash string, t *torrent.Torrent, db TorrentDB) *TorrentInfo {
	files := t.Files()
	stats := make([]FileStat, len(files))
	for i, f := range files {
		stats[i] = FileStat{
			ID:     i + 1, // 1-based (MatriX TorrServer convention)
			Path:   f.DisplayPath(),
			Length: f.Length(),
		}
	}

	info := &TorrentInfo{
		Title:          db.Title,
		Hash:           hash,
		Poster:         db.Poster,
		Data:           db.Data,
		Timestamp:      db.Timestamp,
		FileStats:      stats,
		TorrentSize:    t.Length(),
		BytesCompleted: t.BytesCompleted(),
		Stat:           StatWorking,
		StatString:     "Torrent working",
	}
	if mi := t.Metainfo(); mi.AnnounceList != nil || mi.Announce != "" {
		// no-op — just touch to ensure metainfo is fully loaded.
	}
	if t.Info() != nil {
		info.Name = t.Info().Name
	}
	return info
}

// ---------------------------------------------------------------------------
//  Health
// ---------------------------------------------------------------------------

// Health gathers a snapshot of server-wide metrics for /admin/api/torrs/health
// and the admin v2 live dashboard.  Cheap to call — no DHT walks or disk
// scans.  disk_usage_mb is the cached value from the last cleanup pass.
func (s *BTServer) Health() HealthInfo {
	now := time.Now()

	var torrents int
	var byteCompleted int64
	s.mu.RLock()
	torrents = len(s.active)
	for _, at := range s.active {
		// BytesCompleted reads atomic counters in anacrolix — safe without
		// info-ready check.
		byteCompleted += at.T.BytesCompleted()
	}
	s.mu.RUnlock()

	// Aggregate ClientStats over all torrents through the Client (it
	// surfaces a single ConnStats counter).
	stats := s.client.Stats()
	connStats := stats.ConnStats

	// DHT node count: extracted via reflection from each server's Stats()
	// because anacrolix' DhtServer interface only exposes the opaque
	// interface{} (concrete type is dht.ServerStats with a GoodNodes int).
	dhtNodes := dhtTotalGoodNodes(s.client.DhtServers())

	// Disk usage — quick pass; for huge dataDirs this could be 100s of ms,
	// but it runs only on /health hits (admin dashboard), not on streams.
	// Skipped in RAM mode: <home>/data stays empty there by construction.
	var totalBytes int64
	var ramUsedMB, ramBudgetMB, ramEvictions int64
	if s.ramMode() {
		ramUsedMB, ramBudgetMB, ramEvictions = s.ram.Stats()
	} else {
		dataDir := filepath.Join(s.homeDir, "data")
		totalBytes, _ = s.dataDirUsage(dataDir)
	}

	return HealthInfo{
		RAMCache:            s.ramMode(),
		RAMUsedMB:           ramUsedMB,
		RAMBudgetMB:         ramBudgetMB,
		RAMEvictions:        ramEvictions,
		NumTorrents:         torrents,
		BytesCompleted:      byteCompleted,
		BytesReadUseful:     connStats.BytesReadUsefulData.Int64(),
		BytesWritten:        connStats.BytesWrittenData.Int64(),
		DiskUsageMB:         totalBytes >> 20,
		DiskLimitMB:         int64(s.diskLimitMB()),
		DHTNodes:            dhtNodes,
		ListenPort:          s.client.LocalPort(),
		UptimeSec:           int64(now.Sub(s.startedAt).Seconds()),
		Goroutines:          runtime.NumGoroutine(),
		MaxActiveTorrents:   s.cfg.MaxActiveTorrents,
		MaxDownloadSpeedMBs: s.cfg.MaxDownloadSpeedMBs,
		MaxUploadSpeedMBs:   s.cfg.MaxUploadSpeedMBs,
		DHTEnabled:          !s.cfg.DisableDHT,
		UploadEnabled:       !s.cfg.DisableUpload,
		CleanupEnabled:      s.cleanupEnabled(),
		Now:                 now.Unix(),
	}
}
