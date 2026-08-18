package opensubs

import (
	"net/http"

	"lampac-go/internal/httpx"

	"github.com/go-chi/chi/v5"
	jsoniter "github.com/json-iterator/go"
)

// deps.go — the thin seam this extracted handler package needs from its former
// host (httpapi). All trivial forwarders, so the moved handlers compile unchanged.

// writeJSON forwards to the shared httpx encoder (byte-identical output).
func writeJSON(w http.ResponseWriter, code int, payload any) {
	httpx.WriteJSON(w, code, payload)
}

// extractPathParam reads a chi URL path parameter (mirrors httpapi's helper).
func extractPathParam(r *http.Request, name string) string {
	return chi.URLParam(r, name)
}

// json mirrors httpapi's package-level jsoniter alias.
var json = jsoniter.ConfigCompatibleWithStandardLibrary
