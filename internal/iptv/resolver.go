package iptv

import (
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/httpclient"

	"github.com/rs/zerolog/log"
)

// resolver.go — источники, у которых нет постоянного URL.
//
// Часть вещателей не отдаёт ссылку на поток статически: плеер на их сайте
// проходит цепочку (страница → конфиг → media) и получает подписанный адрес,
// живущий недолго. Закрепить такой адрес в реестре нельзя — он протухнет.
// Поэтому источник хранит ИМЯ РЕЗОЛВЕРА, а настоящий URL добывается по
// требованию и кэшируется.
//
// Отдельная тонкость, ради которой всё и затевалось: цепочку часто пускают
// только с «правильного» Referer и только с IP нужной страны, тогда как сам
// плейлист и сегменты потом играют откуда угодно. Значит через прокси идёт
// только резолв (три лёгких запроса), а тяжёлое видео — напрямую.

// iptvResolveBalancer — имя балансера ТОЛЬКО для резолва динамических
// источников. Позволяет увести через нужную страну три служебных запроса,
// не трогая маршрут самого видео.
const iptvResolveBalancer = "iptv-resolve"

// iptvRegionBalancer — маршрут для гео-запертых источников (RegSource.GeoLocked):
// адрес известен и статичен, но вещатель пускает только «своих». Тот же
// балансер, которым ходит /proxy таких каналов (plugin "iptv-ru"), чтобы
// health-check мерил канал ровно тем маршрутом, каким его потом играют.
const iptvRegionBalancer = "iptv-ru"

// geoLockedHosts — вещатели, отдающие поток только «своей» стране. Проверка по
// ХОСТУ, а не по флагу в реестре: источник с такого домена мог приехать из
// донора или быть добавлен руками, и полагаться на то, что кто-то не забыл
// проставить geo_locked, нельзя — забытый флаг выглядит как заглушка Wink
// вместо канала.
var geoLockedHosts = []string{
	"ngenix.net", // Ростелеком/Wink: чужой IP → 301 на zabava-block/rtk_block
	// Cinerama (UZ): чужой IP → 302 на /blocked/index.m3u8. Заглушка — РАБОЧИЙ HLS с
	// рекламным роликом приложения (h264 1920x1080), поэтому health-check считает такой
	// источник живым, а зритель вместо канала смотрит рекламу. Отличить её от эфира по
	// коду ответа, манифесту или разрешению нельзя — только по маршруту.
	"cinerama.uz",
}

// IsGeoLockedHost reports whether a stream URL needs the broadcaster's country.
func IsGeoLockedHost(rawURL string) bool {
	host := hostOf(rawURL)
	if host == "" {
		return false
	}
	for _, h := range geoLockedHosts {
		if strings.Contains(host, h) {
			return true
		}
	}
	return false
}

// geoLockedUA — Ростелеком смотрит на User-Agent и отбивает клиентов, похожих
// на робота: с "Go-http-client/1.1" его edge отвечает 301 на заглушку, тогда
// как с любым браузерным (и даже с curl) отдаёт поток. Клиентский UA до
// апстрима доходит не всегда — например, когда запрос порождает сам прокси, —
// поэтому гео-запертому источнику UA проставляется ЯВНО.
const geoLockedUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"

// ResolverFunc добывает актуальный URL потока.
type ResolverFunc func(c *http.Client) (string, error)

// resolverEntry — реализация плюс срок жизни её результата.
type resolverEntry struct {
	fn  ResolverFunc
	ttl time.Duration
}

type resolvedURL struct {
	url string
	at  time.Time
}

var (
	resolversMu sync.RWMutex
	resolvers   = map[string]resolverEntry{
		"matchtv": {fn: resolveMatchTV, ttl: 10 * time.Minute},
	}

	resolvedMu sync.Mutex
	resolved   = map[string]resolvedURL{}
)

// HasResolver reports whether a named resolver exists.
func HasResolver(name string) bool {
	resolversMu.RLock()
	defer resolversMu.RUnlock()
	_, ok := resolvers[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

// ResolveSource returns the current stream URL for a named resolver, using the
// memoized value while it is fresh. Ошибка резолва НЕ инвалидирует прошлый
// удачный ответ: вещатель мог моргнуть, а протухший адрес всё же лучше пустоты.
func (s *Store) ResolveSource(name string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(name))
	resolversMu.RLock()
	ent, ok := resolvers[key]
	resolversMu.RUnlock()
	if !ok {
		return "", false
	}

	resolvedMu.Lock()
	prev, seen := resolved[key]
	resolvedMu.Unlock()
	if seen && time.Since(prev.at) < ent.ttl {
		return prev.url, true
	}

	// ОТДЕЛЬНЫЙ балансер, не общий "iptv": шаг, отдающий media-ссылку, у части
	// вещателей открыт только «своей» стране, но заворачивать туда весь IPTV
	// (плейлисты, EPG, health, сами стримы) ради трёх лёгких запросов — значит
	// добавить задержку и точку отказа сотням каналов. Проксируем только резолв:
	//   [[proxy.vless.entries]] balancers = [..., "iptv-resolve"]
	url, err := ent.fn(httpclient.NewForBalancerDynamic(iptvResolveBalancer, 20*time.Second))
	if err != nil || url == "" {
		if seen {
			log.Warn().Err(err).Str("resolver", key).Msg("iptv: резолв не удался — отдаём прошлый адрес")
			return prev.url, true
		}
		log.Warn().Err(err).Str("resolver", key).Msg("iptv: резолв не удался, адреса нет")
		return "", false
	}
	resolvedMu.Lock()
	resolved[key] = resolvedURL{url: url, at: time.Now()}
	resolvedMu.Unlock()
	log.Info().Str("resolver", key).Msg("iptv: адрес потока обновлён")
	return url, true
}

// ---------------------------------------------------------------------------
//  Матч ТВ
// ---------------------------------------------------------------------------

var (
	reMatchConfig = regexp.MustCompile(`config=(https://[^"&]+)`)
	reMatchMedia  = regexp.MustCompile(`https://bl\.video\.matchtv\.ru/media/start/[^]<"]+`)
	reMatchM3U8   = regexp.MustCompile(`https://m3u8\.video\.matchtv\.ru/[^]<"]+\.m3u8[^]<"]*`)
)

// matchTVReferer обязателен: без него конфиг отвечает «просмотр видео с этого
// сайта недоступен» — проверяется именно источник встраивания плеера.
const matchTVReferer = "https://matchtv.ru/"

const matchTVUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// resolveMatchTV walks the site's own player chain:
//
//	iframe → config (feed) → media/start → master .m3u8
func resolveMatchTV(c *http.Client) (string, error) {
	get := func(url string) (string, error) {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("User-Agent", matchTVUA)
		req.Header.Set("Referer", matchTVReferer)
		req.Header.Set("Accept-Encoding", "identity") // тела маленькие; без gzip проще разбирать
		resp, err := c.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
		if err != nil {
			return "", err
		}
		return string(body), nil
	}

	iframe, err := get("https://video.matchtv.ru/iframe/channel/106")
	if err != nil {
		return "", err
	}
	m := reMatchConfig.FindStringSubmatch(iframe)
	if len(m) < 2 {
		return "", errResolve("matchtv: в iframe нет config")
	}
	feed, err := get(m[1])
	if err != nil {
		return "", err
	}
	media := reMatchMedia.FindString(feed)
	if media == "" {
		return "", errResolve("matchtv: feed без media-ссылки (нужен Referer matchtv.ru)")
	}
	page, err := get(media)
	if err != nil {
		return "", err
	}
	m3u8 := reMatchM3U8.FindString(page)
	if m3u8 == "" {
		// Этот шаг вещатель отдаёт только «своему» региону: без нужного IP
		// приходит короткая заглушка вместо ссылки.
		return "", errResolve("matchtv: media без потока (нужен российский IP — [[proxycore.entries]] balancers=[\"iptv\"])")
	}
	return m3u8, nil
}

type errResolve string

func (e errResolve) Error() string { return string(e) }
