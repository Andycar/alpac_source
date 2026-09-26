// Package browsertmp owns the throwaway Chrome/Playwright profile directories
// the browser-backed balancers create under os.TempDir().
//
// Two things leak them, and each needs its own remedy:
//
//   - A hard stop (systemctl restart, OOM kill, panic) skips every deferred
//     cleanup, so the profile of whatever browser was running stays behind
//     forever. Sweep() collects those on the next start.
//
//   - os.RemoveAll racing a still-running Chrome. Cancelling a chromedp context
//     returns before the browser process is gone; Chrome keeps writing (and
//     re-creating) files in the profile it still owns, so the directory pops
//     back into existence right after it was removed. Remove() retries so the
//     final attempt lands after the process is actually dead.
//
// A snap-packaged Chromium adds a third: snapd gives the browser a private
// /tmp, so the profile we create as /tmp/vibix-chrome-123 is really written to
// SnapChromiumTmp/vibix-chrome-123. Remove and Sweep therefore work on every
// root in Roots(), not just os.TempDir().
package browsertmp

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// Prefixes are the temp-dir name prefixes this package owns. Keep in sync with
// the os.MkdirTemp calls in the browser-backed sources.
var Prefixes = []string{
	"vibix-chrome-",
	"mirage-chrome-",
	"mirage-facade-",
	"alloha-chrome-",
	"kinobase-chrome-",
	"lampac-chromedp-",
	"lampac-playwright-",
	// Puppeteer's own throwaway profiles. kinobase now passes an explicit
	// userDataDir (kinobase-chrome-*) so nothing creates these any more, but a
	// host upgraded from an older build can carry hundreds of them — one
	// production box had 130 dirs / 15GB, none of which any janitor owned.
	"puppeteer_dev_chrome_profile-",
	// "yt-mux-" is deliberately absent: youtube.go runs its own sweep with
	// knowledge of which mux sessions are still streaming.
}

// StaleAfter is how old an orphan must be before Sweep deletes it. Comfortably
// longer than any single resolve, so a directory in active use is never touched
// even when a sweep runs mid-request.
const StaleAfter = 2 * time.Hour

// SnapChromiumTmp is the host path of snap Chromium's private /tmp. Anything
// the browser is told to keep under /tmp lands here instead, which is how FI
// piled up 1,220 vibix profiles / 36 GB in ~30 hours and ran out of disk
// (2026-09-26): Remove and Sweep only ever looked at the empty host copies.
// A variable so tests can point it elsewhere.
var SnapChromiumTmp = "/tmp/snap-private-tmp/snap.chromium/tmp"

// snapHostTmp is the directory snapd replaces with SnapChromiumTmp inside the
// snap. Variable for tests only.
var snapHostTmp = "/tmp"

// liveProfiles is InUse, swappable in tests.
var liveProfiles = InUse

// New creates a profile directory with the given prefix.
func New(prefix string) (string, error) { return os.MkdirTemp("", prefix) }

// Roots returns every directory that can hold our browser profiles:
// os.TempDir() and, on a host with snap Chromium, its private /tmp.
func Roots() []string {
	roots := []string{os.TempDir()}
	if st, err := os.Stat(SnapChromiumTmp); err == nil && st.IsDir() && filepath.Clean(SnapChromiumTmp) != filepath.Clean(roots[0]) {
		roots = append(roots, SnapChromiumTmp)
	}
	return roots
}

// snapMirror returns where snap Chromium really keeps a profile we created at
// dir, or "" when dir is not under /tmp or the host has no snap Chromium.
func snapMirror(dir string) string {
	rel, err := filepath.Rel(snapHostTmp, filepath.Clean(dir))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	if st, err := os.Stat(SnapChromiumTmp); err != nil || !st.IsDir() {
		return ""
	}
	return filepath.Join(SnapChromiumTmp, rel)
}

// InUse returns the base names of the --user-data-dir of every running process
// (Linux /proc; empty elsewhere). Age alone does not prove a profile is
// orphaned: a long-lived Chrome can leave the top-level mtime of its profile
// untouched for hours, and on a snap host the sweeps now reach the directories
// Chrome really uses.
func InUse() map[string]struct{} {
	out := map[string]struct{}{}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return out
	}
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil || len(b) == 0 {
			continue
		}
		for _, d := range userDataDirs(b) {
			out[filepath.Base(filepath.Clean(d))] = struct{}{}
		}
	}
	return out
}

// userDataDirs extracts the --user-data-dir values from /proc/<pid>/cmdline.
// Arguments are NUL-separated, except in Chrome's child processes, which
// rewrite their title into one space-joined string — so split on both (our
// profile paths never contain spaces).
func userDataDirs(cmdline []byte) []string {
	var out []string
	args := strings.FieldsFunc(string(cmdline), func(r rune) bool { return r == 0 || r == ' ' })
	for i, a := range args {
		if v, ok := strings.CutPrefix(a, "--user-data-dir="); ok {
			if v != "" {
				out = append(out, v)
			}
		} else if a == "--user-data-dir" && i+1 < len(args) && args[i+1] != "" {
			out = append(out, args[i+1])
		}
	}
	return out
}

// Remove deletes a profile directory, retrying while a dying Chrome re-creates
// files inside it. Blocking: call it after cancelling the browser context, off
// the request path.
func Remove(dir string) {
	if strings.TrimSpace(dir) == "" {
		return
	}
	removeRetrying(dir)
	// Snap Chromium never wrote to dir itself — its profile is the mirror.
	if m := snapMirror(dir); m != "" {
		removeRetrying(m)
	}
}

func removeRetrying(dir string) {
	// Delete, then WAIT before checking. RemoveAll succeeds and os.Stat reports
	// "gone" microseconds later even when the browser is about to re-create the
	// directory, so an immediate check always looks like success — the pause is
	// what makes the verification meaningful.
	const attempts = 4
	backoff := 250 * time.Millisecond
	for i := 0; i < attempts; i++ {
		if err := os.RemoveAll(dir); err != nil {
			log.Debug().Err(err).Str("dir", dir).Int("attempt", i+1).Msg("browsertmp: remove failed")
		}
		time.Sleep(backoff)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			return
		}
		backoff *= 2
	}
	log.Warn().Str("dir", dir).Msg("browsertmp: profile dir keeps coming back — left for the next sweep")
}

// Sweep deletes orphaned profile directories older than StaleAfter and returns
// how many it removed. Safe to call at startup and on a timer: anything younger
// than the cutoff, and any profile a running process names as its
// --user-data-dir, is left alone, so a live browser is never disturbed.
func Sweep() int {
	inUse := liveProfiles()
	removed := 0
	for _, root := range Roots() {
		removed += sweepRoot(root, inUse)
	}
	return removed
}

func sweepRoot(root string, inUse map[string]struct{}) int {
	entries, err := os.ReadDir(root)
	if err != nil {
		log.Debug().Err(err).Str("dir", root).Msg("browsertmp: cannot read temp dir")
		return 0
	}

	cutoff := time.Now().Add(-StaleAfter)
	removed := 0
	for _, e := range entries {
		if !e.IsDir() || !hasKnownPrefix(e.Name()) {
			continue
		}
		if _, live := inUse[e.Name()]; live {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		path := filepath.Join(root, e.Name())
		if err := os.RemoveAll(path); err != nil {
			log.Debug().Err(err).Str("dir", path).Msg("browsertmp: sweep failed")
			continue
		}
		removed++
	}
	if removed > 0 {
		log.Info().Int("removed", removed).Str("dir", root).Msg("browsertmp: swept orphaned browser profiles")
	}
	return removed
}

func hasKnownPrefix(name string) bool {
	for _, p := range Prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
