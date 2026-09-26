package httpapi

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"lampac-go/internal/tmdbcache"
)

// Нормализация карточек TMDB прямо на прокси — ОДНА точка на web, Android и tvOS (все три клиента
// ходят в TMDB через /tmdb/api/, своей логики названий у них нет).
//
// Лечим две вещи, которые видно на любом «браузерном» ряду (репорт 22.08 со скрином ряда Амедиатеки):
//
//  1. НЕЧИТАЕМЫЕ НАЗВАНИЯ. На language=ru-RU TMDB отдаёт название в оригинальном алфавите, если
//     русского перевода нет: арабский, китайский, корейский, иврит, тайский… Пользователь такую
//     карточку не прочтёт вообще. Берём английское название (отдельный запрос language=en-US,
//     результат кэшируем), и только если и оно нечитаемо — оставляем как есть.
//     Плюс чиним классическую «кракозябру» («Ð¡Ð»Ð¾Ð²Ð¾» — UTF-8, прочитанный как latin1/CP1252):
//     такие строки приходят из кривых зеркал TMDB-прокси.
//
//  2. МУСОР В BROWSE-ВЫДАЧАХ. Карточка без постера — серая плашка, которая ничего не сообщает; в
//     каталоге провайдера (у Амедиатеки нет своей «сети», только provider 116) их половина ряда.
//     Выкидываем их из discover / similar / recommendations. В ПОИСКЕ не трогаем: там пользователь
//     ищет конкретный тайтл, и карточка без постера — валидный ответ.
//
// Тела ответов кэшируются уже нормализованными (Put вызывается после), поэтому цена — один проход
// на промах кэша.

const (
	enTitleTTL      = 7 * 24 * time.Hour // английские названия не меняются
	enTitleCacheCap = 5000
	enLookupBudget  = 16              // максимум догрузок на один ответ (идут параллельно, ~один round-trip)
	enLookupTimeout = 3 * time.Second // общий бюджет догрузок на один ответ
)

type enTitleEntry struct {
	title string
	exp   time.Time
}

var (
	enTitleMu    sync.RWMutex
	enTitleCache = map[string]enTitleEntry{}
)

func enTitleGet(key string) (string, bool) {
	enTitleMu.RLock()
	e, ok := enTitleCache[key]
	enTitleMu.RUnlock()
	if !ok || time.Now().After(e.exp) {
		return "", false
	}
	return e.title, true
}

func enTitlePut(key, title string) {
	enTitleMu.Lock()
	defer enTitleMu.Unlock()
	// Кэш ограничен: при переполнении чистим протухшее, и только если не помогло — сбрасываем всё
	// (простая стратегия: записей мало, они дешёвые, а сложное вытеснение тут не окупается).
	if len(enTitleCache) >= enTitleCacheCap {
		now := time.Now()
		for k, v := range enTitleCache {
			if now.After(v.exp) {
				delete(enTitleCache, k)
			}
		}
		if len(enTitleCache) >= enTitleCacheCap {
			enTitleCache = make(map[string]enTitleEntry, enTitleCacheCap)
		}
	}
	enTitleCache[key] = enTitleEntry{title: title, exp: time.Now().Add(enTitleTTL)}
}

// titleScriptCounts считает буквы «читаемых» (латиница + кириллица) и прочих алфавитов.
func titleScriptCounts(s string) (readable, foreign int) {
	for _, r := range s {
		if !unicode.IsLetter(r) {
			continue
		}
		if unicode.Is(unicode.Latin, r) || unicode.Is(unicode.Cyrillic, r) {
			readable++
		} else {
			foreign++
		}
	}
	return readable, foreign
}

// unreadableTitle — название набрано алфавитом, которого наш зритель не читает.
// Смешанные названия («Тайна 東京», «Ne Zha 哪吒») не трогаем: латиница/кириллица в них преобладает;
// при ничьей («S&X 東京») считаем нечитаемым — половину строки всё равно не разобрать.
func unreadableTitle(s string) bool {
	readable, foreign := titleScriptCounts(s)
	return foreign > 0 && foreign >= readable
}

// repairMojibake чинит UTF-8, прочитанный как однобайтовую кодировку («Ð¡Ð»Ð¾Ð²Ð¾» → «Слово»).
// Возвращает исходную строку, если это не mojibake или «починка» сделала хуже.
func repairMojibake(s string) string {
	if !strings.ContainsAny(s, "ÐÑÃÂ") {
		return s
	}
	b := make([]byte, 0, len(s))
	for _, r := range s {
		if r > 0xFF { // настоящий не-латинский символ → строка не mojibake
			return s
		}
		b = append(b, byte(r))
	}
	if !utf8.Valid(b) {
		return s
	}
	out := string(b)
	// Принимаем починку, только если мусора из Latin-1 Supplement (0x80–0xFF) стало меньше:
	// у кракозябры он в каждом символе, у нормальной строки с диакритикой — один-два знака.
	// Считать «читаемые буквы» тут нельзя: «Ð» и «Ñ» сами по себе ЛАТИНСКИЕ буквы.
	if latin1Junk(out) < latin1Junk(s) {
		return out
	}
	return s
}

// latin1Junk считает символы диапазона Latin-1 Supplement — маркер кракозябры.
func latin1Junk(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x80 && r <= 0xFF {
			n++
		}
	}
	return n
}

// tmdbListPath — ответ содержит массив карточек, которые стоит нормализовать.
func tmdbListPath(path string) bool {
	p := strings.ToLower(path)
	return strings.Contains(p, "/discover/") || strings.HasPrefix(p, "discover/") ||
		strings.Contains(p, "/search/") || strings.HasPrefix(p, "search/") ||
		strings.Contains(p, "/trending/") || strings.HasPrefix(p, "trending/") ||
		strings.Contains(p, "/similar") || strings.Contains(p, "/recommendations") ||
		strings.Contains(p, "/popular") || strings.Contains(p, "/top_rated") ||
		strings.Contains(p, "/now_playing") || strings.Contains(p, "/upcoming") ||
		strings.Contains(p, "/on_the_air") || strings.Contains(p, "/airing_today") ||
		strings.Contains(p, "_credits") || strings.Contains(p, "/credits")
}

// tmdbBrowsePath — «витрина», где карточка без постера бесполезна. Поиск сюда НЕ входит.
func tmdbBrowsePath(path string) bool {
	p := strings.ToLower(path)
	return strings.Contains(p, "discover/") || strings.Contains(p, "/similar") ||
		strings.Contains(p, "/recommendations")
}

// mediaKindOf определяет тип карточки для запроса английского названия.
func mediaKindOf(item map[string]any, path string) string {
	if mt, ok := item["media_type"].(string); ok {
		switch mt {
		case "tv", "movie":
			return mt
		default:
			return "" // person и прочее — названий не нормализуем
		}
	}
	if _, ok := item["first_air_date"]; ok {
		return "tv"
	}
	if _, ok := item["release_date"]; ok {
		return "movie"
	}
	if strings.Contains(path, "/tv") || strings.Contains(path, "tv/") {
		return "tv"
	}
	if strings.Contains(path, "/movie") || strings.Contains(path, "movie/") {
		return "movie"
	}
	return ""
}

func itemID(item map[string]any) int {
	// jsoniter (пакетный `json`) кладёт числа в any как float64
	switch v := item["id"].(type) {
	case float64:
		return int(v)
	case int64:
		return int(v)
	case int:
		return v
	}
	return 0
}

// mediaRef — ссылка на карточку, которой нужно английское название.
type mediaRef struct {
	Kind string // "tv" | "movie"
	ID   int
}

func (r mediaRef) key() string { return r.Kind + ":" + strconv.Itoa(r.ID) }

// pendingTitle — карточка, которую не удалось починить без сети.
type pendingTitle struct {
	item  map[string]any
	field string
	ref   mediaRef
}

// normalizeItemLocal чинит название БЕЗ сети: кракозябра + фолбэк на original_*.
// Если название так и осталось нечитаемым — возвращает ссылку для батч-догрузки английского.
func normalizeItemLocal(item map[string]any, path string) (changed bool, pending []pendingTitle) {
	for _, field := range []string{"title", "name"} {
		raw, ok := item[field].(string)
		if !ok || strings.TrimSpace(raw) == "" {
			continue
		}
		fixed := repairMojibake(raw)
		if fixed != raw {
			item[field] = fixed
			changed = true
		}
		if !unreadableTitle(fixed) {
			continue
		}
		// Английское название нередко уже лежит в original_* (корейские/японские тайтлы).
		if orig := latinOriginal(item, field); orig != "" {
			item[field] = orig
			changed = true
			continue
		}
		if kind := mediaKindOf(item, path); kind != "" {
			if id := itemID(item); id > 0 {
				pending = append(pending, pendingTitle{item: item, field: field, ref: mediaRef{Kind: kind, ID: id}})
			}
		}
	}
	return changed, pending
}

// latinOriginal возвращает original_title/original_name, если оно читаемо.
func latinOriginal(item map[string]any, field string) string {
	for _, key := range []string{"original_" + field, "original_title", "original_name"} {
		if v, ok := item[key].(string); ok && strings.TrimSpace(v) != "" && !unreadableTitle(v) {
			return v
		}
	}
	return ""
}

// stillUnreadable — у карточки не осталось ни одного читаемого названия.
func stillUnreadable(item map[string]any) bool {
	seen := false
	for _, field := range []string{"title", "name"} {
		if v, ok := item[field].(string); ok && strings.TrimSpace(v) != "" {
			seen = true
			if !unreadableTitle(v) {
				return false
			}
		}
	}
	return seen
}

func hasPoster(item map[string]any) bool {
	switch v := item["poster_path"].(type) {
	case string:
		return strings.TrimSpace(v) != ""
	}
	// у карточек-персон постера нет по определению — их не фильтруем
	if _, isPerson := item["profile_path"]; isPerson {
		return true
	}
	if mt, _ := item["media_type"].(string); mt == "person" {
		return true
	}
	return false
}

// normalizeTMDBBody — чистый (тестируемый) проход по телу ответа TMDB.
// lookup получает СРАЗУ ВСЕ карточки, которым нужно английское название (одним заходом,
// параллельно) — иначе холодный список ждал бы N последовательных round-trip'ов.
// Возвращает новое тело и признак «изменилось».
func normalizeTMDBBody(body []byte, path string, lookup func([]mediaRef) map[string]string) ([]byte, bool) {
	if len(body) == 0 || body[0] != '{' {
		return body, false
	}
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return body, false
	}
	changed := false
	dropJunk := tmdbBrowsePath(path)
	var pending []pendingTitle

	// Списки: results / cast / crew (фильмографии).
	for _, key := range []string{"results", "cast", "crew"} {
		arr, ok := root[key].([]any)
		if !ok {
			continue
		}
		kept := make([]any, 0, len(arr))
		for _, raw := range arr {
			item, ok := raw.(map[string]any)
			if !ok {
				kept = append(kept, raw)
				continue
			}
			if dropJunk && !hasPoster(item) {
				changed = true
				continue // серая плашка без постера — в витрине это шум
			}
			ch, pend := normalizeItemLocal(item, path)
			changed = changed || ch
			pending = append(pending, pend...)
			kept = append(kept, item)
		}
		if len(kept) != len(arr) {
			root[key] = kept
		}
	}

	// Карточка тайтла (detail): те же title/name на верхнем уровне.
	if _, isList := root["results"]; !isList {
		ch, pend := normalizeItemLocal(root, path)
		changed = changed || ch
		pending = append(pending, pend...)
	}

	// Один батч догрузки английских названий на весь ответ.
	if len(pending) > 0 && lookup != nil {
		refs := make([]mediaRef, 0, len(pending))
		seen := make(map[string]bool, len(pending))
		for _, p := range pending {
			if k := p.ref.key(); !seen[k] {
				seen[k] = true
				refs = append(refs, p.ref)
			}
		}
		titles := lookup(refs)
		for _, p := range pending {
			if en, ok := titles[p.ref.key()]; ok && en != "" && !unreadableTitle(en) {
				p.item[p.field] = en
				changed = true
			}
		}
	}

	// Витрина: карточка, которую не удалось сделать читаемой НИ на русском, НИ на английском
	// (у TMDB нет перевода вовсе — например китайские детские каналы), для нашего зрителя
	// бесполезна ровно так же, как карточка без постера. В поиске такие остаются.
	if dropJunk {
		for _, key := range []string{"results", "cast", "crew"} {
			arr, ok := root[key].([]any)
			if !ok {
				continue
			}
			kept := make([]any, 0, len(arr))
			for _, raw := range arr {
				item, ok := raw.(map[string]any)
				if !ok {
					kept = append(kept, raw)
					continue
				}
				if stillUnreadable(item) {
					changed = true
					continue
				}
				kept = append(kept, item)
			}
			if len(kept) != len(arr) {
				root[key] = kept
			}
		}
	}

	if !changed {
		return body, false
	}
	out, err := json.Marshal(root)
	if err != nil {
		return body, false
	}
	return out, true
}

// enTitleLookup строит батч-догрузку английских названий: кэш → параллельные запросы
// (не больше enLookupBudget штук) с общим таймаутом. Один round-trip на весь список.
func (h *tmdbProxy) enTitleLookup(ctx context.Context) func([]mediaRef) map[string]string {
	return func(refs []mediaRef) map[string]string {
		out := make(map[string]string, len(refs))
		var miss []mediaRef
		for _, r := range refs {
			if t, ok := enTitleGet(r.key()); ok {
				out[r.key()] = t
			} else {
				miss = append(miss, r)
			}
		}
		if len(miss) == 0 || ctx.Err() != nil {
			return out
		}
		if len(miss) > enLookupBudget {
			miss = miss[:enLookupBudget] // остальные останутся как есть — лучше, чем ждать сеть
		}
		apiKey := h.apiKey
		if apiKey == "" {
			apiKey = h.pool.APIKey()
		}
		if apiKey == "" {
			return out
		}

		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, r := range miss {
			wg.Add(1)
			go func(r mediaRef) {
				defer wg.Done()
				fr, err := h.pool.FetchAPI(ctx, "3/"+r.Kind+"/"+strconv.Itoa(r.ID), "api_key="+apiKey+"&language=en-US")
				if err != nil || fr == nil || len(fr.Body) == 0 {
					return
				}
				var card map[string]any
				if json.Unmarshal(fr.Body, &card) != nil {
					return
				}
				title, _ := card["title"].(string)
				if title == "" {
					title, _ = card["name"].(string)
				}
				title = repairMojibake(strings.TrimSpace(title))
				if title == "" {
					return
				}
				enTitlePut(r.key(), title)
				mu.Lock()
				out[r.key()] = title
				mu.Unlock()
			}(r)
		}
		wg.Wait()
		return out
	}
}

// normalizeFetch применяет нормализацию к свежему ответу апстрима (до записи в кэш).
// Меняем тело — снимаем ETag/Last-Modified: они относятся к оригиналу.
func (h *tmdbProxy) normalizeFetch(ctx context.Context, path string, fr *tmdbcache.FetchResult) {
	if fr == nil || len(fr.Body) == 0 {
		return
	}
	if fr.Status != 0 && (fr.Status < 200 || fr.Status >= 300) {
		return
	}
	if ct := fr.Headers["Content-Type"]; ct != "" && !strings.Contains(strings.ToLower(ct), "json") {
		return
	}
	if !tmdbListPath(path) && !strings.Contains(path, "/movie/") && !strings.Contains(path, "/tv/") {
		return
	}
	lctx, cancel := context.WithTimeout(ctx, enLookupTimeout)
	defer cancel()
	out, changed := normalizeTMDBBody(fr.Body, path, h.enTitleLookup(lctx))
	if !changed {
		return
	}
	fr.Body = out
	if fr.Headers != nil {
		delete(fr.Headers, "ETag")
		delete(fr.Headers, "Last-Modified")
	}
}
