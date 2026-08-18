package litesrc

import (
	"context"
	"encoding/base64"
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
	"time"
	"unicode/utf8"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/proxylink"

	"github.com/rs/zerolog/log"
)

var (
	// PlayerJS extraction patterns.
	moonPlayerJSRe   = regexp.MustCompile(`(?is)new\s+Playerjs\s*\(`)
	moonFileRe       = regexp.MustCompile(`(?is)\bfile\s*:\s*`)
	moonAtobRe       = regexp.MustCompile(`(?i)atob\(\s*['"]([A-Za-z0-9+/=]+)['"]\s*\)`)
	moonHelperCallRe = regexp.MustCompile(`(?i)^([A-Za-z_$][\w$]*)\(\s*['"](.+?)['"]\s*\)$`)
	moonHelperKeyRe  = func(name string) *regexp.Regexp {
		return regexp.MustCompile(`(?is)function\s+` + regexp.QuoteMeta(name) + `\s*\([^)]*\)\s*\{[\s\S]*?var\s+k\s*=\s*['"]([^'"]+)['"]`)
	}
	moonStreamQualRe = regexp.MustCompile(`\[([^\]]+)\](https?://[^,\[\s]+)`)
	moonEpisodeNumRe = regexp.MustCompile(`(?i)(?:episode|серія|серия|епізод|ep)\s*(\d+)`)
	moonSeasonNumRe  = regexp.MustCompile(`(?i)(?:season|сезон)\s*(\d+)|(\d+)\s*(?:season|сезон)`)
)

// --- Types ---

type moonAnimeChecker struct {
	client  *http.Client
	host    string // main site, e.g. https://moonanime.art
	apiHost string // API host, e.g. https://apx.lme.isroot.in
	token   string // optional API key
}

type moonSearchResponse struct {
	Seasons []moonSeasonRef `json:"seasons"`
}

type moonSeasonRef struct {
	SeasonNumber int    `json:"season_number"`
	URL          string `json:"url"`
}

// moonSeasonContent is the parsed result of a season page.
type moonSeasonContent struct {
	IsSeries bool
	Voices   []moonVoiceContent
}

type moonVoiceContent struct {
	Name     string
	Episodes []moonEpisodeContent // for series
	File     string               // for movie (raw stream URL / PlayerJS file value)
}

type moonEpisodeContent struct {
	Name   string
	Number int
	File   string // raw stream URL or [quality]url format
}

// --- Constructor ---

func NewMoonAnimeChecker(cfg config.Config) *moonAnimeChecker {
	host := strings.TrimRight(strings.TrimSpace(cfg.Online.MoonAnime.Host), "/")
	if host == "" {
		host = "https://moonanime.art"
	}
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}

	// New NMoonAnime uses a separate API host.
	apiHost := "https://apx.lme.isroot.in"

	return &moonAnimeChecker{
		client:  httpclient.NewForBalancer("moonanime", 15*time.Second),
		host:    host,
		apiHost: apiHost,
		token:   strings.TrimSpace(cfg.Online.MoonAnime.Token),
	}
}

// --- Router ---

func (m *moonAnimeChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := strings.Trim(strings.TrimPrefix(strings.ToLower(r.URL.Path), "/lite/"), "/")

		switch raw {
		case "moonanime":
			if parseBoolParam(r.URL.Query().Get("checksearch")) {
				show := m.checkSearch(r.Context(), r.URL.Query())
				writeCheckSearchResponseNoRCH(w, show, pluginQualityBadgeGet("moonanime"))
				return
			}
			m.index(w, r, links)
			return

		case "moonanime/video", "moonanime/video.m3u8":
			m.video(w, r, raw == "moonanime/video.m3u8", links)
			return
		}

		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "moonanime route not implemented", "balanser": raw})
	}
}

// --- checkSearch ---

func (m *moonAnimeChecker) checkSearch(ctx context.Context, q map[string][]string) bool {
	imdbID := strings.TrimSpace(firstQuery(q, "imdb_id"))
	title := strings.TrimSpace(firstQuery(q, "title"))

	seasons, ok := m.search(ctx, imdbID, "", title, 0)
	if ok && len(seasons) > 0 {
		return true
	}
	// Fallback: probe API host.
	return m.probe(ctx, http.MethodGet, m.apiHost)
}

func (m *moonAnimeChecker) probe(ctx context.Context, method, target string) bool {
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := m.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 400
}

// --- Search: new NMoonAnime API ---

func (m *moonAnimeChecker) search(ctx context.Context, imdbID, malID, title string, year int) ([]moonSeasonRef, bool) {
	endpoints := []string{"/moonanime/search", "/moonanime"}

	for _, ep := range endpoints {
		u := m.buildSearchURL(ep, imdbID, malID, title, year)
		if u == "" {
			continue
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "Mozilla/5.0")
		req.Header.Set("Referer", m.host)
		req.Header.Set("Accept", "application/json")

		resp, err := m.client.Do(req)
		if err != nil {
			log.Debug().Err(err).Str("url", u).Msg("moonanime: search request failed")
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			continue
		}

		var sr moonSearchResponse
		if err := decodeJSONLimited(resp.Body, 2<<20, &sr); err != nil {
			continue
		}

		// Filter valid seasons.
		var valid []moonSeasonRef
		for _, s := range sr.Seasons {
			if strings.TrimSpace(s.URL) != "" {
				if s.SeasonNumber <= 0 {
					s.SeasonNumber = 1
				}
				valid = append(valid, s)
			}
		}

		if len(valid) > 0 {
			// Deduplicate by URL.
			seen := map[string]bool{}
			var deduped []moonSeasonRef
			for _, s := range valid {
				if !seen[s.URL] {
					seen[s.URL] = true
					deduped = append(deduped, s)
				}
			}
			sort.Slice(deduped, func(i, j int) bool { return deduped[i].SeasonNumber < deduped[j].SeasonNumber })
			return deduped, true
		}
	}

	return nil, false
}

func (m *moonAnimeChecker) buildSearchURL(endpoint, imdbID, malID, title string, year int) string {
	qs := url.Values{}
	if malID != "" {
		qs.Set("mal_id", malID)
	} else if imdbID != "" {
		qs.Set("imdb_id", imdbID)
	} else if title != "" {
		qs.Set("title", title)
	} else {
		return ""
	}
	if year > 0 {
		qs.Set("year", strconv.Itoa(year))
	}
	return strings.TrimRight(m.apiHost, "/") + endpoint + "?" + qs.Encode()
}

// --- Index handler ---

func (m *moonAnimeChecker) index(w http.ResponseWriter, r *http.Request, links *proxylink.Manager) {
	q := r.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	imdbID := strings.TrimSpace(q.Get("imdb_id"))
	seasonURL := strings.TrimSpace(q.Get("season_url"))
	activeVoice := strings.TrimSpace(q.Get("t"))

	host := hostFromRequest(r)

	// If we have a season URL, fetch and parse it directly.
	if seasonURL != "" {
		m.renderSeason(w, r, host, rjson, title, originalTitle, seasonURL, activeVoice, links)
		return
	}

	// Search for seasons.
	seasons, ok := m.search(r.Context(), imdbID, "", title, 0)
	if !ok || len(seasons) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Single season → render it directly.
	if len(seasons) == 1 {
		m.renderSeason(w, r, host, rjson, title, originalTitle, seasons[0].URL, activeVoice, links)
		return
	}

	// Multiple seasons → show season picker.
	data := make([]map[string]any, 0, len(seasons))
	labels := make([]string, 0, len(seasons))
	for _, s := range seasons {
		name := fmt.Sprintf("%d сезон", s.SeasonNumber)
		data = append(data, map[string]any{
			"method": "link",
			"id":     s.SeasonNumber,
			"url": host + "/lite/moonanime?rjson=" + getsTVBool(rjson) +
				"&title=" + url.QueryEscape(title) +
				"&original_title=" + url.QueryEscape(originalTitle) +
				"&season_url=" + url.QueryEscape(s.URL),
			"name": name,
		})
		labels = append(labels, name)
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "season", "data": data})
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

func (m *moonAnimeChecker) renderSeason(w http.ResponseWriter, r *http.Request, host string, rjson bool, title, originalTitle, seasonURL, activeVoice string, links *proxylink.Manager) {
	content := m.fetchSeasonPage(r.Context(), seasonURL)
	if content == nil || (len(content.Voices) == 0) {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if !content.IsSeries {
		// Movie — play first voice file directly.
		for _, v := range content.Voices {
			if v.File != "" {
				bestURL := m.bestStreamURL(v.File, r, links)
				writeJSON(w, http.StatusOK, map[string]any{
					"method": "play",
					"url":    bestURL,
					"title":  getsTVJoinName(title, originalTitle),
				})
				return
			}
		}
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Series — voices + episodes.
	voiceNames := make([]string, 0, len(content.Voices))
	voiceMap := map[string]*moonVoiceContent{}
	for i := range content.Voices {
		v := &content.Voices[i]
		voiceNames = append(voiceNames, v.Name)
		voiceMap[v.Name] = v
	}

	if activeVoice == "" || voiceMap[activeVoice] == nil {
		activeVoice = voiceNames[0]
	}

	voiceRows := make([]map[string]any, 0, len(voiceNames))
	for _, vn := range voiceNames {
		voiceRows = append(voiceRows, map[string]any{
			"method": "link",
			"name":   vn,
			"active": vn == activeVoice,
			"url": host + "/lite/moonanime?rjson=" + getsTVBool(rjson) +
				"&title=" + url.QueryEscape(title) +
				"&original_title=" + url.QueryEscape(originalTitle) +
				"&season_url=" + url.QueryEscape(seasonURL) +
				"&t=" + url.QueryEscape(vn),
		})
	}

	voice := voiceMap[activeVoice]
	baseTitle := getsTVJoinName(title, originalTitle)
	data := make([]map[string]any, 0, len(voice.Episodes))
	labels := make([]string, 0, len(voice.Episodes))
	seasons := make([]int, 0, len(voice.Episodes))
	episodesNum := make([]int, 0, len(voice.Episodes))

	for _, ep := range voice.Episodes {
		if ep.File == "" {
			continue
		}
		name := ep.Name
		if name == "" {
			name = fmt.Sprintf("%d серия", ep.Number)
		}
		query := "file=" + url.QueryEscape(ep.File) +
			"&title=" + url.QueryEscape(title) +
			"&original_title=" + url.QueryEscape(originalTitle)
		link := host + "/lite/moonanime/video?" + query
		stream := host + "/lite/moonanime/video.m3u8?" + query + "&play=true"

		data = append(data, map[string]any{
			"method": "call",
			"url":    link,
			"stream": stream,
			"s":      1,
			"e":      ep.Number,
			"name":   name,
			"title":  fmt.Sprintf("%s (%s)", baseTitle, name),
		})
		labels = append(labels, name)
		seasons = append(seasons, 1)
		episodesNum = append(episodesNum, ep.Number)
	}

	if len(data) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "episode", "voice": voiceRows, "data": data})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for _, row := range voiceRows {
		getsTVAppendVoiceHTML(&sb, row)
	}
	sb.WriteString(`</div><div class="videos__line">`)
	for i, row := range data {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, seasons[i], episodesNum[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// --- Video handler ---

func (m *moonAnimeChecker) video(w http.ResponseWriter, r *http.Request, forcePlay bool, links *proxylink.Manager) {
	q := r.URL.Query()
	file := strings.TrimSpace(q.Get("file"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))

	// Legacy: vod= parameter from old API.
	if file == "" {
		file = strings.TrimSpace(q.Get("vod"))
	}
	if file == "" {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	bestURL := m.bestStreamURL(file, r, links)

	if forcePlay || parseBoolParam(q.Get("play")) {
		http.Redirect(w, r, bestURL, http.StatusFound)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"method": "play",
		"url":    bestURL,
		"title":  getsTVJoinName(title, originalTitle),
	})
}

func (m *moonAnimeChecker) bestStreamURL(file string, r *http.Request, links *proxylink.Manager) string {
	// Parse [quality]url format.
	matches := moonStreamQualRe.FindAllStringSubmatch(file, -1)
	if len(matches) > 0 {
		// Sort by quality descending.
		type qs struct {
			url     string
			quality string
			weight  int
		}
		var streams []qs
		for _, mm := range matches {
			q := normalizeQuality(mm[1])
			streams = append(streams, qs{url: mm[2], quality: q, weight: qualityWeight(q)})
		}
		sort.Slice(streams, func(i, j int) bool { return streams[i].weight > streams[j].weight })
		return streamProxyURL(r, streams[0].url, "moonanime", links)
	}
	// Plain URL.
	if strings.HasPrefix(file, "http") {
		return streamProxyURL(r, file, "moonanime", links)
	}
	return file
}

// --- Season page fetching & PlayerJS parsing ---

func (m *moonAnimeChecker) fetchSeasonPage(ctx context.Context, pageURL string) *moonSeasonContent {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36")
	req.Header.Set("Referer", m.host+"/")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*")

	resp, err := m.client.Do(req)
	if err != nil {
		log.Debug().Err(err).Str("url", pageURL).Msg("moonanime: season page fetch failed")
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

	return m.parseSeasonPage(string(body))
}

func (m *moonAnimeChecker) parseSeasonPage(htmlText string) *moonSeasonContent {
	decoded := html.UnescapeString(htmlText)
	content := &moonSeasonContent{}

	// Try to extract PlayerJS payload — may be XOR encrypted.
	filePayload := m.extractPlayerFile(decoded)
	if filePayload == "" {
		return content
	}

	// Try parsing as JSON array (series with voices/episodes).
	var jsonData any
	if err := stdjson.Unmarshal([]byte(filePayload), &jsonData); err == nil {
		voices := m.parseSeriesJSON(jsonData)
		if len(voices) > 0 {
			content.IsSeries = true
			content.Voices = voices
			return content
		}
	}

	// Try [quality]url format or plain URL (movie).
	if strings.Contains(filePayload, "http") {
		content.Voices = []moonVoiceContent{{Name: "Основне джерело", File: filePayload}}
		return content
	}

	return content
}

// extractPlayerFile finds the "file" value in PlayerJS, handling XOR encryption.
func (m *moonAnimeChecker) extractPlayerFile(text string) string {
	candidates := []string{text}

	// Check for atob() outer script encryption (key in first 32 bytes).
	if am := moonAtobRe.FindStringSubmatch(text); len(am) >= 2 {
		if decoded := xorDecodeOuter(am[1]); decoded != "" && len(decoded) > 100 {
			candidates = append([]string{decoded}, candidates...)
		}
	}

	for _, src := range candidates {
		// Find "new Playerjs(" or "Playerjs({"
		if !moonPlayerJSRe.MatchString(src) && !strings.Contains(src, "Playerjs(") {
			continue
		}

		// Extract the file: value.
		fileVal := extractJSValue(src, "file")
		if fileVal == "" {
			continue
		}

		// fileVal may be: plain string, JSON, or helper function call like fnName('base64')
		resolved := m.resolveFileValue(fileVal, src)
		if resolved != "" {
			return resolved
		}
	}

	return ""
}

// resolveFileValue handles plain text, atob(), and custom XOR helpers.
func (m *moonAnimeChecker) resolveFileValue(fileVal, context string) string {
	fileVal = strings.TrimSpace(fileVal)
	if fileVal == "" {
		return ""
	}

	// Already looks like JSON or URL — return as-is.
	if fileVal[0] == '[' || fileVal[0] == '{' || strings.HasPrefix(fileVal, "http") {
		return strings.ReplaceAll(html.UnescapeString(fileVal), `\/`, "/")
	}

	// atob('...') call.
	if strings.HasPrefix(strings.ToLower(fileVal), "atob(") {
		if am := moonAtobRe.FindStringSubmatch(fileVal); len(am) >= 2 {
			if decoded := safeBase64Decode(am[1]); decoded != "" {
				return strings.ReplaceAll(html.UnescapeString(decoded), `\/`, "/")
			}
		}
	}

	// JSON.parse(helperFn('base64')) or helperFn('base64')
	if hm := moonHelperCallRe.FindStringSubmatch(fileVal); len(hm) >= 3 {
		helperName := hm[1]
		payload := hm[2]

		if strings.EqualFold(helperName, "atob") {
			if decoded := safeBase64Decode(payload); decoded != "" {
				return strings.ReplaceAll(html.UnescapeString(decoded), `\/`, "/")
			}
		}

		// XOR helper: find the key in function body.
		key := extractHelperKey(context, helperName)
		if key != "" {
			if decoded := xorDecodeWithKey(payload, key); decoded != "" {
				return strings.ReplaceAll(html.UnescapeString(decoded), `\/`, "/")
			}
		}
	}

	return strings.ReplaceAll(html.UnescapeString(fileVal), `\/`, "/")
}

// parseSeriesJSON parses the PlayerJS file JSON into voices/episodes.
func (m *moonAnimeChecker) parseSeriesJSON(data any) []moonVoiceContent {
	arr, ok := data.([]any)
	if !ok {
		// Single object? Wrap it.
		obj, ok := data.(map[string]any)
		if !ok {
			return nil
		}
		arr = []any{obj}
	}

	var voices []moonVoiceContent
	voiceIdx := 1

	for _, item := range arr {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}

		folder, hasFolders := obj["folder"]
		if !hasFolders {
			// Not a folder — might be a movie entry.
			continue
		}

		voiceName := ""
		if t, ok := obj["title"].(string); ok && strings.TrimSpace(t) != "" {
			voiceName = strings.TrimSpace(html.UnescapeString(t))
		}
		if voiceName == "" {
			voiceName = fmt.Sprintf("Озвучка %d", voiceIdx)
		}

		episodes := m.parseFolderEpisodes(folder)
		if len(episodes) > 0 {
			voices = append(voices, moonVoiceContent{Name: voiceName, Episodes: episodes})
		}
		voiceIdx++
	}

	return voices
}

func (m *moonAnimeChecker) parseFolderEpisodes(folder any) []moonEpisodeContent {
	arr, ok := folder.([]any)
	if !ok {
		return nil
	}

	var episodes []moonEpisodeContent
	idx := 1

	for _, item := range arr {
		obj, ok := item.(map[string]any)
		if !ok {
			idx++
			continue
		}

		// Nested folders (seasons inside voice) — flatten.
		if subFolder, has := obj["folder"]; has {
			sub := m.parseFolderEpisodes(subFolder)
			episodes = append(episodes, sub...)
			idx++
			continue
		}

		fileVal := ""
		if f, ok := obj["file"].(string); ok {
			fileVal = strings.TrimSpace(html.UnescapeString(strings.ReplaceAll(f, `\/`, "/")))
		}
		if fileVal == "" {
			idx++
			continue
		}

		title := ""
		if t, ok := obj["title"].(string); ok {
			title = strings.TrimSpace(html.UnescapeString(t))
		}
		if title == "" {
			title = fmt.Sprintf("Епізод %d", idx)
		}

		num := extractEpisodeNumber(title, idx)
		episodes = append(episodes, moonEpisodeContent{Name: title, Number: num, File: fileVal})
		idx++
	}

	sort.Slice(episodes, func(i, j int) bool {
		if episodes[i].Number != episodes[j].Number {
			return episodes[i].Number < episodes[j].Number
		}
		return episodes[i].Name < episodes[j].Name
	})

	return episodes
}

// --- XOR / Base64 helpers ---

func xorDecodeOuter(b64Payload string) string {
	raw := safeBase64DecodeBytes(b64Payload)
	if len(raw) <= 32 {
		return ""
	}
	key := raw[:32]
	data := raw[32:]
	decoded := make([]byte, len(data))
	for i := range data {
		decoded[i] = data[i] ^ key[i%len(key)]
	}
	return decodeBytes(decoded)
}

func xorDecodeWithKey(b64Payload, key string) string {
	payloadBytes := safeBase64DecodeBytes(b64Payload)
	if len(payloadBytes) == 0 {
		return ""
	}
	keyBytes := []byte(key)
	if len(keyBytes) == 0 {
		return ""
	}
	decoded := make([]byte, len(payloadBytes))
	for i := range payloadBytes {
		decoded[i] = payloadBytes[i] ^ keyBytes[i%len(keyBytes)]
	}
	return decodeBytes(decoded)
}

func safeBase64Decode(s string) string {
	b := safeBase64DecodeBytes(s)
	if b == nil {
		return ""
	}
	return decodeBytes(b)
}

func safeBase64DecodeBytes(s string) []byte {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	// Fix padding.
	if r := len(s) % 4; r != 0 {
		s += strings.Repeat("=", 4-r)
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil
	}
	return b
}

func decodeBytes(data []byte) string {
	if utf8.Valid(data) {
		return string(data)
	}
	// Fallback: treat as Latin-1.
	runes := make([]rune, len(data))
	for i, b := range data {
		runes[i] = rune(b)
	}
	return string(runes)
}

func extractHelperKey(contextText, helperName string) string {
	re := moonHelperKeyRe(helperName)
	if m := re.FindStringSubmatch(contextText); len(m) >= 2 {
		return m[1]
	}
	return ""
}

// extractJSValue extracts a JavaScript object value by key from source text.
func extractJSValue(text, key string) string {
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(key) + `\b\s*:\s*`)
	loc := re.FindStringIndex(text)
	if loc == nil {
		return ""
	}

	idx := loc[1]
	for idx < len(text) && (text[idx] == ' ' || text[idx] == '\t') {
		idx++
	}
	if idx >= len(text) {
		return ""
	}

	ch := text[idx]

	// Quoted string.
	if ch == '"' || ch == '\'' {
		return readJSString(text, idx)
	}

	// Array or object — find matching bracket.
	if ch == '[' || ch == '{' {
		close := byte(']')
		if ch == '{' {
			close = '}'
		}
		end := findMatchingBracket(text, idx, ch, close)
		if end >= idx {
			return text[idx : end+1]
		}
		return ""
	}

	// Unquoted value (function call, variable, etc).
	end := idx
	for end < len(text) && text[end] != ',' && text[end] != '}' && text[end] != '\n' && text[end] != '\r' {
		end++
	}
	return strings.TrimSpace(text[idx:end])
}

func readJSString(text string, start int) string {
	if start >= len(text) {
		return ""
	}
	quote := text[start]
	var sb strings.Builder
	escaped := false
	for i := start + 1; i < len(text); i++ {
		ch := text[i]
		if escaped {
			sb.WriteByte(ch)
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if ch == quote {
			return sb.String()
		}
		sb.WriteByte(ch)
	}
	return ""
}

func findMatchingBracket(text string, start int, open, close byte) int {
	depth := 0
	escaped := false
	var inString byte

	for i := start; i < len(text); i++ {
		ch := text[i]
		if inString != 0 {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == inString {
				inString = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			inString = ch
			continue
		}
		if ch == open {
			depth++
		} else if ch == close {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// --- Quality helpers ---

// Pre-compiled regexes for quality/episode extraction in moonanime hot path.
// Was previously re-compiled inside normalizeQuality/qualityWeight/extractEpisodeNumber
// on every parse — those functions are called per file/per quality, dozens of
// times per /lite/moonanime request.
var (
	moonQualityCaptureRe = regexp.MustCompile(`(\d{3,4})`)
	moonQualityScanRe    = regexp.MustCompile(`\d{3,4}`)
	// Note: the original site code used `(\d+)(?!.*\d)` which RE2 rejects
	// (negative lookahead) — the function would panic on first call. The
	// intended meaning was "the last run of digits in the string", which
	// is `(\d+)\D*\z` in RE2 syntax. \z anchors to true end-of-text
	// (different from $ which can match before a final newline).
	moonTrailingNumberRe = regexp.MustCompile(`(\d+)\D*\z`)
)

func normalizeQuality(q string) string {
	q = strings.TrimSpace(strings.Trim(q, "[]"))
	if strings.EqualFold(q, "auto") {
		return "auto"
	}
	if m := moonQualityCaptureRe.FindStringSubmatch(q); len(m) >= 2 {
		return m[1] + "p"
	}
	return q
}

func qualityWeight(q string) int {
	if m := moonQualityScanRe.FindString(q); m != "" {
		n, _ := strconv.Atoi(m)
		return n
	}
	if strings.EqualFold(q, "auto") {
		return 1
	}
	return 0
}

func extractEpisodeNumber(title string, fallback int) int {
	if m := moonEpisodeNumRe.FindStringSubmatch(title); len(m) >= 2 {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return n
		}
	}
	// Trailing number.
	if m := moonTrailingNumberRe.FindStringSubmatch(title); len(m) >= 2 {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return n
		}
	}
	return fallback
}

func decodeJSONLimited(r io.Reader, n int64, out any) error {
	return stdjson.NewDecoder(io.LimitReader(r, n)).Decode(out)
}
