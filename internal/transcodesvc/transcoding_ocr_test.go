package transcodesvc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOCRCache_KeyStability(t *testing.T) {
	c := NewOCRCache(t.TempDir())
	if c == nil {
		t.Fatal("NewOCRCache returned nil for valid temp root")
	}
	k1 := c.Key("https://cdn.example.com/movie.mkv", 3)
	k2 := c.Key("https://cdn.example.com/movie.mkv", 3)
	if k1 != k2 {
		t.Errorf("same input must produce same key, got %q vs %q", k1, k2)
	}
	k3 := c.Key("https://cdn.example.com/movie.mkv", 4)
	if k1 == k3 {
		t.Errorf("different sub index must produce different key")
	}
	k4 := c.Key("https://cdn.example.com/other.mkv", 3)
	if k1 == k4 {
		t.Errorf("different URL must produce different key")
	}
}

func TestOCRCache_NilSafe(t *testing.T) {
	var c *OCRCache
	// All nil-receiver methods must be safe.
	if got := c.Lookup("x", 0); got != "" {
		t.Errorf("nil cache Lookup should return empty, got %q", got)
	}
	if got := c.PathFor("k"); got != "" {
		t.Errorf("nil cache PathFor should return empty, got %q", got)
	}
	stats := c.Stats()
	if stats["enabled"] != false {
		t.Errorf("nil cache Stats should say enabled=false")
	}
	c.ReleasePending("k") // must not panic
	acquired, _ := c.AcquirePending("k")
	if acquired {
		t.Errorf("nil cache AcquirePending should return false")
	}
}

func TestOCRCache_Lookup_HitMissCounters(t *testing.T) {
	dir := t.TempDir()
	c := NewOCRCache(dir)
	if c == nil {
		t.Fatal("NewOCRCache returned nil")
	}

	// First lookup: miss.
	if got := c.Lookup("https://x/y.mkv", 0); got != "" {
		t.Errorf("uninitialised cache should miss, got %q", got)
	}

	// Plant a file at the expected location.
	key := c.Key("https://x/y.mkv", 0)
	path := c.PathFor(key)
	if err := os.WriteFile(path, []byte("WEBVTT\n\n"), 0o644); err != nil {
		t.Fatalf("seed cache file: %v", err)
	}

	// Second lookup: hit.
	got := c.Lookup("https://x/y.mkv", 0)
	if got != path {
		t.Errorf("cached lookup = %q, want %q", got, path)
	}

	stats := c.Stats()
	if stats["hits"].(int64) != 1 {
		t.Errorf("hits = %v, want 1", stats["hits"])
	}
	if stats["misses"].(int64) != 1 {
		t.Errorf("misses = %v, want 1", stats["misses"])
	}
	rate := stats["hit_rate"].(float64)
	if rate <= 0.4 || rate >= 0.6 {
		t.Errorf("hit_rate ≈ 0.5 expected (1/2), got %f", rate)
	}
}

func TestOCRCache_Lookup_EmptyFileTreatedAsMiss(t *testing.T) {
	c := NewOCRCache(t.TempDir())
	key := c.Key("https://z/w.mkv", 1)
	if err := os.WriteFile(c.PathFor(key), []byte{}, 0o644); err != nil {
		t.Fatalf("seed empty file: %v", err)
	}
	if got := c.Lookup("https://z/w.mkv", 1); got != "" {
		t.Errorf("empty cache file must be treated as miss, got %q", got)
	}
}

func TestOCRCache_Pending_FirstAcquireWins(t *testing.T) {
	c := NewOCRCache(t.TempDir())
	key := "abc"
	first, _ := c.AcquirePending(key)
	if !first {
		t.Errorf("first AcquirePending should win")
	}
	second, ch := c.AcquirePending(key)
	if second {
		t.Errorf("second AcquirePending must wait, got acquired=true")
	}
	if ch == nil {
		t.Errorf("waiter must receive a non-nil channel")
	}

	// Verify the waiter unblocks when ReleasePending fires.
	done := make(chan struct{})
	go func() {
		<-ch
		close(done)
	}()
	c.ReleasePending(key)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Errorf("waiter did not unblock within 2s after release")
	}
}

// ---------------------------------------------------------------------------
// canPromoteToOCR
// ---------------------------------------------------------------------------

func TestCanPromoteToOCR_AllConditions(t *testing.T) {
	dir := t.TempDir()
	c := NewOCRCache(dir)
	tInfo := &TesseractInfo{Available: true, Path: "/usr/bin/tesseract", Version: "5.0.0"}
	pgs := &SubtitleStream{AbsIndex: 2, Codec: "hdmv_pgs_subtitle"}

	// No tesseract → no promotion.
	if canPromoteToOCR(&TesseractInfo{Available: false}, c, "https://x", pgs, false) {
		t.Errorf("no tesseract → must not promote")
	}

	// No cache → no promotion.
	if canPromoteToOCR(tInfo, nil, "https://x", pgs, false) {
		t.Errorf("nil cache → must not promote")
	}

	// Non-bitmap codec → no promotion.
	srt := &SubtitleStream{AbsIndex: 2, Codec: "subrip"}
	if canPromoteToOCR(tInfo, c, "https://x", srt, false) {
		t.Errorf("subrip is text — must not be promoted to OCR (it never burned in)")
	}

	// pipe:0 source → no promotion (can't re-read).
	if canPromoteToOCR(tInfo, c, "https://x", pgs, true) {
		t.Errorf("pipe:0 source → must not promote")
	}

	// Cache miss → no promotion.
	if canPromoteToOCR(tInfo, c, "https://x", pgs, false) {
		t.Errorf("cache miss → must not promote (need cached WebVTT)")
	}

	// Plant a cache hit → promote.
	key := c.Key("https://x", 2)
	if err := os.WriteFile(filepath.Join(dir, ocrCacheDirName, key+".vtt"), []byte("WEBVTT\n\n1\n00:00:01.000 --> 00:00:02.000\nhello\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if !canPromoteToOCR(tInfo, c, "https://x", pgs, false) {
		t.Errorf("all conditions met (tesseract + cache hit + bitmap + http) — must promote")
	}
}

// ---------------------------------------------------------------------------
// WebVTT helpers
// ---------------------------------------------------------------------------

func TestWebVTTTimestamp(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "00:00:00.000"},
		{500 * time.Millisecond, "00:00:00.500"},
		{1*time.Second + 234*time.Millisecond, "00:00:01.234"},
		{61 * time.Second, "00:01:01.000"},
		{3661*time.Second + 999*time.Millisecond, "01:01:01.999"},
		{-1 * time.Second, "00:00:00.000"}, // negatives clamp to zero
	}
	for _, c := range cases {
		got := WebVTTTimestamp(c.d)
		if got != c.want {
			t.Errorf("WebVTTTimestamp(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestAssembleWebVTT_OrdersAndDedupes(t *testing.T) {
	cues := []OCRCue{
		{Start: 5 * time.Second, End: 6 * time.Second, Text: "second"},
		{Start: 1 * time.Second, End: 2 * time.Second, Text: "first"},
		{Start: 10 * time.Second, End: 11 * time.Second, Text: "  "}, // empty after trim
		{Start: 8 * time.Second, End: 9 * time.Second, Text: "third"},
	}
	body := AssembleWebVTT(cues)
	if !strings.HasPrefix(body, "WEBVTT") {
		t.Errorf("must start with WEBVTT, got %q", body[:10])
	}
	// Three cues survive (the empty-text one is dropped).
	if got := strings.Count(body, "-->"); got != 3 {
		t.Errorf("expected 3 cue arrows, got %d in %q", got, body)
	}
	// Order must be ascending by start time.
	idx1 := strings.Index(body, "first")
	idx2 := strings.Index(body, "second")
	idx3 := strings.Index(body, "third")
	if !(idx1 < idx2 && idx2 < idx3) {
		t.Errorf("cues must be sorted by start time, got positions %d / %d / %d", idx1, idx2, idx3)
	}
}

func TestAssembleWebVTT_EmptyInput(t *testing.T) {
	got := AssembleWebVTT(nil)
	if !strings.HasPrefix(got, "WEBVTT") {
		t.Errorf("empty input still yields valid WebVTT header, got %q", got)
	}
	if strings.Contains(got, "-->") {
		t.Errorf("empty input must not have any cues, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// runOCRStub — must honour pending contract even though not implemented.
// ---------------------------------------------------------------------------

func TestRunOCRStub_NotImplementedError(t *testing.T) {
	svc := &TranscodingService{
		ocrCache:  NewOCRCache(t.TempDir()),
		tesseract: &TesseractInfo{Available: true},
	}
	err := svc.runOCRStub("https://x/y.mkv", nil, &SubtitleStream{AbsIndex: 2})
	if err != ErrOCRNotImplemented {
		t.Errorf("runOCRStub should return ErrOCRNotImplemented in Phase 1, got %v", err)
	}
	// Pending must have been released — a second AcquirePending should
	// win again, not block.
	acq, _ := svc.ocrCache.AcquirePending(svc.ocrCache.Key("https://x/y.mkv", 2))
	if !acq {
		t.Errorf("ReleasePending didn't fire — second AcquirePending should win, got acquired=false")
	}
}

func TestRunOCRStub_NoTesseract_ReturnsImmediately(t *testing.T) {
	svc := &TranscodingService{
		tesseract: nil,
	}
	err := svc.runOCRStub("x", nil, &SubtitleStream{AbsIndex: 0})
	if err != ErrOCRNotImplemented {
		t.Errorf("no tesseract path → ErrOCRNotImplemented, got %v", err)
	}
}
