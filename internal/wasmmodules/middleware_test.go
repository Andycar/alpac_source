package wasmmodules

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCallUpstreamsCapturesBodyAndStatus(t *testing.T) {
	// Build a fake resolver returning two upstream handlers, each producing
	// a deterministic body. captureUpstream should rewrite the path so each
	// handler sees /lite/{name}.
	mgr := &Manager{
		upstreams: func(name string) (http.Handler, bool) {
			switch name {
			case "alpha":
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/lite/alpha" {
						t.Errorf("alpha got path %q, want /lite/alpha", r.URL.Path)
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"data":["alpha-item"]}`))
				}), true
			case "beta":
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(503)
					_, _ = w.Write([]byte(`{"error":"upstream down"}`))
				}), true
			}
			return nil, false
		},
	}
	r := httptest.NewRequest("GET", "/lite/middleware?id=42", nil)
	results := mgr.callUpstreams(r, []string{"alpha", "beta", "missing"})

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	if results[0].Balancer != "alpha" || string(results[0].Body) != `{"data":["alpha-item"]}` {
		t.Fatalf("alpha body = %s", string(results[0].Body))
	}
	if results[1].Balancer != "beta" || results[1].Status != 503 {
		t.Fatalf("beta status = %d", results[1].Status)
	}
	if results[2].Balancer != "missing" || results[2].Error == "" {
		t.Fatalf("missing should produce an error: %+v", results[2])
	}
}

// TestMiddlewareEndToEnd loads the quality_filter plugin, fakes an upstream
// emitting a mix of 480p / 720p / 1080p items, and verifies the middleware
// drops anything below 720p (the default).
func TestMiddlewareEndToEnd(t *testing.T) {
	wasmPath := filepath.Join("..", "..", "wasm_modules", "quality_filter", "plugin.wasm")
	if _, err := os.Stat(wasmPath); err != nil {
		t.Skipf("quality_filter plugin.wasm not built: %v", err)
	}
	manifestPath := filepath.Join("..", "..", "wasm_modules", "quality_filter")

	root := t.TempDir()
	dir := filepath.Join(root, "quality_filter")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, name := range []string{"manifest.json", "plugin.wasm"} {
		src, err := os.ReadFile(filepath.Join(manifestPath, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), src, 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	mgr, err := NewManager(root, http.DefaultClient, nil, nil)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	if err := mgr.Scan(); err != nil {
		t.Fatalf("scan: %v", err)
	}

	// Wire a single fake upstream named "echo" that returns a list of items
	// across multiple qualities. The plugin should keep only those >= 720p.
	mgr.SetUpstreamResolver(func(name string) (http.Handler, bool) {
		if name != "echo" {
			return nil, false
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{
                "type":"movie",
                "data":[
                    {"name":"too low","quality":{"480p":"a"}},
                    {"name":"keep me","quality":{"1080p":"b","720p":"c"}},
                    {"name":"max only","maxquality":1080}
                ]
            }`))
		}), true
	})

	// The middleware lives at /lite/quality_filter; trigger it via the
	// MiddlewareHandler exactly as the server would.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/lite/quality_filter", nil)
	mgr.MiddlewareHandler("quality_filter").ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	t.Logf("middleware response: %s", rec.Body.String())

	var resp struct {
		Type string `json:"type"`
		Data []struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Type != "movie" {
		t.Fatalf("expected type=movie, got %q", resp.Type)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("expected 2 items after filtering, got %d", len(resp.Data))
	}
	for _, item := range resp.Data {
		if strings.Contains(strings.ToLower(item.Name), "too low") {
			t.Fatalf("low-quality item leaked through: %v", item)
		}
	}
}
