package litesrc

import (
	"context"
	stdjson "encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/rs/zerolog/log"

	"lampac-go/internal/config"
)

// Поиск трейлера там, где TMDB пуст.
//
// Замер 2026-09-18 по 51 популярному тайтлу: у 22% в TMDB нет ни одного
// YouTube-видео — русские релизы, аниме, старые сериалы. Для них
// единственный источник трейлера — поиск по YouTube (yt-dlp ytsearch,
// ~1.7 с на запрос). Результат запоминается на диске: трейлер у тайтла не
// меняется, а каждый поиск — это обращение к YouTube с нашего IP, которого
// и так хватает (бот-чек 16.09 — свежая память).
//
// Кто пользуется: TMDB-прокси (подкладывает найденное в videos.results, и
// любой клиент — Android, Lampa, tvOS — получает трейлер без своего релиза)
// и ручка /lite/trailer/find для веба, который в TMDB ходит мимо нас.

// TrailerQuery — что ищем. Title/Original/Year нужны только для
// формирования запроса и ранжирования; ключ кэша — Kind+ID.
type TrailerQuery struct {
	Kind     string // "movie" | "tv"
	ID       int
	Title    string // локализованное название
	Original string // оригинальное (обычно английское)
	Year     int
}

func (q TrailerQuery) key() string { return q.Kind + ":" + strconv.Itoa(q.ID) }

// TrailerHit — результат поиска, как он лежит в кэше.
type TrailerHit struct {
	Key    string    `json:"key,omitempty"`
	Title  string    `json:"title,omitempty"`
	Lang   string    `json:"lang,omitempty"` // "ru" | "en" — по алфавиту названия ролика
	Source string    `json:"source,omitempty"`
	At     time.Time `json:"at"`
	// Miss — искали и не нашли ничего похожего на трейлер. Помним, чтобы не
	// искать заново на каждое открытие карточки. Err — поиск не удался
	// (yt-dlp/сеть/бот-чек): помним коротко и пробуем снова.
	Miss bool `json:"miss,omitempty"`
	Err  bool `json:"err,omitempty"`
}

// Found — есть ли ключ, который можно отдать плееру.
func (h TrailerHit) Found() bool { return h.Key != "" && !h.Miss }

const (
	trailerHitTTL  = 30 * 24 * time.Hour // найденный трейлер — стабилен
	trailerMissTTL = 3 * 24 * time.Hour  // «нет трейлера» — вдруг появится
	trailerErrTTL  = 15 * time.Minute    // поиск упал — не долбить, но и не хоронить
	// trailerSearchSlots — сколько поисков идёт одновременно. Меньше, чем
	// общий лимит yt-dlp (3): трейлер не должен отнимать слот у воспроизведения.
	trailerSearchSlots = 2
	// trailerPendingMax — потолок отложенных поисков. Список «популярное»
	// открывает десятки карточек разом; всё, что не влезло, подхватится при
	// следующем открытии — кэш промахов гарантирует, что это не навсегда.
	trailerPendingMax = 32
	// trailerMinScore — ниже этого кандидат не похож на трейлер (см. rankTrailer).
	trailerMinScore = 4
)

// TrailerFinder — кэш + поиск. Потокобезопасен.
type TrailerFinder struct {
	yt       *YoutubeChecker
	disabled bool
	path     string

	// searchFn — точка подмены для тестов; по умолчанию yt.ytSearchN.
	searchFn func(query string, limit int) ([]ytEntry, error)

	mu       sync.Mutex
	cache    map[string]TrailerHit
	inflight map[string]chan struct{} // ждущие результата по ключу
	pending  int
	dirty    bool
	sem      chan struct{}
}

var (
	trailerFinderOnce sync.Once
	trailerFinderInst *TrailerFinder
)

// SharedTrailerFinder — процессный синглтон (как SharedYoutubeChecker): кэш
// и очередь поисков должны быть одни на все ручки.
func SharedTrailerFinder(cfg config.Config) *TrailerFinder {
	trailerFinderOnce.Do(func() {
		trailerFinderInst = NewTrailerFinder(cfg, SharedYoutubeChecker(cfg))
	})
	return trailerFinderInst
}

// NewTrailerFinder читает кэш с диска и запускает фонового писателя.
func NewTrailerFinder(cfg config.Config, yt *YoutubeChecker) *TrailerFinder {
	f := &TrailerFinder{
		yt:       yt,
		disabled: cfg.YouTube.DisableTrailerSearch,
		path:     filepath.Join(cfg.Compat.RepoRoot, "data", "trailers.json"),
		cache:    map[string]TrailerHit{},
		inflight: map[string]chan struct{}{},
		sem:      make(chan struct{}, trailerSearchSlots),
	}
	if yt != nil {
		f.searchFn = yt.ytSearchN
	}
	f.load()
	go f.writer()
	log.Info().Int("cached", len(f.cache)).Bool("search", !f.disabled).Str("path", f.path).
		Msg("trailers: finder ready")
	return f
}

func (f *TrailerFinder) load() {
	data, err := os.ReadFile(f.path)
	if err != nil {
		return
	}
	var m map[string]TrailerHit
	if stdjson.Unmarshal(data, &m) != nil {
		return
	}
	now := time.Now()
	for k, h := range m {
		if h.expired(now) {
			continue
		}
		f.cache[k] = h
	}
}

// writer сбрасывает кэш на диск раз в 20 с, если что-то менялось. Не на
// каждую запись: залп поисков по свежему списку дал бы десятки перезаписей
// файла подряд.
func (f *TrailerFinder) writer() {
	for range time.Tick(20 * time.Second) {
		f.mu.Lock()
		if !f.dirty {
			f.mu.Unlock()
			continue
		}
		f.dirty = false
		snap := make(map[string]TrailerHit, len(f.cache))
		for k, v := range f.cache {
			snap[k] = v
		}
		f.mu.Unlock()
		b, err := stdjson.Marshal(snap)
		if err != nil {
			continue
		}
		_ = os.MkdirAll(filepath.Dir(f.path), 0o755)
		tmp := f.path + ".tmp"
		if os.WriteFile(tmp, b, 0o644) == nil {
			_ = os.Rename(tmp, f.path)
		}
	}
}

func (h TrailerHit) expired(now time.Time) bool {
	ttl := trailerHitTTL
	switch {
	case h.Err:
		ttl = trailerErrTTL
	case h.Miss:
		ttl = trailerMissTTL
	}
	return now.Sub(h.At) > ttl
}

// Cached — что известно о тайтле. known=false → никто не искал (или запись
// протухла): вызывающий может Schedule. known=true с Miss — искали, пусто.
func (f *TrailerFinder) Cached(kind string, id int) (hit TrailerHit, known bool) {
	if f == nil {
		return TrailerHit{}, false
	}
	k := kind + ":" + strconv.Itoa(id)
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.cache[k]
	if !ok || h.expired(time.Now()) {
		return TrailerHit{}, false
	}
	return h, true
}

// Schedule запускает поиск в фоне. Идемпотентно: повторный вызов по тому же
// тайтлу, пока идёт поиск, ничего не плодит. Возвращает false, если поиск
// невозможен (выключен, нет yt-dlp) или очередь полна.
func (f *TrailerFinder) Schedule(q TrailerQuery) bool {
	if f == nil || f.disabled || f.searchFn == nil || q.ID <= 0 || (q.Title == "" && q.Original == "") {
		return false
	}
	k := q.key()
	f.mu.Lock()
	if _, busy := f.inflight[k]; busy {
		f.mu.Unlock()
		return true
	}
	if h, ok := f.cache[k]; ok && !h.expired(time.Now()) {
		f.mu.Unlock()
		return true
	}
	if f.pending >= trailerPendingMax {
		f.mu.Unlock()
		return false
	}
	done := make(chan struct{})
	f.inflight[k] = done
	f.pending++
	f.mu.Unlock()
	go func() {
		defer func() {
			f.mu.Lock()
			delete(f.inflight, k)
			f.pending--
			f.mu.Unlock()
			close(done)
		}()
		f.sem <- struct{}{}
		hit := f.search(q)
		<-f.sem
		f.store(k, hit)
	}()
	return true
}

// Find — синхронно: из кэша, иначе ищем (или ждём уже идущий поиск), но не
// дольше ctx. По таймауту возвращает known=false, поиск при этом
// продолжается в фоне и ляжет в кэш.
func (f *TrailerFinder) Find(ctx context.Context, q TrailerQuery) (TrailerHit, bool) {
	if h, ok := f.Cached(q.Kind, q.ID); ok {
		return h, true
	}
	if !f.Schedule(q) {
		return TrailerHit{}, false
	}
	f.mu.Lock()
	done := f.inflight[q.key()]
	f.mu.Unlock()
	if done == nil { // уже успел завершиться между Schedule и чтением
		return f.Cached(q.Kind, q.ID)
	}
	select {
	case <-done:
		return f.Cached(q.Kind, q.ID)
	case <-ctx.Done():
		return TrailerHit{}, false
	}
}

// Remember кладёт результат в кэш минуя поиск (админ-правка, тесты).
func (f *TrailerFinder) Remember(kind string, id int, hit TrailerHit) {
	if f == nil || id <= 0 {
		return
	}
	f.store(kind+":"+strconv.Itoa(id), hit)
}

func (f *TrailerFinder) store(k string, h TrailerHit) {
	h.At = time.Now()
	f.mu.Lock()
	f.cache[k] = h
	f.dirty = true
	f.mu.Unlock()
}

// search — до двух запросов (оригинал + локализованное название), кандидаты
// сливаются и ранжируются одним проходом.
func (f *TrailerFinder) search(q TrailerQuery) TrailerHit {
	seen := map[string]bool{}
	var pool []ytEntry
	failed := 0
	for _, s := range trailerQueries(q) {
		entries, err := f.searchFn(s, 6)
		if err != nil {
			failed++
			log.Debug().Err(err).Str("q", s).Msg("trailers: search failed")
			continue
		}
		for _, e := range entries {
			if e.ID == "" || seen[e.ID] {
				continue
			}
			seen[e.ID] = true
			pool = append(pool, e)
		}
	}
	if len(pool) == 0 && failed > 0 {
		return TrailerHit{Err: true, Source: "search"}
	}
	best, score := rankTrailer(pool, q)
	if score < trailerMinScore {
		log.Info().Str("kind", q.Kind).Int("id", q.ID).Str("title", q.Title).Int("candidates", len(pool)).
			Int("best_score", score).Msg("trailers: ничего похожего на трейлер не нашли")
		return TrailerHit{Miss: true, Source: "search"}
	}
	log.Info().Str("kind", q.Kind).Int("id", q.ID).Str("title", q.Title).Str("video", best.ID).
		Str("picked", best.Title).Int("score", score).Msg("trailers: нашли поиском")
	return TrailerHit{Key: best.ID, Title: best.Title, Lang: langOfTitle(best.Title), Source: "search"}
}

// trailerQueries — порядок важен: оригинальное название с «official trailer»
// даёт самый чистый рой (студийные каналы), русский запрос добирает дубляж.
func trailerQueries(q TrailerQuery) []string {
	year := ""
	if q.Year > 0 {
		year = " " + strconv.Itoa(q.Year)
	}
	var out []string
	orig := strings.TrimSpace(q.Original)
	title := strings.TrimSpace(q.Title)
	if orig != "" && !strings.EqualFold(orig, title) {
		out = append(out, orig+year+" official trailer")
	}
	if title != "" {
		out = append(out, title+year+" трейлер")
	} else if orig != "" {
		out = append(out, orig+year+" trailer")
	}
	return out
}

var (
	trailerWords = []string{"трейлер", "trailer", "тизер", "teaser"}
	// Что точно НЕ трейлер, даже если в названии есть слово «трейлер».
	trailerBad = []string{
		"обзор", "review", "reaction", "реакци", "full movie", "полный фильм", "весь фильм",
		"recap", "разбор", "объяснен", "explained", "ost", "soundtrack", "саундтрек",
		"сцена", "scene", "клип", "clip", "fan made", "fan-made", "фанат", "gameplay",
		"прохождение", "серия", "episode", "отрывок", "интервью", "interview",
		"behind the scenes", "за кадром", "#shorts", "shorts", "как снимали", "пародия", "parody",
	}
	yearRe = regexp.MustCompile(`\b(19|20)\d{2}\b`)
)

// rankTrailer выбирает кандидата, больше всего похожего на трейлер ЭТОГО
// тайтла. Чистая функция — таблица в тестах.
func rankTrailer(entries []ytEntry, q TrailerQuery) (ytEntry, int) {
	var best ytEntry
	bestScore := -1 << 30
	titleToks := trailerTokens(q.Title)
	origToks := trailerTokens(q.Original)
	for _, e := range entries {
		t := strings.ToLower(e.Title)
		score := 0
		for _, w := range trailerWords {
			if strings.Contains(t, w) {
				score += 4
				break
			}
		}
		if strings.Contains(t, "official") || strings.Contains(t, "официальн") {
			score++
		}
		if q.Year > 0 {
			ys := strconv.Itoa(q.Year)
			if strings.Contains(t, ys) {
				score += 2
			} else {
				for _, m := range yearRe.FindAllString(t, -1) {
					if y, _ := strconv.Atoi(m); y != 0 && (y < q.Year-1 || y > q.Year+1) {
						score -= 2
						break
					}
				}
			}
		}
		match := tokenOverlap(t, titleToks)
		if o := tokenOverlap(t, origToks); o > match {
			match = o
		}
		switch {
		case match >= 0.6:
			score += 3
		case match >= 0.3:
			score++
		case match == 0 && (len(titleToks) > 0 || len(origToks) > 0):
			score -= 2 // ни одного слова из названия — скорее всего чужой ролик
		}
		switch {
		case e.Duration > 0 && e.Duration < 20:
			score -= 3
		case e.Duration >= 20 && e.Duration <= 360:
			score++
		case e.Duration > 900:
			score -= 4
		}
		penalties := 0
		for _, w := range trailerBad {
			if strings.Contains(t, w) {
				penalties++
			}
		}
		if penalties > 2 {
			penalties = 2
		}
		score -= 3 * penalties
		if q.Kind == "movie" && (strings.Contains(t, "сезон") || strings.Contains(t, "season")) {
			score -= 2
		}
		if score > bestScore {
			best, bestScore = e, score
		}
	}
	return best, bestScore
}

// trailerTokens — слова названия длиной ≥3 в нижнем регистре.
func trailerTokens(s string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len([]rune(w)) >= 3 {
			out = append(out, w)
		}
	}
	return out
}

// tokenOverlap — доля слов названия, встретившихся в заголовке ролика.
func tokenOverlap(hay string, toks []string) float64 {
	if len(toks) == 0 {
		return 0
	}
	n := 0
	for _, w := range toks {
		if strings.Contains(hay, w) {
			n++
		}
	}
	return float64(n) / float64(len(toks))
}

func langOfTitle(s string) string {
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			return "ru"
		}
	}
	return "en"
}

// SortedKeys — для отладочной выдачи/тестов.
func (f *TrailerFinder) SortedKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	keys := make([]string, 0, len(f.cache))
	for k := range f.cache {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TrailerFindHandler — GET /lite/trailer/find?type=movie|tv&id=&title=&original=&year=
// → {"key":"…","title":"…","lang":"ru","source":"search"}; пусто → {"key":""}.
// Ждёт поиск не дольше 8 с: дольше веб держать кнопку «Трейлер» серой не
// станет, а результат всё равно ляжет в кэш к следующему открытию.
func TrailerFindHandler(f *TrailerFinder) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		g := r.URL.Query()
		kind := strings.ToLower(strings.TrimSpace(g.Get("type")))
		if kind == "series" || kind == "show" {
			kind = "tv"
		}
		id, _ := strconv.Atoi(strings.TrimSpace(g.Get("id")))
		if (kind != "movie" && kind != "tv") || id <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "type must be movie|tv and id > 0"})
			return
		}
		year, _ := strconv.Atoi(strings.TrimSpace(g.Get("year")))
		q := TrailerQuery{Kind: kind, ID: id, Title: strings.TrimSpace(g.Get("title")),
			Original: strings.TrimSpace(g.Get("original")), Year: year}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		hit, known := f.Find(ctx, q)
		if !known || !hit.Found() {
			w.Header().Set("Cache-Control", "private, max-age=300")
			writeJSON(w, http.StatusOK, map[string]any{"key": ""})
			return
		}
		w.Header().Set("Cache-Control", "private, max-age=3600")
		writeJSON(w, http.StatusOK, map[string]any{
			"key": hit.Key, "title": hit.Title, "lang": hit.Lang, "source": hit.Source,
		})
	}
}
