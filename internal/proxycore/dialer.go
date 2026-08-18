// Package proxycore implements a built-in proxy engine with multiple outbound
// protocols (VLESS, Trojan, Shadowsocks, Hysteria2, SOCKS5, HTTP CONNECT, VMess, WireGuard).
// Each proxy entry runs an in-process SOCKS5 listener that tunnels traffic
// through the configured protocol, replacing external sidecar binaries.
package proxycore

import (
	"context"
	"fmt"
	"net"

	"lampac-go/internal/sidecar"
)

// ProtocolDialer establishes tunneled connections through a proxy server.
type ProtocolDialer interface {
	// DialProxy connects to the proxy server and tunnels to targetAddr ("host:port").
	DialProxy(ctx context.Context, targetAddr string) (net.Conn, error)

	// Close releases persistent resources (QUIC sessions, TUN devices, etc.).
	Close() error

	// Protocol returns the protocol name ("vless", "trojan", "ss", etc.).
	Protocol() string
}

// NewDialer creates a ProtocolDialer from a parsed ProxyOutbound config.
func NewDialer(out sidecar.ProxyOutbound) (ProtocolDialer, error) {
	switch out.Protocol {
	case "trojan":
		return newTrojanDialer(out)
	case "socks5":
		return newSOCKS5OutDialer(out)
	case "http":
		return newHTTPConnectDialer(out)
	case "vless":
		return newVLESSDialer(out)
	case "ss":
		return newShadowsocksDialer(out)
	case "hysteria2":
		return newHysteria2Dialer(out)
	case "vmess":
		return newVMessDialer(out)
	case "wireguard":
		return newWireGuardDialer(out)
	default:
		return nil, fmt.Errorf("proxycore: unsupported protocol %q", out.Protocol)
	}
}

// parseTarget splits "host:port" into components for protocol headers.
func parseTarget(addr string) (host string, port uint16, err error) {
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, fmt.Errorf("invalid target address %q: %w", addr, err)
	}
	portNum, err := net.LookupPort("tcp", p)
	if err != nil {
		return "", 0, fmt.Errorf("invalid port in %q: %w", addr, err)
	}
	return h, uint16(portNum), nil
}
