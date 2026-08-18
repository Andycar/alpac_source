package rutracker

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"
)

// limiter serializes outgoing requests with a minimum spacing and carries the
// circuit breaker. Both exist for one reason: a single account behind a single
// server IP is trivially bannable, and the failure mode is losing the source
// for days — far worse than a slow search.
type limiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time

	fails    int
	openUntil time.Time
}

func newLimiter(interval time.Duration) *limiter {
	if interval < 0 {
		interval = 0
	}
	return &limiter{interval: interval}
}

func (l *limiter) setInterval(d time.Duration) {
	if d < 0 {
		d = 0
	}
	l.mu.Lock()
	l.interval = d
	l.mu.Unlock()
}

func (l *limiter) bannedUntil() time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.openUntil
}

// open reports whether the breaker is currently tripped.
func (l *limiter) open() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return time.Now().Before(l.openUntil)
}

// reserve returns how long the caller must sleep before firing its request.
// It books the slot, so concurrent callers queue instead of colliding.
func (l *limiter) reserve() (time.Duration, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	if now.Before(l.openUntil) {
		return 0, fmt.Errorf("rutracker: source paused until %s", l.openUntil.Format(time.TimeOnly))
	}
	if l.interval <= 0 {
		return 0, nil
	}
	// ±30% jitter so a fixed cadence does not look like a bot metronome.
	jitter := time.Duration(float64(l.interval) * (rand.Float64()*0.6 - 0.3))
	slot := l.next
	if slot.Before(now) {
		slot = now
	}
	l.next = slot.Add(l.interval + jitter)
	return slot.Sub(now), nil
}

// wait blocks for the reserved slot, honouring ctx cancellation.
func (l *limiter) wait(ctx context.Context) error {
	d, err := l.reserve()
	if err != nil {
		return err
	}
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// success closes the breaker.
func (l *limiter) success() {
	l.mu.Lock()
	l.fails = 0
	l.openUntil = time.Time{}
	l.mu.Unlock()
}

// failure trips the breaker with exponential backoff after three consecutive
// failures (or immediately when the upstream explicitly rate-limited us).
func (l *limiter) failure(hard bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails++
	if !hard && l.fails < 3 {
		return
	}
	backoff := time.Duration(1<<min(l.fails, 5)) * time.Minute
	if backoff > 30*time.Minute {
		backoff = 30 * time.Minute
	}
	l.openUntil = time.Now().Add(backoff)
}

// ---------------------------------------------------------------------------
//  stats
// ---------------------------------------------------------------------------

type stats struct {
	mu        sync.Mutex
	searches  int64
	requests  int64
	rowsLast  int
	totalMs   int64
	samples   int64
	challenge bool
	viaFS     bool
	lastErr   string
}

type statsSnapshot struct {
	searches  int64
	requests  int64
	rowsLast  int
	totalMs   int64
	samples   int64
	challenge bool
	viaFS     bool
	lastErr   string
}

func (s *statsSnapshot) avgMs() int64 {
	if s.samples == 0 {
		return 0
	}
	return s.totalMs / s.samples
}

func (s *stats) snapshot() statsSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return statsSnapshot{
		searches: s.searches, requests: s.requests, rowsLast: s.rowsLast,
		totalMs: s.totalMs, samples: s.samples,
		challenge: s.challenge, viaFS: s.viaFS, lastErr: s.lastErr,
	}
}

func (s *stats) addRequest(d time.Duration, err error) {
	s.mu.Lock()
	s.requests++
	s.totalMs += d.Milliseconds()
	s.samples++
	if err != nil {
		s.lastErr = err.Error()
	}
	s.mu.Unlock()
}

func (s *stats) addSearch(rows int) {
	s.mu.Lock()
	s.searches++
	s.rowsLast = rows
	s.mu.Unlock()
}

func (s *stats) setChallenge(v bool) {
	s.mu.Lock()
	s.challenge = v
	s.mu.Unlock()
}

func (s *stats) setViaFS(v bool) {
	s.mu.Lock()
	s.viaFS = v
	s.mu.Unlock()
}

func (s *stats) setErr(err error) {
	s.mu.Lock()
	if err == nil {
		s.lastErr = ""
	} else {
		s.lastErr = err.Error()
	}
	s.mu.Unlock()
}
