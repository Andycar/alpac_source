package cluster

import (
	"net/http"
	"testing"
)

// TestApplyUpstreamHeadersNoDuplicateCORS reproduces the production bug where a
// forwarded route (e.g. /tg/auth served by another cluster node) returned every
// CORS header and X-Request-Id twice: once from the edge node's middleware and
// once copied from the upstream node's response. A browser rejects a response
// with more than one Access-Control-Allow-Origin, silently breaking cross-origin
// plugin fetches. applyUpstreamHeaders must leave exactly one of each.
func TestApplyUpstreamHeadersNoDuplicateCORS(t *testing.T) {
	// dst as the edge node's middleware left it before forwarding.
	dst := http.Header{}
	dst.Set("Access-Control-Allow-Origin", "*")
	dst.Set("Access-Control-Allow-Headers", "Content-Type, X-Lampac-Token")
	dst.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	dst.Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Set-Cookie")
	dst.Set("X-Request-Id", "edge-123")

	// src as the upstream node returned it — same middleware ran there too.
	src := http.Header{}
	src.Set("Access-Control-Allow-Origin", "*")
	src.Set("Access-Control-Allow-Headers", "Content-Type, X-Lampac-Token")
	src.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	src.Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Set-Cookie")
	src.Set("X-Request-Id", "node-456")
	src.Set("Content-Type", "text/html; charset=utf-8")
	src.Set("Location", "/tg/auth")

	applyUpstreamHeaders(dst, src)

	singleValued := []string{
		"Access-Control-Allow-Origin",
		"Access-Control-Allow-Headers",
		"Access-Control-Allow-Methods",
		"Access-Control-Expose-Headers",
		"X-Request-Id",
		"Content-Type",
		"Location",
	}
	for _, h := range singleValued {
		if n := len(dst.Values(h)); n != 1 {
			t.Errorf("%s: got %d values %v, want exactly 1", h, n, dst.Values(h))
		}
	}

	if got := dst.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want *", got)
	}
	// The upstream response is authoritative for headers it sets.
	if got := dst.Get("X-Request-Id"); got != "node-456" {
		t.Errorf("X-Request-Id = %q, want node-456 (upstream authoritative)", got)
	}
	if got := dst.Get("Location"); got != "/tg/auth" {
		t.Errorf("Location = %q, want /tg/auth", got)
	}
}

// TestApplyUpstreamHeadersPreservesSetCookie verifies the dedup doesn't collapse
// headers that are legitimately repeated. Multiple Set-Cookie values must all
// survive — losing one would drop a session/auth cookie on forwarded logins.
func TestApplyUpstreamHeadersPreservesSetCookie(t *testing.T) {
	dst := http.Header{}
	// An edge-set cookie that the upstream does NOT re-send must be replaced,
	// not merged, by the upstream's authoritative set.
	dst.Add("Set-Cookie", "stale=edge")

	src := http.Header{}
	src.Add("Set-Cookie", "lampac_token=abc; Path=/; HttpOnly")
	src.Add("Set-Cookie", "lampac_profile=def; Path=/; HttpOnly")

	applyUpstreamHeaders(dst, src)

	cookies := dst.Values("Set-Cookie")
	if len(cookies) != 2 {
		t.Fatalf("Set-Cookie: got %d values %v, want 2", len(cookies), cookies)
	}
	for _, c := range cookies {
		if c == "stale=edge" {
			t.Errorf("stale edge cookie survived: %v", cookies)
		}
	}
}

// TestApplyUpstreamHeadersKeepsEdgeOnlyHeaders verifies headers the upstream does
// not send are left untouched on dst.
func TestApplyUpstreamHeadersKeepsEdgeOnlyHeaders(t *testing.T) {
	dst := http.Header{}
	dst.Set("Access-Control-Allow-Origin", "*")

	src := http.Header{}
	src.Set("Content-Type", "application/javascript")

	applyUpstreamHeaders(dst, src)

	if got := dst.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("edge-only Access-Control-Allow-Origin = %q, want * (should be kept)", got)
	}
	if got := dst.Get("Content-Type"); got != "application/javascript" {
		t.Errorf("Content-Type = %q, want application/javascript", got)
	}
}
