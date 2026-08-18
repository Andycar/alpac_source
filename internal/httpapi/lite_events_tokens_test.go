package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Регресс: локальная оболочка (webOS/Tizen Home) и store-сборка авторизуются
// ТОЛЬКО заголовком X-Lampac-Token — кук у локального origin нет. Кандидаты
// обязаны видеть заголовки, иначе гейт пускает, а capiAccount/resolveUserGroup
// считают запрос анонимным (профиль «Гость», дефолт-группа режет источники).
func TestCollectLampacTokenCandidates_HeaderOnly(t *testing.T) {
	r := httptest.NewRequest("GET", "/capi/me", nil)
	r.Header.Set("X-Lampac-Token", "tok-header")
	got := collectLampacTokenCandidates(r)
	if len(got) != 1 || got[0] != "tok-header" {
		t.Fatalf("header-only candidates = %v, want [tok-header]", got)
	}
}

func TestCollectLampacTokenCandidates_OrderAndDedup(t *testing.T) {
	r := httptest.NewRequest("GET", "/capi/me?token=tok-query", nil)
	r.AddCookie(&http.Cookie{Name: "alpac_token", Value: "tok-cookie"})
	r.AddCookie(&http.Cookie{Name: "lampac_token", Value: "tok-cookie"}) // дубль схлопывается
	r.Header.Set("X-Alpac-Token", "tok-header")
	got := collectLampacTokenCandidates(r)
	want := []string{"tok-cookie", "tok-header", "tok-query"}
	if len(got) != len(want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("candidates = %v, want %v", got, want)
		}
	}
}
