package httpapi

import (
	"strings"
	"time"

	"lampac-go/internal/balancerstats"
	"lampac-go/internal/config"
	"lampac-go/internal/tgauth"
)

// newBalancerStatsManager builds a stats manager pre-configured with the
// fallback host lists from cfg.Telemetry.Hosts. Returns a manager even when
// telemetry is disabled — callers may still hit Snapshot() (it returns empty),
// and disabling-vs-no-op is enforced at the recorder level via balancer_fetch
// global pointer.
func newBalancerStatsManager(cfg config.Config) *balancerstats.Manager {
	m := balancerstats.NewManager()

	if cfg.Telemetry.FallbackFailLimit > 0 {
		// Reach into the manager via SetFallbacks/Reset path is not enough —
		// fail limit is internal. We pass it via a helper:
		balancerstats.ManagerSetFailLimit(m, cfg.Telemetry.FallbackFailLimit)
	}

	for k, hosts := range cfg.Telemetry.Hosts {
		if k == "" || len(hosts) == 0 {
			continue
		}
		m.SetFallbacks(k, hosts)
	}
	return m
}

// wireBalancerStatsAlerts builds the AlertEngine after the tgauth.Bot and
// admin store are available. Called from server_routes_admin.go after
// adminIDStore is constructed.
func wireBalancerStatsAlerts(s *Server, cfg config.Config, tgBot *tgauth.Bot, adminStore *tgauth.AdminIDStore) {
	if s == nil || s.balancerStats == nil {
		return
	}
	if !cfg.Telemetry.AlertsEnabled || tgBot == nil {
		return
	}
	if adminStore != nil {
		tgBot.SetAdminIDLister(adminStore)
	}
	alertCfg := balancerstats.DefaultAlertConfig()
	alertCfg.Enabled = true
	if cfg.Telemetry.FailRateThreshold > 0 {
		alertCfg.FailRateThreshold = cfg.Telemetry.FailRateThreshold
	}
	if cfg.Telemetry.MinAttempts > 0 {
		alertCfg.MinAttempts = cfg.Telemetry.MinAttempts
	}
	if cfg.Telemetry.CooldownMinutes > 0 {
		alertCfg.Cooldown = time.Duration(cfg.Telemetry.CooldownMinutes) * time.Minute
	}
	s.alertEngine = balancerstats.NewAlertEngine(alertCfg, s.balancerStats, tgBot)
}

// telemetryRecorderEnabled returns whether the recorder hot-path is active.
//
// As of the 2026-05-18 fix this always returns true: the recorder is cheap
// (one map lookup + atomic increment), it powers the admin /api/telemetry
// dashboard, and disabling it left admins blind to balancer health. The flag
// is kept on the struct only for backwards compat with existing config.toml
// files and is intentionally ignored here.
func telemetryRecorderEnabled(_ config.Config) bool {
	return true
}

// canonBalancerKey normalizes a balancer name for stats lookups (matches the
// key the recorder writes — lowercased, trimmed).
func canonBalancerKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
