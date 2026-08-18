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
	cdnmoviesFileRe      = regexp.MustCompile(`(?s)file:\s*'([^']+)'`)
	cdnmoviesSeasonNumRe = regexp.MustCompile(`([0-9]+)$`)
	cdnmoviesEpisodeRe   = regexp.MustCompile(`([0-9]+)$`)
	cdnmoviesQualityRe   = regexp.MustCompile(`\[(360|240)p?\]([^\[\|,\n\r\t ]+\.(?:mp4|m3u8))`)
)

type cdnmoviesChecker struct {
	client *http.Client
	host   string
}

type cdnmoviesVoice struct {
	Title  string            `json:"title"`
	Folder []cdnmoviesSeason `json:"folder"`
}

type cdnmoviesSeason struct {
	Title  string             `json:"title"`
	Folder []cdnmoviesEpisode `json:"folder"`
}

type cdnmoviesEpisode struct {
	Title string `json:"title"`
	File  string `json:"file"`
}

func NewCDNmoviesChecker(cfg config.Config) *cdnmoviesChecker {
	host := strings.TrimSpace(strings.TrimRight(cfg.Online.CDNmovies.Host, "/"))
	if host == "" {
		host = "https://coldcdn.xyz"
	}
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}

	return &cdnmoviesChecker{
		client: httpclient.NewForBalancer("cdnmovies", 12*time.Second),
		host:   host,
	}
}

func (c *cdnmoviesChecker) Handle(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if parseBoolParam(r.URL.Query().Get("checksearch")) {
			show := c.checkSearch(r)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			if show {
				_, _ = w.Write([]byte(`{"type":"movie","rch":false}`))
				return
			}
			_, _ = w.Write([]byte(`{"rch":false}`))
			return
		}

		c.index(w, r)
	}
}

func (c *cdnmoviesChecker) checkSearch(r *http.Request) bool {
	if c.host == "" {
		return false
	}

	kinopoiskID, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("kinopoisk_id")), 10, 64)
	if kinopoiskID > 0 {
		voices, ok := c.fetchVoices(r.Context(), kinopoiskID)
		if ok && len(voices) > 0 {
			return true
		}
	}

	return c.probe(r.Context(), http.MethodHead, c.host) || c.probe(r.Context(), http.MethodGet, c.host)
}

func (c *cdnmoviesChecker) index(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	kinopoiskID, _ := strconv.ParseInt(strings.TrimSpace(q.Get("kinopoisk_id")), 10, 64)
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))

	t, tSet := getsTVQueryInt(q.Get("t"))
	if !tSet {
		t = 0
	}
	s, sSet := getsTVQueryInt(q.Get("s"))
	if !sSet {
		s = -1
	}
	sid, sidSet := getsTVQueryInt(q.Get("sid"))
	if !sidSet {
		sid = -1
	}

	if kinopoiskID <= 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	voices, ok := c.fetchVoices(r.Context(), kinopoiskID)
	if !ok || len(voices) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	if t < 0 || t >= len(voices) {
		t = 0
	}

	host := hostFromRequest(r)
	if s == -1 {
		rows := make([]map[string]any, 0, len(voices[t].Folder))
		labels := make([]string, 0, len(voices[t].Folder))
		for i, season := range voices[t].Folder {
			seasonNum, ok := cdnmoviesNumber(cdnmoviesSeasonNumRe, season.Title)
			if !ok {
				continue
			}

			link := host + "/lite/cdnmovies?rjson=" + getsTVBool(rjson) +
				"&kinopoisk_id=" + strconv.FormatInt(kinopoiskID, 10) +
				"&title=" + url.QueryEscape(title) +
				"&original_title=" + url.QueryEscape(originalTitle) +
				"&t=" + strconv.Itoa(t) +
				"&s=" + strconv.Itoa(seasonNum) +
				"&sid=" + strconv.Itoa(i)

			name := strconv.Itoa(seasonNum) + " сезон"
			rows = append(rows, map[string]any{
				"method": "link",
				"id":     seasonNum,
				"url":    link,
				"name":   name,
			})
			labels = append(labels, name)
		}
		cdnmoviesWriteSeason(w, rjson, rows, labels)
		return
	}

	if sid < 0 || sid >= len(voices[t].Folder) || !cdnmoviesSeasonMatches(voices[t].Folder[sid], s) {
		sid = -1
		for i, season := range voices[t].Folder {
			if cdnmoviesSeasonMatches(season, s) {
				sid = i
				break
			}
		}
	}
	if sid < 0 || sid >= len(voices[t].Folder) {
		writeGetsTVEmpty(w, rjson)
		return
	}

	voiceRows := make([]map[string]any, 0, len(voices))
	for idx, voice := range voices {
		name := strings.TrimSpace(voice.Title)
		if name == "" {
			name = "Voice " + strconv.Itoa(idx+1)
		}
		voiceRows = append(voiceRows, map[string]any{
			"method": "link",
			"name":   name,
			"active": idx == t,
			"url": host + "/lite/cdnmovies?rjson=" + getsTVBool(rjson) +
				"&kinopoisk_id=" + strconv.FormatInt(kinopoiskID, 10) +
				"&title=" + url.QueryEscape(title) +
				"&original_title=" + url.QueryEscape(originalTitle) +
				"&t=" + strconv.Itoa(idx) +
				"&s=" + strconv.Itoa(s) +
				"&sid=" + strconv.Itoa(sid),
		})
	}

	type episodeRow struct {
		data      map[string]any
		label     string
		seasonNum int
		episode   int
	}

	seasonData := voices[t].Folder[sid]
	rows := make([]episodeRow, 0, len(seasonData.Folder))
	baseTitle := getsTVJoinName(title, originalTitle)
	for _, episode := range seasonData.Folder {
		streams := cdnmoviesBuildStreamQuality(episode.File)
		if len(streams) == 0 {
			continue
		}

		epNum, _ := cdnmoviesNumber(cdnmoviesEpisodeRe, episode.Title)
		name := "Серия"
		if epNum > 0 {
			name = strconv.Itoa(epNum) + " серия"
		}

		rows = append(rows, episodeRow{
			data: map[string]any{
				"method":        "call",
				"url":           streams[0]["url"],
				"stream":        streams[0]["url"],
				"s":             s,
				"e":             epNum,
				"name":          name,
				"title":         fmt.Sprintf("%s (%s)", baseTitle, name),
				"streamquality": streams,
			},
			label:     name,
			seasonNum: s,
			episode:   epNum,
		})
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

	data := make([]map[string]any, 0, len(rows))
	labels := make([]string, 0, len(rows))
	seasons := make([]int, 0, len(rows))
	episodes := make([]int, 0, len(rows))
	for _, row := range rows {
		data = append(data, row.data)
		labels = append(labels, row.label)
		seasons = append(seasons, row.seasonNum)
		episodes = append(episodes, row.episode)
	}

	cdnmoviesWriteEpisode(w, rjson, voiceRows, data, labels, seasons, episodes)
}

func (c *cdnmoviesChecker) fetchVoices(ctx context.Context, kinopoiskID int64) ([]cdnmoviesVoice, bool) {
	if c.host == "" || kinopoiskID <= 0 {
		return nil, false
	}

	target := strings.TrimRight(c.host, "/") + "/serial/kinopoisk/" + strconv.FormatInt(kinopoiskID, 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("X-Lampac-Go", "1")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, false
	}

	encoded := submatch1(cdnmoviesFileRe, string(body))
	if encoded == "" {
		return nil, false
	}

	var voices []cdnmoviesVoice
	if err := stdjson.Unmarshal([]byte(encoded), &voices); err != nil {
		fixed := strings.ReplaceAll(encoded, `\"`, `"`)
		if err := stdjson.Unmarshal([]byte(fixed), &voices); err != nil {
			return nil, false
		}
	}
	return voices, len(voices) > 0
}

func (c *cdnmoviesChecker) probe(ctx context.Context, method, target string) bool {
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

func cdnmoviesNumber(re *regexp.Regexp, value string) (int, bool) {
	match := re.FindStringSubmatch(strings.TrimSpace(value))
	if len(match) < 2 {
		return 0, false
	}
	n, err := strconv.Atoi(match[1])
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

func cdnmoviesSeasonMatches(season cdnmoviesSeason, wanted int) bool {
	n, ok := cdnmoviesNumber(cdnmoviesSeasonNumRe, season.Title)
	return ok && n == wanted
}

func cdnmoviesBuildStreamQuality(file string) []map[string]any {
	matches := cdnmoviesQualityRe.FindAllStringSubmatch(file, -1)
	if len(matches) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(matches))
	out := make([]map[string]any, 0, len(matches))
	for _, m := range matches {
		if len(m) < 3 {
			continue
		}
		label := strings.TrimSpace(m[1]) + "p"
		link := strings.TrimSpace(m[2])
		if link == "" {
			continue
		}
		if _, ok := seen[label]; ok {
			continue
		}
		seen[label] = struct{}{}
		out = append(out, map[string]any{
			"quality": label,
			"url":     link,
		})
	}
	return out
}

func cdnmoviesWriteSeason(w http.ResponseWriter, rjson bool, data []map[string]any, labels []string) {
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

func cdnmoviesWriteEpisode(
	w http.ResponseWriter,
	rjson bool,
	voices []map[string]any,
	data []map[string]any,
	labels []string,
	seasons []int,
	episodes []int,
) {
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
