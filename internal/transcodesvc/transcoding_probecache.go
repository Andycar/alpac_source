package transcodesvc

import (
	"container/list"
	"crypto/sha1"
	"encoding/hex"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

// ---------------------------------------------------------------------------
// Probe cache — in-memory LRU + disk-backed JSON files.
//
// Problem: clicking the same MKV twice re-probes it every time.  For
// torrent sources that means re-reading the first 20 MB of the file
// from the swarm, which takes multiple seconds and burns bandwidth.
//
// Solution: keep a keyed cache of ffprobe JSON results.  Key is derived
// from the source URL plus best-effort HTTP HEAD metadata (Content-Length,
// ETag, Last-Modified) so unrelated movies don't collide and updates to
// the same URL invalidate naturally.
//
// Memory layer: O(1) LRU with a configurable capacity (default 512).
// Disk layer: JSON file per entry under {TempRoot}/probe_cache/, evicted
// together with the in-memory entry and re-read on the next cold boot.
//
// TTL: 7 days on disk, 2 hours in memory.  Mismatches with the disk
// TTL simply result in a stale disk hit being promoted into memory;
// the HTTP HEAD fingerprint protects against true staleness.
// ---------------------------------------------------------------------------

const (
	probeCacheMemCap    = 512
	probeCacheMemTTL    = 2 * time.Hour
	probeCacheDiskTTL   = 7 * 24 * time.Hour
	probeHeadTimeout    = 2 * time.Second
	probeCacheDirName   = "probe_cache"
	probeCacheFileExt   = ".json"
	probeCacheWriteDebo = 0 // write-through (simple & fine for our volume)
)

// probeCacheEntry holds a successful probe result plus its freshness metadata.
type probeCacheEntry struct {
	Key         string         `json:"key"`
	Src         string         `json:"src"`
	Probe       map[string]any `json:"probe"`
	Fingerprint string         `json:"fingerprint"`
	StoredAt    time.Time      `json:"stored_at"`
}

// probeCache is the small in-memory LRU with an opt-in disk overlay.
type probeCache struct {
	mu     sync.Mutex
	items  map[string]*list.Element // key → *list.Element{*probeCacheEntry}
	order  *list.List               // LRU order
	dir    string                   // disk path, "" disables disk overlay
	hits   int64                    // atomic counter
	misses int64                    // atomic counter
}

func newProbeCache(tempRoot string) *probeCache {
	dir := ""
	if tempRoot != "" {
		dir = filepath.Join(tempRoot, probeCacheDirName)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Warn().Err(err).Str("dir", dir).Msg("probe cache: failed to create dir, disk overlay disabled")
			dir = ""
		}
	}
	return &probeCache{
		items: make(map[string]*list.Element, probeCacheMemCap),
		order: list.New(),
		dir:   dir,
	}
}

// Stats returns counter snapshot for the admin panel.
func (c *probeCache) Stats() map[string]any {
	if c == nil {
		return map[string]any{"enabled": false}
	}
	c.mu.Lock()
	size := len(c.items)
	c.mu.Unlock()
	return map[string]any{
		"enabled": true,
		"size":    size,
		"hits":    atomic.LoadInt64(&c.hits),
		"misses":  atomic.LoadInt64(&c.misses),
	}
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// Get returns the cached probe for (src, headers) if fresh.  The headers
// map carries the same userAgent/referer that would go into runFFProbe,
// so disk entries survive across server restarts.
func (c *probeCache) Get(src string, headers map[string]string) (map[string]any, bool) {
	if c == nil || strings.TrimSpace(src) == "" {
		return nil, false
	}

	fingerprint := probeFingerprint(src, headers)
	key := probeCacheKey(src, fingerprint)

	c.mu.Lock()
	if el, ok := c.items[key]; ok {
		ent := el.Value.(*probeCacheEntry)
		if time.Since(ent.StoredAt) <= probeCacheMemTTL && ent.Fingerprint == fingerprint {
			c.order.MoveToFront(el)
			c.mu.Unlock()
			atomic.AddInt64(&c.hits, 1)
			log.Debug().Str("src", shortSrc(src)).Msg("probe cache: memory hit")
			return ent.Probe, true
		}
		// Fingerprint drift or expired → drop and fall through to disk.
		c.order.Remove(el)
		delete(c.items, key)
	}
	c.mu.Unlock()

	// Disk overlay.
	if c.dir != "" {
		if ent, ok := c.loadDisk(key); ok && ent.Fingerprint == fingerprint &&
			time.Since(ent.StoredAt) <= probeCacheDiskTTL {
			// Promote into memory.
			c.putMem(key, ent)
			atomic.AddInt64(&c.hits, 1)
			log.Debug().Str("src", shortSrc(src)).Msg("probe cache: disk hit")
			return ent.Probe, true
		}
	}

	atomic.AddInt64(&c.misses, 1)
	return nil, false
}

// Put stores a successful probe result in memory and (best-effort) on disk.
func (c *probeCache) Put(src string, headers map[string]string, probe map[string]any) {
	if c == nil || probe == nil || strings.TrimSpace(src) == "" {
		return
	}
	fingerprint := probeFingerprint(src, headers)
	key := probeCacheKey(src, fingerprint)
	ent := &probeCacheEntry{
		Key:         key,
		Src:         src,
		Probe:       probe,
		Fingerprint: fingerprint,
		StoredAt:    time.Now().UTC(),
	}
	c.putMem(key, ent)
	if c.dir != "" {
		c.storeDisk(key, ent)
	}
}

// Clear drops all cached entries (memory + disk).  Used by admin UI.
func (c *probeCache) Clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.items = make(map[string]*list.Element, probeCacheMemCap)
	c.order.Init()
	c.mu.Unlock()
	if c.dir != "" {
		entries, _ := os.ReadDir(c.dir)
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), probeCacheFileExt) {
				_ = os.Remove(filepath.Join(c.dir, e.Name()))
			}
		}
	}
	atomic.StoreInt64(&c.hits, 0)
	atomic.StoreInt64(&c.misses, 0)
}

// ---------------------------------------------------------------------------
// Internal
// ---------------------------------------------------------------------------

func (c *probeCache) putMem(key string, ent *probeCacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		el.Value = ent
		c.order.MoveToFront(el)
		return
	}
	el := c.order.PushFront(ent)
	c.items[key] = el
	// Evict LRU tail.
	for c.order.Len() > probeCacheMemCap {
		tail := c.order.Back()
		if tail == nil {
			break
		}
		c.order.Remove(tail)
		if old, ok := tail.Value.(*probeCacheEntry); ok {
			delete(c.items, old.Key)
		}
	}
}

func (c *probeCache) loadDisk(key string) (*probeCacheEntry, bool) {
	path := filepath.Join(c.dir, key+probeCacheFileExt)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var ent probeCacheEntry
	if err := json.Unmarshal(data, &ent); err != nil {
		_ = os.Remove(path)
		return nil, false
	}
	return &ent, true
}

func (c *probeCache) storeDisk(key string, ent *probeCacheEntry) {
	path := filepath.Join(c.dir, key+probeCacheFileExt)
	data, err := json.Marshal(ent)
	if err != nil {
		return
	}
	// Write atomically via temp file so a crash mid-write never leaves a
	// truncated JSON that poisons the next cold boot.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// probeCacheKey returns a stable file-safe cache key.
func probeCacheKey(src, fingerprint string) string {
	h := sha1.Sum([]byte(src + "|" + fingerprint))
	return hex.EncodeToString(h[:])
}

// probeFingerprint is a best-effort source freshness tag derived from
// HTTP HEAD (Content-Length + ETag + Last-Modified).  When HEAD fails or
// the URL is a pidtor/torrent handle we return a stable synthetic token
// based on the URL itself — torrent sources are immutable once hashed.
func probeFingerprint(src string, headers map[string]string) string {
	// Torrent sources: the infohash in the URL IS the fingerprint.
	if strings.Contains(src, "/lite/pidtor/s") {
		if idx := strings.Index(src, "/lite/pidtor/s"); idx >= 0 {
			tail := src[idx+len("/lite/pidtor/s"):]
			if end := strings.IndexAny(tail, "/?#"); end > 0 {
				return "pidtor:" + tail[:end]
			}
			return "pidtor:" + tail
		}
	}
	if strings.Contains(src, "/stream") && strings.Contains(src, "link=") {
		if parsed, err := url.Parse(src); err == nil {
			hash := parsed.Query().Get("link")
			idx := parsed.Query().Get("index")
			if hash != "" {
				return "torrs:" + hash + ":" + idx
			}
		}
	}

	// HTTP/HTTPS — issue a HEAD and hash the size/etag/last-modified.
	req, err := http.NewRequest(http.MethodHead, src, nil)
	if err != nil {
		return "url:" + src
	}
	if ua := getHeader(headers, "userAgent"); ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	// Forward a sanitized Referer — strip query string and fragment so an
	// upstream CDN only sees scheme://host[/path], not user-controlled
	// tokens that may have ridden along in a ?token=... URL.
	if ref := sanitizeForwardedReferer(getHeader(headers, "referer")); ref != "" {
		req.Header.Set("Referer", ref)
	}

	client := &http.Client{Timeout: probeHeadTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "url:" + src
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "url:" + src
	}

	parts := []string{resp.Header.Get("Content-Length"), resp.Header.Get("ETag"), resp.Header.Get("Last-Modified")}
	return "http:" + strings.Join(parts, "|")
}

// shortSrc returns a truncated source URL for log lines.
func shortSrc(src string) string {
	if len(src) > 80 {
		return src[:77] + "..."
	}
	return src
}
