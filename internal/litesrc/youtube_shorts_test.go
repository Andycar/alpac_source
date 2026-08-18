package litesrc

import (
	"os"
	"path/filepath"
	"testing"
)

func card(id string, short bool) map[string]any {
	return map[string]any{"video_id": id, "short": short}
}

func ids(cards []map[string]any) []string {
	out := make([]string, 0, len(cards))
	for _, c := range cards {
		out = append(out, c["video_id"].(string))
	}
	return out
}

func TestFilterShortsModes(t *testing.T) {
	base := []map[string]any{card("a", false), card("s1", true), card("b", false), card("s2", true)}

	cases := []struct {
		mode string
		want []string
	}{
		{"hide", []string{"a", "b"}},
		{"only", []string{"s1", "s2"}},
		{"all", []string{"a", "s1", "b", "s2"}},     // explicit «show everything»
		{"", []string{"a", "s1", "b", "s2"}},        // no preference set
		{"garbage", []string{"a", "s1", "b", "s2"}}, // unknown mode must not silently drop anything
	}
	for _, tc := range cases {
		got := ids(filterShorts(base, tc.mode))
		if len(got) != len(tc.want) {
			t.Errorf("mode %q: got %v, want %v", tc.mode, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("mode %q: got %v, want %v", tc.mode, got, tc.want)
				break
			}
		}
	}
}

// A card the probe never classified (missing "short") must survive «hide»: an unknown video is
// treated as a regular one, so a failed probe can't quietly empty someone's feed.
func TestFilterShortsKeepsUnclassified(t *testing.T) {
	cards := []map[string]any{{"video_id": "x"}, card("s", true)}
	got := ids(filterShorts(cards, "hide"))
	if len(got) != 1 || got[0] != "x" {
		t.Fatalf("unclassified card dropped: %v", got)
	}
	// …and it must NOT appear under «only», which promises Shorts and nothing else.
	if got := ids(filterShorts(cards, "only")); len(got) != 1 || got[0] != "s" {
		t.Fatalf("only-mode leaked an unclassified card: %v", got)
	}
}

func TestShortsPrefRoundTrip(t *testing.T) {
	dir := t.TempDir()
	// The store is a process-wide singleton, so drive the type directly rather than via
	// ShortsPrefs() — otherwise the first test to touch it would pin the path for the rest.
	s := &shortsPrefStore{path: filepath.Join(dir, "prefs.json"), m: map[string]string{}}

	if got := s.Get(42); got != "" {
		t.Fatalf("unset pref = %q, want empty", got)
	}
	if err := s.Set(42, "hide"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := s.Get(42); got != "hide" {
		t.Fatalf("pref = %q, want hide", got)
	}
	// Another account must be unaffected — the key is per-tgID.
	if got := s.Get(43); got != "" {
		t.Fatalf("pref leaked to another account: %q", got)
	}
	// "all" is the default and clears the entry rather than storing it.
	if err := s.Set(42, "all"); err != nil {
		t.Fatalf("Set all: %v", err)
	}
	if got := s.Get(42); got != "" {
		t.Fatalf("after reset pref = %q, want empty", got)
	}
	// tgID 0 (unauthenticated) is a no-op, not a panic or a shared bucket.
	if err := s.Set(0, "hide"); err != nil {
		t.Fatalf("Set(0): %v", err)
	}
	if got := s.Get(0); got != "" {
		t.Fatalf("anonymous pref stored: %q", got)
	}
}

func TestShortsPrefPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prefs.json")
	first := &shortsPrefStore{path: path, m: map[string]string{}}
	if err := first.Set(7, "only"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	reloaded := &shortsPrefStore{path: path, m: map[string]string{}}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := json.Unmarshal(b, &reloaded.m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := reloaded.Get(7); got != "only" {
		t.Fatalf("after reload pref = %q, want only", got)
	}
}
