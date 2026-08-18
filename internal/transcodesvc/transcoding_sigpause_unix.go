//go:build !windows

package transcodesvc

import (
	"os/exec"
	"syscall"
)

// pauseProc freezes a running ffmpeg process with SIGSTOP — it stops consuming
// CPU and stops reading its input (HTTP / torrent-stdin pipe), so the upstream
// download pauses too. Returns true if the signal was delivered.
func pauseProc(cmd *exec.Cmd) bool {
	if cmd == nil || cmd.Process == nil {
		return false
	}
	return cmd.Process.Signal(syscall.SIGSTOP) == nil
}

// resumeProc un-freezes a SIGSTOP-paused process with SIGCONT.
func resumeProc(cmd *exec.Cmd) bool {
	if cmd == nil || cmd.Process == nil {
		return false
	}
	return cmd.Process.Signal(syscall.SIGCONT) == nil
}
