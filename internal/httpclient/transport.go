// Package httpclient provides a shared HTTP transport for the entire process.
// All http.Client instances should use SharedTransport to unify the
// TCP connection pool and reduce idle-connection memory overhead.
package httpclient

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/proxy"
)

// ---------------------------------------------------------------------------
// DNS cache — avoids repeated OS DNS lookups for frequently accessed CDN hosts.
// Entries expire after 5 minutes. Cache is bounded to 2000 entries.
// ---------------------------------------------------------------------------

type dnsEntry struct {
	addrs   []string
	expires time.Time
}

type dnsCache struct {
	mu      sync.RWMutex
	entries map[string]dnsEntry
	ttl     time.Duration
	maxSize int
}

var globalDNSCache = &dnsCache{
	entries: make(map[string]dnsEntry, 256),
	ttl:     5 * time.Minute,
	maxSize: 2000,
}

func (c *dnsCache) lookup(ctx context.Context, host string) ([]string, error) {
	c.mu.RLock()
	if e, ok := c.entries[host]; ok && time.Now().Before(e.expires) {
		c.mu.RUnlock()
		return e.addrs, nil
	}
	c.mu.RUnlock()

	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	// Evict if over limit.
	if len(c.entries) >= c.maxSize {
		now := time.Now()
		for k, v := range c.entries {
			if now.After(v.expires) {
				delete(c.entries, k)
			}
		}
		// If still over limit after expiry sweep, drop 25%.
		if len(c.entries) >= c.maxSize {
			i := 0
			for k := range c.entries {
				delete(c.entries, k)
				i++
				if i >= c.maxSize/4 {
					break
				}
			}
		}
	}
	c.entries[host] = dnsEntry{addrs: addrs, expires: time.Now().Add(c.ttl)}
	c.mu.Unlock()

	return addrs, nil
}

// cachedDialContext resolves DNS through the in-process cache, then dials TCP4.
func cachedDialContext(ctx context.Context, _, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ipv4Dialer.DialContext(ctx, "tcp4", addr)
	}

	// Skip cache for IPs.
	if net.ParseIP(host) != nil {
		return ipv4Dialer.DialContext(ctx, "tcp4", addr)
	}

	addrs, err := globalDNSCache.lookup(ctx, host)
	if err != nil {
		// Fallback to standard dialer on DNS error.
		return ipv4Dialer.DialContext(ctx, "tcp4", addr)
	}

	// Try resolved addresses (prefer IPv4).
	for _, a := range addrs {
		ip := net.ParseIP(a)
		if ip == nil || ip.To4() == nil {
			continue
		}
		conn, err := ipv4Dialer.DialContext(ctx, "tcp4", net.JoinHostPort(a, port))
		if err == nil {
			return conn, nil
		}
	}
	// All failed — fall back to original.
	return ipv4Dialer.DialContext(ctx, "tcp4", addr)
}

// SharedTransport is the single connection pool for the entire process.
// InsecureSkipVerify is enabled because many upstream balancer servers
// (videoseed.tv, coldcdn.xyz, etc.) do not send intermediate certificates,
// causing Go's strict TLS chain validation to fail.
// ipv4Dialer forces all outbound connections to use IPv4 ("tcp4").
// Many CDNs (cdn.videobase.biz, superdupercdn, etc.) generate IP-locked tokens
// tied to the server's IPv4 address. If Go's dual-stack dialer picks IPv6
// (e.g. via NAT64/DNS64), the CDN sees a different source IP and returns 403.
var ipv4Dialer = &net.Dialer{
	Timeout:   10 * time.Second,
	KeepAlive: 30 * time.Second,
}

// SharedTransport pool sizing rationale (tuned 2026-05-18 via pprof):
//
// Production goroutine dump showed 514 goroutines parked in
// net/http.(*Transport).dialConn — each idle keep-alive connection consumes
// 1 read + 1 write loop goroutine. With MaxIdleConns=500 and steady traffic
// across ~15-20 distinct upstreams, the pool kept ~250 sockets warm at all
// times (500 goroutines). At 1058 req/min we don't need that depth — even a
// 100ms p50 with full reuse only needs ~100 keep-alive sockets total.
//
// Numbers below trade off: higher = better p99 on bursty multi-upstream
// fanout; lower = fewer idle goroutines, smaller memory footprint.
var SharedTransport = &http.Transport{
	DialContext: cachedDialContext,
	TLSClientConfig: &tls.Config{
		// Default-insecure for back-compat: many balancer upstreams
		// (videoseed.tv, coldcdn.xyz, etc.) do not send intermediate
		// certificates. Operators with controlled upstreams should call
		// EnableStrictBalancerTLS at startup to switch to strict mode.
		InsecureSkipVerify: true,
	},
	// 2026-05-19 dropped 500→200 to cut idle goroutines (-300). 2026-05-20
	// bumped back to 300 + perHost 40 because users reported new TLS
	// handshakes on burst traffic (every cold balancer = +200-500ms). 300
	// keeps the goroutine win (still -200 vs original) without the latency
	// regression. Re-evaluate via pprof if traffic shape changes.
	MaxIdleConns:          300,
	MaxIdleConnsPerHost:   40,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
	// HTTP/2 DISABLED (2026-06-26). With InsecureSkipVerify above, Go's h2
	// transport coalesces connections to DIFFERENT hosts that share an IP — it
	// skips the cert-SAN check that normally forbids this. On shared hosting
	// (kino.pub's api.srvkp.com on OVH 51.255.91.x) that routed api.srvkp.com
	// requests over another balancer host's pooled h2 connection → the wrong
	// origin RST'd them → "EOF" / context-canceled, killing kinopub search.
	// HTTP/1.1 does no cross-host coalescing. Confirmed: GODEBUG=http2client=0
	// fixed it. A non-nil empty TLSNextProto is Go's canonical h2-off switch;
	// ForceAttemptHTTP2=false stops ALPN from re-negotiating it. Proxy/CDN
	// transports are separate (UTLS/tls-client) and keep their own h2 settings.
	TLSNextProto:       map[string]func(string, *tls.Conn) http.RoundTripper{},
	ForceAttemptHTTP2:  false,
	DisableCompression: false,
}

// strictTLSConfig is the package-level state set by EnableStrictBalancerTLS.
// When non-nil, it overrides per-Transport tls.Config below.
var (
	strictTLSEnabled atomic.Bool
	insecureHostSet  atomic.Pointer[map[string]struct{}]
	insecureHostSuf  atomic.Pointer[[]string]
)

// EnableStrictBalancerTLS swaps SharedTransport + IPv6Transport from
// InsecureSkipVerify=true to a verifying tls.Config. `insecureHosts` are
// exceptions kept loose (e.g. known-broken upstreams). Match is exact (case
// insensitive) or "*.suffix" wildcard.
//
// Call this once at server startup, BEFORE any HTTP traffic. Calling later
// is safe but in-flight connections retain the original TLS config.
func EnableStrictBalancerTLS(insecureHosts []string) {
	exactHosts := make(map[string]struct{}, len(insecureHosts))
	suffixes := make([]string, 0, len(insecureHosts))
	for _, h := range insecureHosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if strings.HasPrefix(h, "*.") {
			suffixes = append(suffixes, h[1:]) // store ".suffix"
			continue
		}
		exactHosts[h] = struct{}{}
	}
	insecureHostSet.Store(&exactHosts)
	insecureHostSuf.Store(&suffixes)
	strictTLSEnabled.Store(true)

	tlsCfg := &tls.Config{
		InsecureSkipVerify: false,
		VerifyConnection:   verifyConnectionWithAllowlist,
	}
	SharedTransport.TLSClientConfig = tlsCfg
	IPv6Transport.TLSClientConfig = tlsCfg
}

// verifyConnectionWithAllowlist runs after Go's normal cert chain validation
// would have succeeded. For us this means: if Go got here, the cert is
// already valid. We only get the verify-callback path on mismatched-cert
// hosts (which we explicitly skip — handled by Go's standard path).
//
// To allow broken-cert hosts we need a different hook (VerifyPeerCertificate
// without InsecureSkipVerify=false). Instead we use a per-host transport
// shim — see HostInsecureRoundTripper.
func verifyConnectionWithAllowlist(cs tls.ConnectionState) error {
	// When VerifyConnection runs, Go has either:
	//   - validated the chain successfully (we want this), or
	//   - InsecureSkipVerify is set and validation was bypassed.
	// Since we set InsecureSkipVerify=false above, reaching here means
	// validation succeeded. Return nil to accept.
	_ = cs
	return nil
}

// IsHostInsecureAllowlist returns true if host (or a "*.suffix" of it) is in
// the configured allowlist. Used by per-host insecure shims.
func IsHostInsecureAllowlist(host string) bool {
	if !strictTLSEnabled.Load() {
		// Strict mode off — everything is "insecure" by default; callers
		// fall back to the global InsecureSkipVerify=true setting.
		return true
	}
	host = strings.ToLower(host)
	if idx := strings.LastIndex(host, ":"); idx > 0 {
		host = host[:idx]
	}
	if exact := insecureHostSet.Load(); exact != nil {
		if _, ok := (*exact)[host]; ok {
			return true
		}
	}
	if sufs := insecureHostSuf.Load(); sufs != nil {
		for _, s := range *sufs {
			if strings.HasSuffix(host, s) {
				return true
			}
		}
	}
	return false
}

// dualStackDialer — prefers IPv6, falls back to IPv4. Used by sites that
// pollute their IPv4 A-records with anti-RKN CDN tricks (tevas.dev does this:
// IPv4 of pult.tevas.dev / tevas.team points straight at the video CDN nginx
// that doesn't serve HTML, while IPv6 still hits the proper origin via
// Cloudflare). Standard Go dialer's "Happy Eyeballs" still races both stacks;
// we explicitly prefer tcp6 so we always try IPv6 first.
var dualStackDialer = &net.Dialer{
	Timeout:       10 * time.Second,
	KeepAlive:     30 * time.Second,
	FallbackDelay: 100 * time.Millisecond, // RFC 8305 Happy Eyeballs
	DualStack:     true,
}

func ipv6FirstDialContext(ctx context.Context, _, addr string) (net.Conn, error) {
	// Literal IP — pass through.
	if host, _, err := net.SplitHostPort(addr); err == nil {
		if net.ParseIP(host) != nil {
			return dualStackDialer.DialContext(ctx, "tcp", addr)
		}
	}

	// Strategy 1: ask Go for tcp6 only — this triggers an AAAA-only lookup
	// and is the only path that reliably picks up systemd-resolved CNAME
	// records on Linux without CGO. Without it, pure-Go's LookupIPAddr can
	// return only A records when the upstream resolver answers AAAA only
	// via CNAME-chain (the case with tevas.dev's anti-RKN DNS overrides).
	if conn, err := dualStackDialer.DialContext(ctx, "tcp6", addr); err == nil {
		return conn, nil
	}

	// Strategy 2: fall back to tcp4. This is what SharedTransport does anyway,
	// so we end up no worse off than the default.
	return dualStackDialer.DialContext(ctx, "tcp4", addr)
}

// IPv6Transport is a dual-stack HTTP transport that prefers IPv6. Use this
// when SharedTransport's ipv4-only dial breaks an upstream (see comment on
// dualStackDialer above).
//
// Pool sized smaller than SharedTransport because the IPv6-first path is
// used by a narrow set of upstreams (tevas.dev family — see dualStackDialer).
var IPv6Transport = &http.Transport{
	DialContext: ipv6FirstDialContext,
	TLSClientConfig: &tls.Config{
		InsecureSkipVerify: true,
	},
	MaxIdleConns:          150,
	MaxIdleConnsPerHost:   20,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
	// HTTP/2 off — same InsecureSkipVerify h2 cross-host coalescing hazard as
	// SharedTransport (see its comment). H1.1 avoids it.
	TLSNextProto:       map[string]func(string, *tls.Conn) http.RoundTripper{},
	ForceAttemptHTTP2:  false,
	DisableCompression: false,
}

// ProxiedTransport routes traffic through a SOCKS5 proxy (e.g. xray sidecar).
// It is nil when no proxy is configured — callers must check before use.
// Deprecated: use ProxyRegistry for multi-proxy support. Kept for backward
// compatibility with code that checks ProxiedTransport != nil.
var ProxiedTransport *http.Transport

// proxyRegistry maps lowercase balancer name → *http.Transport.
var proxyRegistry = struct {
	sync.RWMutex
	m map[string]*http.Transport
}{m: make(map[string]*http.Transport)}

// socksAddrRegistry maps lowercase balancer name → SOCKS5 address (host:port).
// Used when callers need the raw address (e.g., to pass to external processes).
var socksAddrRegistry = struct {
	sync.RWMutex
	m map[string]string
}{m: make(map[string]string)}

// InitProxiedTransport creates a SOCKS5-backed HTTP transport.
// Call this once during startup when a VLESS/SOCKS5 proxy is configured.
func InitProxiedTransport(socksAddr string) {
	t := newSOCKS5Transport(socksAddr)
	ProxiedTransport = t
}

// RegisterProxiedBalancer registers a SOCKS5-backed transport for the given
// balancer names. Each balancer name is stored in lowercase.
func RegisterProxiedBalancer(socksAddr string, balancers []string) {
	t := newSOCKS5Transport(socksAddr)
	// Also set the global ProxiedTransport for backward compat (first one wins).
	if ProxiedTransport == nil {
		ProxiedTransport = t
	}
	proxyRegistry.Lock()
	for _, name := range balancers {
		proxyRegistry.m[strings.ToLower(name)] = t
	}
	proxyRegistry.Unlock()

	socksAddrRegistry.Lock()
	for _, name := range balancers {
		socksAddrRegistry.m[strings.ToLower(name)] = socksAddr
	}
	socksAddrRegistry.Unlock()
}

// RegisterBalancerTransport registers an explicit *http.Transport for a
// balancer, overwriting any existing entry. Unlike RegisterProxiedBalancer it
// touches neither the global ProxiedTransport nor socksAddrRegistry — use it to
// bind a balancer to a specific (e.g. direct/non-proxied) transport, such as in
// tests that must reach a local httptest server rather than a SOCKS5 exit.
func RegisterBalancerTransport(balancer string, t *http.Transport) {
	proxyRegistry.Lock()
	proxyRegistry.m[strings.ToLower(balancer)] = t
	proxyRegistry.Unlock()
}

// TransportForBalancer returns the proxied transport for the given balancer
// name, or nil if no proxy is configured for it.
func TransportForBalancer(name string) *http.Transport {
	proxyRegistry.RLock()
	t := proxyRegistry.m[strings.ToLower(name)]
	proxyRegistry.RUnlock()
	return t
}

// IsBalancerProxied returns true if the given balancer has a registered proxy
// (either a standard transport or a custom round tripper like RotatingRoundTripper).
func IsBalancerProxied(name string) bool {
	return TransportForBalancer(name) != nil || RoundTripperForBalancer(name) != nil
}

// roundTripperRegistry stores custom http.RoundTripper implementations per balancer.
// Used for advanced transports like RotatingRoundTripper that can't be represented
// as a simple *http.Transport.
var roundTripperRegistry = struct {
	sync.RWMutex
	m map[string]http.RoundTripper
}{m: make(map[string]http.RoundTripper)}

// RegisterRoundTripper stores a custom RoundTripper for the given balancer name.
// This takes priority over standard transport and uTLS registries.
func RegisterRoundTripper(balancer string, rt http.RoundTripper) {
	roundTripperRegistry.Lock()
	roundTripperRegistry.m[strings.ToLower(balancer)] = rt
	roundTripperRegistry.Unlock()
}

// RoundTripperForBalancer returns the custom RoundTripper for the given balancer,
// or nil if none is registered.
func RoundTripperForBalancer(name string) http.RoundTripper {
	roundTripperRegistry.RLock()
	rt := roundTripperRegistry.m[strings.ToLower(name)]
	roundTripperRegistry.RUnlock()
	return rt
}

// SocksAddrForBalancer returns the SOCKS5 address (host:port) for the given
// balancer, or "" if no proxy is configured.
func SocksAddrForBalancer(name string) string {
	socksAddrRegistry.RLock()
	addr := socksAddrRegistry.m[strings.ToLower(name)]
	socksAddrRegistry.RUnlock()
	return addr
}

// AllProxiedBalancers returns a list of all registered proxied balancer names.
func AllProxiedBalancers() []string {
	proxyRegistry.RLock()
	defer proxyRegistry.RUnlock()
	var names []string
	for name := range proxyRegistry.m {
		names = append(names, name)
	}
	return names
}

// ClearRegistry removes all registered proxied balancers and resets ProxiedTransport.
// Used during hot-reload to start fresh before re-registering.
func ClearRegistry() {
	proxyRegistry.Lock()
	proxyRegistry.m = make(map[string]*http.Transport)
	proxyRegistry.Unlock()
	socksAddrRegistry.Lock()
	socksAddrRegistry.m = make(map[string]string)
	socksAddrRegistry.Unlock()
	proxyLabelRegistry.Lock()
	proxyLabelRegistry.m = make(map[string]string)
	proxyLabelRegistry.Unlock()
	roundTripperRegistry.Lock()
	roundTripperRegistry.m = make(map[string]http.RoundTripper)
	roundTripperRegistry.Unlock()
	ProxiedTransport = nil
}

// proxyLabelRegistry maps proxy entry label → SOCKS5 address.
// Labels come from [proxy.vless.entries] and [proxy.direct.entries] config.
// Used by CDN proxy rotation (Mirage/Aladdin) to resolve human-readable labels
// to actual SOCKS5 addresses without hardcoding dynamic sidecar ports.
var proxyLabelRegistry = struct {
	sync.RWMutex
	m map[string]string
}{m: make(map[string]string)}

// RegisterProxyLabel stores a mapping from proxy label to SOCKS5 address.
// Called during startup and hot-reload after sidecar pool initialization.
func RegisterProxyLabel(label, socksAddr string) {
	if label == "" || socksAddr == "" {
		return
	}
	proxyLabelRegistry.Lock()
	proxyLabelRegistry.m[label] = socksAddr
	proxyLabelRegistry.Unlock()
}

// SocksAddrForLabel returns the SOCKS5 address for the given proxy label,
// or "" if the label is not registered.
func SocksAddrForLabel(label string) string {
	proxyLabelRegistry.RLock()
	addr := proxyLabelRegistry.m[label]
	proxyLabelRegistry.RUnlock()
	return addr
}

// RegisterDirectProxy registers a direct SOCKS5 or HTTP proxy for the given
// balancer names. The URI scheme determines the proxy type:
//   - socks5://host:port → SOCKS5 proxy
//   - http://host:port   → HTTP CONNECT proxy
//   - https://host:port  → HTTPS CONNECT proxy
func RegisterDirectProxy(proxyURI string, balancers []string) error {
	u, err := url.Parse(proxyURI)
	if err != nil {
		return fmt.Errorf("httpclient: invalid proxy URI %q: %w", proxyURI, err)
	}

	var t *http.Transport
	switch strings.ToLower(u.Scheme) {
	case "socks5", "socks5h":
		t = newSOCKS5Transport(u.Host)
	case "http", "https":
		t = newHTTPProxyTransport(u)
	default:
		return fmt.Errorf("httpclient: unsupported proxy scheme %q (use socks5, http, or https)", u.Scheme)
	}

	if ProxiedTransport == nil {
		ProxiedTransport = t
	}
	proxyRegistry.Lock()
	for _, name := range balancers {
		proxyRegistry.m[strings.ToLower(name)] = t
	}
	proxyRegistry.Unlock()

	// For SOCKS5, also register the address so callers can pass it to external processes.
	if strings.HasPrefix(strings.ToLower(u.Scheme), "socks5") {
		socksAddrRegistry.Lock()
		for _, name := range balancers {
			socksAddrRegistry.m[strings.ToLower(name)] = u.Host
		}
		socksAddrRegistry.Unlock()
	}
	return nil
}

// newHTTPProxyTransport creates an http.Transport that uses an HTTP CONNECT proxy.
func newHTTPProxyTransport(proxyURL *url.URL) *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          500,
		MaxIdleConnsPerHost:   50,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   20 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     false,
		DisableCompression:    false,
	}
}

// NewSOCKS5TransportPublic creates a one-off SOCKS5 transport for testing
// or temporary use. Unlike RegisterProxiedBalancer, the transport is NOT
// stored in any registry.
func NewSOCKS5TransportPublic(socksAddr string) *http.Transport {
	return newSOCKS5Transport(socksAddr)
}

// newSOCKS5Transport creates an http.Transport that dials through a SOCKS5 proxy.
func newSOCKS5Transport(socksAddr string) *http.Transport {
	dialer, err := proxy.SOCKS5("tcp", socksAddr, nil, proxy.Direct)
	if err != nil {
		panic("httpclient: failed to create SOCKS5 dialer: " + err.Error())
	}

	contextDialer, ok := dialer.(proxy.ContextDialer)
	if !ok {
		return &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.Dial(network, addr)
			},
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   12,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   20 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ForceAttemptHTTP2:     false,
			DisableCompression:    false,
		}
	}

	return &http.Transport{
		DialContext: contextDialer.DialContext,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
		MaxIdleConns:          500,
		MaxIdleConnsPerHost:   50,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   20 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     false,
		DisableCompression:    false,
	}
}

// NewProxied creates an http.Client that routes through ProxiedTransport.
// Falls back to SharedTransport if ProxiedTransport is nil.
func NewProxied(timeout time.Duration) *http.Client {
	t := ProxiedTransport
	if t == nil {
		t = SharedTransport
	}
	return &http.Client{
		Transport: t,
		Timeout:   timeout,
	}
}

// NewProxiedNoRedirect creates a proxied http.Client that does not follow redirects.
func NewProxiedNoRedirect(timeout time.Duration) *http.Client {
	t := ProxiedTransport
	if t == nil {
		t = SharedTransport
	}
	return &http.Client{
		Transport:     t,
		Timeout:       timeout,
		CheckRedirect: noRedirect,
	}
}

// NewForBalancer creates an http.Client using the proxied transport for the
// given balancer name. Falls back to SharedTransport if no proxy is registered.
func NewForBalancer(balancer string, timeout time.Duration) *http.Client {
	t := TransportForBalancer(balancer)
	if t == nil {
		t = SharedTransport
	}
	return &http.Client{
		Transport: t,
		Timeout:   timeout,
	}
}

// dynamicBalancerRoundTripper re-resolves the per-balancer transport on EVERY request instead of
// capturing it once. Use it for LONG-LIVED clients (e.g. the TMDB image/api pool) so a proxy
// assigned at runtime — admin Proxy-panel hot-reload, or registered after the client was built —
// takes effect WITHOUT a server restart. Mirrors the resolution order of NewForBalancer.
type dynamicBalancerRoundTripper struct{ balancer string }

func (d dynamicBalancerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt := RoundTripperForBalancer(d.balancer); rt != nil {
		return rt.RoundTrip(req)
	}
	if t := TransportForBalancer(d.balancer); t != nil {
		return t.RoundTrip(req)
	}
	return SharedTransport.RoundTrip(req)
}

// NewForBalancerDynamic is like NewForBalancer but resolves the proxy transport per request, so a
// proxy mapped to this balancer after the client is created (or via hot-reload) applies live.
func NewForBalancerDynamic(balancer string, timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: dynamicBalancerRoundTripper{balancer: strings.ToLower(balancer)},
		Timeout:   timeout,
	}
}

// NewForBalancerNoTimeout creates an http.Client for the given balancer WITHOUT
// a client-level timeout. Use this for streaming responses (io.Copy from Body)
// where http.Client.Timeout would kill the transfer mid-stream. The caller must
// control deadlines via context.WithTimeout on individual requests.
func NewForBalancerNoTimeout(balancer string) *http.Client {
	t := TransportForBalancer(balancer)
	if t == nil {
		t = SharedTransport
	}
	return &http.Client{
		Transport: t,
		// No Timeout — streaming transfers can run as long as needed.
		// Timeouts are enforced per-request via context deadlines.
	}
}

// NewForBalancerNoRedirect creates an http.Client for the given balancer that
// does not follow redirects.
func NewForBalancerNoRedirect(balancer string, timeout time.Duration) *http.Client {
	t := TransportForBalancer(balancer)
	if t == nil {
		t = SharedTransport
	}
	return &http.Client{
		Transport:     t,
		Timeout:       timeout,
		CheckRedirect: noRedirect,
	}
}

// noRedirect prevents the client from following HTTP redirects.
var noRedirect = func(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}

// New creates an http.Client with the shared transport and specified timeout.
func New(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: SharedTransport,
		Timeout:   timeout,
	}
}

// NewIPv6 creates an http.Client that prefers IPv6 over IPv4. Use for sites
// whose IPv4 DNS records are poisoned by anti-block schemes (e.g. tevas.dev).
func NewIPv6(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: IPv6Transport,
		Timeout:   timeout,
	}
}

// NewNoRedirect creates an http.Client that does not follow redirects.
func NewNoRedirect(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport:     SharedTransport,
		Timeout:       timeout,
		CheckRedirect: noRedirect,
	}
}
