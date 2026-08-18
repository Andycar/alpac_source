package httpapi

import (
	stdjson "encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestIptvOnlineBindForm(t *testing.T) {
	cfg := config.Config{
		Online: config.OnlineConfig{IptvOnline: config.HostTokenSource{Host: "https://iptv.online"}},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/iptvonline/bind", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "https://iptv.online/ru/dealers/api") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestIptvOnlineBindTokenSnippet(t *testing.T) {
	cfg := config.Config{
		Online: config.OnlineConfig{IptvOnline: config.HostTokenSource{Host: "https://iptv.online"}},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/iptvonline/bind?ID=dealer123&KEY=secret456", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"token": "dealer123:secret456"`) {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestIptvOnlineMovieRjson(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/api/auth":
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected method: %s", r.Method)
			}
			if r.Header.Get("X-API-ID") != "dealer-id" || r.Header.Get("X-API-KEY") != "dealer-key" {
				t.Fatalf("unexpected auth headers: id=%q key=%q", r.Header.Get("X-API-ID"), r.Header.Get("X-API-KEY"))
			}
			_, _ = w.Write([]byte(`{"code":"auth-code"}`))
		case "/v1/api/media/movies":
			if r.Method != http.MethodGet {
				t.Fatalf("unexpected method: %s", r.Method)
			}
			if r.Header.Get("X-API-ID") != "dealer-id" || r.Header.Get("X-API-AUTH") != "auth-code" {
				t.Fatalf("unexpected media search headers: id=%q auth=%q", r.Header.Get("X-API-ID"), r.Header.Get("X-API-AUTH"))
			}
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "Inception") {
				t.Fatalf("unexpected search body: %s", string(body))
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"42","imdb":1375666,"kinopoisk":447301,"orig_title":"Inception","ru_title":"Начало"}]}`))
		case "/v1/api/media/movies/42/":
			if r.Header.Get("X-API-ID") != "dealer-id" || r.Header.Get("X-API-AUTH") != "auth-code" {
				t.Fatalf("unexpected detail headers: id=%q auth=%q", r.Header.Get("X-API-ID"), r.Header.Get("X-API-AUTH"))
			}
			_, _ = w.Write([]byte(`{"data":{"category":"movie","quality":1080,"medias":[{"url":"https://cdn.example/movie/master"}]}}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer api.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			IptvOnline: config.HostTokenSource{Host: api.URL, Token: "dealer-id:dealer-key"},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/iptvonline?rjson=true&serial=0&title=Inception&original_title=Inception&imdb_id=tt1375666", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if toString(payload["type"]) != "movie" {
		t.Fatalf("unexpected type: %v", payload["type"])
	}
	data, _ := payload["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("unexpected data len: %d", len(data))
	}
	first, _ := data[0].(map[string]any)
	if toString(first["url"]) != "https://cdn.example/movie/master#.m3u8" {
		t.Fatalf("unexpected movie url: %v", first["url"])
	}
}

func TestIptvOnlineSerialRjson(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/api/auth":
			_, _ = w.Write([]byte(`{"code":"auth-code"}`))
		case "/v1/api/media/serials":
			_, _ = w.Write([]byte(`{"data":[{"id":"s1","orig_title":"Show","ru_title":"Шоу"}]}`))
		case "/v1/api/media/serials/s1/":
			_, _ = w.Write([]byte(`{"data":{"category":"serial","quality":"720","medias":[{"season":1,"episodes":[{"episode":1,"title":"Pilot","url":"https://cdn.example/s1e1"},{"episode":2,"url":"https://cdn.example/s1e2"}]},{"season":2,"episodes":[{"episode":1,"title":"S2E1","url":"https://cdn.example/s2e1"}]}]}}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer api.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			IptvOnline: config.HostTokenSource{Host: api.URL, Token: "dealer-id:dealer-key"},
		},
	}

	seasonRec := httptest.NewRecorder()
	seasonReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/iptvonline?rjson=true&serial=1&title=Show&original_title=Show", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(seasonRec, seasonReq)

	if seasonRec.Code != http.StatusOK {
		t.Fatalf("unexpected season status: %d body=%s", seasonRec.Code, seasonRec.Body.String())
	}
	var seasonPayload map[string]any
	if err := stdjson.Unmarshal(seasonRec.Body.Bytes(), &seasonPayload); err != nil {
		t.Fatalf("invalid season json: %v", err)
	}
	if toString(seasonPayload["type"]) != "season" {
		t.Fatalf("unexpected season type: %v", seasonPayload["type"])
	}
	seasonData, _ := seasonPayload["data"].([]any)
	if len(seasonData) != 2 {
		t.Fatalf("unexpected season data length: %d", len(seasonData))
	}

	episodeRec := httptest.NewRecorder()
	episodeReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/iptvonline?rjson=true&serial=1&title=Show&original_title=Show&s=1", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(episodeRec, episodeReq)

	if episodeRec.Code != http.StatusOK {
		t.Fatalf("unexpected episode status: %d body=%s", episodeRec.Code, episodeRec.Body.String())
	}
	var episodePayload map[string]any
	if err := stdjson.Unmarshal(episodeRec.Body.Bytes(), &episodePayload); err != nil {
		t.Fatalf("invalid episode json: %v", err)
	}
	if toString(episodePayload["type"]) != "episode" {
		t.Fatalf("unexpected episode type: %v", episodePayload["type"])
	}
	episodeData, _ := episodePayload["data"].([]any)
	if len(episodeData) != 2 {
		t.Fatalf("unexpected episode data length: %d", len(episodeData))
	}
	first, _ := episodeData[0].(map[string]any)
	if toString(first["url"]) != "https://cdn.example/s1e1#.m3u8" {
		t.Fatalf("unexpected first episode url: %v", first["url"])
	}
}

func TestIptvOnlineChecksearch(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/api/auth":
			_, _ = w.Write([]byte(`{"code":"auth-code"}`))
		case "/v1/api/media/movies":
			_, _ = w.Write([]byte(`{"data":[{"id":"m1","orig_title":"Inception","ru_title":"Начало"}]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer api.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			IptvOnline: config.HostTokenSource{Host: api.URL, Token: "dealer-id:dealer-key"},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/iptvonline?checksearch=true&title=Inception&original_title=Inception&serial=0", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"type":"movie"`) {
		t.Fatalf("expected movie marker, got: %s", rec.Body.String())
	}
}
