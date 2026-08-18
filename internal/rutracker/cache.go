package rutracker

import (
	"sync"
	"time"
)

// ttlCache caches listing results per normalized query. Only non-empty answers
// are stored: caching a miss would freeze a transient failure (a challenge, a
// dead session) for the whole TTL — the same trap pidtor documents at
// internal/httpapi/pidtor.go:275.
type ttlCache struct {
	mu sync.RWMutex
	m  map[string]ttlEntry
}

type ttlEntry struct {
	rows []Release
	exp  time.Time
}

func newTTLCache() *ttlCache { return &ttlCache{m: map[string]ttlEntry{}} }

func (c *ttlCache) get(key string) ([]Release, bool) {
	c.mu.RLock()
	e, ok := c.m[key]
	c.mu.RUnlock()
	if !ok || time.Now().After(e.exp) {
		return nil, false
	}
	return cloneRows(e.rows), true
}

func (c *ttlCache) put(key string, rows []Release, ttl time.Duration) {
	if len(rows) == 0 || ttl <= 0 {
		return
	}
	c.mu.Lock()
	// Cheap bound: the cache is a nicety, not a database.
	if len(c.m) > 512 {
		now := time.Now()
		for k, e := range c.m {
			if now.After(e.exp) {
				delete(c.m, k)
			}
		}
		if len(c.m) > 512 {
			c.m = map[string]ttlEntry{}
		}
	}
	c.m[key] = ttlEntry{rows: cloneRows(rows), exp: time.Now().Add(ttl)}
	c.mu.Unlock()
}

// updateMagnets back-fills magnets resolved after the entry was cached, so a
// lazily resolved release is not re-resolved on the next identical search.
func (c *ttlCache) updateMagnets(magnets map[int]string) {
	if len(magnets) == 0 {
		return
	}
	c.mu.Lock()
	for key, e := range c.m {
		changed := false
		for i := range e.rows {
			if e.rows[i].Magnet != "" {
				continue
			}
			if m := magnets[e.rows[i].TopicID]; m != "" {
				e.rows[i].Magnet = m
				changed = true
			}
		}
		if changed {
			c.m[key] = e
		}
	}
	c.mu.Unlock()
}

func (c *ttlCache) purge() {
	c.mu.Lock()
	c.m = map[string]ttlEntry{}
	c.mu.Unlock()
}

func (c *ttlCache) len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.m)
}

func cloneRows(in []Release) []Release {
	if in == nil {
		return nil
	}
	out := make([]Release, len(in))
	copy(out, in)
	return out
}
