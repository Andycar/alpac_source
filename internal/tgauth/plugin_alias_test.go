package tgauth

import "testing"

func TestCanonicalPluginKey(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// Canonical names map to themselves.
		{"rezka", "rezka"},
		{"filmix", "filmix"},
		{"mirage", "mirage"},
		{"rhsprem", "rhsprem"},
		{"redheadsound", "redheadsound"},

		// Sub-path stripping.
		{"rezka/movie", "rezka"},
		{"rezka/movie.m3u8", "rezka"},
		{"rezka/serial", "rezka"},
		{"mirage/stream.m3u8", "mirage"},
		{"aladdin/video", "aladdin"},

		// "rc/" premium-variant prefix.
		{"rc/filmix", "filmix"},
		{"rc/fxapi", "fxapi"},
		{"rc/rhs", "rhsprem"},
		{"rc/rhs/movie", "rhsprem"},
		{"rc/rhs/serial", "rhsprem"},

		// URL-path aliases → canonical.
		{"rhs", "rhsprem"},
		{"rhs/movie", "rhsprem"},
		{"rhs/serial", "rhsprem"},
		{"filmixpro", "filmix"},
		{"kinopubpro", "kinopub"},
		{"vcdn", "videocdn"},
		{"zona-mobilink", "zona"},
		{"zona-hdvb", "zona"},
		{"zona-filmix", "zona"},
		{"zona-takedwn", "zona"},
		{"vokinotk", "vokino"},

		// Search/spider route variants → canonical.
		{"alloha-search", "alloha"},
		{"hdvb-search", "hdvb"},
		{"mirage-search", "mirage"},
		{"aladdin-search", "aladdin"},
		{"collaps-search", "collaps"},
		{"getstv-search", "getstv"},
		{"veoveo-spider", "veoveo"},

		// Legacy PascalCase-lowered admin key.
		{"anilibriaonline", "anilibria"},

		// Case and whitespace insensitivity.
		{"Rhsprem", "rhsprem"},
		{"RHS", "rhsprem"},
		{"  mirage/video  ", "mirage"},
		{"RC/FILMIX", "filmix"},

		// Empty input.
		{"", ""},
		{"   ", ""},
	}
	for _, tc := range cases {
		got := CanonicalPluginKey(tc.in)
		if got != tc.want {
			t.Errorf("CanonicalPluginKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBalancerAllowed_CanonicalAliases(t *testing.T) {
	// Admin denies Rhsprem via the admin panel → stored as "rhsprem".
	// User must NOT be able to access /lite/rhs or /lite/rc/rhs either.
	g := &UserGroup{
		Balancers: map[string]bool{
			"rhsprem":  false,
			"filmix":   false,
			"videocdn": false,
			"zona":     false,
			"vokino":   false,
			// Positive entries so the map isn't "empty = all allowed".
			"kinotochka": true,
			"mirage":     true,
		},
	}

	denied := []string{
		// Direct canonical denials.
		"rhsprem", "filmix", "videocdn", "zona", "vokino",
		// Route aliases for Rhsprem.
		"rhs", "rhs/movie", "rhs/serial", "rc/rhs", "rc/rhs/movie",
		// Filmix premium variants.
		"filmixpro", "rc/filmix",
		// VideoCDN short alias.
		"vcdn",
		// Zona sub-sources.
		"zona-mobilink", "zona-hdvb", "zona-filmix", "zona-takedwn",
		// VoKino Turkish split.
		"vokinotk",
	}
	for _, plugin := range denied {
		if g.BalancerAllowed(plugin) {
			t.Errorf("BalancerAllowed(%q) = true, want false (denied via alias canonicalization)", plugin)
		}
	}

	allowed := []string{
		"kinotochka", "mirage", "mirage-search", "mirage/stream.m3u8",
		// Balancers not in the map at all — default allow.
		"ashdi", "eneyida", "collaps",
	}
	for _, plugin := range allowed {
		if !g.BalancerAllowed(plugin) {
			t.Errorf("BalancerAllowed(%q) = false, want true", plugin)
		}
	}
}

func TestBalancerAllowed_EmptyMap(t *testing.T) {
	g := &UserGroup{Balancers: map[string]bool{}}
	// Empty map → everything allowed.
	for _, plugin := range []string{"mirage", "rhs", "filmixpro", "unknown"} {
		if !g.BalancerAllowed(plugin) {
			t.Errorf("BalancerAllowed(%q) on empty-map group = false, want true", plugin)
		}
	}
}

// TestBalancerAllowed_ProdTwoGroupsRezka reproduces the production setup:
// two groups — "Стандарт" (Rezka off) and "Поддержавшие" (Rezka on) —
// on ALL Rezka URL variants Lampa might hit.
func TestBalancerAllowed_ProdTwoGroupsRezka(t *testing.T) {
	// Admin leaves every balancer checked EXCEPT Rezka.
	// JS at admin_tg_panel.go:7484 iterates all checkboxes and sends a
	// full map like {rezka:false, kinotochka:true, filmix:true, ...}.
	standard := &UserGroup{
		ID:   "default",
		Name: "Стандарт",
		Balancers: map[string]bool{
			"rezka":      false, // <-- denied
			"kinotochka": true,
			"mirage":     true,
			"filmix":     true,
			"collaps":    true,
			"zetflix":    true,
			"rhsprem":    true,
			"videocdn":   true,
			"alloha":     true,
		},
	}
	// Supporters group has Rezka allowed (admin re-checked it before save).
	supporters := &UserGroup{
		ID:   "supporters",
		Name: "Поддержавшие",
		Balancers: map[string]bool{
			"rezka":      true, // <-- allowed
			"kinotochka": true,
			"mirage":     true,
			"filmix":     true,
			"collaps":    true,
			"zetflix":    true,
			"rhsprem":    true,
			"videocdn":   true,
			"alloha":     true,
		},
	}

	// Every URL path Lampa is known to hit for Rezka.
	rezkaURLs := []string{
		"rezka",
		"rezka/movie",
		"rezka/movie.m3u8",
		"rezka/serial",
	}
	for _, u := range rezkaURLs {
		if standard.BalancerAllowed(u) {
			t.Errorf("Стандарт: BalancerAllowed(%q) = true, want false (Rezka must be blocked)", u)
		}
		if !supporters.BalancerAllowed(u) {
			t.Errorf("Поддержавшие: BalancerAllowed(%q) = false, want true (Rezka must be reachable)", u)
		}
	}

	// And a negative: other balancers stay reachable for standard too.
	for _, u := range []string{"kinotochka", "mirage/stream.m3u8", "filmix", "collaps-search", "rc/filmix"} {
		if !standard.BalancerAllowed(u) {
			t.Errorf("Стандарт: BalancerAllowed(%q) = false, want true (other balancers stay open)", u)
		}
	}
}

func TestBalancerAllowed_LegacyAnilibriaOnline(t *testing.T) {
	// Legacy groups.json stored "anilibriaonline" (from the admin UI
	// lowercasing PascalCase "AnilibriaOnline"), but the real plugin key is
	// "anilibria". The legacy-key scan path should still deny it.
	g := &UserGroup{
		Balancers: map[string]bool{
			"anilibriaonline": false,
			"mirage":          true,
		},
	}
	if g.BalancerAllowed("anilibria") {
		t.Error("BalancerAllowed(\"anilibria\") on legacy-keyed group = true, want false")
	}
	if !g.BalancerAllowed("mirage") {
		t.Error("BalancerAllowed(\"mirage\") = false, want true")
	}
}
