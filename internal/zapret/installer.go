package zapret

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// installRoot is where we clone bol-van/zapret when auto-installing. Picked to
// match the upstream install_easy.sh convention so manual zapret installs are
// indistinguishable from ours.
const installRoot = "/opt/zapret"

// installerOptions are tweakable knobs for EnsureInstalled. The defaults match
// what most lampac VPS need; tests can stub via the build* fields.
type installerOptions struct {
	zapretRepo string
	installDir string
}

func defaultInstallerOptions() installerOptions {
	return installerOptions{
		zapretRepo: "https://github.com/bol-van/zapret.git",
		installDir: installRoot,
	}
}

// EnsureInstalled checks for nft and nfqws. Missing pieces are installed in
// place: apt deps, git clone, make. Returns the path to nfqws (or "" if no
// install was needed and the binary is already on PATH/known locations).
//
// Linux-only. On other platforms returns ErrUnsupportedPlatform so the caller
// can disable zapret cleanly.
//
// Requires root for apt-get and writing /opt/zapret. Without root we fail
// fast with a friendly error so the manager can degrade in permissive mode.
func EnsureInstalled(ctx context.Context) (string, error) {
	if runtime.GOOS != "linux" {
		return "", ErrUnsupportedPlatform
	}

	// Step 0: check what's already there. If both nft and nfqws are present,
	// we have nothing to do.
	if _, err := exec.LookPath("nft"); err == nil {
		if bin := findNFQWS(); bin != "" {
			return bin, nil
		}
	}

	if os.Geteuid() != 0 {
		return "", errors.New("zapret: auto-install requires root (run lampac as root or pre-install zapret manually)")
	}

	opts := defaultInstallerOptions()

	// Step 1: install OS packages. We tolerate non-apt distros — the user
	// will see a clear error and can pre-install manually.
	if err := ensureAPTPackages(ctx); err != nil {
		return "", fmt.Errorf("apt deps: %w", err)
	}

	// Step 2: clone or update zapret source.
	if err := ensureZapretSource(ctx, opts); err != nil {
		return "", fmt.Errorf("clone zapret: %w", err)
	}

	// Step 3: build nfqws.
	bin, err := buildNFQWS(ctx, opts)
	if err != nil {
		return "", fmt.Errorf("build nfqws: %w", err)
	}
	log.Info().Str("bin", bin).Msg("zapret: auto-install complete")
	return bin, nil
}

// ErrUnsupportedPlatform is returned by EnsureInstalled on non-Linux hosts.
var ErrUnsupportedPlatform = errors.New("zapret: only Linux is supported")

// findNFQWS scans the same locations as locateBinary; kept as a separate
// helper so the installer can short-circuit before touching apt.
func findNFQWS() string {
	if p, err := exec.LookPath("nfqws"); err == nil {
		return p
	}
	for _, c := range []string{
		filepath.Join(installRoot, "nfq", "nfqws"),
		"/usr/sbin/nfqws",
		"/usr/local/sbin/nfqws",
		"/usr/bin/nfqws",
	} {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

// ensureAPTPackages installs nftables + build deps via apt-get. Skips packages
// that are already present (apt-get is idempotent). Uses noninteractive mode
// so it can't hang on a debconf prompt.
func ensureAPTPackages(ctx context.Context) error {
	if _, err := exec.LookPath("apt-get"); err != nil {
		return errors.New("apt-get not found (only Debian/Ubuntu auto-install is supported; install zapret manually)")
	}

	updateCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := runWithLog(updateCtx, "apt-get", "update", "-q"); err != nil {
		log.Warn().Err(err).Msg("zapret: apt-get update failed (continuing with cached lists)")
	}

	pkgs := []string{
		"nftables",
		"git",
		"make",
		"gcc",
		"libnetfilter-queue-dev",
		"libnfnetlink-dev",
		"libcap-dev",
		"zlib1g-dev",
	}
	args := append([]string{"-q", "-y", "-o", "DPkg::Options::=--force-confnew", "install"}, pkgs...)
	installCtx, cancel2 := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel2()
	cmd := exec.CommandContext(installCtx, "apt-get", args...)
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	return runCmdWithLog(cmd)
}

// ensureZapretSource clones the zapret repo (or pulls the latest if it already
// exists). Uses --depth=1 so we don't haul the entire history.
func ensureZapretSource(ctx context.Context, opts installerOptions) error {
	gitDir := filepath.Join(opts.installDir, ".git")
	if _, err := os.Stat(gitDir); err == nil {
		// Existing checkout — try a fast-forward pull, but don't fail if the
		// network is unreachable; the existing source is good enough.
		pullCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		cmd := exec.CommandContext(pullCtx, "git", "-C", opts.installDir, "pull", "--ff-only")
		if err := runCmdWithLog(cmd); err != nil {
			log.Warn().Err(err).Msg("zapret: git pull failed (using existing source)")
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(opts.installDir), 0755); err != nil {
		return err
	}
	cloneCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cloneCtx, "git", "clone", "--depth=1", opts.zapretRepo, opts.installDir)
	return runCmdWithLog(cmd)
}

// buildNFQWS compiles nfqws via the upstream Makefile. Returns the path to the
// built binary on success.
func buildNFQWS(ctx context.Context, opts installerOptions) (string, error) {
	nfqDir := filepath.Join(opts.installDir, "nfq")
	if _, err := os.Stat(nfqDir); err != nil {
		return "", fmt.Errorf("zapret/nfq/ missing in %s — clone broken", opts.installDir)
	}
	makeCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(makeCtx, "make", "-j", fmt.Sprintf("%d", buildJobs()), "-C", nfqDir)
	if err := runCmdWithLog(cmd); err != nil {
		return "", err
	}
	bin := filepath.Join(nfqDir, "nfqws")
	if _, err := os.Stat(bin); err != nil {
		return "", fmt.Errorf("nfqws not produced at %s", bin)
	}
	// Best-effort: grant CAP_NET_ADMIN so the supervisor can run nfqws as a
	// non-root child if the user later drops privileges. Failures are logged
	// but not fatal — running as root works regardless.
	if _, err := exec.LookPath("setcap"); err == nil {
		setcapCtx, c2 := context.WithTimeout(ctx, 10*time.Second)
		defer c2()
		_ = runCmdWithLog(exec.CommandContext(setcapCtx, "setcap", "cap_net_admin,cap_net_raw=eip", bin))
	}
	return bin, nil
}

func buildJobs() int {
	n := runtime.NumCPU()
	if n < 1 {
		return 1
	}
	if n > 4 {
		return 4
	}
	return n
}

// runWithLog runs `name args...` and forwards stdout+stderr to the lampac log.
func runWithLog(ctx context.Context, name string, args ...string) error {
	return runCmdWithLog(exec.CommandContext(ctx, name, args...))
}

func runCmdWithLog(cmd *exec.Cmd) error {
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if msg := strings.TrimSpace(out.String()); msg != "" {
		// Each subprocess line goes to a single info entry — admins can grep
		// the lampac log for the install transcript.
		for _, line := range strings.Split(msg, "\n") {
			if line = strings.TrimRight(line, "\r"); line != "" {
				log.Info().Str("cmd", cmd.Path).Msg("zapret/install: " + line)
			}
		}
	}
	return err
}
