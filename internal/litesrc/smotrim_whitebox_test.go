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

// Фрагменты живых страниц smotrim.ru, обрезанные до того, что читают парсеры.

const smotrimIndexFixture = `<div class="sitemap">
<a href="/brand/10001">Салями (2011)</a>
<a href="/brand/60800">Челночницы сериал 2016 смотреть онлайн</a>
<a href="/brand/22245">Матушка Георгия (2010)</a>
<a href="/brand/58458">Екатерина сериал смотреть онлайн бесплатно</a>
<a href="/brand/5415">Евдокия (1961)</a>
</div>`

const smotrimBrandFixture = `<main>
<h1>Челночницы</h1>
<a href="/brand/60800/season-1">Сезон 1</a>
<a href="/brand/60800/season-2">Сезон 2</a>
</main>`

const smotrimSeasonFixture = `<div class="episodes">
<a href="/video/1578545">Серия 1</a>
<a href="/video/1578550">Серия 2</a>
<a href="/video/1579015">Серия 3</a>
</div>`

const smotrimMasterFixture = `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-STREAM-INF:BANDWIDTH=4050000
chunklist_b4050000.m3u8?entity=episode&id=2843491&sign=abc
#EXT-X-STREAM-INF:BANDWIDTH=1800000
chunklist_b1800000.m3u8?entity=episode&id=2843491&sign=abc
`

func smotrimVideoFixture(status, tariff, masterURL string) string {
	streams := `"streams":{"m3u8":"` + masterURL + `"},`
	if status != "OK" {
		// Площадка не отдаёт ссылок на подписочное видео вовсе.
		streams = `"streams":{"m3u8":null},`
	}
	return fmt.Sprintf(`{"status":%q,"data":{%s
		"qualities":[{"quality":1080,"name":"1080p","bandwidth":4050000},{"quality":720,"name":"720p","bandwidth":1800000}],
		"subtitles":[{"code":"ru","title":"Русский","vtt":"https://vod.smotrim.ru/subtitle/1-ru.vtt"}],
		"episode":{"number":1,"tariff":%q,"season":{"number":1}},
		"brand":{"title":"Челночницы","type":"serial"}}}`, status, streams, tariff)
}

// ---------------------------------------------------------------------------
// Индекс и подбор
// ---------------------------------------------------------------------------

func TestSmotrimParseIndex(t *testing.T) {
	brands := smotrimParseIndex(smotrimIndexFixture)
	if len(brands) != 5 {
		t.Fatalf("брендов = %d, хотели 5", len(brands))
	}
	byID := map[string]smotrimBrand{}
	for _, b := range brands {
		byID[b.id] = b
	}
	// Год живёт в скобках — он сильный признак при одинаковых названиях.
	if got := byID["10001"]; got.title != "салями" || got.year != 2011 {
		t.Fatalf("строка с годом разобрана неверно: %+v", got)
	}
	// Витринная обвязка снимается: «Челночницы сериал 2016 смотреть онлайн».
	if got := byID["60800"]; got.title != "челночницы" {
		t.Fatalf("название не очищено: %q", got.title)
	}
	if got := byID["58458"]; got.title != "екатерина" {
		t.Fatalf("хвост «смотреть онлайн бесплатно» не снят: %q", got.title)
	}
}

func TestSmotrimCleanTitle(t *testing.T) {
	cases := map[string]string{
		"Сериал Овчарка 2024 смотреть онлайн бесплатно": "овчарка",
		"Программа Монастырская кухня":                  "монастырская кухня",
		"Фильм Любовь как несчастный случай (2012)":     "любовь как несчастный случай",
		"Салями (2011)": "салями",
	}
	for in, want := range cases {
		if got := smotrimCleanTitle(in); got != want {
			t.Fatalf("%q -> %q, хотели %q", in, got, want)
		}
	}
}

func TestSmotrimPickBrandUsesYear(t *testing.T) {
	brands := []smotrimBrand{
		{id: "1", title: "анна каренина", year: 1967},
		{id: "2", title: "анна каренина", year: 2017},
	}
	// Одноимённых экранизаций в архиве много — год решает.
	if got, ok := smotrimPickBrand(brands, "Анна Каренина", "", 2017); !ok || got.id != "2" {
		t.Fatalf("год не учтён: %+v ok=%v", got, ok)
	}
	if got, ok := smotrimPickBrand(brands, "Анна Каренина", "", 1967); !ok || got.id != "1" {
		t.Fatalf("год не учтён: %+v ok=%v", got, ok)
	}
	// Чужого выдумывать нельзя.
	if got, ok := smotrimPickBrand(brands, "Война и мир", "", 1967); ok {
		t.Fatalf("подобран посторонний бренд: %+v", got)
	}
}

func TestSmotrimParseSeasonsAndVideos(t *testing.T) {
	if got := smotrimParseSeasons(smotrimBrandFixture); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("сезоны разобраны неверно: %v", got)
	}
	vids := smotrimParseVideos(smotrimSeasonFixture)
	if len(vids) != 3 || vids[0] != "1578545" || vids[2] != "1579015" {
		t.Fatalf("серии разобраны неверно: %v", vids)
	}
}

func TestSmotrimParseMaster(t *testing.T) {
	master := "https://vod.smotrim.ru/vod/definst/amlst:2843491/playlist.m3u8?entity=episode&id=2843491&sign=abc"
	variants := smotrimParseMaster(smotrimMasterFixture, master)
	if len(variants) != 2 {
		t.Fatalf("вариантов = %d, хотели 2", len(variants))
	}
	// Путь достраивается от master, а подпись варианты несут свою.
	want := "https://vod.smotrim.ru/vod/definst/amlst:2843491/chunklist_b4050000.m3u8?entity=episode&id=2843491&sign=abc"
	if variants[4050000] != want {
		t.Fatalf("вариант 4050000 = %q", variants[4050000])
	}
}

// ---------------------------------------------------------------------------
// Сквозной прогон против фикстурного сайта
// ---------------------------------------------------------------------------

type smotrimFakeSite struct {
	server *httptest.Server
	hits   map[string]int
	status string // что отвечает плеерное API
	tariff string
}

func newSmotrimFakeSite(t *testing.T, status, tariff string) *smotrimFakeSite {
	t.Helper()
	site := &smotrimFakeSite{hits: map[string]int{}, status: status, tariff: tariff}
	mux := http.NewServeMux()

	mux.HandleFunc("/sitemap", func(w http.ResponseWriter, r *http.Request) {
		site.hits["index"]++
		fmt.Fprint(w, smotrimIndexFixture)
	})
	mux.HandleFunc("/brand/", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/season-") {
			site.hits["season"]++
			fmt.Fprint(w, smotrimSeasonFixture)
			return
		}
		site.hits["brand"]++
		fmt.Fprint(w, smotrimBrandFixture)
	})
	mux.HandleFunc("/api/v1/video/", func(w http.ResponseWriter, r *http.Request) {
		site.hits["video"]++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, smotrimVideoFixture(site.status, site.tariff, site.server.URL+"/playlist.m3u8?sign=abc"))
	})
	mux.HandleFunc("/playlist.m3u8", func(w http.ResponseWriter, r *http.Request) {
		site.hits["master"]++
		fmt.Fprint(w, smotrimMasterFixture)
	})

	site.server = httptest.NewServer(mux)
	t.Cleanup(site.server.Close)
	return site
}

func newSmotrimTestChecker(t *testing.T, site *smotrimFakeSite) (*smotrimChecker, http.HandlerFunc) {
	t.Helper()
	cfg := config.Config{Online: config.OnlineConfig{Smotrim: config.SmotrimSource{
		Host:      site.server.URL,
		PlayerAPI: site.server.URL,
	}}}
	checker := NewSmotrimChecker(cfg)
	return checker, checker.Handle(cfg, nil)
}

func smotrimServe(t *testing.T, h http.HandlerFunc, target string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec.Body.String()
}

func TestSmotrimSeasonsAndEpisodes(t *testing.T) {
	site := newSmotrimFakeSite(t, "OK", "public_content")
	_, h := newSmotrimTestChecker(t, site)

	q := "title=" + url.QueryEscape("Челночницы") + "&year=2016&serial=1&rjson=true"
	body := smotrimServe(t, h, "http://l/lite/smotrim?"+q)
	if !strings.Contains(body, `"type":"season"`) || !strings.Contains(body, `"s":2`) {
		t.Fatalf("сезоны не отданы: %s", body)
	}

	body = smotrimServe(t, h, "http://l/lite/smotrim/serial?b=60800&s=1&rjson=true")
	if !strings.Contains(body, `"type":"episode"`) {
		t.Fatalf("серии не отданы: %s", body)
	}
	// Порядок на странице сезона = номера серий.
	if !strings.Contains(body, `"e":1`) || !strings.Contains(body, `"e":3`) {
		t.Fatalf("номера серий не проставлены: %s", body)
	}
	if !strings.Contains(body, "/lite/smotrim/play?v=1578545") {
		t.Fatalf("ссылка на воспроизведение отсутствует: %s", body)
	}
}

func TestSmotrimPlayOpenContent(t *testing.T) {
	site := newSmotrimFakeSite(t, "OK", "public_content")
	_, h := newSmotrimTestChecker(t, site)

	body := smotrimServe(t, h, "http://l/lite/smotrim/play?v=1578545&t="+url.QueryEscape("Челночницы"))
	if !strings.Contains(body, `"method":"play"`) {
		t.Fatalf("ответ play: %s", body)
	}
	// Качества подписаны по таблице API, сопоставленной с полосой варианта.
	if !strings.Contains(body, `"quality":"1080p"`) || !strings.Contains(body, `"quality":"720p"`) {
		t.Fatalf("качества не собраны: %s", body)
	}
	if !strings.Contains(body, `"subtitles"`) {
		t.Fatalf("субтитры потеряны: %s", body)
	}
	if !strings.Contains(body, `"title":"Челночницы"`) {
		t.Fatalf("title должен нести название карточки: %s", body)
	}
}

// Главная граница источника: подписочное видео не отдаётся ни при каких
// обстоятельствах — ни ссылкой, ни «пустым» плеером с master-плейлистом.
func TestSmotrimRefusesSubscriptionContent(t *testing.T) {
	site := newSmotrimFakeSite(t, "VIDEO_DISALLOW_TARIFF", "subscription_content")
	_, h := newSmotrimTestChecker(t, site)

	body := smotrimServe(t, h, "http://l/lite/smotrim/play?v=1578545")
	if strings.Contains(body, "m3u8") || strings.Contains(body, `"method":"play"`) {
		t.Fatalf("подписочное видео просочилось в ответ: %s", body)
	}
	if site.hits["master"] != 0 {
		t.Fatalf("за потоком подписочного видео ходили %d раз", site.hits["master"])
	}

	// И checksearch на таком бренде обязан молчать, иначе источник светится
	// на карточке, где показать нечего.
	body = smotrimServe(t, h, "http://l/lite/smotrim?checksearch=true&serial=1&title="+url.QueryEscape("Челночницы")+"&year=2016")
	if strings.Contains(body, `"type":"movie"`) {
		t.Fatalf("checksearch ответил на подписочном бренде: %s", body)
	}
}

// Тариф важнее статуса: если площадка когда-нибудь ответит OK на подписочную
// серию, отдавать её всё равно нельзя.
func TestSmotrimRefusesPaidTariffEvenOnOK(t *testing.T) {
	site := newSmotrimFakeSite(t, "OK", "subscription_content")
	_, h := newSmotrimTestChecker(t, site)

	body := smotrimServe(t, h, "http://l/lite/smotrim/play?v=1578545")
	if strings.Contains(body, "m3u8") || strings.Contains(body, `"method":"play"`) {
		t.Fatalf("подписочный тариф просочился: %s", body)
	}
}

func TestSmotrimPlayRejectsForeignID(t *testing.T) {
	site := newSmotrimFakeSite(t, "OK", "public_content")
	_, h := newSmotrimTestChecker(t, site)

	for _, bad := range []string{"../../etc/passwd", "http://evil.example/x", "abc", ""} {
		body := smotrimServe(t, h, "http://l/lite/smotrim/play?v="+url.QueryEscape(bad))
		if strings.Contains(body, "m3u8") {
			t.Fatalf("принят чужой идентификатор %q: %s", bad, body)
		}
	}
	if site.hits["video"] != 0 {
		t.Fatalf("плеерное API дёрнуто для отвергнутого идентификатора: %d", site.hits["video"])
	}
}

func TestSmotrimChecksearchCachesProbe(t *testing.T) {
	site := newSmotrimFakeSite(t, "OK", "public_content")
	_, h := newSmotrimTestChecker(t, site)

	target := "http://l/lite/smotrim?checksearch=true&serial=1&title=" + url.QueryEscape("Челночницы") + "&year=2016"
	if body := smotrimServe(t, h, target); !strings.Contains(body, `"type":"movie"`) {
		t.Fatalf("первый checksearch: %s", body)
	}
	first := site.hits["index"] + site.hits["season"] + site.hits["video"]
	if body := smotrimServe(t, h, target); !strings.Contains(body, `"type":"movie"`) {
		t.Fatalf("второй checksearch: %s", body)
	}
	if second := site.hits["index"] + site.hits["season"] + site.hits["video"]; second != first {
		t.Fatalf("проба не закэширована: %d -> %d запросов", first, second)
	}
}

func TestSmotrimYearIsHardFilter(t *testing.T) {
	// Архив вещателя полон общих названий, и на одном имени карточка Lost
	// (2004) подбирала российский фильм 2018 года. Расхождение года должно
	// отсекать кандидата, а не штрафовать его.
	brands := []smotrimBrand{{id: "62789", title: "остаться в живых", year: 2018}}
	if got, ok := smotrimPickBrand(brands, "Остаться в живых", "Lost", 2004); ok {
		t.Fatalf("подобран чужой тайтл: %+v", got)
	}
	// Тот же год — берём.
	if _, ok := smotrimPickBrand(brands, "Остаться в живых", "", 2018); !ok {
		t.Fatal("совпадающий год отвергнут")
	}
	// Год не известен ни одной стороне — решает название.
	if _, ok := smotrimPickBrand([]smotrimBrand{{id: "1", title: "овчарка"}}, "Овчарка", "", 0); !ok {
		t.Fatal("без года подбор по названию сломался")
	}
}
