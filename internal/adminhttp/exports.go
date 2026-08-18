package adminhttp

import "lampac-go/internal/config"

// exports.go — thin exported wrappers over utilities that moved into adminhttp
// but are still called by core httpapi (cluster_secrets, server.go,
// proxycore_bridge). adminhttp does not import httpapi, so the host calls up into
// these instead. (Tech-debt: these are config/geo leaf utilities that would be
// better relocated to a shared leaf package than owned by adminhttp.)

// ConfigRepoRoot returns the runtime repo root (LAMPAC_GO_HOME / _REPO_ROOT / ".").
func ConfigRepoRoot() string { return configRepoRoot() }

// CreateConfigBackup writes data to config_backups/ with a timestamped name.
func CreateConfigBackup(data []byte) (string, error) { return createBackup(data) }

// GenerateAnnotatedTOML renders the running config as commented TOML.
func GenerateAnnotatedTOML(cfg config.Config) string { return generateAnnotatedTOML(cfg) }

// LookupGeoIP resolves an IP to (country, flag-emoji) via ip-api.com.
func LookupGeoIP(ip string) (string, string) { return lookupGeoIP(ip) }

// CountryCodeToFlag converts a 2-letter country code to a flag emoji.
func CountryCodeToFlag(code string) string { return countryCodeToFlag(code) }
