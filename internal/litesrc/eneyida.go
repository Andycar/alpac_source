package litesrc

import (
	stdjson "encoding/json"
	"fmt"
	"html"
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
	eneyidaIframeURIRe      = regexp.MustCompile(`<iframe width="100%" height="400" src="(https?://[^/]+/[^"]+/[0-9]+)"`)
	eneyidaQualityRe        = regexp.MustCompile(` (1080p|720p|480p)</div>`)
	eneyidaFileJSONRe       = regexp.MustCompile(`file:\s*'([^\n\r]+)',`)
	eneyidaMovieHLSRe       = regexp.MustCompile(`file:\s*"((?:https?)://[^"]+/index\.m3u8)"`)
	eneyidaMovieSubtitleRe  = regexp.MustCompile(`subtitle:\s*"([^"]+)"`)
	eneyidaNewsLinkRe       = regexp.MustCompile(`href="(https?://[^/]+/[^"]+\.html)"`)
	eneyidaShortSubtitleRe  = regexp.MustCompile(`class="short_subtitle">(?:<a [^>]+>([0-9]{4})</a>)?([^<]+)</div>`)
	eneyidaUATitleRe        = regexp.MustCompile(`id="short_title"[^>]+>([^<]+)<`)
	eneyidaImgRe            = regexp.MustCompile(`data-src="/([^"]+)"`)
	eneyidaSeasonStartNumRe = regexp.MustCompile(`^([0-9]+)`)
	eneyidaEpisodeStartNum  = regexp.MustCompile(`^([0-9]+)`)
)

type eneyidaChecker struct {
	client *http.Client
	host   string
}

type eneyidaEmbed struct {
	IsEmpty  bool
	Content  string
	Quality  string
	Serial   []eneyidaNode
	Similars []eneyidaSimilar
}

type eneyidaNode struct {
	Title    string        `json:"title"`
	File     string        `json:"file"`
	Subtitle string        `json:"subtitle"`
	Folder   []eneyidaNode `json:"folder"`
}

type eneyidaSimilar struct {
	Title string
	Year  string
	Href  string
	Img   string
}

func NewEneyidaChecker(cfg config.Config) *eneyidaChecker {
	host := strings.TrimSpace(strings.TrimRight(cfg.Online.Eneyida.Host, "/"))
	if host == "" {
		host = "https://eneyida.tv"
	}
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}
	return &eneyidaChecker{
		client: httpclient.NewForBalancer("eneyida", 12*time.Second),
		host:   host,
	}
}

func (e *eneyidaChecker) Handle(cfg config.Config) http.HandlerFunc {
	return e.HandleWithLinks(cfg, nil)
}

func (e *eneyidaChecker) HandleWithLinks(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show := e.checkSearch(req)
			writeCheckSearchResponse(w, show, pluginQualityBadgeGet("eneyida"))
			return
		}

		e.index(w, req, links)
	}
}

// checkSearch reports whether eneyida actually has THIS title.
//
// It used to just ping the host — so the source lit up white for every card as
// long as the site was alive, and the user opened it to an empty list. Now it
// runs the same search the index does and only claims availability when a
// candidate matches; the host probe stays as the fallback for cards with no
// title (there is nothing to search for, and hiding a working source is worse
// than showing it).
func (e *eneyidaChecker) checkSearch(req *http.Request) bool {
	if e.host == "" {
		return false
	}
	q := req.URL.Query()
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	year, _ := getsTVQueryInt(q.Get("year"))

	queries := make([]string, 0, 2)
	if title != "" {
		queries = append(queries, title)
	}
	if originalTitle != "" && normalizeSearchTitle(originalTitle) != normalizeSearchTitle(title) {
		queries = append(queries, originalTitle)
	}
	if len(queries) == 0 {
		return e.hostAlive(req)
	}

	for _, query := range queries {
		searchHTML, ok := e.search(req, query)
		if !ok {
			continue
		}
		// ONLY an exact match counts. eneyida's DLE search is full-text, so a
		// miss still returns rows — searching "Веном" yields «Г'єрсон Агати
		// Крісті» and «Серце дракона» because the words appear in descriptions.
		// Treating those as availability is exactly what lit the source white
		// for titles it does not carry.
		if link, _ := e.parseSearch(searchHTML, query, year); strings.TrimSpace(link) != "" {
			return true
		}
	}
	return false
}

func (e *eneyidaChecker) hostAlive(req *http.Request) bool {
	return e.probe(req, http.MethodHead) || e.probe(req, http.MethodGet)
}

func (e *eneyidaChecker) probe(req *http.Request, method string) bool {
	httpReq, err := http.NewRequestWithContext(req.Context(), method, e.host, nil)
	if err != nil {
		return false
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	httpReq.Header.Set("Accept", "text/html,application/json,*/*")
	httpReq.Header.Set("X-Lampac-Go", "1")

	resp, err := e.client.Do(httpReq)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if method == http.MethodHead && resp.StatusCode == http.StatusMethodNotAllowed {
		return false
	}
	return resp.StatusCode >= 200 && resp.StatusCode < 400
}

func (e *eneyidaChecker) index(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	similar := parseBoolParam(q.Get("similar"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	href := strings.TrimSpace(q.Get("href"))
	source := strings.ToLower(strings.TrimSpace(q.Get("source")))
	sourceID := strings.TrimSpace(q.Get("id"))
	clarification, _ := getsTVQueryInt(q.Get("clarification"))
	year, _ := getsTVQueryInt(q.Get("year"))
	t, tSet := getsTVQueryInt(q.Get("t"))
	s, sSet := getsTVQueryInt(q.Get("s"))
	if !tSet {
		t = -1
	}
	if !sSet {
		s = -1
	}

	if href == "" && source == "eneyida" && sourceID != "" {
		href = strings.TrimRight(e.host, "/") + "/" + strings.TrimLeft(sourceID, "/")
	}

	if href == "" && (originalTitle == "" || year == 0) {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// eneyida titles its entries in Ukrainian ("Веном"), so searching by
	// original_title alone ("Venom") finds the page but never matches the name,
	// and the source ends up empty even though it has the film. Try the
	// preferred spelling first, then the other one.
	searchTitle := originalTitle
	if similar || clarification == 1 {
		searchTitle = title
	}
	alternate := title
	if searchTitle == title {
		alternate = originalTitle
	}

	embed, ok := e.fetchEmbed(req, searchTitle, year, href, similar)
	if (!ok || embed.IsEmpty) && href == "" && strings.TrimSpace(alternate) != "" &&
		normalizeSearchTitle(alternate) != normalizeSearchTitle(searchTitle) {
		if alt, altOK := e.fetchEmbed(req, alternate, year, href, similar); altOK && !alt.IsEmpty {
			embed, ok = alt, altOK
		}
	}
	if !ok || embed.IsEmpty {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if embed.Content == "" && len(embed.Serial) == 0 {
		if href == "" && len(embed.Similars) > 0 {
			e.writeSimilar(w, req, rjson, embed.Similars, clarification, title, originalTitle, year)
			return
		}
		writeGetsTVEmpty(w, rjson)
		return
	}

	if embed.Content != "" {
		e.writeMovie(w, req, rjson, embed.Content, embed.Quality, title, originalTitle, links)
		return
	}

	// A JSON `file:'[…]'` playlist whose nodes all carry File (and no Folder) is a
	// multi-voice MOVIE, not a serial — render it as a movie with voice selection
	// (mirrors ashdi). Left to writeSerial it produces an empty result.
	if eneyidaIsMovieVoices(embed.Serial) {
		e.writeMovieVoices(w, req, rjson, embed.Serial, title, originalTitle, links)
		return
	}

	e.writeSerial(w, req, rjson, embed.Serial, clarification, title, originalTitle, year, href, s, t, links)
}

// eneyidaIsMovieVoices reports whether the parsed playlist is a multi-voice
// movie: every node is a playable leaf (File set, no Folder). Serials have
// Folder-bearing season/voice nodes, so this stays false for them.
func eneyidaIsMovieVoices(nodes []eneyidaNode) bool {
	if len(nodes) == 0 {
		return false
	}
	for _, n := range nodes {
		if strings.TrimSpace(n.File) == "" || len(n.Folder) > 0 {
			return false
		}
	}
	return true
}

// writeMovieVoices renders a multi-voice movie: one play row per voice.
func (e *eneyidaChecker) writeMovieVoices(w http.ResponseWriter, req *http.Request, rjson bool, nodes []eneyidaNode, title, originalTitle string, links *proxylink.Manager) {
	baseTitle := getsTVJoinName(title, originalTitle)
	data := make([]map[string]any, 0, len(nodes))
	labels := make([]string, 0, len(nodes))

	for _, node := range nodes {
		file := strings.TrimSpace(node.File)
		if file == "" {
			continue
		}
		file = streamProxyURL(req, file, "eneyida", links)
		name := strings.TrimSpace(node.Title)
		if name == "" {
			name = "Оригінал"
		}
		row := map[string]any{
			"method": "play",
			"url":    file,
			"stream": file,
			"name":   name,
			"title":  baseTitle + " (" + name + ")",
		}
		if subs := ashdiParseSubtitleList(node.Subtitle); len(subs) > 0 {
			row["subtitles"] = subs
		}
		data = append(data, row)
		labels = append(labels, name)
	}

	if len(data) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "movie", "data": data})
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

func (e *eneyidaChecker) fetchEmbed(req *http.Request, searchTitle string, year int, href string, similar bool) (eneyidaEmbed, bool) {
	result := eneyidaEmbed{}
	link := strings.TrimSpace(href)

	if link == "" {
		if searchTitle == "" || year == 0 {
			return eneyidaEmbed{}, false
		}

		searchHTML, searchOK := e.search(req, searchTitle)
		if !searchOK {
			return eneyidaEmbed{}, false
		}

		searchEmpty := strings.Contains(strings.ToLower(searchHTML), strings.ToLower(">Пошук по сайту<"))
		link, result.Similars = e.parseSearch(searchHTML, searchTitle, year)

		if similar {
			return result, true
		}
		if link == "" {
			if len(result.Similars) > 0 {
				return result, true
			}
			if searchEmpty {
				return eneyidaEmbed{IsEmpty: true}, true
			}
			return eneyidaEmbed{}, false
		}
	}

	page, ok := e.fetchText(req, link, "")
	if !ok {
		return eneyidaEmbed{}, false
	}

	if strings.Contains(page, "full_content fx_row") {
		result.Quality = submatch1(eneyidaQualityRe, page)
	}

	iframeURI := submatch1(eneyidaIframeURIRe, page)
	if iframeURI == "" {
		return eneyidaEmbed{}, false
	}

	iframeHTML, ok := e.fetchText(req, iframeURI, link)
	if !ok {
		return eneyidaEmbed{}, false
	}
	if !strings.Contains(iframeHTML, "file:") {
		return eneyidaEmbed{}, false
	}

	if strings.Contains(iframeHTML, "file: '") || strings.Contains(iframeHTML, "file:'") {
		raw := submatch1(eneyidaFileJSONRe, iframeHTML)
		if raw != "" {
			var serial []eneyidaNode
			if err := stdjson.Unmarshal([]byte(raw), &serial); err == nil && len(serial) > 0 {
				result.Serial = serial
			}
		}
	}

	if len(result.Serial) == 0 {
		result.Content = iframeHTML
	}

	if result.Content == "" && len(result.Serial) == 0 {
		return eneyidaEmbed{}, false
	}
	return result, true
}

func (e *eneyidaChecker) search(req *http.Request, title string) (string, bool) {
	form := url.Values{}
	form.Set("do", "search")
	form.Set("subaction", "search")
	form.Set("search_start", "0")
	form.Set("result_from", "1")
	form.Set("story", title)

	target := strings.TrimRight(e.host, "/") + "/index.php?do=search"
	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return "", false
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	httpReq.Header.Set("X-Lampac-Go", "1")

	resp, err := e.client.Do(httpReq)
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

func (e *eneyidaChecker) parseSearch(htmlBody, queryTitle string, year int) (string, []eneyidaSimilar) {
	rows := strings.Split(htmlBody, "<article ")
	similars := make([]eneyidaSimilar, 0, 8)
	want := normalizeSearchTitle(strings.ToLower(strings.TrimSpace(queryTitle)))
	yearStr := strconv.Itoa(year)

	exactHref := ""            // exact name + year match (most precise)
	relatedHrefs := []string{} // year match + subtitle-variant name (e.g. "Dune" ⇄ "Dune: Part One")

	for i := 1; i < len(rows); i++ {
		row := "<article " + rows[i]
		if strings.Contains(row, ">Анонс</div>") || strings.Contains(row, ">Трейлер</div>") {
			continue
		}

		newslink := strings.TrimSpace(submatch1(eneyidaNewsLinkRe, row))
		if newslink == "" {
			continue
		}

		sub := eneyidaShortSubtitleRe.FindStringSubmatch(row)
		if len(sub) < 3 {
			continue
		}
		blockYear := strings.TrimSpace(sub[1])
		name := strings.TrimSpace(strings.ReplaceAll(sub[2], "&bull;", ""))
		if name == "" {
			continue
		}

		uaName := strings.TrimSpace(submatch1(eneyidaUATitleRe, row))
		img := strings.TrimSpace(submatch1(eneyidaImgRe, row))
		if img != "" {
			img = strings.TrimRight(e.host, "/") + "/" + strings.TrimLeft(img, "/")
		}

		simTitle := strings.TrimSpace(strings.Trim(uaName+" / "+name, " /"))
		if simTitle == "" {
			simTitle = name
		}
		similars = append(similars, eneyidaSimilar{
			Title: simTitle,
			Year:  blockYear,
			Href:  newslink,
			Img:   img,
		})

		// Selection candidates — restricted to the requested year (a movie and a
		// same-named series almost never share the exact year, so the year is the
		// strong discriminator). eneyida often appends a subtitle TMDB's
		// original_title lacks ("Dune" → "Dune: Part One"), so an exact name match
		// alone misses the film and the picker then surfaces same-named SERIES.
		if blockYear != yearStr {
			continue
		}
		normName := normalizeSearchTitle(name)
		switch {
		case normName == want:
			if exactHref == "" {
				exactHref = newslink
			}
		case eneyidaTitleRelated(normName, want):
			relatedHrefs = append(relatedHrefs, newslink)
		}
	}

	selected := exactHref
	// Fall back to a subtitle-variant ONLY when it's unambiguous — several
	// year-matched variants (e.g. two different "Dune: …" titles) stay a picker
	// so we never confidently return the wrong one.
	if selected == "" && len(relatedHrefs) == 1 {
		selected = relatedHrefs[0]
	}

	return selected, similars
}

// eneyidaTitleRelated reports whether two normalized titles are the same film
// under a subtitle difference — one is the other plus a trailing subtitle
// ("dune" ⇄ "dune part one"). It deliberately does NOT match mid-string
// (so "children of dune" is NOT related to "dune").
func eneyidaTitleRelated(name, want string) bool {
	if name == "" || want == "" {
		return false
	}
	if name == want {
		return true
	}
	return strings.HasPrefix(name, want+" ") || strings.HasPrefix(want, name+" ")
}

func (e *eneyidaChecker) fetchText(req *http.Request, target, referer string) (string, bool) {
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

	resp, err := e.client.Do(httpReq)
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

func (e *eneyidaChecker) writeMovie(w http.ResponseWriter, req *http.Request, rjson bool, content, quality, title, originalTitle string, links *proxylink.Manager) {
	hls := submatch1(eneyidaMovieHLSRe, content)
	if hls == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}
	hls = streamProxyURL(req, hls, "eneyida", links)

	label := strings.TrimSpace(quality)
	if label == "" {
		label = "По умолчанию"
	}

	row := map[string]any{
		"method": "play",
		"url":    hls,
		"stream": hls,
		"name":   label,
		"title":  getsTVJoinName(title, originalTitle),
	}
	if subs := ashdiParseSubtitleList(submatch1(eneyidaMovieSubtitleRe, content)); len(subs) > 0 {
		row["subtitles"] = subs
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
	getsTVAppendMovieHTML(&sb, row, label, true, 0, 0)
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (e *eneyidaChecker) writeSerial(
	w http.ResponseWriter,
	req *http.Request,
	rjson bool,
	serial []eneyidaNode,
	clarification int,
	title string,
	originalTitle string,
	year int,
	href string,
	s int,
	t int,
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
		getsTVBool(rjson), clarification, encTitle, encOriginal, year, encHref)

	voiceFirst := eneyidaIsVoiceFirst(serial)
	if s == -1 {
		type seasonItem struct {
			Num   int
			Title string
		}

		items := make([]seasonItem, 0, 8)
		seen := make(map[int]struct{}, 8)

		if voiceFirst {
			for _, voice := range serial {
				for _, season := range voice.Folder {
					num, ok := eneyidaSeasonNumber(season.Title)
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
		} else {
			for _, season := range serial {
				num, ok := eneyidaSeasonNumber(season.Title)
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
			link := fmt.Sprintf("%s/lite/eneyida?%s&s=%d", host, baseQuery, item.Num)
			rows = append(rows, map[string]any{
				"method": "link",
				"id":     item.Num,
				"url":    link,
				"name":   item.Title,
			})
			labels = append(labels, item.Title)
		}

		if rjson {
			writeJSON(w, http.StatusOK, map[string]any{
				"type": "season",
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
		return
	}

	var voiceRows []map[string]any
	var episodes []eneyidaNode

	if voiceFirst {
		voiceRows = make([]map[string]any, 0, len(serial))
		validIndexes := make([]int, 0, len(serial))
		for i, voice := range serial {
			if eneyidaFindSeasonNode(voice.Folder, s) == nil {
				continue
			}
			if t == -1 {
				t = i
			}
			link := fmt.Sprintf("%s/lite/eneyida?%s&s=%d&t=%d", host, baseQuery, s, i)
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
		if t < 0 || t >= len(serial) || eneyidaFindSeasonNode(serial[t].Folder, s) == nil {
			t = validIndexes[0]
			voiceRows[0]["active"] = true
		}
		season := eneyidaFindSeasonNode(serial[t].Folder, s)
		if season == nil {
			writeGetsTVEmpty(w, rjson)
			return
		}
		episodes = season.Folder
	} else {
		season := eneyidaFindSeasonNode(serial, s)
		if season == nil || len(season.Folder) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}
		voiceRows = make([]map[string]any, 0, len(season.Folder))
		if t == -1 {
			t = 0
		}
		for i := range season.Folder {
			link := fmt.Sprintf("%s/lite/eneyida?%s&s=%d&t=%d", host, baseQuery, s, i)
			voiceRows = append(voiceRows, map[string]any{
				"method": "link",
				"name":   strings.TrimSpace(season.Folder[i].Title),
				"active": t == i,
				"url":    link,
			})
		}
		if t < 0 || t >= len(season.Folder) {
			t = 0
			voiceRows[0]["active"] = true
		}
		episodes = season.Folder[t].Folder
	}

	if len(episodes) == 0 {
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
		file := strings.TrimSpace(ep.File)
		if file == "" {
			continue
		}
		file = streamProxyURL(req, file, "eneyida", links)
		num, _ := eneyidaEpisodeNumber(label)
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
		writeJSON(w, http.StatusOK, map[string]any{
			"type":  "episode",
			"data":  episodeData,
			"voice": voiceRows,
		})
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

func (e *eneyidaChecker) writeSimilar(
	w http.ResponseWriter,
	req *http.Request,
	rjson bool,
	items []eneyidaSimilar,
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
		link := fmt.Sprintf("%s/lite/eneyida?clarification=%d&title=%s&original_title=%s&year=%d&href=%s&rjson=%s",
			host,
			clarification,
			url.QueryEscape(title),
			url.QueryEscape(originalTitle),
			year,
			url.QueryEscape(item.Href),
			getsTVBool(rjson),
		)
		data = append(data, map[string]any{
			"method":  "link",
			"url":     link,
			"similar": true,
			"year":    item.Year,
			"details": "",
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

func eneyidaIsVoiceFirst(serial []eneyidaNode) bool {
	if len(serial) == 0 {
		return true
	}
	first := serial[0]
	if len(first.Folder) == 0 {
		return true
	}
	if _, ok := eneyidaSeasonNumber(first.Title); ok {
		return false
	}
	if _, ok := eneyidaSeasonNumber(first.Folder[0].Title); ok {
		return true
	}
	return true
}

func eneyidaSeasonNumber(title string) (int, bool) {
	m := eneyidaSeasonStartNumRe.FindStringSubmatch(strings.TrimSpace(title))
	if len(m) != 2 {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

func eneyidaEpisodeNumber(title string) (int, bool) {
	m := eneyidaEpisodeStartNum.FindStringSubmatch(strings.TrimSpace(title))
	if len(m) != 2 {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

func eneyidaFindSeasonNode(nodes []eneyidaNode, season int) *eneyidaNode {
	for i := range nodes {
		n, ok := eneyidaSeasonNumber(nodes[i].Title)
		if ok && n == season {
			return &nodes[i]
		}
	}
	return nil
}

func eneyidaSafeText(v string) string {
	return html.EscapeString(strings.TrimSpace(v))
}
