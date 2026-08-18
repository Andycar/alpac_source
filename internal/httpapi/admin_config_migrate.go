package httpapi

import (
	stdjson "encoding/json"
	"maps"
	"os"
	"strings"
)

// admin_config_migrate.go — shared config-migration helpers that used to live in
// admin_manifest.go. They stay in httpapi because non-admin code depends on them
// (sync_cron → applyLegacyMapToTOML, migrate_api → fileExists, admin_inspector →
// updateInitConfMap/ensureMapChild) and they back the internal/adminhttp seam
// (UpdateInitConfMap / ApplyLegacyMapToTOML injections). The /admin/manifest
// install handler itself moved to internal/adminhttp.

// updateInitConfMap performs a read-modify-write on config.toml. The modify
// callback receives a map keyed by legacy JSON-style names (e.g., "LampaWeb",
// "TelegramAuth") for backward compatibility. Changes are mapped back to TOML
// before saving. If the server isn't wired yet (first-time setup / manifest
// install), falls back to writing init.conf JSON.
func updateInitConfMap(modify func(root map[string]any)) error {
	if !serverReady() {
		root := map[string]any{}
		if data, ok := readFileAny("init.conf"); ok {
			raw := strings.TrimSpace(string(data))
			if raw != "" {
				if !strings.HasPrefix(raw, "{") {
					raw = "{" + raw + "}"
				}
				if err := stdjson.Unmarshal([]byte(raw), &root); err != nil {
					root = map[string]any{}
				}
			}
		}
		modify(root)
		return writePrettyJSON("init.conf", root)
	}

	legacyRoot := loadMergedConf()
	modify(legacyRoot)
	return updateConfigTOMLMap(func(tomlRoot map[string]any) {
		applyLegacyMapToTOML(legacyRoot, tomlRoot)
	})
}

// applyLegacyMapToTOML applies changes from a legacy JSON-keyed map back into the
// TOML-native nested structure.
func applyLegacyMapToTOML(legacyRoot, tomlRoot map[string]any) {
	for jsonKey, tomlPath := range jsonToTOMLPath {
		legacySection, ok := legacyRoot[jsonKey].(map[string]any)
		if !ok {
			continue
		}

		// Special case: LampaWeb → web.plugins
		if jsonKey == "LampaWeb" {
			if initPlugins, ok := legacySection["initPlugins"].(map[string]any); ok {
				target := setNestedMap(tomlRoot, "web.plugins")
				maps.Copy(target, initPlugins)
			}
			continue
		}

		// Special case: LLM — JSON uses camelCase, TOML uses snake_case.
		if jsonKey == "LLM" {
			target := setNestedMap(tomlRoot, tomlPath)
			for k, v := range legacySection {
				switch k {
				case "apiKey":
					target["api_key"] = v
				case "maxRetries":
					target["max_retries"] = v
				default:
					target[k] = v
				}
			}
			continue
		}

		target := setNestedMap(tomlRoot, tomlPath)
		maps.Copy(target, legacySection)
	}

	// Handle top-level scalar keys that don't map to sections.
	if v, ok := legacyRoot["disableEng"]; ok {
		tomlRoot["disable_eng"] = v
	}
}

func ensureMapChild(root map[string]any, key string) map[string]any {
	if child, ok := root[key].(map[string]any); ok && child != nil {
		return child
	}
	child := map[string]any{}
	root[key] = child
	return child
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}
