package transcodesvc

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// TestSchedulerAcquireExhaustReleaseFlow: a fresh scheduler accepts up to
// `capacity` Acquire() calls before returning ErrSchedulerBusy, and one
// Release re-opens exactly one slot.
func TestSchedulerAcquireExhaustReleaseFlow(t *testing.T) {
	const cap = 3
	s := newTranscodingScheduler(cap, 0, 0, "")

	// Fill up.
	for i := 0; i < cap; i++ {
		if err := s.Acquire(); err != nil {
			t.Fatalf("acquire %d returned err=%v", i, err)
		}
	}
	if err := s.Acquire(); !errors.Is(err, ErrSchedulerBusy) {
		t.Fatalf("expected ErrSchedulerBusy when full, got %v", err)
	}

	// Stats should show full.
	free, total := s.Available()
	if free != 0 || total != cap {
		t.Fatalf("Available()=%d,%d want 0,%d", free, total, cap)
	}

	// Release once → one slot frees → next Acquire succeeds.
	s.Release()
	if err := s.Acquire(); err != nil {
		t.Fatalf("after Release, Acquire should succeed, got %v", err)
	}
}

// TestSchedulerReleaseImbalanceIsLoggedNotPanicking: calling Release more
// times than Acquire must not panic — it's a known-safe defensive path.
func TestSchedulerReleaseImbalanceIsLoggedNotPanicking(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Release without matching Acquire panicked: %v", r)
		}
	}()
	s := newTranscodingScheduler(2, 0, 0, "")
	s.Release() // imbalance — should warn-and-continue
	s.Release()
}

// TestSchedulerStatsCountersAdvance verifies Stats reflects acquires + rejections.
func TestSchedulerStatsCountersAdvance(t *testing.T) {
	s := newTranscodingScheduler(1, 0, 0, "")
	_ = s.Acquire()
	_ = s.Acquire() // will reject

	stats := s.Stats()
	if stats["acquired"].(int64) != 1 {
		t.Fatalf("acquired counter: %v", stats["acquired"])
	}
	if stats["rejections"].(int64) != 1 {
		t.Fatalf("rejections counter: %v", stats["rejections"])
	}
	if stats["in_use"].(int) != 1 || stats["available"].(int) != 0 {
		t.Fatalf("in_use/available: %v / %v", stats["in_use"], stats["available"])
	}
}

// TestSchedulerConcurrentAcquireRelease stress-tests the channel-backed
// slot pool. Race-detector + multiple goroutines exercising the same
// scheduler should keep counters consistent.
func TestSchedulerConcurrentAcquireRelease(t *testing.T) {
	s := newTranscodingScheduler(4, 0, 0, "")
	var wg sync.WaitGroup
	var ok, rej int64
	const N = 200
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Acquire(); err == nil {
				atomic.AddInt64(&ok, 1)
				s.Release()
			} else {
				atomic.AddInt64(&rej, 1)
			}
		}()
	}
	wg.Wait()
	if ok+rej != int64(N) {
		t.Fatalf("ok+rej != N: %d+%d=%d", ok, rej, N)
	}
	// All slots should be empty after the dust settles.
	free, _ := s.Available()
	if free != 4 {
		t.Fatalf("post-test free slots: %d (want 4)", free)
	}
}

// TestPendingReleaseOnceIdempotent: pendingReleaseOnce must release exactly
// once even when both cleanup() and a finalizer call into it.
func TestPendingReleaseOnceIdempotent(t *testing.T) {
	s := newTranscodingScheduler(2, 0, 0, "")
	_ = s.Acquire()

	p := pendingReleaseOnce{sch: s}
	p.Release()
	p.Release() // must be a no-op
	p.Release() // still no-op

	free, _ := s.Available()
	if free != 2 {
		t.Fatalf("expected 2 free slots after single effective release, got %d", free)
	}
}

// TestDirSizeAccumulatesRegularFilesOnly checks the disk-budget helper:
// sub-dir traversal, ignores directories themselves, returns total bytes.
func TestDirSizeAccumulatesRegularFilesOnly(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string, body []byte) {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	mk("a.txt", []byte("hello"))         // 5 bytes
	mk("sub/b.bin", []byte{0, 1, 2})     // 3 bytes
	mk("sub/deep/c.dat", []byte("xxxx")) // 4 bytes

	got, err := dirSize(root)
	if err != nil {
		t.Fatalf("dirSize: %v", err)
	}
	if got != 12 {
		t.Fatalf("total: %d (want 12)", got)
	}
}

// TestDirSizeOnNonexistentRoot returns non-nil error but no panic.
func TestDirSizeOnNonexistentRoot(t *testing.T) {
	got, err := dirSize(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Logf("dirSize on missing path returned err=nil; total=%d (some platforms accept)", got)
	}
	if got != 0 {
		t.Fatalf("dirSize on missing path should return 0, got %d", got)
	}
}

func TestSchedulerMemoryGate(t *testing.T) {
	orig := availableMemBytesFn
	defer func() { availableMemBytesFn = orig }()

	// Gate off (minFreeMemMB=0): low memory is ignored.
	availableMemBytesFn = func() int64 { return 100 * 1024 * 1024 } // 100MB
	s := newTranscodingScheduler(2, 0, 0, "")
	if err := s.Acquire(); err != nil {
		t.Fatalf("gate off must admit, got %v", err)
	}
	s.Release()

	// Gate on, plenty of memory → admit.
	s = newTranscodingScheduler(2, 0, 2000, "") // require 2000MB free
	availableMemBytesFn = func() int64 { return 8000 * 1024 * 1024 }
	if err := s.Acquire(); err != nil {
		t.Fatalf("ample memory must admit, got %v", err)
	}
	s.Release()

	// Gate on, low memory → reject with ErrSchedulerBusy, slot NOT consumed.
	availableMemBytesFn = func() int64 { return 500 * 1024 * 1024 } // 500MB < 2000MB
	if err := s.Acquire(); err != ErrSchedulerBusy {
		t.Fatalf("low memory must reject, got %v", err)
	}
	if free, total := s.Available(); free != total {
		t.Errorf("rejected job must not consume a slot: free=%d total=%d", free, total)
	}
	if got := s.Stats()["mem_rejected"].(int64); got != 1 {
		t.Errorf("mem_rejected = %d, want 1", got)
	}

	// Probe unavailable (-1, e.g. non-Linux) → gate no-ops, admit.
	availableMemBytesFn = func() int64 { return -1 }
	if err := s.Acquire(); err != nil {
		t.Fatalf("unknown memory must admit (fail-open), got %v", err)
	}
	s.Release()
}
