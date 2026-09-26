package skiphttp

import (
	"lampac-go/internal/config"
	"lampac-go/internal/skipdb"
	"lampac-go/internal/tgauth"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

// RegisterRoutes wires the public /api/skip* endpoints when cfg.SkipIntro.Enable
// and returns the opened skipdb.DB for admin-panel access and shutdown
// coordination (nil when disabled). hostDeps injects the admin-auth gate used by
// the admin routes registered later via RegisterAdminRoutes.
func RegisterRoutes(router chi.Router, cfg config.Config, hostDeps Deps) *skipdb.DB {
	if !cfg.SkipIntro.Enable {
		return nil
	}
	setDeps(hostDeps)
	db := skipdb.New(cfg.Compat.RepoRoot, skipdb.Defaults{
		IntroDefaultSec: cfg.SkipIntro.IntroDefaultSec,
		OutroOffsetSec:  cfg.SkipIntro.OutroOffsetSec,
		EnableUserMarks: cfg.SkipIntro.EnableUserMarks,
	})
	router.Get("/api/skip", skipLookupHandler(db, cfg))
	router.Post("/api/skip/mark", skipMarkHandler(db))
	// Авто-детект заставок по звуку (introdetect): клиент присылает адреса серий пака.
	if det := newDetector(cfg, db); det != nil {
		router.Post("/api/skip/detect", skipDetectHandler(db, det))
	}
	shows, eps := db.Stats()
	log.Info().Int("shows", shows).Int("episodes", eps).Msg("skipdb: loaded")
	return db
}

// RegisterAdminRoutes wires the admin-panel /{adminPath}/api/skip* endpoints.
// Called from the host's admin router (which owns adminPath + the AdminIDStore);
// only invoked when the skip DB exists, i.e. after RegisterRoutes ran setDeps.
func RegisterAdminRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore, db *skipdb.DB) {
	router.Get("/"+adminPath+"/api/skip", tgAdminSkipHandler(tgStore, adminStore, db))
	router.Post("/"+adminPath+"/api/skip", tgAdminSkipSetHandler(tgStore, adminStore, db))
	router.Delete("/"+adminPath+"/api/skip/{imdbID}/{season}/{episode}", tgAdminSkipDeleteHandler(tgStore, adminStore, db))
	router.Get("/"+adminPath+"/api/skip/marks", tgAdminSkipMarksHandler(tgStore, adminStore, db))
	router.Post("/"+adminPath+"/api/skip/marks", tgAdminSkipMarkActionHandler(tgStore, adminStore, db))
}
