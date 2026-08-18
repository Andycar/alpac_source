//go:build !windows

package httpapi

import "syscall"

// freeDiskSpaceBytes returns available disk space in bytes for the given path.
func freeDiskSpaceBytes(path string) (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return -1, err
	}
	return int64(stat.Bavail) * int64(stat.Bsize), nil
}
