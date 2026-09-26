//go:build !windows

package litesrc

import (
	"os/exec"
	"syscall"
)

// setProcGroup puts the node child into its own process group so the whole tree
// can be signalled at once.
//
// Without it a timeout reaches node only: exec.CommandContext calls
// Process.Kill(), the Chrome that node spawned through Puppeteer survives and is
// reparented to init. That is what filled /tmp with 130 orphaned
// puppeteer_dev_chrome_profile-* directories (15GB) and left ~30 headless
// browsers holding gigabytes of RSS on the production host.
func setProcGroup(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcGroup SIGKILLs every process in the group created by setProcGroup —
// node plus each Chrome it forked.
//
// The pgid == pid check is a safety interlock, not a formality: if Setpgid did
// not take effect the child still sits in OUR group, and kill(-pgid) would take
// down lampac-go itself. When the group is not ours we kill the single process
// and accept that a browser may linger for the janitor to sweep.
func killProcGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	if pgid, err := syscall.Getpgid(pid); err == nil && pgid == pid {
		if syscall.Kill(-pgid, syscall.SIGKILL) == nil {
			return
		}
	}
	_ = cmd.Process.Kill()
}
