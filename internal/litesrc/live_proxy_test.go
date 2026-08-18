package litesrc

import (
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"
)

// liveProxyClient builds an http.Client routed through LAMPAC_LIVE_PROXY (e.g.
// "http://127.0.0.1:12334"). Live tests need it on dev machines where the
// upstream sites are RKN-blocked: the balancer transports deliberately ignore
// HTTP(S)_PROXY, so without this a blocked host looks like a dead host.
func liveProxyClient(t *testing.T) (*http.Client, bool) {
	t.Helper()
	raw := os.Getenv("LAMPAC_LIVE_PROXY")
	if raw == "" {
		raw = os.Getenv("KINOGO_LIVE_PROXY")
	}
	if raw == "" {
		return nil, false
	}
	proxyURL, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("bad LAMPAC_LIVE_PROXY %q: %v", raw, err)
	}
	return &http.Client{
		Timeout:   25 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
	}, true
}
