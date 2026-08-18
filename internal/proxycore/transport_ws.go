package proxycore

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// upgradeWebSocket upgrades an existing TCP/TLS connection to a WebSocket connection.
// This is used by VLESS and Trojan when network=ws.
func upgradeWebSocket(ctx context.Context, conn net.Conn, serverAddr, host string) (net.Conn, error) {
	_ = ctx // reserved for future deadline propagation

	wsDialer := &websocket.Dialer{
		NetDial: func(network, addr string) (net.Conn, error) {
			return conn, nil // reuse the existing connection
		},
		HandshakeTimeout: 10 * time.Second,
	}

	url := "ws://" + serverAddr + "/"
	headers := http.Header{}
	if host != "" {
		headers.Set("Host", host)
	}

	wsConn, _, err := wsDialer.Dial(url, headers)
	if err != nil {
		return nil, fmt.Errorf("websocket upgrade: %w", err)
	}

	return &wsNetConn{ws: wsConn}, nil
}

// wsNetConn adapts a *websocket.Conn to net.Conn for transparent use in relay.
type wsNetConn struct {
	ws     *websocket.Conn
	reader io.Reader
	mu     sync.Mutex
}

func (c *wsNetConn) Read(p []byte) (int, error) {
	for {
		if c.reader != nil {
			n, err := c.reader.Read(p)
			if n > 0 {
				return n, nil
			}
			if err != io.EOF {
				return 0, err
			}
			c.reader = nil
		}

		_, reader, err := c.ws.NextReader()
		if err != nil {
			return 0, err
		}
		c.reader = reader
	}
}

func (c *wsNetConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	err := c.ws.WriteMessage(websocket.BinaryMessage, p)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *wsNetConn) Close() error {
	return c.ws.Close()
}

func (c *wsNetConn) LocalAddr() net.Addr {
	return c.ws.LocalAddr()
}

func (c *wsNetConn) RemoteAddr() net.Addr {
	return c.ws.RemoteAddr()
}

func (c *wsNetConn) SetDeadline(t time.Time) error {
	if err := c.ws.SetReadDeadline(t); err != nil {
		return err
	}
	return c.ws.SetWriteDeadline(t)
}

func (c *wsNetConn) SetReadDeadline(t time.Time) error {
	return c.ws.SetReadDeadline(t)
}

func (c *wsNetConn) SetWriteDeadline(t time.Time) error {
	return c.ws.SetWriteDeadline(t)
}
