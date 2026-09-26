package litesrc

import (
	stdjson "encoding/json"
	"fmt"
	"html"
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

// zagonka — плеерная страница kinobadi.in (наводка Дмитрия, 20.09.2026).
//
// Один запрос GET /player/player.php?kp_id=<кинопоиск> отдаёт страницу с
// атрибутом data-sources: там уже лежит ВСЁ — для фильма озвучки с
// качествами, для сериала сезоны → озвучки → серии → качества. Отдельных
// запросов за сезоном или серией нет, поэтому страница кэшируется целиком.
//
// Чем ценен: классические авторские озвучки (Живов, Гаврилов, Визгунов,
// Сербин, Гоблин, РТР), которых у наших источников почти нет, плюс сериалы с
// несколькими озвучками. Потолок — 720p (замер по «Матрице», «Игре
// престолов» и «Дюне 2»: везде 240–720p), поэтому в бейдже HD, не FHD.
//
// Что проверено с сервера: манифест отвечает 302 на skullium.mpa.…, сегменты
// относительные (./720.mp4:hls:seg-N.ts) и отдаются 206 video/MP2T как с
// Referer, так и без него; к IP ссылка НЕ привязана (снятая с мака играет с
// сервера). Срок — в пути ссылки, «:ГГГГММДДЧЧ» (сутки), поэтому кэш держим
// короче, а перед выдачей потока сверяем штамп и при нужде перечитываем
// страницу.
//
// Существующий балансер kinobadi ходит на vip.kinobadi.im (femd, только
// фильмы, FHD) — это ДРУГОЙ плеер той же сети, и он остаётся как есть.

const (
	zagonkaLookupTTL   = 3 * time.Hour
	zagonkaNotFoundTTL = 30 * time.Minute
	zagonkaBodyLimit   = 4 << 20 // страница сериала на 8 сезонов — 250 КБ, с запасом
)

var (
	zagonkaSourcesRe = regexp.MustCompile(`data-sources="([^"]+)"`)
	// «:2026092113/» в пути потока — час, до которого ссылка жива.
	zagonkaStampRe = regexp.MustCompile(`:(\d{10})/`)
)

type zagonkaData struct {
	Kind    string          `json:"kind"`
	Voices  []zagonkaVoice  `json:"voices"`
	Seasons []zagonkaSeason `json:"seasons"`
}

type zagonkaSeason struct {
	Num    int            `json:"num"`
	Voices []zagonkaVoice `json:"voices"`
}

type zagonkaVoice struct {
	Name      string            `json:"name"`
	Qualities map[string]string `json:"qualities"`
	Episodes  []zagonkaEpisode  `json:"episodes"`
}

type zagonkaEpisode struct {
	Num       int               `json:"num"`
	Qualities map[string]string `json:"qualities"`
}

type zagonkaEntry struct {
	data    *zagonkaData // nil — карточки нет
	expires time.Time
}

type zagonkaChecker struct {
	client *http.Client
	host   string

	mu    sync.RWMutex
	cache map[int64]zagonkaEntry
}

func NewZagonkaChecker(cfg config.Config) *zagonkaChecker {
	host := strings.TrimRight(strings.TrimSpace(cfg.Online.Zagonka.Host), "/")
	if host == "" {
		host = "https://kinobadi.in"
	}
	return &zagonkaChecker{
		client: httpclient.NewForBalancer("zagonka", 20*time.Second),
		host:   host,
		cache:  make(map[int64]zagonkaEntry, 256),
	}
}

func (z *zagonkaChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		raw := strings.Trim(strings.TrimPrefix(strings.ToLower(req.URL.Path), "/lite/"), "/")
		switch raw {
		case "zagonka":
			if parseBoolParam(req.URL.Query().Get("checksearch")) {
				writeCheckSearchResponse(w, z.checkSearch(req), pluginQualityBadgeGet("zagonka"))
				return
			}
			z.index(w, req, links)
		case "zagonka/serial":
			z.serial(w, req)
		case "zagonka/play":
			z.play(w, req, links)
		default:
			writeJSON(w, http.StatusNotImplemented, map[string]any{
				"error":    "zagonka route not implemented",
				"balanser": raw,
			})
		}
	}
}

// checkSearch — есть ли карточка нужного ВИДА. Страница по kp сериала и по kp
// фильма выглядит одинаково, различает их только kind; запрос с serial=1 на
// фильм (и наоборот) — промах, а не находка.
func (z *zagonkaChecker) checkSearch(req *http.Request) bool {
	kpID := kinobadiParseKpID(req.URL.Query())
	if kpID == 0 {
		return false
	}
	data, ok := z.lookup(req, kpID, false)
	if !ok {
		return false
	}
	if rudubIsSerial(req.URL.Query()) {
		return data.Kind == "series" && len(zagonkaVoiceNames(data)) > 0
	}
	return data.Kind == "movie" && len(data.Voices) > 0
}

// ---------------------------------------------------------------------------
// index — фильм: строки по озвучкам; сериал: выбор озвучки или сезоны
// ---------------------------------------------------------------------------

func (z *zagonkaChecker) index(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	kpID := kinobadiParseKpID(q)
	if kpID == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	data, ok := z.lookup(req, kpID, false)
	if !ok {
		writeGetsTVEmpty(w, rjson)
		return
	}
	title := getsTVJoinName(strings.TrimSpace(q.Get("title")), strings.TrimSpace(q.Get("original_title")))

	if data.Kind != "series" {
		z.movieRows(w, req, links, data, title, rjson)
		return
	}
	z.seriesEntry(w, req, data, kpID, title, rjson)
}

func (z *zagonkaChecker) movieRows(w http.ResponseWriter, req *http.Request, links *proxylink.Manager, data *zagonkaData, title string, rjson bool) {
	rows := make([]map[string]any, 0, len(data.Voices))
	for _, v := range data.Voices {
		row := z.playRow(req, links, v.Qualities, v.Name, title)
		if row == nil {
			continue
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "movie", "data": rows})
		return
	}
	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range rows {
		getsTVAppendMovieHTML(&sb, row, toString(row["name"]), i == 0, 0, 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// seriesEntry — сериал с несколькими озвучками начинается с выбора озвучки
// (как у ahuerezka), с одной — сразу с сезонов. Озвучки у сериала лежат
// ВНУТРИ сезонов и могут различаться от сезона к сезону, поэтому наверх
// поднимается объединение имён, а сезоны потом фильтруются по выбранной.
// seriesEntry — вход в сериал: сезоны активной озвучки и переключатель озвучек
// над ними. Лампа показывает контент только из элементов videos__item, а кнопки
// videos__button считает переключателем — прежний вход из одних кнопок она
// принимала за пустой ответ («поиск не дал результатов»).
func (z *zagonkaChecker) seriesEntry(w http.ResponseWriter, req *http.Request, data *zagonkaData, kpID int64, title string, rjson bool) {
	names := zagonkaVoiceNames(data)
	if len(names) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	q := req.URL.Query()
	active := names[0]
	if want := strings.TrimSpace(q.Get("v")); want != "" {
		for _, n := range names {
			if n == want {
				active = n
				break
			}
		}
	}
	rows, labels := z.buildSeasonRows(req, data, kpID, active, title, rjson)

	host := hostFromRequest(req)
	voices := make([]map[string]any, 0, len(names))
	for _, name := range names {
		link := host + "/lite/zagonka?kinopoisk_id=" + strconv.FormatInt(kpID, 10) +
			"&serial=1&v=" + url.QueryEscape(name) +
			"&title=" + url.QueryEscape(strings.TrimSpace(q.Get("title"))) +
			"&original_title=" + url.QueryEscape(strings.TrimSpace(q.Get("original_title")))
		if rjson {
			link += "&rjson=true"
		}
		voices = append(voices, map[string]any{
			"method": "link",
			"url":    link,
			"name":   name,
			"active": name == active,
		})
	}
	zagonkaWriteSeasons(w, rjson, rows, labels, voices)
}

// serial — сезоны выбранной озвучки (без s) или серии сезона (с s).
func (z *zagonkaChecker) serial(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	kpID := kinobadiParseKpID(q)
	voice := strings.TrimSpace(q.Get("v"))
	title := strings.TrimSpace(q.Get("t"))
	if kpID == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	data, ok := z.lookup(req, kpID, false)
	if !ok || data.Kind != "series" {
		writeGetsTVEmpty(w, rjson)
		return
	}
	if voice == "" {
		names := zagonkaVoiceNames(data)
		if len(names) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}
		voice = names[0]
	}
	seasonNum, _ := strconv.Atoi(strings.TrimSpace(q.Get("s")))
	if seasonNum <= 0 {
		z.seasonRows(w, req, data, kpID, voice, title, rjson)
		return
	}
	z.episodeRows(w, req, data, kpID, voice, seasonNum, title, rjson)
}

func (z *zagonkaChecker) seasonRows(w http.ResponseWriter, req *http.Request, data *zagonkaData, kpID int64, voice, title string, rjson bool) {
	rows, labels := z.buildSeasonRows(req, data, kpID, voice, title, rjson)
	zagonkaWriteSeasons(w, rjson, rows, labels, nil)
}

// buildSeasonRows — сезоны, в которых есть озвучка voice.
func (z *zagonkaChecker) buildSeasonRows(req *http.Request, data *zagonkaData, kpID int64, voice, title string, rjson bool) ([]map[string]any, []string) {
	host := hostFromRequest(req)
	rows := make([]map[string]any, 0, len(data.Seasons))
	labels := make([]string, 0, len(data.Seasons))
	for _, s := range data.Seasons {
		if zagonkaFindVoice(s, voice) == nil {
			continue
		}
		label := strconv.Itoa(s.Num) + " сезон"
		link := host + "/lite/zagonka/serial?kinopoisk_id=" + strconv.FormatInt(kpID, 10) +
			"&v=" + url.QueryEscape(voice) + "&s=" + strconv.Itoa(s.Num) + "&t=" + url.QueryEscape(title)
		if rjson {
			link += "&rjson=true"
		}
		// Номер сезона и в «s», и в «id»: capi-дрилл читает одно из двух в
		// зависимости от формы (см. rudub).
		rows = append(rows, map[string]any{
			"method": "link",
			"url":    link,
			"name":   label,
			"id":     s.Num,
			"s":      s.Num,
		})
		labels = append(labels, label)
	}
	return rows, labels
}

// zagonkaWriteSeasons — сезоны с необязательным переключателем озвучек над ними.
func zagonkaWriteSeasons(w http.ResponseWriter, rjson bool, rows []map[string]any, labels []string, voices []map[string]any) {
	if len(rows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	if rjson {
		payload := map[string]any{"type": "season", "data": rows}
		if len(voices) > 0 {
			payload["voice"] = voices
		}
		writeJSON(w, http.StatusOK, payload)
		return
	}
	var sb strings.Builder
	if len(voices) > 1 {
		sb.WriteString(`<div class="videos__line">`)
		for _, v := range voices {
			getsTVAppendVoiceHTML(&sb, v)
		}
		sb.WriteString(`</div>`)
	}
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range rows {
		getsTVAppendSeasonHTML(&sb, row, labels[i], i == 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (z *zagonkaChecker) episodeRows(w http.ResponseWriter, req *http.Request, data *zagonkaData, kpID int64, voice string, seasonNum int, title string, rjson bool) {
	var season *zagonkaSeason
	for i := range data.Seasons {
		if data.Seasons[i].Num == seasonNum {
			season = &data.Seasons[i]
			break
		}
	}
	if season == nil {
		writeGetsTVEmpty(w, rjson)
		return
	}
	v := zagonkaFindVoice(*season, voice)
	if v == nil || len(v.Episodes) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	host := hostFromRequest(req)
	rows := make([]map[string]any, 0, len(v.Episodes))
	labels := make([]string, 0, len(v.Episodes))
	nums := make([]int, 0, len(v.Episodes))
	for _, ep := range v.Episodes {
		label := strconv.Itoa(ep.Num) + " серия"
		link := host + "/lite/zagonka/play?kinopoisk_id=" + strconv.FormatInt(kpID, 10) +
			"&v=" + url.QueryEscape(voice) + "&s=" + strconv.Itoa(seasonNum) +
			"&e=" + strconv.Itoa(ep.Num) + "&t=" + url.QueryEscape(title)
		rows = append(rows, map[string]any{
			"method":    "call",
			"url":       link,
			"title":     label,
			"translate": voice,
			"s":         seasonNum,
			"e":         ep.Num,
		})
		labels = append(labels, label)
		nums = append(nums, ep.Num)
	}
	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "episode", "data": rows})
		return
	}
	var sb strings.Builder
	// Фильтр «Перевод» Лампа строит из кнопок ТОЛЬКО на уровне серий (там, где
	// есть что играть) — на уровне сезонов кнопки она пропускает. Поэтому
	// озвучки обязаны ехать здесь: те, у которых есть этот же сезон; кнопка
	// ведёт на тот же сезон в другой озвучке.
	if voices := z.seasonVoices(req, data, kpID, seasonNum, voice, title); len(voices) > 1 {
		sb.WriteString(`<div class="videos__line">`)
		for _, v := range voices {
			getsTVAppendVoiceHTML(&sb, v)
		}
		sb.WriteString(`</div>`)
	}
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range rows {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, seasonNum, nums[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// seasonVoices — кнопки озвучек, у которых есть сезон seasonNum.
func (z *zagonkaChecker) seasonVoices(req *http.Request, data *zagonkaData, kpID int64, seasonNum int, active, title string) []map[string]any {
	host := hostFromRequest(req)
	var out []map[string]any
	for _, s := range data.Seasons {
		if s.Num != seasonNum {
			continue
		}
		for _, v := range s.Voices {
			name := strings.TrimSpace(v.Name)
			if name == "" || len(v.Episodes) == 0 {
				continue
			}
			out = append(out, map[string]any{
				"method": "link",
				"url": host + "/lite/zagonka/serial?kinopoisk_id=" + strconv.FormatInt(kpID, 10) +
					"&v=" + url.QueryEscape(name) + "&s=" + strconv.Itoa(seasonNum) + "&t=" + url.QueryEscape(title),
				"name":   name,
				"active": name == active,
			})
		}
	}
	return out
}

// play — поток серии. Страницу перечитываем, если штамп в ссылке протух:
// ссылки живут сутки, а кэш — три часа, но карточка могла попасть в кэш за
// час до полуночи.
func (z *zagonkaChecker) play(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	kpID := kinobadiParseKpID(q)
	voice := strings.TrimSpace(q.Get("v"))
	seasonNum, _ := strconv.Atoi(strings.TrimSpace(q.Get("s")))
	epNum, _ := strconv.Atoi(strings.TrimSpace(q.Get("e")))
	title := strings.TrimSpace(q.Get("t"))
	if kpID == 0 || seasonNum <= 0 || epNum <= 0 {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	pick := func(data *zagonkaData) map[string]string {
		for _, s := range data.Seasons {
			if s.Num != seasonNum {
				continue
			}
			v := zagonkaFindVoice(s, voice)
			if v == nil {
				return nil
			}
			for _, ep := range v.Episodes {
				if ep.Num == epNum {
					return ep.Qualities
				}
			}
		}
		return nil
	}
	data, ok := z.lookup(req, kpID, false)
	if !ok || data.Kind != "series" {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	qualities := pick(data)
	if zagonkaExpired(qualities) {
		if fresh, ok := z.lookup(req, kpID, true); ok {
			qualities = pick(fresh)
		}
	}
	row := z.playRow(req, links, qualities, voice, title)
	if row == nil {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	writeJSON(w, http.StatusOK, row)
}

// playRow — одна строка воспроизведения: лучшее качество в url/stream,
// остальные — картой quality (как у anivids). Пустые качества — nil.
func (z *zagonkaChecker) playRow(req *http.Request, links *proxylink.Manager, qualities map[string]string, voice, title string) map[string]any {
	labels := zagonkaSortedQualities(qualities)
	if len(labels) == 0 {
		return nil
	}
	headers := map[string]string{"Referer": z.host + "/"}
	qualityMap := make(map[string]string, len(labels))
	streams := make([]map[string]any, 0, len(labels))
	for _, label := range labels {
		u := streamProxyURLWithHeaders(req, qualities[label], "zagonka", links, headers)
		qualityMap[label] = u
		streams = append(streams, map[string]any{"quality": label, "url": u})
	}
	best := qualityMap[labels[0]]
	row := map[string]any{
		"method":        "play",
		"url":           best,
		"stream":        best,
		"name":          voice,
		"translate":     voice,
		"quality":       qualityMap,
		"streamquality": streams,
	}
	if title != "" && len(title) <= 200 {
		row["title"] = title
	}
	return row
}

// ---------------------------------------------------------------------------
// Страница и кэш
// ---------------------------------------------------------------------------

func (z *zagonkaChecker) lookup(req *http.Request, kpID int64, force bool) (*zagonkaData, bool) {
	if !force {
		z.mu.RLock()
		entry, ok := z.cache[kpID]
		z.mu.RUnlock()
		if ok && time.Now().Before(entry.expires) {
			return entry.data, entry.data != nil
		}
	}
	data := z.fetch(req, kpID)
	ttl := zagonkaLookupTTL
	if data == nil {
		ttl = zagonkaNotFoundTTL
	}
	z.mu.Lock()
	z.cache[kpID] = zagonkaEntry{data: data, expires: time.Now().Add(ttl)}
	z.mu.Unlock()
	return data, data != nil
}

func (z *zagonkaChecker) fetch(req *http.Request, kpID int64) *zagonkaData {
	target := fmt.Sprintf("%s/player/player.php?kp_id=%d", z.host, kpID)
	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, target, nil)
	if err != nil {
		return nil
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36")
	httpReq.Header.Set("Accept", "text/html,*/*;q=0.8")
	httpReq.Header.Set("Referer", z.host+"/")

	resp, err := balancerDoWithRetry(req.Context(), z.client, httpReq, 2)
	if err != nil {
		log.Debug().Err(err).Int64("kp", kpID).Msg("zagonka: страница не отвечает")
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Debug().Int("status", resp.StatusCode).Int64("kp", kpID).Msg("zagonka: не 2xx")
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, zagonkaBodyLimit))
	if err != nil {
		return nil
	}
	data := zagonkaParsePage(string(body))
	if data == nil {
		log.Debug().Int64("kp", kpID).Int("len", len(body)).Msg("zagonka: на странице нет data-sources")
	}
	return data
}

// zagonkaParsePage вынимает data-sources и разбирает его. Возвращает nil,
// когда атрибута нет или карточка пуста (страница «нет такого фильма»
// выглядит как обычная, но без озвучек и сезонов).
func zagonkaParsePage(page string) *zagonkaData {
	m := zagonkaSourcesRe.FindStringSubmatch(page)
	if len(m) < 2 {
		return nil
	}
	var data zagonkaData
	if err := stdjson.Unmarshal([]byte(html.UnescapeString(m[1])), &data); err != nil {
		return nil
	}
	switch data.Kind {
	case "movie":
		if len(data.Voices) == 0 {
			return nil
		}
	case "series":
		if len(zagonkaVoiceNames(&data)) == 0 {
			return nil
		}
	default:
		return nil
	}
	return &data
}

// zagonkaVoiceNames — объединение озвучек по всем сезонам в порядке первого
// появления (сезон 1 задаёт порядок; «Rezka» у kinobadi обычно первая).
func zagonkaVoiceNames(data *zagonkaData) []string {
	seen := make(map[string]bool)
	var out []string
	for _, s := range data.Seasons {
		for _, v := range s.Voices {
			name := strings.TrimSpace(v.Name)
			if name == "" || seen[name] || len(v.Episodes) == 0 {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

func zagonkaFindVoice(s zagonkaSeason, name string) *zagonkaVoice {
	for i := range s.Voices {
		if strings.EqualFold(strings.TrimSpace(s.Voices[i].Name), strings.TrimSpace(name)) {
			return &s.Voices[i]
		}
	}
	return nil
}

// zagonkaSortedQualities — метки от лучшей к худшей: «720p» > «480p» > …
func zagonkaSortedQualities(q map[string]string) []string {
	labels := make([]string, 0, len(q))
	for label, u := range q {
		if strings.TrimSpace(u) == "" {
			continue
		}
		labels = append(labels, label)
	}
	sort.Slice(labels, func(i, j int) bool {
		return zagonkaQualityRank(labels[i]) > zagonkaQualityRank(labels[j])
	})
	return labels
}

func zagonkaQualityRank(label string) int {
	digits := strings.TrimRight(strings.TrimSpace(label), "pP")
	n, _ := strconv.Atoi(digits)
	return n
}

// zagonkaExpired — протух ли штамп «:ГГГГММДДЧЧ» хотя бы в одной ссылке.
// Штамп без часового пояса; сравниваем по UTC с запасом в час — лучше лишний
// раз перечитать страницу, чем отдать зрителю мёртвую ссылку.
func zagonkaExpired(q map[string]string) bool {
	now := time.Now().UTC().Add(time.Hour)
	for _, u := range q {
		m := zagonkaStampRe.FindStringSubmatch(u)
		if len(m) < 2 {
			continue
		}
		ts, err := time.Parse("2006010215", m[1])
		if err != nil {
			continue
		}
		if ts.Before(now) {
			return true
		}
	}
	return false
}
