//go:build windows

package litesrc

import "os/exec"

// Windows has no POSIX process groups, so the timeout path degrades to killing
// node alone and a spawned Chrome may linger until browsertmp.Sweep collects
// its profile. Production runs on Linux.

func setProcGroup(cmd *exec.Cmd) {}

func killProcGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
