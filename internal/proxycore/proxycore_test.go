package proxycore

import (
	"context"
	"net"
	"testing"
	"time"

	"lampac-go/internal/sidecar"
)

// TestParseUUID validates UUID parsing.
func TestParseUUID(t *testing.T) {
	tests := []struct {
		input string
		want  [16]byte
		err   bool
	}{
		{"550e8400-e29b-41d4-a716-446655440000", [16]byte{0x55, 0x0e, 0x84, 0x00, 0xe2, 0x9b, 0x41, 0xd4, 0xa7, 0x16, 0x44, 0x66, 0x55, 0x44, 0x00, 0x00}, false},
		{"550e8400e29b41d4a716446655440000", [16]byte{0x55, 0x0e, 0x84, 0x00, 0xe2, 0x9b, 0x41, 0xd4, 0xa7, 0x16, 0x44, 0x66, 0x55, 0x44, 0x00, 0x00}, false},
		{"too-short", [16]byte{}, true},
		{"", [16]byte{}, true},
	}
	for _, tt := range tests {
		got, err := parseUUID(tt.input)
		if tt.err {
			if err == nil {
				t.Errorf("parseUUID(%q) expected error", tt.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseUUID(%q) unexpected error: %v", tt.input, err)
			continue
		}
		if got != tt.want {
			t.Errorf("parseUUID(%q) = %x, want %x", tt.input, got, tt.want)
		}
	}
}

// TestTrojanPasswordHash validates Trojan password hashing.
func TestTrojanPasswordHash(t *testing.T) {
	d, err := newTrojanDialer(sidecar.ProxyOutbound{
		Protocol: "trojan",
		Server:   "example.com",
		Port:     443,
		Password: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	// SHA-224 of "test" is well-known.
	if len(d.passwordHex) != 56 {
		t.Errorf("password hex length = %d, want 56", len(d.passwordHex))
	}
}

// TestAppendSOCKSAddr validates SOCKS5 address encoding.
func TestAppendSOCKSAddr(t *testing.T) {
	// Domain name.
	buf := appendSOCKSAddr(nil, "example.com", 443)
	if buf[0] != 0x03 {
		t.Errorf("ATYP = %d, want 3 (domain)", buf[0])
	}
	if buf[1] != 11 { // len("example.com")
		t.Errorf("domain len = %d, want 11", buf[1])
	}

	// IPv4.
	buf = appendSOCKSAddr(nil, "1.2.3.4", 80)
	if buf[0] != 0x01 {
		t.Errorf("ATYP = %d, want 1 (IPv4)", buf[0])
	}

	// IPv6.
	buf = appendSOCKSAddr(nil, "::1", 8080)
	if buf[0] != 0x04 {
		t.Errorf("ATYP = %d, want 4 (IPv6)", buf[0])
	}
}

// TestVLESSAddon validates VLESS addon encoding.
func TestVLESSAddon(t *testing.T) {
	// Empty flow → nil.
	if got := encodeVLESSAddon(""); got != nil {
		t.Errorf("empty flow should return nil, got %v", got)
	}

	// Non-empty flow → protobuf field 1.
	flow := "xtls-rprx-vision"
	addon := encodeVLESSAddon(flow)
	if addon[0] != 0x0a {
		t.Errorf("addon tag = %x, want 0x0a", addon[0])
	}
	if int(addon[1]) != len(flow) {
		t.Errorf("addon length = %d, want %d", addon[1], len(flow))
	}
}

// TestSSKeySize validates cipher key sizes.
func TestSSKeySize(t *testing.T) {
	if ssKeySize("aes-128-gcm") != 16 {
		t.Error("aes-128-gcm should be 16")
	}
	if ssKeySize("aes-256-gcm") != 32 {
		t.Error("aes-256-gcm should be 32")
	}
	if ssKeySize("chacha20-ietf-poly1305") != 32 {
		t.Error("chacha20 should be 32")
	}
	if ssKeySize("unknown") != 0 {
		t.Error("unknown should be 0")
	}
}

// TestSOCKS5ServerStartStop validates SOCKS5 server lifecycle.
func TestSOCKS5ServerStartStop(t *testing.T) {
	// Create a dummy dialer that always fails (we just test the SOCKS5 server itself).
	d := &dummyDialer{}
	srv := newSOCKS5Server(d)

	err := srv.start("127.0.0.1:0") // random port
	if err != nil {
		t.Fatal(err)
	}
	defer srv.stop()

	addr := srv.addr()
	if addr == "" {
		t.Fatal("server addr is empty")
	}

	// Check alive.
	if !srv.alive() {
		t.Error("server should be alive")
	}

	// Connect and send a minimal SOCKS5 handshake.
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Method negotiation: version=5, 1 method, no-auth.
	conn.Write([]byte{0x05, 0x01, 0x00})
	var reply [2]byte
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := conn.Read(reply[:])
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || reply[0] != 0x05 || reply[1] != 0x00 {
		t.Errorf("unexpected method reply: %v", reply[:n])
	}

	// Stop and verify.
	srv.stop()
	if srv.alive() {
		t.Error("server should be dead after stop")
	}
}

// dummyDialer is a ProtocolDialer that always returns an error.
type dummyDialer struct{}

func (d *dummyDialer) DialProxy(ctx context.Context, addr string) (net.Conn, error) {
	return nil, context.DeadlineExceeded
}
func (d *dummyDialer) Close() error     { return nil }
func (d *dummyDialer) Protocol() string { return "dummy" }

// TestNewDialerProtocols validates that NewDialer creates the right dialer types.
func TestNewDialerProtocols(t *testing.T) {
	tests := []struct {
		protocol string
		want     string
	}{
		{"trojan", "trojan"},
		{"socks5", "socks5"},
		{"http", "http"},
		{"vless", "vless"},
		{"ss", "ss"},
		{"vmess", "vmess"},
	}

	for _, tt := range tests {
		out := sidecar.ProxyOutbound{
			Protocol: tt.protocol,
			Server:   "example.com",
			Port:     443,
			Password: "test",
			UUID:     "550e8400-e29b-41d4-a716-446655440000",
			Encryption: "aes-256-gcm",
		}
		d, err := NewDialer(out)
		if err != nil {
			t.Errorf("NewDialer(%s): %v", tt.protocol, err)
			continue
		}
		if d.Protocol() != tt.want {
			t.Errorf("NewDialer(%s).Protocol() = %s, want %s", tt.protocol, d.Protocol(), tt.want)
		}
		d.Close()
	}
}
