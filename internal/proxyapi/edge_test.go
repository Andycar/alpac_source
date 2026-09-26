package proxyapi

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// Выключено по умолчанию: пустой список нод = отдаём сами, как раньше.
func TestEdgeOffByDefault(t *testing.T) {
	configureEdges(nil, nil, nil)
	if got := pickEdge("filmix", "tok", nil, ""); got != "" {
		t.Fatalf("без настроенных нод редиректа быть не должно, получили %q", got)
	}
}

// Белый список: плагин вне его остаётся на primary. Это защита от источников,
// привязывающих поток к адресу, скачавшему манифест.
func TestEdgeAllowlist(t *testing.T) {
	configureEdges([]string{"http://node1:888"}, []string{"filmix"}, nil)
	if got := pickEdge("filmix", "tok", nil, ""); got != "http://node1:888" {
		t.Fatalf("разрешённый плагин должен уходить на ноду, получили %q", got)
	}
	if got := pickEdge("iptv", "tok", nil, ""); got != "" {
		t.Fatalf("плагин вне белого списка должен остаться на primary, получили %q", got)
	}
}

// Звёздочка разрешает всё.
func TestEdgeWildcard(t *testing.T) {
	configureEdges([]string{"http://node1:888"}, []string{"*"}, nil)
	if got := pickEdge("anything", "tok", nil, ""); got == "" {
		t.Fatal("со звёздочкой должен уходить любой плагин")
	}
}

// Липкость: один и тот же токен ВСЕГДА идёт на одну ноду. Иначе манифест
// перепишет одна нода, а сегменты попросят у другой.
func TestEdgeStickyPerToken(t *testing.T) {
	configureEdges([]string{"http://a:888", "http://b:888", "http://c:888"}, []string{"*"}, nil)
	first := pickEdge("filmix", "token-xyz", nil, "")
	for i := 0; i < 50; i++ {
		if got := pickEdge("filmix", "token-xyz", nil, ""); got != first {
			t.Fatalf("выбор ноды для одного токена должен быть постоянным: %q против %q", got, first)
		}
	}
	// А разные токены должны раскладываться по разным нодам, иначе смысла нет.
	seen := map[string]bool{}
	for _, tok := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		seen[pickEdge("filmix", tok, nil, "")] = true
	}
	if len(seen) < 2 {
		t.Fatalf("нагрузка не распределяется: все токены ушли на %v", seen)
	}
}

// Мёртвая нода исключается из выдачи, а не отдаётся клиенту.
func TestEdgeSkipsDead(t *testing.T) {
	configureEdges([]string{"http://a:888", "http://b:888"}, []string{"*"}, nil)
	tok := "token-1"
	chosen := pickEdge("filmix", tok, nil, "")
	markEdgeDead(chosen)
	next := pickEdge("filmix", tok, nil, "")
	if next == chosen {
		t.Fatal("нода, помеченная мёртвой, не должна выбираться")
	}
	if next == "" {
		t.Fatal("должна была подставиться вторая нода")
	}
	// Обе мертвы → отдаём сами, а не в никуда.
	markEdgeDead(next)
	if got := pickEdge("filmix", tok, nil, ""); got != "" {
		t.Fatalf("когда живых нод нет, поток отдаёт primary; получили %q", got)
	}
}

// Возврат на primary — страховка от устаревшего белого списка: если источник
// перестал пускать ноду, зритель должен получить лишний переход, а не отказ.
func TestEdgeGiveBackToPrimary(t *testing.T) {
	configureEdgeFallback("https://tv.example")
	defer configureEdgeFallback("")

	req := httptest.NewRequest("GET", "/proxy/tok?x=1", nil)

	if got := edgeGiveBack(req, 206, "video/mp4"); got != "" {
		t.Fatalf("успешную отдачу возвращать не надо, получено %q", got)
	}
	got := edgeGiveBack(req, 403, "text/plain")
	if !strings.HasPrefix(got, "https://tv.example/proxy/tok?") {
		t.Fatalf("возврат ведёт не на primary: %q", got)
	}
	if !strings.Contains(got, edgeNoRetry+"=1") {
		t.Fatalf("в возврате нет метки %s: %q", edgeNoRetry, got)
	}
	if !strings.Contains(got, "x=1") {
		t.Fatalf("параметры запроса потеряны: %q", got)
	}
}

// Помеченный запрос primary обязан отдать сам, иначе он вернёт его той же
// ноде по тому же токену — и запрос будет ходить по кругу.
func TestEdgeMarkedRequestStaysOnPrimary(t *testing.T) {
	configureEdges([]string{"http://node1:888"}, []string{"*"}, nil)
	defer configureEdges(nil, nil, nil)

	plain := httptest.NewRequest("GET", "/proxy/tok", nil)
	if got := pickEdge("filmix", "tok", plain, ""); got == "" {
		t.Fatal("обычный запрос должен уходить на ноду")
	}
	marked := httptest.NewRequest("GET", "/proxy/tok?"+edgeNoRetry+"=1", nil)
	if got := pickEdge("filmix", "tok", marked, ""); got != "" {
		t.Fatalf("помеченный запрос ушёл на ноду (%q) — это петля", got)
	}
}

// Без primary_host возвращать некому: одиночная установка и сам primary
// обязаны отдавать ошибку как раньше, а не строить редирект в пустоту.
func TestEdgeGiveBackNeedsPrimaryHost(t *testing.T) {
	configureEdgeFallback("")
	req := httptest.NewRequest("GET", "/proxy/tok", nil)
	if got := edgeGiveBack(req, 502, "text/plain"); got != "" {
		t.Fatalf("возврат без primary_host: %q", got)
	}
}

// Источник, который ноды стабильно возвращают, должен отключиться сам:
// иначе каждый его сегмент навсегда получает два лишних перехода.
func TestEdgeSuspendsBouncingPlugin(t *testing.T) {
	configureEdges([]string{"http://node1:888"}, []string{"*"}, nil)
	defer configureEdges(nil, nil, nil)
	resetEdgeBounces()
	defer resetEdgeBounces()

	back := httptest.NewRequest("GET", "/proxy/tok?"+edgeNoRetry+"=1", nil)
	plain := httptest.NewRequest("GET", "/proxy/tok", nil)

	for i := 0; i < bounceThreshold-1; i++ {
		pickEdge("zetflix", "tok", back, "")
	}
	if edgeSuspended("zetflix") {
		t.Fatal("отключили раньше порога — одиночный сбой не повод")
	}
	if got := pickEdge("zetflix", "tok", plain, ""); got == "" {
		t.Fatal("до порога источник обязан выноситься")
	}

	pickEdge("zetflix", "tok", back, "") // порог
	if !edgeSuspended("zetflix") {
		t.Fatal("после порога источник должен отключиться")
	}
	if got := pickEdge("zetflix", "tok", plain, ""); got != "" {
		t.Fatalf("отключённый источник ушёл на ноду: %q", got)
	}
	// Соседний источник не должен пострадать.
	if got := pickEdge("vkmovie", "tok", plain, ""); got == "" {
		t.Fatal("отключение источника задело остальные")
	}
}

// Возвраты считаются только там, где вынос включён: на ноде этот же код
// не должен вести счёт и портить статистику.
func TestEdgeBounceIgnoredWhenOffloadOff(t *testing.T) {
	configureEdges(nil, nil, nil)
	resetEdgeBounces()
	defer resetEdgeBounces()

	back := httptest.NewRequest("GET", "/proxy/tok?"+edgeNoRetry+"=1", nil)
	for i := 0; i < bounceThreshold+3; i++ {
		pickEdge("zetflix", "tok", back, "")
	}
	if edgeSuspended("zetflix") {
		t.Fatal("нода посчитала чужие возвраты своими")
	}
}

// Исключение сильнее «*». Проверка ровно на IPTV: его вынос ломает не скорость,
// а просмотр — сессия живёт в памяти сервера, выдавшего ссылку.
func TestEdgeExcludeBeatsWildcard(t *testing.T) {
	configureEdges([]string{"http://node1:888"}, []string{"*"},
		[]string{"iptv", "iptv-ru", "youtube"})
	defer configureEdges(nil, nil, nil)
	resetEdgeBounces()

	req := httptest.NewRequest("GET", "/proxy/tok", nil)
	for _, p := range []string{"iptv", "IPTV-RU", "youtube"} {
		if got := pickEdge(p, "tok", req, ""); got != "" {
			t.Fatalf("%s ушёл на ноду вопреки исключению: %q", p, got)
		}
	}
	if got := pickEdge("vkmovie", "tok", req, ""); got == "" {
		t.Fatal("исключение задело остальные источники")
	}
}

// Подсказка клиента уважается, но только про наши ноды: иначе параметром в
// ссылке чужой просмотр уводился бы на посторонний хост.
func TestEdgeHonoursClientHint(t *testing.T) {
	configureEdges([]string{"https://a.example", "https://b.example"}, []string{"*"}, nil)
	defer configureEdges(nil, nil, nil)
	resetEdgeBounces()

	pick := func(q string) string {
		return pickEdge("vkmovie", "tok", httptest.NewRequest("GET", "/proxy/tok"+q, nil), "")
	}
	if got := pick("?" + edgeHint + "=https://b.example"); got != "https://b.example" {
		t.Fatalf("выбор клиента не учтён: %q", got)
	}
	// Без схемы и с хвостовым слэшем — то же самое.
	if got := pick("?" + edgeHint + "=b.example/"); got != "https://b.example" {
		t.Fatalf("адрес без схемы не разобран: %q", got)
	}
	// Посторонний хост игнорируется, поток идёт на нашу ноду.
	got := pick("?" + edgeHint + "=https://evil.example")
	if got == "https://evil.example" {
		t.Fatal("поток увели на чужой хост по параметру в ссылке")
	}
	if got == "" {
		t.Fatal("чужая подсказка не должна отменять вынос вовсе")
	}
}

// Названная клиентом нода лежит — выбираем как обычно, а не отдаём мёртвую.
func TestEdgeHintSkipsDeadNode(t *testing.T) {
	configureEdges([]string{"https://a.example", "https://b.example"}, []string{"*"}, nil)
	defer configureEdges(nil, nil, nil)
	resetEdgeBounces()
	markEdgeDead("https://b.example")

	got := pickEdge("vkmovie", "tok", httptest.NewRequest("GET", "/proxy/tok?"+edgeHint+"=https://b.example", nil), "")
	if got == "https://b.example" {
		t.Fatal("клиента отправили на нерабочую ноду")
	}
	if got != "https://a.example" {
		t.Fatalf("ожидалась живая нода, получено %q", got)
	}
}

// Отказ источника приходит и с кодом 200: content-router отвечает списком
// ссылок в JSON, и плеер на этом ломается. Такой ответ тоже надо возвращать.
func TestEdgeGiveBackOnNonMediaBody(t *testing.T) {
	configureEdgeFallback("https://tv.example")
	defer configureEdgeFallback("")
	req := httptest.NewRequest("GET", "/proxy/tok", nil)

	for _, ct := range []string{"application/json", "application/json; charset=utf-8", "text/html"} {
		if got := edgeGiveBack(req, 200, ct); got == "" {
			t.Fatalf("ответ %q не признан отказом", ct)
		}
	}
	// Настоящее медиа не трогаем, включая m3u8 под видом текста.
	for _, ct := range []string{"video/mp4", "application/vnd.apple.mpegurl", "text/plain; charset=utf-8", ""} {
		if got := edgeGiveBack(req, 200, ct); got != "" {
			t.Fatalf("рабочий ответ %q приняли за отказ: %q", ct, got)
		}
	}
}

// Ссылку добыла нода — отдаёт она же, даже если источник отключён
// самоотключением: у CDN с привязкой к адресу другого варианта нет.
func TestEdgePrefersMintingNode(t *testing.T) {
	configureEdges([]string{"https://a.example", "https://b.example"}, []string{"vkmovie"}, []string{"zetflix"})
	defer configureEdges(nil, nil, nil)
	resetEdgeBounces()
	req := httptest.NewRequest("GET", "/proxy/tok", nil)

	// исключённый источник, но ссылка подписана нодой b → b
	if got := pickEdge("zetflix", "tok", req, "https://b.example"); got != "https://b.example" {
		t.Fatalf("подписанную ссылку не отдали добывшей ноде: %q", got)
	}
	// подпись адресом вне stream_edges → отдаём сами
	if got := pickEdge("vkmovie", "tok", req, "https://evil.example"); got != "" {
		t.Fatalf("ушли на неизвестный адрес: %q", got)
	}
	// добывшая нода лежит → сами
	markEdgeDead("https://b.example")
	if got := pickEdge("zetflix", "tok", req, "https://b.example"); got != "" {
		t.Fatalf("отправили на мёртвую ноду: %q", got)
	}
	// возврат подписанной ссылки не штрафует источник
	back := httptest.NewRequest("GET", "/proxy/tok?"+edgeNoRetry+"=1", nil)
	for i := 0; i < bounceThreshold+2; i++ {
		pickEdge("vkmovie", "tok", back, "https://a.example")
	}
	if edgeSuspended("vkmovie") {
		t.Fatal("источник отключили за возвраты подписанных ссылок")
	}
}

// Подсказка зрителя должна переезжать в ссылки, которые сервер выдаёт сам:
// иначе первый запрос уйдёт на выбранную ноду, а все сегменты — по хешу.
func TestWithEdgePropagation(t *testing.T) {
	if got := WithEdge("https://tv.x/proxy/abc.m3u8", "https://edge-a.example.org", ""); got != "https://tv.x/proxy/abc.m3u8?edge=https%3A%2F%2Fedge-a.example.org" {
		t.Fatalf("без query: %q", got)
	}
	if got := WithEdge("https://tv.x/proxy/abc?sid=1", "edge-a.example.org", ""); got != "https://tv.x/proxy/abc?sid=1&edge=edge-a.example.org" {
		t.Fatalf("с query: %q", got)
	}
	if got := WithEdge("https://tv.x/proxy/abc", "", ""); got != "https://tv.x/proxy/abc" {
		t.Fatalf("пустая подсказка изменила ссылку: %q", got)
	}
	req := httptest.NewRequest("GET", "/lite/x?edge=https%3A%2F%2Fedge-b.example.net%3A2053", nil)
	if got := EdgeHintFrom(req); got != "https://edge-b.example.net:2053" {
		t.Fatalf("подсказка из запроса: %q", got)
	}
}

// Замер 2026-09-14: ссылка filmix живёт только на том адресе, который её добыл.
// Одна и та же серия: spb добыла и скачала — 206; ту же ссылку качает main — 429;
// ссылку main качает spb — 429. Значит «кто добыл, тот и отдаёт» — не оптимизация,
// а единственный работающий порядок.
//
// Отсюда смысл исключения: внести filmix в stream_edge_exclude нужно, чтобы
// НЕПОДПИСАННЫЕ ссылки (их минтит primary, у него edge_url пуст) не уезжали на ноду,
// где гарантированно словят 429 и вернутся обратно. Но подписанная нодой ссылка
// обязана уйти к ней ДАЖЕ ПРИ ЭТОМ ЗАПРЕТЕ — иначе исключение убьёт и ту долю
// трафика, которая сейчас работает.
func TestEdgeMintedBySurvivesExcludeList(t *testing.T) {
	configureEdges(
		[]string{"https://torr.example.com"},
		[]string{"*"},
		[]string{"filmix"}, // запрещаем выносить filmix
	)
	defer configureEdges(nil, nil, nil)

	// Ссылку добыла нода — запрет её не касается, отдавать должна она же.
	if got := pickEdge("filmix", "tok", nil, "https://torr.example.com"); got != "https://torr.example.com" {
		t.Fatalf("подписанная нодой ссылка ушла мимо неё: %q", got)
	}

	// Ссылка без подписи (её минтил primary) — на ноду не уходит, отдаём сами.
	if got := pickEdge("filmix", "tok", nil, ""); got != "" {
		t.Fatalf("неподписанная ссылка уехала на ноду %q — там будет 429 и возврат", got)
	}

	// Соседний источник запрет не задевает.
	if got := pickEdge("videodb", "tok", nil, ""); got != "https://torr.example.com" {
		t.Fatalf("запрет filmix задел videodb: %q", got)
	}
}

// Подпись адресом, которого нет в stream_edges, — отдаём сами: слать поток на
// хост, не объявленный нодой, нельзя (так параметром в ссылке можно было бы увести
// чужой просмотр на посторонний сервер).
func TestEdgeMintedByUnknownHostStaysLocal(t *testing.T) {
	configureEdges([]string{"https://torr.example.com"}, []string{"*"}, nil)
	defer configureEdges(nil, nil, nil)

	if got := pickEdge("filmix", "tok", nil, "https://postoronniy.example"); got != "" {
		t.Fatalf("поток ушёл на незнакомый хост %q", got)
	}
}
