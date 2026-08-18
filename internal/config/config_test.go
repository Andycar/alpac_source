package config

import (
	"path/filepath"
	"testing"
)

func TestLoad_DefaultAccessLogEnabled(t *testing.T) {
	t.Setenv("LAMPAC_GO_REPO_ROOT", t.TempDir())
	t.Setenv("LAMPAC_GO_ACCESS_LOG", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if !cfg.Observability.AccessLog {
		t.Fatalf("expected access log to be enabled by default")
	}
}

func TestLoad_AccessLogCanBeDisabledByEnv(t *testing.T) {
	t.Setenv("LAMPAC_GO_REPO_ROOT", filepath.Join(t.TempDir(), "repo"))
	t.Setenv("LAMPAC_GO_ACCESS_LOG", "false")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Observability.AccessLog {
		t.Fatalf("expected access log to be disabled")
	}
}

func TestHostConfig_StreamHostFor(t *testing.T) {
	hc := HostConfig{StreamAliases: map[string]string{
		"lampa.li":         "s.lampa.li",
		"beta.l-vid.online": "stream.l-vid.online",
	}}

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"matched https", "https://lampa.li", "https://s.lampa.li"},
		{"matched http", "http://lampa.li", "http://s.lampa.li"},
		{"matched with port", "https://lampa.li:8443", "https://s.lampa.li:8443"},
		{"matched second domain", "https://beta.l-vid.online", "https://stream.l-vid.online"},
		{"unmatched", "https://other.example", "https://other.example"},
		{"unmatched with port", "https://other.example:443", "https://other.example:443"},
		{"empty scheme", "lampa.li", "s.lampa.li"},
		{"ipv6 unmatched", "https://[::1]:8443", "https://[::1]:8443"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := hc.StreamHostFor(tc.in)
			if got != tc.want {
				t.Fatalf("StreamHostFor(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	// Empty/nil map: pass-through.
	empty := HostConfig{}
	if got := empty.StreamHostFor("https://lampa.li"); got != "https://lampa.li" {
		t.Fatalf("empty alias map should not rewrite, got %q", got)
	}
}

func TestWebConfigShortPluginRoute(t *testing.T) {
	cases := map[string]string{
		"":     "",
		"   ":  "",
		"/":    "",
		"//":   "",
		"m":    "/m",
		"/m":   "/m",
		" m ":  "/m",
		"/m/":  "/m",
		"m.js": "/m.js",
		"p":    "/p",
	}
	for in, want := range cases {
		if got := (WebConfig{PluginShortPath: in}).ShortPluginRoute(); got != want {
			t.Errorf("ShortPluginRoute(%q) = %q, want %q", in, got, want)
		}
	}
}
