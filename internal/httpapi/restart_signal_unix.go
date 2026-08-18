//go:build !windows

package httpapi

import (
	"os"
	"syscall"
)

// requestSelfRestart asks the process to shut down gracefully. On Unix we send
// ourselves SIGTERM, which main.go traps and drains before exiting; the service
// manager (systemd Restart=always) then brings us back.
func requestSelfRestart() {
	_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
}
