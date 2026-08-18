package litesrc

import (
	"context"
	_ "embed"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/proxylink"

	"github.com/rs/zerolog/log"
)

// json (jsoniter, ConfigCompatibleWithStandardLibrary) is the package-level encoder declared in server.go.

// ---------------------------------------------------------------------------
//  YouTube voice-over-translation (VOT) — runs an embedded Node sidecar (vot.js).
//
//  The public FOSWLY vot-backend does NOT translate YouTube (it's for other sites); YouTube VOT is the
//  Yandex protocol that lives inside vot.js. So we bundle vot.js into one self-contained CommonJS file
//  (votjs/vot.bundle.cjs, see votjs/build.sh), embed it here, and run it as `node vot.cjs <id> <from>
//  <to>`. The script prints one JSON line: {ok,status:"success"|"waiting"|"error",url?,remainingTime?}.
//
//  GET /lite/youtube/vot?videoID=<id>[&lang=en]
//    → {status, audio (proxied mp3), remainingTime}. "waiting" → client polls again (bounded loop).
//  The translated mp3 (a signed Yandex S3 url) is proxied through /proxy so the web player fetches it
//  same-origin (avoids CORS / geo-block).
// ---------------------------------------------------------------------------

//go:embed votjs/vot.bundle.cjs
var votBundleJS []byte

var (
	votScriptOnce sync.Once
	votScriptPath string
	votScriptErr  error
)

// votScript materializes the embedded sidecar to a stable temp path once and returns it, rewriting it
// if /tmp got swept out from under us.
func votScript() (string, error) {
	votScriptOnce.Do(func() {
		p := filepath.Join(os.TempDir(), "lampac-go-vot.cjs")
		if err := os.WriteFile(p, votBundleJS, 0o644); err != nil {
			votScriptErr = err
			return
		}
		votScriptPath = p
	})
	if votScriptErr != nil {
		return "", votScriptErr
	}
	if _, err := os.Stat(votScriptPath); err != nil {
		if werr := os.WriteFile(votScriptPath, votBundleJS, 0o644); werr != nil {
			return "", werr
		}
	}
	return votScriptPath, nil
}

// votNodeBin resolves the node binary: explicit config, then PATH, then a few common locations.
func votNodeBin(cfg config.Config) string {
	if p := strings.TrimSpace(cfg.YouTube.VotNodePath); p != "" {
		return p
	}
	if p, err := exec.LookPath("node"); err == nil {
		return p
	}
	for _, c := range []string{"/usr/local/bin/node", "/usr/bin/node", "/opt/homebrew/bin/node"} {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

func YtVotHandler(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	enabled := cfg.YouTube.VotEnabled()
	node := ""
	if enabled {
		node = votNodeBin(cfg)
	}
	worker := strings.TrimSpace(cfg.YouTube.VotWorker)
	if !enabled || node == "" {
		log.Info().Bool("enabled", enabled).Bool("node_found", node != "").Msg("youtube vot: disabled")
	}

	return func(w http.ResponseWriter, r *http.Request) {
		if !enabled || node == "" {
			writeJSON(w, http.StatusOK, map[string]any{"status": "disabled"})
			return
		}

		videoID := strings.TrimSpace(r.URL.Query().Get("videoID"))
		if videoID == "" {
			videoID = strings.TrimSpace(r.URL.Query().Get("videoId"))
		}
		if !isYouTubeVideoID(videoID) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "videoID required"})
			return
		}

		fromLang := strings.TrimSpace(cfg.YouTube.VotLang)
		if fromLang == "" {
			fromLang = "en"
		}
		if l := strings.TrimSpace(r.URL.Query().Get("lang")); l != "" && isLangCode(l) {
			fromLang = l
		}
		toLang := strings.TrimSpace(cfg.YouTube.VotToLang)
		if toLang == "" {
			toLang = "ru"
		}

		script, err := votScript()
		if err != nil {
			log.Warn().Err(err).Msg("youtube vot: cannot materialize sidecar script")
			writeJSON(w, http.StatusOK, map[string]any{"status": "error"})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, node, script, videoID, fromLang, toLang)
		cmd.Env = os.Environ()
		if worker != "" {
			cmd.Env = append(cmd.Env, "VOT_WORKER_HOST="+worker)
		}
		out, err := cmd.Output() // stdout = one JSON line; node exits 0 even on a handled error
		if err != nil {
			stderr := ""
			if ee, ok := err.(*exec.ExitError); ok {
				stderr = strings.TrimSpace(string(ee.Stderr))
			}
			log.Warn().Err(err).Str("stderr", stderr).Msg("youtube vot: node sidecar failed")
			writeJSON(w, http.StatusOK, map[string]any{"status": "error"})
			return
		}

		var sr struct {
			OK            bool    `json:"ok"`
			Status        string  `json:"status"`
			URL           string  `json:"url"`
			RemainingTime float64 `json:"remainingTime"`
			Error         string  `json:"error"`
		}
		if jerr := json.Unmarshal(out, &sr); jerr != nil {
			log.Warn().Err(jerr).Str("out", truncStr(string(out), 300)).Msg("youtube vot: bad sidecar output")
			writeJSON(w, http.StatusOK, map[string]any{"status": "error"})
			return
		}

		resp := map[string]any{}
		switch {
		case sr.Status == "success" && sr.URL != "":
			resp["status"] = "success"
			resp["audio"] = votProxyAudio(sr.URL, r, links)
		case sr.Status == "error":
			resp["status"] = "error"
		default: // "waiting" (or anything unrecognized) → the client polls again
			resp["status"] = "waiting"
			if sr.RemainingTime > 0 {
				resp["remainingTime"] = int(sr.RemainingTime)
			}
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// votProxyAudio proxies the translated mp3 through /proxy so the web player fetches it same-origin
// (avoids CORS / geo-block). Falls back to the raw URL if proxy minting is unavailable.
func votProxyAudio(audio string, r *http.Request, links *proxylink.Manager) string {
	if links == nil {
		return audio
	}
	if tok := links.EncryptURI(audio, clientIP(r), "youtube", false, false, false); tok != "" {
		return hostFromRequest(r) + "/proxy/" + tok
	}
	return audio
}

// isYouTubeVideoID reports whether s is a plausible YouTube video id (the only chars YouTube uses).
func isYouTubeVideoID(s string) bool {
	if s == "" || len(s) > 20 {
		return false
	}
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// isLangCode is a light guard for the ?lang= override (2–5 letters, optional -REGION).
func isLangCode(s string) bool {
	if len(s) < 2 || len(s) > 5 {
		return false
	}
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '-') {
			return false
		}
	}
	return true
}

func truncStr(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
