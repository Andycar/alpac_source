package sisihttp

import (
	stdjson "encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestNextHubListFromYAML(t *testing.T) {
	resetHTTPAPIGlobals(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`
			<div class="item">
			  <a class="title" href="/watch/abc" title="NextHub Title"></a>
			  <img data-src="/img/pic.jpg" />
			  <span class="dur">11:22</span>
			</div>
		`))
	}))
	defer upstream.Close()

	home := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", home)

	yamlBody := fmt.Sprintf(`
enable: true
displayname: TestHub
host: %s
menu:
  sort:
    Новые: list?page={page}
  categories:
    format: category/{cat}?page={page}
    Категория A: cat-a
list:
  uri: list?page={page}
search:
  uri: search?q={search}&page={page}
contentParse:
  nodes: //div[@class='item']
  name:
    node: .//a
    attribute: title
  href:
    node: .//a
    attribute: href
  img:
    node: .//img
    attributes: [data-src, src]
  duration:
    node: .//span[@class='dur']
`, upstream.URL)

	sitesDir := filepath.Join(home, "NextHUB", "sites")
	if err := os.MkdirAll(sitesDir, 0o755); err != nil {
		t.Fatalf("mkdir sites: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sitesDir, "testhub.yaml"), []byte(yamlBody), 0o644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}

	initJSON := `{"sisi":{"NextHUB_sites_enabled":["testhub"]}}`
	if err := os.WriteFile(filepath.Join(home, "init.conf"), []byte(initJSON), 0o644); err != nil {
		t.Fatalf("write init: %v", err)
	}

	source := newNextHubSource(config.Config{})

	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/nexthub?plugin=testhub", nil)
	rec := httptest.NewRecorder()
	source.listHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	list, _ := payload["list"].([]any)
	if len(list) != 1 {
		t.Fatalf("expected 1 list item, got %d, body=%s", len(list), rec.Body.String())
	}
	item, _ := list[0].(map[string]any)
	if !strings.Contains(toString(item["video"]), "/nexthub/vidosik?uri=testhub_-%3A-_") {
		t.Fatalf("unexpected video field: %v", item["video"])
	}
	if toString(item["picture"]) != upstream.URL+"/img/pic.jpg" {
		t.Fatalf("unexpected picture: %v", item["picture"])
	}
	menu, _ := payload["menu"].([]any)
	if len(menu) < 2 {
		t.Fatalf("expected menu with search/sort, got %d", len(menu))
	}
}

func TestNextHubViewRegexMatch(t *testing.T) {
	resetHTTPAPIGlobals(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`q="720p" src="https://cdn.example/720.m3u8" q="480p" src="https://cdn.example/480.m3u8"`))
	}))
	defer upstream.Close()

	home := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", home)

	yamlBody := fmt.Sprintf(`
enable: true
displayname: TestHub
host: %s
list:
  uri: list?page={page}
contentParse:
  nodes: //div[@class='none']
view:
  regexMatch:
    matches: ['720p', '480p']
    pattern: 'q="{value}" src="([^"]+)"'
`, upstream.URL)

	sitesDir := filepath.Join(home, "NextHUB", "sites")
	if err := os.MkdirAll(sitesDir, 0o755); err != nil {
		t.Fatalf("mkdir sites: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sitesDir, "testhub.yaml"), []byte(yamlBody), 0o644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "init.conf"), []byte(`{"sisi":{"NextHUB_sites_enabled":["testhub"]}}`), 0o644); err != nil {
		t.Fatalf("write init: %v", err)
	}

	source := newNextHubSource(config.Config{})

	uriParam := url.QueryEscape("testhub_-:-_" + upstream.URL + "/watch/1")
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/nexthub/vidosik?uri="+uriParam, nil)
	rec := httptest.NewRecorder()
	source.viewHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	qualitys, _ := payload["qualitys"].(map[string]any)
	if toString(qualitys["720p"]) != "https://cdn.example/720.m3u8" {
		t.Fatalf("unexpected 720p link: %v", qualitys["720p"])
	}
}
