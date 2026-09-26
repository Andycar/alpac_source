package litesrc

import (
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

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/proxylink"
)

// kbteam.go — нативный порт JS-модуля kbteam (kb-team.club MSX videocdn API).
//
// Movie:  vid={kp} → массив озвучек × качества (1080/480/…).
// Serial: 3 уровня — vid={kp} → seasons (panel sid) → voices (panel sid+tid) → series.
//
// CDN-хост mpa.education1ne.in.net = CNAME cdn.zerocdn.com (GEO CIS-only), URL-токен
// включает {YYYYMMDDhh} — валидность ~1 час, поэтому кэш ответов короткий (30 мин).
// Матчинг строго по kinopoisk_id — титульного фолбэка нет.

type kbteamChecker struct {
	client   *http.Client
	apiHost  string
	cacheTTL time.Duration

	mu    sync.Mutex
	cache map[string]kbteamCacheEntry
}

type kbteamCacheEntry struct {
	data *kbteamMSX
	exp  time.Time
}

// kbteamMSX — интересующая нас часть MSX-ответа: пункты либо в items, либо
// распиханы по pages[].items.
type kbteamMSX struct {
	Items []kbteamMSXItem `json:"items"`
	Pages []struct {
		Items []kbteamMSXItem `json:"items"`
	} `json:"pages"`
}

type kbteamMSXItem struct {
	Type        string `json:"type"`
	Label       string `json:"label"`
	TitleHeader string `json:"titleHeader"`
	Action      string `json:"action"`
}

func (m *kbteamMSX) flatten() []kbteamMSXItem {
	if m == nil {
		return nil
	}
	if len(m.Items) > 0 {
		return m.Items
	}
	var out []kbteamMSXItem
	for _, p := range m.Pages {
		out = append(out, p.Items...)
	}
	return out
}

func NewKBTeamChecker(cfg config.Config) *kbteamChecker {
	host := strings.TrimSpace(cfg.Online.KBTeam.APIHost)
	if host == "" {
		host = "http://kb-team.club/msx/kinozal/videocdn.php"
	}
	ttl := cfg.Online.KBTeam.CacheTTL
	if ttl <= 0 {
		ttl = 1800
	}
	return &kbteamChecker{
		client:   httpclient.New(15 * time.Second),
		apiHost:  host,
		cacheTTL: time.Duration(ttl) * time.Second,
		cache:    map[string]kbteamCacheEntry{},
	}
}

const kbteamUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"

func (c *kbteamChecker) apiGet(req *http.Request, params string) *kbteamMSX {
	target := c.apiHost + "?" + params + "&device="
	c.mu.Lock()
	if e, ok := c.cache[target]; ok && time.Now().Before(e.exp) {
		c.mu.Unlock()
		return e.data
	}
	c.mu.Unlock()

	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, target, nil)
	if err != nil {
		return nil
	}
	httpReq.Header.Set("User-Agent", kbteamUA)
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("X-Lampac-Go", "1")
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil
	}
	var data kbteamMSX
	if stdjson.Unmarshal(body, &data) != nil {
		return nil
	}
	c.mu.Lock()
	c.cache[target] = kbteamCacheEntry{data: &data, exp: time.Now().Add(c.cacheTTL)}
	// Дешёвая уборка: не даём карте расти безгранично.
	if len(c.cache) > 4096 {
		now := time.Now()
		for k, e := range c.cache {
			if now.After(e.exp) {
				delete(c.cache, k)
			}
		}
	}
	c.mu.Unlock()
	return &data
}

// ---------------------------------------------------------------------------
// MSX helpers
// ---------------------------------------------------------------------------

var (
	kbteamMSXBraceRe = regexp.MustCompile(`\{[^}]+\}`)
	kbteamQualityRe  = regexp.MustCompile(`/(\d+)(?:-[^/]+)?\.mp4`)
	kbteamSidRe      = regexp.MustCompile(`(?:^|&)sid=(\d+)`)
	kbteamNumRe      = regexp.MustCompile(`(\d+)`)
)

func kbteamStripVideo(action string) string {
	if action == "" {
		return ""
	}
	if i := strings.Index(strings.ToLower(action), "video:"); i == 0 {
		return action[len("video:"):]
	}
	return action
}

func kbteamPanelToParams(action string) string {
	if action == "" {
		return ""
	}
	q := action
	if strings.HasPrefix(strings.ToLower(q), "panel:") {
		q = q[len("panel:"):]
	}
	i := strings.Index(q, "?")
	if i < 0 {
		return ""
	}
	return strings.TrimSuffix(q[i+1:], "&device=")
}

func kbteamCleanMSX(s string) string {
	return strings.Join(strings.Fields(kbteamMSXBraceRe.ReplaceAllString(s, "")), " ")
}

func kbteamQualityFromAction(act string) int {
	if m := kbteamQualityRe.FindStringSubmatch(act); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

func kbteamQualityLabel(p int) string {
	switch {
	case p >= 1080:
		return "1080p"
	case p >= 720:
		return "720p"
	case p >= 480:
		return "480p"
	case p >= 360:
		return "360p"
	}
	return strconv.Itoa(p) + "p"
}

func kbteamItemsLookSerial(items []kbteamMSXItem) bool {
	for _, it := range items {
		if it.Type == "control" && strings.HasPrefix(strings.ToLower(it.Action), "panel:") {
			return true
		}
	}
	return false
}

type kbteamVoice struct {
	name string
	urls []struct {
		q   int
		url string
	}
}

// kbteamGroupMovieVoices: каждый `space` начинает озвучку, следующие control'ы —
// её качества.
func kbteamGroupMovieVoices(items []kbteamMSXItem) []kbteamVoice {
	var voices []kbteamVoice
	var cur *kbteamVoice
	flush := func() {
		if cur != nil && len(cur.urls) > 0 {
			voices = append(voices, *cur)
		}
		cur = nil
	}
	for _, it := range items {
		if it.Type == "space" {
			flush()
			name := kbteamCleanMSX(firstNonEmptyStr(it.TitleHeader, it.Label))
			if name == "" {
				name = "Озвучка " + strconv.Itoa(len(voices)+1)
			}
			cur = &kbteamVoice{name: name}
			continue
		}
		if cur != nil && it.Action != "" {
			u := kbteamStripVideo(it.Action)
			q := kbteamQualityFromAction(it.Action)
			if u != "" && q > 0 {
				cur.urls = append(cur.urls, struct {
					q   int
					url string
				}{q: q, url: u})
			}
		}
	}
	flush()
	return voices
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

func (c *kbteamChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		rjson := parseBoolParam(q.Get("rjson"))
		kp := strings.TrimSpace(q.Get("kinopoisk_id"))

		if parseBoolParam(q.Get("checksearch")) {
			ok := false
			if kp != "" {
				data := c.apiGet(req, "act=watch&vid="+url.QueryEscape(kp))
				ok = len(data.flattenSafe()) > 0
			}
			writeCheckSearchResponse(w, ok, pluginQualityBadgeGet("kbteam"))
			return
		}
		if kp == "" {
			writeGetsTVEmpty(w, rjson)
			return
		}

		title := strings.TrimSpace(q.Get("title"))
		orig := strings.TrimSpace(q.Get("original_title"))
		s, sSet := getsTVQueryInt(q.Get("s"))
		if !sSet {
			s = -1
		}
		t, tSet := getsTVQueryInt(q.Get("t"))
		if !tSet {
			t = -1
		}

		data := c.apiGet(req, "act=watch&vid="+url.QueryEscape(kp))
		items := data.flattenSafe()
		if len(items) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}

		if !kbteamItemsLookSerial(items) {
			c.writeMovie(w, req, rjson, title, orig, kbteamGroupMovieVoices(items), links)
			return
		}

		// Сериал.
		if s == -1 {
			if len(items) > 1 {
				c.writeSeasons(w, req, rjson, kp, title, orig, items)
				return
			}
			s = 1
		}
		sidx := s - 1
		if sidx < 0 || sidx >= len(items) {
			sidx = 0
		}
		seasonParams := kbteamPanelToParams(items[sidx].Action)
		if seasonParams == "" {
			writeGetsTVEmpty(w, rjson)
			return
		}
		voicesItems := c.apiGet(req, seasonParams).flattenSafe()
		if len(voicesItems) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}
		tidx := 0
		if t >= 0 && t < len(voicesItems) {
			tidx = t
		}
		voiceParams := kbteamPanelToParams(voicesItems[tidx].Action)
		if voiceParams == "" {
			writeGetsTVEmpty(w, rjson)
			return
		}
		epsItems := c.apiGet(req, voiceParams).flattenSafe()
		c.writeEpisodes(w, req, rjson, kp, title, orig, sidx+1, tidx, voicesItems, epsItems, links)
	}
}

// flattenSafe is flatten() that tolerates a nil receiver from a failed apiGet.
func (m *kbteamMSX) flattenSafe() []kbteamMSXItem {
	if m == nil {
		return nil
	}
	return m.flatten()
}

func (c *kbteamChecker) proxied(req *http.Request, links *proxylink.Manager, u string) string {
	if u == "" {
		return ""
	}
	return streamProxyURL(req, u, "kbteam", links)
}

func (c *kbteamChecker) writeMovie(w http.ResponseWriter, req *http.Request, rjson bool, title, orig string, voices []kbteamVoice, links *proxylink.Manager) {
	base := getsTVJoinName(title, orig)
	rows := make([]map[string]any, 0, len(voices))
	labels := make([]string, 0, len(voices))
	for _, v := range voices {
		sorted := append([]struct {
			q   int
			url string
		}(nil), v.urls...)
		sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].q > sorted[j].q })
		if len(sorted) == 0 {
			continue
		}
		qmap := map[string]string{}
		for _, su := range sorted {
			if _, dup := qmap[kbteamQualityLabel(su.q)]; !dup {
				qmap[kbteamQualityLabel(su.q)] = c.proxied(req, links, su.url)
			}
		}
		top := c.proxied(req, links, sorted[0].url)
		rows = append(rows, map[string]any{
			"method":  "play",
			"url":     top,
			"stream":  top,
			"quality": qmap,
			"name":    v.name,
			"title":   base + " (" + v.name + ")",
		})
		labels = append(labels, v.name)
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

func (c *kbteamChecker) seasonURL(req *http.Request, kp, title, orig string, s int, rjson bool) string {
	params := url.Values{}
	if rjson {
		params.Set("rjson", "true")
	}
	params.Set("kinopoisk_id", kp)
	params.Set("title", title)
	params.Set("original_title", orig)
	params.Set("serial", "1")
	params.Set("s", strconv.Itoa(s))
	return hostFromRequest(req) + "/lite/kbteam?" + params.Encode()
}

func (c *kbteamChecker) writeSeasons(w http.ResponseWriter, req *http.Request, rjson bool, kp, title, orig string, items []kbteamMSXItem) {
	rows := make([]map[string]any, 0, len(items))
	labels := make([]string, 0, len(items))
	for idx, it := range items {
		p := kbteamPanelToParams(it.Action)
		sidx := idx
		if m := kbteamSidRe.FindStringSubmatch(p); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil {
				sidx = n
			}
		}
		label := kbteamCleanMSX(it.Label)
		if label == "" {
			label = "Сезон " + strconv.Itoa(sidx+1)
		}
		rows = append(rows, map[string]any{
			"method": "link",
			"id":     sidx + 1,
			"s":      sidx + 1,
			"name":   label,
			"url":    c.seasonURL(req, kp, title, orig, sidx+1, rjson),
		})
		labels = append(labels, label)
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

func (c *kbteamChecker) writeEpisodes(w http.ResponseWriter, req *http.Request, rjson bool, kp, title, orig string, sNum, tidx int, voicesItems, epsItems []kbteamMSXItem, links *proxylink.Manager) {
	base := getsTVJoinName(title, orig)

	voiceRows := make([]map[string]any, 0, len(voicesItems))
	for i, it := range voicesItems {
		name := kbteamCleanMSX(it.Label)
		if name == "" {
			name = "Озвучка " + strconv.Itoa(i+1)
		}
		params := url.Values{}
		if rjson {
			params.Set("rjson", "true")
		}
		params.Set("kinopoisk_id", kp)
		params.Set("title", title)
		params.Set("original_title", orig)
		params.Set("serial", "1")
		params.Set("s", strconv.Itoa(sNum))
		params.Set("t", strconv.Itoa(i))
		voiceRows = append(voiceRows, map[string]any{
			"name":   name,
			"active": i == tidx,
			"url":    hostFromRequest(req) + "/lite/kbteam?" + params.Encode(),
		})
	}

	rows := make([]map[string]any, 0, len(epsItems))
	labels := make([]string, 0, len(epsItems))
	epNums := make([]int, 0, len(epsItems))
	for i, it := range epsItems {
		u := c.proxied(req, links, kbteamStripVideo(it.Action))
		if u == "" || !strings.HasPrefix(kbteamStripVideo(it.Action), "http") {
			continue
		}
		num := i + 1
		if m := kbteamNumRe.FindStringSubmatch(it.Label); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
				num = n
			}
		}
		nm := kbteamCleanMSX(it.Label)
		if nm == "" {
			nm = "Серия " + strconv.Itoa(num)
		}
		rows = append(rows, map[string]any{
			"method": "play",
			"url":    u,
			"stream": u,
			"s":      sNum,
			"e":      num,
			"name":   nm,
			"title":  base + " (" + nm + ")",
		})
		labels = append(labels, nm)
		epNums = append(epNums, num)
	}
	if len(rows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "episode", "voice": voiceRows, "data": rows})
		return
	}
	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for _, vr := range voiceRows {
		getsTVAppendVoiceHTML(&sb, vr)
	}
	sb.WriteString(`</div><div class="videos__line">`)
	for i, row := range rows {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, sNum, epNums[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}
