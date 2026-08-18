package httpapi

import (
	"lampac-go/internal/config"
	"lampac-go/internal/litesrc"
	"lampac-go/internal/tmdbcache"
	"lampac-go/internal/xsearch"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

// Skip Intro (/api/skip*) moved to internal/skiphttp — the composition root now
// calls skiphttp.RegisterRoutes / skiphttp.RegisterAdminRoutes directly.

// Calendar (/api/calendar/*) moved to internal/calendarhttp — the composition
// root now calls calendarhttp.RegisterRoutes / calendarhttp.RegisterAdminRoutes.

// registerXSearchRoutes wires /api/xsearch* cross-balancer search when
// cfg.XSearch.Enable. Returns the aggregator for shutdown cleanup.
func registerXSearchRoutes(router chi.Router, cfg config.Config, tmdbPool *tmdbcache.Pool) *xsearch.Aggregator {
	if !cfg.XSearch.Enable {
		return nil
	}
	balancerSearchers := litesrc.BuildXSearchAdapters(cfg)
	var searchers []xsearch.TextSearcher
	if cfg.XSearch.TMDBSearch && tmdbPool != nil {
		searchers = append(searchers, xsearch.NewTMDBSearcher(tmdbPool, cfg.TMDBProxy.APIKey))
	}
	searchers = append(searchers, balancerSearchers...)
	agg := xsearch.NewAggregator(cfg.XSearch, searchers...)

	router.Get("/api/xsearch", litesrc.XSearchHandler(agg, tmdbPool, cfg.TMDBProxy.APIKey))
	router.Get("/api/xsearch/sources", litesrc.XSearchSourcesHandler(agg))
	log.Info().Int("sources", len(searchers)).Msg("xsearch: initialized")
	return agg
}

// Collections (/api/collections/*) moved to internal/collectionshttp — the
// composition root now calls collectionshttp.RegisterRoutes directly.

// IPTV (/api/iptv/*) moved to internal/iptvhttp — the composition root now calls
// iptvhttp.RegisterRoutes directly.
