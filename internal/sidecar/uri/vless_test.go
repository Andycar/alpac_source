package uri

import (
	"testing"

	"lampac-go/internal/sidecar"
)

func TestParseVLESS(t *testing.T) {
	tests := []struct {
		name    string
		uri     string
		wantErr bool
		check   func(t *testing.T, out sidecar.ProxyOutbound)
	}{
		{
			name: "basic TLS",
			uri:  "vless://uuid-123@example.com:443?encryption=none&security=tls&sni=example.com&type=tcp#tag",
			check: func(t *testing.T, out sidecar.ProxyOutbound) {
				if out.Protocol != "vless" {
					t.Errorf("protocol = %q", out.Protocol)
				}
				if out.UUID != "uuid-123" {
					t.Errorf("uuid = %q", out.UUID)
				}
				if out.Server != "example.com" {
					t.Errorf("server = %q", out.Server)
				}
				if out.Port != 443 {
					t.Errorf("port = %d", out.Port)
				}
				if out.Security != "tls" {
					t.Errorf("security = %q", out.Security)
				}
				if out.SNI != "example.com" {
					t.Errorf("sni = %q", out.SNI)
				}
				if out.Network != "tcp" {
					t.Errorf("network = %q", out.Network)
				}
			},
		},
		{
			name: "websocket with path",
			uri:  "vless://abc@host.com:8443?type=ws&path=/ws&host=cdn.com&security=tls&sni=cdn.com",
			check: func(t *testing.T, out sidecar.ProxyOutbound) {
				if out.Network != "ws" {
					t.Errorf("network = %q", out.Network)
				}
				if out.Path != "/ws" {
					t.Errorf("path = %q", out.Path)
				}
				if out.Host != "cdn.com" {
					t.Errorf("host = %q", out.Host)
				}
			},
		},
		{
			name: "reality",
			uri:  "vless://uuid@host:443?security=reality&pbk=pubkey123&sid=ab&fp=chrome&sni=www.google.com",
			check: func(t *testing.T, out sidecar.ProxyOutbound) {
				if out.Security != "reality" {
					t.Errorf("security = %q", out.Security)
				}
				if out.PublicKey != "pubkey123" {
					t.Errorf("pubkey = %q", out.PublicKey)
				}
				if out.ShortID != "ab" {
					t.Errorf("shortid = %q", out.ShortID)
				}
				if out.Fingerprint != "chrome" {
					t.Errorf("fp = %q", out.Fingerprint)
				}
			},
		},
		{
			name: "default port and encryption",
			uri:  "vless://uuid@host",
			check: func(t *testing.T, out sidecar.ProxyOutbound) {
				if out.Port != 443 {
					t.Errorf("port = %d", out.Port)
				}
				if out.Encryption != "none" {
					t.Errorf("encryption = %q", out.Encryption)
				}
				if out.Network != "tcp" {
					t.Errorf("network = %q", out.Network)
				}
			},
		},
		{
			name:    "missing UUID",
			uri:     "vless://@host:443",
			wantErr: true,
		},
		{
			name:    "wrong scheme",
			uri:     "vmess://uuid@host:443",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := ParseVLESS(tt.uri)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, out)
			}
		})
	}
}
