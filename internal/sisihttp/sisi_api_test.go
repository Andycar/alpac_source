package sisihttp

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestSisiIndexBuildsChannelsFromInitConf(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "init.conf"), []byte(`{
		"sisi": {"history":{"enable":true}, "lgbt": true},
		"PornHub": {"enable": true, "spider": true},
		"Xvideos": {"enable": true, "spider": true},
		"Xhamster": {"enable": false, "spider": true},
		"PornHubPremium": {"enable": true, "spider": true}
	}`), 0o644); err != nil {
		t.Fatalf("write init.conf: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/sisi", nil)
	rec := httptest.NewRecorder()
	sisiIndexHandler(config.Config{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if strings.TrimSpace(toString(payload["title"])) != "sisi" {
		t.Fatalf("unexpected title: %v", payload["title"])
	}

	rawChannels, ok := payload["channels"].([]any)
	if !ok {
		t.Fatalf("missing channels: %T", payload["channels"])
	}
	gotURLs := make([]string, 0, len(rawChannels))
	for _, item := range rawChannels {
		obj, _ := item.(map[string]any)
		gotURLs = append(gotURLs, toString(obj["playlist_url"]))
	}

	assertHasURL(t, gotURLs, "http://lampac.local/sisi/bookmarks")
	assertHasURL(t, gotURLs, "http://lampac.local/sisi/historys")
	assertHasURL(t, gotURLs, "http://lampac.local/phub")
	assertHasURL(t, gotURLs, "http://lampac.local/xds")
	assertHasURL(t, gotURLs, "http://lampac.local/phubprem")
	assertNoURL(t, gotURLs, "http://lampac.local/phubgay")
	assertNoURL(t, gotURLs, "http://lampac.local/phubsml")
	assertNoURL(t, gotURLs, "http://lampac.local/xdsgay")
	assertNoURL(t, gotURLs, "http://lampac.local/xdssml")
	assertNoURL(t, gotURLs, "http://lampac.local/xmrgay")
	assertNoURL(t, gotURLs, "http://lampac.local/xmrsml")
}

func TestSisiIndexSpiderFilter(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "init.conf"), []byte(`{
		"sisi": {"history":{"enable":false}, "lgbt": false},
		"PornHub": {"enable": true, "spider": false},
		"Xvideos": {"enable": true, "spider": true}
	}`), 0o644); err != nil {
		t.Fatalf("write init.conf: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/sisi?spder=true", nil)
	rec := httptest.NewRecorder()
	sisiIndexHandler(config.Config{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if strings.Contains(body, `"playlist_url":"http://lampac.local/phub"`) {
		t.Fatalf("phub should be filtered out when spider=false")
	}
	if !strings.Contains(body, `"playlist_url":"http://lampac.local/xds"`) {
		t.Fatalf("xds should remain when spider=true")
	}
}

func TestSisiModificationHandler(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "init.conf"), []byte(`{"sisi":{"xdb":false}}`), 0o644); err != nil {
		t.Fatalf("write init.conf: %v", err)
	}

	modPath := filepath.Join(root, "wwwroot", "sisi", "plugins")
	if err := os.MkdirAll(modPath, 0o755); err != nil {
		t.Fatalf("mkdir mod path: %v", err)
	}
	if err := os.WriteFile(filepath.Join(modPath, "modification.js"), []byte(`const host="{localhost}";addId();`), 0o644); err != nil {
		t.Fatalf("write modification.js: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/sisi/plugins/modification.js", nil)
	rec := httptest.NewRecorder()
	sisiModificationHandler(config.Config{
		Compat: config.CompatConfig{RepoRoot: root},
	}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `http://lampac.local/sisi`) {
		t.Fatalf("localhost placeholder not replaced: %q", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "addId();") {
		t.Fatalf("addId() should be removed when xdb=false")
	}
}

func assertHasURL(t *testing.T, urls []string, expected string) {
	t.Helper()
	if slices.Contains(urls, expected) {
		return
	}
	t.Fatalf("expected url %s, got %v", expected, urls)
}

func assertNoURL(t *testing.T, urls []string, unexpected string) {
	t.Helper()
	for _, u := range urls {
		if u == unexpected {
			t.Fatalf("unexpected url %s in %v", unexpected, urls)
		}
	}
}
