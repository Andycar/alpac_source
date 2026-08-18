package sisihttp

import (
	"net/http"
	"os"

	"lampac-go/internal/config"
	"lampac-go/internal/httpx"
	"lampac-go/internal/jsmodules"
	"lampac-go/internal/proxylink"
	"lampac-go/internal/torrs"
)

// deps.go — the injected seam from the former host (httpapi). Everything that
// reaches the running server (live config, auth, subsystem pools, torrs, the
// trusted-proxy client-IP resolver and the config-aware file reader) is provided
// once via Deps at RegisterRoutes time, so this package never imports httpapi.

func writeJSON(w http.ResponseWriter, code int, payload any) { httpx.WriteJSON(w, code, payload) }

// Deps bundles host dependencies injected at RegisterRoutes time.
type Deps struct {
	LiveConfig     func(fallback config.Config) config.Config
	ServerReady    func() bool
	KitAuth        func(r *http.Request, cfg config.Config) (tgID string, userName string, err error)
	ClientIP       func(r *http.Request) string
	ReadFileAny    func(rel string) ([]byte, bool)
	SisiSources    func() *jsmodules.Manager
	ProxyLinks     func() *proxylink.Manager
	GetTorrsServer func() *torrs.BTServer
}

var deps Deps

func setDeps(d Deps) { deps = d }

// Forwarders so the moved handlers keep their original call sites unchanged.

func liveConfig(fallback config.Config) config.Config {
	if deps.LiveConfig != nil {
		return deps.LiveConfig(fallback)
	}
	return fallback
}

func serverReady() bool { return deps.ServerReady != nil && deps.ServerReady() }

func kitAuthFromRequest(r *http.Request, cfg config.Config) (string, string, error) {
	if deps.KitAuth != nil {
		return deps.KitAuth(r, cfg)
	}
	return "", "", http.ErrNoCookie
}

func clientIP(r *http.Request) string {
	if deps.ClientIP != nil {
		return deps.ClientIP(r)
	}
	return ""
}

func readFileAny(rel string) ([]byte, bool) {
	if deps.ReadFileAny != nil {
		return deps.ReadFileAny(rel)
	}
	// Fallback for when the host dep is unwired (unit tests): resolve against the
	// runtime home and read the raw file. This mirrors the host readFileAny's
	// behavior when serverReady()==false — no TOML-as-JSON bridge, just os.ReadFile.
	data, err := os.ReadFile(relToRuntime(rel))
	if err != nil {
		return nil, false
	}
	return data, true
}

func liveSisiSources() *jsmodules.Manager {
	if deps.SisiSources != nil {
		return deps.SisiSources()
	}
	return nil
}

func liveProxyLinks() *proxylink.Manager {
	if deps.ProxyLinks != nil {
		return deps.ProxyLinks()
	}
	return nil
}

func getTorrsServer() *torrs.BTServer {
	if deps.GetTorrsServer != nil {
		return deps.GetTorrsServer()
	}
	return nil
}
