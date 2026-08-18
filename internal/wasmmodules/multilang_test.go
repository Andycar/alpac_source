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

// TestMultiLangRoundTrip exercises every "echo_*" sample plugin under
// wasm_modules/, proving that plugins compiled from any language hit the
// same ABI. The test gracefully skips plugins whose .wasm hasn't been built.
//
// All echo_* plugins share the same expected response shape:
//   {"type":"movie","data":[{name:greet}, {name:"hits=N ip=X"},
//                            {name:"probe URL → STATUS"},
//                            {name:"proxied=URL"}]}
func TestMultiLangRoundTrip(t *testing.T) {
	cases := []struct {
		dir, lang string
	}{
		{"echo", "tinygo"},
		{"echo_as", "assemblyscript"},
		{"echo_c", "c"},
		{"echo_zig", "zig"},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	for _, c := range cases {
		c := c
		t.Run(c.lang, func(t *testing.T) {
			pluginDir := filepath.Join("..", "..", "wasm_modules", c.dir)
			if _, err := os.Stat(filepath.Join(pluginDir, "plugin.wasm")); err != nil {
				t.Skipf("%s/plugin.wasm not built: %v", c.dir, err)
			}

			root := t.TempDir()
			dest := filepath.Join(root, c.dir)
			if err := os.MkdirAll(dest, 0755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			for _, name := range []string{"manifest.json", "plugin.wasm"} {
				src, err := os.ReadFile(filepath.Join(pluginDir, name))
				if err != nil {
					t.Fatalf("read %s: %v", name, err)
				}
				if err := os.WriteFile(filepath.Join(dest, name), src, 0644); err != nil {
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

			inv := Invocation{
				Query:     map[string]string{"fetchURL": srv.URL},
				Headers:   map[string]string{},
				Host:      "https://example.com",
				RequestIP: "9.8.7.6",
				Path:      c.dir,
				UserAgent: "test",
			}
			resp, err := mgr.Invoke(context.Background(), c.dir, inv)
			if err != nil {
				t.Fatalf("invoke: %v", err)
			}
			t.Logf("%s response: %s", c.lang, string(resp.Body))

			var parsed struct {
				Type string `json:"type"`
				Data []struct {
					Name string `json:"name"`
				} `json:"data"`
			}
			if err := json.Unmarshal(resp.Body, &parsed); err != nil {
				t.Fatalf("unmarshal: %v\nbody: %s", err, string(resp.Body))
			}
			if parsed.Type != "movie" {
				t.Fatalf("type = %q, want movie", parsed.Type)
			}
			if len(parsed.Data) != 4 {
				t.Fatalf("data has %d items, want 4", len(parsed.Data))
			}
			if !strings.Contains(parsed.Data[1].Name, "ip=9.8.7.6") {
				t.Fatalf("requestIP not threaded: %q", parsed.Data[1].Name)
			}
			if !strings.Contains(parsed.Data[2].Name, "→ 200") {
				t.Fatalf("HTTP probe failed: %q", parsed.Data[2].Name)
			}
			if !strings.Contains(parsed.Data[3].Name, "/proxy/enc-"+c.dir) {
				t.Fatalf("proxy.url didn't sign properly: %q", parsed.Data[3].Name)
			}

			// Second invoke — hits should advance, proving the cache survives.
			resp2, err := mgr.Invoke(context.Background(), c.dir, inv)
			if err != nil {
				t.Fatalf("invoke 2: %v", err)
			}
			if !strings.Contains(string(resp2.Body), "hits=2") {
				t.Fatalf("expected hits=2 on second call, got: %s", string(resp2.Body))
			}
		})
	}
}
