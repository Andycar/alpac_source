package wasmmodules

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestStudioTemplateAllLanguages(t *testing.T) {
	for _, lang := range []string{"tinygo", "rust", "as"} {
		t.Run(lang, func(t *testing.T) {
			tpl := studioTemplate(lang)
			if tpl.Source == "" || tpl.Manifest == "" {
				t.Fatalf("empty template for %s", lang)
			}
			// Manifest must round-trip through json.Unmarshal.
			var raw map[string]any
			if err := json.Unmarshal([]byte(tpl.Manifest), &raw); err != nil {
				t.Fatalf("manifest invalid JSON: %v", err)
			}
			if raw["id"] == nil {
				t.Fatalf("template manifest missing id")
			}
		})
	}
}

func TestStudioHTTPRejectsBadLanguage(t *testing.T) {
	root := t.TempDir()
	mgr, err := NewManager(filepath.Join(root, "modules"), nil, nil, nil)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	studio, err := NewStudio(mgr)
	if err != nil {
		t.Fatalf("studio: %v", err)
	}

	req := httptest.NewRequest("POST", "/admin/api/wasm-studio/build",
		strings.NewReader(`{"id":"foo","language":"cobol","manifest":{},"source":"x"}`))
	rec := httptest.NewRecorder()
	studio.StudioHTTP().ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("expected 400 for unsupported language, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "unsupported") {
		t.Fatalf("expected 'unsupported' in error: %s", rec.Body.String())
	}
}

func TestStudioInstallRequiresBuild(t *testing.T) {
	root := t.TempDir()
	mgr, err := NewManager(filepath.Join(root, "modules"), nil, nil, nil)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	studio, err := NewStudio(mgr)
	if err != nil {
		t.Fatalf("studio: %v", err)
	}
	if err := studio.Install("never_built"); err == nil {
		t.Fatalf("expected install to fail when no build exists")
	}
}
