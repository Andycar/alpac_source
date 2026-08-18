package uri

import (
	"testing"
)

func TestParseTrojanBasic(t *testing.T) {
	uri := "trojan://my-password@server.example.com:443?security=tls&sni=sni.example.com&type=ws&path=/ws&host=cdn.example.com#tag"

	out, err := ParseTrojan(uri)
	if err != nil {
		t.Fatalf("ParseTrojan: %v", err)
	}
	if out.Protocol != "trojan" {
		t.Errorf("protocol = %q, want trojan", out.Protocol)
	}
	if out.Server != "server.example.com" {
		t.Errorf("server = %q", out.Server)
	}
	if out.Port != 443 {
		t.Errorf("port = %d, want 443", out.Port)
	}
	if out.Password != "my-password" {
		t.Errorf("password = %q", out.Password)
	}
	if out.Security != "tls" {
		t.Errorf("security = %q", out.Security)
	}
	if out.SNI != "sni.example.com" {
		t.Errorf("sni = %q", out.SNI)
	}
	if out.Network != "ws" {
		t.Errorf("network = %q", out.Network)
	}
	if out.Path != "/ws" {
		t.Errorf("path = %q", out.Path)
	}
	if out.Host != "cdn.example.com" {
		t.Errorf("host = %q", out.Host)
	}
}

func TestParseTrojanDefaults(t *testing.T) {
	// No security, no sni, no type specified
	uri := "trojan://pass123@myhost.net:8443"

	out, err := ParseTrojan(uri)
	if err != nil {
		t.Fatalf("ParseTrojan: %v", err)
	}
	if out.Security != "tls" {
		t.Errorf("security = %q, want tls (default)", out.Security)
	}
	if out.SNI != "myhost.net" {
		t.Errorf("sni = %q, want myhost.net (default to host)", out.SNI)
	}
	if out.Network != "tcp" {
		t.Errorf("network = %q, want tcp (default)", out.Network)
	}
	if out.Port != 8443 {
		t.Errorf("port = %d, want 8443", out.Port)
	}
}

func TestParseTrojanNoPort(t *testing.T) {
	uri := "trojan://pass123@myhost.net"
	out, err := ParseTrojan(uri)
	if err != nil {
		t.Fatalf("ParseTrojan: %v", err)
	}
	if out.Port != 443 {
		t.Errorf("port = %d, want 443 (default)", out.Port)
	}
}

func TestParseTrojanGRPC(t *testing.T) {
	uri := "trojan://pass@host.com:443?type=grpc&path=my-service&security=tls&sni=grpc.host.com&fp=chrome#tag"
	out, err := ParseTrojan(uri)
	if err != nil {
		t.Fatalf("ParseTrojan: %v", err)
	}
	if out.Network != "grpc" {
		t.Errorf("network = %q, want grpc", out.Network)
	}
	if out.Path != "my-service" {
		t.Errorf("path = %q, want my-service", out.Path)
	}
	if out.Fingerprint != "chrome" {
		t.Errorf("fp = %q", out.Fingerprint)
	}
}

func TestParseTrojanMissingPassword(t *testing.T) {
	_, err := ParseTrojan("trojan://@host.com:443")
	if err == nil {
		t.Fatal("expected error for missing password")
	}
}
