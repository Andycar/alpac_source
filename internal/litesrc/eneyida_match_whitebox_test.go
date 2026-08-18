package litesrc

import (
	"os"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func eneyidaFixture(t *testing.T, name string) string {
	b, err := os.ReadFile("testdata/eneyida_search_" + name + ".html")
	if err != nil {
		t.Skipf("fixture %s missing: %v", name, err)
	}
	return string(b)
}

func TestEneyidaMatchFixture(t *testing.T) {
	e := NewEneyidaChecker(config.Config{})

	cases := []struct {
		name, fixture, query string
		year                 int
		wantHrefSub          string // "" = expect NO selection (defer to similars)
	}{
		// eneyida lists the 2021 film as "Dune: Part One" — subtitle variant, must match.
		{"dune-2021-subtitle-variant", "Dune", "Dune", 2021, "6396-djuna"},
		// Exact original_title still matches exactly.
		{"dune-part-two-exact", "Dune", "Dune: Part Two", 2024, "9366-duna-chastyna-druga"},
		{"superman-2025-exact", "Superman", "Superman", 2025, "9864-supermen"},
		{"superman-1978-exact", "Superman", "Superman", 1978, "6702-supermen"},
		// "Children of Dune" (2003) must NOT be picked for a "Dune" query (not related).
		{"dune-2003-no-false-match", "Dune", "Dune", 2003, ""},
	}
	for _, c := range cases {
		html := eneyidaFixture(t, c.fixture)
		selected, _ := e.parseSearch(html, c.query, c.year)
		t.Logf("[%s] query=%q year=%d selected=%q", c.name, c.query, c.year, selected)
		if c.wantHrefSub == "" {
			if selected != "" {
				t.Errorf("[%s] expected NO selection, got %q", c.name, selected)
			}
			continue
		}
		if !strings.Contains(strings.ToLower(selected), c.wantHrefSub) {
			t.Errorf("[%s] selected %q, expected substring %q", c.name, selected, c.wantHrefSub)
		}
	}
}

func TestEneyidaTitleRelated(t *testing.T) {
	cases := []struct {
		name, want string
		related    bool
	}{
		{"dune part one", "dune", true},
		{"dune", "dune part one", true},
		{"dune", "dune", true},
		{"children of dune", "dune", false}, // mid-string must NOT match
		{"dunes", "dune", false},            // no word boundary
		{"batman v superman", "superman", false},
		{"", "dune", false},
	}
	for _, c := range cases {
		if got := eneyidaTitleRelated(c.name, c.want); got != c.related {
			t.Errorf("eneyidaTitleRelated(%q,%q)=%v want %v", c.name, c.want, got, c.related)
		}
	}
}
