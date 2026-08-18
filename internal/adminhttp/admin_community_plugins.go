package adminhttp

import (
	stdjson "encoding/json"
	"io"
	"net/http"

	"lampac-go/internal/config"
	"lampac-go/internal/tgauth"
)

// --- Community Plugins Admin API ---
//
// The catalog-touching flows (status build, install-by-name, update-by-name)
// are INJECTED as high-level closures (communityStatusPayload / communityInstall
// ByName / communityUpdateByName) built host-side where the concrete registry +
// cron live — so this handler never needs the internal CatalogEntry/
// CatalogResponse/CommunityPluginStatus types. registry/cron are interfaces.

func tgAdminCommunityPluginsHandler(
	store *tgauth.Store,
	adminStore *tgauth.AdminIDStore,
	registry CustomPlugins,
	cron CommunityCron,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		switch r.Method {
		case http.MethodGet:
			handleCommunityGet(w, cron)
		case http.MethodPost:
			handleCommunityPost(w, r, registry, cron)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

func handleCommunityGet(w http.ResponseWriter, cron CommunityCron) {
	cfg := liveConfig(config.Config{})
	writeJSON(w, http.StatusOK, map[string]any{
		"catalog":           communityStatusPayload(),
		"catalog_url":       cfg.Web.CommunityPluginURL,
		"auto_update_hours": cfg.Web.CommunityAutoUpdateHours,
		"pending_updates":   cron.PendingCount(),
		"last_check":        cron.LastCheck(),
		"last_error":        cron.LastError(),
	})
}

type communityAction struct {
	Action          string `json:"action"`
	Name            string `json:"name,omitempty"`
	CatalogURL      string `json:"catalog_url,omitempty"`
	AutoUpdateHours int    `json:"auto_update_hours,omitempty"`
	Enabled         bool   `json:"enabled,omitempty"`
	Autoload        bool   `json:"autoload,omitempty"`
}

func handleCommunityPost(w http.ResponseWriter, r *http.Request, registry CustomPlugins, cron CommunityCron) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	var action communityAction
	if err := stdjson.Unmarshal(body, &action); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
		return
	}

	switch action.Action {
	case "install":
		status, errMsg := communityInstallByName(action.Name)
		if errMsg != "" {
			writeJSON(w, status, map[string]any{"error": errMsg})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "uninstall":
		registry.Unregister(action.Name)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "update":
		status, updated, errMsg := communityUpdateByName(action.Name)
		if errMsg != "" {
			writeJSON(w, status, map[string]any{"error": errMsg})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "updated": updated})
	case "update_all":
		handleCommunityUpdateAll(w, cron)
	case "check_updates":
		handleCommunityCheckUpdates(w, cron)
	case "save_settings":
		handleCommunitySaveSettings(w, action)
	case "toggle":
		registry.SetEnabled(action.Name, action.Enabled)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "toggle_autoload":
		registry.SetAutoload(action.Name, action.Autoload)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unknown action: " + action.Action})
	}
}

func handleCommunityUpdateAll(w http.ResponseWriter, cron CommunityCron) {
	updated, err := cron.AutoUpdateNow()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "updated": updated})
}

func handleCommunityCheckUpdates(w http.ResponseWriter, cron CommunityCron) {
	pending, err := cron.CheckNow()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pending": pending})
}

func handleCommunitySaveSettings(w http.ResponseWriter, action communityAction) {
	if err := updateConfigTOMLMap(func(root map[string]any) {
		web, ok := root["web"].(map[string]any)
		if !ok {
			web = map[string]any{}
			root["web"] = web
		}
		web["community_plugins_url"] = action.CatalogURL
		web["community_auto_update_hours"] = int64(action.AutoUpdateHours)
	}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
