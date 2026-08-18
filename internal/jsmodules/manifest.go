// Package jsmodules provides a sandboxed JavaScript runtime for user-editable
// media source modules. Each module lives in its own directory with a manifest
// and an entry .js file; the runtime exposes a stable API (http, proxy, cache,
// log) so modules can be written without knowing Go internals.
package jsmodules

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Manifest describes a single module, deserialized from manifest.json.
type Manifest struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Version      string          `json:"version"`
	Author       string          `json:"author,omitempty"`
	Description  string          `json:"description,omitempty"`
	Icon         string          `json:"icon,omitempty"`
	Entry        string          `json:"entry,omitempty"`
	ContentTypes []string        `json:"content_types,omitempty"`
	Quality      string          `json:"quality,omitempty"`
	Languages    []string        `json:"languages,omitempty"`
	HostDefaults string          `json:"host_defaults,omitempty"`
	ConfigSchema []ConfigField   `json:"config_schema,omitempty"`
	Permissions  []string        `json:"permissions,omitempty"`
	Repository   string          `json:"repository,omitempty"`
	Anime        bool            `json:"anime,omitempty"`
	Ukrainian    bool            `json:"ukrainian,omitempty"`
	Tags         []string        `json:"tags,omitempty"`
	Defaults     json.RawMessage `json:"defaults,omitempty"`

	// HTTP transport defaults applied to every http.* call from this module
	// unless overridden per-call. Lets a module declare once "I need uTLS via
	// SOCKS5" without sprinkling opts.transport everywhere.
	//   DefaultTransport: "" | "default" | "utls" | "balancer" | "balancer-utls"
	//                   | "socks5" | "utls-socks5" | "flaresolverr"
	//   DefaultProxy:     explicit "host:port" SOCKS5 (overrides balancer lookup)
	//   DefaultBalancer:  balancer name to look up in proxy registry; defaults
	//                     to the module ID so that
	//                     [[proxy.direct.entries]] balancers=["redheadsound"]
	//                     just works without per-module wiring.
	DefaultTransport string `json:"default_transport,omitempty"`
	DefaultProxy     string `json:"default_proxy,omitempty"`
	DefaultBalancer  string `json:"default_balancer,omitempty"`
}

// ConfigField describes one editable config key in the admin UI.
type ConfigField struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Type        string   `json:"type,omitempty"`        // string, int, bool, secret, textarea, enum
	Default     any      `json:"default,omitempty"`
	Description string   `json:"description,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
	Secret      bool     `json:"secret,omitempty"`
	Options     []string `json:"options,omitempty"`     // for type:"enum" — dropdown choices
}

var moduleIDRE = regexp.MustCompile(`^[a-z][a-z0-9_]{1,31}$`)

// Validate enforces invariants on a loaded manifest. Modules with invalid IDs
// (wrong characters, collision with core routes) are rejected at load time.
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
	if strings.TrimSpace(m.Entry) == "" {
		m.Entry = "index.js"
	}
	if strings.TrimSpace(m.Version) == "" {
		m.Version = "0.0.0"
	}
	return nil
}

// LoadManifest reads and validates a manifest from the given directory.
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

// ReservedRoutes lists route names that cannot be used as module IDs to avoid
// shadowing core infrastructure. Balancer route names are allowed so a JS
// module can override a built-in (router gives JS precedence when enabled).
var ReservedRoutes = map[string]struct{}{
	"events": {},
}

// IsReserved reports whether an ID collides with a built-in balancer.
func IsReserved(id string) bool {
	_, ok := ReservedRoutes[strings.ToLower(id)]
	return ok
}
