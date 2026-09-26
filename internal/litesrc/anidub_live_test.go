package litesrc

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"lampac-go/internal/config"
)

// TestAnidubLive гоняет живой AniDUB от карточки до сегмента CDN.
// Запуск: ANIDUB_LIVE=1 go test ./internal/litesrc/ -run TestAnidubLive -v
func TestAnidubLive(t *testing.T) {
	if os.Getenv("ANIDUB_LIVE") != "1" {
		t.Skip("set ANIDUB_LIVE=1 to run the live anidub test")
	}
	cfg := config.Config{Online: config.OnlineConfig{Anidub: config.HostSource{Host: anidubDefaultHost}}}
	checker := NewAnidubChecker(cfg)
	h := checker.Handle(cfg, nil)

	serve := func(target string) string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		return rec.Body.String()
	}

	// Онгоинг: у AniDub свежие тайтлы играются, старые давно без плеера.
	card := "title=" + url.QueryEscape("Блич: Тысячелетняя кровавая война") +
		"&original_title=" + url.QueryEscape("Bleach: Thousand-Year Blood War") +
		"&year=2022&serial=1&original_language=ja&rjson=true"

	start := time.Now()
	body := serve("http://l/lite/anidub?" + card)
	t.Logf("index (%s): %s", time.Since(start).Round(time.Millisecond), truncURL(body))
	if !strings.Contains(body, `"type":"season"`) {
		t.Fatalf("сезоны не отданы: %s", body)
	}

	var seasons struct {
		Data []struct {
			URL  string `json:"url"`
			Name string `json:"name"`
			S    int    `json:"s"`
		} `json:"data"`
	}
	if err := stdjson.Unmarshal([]byte(body), &seasons); err != nil {
		t.Fatalf("season json: %v", err)
	}
	if len(seasons.Data) == 0 {
		t.Fatal("пустой список сезонов")
	}
	t.Logf("сезонов: %d", len(seasons.Data))

	body = serve(seasons.Data[0].URL)
	var episodes struct {
		Data []struct {
			URL   string `json:"url"`
			Title string `json:"title"`
			S, E  int
		} `json:"data"`
	}
	if err := stdjson.Unmarshal([]byte(body), &episodes); err != nil {
		t.Fatalf("episode json: %v", err)
	}
	if len(episodes.Data) == 0 {
		t.Fatalf("пустой список серий для %s: %s", seasons.Data[0].Name, body)
	}
	t.Logf("%s: %d серий", seasons.Data[0].Name, len(episodes.Data))

	body = serve(episodes.Data[0].URL)
	var play struct {
		Title         string `json:"title"`
		Translate     string `json:"translate"`
		StreamQuality []struct {
			Quality string `json:"quality"`
			URL     string `json:"url"`
		} `json:"streamquality"`
	}
	if err := stdjson.Unmarshal([]byte(body), &play); err != nil {
		t.Fatalf("play json: %v", err)
	}
	if len(play.StreamQuality) == 0 {
		t.Fatalf("качеств нет: %s", body)
	}
	t.Logf("play: title=%q translate=%q качества=%d", play.Title, play.Translate, len(play.StreamQuality))

	// Тот же CDN, что у RuDub: обязателен User-Agent, Referer не проверяется.
	player := checker.playerHost()
	variant := play.StreamQuality[0].URL
	media, code := rudubLiveGet(t, variant, player+"/")
	if code != http.StatusOK || !strings.Contains(media, "#EXTINF") {
		t.Fatalf("вариант %s: code=%d body=%.120s", play.StreamQuality[0].Quality, code, media)
	}
	t.Logf("плейлист варианта %s: %d байт", play.StreamQuality[0].Quality, len(media))
}

// TestAnidubLiveMovie — полнометражка: у неё нет плейлиста сезона, источник
// обязан отдать единственный поток, а не пустоту.
func TestAnidubLiveMovie(t *testing.T) {
	if os.Getenv("ANIDUB_LIVE") != "1" {
		t.Skip("set ANIDUB_LIVE=1 to run the live anidub test")
	}
	cfg := config.Config{Online: config.OnlineConfig{Anidub: config.HostSource{Host: anidubDefaultHost}}}
	h := NewAnidubChecker(cfg).Handle(cfg, nil)

	rec := httptest.NewRecorder()
	// Свежая полнометражка — у старых тайтлов плеера уже нет.
	target := "http://l/lite/anidub?title=" + url.QueryEscape("Играй, Эуфониум! Финал") +
		"&original_title=" + url.QueryEscape("Hibike! Euphonium") + "&rjson=true"
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	body := rec.Body.String()
	t.Logf("movie: %s", truncURL(body))
	if !strings.Contains(body, `"type":"movie"`) {
		t.Fatalf("фильм не отдан: %s", body)
	}
}
