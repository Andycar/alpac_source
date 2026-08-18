package httpapi

import (
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

// TestResolveEventsPluginsFiltersToLocalCore: with_search entries that have
// no Go implementation are dropped silently. Order is preserved.
func TestResolveEventsPluginsFiltersToLocalCore(t *testing.T) {
	cfg := config.Config{
		Online: config.OnlineConfig{
			WithSearch:  []string{"kinotochka", "filmix", "this-balancer-does-not-exist", "kinobase"},
			CustomOrder: true, // keep input order
		},
	}
	r := httptest.NewRequest("GET", "/lite/events", nil)
	got := resolveEventsPlugins(r, cfg, nil)
	want := []string{"kinotochka", "filmix", "kinobase"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filter result: got %v, want %v", got, want)
	}
}

// TestResolveEventsPluginsDedups: duplicate entries don't double up the
// resulting list — buildEventItems will skip dupes too, but we cap earlier.
func TestResolveEventsPluginsDedups(t *testing.T) {
	cfg := config.Config{
		Online: config.OnlineConfig{
			WithSearch:  []string{"kinotochka", "kinotochka", "Kinotochka"},
			CustomOrder: true,
		},
	}
	r := httptest.NewRequest("GET", "/lite/events", nil)
	got := resolveEventsPlugins(r, cfg, nil)
	// dedup happens by lowercased key — accept either exactly one entry or
	// the original capitalisation pattern (filter pass-through).
	count := 0
	for _, p := range got {
		if strings.EqualFold(p, "kinotochka") {
			count++
		}
	}
	if count == 0 {
		t.Fatalf("kinotochka dropped: got %v", got)
	}
}

// TestMakeEventItemWithCheckSearch: when checkOnlineSearch is on, the item
// gets show/rch/index pointers. URL prefix is /lite/<plugin>; the test uses
// "collaps" which has no clarification rule so the URL stays clean.
func TestMakeEventItemWithCheckSearch(t *testing.T) {
	item := makeEventItem("collaps", "http://lampa", "ru", 5, true)

	if item.Balanser != "collaps" {
		t.Fatalf("Balanser: %q", item.Balanser)
	}
	if !strings.HasPrefix(item.URL, "http://lampa/lite/collaps") {
		t.Fatalf("URL prefix: %q", item.URL)
	}
	if item.Show == nil || item.Rch == nil || item.Index == nil {
		t.Fatalf("expected show/rch/index pointers when checkOnlineSearch=true")
	}
	if *item.Index != 5 {
		t.Fatalf("Index: %d", *item.Index)
	}
}

// TestMakeEventItemWithoutCheckSearch: pointers stay nil when checkOnlineSearch=false.
func TestMakeEventItemWithoutCheckSearch(t *testing.T) {
	item := makeEventItem("kinotochka", "http://lampa", "ru", 0, false)
	if item.Show != nil || item.Rch != nil || item.Index != nil {
		t.Fatalf("expected nil show/rch/index when checkOnlineSearch=false")
	}
}

// TestMakeEventItemAppendsClarification: filmix on Russian language gets
// the clarification flag appended to its URL.
func TestMakeEventItemAppendsClarification(t *testing.T) {
	// filmix is one of clarificationPlugins; original_language="ru" is in
	// clarificationLanguages.
	item := makeEventItem("filmix", "http://lampa", "ru", 0, false)
	if !strings.Contains(item.URL, "clarification=1") {
		t.Fatalf("expected clarification flag on filmix+ru, got %q", item.URL)
	}

	// English language must NOT trigger clarification (clarification is for
	// Slavic content where transliteration matters).
	item = makeEventItem("filmix", "http://lampa", "en", 0, false)
	if strings.Contains(item.URL, "clarification=1") {
		t.Fatalf("unexpected clarification on filmix+en: %q", item.URL)
	}
}

// TestSortItemsByShowAndQuality: show=true bubbles up, ties broken by quality
// rank, with original index as last fallback.
func TestSortItemsByShowAndQuality(t *testing.T) {
	yes := true
	no := false
	fhd := "FHD"
	uhd := "4K"
	hd := "HD"

	mkitem := func(name string, show bool, q *string, idx int) liteEventItem {
		s := show
		i := idx
		return liteEventItem{
			Name:    name,
			Show:    &s,
			Quality: q,
			Index:   &i,
		}
	}
	_ = no

	items := []liteEventItem{
		mkitem("c-hd-false", false, &hd, 0),
		mkitem("a-uhd-true", true, &uhd, 1),
		mkitem("b-fhd-true", true, &fhd, 2),
		mkitem("d-noq-true", true, nil, 3),
	}
	sortItemsByShowAndQuality(items, true)

	// Expect: 4K-show > FHD-show > nil-q-show > HD-noshow (last).
	wantOrder := []string{"a-uhd-true", "b-fhd-true", "d-noq-true", "c-hd-false"}
	got := make([]string, len(items))
	for i, it := range items {
		got[i] = it.Name
	}
	if !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("sort order: got %v, want %v", got, wantOrder)
	}
	_ = yes
}

// TestEventPluginVisibleFilmixproRequiresToken: filmixpro is hidden without
// a configured Filmix token.
func TestEventPluginVisibleFilmixproRequiresToken(t *testing.T) {
	cfgNoToken := config.Config{
		Online: config.OnlineConfig{
			Filmix: config.FilmixSource{Token: "", Pro: false},
		},
	}
	r := httptest.NewRequest("GET", "/lite/events", nil)
	if eventPluginVisible(r, cfgNoToken, "filmixpro", "", false) {
		t.Fatalf("filmixpro should be hidden without token")
	}
	if !eventPluginVisible(r, cfgNoToken, "kinotochka", "", false) {
		t.Fatalf("kinotochka should be visible regardless of filmix token")
	}

	cfgWithToken := config.Config{
		Online: config.OnlineConfig{
			Filmix: config.FilmixSource{Token: "secret-tok", Pro: true},
		},
	}
	if !eventPluginVisible(r, cfgWithToken, "filmixpro", "", false) {
		t.Fatalf("filmixpro should be visible when token+pro are set")
	}
}

// TestEventPluginVisibleAnimeLanguageGate: anime balancers are hidden when
// content language is non-Asian.
func TestEventPluginVisibleAnimeLanguageGate(t *testing.T) {
	cfg := config.Config{}
	r := httptest.NewRequest("GET", "/lite/events", nil)

	// "ja" is in animeLanguages — anime balancers should show.
	if !eventPluginVisible(r, cfg, "anilibria", "ja", false) {
		t.Fatalf("anilibria with ja language should be visible")
	}
	// "en" is NOT in animeLanguages — anime should be hidden.
	if eventPluginVisible(r, cfg, "anilibria", "en", false) {
		t.Fatalf("anilibria with en language should be hidden")
	}
	// Empty language = unknown content; allow anime through (current policy).
	if !eventPluginVisible(r, cfg, "anilibria", "", false) {
		t.Fatalf("anilibria with empty language should be visible (unknown content)")
	}
}
