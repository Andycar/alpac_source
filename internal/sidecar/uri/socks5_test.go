package uri

import "testing"

func TestParseSOCKS5Plain(t *testing.T) {
	out, err := ParseSOCKS5("socks5://127.0.0.1:9050")
	if err != nil {
		t.Fatalf("ParseSOCKS5: %v", err)
	}
	if out.Protocol != "socks5" {
		t.Errorf("Protocol = %q, want socks5", out.Protocol)
	}
	if out.Server != "127.0.0.1" || out.Port != 9050 {
		t.Errorf("Server:Port = %s:%d, want 127.0.0.1:9050", out.Server, out.Port)
	}
	if out.UUID != "" || out.Password != "" {
		t.Errorf("expected empty userinfo, got UUID=%q Password=%q", out.UUID, out.Password)
	}
}

func TestParseSOCKS5WithAuth(t *testing.T) {
	out, err := ParseSOCKS5("socks5://proxy_user:p%40ssword@example.com:1080")
	if err != nil {
		t.Fatalf("ParseSOCKS5: %v", err)
	}
	if out.UUID != "proxy_user" {
		t.Errorf("UUID (username) = %q, want proxy_user", out.UUID)
	}
	if out.Password != "p@ssword" {
		t.Errorf("Password = %q, want p@ssword (URL-decoded)", out.Password)
	}
	if out.Server != "example.com" || out.Port != 1080 {
		t.Errorf("Server:Port = %s:%d, want example.com:1080", out.Server, out.Port)
	}
}

func TestParseSOCKS5Alias(t *testing.T) {
	out, err := ParseSOCKS5("socks://user:pass@10.0.0.1:1080")
	if err != nil {
		t.Fatalf("ParseSOCKS5 (socks://): %v", err)
	}
	if out.Server != "10.0.0.1" || out.Port != 1080 || out.UUID != "user" || out.Password != "pass" {
		t.Errorf("unexpected outbound: %+v", out)
	}
}

func TestParseSOCKS5MissingHost(t *testing.T) {
	if _, err := ParseSOCKS5("socks5://:1080"); err == nil {
		t.Error("expected error for missing host")
	}
}

func TestParseHTTPProxyPlain(t *testing.T) {
	out, err := ParseHTTPProxy("http://127.0.0.1:8080")
	if err != nil {
		t.Fatalf("ParseHTTPProxy: %v", err)
	}
	if out.Protocol != "http" {
		t.Errorf("Protocol = %q, want http", out.Protocol)
	}
	if out.Server != "127.0.0.1" || out.Port != 8080 {
		t.Errorf("Server:Port = %s:%d", out.Server, out.Port)
	}
	if out.Security != "" {
		t.Errorf("Security = %q, want empty", out.Security)
	}
}

func TestParseHTTPProxyHTTPS(t *testing.T) {
	out, err := ParseHTTPProxy("https://user:secret@proxy.example.com:8443")
	if err != nil {
		t.Fatalf("ParseHTTPProxy: %v", err)
	}
	if out.Security != "tls" {
		t.Errorf("Security = %q, want tls", out.Security)
	}
	if out.UUID != "user" || out.Password != "secret" {
		t.Errorf("auth: UUID=%q Password=%q", out.UUID, out.Password)
	}
	if out.Port != 8443 {
		t.Errorf("Port = %d, want 8443", out.Port)
	}
}

func TestParseHTTPProxyDefaultPort(t *testing.T) {
	out, err := ParseHTTPProxy("http://proxy.example.com")
	if err != nil {
		t.Fatalf("ParseHTTPProxy: %v", err)
	}
	if out.Port != 80 {
		t.Errorf("default Port = %d, want 80", out.Port)
	}

	out, err = ParseHTTPProxy("https://proxy.example.com")
	if err != nil {
		t.Fatalf("ParseHTTPProxy https: %v", err)
	}
	if out.Port != 443 {
		t.Errorf("default https Port = %d, want 443", out.Port)
	}
}

func TestParseDispatchSOCKS5(t *testing.T) {
	out, eng, err := Parse("socks5://127.0.0.1:9050")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if out.Protocol != "socks5" {
		t.Errorf("Protocol = %q", out.Protocol)
	}
	if string(eng) != "proxycore" {
		t.Errorf("EngineType = %q, want proxycore", eng)
	}
}

func TestParseDispatchHTTP(t *testing.T) {
	_, eng, err := Parse("https://127.0.0.1:8443")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if string(eng) != "proxycore" {
		t.Errorf("EngineType = %q, want proxycore", eng)
	}
}
