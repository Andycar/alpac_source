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
)

var (
	animegoPIDRe          = regexp.MustCompile(`(?is)data-ajax-url="/[^"]+-([0-9]+)"`)
	animegoTitleRe        = regexp.MustCompile(`(?is)card-title text-truncate"><a [^>]+>([^<]+)<`)
	animegoYearRe         = regexp.MustCompile(`(?is)class="anime-year"><a [^>]+>([0-9]{4})<`)
	animegoImageRe        = regexp.MustCompile(`(?is)data-original="([^"]+)"`)
	animegoPlayerBaseRe   = regexp.MustCompile(`(?is)data-player="(?:https?:)?//(aniboom\.[^/]+)/embed/([^"?&]+)\?episode=1(?:&amp;|&)translation=([0-9]+)"`)
	animegoEpisodeRe      = regexp.MustCompile(`(?is)data-episode="([0-9]+)"`)
	animegoTranslationRe  = regexp.MustCompile(`(?is)data-player="(?:https?:)?//aniboom\.[^/]+/embed/[^"?&]+\?episode=[0-9]+(?:&amp;|&)translation=([0-9]+)"\s+data-provider="[0-9]+"\s+data-provide-dubbing="([0-9]+)"`)
	animegoPlayerHLSRe    = regexp.MustCompile(`(?is)"hls":"\{"src":"(?:https?:)?(//[^"]+\.m3u8[^"]*)`)
	animegoWhitespaceTrim = regexp.MustCompile(`\s+`)
)

type animegoCatalogItem struct {
	Title  string
	Year   int
	PID    string
	Season int
	Img    string
}

type animegoPlayerResponse struct {
	Content string `json:"content"`
}

type animegoTranslation struct {
	ID   string
	Name string
}

type animegoPlayerData struct {
	EmbedHost          string
	Token              string
	DefaultTranslation string
	Episodes           []int
	Translations       []animegoTranslation
}

type animegoChecker struct {
	client *http.Client
	host   string
}

func NewAnimegoChecker(cfg config.Config) *animegoChecker {
	host := strings.TrimSpace(cfg.Online.AnimeGo.Host)
	if host == "" {
		host = "https://animego.me"
	}
	host = strings.TrimRight(host, "/")

	return &animegoChecker{
		client: httpclient.NewForBalancer("animego", 10*time.Second),
		host:   host,
	}
}

func (a *animegoChecker) Handle(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		raw := strings.Trim(strings.TrimPrefix(strings.ToLower(req.URL.Path), "/lite/"), "/")
		switch raw {
		case "animego":
			if parseBoolParam(req.URL.Query().Get("checksearch")) {
				show := a.checkSearch(req.Context(), req.URL.Query())
				writeCheckSearchResponseNoRCH(w, show, pluginQualityBadgeGet("animego"))
				return
			}

			a.index(w, req)
			return

		case "animego/video", "animego/video.m3u8":
			a.video(w, req)
			return
		}

		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error":    "animego route is not yet implemented",
			"balanser": raw,
		})
	}
}

func (a *animegoChecker) checkSearch(ctx context.Context, q url.Values) bool {
	title := strings.TrimSpace(q.Get("title"))
	if title == "" {
		return false
	}
	year := animegoIntOrDefault(q.Get("year"), 0)

	catalog, ok := a.search(ctx, title, year)
	if !ok {
		return false
	}
	return len(catalog) > 0
}

func (a *animegoChecker) index(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))

	title := strings.TrimSpace(q.Get("title"))
	if title == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}
	year := animegoIntOrDefault(q.Get("year"), 0)
	pid := strings.TrimSpace(q.Get("pid"))
	season := animegoIntOrDefault(q.Get("s"), 0)
	similar := parseBoolParam(q.Get("similar"))
	selectedTranslation := strings.TrimSpace(q.Get("t"))

	if pid == "" {
		catalog, ok := a.search(req.Context(), title, year)
		if !ok || len(catalog) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}
		if !similar && len(catalog) == 1 {
			pid = catalog[0].PID
			if season == 0 {
				season = catalog[0].Season
			}
		} else {
			a.writeSimilar(w, req, rjson, title, catalog)
			return
		}
	}

	player, ok := a.fetchPlayer(req.Context(), pid)
	if !ok || player.Token == "" || player.EmbedHost == "" || len(player.Episodes) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	if selectedTranslation == "" {
		selectedTranslation = player.DefaultTranslation
	}
	if selectedTranslation == "" && len(player.Translations) > 0 {
		selectedTranslation = player.Translations[0].ID
	}
	if selectedTranslation == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(req)
	voiceRows := make([]map[string]any, 0, len(player.Translations))
	for _, tr := range player.Translations {
		voiceRows = append(voiceRows, map[string]any{
			"method": "link",
			"name":   tr.Name,
			"active": strings.EqualFold(tr.ID, selectedTranslation),
			"url": host + "/lite/animego?rjson=" + getsTVBool(rjson) +
				"&title=" + url.QueryEscape(title) +
				"&pid=" + url.QueryEscape(pid) +
				"&s=" + strconv.Itoa(season) +
				"&t=" + url.QueryEscape(tr.ID),
		})
	}

	baseTitle := getsTVJoinName(title, "")
	rows := make([]map[string]any, 0, len(player.Episodes))
	labels := make([]string, 0, len(player.Episodes))
	seasons := make([]int, 0, len(player.Episodes))
	episodes := make([]int, 0, len(player.Episodes))
	for _, ep := range player.Episodes {
		stream := host + "/lite/animego/video.m3u8?host=" + url.QueryEscape(player.EmbedHost) +
			"&token=" + url.QueryEscape(player.Token) +
			"&e=" + strconv.Itoa(ep) +
			"&t=" + url.QueryEscape(selectedTranslation)
		streamPlay := stream + "&play=true"

		label := strconv.Itoa(ep) + " серия"
		rows = append(rows, map[string]any{
			"method": "call",
			"url":    streamPlay,
			"stream": streamPlay,
			"name":   label,
			"s":      season,
			"e":      ep,
			"title":  fmt.Sprintf("%s (%s)", baseTitle, label),
		})
		labels = append(labels, label)
		seasons = append(seasons, season)
		episodes = append(episodes, ep)
	}
	if len(rows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		payload := map[string]any{
			"type": "episode",
			"data": rows,
		}
		if len(voiceRows) > 0 {
			payload["voice"] = voiceRows
		}
		writeJSON(w, http.StatusOK, payload)
		return
	}

	var sb strings.Builder
	if len(voiceRows) > 0 {
		sb.WriteString(`<div class="videos__line">`)
		for _, row := range voiceRows {
			getsTVAppendVoiceHTML(&sb, row)
		}
		sb.WriteString(`</div>`)
	}
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range rows {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, seasons[i], episodes[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (a *animegoChecker) video(w http.ResponseWriter, req *http.Request) {
	host := strings.TrimSpace(req.URL.Query().Get("host"))
	token := strings.TrimSpace(req.URL.Query().Get("token"))
	translation := strings.TrimSpace(req.URL.Query().Get("t"))
	episode := animegoIntOrDefault(req.URL.Query().Get("e"), 0)
	if host == "" || token == "" || translation == "" || episode <= 0 {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	stream, ok := a.fetchVideo(req.Context(), host, token, translation, episode)
	if !ok || stream == "" {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	if parseBoolParam(req.URL.Query().Get("play")) {
		http.Redirect(w, req, stream, http.StatusFound)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"title":   strings.TrimSpace(req.URL.Query().Get("title")),
		"method":  "play",
		"url":     stream,
		"quality": map[string]string{"auto": stream},
	})
}

func (a *animegoChecker) writeSimilar(w http.ResponseWriter, req *http.Request, rjson bool, title string, catalog []animegoCatalogItem) {
	if len(catalog) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(req)
	rows := make([]map[string]any, 0, len(catalog))
	labels := make([]string, 0, len(catalog))
	for _, item := range catalog {
		if item.PID == "" || strings.TrimSpace(item.Title) == "" {
			continue
		}
		link := host + "/lite/animego?rjson=" + getsTVBool(rjson) +
			"&title=" + url.QueryEscape(title) +
			"&pid=" + url.QueryEscape(item.PID) +
			"&s=" + strconv.Itoa(item.Season)
		rows = append(rows, map[string]any{
			"method":  "link",
			"url":     link,
			"similar": true,
			"year":    item.Year,
			"details": "",
			"title":   item.Title,
			"img":     animegoImageURL(a.host, item.Img),
		})
		labels = append(labels, item.Title)
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

func (a *animegoChecker) search(ctx context.Context, title string, year int) ([]animegoCatalogItem, bool) {
	search, ok := a.fetchSearch(ctx, title)
	if !ok {
		return nil, false
	}

	want := normalizeSearchTitle(title)
	parts := strings.Split(search, `class="p-poster__stack"`)
	out := make([]animegoCatalogItem, 0, len(parts))
	for _, part := range parts {
		pid := strings.TrimSpace(submatch1(animegoPIDRe, part))
		name := strings.TrimSpace(submatch1(animegoTitleRe, part))
		if pid == "" || name == "" {
			continue
		}

		cleanName := normalizeSearchTitle(name)
		if cleanName == "" || !strings.Contains(cleanName, want) {
			continue
		}

		rowYear := animegoIntOrDefault(submatch1(animegoYearRe, part), 0)
		rowSeason := 0
		if year > 0 && rowYear == year && cleanName == want {
			rowSeason = 1
		}

		out = append(out, animegoCatalogItem{
			Title:  animegoCleanSpace(name),
			Year:   rowYear,
			PID:    pid,
			Season: rowSeason,
			Img:    strings.TrimSpace(submatch1(animegoImageRe, part)),
		})
	}
	return out, true
}

func (a *animegoChecker) fetchSearch(ctx context.Context, title string) (string, bool) {
	u, err := url.Parse(a.host + "/search/anime")
	if err != nil {
		return "", false
	}
	qs := url.Values{}
	qs.Set("q", title)
	u.RawQuery = qs.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", false
	}
	httpReq.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	httpReq.Header.Set("X-Lampac-Go", "1")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", false
	}
	return string(body), true
}

func (a *animegoChecker) fetchPlayer(ctx context.Context, pid string) (animegoPlayerData, bool) {
	var out animegoPlayerData
	if pid == "" {
		return out, false
	}

	target := a.host + "/anime/" + url.PathEscape(pid) + "/player?_allow=true"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return out, false
	}
	req.Header.Set("Accept", "application/json,text/plain,*/*")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("DNT", "1")
	req.Header.Set("Pragma", "no-cache")
	req.Header.Set("Referer", a.host+"/")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("X-Lampac-Go", "1")

	resp, err := a.client.Do(req)
	if err != nil {
		return out, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return out, false
	}

	var root animegoPlayerResponse
	if err := stdjson.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&root); err != nil {
		return out, false
	}
	content := strings.TrimSpace(root.Content)
	if content == "" {
		return out, false
	}
	return animegoParsePlayerContent(content)
}

func animegoParsePlayerContent(content string) (animegoPlayerData, bool) {
	var out animegoPlayerData

	base := animegoPlayerBaseRe.FindStringSubmatch(content)
	if len(base) < 4 {
		return out, false
	}
	out.EmbedHost = strings.TrimSpace(base[1])
	out.Token = strings.TrimSpace(base[2])
	out.DefaultTranslation = strings.TrimSpace(base[3])
	if out.EmbedHost == "" || out.Token == "" || out.DefaultTranslation == "" {
		return out, false
	}

	seenEpisodes := map[int]struct{}{}
	for _, m := range animegoEpisodeRe.FindAllStringSubmatch(content, -1) {
		ep := animegoIntOrDefault(m[1], 0)
		if ep <= 0 {
			continue
		}
		if _, ok := seenEpisodes[ep]; ok {
			continue
		}
		seenEpisodes[ep] = struct{}{}
		out.Episodes = append(out.Episodes, ep)
	}
	if len(out.Episodes) == 0 {
		return out, false
	}
	sort.Ints(out.Episodes)

	seenTranslations := map[string]struct{}{}
	for _, m := range animegoTranslationRe.FindAllStringSubmatch(content, -1) {
		if len(m) < 3 {
			continue
		}
		translationID := strings.TrimSpace(m[1])
		dubbingID := strings.TrimSpace(m[2])
		if translationID == "" || dubbingID == "" {
			continue
		}
		if _, ok := seenTranslations[translationID]; ok {
			continue
		}
		name := animegoExtractDubbingName(content, dubbingID)
		if name == "" {
			name = "Voice " + translationID
		}
		seenTranslations[translationID] = struct{}{}
		out.Translations = append(out.Translations, animegoTranslation{
			ID:   translationID,
			Name: animegoCleanSpace(name),
		})
	}

	if len(out.Translations) == 0 {
		out.Translations = append(out.Translations, animegoTranslation{
			ID:   out.DefaultTranslation,
			Name: "Default",
		})
	}

	hasDefault := false
	for _, tr := range out.Translations {
		if tr.ID == out.DefaultTranslation {
			hasDefault = true
			break
		}
	}
	if !hasDefault {
		out.Translations = append([]animegoTranslation{{
			ID:   out.DefaultTranslation,
			Name: "Default",
		}}, out.Translations...)
	}
	return out, true
}

func (a *animegoChecker) fetchVideo(ctx context.Context, host, token, translation string, episode int) (string, bool) {
	baseHost := strings.TrimSpace(host)
	if baseHost == "" {
		return "", false
	}
	if !strings.HasPrefix(baseHost, "http://") && !strings.HasPrefix(baseHost, "https://") {
		baseHost = "https://" + baseHost
	}
	baseHost = strings.TrimRight(baseHost, "/")

	target := baseHost + "/embed/" + url.PathEscape(strings.TrimSpace(token)) +
		"?episode=" + strconv.Itoa(episode) + "&translation=" + url.QueryEscape(strings.TrimSpace(translation))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("DNT", "1")
	req.Header.Set("Pragma", "no-cache")
	req.Header.Set("Referer", a.host+"/")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("X-Lampac-Go", "1")

	resp, err := a.client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", false
	}
	raw := strings.ReplaceAll(string(body), "&quot;", `"`)
	raw = strings.ReplaceAll(raw, `\`, "")

	hls := strings.TrimSpace(submatch1(animegoPlayerHLSRe, raw))
	if hls == "" {
		return "", false
	}
	if strings.HasPrefix(hls, "//") {
		hls = "https:" + hls
	}
	if strings.HasPrefix(hls, "/") {
		hls = "https://" + strings.TrimSpace(host) + hls
	}
	return hls, true
}

func animegoImageURL(host, src string) string {
	src = strings.TrimSpace(src)
	if src == "" {
		return ""
	}
	if strings.Contains(src, "://") {
		return src
	}
	host = strings.TrimSpace(strings.TrimRight(host, "/"))
	if host == "" {
		return src
	}
	if strings.HasPrefix(src, "/") {
		return host + src
	}
	return host + "/" + src
}

// animegoExtractDubbingName finds the dubbing name for a given dubbing ID
// without compiling a new regexp. Looks for: data-dubbing="ID"><span ...>NAME
func animegoExtractDubbingName(content, dubbingID string) string {
	needle := `data-dubbing="` + dubbingID + `"`
	idx := strings.Index(content, needle)
	if idx < 0 {
		// case-insensitive fallback
		idx = strings.Index(strings.ToLower(content), strings.ToLower(needle))
	}
	if idx < 0 {
		return ""
	}
	// Skip past the needle to find <span ...>
	rest := content[idx+len(needle):]
	spanIdx := strings.Index(rest, "<span ")
	if spanIdx < 0 || spanIdx > 100 {
		return ""
	}
	rest = rest[spanIdx:]
	// Find closing > of the span tag
	closeIdx := strings.Index(rest, ">")
	if closeIdx < 0 {
		return ""
	}
	rest = rest[closeIdx+1:]
	// Extract text until < or newline
	end := strings.IndexAny(rest, "<\r\n")
	if end < 0 {
		end = len(rest)
	}
	return strings.TrimSpace(rest[:end])
}

func animegoCleanSpace(v string) string {
	v = animegoWhitespaceTrim.ReplaceAllString(v, " ")
	return strings.TrimSpace(v)
}

func animegoIntOrDefault(v string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return n
}
