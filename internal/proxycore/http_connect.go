package proxycore

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"

	"lampac-go/internal/sidecar"
)

// httpConnectDialer tunnels through an HTTP CONNECT proxy.
type httpConnectDialer struct {
	server   string // host:port of the HTTP proxy
	username string
	password string
	useTLS   bool // connect to proxy via TLS (HTTPS proxy)
}

func newHTTPConnectDialer(out sidecar.ProxyOutbound) (*httpConnectDialer, error) {
	if out.Server == "" || out.Port == 0 {
		return nil, fmt.Errorf("http connect: server and port required")
	}
	return &httpConnectDialer{
		server:   fmt.Sprintf("%s:%d", out.Server, out.Port),
		username: out.UUID,
		password: out.Password,
		useTLS:   out.Security == "tls",
	}, nil
}

func (d *httpConnectDialer) Protocol() string { return "http" }
func (d *httpConnectDialer) Close() error     { return nil }

func (d *httpConnectDialer) DialProxy(ctx context.Context, targetAddr string) (net.Conn, error) {
	var conn net.Conn
	var err error

	if d.useTLS {
		conn, err = dialPlainTLS(ctx, d.server, "", false)
	} else {
		conn, err = dialTCP(ctx, d.server)
	}
	if err != nil {
		return nil, fmt.Errorf("http connect: %w", err)
	}

	// Send CONNECT request.
	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n", targetAddr, targetAddr)
	if d.username != "" {
		cred := base64.StdEncoding.EncodeToString([]byte(d.username + ":" + d.password))
		req += "Proxy-Authorization: Basic " + cred + "\r\n"
	}
	req += "\r\n"

	if _, err := conn.Write([]byte(req)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("http connect write: %w", err)
	}

	// Read response.
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("http connect response: %w", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		conn.Close()
		return nil, fmt.Errorf("http connect: status %d %s", resp.StatusCode, resp.Status)
	}

	// If the bufio reader has buffered data, wrap the connection.
	if br.Buffered() > 0 {
		return &bufferedConn{Conn: conn, br: br}, nil
	}

	return conn, nil
}

// bufferedConn wraps a net.Conn with a bufio.Reader that may have buffered data.
type bufferedConn struct {
	net.Conn
	br *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	return c.br.Read(p)
}
