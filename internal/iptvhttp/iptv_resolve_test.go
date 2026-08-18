package iptvhttp

import (
	"testing"

	"lampac-go/internal/iptv"
)

// TestIPTVResolveStreamURL pins the direct-vs-proxy decision and the
// fail-closed guarantee: when a stream MUST be proxied (header-gated, browser
// cleartext, or proxy=all playlist) and the proxy is unavailable, the raw
// upstream URL must never be returned.
func TestIPTVResolveStreamURL(t *testing.T) {
	httpsCh := &iptv.Channel{URL: "https://cdn.example/stream.m3u8"}

	// Safe direct play: https, no credential headers, no proxy-all.
	if u, errSlug := iptvResolveStreamURL(httpsCh.URL, httpsCh, nil, nil, "1.2.3.4", "https://app.host", false); errSlug != "" || u != httpsCh.URL {
		t.Fatalf("direct https: got (%q,%q)", u, errSlug)
	}

	// Cleartext http:// in the BROWSER must be proxied; nil proxy → fail closed.
	httpCh := &iptv.Channel{URL: "http://cdn.example/stream.m3u8"}
	if u, errSlug := iptvResolveStreamURL(httpCh.URL, httpCh, nil, nil, "1.2.3.4", "https://app.host", false); errSlug == "" || u != "" {
		t.Fatalf("http fail-closed: got (%q,%q)", u, errSlug)
	}

	// Cleartext http:// with direct=1 (native app, usesCleartextTraffic) plays raw —
	// это гео-кейс: стрим уходит с IP устройства, а не сервера.
	if u, errSlug := iptvResolveStreamURL(httpCh.URL, httpCh, nil, nil, "1.2.3.4", "https://app.host", true); errSlug != "" || u != httpCh.URL {
		t.Fatalf("http direct native: got (%q,%q)", u, errSlug)
	}

	// Header-gated https must be proxied EVEN with direct=1 (headers live server-side);
	// nil proxy → fail closed.
	uaCh := &iptv.Channel{URL: "https://cdn.example/stream.m3u8", UserAgent: "SpecialUA"}
	if u, errSlug := iptvResolveStreamURL(uaCh.URL, uaCh, nil, nil, "1.2.3.4", "https://app.host", true); errSlug == "" || u != "" {
		t.Fatalf("ua fail-closed: got (%q,%q)", u, errSlug)
	}

	// Playlist-level UA is a header requirement too — proxied, fail closed without proxy.
	uaPl := &iptv.Playlist{UserAgent: "PanelUA"}
	if u, errSlug := iptvResolveStreamURL(httpsCh.URL, httpsCh, uaPl, nil, "1.2.3.4", "https://app.host", true); errSlug == "" || u != "" {
		t.Fatalf("playlist-ua fail-closed: got (%q,%q)", u, errSlug)
	}

	// proxy=all playlist (sharing protection) → fail closed without proxy, direct or not.
	pl := &iptv.Playlist{ProxyMode: "all"}
	if u, errSlug := iptvResolveStreamURL(httpsCh.URL, httpsCh, pl, nil, "1.2.3.4", "https://app.host", true); errSlug == "" || u != "" {
		t.Fatalf("proxy-all fail-closed: got (%q,%q)", u, errSlug)
	}
}
