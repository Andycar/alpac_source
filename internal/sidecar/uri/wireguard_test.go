package uri

import "testing"

func TestParseWireGuardBasic(t *testing.T) {
	uri := "wg://my-private-key@server.example.com:51820?publickey=peer-pub-key&address=172.16.0.2/32,fd01::2/128&mtu=1400&reserved=1,2,3&presharedkey=psk#tag"

	out, err := ParseWireGuard(uri)
	if err != nil {
		t.Fatalf("ParseWireGuard: %v", err)
	}
	if out.Protocol != "wireguard" {
		t.Errorf("protocol = %q", out.Protocol)
	}
	if out.Server != "server.example.com" {
		t.Errorf("server = %q", out.Server)
	}
	if out.Port != 51820 {
		t.Errorf("port = %d", out.Port)
	}
	if out.PrivateKey != "my-private-key" {
		t.Errorf("private_key = %q", out.PrivateKey)
	}
	if out.PeerPublicKey != "peer-pub-key" {
		t.Errorf("peer_public_key = %q", out.PeerPublicKey)
	}
	if out.PreSharedKey != "psk" {
		t.Errorf("pre_shared_key = %q", out.PreSharedKey)
	}
	if len(out.LocalAddr) != 2 || out.LocalAddr[0] != "172.16.0.2/32" || out.LocalAddr[1] != "fd01::2/128" {
		t.Errorf("local_addr = %v", out.LocalAddr)
	}
	if out.MTU != 1400 {
		t.Errorf("mtu = %d", out.MTU)
	}
	if len(out.Reserved) != 3 || out.Reserved[0] != 1 || out.Reserved[1] != 2 || out.Reserved[2] != 3 {
		t.Errorf("reserved = %v", out.Reserved)
	}
}

func TestParseWireGuardDefaults(t *testing.T) {
	uri := "wg://key@host.net"
	out, err := ParseWireGuard(uri)
	if err != nil {
		t.Fatalf("ParseWireGuard: %v", err)
	}
	if out.Port != 51820 {
		t.Errorf("port = %d (want 51820 default)", out.Port)
	}
	if out.MTU != 1280 {
		t.Errorf("mtu = %d (want 1280 default)", out.MTU)
	}
}

func TestParseWireGuardWARP(t *testing.T) {
	uri := "wg://warp"
	out, err := ParseWireGuard(uri)
	if err != nil {
		t.Fatalf("ParseWireGuard: %v", err)
	}
	if out.Protocol != "wireguard" {
		t.Errorf("protocol = %q", out.Protocol)
	}
	if out.Server != "warp" {
		t.Errorf("server = %q (should be sentinel 'warp')", out.Server)
	}
}

func TestParseWireGuardMissingHost(t *testing.T) {
	_, err := ParseWireGuard("wg://")
	if err == nil {
		t.Fatal("expected error for missing host")
	}
}
