// Package browsergate holds the process-wide Chrome/chromedp concurrency
// primitives shared by every browser-using online source (mirage, rezka,
// vibix, alloha, zona, turbo, …): DynSemaphore (runtime-resizable capacity),
// Gate (shared/exclusive writer-preferring gate) and ChromeSem (per-pool
// semaphore that also claims a shared slot on the Global gate).
//
// LEAF package: stdlib + zerolog only.
package browsergate

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// ---------------------------------------------------------------------------
// browserGate — process-wide writer-preferring gate for Chrome sessions.
// ---------------------------------------------------------------------------
//
// The lampac-go daemon runs multiple modules that each spawn Chrome/chromedp
// instances concurrently (mirage, rezka, vibix, alloha, zona, turbo, …).
// Each module uses its own per-pool DynSemaphore for capacity, but there is
// no coordination *between* pools. With 8 mirage slots + 4 rezka slots
// active simultaneously the host can have a dozen Chrome trees fighting
// for CPU, /dev/shm and X server state.
//
// That's fine for ordinary scraping, but it breaks fancdn-register: Google
// reCAPTCHA v3 returns score 0.0 for browsers running on a contended host
// (slow JS timings, broken sandbox handshakes), and fanserial rejects the
// registration with "Проверка не пройдена". When the same subprocess is
// launched in isolation (via `systemd-run`) it sails through.
//
// Gate fixes that by adding two modes:
//
//	Shared    — any number can hold (subject to per-pool sem limits).
//	Exclusive — single holder; waits until ALL shared callers release;
//	            new shared acquires block until exclusive releases.
//
// "Writer preference": once an Exclusive acquire is queued, no new Shared
// acquires are admitted. This prevents starvation when shared traffic is
// continuous (which it is — every /lite/mirage hit takes a slot).
//
// Acquire APIs return false only on context cancellation; the caller MUST
// pair every successful Acquire with the matching Release.

// ---------------------------------------------------------------------------
// Dynamic semaphore — supports runtime capacity changes.
// ---------------------------------------------------------------------------

// DynSemaphore is a semaphore whose capacity can be changed at runtime.
// Unlike a fixed-size channel, it uses a mutex + condition variable so the
// limit can be raised or lowered without recreating the channel.
type DynSemaphore struct {
	mu      sync.Mutex
	cond    *sync.Cond
	limit   int
	current int
}

func NewDynSemaphore(limit int) *DynSemaphore {
	ds := &DynSemaphore{limit: limit}
	ds.cond = sync.NewCond(&ds.mu)
	return ds
}

// Acquire blocks until a slot is available or ctx is done.
// Returns true if acquired, false on context cancellation.
func (ds *DynSemaphore) Acquire(ctx context.Context) bool {
	ds.mu.Lock()
	defer ds.mu.Unlock()

	for ds.current >= ds.limit {
		// Wait with context awareness.
		done := make(chan struct{})
		go func() {
			ds.mu.Lock()
			ds.cond.Wait()
			ds.mu.Unlock()
			close(done)
		}()

		ds.mu.Unlock()
		select {
		case <-done:
			ds.mu.Lock()
			continue
		case <-ctx.Done():
			// Wake the goroutine so it doesn't leak.
			ds.cond.Broadcast()
			<-done
			ds.mu.Lock()
			return false
		}
	}
	ds.current++
	return true
}

// Release frees a slot and wakes one waiter.
func (ds *DynSemaphore) Release() {
	ds.mu.Lock()
	if ds.current > 0 {
		ds.current--
	}
	ds.mu.Unlock()
	ds.cond.Signal()
}

// SetLimit changes the semaphore capacity at runtime.
func (ds *DynSemaphore) SetLimit(n int) {
	if n < 1 {
		n = 1
	}
	ds.mu.Lock()
	ds.limit = n
	ds.mu.Unlock()
	ds.cond.Broadcast()
}

// Stats returns (current, limit).
func (ds *DynSemaphore) Stats() (int, int) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	return ds.current, ds.limit
}

type Gate struct {
	mu               sync.Mutex
	cond             *sync.Cond
	exclusiveHeld    bool
	exclusiveWaiters int
	sharedCount      int
}

func NewGate() *Gate {
	g := &Gate{}
	g.cond = sync.NewCond(&g.mu)
	return g
}

// acquireShared blocks until no exclusive holder is active and no exclusive
// acquire is queued. Returns false on ctx cancellation.
func (g *Gate) acquireShared(ctx context.Context) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for g.exclusiveHeld || g.exclusiveWaiters > 0 {
		if !g.waitWithCtxLocked(ctx) {
			return false
		}
	}
	g.sharedCount++
	return true
}

func (g *Gate) releaseShared() {
	g.mu.Lock()
	if g.sharedCount > 0 {
		g.sharedCount--
	}
	wake := g.sharedCount == 0 && g.exclusiveWaiters > 0
	g.mu.Unlock()
	if wake {
		// Only one exclusive can win; Signal is enough.
		g.cond.Signal()
	}
}

// acquireExclusive blocks until exclusiveHeld == false AND sharedCount == 0.
// Once queued, new shared acquires are blocked (writer preference).
// Returns false on ctx cancellation.
func (g *Gate) acquireExclusive(ctx context.Context) bool {
	start := time.Now()
	g.mu.Lock()
	g.exclusiveWaiters++
	for g.exclusiveHeld || g.sharedCount > 0 {
		if !g.waitWithCtxLocked(ctx) {
			g.exclusiveWaiters--
			// Wake one shared waiter if we were the last exclusive blocker.
			wakeShared := g.exclusiveWaiters == 0
			g.mu.Unlock()
			if wakeShared {
				g.cond.Broadcast()
			}
			return false
		}
	}
	g.exclusiveWaiters--
	g.exclusiveHeld = true
	waited := time.Since(start)
	g.mu.Unlock()
	if waited > 500*time.Millisecond {
		log.Info().Dur("waited", waited).Msg("Gate: acquired exclusive after wait")
	}
	return true
}

func (g *Gate) releaseExclusive() {
	g.mu.Lock()
	g.exclusiveHeld = false
	g.mu.Unlock()
	// Wake everyone; whoever wins races into their slot.
	g.cond.Broadcast()
}

// waitWithCtxLocked: caller holds g.mu. Releases it during wait, reacquires
// before returning. Returns false if ctx fired.
func (g *Gate) waitWithCtxLocked(ctx context.Context) bool {
	done := make(chan struct{})
	go func() {
		g.mu.Lock()
		g.cond.Wait()
		g.mu.Unlock()
		close(done)
	}()
	g.mu.Unlock()
	select {
	case <-done:
		g.mu.Lock()
		return true
	case <-ctx.Done():
		// Wake the helper so it doesn't leak.
		g.cond.Broadcast()
		<-done
		g.mu.Lock()
		return false
	}
}

// ---------------------------------------------------------------------------
// ChromeSem — drop-in replacement for DynSemaphore that also routes
// every Acquire/Release through Gate in Shared mode.
// ---------------------------------------------------------------------------
//
// Existing call sites (vibix, mirage, rezka, alloha, turbo, zona, vibix)
// keep the same .Acquire(ctx)/.Release() API. The gate coordination is
// transparent: each call first claims a shared slot on the global gate,
// then grabs a per-pool capacity slot. Release unwinds in reverse.

type ChromeSem struct {
	sem  *DynSemaphore
	gate *Gate
}

func NewChromeSem(limit int, gate *Gate) *ChromeSem {
	return &ChromeSem{
		sem:  NewDynSemaphore(limit),
		gate: gate,
	}
}

// Acquire blocks until a slot is available (subject to the global gate)
// or ctx is cancelled. Returns true on success.
func (s *ChromeSem) Acquire(ctx context.Context) bool {
	if !s.gate.acquireShared(ctx) {
		return false
	}
	if !s.sem.Acquire(ctx) {
		s.gate.releaseShared()
		return false
	}
	return true
}

// Release frees the per-pool slot and the shared gate slot.
func (s *ChromeSem) Release() {
	s.sem.Release()
	s.gate.releaseShared()
}

// SetLimit changes the per-pool capacity at runtime (gate is unaffected).
func (s *ChromeSem) SetLimit(n int) { s.sem.SetLimit(n) }

// Stats returns (active, limit) on the per-pool semaphore.
func (s *ChromeSem) Stats() (int, int) { return s.sem.Stats() }

// ---------------------------------------------------------------------------
// Global gate + exclusive-mode helpers (used by fancdn-register).
// ---------------------------------------------------------------------------

var Global = NewGate()

// AcquireBrowserExclusive blocks until ALL shared Chrome sessions (across
// every per-pool semaphore wired through ChromeSem) have released.
// While the returned release function is unreturned, no new shared
// acquires are admitted. Use this for sensitive work where Chrome must
// not compete with other instances for CPU / /dev/shm / X11 state — most
// notably fancdn-register, which needs a clean environment for Google
// reCAPTCHA v3 to give a non-zero score.
//
// Returns (release, ok). ok=false means ctx was cancelled — caller MUST
// NOT call release in that case.
func AcquireBrowserExclusive(ctx context.Context, who string) (release func(), ok bool) {
	if !Global.acquireExclusive(ctx) {
		return nil, false
	}
	start := time.Now()
	released := false
	return func() {
		if released {
			return // be lenient about double-release
		}
		released = true
		Global.releaseExclusive()
		log.Debug().
			Str("who", who).
			Dur("held", time.Since(start)).
			Msg("Gate: released exclusive")
	}, true
}
