package httpapi

import (
	"context"
	stdjson "encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"lampac-go/internal/tmdbcache"
)

// Антологии: один сериал, который TMDB держит НЕСКОЛЬКИМИ карточками.
//
// Пример, с которого всё началось (19.09.2026): нетфликсовский «Монстр» — это четыре отдельных
// сериала в TMDB (Дамер 2022, Менендесы 2024, Эд Гин 2025, Лиззи Борден 2026), у каждого «сезон 1».
// Зритель видит четыре несвязанные карточки и не понимает, что это одно шоу.
//
// **Автоматически их не склеить.** Проверено на живых данных: у всех четырёх ПУСТЫЕ `tvdb_id` и
// `imdb_id`, то есть общего внешнего ключа нет вовсе. Остаётся совпадение сети (Netflix), типа
// (Miniseries), создателя и слова Monster в названии — по такому признаку в ту же кучу попадают
// «Школа монстров», японский MONSTER 2004 года и «American Monster». Поэтому список — РУЧНОЙ.
//
// **Почему не слить в один сериал с четырьмя сезонами:** это сломало бы воспроизведение. Онлайн-
// источники и торренты ищутся по названию и номеру сезона, а раздачи каждой части помечены как
// «сезон 1». Карточка стала бы красивой и неиграющей.
//
// Поэтому склейка идёт через механизм, который у клиентов УЖЕ есть, — франшизу фильмов
// (`belongs_to_collection` + `/collection/{id}`). Прокси подставляет её сериалам, и карточка
// получает ряд «все части» с переходом между ними. Клиенты, которые читают это поле (наш Android,
// Lampa), получают ряд без единой правки.
type tmdbGroup struct {
	// ID — СИНТЕТИЧЕСКИЙ id коллекции. Начинается с 9_000_000, чтобы заведомо не пересечься с
	// настоящими коллекциями TMDB (там id четырёх-шестизначные).
	ID    int    `json:"id"`
	Title string `json:"title"`
	// Parts — tmdb id сериалов ПО ПОРЯДКУ выхода. Порядок берём отсюда, а не из дат: у частей
	// антологии даты иногда совпадают с анонсом следующей.
	Parts []int `json:"parts"`
}

const tmdbGroupIDBase = 9_000_000

type tmdbGroupStore struct {
	mu     sync.RWMutex
	path   string
	byPart map[int]tmdbGroup
	byID   map[int]tmdbGroup
}

func newTmdbGroupStore(repoRoot string) *tmdbGroupStore {
	s := &tmdbGroupStore{
		path:   filepath.Join(repoRoot, "database", "tmdb", "groups.json"),
		byPart: map[int]tmdbGroup{},
		byID:   map[int]tmdbGroup{},
	}
	if err := s.Reload(); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Warn().Err(err).Str("path", s.path).Msg("tmdb: группы антологий не прочитаны")
	}
	return s
}

// Reload перечитывает список групп. Зовётся из reloadDiskStores по SIGHUP — добавить антологию
// можно без перезапуска и без обрыва потоков.
func (s *tmdbGroupStore) Reload() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	var list []tmdbGroup
	if err := stdjson.Unmarshal(data, &list); err != nil {
		return err
	}
	byPart := make(map[int]tmdbGroup, len(list)*4)
	byID := make(map[int]tmdbGroup, len(list))
	for _, g := range list {
		if g.ID <= 0 || len(g.Parts) < 2 {
			continue // группа из одной части бессмысленна: ряд «все части» показывать нечему
		}
		byID[g.ID] = g
		for _, p := range g.Parts {
			byPart[p] = g
		}
	}
	s.mu.Lock()
	s.byPart, s.byID = byPart, byID
	s.mu.Unlock()
	log.Info().Int("groups", len(byID)).Int("parts", len(byPart)).Msg("tmdb: группы антологий загружены")
	return nil
}

func (s *tmdbGroupStore) forPart(id int) (tmdbGroup, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g, ok := s.byPart[id]
	return g, ok
}

func (s *tmdbGroupStore) byCollectionID(id int) (tmdbGroup, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g, ok := s.byID[id]
	return g, ok
}

var (
	tmdbTVPathRe         = regexp.MustCompile(`(?:^|/)tv/(\d+)$`)
	tmdbCollectionPathRe = regexp.MustCompile(`(?:^|/)collection/(\d+)$`)
)

// fillGroup подставляет сериалу его антологию: карточка части начинает сообщать
// `belongs_to_collection`, и клиент рисует ряд «все части» тем же кодом, что у франшизы фильмов.
// Настоящую коллекцию (у фильма) не трогаем — только пустое поле у сериала.
func (h *tmdbProxy) fillGroup(tail string, fr *tmdbcache.FetchResult) {
	if h.groups == nil || fr == nil || fr.Status != 200 || len(fr.Body) == 0 {
		return
	}
	m := tmdbTVPathRe.FindStringSubmatch(tail)
	if m == nil {
		return
	}
	id, err := strconv.Atoi(m[1])
	if err != nil {
		return
	}
	g, ok := h.groups.forPart(id)
	if !ok {
		return
	}
	var doc map[string]any
	if err := stdjson.Unmarshal(fr.Body, &doc); err != nil {
		return
	}
	if v, exists := doc["belongs_to_collection"]; exists && v != nil {
		return // у карточки уже есть своя коллекция — чужого не навязываем
	}
	doc["belongs_to_collection"] = map[string]any{
		"id":            g.ID,
		"name":          g.Title,
		"poster_path":   nil,
		"backdrop_path": nil,
	}
	if out, err := stdjson.Marshal(doc); err == nil {
		fr.Body = out
	}
}

// serveGroupCollection отвечает на /collection/<синтетический id> составом антологии в форме
// обычной коллекции TMDB. Возвращает true, если запрос обслужен здесь и наверх идти не нужно.
//
// Данные частей берём из самого TMDB (через тот же пул и кэш), а не из файла групп: иначе название
// и постер пришлось бы поддерживать руками, и они бы устарели при первом же переводе.
func (h *tmdbProxy) serveGroupCollection(ctx context.Context, tail, query string) ([]byte, bool) {
	if h.groups == nil {
		return nil, false
	}
	m := tmdbCollectionPathRe.FindStringSubmatch(tail)
	if m == nil {
		return nil, false
	}
	id, err := strconv.Atoi(m[1])
	if err != nil || id < tmdbGroupIDBase {
		return nil, false // настоящая коллекция TMDB — пусть идёт наверх как обычно
	}
	g, ok := h.groups.byCollectionID(id)
	if !ok {
		return nil, false
	}

	type part struct {
		ID           int     `json:"id"`
		MediaType    string  `json:"media_type"` // «tv» — без него клиент откроет часть как фильм
		Name         string  `json:"name"`
		Title        string  `json:"title"` // дубль для клиентов, читающих только title
		OriginalName string  `json:"original_name,omitempty"`
		Overview     string  `json:"overview,omitempty"`
		PosterPath   string  `json:"poster_path,omitempty"`
		BackdropPath string  `json:"backdrop_path,omitempty"`
		FirstAirDate string  `json:"first_air_date,omitempty"`
		ReleaseDate  string  `json:"release_date,omitempty"` // тот же дубль для старых клиентов
		VoteAverage  float64 `json:"vote_average,omitempty"`
	}

	parts := make([]part, 0, len(g.Parts))
	var mu sync.Mutex
	var wg sync.WaitGroup
	order := make(map[int]int, len(g.Parts))
	for i, pid := range g.Parts {
		order[pid] = i
		wg.Add(1)
		go func(pid int) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 12*time.Second)
			defer cancel()
			fr, err := h.pool.FetchAPI(cctx, "3/tv/"+strconv.Itoa(pid), query)
			if err != nil || fr == nil || fr.Status != 200 {
				return
			}
			var d struct {
				ID           int     `json:"id"`
				Name         string  `json:"name"`
				OriginalName string  `json:"original_name"`
				Overview     string  `json:"overview"`
				PosterPath   string  `json:"poster_path"`
				BackdropPath string  `json:"backdrop_path"`
				FirstAirDate string  `json:"first_air_date"`
				VoteAverage  float64 `json:"vote_average"`
			}
			if stdjson.Unmarshal(fr.Body, &d) != nil || d.ID == 0 {
				return
			}
			mu.Lock()
			parts = append(parts, part{
				ID: d.ID, MediaType: "tv", Name: d.Name, Title: d.Name,
				OriginalName: d.OriginalName, Overview: d.Overview,
				PosterPath: d.PosterPath, BackdropPath: d.BackdropPath,
				FirstAirDate: d.FirstAirDate, ReleaseDate: d.FirstAirDate,
				VoteAverage: d.VoteAverage,
			})
			mu.Unlock()
		}(pid)
	}
	wg.Wait()
	if len(parts) == 0 {
		return nil, false // ни одна часть не ответила — пусть клиент увидит обычную ошибку
	}
	// Порядок — из файла групп, а не по датам: у антологий дата следующей части появляется
	// задолго до выхода и перемешала бы список.
	sortPartsByOrder(parts, func(p part) int { return order[p.ID] })

	out, err := stdjson.Marshal(map[string]any{
		"id":       g.ID,
		"name":     g.Title,
		"overview": "",
		"parts":    parts,
	})
	if err != nil {
		return nil, false
	}
	return out, true
}

// sortPartsByOrder — вставками: частей единицы, заводить sort.Slice ради этого незачем.
func sortPartsByOrder[T any](items []T, key func(T) int) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && key(items[j]) < key(items[j-1]); j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}
