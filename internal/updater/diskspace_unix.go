//go:build !windows

package updater

import "golang.org/x/sys/unix"

// freeDiskBytes reports the bytes available to a non-privileged writer in the
// filesystem backing dir (Bavail accounts for the reserved-block headroom).
// ok is false when the value can't be determined, in which case callers skip
// the pre-flight space check.
func freeDiskBytes(dir string) (free uint64, ok bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, false
	}
	return st.Bavail * uint64(st.Bsize), true
}
