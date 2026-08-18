package proxycore

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"

	"lampac-go/internal/sidecar"
)

// trojanDialer implements the Trojan protocol.
// Protocol: TLS connection → SHA256(password) hex + CRLF + cmd + ATYP+addr+port + CRLF → payload
type trojanDialer struct {
	server      string // proxy host:port
	passwordHex string // SHA224-hex of password (56 chars)
	tls         tlsConfig
	network     string // "tcp", "ws"
}

func newTrojanDialer(out sidecar.ProxyOutbound) (*trojanDialer, error) {
	if out.Server == "" || out.Port == 0 || out.Password == "" {
		return nil, fmt.Errorf("trojan: server, port, and password required")
	}

	hash := sha256.Sum224([]byte(out.Password))

	return &trojanDialer{
		server:      fmt.Sprintf("%s:%d", out.Server, out.Port),
		passwordHex: hex.EncodeToString(hash[:]),
		tls: tlsConfig{
			Server:      fmt.Sprintf("%s:%d", out.Server, out.Port),
			SNI:         out.SNI,
			Fingerprint: out.Fingerprint,
			ALPN:        out.ALPN,
			Insecure:    out.Security == "none",
		},
		network: out.Network,
	}, nil
}

func (d *trojanDialer) Protocol() string { return "trojan" }
func (d *trojanDialer) Close() error     { return nil }

func (d *trojanDialer) DialProxy(ctx context.Context, targetAddr string) (net.Conn, error) {
	host, port, err := parseTarget(targetAddr)
	if err != nil {
		return nil, fmt.Errorf("trojan: %w", err)
	}

	// 1. Establish TLS connection to proxy server.
	conn, err := dialTLS(ctx, d.tls)
	if err != nil {
		return nil, fmt.Errorf("trojan: %w", err)
	}

	// 2. Optionally wrap with WebSocket transport.
	if d.network == "ws" {
		wsConn, err := upgradeWebSocket(ctx, conn, d.tls.Server, d.tls.SNI)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("trojan ws: %w", err)
		}
		conn = wsConn
	}

	// 3. Send Trojan request header.
	if err := d.sendHeader(conn, host, port); err != nil {
		conn.Close()
		return nil, fmt.Errorf("trojan header: %w", err)
	}

	return conn, nil
}

// sendHeader writes the Trojan protocol header:
// SHA224_HEX(56) + CRLF + CMD(1) + ATYP(1) + ADDR(variable) + PORT(2) + CRLF
func (d *trojanDialer) sendHeader(conn net.Conn, host string, port uint16) error {
	buf := make([]byte, 0, 128)

	// Password hash (56 hex chars).
	buf = append(buf, []byte(d.passwordHex)...)
	buf = append(buf, '\r', '\n')

	// Command: CONNECT.
	buf = append(buf, 0x01)

	// Address.
	buf = appendSOCKSAddr(buf, host, port)

	// Trailing CRLF.
	buf = append(buf, '\r', '\n')

	_, err := conn.Write(buf)
	return err
}

// appendSOCKSAddr appends a SOCKS5-style address (ATYP + addr + port) to buf.
func appendSOCKSAddr(buf []byte, host string, port uint16) []byte {
	ip := net.ParseIP(host)
	if ip == nil {
		// Domain name.
		buf = append(buf, 0x03, byte(len(host)))
		buf = append(buf, []byte(host)...)
	} else if ip4 := ip.To4(); ip4 != nil {
		buf = append(buf, 0x01)
		buf = append(buf, ip4...)
	} else {
		buf = append(buf, 0x04)
		buf = append(buf, ip.To16()...)
	}

	var portBuf [2]byte
	binary.BigEndian.PutUint16(portBuf[:], port)
	buf = append(buf, portBuf[:]...)

	return buf
}
