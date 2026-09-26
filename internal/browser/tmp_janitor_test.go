package browser

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"lampac-go/internal/browsertmp"
)

// Tests never sweep the machine's real snap Chromium /tmp.
func TestMain(m *testing.M) {
	browsertmp.SnapChromiumTmp = filepath.Join(os.TempDir(), "browser-test-no-snap")
	os.Exit(m.Run())
}

// A snap Chromium keeps its scratch and profile dirs in its private /tmp —
// the janitor has to sweep there too, or it frees nothing on such hosts.
func TestCleanupStaleChromeTmp_SweepsSnapChromiumTmp(t *testing.T) {
	host, snap := t.TempDir(), t.TempDir()
	t.Setenv("TMPDIR", host)
	prev := browsertmp.SnapChromiumTmp
	browsertmp.SnapChromiumTmp = snap
	t.Cleanup(func() { browsertmp.SnapChromiumTmp = prev })

	pastMtime := time.Now().Add(-2 * time.Hour)
	staleSnap := mkDir(t, snap, "lampac-chromedp-1", pastMtime)
	staleCrashpad := mkDir(t, snap, ".org.chromium.Chromium.Ab12", pastMtime)
	staleHost := mkDir(t, host, "lampac-chromedp-2", pastMtime)
	keepRezka := mkDir(t, snap, "rezka-chrome", pastMtime)

	if n := CleanupStaleChromeTmp("", 10*time.Minute); n != 3 {
		t.Fatalf("removed=%d, want 3 (two in the snap tmp, one in TMPDIR)", n)
	}
	for _, p := range []string{staleSnap, staleCrashpad, staleHost} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("expected %s removed, stat err=%v", p, err)
		}
	}
	if _, err := os.Stat(keepRezka); err != nil {
		t.Errorf("persistent profile must stay: %v", err)
	}
}

// TestCleanupStaleChromeTmp_RemovesStaleAndKeepsFresh covers the core
// two-way behaviour: stale (older than maxAge) directories matching a
// chrome prefix are removed, fresh ones are kept regardless of name.
func TestCleanupStaleChromeTmp_RemovesStaleAndKeepsFresh(t *testing.T) {
	tmp := t.TempDir()
	maxAge := time.Minute
	pastMtime := time.Now().Add(-2 * time.Hour)
	freshMtime := time.Now().Add(-5 * time.Second)

	// Stale matches: should be removed.
	staleChrome := mkDir(t, tmp, "com.google.Chrome.AbCdEf", pastMtime)
	staleChromium := mkDir(t, tmp, ".org.chromium.Chromium.Xyz123", pastMtime)
	staleMirage := mkDir(t, tmp, "mirage-chrome-9999", pastMtime)
	staleMirageFacade := mkDir(t, tmp, "mirage-facade-9999", pastMtime)
	staleAlloha := mkDir(t, tmp, "alloha-chrome-1234", pastMtime)
	staleChromedp := mkDir(t, tmp, "lampac-chromedp-1234", pastMtime)
	stalePlaywright := mkDir(t, tmp, "lampac-playwright-1234", pastMtime)

	// Fresh match: kept (might be an active Chrome session).
	freshChrome := mkDir(t, tmp, "com.google.Chrome.FreshOne", freshMtime)

	// Non-matching names: kept regardless of age (persistent profiles,
	// unrelated tools).
	keepRezka := mkDir(t, tmp, "rezka-chrome", pastMtime)
	keepZona := mkDir(t, tmp, "zona-chrome-profiles", pastMtime)
	keepPuppet := mkDir(t, tmp, "puppeteer_dev_chrome_profile-abc", pastMtime)
	keepNode := mkDir(t, tmp, "node_modules", pastMtime)

	// Regular file with a matching prefix: must NOT be touched
	// (defensive — Chrome only ever creates directories, but a stray
	// file should never be wiped by the janitor).
	staleFilePath := filepath.Join(tmp, "com.google.Chrome.SomeFile")
	if err := os.WriteFile(staleFilePath, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := os.Chtimes(staleFilePath, pastMtime, pastMtime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	removed := CleanupStaleChromeTmp(tmp, maxAge)
	if removed != 7 {
		t.Fatalf("removed=%d, want 7 (stale chrome/chromium/mirage/alloha dirs)", removed)
	}

	// Stale chrome-* dirs gone.
	for _, p := range []string{staleChrome, staleChromium, staleMirage, staleMirageFacade, staleAlloha, staleChromedp, stalePlaywright} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("expected %s removed, stat err=%v", p, err)
		}
	}
	// Fresh chrome dir kept.
	if _, err := os.Stat(freshChrome); err != nil {
		t.Errorf("fresh chrome dir should be kept: %v", err)
	}
	// Persistent / unrelated dirs kept.
	for _, p := range []string{keepRezka, keepZona, keepPuppet, keepNode} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("non-matching dir %s should be kept: %v", p, err)
		}
	}
	// File with matching prefix kept.
	if _, err := os.Stat(staleFilePath); err != nil {
		t.Errorf("file with matching prefix should be kept: %v", err)
	}
}

// TestCleanupStaleChromeTmp_EmptyDir tolerates a missing or empty
// tmp dir without panicking.
func TestCleanupStaleChromeTmp_EmptyDir(t *testing.T) {
	tmp := t.TempDir()
	if n := CleanupStaleChromeTmp(tmp, time.Minute); n != 0 {
		t.Fatalf("empty tmp returned removed=%d, want 0", n)
	}
	// Non-existent dir: best-effort, no panic, returns 0.
	if n := CleanupStaleChromeTmp(filepath.Join(tmp, "nope"), time.Minute); n != 0 {
		t.Fatalf("missing tmp returned removed=%d, want 0", n)
	}
}

// TestStartChromeTmpJanitor_BootSweepAndTicker verifies that
// StartChromeTmpJanitor sweeps once synchronously and continues to
// sweep on the configured interval until the context is cancelled.
func TestStartChromeTmpJanitor_BootSweepAndTicker(t *testing.T) {
	tmp := t.TempDir()
	// Redirect TempDir() lookup by setting TMPDIR for the duration of
	// the test. StartChromeTmpJanitor calls os.TempDir() internally,
	// which honours $TMPDIR on Unix.
	t.Setenv("TMPDIR", tmp)

	pastMtime := time.Now().Add(-2 * time.Hour)
	preBoot := mkDir(t, tmp, "com.google.Chrome.PreBoot", pastMtime)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Short interval so the ticker fires inside the test.
	StartChromeTmpJanitor(ctx, 25*time.Millisecond, 10*time.Millisecond)

	// The boot sweep is synchronous, so the pre-boot dir is gone before
	// StartChromeTmpJanitor returns.
	if _, err := os.Stat(preBoot); !os.IsNotExist(err) {
		t.Fatalf("boot sweep should have removed %s, stat err=%v", preBoot, err)
	}

	// Drop a new stale dir and wait for the ticker to fire.
	postBoot := mkDir(t, tmp, "com.google.Chrome.PostBoot", pastMtime)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(postBoot); os.IsNotExist(err) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("ticker sweep did not remove %s within deadline", postBoot)
}

// TestHasChromeTmpPrefix locks in the prefix list as an invariant —
// extending chromeTmpPrefixes is intentional, accidentally narrowing
// it is not.
func TestHasChromeTmpPrefix(t *testing.T) {
	matches := []string{
		"com.google.Chrome.aaa",
		".com.google.Chrome.aaa",
		"org.chromium.Chromium.aaa",
		".org.chromium.Chromium.aaa",
		"chromium_aaa",
		"lampac-chromedp-1",
		"lampac-playwright-1",
		"mirage-chrome-1",
		"mirage-facade-1",
		"alloha-chrome-1",
	}
	for _, s := range matches {
		if !hasChromeTmpPrefix(s) {
			t.Errorf("%q should match a chrome tmp prefix", s)
		}
	}
	nonMatches := []string{
		"rezka-chrome",
		"zona-chrome-profiles",
		"turbo-chrome-pac",
		"puppeteer_dev_chrome_profile-abc",
		"node_modules",
		"package.json",
		"kinobase-extract.js",
		"mongodb-27017.sock",
		"chrome-something", // intentionally no leading "chromium"
	}
	for _, s := range nonMatches {
		if hasChromeTmpPrefix(s) {
			t.Errorf("%q must NOT match a chrome tmp prefix", s)
		}
	}
}

func mkDir(t *testing.T, parent, name string, mtime time.Time) string {
	t.Helper()
	full := filepath.Join(parent, name)
	if err := os.MkdirAll(full, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", full, err)
	}
	if err := os.Chtimes(full, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", full, err)
	}
	return full
}
