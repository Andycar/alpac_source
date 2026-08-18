package uri

import (
	"encoding/base64"
	"testing"
)

func TestParseVMessBase64(t *testing.T) {
	// Build base64 payload
	jsonPayload := `{"v":"2","ps":"test","add":"server.example.com","port":"443","id":"a3482e88-686a-4a58-8126-99c9df64b060","aid":"0","scy":"auto","net":"ws","type":"none","host":"cdn.example.com","path":"/ws","tls":"tls","sni":"sni.example.com","alpn":"h2,http/1.1","fp":"chrome"}`
	encoded := base64.StdEncoding.EncodeToString([]byte(jsonPayload))
	uri := "vmess://" + encoded

	out, err := ParseVMess(uri)
	if err != nil {
		t.Fatalf("ParseVMess: %v", err)
	}
	if out.Protocol != "vmess" {
		t.Errorf("protocol = %q, want vmess", out.Protocol)
	}
	if out.Server != "server.example.com" {
		t.Errorf("server = %q, want server.example.com", out.Server)
	}
	if out.Port != 443 {
		t.Errorf("port = %d, want 443", out.Port)
	}
	if out.UUID != "a3482e88-686a-4a58-8126-99c9df64b060" {
		t.Errorf("uuid = %q", out.UUID)
	}
	if out.AlterID != 0 {
		t.Errorf("alterID = %d, want 0", out.AlterID)
	}
	if out.Network != "ws" {
		t.Errorf("network = %q, want ws", out.Network)
	}
	if out.Security != "tls" {
		t.Errorf("security = %q, want tls", out.Security)
	}
	if out.SNI != "sni.example.com" {
		t.Errorf("sni = %q", out.SNI)
	}
	if out.Host != "cdn.example.com" {
		t.Errorf("host = %q", out.Host)
	}
	if out.Path != "/ws" {
		t.Errorf("path = %q", out.Path)
	}
	if out.Fingerprint != "chrome" {
		t.Errorf("fp = %q", out.Fingerprint)
	}
}

func TestParseVMessBase64NumericPort(t *testing.T) {
	// Port as number in JSON
	jsonPayload := `{"add":"1.2.3.4","port":8443,"id":"test-uuid","net":"tcp","tls":"none"}`
	encoded := base64.RawStdEncoding.EncodeToString([]byte(jsonPayload))
	uri := "vmess://" + encoded

	out, err := ParseVMess(uri)
	if err != nil {
		t.Fatalf("ParseVMess: %v", err)
	}
	if out.Port != 8443 {
		t.Errorf("port = %d, want 8443", out.Port)
	}
	if out.Security != "none" {
		t.Errorf("security = %q, want none", out.Security)
	}
}

func TestParseVMessURLFormat(t *testing.T) {
	uri := "vmess://a3482e88-686a-4a58-8126-99c9df64b060@server.example.com:443?type=ws&security=tls&sni=sni.example.com&path=/ws&host=cdn.example.com#tag"

	out, err := ParseVMess(uri)
	if err != nil {
		t.Fatalf("ParseVMess: %v", err)
	}
	if out.Protocol != "vmess" {
		t.Errorf("protocol = %q", out.Protocol)
	}
	if out.Server != "server.example.com" {
		t.Errorf("server = %q", out.Server)
	}
	if out.UUID != "a3482e88-686a-4a58-8126-99c9df64b060" {
		t.Errorf("uuid = %q", out.UUID)
	}
	if out.Network != "ws" {
		t.Errorf("network = %q", out.Network)
	}
	if out.SNI != "sni.example.com" {
		t.Errorf("sni = %q", out.SNI)
	}
}

func TestParseVMessMissingServer(t *testing.T) {
	jsonPayload := `{"id":"test-uuid","port":"443"}`
	encoded := base64.StdEncoding.EncodeToString([]byte(jsonPayload))
	uri := "vmess://" + encoded

	_, err := ParseVMess(uri)
	if err == nil {
		t.Fatal("expected error for missing server")
	}
}
