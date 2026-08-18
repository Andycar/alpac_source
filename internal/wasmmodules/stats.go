package wasmmodules

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// ModuleStats are surfaced on the admin panel.
//
// Counters use atomics so the runtime can update them without locks. The
// latency histogram is gated by a mutex (write happens once per Invoke; reads
// are rare) — using a sync.RWMutex would be overkill.
type ModuleStats struct {
	Invocations atomic.Int64 `json:"-"`
	Errors      atomic.Int64 `json:"-"`

	// Network IO recorded by host_http.
	HTTPRequests atomic.Int64 `json:"-"`
	HTTPBytesIn  atomic.Int64 `json:"-"`
	HTTPBytesOut atomic.Int64 `json:"-"`

	// Cache stats — recorded by host_cache_get/set.
	CacheHits   atomic.Int64 `json:"-"`
	CacheMisses atomic.Int64 `json:"-"`
	CacheSets   atomic.Int64 `json:"-"`

	// Last error timestamp + message — useful for the admin panel "what
	// broke?" column without trawling the log buffer.
	lastErrMu  sync.Mutex
	lastErr    string
	lastErrAt  time.Time
	lastOKAt   time.Time

	// Latency samples in microseconds. Bounded ring; we don't keep history
	// past `latencyCap` entries to keep memory and percentile-cost flat.
	latencyMu  sync.Mutex
	latencyBuf []int64 // microseconds
	latencyPos int
}

const latencyCap = 256

// recordInvoke updates counters + histogram for one Invoke completion.
func (s *ModuleStats) recordInvoke(elapsed time.Duration, err error) {
	s.Invocations.Add(1)
	if err != nil {
		s.Errors.Add(1)
		s.lastErrMu.Lock()
		s.lastErr = err.Error()
		s.lastErrAt = time.Now()
		s.lastErrMu.Unlock()
	} else {
		s.lastErrMu.Lock()
		s.lastOKAt = time.Now()
		s.lastErrMu.Unlock()
	}

	micros := elapsed.Microseconds()
	if micros < 0 {
		micros = 0
	}
	s.latencyMu.Lock()
	if len(s.latencyBuf) < latencyCap {
		s.latencyBuf = append(s.latencyBuf, micros)
	} else {
		s.latencyBuf[s.latencyPos] = micros
		s.latencyPos = (s.latencyPos + 1) % latencyCap
	}
	s.latencyMu.Unlock()
}

// StatsSnapshot is a JSON-friendly read of ModuleStats.
type StatsSnapshot struct {
	Invocations  int64     `json:"invocations"`
	Errors       int64     `json:"errors"`
	HTTPRequests int64     `json:"http_requests"`
	HTTPBytesIn  int64     `json:"http_bytes_in"`
	HTTPBytesOut int64     `json:"http_bytes_out"`
	CacheHits    int64     `json:"cache_hits"`
	CacheMisses  int64     `json:"cache_misses"`
	CacheSets    int64     `json:"cache_sets"`
	CacheHitRate float64   `json:"cache_hit_rate"`
	P50US        int64     `json:"p50_us"`
	P95US        int64     `json:"p95_us"`
	P99US        int64     `json:"p99_us"`
	LastError    string    `json:"last_error,omitempty"`
	LastErrorAt  time.Time `json:"last_error_at,omitempty"`
	LastOKAt     time.Time `json:"last_ok_at,omitempty"`
}

// Snapshot returns a point-in-time copy fit for JSON marshalling.
func (s *ModuleStats) Snapshot() StatsSnapshot {
	out := StatsSnapshot{
		Invocations:  s.Invocations.Load(),
		Errors:       s.Errors.Load(),
		HTTPRequests: s.HTTPRequests.Load(),
		HTTPBytesIn:  s.HTTPBytesIn.Load(),
		HTTPBytesOut: s.HTTPBytesOut.Load(),
		CacheHits:    s.CacheHits.Load(),
		CacheMisses:  s.CacheMisses.Load(),
		CacheSets:    s.CacheSets.Load(),
	}
	if total := out.CacheHits + out.CacheMisses; total > 0 {
		out.CacheHitRate = float64(out.CacheHits) / float64(total)
	}

	s.latencyMu.Lock()
	if len(s.latencyBuf) > 0 {
		buf := append([]int64(nil), s.latencyBuf...)
		s.latencyMu.Unlock()
		sort.Slice(buf, func(i, j int) bool { return buf[i] < buf[j] })
		out.P50US = percentile(buf, 50)
		out.P95US = percentile(buf, 95)
		out.P99US = percentile(buf, 99)
	} else {
		s.latencyMu.Unlock()
	}

	s.lastErrMu.Lock()
	out.LastError = s.lastErr
	out.LastErrorAt = s.lastErrAt
	out.LastOKAt = s.lastOKAt
	s.lastErrMu.Unlock()
	return out
}

// percentile returns the given percentile of an already-sorted slice.
// Linear nearest-rank — accurate enough for small fixed buffers.
func percentile(sorted []int64, p int) int64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}
	idx := (p * len(sorted)) / 100
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
