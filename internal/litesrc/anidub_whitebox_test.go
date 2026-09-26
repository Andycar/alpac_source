package litesrc

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

// Фрагменты живых страниц online.anidub.com, обрезанные до того, что читают
// парсеры: выдача поиска и страница тайтла.

const anidubSearchFixture = `<div class="th-item">
	<a class="th-in" href="https://online.anidub.com/7874-kosmicheskie-bratya-uchuu-kyoudai.html">
		<div class="th-img"><img src="/uploads/posts/x.jpg" alt="Космические братья"></div>
		<div class="th-title">Космические братья / Uchuu Kyoudai [45 из 99]</div>
		<div class="th-subtitle nowrap"><span><a href="#">2012</a></span> • <span><a href="#">Аниме сериалы</a></span></div>
	</a>
</div>
<div class="th-item">
	<a class="th-in" href="https://online.anidub.com/11393-kosmicheskie-bratya-epizod-0.html">
		<div class="th-img"><img src="/uploads/posts/y.jpg" alt="Эпизод 0"></div>
		<div class="th-title">Космические братья: Эпизод 0 / Uchuu Kyoudai: Number Zero</div>
		<div class="th-subtitle nowrap"><span><a href="#">2014</a></span> • <span><a href="#">Аниме Фильмы</a></span></div>
	</a>
</div>
<div class="th-item">
	<a class="th-in" href="https://online.anidub.com/12439-bratja.html">
		<div class="th-img"><img src="/uploads/posts/z.jpg" alt="Братья"></div>
		<div class="th-title">Братья / Brothers</div>
		<div class="th-subtitle nowrap"><span><a href="#">2026</a></span> • <span><a href="#">Дорамы</a></span></div>
	</a>
</div>`

func anidubTitleFixture(season int, playerURL string) string {
	return fmt.Sprintf(`<h1>Братья</h1>
<div class="fmain">
	<div class="finfo"><span>Сезон %d, серии 10 из 16</span></div>
	<div class="finfo"><span>2026</span> <span>Таиланд</span></div>
	<div class="fplayer"><iframe src="%s/index.php?v=/s10/2219cc6f11dd49bd2ef8de7b49e8795a&amp;playlist" allowfullscreen></iframe></div>
</div>`, season, playerURL)
}

// ---------------------------------------------------------------------------
// Парсеры каталога
// ---------------------------------------------------------------------------

func TestAnidubParseCards(t *testing.T) {
	cards := anidubParseCards(anidubSearchFixture)
	if len(cards) != 3 {
		t.Fatalf("карточек = %d, хотели 3", len(cards))
	}
	if cards[0].rus != "Космические братья" || cards[0].orig != "Uchuu Kyoudai" {
		t.Fatalf("название разобрано неверно: %+v", cards[0])
	}
	// «[45 из 99]» — счётчик серий, а не часть названия.
	if strings.Contains(cards[0].rus+cards[0].orig, "[") {
		t.Fatalf("счётчик серий попал в название: %+v", cards[0])
	}
	if cards[0].year != 2012 || cards[0].cat != "Аниме сериалы" || cards[0].kind != anidubKindSerial {
		t.Fatalf("метаданные сериала: %+v", cards[0])
	}
	// Категория разводит фильмы и сериалы — иначе источник ответит фильмом на
	// сериальную карточку.
	if cards[1].kind != anidubKindMovie {
		t.Fatalf("«Аниме Фильмы» должны считаться фильмом: %+v", cards[1])
	}
	if cards[2].kind != anidubKindSerial {
		t.Fatalf("дорама — сериал: %+v", cards[2])
	}
}

func TestAnidubSplitTitle(t *testing.T) {
	cases := []struct{ in, rus, orig string }{
		{"Космические братья / Uchuu Kyoudai [45 из 99]", "Космические братья", "Uchuu Kyoudai"},
		{"Ты и я / Kimi to Boku [TV-1 + TV-2] [26 из 26]", "Ты и я", "Kimi to Boku"},
		{"Братья / Brothers", "Братья", "Brothers"},
		{"Без оригинала", "Без оригинала", ""},
	}
	for _, c := range cases {
		rus, orig := anidubSplitTitle(c.in)
		if rus != c.rus || orig != c.orig {
			t.Fatalf("%q -> (%q, %q), хотели (%q, %q)", c.in, rus, orig, c.rus, c.orig)
		}
	}
}

func TestAnidubPickCards(t *testing.T) {
	cards := anidubParseCards(anidubSearchFixture)

	// Сериальная карточка: фильм «Эпизод 0» не должен попасть в кандидаты.
	picked := anidubPickCards(cards, "Космические братья", "Space Brothers", 2012, true)
	if len(picked) != 1 || picked[0].url != "https://online.anidub.com/7874-kosmicheskie-bratya-uchuu-kyoudai.html" {
		t.Fatalf("сериал подобран неверно: %+v", picked)
	}
	// Фильмовая карточка забирает ровно фильм.
	picked = anidubPickCards(cards, "Космические братья: Эпизод 0", "", 2014, false)
	if len(picked) != 1 || picked[0].kind != anidubKindMovie {
		t.Fatalf("фильм подобран неверно: %+v", picked)
	}
	// Совпадение по оригиналу, когда русское название у карточки другое.
	picked = anidubPickCards(cards, "Бразерс", "Brothers", 2026, true)
	if len(picked) != 1 || picked[0].orig != "Brothers" {
		t.Fatalf("подбор по оригиналу: %+v", picked)
	}
	// Чужого выдумывать нельзя.
	if picked = anidubPickCards(cards, "Наруто", "Naruto", 2002, true); len(picked) != 0 {
		t.Fatalf("подобран посторонний тайтл: %+v", picked)
	}
}

func TestAnidubSanitizeTitleURL(t *testing.T) {
	a := NewAnidubChecker(config.Config{})
	good := "https://online.anidub.com/12439-bratja.html"
	if got, ok := a.sanitizeTitleURL(good); !ok || got != good {
		t.Fatalf("нормальный адрес отвергнут: %q %v", got, ok)
	}
	// ?u= приходит с нашей же страницы сезонов — чужой хост и произвольный
	// путь превращать в запрос нельзя.
	for _, bad := range []string{
		"https://evil.example/12439-bratja.html",
		"https://online.anidub.com/engine/download.php?id=1",
		"https://online.anidub.com/../etc/passwd",
		"not a url",
		"",
	} {
		if _, ok := a.sanitizeTitleURL(bad); ok {
			t.Fatalf("принят опасный адрес: %q", bad)
		}
	}
}

// ---------------------------------------------------------------------------
// Сквозной прогон против фикстурного сайта
// ---------------------------------------------------------------------------

type anidubFakeSite struct {
	server *httptest.Server
	hits   map[string]int
}

func newAnidubFakeSite(t *testing.T) *anidubFakeSite {
	t.Helper()
	site := &anidubFakeSite{hits: map[string]int{}}
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Query().Get("do") == "search":
			site.hits["search"]++
			fmt.Fprint(w, strings.ReplaceAll(anidubSearchFixture, "https://online.anidub.com", site.server.URL))
		case strings.HasPrefix(r.URL.Path, "/7874-"):
			site.hits["title"]++
			fmt.Fprint(w, anidubTitleFixture(1, site.server.URL))
		case strings.HasPrefix(r.URL.Path, "/11393-"):
			site.hits["title"]++
			fmt.Fprint(w, anidubTitleFixture(1, site.server.URL))
		case strings.HasPrefix(r.URL.Path, "/12439-"):
			site.hits["title"]++
			// Сезон 2 — проверяем, что номер читается со страницы, а не из выдачи.
			fmt.Fprint(w, anidubTitleFixture(2, site.server.URL))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	mux.HandleFunc("/index.php", func(w http.ResponseWriter, r *http.Request) {
		site.hits["playlist"]++
		fmt.Fprint(w, anividsPlaylistFixture(1))
	})
	mux.HandleFunc("/vid.php", func(w http.ResponseWriter, r *http.Request) {
		site.hits["vid"]++
		if r.Header.Get("User-Agent") == "" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		fmt.Fprint(w, anividsMasterFixture)
	})

	site.server = httptest.NewServer(mux)
	t.Cleanup(site.server.Close)
	return site
}

func newAnidubTestChecker(t *testing.T, site *anidubFakeSite) (*anidubChecker, http.HandlerFunc) {
	t.Helper()
	cfg := config.Config{Online: config.OnlineConfig{Anidub: config.HostSource{Host: site.server.URL}}}
	checker := NewAnidubChecker(cfg)
	return checker, checker.Handle(cfg, nil)
}

func anidubServe(t *testing.T, h http.HandlerFunc, target string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec.Body.String()
}

func TestAnidubSeasonsAndEpisodes(t *testing.T) {
	site := newAnidubFakeSite(t)
	_, h := newAnidubTestChecker(t, site)

	q := "title=" + url.QueryEscape("Космические братья") + "&original_title=" + url.QueryEscape("Uchuu Kyoudai") + "&year=2012&serial=1&rjson=true"
	body := anidubServe(t, h, "http://l/lite/anidub?"+q)
	if !strings.Contains(body, `"type":"season"`) {
		t.Fatalf("сезоны не отданы: %s", body)
	}
	if !strings.Contains(body, `"s":1`) || !strings.Contains(body, "1 сезон") {
		t.Fatalf("номер сезона не проставлен: %s", body)
	}

	titleURL := site.server.URL + "/7874-kosmicheskie-bratya-uchuu-kyoudai.html"
	body = anidubServe(t, h, "http://l/lite/anidub/serial?u="+url.QueryEscape(titleURL)+"&s=1&rjson=true")
	if !strings.Contains(body, `"type":"episode"`) {
		t.Fatalf("серии не отданы: %s", body)
	}
	if !strings.Contains(body, "/lite/anidub/play?h=s10%2F82ed926165ca970566b1515f5231079a") {
		t.Fatalf("ссылка на воспроизведение отсутствует: %s", body)
	}
	if !strings.Contains(body, "q=1080p") {
		t.Fatalf("качество релиза не доехало до ссылки: %s", body)
	}

	// Дерево кэшируется: повторный заход сайт не трогает.
	before := site.hits["title"]
	anidubServe(t, h, "http://l/lite/anidub/serial?u="+url.QueryEscape(titleURL)+"&s=1&rjson=true")
	if site.hits["title"] != before {
		t.Fatalf("страница тайтла перезапрошена: %d -> %d", before, site.hits["title"])
	}
}

func TestAnidubMovieCard(t *testing.T) {
	site := newAnidubFakeSite(t)
	_, h := newAnidubTestChecker(t, site)

	q := "title=" + url.QueryEscape("Космические братья: Эпизод 0") + "&year=2014&rjson=true"
	body := anidubServe(t, h, "http://l/lite/anidub?"+q)
	if !strings.Contains(body, `"type":"movie"`) {
		t.Fatalf("фильм не отдан: %s", body)
	}
	if !strings.Contains(body, "/lite/anidub/play?h=") {
		t.Fatalf("ссылка на поток отсутствует: %s", body)
	}
}

func TestAnidubSeasonNumberComesFromTitlePage(t *testing.T) {
	site := newAnidubFakeSite(t)
	_, h := newAnidubTestChecker(t, site)

	// У «Братьев» на странице стоит «Сезон 2» — именно он должен попасть в
	// выдачу, выдача поиска номера сезона не знает.
	q := "title=" + url.QueryEscape("Братья") + "&original_title=Brothers&year=2026&serial=1&rjson=true"
	body := anidubServe(t, h, "http://l/lite/anidub?"+q)
	if !strings.Contains(body, `"s":2`) || !strings.Contains(body, "2 сезон") {
		t.Fatalf("сезон со страницы тайтла не подхвачен: %s", body)
	}
}

func TestAnidubPlay(t *testing.T) {
	site := newAnidubFakeSite(t)
	checker, h := newAnidubTestChecker(t, site)

	// Плеерный хост узнаётся из iframe — прогреваем дерево.
	anidubServe(t, h, "http://l/lite/anidub?title="+url.QueryEscape("Братья")+"&original_title=Brothers&serial=1&rjson=true")
	if got := checker.playerHost(); got != site.server.URL {
		t.Fatalf("плеерный хост = %q, хотели %q", got, site.server.URL)
	}

	body := anidubServe(t, h, "http://l/lite/anidub/play?h="+url.QueryEscape("s10/82ed926165ca970566b1515f5231079a")+"&q=1080p&t="+url.QueryEscape("Братья"))
	if !strings.Contains(body, `"method":"play"`) {
		t.Fatalf("ответ play: %s", body)
	}
	if !strings.Contains(body, `"quality":"1080p"`) || !strings.Contains(body, `"quality":"480p"`) {
		t.Fatalf("качества не собраны: %s", body)
	}
	// title — название карточки, а не студии: иначе capi выбросит источник.
	if !strings.Contains(body, `"title":"Братья"`) || !strings.Contains(body, `"translate":"AniDUB"`) {
		t.Fatalf("имена в ответе: %s", body)
	}
}

func TestAnidubPlayRejectsForeignHash(t *testing.T) {
	site := newAnidubFakeSite(t)
	_, h := newAnidubTestChecker(t, site)

	for _, bad := range []string{"../../etc/passwd", "http://evil.example/x", "s10/..%2fadmin", ""} {
		body := anidubServe(t, h, "http://l/lite/anidub/play?h="+url.QueryEscape(bad))
		if strings.Contains(body, "vid.php") {
			t.Fatalf("принят чужой идентификатор %q: %s", bad, body)
		}
	}
	if site.hits["vid"] != 0 {
		t.Fatalf("плеер дёрнут для отвергнутого идентификатора: %d", site.hits["vid"])
	}
}

func TestAnidubChecksearchCachesProbe(t *testing.T) {
	site := newAnidubFakeSite(t)
	_, h := newAnidubTestChecker(t, site)

	target := "http://l/lite/anidub?checksearch=true&serial=1&title=" + url.QueryEscape("Братья") + "&original_title=Brothers"
	if body := anidubServe(t, h, target); !strings.Contains(body, `"type":"movie"`) {
		t.Fatalf("первый checksearch: %s", body)
	}
	first := site.hits["search"] + site.hits["title"] + site.hits["playlist"]

	if body := anidubServe(t, h, target); !strings.Contains(body, `"type":"movie"`) {
		t.Fatalf("второй checksearch: %s", body)
	}
	if second := site.hits["search"] + site.hits["title"] + site.hits["playlist"]; second != first {
		t.Fatalf("проба не закэширована: %d -> %d запросов", first, second)
	}
}

func TestAnidubSeasonFromTitle(t *testing.T) {
	cases := []struct {
		rus, orig string
		want      int
	}{
		// Продолжения, у которых на странице стоит «Сезон 1».
		{"Реинкарнация безработного ТВ-2", "Mushoku Tensei II: Isekai Ittara Honki Dasu TV-2", 2},
		{"Блич: Тысячелетняя кровавая война - Конфликт ТВ-2", "", 2},
		{"Тетрадь смерти", "Death Note III", 3},
		{"Магическая битва 2 сезон", "", 2},
		{"", "Vinland Saga Season 2", 2},
		// Первый сезон номера не несёт — считать его «вторым» нельзя.
		{"Рыцарь-скелет в ином мире", "Gaikotsu Kishi-sama, Tadaima Isekai e Odekake-chuu", 0},
		{"Стальной алхимик", "Fullmetal Alchemist I", 0},
		{"Необъятный океан", "Grand Blue", 0},
	}
	for _, c := range cases {
		if got := anidubSeasonFromTitle(c.rus, c.orig); got != c.want {
			t.Fatalf("(%q, %q) -> %d, хотели %d", c.rus, c.orig, got, c.want)
		}
	}
}

func TestAnidubTitleMatchGrades(t *testing.T) {
	// Точное совпадение сильнее совпадения по основной части — иначе «Наруто»
	// перебивал бы «Наруто: Ураганные хроники» на её собственной карточке.
	if got := anidubTitleMatch("Наруто", "Наруто"); got != 2 {
		t.Fatalf("точное совпадение = %d", got)
	}
	if got := anidubTitleMatch("Наруто: Ураганные хроники", "Наруто"); got != 1 {
		t.Fatalf("совпадение по основной части = %d", got)
	}
	// У AniDub свой подзаголовок, у TMDB — свой; сходиться они должны.
	if got := anidubTitleMatch("Mushoku Tensei: Isekai Ittara Honki Dasu", "Mushoku Tensei: Jobless Reincarnation"); got != 1 {
		t.Fatalf("разные подзаголовки = %d", got)
	}
	// Несколько написаний оригинала через «/».
	if got := anidubTitleMatch("Kimi to Boku / Ты и я", "Kimi to Boku"); got != 2 {
		t.Fatalf("вариант через слэш = %d", got)
	}
	if got := anidubTitleMatch("Блич", "Ван-Пис"); got != 0 {
		t.Fatalf("чужое название = %d", got)
	}
}

func TestAnidubSeasonForPrefersTitle(t *testing.T) {
	// Страница говорит «Сезон 1» (студия нумерует внутри релиза), название —
	// «ТВ-2». Верить надо названию.
	card := anidubCard{rus: "Блич: Тысячелетняя кровавая война - Конфликт ТВ-2"}
	if got := anidubSeasonFor(card, &anidubShow{season: 1}); got != 2 {
		t.Fatalf("сезон = %d, хотели 2", got)
	}
	// Название номера не несёт — берём то, что сказала страница.
	if got := anidubSeasonFor(anidubCard{rus: "Необъятный океан"}, &anidubShow{season: 3}); got != 3 {
		t.Fatalf("сезон со страницы = %d, хотели 3", got)
	}
	if got := anidubSeasonFor(anidubCard{}, &anidubShow{}); got != 1 {
		t.Fatalf("умолчание = %d, хотели 1", got)
	}
}

func TestAnidubKindUnknownIsPermissive(t *testing.T) {
	// У части тайтлов в подписи выдачи стоит не категория, а «1 сезон» — вид
	// контента тогда неизвестен, и отбрасывать карточку нельзя: именно так
	// терялись свежие полнометражки.
	cards := []anidubCard{{url: "u", rus: "Недотрога", cat: "1 сезон", kind: anidubKindUnknown}}
	if got := anidubPickCards(cards, "Недотрога", "", 0, false); len(got) != 1 {
		t.Fatalf("фильм с неизвестным видом отброшен: %+v", got)
	}
	if got := anidubPickCards(cards, "Недотрога", "", 0, true); len(got) != 1 {
		t.Fatalf("сериал с неизвестным видом отброшен: %+v", got)
	}
	// А известный вид по-прежнему отсекает чужое.
	movie := []anidubCard{{url: "u", rus: "Недотрога", cat: "Аниме Фильмы", kind: anidubKindMovie}}
	if got := anidubPickCards(movie, "Недотрога", "", 0, true); len(got) != 0 {
		t.Fatalf("фильм попал на сериальную карточку: %+v", got)
	}
}

func TestAnidubTitleMarkerStripped(t *testing.T) {
	// «(фильм)» — пометка вида, в карточке TMDB её нет.
	if got := anidubTitleMatch("Играй, эуфониум! (фильм)", "Играй, Эуфониум!"); got != 2 {
		t.Fatalf("пометка вида помешала совпадению: %d", got)
	}
	if got := anidubTitleMatch("Стальной алхимик (ТВ)", "Стальной алхимик"); got != 2 {
		t.Fatalf("пометка «(ТВ)» помешала совпадению: %d", got)
	}
	// Осмысленные скобки не трогаем: это разные фильмы.
	if got := anidubTitleMatch("Призраки (2017)", "Призраки"); got == 2 {
		t.Fatalf("год в скобках принят за пометку вида: %d", got)
	}
}

func TestAnidubPrefixMatch(t *testing.T) {
	// Русские названия расходятся хвостом — узнаём по первым двум словам.
	if got := anidubTitleMatch("Рыцарь-скелет в ином мире", "Рыцарь-скелет вступает в параллельный мир"); got != 1 {
		t.Fatalf("тайтл с другим хвостом = %d", got)
	}
	// Но второе слово уже различает соседей по каталогу.
	if got := anidubTitleMatch("Магическая академия", "Магическая битва"); got != 0 {
		t.Fatalf("разные тайтлы совпали: %d", got)
	}
	// Одного общего слова мало.
	if got := anidubTitleMatch("Атака титанов", "Атака на Титан"); got != 0 {
		t.Fatalf("одно слово дало совпадение: %d", got)
	}
}

func TestAnidubShortQuery(t *testing.T) {
	cases := map[string]string{
		"Рыцарь-скелет вступает в параллельный мир":                       "рыцарь скелет",
		"Реинкарнация безработного: История о приключениях в другом мире": "",
		"Ван-Пис": "",
		"Блич":    "",
	}
	for in, want := range cases {
		got := normalizeSearchTitle(anidubShortQuery(in))
		if want != "" {
			want = normalizeSearchTitle(want)
		}
		if got != want {
			t.Fatalf("%q -> %q, хотели %q", in, got, want)
		}
	}
}
