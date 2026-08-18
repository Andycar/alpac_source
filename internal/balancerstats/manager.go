package balancerstats

import (
	"context"
	"strings"
	"sync"
	"time"
)

// Manager is the global balancer-stats coordinator. It owns one ring per
// balancer, tracks fallback host rotations, and exposes Snapshot() for the
// admin API and Subscribe() for live SSE streaming.
type Manager struct {
	mu      sync.RWMutex
	rings   map[string]*balancerRing // key: lowercase balancer name
	csRings map[string]*balancerRing // separate ring for checksearch probes
	ringCap int

	// Fallback configuration (per balancer): primary first, fallbacks after.
	fallbacks map[string][]string

	// Active host index per balancer (rotates within fallbacks list).
	hostIdx map[string]int

	// Per-host stats (lightweight: just success/fail counters reset every minute).
	hostStats map[string]map[string]*hostCounter // balancer → host → counter

	// Subscribers receive a sentinel signal whenever a new attempt is recorded.
	subMu  sync.Mutex
	subs   map[uint64]chan struct{}
	subSeq uint64

	// Configurable thresholds (also exposed for tests).
	fallbackFailWindow time.Duration // window over which to count fails for rotation
	fallbackFailLimit  int           // consecutive fails on a host before rotating
}

type hostCounter struct {
	success int
	failure int
	since   time.Time
}

// NewManager creates a new manager with sensible defaults.
func NewManager() *Manager {
	return &Manager{
		rings:              make(map[string]*balancerRing),
		csRings:            make(map[string]*balancerRing),
		ringCap:            256,
		fallbacks:          make(map[string][]string),
		hostIdx:            make(map[string]int),
		hostStats:          make(map[string]map[string]*hostCounter),
		subs:               make(map[uint64]chan struct{}),
		fallbackFailWindow: 60 * time.Second,
		fallbackFailLimit:  4,
	}
}

// SetFallbacks configures the host rotation list for a balancer.
// The first entry is the primary; subsequent entries are tried in order on
// repeated failures within fallbackFailWindow.
func (m *Manager) SetFallbacks(balancer string, hosts []string) {
	if balancer == "" {
		return
	}
	key := strings.ToLower(balancer)
	clean := make([]string, 0, len(hosts))
	for _, h := range hosts {
		h = strings.TrimSpace(h)
		if h != "" {
			clean = append(clean, h)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(clean) == 0 {
		delete(m.fallbacks, key)
		delete(m.hostIdx, key)
		return
	}
	m.fallbacks[key] = clean
	if _, ok := m.hostIdx[key]; !ok {
		m.hostIdx[key] = 0
	}
	if m.hostIdx[key] >= len(clean) {
		m.hostIdx[key] = 0
	}
}

// Fallbacks returns a copy of the configured fallback list for a balancer.
func (m *Manager) Fallbacks(balancer string) []string {
	key := strings.ToLower(balancer)
	m.mu.RLock()
	defer m.mu.RUnlock()
	src := m.fallbacks[key]
	if len(src) == 0 {
		return nil
	}
	out := make([]string, len(src))
	copy(out, src)
	return out
}

// CurrentHost returns the host currently selected for the balancer, or "" if
// no fallback list is configured. Use this from balancer code to choose the
// active host: `host := mgr.CurrentHost("collaps"); if host == "" { host = cfg.APIHost }`.
func (m *Manager) CurrentHost(balancer string) string {
	key := strings.ToLower(balancer)
	m.mu.RLock()
	defer m.mu.RUnlock()
	hosts := m.fallbacks[key]
	if len(hosts) == 0 {
		return ""
	}
	idx := m.hostIdx[key]
	if idx < 0 || idx >= len(hosts) {
		idx = 0
	}
	return hosts[idx]
}

// Record stores an attempt and may rotate the active host on repeated failures.
// This is the hot path — called once per HTTP request from balancerFetch.
func (m *Manager) Record(balancer string, success bool, latencyMs int64, status int, errMsg, host, url string) {
	if balancer == "" {
		return
	}
	key := strings.ToLower(balancer)

	m.mu.Lock()
	ring, ok := m.rings[key]
	if !ok {
		ring = newBalancerRing(m.ringCap)
		m.rings[key] = ring
	}

	// Update per-host counters.
	hosts := m.hostStats[key]
	if hosts == nil {
		hosts = make(map[string]*hostCounter)
		m.hostStats[key] = hosts
	}
	hc := hosts[host]
	if hc == nil || time.Since(hc.since) > 5*time.Minute {
		hc = &hostCounter{since: time.Now()}
		hosts[host] = hc
	}
	if success {
		hc.success++
	} else {
		hc.failure++
	}

	// Decide whether to rotate fallback host.
	rotate := false
	if !success && len(m.fallbacks[key]) > 1 {
		// Count failures in the last fallbackFailWindow on the current host.
		cutoff := time.Now().Add(-m.fallbackFailWindow)
		fails := 1 // count this failure
		for i := 0; i < ring.count && i < ring.cap; i++ {
			idx := (ring.head - 1 - i + ring.cap) % ring.cap
			a := ring.attempts[idx]
			if a.Time.Before(cutoff) {
				break
			}
			if a.Host == host && !a.Success {
				fails++
			}
			if a.Host == host && a.Success {
				// Recent success means we shouldn't rotate yet.
				fails = 0
				break
			}
		}
		if fails >= m.fallbackFailLimit {
			rotate = true
		}
	}
	if rotate {
		m.hostIdx[key] = (m.hostIdx[key] + 1) % len(m.fallbacks[key])
	}
	m.mu.Unlock()

	// Record under the ring's own lock (avoids holding manager lock).
	ring.record(Attempt{
		Time:      time.Now(),
		Success:   success,
		LatencyMs: latencyMs,
		Status:    status,
		Err:       truncErr(errMsg),
		Host:      host,
		URL:       truncURL(url),
	})

	m.notifySubscribers()
}

// RecordChecksearch stores a checksearch probe attempt in a SEPARATE ring buffer.
// This lets the dashboard show "is checksearch healthy for this balancer?"
// independently of stream-fetch attempts. The success flag here means "we got
// a valid HTTP response" — NOT "we found content". An empty response (no
// content for the queried film) still counts as success for telemetry.
func (m *Manager) RecordChecksearch(balancer string, success bool, latencyMs int64, status int, errMsg, host, url string) {
	if balancer == "" {
		return
	}
	key := strings.ToLower(balancer)
	m.mu.Lock()
	ring, ok := m.csRings[key]
	if !ok {
		ring = newBalancerRing(m.ringCap)
		m.csRings[key] = ring
	}
	m.mu.Unlock()

	ring.record(Attempt{
		Time:      time.Now(),
		Success:   success,
		LatencyMs: latencyMs,
		Status:    status,
		Err:       truncErr(errMsg),
		Host:      host,
		URL:       truncURL(url),
	})
	m.notifySubscribers()
}

// ChecksearchSnapshot returns a per-balancer summary of checksearch probes.
// The Snapshot shape mirrors what Snapshot() returns for regular attempts.
func (m *Manager) ChecksearchSnapshot(balancer string) map[string]Snapshot {
	m.mu.RLock()
	keys := make([]string, 0, len(m.csRings))
	if balancer != "" {
		k := strings.ToLower(balancer)
		if _, ok := m.csRings[k]; ok {
			keys = append(keys, k)
		}
	} else {
		for k := range m.csRings {
			keys = append(keys, k)
		}
	}
	m.mu.RUnlock()

	out := make(map[string]Snapshot, len(keys))
	for _, k := range keys {
		m.mu.RLock()
		ring := m.csRings[k]
		m.mu.RUnlock()
		if ring == nil {
			continue
		}

		ring.mu.RLock()
		snap := Snapshot{
			Balancer:    k,
			LastAttempt: ring.lastAttempt,
			LastSuccess: ring.lastSuccess,
			LastError:   ring.lastError,
			LastErrorAt: ring.lastErrorAt,
			Windows:     map[string]Window{},
		}
		ring.mu.RUnlock()

		snap.Windows["1m"] = computeWindow(ring.since(1 * time.Minute))
		snap.Windows["5m"] = computeWindow(ring.since(5 * time.Minute))
		snap.Windows["15m"] = computeWindow(ring.since(15 * time.Minute))
		snap.Windows["1h"] = computeWindow(ring.since(60 * time.Minute))

		out[k] = snap
	}
	return out
}

// Snapshot returns a per-balancer summary suitable for the dashboard.
// If 'balancer' is non-empty, only that balancer is returned (or empty map).
func (m *Manager) Snapshot(balancer string) map[string]Snapshot {
	m.mu.RLock()
	keys := make([]string, 0, len(m.rings))
	if balancer != "" {
		k := strings.ToLower(balancer)
		if _, ok := m.rings[k]; ok {
			keys = append(keys, k)
		}
	} else {
		for k := range m.rings {
			keys = append(keys, k)
		}
	}
	// Snapshot fallback state under the same lock.
	hostByKey := make(map[string]string, len(keys))
	for _, k := range keys {
		hosts := m.fallbacks[k]
		if len(hosts) == 0 {
			continue
		}
		idx := m.hostIdx[k]
		if idx >= 0 && idx < len(hosts) {
			hostByKey[k] = hosts[idx]
		}
	}
	m.mu.RUnlock()

	out := make(map[string]Snapshot, len(keys))
	for _, k := range keys {
		m.mu.RLock()
		ring := m.rings[k]
		m.mu.RUnlock()
		if ring == nil {
			continue
		}

		ring.mu.RLock()
		snap := Snapshot{
			Balancer:    k,
			LastAttempt: ring.lastAttempt,
			LastSuccess: ring.lastSuccess,
			LastError:   ring.lastError,
			LastErrorAt: ring.lastErrorAt,
			CurrentHost: hostByKey[k],
			Windows:     map[string]Window{},
		}
		if snap.CurrentHost == "" {
			snap.CurrentHost = ring.currentHost
		}
		ring.mu.RUnlock()

		snap.Windows["1m"] = computeWindow(ring.since(1 * time.Minute))
		snap.Windows["5m"] = computeWindow(ring.since(5 * time.Minute))
		snap.Windows["15m"] = computeWindow(ring.since(15 * time.Minute))
		snap.Windows["1h"] = computeWindow(ring.since(60 * time.Minute))

		// Recent errors (up to 5) for the UI.
		recent := ring.recentN(20)
		for _, a := range recent {
			if a.Success {
				continue
			}
			snap.RecentErrs = append(snap.RecentErrs, RecentError{
				Time:   a.Time,
				Status: a.Status,
				Err:    a.Err,
				Host:   a.Host,
				URL:    a.URL,
			})
			if len(snap.RecentErrs) >= 5 {
				break
			}
		}

		out[k] = snap
	}
	return out
}

// Subscribe returns a channel that receives a struct{} signal whenever a new
// attempt is recorded. The returned id should be passed to Unsubscribe.
// The channel buffer is 1 — if the consumer is slow, signals coalesce.
func (m *Manager) Subscribe() (uint64, chan struct{}) {
	m.subMu.Lock()
	defer m.subMu.Unlock()
	m.subSeq++
	id := m.subSeq
	ch := make(chan struct{}, 1)
	m.subs[id] = ch
	return id, ch
}

// Unsubscribe removes a previously registered subscriber.
func (m *Manager) Unsubscribe(id uint64) {
	m.subMu.Lock()
	defer m.subMu.Unlock()
	if ch, ok := m.subs[id]; ok {
		close(ch)
		delete(m.subs, id)
	}
}

func (m *Manager) notifySubscribers() {
	m.subMu.Lock()
	defer m.subMu.Unlock()
	for _, ch := range m.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Reset clears stats for the given balancer (or all if "").
// Used by the admin "reset stats" button.
func (m *Manager) Reset(balancer string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if balancer == "" {
		m.rings = make(map[string]*balancerRing)
		m.csRings = make(map[string]*balancerRing)
		m.hostStats = make(map[string]map[string]*hostCounter)
		// keep fallbacks and hostIdx as-is — those are config, not stats
		return
	}
	key := strings.ToLower(balancer)
	delete(m.rings, key)
	delete(m.csRings, key)
	delete(m.hostStats, key)
}

// RotateHost manually advances to the next fallback host for the balancer.
// Used by the admin "force rotate" button.
func (m *Manager) RotateHost(balancer string) string {
	key := strings.ToLower(balancer)
	m.mu.Lock()
	defer m.mu.Unlock()
	hosts := m.fallbacks[key]
	if len(hosts) <= 1 {
		return ""
	}
	m.hostIdx[key] = (m.hostIdx[key] + 1) % len(hosts)
	return hosts[m.hostIdx[key]]
}

// Run starts background maintenance (periodic pruning of old entries).
// Currently a no-op since rings are bounded — kept for future expansion.
func (m *Manager) Run(ctx context.Context) {
	<-ctx.Done()
}

// ManagerSetFailLimit overrides the fallback rotation threshold. Exported as
// a free function (not a method) to keep the package's small public surface
// focused on the hot-path API; this is set once at startup.
func ManagerSetFailLimit(m *Manager, limit int) {
	if m == nil || limit <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fallbackFailLimit = limit
}

// FailLimit returns the configured fallback fail limit.
func (m *Manager) FailLimit() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.fallbackFailLimit
}

func truncErr(s string) string {
	const max = 240
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

func truncURL(u string) string {
	const max = 200
	if len(u) > max {
		return u[:max] + "…"
	}
	return u
}
