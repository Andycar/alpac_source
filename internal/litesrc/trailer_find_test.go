package litesrc

import (
	"context"
	stdjson "encoding/json"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"lampac-go/internal/config"
)

func testFinder(t *testing.T, search func(string, int) ([]ytEntry, error)) *TrailerFinder {
	t.Helper()
	cfg := config.Config{}
	cfg.Compat.RepoRoot = t.TempDir()
	f := NewTrailerFinder(cfg, nil)
	f.searchFn = search
	return f
}

// Ранжирование — сердце фолбэка: на живом поиске «Улица Сезам трейлер» первым
// шла заставка программы, а у «Paradise Hotel» — чужое шоу. Таблица держит
// эти случаи.
func TestRankTrailerPrefersRealTrailer(t *testing.T) {
	q := TrailerQuery{Kind: "tv", ID: 1, Title: "Во все тяжкие", Original: "Breaking Bad", Year: 2008}
	entries := []ytEntry{
		{ID: "recap", Title: "Breaking Bad — весь сериал за 10 минут (recap)", Duration: 600},
		{ID: "good", Title: "Breaking Bad Trailer (First Season) 2008", Duration: 128},
		{ID: "react", Title: "Reaction на трейлер Breaking Bad", Duration: 700},
		{ID: "other", Title: "Better Call Saul official trailer", Duration: 130},
	}
	best, score := rankTrailer(entries, q)
	if best.ID != "good" {
		t.Fatalf("выбрали %q (score %d), ждали good", best.ID, score)
	}
	if score < trailerMinScore {
		t.Fatalf("настоящий трейлер набрал %d < порога %d", score, trailerMinScore)
	}
}

func TestRankTrailerRejectsUnrelated(t *testing.T) {
	q := TrailerQuery{Kind: "tv", ID: 2, Title: "Улица Сезам", Year: 1969}
	entries := []ytEntry{
		{ID: "intro", Title: "Заставка программы Улица Сезам (НТВ, 1996)", Duration: 64},
		{ID: "spec", Title: "The Power of We: A Sesame Street Special", Duration: 41},
	}
	_, score := rankTrailer(entries, q)
	if score >= trailerMinScore {
		t.Fatalf("заставку приняли за трейлер: score %d", score)
	}
}

func TestRankTrailerMoviePenalizesSeasonAndShorts(t *testing.T) {
	q := TrailerQuery{Kind: "movie", ID: 3, Title: "Дюна", Original: "Dune", Year: 2021}
	entries := []ytEntry{
		{ID: "season", Title: "Dune: Prophecy season 1 trailer 2024", Duration: 120},
		{ID: "short", Title: "Dune trailer #shorts", Duration: 15},
		{ID: "movie", Title: "Dune (2021) — Русский трейлер", Duration: 150},
	}
	best, _ := rankTrailer(entries, q)
	if best.ID != "movie" {
		t.Fatalf("выбрали %q, ждали movie", best.ID)
	}
}

func TestTrailerQueriesOrderAndDedup(t *testing.T) {
	qs := trailerQueries(TrailerQuery{Title: "Дюна", Original: "Dune", Year: 2021})
	if len(qs) != 2 || !strings.HasPrefix(qs[0], "Dune 2021 official trailer") || !strings.HasPrefix(qs[1], "Дюна 2021 трейлер") {
		t.Fatalf("запросы: %v", qs)
	}
	// Оригинал совпадает с названием — один запрос, не два одинаковых.
	if qs := trailerQueries(TrailerQuery{Title: "Dune", Original: "dune"}); len(qs) != 1 {
		t.Fatalf("дубль запроса: %v", qs)
	}
	if qs := trailerQueries(TrailerQuery{}); len(qs) != 0 {
		t.Fatalf("пустой запрос должен давать пусто: %v", qs)
	}
}

// Find ждёт поиск, кладёт в кэш, а повторный вызов уже не ищет.
func TestFinderFindCachesAndDedups(t *testing.T) {
	var calls int32
	f := testFinder(t, func(q string, n int) ([]ytEntry, error) {
		atomic.AddInt32(&calls, 1)
		return []ytEntry{{ID: "abc", Title: "Дюна 2021 официальный трейлер", Duration: 140}}, nil
	})
	q := TrailerQuery{Kind: "movie", ID: 10, Title: "Дюна", Original: "Dune", Year: 2021}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hit, ok := f.Find(ctx, q)
	if !ok || !hit.Found() || hit.Key != "abc" || hit.Lang != "ru" {
		t.Fatalf("Find: %+v ok=%v", hit, ok)
	}
	if _, ok := f.Find(ctx, q); !ok {
		t.Fatal("второй Find должен ответить из кэша")
	}
	if c := atomic.LoadInt32(&calls); c != 2 { // два запроса (оригинал + русский) за ОДИН поиск
		t.Fatalf("поиск дёрнули %d раз, ждали 2 (один поиск из двух запросов)", c)
	}
}

// Пустой результат — это Miss: помним и не ищем заново; ошибка поиска — Err,
// помним коротко.
func TestFinderMissAndErrAreRemembered(t *testing.T) {
	var calls int32
	f := testFinder(t, func(q string, n int) ([]ytEntry, error) {
		atomic.AddInt32(&calls, 1)
		return nil, nil
	})
	q := TrailerQuery{Kind: "tv", ID: 20, Title: "Нечто"}
	ctx := context.Background()
	hit, ok := f.Find(ctx, q)
	if !ok || !hit.Miss || hit.Found() {
		t.Fatalf("ждали Miss: %+v ok=%v", hit, ok)
	}
	f.Find(ctx, q)
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("после Miss искали снова: %d вызовов", calls)
	}
	// Протухший Miss ищется заново.
	f.Remember("tv", 20, TrailerHit{Miss: true, At: time.Now().Add(-trailerMissTTL - time.Hour)})
	f.mu.Lock()
	f.cache["tv:20"] = TrailerHit{Miss: true, At: time.Now().Add(-trailerMissTTL - time.Hour)}
	f.mu.Unlock()
	if _, known := f.Cached("tv", 20); known {
		t.Fatal("протухший Miss должен читаться как неизвестный")
	}
}

// Без yt-dlp (searchFn=nil) и при выключенном поиске Schedule честно отказывает —
// прокси тогда отдаёт ответ TMDB как есть и кэширует его.
func TestFinderDisabled(t *testing.T) {
	cfg := config.Config{}
	cfg.Compat.RepoRoot = t.TempDir()
	cfg.YouTube.DisableTrailerSearch = true
	f := NewTrailerFinder(cfg, nil)
	if f.Schedule(TrailerQuery{Kind: "movie", ID: 1, Title: "x"}) {
		t.Fatal("выключенный поиск не должен планироваться")
	}
	if _, ok := f.Find(context.Background(), TrailerQuery{Kind: "movie", ID: 1, Title: "x"}); ok {
		t.Fatal("выключенный поиск не должен отдавать результат")
	}
}

func TestTrailerFindHandler(t *testing.T) {
	f := testFinder(t, func(q string, n int) ([]ytEntry, error) {
		return []ytEntry{{ID: "k1", Title: "Some Movie official trailer 2020", Duration: 100}}, nil
	})
	h := TrailerFindHandler(f)
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", "/lite/trailer/find?type=movie&id=7&title=Some+Movie&year=2020", nil))
	var resp map[string]any
	if err := stdjson.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp["key"] != "k1" {
		t.Fatalf("ответ: %s (%v)", rec.Body.String(), err)
	}
	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", "/lite/trailer/find?type=bogus&id=7", nil))
	if rec.Code != 400 {
		t.Fatalf("плохой type должен давать 400, got %d", rec.Code)
	}
}
