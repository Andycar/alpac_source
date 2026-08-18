package wasmmodules

import (
	"sync"
	"time"
)

// cacheStore is a tiny per-module in-memory cache with TTL. Same shape as the
// jsmodules cache so plugins behave consistently across runtimes.
type cacheStore struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	value     any
	expiresAt time.Time
}

func newCacheStore() *cacheStore {
	return &cacheStore{entries: make(map[string]cacheEntry)}
}

func (c *cacheStore) Get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if time.Now().After(e.expiresAt) {
		delete(c.entries, key)
		return nil, false
	}
	return e.value, true
}

func (c *cacheStore) Set(key string, value any, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = cacheEntry{value: value, expiresAt: time.Now().Add(ttl)}
}

func (c *cacheStore) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}

func (c *cacheStore) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]cacheEntry)
}
