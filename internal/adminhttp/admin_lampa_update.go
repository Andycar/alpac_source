package adminhttp

import (
	stdjson "encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/tgauth"

	"github.com/rs/zerolog/log"
)

// ---------------------------------------------------------------------------
// Lampa OTA update — check upstream version + download from GitHub
// ---------------------------------------------------------------------------

const (
	lampaRepo         = "yumata/lampa"
	lampaBranch       = "main"
	lampaRawBaseURL   = "https://raw.githubusercontent.com/" + lampaRepo + "/" + lampaBranch + "/"
	lampaTreeAPIURL   = "https://api.github.com/repos/" + lampaRepo + "/git/trees/" + lampaBranch + "?recursive=1"
	lampaCommitAPIURL = "https://api.github.com/repos/" + lampaRepo + "/commits/" + lampaBranch
	lampaUA           = "lampac-go/1.0"
)

// protectedPaths are files/dirs that must NOT be overwritten during update.
var protectedPaths = map[string]bool{
	"personal.lampa":          true,
	"plugins/modification.js": true,
	"plugins_black_list.json": true,
	"img/welcome.jpg":         true,
}

// protectedPrefixes are directory prefixes that must NOT be overwritten.
var protectedPrefixes = []string{
	"img/bokeh/",
	"img/bokeh-h/",
	"img/ili/",
	"plugins/",
	"vender/",
}

func isProtectedPath(rel string) bool {
	if protectedPaths[rel] {
		return true
	}
	for _, pfx := range protectedPrefixes {
		if strings.HasPrefix(rel, pfx) {
			return true
		}
	}
	return false
}

// lampaAssembly mirrors the upstream assembly.json structure.
type lampaAssembly struct {
	AppVersion string `json:"app_version"`
	CSSVersion string `json:"css_version"`
	CSSDigital int    `json:"css_digital"`
	AppDigital int    `json:"app_digital"`
	Time       int64  `json:"time"`
	Hash       string `json:"hash"`
}

type lampaVersionInfo struct {
	Version string `json:"version"`
	Digital int    `json:"digital"`
	Hash    string `json:"hash"`
	Commit  string `json:"commit,omitempty"`
}

// lampaUpdateMu serializes update operations.
var lampaUpdateMu sync.Mutex

// ---------------------------------------------------------------------------
// GET /{adminPath}/api/lampa/version
// ---------------------------------------------------------------------------

func tgAdminLampaVersionHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, ok := tgAdminAuthCheck(w, r, store, adminStore)
		if !ok {
			return
		}

		wwwDir := lampaWWWDir()
		if wwwDir == "" {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "wwwroot not found"})
			return
		}

		local := readLocalAssembly(wwwDir)
		localCommit := readLocalTreeCommit(wwwDir)

		remote, remoteCommit, err := fetchRemoteVersion()
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"local":            lampaVersionInfo{Version: local.AppVersion, Digital: local.AppDigital, Hash: local.Hash, Commit: localCommit},
				"remote":           nil,
				"update_available": false,
				"error":            err.Error(),
			})
			return
		}

		updateAvailable := remote.AppDigital > local.AppDigital || (remote.AppDigital == local.AppDigital && remote.Hash != local.Hash)

		writeJSON(w, http.StatusOK, map[string]any{
			"local":            lampaVersionInfo{Version: local.AppVersion, Digital: local.AppDigital, Hash: local.Hash, Commit: localCommit},
			"remote":           lampaVersionInfo{Version: remote.AppVersion, Digital: remote.AppDigital, Hash: remote.Hash, Commit: remoteCommit},
			"update_available": updateAvailable,
		})
	}
}

// ---------------------------------------------------------------------------
// POST /{adminPath}/api/lampa/update
// ---------------------------------------------------------------------------

func tgAdminLampaUpdateHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, isSuper := tgAdminAuthCheck(w, r, store, adminStore)
		if !isSuper {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "super admin required"})
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "POST required"})
			return
		}

		wwwDir := lampaWWWDir()
		if wwwDir == "" {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "wwwroot not found"})
			return
		}

		if !lampaUpdateMu.TryLock() {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "update already in progress"})
			return
		}
		defer lampaUpdateMu.Unlock()

		oldAssembly := readLocalAssembly(wwwDir)
		full := strings.TrimSpace(r.URL.Query().Get("full")) == "true"

		var updated []string
		var commitSHA string
		var err error

		if full {
			updated, commitSHA, err = lampaFullSync(wwwDir)
		} else {
			updated, commitSHA, err = lampaQuickUpdate(wwwDir)
		}

		if err != nil {
			log.Error().Err(err).Msg("lampa update failed")
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}

		// Write commit SHA to tree file.
		if commitSHA != "" {
			_ = os.WriteFile(filepath.Join(wwwDir, "tree"), []byte(commitSHA), 0o644)
		}

		newAssembly := readLocalAssembly(wwwDir)

		log.Info().
			Str("old", oldAssembly.AppVersion).
			Str("new", newAssembly.AppVersion).
			Int("files", len(updated)).
			Bool("full", full).
			Msg("lampa updated")

		writeJSON(w, http.StatusOK, map[string]any{
			"ok":            true,
			"old_version":   oldAssembly.AppVersion,
			"new_version":   newAssembly.AppVersion,
			"files_updated": updated,
		})
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// lampaGitHubGet performs a GET request with User-Agent and Accept headers.
func lampaGitHubGet(client *http.Client, url, accept string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", lampaUA)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	return client.Do(req)
}

func lampaWWWDir() string {
	if !serverReady() {
		return ""
	}
	cfg := liveConfig(config.Config{})
	// Use the same resolution as the rest of the app.
	lampaType := "lampa-main"
	candidates := []string{
		filepath.Join(cfg.Compat.RepoRoot, "wwwroot", lampaType),
		filepath.Join("wwwroot", lampaType),
		filepath.Join("/home/wwwroot", lampaType),
	}
	for _, d := range candidates {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			return d
		}
	}
	return ""
}

func readLocalAssembly(wwwDir string) lampaAssembly {
	data, err := os.ReadFile(filepath.Join(wwwDir, "assembly.json"))
	if err != nil {
		return lampaAssembly{}
	}
	var a lampaAssembly
	_ = stdjson.Unmarshal(data, &a)
	return a
}

func readLocalTreeCommit(wwwDir string) string {
	data, err := os.ReadFile(filepath.Join(wwwDir, "tree"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func fetchRemoteVersion() (lampaAssembly, string, error) {
	client := httpclient.New(15 * time.Second)

	// Fetch assembly.json
	resp, err := lampaGitHubGet(client, lampaRawBaseURL+"assembly.json", "")
	if err != nil {
		return lampaAssembly{}, "", fmt.Errorf("fetch assembly.json: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return lampaAssembly{}, "", fmt.Errorf("assembly.json: HTTP %d — %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return lampaAssembly{}, "", fmt.Errorf("read assembly.json: %w", err)
	}
	var a lampaAssembly
	if err := stdjson.Unmarshal(body, &a); err != nil {
		return lampaAssembly{}, "", fmt.Errorf("parse assembly.json: %w", err)
	}

	// Fetch latest commit SHA.
	commitSHA := fetchLatestCommitSHA(client)

	return a, commitSHA, nil
}

func fetchLatestCommitSHA(client *http.Client) string {
	resp, err := lampaGitHubGet(client, lampaCommitAPIURL, "application/vnd.github.sha")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return ""
	}
	body, _ := io.ReadAll(resp.Body)
	return strings.TrimSpace(string(body))
}

// lampaQuickUpdate downloads the core files: assembly.json, app.min.js, css/app.css, index.html.
func lampaQuickUpdate(wwwDir string) ([]string, string, error) {
	client := httpclient.New(120 * time.Second)
	files := []string{"assembly.json", "app.min.js", "css/app.css", "index.html"}

	var updated []string
	for _, f := range files {
		if err := downloadAndSave(client, wwwDir, f); err != nil {
			return updated, "", fmt.Errorf("download %s: %w", f, err)
		}
		updated = append(updated, f)
	}

	// Post-process: inject lampainit.js into index.html.
	injectLampaInit(filepath.Join(wwwDir, "index.html"))

	commitSHA := fetchLatestCommitSHA(httpclient.New(15 * time.Second))
	return updated, commitSHA, nil
}

// lampaFullSync uses GitHub tree API to sync all files.
func lampaFullSync(wwwDir string) ([]string, string, error) {
	client := httpclient.New(30 * time.Second)

	resp, err := lampaGitHubGet(client, lampaTreeAPIURL, "application/json")
	if err != nil {
		return nil, "", fmt.Errorf("fetch tree: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, "", fmt.Errorf("tree API: HTTP %d — %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var tree struct {
		SHA  string `json:"sha"`
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
			Size int    `json:"size"`
		} `json:"tree"`
	}
	if err := stdjson.NewDecoder(resp.Body).Decode(&tree); err != nil {
		return nil, "", fmt.Errorf("parse tree: %w", err)
	}

	// Get commit SHA.
	commitSHA := fetchLatestCommitSHA(httpclient.New(15 * time.Second))

	// Filter to actual files (not dirs, not protected, not infrastructure).
	dlClient := httpclient.New(120 * time.Second)
	var updated []string
	skipPrefixes := []string{".docker", ".git"}
	infraFiles := map[string]bool{
		"Dockerfile": true, ".dockerignore": true,
		"LICENSE": true, "README.md": true, "SECURITY.md": true,
	}

	for _, entry := range tree.Tree {
		if entry.Type != "blob" {
			continue
		}
		if infraFiles[entry.Path] {
			continue
		}
		skip := false
		for _, pfx := range skipPrefixes {
			if strings.HasPrefix(entry.Path, pfx) {
				skip = true
				break
			}
		}
		if skip || isProtectedPath(entry.Path) {
			continue
		}

		if err := downloadAndSave(dlClient, wwwDir, entry.Path); err != nil {
			log.Warn().Err(err).Str("file", entry.Path).Msg("lampa full sync: skip file")
			continue
		}
		updated = append(updated, entry.Path)
	}

	// Post-process: inject lampainit.js into index.html.
	injectLampaInit(filepath.Join(wwwDir, "index.html"))

	return updated, commitSHA, nil
}

func downloadAndSave(client *http.Client, wwwDir, relPath string) error {
	resp, err := lampaGitHubGet(client, lampaRawBaseURL+relPath, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("HTTP %d — %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	target := filepath.Join(wwwDir, filepath.FromSlash(relPath))
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, data, 0o644)
}

// injectLampaInit adds <script src="/lampainit.js"></script> before </body>
// if not already present.
func injectLampaInit(indexPath string) {
	data, err := os.ReadFile(indexPath)
	if err != nil {
		return
	}
	content := string(data)
	tag := `<script src="/lampainit.js"></script>`
	if strings.Contains(content, "lampainit.js") {
		return
	}
	content = strings.Replace(content, "</body>", tag+"\n</body>", 1)
	_ = os.WriteFile(indexPath, []byte(content), 0o644)
}
