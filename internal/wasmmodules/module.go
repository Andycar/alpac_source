package wasmmodules

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Module is a loaded WASM plugin with its manifest, on-disk path, mutable
// admin overrides, runtime stats and an attached cache.
type Module struct {
	Manifest Manifest
	Dir      string
	Enabled  bool
	Error    string

	// rawWASM is the cached server-side .wasm file; reused on every Invoke
	// (wazero re-instantiates per call would be very slow). nil for client-only.
	rawWASM []byte

	// rt is the long-lived runtime; lazy on first Invoke.
	rtMu sync.Mutex
	rt   *Runtime

	cache    *cacheStore
	overrideMu sync.RWMutex
	overrides  map[string]any

	perms *permissionSet
	stats ModuleStats
}

// PluginKey is the identifier under which the module registers with the
// dynamic route registry — kept identical to the manifest ID so admin URLs
// like /lite/foo work for both module kinds.
func (m *Module) PluginKey() string { return m.Manifest.ID }

// ConfigSnapshot merges manifest defaults with admin overrides into a JSON
// blob handed to the guest as inv.config. Mirrors jsmodules.Module behaviour.
func (m *Module) ConfigSnapshot() json.RawMessage {
	merged := map[string]any{}

	for _, f := range m.Manifest.ConfigSchema {
		if f.Default != nil {
			merged[f.Key] = f.Default
		}
	}
	if len(m.Manifest.Defaults) > 0 {
		var d map[string]any
		if err := json.Unmarshal(m.Manifest.Defaults, &d); err == nil {
			for k, v := range d {
				merged[k] = v
			}
		}
	}
	m.overrideMu.RLock()
	for k, v := range m.overrides {
		merged[k] = v
	}
	m.overrideMu.RUnlock()

	out, err := json.Marshal(merged)
	if err != nil {
		return json.RawMessage("{}")
	}
	return out
}

// SetOverride writes a single config key (admin panel call). Pass nil to
// clear. Persisted to {dir}/config.json.
func (m *Module) SetOverride(key string, value any) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("empty key")
	}
	m.overrideMu.Lock()
	if m.overrides == nil {
		m.overrides = map[string]any{}
	}
	if value == nil {
		delete(m.overrides, key)
	} else {
		m.overrides[key] = value
	}
	snapshot := make(map[string]any, len(m.overrides))
	for k, v := range m.overrides {
		snapshot[k] = v
	}
	m.overrideMu.Unlock()

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(m.Dir, "config.json"), data, 0644)
}

// loadOverrides reads {dir}/config.json if present.
func (m *Module) loadOverrides() {
	data, err := os.ReadFile(filepath.Join(m.Dir, "config.json"))
	if err != nil {
		return
	}
	var v map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		return
	}
	m.overrideMu.Lock()
	m.overrides = v
	m.overrideMu.Unlock()
}

// Stats returns a non-atomic snapshot fit for JSON marshalling.
func (m *Module) Stats() StatsSnapshot { return m.stats.Snapshot() }
