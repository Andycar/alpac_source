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

// Фрагменты реальных страниц RuDub (play26.ru-dub.xyz), обрезанные до того,
// что читают парсеры.

const rudubCatalogFixture = `<ul class="catalog-list">
<li><a href="https://play26.ru-dub.xyz/mismatched/" >(Не)пара <span>6</span></a>
</li><li><a href="https://play26.ru-dub.xyz/112263/" >11.22.63 <span>9</span></a>
</li><li><a href="https://play26.ru-dub.xyz/12monkeys/" >12 обезьян <span>47</span></a>
</li><li><a href="https://play26.ru-dub.xyz/1670-serial/" >1670 <span>24</span></a>
</li><li><a href="https://play26.ru-dub.xyz/vigil/" >Виджил <span>18</span></a>
</li><li><a href="https://play26.ru-dub.xyz/vigilante/" >Линчеватель <span>8</span></a>
</li><li><a href="https://play26.ru-dub.xyz/zeitverbrechen/" >&quot;Цайт&quot;. Преступления <span>4</span></a>
</li></ul>`

const rudubShowPageFixture = `<div class="sect__content d-grid-items catalog-grid" id="dle-content">
<div class="poster grid-item">
	<div class="poster__img"><img src="/uploads/posts/2026-08/player-vigil-s3.webp" alt="Виджил"></div>
	<div class="poster__desc order-last">
		<a href="https://play26.ru-dub.xyz/vigil/video/105974" class="poster__link"><h3 class="poster__title">Виджил</h3></a>
		<div class="poster__meta ws-nowrap">3 сезон, 6 серия</div>
	</div>
</div>
<div class="poster grid-item">
	<div class="poster__img"><img src="/uploads/posts/2026-08/player-vigil-s3.webp" alt="Виджил"></div>
	<div class="poster__desc order-last">
		<a href="https://play26.ru-dub.xyz/vigil/video/105748" class="poster__link"><h3 class="poster__title">Виджил</h3></a>
		<div class="poster__meta ws-nowrap">3 сезон, 1 серия</div>
	</div>
</div>
<div class="poster grid-item">
	<div class="poster__img"><img src="/uploads/posts/2023-12/player-vigil-s2.webp" alt="Виджил"></div>
	<div class="poster__desc order-last">
		<a href="https://play26.ru-dub.xyz/vigil/video/86094" class="poster__link"><h3 class="poster__title">Виджил</h3></a>
		<div class="poster__meta ws-nowrap">2 сезон, 1 серия</div>
	</div>
</div>
<div class="poster grid-item">
	<div class="poster__img"><img src="/uploads/posts/2021-09/player-vigil-s1.webp" alt="Виджил"></div>
	<div class="poster__desc order-last">
		<a href="https://play26.ru-dub.xyz/vigil/video/69872" class="poster__link"><h3 class="poster__title">Виджил</h3></a>
		<div class="poster__meta ws-nowrap">1 сезон, 1 серия</div>
	</div>
</div>
</div>`

// ---------------------------------------------------------------------------
// Чистые парсеры
// ---------------------------------------------------------------------------

func TestRudubParseCatalog(t *testing.T) {
	items := rudubParseCatalog(rudubCatalogFixture)
	if len(items) != 7 {
		t.Fatalf("catalogue rows = %d, want 7", len(items))
	}
	if items[4].slug != "vigil" || items[4].name != "Виджил" || items[4].episodes != 18 {
		t.Fatalf("unexpected row: %+v", items[4])
	}
	// &quot; в названии разворачивается — иначе normalizeSearchTitle не сведёт
	// его с названием из карточки.
	if got := items[6].name; !strings.HasPrefix(got, `"Цайт"`) {
		t.Fatalf("unescaped name = %q", got)
	}
}

func TestRudubPickShow(t *testing.T) {
	items := rudubParseCatalog(rudubCatalogFixture)

	// Точное совпадение по русскому названию и по slug (=оригинал).
	item, ok := rudubPickShow(items, "Виджил", "Vigil")
	if !ok || item.slug != "vigil" {
		t.Fatalf("Виджил/Vigil -> %+v ok=%v", item, ok)
	}
	// «Vigil» не должен притянуть «Vigilante»: сравнение точное, не по префиксу.
	item, ok = rudubPickShow(items, "Линчеватель", "Vigilante")
	if !ok || item.slug != "vigilante" {
		t.Fatalf("Линчеватель -> %+v ok=%v", item, ok)
	}
	// Только оригинал (русского названия карточка не дала).
	item, ok = rudubPickShow(items, "", "12 Monkeys")
	if !ok || item.slug != "12monkeys" {
		t.Fatalf("12 Monkeys -> %+v ok=%v", item, ok)
	}
	// Хвост «-serial» в адресе не мешает совпасть с оригиналом.
	item, ok = rudubPickShow(items, "1670", "1670")
	if !ok || item.slug != "1670-serial" {
		t.Fatalf("1670 -> %+v ok=%v", item, ok)
	}
	// Чужого в каталоге нет — и выдумывать его нельзя.
	if item, ok = rudubPickShow(items, "Волчонок", "Teen Wolf"); ok {
		t.Fatalf("unknown show matched %+v", item)
	}
}

func TestRudubParseCards(t *testing.T) {
	cards := rudubParseCards(rudubShowPageFixture)
	if len(cards) != 4 {
		t.Fatalf("cards = %d, want 4", len(cards))
	}
	// Список идёт от свежих к старым — checkSearch опирается на первый элемент.
	if cards[0].season != 3 || cards[0].episode != 6 || cards[0].episodeID != "105974" {
		t.Fatalf("freshest card = %+v", cards[0])
	}
	if cards[3].season != 1 || cards[3].episodeID != "69872" {
		t.Fatalf("oldest card = %+v", cards[3])
	}
}

// ---------------------------------------------------------------------------
// Сквозной прогон против фикстурного сайта
// ---------------------------------------------------------------------------

type rudubFakeSite struct {
	server *httptest.Server
	hits   map[string]int
}

func newRudubFakeSite(t *testing.T) *rudubFakeSite {
	t.Helper()
	site := &rudubFakeSite{hits: map[string]int{}}

	mux := http.NewServeMux()
	mux.HandleFunc("/catalogue.html", func(w http.ResponseWriter, r *http.Request) {
		site.hits["catalogue"]++
		fmt.Fprint(w, rudubCatalogFixture)
	})
	mux.HandleFunc("/vigil/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/vigil/video/"):
			site.hits["episode"]++
			id := strings.TrimPrefix(r.URL.Path, "/vigil/video/")
			if id == "86094" {
				// Второй сезон остался на мёртвом плеере.
				fmt.Fprint(w, anividsDeadPlayerFixture)
				return
			}
			fmt.Fprintf(w, `<div class="page__player"><iframe src="%s/index.php?v=/s12/21809713df7c4d7590bbe0bba92e419a" width="610"></iframe></div>`, site.server.URL)
		case strings.HasPrefix(r.URL.Path, "/vigil/page/"):
			site.hits["page"]++
			fmt.Fprint(w, `<div id="dle-content"></div>`)
		default:
			site.hits["show"]++
			fmt.Fprint(w, rudubShowPageFixture)
		}
	})
	mux.HandleFunc("/index.php", func(w http.ResponseWriter, r *http.Request) {
		site.hits["playlist"]++
		fmt.Fprint(w, anividsPlaylistFixture(3))
	})
	mux.HandleFunc("/vid.php", func(w http.ResponseWriter, r *http.Request) {
		site.hits["vid"]++
		if r.Header.Get("Referer") == "" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegURL")
		fmt.Fprint(w, anividsMasterFixture)
	})

	site.server = httptest.NewServer(mux)
	t.Cleanup(site.server.Close)
	return site
}

func newRudubTestChecker(t *testing.T, site *rudubFakeSite) (*rudubChecker, http.HandlerFunc) {
	t.Helper()
	cfg := config.Config{Online: config.OnlineConfig{Rudub: config.RudubSource{
		Host:        site.server.URL,
		TrackerHost: site.server.URL,
	}}}
	checker := NewRudubChecker(cfg)
	return checker, checker.Handle(cfg, nil)
}

func rudubServe(t *testing.T, h http.HandlerFunc, target string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec.Body.String()
}

func TestRudubSeasonsAndEpisodes(t *testing.T) {
	site := newRudubFakeSite(t)
	_, h := newRudubTestChecker(t, site)

	q := "title=" + url.QueryEscape("Виджил") + "&original_title=Vigil&serial=1&rjson=true"
	body := rudubServe(t, h, "http://l/lite/rudub?"+q)

	if !strings.Contains(body, `"type":"season"`) {
		t.Fatalf("index did not answer with seasons: %s", body)
	}
	for _, want := range []string{"1 сезон", "3 сезон"} {
		if !strings.Contains(body, want) {
			t.Fatalf("season %q missing: %s", want, body)
		}
	}
	// Второй сезон висит на cdnmovies — показывать его нечестно.
	if strings.Contains(body, "2 сезон") {
		t.Fatalf("dead season served: %s", body)
	}

	body = rudubServe(t, h, "http://l/lite/rudub/serial?slug=vigil&s=3&rjson=true")
	if !strings.Contains(body, `"type":"episode"`) {
		t.Fatalf("serial did not answer with episodes: %s", body)
	}
	if !strings.Contains(body, "/lite/rudub/play?h=s10%2F82ed926165ca970566b1515f5231079a") {
		t.Fatalf("play link missing: %s", body)
	}
	if !strings.Contains(body, "q=1080p") {
		t.Fatalf("release quality not carried into play link: %s", body)
	}

	// Дерево кэшируется: второй заход по тем же сезонам сайт не трогает.
	showHits := site.hits["show"]
	rudubServe(t, h, "http://l/lite/rudub/serial?slug=vigil&s=1&rjson=true")
	if site.hits["show"] != showHits {
		t.Fatalf("show page refetched: %d -> %d", showHits, site.hits["show"])
	}
}

func TestRudubPlay(t *testing.T) {
	site := newRudubFakeSite(t)
	checker, h := newRudubTestChecker(t, site)
	// Плеерный хост балансер узнаёт из iframe — прогреваем дерево.
	rudubServe(t, h, "http://l/lite/rudub?title="+url.QueryEscape("Виджил")+"&original_title=Vigil&serial=1&rjson=true")
	if got := checker.playerHost(); got != site.server.URL {
		t.Fatalf("player host = %q, want %q", got, site.server.URL)
	}

	body := rudubServe(t, h, "http://l/lite/rudub/play?h="+url.QueryEscape("s10/82ed926165ca970566b1515f5231079a")+"&q=1080p")
	if !strings.Contains(body, `"method":"play"`) {
		t.Fatalf("play answer: %s", body)
	}
	if !strings.Contains(body, `"quality":"1080p"`) || !strings.Contains(body, `"quality":"480p"`) {
		t.Fatalf("stream qualities missing: %s", body)
	}
	if !strings.Contains(body, "/vid.php?v=/s10/") {
		t.Fatalf("master url missing: %s", body)
	}
}

func TestRudubPlayRejectsForeignHash(t *testing.T) {
	site := newRudubFakeSite(t)
	_, h := newRudubTestChecker(t, site)

	// ?h= приходит с нашей же страницы серий; всё, что не «sN/hash», не должно
	// превращаться в запрос на плеерный хост.
	for _, bad := range []string{"../../etc/passwd", "s10/..%2f..%2fadmin", "http://evil.example/x", ""} {
		body := rudubServe(t, h, "http://l/lite/rudub/play?h="+url.QueryEscape(bad))
		if strings.Contains(body, "vid.php") {
			t.Fatalf("hash %q accepted: %s", bad, body)
		}
	}
	if site.hits["vid"] != 0 {
		t.Fatalf("player contacted for a rejected hash: %d hits", site.hits["vid"])
	}
}

func TestRudubChecksearch(t *testing.T) {
	site := newRudubFakeSite(t)
	_, h := newRudubTestChecker(t, site)

	// Положительный ответ checksearch — это `{"type":"movie",…}`; отказ —
	// голое `{"rch":false}`.
	body := rudubServe(t, h, "http://l/lite/rudub?checksearch=true&serial=1&title="+url.QueryEscape("Виджил")+"&original_title=Vigil")
	if !strings.Contains(body, `"type":"movie"`) {
		t.Fatalf("checksearch for a known show: %s", body)
	}

	// Фильм: каталог сериальный, показывать источник не на чем.
	body = rudubServe(t, h, "http://l/lite/rudub?checksearch=true&serial=0&title="+url.QueryEscape("Виджил")+"&original_title=Vigil")
	if strings.Contains(body, `"type":"movie"`) {
		t.Fatalf("checksearch answered for a movie: %s", body)
	}

	body = rudubServe(t, h, "http://l/lite/rudub?checksearch=true&serial=1&title="+url.QueryEscape("Волчонок")+"&original_title=Teen+Wolf")
	if strings.Contains(body, `"type":"movie"`) {
		t.Fatalf("checksearch answered for an unknown show: %s", body)
	}
}

func TestRudubIndexSkipsMovies(t *testing.T) {
	site := newRudubFakeSite(t)
	_, h := newRudubTestChecker(t, site)

	body := rudubServe(t, h, "http://l/lite/rudub?title="+url.QueryEscape("Виджил")+"&original_title=Vigil&serial=0&rjson=true")
	if strings.Contains(body, "season") {
		t.Fatalf("movie card served: %s", body)
	}
	if site.hits["catalogue"] != 0 {
		t.Fatalf("catalogue fetched for a movie card: %d", site.hits["catalogue"])
	}
}

func TestRudubChecksearchProbeCached(t *testing.T) {
	site := newRudubFakeSite(t)
	_, h := newRudubTestChecker(t, site)

	target := "http://l/lite/rudub?checksearch=true&serial=1&title=" + url.QueryEscape("Виджил") + "&original_title=Vigil"
	if body := rudubServe(t, h, target); !strings.Contains(body, `"type":"movie"`) {
		t.Fatalf("first checksearch: %s", body)
	}
	first := site.hits["show"] + site.hits["episode"] + site.hits["playlist"]
	if first == 0 {
		t.Fatal("first checksearch made no requests")
	}

	// Повтор по той же карточке обязан прийти из кэша пробы: иначе популярный
	// сериал гонял бы три запроса к площадке на каждый опрос источника.
	if body := rudubServe(t, h, target); !strings.Contains(body, `"type":"movie"`) {
		t.Fatalf("second checksearch: %s", body)
	}
	if second := site.hits["show"] + site.hits["episode"] + site.hits["playlist"]; second != first {
		t.Fatalf("probe not cached: %d -> %d requests", first, second)
	}

	// Отрицательный ответ кэшируется так же.
	miss := "http://l/lite/rudub?checksearch=true&serial=1&title=" + url.QueryEscape("Волчонок") + "&original_title=Teen+Wolf"
	rudubServe(t, h, miss)
	before := site.hits["show"]
	rudubServe(t, h, miss)
	if site.hits["show"] != before {
		t.Fatalf("unknown card re-fetched: %d -> %d", before, site.hits["show"])
	}
}

func TestRudubInflightGate(t *testing.T) {
	site := newRudubFakeSite(t)
	checker, _ := newRudubTestChecker(t, site)

	// Ворота общие на процесс: параллельный резолв сезонов не должен уходить
	// на площадку шире, чем rudubMaxInflight.
	if cap(checker.gate) != rudubMaxInflight {
		t.Fatalf("gate cap = %d, want %d", cap(checker.gate), rudubMaxInflight)
	}
	if _, ok := checker.resolveShow(t.Context(), "vigil"); !ok {
		t.Fatal("resolveShow failed under the gate")
	}
	if len(checker.gate) != 0 {
		t.Fatalf("gate leaked %d slots", len(checker.gate))
	}
}
