package httpapi

import (
	"fmt"
	"maps"
	"os"
	"strings"
	"sync"

	"lampac-go/internal/config"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/rs/zerolog/log"
)

// --- Admin panel shared helpers (TOML-only) ---

// tomlWriteMu serializes read-modify-write cycles on config.toml.
var tomlWriteMu sync.Mutex

// loadConfigTOMLAsMap reads config.toml and returns the entire file as
// a nested map[string]any. Returns an empty map if the file does not exist
// or cannot be parsed.
func loadConfigTOMLAsMap() map[string]any {
	if !serverReady() {
		return map[string]any{}
	}
	cfg := liveConfig(config.Config{})
	tomlPath := cfg.TOMLPath
	if tomlPath == "" {
		tomlPath = config.TOMLFilePath(cfg.Compat.RepoRoot)
	}
	data, err := os.ReadFile(tomlPath)
	if err != nil {
		log.Warn().Err(err).Msg("admin: failed to read config.toml")
		return map[string]any{}
	}
	var root map[string]any
	if err := toml.Unmarshal(data, &root); err != nil {
		log.Warn().Err(err).Msg("admin: failed to parse config.toml")
		return map[string]any{}
	}
	return root
}

// saveConfigTOMLFromMap marshals root to TOML, writes config.toml atomically,
// and triggers a hot-reload of the running config.
func saveConfigTOMLFromMap(root map[string]any) error {
	if !serverReady() {
		return fmt.Errorf("server not initialized")
	}
	tomlWriteMu.Lock()
	defer tomlWriteMu.Unlock()

	cfg := liveConfig(config.Config{})
	tomlPath := cfg.TOMLPath
	if tomlPath == "" {
		tomlPath = config.TOMLFilePath(cfg.Compat.RepoRoot)
	}

	data, err := toml.Marshal(root)
	if err != nil {
		return fmt.Errorf("marshal TOML: %w", err)
	}
	data = config.SanitizeTOMLInts(data)

	// Atomic write via temp file.
	tmp := tomlPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, tomlPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}

	// Hot-reload config.
	if err := reloadServer(); err != nil {
		log.Warn().Err(err).Msg("admin: hot-reload failed after config.toml save")
	}

	return nil
}

// updateConfigTOMLMap performs an atomic read-modify-write cycle on config.toml.
// The modify callback receives the full TOML map to mutate in place.
func updateConfigTOMLMap(modify func(root map[string]any)) error {
	if !serverReady() {
		return fmt.Errorf("server not initialized")
	}
	tomlWriteMu.Lock()
	defer tomlWriteMu.Unlock()

	cfg := liveConfig(config.Config{})
	tomlPath := cfg.TOMLPath
	if tomlPath == "" {
		tomlPath = config.TOMLFilePath(cfg.Compat.RepoRoot)
	}

	root := map[string]any{}
	if data, err := os.ReadFile(tomlPath); err == nil {
		_ = toml.Unmarshal(data, &root)
	}

	modify(root)

	data, err := toml.Marshal(root)
	if err != nil {
		return fmt.Errorf("marshal TOML: %w", err)
	}
	data = config.SanitizeTOMLInts(data)

	tmp := tomlPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, tomlPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}

	if err := reloadServer(); err != nil {
		log.Warn().Err(err).Msg("admin: hot-reload failed after config.toml update")
	}

	// Invalidate the cached merged config so /lite/events picks up
	// balancer enable/disable changes immediately.
	invalidateMergedConfCache()

	return nil
}

// updateConfigTOMLMapNoReload is updateConfigTOMLMap minus the Reload() call.
// Use it when the caller already keeps the relevant in-memory state in sync
// itself and a full reload (which restarts proxy pools, cluster, etc.) would
// be way too heavy for the change.
func updateConfigTOMLMapNoReload(modify func(root map[string]any)) error {
	if !serverReady() {
		return fmt.Errorf("server not initialized")
	}
	tomlWriteMu.Lock()
	defer tomlWriteMu.Unlock()

	cfg := liveConfig(config.Config{})
	tomlPath := cfg.TOMLPath
	if tomlPath == "" {
		tomlPath = config.TOMLFilePath(cfg.Compat.RepoRoot)
	}

	root := map[string]any{}
	if data, err := os.ReadFile(tomlPath); err == nil {
		_ = toml.Unmarshal(data, &root)
	}

	modify(root)

	data, err := toml.Marshal(root)
	if err != nil {
		return fmt.Errorf("marshal TOML: %w", err)
	}
	data = config.SanitizeTOMLInts(data)

	tmp := tomlPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, tomlPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}

	invalidateMergedConfCache()
	return nil
}

// ---------------------------------------------------------------------------
//  JSON-key → TOML-path mapping for backward compatibility.
//
//  Admin panel handlers historically use PascalCase JSON keys like "Rezka",
//  "TelegramAuth", "LampaWeb". TOML config uses lowercase nested keys like
//  [online.rezka], [telegram], [web.plugins].
//
//  The mapping below enables loadMergedConf() to return a map keyed by the
//  JSON-style names so that existing GET handlers continue working unchanged.
// ---------------------------------------------------------------------------

// jsonToTOMLPath maps legacy PascalCase admin JSON keys to their location in
// the TOML config hierarchy. Format: "toml_section.toml_subsection".
var jsonToTOMLPath = map[string]string{
	// Online balancers
	"Rezka":           "online.rezka",
	"Rhsprem":         "online.rhsprem",
	"Collaps":         "online.collaps",
	"Collaps-dash":    "online.collaps", // dash variant shares section
	"Kinotochka":      "online.kinotochka",
	"RutubeMovie":     "online.rutube_movie",
	"VkMovie":         "online.vk_movie",
	"Plvideo":         "online.plvideo",
	"CDNvideohub":     "online.cdnvideohub",
	"Kubikvkube":      "online.kubikvkube",
	"Redheadsound":    "online.redheadsound",
	"iRemux":          "online.iremux",
	"Mirkino":         "online.mirkino",
	"Zetflix":         "online.zetflix",
	"ZetflixDB":       "online.zetflixdb",
	"CDNmovies":       "online.cdnmovies",
	"VDBmovies":       "online.vdbmovies",
	"FanCDN":          "online.fancdn",
	"Kinobase":        "online.kinobase",
	"VideoCDN":        "online.videocdn",
	"Lumex":           "online.lumex",
	"VoKino":          "online.vokino",
	"IframeVideo":     "online.iframe_video",
	"HDVB":            "online.hdvb",
	"Vibix":           "online.vibix",
	"Videoseed":       "online.videoseed",
	"KinoPub":         "online.kinopub",
	"Alloha":          "online.alloha",
	"GetsTV":          "online.getstv",
	"Kodik":           "online.kodik",
	"Mirage":          "online.mirage",
	"Aladdin":         "online.aladdin",
	"Kinogo":          "online.kinogo",
	"Lift":            "online.lift",
	"UaKino":          "online.uakino",
	"Kinovod":         "online.kinovod",
	"Turbo":           "online.turbo",
	"SakhTV":          "online.sakhtv",
	"FEMD":            "online.femd",
	"Gencit":          "online.gencit",
	"Kinobadi":        "online.kinobadi",
	"FlixCDN":         "online.flixcdn",
	"TwoEmbed":        "online.twoembed",
	"Uafilm":          "online.uafilm",
	"Youtube":         "youtube",
	"IptvOnline":      "online.iptv_online",
	"Filmix":          "online.filmix",
	"FilmixPartner":   "online.filmix_partner",
	"MoonAnime":       "online.moonanime",
	"AnilibriaOnline": "online.anilibria",
	"AniLiberty":      "online.aniliberty",
	"Animebesst":      "online.animebesst",
	"AniMedia":        "online.animedia",
	"Animevost":       "online.animevost",
	"AnimeGo":         "online.animego",
	"AnimeLib":        "online.animelib",
	"Kinoukr":         "online.kinoukr",
	"VideoDB":         "online.videodb",
	"Ashdi":           "online.ashdi",
	"Eneyida":         "online.eneyida",
	"Bamboo":          "online.bamboo",
	"Unimay":          "online.unimay",
	"StarLight":       "online.starlight",
	"KlonFUN":         "online.klonfun",
	"Uaflix":          "online.uaflix",
	"AnimeON":         "online.animeon",
	"Mikai":           "online.mikai",
	"VeoVeo":          "online.veoveo",
	"PidTor":          "online.pidtor",
	"Hydraflix":       "online.hydraflix",
	"Vidsrc":          "online.vidsrc",
	"VidLink":         "online.vidlink",
	"Videasy":         "online.videasy",
	"Autoembed":       "online.autoembed",
	"Rgshows":         "online.rgshows",
	"Zona":            "online.zona",
	// Non-online sections
	"LampaWeb":     "web",
	"TelegramAuth": "telegram",
	"TorrServer":   "torrserver",
	"ProxyVless":   "proxy.vless",
	"sisi":         "sisi",
	"sync":         "sync",
	"transcoding":  "transcoding",
	"kit":          "kit",
	"admin":        "admin",
	"accsdb":       "accsdb",
	"listen":       "server",
	"LLM":          "llm",
}

// balancerDefaults provides default field values for balancers that may not
// yet have a section in config.toml. Keyed by PascalCase admin name.
var balancerDefaults = map[string]map[string]any{
	"Bamboo":    {"host": "https://bambooua.com"},
	"Unimay":    {"host": "https://api.unimay.media/v1"},
	"StarLight": {"host": "https://tp-back.starlight.digital"},
	"KlonFUN":   {"host": "https://klon.fun"},
	"Uaflix":    {"host": "https://uafix.net"},
	"AnimeON":   {"host": "https://animeon.club"},
	"Mikai":     {"host": "https://api.mikai.me/v1"},
	"Zona":      {"host": "https://w1.zona.im", "split_balancers": false},
	"Mirkino":   {"host": "https://ru.mir-kino.pp.ru", "login": "", "passwd": "", "token": "", "user_id": ""},
}

// getNestedMap traverses root by dot-separated path and returns the map at
// that location, or nil if not found.
func getNestedMap(root map[string]any, path string) map[string]any {
	parts := strings.Split(path, ".")
	current := root
	for _, p := range parts {
		child, ok := current[p].(map[string]any)
		if !ok {
			return nil
		}
		current = child
	}
	return current
}

// setNestedMap ensures all intermediate maps exist and returns the deepest one.
func setNestedMap(root map[string]any, path string) map[string]any {
	parts := strings.Split(path, ".")
	current := root
	for _, p := range parts {
		child, ok := current[p].(map[string]any)
		if !ok {
			child = map[string]any{}
			current[p] = child
		}
		current = child
	}
	return current
}

// ---------------------------------------------------------------------------
//  Backward-compatible functions used by admin panel handlers.
//  These were previously reading/writing init.conf JSON;
//  now they operate on config.toml.
// ---------------------------------------------------------------------------

// loadMergedConf returns a map keyed by legacy JSON names (e.g. "Rezka",
// "TelegramAuth") with values pulled from config.toml nested structure.
// This lets existing GET handlers work without changes.
func loadMergedConf() map[string]any {
	tomlRoot := loadConfigTOMLAsMap()
	result := map[string]any{}

	for jsonKey, tomlPath := range jsonToTOMLPath {
		section := getNestedMap(tomlRoot, tomlPath)
		if section != nil {
			// Make a shallow copy.
			cp := make(map[string]any, len(section))
			maps.Copy(cp, section)
			// Merge defaults for missing fields (e.g. host not in TOML).
			if defaults, ok := balancerDefaults[jsonKey]; ok {
				for k, v := range defaults {
					if _, exists := cp[k]; !exists {
						cp[k] = v
					}
				}
			}
			result[jsonKey] = cp
		} else if defaults, ok := balancerDefaults[jsonKey]; ok {
			// Balancer not in config.toml yet — use built-in defaults.
			cp := make(map[string]any, len(defaults))
			maps.Copy(cp, defaults)
			result[jsonKey] = cp
		}
	}

	// Special case: LLM — TOML uses snake_case, JSON/admin expects camelCase.
	if llm, ok := result["LLM"].(map[string]any); ok {
		if v, ok := llm["api_key"]; ok {
			llm["apiKey"] = v
			delete(llm, "api_key")
		}
		if v, ok := llm["max_retries"]; ok {
			llm["maxRetries"] = v
			delete(llm, "max_retries")
		}
	}

	// Special case: LampaWeb needs initPlugins sub-key from web.plugins.
	if webSection := getNestedMap(tomlRoot, "web"); webSection != nil {
		lw := make(map[string]any)
		if plugins := getNestedMap(tomlRoot, "web.plugins"); plugins != nil {
			pluginsCopy := make(map[string]any, len(plugins))
			maps.Copy(pluginsCopy, plugins)
			lw["initPlugins"] = pluginsCopy
		}
		result["LampaWeb"] = lw
	}

	return result
}

// loadInitConf returns the full TOML config as a TOML-native map (no key mapping).
// Used where callers need direct TOML key access.
func loadInitConf() map[string]any {
	return loadConfigTOMLAsMap()
}

// saveInitConf writes the entire map as config.toml.
func saveInitConf(root map[string]any) error {
	return saveConfigTOMLFromMap(root)
}

// updateInitJSONToken updates a token field for a specific balancer in config.toml.
// name is the PascalCase balancer name (e.g., "Filmix").
func updateInitJSONToken(name, token string) {
	tomlPath, ok := jsonToTOMLPath[name]
	if !ok {
		log.Warn().Str("name", name).Msg("admin: unknown balancer for token update")
		return
	}
	if err := updateConfigTOMLMap(func(root map[string]any) {
		section := setNestedMap(root, tomlPath)
		section["token"] = token
	}); err != nil {
		log.Warn().Err(err).Str("name", name).Msg("admin: failed to update token in config.toml")
	}
}

// ---------------------------------------------------------------------------
//  Utility converters
// ---------------------------------------------------------------------------

// toBoolAny converts any value to bool.
func toBoolAny(v any) bool {
	if v == nil {
		return false
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true" || t == "1"
	default:
		return false
	}
}

// toStringAny converts any value to string.
func toStringAny(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return fmt.Sprintf("%v", t)
	case int64:
		return fmt.Sprintf("%d", t)
	default:
		return fmt.Sprint(v)
	}
}

func toFloat64Any(v any) (float64, bool) {
	if v == nil {
		return 0, false
	}
	switch t := v.(type) {
	case float64:
		return t, true
	case int64:
		return float64(t), true
	case int:
		return float64(t), true
	default:
		return 0, false
	}
}
