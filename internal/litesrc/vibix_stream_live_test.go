package litesrc

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"lampac-go/internal/config"
)

// VIBIX_LIVE=1 VIBIX_TOKEN=... go test ./internal/litesrc/ -run TestVibixStreamLive -v -timeout 120s
func TestVibixStreamLive(t *testing.T) {
	if os.Getenv("VIBIX_LIVE") == "" {
		t.Skip("set VIBIX_LIVE=1")
	}
	cfg := config.Config{Online: config.OnlineConfig{
		Vibix: config.VibixSource{Host: "https://vibix.org", Token: os.Getenv("VIBIX_TOKEN")},
	}}
	checker := NewVibixChecker(cfg)
	handler := checker.Handle(cfg, nil) // nil proxyLinks → segments not rewritten, but m3u8 still served

	// Deferred stream URL as capi would build it (Project Hail Mary, LostFilm voice, 1080p).
	target := "http://lampac.local/lite/vibix/stream.m3u8?kinopoisk_id=1382256&voice=" +
		"LostFilm&q=1080p"
	start := time.Now()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", target, nil)
	handler.ServeHTTP(rec, req)
	body := rec.Body.String()
	t.Logf("stream took=%s status=%d len=%d head=%q", time.Since(start), rec.Code, len(body), truncForLog(body, 80))
	if rec.Code != 200 || !strings.Contains(body, "#EXTM3U") {
		t.Fatalf("stream endpoint did not return m3u8: status=%d body=%s", rec.Code, truncForLog(body, 200))
	}
}

func truncForLog(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
