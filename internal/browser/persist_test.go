package browser

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPersisted_LoadEmptyReturnsZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "browser_engine.json")

	eng, overrides, err := LoadPersisted(path)
	if err != nil {
		t.Fatalf("LoadPersisted on missing file: %v", err)
	}
	if eng != "" {
		t.Errorf("engine = %q, want empty", eng)
	}
	if overrides != nil {
		t.Errorf("overrides = %v, want nil", overrides)
	}
}

func TestPersisted_SaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "browser_engine.json")

	want := map[string]string{"mirage": "rod", "kinobase": "playwright"}
	if err := SavePersisted(path, "rod", want); err != nil {
		t.Fatalf("SavePersisted: %v", err)
	}

	eng, got, err := LoadPersisted(path)
	if err != nil {
		t.Fatalf("LoadPersisted: %v", err)
	}
	if eng != "rod" {
		t.Errorf("engine = %q, want rod", eng)
	}
	if len(got) != len(want) {
		t.Errorf("overrides len = %d, want %d", len(got), len(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("overrides[%q] = %q, want %q", k, got[k], v)
		}
	}
}

func TestPersisted_AtomicNoLeftoverTmp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "browser_engine.json")
	if err := SavePersisted(path, "rod", nil); err != nil {
		t.Fatalf("SavePersisted: %v", err)
	}
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Fatal(".tmp file left behind after successful save")
	}
}
