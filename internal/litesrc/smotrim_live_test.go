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

// TestSmotrimLive гоняет живой «Смотрим» от карточки до плейлиста.
// Запуск: SMOTRIM_LIVE=1 go test ./internal/litesrc/ -run TestSmotrimLive -v
func TestSmotrimLive(t *testing.T) {
	if os.Getenv("SMOTRIM_LIVE") != "1" {
		t.Skip("set SMOTRIM_LIVE=1 to run the live smotrim test")
	}
	cfg := config.Config{Online: config.OnlineConfig{Smotrim: config.SmotrimSource{
		Host:      smotrimDefaultHost,
		PlayerAPI: smotrimDefaultPlayerAPI,
	}}}
	checker := NewSmotrimChecker(cfg)
	h := checker.Handle(cfg, nil)

	serve := func(target string) string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		return rec.Body.String()
	}

	// Архивный сериал ВГТРК — открытая часть каталога.
	card := "title=" + url.QueryEscape("Возвращение домой") + "&year=2011&serial=1&rjson=true"
	start := time.Now()
	body := serve("http://l/lite/smotrim?" + card)
	t.Logf("index (%s): %s", time.Since(start).Round(time.Millisecond), truncURL(body))
	if !strings.Contains(body, `"type":"season"`) {
		t.Fatalf("сезоны не отданы: %s", body)
	}

	var seasons struct {
		Data []struct {
			URL  string `json:"url"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := stdjson.Unmarshal([]byte(body), &seasons); err != nil || len(seasons.Data) == 0 {
		t.Fatalf("season json: %v / %s", err, body)
	}
	t.Logf("сезонов: %d", len(seasons.Data))

	body = serve(seasons.Data[0].URL)
	var episodes struct {
		Data []struct {
			URL string `json:"url"`
			E   int    `json:"e"`
		} `json:"data"`
	}
	if err := stdjson.Unmarshal([]byte(body), &episodes); err != nil || len(episodes.Data) == 0 {
		t.Fatalf("episode json: %v / %s", err, truncURL(body))
	}
	t.Logf("%s: %d серий", seasons.Data[0].Name, len(episodes.Data))

	body = serve(episodes.Data[0].URL)
	var play struct {
		URL           string `json:"url"`
		Title         string `json:"title"`
		StreamQuality []struct {
			Quality string `json:"quality"`
			URL     string `json:"url"`
		} `json:"streamquality"`
		Subtitles []map[string]string `json:"subtitles"`
	}
	if err := stdjson.Unmarshal([]byte(body), &play); err != nil {
		t.Fatalf("play json: %v / %s", err, truncURL(body))
	}
	if len(play.StreamQuality) == 0 {
		t.Fatalf("качеств нет: %s", truncURL(body))
	}
	t.Logf("play: качеств %d (верхнее %s), субтитров %d",
		len(play.StreamQuality), play.StreamQuality[0].Quality, len(play.Subtitles))

	// Поток у площадки открывается без заголовков — проверяем как есть.
	media, code := rudubLiveGet(t, play.StreamQuality[0].URL, "")
	if code != http.StatusOK || !strings.Contains(media, "#EXTINF") {
		t.Fatalf("вариант %s: code=%d body=%.120s", play.StreamQuality[0].Quality, code, media)
	}
	t.Logf("плейлист варианта: %d байт", len(media))
}

// Подписочный контент источник обязан пропускать и на живой площадке.
func TestSmotrimLiveSkipsPaid(t *testing.T) {
	if os.Getenv("SMOTRIM_LIVE") != "1" {
		t.Skip("set SMOTRIM_LIVE=1 to run the live smotrim test")
	}
	cfg := config.Config{Online: config.OnlineConfig{Smotrim: config.SmotrimSource{
		Host:      smotrimDefaultHost,
		PlayerAPI: smotrimDefaultPlayerAPI,
	}}}
	checker := NewSmotrimChecker(cfg)

	// «Челночницы» — премьерный сериал площадки, серии закрыты подпиской.
	if _, ok := checker.resolveVideo(t.Context(), "1578545"); ok {
		t.Fatal("подписочное видео принято как доступное")
	}
	t.Log("подписочное видео отклонено, как и должно быть")
}
