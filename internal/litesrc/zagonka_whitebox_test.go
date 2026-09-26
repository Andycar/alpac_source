package litesrc

import (
	stdjson "encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

// Страница kinobadi.in/player/player.php?kp_id=N: всё лежит в data-sources
// (HTML-экранированный JSON). Фикстуры — урезанные слепки настоящих ответов
// за 20.09.2026: «Матрица» (фильм, 5 озвучек) и «Игра престолов» (сериал,
// в сезоне две озвучки).
func zagonkaPage(sources string) string {
	return `<!DOCTYPE html><html><head><title>Player</title></head><body>
<div class="dl-player-wrap dlv2-player" data-sources="` + html.EscapeString(sources) + `"></div>
</body></html>`
}

const zagonkaMovieJSON = `{"kind":"movie","voices":[
 {"name":"РТР","qualities":{"240p":"https://mpa.example/movies/a/tok:2099010100/240.mp4:hls:manifest.m3u8","720p":"https://mpa.example/movies/a/tok:2099010100/720.mp4:hls:manifest.m3u8","480p":"https://mpa.example/movies/a/tok:2099010100/480.mp4:hls:manifest.m3u8"}},
 {"name":"Гоблин","qualities":{"360p":"https://mpa.example/movies/b/tok:2099010100/360.mp4:hls:manifest.m3u8"}},
 {"name":"Пустая","qualities":{}}
]}`

const zagonkaSeriesJSON = `{"kind":"series","seasons":[
 {"num":1,"voices":[
   {"name":"Rezka","episodes":[{"num":1,"qualities":{"480p":"https://mpa.example/tv/r/tok:2099010100/480.mp4:hls:manifest.m3u8","720p":"https://mpa.example/tv/r/tok:2099010100/720.mp4:hls:manifest.m3u8"}},{"num":2,"qualities":{"720p":"https://mpa.example/tv/r2/tok:2099010100/720.mp4:hls:manifest.m3u8"}}]},
   {"name":"Кравец","episodes":[{"num":1,"qualities":{"720p":"https://mpa.example/tv/k/tok:2099010100/720.mp4:hls:manifest.m3u8"}}]}
 ]},
 {"num":2,"voices":[
   {"name":"Rezka","episodes":[{"num":1,"qualities":{"720p":"https://mpa.example/tv/r3/tok:2099010100/720.mp4:hls:manifest.m3u8"}}]}
 ]}
]}`

func newZagonkaTestServer(t *testing.T) (*zagonkaChecker, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("kp_id") {
		case "301":
			_, _ = fmt.Fprint(w, zagonkaPage(zagonkaMovieJSON))
		case "464963":
			_, _ = fmt.Fprint(w, zagonkaPage(zagonkaSeriesJSON))
		default:
			// Неизвестная карточка: страница есть, атрибута нет.
			_, _ = fmt.Fprint(w, `<!DOCTYPE html><html><body><div class="dl-player-wrap"></div></body></html>`)
		}
	}))
	t.Cleanup(srv.Close)
	cfg := config.Config{}
	cfg.Online.Zagonka.Host = srv.URL
	z := NewZagonkaChecker(cfg)
	z.client = srv.Client()
	return z, srv
}

func zagonkaGet(t *testing.T, z *zagonkaChecker, path string) (int, map[string]any, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	z.Handle(config.Config{}, nil).ServeHTTP(rec, req)
	body := rec.Body.String()
	var m map[string]any
	_ = stdjson.Unmarshal([]byte(body), &m)
	return rec.Code, m, body
}

func TestZagonkaChecksearchDistinguishesKind(t *testing.T) {
	z, _ := newZagonkaTestServer(t)
	cases := []struct {
		path string
		want bool
	}{
		{"/lite/zagonka?checksearch=true&kinopoisk_id=301", true},
		{"/lite/zagonka?checksearch=true&kinopoisk_id=301&serial=1", false}, // фильм под сериальным запросом — промах
		{"/lite/zagonka?checksearch=true&kinopoisk_id=464963&serial=1", true},
		{"/lite/zagonka?checksearch=true&kinopoisk_id=464963", false},
		{"/lite/zagonka?checksearch=true&kinopoisk_id=999", false},
		{"/lite/zagonka?checksearch=true", false},
	}
	for _, c := range cases {
		_, _, body := zagonkaGet(t, z, c.path)
		got := strings.Contains(body, `"type":"movie"`)
		if got != c.want {
			t.Errorf("%s: %s (ожидали show=%v)", c.path, body, c.want)
		}
	}
}

func TestZagonkaMovieRows(t *testing.T) {
	z, _ := newZagonkaTestServer(t)
	_, m, body := zagonkaGet(t, z, "/lite/zagonka?kinopoisk_id=301&title=%D0%9C%D0%B0%D1%82%D1%80%D0%B8%D1%86%D0%B0&original_title=The+Matrix&rjson=true")
	if m["type"] != "movie" {
		t.Fatalf("type=%v, тело: %s", m["type"], body)
	}
	rows, _ := m["data"].([]any)
	// Озвучка без единого качества не должна становиться строкой.
	if len(rows) != 2 {
		t.Fatalf("строк %d, ожидали 2: %s", len(rows), body)
	}
	first := rows[0].(map[string]any)
	if first["name"] != "РТР" || first["method"] != "play" {
		t.Fatalf("первая строка: %v", first)
	}
	// Лучшее качество — 720p — в url; прокси не подключён, поэтому ссылка сырая.
	if !strings.HasSuffix(first["url"].(string), "/720.mp4:hls:manifest.m3u8") {
		t.Fatalf("url не 720p: %v", first["url"])
	}
	q := first["quality"].(map[string]any)
	if len(q) != 3 {
		t.Fatalf("карта качеств: %v", q)
	}
	if first["title"] != "Матрица / The Matrix" {
		t.Fatalf("title: %v", first["title"])
	}
}

func TestZagonkaSeriesVoiceSeasonEpisodePlay(t *testing.T) {
	z, _ := newZagonkaTestServer(t)

	// 1. Вход в сериал — сезоны активной озвучки (Rezka: два сезона) и
	// переключатель озвучек рядом. Одни кнопки без сезонов Лампа считала
	// пустым ответом: «поиск не дал результатов».
	_, m, body := zagonkaGet(t, z, "/lite/zagonka?kinopoisk_id=464963&serial=1&title=%D0%98%D0%B3%D1%80%D0%B0&rjson=true")
	if m["type"] != "season" {
		t.Fatalf("на входе ожидались сезоны, type=%v: %s", m["type"], body)
	}
	if ss, _ := m["data"].([]any); len(ss) != 2 {
		t.Fatalf("у Rezka ожидали 2 сезона на входе: %s", body)
	}
	voices, _ := m["voice"].([]any)
	if len(voices) != 2 || voices[0].(map[string]any)["name"] != "Rezka" || voices[0].(map[string]any)["active"] != true {
		t.Fatalf("переключатель озвучек: %s", body)
	}
	link := voices[1].(map[string]any)["url"].(string) // Кравец
	if !strings.Contains(link, "/lite/zagonka?") || !strings.Contains(link, "rjson=true") {
		t.Fatalf("ссылка озвучки: %s", link)
	}
	// HTML-вид (так спрашивает Лампа): обязан содержать элемент сезона.
	_, _, html := zagonkaGet(t, z, "/lite/zagonka?kinopoisk_id=464963&serial=1&title=T")
	if !strings.Contains(html, "videos__item") {
		t.Fatalf("в HTML нет ни одного элемента контента: %s", html)
	}

	// 2. Сезоны выбранной озвучки: у «Кравец» только первый сезон.
	_, m, body = zagonkaGet(t, z, link[strings.Index(link, "/lite/"):])
	if m["type"] != "season" {
		t.Fatalf("type=%v: %s", m["type"], body)
	}
	seasons, _ := m["data"].([]any)
	if len(seasons) != 1 {
		t.Fatalf("у Кравца ожидали 1 сезон: %s", body)
	}
	s1 := seasons[0].(map[string]any)
	if s1["s"] != float64(1) || s1["id"] != float64(1) {
		t.Fatalf("номер сезона обязан быть и в s, и в id: %v", s1)
	}

	// 3. Серии сезона.
	_, m, body = zagonkaGet(t, z, s1["url"].(string)[strings.Index(s1["url"].(string), "/lite/"):])
	if m["type"] != "episode" {
		t.Fatalf("type=%v: %s", m["type"], body)
	}
	eps, _ := m["data"].([]any)
	if len(eps) != 1 {
		t.Fatalf("у Кравца в 1 сезоне ожидали 1 серию: %s", body)
	}
	ep := eps[0].(map[string]any)
	if ep["method"] != "call" || ep["translate"] != "Кравец" || ep["e"] != float64(1) {
		t.Fatalf("строка серии: %v", ep)
	}

	// 4. Поток: название сериала обязано доехать до play (wrong-film-гард capi).
	_, m, body = zagonkaGet(t, z, ep["url"].(string)[strings.Index(ep["url"].(string), "/lite/"):])
	if m["method"] != "play" || m["title"] != "Игра" || m["translate"] != "Кравец" {
		t.Fatalf("play: %s", body)
	}
	if !strings.HasSuffix(m["url"].(string), "/tv/k/tok:2099010100/720.mp4:hls:manifest.m3u8") {
		t.Fatalf("play url: %v", m["url"])
	}

	// 5. Rezka: два сезона — фильтр по озвучке работает в обе стороны.
	_, m, _ = zagonkaGet(t, z, "/lite/zagonka/serial?kinopoisk_id=464963&v=Rezka&rjson=true")
	if seasons, _ := m["data"].([]any); len(seasons) != 2 {
		t.Fatalf("у Rezka ожидали 2 сезона, получили %d", len(seasons))
	}
}

func TestZagonkaExpiredStampForcesRefetch(t *testing.T) {
	// Штамп в прошлом → play перечитывает страницу; вторая версия страницы
	// несёт свежую ссылку, и именно она должна уйти зрителю.
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		stamp := "2000010100"
		if hits > 1 {
			stamp = "2099010100"
		}
		js := `{"kind":"series","seasons":[{"num":1,"voices":[{"name":"Rezka","episodes":[{"num":1,"qualities":{"720p":"https://mpa.example/tv/x/tok:` + stamp + `/720.mp4:hls:manifest.m3u8"}}]}]}]}`
		_, _ = fmt.Fprint(w, zagonkaPage(js))
	}))
	defer srv.Close()
	cfg := config.Config{}
	cfg.Online.Zagonka.Host = srv.URL
	z := NewZagonkaChecker(cfg)
	z.client = srv.Client()

	_, m, body := zagonkaGet(t, z, "/lite/zagonka/play?kinopoisk_id=1&v=Rezka&s=1&e=1")
	if hits != 2 {
		t.Fatalf("страницу должны были перечитать (hits=%d): %s", hits, body)
	}
	if !strings.Contains(m["url"].(string), ":2099010100/") {
		t.Fatalf("ушла протухшая ссылка: %v", m["url"])
	}
}

func TestZagonkaQualityOrderAndStamp(t *testing.T) {
	got := zagonkaSortedQualities(map[string]string{"360p": "u", "1080p": "u", "480p": "", "720p": "u"})
	if strings.Join(got, ",") != "1080p,720p,360p" {
		t.Fatalf("порядок качеств: %v", got)
	}
	if !zagonkaExpired(map[string]string{"720p": "https://h/x/tok:2000010100/720.mp4"}) {
		t.Error("штамп 2000 года должен считаться протухшим")
	}
	if zagonkaExpired(map[string]string{"720p": "https://h/x/tok:2099010100/720.mp4"}) {
		t.Error("штамп 2099 года ещё жив")
	}
	if zagonkaExpired(map[string]string{"720p": "https://h/x/без-штампа.m3u8"}) {
		t.Error("ссылка без штампа не считается протухшей")
	}
}

// Фильтр «Перевод» Лампа строит из кнопок только на уровне серий. Кнопки —
// озвучки, у которых есть ЭТОТ сезон, и ведут на него же в другой озвучке.
func TestZagonkaEpisodesCarryVoiceFilter(t *testing.T) {
	z, _ := newZagonkaTestServer(t)

	// 1 сезон есть у Rezka и у Кравца → две кнопки, активна текущая.
	_, _, html := zagonkaGet(t, z, "/lite/zagonka/serial?kinopoisk_id=464963&v=Rezka&s=1&t=T")
	if strings.Count(html, "videos__button") != 2 {
		t.Fatalf("ожидали 2 кнопки озвучек над сериями 1 сезона: %s", html)
	}
	if !strings.Contains(html, "videos__item") {
		t.Fatalf("серии пропали: %s", html)
	}
	// В data-json амперсанд экранирован как \u0026.
	if !strings.Contains(html, "v=%D0%9A%D1%80%D0%B0%D0%B2%D0%B5%D1%86"+`\u0026`+"s=1") {
		t.Fatalf("кнопка Кравца должна вести на тот же 1 сезон: %s", html)
	}
	// 2 сезон — только у Rezka: одна озвучка, фильтр не нужен.
	_, _, html2 := zagonkaGet(t, z, "/lite/zagonka/serial?kinopoisk_id=464963&v=Rezka&s=2&t=T")
	if strings.Contains(html2, "videos__button") {
		t.Fatalf("у сезона с одной озвучкой кнопок быть не должно: %s", html2)
	}
}
