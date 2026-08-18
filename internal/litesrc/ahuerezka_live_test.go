package litesrc

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

// TestAhueRezkaLive hits the real hdbase.workers.dev API end-to-end.
// Run with: AHUEREZKA_LIVE=1 go test ./internal/litesrc/ -run TestAhueRezkaLive -v
func TestAhueRezkaLive(t *testing.T) {
	if os.Getenv("AHUEREZKA_LIVE") != "1" {
		t.Skip("set AHUEREZKA_LIVE=1 to run the live worker test")
	}
	// HLS stays false — the shipped default. The worker's HLS spelling
	// (":hls:manifest.m3u8") is a hard 404 on stream.voidboost.*, so a live test
	// pinning HLS:true asserted a form that cannot play.
	cfg := config.Config{Online: config.OnlineConfig{AhueRezka: config.AhueRezkaSource{
		Host: "https://rezka.hdbase.workers.dev", KpHost: "https://kp.hdbase.workers.dev", Premium: true, HLS: false,
	}}}
	h := NewAhueRezkaChecker(cfg).Handle(cfg, "ahuerezka", nil)

	serve := func(target string) (int, string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, target, nil)
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	// 1) checksearch movie (Alien, kp 386)
	_, body := serve("http://l/lite/ahuerezka?checksearch=true&kinopoisk_id=386&title=Alien&year=1979")
	t.Logf("checksearch movie: %s", body)
	if !strings.Contains(body, `"rch":true`) {
		t.Fatalf("movie checksearch failed: %s", body)
	}

	// 2) movie stream (Dubляж, t=56) -> must yield a voidboost stream via /proxy/
	_, body = serve("http://l/lite/ahuerezka/movie?kinopoisk_id=386&t=56&title=Чужой&rjson=true&call=true")
	t.Logf("movie stream: %s", body)
	// NOTE: no "/proxy/" assertion here. Wrapping a stream URL goes through
	// deps.StreamProxyDirectURL, which the httpapi package injects at wiring
	// time; inside the litesrc package that hook is nil and the balancer
	// correctly returns the raw CDN URL. Asserting on /proxy/ made this test
	// permanently red while the balancer was working fine.
	if strings.Contains(body, ":hls:manifest.m3u8") {
		t.Fatalf("stream carries the 404-ing HLS spelling: %s", body)
	}
	if !strings.Contains(body, `"method":"play"`) || !strings.Contains(body, "voidboost") {
		t.Fatalf("movie stream not resolved: %s", body)
	}

	// 3) serial episodes (Alien: Earth, kp 4308326, HDrezka Studio t=111, s1e1)
	_, body = serve("http://l/lite/ahuerezka/serial?kinopoisk_id=4308326&t=111&s=1&title=Alien+Earth&rjson=true")
	t.Logf("serial episodes: %s", body)
	if !strings.Contains(body, `"type":"episode"`) {
		t.Fatalf("serial episodes failed: %s", body)
	}

	// 4) episode stream s1e1 -> voidboost stream
	_, body = serve("http://l/lite/ahuerezka/movie?kinopoisk_id=4308326&t=111&s=1&e=1&title=Alien+Earth&rjson=true&call=true")
	t.Logf("episode stream: %s", body)
	// NOTE: no "/proxy/" assertion here. Wrapping a stream URL goes through
	// deps.StreamProxyDirectURL, which the httpapi package injects at wiring
	// time; inside the litesrc package that hook is nil and the balancer
	// correctly returns the raw CDN URL. Asserting on /proxy/ made this test
	// permanently red while the balancer was working fine.
	if strings.Contains(body, ":hls:manifest.m3u8") {
		t.Fatalf("stream carries the 404-ing HLS spelling: %s", body)
	}
	if !strings.Contains(body, `"method":"play"`) || !strings.Contains(body, "voidboost") {
		t.Fatalf("episode stream not resolved: %s", body)
	}
}
