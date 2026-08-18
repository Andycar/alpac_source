package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestErrorAccsdbHandler(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/e/acb", nil)
	errorAccsdbHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Настройка AccsDB") {
		t.Fatalf("missing page title: %s", body)
	}
	if !strings.Contains(body, "shared_passwd") {
		t.Fatalf("missing shared_passwd block: %s", body)
	}
	if !strings.Contains(body, "http://lampac.local/admin") {
		t.Fatalf("missing host replacement: %s", body)
	}
}

func TestRandomLowerAlnum(t *testing.T) {
	out := randomLowerAlnum(16)
	if len(out) != 16 {
		t.Fatalf("unexpected len: %d", len(out))
	}
	for _, ch := range out {
		if !(ch >= 'a' && ch <= 'z') && !(ch >= '0' && ch <= '9') {
			t.Fatalf("unexpected char %q in %q", ch, out)
		}
	}
}
