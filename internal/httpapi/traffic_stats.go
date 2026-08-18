package httpapi

import (
	"sync"
	"sync/atomic"
	"time"
)

// trafficStats tracks bandwidth (bytes) and per-balancer request/error counts.
// Data is in-memory with 2-hour retention, matching requestStatsTracker pattern.
type trafficStats struct {
	mu sync.Mutex

	// Total bytes proxied via /proxy/* per minute bucket.
	proxyBytes map[int64]int64

	// Total bytes through middleware (all responses) per minute.
	totalBytes map[int64]int64

	// Per-balancer request counts: balancer -> minute -> count.
	balancerReqs map[string]map[int64]int64

	// Per-balancer error counts (HTTP >=400 or network error): balancer -> minute -> count.
	balancerErrs map[string]map[int64]int64

	// Currently active proxy connections.
	activeProxy atomic.Int64
}

// TrafficSnapshot is the JSON-serializable aggregate returned by Snapshot().
type TrafficSnapshot struct {
	TotalBytesMin     int64                    `json:"total_bytes_min"`
	TotalBytesHour    int64                    `json:"total_bytes_hour"`
	ProxyBytesMin     int64                    `json:"proxy_bytes_min"`
	ProxyBytesHour    int64                    `json:"proxy_bytes_hour"`
	ActiveProxy       int64                    `json:"active_proxy"`
	BalancerStats     map[string]*BalancerStat `json:"balancer_stats"`
	ProxyBytesHistory [10]int64                `json:"proxy_bytes_history"` // last 10 minutes
}

// BalancerStat holds per-balancer request/error counters.
type BalancerStat struct {
	ReqsMin  int64 `json:"reqs_min"`
	ReqsHour int64 `json:"reqs_hour"`
	ErrsMin  int64 `json:"errs_min"`
	ErrsHour int64 `json:"errs_hour"`
}

func newTrafficStats() *trafficStats {
	return &trafficStats{
		proxyBytes:   make(map[int64]int64),
		totalBytes:   make(map[int64]int64),
		balancerReqs: make(map[string]map[int64]int64),
		balancerErrs: make(map[string]map[int64]int64),
	}
}

// RecordProxy adds a proxy response's byte count and request to the stats.
// Called from proxyapi via the TrafficRecorder callback.
func (t *trafficStats) RecordProxy(plugin string, bytes int64, isError bool) {
	now := time.Now().UTC().Truncate(time.Minute).Unix()

	t.mu.Lock()
	defer t.mu.Unlock()

	t.proxyBytes[now] += bytes

	if plugin != "" {
		if t.balancerReqs[plugin] == nil {
			t.balancerReqs[plugin] = make(map[int64]int64)
		}
		t.balancerReqs[plugin][now]++

		if isError {
			if t.balancerErrs[plugin] == nil {
				t.balancerErrs[plugin] = make(map[int64]int64)
			}
			t.balancerErrs[plugin][now]++
		}
	}

	t.cleanupLocked(now)
}

// RecordTotal adds a response's byte count to total traffic stats.
// Called from loggingMiddleware.
func (t *trafficStats) RecordTotal(bytes int64) {
	if bytes <= 0 {
		return
	}
	now := time.Now().UTC().Truncate(time.Minute).Unix()

	t.mu.Lock()
	t.totalBytes[now] += bytes
	t.mu.Unlock()
}

// BeginProxy increments the active proxy counter and returns a done function.
func (t *trafficStats) BeginProxy() func() {
	t.activeProxy.Add(1)
	return func() { t.activeProxy.Add(-1) }
}

// Snapshot returns an aggregated view of all traffic stats for the dashboard API.
func (t *trafficStats) Snapshot() TrafficSnapshot {
	now := time.Now().UTC().Truncate(time.Minute)
	key := now.Unix()

	t.mu.Lock()
	defer t.mu.Unlock()
	t.cleanupLocked(key)

	snap := TrafficSnapshot{
		ActiveProxy:   t.activeProxy.Load(),
		BalancerStats: make(map[string]*BalancerStat),
	}

	// Current minute
	snap.TotalBytesMin = t.totalBytes[key]
	snap.ProxyBytesMin = t.proxyBytes[key]

	// Last 60 minutes
	for i := range 60 {
		mk := now.Add(-time.Duration(i) * time.Minute).Unix()
		snap.TotalBytesHour += t.totalBytes[mk]
		snap.ProxyBytesHour += t.proxyBytes[mk]
	}

	// Proxy bytes history — last 10 minutes (index 0 = -9min, index 9 = now)
	for i := range 10 {
		mk := now.Add(-time.Duration(9-i) * time.Minute).Unix()
		snap.ProxyBytesHistory[i] = t.proxyBytes[mk]
	}

	// Per-balancer aggregation
	allBalancers := make(map[string]struct{})
	for name := range t.balancerReqs {
		allBalancers[name] = struct{}{}
	}
	for name := range t.balancerErrs {
		allBalancers[name] = struct{}{}
	}

	for name := range allBalancers {
		bs := &BalancerStat{}

		if reqs, ok := t.balancerReqs[name]; ok {
			bs.ReqsMin = reqs[key]
			for i := range 60 {
				mk := now.Add(-time.Duration(i) * time.Minute).Unix()
				bs.ReqsHour += reqs[mk]
			}
		}
		if errs, ok := t.balancerErrs[name]; ok {
			bs.ErrsMin = errs[key]
			for i := range 60 {
				mk := now.Add(-time.Duration(i) * time.Minute).Unix()
				bs.ErrsHour += errs[mk]
			}
		}

		snap.BalancerStats[name] = bs
	}

	return snap
}

// cleanupLocked removes data older than 2 hours. Must hold t.mu.
func (t *trafficStats) cleanupLocked(currentMinute int64) {
	keepFrom := currentMinute - int64((2*time.Hour)/time.Minute)

	for key := range t.proxyBytes {
		if key < keepFrom {
			delete(t.proxyBytes, key)
		}
	}
	for key := range t.totalBytes {
		if key < keepFrom {
			delete(t.totalBytes, key)
		}
	}
	for name, m := range t.balancerReqs {
		for key := range m {
			if key < keepFrom {
				delete(m, key)
			}
		}
		if len(m) == 0 {
			delete(t.balancerReqs, name)
		}
	}
	for name, m := range t.balancerErrs {
		for key := range m {
			if key < keepFrom {
				delete(m, key)
			}
		}
		if len(m) == 0 {
			delete(t.balancerErrs, name)
		}
	}
}

var runtimeTrafficStats = newTrafficStats()
