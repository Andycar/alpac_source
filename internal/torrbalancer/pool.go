package torrbalancer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"lampac-go/internal/httpclient"

	"github.com/rs/zerolog/log"
)

const defaultHealthTimeout = 8 * time.Second

// listProbeTimeout — ОТДЕЛЬНЫЙ бюджет для POST /torrents list. Раньше он делил
// контекст с /echo, а это несопоставимые по цене вызовы: echo отвечает за
// миллисекунды, а list на нагруженном бэкенде с полутора сотнями раздач честно
// идёт единицы секунд (замеры: 0.5–6.5 с на ihor1, до 10 с на ts1 под пиком).
// С общим восьмисекундным бюджетом здоровый сервер уезжал в карантин просто
// за то, что занят.
const listProbeTimeout = 20 * time.Second

// wedgeMissesToQuarantine — сколько подряд неудачных проб torrents-API нужно,
// чтобы увести бэкенд из выдачи. С единицей один таймаут на пике стоил серверу
// двух минут простоя, а зрителю — холодного старта на другом сервере посреди
// просмотра: 18.09.2026 так набралось 38 карантинов за час на ЖИВЫХ машинах.
// Устойчивый зомби Selectel-типа копит промахи подряд и доезжает до карантина
// за один лишний цикл пробы (~30 с).
const wedgeMissesToQuarantine = 2

// Stream circuit-breaker tuning (see Backend.strikes).
const (
	// strikeWindow bounds how long a distinct-torrent strike stays counted.
	strikeWindow = 10 * time.Minute
	// quarantineDur is how long a tripped backend sits out of selection before
	// the breaker half-opens and lets traffic test it again.
	quarantineDur = 2 * time.Minute
	// maxWedgeQuarantine caps the escalating wedge-quarantine (2→4→8→16→30 мин):
	// хронический полу-зомби (Selectel-типа: /echo жив, /torrents висит неделями)
	// не должен каждые 2 минуты полу-открываться и ловить живые add'ы юзеров.
	maxWedgeQuarantine = 30 * time.Minute
	// chronicWedgeStreak — столько wedge-квارантинов ПОДРЯД (пробы каждые ~30с →
	// ~20 минут сплошного клина) переводят бэкенд в «хронические»: авто-лечение
	// (/shutdown, SSH) прекращается — два цикла эскалации уже прошли и не помогли,
	// дальнейшие рестарты только рвут стримы и шумят в TG. Раз в час — OnChronic.
	chronicWedgeStreak = 40
	// chronicNotifyEvery bounds OnChronic alerts for a permanently sick backend.
	chronicNotifyEvery = time.Hour
)

// backendTarget is an immutable (host, authHdr) pair published atomically so
// request handlers can read a backend's destination on the hot path without
// taking the pool lock (and without racing a concurrent Reconcile).
type backendTarget struct {
	host    string
	authHdr string
	// Прямая отдача (DirectLink): публичный фронт бэкенда и секрет secure_link.
	// Едут в том же атомарном снимке, что и host/authHdr, — горячий путь читает
	// их без блокировки через DirectTarget().
	directURL    string
	directSecret string
}

// Backend is the runtime state of one TorrServer backend.
type Backend struct {
	ID       string
	Name     string
	Host     string
	Login    string
	Password string
	Weight   int
	Enabled  bool

	// SSH-управление машиной бэкенда (см. StoredBackend). Обновляется в
	// reconcileLocked; читается только под pool.mu либо со снапшота Backends().
	SSHHost     string
	SSHPort     int
	SSHUser     string
	SSHPassword string
	SSHCmd      string

	// Прямая отдача зрителю (см. StoredBackend.DirectURL, DirectLink). Пусто =
	// поток идёт через main. Горячий путь читает через DirectTarget().
	DirectURL    string
	DirectSecret string

	authHdr string // precomputed Basic-Auth header; rebuilt in reconcileLocked
	// target mirrors (host, authHdr), republished on every Reconcile. Read
	// lock-free via Target() from the proxy hot path.
	target atomic.Pointer[backendTarget]

	healthy      atomic.Bool
	activeConns  atomic.Int64
	totalServed  atomic.Int64
	totalFailed  atomic.Int64
	torrentCount atomic.Int64 // last observed torrent count (best-effort)

	// Measured streaming throughput (see throughput.go). rateBits holds a
	// float64 in its bit pattern so it can live in an atomic; tier is the
	// coarse, hysteresis-guarded multiplier the pickers actually use.
	rateBits    atomic.Uint64
	rateSamples atomic.Int64
	tier        atomic.Int64
	lastLatency atomic.Int64 // ms — last probe latency
	lastCheck   atomic.Value // time.Time
	lastError   atomic.Value // string

	// Passive stream circuit-breaker. The /echo health probe can't see a
	// HALF-WEDGED TorrServer (prod 2026-07-16: /echo answered in ms while every
	// /stream hung >30s — the pool kept routing to a zombie for hours). Real
	// stream outcomes are the only signal that catches this: zero-data stream
	// failures accumulate as strikes (deduped by infohash so ONE dead torrent
	// retried in a loop can't condemn a healthy backend); FailThreshold DISTINCT
	// torrents failing inside strikeWindow trips a quarantine. Quarantine is
	// time-boxed (circuit-breaker "open" state): after quarantineDur the backend
	// is eligible again ("half-open") and re-trips within seconds if still
	// wedged. HRW re-homes its torrents to healthy backends automatically.
	quarantineUntil atomic.Int64 // unix nanos; 0 = not quarantined
	strikeMu        sync.Mutex
	strikes         map[string]time.Time // infohash → last zero-data failure

	// lastHeal — unix nanos последней попытки автолечения (шатдаун/SSH).
	// CAS-кулдаун, чтобы проба и прокси не устраивали рестарт-шторм.
	lastHeal atomic.Int64

	// wedgeMisses — подряд идущие НЕУДАЧНЫЕ пробы torrents-API, ещё не доросшие
	// до карантина. Обнуляется любой успешной пробой (см. wedgeMissesToQuarantine).
	wedgeMisses atomic.Int64
	// wedgeStreak — подряд идущие wedge-карантины (torrents-API мёртв) без
	// единой успешной пробы между ними. Двигает эскалацию длительности карантина
	// и порог «хроник» chronicWedgeStreak; сбрасывается в markOK.
	wedgeStreak atomic.Int64
	// lastChronicNotify — unix nanos последнего OnChronic-алерта (CAS-троттлинг).
	lastChronicNotify atomic.Int64

	probeHistMu  sync.Mutex
	probeHist    []bool
	probeHistCap int

	consecFails int // guarded by pool.mu
	consecOK    int // guarded by pool.mu
}

// Quarantined reports whether the stream circuit-breaker currently excludes
// this backend from selection.
func (b *Backend) Quarantined() bool {
	return time.Now().UnixNano() < b.quarantineUntil.Load()
}

// Available is the selection gate: probe-healthy AND not stream-quarantined.
func (b *Backend) Available() bool {
	return b.healthy.Load() && !b.Quarantined()
}

func (b *Backend) recordProbe(success bool) {
	b.probeHistMu.Lock()
	defer b.probeHistMu.Unlock()
	if b.probeHistCap == 0 {
		b.probeHistCap = 120 // ~1h at 30s interval
	}
	b.probeHist = append(b.probeHist, success)
	if len(b.probeHist) > b.probeHistCap {
		b.probeHist = b.probeHist[len(b.probeHist)-b.probeHistCap:]
	}
}

// UptimePct returns the % of successful probes in the history buffer.
func (b *Backend) UptimePct() float64 {
	b.probeHistMu.Lock()
	defer b.probeHistMu.Unlock()
	if len(b.probeHist) == 0 {
		return 100.0
	}
	ok := 0
	for _, v := range b.probeHist {
		if v {
			ok++
		}
	}
	return float64(ok) / float64(len(b.probeHist)) * 100.0
}

func (b *Backend) lastCheckTime() time.Time {
	v, _ := b.lastCheck.Load().(time.Time)
	return v
}
func (b *Backend) lastErrorStr() string {
	v, _ := b.lastError.Load().(string)
	return v
}

// Target returns the backend's current (host, authHdr) — lock-free and
// race-safe with Reconcile. Use this on the proxy hot path.
func (b *Backend) Target() (host, authHdr string) {
	if t := b.target.Load(); t != nil {
		return t.host, t.authHdr
	}
	return b.Host, b.authHdr
}

// DirectTarget returns the backend's public TLS front and the secure_link
// secret for direct-to-viewer streaming (both empty when the backend only
// serves through main). Lock-free and race-safe with Reconcile, like Target().
func (b *Backend) DirectTarget() (base, secret string) {
	if t := b.target.Load(); t != nil {
		return t.directURL, t.directSecret
	}
	return b.DirectURL, b.DirectSecret
}

// BackendStatus is a snapshot for the admin dashboard. Never exposes the password.
type BackendStatus struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Host        string `json:"host"`
	Login       string `json:"login,omitempty"`
	HasAuth     bool   `json:"has_auth"`
	Enabled     bool   `json:"enabled"`
	Healthy     bool   `json:"healthy"`
	Quarantined bool   `json:"quarantined"`
	// Measured streaming rate and the routing multiplier derived from it.
	// RateBytesPerSec is 0 until the backend has served a real stream.
	RateBytesPerSec float64   `json:"rate_bytes_per_sec"`
	SpeedTier       int       `json:"speed_tier"`
	ActiveConns     int64     `json:"active_conns"`
	TotalServed     int64     `json:"total_served"`
	TotalFailed     int64     `json:"total_failed"`
	TorrentCount    int64     `json:"torrent_count"`
	LastLatencyMs   int64     `json:"last_latency_ms"`
	Weight          int       `json:"weight"`
	Notes           string    `json:"notes,omitempty"`
	LastCheck       time.Time `json:"last_check"`
	LastError       string    `json:"last_error,omitempty"`
	UptimePct       float64   `json:"uptime_pct"`
	WedgeStreak     int64     `json:"wedge_streak,omitempty"`

	// SSH-управление: пароль наружу не отдаётся, только настройки + флаг.
	HasSSH  bool   `json:"has_ssh"`
	SSHHost string `json:"ssh_host,omitempty"`
	SSHPort int    `json:"ssh_port,omitempty"`
	SSHUser string `json:"ssh_user,omitempty"`
	SSHCmd  string `json:"ssh_cmd,omitempty"`
}

// Pool manages a set of TorrServer backends with health checks and
// sticky-by-infohash selection.
type Pool struct {
	mu       sync.RWMutex
	backends []*Backend
	client   *http.Client
	cancel   context.CancelFunc
	settings Settings
	store    *Store

	// OnZombie fires (async, at most once per quarantine trip) when the stream
	// circuit-breaker trips. echoAlive distinguishes a half-wedged zombie
	// (/echo fine, streams dead — restart helps) from a hard-down backend.
	// Wired once at startup, before traffic.
	OnZombie func(b *Backend, echoAlive bool)

	// OnChronic fires (async, at most once per chronicNotifyEvery) when a
	// backend's wedgeStreak crosses chronicWedgeStreak — рестарты не помогли,
	// автолечение остановлено, машине нужен человек. Wired once at startup.
	OnChronic func(b *Backend, streak int64)
}

// NewPool creates a pool backed by the given store.
func NewPool(store *Store) *Pool {
	p := &Pool{
		// Пробы ходят на наши TLS-фронты tsN.torr.example.com с настоящими сертификатами —
		// проверяем их, иначе Basic-auth TorrServer уезжал бы кому угодно на пути.
		// Локальные http-бэкенды нод (127.0.0.1:9080) это не задевает.
		client: httpclient.NewVerifying(defaultHealthTimeout),
		store:  store,
	}
	if store != nil {
		p.settings = store.Settings()
		p.reconcileLocked(store.Snapshot())
	} else {
		p.settings = DefaultSettings()
	}
	return p
}

// Start begins periodic health probes in the background.
func (p *Pool) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	go p.healthLoop(ctx)
	log.Info().Int("backends", len(p.Backends())).Msg("torrbalancer: pool started")
}

// Stop terminates the health-check loop.
func (p *Pool) Stop() {
	if p.cancel != nil {
		p.cancel()
	}
}

// Reconcile syncs the in-memory backend list with a fresh store snapshot.
// Existing backends keep their runtime stats (matched by ID); new backends are
// added; removed backends are dropped. Credentials/auth header are refreshed.
func (p *Pool) Reconcile(snapshot []StoredBackend) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reconcileLocked(snapshot)
}

func (p *Pool) reconcileLocked(snapshot []StoredBackend) {
	byID := make(map[string]*Backend, len(p.backends))
	for _, b := range p.backends {
		byID[b.ID] = b
	}
	next := make([]*Backend, 0, len(snapshot))
	for _, sb := range snapshot {
		w := sb.Weight
		if w < 1 {
			w = 1
		}
		auth := buildAuthHdr(sb.Login, sb.Password)
		tgt := &backendTarget{host: sb.Host, authHdr: auth, directURL: sb.DirectURL, directSecret: sb.DirectSecret}
		if existing, ok := byID[sb.ID]; ok {
			existing.Name = sb.Name
			existing.Host = sb.Host
			existing.Login = sb.Login
			existing.Password = sb.Password
			existing.Weight = w
			existing.Enabled = sb.Enabled
			existing.SSHHost = sb.SSHHost
			existing.SSHPort = sb.SSHPort
			existing.SSHUser = sb.SSHUser
			existing.SSHPassword = sb.SSHPassword
			existing.SSHCmd = sb.SSHCmd
			existing.DirectURL = sb.DirectURL
			existing.DirectSecret = sb.DirectSecret
			existing.authHdr = auth
			existing.target.Store(tgt)
			next = append(next, existing)
			continue
		}
		b := &Backend{
			ID:           sb.ID,
			Name:         sb.Name,
			Host:         sb.Host,
			Login:        sb.Login,
			Password:     sb.Password,
			Weight:       w,
			Enabled:      sb.Enabled,
			SSHHost:      sb.SSHHost,
			SSHPort:      sb.SSHPort,
			SSHUser:      sb.SSHUser,
			SSHPassword:  sb.SSHPassword,
			SSHCmd:       sb.SSHCmd,
			DirectURL:    sb.DirectURL,
			DirectSecret: sb.DirectSecret,
			authHdr:      auth,
		}
		b.healthy.Store(true) // optimistic until the first probe
		b.target.Store(tgt)
		next = append(next, b)
	}
	p.backends = next
}

// NewDetachedBackend builds a standalone Backend that belongs to no pool —
// the shape a remote transcode box receives from the main server's
// /transcoding/ts-pick oracle. It carries host + credentials (Target() and
// the Login/Password fields both work) but no health state, so callers must
// never feed it to Pool methods (MarkFailed, QuarantineWedged, …).
func NewDetachedBackend(id, name, host, login, password string) *Backend {
	host = strings.TrimRight(strings.TrimSpace(host), "/")
	if strings.TrimSpace(id) == "" {
		id = host
	}
	if strings.TrimSpace(name) == "" {
		name = host
	}
	b := &Backend{
		ID:       id,
		Name:     name,
		Host:     host,
		Login:    login,
		Password: password,
		Weight:   1,
		Enabled:  true,
		authHdr:  buildAuthHdr(login, password),
	}
	b.healthy.Store(true)
	b.target.Store(&backendTarget{host: host, authHdr: b.authHdr})
	return b
}

// PickForHash returns the backend that should serve the torrent identified by
// infohash, restricted to backends for which allow(id) is true. Selection is
// weighted rendezvous hashing (HRW): deterministic, weight-aware, and stable
// under backend churn (removing one backend only re-homes the torrents that
// were pinned to it). Returns nil when no enabled+healthy+allowed backend
// exists. An empty infohash falls through to PickPrimary.
func (p *Pool) PickForHash(infohash string, allow func(backendID string) bool) *Backend {
	if infohash == "" {
		return p.PickPrimary(allow)
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	// Fail-open: health-гейт имеет смысл, пока есть выбор. 14.08.2026 вечерний
	// затык уронил пробы ВСЕХ бэкендов разом — пул 19 минут отдавал юзерам 503,
	// хотя «нездоровые» бэкенды продолжали тянуть стримы. Если здоровых нет —
	// раздаём по HRW среди просто enabled: медленный бэкенд лучше отказа.
	for _, gate := range []func(*Backend) bool{(*Backend).Available, func(*Backend) bool { return true }} {
		var best *Backend
		var bestScore float64
		for _, b := range p.backends {
			if !b.Enabled || !gate(b) {
				continue
			}
			if allow != nil && !allow(b.ID) {
				continue
			}
			// effectiveWeight, not Weight: a backend that measurably streams
			// faster should win proportionally more hashes. HRW is chosen
			// precisely because changing a weight moves only the share that
			// must move, keeping the rest of the torrents on their warm backend.
			score := hrwScore(infohash, b.ID, b.effectiveWeight())
			if best == nil || score > bestScore {
				best = b
				bestScore = score
			}
		}
		if best != nil {
			return best
		}
	}
	return nil
}

// PickPrimary returns the least-loaded enabled+healthy+allowed backend (ties
// broken by name for determinism). Used for hashless requests (web UI, echo,
// settings). Returns nil when none qualifies.
func (p *Pool) PickPrimary(allow func(backendID string) bool) *Backend {
	p.mu.RLock()
	defer p.mu.RUnlock()
	// Fail-open как в PickForHash: пустой «здоровый» пул не повод для 503.
	for _, gate := range []func(*Backend) bool{(*Backend).Available, func(*Backend) bool { return true }} {
		var best *Backend
		for _, b := range p.backends {
			if !b.Enabled || !gate(b) {
				continue
			}
			if allow != nil && !allow(b.ID) {
				continue
			}
			if best == nil {
				best = b
				continue
			}
			// Load per unit of measured capability, not raw connections: two
			// connections on a 10 MB/s backend are lighter than one on a
			// throttled link. Ties still break on name so the choice stays
			// deterministic.
			bc := float64(best.activeConns.Load()) / float64(best.effectiveWeight())
			cc := float64(b.activeConns.Load()) / float64(b.effectiveWeight())
			if cc < bc || (cc == bc && strings.ToLower(b.Name) < strings.ToLower(best.Name)) {
				best = b
			}
		}
		if best != nil {
			return best
		}
	}
	return nil
}

// AllEnabledHealthy returns every enabled+healthy+allowed backend — used for
// fan-out endpoints (playlistall, torrents list) whose data spans backends.
// Fail-open: если здоровых нет вовсе, отдаёт всех enabled+allowed — fanout
// переживает частичные таймауты, а пустой ответ юзеру не переживает ничего.
func (p *Pool) AllEnabledHealthy(allow func(backendID string) bool) []*Backend {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, gate := range []func(*Backend) bool{(*Backend).Available, func(*Backend) bool { return true }} {
		out := make([]*Backend, 0, len(p.backends))
		for _, b := range p.backends {
			if !b.Enabled || !gate(b) {
				continue
			}
			if allow != nil && !allow(b.ID) {
				continue
			}
			out = append(out, b)
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

// hrwScore computes the weighted rendezvous score for (key, backend). Higher
// wins. weight scales the expected share of keys a backend receives.
func hrwScore(key, id string, weight int) float64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(id))
	// Use the top 53 bits so the value is exactly representable as a float64,
	// then map into the open interval (0,1) so Log is always finite & negative.
	v := h.Sum64() >> 11 // [0, 2^53)
	f := (float64(v) + 0.5) / float64(uint64(1)<<53)
	if weight < 1 {
		weight = 1
	}
	return -float64(weight) / math.Log(f)
}

func buildAuthHdr(login, password string) string {
	if strings.TrimSpace(password) == "" {
		return ""
	}
	if strings.TrimSpace(login) == "" {
		login = "ts" // default TorrServer username
	}
	cred := base64.StdEncoding.EncodeToString([]byte(login + ":" + password))
	return "Basic " + cred
}

// IncrConns / DecrConns track per-backend active connections.
func (p *Pool) IncrConns(b *Backend) {
	if b != nil {
		b.activeConns.Add(1)
	}
}
func (p *Pool) DecrConns(b *Backend) {
	if b != nil {
		b.activeConns.Add(-1)
		b.totalServed.Add(1)
	}
}

// MarkFailed bumps a backend's failure counter (e.g. proxy got a transport error).
func (p *Pool) MarkFailed(b *Backend) {
	if b != nil {
		b.totalFailed.Add(1)
	}
}

// RecordStreamStrike registers a zero-data /stream outcome against b: the
// backend accepted the request but produced no response (header timeout,
// connection reset mid-wait). Strikes are deduped by infohash — one dead
// torrent retried in a loop is ONE strike; only FailThreshold DISTINCT
// torrents failing within strikeWindow trip the quarantine. On the trip,
// OnZombie fires once (async) with the result of an immediate /echo check so
// the callback can tell "half-wedged zombie" from "hard down".
func (p *Pool) RecordStreamStrike(b *Backend, infohash string) {
	if b == nil {
		return
	}
	b.totalFailed.Add(1)
	threshold := p.CurrentSettings().FailThreshold

	if infohash == "" {
		infohash = "_hashless_"
	}
	now := time.Now()
	b.strikeMu.Lock()
	if b.strikes == nil {
		b.strikes = make(map[string]time.Time)
	}
	for k, t := range b.strikes {
		if now.Sub(t) > strikeWindow {
			delete(b.strikes, k)
		}
	}
	b.strikes[infohash] = now
	distinct := len(b.strikes)
	tripped := distinct >= threshold && !b.Quarantined()
	if tripped {
		// Fresh evidence required for the next trip after the breaker half-opens.
		b.strikes = make(map[string]time.Time)
	}
	b.strikeMu.Unlock()

	if !tripped {
		if distinct < threshold {
			log.Debug().Str("backend", b.Host).Str("infohash", infohash).Int("distinct", distinct).Int("threshold", threshold).
				Msg("torrbalancer: stream strike recorded")
		}
		return
	}

	b.quarantineUntil.Store(now.Add(quarantineDur).UnixNano())
	b.lastError.Store("quarantined: streams return no data (circuit-breaker)")
	log.Warn().Str("backend", b.Host).Int("distinctTorrents", distinct).Dur("quarantine", quarantineDur).
		Msg("torrbalancer: stream circuit-breaker TRIPPED — backend quarantined, torrents re-home via HRW")

	if cb := p.OnZombie; cb != nil {
		go cb(b, p.echoAlive(b))
	}
}

// RecordStreamOK clears accumulated strikes — the backend proved it can serve
// stream data (response headers arrived).
func (p *Pool) RecordStreamOK(b *Backend) {
	if b == nil {
		return
	}
	b.strikeMu.Lock()
	if len(b.strikes) > 0 {
		b.strikes = make(map[string]time.Time)
	}
	b.strikeMu.Unlock()
}

// echoAlive performs one immediate /echo check (independent of the probe
// loop) — used at quarantine-trip time to classify the failure.
func (p *Pool) echoAlive(b *Backend) bool {
	host, authHdr := b.Target()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, host+"/echo", nil)
	if err != nil {
		return false
	}
	if authHdr != "" {
		req.Header.Set("Authorization", authHdr)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	buf := make([]byte, 128)
	n, _ := resp.Body.Read(buf)
	return resp.StatusCode == http.StatusOK && strings.Contains(string(buf[:n]), "MatriX")
}

// ShutdownBackend asks a TorrServer to exit via its /shutdown endpoint. Under
// systemd Restart=always (the recommended deployment) this is a remote
// restart — the auto-heal for the half-wedged zombie state.
func (p *Pool) ShutdownBackend(b *Backend) error {
	if b == nil {
		return errors.New("nil backend")
	}
	host, authHdr := b.Target()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, host+"/shutdown", nil)
	if err != nil {
		return err
	}
	if authHdr != "" {
		req.Header.Set("Authorization", authHdr)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		// The server may die mid-response — connection errors after the request
		// went out are the EXPECTED outcome of a successful shutdown.
		log.Info().Str("backend", b.Host).Err(err).Msg("torrbalancer: /shutdown sent (connection dropped — likely shutting down)")
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("/shutdown HTTP %s", resp.Status)
	}
	log.Info().Str("backend", b.Host).Msg("torrbalancer: /shutdown acknowledged")
	return nil
}

// HasEnabledBackends reports whether any backend is enabled (pool is "active").
func (p *Pool) HasEnabledBackends() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, b := range p.backends {
		if b.Enabled {
			return true
		}
	}
	return false
}

// Backends returns a snapshot slice of live *Backend pointers.
func (p *Pool) Backends() []*Backend {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*Backend, len(p.backends))
	copy(out, p.backends)
	return out
}

// FindByID returns a live backend pointer or nil.
func (p *Pool) FindByID(id string) *Backend {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, b := range p.backends {
		if b.ID == id {
			return b
		}
	}
	return nil
}

// Snapshot returns a status snapshot of all backends for the admin dashboard.
func (p *Pool) Snapshot() []BackendStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]BackendStatus, len(p.backends))
	for i, b := range p.backends {
		out[i] = BackendStatus{
			ID:              b.ID,
			Name:            b.Name,
			Host:            b.Host,
			Login:           b.Login,
			HasAuth:         b.authHdr != "",
			Enabled:         b.Enabled,
			Healthy:         b.Available(), // admin badge reflects real availability (probe + quarantine)
			Quarantined:     b.Quarantined(),
			ActiveConns:     b.activeConns.Load(),
			TotalServed:     b.totalServed.Load(),
			TotalFailed:     b.totalFailed.Load(),
			RateBytesPerSec: b.RateBytesPerSec(),
			SpeedTier:       b.SpeedTier(),
			TorrentCount:    b.torrentCount.Load(),
			LastLatencyMs:   b.lastLatency.Load(),
			Weight:          b.Weight,
			LastCheck:       b.lastCheckTime(),
			LastError:       b.lastErrorStr(),
			UptimePct:       b.UptimePct(),
			WedgeStreak:     b.wedgeStreak.Load(),
			HasSSH:          b.HasSSH(),
			SSHHost:         b.SSHHost,
			SSHPort:         b.SSHPort,
			SSHUser:         b.SSHUser,
			SSHCmd:          b.SSHCmd,
		}
	}
	return out
}

// ApplySettings подменяет настройки в памяти без записи на диск — пара к
// Store.Reload, см. cluster.Pool.ApplySettings.
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

// SetSettings updates pool settings at runtime (persists via the store).
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

// ProbeNow runs an immediate health probe on one backend by ID.
func (p *Pool) ProbeNow(ctx context.Context, id string) (BackendStatus, bool) {
	b := p.FindByID(id)
	if b == nil {
		return BackendStatus{}, false
	}
	p.probe(ctx, b)
	for _, s := range p.Snapshot() {
		if s.ID == id {
			return s, true
		}
	}
	return BackendStatus{}, false
}

// ----------- health checking -----------

func (p *Pool) healthLoop(ctx context.Context) {
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
	for _, b := range p.Backends() {
		if !b.Enabled {
			continue
		}
		wg.Add(1)
		go func(backend *Backend) {
			defer wg.Done()
			p.probe(ctx, backend)
		}(b)
	}
	wg.Wait()
}

// probe checks a single backend's /echo endpoint.
func (p *Pool) probe(ctx context.Context, b *Backend) {
	host, authHdr := b.Target()

	probeCtx, cancel := context.WithTimeout(ctx, defaultHealthTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, host+"/echo", nil)
	if err != nil {
		b.lastError.Store(err.Error())
		p.markFailed(b)
		return
	}
	if authHdr != "" {
		req.Header.Set("Authorization", authHdr)
	}

	start := time.Now()
	resp, err := p.client.Do(req)
	b.lastLatency.Store(time.Since(start).Milliseconds())
	b.lastCheck.Store(time.Now())
	if err != nil {
		log.Debug().Err(err).Str("backend", host).Msg("torrbalancer: health probe failed")
		b.lastError.Store(err.Error())
		p.markFailed(b)
		return
	}
	defer resp.Body.Close()

	buf := make([]byte, 128)
	n, _ := resp.Body.Read(buf)
	body := string(buf[:n])
	if resp.StatusCode == http.StatusUnauthorized {
		b.lastError.Store("auth failed (401) — wrong login/password")
		p.markFailed(b)
		return
	}
	if resp.StatusCode != http.StatusOK {
		b.lastError.Store("HTTP " + resp.Status)
		p.markFailed(b)
		return
	}
	if !strings.Contains(body, "MatriX") {
		b.lastError.Store("unexpected /echo response")
		p.markFailed(b)
		return
	}
	// /echo жив — но это ещё не здоровье. Полу-зомби (прод 2026-08-13, Selectel:
	// /echo за миллисекунды, ЛЮБОЙ POST /torrents висит до таймаута) неделями
	// проходил echo-медосмотр и ловил add'ы → у клиентов «add http 502». Здоровье
	// подтверждает только рабочий torrents-API.
	listCtx, listCancel := context.WithTimeout(ctx, listProbeTimeout)
	count, terr := p.torrentsListCount(listCtx, host, authHdr)
	listCancel()
	if terr != nil {
		reason := "полу-зомби: /echo отвечает, а /torrents не работает (" + terr.Error() + ")"
		b.lastError.Store(reason)
		fails := p.markFailed(b)
		// Один промах — ещё не приговор: под пиком torrents-API живого бэкенда
		// мигает. Уводим из выдачи только на втором подряд.
		if misses := b.wedgeMisses.Add(1); misses < wedgeMissesToQuarantine {
			log.Debug().Str("backend", host).Int64("misses", misses).
				Msg("torrbalancer: torrents-API промахнулся, жду подтверждения")
			return
		}
		// Карантин — сразу (безвреден: HRW просто уводит торренты на 2 мин), а
		// вот авто-лечение (/shutdown, SSH) — только по УСТОЙЧИВОМУ зомби, порог
		// вдвое строже unhealthy: под пиковой стрим-нагрузкой torrents-API живого
		// бэкенда МИГАЕТ на десятки секунд (прод 2026-08-13, 203.0.113.97: curl
		// list 3×10с подряд, затем 3×0.05с) — миганию рестарт стоил бы всех его
		// стримов. Мигающий до 2×порога не дотягивает (recovered обнуляет счёт),
		// перманентный клин Selectel-типа — дотягивает за ~4 минуты.
		p.tripWedgeQuarantine(b, reason, fails >= 2*p.CurrentSettings().FailThreshold)
		return
	}
	b.torrentCount.Store(count)
	b.wedgeMisses.Store(0)
	b.lastError.Store("")
	p.markOK(b)
}

// torrentsListCount POSTs {action:"list"} and returns the torrent count. Any
// transport error / non-200 / non-JSON is a health failure — this is the check
// that catches the half-wedged zombie the /echo probe can't see.
func (p *Pool) torrentsListCount(ctx context.Context, host, authHdr string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, host+"/torrents", strings.NewReader(`{"action":"list"}`))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if authHdr != "" {
		req.Header.Set("Authorization", authHdr)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %s", resp.Status)
	}
	var arr []json.RawMessage
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&arr); err != nil {
		return 0, fmt.Errorf("bad JSON: %w", err)
	}
	return int64(len(arr)), nil
}

// TorrentsAPIAlive runs one immediate torrents-list check (independent of the
// probe loop) — used by the auto-heal escalation to verify a restart worked.
func (p *Pool) TorrentsAPIAlive(b *Backend) bool {
	host, authHdr := b.Target()
	ctx, cancel := context.WithTimeout(context.Background(), listProbeTimeout)
	defer cancel()
	_, err := p.torrentsListCount(ctx, host, authHdr)
	return err == nil
}

// healCooldown bounds how often auto-heal (shutdown → SSH restart) may run per
// backend, so a persistently wedged box doesn't get restart-stormed.
const healCooldown = 10 * time.Minute

// TryBeginHeal reports whether an auto-heal attempt may start now (CAS on the
// per-backend cooldown). The caller that gets true owns this heal window.
func (p *Pool) TryBeginHeal(b *Backend) bool {
	now := time.Now().UnixNano()
	last := b.lastHeal.Load()
	if now-last < int64(healCooldown) {
		return false
	}
	return b.lastHeal.CompareAndSwap(last, now)
}

// QuarantineWedged time-boxes a backend out of selection (same breaker window
// the stream strikes use). Reason lands in the admin dashboard. Used by the
// request path when a torrents-API call to the backend times out; лечение оно
// НЕ запускает — единичный таймаут запроса бывает и у здорового бэкенда под
// пиком, /shutdown за такое непозволителен. Устойчивого зомби добьёт проба.
func (p *Pool) QuarantineWedged(b *Backend, reason string) {
	if b == nil {
		return
	}
	b.lastError.Store(reason)
	p.tripWedgeQuarantine(b, reason, false)
}

func (p *Pool) tripWedgeQuarantine(b *Backend, reason string, fireHeal bool) {
	streak := b.wedgeStreak.Add(1)
	dur := wedgeQuarantineFor(streak)
	b.quarantineUntil.Store(time.Now().Add(dur).UnixNano())
	log.Warn().Str("backend", b.Host).Str("reason", reason).Bool("heal", fireHeal).
		Int64("wedgeStreak", streak).Dur("quarantine", dur).
		Msg("torrbalancer: backend quarantined (torrents API wedged)")
	if !fireHeal {
		return
	}
	if streak >= chronicWedgeStreak {
		// Хроник: ~20 минут клина, /shutdown и SSH уже пробовались и не помогли.
		// Дальше рестартовать бессмысленно и вредно — держим в карантине и раз в
		// час зовём человека.
		now := time.Now().UnixNano()
		last := b.lastChronicNotify.Load()
		if now-last >= int64(chronicNotifyEvery) && b.lastChronicNotify.CompareAndSwap(last, now) {
			if cb := p.OnChronic; cb != nil {
				go cb(b, streak)
			}
		}
		return
	}
	if cb := p.OnZombie; cb != nil && p.TryBeginHeal(b) {
		go cb(b, p.echoAlive(b))
	}
}

// wedgeQuarantineFor maps the consecutive-wedge streak to a quarantine length:
// 2, 4, 8, 16 минут, дальше — потолок maxWedgeQuarantine. Первый клин почти
// бесплатен для здорового-но-мигающего бэкенда, хронику полу-открытие достаётся
// всё реже.
func wedgeQuarantineFor(streak int64) time.Duration {
	dur := quarantineDur
	for i := int64(1); i < streak && dur < maxWedgeQuarantine; i++ {
		dur *= 2
	}
	return min(dur, maxWedgeQuarantine)
}

func (p *Pool) markFailed(b *Backend) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	b.consecOK = 0
	b.consecFails++
	if b.consecFails >= p.settings.FailThreshold {
		if b.healthy.Load() {
			log.Warn().Str("backend", b.Host).Int("fails", b.consecFails).Msg("torrbalancer: backend marked unhealthy")
		}
		b.healthy.Store(false)
	}
	b.recordProbe(false)
	return b.consecFails
}

func (p *Pool) markOK(b *Backend) {
	p.mu.Lock()
	defer p.mu.Unlock()
	b.consecFails = 0
	b.consecOK++
	if s := b.wedgeStreak.Swap(0); s > 0 {
		b.lastChronicNotify.Store(0)
		// Клин кончился — torrents-API снова отвечает. Эскалированный хвост
		// карантина (до 30 мин) здоровому уже не нужен: ужимаем до базовых 2 мин,
		// стрим-брейкерный карантин короче этого не бывает и не пострадает.
		if capNs := time.Now().Add(quarantineDur).UnixNano(); b.quarantineUntil.Load() > capNs {
			b.quarantineUntil.Store(capNs)
		}
	}
	if !b.healthy.Load() && b.consecOK >= p.settings.RecoverThreshold {
		b.healthy.Store(true)
		log.Info().Str("backend", b.Host).Msg("torrbalancer: backend recovered")
	}
	b.recordProbe(true)
}
