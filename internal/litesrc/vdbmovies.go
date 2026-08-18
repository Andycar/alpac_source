package litesrc

import (
	"encoding/base64"
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
	"sync"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/proxylink"
)

var (
	vdbmoviesFileRe            = regexp.MustCompile(`file:\s*'#\.([^']+)`)
	vdbmoviesFallbackFileRe    = regexp.MustCompile(`file:\s*'([^']+)'`)
	vdbmoviesForbiddenQuality  = regexp.MustCompile(`forbidden_quality:\s*(?:"|')([^"']+)(?:"|')`)
	vdbmoviesDefaultQuality    = regexp.MustCompile(`default_quality:\s*(?:"|')([^"']+)(?:"|')`)
	vdbmoviesSubtitleRe        = regexp.MustCompile(`\[([^\]]+)\](https?://[^,\[\]\s]+)`)
	vdbmoviesStreamQualityRe   = regexp.MustCompile(`\[([^\]]+)\](https?://[^\[\|,\n\r\t ]+\.m3u8)`)
	vdbmoviesSeasonNumberRe    = regexp.MustCompile(`^([0-9]+)`)
	vdbmoviesEpisodeNumberRe   = regexp.MustCompile(`^([0-9]+)`)
	vdbmoviesTrashEncodedOnce  sync.Once
	vdbmoviesTrashEncodedCache []string
)

type vdbmoviesMovie struct {
	Title     string `json:"title"`
	File      string `json:"file"`
	Subtitle  string `json:"subtitle"`
	Subtitles string `json:"subtitles"`
}

type vdbmoviesVoice struct {
	Title  string            `json:"title"`
	Folder []vdbmoviesSeason `json:"folder"`
}

type vdbmoviesSeason struct {
	Title  string             `json:"title"`
	Folder []vdbmoviesEpisode `json:"folder"`
}

type vdbmoviesEpisode struct {
	Title string `json:"title"`
	File  string `json:"file"`
}

type vdbmoviesEmbed struct {
	Movies  []vdbmoviesMovie
	Serial  []vdbmoviesVoice
	Quality string
}

type vdbmoviesChecker struct {
	client *http.Client
	host   string
}

func NewVDBmoviesChecker(cfg config.Config) *vdbmoviesChecker {
	host := strings.TrimSpace(cfg.Online.VDBmovies.Host)
	if host == "" {
		host = "https://cdnmovies-stream.online"
	}
	if host != "" && !strings.Contains(host, "://") {
		host = "https://" + host
	}
	host = strings.TrimRight(host, "/")

	return &vdbmoviesChecker{
		client: httpclient.NewForBalancer("vdbmovies", 10*time.Second),
		host:   host,
	}
}

func (v *vdbmoviesChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show := v.checkSearch(req)
			writeCheckSearchResponseNoRCH(w, show, pluginQualityBadgeGet("vdbmovies"))
			return
		}

		v.index(w, req, links)
	}
}

func (v *vdbmoviesChecker) checkSearch(req *http.Request) bool {
	q := req.URL.Query()
	orid := strings.TrimSpace(q.Get("orid"))
	kinopoiskID := strings.TrimSpace(q.Get("kinopoisk_id"))

	var target string
	switch {
	case orid != "":
		if !isInt64(orid) {
			return false
		}
		target = v.host + "/content/" + url.PathEscape(orid) + "/iframe"
	case kinopoiskID != "":
		if !isInt64(kinopoiskID) {
			return false
		}
		target = v.host + "/kinopoisk/" + url.PathEscape(kinopoiskID) + "/iframe"
	default:
		return false
	}

	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, target, nil)
	if err != nil {
		return false
	}
	httpReq.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	httpReq.Header.Set("Referer", "https://movieboom.store/")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	httpReq.Header.Set("X-Lampac-Go", "1")

	resp, err := v.client.Do(httpReq)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return false
	}
	html := string(body)

	if vdbmoviesFileRe.MatchString(html) || vdbmoviesFallbackFileRe.MatchString(html) {
		return true
	}
	if strings.Contains(html, "makePlayer(") && strings.Contains(html, "file:") {
		return true
	}
	return false
}

func isInt64(v string) bool {
	_, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	return err == nil
}

func (v *vdbmoviesChecker) index(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	similar := parseBoolParam(q.Get("similar"))
	orid := strings.TrimSpace(q.Get("orid"))
	imdbID := strings.TrimSpace(q.Get("imdb_id"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	t := strings.TrimSpace(q.Get("t"))

	kinopoiskID, _ := strconv.ParseInt(strings.TrimSpace(q.Get("kinopoisk_id")), 10, 64)
	s, sOK := getsTVQueryInt(q.Get("s"))
	if !sOK {
		s = -1
	}
	sid, sidOK := getsTVQueryInt(q.Get("sid"))
	if !sidOK {
		sid = -1
	}

	if similar {
		writeGetsTVEmpty(w, rjson)
		return
	}
	if orid == "" && kinopoiskID == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	embed, ok := v.fetchEmbed(req, orid, kinopoiskID)
	if !ok {
		writeGetsTVEmpty(w, rjson)
		return
	}
	if len(embed.Movies) == 0 && len(embed.Serial) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(req)
	if len(embed.Movies) > 0 {
		v.writeMovie(w, req, rjson, embed.Movies, links)
		return
	}

	v.writeSerial(w, req, rjson, host, embed, orid, imdbID, kinopoiskID, title, originalTitle, s, sid, t, links)
}

func (v *vdbmoviesChecker) fetchEmbed(req *http.Request, orid string, kinopoiskID int64) (vdbmoviesEmbed, bool) {
	if v.host == "" {
		return vdbmoviesEmbed{}, false
	}

	target := ""
	switch {
	case orid != "":
		target = v.host + "/content/" + url.PathEscape(orid) + "/iframe"
	case kinopoiskID > 0:
		target = v.host + "/kinopoisk/" + strconv.FormatInt(kinopoiskID, 10) + "/iframe"
	default:
		return vdbmoviesEmbed{}, false
	}

	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, target, nil)
	if err != nil {
		return vdbmoviesEmbed{}, false
	}
	httpReq.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	httpReq.Header.Set("Referer", "https://movieboom.store/")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	httpReq.Header.Set("X-Lampac-Go", "1")

	resp, err := v.client.Do(httpReq)
	if err != nil {
		return vdbmoviesEmbed{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return vdbmoviesEmbed{}, false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return vdbmoviesEmbed{}, false
	}
	htmlBody := string(body)

	file := submatch1(vdbmoviesFileRe, htmlBody)
	if file == "" {
		file = submatch1(vdbmoviesFallbackFileRe, htmlBody)
	}
	if file == "" {
		return vdbmoviesEmbed{}, false
	}

	decoded := vdbmoviesDecodeFile(file)
	if decoded == "" {
		return vdbmoviesEmbed{}, false
	}

	forbidden := submatch1(vdbmoviesForbiddenQuality, htmlBody)
	defaultQuality := submatch1(vdbmoviesDefaultQuality, htmlBody)
	return vdbmoviesParseEmbed(decoded, forbidden, defaultQuality)
}

func (v *vdbmoviesChecker) writeMovie(w http.ResponseWriter, req *http.Request, rjson bool, movies []vdbmoviesMovie, links *proxylink.Manager) {
	rows := make([]map[string]any, 0, len(movies))
	labels := make([]string, 0, len(movies))

	for i, movie := range movies {
		file := strings.TrimSpace(movie.File)
		if file == "" {
			continue
		}

		streams := vdbmoviesBuildStreamQuality(file)
		if len(streams) == 0 {
			continue
		}

		for _, sq := range streams {
			sq["url"] = streamProxyURL(req, sq["url"], "vdbmovies", links)
		}

		title := strings.TrimSpace(movie.Title)
		if title == "" {
			title = "Плеер " + strconv.Itoa(i+1)
		}

		row := map[string]any{
			"method":        "call",
			"url":           streams[0]["url"],
			"stream":        streams[0]["url"],
			"translate":     title,
			"streamquality": streams,
		}

		subtitles := vdbmoviesParseSubtitles(strings.TrimSpace(movie.Subtitle))
		if len(subtitles) == 0 {
			subtitles = vdbmoviesParseSubtitles(strings.TrimSpace(movie.Subtitles))
		}
		if len(subtitles) > 0 {
			row["subtitles"] = subtitles
		}

		rows = append(rows, row)
		labels = append(labels, title)
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
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, 0, 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (v *vdbmoviesChecker) writeSerial(
	w http.ResponseWriter,
	req *http.Request,
	rjson bool,
	host string,
	embed vdbmoviesEmbed,
	orid string,
	imdbID string,
	kinopoiskID int64,
	title string,
	originalTitle string,
	s int,
	sid int,
	t string,
	links *proxylink.Manager,
) {
	if s == -1 {
		rows := make([]map[string]any, 0, len(embed.Serial))
		labels := make([]string, 0, len(embed.Serial))
		for i, season := range embed.Serial {
			seasonNum, ok := vdbmoviesNumber(vdbmoviesSeasonNumberRe, season.Title)
			if !ok {
				continue
			}

			link := host + "/lite/vdbmovies?orid=" + url.QueryEscape(orid) +
				"&imdb_id=" + url.QueryEscape(imdbID) +
				"&kinopoisk_id=" + strconv.FormatInt(kinopoiskID, 10) +
				"&rjson=" + getsTVBool(rjson) +
				"&title=" + url.QueryEscape(title) +
				"&original_title=" + url.QueryEscape(originalTitle) +
				"&s=" + strconv.Itoa(seasonNum) +
				"&sid=" + strconv.Itoa(i)

			seasonTitle := strconv.Itoa(seasonNum) + " сезон"
			rows = append(rows, map[string]any{
				"method":  "link",
				"id":      seasonNum,
				"url":     link,
				"name":    seasonTitle,
				"quality": embed.Quality,
			})
			labels = append(labels, seasonTitle)
		}

		if len(rows) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
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

	if sid < 0 || sid >= len(embed.Serial) || !vdbmoviesSeasonMatches(embed.Serial[sid], s) {
		sid = -1
		for i, season := range embed.Serial {
			if vdbmoviesSeasonMatches(season, s) {
				sid = i
				break
			}
		}
	}
	if sid < 0 || sid >= len(embed.Serial) {
		writeGetsTVEmpty(w, rjson)
		return
	}

	seasonData := embed.Serial[sid]
	voiceRows := make([]map[string]any, 0, 8)
	seenVoice := make(map[string]struct{}, 8)
	for _, episode := range seasonData.Folder {
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

			link := host + "/lite/vdbmovies?orid=" + url.QueryEscape(orid) +
				"&imdb_id=" + url.QueryEscape(imdbID) +
				"&kinopoisk_id=" + strconv.FormatInt(kinopoiskID, 10) +
				"&rjson=" + getsTVBool(rjson) +
				"&title=" + url.QueryEscape(title) +
				"&original_title=" + url.QueryEscape(originalTitle) +
				"&s=" + strconv.Itoa(s) +
				"&sid=" + strconv.Itoa(sid) +
				"&t=" + url.QueryEscape(name)

			voiceRows = append(voiceRows, map[string]any{
				"method": "link",
				"name":   name,
				"active": t == name,
				"url":    link,
			})
		}
	}

	type episodeRow struct {
		data      map[string]any
		label     string
		seasonNum int
		episode   int
	}

	rows := make([]episodeRow, 0, len(seasonData.Folder))
	baseTitle := getsTVJoinName(title, originalTitle)
	for _, episode := range seasonData.Folder {
		episodeName, episodeNum := vdbmoviesEpisodeLabel(episode.Title)
		if episodeName == "" {
			continue
		}

		for _, voice := range episode.Folder {
			if strings.TrimSpace(voice.Title) != t {
				continue
			}
			streams := vdbmoviesBuildStreamQuality(voice.File)
			if len(streams) == 0 {
				continue
			}
			for _, sq := range streams {
				sq["url"] = streamProxyURL(req, sq["url"], "vdbmovies", links)
			}
			rows = append(rows, episodeRow{
				data: map[string]any{
					"method":        "call",
					"url":           streams[0]["url"],
					"stream":        streams[0]["url"],
					"s":             s,
					"e":             episodeNum,
					"name":          episodeName,
					"title":         fmt.Sprintf("%s (%s)", baseTitle, episodeName),
					"streamquality": streams,
				},
				label:     episodeName,
				seasonNum: s,
				episode:   episodeNum,
			})
			break
		}
	}

	if len(rows) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].episode == rows[j].episode {
			return rows[i].label < rows[j].label
		}
		return rows[i].episode < rows[j].episode
	})

	episodeData := make([]map[string]any, 0, len(rows))
	episodeLabels := make([]string, 0, len(rows))
	seasons := make([]int, 0, len(rows))
	episodes := make([]int, 0, len(rows))
	for _, row := range rows {
		episodeData = append(episodeData, row.data)
		episodeLabels = append(episodeLabels, row.label)
		seasons = append(seasons, row.seasonNum)
		episodes = append(episodes, row.episode)
	}

	if rjson {
		payload := map[string]any{
			"type": "episode",
			"data": episodeData,
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
	for i, row := range episodeData {
		getsTVAppendMovieHTML(&sb, row, episodeLabels[i], i == 0, seasons[i], episodes[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func vdbmoviesParseEmbed(decoded, forbiddenQuality, defaultQuality string) (vdbmoviesEmbed, bool) {
	defaultQuality = strings.TrimSpace(defaultQuality)
	if defaultQuality == "" {
		defaultQuality = "360p"
	}

	quality := defaultQuality
	switch {
	case strings.Contains(forbiddenQuality, "1080p"):
		quality = "1080p"
	case strings.Contains(forbiddenQuality, "720p"):
		quality = "720p"
	case strings.Contains(forbiddenQuality, "480p"):
		quality = "480p"
	}

	if strings.Contains(decoded, `"folder"`) {
		var serial []vdbmoviesVoice
		if err := stdjson.Unmarshal([]byte(decoded), &serial); err == nil && len(serial) > 0 {
			return vdbmoviesEmbed{Serial: serial, Quality: quality}, true
		}
	}

	var movies []vdbmoviesMovie
	if err := stdjson.Unmarshal([]byte(decoded), &movies); err == nil && len(movies) > 0 {
		return vdbmoviesEmbed{Movies: movies, Quality: quality}, true
	}

	return vdbmoviesEmbed{}, false
}

func vdbmoviesDecodeFile(raw string) string {
	value := strings.TrimSpace(raw)
	value = strings.TrimPrefix(value, "#.")
	if value == "" {
		return ""
	}

	for _, trash := range vdbmoviesTrashEncoded() {
		if strings.Contains(value, trash) {
			value = strings.ReplaceAll(value, trash, "")
		}
	}

	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return value
	}
	return string(decoded)
}

func vdbmoviesTrashEncoded() []string {
	vdbmoviesTrashEncodedOnce.Do(func() {
		trashList := []string{
			"wNp2wBTNcPRQvTC0_CpxCsq_8T1u9Q",
			"md-Od2G9RWOgSa5HoBSSbWrCyIqQyY",
			"kzuOYQqB_QSOL-xzN_Kz3kkgkHhHit",
			"6-xQWMh7ertLp8t_M9huUDk1M0VrYJ",
			"RyTwtf15_GLEsXxnpU4Ljjd0ReY-VH",
		}
		vdbmoviesTrashEncodedCache = make([]string, 0, len(trashList))
		for _, trash := range trashList {
			vdbmoviesTrashEncodedCache = append(vdbmoviesTrashEncodedCache, "//"+base64.StdEncoding.EncodeToString([]byte(trash)))
		}
	})
	return vdbmoviesTrashEncodedCache
}

func vdbmoviesBuildStreamQuality(file string) []map[string]string {
	file = strings.TrimSpace(file)
	if file == "" {
		return nil
	}

	out := make([]map[string]string, 0, 6)
	target := vdbmoviesStreamQualityRe.FindStringSubmatch(file)
	if len(target) == 3 {
		targetQuality := strings.TrimSpace(strings.TrimSuffix(target[1], "p"))
		targetLink := strings.TrimSpace(target[2])
		if targetQuality != "" && targetLink != "" {
			for _, q := range []string{"1080", "720", "480", "360", "240"} {
				link := strings.Replace(targetLink, "/"+targetQuality+".mp4:", "/"+q+".mp4:", 1)
				link = strings.ReplaceAll(link, ":hls:manifest.m3u8", "")
				if link == "" {
					continue
				}
				out = append(out, map[string]string{
					"url":     link,
					"quality": q + "p",
				})
			}
		}
	}

	if len(out) == 0 {
		for _, match := range vdbmoviesStreamQualityRe.FindAllStringSubmatch(file, -1) {
			if len(match) != 3 {
				continue
			}
			link := strings.ReplaceAll(strings.TrimSpace(match[2]), ":hls:manifest.m3u8", "")
			if link == "" {
				continue
			}
			out = append(out, map[string]string{
				"url":     link,
				"quality": strings.TrimSpace(match[1]),
			})
		}
	}

	return out
}

func vdbmoviesParseSubtitles(raw string) []map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	out := make([]map[string]string, 0, 2)
	for _, match := range vdbmoviesSubtitleRe.FindAllStringSubmatch(raw, -1) {
		if len(match) != 3 {
			continue
		}
		link := strings.TrimSpace(match[2])
		if link == "" {
			continue
		}
		if strings.HasPrefix(link, "//") {
			link = "https:" + link
		}
		out = append(out, map[string]string{
			"label": html.EscapeString(strings.TrimSpace(match[1])),
			"url":   link,
		})
	}
	return out
}

func vdbmoviesNumber(re *regexp.Regexp, title string) (int, bool) {
	m := re.FindStringSubmatch(strings.TrimSpace(title))
	if len(m) != 2 {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

func vdbmoviesSeasonMatches(season vdbmoviesVoice, wantSeason int) bool {
	n, ok := vdbmoviesNumber(vdbmoviesSeasonNumberRe, season.Title)
	return ok && n == wantSeason
}

func vdbmoviesEpisodeLabel(raw string) (string, int) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", 0
	}
	n, ok := vdbmoviesNumber(vdbmoviesEpisodeNumberRe, raw)
	if ok {
		return strconv.Itoa(n) + " серия", n
	}
	return raw, 0
}
