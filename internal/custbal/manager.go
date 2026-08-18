// Package custbal manages custom balancer subprocesses.
// Each custom balancer is a standalone Go binary that listens on a TCP port
// and implements the same HTTP API as built-in balancers.
package custbal

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// ProcessState describes the lifecycle state of a custom balancer subprocess.
type ProcessState string

const (
	StateStopped  ProcessState = "stopped"
	StateStarting ProcessState = "starting"
	StateRunning  ProcessState = "running"
	StateError    ProcessState = "error"
)

// BalancerConfig describes a custom balancer and is stored as config.json.
type BalancerConfig struct {
	Name         string `json:"name"`
	DisplayName  string `json:"display_name,omitempty"`
	Port         int    `json:"port"`
	QualityBadge string `json:"quality_badge,omitempty"` // "FHD", "4K", etc.
	ContentType  string `json:"content_type,omitempty"`  // movie, serial, both
	Host         string `json:"host,omitempty"`           // upstream host override (passed as -host flag)
	AutoStart    bool   `json:"auto_start"`
}

// ProcessStatus is the JSON-serializable runtime status of a subprocess.
type ProcessStatus struct {
	Name         string       `json:"name"`
	DisplayName  string       `json:"display_name"`
	State        ProcessState `json:"state"`
	Port         int          `json:"port"`
	PID          int          `json:"pid"`
	QualityBadge string       `json:"quality_badge"`
	Uptime       float64      `json:"uptime_sec"`
	LastError    string       `json:"last_error,omitempty"`
}

// Process represents a single custom balancer subprocess.
type Process struct {
	Config    BalancerConfig
	Dir       string // directory containing main.go and binary
	MainHost  string // main server address for proxy URL construction

	mu        sync.Mutex
	cmd       *exec.Cmd
	waitDone  chan struct{}
	state     ProcessState
	startedAt time.Time
	lastError string
}

// NewProcess creates a new Process from a config and directory.
func NewProcess(cfg BalancerConfig, dir, mainHost string) *Process {
	return &Process{
		Config:   cfg,
		Dir:      dir,
		MainHost: mainHost,
		state:    StateStopped,
	}
}

// Start launches the subprocess binary.
func (p *Process) Start() error {
	p.mu.Lock()
	if p.state == StateRunning || p.state == StateStarting {
		p.mu.Unlock()
		return nil
	}

	binPath := filepath.Join(p.Dir, p.Config.Name)
	if _, err := os.Stat(binPath); os.IsNotExist(err) {
		p.state = StateError
		p.lastError = "binary not found: " + binPath
		p.mu.Unlock()
		return fmt.Errorf("custbal: %s", p.lastError)
	}

	addr := fmt.Sprintf("127.0.0.1:%d", p.Config.Port)
	args := []string{
		"-port", fmt.Sprintf("%d", p.Config.Port),
	}
	if p.MainHost != "" {
		args = append(args, "-main-host", p.MainHost)
	}
	if p.Config.Host != "" {
		args = append(args, "-host", p.Config.Host)
	}

	cmd := exec.Command(binPath, args...)
	cmd.Dir = p.Dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		p.state = StateError
		p.lastError = err.Error()
		p.mu.Unlock()
		return fmt.Errorf("custbal: start %s: %w", p.Config.Name, err)
	}

	p.state = StateStarting
	p.startedAt = time.Time{}
	p.lastError = ""
	p.cmd = cmd
	done := make(chan struct{})
	p.waitDone = done
	p.mu.Unlock()

	log.Info().Str("name", p.Config.Name).Int("pid", cmd.Process.Pid).
		Str("addr", addr).Msg("custbal: process started")

	// Monitor process readiness/exit in background.
	go func() {
		readyErr := waitReady(addr, 15*time.Second)
		if readyErr != nil {
			log.Warn().Str("name", p.Config.Name).Err(readyErr).Msg("custbal: process not ready")
			if cmd.Process != nil {
				_ = cmd.Process.Signal(os.Interrupt)
				time.AfterFunc(1200*time.Millisecond, func() {
					_ = cmd.Process.Kill()
				})
			}
		} else {
			p.mu.Lock()
			if p.cmd == cmd {
				p.state = StateRunning
				p.startedAt = time.Now()
			}
			p.mu.Unlock()
			log.Info().Str("name", p.Config.Name).Str("addr", addr).Msg("custbal: process ready")
		}

		waitErr := cmd.Wait()

		p.mu.Lock()
		if p.waitDone == done {
			close(done)
			p.waitDone = nil
		}
		if p.cmd == cmd {
			p.cmd = nil
		}
		wasStopped := p.state == StateStopped
		switch {
		case wasStopped:
			// Graceful stop requested by user.
		case readyErr != nil:
			p.state = StateError
			p.lastError = "not ready: " + readyErr.Error()
		case waitErr != nil:
			p.state = StateError
			p.lastError = waitErr.Error()
		default:
			p.state = StateStopped
		}
		p.mu.Unlock()

		if !wasStopped {
			if readyErr != nil {
				log.Warn().Str("name", p.Config.Name).Err(readyErr).Msg("custbal: process exited during startup")
			} else {
				log.Warn().Str("name", p.Config.Name).Err(waitErr).Msg("custbal: process exited unexpectedly")
			}
		}
	}()

	return nil
}

// Stop gracefully terminates the subprocess.
func (p *Process) Stop() {
	p.mu.Lock()
	cmd := p.cmd
	done := p.waitDone
	if cmd == nil || cmd.Process == nil {
		p.state = StateStopped
		p.mu.Unlock()
		return
	}
	p.state = StateStopped
	p.lastError = ""
	p.mu.Unlock()

	log.Info().Str("name", p.Config.Name).Int("pid", cmd.Process.Pid).Msg("custbal: stopping")
	_ = cmd.Process.Signal(os.Interrupt)

	if done != nil {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
			}
		}
	} else {
		// Best effort fallback for older process state.
		time.Sleep(250 * time.Millisecond)
	}

	p.mu.Lock()
	if p.cmd == cmd {
		p.cmd = nil
	}
	if p.waitDone == done {
		p.waitDone = nil
	}
	p.mu.Unlock()
}

// Restart stops and starts the subprocess.
func (p *Process) Restart() error {
	p.Stop()
	return p.Start()
}

// Alive checks whether the subprocess TCP port is responding.
func (p *Process) Alive() bool {
	addr := fmt.Sprintf("127.0.0.1:%d", p.Config.Port)
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// State returns the current process state.
func (p *Process) State() ProcessState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

// Status returns the JSON-serializable status.
func (p *Process) Status() ProcessStatus {
	p.mu.Lock()
	defer p.mu.Unlock()

	pid := 0
	if p.cmd != nil && p.cmd.Process != nil {
		pid = p.cmd.Process.Pid
	}
	uptime := 0.0
	if p.state == StateRunning {
		uptime = time.Since(p.startedAt).Seconds()
	}
	dn := p.Config.DisplayName
	if dn == "" {
		dn = p.Config.Name
	}
	return ProcessStatus{
		Name:         p.Config.Name,
		DisplayName:  dn,
		State:        p.state,
		Port:         p.Config.Port,
		PID:          pid,
		QualityBadge: p.Config.QualityBadge,
		Uptime:       uptime,
		LastError:    p.lastError,
	}
}

// Addr returns the HTTP address of the subprocess.
func (p *Process) Addr() string {
	return fmt.Sprintf("127.0.0.1:%d", p.Config.Port)
}

// LoadConfig reads config.json from the given directory.
func LoadConfig(dir string) (BalancerConfig, error) {
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return BalancerConfig{}, err
	}
	var cfg BalancerConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return BalancerConfig{}, err
	}
	return cfg, nil
}

// SaveConfig writes config.json to the given directory.
func SaveConfig(dir string, cfg BalancerConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "config.json"), data, 0644)
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
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s", addr)
}
