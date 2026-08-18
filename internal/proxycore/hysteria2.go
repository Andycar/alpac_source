package proxycore

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/quic-go/quic-go"

	"lampac-go/internal/sidecar"
)

// hy2Dialer implements the Hysteria2 protocol over QUIC.
type hy2Dialer struct {
	server   string
	password string
	sni      string
	insecure bool
	obfs     string
	obfsPass string

	mu      sync.Mutex
	session *quic.Conn
}

func newHysteria2Dialer(out sidecar.ProxyOutbound) (*hy2Dialer, error) {
	if out.Server == "" || out.Port == 0 {
		return nil, fmt.Errorf("hysteria2: server and port required")
	}
	sni := out.SNI
	if sni == "" {
		sni = out.Server
	}
	return &hy2Dialer{
		server:   fmt.Sprintf("%s:%d", out.Server, out.Port),
		password: out.Password,
		sni:      sni,
		insecure: out.Security == "none",
		obfs:     out.Obfs,
		obfsPass: out.ObfsPassword,
	}, nil
}

func (d *hy2Dialer) Protocol() string { return "hysteria2" }

func (d *hy2Dialer) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.session != nil {
		d.session.CloseWithError(0, "closed")
		d.session = nil
	}
	return nil
}

func (d *hy2Dialer) DialProxy(ctx context.Context, targetAddr string) (net.Conn, error) {
	host, port, err := parseTarget(targetAddr)
	if err != nil {
		return nil, fmt.Errorf("hysteria2: %w", err)
	}

	session, err := d.getOrCreateSession(ctx)
	if err != nil {
		return nil, fmt.Errorf("hysteria2 session: %w", err)
	}

	stream, err := session.OpenStreamSync(ctx)
	if err != nil {
		// Session may be dead, try reconnecting once.
		d.mu.Lock()
		d.session = nil
		d.mu.Unlock()

		session, err = d.getOrCreateSession(ctx)
		if err != nil {
			return nil, fmt.Errorf("hysteria2 reconnect: %w", err)
		}
		stream, err = session.OpenStreamSync(ctx)
		if err != nil {
			return nil, fmt.Errorf("hysteria2 stream: %w", err)
		}
	}

	// Send Hysteria2 request header.
	if err := d.sendRequest(stream, host, port); err != nil {
		stream.Close()
		return nil, fmt.Errorf("hysteria2 request: %w", err)
	}

	// Read response.
	if err := d.readResponse(stream); err != nil {
		stream.Close()
		return nil, fmt.Errorf("hysteria2 response: %w", err)
	}

	return &hy2StreamConn{stream: stream, session: session}, nil
}

func (d *hy2Dialer) getOrCreateSession(ctx context.Context) (*quic.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.session != nil {
		return d.session, nil
	}

	tlsCfg := &tls.Config{
		ServerName:         d.sni,
		InsecureSkipVerify: d.insecure,
		NextProtos:         []string{"h3"},
	}

	quicCfg := &quic.Config{
		MaxIdleTimeout:  60 * time.Second,
		KeepAlivePeriod: 15 * time.Second,
	}

	session, err := quic.DialAddr(ctx, d.server, tlsCfg, quicCfg)
	if err != nil {
		return nil, err
	}

	// Authenticate via HTTP/3 CONNECT-style header.
	authStream, err := session.OpenStreamSync(ctx)
	if err != nil {
		session.CloseWithError(0, "auth failed")
		return nil, err
	}

	// Send Hysteria2 auth: password-based.
	authHeader := fmt.Sprintf("Hysteria2-Auth: %s\r\n\r\n", d.password)
	if _, err := authStream.Write([]byte(authHeader)); err != nil {
		session.CloseWithError(0, "auth write failed")
		return nil, err
	}
	authStream.Close()

	d.session = session
	return session, nil
}

// sendRequest sends the Hysteria2 TCP proxy request.
func (d *hy2Dialer) sendRequest(stream *quic.Stream, host string, port uint16) error {
	buf := []byte{0x00} // Request type: TCP connect.

	addrBytes := []byte(host)
	buf = append(buf, byte(len(addrBytes)))
	buf = append(buf, addrBytes...)

	var portBuf [2]byte
	binary.BigEndian.PutUint16(portBuf[:], port)
	buf = append(buf, portBuf[:]...)

	_, err := stream.Write(buf)
	return err
}

// readResponse reads the Hysteria2 proxy response.
func (d *hy2Dialer) readResponse(stream *quic.Stream) error {
	var status [1]byte
	if _, err := io.ReadFull(stream, status[:]); err != nil {
		return fmt.Errorf("read status: %w", err)
	}
	if status[0] != 0x00 {
		var msgLen [1]byte
		io.ReadFull(stream, msgLen[:])
		if msgLen[0] > 0 {
			msg := make([]byte, msgLen[0])
			io.ReadFull(stream, msg)
			return fmt.Errorf("proxy refused: %s", string(msg))
		}
		return fmt.Errorf("proxy refused: status %d", status[0])
	}
	return nil
}

// hy2StreamConn wraps a QUIC stream as a net.Conn.
type hy2StreamConn struct {
	stream  *quic.Stream
	session *quic.Conn
}

func (c *hy2StreamConn) Read(p []byte) (int, error)  { return c.stream.Read(p) }
func (c *hy2StreamConn) Write(p []byte) (int, error) { return c.stream.Write(p) }
func (c *hy2StreamConn) Close() error                { return c.stream.Close() }

func (c *hy2StreamConn) LocalAddr() net.Addr  { return c.session.LocalAddr() }
func (c *hy2StreamConn) RemoteAddr() net.Addr { return c.session.RemoteAddr() }

func (c *hy2StreamConn) SetDeadline(t time.Time) error {
	_ = c.stream.SetReadDeadline(t)
	return c.stream.SetWriteDeadline(t)
}

func (c *hy2StreamConn) SetReadDeadline(t time.Time) error {
	return c.stream.SetReadDeadline(t)
}

func (c *hy2StreamConn) SetWriteDeadline(t time.Time) error {
	return c.stream.SetWriteDeadline(t)
}
