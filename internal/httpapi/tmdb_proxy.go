package httpapi

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/tmdbcache"

	"github.com/rs/zerolog/log"
)

// tmdbProxy is the HTTP handler for /tmdb/* requests.
// It uses an in-memory cache and a pool of upstream servers with automatic
// failover and healthcheck.
type tmdbProxy struct {
	cache      *tmdbcache.Cache
	pool       *tmdbcache.Pool
	imgCache   *tmdbcache.ImageCache // bounded on-disk cache for images; nil = disabled
	apiKey     string                // server-side TMDB API key
	baseTTLMin int                   // base TTL for cache entries (minutes)
}

// tmdbProxyHandler creates the /tmdb/* HTTP handler.
func tmdbProxyHandler(cache *tmdbcache.Cache, pool *tmdbcache.Pool, imgCache *tmdbcache.ImageCache, apiKey string, baseTTLMin int) http.Handler {
	if baseTTLMin <= 0 {
		baseTTLMin = 120
	}
	return &tmdbProxy{
		cache:      cache,
		pool:       pool,
		imgCache:   imgCache,
		apiKey:     strings.TrimSpace(apiKey),
		baseTTLMin: baseTTLMin,
	}
}

func (h *tmdbProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	switch {
	case strings.HasPrefix(path, "/tmdb/api/"):
		h.handleAPI(w, r)
	case strings.HasPrefix(path, "/tmdb/img/"):
		h.handleIMG(w, r)
	default:
		http.NotFound(w, r)
	}
}

// ---------- API requests (cached) ----------

func (h *tmdbProxy) handleAPI(w http.ResponseWriter, r *http.Request) {
	tail := strings.TrimPrefix(r.URL.Path, "/tmdb/api/")

	// Build clean query string for upstream.
	q := r.URL.Query()
	q.Del("account_email")
	q.Del("uid")
	q.Del("token")

	// Capture client API key for server-side features (collections, xsearch).
	if clientKey := q.Get("api_key"); clientKey != "" {
		h.pool.SetAPIKeyIfEmpty(clientKey)
	}

	// Inject server-side API key if client doesn't provide one.
	if h.apiKey != "" && q.Get("api_key") == "" {
		q.Set("api_key", h.apiKey)
	}

	query := q.Encode()

	// Cache lookup.
	cacheKey := tmdbcache.CacheKey(tail, q)
	result, body, headers, status := h.cache.Get(cacheKey)

	switch result {
	case tmdbcache.CacheHit:
		// Fresh cache hit — serve directly.
		h.writeResponse(w, body, headers, status)
		return

	case tmdbcache.CacheStale:
		// Stale but usable — serve stale, refresh in background.
		h.writeResponse(w, body, headers, status)

		if h.cache.MarkRefreshing(cacheKey) {
			go func() {
				defer h.cache.DoneRefreshing(cacheKey)
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				fr, err := h.pool.FetchAPI(ctx, tail, query)
				if err != nil {
					log.Debug().Err(err).Str("path", tail).Msg("tmdb: background refresh failed")
					return
				}
				h.cache.Put(cacheKey, fr.Body, fr.Headers, fr.Status, tail, h.baseTTLMin)
			}()
		}
		return

	case tmdbcache.CacheMiss:
		// Cache miss — fetch from upstream.
	}

	// Synchronous fetch from upstream pool.
	ctx := r.Context()
	fr, err := h.pool.FetchAPI(ctx, tail, query)
	if err != nil {
		// Warn, not Debug: every upstream failing means the client gets an empty stub
		// card and the user sees a blank/broken page — this must be visible in the log
		// of a default install, otherwise the only symptom is a JS error in Lampa.
		log.Warn().Err(err).Str("path", tail).Msg("tmdb: all upstreams failed, serving empty fallback")
		h.writeEmptyFallback(w, r)
		return
	}

	// Store in cache and serve.
	h.cache.Put(cacheKey, fr.Body, fr.Headers, fr.Status, tail, h.baseTTLMin)
	h.writeResponse(w, fr.Body, fr.Headers, fr.Status)
}

// ---------- Image requests ----------
//
// The web client routes ALL TMDB posters/backdrops through this proxy (not just on fallback) so
// image bandwidth stays on hosts we control instead of the external cub CDN (imagetmdb.com), which
// can be geo-blocked or disappear. We don't keep a server-side image cache (would bloat RSS — see the
// perf audit), so a long immutable Cache-Control is what keeps the load sane: TMDB image paths are
// content-addressed (the filename is a content hash) → the same path never changes, so browsers and
// any CDN can cache repeats hard and serve them without re-hitting us.
func (h *tmdbProxy) handleIMG(w http.ResponseWriter, r *http.Request) {
	tail := strings.TrimPrefix(r.URL.Path, "/tmdb/img/")

	// Disk cache hit: immutable content-addressed poster — serve without touching
	// the (slow/geo-blocked) upstream CDN. This dedups the common case of many
	// clients requesting the same popular title's poster.
	if body := h.imgCache.Get(tail); body != nil {
		h.writeResponse(w, body, map[string]string{
			"Content-Type":  contentTypeForImg(tail),
			"Cache-Control": "public, max-age=2592000, immutable",
		}, http.StatusOK)
		return
	}

	ctx := r.Context()
	fr, err := h.pool.FetchIMG(ctx, tail)
	if err != nil {
		if errors.Is(err, tmdbcache.ErrBulkheadFull) {
			// Concurrency cap hit — shed fast so a slow CDN can't pile up and
			// starve the rest of the server. no-store: never cache the busy
			// response over the immutable image path; the client just retries.
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Retry-After", "2")
			http.Error(w, "tmdb image proxy busy", http.StatusServiceUnavailable)
			return
		}
		log.Debug().Err(err).Str("path", tail).Msg("tmdb: img all upstreams failed")
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}

	// Store in the disk cache for the next client (2xx only — never cache an error body).
	if fr.Status == 0 || (fr.Status >= 200 && fr.Status < 300) {
		h.imgCache.Put(tail, fr.Body)
	}

	// Force a long immutable Cache-Control regardless of what the upstream sent — these paths are
	// immutable, and the client now depends on this path for every poster.
	if fr.Headers == nil {
		fr.Headers = make(map[string]string, 1)
	}
	fr.Headers["Cache-Control"] = "public, max-age=2592000, immutable"

	// Stream image response.
	h.writeResponse(w, fr.Body, fr.Headers, fr.Status)
}

// contentTypeForImg infers the Content-Type for a cached image from its path
// extension (TMDB posters/backdrops are jpg; logos are sometimes png/svg).
func contentTypeForImg(imgPath string) string {
	switch strings.ToLower(filepath.Ext(imgPath)) {
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	case ".gif":
		return "image/gif"
	default:
		return "image/jpeg"
	}
}

// ---------- Response writing ----------

func (h *tmdbProxy) writeResponse(w http.ResponseWriter, body []byte, headers map[string]string, status int) {
	for k, v := range headers {
		w.Header().Set(k, v)
	}
	if w.Header().Get("Content-Length") == "" {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	}
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// writeEmptyFallback returns a valid TMDB-style empty response that matches
// what Lampa expects. For detail endpoints (movie/tv with append_to_response),
// we include empty sub-objects so code like movie.translations.translations
// doesn't crash with "Cannot read properties of undefined".
func (h *tmdbProxy) writeEmptyFallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	appendFields := r.URL.Query().Get("append_to_response")
	if appendFields != "" {
		obj := emptyDetailCard()
		for field := range strings.SplitSeq(appendFields, ",") {
			field = strings.TrimSpace(field)
			if field == "" {
				continue
			}
			switch field {
			case "translations":
				obj["translations"] = map[string]any{"translations": []any{}}
			case "credits":
				obj["credits"] = map[string]any{"cast": []any{}, "crew": []any{}}
			case "videos":
				obj["videos"] = map[string]any{"results": []any{}}
			case "images":
				obj["images"] = map[string]any{"backdrops": []any{}, "posters": []any{}, "logos": []any{}}
			case "external_ids":
				obj["external_ids"] = map[string]any{}
			case "recommendations", "similar":
				obj[field] = map[string]any{"page": 1, "results": []any{}, "total_pages": 0, "total_results": 0}
			case "content_ratings":
				obj["content_ratings"] = map[string]any{"results": []any{}}
			case "release_dates":
				obj["release_dates"] = map[string]any{"results": []any{}}
			default:
				obj[field] = map[string]any{"results": []any{}}
			}
		}
		data, _ := json.Marshal(obj)
		_, _ = w.Write(data)
		return
	}

	if strings.Contains(r.URL.Path, "/collection/") {
		_, _ = w.Write([]byte(`{"id":0,"name":"","overview":"","parts":[]}`))
		return
	}

	_, _ = w.Write([]byte(`{"page":1,"results":[],"total_pages":0,"total_results":0}`))
}

// emptyDetailCard is the stub body served for a movie/tv detail request when every
// upstream failed. Lampa builds the full card page straight from this object without
// checking whether the fetch actually succeeded, and several of its helpers assume the
// TMDB shape: TMDB.parseCountries() returns the string '' when production_countries is
// missing and Start.create then calls countries.join(', ') on it ("countries.join is not
// a function"), Descriptiopn.create reads .length off genres/production_companies, and
// countSeasons() iterates seasons. So every array field Lampa touches must be present
// and empty — a bare {"id":0} takes the whole app down with a JS error screen.
// Movie and TV fields are both included: the request path carries an optional API
// version segment (/tmdb/api/3/tv/1399 vs /tmdb/api/tv/1399), so sniffing the type
// from it is easy to get wrong, and the extra keys are inert — Lampa branches on
// truthiness (card.name && …, !card.first_air_date), and "" behaves like absent.
func emptyDetailCard() map[string]any {
	return map[string]any{
		"id":                   0,
		"title":                "",
		"original_title":       "",
		"name":                 "",
		"original_name":        "",
		"overview":             "",
		"tagline":              "",
		"poster_path":          nil,
		"backdrop_path":        nil,
		"release_date":         "",
		"first_air_date":       "",
		"runtime":              0,
		"vote_average":         0,
		"number_of_seasons":    0,
		"number_of_episodes":   0,
		"genres":               []any{},
		"seasons":              []any{},
		"episode_run_time":     []any{},
		"production_countries": []any{},
		"production_companies": []any{},
		"spoken_languages":     []any{},
	}
}

// ---------- Stats endpoint ----------

func tmdbStatsHandler(cache *tmdbcache.Cache, pool *tmdbcache.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"cache":     cache.Stats(),
			"upstreams": pool.UpstreamStatsList(),
			"bulkhead":  pool.BulkheadStats(),
		})
	}
}
