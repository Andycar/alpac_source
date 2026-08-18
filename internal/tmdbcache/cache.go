// Package tmdbcache provides an in-memory response cache with TTL-based
// expiration and stale-while-revalidate semantics for TMDB API proxy.
package tmdbcache

import (
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

// cacheItem stores a cached upstream response.
type cacheItem struct {
	body    []byte
	headers map[string]string // Content-Type, Cache-Control, etc.
	status  int
	staleAt time.Time // after staleAt → serve stale + revalidate in background
	hardExp time.Time // after hardExp → entry is fully expired, must re-fetch
}

// maxBytes bounds the cache by RETAINED BYTES, not just entry count: 50k entries
// at 10-50KB TMDB JSON each is 0.5-2.5GB — a count cap alone let the cache absorb
// gigabytes on a busy server. 256MB comfortably holds ~10-25k typical responses.
const maxBytes = 256 << 20

// Cache is a thread-safe in-memory cache for TMDB API responses.
type Cache struct {
	mu       sync.RWMutex
	items    map[string]*cacheItem
	maxItems int

	// In-flight background refreshes — prevents duplicate revalidation.
	refreshing sync.Map // key → struct{}

	// Metrics (atomic).
	hits       atomic.Int64
	misses     atomic.Int64
	staleHits  atomic.Int64
	itemCount  atomic.Int64
	totalBytes atomic.Int64

	stopCh chan struct{}
}

// NewCache creates a cache with the given capacity.
// If maxItems <= 0, defaults to 50 000.
func NewCache(maxItems int) *Cache {
	if maxItems <= 0 {
		maxItems = 50_000
	}
	return &Cache{
		items:    make(map[string]*cacheItem, 1024),
		maxItems: maxItems,
		stopCh:   make(chan struct{}),
	}
}

// StartCleanup starts a background goroutine that evicts expired entries.
func (c *Cache) StartCleanup() {
	go c.cleanupLoop()
}

// Stop signals the cleanup goroutine to exit.
func (c *Cache) Stop() {
	select {
	case c.stopCh <- struct{}{}:
	default:
	}
}

// ---------- Cache key building ----------

// authStrip are query params that vary per client but don't change the response.
var authStrip = map[string]struct{}{
	"account_email": {},
	"uid":           {},
	"token":         {},
	"api_key":       {},
}

// CacheKey builds a canonical cache key from path + query params,
// stripping auth-related params that don't affect the response content.
func CacheKey(path string, query url.Values) string {
	// Collect non-auth params sorted by key.
	keys := make([]string, 0, len(query))
	for k := range query {
		if _, skip := authStrip[k]; skip {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString(path)
	for _, k := range keys {
		vals := query[k]
		sort.Strings(vals)
		for _, v := range vals {
			b.WriteByte('|')
			b.WriteString(k)
			b.WriteByte('=')
			b.WriteString(v)
		}
	}
	return b.String()
}

// ---------- TTL classification ----------

// ttlForPath returns (staleTTL, hardTTL) based on the request path pattern.
func ttlForPath(path string, baseTTLMin int) (stale, hard time.Duration) {
	base := time.Duration(baseTTLMin) * time.Minute
	if base <= 0 {
		base = 120 * time.Minute // 2 hours default
	}

	switch {
	case strings.Contains(path, "/configuration") ||
		strings.Contains(path, "/genre/"):
		// Nearly static data — cache 24h.
		stale = 24 * time.Hour
		hard = 72 * time.Hour

	case strings.Contains(path, "/search/") ||
		strings.Contains(path, "/discover/") ||
		strings.Contains(path, "/trending/"):
		// Dynamic lists — cache 30 min.
		stale = 30 * time.Minute
		hard = 90 * time.Minute

	default:
		// Detail endpoints (/movie/{id}, /tv/{id}, /person/{id}, etc.)
		stale = base
		hard = base * 3
	}
	return
}

// ---------- Get / Put ----------

// CacheResult describes a cache lookup outcome.
type CacheResult int

const (
	CacheMiss  CacheResult = iota // not in cache
	CacheHit                      // fresh entry
	CacheStale                    // stale but usable, revalidation needed
)

// Get looks up a cached response.
// Returns the result type and the cached item (nil on miss).
func (c *Cache) Get(key string) (CacheResult, []byte, map[string]string, int) {
	c.mu.RLock()
	item, ok := c.items[key]
	c.mu.RUnlock()
	if !ok {
		c.misses.Add(1)
		return CacheMiss, nil, nil, 0
	}

	now := time.Now()
	if now.After(item.hardExp) {
		// Fully expired — treat as miss.
		c.misses.Add(1)
		return CacheMiss, nil, nil, 0
	}

	if now.After(item.staleAt) {
		c.staleHits.Add(1)
		return CacheStale, item.body, item.headers, item.status
	}

	c.hits.Add(1)
	return CacheHit, item.body, item.headers, item.status
}

// Put stores a response in cache.
func (c *Cache) Put(key string, body []byte, headers map[string]string, status int, path string, baseTTLMin int) {
	stale, hard := ttlForPath(path, baseTTLMin)
	now := time.Now()

	c.mu.Lock()
	defer c.mu.Unlock()

	// Evict if at capacity (count OR retained bytes) — simple: remove oldest 10%.
	for i := 0; (len(c.items) >= c.maxItems || c.totalBytes.Load()+int64(len(body)) > maxBytes) && len(c.items) > 0 && i < 10; i++ {
		c.evictLocked()
	}

	old, existed := c.items[key]
	c.items[key] = &cacheItem{
		body:    body,
		headers: headers,
		status:  status,
		staleAt: now.Add(stale),
		hardExp: now.Add(hard),
	}

	if !existed {
		c.itemCount.Add(1)
		c.totalBytes.Add(int64(len(body)))
	} else {
		c.totalBytes.Add(int64(len(body) - len(old.body)))
	}
}

// MarkRefreshing tries to claim a background refresh slot for the key.
// Returns true if this caller should do the refresh.
func (c *Cache) MarkRefreshing(key string) bool {
	_, loaded := c.refreshing.LoadOrStore(key, struct{}{})
	return !loaded
}

// DoneRefreshing releases the refresh slot.
func (c *Cache) DoneRefreshing(key string) {
	c.refreshing.Delete(key)
}

// ---------- Eviction ----------

func (c *Cache) evictLocked() {
	// Remove the oldest 10% by staleAt.
	type kv struct {
		key     string
		staleAt time.Time
	}
	all := make([]kv, 0, len(c.items))
	for k, v := range c.items {
		all = append(all, kv{k, v.staleAt})
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].staleAt.Before(all[j].staleAt)
	})

	toRemove := max(len(all)/10, 1)
	for i := 0; i < toRemove && i < len(all); i++ {
		if item, ok := c.items[all[i].key]; ok {
			c.totalBytes.Add(-int64(len(item.body)))
			c.itemCount.Add(-1)
		}
		delete(c.items, all[i].key)
	}
}

// ---------- Cleanup loop ----------

func (c *Cache) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopCh:
			return
		case <-ticker.C:
			c.cleanup()
		}
	}
}

func (c *Cache) cleanup() {
	now := time.Now()
	var removed int

	c.mu.Lock()
	for key, item := range c.items {
		if now.After(item.hardExp) {
			c.totalBytes.Add(-int64(len(item.body)))
			c.itemCount.Add(-1)
			delete(c.items, key)
			removed++
		}
	}
	c.mu.Unlock()

	if removed > 0 {
		log.Debug().Int("removed", removed).Int64("remaining", c.itemCount.Load()).
			Msg("tmdbcache: cleanup complete")
	}
}

// ---------- Metrics ----------

// Stats returns cache statistics.
type Stats struct {
	Hits       int64   `json:"hits"`
	Misses     int64   `json:"misses"`
	StaleHits  int64   `json:"stale_hits"`
	ItemCount  int64   `json:"item_count"`
	TotalBytes int64   `json:"total_bytes"`
	HitRate    float64 `json:"hit_rate_pct"`
}

func (c *Cache) Stats() Stats {
	h := c.hits.Load()
	m := c.misses.Load()
	s := c.staleHits.Load()
	total := h + m + s
	var rate float64
	if total > 0 {
		rate = float64(h+s) / float64(total) * 100
	}
	return Stats{
		Hits:       h,
		Misses:     m,
		StaleHits:  s,
		ItemCount:  c.itemCount.Load(),
		TotalBytes: c.totalBytes.Load(),
		HitRate:    rate,
	}
}
