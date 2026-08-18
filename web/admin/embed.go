// Package adminweb owns the static asset bundle for the v2 admin panel +
// Telegram WebApp. The frontend source lives at web/admin/** in the repo and
// is embedded into the binary via `go:embed` so the server still ships as a
// single file.
//
// See web/admin/README.md for the source layout and how to add a page.
package adminweb

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed all:styles all:components all:pages all:vendor all:legacy
//go:embed index.html tg.html app.js router.js api.js tg-bridge.js icons.js
var FS embed.FS

// Sub returns a fs.FS rooted at the package's embed root (so callers can
// http.FileServer it without a "/web/admin/" prefix in URLs).
func Sub() fs.FS {
	return FS
}

// FileServer returns an http.Handler that serves the embedded admin v2 bundle.
// Use it under a sub-route like /{adminPath}/v2/.
func FileServer() http.Handler {
	return http.FileServer(http.FS(FS))
}
