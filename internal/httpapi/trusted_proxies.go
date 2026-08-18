package httpapi

import (
	"net"
	"net/url"
	"strings"
	"sync/atomic"
)

// sanitizeForwardedReferer strips query string and fragment from a Referer
// header value before it is forwarded to an upstream. Most upstream CDNs
// only validate the scheme+host of Referer (an "Origin-style" check), so
// keeping path+host is enough; query strings may carry user tokens.
//
// Returns "" for unparseable input — caller should skip setting the header.
func sanitizeForwardedReferer(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// trustedProxiesRegistry holds parsed entries from
// SecurityConfig.TrustedProxies. atomic.Pointer for lock-free reads on the
// hot path of every request.
//
// Loopback (127.0.0.1, ::1) is ALWAYS trusted.
// RFC1918 private ranges (10/8, 172.16/12, 192.168/16) plus link-local
// (169.254/16) and IPv6 ULA (fc00::/7) are trusted BY DEFAULT — the common
// deployment is Lampa behind nginx/traefik on a private network. Admins
// who run Lampa directly on the public internet should set
// `[security] strict_rfc1918 = true` to require explicit allowlisting.
//
// When the operator provides a list via `[security] trusted_proxies`, those
// entries are accepted IN ADDITION to (not instead of) the loopback +
// RFC1918 defaults — unless strict_rfc1918 is set, in which case only the
// explicit list + loopback is honored.
type trustedNetwork struct {
	ip  net.IP     // non-nil for single-address entries
	net *net.IPNet // non-nil for CIDR entries
}

var (
	trustedProxiesPtr atomic.Pointer[[]trustedNetwork]
	strictRFC1918     atomic.Bool // when true, RFC1918 default-trust is OFF
)

// defaultPrivateNetworks is the always-trusted-unless-strict list.
// Initialised once at package load; values never change at runtime.
var defaultPrivateNetworks = func() []*net.IPNet {
	cidrs := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"169.254.0.0/16", // link-local
		"fc00::/7",       // IPv6 ULA
		"fe80::/10",      // IPv6 link-local
	}
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		if _, n, err := net.ParseCIDR(c); err == nil {
			out = append(out, n)
		}
	}
	return out
}()

// SetStrictRFC1918 toggles whether RFC1918 ranges are trusted by default.
// When true, only loopback + explicit TrustedProxies entries are honored.
func SetStrictRFC1918(strict bool) { strictRFC1918.Store(strict) }

// SetTrustedProxies parses and atomically replaces the allowlist. Bad
// entries are logged-and-skipped — they don't fail the whole list. Called
// once during server bootstrap from cfg.Security.TrustedProxies.
func SetTrustedProxies(entries []string) {
	out := make([]trustedNetwork, 0, len(entries))
	for _, raw := range entries {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if strings.Contains(raw, "/") {
			if _, ipnet, err := net.ParseCIDR(raw); err == nil {
				out = append(out, trustedNetwork{net: ipnet})
				continue
			}
		}
		if ip := net.ParseIP(raw); ip != nil {
			out = append(out, trustedNetwork{ip: ip})
		}
	}
	trustedProxiesPtr.Store(&out)
}

// isRemoteFromTrustedProxy reports whether directIP (r.RemoteAddr stripped
// of port) is in the configured allowlist. Used by clientIP() to decide
// whether X-Forwarded-For / X-Real-IP headers can be trusted.
//
// Check order:
//  1. Loopback (127.0.0.1, ::1) — always trusted, can't be spoofed externally.
//  2. RFC1918 / link-local — trusted UNLESS SetStrictRFC1918(true) was called.
//  3. Explicit entries from SetTrustedProxies — always trusted.
func isRemoteFromTrustedProxy(directIP string) bool {
	if directIP == "" {
		return false
	}
	ip := net.ParseIP(directIP)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	if !strictRFC1918.Load() {
		for _, n := range defaultPrivateNetworks {
			if n.Contains(ip) {
				return true
			}
		}
	}
	list := trustedProxiesPtr.Load()
	if list != nil {
		for _, e := range *list {
			if e.ip != nil && e.ip.Equal(ip) {
				return true
			}
			if e.net != nil && e.net.Contains(ip) {
				return true
			}
		}
	}
	return false
}
