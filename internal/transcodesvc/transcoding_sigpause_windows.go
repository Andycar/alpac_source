//go:build windows

package transcodesvc

import "os/exec"

// SIGSTOP / SIGCONT have no Windows equivalent, so the window pacer degrades to
// a no-op there: ffmpeg keeps running at -readrate. Production runs on Linux.

func pauseProc(cmd *exec.Cmd) bool  { return false }
func resumeProc(cmd *exec.Cmd) bool { return false }
