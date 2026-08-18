package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lampac-go/internal/config"

	"github.com/go-chi/chi/v5"
)

func TestLoadPluginTemplatePrefersMyOverride(t *testing.T) {
	root := t.TempDir()
	pluginsDir := filepath.Join(root, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatalf("mkdir plugins: %v", err)
	}

	if err := os.WriteFile(filepath.Join(pluginsDir, "dlna.js"), []byte("BASE"), 0o644); err != nil {
		t.Fatalf("write dlna.js: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginsDir, "dlna.my.js"), []byte("OVERRIDE"), 0o644); err != nil {
		t.Fatalf("write dlna.my.js: %v", err)
	}

	cfg := config.Config{Compat: config.CompatConfig{RepoRoot: root}}
	got, err := loadPluginTemplate("dlna.js", cfg)
	if err != nil {
		t.Fatalf("loadPluginTemplate failed: %v", err)
	}
	if got != "OVERRIDE" {
		t.Fatalf("expected .my override, got: %q", got)
	}
}

func TestSyncJSHandlerInjectsSyncInvcAndSupportsLite(t *testing.T) {
	root := t.TempDir()
	pluginsDir := filepath.Join(root, "plugins")
	if err := os.MkdirAll(filepath.Join(pluginsDir, "sync_v2"), 0o755); err != nil {
		t.Fatalf("mkdir sync_v2: %v", err)
	}

	if err := os.WriteFile(filepath.Join(pluginsDir, "sync_v2", "sync.js"), []byte("sync-v2::{sync-invc}::{localhost}::{token}"), 0o644); err != nil {
		t.Fatalf("write sync_v2/sync.js: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginsDir, "sync_lite.js"), []byte("sync-lite::{sync-invc}::{localhost}::{token}"), 0o644); err != nil {
		t.Fatalf("write sync_lite.js: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginsDir, "sync-invc.js"), []byte("SYNC_INVC"), 0o644); err != nil {
		t.Fatalf("write sync-invc.js: %v", err)
	}

	cfg := config.Config{Compat: config.CompatConfig{RepoRoot: root}}
	handler := syncJSHandler(cfg)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/sync.js", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "sync-v2::SYNC_INVC::http://lampac.local::") {
		t.Fatalf("sync-v2 placeholders not replaced correctly: %s", body)
	}
	if strings.Contains(body, "{sync-invc}") {
		t.Fatalf("sync-invc placeholder must be replaced: %s", body)
	}

	recLite := httptest.NewRecorder()
	reqLite := httptest.NewRequest(http.MethodGet, "http://lampac.local/sync.js?lite=1", nil)
	handler.ServeHTTP(recLite, reqLite)
	if recLite.Code != http.StatusOK {
		t.Fatalf("unexpected lite status: %d body=%s", recLite.Code, recLite.Body.String())
	}
	if !strings.Contains(recLite.Body.String(), "sync-lite::SYNC_INVC::http://lampac.local::") {
		t.Fatalf("sync_lite template not selected: %s", recLite.Body.String())
	}
}

func TestSyncJSHandlerFallbackSnippetWhenSyncInvcMissing(t *testing.T) {
	root := t.TempDir()
	pluginsDir := filepath.Join(root, "plugins")
	if err := os.MkdirAll(filepath.Join(pluginsDir, "sync_v2"), 0o755); err != nil {
		t.Fatalf("mkdir sync_v2: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginsDir, "sync_v2", "sync.js"), []byte("x {sync-invc} y"), 0o644); err != nil {
		t.Fatalf("write sync_v2/sync.js: %v", err)
	}

	cfg := config.Config{Compat: config.CompatConfig{RepoRoot: root}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/sync.js", nil)
	syncJSHandler(cfg).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "{sync-invc}") {
		t.Fatalf("fallback must replace sync-invc placeholder: %s", body)
	}
	if !strings.Contains(body, "var sync_invc = window.sync_invc || {};") {
		t.Fatalf("fallback sync_invc snippet missing: %s", body)
	}
}

func TestOnlineJSHandlerReplacesRuntimePlaceholders(t *testing.T) {
	root := t.TempDir()
	pluginsDir := filepath.Join(root, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatalf("mkdir plugins: %v", err)
	}

	template := strings.Join([]string{
		"host={localhost}",
		"tok={token}",
		"{rch_websoket}",
	}, "\n")

	if err := os.WriteFile(filepath.Join(pluginsDir, "online.js"), []byte(template), 0o644); err != nil {
		t.Fatalf("write online.js: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginsDir, "rch_nws.js"), []byte("RCH {invc-rch_nws} @ {localhost} ? {token}"), 0o644); err != nil {
		t.Fatalf("write rch_nws.js: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginsDir, "invc-rch_nws.js"), []byte("INVC_RCH_NWS"), 0o644); err != nil {
		t.Fatalf("write invc-rch_nws.js: %v", err)
	}

	cfg := config.Config{
		Compat:    config.CompatConfig{RepoRoot: root},
		WebSocket: config.WebSocketConfig{Type: "nws"},
	}

	router := chi.NewRouter()
	router.Get("/online/js/{token}", onlineJSHandler(cfg, nil, nil))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/online/js/a+b", nil)
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	contains := func(part string) {
		if !strings.Contains(body, part) {
			t.Fatalf("expected body to contain %q, body=%s", part, body)
		}
	}
	notContains := func(part string) {
		if strings.Contains(body, part) {
			t.Fatalf("expected body to not contain %q, body=%s", part, body)
		}
	}

	contains("host=http://lampac.local")
	contains("tok=a%2Bb")
	contains("RCH INVC_RCH_NWS @ http://lampac.local ? a%2Bb")
	notContains("{rch_websoket}")
}

func TestSisiJSHandlerReplacesRuntimePlaceholders(t *testing.T) {
	root := t.TempDir()
	pluginsDir := filepath.Join(root, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatalf("mkdir plugins: %v", err)
	}

	template := strings.Join([]string{
		"use_api: 'lampac'",
		"Defined.use_api == 'pwa'",
		"'<div>p</div>'",
		"Lampa.Search.addSource(Search);",
		"{rch_websoket}",
		"push={push_all}",
		"hist={historySave}",
		"host={localhost}",
		"tok={token}",
		"window.rchtype",
	}, "\n")

	if err := os.WriteFile(filepath.Join(pluginsDir, "sisi.js"), []byte(template), 0o644); err != nil {
		t.Fatalf("write sisi.js: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginsDir, "rch_nws.js"), []byte("RCH {invc-rch_nws} @ {localhost}"), 0o644); err != nil {
		t.Fatalf("write rch_nws.js: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginsDir, "invc-rch.js"), []byte("INVC_RCH"), 0o644); err != nil {
		t.Fatalf("write invc-rch.js: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginsDir, "invc-rch_nws.js"), []byte("INVC_RCH_NWS"), 0o644); err != nil {
		t.Fatalf("write invc-rch_nws.js: %v", err)
	}

	cfg := config.Config{
		Compat:    config.CompatConfig{RepoRoot: root},
		WebSocket: config.WebSocketConfig{Type: "nws"},
		Sisi: config.SisiConfig{
			Spider:             false,
			Component:          "my_sisi",
			IconName:           "x",
			PushAll:            false,
			HistoryEnable:      true,
			ForcedCheckRchType: true,
		},
	}

	router := chi.NewRouter()
	router.Get("/sisi/js/{token}", sisiJSHandler(cfg))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/sisi/js/a+b", nil)
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	contains := func(part string) {
		if !strings.Contains(body, part) {
			t.Fatalf("expected body to contain %q, body=%s", part, body)
		}
	}
	notContains := func(part string) {
		if strings.Contains(body, part) {
			t.Fatalf("expected body to not contain %q, body=%s", part, body)
		}
	}

	contains("use_api: 'my_sisi'")
	contains("'<div>x</div>'")
	contains("RCH INVC_RCH_NWS @ http://lampac.local")
	contains("push=false")
	contains("hist=true")
	contains("host=http://lampac.local")
	contains("tok=a%2Bb")
	contains("Defined.rchtype")

	notContains("{rch_websoket}")
	notContains("Lampa.Search.addSource(Search);")
	notContains("window.rchtype")
}

func TestSisiJSHandlerLiteMode(t *testing.T) {
	root := t.TempDir()
	pluginsDir := filepath.Join(root, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatalf("mkdir plugins: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginsDir, "sisi.lite.js"), []byte("lite::{localhost}"), 0o644); err != nil {
		t.Fatalf("write sisi.lite.js: %v", err)
	}

	cfg := config.Config{
		Compat:    config.CompatConfig{RepoRoot: root},
		WebSocket: config.WebSocketConfig{Type: "nws"},
		Sisi:      config.SisiConfig{Spider: true, Component: "sisi", PushAll: true, HistoryEnable: true},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/sisi.js?lite=1", nil)
	sisiJSHandler(cfg).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "lite::http://lampac.local" {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}
