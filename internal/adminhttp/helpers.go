package adminhttp

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"lampac-go/internal/httpx"
)

// adminPathFile is where the randomized admin URL prefix is persisted.
const adminPathFile = "database/tgauth/admin_path.txt"

// helpers.go — pure helpers copied verbatim from the host's shared-helper files
// (admin_init.go / admin_panel_helpers.go). None touch server state, so they are
// duplicated (not injected) to keep moved handlers' call sites unchanged. The
// host keeps its own copies for the files that stay behind.

func writeHTML(w http.ResponseWriter, code int, html string) { httpx.WriteHTML(w, code, html) }

func writeRawJSON(w http.ResponseWriter, payload []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

func cookieValue(r *http.Request, name string) string {
	cookie, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func relToRuntime(rel string) string {
	if root := strings.TrimSpace(os.Getenv("LAMPAC_GO_HOME")); root != "" {
		return filepath.Join(root, rel)
	}
	if root := strings.TrimSpace(os.Getenv("LAMPAC_GO_REPO_ROOT")); root != "" {
		return filepath.Join(root, rel)
	}
	return rel
}

func toBoolAny(v any) bool {
	if v == nil {
		return false
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true" || t == "1"
	default:
		return false
	}
}

func toStringAny(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return fmt.Sprintf("%v", t)
	case int64:
		return fmt.Sprintf("%d", t)
	default:
		return fmt.Sprint(v)
	}
}

// ensureMapChild / setNestedMap — pure TOML/JSON map navigation helpers copied
// from the host. ensureMapChild returns (creating if absent) root[key] as a map;
// setNestedMap walks/creates a dotted path and returns the leaf map.
func ensureMapChild(root map[string]any, key string) map[string]any {
	if child, ok := root[key].(map[string]any); ok && child != nil {
		return child
	}
	child := map[string]any{}
	root[key] = child
	return child
}

func setNestedMap(root map[string]any, path string) map[string]any {
	parts := strings.Split(path, ".")
	current := root
	for _, p := range parts {
		child, ok := current[p].(map[string]any)
		if !ok {
			child = map[string]any{}
			current[p] = child
		}
		current = child
	}
	return current
}

func randomAlphaNum(n int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	for i := range b {
		idx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		b[i] = charset[idx.Int64()]
	}
	return string(b)
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// hostFromRequest reconstructs the public-facing scheme://host, honoring
// X-Forwarded-Proto / X-Forwarded-Host from a reverse proxy.
func hostFromRequest(r *http.Request) string {
	scheme := "http"
	if xf := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); xf != "" {
		scheme = xf
	} else if r.TLS != nil {
		scheme = "https"
	}
	var host string
	if xfh := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); xfh != "" {
		if i := strings.IndexByte(xfh, ','); i > 0 {
			xfh = strings.TrimSpace(xfh[:i])
		}
		host = xfh
	}
	if host == "" {
		host = r.Host
	}
	if host == "" {
		host = r.RemoteAddr
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
	}
	return scheme + "://" + host
}
