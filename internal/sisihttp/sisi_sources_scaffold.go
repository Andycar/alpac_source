package sisihttp

import (
	"net/http"
	"strings"

	"lampac-go/internal/config"
)

func sisiListStubHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if parseBoolParam(r.URL.Query().Get("checksearch")) {
			writeRawJSON(w, []byte(`{"rch":false}`))
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"menu":        []any{},
			"list":        []any{},
			"total_pages": 1,
		})
	}
}

func sisiStarsStubHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"list":        []any{},
			"total_pages": 1,
		})
	}
}

func sisiViewStubHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if parseBoolParam(r.URL.Query().Get("checksearch")) {
			writeRawJSON(w, []byte(`{"rch":false}`))
			return
		}
		if parseBoolParam(r.URL.Query().Get("related")) {
			writeJSON(w, http.StatusOK, map[string]any{
				"list":        []any{},
				"total_pages": 1,
			})
			return
		}

		path := strings.ToLower(strings.TrimSpace(r.URL.Path))
		if strings.HasSuffix(path, ".m3u8") || strings.HasSuffix(path, "/potok") {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("#EXTM3U\n"))
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"qualitys":  []any{},
			"recomends": []any{},
		})
	}
}
