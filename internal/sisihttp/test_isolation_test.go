package sisihttp

import (
	"testing"

	"lampac-go/internal/httpclient"
)

// resetHTTPAPIGlobals gives the sisi live-source tests a clean httpclient proxy
// registry between runs. Sisi sources register a residential SOCKS5 proxy on
// construction, and the first registration also becomes the global
// ProxiedTransport (first-wins) — a leaked registration would silently reroute
// every later NewProxied-based test through a dead proxy → empty responses.
//
// (The original host helper also reset serverRef / plugin caches / rate buckets;
// those are httpapi-package globals this extracted package no longer touches, so
// only the httpclient reset is kept. Name retained to avoid churning ~20 call sites.)
func resetHTTPAPIGlobals(t *testing.T) {
	t.Helper()
	httpclient.ClearRegistry()
	t.Cleanup(func() { httpclient.ClearRegistry() })
}
