package skipsrc

import "testing"

func seg(cat Category, start, end float64, base CoordBase, trust int, src string) Segment {
	return Segment{Category: cat, Start: start, End: end, Base: base, Trust: trust, Source: src, Votes: 1}
}

// The headline case: a duration-aware source and a broadcast-timed one disagree
// by half a minute. Averaging them (36s) would land inside the episode; the
// answer must be the duration-aware number, because it was computed for this file.
func TestVotePrefersDurationAwareTiming(t *testing.T) {
	got := vote([]result{
		{name: "skipdb", signal: 0.9, segments: []Segment{
			seg(CatIntro, 12, 102, BaseDurationAware, TrustDurationAware, "skipdb")}},
		{name: "introdb", signal: 0.7, segments: []Segment{
			seg(CatIntro, 60, 150, BaseAbsolute, TrustAbsolute, "introdb")}},
	})
	if len(got) != 1 {
		t.Fatalf("want 1 segment, got %d", len(got))
	}
	if got[0].Start != 12 {
		t.Errorf("start = %v, want 12 (duration-aware source wins, never an average)", got[0].Start)
	}
	if !got[0].Confirmed || got[0].Votes != 2 {
		t.Errorf("two sources within tolerance must confirm: votes=%d confirmed=%v",
			got[0].Votes, got[0].Confirmed)
	}
}

// Anime is the deliberate exception: releases follow the broadcast cut, so
// Aniskip's absolute times outrank a duration-aware guess.
func TestVoteAnimeAbsoluteOutranksDurationAware(t *testing.T) {
	got := vote([]result{
		{name: "skipdb", signal: 0.4, segments: []Segment{
			seg(CatIntro, 100, 190, BaseDurationAware, TrustDurationAware, "skipdb")}},
		{name: "aniskip", signal: 0.9, segments: []Segment{
			seg(CatIntro, 85, 175, BaseAbsolute, TrustAnime, "aniskip")}},
	})
	if len(got) != 1 || got[0].Start != 85 {
		t.Fatalf("aniskip timing must win for anime, got %+v", got)
	}
}

// One source claiming an intro at 0:10 and another at 0:40 is one intro seen
// twice; the same source claiming two far-apart intros is not corroboration.
func TestVoteClustersAndKeepsSinglePerCategory(t *testing.T) {
	got := vote([]result{
		{name: "a", signal: 0.5, segments: []Segment{
			seg(CatIntro, 10, 90, BaseAbsolute, TrustAbsolute, "a"),
			seg(CatIntro, 600, 700, BaseAbsolute, TrustAbsolute, "a"), // phantom, far away
		}},
		{name: "b", signal: 0.5, segments: []Segment{
			seg(CatIntro, 40, 120, BaseAbsolute, TrustAbsolute, "b")}},
	})
	if len(got) != 1 {
		t.Fatalf("one intro expected, got %d: %+v", len(got), got)
	}
	if got[0].Votes != 2 {
		t.Errorf("votes = %d, want 2 (distinct sources only)", got[0].Votes)
	}
	if got[0].Start != 10 {
		t.Errorf("start = %v, want the earliest of the agreeing cluster", got[0].Start)
	}
}

// A lone source still produces a usable segment — coverage matters — but it is
// not marked confirmed, so a client may treat it more cautiously.
func TestVoteSingleSourceUnconfirmed(t *testing.T) {
	got := vote([]result{
		{name: "introdb", signal: 0.3, segments: []Segment{
			seg(CatCredits, 2400, 2500, BaseAbsolute, TrustAbsolute, "introdb")}},
	})
	if len(got) != 1 || got[0].Confirmed {
		t.Fatalf("single source must survive but stay unconfirmed, got %+v", got)
	}
}

// The 99999 "till the end" sentinel is credits-only; anywhere else it would
// swallow the rest of the episode.
func TestOpenEndedOnlyForCredits(t *testing.T) {
	out := appendSeg(nil, 100, 99999, CatIntro, BaseAbsolute, TrustAbsolute, "x")
	if len(out) != 0 {
		t.Errorf("open-ended intro must be dropped, got %+v", out)
	}
	out = appendSeg(nil, 2400, 99999, CatCredits, BaseAbsolute, TrustAbsolute, "x")
	if len(out) != 1 {
		t.Errorf("open-ended credits must be kept, got %+v", out)
	}
	if out := appendSeg(nil, 50, 50, CatIntro, BaseAbsolute, TrustAbsolute, "x"); len(out) != 0 {
		t.Errorf("zero-length segment must be dropped, got %+v", out)
	}
}

// Different rips of one episode must not share cached timings.
func TestCacheKeySplitsByRuntime(t *testing.T) {
	a := cacheKey(Query{ImdbID: "tt1", TmdbID: -1, Season: 1, Episode: 2, Duration: 2700})
	b := cacheKey(Query{ImdbID: "tt1", TmdbID: -1, Season: 1, Episode: 2, Duration: 2400})
	if a == b {
		t.Errorf("runtimes 45min and 40min must not share a key (%s)", a)
	}
	// Small differences (a few seconds of encoder padding) are the same cut.
	c := cacheKey(Query{ImdbID: "tt1", TmdbID: -1, Season: 1, Episode: 2, Duration: 2705})
	if a != c {
		t.Errorf("near-identical runtimes must share a key: %s vs %s", a, c)
	}
}

func TestNormCategory(t *testing.T) {
	cases := map[string]Category{
		"op": CatIntro, "Opening": CatIntro, "intro": CatIntro,
		"ed": CatCredits, "Ending": CatCredits, "outro": CatCredits, "Credits": CatCredits,
		"recap": CatRecap, "Previously on": CatRecap,
		"preview": CatPreview, "next episode": CatPreview,
		"whatever": CatUnknown,
	}
	for in, want := range cases {
		if got := normCategory(in); got != want {
			t.Errorf("normCategory(%q) = %v, want %v", in, got, want)
		}
	}
}
