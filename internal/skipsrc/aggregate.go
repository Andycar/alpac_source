package skipsrc

import (
	"context"
	"errors"
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

	// A failing source is logged at once, then at most once per this interval
	// with the number of failures in between: loud enough to notice a dead or
	// reshaped database, quiet enough not to log every lookup.
	reportEvery = 15 * time.Minute
)

// Aggregator probes the public skip databases and caches the reconciled answer.
type Aggregator struct {
	client *http.Client

	mu    sync.Mutex
	cache map[string]cacheEntry

	health sourceHealth
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

	now := time.Now()
	for _, r := range results {
		// Canceled = the viewer left before the answer; not the source's fault.
		if r.err == nil || errors.Is(r.err, context.Canceled) {
			continue
		}
		if report, n := a.health.fail(r.name, now); report {
			log.Warn().Str("source", r.name).Int("failures", n).Err(r.err).
				Msg("skipsrc: source failed")
		}
	}

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

// sourceHealth rate-limits failure reports per source. Without them a dead
// database is indistinguishable from one that simply has no data — which is how
// SkipMe (410), IntroHater (503) and a reshaped IntroDB answer went unnoticed.
type sourceHealth struct {
	mu     sync.Mutex
	last   map[string]time.Time // when the source was last reported
	failed map[string]int       // failures since that report
}

// fail records one failure and says whether to log it now, with how many
// failures the line accounts for (this one included).
func (h *sourceHealth) fail(name string, now time.Time) (report bool, n int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.last == nil {
		h.last, h.failed = map[string]time.Time{}, map[string]int{}
	}
	h.failed[name]++
	if t, ok := h.last[name]; ok && now.Sub(t) < reportEvery {
		return false, 0
	}
	n = h.failed[name]
	h.last[name], h.failed[name] = now, 0
	return true, n
}
