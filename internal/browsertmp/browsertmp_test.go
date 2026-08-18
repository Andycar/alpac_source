package browsertmp

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSweepRemovesStaleOrphansAndSparesFreshOnes(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp) // os.TempDir() honours TMPDIR on unix

	old := time.Now().Add(-StaleAfter - time.Hour)
	mk := func(name string, modTime time.Time, withFile bool) string {
		dir := filepath.Join(tmp, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if withFile {
			if err := os.WriteFile(filepath.Join(dir, "Cookies"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Chtimes(dir, modTime, modTime); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	staleVibix := mk("vibix-chrome-123", old, true)
	staleMirage := mk("mirage-chrome-456", old, false)
	freshVibix := mk("vibix-chrome-789", time.Now(), true) // a live browser's profile
	unrelated := mk("some-other-dir", old, false)

	if got := Sweep(); got != 2 {
		t.Fatalf("Sweep removed %d dirs, want 2", got)
	}
	for _, dir := range []string{staleVibix, staleMirage} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("stale dir %s survived the sweep", filepath.Base(dir))
		}
	}
	for _, dir := range []string{freshVibix, unrelated} {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("%s should have been left alone: %v", filepath.Base(dir), err)
		}
	}
}

func TestRemoveDeletesProfileDir(t *testing.T) {
	dir, err := New("vibix-chrome-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Preferences"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	Remove(dir)

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("profile dir %s not removed", dir)
	}
}

// A directory that keeps being re-created (what a dying Chrome does) must not
// hang Remove forever — it gives up and leaves it for the next sweep.
func TestRemoveGivesUpOnResurrectingDir(t *testing.T) {
	dir, err := New("vibix-chrome-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	// Re-create the directory as fast as possible so every RemoveAll is undone
	// before Remove can confirm it stayed gone — that is exactly what a dying
	// Chrome does to its profile.
	stop := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			select {
			case <-stop:
				return
			default:
				_ = os.MkdirAll(dir, 0o755)
			}
		}
	}()

	start := time.Now()
	done := make(chan struct{})
	go func() { Remove(dir); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Remove did not give up on a resurrecting directory")
	}
	elapsed := time.Since(start)
	close(stop)
	<-stopped

	// It must have actually retried (the backoff schedule sums to ~3.75s) rather
	// than declaring success on the first pass, and still bailed out promptly.
	if elapsed < 3500*time.Millisecond {
		t.Fatalf("Remove returned after %s — it did not retry through the resurrection", elapsed)
	}
	if elapsed > 15*time.Second {
		t.Fatalf("Remove took %s — retry budget is too generous to hold a caller", elapsed)
	}
}
