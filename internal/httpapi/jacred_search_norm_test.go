package httpapi

import (
	"net/url"
	"testing"
)

// "Déjà Vu" is what TMDB hands us for films without a localized name, and it is
// poison for tracker search: measured against jacred.stream on 2026-08-01 it
// returned 201 rows of DJVU e-book packs and zero films, while "Deja Vu" returned
// 104 rows of the actual movie.
func TestStripLatinDiacritics(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Déjà Vu", "Deja Vu"},
		{"Amélie", "Amelie"},
		{"El Laberinto del Fauno", "El Laberinto del Fauno"}, // already ASCII, untouched
		{"", ""},
		{"Æon Flux", "Æon Flux"}, // not a combining mark — left alone rather than mangled
	}
	for _, c := range cases {
		if got := stripLatinDiacritics(c.in); got != c.want {
			t.Fatalf("stripLatinDiacritics(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Cyrillic must survive untouched: NFD splits "й" into "и"+breve and "ё" into
// "е"+diaeresis, so a blind mark-drop would silently change the word.
func TestStripLatinDiacriticsKeepsCyrillic(t *testing.T) {
	for _, s := range []string{"Ёлки", "Майор Гром", "Приключения Электроника", "Дежавю"} {
		if got := stripLatinDiacritics(s); got != s {
			t.Fatalf("cyrillic must not change: %q → %q", s, got)
		}
	}
	// Mixed strings fold only the Latin half.
	if got := stripLatinDiacritics("Дежавю / Déjà Vu"); got != "Дежавю / Deja Vu" {
		t.Fatalf("mixed: got %q", got)
	}
}

func TestNormalizeJacredSearchQuery(t *testing.T) {
	q := url.Values{
		"title":          {"Déjà Vu"},
		"title_original": {"Déjà Vu"},
		"search":         {"Amélie"},
		"year":           {"2006"},
		"apikey":         {"pp"},
	}
	if !normalizeJacredSearchQuery(q) {
		t.Fatalf("expected the query to change")
	}
	if q.Get("title") != "Deja Vu" || q.Get("title_original") != "Deja Vu" || q.Get("search") != "Amelie" {
		t.Fatalf("unexpected folding: %v", q)
	}
	// Non-title params are never touched.
	if q.Get("year") != "2006" || q.Get("apikey") != "pp" {
		t.Fatalf("non-title params must be untouched: %v", q)
	}
	// An all-ASCII query reports no change, so the proxy stays a pure passthrough.
	if normalizeJacredSearchQuery(url.Values{"title": {"Home Alone"}}) {
		t.Fatalf("ASCII query must not report a change")
	}
}

// The v1 endpoint (used by the native androidtv/tvOS clients via `search=`) must be
// folded exactly like the v2 one the web client uses — the fix has to be
// client-agnostic, since a released APK cannot be patched from the server.
func TestNormalizeJacredSearchQueryV1SearchParam(t *testing.T) {
	q := url.Values{"search": {"Déjà Vu"}}
	if !normalizeJacredSearchQuery(q) || q.Get("search") != "Deja Vu" {
		t.Fatalf("v1 search param must fold: %v", q)
	}
}
