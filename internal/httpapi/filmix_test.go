package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
)

func TestFilmixChecksearchNative(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/search" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("story") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`[{"id":100}]`))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Filmix: config.FilmixSource{Host: upstream.URL, Token: "filmix-token"},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/filmix?checksearch=true&title=Inception&year=2010", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"type":"movie"`) {
		t.Fatalf("expected movie marker, got: %s", rec.Body.String())
	}
}

// deadFXServer returns an api.filmix.tv stand-in whose request-token always
// fails, forcing the legacy filmixapp fallback path without touching the network.
func deadFXServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFilmixFullModeMovieFromSourceID(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/post/123" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"player_links":{"movie":[{"translation":"Dub","link":"https://cdn1.example/s/hash1/path_[2160,1080,720,480].mp4"}]}}`))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Filmix:   config.FilmixSource{Host: upstream.URL, Token: "token", HLS: true},
			FilmixTV: config.HostSource{Host: deadFXServer(t).URL},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/filmix?rjson=true&source=filmix&id=/123-test&title=Film", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rec.Body.String())
	}
	if toString(payload["type"]) != "movie" {
		t.Fatalf("unexpected type: %#v body=%s", payload["type"], rec.Body.String())
	}

	data, _ := payload["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("unexpected data rows: %d body=%s", len(data), rec.Body.String())
	}
	row, _ := data[0].(map[string]any)
	if !strings.Contains(toString(row["url"]), "/hls/path_720.mp4/index.m3u8?hash=hash1") {
		t.Fatalf("unexpected selected stream: %#v body=%s", row["url"], rec.Body.String())
	}
	streams, _ := row["streamquality"].([]any)
	if len(streams) != 1 {
		t.Fatalf("unexpected streamquality count: %d body=%s", len(streams), rec.Body.String())
	}
}

func TestFilmixFullModeSerialSeasonAndEpisode(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/post/555" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{
			"player_links":{
				"playlist":{
					"1":{
						"Dub":{
							"1":{"link":"https://cdn2.example/s/hash2/serial_%s.mp4","qualities":[1080,720,480]},
							"2":{"link":"https://cdn2.example/s/hash2/serial_%s.mp4","qualities":[720]}
						},
						"Orig":[
							{"link":"https://cdn3.example/s/hash3/serial_%s.mp4","qualities":[480]}
						]
					}
				}
			}
		}`))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Filmix:   config.FilmixSource{Host: upstream.URL, Token: "token", HLS: true},
			FilmixTV: config.HostSource{Host: deadFXServer(t).URL},
		},
	}

	seasonRec := httptest.NewRecorder()
	seasonReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/filmix?rjson=true&postid=555&title=Serial", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(seasonRec, seasonReq)
	if seasonRec.Code != http.StatusOK {
		t.Fatalf("unexpected season status: %d body=%s", seasonRec.Code, seasonRec.Body.String())
	}

	var seasonPayload map[string]any
	if err := stdjson.Unmarshal(seasonRec.Body.Bytes(), &seasonPayload); err != nil {
		t.Fatalf("season unmarshal: %v body=%s", err, seasonRec.Body.String())
	}
	if toString(seasonPayload["type"]) != "season" {
		t.Fatalf("unexpected season type: %#v body=%s", seasonPayload["type"], seasonRec.Body.String())
	}

	episodeRec := httptest.NewRecorder()
	episodeReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/filmix?rjson=true&postid=555&title=Serial&s=1", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(episodeRec, episodeReq)
	if episodeRec.Code != http.StatusOK {
		t.Fatalf("unexpected episode status: %d body=%s", episodeRec.Code, episodeRec.Body.String())
	}

	var epPayload map[string]any
	if err := stdjson.Unmarshal(episodeRec.Body.Bytes(), &epPayload); err != nil {
		t.Fatalf("episode unmarshal: %v body=%s", err, episodeRec.Body.String())
	}
	if toString(epPayload["type"]) != "episode" {
		t.Fatalf("unexpected episode type: %#v body=%s", epPayload["type"], episodeRec.Body.String())
	}
	voices, _ := epPayload["voice"].([]any)
	if len(voices) != 2 {
		t.Fatalf("unexpected voice rows: %d body=%s", len(voices), episodeRec.Body.String())
	}
	data, _ := epPayload["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("unexpected episode rows: %d body=%s", len(data), episodeRec.Body.String())
	}
	firstRow, _ := data[0].(map[string]any)
	if !strings.Contains(toString(firstRow["url"]), "/hls/serial_720.mp4/index.m3u8?hash=hash2") {
		t.Fatalf("unexpected episode stream: %#v body=%s", firstRow["url"], episodeRec.Body.String())
	}
}

func TestFilmixSearchSimilarMode(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/search":
			_, _ = w.Write([]byte(`[
				{"id":101,"title":"Inception","original_title":"Inception","poster":"https://img/1.jpg","year":2010},
				{"id":102,"title":"Interstellar","original_title":"Interstellar","poster":"https://img/2.jpg","year":2014}
			]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Filmix: config.FilmixSource{Host: upstream.URL, Token: "token"},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/filmix?rjson=true&title=Inception&year=2010&similar=true", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rec.Body.String())
	}
	if toString(payload["type"]) != "similar" {
		t.Fatalf("unexpected type: %#v body=%s", payload["type"], rec.Body.String())
	}
	data, _ := payload["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("unexpected similar rows: %d body=%s", len(data), rec.Body.String())
	}
}

// fxServer returns an api.filmix.tv stand-in serving request-token and a fixed
// video-links body for the given post.
func fxServer(t *testing.T, postPath, videoLinksJSON string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api-fx/request-token":
			_, _ = w.Write([]byte(`{"token":"fxhash1"}`))
		case postPath:
			if r.Header.Get("hash") != "fxhash1" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_, _ = w.Write([]byte(videoLinksJSON))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The api-fx path must win over the legacy API and pass its 4K HLS links through
// untouched — the legacy /s/ hashes cap at 720p (premium stub above), video-links
// is the only shape that streams >720p.
func TestFilmixFXMoviePreferredOverLegacy(t *testing.T) {
	legacyCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		legacyCalled = true
		w.WriteHeader(http.StatusNotFound)
	}))
	defer upstream.Close()

	fx := fxServer(t, "/api-fx/post/123/video-links", `[
		{"voiceover":"Дубляж [Rus, 4K]","files":[
			{"url":"https://nl1.cdnsqu.com/hls/film_480.mp4/index.m3u8?hash=h","quality":480},
			{"url":"https://nl1.cdnsqu.com/hls/film_2160.mp4/index.m3u8?hash=h","quality":2160},
			{"url":"https://nl1.cdnsqu.com/hls/film_1080.mp4/index.m3u8?hash=h","quality":1080}
		]},
		{"voiceover":"Empty","files":[]}
	]`)

	cfg := config.Config{
		Online: config.OnlineConfig{
			Filmix:   config.FilmixSource{Host: upstream.URL, Token: "token"},
			FilmixTV: config.HostSource{Host: fx.URL},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/filmix?rjson=true&postid=123&title=Film", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rec.Body.String())
	}
	if toString(payload["type"]) != "movie" {
		t.Fatalf("unexpected type: %#v body=%s", payload["type"], rec.Body.String())
	}
	data, _ := payload["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("unexpected data rows: %d body=%s", len(data), rec.Body.String())
	}
	row, _ := data[0].(map[string]any)
	if !strings.Contains(toString(row["url"]), "film_2160.mp4") {
		t.Fatalf("expected 2160 first: %#v", row["url"])
	}
	streams, _ := row["streamquality"].([]any)
	if len(streams) != 3 {
		t.Fatalf("unexpected streamquality count: %d body=%s", len(streams), rec.Body.String())
	}
	// ★С 2026-08-22 легаси ВЫЗЫВАЕТСЯ и при успешном api-fx — но только чтобы добрать озвучки,
	// которых там нет вовсе (HEVC-рипы; см. filmix_merge.go). Приоритет остаётся за api-fx: его
	// строки идут первыми и не подменяются, что и проверяется выше по url и streamquality.
	if !legacyCalled {
		t.Fatalf("легаси должен опрашиваться для добора эксклюзивных озвучек")
	}
}

func TestFilmixFXSerialSeasonsAndEpisodes(t *testing.T) {
	fx := fxServer(t, "/api-fx/post/555/video-links", `{
		"Дубляж":{
			"season-1":{"season":1,"episodes":{
				"e2":{"episode":2,"files":[{"url":"https://nl1.cdnsqu.com/hls/s1e2_1080.mp4/index.m3u8?hash=h","quality":1080}]},
				"e1":{"episode":1,"files":[
					{"url":"https://nl1.cdnsqu.com/hls/s1e1_720.mp4/index.m3u8?hash=h","quality":720},
					{"url":"https://nl1.cdnsqu.com/hls/s1e1_2160.mp4/index.m3u8?hash=h","quality":2160}
				]}
			}},
			"season-2":{"season":2,"episodes":{
				"e1":{"episode":1,"files":[{"url":"https://nl1.cdnsqu.com/hls/s2e1_480.mp4/index.m3u8?hash=h","quality":480}]}
			}}
		},
		"Orig":{
			"season-1":{"season":1,"episodes":{
				"e1":{"episode":1,"files":[{"url":"https://nl1.cdnsqu.com/hls/orig_480.mp4/index.m3u8?hash=h","quality":480}]}
			}}
		}
	}`)

	// Легаси-плечо должно молчать, но глушить его мёртвым портом нельзя:
	// filmixAPIHosts() ротирует зеркало именно на ТРАНСПОРТНОЙ ошибке, и
	// недозвон на 127.0.0.1:1 уводил запрос на зашитый живой filmixapp.cyou —
	// тест тянул настоящие серии с werkecdn.me, а заодно «залипал» на боевом
	// зеркале в process-wide filmixHostIdx и ронял соседние FilmixFX-тесты.
	// Явный 404 — это РЕАЛЬНЫЙ ответ мирроринга, он не ротирует (см. filmix.go).
	legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer legacy.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Filmix:   config.FilmixSource{Host: legacy.URL, Token: "token"},
			FilmixTV: config.HostSource{Host: fx.URL},
		},
	}

	seasonRec := httptest.NewRecorder()
	seasonReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/filmix?rjson=true&postid=555&title=Serial", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(seasonRec, seasonReq)
	if seasonRec.Code != http.StatusOK {
		t.Fatalf("unexpected season status: %d body=%s", seasonRec.Code, seasonRec.Body.String())
	}
	var seasonPayload map[string]any
	if err := stdjson.Unmarshal(seasonRec.Body.Bytes(), &seasonPayload); err != nil {
		t.Fatalf("season unmarshal: %v body=%s", err, seasonRec.Body.String())
	}
	if toString(seasonPayload["type"]) != "season" {
		t.Fatalf("unexpected season type: %#v body=%s", seasonPayload["type"], seasonRec.Body.String())
	}
	seasons, _ := seasonPayload["data"].([]any)
	if len(seasons) != 2 {
		t.Fatalf("unexpected season rows: %d body=%s", len(seasons), seasonRec.Body.String())
	}

	// Voices sort like the legacy path (lexicographic): "Orig" < "Дубляж", so
	// t=1 selects the two-episode Дубляж voice.
	episodeRec := httptest.NewRecorder()
	episodeReq := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/filmix?rjson=true&postid=555&title=Serial&s=1&t=1", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(episodeRec, episodeReq)
	if episodeRec.Code != http.StatusOK {
		t.Fatalf("unexpected episode status: %d body=%s", episodeRec.Code, episodeRec.Body.String())
	}
	var epPayload map[string]any
	if err := stdjson.Unmarshal(episodeRec.Body.Bytes(), &epPayload); err != nil {
		t.Fatalf("episode unmarshal: %v body=%s", err, episodeRec.Body.String())
	}
	if toString(epPayload["type"]) != "episode" {
		t.Fatalf("unexpected episode type: %#v body=%s", epPayload["type"], episodeRec.Body.String())
	}
	voices, _ := epPayload["voice"].([]any)
	if len(voices) != 2 {
		t.Fatalf("unexpected voice rows: %d body=%s", len(voices), episodeRec.Body.String())
	}
	data, _ := epPayload["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("unexpected episode rows: %d body=%s", len(data), episodeRec.Body.String())
	}
	firstRow, _ := data[0].(map[string]any)
	if !strings.Contains(toString(firstRow["url"]), "s1e1_2160.mp4") {
		t.Fatalf("expected e1 2160 first: %#v body=%s", firstRow["url"], episodeRec.Body.String())
	}
	if firstRow["e"].(float64) != 1 {
		t.Fatalf("unexpected episode order: %#v", firstRow["e"])
	}
}

// A geo-degraded api-fx answer (480p-only preview for non-RU IPs) must not
// shadow the legacy path, which still serves 720p with a token.
func TestFilmixFXGeoCappedPrefersLegacy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/post/123" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"player_links":{"movie":[{"translation":"Dub","link":"https://cdn1.example/s/hash1/path_[2160,1080,720,480,].mp4"}]}}`))
	}))
	defer upstream.Close()

	fx := fxServer(t, "/api-fx/post/123/video-links", `[
		{"voiceover":"Dub","files":[{"url":"https://nl1.cdnsqu.com/hls/film_480.mp4/index.m3u8?hash=h","quality":480}]}
	]`)

	cfg := config.Config{
		Online: config.OnlineConfig{
			Filmix:   config.FilmixSource{Host: upstream.URL, Token: "token"},
			FilmixTV: config.HostSource{Host: fx.URL},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/filmix?rjson=true&postid=123&title=Film", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rec.Body.String())
	}
	data, _ := payload["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("unexpected data rows: %d body=%s", len(data), rec.Body.String())
	}
	row, _ := data[0].(map[string]any)
	streams, _ := row["streamquality"].([]any)
	first, _ := streams[0].(map[string]any)
	if toString(first["quality"]) != "720p" {
		t.Fatalf("legacy 720p must win over geo-capped fx 480p, got %#v body=%s", first["quality"], rec.Body.String())
	}
}

// A blocked-title message from api-fx must fall back to the legacy API (which,
// with a token, still serves ≤720p).
func TestFilmixFXBlockedFallsBackToLegacy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/post/123" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"player_links":{"movie":[{"translation":"Dub","link":"https://cdn1.example/s/hash1/path_[2160,1080,720,480,].mp4"}]}}`))
	}))
	defer upstream.Close()

	fx := fxServer(t, "/api-fx/post/123/video-links", `{"message":"Видео заблокировано!"}`)

	cfg := config.Config{
		Online: config.OnlineConfig{
			Filmix:   config.FilmixSource{Host: upstream.URL, Token: "token"},
			FilmixTV: config.HostSource{Host: fx.URL},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/filmix?rjson=true&postid=123&title=Film", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rec.Body.String())
	}
	if toString(payload["type"]) != "movie" {
		t.Fatalf("unexpected type: %#v body=%s", payload["type"], rec.Body.String())
	}
	data, _ := payload["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("unexpected data rows: %d body=%s", len(data), rec.Body.String())
	}
	row, _ := data[0].(map[string]any)
	streams, _ := row["streamquality"].([]any)
	// Legacy path must stay capped at 720p — 2160/1080 stream the premium stub.
	for _, s := range streams {
		sm, _ := s.(map[string]any)
		if q := toString(sm["quality"]); q == "2160p" || q == "1080p" {
			t.Fatalf("legacy fallback leaked >720p: %s body=%s", q, rec.Body.String())
		}
	}
	if len(streams) != 2 {
		t.Fatalf("unexpected streamquality count: %d body=%s", len(streams), rec.Body.String())
	}
}

// toHLS rewrites the legacy "/s/<hash>/" form into "/hls/.../index.m3u8?hash=". The current API
// already returns the ready "/hls/..." shape (toHLS is a no-op there); the inline-origin double-"?"
// normalization now lives ONLY in the proxy fetch layer (proxyapi.fixFilmixDoubleQuery) so that the
// /lite/filmix output handed to native Lampa clients is byte-identical to upstream.

// Прямой CDN (архитектура B): с fxdirect=1 сервер отдаёт РЕЦЕПТ самостоятельного минта, а не
// проксированные потоки — клиент дальше качает CDN напрямую. Без флага поведение прежнее.
func TestFilmixDirectDescriptor(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound) // никаких обращений к легаси в direct-режиме
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			// Рецепт отдаётся только при включённом direct_lampa — параметра в запросе мало.
			Filmix:   config.FilmixSource{Host: upstream.URL, Token: "token", DirectLampa: true},
			FilmixTV: config.HostSource{Host: "https://api.filmix.tv"},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"http://lampac.local/lite/filmix?rjson=true&fxdirect=1&postid=7430&title=HA", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rec.Body.String())
	}
	if toString(payload["type"]) != "filmix-direct" {
		t.Fatalf("expected filmix-direct, got %#v", payload["type"])
	}
	data, _ := payload["data"].(map[string]any)
	if toString(data["method"]) != "filmix-direct" {
		t.Fatalf("unexpected method: %#v", data["method"])
	}
	if !strings.Contains(toString(data["video_links"]), "/api-fx/post/7430/video-links") {
		t.Fatalf("bad video_links: %#v", data["video_links"])
	}
	if data["inherit_hash"] != true {
		t.Fatalf("inherit_hash must be true (HDR EXT-X-MAP) — %#v", data["inherit_hash"])
	}
	// Fallback указывает на тот же postid БЕЗ fxdirect (серверный проксирующий путь).
	fb := toString(data["fallback"])
	if !strings.Contains(fb, "postid=7430") || strings.Contains(fb, "fxdirect") {
		t.Fatalf("bad fallback: %s", fb)
	}
}

// Сниппет прямого CDN живёт СТАТИКОЙ в plugins/online.js, поэтому Лампа шлёт `fxdirect=1` и
// после выключения `direct_lampa`. Раньше сервер честно отдавал рецепт: браузеры минтили хеш
// сами и упирались в 429 от werkecdn, тогда как приложение (параметр не шлёт) спокойно играло
// через /proxy. Выключатель обязан действовать без переката клиентов.
func TestFilmixDirectIgnoredWhenDisabled(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Filmix:   config.FilmixSource{Host: upstream.URL, Token: "token"}, // DirectLampa не задан
			FilmixTV: config.HostSource{Host: "https://api.filmix.tv"},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"http://lampac.local/lite/filmix?rjson=true&fxdirect=1&postid=7430&title=HA", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "\"recipe\"") || strings.Contains(body, "fallback") {
		t.Fatalf("при выключенном флаге отдан рецепт прямого CDN: %s", body)
	}
}
