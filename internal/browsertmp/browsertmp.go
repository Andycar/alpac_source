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
package browsertmp

import (
	"os"
	"path/filepath"
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
	"lampac-chromedp-",
	"lampac-playwright-",
	// "yt-mux-" is deliberately absent: youtube.go runs its own sweep with
	// knowledge of which mux sessions are still streaming.
}

// StaleAfter is how old an orphan must be before Sweep deletes it. Comfortably
// longer than any single resolve, so a directory in active use is never touched
// even when a sweep runs mid-request.
const StaleAfter = 2 * time.Hour

// New creates a profile directory with the given prefix.
func New(prefix string) (string, error) { return os.MkdirTemp("", prefix) }

// Remove deletes a profile directory, retrying while a dying Chrome re-creates
// files inside it. Blocking: call it after cancelling the browser context, off
// the request path.
func Remove(dir string) {
	if strings.TrimSpace(dir) == "" {
		return
	}
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
// than the cutoff is left alone, so a live browser is never disturbed.
func Sweep() int {
	root := os.TempDir()
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
