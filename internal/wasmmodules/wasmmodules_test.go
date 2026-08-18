package wasmmodules

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPackUnpackPtrLen(t *testing.T) {
	cases := []struct{ ptr, length uint32 }{
		{0, 0},
		{1024, 256},
		{0xDEADBEEF, 0x10000},
		{0xFFFFFFFF, 0xFFFFFFFF},
	}
	for _, c := range cases {
		packed := PackPtrLen(c.ptr, c.length)
		ptr, length := UnpackPtrLen(packed)
		if ptr != c.ptr || length != c.length {
			t.Errorf("roundtrip failed: in=(%d,%d) out=(%d,%d)", c.ptr, c.length, ptr, length)
		}
	}
}

func TestManifestValidate(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name    string
		mf      Manifest
		wantErr bool
	}{
		{"empty id", Manifest{}, true},
		{"bad id", Manifest{ID: "Foo"}, true},
		{"server defaults", Manifest{ID: "echo"}, false},
		{"client missing entry", Manifest{ID: "echo", Target: TargetClient}, true},
		{"client ok", Manifest{ID: "echo", Target: TargetClient, EntryClient: "client.wasm"}, false},
		{"both ok", Manifest{ID: "echo", Target: TargetBoth, EntryClient: "c.wasm"}, false},
		{"unknown target", Manifest{ID: "echo", Target: "android"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.mf.Validate()
			if c.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}

	// Round-trip on disk.
	mf := Manifest{ID: "echo", Name: "Echo", Version: "0.1.0", Target: TargetServer}
	if err := SaveManifest(dir, mf); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.ID != "echo" || !got.HasServer() || got.HasClient() {
		t.Fatalf("unexpected manifest: %+v", got)
	}
	if got.EntryServer != "plugin.wasm" {
		t.Fatalf("default entry_server not applied: %q", got.EntryServer)
	}
}

func TestManagerScanIgnoresEmptyDir(t *testing.T) {
	dir := t.TempDir()
	mgr, err := NewManager(dir, nil, nil, nil)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	if err := mgr.Scan(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got := mgr.List(); len(got) != 0 {
		t.Fatalf("expected empty list, got %d entries", len(got))
	}
}

func TestManagerLoadsManifestWithoutWASM(t *testing.T) {
	// A manifest with no plugin.wasm should still load (Module.Error set) so
	// the admin UI can show what went wrong.
	root := t.TempDir()
	mod := filepath.Join(root, "broken")
	if err := os.MkdirAll(mod, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mf := Manifest{ID: "broken", Target: TargetServer}
	if err := SaveManifest(mod, mf); err != nil {
		t.Fatalf("save: %v", err)
	}
	mgr, err := NewManager(root, nil, nil, nil)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	if err := mgr.Scan(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	loaded, ok := mgr.Get("broken")
	if !ok {
		t.Fatalf("module not loaded")
	}
	if loaded.Error == "" {
		t.Fatalf("expected Error to be set")
	}
}

func TestModuleConfigSnapshotMergesDefaultsAndOverrides(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "demo")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mf := Manifest{
		ID:     "demo",
		Target: TargetServer,
		ConfigSchema: []ConfigField{
			{Key: "host", Default: "https://example.com"},
			{Key: "verbose", Default: false},
		},
	}
	if err := SaveManifest(dir, mf); err != nil {
		t.Fatalf("save: %v", err)
	}
	mgr, err := NewManager(root, nil, nil, nil)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	if err := mgr.Scan(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	mod, _ := mgr.Get("demo")
	if mod == nil {
		t.Fatalf("module not loaded")
	}
	if err := mod.SetOverride("verbose", true); err != nil {
		t.Fatalf("override: %v", err)
	}
	var snap map[string]any
	if err := json.Unmarshal(mod.ConfigSnapshot(), &snap); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap["host"] != "https://example.com" {
		t.Fatalf("expected default host, got %v", snap["host"])
	}
	if v, _ := snap["verbose"].(bool); !v {
		t.Fatalf("override not applied: %v", snap["verbose"])
	}
}
