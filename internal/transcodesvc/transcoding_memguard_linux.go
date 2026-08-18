//go:build linux

package transcodesvc

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// availableMemBytes returns the OS "available" memory in bytes — memory that
// can be given to a new process without swapping — from /proc/meminfo's
// MemAvailable. Returns -1 when it can't be read (gate then no-ops).
//
// This is the host-wide figure. A container without a memory cgroup limit (the
// tc-box case: docker --memory unset → limit == host RAM) sees the same number
// the OOM killer acts on, so gating on it is exactly what prevents the global
// OOM that reaps a running ffmpeg mid-playback.
func availableMemBytes() int64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return -1
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "MemAvailable:") {
			continue
		}
		fields := strings.Fields(line) // "MemAvailable:  1234567 kB"
		if len(fields) >= 2 {
			if kb, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
				return kb * 1024
			}
		}
		return -1
	}
	return -1
}
