package transcodesvc

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpx"
	"lampac-go/internal/torrbalancer"
	"lampac-go/internal/torrs"

	jsoniter "github.com/json-iterator/go"
)

// deps.go — the seam this extracted cluster needs from its former host (httpapi).
// Trivial things are local copies/forwarders; anything that reaches back into the
// running server (live config, the plugin-JS renderer, the torrent-balancer pool)
// is INJECTED once via Deps at Register time — the cluster never imports httpapi.

// --- trivial forwarders / copies ---

func writeJSON(w http.ResponseWriter, code int, payload any) {
	httpx.WriteJSON(w, code, payload)
}

func requestScheme(r *http.Request) string {
	if xf := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); xf != "" {
		return xf
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// json mirrors httpapi's package-level jsoniter alias.
var json = jsoniter.ConfigCompatibleWithStandardLibrary

// Deps bundles everything the transcode cluster needs from its host (httpapi),
// injected once at RegisterRoutes time so this package never imports httpapi.
type Deps struct {
	LiveConfig        func() config.Config
	PluginJS          func(templateFile, endpointPath string, cfg config.Config) http.HandlerFunc
	TSBalancerPool    func() *torrbalancer.Pool
	TorrsInProcess    func() bool
	GetTorrsServer    func() *torrs.BTServer
	PidtorExtractTR   func(rawQuery string) string
	PidtorTSAddMagnet func(ctx interface{ Deadline() (time.Time, bool) }, client *http.Client, tsHost string, headers map[string]string, magnet string) (string, error)
}

func setProviders(d Deps) {
	liveConfigProvider = d.LiveConfig
	pluginJSProvider = d.PluginJS
	tsBalancerPoolProvider = d.TSBalancerPool
	torrsIsInProcessProvider = d.TorrsInProcess
	getTorrsServerProvider = d.GetTorrsServer
	pidtorExtractTRProvider = d.PidtorExtractTR
	pidtorTSAddMagnetProvider = d.PidtorTSAddMagnet
}

// --- injected host dependencies (set by Register) ---

var (
	liveConfigProvider     func() config.Config
	pluginJSProvider       func(templateFile, endpointPath string, cfg config.Config) http.HandlerFunc
	tsBalancerPoolProvider func() *torrbalancer.Pool
)

// liveConfig returns the host's hot-reloaded config (falls back to the supplied
// value before wiring / in tests).
func liveConfig(fallback config.Config) config.Config {
	if liveConfigProvider != nil {
		return liveConfigProvider()
	}
	return fallback
}

// genericPluginJSHandler renders a Lampa plugin-JS template via the host's
// renderer (the transcode cluster serves /transcoding.js, /tracks.js, …).
func genericPluginJSHandler(templateFile, endpointPath string, cfg config.Config) http.HandlerFunc {
	if pluginJSProvider != nil {
		return pluginJSProvider(templateFile, endpointPath, cfg)
	}
	return func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) }
}

// liveTSBalancerPool returns the host's torrent-balancer pool (nil when none).
func liveTSBalancerPool() *torrbalancer.Pool {
	if tsBalancerPoolProvider != nil {
		return tsBalancerPoolProvider()
	}
	return nil
}

// --- injected torrent-server (torrs / pidtor) integration ---
// These bridge to the build-tagged torrs subsystem in the host; injecting them
// keeps this package free of the torrs/notorrs build tags.

var (
	torrsIsInProcessProvider  func() bool
	getTorrsServerProvider    func() *torrs.BTServer
	pidtorExtractTRProvider   func(rawQuery string) string
	pidtorTSAddMagnetProvider func(ctx interface{ Deadline() (time.Time, bool) }, client *http.Client, tsHost string, headers map[string]string, magnet string) (string, error)
)

func torrsIsInProcess() bool {
	return torrsIsInProcessProvider != nil && torrsIsInProcessProvider()
}

func getTorrsServer() *torrs.BTServer {
	if getTorrsServerProvider != nil {
		return getTorrsServerProvider()
	}
	return nil
}

func pidtorExtractTRFromQuery(rawQuery string) string {
	if pidtorExtractTRProvider != nil {
		return pidtorExtractTRProvider(rawQuery)
	}
	return ""
}

func pidtorTSAddMagnet(ctx interface{ Deadline() (time.Time, bool) }, client *http.Client, tsHost string, headers map[string]string, magnet string) (string, error) {
	return pidtorTSAddMagnetProvider(ctx, client, tsHost, headers, magnet)
}

// --- pure helper copies (no host state) ---

func relToRuntime(rel string) string {
	if root := strings.TrimSpace(os.Getenv("LAMPAC_GO_HOME")); root != "" {
		return filepath.Join(root, rel)
	}
	if root := strings.TrimSpace(os.Getenv("LAMPAC_GO_REPO_ROOT")); root != "" {
		return filepath.Join(root, rel)
	}
	return rel
}

func sanitizeForwardedReferer(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
