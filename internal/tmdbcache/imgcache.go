package tmdbcache

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// ImageCache is a bounded, on-DISK LRU cache for TMDB poster/backdrop images.
//
// Server-side image caching was originally avoided to keep RSS low (see the
// comment in httpapi/tmdb_proxy.go). This cache lives on DISK — images stream to
// and from files, never held in the heap — so RSS stays flat while repeat fetches
// for the same poster (very common: many clients request the same popular title)
// are served locally instead of re-hitting the slow/geo-blocked upstream CDN that
// caused the "everything got slow" incident. TMDB image paths are content-addressed
// (the filename is a content hash) → immutable, so no TTL is needed: eviction is
// purely size-bounded LRU. A nil *ImageCache is a disabled no-op.
type ImageCache struct {
	dir      string
	maxBytes int64

	mu      sync.Mutex
	size    int64
	entries map[string]*imgEntry // hashed-key → metadata
}

type imgEntry struct {
	size    int64
	lastUse time.Time
}

// NewImageCache opens (creating if needed) an on-disk image cache under dir with a
// maxMB size budget. Returns nil (disabled) when dir is empty or maxMB <= 0.
func NewImageCache(dir string, maxMB int) *ImageCache {
	if dir == "" || maxMB <= 0 {
		return nil
	}
	if os.MkdirAll(dir, 0o755) != nil {
		return nil
	}
	c := &ImageCache{
		dir:      dir,
		maxBytes: int64(maxMB) << 20,
		entries:  make(map[string]*imgEntry),
	}
	c.scan()
	return c
}

func (c *ImageCache) keyFile(imgPath string) string {
	h := sha256.Sum256([]byte(imgPath))
	return hex.EncodeToString(h[:])
}

// scan rebuilds the in-memory index + size accounting from whatever is already on
// disk (survives restarts). Skips *.tmp partials.
func (c *ImageCache) scan() {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) == ".tmp" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		c.entries[e.Name()] = &imgEntry{size: info.Size(), lastUse: info.ModTime()}
		c.size += info.Size()
	}
}

// Get returns the cached bytes for an image path, or nil on miss. A hit refreshes
// the entry's LRU recency.
func (c *ImageCache) Get(imgPath string) []byte {
	if c == nil {
		return nil
	}
	name := c.keyFile(imgPath)
	c.mu.Lock()
	e, ok := c.entries[name]
	c.mu.Unlock()
	if !ok {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(c.dir, name))
	if err != nil {
		// File vanished (external cleaner?) — drop the stale index entry.
		c.mu.Lock()
		if cur := c.entries[name]; cur != nil {
			c.size -= cur.size
			delete(c.entries, name)
		}
		c.mu.Unlock()
		return nil
	}
	now := time.Now()
	c.mu.Lock()
	e.lastUse = now
	c.mu.Unlock()
	_ = os.Chtimes(filepath.Join(c.dir, name), now, now)
	return b
}

// Put stores image bytes, then evicts least-recently-used entries if over budget.
// No-op for empty bodies or single images larger than a quarter of the budget
// (one giant file shouldn't be able to churn the whole cache).
func (c *ImageCache) Put(imgPath string, body []byte) {
	if c == nil || len(body) == 0 {
		return
	}
	if int64(len(body)) > c.maxBytes/4 {
		return
	}
	name := c.keyFile(imgPath)
	full := filepath.Join(c.dir, name)
	tmp := full + ".tmp"
	if os.WriteFile(tmp, body, 0o644) != nil {
		return
	}
	if os.Rename(tmp, full) != nil {
		_ = os.Remove(tmp)
		return
	}
	c.mu.Lock()
	if old, ok := c.entries[name]; ok {
		c.size -= old.size
	}
	c.entries[name] = &imgEntry{size: int64(len(body)), lastUse: time.Now()}
	c.size += int64(len(body))
	c.evictLocked()
	c.mu.Unlock()
}

// evictLocked removes the oldest entries until the cache is back under 90% of the
// budget. Caller must hold c.mu.
func (c *ImageCache) evictLocked() {
	if c.size <= c.maxBytes {
		return
	}
	type kv struct {
		name    string
		lastUse time.Time
	}
	all := make([]kv, 0, len(c.entries))
	for n, e := range c.entries {
		all = append(all, kv{n, e.lastUse})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].lastUse.Before(all[j].lastUse) })

	target := c.maxBytes * 9 / 10
	for _, item := range all {
		if c.size <= target {
			break
		}
		if e, ok := c.entries[item.name]; ok {
			_ = os.Remove(filepath.Join(c.dir, item.name))
			c.size -= e.size
			delete(c.entries, item.name)
		}
	}
}

// Stats reports cache utilization for the admin TMDB stats endpoint.
func (c *ImageCache) Stats() map[string]any {
	if c == nil {
		return map[string]any{"enabled": false}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return map[string]any{
		"enabled":    true,
		"entries":    len(c.entries),
		"size_bytes": c.size,
		"max_bytes":  c.maxBytes,
	}
}
