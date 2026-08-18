package httpapi

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

// CustomPlugin describes a user-uploaded JS plugin served at /{Name}.js.
type CustomPlugin struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"` // human-readable name (may contain Cyrillic)
	Enabled     bool   `json:"enabled"`
	Autoload    bool   `json:"autoload"`         // auto-inject into on.js at startup
	Public      bool   `json:"public"`           // show in Lampa extensions store
	Descr       string `json:"descr,omitempty"`  // description for store
	Author      string `json:"author,omitempty"` // author for store
	Image       string `json:"image,omitempty"`  // image URL for store

	// Community plugin fields (empty for user-uploaded plugins).
	IsCommunity      bool   `json:"is_community,omitempty"`
	CommunitySource  string `json:"community_source,omitempty"`  // source_url from catalog
	CommunityVersion string `json:"community_version,omitempty"` // installed version string
	CommunityHash    string `json:"community_hash,omitempty"`    // SHA-256 of installed .js
}

// CustomPluginRegistry manages user-uploaded JS plugins that are served
// dynamically without a server restart. Files live in {dir}/*.js and
// metadata is persisted in {dir}/_registry.json.
type CustomPluginRegistry struct {
	mu      sync.RWMutex
	plugins map[string]CustomPlugin // key: plugin name (without .js)
	dir     string                  // absolute path to plugins/custom/
}

// NewCustomPluginRegistry creates or loads the registry from dir.
// If dir doesn't exist, it is created.
func NewCustomPluginRegistry(dir string) *CustomPluginRegistry {
	r := &CustomPluginRegistry{
		plugins: make(map[string]CustomPlugin),
		dir:     dir,
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Warn().Err(err).Str("dir", dir).Msg("customplugins: cannot create dir")
		return r
	}
	r.load()
	return r
}

// load reads _registry.json from disk into memory.
func (r *CustomPluginRegistry) load() {
	data, err := os.ReadFile(filepath.Join(r.dir, "_registry.json"))
	if err != nil {
		// First run or missing file — scan directory for .js files.
		r.scanDir()
		return
	}
	var list []CustomPlugin
	if err := json.Unmarshal(data, &list); err != nil {
		log.Warn().Err(err).Msg("customplugins: bad _registry.json")
		r.scanDir()
		return
	}
	for _, p := range list {
		r.plugins[p.Name] = p
	}
}

// scanDir discovers .js files already present in the directory.
func (r *CustomPluginRegistry) scanDir() {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".js") || e.Name() == "_registry.json" {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".js")
		if _, ok := r.plugins[name]; !ok {
			r.plugins[name] = CustomPlugin{Name: name, Enabled: true}
		}
	}
	r.saveLocked()
}

// save persists the registry to _registry.json. Must be called with mu held.
func (r *CustomPluginRegistry) saveLocked() {
	list := make([]CustomPlugin, 0, len(r.plugins))
	for _, p := range r.plugins {
		list = append(list, p)
	}
	data, _ := json.MarshalIndent(list, "", "  ")
	_ = os.WriteFile(filepath.Join(r.dir, "_registry.json"), data, 0o644)
}

// UpdateMeta updates metadata fields (public, descr, author) for a plugin.
func (r *CustomPluginRegistry) UpdateMeta(name string, public bool, descr, author string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	p, ok := r.plugins[name]
	if !ok {
		return
	}
	p.Public = public
	p.Descr = descr
	p.Author = author
	r.plugins[name] = p
	r.saveLocked()
}

// PublicPlugins returns enabled plugins marked as public.
func (r *CustomPluginRegistry) PublicPlugins() []CustomPlugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []CustomPlugin
	for _, p := range r.plugins {
		if p.Enabled && p.Public {
			out = append(out, p)
		}
	}
	return out
}

// Register saves a JS plugin to disk and adds it to the registry.
// displayName is the human-readable name (may contain Cyrillic); name is the ASCII filesystem key.
func (r *CustomPluginRegistry) Register(name, displayName string, content []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	path := filepath.Join(r.dir, name+".js")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return err
	}
	r.plugins[name] = CustomPlugin{Name: name, DisplayName: displayName, Enabled: true}
	r.saveLocked()
	log.Info().Str("name", name).Str("display", displayName).Msg("customplugins: registered")
	return nil
}

// Unregister removes a plugin from disk and registry.
func (r *CustomPluginRegistry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	_ = os.Remove(filepath.Join(r.dir, name+".js"))
	r.deleteImageLocked(name)
	delete(r.plugins, name)
	r.saveLocked()
	log.Info().Str("name", name).Msg("customplugins: unregistered")
}

// SetEnabled toggles a plugin on or off.
func (r *CustomPluginRegistry) SetEnabled(name string, enabled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	p, ok := r.plugins[name]
	if !ok {
		return
	}
	p.Enabled = enabled
	r.plugins[name] = p
	r.saveLocked()
}

// SetAutoload toggles auto-injection of a plugin into on.js.
func (r *CustomPluginRegistry) SetAutoload(name string, autoload bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	p, ok := r.plugins[name]
	if !ok {
		return
	}
	p.Autoload = autoload
	r.plugins[name] = p
	r.saveLocked()
}

// AutoloadPlugins returns enabled plugins marked for auto-loading via on.js.
func (r *CustomPluginRegistry) AutoloadPlugins() []CustomPlugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []CustomPlugin
	for _, p := range r.plugins {
		if p.Enabled && p.Autoload {
			out = append(out, p)
		}
	}
	return out
}

// List returns all registered plugins.
func (r *CustomPluginRegistry) List() []CustomPlugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]CustomPlugin, 0, len(r.plugins))
	for _, p := range r.plugins {
		out = append(out, p)
	}
	return out
}

// ListAny is List boxed as `any` so the admin handler in internal/adminhttp can
// serialize the plugin list through the CustomPlugins interface without needing
// the internal CustomPlugin type.
func (r *CustomPluginRegistry) ListAny() any { return r.List() }

// SaveImage saves an image file for a plugin and updates the Image field.
// ext should include the dot (e.g. ".png", ".jpg").
func (r *CustomPluginRegistry) SaveImage(name string, data []byte, ext string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	p, ok := r.plugins[name]
	if !ok {
		return os.ErrNotExist
	}

	// Remove old image if extension changed.
	r.deleteImageLocked(name)

	path := filepath.Join(r.dir, name+".img"+ext)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}

	p.Image = name + ".img" + ext
	r.plugins[name] = p
	r.saveLocked()
	return nil
}

// DeleteImage removes the image file for a plugin.
func (r *CustomPluginRegistry) DeleteImage(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.deleteImageLocked(name)

	p, ok := r.plugins[name]
	if !ok {
		return
	}
	p.Image = ""
	r.plugins[name] = p
	r.saveLocked()
}

func (r *CustomPluginRegistry) deleteImageLocked(name string) {
	// Remove any existing image file matching pattern name.img.*
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return
	}
	prefix := name + ".img."
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), prefix) {
			_ = os.Remove(filepath.Join(r.dir, e.Name()))
		}
	}
}

// ImagePath returns the full filesystem path of a plugin image, or empty if none.
func (r *CustomPluginRegistry) ImagePath(filename string) string {
	// Validate filename to prevent path traversal.
	if strings.Contains(filename, "/") || strings.Contains(filename, "\\") || strings.HasPrefix(filename, ".") {
		return ""
	}
	if !strings.Contains(filename, ".img.") {
		return ""
	}
	path := filepath.Join(r.dir, filename)
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

// resolvePluginImageURL converts a plugin Image field to a full URL.
// If already a full URL (http/https), returns as-is. If a local filename, builds URL from host.
func resolvePluginImageURL(image, host string) string {
	if image == "" {
		return ""
	}
	if strings.HasPrefix(image, "http://") || strings.HasPrefix(image, "https://") {
		return image
	}
	return host + "/customplugins/image/" + image
}

// ServePlugin serves a custom JS plugin if it exists and is enabled.
// Returns true if the plugin was found and served.
// tokenOverride is used when chi.URLParam is unavailable (e.g. notFoundHandler).
func (r *CustomPluginRegistry) ServePlugin(name string, w http.ResponseWriter, req *http.Request, tokenOverride ...string) bool {
	r.mu.RLock()
	p, ok := r.plugins[name]
	r.mu.RUnlock()
	if !ok || !p.Enabled {
		return false
	}

	data, err := os.ReadFile(filepath.Join(r.dir, name+".js"))
	if err != nil {
		return false
	}

	host := hostFromRequest(req)
	token := chi.URLParam(req, "token")
	if token == "" && len(tokenOverride) > 0 {
		token = tokenOverride[0]
	}

	out := string(data)
	out = strings.ReplaceAll(out, "{localhost}", host)
	out = strings.ReplaceAll(out, "{token}", url.QueryEscape(token))
	out = applyPublicBrandingJS(out)

	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(out))
	return true
}

// ---------------------------------------------------------------------------
//  Community plugin helpers
// ---------------------------------------------------------------------------

// SetCommunityMeta updates the community-specific fields for a plugin.
func (r *CustomPluginRegistry) SetCommunityMeta(name, sourceURL, version, hash string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	p, ok := r.plugins[name]
	if !ok {
		return
	}
	p.IsCommunity = true
	p.CommunitySource = sourceURL
	p.CommunityVersion = version
	p.CommunityHash = hash
	r.plugins[name] = p
	r.saveLocked()
}

// ReplaceContent overwrites the .js file without changing metadata flags
// (Enabled, Autoload, Public, etc.). Used for community plugin updates.
func (r *CustomPluginRegistry) ReplaceContent(name string, content []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.plugins[name]; !ok {
		return os.ErrNotExist
	}
	path := filepath.Join(r.dir, name+".js")
	return os.WriteFile(path, content, 0o644)
}

// CommunityPlugins returns only community-installed plugins.
func (r *CustomPluginRegistry) CommunityPlugins() []CustomPlugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []CustomPlugin
	for _, p := range r.plugins {
		if p.IsCommunity {
			out = append(out, p)
		}
	}
	return out
}
