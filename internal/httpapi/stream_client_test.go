package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/proxylink"
)

func streamTestRequest(rchtype string) *http.Request {
	u := "/lite/x"
	if rchtype != "" {
		u += "?rchtype=" + rchtype
	}
	req := httptest.NewRequest(http.MethodGet, u, nil)
	req.RemoteAddr = "127.0.0.1:5555"
	req.Host = "lampac.example"
	return req
}

func newStreamTestLinks(t *testing.T) *proxylink.Manager {
	t.Helper()
	links, err := proxylink.New(proxylink.Options{CacheDir: t.TempDir(), VerifyIP: true, EncryptAES: true})
	if err != nil {
		t.Fatalf("proxylink.New: %v", err)
	}
	return links
}

// apk client + opt-in => raw CDN URL handed to the player with headers attached.
func TestStreamURLForClientDirectAPK(t *testing.T) {
	links := newStreamTestLinks(t)
	raw := "https://cdn.example/stream.m3u8"
	hdr := map[string]string{"Referer": "https://site.example/", "Origin": "https://site.example"}

	url, gotHdr := streamURLForClient(streamTestRequest("apk"), links, "demo", raw, hdr, true)
	if url != raw {
		t.Fatalf("apk: expected raw URL %q, got %q", raw, url)
	}
	if gotHdr["Referer"] != hdr["Referer"] || gotHdr["Origin"] != hdr["Origin"] {
		t.Fatalf("apk: expected headers passed through, got %v", gotHdr)
	}
}

// browser client => server /proxy/ relay, headers embedded (nil play headers).
func TestStreamURLForClientProxyBrowser(t *testing.T) {
	links := newStreamTestLinks(t)
	raw := "https://cdn.example/stream.m3u8"
	hdr := map[string]string{"Referer": "https://site.example/"}

	url, gotHdr := streamURLForClient(streamTestRequest("cors"), links, "demo", raw, hdr, true)
	if !strings.Contains(url, "/proxy/") {
		t.Fatalf("browser: expected /proxy/ URL, got %q", url)
	}
	if gotHdr != nil {
		t.Fatalf("browser: expected nil play headers (embedded in proxy URL), got %v", gotHdr)
	}
}

// clientStream disabled => always proxy, even for an apk client (no behaviour
// change for balancers that haven't opted in).
func TestStreamURLForClientDisabledAlwaysProxy(t *testing.T) {
	links := newStreamTestLinks(t)
	raw := "https://cdn.example/stream.m3u8"
	hdr := map[string]string{"Referer": "https://site.example/"}

	url, gotHdr := streamURLForClient(streamTestRequest("apk"), links, "demo", raw, hdr, false)
	if !strings.Contains(url, "/proxy/") {
		t.Fatalf("clientStream=false must proxy, got %q", url)
	}
	if gotHdr != nil {
		t.Fatalf("clientStream=false: expected nil play headers, got %v", gotHdr)
	}
}
