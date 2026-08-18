package jsmodules

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureClient serves captured turboserial.com responses by URL substring so
// the awmzone module can be exercised offline (and deterministically) through
// the real goja runtime — including goja's own atob/btoa used by the PlayerJS
// "#2" deobfuscation.
type fixtureClient struct{ t *testing.T }

func (c fixtureClient) Do(req *http.Request) (*http.Response, error) {
	u := req.URL.String()
	var file string
	switch {
	case strings.Contains(u, "/mary/spotlight"):
		file = "search.json"
	case strings.Contains(u, "/watch/7294"):
		file = "watch.html"
	case strings.Contains(u, "/watch/571"):
		file = "watch_serial.html"
	case strings.Contains(u, "/embed-players/30648"):
		file = "embed.html"
	case strings.Contains(u, "/embed-players/17544"):
		file = "embed_serial.html"
	default:
		c.t.Fatalf("fixtureClient: unexpected URL %s", u)
	}
	data, err := os.ReadFile(filepath.Join("testdata", "awmzone", file))
	if err != nil {
		c.t.Fatalf("fixtureClient: read %s: %v", file, err)
	}
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(string(data))),
		Header:     http.Header{"Content-Type": []string{"text/html"}},
		Request:    req,
	}, nil
}

func loadAwmzone(t *testing.T) *Module {
	t.Helper()
	mod, err := LoadModule(filepath.Join("..", "..", "modules", "awmzone"))
	if err != nil {
		t.Fatalf("LoadModule: %v", err)
	}
	if mod.Error != "" {
		t.Fatalf("awmzone failed to compile in goja (strict): %s", mod.Error)
	}
	return mod
}

func invokeAwmzone(t *testing.T, mod *Module, q map[string]string, checksearch bool) map[string]any {
	t.Helper()
	rt, err := NewRuntime(mod, fixtureClient{t}, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	defer rt.Close()
	resp, err := rt.Invoke(context.Background(), Invocation{
		Query: q, Host: "http://127.0.0.1:9118", Checksearch: checksearch,
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		t.Fatalf("unmarshal response %q: %v", resp.Body, err)
	}
	return out
}

func TestAwmzoneCompiles(t *testing.T) { loadAwmzone(t) }

func TestAwmzoneMovie(t *testing.T) {
	mod := loadAwmzone(t)

	cs := invokeAwmzone(t, mod, map[string]string{"title": "Чужой", "original_title": "Alien"}, true)
	if cs["rch"] != true {
		t.Fatalf("checksearch rch != true: %v", cs)
	}

	res := invokeAwmzone(t, mod, map[string]string{"title": "Чужой", "original_title": "Alien"}, false)
	if res["type"] != "movie" {
		t.Fatalf("type = %v, want movie", res["type"])
	}
	data, _ := res["data"].([]any)
	if len(data) == 0 {
		t.Fatalf("no movie data items")
	}
	first, _ := data[0].(map[string]any)
	url, _ := first["url"].(string)
	if !strings.Contains(url, "awmzone") || !strings.Contains(url, ".m3u8") {
		t.Fatalf("first movie URL not an awmzone m3u8: %q", url)
	}
}

func TestAwmzoneSerial(t *testing.T) {
	mod := loadAwmzone(t)

	res := invokeAwmzone(t, mod, map[string]string{"title": "Чужой дед", "serial": "1"}, false)
	if res["type"] != "episode" && res["type"] != "season" {
		t.Fatalf("serial type = %v, want episode/season", res["type"])
	}
	data, _ := res["data"].([]any)
	if len(data) == 0 {
		t.Fatalf("no serial data items")
	}
	first, _ := data[0].(map[string]any)
	url, _ := first["url"].(string)
	if !strings.Contains(url, "awmzone") || !strings.Contains(url, ".m3u8") {
		t.Fatalf("first episode URL not an awmzone m3u8: %q", url)
	}
}

func TestAwmzoneNoMatch(t *testing.T) {
	mod := loadAwmzone(t)
	cs := invokeAwmzone(t, mod, map[string]string{"title": "несуществующий фильм zzqq"}, true)
	if cs["rch"] != false {
		t.Fatalf("checksearch rch != false for no-match: %v", cs)
	}
}
