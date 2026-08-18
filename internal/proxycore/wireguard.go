package proxycore

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"

	"lampac-go/internal/sidecar"
)

// wgDialer implements WireGuard tunneling via a userspace network stack.
// The WireGuard device is created lazily on the first DialProxy call and
// reused across all subsequent connections (same lifecycle as Hysteria2 QUIC session).
type wgDialer struct {
	server        string // "host:port" of the WireGuard endpoint
	privateKey    string // base64-encoded X25519 private key
	peerPublicKey string // base64-encoded X25519 peer public key
	preSharedKey  string // optional base64 pre-shared key
	localAddr     []string
	mtu           int
	reserved      []int // optional WARP reserved bytes (3-byte UDP header field)

	once sync.Once
	tnet *netstack.Net
	dev  *device.Device
	err  error
}

func newWireGuardDialer(out sidecar.ProxyOutbound) (*wgDialer, error) {
	if out.Server == "" || out.Port == 0 {
		return nil, fmt.Errorf("wireguard: server and port required")
	}
	if out.PrivateKey == "" || out.PeerPublicKey == "" {
		return nil, fmt.Errorf("wireguard: private_key and peer_public_key required")
	}
	return &wgDialer{
		server:        fmt.Sprintf("%s:%d", out.Server, out.Port),
		privateKey:    out.PrivateKey,
		peerPublicKey: out.PeerPublicKey,
		preSharedKey:  out.PreSharedKey,
		localAddr:     out.LocalAddr,
		mtu:           out.MTU,
		reserved:      out.Reserved,
	}, nil
}

func (d *wgDialer) Protocol() string { return "wireguard" }

func (d *wgDialer) Close() error {
	d.once.Do(func() {}) // prevent re-init if Close races with first DialProxy
	if d.dev != nil {
		d.dev.Close()
		d.dev = nil
	}
	return nil
}

func (d *wgDialer) DialProxy(ctx context.Context, targetAddr string) (net.Conn, error) {
	d.once.Do(func() {
		d.tnet, d.dev, d.err = d.setupDevice()
	})
	if d.err != nil {
		return nil, d.err
	}
	return d.tnet.DialContext(ctx, "tcp", targetAddr)
}

// setupDevice creates and configures the userspace WireGuard device.
func (d *wgDialer) setupDevice() (*netstack.Net, *device.Device, error) {
	mtu := d.mtu
	if mtu <= 0 {
		mtu = 1280
	}

	// Parse local tunnel addresses (e.g. "172.16.0.2/32", "fd01::2/128").
	var localAddrs []netip.Addr
	for _, addr := range d.localAddr {
		host := strings.SplitN(addr, "/", 2)[0]
		ip, err := netip.ParseAddr(host)
		if err != nil {
			return nil, nil, fmt.Errorf("wireguard: invalid local address %q: %w", addr, err)
		}
		localAddrs = append(localAddrs, ip)
	}
	if len(localAddrs) == 0 {
		localAddrs = []netip.Addr{netip.MustParseAddr("172.16.0.2")}
	}

	dns := []netip.Addr{
		netip.MustParseAddr("1.1.1.1"),
		netip.MustParseAddr("8.8.8.8"),
	}

	tdev, tnet, err := netstack.CreateNetTUN(localAddrs, dns, mtu)
	if err != nil {
		return nil, nil, fmt.Errorf("wireguard: create netstack TUN: %w", err)
	}

	// If WARP reserved bytes are configured, wrap the default bind to
	// inject them on send and zero them on receive — this lets us speak
	// to Cloudflare WARP endpoints without a sidecar.
	var bind conn.Bind = conn.NewDefaultBind()
	if hasReserved(d.reserved) {
		bind = newWARPBind(bind, d.reserved)
	}

	dev := device.NewDevice(tdev, bind, device.NewLogger(device.LogLevelError, "wg: "))

	// Convert base64 keys to lowercase hex for the WireGuard UAPI protocol.
	privHex, err := b64ToHex(d.privateKey)
	if err != nil {
		dev.Close()
		return nil, nil, fmt.Errorf("wireguard: invalid private key: %w", err)
	}
	pubHex, err := b64ToHex(d.peerPublicKey)
	if err != nil {
		dev.Close()
		return nil, nil, fmt.Errorf("wireguard: invalid peer public key: %w", err)
	}

	// Build UAPI (WireGuard kernel interface) configuration.
	// Device section first, then peer section.
	var cfg strings.Builder
	cfg.WriteString("private_key=")
	cfg.WriteString(privHex)
	cfg.WriteByte('\n')
	cfg.WriteString("public_key=")
	cfg.WriteString(pubHex)
	cfg.WriteByte('\n')
	cfg.WriteString("allowed_ip=0.0.0.0/0\n")
	cfg.WriteString("allowed_ip=::/0\n")
	cfg.WriteString("endpoint=")
	cfg.WriteString(d.server)
	cfg.WriteByte('\n')

	if d.preSharedKey != "" {
		pskHex, err := b64ToHex(d.preSharedKey)
		if err == nil {
			cfg.WriteString("preshared_key=")
			cfg.WriteString(pskHex)
			cfg.WriteByte('\n')
		}
	}

	if err := dev.IpcSet(cfg.String()); err != nil {
		dev.Close()
		return nil, nil, fmt.Errorf("wireguard: IPC config: %w", err)
	}

	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, nil, fmt.Errorf("wireguard: device up: %w", err)
	}

	return tnet, dev, nil
}

// hasReserved reports whether the reserved-bytes slice contains any non-zero
// value. WARP-issued reserved bytes are always non-zero; an all-zero slice
// indicates standard WireGuard, in which case we skip the bind wrapper.
func hasReserved(r []int) bool {
	for _, b := range r {
		if b != 0 {
			return true
		}
	}
	return false
}

// b64ToHex converts a base64-encoded WireGuard key (32 bytes) to lowercase hex.
// The WireGuard UAPI protocol expects hex keys, but configs/URIs store them as base64.
func b64ToHex(b64key string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(b64key)
	if err != nil {
		// Try URL-safe base64.
		data, err = base64.RawURLEncoding.DecodeString(b64key)
		if err != nil {
			return "", fmt.Errorf("base64 decode: %w", err)
		}
	}
	if len(data) != 32 {
		return "", fmt.Errorf("expected 32-byte key, got %d bytes", len(data))
	}
	return hex.EncodeToString(data), nil
}
