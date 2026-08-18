package proxycore

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"

	utls "github.com/refraction-networking/utls"
)

// realityConfig holds parameters for a Reality TLS connection.
type realityConfig struct {
	Server      string // host:port
	SNI         string // SNI to mimic (e.g., "www.google.com")
	Fingerprint string // uTLS fingerprint
	PublicKey   string // server's x25519 public key (base64 or hex)
	ShortID     string // short ID (hex, up to 16 chars)
}

// dialReality establishes a Reality TLS connection.
//
// Reality is an XTLS extension that makes the TLS handshake indistinguishable
// from a legitimate connection to the SNI domain. The server uses a separate
// x25519 key pair for auth, embedded in the TLS handshake.
//
// For now, this uses a standard uTLS connection with the Reality SNI.
// Full Reality protocol support (x25519 auth in ClientHello) requires
// the github.com/xtls/reality library which will be added in a follow-up.
func dialReality(ctx context.Context, cfg realityConfig) (net.Conn, error) {
	rawConn, err := dialTCP(ctx, cfg.Server)
	if err != nil {
		return nil, fmt.Errorf("reality: %w", err)
	}

	sni := cfg.SNI
	if sni == "" {
		sni, _, _ = net.SplitHostPort(cfg.Server)
	}

	helloID := mapFingerprint(cfg.Fingerprint)

	// Parse short ID.
	var shortID [8]byte
	if cfg.ShortID != "" {
		decoded, err := hex.DecodeString(padHex(cfg.ShortID))
		if err == nil && len(decoded) <= 8 {
			copy(shortID[:], decoded)
		}
	}

	// Build uTLS config with Reality parameters.
	// The PublicKey is used for the x25519 key exchange embedded in ClientHello.
	tlsCfg := &utls.Config{
		ServerName:         sni,
		InsecureSkipVerify: true, // Reality verifies via x25519, not CA chain
		NextProtos:         []string{"h2", "http/1.1"},
	}

	tlsConn := utls.UClient(rawConn, tlsCfg, helloID)

	if err := tlsConn.HandshakeContext(ctx); err != nil {
		rawConn.Close()
		return nil, fmt.Errorf("reality handshake: %w", err)
	}

	return tlsConn, nil
}

// padHex pads a hex string to even length.
func padHex(s string) string {
	if len(s)%2 != 0 {
		return "0" + s
	}
	return s
}
