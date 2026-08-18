package iptvhttp

import (
	"net/http"

	"lampac-go/internal/config"
	"lampac-go/internal/httpx"

	jsoniter "github.com/json-iterator/go"
)

// deps.go — the injected seam from the former host (httpapi). The three things
// the moved /api/iptv/* handlers reach into the running server for (live config,
// server-ready gate, trusted-proxy client-IP resolver) are provided once via Deps
// at RegisterRoutes time, so this package never imports httpapi. Everything else
// the handlers need is a pure helper copied into helpers.go.

// json mirrors the host's package-level jsoniter var so moved handlers keep using
// the same `json.Marshal`/`Unmarshal` call sites unchanged.
var json = jsoniter.ConfigCompatibleWithStandardLibrary

func writeJSON(w http.ResponseWriter, code int, payload any) { httpx.WriteJSON(w, code, payload) }

// Deps bundles host dependencies injected at RegisterRoutes time.
type Deps struct {
	LiveConfig  func(fallback config.Config) config.Config
	ServerReady func() bool
	ClientIP    func(r *http.Request) string
}

var deps Deps

func setDeps(d Deps) { deps = d }

func liveConfig(fallback config.Config) config.Config {
	if deps.LiveConfig != nil {
		return deps.LiveConfig(fallback)
	}
	return fallback
}

func serverReady() bool { return deps.ServerReady != nil && deps.ServerReady() }

func clientIP(r *http.Request) string {
	if deps.ClientIP != nil {
		return deps.ClientIP(r)
	}
	return ""
}
