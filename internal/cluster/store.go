package cluster

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"

	"github.com/rs/zerolog/log"
)

// StoredNode is the on-disk representation of a cluster node.
// Runtime stats (active conns, latency, health) live separately on *Node.
type StoredNode struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Host    string `json:"host"`
	Weight  int    `json:"weight"`
	Enabled bool   `json:"enabled"`
	Region  string `json:"region,omitempty"`
	// EdgeURL — клиентский адрес ноды (https://edge-a.example.org): по нему primary
	// понимает, какую ноду просит зритель параметром edge=, и куда можно
	// отправлять /lite источников, чей поток привязан к добывшему серверу.
	EdgeURL string `json:"edge_url,omitempty"`
	// EdgeLabel — как ноду называть зрителю («Санкт-Петербург»): домен на
	// экране телевизора ничего человеку не говорит, город — говорит.
	EdgeLabel string    `json:"edge_label,omitempty"`
	Notes     string    `json:"notes,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Settings controls pool behaviour. Persisted alongside nodes.
type Settings struct {
	// Strategy: "least-conns" | "latency" | "hybrid". Default "hybrid".
	Strategy string `json:"strategy"`
	// LatencyWeight 0..1 (only used by hybrid). 0 = pure least-conns, 1 = pure latency.
	LatencyWeight float64 `json:"latency_weight"`
	// FailThreshold consecutive probe failures before marking unhealthy.
	FailThreshold int `json:"fail_threshold"`
	// RecoverThreshold consecutive successes before marking healthy again.
	RecoverThreshold int `json:"recover_threshold"`
	// ProbeIntervalSec between background health probes.
	ProbeIntervalSec int `json:"probe_interval_sec"`
	// MaxRetries forward attempts on 5xx / connection error.
	MaxRetries int `json:"max_retries"`
	// AdvertiseName for X-Lampac-Server header (this node's display name).
	AdvertiseName string `json:"advertise_name"`
	// AdvertiseRegion for /api/servers/info (this node's region tag).
	AdvertiseRegion string `json:"advertise_region"`
	// ExposePublicList: if true, /api/servers/list returns name/region of nodes
	// (no hosts unless PublicHosts is true).
	ExposePublicList bool `json:"expose_public_list"`
	// PublicHosts: if true, expose node hosts in the public list (allows the
	// Lampa widget to direct-ping nodes).
	PublicHosts bool `json:"public_hosts"`
	// LinkMbps — ширина канала хостов для сводки мощностей: id ноды (или
	// "primary") → Мбит/с. Без неё вердикт «канал забит» вынести нельзя, и
	// сводка лишь показывает пик и просит задать значение.
	LinkMbps map[string]int `json:"link_mbps,omitempty"`
	// LocalWeight is the primary's capacity relative to a node of weight 1.
	// Pick() scores local as localConns/LocalWeight against each node's
	// conns/latency score, so leaving this at 1 declares an 8-core primary to be
	// exactly as capable as a 2-core node.
	//
	// Measured on prod 2026-09-01 with LocalWeight=1: the primary kept only 1081
	// of ~15900 /lite/ requests per 10 min (7%) while sitting at 3% CPU pressure
	// on 8 cores, and the 2-core nodes it fed ran at 41% and 71% pressure,
	// answering 94-99% of requests slower than 5 seconds. Local stopped winning
	// the comparison at ~24 in-flight requests while the nodes were allowed 35
	// and 44.
	//
	// Set it to roughly the core-count ratio (8-core primary, 2-core nodes → 4).
	// 0 or negative is normalised to 1, which reproduces the old behaviour.
	LocalWeight int `json:"local_weight"`
	// ForceNode: testing mode that always forwards to a node when one is
	// available, ignoring the primary's local-vs-node score comparison.
	// Useful for proving the cluster path works end-to-end.
	ForceNode bool `json:"force_node"`
	// Rules: per-balancer routing overrides. Evaluated before the strategy
	// score. Use cases: geo-routing (UA sources → UA node), load isolation
	// (heavy source pinned to dedicated node), debugging.
	Rules []RoutingRule `json:"rules,omitempty"`
}

// RoutingRule routes a specific balancer to a specific destination.
// Target options:
//   - "node"    — must go through any healthy node (skip primary)
//   - "local"   — must be handled locally on primary (skip all nodes)
//   - "node-id" — must go through the named node (by ID); falls back to
//     "node" behaviour if that node is unhealthy/disabled
//   - "sticky"  — one title/post always resolves on the same node (weighted rendezvous
//     hash of postid or title+year, see Pool.pickStickySkip); NodeIDs optionally narrow
//     the ring. For CDNs that pin a signed link to the first IP per account+post
//     (Filmix, 22.09.2026): spreading a post over several nodes sharing an account
//     turns every other node's viewers into 429s.
//   - "nodes"   — only the listed nodes (NodeIDs) may resolve it; the viewer's
//     edge hint is honoured only when it names one of them; none healthy →
//     primary. For sources whose links are bound to the resolver AND not every
//     node gets playable ones (AhueRezka: 3 of 9 on 21.09.2026) — "edge" would
//     happily pick a node that only ever gets dead links.
type RoutingRule struct {
	Balancer string   `json:"balancer"`           // e.g. "kinotochka"
	Target   string   `json:"target"`             // "node" | "local" | "node-id" | "nodes" | "edge" | "sticky"
	NodeID   string   `json:"node_id,omitempty"`  // when Target == "node-id"
	NodeIDs  []string `json:"node_ids,omitempty"` // when Target == "nodes" | "sticky" (optional ring)
	Comment  string   `json:"comment,omitempty"`  // optional human note
}

// DefaultSettings returns sane defaults.
func DefaultSettings() Settings {
	return Settings{
		Strategy:         "hybrid",
		LatencyWeight:    0.4,
		LocalWeight:      1,
		FailThreshold:    3,
		RecoverThreshold: 2,
		ProbeIntervalSec: 30,
		MaxRetries:       2,
		AdvertiseName:    "",
		AdvertiseRegion:  "",
		ExposePublicList: true,
		PublicHosts:      false,
	}
}

// Store persists cluster nodes + settings to JSON. Thread-safe.
type Store struct {
	mu       sync.RWMutex
	dir      string
	nodes    []StoredNode
	settings Settings
}

// NewStore opens (or creates) the cluster store under {dbDir}/database/cluster/.
// If the store is empty and seedFrom has nodes, they are imported as the
// initial set (one-time migration from config.toml).
func NewStore(dbDir string, seedFrom config.ClusterConfig) (*Store, error) {
	dir := filepath.Join(dbDir, "database", "cluster")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("cluster store: mkdir %s: %w", dir, err)
	}
	s := &Store{dir: dir, settings: DefaultSettings()}
	if err := s.load(); err != nil {
		return nil, err
	}
	if len(s.nodes) == 0 && len(seedFrom.Nodes) > 0 {
		seeded := 0
		for _, n := range seedFrom.Nodes {
			if strings.TrimSpace(n.Host) == "" {
				continue
			}
			s.nodes = append(s.nodes, StoredNode{
				ID:        randID(),
				Name:      hostToName(n.Host),
				Host:      strings.TrimRight(n.Host, "/"),
				Weight:    maxInt(1, n.Weight),
				Enabled:   true,
				CreatedAt: time.Now().UTC(),
				UpdatedAt: time.Now().UTC(),
			})
			seeded++
		}
		if seeded > 0 {
			if err := s.persist(); err != nil {
				return nil, err
			}
			log.Info().Int("seeded", seeded).Msg("cluster: seeded nodes from config.toml")
		}
	}
	return s, nil
}

func (s *Store) load() error {
	// Settings.
	if data, err := os.ReadFile(filepath.Join(s.dir, "settings.json")); err == nil {
		var st Settings
		if err := json.Unmarshal(data, &st); err == nil {
			st.normalize()
			s.settings = st
		} else {
			log.Warn().Err(err).Msg("cluster: settings.json malformed, using defaults")
		}
	}
	// Nodes.
	data, err := os.ReadFile(filepath.Join(s.dir, "nodes.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var list []StoredNode
	if err := json.Unmarshal(data, &list); err != nil {
		log.Warn().Err(err).Msg("cluster: nodes.json malformed, starting empty")
		return nil
	}
	// Sanitize.
	for i := range list {
		if list[i].Weight < 1 {
			list[i].Weight = 1
		}
		list[i].Host = strings.TrimRight(strings.TrimSpace(list[i].Host), "/")
		if list[i].ID == "" {
			list[i].ID = randID()
		}
		if list[i].Name == "" {
			list[i].Name = hostToName(list[i].Host)
		}
	}
	s.nodes = list
	return nil
}

// Reload перечитывает nodes.json и settings.json с диска поверх копии в памяти.
// Нужен для SIGHUP: load() зовётся только из конструктора, поэтому правка файла
// мимо админки раньше доезжала лишь перезапуском — а он рвёт все живые /proxy-потоки.
// Битый или пропавший файл load() переживает сам, оставляя прежний список.
func (s *Store) Reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

func (s *Store) persist() error {
	// Caller holds s.mu (or call from constructor before goroutines).
	nodesPath := filepath.Join(s.dir, "nodes.json")
	settingsPath := filepath.Join(s.dir, "settings.json")
	nb, err := json.MarshalIndent(s.nodes, "", "  ")
	if err != nil {
		return err
	}
	sb, err := json.MarshalIndent(s.settings, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(nodesPath, nb); err != nil {
		return err
	}
	return writeAtomic(settingsPath, sb)
}

// Snapshot returns a copy of the node list.
func (s *Store) Snapshot() []StoredNode {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]StoredNode, len(s.nodes))
	copy(out, s.nodes)
	return out
}

// Settings returns a copy of the current settings.
func (s *Store) Settings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

// SetSettings overwrites settings (normalizes & persists).
func (s *Store) SetSettings(st Settings) error {
	st.normalize()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings = st
	return s.persist()
}

// Add inserts a new node (auto-generated ID). Returns the stored entry.
func (s *Store) Add(name, host string, weight int, enabled bool, region, notes string) (StoredNode, error) {
	host = strings.TrimRight(strings.TrimSpace(host), "/")
	if host == "" {
		return StoredNode{}, errors.New("host is required")
	}
	if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
		host = "http://" + host
	}
	if weight < 1 {
		weight = 1
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = hostToName(host)
	}
	now := time.Now().UTC()
	n := StoredNode{
		ID:        randID(),
		Name:      name,
		Host:      host,
		Weight:    weight,
		Enabled:   enabled,
		Region:    strings.TrimSpace(region),
		Notes:     strings.TrimSpace(notes),
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ex := range s.nodes {
		if strings.EqualFold(ex.Host, n.Host) {
			return StoredNode{}, fmt.Errorf("host %q already exists", n.Host)
		}
	}
	s.nodes = append(s.nodes, n)
	if err := s.persist(); err != nil {
		return StoredNode{}, err
	}
	return n, nil
}

// Update mutates an existing node by ID. Fields with their zero value in
// `patch` are skipped EXCEPT for Enabled which is always applied (toggle).
type UpdatePatch struct {
	Name    *string
	Host    *string
	Weight  *int
	Enabled *bool
	Region  *string
	Notes   *string
}

func (s *Store) Update(id string, p UpdatePatch) (StoredNode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.nodes {
		if s.nodes[i].ID != id {
			continue
		}
		n := &s.nodes[i]
		if p.Name != nil {
			n.Name = strings.TrimSpace(*p.Name)
			if n.Name == "" {
				n.Name = hostToName(n.Host)
			}
		}
		if p.Host != nil {
			h := strings.TrimRight(strings.TrimSpace(*p.Host), "/")
			if h == "" {
				return StoredNode{}, errors.New("host cannot be empty")
			}
			if !strings.HasPrefix(h, "http://") && !strings.HasPrefix(h, "https://") {
				h = "http://" + h
			}
			for j, ex := range s.nodes {
				if j != i && strings.EqualFold(ex.Host, h) {
					return StoredNode{}, fmt.Errorf("host %q already exists", h)
				}
			}
			n.Host = h
		}
		if p.Weight != nil {
			w := *p.Weight
			if w < 1 {
				w = 1
			}
			n.Weight = w
		}
		if p.Enabled != nil {
			n.Enabled = *p.Enabled
		}
		if p.Region != nil {
			n.Region = strings.TrimSpace(*p.Region)
		}
		if p.Notes != nil {
			n.Notes = strings.TrimSpace(*p.Notes)
		}
		n.UpdatedAt = time.Now().UTC()
		if err := s.persist(); err != nil {
			return StoredNode{}, err
		}
		return *n, nil
	}
	return StoredNode{}, fmt.Errorf("node %q not found", id)
}

// Delete removes a node by ID.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, n := range s.nodes {
		if n.ID == id {
			s.nodes = append(s.nodes[:i], s.nodes[i+1:]...)
			return s.persist()
		}
	}
	return fmt.Errorf("node %q not found", id)
}

// Find returns a node by ID.
func (s *Store) Find(id string) (StoredNode, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, n := range s.nodes {
		if n.ID == id {
			return n, true
		}
	}
	return StoredNode{}, false
}

// SortedByName returns nodes sorted by Name (stable for UI display).
func (s *Store) SortedByName() []StoredNode {
	list := s.Snapshot()
	sort.Slice(list, func(i, j int) bool {
		return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name)
	})
	return list
}

// --------------- helpers ---------------

func (st *Settings) normalize() {
	if st.Strategy != "least-conns" && st.Strategy != "latency" && st.Strategy != "hybrid" {
		st.Strategy = "hybrid"
	}
	if st.LocalWeight < 1 {
		st.LocalWeight = 1
	}
	if st.LatencyWeight < 0 {
		st.LatencyWeight = 0
	}
	if st.LatencyWeight > 1 {
		st.LatencyWeight = 1
	}
	if st.FailThreshold < 1 {
		st.FailThreshold = 3
	}
	if st.RecoverThreshold < 1 {
		st.RecoverThreshold = 2
	}
	if st.ProbeIntervalSec < 5 {
		st.ProbeIntervalSec = 30
	}
	if st.MaxRetries < 0 {
		st.MaxRetries = 0
	}
	if st.MaxRetries > 5 {
		st.MaxRetries = 5
	}
}

func randID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func hostToName(host string) string {
	h := strings.TrimPrefix(host, "https://")
	h = strings.TrimPrefix(h, "http://")
	if i := strings.Index(h, "/"); i > 0 {
		h = h[:i]
	}
	return h
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
