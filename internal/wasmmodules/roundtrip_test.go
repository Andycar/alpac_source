package wasmmodules

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeProxy is a minimal ProxyBuilder for tests — returns a deterministic
// "enc-{plugin}" stub so we can assert the host's proxy.url() reaches the guest.
type fakeProxy struct{}

func (fakeProxy) EncryptURI(_, _, plugin string, _, _, _ bool) string {
	return "enc-" + plugin
}
func (fakeProxy) EncryptURIWithHeaders(_, _, plugin string, _ map[string]string) string {
	return "enc-" + plugin
}

// TestRoundTripEchoPlugin exercises the full guest↔host loop using the
// TinyGo-compiled wasm_modules/echo/plugin.wasm. Skipped if the .wasm hasn't
// been built yet.
func TestRoundTripEchoPlugin(t *testing.T) {
	wasmPath := filepath.Join("..", "..", "wasm_modules", "echo", "plugin.wasm")
	if _, err := os.Stat(wasmPath); err != nil {
		t.Skipf("plugin.wasm not built (run `make -C wasm_modules/echo build`): %v", err)
	}
	manifestPath := filepath.Join("..", "..", "wasm_modules", "echo")

	// Stand up a fake HTTP target so the plugin's http.GetURL() probe doesn't
	// hit the real network during tests.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test", "ok")
		_, _ = w.Write([]byte(`{"hello":"world"}`))
	}))
	defer srv.Close()

	root := t.TempDir()
	dir := filepath.Join(root, "echo")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Copy manifest + plugin.wasm into a temp dir so the test doesn't depend
	// on the on-disk layout (and won't be polluted by a pre-existing
	// config.json from manual testing).
	for _, name := range []string{"manifest.json", "plugin.wasm"} {
		src, err := os.ReadFile(filepath.Join(manifestPath, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), src, 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	mgr, err := NewManager(root, http.DefaultClient, fakeProxy{}, nil)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	if err := mgr.Scan(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	mod, ok := mgr.Get("echo")
	if !ok {
		t.Fatalf("echo not loaded")
	}
	// Override the probe URL so we hit the test server.
	if err := mod.SetOverride("fetchURL", srv.URL); err != nil {
		t.Fatalf("override: %v", err)
	}
	if err := mod.SetOverride("greet", "ciao from wasm"); err != nil {
		t.Fatalf("override: %v", err)
	}

	inv := Invocation{
		Query:     map[string]string{"id": "42"},
		Headers:   map[string]string{"x-test": "1"},
		Host:      "https://example.com",
		RequestIP: "1.2.3.4",
		Path:      "echo",
		UserAgent: "test-agent",
	}
	resp, err := mgr.Invoke(context.Background(), "echo", inv)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	t.Logf("response body: %s", string(resp.Body))

	var parsed struct {
		Type string `json:"type"`
		Data []struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.Body, &parsed); err != nil {
		t.Fatalf("unmarshal response: %v\nbody: %s", err, string(resp.Body))
	}
	if parsed.Type != "movie" {
		t.Fatalf("expected type=movie, got %q", parsed.Type)
	}
	if len(parsed.Data) != 4 {
		t.Fatalf("expected 4 items, got %d", len(parsed.Data))
	}
	if parsed.Data[0].Name != "ciao from wasm" {
		t.Fatalf("greet override not applied: %q", parsed.Data[0].Name)
	}
	if !strings.Contains(parsed.Data[1].Name, "ip=1.2.3.4") {
		t.Fatalf("requestIP not threaded through: %q", parsed.Data[1].Name)
	}
	if !strings.Contains(parsed.Data[2].Name, "→ 200") {
		t.Fatalf("http probe didn't reach test server: %q", parsed.Data[2].Name)
	}
	// fakeProxy returns "enc-{plugin}" — host_proxy_url should default plugin
	// to module ID ("echo") so the result URL is "/proxy/enc-echo".
	if !strings.Contains(parsed.Data[3].Name, "/proxy/enc-echo") {
		t.Fatalf("proxy.url didn't sign through fakeProxy: %q", parsed.Data[3].Name)
	}

	// Hits counter should advance on a second invocation — proves the cache
	// host imports survive between calls.
	resp2, err := mgr.Invoke(context.Background(), "echo", inv)
	if err != nil {
		t.Fatalf("invoke 2: %v", err)
	}
	if !strings.Contains(string(resp2.Body), "hits=2") {
		t.Fatalf("expected hits=2 on second call, got: %s", string(resp2.Body))
	}
}
