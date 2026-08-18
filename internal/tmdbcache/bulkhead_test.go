package tmdbcache

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The bulkhead is the fix for the "slow TMDB → everything got slow" incident:
// bound concurrent upstream fetches so a stalled CDN can't pile up unbounded
// goroutines/sockets. These tests pin the fail-fast + fairness behavior.

func TestBulkheadFailsFastWhenFull(t *testing.T) {
	sem := make(chan struct{}, 2)
	ctx := context.Background()

	if err := acquire(ctx, sem); err != nil {
		t.Fatalf("acquire slot 1: %v", err)
	}
	if err := acquire(ctx, sem); err != nil {
		t.Fatalf("acquire slot 2: %v", err)
	}

	// Third acquire: no slot → must fail fast with ErrBulkheadFull after ~the
	// brief wait, NOT block indefinitely.
	start := time.Now()
	err := acquire(ctx, sem)
	elapsed := time.Since(start)
	if !errors.Is(err, ErrBulkheadFull) {
		t.Fatalf("expected ErrBulkheadFull, got %v", err)
	}
	if elapsed < bulkheadAcquireWait || elapsed > bulkheadAcquireWait+2*time.Second {
		t.Errorf("fail-fast took %v, want ~%v", elapsed, bulkheadAcquireWait)
	}

	// Freeing a slot lets the next acquire through immediately.
	release(sem)
	start = time.Now()
	if err := acquire(ctx, sem); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Errorf("acquire after release took %v, want immediate", d)
	}
}

func TestBulkheadNilIsUnbounded(t *testing.T) {
	for i := 0; i < 200; i++ {
		if err := acquire(context.Background(), nil); err != nil {
			t.Fatalf("nil sem must never block/fail: %v", err)
		}
	}
	release(nil) // must not panic
}

func TestBulkheadHonorsContext(t *testing.T) {
	sem := make(chan struct{}, 1)
	if err := acquire(context.Background(), sem); err != nil {
		t.Fatalf("fill: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := acquire(ctx, sem); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
