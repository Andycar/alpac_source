package httpapi

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// TestAtomicGlobalBalancerStatsRace exercises the publication pattern under
// concurrent reads to catch a regression to plain `*T` storage. The race
// detector (`go test -race`) would flag the old pattern but not this one.
func TestAtomicGlobalBalancerStatsRace(t *testing.T) {
	prev := GetGlobalBalancerStats()
	t.Cleanup(func() { SetGlobalBalancerStats(prev) })

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = GetGlobalBalancerStats() // read
			// also stomp a no-op write to force happens-before paths.
			SetGlobalBalancerStats(GetGlobalBalancerStats())
		}()
	}
	wg.Wait()
	// If we got here without a race detector trip, the atomic.Pointer publication holds.
}

// TestTrustedProxiesAllowlist: client IP is taken from headers ONLY when the
// direct remote address is in the allowlist (or loopback).
func TestTrustedProxiesAllowlist(t *testing.T) {
	prev := trustedProxiesPtr.Load()
	t.Cleanup(func() { trustedProxiesPtr.Store(prev) })

	SetTrustedProxies([]string{"10.0.0.5", "192.168.0.0/16"})

	mk := func(remote, xff, xri string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		if xri != "" {
			r.Header.Set("X-Real-IP", xri)
		}
		return r
	}

	cases := []struct {
		name, remote, xff, xri, want string
	}{
		{"trusted exact, XFF", "10.0.0.5:55", "1.2.3.4", "", "1.2.3.4"},
		{"trusted CIDR, XFF", "192.168.7.99:55", "5.6.7.8, 9.9.9.9", "", "5.6.7.8"},
		{"trusted exact, XRI fallback", "10.0.0.5:55", "", "100.64.0.1", "100.64.0.1"},
		{"untrusted ignores XFF", "8.8.8.8:55", "1.2.3.4", "", "8.8.8.8"},
		{"loopback always trusted", "127.0.0.1:55", "1.2.3.4", "", "1.2.3.4"},
		{"loopback v6 trusted", "[::1]:55", "1.2.3.4", "", "1.2.3.4"},
	}
	for _, c := range cases {
		got := clientIP(mk(c.remote, c.xff, c.xri))
		if got != c.want {
			t.Errorf("%s: clientIP=%q, want %q (remote=%q xff=%q xri=%q)",
				c.name, got, c.want, c.remote, c.xff, c.xri)
		}
	}
}

// TestTrustedProxiesEmptyAllowlist verifies that with no entries, only
// loopback is trusted (the safe default).
func TestTrustedProxiesEmptyAllowlist(t *testing.T) {
	prev := trustedProxiesPtr.Load()
	t.Cleanup(func() { trustedProxiesPtr.Store(prev) })

	SetTrustedProxies(nil)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "8.8.8.8:55"
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	if got := clientIP(r); got != "8.8.8.8" {
		t.Fatalf("untrusted remote should fall back to RemoteAddr; got %q", got)
	}

	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	r2.RemoteAddr = "127.0.0.1:55"
	r2.Header.Set("X-Forwarded-For", "1.2.3.4")
	if got := clientIP(r2); got != "1.2.3.4" {
		t.Fatalf("loopback should still trust XFF; got %q", got)
	}
}

// TestSanitizeForwardedRefererStripsQuery: tokens in the query string don't
// leak via Referer when we forward upstream.
func TestSanitizeForwardedRefererStripsQuery(t *testing.T) {
	cases := map[string]string{
		"https://lampa.example/path?token=SECRET":       "https://lampa.example/path",
		"https://lampa.example/path?token=A&foo=B#frag": "https://lampa.example/path",
		"https://example.com/":                          "https://example.com/",
		"":                                              "",
		"not-a-url":                                     "",
		"javascript:alert(1)":                           "",
		"   https://x.test/p?session=xyz   ":            "https://x.test/p",
	}
	for in, want := range cases {
		got := sanitizeForwardedReferer(in)
		if got != want {
			t.Errorf("sanitizeForwardedReferer(%q)=%q want %q", in, got, want)
		}
	}
}
