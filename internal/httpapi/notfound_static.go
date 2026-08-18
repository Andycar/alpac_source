package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"lampac-go/internal/config"
)

func notFoundHandler(cfg config.Config, customPlugins *CustomPluginRegistry) http.HandlerFunc {
	// Detect the active lampa subfolder once at init so static assets served
	// from the root (e.g. /css/app.css) can be resolved from
	// wwwroot/{lampaFolder}/css/app.css when <base href="/"> is used.
	settings := loadLampaIndexSettings(cfg.Compat.RepoRoot)
	lampaFolder := detectLampaFolder(cfg.Compat.RepoRoot, settings)

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && customPlugins != nil {
			// Hot-loaded custom JS plugins: /myplugin.js
			if strings.HasSuffix(r.URL.Path, ".js") {
				name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), ".js")
				if name != "" && !strings.Contains(name, "/") {
					if customPlugins.ServePlugin(name, w, r) {
						return
					}
				}
			}
			// Tokenized: /myplugin/js/{token}
			p := strings.TrimPrefix(r.URL.Path, "/")
			if parts := strings.SplitN(p, "/", 3); len(parts) == 3 && parts[1] == "js" {
				if customPlugins.ServePlugin(parts[0], w, r, parts[2]) {
					return
				}
			}
		}

		if (r.Method == http.MethodGet || r.Method == http.MethodHead) && tryServeWWWRootStatic(cfg.Compat.RepoRoot, lampaFolder, w, r) {
			return
		}
		http.NotFound(w, r)
	}
}

func methodNotAllowedHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// detectLampaFolder returns the lampa subfolder name (e.g. "lampa-main")
// derived from the active index path (e.g. "lampa-main/index.html").
func detectLampaFolder(cfgRoot string, settings lampaIndexSettings) string {
	index := strings.TrimSpace(settings.Index)
	if index == "" {
		index = detectDefaultLampaIndex(cfgRoot, settings)
	}
	if i := strings.IndexByte(index, '/'); i > 0 {
		return index[:i]
	}
	return ""
}

func tryServeWWWRootStatic(cfgRoot, lampaFolder string, w http.ResponseWriter, r *http.Request) bool {
	rel, ok := normalizeWWWRootPath(r.URL.Path)
	if !ok {
		return false
	}

	// Build the list of relative paths to try:
	// 1) the path as-is (e.g. "css/app.css")
	// 2) under the lampa subfolder (e.g. "lampa-main/css/app.css")
	rels := []string{rel}
	if lampaFolder != "" {
		rels = append(rels, filepath.Join(lampaFolder, rel))
	}

	for _, root := range []string{
		filepath.Join(cfgRoot, "wwwroot"),
		"wwwroot",
		"/home/wwwroot",
	} {
		for _, candidate := range rels {
			path := filepath.Join(root, candidate)
			info, err := os.Stat(path)
			if err != nil {
				continue
			}

			if info.IsDir() {
				indexPath := filepath.Join(path, "index.html")
				indexInfo, err := os.Stat(indexPath)
				if err != nil || indexInfo.IsDir() {
					continue
				}
				http.ServeFile(w, r, indexPath)
				return true
			}

			http.ServeFile(w, r, path)
			return true
		}
	}

	return false
}

func normalizeWWWRootPath(requestPath string) (string, bool) {
	clean := filepath.Clean("/" + strings.TrimSpace(requestPath))
	if clean == "/" {
		return "", false
	}

	rel := strings.TrimPrefix(clean, "/")
	if rel == "" || rel == "." {
		return "", false
	}
	if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", false
	}

	return rel, true
}
