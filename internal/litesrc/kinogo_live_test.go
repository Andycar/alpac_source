package litesrc

import (
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

// KINOGO_LIVE=1 go test ./internal/litesrc/ -run TestKinogoLive -v -timeout 180s
//
// Runs the whole chain against the real site with a DEAD mirror configured
// (kinogo.luxury sits behind a Cloudflare challenge since 2026-08), so it also
// proves the mirror fallback works end to end.
func TestKinogoLive(t *testing.T) {
	if os.Getenv("KINOGO_LIVE") == "" {
		t.Skip("set KINOGO_LIVE=1 to hit the real site")
	}

	checker := NewKinogoChecker(config.Config{
		Online: config.OnlineConfig{Kinogo: config.KinogoSource{Host: "https://kinogo.luxury"}},
	})
	// The site is RKN-blocked from some dev machines; the balancer transport
	// ignores HTTP(S)_PROXY on purpose, so allow routing this test through one.
	if proxied, ok := liveProxyClient(t); ok {
		checker.client = proxied
	}
	handler := checker.Handle(config.Config{}, nil)

	call := func(t *testing.T, query string) map[string]any {
		t.Helper()
		req := httptest.NewRequest("GET", "/lite/kinogo?"+query, nil)
		rec := httptest.NewRecorder()
		handler(rec, req)
		if rec.Code != 200 {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("bad json: %v (%s)", err, rec.Body.String())
		}
		return out
	}

	t.Run("movie", func(t *testing.T) {
		out := call(t, "rjson=true&title=%D0%92%D0%B5%D0%BD%D0%BE%D0%BC&year=2018")
		if out["type"] != "movie" {
			t.Fatalf("type=%v, want movie: %+v", out["type"], out)
		}
		rows, _ := out["data"].([]any)
		if len(rows) == 0 {
			t.Fatal("no playable rows")
		}
		first, _ := rows[0].(map[string]any)
		stream, _ := first["url"].(string)
		if stream == "" {
			t.Fatalf("empty stream url: %+v", first)
		}
		subs, _ := first["subtitles"].([]any)
		t.Logf("movie stream=%s voice=%v subtitles=%d", stream, first["voice_name"], len(subs))
	})

	t.Run("serial", func(t *testing.T) {
		// href of a serial page; a dead mirror in the href must be rerouted.
		const href = "https://kinogo.luxury/57018-ukrytie-1-2-sezon.html"

		out := call(t, "rjson=true&title=%D0%A3%D0%BA%D1%80%D1%8B%D1%82%D0%B8%D0%B5&href="+url.QueryEscape(href))
		if out["type"] != "season" {
			t.Fatalf("type=%v, want season: %+v", out["type"], out)
		}
		seasons, _ := out["data"].([]any)
		if len(seasons) == 0 {
			t.Fatal("no seasons")
		}
		t.Logf("seasons=%d", len(seasons))

		first, _ := seasons[0].(map[string]any)
		seasonNum, _ := first["id"].(float64)
		out = call(t, "rjson=true&title=%D0%A3%D0%BA%D1%80%D1%8B%D1%82%D0%B8%D0%B5&href="+url.QueryEscape(href)+
			"&s="+strconv.Itoa(int(seasonNum)))
		if out["type"] != "episode" {
			t.Fatalf("type=%v, want episode: %+v", out["type"], out)
		}
		episodes, _ := out["data"].([]any)
		if len(episodes) == 0 {
			t.Fatal("no episodes")
		}
		ep, _ := episodes[0].(map[string]any)
		if stream, _ := ep["url"].(string); stream == "" {
			t.Fatalf("episode without stream: %+v", ep)
		}
		t.Logf("season %d: %d episodes, first=%v", int(seasonNum), len(episodes), ep["name"])
	})

	// Cards that used to fall through to `similar` (and were then dropped by
	// /capi) because of the title qualifier or a one-year drift.
	t.Run("exact match instead of similar", func(t *testing.T) {
		cases := []struct{ name, title, year string }{
			{"season range in title", "Реальные пацаны", "2010"},
			{"year drift", "Обсессия", "2026"},
			{"series", "Укрытие", "2023"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				out := call(t, "rjson=true&title="+url.QueryEscape(tc.title)+"&year="+tc.year)
				switch out["type"] {
				case "movie", "season", "episode":
					rows, _ := out["data"].([]any)
					t.Logf("%s: type=%v rows=%d", tc.title, out["type"], len(rows))
				default:
					t.Fatalf("%s: type=%v (still not pinned to a card): %+v", tc.title, out["type"], out)
				}
			})
		}
	})

	t.Run("active mirror is the live one", func(t *testing.T) {
		if got := checker.currentHost(); !strings.Contains(got, "kinogo.la") {
			t.Fatalf("active host=%q, expected the live mirror after fallback", got)
		}
	})
}
