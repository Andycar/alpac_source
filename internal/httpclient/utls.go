// Package httpclient — uTLS transport for TLS fingerprint spoofing.
//
// Sites behind advanced anti-bot systems (DDoS-Guard, Cloudflare) may filter
// requests by JA3/TLS fingerprint AND HTTP/2 fingerprint (Settings frames,
// pseudo-header order). Go's default crypto/tls produces a unique fingerprint
// that is trivially blocked. uTLS (refraction-networking/utls) impersonates
// a real browser's TLS ClientHello, and we use x/net/http2.Transport with
// NewClientConn to properly handle HTTP/2 over the uTLS connection.
//
// The round tripper respects ALPN negotiation: if the server negotiates "h2",
// HTTP/2 is used. If "http/1.1" (or no ALPN), a standard HTTP/1.1 transport
// is used with the uTLS connection — avoiding "frame too large" errors on
// HTTP/1.1-only servers like nginx/1.18.0.
//
// IMPORTANT: Some CDNs (like DDoS-Guard on kinoserial.net) also check HTTP
// headers — requests MUST include Chrome-like Sec-Fetch-*, sec-ch-ua*, etc.
// This is the caller's responsibility.
package httpclient

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"lampac-go/internal/antidpi"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
	"golang.org/x/net/proxy"
)

// dialFunc abstracts the TCP dialing step so we can swap in a SOCKS5 dialer.
type dialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// utlsRoundTripper is a custom RoundTripper that uses uTLS for TLS handshake
// and properly handles both HTTP/2 and HTTP/1.1 based on ALPN negotiation.
type utlsRoundTripper struct {
	helloID     utls.ClientHelloID
	dialConn    dialFunc      // TCP dial step (direct or via SOCKS5 proxy)
	dialTimeout time.Duration // max time for SOCKS5 + TLS handshake (0 = no limit)

	// h2Transport creates new HTTP/2 client connections.
	h2Transport *http2.Transport

	// h1Transport handles HTTP/1.1 over uTLS connections.
	// Lazily created on first HTTP/1.1 host encountered.
	h1Transport     *http.Transport
	h1TransportOnce sync.Once

	mu      sync.Mutex
	h2Conns map[string]*http2.ClientConn // addr → cached HTTP/2 conn
	rawConn map[string]net.Conn          // addr → underlying TLS conn
	h1Hosts map[string]bool              // hosts known to use HTTP/1.1
}

func newUTLSRoundTripper(helloID utls.ClientHelloID) *utlsRoundTripper {
	dialer := &net.Dialer{
		Timeout:   15 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	return &utlsRoundTripper{
		helloID:  helloID,
		dialConn: dialer.DialContext,
		h2Transport: &http2.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			// Chrome-like HTTP/2 SETTINGS to avoid CDN fingerprinting.
			// Default Go values (e.g. HEADER_TABLE_SIZE=4096) are different
			// from Chrome (65536) and some CDNs detect/block based on this.
			MaxDecoderHeaderTableSize:  65536,
			MaxEncoderHeaderTableSize:  65536,
			MaxHeaderListSize:          262144,
			MaxReadFrameSize:           16384,
			StrictMaxConcurrentStreams: true,
		},
		h2Conns: make(map[string]*http2.ClientConn),
		rawConn: make(map[string]net.Conn),
		h1Hosts: make(map[string]bool),
	}
}

// newUTLSRoundTripperSOCKS5 creates a uTLS roundtripper that dials through SOCKS5.
func newUTLSRoundTripperSOCKS5(helloID utls.ClientHelloID, socksAddr string) *utlsRoundTripper {
	socksDialer, err := proxy.SOCKS5("tcp", socksAddr, nil, proxy.Direct)
	if err != nil {
		// Fallback to direct if SOCKS5 fails.
		return newUTLSRoundTripper(helloID)
	}
	ctxDialer, ok := socksDialer.(proxy.ContextDialer)
	if !ok {
		// Wrap non-context dialer.
		rt := newUTLSRoundTripper(helloID)
		rt.dialConn = func(_ context.Context, network, addr string) (net.Conn, error) {
			return socksDialer.Dial(network, addr)
		}
		return rt
	}
	rt := newUTLSRoundTripper(helloID)
	rt.dialConn = ctxDialer.DialContext
	return rt
}

func (rt *utlsRoundTripper) dialUTLS(ctx context.Context, addr string) (net.Conn, string, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}

	// Apply dial timeout if configured (limits SOCKS5 connect + TLS handshake).
	// This prevents indefinite hangs when DPI blocks or drops packets.
	dialCtx := ctx
	if rt.dialTimeout > 0 {
		var cancel context.CancelFunc
		dialCtx, cancel = context.WithTimeout(ctx, rt.dialTimeout)
		defer cancel()
	}

	rawConn, err := rt.dialConn(dialCtx, "tcp", addr)
	if err != nil {
		return nil, "", err
	}

	config := &utls.Config{
		ServerName:         host,
		InsecureSkipVerify: true,
	}
	tlsConn := utls.UClient(rawConn, config, rt.helloID)

	if err := tlsConn.HandshakeContext(dialCtx); err != nil {
		_ = rawConn.Close()
		return nil, "", err
	}

	alpn := tlsConn.ConnectionState().NegotiatedProtocol
	return tlsConn, alpn, nil
}

func (rt *utlsRoundTripper) getOrDialH2(ctx context.Context, addr string) (*http2.ClientConn, error) {
	rt.mu.Lock()
	if cc, ok := rt.h2Conns[addr]; ok && cc.CanTakeNewRequest() {
		rt.mu.Unlock()
		return cc, nil
	}
	// Clean up dead connection.
	if old, ok := rt.rawConn[addr]; ok {
		_ = old.Close()
		delete(rt.rawConn, addr)
		delete(rt.h2Conns, addr)
	}
	rt.mu.Unlock()

	conn, alpn, err := rt.dialUTLS(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("utls dial %s: %w", addr, err)
	}

	// If server negotiated HTTP/1.1, mark it and close this connection.
	// The caller will fall through to the HTTP/1.1 path.
	if alpn != "h2" {
		_ = conn.Close()
		rt.mu.Lock()
		rt.h1Hosts[addr] = true
		rt.mu.Unlock()
		return nil, nil // nil cc signals "use HTTP/1.1"
	}

	cc, err := rt.h2Transport.NewClientConn(conn)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("h2 client conn: %w", err)
	}

	rt.mu.Lock()
	rt.h2Conns[addr] = cc
	rt.rawConn[addr] = conn
	rt.mu.Unlock()

	return cc, nil
}

// getH1Transport returns a lazily-created HTTP/1.1 transport that uses uTLS
// for TLS handshakes. The transport manages its own connection pool.
func (rt *utlsRoundTripper) getH1Transport() *http.Transport {
	rt.h1TransportOnce.Do(func() {
		rt.h1Transport = &http.Transport{
			DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				conn, _, err := rt.dialUTLS(ctx, addr)
				return conn, err
			},
			ForceAttemptHTTP2:   false,
			MaxIdleConns:        50,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
		}
	})
	return rt.h1Transport
}

func (rt *utlsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" {
		return http.DefaultTransport.RoundTrip(req)
	}

	addr := req.URL.Host
	if !hasPort(addr) {
		addr += ":443"
	}

	// Check if this host is known to use HTTP/1.1.
	rt.mu.Lock()
	isH1 := rt.h1Hosts[addr]
	rt.mu.Unlock()
	if isH1 {
		return rt.getH1Transport().RoundTrip(req)
	}

	// Try HTTP/2 path (will detect ALPN and may return nil for HTTP/1.1 hosts).
	cc, err := rt.getOrDialH2(req.Context(), addr)
	if err != nil {
		return nil, err
	}
	if cc == nil {
		// Server uses HTTP/1.1 — fall through to HTTP/1.1 transport.
		return rt.getH1Transport().RoundTrip(req)
	}

	resp, err := cc.RoundTrip(req)
	if err != nil {
		// Connection may be dead — remove from cache and retry once.
		rt.mu.Lock()
		delete(rt.h2Conns, addr)
		if old, ok := rt.rawConn[addr]; ok {
			_ = old.Close()
			delete(rt.rawConn, addr)
		}
		rt.mu.Unlock()

		cc2, err2 := rt.getOrDialH2(req.Context(), addr)
		if err2 != nil {
			return nil, err // Return original error.
		}
		if cc2 == nil {
			// Switched to HTTP/1.1 on retry.
			return rt.getH1Transport().RoundTrip(req)
		}
		return cc2.RoundTrip(req)
	}

	return resp, nil
}

func hasPort(host string) bool {
	_, _, err := net.SplitHostPort(host)
	return err == nil
}

// UTLSTransport is a shared uTLS transport with Chrome TLS fingerprint.
// Supports both HTTP/2 and HTTP/1.1 based on server ALPN negotiation.
// Use it for hosts that block standard Go HTTP clients via JA3/TLS filtering.
//
// IMPORTANT: Callers MUST set Chrome-like headers (Sec-Fetch-*, sec-ch-ua*,
// Upgrade-Insecure-Requests) for CDNs that also check HTTP headers.
var UTLSTransport = newUTLSRoundTripper(utls.HelloChrome_120)

// UTLSTransport133 is a uTLS transport with Chrome 133 TLS fingerprint.
// Used for hosts behind Cloudflare Turnstile that bind cf_clearance cookies
// to the exact JA3 fingerprint — must match the user's real browser version.
var UTLSTransport133 = newUTLSRoundTripper(utls.HelloChrome_133)

// NewUTLS creates an http.Client with Chrome TLS fingerprint.
func NewUTLS(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: UTLSTransport,
		Timeout:   timeout,
	}
}

// newUTLSRoundTripperIPv4 is like newUTLSRoundTripper but pins dialing to IPv4.
// The default dual-stack dialer prefers AAAA, and on some hosts the IPv6 path to
// a Cloudflare-fronted origin is flaky — the TCP connects but the TLS handshake
// intermittently EOFs. Forcing tcp4 sidesteps that and keeps the egress IP
// stable (important for CDNs that IP-bind signed URLs).
func newUTLSRoundTripperIPv4(helloID utls.ClientHelloID) *utlsRoundTripper {
	rt := newUTLSRoundTripper(helloID)
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	rt.dialConn = func(ctx context.Context, _, addr string) (net.Conn, error) {
		return dialer.DialContext(ctx, "tcp4", addr)
	}
	return rt
}

// UTLSTransportIPv4 is a Chrome-fingerprint uTLS transport pinned to IPv4. Use
// it for hosts whose IPv6 path is unreliable from the deploy environment.
var UTLSTransportIPv4 = newUTLSRoundTripperIPv4(utls.HelloChrome_120)

// NewUTLSIPv4 creates a uTLS http.Client (Chrome fingerprint) that dials IPv4 only.
func NewUTLSIPv4(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: UTLSTransportIPv4,
		Timeout:   timeout,
	}
}

// NewUTLSNoRedirect creates a uTLS http.Client that does not follow redirects.
func NewUTLSNoRedirect(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport:     UTLSTransport,
		Timeout:       timeout,
		CheckRedirect: noRedirect,
	}
}

// NewUTLSForBalancer creates an http.Client with Chrome TLS fingerprint that
// routes traffic through the SOCKS5 proxy registered for the given balancer.
// Falls back to direct connection if no proxy is configured.
func NewUTLSForBalancer(balancer string, timeout time.Duration) *http.Client {
	socksAddr := SocksAddrForBalancer(balancer)
	var rt http.RoundTripper
	if socksAddr != "" {
		rt = newUTLSRoundTripperSOCKS5(utls.HelloChrome_120, socksAddr)
	} else {
		rt = UTLSTransport
	}
	return &http.Client{
		Transport: rt,
		Timeout:   timeout,
	}
}

// NewUTLSForBalancerNoRedirect creates a proxied uTLS client that doesn't follow redirects.
func NewUTLSForBalancerNoRedirect(balancer string, timeout time.Duration) *http.Client {
	socksAddr := SocksAddrForBalancer(balancer)
	var rt http.RoundTripper
	if socksAddr != "" {
		rt = newUTLSRoundTripperSOCKS5(utls.HelloChrome_120, socksAddr)
	} else {
		rt = UTLSTransport
	}
	return &http.Client{
		Transport:     rt,
		Timeout:       timeout,
		CheckRedirect: noRedirect,
	}
}

// UTLSTransportForBalancer returns a shared uTLS+SOCKS5 round tripper for a
// balancer. Unlike NewUTLSForBalancer (which creates a new client per call),
// this caches the transport for reuse in hot paths like the proxy handler.
// Checks the custom round-tripper registry first (for RotatingRoundTripper etc.),
// then falls back to SOCKS5-based uTLS.
// Returns nil if no proxy is configured for the balancer.
func UTLSTransportForBalancer(balancer string) http.RoundTripper {
	// Check custom round-tripper registry first (e.g. RotatingRoundTripper).
	if rt := RoundTripperForBalancer(balancer); rt != nil {
		return rt
	}
	socksAddr := SocksAddrForBalancer(balancer)
	if socksAddr == "" {
		return nil
	}
	return newUTLSRoundTripperSOCKS5(utls.HelloChrome_120, socksAddr)
}

// NewUTLSViaSOCKS5 creates an http.Client with Chrome TLS fingerprint that
// routes traffic through the given SOCKS5 proxy address.
// socksAddr format: "host:port" (e.g. "1.2.3.4:1080")
func NewUTLSViaSOCKS5(socksAddr string, timeout time.Duration) *http.Client {
	rt := newUTLSRoundTripperSOCKS5(utls.HelloChrome_133, socksAddr)
	return &http.Client{
		Transport: rt,
		Timeout:   timeout,
	}
}

// NewUTLSViaSOCKS5NoTimeout creates a uTLS+SOCKS5 client without a timeout.
// Used for streaming large segments (4K .ts/.m4s) where a fixed deadline
// would abort legitimate slow downloads.
func NewUTLSViaSOCKS5NoTimeout(socksAddr string) *http.Client {
	rt := newUTLSRoundTripperSOCKS5(utls.HelloChrome_133, socksAddr)
	return &http.Client{Transport: rt}
}

// NewStreamClientViaSOCKS5 creates an HTTP client optimized for streaming large
// files through a SOCKS5 proxy with DPI bypass (e.g. YouTube video segments).
//
// Differences from NewUTLSViaSOCKS5NoTimeout:
//   - Chrome 133 fingerprint (larger, modern ClientHello ~1400 bytes vs ~550 for Chrome 120)
//   - 20s timeout for SOCKS5 connect + TLS handshake (prevents indefinite hangs
//     when DPI drops packets or blocks the handshake)
//   - No overall client timeout (streaming can take hours for large videos)
func NewStreamClientViaSOCKS5(socksAddr string) *http.Client {
	rt := newUTLSRoundTripperSOCKS5(utls.HelloChrome_133, socksAddr)
	rt.dialTimeout = 20 * time.Second
	return &http.Client{Transport: rt}
}

// NewDirectSplitStreamClient creates an HTTP client that connects DIRECTLY
// to the target (bypassing SOCKS5 proxy) with TLS ClientHello splitting for
// DPI bypass. Uses uTLS Chrome 133 fingerprint.
//
// This is more effective than the SOCKS5 proxy path for YouTube CDN edge
// servers because:
//   - Per-connection strategy selection (split-sni is most effective)
//   - No SOCKS5 overhead
//   - 20s timeout for connect + TLS handshake
//
// Use this for cases where the Go process itself is the endpoint (not proxying
// for another client) and needs DPI bypass.
func NewDirectSplitStreamClient(st antidpi.Strategy) *http.Client {
	rt := newUTLSRoundTripper(utls.HelloChrome_133)
	rt.dialConn = antidpi.SplitDialer(st)
	rt.dialTimeout = 20 * time.Second
	return &http.Client{Transport: rt}
}

// NewDirectNoSNIStreamClient creates an HTTP client that connects directly to
// the target WITHOUT sending a TLS SNI extension. This defeats SNI-based DPI
// blocking because the DPI can't identify the target hostname from the ClientHello.
//
// Uses Go's standard crypto/tls (not uTLS) with InsecureSkipVerify=true.
// The server may serve a default/wildcard certificate — we don't verify it.
// The HTTP Host header still tells the server which content to serve.
//
// Works for CDN edge servers that accept connections without SNI (common for
// Google/YouTube CDN since they use IP-based routing within their infrastructure).
func NewDirectNoSNIStreamClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
				defer cancel()

				var d net.Dialer
				conn, err := d.DialContext(dialCtx, "tcp4", addr)
				if err != nil {
					return nil, err
				}

				// TLS with empty ServerName → no SNI extension sent.
				// DPI can't determine the target hostname.
				tlsConn := tls.Client(conn, &tls.Config{
					InsecureSkipVerify: true,
					// ServerName deliberately left empty.
				})

				hsCtx, hsCancel := context.WithTimeout(ctx, 15*time.Second)
				defer hsCancel()
				if err := tlsConn.HandshakeContext(hsCtx); err != nil {
					_ = conn.Close()
					return nil, err
				}
				return tlsConn, nil
			},
			ResponseHeaderTimeout: 30 * time.Second,
		},
	}
}

// ChromeHeaders returns the set of HTTP headers that Chrome sends for page
// navigation requests. CDNs with advanced anti-bot protection (DDoS-Guard)
// often check these headers and block requests without them.
func ChromeHeaders(referer string) http.Header {
	return http.Header{
		"Accept":                    {"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7"},
		"Accept-Encoding":           {"gzip, deflate, br, zstd"},
		"Accept-Language":           {"ru-RU,ru;q=0.9,en-US;q=0.8,en;q=0.7"},
		"Cache-Control":             {"max-age=0"},
		"Referer":                   {referer},
		"Sec-Ch-Ua":                 {`"Chromium";v="131", "Not_A Brand";v="24"`},
		"Sec-Ch-Ua-Mobile":          {"?0"},
		"Sec-Ch-Ua-Platform":        {`"Linux"`},
		"Sec-Fetch-Dest":            {"iframe"},
		"Sec-Fetch-Mode":            {"navigate"},
		"Sec-Fetch-Site":            {"cross-site"},
		"Upgrade-Insecure-Requests": {"1"},
		"User-Agent":                {"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"},
	}
}
