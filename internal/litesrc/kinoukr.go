package litesrc

import (
	"encoding/base64"
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
)

var (
	kinoukrAnyIframeRe      = regexp.MustCompile(`src="(https?://[^"]+)"`)
	kinoukrFileJSONRe       = regexp.MustCompile(`file:\s*'([^\n\r]+)',`)
	kinoukrPlayerBlockRe    = regexp.MustCompile(`(?s)new Playerjs(.*)$`)
	kinoukrMovieHLSRe       = regexp.MustCompile(`file:\s*(?:"|')(https?://[^"']+/index\.m3u8)(?:"|')`)
	kinoukrMovieBase64Re    = regexp.MustCompile(`file:\s*(?:"|')([^"']+)(?:"|')`)
	kinoukrMovieSubtitleRe  = regexp.MustCompile(`"subtitle":\s*"([^"]+)"`)
	kinoukrEpisodeNumRe     = regexp.MustCompile(`^\s*([0-9]+)`)
	kinoukrSeasonEndNumRe   = regexp.MustCompile(`([0-9]+)$`)
	kinoukrSeasonStartNumRe = regexp.MustCompile(`^\s*([0-9]+)`)

	// xfsearch result parsing
	kinoukrXFSearchTitleRe = regexp.MustCompile(`class="short-title"\s+href="([^"]+)">([^<]+)</a>`)
	// movie page metadata
	kinoukrOriginalTitleRe = regexp.MustCompile(`class="foriginal">([^<]+)<`)
	kinoukrPageYearRe      = regexp.MustCompile(`/xfsearch/year/(\d{4})/`)
	kinoukrPageIframeRe    = regexp.MustCompile(`<iframe[^>]+src="(https?://(?:ashdi|tortuga)[^"]+)"`)
)

type kinoukrChecker struct {
	client *http.Client
	host   string
}

type kinoukrXFResult struct {
	Title   string
	PageURL string
}

type kinoukrPageInfo struct {
	Title      string
	OrigTitle  string
	Year       string
	PageURL    string
	IframeURLs []string // ashdi/tortuga iframe URLs, ashdi first
}

type kinoukrSimilar struct {
	Title string
	Year  string
	Href  string
}

type kinoukrEmbed struct {
	IsEmpty     bool
	SourceType  string
	Content     string
	Serial      []kinoukrTortugaVoice
	SerialAshdi []ashdiVoice
	Similars    []kinoukrSimilar
}

type kinoukrTortugaVoice struct {
	Title  string               `json:"title"`
	Season string               `json:"season"`
	Folder []kinoukrTortugaItem `json:"folder"`
}

type kinoukrTortugaItem struct {
	Title  string                 `json:"title"`
	Number string                 `json:"number"`
	Folder []kinoukrTortugaStream `json:"folder"`
}

type kinoukrTortugaStream struct {
	Title    string `json:"title"`
	File     string `json:"file"`
	Subtitle string `json:"subtitle"`
}

func NewKinoukrChecker(cfg config.Config) *kinoukrChecker {
	host := strings.TrimSpace(strings.TrimRight(cfg.Online.Kinoukr.Host, "/"))
	if host == "" {
		host = "https://kinoukr.tv"
	}
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}

	return &kinoukrChecker{
		client: httpclient.NewForBalancer("kinoukr", 12*time.Second),
		host:   host,
	}
}

func (k *kinoukrChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show := k.checkSearch(req)
			writeCheckSearchResponse(w, show, pluginQualityBadgeGet("kinoukr"))
			return
		}

		k.index(w, req, links)
	}
}

func (k *kinoukrChecker) index(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))

	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	href := strings.TrimSpace(q.Get("href"))
	source := strings.ToLower(strings.TrimSpace(q.Get("source")))
	sourceID := strings.TrimSpace(q.Get("id"))
	clarification, _ := getsTVQueryInt(q.Get("clarification"))
	year, _ := getsTVQueryInt(q.Get("year"))
	s, sSet := getsTVQueryInt(q.Get("s"))
	if !sSet {
		s = -1
	}
	t := strings.TrimSpace(q.Get("t"))

	if href == "" && source == "kinoukr" && sourceID != "" {
		link := strings.TrimRight(k.host, "/") + "/" + strings.TrimLeft(sourceID, "/")
		href = k.getIframeSource(req, link)
	}

	embed, ok := k.fetchEmbed(req, clarification, title, originalTitle, year, href)
	if !ok || embed.IsEmpty {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if embed.Content == "" && len(embed.Serial) == 0 && len(embed.SerialAshdi) == 0 {
		if href == "" && len(embed.Similars) > 0 {
			k.writeSimilar(w, req, rjson, embed.Similars, clarification, title, originalTitle, year)
			return
		}
		writeGetsTVEmpty(w, rjson)
		return
	}

	if embed.SourceType == "ashdi" {
		if len(embed.SerialAshdi) > 0 {
			k.writeAshdiSerial(w, req, rjson, embed.SerialAshdi, clarification, title, originalTitle, year, href, s, t, links)
			return
		}
		if embed.Content != "" {
			k.writeAshdiMovie(w, req, rjson, embed.Content, title, originalTitle, links)
			return
		}
		writeGetsTVEmpty(w, rjson)
		return
	}

	if embed.Content != "" {
		k.writeTortugaMovie(w, req, rjson, embed.Content, title, originalTitle, links)
		return
	}

	k.writeTortugaSerial(w, req, rjson, embed.Serial, clarification, title, originalTitle, year, href, s, t, links)
}

func (k *kinoukrChecker) checkSearch(req *http.Request) bool {
	q := req.URL.Query()
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))

	query := originalTitle
	if query == "" {
		query = title
	}
	if query == "" {
		return false
	}

	results := k.xfSearch(req, query)
	return len(results) > 0
}

// xfSearch searches kinoukr.tv via /xfsearch/ (GET, bypasses Cloudflare).
func (k *kinoukrChecker) xfSearch(req *http.Request, query string) []kinoukrXFResult {
	target := strings.TrimRight(k.host, "/") + "/xfsearch/" + url.PathEscape(query) + "/"
	body, ok := k.fetchText(req, target, "")
	if !ok {
		return nil
	}

	matches := kinoukrXFSearchTitleRe.FindAllStringSubmatch(body, 20)
	if len(matches) == 0 {
		return nil
	}

	results := make([]kinoukrXFResult, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		pageURL := strings.TrimSpace(m[1])
		if pageURL == "" {
			continue
		}
		if _, ok := seen[pageURL]; ok {
			continue
		}
		seen[pageURL] = struct{}{}
		results = append(results, kinoukrXFResult{
			Title:   strings.TrimSpace(m[2]),
			PageURL: pageURL,
		})
	}
	return results
}

// fetchPageInfo fetches a kinoukr movie/serial page and extracts metadata + iframe URLs.
func (k *kinoukrChecker) fetchPageInfo(req *http.Request, pageURL string) (kinoukrPageInfo, bool) {
	body, ok := k.fetchText(req, pageURL, "")
	if !ok {
		return kinoukrPageInfo{}, false
	}

	info := kinoukrPageInfo{PageURL: pageURL}
	info.OrigTitle = strings.TrimSpace(submatch1(kinoukrOriginalTitleRe, body))
	info.Year = submatch1(kinoukrPageYearRe, body)

	iframeMatches := kinoukrPageIframeRe.FindAllStringSubmatch(body, 10)
	ashdiURLs := make([]string, 0, 2)
	tortugaURLs := make([]string, 0, 2)
	for _, m := range iframeMatches {
		u := strings.TrimSpace(m[1])
		if strings.Contains(strings.ToLower(u), "ashdi") {
			ashdiURLs = append(ashdiURLs, u)
		} else {
			tortugaURLs = append(tortugaURLs, u)
		}
	}
	// Prefer ashdi over tortuga
	info.IframeURLs = append(ashdiURLs, tortugaURLs...)

	return info, len(info.IframeURLs) > 0
}

func (k *kinoukrChecker) fetchEmbed(
	req *http.Request,
	clarification int,
	title string,
	originalTitle string,
	year int,
	href string,
) (kinoukrEmbed, bool) {
	result := kinoukrEmbed{}
	iframeURI := strings.TrimSpace(href)

	if iframeURI == "" {
		query := originalTitle
		if clarification == 1 || query == "" {
			query = title
		}
		if query == "" {
			return kinoukrEmbed{}, false
		}

		xfResults := k.xfSearch(req, query)
		if len(xfResults) == 0 {
			return kinoukrEmbed{IsEmpty: true}, true
		}

		// Fetch page info for each result (limit to 5)
		limit := min(len(xfResults), 5)
		similars := make([]kinoukrSimilar, 0, limit)
		yearText := strconv.Itoa(year)

		for _, xf := range xfResults[:limit] {
			info, ok := k.fetchPageInfo(req, xf.PageURL)
			if !ok {
				continue
			}
			bestIframe := info.IframeURLs[0]
			titleValue := xf.Title
			if info.OrigTitle != "" {
				titleValue = xf.Title + " / " + info.OrigTitle
			}

			model := kinoukrSimilar{
				Href:  bestIframe,
				Title: titleValue,
				Year:  info.Year,
			}
			if year > 0 && model.Year == yearText {
				similars = append([]kinoukrSimilar{model}, similars...)
			} else {
				similars = append(similars, model)
			}
		}

		if len(similars) == 0 {
			return kinoukrEmbed{IsEmpty: true}, true
		}

		result.Similars = similars
		if len(similars) > 1 && (year <= 0 || similars[0].Year != yearText) {
			return result, true
		}
		iframeURI = similars[0].Href
	}

	iframeBody, ok := k.fetchText(req, iframeURI, "")
	if !ok {
		return kinoukrEmbed{}, false
	}
	if !strings.Contains(iframeBody, "file:") {
		return kinoukrEmbed{}, false
	}

	isAshdi := strings.Contains(strings.ToLower(iframeURI), "ashdi")
	if isAshdi {
		result.SourceType = "ashdi"

		raw := strings.TrimSpace(submatch1(kinoukrFileJSONRe, iframeBody))
		if strings.HasPrefix(raw, "[") {
			var serial []ashdiVoice
			if err := stdjson.Unmarshal([]byte(raw), &serial); err == nil && len(serial) > 0 {
				result.SerialAshdi = serial
			}
		}

		if len(result.SerialAshdi) == 0 {
			result.Content = strings.TrimSpace(submatch1(kinoukrPlayerBlockRe, iframeBody))
		}
	} else {
		result.SourceType = "tortuga"

		raw := strings.TrimSpace(submatch1(kinoukrFileJSONRe, iframeBody))
		if raw != "" {
			if decoded := kinoukrDecodeReversedBase64(raw); decoded != "" {
				raw = decoded
			}
			var serial []kinoukrTortugaVoice
			if err := stdjson.Unmarshal([]byte(raw), &serial); err == nil && len(serial) > 0 {
				result.Serial = serial
			}
		}

		if len(result.Serial) == 0 {
			result.Content = iframeBody
		}
	}

	if result.Content == "" && len(result.Serial) == 0 && len(result.SerialAshdi) == 0 {
		return kinoukrEmbed{}, false
	}
	return result, true
}

func (k *kinoukrChecker) getIframeSource(req *http.Request, link string) string {
	if strings.TrimSpace(link) == "" {
		return ""
	}
	news, ok := k.fetchText(req, link, "")
	if !ok {
		return ""
	}
	return strings.TrimSpace(submatch1(kinoukrAnyIframeRe, news))
}

func (k *kinoukrChecker) fetchText(req *http.Request, target, referer string) (string, bool) {
	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, target, nil)
	if err != nil {
		return "", false
	}
	httpReq.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	if referer != "" {
		httpReq.Header.Set("Referer", referer)
	}
	httpReq.Header.Set("X-Lampac-Go", "1")

	resp, err := k.client.Do(httpReq)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", false
	}
	return string(body), true
}

func (k *kinoukrChecker) writeSimilar(
	w http.ResponseWriter,
	req *http.Request,
	rjson bool,
	items []kinoukrSimilar,
	clarification int,
	title string,
	originalTitle string,
	year int,
) {
	if len(items) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(req)
	data := make([]map[string]any, 0, len(items))
	labels := make([]string, 0, len(items))

	for _, item := range items {
		link := fmt.Sprintf(
			"%s/lite/kinoukr?rjson=%s&clarification=%d&title=%s&original_title=%s&year=%d&href=%s",
			host,
			getsTVBool(rjson),
			clarification,
			url.QueryEscape(title),
			url.QueryEscape(originalTitle),
			year,
			url.QueryEscape(item.Href),
		)
		data = append(data, map[string]any{
			"method":  "link",
			"url":     link,
			"similar": true,
			"year":    item.Year,
			"details": "",
			"title":   item.Title,
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

func (k *kinoukrChecker) writeAshdiMovie(w http.ResponseWriter, req *http.Request, rjson bool, content, title, originalTitle string, links *proxylink.Manager) {
	hls := submatch1(ashdiMovieFileRe, content)
	if hls == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}
	hls = ashdiFixStream(hls)
	hls = streamProxyURL(req, hls, "kinoukr", links)

	row := map[string]any{
		"method": "play",
		"url":    hls,
		"stream": hls,
		"name":   "По умолчанию",
		"title":  getsTVJoinName(title, originalTitle),
	}
	if subtitles := ashdiParseSubtitleBlock(content); len(subtitles) > 0 {
		row["subtitles"] = subtitles
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "movie",
			"data": []map[string]any{row},
		})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	getsTVAppendMovieHTML(&sb, row, "По умолчанию", true, 0, 0)
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (k *kinoukrChecker) writeAshdiSerial(
	w http.ResponseWriter,
	req *http.Request,
	rjson bool,
	voices []ashdiVoice,
	clarification int,
	title string,
	originalTitle string,
	year int,
	href string,
	s int,
	tRaw string,
	links *proxylink.Manager,
) {
	if len(voices) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(req)
	encTitle := url.QueryEscape(title)
	encOriginal := url.QueryEscape(originalTitle)
	encHref := url.QueryEscape(href)
	baseQuery := fmt.Sprintf("rjson=%s&clarification=%d&title=%s&original_title=%s&year=%d&href=%s",
		getsTVBool(rjson), clarification, encTitle, encOriginal, year, encHref,
	)

	t, tSet := getsTVQueryInt(tRaw)
	if !tSet {
		t = -1
	}

	if s == -1 {
		type seasonItem struct {
			Num   int
			Title string
		}
		items := make([]seasonItem, 0, 8)
		seen := make(map[int]struct{}, 8)

		for _, voice := range voices {
			for _, season := range voice.Folder {
				num, ok := kinoukrAshdiSeasonNumber(season.Title)
				if !ok {
					continue
				}
				if _, ok := seen[num]; ok {
					continue
				}
				seen[num] = struct{}{}
				items = append(items, seasonItem{Num: num, Title: strings.TrimSpace(season.Title)})
			}
		}

		sort.Slice(items, func(i, j int) bool { return items[i].Num < items[j].Num })
		if len(items) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}

		rows := make([]map[string]any, 0, len(items))
		labels := make([]string, 0, len(items))
		for _, item := range items {
			link := fmt.Sprintf("%s/lite/kinoukr?%s&s=%d", host, baseQuery, item.Num)
			rows = append(rows, map[string]any{
				"method": "link",
				"id":     item.Num,
				"url":    link,
				"name":   item.Title,
			})
			labels = append(labels, item.Title)
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
		return
	}

	voiceRows := make([]map[string]any, 0, len(voices))
	validIndexes := make([]int, 0, len(voices))
	for i, voice := range voices {
		if kinoukrFindAshdiSeason(voice.Folder, s) == nil {
			continue
		}
		if t == -1 {
			t = i
		}
		link := fmt.Sprintf("%s/lite/kinoukr?%s&s=%d&t=%d", host, baseQuery, s, i)
		voiceRows = append(voiceRows, map[string]any{
			"method": "link",
			"name":   strings.TrimSpace(voice.Title),
			"active": t == i,
			"url":    link,
		})
		validIndexes = append(validIndexes, i)
	}
	if len(validIndexes) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	if t < 0 || t >= len(voices) || kinoukrFindAshdiSeason(voices[t].Folder, s) == nil {
		t = validIndexes[0]
		voiceRows[0]["active"] = true
	}

	season := kinoukrFindAshdiSeason(voices[t].Folder, s)
	if season == nil || len(season.Folder) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	type episodeRow struct {
		Data  map[string]any
		Label string
		Num   int
	}
	rows := make([]episodeRow, 0, len(season.Folder))
	baseTitle := getsTVJoinName(title, originalTitle)

	for _, ep := range season.Folder {
		label := strings.TrimSpace(ep.Title)
		if label == "" {
			continue
		}
		file := strings.TrimSpace(ep.File)
		if file == "" {
			continue
		}
		file = ashdiFixStream(file)
		file = streamProxyURL(req, file, "kinoukr", links)
		num, _ := kinoukrEpisodeNumber(label)
		row := map[string]any{
			"method": "play",
			"url":    file,
			"stream": file,
			"s":      s,
			"e":      num,
			"name":   label,
			"title":  fmt.Sprintf("%s (%s)", baseTitle, label),
		}
		if subs := ashdiParseSubtitleList(ep.Subtitle); len(subs) > 0 {
			row["subtitles"] = subs
		}
		rows = append(rows, episodeRow{Data: row, Label: label, Num: num})
	}

	if len(rows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Num == rows[j].Num {
			return rows[i].Label < rows[j].Label
		}
		return rows[i].Num < rows[j].Num
	})

	episodeData := make([]map[string]any, 0, len(rows))
	labels := make([]string, 0, len(rows))
	seasons := make([]int, 0, len(rows))
	episodesNum := make([]int, 0, len(rows))
	for _, row := range rows {
		episodeData = append(episodeData, row.Data)
		labels = append(labels, row.Label)
		seasons = append(seasons, s)
		episodesNum = append(episodesNum, row.Num)
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "episode", "data": episodeData, "voice": voiceRows})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for _, row := range voiceRows {
		getsTVAppendVoiceHTML(&sb, row)
	}
	sb.WriteString(`</div><div class="videos__line">`)
	for i, row := range episodeData {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, seasons[i], episodesNum[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (k *kinoukrChecker) writeTortugaMovie(w http.ResponseWriter, req *http.Request, rjson bool, content, title, originalTitle string, links *proxylink.Manager) {
	hls := strings.TrimSpace(submatch1(kinoukrMovieHLSRe, content))
	if hls == "" {
		base64Value := strings.TrimSpace(submatch1(kinoukrMovieBase64Re, content))
		hls = kinoukrDecodeReversedBase64(base64Value)
	}
	if hls == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}
	hls = streamProxyURL(req, hls, "kinoukr", links)

	row := map[string]any{
		"method": "play",
		"url":    hls,
		"stream": hls,
		"name":   "По умолчанию",
		"title":  getsTVJoinName(title, originalTitle),
	}
	if subs := ashdiParseSubtitleList(submatch1(kinoukrMovieSubtitleRe, content)); len(subs) > 0 {
		row["subtitles"] = subs
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "movie", "data": []map[string]any{row}})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	getsTVAppendMovieHTML(&sb, row, "По умолчанию", true, 0, 0)
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (k *kinoukrChecker) writeTortugaSerial(
	w http.ResponseWriter,
	req *http.Request,
	rjson bool,
	serial []kinoukrTortugaVoice,
	clarification int,
	title string,
	originalTitle string,
	year int,
	href string,
	s int,
	t string,
	links *proxylink.Manager,
) {
	if len(serial) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(req)
	encTitle := url.QueryEscape(title)
	encOriginal := url.QueryEscape(originalTitle)
	encHref := url.QueryEscape(href)
	baseQuery := fmt.Sprintf("rjson=%s&clarification=%d&title=%s&original_title=%s&year=%d&href=%s",
		getsTVBool(rjson), clarification, encTitle, encOriginal, year, encHref,
	)

	if s == -1 {
		type seasonItem struct {
			Num   int
			Title string
		}
		items := make([]seasonItem, 0, len(serial))
		for _, season := range serial {
			num, err := strconv.Atoi(strings.TrimSpace(season.Season))
			if err != nil || num < 1 {
				continue
			}
			items = append(items, seasonItem{Num: num, Title: strings.TrimSpace(season.Title)})
		}
		sort.Slice(items, func(i, j int) bool { return items[i].Num < items[j].Num })
		if len(items) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}

		rows := make([]map[string]any, 0, len(items))
		labels := make([]string, 0, len(items))
		for _, item := range items {
			label := item.Title
			if label == "" {
				label = fmt.Sprintf("%d сезон", item.Num)
			}
			link := fmt.Sprintf("%s/lite/kinoukr?%s&s=%d", host, baseQuery, item.Num)
			rows = append(rows, map[string]any{
				"method": "link",
				"id":     item.Num,
				"url":    link,
				"name":   label,
			})
			labels = append(labels, label)
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
		return
	}

	sSeason := strconv.Itoa(s)
	var episodes []kinoukrTortugaItem
	for _, season := range serial {
		if strings.TrimSpace(season.Season) == sSeason {
			episodes = season.Folder
			break
		}
	}
	if len(episodes) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	voiceRows := make([]map[string]any, 0, 8)
	seenVoice := make(map[string]struct{}, 8)
	for _, episode := range episodes {
		for _, voice := range episode.Folder {
			name := strings.TrimSpace(voice.Title)
			if name == "" {
				continue
			}
			if _, ok := seenVoice[name]; ok {
				continue
			}
			seenVoice[name] = struct{}{}
			if t == "" {
				t = name
			}
			link := fmt.Sprintf("%s/lite/kinoukr?%s&s=%d&t=%s", host, baseQuery, s, url.QueryEscape(name))
			voiceRows = append(voiceRows, map[string]any{
				"method": "link",
				"name":   name,
				"active": t == name,
				"url":    link,
			})
		}
	}
	if len(voiceRows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	type episodeRow struct {
		Data  map[string]any
		Label string
		Num   int
	}
	rows := make([]episodeRow, 0, len(episodes))
	baseTitle := getsTVJoinName(title, originalTitle)

	for _, ep := range episodes {
		label := strings.TrimSpace(ep.Title)
		if label == "" {
			continue
		}

		var stream kinoukrTortugaStream
		found := false
		for _, voice := range ep.Folder {
			if strings.TrimSpace(voice.Title) == t {
				stream = voice
				found = true
				break
			}
		}
		if !found || strings.TrimSpace(stream.File) == "" {
			continue
		}

		num, ok := kinoukrEpisodeNumber(label)
		if !ok {
			n, err := strconv.Atoi(strings.TrimSpace(ep.Number))
			if err != nil || n < 1 {
				n = len(rows) + 1
			}
			num = n
		}
		file := streamProxyURL(req, strings.TrimSpace(stream.File), "kinoukr", links)
		row := map[string]any{
			"method": "play",
			"url":    file,
			"stream": file,
			"s":      s,
			"e":      num,
			"name":   label,
			"title":  fmt.Sprintf("%s (%s)", baseTitle, label),
		}
		if subs := ashdiParseSubtitleList(stream.Subtitle); len(subs) > 0 {
			row["subtitles"] = subs
		}
		rows = append(rows, episodeRow{Data: row, Label: label, Num: num})
	}
	if len(rows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Num == rows[j].Num {
			return rows[i].Label < rows[j].Label
		}
		return rows[i].Num < rows[j].Num
	})

	episodeData := make([]map[string]any, 0, len(rows))
	labels := make([]string, 0, len(rows))
	seasons := make([]int, 0, len(rows))
	episodesNum := make([]int, 0, len(rows))
	for _, row := range rows {
		episodeData = append(episodeData, row.Data)
		labels = append(labels, row.Label)
		seasons = append(seasons, s)
		episodesNum = append(episodesNum, row.Num)
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "episode", "data": episodeData, "voice": voiceRows})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for _, row := range voiceRows {
		getsTVAppendVoiceHTML(&sb, row)
	}
	sb.WriteString(`</div><div class="videos__line">`)
	for i, row := range episodeData {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, seasons[i], episodesNum[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func kinoukrFindAshdiSeason(nodes []ashdiSeason, season int) *ashdiSeason {
	for i := range nodes {
		num, ok := kinoukrAshdiSeasonNumber(nodes[i].Title)
		if ok && num == season {
			return &nodes[i]
		}
	}
	return nil
}

func kinoukrAshdiSeasonNumber(title string) (int, bool) {
	m := kinoukrSeasonEndNumRe.FindStringSubmatch(strings.TrimSpace(title))
	if len(m) != 2 {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

func kinoukrEpisodeNumber(title string) (int, bool) {
	m := kinoukrEpisodeNumRe.FindStringSubmatch(strings.TrimSpace(title))
	if len(m) != 2 {
		m = kinoukrSeasonStartNumRe.FindStringSubmatch(strings.TrimSpace(title))
		if len(m) != 2 {
			return 0, false
		}
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

func kinoukrReverseString(v string) string {
	if v == "" {
		return ""
	}
	runes := []rune(v)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}

func kinoukrDecodeReversedBase64(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	tryDecode := func(v string) string {
		if v == "" {
			return ""
		}
		if decoded, err := base64.StdEncoding.DecodeString(v); err == nil {
			return kinoukrReverseString(string(decoded))
		}
		if decoded, err := base64.RawStdEncoding.DecodeString(v); err == nil {
			return kinoukrReverseString(string(decoded))
		}
		if rem := len(v) % 4; rem != 0 {
			v = v + strings.Repeat("=", 4-rem)
		}
		if decoded, err := base64.StdEncoding.DecodeString(v); err == nil {
			return kinoukrReverseString(string(decoded))
		}
		return ""
	}

	if out := tryDecode(value); out != "" {
		return out
	}
	if out := tryDecode(strings.TrimSuffix(strings.TrimSuffix(value, "=="), "=")); out != "" {
		return out
	}
	return ""
}
