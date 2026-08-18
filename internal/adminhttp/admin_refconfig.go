package adminhttp

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/tgauth"
)

// json is a package-level alias defined in server.go (jsoniter).

// ---------------------------------------------------------------------------
// Reference config proxy — fetches the curated TOML blob from alcopa-site
// (`GET {server_url}/api/v1/config/reference`) and, on explicit admin
// confirmation, writes it into config.reference.toml next to config.toml.
//
// The admin UI shows a diff against the current reference so the operator
// knows what changed before applying. The reference file is NOT auto-merged
// into the running config — merging is a separate step handled by the
// operator (copy the interesting sections by hand, or we extend the flow
// later).
//
// Routes:
//	GET  /{admin}/api/refconfig          — current reference (from server)
//	POST /{admin}/api/refconfig/save     — persist last-fetched body to disk
// ---------------------------------------------------------------------------

// referenceResponse mirrors the server's JSON response.
type referenceResponse struct {
	Body      string    `json:"body"`
	Version   int64     `json:"version"`
	Note      string    `json:"note"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by"`
}

func tgAdminRefConfigFetchHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, isSuper := tgAdminAuthCheck(w, r, store, adminStore); !isSuper {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "super admin required"})
			return
		}
		svc := liveUpdater()
		if svc == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "updater not initialized"})
			return
		}
		cfg := svc.Config()
		if cfg.ServerURL == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "server_url not configured"})
			return
		}

		body, err := fetchReference(cfg.ServerURL, cfg.ServerToken)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}

		local, _ := readLocalReference()
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":         true,
			"server":     body,
			"local_body": local.Body,
			"local_path": localReferencePath(),
		})
	}
}

func tgAdminRefConfigSaveHandler(store *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, isSuper := tgAdminAuthCheck(w, r, store, adminStore); !isSuper {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "super admin required"})
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "POST required"})
			return
		}
		var body struct {
			Body string `json:"body"`
			Note string `json:"note"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
			return
		}
		if err := writeLocalReference(body.Body); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":   true,
			"path": localReferencePath(),
		})
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func fetchReference(baseURL, token string) (*referenceResponse, error) {
	url := strings.TrimRight(baseURL, "/") + "/api/v1/config/reference"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "lampac-go-refconfig")
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("server %s: HTTP %d — %s", url, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out referenceResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// localReferencePath returns the absolute path where we persist the
// last-pulled reference config, so operators can diff it with their
// real config.toml or merge interesting sections by hand.
func localReferencePath() string {
	root := "."
	if serverReady() {
		if cfg := liveConfig(config.Config{}); cfg.Compat.RepoRoot != "" {
			root = cfg.Compat.RepoRoot
		}
	}
	// Live next to config.toml so `diff` is one command away.
	return filepath.Join(filepath.Dir(config.TOMLFilePath(root)), "config.reference.toml")
}

func readLocalReference() (struct{ Body string }, error) {
	p := localReferencePath()
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return struct{ Body string }{}, nil
		}
		return struct{ Body string }{}, err
	}
	return struct{ Body string }{Body: string(data)}, nil
}

func writeLocalReference(body string) error {
	p := localReferencePath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
