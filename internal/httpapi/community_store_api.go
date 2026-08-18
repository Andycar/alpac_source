package httpapi

import (
	stdjson "encoding/json"
	"io"
	"net/http"
	"time"

	"lampac-go/internal/tgauth"

	"github.com/rs/zerolog/log"
)

// communityStoreListHandler serves GET /api/community-plugins — public catalog
// listing with installed status. No admin auth required.
func communityStoreListHandler(
	registry *CustomPluginRegistry,
	cron *CommunityUpdateCron,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		catalog, err := cron.CachedCatalog(5 * time.Minute)
		if err != nil {
			// Fallback: return empty catalog, not an error.
			log.Warn().Err(err).Msg("community-store: catalog fetch failed")
			catalog = &CatalogResponse{}
		}

		statuses := buildCommunityStatus(registry, catalog)
		writeJSON(w, http.StatusOK, map[string]any{
			"catalog": statuses,
		})
	}
}

// communityStoreActionHandler serves POST /api/community-plugins — install/uninstall
// actions. Requires admin auth via tgTokenStore cookie or localhost.
func communityStoreActionHandler(
	registry *CustomPluginRegistry,
	cron *CommunityUpdateCron,
	store *tgauth.Store,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Check admin auth: either TG admin, or localhost fallback.
		if !communityStoreAuthCheck(r, store) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin access required"})
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}

		var action struct {
			Action string `json:"action"`
			Name   string `json:"name"`
		}
		if err := stdjson.Unmarshal(body, &action); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
			return
		}

		switch action.Action {
		case "install":
			catalog, err := cron.CachedCatalog(5 * time.Minute)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "catalog unavailable"})
				return
			}
			var entry *CatalogEntry
			for i := range catalog.Plugins {
				if catalog.Plugins[i].Name == action.Name {
					entry = &catalog.Plugins[i]
					break
				}
			}
			if entry == nil {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "plugin not found"})
				return
			}
			if err := installCommunityPlugin(registry, *entry); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})

		case "uninstall":
			registry.Unregister(action.Name)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})

		case "update":
			catalog, err := cron.CachedCatalog(5 * time.Minute)
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "catalog unavailable"})
				return
			}
			var entry *CatalogEntry
			for i := range catalog.Plugins {
				if catalog.Plugins[i].Name == action.Name {
					entry = &catalog.Plugins[i]
					break
				}
			}
			if entry == nil {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "plugin not found"})
				return
			}
			updated, err := updateCommunityPlugin(registry, *entry)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "updated": updated})

		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown action"})
		}
	}
}

// communityStoreAuthCheck checks if the request is from an admin.
// Returns true for localhost or any authenticated TG user (plugin install
// is a trusted action since only the server owner accesses the admin Lampa).
func communityStoreAuthCheck(r *http.Request, store *tgauth.Store) bool {
	// Localhost always allowed.
	ip := clientIP(r)
	if ip == "127.0.0.1" || ip == "::1" {
		return true
	}

	// Any authenticated user can manage plugins (self-hosted = admin).
	if store == nil {
		return false
	}
	cookie, err := r.Cookie("lampac_token")
	if err != nil {
		return false
	}
	_, ok := store.Lookup(cookie.Value)
	return ok
}
