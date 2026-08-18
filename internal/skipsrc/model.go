// Package skipsrc aggregates intro/recap/credits timings from the public skip
// databases and reconciles their disagreements into one answer.
//
// Why an aggregator at all: a single source covers a fraction of titles, so any
// one of them alone leaves most episodes without a skip button. Six of them
// together cover a lot — but they contradict each other, and the contradictions
// are not symmetric (see CoordBase below), so simply taking "the first non-empty
// answer" produces skips that fire in the middle of a scene.
//
// This lives on the server on purpose: every client (web, Android TV, tvOS,
// Tizen) then gets the same coverage from one lookup, the API keys and the
// per-source quirks stay in one place, and the cache is shared instead of being
// re-earned on every device.
package skipsrc

import "strings"

// Category is what a segment actually is. The public APIs each use their own
// vocabulary ("op"/"ed", "outro"/"credits", labels in free text); everything is
// normalised to these so segments from different sources can be compared at all.
type Category string

const (
	CatIntro   Category = "intro"
	CatRecap   Category = "recap"
	CatCredits Category = "credits"
	CatPreview Category = "preview"
	CatUnknown Category = "unknown"
)

// CoordBase records WHICH TIMELINE a segment's numbers belong to — the single
// most important property here.
//
//   - DurationAware: the source was told this file's runtime and answered in ITS
//     coordinates. Usable as-is.
//   - Absolute: the source answers in broadcast-cut seconds. A web-rip with the
//     recap trimmed, or a release with a distributor logo bolted on the front, is
//     shifted by seconds to minutes against those numbers.
//
// Mixing the two — averaging an absolute start with a duration-aware one — is
// what produces a skip button that jumps into the middle of the episode. So the
// rule throughout this package is: never average across bases, pick a winner.
type CoordBase string

const (
	BaseDurationAware CoordBase = "duration"
	BaseAbsolute      CoordBase = "absolute"
	BaseCurated       CoordBase = "curated" // our own hand-checked DB
)

// Time trust ranks the coordinate systems. Our curated entries win over anything
// public; a source that was given the runtime wins over one that was not.
const (
	TrustCurated      = 400
	TrustAnime        = 250 // Aniskip on anime: files are usually the broadcast cut, so its absolute times fit
	TrustDurationAware = 200
	TrustSkipMe       = 190 // duration-aware, but observed to drift
	TrustAbsolute     = 100
)

// Segment is one skippable region, in seconds, tagged with where its numbers
// came from.
type Segment struct {
	Category  Category  `json:"type"`
	Start     float64   `json:"start"`
	End       float64   `json:"end"`
	Base      CoordBase `json:"-"`
	Trust     int       `json:"-"`
	Source    string    `json:"source,omitempty"`
	Signal    float64   `json:"-"`          // 0..1 source-reported confidence
	Confirmed bool      `json:"confirmed"`  // backed by >= MinVotes independent sources
	Votes     int       `json:"votes"`      // how many sources agreed
}

// normCategory maps a source's own wording onto Category.
func normCategory(s string) Category {
	t := strings.ToLower(strings.TrimSpace(s))
	switch {
	case strings.Contains(t, "recap"), strings.Contains(t, "previously"):
		return CatRecap
	case strings.Contains(t, "preview"), strings.Contains(t, "next"):
		return CatPreview
	case strings.Contains(t, "credit"), strings.Contains(t, "outro"), strings.Contains(t, "ending"), t == "ed":
		return CatCredits
	case strings.Contains(t, "intro"), strings.Contains(t, "opening"), t == "op":
		return CatIntro
	default:
		return CatUnknown
	}
}

// legacyType maps back to the wire vocabulary /api/skip has always used, so
// existing clients keep working unchanged.
func (c Category) LegacyType() string {
	switch c {
	case CatCredits:
		return "outro"
	case CatRecap:
		return "recap"
	case CatPreview:
		return "preview"
	default:
		return "intro"
	}
}
