package proxycore

import (
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"lampac-go/internal/sidecar"
)

// InstanceStats holds traffic and connection statistics for a proxy instance.
type InstanceStats struct {
	ActiveConns int64         `json:"active_conns"`
	TotalConns  int64         `json:"total_conns"`
	BytesUp     int64         `json:"bytes_up"`
	BytesDown   int64         `json:"bytes_down"`
	FailCount   int64         `json:"fail_count"`
	LastLatency time.Duration `json:"last_latency"`
	AvgLatency  time.Duration `json:"avg_latency"`
}

// Instance represents a running in-process proxy (replaces sidecar.Manager).
type Instance struct {
	id        string
	label     string
	protocol  string
	server    string // upstream proxy server address
	socksAddr string
	dialer    ProtocolDialer
	srv       *socks5Server
	startedAt time.Time

	mu      sync.Mutex
	stopped bool

	// Latency tracking (guarded by mu).
	latencyLast    time.Duration
	latencyAvg     time.Duration
	latencySamples []time.Duration
	latencyFail    int64

	// GeoIP info (guarded by mu).
	country     string
	countryCode string
	flag        string
	serverIP    string
}

// Start creates and starts a new proxy instance.
// It parses the outbound config, creates the appropriate ProtocolDialer,
// and starts a local SOCKS5 listener on 127.0.0.1:{port}.
func Start(out sidecar.ProxyOutbound, port int, id, label string) (*Instance, error) {
	dialer, err := NewDialer(out)
	if err != nil {
		return nil, fmt.Errorf("proxycore: create dialer: %w", err)
	}

	listenAddr := fmt.Sprintf("127.0.0.1:%d", port)
	srv := newSOCKS5Server(dialer)

	if err := srv.start(listenAddr); err != nil {
		dialer.Close()
		return nil, err
	}

	// Wait for port to be ready.
	if err := waitReady(listenAddr, 10*time.Second); err != nil {
		srv.stop()
		dialer.Close()
		return nil, fmt.Errorf("proxycore: port %d not ready: %w", port, err)
	}

	inst := &Instance{
		id:        id,
		label:     label,
		protocol:  out.Protocol,
		server:    fmt.Sprintf("%s:%d", out.Server, out.Port),
		socksAddr: listenAddr,
		dialer:    dialer,
		srv:       srv,
		startedAt: time.Now(),
	}

	log.Info().
		Str("id", id).
		Str("label", label).
		Str("proto", out.Protocol).
		Str("server", inst.server).
		Str("socks", listenAddr).
		Msg("proxycore: instance started")

	return inst, nil
}

// ID returns the unique instance identifier.
func (i *Instance) ID() string { return i.id }

// Label returns the human-readable name.
func (i *Instance) Label() string { return i.label }

// SOCKSAddr returns the local SOCKS5 listen address ("127.0.0.1:40000").
func (i *Instance) SOCKSAddr() string { return i.socksAddr }

// Protocol returns the outbound protocol name.
func (i *Instance) Protocol() string { return i.protocol }

// Server returns the upstream proxy server address.
func (i *Instance) Server() string { return i.server }

// Engine returns "proxycore" (satisfies the same contract as sidecar.Manager).
func (i *Instance) Engine() sidecar.EngineType { return sidecar.EngineProxyCore }

// PID returns 0 (in-process, no child process).
func (i *Instance) PID() int { return 0 }

// Alive checks if the SOCKS5 listener is accepting connections.
func (i *Instance) Alive() bool {
	i.mu.Lock()
	stopped := i.stopped
	i.mu.Unlock()
	if stopped {
		return false
	}
	return i.srv.alive()
}

// UptimeSeconds returns how long the instance has been running.
func (i *Instance) UptimeSeconds() float64 {
	return time.Since(i.startedAt).Seconds()
}

// Stats returns current traffic statistics.
func (i *Instance) Stats() InstanceStats {
	return i.srv.stats()
}

// SetGeo sets GeoIP information for this instance.
func (i *Instance) SetGeo(country, countryCode, flag, serverIP string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.country = country
	i.countryCode = countryCode
	i.flag = flag
	i.serverIP = serverIP
}

// Geo returns GeoIP information.
func (i *Instance) Geo() (country, countryCode, flag, serverIP string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.country, i.countryCode, i.flag, i.serverIP
}

// Stop gracefully shuts down the instance.
func (i *Instance) Stop() {
	i.mu.Lock()
	if i.stopped {
		i.mu.Unlock()
		return
	}
	i.stopped = true
	i.mu.Unlock()

	log.Info().Str("id", i.id).Str("label", i.label).Msg("proxycore: stopping instance")
	i.srv.stop()
	i.dialer.Close()
}

// waitReady polls a TCP address until it accepts connections or timeout.
func waitReady(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s", addr)
}
