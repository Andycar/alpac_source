package litesrc

// smotrim — «Смотрим» (ВГТРК): архив телеканалов холдинга, сериалы, фильмы и
// программы. Легальная площадка вещателя, и источник работает ТОЛЬКО с той
// частью каталога, которую она раздаёт открыто.
//
// Цепочка:
//
//	GET /sitemap                     -> индекс брендов: «/brand/{id}» + «Название (год)»
//	GET /brand/{id}                  -> ссылки «/brand/{id}/season-{n}» = список сезонов
//	GET /brand/{id}/season-{n}       -> «/video/{id}» серий; ПОРЯДОК на странице совпадает
//	                                    с номерами серий (проверено на живых страницах)
//	GET {playerAPI}/api/v1/video/{id}
//	                                 -> status + streams.m3u8 + qualities + subtitles
//
// Платная часть каталога (премьеры) отмечена явно: `status` приходит
// `VIDEO_DISALLOW_TARIFF`, `episode.tariff` — `subscription_content`, и ссылок
// на медиа в ответе просто нет. Такие серии источник пропускает: открывать
// подписочный контент он не умеет и не должен. Открытый архив отвечает
// `status: OK` и `tariff: public_content` — с ним и работаем.
//
// Поток отдаётся без каких-либо заголовков (проверено: плейлист открывается и
// без User-Agent, и без Referer), подпись зашита в сам URL.

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
	smotrimDefaultHost      = "https://smotrim.ru"
	smotrimDefaultPlayerAPI = "https://player-api.smotrim.ru"

	smotrimIndexTTL    = 6 * time.Hour
	smotrimShowTTL     = 90 * time.Minute
	smotrimNotFoundTTL = 30 * time.Minute
	smotrimMaxInflight = 12
)

var (
	// Строка индекса: «<a href="/brand/60800">Челночницы сериал 2016…</a>».
	smotrimIndexRowRe = regexp.MustCompile(`href="/brand/(\d+)"[^>]*>([^<]{2,120})<`)
	// Год в названии индекса: «Салями (2011)», «Екатерина (2014-2019)».
	smotrimIndexYearRe = regexp.MustCompile(`\((\d{4})`)
	// Сезоны бренда.
	smotrimSeasonLinkRe = regexp.MustCompile(`/brand/\d+/season-(\d+)`)
	// Серии сезона.
	smotrimVideoRe = regexp.MustCompile(`/video/(\d+)`)
	// Идентификаторы, которые нам отдают наши же ссылки.
	smotrimIDRe = regexp.MustCompile(`^\d{1,12}$`)
	// Варианты master-плейлиста: размечены полосой, без RESOLUTION.
	smotrimStreamInfRe = regexp.MustCompile(`#EXT-X-STREAM-INF:[^\n]*BANDWIDTH=(\d+)[^\n]*\n([^\n\r]+)`)
)

// smotrimJunkWords — слова, которыми индекс «Смотрим» обрастает вокруг
// названия: «Сериал Овчарка 2024 смотреть онлайн бесплатно». Их надо снять,
// иначе ни одно название не совпадёт с карточкой.
var smotrimJunkWords = []string{
	"сериал", "фильм", "программа", "шоу", "мультфильм", "документальный",
	"смотреть", "онлайн", "бесплатно", "хорошем", "качестве", "все", "серии",
	"сезоны", "выпуски", "на", "в",
}

type smotrimChecker struct {
	client    *http.Client
	host      string
	playerAPI string

	full *smotrimIndexStore // полный индекс каталога (диск + фон)

	mu           sync.RWMutex
	brandIndex   []smotrimBrand
	indexExpires time.Time
	indexLoading sync.Mutex
	shows        map[string]*smotrimShow
	probes       map[string]smotrimProbe

	gate chan struct{}
}

type smotrimBrand struct {
	id    string
	title string // очищенное название
	raw   string // строка индекса как есть — для диагностики
	year  int
}

type smotrimShow struct {
	seasons []int
	expires time.Time
}

type smotrimProbe struct {
	playable bool
	expires  time.Time
}

// smotrimVideoResp — ответ плеерного API (только то, что нам нужно).
type smotrimVideoResp struct {
	Status string `json:"status"`
	Data   struct {
		Streams struct {
			M3U8 string `json:"m3u8"`
		} `json:"streams"`
		Qualities []struct {
			Name      string `json:"name"`
			Bandwidth int64  `json:"bandwidth"`
		} `json:"qualities"`
		Subtitles []struct {
			Code  string `json:"code"`
			Title string `json:"title"`
			VTT   string `json:"vtt"`
		} `json:"subtitles"`
		Episode struct {
			Number int    `json:"number"`
			Tariff string `json:"tariff"`
			Season struct {
				Number int `json:"number"`
			} `json:"season"`
		} `json:"episode"`
		Brand struct {
			Title string `json:"title"`
			Type  string `json:"type"`
		} `json:"brand"`
	} `json:"data"`
}

func NewSmotrimChecker(cfg config.Config) *smotrimChecker {
	host := strings.TrimSpace(strings.TrimRight(cfg.Online.Smotrim.Host, "/"))
	if host == "" {
		host = smotrimDefaultHost
	}
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}
	playerAPI := strings.TrimSpace(strings.TrimRight(cfg.Online.Smotrim.PlayerAPI, "/"))
	if playerAPI == "" {
		playerAPI = smotrimDefaultPlayerAPI
	}
	c := &smotrimChecker{
		client:    httpclient.NewForBalancer("smotrim", 15*time.Second),
		host:      host,
		playerAPI: playerAPI,
		shows:     make(map[string]*smotrimShow, 128),
		probes:    make(map[string]smotrimProbe, 256),
		gate:      make(chan struct{}, smotrimMaxInflight),
	}
	c.full = newSmotrimIndexStore(cfg.Compat.RepoRoot, host, c.client, c.fetch)
	return c
}

// ---------------------------------------------------------------------------
// HTTP entry point
// ---------------------------------------------------------------------------

func (s *smotrimChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			writeCheckSearchResponseNoRCH(w, s.checkSearch(req), pluginQualityBadgeGet("smotrim"))
			return
		}

		raw := strings.TrimPrefix(strings.ToLower(req.URL.Path), "/lite/")
		switch {
		case strings.HasPrefix(raw, "smotrim/play"):
			s.play(w, req, links)
		case strings.HasPrefix(raw, "smotrim/serial"):
			s.serial(w, req)
		default:
			s.index(w, req)
		}
	}
}

// checkSearch: индекс (обычно из кэша) плюс проверка первой серии. Проверять
// приходится по-настоящему — премьеры площадки закрыты подпиской, и без пробы
// источник светился бы на карточках, где ничего показать не может.
func (s *smotrimChecker) checkSearch(req *http.Request) bool {
	q := req.URL.Query()
	brand, ok := s.pickBrand(req.Context(), q)
	if !ok {
		return false
	}
	if playable, ok := s.cachedProbe(brand.id); ok {
		return playable
	}

	playable := false
	if videos, ok := s.seasonVideos(req.Context(), brand.id, 1); ok && len(videos) > 0 {
		if _, ok := s.resolveVideo(req.Context(), videos[0]); ok {
			playable = true
		}
	}
	s.storeProbe(brand.id, playable)
	return playable
}

func (s *smotrimChecker) index(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))

	brand, ok := s.pickBrand(req.Context(), q)
	if !ok {
		writeGetsTVEmpty(w, rjson)
		return
	}
	host := hostFromRequest(req)
	cardTitle := getsTVJoinName(strings.TrimSpace(q.Get("title")), strings.TrimSpace(q.Get("original_title")))
	if cardTitle == "" {
		cardTitle = brand.title
	}

	// Фильм: спускаться некуда — отдаём первое доступное видео бренда.
	if strings.TrimSpace(q.Get("serial")) != "1" {
		videos, ok := s.seasonVideos(req.Context(), brand.id, 1)
		if !ok || len(videos) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}
		row := map[string]any{
			"method":    "call",
			"url":       s.playLink(host, videos[0], cardTitle, rjson),
			"title":     cardTitle,
			"translate": "Смотрим",
		}
		if rjson {
			writeJSON(w, http.StatusOK, map[string]any{"type": "movie", "data": []map[string]any{row}})
			return
		}
		var sb strings.Builder
		sb.WriteString(`<div class="videos__line">`)
		getsTVAppendMovieHTML(&sb, row, "Смотрим", true, 0, 0)
		sb.WriteString(`</div>`)
		writeHTML(w, http.StatusOK, sb.String())
		return
	}

	show, ok := s.resolveShow(req.Context(), brand.id)
	if !ok || len(show.seasons) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	data := make([]map[string]any, 0, len(show.seasons))
	labels := make([]string, 0, len(show.seasons))
	for _, num := range show.seasons {
		label := strconv.Itoa(num) + " сезон"
		link := host + "/lite/smotrim/serial?b=" + url.QueryEscape(brand.id) + "&s=" + strconv.Itoa(num)
		if cardTitle != "" {
			link += "&t=" + url.QueryEscape(cardTitle)
		}
		if rjson {
			link += "&rjson=true"
		}
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

func (s *smotrimChecker) serial(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))

	brandID := strings.TrimSpace(q.Get("b"))
	season, _ := strconv.Atoi(strings.TrimSpace(q.Get("s")))
	if !smotrimIDRe.MatchString(brandID) || season <= 0 || season > 99 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	videos, ok := s.seasonVideos(req.Context(), brandID, season)
	if !ok || len(videos) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(req)
	cardTitle := strings.TrimSpace(q.Get("t"))
	data := make([]map[string]any, 0, len(videos))
	labels := make([]string, 0, len(videos))
	nums := make([]int, 0, len(videos))
	for i, vid := range videos {
		num := i + 1 // порядок на странице сезона совпадает с номерами серий
		label := strconv.Itoa(num) + " серия"
		data = append(data, map[string]any{
			"method":    "call",
			"url":       s.playLink(host, vid, cardTitle, false),
			"title":     label,
			"translate": "Смотрим",
			"s":         season,
			"e":         num,
		})
		labels = append(labels, label)
		nums = append(nums, num)
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

func (s *smotrimChecker) play(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	videoID := strings.TrimSpace(q.Get("v"))
	if !smotrimIDRe.MatchString(videoID) {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	resp, ok := s.resolveVideo(req.Context(), videoID)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	master := strings.TrimSpace(resp.Data.Streams.M3U8)
	if master == "" {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	stream := streamProxyURL(req, master, "smotrim", links)

	// Варианты master-плейлиста размечены только полосой, без RESOLUTION, зато
	// API отдаёт таблицу «качество → bandwidth» — по ней и подписываем.
	variants := map[int64]string{}
	if body, ok := s.fetch(req.Context(), master, "https://player.smotrim.ru/"); ok {
		variants = smotrimParseMaster(body, master)
	}
	streams := make([]map[string]any, 0, len(resp.Data.Qualities))
	qualityMap := make(map[string]string, len(resp.Data.Qualities))
	for _, q := range resp.Data.Qualities {
		label := strings.TrimSpace(q.Name)
		if label == "" {
			continue
		}
		u := variants[q.Bandwidth]
		if u == "" {
			continue
		}
		proxied := streamProxyURL(req, u, "smotrim", links)
		streams = append(streams, map[string]any{"quality": label, "url": proxied})
		qualityMap[label] = proxied
	}
	sort.SliceStable(streams, func(i, j int) bool {
		return smotrimQualityRank(toString(streams[i]["quality"])) > smotrimQualityRank(toString(streams[j]["quality"]))
	})

	row := map[string]any{
		"method":    "play",
		"url":       stream,
		"stream":    stream,
		"name":      "Смотрим",
		"translate": "Смотрим",
	}
	if len(streams) > 0 {
		row["streamquality"] = streams
		row["quality"] = qualityMap
	}
	if title := strings.TrimSpace(q.Get("t")); title != "" && len(title) <= 200 {
		row["title"] = title
	}
	if subs := smotrimSubtitles(resp); len(subs) > 0 {
		row["subtitles"] = subs
	}
	writeJSON(w, http.StatusOK, row)
}

func (s *smotrimChecker) playLink(host, videoID, title string, rjson bool) string {
	link := host + "/lite/smotrim/play?v=" + url.QueryEscape(videoID)
	if title != "" {
		link += "&t=" + url.QueryEscape(title)
	}
	if rjson {
		link += "&rjson=true"
	}
	return link
}

func smotrimSubtitles(resp smotrimVideoResp) []map[string]string {
	out := make([]map[string]string, 0, len(resp.Data.Subtitles))
	for _, sub := range resp.Data.Subtitles {
		u := strings.TrimSpace(sub.VTT)
		if u == "" {
			continue
		}
		label := strings.TrimSpace(sub.Title)
		if label == "" {
			label = strings.TrimSpace(sub.Code)
		}
		if label == "" {
			label = "Субтитры"
		}
		out = append(out, map[string]string{"label": label, "url": u})
	}
	return out
}

// smotrimParseMaster разбирает master-плейлист в карту «полоса → URL».
// Варианты в нём относительные, но собственную подпись несут сами («?entity=…
// &sign=…»), поэтому достраивается только путь.
func smotrimParseMaster(body, masterURL string) map[int64]string {
	matches := smotrimStreamInfRe.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return nil
	}
	base := masterURL
	if idx := strings.Index(base, "?"); idx >= 0 {
		base = base[:idx]
	}
	if idx := strings.LastIndex(base, "/"); idx >= 0 {
		base = base[:idx+1]
	}

	out := make(map[int64]string, len(matches))
	for _, m := range matches {
		bw, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || bw <= 0 {
			continue
		}
		u := strings.TrimSpace(m[2])
		if u == "" || strings.HasPrefix(u, "#") {
			continue
		}
		if !strings.HasPrefix(u, "http") {
			u = base + u
		}
		if _, dup := out[bw]; !dup {
			out[bw] = u
		}
	}
	return out
}

func smotrimQualityRank(label string) int {
	n := 0
	for _, c := range label {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		} else if n > 0 {
			break
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// Индекс и подбор бренда
// ---------------------------------------------------------------------------

func (s *smotrimChecker) pickBrand(ctx context.Context, q url.Values) (smotrimBrand, bool) {
	title := strings.TrimSpace(q.Get("title"))
	orig := strings.TrimSpace(q.Get("original_title"))
	year, _ := strconv.Atoi(strings.TrimSpace(q.Get("year")))
	if title == "" && orig == "" {
		return smotrimBrand{}, false
	}
	brands, ok := s.ensureIndex(ctx)
	if !ok {
		return smotrimBrand{}, false
	}
	return smotrimPickBrand(brands, title, orig, year)
}

// smotrimPickBrand выбирает бренд по названию и году.
//
// Год — сильный признак: у площадки в индексе он стоит прямо в строке, а
// одноимённых экранизаций в архиве ВГТРК хватает («Анна Каренина» и подобные).
// Поэтому при совпадении названий выигрывает тот, у кого сходится год.
func smotrimPickBrand(brands []smotrimBrand, title, orig string, year int) (smotrimBrand, bool) {
	wantRu := normalizeSearchTitle(title)
	wantOrig := normalizeSearchTitle(orig)
	if wantRu == "" && wantOrig == "" {
		return smotrimBrand{}, false
	}

	var best smotrimBrand
	bestScore := 0
	for _, b := range brands {
		got := normalizeSearchTitle(b.title)
		if got == "" {
			continue
		}
		score := 0
		if wantRu != "" && got == wantRu {
			score += 3
		} else if wantOrig != "" && got == wantOrig {
			score += 2
		} else {
			continue
		}
		// Год — ЖЁСТКИЙ фильтр, когда его знают обе стороны. Названия в архиве
		// вещателя сплошь общие («Медведь», «Другие», «Следопыт»), и на одном
		// названии карточка Lost подбирала российский фильм 2018 года, а The
		// Bear — картину 1938-го. Штрафа мало: точное совпадение имени его
		// перевешивало, и источник отдавал чужое кино.
		if year > 0 && b.year > 0 {
			switch d := b.year - year; {
			case d == 0:
				score += 3
			case d > -2 && d < 2:
				score++
			default:
				continue
			}
		}
		if score > bestScore {
			best, bestScore = b, score
		}
	}
	return best, bestScore > 0
}

func (s *smotrimChecker) ensureIndex(ctx context.Context) ([]smotrimBrand, bool) {
	// Полный индекс (14 тысяч брендов) строится в фоне и живёт на диске.
	// Пока его нет, работаем на карте сайта — это 899 брендов, лучше, чем
	// ничего, и запрос не ждёт девятнадцатиминутного обхода.
	if s.full != nil {
		brands, fresh := s.full.Brands()
		if !fresh {
			s.full.EnsureFresh(ctx)
		}
		if len(brands) > 0 {
			return brands, true
		}
	}

	s.mu.RLock()
	brands, expires := s.brandIndex, s.indexExpires
	s.mu.RUnlock()
	if len(brands) > 0 && time.Now().Before(expires) {
		return brands, true
	}

	s.indexLoading.Lock()
	defer s.indexLoading.Unlock()

	s.mu.RLock()
	brands, expires = s.brandIndex, s.indexExpires
	s.mu.RUnlock()
	if len(brands) > 0 && time.Now().Before(expires) {
		return brands, true
	}

	body, ok := s.fetch(ctx, s.host+"/sitemap", s.host+"/")
	if !ok {
		if len(brands) > 0 {
			return brands, true // протухший индекс лучше пустого
		}
		return nil, false
	}
	parsed := smotrimParseIndex(body)
	if len(parsed) == 0 {
		if len(brands) > 0 {
			return brands, true
		}
		return nil, false
	}

	s.mu.Lock()
	s.brandIndex = parsed
	s.indexExpires = time.Now().Add(smotrimIndexTTL)
	s.mu.Unlock()

	log.Info().Int("brands", len(parsed)).Msg("smotrim: index loaded")
	return parsed, true
}

func smotrimParseIndex(html string) []smotrimBrand {
	matches := smotrimIndexRowRe.FindAllStringSubmatch(html, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]smotrimBrand, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		id := m[1]
		if _, dup := seen[id]; dup {
			continue
		}
		raw := strings.TrimSpace(m[2])
		title := smotrimCleanTitle(raw)
		if title == "" {
			continue
		}
		seen[id] = struct{}{}
		year, _ := strconv.Atoi(submatch1(smotrimIndexYearRe, raw))
		out = append(out, smotrimBrand{id: id, title: title, raw: raw, year: year})
	}
	return out
}

// smotrimCleanTitle снимает с названия витринную обвязку: «Сериал Овчарка 2024
// смотреть онлайн бесплатно» → «Овчарка». Без этого индекс не сходится с
// карточкой ни одной строкой.
func smotrimCleanTitle(raw string) string {
	s := raw
	if idx := strings.Index(s, "("); idx > 0 {
		s = s[:idx] // «Салями (2011)» — год уже разобран отдельно
	}
	s = strings.ReplaceAll(s, "&quot;", " ")
	s = strings.ReplaceAll(s, "&amp;", " ")

	fields := strings.Fields(normalizeSearchTitle(s))
	// Ведущее жанровое слово («Сериал», «Фильм», «Программа») и хвост
	// «2016 смотреть онлайн бесплатно» — обвязка витрины, а не имя. Снимать
	// нужно ОДНИМ циклом: год стоит посреди хвоста («Челночницы сериал 2016
	// смотреть онлайн»), и в два прохода за ним оставалось слово «сериал».
	for len(fields) > 1 {
		switch {
		case smotrimIsJunk(fields[0]):
			fields = fields[1:]
		case smotrimIsJunk(fields[len(fields)-1]):
			fields = fields[:len(fields)-1]
		case len(fields[len(fields)-1]) == 4 && isAllDigits(fields[len(fields)-1]):
			fields = fields[:len(fields)-1]
		default:
			return strings.Join(fields, " ")
		}
	}
	return strings.Join(fields, " ")
}

func smotrimIsJunk(word string) bool {
	for _, junk := range smotrimJunkWords {
		if word == junk {
			return true
		}
	}
	return false
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Сезоны и серии
// ---------------------------------------------------------------------------

func (s *smotrimChecker) cachedShow(brandID string) *smotrimShow {
	s.mu.RLock()
	show, ok := s.shows[brandID]
	s.mu.RUnlock()
	if !ok || time.Now().After(show.expires) {
		return nil
	}
	return show
}

func (s *smotrimChecker) resolveShow(ctx context.Context, brandID string) (*smotrimShow, bool) {
	if show := s.cachedShow(brandID); show != nil {
		return show, len(show.seasons) > 0
	}
	page, ok := s.fetch(ctx, s.host+"/brand/"+brandID, s.host+"/")
	if !ok {
		return &smotrimShow{}, false
	}
	seasons := smotrimParseSeasons(page)
	ttl := smotrimShowTTL
	if len(seasons) == 0 {
		ttl = smotrimNotFoundTTL
	}
	show := &smotrimShow{seasons: seasons, expires: time.Now().Add(ttl)}

	s.mu.Lock()
	if len(s.shows) > 512 {
		now := time.Now()
		for k, v := range s.shows {
			if now.After(v.expires) {
				delete(s.shows, k)
			}
		}
	}
	s.shows[brandID] = show
	s.mu.Unlock()

	return show, len(seasons) > 0
}

func smotrimParseSeasons(html string) []int {
	matches := smotrimSeasonLinkRe.FindAllStringSubmatch(html, -1)
	seen := make(map[int]struct{}, len(matches))
	out := make([]int, 0, len(matches))
	for _, m := range matches {
		n, err := strconv.Atoi(m[1])
		if err != nil || n <= 0 || n > 99 {
			continue
		}
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

// seasonVideos отдаёт идентификаторы серий сезона В ПОРЯДКЕ страницы — он
// совпадает с номерами серий (сверено с episode.number плеерного API).
func (s *smotrimChecker) seasonVideos(ctx context.Context, brandID string, season int) ([]string, bool) {
	target := s.host + "/brand/" + brandID + "/season-" + strconv.Itoa(season)
	page, ok := s.fetch(ctx, target, s.host+"/")
	if !ok {
		return nil, false
	}
	return smotrimParseVideos(page), true
}

func smotrimParseVideos(html string) []string {
	matches := smotrimVideoRe.FindAllStringSubmatch(html, -1)
	out := make([]string, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		id := m[1]
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func (s *smotrimChecker) cachedProbe(brandID string) (bool, bool) {
	s.mu.RLock()
	probe, ok := s.probes[brandID]
	s.mu.RUnlock()
	if !ok || time.Now().After(probe.expires) {
		return false, false
	}
	return probe.playable, true
}

func (s *smotrimChecker) storeProbe(brandID string, playable bool) {
	ttl := smotrimShowTTL
	if !playable {
		ttl = smotrimNotFoundTTL
	}
	s.mu.Lock()
	if len(s.probes) > 2048 {
		now := time.Now()
		for k, v := range s.probes {
			if now.After(v.expires) {
				delete(s.probes, k)
			}
		}
	}
	s.probes[brandID] = smotrimProbe{playable: playable, expires: time.Now().Add(ttl)}
	s.mu.Unlock()
}

// resolveVideo спрашивает плеерное API и пропускает только открытый контент.
func (s *smotrimChecker) resolveVideo(ctx context.Context, videoID string) (smotrimVideoResp, bool) {
	body, ok := s.fetch(ctx, s.playerAPI+"/api/v1/video/"+videoID, "https://player.smotrim.ru/")
	if !ok {
		return smotrimVideoResp{}, false
	}
	var resp smotrimVideoResp
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		log.Debug().Err(err).Str("video", videoID).Msg("smotrim: bad player json")
		return smotrimVideoResp{}, false
	}
	// Подписочный контент площадка отмечает сама и ссылок на медиа не даёт —
	// пропускаем, не пытаясь ничего обойти.
	if !strings.EqualFold(resp.Status, "OK") {
		return smotrimVideoResp{}, false
	}
	if tariff := strings.TrimSpace(resp.Data.Episode.Tariff); tariff != "" && tariff != "public_content" {
		return smotrimVideoResp{}, false
	}
	if strings.TrimSpace(resp.Data.Streams.M3U8) == "" {
		return smotrimVideoResp{}, false
	}
	return resp, true
}

// ---------------------------------------------------------------------------
// Fetch
// ---------------------------------------------------------------------------

func (s *smotrimChecker) fetch(ctx context.Context, target, referer string) (string, bool) {
	if s.gate != nil {
		select {
		case s.gate <- struct{}{}:
			defer func() { <-s.gate }()
		case <-ctx.Done():
			return "", false
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("User-Agent", anividsUA)
	req.Header.Set("Accept", "text/html,application/json,*/*;q=0.8")
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}

	resp, err := balancerDoWithRetry(ctx, s.client, req, 1)
	if err != nil {
		log.Debug().Err(err).Str("url", truncURL(target)).Msg("smotrim: fetch error")
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Debug().Int("status", resp.StatusCode).Str("url", truncURL(target)).Msg("smotrim: non-2xx")
		return "", false
	}
	// Страницы «Смотрим» тяжёлые: бренд с двумя сезонами — под 500 КБ.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", false
	}
	return string(body), true
}
