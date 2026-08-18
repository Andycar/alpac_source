package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestKinobaseChecksearchNative(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`
			<ul>
				<li class="item">
					<div class="title"><a>Inception</a></div>
					<span class="year">2010</span>
					<a href="/movie/inception">open</a>
				</li>
			</ul>
		`))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Kinobase: config.KinobaseSource{Host: upstream.URL, PlayerJS: true},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/kinobase?checksearch=true&title=Inception&year=2010", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"movie"`) {
		t.Fatalf("expected movie marker, got: %s", body)
	}
}

func TestKinobaseSimilarAndSerialPlayerJS(t *testing.T) {
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/search":
			_, _ = w.Write([]byte(`
				<ul>
					<li class="item">
						<div class="title"><a>Inception</a></div>
						<span class="year">2011</span>
						<a href="/movie/inception-alt">open</a>
					</li>
					<li class="item">
						<div class="title"><a>Inception</a></div>
						<span class="year">2010</span>
						<a href="/movie/inception-main">open</a>
					</li>
				</ul>
			`))
		case r.URL.Path == "/movie/inception-main":
			// playerjsfile in season/episode format
			_, _ = w.Write([]byte(`
				<div id="playerjsfile">[{"id":1,"title":"1 сезон","folder":[{"id":11,"title":"1 серия","file":"[1080p]{DUB}https://cdn.example/s1e1_1080.m3u8,[720p]{DUB}https://cdn.example/s1e1_720.m3u8"},{"id":12,"title":"2 серия","file":"[1080p]{DUB}https://cdn.example/s1e2_1080.m3u8"}]}]</div>
			`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Kinobase: config.KinobaseSource{Host: upstream.URL, PlayerJS: true},
		},
	}

	// Similar mode (year mismatch + similar=true)
	simRec := httptest.NewRecorder()
	simReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/kinobase?rjson=true&title=Inception&year=2001&similar=true", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(simRec, simReq)
	if simRec.Code != http.StatusOK {
		t.Fatalf("unexpected similar status: %d body=%s", simRec.Code, simRec.Body.String())
	}
	var similarPayload map[string]any
	if err := stdjson.Unmarshal(simRec.Body.Bytes(), &similarPayload); err != nil {
		t.Fatalf("invalid similar json: %v body=%s", err, simRec.Body.String())
	}
	if toString(similarPayload["type"]) != "similar" {
		t.Fatalf("unexpected similar type: %#v body=%s", similarPayload["type"], simRec.Body.String())
	}
	simData, _ := similarPayload["data"].([]any)
	if len(simData) != 2 {
		t.Fatalf("unexpected similar rows count: %d", len(simData))
	}

	// Season list
	seasonRec := httptest.NewRecorder()
	seasonReq := httptest.NewRequest(
		http.MethodGet,
		"http://lampac.local/lite/kinobase?rjson=true&title=Inception&year=2010&href="+url.QueryEscape("/movie/inception-main"),
		nil,
	)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(seasonRec, seasonReq)
	if seasonRec.Code != http.StatusOK {
		t.Fatalf("unexpected season status: %d body=%s", seasonRec.Code, seasonRec.Body.String())
	}
	var seasonPayload map[string]any
	if err := stdjson.Unmarshal(seasonRec.Body.Bytes(), &seasonPayload); err != nil {
		t.Fatalf("invalid season json: %v body=%s", err, seasonRec.Body.String())
	}
	if toString(seasonPayload["type"]) != "season" {
		t.Fatalf("unexpected season type: %#v body=%s", seasonPayload["type"], seasonRec.Body.String())
	}

	// Episode list for season=1
	epRec := httptest.NewRecorder()
	epReq := httptest.NewRequest(
		http.MethodGet,
		"http://lampac.local/lite/kinobase?rjson=true&title=Inception&year=2010&href="+url.QueryEscape("/movie/inception-main")+"&s=1",
		nil,
	)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(epRec, epReq)
	if epRec.Code != http.StatusOK {
		t.Fatalf("unexpected episode status: %d body=%s", epRec.Code, epRec.Body.String())
	}
	var epPayload map[string]any
	if err := stdjson.Unmarshal(epRec.Body.Bytes(), &epPayload); err != nil {
		t.Fatalf("invalid episode json: %v body=%s", err, epRec.Body.String())
	}
	if toString(epPayload["type"]) != "episode" {
		t.Fatalf("unexpected episode type: %#v body=%s", epPayload["type"], epRec.Body.String())
	}
	epData, _ := epPayload["data"].([]any)
	if len(epData) != 2 {
		t.Fatalf("unexpected episode rows count: %d", len(epData))
	}
}
