package cluster

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
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
	ID        string
	Name      string
	Host      string
	Weight    int
	Region    string
	Enabled   bool
	EdgeURL   string // клиентский адрес ноды, см. StoredNode.EdgeURL
	EdgeLabel string // имя для зрителя, см. StoredNode.EdgeLabel

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

	// Rolling failure rate over recent forwarded requests (see failrate.go).
	// Health from ping alone cannot see a node that answers but cannot serve.
	failEWMA    atomic.Int64 // per-mille, 0..1000
	failSamples atomic.Int64

	// Version and build reported by the node's /api/cluster/ping. Empty until
	// the first successful probe, or when the node is too old to report them.
	// build (short binary hash) is what actually distinguishes two builds —
	// the version string is identical across every release.
	version atomic.Value // string
	build   atomic.Value // string

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

func (n *Node) IsHealthy() bool      { return n.healthy.Load() }
func (n *Node) ActiveConns() int64   { return n.activeConns.Load() }
func (n *Node) TotalServed() int64   { return n.totalServed.Load() }
func (n *Node) TotalFailed() int64   { return n.totalFailed.Load() }
func (n *Node) LastLatencyMs() int64 { return n.lastLatency.Load() }
func (n *Node) AvgLatencyMs() int64  { return n.avgLatency.Load() }
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
	EdgeURL       string    `json:"edge_url,omitempty"`
	EdgeLabel     string    `json:"edge_label,omitempty"`
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
	// Share of recently forwarded requests that failed, 0..1 (see failrate.go).
	// This is what routing penalises — total_failed is a lifetime figure and
	// never recovers, so it cannot be used for that.
	FailRate float64 `json:"fail_rate"`
	// Build reported by the node itself. Empty = not probed yet, or a node old
	// enough that its ping does not carry a version.
	Version string `json:"version,omitempty"`
	// Short hash of the node's binary — the only reliable way to tell whether
	// the fleet is running the same code.
	Build string `json:"build,omitempty"`
}

const (
	defaultHealthTimeout = 8 * time.Second
)

// Pool manages a set of backend nodes with health checks and selection.
type Pool struct {
	mu       sync.RWMutex
	nodes    []*Node
	apiKey   string
	client   *http.Client
	cancel   context.CancelFunc
	settings Settings
	store    *Store

	localConns  atomic.Int64
	localWeight int

	// localBuild is this instance's own short binary hash, used only to warn
	// when a node reports a different one.
	localBuild atomic.Value // string

	// localServed counts requests handled by primary itself (not forwarded).
	localServed atomic.Int64
	localFailed atomic.Int64
	localBytes  atomic.Int64

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
	Time      time.Time `json:"time"`
	Balancer  string    `json:"balancer"`
	Target    string    `json:"target"` // "local" or node name
	NodeID    string    `json:"node_id,omitempty"`
	ClientIP  string    `json:"client_ip,omitempty"`
	LatencyMs int64     `json:"latency_ms,omitempty"`
	Status    int       `json:"status,omitempty"` // HTTP status, 0 if not applicable
	BytesOut  int64     `json:"bytes_out,omitempty"`
	Failed    bool      `json:"failed,omitempty"`
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
		Time:      time.Now(),
		Balancer:  balancer,
		Target:    node.Name,
		NodeID:    node.ID,
		ClientIP:  clientIP,
		LatencyMs: latencyMs,
		Status:    status,
		BytesOut:  bytesOut,
		Failed:    failed,
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

// ApplySettings подменяет настройки в памяти, ничего не записывая на диск.
// Отличие от SetSettings: та сохраняет в стор, а на перечитывании мы, наоборот,
// приехали ИЗ файла — писать обратно нечего и незачем.
func (p *Pool) ApplySettings(st Settings) {
	st.normalize()
	p.mu.Lock()
	p.settings = st
	p.mu.Unlock()
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
			existing.EdgeURL = normalizeEdgeURL(sn.EdgeURL)
			existing.EdgeLabel = strings.TrimSpace(sn.EdgeLabel)
			next = append(next, existing)
			continue
		}
		n := &Node{
			ID:        sn.ID,
			Name:      sn.Name,
			Host:      sn.Host,
			Weight:    maxInt(1, sn.Weight),
			Region:    sn.Region,
			Enabled:   sn.Enabled,
			EdgeURL:   normalizeEdgeURL(sn.EdgeURL),
			EdgeLabel: strings.TrimSpace(sn.EdgeLabel),
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
//     best node, then to local
//
// If no rule matches, falls through to regular Pick() behaviour.
//
// Returned bool is `localForced` — when true the caller must NOT consult
// Pick() further; the rule explicitly chose local.
func (p *Pool) PickFor(balancer, hint string) (node *Node, localForced bool) {
	return p.PickForSkip(balancer, hint, nil)
}

// PickForSkip — выбор добывающей ноды с учётом нод, отключённых зрителем.
func (p *Pool) PickForSkip(balancer, hint string, skip map[string]bool) (node *Node, localForced bool) {
	return p.PickForSkipKey(balancer, hint, "", skip)
}

// PickForSkipKey — PickForSkip с ключом липкости (см. StickyKey): правило «sticky» ведёт один
// и тот же ключ на одну и ту же ноду. Пустой ключ — правило работает как «edge».
func (p *Pool) PickForSkipKey(balancer, hint, key string, skip map[string]bool) (node *Node, localForced bool) {
	// Правило записано на ИМЯ балансера, а сюда приходит весь путь после /lite/. У части
	// источников ссылку добывает второй запрос с подпутём («cdnvideohub/video.m3u8»,
	// «videoseed/video/…»), и без отсечения он правилу не соответствовал — уходил в обычную
	// балансировку. Для источника с IP-привязкой это провал: добыть могла нода без своего
	// клиентского адреса, подписать ссылку ей нечем, и отдавать её берётся main с чужого IP.
	if i := strings.IndexAny(balancer, "/?#"); i >= 0 {
		balancer = balancer[:i]
	}
	if balancer == "" {
		return p.Pick(), false
	}
	p.mu.RLock()
	rules := p.settings.Rules
	p.mu.RUnlock()

	// Правило «local» сильнее любой подсказки: источники, которые умеет
	// добывать только primary (браузер, токены), к нодам не уходят.
	// «nodes» — тоже сильнее подсказки: нода не из списка получает мёртвые
	// ссылки, и увести туда зрителя, выбравшего её скорость, значит сломать ему
	// источник.
	for _, r := range rules {
		if !strings.EqualFold(r.Balancer, balancer) {
			continue
		}
		switch r.Target {
		case "local":
			return nil, true
		case "nodes":
			return p.pickAllowedSkip(r.NodeIDs, hint, skip)
		case "sticky":
			// Тоже сильнее подсказки: у CDN с привязкой к первому IP «своя» нода зрителя —
			// это чужой адрес для уже играющего поста, то есть 429 вместо кино.
			return p.pickStickySkip(r.NodeIDs, key, skip)
		}
	}

	// Зритель померил скорость и просит конкретную ноду (edge=…): пусть она
	// и добывает ссылку — тогда поток, привязанный к добывшему, отдаст именно
	// та нода, до которой у зрителя лучший канал. Нода узнаётся по edge_url.
	// Запрет сильнее просьбы — как и в pickEdge на отдаче.
	if n := p.findByEdge(hint); n != nil && !nodeSkipped(n, skip) {
		return n, false
	}

	for _, r := range rules {
		if !strings.EqualFold(r.Balancer, balancer) {
			continue
		}
		switch r.Target {
		case "node-id":
			// Try the pinned node first.
			if r.NodeID != "" {
				// Закрепление админом — про нагрузку, а запрет зрителя — про его личный
				// маршрут: уводить человека на ноду, которую он выключил, нельзя и здесь.
				if n := p.FindByID(r.NodeID); n != nil && n.Enabled && n.healthy.Load() && !nodeSkipped(n, skip) {
					return n, false
				}
			}
			// Fallthrough to "any healthy node".
			fallthrough
		case "node":
			n := p.PickRemoteSkip(skip)
			return n, false
		case "edge":
			// Только ноды с клиентским адресом: их ссылки отдаст тот, кто добыл.
			// Нет ни одной живой — любая нода лучше, чем primary, который
			// такой источник добыть не может вовсе (hdvb на main → пусто).
			if n := p.PickEdgeNodeSkip(skip); n != nil {
				return n, false
			}
			return p.PickRemoteSkip(skip), false
		}
		// Unknown target — ignore and fall through to default pick.
		break
	}
	return p.PickSkip(skip), false
}

// StickyKey — идентичность запроса /lite для правила «sticky»: пост, если он уже известен,
// иначе название + оригинал + год (так первый и второй уровень одного тайтла обычно
// сходятся на одной ноде). Пусто — идентичности нет.
func StickyKey(q url.Values) string {
	for _, k := range []string{"postid", "post_id"} {
		if v, err := strconv.Atoi(strings.TrimSpace(q.Get(k))); err == nil && v > 0 {
			return "post:" + strconv.Itoa(v)
		}
	}
	norm := func(k string) string { return strings.ToLower(strings.Join(strings.Fields(q.Get(k)), " ")) }
	title, orig, year := norm("title"), norm("original_title"), norm("year")
	if title == "" && orig == "" {
		for _, k := range []string{"kinopoisk_id", "imdb_id", "id"} {
			if v := strings.TrimSpace(q.Get(k)); v != "" && v != "0" {
				return k + ":" + v
			}
		}
		return ""
	}
	return "title:" + title + "|" + orig + "|" + year
}

// pickStickySkip — рандеву-хеширование по ключу: один пост всегда уходит на одну и ту же
// живую ноду (с учётом веса), и только её выпадение переносит пост на соседнюю.
//
// Зачем: подпись Filmix `/s/<hash>/` выдаётся на УЧЁТКУ+пост (не на токен и не на устройство)
// и закрепляется CDN за первым IP, который её тронул; с любого другого адреса та же ссылка —
// 429 с retry-after ≈ 10 ч (проверено 22.09.2026: main 206, SPB 429, main 206, SPB 429).
// Шесть хостов на одной учётке при равномерной раздаче = популярный пост играет с одной ноды,
// а с остальных зрители получают 429 (CH 438, MSK 264, DE 232 за сутки). Липкость убирает
// столкновения без единого лишнего токена. Список NodeIDs (если задан) ограничивает кольцо.
func (p *Pool) pickStickySkip(ids []string, key string, skip map[string]bool) (*Node, bool) {
	if key == "" {
		if n := p.PickEdgeNodeSkip(skip); n != nil {
			return n, false
		}
		return p.PickRemoteSkip(skip), false
	}
	allowed := map[string]bool{}
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			allowed[id] = true
		}
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	var best *Node
	bestScore := math.MaxFloat64
	for _, n := range p.nodes {
		if len(allowed) > 0 && !allowed[n.ID] {
			continue
		}
		if !n.Enabled || !n.healthy.Load() || nodeSkipped(n, skip) {
			continue
		}
		w := n.Weight
		if w <= 0 {
			w = 1
		}
		h := fnv.New64a()
		_, _ = h.Write([]byte(key))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(n.ID))
		u := (float64(h.Sum64()>>11) + 1) / float64(uint64(1)<<53) // (0,1]
		// Взвешенное рандеву: min(-ln u / w) выбирает ноду i с вероятностью w_i/Σw и не
		// перетасовывает остальных при выпадении одной.
		if score := -math.Log(u) / float64(w); score < bestScore {
			bestScore, best = score, n
		}
	}
	if best == nil {
		return nil, true // ни одной живой — primary добудет сам
	}
	return best, false
}

// pickAllowedSkip — нода из разрешённого списка. Подсказка зрителя берётся,
// только если называет ноду из списка; иначе — лучшая по оценке среди
// разрешённых. Ни одной живой — primary (localForced): лучше честно добыть на
// main, чем отдать заведомо мёртвую ссылку с неподходящей ноды.
func (p *Pool) pickAllowedSkip(ids []string, hint string, skip map[string]bool) (*Node, bool) {
	allowed := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			allowed[id] = true
		}
	}
	if len(allowed) == 0 {
		return nil, true
	}
	if n := p.findByEdge(hint); n != nil && allowed[n.ID] && !nodeSkipped(n, skip) {
		return n, false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	var best *Node
	bestScore := math.MaxFloat64
	for _, n := range p.nodes {
		if !allowed[n.ID] || !n.Enabled || !n.healthy.Load() || nodeSkipped(n, skip) {
			continue
		}
		if s := p.scoreLocked(n); s < bestScore {
			bestScore, best = s, n
		}
	}
	if best == nil {
		return nil, true
	}
	return best, false
}

// FailoverCandidates — ноды, которым main может отдать запрос на добор, когда
// сам контент не получил: живые, включённые, с клиентским адресом и не
// отключённые зрителем. Клиентский адрес обязателен: у источников с привязкой
// ссылки к добывшему видео должна отдавать сама нода, а без edge_url её ссылку
// отдавал бы main со своего IP — и получал бы 404. По возрастанию нагрузки.
func (p *Pool) FailoverCandidates(skip map[string]bool) []*Node {
	p.mu.RLock()
	defer p.mu.RUnlock()
	type scored struct {
		n *Node
		s float64
	}
	var list []scored
	for _, n := range p.nodes {
		if n.EdgeURL == "" || !n.Enabled || !n.healthy.Load() || nodeSkipped(n, skip) {
			continue
		}
		list = append(list, scored{n, p.scoreLocked(n)})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].s < list[j].s })
	out := make([]*Node, 0, len(list))
	for _, x := range list {
		out = append(out, x.n)
	}
	return out
}

// RuleTarget — цель правила маршрутизации для балансера ("" — правила нет).
func (p *Pool) RuleTarget(balancer string) string {
	if i := strings.IndexAny(balancer, "/?#|"); i >= 0 {
		balancer = balancer[:i]
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, r := range p.settings.Rules {
		if strings.EqualFold(r.Balancer, balancer) {
			return r.Target
		}
	}
	return ""
}

// normalizeEdgeURL приводит адрес к виду «https://host[:port]» без хвоста.
func normalizeEdgeURL(u string) string {
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	if u == "" {
		return ""
	}
	if !strings.Contains(u, "://") {
		u = "https://" + u
	}
	return strings.ToLower(u)
}

// findByEdge — живая включённая нода с таким клиентским адресом, либо nil.
func (p *Pool) findByEdge(hint string) *Node {
	want := normalizeEdgeURL(hint)
	if want == "" {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, n := range p.nodes {
		if n.EdgeURL != "" && n.EdgeURL == want && n.Enabled && n.healthy.Load() {
			return n
		}
	}
	return nil
}

// PickEdgeNode — лучшая по счёту живая нода, у которой есть клиентский адрес.
// nodeSkipped — отключил ли зритель эту ноду у себя (набор нормализованных edge_url).
//
// Запрет обязан действовать не только на ОТДАЧУ, но и на ДОБЫЧУ: у источников с правилом «edge»
// ссылку отдаёт тот, кто её добыл, и без этой проверки зритель, отключивший ноду, всё равно
// уезжал бы на неё — добыли там, значит оттуда и отдадут.
//
// Ноду без edge_url отключить нельзя: зритель её не видит и назвать не может.
func nodeSkipped(n *Node, skip map[string]bool) bool {
	if len(skip) == 0 || n == nil || n.EdgeURL == "" {
		return false
	}
	return skip[normalizeEdgeURL(n.EdgeURL)]
}

func (p *Pool) PickEdgeNode() *Node { return p.PickEdgeNodeSkip(nil) }

// PickEdgeNodeSkip — то же, но мимо нод, отключённых зрителем.
func (p *Pool) PickEdgeNodeSkip(skip map[string]bool) *Node {
	p.mu.RLock()
	defer p.mu.RUnlock()
	bestScore := math.MaxFloat64
	var best *Node
	for _, n := range p.nodes {
		if n.EdgeURL == "" || !n.Enabled || !n.healthy.Load() || nodeSkipped(n, skip) {
			continue
		}
		if score := p.scoreLocked(n); score < bestScore {
			bestScore, best = score, n
		}
	}
	return best
}

// Pick selects the best node according to the configured strategy.
// Returns nil if the primary itself is the lowest-scoring choice (handle
// locally) or if no remote nodes are eligible.
//
// When Settings.ForceNode is true, the primary's local baseline is skipped
// entirely — every request goes to the best available node. Falls back to
// local only if no healthy node exists.
func (p *Pool) Pick() *Node { return p.PickSkip(nil) }

// PickSkip — то же, но мимо нод, отключённых зрителем.
func (p *Pool) PickSkip(skip map[string]bool) *Node {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.settings.ForceNode {
		var best *Node
		bestScore := math.MaxFloat64
		for _, n := range p.nodes {
			if !n.Enabled || !n.healthy.Load() || nodeSkipped(n, skip) {
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
	// The divisor is the primary's capacity relative to a node of weight 1; see
	// Settings.LocalWeight for why leaving it at 1 starves an 8-core primary.
	bestScore := float64(p.localConns.Load()) / float64(p.effectiveLocalWeight())
	var best *Node // nil = local

	for _, n := range p.nodes {
		if !n.Enabled || !n.healthy.Load() || nodeSkipped(n, skip) {
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
func (p *Pool) PickRemote() *Node { return p.PickRemoteSkip(nil) }

// PickRemoteSkip — то же, но мимо нод, отключённых зрителем.
func (p *Pool) PickRemoteSkip(skip map[string]bool) *Node {
	p.mu.RLock()
	defer p.mu.RUnlock()

	bestScore := math.MaxFloat64
	var best *Node
	for _, n := range p.nodes {
		if !n.Enabled || !n.healthy.Load() || nodeSkipped(n, skip) {
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
// scoreLocked ranks a node — lower is better.
//
// Every strategy is scaled by the node's recent failure rate, so the number
// means "cost per successful request" rather than cost per attempt. Without
// that scaling the pool happily kept feeding a node that was dropping 43% of
// what it was given: the failures were counted but never consulted, and a
// retry on another node costs the client the full latency twice.
func (p *Pool) scoreLocked(n *Node) float64 {
	return p.rawScoreLocked(n) * n.failPenalty()
}

// effectiveLocalWeight is the primary's capacity divisor. Settings wins when it
// carries a usable value; p.localWeight is the pre-settings fallback. Callers
// hold at least a read lock.
func (p *Pool) effectiveLocalWeight() int {
	if w := p.settings.LocalWeight; w > 0 {
		return w
	}
	if p.localWeight > 0 {
		return p.localWeight
	}
	return 1
}

func (p *Pool) rawScoreLocked(n *Node) float64 {
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
	n.observeOutcome(true)
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
	n.observeOutcome(false)
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
			EdgeURL:         n.EdgeURL,
			EdgeLabel:       n.EdgeLabel,
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
			FailRate:        n.FailRate(),
			Version:         n.NodeVersion(),
			Build:           n.NodeBuild(),
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

// SetLocalBuild records this instance's build so probes can flag a node that
// is running something else.
func (p *Pool) SetLocalBuild(b string) {
	if strings.TrimSpace(b) != "" {
		p.localBuild.Store(b)
	}
}

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

	// Read the version the node reports. Nodes older than this field simply
	// omit it, so an empty string means "unknown", never "mismatch".
	var ping struct {
		Version string `json:"version"`
		Build   string `json:"build"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if err := json.Unmarshal(body, &ping); err == nil {
		prev, _ := n.version.Load().(string)
		if v := strings.TrimSpace(ping.Version); v != "" && v != prev {
			n.version.Store(v)
		}
		prevBuild, _ := n.build.Load().(string)
		if b := strings.TrimSpace(ping.Build); b != "" && b != prevBuild {
			n.build.Store(b)
			if prevBuild != "" {
				log.Info().Str("node", n.Host).Str("from", prevBuild).Str("to", b).
					Msg("cluster: node build changed")
			}
			if mine, _ := p.localBuild.Load().(string); mine != "" && mine != b {
				log.Warn().Str("node", n.Host).Str("node_build", b).Str("primary_build", mine).
					Msg("cluster: node runs a different build than the primary")
			}
		}
	}

	n.lastError.Store("")
	p.markOK(n)
}

// NodeVersion returns the version last reported by the node, or "" when the
// node has not been probed yet or is too old to report one.
func (n *Node) NodeVersion() string {
	v, _ := n.version.Load().(string)
	return v
}

// NodeBuild returns the short binary hash the node reported, or "".
func (n *Node) NodeBuild() string {
	v, _ := n.build.Load().(string)
	return v
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
