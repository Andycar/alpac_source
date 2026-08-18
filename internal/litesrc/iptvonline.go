package litesrc

import (
	"bytes"
	"context"
	stdjson "encoding/json"
	"fmt"
	"html"
	"io"
	"lampac-go/internal/httpclient"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"
)

type iptvOnlineChecker struct {
	client *http.Client
	host   string
	token  string
}

type iptvOnlineAuthResponse struct {
	Code string `json:"code"`
}

type iptvOnlineSearchResponse struct {
	Message string               `json:"message"`
	Data    []iptvOnlineSearchEl `json:"data"`
}

type iptvOnlineSearchEl struct {
	ID        stdjson.RawMessage `json:"id"`
	Kinopoisk *int64             `json:"kinopoisk"`
	IMDB      *int64             `json:"imdb"`
	OrigTitle string             `json:"orig_title"`
	RuTitle   string             `json:"ru_title"`
}

type iptvOnlineDetailResponse struct {
	Data iptvOnlineDetailData `json:"data"`
}

type iptvOnlineDetailData struct {
	Category string                `json:"category"`
	Quality  stdjson.RawMessage    `json:"quality"`
	Medias   []iptvOnlineDetailSet `json:"medias"`
}

type iptvOnlineDetailSet struct {
	Season   int                    `json:"season"`
	URL      string                 `json:"url"`
	Episodes []iptvOnlineDetailItem `json:"episodes"`
}

type iptvOnlineDetailItem struct {
	Episode int    `json:"episode"`
	Title   string `json:"title"`
	URL     string `json:"url"`
}

type iptvOnlineAuthCacheItem struct {
	Code string
	Exp  time.Time
}

var iptvOnlineAuthCache sync.Map

func NewIptvOnlineChecker(cfg config.Config) *iptvOnlineChecker {
	host := strings.TrimSpace(strings.TrimRight(cfg.Online.IptvOnline.Host, "/"))
	if host == "" {
		host = "https://iptv.online"
	}
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}

	return &iptvOnlineChecker{
		client: httpclient.NewForBalancer("iptvonline", 12*time.Second),
		host:   host,
		token:  strings.TrimSpace(cfg.Online.IptvOnline.Token),
	}
}

func (c *iptvOnlineChecker) Handle(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := strings.Trim(strings.TrimPrefix(strings.ToLower(r.URL.Path), "/lite/"), "/")

		switch raw {
		case "iptvonline/bind":
			c.bind(w, r)
			return
		case "iptvonline":
			if parseBoolParam(r.URL.Query().Get("checksearch")) {
				show := c.checkSearch(r.Context(), r.URL.Query())
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusOK)
				if show {
					_, _ = w.Write([]byte(`{"type":"movie","rch":false}`))
					return
				}
				_, _ = w.Write([]byte(`{"rch":false}`))
				return
			}

			if _, _, ok := c.tokenParts(); !ok {
				writeIptvError(w, parseBoolParam(r.URL.Query().Get("rjson")), "token", http.StatusUnauthorized)
				return
			}

			c.index(w, r)
			return
		}

		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error":    "iptvonline route is not implemented in local mode",
			"balanser": raw,
		})
	}
}

func (c *iptvOnlineChecker) bind(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.URL.Query().Get("KEY"))
	id := strings.TrimSpace(r.URL.Query().Get("ID"))

	if key == "" || id == "" {
		writeHTML(w, http.StatusOK, `Введите данные <a href='https://iptv.online/ru/dealers/api' target=_blank>https://iptv.online/ru/dealers/api</a> <br> <br><form method="get" action="/lite/iptvonline/bind"><input type="text" name="KEY" placeholder="X-API-KEY"> &nbsp; &nbsp; <input type="text" name="ID" placeholder="X-API-ID"><br><br><button>Авторизоваться</button></form> `)
		return
	}

	writeHTML(w, http.StatusOK, `Добавьте в init.conf<br><br>"IptvOnline": {<br>&nbsp;&nbsp;"enable": true,<br>&nbsp;&nbsp;"token": "`+html.EscapeString(id+":"+key)+`"<br>}`)
}

func (c *iptvOnlineChecker) index(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))

	serial, serialSet := getsTVQueryInt(q.Get("serial"))
	if !serialSet {
		serial = -1
	}
	s, sSet := getsTVQueryInt(q.Get("s"))
	if !sSet {
		s = -1
	}

	imdbID := strings.TrimSpace(q.Get("imdb_id"))
	kinopoiskID := strings.TrimSpace(q.Get("kinopoisk_id"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))

	authCode, apiID, ok := c.ensureAuth(r.Context())
	if !ok {
		writeIptvError(w, rjson, "auth", http.StatusBadGateway)
		return
	}

	itemID, message, ok := c.search(r.Context(), authCode, apiID, serial == 1, imdbID, kinopoiskID, title, originalTitle)
	if !ok {
		if message != "" {
			writeIptvError(w, rjson, message, http.StatusForbidden)
			return
		}
		writeGetsTVEmpty(w, rjson)
		return
	}

	detail, ok := c.fetchDetail(r.Context(), authCode, apiID, serial == 1, itemID)
	if !ok {
		writeIptvError(w, rjson, "detail", http.StatusBadGateway)
		return
	}

	host := hostFromRequest(r)
	quality := iptvOnlineQuality(detail.Quality)
	if strings.EqualFold(strings.TrimSpace(detail.Category), "movie") {
		stream := ""
		for _, media := range detail.Medias {
			u := strings.TrimSpace(media.URL)
			if u == "" {
				continue
			}
			stream = u + "#.m3u8"
			break
		}
		if stream == "" {
			writeGetsTVEmpty(w, rjson)
			return
		}

		label := quality
		if label == "" {
			label = strings.TrimSpace(title)
			if label == "" {
				label = strings.TrimSpace(originalTitle)
			}
			if label == "" {
				label = "Play"
			}
		}

		row := map[string]any{
			"method": "play",
			"url":    stream,
			"title":  getsTVJoinName(title, originalTitle),
		}
		if quality != "" {
			row["quality"] = map[string]any{quality: stream}
		}
		if rjson {
			writeJSON(w, http.StatusOK, map[string]any{
				"type": "movie",
				"data": []any{row},
			})
			return
		}

		var sb strings.Builder
		sb.WriteString(`<div class="videos__line">`)
		getsTVAppendMovieHTML(&sb, row, label, true, 0, 0)
		sb.WriteString(`</div>`)
		writeHTML(w, http.StatusOK, sb.String())
		return
	}

	if s == -1 {
		seasonSet := map[int]struct{}{}
		for _, media := range detail.Medias {
			if media.Season > 0 {
				seasonSet[media.Season] = struct{}{}
			}
		}
		if len(seasonSet) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}

		seasons := make([]int, 0, len(seasonSet))
		for season := range seasonSet {
			seasons = append(seasons, season)
		}
		sort.Ints(seasons)

		data := make([]map[string]any, 0, len(seasons))
		labels := make([]string, 0, len(seasons))
		for _, season := range seasons {
			name := strconv.Itoa(season) + " сезон"
			link := host + "/lite/iptvonline?rjson=" + getsTVBool(rjson) +
				"&serial=" + strconv.Itoa(serial) +
				"&kinopoisk_id=" + urlEncodeIfNotEmpty(kinopoiskID) +
				"&imdb_id=" + urlEncodeIfNotEmpty(imdbID) +
				"&title=" + urlEncodeIfNotEmpty(title) +
				"&original_title=" + urlEncodeIfNotEmpty(originalTitle) +
				"&s=" + strconv.Itoa(season)
			data = append(data, map[string]any{
				"method": "link",
				"id":     season,
				"url":    link,
				"name":   name,
			})
			labels = append(labels, name)
		}

		if rjson {
			payload := map[string]any{
				"type": "season",
				"data": data,
			}
			if quality != "" {
				payload["maxquality"] = quality
			}
			writeJSON(w, http.StatusOK, payload)
			return
		}

		var sb strings.Builder
		sb.WriteString(`<div class="videos__line">`)
		if quality != "" {
			sb.WriteString(`<!--q:` + html.EscapeString(quality) + `-->`)
		}
		for i, row := range data {
			getsTVAppendSeasonHTML(&sb, row, labels[i], i == 0)
		}
		sb.WriteString(`</div>`)
		writeHTML(w, http.StatusOK, sb.String())
		return
	}

	var selected *iptvOnlineDetailSet
	for i := range detail.Medias {
		if detail.Medias[i].Season == s {
			selected = &detail.Medias[i]
			break
		}
	}
	if selected == nil || len(selected.Episodes) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	rows := make([]map[string]any, 0, len(selected.Episodes))
	labels := make([]string, 0, len(selected.Episodes))
	seasons := make([]int, 0, len(selected.Episodes))
	episodes := make([]int, 0, len(selected.Episodes))
	for _, ep := range selected.Episodes {
		file := strings.TrimSpace(ep.URL)
		if file == "" {
			continue
		}
		file += "#.m3u8"
		name := strings.TrimSpace(ep.Title)
		if name == "" {
			if ep.Episode > 0 {
				name = strconv.Itoa(ep.Episode) + " серия"
			} else {
				name = "Серия"
			}
		}

		rows = append(rows, map[string]any{
			"method": "play",
			"url":    file,
			"s":      s,
			"e":      ep.Episode,
			"name":   name,
			"title":  fmt.Sprintf("%s (%d серия)", getsTVJoinName(title, originalTitle), ep.Episode),
		})
		labels = append(labels, name)
		seasons = append(seasons, s)
		episodes = append(episodes, ep.Episode)
	}
	if len(rows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "episode",
			"data": rows,
		})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range rows {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, seasons[i], episodes[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (c *iptvOnlineChecker) checkSearch(ctx context.Context, q map[string][]string) bool {
	if c.host == "" {
		return false
	}
	if _, _, ok := c.tokenParts(); !ok {
		return c.probeHost(ctx)
	}

	authCode, apiID, ok := c.ensureAuth(ctx)
	if !ok {
		return c.probeHost(ctx)
	}

	imdbID := strings.TrimSpace(firstQuery(q, "imdb_id"))
	kinopoiskID := strings.TrimSpace(firstQuery(q, "kinopoisk_id"))
	title := strings.TrimSpace(firstQuery(q, "title"))
	originalTitle := strings.TrimSpace(firstQuery(q, "original_title"))
	serial := strings.TrimSpace(firstQuery(q, "serial")) == "1"

	_, message, ok := c.search(ctx, authCode, apiID, serial, imdbID, kinopoiskID, title, originalTitle)
	return ok && message == ""
}

func (c *iptvOnlineChecker) ensureAuth(ctx context.Context) (authCode, apiID string, ok bool) {
	apiID, apiKey, ok := c.tokenParts()
	if !ok {
		return "", "", false
	}

	if item, ok := iptvOnlineAuthCache.Load(c.token); ok {
		cache := item.(iptvOnlineAuthCacheItem)
		if time.Now().Before(cache.Exp) && strings.TrimSpace(cache.Code) != "" {
			return cache.Code, apiID, true
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.host+"/v1/api/auth", bytes.NewReader(nil))
	if err != nil {
		return "", "", false
	}
	req.Header.Set("X-API-KEY", apiKey)
	req.Header.Set("X-API-ID", apiID)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("X-Lampac-Go", "1")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", false
	}

	var root iptvOnlineAuthResponse
	if err := stdjson.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&root); err != nil {
		return "", "", false
	}
	if strings.TrimSpace(root.Code) == "" {
		return "", "", false
	}

	iptvOnlineAuthCache.Store(c.token, iptvOnlineAuthCacheItem{
		Code: root.Code,
		Exp:  time.Now().Add(2 * time.Hour),
	})
	return root.Code, apiID, true
}

func (c *iptvOnlineChecker) search(ctx context.Context, authCode, apiID string, serial bool, imdbID, kinopoiskID, title, originalTitle string) (id, message string, ok bool) {
	tryQueries := make([]string, 0, 2)
	if strings.TrimSpace(originalTitle) != "" {
		tryQueries = append(tryQueries, strings.TrimSpace(originalTitle))
	}
	if strings.TrimSpace(title) != "" {
		normTitle := normalizeSearchTitle(title)
		normOriginal := normalizeSearchTitle(originalTitle)
		if normTitle != "" && normTitle != normOriginal {
			tryQueries = append(tryQueries, strings.TrimSpace(title))
		}
	}

	if len(tryQueries) == 0 && (imdbID == "" && kinopoiskID == "") {
		return "", "", false
	}

	for _, query := range tryQueries {
		items, msg, ok := c.searchPage(ctx, authCode, apiID, serial, query)
		if !ok {
			return "", "", false
		}
		if msg != "" {
			return "", msg, false
		}
		if len(items) == 0 {
			continue
		}

		if found := iptvOnlinePickByIDs(items, imdbID, kinopoiskID); found != "" {
			return found, "", true
		}
		if found := iptvOnlinePickByTitle(items, title, originalTitle); found != "" {
			return found, "", true
		}
	}

	return "", "", false
}

func (c *iptvOnlineChecker) searchPage(ctx context.Context, authCode, apiID string, serial bool, search string) ([]iptvOnlineSearchEl, string, bool) {
	if strings.TrimSpace(search) == "" {
		return nil, "", true
	}

	path := "movies"
	if serial {
		path = "serials"
	}

	payload, _ := stdjson.Marshal(map[string]any{"search": search})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.host+"/v1/api/media/"+path, bytes.NewReader(payload))
	if err != nil {
		return nil, "", false
	}
	req.Header.Set("X-API-AUTH", authCode)
	req.Header.Set("X-API-ID", apiID)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("X-Lampac-Go", "1")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", false
	}

	var root iptvOnlineSearchResponse
	if err := stdjson.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&root); err != nil {
		return nil, "", false
	}
	return root.Data, strings.TrimSpace(root.Message), true
}

func (c *iptvOnlineChecker) fetchDetail(ctx context.Context, authCode, apiID string, serial bool, id string) (iptvOnlineDetailData, bool) {
	var out iptvOnlineDetailData
	if strings.TrimSpace(id) == "" {
		return out, false
	}

	path := "movies"
	if serial {
		path = "serials"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.host+"/v1/api/media/"+path+"/"+id+"/", nil)
	if err != nil {
		return out, false
	}
	req.Header.Set("X-API-AUTH", authCode)
	req.Header.Set("X-API-ID", apiID)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("X-Lampac-Go", "1")

	resp, err := c.client.Do(req)
	if err != nil {
		return out, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return out, false
	}

	var root iptvOnlineDetailResponse
	if err := stdjson.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&root); err != nil {
		return out, false
	}
	if len(root.Data.Medias) == 0 {
		return out, false
	}
	return root.Data, true
}

func (c *iptvOnlineChecker) tokenParts() (id, key string, ok bool) {
	parts := strings.SplitN(strings.TrimSpace(c.token), ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	id = strings.TrimSpace(parts[0])
	key = strings.TrimSpace(parts[1])
	if id == "" || key == "" {
		return "", "", false
	}
	return id, key, true
}

func (c *iptvOnlineChecker) probeHost(ctx context.Context) bool {
	return c.probe(ctx, http.MethodHead, c.host) || c.probe(ctx, http.MethodGet, c.host)
}

func (c *iptvOnlineChecker) probe(ctx context.Context, method, target string) bool {
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/json,*/*")
	req.Header.Set("X-Lampac-Go", "1")

	resp, err := c.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if method == http.MethodHead && resp.StatusCode == http.StatusMethodNotAllowed {
		return false
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return false
	}
	if method == http.MethodHead {
		return true
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(body))) > 0
}

func iptvOnlinePickByIDs(items []iptvOnlineSearchEl, imdbID, kinopoiskID string) string {
	imdbID = strings.ToLower(strings.TrimSpace(imdbID))
	kinopoiskID = strings.TrimSpace(kinopoiskID)

	for _, item := range items {
		id := iptvRawString(item.ID)
		if id == "" {
			continue
		}
		if imdbID != "" && item.IMDB != nil {
			if "tt"+strconv.FormatInt(*item.IMDB, 10) == imdbID {
				return id
			}
		}
		if kinopoiskID != "" && item.Kinopoisk != nil {
			if strconv.FormatInt(*item.Kinopoisk, 10) == kinopoiskID {
				return id
			}
		}
	}
	return ""
}

func iptvOnlinePickByTitle(items []iptvOnlineSearchEl, title, originalTitle string) string {
	wantTitle := normalizeSearchTitle(title)
	wantOriginal := normalizeSearchTitle(originalTitle)
	for _, item := range items {
		id := iptvRawString(item.ID)
		if id == "" {
			continue
		}
		orig := normalizeSearchTitle(item.OrigTitle)
		ru := normalizeSearchTitle(item.RuTitle)

		if wantOriginal != "" && orig == wantOriginal {
			return id
		}
		if wantTitle != "" && ru == wantTitle {
			return id
		}
	}
	return ""
}

func iptvRawString(raw stdjson.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := stdjson.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	var i int64
	if err := stdjson.Unmarshal(raw, &i); err == nil {
		return strconv.FormatInt(i, 10)
	}
	var f float64
	if err := stdjson.Unmarshal(raw, &f); err == nil {
		return strconv.FormatInt(int64(f), 10)
	}
	return ""
}

func iptvOnlineQuality(raw stdjson.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := stdjson.Unmarshal(raw, &s); err == nil {
		s = strings.TrimSpace(s)
		if s == "" {
			return ""
		}
		if strings.HasSuffix(strings.ToLower(s), "p") {
			return s
		}
		if _, err := strconv.Atoi(s); err == nil {
			return s + "p"
		}
		return s
	}
	var i int64
	if err := stdjson.Unmarshal(raw, &i); err == nil && i > 0 {
		return strconv.FormatInt(i, 10) + "p"
	}
	var f float64
	if err := stdjson.Unmarshal(raw, &f); err == nil && f > 0 {
		return strconv.FormatInt(int64(f), 10) + "p"
	}
	return ""
}

func writeIptvError(w http.ResponseWriter, rjson bool, msg string, status int) {
	if rjson {
		writeJSON(w, status, map[string]any{"error": msg})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(msg))
}

func urlEncodeIfNotEmpty(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	return url.QueryEscape(v)
}
