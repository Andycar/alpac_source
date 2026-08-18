package adminhttp

import (
	stdjson "encoding/json"
	"io"
	"net/http"

	"lampac-go/internal/config"
	"lampac-go/internal/tgauth"

	"github.com/rs/zerolog/log"
)

// tgAdminBrandingHandler serves the admin branding API:
//
//	GET  /api/branding              — current values + built-in defaults
//	POST /api/branding              — save runtime override (database/branding.json)
//	                                  body: Branding JSON; empty fields = use default
//
// Auth: regular admin. Saves go through saveBrandingOverride which writes
// atomically and updates the in-memory store, so the new values take effect
// for every subsequent /online.js, /lampainit.js and Lampa index.html.
func tgAdminBrandingHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeBrandingSnapshot(w)
		case http.MethodPost:
			handleBrandingSave(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func writeBrandingSnapshot(w http.ResponseWriter) {
	repoRoot := "."
	if serverReady() {
		repoRoot = liveConfig(config.Config{}).Compat.RepoRoot
		if repoRoot == "" {
			repoRoot = "."
		}
	}
	override, hasOverride := loadBrandingOverride(repoRoot)
	writeJSON(w, http.StatusOK, map[string]any{
		"current":      currentBranding(),
		"defaults":     brandingDefaults,
		"override":     override,
		"has_override": hasOverride,
	})
}

func handleBrandingSave(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "read body: " + err.Error()})
		return
	}
	defer r.Body.Close()
	// Validate JSON generically (the host owns the Branding type + unmarshal).
	if len(body) > 0 {
		var probe map[string]any
		if err := stdjson.Unmarshal(body, &probe); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON: " + err.Error()})
			return
		}
	}

	repoRoot := "."
	if serverReady() {
		repoRoot = liveConfig(config.Config{}).Compat.RepoRoot
		if repoRoot == "" {
			repoRoot = "."
		}
	}
	if err := saveBrandingOverrideJSON(repoRoot, body); err != nil {
		log.Error().Err(err).Msg("branding: save failed")
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "save failed: " + err.Error()})
		return
	}
	log.Info().Str("name", currentBrandingName()).Msg("branding: override saved")

	// Echo the snapshot back so the UI doesn't need a follow-up GET.
	writeBrandingSnapshot(w)
}
