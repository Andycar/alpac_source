package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"lampac-go/internal/auth"
)

func TestApiAuthWhoamiUnauthenticated(t *testing.T) {
	h := apiAuthWhoamiHandler(func() string { return "tg" })
	req := httptest.NewRequest(http.MethodGet, "/api/auth/whoami", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var resp map[string]any
	_ = stdjson.NewDecoder(rec.Body).Decode(&resp)
	if resp["authenticated"] != false {
		t.Fatalf("authenticated=%v want false", resp["authenticated"])
	}
	if resp["mode"] != "tg" {
		t.Fatalf("mode=%v want tg", resp["mode"])
	}
}

func TestApiAuthWhoamiTGUser(t *testing.T) {
	h := apiAuthWhoamiHandler(func() string { return "tg" })
	exp := time.Now().Add(24 * time.Hour).UTC()
	req := authedTestRequest(httptest.NewRequest(http.MethodGet, "/api/auth/whoami", nil))
	// Override the test user with a TG-shaped ID.
	ctx := auth.WithUser(req.Context(), &auth.User{
		ID:      "tg:12345",
		Aliases: []string{"@alice"},
		Expires: exp,
	})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var resp map[string]any
	_ = stdjson.NewDecoder(rec.Body).Decode(&resp)
	if resp["authenticated"] != true || resp["type"] != "tg" {
		t.Fatalf("unexpected: %+v", resp)
	}
	if resp["uid"] != "12345" || resp["username"] != "@alice" {
		t.Fatalf("derived fields: %+v", resp)
	}
}

func TestApiAuthWhoamiPasswordUser(t *testing.T) {
	h := apiAuthWhoamiHandler(func() string { return "password" })
	exp := time.Now().Add(48 * time.Hour).UTC()
	req := httptest.NewRequest(http.MethodGet, "/api/auth/whoami", nil)
	ctx := auth.WithUser(req.Context(), &auth.User{
		ID:      "pw:deadbeef",
		Aliases: []string{"alice"},
		Expires: exp,
	})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var resp map[string]any
	_ = stdjson.NewDecoder(rec.Body).Decode(&resp)
	if resp["type"] != "password" || resp["uid"] != "deadbeef" || resp["username"] != "alice" {
		t.Fatalf("password derivation: %+v", resp)
	}
	if dl, _ := resp["days_left"].(float64); dl < 1 || dl > 2 {
		t.Fatalf("days_left=%v want ~1-2", resp["days_left"])
	}
}

func TestApiAuthWhoamiAnonUser(t *testing.T) {
	h := apiAuthWhoamiHandler(func() string { return "none" })
	req := httptest.NewRequest(http.MethodGet, "/api/auth/whoami", nil)
	ctx := auth.WithUser(req.Context(), &auth.User{
		ID:        "anon:cafebabe",
		Anonymous: true,
		Expires:   time.Now().Add(time.Hour),
	})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var resp map[string]any
	_ = stdjson.NewDecoder(rec.Body).Decode(&resp)
	if resp["type"] != "anon" || resp["uid"] != "cafebabe" || resp["mode"] != "none" {
		t.Fatalf("anon shape: %+v", resp)
	}
}
