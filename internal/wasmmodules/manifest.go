// Package wasmmodules runs sandboxed WebAssembly plugins that implement the
// same balancer/source contract as the JavaScript modules. Plugins can be
// authored in any language that compiles to WASI (TinyGo, Rust, AssemblyScript,
// Zig, …); the host exposes a small ABI for HTTP, proxy URL signing, logging
// and per-module cache.
//
// Manifest semantics mirror jsmodules so the admin UI and hub.alcopa.cc
// catalogue can treat both kinds uniformly.
package wasmmodules

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Target tells the host where a plugin is meant to run.
//
//   - "server" — invoked from /lite/{id} on this Go process. Must export
//     handle(ptr,len)->i64 and alloc(size)->i32.
//   - "client" — shipped to the browser, instantiated by Lampa via
//     WebAssembly.instantiateStreaming. The server only serves the .wasm and
//     a manifest entry; runtime stays on the client.
//   - "both"   — manifest declares two artefacts (entry_server / entry_client)
//     so a single plugin can register a balancer AND a Lampa UI extension.
type Target string

const (
	TargetServer     Target = "server"
	TargetClient     Target = "client"
	TargetBoth       Target = "both"
	TargetMiddleware Target = "middleware"
)

// Manifest describes a single WASM plugin, deserialized from manifest.json.
type Manifest struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Author      string `json:"author,omitempty"`
	Description string `json:"description,omitempty"`
	Icon        string `json:"icon,omitempty"`
	Repository  string `json:"repository,omitempty"`

	// Target selects server / client / both. Server is the default — it's the
	// only target the host actually runs; client/both still get loaded so the
	// admin UI can list them and the static .wasm gets served.
	Target Target `json:"target,omitempty"`

	// EntryServer is the path (relative to the module dir) of the .wasm file
	// loaded into the wazero runtime. Required when Target is "server" or
	// "both"; defaults to "plugin.wasm".
	EntryServer string `json:"entry_server,omitempty"`

	// EntryClient is the path of the .wasm file streamed to the browser.
	// Required when Target is "client" or "both".
	EntryClient string `json:"entry_client,omitempty"`

	// Language is informational ("tinygo", "rust", "as", "zig", …) — shown in
	// the admin UI so you can tell which toolchain produced a given module.
	Language string `json:"language,omitempty"`

	// Upstreams lists the balancer IDs this middleware module wraps. Only
	// meaningful when Target == "middleware". The host calls each upstream's
	// /lite/{id} first, then hands the merged JSON list to the guest's
	// handle() for filtering/transformation. Set to ["*"] to wrap every
	// balancer the host knows about.
	Upstreams []string `json:"upstreams,omitempty"`

	// ABIVersion lets the host reject plugins built against an incompatible
	// host ABI. Bumped when host imports change in a breaking way.
	ABIVersion int `json:"abi_version,omitempty"`

	// Catalogue / categorisation — same fields as jsmodules.Manifest so the
	// admin panel and hub catalogue can treat both module kinds the same.
	ContentTypes []string      `json:"content_types,omitempty"`
	Quality      string        `json:"quality,omitempty"`
	Languages    []string      `json:"languages,omitempty"`
	Anime        bool          `json:"anime,omitempty"`
	Ukrainian    bool          `json:"ukrainian,omitempty"`
	Tags         []string      `json:"tags,omitempty"`
	Permissions  []string      `json:"permissions,omitempty"`
	ConfigSchema []ConfigField `json:"config_schema,omitempty"`

	// Defaults is opaque JSON merged into the runtime config snapshot before
	// admin overrides — same convention as jsmodules.
	Defaults json.RawMessage `json:"defaults,omitempty"`
}

// ConfigField mirrors jsmodules.ConfigField — same admin-panel widgets work
// for both module kinds.
type ConfigField struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Type        string   `json:"type,omitempty"`
	Default     any      `json:"default,omitempty"`
	Description string   `json:"description,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
	Secret      bool     `json:"secret,omitempty"`
	Options     []string `json:"options,omitempty"`
}

// CurrentABIVersion is the host ABI revision shipped with this build. Plugins
// declaring a different ABIVersion are loaded but flagged in the admin UI.
const CurrentABIVersion = 1

var moduleIDRE = regexp.MustCompile(`^[a-z][a-z0-9_]{1,31}$`)

// Validate enforces invariants and applies defaults. Mirrors jsmodules
// validation so IDs from one kind cannot collide with the other namespace.
func (m *Manifest) Validate() error {
	id := strings.TrimSpace(m.ID)
	if id == "" {
		return fmt.Errorf("manifest: id is required")
	}
	if !moduleIDRE.MatchString(id) {
		return fmt.Errorf("manifest: id %q must match [a-z][a-z0-9_]{1,31}", id)
	}
	m.ID = id
	if strings.TrimSpace(m.Name) == "" {
		m.Name = id
	}
	if strings.TrimSpace(m.Version) == "" {
		m.Version = "0.0.0"
	}
	if m.Target == "" {
		m.Target = TargetServer
	}
	switch m.Target {
	case TargetServer, TargetClient, TargetBoth, TargetMiddleware:
	default:
		return fmt.Errorf("manifest: unknown target %q (server|client|both|middleware)", m.Target)
	}
	if m.Target == TargetServer || m.Target == TargetBoth || m.Target == TargetMiddleware {
		if strings.TrimSpace(m.EntryServer) == "" {
			m.EntryServer = "plugin.wasm"
		}
	}
	if m.Target == TargetMiddleware && len(m.Upstreams) == 0 {
		return fmt.Errorf("manifest: middleware target requires upstreams")
	}
	if m.Target == TargetClient || m.Target == TargetBoth {
		if strings.TrimSpace(m.EntryClient) == "" {
			return fmt.Errorf("manifest: entry_client is required for target %q", m.Target)
		}
	}
	return nil
}

// HasServer reports whether the plugin ships a server-side .wasm. Middleware
// modules also run server-side, so they count.
func (m *Manifest) HasServer() bool {
	return m.Target == TargetServer || m.Target == TargetBoth || m.Target == TargetMiddleware
}

// HasClient reports whether the plugin ships a client-side .wasm.
func (m *Manifest) HasClient() bool { return m.Target == TargetClient || m.Target == TargetBoth }

// IsMiddleware reports whether this plugin filters/transforms results from
// upstream balancers rather than acting as one.
func (m *Manifest) IsMiddleware() bool { return m.Target == TargetMiddleware }

// LoadManifest reads manifest.json from a module directory and validates it.
func LoadManifest(dir string) (Manifest, error) {
	var m Manifest
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("manifest parse: %w", err)
	}
	if err := m.Validate(); err != nil {
		return m, err
	}
	return m, nil
}

// SaveManifest writes a manifest back to disk (admin may edit it).
func SaveManifest(dir string, m Manifest) error {
	if err := m.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(&m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0644)
}
