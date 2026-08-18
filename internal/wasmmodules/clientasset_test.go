package wasmmodules

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestClientAssetCatalogAndManifest(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "ratings")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mf := Manifest{
		ID:          "ratings",
		Name:        "Ratings",
		Version:     "1.0.0",
		Target:      TargetClient,
		EntryClient: "client.wasm",
		Tags:        []string{"ui"},
	}
	if err := SaveManifest(dir, mf); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Empty .wasm file is fine for the catalog/manifest endpoints — they
	// don't need to instantiate it.
	if err := os.WriteFile(filepath.Join(dir, "client.wasm"), []byte{0x00, 0x61, 0x73, 0x6d}, 0644); err != nil {
		t.Fatalf("write wasm: %v", err)
	}

	mgr, err := NewManager(root, nil, nil, nil)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	if err := mgr.Scan(); err != nil {
		t.Fatalf("scan: %v", err)
	}

	h := mgr.ClientAssetHandler()

	// Catalog
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/wasm/index.json", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("index status = %d", rec.Code)
	}
	var cat struct {
		Plugins []catalogEntry `json:"plugins"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &cat); err != nil {
		t.Fatalf("catalog parse: %v", err)
	}
	if len(cat.Plugins) != 1 || cat.Plugins[0].ID != "ratings" {
		t.Fatalf("catalog = %+v", cat)
	}
	if cat.Plugins[0].WASMURL != "/wasm/ratings/client.wasm" {
		t.Fatalf("wasm_url = %q", cat.Plugins[0].WASMURL)
	}

	// Manifest
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/wasm/ratings/manifest.json", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("manifest status = %d", rec.Code)
	}

	// Client.wasm
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/wasm/ratings/client.wasm", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("wasm status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/wasm" {
		t.Fatalf("content-type = %q", ct)
	}

	// Unknown plugin → 404
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/wasm/missing/client.wasm", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("expected 404 for missing plugin, got %d", rec.Code)
	}
}
