package balancerhealth

import (
	"context"
	"crypto/tls"
	stdjson "encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

// HealthStatus represents the health state of a single balancer.
type HealthStatus struct {
	Healthy          bool      `json:"healthy"`
	AutoDisabled     bool      `json:"auto_disabled"`
	LastCheckTime    time.Time `json:"last_check_time"`
	LastCheckLatency int64     `json:"last_check_latency_ms"`
	LastCheckStatus  int       `json:"last_check_status"`
	LastCheckError   string    `json:"last_check_error,omitempty"`
	ConsecutiveFails int       `json:"consecutive_fails"`
	ConsecutiveOK    int       `json:"consecutive_ok"`
}

// HealthChecker runs periodic health probes against balancer hosts.
type HealthChecker struct {
	mu       sync.RWMutex
	statuses map[string]*HealthStatus // key: lowercase balancer name

	enabled          bool          // master switch; when false the loop sleeps and probe() is a no-op
	failThreshold    int           // consecutive fails before auto-disable (default 3)
	recoverThreshold int           // consecutive OKs before auto-re-enable (default 2)
	interval         time.Duration // check interval (default 60s)
	timeout          time.Duration // per-probe timeout (default 10s)

	// excluded is the per-balancer opt-out set. Keys are lowercase. A
	// balancer in this set is skipped during runOnce() so no new probes
	// are issued — but its existing status entry is preserved so the row
	// is still visible in the admin UI with its last-known state. This
	// does NOT clear AutoDisabled flags: lite_events.go enforcement keeps
	// honouring them, the operator can clear them manually if needed.
	excluded map[string]struct{}

	persistPath string // path for state persistence (set by LoadFromDisk)

	// targetsFn resolves the balancers to probe each pass. Injected by the host
	// (SetTargets) so this package stays free of the httpapi config/plugin registry.
	targetsFn func() []Target

	// settingsCh wakes the loop when ApplySettings changes interval/enabled
	// so the next probe pass uses the new cadence without waiting out the
	// old ticker. Buffered=1 so writers never block; coalescing is fine,
	// we just want at-least-once delivery.
	settingsCh chan struct{}

	cancel context.CancelFunc
}

// HealthSettings is a public mirror of the tunable fields used by the admin
// API. Kept separate from config.HealthCheckConfig so the httpapi package
// stays import-free of internal/config.
type HealthSettings struct {
	Enabled          bool `json:"enabled"`
	IntervalSec      int  `json:"interval_sec"`
	TimeoutSec       int  `json:"timeout_sec"`
	FailThreshold    int  `json:"fail_threshold"`
	RecoverThreshold int  `json:"recover_threshold"`

	// Excluded names (lowercase, sorted on read) — balancers in this list
	// are skipped during probing. Stored as a slice rather than a map so
	// the JSON file is human-readable.
	Excluded []string `json:"excluded,omitempty"`
}

// healthcheckPersistFile is the relative path where auto-disable state is
// saved across restarts. Without persistence a process restart silently
// re-enables every dead balancer for one minute (until the first healthcheck
// pass marks them down again), which mid-incident produces a flood of
// 5xx-from-balancer responses to clients.
const healthcheckPersistFile = "database/healthcheck.json"

// healthcheckSettingsFile is the relative path where the admin-tuned
// runtime settings live. Kept separate from healthcheckPersistFile so the
// status map and the settings can be edited / wiped independently.
const healthcheckSettingsFile = "database/healthcheck_settings.json"

// LoadHealthcheckSettingsOverride reads the admin override JSON. Returns
// (zero, false) when the file does not exist OR is corrupt — caller falls
// back to the TOML defaults in that case.
func LoadHealthcheckSettingsOverride(repoRoot string) (HealthSettings, bool) {
	path := filepath.Join(repoRoot, healthcheckSettingsFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return HealthSettings{}, false
	}
	var s HealthSettings
	if err := stdjson.Unmarshal(data, &s); err != nil {
		log.Warn().Err(err).Str("path", path).Msg("healthcheck: settings override is corrupt; ignoring")
		return HealthSettings{}, false
	}
	return s, true
}

// SaveHealthcheckSettingsOverride atomically writes the admin override JSON.
// Caller is the admin handler; settings here outlive the process and win
// over the TOML defaults at next boot.
func SaveHealthcheckSettingsOverride(repoRoot string, s HealthSettings) error {
	path := filepath.Join(repoRoot, healthcheckSettingsFile)
	data, err := stdjson.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// globalHealthChecker is set when the server starts the health checker.
// Wrapped in atomic.Pointer so the read-side (called from /lite/events on
// every request) is lock-free and has a proper happens-before relation with
// the init-time store.
var globalHealthChecker atomic.Pointer[HealthChecker]

// GetGlobalHealthChecker returns the active health checker or nil.
func GetGlobalHealthChecker() *HealthChecker { return globalHealthChecker.Load() }

// SetGlobalHealthChecker is called from server bootstrap to expose the
// checker process-wide. Subsequent calls atomically replace.
func SetGlobalHealthChecker(h *HealthChecker) { globalHealthChecker.Store(h) }

// NewHealthChecker creates a new health checker with default thresholds.
// RunOnce triggers one immediate probe pass (admin "check now" action), outside
// the periodic loop.
func (hc *HealthChecker) RunOnce() { hc.runOnce() }

// SetTargets injects the probe-target resolver. Call before Start.
func (hc *HealthChecker) SetTargets(fn func() []Target) {
	hc.mu.Lock()
	hc.targetsFn = fn
	hc.mu.Unlock()
}

// NewHealthChecker creates a new health checker with default thresholds. Call
// SetTargets to inject the probe-target resolver before Start.
func NewHealthChecker() *HealthChecker {
	return &HealthChecker{
		statuses:         make(map[string]*HealthStatus),
		enabled:          true,
		failThreshold:    3,
		recoverThreshold: 2,
		interval:         60 * time.Second,
		timeout:          10 * time.Second,
		excluded:         make(map[string]struct{}),
		settingsCh:       make(chan struct{}, 1),
	}
}

// Settings returns the current tunables. Snapshot is by value, so callers
// don't need to hold any lock.
func (hc *HealthChecker) Settings() HealthSettings {
	hc.mu.RLock()
	defer hc.mu.RUnlock()
	out := HealthSettings{
		Enabled:          hc.enabled,
		IntervalSec:      int(hc.interval / time.Second),
		TimeoutSec:       int(hc.timeout / time.Second),
		FailThreshold:    hc.failThreshold,
		RecoverThreshold: hc.recoverThreshold,
	}
	if len(hc.excluded) > 0 {
		out.Excluded = make([]string, 0, len(hc.excluded))
		for name := range hc.excluded {
			out.Excluded = append(out.Excluded, name)
		}
		sort.Strings(out.Excluded)
	}
	return out
}

// IsExcluded reports whether the given balancer is opted out of probing.
// Case-insensitive. Lock-free reads are not safe here because the set can
// mutate; callers should treat this as a one-shot check, not a hot path.
func (hc *HealthChecker) IsExcluded(balancer string) bool {
	if balancer == "" {
		return false
	}
	hc.mu.RLock()
	defer hc.mu.RUnlock()
	_, ok := hc.excluded[strings.ToLower(balancer)]
	return ok
}

// ApplySettings atomically swaps the tunables. Values out of safe bounds are
// clamped silently — callers that need validation should do it before calling.
// The loop is woken via settingsCh so a new interval kicks in on the next
// pass instead of waiting out the old ticker.
func (hc *HealthChecker) ApplySettings(s HealthSettings) {
	if s.IntervalSec < 5 {
		s.IntervalSec = 5
	} else if s.IntervalSec > 3600 {
		s.IntervalSec = 3600
	}
	if s.TimeoutSec < 1 {
		s.TimeoutSec = 1
	} else if s.TimeoutSec > 60 {
		s.TimeoutSec = 60
	}
	if s.FailThreshold < 1 {
		s.FailThreshold = 1
	} else if s.FailThreshold > 50 {
		s.FailThreshold = 50
	}
	if s.RecoverThreshold < 1 {
		s.RecoverThreshold = 1
	} else if s.RecoverThreshold > 50 {
		s.RecoverThreshold = 50
	}

	// Normalise the exclusion list: lowercase + trim + dedupe. An empty
	// or whitespace-only entry is dropped (UI sometimes sends "" for an
	// unfilled row).
	excluded := make(map[string]struct{}, len(s.Excluded))
	for _, name := range s.Excluded {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		excluded[name] = struct{}{}
	}

	hc.mu.Lock()
	hc.enabled = s.Enabled
	hc.interval = time.Duration(s.IntervalSec) * time.Second
	hc.timeout = time.Duration(s.TimeoutSec) * time.Second
	hc.failThreshold = s.FailThreshold
	hc.recoverThreshold = s.RecoverThreshold
	hc.excluded = excluded
	hc.mu.Unlock()

	// Wake the loop. Non-blocking; coalesce if a wake is already pending.
	if hc.settingsCh != nil {
		select {
		case hc.settingsCh <- struct{}{}:
		default:
		}
	}
}

// SetExcluded toggles a single balancer's exclusion flag and persists the
// change to the admin override file. Returns the new state (true=excluded).
// This is the hot-path entrypoint used by per-row UI toggles — it avoids
// re-sending the full settings payload just to flip one bit.
func (hc *HealthChecker) SetExcluded(balancer string, excluded bool) bool {
	name := strings.ToLower(strings.TrimSpace(balancer))
	if name == "" {
		return false
	}
	hc.mu.Lock()
	if hc.excluded == nil {
		hc.excluded = make(map[string]struct{})
	}
	if excluded {
		hc.excluded[name] = struct{}{}
	} else {
		delete(hc.excluded, name)
	}
	hc.mu.Unlock()

	// No need to wake the loop here — the next runOnce() naturally
	// observes the new excluded set under its own lock.
	return excluded
}

// ResetStatus clears the running counters and AutoDisabled flag for the
// given balancer (case-insensitive). Pass an empty string to clear ALL
// balancers. Returns the number of entries affected. The persisted state
// is updated so the change survives a restart.
func (hc *HealthChecker) ResetStatus(balancer string) int {
	hc.mu.Lock()
	affected := 0
	if balancer == "" {
		affected = len(hc.statuses)
		hc.statuses = make(map[string]*HealthStatus)
	} else {
		key := strings.ToLower(balancer)
		if _, ok := hc.statuses[key]; ok {
			delete(hc.statuses, key)
			affected = 1
		}
	}
	hc.mu.Unlock()
	hc.saveToDisk()
	return affected
}

// ManualReEnable clears the AutoDisabled flag and resets counters for the
// given balancer without removing its history. Useful when an operator
// wants to give a flaky balancer one more chance without waiting for the
// recover threshold.
func (hc *HealthChecker) ManualReEnable(balancer string) bool {
	if balancer == "" {
		return false
	}
	hc.mu.Lock()
	key := strings.ToLower(balancer)
	s, ok := hc.statuses[key]
	if !ok {
		hc.mu.Unlock()
		return false
	}
	s.AutoDisabled = false
	s.ConsecutiveFails = 0
	s.ConsecutiveOK = 0
	s.LastCheckError = ""
	hc.mu.Unlock()
	hc.saveToDisk()
	return true
}

// Start launches the background health check goroutine.
func (hc *HealthChecker) Start(ctx context.Context) {
	ctx, hc.cancel = context.WithCancel(ctx)
	go hc.loop(ctx)
	hc.mu.RLock()
	interval := hc.interval
	enabled := hc.enabled
	hc.mu.RUnlock()
	log.Info().Dur("interval", interval).Bool("enabled", enabled).Msg("healthcheck: started")
}

// Stop cancels the background goroutine.
func (hc *HealthChecker) Stop() {
	if hc.cancel != nil {
		hc.cancel()
	}
}

// IsHealthy returns whether a balancer is currently healthy.
// Returns true for unknown balancers (not yet checked).
func (hc *HealthChecker) IsHealthy(balancer string) bool {
	hc.mu.RLock()
	defer hc.mu.RUnlock()
	s, ok := hc.statuses[strings.ToLower(balancer)]
	if !ok {
		return true // unknown = assume healthy
	}
	return s.Healthy
}

// IsAutoDisabled returns whether a balancer was auto-disabled by healthcheck.
func (hc *HealthChecker) IsAutoDisabled(balancer string) bool {
	hc.mu.RLock()
	defer hc.mu.RUnlock()
	s, ok := hc.statuses[strings.ToLower(balancer)]
	if !ok {
		return false
	}
	return s.AutoDisabled
}

// Snapshot returns a copy of all health statuses for the dashboard API.
func (hc *HealthChecker) Snapshot() map[string]HealthStatus {
	hc.mu.RLock()
	defer hc.mu.RUnlock()
	out := make(map[string]HealthStatus, len(hc.statuses))
	for k, v := range hc.statuses {
		out[k] = *v
	}
	return out
}

// LoadFromDisk hydrates the checker's state from the persistence file. Safe
// to call before Start — usually invoked from server bootstrap right after
// NewHealthChecker. Errors are logged but not fatal: a missing/corrupt file
// just means we start with an empty state and rebuild over the first cycle.
func (hc *HealthChecker) LoadFromDisk(repoRoot string) {
	path := filepath.Join(repoRoot, healthcheckPersistFile)
	hc.persistPath = path

	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Warn().Err(err).Str("path", path).Msg("healthcheck: load failed; starting fresh")
		}
		return
	}
	var snap map[string]HealthStatus
	if err := stdjson.Unmarshal(data, &snap); err != nil {
		log.Warn().Err(err).Str("path", path).Msg("healthcheck: persist file is corrupt; starting fresh")
		return
	}
	hc.mu.Lock()
	loaded := 0
	for k, v := range snap {
		cp := v // copy
		hc.statuses[strings.ToLower(k)] = &cp
		if cp.AutoDisabled {
			loaded++
		}
	}
	hc.mu.Unlock()
	if loaded > 0 {
		log.Info().Int("auto_disabled", loaded).Str("path", path).Msg("healthcheck: restored persisted state")
	}
}

// saveToDisk writes the current statuses to the persistence file. Called
// (best-effort, errors logged) whenever AutoDisabled flips state. Skipped if
// persistPath is empty — i.e. LoadFromDisk wasn't called.
func (hc *HealthChecker) saveToDisk() {
	if hc.persistPath == "" {
		return
	}
	snap := hc.Snapshot()
	data, err := stdjson.MarshalIndent(snap, "", "  ")
	if err != nil {
		log.Warn().Err(err).Msg("healthcheck: marshal state failed")
		return
	}
	// Atomic write: tmp + rename so a crash mid-write doesn't truncate the file.
	tmp := hc.persistPath + ".tmp"
	if err := os.MkdirAll(filepath.Dir(hc.persistPath), 0o755); err != nil {
		log.Warn().Err(err).Msg("healthcheck: mkdir failed")
		return
	}
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		log.Warn().Err(err).Msg("healthcheck: tmp write failed")
		return
	}
	if err := os.Rename(tmp, hc.persistPath); err != nil {
		log.Warn().Err(err).Msg("healthcheck: rename failed")
	}
}

func (hc *HealthChecker) loop(ctx context.Context) {
	// Run first check immediately after a short delay, but only if the
	// checker is currently enabled. If disabled, fall through to the ticker
	// loop — operators can flip it back on via the admin API at any time.
	select {
	case <-time.After(5 * time.Second):
		if hc.isEnabled() {
			hc.runOnce()
		}
	case <-ctx.Done():
		return
	}

	hc.mu.RLock()
	interval := hc.interval
	hc.mu.RUnlock()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if hc.isEnabled() {
				hc.runOnce()
			}
		case <-hc.settingsCh:
			// ApplySettings changed something — rebuild the ticker so the
			// new interval takes effect immediately and run once now if
			// the checker was just (re-)enabled.
			hc.mu.RLock()
			newInterval := hc.interval
			enabled := hc.enabled
			hc.mu.RUnlock()
			if newInterval != interval {
				ticker.Stop()
				ticker = time.NewTicker(newInterval)
				interval = newInterval
			}
			if enabled {
				// Run a probe right after a settings change so the UI
				// reflects the new state without waiting an interval.
				go hc.runOnce()
			}
		case <-ctx.Done():
			return
		}
	}
}

func (hc *HealthChecker) isEnabled() bool {
	hc.mu.RLock()
	defer hc.mu.RUnlock()
	return hc.enabled
}

// Target is one balancer to probe: its display name + the host to hit. The host
// resolution (which balancers, from which config sections) is the host app's
// concern and is injected via SetTargets — this keeps the checker a pure leaf
// package with no dependency on the httpapi config/plugin registry.
type Target struct {
	Name string
	Host string
}

func (hc *HealthChecker) runOnce() {
	if hc.targetsFn == nil {
		return
	}
	targets := hc.targetsFn()

	// Snapshot the exclusion set once so a long pass doesn't repeatedly
	// pay the RLock for each balancer.
	hc.mu.RLock()
	excluded := make(map[string]struct{}, len(hc.excluded))
	for k := range hc.excluded {
		excluded[k] = struct{}{}
	}
	hc.mu.RUnlock()

	var wg sync.WaitGroup
	for _, t := range targets {
		if t.Host == "" {
			continue
		}
		if _, skip := excluded[strings.ToLower(t.Name)]; skip {
			continue
		}
		wg.Add(1)
		go func(name, host string) {
			defer wg.Done()
			hc.probe(name, host)
		}(t.Name, t.Host)
	}
	wg.Wait()
}

func (hc *HealthChecker) probe(name, host string) {
	if !strings.HasPrefix(host, "http") {
		host = "https://" + host
	}

	client := &http.Client{
		Timeout: hc.timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, // healthcheck only probes availability, skip cert chain validation
			},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}

	start := time.Now()
	resp, err := client.Head(host)
	latency := time.Since(start).Milliseconds()

	hc.mu.Lock()
	defer hc.mu.Unlock()

	lowerName := strings.ToLower(name)
	status, exists := hc.statuses[lowerName]
	if !exists {
		status = &HealthStatus{}
		hc.statuses[lowerName] = status
	}

	status.LastCheckTime = time.Now()
	status.LastCheckLatency = latency

	if err != nil {
		status.LastCheckError = err.Error()
		status.LastCheckStatus = 0
		status.Healthy = false
		status.ConsecutiveFails++
		status.ConsecutiveOK = 0
	} else {
		resp.Body.Close()
		status.LastCheckStatus = resp.StatusCode
		status.LastCheckError = ""
		if resp.StatusCode < 500 {
			status.Healthy = true
			status.ConsecutiveOK++
			status.ConsecutiveFails = 0
		} else {
			status.Healthy = false
			status.ConsecutiveFails++
			status.ConsecutiveOK = 0
		}
	}

	persist := false

	// Auto-disable after N consecutive failures
	if status.ConsecutiveFails >= hc.failThreshold && !status.AutoDisabled {
		status.AutoDisabled = true
		persist = true
		log.Warn().
			Str("balancer", name).
			Int("consecutive_fails", status.ConsecutiveFails).
			Msg("healthcheck: auto-disabled balancer")
	}

	// Auto-re-enable after M consecutive successes
	if status.AutoDisabled && status.ConsecutiveOK >= hc.recoverThreshold {
		status.AutoDisabled = false
		persist = true
		log.Info().
			Str("balancer", name).
			Int("consecutive_ok", status.ConsecutiveOK).
			Msg("healthcheck: auto-re-enabled balancer")
	}

	if persist {
		// saveToDisk takes RLock internally; release the write lock first.
		hc.mu.Unlock()
		hc.saveToDisk()
		hc.mu.Lock() // re-acquire so the deferred Unlock pairs correctly
	}
}
