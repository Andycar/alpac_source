package proxyapi

import (
	"net/http"
	"testing"
)

// IPTV streams proxy to a paid provider that bans playlists it detects as "shared". The tell-tale
// is many distinct client IPs on one account. nginx stamps the client IP onto X-Real-IP /
// X-Forwarded-For, and the general request-copy path forwards inbound headers verbatim — so the
// IPTV egress MUST strip every client-IP-bearing header, leaving only the server's egress IP.
func TestStripClientIdentityHeaders(t *testing.T) {
	h := http.Header{}
	leaky := []string{
		"X-Forwarded-For", "X-Real-IP", "X-Client-IP", "Forwarded", "Via",
		"True-Client-IP", "CF-Connecting-IP", "Fastly-Client-IP", "X-Lampac-Go",
	}
	for _, k := range leaky {
		h.Set(k, "203.0.113.7") // a client's real IP (or internal marker)
	}
	// Headers that must survive — the upstream legitimately needs these.
	h.Set("User-Agent", "Mozilla/5.0")
	h.Set("Referer", "https://provider.example/")
	h.Set("Range", "bytes=0-")

	stripClientIdentityHeaders(h)

	for _, k := range leaky {
		if v := h.Get(k); v != "" {
			t.Errorf("client-identity header %q leaked upstream: %q", k, v)
		}
	}
	for _, k := range []string{"User-Agent", "Referer", "Range"} {
		if h.Get(k) == "" {
			t.Errorf("legitimate header %q was dropped", k)
		}
	}
}
