package uri

import (
	"encoding/base64"
	"testing"
)

func TestParseShadowsocksSIP002(t *testing.T) {
	// SIP002: ss://BASE64(method:password)@host:port
	userinfo := base64.StdEncoding.EncodeToString([]byte("aes-256-gcm:my-secret"))
	uri := "ss://" + userinfo + "@server.example.com:8388#tag"

	out, err := ParseShadowsocks(uri)
	if err != nil {
		t.Fatalf("ParseShadowsocks: %v", err)
	}
	if out.Protocol != "ss" {
		t.Errorf("protocol = %q", out.Protocol)
	}
	if out.Server != "server.example.com" {
		t.Errorf("server = %q", out.Server)
	}
	if out.Port != 8388 {
		t.Errorf("port = %d", out.Port)
	}
	if out.Encryption != "aes-256-gcm" {
		t.Errorf("cipher = %q", out.Encryption)
	}
	if out.Password != "my-secret" {
		t.Errorf("password = %q", out.Password)
	}
}

func TestParseShadowsocksPlain(t *testing.T) {
	// Plain method:password without base64
	uri := "ss://chacha20-ietf-poly1305:pass123@1.2.3.4:443"
	out, err := ParseShadowsocks(uri)
	if err != nil {
		t.Fatalf("ParseShadowsocks: %v", err)
	}
	if out.Encryption != "chacha20-ietf-poly1305" {
		t.Errorf("cipher = %q", out.Encryption)
	}
	if out.Password != "pass123" {
		t.Errorf("password = %q", out.Password)
	}
	if out.Port != 443 {
		t.Errorf("port = %d", out.Port)
	}
}

func TestParseShadowsocksLegacy(t *testing.T) {
	// Legacy: ss://BASE64(method:password@host:port)
	payload := base64.StdEncoding.EncodeToString([]byte("aes-128-gcm:test@10.0.0.1:1234"))
	uri := "ss://" + payload + "#MyServer"
	out, err := ParseShadowsocks(uri)
	if err != nil {
		t.Fatalf("ParseShadowsocks: %v", err)
	}
	if out.Server != "10.0.0.1" {
		t.Errorf("server = %q", out.Server)
	}
	if out.Port != 1234 {
		t.Errorf("port = %d", out.Port)
	}
	if out.Encryption != "aes-128-gcm" {
		t.Errorf("cipher = %q", out.Encryption)
	}
}

func TestParseShadowsocksMissingPassword(t *testing.T) {
	uri := "ss://aes-256-gcm:@host:8388"
	_, err := ParseShadowsocks(uri)
	if err == nil {
		t.Fatal("expected error for empty password")
	}
}
