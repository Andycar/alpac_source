package rutracker

import (
	"context"
	"errors"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/transform"
)

// searchWorkTimeout bounds one whole search (listing + synchronous resolves)
// regardless of the caller's context, because the work is shared through
// singleflight: the first caller hanging up must not abort everyone else.
const searchWorkTimeout = 45 * time.Second

// backgroundResolveMax caps how many extra magnets one search resolves in the
// background. The point is to warm the permanent cache, not to crawl.
const backgroundResolveMax = 24

// Search returns releases for a free-text query. It is safe to call on a
// disabled client (returns nil, nil) so callers can stay unconditional.
func (c *Client) Search(ctx context.Context, query string) ([]Release, error) {
	query = strings.TrimSpace(query)
	if query == "" || !c.Enabled() {
		return nil, nil
	}
	if c.limiter.open() {
		return nil, errors.New("rutracker: source paused (circuit breaker)")
	}

	key := normalizeQuery(query)
	if rows, ok := c.search.get(key); ok {
		return rows, nil
	}

	res, err, _ := c.group.Do(key, func() (any, error) {
		// Detached context: shared work, own deadline.
		workCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), searchWorkTimeout)
		defer cancel()

		rows, err := c.doSearch(workCtx, query)
		if err != nil {
			return nil, err
		}
		c.search.put(key, rows, time.Duration(c.Config().SearchTTLMin)*time.Minute)
		return rows, nil
	})
	if err != nil {
		c.st.setErr(err)
		return nil, err
	}
	rows, _ := res.([]Release)
	return cloneRows(rows), nil
}

func (c *Client) doSearch(ctx context.Context, query string) ([]Release, error) {
	cfg := c.Config()
	if err := c.ensureSession(ctx); err != nil {
		return nil, err
	}

	target := c.searchURL(cfg, query)
	page, err := c.fetchListing(ctx, target)
	if err != nil {
		return nil, err
	}

	rows := parseListing(page, cfg.Host)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Seeders > rows[j].Seeders })
	if len(rows) > cfg.MaxResults {
		rows = rows[:cfg.MaxResults]
	}

	// Permanent cache first — a release resolved for any past query is free.
	for i := range rows {
		if m := c.hashes.get(rows[i].TopicID); m != "" {
			rows[i].Magnet = m
		}
	}

	c.resolveBatch(ctx, rows, cfg.ResolveTop, time.Duration(cfg.ResolveBudgetSec)*time.Second)
	c.scheduleBackgroundResolve(rows)

	c.st.addSearch(len(rows))
	log.Debug().Str("query", query).Int("rows", len(rows)).
		Int("resolved", countResolved(rows)).Msg("rutracker: search")
	return rows, nil
}

// fetchListing gets the search page, re-logging in once when the forum hands
// us a guest page (an expired bb_session answers 200, not 401).
func (c *Client) fetchListing(ctx context.Context, target string) (string, error) {
	resp, err := c.fetch(ctx, target, fetchOpts{})
	if err != nil {
		return "", err
	}
	if loggedInPage(resp.body) {
		return resp.body, nil
	}

	if strings.TrimSpace(c.Config().Cookie) != "" {
		// A hand-supplied cookie cannot be refreshed by us.
		return "", errGuest
	}
	c.invalidateSession()
	if err := c.ensureSession(ctx); err != nil {
		return "", err
	}
	resp, err = c.fetch(ctx, target, fetchOpts{})
	if err != nil {
		return "", err
	}
	if !loggedInPage(resp.body) {
		return "", errGuest
	}
	return resp.body, nil
}

// searchURL builds the listing request. The query is percent-encoded from
// windows-1251 bytes: the forum is a cp1251 application and decodes `nm` in
// that charset, so a UTF-8 encoded Cyrillic query comes out as mojibake and
// silently finds nothing.
func (c *Client) searchURL(cfg Config, query string) string {
	return cfg.Host + "/forum/tracker.php?nm=" + encodeCP1251Query(query) +
		// sort by seeders, descending — page 1 then holds the rows worth
		// resolving first.
		"&o=10&s=2"
}

var cp1251Encoder = encoding.ReplaceUnsupported(charmap.Windows1251.NewEncoder())

func encodeCP1251Query(s string) string {
	encoded, _, err := transform.String(cp1251Encoder, s)
	if err != nil {
		return url.QueryEscape(s)
	}
	return url.QueryEscape(encoded)
}

// resolveBatch fills in magnets for the top `limit` unresolved rows within
// `budget`. Rows are already seeder-sorted, so this is "resolve what a user is
// most likely to click".
func (c *Client) resolveBatch(ctx context.Context, rows []Release, limit int, budget time.Duration) {
	if limit <= 0 || len(rows) == 0 {
		return
	}
	deadline := time.Now().Add(budget)
	batchCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	var wg sync.WaitGroup
	var mu sync.Mutex
	picked := 0
	for i := range rows {
		if picked >= limit {
			break
		}
		if rows[i].Resolved() {
			continue
		}
		picked++
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			select {
			case c.topicSem <- struct{}{}:
				defer func() { <-c.topicSem }()
			case <-batchCtx.Done():
				return
			}
			magnet, err := c.resolveMagnet(batchCtx, rows[idx].TopicID)
			if err != nil || magnet == "" {
				return
			}
			mu.Lock()
			rows[idx].Magnet = magnet
			mu.Unlock()
		}(i)
	}
	wg.Wait()
}

var backgroundResolving atomic.Bool

// scheduleBackgroundResolve warms the permanent cache for the rows we did not
// resolve synchronously, so the next search on the same title starts hot. One
// job at a time process-wide — this is a nicety, never a crawl.
func (c *Client) scheduleBackgroundResolve(rows []Release) {
	pending := make([]int, 0, len(rows))
	for _, r := range rows {
		if !r.Resolved() {
			pending = append(pending, r.TopicID)
		}
	}
	if len(pending) == 0 {
		return
	}
	if len(pending) > backgroundResolveMax {
		pending = pending[:backgroundResolveMax]
	}
	if !backgroundResolving.CompareAndSwap(false, true) {
		return
	}

	go func() {
		defer backgroundResolving.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		resolved := map[int]string{}
		for _, topicID := range pending {
			if ctx.Err() != nil || c.limiter.open() {
				break
			}
			magnet, err := c.resolveMagnet(ctx, topicID)
			if err != nil || magnet == "" {
				continue
			}
			resolved[topicID] = magnet
		}
		if len(resolved) == 0 {
			return
		}
		c.search.updateMagnets(resolved)
		c.hashes.flush()
		log.Debug().Int("resolved", len(resolved)).Msg("rutracker: background magnet resolve done")
	}()
}

func normalizeQuery(q string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(q))), " ")
}

func countResolved(rows []Release) int {
	n := 0
	for _, r := range rows {
		if r.Resolved() {
			n++
		}
	}
	return n
}
