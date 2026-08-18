package wasmmodules

import "testing"

func TestPermissionSet(t *testing.T) {
	cases := []struct {
		name    string
		perms   []string
		url     string
		allowed bool
	}{
		{"empty list = permissive default", nil, "https://anywhere.io/x", true},
		{"http:* allows everything", []string{"http:*"}, "https://anywhere.io/x", true},
		{"exact host match", []string{"http:example.com"}, "https://example.com/api", true},
		{"exact host miss", []string{"http:example.com"}, "https://other.com/api", false},
		{"wildcard sub match", []string{"http:*.example.com"}, "https://api.example.com/x", true},
		{"wildcard sub does not match apex", []string{"http:*.example.com"}, "https://example.com/x", false},
		{"deep subdomain", []string{"http:*.example.com"}, "https://a.b.example.com/x", true},
		{"http port preserved", []string{"http:example.com"}, "https://example.com:8443/x", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newPermissionSet(c.perms)
			ok, why := p.allowHTTP(c.url)
			if ok != c.allowed {
				t.Fatalf("allowHTTP(%q)=%v (%s), want %v", c.url, ok, why, c.allowed)
			}
		})
	}
}

func TestPermissionCacheProxy(t *testing.T) {
	// Empty list — everything allowed.
	p := newPermissionSet(nil)
	if !p.allowCache() || !p.allowProxy() {
		t.Fatalf("empty list should be permissive")
	}
	// Restricted list — only what's named.
	p = newPermissionSet([]string{"http:*", "cache"})
	if !p.allowCache() {
		t.Fatalf("cache should be granted")
	}
	if p.allowProxy() {
		t.Fatalf("proxy should NOT be granted (not in list)")
	}
}
