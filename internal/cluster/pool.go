package cluster

import (
	"context"
	"math"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"

	"github.com/rs/zerolog/log"
)

// Node represents a single backend lampac-go instance (runtime state).
type Node struct {
	ID          string
	Name        string
	Host        string
	Weight      int
	Region      string
	Enabled     bool

	healthy     atomic.Bool
	activeConns atomic.Int64
	totalServed atomic.Int64
	totalFailed atomic.Int64
	bytesOut    atomic.Int64 // bytes streamed back to clients via this node
	lastLatency atomic.Int64 // ms — last probe latency
	avgLatency  atomic.Int64 // ms — EWMA across probe + real requests
	lastCheck   atomic.Value // time.Time
	lastError   atomic.Value // string

	// Per-balancer counters + sliding-window unique-client tracker.
	// Guarded by statsMu — these grow per request so RWMutex avoids
	// blocking readers when admin UI polls.
	statsMu       sync.RWMutex
	byBalancer    map[string]int64
	recentClients map[string]time.Time // IP → last seen

	// Probe history ring buffer — last N health-probe outcomes (true=ok).
	// Used to compute availability % over recent past (~last hour at 30s interval).
	probeHistMu  sync.Mutex
	probeHist    []bool
	probeHistCap int

	consecFails int // guarded by pool.mu
	consecOK    int // guarded by pool.mu
}

// recordProbe appends a probe outcome to the history ring buffer.
func (n *Node) recordProbe(success bool) {
	n.probeHistMu.Lock()
	defer n.probeHistMu.Unlock()
	if n.probeHistCap == 0 {
		n.probeHistCap = 120 // ~1h at 30s interval
	}
	n.probeHist = append(n.probeHist, success)
	if len(n.probeHist) > n.probeHistCap {
		n.probeHist = n.probeHist[len(n.probeHist)-n.probeHistCap:]
	}
}

// UptimePct returns the % of successful probes in the history buffer.
// Returns 100 if no history exists (optimistic default).
func (n *Node) UptimePct() float64 {
	n.probeHistMu.Lock()
	defer n.probeHistMu.Unlock()
	if len(n.probeHist) == 0 {
		return 100.0
	}
	ok := 0
	for _, v := range n.probeHist {
		if v {
			ok++
		}
	}
	return float64(ok) / float64(len(n.probeHist)) * 100.0
}

func (n *Node) IsHealthy() bool       { return n.healthy.Load() }
func (n *Node) ActiveConns() int64    { return n.activeConns.Load() }
func (n *Node) TotalServed() int64    { return n.totalServed.Load() }
func (n *Node) TotalFailed() int64    { return n.totalFailed.Load() }
func (n *Node) LastLatencyMs() int64  { return n.lastLatency.Load() }
func (n *Node) AvgLatencyMs() int64   { return n.avgLatency.Load() }
func (n *Node) LastCheck() time.Time {
	v, _ := n.lastCheck.Load().(time.Time)
	return v
}
func (n *Node) LastError() string {
	v, _ := n.lastError.Load().(string)
	return v
}

// updateEWMA updates exponential moving average latency. alpha=0.2 weights
// recent samples more.
func (n *Node) updateEWMA(sampleMs int64) {
	cur := n.avgLatency.Load()
	if cur == 0 {
		n.avgLatency.Store(sampleMs)
		return
	}
	next := int64(0.8*float64(cur) + 0.2*float64(sampleMs))
	n.avgLatency.Store(next)
}

// RecordRequest registers a forwarded request for stats: increments the
// per-balancer counter, refreshes the client's last-seen timestamp, and
// adds the streamed byte count. Thread-safe.
func (n *Node) RecordRequest(balancer, clientIP string, bytes int64) {
	if bytes > 0 {
		n.bytesOut.Add(bytes)
	}
	if balancer == "" && clientIP == "" {
		return
	}
	n.statsMu.Lock()
	if balancer != "" {
		if n.byBalancer == nil {
			n.byBalancer = make(map[string]int64, 8)
		}
		n.byBalancer[balancer]++
	}
	if clientIP != "" {
		if n.recentClients == nil {
			n.recentClients = make(map[string]time.Time, 16)
		}
		n.recentClients[clientIP] = time.Now()
	}
	n.statsMu.Unlock()
}

// sweepClients drops entries older than cutoff. Called periodically by the
// pool's sweeper goroutine.
func (n *Node) sweepClients(cutoff time.Time) {
	n.statsMu.Lock()
	defer n.statsMu.Unlock()
	if len(n.recentClients) == 0 {
		return
	}
	for ip, t := range n.recentClients {
		if t.Before(cutoff) {
			delete(n.recentClients, ip)
		}
	}
}

// statsSnapshot returns a copy of the per-balancer counts and the current
// unique-client count for inclusion in NodeStatus.
func (n *Node) statsSnapshot() (uniqueClients int, byBalancer map[string]int64) {
	n.statsMu.RLock()
	defer n.statsMu.RUnlock()
	uniqueClients = len(n.recentClients)
	if len(n.byBalancer) > 0 {
		byBalancer = make(map[string]int64, len(n.byBalancer))
		for k, v := range n.byBalancer {
			byBalancer[k] = v
		}
	}
	return
}

// NodeStatus is a snapshot for the admin dashboard / public API.
type NodeStatus struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Host          string    `json:"host"`
	Region        string    `json:"region,omitempty"`
	Enabled       bool      `json:"enabled"`
	Healthy       bool      `json:"healthy"`
	ActiveConns   int64     `json:"active_conns"`
	TotalServed   int64     `json:"total_served"`
	TotalFailed   int64     `json:"total_failed"`
	BytesOut      int64     `json:"bytes_out"`
	LastLatencyMs int64     `json:"last_latency_ms"`
	AvgLatencyMs  int64     `json:"avg_latency_ms"`
	Weight        int       `json:"weight"`
	LastCheck     time.Time `json:"last_check"`
	LastError     string    `json:"last_error,omitempty"`
	// Per-balancer request count for the current uptime window.
	ByBalancer map[string]int64 `json:"by_balancer,omitempty"`
	// Distinct client IPs seen in the last 5 minutes (≈ active users).
	UniqueClients5m int `json:"unique_clients_5m"`
	// % of successful probes over the recent history (typically last hour).
	UptimePct float64 `json:"uptime_pct"`
}

const (
	defaultHealthTimeout = 8 * time.Second
)

// Pool manages a set of backend nodes with health checks and selection.
type Pool struct {
	mu         sync.RWMutex
	nodes      []*Node
	apiKey     string
	client     *http.Client
	cancel     context.CancelFunc
	settings   Settings
	store      *Store

	localConns  atomic.Int64
	localWeight int

	// localServed counts requests handled by primary itself (not forwarded).
	localServed  atomic.Int64
	localFailed  atomic.Int64
	localBytes   atomic.Int64

	// Local stats — same shape as Node, for primary-handled requests.
	localStatsMu       sync.RWMutex
	localByBalancer    map[string]int64
	localRecentClients map[string]time.Time

	// Recent forwards feed — ring buffer of last N routing decisions.
	// Each entry: time, balancer, target (node name or "local"), client IP.
	eventsMu  sync.Mutex
	events    []ForwardEvent // newest-last
	eventsCap int            // default 50
}

// ForwardEvent describes a single routing decision (forward or local handling).
type ForwardEvent struct {
	Time     time.Time `json:"time"`
	Balancer string    `json:"balancer"`
	Target   string    `json:"target"`         // "local" or node name
	NodeID   string    `json:"node_id,omitempty"`
	ClientIP string    `json:"client_ip,omitempty"`
	LatencyMs int64    `json:"latency_ms,omitempty"`
	Status   int       `json:"status,omitempty"` // HTTP status, 0 if not applicable
	BytesOut  int64    `json:"bytes_out,omitempty"`
	Failed   bool      `json:"failed,omitempty"`
}

// recordEvent appends an event to the ring buffer, dropping the oldest when at capacity.
func (p *Pool) recordEvent(e ForwardEvent) {
	if p.eventsCap <= 0 {
		return
	}
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	p.events = append(p.events, e)
	if len(p.events) > p.eventsCap {
		// Drop oldest. Simple shift; ring buffer would be marginally faster
		// but 50 entries is trivial.
		p.events = p.events[len(p.events)-p.eventsCap:]
	}
}

// RecentEvents returns the last N events in reverse chronological order
// (newest first).
func (p *Pool) RecentEvents() []ForwardEvent {
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	out := make([]ForwardEvent, len(p.events))
	for i, e := range p.events {
		out[len(p.events)-1-i] = e
	}
	return out
}

// RecordForwardEvent is called by the forwarder after a node handled a request.
func (p *Pool) RecordForwardEvent(node *Node, balancer, clientIP string, status int, bytesOut, latencyMs int64, failed bool) {
	if node == nil {
		return
	}
	p.recordEvent(ForwardEvent{
		Time:     time.Now(),
		Balancer: balancer,
		Target:   node.Name,
		NodeID:   node.ID,
		ClientIP: clientIP,
		LatencyMs: latencyMs,
		Status:   status,
		BytesOut: bytesOut,
		Failed:   failed,
	})
}

// RecordLocalEvent is called when primary handled a /lite/* request itself.
func (p *Pool) RecordLocalEvent(balancer, clientIP string, status int) {
	p.recordEvent(ForwardEvent{
		Time:     time.Now(),
		Balancer: balancer,
		Target:   "local",
		ClientIP: clientIP,
		Status:   status,
	})
}

// NewPool creates a pool. If store != nil, settings + nodes are loaded from it.
// Otherwise falls back to the legacy config.ClusterConfig.Nodes list.
func NewPool(cfg config.ClusterConfig, store *Store) *Pool {
	p := &Pool{
		apiKey:      cfg.APIKey,
		client:      httpclient.New(defaultHealthTimeout),
		localWeight: 1,
		store:       store,
		eventsCap:   50,
	}
	if store != nil {
		p.settings = store.Settings()
		p.reconcileLocked(store.Snapshot())
	} else {
		p.settings = DefaultSettings()
		legacy := make([]StoredNode, 0, len(cfg.Nodes))
		for _, n := range cfg.Nodes {
			legacy = append(legacy, StoredNode{
				ID:      randID(),
				Name:    hostToName(n.Host),
				Host:    n.Host,
				Weight:  maxInt(1, n.Weight),
				Enabled: true,
			})
		}
		p.reconcileLocked(legacy)
	}
	return p
}

// Start begins periodic health probes in the background.
func (p *Pool) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	go p.healthLoop(ctx)
	go p.statsSweepLoop(ctx)
	log.Info().Int("nodes", len(p.nodes)).Str("strategy", p.settings.Strategy).Msg("cluster: pool started")
}

// statsSweepLoop ages out client-IP entries older than 5 minutes across all
// nodes + primary, so the "unique clients" metric reflects current activity.
func (p *Pool) statsSweepLoop(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-5 * time.Minute)
			for _, n := range p.Nodes() {
				n.sweepClients(cutoff)
			}
			p.sweepLocalClients(cutoff)
		}
	}
}

// Stop terminates the health-check loop.
func (p *Pool) Stop() {
	if p.cancel != nil {
		p.cancel()
	}
}

// SetSettings updates pool settings at runtime (persists if store is set).
func (p *Pool) SetSettings(st Settings) error {
	st.normalize()
	p.mu.Lock()
	p.settings = st
	p.mu.Unlock()
	if p.store != nil {
		return p.store.SetSettings(st)
	}
	return nil
}

// CurrentSettings returns a copy of the active settings.
func (p *Pool) CurrentSettings() Settings {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.settings
}

// Reconcile syncs the in-memory node list with a fresh snapshot from the
// store. Existing nodes keep their runtime stats; new nodes are added; removed
// nodes are dropped.
func (p *Pool) Reconcile(snapshot []StoredNode) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reconcileLocked(snapshot)
}

func (p *Pool) reconcileLocked(snapshot []StoredNode) {
	byID := make(map[string]*Node, len(p.nodes))
	for _, n := range p.nodes {
		byID[n.ID] = n
	}
	next := make([]*Node, 0, len(snapshot))
	for _, sn := range snapshot {
		if existing, ok := byID[sn.ID]; ok {
			existing.Name = sn.Name
			existing.Host = sn.Host
			existing.Weight = maxInt(1, sn.Weight)
			existing.Region = sn.Region
			existing.Enabled = sn.Enabled
			next = append(next, existing)
			continue
		}
		n := &Node{
			ID:      sn.ID,
			Name:    sn.Name,
			Host:    sn.Host,
			Weight:  maxInt(1, sn.Weight),
			Region:  sn.Region,
			Enabled: sn.Enabled,
		}
		n.healthy.Store(true) // optimistic
		next = append(next, n)
	}
	p.nodes = next
}

// PickFor returns the routing decision for a specific balancer name.
// It first consults Settings.Rules — if a rule matches, it overrides the
// strategy:
//   - target=local       → return (nil, true) meaning "handle locally"
//   - target=node        → pick best healthy node, fallback nil if none
//   - target=node-id     → pick that specific node if healthy, fallback to
//                          best node, then to local
//
// If no rule matches, falls through to regular Pick() behaviour.
//
// Returned bool is `localForced` — when true the caller must NOT consult
// Pick() further; the rule explicitly chose local.
func (p *Pool) PickFor(balancer string) (node *Node, localForced bool) {
	if balancer == "" {
		return p.Pick(), false
	}
	p.mu.RLock()
	rules := p.settings.Rules
	p.mu.RUnlock()

	for _, r := range rules {
		if !strings.EqualFold(r.Balancer, balancer) {
			continue
		}
		switch r.Target {
		case "local":
			return nil, true
		case "node-id":
			// Try the pinned node first.
			if r.NodeID != "" {
				if n := p.FindByID(r.NodeID); n != nil && n.Enabled && n.healthy.Load() {
					return n, false
				}
			}
			// Fallthrough to "any healthy node".
			fallthrough
		case "node":
			n := p.PickRemote()
			return n, false
		}
		// Unknown target — ignore and fall through to default pick.
		break
	}
	return p.Pick(), false
}

// Pick selects the best node according to the configured strategy.
// Returns nil if the primary itself is the lowest-scoring choice (handle
// locally) or if no remote nodes are eligible.
//
// When Settings.ForceNode is true, the primary's local baseline is skipped
// entirely — every request goes to the best available node. Falls back to
// local only if no healthy node exists.
func (p *Pool) Pick() *Node {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.settings.ForceNode {
		var best *Node
		bestScore := math.MaxFloat64
		for _, n := range p.nodes {
			if !n.Enabled || !n.healthy.Load() {
				continue
			}
			s := p.scoreLocked(n)
			if s < bestScore {
				bestScore = s
				best = n
			}
		}
		return best
	}

	// Local (primary) baseline score — uses conns only, latency assumed 0.
	bestScore := float64(p.localConns.Load()) / float64(p.localWeight)
	var best *Node // nil = local

	for _, n := range p.nodes {
		if !n.Enabled || !n.healthy.Load() {
			continue
		}
		score := p.scoreLocked(n)
		if score < bestScore {
			bestScore = score
			best = n
		}
	}
	return best
}

// PickRemote returns the best healthy remote node, never primary. Used by
// fan-out checksearch.
func (p *Pool) PickRemote() *Node {
	p.mu.RLock()
	defer p.mu.RUnlock()

	bestScore := math.MaxFloat64
	var best *Node
	for _, n := range p.nodes {
		if !n.Enabled || !n.healthy.Load() {
			continue
		}
		score := p.scoreLocked(n)
		if score < bestScore {
			bestScore = score
			best = n
		}
	}
	return best
}

// PickRetryCandidates returns up to maxAttempts healthy remote nodes, sorted
// by current score, excluding `skip`. Used by the forwarder for retries.
func (p *Pool) PickRetryCandidates(maxAttempts int, skip map[string]bool) []*Node {
	p.mu.RLock()
	defer p.mu.RUnlock()
	type scored struct {
		n   *Node
		val float64
	}
	cands := make([]scored, 0, len(p.nodes))
	for _, n := range p.nodes {
		if !n.Enabled || !n.healthy.Load() {
			continue
		}
		if skip != nil && skip[n.ID] {
			continue
		}
		cands = append(cands, scored{n, p.scoreLocked(n)})
	}
	// Sort by ascending score.
	for i := 1; i < len(cands); i++ {
		for j := i; j > 0 && cands[j].val < cands[j-1].val; j-- {
			cands[j], cands[j-1] = cands[j-1], cands[j]
		}
	}
	if maxAttempts > 0 && maxAttempts < len(cands) {
		cands = cands[:maxAttempts]
	}
	out := make([]*Node, len(cands))
	for i, c := range cands {
		out[i] = c.n
	}
	return out
}

// scoreLocked computes the strategy-aware score (lower = better).
// Caller must hold p.mu.RLock.
func (p *Pool) scoreLocked(n *Node) float64 {
	conns := float64(n.activeConns.Load()) / float64(n.Weight)
	switch p.settings.Strategy {
	case "least-conns":
		return conns
	case "latency":
		// Use avg latency, fallback to last probe.
		lat := float64(n.avgLatency.Load())
		if lat == 0 {
			lat = float64(n.lastLatency.Load())
		}
		if lat == 0 {
			lat = 1
		}
		return lat / float64(n.Weight)
	case "hybrid":
		// Normalize: connections term ~0..N, latency term ~0..few-hundred-ms.
		// Map latency 0..2000ms into 0..10 range so it competes with conns.
		lat := float64(n.avgLatency.Load())
		if lat == 0 {
			lat = float64(n.lastLatency.Load())
		}
		latNorm := lat / 200.0 // 200ms ~= 1 conn-equivalent
		w := p.settings.LatencyWeight
		return (1-w)*conns + w*latNorm
	}
	return conns
}

// IncrLocal / DecrLocal track primary-handled connections.
func (p *Pool) IncrLocal() {
	p.localConns.Add(1)
}
func (p *Pool) DecrLocal() {
	p.localConns.Add(-1)
	p.localServed.Add(1)
}

// LocalActiveConns / LocalTotalServed expose primary counters for status.
func (p *Pool) LocalActiveConns() int64 { return p.localConns.Load() }
func (p *Pool) LocalTotalServed() int64 { return p.localServed.Load() }
func (p *Pool) LocalTotalFailed() int64 { return p.localFailed.Load() }
func (p *Pool) LocalBytesOut() int64    { return p.localBytes.Load() }

// RecordLocalRequest is the primary-side counterpart of Node.RecordRequest.
// Call from any handler that serves traffic locally (lite, proxy, etc.).
func (p *Pool) RecordLocalRequest(balancer, clientIP string, bytes int64, failed bool) {
	if bytes > 0 {
		p.localBytes.Add(bytes)
	}
	if failed {
		p.localFailed.Add(1)
	}
	if balancer == "" && clientIP == "" {
		return
	}
	p.localStatsMu.Lock()
	if balancer != "" {
		if p.localByBalancer == nil {
			p.localByBalancer = make(map[string]int64, 16)
		}
		p.localByBalancer[balancer]++
	}
	if clientIP != "" {
		if p.localRecentClients == nil {
			p.localRecentClients = make(map[string]time.Time, 32)
		}
		p.localRecentClients[clientIP] = time.Now()
	}
	p.localStatsMu.Unlock()
}

// localStatsSnapshot returns the primary's unique-clients count and
// per-balancer breakdown for inclusion in cluster status responses.
func (p *Pool) localStatsSnapshot() (uniqueClients int, byBalancer map[string]int64) {
	p.localStatsMu.RLock()
	defer p.localStatsMu.RUnlock()
	uniqueClients = len(p.localRecentClients)
	if len(p.localByBalancer) > 0 {
		byBalancer = make(map[string]int64, len(p.localByBalancer))
		for k, v := range p.localByBalancer {
			byBalancer[k] = v
		}
	}
	return
}

// sweepLocalClients ages out client IPs not seen within the cutoff window.
func (p *Pool) sweepLocalClients(cutoff time.Time) {
	p.localStatsMu.Lock()
	defer p.localStatsMu.Unlock()
	for ip, t := range p.localRecentClients {
		if t.Before(cutoff) {
			delete(p.localRecentClients, ip)
		}
	}
}

// IncrConns / DecrConns track per-node active connections (used by forwarder).
func (p *Pool) IncrConns(n *Node) { n.activeConns.Add(1) }
func (p *Pool) DecrConns(n *Node) {
	n.activeConns.Add(-1)
	n.totalServed.Add(1)
}

// MarkFailedRequest is called by the forwarder when a forward attempt fails
// (5xx, dial error, etc). It bumps failure counters and feeds EWMA with the
// observed latency.
func (p *Pool) MarkFailedRequest(n *Node, latencyMs int64, err string) {
	n.totalFailed.Add(1)
	if latencyMs > 0 {
		n.updateEWMA(latencyMs)
	}
	if err != "" {
		n.lastError.Store(err)
	}
}

// MarkSuccessRequest is called after a successful proxied response. It feeds
// EWMA with the observed latency to keep avgLatency responsive to real load.
func (p *Pool) MarkSuccessRequest(n *Node, latencyMs int64) {
	if latencyMs > 0 {
		n.updateEWMA(latencyMs)
	}
}

// Nodes returns a snapshot slice of *Node (live pointers — runtime stats stay live).
func (p *Pool) Nodes() []*Node {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*Node, len(p.nodes))
	copy(out, p.nodes)
	return out
}

// FindByID returns a live node pointer or nil.
func (p *Pool) FindByID(id string) *Node {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, n := range p.nodes {
		if n.ID == id {
			return n
		}
	}
	return nil
}

// Snapshot returns a status snapshot of all nodes for dashboards / public API.
func (p *Pool) Snapshot() []NodeStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]NodeStatus, len(p.nodes))
	for i, n := range p.nodes {
		uniq, byBal := n.statsSnapshot()
		out[i] = NodeStatus{
			ID:              n.ID,
			Name:            n.Name,
			Host:            n.Host,
			Region:          n.Region,
			Enabled:         n.Enabled,
			Healthy:         n.healthy.Load(),
			ActiveConns:     n.activeConns.Load(),
			TotalServed:     n.totalServed.Load(),
			TotalFailed:     n.totalFailed.Load(),
			BytesOut:        n.bytesOut.Load(),
			LastLatencyMs:   n.lastLatency.Load(),
			AvgLatencyMs:    n.avgLatency.Load(),
			Weight:          n.Weight,
			LastCheck:       n.LastCheck(),
			LastError:       n.LastError(),
			ByBalancer:      byBal,
			UniqueClients5m: uniq,
			UptimePct:       n.UptimePct(),
		}
	}
	return out
}

// LocalStatus is the primary's counterpart to NodeStatus.
type LocalStatus struct {
	ActiveConns     int64            `json:"active_conns"`
	TotalServed     int64            `json:"total_served"`
	TotalFailed     int64            `json:"total_failed"`
	BytesOut        int64            `json:"bytes_out"`
	UniqueClients5m int              `json:"unique_clients_5m"`
	ByBalancer      map[string]int64 `json:"by_balancer,omitempty"`
	Strategy        string           `json:"strategy"`
}

// LocalSnapshot returns the primary's own request stats.
func (p *Pool) LocalSnapshot() LocalStatus {
	uniq, byBal := p.localStatsSnapshot()
	return LocalStatus{
		ActiveConns:     p.localConns.Load(),
		TotalServed:     p.localServed.Load(),
		TotalFailed:     p.localFailed.Load(),
		BytesOut:        p.localBytes.Load(),
		UniqueClients5m: uniq,
		ByBalancer:      byBal,
		Strategy:        p.Strategy(),
	}
}

// APIKey returns the shared cluster secret.
func (p *Pool) APIKey() string { return p.apiKey }

// Strategy returns the active routing strategy name.
func (p *Pool) Strategy() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.settings.Strategy
}

// ProbeNow runs an immediate health probe on a single node by ID.
// Returns the resulting NodeStatus.
func (p *Pool) ProbeNow(ctx context.Context, id string) (NodeStatus, bool) {
	n := p.FindByID(id)
	if n == nil {
		return NodeStatus{}, false
	}
	p.probe(ctx, n)
	for _, s := range p.Snapshot() {
		if s.ID == id {
			return s, true
		}
	}
	return NodeStatus{}, false
}

// ----------- health checking -----------

func (p *Pool) healthLoop(ctx context.Context) {
	// Initial probe after 3 seconds.
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			p.probeAll(ctx)
			interval := time.Duration(p.CurrentSettings().ProbeIntervalSec) * time.Second
			if interval < 5*time.Second {
				interval = 30 * time.Second
			}
			timer.Reset(interval)
		}
	}
}

func (p *Pool) probeAll(ctx context.Context) {
	var wg sync.WaitGroup
	for _, n := range p.Nodes() {
		if !n.Enabled {
			continue
		}
		wg.Add(1)
		go func(node *Node) {
			defer wg.Done()
			p.probe(ctx, node)
		}(n)
	}
	wg.Wait()
}

func (p *Pool) probe(ctx context.Context, n *Node) {
	probeCtx, cancel := context.WithTimeout(ctx, defaultHealthTimeout)
	defer cancel()

	url := n.Host + "/api/cluster/ping"
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, url, nil)
	if err != nil {
		n.lastError.Store(err.Error())
		p.markFailed(n)
		return
	}
	req.Header.Set("X-Cluster-Key", p.apiKey)

	start := time.Now()
	resp, err := p.client.Do(req)
	latency := time.Since(start).Milliseconds()
	n.lastLatency.Store(latency)
	n.lastCheck.Store(time.Now())
	n.updateEWMA(latency)

	if err != nil {
		log.Debug().Err(err).Str("node", n.Host).Msg("cluster: health probe failed")
		n.lastError.Store(err.Error())
		p.markFailed(n)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Debug().Int("status", resp.StatusCode).Str("node", n.Host).Msg("cluster: health probe non-200")
		n.lastError.Store("HTTP " + resp.Status)
		p.markFailed(n)
		return
	}
	n.lastError.Store("")
	p.markOK(n)
}

func (p *Pool) markFailed(n *Node) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n.consecOK = 0
	n.consecFails++
	if n.consecFails >= p.settings.FailThreshold {
		if n.healthy.Load() {
			log.Warn().Str("node", n.Host).Int("fails", n.consecFails).Msg("cluster: node marked unhealthy")
		}
		n.healthy.Store(false)
	}
	n.recordProbe(false)
}

func (p *Pool) markOK(n *Node) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n.consecFails = 0
	n.consecOK++
	if !n.healthy.Load() && n.consecOK >= p.settings.RecoverThreshold {
		n.healthy.Store(true)
		log.Info().Str("node", n.Host).Msg("cluster: node recovered")
	}
	n.recordProbe(true)
}
