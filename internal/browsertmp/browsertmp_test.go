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

// Tests never touch the machine's real snap Chromium /tmp; the ones that need
// a snap root point SnapChromiumTmp at their own directory.
func TestMain(m *testing.M) {
	SnapChromiumTmp = filepath.Join(os.TempDir(), "browsertmp-test-no-snap")
	os.Exit(m.Run())
}

// withSnap makes host play /tmp and snap play snap Chromium's private /tmp.
func withSnap(t *testing.T, host, snap string) {
	t.Helper()
	for _, d := range []string{host, snap} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	prevHost, prevSnap, prevLive := snapHostTmp, SnapChromiumTmp, liveProfiles
	snapHostTmp, SnapChromiumTmp = host, snap
	t.Cleanup(func() { snapHostTmp, SnapChromiumTmp, liveProfiles = prevHost, prevSnap, prevLive })
}

func mkProfile(t *testing.T, root, name string, modTime time.Time) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, "Default"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Default", "Cookies"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dir, modTime, modTime); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Snap Chromium writes the profile we created as /tmp/vibix-chrome-N into its
// private /tmp. Deleting only the host path frees nothing — that is how FI
// filled its disk on 2026-09-26.
func TestRemoveAlsoDeletesSnapMirror(t *testing.T) {
	base := t.TempDir()
	host, snap := filepath.Join(base, "host"), filepath.Join(base, "snap")
	withSnap(t, host, snap)

	dir := mkProfile(t, host, "vibix-chrome-1", time.Now())
	mirror := mkProfile(t, snap, "vibix-chrome-1", time.Now())
	other := mkProfile(t, snap, "vibix-chrome-2", time.Now())

	Remove(dir)

	for _, d := range []string{dir, mirror} {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("%s survived Remove", d)
		}
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("another browser's profile was touched: %v", err)
	}
}

func TestSnapMirrorOnlyUnderHostTmp(t *testing.T) {
	base := t.TempDir()
	host, snap := filepath.Join(base, "host"), filepath.Join(base, "snap")
	withSnap(t, host, snap)

	cases := map[string]string{
		filepath.Join(host, "vibix-chrome-1"):   filepath.Join(snap, "vibix-chrome-1"),
		filepath.Join(host, "a", "b"):           filepath.Join(snap, "a", "b"),
		host:                                    "",
		filepath.Join(base, "elsewhere", "x"):   "",
		filepath.Join(host, "..", "host2", "x"): "",
	}
	for in, want := range cases {
		if got := snapMirror(in); got != want {
			t.Errorf("snapMirror(%q) = %q, want %q", in, got, want)
		}
	}

	SnapChromiumTmp = filepath.Join(base, "no-snap")
	if got := snapMirror(filepath.Join(host, "vibix-chrome-1")); got != "" {
		t.Errorf("no snap Chromium, but snapMirror = %q", got)
	}
	if roots := Roots(); len(roots) != 1 {
		t.Errorf("no snap Chromium, but Roots = %v", roots)
	}
}

// Sweep reaches snap Chromium's private /tmp, and never takes the profile a
// running browser names as its --user-data-dir, however old its mtime.
func TestSweepCoversSnapTmpAndSparesLiveProfiles(t *testing.T) {
	base := t.TempDir()
	host, snap := filepath.Join(base, "host"), filepath.Join(base, "snap")
	t.Setenv("TMPDIR", host)
	withSnap(t, host, snap)
	liveProfiles = func() map[string]struct{} { return map[string]struct{}{"vibix-chrome-live": {}} }

	old := time.Now().Add(-StaleAfter - time.Hour)
	staleSnap := mkProfile(t, snap, "vibix-chrome-1", old)
	staleHost := mkProfile(t, host, "lampac-chromedp-3", old)
	live := mkProfile(t, snap, "vibix-chrome-live", old)
	fresh := mkProfile(t, snap, "vibix-chrome-2", time.Now())
	persistent := mkProfile(t, snap, "rezka-chrome", old)

	if got := Sweep(); got != 2 {
		t.Fatalf("Sweep removed %d dirs, want 2", got)
	}
	for _, d := range []string{staleSnap, staleHost} {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("stale %s survived the sweep", d)
		}
	}
	for _, d := range []string{live, fresh, persistent} {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("%s should have been left alone: %v", filepath.Base(d), err)
		}
	}
}

func TestUserDataDirs(t *testing.T) {
	cases := []struct {
		cmdline string
		want    []string
	}{
		{"/snap/chromium/3530/usr/lib/chromium-browser/chrome\x00--headless\x00--user-data-dir=/tmp/vibix-chrome-1\x00--no-sandbox\x00", []string{"/tmp/vibix-chrome-1"}},
		// Chrome's child processes rewrite their title into one space-joined string.
		{"/snap/chromium/3530/usr/lib/chromium-browser/chrome --type=renderer --user-data-dir=/tmp/rezka-chrome --lang=en-US\x00", []string{"/tmp/rezka-chrome"}},
		{"chrome\x00--user-data-dir\x00/tmp/p\x00", []string{"/tmp/p"}},
		{"chrome\x00--user-data-dir=\x00", nil},
		{"bash\x00-c\x00sleep 1\x00", nil},
	}
	for _, c := range cases {
		got := userDataDirs([]byte(c.cmdline))
		if len(got) != len(c.want) {
			t.Errorf("userDataDirs(%q) = %v, want %v", c.cmdline, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("userDataDirs(%q) = %v, want %v", c.cmdline, got, c.want)
			}
		}
	}
}
