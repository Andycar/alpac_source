package proxycore

import (
	"fmt"
	"strings"
	"sync"

	"github.com/rs/zerolog/log"

	"lampac-go/internal/httpclient"
	"lampac-go/internal/sidecar"
	sidecaruri "lampac-go/internal/sidecar/uri"
)

// PoolEntry describes one proxy to manage.
type PoolEntry struct {
	URI       string   `json:"uri" toml:"uri"`
	Label     string   `json:"label" toml:"label"`
	Balancers []string `json:"balancers" toml:"balancers"`
	Engine    string   `json:"engine" toml:"engine"` // "proxycore", "xray", "mihomo", "" (auto)
}

// PoolStatus is the status of a running proxy instance (for API responses).
type PoolStatus struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Protocol    string   `json:"protocol"`
	Server      string   `json:"server"`
	Engine      string   `json:"engine"`
	SOCKSAddr   string   `json:"socks_addr"`
	Balancers   []string `json:"balancers"`
	Alive       bool     `json:"alive"`
	UptimeSec   float64  `json:"uptime_sec"`
	ActiveConns int64    `json:"active_conns"`
	TotalConns  int64    `json:"total_conns"`
	BytesUp     int64    `json:"bytes_up"`
	BytesDown   int64    `json:"bytes_down"`
	FailCount   int64    `json:"fail_count"`
	LatencyMs   int64    `json:"latency_ms"`
	AvgLatMs    int64    `json:"avg_latency_ms"`
	Country     string   `json:"country,omitempty"`
	CountryCode string   `json:"country_code,omitempty"`
	Flag        string   `json:"flag,omitempty"`
	ServerIP    string   `json:"server_ip,omitempty"`
}

// Pool manages multiple proxy instances with routing rules.
type Pool struct {
	mu        sync.RWMutex
	instances []*Instance
	entries   []PoolEntry // original config entries (1:1 with instances)
	router    *router
	prober    latencyProber
	basePort  int
	binDir    string // directory for WARP config cache
}

// NewPool creates a new proxy pool from config entries.
// binDir is used to cache WARP registration state (warp-config.json).
func NewPool(entries []PoolEntry, basePort int, binDir string) (*Pool, error) {
	if basePort == 0 {
		basePort = 40000
	}

	p := &Pool{
		entries:  entries,
		router:   newRouter(),
		basePort: basePort,
		binDir:   binDir,
	}

	if err := p.startAll(); err != nil {
		p.StopAll()
		return nil, err
	}

	// Start background latency probing.
	p.startProber()

	return p, nil
}

// startAll starts all proxy instances and registers them with httpclient.
func (p *Pool) startAll() error {
	for i, entry := range p.entries {
		// Skip non-proxycore entries (they'll be handled by sidecar).
		if entry.Engine != "" && entry.Engine != "proxycore" {
			continue
		}

		// Heal the "_keep_" sentinel: the edit modal used to send it as a
		// "don't touch the URI" marker, but an older handler version wrote
		// it through literally and corrupted the entry. If the label clearly
		// references WARP, fall back to "wg://warp" so the auto-registration
		// below recovers it; otherwise skip with a warning so the rest of
		// the pool still starts.
		uri := strings.TrimSpace(entry.URI)
		if uri == "_keep_" {
			if labelMentionsWARP(entry.Label) {
				log.Warn().Str("label", entry.Label).Msg("proxycore: healing corrupt _keep_ URI → wg://warp")
				uri = "wg://warp"
			} else {
				log.Warn().Int("idx", i).Str("label", entry.Label).Msg("proxycore: skipping entry with corrupt _keep_ URI — delete and re-add")
				continue
			}
		}

		// Parse URI. Fail-soft per entry: one broken proxy must not block the
		// whole pool, otherwise the user's other entries (and unrelated
		// admin endpoints downstream of the reload) stay offline.
		out, _, err := sidecaruri.Parse(uri)
		if err != nil {
			log.Warn().Err(err).Int("idx", i).Str("label", entry.Label).Str("uri", uri).Msg("proxycore: skipping unparseable entry")
			continue
		}

		// WARP auto-registration: resolve actual WireGuard config from Cloudflare API.
		//
		// Triggered on three shapes:
		//   1. "wg://warp" — the sentinel URI from the one-click Add button.
		//   2. "wg://<priv>@<host>:0?..." — legacy entry produced by an older
		//      buggy `warpConfigToURI` that wrote a `:0` placeholder port.
		//      `newWireGuardDialer` would otherwise reject it with
		//      "wireguard: server and port required" and pool startup would
		//      fail until the user deleted the entry by hand. Heal it instead.
		//   3. Any wireguard entry whose label clearly references WARP and
		//      whose host/port doesn't survive parsing.
		looksWARP := out.Protocol == "wireguard" && (out.Server == "warp" ||
			(labelMentionsWARP(entry.Label) && (out.Server == "" || out.Port == 0)))
		if looksWARP {
			binDir := p.binDir
			if binDir == "" {
				binDir = "."
			}
			log.Info().Str("label", entry.Label).Str("bin_dir", binDir).Msg("proxycore: registering Cloudflare WARP")
			warpCfg, werr := sidecar.RegisterOrLoadWARP(binDir)
			if werr != nil {
				return fmt.Errorf("proxycore: WARP registration for %q: %w", entry.Label, werr)
			}
			out = sidecar.WARPToOutbound(warpCfg)
			log.Info().
				Str("label", entry.Label).
				Str("server", out.Server).
				Int("port", out.Port).
				Strs("local_addr", out.LocalAddr).
				Int("reserved_len", len(out.Reserved)).
				Msg("proxycore: WARP resolved")
			if out.Server == "" || out.Port == 0 {
				return fmt.Errorf("proxycore: WARP resolution returned empty server/port for %q (cfg.endpoint=%q)", entry.Label, warpCfg.Endpoint)
			}
		}

		// Check if proxycore supports this protocol.
		if !isSupported(out.Protocol) {
			log.Warn().Str("proto", out.Protocol).Str("label", entry.Label).
				Msg("proxycore: protocol not supported, use engine=xray or engine=mihomo")
			continue
		}

		port := p.basePort + i
		id := fmt.Sprintf("proxy-%d", i)
		label := entry.Label
		if label == "" {
			label = fmt.Sprintf("%s-%s:%d", out.Protocol, out.Server, out.Port)
		}

		inst, err := Start(out, port, id, label)
		if err != nil {
			return fmt.Errorf("proxycore: start %s: %w", label, err)
		}

		p.mu.Lock()
		p.instances = append(p.instances, inst)
		p.mu.Unlock()

		// Register with httpclient for balancer routing.
		if len(entry.Balancers) > 0 {
			httpclient.RegisterProxiedBalancer(inst.SOCKSAddr(), entry.Balancers)
		}
		if label != "" {
			httpclient.RegisterProxyLabel(label, inst.SOCKSAddr())
		}

		log.Info().
			Str("id", id).
			Str("label", label).
			Str("proto", out.Protocol).
			Strs("balancers", entry.Balancers).
			Msg("proxycore: entry registered")
	}

	return nil
}

// StopAll stops all instances and the prober.
func (p *Pool) StopAll() {
	p.stopProber()
	p.mu.Lock()
	for _, inst := range p.instances {
		inst.Stop()
	}
	p.instances = nil
	p.mu.Unlock()
}

// Status returns the status of all running instances.
func (p *Pool) Status() []PoolStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()

	statuses := make([]PoolStatus, 0, len(p.instances))
	for i, inst := range p.instances {
		s := inst.Stats()
		inst.mu.Lock()
		latMs := inst.latencyLast.Milliseconds()
		avgMs := inst.latencyAvg.Milliseconds()
		country := inst.country
		countryCode := inst.countryCode
		flag := inst.flag
		serverIP := inst.serverIP
		inst.mu.Unlock()

		var balancers []string
		if i < len(p.entries) {
			balancers = p.entries[i].Balancers
		}

		statuses = append(statuses, PoolStatus{
			ID:          inst.id,
			Label:       inst.label,
			Protocol:    inst.protocol,
			Server:      inst.server,
			Engine:      "proxycore",
			SOCKSAddr:   inst.socksAddr,
			Balancers:   balancers,
			Alive:       inst.Alive(),
			UptimeSec:   inst.UptimeSeconds(),
			ActiveConns: s.ActiveConns,
			TotalConns:  s.TotalConns,
			BytesUp:     s.BytesUp,
			BytesDown:   s.BytesDown,
			FailCount:   s.FailCount,
			LatencyMs:   latMs,
			AvgLatMs:    avgMs,
			Country:     country,
			CountryCode: countryCode,
			Flag:        flag,
			ServerIP:    serverIP,
		})
	}

	return statuses
}

// Instances returns all running instances (for routing).
func (p *Pool) Instances() []*Instance {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make([]*Instance, len(p.instances))
	copy(result, p.instances)
	return result
}

// SetRules updates routing rules.
func (p *Pool) SetRules(rules []RoutingRule) {
	p.router.setRules(rules)
}

// Rules returns current routing rules.
func (p *Pool) Rules() []RoutingRule {
	p.router.mu.RLock()
	defer p.router.mu.RUnlock()
	result := make([]RoutingRule, len(p.router.rules))
	copy(result, p.router.rules)
	return result
}

// ResolveProxy finds the best proxy for a balancer using routing rules.
func (p *Pool) ResolveProxy(balancer string) *Instance {
	return p.router.resolve(balancer, p.Instances())
}

// labelMentionsWARP returns true if the entry label suggests it's a Cloudflare
// WARP endpoint — case-insensitive substring match on "warp". Used to detect
// stale/half-built WARP entries that should be healed via re-registration
// rather than failing the whole pool startup.
func labelMentionsWARP(label string) bool {
	return strings.Contains(strings.ToLower(label), "warp")
}

// isSupported returns true if proxycore can handle this protocol natively.
func isSupported(protocol string) bool {
	switch strings.ToLower(protocol) {
	case "vless", "trojan", "ss", "hysteria2", "socks5", "http", "vmess", "wireguard":
		return true
	default:
		return false
	}
}

// Entries returns the original config entries.
func (p *Pool) Entries() []PoolEntry {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make([]PoolEntry, len(p.entries))
	copy(result, p.entries)
	return result
}
