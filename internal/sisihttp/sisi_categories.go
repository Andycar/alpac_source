package sisihttp

import (
	"net/http"
)

// sisiUnifiedCategory represents a unified category usable across all sisi sources.
type sisiUnifiedCategory struct {
	Key   string `json:"key"`
	Title string `json:"title"`
}

// sisiUnifiedCategories is the curated list of unified categories.
var sisiUnifiedCategories = []sisiUnifiedCategory{
	{Key: "russian", Title: "Русское"},
	{Key: "anal", Title: "Анал"},
	{Key: "asian", Title: "Азиатки"},
	{Key: "milf", Title: "MILF / Зрелые"},
	{Key: "teen", Title: "Молодые"},
	{Key: "amateur", Title: "Любительское"},
	{Key: "lesbian", Title: "Лесбиянки"},
	{Key: "big-tits", Title: "Большие сиськи"},
	{Key: "blowjob", Title: "Минет"},
	{Key: "bdsm", Title: "BDSM"},
	{Key: "blonde", Title: "Блондинки"},
	{Key: "brunette", Title: "Брюнетки"},
	{Key: "bbw", Title: "BBW"},
	{Key: "interracial", Title: "Межрасовое"},
	{Key: "pov", Title: "POV"},
	{Key: "threesome", Title: "Тройка"},
	{Key: "creampie", Title: "Кремпай"},
	{Key: "hentai", Title: "Хентай"},
}

// sisiCategoryMapping maps unified category key → source-specific category value.
// Key: plugin name (xds, phub, xmr, elo, epr, hqr, sbg, ptx).
// Sources not listed here will use search fallback.
var sisiCategoryMapping = map[string]map[string]string{
	"xds": {
		"anal": "Anal-12", "asian": "Asian_Woman-32", "milf": "Milf-19",
		"teen": "Teen-13", "amateur": "Amateur-65", "lesbian": "Lesbian-26",
		"big-tits": "Big_Tits-23", "blowjob": "Blowjob-15", "blonde": "Blonde-20",
		"brunette": "Brunette-25", "interracial": "Interracial-27",
	},
	"xdsred": {
		"anal": "Anal-12", "asian": "Asian_Woman-32", "milf": "Milf-19",
		"teen": "Teen-13", "amateur": "Amateur-65", "lesbian": "Lesbian-26",
		"big-tits": "Big_Tits-23", "blowjob": "Blowjob-15", "blonde": "Blonde-20",
		"brunette": "Brunette-25", "interracial": "Interracial-27",
	},
	"phub": {
		"russian": "99", "anal": "35", "asian": "1", "bdsm": "10",
		"big-tits": "8", "blonde": "9", "milf": "28",
	},
	"phubprem": {
		"russian": "99", "anal": "35", "asian": "1", "bdsm": "10",
		"big-tits": "8", "blonde": "9", "milf": "28",
	},
	"xmr": {
		"russian": "russian", "anal": "anal", "asian": "asian",
		"big-tits": "big-tits", "milf": "milf", "teen": "teen",
		"amateur": "amateur", "lesbian": "lesbian", "interracial": "interracial",
		"blowjob": "blowjob", "bbw": "bbw", "hentai": "hentai",
	},
	"elo": {
		"anal": "anal-videos", "bdsm": "bdsm-porn", "big-tits": "big-tits",
		"blonde": "blonde", "brunette": "a1-brunette", "milf": "milf",
		"lesbian": "lesbian", "teen": "teen", "pov": "pov",
	},
	"epr": {
		"anal": "anal", "asian": "asian", "bbw": "bbw", "bdsm": "bdsm",
		"blowjob": "blowjob", "milf": "milf", "amateur": "amateur",
	},
	"hqr": {
		"anal": "anal", "milf": "milf", "lesbian": "lesbian",
		"big-tits": "big-tits", "teen": "teen-porn", "pov": "pov",
		"threesome": "threesome", "asian": "asian", "creampie": "creampie",
	},
}

// sisiCategorySearchTerms maps unified category key → search term for sources
// that don't support native category filtering.
// PornLab uses Russian terms because the tracker has Russian titles.
var sisiCategorySearchTerms = map[string]string{
	"russian":     "русское",
	"anal":        "anal",
	"asian":       "asian",
	"milf":        "milf",
	"teen":        "teen",
	"amateur":     "amateur",
	"lesbian":     "lesbian",
	"big-tits":    "big tits",
	"blowjob":     "blowjob",
	"bdsm":        "bdsm",
	"blonde":      "blonde",
	"brunette":    "brunette",
	"bbw":         "bbw",
	"interracial": "interracial",
	"pov":         "pov",
	"threesome":   "threesome",
	"creampie":    "creampie",
	"hentai":      "hentai",
}

// Per-source search term overrides (e.g. PornLab uses Russian).
var sisiCategorySearchOverrides = map[string]map[string]string{
	"plab": {
		"russian": "русское", "anal": "анал", "asian": "азиатки",
		"milf": "милф", "teen": "молодые", "amateur": "любительское",
		"lesbian": "лесби", "big-tits": "большие сиськи", "blowjob": "минет",
		"bdsm": "бдсм", "blonde": "блондинка", "brunette": "брюнетка",
		"bbw": "толстушки", "interracial": "межрасовое", "pov": "от первого лица",
		"threesome": "тройка", "creampie": "кончил внутрь", "hentai": "хентай",
	},
	"sxst": {
		"russian": "русское", "anal": "анал", "asian": "азиатки",
		"milf": "зрелые", "teen": "молодые", "amateur": "домашнее",
		"lesbian": "лесби", "big-tits": "большие сиськи", "blowjob": "минет",
		"blonde": "блондинка", "brunette": "брюнетка",
	},
}

// sisiCategoryResult holds the resolved category for a specific source.
type sisiCategoryResult struct {
	CategoryValue string // source-specific category value (e.g. "Anal-12")
	UseSearch     bool   // true = use search instead of native category
	SearchTerm    string // search term to use when UseSearch is true
}

// resolveSisiCategory translates a unified category key into a source-specific value.
func resolveSisiCategory(plugin, cat string) sisiCategoryResult {
	if cat == "" {
		return sisiCategoryResult{}
	}

	// Check native category mapping first.
	if sourceMap, ok := sisiCategoryMapping[plugin]; ok {
		if val, ok := sourceMap[cat]; ok {
			return sisiCategoryResult{CategoryValue: val}
		}
	}

	// Fallback to search — check per-source overrides first.
	if overrides, ok := sisiCategorySearchOverrides[plugin]; ok {
		if term, ok := overrides[cat]; ok {
			return sisiCategoryResult{UseSearch: true, SearchTerm: term}
		}
	}

	// Generic search fallback.
	if term, ok := sisiCategorySearchTerms[cat]; ok {
		return sisiCategoryResult{UseSearch: true, SearchTerm: term}
	}

	// Unknown category — use key as search term.
	return sisiCategoryResult{UseSearch: true, SearchTerm: cat}
}

// sisiCategoriesHandler returns the list of unified categories as JSON.
func sisiCategoriesHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, sisiUnifiedCategories)
	}
}
