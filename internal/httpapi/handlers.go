package httpapi

import (
	"net/http"
	"sort"
	"time"

	"lampac-go/internal/httpx"
	"lampac-go/internal/modules"
)

type LocalRouteSpec struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"time":   time.Now().UTC().Format(time.RFC3339),
	})
}

func readyHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "ok",
			"mode":   "local",
			"time":   time.Now().UTC().Format(time.RFC3339),
		})
	}
}

func versionHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(publicVersionText()))
}

func pingHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("pong"))
}

func modulesHandler(manifest []modules.RootModule) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"total":   len(manifest),
			"modules": manifest,
		})
	}
}

func localRoutesHandler(routes []LocalRouteSpec) http.HandlerFunc {
	// Return stable order for deterministic tooling output.
	out := make([]LocalRouteSpec, len(routes))
	copy(out, routes)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Method < out[j].Method
	})

	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"total":  len(out),
			"routes": out,
		})
	}
}

// writeJSON forwards to httpx.WriteJSON — the canonical response helper (see
// internal/httpx). Kept as a thin package-local wrapper so the ~1800 existing
// call sites don't need touching; new/extracted code should call httpx directly.
func writeJSON(w http.ResponseWriter, code int, payload any) {
	httpx.WriteJSON(w, code, payload)
}
