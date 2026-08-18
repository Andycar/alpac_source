package custbal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/rs/zerolog/log"
)

var balancerNameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func normalizeAndValidateName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !balancerNameRE.MatchString(name) {
		return "", fmt.Errorf("custbal: invalid balancer name %q", name)
	}
	return name, nil
}

// Pool manages all custom balancer subprocesses.
type Pool struct {
	mu        sync.RWMutex
	processes map[string]*Process
	basePort  int
	baseDir   string
	mainHost  string // main server address (e.g., "http://127.0.0.1:888")
}

// NewPool creates a pool rooted at baseDir.
// basePort is the starting TCP port for subprocesses (e.g., 50000).
func NewPool(baseDir string, basePort int, mainHost string) *Pool {
	return &Pool{
		processes: make(map[string]*Process),
		basePort:  basePort,
		baseDir:   baseDir,
		mainHost:  mainHost,
	}
}

// BaseDir returns the root directory for custom balancers.
func (p *Pool) BaseDir() string { return p.baseDir }

// ScanAndStart scans baseDir for subdirectories with config.json
// and starts those marked with auto_start=true.
func (p *Pool) ScanAndStart() error {
	entries, err := os.ReadDir(p.baseDir)
	if os.IsNotExist(err) {
		return nil // no custom_balancers dir yet
	}
	if err != nil {
		return fmt.Errorf("custbal pool: scan %s: %w", p.baseDir, err)
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(p.baseDir, e.Name())
		cfg, err := LoadConfig(dir)
		if err != nil {
			log.Debug().Str("dir", e.Name()).Err(err).Msg("custbal pool: skip (no config)")
			continue
		}
		if cfg.Name == "" {
			cfg.Name = e.Name()
		}
		name, err := normalizeAndValidateName(cfg.Name)
		if err != nil {
			log.Warn().Str("name", cfg.Name).Msg("custbal pool: skip invalid balancer name")
			continue
		}
		cfg.Name = name
		if cfg.Port == 0 {
			cfg.Port = p.allocPort()
			// Save back so port is persistent.
			_ = SaveConfig(dir, cfg)
		}

		proc := NewProcess(cfg, dir, p.mainHost)
		p.mu.Lock()
		p.processes[cfg.Name] = proc
		p.mu.Unlock()

		if cfg.AutoStart {
			if err := proc.Start(); err != nil {
				log.Warn().Str("name", cfg.Name).Err(err).Msg("custbal pool: auto-start failed")
			}
		}
	}
	return nil
}

// Deploy writes main.go and config.json, compiles the binary, and starts it.
func (p *Pool) Deploy(name, goCode string, cfg BalancerConfig) (string, error) {
	safeName, err := normalizeAndValidateName(name)
	if err != nil {
		return "", err
	}
	if cfg.Name == "" {
		cfg.Name = safeName
	} else {
		cfgName, err := normalizeAndValidateName(cfg.Name)
		if err != nil {
			return "", err
		}
		cfg.Name = cfgName
	}
	if cfg.Port == 0 {
		cfg.Port = p.allocPort()
	}
	if cfg.AutoStart {
		// Default: auto-start after deploy.
	}

	dir := filepath.Join(p.baseDir, safeName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("custbal deploy: mkdir: %w", err)
	}

	// Write main.go
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(goCode), 0644); err != nil {
		return "", fmt.Errorf("custbal deploy: write main.go: %w", err)
	}

	// Write config.json
	if err := SaveConfig(dir, cfg); err != nil {
		return "", fmt.Errorf("custbal deploy: write config: %w", err)
	}

	// Write go.mod for standalone binary
	goMod := fmt.Sprintf("module custbal-%s\n\ngo 1.22\n", safeName)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0644); err != nil {
		return "", fmt.Errorf("custbal deploy: write go.mod: %w", err)
	}

	// Compile
	output, err := Compile(dir, safeName)
	if err != nil {
		return output, fmt.Errorf("custbal deploy: compile: %w", err)
	}

	// Stop existing process if any.
	p.mu.Lock()
	if old, ok := p.processes[safeName]; ok {
		old.Stop()
	}
	proc := NewProcess(cfg, dir, p.mainHost)
	p.processes[safeName] = proc
	p.mu.Unlock()

	// Start
	if err := proc.Start(); err != nil {
		return output, fmt.Errorf("custbal deploy: start: %w", err)
	}

	return output, nil
}

// Start starts a specific balancer by name.
func (p *Pool) Start(name string) error {
	safeName, err := normalizeAndValidateName(name)
	if err != nil {
		return err
	}
	p.mu.RLock()
	proc, ok := p.processes[safeName]
	p.mu.RUnlock()
	if !ok {
		return fmt.Errorf("custbal: unknown balancer %q", safeName)
	}
	return proc.Start()
}

// Stop stops a specific balancer by name.
func (p *Pool) Stop(name string) error {
	safeName, err := normalizeAndValidateName(name)
	if err != nil {
		return err
	}
	p.mu.RLock()
	proc, ok := p.processes[safeName]
	p.mu.RUnlock()
	if !ok {
		return fmt.Errorf("custbal: unknown balancer %q", safeName)
	}
	proc.Stop()
	return nil
}

// Restart stops and starts a specific balancer.
func (p *Pool) Restart(name string) error {
	safeName, err := normalizeAndValidateName(name)
	if err != nil {
		return err
	}
	p.mu.RLock()
	proc, ok := p.processes[safeName]
	p.mu.RUnlock()
	if !ok {
		return fmt.Errorf("custbal: unknown balancer %q", safeName)
	}
	return proc.Restart()
}

// Recompile recompiles and restarts a balancer.
func (p *Pool) Recompile(name string) (string, error) {
	safeName, err := normalizeAndValidateName(name)
	if err != nil {
		return "", err
	}
	p.mu.RLock()
	proc, ok := p.processes[safeName]
	p.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("custbal: unknown balancer %q", safeName)
	}

	proc.Stop()
	output, err := Compile(proc.Dir, safeName)
	if err != nil {
		return output, err
	}
	return output, proc.Start()
}

// Remove stops and removes a custom balancer directory.
func (p *Pool) Remove(name string) error {
	safeName, err := normalizeAndValidateName(name)
	if err != nil {
		return err
	}
	p.mu.Lock()
	proc, ok := p.processes[safeName]
	if ok {
		proc.Stop()
		delete(p.processes, safeName)
	}
	p.mu.Unlock()

	dir := filepath.Join(p.baseDir, safeName)
	return os.RemoveAll(dir)
}

// List returns the status of all custom balancers.
func (p *Pool) List() []ProcessStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()

	out := make([]ProcessStatus, 0, len(p.processes))
	for _, proc := range p.processes {
		out = append(out, proc.Status())
	}
	return out
}

// RouteMap returns a map of balancer names to their HTTP addresses.
// Only includes running processes.
func (p *Pool) RouteMap() map[string]string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	m := make(map[string]string)
	for name, proc := range p.processes {
		if proc.State() == StateRunning || proc.State() == StateStarting {
			m[name] = proc.Addr()
		}
	}
	return m
}

// Names returns names of all registered custom balancers (any state).
func (p *Pool) Names() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	names := make([]string, 0, len(p.processes))
	for name := range p.processes {
		names = append(names, name)
	}
	return names
}

// QualityBadge returns the quality badge for a custom balancer.
func (p *Pool) QualityBadge(name string) string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if proc, ok := p.processes[name]; ok {
		return proc.Config.QualityBadge
	}
	return ""
}

// Get returns a process by name.
func (p *Pool) Get(name string) (*Process, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	proc, ok := p.processes[name]
	return proc, ok
}

// StopAll stops all custom balancer subprocesses.
func (p *Pool) StopAll() {
	p.mu.RLock()
	procs := make([]*Process, 0, len(p.processes))
	for _, proc := range p.processes {
		procs = append(procs, proc)
	}
	p.mu.RUnlock()

	for _, proc := range procs {
		proc.Stop()
	}
}

// allocPort finds the next available port starting from basePort.
func (p *Pool) allocPort() int {
	used := make(map[int]bool)
	for _, proc := range p.processes {
		used[proc.Config.Port] = true
	}
	port := p.basePort
	for used[port] {
		port++
	}
	return port
}

// ConfigForJSON returns all configs as a JSON-friendly structure for admin panel.
func (p *Pool) ConfigForJSON() []json.RawMessage {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var out []json.RawMessage
	for _, proc := range p.processes {
		data, _ := json.Marshal(proc.Status())
		out = append(out, data)
	}
	return out
}
