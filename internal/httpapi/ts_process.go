package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"

	"github.com/rs/zerolog/log"
)

// tsProcessManager manages the TorrServer binary lifecycle.
// If the configured TorrServer is local (no external URL) and the binary
// exists on disk, the manager will auto-start it and keep it running.
type tsProcessManager struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	cancel  context.CancelFunc
	cfg     config.Config
	logPath string // path to torrserver.log
	running bool
}

var tsManager *tsProcessManager

// initTorrServerProcess checks whether TorrServer needs to be managed
// by lampac-go.  Called once from StartServer after config is loaded.
//
// Conditions to auto-manage:
//   - TorrServer.URL is empty (local mode, not remote)
//   - TorrServer.Port > 0
//   - The binary exists on disk
//   - TorrServer is NOT already responding (e.g., started by entrypoint)
func initTorrServerProcess(cfg config.Config) {
	if cfg.TorrServer.URL != "" {
		log.Debug().Str("url", cfg.TorrServer.URL).Msg("ts-proc: external TorrServer configured, skipping process management")
		return
	}
	port := cfg.TorrServer.Port
	if port <= 0 {
		port = 9080
	}

	// Skip if in-process torrent server is active (torrs build tag).
	if torrsIsInProcess() {
		log.Debug().Msg("ts-proc: in-process torrent server active, skipping external process")
		return
	}

	bin := torrserverBinaryPath(cfg)
	if bin == "" {
		log.Debug().Msg("ts-proc: TorrServer binary not found, skipping process management")
		return
	}

	// Already responding? (started by entrypoint or systemd)
	if isTorrServerAlive(port) {
		log.Info().Int("port", port).Msg("ts-proc: TorrServer already running, skipping auto-start")
		return
	}

	// Start it
	tsManager = &tsProcessManager{cfg: cfg}
	tsManager.start()

	// Monitor goroutine: restart if it crashes
	go tsManager.monitor()
}

// torrserverBinaryPath finds the TorrServer binary on disk.
func torrserverBinaryPath(cfg config.Config) string {
	homeDir := cfg.TorrServer.HomeDir
	if homeDir == "" {
		homeDir = filepath.Join(cfg.Compat.RepoRoot, "torrserver")
	}

	suffix := "TorrServer-linux"
	if runtime.GOOS == "darwin" {
		suffix = "TorrServer-darwin"
	}

	path := filepath.Join(homeDir, suffix)
	if fi, err := os.Stat(path); err == nil && !fi.IsDir() {
		return path
	}
	return ""
}

// isTorrServerAlive checks if TorrServer responds on the given port.
func isTorrServerAlive(port int) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/echo", port))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (m *tsProcessManager) start() {
	m.mu.Lock()
	defer m.mu.Unlock()

	port := m.cfg.TorrServer.Port
	if port <= 0 {
		port = 9080
	}

	bin := torrserverBinaryPath(m.cfg)
	if bin == "" {
		return
	}

	homeDir := m.cfg.TorrServer.HomeDir
	if homeDir == "" {
		homeDir = filepath.Join(m.cfg.Compat.RepoRoot, "torrserver")
	}
	accsPath := filepath.Join(homeDir, "accs.db")

	args := []string{
		"--port", fmt.Sprintf("%d", port),
		"--path", homeDir,
	}
	if _, err := os.Stat(accsPath); err == nil {
		args = append(args, "--accs", accsPath)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = homeDir

	// Log file for TorrServer stdout/stderr
	m.logPath = filepath.Join(homeDir, "torrserver.log")
	logFile, err := os.OpenFile(m.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Warn().Err(err).Msg("ts-proc: cannot open log file, using /dev/null")
	} else {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}

	if err := cmd.Start(); err != nil {
		log.Error().Err(err).Str("bin", bin).Msg("ts-proc: failed to start TorrServer")
		cancel()
		return
	}

	m.cmd = cmd
	m.cancel = cancel
	m.running = true

	log.Info().
		Int("pid", cmd.Process.Pid).
		Int("port", port).
		Str("bin", bin).
		Msg("ts-proc: TorrServer started by lampac-go")

	// Wait for it to become healthy (up to 5s)
	go func() {
		for range 10 {
			time.Sleep(500 * time.Millisecond)
			if isTorrServerAlive(port) {
				log.Info().Int("port", port).Msg("ts-proc: ✓ TorrServer is healthy")
				return
			}
		}
		log.Warn().Int("port", port).Msg("ts-proc: TorrServer started but not responding after 5s (check torrserver.log)")
	}()
}

func (m *tsProcessManager) monitor() {
	// Exponential backoff so a crash-loop (bad args, port conflict) doesn't
	// burn CPU.  Reset to base on any process that survives >2 min.
	const (
		backoffBase = 3 * time.Second
		backoffCap  = 60 * time.Second
		runOK       = 2 * time.Minute
	)
	backoff := backoffBase

	for {
		m.mu.Lock()
		cmd := m.cmd
		m.mu.Unlock()

		if cmd == nil || cmd.Process == nil {
			return
		}

		startedAt := time.Now()
		err := cmd.Wait()
		ran := time.Since(startedAt)

		m.mu.Lock()
		m.running = false
		m.mu.Unlock()

		if err != nil {
			log.Warn().Err(err).Dur("ran", ran).Msg("ts-proc: TorrServer exited unexpectedly")
		} else {
			log.Info().Dur("ran", ran).Msg("ts-proc: TorrServer exited normally")
		}

		// Restart after a delay (unless context was cancelled = shutdown)
		m.mu.Lock()
		cancelled := m.cancel == nil
		m.mu.Unlock()
		if cancelled {
			return
		}

		// Reset backoff if the process ran long enough to be healthy.
		if ran >= runOK {
			backoff = backoffBase
		}

		log.Info().Dur("delay", backoff).Msg("ts-proc: restarting TorrServer")
		time.Sleep(backoff)

		// Check if it's already running (e.g., user started it manually)
		port := m.cfg.TorrServer.Port
		if port <= 0 {
			port = 9080
		}
		if isTorrServerAlive(port) {
			log.Info().Msg("ts-proc: TorrServer already running (external), stopping monitor")
			return
		}

		m.start()

		// Grow backoff for the next failure.
		backoff *= 2
		if backoff > backoffCap {
			backoff = backoffCap
		}
	}
}

// stopTorrServerProcess gracefully stops the managed TorrServer process.
// Sends SIGINT, waits up to 5 s for clean exit (TorrServer needs that long to
// flush its bolt DB), then SIGKILLs.  Cancelling the context first tells the
// monitor goroutine not to restart.
func stopTorrServerProcess() {
	if tsManager == nil {
		return
	}
	tsManager.mu.Lock()

	if tsManager.cancel != nil {
		tsManager.cancel()
		tsManager.cancel = nil
	}
	cmd := tsManager.cmd
	tsManager.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return
	}

	_ = cmd.Process.Signal(os.Interrupt)

	// Wait up to 5 s for the process to flush state and exit.
	done := make(chan struct{}, 1)
	go func() {
		_ = cmd.Wait()
		done <- struct{}{}
	}()
	select {
	case <-done:
		log.Info().Msg("ts-proc: TorrServer exited cleanly")
	case <-time.After(5 * time.Second):
		log.Warn().Msg("ts-proc: TorrServer didn't exit in 5s, killing")
		_ = cmd.Process.Kill()
	}

	tsManager.mu.Lock()
	tsManager.running = false
	tsManager.mu.Unlock()
}

// torrServerProcessStatus returns status info for admin panel diagnostics.
func torrServerProcessStatus() map[string]any {
	result := map[string]any{
		"managed": tsManager != nil,
	}
	if tsManager == nil {
		return result
	}
	tsManager.mu.Lock()
	defer tsManager.mu.Unlock()

	result["running"] = tsManager.running
	result["log_path"] = tsManager.logPath
	if tsManager.cmd != nil && tsManager.cmd.Process != nil {
		result["pid"] = tsManager.cmd.Process.Pid
	}

	// Read last 30 lines of log
	if tsManager.logPath != "" {
		if data, err := os.ReadFile(tsManager.logPath); err == nil {
			lines := strings.Split(string(data), "\n")
			if len(lines) > 30 {
				lines = lines[len(lines)-30:]
			}
			result["recent_log"] = strings.Join(lines, "\n")
		}
	}
	return result
}
