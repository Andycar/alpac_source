package browser

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// chromeTmpPrefixes are leading filename fragments for transient Chrome
// scratch directories that Chrome itself or our balancer pools create
// in $TMPDIR. The janitor only ever touches names matching one of these
// prefixes; persistent profiles ("rezka-chrome", "zona-chrome-profiles",
// "turbo-chrome-pac", "puppeteer_dev_chrome_profile-*") are intentionally
// omitted — they are managed by their owners.
//
// "com.google.Chrome." and ".org.chromium.Chromium." are created by the
// Chrome / Chromium binary itself for Crashpad scratch space whenever a
// session starts. Chrome deletes them on graceful exit, but a context
// cancel / SIGKILL leaves them behind, which is what fills /tmp on
// production hosts.
//
// "mirage-chrome-" / "alloha-chrome-" are our own MkdirTemp prefixes —
// the owning resolve does `defer os.RemoveAll`, but a panic / SIGKILL
// would skip the defer.
var chromeTmpPrefixes = []string{
	"com.google.Chrome.",
	".com.google.Chrome.",
	".org.chromium.Chromium.",
	"org.chromium.Chromium.",
	"chromium_",
	"lampac-chromedp-",
	"lampac-playwright-",
	"mirage-chrome-",
	"mirage-facade-",
	"alloha-chrome-",
}

// Defaults — exposed only for tests; the public entry point takes
// explicit values so callers can tune behaviour from main.go.
const (
	chromeTmpDefaultMaxAge   = 10 * time.Minute
	chromeTmpDefaultInterval = 5 * time.Minute
)

// CleanupStaleChromeTmp removes Chrome scratch directories in tmpDir
// whose ModTime is older than maxAge. Returns the number of removed
// entries. Failures are logged at debug level and never propagated —
// the caller continues even if a single removal fails.
//
// Pass tmpDir == "" to use os.TempDir(). Pass maxAge == 0 to skip the
// freshness check (NOT recommended in production — a live Chrome
// session can have an arbitrary ModTime).
func CleanupStaleChromeTmp(tmpDir string, maxAge time.Duration) int {
	if tmpDir == "" {
		tmpDir = os.TempDir()
	}
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		log.Debug().Err(err).Str("dir", tmpDir).Msg("chrome tmp janitor: readdir failed")
		return 0
	}

	cutoff := time.Now().Add(-maxAge)
	removed := 0
	for _, e := range entries {
		name := e.Name()
		if !hasChromeTmpPrefix(name) {
			continue
		}
		// Only directories — never touch sockets / regular files that
		// happen to share a prefix.
		if !e.IsDir() {
			continue
		}
		full := filepath.Join(tmpDir, name)
		info, err := os.Stat(full)
		if err != nil {
			continue
		}
		if maxAge > 0 && !info.ModTime().Before(cutoff) {
			// Directory is fresher than the cutoff — likely owned by
			// a live Chrome process. Leave it alone.
			continue
		}
		if err := os.RemoveAll(full); err != nil {
			log.Debug().Err(err).Str("dir", full).Msg("chrome tmp janitor: remove failed")
			continue
		}
		removed++
	}
	if removed > 0 {
		log.Info().Int("removed", removed).Str("dir", tmpDir).Msg("chrome tmp janitor: swept stale dirs")
	}
	return removed
}

// StartChromeTmpJanitor runs CleanupStaleChromeTmp once immediately
// (with maxAge) and then on every tick of interval until ctx is done.
// It is safe to call multiple times — each call starts its own
// goroutine; production callers should invoke it exactly once.
//
// interval == 0 falls back to chromeTmpDefaultInterval; maxAge == 0
// falls back to chromeTmpDefaultMaxAge.
func StartChromeTmpJanitor(ctx context.Context, interval, maxAge time.Duration) {
	if interval <= 0 {
		interval = chromeTmpDefaultInterval
	}
	if maxAge <= 0 {
		maxAge = chromeTmpDefaultMaxAge
	}
	// Synchronous boot sweep: free disk before the first balancer
	// resolve gets a chance to add more scratch dirs.
	CleanupStaleChromeTmp("", maxAge)

	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				CleanupStaleChromeTmp("", maxAge)
			}
		}
	}()
}

func hasChromeTmpPrefix(name string) bool {
	for _, p := range chromeTmpPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
