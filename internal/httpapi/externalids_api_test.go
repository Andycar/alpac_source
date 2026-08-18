package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExternalIDsLocalLookup(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	payload := `{"tt123":"100","tt456":"200"}`
	if err := os.WriteFile(filepath.Join(dataDir, "externalids.json"), []byte(payload), 0o644); err != nil {
		t.Fatalf("write externalids: %v", err)
	}

	// reset cache between tests
	externalIDsCache.mu.Lock()
	externalIDsCache.items = map[string]string{}
	externalIDsCache.lastCheck = timeZero()
	externalIDsCache.lastWriteTime = timeZero()
	externalIDsCache.mu.Unlock()

	rec1 := httptest.NewRecorder()
	req1 := httptest.NewRequest(http.MethodGet, "http://lampac.local/externalids?imdb_id=tt123", nil)
	externalIDsHandler(root).ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec1.Code)
	}
	if !strings.Contains(rec1.Body.String(), `"kinopoisk_id":"100"`) {
		t.Fatalf("unexpected body: %s", rec1.Body.String())
	}

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "http://lampac.local/externalids?id=KP_200", nil)
	externalIDsHandler(root).ServeHTTP(rec2, req2)
	if !strings.Contains(rec2.Body.String(), `"imdb_id":"tt456"`) {
		t.Fatalf("unexpected kp lookup body: %s", rec2.Body.String())
	}
}

func TestExternalIDsNumericIDWithoutLegacy(t *testing.T) {
	root := t.TempDir()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/externalids?id=12345&serial=1", nil)
	externalIDsHandler(root).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	// no local mapping and no legacy fallback -> empty payload
	if !strings.Contains(rec.Body.String(), `"imdb_id":""`) {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func timeZero() time.Time {
	return time.Time{}
}
