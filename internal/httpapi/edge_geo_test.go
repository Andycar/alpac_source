package httpapi

import (
	"testing"
	"time"
)

// Определение страны обязано быть мгновенным: решение принимается на каждый запрос манифеста.
// Готовое значение из кэша /24 отдаём сразу, промах не блокирует поток и не запрещает ноду.
func TestCountryForEdge(t *testing.T) {
	asnMu.Lock()
	asnCache = map[string]asnInfo{}
	asnMu.Unlock()
	t.Cleanup(func() {
		asnMu.Lock()
		asnCache = map[string]asnInfo{}
		asnMu.Unlock()
	})

	// приватные и мусорные адреса — никаких походов наружу и никаких ограничений
	for _, ip := range []string{"", "127.0.0.1", "10.1.2.3", "192.168.0.5", "не-адрес"} {
		if c := countryForEdge(ip); c != "" {
			t.Fatalf("для %q ожидали пустую страну, получили %q", ip, c)
		}
	}

	// прогретая подсеть — отвечаем из кэша
	ip := "188.128.41.149"
	asnMu.Lock()
	asnCache[asnKey(ip)] = asnInfo{Country: "ru", at: time.Now()}
	asnMu.Unlock()
	if c := countryForEdge(ip); c != "RU" {
		t.Fatalf("из кэша ожидали RU, получили %q", c)
	}

	// сосед по той же /24 обслуживается тем же значением — один провайдер на префикс
	if c := countryForEdge("188.128.41.7"); c != "RU" {
		t.Fatalf("сосед по /24 должен дать RU, получили %q", c)
	}

	// протухшая запись не используется
	asnMu.Lock()
	asnCache[asnKey(ip)] = asnInfo{Country: "RU", at: time.Now().Add(-asnTTL - time.Hour)}
	asnMu.Unlock()
	if c := countryForEdge(ip); c != "" {
		t.Fatalf("протухшую запись отдавать нельзя, получили %q", c)
	}
}

// Фоновое уточнение ставится не чаще одного раза на подсеть — иначе бесплатный лимит
// ip-api (45 запросов в минуту) выгорит на первом же всплеске зрителей.
func TestEdgeGeoFillDeduplicates(t *testing.T) {
	edgeGeoPendMu.Lock()
	edgeGeoPending = map[string]time.Time{}
	edgeGeoPendMu.Unlock()

	key := "203.0.113.0/24"
	scheduleEdgeGeoFill(key, "203.0.113.10")
	edgeGeoPendMu.Lock()
	first, ok := edgeGeoPending[key]
	edgeGeoPendMu.Unlock()
	if !ok {
		t.Fatal("первое уточнение не зарегистрировано")
	}
	scheduleEdgeGeoFill(key, "203.0.113.11")
	edgeGeoPendMu.Lock()
	second := edgeGeoPending[key]
	n := len(edgeGeoPending)
	edgeGeoPendMu.Unlock()
	if !second.Equal(first) {
		t.Fatal("повторное уточнение той же подсети должно отсекаться")
	}
	if n != 1 {
		t.Fatalf("ожидали одну запись в очереди, стало %d", n)
	}
}
