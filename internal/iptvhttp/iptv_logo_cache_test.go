package iptvhttp

import (
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"lampac-go/internal/iptv"
)

// ageFile отматывает mtime файла за окно свежести logoFreshFor.
func ageFile(t *testing.T, path string) {
	t.Helper()
	old := time.Now().Add(-logoFreshFor - time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

// TestIPTVLogoCacheStaleFallback: логотип канала реестра после первой отдачи
// живёт в дисковом кэше — свежий кэш отдаётся без похода на upstream, а при
// смерти upstream'а отдаётся последняя удачная копия.
func TestIPTVLogoCacheStaleFallback(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("PNGBYTES"))
	}))

	root := t.TempDir()
	store := iptv.NewStore(root, iptv.StoreConfig{Registry: true})
	ch, err := store.Registry().Upsert(iptv.RegChannel{
		Name:   "Первый канал",
		Logo:   upstream.URL + "/logo.png",
		Pinned: []iptv.RegSource{{URL: "https://cdn.example/first.m3u8"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	logos := newLogoCache(root)
	handler := iptvLogoHandler(store, nil, nil, logos)
	get := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		handler(rec, httptest.NewRequest(http.MethodGet, "/api/iptv/logo?channel_id="+ch.ID, nil))
		return rec
	}

	// Первая отдача: upstream + кэш на диск.
	if rec := get(); rec.Code != 200 || rec.Body.String() != "PNGBYTES" {
		t.Fatalf("first fetch: %d %q", rec.Code, rec.Body.String())
	}
	if hits.Load() != 1 {
		t.Fatalf("expected 1 upstream hit, got %d", hits.Load())
	}

	// Вторая отдача: свежий кэш, upstream не трогаем.
	if rec := get(); rec.Code != 200 || rec.Body.String() != "PNGBYTES" {
		t.Fatalf("cached fetch: %d", rec.Code)
	}
	if hits.Load() != 1 {
		t.Fatalf("fresh cache must not hit upstream, got %d hits", hits.Load())
	}

	// Upstream умер, кэш насильно «состарен» → идём на upstream, падаем,
	// отдаём последнюю копию.
	upstream.Close()
	dataPath, _ := logos.paths(ch.Logo)
	ageFile(t, dataPath)
	if rec := get(); rec.Code != 200 || rec.Body.String() != "PNGBYTES" {
		t.Fatalf("stale fallback: %d %q", rec.Code, rec.Body.String())
	}
}

// TestIPTVLogoMissingIsCacheable: у канала без логотипа отказ должен быть
// кэшируемым, иначе клиент переспрашивает его на каждом экране. На проде это
// давало ~6700 запросов в час, 95% из которых — именно эта ветка.
//
// TTL разные: «канал есть, логотипа нет» держится до переразбора плейлиста,
// а «канала не знаю» — короткий, иначе появившийся после обновления канал
// сутки оставался бы без иконки.
func TestIPTVLogoMissingIsCacheable(t *testing.T) {
	root := t.TempDir()
	store := iptv.NewStore(root, iptv.StoreConfig{Registry: true})
	noLogo, err := store.Registry().Upsert(iptv.RegChannel{
		Name:   "Канал без логотипа",
		Pinned: []iptv.RegSource{{URL: "https://cdn.example/nologo.m3u8"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	handler := iptvLogoHandler(store, nil, nil, newLogoCache(root))
	get := func(id string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		handler(rec, httptest.NewRequest(http.MethodGet, "/api/iptv/logo?channel_id="+id, nil))
		return rec
	}

	rec := get(noLogo.ID)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("канал без логотипа: код %d, ожидался 404", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=86400" {
		t.Fatalf("канал без логотипа: Cache-Control %q, ожидался суточный", got)
	}

	rec = get("own_deadbeefdeadbeef")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("неизвестный канал: код %d, ожидался 404", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=300" {
		t.Fatalf("неизвестный канал: Cache-Control %q, ожидался короткий", got)
	}
}

// TestIPTVLogoDeadHostIsNotRetried: мёртвый хост логотипа не должен опрашиваться на каждый
// запрос. На проде это давало 216–517 ответов 502 в час в вечерний пик (замер 2026-09-08):
// дисковый кэш спасает только каналы РЕЕСТРА, а отказы приходили от донорских каналов, и
// каждый следующий зритель шёл к тому же мёртвому адресу и ждал таймаут заново.
func TestIPTVLogoDeadHostIsNotRetried(t *testing.T) {
	logoFailMu.Lock()
	logoFail = map[string]time.Time{}
	logoFailMu.Unlock()

	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError) // хост жив, но картинку не отдаёт
	}))
	defer upstream.Close()

	root := t.TempDir()
	store := iptv.NewStore(root, iptv.StoreConfig{Registry: true})
	// Донорский канал (не own_*): дискового кэша у него нет — ровно тот случай, что давал 502.
	ch, err := store.Registry().Upsert(iptv.RegChannel{
		Name:   "Канал без картинки",
		Logo:   upstream.URL + "/dead.png",
		Pinned: []iptv.RegSource{{URL: "https://cdn.example/x.m3u8"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := iptvLogoHandler(store, nil, nil, nil) // logos=nil → кэша нет, как у донора
	get := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		handler(rec, httptest.NewRequest(http.MethodGet, "/api/iptv/logo?channel_id="+ch.ID, nil))
		return rec
	}

	rec := get()
	if rec.Code != http.StatusNotFound {
		t.Fatalf("отказ хоста должен читаться как «нет картинки» (404), а не как авария шлюза: %d", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc == "" {
		t.Fatal("ответ без Cache-Control — клиент будет переспрашивать на каждом экране")
	}
	if hits.Load() != 1 {
		t.Fatalf("ожидали один поход наверх, было %d", hits.Load())
	}

	// Следующие запросы обязаны обслуживаться из отрицательного кэша, БЕЗ похода наверх.
	for i := 0; i < 5; i++ {
		if rec := get(); rec.Code != http.StatusNotFound {
			t.Fatalf("повтор %d: код %d", i, rec.Code)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("мёртвый хост опрошен %d раз вместо одного", hits.Load())
	}

	// По истечении срока запрет снимается — ребрендинг или починка хоста долетят.
	logoFailMu.Lock()
	logoFail[ch.Logo] = time.Now().Add(-logoFailFor - time.Minute)
	logoFailMu.Unlock()
	_ = get()
	if hits.Load() != 2 {
		t.Fatalf("после истечения срока ожидали повторную попытку, походов: %d", hits.Load())
	}
}
