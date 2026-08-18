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
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/config"
)

var (
	iframeVideoVoiceRe         = regexp.MustCompile(`<a href=['"]/[^/]+/([^/]+)/iframe[^'"]*['"][^>]*>\s*<span[^>]*>([^<]+)</span>`)
	iframeVideoFallbackVoiceRe = regexp.MustCompile(`<span class='muted'><span [^>]+>([^<]+)</span>`)
	iframeVideoFallbackVoice2  = regexp.MustCompile(`<span class='muted'>([^<\n\r]+)`)
	iframeVideoTokenPathRe     = regexp.MustCompile(`/[^/]+/([^/]+)/iframe`)
	iframeVideoSrcRe           = regexp.MustCompile(`"src":"([^"]+)"`)
)

type iframevideoChecker struct {
	client  *http.Client
	apiHost string
	cdnHost string
	token   string
}

type iframevideoFrame struct {
	Content string
	Type    string
	CID     int
	Path    string
}

func NewIframevideoChecker(cfg config.Config) *iframevideoChecker {
	apiHost := decodeMaybeEncryptedHost(strings.TrimSpace(cfg.Online.IframeVideo.APIHost))
	if apiHost == "" {
		apiHost = "https://iframe.video"
	}
	if !strings.Contains(apiHost, "://") {
		apiHost = "https://" + apiHost
	}
	apiHost = strings.TrimRight(apiHost, "/")

	cdnHost := decodeMaybeEncryptedHost(strings.TrimSpace(cfg.Online.IframeVideo.CDNHost))
	if cdnHost == "" {
		cdnHost = "https://videoframe.space"
	}
	if !strings.Contains(cdnHost, "://") {
		cdnHost = "https://" + cdnHost
	}
	cdnHost = strings.TrimRight(cdnHost, "/")

	return &iframevideoChecker{
		client:  httpclient.NewForBalancer("iframevideo", 12*time.Second),
		apiHost: apiHost,
		cdnHost: cdnHost,
		token:   strings.TrimSpace(cfg.Online.IframeVideo.Token),
	}
}

func decodeMaybeEncryptedHost(v string) string {
	v = strings.TrimSpace(strings.TrimPrefix(v, "encrypt:"))
	if v == "" {
		return ""
	}
	if strings.Contains(v, "://") {
		return v
	}

	runes := []rune(v)
	for i := range runes {
		runes[i] = runes[i] - 3
	}
	decoded := string(runes)
	if strings.Contains(decoded, "://") {
		return decoded
	}
	return v
}

func (i *iframevideoChecker) Handle(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		raw := strings.Trim(strings.TrimPrefix(strings.ToLower(req.URL.Path), "/lite/"), "/")

		switch raw {
		case "iframevideo":
			if parseBoolParam(req.URL.Query().Get("checksearch")) {
				show := i.checkSearch(req)
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusOK)
				if show {
					_, _ = w.Write([]byte(`{"type":"movie","rch":false}`))
					return
				}
				_, _ = w.Write([]byte(`{"rch":false}`))
				return
			}

			i.index(w, req)
			return

		case "iframevideo/video", "iframevideo/video.m3u8":
			i.video(w, req, raw == "iframevideo/video.m3u8")
			return
		}

		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error":    "iframevideo route is not implemented in local mode",
			"balanser": raw,
		})
	}
}

func (i *iframevideoChecker) checkSearch(req *http.Request) bool {
	imdbID := strings.TrimSpace(req.URL.Query().Get("imdb_id"))
	kinopoiskID, _ := strconv.ParseInt(strings.TrimSpace(req.URL.Query().Get("kinopoisk_id")), 10, 64)

	if imdbID == "" && kinopoiskID == 0 {
		if i.probe(req, http.MethodHead, i.apiHost) {
			return true
		}
		return i.probe(req, http.MethodGet, i.apiHost)
	}

	frame, ok := i.fetchFrame(req, imdbID, kinopoiskID)
	if !ok {
		if i.probe(req, http.MethodHead, i.apiHost) {
			return true
		}
		return i.probe(req, http.MethodGet, i.apiHost)
	}
	t := strings.ToLower(strings.TrimSpace(frame.Type))
	return t == "movie" || t == "anime"
}

func (i *iframevideoChecker) index(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	imdbID := strings.TrimSpace(q.Get("imdb_id"))
	kinopoiskID, _ := strconv.ParseInt(strings.TrimSpace(q.Get("kinopoisk_id")), 10, 64)
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))

	frame, ok := i.fetchFrame(req, imdbID, kinopoiskID)
	if !ok {
		writeGetsTVEmpty(w, rjson)
		return
	}

	t := strings.ToLower(strings.TrimSpace(frame.Type))
	if t != "movie" && t != "anime" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	rows := i.parseVoiceRows(req, frame, title, originalTitle)
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
	for idx, row := range rows {
		name := strings.TrimSpace(fmt.Sprint(row["name"]))
		if name == "" {
			name = "По умолчанию"
		}
		getsTVAppendMovieHTML(&sb, row, name, idx == 0, 0, 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (i *iframevideoChecker) parseVoiceRows(req *http.Request, frame iframevideoFrame, title, originalTitle string) []map[string]any {
	host := hostFromRequest(req)
	baseTitle := getsTVJoinName(title, originalTitle)

	buildRow := func(token, voice string) map[string]any {
		query := "title=" + url.QueryEscape(title) +
			"&original_title=" + url.QueryEscape(originalTitle) +
			"&type=" + url.QueryEscape(frame.Type) +
			"&cid=" + strconv.Itoa(frame.CID) +
			"&token=" + url.QueryEscape(token)

		urlJSON := host + "/lite/iframevideo/video?" + query
		stream := host + "/lite/iframevideo/video.m3u8?" + query + "&play=true"

		return map[string]any{
			"method": "call",
			"url":    urlJSON,
			"stream": stream,
			"name":   voice,
			"title":  baseTitle + " (" + voice + ")",
		}
	}

	rows := make([]map[string]any, 0, 4)
	for _, m := range iframeVideoVoiceRe.FindAllStringSubmatch(frame.Content, -1) {
		if len(m) != 3 {
			continue
		}
		token := strings.TrimSpace(m[1])
		voice := strings.TrimSpace(html.UnescapeString(m[2]))
		if token == "" {
			continue
		}
		if voice == "" {
			voice = "По умолчанию"
		}
		rows = append(rows, buildRow(token, voice))
	}
	if len(rows) > 0 {
		return rows
	}

	token := strings.TrimSpace(submatch1(iframeVideoTokenPathRe, frame.Path))
	if token == "" {
		return nil
	}
	voice := strings.TrimSpace(submatch1(iframeVideoFallbackVoiceRe, frame.Content))
	if voice == "" {
		voice = strings.TrimSpace(submatch1(iframeVideoFallbackVoice2, frame.Content))
	}
	if voice == "" {
		voice = "По умолчанию"
	}
	rows = append(rows, buildRow(token, voice))
	return rows
}

func (i *iframevideoChecker) video(w http.ResponseWriter, req *http.Request, forcePlay bool) {
	q := req.URL.Query()
	kind := strings.TrimSpace(q.Get("type"))
	cid, _ := strconv.Atoi(strings.TrimSpace(q.Get("cid")))
	token := strings.TrimSpace(q.Get("token"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))

	if cid < 1 || token == "" {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	stream, ok := i.loadVideo(req, kind, cid, token)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	if forcePlay || parseBoolParam(q.Get("play")) {
		http.Redirect(w, req, stream, http.StatusFound)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"method": "play",
		"url":    stream,
		"title":  getsTVJoinName(title, originalTitle),
	})
}

func (i *iframevideoChecker) loadVideo(req *http.Request, kind string, cid int, token string) (string, bool) {
	if i.cdnHost == "" {
		return "", false
	}

	form := url.Values{}
	form.Set("token", token)
	form.Set("type", kind)
	form.Set("season", "")
	form.Set("episode", "")
	form.Set("mobile", "false")
	form.Set("id", strconv.Itoa(cid))
	form.Set("qt", "480")

	target := strings.TrimRight(i.cdnHost, "/") + "/loadvideo"
	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return "", false
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("DNT", "1")
	httpReq.Header.Set("Origin", strings.TrimRight(i.cdnHost, "/"))
	httpReq.Header.Set("Referer", strings.TrimRight(i.cdnHost, "/")+"/")
	httpReq.Header.Set("Sec-Fetch-Dest", "empty")
	httpReq.Header.Set("Sec-Fetch-Mode", "cors")
	httpReq.Header.Set("Sec-Fetch-Site", "same-origin")
	httpReq.Header.Set("X-REF", strings.TrimRight(i.apiHost, "/")+"/")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	httpReq.Header.Set("X-Lampac-Go", "1")

	resp, err := i.client.Do(httpReq)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", false
	}

	var parsed struct {
		Src string `json:"src"`
	}
	if err := stdjson.Unmarshal(body, &parsed); err == nil && strings.TrimSpace(parsed.Src) != "" {
		src := strings.TrimSpace(strings.ReplaceAll(parsed.Src, "\\", ""))
		if strings.HasPrefix(src, "//") {
			src = "https:" + src
		}
		return src, true
	}

	src := strings.TrimSpace(submatch1(iframeVideoSrcRe, string(body)))
	if src == "" {
		return "", false
	}
	src = strings.ReplaceAll(src, "\\", "")
	if strings.HasPrefix(src, "//") {
		src = "https:" + src
	}
	return src, true
}

func (i *iframevideoChecker) fetchFrame(req *http.Request, imdbID string, kinopoiskID int64) (iframevideoFrame, bool) {
	if kinopoiskID == 0 && strings.TrimSpace(imdbID) == "" {
		return iframevideoFrame{}, false
	}
	if i.apiHost == "" {
		return iframevideoFrame{}, false
	}

	target := strings.TrimRight(i.apiHost, "/") + "/api/v2/search?imdb=" + url.QueryEscape(strings.TrimSpace(imdbID)) + "&kp=" + strconv.FormatInt(kinopoiskID, 10)
	if strings.TrimSpace(i.token) != "" {
		target = strings.TrimRight(i.apiHost, "/") + "/api/v2/movies?kp=" + strconv.FormatInt(kinopoiskID, 10) +
			"&imdb=" + url.QueryEscape(strings.TrimSpace(imdbID)) +
			"&api_token=" + url.QueryEscape(strings.TrimSpace(i.token))
	}

	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, target, nil)
	if err != nil {
		return iframevideoFrame{}, false
	}
	httpReq.Header.Set("Accept", "application/json,text/plain,*/*")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	httpReq.Header.Set("X-Lampac-Go", "1")

	resp, err := i.client.Do(httpReq)
	if err != nil {
		return iframevideoFrame{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return iframevideoFrame{}, false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return iframevideoFrame{}, false
	}

	var root struct {
		Results []struct {
			CID  int    `json:"cid"`
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"results"`
	}
	if err := stdjson.Unmarshal(body, &root); err != nil {
		return iframevideoFrame{}, false
	}
	if len(root.Results) == 0 {
		return iframevideoFrame{}, false
	}

	item := root.Results[0]
	if item.CID < 1 || strings.TrimSpace(item.Path) == "" {
		return iframevideoFrame{}, false
	}
	path := strings.TrimSpace(item.Path)
	if strings.HasPrefix(path, "//") {
		path = "https:" + path
	}

	pageReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, path, nil)
	if err != nil {
		return iframevideoFrame{}, false
	}
	pageReq.Header.Set("DNT", "1")
	pageReq.Header.Set("Referer", strings.TrimRight(i.apiHost, "/")+"/")
	pageReq.Header.Set("Sec-Fetch-Dest", "iframe")
	pageReq.Header.Set("Sec-Fetch-Mode", "navigate")
	pageReq.Header.Set("Sec-Fetch-Site", "cross-site")
	pageReq.Header.Set("Upgrade-Insecure-Requests", "1")
	pageReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	pageReq.Header.Set("X-Lampac-Go", "1")

	pageResp, err := i.client.Do(pageReq)
	if err != nil {
		return iframevideoFrame{}, false
	}
	defer pageResp.Body.Close()
	if pageResp.StatusCode < 200 || pageResp.StatusCode >= 300 {
		return iframevideoFrame{}, false
	}

	content, err := io.ReadAll(io.LimitReader(pageResp.Body, 2<<20))
	if err != nil {
		return iframevideoFrame{}, false
	}

	return iframevideoFrame{
		Content: string(content),
		Type:    strings.TrimSpace(item.Type),
		CID:     item.CID,
		Path:    path,
	}, true
}

func (i *iframevideoChecker) probe(req *http.Request, method, target string) bool {
	httpReq, err := http.NewRequestWithContext(req.Context(), method, target, nil)
	if err != nil {
		return false
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	httpReq.Header.Set("Accept", "text/html,application/json,*/*")
	httpReq.Header.Set("X-Lampac-Go", "1")

	resp, err := i.client.Do(httpReq)
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
