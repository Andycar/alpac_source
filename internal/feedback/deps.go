// Package feedback is the user feedback / support-ticket subsystem extracted
// from httpapi: the on-disk FeedbackStore plus the public /api/feedback[/my|/{id}]
// submission endpoints. Admin management lives in internal/adminhttp (wired
// host-side via FeedbackOps closures over this store).
package feedback

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	jsoniter "github.com/json-iterator/go"

	"lampac-go/internal/httpx"
)

// json mirrors httpapi/server.go — the monolith-compatible encoder.
var json = jsoniter.ConfigCompatibleWithStandardLibrary

func writeJSON(w http.ResponseWriter, status int, v any) { httpx.WriteJSON(w, status, v) }

// relToRuntime mirrors httpapi (pure runtime-relative path).
func relToRuntime(rel string) string {
	if root := strings.TrimSpace(os.Getenv("LAMPAC_GO_HOME")); root != "" {
		return filepath.Join(root, rel)
	}
	if root := strings.TrimSpace(os.Getenv("LAMPAC_GO_REPO_ROOT")); root != "" {
		return filepath.Join(root, rel)
	}
	return rel
}
