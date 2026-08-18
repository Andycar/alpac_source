package httpapi

import (
	"os"
	"path/filepath"
	"testing"

	"lampac-go/internal/config"
	"lampac-go/internal/modules"
)

// TestLiveManifest verifies the disk-backed manifest used by /lampainit.js
// and /on.js: the admin-written file (runtime dir, LAMPAC_GO_HOME) wins over
// the boot fallback, and a missing file falls back to the boot manifest.
func TestLiveManifest(t *testing.T) {
	resetHTTPAPIGlobals(t)

	home := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", home)
	cfg := config.Config{Compat: config.CompatConfig{RepoRoot: t.TempDir()}}
	fallback := []modules.RootModule{{Dll: "Fallback.dll", Enable: true}}

	// No file anywhere — fallback wins.
	if got := liveManifest(cfg, fallback); len(got) != 1 || got[0].Dll != "Fallback.dll" {
		t.Fatalf("expected fallback manifest, got %+v", got)
	}

	// Admin writes the manifest into the runtime dir (the same path
	// admin_manifest.go resolves via relToRuntime). Bust the 60s TTL entry
	// cached by the miss above — in production this is the ≤60s window
	// before an admin edit becomes visible.
	if err := os.MkdirAll(filepath.Join(home, "module"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `[{"enable":true,"dll":"TorrServer.dll"},{"enable":false,"dll":"Online.dll"}]`
	if err := os.WriteFile(filepath.Join(home, "module", "manifest.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	liveManifestCache.Lock()
	liveManifestCache.key = ""
	liveManifestCache.Unlock()

	got := liveManifest(cfg, fallback)
	if len(got) != 2 || got[0].Dll != "TorrServer.dll" || !got[0].Enable || got[1].Enable {
		t.Fatalf("expected on-disk manifest, got %+v", got)
	}

	// And the gate helper agrees with what the admin wrote.
	if !hasEnabledModule(got, "torrserver.dll") {
		t.Fatalf("TorrServer.dll should be enabled")
	}
	if hasEnabledModule(got, "online.dll") {
		t.Fatalf("Online.dll should be disabled")
	}
	if hasEnabledModule(got, "jacred.dll") {
		t.Fatalf("absent dll in non-empty manifest should read as disabled")
	}
}
