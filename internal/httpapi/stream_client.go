package httpapi

import (
	"net/http"
	"strings"

	"lampac-go/internal/proxylink"
)

// ---------------------------------------------------------------------------
//  Client-side stream delivery: direct-with-headers vs server /proxy/ relay.
//
//  A balancer whose stream needs custom request headers (Referer, Origin,
//  Authorization, …) normally wraps the CDN URL through /proxy/, so the server
//  injects the headers and relays the bytes. That spends server bandwidth and
//  exposes the server IP to the CDN.
//
//  The Lampa client already carries headers to the player — online.js does
//  `first.headers = json.headers` and the native Android player applies them to
//  every request of the HLS session (manifest + segments + keys). So for a
//  capable client we can hand over the RAW CDN URL plus the headers and let the
//  device fetch the stream directly (CDN -> device), keeping the server out of
//  the byte path. Browsers can't set forbidden headers (Origin/Referer), so they
//  keep using the /proxy/ relay.
//
//  The client announces its kind via ?rchtype on every request (online.js):
//  "apk" = native player (any header), "cors"/"web" = browser (limited).
//
//  Not for CDNs that need per-segment token rewriting (the m3u8 itself must be
//  rewritten, which the server still does) — only for static-header streams.
// ---------------------------------------------------------------------------

// rchClientStreamCapable reports whether the requesting client can apply
// arbitrary request headers in the media player itself (native Android app).
// Browser/Tizen clients ("cors"/"web") cannot set forbidden headers such as
// Origin/Referer, so they must keep using the server /proxy/ relay.
func rchClientStreamCapable(r *http.Request) bool {
	return strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("rchtype")), "apk")
}

// streamURLForClient decides how to deliver a header-bearing stream URL.
//
//   - clientStream enabled + header-capable client (apk) => returns the raw CDN
//     URL and the headers map for the balancer to drop straight into the play
//     object ({url, headers}); the device fetches the stream directly.
//   - otherwise => returns a /proxy/ URL with the headers embedded + encrypted
//     (server relays the bytes) and nil play headers — identical to before.
//
// With clientStream=false the result is always the /proxy/ path, so adopting the
// helper is a no-op until a balancer explicitly opts in.
func streamURLForClient(r *http.Request, links *proxylink.Manager, plugin, rawURL string, headers map[string]string, clientStream bool) (string, map[string]string) {
	if clientStream && len(headers) > 0 && rchClientStreamCapable(r) {
		return rawURL, headers
	}
	if links == nil {
		return rawURL, nil
	}
	enc := links.EncryptURIWithHeaders(rawURL, clientIP(r), plugin, headers)
	if enc == "" {
		return rawURL, nil
	}
	return hostFromRequest(r) + "/proxy/" + enc, nil
}
