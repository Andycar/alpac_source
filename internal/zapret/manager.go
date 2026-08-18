package zapret

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

// Config captures the user-tunable knobs from config.toml [zapret] section.
// Defaults applied here mirror what the upstream zapret cookbook recommends
// for TSPU (Russian throttling) on TLS 1.3.
type Config struct {
	Enable bool

	// Path to the nfqws binary. If empty, we look in PATH (`nfqws`) and a
	// few well-known locations (/opt/zapret/nfq/nfqws, /usr/sbin/nfqws).
	Binary string

	// QueueNum: the NFQUEUE number nft rules feed into and nfqws listens on.
	QueueNum int

	// TCPPorts is the dport set the nft rule queues. Default [443, 80].
	TCPPorts []int

	// Strategies passed to nfqws --dpi-desync. Default "fake,split2" — fake
	// pollutes the DPI's sequence tracking and split2 splits the real
	// ClientHello. Order matters; nfqws applies them left to right.
	Strategies []string

	// DesyncTTL is the TTL for fake packets (so they die before reaching the
	// real server). Default 8 — covers most middlebox topologies.
	DesyncTTL int

	// HostlistPath is an optional file with explicit hosts (one per line).
	// Merged with DefaultHosts unless HostlistOnly is true.
	HostlistPath string
	HostlistOnly bool // when true, do NOT merge DefaultHosts

	// NFTable name. Use a distinct name so we never collide with hand-written
	// firewall rules. Default "lampac_zapret".
	NFTable string

	// Permissive: if true, a startup failure (no nft, no root, no binary,
	// nfqws crashes immediately) is logged but does not abort the lampac
	// process. Default true — prefer degraded YT over a server that won't
	// start at all.
	Permissive bool

	// ExtraArgs is appended to the nfqws command line for advanced flags
	// (--dpi-desync-fooling=md5sig,badseq, --dpi-desync-fake-tls=..., etc).
	ExtraArgs []string

	// AutoInstall: when true, Start() will install nftables + clone+make
	// bol-van/zapret automatically if `nfqws` isn't already on the host.
	// Linux + root only; on other setups falls back to permissive degrade.
	// Default true (set explicitly via Config — zero-value is false, so the
	// caller decides).
	AutoInstall bool
}

// applyDefaults fills in zero-values with sensible production defaults.
func (c *Config) applyDefaults() {
	if c.QueueNum == 0 {
		c.QueueNum = 200
	}
	if len(c.TCPPorts) == 0 {
		c.TCPPorts = []int{443, 80}
	}
	if len(c.Strategies) == 0 {
		c.Strategies = []string{"fake", "split2"}
	}
	if c.DesyncTTL == 0 {
		c.DesyncTTL = 8
	}
	if strings.TrimSpace(c.NFTable) == "" {
		c.NFTable = "lampac_zapret"
	}
}

// Manager owns the nfqws child process and the nftables rules. Lifecycle:
//
//	m := New(cfg, runtimeDir); err := m.Start(ctx); ...; m.Stop()
//
// Start is non-blocking — it spins the child supervisor in a goroutine and
// returns once the initial setup (rules + first nfqws launch) succeeded or
// permissively failed.
type Manager struct {
	cfg     Config
	runDir  string // where we drop hostlist.txt, nfqws.pid, etc.
	rules   nftRules

	mu       sync.Mutex
	cmd      *exec.Cmd
	stopped  atomic.Bool
	started  atomic.Bool
	doneCh   chan struct{}
	hostlist []string
}

// New constructs a Manager. runtimeDir is where temporary files (the
// rendered hostlist) are written; pass cfg.Compat.RepoRoot/database/zapret
// or similar.
func New(cfg Config, runtimeDir string) *Manager {
	cfg.applyDefaults()
	return &Manager{
		cfg:    cfg,
		runDir: runtimeDir,
		rules: nftRules{
			tableName: cfg.NFTable,
			queueNum:  cfg.QueueNum,
			tcpPorts:  cfg.TCPPorts,
		},
		doneCh: make(chan struct{}),
	}
}

// Start prepares the hostlist, applies nftables rules and launches nfqws as
// a supervised child. Returns nil even on partial failure when Permissive=true.
func (m *Manager) Start(ctx context.Context) error {
	if !m.cfg.Enable {
		return nil
	}
	if m.started.Load() {
		return errors.New("zapret: already started")
	}

	if err := m.prepareHostlist(); err != nil {
		return m.wrap("prepare hostlist", err)
	}

	bin, err := locateBinary(m.cfg.Binary)
	if err != nil {
		// Auto-install when allowed and not on a fully-set-up host. The
		// installer handles its own platform/permission checks; we just
		// surface its error if it can't help.
		if m.cfg.AutoInstall {
			log.Info().Msg("zapret: nfqws not found — running auto-install")
			installed, ierr := EnsureInstalled(ctx)
			if ierr != nil {
				return m.wrap("auto-install", ierr)
			}
			bin = installed
		} else {
			return m.wrap("locate nfqws", err)
		}
	}
	m.cfg.Binary = bin

	if err := m.rules.apply(ctx); err != nil {
		return m.wrap("apply nftables", err)
	}

	m.started.Store(true)
	go m.supervise(ctx)
	return nil
}

func (m *Manager) wrap(stage string, err error) error {
	if err == nil {
		return nil
	}
	if m.cfg.Permissive {
		log.Warn().Err(err).Str("stage", stage).Msg("zapret: degraded — DPI bypass disabled")
		return nil
	}
	return fmt.Errorf("zapret: %s: %w", stage, err)
}

// Stop tears down the nfqws process and removes the nftables rules. Idempotent.
func (m *Manager) Stop() {
	if !m.cfg.Enable || !m.started.Load() {
		return
	}
	if m.stopped.Swap(true) {
		return
	}
	m.mu.Lock()
	cmd := m.cmd
	m.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(os.Interrupt)
		// Hard-kill after a grace window.
		go func() {
			time.Sleep(2 * time.Second)
			_ = cmd.Process.Kill()
		}()
	}
	<-m.doneCh
	if err := m.rules.cleanup(); err != nil {
		log.Warn().Err(err).Msg("zapret: nftables cleanup failed")
	}
}

func (m *Manager) prepareHostlist() error {
	if err := os.MkdirAll(m.runDir, 0755); err != nil {
		return err
	}
	hosts, err := LoadHostlist(m.cfg.HostlistPath, !m.cfg.HostlistOnly)
	if err != nil {
		// Treat missing file as soft error if defaults are enough.
		if !errors.Is(err, os.ErrNotExist) {
			log.Warn().Err(err).Str("path", m.cfg.HostlistPath).Msg("zapret: hostlist load")
		}
	}
	if len(hosts) == 0 {
		hosts = append(hosts, DefaultHosts...)
	}
	m.hostlist = hosts
	out := filepath.Join(m.runDir, "hostlist.txt")
	return WriteHostlist(out, hosts)
}

// supervise runs nfqws and restarts it on crash with exponential backoff.
func (m *Manager) supervise(ctx context.Context) {
	defer close(m.doneCh)
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	for {
		if ctx.Err() != nil || m.stopped.Load() {
			return
		}
		args := m.buildArgs()
		log.Info().Str("bin", m.cfg.Binary).Strs("args", args).Msg("zapret: launching nfqws")

		cmd := exec.CommandContext(ctx, m.cfg.Binary, args...)
		// Pipe stderr into our log so admins can see what nfqws complains
		// about (missing CAP_NET_ADMIN, kernel module not loaded, etc).
		stderr, _ := cmd.StderrPipe()
		stdout, _ := cmd.StdoutPipe()
		go drainTo(stderr, "stderr")
		go drainTo(stdout, "stdout")

		m.mu.Lock()
		m.cmd = cmd
		m.mu.Unlock()

		startedAt := time.Now()
		if err := cmd.Start(); err != nil {
			log.Error().Err(err).Msg("zapret: nfqws start failed")
			if !m.cfg.Permissive {
				return
			}
			time.Sleep(backoff)
			backoff = nextBackoff(backoff, maxBackoff)
			continue
		}

		err := cmd.Wait()
		ranFor := time.Since(startedAt)

		m.mu.Lock()
		m.cmd = nil
		m.mu.Unlock()

		if m.stopped.Load() || ctx.Err() != nil {
			return
		}
		log.Warn().Err(err).Dur("uptime", ranFor).Msg("zapret: nfqws exited, restarting")
		// If the process died fast (probably misconfig), back off harder.
		if ranFor < 5*time.Second {
			time.Sleep(backoff)
			backoff = nextBackoff(backoff, maxBackoff)
		} else {
			backoff = time.Second
		}
	}
}

// buildArgs assembles the nfqws command line.
//
// Reference: https://github.com/bol-van/zapret/blob/master/docs/nfqws.txt
func (m *Manager) buildArgs() []string {
	hostlist := filepath.Join(m.runDir, "hostlist.txt")
	args := []string{
		fmt.Sprintf("--qnum=%d", m.cfg.QueueNum),
		fmt.Sprintf("--dpi-desync=%s", strings.Join(m.cfg.Strategies, ",")),
		fmt.Sprintf("--dpi-desync-ttl=%d", m.cfg.DesyncTTL),
		"--dpi-desync-fooling=md5sig,badseq",
		"--hostlist=" + hostlist,
	}
	if len(m.cfg.ExtraArgs) > 0 {
		args = append(args, m.cfg.ExtraArgs...)
	}
	return args
}

func drainTo(rc io.ReadCloser, stream string) {
	if rc == nil {
		return
	}
	defer rc.Close()
	buf := make([]byte, 2048)
	for {
		n, err := rc.Read(buf)
		if n > 0 {
			log.Info().Str("stream", stream).Msg("zapret/nfqws: " + strings.TrimSpace(string(buf[:n])))
		}
		if err != nil {
			return
		}
	}
}

func nextBackoff(cur, max time.Duration) time.Duration {
	next := cur * 2
	if next > max {
		next = max
	}
	return next
}

// locateBinary picks an nfqws path: explicit override → PATH → known locations.
func locateBinary(override string) (string, error) {
	if override = strings.TrimSpace(override); override != "" {
		if _, err := os.Stat(override); err == nil {
			return override, nil
		}
		return "", fmt.Errorf("nfqws binary %q not found", override)
	}
	if p, err := exec.LookPath("nfqws"); err == nil {
		return p, nil
	}
	candidates := []string{
		"/opt/zapret/nfq/nfqws",
		"/usr/sbin/nfqws",
		"/usr/local/sbin/nfqws",
		"/usr/bin/nfqws",
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", errors.New("nfqws binary not found (install zapret or set zapret.binary)")
}
