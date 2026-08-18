package litesrc

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

// TestAnwapLive drives the real site end to end.
// Run with: ANWAP_LIVE=1 go test ./internal/litesrc/ -run TestAnwapLive -v
func TestAnwapLive(t *testing.T) {
	if os.Getenv("ANWAP_LIVE") != "1" {
		t.Skip("set ANWAP_LIVE=1 to run the live anwap test")
	}
	cfg := config.Config{Online: config.OnlineConfig{Anwap: config.HostSource{Host: anwapDefaultHost}}}
	h := NewAnwapChecker(cfg).Handle(cfg, nil)

	serve := func(target string) string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		return rec.Body.String()
	}

	// 1) checksearch for a film that exists.
	body := serve("http://l/lite/anwap?checksearch=true&title=" + anwapEsc("Матрица времени") + "&year=2017")
	t.Logf("checksearch: %s", body)
	// writeCheckSearchResponseNoRCH emits {"type":"movie",…} for a hit and a bare
	// {"rch":false} for a miss, so the type field is the positive signal.
	if !strings.Contains(body, `"type":"movie"`) {
		t.Fatalf("film checksearch failed: %s", body)
	}

	// 2) film -> a play node.
	body = serve("http://l/lite/anwap?title=" + anwapEsc("Матрица времени") + "&year=2017&rjson=true")
	t.Logf("film index: %s", trunc(body))
	if !strings.Contains(body, "/lite/anwap/play") {
		t.Fatalf("no play node for the film: %s", trunc(body))
	}

	// 3) play -> a real stream URL.
	path := between(body, `path=`, `"`)
	if path == "" {
		t.Fatalf("could not read the play path out of %s", trunc(body))
	}
	body = serve("http://l/lite/anwap/play?path=" + path)
	t.Logf("play: %s", trunc(body))
	if !strings.Contains(body, `"method":"play"`) || !strings.Contains(body, "anwap") {
		t.Fatalf("play did not resolve a stream: %s", trunc(body))
	}
	// A truncated decode (the split-junk bug) yields a hostname with no path.
	if strings.Contains(body, "FkiU7hG") {
		t.Fatalf("stream URL carries an unstripped junk token: %s", trunc(body))
	}
}

func anwapEsc(s string) string {
	r := strings.NewReplacer(" ", "%20")
	return r.Replace(s)
}

func trunc(s string) string {
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}

func between(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	s = s[i+len(start):]
	j := strings.Index(s, end)
	if j < 0 {
		return ""
	}
	return s[:j]
}
