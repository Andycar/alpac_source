package litesrc

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestGetsTVBindHandlerForm(t *testing.T) {
	cfg := config.Config{Online: config.OnlineConfig{GetsTV: config.HostTokenSource{Host: "https://getstv.com"}}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/getstv/bind", nil)

	GetsTVBindHandler(cfg).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Введите данные аккаунта getstv.com") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestGetsTVBindHandlerToken(t *testing.T) {
	var received map[string]any
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/login" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if err := stdjson.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"token-123"}`))
	}))
	defer api.Close()

	cfg := config.Config{Online: config.OnlineConfig{GetsTV: config.HostTokenSource{Host: api.URL}}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/getstv/bind?login=user@example.com&pass=secret", nil)

	GetsTVBindHandler(cfg).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if strings.TrimSpace(toString(received["email"])) != "user@example.com" {
		t.Fatalf("unexpected request email: %+v", received)
	}
	if strings.TrimSpace(toString(received["password"])) != "secret" {
		t.Fatalf("unexpected request password: %+v", received)
	}
	if strings.TrimSpace(toString(received["fingerprint"])) == "" {
		t.Fatalf("fingerprint is empty: %+v", received)
	}
	if !strings.Contains(rec.Body.String(), `"token": "token-123"`) && !strings.Contains(rec.Body.String(), `"token":"token-123"`) {
		t.Fatalf("token not reflected in response: %s", rec.Body.String())
	}
}
