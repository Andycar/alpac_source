//go:build windows

package httpapi

import "os"

// requestSelfRestart exits the process so the service manager (e.g. NSSM with
// restart=always) relaunches it. Windows has no self-SIGTERM, so this is a hard
// exit rather than a graceful drain.
func requestSelfRestart() {
	os.Exit(0)
}
