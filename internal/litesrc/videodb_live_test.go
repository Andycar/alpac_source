package litesrc

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

// VIDEODB_LIVE=1 go test ./internal/litesrc/ -run TestZetflixDBLive -v -timeout 180s
//
// Hits the real obrut catalogues. Проверяет и новый источник zetflixdb (AO), и
// то, что videodb больше не залипает на мёртвом 30bf3790/AN.
func TestZetflixDBLive(t *testing.T) {
	if os.Getenv("VIDEODB_LIVE") == "" {
		t.Skip("set VIDEODB_LIVE=1 to hit obrut.show")
	}
	const kp = "1236063" // Tenet — present in both live catalogues

	run := func(t *testing.T, checker *videodbChecker, route string) string {
		t.Helper()
		if proxied, ok := liveProxyClient(t); ok {
			checker.client = proxied
			checker.cloudClient = proxied
		}
		handler := checker.Handle(config.Config{}, nil)
		rec := httptest.NewRecorder()
		handler(rec, httptest.NewRequest("GET", route+"?rjson=true&kinopoisk_id="+kp+"&title=Довод", nil))
		if rec.Code != 200 {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	t.Run("zetflixdb serves the AO catalogue", func(t *testing.T) {
		body := run(t, NewZetflixDBChecker(config.Config{}), "/lite/zetflixdb")
		if !strings.Contains(body, "\"url\"") {
			t.Fatalf("no playable rows: %s", truncate(body, 220))
		}
		t.Logf("zetflixdb: %s", truncate(body, 220))
	})

	t.Run("videodb skips the dead primary", func(t *testing.T) {
		checker := NewVideodbChecker(config.Config{
			// The production config still names the dead host first.
			Online: config.OnlineConfig{VideoDB: config.VideoDBSource{
				Host:    "https://kinogo.media",
				APIHost: "https://30bf3790.obrut.show",
			}},
		})
		body := run(t, checker, "/lite/videodb")
		if !strings.Contains(body, "\"url\"") {
			t.Fatalf("no playable rows: %s", truncate(body, 220))
		}
		if host, _ := checker.lastObrutOK.Load().(string); strings.Contains(host, "30bf3790") || host == "" {
			t.Fatalf("resolved via %q, expected one of the live catalogues", host)
		} else {
			t.Logf("videodb resolved via %s", host)
		}
	})
}
