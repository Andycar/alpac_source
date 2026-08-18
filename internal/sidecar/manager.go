package sidecar

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// Manager controls a proxy sidecar process (xray or mihomo).
type Manager struct {
	cmd       *exec.Cmd
	socksAddr string
	cfgPath   string    // config file path
	startedAt time.Time
	engine    EngineType // which engine runs this process

	stderr  *ringBuffer   // last few KiB of the process's stderr (for diagnostics)
	exited  chan struct{} // closed when the process exits
	waitErr error         // result of cmd.Wait (valid once exited is closed)
}

// ringBuffer is a thread-safe fixed-size tail buffer. It keeps only the most
// recent bytes written to it, so we can surface a sidecar's last stderr output
// (e.g. xray's fatal config error) without unbounded memory growth.
type ringBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func newRingBuffer(max int) *ringBuffer { return &ringBuffer{max: max} }

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.max {
		r.buf = r.buf[len(r.buf)-r.max:]
	}
	return len(p), nil
}

func (r *ringBuffer) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.TrimSpace(string(r.buf))
}

// lastLine returns the last non-empty line of s, capped at 300 chars. Proxy
// engines print the fatal reason on (or near) the final line, so this keeps the
// signal while dropping the startup banner noise.
func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[i+1:])
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// StartSidecar parses a proxy URI, ensures the engine binary exists,
// generates a config, and starts the sidecar process.
// engineOverride can be "" for auto-detection.
func StartSidecar(proxyURI string, engineOverride string, binDir string, port int, engines map[EngineType]Engine, parseURI func(string) (ProxyOutbound, EngineType, error)) (*Manager, error) {
	if port <= 0 {
		port = 40000
	}
	if binDir == "" {
		binDir = "."
	}

	// Parse the proxy URI.
	outbound, detectedEngine, err := parseURI(proxyURI)
	if err != nil {
		return nil, fmt.Errorf("sidecar: invalid proxy URI: %w", err)
	}

	// Special case: wg://warp — auto-register with Cloudflare WARP.
	if outbound.Protocol == "wireguard" && outbound.Server == "warp" {
		warpCfg, werr := RegisterOrLoadWARP(binDir)
		if werr != nil {
			return nil, fmt.Errorf("sidecar: WARP registration: %w", werr)
		}
		outbound = WARPToOutbound(warpCfg)
		detectedEngine = EngineMihomo // WARP always via mihomo
	}

	// Resolve engine (explicit override > auto-detected).
	engineType := detectedEngine
	if engineOverride != "" {
		engineType = EngineType(engineOverride)
	}

	engine, ok := engines[engineType]
	if !ok {
		return nil, fmt.Errorf("sidecar: unknown engine %q", engineType)
	}

	// Ensure binary exists.
	binPath, err := engine.EnsureBinary(binDir)
	if err != nil {
		return nil, fmt.Errorf("sidecar: ensure %s binary: %w", engineType, err)
	}

	// Generate config.
	cfgData, ext, err := engine.GenerateConfig(outbound, port)
	if err != nil {
		return nil, fmt.Errorf("sidecar: generate %s config: %w", engineType, err)
	}

	// Write config file.
	cfgPath := filepath.Join(binDir, fmt.Sprintf("sidecar-%d.%s", port, ext))
	if err := os.WriteFile(cfgPath, cfgData, 0644); err != nil {
		return nil, fmt.Errorf("sidecar: write config: %w", err)
	}

	socksAddr := fmt.Sprintf("127.0.0.1:%d", port)

	// Start process. Capture stderr (bounded) so that, when the engine fails to
	// start, we can report *why* (bad config, port already in use, crash) instead
	// of a blind "timeout waiting for 127.0.0.1:PORT".
	stderr := newRingBuffer(8 << 10) // last 8 KiB
	args := engine.StartArgs(binPath, cfgPath)
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout = io.Discard
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("sidecar: start %s: %w", engineType, err)
	}

	log.Info().
		Int("pid", cmd.Process.Pid).
		Str("engine", string(engineType)).
		Str("socks", socksAddr).
		Msg("sidecar: process started")

	m := &Manager{
		cmd:       cmd,
		socksAddr: socksAddr,
		cfgPath:   cfgPath,
		startedAt: time.Now(),
		engine:    engineType,
		stderr:    stderr,
		exited:    make(chan struct{}),
	}

	// Single reaper goroutine owns cmd.Wait; both the readiness check and Stop()
	// observe the process exit via m.exited (calling Wait twice would error).
	go func() {
		m.waitErr = m.cmd.Wait()
		close(m.exited)
	}()

	// Wait for the SOCKS5 port to become available, bailing out early if the
	// process dies first.
	if err := m.waitReady(10 * time.Second); err != nil {
		m.Stop()
		return nil, fmt.Errorf("sidecar: %s not ready: %w", engineType, err)
	}

	log.Info().
		Str("engine", string(engineType)).
		Str("socks", socksAddr).
		Msg("sidecar: SOCKS5 proxy ready")

	return m, nil
}

// waitReady polls the SOCKS5 port until it accepts connections, the process
// exits, or the timeout elapses. If the process exits before the port opens it
// returns immediately with the captured stderr tail (the real failure reason),
// rather than blocking for the whole timeout.
func (m *Manager) waitReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.DialTimeout("tcp", m.socksAddr, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}

		select {
		case <-m.exited:
			if tail := lastLine(m.stderr.String()); tail != "" {
				return fmt.Errorf("process exited before opening %s: %s", m.socksAddr, tail)
			}
			if m.waitErr != nil {
				return fmt.Errorf("process exited before opening %s: %v", m.socksAddr, m.waitErr)
			}
			return fmt.Errorf("process exited before opening %s", m.socksAddr)
		default:
		}

		if !time.Now().Before(deadline) {
			if tail := lastLine(m.stderr.String()); tail != "" {
				return fmt.Errorf("timeout waiting for %s (stderr: %s)", m.socksAddr, tail)
			}
			return fmt.Errorf("timeout waiting for %s", m.socksAddr)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// SOCKSAddr returns the SOCKS5 proxy address (e.g. "127.0.0.1:40000").
func (m *Manager) SOCKSAddr() string { return m.socksAddr }

// Engine returns the engine type running this sidecar.
func (m *Manager) Engine() EngineType { return m.engine }

// PID returns the process PID, or 0 if not running.
func (m *Manager) PID() int {
	if m.cmd != nil && m.cmd.Process != nil {
		return m.cmd.Process.Pid
	}
	return 0
}

// Alive checks whether the SOCKS5 proxy is responding.
func (m *Manager) Alive() bool {
	if m.socksAddr == "" {
		return false
	}
	conn, err := net.DialTimeout("tcp", m.socksAddr, 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// UptimeSeconds returns how long the process has been running.
func (m *Manager) UptimeSeconds() float64 {
	return time.Since(m.startedAt).Seconds()
}

// Stop gracefully terminates the sidecar process.
func (m *Manager) Stop() {
	if m.cmd == nil || m.cmd.Process == nil {
		return
	}
	log.Info().Int("pid", m.cmd.Process.Pid).Str("engine", string(m.engine)).Msg("sidecar: stopping")
	_ = m.cmd.Process.Signal(os.Interrupt)

	// The reaper goroutine started in StartSidecar owns cmd.Wait; observe the
	// exit via m.exited. (Guard against a Manager built without it.)
	if m.exited == nil {
		_ = m.cmd.Process.Kill()
		return
	}
	select {
	case <-m.exited:
	case <-time.After(5 * time.Second):
		_ = m.cmd.Process.Kill()
		<-m.exited
	}
}
