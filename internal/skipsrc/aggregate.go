package skipsrc

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	perSourceTimeout = 5 * time.Second
	// One deadline for the whole fan-out: sources are probed in parallel, so the
	// user waits for the slowest, not for the sum. A lookup that outlives this is
	// worthless anyway — the episode is already playing.
	probeDeadline = 8 * time.Second

	positiveTTL = 24 * time.Hour // timings for a given cut do not change
	negativeTTL = 10 * time.Minute
	maxCache    = 4096
)

// Aggregator probes the public skip databases and caches the reconciled answer.
type Aggregator struct {
	client *http.Client

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	segments []Segment
	expires  time.Time
}

func New() *Aggregator {
	return &Aggregator{
		client: &http.Client{Timeout: perSourceTimeout},
		cache:  make(map[string]cacheEntry),
	}
}

// Lookup returns the reconciled segments for q, or nil when nothing is known.
func (a *Aggregator) Lookup(ctx context.Context, q Query) []Segment {
	if q.ImdbID == "" && q.TmdbID < 0 {
		return nil
	}
	key := cacheKey(q)
	if segs, ok := a.fromCache(key); ok {
		return segs
	}

	ctx, cancel := context.WithTimeout(ctx, probeDeadline)
	defer cancel()

	fetchers := sourcesFor(q)
	results := make([]result, len(fetchers))
	var wg sync.WaitGroup
	for i, f := range fetchers {
		wg.Add(1)
		go func(i int, f fetcher) {
			defer wg.Done()
			defer func() {
				// A malformed answer from a third-party API must not take the
				// server down with it.
				if r := recover(); r != nil {
					log.Warn().Interface("panic", r).Msg("skipsrc: source panicked")
				}
			}()
			results[i] = f(ctx, a.client, q)
		}(i, f)
	}
	wg.Wait()

	nonEmpty := results[:0:0]
	for _, r := range results {
		if len(r.segments) > 0 {
			nonEmpty = append(nonEmpty, r)
		}
	}
	segs := vote(nonEmpty)
	a.store(key, segs)
	if len(segs) > 0 {
		log.Debug().Str("imdb", q.ImdbID).Int("segments", len(segs)).
			Int("sources", len(nonEmpty)).Msg("skipsrc: resolved")
	}
	return segs
}

func (a *Aggregator) fromCache(key string) ([]Segment, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e, ok := a.cache[key]
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return append([]Segment(nil), e.segments...), true
}

func (a *Aggregator) store(key string, segs []Segment) {
	ttl := positiveTTL
	if len(segs) == 0 {
		// A miss is remembered only briefly: these databases are crowd-sourced,
		// so today's unknown episode is next week's known one.
		ttl = negativeTTL
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.cache) >= maxCache {
		a.cache = make(map[string]cacheEntry, maxCache/2)
	}
	a.cache[key] = cacheEntry{segments: segs, expires: time.Now().Add(ttl)}
}

// cacheKey includes a coarse runtime bucket. Without it a 4K remux would reuse
// the timings resolved for a web-rip of the same episode — different cut, same
// key, skip button in the wrong place.
func cacheKey(q Query) string {
	bucket := 0
	if q.Duration > 0 {
		bucket = int(math.Round(q.Duration/30)) * 30
	}
	return fmt.Sprintf("%s|%d|%d|%d|%d", q.ImdbID, q.TmdbID, q.Season, q.Episode, bucket)
}
