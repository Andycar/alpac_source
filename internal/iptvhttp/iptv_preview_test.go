package iptvhttp

import (
	"bytes"
	"strconv"
	"testing"
	"time"
)

// cappedWriter must stop buffering past max yet always report full consumption
// (so ffmpeg keeps writing/exits cleanly instead of erroring on a short write).
func TestCappedWriter(t *testing.T) {
	var buf bytes.Buffer
	w := &cappedWriter{buf: &buf, max: 10}

	n, err := w.Write([]byte("hello"))
	if err != nil || n != 5 {
		t.Fatalf("first write: n=%d err=%v", n, err)
	}
	// This pushes past the 10-byte cap: only 5 more bytes may be buffered, but Write reports all 8.
	n, err = w.Write([]byte("WORLDxyz"))
	if err != nil || n != 8 {
		t.Fatalf("second write should claim full consumption: n=%d err=%v", n, err)
	}
	if buf.Len() != 10 {
		t.Fatalf("buffer should be capped at 10, got %d", buf.Len())
	}
	if got := buf.String(); got != "helloWORLD" {
		t.Fatalf("unexpected buffered content %q", got)
	}

	// Once full, further writes are dropped but still acknowledged.
	n, _ = w.Write([]byte("more"))
	if n != 4 || buf.Len() != 10 {
		t.Fatalf("post-cap write: n=%d len=%d", n, buf.Len())
	}
}

// evictLocked must drop exactly the OLDEST entry once over the cap, keeping the
// cache bounded.
func TestPreviewEvictDropsOldest(t *testing.T) {
	c := newIPTVPreviewCache("ffmpeg", 3, 600)

	base := time.Now()
	// Fill to the cap with strictly increasing timestamps.
	for i := 0; i < iptvPreviewMaxEntries; i++ {
		c.entries[key(i)] = &iptvPreviewEntry{data: []byte{1}, ts: base.Add(time.Duration(i) * time.Second)}
	}
	// One past the cap → the oldest (index 0) is the eviction candidate.
	c.entries[key(iptvPreviewMaxEntries)] = &iptvPreviewEntry{data: []byte{1}, ts: base.Add(time.Duration(iptvPreviewMaxEntries) * time.Second)}

	c.evictLocked()

	if len(c.entries) != iptvPreviewMaxEntries {
		t.Fatalf("expected %d entries after eviction, got %d", iptvPreviewMaxEntries, len(c.entries))
	}
	if _, ok := c.entries[key(0)]; ok {
		t.Fatalf("oldest entry should have been evicted")
	}
	if _, ok := c.entries[key(iptvPreviewMaxEntries)]; !ok {
		t.Fatalf("newest entry should have survived")
	}
}

// Under the cap, evictLocked is a no-op.
func TestPreviewEvictNoopUnderCap(t *testing.T) {
	c := newIPTVPreviewCache("", 0, 0) // defaults
	c.entries["a"] = &iptvPreviewEntry{data: []byte{1}, ts: time.Now()}
	c.evictLocked()
	if len(c.entries) != 1 {
		t.Fatalf("expected entry to survive under cap, got %d", len(c.entries))
	}
}

func key(i int) string { return "ch" + strconv.Itoa(i) }
