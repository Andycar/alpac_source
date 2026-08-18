// Package userdata is the per-user persistence + sync layer extracted from
// httpapi: watch-timecodes, bookmarks, generic storage + media blobs
// (timecode/bookmark/storage/media_api), the profile-migration endpoints
// (migrate_api), and the Lampa↔ALPAC SyncBridge that mirrors capi account
// mutations into the classic Lampa buckets (sync_bridge). The data-access
// functions and the sync bridge are one package because they share the same
// on-disk buckets + mutexes. The host wires the seam once via SetDeps.
package userdata

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	jsoniter "github.com/json-iterator/go"

	"lampac-go/internal/httpx"
)

// Deps are the host-side hooks with real dependency chains (the rest of the
// shared helpers are pure and copied below).
type Deps struct {
	// ClientIP resolves the real client IP honoring the trusted-proxy config.
	ClientIP func(r *http.Request) string
	// ReadFileAny reads a runtime-relative file, honoring the init.conf /
	// config-TOML bridge when the server is ready.
	ReadFileAny func(rel string) ([]byte, bool)
	// NwsBroadcast pushes a sync event to a user's connected devices via the
	// now-watching WS hub.
	NwsBroadcast func(senderConnID, uid, eventName string, eventData any)
}

var deps = Deps{
	// Unwired default mirrors the host's no-trusted-proxy path: the direct
	// peer IP from RemoteAddr (NOT "" — an empty IP drops the IP-based user
	// fallback in BookmarkUserID and 401s direct-handler unit tests).
	ClientIP: func(r *http.Request) string {
		if host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr)); err == nil && host != "" {
			return host
		}
		return strings.TrimSpace(r.RemoteAddr)
	},
	// Unwired default mirrors the host's serverReady()==false path: a plain
	// runtime-relative read (the TOML-as-JSON bridge is server-gated). An
	// nil/false default silently drops init.conf-driven config in direct
	// handler unit tests (the sisi lesson).
	ReadFileAny: func(rel string) ([]byte, bool) {
		b, err := os.ReadFile(relToRuntime(rel))
		if err != nil {
			return nil, false
		}
		return b, true
	},
	NwsBroadcast: func(string, string, string, any) {},
}

// SetDeps wires the host seam. Call once before serving the userdata routes.
func SetDeps(d Deps) {
	if d.ClientIP != nil {
		deps.ClientIP = d.ClientIP
	}
	if d.ReadFileAny != nil {
		deps.ReadFileAny = d.ReadFileAny
	}
	if d.NwsBroadcast != nil {
		deps.NwsBroadcast = d.NwsBroadcast
	}
}

// json mirrors httpapi/server.go — the monolith-compatible encoder.
var json = jsoniter.ConfigCompatibleWithStandardLibrary

func writeJSON(w http.ResponseWriter, status int, v any) { httpx.WriteJSON(w, status, v) }

func writeRawJSON(w http.ResponseWriter, payload []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

// --- pure shared helpers copied from httpapi (no dep chains) ---

func relToRuntime(rel string) string {
	if root := strings.TrimSpace(os.Getenv("LAMPAC_GO_HOME")); root != "" {
		return filepath.Join(root, rel)
	}
	if root := strings.TrimSpace(os.Getenv("LAMPAC_GO_REPO_ROOT")); root != "" {
		return filepath.Join(root, rel)
	}
	return rel
}

func cookieValue(r *http.Request, name string) string {
	cookie, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func requestProfileID(r *http.Request) string {
	pid := strings.TrimSpace(r.URL.Query().Get("profile_id"))
	if pid == "0" {
		return ""
	}
	return pid
}

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
	return scheme + "://" + host
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
