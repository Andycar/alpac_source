package xsearch

import (
	"context"
	"strings"
	"sync"
	"time"
	"unicode"

	"lampac-go/internal/config"
)

// SearchResult is a single result from one balancer.
type SearchResult struct {
	Title         string `json:"title"`
	OriginalTitle string `json:"original_title,omitempty"`
	Year          int    `json:"year,omitempty"`
	KinopoiskID   int64  `json:"kinopoisk_id,omitempty"`
	ImdbID        string `json:"imdb_id,omitempty"`
	TmdbID        int    `json:"tmdb_id,omitempty"`
	Poster        string `json:"poster,omitempty"`
	ContentType   string `json:"type"` // "movie" | "serial"
	Balancer      string `json:"balancer"`
	Quality       string `json:"quality,omitempty"`
}

// TextSearcher is a balancer that supports free-text search.
type TextSearcher interface {
	SearchText(ctx context.Context, query string) ([]SearchResult, error)
	Name() string
}

// SourceInfo describes one balancer that has a given title.
type SourceInfo struct {
	Balancer string `json:"balancer"`
	Quality  string `json:"quality"`
}

// MergedResult is a deduplicated result with sources from multiple balancers.
type MergedResult struct {
	Title         string       `json:"title"`
	OriginalTitle string       `json:"original_title,omitempty"`
	Year          int          `json:"year,omitempty"`
	KinopoiskID   int64        `json:"kinopoisk_id,omitempty"`
	ImdbID        string       `json:"imdb_id,omitempty"`
	TmdbID        int          `json:"tmdb_id,omitempty"`
	Poster        string       `json:"poster,omitempty"`
	ContentType   string       `json:"type"`
	Sources       []SourceInfo `json:"sources"`
}

// Aggregator fans out text searches to multiple balancers and merges results.
type Aggregator struct {
	searchers []TextSearcher
	cfg       config.XSearchConfig

	cacheMu sync.RWMutex
	cache   map[string]*cacheEntry
}

type cacheEntry struct {
	results []MergedResult
	expires time.Time
}

// NewAggregator creates a cross-balancer search aggregator.
func NewAggregator(cfg config.XSearchConfig, searchers ...TextSearcher) *Aggregator {
	return &Aggregator{
		searchers: searchers,
		cfg:       cfg,
		cache:     make(map[string]*cacheEntry),
	}
}

// AddSearcher registers an additional text searcher.
func (a *Aggregator) AddSearcher(s TextSearcher) {
	a.searchers = append(a.searchers, s)
}

// ActiveSources returns the list of registered searcher names.
func (a *Aggregator) ActiveSources() []SourceInfo {
	out := make([]SourceInfo, 0, len(a.searchers))
	for _, s := range a.searchers {
		out = append(out, SourceInfo{Balancer: s.Name()})
	}
	return out
}

// Search runs a parallel text search across all registered balancers,
// deduplicates, and returns merged results.
func (a *Aggregator) Search(ctx context.Context, query, contentType string) ([]MergedResult, bool) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, false
	}

	cacheKey := normalizeForCache(query) + "|" + contentType

	// Check cache.
	if entry, ok := a.cacheGet(cacheKey); ok {
		return entry, true
	}

	timeout := time.Duration(a.cfg.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Fan-out to all searchers.
	type result struct {
		items []SearchResult
		err   error
	}
	ch := make(chan result, len(a.searchers))
	sem := make(chan struct{}, 8)

	for _, s := range a.searchers {
		go func(s TextSearcher) {
			sem <- struct{}{}
			defer func() { <-sem }()
			items, err := s.SearchText(ctx, query)
			ch <- result{items: items, err: err}
		}(s)
	}

	var all []SearchResult
	for range a.searchers {
		r := <-ch
		if r.err == nil && len(r.items) > 0 {
			all = append(all, r.items...)
		}
	}

	merged := merge(all, contentType, a.cfg.MaxResults)
	a.cacheSet(cacheKey, merged)
	return merged, false
}

func (a *Aggregator) cacheGet(key string) ([]MergedResult, bool) {
	a.cacheMu.RLock()
	e, ok := a.cache[key]
	a.cacheMu.RUnlock()
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return e.results, true
}

func (a *Aggregator) cacheSet(key string, results []MergedResult) {
	ttl := time.Duration(a.cfg.CacheTTLSec) * time.Second
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	a.cacheMu.Lock()
	a.cache[key] = &cacheEntry{results: results, expires: time.Now().Add(ttl)}
	// Lazy cleanup every 50 writes.
	if len(a.cache)%50 == 0 {
		now := time.Now()
		for k, v := range a.cache {
			if now.After(v.expires) {
				delete(a.cache, k)
			}
		}
	}
	a.cacheMu.Unlock()
}

// merge deduplicates SearchResult items into MergedResult list.
func merge(items []SearchResult, contentType string, maxResults int) []MergedResult {
	if maxResults <= 0 {
		maxResults = 30
	}

	type mergeKey struct {
		kpID int64
		imdb string
		tmdb int
		norm string // normalized title + year fallback
	}

	byKey := make(map[mergeKey]*MergedResult)
	var order []mergeKey

	for _, item := range items {
		// Filter by content type.
		if contentType == "movie" && item.ContentType != "" && item.ContentType != "movie" {
			continue
		}
		if contentType == "serial" && item.ContentType != "" && item.ContentType != "serial" {
			continue
		}

		// Find matching key.
		var key mergeKey
		var found *MergedResult

		// Try KP ID match.
		if item.KinopoiskID > 0 {
			k := mergeKey{kpID: item.KinopoiskID}
			if existing, ok := byKey[k]; ok {
				found = existing
				key = k
			}
		}
		// Try IMDB match.
		if found == nil && item.ImdbID != "" {
			k := mergeKey{imdb: item.ImdbID}
			if existing, ok := byKey[k]; ok {
				found = existing
				key = k
			}
		}
		// Try TMDB match.
		if found == nil && item.TmdbID > 0 {
			k := mergeKey{tmdb: item.TmdbID}
			if existing, ok := byKey[k]; ok {
				found = existing
				key = k
			}
		}
		// Try title+year match.
		if found == nil {
			norm := normalizeTitle(item.Title)
			if item.Year > 0 {
				norm += "|" + itoa(item.Year)
			}
			k := mergeKey{norm: norm}
			if existing, ok := byKey[k]; ok {
				found = existing
				key = k
			}
		}

		if found != nil {
			// Merge source into existing result.
			found.Sources = appendSource(found.Sources, item.Balancer, item.Quality)
			// Enrich missing fields.
			if found.KinopoiskID == 0 && item.KinopoiskID > 0 {
				found.KinopoiskID = item.KinopoiskID
			}
			if found.ImdbID == "" && item.ImdbID != "" {
				found.ImdbID = item.ImdbID
			}
			if found.TmdbID == 0 && item.TmdbID > 0 {
				found.TmdbID = item.TmdbID
			}
			if found.Poster == "" && item.Poster != "" {
				found.Poster = item.Poster
			}
			if found.OriginalTitle == "" && item.OriginalTitle != "" {
				found.OriginalTitle = item.OriginalTitle
			}
			if found.Year == 0 && item.Year > 0 {
				found.Year = item.Year
			}
			if found.ContentType == "" && item.ContentType != "" {
				found.ContentType = item.ContentType
			}
			continue
		}

		// Create new merged result.
		mr := &MergedResult{
			Title:         item.Title,
			OriginalTitle: item.OriginalTitle,
			Year:          item.Year,
			KinopoiskID:   item.KinopoiskID,
			ImdbID:        item.ImdbID,
			TmdbID:        item.TmdbID,
			Poster:        item.Poster,
			ContentType:   item.ContentType,
			Sources:       []SourceInfo{{Balancer: item.Balancer, Quality: item.Quality}},
		}

		// Register under all available keys.
		if item.KinopoiskID > 0 {
			k := mergeKey{kpID: item.KinopoiskID}
			byKey[k] = mr
			key = k
		}
		if item.ImdbID != "" {
			byKey[mergeKey{imdb: item.ImdbID}] = mr
			if key == (mergeKey{}) {
				key = mergeKey{imdb: item.ImdbID}
			}
		}
		if item.TmdbID > 0 {
			byKey[mergeKey{tmdb: item.TmdbID}] = mr
			if key == (mergeKey{}) {
				key = mergeKey{tmdb: item.TmdbID}
			}
		}
		{
			norm := normalizeTitle(item.Title)
			if item.Year > 0 {
				norm += "|" + itoa(item.Year)
			}
			byKey[mergeKey{norm: norm}] = mr
			if key == (mergeKey{}) {
				key = mergeKey{norm: norm}
			}
		}

		order = append(order, key)
	}

	// Build output in insertion order, sorted: more sources first, then best quality.
	seen := make(map[*MergedResult]bool)
	out := make([]MergedResult, 0, len(order))
	for _, k := range order {
		mr := byKey[k]
		if mr == nil || seen[mr] {
			continue
		}
		seen[mr] = true
		out = append(out, *mr)
	}

	// Sort: more sources → higher, best quality → higher.
	sortResults(out)

	if len(out) > maxResults {
		out = out[:maxResults]
	}
	return out
}

func appendSource(sources []SourceInfo, balancer, quality string) []SourceInfo {
	for _, s := range sources {
		if s.Balancer == balancer {
			return sources
		}
	}
	return append(sources, SourceInfo{Balancer: balancer, Quality: quality})
}

func sortResults(results []MergedResult) {
	// Stable sort: more sources first, then best quality.
	for i := 1; i < len(results); i++ {
		for j := i; j > 0; j-- {
			if compareResults(results[j], results[j-1]) {
				results[j], results[j-1] = results[j-1], results[j]
			} else {
				break
			}
		}
	}
}

func compareResults(a, b MergedResult) bool {
	// More sources = better.
	if len(a.Sources) != len(b.Sources) {
		return len(a.Sources) > len(b.Sources)
	}
	// Best quality among sources.
	return bestQuality(a.Sources) > bestQuality(b.Sources)
}

func bestQuality(sources []SourceInfo) int {
	best := 0
	for _, s := range sources {
		r := qualRank(s.Quality)
		if r > best {
			best = r
		}
	}
	return best
}

func qualRank(q string) int {
	switch q {
	case "4K":
		return 5
	case "2K":
		return 4
	case "FHD":
		return 3
	case "HD":
		return 2
	case "SD":
		return 1
	default:
		return 0
	}
}

func normalizeTitle(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var buf strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			buf.WriteRune(r)
		}
	}
	return buf.String()
}

func normalizeForCache(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := [20]byte{}
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte(n%10) + '0'
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
