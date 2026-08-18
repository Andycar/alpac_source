package collectionshttp

import (
	"net/http"

	"lampac-go/internal/config"
	"lampac-go/internal/httpx"
	"lampac-go/internal/tmdbcache"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

// writeJSON is the local copy of the host helper — the moved handlers call it
// unchanged. It just delegates to httpx so no host coupling remains.
func writeJSON(w http.ResponseWriter, code int, payload any) { httpx.WriteJSON(w, code, payload) }

// RegisterRoutes wires /api/collections/* (TMDB smart collections) when enabled.
// The cluster is fully self-contained: it needs only a TMDB cache pool and config
// values, so nothing from the former host is injected.
func RegisterRoutes(router chi.Router, cfg config.Config, tmdbPool *tmdbcache.Pool) {
	if !cfg.Collections.Enable || tmdbPool == nil {
		return
	}
	h := newCollectionsHandler(tmdbPool, cfg.TMDBProxy.APIKey, cfg.Collections, cfg.Compat.RepoRoot)

	router.Get("/api/collections/featured", h.handleFeatured)
	router.Get("/api/collections/trending", h.handleTrending)
	router.Get("/api/collections/person/search", h.handlePersonSearch)
	router.Get("/api/collections/person/popular", h.handlePersonPopular)
	router.Get("/api/collections/person/{personID}", h.handlePersonDetail)
	router.Get("/api/collections/person/{personID}/movies", h.handlePersonMovies)
	router.Get("/api/collections/person/{personID}/tv", h.handlePersonTV)
	router.Get("/api/collections/genre/list", h.handleGenreList)
	router.Get("/api/collections/genre/{genreID}/movies", h.handleGenreMovies)
	router.Get("/api/collections/genre/{genreID}/tv", h.handleGenreTV)
	router.Get("/api/collections/studio/search", h.handleStudioSearch)
	router.Get("/api/collections/studio/{studioID}/movies", h.handleStudioMovies)
	router.Get("/api/collections/stats", h.handleStats)
	router.Get("/api/collections/pinned", h.handlePinnedList)
	router.Post("/api/collections/pin", h.handlePin)
	router.Delete("/api/collections/pin/{personID}", h.handleUnpin)

	log.Info().Msg("collections: TMDB smart collections enabled")
}
