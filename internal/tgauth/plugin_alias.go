package tgauth

import "strings"

// pluginAliasMap maps URL-path aliases (and legacy admin-panel keys) to the
// canonical plugin key used in a UserGroup's Balancers map.
//
// Context: the admin panel lists balancers from a PascalCase knownBalancers
// array and stores the lowercased display name as the group-map key. But
// httpapi/lite_sources.go registers multiple route aliases pointing to the
// same handler — for example /lite/rhs, /lite/rc/rhs and /lite/rhsprem all
// land on the Rhsprem checker. Without canonicalization, a group that denies
// "rhsprem" would still allow /lite/rhs, because BalancerAllowed would look
// up "rhs" in the Balancers map, miss it, and default-allow.
//
// There is also one PascalCase lowered mismatch: the admin UI builds its key
// as "AnilibriaOnline".toLowerCase() → "anilibriaonline", but the real plugin
// key (and URL path) is "anilibria". The alias entry handles legacy
// groups.json data where that mismatched key was stored.
//
// Only aliases are listed. Canonical keys map to themselves and do not need
// an entry.
var pluginAliasMap = map[string]string{
	// Premium Russian voice-over tier — /lite/rhs and /lite/rc/rhs both
	// resolve to the same Rhsprem handler as /lite/rhsprem.
	"rhs": "rhsprem",

	// Filmix premium tier — /lite/filmixpro is a separate URL but backed
	// by the same Filmix checker in lite_sources.go.
	"filmixpro": "filmix",

	// KinoPub premium tier
	"kinopubpro": "kinopub",

	// VideoCDN short alias
	"vcdn": "videocdn",

	// Zona sub-sources — all share the same handler/config under "zona".
	"zona-mobilink": "zona",
	"zona-hdvb":     "zona",
	"zona-filmix":   "zona",
	"zona-takedwn":  "zona",

	// VoKino Turkish split (single handler)
	"vokinotk": "vokino",

	// Search/spider route variants — same handler, same content source, the
	// admin only sees and toggles the canonical name. Without these entries,
	// a denied balancer is still reachable via ?checksearch=true on the
	// -search variant.
	"alloha-search":  "alloha",
	"hdvb-search":    "hdvb",
	"mirage-search":  "mirage",
	"aladdin-search": "aladdin",
	"collaps-search": "collaps",
	"getstv-search":  "getstv",
	"veoveo-spider":  "veoveo",

	// PascalCase-lowered mismatch (legacy groups.json data).
	"anilibriaonline": "anilibria",
}

// CanonicalPluginKey normalizes a plugin key for use with
// UserGroup.BalancerAllowed.
//
// Accepts any of:
//   - URL-path fragments:  "rezka/movie", "rc/filmix", "rc/rhs/movie"
//   - Bare plugin names:   "filmix", "rhs", "filmixpro"
//   - Aliases:             "rhs" → "rhsprem", "vcdn" → "videocdn"
//   - Legacy admin keys:   "anilibriaonline" → "anilibria"
//
// The function strips the "rc/" premium-variant prefix, drops everything
// after the first remaining slash (sub-path), lowercases, and resolves the
// result against pluginAliasMap. Returns empty string for empty input.
func CanonicalPluginKey(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return ""
	}

	// Strip "rc/" premium-variant prefix: "rc/rhs/movie" → "rhs/movie".
	if rest := strings.TrimPrefix(raw, "rc/"); rest != raw {
		raw = rest
	}

	// Drop sub-path: "rhs/movie" → "rhs".
	if slashIdx := strings.Index(raw, "/"); slashIdx >= 0 {
		raw = raw[:slashIdx]
	}

	// Resolve alias.
	if canonical, ok := pluginAliasMap[raw]; ok {
		return canonical
	}
	return raw
}
