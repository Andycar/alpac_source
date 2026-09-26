package litesrc

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/sync/singleflight"
)

// kino.pub's API is multi-node and throttles volume: capi drills kinopub on every resolve for
// every viewer, so the same handful of titles is asked for over and over. Measured on prod
// 2026-08-27: 677 searches in an hour but only 351 distinct queries — 48% pure repeats, with
// «Холод» alone asked 37 times. That volume is what earns the HTTP 429/403 storm, and each
// throttled v1 answer then falls through to the v1.1 endpoint, which currently blackholes on
// some nodes and stalls the whole resolve.
//
// So: remember answers briefly, and collapse concurrent askers into one upstream call.
type kpSearchEntry struct {
	items []kinoPubItem
	ok    bool
	at    time.Time
}

var (
	kpSearchMu    sync.Mutex
	kpSearchCache = map[string]kpSearchEntry{}
	kpSearchGroup singleflight.Group
)

const (
	// A catalogue entry does not appear or vanish within minutes, so a short positive TTL is
	// plenty to absorb the repeat traffic without serving anything stale.
	kpSearchTTL = 10 * time.Minute
	// A miss is remembered for much less: it is often the throttling talking, not a real absence.
	kpSearchNegTTL  = 90 * time.Second
	kpSearchMaxKeys = 4000
	// kpFallbackBudget caps the SECOND endpoint attempt. v1.1 nodes intermittently accept the
	// connection and never answer (measured: 3 of 6 requests hung for the full 25s timeout while
	// the rest returned in 0.4s). As a fallback it must fail fast — spending the caller's whole
	// resolve budget on it turns a recoverable miss into a dead source.
	kpFallbackBudget = 4 * time.Second
)

func kpSearchKey(query string) string {
	return strings.ToLower(strings.TrimSpace(query))
}

func kpSearchLookup(key string) (kpSearchEntry, bool) {
	kpSearchMu.Lock()
	defer kpSearchMu.Unlock()
	e, ok := kpSearchCache[key]
	if !ok {
		return kpSearchEntry{}, false
	}
	ttl := kpSearchTTL
	if !e.ok || len(e.items) == 0 {
		ttl = kpSearchNegTTL
	}
	if time.Since(e.at) > ttl {
		delete(kpSearchCache, key)
		return kpSearchEntry{}, false
	}
	return e, true
}

func kpSearchStore(key string, items []kinoPubItem, ok bool) {
	kpSearchMu.Lock()
	defer kpSearchMu.Unlock()
	if len(kpSearchCache) >= kpSearchMaxKeys {
		now := time.Now()
		for k, e := range kpSearchCache {
			if now.Sub(e.at) > kpSearchNegTTL {
				delete(kpSearchCache, k)
			}
		}
		if len(kpSearchCache) >= kpSearchMaxKeys {
			kpSearchCache = map[string]kpSearchEntry{}
		}
	}
	kpSearchCache[key] = kpSearchEntry{items: items, ok: ok, at: time.Now()}
}

// kpBoundedReq returns req with a context that cannot outlive d — used for fallback endpoint
// attempts so one blackholed node cannot consume the caller's entire budget. The cancel func
// must be called by the caller.
func kpBoundedReq(req *http.Request, d time.Duration) (*http.Request, context.CancelFunc) {
	if req == nil {
		return req, func() {}
	}
	ctx, cancel := context.WithTimeout(req.Context(), d)
	return req.WithContext(ctx), cancel
}

// searchAPI answers from the short-lived cache when it can, and otherwise lets exactly one
// caller per query talk to the API while the rest wait for its answer.
func (k *kinoPubChecker) searchAPI(req *http.Request, token, query string) ([]kinoPubItem, bool) {
	key := kpSearchKey(query)
	if key == "" {
		return k.searchAPIUncached(req, token, query)
	}
	if e, hit := kpSearchLookup(key); hit {
		log.Debug().Str("q", query).Int("items", len(e.items)).Bool("ok", e.ok).
			Msg("kinopub: search served from cache")
		return e.items, e.ok
	}

	v, _, _ := kpSearchGroup.Do(key, func() (any, error) {
		items, ok := k.searchAPIUncached(req, token, query)
		// A result that failed only because THIS caller went away says nothing about the query;
		// caching it would punish everyone else waiting on the same title.
		if req == nil || req.Context().Err() == nil {
			kpSearchStore(key, items, ok)
		}
		return kpSearchEntry{items: items, ok: ok, at: time.Now()}, nil
	})
	e, _ := v.(kpSearchEntry)
	return e.items, e.ok
}
