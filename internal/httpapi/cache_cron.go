package httpapi

import (
	"os"
	"path/filepath"
	"sort"
	"sync/atomic"
	"time"

	"lampac-go/internal/config"

	"github.com/rs/zerolog/log"
)

// relToRuntimeFunc is a test hook for path resolution.
// In production it calls the real relToRuntime; tests may override it.
var relToRuntimeFunc = relToRuntime

// CacheCron periodically removes expired cache files based on TTL rules.
// Mirrors .NET Lampac.Engine.CRON.CacheCron.
type CacheCron struct {
	cfg     config.Config
	running int32
	stop    chan struct{}
}

// NewCacheCron creates a new CacheCron instance.
func NewCacheCron(cfg config.Config) *CacheCron {
	return &CacheCron{
		cfg:  cfg,
		stop: make(chan struct{}),
	}
}

// Start begins the background cache cleanup loop.
// Initial delay: 2 minutes; interval: 5 minutes (same as .NET).
func (cc *CacheCron) Start() {
	log.Info().Msg("cache-cron: starting")
	go cc.loop()
}

// Stop signals the background loop to exit.
func (cc *CacheCron) Stop() {
	select {
	case cc.stop <- struct{}{}:
	default:
	}
}

func (cc *CacheCron) loop() {
	timer := time.NewTimer(2 * time.Minute)
	defer timer.Stop()

	for {
		select {
		case <-cc.stop:
			return
		case <-timer.C:
			cc.run()
			timer.Reset(5 * time.Minute)
		}
	}
}

// cacheEntry describes a directory to clean and its TTL in minutes.
type cacheEntry struct {
	path   string // relative to cache root
	ttlMin int    // -1 = skip, 0 = delete all, >0 = TTL in minutes
}

func (cc *CacheCron) run() {
	if !atomic.CompareAndSwapInt32(&cc.running, 0, 1) {
		return
	}
	defer atomic.StoreInt32(&cc.running, 0)

	cacheRoot := relToRuntimeFunc("cache")
	if _, err := os.Stat(cacheRoot); os.IsNotExist(err) {
		return // no cache directory at all
	}

	fcfg := cc.cfg.FileCacheInactive
	imgTime := cc.cfg.ServerProxy.Image.CacheTime
	if imgTime <= 0 {
		imgTime = 60
	}

	entries := []cacheEntry{
		{"tmdb", imgTime},
		{"cub", imgTime},
		{"img", imgTime},
		{"torrent", fcfg.Torrent},
		{"html", fcfg.HTML},
		{"hls", fcfg.HLS},
		{filepath.Join("storage", "temp"), 10}, // hardcoded 10 min like .NET
	}

	type fileEntry struct {
		path    string
		modTime time.Time
		size    int64
	}

	var lowDiskFiles []fileEntry
	now := time.Now().UTC()
	var totalDeleted int

	for _, e := range entries {
		if e.ttlMin == -1 {
			continue
		}

		dir := filepath.Join(cacheRoot, e.path)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}

		cutoff := now.Add(-time.Duration(e.ttlMin) * time.Minute)

		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}

			if e.ttlMin == 0 {
				_ = os.Remove(path)
				totalDeleted++
				return nil
			}

			modTime := info.ModTime().UTC()
			if modTime.Before(cutoff) {
				_ = os.Remove(path)
				totalDeleted++
			} else {
				// Collect for potential emergency disk-space cleanup.
				lowDiskFiles = append(lowDiskFiles, fileEntry{
					path: path, modTime: modTime, size: info.Size(),
				})
			}
			return nil
		})
	}

	// Emergency disk-space cleanup: if free space < threshold, delete oldest
	// files until we've freed 2 GB (matches .NET behavior).
	if fcfg.FreeDiskSpace > 0 && len(lowDiskFiles) > 0 {
		free, err := freeDiskSpaceBytes(cacheRoot)
		if err == nil && free >= 0 && free < fcfg.FreeDiskSpace {
			log.Warn().
				Int64("free_bytes", free).
				Int64("threshold", fcfg.FreeDiskSpace).
				Msg("cache-cron: disk space low, emergency cleanup")

			sort.Slice(lowDiskFiles, func(i, j int) bool {
				return lowDiskFiles[i].modTime.Before(lowDiskFiles[j].modTime)
			})

			var removed int64
			const maxRemove = 2 << 30 // 2 GB
			for _, f := range lowDiskFiles {
				if removed >= maxRemove {
					break
				}
				if err := os.Remove(f.path); err == nil {
					removed += f.size
					totalDeleted++
				}
			}

			log.Info().
				Int64("freed_bytes", removed).
				Msg("cache-cron: emergency cleanup done")
		}
	}

	// Remove empty subdirectories left after file deletion.
	for _, e := range entries {
		dir := filepath.Join(cacheRoot, e.path)
		removeEmptyDirs(dir)
	}

	if totalDeleted > 0 {
		log.Info().Int("deleted", totalDeleted).Msg("cache-cron: cleanup complete")
	}
}

// removeEmptyDirs removes empty subdirectories bottom-up.
func removeEmptyDirs(root string) {
	var dirs []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil && info.IsDir() && path != root {
			dirs = append(dirs, path)
		}
		return nil
	})

	// Process deepest directories first.
	for i := len(dirs) - 1; i >= 0; i-- {
		entries, err := os.ReadDir(dirs[i])
		if err == nil && len(entries) == 0 {
			_ = os.Remove(dirs[i])
		}
	}
}

// freeDiskSpaceBytes is defined per-OS in diskfree_unix.go / diskfree_windows.go.
