package proxycore

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"

	"lampac-go/internal/sidecar"
)

// socks5OutDialer connects through an upstream SOCKS5 proxy.
type socks5OutDialer struct {
	server   string // host:port of the SOCKS5 proxy
	username string
	password string
}

func newSOCKS5OutDialer(out sidecar.ProxyOutbound) (*socks5OutDialer, error) {
	if out.Server == "" || out.Port == 0 {
		return nil, fmt.Errorf("socks5 outbound: server and port required")
	}
	return &socks5OutDialer{
		server:   fmt.Sprintf("%s:%d", out.Server, out.Port),
		username: out.UUID,     // reuse UUID field for username
		password: out.Password,
	}, nil
}

func (d *socks5OutDialer) Protocol() string { return "socks5" }
func (d *socks5OutDialer) Close() error     { return nil }

func (d *socks5OutDialer) DialProxy(ctx context.Context, targetAddr string) (net.Conn, error) {
	conn, err := dialTCP(ctx, d.server)
	if err != nil {
		return nil, fmt.Errorf("socks5 out: %w", err)
	}

	if err := d.handshake(conn, targetAddr); err != nil {
		conn.Close()
		return nil, fmt.Errorf("socks5 out: %w", err)
	}

	return conn, nil
}

func (d *socks5OutDialer) handshake(conn net.Conn, targetAddr string) error {
	host, port, err := parseTarget(targetAddr)
	if err != nil {
		return err
	}

	// Method negotiation.
	if d.username != "" {
		// Offer no-auth + username/password.
		if _, err := conn.Write([]byte{0x05, 0x02, 0x00, 0x02}); err != nil {
			return err
		}
	} else {
		// Offer no-auth only.
		if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
			return err
		}
	}

	var methodReply [2]byte
	if _, err := io.ReadFull(conn, methodReply[:]); err != nil {
		return fmt.Errorf("method reply: %w", err)
	}
	if methodReply[0] != 0x05 {
		return fmt.Errorf("unexpected SOCKS version %d", methodReply[0])
	}

	// Username/password auth if requested.
	if methodReply[1] == 0x02 {
		if err := d.sendAuth(conn); err != nil {
			return err
		}
	} else if methodReply[1] != 0x00 {
		return fmt.Errorf("unsupported auth method %d", methodReply[1])
	}

	// CONNECT request.
	req := []byte{0x05, 0x01, 0x00} // VER CMD RSV

	// Encode address.
	ip := net.ParseIP(host)
	if ip == nil {
		// Domain name.
		req = append(req, 0x03, byte(len(host)))
		req = append(req, []byte(host)...)
	} else if ip4 := ip.To4(); ip4 != nil {
		req = append(req, 0x01)
		req = append(req, ip4...)
	} else {
		req = append(req, 0x04)
		req = append(req, ip.To16()...)
	}

	// Port (big-endian).
	var portBuf [2]byte
	binary.BigEndian.PutUint16(portBuf[:], port)
	req = append(req, portBuf[:]...)

	if _, err := conn.Write(req); err != nil {
		return fmt.Errorf("connect request: %w", err)
	}

	// Read reply header.
	var reply [4]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return fmt.Errorf("connect reply: %w", err)
	}
	if reply[1] != 0x00 {
		return fmt.Errorf("SOCKS5 connect failed: status %d", reply[1])
	}

	// Skip bound address.
	switch reply[3] {
	case 0x01:
		var skip [4 + 2]byte
		io.ReadFull(conn, skip[:])
	case 0x03:
		var domLen [1]byte
		io.ReadFull(conn, domLen[:])
		skip := make([]byte, int(domLen[0])+2)
		io.ReadFull(conn, skip)
	case 0x04:
		var skip [16 + 2]byte
		io.ReadFull(conn, skip[:])
	}

	return nil
}

func (d *socks5OutDialer) sendAuth(conn net.Conn) error {
	// RFC 1929: VER(1) ULEN(1) UNAME(1-255) PLEN(1) PASSWD(1-255)
	auth := []byte{0x01, byte(len(d.username))}
	auth = append(auth, []byte(d.username)...)
	auth = append(auth, byte(len(d.password)))
	auth = append(auth, []byte(d.password)...)

	if _, err := conn.Write(auth); err != nil {
		return fmt.Errorf("auth: %w", err)
	}

	var reply [2]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return fmt.Errorf("auth reply: %w", err)
	}
	if reply[1] != 0x00 {
		return fmt.Errorf("SOCKS5 auth failed: status %d", reply[1])
	}
	return nil
}
