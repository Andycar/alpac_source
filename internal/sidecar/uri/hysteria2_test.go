package uri

import "testing"

func TestParseHysteria2Basic(t *testing.T) {
	uri := "hysteria2://my-pass@server.example.com:443?sni=sni.example.com&obfs=salamander&obfs-password=obfs-pass#tag"

	out, err := ParseHysteria2(uri)
	if err != nil {
		t.Fatalf("ParseHysteria2: %v", err)
	}
	if out.Protocol != "hysteria2" {
		t.Errorf("protocol = %q", out.Protocol)
	}
	if out.Server != "server.example.com" {
		t.Errorf("server = %q", out.Server)
	}
	if out.Port != 443 {
		t.Errorf("port = %d", out.Port)
	}
	if out.Password != "my-pass" {
		t.Errorf("password = %q", out.Password)
	}
	if out.SNI != "sni.example.com" {
		t.Errorf("sni = %q", out.SNI)
	}
	if out.Obfs != "salamander" {
		t.Errorf("obfs = %q", out.Obfs)
	}
	if out.ObfsPassword != "obfs-pass" {
		t.Errorf("obfs-password = %q", out.ObfsPassword)
	}
}

func TestParseHysteria2Hy2Scheme(t *testing.T) {
	uri := "hy2://pass@1.2.3.4:8443"
	out, err := ParseHysteria2(uri)
	if err != nil {
		t.Fatalf("ParseHysteria2: %v", err)
	}
	if out.Port != 8443 {
		t.Errorf("port = %d", out.Port)
	}
	if out.SNI != "1.2.3.4" {
		t.Errorf("sni = %q (should default to host)", out.SNI)
	}
}

func TestParseHysteria2WithBandwidth(t *testing.T) {
	uri := "hysteria2://pass@host.com:443?up=100&down=200"
	out, err := ParseHysteria2(uri)
	if err != nil {
		t.Fatalf("ParseHysteria2: %v", err)
	}
	if out.UpMbps != 100 {
		t.Errorf("up = %d", out.UpMbps)
	}
	if out.DownMbps != 200 {
		t.Errorf("down = %d", out.DownMbps)
	}
}

func TestParseHysteria2MissingPassword(t *testing.T) {
	_, err := ParseHysteria2("hysteria2://@host.com:443")
	if err == nil {
		t.Fatal("expected error for missing password")
	}
}
