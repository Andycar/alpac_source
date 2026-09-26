package litesrc

// rudub — RuDub.TV, студия закадровой озвучки со своим онлайн-каталогом.
// Только сериалы: фильмов на площадке нет вовсе.
//
// Точка входа, которую дал пользователь — трекерная витрина
// r4.rudub.world/details.php?id=N: страница раздачи с торрентами по сезону, а
// под ними iframe того же плеера. Плеерный сайт (play26.ru-dub.xyz) отдаёт тот
// же контент в на порядок более удобной форме, поэтому балансер ходит туда, а
// трекер остаётся только как указатель на живой домен плеера (см. discoverHost).
//
// Цепочка:
//
//	GET /catalogue.html            -> 4.3к строк «<a href=".../{slug}/">Название <span>{серий}</span></a>»
//	                                  (один запрос на весь каталог, кэш rudubCatalogTTL)
//	GET /{slug}/ (+ /page/{n}/)    -> карточки серий: «{s} сезон, {e} серия» + /video/{id}
//	GET /{slug}/video/{id}         -> <iframe src="{player}/index.php?v=/{shard}/{hash}">
//	GET {player}/index.php?v=…&playlist
//	                               -> все серии ЭТОГО сезона: <span data="{shard}/{hash}">{e} серия</span>
//	                                  + event-filename="Show.s03e06.HD1080p.WEBRip…" — честная метка качества
//	GET {player}/vid.php?v=/{shard}/{hash}
//	                               -> master m3u8 (fhd.mp4 + sd.mp4) на cdnN.anivids.link
//
// Две вещи, которые стоили бы отладки на проде:
//
//  1. CDN (cdnN.anivids.link) отвечает 403 на запрос без User-Agent — именно
//     на него, а не на Referer: с любым UA плейлист отдаётся даже при чужом
//     Referer. Поэтому ссылки уходят клиенту через /proxy с заголовками
//     (streamProxyURLWithHeaders), где UA задан явно.
//  2. RESOLUTION в master-плейлисте врёт: у варианта fhd.mp4 написано
//     1280x720, а сегмент по факту 1920x1080 (проверено ffprobe). Поэтому
//     метки берутся из имени релиза в плеере (HD1080p/HD720p), а не из
//     плейлиста.
//
// Охват: примерно у половины каталога сезоны стоят на старых плеерах
// (cdnmovies.nl отвечает 404, vio.to/hdgo.cc не отвечают вовсе) — такие сезоны
// не отдаются, а checkSearch проверяет самый свежий сезон, чтобы источник не
// светился пустым на карточке.

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/proxylink"

	"github.com/rs/zerolog/log"
)

const (
	rudubDefaultHost    = "https://play26.ru-dub.xyz"
	rudubDefaultTracker = "https://r4.rudub.world"

	rudubCatalogTTL  = 6 * time.Hour
	rudubShowTTL     = 90 * time.Minute
	rudubNotFoundTTL = 30 * time.Minute

	// Страница сериала отдаёт 45 карточек; 10 страниц = 450 серий, длиннее
	// на площадке нет ничего (максимум в каталоге — 227).
	rudubMaxListPages = 10
	// Сезоны резолвятся параллельно: у «Анатомии страсти» их два десятка.
	rudubSeasonWorkers = 6
	// Потолок одновременных запросов к площадке на весь процесс. Без него
	// checksearch с десятков карточек сразу выглядит для сайта как наплыв.
	rudubMaxInflight = 12
)

var (
	// <a href="https://play26.ru-dub.xyz/vigil/" >Виджил <span>18</span></a>
	rudubCatalogRowRe = regexp.MustCompile(`<a href="https?://[^"]+?/([a-z0-9][a-z0-9\-]*)/"\s*>([^<]*)<span>(\d+)</span></a>`)
	// Карточка серии: ссылка на /video/{id}, ниже — «3 сезон, 6 серия».
	rudubEpisodeCardRe = regexp.MustCompile(`href="https?://[^"]+?/video/(\d+)"[\s\S]{0,400}?poster__meta[^>]*>\s*(\d+)\s*сезон,\s*(\d+)\s*сери`)
	// Ссылка на живой плеерный домен в шапке трекера.
	rudubPlayerHostRe = regexp.MustCompile(`https://play\d*\.ru-?dub\.[a-z]{2,6}`)
)

type rudubChecker struct {
	client  *http.Client
	tracker string

	mu sync.RWMutex
	// host и player меняются на живую: домен плеерного сайта нумерованный
	// (play26 → play27 → …), адрес самого плеера приходит из iframe.
	host   string
	player string

	catalog    []rudubCatalogItem
	catExpires time.Time
	catLoading sync.Mutex

	shows map[string]*rudubShow
	// probes — результат лёгкой проверки checksearch (свежий сезон играется
	// или нет). Без него популярная карточка гоняла бы три запроса к площадке
	// на КАЖДЫЙ опрос источника.
	probes map[string]rudubProbe

	// gate ограничивает одновременные запросы к площадке.
	gate chan struct{}
}

type rudubProbe struct {
	playable bool
	expires  time.Time
}

type rudubCatalogItem struct {
	slug     string
	name     string
	episodes int
}

type rudubShow struct {
	seasons []rudubSeason
	expires time.Time
}

type rudubSeason struct {
	num      int
	quality  string // «1080p» / «720p» — из имени релиза
	episodes []anividsEpisode
}

func NewRudubChecker(cfg config.Config) *rudubChecker {
	host := rudubNormalizeHost(cfg.Online.Rudub.Host)
	if host == "" {
		host = rudubDefaultHost
	}
	tracker := rudubNormalizeHost(cfg.Online.Rudub.TrackerHost)
	if tracker == "" {
		tracker = rudubDefaultTracker
	}

	return &rudubChecker{
		client:  httpclient.NewForBalancer("rudub", 15*time.Second),
		host:    host,
		tracker: tracker,
		player:  anividsDefaultPlayer,
		shows:   make(map[string]*rudubShow, 64),
		probes:  make(map[string]rudubProbe, 128),
		gate:    make(chan struct{}, rudubMaxInflight),
	}
}

func rudubNormalizeHost(raw string) string {
	h := strings.TrimSpace(strings.TrimRight(raw, "/"))
	if h == "" {
		return ""
	}
	if !strings.Contains(h, "://") {
		h = "https://" + h
	}
	return h
}

// ---------------------------------------------------------------------------
// HTTP entry point
// ---------------------------------------------------------------------------

func (r *rudubChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			writeCheckSearchResponseNoRCH(w, r.checkSearch(req), pluginQualityBadgeGet("rudub"))
			return
		}

		raw := strings.TrimPrefix(strings.ToLower(req.URL.Path), "/lite/")
		switch {
		case strings.HasPrefix(raw, "rudub/play"):
			r.play(w, req, links)
		case strings.HasPrefix(raw, "rudub/serial"):
			r.serial(w, req)
		default:
			r.index(w, req)
		}
	}
}

// checkSearch отвечает на вопрос «есть ли что показать» без полного обхода
// сериала: каталог (обычно из кэша) + самый свежий сезон. Сезон проверяется
// по-настоящему — половина каталога висит на умерших плеерах, и врать в
// бейдже значит выдавать пустую карточку.
func (r *rudubChecker) checkSearch(req *http.Request) bool {
	q := req.URL.Query()
	if !rudubIsSerial(q) {
		return false
	}
	item, ok := r.pickShow(req.Context(), q.Get("title"), q.Get("original_title"))
	if !ok {
		return false
	}

	// Полное дерево уже в кэше — отвечаем по нему.
	if show := r.cachedShow(item.slug); show != nil {
		return len(show.seasons) > 0
	}
	if playable, ok := r.cachedProbe(item.slug); ok {
		return playable
	}

	playable := false
	if cards, ok := r.fetchShowPage(req.Context(), item.slug, 1); ok && len(cards) > 0 {
		if season, epID, ok := rudubFreshestCard(cards); ok {
			_, playable = r.resolveSeason(req.Context(), item.slug, season, []string{epID})
		}
	}
	r.storeProbe(item.slug, playable)
	return playable
}

func (r *rudubChecker) cachedProbe(slug string) (bool, bool) {
	r.mu.RLock()
	probe, ok := r.probes[slug]
	r.mu.RUnlock()
	if !ok || time.Now().After(probe.expires) {
		return false, false
	}
	return probe.playable, true
}

func (r *rudubChecker) storeProbe(slug string, playable bool) {
	ttl := rudubShowTTL
	if !playable {
		ttl = rudubNotFoundTTL
	}
	r.mu.Lock()
	if len(r.probes) > 2048 {
		now := time.Now()
		for k, v := range r.probes {
			if now.After(v.expires) {
				delete(r.probes, k)
			}
		}
	}
	r.probes[slug] = rudubProbe{playable: playable, expires: time.Now().Add(ttl)}
	r.mu.Unlock()
}

// index отдаёт список сезонов карточки.
func (r *rudubChecker) index(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))

	if !rudubIsSerial(q) {
		writeGetsTVEmpty(w, rjson)
		return
	}
	item, ok := r.pickShow(req.Context(), q.Get("title"), q.Get("original_title"))
	if !ok {
		writeGetsTVEmpty(w, rjson)
		return
	}
	show, ok := r.resolveShow(req.Context(), item.slug)
	if !ok || len(show.seasons) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(req)
	data := make([]map[string]any, 0, len(show.seasons))
	labels := make([]string, 0, len(show.seasons))
	for _, season := range show.seasons {
		label := strconv.Itoa(season.num) + " сезон"
		link := host + "/lite/rudub/serial?slug=" + url.QueryEscape(item.slug) + "&s=" + strconv.Itoa(season.num)
		if rjson {
			link += "&rjson=true"
		}
		// Номер сезона нужен и в «s», и в «id»: без него capi-дрилл не понимает,
		// в какую строку спускаться, и уходит в первый попавшийся сезон
		// («id» — форма, которую читают hdvb/zetflix, «s» — прямая).
		data = append(data, map[string]any{
			"method": "link",
			"url":    link,
			"name":   label,
			"id":     season.num,
			"s":      season.num,
		})
		labels = append(labels, label)
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "season", "data": data})
		return
	}
	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range data {
		getsTVAppendSeasonHTML(&sb, row, labels[i], i == 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// serial отдаёт серии одного сезона.
func (r *rudubChecker) serial(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))

	slug := rudubSanitizeSlug(q.Get("slug"))
	seasonNum, _ := strconv.Atoi(strings.TrimSpace(q.Get("s")))
	if slug == "" || seasonNum <= 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	show, ok := r.resolveShow(req.Context(), slug)
	if !ok {
		writeGetsTVEmpty(w, rjson)
		return
	}
	var season rudubSeason
	found := false
	for _, s := range show.seasons {
		if s.num == seasonNum {
			season, found = s, true
			break
		}
	}
	if !found || len(season.episodes) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(req)
	showTitle := r.showName(slug)
	data := make([]map[string]any, 0, len(season.episodes))
	labels := make([]string, 0, len(season.episodes))
	nums := make([]int, 0, len(season.episodes))
	for _, ep := range season.episodes {
		label := strconv.Itoa(ep.num) + " серия"
		link := host + "/lite/rudub/play?h=" + url.QueryEscape(ep.hash)
		if season.quality != "" {
			link += "&q=" + url.QueryEscape(season.quality)
		}
		if showTitle != "" {
			link += "&t=" + url.QueryEscape(showTitle)
		}
		row := map[string]any{
			"method":    "call",
			"url":       link,
			"title":     label,
			"translate": "RuDub",
			"s":         season.num,
			"e":         ep.num,
		}
		data = append(data, row)
		labels = append(labels, label)
		nums = append(nums, ep.num)
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "episode", "data": data})
		return
	}
	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range data {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, season.num, nums[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// play минтит master-плейлист серии — вся механика в общем слое anivids.
func (r *rudubChecker) play(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	anividsWritePlay(w, req, links, anividsPlayOptions{
		plugin:  "rudub",
		voice:   "RuDub",
		player:  r.playerHost(),
		hash:    strings.TrimSpace(q.Get("h")),
		quality: q.Get("q"),
		title:   q.Get("t"),
		fetch:   r.fetch,
	})
}

// ---------------------------------------------------------------------------
// Каталог и подбор сериала
// ---------------------------------------------------------------------------

func (r *rudubChecker) pickShow(ctx context.Context, title, originalTitle string) (rudubCatalogItem, bool) {
	items, ok := r.ensureCatalog(ctx)
	if !ok {
		return rudubCatalogItem{}, false
	}
	return rudubPickShow(items, title, originalTitle)
}

// rudubPickShow сводит карточку Лампы к строке каталога.
//
// Каталог даёт только русское название и slug (slug — транслитерация
// оригинального названия: «12 Monkeys» → 12monkeys, иногда с хвостом
// «-serial»). Года в нём нет, поэтому одноимённые сериалы разных лет
// («Ведьмак» 2002 и 2019) различить нечем: при совпадении по обоим полям
// выигрывает такая строка, иначе берётся первое точное совпадение.
func rudubPickShow(items []rudubCatalogItem, title, originalTitle string) (rudubCatalogItem, bool) {
	wantName := normalizeSearchTitle(title)
	wantSlug := rudubSlugKey(originalTitle)
	wantSlugRu := rudubSlugKey(title)
	if wantName == "" && wantSlug == "" {
		return rudubCatalogItem{}, false
	}

	var byName, bySlug rudubCatalogItem
	var haveName, haveSlug bool

	for _, item := range items {
		nameHit := wantName != "" && normalizeSearchTitle(item.name) == wantName
		slugKey := rudubSlugKey(item.slug)
		slugHit := slugKey != "" && (slugKey == wantSlug || (wantSlug == "" && slugKey == wantSlugRu))

		if nameHit && slugHit {
			return item, true
		}
		if nameHit && !haveName {
			byName, haveName = item, true
		}
		if slugHit && !haveSlug {
			bySlug, haveSlug = item, true
		}
	}

	// Русское название точнее: slug'и вроде «see» (Видеть) короткие и
	// пересекаются с обычными английскими словами.
	if haveName {
		return byName, true
	}
	if haveSlug {
		return bySlug, true
	}
	return rudubCatalogItem{}, false
}

// rudubSlugKey приводит название и slug к общему виду: только буквы и цифры,
// без хвоста «serial», который сайт вешает на неоднозначные адреса (1670-serial).
func rudubSlugKey(raw string) string {
	v := strings.ToLower(strings.TrimSpace(raw))
	if v == "" {
		return ""
	}
	var sb strings.Builder
	sb.Grow(len(v))
	for _, ch := range v {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') {
			sb.WriteRune(ch)
		}
	}
	out := sb.String()
	if len(out) > 6 {
		out = strings.TrimSuffix(out, "serial")
	}
	return out
}

func (r *rudubChecker) ensureCatalog(ctx context.Context) ([]rudubCatalogItem, bool) {
	r.mu.RLock()
	items, expires := r.catalog, r.catExpires
	r.mu.RUnlock()
	if len(items) > 0 && time.Now().Before(expires) {
		return items, true
	}

	// Каталог — полмегабайта; тянем его один раз даже под параллельным
	// checksearch со всех карточек сразу.
	r.catLoading.Lock()
	defer r.catLoading.Unlock()

	r.mu.RLock()
	items, expires = r.catalog, r.catExpires
	r.mu.RUnlock()
	if len(items) > 0 && time.Now().Before(expires) {
		return items, true
	}

	host := r.resolveHost(ctx)
	body, ok := r.fetch(ctx, host+"/catalogue.html", host+"/")
	if !ok {
		// Домен мог переехать — спрашиваем трекер и пробуем ещё раз.
		if next, moved := r.discoverHost(ctx); moved {
			body, ok = r.fetch(ctx, next+"/catalogue.html", next+"/")
		}
	}
	if !ok {
		// Отдаём протухший каталог, если он есть: сериалы из него никуда не
		// делись, а без него источник просто исчезнет с карточки.
		if len(items) > 0 {
			return items, true
		}
		return nil, false
	}

	parsed := rudubParseCatalog(body)
	if len(parsed) == 0 {
		if len(items) > 0 {
			return items, true
		}
		return nil, false
	}

	r.mu.Lock()
	r.catalog = parsed
	r.catExpires = time.Now().Add(rudubCatalogTTL)
	r.mu.Unlock()

	log.Info().Int("shows", len(parsed)).Msg("rudub: catalogue loaded")
	return parsed, true
}

func rudubParseCatalog(html string) []rudubCatalogItem {
	matches := rudubCatalogRowRe.FindAllStringSubmatch(html, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]rudubCatalogItem, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		slug := strings.TrimSpace(m[1])
		name := strings.TrimSpace(rudubUnescape(m[2]))
		if slug == "" || name == "" {
			continue
		}
		if _, dup := seen[slug]; dup {
			continue
		}
		seen[slug] = struct{}{}
		episodes, _ := strconv.Atoi(m[3])
		out = append(out, rudubCatalogItem{slug: slug, name: name, episodes: episodes})
	}
	return out
}

var rudubUnescaper = strings.NewReplacer(
	"&quot;", `"`, "&#039;", "'", "&apos;", "'",
	"&laquo;", "«", "&raquo;", "»", "&nbsp;", " ",
	"&lt;", "<", "&gt;", ">", "&amp;", "&",
)

func rudubUnescape(v string) string {
	// &amp;quot; встречается в каталоге как есть — разворачиваем дважды.
	return rudubUnescaper.Replace(rudubUnescaper.Replace(v))
}

// ---------------------------------------------------------------------------
// Дерево сериала
// ---------------------------------------------------------------------------

// showName возвращает название сериала из каталога (пусто, если каталог ещё
// не загружен). Оно уезжает в ссылку серии и обратно в ответ play: capi
// сверяет title ответа с названием карточки и выбрасывает источник, если там
// что-то другое (имя студии в этом поле выглядело как «другой фильм»).
func (r *rudubChecker) showName(slug string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, item := range r.catalog {
		if item.slug == slug {
			return item.name
		}
	}
	return ""
}

func (r *rudubChecker) cachedShow(slug string) *rudubShow {
	r.mu.RLock()
	show, ok := r.shows[slug]
	r.mu.RUnlock()
	if !ok || time.Now().After(show.expires) {
		return nil
	}
	return show
}

func (r *rudubChecker) resolveShow(ctx context.Context, slug string) (*rudubShow, bool) {
	if show := r.cachedShow(slug); show != nil {
		return show, len(show.seasons) > 0
	}

	candidates, ok := r.collectSeasonCards(ctx, slug)
	if !ok || len(candidates) == 0 {
		r.storeShow(slug, &rudubShow{expires: time.Now().Add(rudubNotFoundTTL)})
		return &rudubShow{}, false
	}

	nums := make([]int, 0, len(candidates))
	for num := range candidates {
		nums = append(nums, num)
	}
	sort.Ints(nums)

	type result struct {
		season rudubSeason
		ok     bool
	}
	results := make([]result, len(nums))

	workers := rudubSeasonWorkers
	if len(nums) < workers {
		workers = len(nums)
	}
	var wg sync.WaitGroup
	jobs := make(chan int)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				season, ok := r.resolveSeason(ctx, slug, nums[idx], candidates[nums[idx]])
				results[idx] = result{season: season, ok: ok}
			}
		}()
	}
	for i := range nums {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	seasons := make([]rudubSeason, 0, len(nums))
	for _, res := range results {
		if res.ok && len(res.season.episodes) > 0 {
			seasons = append(seasons, res.season)
		}
	}

	ttl := rudubShowTTL
	if len(seasons) == 0 {
		ttl = rudubNotFoundTTL
	}
	show := &rudubShow{seasons: seasons, expires: time.Now().Add(ttl)}
	r.storeShow(slug, show)
	return show, len(seasons) > 0
}

func (r *rudubChecker) storeShow(slug string, show *rudubShow) {
	r.mu.Lock()
	if len(r.shows) > 512 {
		now := time.Now()
		for k, v := range r.shows {
			if now.After(v.expires) {
				delete(r.shows, k)
			}
		}
	}
	r.shows[slug] = show
	r.mu.Unlock()
}

// collectSeasonCards листает страницы сериала, пока не соберёт по несколько
// серий на каждый сезон. Список идёт от новых к старым, поэтому первая
// страница обычно закрывает свежие сезоны, а первый сезон лежит на последней.
func (r *rudubChecker) collectSeasonCards(ctx context.Context, slug string) (map[int][]string, bool) {
	cards := make(map[int][]string, 8)
	fetched := false

	for page := 1; page <= rudubMaxListPages; page++ {
		body, ok := r.fetchShowPage(ctx, slug, page)
		if !ok || len(body) == 0 {
			break
		}
		fetched = true
		for _, card := range body {
			// Держим до трёх кандидатов: если у первой серии сезона остался
			// мёртвый плеер, соседняя может быть уже перезалита.
			if len(cards[card.season]) < 3 {
				cards[card.season] = append(cards[card.season], card.episodeID)
			}
		}
		if rudubHaveAllSeasons(cards) {
			break
		}
	}
	return cards, fetched
}

type rudubCard struct {
	season    int
	episode   int
	episodeID string
}

func rudubHaveAllSeasons(cards map[int][]string) bool {
	if len(cards) == 0 {
		return false
	}
	max := 0
	for num := range cards {
		if num > max {
			max = num
		}
	}
	for n := 1; n <= max; n++ {
		if len(cards[n]) == 0 {
			return false
		}
	}
	return true
}

func rudubFreshestCard(cards []rudubCard) (int, string, bool) {
	if len(cards) == 0 {
		return 0, "", false
	}
	return cards[0].season, cards[0].episodeID, true
}

func (r *rudubChecker) fetchShowPage(ctx context.Context, slug string, page int) ([]rudubCard, bool) {
	host := r.resolveHost(ctx)
	target := host + "/" + slug + "/"
	if page > 1 {
		target += "page/" + strconv.Itoa(page) + "/"
	}
	body, ok := r.fetch(ctx, target, host+"/")
	if !ok {
		return nil, false
	}
	return rudubParseCards(body), true
}

func rudubParseCards(html string) []rudubCard {
	matches := rudubEpisodeCardRe.FindAllStringSubmatch(html, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]rudubCard, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		id := m[1]
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		season, _ := strconv.Atoi(m[2])
		episode, _ := strconv.Atoi(m[3])
		if season <= 0 {
			continue
		}
		out = append(out, rudubCard{season: season, episode: episode, episodeID: id})
	}
	return out
}

// resolveSeason разворачивает сезон: страница серии → iframe → плейлист.
func (r *rudubChecker) resolveSeason(ctx context.Context, slug string, seasonNum int, episodeIDs []string) (rudubSeason, bool) {
	host := r.resolveHost(ctx)
	for _, id := range episodeIDs {
		page, ok := r.fetch(ctx, host+"/"+slug+"/video/"+id, host+"/")
		if !ok {
			continue
		}
		player, hash, ok := anividsParseIframe(page)
		if !ok {
			// Сезон стоит на старом плеере (cdnmovies.nl / vio.to / hdgo.cc) —
			// все они мертвы, играть нечего.
			continue
		}
		r.rememberPlayer(player)

		body, ok := r.fetch(ctx, player+"/index.php?v=/"+hash+"&playlist", host+"/")
		if !ok {
			// Плейлист не ответил — отдаём хотя бы саму серию.
			return rudubSeason{num: seasonNum, episodes: []anividsEpisode{{num: 1, hash: hash}}}, true
		}

		episodes := anividsParsePlaylist(body)
		quality := anividsQualityFromRelease(submatch1(anividsFilenameRe, body))
		if len(episodes) == 0 {
			episodes = []anividsEpisode{{num: 1, hash: hash}}
		}
		return rudubSeason{num: seasonNum, quality: quality, episodes: episodes}, true
	}
	return rudubSeason{}, false
}

// ---------------------------------------------------------------------------
// Хосты
// ---------------------------------------------------------------------------

// resolveHost — текущий домен плеерного сайта. Переезд ищется лениво: пока
// каталог грузится, ходить на трекер незачем — ensureCatalog зовёт
// discoverHost на первой же неудаче.
func (r *rudubChecker) resolveHost(ctx context.Context) string {
	r.mu.RLock()
	host := r.host
	r.mu.RUnlock()

	if host == "" {
		if next, ok := r.discoverHost(ctx); ok {
			return next
		}
		return rudubDefaultHost
	}
	return host
}

func (r *rudubChecker) playerHost() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.player != "" {
		return r.player
	}
	return anividsDefaultPlayer
}

func (r *rudubChecker) rememberPlayer(host string) {
	host = strings.TrimRight(strings.TrimSpace(host), "/")
	if host == "" {
		return
	}
	r.mu.RLock()
	same := r.player == host
	r.mu.RUnlock()
	if same {
		return
	}
	r.mu.Lock()
	r.player = host
	r.mu.Unlock()
	log.Info().Str("player", host).Msg("rudub: player host updated")
}

// discoverHost спрашивает у трекера актуальный номер плеерного домена —
// play26 однажды станет play27, и без этого источник умрёт молча.
func (r *rudubChecker) discoverHost(ctx context.Context) (string, bool) {
	body, ok := r.fetch(ctx, r.tracker+"/", r.tracker+"/")
	if !ok {
		return "", false
	}
	found := rudubPlayerHostRe.FindString(body)
	if found == "" {
		return "", false
	}
	found = strings.TrimRight(found, "/")

	r.mu.Lock()
	moved := r.host != found
	r.host = found
	if moved {
		// Домен переехал — старое дерево ссылалось на прежний хост.
		r.catalog = nil
		r.catExpires = time.Time{}
		r.shows = make(map[string]*rudubShow, 64)
		r.probes = make(map[string]rudubProbe, 128)
	}
	r.mu.Unlock()

	if moved {
		log.Info().Str("host", found).Msg("rudub: player site moved")
	}
	return found, true
}

// ---------------------------------------------------------------------------
// Fetch
// ---------------------------------------------------------------------------

func (r *rudubChecker) fetch(ctx context.Context, target, referer string) (string, bool) {
	if r.gate != nil {
		select {
		case r.gate <- struct{}{}:
			defer func() { <-r.gate }()
		case <-ctx.Done():
			return "", false
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("User-Agent", anividsUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}

	resp, err := balancerDoWithRetry(ctx, r.client, req, 1)
	if err != nil {
		log.Debug().Err(err).Str("url", truncURL(target)).Msg("rudub: fetch error")
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Debug().Int("status", resp.StatusCode).Str("url", truncURL(target)).Msg("rudub: non-2xx")
		return "", false
	}
	// Каталог — около 520 КБ, остальное сильно меньше.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", false
	}
	return string(body), true
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// rudubIsSerial: площадка сериальная, на фильмах источнику делать нечего.
func rudubIsSerial(q url.Values) bool {
	return strings.TrimSpace(q.Get("serial")) == "1"
}

func rudubSanitizeSlug(raw string) string {
	slug := strings.ToLower(strings.TrimSpace(raw))
	if slug == "" || len(slug) > 120 {
		return ""
	}
	for _, ch := range slug {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-' {
			continue
		}
		return ""
	}
	return slug
}
