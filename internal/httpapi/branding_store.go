package httpapi

import (
	stdjson "encoding/json"
	"os"
	"path/filepath"
	"sync"

	"lampac-go/internal/config"

	"github.com/rs/zerolog/log"
)

// brandingStoreMu guards atomic write of database/branding.json — concurrent
// admin saves are serialised so the persisted file always reflects the last
// committed Branding.
var brandingStoreMu sync.Mutex

// brandingStorePath returns the on-disk path of the runtime override JSON.
func brandingStorePath(repoRoot string) string {
	return filepath.Join(repoRoot, "database", "branding.json")
}

// loadBrandingOverride reads database/branding.json. Missing file is not an
// error — returns zero-value Branding so caller can decide what to do.
func loadBrandingOverride(repoRoot string) (Branding, bool, error) {
	path := brandingStorePath(repoRoot)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Branding{}, false, nil
		}
		return Branding{}, false, err
	}
	var b Branding
	if err := stdjson.Unmarshal(data, &b); err != nil {
		return Branding{}, false, err
	}
	return b, true, nil
}

// saveBrandingOverride writes the JSON override atomically (tmp+rename) and
// updates the in-memory branding store. Pass an empty Branding to remove all
// overrides — defaults will take over on next read.
func saveBrandingOverride(repoRoot string, b Branding) error {
	brandingStoreMu.Lock()
	defer brandingStoreMu.Unlock()

	path := brandingStorePath(repoRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := stdjson.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	SetBranding(b)
	return nil
}

// applyBrandingFromConfig layers config.toml [branding] (then JSON override
// if present) onto the runtime store. Safe to call at boot AND on hot-reload.
//
// Layering order (lowest → highest precedence):
//  1. Built-in defaults (in branding.go's brandingDefaults).
//  2. config.toml [branding] section.
//  3. database/branding.json runtime override.
//
// Errors loading the JSON are logged but never fatal — the previous in-memory
// state survives.
func applyBrandingFromConfig(cfg config.Config) {
	merged := brandingFromConfig(cfg.Branding)
	SetBranding(merged)

	repoRoot := cfg.Compat.RepoRoot
	if repoRoot == "" {
		repoRoot = "."
	}
	override, ok, err := loadBrandingOverride(repoRoot)
	if err != nil {
		log.Warn().Err(err).Str("path", brandingStorePath(repoRoot)).Msg("branding: failed to read override")
		return
	}
	if !ok {
		return
	}
	// SetBranding merges field-by-field with defaults, but here we want the
	// JSON override to take precedence over the TOML defaults we just set.
	// Combine the two layers explicitly.
	final := merged
	if v := override.Name; v != "" {
		final.Name = v
	}
	if v := override.Version; v != "" {
		final.Version = v
	}
	if override.Major > 0 {
		final.Major = override.Major
	}
	if override.Minor > 0 {
		final.Minor = override.Minor
	}
	if v := override.OnlineNameRU; v != "" {
		final.OnlineNameRU = v
	}
	if v := override.OnlineNameUK; v != "" {
		final.OnlineNameUK = v
	}
	if v := override.OnlineNameEN; v != "" {
		final.OnlineNameEN = v
	}
	if v := override.OnlineNameZH; v != "" {
		final.OnlineNameZH = v
	}
	if v := override.HTMLTitleV2; v != "" {
		final.HTMLTitleV2 = v
	}
	if v := override.HTMLTitleLite; v != "" {
		final.HTMLTitleLite = v
	}
	SetBranding(final)
}

// brandingFromConfig converts a TOML [branding] section into a Branding
// struct. Empty config fields stay empty so SetBranding can fall back to
// built-in defaults.
func brandingFromConfig(c config.BrandingConfig) Branding {
	return Branding{
		Name:          c.Name,
		Version:       c.Version,
		Major:         c.Major,
		Minor:         c.Minor,
		OnlineNameRU:  c.OnlineNameRU,
		OnlineNameUK:  c.OnlineNameUK,
		OnlineNameEN:  c.OnlineNameEN,
		OnlineNameZH:  c.OnlineNameZH,
		HTMLTitleV2:   c.HTMLTitleV2,
		HTMLTitleLite: c.HTMLTitleLite,
	}
}
