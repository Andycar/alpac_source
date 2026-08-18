package browser

import (
	"os"
	"path/filepath"
	"strings"
)

// ChromeProcessEnv keeps Chrome's scratch files next to its profile instead of
// the global /tmp. Chrome creates com.google.Chrome.* / Chromium.* directories
// under TMPDIR; on busy balancer hosts those can fill /tmp when processes are
// cancelled or killed before Chrome's own cleanup runs.
func ChromeProcessEnv(env []string, userDataDir string) []string {
	if len(env) == 0 {
		env = os.Environ()
	}
	if userDataDir == "" {
		userDataDir = os.TempDir()
	}
	tmpDir := filepath.Join(userDataDir, "tmp")
	_ = os.MkdirAll(tmpDir, 0o700)
	env = upsertEnv(env, "XDG_RUNTIME_DIR", userDataDir)
	env = upsertEnv(env, "TMPDIR", tmpDir)
	env = upsertEnv(env, "TMP", tmpDir)
	env = upsertEnv(env, "TEMP", tmpDir)
	return env
}

// ChromeProcessEnvList is the rod launcher form of ChromeProcessEnv.
func ChromeProcessEnvList(userDataDir string) []string {
	return ChromeProcessEnv(nil, userDataDir)
}

// ChromeProcessEnvMap is the Playwright launch-options form of ChromeProcessEnv.
func ChromeProcessEnvMap(userDataDir string) map[string]string {
	out := make(map[string]string)
	for _, kv := range ChromeProcessEnv(nil, userDataDir) {
		k, v, ok := strings.Cut(kv, "=")
		if ok {
			out[k] = v
		}
	}
	return out
}

func upsertEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}
