package updater

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureDiskSpace(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "lampac-go")
	if err := os.WriteFile(exe, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	orig := diskFree
	t.Cleanup(func() { diskFree = orig })

	// Ample space → no error.
	diskFree = func(string) (uint64, bool) { return 1 << 30, true }
	if err := ensureDiskSpace(exe, 50<<20); err != nil {
		t.Fatalf("ample space: unexpected error %v", err)
	}

	// Free space unknown (e.g. Windows) → check is skipped even for a huge want.
	diskFree = func(string) (uint64, bool) { return 0, false }
	if err := ensureDiskSpace(exe, 1<<40); err != nil {
		t.Fatalf("unknown free space: expected skip, got %v", err)
	}

	// Tight space, no rollback backup to reclaim → error.
	diskFree = func(string) (uint64, bool) { return 10 << 20, true }
	if err := ensureDiskSpace(exe, 50<<20); err == nil {
		t.Fatal("tight space: expected error, got nil")
	}

	// Tight space, but reclaiming a stale .old tips it over the threshold.
	// need = 50 MiB + 32 MiB margin = 82 MiB; free 30 MiB + 60 MiB backup = 90 MiB.
	backup := exe + ".old"
	if err := os.WriteFile(backup, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(backup, 60<<20); err != nil { // sparse, no real disk use
		t.Fatal(err)
	}
	diskFree = func(string) (uint64, bool) { return 30 << 20, true }
	if err := ensureDiskSpace(exe, 50<<20); err != nil {
		t.Fatalf("reclaim path: expected success, got %v", err)
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatal("reclaim path: expected stale .old to be removed")
	}
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   uint64
		want string
	}{
		{512, "512 B"},
		{2 << 10, "2 KiB"},
		{98 << 20, "98 MiB"},
	}
	for _, c := range cases {
		if got := humanBytes(c.in); got != c.want {
			t.Errorf("humanBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}
