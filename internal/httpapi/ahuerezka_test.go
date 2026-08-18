package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

// fakeAhueWorker emulates rezka.hdbase.workers.dev for the four endpoints the
// balancer uses. id=386 is a movie, id=4308326 is a serial.
func fakeAhueWorker(t *testing.T) *httptest.Server {
	t.Helper()

	const movieStream = `[360p]https://stream.voidboost.cc/aaa/360.mp4:hls:manifest.m3u8 or https://stream.voidboost.cc/aaa/360.mp4,` +
		`[480p]https://stream.voidboost.cc/aaa/480.mp4:hls:manifest.m3u8 or https://stream.voidboost.cc/aaa/480.mp4,` +
		`[720p]https://stream.voidboost.cc/aaa/720.mp4:hls:manifest.m3u8 or https://stream.voidboost.cc/aaa/720.mp4,` +
		`[1080p]https://stream.voidboost.cc/aaa/1080.mp4:hls:manifest.m3u8 or https://stream.voidboost.cc/aaa/1080.mp4,` +
		`[<span class="pjs-prem-quality">1080p Ultra<img src="https://st.hdrezka.ac/x.svg" alt=""></span>]https://stream.voidboost.cc/aaa/ultra.mp4:hls:manifest.m3u8 or https://stream.voidboost.cc/aaa/ultra.mp4`

	const episodeStream = movieStream +
		`,[<span class="pjs-prem-quality">2K<img src="https://st.hdrezka.ac/x.svg" alt=""></span>]https://stream.voidboost.cc/bbb/2k.mp4:hls:manifest.m3u8 or https://stream.voidboost.cc/bbb/2k.mp4` +
		`,[<span class="pjs-prem-quality">4K<img src="https://st.hdrezka.ac/x.svg" alt=""></span>]https://stream.voidboost.cc/bbb/4k.mp4:hls:manifest.m3u8 or https://stream.voidboost.cc/bbb/4k.mp4`

	writeJSONRaw := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = stdjson.NewEncoder(w).Encode(v)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/info", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("id") {
		case "4308326":
			writeJSONRaw(w, map[string]any{
				"translators": []map[string]string{
					{"id": "111", "name": "HDrezka Studio"},
					{"id": "1", "name": "лостфильм (LostFilm)"},
				},
				"defaultTranslator": "111",
				"hasSeasons":        true,
			})
		case "386":
			writeJSONRaw(w, map[string]any{
				"translators": []map[string]string{
					{"id": "56", "name": "Дубляж"},
					{"id": "56d", "name": "Дубляж (реж. версия)"},
				},
				"defaultTranslator": "56",
				"hasSeasons":        false,
			})
		default:
			writeJSONRaw(w, map[string]any{"translators": []any{}})
		}
	})
	mux.HandleFunc("/episodes", func(w http.ResponseWriter, r *http.Request) {
		writeJSONRaw(w, map[string]any{
			"seasons": []map[string]any{{"id": 1, "name": "Сезон 1"}},
			"episodes": map[string]any{
				"1": []map[string]any{
					{"id": 1, "title": "Серия 1"},
					{"id": 2, "title": "Серия 2"},
				},
			},
		})
	})
	mux.HandleFunc("/movie-stream", func(w http.ResponseWriter, r *http.Request) {
		writeJSONRaw(w, map[string]any{"stream": movieStream, "thumbnails": "", "subtitle": false})
	})
	mux.HandleFunc("/episode-stream", func(w http.ResponseWriter, r *http.Request) {
		writeJSONRaw(w, map[string]any{"stream": episodeStream, "thumbnails": "", "subtitle": false})
	})

	return httptest.NewServer(mux)
}

func newAhueTestConfig(workerURL string) config.Config {
	return config.Config{
		Online: config.OnlineConfig{
			AhueRezka: config.AhueRezkaSource{
				Host:    workerURL,
				KpHost:  workerURL,
				Premium: true,
				HLS:     true,
			},
		},
	}
}

func ahueServe(cfg config.Config, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)
	return rec
}

func TestAhueRezkaCheckSearch(t *testing.T) {
	worker := fakeAhueWorker(t)
	defer worker.Close()
	cfg := newAhueTestConfig(worker.URL)

	rec := ahueServe(cfg, "http://lampac.local/lite/ahuerezka?checksearch=true&kinopoisk_id=386&title=Alien&year=1979")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"rch":true`) {
		t.Fatalf("expected rch:true, got: %s", body)
	}
	// HD, not FHD: the worker's labels are inflated one step (its "1080p" is a
	// 1280x720 file, measured with ffprobe on two unrelated titles), and the
	// genuinely higher tiers are premium-only stubs we never serve.
	if !strings.Contains(body, `"quality":"HD"`) {
		t.Fatalf("expected HD badge, got: %s", body)
	}

	// Unknown id -> no translators -> rch:false.
	rec = ahueServe(cfg, "http://lampac.local/lite/ahuerezka?checksearch=true&kinopoisk_id=999999999&title=Nope")
	if !strings.Contains(rec.Body.String(), `"rch":false`) {
		t.Fatalf("expected rch:false for unknown id, got: %s", rec.Body.String())
	}
}

func TestAhueRezkaEmbedMovie(t *testing.T) {
	worker := fakeAhueWorker(t)
	defer worker.Close()
	cfg := newAhueTestConfig(worker.URL)

	rec := ahueServe(cfg, "http://lampac.local/lite/ahuerezka?kinopoisk_id=386&title=Alien&original_title=Alien&rjson=true")
	var resp struct {
		Type string           `json:"type"`
		Data []map[string]any `json:"data"`
	}
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rec.Body.String())
	}
	if resp.Type != "voice" {
		t.Fatalf("expected type voice, got %q", resp.Type)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("expected 2 translators, got %d", len(resp.Data))
	}
	first := resp.Data[0]
	if first["method"] != "call" {
		t.Fatalf("movie translator must use method:call, got %v", first["method"])
	}
	if u, _ := first["url"].(string); !strings.Contains(u, "/lite/ahuerezka/movie") || !strings.Contains(u, "t=56") {
		t.Fatalf("unexpected movie url: %v", first["url"])
	}
	if first["active"] != true {
		t.Fatalf("default translator (56) should be active, got %v", first["active"])
	}
}

func TestAhueRezkaEmbedSerial(t *testing.T) {
	worker := fakeAhueWorker(t)
	defer worker.Close()
	cfg := newAhueTestConfig(worker.URL)

	rec := ahueServe(cfg, "http://lampac.local/lite/ahuerezka?kinopoisk_id=4308326&title=Alien+Earth&rjson=true")
	var resp struct {
		Type string           `json:"type"`
		Data []map[string]any `json:"data"`
	}
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rec.Body.String())
	}
	if resp.Type != "voice" {
		t.Fatalf("expected type voice, got %q", resp.Type)
	}
	first := resp.Data[0]
	if first["method"] != "link" {
		t.Fatalf("serial translator must use method:link, got %v", first["method"])
	}
	if u, _ := first["url"].(string); !strings.Contains(u, "/lite/ahuerezka/serial") {
		t.Fatalf("unexpected serial url: %v", first["url"])
	}
}

func TestAhueRezkaSerialSeasonsAndEpisodes(t *testing.T) {
	worker := fakeAhueWorker(t)
	defer worker.Close()
	cfg := newAhueTestConfig(worker.URL)

	// Season list (no s).
	rec := ahueServe(cfg, "http://lampac.local/lite/ahuerezka/serial?kinopoisk_id=4308326&id=4308326&t=111&title=Alien&rjson=true")
	var seasons struct {
		Type  string           `json:"type"`
		Data  []map[string]any `json:"data"`
		Voice []map[string]any `json:"voice"`
	}
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &seasons); err != nil {
		t.Fatalf("unmarshal seasons: %v; body=%s", err, rec.Body.String())
	}
	if seasons.Type != "season" || len(seasons.Data) != 1 {
		t.Fatalf("expected 1 season, got type=%q n=%d", seasons.Type, len(seasons.Data))
	}
	if len(seasons.Voice) != 2 {
		t.Fatalf("expected 2 voices in season view, got %d", len(seasons.Voice))
	}
	if u, _ := seasons.Data[0]["url"].(string); !strings.Contains(u, "s=1") {
		t.Fatalf("season link missing s=1: %v", seasons.Data[0]["url"])
	}

	// Episode list (s=1).
	rec = ahueServe(cfg, "http://lampac.local/lite/ahuerezka/serial?kinopoisk_id=4308326&id=4308326&t=111&s=1&title=Alien&rjson=true")
	var eps struct {
		Type string           `json:"type"`
		Data []map[string]any `json:"data"`
	}
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &eps); err != nil {
		t.Fatalf("unmarshal episodes: %v; body=%s", err, rec.Body.String())
	}
	if eps.Type != "episode" || len(eps.Data) != 2 {
		t.Fatalf("expected 2 episodes, got type=%q n=%d", eps.Type, len(eps.Data))
	}
	if eps.Data[1]["e"].(float64) != 2 {
		t.Fatalf("expected second episode e=2, got %v", eps.Data[1]["e"])
	}
	if u, _ := eps.Data[0]["url"].(string); !strings.Contains(u, "/lite/ahuerezka/movie") || !strings.Contains(u, "e=1") {
		t.Fatalf("episode link malformed: %v", eps.Data[0]["url"])
	}
}

func TestAhueRezkaMovieStream(t *testing.T) {
	worker := fakeAhueWorker(t)
	defer worker.Close()
	cfg := newAhueTestConfig(worker.URL)

	rec := ahueServe(cfg, "http://lampac.local/lite/ahuerezka/movie?kinopoisk_id=386&id=386&t=56&title=Alien&voice_name=Дубляж&rjson=true&call=true")
	var play map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &play); err != nil {
		t.Fatalf("unmarshal play: %v; body=%s", err, rec.Body.String())
	}
	if play["method"] != "play" {
		t.Fatalf("expected method play, got %v", play["method"])
	}
	u, _ := play["url"].(string)
	if !strings.Contains(u, "/proxy/") || !strings.Contains(u, "pl=ahuerezka") {
		t.Fatalf("stream url not proxied for ahuerezka: %v", u)
	}
	// Premium on -> default stream is the 1080p Ultra mirror.
	if !strings.Contains(u, "ultra") {
		t.Fatalf("expected Ultra as default stream, got %v", u)
	}
	quality, ok := play["quality"].(map[string]any)
	if !ok || quality["1080p"] == nil {
		t.Fatalf("expected quality map with 1080p, got %v", play["quality"])
	}
}

func TestAhueRezkaEpisodeStream4K(t *testing.T) {
	worker := fakeAhueWorker(t)
	defer worker.Close()
	cfg := newAhueTestConfig(worker.URL)

	rec := ahueServe(cfg, "http://lampac.local/lite/ahuerezka/movie?kinopoisk_id=4308326&id=4308326&t=111&s=1&e=1&title=Alien&rjson=true&call=true")
	var play map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &play); err != nil {
		t.Fatalf("unmarshal play: %v; body=%s", err, rec.Body.String())
	}
	if play["s"].(float64) != 1 || play["e"].(float64) != 1 {
		t.Fatalf("expected s=1 e=1, got s=%v e=%v", play["s"], play["e"])
	}
	quality, ok := play["quality"].(map[string]any)
	if !ok {
		t.Fatalf("expected quality map, got %v", play["quality"])
	}
	// Quality-map keys use the rezka "label" (resolution), not the badge:
	// 4K -> 2160p, 2K -> 1440p (identical to the native rezka balancer).
	for _, want := range []string{"2160p", "1440p", "1080p", "720p"} {
		if quality[want] == nil {
			t.Fatalf("quality map missing %q: %v", want, quality)
		}
	}
	// Default stream should be the 4K mirror.
	if u, _ := play["url"].(string); !strings.Contains(u, "4k") {
		t.Fatalf("expected 4K default stream, got %v", u)
	}
}

func TestAhueRezkaPremiumOffDropsStubTiers(t *testing.T) {
	// With premium disabled (the default), the premium tiers (Ultra/2K/4K) —
	// which via the hdbase worker are a ~1min "buy premium" stub — must be
	// dropped, so the default stream is the real 1080p, not the stub.
	worker := fakeAhueWorker(t)
	defer worker.Close()
	cfg := newAhueTestConfig(worker.URL)
	cfg.Online.AhueRezka.Premium = false

	// Episode stream in the fake worker carries 2K + 4K tiers.
	rec := ahueServe(cfg, "http://lampac.local/lite/ahuerezka/movie?kinopoisk_id=4308326&t=111&s=1&e=1&title=Alien&rjson=true&call=true")
	var play map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &play); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rec.Body.String())
	}
	quality, _ := play["quality"].(map[string]any)
	for _, banned := range []string{"2160p", "1440p"} {
		if quality[banned] != nil {
			t.Fatalf("premium tier %q must be dropped when premium=false: %v", banned, quality)
		}
	}
	// The worker's top free tier is spelled "1080p" but delivers a 1280x720 file,
	// so it is surfaced as 720p (see ahueRezkaTrueLabel). The FILE is still the
	// one the worker tagged 1080p — only the label the user sees is corrected.
	if quality["720p"] == nil {
		t.Fatalf("expected the top free tier (labelled 720p) in quality map, got %v", quality)
	}
	if quality["1080p"] != nil {
		t.Fatalf("inflated 1080p label must not be offered with premium off: %v", quality)
	}
	// Default stream must be that file, not the 4K/2K stub.
	if u, _ := play["url"].(string); !strings.Contains(u, "1080") || strings.Contains(u, "4k") || strings.Contains(u, "2k") {
		t.Fatalf("default must be the real top free tier, not a premium stub: %v", u)
	}
}

func TestAhueRezkaKPSearchFallback(t *testing.T) {
	// When kinopoisk_id is absent, the balancer resolves it via the kp worker.
	var searched bool
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/search":
			searched = true
			_ = stdjson.NewEncoder(w).Encode(map[string]any{
				"films": []map[string]any{
					{"filmId": 386, "nameRu": "Чужой", "year": "1979"},
				},
			})
		case "/info":
			if r.URL.Query().Get("id") != "386" {
				t.Errorf("expected resolved id=386, got %q", r.URL.Query().Get("id"))
			}
			_ = stdjson.NewEncoder(w).Encode(map[string]any{
				"translators":       []map[string]string{{"id": "56", "name": "Дубляж"}},
				"defaultTranslator": "56",
				"hasSeasons":        false,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer worker.Close()
	cfg := newAhueTestConfig(worker.URL)

	rec := ahueServe(cfg, "http://lampac.local/lite/ahuerezka?checksearch=true&title=%D0%A7%D1%83%D0%B6%D0%BE%D0%B9&year=1979")
	if !strings.Contains(rec.Body.String(), `"rch":true`) {
		t.Fatalf("expected rch:true via kp fallback, got: %s", rec.Body.String())
	}
	if !searched {
		t.Fatal("kp search fallback was not used")
	}
}
