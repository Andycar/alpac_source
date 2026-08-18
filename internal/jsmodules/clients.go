package jsmodules

import (
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/httpclient"
)

// transportSpec describes how to build an http.Client for a single js-side
// http.* call. Every field is optional; the zero value picks the runtime's
// default client (the one injected by the Manager).
type transportSpec struct {
	kind     string // "" | "default" | "utls" | "balancer" | "balancer-utls" | "socks5" | "utls-socks5"
	balancer string // when kind ∈ {balancer, balancer-utls}, name to look up in proxy registry
	proxy    string // explicit "host:port" (or full "scheme://host:port") for socks5/utls-socks5
	timeout  time.Duration
}

// key builds a stable cache key for transportSpec so we can re-use clients.
func (s transportSpec) key() string {
	return strings.Join([]string{
		s.kind,
		s.balancer,
		s.proxy,
		s.timeout.String(),
	}, "|")
}

// clientCache memoises http.Clients keyed by transportSpec. Clients can be
// shared across goroutines (Go's http.Client is safe for concurrent use).
type clientCache struct {
	mu sync.RWMutex
	m  map[string]*http.Client
}

func newClientCache() *clientCache { return &clientCache{m: map[string]*http.Client{}} }

func (c *clientCache) get(key string) *http.Client {
	c.mu.RLock()
	cl := c.m[key]
	c.mu.RUnlock()
	return cl
}
func (c *clientCache) put(key string, cl *http.Client) {
	c.mu.Lock()
	c.m[key] = cl
	c.mu.Unlock()
}

// pickClient returns an http.Client matching the spec. When jar is non-nil it
// is attached to the returned client (so cookies persist across calls within
// a single js-module invocation). Cached clients are cloned with the new jar.
//
// The client cache lives on the Manager so a hot module reuses transports.
func (m *Manager) pickClient(spec transportSpec, jar *cookiejar.Jar, fallback HTTPClient) HTTPClient {
	if m.clients == nil {
		m.clients = newClientCache()
	}
	// Fast path: empty spec → keep the manager-injected client (preserves
	// existing behaviour for modules that don't ask for a transport).
	if spec.kind == "" || spec.kind == "default" {
		if fallback == nil {
			fallback = http.DefaultClient
		}
		// If a jar is available, wrap the fallback so cookies stick. We only
		// wrap genuine *http.Client instances; the interface is opaque.
		if jar != nil {
			if base, ok := fallback.(*http.Client); ok {
				return cloneClientWithJar(base, jar)
			}
		}
		return fallback
	}

	// Cache hit returns a CLONE with the current jar — sharing a *http.Client
	// across goroutines is safe, but two runtimes must not share a jar.
	if cl := m.clients.get(spec.key()); cl != nil {
		return cloneClientWithJar(cl, jar)
	}

	cl := buildClient(spec)
	m.clients.put(spec.key(), cl)
	return cloneClientWithJar(cl, jar)
}

// cloneClientWithJar returns a shallow copy of c with the given jar attached.
// Transport, Timeout and CheckRedirect are reused.
func cloneClientWithJar(c *http.Client, jar *cookiejar.Jar) *http.Client {
	if c == nil {
		return &http.Client{Jar: jar}
	}
	clone := *c
	if jar != nil {
		clone.Jar = jar
	}
	return &clone
}

// buildClient instantiates an http.Client for the given spec. It defers to
// existing httpclient factories so we automatically inherit balancer-specific
// proxy registries, uTLS Chrome fingerprints and SOCKS5 dialers.
func buildClient(spec transportSpec) *http.Client {
	timeout := spec.timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	switch strings.ToLower(spec.kind) {
	case "ipv6":
		// IPv6-first dialer for sites whose IPv4 DNS is poisoned by anti-RKN
		// CDN tricks (tevas.dev: IPv4 of pult/team points at video CDN nginx
		// that doesn't serve HTML; IPv6 hits the real origin). Falls back to
		// IPv4 if the host has no AAAA records or v6 is unreachable.
		return httpclient.NewIPv6(timeout)

	case "utls":
		return httpclient.NewUTLS(timeout)

	case "balancer":
		if spec.balancer != "" {
			return httpclient.NewForBalancer(spec.balancer, timeout)
		}
		return httpclient.New(timeout)

	case "balancer-utls":
		if spec.balancer != "" {
			return httpclient.NewUTLSForBalancer(spec.balancer, timeout)
		}
		return httpclient.NewUTLS(timeout)

	case "socks5":
		addr := resolveProxyAddr(spec)
		if addr == "" {
			return httpclient.New(timeout)
		}
		return &http.Client{
			Transport: httpclient.NewSOCKS5TransportPublic(addr),
			Timeout:   timeout,
		}

	case "utls-socks5":
		addr := resolveProxyAddr(spec)
		if addr == "" {
			// Fall back to plain uTLS if the caller forgot to give us an
			// address — at least the TLS fingerprint helps.
			return httpclient.NewUTLS(timeout)
		}
		return httpclient.NewUTLSViaSOCKS5(addr, timeout)
	}

	return httpclient.New(timeout)
}

// resolveProxyAddr resolves the SOCKS5 host:port for a spec. Priority:
//  1. explicit spec.proxy ("host:port" or "socks5://host:port")
//  2. balancer-registered SOCKS5 (set by RegisterDirectProxy / proxy entries)
func resolveProxyAddr(spec transportSpec) string {
	if p := strings.TrimSpace(spec.proxy); p != "" {
		// Accept either bare host:port or a scheme prefix.
		p = strings.TrimPrefix(p, "socks5h://")
		p = strings.TrimPrefix(p, "socks5://")
		return p
	}
	if spec.balancer != "" {
		return httpclient.SocksAddrForBalancer(spec.balancer)
	}
	return ""
}

// parseTransportSpec extracts a transportSpec from js-side opts plus per-module
// defaults from the manifest (default_transport, default_proxy, default_balancer).
//
// js side schema:
//
//	http.get(url, {
//	  transport: "utls-socks5",   // selector
//	  proxy:     "1.2.3.4:1080",  // override
//	  balancer:  "redheadsound",  // override
//	  timeout:   45,              // seconds
//	})
func parseTransportSpec(opts map[string]any, defaults moduleHTTPDefaults) transportSpec {
	get := func(k string) string {
		if opts == nil {
			return ""
		}
		v, ok := opts[k]
		if !ok || v == nil {
			return ""
		}
		s, _ := v.(string)
		return strings.TrimSpace(s)
	}
	getInt := func(k string) int {
		if opts == nil {
			return 0
		}
		v, ok := opts[k]
		if !ok || v == nil {
			return 0
		}
		switch x := v.(type) {
		case int64:
			return int(x)
		case float64:
			return int(x)
		case int:
			return x
		}
		return 0
	}

	kind := get("transport")
	if kind == "" {
		kind = defaults.Transport
	}
	proxy := get("proxy")
	if proxy == "" {
		proxy = defaults.Proxy
	}
	balancer := get("balancer")
	if balancer == "" {
		balancer = defaults.Balancer
	}
	to := getInt("timeout")
	var timeout time.Duration
	if to > 0 {
		timeout = time.Duration(to) * time.Second
	}
	return transportSpec{
		kind:     strings.ToLower(kind),
		balancer: balancer,
		proxy:    proxy,
		timeout:  timeout,
	}
}

// moduleHTTPDefaults captures manifest-level fallbacks so a module can declare
// once "use utls-socks5 by default" without setting it on every http.get call.
type moduleHTTPDefaults struct {
	Transport string
	Proxy     string
	Balancer  string
}
