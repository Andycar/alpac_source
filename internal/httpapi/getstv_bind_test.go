package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestGetsTVBindRouteInLiteSource(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"route-token"}`))
	}))
	defer api.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			GetsTV: config.HostTokenSource{Host: api.URL},
		},
	}

	rec := httptest.NewRecorder()
	req := authedTestRequest(httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/getstv/bind?login=user&pass=pass", nil))
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "route-token") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}
