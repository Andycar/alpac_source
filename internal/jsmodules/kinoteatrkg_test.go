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

type ktFixtureClient struct{ t *testing.T }

func (c ktFixtureClient) Do(req *http.Request) (*http.Response, error) {
	u := req.URL.String()
	var file string
	switch {
	case strings.Contains(u, "/search"):
		file = "search.html"
	case strings.Contains(u, "/site/view"):
		file = "view.html"
	default:
		c.t.Fatalf("ktFixtureClient: unexpected URL %s", u)
	}
	data, err := os.ReadFile(filepath.Join("testdata", "kinoteatrkg", file))
	if err != nil {
		c.t.Fatalf("ktFixtureClient: read %s: %v", file, err)
	}
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(string(data))),
		Header:     http.Header{"Content-Type": []string{"text/html"}},
		Request:    req,
	}, nil
}

func loadKinoteatrkg(t *testing.T) *Module {
	t.Helper()
	mod, err := LoadModule(filepath.Join("..", "..", "modules", "kinoteatrkg"))
	if err != nil {
		t.Fatalf("LoadModule: %v", err)
	}
	if mod.Error != "" {
		t.Fatalf("kinoteatrkg failed to compile in goja (strict): %s", mod.Error)
	}
	return mod
}

func invokeKt(t *testing.T, mod *Module, q map[string]string, checksearch bool) map[string]any {
	t.Helper()
	rt, err := NewRuntime(mod, ktFixtureClient{t}, nil, nil, nil)
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
		t.Fatalf("unmarshal %q: %v", resp.Body, err)
	}
	return out
}

func TestKinoteatrkgCompiles(t *testing.T) { loadKinoteatrkg(t) }

func TestKinoteatrkgMovie(t *testing.T) {
	mod := loadKinoteatrkg(t)

	cs := invokeKt(t, mod, map[string]string{"title": "Чужой 3", "original_title": "Alien 3", "year": "1992"}, true)
	if cs["rch"] != true {
		t.Fatalf("checksearch rch != true: %v", cs)
	}

	res := invokeKt(t, mod, map[string]string{"title": "Чужой 3", "original_title": "Alien 3", "year": "1992"}, false)
	if res["type"] != "movie" {
		t.Fatalf("type = %v, want movie", res["type"])
	}
	data, _ := res["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("want 1 movie item, got %d", len(data))
	}
	first, _ := data[0].(map[string]any)
	url, _ := first["url"].(string)
	if !strings.Contains(url, "kinoteatr") || !strings.Contains(url, ".mp4") {
		t.Fatalf("movie URL not a kinoteatr mp4: %q", url)
	}
	if strings.Contains(url, " ") {
		t.Fatalf("URL has unescaped space: %q", url)
	}
}

func TestKinoteatrkgMatchByEnglish(t *testing.T) {
	mod := loadKinoteatrkg(t)
	// Russian title differs slightly; English original_title must still match a card.
	res := invokeKt(t, mod, map[string]string{"title": "", "original_title": "Alien 3", "year": "1992"}, false)
	data, _ := res["data"].([]any)
	if len(data) == 0 {
		t.Fatalf("no match by English title")
	}
}

func TestKinoteatrkgNoMatch(t *testing.T) {
	mod := loadKinoteatrkg(t)
	cs := invokeKt(t, mod, map[string]string{"title": "несуществующий zzqq"}, true)
	if cs["rch"] != false {
		t.Fatalf("rch != false for no-match: %v", cs)
	}
}
