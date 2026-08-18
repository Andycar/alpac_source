package httpapi

import (
	"crypto/sha512"
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"lampac-go/internal/config"
)

// widgetBuildMu serializes widget builds so concurrent requests for the same
// host don't race writing the cache file.
var widgetBuildMu sync.Mutex

// resolveWidgetHost returns the host URL (scheme+host, no trailing slash) to
// embed in generated widget resources. `?overwritehost=` wins if provided.
func resolveWidgetHost(r *http.Request) string {
	if h := strings.TrimSpace(r.URL.Query().Get("overwritehost")); h != "" {
		return strings.TrimRight(h, "/")
	}
	return strings.TrimRight(hostFromRequest(r), "/")
}

// widgetsSourceDir returns the first existing `data/widgets/<sub>` directory,
// or "" if none exists. Checks repo-root first, then CWD, then /home/data.
func widgetsSourceDir(cfg config.Config, sub string) string {
	candidates := []string{
		filepath.Join(cfg.Compat.RepoRoot, "data", "widgets", sub),
		filepath.Join("data", "widgets", sub),
		filepath.Join("/home/data/widgets", sub),
	}
	for _, p := range candidates {
		if p == filepath.Join("", "data", "widgets", sub) {
			continue
		}
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
	}
	return ""
}

// widgetsCacheFile returns the path where a built widget archive should be
// cached, creating the parent directory on demand.
func widgetsCacheFile(cfg config.Config, name string) string {
	base := filepath.Join(cfg.Compat.RepoRoot, "data", "widgets")
	if cfg.Compat.RepoRoot == "" {
		base = filepath.Join("data", "widgets")
	}
	_ = os.MkdirAll(base, 0o755)
	return filepath.Join(base, name)
}

func sha512Base64(data []byte) string {
	sum := sha512.Sum512(data)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// isTextWidgetFile reports whether the widget file should have {localhost}
// substituted. Binaries (png, jpg, ...) are copied as-is.
func isTextWidgetFile(name string) bool {
	l := strings.ToLower(name)
	switch filepath.Ext(l) {
	case ".html", ".htm", ".js", ".json", ".css", ".xml", ".txt", ".svg":
		return true
	}
	return false
}
