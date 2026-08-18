//go:build !linux

package httpapi

// ProcessOSStats holds per-process resource usage.
// On non-Linux platforms this is a stub.
type ProcessOSStats struct {
	PID        int     `json:"pid"`
	RSSKB      int64   `json:"rss_kb"`
	CPUUserSec float64 `json:"cpu_user_sec"`
	CPUSysSec  float64 `json:"cpu_sys_sec"`
	Threads    int     `json:"threads"`
	State      string  `json:"state"`
}

// readProcessStats is a no-op on non-Linux platforms.
func readProcessStats(pid int) (ProcessOSStats, bool) {
	return ProcessOSStats{}, false
}
