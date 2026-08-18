package skipsrc

import "sort"

const (
	// Two sources landing within half a minute of each other are talking about
	// the same intro — releases differ by a logo here, a trimmed recap there.
	// Wider than that and unrelated segments start "agreeing".
	agreeToleranceSec = 30.0
	// Agreement means independent sources, not one source repeating itself.
	minVotes = 2
)

// vote turns the raw per-source answers into at most one segment per category.
//
// The naive alternative — take the first source that answered — is what makes
// skip buttons fire mid-scene: whichever database replied first wins, including
// when it is the one quoting broadcast timings for a re-cut release. Here every
// source votes, and the timing of the winning cluster is taken from its most
// trustworthy member rather than averaged: an absolute start and a
// duration-aware start are answers to different questions, and their mean is an
// answer to neither.
func vote(results []result) []Segment {
	byCat := map[Category][]Segment{}
	for _, r := range results {
		for _, s := range r.segments {
			if s.Category == CatUnknown {
				continue
			}
			s.Signal = r.signal
			byCat[s.Category] = append(byCat[s.Category], s)
		}
	}

	out := make([]Segment, 0, len(byCat))
	for cat, segs := range byCat {
		if best, ok := bestCluster(segs); ok {
			best.Category = cat
			out = append(out, best)
		}
	}
	// Chronological — clients walk the list in playback order.
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

// bestCluster groups segments of one category by proximity and returns the
// representative of the strongest group.
func bestCluster(segs []Segment) (Segment, bool) {
	if len(segs) == 0 {
		return Segment{}, false
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].Start < segs[j].Start })

	var clusters [][]Segment
	cur := []Segment{segs[0]}
	for _, s := range segs[1:] {
		prev := cur[len(cur)-1]
		// Same cluster when the starts are close, or when the regions overlap at
		// all — a source that reports a longer intro still means that intro.
		if s.Start-prev.Start <= agreeToleranceSec || s.Start < prev.End {
			cur = append(cur, s)
			continue
		}
		clusters = append(clusters, cur)
		cur = []Segment{s}
	}
	clusters = append(clusters, cur)

	var best Segment
	bestSources, bestTrust, found := 0, -1, false
	for _, cl := range clusters {
		rep, sources := representative(cl)
		switch {
		case !found,
			sources > bestSources,
			sources == bestSources && rep.Trust > bestTrust,
			sources == bestSources && rep.Trust == bestTrust && rep.Start < best.Start:
			best, bestSources, bestTrust, found = rep, sources, rep.Trust, true
		}
	}
	if !found {
		return Segment{}, false
	}
	best.Votes = bestSources
	best.Confirmed = bestSources >= minVotes
	return best, true
}

// representative picks whose numbers the cluster speaks with: highest trust,
// then strongest source signal. Returns the number of DISTINCT sources backing
// it — the same database answering twice is not corroboration.
func representative(cluster []Segment) (Segment, int) {
	seen := map[string]bool{}
	rep := cluster[0]
	for _, s := range cluster {
		seen[s.Source] = true
		if s.Trust > rep.Trust || (s.Trust == rep.Trust && s.Signal > rep.Signal) {
			rep = s
		}
	}
	return rep, len(seen)
}
