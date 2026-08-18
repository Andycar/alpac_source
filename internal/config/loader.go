package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/rs/zerolog/log"
)

// reWholeFloat matches TOML values like "= 9080.0" or "= -1001234567890.0"
// (whole-number floats) so they can be rewritten as plain integers.
// The TOML spec treats 9080.0 as float64, which pelletier/go-toml rejects
// when the target Go struct field is int.
//
// The pattern is intentionally conservative:
//   - Only touches lines with `key = [-]<digits>.0` (optionally followed by a comment).
//   - Leaves genuine floats (e.g., "temp = 0.1", "readrate = 1.6") untouched.
var reWholeFloat = regexp.MustCompile(`(?m)(=\s*)(-?\d+)\.0(\s*(?:#.*)?)$`)

// Load returns a fully populated Config by reading config files and applying
// environment variable overrides.
//
// Priority (highest to lowest):
//  1. Environment variables (LAMPAC_GO_*)
//  2. config.toml
func Load() (Config, error) {
	cfg := applyDefaults()

	// Apply env vars first to get LAMPAC_GO_REPO_ROOT (needed for file lookup).
	applyEnvOverrides(&cfg)

	repoRoot := cfg.Compat.RepoRoot

	loadTOML(repoRoot, &cfg)

	// Env vars always win (re-apply after file loading).
	applyEnvOverrides(&cfg)

	// Migrate legacy init.conf settings into TOML if missing.
	migrateFromInitConf(repoRoot, &cfg)

	// Post-processing: resolve derived values.
	applyPostProcessing(&cfg)

	return cfg, nil
}

// SanitizeTOMLInts rewrites whole-number floats (e.g., "port = 9080.0") as
// plain integers ("port = 9080") so the strict TOML parser doesn't reject
// float→int assignments.  Values like "temp = 0.1" are left untouched.
func SanitizeTOMLInts(data []byte) []byte {
	return reWholeFloat.ReplaceAll(data, []byte("${1}${2}${3}"))
}

// loadTOML tries to load config.toml from repoRoot or repoRoot/config.
// Returns true if a TOML file was found and loaded successfully.
func loadTOML(repoRoot string, cfg *Config) bool {
	for _, dir := range []string{repoRoot, filepath.Join(repoRoot, "config")} {
		tomlPath := filepath.Join(dir, "config.toml")
		data, err := os.ReadFile(tomlPath)
		if err != nil {
			continue
		}
		data = SanitizeTOMLInts(data)
		if err := toml.Unmarshal(data, cfg); err != nil {
			// Loud on purpose: one bad line (a pasted JSON-style `"key": value`
			// is the classic) makes the WHOLE file unreadable, and the server
			// then starts on bare defaults — no balancers, no tokens, no bot.
			// A quiet warning here reads like «настройка не сработала» instead
			// of «конфига нет вообще».
			log.Error().Err(err).Str("path", tomlPath).
				Msg("config: config.toml НЕ РАЗОБРАН — весь конфиг проигнорирован, сервер стартует на дефолтах. Проверьте синтаксис TOML (ключи вида key = value, без кавычек и двоеточий)")
			return false
		}
		// Backward compatibility: legacy installs used [kit] serverHost (camelCase)
		// where the canonical key is server_host. The bot only reads ServerHost,
		// so without this fallback /kit and /remote silently fail.
		if cfg.Kit.ServerHost == "" && cfg.Kit.ServerHostCamel != "" {
			cfg.Kit.ServerHost = cfg.Kit.ServerHostCamel
			log.Warn().Str("path", tomlPath).Msg("config: migrated [kit] serverHost → server_host (legacy key, please update config.toml)")
		}
		cfg.Kit.ServerHostCamel = ""
		cfg.TOMLPath = tomlPath
		log.Info().Str("path", tomlPath).Msg("config: loaded config.toml")
		return true
	}
	return false
}

// migrateFromInitConf reads legacy init.conf (JSON) and backfills settings into
// cfg that are missing from config.toml. This covers the TOML migration scenario
// where users upgrade lampac-go and their TG/bot settings only exist in init.conf.
func migrateFromInitConf(repoRoot string, cfg *Config) {
	// Only migrate if bot_token is empty in TOML (the key indicator).
	if cfg.TelegramAuth.BotToken != "" {
		return
	}

	// Try to read init.conf from several locations.
	var raw []byte
	for _, dir := range []string{repoRoot, filepath.Join(repoRoot, "config"), "."} {
		for _, name := range []string{"init.conf", "init.json"} {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err == nil && len(data) > 0 {
				raw = data
				break
			}
		}
		if raw != nil {
			break
		}
	}
	if raw == nil {
		return
	}

	// init.conf may lack outer braces.
	trimmed := strings.TrimSpace(string(raw))
	if trimmed != "" && !strings.HasPrefix(trimmed, "{") {
		trimmed = "{" + trimmed + "}"
	}

	var root map[string]any
	if json.Unmarshal([]byte(trimmed), &root) != nil {
		return
	}

	// Extract TelegramAuth section.
	tg, _ := root["TelegramAuth"].(map[string]any)
	if tg == nil {
		return
	}

	botToken, _ := tg["bot_token"].(string)
	if botToken == "" {
		return
	}

	log.Info().Msg("config: migrating TelegramAuth from init.conf → config.toml")

	cfg.TelegramAuth.BotToken = botToken

	if v, ok := tg["enable"].(bool); ok {
		cfg.TelegramAuth.Enable = v
	} else {
		// If there's a bot_token, enable by default.
		cfg.TelegramAuth.Enable = true
	}

	if v, ok := tg["admin_id"].(float64); ok && int64(v) > 0 {
		cfg.TelegramAuth.AdminID = int64(v)
	}

	if v, ok := tg["bot_name"].(string); ok && v != "" {
		cfg.TelegramAuth.BotName = v
	}

	if v, ok := tg["max_devices_per_user"].(float64); ok && int(v) != 0 {
		cfg.TelegramAuth.MaxDevicesPerUser = int(v)
	} else if v, ok := tg["max_devices"].(float64); ok && int(v) != 0 {
		// Alias: admin panel historically saves as "max_devices".
		cfg.TelegramAuth.MaxDevicesPerUser = int(v)
	}

	if v, ok := tg["auto_approve"].(bool); ok {
		cfg.TelegramAuth.AutoApprove = v
	}

	if v, ok := tg["auto_approve_days"].(float64); ok && int(v) > 0 {
		cfg.TelegramAuth.AutoApproveDays = int(v)
	}

	// Persist migrated settings to config.toml so this runs only once.
	tomlPath := TOMLFilePath(repoRoot)
	if tomlData, err := os.ReadFile(tomlPath); err == nil {
		var tomlRoot map[string]any
		if toml.Unmarshal(SanitizeTOMLInts(tomlData), &tomlRoot) == nil {
			tgSection, _ := tomlRoot["telegram"].(map[string]any)
			if tgSection == nil {
				tgSection = map[string]any{}
				tomlRoot["telegram"] = tgSection
			}
			tgSection["enable"] = cfg.TelegramAuth.Enable
			tgSection["bot_token"] = cfg.TelegramAuth.BotToken
			tgSection["admin_id"] = cfg.TelegramAuth.AdminID
			if cfg.TelegramAuth.BotName != "" {
				tgSection["bot_name"] = cfg.TelegramAuth.BotName
			}
			if cfg.TelegramAuth.MaxDevicesPerUser > 0 {
				tgSection["max_devices_per_user"] = cfg.TelegramAuth.MaxDevicesPerUser
			}
			if cfg.TelegramAuth.AutoApprove {
				tgSection["auto_approve"] = true
			}
			if cfg.TelegramAuth.AutoApproveDays > 0 {
				tgSection["auto_approve_days"] = cfg.TelegramAuth.AutoApproveDays
			}

			if out, err := toml.Marshal(tomlRoot); err == nil {
				tmp := tomlPath + ".tmp"
				if os.WriteFile(tmp, out, 0o644) == nil {
					if os.Rename(tmp, tomlPath) == nil {
						log.Info().Str("path", tomlPath).Msg("config: saved migrated TelegramAuth to config.toml")
					} else {
						_ = os.Remove(tmp)
					}
				}
			}
		}
	}
}

// applyPostProcessing resolves derived config values after all sources are merged.
func applyPostProcessing(cfg *Config) {
	// Mirror edge mode — clamp TTLs, trim host.
	cfg.Mirror.Normalize()

	// TorrServer home dir default
	if cfg.TorrServer.HomeDir == "" {
		cfg.TorrServer.HomeDir = filepath.Join(cfg.Compat.RepoRoot, "torrserver")
	}

	// TorrServer password from accs.db
	if cfg.TorrServer.Password == "" {
		accsPath := filepath.Join(cfg.TorrServer.HomeDir, "accs.db")
		if data, err := os.ReadFile(accsPath); err == nil {
			var accs map[string]string
			if json.Unmarshal(data, &accs) == nil {
				if pw := strings.TrimSpace(accs["ts"]); pw != "" {
					cfg.TorrServer.Password = pw
				}
			}
		}
	}

	// Zetflix.proxy backward compatibility → Proxy.Vless
	if cfg.Proxy.Vless.URI == "" && cfg.Online.Zetflix.Proxy != "" {
		cfg.Proxy.Vless.URI = cfg.Online.Zetflix.Proxy
		if len(cfg.Proxy.Vless.Balancers) == 0 {
			cfg.Proxy.Vless.Balancers = []string{"Zetflix"}
		}
	}

	// Normalize: single entry → Entries slice for uniform downstream usage.
	if len(cfg.Proxy.Vless.Entries) == 0 && cfg.Proxy.Vless.URI != "" {
		cfg.Proxy.Vless.Entries = []ProxyVlessEntry{
			{URI: cfg.Proxy.Vless.URI, Balancers: cfg.Proxy.Vless.Balancers},
		}
	}

	log.Info().
		Bool("transcoding_enable", cfg.Transcoding.Enable).
		Str("ffmpeg", cfg.Transcoding.FFmpeg).
		Msg("config: loaded")
}

// ValidateTOML tries to unmarshal raw TOML bytes into a Config struct.
// Returns the parsed config on success, or an error describing the problem.
// Whole-number floats (e.g., "9080.0") are automatically sanitized to ints.
func ValidateTOML(data []byte) (Config, error) {
	cfg := applyDefaults()
	data = SanitizeTOMLInts(data)
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	if cfg.Kit.ServerHost == "" && cfg.Kit.ServerHostCamel != "" {
		cfg.Kit.ServerHost = cfg.Kit.ServerHostCamel
	}
	cfg.Kit.ServerHostCamel = ""
	return cfg, nil
}

// MarshalTOML serializes the current loaded config to TOML bytes.
func MarshalTOML(cfg Config) ([]byte, error) {
	return toml.Marshal(cfg)
}

// TOMLFilePath returns the path to config.toml for the given repoRoot.
// It checks repoRoot first, then repoRoot/config.
func TOMLFilePath(repoRoot string) string {
	for _, dir := range []string{repoRoot, filepath.Join(repoRoot, "config")} {
		p := filepath.Join(dir, "config.toml")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	// Default: create in repoRoot.
	return filepath.Join(repoRoot, "config.toml")
}
