package updater

import (
	"sync/atomic"
	"testing"
)

// Restart must run the registered drainer (which flushes in-flight HTTP
// requests) BEFORE it re-execs the process. If the order ever flips, a client
// mid-download of a plugin JS gets a truncated body and old WebKit throws
// "Unexpected token ILLEGAL" — the exact regression this hook prevents.
func TestServiceRestart_DrainsBeforeReexec(t *testing.T) {
	orig := reexecProcess
	t.Cleanup(func() { reexecProcess = orig })

	var step atomic.Int32 // monotonic counter to capture call order
	var drainOrder, reexecOrder int32

	reexecProcess = func() error {
		reexecOrder = step.Add(1)
		return nil // stub: do NOT execve, or the test runner dies
	}

	s := New(Config{}, "1.0", "abc", "today")
	s.SetRestartDrainer(func() {
		drainOrder = step.Add(1)
	})

	if err := s.Restart(); err != nil {
		t.Fatalf("Restart returned error: %v", err)
	}
	if drainOrder == 0 {
		t.Fatal("drainer was never called")
	}
	if reexecOrder == 0 {
		t.Fatal("reexec was never called")
	}
	if drainOrder > reexecOrder {
		t.Fatalf("drain ran after reexec (drain=%d reexec=%d); must drain first", drainOrder, reexecOrder)
	}
}

// With no drainer registered, Restart must still re-exec (the hook is
// optional — non-HTTP callers or older wiring may not set it).
func TestServiceRestart_NoDrainerStillReexecs(t *testing.T) {
	orig := reexecProcess
	t.Cleanup(func() { reexecProcess = orig })

	var called atomic.Bool
	reexecProcess = func() error {
		called.Store(true)
		return nil
	}

	s := New(Config{}, "1.0", "abc", "today")
	if err := s.Restart(); err != nil {
		t.Fatalf("Restart returned error: %v", err)
	}
	if !called.Load() {
		t.Fatal("reexec was not called when no drainer was set")
	}
}
