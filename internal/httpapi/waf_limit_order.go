package httpapi

import "sort"

// wafCatchAllProbes are paths with nothing in common. A pattern matching every
// one of them is a catch-all (".*", ".+", "^.*$"), not a rule about some part
// of the API.
var wafCatchAllProbes = []string{"/", "/a", "/zzz/qqq", "/lite/x", "/capi/y"}

// isWAFCatchAllRule reports whether a compiled rule matches everything.
func isWAFCatchAllRule(r *wafCompiledLimitRule) bool {
	for _, p := range wafCatchAllProbes {
		if !r.pattern.MatchString(p) {
			return false
		}
	}
	return true
}

// sortWAFLimitRules puts specific rules before catch-alls and makes the whole
// order deterministic.
//
// checkRateLimit stops at the first matching rule, so a catch-all sitting ahead
// of the specific rules silences all of them. The rules come out of a JSON map,
// and ranging a Go map randomises their order — which made the effective limits
// change from restart to restart rather than fail outright. Sorting keeps the
// specific patterns in charge and leaves the catch-all as the fallback it is
// meant to be; ties break on the raw pattern so two runs of the same config
// always compile to the same order.
func sortWAFLimitRules(rules []wafCompiledLimitRule) {
	catchAll := make(map[string]bool, len(rules))
	for i := range rules {
		catchAll[rules[i].raw] = isWAFCatchAllRule(&rules[i])
	}
	sort.SliceStable(rules, func(i, j int) bool {
		ci, cj := catchAll[rules[i].raw], catchAll[rules[j].raw]
		if ci != cj {
			return !ci // specific first
		}
		return rules[i].raw < rules[j].raw
	})
}
