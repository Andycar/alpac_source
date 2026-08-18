package dlnahttp

import (
	"net/http"

	"lampac-go/internal/config"
	"lampac-go/internal/httpx"
)

// deps.go — thin seam from the former host (httpapi). writeJSON is a local
// forwarder; the plugin-JS renderer is injected once via Deps at Register time.

func writeJSON(w http.ResponseWriter, code int, payload any) {
	httpx.WriteJSON(w, code, payload)
}

// Deps injects host dependencies this package can't reach on its own.
type Deps struct {
	PluginJS func(templateFile, endpointPath string, cfg config.Config) http.HandlerFunc
}

var pluginJSProvider func(templateFile, endpointPath string, cfg config.Config) http.HandlerFunc

// genericPluginJSHandler renders a Lampa plugin-JS template via the host renderer
// (dlna serves /dlna.js). Falls back to 404 before wiring.
func genericPluginJSHandler(templateFile, endpointPath string, cfg config.Config) http.HandlerFunc {
	if pluginJSProvider != nil {
		return pluginJSProvider(templateFile, endpointPath, cfg)
	}
	return func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) }
}
