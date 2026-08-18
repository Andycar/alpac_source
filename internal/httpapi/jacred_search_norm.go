package httpapi

import (
	"net/url"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// jacredSearchParams are the query keys that carry a movie title upstream:
// v2 (/api/v2.0/indexers/…) uses title + title_original, v1 uses search.
var jacredSearchParams = []string{"title", "title_original", "search"}

// stripLatinDiacritics folds Latin letters onto their base form: "Déjà Vu" →
// "Deja Vu". Cyrillic is deliberately left alone — decomposing it and dropping
// the marks would turn "й" into "и" and "ё" into "е", quietly changing the word.
//
// Why this matters for torrent search (measured against jacred.stream on
// 2026-08-01): "Déjà Vu" returns 201 rows, ALL of them DJVU e-book packs and not
// one film, while "Deja Vu" returns 104 rows of the actual movie. Trackers spell
// releases in plain ASCII, so a title carrying diacritics — which is exactly what
// TMDB hands us whenever a film has no localized name — matches nothing real and
// the leftover short token ("Vu") drags in unrelated junk.
func stripLatinDiacritics(s string) string {
	if s == "" || isASCII(s) {
		return s
	}

	decomposed := norm.NFD.String(s)
	var b strings.Builder
	b.Grow(len(decomposed))

	latinBase := false
	for _, r := range decomposed {
		if unicode.Is(unicode.Mn, r) {
			if latinBase {
				continue // drop the accent that belongs to a Latin letter
			}
			b.WriteRune(r) // keep it: this mark is part of a non-Latin letter
			continue
		}
		latinBase = r <= unicode.MaxASCII && unicode.IsLetter(r)
		b.WriteRune(r)
	}
	return norm.NFC.String(b.String())
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > unicode.MaxASCII {
			return false
		}
	}
	return true
}

// normalizeJacredSearchQuery rewrites the title-bearing params in place so every
// client (web, androidtv, tvOS, Lampa) gets the fix without shipping a new build.
// Returns true when anything actually changed.
func normalizeJacredSearchQuery(q url.Values) bool {
	changed := false
	for _, key := range jacredSearchParams {
		for i, v := range q[key] {
			if folded := stripLatinDiacritics(v); folded != v {
				q[key][i] = folded
				changed = true
			}
		}
	}
	return changed
}
