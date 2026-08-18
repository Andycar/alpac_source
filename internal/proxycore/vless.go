package proxycore

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"

	"lampac-go/internal/sidecar"
)

// vlessDialer implements the VLESS protocol.
// Spec: Version(0) + UUID(16B) + AddonsLen + Addons + Cmd + ATYP+Addr+Port → payload
type vlessDialer struct {
	server  string // proxy host:port
	uuid    [16]byte
	flow    string // "xtls-rprx-vision" or ""
	tls     tlsConfig
	network string // "tcp", "ws", "grpc", "h2"
	path    string // WS path or gRPC service name

	// Reality fields.
	useReality bool
	realityCfg realityConfig
}

func newVLESSDialer(out sidecar.ProxyOutbound) (*vlessDialer, error) {
	if out.Server == "" || out.Port == 0 || out.UUID == "" {
		return nil, fmt.Errorf("vless: server, port, and UUID required")
	}

	uuid, err := parseUUID(out.UUID)
	if err != nil {
		return nil, fmt.Errorf("vless: %w", err)
	}

	alpn := out.ALPN
	// gRPC transport requires ALPN h2.
	if out.Network == "grpc" && alpn == "" {
		alpn = "h2"
	}

	d := &vlessDialer{
		server:  fmt.Sprintf("%s:%d", out.Server, out.Port),
		uuid:    uuid,
		flow:    out.Flow,
		network: out.Network,
		path:    out.Path,
		tls: tlsConfig{
			Server:      fmt.Sprintf("%s:%d", out.Server, out.Port),
			SNI:         out.SNI,
			Fingerprint: out.Fingerprint,
			ALPN:        alpn,
		},
	}

	if out.Security == "reality" {
		d.useReality = true
		d.realityCfg = realityConfig{
			Server:      d.server,
			SNI:         out.SNI,
			Fingerprint: out.Fingerprint,
			PublicKey:    out.PublicKey,
			ShortID:     out.ShortID,
		}
	}

	return d, nil
}

func (d *vlessDialer) Protocol() string { return "vless" }
func (d *vlessDialer) Close() error     { return nil }

func (d *vlessDialer) DialProxy(ctx context.Context, targetAddr string) (net.Conn, error) {
	host, port, err := parseTarget(targetAddr)
	if err != nil {
		return nil, fmt.Errorf("vless: %w", err)
	}

	// 1. Establish connection to proxy server.
	var conn net.Conn
	if d.useReality {
		conn, err = dialReality(ctx, d.realityCfg)
	} else {
		conn, err = dialTLS(ctx, d.tls)
	}
	if err != nil {
		return nil, fmt.Errorf("vless: %w", err)
	}

	// 2. Optionally wrap with transport layer.
	switch d.network {
	case "ws":
		wsConn, err := upgradeWebSocket(ctx, conn, d.server, d.tls.SNI)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("vless ws: %w", err)
		}
		conn = wsConn
	case "grpc":
		grpcConn, err := upgradeGRPC(ctx, conn, d.tls.SNI, d.path)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("vless grpc: %w", err)
		}
		conn = grpcConn
	}

	// 3. Send VLESS request header.
	if err := d.sendHeader(conn, host, port); err != nil {
		conn.Close()
		return nil, fmt.Errorf("vless header: %w", err)
	}

	// 4. Read VLESS response header.
	if err := readVLESSResponse(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("vless response: %w", err)
	}

	return conn, nil
}

// sendHeader writes the VLESS request header:
// Version(1) + UUID(16) + AddonsLen(1) + [Addons] + Cmd(1) + ATYP + ADDR + PORT
func (d *vlessDialer) sendHeader(conn net.Conn, host string, port uint16) error {
	buf := make([]byte, 0, 128)

	// Version.
	buf = append(buf, 0x00)

	// UUID (16 bytes).
	buf = append(buf, d.uuid[:]...)

	// Addons.
	if d.flow != "" {
		// Addon: flow field.
		addon := encodeVLESSAddon(d.flow)
		buf = append(buf, byte(len(addon)))
		buf = append(buf, addon...)
	} else {
		buf = append(buf, 0x00) // no addons
	}

	// Command: TCP (0x01).
	buf = append(buf, 0x01)

	// Port (big-endian, before address in VLESS).
	var portBuf [2]byte
	binary.BigEndian.PutUint16(portBuf[:], port)
	buf = append(buf, portBuf[:]...)

	// Address type + address.
	ip := net.ParseIP(host)
	if ip == nil {
		// Domain.
		buf = append(buf, 0x02, byte(len(host)))
		buf = append(buf, []byte(host)...)
	} else if ip4 := ip.To4(); ip4 != nil {
		buf = append(buf, 0x01)
		buf = append(buf, ip4...)
	} else {
		buf = append(buf, 0x03)
		buf = append(buf, ip.To16()...)
	}

	_, err := conn.Write(buf)
	return err
}

// encodeVLESSAddon encodes a protobuf-like addon for the flow field.
// VLESS addons use a simplified protobuf: field 1 (string) = flow name.
func encodeVLESSAddon(flow string) []byte {
	if flow == "" {
		return nil
	}
	// Protobuf field 1, wire type 2 (length-delimited): tag = 0x0a
	buf := []byte{0x0a, byte(len(flow))}
	buf = append(buf, []byte(flow)...)
	return buf
}

// readVLESSResponse reads the VLESS server response header.
// Format: Version(1) + AddonsLen(1) + [Addons]
func readVLESSResponse(conn net.Conn) error {
	var hdr [2]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return fmt.Errorf("read response header: %w", err)
	}

	// hdr[0] = version (should be 0)
	// hdr[1] = addons length
	if hdr[1] > 0 {
		addons := make([]byte, hdr[1])
		if _, err := io.ReadFull(conn, addons); err != nil {
			return fmt.Errorf("read response addons: %w", err)
		}
	}

	return nil
}

// parseUUID parses a UUID string (with or without dashes) into 16 bytes.
func parseUUID(s string) ([16]byte, error) {
	var uuid [16]byte

	// Remove dashes.
	clean := make([]byte, 0, 32)
	for _, c := range []byte(s) {
		if c != '-' {
			clean = append(clean, c)
		}
	}

	if len(clean) != 32 {
		return uuid, fmt.Errorf("invalid UUID length: %d (expected 32 hex chars)", len(clean))
	}

	for i := 0; i < 16; i++ {
		hi := unhex(clean[i*2])
		lo := unhex(clean[i*2+1])
		if hi == 0xFF || lo == 0xFF {
			return uuid, fmt.Errorf("invalid hex char in UUID at position %d", i*2)
		}
		uuid[i] = hi<<4 | lo
	}

	return uuid, nil
}

func unhex(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	default:
		return 0xFF
	}
}
