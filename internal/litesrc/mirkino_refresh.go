package litesrc

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"lampac-go/internal/proxylink"
)

// Переминт протухших ссылок «Мира кино».
//
// Источник — Jellyfin, и он выдаёт mediaSourceId ОДНОРАЗОВО: каждый вызов
// PlaybackInfo возвращает новые идентификаторы, а прежние умирают примерно
// через минуту (замер 16.09.2026 на проде: 307 сразу, 307 на 30-й секунде,
// 401 на 60-й и дальше). Мы прячем такой билет в /proxy-токен со сроком 36
// часов, поэтому фильм играл ровно до первой перемотки или паузы — дальше
// 401, и за три часа набегало под тысячу отказов.
//
// Лечение: по 401 прокси зовёт сюда, мы берём у источника свежий билет на
// ТУ ЖЕ дорожку и отдаём новый адрес. Повтор запроса зритель не замечает.
//
// Почему «ту же» — нетривиально: PlaybackInfo отдаёт несколько MediaSources
// (2160p и 1080p — это разные файлы), и взять первый попавшийся значило бы
// посреди фильма перепрыгнуть на другое качество, то есть на другой файл с
// другой длиной. Поэтому при выпуске ссылки мы подмешиваем в неё стабильные
// приметы дорожки — точный размер файла и имя, — а при переминте ищем по
// ним. Размер первичен: имена у источника повторяются («1080p» у двух
// озвучек), а байты уникальны. Лишние параметры Jellyfin игнорирует
// (проверено: с ними ответ тот же 307).

const (
	// mirkinoSizeParam / mirkinoNameParam — наши приметы дорожки внутри
	// ссылки. Префикс x_ — чтобы не столкнуться с параметрами Jellyfin.
	mirkinoSizeParam = "x_ms_size"
	mirkinoNameParam = "x_ms_name"

	// mirkinoRefreshCollapse — окно схлопывания одновременных переминтов по
	// одному фильму. Популярную новинку смотрят десятки человек, и их 401
	// приходят пачкой: без окна мы устроили бы источнику залп PlaybackInfo.
	// Держим маленьким (билет живёт ~60 с), чтобы никто не получил уже
	// наполовину истёкший билет.
	mirkinoRefreshCollapse = 5 * time.Second
)

// mirkinoStreamRefresher — реализация proxylink.TargetRefresher, привязанная
// к конкретному экземпляру источника (у него токен и HTTP-клиент).
type mirkinoStreamRefresher struct {
	m *mirkinoChecker

	mu    sync.Mutex
	cache map[string]mirkinoRefreshEntry // itemID → недавний ответ PlaybackInfo
}

type mirkinoRefreshEntry struct {
	sources []jfMediaSource
	at      time.Time
}

// registerRefresher подключает переминт для плагина mirkino.
func (m *mirkinoChecker) registerRefresher() {
	r := &mirkinoStreamRefresher{m: m, cache: map[string]mirkinoRefreshEntry{}}
	proxylink.RegisterTargetRefresher("mirkino", r.refresh)
}

// mirkinoDecorateStreamURL добавляет к ссылке приметы дорожки, по которым её
// потом можно будет переминтить. Зовётся в момент выпуска ссылки.
func mirkinoDecorateStreamURL(raw string, src jfMediaSource) string {
	if raw == "" {
		return raw
	}
	sep := "&"
	if !strings.Contains(raw, "?") {
		sep = "?"
	}
	out := raw
	if src.Size > 0 {
		out += sep + mirkinoSizeParam + "=" + strconv.FormatInt(src.Size, 10)
		sep = "&"
	}
	if n := strings.TrimSpace(src.Name); n != "" {
		out += sep + mirkinoNameParam + "=" + url.QueryEscape(n)
	}
	return out
}

// refresh: протухший адрес → свежий. Возвращает ok=false, если адрес не наш,
// источник не ответил или подходящей дорожки не нашлось — прокси тогда
// отдаст зрителю исходный отказ, а не подсунет чужой файл.
func (r *mirkinoStreamRefresher) refresh(ctx context.Context, stale string) (string, bool) {
	u, err := url.Parse(stale)
	if err != nil {
		return "", false
	}
	itemID := mirkinoItemIDFromPath(u.Path)
	if itemID == "" {
		return "", false
	}
	q := u.Query()
	wantSize, _ := strconv.ParseInt(q.Get(mirkinoSizeParam), 10, 64)
	wantName := q.Get(mirkinoNameParam)
	oldID := q.Get("mediaSourceId")

	sources := r.sourcesFor(ctx, itemID)
	if len(sources) == 0 {
		log.Debug().Str("item", itemID).Msg("mirkino: переминт — источник не отдал дорожек")
		return "", false
	}
	src, ok := mirkinoPickSource(sources, wantSize, wantName, len(q.Get(mirkinoSizeParam)) == 0)
	if !ok {
		log.Warn().Str("item", itemID).Int64("want_size", wantSize).Str("want_name", wantName).
			Int("got", len(sources)).
			Msg("mirkino: переминт — не нашёл ту же дорожку, отдаём отказ как есть")
		return "", false
	}
	if src.ID == "" || src.ID == oldID {
		return "", false
	}

	token, _, authOK := r.m.ensureAuth(ctx, false)
	if !authOK || token == "" {
		return "", false
	}
	fresh := fmt.Sprintf("%s/videos/%s/stream?static=true&mediaSourceId=%s&api_key=%s",
		r.m.host, url.PathEscape(itemID), url.QueryEscape(src.ID), url.QueryEscape(token))
	fresh = mirkinoDecorateStreamURL(fresh, src)
	// Прочие параметры исходной ссылки (например edge_skip, который дописывает
	// раздача через ноды) переносим как есть — они нужны нашему же прокси.
	for k, vs := range q {
		switch k {
		case "static", "mediaSourceId", "api_key", mirkinoSizeParam, mirkinoNameParam:
			continue
		}
		for _, v := range vs {
			fresh += "&" + url.QueryEscape(k) + "=" + url.QueryEscape(v)
		}
	}
	return fresh, true
}

// sourcesFor — PlaybackInfo с коротким окном схлопывания, чтобы пачка
// одновременных 401 по одному фильму стоила источнику один вызов.
func (r *mirkinoStreamRefresher) sourcesFor(ctx context.Context, itemID string) []jfMediaSource {
	r.mu.Lock()
	if e, ok := r.cache[itemID]; ok && time.Since(e.at) < mirkinoRefreshCollapse {
		r.mu.Unlock()
		return e.sources
	}
	r.mu.Unlock()

	sources := r.m.playbackSources(ctx, itemID)
	if len(sources) == 0 {
		return nil
	}
	r.mu.Lock()
	// Кэш маленький и самоочищающийся: чистим протухшее, когда разрослось.
	if len(r.cache) > 512 {
		for k, e := range r.cache {
			if time.Since(e.at) >= mirkinoRefreshCollapse {
				delete(r.cache, k)
			}
		}
	}
	r.cache[itemID] = mirkinoRefreshEntry{sources: sources, at: time.Now()}
	r.mu.Unlock()
	return sources
}

// mirkinoItemIDFromPath вытаскивает id фильма из «/videos/{id}/stream».
func mirkinoItemIDFromPath(p string) string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	for i, seg := range parts {
		if strings.EqualFold(seg, "videos") && i+1 < len(parts) {
			id, err := url.PathUnescape(parts[i+1])
			if err != nil {
				return parts[i+1]
			}
			return id
		}
	}
	return ""
}

// mirkinoPickSource ищет ту же дорожку, что была в протухшей ссылке.
//
// Порядок: точный размер файла (уникален), затем имя качества, затем — только
// если примет в ссылке не было вовсе (старые ссылки, выпущенные до этой
// правки) — первая дорожка. Последнее допущение осознанное: у старой ссылки
// выбора нет, а «первая» у Jellyfin стабильно лучшая по качеству, то есть
// ровно та, которую отдавали по умолчанию.
func mirkinoPickSource(sources []jfMediaSource, wantSize int64, wantName string, legacyLink bool) (jfMediaSource, bool) {
	if wantSize > 0 {
		for _, s := range sources {
			if s.Size == wantSize {
				return s, true
			}
		}
	}
	if n := strings.TrimSpace(wantName); n != "" {
		for _, s := range sources {
			if strings.EqualFold(strings.TrimSpace(s.Name), n) {
				return s, true
			}
		}
	}
	if legacyLink && strings.TrimSpace(wantName) == "" {
		return sources[0], true
	}
	return jfMediaSource{}, false
}
