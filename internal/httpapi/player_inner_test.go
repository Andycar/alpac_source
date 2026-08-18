package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestPlayerInnerHandler(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", root)

	if err := os.WriteFile(filepath.Join(root, "init.conf"), []byte(`{"playerInner":"/bin/echo"}`), 0o644); err != nil {
		t.Fatalf("write init.conf: %v", err)
	}

	router := chi.NewRouter()
	router.Get("/player-inner/*", playerInnerHandler())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/player-inner/https://example.com/video?id=1", nil)
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPlayerInnerInvalidInput(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LAMPAC_GO_HOME", root)

	if err := os.WriteFile(filepath.Join(root, "init.conf"), []byte(`{"playerInner":"/bin/echo"}`), 0o644); err != nil {
		t.Fatalf("write init.conf: %v", err)
	}

	router := chi.NewRouter()
	router.Get("/player-inner/*", playerInnerHandler())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/player-inner/javascript:alert(1)", nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status for invalid input: %d", rec.Code)
	}
}
