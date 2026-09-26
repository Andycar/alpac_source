package proxyapi

import (
	"context"
	"hash/fnv"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

// edge.go — разгрузка primary: отдача потока с нод кластера.
//
// Раньше ВСЕ байты видео шли через main, хотя ноды умеют отдавать те же
// ссылки: общий `[proxy_link] shared_secret` даёт любой из них расшифровать
// токен, выпущенный где угодно (проверено на проде — ссылка от main
// отдавалась нодой байт в байт). Здесь main при запросе потока отвечает
// одним 302 на ноду, и дальше клиент общается уже с ней.
//
// Почему 302, а не «выпускать ссылки сразу на ноду»: мест выдачи /proxy-ссылок
// в коде около сорока, и переписывать хост в каждом — верный способ забыть
// одно. Редирект живёт в единственной точке — там, где токен уже расшифрован,
// то есть известен плагин и понятно, можно ли его выносить.
//
// Лишний переход всего один на поток, а не на сегмент: манифест переписывает
// тот, кто его отдал (streamHostFromRequest), поэтому сегменты в нём уже
// адресованы на ноду.

type edgePool struct {
	mu      sync.RWMutex
	hosts   []string
	allowed map[string]bool // белый список плагинов; ключ "*" = все
	// denied — что не выносить НИКОГДА, сильнее «*». Нужен, потому что с «*»
	// список источников не надо вести руками, но есть те, для кого вынос
	// сломает не скорость, а саму работу: IPTV (сессия живёт в памяти
	// выдавшего сервера) и YouTube (ссылка привязана к адресу, который её
	// извлёк). Их не спасает и возврат: он бы удваивал переходы на каждом
	// сегменте живого эфира.
	denied map[string]bool
	// dead — момент, до которого нода считается нерабочей после неудачной пробы.
	dead map[string]time.Time
	// geoDeny — нода → страны, которым её отдавать нельзя (см. StreamEdgeGeoDeny).
	// Ключ — нормализованный адрес, значение — множество кодов стран в верхнем регистре.
	geoDeny map[string]map[string]bool
}

var edges = &edgePool{allowed: map[string]bool{}, denied: map[string]bool{}, dead: map[string]time.Time{}, geoDeny: map[string]map[string]bool{}}

// geoLookup — определение страны по адресу зрителя (GeoLite2 живёт в httpapi, сюда
// приходит функцией, чтобы proxyapi не зависел от базы и оставался тестируемым).
var geoLookup atomic.Pointer[func(string) string]

// SetGeoLookup подключает определение страны. Без него гео-ограничения нод не работают
// (и это безопасно: список просто не применяется, всё как раньше).
func SetGeoLookup(fn func(string) string) {
	if fn == nil {
		geoLookup.Store(nil)
		return
	}
	geoLookup.Store(&fn)
}

// configureEdgeGeo применяет карту «нода → запрещённые страны».
func configureEdgeGeo(deny map[string][]string) {
	m := make(map[string]map[string]bool, len(deny))
	for host, countries := range deny {
		h := normalizeEdge(host)
		if h == "" {
			continue
		}
		set := make(map[string]bool, len(countries))
		for _, c := range countries {
			if c = strings.ToUpper(strings.TrimSpace(c)); c != "" {
				set[c] = true
			}
		}
		if len(set) > 0 {
			m[strings.ToLower(h)] = set
		}
	}
	edges.mu.Lock()
	edges.geoDeny = m
	edges.mu.Unlock()
	if len(m) > 0 {
		for h, set := range m {
			cs := make([]string, 0, len(set))
			for c := range set {
				cs = append(cs, c)
			}
			sort.Strings(cs)
			log.Info().Str("edge", h).Strs("deny", cs).Msg("proxy: нода не отдаётся этим странам")
		}
	}
}

// geoBlockedLocked — нельзя ли отдать эту ноду зрителю из страны country.
// Вызывать под edges.mu (R)Lock. Пустая страна = не знаем, не ограничиваем.
func (p *edgePool) geoBlockedLocked(host, country string) bool {
	if country == "" || len(p.geoDeny) == 0 {
		return false
	}
	return p.geoDeny[strings.ToLower(host)][country]
}

// edgeGeoLogged гасит повтор одинаковых записей в логе: решение принимается на каждый
// запрос, а знать надо лишь то, что правило вообще срабатывает.
var edgeGeoLogged sync.Map // "host|country" -> time.Time

func noteEdgeGeoBlock(host, country, plugin string) {
	k := host + "|" + country
	now := time.Now()
	if v, ok := edgeGeoLogged.Load(k); ok {
		if t, ok2 := v.(time.Time); ok2 && now.Sub(t) < time.Minute {
			return
		}
	}
	edgeGeoLogged.Store(k, now)
	log.Info().Str("edge", host).Str("country", country).Str("plugin", plugin).
		Msg("proxy: нода закрыта для страны зрителя — отдаём иначе")
}

// edgeNoRetry — метка «этот запрос уже возвращала нода, отдавай сам».
// Ставит её нода, читает primary. Без метки возврат зациклился бы:
// primary снова выбрал бы ту же ноду по тому же токену.
const edgeNoRetry = "_noedge"

// edgeHint — параметр, которым КЛИЕНТ просит отдать поток с конкретной ноды.
//
// Клиент знает то, чего не знает сервер: свою настоящую скорость до каждой
// ноды. Геопривязка по адресу этого не заменяет — мобильный оператор в Москве
// может ходить в Германию быстрее, чем в Петербург. Поэтому выбор ноды —
// подсказка от замера скорости в приложении, а сервер лишь проверяет, что
// названная нода вообще наша и живая.
const edgeHint = "edge"

// edgeSkip — ноды, которые зритель отключил у себя в приложении. Симметричен
// edgeHint: тот просит конкретную ноду, этот запрещает перечисленные. Список
// через запятую, едет тем же путём — в первом запросе и дальше в ссылках
// сегментов, иначе запрет действовал бы только на манифест.
const edgeSkip = "edge_skip"

// edgePrimary — адрес primary глазами ноды (`[cluster] primary_host`).
// Пусто у самого primary и у одиночной установки: возвращать некому.
var edgePrimary struct {
	mu   sync.RWMutex
	host string
}

// configureEdgeFallback запоминает, куда нода возвращает то, что не смогла отдать.
func configureEdgeFallback(primaryHost string) {
	h := strings.TrimRight(strings.TrimSpace(primaryHost), "/")
	edgePrimary.mu.Lock()
	edgePrimary.host = h
	edgePrimary.mu.Unlock()
}

// notMedia распознаёт ответ, который отдавать плееру бессмысленно.
//
// Отказ источника далеко не всегда приходит кодом ошибки: content-router
// отвечает 200 и списком ссылок в JSON, часть CDN — 200 и страницей с
// капчей. Плеер на такое говорит «источник отказал» и роняет просмотр, а
// возврат по коду ответа тут не срабатывает — кода-то нет. Через /proxy
// ходит только медиа, поэтому JSON и HTML здесь всегда означают отказ.
//
// text/plain НЕ считается отказом: часть источников отдаёт так m3u8.
func notMedia(contentType string) bool {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	switch ct {
	case "application/json", "text/json", "text/html", "application/xhtml+xml":
		return true
	}
	return false
}

// edgeGiveBack возвращает адрес на primary для запроса, который эта нода
// отдать не смогла, или "" если возвращать не нужно.
//
// Зачем: белый список источников — это ручная догадка о том, пустит ли CDN
// чужой адрес. Догадка стареет молча: источник меняет политику, и зритель
// получает чёрный экран вместо кино. С возвратом худшее, что бывает при
// неверном списке, — лишний переход, а не сломанный просмотр.
func edgeGiveBack(r *http.Request, status int, contentType string) string {
	if status < 400 && !notMedia(contentType) {
		return ""
	}
	if r.URL.Query().Get(edgeNoRetry) != "" {
		return "" // уже возвращали — дальше некуда
	}
	edgePrimary.mu.RLock()
	host := edgePrimary.host
	edgePrimary.mu.RUnlock()
	if host == "" {
		return ""
	}
	q := r.URL.Query()
	q.Set(edgeNoRetry, "1")
	return host + r.URL.Path + "?" + q.Encode()
}

// ApplyEdgeConfig переприменяет настройки раздачи с нод на живом сервере.
// Раньше configureEdges звался только из proxyapi.New, поэтому добавление ноды
// в stream_edges требовало перезапуска — со всеми оборванными потоками. Вызывается
// из httpapi.Server.Reload по SIGHUP.
func ApplyEdgeConfig(hosts, plugins, exclude []string, geoDeny map[string][]string) {
	configureEdges(hosts, plugins, exclude)
	configureEdgeGeo(geoDeny)
}

// configureEdges применяет настройки. Пустой список хостов = функция выключена.
func configureEdges(hosts, plugins, exclude []string) {
	edges.mu.Lock()
	defer edges.mu.Unlock()
	edges.hosts = edges.hosts[:0]
	// Новый список — новая жизнь: память о «мёртвых» нодах относится к
	// прежнему составу и не должна переживать перенастройку.
	edges.dead = map[string]time.Time{}
	for _, h := range hosts {
		h = strings.TrimRight(strings.TrimSpace(h), "/")
		if h != "" {
			edges.hosts = append(edges.hosts, h)
		}
	}
	edges.allowed = make(map[string]bool, len(plugins))
	for _, p := range plugins {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" {
			edges.allowed[p] = true
		}
	}
	edges.denied = make(map[string]bool, len(exclude))
	for _, p := range exclude {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" {
			edges.denied[p] = true
		}
	}
	if len(edges.hosts) > 0 {
		log.Info().Int("edges", len(edges.hosts)).Int("plugins", len(edges.allowed)).
			Int("excluded", len(edges.denied)).
			Msg("proxy: отдача потока с нод включена")
	}
}

// pickEdge возвращает адрес ноды для этого токена или "" (отдавать самим).
//
// Выбор ЛИПКИЙ по токену: все запросы одного потока обязаны идти на одну ноду.
// Иначе манифест переписала бы одна, а сегменты просил бы клиент у другой —
// и у источников, привязывающих поток к адресу скачавшего манифест, всё
// развалилось бы на первом же сегменте.
func pickEdge(plugin, token string, r *http.Request, mintedBy string) string {
	edges.mu.RLock()
	empty := len(edges.hosts) == 0
	edges.mu.RUnlock()
	if empty {
		return ""
	}

	// Страна зрителя — считаем один раз и лениво: без настроенных гео-ограничений
	// (обычный случай) обращения к базе не будет вовсе.
	country, countryDone := "", false
	clientCountry := func() string {
		if countryDone {
			return country
		}
		countryDone = true
		if r == nil {
			return ""
		}
		if fnp := geoLookup.Load(); fnp != nil {
			country = strings.ToUpper(strings.TrimSpace((*fnp)(requestIP(r))))
		}
		return country
	}
	if r != nil && r.URL.Query().Get(edgeNoRetry) != "" {
		// Нода уже пробовала и вернула запрос — отдаём сами. Отказ считаем
		// только для «свободных» ссылок: подписанную добывшей нодой больше
		// некому отдать, и штрафовать за неё источник не за что.
		if mintedBy == "" {
			noteEdgeBounce(plugin)
		}
		return ""
	}

	// Ссылку добыла нода — отдавать должна она же, что бы ни говорили белый
	// список и самоотключение: у CDN с привязкой к адресу другого варианта нет.
	// Нода лежит → отдаём сами; для привязанной ссылки это провал, но лучшего
	// исхода не существует, а неподписанные источники так и играют.
	if want := normalizeEdge(mintedBy); want != "" {
		edges.mu.RLock()
		defer edges.mu.RUnlock()
		for _, h := range edges.hosts {
			if strings.EqualFold(h, want) {
				if t, ok := edges.dead[h]; ok && time.Now().Before(t) {
					return ""
				}
				// Имя ноды закрыто в стране зрителя — отдавать туда бессмысленно: у
				// него оборвут TLS. Для ссылки с привязкой к IP добывшего это всё
				// равно провал, но недостижимый хост — провал гарантированный.
				if c := clientCountry(); edges.geoBlockedLocked(h, c) {
					noteEdgeGeoBlock(h, c, plugin)
					return ""
				}
				return h
			}
		}
		return "" // подписана адресом, которого нет в stream_edges — не рискуем
	}

	if edgeSuspended(plugin) {
		return ""
	}

	edges.mu.RLock()
	defer edges.mu.RUnlock()
	low := strings.ToLower(plugin)
	if edges.denied[low] {
		return ""
	}
	if !edges.allowed["*"] && !edges.allowed[low] {
		return ""
	}

	// Отключённые зрителем ноды. Запрет сильнее просьбы: если клиент разом
	// прислал и edge, и edge_skip с тем же адресом, выигрывает запрет.
	skip := parseEdgeSkip(EdgeSkipEffective(r))

	// Просьба клиента — но только про ноду из нашего же списка: иначе параметром
	// в ссылке можно было бы увести чужой просмотр на посторонний хост.
	if r != nil {
		if want := normalizeEdge(r.URL.Query().Get(edgeHint)); want != "" && !skip[strings.ToLower(want)] {
			for _, h := range edges.hosts {
				if strings.EqualFold(h, want) {
					if c := clientCountry(); edges.geoBlockedLocked(h, c) {
						noteEdgeGeoBlock(h, c, plugin)
						break // просьба клиента не отменяет блокировку его же провайдера
					}
					if t, ok := edges.dead[h]; !ok || time.Now().After(t) {
						return h
					}
					break // названная нода не отвечает — выбираем как обычно
				}
			}
		}
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(token))
	start := int(h.Sum32()) % len(edges.hosts)
	now := time.Now()
	blocked := ""
	for i := 0; i < len(edges.hosts); i++ {
		cand := edges.hosts[(start+i)%len(edges.hosts)]
		if skip[strings.ToLower(cand)] {
			continue // зритель отключил эту ноду у себя
		}
		if t, ok := edges.dead[cand]; ok && now.Before(t) {
			continue // нода недавно не ответила — пропускаем, не пытая клиента
		}
		if c := clientCountry(); edges.geoBlockedLocked(cand, c) {
			blocked = cand
			continue // берём следующую; все закрыты — отдадим сами
		}
		return cand
	}
	if blocked != "" {
		noteEdgeGeoBlock(blocked, country, plugin)
	}
	return ""
}

// EdgeSkipFrom — сырое значение списка отключённых нод из запроса.
func EdgeSkipFrom(r *http.Request) string {
	if r == nil {
		return ""
	}
	return strings.TrimSpace(r.URL.Query().Get(edgeSkip))
}

// edgeSkipStored — отключённые ноды, сохранённые за аккаунтом зрителя (бот и экран «Проверка
// связи» пишут туда же). Подсовывается снаружи: сам proxyapi про телеграм-аккаунты не знает и
// знать не должен, а зависимость в обратную сторону замкнула бы пакеты.
var edgeSkipStored atomic.Pointer[func(*http.Request) string]

// SetEdgeSkipResolver задаёт, как достать сохранённый список по запросу.
func SetEdgeSkipResolver(fn func(*http.Request) string) {
	if fn == nil {
		edgeSkipStored.Store(nil)
		return
	}
	edgeSkipStored.Store(&fn)
}

// EdgeSkipEffective — то, что реально запрещено для этого запроса: список из ссылки ПЛЮС
// сохранённый за аккаунтом. Объединение, а не выбор одного: ссылку собирают один раз, а живёт
// она долго, и настройка, сделанная после выдачи ссылки, обязана подействовать сразу.
//
// Сохранённый список виден не всегда: плеер ходит за сегментами без авторизации, и спросить там
// некого. Поэтому главное применение — момент СБОРКИ ссылки, где запрос ещё авторизован; дальше
// запрет едет в самой ссылке.
func EdgeSkipEffective(r *http.Request) string {
	fromURL := EdgeSkipFrom(r)
	fnp := edgeSkipStored.Load()
	if fnp == nil || r == nil {
		return fromURL
	}
	stored := strings.TrimSpace((*fnp)(r))
	if stored == "" {
		return fromURL
	}
	if fromURL == "" {
		return stored
	}
	// Слияние с отсевом повторов: список уезжает в ссылку, а она и так длинная.
	seen := map[string]bool{}
	out := make([]string, 0, 8)
	for _, part := range strings.Split(fromURL+","+stored, ",") {
		h := normalizeEdge(part)
		if h == "" {
			continue
		}
		low := strings.ToLower(h)
		if seen[low] {
			continue
		}
		seen[low] = true
		out = append(out, h)
	}
	return strings.Join(out, ",")
}

// EdgeSkipSet — отключённые зрителем ноды набором, для кластера: тот выбирает, КТО ДОБУДЕТ
// ссылку, и обязан считаться с тем же запретом, что и отдача. Иначе у источников с правилом
// «edge» зритель уезжал бы на выключенную ноду: добыли там — оттуда и отдадут.
func EdgeSkipSet(r *http.Request) map[string]bool {
	return parseEdgeSkip(EdgeSkipEffective(r))
}

// parseEdgeSkip разбирает список в набор нормализованных адресов.
func parseEdgeSkip(raw string) map[string]bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	out := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		if h := normalizeEdge(part); h != "" {
			out[strings.ToLower(h)] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// normalizeEdge приводит адрес ноды к виду из настроек.
func normalizeEdge(h string) string {
	h = strings.TrimRight(strings.TrimSpace(h), "/")
	if h == "" {
		return ""
	}
	if !strings.Contains(h, "://") {
		h = "https://" + h
	}
	return h
}

// EdgeHosts возвращает клиентские адреса нод для отдачи потока — то, что
// приложению надо замерить, чтобы выбрать себе ближайшую.
func EdgeHosts() []string {
	edges.mu.RLock()
	defer edges.mu.RUnlock()
	out := make([]string, len(edges.hosts))
	copy(out, edges.hosts)
	return out
}

// EdgeHostsFor — то же, но без нод, закрытых в стране этого зрителя: список едет в
// приложение для замера скорости, и предлагать там заведомо недостижимый адрес значит
// тратить пробу и показывать человеку сломанный пункт в настройках.
func EdgeHostsFor(ip string) []string {
	all := EdgeHosts()
	country := ""
	if fnp := geoLookup.Load(); fnp != nil {
		country = strings.ToUpper(strings.TrimSpace((*fnp)(ip)))
	}
	if country == "" {
		return all
	}
	edges.mu.RLock()
	defer edges.mu.RUnlock()
	if len(edges.geoDeny) == 0 {
		return all
	}
	out := make([]string, 0, len(all))
	for _, h := range all {
		if !edges.geoBlockedLocked(h, country) {
			out = append(out, h)
		}
	}
	return out
}

// markEdgeDead выводит ноду из ротации на минуту. Зовётся, когда проба показала
// недоступность: молча слать туда клиента значит подарить ему мёртвый поток.
func markEdgeDead(host string) {
	edges.mu.Lock()
	edges.dead[host] = time.Now().Add(time.Minute)
	edges.mu.Unlock()
	log.Warn().Str("edge", host).Msg("proxy: нода не отвечает, временно исключена из отдачи")
}

// edgeAlive — дешёвая проба перед редиректом, с кэшем на 30 секунд.
//
// Без неё падение ноды означало бы, что часть зрителей получает 302 в никуда,
// и виноватым выглядел бы плеер. Проба идёт по HEAD на корень и стоит доли
// секунды; результат общий для всех запросов.
var edgeProbe sync.Map // host -> *edgeProbeState

type edgeProbeState struct {
	mu   sync.Mutex
	at   time.Time
	live bool
}

func edgeAlive(client *http.Client, host string) bool {
	v, _ := edgeProbe.LoadOrStore(host, &edgeProbeState{})
	st := v.(*edgeProbeState)
	st.mu.Lock()
	defer st.mu.Unlock()
	if time.Since(st.at) < 30*time.Second {
		return st.live
	}
	req, err := http.NewRequest(http.MethodHead, host+"/", nil)
	if err != nil {
		st.at, st.live = time.Now(), false
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := client.Do(req.WithContext(ctx))
	st.at = time.Now()
	st.live = err == nil
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !st.live {
		markEdgeDead(host)
	}
	return st.live
}

// ——— самоотключение источника, который ноды отдать не могут ———
//
// Белый список — это догадка человека о чужой политике CDN, и она стареет:
// источник вводит привязку к адресу, и каждый сегмент начинает ходить
// «main → нода → обратно на main». Просмотр не ломается (см. edgeGiveBack),
// но два перехода вместо нуля — на каждый сегмент, и заметить это можно
// только по жалобе. Поэтому primary считает возвраты и сам перестаёт
// выносить источник, который стабильно не идёт, а через паузу пробует снова:
// политика CDN может и вернуться обратно.
const (
	bounceWindow    = 5 * time.Minute
	bounceThreshold = 5
	bounceSuspend   = 30 * time.Minute
)

type bounceState struct {
	mu          sync.Mutex
	count       int
	since       time.Time
	suspend     time.Time
	suspensions int64 // сколько раз источник снимался с выноса (для сводки мощностей)
}

// EdgeBounceStat — состояние выноса источника для сводки мощностей.
type EdgeBounceStat struct {
	Suspended   bool
	Suspensions int64
}

// EdgeBounceStats — по каждому источнику: снят ли с выноса сейчас и сколько
// раз снимался. Сводка мощностей показывает по ним, какой трафик прибит к main.
func EdgeBounceStats() map[string]EdgeBounceStat {
	out := map[string]EdgeBounceStat{}
	now := time.Now()
	bounces.Range(func(k, v any) bool {
		st := v.(*bounceState)
		st.mu.Lock()
		out[k.(string)] = EdgeBounceStat{Suspended: now.Before(st.suspend), Suspensions: st.suspensions}
		st.mu.Unlock()
		return true
	})
	return out
}

var bounces sync.Map // plugin -> *bounceState

func bounceFor(plugin string) *bounceState {
	v, _ := bounces.LoadOrStore(strings.ToLower(plugin), &bounceState{})
	return v.(*bounceState)
}

// noteEdgeBounce отмечает, что нода вернула поток этого источника обратно.
func noteEdgeBounce(plugin string) {
	if plugin == "" {
		return
	}
	st := bounceFor(plugin)
	now := time.Now()

	st.mu.Lock()
	defer st.mu.Unlock()
	if now.Sub(st.since) > bounceWindow {
		st.count, st.since = 0, now
	}
	st.count++
	if st.count >= bounceThreshold && now.After(st.suspend) {
		st.suspend = now.Add(bounceSuspend)
		st.suspensions++
		log.Warn().Str("plugin", plugin).Int("bounces", st.count).
			Dur("suspend", bounceSuspend).
			Msg("proxy: источник не идёт с нод, временно отдаю сам")
	}
}

// edgeSuspended сообщает, что источник сейчас выносить не надо.
func edgeSuspended(plugin string) bool {
	v, ok := bounces.Load(strings.ToLower(plugin))
	if !ok {
		return false
	}
	st := v.(*bounceState)
	st.mu.Lock()
	defer st.mu.Unlock()
	return time.Now().Before(st.suspend)
}

// resetEdgeBounces — только для тестов.
func resetEdgeBounces() { bounces = sync.Map{} }

// EdgeHintFrom — подсказка клиента из запроса ("" если её нет). Экспорт для
// httpapi: /lite-эндпоинты выдают /proxy-ссылки и должны переносить её туда.
func EdgeHintFrom(r *http.Request) string {
	if r == nil {
		return ""
	}
	return strings.TrimSpace(r.URL.Query().Get(edgeHint))
}

// WithEdge дописывает подсказку клиента к /proxy-ссылке.
//
// Зачем: подсказка живёт в ПЕРВОМ запросе (манифест), а плеер дальше ходит по
// ссылкам из тела плейлиста — без переноса каждый сегмент выбирал бы ноду по
// хешу токена, и замер скорости в приложении ничего бы не решал. Проверка,
// что нода наша и живая, остаётся в pickEdge — здесь только переносим.
func WithEdge(u, hint, skip string) string {
	if u == "" {
		return u
	}
	add := func(key, val string) {
		val = strings.TrimSpace(val)
		if val == "" {
			return
		}
		sep := "?"
		if strings.Contains(u, "?") {
			sep = "&"
		}
		u += sep + key + "=" + url.QueryEscape(val)
	}
	add(edgeHint, hint)
	// Запрет тоже обязан доехать до сегментов: плеер ходит по ссылкам из тела
	// плейлиста, и без переноса отключённая нода вернулась бы на второй же
	// секунде просмотра.
	add(edgeSkip, skip)
	return u
}
