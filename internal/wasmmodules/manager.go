package wasmmodules

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// ModuleListener mirrors jsmodules.ModuleListener — admin / router can watch
// install/reload/remove events to keep their caches in sync.
type ModuleListener func(m *Module, event string)

// Manager owns the on-disk wasm_modules directory, holds loaded modules and
// hands out runtimes via Invoke().
type Manager struct {
	BaseDir    string
	HTTPClient HTTPClient
	Proxy      ProxyBuilder
	Logger     *zerolog.Logger

	mu        sync.RWMutex
	modules   map[string]*Module
	sink      *logSink
	crashes   *crashStore
	upstreams UpstreamResolver

	listeners []ModuleListener
	listMu    sync.Mutex
}

// NewManager creates a manager rooted at baseDir. The directory is created
// if missing. Scan() must be called separately.
func NewManager(baseDir string, httpClient HTTPClient, proxy ProxyBuilder, logger *zerolog.Logger) (*Manager, error) {
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, err
	}
	// Crash dumps live next to the module dir so backups carry them.
	crashRoot := filepath.Join(filepath.Dir(baseDir), "wasm_dumps")
	return &Manager{
		BaseDir:    baseDir,
		HTTPClient: httpClient,
		Proxy:      proxy,
		Logger:     logger,
		modules:    make(map[string]*Module),
		sink:       newLogSink(500),
		crashes:    newCrashStore(crashRoot),
	}, nil
}

// Crashes exposes the crash-dump store for the admin panel.
func (m *Manager) Crashes(moduleID string, limit int) []CrashDump {
	return m.crashes.List(moduleID, limit)
}

// OnChange registers a listener.
func (m *Manager) OnChange(fn ModuleListener) {
	m.listMu.Lock()
	m.listeners = append(m.listeners, fn)
	m.listMu.Unlock()
}

func (m *Manager) fire(mod *Module, event string) {
	m.listMu.Lock()
	listeners := append([]ModuleListener(nil), m.listeners...)
	m.listMu.Unlock()
	for _, fn := range listeners {
		fn(mod, event)
	}
}

// Scan loads every subdirectory of BaseDir that contains a manifest.json.
// Modules that fail to parse are still added with their Error field set so
// the admin UI can show them.
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
		mod, err := m.loadFromDir(dir)
		if err != nil {
			if m.Logger != nil {
				m.Logger.Warn().Str("dir", dir).Err(err).Msg("wasmmodules: load failed")
			}
			continue
		}
		seen[mod.Manifest.ID] = struct{}{}

		m.mu.Lock()
		prev, exists := m.modules[mod.Manifest.ID]
		m.modules[mod.Manifest.ID] = mod
		m.mu.Unlock()

		if exists {
			prev.rtMu.Lock()
			if prev.rt != nil {
				prev.rt.Close(context.Background())
			}
			prev.rtMu.Unlock()
			m.fire(mod, "reloaded")
		} else {
			m.fire(mod, "loaded")
		}
	}

	// Drop modules whose dir disappeared.
	m.mu.Lock()
	var removed []*Module
	for id, mod := range m.modules {
		if _, ok := seen[id]; !ok {
			removed = append(removed, mod)
			delete(m.modules, id)
		}
	}
	m.mu.Unlock()
	for _, mod := range removed {
		mod.rtMu.Lock()
		if mod.rt != nil {
			mod.rt.Close(context.Background())
		}
		mod.rtMu.Unlock()
		m.fire(mod, "removed")
	}
	return nil
}

// loadFromDir reads the manifest, the .wasm file (if server target) and the
// admin overrides.
func (m *Manager) loadFromDir(dir string) (*Module, error) {
	mf, err := LoadManifest(dir)
	if err != nil {
		return nil, err
	}
	mod := &Module{
		Manifest: mf,
		Dir:      dir,
		Enabled:  true,
		cache:    newCacheStore(),
		perms:    newPermissionSet(mf.Permissions),
	}
	mod.loadOverrides()

	if mf.HasServer() {
		wasmPath := filepath.Join(dir, mf.EntryServer)
		raw, err := os.ReadFile(wasmPath)
		if err != nil {
			mod.Error = fmt.Sprintf("read %s: %v", mf.EntryServer, err)
			return mod, nil // still return mod so admin sees it
		}
		mod.rawWASM = raw
	}
	return mod, nil
}

// Get fetches a loaded module by ID.
func (m *Manager) Get(id string) (*Module, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	mod, ok := m.modules[id]
	return mod, ok
}

// List returns a deterministic snapshot of all modules.
func (m *Manager) List() []*Module {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Module, 0, len(m.modules))
	for _, mod := range m.modules {
		out = append(out, mod)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Manifest.ID < out[j].Manifest.ID })
	return out
}

// SetEnabled toggles a module on/off without removing it.
func (m *Manager) SetEnabled(id string, enabled bool) error {
	m.mu.Lock()
	mod, ok := m.modules[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("module %q not found", id)
	}
	mod.Enabled = enabled
	m.mu.Unlock()
	m.fire(mod, "toggled")
	return nil
}

// Invoke executes the guest's handle() with the given invocation. Server
// modules only.
func (m *Manager) Invoke(ctx context.Context, id string, inv Invocation) (Response, error) {
	mod, ok := m.Get(id)
	if !ok {
		return Response{}, fmt.Errorf("module %q not found", id)
	}
	if !mod.Manifest.HasServer() {
		return Response{}, fmt.Errorf("module %q is client-only", id)
	}
	if mod.rawWASM == nil {
		return Response{}, fmt.Errorf("module %q: %s", id, mod.Error)
	}
	rt, err := m.runtimeFor(ctx, mod)
	if err != nil {
		mod.stats.recordInvoke(0, err)
		m.recordCrash(mod, inv, err, 0)
		return Response{}, err
	}
	start := time.Now()
	resp, err := rt.Invoke(ctx, inv)
	mod.stats.recordInvoke(time.Since(start), err)
	if err != nil {
		var memBytes uint32
		if rt.instance != nil && rt.instance.Memory() != nil {
			memBytes = rt.instance.Memory().Size()
		}
		m.recordCrash(mod, inv, err, memBytes)
		return Response{}, err
	}
	return resp, nil
}

// recordCrash persists one CrashDump and snags the last 50 log lines from
// the sink so the admin panel can replay what the plugin was doing.
func (m *Manager) recordCrash(mod *Module, inv Invocation, err error, memBytes uint32) {
	if m.crashes == nil {
		return
	}
	headers := ""
	if len(inv.Headers) > 0 {
		// Only keep a flat preview; full headers can leak sensitive cookies.
		var keys []string
		for k := range inv.Headers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		headers = "[" + fmt.Sprint(keys) + "]"
	}
	query := ""
	if len(inv.Query) > 0 {
		var pairs []string
		for k, v := range inv.Query {
			pairs = append(pairs, k+"="+v)
		}
		sort.Strings(pairs)
		query = fmt.Sprint(pairs)
	}
	dump := CrashDump{
		Module:   mod.Manifest.ID,
		Time:     time.Now(),
		Error:    err.Error(),
		Path:     inv.Path,
		Query:    query,
		Headers:  headers,
		MemBytes: memBytes,
		LogTail:  m.sink.Tail(mod.Manifest.ID, 50),
	}
	if path, saveErr := m.crashes.Save(dump); saveErr != nil {
		if m.Logger != nil {
			m.Logger.Warn().Err(saveErr).Str("module", mod.Manifest.ID).Msg("wasmmodules: crashdump save failed")
		}
	} else if m.Logger != nil {
		m.Logger.Warn().Str("module", mod.Manifest.ID).Str("dump", path).Err(err).Msg("wasmmodules: plugin crash recorded")
	}
}

func (m *Manager) runtimeFor(ctx context.Context, mod *Module) (*Runtime, error) {
	mod.rtMu.Lock()
	defer mod.rtMu.Unlock()
	if mod.rt != nil {
		return mod.rt, nil
	}
	rt, err := NewRuntime(ctx, mod, mod.rawWASM, m.HTTPClient, m.Proxy, m.Logger, m.sink)
	if err != nil {
		return nil, err
	}
	mod.rt = rt
	return rt, nil
}

// Logs returns the last n log lines (optionally filtered by module ID).
func (m *Manager) Logs(moduleID string, n int) []LogEntry {
	if n <= 0 {
		n = 200
	}
	return m.sink.Tail(moduleID, n)
}

// SubscribeLogs returns a live log channel; caller must Unsubscribe when done.
func (m *Manager) SubscribeLogs() chan LogEntry { return m.sink.Subscribe() }
func (m *Manager) UnsubscribeLogs(ch chan LogEntry) { m.sink.Unsubscribe(ch) }
