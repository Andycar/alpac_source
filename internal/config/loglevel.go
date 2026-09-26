package config

import (
	"os"
	"strings"

	"github.com/rs/zerolog"
)

// ResolveLogLevel maps the configured level to a zerolog level, with the
// LOG_LEVEL environment variable taking priority over [observability]
// log_level — the same precedence the process start-up uses.
//
// Anything unrecognised (including an empty setting) resolves to info, so a
// typo degrades to the normal level instead of silencing the server.
func ResolveLogLevel(cfgLevel string) zerolog.Level {
	lvl := strings.TrimSpace(os.Getenv("LOG_LEVEL"))
	if lvl == "" {
		lvl = strings.TrimSpace(cfgLevel)
	}
	switch strings.ToLower(lvl) {
	case "debug":
		return zerolog.DebugLevel
	case "warn", "warning":
		return zerolog.WarnLevel
	case "error":
		return zerolog.ErrorLevel
	default:
		return zerolog.InfoLevel
	}
}

// ApplyLogLevel sets the global zerolog level and reports whether it changed.
//
// Reload calls this so log_level behaves like the rest of config.toml. It used
// to be read once at start-up only, which made the setting a trap: raising it
// to "debug" to investigate a live incident changed the file, the server
// reported "config: loaded config.toml", and nothing about the logs changed —
// the extra detail only appeared after a full restart, by which point the
// incident was usually over.
func ApplyLogLevel(cfgLevel string) (zerolog.Level, bool) {
	want := ResolveLogLevel(cfgLevel)
	if zerolog.GlobalLevel() == want {
		return want, false
	}
	zerolog.SetGlobalLevel(want)
	return want, true
}
