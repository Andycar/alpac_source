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
	"sort"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/proxylink"

	"github.com/rs/zerolog/log"
)

type anilibriaChecker struct {
	client *http.Client
	host   string
}

var (
	anilibriaCodeNRe      = regexp.MustCompile(`-([0-9]+)(nd|th)`)
	anilibriaCodeSeasonRe = regexp.MustCompile(`season-([0-9]+)`)
)

// ---------------------------------------------------------------------------
// New API v1 data structures (anilibria.top)
// ---------------------------------------------------------------------------

type anilibriaItem struct {
	ID   int `json:"id"`
	Year int `json:"year"`
	Name struct {
		Main        string `json:"main"`
		English     string `json:"english"`
		Alternative string `json:"alternative"`
	} `json:"name"`
	Alias  string `json:"alias"`
	Poster struct {
		Src       string `json:"src"`
		Optimized struct {
			Src string `json:"src"`
		} `json:"optimized"`
	} `json:"poster"`
	Episodes []anilibriaEpisode `json:"episodes"`
}

type anilibriaEpisode struct {
	ID      string `json:"id"`
	Ordinal int    `json:"ordinal"`
	Name    string `json:"name"`
	HLS480  string `json:"hls_480"`
	HLS720  string `json:"hls_720"`
	HLS1080 string `json:"hls_1080"`
}

const anilibriaPosterBase = "https://anilibria.top"

func NewAnilibriaChecker(cfg config.Config) *anilibriaChecker {
	host := strings.TrimSpace(cfg.Online.Anilibria.Host)
	if host == "" {
		host = "https://anilibria.top"
	}
	// API v2 (api.anilibria.tv) is deprecated (returns 410 Gone).
	// Force migration to v1 API at anilibria.top.
	if strings.Contains(host, "api.anilibria.tv") {
		host = "https://anilibria.top"
	}
	host = strings.TrimRight(host, "/")

	return &anilibriaChecker{
		client: httpclient.NewForBalancer("anilibria", 10*time.Second),
		host:   host,
	}
}

func (a *anilibriaChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show := a.checkSearch(req.Context(), req.URL.Query())
			writeCheckSearchResponseNoRCH(w, show, pluginQualityBadgeGet("anilibria"))
			return
		}

		a.index(w, req, links)
	}
}

func (a *anilibriaChecker) checkSearch(ctx context.Context, q url.Values) bool {
	title := strings.TrimSpace(q.Get("title"))
	if title == "" {
		return false
	}
	year, _ := strconv.Atoi(strings.TrimSpace(q.Get("year")))

	items, ok := a.fetch(ctx, title)
	if !ok || len(items) == 0 {
		return false
	}

	want := normalizeSearchTitle(title)
	for _, item := range items {
		ru := normalizeSearchTitle(item.Name.Main)
		en := normalizeSearchTitle(item.Name.English)
		matched := (ru != "" && strings.HasPrefix(ru, want)) || (en != "" && strings.HasPrefix(en, want))
		if !matched {
			continue
		}
		if year > 0 && item.Year > 0 && item.Year != year && item.Year != year-1 && item.Year != year+1 {
			continue
		}
		return true
	}

	return len(items) > 0
}

func (a *anilibriaChecker) index(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	title := strings.TrimSpace(q.Get("title"))
	if title == "" {
		writeGetsTVEmpty(w, parseBoolParam(q.Get("rjson")))
		return
	}

	rjson := parseBoolParam(q.Get("rjson"))
	similar := parseBoolParam(q.Get("similar"))
	code := strings.TrimSpace(q.Get("code")) // alias
	year, _ := strconv.Atoi(strings.TrimSpace(q.Get("year")))
	releaseID, _ := strconv.Atoi(strings.TrimSpace(q.Get("release_id")))

	// If we have a release_id, fetch that specific release.
	if releaseID > 0 || code != "" {
		var item *anilibriaItem
		var ok bool

		if releaseID > 0 {
			item, ok = a.fetchRelease(req.Context(), releaseID)
		} else {
			// Find by alias in search results.
			items, fetchOK := a.fetch(req.Context(), title)
			if fetchOK {
				for i := range items {
					if items[i].Alias == code {
						item = &items[i]
						ok = true
						break
					}
				}
			}
		}

		if !ok || item == nil {
			writeGetsTVEmpty(w, rjson)
			return
		}

		// If episodes are empty (search results don't include episodes), fetch full release.
		if len(item.Episodes) == 0 && item.ID > 0 {
			full, fullOK := a.fetchRelease(req.Context(), item.ID)
			if fullOK && full != nil {
				item = full
			}
		}

		rows := a.buildEpisodeRows(req, title, *item, code, links)
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
		return
	}

	// Search mode.
	items, ok := a.fetch(req.Context(), title)
	if !ok || len(items) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	candidates := a.filterByTitle(items, title)
	if len(candidates) == 0 {
		candidates = items
	}

	stitle := normalizeSearchTitle(title)
	exact := false
	if !similar {
		if len(candidates) == 1 {
			item := candidates[0]
			ru := normalizeSearchTitle(item.Name.Main)
			en := normalizeSearchTitle(item.Name.English)
			if item.Year == year && (ru == stitle || en == stitle) {
				exact = true
			}
		}
	}

	if !exact {
		a.writeSimilar(w, req, rjson, title, candidates)
		return
	}

	root := candidates[0]

	// Fetch full release to get episodes.
	if len(root.Episodes) == 0 && root.ID > 0 {
		full, fullOK := a.fetchRelease(req.Context(), root.ID)
		if fullOK && full != nil {
			root = *full
		}
	}

	rows := a.buildEpisodeRows(req, title, root, root.Alias, links)
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

// ---------------------------------------------------------------------------
// API v1 fetch: search & release
// ---------------------------------------------------------------------------

func (a *anilibriaChecker) fetch(ctx context.Context, title string) ([]anilibriaItem, bool) {
	u := a.host + "/api/v1/app/search/releases?query=" + url.QueryEscape(title)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		log.Debug().Err(err).Str("url", u).Msg("anilibria: fetch request build failed")
		return nil, false
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		log.Debug().Err(err).Str("url", u).Msg("anilibria: fetch request failed")
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Debug().Int("status", resp.StatusCode).Str("url", u).Msg("anilibria: fetch bad status")
		return nil, false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		log.Debug().Err(err).Str("url", u).Msg("anilibria: fetch read body failed")
		return nil, false
	}

	var items []anilibriaItem
	if err := stdjson.Unmarshal(body, &items); err != nil {
		log.Debug().Err(err).Str("url", u).Str("body_prefix", string(body[:min(len(body), 200)])).Msg("anilibria: fetch JSON decode failed")
		return nil, false
	}
	log.Debug().Int("count", len(items)).Str("url", u).Msg("anilibria: fetch OK")
	return items, true
}

func (a *anilibriaChecker) fetchRelease(ctx context.Context, id int) (*anilibriaItem, bool) {
	u := fmt.Sprintf("%s/api/v1/anime/releases/%d", a.host, id)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		log.Debug().Err(err).Str("url", u).Msg("anilibria: fetchRelease request build failed")
		return nil, false
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		log.Debug().Err(err).Str("url", u).Msg("anilibria: fetchRelease request failed")
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Debug().Int("status", resp.StatusCode).Str("url", u).Msg("anilibria: fetchRelease bad status")
		return nil, false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		log.Debug().Err(err).Str("url", u).Msg("anilibria: fetchRelease read body failed")
		return nil, false
	}

	var item anilibriaItem
	if err := stdjson.Unmarshal(body, &item); err != nil {
		log.Debug().Err(err).Str("url", u).Str("body_prefix", string(body[:min(len(body), 200)])).Msg("anilibria: fetchRelease JSON decode failed")
		return nil, false
	}
	log.Debug().Int("id", item.ID).Int("episodes", len(item.Episodes)).Str("url", u).Msg("anilibria: fetchRelease OK")
	return &item, true
}

// ---------------------------------------------------------------------------
// Filter / Select
// ---------------------------------------------------------------------------

func (a *anilibriaChecker) filterByTitle(items []anilibriaItem, title string) []anilibriaItem {
	if len(items) == 0 {
		return nil
	}
	stitle := normalizeSearchTitle(title)
	if stitle == "" {
		return items
	}

	out := make([]anilibriaItem, 0, len(items))
	for _, item := range items {
		ru := normalizeSearchTitle(item.Name.Main)
		en := normalizeSearchTitle(item.Name.English)
		if (ru != "" && strings.HasPrefix(ru, stitle)) || (en != "" && strings.HasPrefix(en, stitle)) {
			out = append(out, item)
		}
	}
	if len(out) == 0 {
		return items
	}
	return out
}

// ---------------------------------------------------------------------------
// Similar results
// ---------------------------------------------------------------------------

func (a *anilibriaChecker) writeSimilar(w http.ResponseWriter, req *http.Request, rjson bool, title string, items []anilibriaItem) {
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
		if name == "" {
			continue
		}

		img := strings.TrimSpace(item.Poster.Optimized.Src)
		if img == "" {
			img = strings.TrimSpace(item.Poster.Src)
		}
		if img != "" && !strings.Contains(img, "://") {
			img = anilibriaPosterBase + img
		}

		link := host + "/lite/anilibria?title=" + url.QueryEscape(title)
		if item.ID > 0 {
			link += "&release_id=" + strconv.Itoa(item.ID)
		}
		if strings.TrimSpace(item.Alias) != "" {
			link += "&code=" + url.QueryEscape(strings.TrimSpace(item.Alias))
		}
		if rjson {
			link += "&rjson=true"
		}

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

// ---------------------------------------------------------------------------
// Episode rows
// ---------------------------------------------------------------------------

func (a *anilibriaChecker) buildEpisodeRows(req *http.Request, title string, root anilibriaItem, code string, links *proxylink.Manager) []map[string]any {
	if len(root.Episodes) == 0 {
		return nil
	}

	// Sort episodes by ordinal.
	eps := make([]anilibriaEpisode, len(root.Episodes))
	copy(eps, root.Episodes)
	sort.Slice(eps, func(i, j int) bool { return eps[i].Ordinal < eps[j].Ordinal })

	season := anilibriaDeriveSeason(title, root, code)
	out := make([]map[string]any, 0, len(eps))
	for _, ep := range eps {
		streams := anilibriaBuildStreamsV1(ep)
		if len(streams) == 0 {
			continue
		}
		for i, s := range streams {
			if u, ok := s["url"].(string); ok {
				streams[i]["url"] = streamProxyURL(req, u, "anilibria", links)
			}
		}
		name := strconv.Itoa(ep.Ordinal) + " серия"
		if ep.Name != "" {
			name = strconv.Itoa(ep.Ordinal) + " - " + ep.Name
		}
		out = append(out, map[string]any{
			"method":        "play",
			"url":           streams[0]["url"],
			"stream":        streams[0]["url"],
			"s":             season,
			"e":             ep.Ordinal,
			"name":          name,
			"title":         title,
			"streamquality": streams,
		})
	}
	return out
}

func anilibriaDeriveSeason(title string, root anilibriaItem, code string) int {
	stitle := normalizeSearchTitle(title)
	if normalizeSearchTitle(root.Name.Main) == stitle || normalizeSearchTitle(root.Name.English) == stitle {
		return 1
	}

	if m := submatch1(anilibriaCodeNRe, code); m != "" {
		if n, err := strconv.Atoi(m); err == nil {
			return n
		}
	}
	if m := submatch1(anilibriaCodeSeasonRe, code); m != "" {
		if n, err := strconv.Atoi(m); err == nil {
			return n
		}
	}
	if strings.TrimSpace(code) == "" {
		return 0
	}
	return 1
}

// anilibriaBuildStreamsV1 builds stream quality list from API v1 episode.
func anilibriaBuildStreamsV1(ep anilibriaEpisode) []map[string]any {
	type q struct {
		label string
		url   string
	}
	qualities := []q{
		{label: "1080p", url: strings.TrimSpace(ep.HLS1080)},
		{label: "720p", url: strings.TrimSpace(ep.HLS720)},
		{label: "480p", url: strings.TrimSpace(ep.HLS480)},
	}

	out := make([]map[string]any, 0, 3)
	for _, item := range qualities {
		if item.url == "" {
			continue
		}
		out = append(out, map[string]any{
			"quality": item.label,
			"url":     item.url,
		})
	}
	return out
}

func anilibriaInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int32:
		return int(x)
	case int64:
		return int(x)
	case float64:
		return int(x)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(x))
		return n
	default:
		return 0
	}
}
