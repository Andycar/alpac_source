package wasmmodules

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatcherReloadsOnManifestChange(t *testing.T) {
	root := t.TempDir()
	mgr, err := NewManager(root, nil, nil, nil)
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	if err := mgr.Scan(); err != nil {
		t.Fatalf("initial scan: %v", err)
	}
	w, err := NewWatcher(mgr)
	if err != nil {
		t.Fatalf("watcher: %v", err)
	}
	defer w.Close()

	// Create a new module dir + manifest. fsnotify on macOS sometimes batches
	// CREATE events 100-200 ms after mkdir/write — give it a generous window.
	dir := filepath.Join(root, "demo")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mf := Manifest{ID: "demo", Target: TargetServer}
	data, _ := json.MarshalIndent(mf, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	// Wait up to 2 s for the debounced reload to land.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := mgr.Get("demo"); ok {
			return // success
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("watcher did not pick up new module within deadline")
}
