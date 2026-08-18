package httpapi

import (
	"errors"
	"fmt"
	"lampac-go/internal/adminhttp"
	"os"
	"regexp"
	"strings"

	"lampac-go/internal/config"

	"github.com/rs/zerolog/log"
)

// ensureClusterSecretsResult reports what was generated.
type ensureClusterSecretsResult struct {
	APIKeyGenerated       bool
	SharedSecretGenerated bool
	APIKey                string // current value (existing or freshly generated)
	SharedSecret          string // current value (existing or freshly generated)
}

// ensureClusterSecrets makes sure cluster.api_key and proxy_link.shared_secret
// are both set. If either is empty, it generates a 32-byte hex value (256 bit
// of entropy), patches config.toml in place, and triggers a hot-reload.
//
// Returns what was generated and the current values so the admin UI can show
// them to the user with a "copy to peers" affordance.
func ensureClusterSecrets() (ensureClusterSecretsResult, error) {
	res := ensureClusterSecretsResult{}
	if !serverReady() {
		return res, errors.New("server not initialised")
	}
	cur := liveConfig(config.Config{})
	res.APIKey = strings.TrimSpace(cur.Cluster.APIKey)
	res.SharedSecret = strings.TrimSpace(cur.ProxyLink.SharedSecret)
	if res.APIKey != "" && res.SharedSecret != "" {
		return res, nil // nothing to do
	}

	if res.APIKey == "" {
		res.APIKey = randomHex(32)
		res.APIKeyGenerated = true
	}
	if res.SharedSecret == "" {
		res.SharedSecret = randomHex(32)
		res.SharedSecretGenerated = true
	}

	// Patch config.toml on disk so the values persist across restarts.
	root := adminhttp.ConfigRepoRoot()
	tomlPath := config.TOMLFilePath(root)
	data, err := os.ReadFile(tomlPath)
	if err != nil {
		return res, fmt.Errorf("read config.toml: %w", err)
	}
	patched, err := patchClusterSecretsTOML(string(data), res.APIKey, res.SharedSecret)
	if err != nil {
		return res, fmt.Errorf("patch config.toml: %w", err)
	}
	// Backup current before rewrite.
	if _, err := adminhttp.CreateConfigBackup(data); err != nil {
		log.Warn().Err(err).Msg("cluster: backup of config.toml before secret injection failed")
	}
	if err := os.WriteFile(tomlPath, []byte(patched), 0o644); err != nil {
		return res, fmt.Errorf("write config.toml: %w", err)
	}
	log.Info().Bool("api_key", res.APIKeyGenerated).Bool("shared_secret", res.SharedSecretGenerated).Msg("cluster: auto-generated secrets written to config.toml")

	// Hot-reload so the new values take effect without restart.
	if err := reloadServer(); err != nil {
		return res, fmt.Errorf("reload: %w", err)
	}
	log.Info().Msg("cluster: hot-reload complete — proxylink AES key rotated to shared_secret")
	return res, nil
}

// patchClusterSecretsTOML inserts or replaces api_key= in [cluster] and
// shared_secret= in [proxy_link]. Idempotent — already-set keys are left
// alone (this function is only called when at least one is missing).
//
// Strategy: regex-based section replacement. We don't fully parse TOML
// because go-toml round-trips strip comments and formatting; the original
// config.toml usually has comments the admin wants to keep.
func patchClusterSecretsTOML(src, apiKey, sharedSecret string) (string, error) {
	src = patchTOMLKey(src, "cluster", "api_key", apiKey)
	src = patchTOMLKey(src, "proxy_link", "shared_secret", sharedSecret)
	return src, nil
}

// patchTOMLKey sets `key = "value"` inside [section]. If the section exists
// and the key is already there with a non-empty value, leaves it alone. If
// the section exists but the key is missing or empty, appends the key. If
// the section is missing entirely, appends a new section at the end.
func patchTOMLKey(src, section, key, value string) string {
	sectionRe := regexp.MustCompile(`(?m)^\[` + regexp.QuoteMeta(section) + `\]\s*$`)
	sectionLoc := sectionRe.FindStringIndex(src)
	if sectionLoc == nil {
		// Append new section.
		if !strings.HasSuffix(src, "\n") {
			src += "\n"
		}
		src += fmt.Sprintf("\n[%s]\n%s = %q\n", section, key, value)
		return src
	}

	// Find the section's body: from after the header to either the next [section] or EOF.
	bodyStart := sectionLoc[1]
	rest := src[bodyStart:]
	nextSection := regexp.MustCompile(`(?m)^\[`).FindStringIndex(rest)
	bodyEnd := len(src)
	if nextSection != nil {
		bodyEnd = bodyStart + nextSection[0]
	}
	body := src[bodyStart:bodyEnd]

	keyRe := regexp.MustCompile(`(?m)^(\s*)` + regexp.QuoteMeta(key) + `\s*=\s*"(.*?)"`)
	if m := keyRe.FindStringSubmatchIndex(body); m != nil {
		// Key exists. Check if value is empty — only then we replace it.
		existing := body[m[4]:m[5]]
		if strings.TrimSpace(existing) != "" {
			return src // already set, don't touch
		}
		// Replace the value in-place.
		newBody := body[:m[4]] + value + body[m[5]:]
		return src[:bodyStart] + newBody + src[bodyEnd:]
	}

	// Key missing — insert at the top of the section body.
	insertion := fmt.Sprintf("%s = %q\n", key, value)
	return src[:bodyStart] + "\n" + insertion + src[bodyStart:]
}

// (randomHex is defined in bkit_session.go — shared utility.)

// buildPeerConfigSnippet returns a ready-to-paste config.toml fragment for a
// new cluster node. The admin shows this in a copy-to-clipboard field after
// generating secrets, so users can stand up a node by pasting one block.
func buildPeerConfigSnippet(apiKey, sharedSecret string) string {
	return fmt.Sprintf(`# Paste into config.toml on each cluster node, then start lampac-go.
[cluster]
enable = true
mode = "node"
api_key = %q

[proxy_link]
shared_secret = %q
`, apiKey, sharedSecret)
}

// forceRewriteClusterSecrets replaces both cluster.api_key and
// proxy_link.shared_secret with the supplied values regardless of current
// state. Used by the explicit "regenerate-secrets" action — the patcher
// can't simply overwrite non-empty keys, so we run a separate code path.
func forceRewriteClusterSecrets(apiKey, sharedSecret string) error {
	if !serverReady() {
		return errors.New("server not initialised")
	}
	root := adminhttp.ConfigRepoRoot()
	tomlPath := config.TOMLFilePath(root)
	data, err := os.ReadFile(tomlPath)
	if err != nil {
		return fmt.Errorf("read config.toml: %w", err)
	}
	patched := replaceClusterSecretValue(string(data), "cluster", "api_key", apiKey)
	patched = replaceClusterSecretValue(patched, "proxy_link", "shared_secret", sharedSecret)
	if _, err := adminhttp.CreateConfigBackup(data); err != nil {
		log.Warn().Err(err).Msg("cluster: backup of config.toml before regen failed")
	}
	if err := os.WriteFile(tomlPath, []byte(patched), 0o644); err != nil {
		return fmt.Errorf("write config.toml: %w", err)
	}
	log.Warn().Msg("cluster: secrets regenerated — all peer nodes must update their config")
	return reloadServer()
}

// replaceClusterSecretValue rewrites `key = "..."` inside [section]. If the
// key is missing, it inserts; if the section is missing, it appends. Unlike
// patchTOMLKey it always replaces, even when the existing value is non-empty.
func replaceClusterSecretValue(src, section, key, value string) string {
	sectionRe := regexp.MustCompile(`(?m)^\[` + regexp.QuoteMeta(section) + `\]\s*$`)
	sectionLoc := sectionRe.FindStringIndex(src)
	if sectionLoc == nil {
		if !strings.HasSuffix(src, "\n") {
			src += "\n"
		}
		src += fmt.Sprintf("\n[%s]\n%s = %q\n", section, key, value)
		return src
	}
	bodyStart := sectionLoc[1]
	rest := src[bodyStart:]
	nextSection := regexp.MustCompile(`(?m)^\[`).FindStringIndex(rest)
	bodyEnd := len(src)
	if nextSection != nil {
		bodyEnd = bodyStart + nextSection[0]
	}
	body := src[bodyStart:bodyEnd]

	keyRe := regexp.MustCompile(`(?m)^(\s*)` + regexp.QuoteMeta(key) + `\s*=\s*"(.*?)"`)
	if m := keyRe.FindStringSubmatchIndex(body); m != nil {
		newBody := body[:m[4]] + value + body[m[5]:]
		return src[:bodyStart] + newBody + src[bodyEnd:]
	}
	insertion := fmt.Sprintf("%s = %q\n", key, value)
	return src[:bodyStart] + "\n" + insertion + src[bodyStart:]
}
