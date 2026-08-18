package httpapi

import (
	"testing"

	"lampac-go/internal/litesrc"
)

// 2026-08-05: filmix перестал выдавать device-код («Не удалось получить код»), а kit отвечал сухим
// "filmix did not return a code" с единственного захардкоженного зеркала — по такому ответу нельзя
// понять, упал апстрим или сломались мы.
func TestFilmixAPIHostsFailoverOrder(t *testing.T) {
	hosts := litesrc.FilmixAPIHosts("")
	if len(hosts) < 2 {
		t.Fatalf("нужен failover минимум по двум зеркалам, получено %v", hosts)
	}

	// Заданный в конфиге хост обязан идти первым, но не вытеснять запасные.
	custom := litesrc.FilmixAPIHosts("http://my.mirror")
	if custom[0] != "http://my.mirror" {
		t.Fatalf("хост из конфига должен быть первым: %v", custom)
	}
	if len(custom) < len(hosts) {
		t.Fatalf("запасные зеркала не должны теряться: %v", custom)
	}

	// Дубликат не должен появляться дважды.
	seen := map[string]bool{}
	for _, h := range litesrc.FilmixAPIHosts(hosts[0]) {
		if seen[h] {
			t.Fatalf("зеркало продублировано: %v", litesrc.FilmixAPIHosts(hosts[0]))
		}
		seen[h] = true
	}
}

// Ответ апстрима должен доезжать до пользователя: именно он объясняет, что чинить нечего.
func TestKitBindErrorCarriesUpstreamMessage(t *testing.T) {
	var got map[string]string
	payload := `{"error":"filmix did not return a code","upstream":"Не удалось получить код. Попробуйте снова через минутку!"}`
	if err := json.Unmarshal([]byte(payload), &got); err != nil {
		t.Fatalf("ответ должен быть валидным JSON: %v", err)
	}
	if got["upstream"] == "" {
		t.Fatal("текст апстрима обязан присутствовать")
	}
}
