package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

// TestAuthRequiredLiteHandlerReturnsAccsdb verifies the synthetic
// /lite/_auth_required path always returns the accsdb-style banner JSON.
// Used by Lampa when probing the synthetic balancer emitted by
// /lite/events on unauth requests.
func TestAuthRequiredLiteHandlerReturnsAccsdb(t *testing.T) {
	cfg := config.Config{}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/_auth_required?checksearch=true&id=550", nil)
	liteSourceHandler(cfg, nil, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200", rec.Code)
	}
	var resp map[string]any
	if err := stdjson.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("invalid json: %v body=%s", err, rec.Body.String())
	}
	// accsdbResponse shape: {accsdb, results, msg, ...}.
	if resp["accsdb"] != true {
		t.Fatalf("expected accsdb=true, got: %v", resp)
	}
	msg, _ := resp["msg"].(string)
	if !strings.Contains(strings.ToLower(msg), "авторизаци") {
		t.Fatalf("msg=%q want contains 'авторизаци'", msg)
	}
}
