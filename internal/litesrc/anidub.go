package litesrc

// anidub — AniDUB Online (online.anidub.com): аниме, дорамы, азиатское кино и
// мультфильмы в озвучке команды AniDUB.
//
// Видео крутится тем же плеером, что у RuDub, поэтому вся механика потока
// живёт в общем слое anivids.go, а здесь — только каталог:
//
//	GET /?do=search&subaction=search&story={запрос}
//	                       -> карточки «th-item»: ссылка, «Рус / Original», «2012 • Аниме сериалы»
//	GET /{id}-{slug}.html  -> «Сезон N, серии X из Y» + <iframe> плеера
//	                          (дальше — anivids: playlist со всеми сериями, vid.php за потоком)
//
// Две особенности каталога, которых не было у RuDub:
//
//  1. Тайтл — это ОДИН сезон. «Сезон 2» лежит отдельной страницей со своим
//     названием, поэтому список сезонов собирается из нескольких результатов
//     поиска, а номер читается со страницы тайтла.
//  2. Каталог смешанный: рядом с сериалами лежат «Аниме Фильмы» и «Азиатские
//     фильмы». Категория из выдачи разводит их по movie/serial — иначе
//     источник отвечал бы фильмом на сериальную карточку и наоборот.

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
	anidubDefaultHost = "https://online.anidub.com"

	anidubSearchTTL   = 6 * time.Hour
	anidubShowTTL     = 90 * time.Minute
	anidubNotFoundTTL = 30 * time.Minute

	// Сколько результатов поиска разворачиваем в сезоны. Больше шести —
	// это уже однофамильцы, а каждый кандидат стоит запроса.
	anidubMaxCandidates = 6
	anidubMaxInflight   = 12
)

var (
	// Карточка выдачи: ссылка, «Рус / Original», «2012 • Аниме сериалы».
	anidubCardHrefRe  = regexp.MustCompile(`href="(https?://[^"]+?/\d+-[^"]+\.html)"`)
	anidubCardTitleRe = regexp.MustCompile(`class="th-title">([^<]+)<`)
	anidubCardSubRe   = regexp.MustCompile(`class="th-subtitle[^"]*">([\s\S]{0,300}?)</div>`)
	anidubYearRe      = regexp.MustCompile(`(\d{4})`)
	// «Сезон 1, серии 10 из 16» на странице тайтла.
	anidubSeasonRe = regexp.MustCompile(`Сезон\s*(\d+)`)
	// Хвосты в названии: «[26 из 26]», «[TV-1 + TV-2]».
	anidubBracketRe = regexp.MustCompile(`\[[^\]]*\]`)
	// Пометка вида в скобках: «Играй, эуфониум! (фильм)» — это маркер, а не
	// часть названия, и в карточке TMDB его нет.
	anidubParenRe = regexp.MustCompile(`(?i)\((?:фильм|movie|тв|tv|ova|ona|спешл|special)[^)]*\)`)
	anidubTagRe   = regexp.MustCompile(`<[^>]*>`)
	// Адрес тайтла: только «/{id}-{slug}.html» на своём хосте.
	anidubPathRe = regexp.MustCompile(`^/\d+-[a-z0-9_-]+\.html$`)
	// Номер сезона в названии тайтла: «ТВ-2», «TV-2», «2 сезон», «Season 2».
	anidubSeasonTitleRes = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(?:тв|tv)\s*[-–—]?\s*(\d{1,2})\b`),
		regexp.MustCompile(`(?i)(\d{1,2})\s*(?:-?(?:й|ый|nd|rd|th))?\s*(?:сезон|season)`),
		regexp.MustCompile(`(?i)(?:сезон|season)\s*(\d{1,2})`),
	}
)

// Вид контента карточки. Подпись в выдаче — НЕ всегда категория: у части
// тайтлов там стоит «1 сезон», и тогда вид неизвестен. Отбрасывать такие
// карточки нельзя — именно так терялись свежие полнометражки.
const (
	anidubKindUnknown = iota
	anidubKindMovie
	anidubKindSerial
)

var anidubMovieCats = map[string]struct{}{
	"аниме фильмы":     {},
	"азиатские фильмы": {},
	"полнометражные":   {},
	"мультфильмы":      {},
}

var anidubSerialCats = map[string]struct{}{
	"аниме сериалы": {},
	"аниме ongoing": {},
	"аниме ova":     {},
	"аниме ona":     {},
	"дорамы":        {},
	"дубляж":        {},
}

func anidubKindOf(cat string) int {
	cat = strings.ToLower(strings.TrimSpace(cat))
	if _, ok := anidubMovieCats[cat]; ok {
		return anidubKindMovie
	}
	if _, ok := anidubSerialCats[cat]; ok {
		return anidubKindSerial
	}
	return anidubKindUnknown
}

type anidubChecker struct {
	client *http.Client
	host   string

	mu       sync.RWMutex
	player   string
	searches map[string]anidubSearchEntry
	shows    map[string]*anidubShow
	probes   map[string]anidubProbe

	gate chan struct{}
}

type anidubCard struct {
	url  string
	rus  string
	orig string
	year int
	cat  string
	kind int // anidubKind*
}

type anidubSearchEntry struct {
	cards   []anidubCard
	expires time.Time
}

type anidubShow struct {
	season   int
	quality  string
	episodes []anividsEpisode
	expires  time.Time
}

type anidubProbe struct {
	playable bool
	expires  time.Time
}

func NewAnidubChecker(cfg config.Config) *anidubChecker {
	host := strings.TrimSpace(strings.TrimRight(cfg.Online.Anidub.Host, "/"))
	if host == "" {
		host = anidubDefaultHost
	}
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}
	return &anidubChecker{
		client:   anidubHTTPClient(),
		host:     host,
		player:   anividsDefaultPlayer,
		searches: make(map[string]anidubSearchEntry, 128),
		shows:    make(map[string]*anidubShow, 128),
		probes:   make(map[string]anidubProbe, 128),
		gate:     make(chan struct{}, anidubMaxInflight),
	}
}

// anidubHTTPClient НЕ должен падать на общий http/1.1-транспорт: тот выключает
// HTTP/2 (осознанный фикс коалесцирования соединений при InsecureSkipVerify) и
// режет TLS-handshake десятью секундами, а этот edge с таким ClientHello
// рукопожатие не доводит — каждый запрос умирал ровно на 10 с, как когда-то у
// anwap и uafilm. Явно назначенный балансеру прокси по-прежнему выигрывает:
// меняется только запасной путь.
func anidubHTTPClient() *http.Client {
	if t := httpclient.TransportForBalancer("anidub"); t != nil {
		return &http.Client{Transport: t, Timeout: 15 * time.Second}
	}
	tr, _ := http.DefaultTransport.(*http.Transport)
	if tr == nil {
		tr = &http.Transport{ForceAttemptHTTP2: true}
	} else {
		tr = tr.Clone()
		tr.ForceAttemptHTTP2 = true
	}
	return &http.Client{Transport: tr, Timeout: 15 * time.Second}
}

// ---------------------------------------------------------------------------
// HTTP entry point
// ---------------------------------------------------------------------------

func (a *anidubChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			writeCheckSearchResponseNoRCH(w, a.checkSearch(req), pluginQualityBadgeGet("anidub"))
			return
		}

		raw := strings.TrimPrefix(strings.ToLower(req.URL.Path), "/lite/")
		switch {
		case strings.HasPrefix(raw, "anidub/play"):
			q := req.URL.Query()
			anividsWritePlay(w, req, links, anividsPlayOptions{
				plugin:  "anidub",
				voice:   "AniDUB",
				player:  a.playerHost(),
				hash:    strings.TrimSpace(q.Get("h")),
				quality: q.Get("q"),
				title:   q.Get("t"),
				fetch:   a.fetch,
			})
		case strings.HasPrefix(raw, "anidub/serial"):
			a.serial(w, req)
		default:
			a.index(w, req)
		}
	}
}

// checkSearch отвечает на «есть ли что показать»: поиск (обычно из кэша) плюс
// разворот одного, лучшего кандидата. Половину каталога составляют старые
// тайтлы, чьи сезоны висят на умерших плеерах, так что проверять приходится
// по-настоящему — иначе источник светится пустым.
func (a *anidubChecker) checkSearch(req *http.Request) bool {
	q := req.URL.Query()
	cards, ok := a.lookup(req.Context(), q)
	if !ok || len(cards) == 0 {
		return false
	}
	card := cards[0]

	if show := a.cachedShow(card.url); show != nil {
		return len(show.episodes) > 0
	}
	if playable, ok := a.cachedProbe(card.url); ok {
		return playable
	}
	show, ok := a.resolveShow(req.Context(), card.url)
	playable := ok && len(show.episodes) > 0
	a.storeProbe(card.url, playable)
	return playable
}

// index отдаёт список сезонов (сериал) или единственный поток (фильм).
func (a *anidubChecker) index(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))

	cards, ok := a.lookup(req.Context(), q)
	if !ok || len(cards) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	serial := strings.TrimSpace(q.Get("serial")) == "1"
	host := hostFromRequest(req)
	cardTitle := getsTVJoinName(strings.TrimSpace(q.Get("title")), strings.TrimSpace(q.Get("original_title")))

	// Фильм: тайтл целиком — один поток, спускаться некуда.
	if !serial {
		show, ok := a.resolveShow(req.Context(), cards[0].url)
		if !ok || len(show.episodes) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}
		link := a.playLink(host, show.episodes[0].hash, show.quality, cardTitle, rjson)
		row := map[string]any{
			"method":    "call",
			"url":       link,
			"title":     cardTitle,
			"translate": "AniDUB",
		}
		if rjson {
			writeJSON(w, http.StatusOK, map[string]any{"type": "movie", "data": []map[string]any{row}})
			return
		}
		var sb strings.Builder
		sb.WriteString(`<div class="videos__line">`)
		getsTVAppendMovieHTML(&sb, row, "AniDUB", true, 0, 0)
		sb.WriteString(`</div>`)
		writeHTML(w, http.StatusOK, sb.String())
		return
	}

	// Сериал: каждый тайтл — отдельный сезон, поэтому кандидатов разворачиваем
	// параллельно и собираем из них список сезонов.
	type resolved struct {
		card anidubCard
		show *anidubShow
	}
	results := make([]resolved, len(cards))
	var wg sync.WaitGroup
	for i, card := range cards {
		wg.Add(1)
		go func(i int, card anidubCard) {
			defer wg.Done()
			if show, ok := a.resolveShow(req.Context(), card.url); ok && len(show.episodes) > 0 {
				results[i] = resolved{card: card, show: show}
			}
		}(i, card)
	}
	wg.Wait()

	type seasonRow struct {
		num  int
		url  string
		eps  int
		name string
	}
	bySeason := make(map[int]seasonRow, len(results))
	for _, r := range results {
		if r.show == nil {
			continue
		}
		num := anidubSeasonFor(r.card, r.show)
		// При коллизии выигрывает тайтл с бОльшим числом серий: у AniDub рядом
		// с сезоном часто лежит его же OVA/спешл с тем же номером.
		if prev, ok := bySeason[num]; ok && prev.eps >= len(r.show.episodes) {
			continue
		}
		bySeason[num] = seasonRow{num: num, url: r.card.url, eps: len(r.show.episodes), name: r.card.rus}
	}
	if len(bySeason) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	nums := make([]int, 0, len(bySeason))
	for num := range bySeason {
		nums = append(nums, num)
	}
	sort.Ints(nums)

	data := make([]map[string]any, 0, len(nums))
	labels := make([]string, 0, len(nums))
	for _, num := range nums {
		row := bySeason[num]
		label := strconv.Itoa(num) + " сезон"
		link := host + "/lite/anidub/serial?u=" + url.QueryEscape(row.url) + "&s=" + strconv.Itoa(num)
		if t := strings.TrimSpace(cardTitle); t != "" {
			link += "&t=" + url.QueryEscape(t)
		}
		if rjson {
			link += "&rjson=true"
		}
		// Номер сезона нужен и в «s», и в «id» — без него capi-дрилл не знает,
		// в какую строку спускаться (см. rudub.go).
		data = append(data, map[string]any{
			"method": "link",
			"url":    link,
			"name":   label,
			"id":     num,
			"s":      num,
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

// serial отдаёт серии одного тайтла-сезона.
func (a *anidubChecker) serial(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))

	target, ok := a.sanitizeTitleURL(q.Get("u"))
	if !ok {
		writeGetsTVEmpty(w, rjson)
		return
	}
	season, _ := strconv.Atoi(strings.TrimSpace(q.Get("s")))
	if season <= 0 {
		season = 1
	}

	show, ok := a.resolveShow(req.Context(), target)
	if !ok || len(show.episodes) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(req)
	cardTitle := strings.TrimSpace(q.Get("t"))
	data := make([]map[string]any, 0, len(show.episodes))
	labels := make([]string, 0, len(show.episodes))
	nums := make([]int, 0, len(show.episodes))
	for _, ep := range show.episodes {
		label := strconv.Itoa(ep.num) + " серия"
		row := map[string]any{
			"method":    "call",
			"url":       a.playLink(host, ep.hash, show.quality, cardTitle, false),
			"title":     label,
			"translate": "AniDUB",
			"s":         season,
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
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, season, nums[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (a *anidubChecker) playLink(host, hash, quality, title string, rjson bool) string {
	link := host + "/lite/anidub/play?h=" + url.QueryEscape(hash)
	if quality != "" {
		link += "&q=" + url.QueryEscape(quality)
	}
	if title != "" {
		link += "&t=" + url.QueryEscape(title)
	}
	if rjson {
		link += "&rjson=true"
	}
	return link
}

// ---------------------------------------------------------------------------
// Поиск и подбор тайтла
// ---------------------------------------------------------------------------

// lookup ищет карточку Лампы в каталоге. Возвращает кандидатов, отсортированных
// по убыванию уверенности: первым идёт лучший.
func (a *anidubChecker) lookup(ctx context.Context, q url.Values) ([]anidubCard, bool) {
	title := strings.TrimSpace(q.Get("title"))
	orig := strings.TrimSpace(q.Get("original_title"))
	year, _ := strconv.Atoi(strings.TrimSpace(q.Get("year")))
	serial := strings.TrimSpace(q.Get("serial")) == "1"
	if title == "" && orig == "" {
		return nil, false
	}

	seen := make(map[string]struct{}, 16)
	var picked []anidubCard
	// Полное название TMDB часто длиннее того, что знает каталог, и поиск по
	// нему не находит ничего («Рыцарь-скелет вступает в параллельный мир»).
	// Поэтому вторым заходом идёт укороченный запрос — первые два слова.
	for _, query := range []string{title, orig, anidubShortQuery(title)} {
		if strings.TrimSpace(query) == "" {
			continue
		}
		cards, ok := a.search(ctx, query)
		if !ok {
			continue
		}
		for _, c := range anidubPickCards(cards, title, orig, year, serial) {
			if _, dup := seen[c.url]; dup {
				continue
			}
			seen[c.url] = struct{}{}
			picked = append(picked, c)
			if len(picked) >= anidubMaxCandidates {
				return picked, true
			}
		}
		// Русское название — более точный запрос; если оно уже дало попадания,
		// вторым заходом не платим.
		if len(picked) > 0 {
			break
		}
	}
	return picked, len(picked) > 0
}

// anidubPickCards оставляет карточки, чьё название совпадает с запрошенным, и
// сортирует их: сперва совпавшие по обоим названиям, затем по году.
//
// Год у аниме — слабый признак: TMDB держит дату первого эфира всего сериала, а
// AniDub — год конкретного сезона, поэтому год только ранжирует, но не
// отсекает. Отсекает вид контента: фильм для сериальной карточки (и наоборот)
// — это всегда чужой тайтл.
func anidubPickCards(cards []anidubCard, title, orig string, year int, serial bool) []anidubCard {
	type scored struct {
		card  anidubCard
		score int
	}
	var out []scored
	for _, c := range cards {
		// Вид отсекает только когда он ИЗВЕСТЕН: фильм на сериальной карточке
		// (и наоборот) — всегда чужой тайтл, а «вид неизвестен» значит лишь
		// то, что в подписи оказался не тот текст.
		if (serial && c.kind == anidubKindMovie) || (!serial && c.kind == anidubKindSerial) {
			continue
		}
		ruHit := anidubTitleMatch(c.rus, title)
		origHit := anidubTitleMatch(c.orig, orig)
		if h := anidubTitleMatch(c.rus, orig); h > origHit {
			origHit = h
		}
		if ruHit == 0 && origHit == 0 {
			continue
		}
		// Точное совпадение весит вдвое против совпадения по основной части —
		// «Наруто» не должен обойти «Наруто: Ураганные хроники» на её же карточке.
		score := 2*ruHit + 2*origHit
		if year > 0 && c.year > 0 {
			switch d := c.year - year; {
			case d == 0:
				score += 2
			case d > -2 && d < 2:
				score++
			}
		}
		out = append(out, scored{card: c, score: score})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].score > out[j].score })

	picked := make([]anidubCard, 0, len(out))
	for _, s := range out {
		picked = append(picked, s.card)
	}
	return picked
}

// anidubTitleMatch сверяет одну сторону названия с запрошенным и возвращает
// силу совпадения: 2 — точное, 1 — по основной части до подзаголовка, 0 — мимо.
//
// Градация нужна из-за аниме: у AniDub тайтл почти всегда несёт собственный
// подзаголовок («Реинкарнация безработного: История о приключениях в другом
// мире», «Mushoku Tensei: Isekai Ittara Honki Dasu»), а TMDB держит короткое
// имя или ДРУГОЙ подзаголовок («Mushoku Tensei: Jobless Reincarnation»).
// Точное сравнение теряло такие тайтлы целиком. Обратная сторона — «Наруто» и
// «Наруто: Ураганные хроники» тоже сходятся по основной части, поэтому это
// совпадение слабее точного и проигрывает ему при сортировке кандидатов.
func anidubTitleMatch(side, wantRaw string) int {
	if strings.TrimSpace(side) == "" || strings.TrimSpace(wantRaw) == "" {
		return 0
	}
	want := normalizeSearchTitle(anidubParenRe.ReplaceAllString(wantRaw, " "))
	wantBase := normalizeSearchTitle(anidubTitleBase(wantRaw))
	if want == "" {
		return 0
	}
	best := 0
	for _, part := range strings.Split(side, "/") {
		got := normalizeSearchTitle(anidubParenRe.ReplaceAllString(part, " "))
		if got == "" {
			continue
		}
		if got == want {
			return 2
		}
		gotBase := normalizeSearchTitle(anidubTitleBase(part))
		if gotBase != "" && (gotBase == want || gotBase == wantBase || got == wantBase) {
			best = 1
			continue
		}
		// Русские названия у TMDB и AniDub нередко расходятся хвостом:
		// «Рыцарь-скелет ВСТУПАЕТ В ПАРАЛЛЕЛЬНЫЙ МИР» против «Рыцарь-скелет
		// В ИНОМ МИРЕ». Совпадения первых двух слов достаточно, чтобы узнать
		// тайтл, и мало, чтобы спутать «Магическую битву» с «Магической
		// академией» — там расходится уже второе слово.
		if anidubSharePrefix2(got, want) {
			best = 1
		}
	}
	return best
}

// anidubSharePrefix2 — у обоих названий совпадают первые два слова.
func anidubSharePrefix2(a, b string) bool {
	fa, fb := strings.Fields(a), strings.Fields(b)
	if len(fa) < 2 || len(fb) < 2 {
		return false
	}
	return fa[0] == fb[0] && fa[1] == fb[1]
}

// anidubTitleBase отрезает подзаголовок: всё после «:» или « - ».
func anidubTitleBase(raw string) string {
	s := strings.TrimSpace(anidubParenRe.ReplaceAllString(raw, " "))
	if idx := strings.Index(s, ":"); idx > 0 {
		s = s[:idx]
	}
	if idx := strings.Index(s, " - "); idx > 0 {
		s = s[:idx]
	}
	return strings.TrimSpace(s)
}

// anidubShortQuery — первые два слова названия, запасной поисковый запрос.
// Пустая строка, если слов меньше трёх: там укорачивать нечего.
//
// Слова считаются по НОРМАЛИЗОВАННОЙ форме: «Рыцарь-скелет» — это два слова, а
// не одно, иначе запасной запрос выходил «Рыцарь-скелет вступает» и не искал
// ничего нового.
func anidubShortQuery(title string) string {
	fields := strings.Fields(normalizeSearchTitle(anidubTitleBase(title)))
	if len(fields) < 3 {
		return ""
	}
	return fields[0] + " " + fields[1]
}

func (a *anidubChecker) search(ctx context.Context, query string) ([]anidubCard, bool) {
	key := normalizeSearchTitle(query)
	if key == "" {
		return nil, false
	}

	a.mu.RLock()
	entry, ok := a.searches[key]
	a.mu.RUnlock()
	if ok && time.Now().Before(entry.expires) {
		return entry.cards, true
	}

	target := a.host + "/?do=search&subaction=search&story=" + url.QueryEscape(query)
	body, ok := a.fetch(ctx, target, a.host+"/")
	if !ok {
		return nil, false
	}
	cards := anidubParseCards(body)

	a.mu.Lock()
	if len(a.searches) > 512 {
		now := time.Now()
		for k, v := range a.searches {
			if now.After(v.expires) {
				delete(a.searches, k)
			}
		}
	}
	a.searches[key] = anidubSearchEntry{cards: cards, expires: time.Now().Add(anidubSearchTTL)}
	a.mu.Unlock()

	return cards, true
}

func anidubParseCards(html string) []anidubCard {
	chunks := strings.Split(html, `<div class="th-item">`)
	if len(chunks) < 2 {
		return nil
	}
	out := make([]anidubCard, 0, len(chunks)-1)
	seen := make(map[string]struct{}, len(chunks))
	for _, chunk := range chunks[1:] {
		href := submatch1(anidubCardHrefRe, chunk)
		if href == "" {
			continue
		}
		if _, dup := seen[href]; dup {
			continue
		}
		seen[href] = struct{}{}

		rus, orig := anidubSplitTitle(submatch1(anidubCardTitleRe, chunk))
		if rus == "" && orig == "" {
			continue
		}

		sub := strings.TrimSpace(anidubTagRe.ReplaceAllString(submatch1(anidubCardSubRe, chunk), " "))
		sub = strings.Join(strings.Fields(sub), " ")
		year, _ := strconv.Atoi(submatch1(anidubYearRe, sub))
		cat := sub
		if idx := strings.Index(sub, "•"); idx >= 0 {
			cat = strings.TrimSpace(sub[idx+len("•"):])
		}
		out = append(out, anidubCard{url: href, rus: rus, orig: orig, year: year, cat: cat, kind: anidubKindOf(cat)})
	}
	return out
}

// anidubSplitTitle разбирает «Рус / Original [26 из 26]» на две стороны.
func anidubSplitTitle(raw string) (rus, orig string) {
	s := strings.TrimSpace(anidubBracketRe.ReplaceAllString(raw, " "))
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return "", ""
	}
	if idx := strings.Index(s, " / "); idx >= 0 {
		return strings.TrimSpace(s[:idx]), strings.TrimSpace(s[idx+3:])
	}
	return s, ""
}

// anidubSeasonFor — номер сезона тайтла: название релиза важнее страницы.
func anidubSeasonFor(card anidubCard, show *anidubShow) int {
	if n := anidubSeasonFromTitle(card.rus, card.orig); n > 0 {
		return n
	}
	if show != nil && show.season > 0 {
		return show.season
	}
	return 1
}

// anidubSeasonFromTitle достаёт номер сезона из названия тайтла.
//
// На AniDub каждый сезон — отдельная страница, и почти на всех написано
// «Сезон 1»: студия нумерует серии внутри своего релиза, а не внутри сериала.
// Настоящий номер живёт в названии — «ТВ-2», «TV-2», «Mushoku Tensei II»,
// «2 сезон». Без этого три тайтла «Блича» схлопывались в один «1 сезон», и
// зритель видел только тот, у кого больше серий.
func anidubSeasonFromTitle(parts ...string) int {
	for _, raw := range parts {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		for _, re := range anidubSeasonTitleRes {
			if m := re.FindStringSubmatch(s); len(m) > 1 {
				if n, err := strconv.Atoi(m[1]); err == nil && n > 1 && n < 100 {
					return n
				}
			}
		}
		if n := anidubRomanSeason(s); n > 1 {
			return n
		}
	}
	return 0
}

// anidubRomanSeason ловит римскую нумерацию продолжений: «Mushoku Tensei II».
// Только заглавные и только отдельным словом — иначе «I» из любого названия
// превратит первый сезон во второй.
func anidubRomanSeason(s string) int {
	for _, word := range strings.Fields(s) {
		word = strings.Trim(word, ".,:;()[]")
		switch word {
		case "II":
			return 2
		case "III":
			return 3
		case "IV":
			return 4
		case "V":
			return 5
		}
	}
	return 0
}

// ---------------------------------------------------------------------------
// Разворот тайтла
// ---------------------------------------------------------------------------

func (a *anidubChecker) cachedShow(target string) *anidubShow {
	a.mu.RLock()
	show, ok := a.shows[target]
	a.mu.RUnlock()
	if !ok || time.Now().After(show.expires) {
		return nil
	}
	return show
}

func (a *anidubChecker) resolveShow(ctx context.Context, target string) (*anidubShow, bool) {
	if show := a.cachedShow(target); show != nil {
		return show, len(show.episodes) > 0
	}

	page, ok := a.fetch(ctx, target, a.host+"/")
	if !ok {
		return &anidubShow{}, false
	}

	// В кэш идёт ТОЛЬКО номер, который назвала страница: он не зависит от
	// того, с какой карточки пришли. Номер из названия релиза считается выше
	// по стеку (см. anidubSeasonFor) — иначе первый же заход через
	// /lite/anidub/serial, где названия нет, зафиксировал бы в кэше «1».
	season := 1
	if m := anidubSeasonRe.FindStringSubmatch(anidubTagRe.ReplaceAllString(page, " ")); len(m) > 1 {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
			season = n
		}
	}

	player, hash, ok := anividsParseIframe(page)
	if !ok {
		// Старые тайтлы стоят на умерших плеерах — играть нечего.
		a.storeShow(target, &anidubShow{season: season, expires: time.Now().Add(anidubNotFoundTTL)})
		return &anidubShow{}, false
	}
	a.rememberPlayer(player)

	show := &anidubShow{season: season, expires: time.Now().Add(anidubShowTTL)}
	if body, ok := a.fetch(ctx, anividsPlaylistURL(player, hash), a.host+"/"); ok {
		show.episodes = anividsParsePlaylist(body)
		show.quality = anividsQualityFromRelease(submatch1(anividsFilenameRe, body))
	}
	if len(show.episodes) == 0 {
		// Плейлиста нет — у тайтла одна серия (фильм или спешл).
		show.episodes = []anividsEpisode{{num: 1, hash: hash}}
	}
	a.storeShow(target, show)
	return show, true
}

func (a *anidubChecker) storeShow(target string, show *anidubShow) {
	a.mu.Lock()
	if len(a.shows) > 512 {
		now := time.Now()
		for k, v := range a.shows {
			if now.After(v.expires) {
				delete(a.shows, k)
			}
		}
	}
	a.shows[target] = show
	a.mu.Unlock()
}

func (a *anidubChecker) cachedProbe(target string) (bool, bool) {
	a.mu.RLock()
	probe, ok := a.probes[target]
	a.mu.RUnlock()
	if !ok || time.Now().After(probe.expires) {
		return false, false
	}
	return probe.playable, true
}

func (a *anidubChecker) storeProbe(target string, playable bool) {
	ttl := anidubShowTTL
	if !playable {
		ttl = anidubNotFoundTTL
	}
	a.mu.Lock()
	if len(a.probes) > 2048 {
		now := time.Now()
		for k, v := range a.probes {
			if now.After(v.expires) {
				delete(a.probes, k)
			}
		}
	}
	a.probes[target] = anidubProbe{playable: playable, expires: time.Now().Add(ttl)}
	a.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Хосты и загрузка
// ---------------------------------------------------------------------------

func (a *anidubChecker) playerHost() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.player != "" {
		return a.player
	}
	return anividsDefaultPlayer
}

func (a *anidubChecker) rememberPlayer(host string) {
	host = strings.TrimRight(strings.TrimSpace(host), "/")
	if host == "" {
		return
	}
	a.mu.RLock()
	same := a.player == host
	a.mu.RUnlock()
	if same {
		return
	}
	a.mu.Lock()
	a.player = host
	a.mu.Unlock()
	log.Info().Str("player", host).Msg("anidub: player host updated")
}

// sanitizeTitleURL пропускает только адреса тайтлов на своём хосте: ?u= придёт
// с нашей же страницы сезонов, и превращать его в произвольный запрос нельзя.
func (a *anidubChecker) sanitizeTitleURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 300 {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", false
	}
	self, err := url.Parse(a.host)
	if err != nil || !strings.EqualFold(u.Host, self.Host) {
		return "", false
	}
	if !anidubPathRe.MatchString(strings.ToLower(u.Path)) {
		return "", false
	}
	return u.Scheme + "://" + u.Host + u.Path, true
}

func (a *anidubChecker) fetch(ctx context.Context, target, referer string) (string, bool) {
	if a.gate != nil {
		select {
		case a.gate <- struct{}{}:
			defer func() { <-a.gate }()
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

	resp, err := balancerDoWithRetry(ctx, a.client, req, 1)
	if err != nil {
		log.Debug().Err(err).Str("url", truncURL(target)).Msg("anidub: fetch error")
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Debug().Int("status", resp.StatusCode).Str("url", truncURL(target)).Msg("anidub: non-2xx")
		return "", false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", false
	}
	return string(body), true
}
