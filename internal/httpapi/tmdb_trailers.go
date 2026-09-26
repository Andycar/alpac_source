package httpapi

import (
	"context"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"lampac-go/internal/litesrc"
	"lampac-go/internal/tmdbcache"
)

// Трейлеры в ответах TMDB-прокси.
//
// Две правки, обе на стороне сервера, чтобы накрыть ВСЕ клиенты (Android, Lampa,
// tvOS, старые сборки) без единого релиза:
//  1. withVideoLanguage — TMDB отдаёт видео только на языке запроса, пока не
//     попросить include_video_language. Из-за этого карточка с language=ru-RU
//     видела 2 ролика вместо 214 и трейлер был у 31% тайтлов вместо 78%.
//  2. fillTrailer — когда в TMDB видео нет совсем (22% популярного), берём
//     найденное поиском по YouTube (litesrc.TrailerFinder) и кладём в
//     videos.results как обычную TMDB-запись. Клиент не отличит.

// tmdbVideoPath: «3/movie/123/videos», «3/tv/45/videos» — прямой запрос видео;
// «3/movie/123» / «3/tv/45» — карточка (видео приходят через append_to_response).
var tmdbVideoPath = regexp.MustCompile(`(?:^|/)(movie|tv)/(\d+)(/videos)?$`)

// parseTMDBVideoPath разбирает путь. direct=true — это сам /videos.
func parseTMDBVideoPath(path string) (kind string, id int, direct bool, ok bool) {
	m := tmdbVideoPath.FindStringSubmatch(strings.TrimSuffix(path, "/"))
	if m == nil {
		return "", 0, false, false
	}
	id, _ = strconv.Atoi(m[2])
	return m[1], id, m[3] != "", id > 0
}

// withVideoLanguage дописывает include_video_language там, где ответ несёт видео
// и клиент сам параметр не передал. Возвращает true, если дописали.
func withVideoLanguage(path string, q url.Values) bool {
	if q.Get("include_video_language") != "" {
		return false
	}
	_, _, direct, ok := parseTMDBVideoPath(path)
	if !ok {
		return false
	}
	if !direct && !strings.Contains(q.Get("append_to_response"), "videos") {
		return false
	}
	q.Set("include_video_language", "ru,en,null")
	return true
}

// hasYouTubeVideo — есть ли среди results рабочая запись YouTube.
func hasYouTubeVideo(results []any) bool {
	for _, it := range results {
		v, _ := it.(map[string]any)
		if v == nil {
			continue
		}
		site, _ := v["site"].(string)
		key, _ := v["key"].(string)
		if strings.EqualFold(site, "YouTube") && strings.TrimSpace(key) != "" {
			return true
		}
	}
	return false
}

// syntheticVideo — запись в форме TMDB /videos. type=Trailer, чтобы существующие
// пикеры («сначала Trailer, потом Teaser») брали её без правок.
func syntheticVideo(hit litesrc.TrailerHit) map[string]any {
	name := strings.TrimSpace(hit.Title)
	if name == "" {
		name = "Трейлер"
	}
	return map[string]any{
		"id":           "alpac-search-" + hit.Key,
		"iso_639_1":    hit.Lang,
		"iso_3166_1":   "",
		"key":          hit.Key,
		"name":         name,
		"official":     false,
		"published_at": hit.At.UTC().Format(time.RFC3339),
		"site":         "YouTube",
		"size":         1080,
		"type":         "Trailer",
	}
}

// yearOf — «2024-05-17» → 2024.
func yearOf(s string) int {
	if len(s) >= 4 {
		y, _ := strconv.Atoi(s[:4])
		return y
	}
	return 0
}

// queryFromDetail собирает TrailerQuery из объекта карточки TMDB.
func queryFromDetail(kind string, id int, d map[string]any) litesrc.TrailerQuery {
	str := func(k string) string { v, _ := d[k].(string); return strings.TrimSpace(v) }
	q := litesrc.TrailerQuery{Kind: kind, ID: id}
	if kind == "tv" {
		q.Title, q.Original, q.Year = str("name"), str("original_name"), yearOf(str("first_air_date"))
	} else {
		q.Title, q.Original, q.Year = str("title"), str("original_title"), yearOf(str("release_date"))
	}
	return q
}

// injectTrailer — чистая часть fillTrailer: разбирает тело, и если YouTube-видео
// нет, спрашивает finder. detail — как получить карточку для прямого /videos
// (там в теле только results, названия нет). Возвращает новое тело,
// «изменилось» и «не кэшировать» (поиск ещё идёт).
func injectTrailer(body []byte, path string, finder *litesrc.TrailerFinder,
	detail func(kind string, id int) map[string]any) (out []byte, changed, noCache bool) {
	if finder == nil || len(body) == 0 || body[0] != '{' {
		return body, false, false
	}
	kind, id, direct, ok := parseTMDBVideoPath(path)
	if !ok {
		return body, false, false
	}
	var root map[string]any
	if json.Unmarshal(body, &root) != nil {
		return body, false, false
	}
	var holder map[string]any // объект, у которого results
	if direct {
		holder = root
	} else {
		holder, _ = root["videos"].(map[string]any)
		if holder == nil {
			return body, false, false // карточка без append videos — не наше дело
		}
	}
	results, _ := holder["results"].([]any)
	if hasYouTubeVideo(results) {
		return body, false, false
	}
	hit, known := finder.Cached(kind, id)
	if known {
		if !hit.Found() {
			return body, false, false // искали, пусто — отдаём как есть и кэшируем
		}
		holder["results"] = append([]any{syntheticVideo(hit)}, results...)
		out, err := json.Marshal(root)
		if err != nil {
			return body, false, false
		}
		return out, true, false
	}
	// Неизвестно — ставим поиск в очередь. Ответ отдаём пустым, но НЕ кэшируем.
	var q litesrc.TrailerQuery
	if direct {
		if detail == nil {
			return body, false, false
		}
		d := detail(kind, id)
		if d == nil {
			return body, false, false
		}
		q = queryFromDetail(kind, id, d)
	} else {
		q = queryFromDetail(kind, id, root)
	}
	if finder.Schedule(q) {
		return body, false, true
	}
	return body, false, false
}

// fillTrailer применяет injectTrailer к свежему ответу апстрима.
func (h *tmdbProxy) fillTrailer(ctx context.Context, path string, fr *tmdbcache.FetchResult) {
	if h.finder == nil || fr == nil || len(fr.Body) == 0 {
		return
	}
	if fr.Status != 0 && (fr.Status < 200 || fr.Status >= 300) {
		return
	}
	if ct := fr.Headers["Content-Type"]; ct != "" && !strings.Contains(strings.ToLower(ct), "json") {
		return
	}
	detail := func(kind string, id int) map[string]any {
		// Прямой /videos не несёт названия — один поход за карточкой. Апстримов
		// не ждём дольше 5 с: это фоновая забота, а не ответ зрителю.
		dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		q := url.Values{"language": {"ru-RU"}}
		if h.apiKey != "" {
			q.Set("api_key", h.apiKey)
		}
		r, err := h.pool.FetchAPI(dctx, "3/"+kind+"/"+strconv.Itoa(id), q.Encode())
		if err != nil || r == nil || r.Status < 200 || r.Status >= 300 {
			return nil
		}
		var d map[string]any
		if json.Unmarshal(r.Body, &d) != nil {
			return nil
		}
		return d
	}
	out, changed, noCache := injectTrailer(fr.Body, path, h.finder, detail)
	if noCache {
		fr.NoCache = true
	}
	if !changed {
		return
	}
	fr.Body = out
	if fr.Headers != nil {
		delete(fr.Headers, "ETag")
		delete(fr.Headers, "Last-Modified")
		fr.Headers["Content-Length"] = strconv.Itoa(len(out))
	}
	log.Debug().Str("path", path).Msg("tmdb: трейлер из поиска подложен в videos")
}
