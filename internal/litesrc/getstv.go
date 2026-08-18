package litesrc

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"io"
	"lampac-go/internal/httpclient"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/kit"
)

type getsTVChecker struct {
	client *http.Client
	host   string
	token  string
}

type getsTVSearchItem struct {
	ID          string `json:"_id"`
	ContentType string `json:"contentType"`
	Poster      string `json:"poster"`
	Title       struct {
		RU string `json:"ru"`
		EN string `json:"en"`
	} `json:"title"`
	Released stdjson.RawMessage `json:"released"`
}

type getsTVMovieDetails struct {
	Type    string         `json:"type"`
	Media   []getsTVMedia  `json:"media"`
	Seasons []getsTVSeason `json:"seasons"`
}

type getsTVMedia struct {
	ID         string `json:"_id"`
	TrName     string `json:"trName"`
	SourceType string `json:"sourceType"`
}

type getsTVSeason struct {
	SeasonNum int             `json:"seasonNum"`
	Episodes  []getsTVEpisode `json:"episodes"`
}

type getsTVEpisode struct {
	EpisodeNum int        `json:"episodeNum"`
	Trs        []getsTVTr `json:"trs"`
}

type getsTVTr struct {
	ID     string `json:"_id"`
	TrID   int    `json:"trId"`
	TrName string `json:"trName"`
}

type getsTVVideoResponse struct {
	Resolutions []getsTVResolution `json:"resolutions"`
	Subtitles   []getsTVSubtitle   `json:"subtitles"`
	Media       struct {
		Movie struct {
			Title struct {
				RU string `json:"ru"`
				EN string `json:"en"`
			} `json:"title"`
		} `json:"movie"`
	} `json:"media"`
}

type getsTVResolution struct {
	URL  string `json:"url"`
	Type int    `json:"type"`
}

type getsTVSubtitle struct {
	Lang string `json:"lang"`
	URL  string `json:"url"`
}

type getsTVSimilarItem struct {
	URL     string
	Year    int
	Details string
	Title   string
	Img     string
}

func NewGetsTVChecker(cfg config.Config) *getsTVChecker {
	host := strings.TrimSpace(strings.TrimRight(cfg.Online.GetsTV.Host, "/"))
	if host != "" && !strings.Contains(host, "://") {
		host = "https://" + host
	}
	return &getsTVChecker{
		client: httpclient.NewForBalancer("getstv", 12*time.Second),
		host:   host,
		token:  strings.TrimSpace(cfg.Online.GetsTV.Token),
	}
}

// tokenForCtx returns the per-user kit token if available, otherwise the global token.
func (g *getsTVChecker) tokenForCtx(ctx context.Context) string {
	if kitToken, ok := kit.TokenOverride(ctx, "GetsTV"); ok {
		return kitToken
	}
	return g.token
}

func (g *getsTVChecker) Handle(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := strings.Trim(strings.TrimPrefix(strings.ToLower(r.URL.Path), "/lite/"), "/")

		switch raw {
		case "getstv":
			if parseBoolParam(r.URL.Query().Get("checksearch")) {
				show := g.checkSearch(r.Context(), r.URL.Query())
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusOK)
				if show {
					_, _ = w.Write([]byte(`{"type":"movie","rch":false}`))
					return
				}
				_, _ = w.Write([]byte(`{"rch":false}`))
				return
			}

			if g.host == "" || g.tokenForCtx(r.Context()) == "" {
				writeGetsTVEmpty(w, parseBoolParam(r.URL.Query().Get("rjson")))
				return
			}
			g.index(w, r)
			return

		case "getstv-search":
			if parseBoolParam(r.URL.Query().Get("checksearch")) {
				show := g.checkSearch(r.Context(), r.URL.Query())
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusOK)
				if show {
					_, _ = w.Write([]byte(`{"type":"movie","rch":false}`))
					return
				}
				_, _ = w.Write([]byte(`{"rch":false}`))
				return
			}

			if g.host == "" || g.tokenForCtx(r.Context()) == "" {
				writeGetsTVEmpty(w, parseBoolParam(r.URL.Query().Get("rjson")))
				return
			}
			g.spiderSearch(w, r)
			return

		case "getstv/video.m3u8":
			if g.host == "" || g.tokenForCtx(r.Context()) == "" {
				writeJSON(w, http.StatusOK, map[string]any{})
				return
			}
			g.video(w, r)
			return
		}

		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error":    "getstv route is not implemented in local mode",
			"balanser": raw,
		})
	}
}

func (g *getsTVChecker) index(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))

	orid := strings.TrimSpace(q.Get("orid"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	year, _ := strconv.Atoi(strings.TrimSpace(q.Get("year")))
	t, tSet := getsTVQueryInt(q.Get("t"))
	s, sSet := getsTVQueryInt(q.Get("s"))
	if !tSet {
		t = -1
	}
	if !sSet {
		s = -1
	}
	similar := parseBoolParam(q.Get("similar"))

	if orid == "" {
		source := strings.ToLower(strings.TrimSpace(q.Get("source")))
		sourceID := strings.TrimSpace(q.Get("id"))
		if source == "getstv" && sourceID != "" {
			orid = sourceID
		}
	}

	if orid == "" {
		exactID, similars, ok := g.search(r.Context(), title, originalTitle, year)
		if !ok {
			writeGetsTVEmpty(w, rjson)
			return
		}
		if exactID != "" && !similar {
			orid = exactID
		} else {
			if len(similars) == 0 {
				writeGetsTVEmpty(w, rjson)
				return
			}
			g.writeSimilar(w, r, rjson, similars)
			return
		}
	}

	root, ok := g.movieDetails(r.Context(), orid)
	if !ok {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(r)
	defaultArgs := "&orid=" + url.QueryEscape(orid) +
		"&title=" + url.QueryEscape(title) +
		"&original_title=" + url.QueryEscape(originalTitle) +
		"&year=" + strconv.Itoa(year)

	if strings.EqualFold(strings.TrimSpace(root.Type), "movie") {
		entries := make([]map[string]any, 0, len(root.Media))
		labels := make([]string, 0, len(root.Media))
		baseTitle := getsTVJoinName(title, originalTitle)

		for _, media := range root.Media {
			id := strings.TrimSpace(media.ID)
			if id == "" {
				continue
			}
			label := strings.TrimSpace(media.TrName)
			if label == "" {
				label = "Default"
			}
			link := host + "/lite/getstv/video.m3u8?id=" + url.QueryEscape(id)
			streamLink := link + "&play=true"

			entries = append(entries, map[string]any{
				"method":    "call",
				"url":       link,
				"stream":    streamLink,
				"translate": label,
				"details":   strings.TrimSpace(media.SourceType),
				"title":     fmt.Sprintf("%s (%s)", baseTitle, label),
			})
			labels = append(labels, label)
		}

		g.writeMovie(w, rjson, entries, labels)
		return
	}

	if s == -1 {
		data := make([]map[string]any, 0, len(root.Seasons))
		labels := make([]string, 0, len(root.Seasons))
		for _, season := range root.Seasons {
			if season.SeasonNum < 1 {
				continue
			}
			name := strconv.Itoa(season.SeasonNum) + " сезон"
			link := host + "/lite/getstv?rjson=" + getsTVBool(rjson) + "&s=" + strconv.Itoa(season.SeasonNum) + defaultArgs
			data = append(data, map[string]any{
				"method": "link",
				"id":     season.SeasonNum,
				"url":    link,
				"name":   name,
			})
			labels = append(labels, name)
		}

		g.writeSeason(w, rjson, data, labels)
		return
	}

	var episodes []getsTVEpisode
	for _, season := range root.Seasons {
		if season.SeasonNum == s {
			episodes = season.Episodes
			break
		}
	}
	if len(episodes) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	voiceURLs := make([]map[string]any, 0, 8)
	voiceSeen := make(map[int]struct{}, 8)
	for _, ep := range episodes {
		for _, tr := range ep.Trs {
			if _, ok := voiceSeen[tr.TrID]; ok {
				continue
			}
			voiceSeen[tr.TrID] = struct{}{}
			if t == -1 {
				t = tr.TrID
			}
			voiceURLs = append(voiceURLs, map[string]any{
				"method": "link",
				"name":   strings.TrimSpace(tr.TrName),
				"active": t == tr.TrID,
				"url": host + "/lite/getstv?rjson=" + getsTVBool(rjson) +
					"&s=" + strconv.Itoa(s) +
					"&t=" + strconv.Itoa(tr.TrID) +
					defaultArgs,
			})
		}
	}

	type episodeRow struct {
		data    map[string]any
		label   string
		episode int
	}

	rows := make([]episodeRow, 0, len(episodes))
	baseTitle := getsTVJoinName(title, originalTitle)
	for _, ep := range episodes {
		for _, tr := range ep.Trs {
			if tr.TrID != t {
				continue
			}
			id := strings.TrimSpace(tr.ID)
			if id == "" {
				continue
			}
			name := strconv.Itoa(ep.EpisodeNum) + " серия"
			link := host + "/lite/getstv/video.m3u8?id=" + url.QueryEscape(id)
			streamLink := link + "&play=true"

			rows = append(rows, episodeRow{
				data: map[string]any{
					"method": "call",
					"url":    link,
					"stream": streamLink,
					"s":      s,
					"e":      ep.EpisodeNum,
					"name":   name,
					"title":  fmt.Sprintf("%s (%d серия)", baseTitle, ep.EpisodeNum),
				},
				label:   name,
				episode: ep.EpisodeNum,
			})
			break
		}
	}

	sort.Slice(rows, func(i, j int) bool {
		return rows[i].episode < rows[j].episode
	})

	data := make([]map[string]any, 0, len(rows))
	labels := make([]string, 0, len(rows))
	seasons := make([]int, 0, len(rows))
	episodesIdx := make([]int, 0, len(rows))
	for _, row := range rows {
		data = append(data, row.data)
		labels = append(labels, row.label)
		seasons = append(seasons, s)
		episodesIdx = append(episodesIdx, row.episode)
	}

	g.writeEpisode(w, rjson, voiceURLs, data, labels, seasons, episodesIdx)
}

func (g *getsTVChecker) spiderSearch(w http.ResponseWriter, r *http.Request) {
	title := strings.TrimSpace(r.URL.Query().Get("title"))
	rjson := parseBoolParam(r.URL.Query().Get("rjson"))
	if title == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	_, similars, ok := g.search(r.Context(), title, "", 0)
	if !ok || len(similars) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	g.writeSimilar(w, r, rjson, similars)
}

func (g *getsTVChecker) video(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	root, ok := g.videoInfo(r.Context(), id)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	quality := map[string]string{}
	firstURL := ""
	for _, res := range root.Resolutions {
		link := strings.TrimSpace(res.URL)
		if link == "" {
			continue
		}
		q := "auto"
		if res.Type > 0 {
			q = strconv.Itoa(res.Type) + "p"
		}
		if firstURL == "" {
			firstURL = link
		}
		if _, ok := quality[q]; !ok {
			quality[q] = link
		}
	}
	if firstURL == "" {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	if parseBoolParam(r.URL.Query().Get("play")) {
		http.Redirect(w, r, firstURL, http.StatusFound)
		return
	}

	subtitles := make([]map[string]any, 0, len(root.Subtitles))
	for _, sub := range root.Subtitles {
		label := strings.TrimSpace(sub.Lang)
		link := strings.TrimSpace(sub.URL)
		if label == "" || link == "" {
			continue
		}
		subtitles = append(subtitles, map[string]any{
			"method": "link",
			"url":    link,
			"label":  label,
		})
	}

	titleRu := strings.TrimSpace(root.Media.Movie.Title.RU)
	titleEn := strings.TrimSpace(root.Media.Movie.Title.EN)
	name := getsTVJoinName(titleRu, titleEn)

	payload := map[string]any{
		"title":   name,
		"method":  "play",
		"url":     firstURL,
		"quality": quality,
	}
	if len(subtitles) > 0 {
		payload["subtitles"] = subtitles
	}

	writeJSON(w, http.StatusOK, payload)
}

func (g *getsTVChecker) checkSearch(ctx context.Context, q url.Values) bool {
	if g.host == "" {
		return false
	}
	if g.token == "" {
		return g.probeHost(ctx)
	}
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	year, _ := strconv.Atoi(strings.TrimSpace(q.Get("year")))
	exactID, similars, ok := g.search(ctx, title, originalTitle, year)
	if !ok {
		return g.probeHost(ctx)
	}
	return exactID != "" || len(similars) > 0
}

func (g *getsTVChecker) search(ctx context.Context, title, originalTitle string, year int) (string, []getsTVSimilarItem, bool) {
	if g.host == "" || g.tokenForCtx(ctx) == "" {
		return "", nil, false
	}

	query := strings.TrimSpace(title)
	if query == "" {
		query = strings.TrimSpace(originalTitle)
	}
	if query == "" {
		return "", nil, true
	}

	endpoint := g.host + "/api/movies?skip=0&sort=updated&searchText=" + url.QueryEscape(query)
	var root []getsTVSearchItem
	if !g.getJSON(ctx, endpoint, &root) {
		return "", nil, false
	}

	similars := make([]getsTVSimilarItem, 0, len(root))
	ids := make([]string, 0, 2)
	wantTitle := normalizeSearchTitle(title)
	wantOriginal := normalizeSearchTitle(originalTitle)

	for _, item := range root {
		name := getsTVJoinName(item.Title.RU, item.Title.EN)
		released := getsTVYearFromRaw(item.Released)
		link := "/lite/getstv?orid=" + url.QueryEscape(item.ID) +
			"&title=" + url.QueryEscape(title) +
			"&original_title=" + url.QueryEscape(originalTitle) +
			"&year=" + strconv.Itoa(year)
		similars = append(similars, getsTVSimilarItem{
			URL:     link,
			Year:    released,
			Details: strings.TrimSpace(item.ContentType),
			Title:   name,
			Img:     getsTVPoster(item.Poster),
		})

		if year <= 0 || released != year {
			continue
		}
		ru := normalizeSearchTitle(item.Title.RU)
		en := normalizeSearchTitle(item.Title.EN)
		if ru != "" && (ru == wantTitle || ru == wantOriginal) {
			ids = append(ids, item.ID)
			continue
		}
		if en != "" && (en == wantTitle || en == wantOriginal) {
			ids = append(ids, item.ID)
			continue
		}
	}

	if len(ids) == 1 {
		return ids[0], similars, true
	}

	return "", similars, true
}

func (g *getsTVChecker) movieDetails(ctx context.Context, orid string) (getsTVMovieDetails, bool) {
	var out getsTVMovieDetails
	if orid == "" {
		return out, false
	}
	endpoint := g.host + "/api/movies/" + url.PathEscape(orid)
	if !g.getJSON(ctx, endpoint, &out) {
		return out, false
	}
	return out, true
}

func (g *getsTVChecker) videoInfo(ctx context.Context, id string) (getsTVVideoResponse, bool) {
	var out getsTVVideoResponse
	if id == "" {
		return out, false
	}
	endpoint := g.host + "/api/media/" + url.PathEscape(id) + "?format=m3u8&protocol=https"
	if !g.getJSON(ctx, endpoint, &out) {
		return out, false
	}
	return out, true
}

func (g *getsTVChecker) getJSON(ctx context.Context, target string, out any) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("X-Lampac-Go", "1")
	if token := g.tokenForCtx(ctx); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := g.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false
	}

	dec := stdjson.NewDecoder(io.LimitReader(resp.Body, 8<<20))
	if err := dec.Decode(out); err != nil {
		return false
	}
	return true
}

func (g *getsTVChecker) probeHost(ctx context.Context) bool {
	return g.probe(ctx, http.MethodHead, g.host) || g.probe(ctx, http.MethodGet, g.host)
}

func (g *getsTVChecker) probe(ctx context.Context, method, target string) bool {
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/json,*/*")
	req.Header.Set("X-Lampac-Go", "1")

	resp, err := g.client.Do(req)
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

func (g *getsTVChecker) writeMovie(w http.ResponseWriter, rjson bool, data []map[string]any, labels []string) {
	if len(data) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "movie",
			"data": data,
		})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range data {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, 0, 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (g *getsTVChecker) writeSeason(w http.ResponseWriter, rjson bool, data []map[string]any, labels []string) {
	if len(data) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "season",
			"data": data,
		})
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

func (g *getsTVChecker) writeEpisode(w http.ResponseWriter, rjson bool, voices []map[string]any, data []map[string]any, labels []string, seasons []int, episodes []int) {
	if len(data) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	if rjson {
		payload := map[string]any{
			"type": "episode",
			"data": data,
		}
		if len(voices) > 0 {
			payload["voice"] = voices
		}
		writeJSON(w, http.StatusOK, payload)
		return
	}

	var sb strings.Builder
	if len(voices) > 0 {
		sb.WriteString(`<div class="videos__line">`)
		for _, row := range voices {
			getsTVAppendVoiceHTML(&sb, row)
		}
		sb.WriteString(`</div>`)
	}

	sb.WriteString(`<div class="videos__line">`)
	for i, row := range data {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, seasons[i], episodes[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (g *getsTVChecker) writeSimilar(w http.ResponseWriter, r *http.Request, rjson bool, items []getsTVSimilarItem) {
	if len(items) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(r)
	data := make([]map[string]any, 0, len(items))
	labels := make([]string, 0, len(items))
	for _, item := range items {
		link := item.URL
		if strings.HasPrefix(link, "/") {
			link = host + link
		}
		data = append(data, map[string]any{
			"method":  "link",
			"url":     link,
			"similar": true,
			"year":    item.Year,
			"details": item.Details,
			"title":   item.Title,
			"img":     item.Img,
		})
		labels = append(labels, item.Title)
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "similar",
			"data": data,
		})
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

func getsTVPoster(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if strings.Contains(v, "://") {
		return v
	}
	return "https://img.getstv.com/poster/cover/345x518/" + v + ".jpg"
}

func getsTVYearFromRaw(raw stdjson.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}

	var s string
	if err := stdjson.Unmarshal(raw, &s); err == nil {
		s = strings.TrimSpace(s)
		if s == "" {
			return 0
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t.Year()
		}
		if t, err := time.Parse("2006-01-02", s); err == nil {
			return t.Year()
		}
		if len(s) >= 4 {
			if y, err := strconv.Atoi(s[:4]); err == nil {
				return y
			}
		}
	}

	var unixTs int64
	if err := stdjson.Unmarshal(raw, &unixTs); err == nil && unixTs > 0 {
		if unixTs > 1_000_000_000_000 {
			return time.UnixMilli(unixTs).Year()
		}
		return time.Unix(unixTs, 0).Year()
	}

	var f float64
	if err := stdjson.Unmarshal(raw, &f); err == nil && f > 0 {
		unix := int64(f)
		if unix > 1_000_000_000_000 {
			return time.UnixMilli(unix).Year()
		}
		return time.Unix(unix, 0).Year()
	}

	return 0
}
