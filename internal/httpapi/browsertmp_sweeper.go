package httpapi

import (
	"context"
	"time"

	"lampac-go/internal/browsertmp"
)

// browserTmpSweepInterval is how often orphaned browser profiles are collected
// after the startup pass. Hourly is plenty: the sweeper only exists to catch
// what a crash or a wedged Chrome left behind, and it never touches a directory
// younger than browsertmp.StaleAfter.
const browserTmpSweepInterval = time.Hour

// startBrowserTmpSweeper runs one sweep immediately (clearing whatever the
// previous process leaked) and then repeats until ctx is done.
func startBrowserTmpSweeper(ctx context.Context) {
	go func() {
		browsertmp.Sweep()

		ticker := time.NewTicker(browserTmpSweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				browsertmp.Sweep()
			}
		}
	}()
}
