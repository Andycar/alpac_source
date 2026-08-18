// Package envpresets manages "environment presets" — distribution units
// that ship configuration files (PAC, systemd unit files, helper scripts)
// rather than runnable JS modules.
//
// Lifecycle:
//
//	hub.alcopa.cc → ZIP → Install (download, extract, render templates,
//	                                copy files, run post_install)
//	                  → Configure (edit per-preset .config.json with
//	                                user values, re-render + re-copy)
//	                  → Uninstall (remove tracked files, run post_uninstall)
//
// Each installed preset lives under {repoRoot}/env_presets/{id}/:
//
//	manifest.json    — copy of the original manifest (for inspection)
//	src/             — files extracted from the ZIP, untouched
//	.config.json     — user-applied parameter values (params from manifest)
//	.installed.json  — list of {dst,sha256} for clean uninstall
package envpresets

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Manifest mirrors hub-side server.Manifest's env-preset fields plus core
// id/name/version. Loaded from manifest.json on install.
type Manifest struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Author      string `json:"author,omitempty"`
	Description string `json:"description,omitempty"`
	ModuleType  string `json:"module_type,omitempty"`
	Tags        []string `json:"tags,omitempty"`

	Files         []FileSpec  `json:"files"`
	Params        []ParamSpec `json:"params,omitempty"`
	Deps          *Deps       `json:"deps,omitempty"`
	PostInstall   []string    `json:"post_install,omitempty"`
	PostUninstall []string    `json:"post_uninstall,omitempty"`
	Ports         []int       `json:"ports,omitempty"`
	RequiresRoot  bool        `json:"requires_root,omitempty"`
}

// FileSpec — куда положить какой entry из ZIP.
type FileSpec struct {
	Src       string `json:"src"`
	Dst       string `json:"dst"`
	Mode      string `json:"mode,omitempty"`     // "0644" | "0755"
	Template  bool   `json:"template,omitempty"` // если true — рендерим {{key}}
	NeedsRoot bool   `json:"needs_root,omitempty"`
}

// ParamSpec — типизированный параметр пресета.
type ParamSpec struct {
	Key         string   `json:"key"`
	Label       string   `json:"label,omitempty"`
	Type        string   `json:"type,omitempty"` // string | int | bool | enum | secret
	Default     any      `json:"default,omitempty"`
	Description string   `json:"description,omitempty"`
	Min         *int     `json:"min,omitempty"`
	Max         *int     `json:"max,omitempty"`
	Options     []string `json:"options,omitempty"`
	Required    bool     `json:"required,omitempty"`
}

// Deps — внешние зависимости.
type Deps struct {
	Apt     []string `json:"apt,omitempty"`
	Pip     []string `json:"pip,omitempty"`
	Systemd []string `json:"systemd,omitempty"`
}

var (
	idRe       = regexp.MustCompile(`^[a-z][a-z0-9_]{1,31}$`)
	paramKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,31}$`)
)

// Validate enforces invariants on a manifest. Mirrors hub-side validation but
// runs on the lampac-go install side too (defence in depth).
func (m *Manifest) Validate() error {
	if !idRe.MatchString(m.ID) {
		return fmt.Errorf("manifest.id %q must match ^[a-z][a-z0-9_]{1,31}$", m.ID)
	}
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("manifest.name is required")
	}
	if strings.TrimSpace(m.Version) == "" {
		return fmt.Errorf("manifest.version is required")
	}
	if len(m.Files) == 0 {
		return fmt.Errorf("env-preset must declare files[] (at least one)")
	}
	keys := map[string]bool{}
	for _, p := range m.Params {
		if !paramKeyRe.MatchString(p.Key) {
			return fmt.Errorf("param key %q must match ^[a-z][a-z0-9_]{1,31}$", p.Key)
		}
		if keys[p.Key] {
			return fmt.Errorf("duplicate param key %q", p.Key)
		}
		keys[p.Key] = true
	}
	for i, f := range m.Files {
		if f.Src == "" || f.Dst == "" {
			return fmt.Errorf("file[%d]: src and dst are required", i)
		}
	}
	return nil
}

// LoadManifest reads manifest.json from a preset directory.
func LoadManifest(dir string) (*Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest.json: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// SaveManifest writes manifest.json (admin may edit it).
func SaveManifest(dir string, m *Manifest) error {
	if err := m.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o644)
}

// InstalledFile — запись в .installed.json: какой dst-путь был создан и его
// текущий sha256 (для проверки что файл не был изменён вручную перед uninstall).
type InstalledFile struct {
	Dst    string `json:"dst"`
	SHA256 string `json:"sha256,omitempty"`
	Mode   string `json:"mode,omitempty"`
}

// InstallState — что лежит в .installed.json.
type InstallState struct {
	Version string          `json:"version"` // версия установленного пресета
	Files   []InstalledFile `json:"files"`
	// Config — копия .config.json в момент установки (на случай если юзер
	// потом перезапишет config).
	Config map[string]any `json:"config,omitempty"`
}
