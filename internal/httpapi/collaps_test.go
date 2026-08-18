package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestCollapsChecksearchByEmbed(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embed/kp/447301" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`makePlayer({hls: "https://cdn.example/video.m3u8"})`))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Collaps: config.CollapsSource{APIHost: upstream.URL, Token: "token"},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/collaps?checksearch=true&kinopoisk_id=447301", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"type":"movie"`) {
		t.Fatalf("expected movie marker, got: %s", rec.Body.String())
	}
}

func TestCollapsDashChecksearchByList(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/list" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("token") != "test-token" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"results":[{"id":1}]}`))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Collaps: config.CollapsSource{APIHost: upstream.URL, ListHost: upstream.URL, Token: "test-token"},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/collaps-dash?checksearch=true&title=Inception", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"type":"movie"`) {
		t.Fatalf("expected movie marker, got: %s", rec.Body.String())
	}
}

func TestCollapsMovieAndDashRoutes(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embed/kp/447301" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`makePlayer({
hls: "https://cdn.example/video.m3u8?x=1\u0026y=2",
dasha: "https://cdn.example/video.mpd",
audio: {"names":["Dub","Orig"]},
cc: [{"url":"https://cdn.example/sub.vtt","name":"RU"}],
})`))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Collaps: config.CollapsSource{APIHost: upstream.URL, Token: "token"},
		},
	}

	hlsRec := httptest.NewRecorder()
	hlsReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/collaps?rjson=true&title=Inception&kinopoisk_id=447301", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(hlsRec, hlsReq)
	if hlsRec.Code != http.StatusOK {
		t.Fatalf("unexpected hls status: %d body=%s", hlsRec.Code, hlsRec.Body.String())
	}

	var hlsPayload map[string]any
	if err := stdjson.Unmarshal(hlsRec.Body.Bytes(), &hlsPayload); err != nil {
		t.Fatalf("invalid hls json: %v body=%s", err, hlsRec.Body.String())
	}
	data, _ := hlsPayload["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("unexpected hls rows: %d body=%s", len(data), hlsRec.Body.String())
	}
	hlsRow, _ := data[0].(map[string]any)
	if toString(hlsRow["url"]) != "https://cdn.example/video.m3u8?x=1&y=2" {
		t.Fatalf("unexpected hls url: %#v body=%s", hlsRow["url"], hlsRec.Body.String())
	}

	dashRec := httptest.NewRecorder()
	dashReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/collaps-dash?rjson=true&title=Inception&kinopoisk_id=447301", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(dashRec, dashReq)
	if dashRec.Code != http.StatusOK {
		t.Fatalf("unexpected dash status: %d body=%s", dashRec.Code, dashRec.Body.String())
	}

	var dashPayload map[string]any
	if err := stdjson.Unmarshal(dashRec.Body.Bytes(), &dashPayload); err != nil {
		t.Fatalf("invalid dash json: %v body=%s", err, dashRec.Body.String())
	}
	dashData, _ := dashPayload["data"].([]any)
	if len(dashData) != 1 {
		t.Fatalf("unexpected dash rows: %d body=%s", len(dashData), dashRec.Body.String())
	}
	dashRow, _ := dashData[0].(map[string]any)
	if toString(dashRow["url"]) != "https://cdn.example/video.mpd" {
		t.Fatalf("unexpected dash url: %#v body=%s", dashRow["url"], dashRec.Body.String())
	}
}

func TestCollapsSerialAndSimilar(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/embed/kp/447301":
			_, _ = w.Write([]byte(`seasons:[{"season":1,"episodes":[{"episode":"1","hls":"https://cdn.example/s1e1.m3u8","audio":{"names":["Dub"]},"cc":[{"url":"https://cdn.example/s1e1.vtt","name":"RU"}]},{"episode":"2","hls":"https://cdn.example/s1e2.m3u8"}]}],`))
		case "/list":
			if r.URL.Query().Get("token") != "token" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_, _ = w.Write([]byte(`{"results":[{"id":123,"name":"Inception","year":2010,"poster":"/img/inception.jpg"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Collaps: config.CollapsSource{APIHost: upstream.URL, ListHost: upstream.URL, Token: "token"},
		},
	}

	seasonRec := httptest.NewRecorder()
	seasonReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/collaps?rjson=true&title=Inception&kinopoisk_id=447301", nil)
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

	episodeRec := httptest.NewRecorder()
	episodeReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/collaps?rjson=true&title=Inception&kinopoisk_id=447301&s=1", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(episodeRec, episodeReq)
	if episodeRec.Code != http.StatusOK {
		t.Fatalf("unexpected episode status: %d body=%s", episodeRec.Code, episodeRec.Body.String())
	}
	var epPayload map[string]any
	if err := stdjson.Unmarshal(episodeRec.Body.Bytes(), &epPayload); err != nil {
		t.Fatalf("invalid episode json: %v body=%s", err, episodeRec.Body.String())
	}
	if toString(epPayload["type"]) != "episode" {
		t.Fatalf("unexpected episode type: %#v body=%s", epPayload["type"], episodeRec.Body.String())
	}
	epData, _ := epPayload["data"].([]any)
	if len(epData) != 2 {
		t.Fatalf("unexpected episode rows count: %d body=%s", len(epData), episodeRec.Body.String())
	}

	simRec := httptest.NewRecorder()
	simReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/collaps?rjson=true&title=Inception&similar=true", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(simRec, simReq)
	if simRec.Code != http.StatusOK {
		t.Fatalf("unexpected similar status: %d body=%s", simRec.Code, simRec.Body.String())
	}
	var simPayload map[string]any
	if err := stdjson.Unmarshal(simRec.Body.Bytes(), &simPayload); err != nil {
		t.Fatalf("invalid similar json: %v body=%s", err, simRec.Body.String())
	}
	if toString(simPayload["type"]) != "similar" {
		t.Fatalf("unexpected similar type: %#v body=%s", simPayload["type"], simRec.Body.String())
	}
	simData, _ := simPayload["data"].([]any)
	if len(simData) != 1 {
		t.Fatalf("unexpected similar rows count: %d body=%s", len(simData), simRec.Body.String())
	}
	simRow, _ := simData[0].(map[string]any)
	if !strings.Contains(toString(simRow["url"]), "/lite/collaps?orid=123") {
		t.Fatalf("unexpected similar link: %#v body=%s", simRow["url"], simRec.Body.String())
	}
}
