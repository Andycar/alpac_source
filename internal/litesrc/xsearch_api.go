package litesrc

import (
	"net/http"
	"strings"
	"sync/atomic"

	"lampac-go/internal/tmdbcache"
	"lampac-go/internal/xsearch"
)

// XSearchHandler serves GET /api/xsearch?q=...&type=movie|serial|any
func XSearchHandler(agg *xsearch.Aggregator, pool *tmdbcache.Pool, apiKey string) http.HandlerFunc {
	var totalQueries int64

	return func(w http.ResponseWriter, r *http.Request) {
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		if query == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "q parameter required"})
			return
		}

		contentType := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("type")))
		if contentType != "movie" && contentType != "serial" {
			contentType = "" // any
		}

		atomic.AddInt64(&totalQueries, 1)

		results, cached := agg.Search(r.Context(), query, contentType)

		// Enrich TMDB-only results with external IDs (imdb_id).
		if pool != nil && apiKey != "" && !cached {
			xsearch.EnrichWithExternalIDs(r.Context(), pool, apiKey, results)
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"results": results,
			"total":   len(results),
			"cached":  cached,
		})
	}
}

// XSearchSourcesHandler serves GET /api/xsearch/sources — list of active search sources.
func XSearchSourcesHandler(agg *xsearch.Aggregator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, agg.ActiveSources())
	}
}
