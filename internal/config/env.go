package config

import (
	"os"
	"strconv"
	"strings"
)

// applyEnvOverrides applies LAMPAC_GO_* environment variables on top of cfg.
// Environment variables always have the highest priority.
func applyEnvOverrides(cfg *Config) {
	setString := func(envKey string, dst *string) {
		if v := strings.TrimSpace(os.Getenv(envKey)); v != "" {
			*dst = v
		}
	}
	setBool := func(envKey string, dst *bool) {
		if v := strings.TrimSpace(os.Getenv(envKey)); v != "" {
			*dst = parseBool(v)
		}
	}
	setInt := func(envKey string, dst *int) {
		if v := strings.TrimSpace(os.Getenv(envKey)); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				*dst = n
			}
		}
	}
	setInt64 := func(envKey string, dst *int64) {
		if v := strings.TrimSpace(os.Getenv(envKey)); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				*dst = n
			}
		}
	}

	// Server
	setString("LAMPAC_GO_ADDR", &cfg.Server.Addr)

	// Compat
	setString("LAMPAC_GO_REPO_ROOT", &cfg.Compat.RepoRoot)

	// Proxy link
	setString("LAMPAC_GO_CACHE_DIR", &cfg.ProxyLink.CacheDir)

	// Observability
	setString("LAMPAC_GO_METRICS_PATH", &cfg.Observability.MetricsPath)
	setString("LAMPAC_GO_HEALTH_PATH", &cfg.Observability.HealthPath)
	setString("LAMPAC_GO_READY_PATH", &cfg.Observability.ReadyPath)
	setBool("LAMPAC_GO_ACCESS_LOG", &cfg.Observability.AccessLog)

	// JacRed
	setString("LAMPAC_GO_JACRED_HOST", &cfg.Parser.JacRedHost)
	setBool("LAMPAC_GO_JACRED_LOCAL", &cfg.Parser.JacRedLocal)
	setString("LAMPAC_GO_JACRED_HOME", &cfg.Parser.JacRedHome)
	setInt("LAMPAC_GO_JACRED_LOCAL_PORT", &cfg.Parser.JacRedLocalPort)

	// RuTracker (credentials via env so they need not sit in config.toml)
	setBool("LAMPAC_GO_RUTRACKER_ENABLE", &cfg.Parser.RuTracker.Enable)
	setString("LAMPAC_GO_RUTRACKER_HOST", &cfg.Parser.RuTracker.Host)
	setString("LAMPAC_GO_RUTRACKER_LOGIN", &cfg.Parser.RuTracker.Login)
	setString("LAMPAC_GO_RUTRACKER_PASSWORD", &cfg.Parser.RuTracker.Password)
	setString("LAMPAC_GO_RUTRACKER_COOKIE", &cfg.Parser.RuTracker.Cookie)

	// TorrServer
	setInt("LAMPAC_GO_TORRSERVER_PORT", &cfg.TorrServer.Port)
	setString("LAMPAC_GO_TORRSERVER_HOME", &cfg.TorrServer.HomeDir)

	// Telegram auth
	setBool("LAMPAC_GO_TG_AUTH_ENABLE", &cfg.TelegramAuth.Enable)
	setString("LAMPAC_GO_TG_BOT_TOKEN", &cfg.TelegramAuth.BotToken)
	setInt64("LAMPAC_GO_TG_ADMIN_ID", &cfg.TelegramAuth.AdminID)
	setString("LAMPAC_GO_TG_BOT_NAME", &cfg.TelegramAuth.BotName)
	setInt("LAMPAC_GO_TG_MAX_DEVICES", &cfg.TelegramAuth.MaxDevicesPerUser)

	// Admin auth
	if cfg.AdminAuth.Password == "" {
		setString("LAMPAC_GO_ADMIN_PASSWORD", &cfg.AdminAuth.Password)
	}
}

// parseBool parses "1", "true", "yes", "on" as true; anything else as false.
func parseBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
