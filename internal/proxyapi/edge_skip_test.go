package proxyapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Зритель отключил ноду у себя — трафик туда не идёт, берётся следующая.
func TestEdgeSkipExcludesNode(t *testing.T) {
	configureEdges([]string{"http://a:888", "http://b:888"}, []string{"*"}, nil)
	r := httptest.NewRequest("GET", "/proxy/x?edge_skip=http://a:888", nil)

	for _, token := range []string{"t1", "t2", "t3", "t4", "t5"} {
		if got := pickEdge("filmix", token, r, ""); got == "http://a:888" {
			t.Fatalf("токен %q ушёл на отключённую ноду", token)
		}
	}
}

// Отключены все — отдаём сами, а не выбираем «наименее плохую».
func TestEdgeSkipAllFallsBackToPrimary(t *testing.T) {
	configureEdges([]string{"http://a:888", "http://b:888"}, []string{"*"}, nil)
	r := httptest.NewRequest("GET", "/proxy/x?edge_skip=http://a:888,http://b:888", nil)
	if got := pickEdge("filmix", "tok", r, ""); got != "" {
		t.Fatalf("при всех отключённых нодах отдавать должен primary, получили %q", got)
	}
}

// Запрет сильнее просьбы: клиент не должен попасть на ноду, которую сам же
// отключил, даже если она названа в edge=.
func TestEdgeSkipBeatsHint(t *testing.T) {
	configureEdges([]string{"http://a:888", "http://b:888"}, []string{"*"}, nil)
	r := httptest.NewRequest("GET", "/proxy/x?edge=http://a:888&edge_skip=http://a:888", nil)
	if got := pickEdge("filmix", "tok", r, ""); got == "http://a:888" {
		t.Fatal("запрет зрителя должен перебивать его же подсказку")
	}
}

// Ссылку добыла нода — отдаёт она же, что бы ни отключил зритель: у источника
// с привязкой к адресу другого варианта просто нет.
func TestEdgeSkipDoesNotBreakMintedLink(t *testing.T) {
	configureEdges([]string{"http://a:888", "http://b:888"}, []string{"*"}, nil)
	r := httptest.NewRequest("GET", "/proxy/x?edge_skip=http://a:888", nil)
	if got := pickEdge("filmix", "tok", r, "http://a:888"); got != "http://a:888" {
		t.Fatalf("подписанная нодой ссылка должна остаться на ней, получили %q", got)
	}
}

// Запрет обязан доехать до сегментов — плеер ходит по ссылкам из плейлиста.
func TestWithEdgeCarriesBothParams(t *testing.T) {
	got := WithEdge("https://host/proxy/abc", "https://fast:888", "https://slow:888")
	if !strings.Contains(got, "edge=") || !strings.Contains(got, "edge_skip=") {
		t.Fatalf("оба параметра должны переноситься: %s", got)
	}
	// Пустые значения не должны сорить в ссылке.
	if got := WithEdge("https://host/proxy/abc", "", ""); strings.Contains(got, "edge") {
		t.Fatalf("без выбора и запрета ссылка остаётся чистой: %s", got)
	}
}

// Список разбирается терпимо: пробелы, хвостовые слэши, адрес без схемы.
func TestParseEdgeSkipNormalises(t *testing.T) {
	set := parseEdgeSkip(" fast.example:888/ , https://slow.example ")
	if !set["https://fast.example:888"] || !set["https://slow.example"] {
		t.Fatalf("список разобран неверно: %v", set)
	}
	if parseEdgeSkip("") != nil || parseEdgeSkip("  ,  ") != nil {
		t.Fatal("пустой список должен давать nil, а не пустую карту")
	}
}

// ── сохранённый за аккаунтом список ────────────────────────────────────────

// Сохранённая настройка обязана действовать на ссылку, выданную ДО неё: ссылка живёт долго, а
// «отключил в боте» человек ждёт сразу.
func TestStoredSkipAppliesToOldLink(t *testing.T) {
	SetEdgeSkipResolver(func(*http.Request) string { return "https://edge-b.example.net:2053" })
	defer SetEdgeSkipResolver(nil)

	r := httptest.NewRequest(http.MethodGet, "/proxy/x", nil) // в ссылке запрета нет вовсе
	if got := EdgeSkipEffective(r); got != "https://edge-b.example.net:2053" {
		t.Fatalf("сохранённый запрет не подхватился: %q", got)
	}
}

// Список из ссылки и сохранённый складываются, а не вытесняют друг друга.
func TestStoredSkipMergesWithURL(t *testing.T) {
	SetEdgeSkipResolver(func(*http.Request) string { return "https://fr.torr.example.com,https://edge-b.example.net:2053" })
	defer SetEdgeSkipResolver(nil)

	r := httptest.NewRequest(http.MethodGet, "/proxy/x?edge_skip=https%3A%2F%2Fspb.torr.example.com%2Chttps%3A%2F%2Fedge-b.example.net%3A2053", nil)
	got := parseEdgeSkip(EdgeSkipEffective(r))
	for _, want := range []string{"https://spb.torr.example.com", "https://fr.torr.example.com", "https://edge-b.example.net:2053"} {
		if !got[want] {
			t.Fatalf("в объединении нет %s: %v", want, got)
		}
	}
	if len(got) != 3 {
		t.Fatalf("повтор не схлопнулся: %v", got)
	}
}

// Без резолвера (бот не настроен) поведение прежнее — только то, что в ссылке.
func TestNoResolverKeepsURLOnly(t *testing.T) {
	SetEdgeSkipResolver(nil)
	r := httptest.NewRequest(http.MethodGet, "/proxy/x?edge_skip=https%3A%2F%2Ftorr.example.com", nil)
	if got := EdgeSkipEffective(r); got != "https://torr.example.com" {
		t.Fatalf("без резолвера должен остаться список из ссылки, а пришло %q", got)
	}
}
