//go:build !windows

package updater

import "syscall"

// platformRestart re-executes the running binary in-place using execve.
// On Linux/macOS this preserves the process PID, so systemd, launchd and
// sysvinit-style supervisors don't notice the swap — they keep pointing
// at the same process, which is now running the new code.
func platformRestart(exe string, args []string) error {
	return syscall.Exec(exe, args, syscall.Environ())
}
