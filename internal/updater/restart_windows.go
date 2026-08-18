//go:build windows

package updater

import (
	"os"
	"os/exec"
	"syscall"
)

// platformRestart spawns a detached child copy of the binary with the same
// arguments, then exits the current process.
//
// Windows has no execve — the only way to "restart in-place" is to launch a
// new process and let the service manager (or a wrapper .bat) notice that
// the old PID is gone. This helper uses CREATE_NEW_PROCESS_GROUP so the
// child survives independent of the parent's console.
func platformRestart(exe string, args []string) error {
	cmd := exec.Command(exe, args[1:]...)
	cmd.Env = os.Environ()
	cmd.Stdin = nil
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x00000200, // CREATE_NEW_PROCESS_GROUP
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	// Detach from the child and exit.
	_ = cmd.Process.Release()
	os.Exit(0)
	return nil
}
