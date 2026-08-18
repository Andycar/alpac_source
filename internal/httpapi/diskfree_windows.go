//go:build windows

package httpapi

import "golang.org/x/sys/windows"

// freeDiskSpaceBytes returns available disk space in bytes for the given path.
func freeDiskSpaceBytes(path string) (int64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return -1, err
	}
	var freeAvail, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &freeAvail, &total, &totalFree); err != nil {
		return -1, err
	}
	return int64(freeAvail), nil
}
