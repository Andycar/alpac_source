// Package jacred manages a local jacred-fdb (https://github.com/jacred-fdb/jacred)
// instance: downloads the release binary, bootstraps the FDB torrent database,
// generates init.conf, supervises the child process and replaces the host
// crontab (jacred has no built-in scheduler — parsing is triggered over HTTP).
//
// lampac-go proxies /api/v2.0/indexers/* and /api/v1.0/* to this instance when
// [parser] jacred_local = true, so Lampa clients keep talking to lampac only.
package jacred

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

// Config captures [parser] jacred_local_* knobs from config.toml.
type Config struct {
	Enable bool

	// HomeDir is where the binary, init.conf and Data/ live.
	// Default {RepoRoot}/jacred.
	HomeDir string

	// Port the local instance listens on (127.0.0.1 only). Default 9117.
	Port int

	// APIKey mirrors Parser.JacRedKey — written into init.conf so the proxy's
	// injected ?apikey= keeps working against the local instance.
	APIKey string

	// SyncAPI: upstream jacred with opensync=true to pull the database from.
	// When set, own tracker parsing (the cron loop) is disabled — the instance
	// only syncs. When empty, jacred parses trackers itself on our schedule.
	SyncAPI string

	// BootstrapDB: download the prebuilt FDB archive on first start so search
	// works immediately instead of waiting days for own parsing. Default true.
	BootstrapDB bool

	// DBArchiveURL overrides the bootstrap archive location.
	DBArchiveURL string
}

func (c *Config) applyDefaults() {
	if c.Port == 0 {
		c.Port = 9117
	}
	if strings.TrimSpace(c.DBArchiveURL) == "" {
		c.DBArchiveURL = defaultDBArchiveURL
	}
}

const defaultDBArchiveURL = "https://sync.jacred.stream/latest.tar.zst.zip"

// Manager owns the local jacred child process. Lifecycle mirrors
// zapret.Manager: New → Start (non-blocking) → Stop. Start is permissive —
// install/bootstrap failures are logged, never fatal for lampac itself.
type Manager struct {
	cfg Config

	mu           sync.Mutex
	cmd          *exec.Cmd
	stopped      atomic.Bool
	started      atomic.Bool
	healthy      atomic.Bool
	portConflict atomic.Bool // a foreign process is holding our port (see Status.PortConflict)
	doneCh       chan struct{}
	cancel       context.CancelFunc

	// install/bootstrap progress for the admin panel
	stateMu sync.Mutex
	state   Status
}

// Status is a snapshot for the admin deps panel.
type Status struct {
	Installed bool   `json:"installed"`
	Version   string `json:"version"` // release tag recorded at install time
	Running   bool   `json:"running"`
	Healthy   bool   `json:"healthy"`
	Stage     string `json:"stage"` // installing / bootstrapping-db / running / stopped / error
	Error     string `json:"error,omitempty"`
	DBSizeMB  int64  `json:"db_size_mb"`
	// PortConflict is set when our managed child cannot hold its port because a
	// FOREIGN process (commonly a pre-existing external jac.red on the same host)
	// is already answering there. In that state Version/DBSizeMB below still read
	// from OUR on-disk install, but the process actually serving the port — and
	// the /jacred/ web UI — is the other one, so it may 404. Surfaced so the admin
	// panel stops reporting the stale process as "working".
	PortConflict bool `json:"port_conflict,omitempty"`
}

func New(cfg Config) *Manager {
	cfg.applyDefaults()
	return &Manager{cfg: cfg, doneCh: make(chan struct{})}
}

// BaseURL returns the local instance origin.
func (m *Manager) BaseURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", m.cfg.Port)
}

// Healthy reports whether the last probe of the child succeeded.
func (m *Manager) Healthy() bool { return m.healthy.Load() }

// PortConflict reports whether a foreign process is holding our port (our child
// can't bind). See Status.PortConflict.
func (m *Manager) PortConflict() bool { return m.portConflict.Load() }

func (m *Manager) setStage(stage, errMsg string) {
	m.stateMu.Lock()
	m.state.Stage = stage
	m.state.Error = errMsg
	m.stateMu.Unlock()
}

// Snapshot returns the current status for the admin panel.
func (m *Manager) Snapshot() Status {
	m.stateMu.Lock()
	st := m.state
	m.stateMu.Unlock()
	st.Running = m.started.Load() && !m.stopped.Load()
	st.Healthy = m.healthy.Load()
	st.PortConflict = m.portConflict.Load()
	if _, err := binaryPath(m.cfg.HomeDir); err == nil {
		st.Installed = true
		st.Version = installedVersion(m.cfg.HomeDir)
	}
	st.DBSizeMB = dirSizeMB(filepath.Join(m.cfg.HomeDir, "Data"))
	return st
}

// Inspect reports on-disk install state for homeDir without a live Manager
// (used by the admin panel when the local parser is disabled).
func Inspect(homeDir string) Status {
	st := Status{Stage: "stopped"}
	if _, err := binaryPath(homeDir); err == nil {
		st.Installed = true
		st.Version = installedVersion(homeDir)
	}
	st.DBSizeMB = dirSizeMB(filepath.Join(homeDir, "Data"))
	return st
}

// Start installs (if needed), bootstraps the DB, writes init.conf and spins
// the supervisor + scheduler goroutines. Non-blocking: heavy work (release
// download ~50 MB, DB archive potentially GBs) runs in the background so
// lampac boot is never delayed. Always permissive.
func (m *Manager) Start(ctx context.Context) error {
	if !m.cfg.Enable {
		return nil
	}
	if m.started.Swap(true) {
		return errors.New("jacred: already started")
	}
	ctx, m.cancel = context.WithCancel(ctx)

	go func() {
		defer close(m.doneCh)

		if _, err := binaryPath(m.cfg.HomeDir); err != nil {
			m.setStage("installing", "")
			if err := Install(ctx, m.cfg.HomeDir); err != nil {
				log.Error().Err(err).Msg("jacred: install failed — local parser disabled")
				m.setStage("error", "install: "+err.Error())
				return
			}
		}

		if m.cfg.BootstrapDB {
			m.setStage("bootstrapping-db", "")
			if err := BootstrapDB(ctx, m.cfg.HomeDir, m.cfg.DBArchiveURL); err != nil {
				// Non-fatal: jacred still works, base fills via parsing/sync.
				log.Warn().Err(err).Msg("jacred: DB bootstrap failed — starting with empty/partial base")
			}
		}

		if err := ensureInitConf(m.cfg); err != nil {
			log.Error().Err(err).Msg("jacred: init.conf write failed — local parser disabled")
			m.setStage("error", "init.conf: "+err.Error())
			return
		}

		m.setStage("running", "")
		go m.healthLoop(ctx)
		if strings.TrimSpace(m.cfg.SyncAPI) == "" {
			go m.cronLoop(ctx)
		}
		m.supervise(ctx)
	}()
	return nil
}

// Stop terminates the child process. Idempotent.
func (m *Manager) Stop() {
	if !m.cfg.Enable || !m.started.Load() {
		return
	}
	if m.stopped.Swap(true) {
		return
	}
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Lock()
	cmd := m.cmd
	m.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(os.Interrupt)
		go func() {
			time.Sleep(3 * time.Second)
			_ = cmd.Process.Kill()
		}()
	}
	select {
	case <-m.doneCh:
	case <-time.After(5 * time.Second):
	}
	m.setStage("stopped", "")
}

// RestartChild politely kills the current child so the supervisor relaunches
// it — used after a binary update to pick up the new release.
func (m *Manager) RestartChild() {
	m.mu.Lock()
	cmd := m.cmd
	m.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(os.Interrupt)
	}
}

// supervise runs the JacRed binary and restarts it on crash with backoff.
func (m *Manager) supervise(ctx context.Context) {
	backoff := time.Second
	const maxBackoff = 60 * time.Second
	for {
		if ctx.Err() != nil || m.stopped.Load() {
			return
		}
		bin, err := binaryPath(m.cfg.HomeDir)
		if err != nil {
			log.Error().Err(err).Msg("jacred: binary vanished, supervisor exiting")
			return
		}

		log.Info().Str("bin", bin).Int("port", m.cfg.Port).Msg("jacred: launching local instance")
		cmd := exec.CommandContext(ctx, bin)
		cmd.Dir = m.cfg.HomeDir
		stderr, _ := cmd.StderrPipe()
		stdout, _ := cmd.StdoutPipe()
		go drainTo(stderr, "stderr")
		go drainTo(stdout, "stdout")

		m.mu.Lock()
		m.cmd = cmd
		m.mu.Unlock()

		startedAt := time.Now()
		if err := cmd.Start(); err != nil {
			log.Error().Err(err).Msg("jacred: start failed")
			m.healthy.Store(false)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = nextBackoff(backoff, maxBackoff)
			continue
		}

		err = cmd.Wait()
		ranFor := time.Since(startedAt)
		m.healthy.Store(false)

		m.mu.Lock()
		m.cmd = nil
		m.mu.Unlock()

		if m.stopped.Load() || ctx.Err() != nil {
			return
		}
		if ranFor < 10*time.Second {
			// Our child died almost immediately. If SOMETHING still answers on the
			// port (our child is now dead, so it isn't us), a foreign process — most
			// often a pre-existing external jac.red on the same host — is holding the
			// port and our child can never bind. Flag it so the admin panel reports
			// the real state instead of the foreigner as "working".
			probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			code, answered := m.probeConf(probeCtx, &http.Client{Timeout: 5 * time.Second})
			cancel()
			if answered {
				m.portConflict.Store(true)
				m.healthy.Store(false)
				msg := fmt.Sprintf("port %d is already in use by another process (an external jac.red on this host?) — the local parser can't start. Stop that process, or set [parser] jacred_local_port to a free port.", m.cfg.Port)
				m.setStage("error", msg)
				log.Error().Int("port", m.cfg.Port).Int("foreign_status", code).Msg("jacred: " + msg)
			} else {
				log.Warn().Err(err).Dur("uptime", ranFor).Msg("jacred: process exited, restarting")
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = nextBackoff(backoff, maxBackoff)
		} else {
			// A long, healthy run held the port — no conflict this cycle; clear any
			// stale flag + error stage (belt-and-suspenders; healthLoop usually did).
			if m.portConflict.Swap(false) {
				m.setStage("running", "")
			}
			log.Warn().Err(err).Dur("uptime", ranFor).Msg("jacred: process exited, restarting")
			backoff = time.Second
		}
	}
}

// probeConf GETs /api/v1.0/conf on the local port and returns the HTTP status
// code. ok=false means nothing answered (connection refused / timeout). A
// response — of ANY status — means SOMETHING is listening on the port, which is
// how we detect a foreign holder when our own child is down.
func (m *Manager) probeConf(ctx context.Context, client *http.Client) (code int, ok bool) {
	url := m.BaseURL() + "/api/v1.0/conf"
	if m.cfg.APIKey != "" {
		url += "?apikey=" + m.cfg.APIKey
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, false
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, true
}

// healthyFromProbe decides whether a probe status code means a working jacred
// API is on the port. A 404 means the endpoint is absent — the process there is
// not a functioning jacred (wrong version, or a foreign process) — so it is NOT
// healthy, even though the old `< 500` check treated it as up. Auth challenges
// (401/403) still indicate a live jacred, so they count as up.
func healthyFromProbe(code int) bool {
	if code == http.StatusNotFound {
		return false
	}
	return code > 0 && code < 500
}

// childAlive reports whether our supervised child process is currently running.
func (m *Manager) childAlive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cmd != nil && m.cmd.Process != nil
}

// healthLoop probes /api/v1.0/conf so the proxy knows when to prefer the
// local instance vs fall back to the external upstream. Health is reported only
// when a working jacred API answers AND no foreign process is shadowing our
// port — so a stale external jac.red can't make the panel show a false "working".
func (m *Manager) healthLoop(ctx context.Context) {
	client := &http.Client{Timeout: 5 * time.Second}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		was := m.healthy.Load()
		code, ok := m.probeConf(ctx, client)
		up := ok && healthyFromProbe(code)
		// If our own child is alive AND the API answers, the port is genuinely
		// ours — clear any stale foreign-conflict flag (the other process left)
		// and restore the "running" stage so the panel drops the error message.
		if up && m.childAlive() {
			if m.portConflict.Swap(false) {
				m.setStage("running", "")
			}
		}
		now := up && !m.portConflict.Load()
		m.healthy.Store(now)
		if now != was {
			log.Info().Bool("healthy", now).Msg("jacred: local instance health changed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func drainTo(rc io.ReadCloser, stream string) {
	if rc == nil {
		return
	}
	defer rc.Close()
	buf := make([]byte, 4096)
	for {
		n, err := rc.Read(buf)
		if n > 0 {
			msg := strings.TrimSpace(string(buf[:n]))
			if msg != "" {
				log.Debug().Str("stream", stream).Msg("jacred: " + msg)
			}
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

// binaryPath locates the JacRed executable inside homeDir. Release zips for
// linux/windows put a single file at the root; the osx zips ship a directory.
func binaryPath(homeDir string) (string, error) {
	name := "JacRed"
	if runtime.GOOS == "windows" {
		name = "JacRed.exe"
	}
	direct := filepath.Join(homeDir, name)
	if st, err := os.Stat(direct); err == nil && !st.IsDir() {
		return direct, nil
	}
	// osx zips may nest the payload one level down.
	entries, err := os.ReadDir(homeDir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		nested := filepath.Join(homeDir, e.Name(), name)
		if st, err := os.Stat(nested); err == nil && !st.IsDir() {
			return nested, nil
		}
	}
	return "", fmt.Errorf("jacred binary not found in %s", homeDir)
}

func dirSizeMB(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total / (1024 * 1024)
}
