package litesrc

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/kit"
	"lampac-go/internal/proxylink"

	"github.com/rs/zerolog/log"
)

type vokinoChecker struct {
	client  *http.Client
	apiHost string
	token   string
}

// vokinoViewResponse represents the /v2/view response.
type vokinoViewResponse struct {
	Online map[string]vokinoOnlineEntry `json:"online"`
}

type vokinoOnlineEntry struct {
	PlaylistURL string `json:"playlist_url"`
}

// vokinoRoot is the response from /v2/online/{balancer}/{id}.
type vokinoRoot struct {
	Menu     []vokinoChannel `json:"menu"`
	Channels []vokinoChannel `json:"channels"`
}

type vokinoChannel struct {
	Title       string            `json:"title"`
	Ident       string            `json:"ident"`
	PlaylistURL string            `json:"playlist_url"`
	Selected    bool              `json:"selected"`
	Submenu     []vokinoChannel   `json:"submenu"`
	StreamURL   string            `json:"stream_url"`
	QualityFull string            `json:"quality_full"`
	Extra       map[string]string `json:"extra"`
}

var vokinoBalancerRe = regexp.MustCompile(`/v2/online/([^/]+)/`)
var vokinoSeasonNumRe = regexp.MustCompile(`^([0-9]+)`)
var vokinoSeasonNumEndRe = regexp.MustCompile(`([0-9]+)$`)
var vokinoEpisodeNumRe = regexp.MustCompile(`([0-9]+)$`)

func NewVokinoChecker(cfg config.Config) *vokinoChecker {
	host := strings.TrimSpace(cfg.Online.Vokino.Host)
	if host == "" {
		host = "http://api.vokino.org/"
	}
	host = strings.TrimRight(host, "/")

	return &vokinoChecker{
		client:  httpclient.New(15 * time.Second),
		apiHost: host,
		token:   strings.TrimSpace(cfg.Online.Vokino.Token),
	}
}

// tokenForCtx returns the per-user kit token if available, otherwise the global token.
func (v *vokinoChecker) tokenForCtx(ctx context.Context) string {
	if kitToken, ok := kit.TokenOverride(ctx, "VoKino"); ok {
		return kitToken
	}
	return v.token
}

func (v *vokinoChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			// Return stub to avoid 429 and extra view count
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("data-json="))
			return
		}

		v.index(w, req, links)
	}
}

func (v *vokinoChecker) index(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	kpID := strings.TrimSpace(q.Get("kinopoisk_id"))
	imdbID := strings.TrimSpace(q.Get("imdb_id"))
	origID := strings.TrimSpace(q.Get("origid"))
	balancer := strings.TrimSpace(q.Get("balancer"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	t := strings.TrimSpace(q.Get("t"))
	rjson := parseBoolParam(q.Get("rjson"))
	sParam := strings.TrimSpace(q.Get("s"))

	// source + id fallback for origid
	if origID == "" {
		source := strings.TrimSpace(q.Get("source"))
		id := strings.TrimSpace(q.Get("id"))
		if source != "" && id != "" && strings.Contains(strings.ToLower(source), "vokino") {
			origID = id
		}
	}

	if kpID == "" && origID == "" && imdbID == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if v.tokenForCtx(req.Context()) == "" {
		log.Warn().Msg("vokino: no token configured")
		writeGetsTVEmpty(w, rjson)
		return
	}

	contentID := origID
	if contentID == "" {
		contentID = kpID
	}
	if contentID == "" {
		contentID = imdbID
	}

	host := hostFromRequest(req)

	if balancer == "" {
		// Step 1: Get view — list of available balancers
		v.writeBalancerList(w, req, host, contentID, origID, kpID, title, originalTitle, rjson)
		return
	}

	// Step 2: Get online/{balancer}/{id} — channels
	v.writeChannels(w, req, host, contentID, origID, kpID, balancer, title, originalTitle, t, sParam, rjson, links)
}

func (v *vokinoChecker) writeBalancerList(w http.ResponseWriter, req *http.Request, host, contentID, origID, kpID, title, originalTitle string, rjson bool) {
	uri := fmt.Sprintf("%s/v2/view/%s?token=%s", v.apiHost, contentID, v.tokenForCtx(req.Context()))

	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, uri, nil)
	if err != nil {
		writeGetsTVEmpty(w, rjson)
		return
	}
	httpReq.Header.Set("User-Agent", "lampac")

	resp, err := v.client.Do(httpReq)
	if err != nil {
		log.Warn().Err(err).Msg("vokino: view request failed")
		writeGetsTVEmpty(w, rjson)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if strings.Contains(string(body), "not found") {
		writeGetsTVEmpty(w, rjson)
		return
	}

	var view vokinoViewResponse
	if err := stdjson.Unmarshal(body, &view); err != nil || len(view.Online) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Build similar list (balancer buttons)
	type similar struct {
		title    string
		balancer string
	}
	var similars []similar

	for name, entry := range view.Online {
		if entry.PlaylistURL == "" {
			continue
		}
		m := vokinoBalancerRe.FindStringSubmatch(entry.PlaylistURL)
		bal := ""
		if len(m) >= 2 {
			bal = m[1]
		}
		if bal == "" {
			bal = strings.ToLower(name)
		}
		s := similar{title: name, balancer: bal}
		if name == "Vokino" {
			similars = append([]similar{s}, similars...)
		} else {
			similars = append(similars, s)
		}
	}

	if len(similars) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	encTitle := url.QueryEscape(title)
	encOrigTitle := url.QueryEscape(originalTitle)

	rows := make([]map[string]any, 0, len(similars))
	for _, s := range similars {
		link := fmt.Sprintf("%s/lite/vokino?rjson=%v&origid=%s&kinopoisk_id=%s&title=%s&original_title=%s&balancer=%s",
			host, rjson, url.QueryEscape(origID), url.QueryEscape(kpID), encTitle, encOrigTitle, url.QueryEscape(s.balancer))
		rows = append(rows, map[string]any{
			"method":  "link",
			"url":     link,
			"similar": true,
			"title":   s.title,
		})
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
		label := fmt.Sprint(row["title"])
		getsTVAppendSeasonHTML(&sb, row, label, i == 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (v *vokinoChecker) writeChannels(w http.ResponseWriter, req *http.Request, host, contentID, origID, kpID, balancer, title, originalTitle, t, sParam string, rjson bool, links *proxylink.Manager) {
	uri := fmt.Sprintf("%s/v2/online/%s/%s?token=%s", v.apiHost, balancer, contentID, v.tokenForCtx(req.Context()))
	if t != "" {
		uri += "&" + t
	}

	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, uri, nil)
	if err != nil {
		writeGetsTVEmpty(w, rjson)
		return
	}
	httpReq.Header.Set("User-Agent", "lampac")

	resp, err := v.client.Do(httpReq)
	if err != nil {
		log.Warn().Err(err).Str("balancer", balancer).Msg("vokino: online request failed")
		writeGetsTVEmpty(w, rjson)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if strings.Contains(string(body), "not found") {
		writeGetsTVEmpty(w, rjson)
		return
	}

	var root vokinoRoot
	if err := stdjson.Unmarshal(body, &root); err != nil || len(root.Channels) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	encTitle := url.QueryEscape(title)
	encOrigTitle := url.QueryEscape(originalTitle)

	// Build voice template from menu
	var voices []vokinoChannel
	for _, m := range root.Menu {
		if m.Title == "Перевод" && len(m.Submenu) > 0 {
			voices = m.Submenu
			break
		}
	}

	s, _ := strconv.Atoi(sParam)

	// Check if serial (submenu) or movie
	if len(root.Channels) > 0 && root.Channels[0].PlaylistURL == "submenu" {
		// Serial mode
		if s <= 0 {
			// Season list
			qualityBadge := root.Channels[0].QualityFull
			qualityBadge = strings.ReplaceAll(qualityBadge, "2160p.", "4K ")

			rows := make([]map[string]any, 0, len(root.Channels))
			for _, ch := range root.Channels {
				sname := ""
				if m := vokinoSeasonNumRe.FindString(ch.Title); m != "" {
					sname = m
				}
				if sname == "" {
					if m := vokinoSeasonNumEndRe.FindString(ch.Title); m != "" {
						sname = m
					}
				}

				link := fmt.Sprintf("%s/lite/vokino?rjson=%v&origid=%s&kinopoisk_id=%s&balancer=%s&title=%s&original_title=%s&t=%s&s=%s",
					host, rjson, url.QueryEscape(origID), url.QueryEscape(kpID), url.QueryEscape(balancer), encTitle, encOrigTitle, url.QueryEscape(t), url.QueryEscape(sname))

				sn, _ := strconv.Atoi(sname)
				rows = append(rows, map[string]any{
					"method": "link",
					"url":    link,
					"title":  ch.Title,
					"s":      sn,
				})
			}

			if rjson {
				writeJSON(w, http.StatusOK, map[string]any{
					"type":    "season",
					"data":    rows,
					"quality": qualityBadge,
				})
				return
			}

			var sb strings.Builder
			sb.WriteString(`<div class="videos__line">`)
			for i, row := range rows {
				label := fmt.Sprint(row["title"])
				getsTVAppendSeasonHTML(&sb, row, label, i == 0)
			}
			sb.WriteString(`</div>`)
			writeHTML(w, http.StatusOK, sb.String())
			return
		}

		// Episode list
		// Find matching season
		var selectedSeason *vokinoChannel
		for i := range root.Channels {
			ch := &root.Channels[i]
			sname := vokinoSeasonNumRe.FindString(ch.Title)
			if sname == "" {
				sname = vokinoSeasonNumEndRe.FindString(ch.Title)
			}
			if sname == sParam || strings.HasPrefix(ch.Title, sParam+" ") || strings.HasSuffix(ch.Title, " "+sParam) {
				selectedSeason = ch
				break
			}
		}
		if selectedSeason == nil || len(selectedSeason.Submenu) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}

		rows := make([]map[string]any, 0, len(selectedSeason.Submenu))
		for _, ep := range selectedSeason.Submenu {
			streamURL := ep.StreamURL
			if streamURL == "" {
				continue
			}
			proxyURL := streamProxyURL(req, streamURL, "vokinocdn", links)

			ename := vokinoEpisodeNumRe.FindString(ep.Ident)
			e, _ := strconv.Atoi(ename)

			rows = append(rows, map[string]any{
				"method": "play",
				"url":    proxyURL,
				"stream": proxyURL,
				"s":      s,
				"e":      e,
				"name":   ep.Title,
				"title":  title,
			})
		}

		if len(rows) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}

		// Prepend voice HTML if needed
		if rjson {
			result := map[string]any{
				"type": "episode",
				"data": rows,
			}
			if len(voices) > 0 {
				voiceRows := v.buildVoiceRows(voices, host, origID, kpID, balancer, encTitle, encOrigTitle, rjson, sParam)
				result["voice"] = voiceRows
			}
			writeJSON(w, http.StatusOK, result)
			return
		}

		var sb strings.Builder
		if len(voices) > 0 {
			v.writeVoiceHTML(&sb, voices, host, origID, kpID, balancer, encTitle, encOrigTitle, rjson, sParam)
		}
		sb.WriteString(`<div class="videos__line">`)
		for i, row := range rows {
			name := fmt.Sprint(row["name"])
			if name == "" {
				name = "Серия"
			}
			getsTVAppendMovieHTML(&sb, row, name, i == 0, s, vokinoIntFromMap(row, "e"))
		}
		sb.WriteString(`</div>`)
		writeHTML(w, http.StatusOK, sb.String())
	} else {
		// Movie mode
		rows := make([]map[string]any, 0, len(root.Channels))
		for _, ch := range root.Channels {
			streamURL := ch.StreamURL
			if streamURL == "" {
				continue
			}
			proxyURL := streamProxyURL(req, streamURL, "vokinocdn", links)

			name := ch.QualityFull
			name = strings.ReplaceAll(name, "2160p.", "4K ")
			if size, ok := ch.Extra["size"]; ok && size != "" {
				name += " - " + size
			}

			rows = append(rows, map[string]any{
				"method": "play",
				"url":    proxyURL,
				"stream": proxyURL,
				"name":   name,
				"title":  title,
			})
		}

		if len(rows) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}

		// Build streamquality
		if len(rows) > 1 {
			sq := make([]map[string]any, len(rows))
			for i, r := range rows {
				sq[i] = map[string]any{
					"quality": r["name"],
					"url":     r["url"],
				}
			}
			rows[0]["streamquality"] = sq
		}

		if rjson {
			result := map[string]any{
				"type": "movie",
				"data": rows,
			}
			if len(voices) > 0 {
				voiceRows := v.buildVoiceRows(voices, host, origID, kpID, balancer, encTitle, encOrigTitle, rjson, sParam)
				result["voice"] = voiceRows
			}
			writeJSON(w, http.StatusOK, result)
			return
		}

		var sb strings.Builder
		if len(voices) > 0 {
			v.writeVoiceHTML(&sb, voices, host, origID, kpID, balancer, encTitle, encOrigTitle, rjson, sParam)
		}
		sb.WriteString(`<div class="videos__line">`)
		for i, row := range rows {
			name := fmt.Sprint(row["name"])
			if name == "" {
				name = title
			}
			getsTVAppendMovieHTML(&sb, row, name, i == 0, 0, 0)
		}
		sb.WriteString(`</div>`)
		writeHTML(w, http.StatusOK, sb.String())
	}
}

func (v *vokinoChecker) buildVoiceRows(voices []vokinoChannel, host, origID, kpID, balancer, encTitle, encOrigTitle string, rjson bool, sParam string) []map[string]any {
	rows := make([]map[string]any, 0, len(voices))
	for _, voice := range voices {
		if voice.PlaylistURL == "" || !strings.Contains(voice.PlaylistURL, "?") {
			continue
		}
		parts := strings.SplitN(voice.PlaylistURL, "?", 2)
		tVal := url.QueryEscape(parts[1])
		link := fmt.Sprintf("%s/lite/vokino?rjson=%v&origid=%s&kinopoisk_id=%s&balancer=%s&title=%s&original_title=%s&t=%s&s=%s",
			host, rjson, url.QueryEscape(origID), url.QueryEscape(kpID), url.QueryEscape(balancer), encTitle, encOrigTitle, tVal, url.QueryEscape(sParam))
		rows = append(rows, map[string]any{
			"method":   "link",
			"url":      link,
			"title":    voice.Title,
			"selected": voice.Selected,
		})
	}
	return rows
}

func (v *vokinoChecker) writeVoiceHTML(sb *strings.Builder, voices []vokinoChannel, host, origID, kpID, balancer, encTitle, encOrigTitle string, rjson bool, sParam string) {
	sb.WriteString(`<div class="videos__line">`)
	for _, voice := range voices {
		if voice.PlaylistURL == "" || !strings.Contains(voice.PlaylistURL, "?") {
			continue
		}
		parts := strings.SplitN(voice.PlaylistURL, "?", 2)
		tVal := url.QueryEscape(parts[1])
		link := fmt.Sprintf("%s/lite/vokino?rjson=%v&origid=%s&kinopoisk_id=%s&balancer=%s&title=%s&original_title=%s&t=%s&s=%s",
			host, rjson, url.QueryEscape(origID), url.QueryEscape(kpID), url.QueryEscape(balancer), encTitle, encOrigTitle, tVal, url.QueryEscape(sParam))
		sel := ""
		if voice.Selected {
			sel = " active"
		}
		sb.WriteString(fmt.Sprintf(`<div class="videos__button selector%s" data-json='{"method":"link","url":"%s"}'>`, sel, link))
		sb.WriteString(`<div class="videos__button-text">`)
		sb.WriteString(voice.Title)
		sb.WriteString(`</div></div>`)
	}
	sb.WriteString(`</div>`)
}

func vokinoIntFromMap(m map[string]any, key string) int {
	switch val := m[key].(type) {
	case int:
		return val
	case float64:
		return int(val)
	default:
		return 0
	}
}
