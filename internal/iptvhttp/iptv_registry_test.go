package iptvhttp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/iptv"
	"lampac-go/internal/proxylink"
	"lampac-go/internal/tgauth"

	"github.com/go-chi/chi/v5"
)

// newRegistryTestStore строит store с включённым реестром и одним каналом,
// закреплённым руками (без сети и без донорских плейлистов).
func newRegistryTestStore(t *testing.T) (*iptv.Store, string) {
	t.Helper()
	store := iptv.NewStore(t.TempDir(), iptv.StoreConfig{Registry: true})
	ch, err := store.Registry().Upsert(iptv.RegChannel{
		Name:   "Первый канал",
		TvgID:  "1tv.ru",
		Group:  "Эфир",
		Pinned: []iptv.RegSource{{URL: "https://cdn.example/first.m3u8", Quality: "FHD"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, ch.ID
}

// newStreamAuthFixture — tgauth-store с живым токеном, ключ подписи и
// proxylink-менеджер: всё, что нужно закрытым ручкам stream/export.
func newStreamAuthFixture(t *testing.T) (*tgauth.Store, []byte, *proxylink.Manager, string) {
	t.Helper()
	tgStore := tgauth.NewStore(t.TempDir())
	t.Cleanup(tgStore.Close)
	const token = "tok-live"
	if err := tgStore.Add(tgauth.ApprovedToken{Token: token, TelegramID: 42, ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	links, err := proxylink.New(proxylink.Options{CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return tgStore, loadStreamKey(t.TempDir()), links, token
}

// TestIPTVStreamRedirect: /api/iptv/stream/{id} закрыт токеном и подписью;
// валидная пара → 302 ВСЕГДА на /proxy (сырой апстрим не отдаётся никогда),
// без токена → 401, кривая подпись → 403, просроченный exp → 403.
func TestIPTVStreamRedirect(t *testing.T) {
	store, id := newRegistryTestStore(t)
	tgStore, key, links, token := newStreamAuthFixture(t)
	router := chi.NewRouter()
	router.Get("/api/iptv/stream/{id}", iptvStreamHandler(store, tgStore, key, links))

	get := func(target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		return rec
	}

	// Голый запрос (как из украденного старого экспорта) — 401.
	if rec := get("/api/iptv/stream/" + id); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated must be 401, got %d", rec.Code)
	}
	// Валидный токен, но без подписи — 403.
	if rec := get("/api/iptv/stream/" + id + "?token=" + token); rec.Code != http.StatusForbidden {
		t.Fatalf("unsigned must be 403, got %d", rec.Code)
	}
	// Подпись от ЧУЖОГО токена — 403.
	badSig := signStreamLink(key, id, "other-token", "")
	if rec := get("/api/iptv/stream/" + id + "?token=" + token + "&sig=" + badSig); rec.Code != http.StatusForbidden {
		t.Fatalf("foreign sig must be 403, got %d", rec.Code)
	}
	// Просроченный exp — 403 (и продлить его правкой query нельзя: exp в подписи).
	past := strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10)
	expSig := signStreamLink(key, id, token, past)
	if rec := get("/api/iptv/stream/" + id + "?token=" + token + "&exp=" + past + "&sig=" + expSig); rec.Code != http.StatusForbidden {
		t.Fatalf("expired must be 403, got %d", rec.Code)
	}
	// Валидная пара — 302, и строго на /proxy: cdn.example не должен утечь.
	sig := signStreamLink(key, id, token, "")
	rec := get("/api/iptv/stream/" + id + "?token=" + token + "&sig=" + sig)
	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d: %s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "/proxy/") || strings.Contains(loc, "cdn.example") {
		t.Fatalf("redirect must go via /proxy and never leak upstream, got %q", loc)
	}
	// Незнакомый id (с валидной для него подписью) — 404.
	nopeSig := signStreamLink(key, "own_nope", token, "")
	if rec := get("/api/iptv/stream/own_nope?token=" + token + "&sig=" + nopeSig); rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown channel, got %d", rec.Code)
	}
}

// TestIPTVExportM3U: экспорт открывается только конфигом + валидным токеном;
// ссылки внутри подписаны, привязаны к токену и указывают на СВОЙ сервер.
func TestIPTVExportM3U(t *testing.T) {
	store, id := newRegistryTestStore(t)
	tgStore, key, _, token := newStreamAuthFixture(t)
	cfg := config.Config{}
	cfg.IPTV.EPGUrls = []string{"http://epg.example/e.xml"}

	// Флаг выключен (default) — ручки как будто нет.
	rec := httptest.NewRecorder()
	iptvExportM3UHandler(cfg, store, tgStore, key)(rec, httptest.NewRequest(http.MethodGet, "http://my.host/api/iptv/export.m3u?token="+token, nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("export disabled must be 404, got %d", rec.Code)
	}

	cfg.IPTV.RegistryExport = true
	// Без токена / с левым токеном — 401.
	for _, target := range []string{"http://my.host/api/iptv/export.m3u", "http://my.host/api/iptv/export.m3u?token=stolen"} {
		rec = httptest.NewRecorder()
		iptvExportM3UHandler(cfg, store, tgStore, key)(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s must be 401, got %d", target, rec.Code)
		}
	}

	rec = httptest.NewRecorder()
	iptvExportM3UHandler(cfg, store, tgStore, key)(rec, httptest.NewRequest(http.MethodGet, "http://my.host/api/iptv/export.m3u?token="+token, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	wantURL := "http://my.host/api/iptv/stream/" + id + "?token=" + token + "&sig=" + signStreamLink(key, id, token, "")
	for _, want := range []string{
		`url-tvg="http://epg.example/e.xml"`,
		`tvg-id="1tv.ru"`,
		`group-title="Эфир"`,
		wantURL,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("export missing %q in:\n%s", want, body)
		}
	}
	if strings.Contains(body, "cdn.example") {
		t.Fatalf("export must not leak upstream source URLs:\n%s", body)
	}
}

// TestRegistryChannelsRequireRealToken: каналы/группы/плей реестра закрыты от
// анонима И от md5-фолбэка iptvTgID (выдуманная кука — не авторизация);
// честный tgauth-токен проходит. Личных плейлистов гейт не касается.
func TestRegistryChannelsRequireRealToken(t *testing.T) {
	store, id := newRegistryTestStore(t)
	tgStore, _, links, token := newStreamAuthFixture(t)

	channels := iptvChannelsHandler(store, tgStore, nil, links)
	play := iptvPlayHandler(config.Config{}, store, tgStore, nil, links, nil)
	groups := iptvGroupsHandler(store, tgStore)

	run := func(h http.HandlerFunc, target string, cookie string) int {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: "lampac_token", Value: cookie})
		}
		rec := httptest.NewRecorder()
		h(rec, req)
		return rec.Code
	}

	// Аноним — 401 на всех трёх ручках реестра.
	if c := run(channels, "/api/iptv/channels?playlist_id="+iptv.RegistryPlaylistID+"&play=1", ""); c != http.StatusUnauthorized {
		t.Fatalf("anonymous channels must be 401, got %d", c)
	}
	if c := run(play, "/api/iptv/play?channel_id="+id, ""); c != http.StatusUnauthorized {
		t.Fatalf("anonymous play must be 401, got %d", c)
	}
	if c := run(groups, "/api/iptv/groups?playlist_id="+iptv.RegistryPlaylistID, ""); c != http.StatusUnauthorized {
		t.Fatalf("anonymous groups must be 401, got %d", c)
	}
	// Выдуманная кука (md5-фолбэк iptvTgID) — тоже 401: это не авторизация.
	if c := run(play, "/api/iptv/play?channel_id="+id, "made-up-cookie"); c != http.StatusUnauthorized {
		t.Fatalf("fake cookie play must be 401, got %d", c)
	}
	// Честный токен — 200.
	if c := run(play, "/api/iptv/play?channel_id="+id+"&token="+token, ""); c != http.StatusOK {
		t.Fatalf("real token play must be 200, got %d", c)
	}
	if c := run(channels, "/api/iptv/channels?playlist_id="+iptv.RegistryPlaylistID+"&token="+token, ""); c != http.StatusOK {
		t.Fatalf("real token channels must be 200, got %d", c)
	}
}

// TestAppProofMiddleware: enforce режет запросы без валидного X-App-Proof,
// пропускает подписанные, ловит replay nonce; off пропускает всё.
func TestAppProofMiddleware(t *testing.T) {
	okHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	const secret = "app-secret"

	proof := func(path string, ts int64, nonce string) string {
		mac := hmac.New(sha256.New, []byte(secret))
		tsStr := strconv.FormatInt(ts, 10)
		mac.Write([]byte("v1|" + tsStr + "|" + nonce + "|" + path))
		return "v1." + tsStr + "." + nonce + "." + hex.EncodeToString(mac.Sum(nil))
	}

	iptvCfg := config.IPTVConfig{AppSecret: secret, AppAttest: "enforce"}
	h := appProofMiddleware(iptvCfg)(okHandler)

	// Без заголовка — 401.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/iptv/play", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no proof must be 401, got %d", rec.Code)
	}
	// Валидная подпись — 200.
	req := httptest.NewRequest(http.MethodGet, "/api/iptv/play", nil)
	req.Header.Set("X-App-Proof", proof("/api/iptv/play", time.Now().Unix(), "n-1"))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid proof must pass, got %d", rec.Code)
	}
	// Тот же nonce второй раз — replay, 401.
	req2 := httptest.NewRequest(http.MethodGet, "/api/iptv/play", nil)
	req2.Header.Set("X-App-Proof", proof("/api/iptv/play", time.Now().Unix(), "n-1"))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req2)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("nonce replay must be 401, got %d", rec.Code)
	}
	// Подпись под ДРУГОЙ путь — 401.
	req3 := httptest.NewRequest(http.MethodGet, "/api/iptv/play", nil)
	req3.Header.Set("X-App-Proof", proof("/api/iptv/channels", time.Now().Unix(), "n-2"))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req3)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong-path proof must be 401, got %d", rec.Code)
	}
	// Протухший ts — 401.
	req4 := httptest.NewRequest(http.MethodGet, "/api/iptv/play", nil)
	req4.Header.Set("X-App-Proof", proof("/api/iptv/play", time.Now().Add(-10*time.Minute).Unix(), "n-3"))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req4)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("stale ts must be 401, got %d", rec.Code)
	}

	// off (default) — пропускает без заголовка.
	off := appProofMiddleware(config.IPTVConfig{})(okHandler)
	rec = httptest.NewRecorder()
	off.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/iptv/play", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("attest off must pass everything, got %d", rec.Code)
	}
}

// TestEPGNowResolvesRegistryChannelByName: канал реестра БЕЗ tvg-id (панели их
// сплошь не отдают) всё равно получает программу — по имени; а в ответе стоит
// тот id, о котором спросил клиент, иначе он не сматчит строки гида.
func TestEPGNowResolvesRegistryChannelByName(t *testing.T) {
	now := time.Now().UTC()
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<tv>
  <channel id="rossiya1-slug"><display-name>Россия 1</display-name></channel>
  <programme start="` + now.Add(-time.Hour).Format("20060102150405 +0000") + `" stop="` + now.Add(time.Hour).Format("20060102150405 +0000") + `" channel="rossiya1-slug"><title>Вести</title></programme>
</tv>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(xml))
	}))
	defer srv.Close()

	store := iptv.NewStore(t.TempDir(), iptv.StoreConfig{Registry: true})
	ch, err := store.Registry().Upsert(iptv.RegChannel{
		Name:   "Россия 1 HD", // качество в имени; tvg-id НЕТ
		Pinned: []iptv.RegSource{{URL: "https://cdn/r1.m3u8"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ch.TvgID != "" {
		t.Fatalf("test premise broken: channel must have no tvg-id")
	}

	epg := iptv.NewEPGEngine(store, iptv.EPGConfig{URLs: []string{srv.URL}})
	epg.Refresh()

	rec := httptest.NewRecorder()
	iptvEPGNowHandler(epg, store)(rec, httptest.NewRequest(http.MethodGet, "/api/iptv/epg/now?channel_ids="+ch.ID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var got struct {
		EPG []struct {
			ChannelID string `json:"channel_id"`
			Now       *struct {
				Title string `json:"title"`
			} `json:"now"`
		} `json:"epg"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.EPG) != 1 {
		t.Fatalf("expected one entry, got %+v", got)
	}
	if got.EPG[0].ChannelID != ch.ID {
		t.Fatalf("response must echo the id the client asked about, got %q want %q", got.EPG[0].ChannelID, ch.ID)
	}
	if got.EPG[0].Now == nil || got.EPG[0].Now.Title != "Вести" {
		t.Fatalf("programme not resolved by name: %+v", got.EPG[0])
	}
}
