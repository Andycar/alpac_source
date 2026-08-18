package browser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChromeProcessEnv_RoutesTmpToProfile(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "profile")
	env := ChromeProcessEnv([]string{"PATH=/bin", "TMPDIR=/tmp/old"}, profile)
	tmpDir := filepath.Join(profile, "tmp")

	for _, want := range []string{
		"XDG_RUNTIME_DIR=" + profile,
		"TMPDIR=" + tmpDir,
		"TMP=" + tmpDir,
		"TEMP=" + tmpDir,
	} {
		if !envHas(env, want) {
			t.Fatalf("env missing %q in %v", want, env)
		}
	}
	if _, err := os.Stat(tmpDir); err != nil {
		t.Fatalf("tmp dir not created: %v", err)
	}
}

func envHas(env []string, want string) bool {
	for _, kv := range env {
		if strings.HasPrefix(kv, strings.Split(want, "=")[0]+"=") {
			return kv == want
		}
	}
	return false
}
