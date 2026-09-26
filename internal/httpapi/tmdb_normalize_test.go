package httpapi

import (
	"strings"
	"testing"
)

func TestUnreadableTitle(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"Оби-Ван Кеноби", false},
		{"Stranger Things", false},
		{"Hermanos de sangre: Cohn", false},
		{"賞金猎人", true},        // китайский
		{"عالیجناب", true},    // арабская вязь
		{"기적의 형제", true},      // хангыль
		{"Тайна 東京", false},   // смешанное: кириллицы больше
		{"S&X 東京 2020", true}, // ничья 2:2 → нечитаемо
		{"", false},           // пусто
		{"1899", false},       // только цифры
		{"Ne Zha 哪吒", false},  // латиницы больше — оставляем
	}
	for _, c := range cases {
		if got := unreadableTitle(c.in); got != c.want {
			r, f := titleScriptCounts(c.in)
			t.Errorf("unreadableTitle(%q) = %v, want %v (readable=%d foreign=%d)", c.in, got, c.want, r, f)
		}
	}
}

func TestRepairMojibake(t *testing.T) {
	// «Слово» в UTF-8, прочитанное как latin1 — классическая кракозябра из кривого зеркала.
	broken := "Ð¡Ð»Ð¾Ð²Ð¾"
	if got := repairMojibake(broken); got != "Слово" {
		t.Errorf("repairMojibake(%q) = %q, want %q", broken, got, "Слово")
	}
	// Нормальные строки с диакритикой чинить НЕЛЬЗЯ.
	for _, ok := range []string{"Là-bas", "Amélie", "Coração", "Stranger Things", "Оби-Ван"} {
		if got := repairMojibake(ok); got != ok {
			t.Errorf("repairMojibake(%q) изменил строку на %q", ok, got)
		}
	}
}

func TestNormalizeBodyDropsPosterlessInBrowse(t *testing.T) {
	body := []byte(`{"page":1,"results":[
		{"id":1,"name":"Хороший","poster_path":"/a.jpg","first_air_date":"2024-01-01"},
		{"id":2,"name":"Неймовірні дуети","poster_path":null,"first_air_date":"2023-01-01"},
		{"id":3,"name":"Без постера","first_air_date":"2023-01-01"}
	]}`)
	out, changed := normalizeTMDBBody(body, "3/discover/tv", nil)
	if !changed {
		t.Fatal("ожидалась фильтрация карточек без постера")
	}
	s := string(out)
	if !strings.Contains(s, "Хороший") {
		t.Error("карточка с постером не должна выпадать")
	}
	if strings.Contains(s, "Неймовірні") || strings.Contains(s, "Без постера") {
		t.Errorf("карточки без постера остались: %s", s)
	}
}

func TestNormalizeBodyKeepsPosterlessInSearch(t *testing.T) {
	body := []byte(`{"page":1,"results":[{"id":2,"name":"Без постера","poster_path":null,"first_air_date":"2023-01-01"}]}`)
	out, _ := normalizeTMDBBody(body, "3/search/tv", nil)
	if !strings.Contains(string(out), "Без постера") {
		t.Error("в поиске карточка без постера должна оставаться — это валидный ответ")
	}
}

func TestNormalizeBodyUsesEnglishTitle(t *testing.T) {
	body := []byte(`{"results":[{"id":42,"name":"賞金猎人","original_name":"賞金猎人","poster_path":"/a.jpg","first_air_date":"2024-01-01"}]}`)
	calls := 0
	lookup := func(refs []mediaRef) map[string]string {
		calls++ // ровно ОДИН батч на весь ответ, а не по запросу на карточку
		if len(refs) != 1 || refs[0].Kind != "tv" || refs[0].ID != 42 {
			t.Errorf("lookup(%+v) — ожидался один ref tv/42", refs)
		}
		return map[string]string{"tv:42": "Bounty Hunters"}
	}
	out, changed := normalizeTMDBBody(body, "3/discover/tv", lookup)
	if !changed || calls != 1 {
		t.Fatalf("changed=%v calls=%d — ожидалась одна догрузка и замена", changed, calls)
	}
	if !strings.Contains(string(out), "Bounty Hunters") {
		t.Errorf("английское название не подставилось: %s", out)
	}
}

func TestNormalizeBodyFallsBackToOriginal(t *testing.T) {
	// Английского нет (lookup пустой), но original_name латиницей — берём его.
	body := []byte(`{"results":[{"id":7,"name":"기적의 형제","original_name":"Miracle Brothers","poster_path":"/a.jpg","first_air_date":"2024-01-01"}]}`)
	out, changed := normalizeTMDBBody(body, "3/discover/tv", func([]mediaRef) map[string]string { return nil })
	if !changed || !strings.Contains(string(out), "Miracle Brothers") {
		t.Errorf("ожидался фолбэк на original_name: changed=%v body=%s", changed, out)
	}
}

func TestNormalizeBodyLeavesGoodListAlone(t *testing.T) {
	body := []byte(`{"page":1,"results":[{"id":1,"title":"Оби-Ван Кеноби","poster_path":"/a.jpg","release_date":"2022-05-27"}]}`)
	out, changed := normalizeTMDBBody(body, "3/discover/movie", nil)
	if changed {
		t.Errorf("нормальный список не должен меняться: %s", out)
	}
}

func TestNormalizeBodyDetailCard(t *testing.T) {
	body := []byte(`{"id":42,"name":"Ð¡Ð»Ð¾Ð²Ð¾","poster_path":"/a.jpg","first_air_date":"2024-01-01"}`)
	out, changed := normalizeTMDBBody(body, "3/tv/42", nil)
	if !changed || !strings.Contains(string(out), "Слово") {
		t.Errorf("кракозябра в карточке тайтла не починена: %s", out)
	}
}

func TestNormalizeBodyIgnoresNonJSON(t *testing.T) {
	body := []byte("not json at all")
	out, changed := normalizeTMDBBody(body, "3/discover/tv", nil)
	if changed || string(out) != "not json at all" {
		t.Error("не-JSON тело должно возвращаться как есть")
	}
}

func TestBrowseAndListPathMatching(t *testing.T) {
	browse := []string{"3/discover/tv", "discover/movie", "3/tv/1399/similar", "3/movie/27205/recommendations"}
	for _, p := range browse {
		if !tmdbBrowsePath(p) {
			t.Errorf("%q должен считаться витриной (фильтрация мусора)", p)
		}
	}
	notBrowse := []string{"3/search/tv", "3/tv/1399", "3/person/6384/combined_credits"}
	for _, p := range notBrowse {
		if tmdbBrowsePath(p) {
			t.Errorf("%q НЕ должен фильтроваться", p)
		}
	}
	for _, p := range append(browse, notBrowse[0], notBrowse[2]) {
		if !tmdbListPath(p) {
			t.Errorf("%q должен нормализоваться как список", p)
		}
	}
}

func TestNormalizeBodyBatchesLookups(t *testing.T) {
	// Три нечитаемых карточки без латинского original → ОДИН батч с тремя ref'ами
	// (иначе холодный список ждал бы три последовательных запроса к TMDB).
	body := []byte(`{"results":[
		{"id":1,"name":"賞金猎人","original_name":"賞金猎人","poster_path":"/a.jpg","first_air_date":"2024-01-01"},
		{"id":2,"name":"عالیجناب","original_name":"عالیجناب","poster_path":"/b.jpg","first_air_date":"2024-01-01"},
		{"id":3,"name":"기적의 형제","original_name":"기적의 형제","poster_path":"/c.jpg","first_air_date":"2024-01-01"}
	]}`)
	batches := 0
	got := 0
	lookup := func(refs []mediaRef) map[string]string {
		batches++
		got = len(refs)
		return map[string]string{"tv:1": "Bounty Hunters", "tv:2": "His Excellency", "tv:3": "Miracle Brothers"}
	}
	out, changed := normalizeTMDBBody(body, "3/discover/tv", lookup)
	if batches != 1 || got != 3 {
		t.Fatalf("batches=%d refs=%d — ожидался один батч на три карточки", batches, got)
	}
	s := string(out)
	if !changed || !strings.Contains(s, "Bounty Hunters") || !strings.Contains(s, "His Excellency") || !strings.Contains(s, "Miracle Brothers") {
		t.Errorf("не все названия заменены: %s", s)
	}
}

func TestNormalizeBodyDropsHopelessTitlesInBrowse(t *testing.T) {
	// У карточки нет перевода ни на русский, ни на английский (TMDB отдаёт иероглифы и там,
	// и там) — в витрине она бесполезна. В поиске такая карточка обязана остаться.
	body := []byte(`{"results":[
		{"id":1,"name":"哆啦A梦TV版","original_name":"哆啦A梦TV版","poster_path":"/a.jpg","first_air_date":"2024-01-01"},
		{"id":2,"name":"Хороший","poster_path":"/b.jpg","first_air_date":"2024-01-01"}
	]}`)
	noEN := func([]mediaRef) map[string]string { return nil }

	out, changed := normalizeTMDBBody(body, "3/discover/tv", noEN)
	if !changed || strings.Contains(string(out), "哆啦") {
		t.Errorf("нечитаемая карточка должна выпадать из витрины: %s", out)
	}
	if !strings.Contains(string(out), "Хороший") {
		t.Errorf("читаемая карточка выпала: %s", out)
	}

	outSearch, _ := normalizeTMDBBody(body, "3/search/tv", noEN)
	if !strings.Contains(string(outSearch), "哆啦") {
		t.Errorf("в поиске карточка должна оставаться: %s", outSearch)
	}
}
