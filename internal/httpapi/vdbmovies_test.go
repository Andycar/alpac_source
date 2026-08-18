package httpapi

import (
	"encoding/base64"
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

// vdbmoviesTrashEncoded mirrors the litesrc original (test fixture data).
func vdbmoviesTrashEncoded() []string {
	trashList := []string{
		"wNp2wBTNcPRQvTC0_CpxCsq_8T1u9Q",
		"md-Od2G9RWOgSa5HoBSSbWrCyIqQyY",
		"kzuOYQqB_QSOL-xzN_Kz3kkgkHhHit",
		"6-xQWMh7ertLp8t_M9huUDk1M0VrYJ",
		"RyTwtf15_GLEsXxnpU4Ljjd0ReY-VH",
	}
	out := make([]string, 0, len(trashList))
	for _, trash := range trashList {
		out = append(out, "//"+base64.StdEncoding.EncodeToString([]byte(trash)))
	}
	return out
}

func TestVDBmoviesChecksearch(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/content/123/iframe" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(vdbmoviesTestIframeHTML(`[{"title":"Dub","file":"[720p]https://cdn.example/video/720.mp4:hls:manifest.m3u8"}]`)))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{VDBmovies: config.HostSource{Host: upstream.URL}},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/vdbmovies?checksearch=true&orid=123", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"movie"`) {
		t.Fatalf("expected show=true payload, got: %s", body)
	}
	if !strings.Contains(body, `"rch":false`) {
		t.Fatalf("expected rch=false payload, got: %s", body)
	}
}

func TestVDBmoviesMovieRjson(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/content/123/iframe" {
			http.NotFound(w, r)
			return
		}
		raw := `[{"title":"Dub 1","file":"[720p]https://cdn.example/video/720.mp4:hls:manifest.m3u8","subtitle":"[RU]https://sub.example/ru.vtt"}]`
		_, _ = w.Write([]byte(vdbmoviesTestIframeHTML(raw)))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{VDBmovies: config.HostSource{Host: upstream.URL}},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/vdbmovies?rjson=true&orid=123", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}

	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v, body=%s", err, rec.Body.String())
	}

	if payload["type"] != "movie" {
		t.Fatalf("unexpected type: %v", payload["type"])
	}
	data, ok := payload["data"].([]any)
	if !ok || len(data) != 1 {
		t.Fatalf("unexpected data: %#v", payload["data"])
	}
	row, _ := data[0].(map[string]any)
	if row["method"] != "call" {
		t.Fatalf("unexpected method: %#v", row["method"])
	}
	if _, ok := row["streamquality"].([]any); !ok {
		t.Fatalf("streamquality is missing: %#v", row)
	}
	if _, ok := row["subtitles"].([]any); !ok {
		t.Fatalf("subtitles are missing: %#v", row)
	}
}

func TestVDBmoviesSerialRjson(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/content/serial/iframe" {
			http.NotFound(w, r)
			return
		}
		raw := `[{"title":"1 сезон","folder":[{"title":"1 серия","folder":[{"title":"Voice A","file":"[720p]https://cdn.example/s1e1/720.mp4:hls:manifest.m3u8"},{"title":"Voice B","file":"[720p]https://cdn.example/s1e1b/720.mp4:hls:manifest.m3u8"}]},{"title":"2 серия","folder":[{"title":"Voice A","file":"[720p]https://cdn.example/s1e2/720.mp4:hls:manifest.m3u8"}]}]}]`
		_, _ = w.Write([]byte(vdbmoviesTestIframeHTML(raw)))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{VDBmovies: config.HostSource{Host: upstream.URL}},
	}

	recSeason := httptest.NewRecorder()
	reqSeason := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/vdbmovies?rjson=true&orid=serial", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(recSeason, reqSeason)

	if recSeason.Code != http.StatusOK {
		t.Fatalf("unexpected season status: %d", recSeason.Code)
	}
	var seasonPayload map[string]any
	if err := stdjson.Unmarshal(recSeason.Body.Bytes(), &seasonPayload); err != nil {
		t.Fatalf("season unmarshal: %v, body=%s", err, recSeason.Body.String())
	}
	if seasonPayload["type"] != "season" {
		t.Fatalf("unexpected season type: %#v", seasonPayload["type"])
	}

	recEpisode := httptest.NewRecorder()
	reqEpisode := httptest.NewRequest(
		http.MethodGet,
		"http://lampac.local/lite/vdbmovies?rjson=true&orid=serial&s=1&sid=0&t=Voice+A&title=Test&original_title=Test+Original",
		nil,
	)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(recEpisode, reqEpisode)

	if recEpisode.Code != http.StatusOK {
		t.Fatalf("unexpected episode status: %d", recEpisode.Code)
	}
	var episodePayload map[string]any
	if err := stdjson.Unmarshal(recEpisode.Body.Bytes(), &episodePayload); err != nil {
		t.Fatalf("episode unmarshal: %v, body=%s", err, recEpisode.Body.String())
	}
	if episodePayload["type"] != "episode" {
		t.Fatalf("unexpected episode type: %#v", episodePayload["type"])
	}
	if _, ok := episodePayload["voice"].([]any); !ok {
		t.Fatalf("voice section is missing: %#v", episodePayload)
	}
	data, ok := episodePayload["data"].([]any)
	if !ok || len(data) == 0 {
		t.Fatalf("episode data is missing: %#v", episodePayload["data"])
	}
}

func TestVDBmoviesEmptyWithoutIDs(t *testing.T) {
	cfg := config.Config{
		Online: config.OnlineConfig{VDBmovies: config.HostSource{Host: "https://cdnmovies-stream.online"}},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/vdbmovies?rjson=true&title=Only+Title", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if strings.TrimSpace(rec.Body.String()) != "{}" {
		t.Fatalf("expected empty rjson object, got: %s", rec.Body.String())
	}
}

func vdbmoviesTestIframeHTML(raw string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(raw)) + vdbmoviesTrashEncoded()[0]
	return `<script>window.makePlayer && makePlayer({file:'#.` + encoded + `', forbidden_quality:'1080p', default_quality:'360p'});</script>`
}
