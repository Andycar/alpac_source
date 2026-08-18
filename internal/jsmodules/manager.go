package jsmodules

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// ModuleListener is notified on install/reload/remove so callers (HTTP router,
// admin panel) can keep their caches in sync.
type ModuleListener func(m *Module, event string)

// Manager is the top-level registry for JS modules. It owns the on-disk dir,
// tracks loaded modules, issues runtimes to handlers, and mediates admin ops.
type Manager struct {
	BaseDir    string
	HTTPClient HTTPClient
	Proxy      ProxyBuilder
	Logger     *zerolog.Logger

	// FlareSolverr is the URL of a FlareSolverr instance (optional). When set,
	// JS modules can request transport:"flaresolverr" from http.get/post and
	// route the call through it to bypass Cloudflare/DDoS-Guard challenges.
	// Wire this from cfg.Online.FlareSolverr at startup.
	FlareSolverr string

	mu      sync.RWMutex
	modules map[string]*Module
	sink    *logSink

	listeners []ModuleListener
	listMu    sync.Mutex

	clients *clientCache
}

// NewManager creates a manager rooted at baseDir. The directory is created if
// missing. The manager does not start a disk watcher by itself; callers should
// invoke Scan() on startup.
func NewManager(baseDir string, httpClient HTTPClient, proxy ProxyBuilder, logger *zerolog.Logger) (*Manager, error) {
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, err
	}
	m := &Manager{
		BaseDir:    baseDir,
		HTTPClient: httpClient,
		Proxy:      proxy,
		Logger:     logger,
		modules:    make(map[string]*Module),
		sink:       newLogSink(500),
	}
	return m, nil
}

// OnChange registers a listener that gets called on install/reload/remove.
func (m *Manager) OnChange(fn ModuleListener) {
	m.listMu.Lock()
	m.listeners = append(m.listeners, fn)
	m.listMu.Unlock()
}

func (m *Manager) fireEvent(mod *Module, event string) {
	m.listMu.Lock()
	listeners := append([]ModuleListener(nil), m.listeners...)
	m.listMu.Unlock()
	for _, fn := range listeners {
		fn(mod, event)
	}
}

// Scan loads every subdirectory of BaseDir that contains a manifest.json.
// Modules that fail to parse are still added with their Error field set, so the
// admin panel can show them.
func (m *Manager) Scan() error {
	entries, err := os.ReadDir(m.BaseDir)
	if err != nil {
		return err
	}

	seen := map[string]struct{}{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(m.BaseDir, e.Name())
		if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err != nil {
			continue
		}
		mod, err := LoadModule(dir)
		if err != nil {
			if m.Logger != nil {
				m.Logger.Warn().Str("dir", dir).Err(err).Msg("jsmodules: skip module")
			}
			continue
		}
		if IsReserved(mod.Manifest.ID) {
			if m.Logger != nil {
				m.Logger.Warn().Str("id", mod.Manifest.ID).Msg("jsmodules: reserved id, skip")
			}
			continue
		}
		m.mu.Lock()
		existing := m.modules[mod.PluginKey()]
		if existing != nil {
			// Preserve Enabled state across reload.
			mod.Enabled = existing.Enabled
		}
		m.modules[mod.PluginKey()] = mod
		m.mu.Unlock()
		seen[mod.PluginKey()] = struct{}{}
		m.fireEvent(mod, "loaded")
	}

	// Remove modules whose directory has disappeared.
	m.mu.Lock()
	for id, mod := range m.modules {
		if _, ok := seen[id]; !ok {
			delete(m.modules, id)
			go m.fireEvent(mod, "removed")
		}
	}
	m.mu.Unlock()
	return nil
}

// List returns a stable-sorted snapshot of all installed modules.
func (m *Manager) List() []*Module {
	m.mu.RLock()
	out := make([]*Module, 0, len(m.modules))
	for _, mod := range m.modules {
		out = append(out, mod)
	}
	m.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Manifest.ID < out[j].Manifest.ID })
	return out
}

// Get returns the module with the given ID (case-insensitive).
func (m *Manager) Get(id string) (*Module, bool) {
	m.mu.RLock()
	mod, ok := m.modules[strings.ToLower(id)]
	m.mu.RUnlock()
	return mod, ok
}

// Enabled reports whether a module exists and is enabled.
func (m *Manager) Enabled(id string) bool {
	mod, ok := m.Get(id)
	return ok && mod.Enabled
}

// Has reports whether a module with the given ID is installed.
func (m *Manager) Has(id string) bool {
	_, ok := m.Get(id)
	return ok
}

// SetEnabled flips the enabled flag (persisted in config.json).
func (m *Manager) SetEnabled(id string, enabled bool) error {
	mod, ok := m.Get(id)
	if !ok {
		return fmt.Errorf("module %q not found", id)
	}
	mod.Enabled = enabled
	cfg := mod.Config()
	cfg["_enabled"] = enabled
	if err := mod.SetConfig(cfg); err != nil {
		return err
	}
	m.fireEvent(mod, "toggled")
	return nil
}

// Reload re-reads a module from disk.
func (m *Manager) Reload(id string) error {
	mod, ok := m.Get(id)
	if !ok {
		return fmt.Errorf("module %q not found", id)
	}
	if err := mod.Reload(); err != nil {
		m.fireEvent(mod, "reloaded")
		return err
	}
	m.fireEvent(mod, "reloaded")
	return nil
}

// Remove deletes the module from disk and unregisters it.
func (m *Manager) Remove(id string) error {
	mod, ok := m.Get(id)
	if !ok {
		return fmt.Errorf("module %q not found", id)
	}
	dir := mod.Dir
	if !strings.HasPrefix(filepath.Clean(dir), filepath.Clean(m.BaseDir)) {
		return fmt.Errorf("refusing to remove %s — outside BaseDir", dir)
	}
	m.mu.Lock()
	delete(m.modules, mod.PluginKey())
	m.mu.Unlock()
	m.fireEvent(mod, "removed")
	return os.RemoveAll(dir)
}

// InstallFromZip accepts a zip archive in memory, expands it to a temp dir,
// validates the manifest, moves it into BaseDir, and loads the module.
func (m *Manager) InstallFromZip(data []byte) (*Module, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("unzip: %w", err)
	}

	// Find manifest.json to determine the module ID.
	var manifest Manifest
	var rootPrefix string
	for _, f := range zr.File {
		if filepath.Base(f.Name) == "manifest.json" && filepath.Dir(f.Name) != "" {
			// Nested under a folder like "module-foo/manifest.json".
			rootPrefix = filepath.Dir(f.Name) + "/"
		}
		if f.Name == "manifest.json" || rootPrefix != "" && f.Name == rootPrefix+"manifest.json" {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			data, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return nil, err
			}
			if err := json.Unmarshal(data, &manifest); err != nil {
				return nil, fmt.Errorf("manifest parse: %w", err)
			}
			break
		}
	}
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	if IsReserved(manifest.ID) {
		return nil, fmt.Errorf("module id %q is reserved", manifest.ID)
	}

	destDir := filepath.Join(m.BaseDir, manifest.ID)
	if _, err := os.Stat(destDir); err == nil {
		// Archive into .backup-<timestamp>
		backup := destDir + ".backup-" + fmt.Sprintf("%d", time.Now().Unix())
		if err := os.Rename(destDir, backup); err != nil {
			return nil, fmt.Errorf("backup existing: %w", err)
		}
	}
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return nil, err
	}

	for _, f := range zr.File {
		name := f.Name
		if rootPrefix != "" {
			if !strings.HasPrefix(name, rootPrefix) {
				continue
			}
			name = strings.TrimPrefix(name, rootPrefix)
		}
		if name == "" || strings.HasSuffix(name, "/") {
			continue
		}
		// Guard against zip slip.
		outPath := filepath.Join(destDir, name)
		if !strings.HasPrefix(filepath.Clean(outPath), filepath.Clean(destDir)) {
			return nil, fmt.Errorf("zip entry escapes dir: %s", name)
		}
		if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
			return nil, err
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		out, err := os.Create(outPath)
		if err != nil {
			rc.Close()
			return nil, err
		}
		_, err = io.Copy(out, rc)
		rc.Close()
		out.Close()
		if err != nil {
			return nil, err
		}
	}

	mod, err := LoadModule(destDir)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.modules[mod.PluginKey()] = mod
	m.mu.Unlock()
	m.fireEvent(mod, "installed")
	return mod, nil
}

// InstallFromURL downloads a zip from the given URL and installs it.
func (m *Manager) InstallFromURL(ctx context.Context, url string) (*Module, error) {
	if m.HTTPClient == nil {
		return nil, fmt.Errorf("http client not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := m.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("download: status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
	if err != nil {
		return nil, err
	}
	return m.InstallFromZip(data)
}

// Invoke runs a module handler. Returns NotFound error if the module is absent
// or disabled, or if no handler is defined.
func (m *Manager) Invoke(ctx context.Context, id string, inv Invocation) (Response, error) {
	mod, ok := m.Get(id)
	if !ok {
		return Response{}, fmt.Errorf("module %q not installed", id)
	}
	if !mod.Enabled {
		return Response{}, fmt.Errorf("module %q disabled", id)
	}
	if mod.program == nil {
		return Response{}, fmt.Errorf("module %q has parse error: %s", id, mod.Error)
	}

	rt, err := NewRuntimeWithManager(mod, m.HTTPClient, m.Proxy, m.Logger, m.sink, m)
	if err != nil {
		mod.stats.recordError(err.Error())
		return Response{}, err
	}
	defer rt.Close()

	start := time.Now()
	resp, err := rt.Invoke(ctx, inv)
	if err != nil {
		mod.stats.recordError(err.Error())
		m.sink.Append(id, "error", err.Error())
		return Response{}, err
	}
	mod.stats.recordOk(time.Since(start))
	return resp, nil
}

// LogTail returns recent log entries.
func (m *Manager) LogTail(module string, n int) []LogEntry { return m.sink.Tail(module, n) }

// LogSubscribe returns a channel receiving new log entries.
func (m *Manager) LogSubscribe(buf int) (int, <-chan LogEntry) { return m.sink.Subscribe(buf) }

// LogUnsubscribe closes a subscription.
func (m *Manager) LogUnsubscribe(id int) { m.sink.Unsubscribe(id) }

// LogClear wipes the ring buffer for a module.
func (m *Manager) LogClear(module string) { m.sink.Clear(module) }

// StatsSnapshot returns a lock-free copy of stats for a module.
func (m *Manager) StatsSnapshot(id string) (ModuleStatsSnapshot, bool) {
	mod, ok := m.Get(id)
	if !ok {
		return ModuleStatsSnapshot{}, false
	}
	return mod.stats.Snapshot(), true
}
