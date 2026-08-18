package httpapi

import (
	"sort"
	"strings"
	"testing"
)

// Every implemented balancer must also be registered in knownBalancers.
//
// That list is not cosmetic: it feeds the per-user visibility editor, the
// healthcheck targets and the telemetry dashboard. A source missing from it is
// invisible to all three — and, worse, it disappears from the client entirely
// for any user whose kit visibility map is in whitelist mode (any entry set to
// true hides everything not listed, and an unregistered source can never be
// listed). That is how Lift ended up "showing sometimes".
func TestEveryLocalCorePluginIsRegisteredInKnownBalancers(t *testing.T) {
	registered := make(map[string]string, len(knownBalancers))
	for _, name := range knownBalancers {
		registered[PluginKeyFor(name)] = name
	}

	// Premium tiers share their base source's config section and visibility.
	tiers := map[string]string{"filmixpro": "filmix"}

	var missing []string
	for plugin := range localCorePlugins {
		if _, ok := registered[plugin]; ok {
			continue
		}
		if base, isTier := tiers[plugin]; isTier {
			if _, ok := registered[base]; ok {
				continue
			}
		}
		// An alias spelling counts: "remux" is registered as "iRemux".
		found := false
		for _, spelling := range balancerAliasSpellings(plugin) {
			if _, ok := registered[spelling]; ok {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, plugin)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("balancers implemented but absent from knownBalancers "+
			"(they will be hidden for whitelist-mode users and skipped by healthcheck): %s",
			strings.Join(missing, ", "))
	}
}

// A registered balancer with no group lands in "other", which sorts it to the
// bottom of the client's source list — a silent demotion that is easy to miss.
// (A status tag is only a UI hint, so it is not required here.)
func TestEveryKnownBalancerHasGroup(t *testing.T) {
	var noGroup []string
	for _, name := range knownBalancers {
		if strings.TrimSpace(balancerGroupMap[name]) == "" {
			noGroup = append(noGroup, name)
		}
	}
	sort.Strings(noGroup)
	if len(noGroup) > 0 {
		t.Errorf("knownBalancers without a group (they sort last as \"other\"): %s", strings.Join(noGroup, ", "))
	}
}
