package litesrc

import (
	"bytes"
	"compress/gzip"
	stdjson "encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/proxylink"
)

// scts.go — нативный порт JS-модуля scts (community-каталог Sakhalin Cable TV).
//
// API сайта закрыт по IP-allowlist (AS51004), но CDN dl.yama.scts.tv открыт миру
// и отдаёт MP4 с Range. Балансер тянет thin-JSON (~2 МБ gzip) с нашего хаба,
// держит индексы В ПАМЯТИ процесса и отдаёт клиенту прямые CDN-URL — без
// проксей и без транзита видео через lampac. Порт из modules/scts/index.js:
// нативный дрилл отвечает из памяти (~1 мс против ~1 с goja-раунда).
//
// Slim-формат: {"v":1,"n":N,"items":[{"i":4,"n":"Таксист","y":1976,"t":103,
// "m":"tt0075314","f":[["file.mkv","http://dl.yama.scts.tv/..."]]},
// {"n":"Тайны Смолвиля (6 сезон)","k":"tv","s":6,...}]} — k="tv"/s=N только
// у записей-сезонов сериалов; t = tmdb id, m = imdb id.

type sctsItem struct {
	I int    `json:"i"`
	N string `json:"n"`
	Y int    `json:"y"`
	T int    `json:"t"`
	M string `json:"m"`
	K string `json:"k"`
	S int    `json:"s"`
	// f = [["file name","cdn url"], …]; a fixed-size [2]string element takes
	// exactly the name/url pair from each JSON tuple.
	Files [][2]string `json:"f"`
}

type sctsIndex struct {
	items      []sctsItem
	tmdbMovie  map[int]*sctsItem
	tmdbSeries map[int][]*sctsItem
	imdbMovie  map[string]*sctsItem
	imdbSeries map[string][]*sctsItem
	titleYear  map[string][]*sctsItem
	builtAt    time.Time
}

type sctsChecker struct {
	client     *http.Client
	catalogURL string
	refresh    time.Duration

	mu       sync.Mutex
	idx      *sctsIndex
	fetching bool
}

func NewSCTSChecker(cfg config.Config) *sctsChecker {
	cu := strings.TrimSpace(cfg.Online.SCTS.CatalogURL)
	if cu == "" {
		cu = "https://hub.alcopa.cc/scts/scts-slim.json.gz"
	}
	hours := cfg.Online.SCTS.RefreshHours
	if hours <= 0 {
		hours = 24
	}
	return &sctsChecker{
		client:     httpclient.New(60 * time.Second),
		catalogURL: cu,
		refresh:    time.Duration(hours) * time.Hour,
	}
}

var (
	sctsSharedOnce sync.Once
	sctsShared     *sctsChecker
)

// SharedSCTSChecker: каталог 18 МБ в памяти — один на процесс, переживает
// пересборки liteSourceHandler (hot-reload) как у SakhTV.
func SharedSCTSChecker(cfg config.Config) *sctsChecker {
	sctsSharedOnce.Do(func() { sctsShared = NewSCTSChecker(cfg) })
	return sctsShared
}

// ---------------------------------------------------------------------------
// Catalog load + indexes
// ---------------------------------------------------------------------------

func (c *sctsChecker) catalog() *sctsIndex {
	c.mu.Lock()
	idx := c.idx
	fresh := idx != nil && time.Since(idx.builtAt) < c.refresh
	if fresh || c.fetching {
		c.mu.Unlock()
		return idx // может быть nil на самом первом холодном запросе — это ок
	}
	c.fetching = true
	c.mu.Unlock()

	fetched := c.fetchCatalog()
	c.mu.Lock()
	c.fetching = false
	if fetched != nil {
		c.idx = fetched
	}
	idx = c.idx
	c.mu.Unlock()
	return idx
}

func (c *sctsChecker) fetchCatalog() *sctsIndex {
	req, err := http.NewRequest(http.MethodGet, c.catalogURL, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Lampac-Go", "1")
	resp, err := c.client.Do(req)
	if err != nil {
		log.Warn().Err(err).Str("url", c.catalogURL).Msg("scts: catalog fetch failed")
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Warn().Int("status", resp.StatusCode).Msg("scts: catalog bad status")
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil
	}
	// .json.gz может прийти и сырым gzip (magic 1f 8b), и уже распакованным
	// (Content-Encoding: gzip → Go-клиент распаковал прозрачно).
	if len(raw) > 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil
		}
		raw, err = io.ReadAll(io.LimitReader(zr, 128<<20))
		zr.Close()
		if err != nil {
			return nil
		}
	}
	var parsed struct {
		N     int        `json:"n"`
		Items []sctsItem `json:"items"`
	}
	if err := stdjson.Unmarshal(raw, &parsed); err != nil {
		log.Warn().Err(err).Msg("scts: catalog parse failed")
		return nil
	}
	if len(parsed.Items) == 0 {
		log.Warn().Msg("scts: catalog empty")
		return nil
	}

	idx := &sctsIndex{
		items:      parsed.Items,
		tmdbMovie:  map[int]*sctsItem{},
		tmdbSeries: map[int][]*sctsItem{},
		imdbMovie:  map[string]*sctsItem{},
		imdbSeries: map[string][]*sctsItem{},
		titleYear:  map[string][]*sctsItem{},
		builtAt:    time.Now(),
	}
	for i := range idx.items {
		m := &idx.items[i]
		isTV := m.K == "tv"
		if m.T > 0 {
			if isTV {
				idx.tmdbSeries[m.T] = append(idx.tmdbSeries[m.T], m)
			} else if idx.tmdbMovie[m.T] == nil {
				idx.tmdbMovie[m.T] = m
			}
		}
		if m.M != "" {
			key := strings.ToLower(m.M)
			if isTV {
				idx.imdbSeries[key] = append(idx.imdbSeries[key], m)
			} else if idx.imdbMovie[key] == nil {
				idx.imdbMovie[key] = m
			}
		}
		if m.N != "" && m.Y > 0 {
			for _, k := range sctsTitleYearKeys(m) {
				idx.titleYear[k] = append(idx.titleYear[k], m)
			}
		}
	}
	log.Info().Int("n", len(idx.items)).
		Int("tmdb_movie", len(idx.tmdbMovie)).Int("tmdb_series", len(idx.tmdbSeries)).
		Msg("scts: catalog indexed")
	return idx
}

var sctsSeasonSuffixRe = regexp.MustCompile(`(?i)\s*\(\s*\d+\s*сезон\s*\)\s*`)

func sctsTitleYearKeys(m *sctsItem) []string {
	year := strconv.Itoa(m.Y)
	out := make([]string, 0, 2)
	seen := map[string]bool{}
	add := func(s string) {
		n := sctsNormalizeTitle(s)
		if n == "" || seen[n] {
			return
		}
		seen[n] = true
		out = append(out, n+"|"+year)
	}
	add(m.N)
	add(sctsSeasonSuffixRe.ReplaceAllString(m.N, " "))
	return out
}

var sctsTitleCleanRe = regexp.MustCompile(`[^a-zа-я0-9 ]+`)

func sctsNormalizeTitle(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "ё", "е")
	s = sctsTitleCleanRe.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(s), " ")
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

func sctsPickSeason(list []*sctsItem, season int) *sctsItem {
	if len(list) == 0 {
		return nil
	}
	if season > 0 {
		for _, m := range list {
			if m.S == season {
				return m
			}
		}
	}
	best := list[0]
	for _, m := range list {
		sj, sb := m.S, best.S
		if sj == 0 {
			sj = 999
		}
		if sb == 0 {
			sb = 999
		}
		if sj < sb {
			best = m
		}
	}
	return best
}

// sctsFind возвращает (найденная запись, все сезоны сериала | nil для фильма).
func (c *sctsChecker) find(idx *sctsIndex, q url.Values) (*sctsItem, []*sctsItem) {
	serial := strings.TrimSpace(q.Get("serial")) == "1" || strings.TrimSpace(q.Get("serial")) == "true"
	season, _ := strconv.Atoi(strings.TrimSpace(q.Get("s")))

	if t := firstNonEmptyStr(q.Get("tmdb_id"), q.Get("id")); t != "" {
		if id, err := strconv.Atoi(t); err == nil && id > 0 {
			if !serial {
				if m := idx.tmdbMovie[id]; m != nil {
					return m, nil
				}
			}
			if list := idx.tmdbSeries[id]; len(list) > 0 {
				return sctsPickSeason(list, season), list
			}
		}
	}
	if imdb := strings.ToLower(strings.TrimSpace(q.Get("imdb_id"))); imdb != "" {
		if !serial {
			if m := idx.imdbMovie[imdb]; m != nil {
				return m, nil
			}
		}
		if list := idx.imdbSeries[imdb]; len(list) > 0 {
			return sctsPickSeason(list, season), list
		}
	}
	if year := strings.TrimSpace(q.Get("year")); year != "" {
		for _, t := range []string{q.Get("original_title"), q.Get("title")} {
			if strings.TrimSpace(t) == "" {
				continue
			}
			cands := idx.titleYear[sctsNormalizeTitle(t)+"|"+year]
			if len(cands) == 0 {
				continue
			}
			var tvs []*sctsItem
			for _, cd := range cands {
				if cd.K == "tv" {
					tvs = append(tvs, cd)
				}
			}
			if len(tvs) > 0 {
				return sctsPickSeason(tvs, season), tvs
			}
			return cands[0], nil
		}
	}
	return nil, nil
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Episode number from filename
// ---------------------------------------------------------------------------

var sctsEpPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)s\d+\.?e(\d{1,3})`),
	regexp.MustCompile(`(?i)\(?\s*(\d{1,3})\s*(?:[._\- ]+)?\s*(?:ser|серия|сер|seriya|s)\b`),
	regexp.MustCompile(`(\d{1,3})\s+серия`),
	regexp.MustCompile(`^(\d{1,3})[._\- ]`),
	regexp.MustCompile(`(?i)e(\d{1,3})\b`),
}

func sctsParseEpisodeNum(name string) int {
	if name == "" {
		return 0
	}
	for _, re := range sctsEpPatterns {
		if m := re.FindStringSubmatch(name); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil && n > 0 && n < 500 {
				return n
			}
		}
	}
	return 0
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

func (c *sctsChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		rjson := parseBoolParam(q.Get("rjson"))

		idx := c.catalog()
		if idx == nil {
			if parseBoolParam(q.Get("checksearch")) {
				writeCheckSearchResponse(w, false, "")
				return
			}
			writeGetsTVEmpty(w, rjson)
			return
		}

		found, seasons := c.find(idx, q)
		if found != nil && len(found.Files) == 0 && len(seasons) > 0 {
			for _, m := range seasons {
				if len(m.Files) > 0 {
					found = m
					break
				}
			}
		}
		playable := found != nil && len(found.Files) > 0

		if parseBoolParam(q.Get("checksearch")) {
			writeCheckSearchResponse(w, playable, "FHD")
			return
		}
		if !playable {
			writeGetsTVEmpty(w, rjson)
			return
		}

		baseTitle := getsTVJoinName(strings.TrimSpace(q.Get("title")), strings.TrimSpace(q.Get("original_title")))
		if baseTitle == "" {
			baseTitle = found.N
		}
		season, _ := strconv.Atoi(strings.TrimSpace(q.Get("s")))

		// Сериал, сезон не выбран → список сезонов.
		if len(seasons) > 1 && season == 0 {
			c.writeSeasonList(w, req, rjson, seasons)
			return
		}
		if len(seasons) > 0 {
			c.writeEpisodes(w, rjson, found, baseTitle)
			return
		}
		c.writeMovie(w, rjson, found, baseTitle)
	}
}

func (c *sctsChecker) writeMovie(w http.ResponseWriter, rjson bool, m *sctsItem, baseTitle string) {
	rows := make([]map[string]any, 0, len(m.Files))
	labels := make([]string, 0, len(m.Files))
	for _, f := range m.Files {
		u := strings.TrimSpace(f[1])
		if u == "" {
			continue
		}
		rows = append(rows, map[string]any{
			"method": "play",
			"url":    u,
			"stream": u,
			"name":   "1080p",
			"title":  baseTitle,
		})
		labels = append(labels, "1080p")
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
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, 0, 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (c *sctsChecker) writeEpisodes(w http.ResponseWriter, rjson bool, m *sctsItem, baseTitle string) {
	seasonNum := m.S
	if seasonNum == 0 {
		seasonNum = 1
	}
	type epRow struct {
		row   map[string]any
		label string
		num   int
	}
	eps := make([]epRow, 0, len(m.Files))
	for _, f := range m.Files {
		name, u := strings.TrimSpace(f[0]), strings.TrimSpace(f[1])
		if u == "" {
			continue
		}
		num := sctsParseEpisodeNum(name)
		label := name
		if num > 0 {
			label = strconv.Itoa(num) + " серия"
		}
		eps = append(eps, epRow{
			row: map[string]any{
				"method": "play",
				"url":    u,
				"stream": u,
				"s":      seasonNum,
				"e":      num,
				"name":   label,
				"title":  baseTitle + " / " + strconv.Itoa(seasonNum) + " сезон " + strconv.Itoa(num) + " серия",
			},
			label: label,
			num:   num,
		})
	}
	sort.SliceStable(eps, func(i, j int) bool {
		if eps[i].num == 0 {
			return false
		}
		if eps[j].num == 0 {
			return true
		}
		return eps[i].num < eps[j].num
	})
	if len(eps) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	if rjson {
		rows := make([]map[string]any, 0, len(eps))
		for _, ep := range eps {
			rows = append(rows, ep.row)
		}
		writeJSON(w, http.StatusOK, map[string]any{"type": "episode", "data": rows})
		return
	}
	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, ep := range eps {
		getsTVAppendMovieHTML(&sb, ep.row, ep.label, i == 0, seasonNum, ep.num)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (c *sctsChecker) writeSeasonList(w http.ResponseWriter, req *http.Request, rjson bool, seasons []*sctsItem) {
	host := hostFromRequest(req)
	q := req.URL.Query()
	sorted := append([]*sctsItem(nil), seasons...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].S < sorted[j].S })

	rows := make([]map[string]any, 0, len(sorted))
	labels := make([]string, 0, len(sorted))
	for _, m := range sorted {
		if m.S == 0 {
			continue
		}
		params := url.Values{}
		if rjson {
			params.Set("rjson", "true")
		}
		params.Set("serial", "1")
		params.Set("s", strconv.Itoa(m.S))
		for _, k := range []string{"tmdb_id", "id", "imdb_id", "title", "original_title", "year"} {
			if v := strings.TrimSpace(q.Get(k)); v != "" {
				params.Set(k, v)
			}
		}
		name := strconv.Itoa(m.S) + " сезон"
		rows = append(rows, map[string]any{
			"method": "link",
			"id":     m.S,
			"s":      m.S,
			"url":    host + "/lite/scts?" + params.Encode(),
			"name":   name,
		})
		labels = append(labels, name)
	}
	if len(rows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "season", "data": rows})
		return
	}
	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range rows {
		getsTVAppendSeasonHTML(&sb, row, labels[i], i == 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}
