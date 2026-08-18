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
)

var cdnvideohubHLSRe = regexp.MustCompile(`"hlsUrl":"([^"]+)"`)

type cdnvideohubChecker struct {
	client *http.Client
	host   string
}

type cdnvideohubPlaylist struct {
	IsSerial bool               `json:"isSerial"`
	Items    []cdnvideohubEntry `json:"items"`
}

type cdnvideohubEntry struct {
	Season      int    `json:"season"`
	VoiceStudio string `json:"voiceStudio"`
	VoiceType   string `json:"voiceType"`
	Episode     int    `json:"episode"`
	VKID        string `json:"vkId"`
}

func NewCDNvideohubChecker(cfg config.Config) *cdnvideohubChecker {
	host := strings.TrimSpace(strings.TrimRight(cfg.Online.CDNvideohub.Host, "/"))
	if host == "" {
		host = "https://plapi.cdnvideohub.com"
	}
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}

	return &cdnvideohubChecker{
		client: httpclient.NewForBalancer("cdnvideohub", 12*time.Second),
		host:   host,
	}
}

func (c *cdnvideohubChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := strings.Trim(strings.TrimPrefix(strings.ToLower(r.URL.Path), "/lite/"), "/")

		switch raw {
		case "cdnvideohub":
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
			return

		case "cdnvideohub/video", "cdnvideohub/video.m3u8":
			c.video(w, r, links)
			return
		}

		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error":    "cdnvideohub route is not implemented in local mode",
			"balanser": raw,
		})
	}
}

func (c *cdnvideohubChecker) checkSearch(r *http.Request) bool {
	if c.host == "" {
		return false
	}

	kinopoiskID, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("kinopoisk_id")), 10, 64)
	if kinopoiskID > 0 {
		root, ok := c.fetchPlaylist(r.Context(), kinopoiskID)
		if ok && len(root.Items) > 0 {
			return true
		}
	}

	return c.probe(r.Context(), http.MethodHead, c.host) || c.probe(r.Context(), http.MethodGet, c.host)
}

func (c *cdnvideohubChecker) index(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	kinopoiskID, _ := strconv.ParseInt(strings.TrimSpace(q.Get("kinopoisk_id")), 10, 64)
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	voice := strings.TrimSpace(q.Get("t"))
	season, seasonSet := getsTVQueryInt(q.Get("s"))
	if !seasonSet {
		season = -1
	}

	if kinopoiskID <= 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	root, ok := c.fetchPlaylist(r.Context(), kinopoiskID)
	if !ok || len(root.Items) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(r)
	if root.IsSerial {
		c.writeSerial(w, rjson, host, kinopoiskID, title, originalTitle, season, voice, root.Items)
		return
	}
	c.writeMovie(w, rjson, host, title, originalTitle, root.Items)
}

func (c *cdnvideohubChecker) writeMovie(w http.ResponseWriter, rjson bool, host, title, originalTitle string, items []cdnvideohubEntry) {
	rows := make([]map[string]any, 0, len(items))
	labels := make([]string, 0, len(items))
	baseTitle := getsTVJoinName(title, originalTitle)

	for _, item := range items {
		vkID := strings.TrimSpace(item.VKID)
		if vkID == "" {
			continue
		}

		voice := strings.TrimSpace(item.VoiceStudio)
		if voice == "" {
			voice = strings.TrimSpace(item.VoiceType)
		}
		if voice == "" {
			voice = "По умолчанию"
		}

		link := host + "/lite/cdnvideohub/video.m3u8?vkId=" + url.QueryEscape(vkID) + "&title=" + url.QueryEscape(title)
		stream := link + "&play=true"
		rows = append(rows, map[string]any{
			"method": "call",
			"url":    link,
			"stream": stream,
			"name":   voice,
			"title":  baseTitle + " (" + voice + ")",
		})
		labels = append(labels, voice)
	}

	cdnvideohubWriteMovie(w, rjson, rows, labels)
}

func (c *cdnvideohubChecker) writeSerial(
	w http.ResponseWriter,
	rjson bool,
	host string,
	kinopoiskID int64,
	title string,
	originalTitle string,
	season int,
	voice string,
	items []cdnvideohubEntry,
) {
	defaultArgs := "&rjson=" + getsTVBool(rjson) +
		"&kinopoisk_id=" + strconv.FormatInt(kinopoiskID, 10) +
		"&title=" + url.QueryEscape(title) +
		"&original_title=" + url.QueryEscape(originalTitle)

	if season == -1 {
		uniq := make(map[int]struct{}, 8)
		seasons := make([]int, 0, 8)
		for _, item := range items {
			if item.Season < 1 {
				continue
			}
			if _, ok := uniq[item.Season]; ok {
				continue
			}
			uniq[item.Season] = struct{}{}
			seasons = append(seasons, item.Season)
		}
		sort.Ints(seasons)

		data := make([]map[string]any, 0, len(seasons))
		labels := make([]string, 0, len(seasons))
		for _, s := range seasons {
			name := strconv.Itoa(s) + " сезон"
			data = append(data, map[string]any{
				"method": "link",
				"id":     s,
				"url":    host + "/lite/cdnvideohub?s=" + strconv.Itoa(s) + defaultArgs,
				"name":   name,
			})
			labels = append(labels, name)
		}

		cdnvideohubWriteSeason(w, rjson, data, labels)
		return
	}

	voiceSet := make(map[string]struct{}, 8)
	voiceOrder := make([]string, 0, 8)
	for _, item := range items {
		if item.Season != season {
			continue
		}
		name := strings.TrimSpace(item.VoiceStudio)
		if name == "" {
			name = strings.TrimSpace(item.VoiceType)
		}
		if name == "" {
			name = "По умолчанию"
		}
		if _, ok := voiceSet[name]; ok {
			continue
		}
		voiceSet[name] = struct{}{}
		voiceOrder = append(voiceOrder, name)
	}
	if len(voiceOrder) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}
	if voice == "" {
		voice = voiceOrder[0]
	}
	if _, ok := voiceSet[voice]; !ok {
		voice = voiceOrder[0]
	}

	voiceRows := make([]map[string]any, 0, len(voiceOrder))
	for _, rowVoice := range voiceOrder {
		voiceRows = append(voiceRows, map[string]any{
			"method": "link",
			"name":   rowVoice,
			"active": rowVoice == voice,
			"url": host + "/lite/cdnvideohub?s=" + strconv.Itoa(season) +
				"&t=" + url.QueryEscape(rowVoice) + defaultArgs,
		})
	}

	episodeMap := make(map[int]string, 32)
	episodeList := make([]int, 0, 32)
	for _, item := range items {
		if item.Season != season {
			continue
		}
		rowVoice := strings.TrimSpace(item.VoiceStudio)
		if rowVoice == "" {
			rowVoice = strings.TrimSpace(item.VoiceType)
		}
		if rowVoice == "" {
			rowVoice = "По умолчанию"
		}
		if rowVoice != voice {
			continue
		}
		vkID := strings.TrimSpace(item.VKID)
		if vkID == "" {
			continue
		}
		if _, exists := episodeMap[item.Episode]; exists {
			continue
		}
		episodeMap[item.Episode] = vkID
		episodeList = append(episodeList, item.Episode)
	}
	sort.Ints(episodeList)

	baseTitle := getsTVJoinName(title, originalTitle)
	data := make([]map[string]any, 0, len(episodeList))
	labels := make([]string, 0, len(episodeList))
	seasons := make([]int, 0, len(episodeList))
	episodes := make([]int, 0, len(episodeList))
	for _, episode := range episodeList {
		vkID := episodeMap[episode]
		link := host + "/lite/cdnvideohub/video.m3u8?vkId=" + url.QueryEscape(vkID) + "&title=" + url.QueryEscape(title)
		stream := link + "&play=true"

		name := "Серия"
		titleLabel := baseTitle
		if episode > 0 {
			name = strconv.Itoa(episode) + " серия"
			titleLabel = fmt.Sprintf("%s (%d серия)", baseTitle, episode)
		}

		data = append(data, map[string]any{
			"method": "call",
			"url":    link,
			"stream": stream,
			"s":      season,
			"e":      episode,
			"name":   name,
			"title":  titleLabel,
		})
		labels = append(labels, name)
		seasons = append(seasons, season)
		episodes = append(episodes, episode)
	}

	cdnvideohubWriteEpisode(w, rjson, voiceRows, data, labels, seasons, episodes)
}

func (c *cdnvideohubChecker) video(w http.ResponseWriter, r *http.Request, links *proxylink.Manager) {
	vkID := strings.TrimSpace(r.URL.Query().Get("vkId"))
	if vkID == "" {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	link, ok := c.fetchVideo(r.Context(), vkID)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	// Proxy HLS through /proxy/ — okcdn.ru binds URLs to server IP
	// and checks Origin header. Direct client access gets 400.
	proxied := streamProxyURLWithHeaders(r, link, "cdnvideohub", links, map[string]string{
		"Origin":  "https://cdnvideohub.com",
		"Referer": "https://cdnvideohub.com/",
	})

	if parseBoolParam(r.URL.Query().Get("play")) {
		http.Redirect(w, r, proxied, http.StatusFound)
		return
	}

	payload := map[string]any{
		"method": "play",
		"url":    proxied,
	}
	if title := strings.TrimSpace(r.URL.Query().Get("title")); title != "" {
		payload["title"] = title
	}
	writeJSON(w, http.StatusOK, payload)
}

func (c *cdnvideohubChecker) fetchPlaylist(ctx context.Context, kinopoiskID int64) (cdnvideohubPlaylist, bool) {
	var root cdnvideohubPlaylist
	if c.host == "" || kinopoiskID <= 0 {
		return root, false
	}

	target := strings.TrimRight(c.host, "/") + "/api/v1/player/sv/playlist?pub=12&aggr=kp&id=" + strconv.FormatInt(kinopoiskID, 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return root, false
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("X-Lampac-Go", "1")

	resp, err := c.client.Do(req)
	if err != nil {
		return root, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return root, false
	}

	dec := stdjson.NewDecoder(io.LimitReader(resp.Body, 8<<20))
	if err := dec.Decode(&root); err != nil {
		return root, false
	}
	if root.Items == nil {
		root.Items = []cdnvideohubEntry{}
	}
	return root, true
}

func (c *cdnvideohubChecker) fetchVideo(ctx context.Context, vkID string) (string, bool) {
	if c.host == "" || strings.TrimSpace(vkID) == "" {
		return "", false
	}

	target := strings.TrimRight(c.host, "/") + "/api/v1/player/sv/video/" + url.PathEscape(vkID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	req.Header.Set("X-Lampac-Go", "1")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", false
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", false
	}

	link := submatch1(cdnvideohubHLSRe, string(raw))
	if link == "" {
		var payload map[string]any
		if err := stdjson.Unmarshal(raw, &payload); err == nil {
			link = strings.TrimSpace(fmt.Sprint(payload["hlsUrl"]))
		}
	}
	if link == "" {
		return "", false
	}

	link = strings.ReplaceAll(link, `\u0026`, "&")
	link = strings.ReplaceAll(link, "u0026", "&")
	link = strings.ReplaceAll(link, `\\`, "")
	link = strings.ReplaceAll(link, `\/`, "/")
	link = strings.TrimSpace(link)
	return link, link != ""
}

func (c *cdnvideohubChecker) probe(ctx context.Context, method, target string) bool {
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

func cdnvideohubWriteMovie(w http.ResponseWriter, rjson bool, data []map[string]any, labels []string) {
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

func cdnvideohubWriteSeason(w http.ResponseWriter, rjson bool, data []map[string]any, labels []string) {
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

func cdnvideohubWriteEpisode(
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
