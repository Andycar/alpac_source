package custbal

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Compile builds a custom balancer binary in the given directory.
// It runs `go build -o {name} .` and returns the combined stdout+stderr output.
func Compile(dir, name string) (string, error) {
	binName := name
	outPath := filepath.Join(dir, binName)

	// Ensure go.mod exists.
	goModPath := filepath.Join(dir, "go.mod")
	if _, err := os.Stat(goModPath); os.IsNotExist(err) {
		goMod := fmt.Sprintf("module custbal-%s\n\ngo 1.22\n", name)
		if err := os.WriteFile(goModPath, []byte(goMod), 0644); err != nil {
			return "", fmt.Errorf("write go.mod: %w", err)
		}
	}

	// Find Go binary.
	goPath := findGoBinary()

	cmd := exec.Command(goPath, "build", "-o", outPath, ".")
	cmd.Dir = dir

	env := append(os.Environ(), "CGO_ENABLED=0")

	// Pin GOCACHE to a per-balancer directory. The shared user GOCACHE is
	// keyed by Go version; mixing a 1.24 cache with a 1.26 toolchain
	// produces cryptic "compile: version X does not match go tool
	// version Y" errors when the host has multiple Go installs (common
	// on dev macs). A per-balancer cache keeps things hermetic and small.
	cacheDir := filepath.Join(dir, ".gocache")
	_ = os.MkdirAll(cacheDir, 0755)
	env = append(env, "GOCACHE="+cacheDir)

	if os.Getenv("GOPATH") == "" && os.Getenv("HOME") == "" {
		goPathDir := filepath.Join(dir, ".gopath")
		os.MkdirAll(goPathDir, 0755)
		env = append(env, "GOPATH="+goPathDir)
	}

	cmd.Env = env

	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))
	if err != nil {
		return output, fmt.Errorf("go build failed: %s: %w", output, err)
	}

	// Make binary executable.
	if err := os.Chmod(outPath, 0755); err != nil {
		return output, fmt.Errorf("chmod: %w", err)
	}

	return output, nil
}

// findGoBinary locates the Go compiler. Prefers a binary whose `go
// version` matches runtime.Version() of the running lampac-go process —
// otherwise the cached stdlib `.a` files (from one toolchain) collide
// with the compiler tool of another toolchain and you get
// "compile: version X does not match go tool version Y".
func findGoBinary() string {
	want := runtime.Version() // e.g. "go1.24.6"

	candidates := []string{}
	// runtime.GOROOT() points at the toolchain that built lampac-go —
	// strongest candidate.
	if root := runtime.GOROOT(); root != "" {
		candidates = append(candidates, filepath.Join(root, "bin", "go"))
	}
	candidates = append(candidates,
		"/usr/local/go/bin/go",
		"/usr/bin/go",
		"/opt/homebrew/bin/go",
	)
	if p, err := exec.LookPath("go"); err == nil {
		candidates = append(candidates, p)
	}

	// First pass: pick a candidate whose `go version` matches runtime.Version().
	for _, p := range candidates {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if goBinaryVersion(p) == want {
			return p
		}
	}
	// Fallback: any candidate that exists, even with a version mismatch
	// (better than failing — and tests like custom main.go without
	// stdlib imports may compile fine across versions).
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "go"
}

// goBinaryVersion returns the runtime version reported by `<binary>
// version`, e.g. "go1.24.6". Empty on probe failure.
func goBinaryVersion(binary string) string {
	out, err := exec.Command(binary, "version").Output()
	if err != nil {
		return ""
	}
	// Output: "go version go1.24.6 darwin/arm64"
	fields := strings.Fields(string(out))
	if len(fields) >= 3 && strings.HasPrefix(fields[2], "go") {
		return fields[2]
	}
	return ""
}
