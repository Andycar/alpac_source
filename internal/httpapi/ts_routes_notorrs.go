//go:build !torrs

package httpapi

import (
	"lampac-go/internal/config"
	"lampac-go/internal/torrs"

	"github.com/go-chi/chi/v5"
)

// registerTSRoutes registers TorrServer routes using the reverse proxy approach.
func registerTSRoutes(router chi.Router, cfg config.Config) {
	registerTSProxyRoutes(router, cfg)
}

func shutdownTSRoutes() {
	stopTorrServerProcess()
}

func torrsIsInProcess() bool {
	return false
}

// getTorrsServer returns nil when not built with torrs tag.
func getTorrsServer() *torrs.BTServer {
	return nil
}
