//go:build windows

package updater

// freeDiskBytes is unsupported on Windows, so the updater skips the pre-flight
// space check there and lets the download surface a write error if the disk is
// full.
func freeDiskBytes(dir string) (free uint64, ok bool) {
	return 0, false
}
