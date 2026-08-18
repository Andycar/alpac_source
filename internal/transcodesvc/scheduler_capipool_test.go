package transcodesvc

import "testing"

// The separate pool is opt-in: with max_concurrent_capi unset (0) a tagged
// request still works — schedulerFor falls back to the main pool.
func TestSchedulerForCapiPool(t *testing.T) {
	main := newTranscodingScheduler(2, 0, 0, t.TempDir())
	capi := newTranscodingScheduler(3, 0, 0, t.TempDir())

	shared := &TranscodingService{scheduler: main} // capi pool unconfigured
	if got := shared.schedulerFor(&TranscodingStartRequest{useCapiPool: true}); got != main {
		t.Errorf("unconfigured capi pool must fall back to main")
	}

	split := &TranscodingService{scheduler: main, schedulerCapi: capi}
	if got := split.schedulerFor(&TranscodingStartRequest{useCapiPool: true}); got != capi {
		t.Errorf("tagged request must use the capi pool when configured")
	}
	if got := split.schedulerFor(&TranscodingStartRequest{useCapiPool: false}); got != main {
		t.Errorf("untagged (standard Lampa) request must use the main pool")
	}
	// Isolation: exhausting the main pool must NOT block the capi pool.
	_ = main.Acquire()
	_ = main.Acquire()
	if err := main.Acquire(); err == nil {
		t.Fatalf("main pool should be exhausted at capacity 2")
	}
	if err := capi.Acquire(); err != nil {
		t.Errorf("capi pool must still have free slots when main is full: %v", err)
	}
}
