package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestEngBaseChecksearchReturnsDataJSON(t *testing.T) {
	cfg := config.Config{}

	sources := []string{
		"vidsrc",
		"hydraflix",
		"movpi",
		"vidlink",
		"videasy",
		"smashystream",
		"autoembed",
		"playembed",
		"twoembed",
		"rgshows",
	}

	for _, src := range sources {
		t.Run(src, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/"+src+"?checksearch=true&id=550&serial=0", nil)
			authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("unexpected status: %d", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "data-json=") {
				t.Fatalf("unexpected body: %s", rec.Body.String())
			}
		})
	}
}
