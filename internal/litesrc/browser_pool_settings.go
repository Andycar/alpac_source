package litesrc

import (
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// Runtime-adjustable Mirage browser pool & cache settings.
// ---------------------------------------------------------------------------

var (
	mirageStreamCacheMax  atomic.Int32 // max cache entries
	mirageStreamCacheTTLH atomic.Int32 // eviction age in hours
)

func init() {
	mirageStreamCacheMax.Store(2000)
	mirageStreamCacheTTLH.Store(8)
}

// SetMirageBrowserLimit changes the max concurrent chromedp sessions at runtime.
func SetMirageBrowserLimit(n int) {
	mirageBrowserSem.SetLimit(n)
}

// SetMirageStreamCacheMax changes the max stream cache entries at runtime.
func SetMirageStreamCacheMax(n int) {
	if n < 100 {
		n = 100
	}
	mirageStreamCacheMax.Store(int32(n))
}

// SetMirageStreamCacheTTL changes the cache eviction age (hours) at runtime.
func SetMirageStreamCacheTTL(hours int) {
	if hours < 1 {
		hours = 1
	}
	mirageStreamCacheTTLH.Store(int32(hours))
}

// GetMirageStreamCacheMax returns the current max cache entries.
func GetMirageStreamCacheMax() int {
	return int(mirageStreamCacheMax.Load())
}

// GetMirageStreamCacheTTL returns the current cache eviction duration.
func GetMirageStreamCacheTTL() time.Duration {
	return time.Duration(mirageStreamCacheTTLH.Load()) * time.Hour
}

// MirageBrowserStats returns (active, limit) for the browser semaphore.
func MirageBrowserStats() (active int, limit int) {
	return mirageBrowserSem.Stats()
}

// GetMirageStreamCacheTTLHours returns the eviction age in raw hours
// (admin panel payload; GetMirageStreamCacheTTL returns the Duration).
func GetMirageStreamCacheTTLHours() int {
	return int(mirageStreamCacheTTLH.Load())
}
