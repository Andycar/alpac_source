//go:build linux

package httpapi

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ProcessOSStats holds per-process resource usage read from /proc.
type ProcessOSStats struct {
	PID        int     `json:"pid"`
	RSSKB      int64   `json:"rss_kb"`
	CPUUserSec float64 `json:"cpu_user_sec"`
	CPUSysSec  float64 `json:"cpu_sys_sec"`
	Threads    int     `json:"threads"`
	State      string  `json:"state"` // R, S, D, Z, T
}

const clkTck = 100 // sysconf(_SC_CLK_TCK) — virtually always 100 on Linux

// readProcessStats reads /proc/{pid}/stat and /proc/{pid}/statm to collect
// RSS, CPU time, thread count and process state for a given PID.
func readProcessStats(pid int) (ProcessOSStats, bool) {
	s := ProcessOSStats{PID: pid}

	// --- /proc/{pid}/stat ---
	statData, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return s, false
	}
	statStr := string(statData)

	// The comm field (field 2) is enclosed in parentheses and may contain
	// spaces, so we find the last ')' to safely parse subsequent fields.
	closeParen := strings.LastIndex(statStr, ")")
	if closeParen < 0 || closeParen+2 >= len(statStr) {
		return s, false
	}
	rest := strings.Fields(statStr[closeParen+2:])
	// rest[0] = state (field 3), rest[11] = utime (field 14), rest[12] = stime (field 15),
	// rest[17] = num_threads (field 20)
	if len(rest) < 18 {
		return s, false
	}

	s.State = rest[0]

	if utime, err := strconv.ParseInt(rest[11], 10, 64); err == nil {
		s.CPUUserSec = float64(utime) / clkTck
	}
	if stime, err := strconv.ParseInt(rest[12], 10, 64); err == nil {
		s.CPUSysSec = float64(stime) / clkTck
	}
	if threads, err := strconv.Atoi(rest[17]); err == nil {
		s.Threads = threads
	}

	// --- /proc/{pid}/statm ---
	statmData, err := os.ReadFile(fmt.Sprintf("/proc/%d/statm", pid))
	if err != nil {
		// stat succeeded, statm failed — return what we have
		return s, true
	}
	fields := strings.Fields(string(statmData))
	// field[1] = resident pages
	if len(fields) >= 2 {
		if pages, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
			pageSize := int64(os.Getpagesize()) // typically 4096
			s.RSSKB = pages * pageSize / 1024
		}
	}

	return s, true
}
