package updater

import (
	"os"
	"runtime"
	"strings"
)

// RunMode describes how the current binary is launched and therefore
// how the updater is allowed to apply a new build.
type RunMode int

const (
	// ModeBinary — plain binary started by hand or by a supervisor that
	// re-executes it. Self-replace + self-exec is allowed.
	ModeBinary RunMode = iota
	// ModeSystemd — launched under systemd. Self-replace is allowed, and
	// the service can be restarted by asking systemd (or by exiting with a
	// restart policy in place).
	ModeSystemd
	// ModeDocker — running inside a container. The filesystem is usually
	// ephemeral and replacing /lampac-go is pointless. We only notify.
	ModeDocker
	// ModeDev — running via `go run` / `dlv` / from a temp build dir.
	// Self-update is blocked.
	ModeDev
)

func (m RunMode) String() string {
	switch m {
	case ModeBinary:
		return "binary"
	case ModeSystemd:
		return "systemd"
	case ModeDocker:
		return "docker"
	case ModeDev:
		return "dev"
	}
	return "unknown"
}

// CanSelfUpdate reports whether the current mode allows self-replacement of
// the running binary. Docker and dev modes disallow it.
func (m RunMode) CanSelfUpdate() bool {
	return m == ModeBinary || m == ModeSystemd
}

// DetectMode inspects the environment and returns the current run mode.
// The detection is best-effort and never errors; unknown environments fall
// back to ModeBinary.
func DetectMode() RunMode {
	// Dev: go run compiles into a temp directory like
	// /tmp/go-build*/b001/exe/lampac-go.
	if exe, err := os.Executable(); err == nil {
		low := strings.ToLower(exe)
		if strings.Contains(low, "/go-build") || strings.Contains(low, "/tmp/go-build") {
			return ModeDev
		}
	}

	// Docker: /.dockerenv always present in official images.
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return ModeDocker
	}
	// Fallback: /proc/1/cgroup contains "docker"/"containerd"/"kubepods".
	if runtime.GOOS == "linux" {
		if data, err := os.ReadFile("/proc/1/cgroup"); err == nil {
			s := string(data)
			if strings.Contains(s, "docker") || strings.Contains(s, "containerd") || strings.Contains(s, "kubepods") {
				return ModeDocker
			}
		}
	}

	// Systemd: INVOCATION_ID is set by systemd for every unit start since v232.
	if os.Getenv("INVOCATION_ID") != "" || os.Getenv("NOTIFY_SOCKET") != "" {
		return ModeSystemd
	}

	return ModeBinary
}

// AssetName returns the name of the release asset for the current platform:
//
//	lampac-go-<os>-<arch>
//
// All builds ship with TorrServer and ProxyCore embedded, so there is no
// longer a separate "-ts-" variant — a single binary per architecture.
func AssetName() string {
	return "lampac-go-" + runtime.GOOS + "-" + runtime.GOARCH
}
