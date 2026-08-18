// Package balancerstats records per-balancer success/failure metrics from
// real user traffic and exposes sliding-window aggregates for the admin
// dashboard, alerting engine, and fallback-host rotation.
//
// Design goals:
//   - Lock-free hot path: Record() is called from every balancerFetch invocation.
//     A per-balancer ring buffer protected by a single mutex is fine for our
//     volume (~hundreds of req/s at peak), but Record() never blocks on I/O.
//   - Bounded memory: each balancer keeps the last N attempts (default 256).
//   - Sliding windows: aggregates are computed on-demand from the ring, no
//     background tickers updating partial counters.
//
// The Manager is the public entry point; it lives as a singleton on the
// server (server.balancerStats) and is plumbed where needed.
package balancerstats

import (
	"sort"
	"sync"
	"time"
)

// Attempt is a single balancer HTTP attempt — recorded by balancerFetch.
type Attempt struct {
	Time      time.Time
	Success   bool
	LatencyMs int64
	Status    int    // HTTP status code (0 if network error before response)
	Err       string // truncated error message (empty on success)
	Host      string // host that was contacted (used for fallback rotation)
	URL       string // truncated URL for surfacing recent errors
}

// Window is an aggregate over a recent slice of attempts.
type Window struct {
	Total       int     `json:"total"`
	Success     int     `json:"success"`
	Failure     int     `json:"failure"`
	SuccessRate float64 `json:"success_rate"` // 0.0–1.0
	AvgLatency  int64   `json:"avg_latency_ms"`
	P50Latency  int64   `json:"p50_latency_ms"`
	P95Latency  int64   `json:"p95_latency_ms"`
	P99Latency  int64   `json:"p99_latency_ms"`
}

// Snapshot summarizes a balancer's recent behavior for the dashboard.
type Snapshot struct {
	Balancer    string             `json:"balancer"`
	LastAttempt time.Time          `json:"last_attempt"`
	LastSuccess time.Time          `json:"last_success"`
	LastError   string             `json:"last_error,omitempty"`
	LastErrorAt time.Time          `json:"last_error_at,omitempty"`
	CurrentHost string             `json:"current_host,omitempty"`
	Windows     map[string]Window  `json:"windows"` // "1m", "5m", "15m", "1h"
	HostStats   map[string]Window  `json:"host_stats,omitempty"`
	RecentErrs  []RecentError      `json:"recent_errors,omitempty"`
}

// RecentError surfaces individual recent failures for the UI.
type RecentError struct {
	Time   time.Time `json:"time"`
	Status int       `json:"status"`
	Err    string    `json:"err"`
	Host   string    `json:"host,omitempty"`
	URL    string    `json:"url,omitempty"`
}

// balancerRing is the per-balancer ring buffer of attempts.
type balancerRing struct {
	mu       sync.RWMutex
	attempts []Attempt
	head     int // next write index
	count    int
	cap      int

	// Hot fields cached for fast lookup, updated under mu.
	lastAttempt time.Time
	lastSuccess time.Time
	lastError   string
	lastErrorAt time.Time
	currentHost string
}

func newBalancerRing(capacity int) *balancerRing {
	if capacity <= 0 {
		capacity = 256
	}
	return &balancerRing{
		attempts: make([]Attempt, capacity),
		cap:      capacity,
	}
}

func (r *balancerRing) record(a Attempt) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts[r.head] = a
	r.head = (r.head + 1) % r.cap
	if r.count < r.cap {
		r.count++
	}
	r.lastAttempt = a.Time
	if a.Success {
		r.lastSuccess = a.Time
	} else {
		r.lastError = a.Err
		r.lastErrorAt = a.Time
	}
	if a.Host != "" {
		r.currentHost = a.Host
	}
}

// since returns a copy of all attempts within the given duration, newest first.
func (r *balancerRing) since(d time.Duration) []Attempt {
	cutoff := time.Now().Add(-d)
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Attempt, 0, r.count)
	// Walk the ring backwards from head-1 (most recent) to find entries newer than cutoff.
	for i := 0; i < r.count; i++ {
		idx := (r.head - 1 - i + r.cap) % r.cap
		a := r.attempts[idx]
		if a.Time.Before(cutoff) {
			break
		}
		out = append(out, a)
	}
	return out
}

// recentN returns up to n most recent attempts (newest first).
func (r *balancerRing) recentN(n int) []Attempt {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if n > r.count {
		n = r.count
	}
	out := make([]Attempt, 0, n)
	for i := 0; i < n; i++ {
		idx := (r.head - 1 - i + r.cap) % r.cap
		out = append(out, r.attempts[idx])
	}
	return out
}

// computeWindow aggregates a slice of attempts into a Window.
func computeWindow(attempts []Attempt) Window {
	w := Window{Total: len(attempts)}
	if w.Total == 0 {
		return w
	}
	latencies := make([]int64, 0, len(attempts))
	var totalLat int64
	for _, a := range attempts {
		if a.Success {
			w.Success++
		} else {
			w.Failure++
		}
		latencies = append(latencies, a.LatencyMs)
		totalLat += a.LatencyMs
	}
	w.SuccessRate = float64(w.Success) / float64(w.Total)
	w.AvgLatency = totalLat / int64(len(latencies))
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	w.P50Latency = percentile(latencies, 0.50)
	w.P95Latency = percentile(latencies, 0.95)
	w.P99Latency = percentile(latencies, 0.99)
	return w
}

func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
