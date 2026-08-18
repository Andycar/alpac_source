package proxyapi

import (
	"testing"
	"time"
)

// resetM3UCache clears the shared package-level cache so tests don't contaminate
// each other (the cache + its byte counter are global).
func resetM3UCache() {
	m3uCache.Lock()
	m3uCache.items = make(map[string]m3uCacheEntry, 512)
	m3uCache.totalBytes = 0
	m3uCache.Unlock()
}

func body(n int) []byte { return make([]byte, n) }

// A normal-sized entry is stored and totalBytes tracks it exactly.
func TestM3UCache_StoreAndByteCount(t *testing.T) {
	resetM3UCache()
	m3uCacheSetTTL("k1", m3uCacheEntry{body: body(1000)}, time.Minute)
	if _, ok := m3uCacheGet("k1"); !ok {
		t.Fatal("k1 should be cached")
	}
	if got := m3uCache.totalBytes; got != 1000 {
		t.Fatalf("totalBytes = %d, want 1000", got)
	}
}

// Overwriting a key must replace (not double-count) its bytes.
func TestM3UCache_OverwriteKeepsBytesConsistent(t *testing.T) {
	resetM3UCache()
	m3uCacheSetTTL("k", m3uCacheEntry{body: body(1000)}, time.Minute)
	m3uCacheSetTTL("k", m3uCacheEntry{body: body(3000)}, time.Minute)
	if got := m3uCache.totalBytes; got != 3000 {
		t.Fatalf("totalBytes after overwrite = %d, want 3000 (old 1000 must be subtracted)", got)
	}
	if len(m3uCache.items) != 1 {
		t.Fatalf("items = %d, want 1", len(m3uCache.items))
	}
}

// Entries larger than the per-entry cap are served but NOT retained — this is the
// core fix: a 10-15MB rewritten VOD/catchup manifest must never sit in the cache.
func TestM3UCache_RejectsOversizeEntry(t *testing.T) {
	resetM3UCache()
	m3uCacheSetTTL("huge", m3uCacheEntry{body: body(m3uCacheMaxEntryBytes + 1)}, time.Minute)
	if _, ok := m3uCacheGet("huge"); ok {
		t.Fatal("oversize entry must not be cached")
	}
	if m3uCache.totalBytes != 0 {
		t.Fatalf("totalBytes = %d, want 0 (oversize entry not counted)", m3uCache.totalBytes)
	}
}

// Exceeding the byte budget triggers a full clear (then the new entry lands).
func TestM3UCache_ByteBudgetClears(t *testing.T) {
	resetM3UCache()
	// Fill with near-cap entries until just under the byte budget.
	per := 400 << 10 // 400KB each (under the 512KB per-entry cap)
	n := m3uCacheMaxBytes / per
	for i := 0; i < n; i++ {
		m3uCacheSetTTL(key(i), m3uCacheEntry{body: body(per)}, time.Minute)
	}
	if m3uCache.totalBytes > m3uCacheMaxBytes {
		t.Fatalf("totalBytes %d exceeded budget %d before the trigger", m3uCache.totalBytes, m3uCacheMaxBytes)
	}
	before := len(m3uCache.items)
	// One more push over the budget → clear-all, then store the newcomer alone.
	m3uCacheSetTTL("overflow", m3uCacheEntry{body: body(per)}, time.Minute)
	if len(m3uCache.items) >= before {
		t.Fatalf("budget overflow should have cleared the cache (items %d, before %d)", len(m3uCache.items), before)
	}
	if _, ok := m3uCacheGet("overflow"); !ok {
		t.Fatal("the newcomer that tripped the budget must still be stored")
	}
	// totalBytes must equal exactly the surviving entries' bytes (no drift).
	var sum int64
	m3uCache.RLock()
	for _, e := range m3uCache.items {
		sum += int64(len(e.body))
	}
	m3uCache.RUnlock()
	if sum != m3uCache.totalBytes {
		t.Fatalf("totalBytes %d != sum of bodies %d (accounting drift)", m3uCache.totalBytes, sum)
	}
}

// At the byte-cap eviction path, expired entries must be swept AND their bytes
// subtracted (the cap sweep deletes by TTL before deciding whether to clear-all).
func TestM3UCache_ExpiredEvictionDecrementsBytes(t *testing.T) {
	resetM3UCache()
	per := 400 << 10 // 400KB, under the per-entry cap
	n := m3uCacheMaxBytes / per
	// Fill near budget; mark the first half as already-expired (rewrite expiresAt directly).
	for i := 0; i < n; i++ {
		m3uCacheSetTTL(key(i), m3uCacheEntry{body: body(per)}, time.Minute)
	}
	m3uCache.Lock()
	expiredCount := 0
	for k, e := range m3uCache.items {
		if expiredCount < n/2 {
			e.expiresAt = time.Now().Add(-time.Hour)
			m3uCache.items[k] = e
			expiredCount++
		}
	}
	m3uCache.Unlock()

	// One more legal (under-cap) set trips the byte budget → the eviction branch
	// sweeps the expired half and decrements their bytes.
	m3uCacheSetTTL("trigger", m3uCacheEntry{body: body(per)}, time.Minute)

	var sum int64
	m3uCache.RLock()
	for _, e := range m3uCache.items {
		sum += int64(len(e.body))
	}
	m3uCache.RUnlock()
	if sum != m3uCache.totalBytes {
		t.Fatalf("totalBytes %d != sum of bodies %d after expired eviction (accounting drift)", m3uCache.totalBytes, sum)
	}
	if m3uCache.totalBytes > m3uCacheMaxBytes {
		t.Fatalf("totalBytes %d still over budget %d after eviction", m3uCache.totalBytes, m3uCacheMaxBytes)
	}
}

func key(i int) string {
	return "k" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + string(rune('0'+(i/676)%10))
}
