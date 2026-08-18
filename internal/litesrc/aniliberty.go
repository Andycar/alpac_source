package litesrc

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"io"
	"lampac-go/internal/httpclient"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/proxylink"
)

type anilibertyChecker struct {
	client *http.Client
	host   string
}

var (
	anilibertyAliasNRe      = regexp.MustCompile(`-([0-9]+)(nd|th)`)
	anilibertyAliasSeasonRe = regexp.MustCompile(`season-([0-9]+)`)
)

type anilibertySearchItem struct {
	Name struct {
		Main    string `json:"main"`
		English string `json:"english"`
	} `json:"name"`
	ID     int `json:"id"`
	Year   int `json:"year"`
	Poster struct {
		Src string `json:"src"`
	} `json:"poster"`
}

type anilibertyRelease struct {
	Alias    string              `json:"alias"`
	Episodes []anilibertyEpisode `json:"episodes"`
}

type anilibertyEpisode struct {
	Ordinal string `json:"ordinal"`
	Name    string `json:"name"`

	HLS1080 string `json:"hls_1080"`
	HLS720  string `json:"hls_720"`
	HLS480  string `json:"hls_480"`
}

func NewAnilibertyChecker(cfg config.Config) *anilibertyChecker {
	host := strings.TrimSpace(cfg.Online.AniLiberty.Host)
	if host == "" {
		host = "https://api.anilibria.app"
	}
	host = strings.TrimRight(host, "/")

	return &anilibertyChecker{
		client: httpclient.NewForBalancer("aniliberty", 10*time.Second),
		host:   host,
	}
}

func (a *anilibertyChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show := a.checkSearch(req.Context(), req.URL.Query())
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			if show {
				_, _ = w.Write([]byte(`{"type":"movie","rch":false}`))
				return
			}
			_, _ = w.Write([]byte(`{"rch":false}`))
			return
		}

		a.index(w, req, links)
	}
}

func (a *anilibertyChecker) checkSearch(ctx context.Context, q url.Values) bool {
	title := strings.TrimSpace(q.Get("title"))
	if title == "" {
		return false
	}
	year, _ := strconv.Atoi(strings.TrimSpace(q.Get("year")))

	items, ok := a.search(ctx, title)
	if !ok || len(items) == 0 {
		return false
	}

	want := normalizeSearchTitle(title)
	filtered := make([]anilibertySearchItem, 0, len(items))
	for _, item := range items {
		mainName := normalizeSearchTitle(item.Name.Main)
		enName := normalizeSearchTitle(item.Name.English)
		matched := (mainName != "" && strings.HasPrefix(mainName, want)) || (enName != "" && strings.HasPrefix(enName, want))
		if !matched {
			continue
		}
		filtered = append(filtered, item)
		if year > 0 && item.Year > 0 && item.Year != year && item.Year != year-1 && item.Year != year+1 {
			continue
		}
		return true
	}

	if len(filtered) > 0 {
		return true
	}
	return len(items) > 0
}

func (a *anilibertyChecker) index(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	title := strings.TrimSpace(q.Get("title"))
	releases := strings.TrimSpace(q.Get("releases"))
	similar := parseBoolParam(q.Get("similar"))

	if releases == "" {
		if title == "" {
			writeGetsTVEmpty(w, rjson)
			return
		}

		items, ok := a.search(req.Context(), title)
		if !ok || len(items) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}

		stitle := normalizeSearchTitle(title)
		filtered := make([]anilibertySearchItem, 0, len(items))
		for _, item := range items {
			mainName := normalizeSearchTitle(item.Name.Main)
			enName := normalizeSearchTitle(item.Name.English)
			if similar || (mainName != "" && strings.HasPrefix(mainName, stitle)) || (enName != "" && strings.HasPrefix(enName, stitle)) {
				filtered = append(filtered, item)
			}
		}
		if len(filtered) == 0 && !similar {
			filtered = items
		}
		if len(filtered) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}

		if !similar && len(filtered) == 1 {
			releases = strconv.Itoa(filtered[0].ID)
		} else {
			a.writeSimilar(w, req, rjson, title, filtered)
			return
		}
	}

	root, ok := a.fetchRelease(req.Context(), releases)
	if !ok || len(root.Episodes) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	season := anilibertySeason(root.Alias)
	rows := make([]map[string]any, 0, len(root.Episodes))
	for _, episode := range root.Episodes {
		ordinal := strings.TrimSpace(episode.Ordinal)
		if ordinal == "" {
			continue
		}

		streams := a.buildStreams(episode)
		if len(streams) == 0 {
			continue
		}
		for i, s := range streams {
			if u, ok := s["url"].(string); ok {
				streams[i]["url"] = streamProxyURL(req, u, "aniliberty", links)
			}
		}

		epName := strings.TrimSpace(episode.Name)
		if epName == "" {
			epName = ordinal + " серия"
		}
		epNum, _ := strconv.Atoi(ordinal)

		rows = append(rows, map[string]any{
			"method":        "call",
			"url":           streams[0]["url"],
			"stream":        streams[0]["url"],
			"s":             season,
			"e":             epNum,
			"name":          epName,
			"title":         title,
			"streamquality": streams,
		})
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
		name := strings.TrimSpace(fmt.Sprint(row["name"]))
		if name == "" {
			name = "Серия"
		}
		getsTVAppendMovieHTML(&sb, row, name, i == 0, anilibriaInt(row["s"]), anilibriaInt(row["e"]))
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (a *anilibertyChecker) search(ctx context.Context, title string) ([]anilibertySearchItem, bool) {
	u, err := url.Parse(a.host + "/api/v1/app/search/releases")
	if err != nil {
		return nil, false
	}
	qs := url.Values{}
	qs.Set("query", title)
	u.RawQuery = qs.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, false
	}
	httpReq.Header.Set("Accept", "application/json,text/plain,*/*")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	httpReq.Header.Set("X-Lampac-Go", "1")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false
	}

	var items []anilibertySearchItem
	if err := stdjson.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&items); err != nil {
		return nil, false
	}
	return items, true
}

func (a *anilibertyChecker) fetchRelease(ctx context.Context, releases string) (anilibertyRelease, bool) {
	var out anilibertyRelease
	releases = strings.TrimSpace(releases)
	if releases == "" {
		return out, false
	}

	target := strings.TrimRight(a.host, "/") + "/api/v1/anime/releases/" + url.PathEscape(releases)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return out, false
	}
	httpReq.Header.Set("Accept", "application/json,text/plain,*/*")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	httpReq.Header.Set("X-Lampac-Go", "1")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return out, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return out, false
	}

	if err := stdjson.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&out); err != nil {
		return out, false
	}
	if len(out.Episodes) == 0 {
		return out, false
	}
	return out, true
}

func (a *anilibertyChecker) writeSimilar(w http.ResponseWriter, req *http.Request, rjson bool, title string, items []anilibertySearchItem) {
	if len(items) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(req)
	rows := make([]map[string]any, 0, len(items))
	labels := make([]string, 0, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Name.Main)
		if name != "" && strings.TrimSpace(item.Name.English) != "" {
			name += " / " + strings.TrimSpace(item.Name.English)
		} else if name == "" {
			name = strings.TrimSpace(item.Name.English)
		}
		if name == "" || item.ID == 0 {
			continue
		}

		img := anilibertyImageURL(strings.TrimSpace(a.host), strings.TrimSpace(item.Poster.Src))
		link := host + "/lite/aniliberty?rjson=" + getsTVBool(rjson) +
			"&title=" + url.QueryEscape(title) +
			"&releases=" + strconv.Itoa(item.ID)

		rows = append(rows, map[string]any{
			"method":  "link",
			"url":     link,
			"similar": true,
			"year":    item.Year,
			"details": "",
			"title":   name,
			"img":     img,
		})
		labels = append(labels, name)
	}

	if len(rows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "similar",
			"data": rows,
		})
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

func anilibertySeason(alias string) int {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return 1
	}
	if m := submatch1(anilibertyAliasNRe, alias); m != "" {
		if n, err := strconv.Atoi(m); err == nil && n > 0 {
			return n
		}
	}
	if m := submatch1(anilibertyAliasSeasonRe, alias); m != "" {
		if n, err := strconv.Atoi(m); err == nil && n > 0 {
			return n
		}
	}
	return 1
}

func (a *anilibertyChecker) buildStreams(ep anilibertyEpisode) []map[string]any {
	type q struct {
		label string
		raw   string
	}
	raw := []q{
		{label: "1080p", raw: strings.TrimSpace(ep.HLS1080)},
		{label: "720p", raw: strings.TrimSpace(ep.HLS720)},
		{label: "480p", raw: strings.TrimSpace(ep.HLS480)},
	}

	out := make([]map[string]any, 0, 3)
	for _, item := range raw {
		if item.raw == "" {
			continue
		}
		link := item.raw
		if strings.HasPrefix(link, "//") {
			link = "https:" + link
		}
		out = append(out, map[string]any{
			"quality": item.label,
			"url":     link,
		})
	}
	return out
}

func anilibertyImageURL(host, src string) string {
	if src == "" {
		return ""
	}
	if strings.Contains(src, "://") {
		return src
	}
	host = strings.TrimRight(strings.TrimSpace(host), "/")
	if host == "" {
		return src
	}
	if strings.HasPrefix(src, "/") {
		return host + src
	}
	return host + "/" + src
}
