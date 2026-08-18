package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/rs/zerolog/log"
)

// ---------------------------------------------------------------------------
//  Community plugin catalog types
// ---------------------------------------------------------------------------

// CatalogEntry represents one plugin from the remote community catalog.
type CatalogEntry struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name"`
	Description string   `json:"description"`
	Author      string   `json:"author"`
	Version     string   `json:"version"`
	SourceURL   string   `json:"source_url"`
	Homepage    string   `json:"homepage"`
	ImageURL    string   `json:"image_url"`
	Category    string   `json:"category"`
	Tags        []string `json:"tags"`
}

// CatalogResponse is the top-level JSON returned by the community plugins URL.
type CatalogResponse struct {
	Version   int            `json:"version"`
	UpdatedAt string         `json:"updated_at"`
	Plugins   []CatalogEntry `json:"plugins"`
}

// CommunityPluginStatus combines catalog and local installation data for the UI.
type CommunityPluginStatus struct {
	CatalogEntry
	Installed        bool   `json:"installed"`
	InstalledVersion string `json:"installed_version,omitempty"`
	UpdateAvailable  bool   `json:"update_available"`
	Enabled          bool   `json:"enabled"`
	Autoload         bool   `json:"autoload"`
}

// ---------------------------------------------------------------------------
//  Catalog fetch
// ---------------------------------------------------------------------------

var communityHTTPClient = &http.Client{Timeout: 15 * time.Second}

// fetchCatalog downloads and parses the remote community plugin catalog.
func fetchCatalog(catalogURL string) (*CatalogResponse, error) {
	if catalogURL == "" {
		return &CatalogResponse{}, nil
	}

	resp, err := communityHTTPClient.Get(catalogURL)
	if err != nil {
		return nil, fmt.Errorf("fetch catalog: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("catalog returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20)) // 5 MB limit
	if err != nil {
		return nil, fmt.Errorf("read catalog: %w", err)
	}

	var catalog CatalogResponse
	if err := json.Unmarshal(body, &catalog); err != nil {
		return nil, fmt.Errorf("parse catalog: %w", err)
	}
	return &catalog, nil
}

// ---------------------------------------------------------------------------
//  Install / Update / Status
// ---------------------------------------------------------------------------

// downloadPluginJS downloads the JS content from a source URL.
func downloadPluginJS(sourceURL string) ([]byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(sourceURL)
	if err != nil {
		return nil, fmt.Errorf("download plugin: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("plugin source returned %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20)) // 5 MB limit
	if err != nil {
		return nil, fmt.Errorf("read plugin: %w", err)
	}
	return data, nil
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// installCommunityPlugin downloads JS from the catalog entry and registers it.
// Runs the static validator first; refuses install if validator returns OK=false
// unless ForceInstall is true (set via ?force=1 in admin API).
func installCommunityPlugin(registry *CustomPluginRegistry, entry CatalogEntry) error {
	return installCommunityPluginWithOptions(registry, entry, false)
}

// installCommunityPluginWithOptions exposes a force-install path for admins
// who want to override validator failures. The default `false` matches the
// original install path used by the cron and one-click install endpoints.
func installCommunityPluginWithOptions(registry *CustomPluginRegistry, entry CatalogEntry, force bool) error {
	content, err := downloadPluginJS(entry.SourceURL)
	if err != nil {
		return err
	}

	// Sandbox validator — refuses install on hard failures unless overridden.
	val := ValidatePluginSource(entry.Name, content)
	if !val.OK && !force {
		// Compose a compact error mentioning the first failing finding.
		first := "validation failed"
		for _, f := range val.Findings {
			if f.Severity == "fail" {
				first = f.Code + ": " + f.Message
				break
			}
		}
		log.Warn().Str("name", entry.Name).Str("first_fail", first).
			Int("findings", len(val.Findings)).
			Msg("community: install rejected by validator")
		return fmt.Errorf("plugin validation failed (%s); use force=1 to override", first)
	}
	for _, f := range val.Findings {
		if f.Severity == "warn" {
			log.Warn().Str("name", entry.Name).Str("code", f.Code).Str("msg", f.Message).Msg("community: validator warning")
		}
	}

	if err := registry.Register(entry.Name, entry.DisplayName, content); err != nil {
		return fmt.Errorf("register: %w", err)
	}

	// Set community metadata.
	registry.SetCommunityMeta(entry.Name, entry.SourceURL, entry.Version, sha256Hex(content))

	// Set description, author, image from catalog.
	registry.UpdateMeta(entry.Name, false, entry.Description, entry.Author)

	log.Info().Str("name", entry.Name).Str("version", entry.Version).Msg("community: plugin installed")

	// Notify admins of the new install (best-effort, non-blocking).
	notifyAdminsPluginInstalled(entry, val)

	return nil
}

// updateCommunityPlugin re-downloads the JS and replaces it if content changed.
// Runs validator and refuses update on hard failures (auto-update path) — this
// stops a compromised catalog from auto-pushing malicious code to all users.
func updateCommunityPlugin(registry *CustomPluginRegistry, entry CatalogEntry) (bool, error) {
	content, err := downloadPluginJS(entry.SourceURL)
	if err != nil {
		return false, err
	}

	newHash := sha256Hex(content)
	var prevVersion string

	// Check if content actually changed.
	for _, p := range registry.CommunityPlugins() {
		if p.Name == entry.Name {
			prevVersion = p.CommunityVersion
			if p.CommunityHash == newHash {
				return false, nil // no change
			}
		}
	}

	val := ValidatePluginSource(entry.Name, content)
	if !val.OK {
		first := "validation failed"
		for _, f := range val.Findings {
			if f.Severity == "fail" {
				first = f.Code + ": " + f.Message
				break
			}
		}
		log.Error().Str("name", entry.Name).Str("first_fail", first).
			Msg("community: AUTO-UPDATE REJECTED — validator blocked malicious or broken plugin")
		notifyAdminsPluginUpdateBlocked(entry, val)
		return false, fmt.Errorf("auto-update blocked by validator: %s", first)
	}

	if err := registry.ReplaceContent(entry.Name, content); err != nil {
		return false, fmt.Errorf("replace: %w", err)
	}

	registry.SetCommunityMeta(entry.Name, entry.SourceURL, entry.Version, newHash)
	registry.UpdateMeta(entry.Name, false, entry.Description, entry.Author)

	log.Info().Str("name", entry.Name).Str("version", entry.Version).Msg("community: plugin updated")
	notifyAdminsPluginUpdated(entry, prevVersion, val)
	return true, nil
}

// buildCommunityStatus merges the catalog with installed plugin data for the UI.
func buildCommunityStatus(registry *CustomPluginRegistry, catalog *CatalogResponse) []CommunityPluginStatus {
	if catalog == nil {
		return nil
	}

	installed := map[string]CustomPlugin{}
	for _, p := range registry.List() {
		installed[p.Name] = p
	}

	out := make([]CommunityPluginStatus, 0, len(catalog.Plugins))
	for _, entry := range catalog.Plugins {
		st := CommunityPluginStatus{
			CatalogEntry: entry,
		}
		if p, ok := installed[entry.Name]; ok && p.IsCommunity {
			st.Installed = true
			st.InstalledVersion = p.CommunityVersion
			st.Enabled = p.Enabled
			st.Autoload = p.Autoload
			st.UpdateAvailable = p.CommunityVersion != entry.Version
		}
		out = append(out, st)
	}
	return out
}
