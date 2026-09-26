package skipsrc

import (
	"context"
	"os"
	"testing"
)

// Live end-to-end probe against the real public APIs. Off by default — it needs
// network and the answers depend on third-party crowd-sourced data, so it is a
// diagnostic tool ("are the endpoints still alive and shaped the way we parse
// them?"), not a gate.
//
//	SKIPSRC_LIVE=1 go test ./internal/skipsrc/ -run Live -v
func TestLiveAniskipAnime(t *testing.T) {
	if os.Getenv("SKIPSRC_LIVE") == "" {
		t.Skip("set SKIPSRC_LIVE=1 to probe the real skip databases")
	}
	// Attack on Titan S01E01 — imdb tt2560140 maps to MAL 16498 through ARM, and
	// Aniskip has both an opening and an ending for it.
	segs := New().Lookup(context.Background(), Query{
		ImdbID: "tt2560140", TmdbID: -1, Season: 1, Episode: 1, Duration: 1540,
	})
	if len(segs) == 0 {
		t.Fatal("no segments resolved — endpoints changed, or the anime path broke")
	}
	var haveIntro, haveCredits bool
	for _, s := range segs {
		t.Logf("%-8s %7.1f–%7.1f  src=%-10s trust=%d votes=%d confirmed=%v",
			s.Category, s.Start, s.End, s.Source, s.Trust, s.Votes, s.Confirmed)
		switch s.Category {
		case CatIntro:
			haveIntro = true
			// The opening sits near the top of the episode, not at minute twenty.
			if s.Start > 300 {
				t.Errorf("intro starts at %.0fs — suspiciously late", s.Start)
			}
		case CatCredits:
			haveCredits = true
		}
	}
	if !haveIntro || !haveCredits {
		t.Errorf("expected both an opening and an ending, got intro=%v credits=%v", haveIntro, haveCredits)
	}
}

// Same call twice must hit the cache the second time (and not re-probe).
func TestLiveCacheHit(t *testing.T) {
	if os.Getenv("SKIPSRC_LIVE") == "" {
		t.Skip("set SKIPSRC_LIVE=1 to probe the real skip databases")
	}
	a := New()
	q := Query{ImdbID: "tt2560140", TmdbID: -1, Season: 1, Episode: 1, Duration: 1540}
	first := a.Lookup(context.Background(), q)
	if len(first) == 0 {
		t.Skip("nothing resolved; cache behaviour not observable")
	}
	if _, ok := a.fromCache(cacheKey(q)); !ok {
		t.Fatal("result was not cached")
	}
}

// Every source answers without failing — a dead host (410/503), a Cloudflare
// wall or a reshaped answer shows up here as an error, not as "0 segments".
// Breaking Bad S01E02 is known to SkipDB and IntroDB (2026-09-25).
func TestLiveSourcesHealthy(t *testing.T) {
	if os.Getenv("SKIPSRC_LIVE") == "" {
		t.Skip("set SKIPSRC_LIVE=1 to hit the real skip databases")
	}
	c := New().client
	ep := Query{ImdbID: "tt0903747", TmdbID: 1396, Season: 1, Episode: 2, Duration: 2880}
	anime := Query{ImdbID: "tt2560140", TmdbID: 1429, Season: 1, Episode: 1, Duration: 1540}
	for _, tc := range []struct {
		f fetcher
		q Query
	}{{fetchSkipDB, ep}, {fetchIntroDB, ep}, {fetchAniskip, anime}} {
		ctx, cancel := context.WithTimeout(context.Background(), probeDeadline)
		r := tc.f(ctx, c, tc.q)
		cancel()
		if r.err != nil {
			t.Errorf("%s failed: %v", r.name, r.err)
			continue
		}
		if len(r.segments) == 0 {
			t.Errorf("%s answered but knows nothing about a well-covered title", r.name)
		}
		t.Logf("%s: %d segments", r.name, len(r.segments))
	}
}
