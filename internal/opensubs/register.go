package opensubs

import (
	"lampac-go/internal/config"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

// RegisterRoutes wires /api/opensubs/* into router when OpenSubtitles is enabled
// and an API key is present. Single composition-root entry — the service, cache
// and handlers all stay unexported in this package.
func RegisterRoutes(router chi.Router, cfg config.Config) {
	if !cfg.OpenSubs.Enable || cfg.OpenSubs.APIKey == "" {
		return
	}
	osSvc := newOpenSubsService(cfg)
	router.Get("/api/opensubs/search", openSubsSearchHandler(cfg))
	router.Get("/api/opensubs/download", osSvc.handleDownload)
	// Auto-sync: measure how far this subtitle runs from the stream's own audio
	// (?apply=1 → the shifted VTT). See sync.go.
	router.Get("/api/opensubs/sync", osSvc.handleSync)
	// Segmented WebVTT HLS rendition (timeline-aligned subtitle segments).
	router.Get("/api/opensubs/rendition/{fileID}/index.m3u8", osSvc.handleRendition)
	router.Get("/api/opensubs/rendition/{fileID}/seg_{i:[0-9]+}.vtt", osSvc.handleSegment)
	log.Info().Msg("opensubs: OpenSubtitles subtitle search enabled")
}
