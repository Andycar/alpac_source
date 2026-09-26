package proxyapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Матрица netdiag 2026-09-08: edge-a.example.org не открывается у российских операторов (обрыв на TLS),
// но за пределами РФ даёт 100 %. Значит ссылку на неё нельзя выдавать зрителю из RU, а всем
// остальным — можно, и снимать ноду целиком нельзя.
func withEdges(t *testing.T, hosts []string, deny map[string][]string, country func(string) string) {
	t.Helper()
	configureEdges(hosts, []string{"*"}, nil)
	configureEdgeGeo(deny)
	SetGeoLookup(country)
	t.Cleanup(func() {
		configureEdges(nil, nil, nil)
		configureEdgeGeo(nil)
		SetGeoLookup(nil)
		resetEdgeBounces()
	})
}

func reqFrom(ip string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/proxy/tok", nil)
	r.Header.Set("X-Real-IP", ip)
	return r
}

func TestPickEdgeGeoDeny(t *testing.T) {
	hosts := []string{"https://edge-a.example.org", "https://edge-b.example.net:2053"}
	deny := map[string][]string{"https://edge-a.example.org": {"RU"}}
	countries := map[string]string{"1.1.1.1": "RU", "2.2.2.2": "UA", "3.3.3.3": ""}
	withEdges(t, hosts, deny, func(ip string) string { return countries[ip] })

	// зритель из RU не должен получить edge-a.example.org ни при каком токене
	for _, tok := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		if got := pickEdge("filmix", tok, reqFrom("1.1.1.1"), ""); got == "https://edge-a.example.org" {
			t.Fatalf("токен %q: зрителю из RU выдали закрытую ноду", tok)
		}
	}
	// зритель из UA — edge-a.example.org разрешён, значит по разным токенам он должен встретиться
	seen := map[string]bool{}
	for _, tok := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		seen[pickEdge("filmix", tok, reqFrom("2.2.2.2"), "")] = true
	}
	if !seen["https://edge-a.example.org"] {
		t.Fatal("зрителю из UA edge-a.example.org не выдали ни разу — фильтр слишком широкий")
	}
	// страна неизвестна — ограничений нет (не наказываем за отсутствие базы)
	seen = map[string]bool{}
	for _, tok := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		seen[pickEdge("filmix", tok, reqFrom("3.3.3.3"), "")] = true
	}
	if !seen["https://edge-a.example.org"] {
		t.Fatal("без известной страны нода должна выдаваться как раньше")
	}
}

func TestPickEdgeGeoDenyOverridesHintAndMinter(t *testing.T) {
	hosts := []string{"https://edge-a.example.org", "https://edge-b.example.net:2053"}
	deny := map[string][]string{"https://edge-a.example.org": {"RU"}}
	withEdges(t, hosts, deny, func(ip string) string {
		if ip == "1.1.1.1" {
			return "RU"
		}
		return "DE"
	})

	// просьба клиента не отменяет блокировку его же провайдера
	r := reqFrom("1.1.1.1")
	r.URL.RawQuery = "edge=https%3A%2F%2Fedge-a.example.org"
	if got := pickEdge("filmix", "tok", r, ""); got == "https://edge-a.example.org" {
		t.Fatalf("подсказка edge= пробила гео-запрет: %s", got)
	}
	// ссылка, добытая закрытой нодой: отдавать туда нельзя — зритель её не откроет
	if got := pickEdge("filmix", "tok", reqFrom("1.1.1.1"), "https://edge-a.example.org"); got != "" {
		t.Fatalf("для добывшей, но закрытой ноды ожидали отдачу с main, получили %q", got)
	}
	// у зрителя из DE та же ссылка обязана уйти на добывшую ноду (привязка к IP)
	if got := pickEdge("filmix", "tok", reqFrom("9.9.9.9"), "https://edge-a.example.org"); got != "https://edge-a.example.org" {
		t.Fatalf("вне РФ добывшая нода должна отдавать сама, получили %q", got)
	}
}

func TestPickEdgeAllDeniedFallsBackToMain(t *testing.T) {
	hosts := []string{"https://edge-a.example.org"}
	deny := map[string][]string{"https://edge-a.example.org": {"RU"}}
	withEdges(t, hosts, deny, func(string) string { return "RU" })
	if got := pickEdge("filmix", "tok", reqFrom("1.1.1.1"), ""); got != "" {
		t.Fatalf("все ноды закрыты — ожидали отдачу с main, получили %q", got)
	}
}
