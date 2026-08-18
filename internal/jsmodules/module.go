package jsmodules

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dop251/goja"
)

// Module holds the parsed JS program, manifest, and config for one installed
// module. Modules are created by the Manager on disk scan and reused across
// requests; the actual goja.Runtime is re-created per request.
type Module struct {
	Manifest Manifest
	Dir      string
	Enabled  bool
	Source   string    // raw JS source
	LoadedAt time.Time // timestamp of last (re)load
	Error    string    // last parse error

	program *goja.Program
	cache   *cacheStore

	cfgMu    sync.RWMutex
	cfg      map[string]any
	cfgBytes atomic.Pointer[[]byte] // for quick read

	stats ModuleStats
}

// ModuleStats accumulates runtime metrics shown in the admin panel.
type ModuleStats struct {
	mu            sync.Mutex
	RequestCount  uint64    `json:"request_count"`
	ErrorCount    uint64    `json:"error_count"`
	LastRequest   time.Time `json:"last_request,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
	AvgDurationMs float64   `json:"avg_duration_ms"`
}

func (s *ModuleStats) recordOk(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.RequestCount++
	s.LastRequest = time.Now()
	// Exponential moving average.
	ms := float64(d.Milliseconds())
	if s.AvgDurationMs == 0 {
		s.AvgDurationMs = ms
	} else {
		s.AvgDurationMs = 0.8*s.AvgDurationMs + 0.2*ms
	}
}

func (s *ModuleStats) recordError(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ErrorCount++
	s.LastError = msg
	s.LastRequest = time.Now()
}

// ModuleStatsSnapshot is a lock-free copy of ModuleStats, safe to return and
// marshal. ModuleStats itself embeds a sync.Mutex and must never be copied by
// value (go vet copylocks) — always take a Snapshot.
type ModuleStatsSnapshot struct {
	RequestCount  uint64    `json:"request_count"`
	ErrorCount    uint64    `json:"error_count"`
	LastRequest   time.Time `json:"last_request,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
	AvgDurationMs float64   `json:"avg_duration_ms"`
}

// Snapshot returns a lock-free copy safe to marshal.
func (s *ModuleStats) Snapshot() ModuleStatsSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ModuleStatsSnapshot{
		RequestCount:  s.RequestCount,
		ErrorCount:    s.ErrorCount,
		LastRequest:   s.LastRequest,
		LastError:     s.LastError,
		AvgDurationMs: s.AvgDurationMs,
	}
}

// Config file path inside the module dir.
const configFileName = "config.json"

// LoadModule compiles a module from its directory.
func LoadModule(dir string) (*Module, error) {
	manifest, err := LoadManifest(dir)
	if err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	entry := manifest.Entry
	if entry == "" {
		entry = "index.js"
	}
	srcPath := filepath.Join(dir, entry)
	source, err := os.ReadFile(srcPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", entry, err)
	}

	prog, perr := goja.Compile(manifest.ID, string(source), true)
	if perr != nil {
		// Still return a module so admin can see+fix the error.
		mod := &Module{
			Manifest: manifest, Dir: dir,
			Source: string(source), LoadedAt: time.Now(),
			Error: perr.Error(), cache: newCacheStore(), Enabled: true,
		}
		mod.loadConfig()
		return mod, nil
	}

	mod := &Module{
		Manifest: manifest, Dir: dir,
		Source:   string(source),
		program:  prog,
		cache:    newCacheStore(),
		LoadedAt: time.Now(),
		Enabled:  true,
	}
	mod.loadConfig()
	return mod, nil
}

// SourcePath returns the full path to the entry JS file.
func (m *Module) SourcePath() string {
	entry := m.Manifest.Entry
	if entry == "" {
		entry = "index.js"
	}
	return filepath.Join(m.Dir, entry)
}

// SetSource rewrites the JS source on disk and recompiles the program.
// Returns the new parse error (empty on success).
func (m *Module) SetSource(source string) error {
	if err := os.WriteFile(m.SourcePath(), []byte(source), 0644); err != nil {
		return fmt.Errorf("write source: %w", err)
	}
	m.Source = source
	prog, perr := goja.Compile(m.Manifest.ID, source, true)
	if perr != nil {
		m.Error = perr.Error()
		m.program = nil
		return perr
	}
	m.Error = ""
	m.program = prog
	m.LoadedAt = time.Now()
	return nil
}

// Reload re-reads manifest, source, and config from disk.
func (m *Module) Reload() error {
	man, err := LoadManifest(m.Dir)
	if err != nil {
		return err
	}
	entry := man.Entry
	if entry == "" {
		entry = "index.js"
	}
	src, err := os.ReadFile(filepath.Join(m.Dir, entry))
	if err != nil {
		return err
	}
	prog, perr := goja.Compile(man.ID, string(src), true)
	if perr != nil {
		m.Manifest = man
		m.Source = string(src)
		m.Error = perr.Error()
		m.program = nil
		m.LoadedAt = time.Now()
		m.loadConfig()
		return perr
	}
	m.Manifest = man
	m.Source = string(src)
	m.program = prog
	m.Error = ""
	m.LoadedAt = time.Now()
	m.loadConfig()
	return nil
}

// Config returns the live module config map (read-only from the caller's view).
func (m *Module) Config() map[string]any {
	m.cfgMu.RLock()
	defer m.cfgMu.RUnlock()
	cp := make(map[string]any, len(m.cfg))
	for k, v := range m.cfg {
		cp[k] = v
	}
	return cp
}

// ConfigSnapshot returns an immutable snapshot suitable for passing to JS.
func (m *Module) ConfigSnapshot() map[string]any {
	m.cfgMu.RLock()
	defer m.cfgMu.RUnlock()
	cp := make(map[string]any, len(m.cfg))
	for k, v := range m.cfg {
		cp[k] = v
	}
	return cp
}

// SetConfig merges an update into the config and persists config.json.
func (m *Module) SetConfig(update map[string]any) error {
	m.cfgMu.Lock()
	if m.cfg == nil {
		m.cfg = map[string]any{}
	}
	for k, v := range update {
		m.cfg[k] = v
	}
	cfg := make(map[string]any, len(m.cfg))
	for k, v := range m.cfg {
		cfg[k] = v
	}
	m.cfgMu.Unlock()

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	cp := append([]byte(nil), data...)
	m.cfgBytes.Store(&cp)
	return os.WriteFile(filepath.Join(m.Dir, configFileName), data, 0644)
}

// loadConfig reads config.json or seeds defaults from the manifest.
func (m *Module) loadConfig() {
	cfg := make(map[string]any)
	// Seed from manifest defaults.
	if len(m.Manifest.Defaults) > 0 {
		_ = json.Unmarshal(m.Manifest.Defaults, &cfg)
	}
	for _, f := range m.Manifest.ConfigSchema {
		if f.Default != nil {
			if _, exists := cfg[f.Key]; !exists {
				cfg[f.Key] = f.Default
			}
		}
	}
	// Overlay disk config.
	data, err := os.ReadFile(filepath.Join(m.Dir, configFileName))
	if err == nil {
		var disk map[string]any
		if json.Unmarshal(data, &disk) == nil {
			for k, v := range disk {
				cfg[k] = v
			}
		}
	}
	m.cfgMu.Lock()
	m.cfg = cfg
	m.cfgMu.Unlock()
}

// PluginKey returns the route key for this module (== manifest ID).
func (m *Module) PluginKey() string { return strings.ToLower(m.Manifest.ID) }
