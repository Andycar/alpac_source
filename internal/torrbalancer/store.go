// Package torrbalancer load-balances requests across multiple backend
// TorrServer instances. It mirrors the shape of internal/cluster (JSON store +
// runtime pool + background health probes) but is a separate implementation
// because TorrServer selection is sticky-by-infohash (a torrent is stateful and
// must always stream from the backend it was added to), the health probe is
// TorrServer's /echo endpoint, and each backend carries its own credentials.
package torrbalancer

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

// StoredBackend is the on-disk representation of one TorrServer backend.
// Runtime stats (active conns, latency, health) live separately on *Backend.
type StoredBackend struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Host      string    `json:"host"`               // full URL, no trailing slash
	Login     string    `json:"login,omitempty"`    // per-backend Basic-Auth user
	Password  string    `json:"password,omitempty"` // per-backend Basic-Auth password
	Weight    int       `json:"weight"`             // HRW weight, >=1
	Enabled   bool      `json:"enabled"`
	Notes     string    `json:"notes,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// SSH-доступ к машине бэкенда — даёт балансеру «полное управление»: ручной
	// рестарт из админки и эскалацию автолечения (когда /shutdown не вернул
	// бэкенд к жизни). Пусто = SSH-управление для этого бэкенда недоступно.
	SSHHost     string `json:"ssh_host,omitempty"` // пусто → хост из Host (без порта)
	SSHPort     int    `json:"ssh_port,omitempty"` // 0 → 22
	SSHUser     string `json:"ssh_user,omitempty"` // пусто → root
	SSHPassword string `json:"ssh_password,omitempty"`
	SSHCmd      string `json:"ssh_cmd,omitempty"` // пусто → systemctl restart torrserver

	// Прямая отдача зрителю (см. DirectLink). DirectURL — публичный https-фронт
	// ЭТОЙ машины (nginx перед 127.0.0.1:8090, deploy/ts-direct.conf); когда он
	// задан, /ts/stream отвечает 302 на подписанную ссылку, и байты идут
	// бэкенд→зритель, минуя main. Пусто = отдача через main, как прежде — так
	// бэкенды переводятся по одному. DirectSecret — общий с nginx secure_link.
	DirectURL    string `json:"direct_url,omitempty"`
	DirectSecret string `json:"direct_secret,omitempty"`
}

// Settings controls pool behaviour. Persisted alongside backends.
type Settings struct {
	// FailThreshold consecutive probe failures before marking a backend unhealthy.
	FailThreshold int `json:"fail_threshold"`
	// RecoverThreshold consecutive successes before marking healthy again.
	RecoverThreshold int `json:"recover_threshold"`
	// ProbeIntervalSec between background health probes.
	ProbeIntervalSec int `json:"probe_interval_sec"`
	// DebugHeader: emit X-Lampac-TS-Backend on proxied responses.
	DebugHeader bool `json:"debug_header"`
	// AutoRestartZombie: when the stream circuit-breaker quarantines a backend
	// whose /echo still answers (half-wedged zombie), send it /shutdown so a
	// systemd Restart=always deployment restarts it automatically. Off by
	// default — the operator must opt in to remote restarts.
	AutoRestartZombie bool `json:"auto_restart_zombie"`
	// DirectTTLSec — срок жизни подписанной прямой ссылки (DirectLink). Плеер
	// открывает каждый seek заново через main и получает свежий 302, так что
	// срок важен лишь для одного непрерывного чтения; 6 часов покрывают любой
	// фильм с запасом и не дают ссылке жить сутками.
	DirectTTLSec int `json:"direct_ttl_sec"`
}

// DefaultSettings returns sane defaults.
func DefaultSettings() Settings {
	return Settings{
		FailThreshold:    3,
		RecoverThreshold: 2,
		ProbeIntervalSec: 30,
		DebugHeader:      true,
		DirectTTLSec:     6 * 3600,
	}
}

func (st *Settings) normalize() {
	if st.FailThreshold < 1 {
		st.FailThreshold = 3
	}
	if st.RecoverThreshold < 1 {
		st.RecoverThreshold = 2
	}
	if st.ProbeIntervalSec < 5 {
		st.ProbeIntervalSec = 30
	}
	if st.DirectTTLSec < 60 {
		st.DirectTTLSec = 6 * 3600
	}
}

// Store persists TorrServer backends + settings to JSON. Thread-safe.
type Store struct {
	mu       sync.RWMutex
	dir      string
	backends []StoredBackend
	settings Settings
}

// NewStore opens (or creates) the store under {dbDir}/database/torrbalancer/.
// If the store is empty, it seeds backend #1 from the legacy single-TorrServer
// config (seedFrom) so existing deployments keep working unchanged.
func NewStore(dbDir string, seedFrom config.TorrServerConfig) (*Store, error) {
	dir := filepath.Join(dbDir, "database", "torrbalancer")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("torrbalancer store: mkdir %s: %w", dir, err)
	}
	s := &Store{dir: dir, settings: DefaultSettings()}
	if err := s.load(); err != nil {
		return nil, err
	}
	if len(s.backends) == 0 {
		if seed, ok := seedBackend(seedFrom); ok {
			s.backends = append(s.backends, seed)
			if err := s.persist(); err != nil {
				return nil, err
			}
			log.Info().Str("host", seed.Host).Msg("torrbalancer: seeded backend from [torrserver] config")
		}
	}
	return s, nil
}

// seedBackend derives the initial backend from the single-server config,
// deriving the host exactly as the legacy newTSProxy did.
func seedBackend(cfg config.TorrServerConfig) (StoredBackend, bool) {
	host := strings.TrimRight(strings.TrimSpace(cfg.URL), "/")
	if host == "" && cfg.Port > 0 {
		host = fmt.Sprintf("http://127.0.0.1:%d", cfg.Port)
	}
	if host == "" {
		return StoredBackend{}, false
	}
	now := time.Now().UTC()
	return StoredBackend{
		ID:        randID(),
		Name:      hostToName(host),
		Host:      host,
		Login:     strings.TrimSpace(cfg.Login),
		Password:  cfg.Password,
		Weight:    1,
		Enabled:   true,
		Notes:     "seeded from [torrserver] config",
		CreatedAt: now,
		UpdatedAt: now,
	}, true
}

func (s *Store) load() error {
	if data, err := os.ReadFile(filepath.Join(s.dir, "settings.json")); err == nil {
		var st Settings
		if err := json.Unmarshal(data, &st); err == nil {
			st.normalize()
			s.settings = st
		} else {
			log.Warn().Err(err).Msg("torrbalancer: settings.json malformed, using defaults")
		}
	}
	data, err := os.ReadFile(filepath.Join(s.dir, "backends.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var list []StoredBackend
	if err := json.Unmarshal(data, &list); err != nil {
		log.Warn().Err(err).Msg("torrbalancer: backends.json malformed, starting empty")
		return nil
	}
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
	s.backends = list
	return nil
}

// Reload перечитывает backends.json и settings.json с диска. См. cluster.Store.Reload:
// без этого смена веса бэкенда в файле требовала перезапуска всего сервера.
func (s *Store) Reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

func (s *Store) persist() error {
	bb, err := json.MarshalIndent(s.backends, "", "  ")
	if err != nil {
		return err
	}
	sb, err := json.MarshalIndent(s.settings, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(s.dir, "backends.json"), bb); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.dir, "settings.json"), sb)
}

// Snapshot returns a copy of the backend list.
func (s *Store) Snapshot() []StoredBackend {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]StoredBackend, len(s.backends))
	copy(out, s.backends)
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

// Add inserts a new backend (auto-generated ID). Returns the stored entry.
func (s *Store) Add(name, host, login, password string, weight int, enabled bool, notes string) (StoredBackend, error) {
	host = normalizeHost(host)
	if host == "" {
		return StoredBackend{}, errors.New("host is required")
	}
	if weight < 1 {
		weight = 1
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = hostToName(host)
	}
	now := time.Now().UTC()
	b := StoredBackend{
		ID:        randID(),
		Name:      name,
		Host:      host,
		Login:     strings.TrimSpace(login),
		Password:  password,
		Weight:    weight,
		Enabled:   enabled,
		Notes:     strings.TrimSpace(notes),
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ex := range s.backends {
		if strings.EqualFold(ex.Host, b.Host) {
			return StoredBackend{}, fmt.Errorf("host %q already exists", b.Host)
		}
	}
	s.backends = append(s.backends, b)
	if err := s.persist(); err != nil {
		return StoredBackend{}, err
	}
	return b, nil
}

// UpdatePatch holds optional field updates. Nil fields are left unchanged.
type UpdatePatch struct {
	Name     *string
	Host     *string
	Login    *string
	Password *string
	Weight   *int
	Enabled  *bool
	Notes    *string

	SSHHost     *string
	SSHPort     *int
	SSHUser     *string
	SSHPassword *string
	SSHCmd      *string

	DirectURL    *string
	DirectSecret *string
}

// Update mutates an existing backend by ID.
func (s *Store) Update(id string, p UpdatePatch) (StoredBackend, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.backends {
		if s.backends[i].ID != id {
			continue
		}
		b := &s.backends[i]
		if p.Name != nil {
			b.Name = strings.TrimSpace(*p.Name)
			if b.Name == "" {
				b.Name = hostToName(b.Host)
			}
		}
		if p.Host != nil {
			h := normalizeHost(*p.Host)
			if h == "" {
				return StoredBackend{}, errors.New("host cannot be empty")
			}
			for j, ex := range s.backends {
				if j != i && strings.EqualFold(ex.Host, h) {
					return StoredBackend{}, fmt.Errorf("host %q already exists", h)
				}
			}
			b.Host = h
		}
		if p.Login != nil {
			b.Login = strings.TrimSpace(*p.Login)
		}
		if p.Password != nil {
			b.Password = *p.Password
		}
		if p.Weight != nil {
			w := *p.Weight
			if w < 1 {
				w = 1
			}
			b.Weight = w
		}
		if p.Enabled != nil {
			b.Enabled = *p.Enabled
		}
		if p.Notes != nil {
			b.Notes = strings.TrimSpace(*p.Notes)
		}
		if p.SSHHost != nil {
			b.SSHHost = strings.TrimSpace(*p.SSHHost)
		}
		if p.SSHPort != nil {
			port := *p.SSHPort
			if port < 0 || port > 65535 {
				port = 0
			}
			b.SSHPort = port
		}
		if p.SSHUser != nil {
			b.SSHUser = strings.TrimSpace(*p.SSHUser)
		}
		if p.SSHPassword != nil {
			b.SSHPassword = *p.SSHPassword
		}
		if p.SSHCmd != nil {
			b.SSHCmd = strings.TrimSpace(*p.SSHCmd)
		}
		if p.DirectURL != nil {
			b.DirectURL = strings.TrimRight(strings.TrimSpace(*p.DirectURL), "/")
		}
		if p.DirectSecret != nil {
			b.DirectSecret = strings.TrimSpace(*p.DirectSecret)
		}
		b.UpdatedAt = time.Now().UTC()
		if err := s.persist(); err != nil {
			return StoredBackend{}, err
		}
		return *b, nil
	}
	return StoredBackend{}, fmt.Errorf("backend %q not found", id)
}

// Delete removes a backend by ID.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, b := range s.backends {
		if b.ID == id {
			s.backends = append(s.backends[:i], s.backends[i+1:]...)
			return s.persist()
		}
	}
	return fmt.Errorf("backend %q not found", id)
}

// Find returns a backend by ID.
func (s *Store) Find(id string) (StoredBackend, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, b := range s.backends {
		if b.ID == id {
			return b, true
		}
	}
	return StoredBackend{}, false
}

// SortedByName returns backends sorted by Name (stable for UI display).
func (s *Store) SortedByName() []StoredBackend {
	list := s.Snapshot()
	sort.Slice(list, func(i, j int) bool {
		return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name)
	})
	return list
}

// --------------- helpers ---------------

func normalizeHost(host string) string {
	host = strings.TrimRight(strings.TrimSpace(host), "/")
	if host == "" {
		return ""
	}
	if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
		host = "http://" + host
	}
	return host
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
