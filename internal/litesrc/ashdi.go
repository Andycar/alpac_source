package litesrc

import (
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

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/proxylink"

	"github.com/rs/zerolog/log"
)

const defaultWormholeHost = "https://wormhole.lampame.v6.rocks"

var (
	ashdiIframeRe       = regexp.MustCompile(`src="(https?://[^"]+)"`)
	ashdiPlayerBlockRe  = regexp.MustCompile(`(?s)new Playerjs(.*)$`)
	ashdiFileJSONRe     = regexp.MustCompile(`file:\s*'([^\n\r]+)',`)
	ashdiMovieFileRe    = regexp.MustCompile(`file:\s*(?:"|')(?P<hls>https?://[^"'\n\r\t ]+/index\.m3u8)`)
	ashdiSubtitleRe     = regexp.MustCompile(`subtitle(?:")?:\s*"([^"]+)"`)
	ashdiEpisodeNumRe   = regexp.MustCompile(`([0-9]+)$`)
	ashdiSeasonNumEndRe = regexp.MustCompile(`([0-9]+)$`)
)

type ashdiChecker struct {
	client       *http.Client
	host         string // base.ashdi.vip
	wormholeHost string // wormhole.lampame.v6.rocks
}

// ashdiEmbed holds parsed embed data. Exactly one of Content, Voices or Serial
// is populated on success.
type ashdiEmbed struct {
	IsEmpty bool
	Content string            // raw Playerjs block (legacy single movie)
	Voices  []ashdiMultiVoice // multivoice data (movies with voices or serials)
}

// ashdiMultiVoice is a unified entry from the multivoice Playerjs file field.
// For movies: File is set, Folder is nil.
// For serials: Folder is set, File is empty.
type ashdiMultiVoice struct {
	Title    string        `json:"title"`
	File     string        `json:"file"`
	Subtitle string        `json:"subtitle"`
	Poster   string        `json:"poster"`
	ID       string        `json:"id"`
	Folder   []ashdiSeason `json:"folder"`
}

// ashdiVoice is legacy serial-only voice structure (from old read_api flow).
type ashdiVoice struct {
	Title  string        `json:"title"`
	Folder []ashdiSeason `json:"folder"`
}

type ashdiSeason struct {
	Title  string        `json:"title"`
	Folder []ashdiSeries `json:"folder"`
}

type ashdiSeries struct {
	Title    string `json:"title"`
	File     string `json:"file"`
	Subtitle string `json:"subtitle"`
}

// ashdiWormholeResponse is the JSON response from the wormhole API.
type ashdiWormholeResponse struct {
	Play string `json:"play"` // e.g. "https://ashdi.vip/vod/232851"
}

func NewAshdiChecker(cfg config.Config) *ashdiChecker {
	host := strings.TrimSpace(strings.TrimRight(cfg.Online.Ashdi.Host, "/"))
	if host != "" && !strings.Contains(host, "://") {
		host = "https://" + host
	}
	return &ashdiChecker{
		client:       httpclient.New(12 * time.Second),
		host:         host,
		wormholeHost: defaultWormholeHost,
	}
}

func (a *ashdiChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show := a.checkSearch(req)
			writeCheckSearchResponse(w, show, pluginQualityBadgeGet("ashdi"))
			return
		}

		a.index(w, req, links)
	}
}

func (a *ashdiChecker) checkSearch(req *http.Request) bool {
	q := req.URL.Query()

	// Try wormhole first (by imdb_id, no token needed).
	imdbID := strings.TrimSpace(q.Get("imdb_id"))
	if imdbID != "" {
		if playURL := a.fetchWormholeURL(req, imdbID); playURL != "" {
			return true
		}
	}

	// Fallback: old API by kinopoisk_id.
	kp := strings.TrimSpace(q.Get("kinopoisk_id"))
	if kp != "" {
		if _, err := strconv.ParseInt(kp, 10, 64); err == nil {
			if embed, ok := a.fetchEmbedLegacy(req, kp); ok && !embed.IsEmpty {
				return true
			}
		}
	}

	return false
}

func (a *ashdiChecker) index(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	imdbID := strings.TrimSpace(q.Get("imdb_id"))
	kp := strings.TrimSpace(q.Get("kinopoisk_id"))

	t, tSet := getsTVQueryInt(q.Get("t"))
	s, sSet := getsTVQueryInt(q.Get("s"))
	if !tSet {
		t = -1
	}
	if !sSet {
		s = -1
	}

	// Try wormhole path first (imdb_id → ashdi URL → multivoice).
	if imdbID != "" {
		embed, ok := a.fetchViaWormhole(req, imdbID)
		if ok && !embed.IsEmpty {
			a.renderEmbed(w, req, rjson, embed, kp, imdbID, title, originalTitle, s, t, links)
			return
		}
	}

	// Fallback: legacy API by kinopoisk_id.
	if kp == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}
	if _, err := strconv.ParseInt(kp, 10, 64); err != nil {
		writeGetsTVEmpty(w, rjson)
		return
	}

	embed, ok := a.fetchEmbedLegacy(req, kp)
	if !ok || embed.IsEmpty {
		writeGetsTVEmpty(w, rjson)
		return
	}

	a.renderEmbed(w, req, rjson, embed, kp, imdbID, title, originalTitle, s, t, links)
}

// renderEmbed dispatches to the correct renderer based on embed contents.
func (a *ashdiChecker) renderEmbed(
	w http.ResponseWriter, req *http.Request, rjson bool,
	embed ashdiEmbed, kp, imdbID, title, originalTitle string,
	s, t int, links *proxylink.Manager,
) {
	// Multivoice data from wormhole.
	if len(embed.Voices) > 0 {
		// Check if it's a movie (voices have File) or serial (voices have Folder).
		if embed.Voices[0].File != "" {
			a.writeMovieMultiVoice(w, req, rjson, embed.Voices, title, originalTitle, links)
		} else {
			// Convert to legacy voice format for serial rendering.
			voices := make([]ashdiVoice, 0, len(embed.Voices))
			for _, v := range embed.Voices {
				voices = append(voices, ashdiVoice{Title: v.Title, Folder: v.Folder})
			}
			a.writeSerial(w, req, rjson, voices, kp, imdbID, title, originalTitle, s, t, links)
		}
		return
	}

	// Legacy single-movie block.
	if embed.Content != "" {
		a.writeMovieLegacy(w, req, rjson, embed.Content, title, originalTitle, links)
		return
	}

	writeGetsTVEmpty(w, rjson)
}

// ─── WORMHOLE FLOW ─────────────────────────────────────────────────

// fetchWormholeURL calls the wormhole API and returns the ashdi play URL.
func (a *ashdiChecker) fetchWormholeURL(req *http.Request, imdbID string) string {
	wormURL := a.wormholeHost + "/?imdb_id=" + url.QueryEscape(imdbID)

	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, wormURL, nil)
	if err != nil {
		return ""
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := balancerDoWithRetry(httpReq.Context(), a.client, httpReq, 2)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if err != nil {
		return ""
	}

	var wr ashdiWormholeResponse
	if err := stdjson.Unmarshal(body, &wr); err != nil || wr.Play == "" {
		return ""
	}

	return wr.Play
}

// fetchViaWormhole resolves imdb_id through the wormhole API, then fetches the
// ashdi page with ?multivoice to get all voice tracks.
func (a *ashdiChecker) fetchViaWormhole(req *http.Request, imdbID string) (ashdiEmbed, bool) {
	playURL := a.fetchWormholeURL(req, imdbID)
	if playURL == "" {
		return ashdiEmbed{}, false
	}

	// Append ?multivoice to get all voice tracks.
	multiURL := playURL + "?multivoice"
	content, ok := a.fetchText(req, multiURL)
	if !ok {
		return ashdiEmbed{}, false
	}

	if !strings.Contains(content, "new Playerjs") {
		return ashdiEmbed{}, false
	}

	rawFile := submatch1(ashdiFileJSONRe, content)
	if rawFile == "" {
		// Fallback: try without multivoice for single-stream content.
		content2, ok2 := a.fetchText(req, playURL)
		if !ok2 || !strings.Contains(content2, "new Playerjs") {
			return ashdiEmbed{}, false
		}
		block := submatch1(ashdiPlayerBlockRe, content2)
		if strings.TrimSpace(block) == "" {
			return ashdiEmbed{}, false
		}
		return ashdiEmbed{Content: block}, true
	}

	// If file field starts with '[' it's a JSON array of voices.
	// Otherwise it's a single stream URL — return as legacy Content block.
	if !strings.HasPrefix(strings.TrimSpace(rawFile), "[") {
		block := submatch1(ashdiPlayerBlockRe, content)
		if strings.TrimSpace(block) == "" {
			return ashdiEmbed{}, false
		}
		return ashdiEmbed{Content: block}, true
	}

	var voices []ashdiMultiVoice
	if err := stdjson.Unmarshal([]byte(rawFile), &voices); err != nil {
		log.Debug().Err(err).Str("imdb", imdbID).Msg("ashdi: failed to parse multivoice JSON")
		return ashdiEmbed{}, false
	}
	if len(voices) == 0 {
		return ashdiEmbed{IsEmpty: true}, true
	}

	return ashdiEmbed{Voices: voices}, true
}

// ─── LEGACY API FLOW ───────────────────────────────────────────────

// fetchEmbedLegacy uses the old read_api.php endpoint (requires token).
func (a *ashdiChecker) fetchEmbedLegacy(req *http.Request, kinopoiskID string) (ashdiEmbed, bool) {
	if a.host == "" {
		return ashdiEmbed{}, false
	}

	productURL := a.host + "/api/product/read_api.php?kinopoisk=" + url.QueryEscape(kinopoiskID)
	product, ok := a.fetchText(req, productURL)
	if !ok {
		return ashdiEmbed{}, false
	}
	if strings.Contains(strings.ToLower(product), "product does not exist") {
		return ashdiEmbed{IsEmpty: true}, true
	}

	iframeURI := submatch1(ashdiIframeRe, product)
	if iframeURI == "" {
		return ashdiEmbed{}, false
	}

	content, ok := a.fetchText(req, iframeURI)
	if !ok {
		return ashdiEmbed{}, false
	}
	if !strings.Contains(content, "new Playerjs") {
		return ashdiEmbed{}, false
	}

	rawFile := submatch1(ashdiFileJSONRe, content)
	if rawFile != "" {
		var serial []ashdiVoice
		if err := stdjson.Unmarshal([]byte(rawFile), &serial); err == nil && len(serial) > 0 {
			// Convert to multivoice format.
			voices := make([]ashdiMultiVoice, 0, len(serial))
			for _, v := range serial {
				voices = append(voices, ashdiMultiVoice{Title: v.Title, Folder: v.Folder})
			}
			return ashdiEmbed{Voices: voices}, true
		}
	}

	block := submatch1(ashdiPlayerBlockRe, content)
	if strings.TrimSpace(block) == "" {
		return ashdiEmbed{}, false
	}
	return ashdiEmbed{Content: block}, true
}

// ─── FETCH HELPER ──────────────────────────────────────────────────

func (a *ashdiChecker) fetchText(req *http.Request, target string) (string, bool) {
	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, target, nil)
	if err != nil {
		return "", false
	}
	httpReq.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	httpReq.Header.Set("Referer", "https://ashdi.vip/")

	resp, err := balancerDoWithRetry(httpReq.Context(), a.client, httpReq, 2)
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

// ─── MOVIE RENDERERS ───────────────────────────────────────────────

// writeMovieMultiVoice renders a movie with multiple voice tracks.
func (a *ashdiChecker) writeMovieMultiVoice(
	w http.ResponseWriter, req *http.Request, rjson bool,
	voices []ashdiMultiVoice, title, originalTitle string,
	links *proxylink.Manager,
) {
	rows := make([]map[string]any, 0, len(voices))
	for _, v := range voices {
		stream := ashdiFixStream(strings.TrimSpace(v.File))
		if stream == "" {
			continue
		}
		stream = streamProxyURL(req, stream, "ashdi", links)

		voiceName := strings.TrimSpace(v.Title)
		if voiceName == "" {
			voiceName = "По умолчанию"
		}

		row := map[string]any{
			"method": "play",
			"url":    stream,
			"stream": stream,
			"name":   voiceName,
			"title":  getsTVJoinName(title, originalTitle),
		}
		if subs := ashdiParseSubtitleList(v.Subtitle); len(subs) > 0 {
			row["subtitles"] = subs
		}
		rows = append(rows, row)
	}

	if len(rows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "movie",
			"data": rows,
		})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range rows {
		name, _ := row["name"].(string)
		getsTVAppendMovieHTML(&sb, row, name, i == 0, 0, 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// writeMovieLegacy renders a single movie from legacy Playerjs block.
func (a *ashdiChecker) writeMovieLegacy(
	w http.ResponseWriter, req *http.Request, rjson bool,
	content, title, originalTitle string,
	links *proxylink.Manager,
) {
	hls := submatch1(ashdiMovieFileRe, content)
	if hls == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}
	hls = ashdiFixStream(hls)
	hls = streamProxyURL(req, hls, "ashdi", links)

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

// ─── SERIAL RENDERER ───────────────────────────────────────────────

func (a *ashdiChecker) writeSerial(
	w http.ResponseWriter, req *http.Request, rjson bool,
	voices []ashdiVoice, kp, imdbID, title, originalTitle string,
	s, t int, links *proxylink.Manager,
) {
	if len(voices) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	host := hostFromRequest(req)
	encTitle := url.QueryEscape(title)
	encOriginal := url.QueryEscape(originalTitle)

	// Build ID param for callbacks — prefer imdb_id since wormhole needs it.
	idParam := ""
	if imdbID != "" {
		idParam = "&imdb_id=" + url.QueryEscape(imdbID)
	}
	if kp != "" {
		idParam += "&kinopoisk_id=" + url.QueryEscape(kp)
	}

	// Season list.
	if s == -1 {
		type seasonItem struct {
			num   int
			title string
			link  string
		}
		items := make([]seasonItem, 0, 8)
		seen := make(map[string]struct{}, 8)

		for _, voice := range voices {
			for _, season := range voice.Folder {
				seasonTitle := strings.TrimSpace(season.Title)
				if seasonTitle == "" {
					continue
				}
				if _, ok := seen[seasonTitle]; ok {
					continue
				}
				seen[seasonTitle] = struct{}{}

				seasonNum := submatch1(ashdiSeasonNumEndRe, seasonTitle)
				if seasonNum == "" {
					continue
				}
				link := fmt.Sprintf("%s/lite/ashdi?rjson=%s%s&title=%s&original_title=%s&s=%s",
					host, getsTVBool(rjson), idParam, encTitle, encOriginal, seasonNum)
				num, _ := strconv.Atoi(seasonNum)
				items = append(items, seasonItem{num: num, title: seasonTitle, link: link})
			}
		}

		sort.Slice(items, func(i, j int) bool {
			if items[i].num == items[j].num {
				return items[i].title < items[j].title
			}
			return items[i].num < items[j].num
		})

		if len(items) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}

		rows := make([]map[string]any, 0, len(items))
		labels := make([]string, 0, len(items))
		for _, item := range items {
			rows = append(rows, map[string]any{
				"method": "link",
				"id":     item.num,
				"url":    item.link,
				"name":   item.title,
			})
			labels = append(labels, item.title)
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

	// Voice and episode list.
	voiceRows := make([]map[string]any, 0, len(voices))
	voiceIndex := make([]int, 0, len(voices))
	for i, voice := range voices {
		if !ashdiVoiceHasSeason(voice, s) {
			continue
		}
		if t == -1 {
			t = i
		}
		link := fmt.Sprintf("%s/lite/ashdi?rjson=%s%s&title=%s&original_title=%s&s=%d&t=%d",
			host, getsTVBool(rjson), idParam, encTitle, encOriginal, s, i)
		voiceRows = append(voiceRows, map[string]any{
			"method": "link",
			"name":   strings.TrimSpace(voice.Title),
			"active": t == i,
			"url":    link,
		})
		voiceIndex = append(voiceIndex, i)
	}

	if len(voiceRows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	if t < 0 || t >= len(voices) || !ashdiVoiceHasSeason(voices[t], s) {
		t = voiceIndex[0]
		voiceRows[0]["active"] = true
	}

	season := ashdiFindSeason(voices[t], s)
	if season == nil || len(season.Folder) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	type episodeRow struct {
		data  map[string]any
		label string
		ep    int
	}
	episodes := make([]episodeRow, 0, len(season.Folder))
	baseTitle := getsTVJoinName(title, originalTitle)

	for _, ep := range season.Folder {
		label := strings.TrimSpace(ep.Title)
		if label == "" {
			continue
		}
		epNum, _ := strconv.Atoi(submatch1(ashdiEpisodeNumRe, label))
		stream := ashdiFixStream(strings.TrimSpace(ep.File))
		if stream == "" {
			continue
		}
		stream = streamProxyURL(req, stream, "ashdi", links)

		row := map[string]any{
			"method": "play",
			"url":    stream,
			"stream": stream,
			"s":      s,
			"e":      epNum,
			"name":   label,
			"title":  fmt.Sprintf("%s (%s)", baseTitle, label),
		}
		if subs := ashdiParseSubtitleList(ep.Subtitle); len(subs) > 0 {
			row["subtitles"] = subs
		}
		episodes = append(episodes, episodeRow{
			data:  row,
			label: label,
			ep:    epNum,
		})
	}

	if len(episodes) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	sort.Slice(episodes, func(i, j int) bool {
		if episodes[i].ep == episodes[j].ep {
			return episodes[i].label < episodes[j].label
		}
		return episodes[i].ep < episodes[j].ep
	})

	rows := make([]map[string]any, 0, len(episodes))
	labels := make([]string, 0, len(episodes))
	seasons := make([]int, 0, len(episodes))
	episodeNums := make([]int, 0, len(episodes))
	for _, row := range episodes {
		rows = append(rows, row.data)
		labels = append(labels, row.label)
		seasons = append(seasons, s)
		episodeNums = append(episodeNums, row.ep)
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type":  "episode",
			"data":  rows,
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
	for i, row := range rows {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, seasons[i], episodeNums[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// ─── HELPERS ───────────────────────────────────────────────────────

func ashdiVoiceHasSeason(voice ashdiVoice, s int) bool {
	for _, season := range voice.Folder {
		if strings.HasSuffix(strings.TrimSpace(season.Title), " "+strconv.Itoa(s)) {
			return true
		}
	}
	return false
}

func ashdiFindSeason(voice ashdiVoice, s int) *ashdiSeason {
	for i := range voice.Folder {
		season := &voice.Folder[i]
		if strings.HasSuffix(strings.TrimSpace(season.Title), " "+strconv.Itoa(s)) {
			return season
		}
	}
	return nil
}

func ashdiFixStream(v string) string {
	return strings.ReplaceAll(v, "0yql3tj", "oyql3tj")
}

func ashdiParseSubtitleBlock(content string) []map[string]string {
	raw := submatch1(ashdiSubtitleRe, content)
	return ashdiParseSubtitleList(raw)
}

func ashdiParseSubtitleList(raw string) []map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	out := make([]map[string]string, 0, 2)
	for _, match := range vdbmoviesSubtitleRe.FindAllStringSubmatch(raw, -1) {
		if len(match) != 3 {
			continue
		}
		link := ashdiFixStream(strings.TrimSpace(match[2]))
		if link == "" {
			continue
		}
		out = append(out, map[string]string{
			"label": html.EscapeString(strings.TrimSpace(match[1])),
			"url":   link,
		})
	}
	return out
}
