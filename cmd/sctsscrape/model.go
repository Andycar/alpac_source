package main

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Catalog — корневой объект scts.json.
type Catalog struct {
	Source     string  `json:"source"` // "online.scts.tv"
	UpdatedAt  string  `json:"updated_at"`
	LastID     int     `json:"last_id"`
	TotalFound int     `json:"total_found"`
	Movies     []Movie `json:"movies"`
}

// Movie — нормализованный фильм для community-JSON.
type Movie struct {
	MovieID     int      `json:"movie_id"`
	Name        string   `json:"name,omitempty"`               // русское название
	IntlName    string   `json:"international_name,omitempty"` // оригинал, если есть
	Year        int      `json:"year,omitempty"`
	KPID        string   `json:"kp_id,omitempty"`
	IMDBID      string   `json:"imdb_id,omitempty"`
	TMDBID      int      `json:"tmdb_id,omitempty"`
	Type        string   `json:"type,omitempty"`   // "movie" / "tv"
	Season      int      `json:"season,omitempty"` // для сериалов — индекс сезона (1..N)
	Genres      []string `json:"genres,omitempty"`
	Countries   []string `json:"countries,omitempty"`
	Directors   []string `json:"directors,omitempty"`
	Cast        []string `json:"cast,omitempty"`
	Description string   `json:"description,omitempty"`
	MPAA        string   `json:"mpaa,omitempty"`
	RatingIMDB  float64  `json:"rating_imdb,omitempty"`
	RatingKP    float64  `json:"rating_kp,omitempty"`
	Cover       string   `json:"cover,omitempty"`
	Slug        string   `json:"slug,omitempty"` // транслит-имя папки фильма
	Files       []File   `json:"files"`
	CreatedAt   string   `json:"created_at,omitempty"`
	UpdatedAt   string   `json:"updated_at,omitempty"`
	EnrichedAt  string   `json:"enriched_at,omitempty"` // timestamp когда обогащено через TMDB/KP
}

// File — отдельный медиа-файл (только реальные файлы, is_dir записи отбрасываются).
type File struct {
	FileID      int               `json:"file_id"`
	Name        string            `json:"name"`
	Size        int64             `json:"size,omitempty"`
	Active      bool              `json:"active"`
	Quality     string            `json:"quality,omitempty"`
	Resolution  string            `json:"resolution,omitempty"` // "1920x800"
	VideoInfo   string            `json:"video_info,omitempty"` // "1920x800, 23.976 fps, ffh264"
	DurationSec int               `json:"duration_sec,omitempty"`
	Format      string            `json:"format,omitempty"` // "mkv" / "mp4"
	Audio       []string          `json:"audio,omitempty"`
	Translation []string          `json:"translation,omitempty"`
	Streams     map[string]string `json:"streams,omitempty"` // {"720p": URL, ...}
	Download    string            `json:"download,omitempty"`
	DCPP        string            `json:"dcpp,omitempty"`
}

// parseMoviesFromBytes принимает сырое тело ответа api.php и возвращает
// нормализованные фильмы. Поддерживает три формы:
//   - массив фильмов        [{...}, {...}]
//   - одиночный фильм       {id: ..., name: ..., files: [...]}
//   - JsHttpRequest envelope {"result": ..., "id": "<timestamp>", "js": ...}
//
// КЛЮЧЕВОЕ: envelope JsHttpRequest имеет поля `id` (timestamp request ID),
// `result`, `js`. Если result=null — это означает «нет данных», и НЕЛЬЗЯ
// брать envelope.id как movie_id (это распространённая ошибка).
func parseMoviesFromBytes(raw []byte) ([]Movie, bool, error) {
	raw = stripJHRWrapper(raw)
	if len(raw) == 0 {
		return nil, true, nil
	}

	// Распознаём JsHttpRequest envelope: объект с полем "js" или одновременно
	// с "id" + "result". Если нашли — работаем только с result.
	if env, ok := parseObject(raw); ok && isJHREnvelope(env) {
		r, hasResult := env["result"]
		if !hasResult || isJSONNullish(r) {
			return nil, true, nil
		}
		raw = r
	}

	// Глубже: {"movie": {...}} или {"movies": [...]}.
	if inner, ok := parseObject(raw); ok {
		if r, found := inner["movie"]; found && !isJSONNullish(r) {
			raw = r
		} else if r, found := inner["movies"]; found && !isJSONNullish(r) {
			raw = r
		}
	}

	trim := strings.TrimSpace(string(raw))
	if trim == "" || trim == "null" || trim == "[]" || trim == "{}" {
		return nil, true, nil
	}

	if strings.HasPrefix(trim, "[") {
		var arr []map[string]any
		if err := json.Unmarshal(raw, &arr); err != nil {
			return nil, false, err
		}
		out := make([]Movie, 0, len(arr))
		for _, obj := range arr {
			if m := normalizeMovie(obj); m != nil && m.MovieID > 0 {
				out = append(out, *m)
			}
		}
		return out, len(out) == 0, nil
	}

	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, false, err
	}
	m := normalizeMovie(obj)
	if m == nil || m.MovieID == 0 {
		return nil, true, nil
	}
	return []Movie{*m}, false, nil
}

func parseObject(raw []byte) (map[string]json.RawMessage, bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, false
	}
	return m, true
}

func isJSONNullish(r json.RawMessage) bool {
	s := strings.TrimSpace(string(r))
	return s == "" || s == "null" || s == `""` || s == "[]" || s == "{}"
}

// isJHREnvelope распознаёт обёртку JsHttpRequest:
//   - есть поле "js" — характерный признак JsHttpRequest 2010 года
//   - есть поле "result" вместе с "id" (id здесь — request timestamp, не movie_id)
// При этом отсутствуют признаки самого movie-объекта (files / name / movie_id).
func isJHREnvelope(env map[string]json.RawMessage) bool {
	_, hasJs := env["js"]
	_, hasResult := env["result"]
	_, hasFiles := env["files"]
	_, hasMovieID := env["movie_id"]
	if hasFiles || hasMovieID {
		return false
	}
	if hasJs {
		return true
	}
	if hasResult {
		return true
	}
	return false
}

// CleanCatalog убирает мусорные записи: без files (или с пустыми/null files)
// и с подозрительно большими movie_id (request-timestamps типа 17788532771876201).
// Возвращает (removed, kept).
func (c *Catalog) CleanCatalog() (removed, kept int) {
	const maxRealisticID = 10_000_000 // SCTS-каталог ~50-200k, любой ID выше — мусор
	filtered := c.Movies[:0]
	for _, m := range c.Movies {
		// фейковые: огромный ID (JsHttpRequest timestamp) либо ни files, ни name
		if m.MovieID > maxRealisticID {
			removed++
			continue
		}
		if len(m.Files) == 0 && m.Name == "" {
			removed++
			continue
		}
		filtered = append(filtered, m)
		kept++
	}
	c.Movies = filtered
	c.TotalFound = kept
	return
}

var (
	reKP   = regexp.MustCompile(`kinopoisk\.ru/(?:film|series)/(\d+)`)
	reIMDB = regexp.MustCompile(`imdb\.com/title/(tt\d+)`)
)

// normalizeMovie разбирает один объект-фильм (как-есть из API) в наш Movie.
// Толерантный парсер: всё в string-числах, неизвестные поля игнорируются.
// Из иерархии files[] (где первый is_dir=1 — родительская папка, остальные —
// реальные файлы) извлекает только реальные файлы и забирает slug из папки.
func normalizeMovie(obj map[string]any) *Movie {
	if len(obj) == 0 {
		return nil
	}

	// Защита от envelope-объекта: если есть поле `js` или `result` — это не фильм.
	if _, hasJs := obj["js"]; hasJs {
		return nil
	}
	if _, hasResult := obj["result"]; hasResult {
		return nil
	}

	// id может быть `id` (lite-метод getList/getCatalog) или `movie_id` (getMovie).
	id := toInt(obj["id"])
	if id == 0 {
		id = toInt(obj["movie_id"])
	}
	if id == 0 {
		return nil
	}
	// Заведомо мусорный ID — JsHttpRequest request timestamp (16-17 цифр).
	if id > 10_000_000 {
		return nil
	}

	// Объект без признаков фильма — пропускаем (защита от частичных envelope-объектов).
	if obj["name"] == nil && obj["files"] == nil && obj["movie_id"] == nil {
		return nil
	}

	m := &Movie{
		MovieID:     id,
		Name:        toStr(obj["name"]),
		IntlName:    toStr(obj["international_name"]),
		Year:        toInt(obj["year"]),
		Description: stripHTML(toStr(obj["description"])),
		MPAA:        toStr(obj["mpaa"]),
		RatingIMDB:  toFloat(obj["rating_imdb_value"]),
		RatingKP:    toFloat(obj["rating_kinopoisk_value"]),
		Genres:      toStrSlice(obj["genres"]),
		Countries:   toStrSlice(obj["countries"]),
		Directors:   toStrSlice(obj["directors"]),
		Cast:        toStrSlice(obj["cast"]),
		CreatedAt:   toStr(obj["created_at"]),
		UpdatedAt:   toStr(obj["updated_at"]),
	}

	if s := toStr(obj["kinopoisk_url"]); s != "" {
		if mm := reKP.FindStringSubmatch(s); len(mm) == 2 {
			m.KPID = mm[1]
		}
	}
	if s := toStr(obj["imdb_url"]); s != "" {
		if mm := reIMDB.FindStringSubmatch(s); len(mm) == 2 {
			m.IMDBID = mm[1]
		}
	}

	// cover может быть строкой или {original, thumbnail}.
	if v, ok := obj["cover"]; ok {
		switch t := v.(type) {
		case string:
			m.Cover = t
		case map[string]any:
			if s := toStr(t["original"]); s != "" {
				m.Cover = s
			} else if s := toStr(t["thumbnail"]); s != "" {
				m.Cover = s
			}
		}
	}

	if filesAny, ok := obj["files"]; ok {
		if arr, _ := filesAny.([]any); arr != nil {
			for _, it := range arr {
				fm, _ := it.(map[string]any)
				if fm == nil {
					continue
				}
				if asBool(fm["is_dir"]) {
					if m.Slug == "" {
						m.Slug = toStr(fm["name"])
					}
					continue
				}
				f := normalizeFile(fm)
				if f != nil {
					m.Files = append(m.Files, *f)
				}
			}
		}
	}

	return m
}

func normalizeFile(fm map[string]any) *File {
	f := &File{
		FileID:      toInt(fm["file_id"]),
		Name:        toStr(fm["name"]),
		Size:        toInt64(fm["size"]),
		Active:      asBool(fm["active"]),
		Quality:     toStr(fm["quality"]),
		Translation: toStrSlice(fm["translation"]),
	}
	if f.FileID == 0 && f.Name == "" {
		return nil
	}

	// metainfo: либо объект, либо пустая строка.
	if meta, _ := fm["metainfo"].(map[string]any); meta != nil {
		if vid, _ := meta["video"].(map[string]any); vid != nil {
			f.Resolution = toStr(vid["label"])
			f.VideoInfo = toStr(vid["info"])
		}
		if audArr, _ := meta["audio"].([]any); audArr != nil {
			for _, a := range audArr {
				if am, _ := a.(map[string]any); am != nil {
					if info := toStr(am["info"]); info != "" {
						f.Audio = append(f.Audio, info)
					}
				}
			}
		}
		f.DurationSec = toInt(meta["playtime_seconds"])
		f.Format = toStr(meta["format"])
	}

	if links, _ := fm["links"].(map[string]any); links != nil {
		f.Download = toStr(links["download"])
		f.DCPP = toStr(links["dcpp"])
		// streams: объект {720p: URL} или пустой массив [].
		if streams, _ := links["streams"].(map[string]any); streams != nil {
			f.Streams = map[string]string{}
			for k, v := range streams {
				if s := toStr(v); s != "" {
					f.Streams[k] = s
				}
			}
			if len(f.Streams) == 0 {
				f.Streams = nil
			}
		}
	}

	return f
}

func toStr(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	case nil:
		return ""
	default:
		return ""
	}
}

func toInt(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(t))
		return n
	}
	return 0
}

func toInt64(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		return n
	}
	return 0
}

func toFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f
	}
	return 0
}

func toStrSlice(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, it := range t {
			if s := toStr(it); s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return nil
		}
		return []string{s}
	}
	return nil
}

func asBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t != 0
	case string:
		s := strings.ToLower(strings.TrimSpace(t))
		return s == "1" || s == "true" || s == "yes"
	}
	return false
}

var reTags = regexp.MustCompile(`<[^>]+>`)

func stripHTML(s string) string {
	if s == "" {
		return s
	}
	s = reTags.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	return strings.TrimSpace(s)
}

func nowISO() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// SortByID — стабильный порядок в выходном JSON.
func (c *Catalog) SortByID() {
	sort.Slice(c.Movies, func(i, j int) bool {
		return c.Movies[i].MovieID < c.Movies[j].MovieID
	})
}

// MergeMovies добавляет фильмы, обновляя записи с тем же ID.
func (c *Catalog) MergeMovies(movies []Movie) (added, updated int) {
	idx := make(map[int]int, len(c.Movies))
	for i, m := range c.Movies {
		idx[m.MovieID] = i
	}
	for _, m := range movies {
		if i, ok := idx[m.MovieID]; ok {
			c.Movies[i] = m
			updated++
			continue
		}
		c.Movies = append(c.Movies, m)
		idx[m.MovieID] = len(c.Movies) - 1
		added++
	}
	c.TotalFound = len(c.Movies)
	return
}
