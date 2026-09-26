package litesrc

import (
	stdjson "encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"lampac-go/internal/config"
)

// TestRudubLive гоняет живой сайт от карточки до сегмента CDN.
// Запуск: RUDUB_LIVE=1 go test ./internal/litesrc/ -run TestRudubLive -v
func TestRudubLive(t *testing.T) {
	if os.Getenv("RUDUB_LIVE") != "1" {
		t.Skip("set RUDUB_LIVE=1 to run the live rudub test")
	}
	cfg := config.Config{Online: config.OnlineConfig{Rudub: config.RudubSource{
		Host:        rudubDefaultHost,
		TrackerHost: rudubDefaultTracker,
	}}}
	checker := NewRudubChecker(cfg)
	h := checker.Handle(cfg, nil)

	serve := func(target string) string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		return rec.Body.String()
	}

	card := "title=" + url.QueryEscape("Виджил") + "&original_title=Vigil&serial=1&year=2021&rjson=true"

	start := time.Now()
	body := serve("http://l/lite/rudub?" + card)
	t.Logf("index (%s): %s", time.Since(start).Round(time.Millisecond), truncURL(body))
	if !strings.Contains(body, `"type":"season"`) {
		t.Fatalf("no seasons: %s", body)
	}

	var seasons struct {
		Data []struct {
			URL  string `json:"url"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := stdjson.Unmarshal([]byte(body), &seasons); err != nil {
		t.Fatalf("season json: %v", err)
	}
	if len(seasons.Data) == 0 {
		t.Fatal("empty season list")
	}
	t.Logf("seasons: %d", len(seasons.Data))

	last := seasons.Data[len(seasons.Data)-1]
	body = serve(strings.Replace(last.URL, "http://l", "http://l", 1))
	if !strings.Contains(body, `"type":"episode"`) {
		t.Fatalf("no episodes for %s: %s", last.Name, body)
	}
	var episodes struct {
		Data []struct {
			URL   string `json:"url"`
			Title string `json:"title"`
			S     int    `json:"s"`
			E     int    `json:"e"`
		} `json:"data"`
	}
	if err := stdjson.Unmarshal([]byte(body), &episodes); err != nil {
		t.Fatalf("episode json: %v", err)
	}
	if len(episodes.Data) == 0 {
		t.Fatalf("empty episode list for %s", last.Name)
	}
	t.Logf("%s: %d серий", last.Name, len(episodes.Data))

	body = serve(episodes.Data[0].URL)
	t.Logf("play: %s", body)
	var play struct {
		URL           string `json:"url"`
		StreamQuality []struct {
			Quality string `json:"quality"`
			URL     string `json:"url"`
		} `json:"streamquality"`
	}
	if err := stdjson.Unmarshal([]byte(body), &play); err != nil {
		t.Fatalf("play json: %v", err)
	}
	if len(play.StreamQuality) == 0 {
		t.Fatalf("no stream qualities: %s", body)
	}

	// Ссылки уходят клиенту через /proxy с заголовками — здесь проверяем, что
	// с ними CDN отдаёт плейлист, а без User-Agent отвечает отказом.
	player := checker.playerHost()
	variant := play.StreamQuality[0].URL
	t.Logf("variant %s: %s", play.StreamQuality[0].Quality, truncURL(variant))

	withRef, codeRef := rudubLiveGet(t, variant, player+"/")
	if codeRef != http.StatusOK || !strings.Contains(withRef, "#EXTINF") {
		t.Fatalf("variant with referer: code=%d body=%.120s", codeRef, withRef)
	}
	// Пустой UA — единственная проверка, которую CDN действительно делает.
	if _, codeBare := rudubLiveGetNoUA(t, variant); codeBare == http.StatusOK {
		t.Logf("CDN отдал вариант без User-Agent (code=%d) — проверка ослабла", codeBare)
	}

	seg := regexp.MustCompile(`(?m)^[^#\n]+$`).FindString(withRef)
	if seg == "" {
		t.Fatal("no segment in media playlist")
	}
	if !strings.HasPrefix(seg, "http") {
		base := variant[:strings.LastIndex(variant, "/")+1]
		seg = base + strings.TrimSpace(seg)
	}
	segBody, segCode := rudubLiveGet(t, seg, player+"/")
	if segCode != http.StatusOK || len(segBody) < 100_000 {
		t.Fatalf("segment: code=%d size=%d", segCode, len(segBody))
	}
	t.Logf("segment ok: %d bytes", len(segBody))
}

// rudubLiveGetNoUA намеренно уходит без User-Agent: Go подставляет свой, если
// заголовок не тронуть, поэтому его нужно занулить явно.
func rudubLiveGetNoUA(t *testing.T, target string) (string, int) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("User-Agent", "")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("fetch %s: %v", truncURL(target), err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return string(body), resp.StatusCode
}

func rudubLiveGet(t *testing.T, target, referer string) (string, int) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("User-Agent", anividsUA)
	if referer != "" {
		req.Header.Set("Referer", referer)
		req.Header.Set("Origin", strings.TrimSuffix(referer, "/"))
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("fetch %s: %v", truncURL(target), err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return string(body), resp.StatusCode
}

// TestRudubLiveLongShow — худший случай по стоимости: 227 серий, два десятка
// сезонов. index листает пагинацию и разворачивает каждый сезон, поэтому здесь
// меряется именно то, во что упрётся клиент на такой карточке.
// Запуск: RUDUB_LIVE=1 go test ./internal/litesrc/ -run TestRudubLiveLongShow -v
func TestRudubLiveLongShow(t *testing.T) {
	if os.Getenv("RUDUB_LIVE") != "1" {
		t.Skip("set RUDUB_LIVE=1 to run the live rudub test")
	}
	cfg := config.Config{Online: config.OnlineConfig{Rudub: config.RudubSource{
		Host:        rudubDefaultHost,
		TrackerHost: rudubDefaultTracker,
	}}}
	checker := NewRudubChecker(cfg)

	start := time.Now()
	show, ok := checker.resolveShow(t.Context(), "greysanatomy")
	elapsed := time.Since(start)
	if !ok {
		t.Fatalf("greysanatomy not resolved in %s", elapsed.Round(time.Millisecond))
	}

	total := 0
	for _, s := range show.seasons {
		total += len(s.episodes)
	}
	for _, s := range show.seasons {
		t.Logf("  сезон %d: %d серий, качество %q", s.num, len(s.episodes), s.quality)
	}
	t.Logf("«Анатомия страсти»: %d сезонов, %d серий за %s",
		len(show.seasons), total, elapsed.Round(time.Millisecond))

	// Порог с запасом: клиент ждёт источник считаные секунды, и если обход
	// такой карточки уползёт за это — резолв сезонов пора делать ленивым.
	if elapsed > 25*time.Second {
		t.Fatalf("resolve too slow: %s", elapsed.Round(time.Millisecond))
	}

	// Повторный заход обязан прийти из кэша дерева.
	start = time.Now()
	if _, ok := checker.resolveShow(t.Context(), "greysanatomy"); !ok {
		t.Fatal("cached resolve failed")
	}
	if cached := time.Since(start); cached > 50*time.Millisecond {
		t.Fatalf("second resolve took %s — cache not used", cached.Round(time.Millisecond))
	}
}

// TestRudubLiveCardMatch прогоняет матчинг по реальным карточкам Лампы:
// TSV «title\toriginal_title\tyear» (выгрузка из логов прода).
// Запуск: RUDUB_LIVE=1 RUDUB_CARDS=/path/cards.tsv go test ./internal/litesrc/ -run TestRudubLiveCardMatch -v
func TestRudubLiveCardMatch(t *testing.T) {
	path := os.Getenv("RUDUB_CARDS")
	if os.Getenv("RUDUB_LIVE") != "1" || path == "" {
		t.Skip("set RUDUB_LIVE=1 and RUDUB_CARDS=<tsv> to run the card-match report")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cards: %v", err)
	}
	cfg := config.Config{Online: config.OnlineConfig{Rudub: config.RudubSource{
		Host:        rudubDefaultHost,
		TrackerHost: rudubDefaultTracker,
	}}}
	checker := NewRudubChecker(cfg)
	items, ok := checker.ensureCatalog(t.Context())
	if !ok {
		t.Fatal("catalogue not loaded")
	}
	t.Logf("каталог: %d сериалов", len(items))

	var total, hits, strong int
	var weak []string
	for _, line := range strings.Split(string(raw), "\n") {
		cols := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(cols) < 2 {
			continue
		}
		title, orig := strings.TrimSpace(cols[0]), strings.TrimSpace(cols[1])
		if title == "" && orig == "" {
			continue
		}
		total++
		item, found := rudubPickShow(items, title, orig)
		if !found {
			continue
		}
		hits++
		nameHit := normalizeSearchTitle(item.name) == normalizeSearchTitle(title)
		slugHit := rudubSlugKey(item.slug) == rudubSlugKey(orig)
		if nameHit && slugHit {
			strong++
		} else {
			weak = append(weak, fmt.Sprintf("%s / %s -> %s (%s) name=%v slug=%v",
				title, orig, item.slug, item.name, nameHit, slugHit))
		}
	}
	t.Logf("карточек: %d, найдено: %d (%.0f%%), из них по обоим полям: %d",
		total, hits, 100*float64(hits)/float64(max(total, 1)), strong)
	for i, w := range weak {
		if i >= 25 {
			t.Logf("… ещё %d совпадений по одному полю", len(weak)-i)
			break
		}
		t.Logf("  слабое: %s", w)
	}
}
