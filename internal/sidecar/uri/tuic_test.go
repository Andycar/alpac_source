package uri

import "testing"

func TestParseTUICBasic(t *testing.T) {
	uri := "tuic://a3482e88-686a-4a58-8126-99c9df64b060:my-pass@server.example.com:443?sni=sni.example.com&congestion_control=cubic&udp_relay_mode=quic#tag"

	out, err := ParseTUIC(uri)
	if err != nil {
		t.Fatalf("ParseTUIC: %v", err)
	}
	if out.Protocol != "tuic" {
		t.Errorf("protocol = %q", out.Protocol)
	}
	if out.Server != "server.example.com" {
		t.Errorf("server = %q", out.Server)
	}
	if out.Port != 443 {
		t.Errorf("port = %d", out.Port)
	}
	if out.UUID != "a3482e88-686a-4a58-8126-99c9df64b060" {
		t.Errorf("uuid = %q", out.UUID)
	}
	if out.Password != "my-pass" {
		t.Errorf("password = %q", out.Password)
	}
	if out.SNI != "sni.example.com" {
		t.Errorf("sni = %q", out.SNI)
	}
	if out.CongestionCtrl != "cubic" {
		t.Errorf("congestion = %q", out.CongestionCtrl)
	}
	if out.UDPRelayMode != "quic" {
		t.Errorf("relay = %q", out.UDPRelayMode)
	}
}

func TestParseTUICDefaults(t *testing.T) {
	uri := "tuic://uuid123:pass@host.net:443"
	out, err := ParseTUIC(uri)
	if err != nil {
		t.Fatalf("ParseTUIC: %v", err)
	}
	if out.CongestionCtrl != "bbr" {
		t.Errorf("congestion = %q, want bbr (default)", out.CongestionCtrl)
	}
	if out.UDPRelayMode != "native" {
		t.Errorf("relay = %q, want native (default)", out.UDPRelayMode)
	}
	if out.SNI != "host.net" {
		t.Errorf("sni = %q (should default to host)", out.SNI)
	}
}

func TestParseTUICMissingUUID(t *testing.T) {
	_, err := ParseTUIC("tuic://:pass@host.com:443")
	if err == nil {
		t.Fatal("expected error for missing UUID")
	}
}
