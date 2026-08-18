package httpapi

import (
	"lampac-go/internal/kpcatalog"
	"net/http"
)

// kpClientSingleton is initialized once in server.go when KP token is configured.
// nil means KP catalog is disabled.
var kpClientSingleton *kpcatalog.KPClient

func catalogIndexHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		result := map[string]any{}
		if kpClientSingleton != nil {
			result["КиноПоиск"] = kpClientSingleton.BuildCatalogDef()
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func catalogListHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		source := r.URL.Query().Get("source")
		if source == "kp" && kpClientSingleton != nil {
			kpClientSingleton.HandleList(w, r)
			return
		}
		// Default: empty results (backward compat).
		writeJSON(w, http.StatusOK, map[string]any{
			"results":       []any{},
			"page":          1,
			"total_pages":   1,
			"total_results": 0,
		})
	}
}

func catalogCardHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plugin := r.URL.Query().Get("plugin")
		// catalog.js sends plugin=КиноПоиск (source name); also accept "kp" for direct API use.
		if (plugin == "kp" || plugin == "КиноПоиск") && kpClientSingleton != nil {
			kpClientSingleton.HandleCard(w, r)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{})
	}
}
