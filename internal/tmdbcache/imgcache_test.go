package tmdbcache

import (
	"testing"
	"time"
)

func TestImageCache_PutGetMiss(t *testing.T) {
	c := NewImageCache(t.TempDir(), 10)
	if c == nil {
		t.Fatal("expected an enabled cache")
	}
	c.Put("/t/p/w300/a.jpg", []byte("poster-a"))
	if got := c.Get("/t/p/w300/a.jpg"); string(got) != "poster-a" {
		t.Fatalf("Get = %q, want poster-a", got)
	}
	if got := c.Get("/t/p/w300/nope.jpg"); got != nil {
		t.Fatalf("miss should be nil, got %q", got)
	}
}

func TestImageCache_Disabled(t *testing.T) {
	if NewImageCache("", 10) != nil {
		t.Fatal("empty dir should disable (nil)")
	}
	if NewImageCache(t.TempDir(), 0) != nil {
		t.Fatal("0 MB should disable (nil)")
	}
	var c *ImageCache // nil receiver must be a safe no-op
	c.Put("x", []byte("y"))
	if c.Get("x") != nil {
		t.Fatal("nil cache Get should be nil")
	}
}

func TestImageCache_EvictsLRUUnderBudget(t *testing.T) {
	c := NewImageCache(t.TempDir(), 1) // 1 MiB budget
	blob := make([]byte, 200*1024)     // 200 KiB (< maxBytes/4 = 256 KiB, so accepted)

	// 6 × 200 KiB = 1.2 MiB > 1 MiB → eviction down to <=90% (≈0.9 MiB).
	for _, k := range []string{"a", "b", "c", "d", "e", "f"} {
		c.Put(k, blob)
		time.Sleep(2 * time.Millisecond) // distinct lastUse so LRU order is deterministic
	}

	c.mu.Lock()
	size, n := c.size, len(c.entries)
	c.mu.Unlock()
	if size > c.maxBytes {
		t.Fatalf("size %d exceeds budget %d after eviction", size, c.maxBytes)
	}
	if n >= 6 {
		t.Fatalf("expected some eviction, still have %d entries", n)
	}
	// The oldest ("a") should be gone; the newest ("f") should remain.
	if c.Get("a") != nil {
		t.Error("LRU victim 'a' should have been evicted")
	}
	if c.Get("f") == nil {
		t.Error("most-recent 'f' should still be cached")
	}
}

func TestImageCache_PersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	c1 := NewImageCache(dir, 10)
	c1.Put("/t/p/w780/keep.jpg", []byte("survives-restart"))

	// A fresh cache over the same dir must rebuild its index from disk.
	c2 := NewImageCache(dir, 10)
	if got := c2.Get("/t/p/w780/keep.jpg"); string(got) != "survives-restart" {
		t.Fatalf("after reopen Get = %q, want survives-restart", got)
	}
}
