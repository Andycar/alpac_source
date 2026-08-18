// Package litesrc is the growing home of the online-source ("/lite/*")
// checkers extracted from internal/httpapi — the adminhttp pattern applied to
// the source front. Sources move here file-by-file; the host (httpapi) wires
// the seam once via SetDeps at the top of liteSourceHandler, before any
// checker is constructed.
//
// Pilot family: rezka (PidoRezka) + rhsprem + ahuerezka.
package litesrc

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"

	jsoniter "github.com/json-iterator/go"

	"lampac-go/internal/browsergate"
	"lampac-go/internal/config"
	"lampac-go/internal/httpx"
	"lampac-go/internal/proxylink"
)

// json mirrors httpapi/server.go — the monolith-compatible encoder.
var json = jsoniter.ConfigCompatibleWithStandardLibrary

// Deps are the host-side (httpapi) functions the extracted sources reach the
// live server through. All signatures are primitive/external — no httpapi
// types cross the boundary.
type Deps struct {
	// StreamProxyDirectURL wraps a raw URL through /proxy/ in direct mode
	// (keyless, multi-instance-safe); returns the input unchanged when the
	// stream proxy is disabled for the plugin.
	StreamProxyDirectURL func(req *http.Request, rawURL, plugin string) string
	// PluginQualityBadgeGet returns the current quality badge ("4K"/"FHD"/…)
	// for a plugin key, "" when unset.
	PluginQualityBadgeGet func(plugin string) string
	// ClientIP resolves the real client IP honoring the trusted-proxy config
	// (X-Forwarded-For/X-Real-IP only from trusted proxies).
	ClientIP func(r *http.Request) string
	// IsStreamProxyDisabled reports whether cfg.Online.NoStreamProxy lists
	// the plugin (live config).
	IsStreamProxyDisabled func(plugin string) bool
	// StreamHostFromRequest is hostFromRequest + the live config's
	// Host.StreamHostFor alias mapping.
	StreamHostFromRequest func(r *http.Request) string
	// StreamProxyURL wraps a raw CDN URL through /proxy/ with proxylink
	// encryption; returns the input unchanged when links is nil or the
	// stream proxy is disabled for the plugin.
	StreamProxyURL func(req *http.Request, rawURL, plugin string, links *proxylink.Manager) string
	// StreamProxyURLWithHeaders is StreamProxyURL with upstream headers
	// embedded in the encrypted payload.
	StreamProxyURLWithHeaders func(req *http.Request, rawURL, plugin string, links *proxylink.Manager, headers map[string]string) string
	// RchGate implements the "rhub on but no device bound" handshake at the
	// top of a handler; true means the response was already written.
	RchGate func(w http.ResponseWriter, r *http.Request, rhub bool) bool
	// NewRchClient binds a per-request remote-checksearch fetcher to the
	// device identified by ?nws_id (never nil).
	NewRchClient func(r *http.Request) RchFetcher
	// ExternalKPForIMDB resolves a Kinopoisk id string from an IMDb id via
	// the host's data/externalids.json cache ("" when unknown).
	ExternalKPForIMDB func(cfgRoot, imdbID string) string
	// LiveConfig returns the hot-reloaded config (fallback when the
	// server is not ready — mirrors httpapi deps.go).
	LiveConfig func(fallback config.Config) config.Config
	// ServerReady reports whether the live server singleton is bound.
	ServerReady func() bool
	// CapiResolveRequest reports whether the request runs under capi's
	// resolve mode (ctx flag set by the capi core).
	CapiResolveRequest func(r *http.Request) bool
	// PurgeStreamCache drops capi's resolved-stream cache. Call it when a
	// balancer's upstream SESSION rotates: links minted under the retired
	// session can be dead, and the cache would keep handing them out for the
	// whole stale window. Rare by construction — a no-op when unwired.
	PurgeStreamCache func(balancer string)
}

// RchFetcher is the narrow litesrc view of httpapi's *rchClient: dispatch a
// fetch through the device bound to the request, if one is connected.
type RchFetcher interface {
	IsConnected() bool
	Get(ctx context.Context, rawurl string, headers map[string]string) (string, error)
}

// rchDisconnected is the unwired NewRchClient default — no device bound.
type rchDisconnected struct{}

func (rchDisconnected) IsConnected() bool { return false }
func (rchDisconnected) Get(context.Context, string, map[string]string) (string, error) {
	return "", errors.New("rch: no device bound")
}

var deps Deps

// SetDeps wires the host seam. Call once before constructing any checker.
func SetDeps(d Deps) { deps = d }

// streamProxyDirectURL forwards to the injected host implementation. Unwired
// (direct-handler unit tests) it mirrors the host's no-server path: the URL
// is returned unproxied.
func streamProxyDirectURL(req *http.Request, rawURL, plugin string) string {
	if deps.StreamProxyDirectURL == nil {
		return rawURL
	}
	return deps.StreamProxyDirectURL(req, rawURL, plugin)
}

// purgeStreamCache forwards to the injected host implementation; unwired
// (direct-handler unit tests) it does nothing.
func purgeStreamCache(balancer string) {
	if deps.PurgeStreamCache == nil {
		return
	}
	deps.PurgeStreamCache(balancer)
}

// pluginQualityBadgeGet forwards to the injected host implementation;
// unwired default is "no badge".
func pluginQualityBadgeGet(plugin string) string {
	if deps.PluginQualityBadgeGet == nil {
		return ""
	}
	return deps.PluginQualityBadgeGet(plugin)
}

// clientIP forwards to the injected host implementation. Unwired it mirrors
// the host's no-trusted-proxy path: the direct peer address.
func clientIP(r *http.Request) string {
	if deps.ClientIP == nil {
		if host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr)); err == nil && host != "" {
			return host
		}
		return strings.TrimSpace(r.RemoteAddr)
	}
	return deps.ClientIP(r)
}

// isStreamProxyDisabled forwards to the host; unwired mirrors the
// !serverReady() path (proxy enabled).
func isStreamProxyDisabled(plugin string) bool {
	if deps.IsStreamProxyDisabled == nil {
		return false
	}
	return deps.IsStreamProxyDisabled(plugin)
}

// streamHostFromRequest forwards to the host; unwired mirrors the
// !serverReady() path (no alias mapping).
func streamHostFromRequest(r *http.Request) string {
	if deps.StreamHostFromRequest == nil {
		return hostFromRequest(r)
	}
	return deps.StreamHostFromRequest(r)
}

// streamProxyURL forwards to the host; unwired the URL stays unproxied
// (matches the host's nil-links path).
func streamProxyURL(req *http.Request, rawURL, plugin string, links *proxylink.Manager) string {
	if deps.StreamProxyURL == nil {
		return strings.TrimSpace(rawURL)
	}
	return deps.StreamProxyURL(req, rawURL, plugin, links)
}

func streamProxyURLWithHeaders(req *http.Request, rawURL, plugin string, links *proxylink.Manager, headers map[string]string) string {
	if deps.StreamProxyURLWithHeaders == nil {
		return strings.TrimSpace(rawURL)
	}
	return deps.StreamProxyURLWithHeaders(req, rawURL, plugin, links, headers)
}

// rchGate forwards to the host handshake; unwired = no rhub, proceed.
func rchGate(w http.ResponseWriter, r *http.Request, rhub bool) bool {
	if deps.RchGate == nil {
		return false
	}
	return deps.RchGate(w, r, rhub)
}

// newRchClient forwards to the host; unwired = disconnected fetcher.
func newRchClient(r *http.Request) RchFetcher {
	if deps.NewRchClient == nil {
		return rchDisconnected{}
	}
	return deps.NewRchClient(r)
}

// mirageSem returns the shared mirage Chrome pool semaphore (owned by
// mirage_browser.go since the mirage family moved in).
func mirageSem() *browsergate.ChromeSem { return mirageBrowserSem }

// liveConfig forwards to the host's hot-reload accessor; unwired mirrors
// the not-ready path (captured fallback).
func liveConfig(fallback config.Config) config.Config {
	if deps.LiveConfig == nil {
		return fallback
	}
	return deps.LiveConfig(fallback)
}

// serverReady forwards to the host; unwired = not ready.
func serverReady() bool {
	if deps.ServerReady == nil {
		return false
	}
	return deps.ServerReady()
}

// capiResolveRequest forwards to the host capi core; unwired = false.
func capiResolveRequest(r *http.Request) bool {
	if deps.CapiResolveRequest == nil {
		return false
	}
	return deps.CapiResolveRequest(r)
}

// rchFetch mirrors httpapi/rch_client.go — composed from the injected
// newRchClient, so it needs no extra Deps field.
func rchFetch(r *http.Request, rhub bool, target string, headers map[string]string, direct func() (string, error)) (string, error) {
	if rhub {
		if rch := newRchClient(r); rch.IsConnected() {
			if body, err := rch.Get(r.Context(), target, headers); err == nil && body != "" {
				return body, nil
			}
		}
	}
	return direct()
}

// rchReqCtxKey/rchWithRequest/rchFetchCtx mirror httpapi/rch_client.go —
// the ctx-carrying variant for balancers whose fetch helpers take ctx.
type rchReqCtxKey struct{}

func rchWithRequest(r *http.Request) context.Context {
	return context.WithValue(r.Context(), rchReqCtxKey{}, r)
}

func rchFetchCtx(ctx context.Context, rhub bool, target string, headers map[string]string, direct func() (string, error)) (string, error) {
	if rhub {
		if r, ok := ctx.Value(rchReqCtxKey{}).(*http.Request); ok && r != nil {
			return rchFetch(r, rhub, target, headers, direct)
		}
	}
	return direct()
}

// rchClientStreamCapable mirrors httpapi/stream_client.go (pure).
func rchClientStreamCapable(r *http.Request) bool {
	return strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("rchtype")), "apk")
}

// externalKPForIMDB forwards to the host cache; unwired = unknown.
func externalKPForIMDB(cfgRoot, imdbID string) string {
	if deps.ExternalKPForIMDB == nil {
		return ""
	}
	return deps.ExternalKPForIMDB(cfgRoot, imdbID)
}

// Local forwarders to shared response infra (same encoder as the monolith).
func writeJSON(w http.ResponseWriter, status int, v any) {
	httpx.WriteJSON(w, status, v)
}

func writeHTML(w http.ResponseWriter, status int, body string) {
	httpx.WriteHTML(w, status, body)
}
