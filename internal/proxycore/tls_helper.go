package proxycore

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"time"

	utls "github.com/refraction-networking/utls"
)

const tcpDialTimeout = 10 * time.Second

// tlsConfig holds parameters for establishing a TLS connection to a proxy server.
type tlsConfig struct {
	Server      string // host:port
	SNI         string
	Fingerprint string // "chrome", "firefox", "safari", "random", ""
	ALPN        string // comma-separated: "h2,http/1.1"
	Insecure    bool
}

// dialTLS establishes a TLS connection to the proxy server using uTLS fingerprinting.
func dialTLS(ctx context.Context, cfg tlsConfig) (net.Conn, error) {
	rawConn, err := dialTCP(ctx, cfg.Server)
	if err != nil {
		return nil, err
	}

	sni := cfg.SNI
	if sni == "" {
		sni, _, _ = net.SplitHostPort(cfg.Server)
	}

	helloID := mapFingerprint(cfg.Fingerprint)

	tlsCfg := &utls.Config{
		ServerName:         sni,
		InsecureSkipVerify: cfg.Insecure,
	}

	if cfg.ALPN != "" {
		for _, proto := range strings.Split(cfg.ALPN, ",") {
			if p := strings.TrimSpace(proto); p != "" {
				tlsCfg.NextProtos = append(tlsCfg.NextProtos, p)
			}
		}
	}

	tlsConn := utls.UClient(rawConn, tlsCfg, helloID)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		rawConn.Close()
		return nil, fmt.Errorf("TLS handshake to %s: %w", cfg.Server, err)
	}

	return tlsConn, nil
}

// dialTCP establishes a plain TCP connection.
func dialTCP(ctx context.Context, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: tcpDialTimeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("TCP dial %s: %w", addr, err)
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}
	return conn, nil
}

// dialPlainTLS uses Go's standard TLS (no fingerprinting). Fallback for simple cases.
func dialPlainTLS(ctx context.Context, addr, sni string, insecure bool) (net.Conn, error) {
	rawConn, err := dialTCP(ctx, addr)
	if err != nil {
		return nil, err
	}
	if sni == "" {
		sni, _, _ = net.SplitHostPort(addr)
	}
	tlsConn := tls.Client(rawConn, &tls.Config{
		ServerName:         sni,
		InsecureSkipVerify: insecure,
	})
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		rawConn.Close()
		return nil, fmt.Errorf("TLS handshake to %s: %w", addr, err)
	}
	return tlsConn, nil
}

// mapFingerprint maps a fingerprint name to a uTLS ClientHelloID.
func mapFingerprint(fp string) utls.ClientHelloID {
	switch strings.ToLower(fp) {
	case "chrome", "":
		return utls.HelloChrome_120
	case "chrome133":
		return utls.HelloChrome_133
	case "firefox":
		return utls.HelloFirefox_120
	case "safari":
		return utls.HelloSafari_Auto
	case "edge":
		return utls.HelloEdge_Auto
	case "random":
		return utls.HelloRandomized
	case "randomized":
		return utls.HelloRandomized
	default:
		return utls.HelloChrome_120
	}
}
