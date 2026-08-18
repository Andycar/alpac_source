package sidecar

import (
	"fmt"

	"github.com/rs/zerolog/log"
)

// PoolConfig describes one proxy entry for NewPool.
type PoolConfig struct {
	URI       string   // proxy URI (any supported scheme)
	Balancers []string
	Label     string
	Engine    string // "xray", "mihomo", or "" (auto-detect)
}

// PoolEntry represents a running sidecar with its associated balancers.
type PoolEntry struct {
	Manager   *Manager
	Balancers []string // balancer names routed through this proxy
	Label     string   // human-readable label
}

// Pool manages multiple sidecar processes.
type Pool struct {
	entries  []PoolEntry
	failures []PoolFailure
}

// PoolFailure records a proxy entry that failed to start. The pool keeps these
// so failures stay visible (admin status / logs) instead of silently vanishing.
type PoolFailure struct {
	Index  int    `json:"index"`
	Label  string `json:"label"`
	Port   int    `json:"port"`
	Engine string `json:"engine"`
	Error  string `json:"error"`
}

// PoolStatus describes the runtime status of one sidecar.
type PoolStatus struct {
	Label     string   `json:"label"`
	Engine    string   `json:"engine"`
	PID       int      `json:"pid"`
	SOCKSAddr string   `json:"socks_addr"`
	Balancers []string `json:"balancers"`
	Alive     bool     `json:"alive"`
	UptimeSec float64  `json:"uptime_sec"`
}

// NewPool starts multiple sidecar processes, each on a different SOCKS5 port.
// basePort is the first port; subsequent entries use basePort+1, +2, etc.
// parseURI is the URI parser function (typically uri.Parse).
func NewPool(configs []PoolConfig, binDir string, basePort int, engines map[EngineType]Engine, parseURI func(string) (ProxyOutbound, EngineType, error)) (*Pool, error) {
	if basePort <= 0 {
		basePort = 40000
	}
	var entries []PoolEntry
	var failures []PoolFailure
	var firstErr error
	firstIdx, firstLabel := 0, ""
	configured := 0
	for i, pc := range configs {
		if pc.URI == "" {
			continue
		}
		configured++
		port := basePort + i
		mgr, err := StartSidecar(pc.URI, pc.Engine, binDir, port, engines, parseURI)
		if err != nil {
			// One bad proxy must not take down the whole pool: log it, record
			// the failure, and keep starting the remaining entries.
			log.Error().
				Err(err).
				Int("idx", i).
				Str("label", pc.Label).
				Int("port", port).
				Str("engine", pc.Engine).
				Msg("sidecar pool: entry failed to start — skipping, others continue")
			failures = append(failures, PoolFailure{
				Index:  i,
				Label:  pc.Label,
				Port:   port,
				Engine: pc.Engine,
				Error:  err.Error(),
			})
			if firstErr == nil {
				firstErr, firstIdx, firstLabel = err, i, pc.Label
			}
			continue
		}
		entries = append(entries, PoolEntry{
			Manager:   mgr,
			Balancers: pc.Balancers,
			Label:     pc.Label,
		})
		log.Info().
			Int("idx", i).
			Str("label", pc.Label).
			Str("engine", string(mgr.Engine())).
			Str("socks", mgr.SOCKSAddr()).
			Strs("balancers", pc.Balancers).
			Msg("sidecar pool: entry started")
	}

	pool := &Pool{entries: entries, failures: failures}

	// Only fail hard when entries were configured but none came up at all — a
	// total outage. Partial failures return a usable pool plus recorded
	// failures, so the working proxies stay up.
	if configured > 0 && len(entries) == 0 {
		return pool, fmt.Errorf("sidecar pool entry %d (%s): %w", firstIdx, firstLabel, firstErr)
	}
	if len(failures) > 0 {
		log.Warn().
			Int("started", len(entries)).
			Int("failed", len(failures)).
			Msg("sidecar pool: started with some entries down")
	}
	return pool, nil
}

// Entries returns all pool entries.
func (p *Pool) Entries() []PoolEntry {
	if p == nil {
		return nil
	}
	return p.entries
}

// Failures returns the proxy entries that failed to start (empty if all are up).
func (p *Pool) Failures() []PoolFailure {
	if p == nil {
		return nil
	}
	return p.failures
}

// Status returns the runtime status of all sidecar processes.
func (p *Pool) Status() []PoolStatus {
	if p == nil {
		return nil
	}
	out := make([]PoolStatus, 0, len(p.entries))
	for _, e := range p.entries {
		out = append(out, PoolStatus{
			Label:     e.Label,
			Engine:    string(e.Manager.Engine()),
			PID:       e.Manager.PID(),
			SOCKSAddr: e.Manager.SOCKSAddr(),
			Balancers: e.Balancers,
			Alive:     e.Manager.Alive(),
			UptimeSec: e.Manager.UptimeSeconds(),
		})
	}
	return out
}

// StopAll gracefully terminates all sidecar processes in the pool.
func (p *Pool) StopAll() {
	if p == nil {
		return
	}
	for _, e := range p.entries {
		e.Manager.Stop()
	}
}
