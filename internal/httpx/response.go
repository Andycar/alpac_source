// Package httpx holds the leaf HTTP response helpers shared across the server.
// It depends on nothing else in the codebase, so any package (handlers that
// leave the internal/httpapi monolith, sub-packages, tools) can import it
// without pulling in the monolith. See internal/httpapi/ARCHITECTURE.md.
package httpx

import (
	"net/http"

	jsoniter "github.com/json-iterator/go"
)

// json mirrors internal/httpapi's package-level encoder
// (jsoniter.ConfigCompatibleWithStandardLibrary) so responses written through
// httpx are byte-for-byte identical to those written by the monolith.
var json = jsoniter.ConfigCompatibleWithStandardLibrary

// WriteJSON writes payload as a JSON response with the given status code.
func WriteJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(payload)
}

// WriteHTML writes an HTML response with the given status code.
func WriteHTML(w http.ResponseWriter, code int, html string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(html))
}
