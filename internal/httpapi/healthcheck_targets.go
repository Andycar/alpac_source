package httpapi

import (
	"strings"

	"lampac-go/internal/balancerhealth"
)

// healthcheckTargets resolves which balancers the health checker probes each pass:
// every local core plugin that maps to a knownBalancers config section, paired with
// its host (host / apihost / linkhost). This resolution reads the httpapi config +
// plugin registry, so it stays here and is injected into the checker (which lives in
// the balancerhealth leaf package) via HealthChecker.SetTargets.
func healthcheckTargets() []balancerhealth.Target {
	root := loadMergedConf()
	var targets []balancerhealth.Target
	for pluginName := range localCorePlugins {
		// Find the config section by matching knownBalancers case-insensitively.
		configName := ""
		for _, kb := range knownBalancers {
			if strings.EqualFold(kb, pluginName) {
				configName = kb
				break
			}
		}
		if configName == "" {
			continue
		}
		section, ok := root[configName].(map[string]any)
		if !ok {
			continue
		}
		host := toStringAny(section["host"])
		if host == "" {
			host = toStringAny(section["apihost"])
		}
		if host == "" {
			host = toStringAny(section["linkhost"])
		}
		if host == "" {
			continue
		}
		targets = append(targets, balancerhealth.Target{Name: pluginName, Host: host})
	}
	return targets
}
