package litesrc

import (
	"net/http"

	"lampac-go/internal/config"
)

// BaseENG parity: in legacy C# checksearch returns `data-json=` for these sources.
type engBaseChecker struct {
	plugin string
}

func NewEngBaseChecker(plugin string) *engBaseChecker {
	return &engBaseChecker{plugin: plugin}
}

func (e *engBaseChecker) Handle(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("data-json="))
			return
		}

		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error":    "eng source full mode is not implemented in local mode",
			"balanser": e.plugin,
		})
	}
}
