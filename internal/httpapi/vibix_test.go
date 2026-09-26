package httpapi

import (
	"encoding/base64"
	stdjson "encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
)

// vibixTestDirectTransport routes the "vibix" balancer through the shared
// (direct) transport so the source reaches the httptest mock instead of the
// hardcoded residential SOCKS5 (127.0.0.1:40008) that newVibixChecker would
// otherwise register. Must run before the handler builds the checker.
func vibixTestDirectTransport(t *testing.T) {
	resetHTTPAPIGlobals(t)
	httpclient.RegisterBalancerTransport("vibix", httpclient.SharedTransport)
}

// vibixDecoderKey mirrors the litesrc original (test fixture builder).
const vibixDecoderKey = "RySdvcyu5iTUxn97vn4HwoniwgxaCynA"

// vibixEncryptPayload encrypts a JSON payload using the same v=1 scheme.
func vibixEncryptPayload(plainJSON string) string {
	key := []byte(vibixDecoderKey)
	data := []byte(plainJSON)
	for i := range data {
		data[i] ^= key[i%len(key)]
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	// Reverse for useReverse=true
	runes := []rune(encoded)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}

func TestVibixChecksearch(t *testing.T) {
	vibixTestDirectTransport(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Vibix: config.VibixSource{Host: upstream.URL, Token: ""},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://lampac.local/lite/vibix?checksearch=true&title=X", nil)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"type":"movie"`) {
		t.Fatalf("expected show=true payload, got: %s", rec.Body.String())
	}
}

func TestVibixMovieRjson(t *testing.T) {
	vibixTestDirectTransport(t)
	plainJSON := `{"data":{"playlist":[{"title":"Voice","file":"[1080]https://cdn.example/m1080.m3u8,[720]https://cdn.example/m720.m3u8"}]}}`
	encrypted := vibixEncryptPayload(plainJSON)

	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/publisher/videos/kp/123"):
			_, _ = w.Write([]byte(`{"iframe_url":"` + upstream.URL + `/embed/abc","type":"movie"}`))
		case strings.HasPrefix(r.URL.Path, "/api/v1/embed/abc"):
			_, _ = w.Write([]byte(`{"p":"` + encrypted + `","v":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Vibix: config.VibixSource{Host: upstream.URL, Token: "token123"},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodGet,
		"http://lampac.local/lite/vibix?rjson=true&kinopoisk_id=123&title=Film&original_title=Film+Orig",
		nil,
	)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v, body=%s", err, rec.Body.String())
	}
	if payload["type"] != "movie" {
		t.Fatalf("unexpected type: %#v", payload["type"])
	}
	data, ok := payload["data"].([]any)
	if !ok || len(data) == 0 {
		t.Fatalf("unexpected data: %#v", payload["data"])
	}
	// Check method is "play"
	row := data[0].(map[string]any)
	if row["method"] != "play" {
		t.Fatalf("expected method=play, got: %v", row["method"])
	}
}

func TestVibixMovieRjsonLegacy(t *testing.T) {
	vibixTestDirectTransport(t)
	// Test backward compatibility with unencrypted responses
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/publisher/videos/kp/123"):
			_, _ = w.Write([]byte(`{"iframe_url":"` + upstream.URL + `/embed/abc","type":"movie"}`))
		case strings.HasPrefix(r.URL.Path, "/api/v1/embed/abc"):
			_, _ = w.Write([]byte(`{"data":{"playlist":[{"title":"Voice","file":"[1080]https://cdn.example/m1080.m3u8,[720]https://cdn.example/m720.m3u8"}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Vibix: config.VibixSource{Host: upstream.URL, Token: "token123"},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodGet,
		"http://lampac.local/lite/vibix?rjson=true&kinopoisk_id=123&title=Film&original_title=Film+Orig",
		nil,
	)
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	var payload map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v, body=%s", err, rec.Body.String())
	}
	if payload["type"] != "movie" {
		t.Fatalf("unexpected type: %#v", payload["type"])
	}
}

func TestVibixSerialRjson(t *testing.T) {
	vibixTestDirectTransport(t)
	plainJSON := `{"data":{"playlist":[{"title":"Сезон 1","folder":[{"title":"Серия 1","file":"[1080]https://cdn.example/s1e1_1080.m3u8,[720]https://cdn.example/s1e1_720.m3u8"},{"title":"Серия 2","file":"[720]https://cdn.example/s1e2_720.m3u8"}]}]}}`
	encrypted := vibixEncryptPayload(plainJSON)

	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/publisher/videos/kp/777"):
			_, _ = w.Write([]byte(`{"iframe_url":"` + upstream.URL + `/embed-serials/abc","type":"serial"}`))
		case strings.HasPrefix(r.URL.Path, "/api/v1/embed-serials/abc"):
			_, _ = w.Write([]byte(`{"p":"` + encrypted + `","v":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Vibix: config.VibixSource{Host: upstream.URL, Token: "token123"},
		},
	}

	recSeason := httptest.NewRecorder()
	reqSeason := httptest.NewRequest(
		http.MethodGet,
		"http://lampac.local/lite/vibix?rjson=true&kinopoisk_id=777&title=Serial&original_title=Serial+Orig",
		nil,
	)
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
		"http://lampac.local/lite/vibix?rjson=true&kinopoisk_id=777&title=Serial&original_title=Serial+Orig&s=1",
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
}

// TestVibixCapiMovieDeferred verifies the /capi deferred movie path: voices come
// from the publisher API and each quality points at /lite/vibix/stream.m3u8 —
// no browser resolve during the drill.
func TestVibixCapiMovieDeferred(t *testing.T) {
	vibixTestDirectTransport(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/publisher/videos/kp/123") {
			_, _ = w.Write([]byte(`{"type":"movie","quality":"FullHD","voiceovers":[{"id":1,"name":"LostFilm"},{"id":2,"name":"Дубляж MovieDalen"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Vibix: config.VibixSource{Host: upstream.URL, Token: "token123"},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"http://lampac.local/lite/vibix?rjson=true&kinopoisk_id=123&title=Film&original_title=Film+Orig", nil)
	req = req.WithContext(capiWithResolve(req.Context()))
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Type string           `json:"type"`
		Data []map[string]any `json:"data"`
	}
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rec.Body.String())
	}
	if payload.Type != "movie" || len(payload.Data) != 2 {
		t.Fatalf("expected 2-voice movie, got type=%q data=%d: %s", payload.Type, len(payload.Data), rec.Body.String())
	}
	names := map[string]bool{}
	for _, row := range payload.Data {
		names[fmt.Sprint(row["translate"])] = true
		qmap, ok := row["quality"].(map[string]any)
		if !ok || len(qmap) == 0 {
			t.Fatalf("row missing quality map: %#v", row)
		}
		for label, u := range qmap {
			us := fmt.Sprint(u)
			if !strings.Contains(us, "/lite/vibix/stream.m3u8") || !strings.Contains(us, "voice=") || !strings.Contains(us, "q="+label) {
				t.Fatalf("bad deferred url for %s: %s", label, us)
			}
		}
	}
	if !names["LostFilm"] || !names["Дубляж MovieDalen"] {
		t.Fatalf("missing expected voices: %v", names)
	}
}

// TestVibixIframeMovieCapi: with iframe_mode on and a client that advertised
// iframe=1, a movie drill returns a single iframe:// embed-page marker (the vibix
// player runs client-side under our publisher id) instead of the server-resolved
// stream — so it monetizes.
func TestVibixIframeMovieCapi(t *testing.T) {
	vibixTestDirectTransport(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/publisher/videos/kp/123") {
			_, _ = w.Write([]byte(`{"type":"movie","quality":"FullHD","embed_code":"data-publisher-id=\"678652620\" data-type=\"movie\" data-id=\"4433\"","voiceovers":[{"id":1,"name":"LostFilm"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Vibix: config.VibixSource{Host: upstream.URL, Token: "token123", IframeMode: true},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"http://lampac.local/lite/vibix?rjson=true&kinopoisk_id=123&title=Film&iframe=1", nil)
	req = req.WithContext(capiWithResolve(req.Context()))
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Type string           `json:"type"`
		Data []map[string]any `json:"data"`
	}
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rec.Body.String())
	}
	if payload.Type != "movie" || len(payload.Data) != 1 {
		t.Fatalf("expected single iframe voice, got type=%q data=%d: %s", payload.Type, len(payload.Data), rec.Body.String())
	}
	qmap, ok := payload.Data[0]["quality"].(map[string]any)
	if !ok || len(qmap) == 0 {
		t.Fatalf("missing quality map: %#v", payload.Data[0])
	}
	u := fmt.Sprint(qmap["auto"])
	if !strings.HasPrefix(u, "iframe://") || !strings.Contains(u, "/vibix_embed/") {
		t.Fatalf("expected iframe:// embed url, got %s", u)
	}
}

// TestVibixIframeModeWithoutCapability: iframe_mode on but the client did NOT
// advertise iframe=1 → fall back to the server-resolved deferred stream (never
// hand a non-embedding client an unplayable iframe page).
func TestVibixIframeModeWithoutCapability(t *testing.T) {
	vibixTestDirectTransport(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/publisher/videos/kp/123") {
			_, _ = w.Write([]byte(`{"type":"movie","quality":"FullHD","embed_code":"data-publisher-id=\"678652620\" data-type=\"movie\" data-id=\"4433\"","voiceovers":[{"id":1,"name":"LostFilm"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	cfg := config.Config{
		Online: config.OnlineConfig{
			Vibix: config.VibixSource{Host: upstream.URL, Token: "token123", IframeMode: true},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"http://lampac.local/lite/vibix?rjson=true&kinopoisk_id=123&title=Film", nil) // no iframe=1
	req = req.WithContext(capiWithResolve(req.Context()))
	authedHandler(liteSourceHandler(cfg, nil, nil)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "iframe://") {
		t.Fatalf("iframe marker leaked to non-iframe client: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "/lite/vibix/stream.m3u8") {
		t.Fatalf("expected server-resolve fallback, got: %s", rec.Body.String())
	}
}
