package tgauth

import "testing"

// TestBalancerAllowedStrictMode verifies the opt-in strict-allowlist flag:
// with StrictBalancers=false (legacy default) a non-empty map is a denylist
// (absent key = allowed); with StrictBalancers=true it is an allowlist (only
// explicitly-true keys allowed, absent = denied). An empty map = all allowed
// in both modes (so enabling strict with no selection never locks the group out).
func TestBalancerAllowedStrictMode(t *testing.T) {
	cases := []struct {
		name   string
		strict bool
		bals   map[string]bool
		plugin string
		want   bool
	}{
		{"empty map = all (strict)", true, map[string]bool{}, "filmix", true},
		{"empty map = all (loose)", false, map[string]bool{}, "filmix", true},
		{"strict: selected is allowed", true, map[string]bool{"filmix": true}, "filmix", true},
		{"strict: unselected is denied", true, map[string]bool{"filmix": true}, "kinotochka", false},
		{"loose: unselected is allowed (denylist)", false, map[string]bool{"filmix": true}, "kinotochka", true},
		{"loose: explicit false denied", false, map[string]bool{"kinotochka": false}, "kinotochka", false},
		{"strict: explicit false denied", true, map[string]bool{"filmix": true, "kinotochka": false}, "kinotochka", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := UserGroup{StrictBalancers: c.strict, Balancers: c.bals}
			if got := g.BalancerAllowed(c.plugin); got != c.want {
				t.Fatalf("BalancerAllowed(%q) strict=%v bals=%v = %v, want %v", c.plugin, c.strict, c.bals, got, c.want)
			}
		})
	}
}
