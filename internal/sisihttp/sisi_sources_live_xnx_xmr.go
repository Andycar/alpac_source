package sisihttp

import (
	"context"
	stdjson "encoding/json"
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
	xnxxHrefTitleRe = regexp.MustCompile(`(?is)<a href="/(video-[^"]+)" title="([^"]+)"`)
	xnxxImgRe       = regexp.MustCompile(`(?is)data-src="([^"]+)"`)
	xnxxTimeRe      = regexp.MustCompile(`(?is)</span>([^<]+)<span class="video-hd">`)
	xnxxQualityRe   = regexp.MustCompile(`(?is)<span class="superfluous">\s*-\s*</span>([^<]+)</span>`)
	xnxxHLSRe       = regexp.MustCompile(`(?is)html5player\.setVideoHLS\('([^']+)'\);`)

	xmrHrefTitleRe  = regexp.MustCompile(`(?is)__nam[^"]*" href="https?://[^/]+/([^"]+)"(?:[^>]+)?>(?:<!--[^-]+-->)?([^<]+)`)
	xmrDurationRe   = regexp.MustCompile(`(?is)data-role="video-duration"><[^>]+>([^<]+)`)
	xmrDatetimeRe   = regexp.MustCompile(`(?is)datetime="([^"]+)"`)
	xmrSrcsetRe     = regexp.MustCompile(`(?is)\s+srcset="([^"]+)"`)
	xmrImageRe      = regexp.MustCompile(`(?is)thumb-image-container__image" src="([^"]+)"`)
	xmrNoscriptRe   = regexp.MustCompile(`(?is)<noscript><img src="([^"]+)"`)
	xmrPreviewRe    = regexp.MustCompile(`(?is)data-previewvideo="([^"]+)"`)
	xmrStreamLinkRe = regexp.MustCompile(`(?is)rel="preload" href="([^"]+)"`)
	srcsetTailRe    = regexp.MustCompile(`(?is)\s+(?:[0-9]+w|[0-9]*\.?[0-9]+x)\s*$`)

	validXnxxPlugin     = map[string]struct{}{"xnx": {}}
	validXhamsterPlugin = map[string]struct{}{"xmr": {}, "xmrgay": {}, "xmrsml": {}}
)

type sisiXnxxSource struct {
	cfg    config.Config
	client *http.Client
	host   string
}

type sisiXhamsterSource struct {
	cfg    config.Config
	client *http.Client
	host   string
}

type xhamsterInitialsPayload struct {
	LayoutPage struct {
		VideoListProps struct {
			VideoThumbProps []xhamsterVideoThumb `json:"videoThumbProps"`
		} `json:"videoListProps"`
	} `json:"layoutPage"`
}

type xhamsterVideoThumb struct {
	VideoType  string `json:"videoType"`
	Title      string `json:"title"`
	PageURL    string `json:"pageURL"`
	ThumbURL   string `json:"thumbURL"`
	ImageURL   string `json:"imageURL"`
	TrailerURL string `json:"trailerURL"`
	IsUHD      bool   `json:"isUHD"`
	Duration   int    `json:"duration"`
}

func newSisiXnxxSource(cfg config.Config) *sisiXnxxSource {
	return &sisiXnxxSource{
		cfg:    cfg,
		client: httpclient.NewProxied(12 * time.Second),
		host:   SisiSourceHost("Xnxx", "https://www.xnxx.com"),
	}
}

func newSisiXhamsterSource(cfg config.Config) *sisiXhamsterSource {
	return &sisiXhamsterSource{
		cfg: cfg,
		// Use direct transport here: in deployments with global SOCKS sidecar,
		// xHamster often stalls/times out through proxy and returns empty lists.
		client: httpclient.New(25 * time.Second),
		host:   SisiSourceHost("Xhamster", "https://ru.xhamster.com"),
	}
}

func (s *sisiXnxxSource) listHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		plugin := sisiSourcePluginFromPath(r.URL.Path)
		if _, ok := validXnxxPlugin[plugin]; !ok {
			sisiListStubHandler(s.cfg).ServeHTTP(w, r)
			return
		}

		search := strings.TrimSpace(r.URL.Query().Get("search"))
		pg := sisiIntOrDefault(r.URL.Query().Get("pg"), 1)
		if pg <= 0 {
			pg = 1
		}

		// Unified category filter (xnxx has no native categories — search fallback).
		if cat := r.URL.Query().Get("cat"); cat != "" {
			res := resolveSisiCategory(plugin, cat)
			if res.UseSearch || res.CategoryValue != "" {
				if res.SearchTerm != "" {
					search = res.SearchTerm
				} else {
					search = res.CategoryValue
				}
			}
		}

		html, err := s.fetchHTML(r.Context(), s.listURL(search, pg))
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"menu":        xnxMenu(hostFromRequest(r)),
				"list":        []any{},
				"total_pages": 1,
			})
			return
		}

		resp := map[string]any{
			"list":        parseXnxxPlaylist(hostFromRequest(r), html),
			"total_pages": 1,
		}
		if search == "" {
			resp["menu"] = xnxMenu(hostFromRequest(r))
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func (s *sisiXnxxSource) viewHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		plugin := sisiSourcePluginFromPath(r.URL.Path)
		if _, ok := validXnxxPlugin[plugin]; !ok {
			sisiViewStubHandler(s.cfg).ServeHTTP(w, r)
			return
		}

		uri := strings.TrimSpace(r.URL.Query().Get("uri"))
		if uri == "" {
			writeJSON(w, http.StatusOK, map[string]any{"qualitys": map[string]any{}, "recomends": []any{}})
			return
		}

		html, err := s.fetchHTML(r.Context(), s.streamURL(uri))
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"qualitys": map[string]any{}, "recomends": []any{}})
			return
		}

		if parseBoolParam(r.URL.Query().Get("related")) {
			writeJSON(w, http.StatusOK, map[string]any{
				"list":        parseXnxxRelated(hostFromRequest(r), html),
				"total_pages": 1,
			})
			return
		}

		qualities := map[string]any{}
		if m := xnxxHLSRe.FindStringSubmatch(html); len(m) > 1 {
			qualities["auto"] = strings.TrimSpace(m[1])
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"qualitys":       qualities,
			"qualitys_proxy": sisiProxyQualitys(r, qualities),
			"recomends":      parseXnxxRelated(hostFromRequest(r), html),
		})
	}
}

func (s *sisiXhamsterSource) listHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		plugin := sisiSourcePluginFromPath(r.URL.Path)
		if _, ok := validXhamsterPlugin[plugin]; !ok {
			sisiListStubHandler(s.cfg).ServeHTTP(w, r)
			return
		}

		search := strings.TrimSpace(r.URL.Query().Get("search"))
		c := strings.TrimSpace(r.URL.Query().Get("c"))
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		sortKey := strings.TrimSpace(r.URL.Query().Get("sort"))
		if sortKey == "" {
			sortKey = "newest"
		}
		pg := sisiIntOrDefault(r.URL.Query().Get("pg"), 1)
		if pg <= 0 {
			pg = 1
		}

		// Unified category filter.
		if cat := r.URL.Query().Get("cat"); cat != "" {
			res := resolveSisiCategory(plugin, cat)
			if res.UseSearch {
				search = res.SearchTerm
			} else if res.CategoryValue != "" {
				c = res.CategoryValue
			}
		}

		// Legacy controller increments page before URL generation.
		pg++

		html, err := s.fetchHTML(r.Context(), s.listURL(plugin, search, c, q, sortKey, pg))
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"menu":        xhamsterMenu(hostFromRequest(r), plugin, c, q, sortKey),
				"list":        []any{},
				"total_pages": 1,
			})
			return
		}

		resp := map[string]any{
			"list":        parseXhamsterPlaylist(hostFromRequest(r), html),
			"total_pages": 1,
		}
		if search == "" {
			resp["menu"] = xhamsterMenu(hostFromRequest(r), plugin, c, q, sortKey)
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func (s *sisiXhamsterSource) viewHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		uri := strings.TrimSpace(r.URL.Query().Get("uri"))
		if uri == "" {
			writeJSON(w, http.StatusOK, map[string]any{"qualitys": map[string]any{}, "recomends": []any{}})
			return
		}

		html, err := s.fetchHTML(r.Context(), s.streamURL(uri))
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"qualitys": map[string]any{}, "recomends": []any{}})
			return
		}

		if parseBoolParam(r.URL.Query().Get("related")) {
			writeJSON(w, http.StatusOK, map[string]any{
				"list":        parseXhamsterPlaylist(hostFromRequest(r), html),
				"total_pages": 1,
			})
			return
		}

		stream := strings.TrimSpace(submatch1(xmrStreamLinkRe, html))
		qualities := map[string]any{}
		if strings.Contains(stream, ".m3u") {
			stream = strings.ReplaceAll(stream, `\`, "")
			if strings.HasPrefix(stream, "/") {
				stream = strings.TrimRight(s.host, "/") + stream
			}
			qualities["auto"] = stream
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"qualitys":       qualities,
			"qualitys_proxy": sisiProxyQualitys(r, qualities),
			"recomends":      parseXhamsterPlaylist(hostFromRequest(r), html),
		})
	}
}

func (s *sisiXnxxSource) fetchHTML(ctx context.Context, target string) (string, error) {
	return sisiFetchHTML(ctx, s.client, target)
}

func (s *sisiXhamsterSource) fetchHTML(ctx context.Context, target string) (string, error) {
	return sisiFetchHTML(ctx, s.client, target)
}

func (s *sisiXnxxSource) listURL(search string, pg int) string {
	host := strings.TrimRight(s.host, "/")
	if search != "" {
		return host + "/search/" + url.QueryEscape(search) + "/" + strconv.Itoa(pg)
	}
	return host + "/best/" + time.Now().AddDate(0, -1, 0).Format("2006-01") + "/" + strconv.Itoa(pg)
}

func (s *sisiXnxxSource) streamURL(uri string) string {
	path := strings.TrimLeft(strings.TrimSpace(uri), "/")
	if i := strings.Index(path, "/"); i > 0 {
		path = path[:i] + "/_"
	}
	return strings.TrimRight(s.host, "/") + "/" + path
}

func (s *sisiXhamsterSource) listURL(plugin, search, c, q, sortKey string, pg int) string {
	host := strings.TrimRight(s.host, "/")
	if search != "" {
		return host + "/search/" + url.QueryEscape(search) + "?page=" + strconv.Itoa(pg)
	}

	switch plugin {
	case "xmrsml":
		host += "/shemale"
	case "xmrgay":
		host += "/gay"
	}

	if c != "" {
		host += "/categories/" + url.PathEscape(c)
	}
	if q != "" {
		host += "/" + url.PathEscape(q)
	}
	switch sortKey {
	case "newest":
		host += "/newest"
	case "best":
		host += "/best"
	}
	if pg > 0 {
		host += "/" + strconv.Itoa(pg)
	}
	return host
}

func (s *sisiXhamsterSource) streamURL(uri string) string {
	return strings.TrimRight(s.host, "/") + "/" + strings.TrimLeft(strings.TrimSpace(uri), "/")
}

func parseXnxxPlaylist(host, html string) []map[string]any {
	parts := strings.Split(html, `<div id="video_`)
	if len(parts) <= 1 {
		return []map[string]any{}
	}

	out := make([]map[string]any, 0, len(parts)-1)
	for i := 1; i < len(parts); i++ {
		row := parts[i]
		m := xnxxHrefTitleRe.FindStringSubmatch(row)
		if len(m) < 3 {
			continue
		}
		href := strings.TrimSpace(m[1])
		title := strings.TrimSpace(m[2])
		if href == "" || title == "" {
			continue
		}

		img := strings.TrimSpace(submatch1(xnxxImgRe, row))
		if img == "" {
			continue
		}
		img = strings.ReplaceAll(img, ".THUMBNUM.", ".1.")

		preview := xnxxPreviewFromImage(img)
		item := map[string]any{
			"name":    title,
			"video":   host + "/xnx/vidosik?uri=" + url.QueryEscape(href),
			"picture": img,
			"preview": preview,
			"time":    strings.TrimSpace(submatch1(xnxxTimeRe, row)),
			"quality": strings.TrimSpace(submatch1(xnxxQualityRe, row)),
			"json":    true,
			"related": true,
			"bookmark": map[string]any{
				"site":  "xnx",
				"href":  href,
				"image": img,
			},
		}
		out = append(out, item)
	}
	return out
}

func xnxxPreviewFromImage(img string) string {
	preview := xvPreviewPath.ReplaceAllString(img, "/videopreview/")
	preview = xvPreviewTail.ReplaceAllString(preview, "")
	preview = xvPreviewDash.ReplaceAllString(preview, "")
	return preview + "_169.mp4"
}

func parseXnxxRelated(host, html string) []map[string]any {
	m := xvRelatedRe.FindStringSubmatch(html)
	if len(m) < 2 {
		return []map[string]any{}
	}
	type related struct {
		TF string `json:"tf"`
		U  string `json:"u"`
		I  string `json:"i"`
	}
	var arr []related
	if err := stdjson.Unmarshal([]byte(m[1]), &arr); err != nil {
		return []map[string]any{}
	}

	out := make([]map[string]any, 0, len(arr))
	for _, r := range arr {
		title := strings.TrimSpace(r.TF)
		href := strings.TrimLeft(strings.TrimSpace(r.U), "/")
		img := strings.TrimSpace(r.I)
		if title == "" || href == "" || img == "" {
			continue
		}
		out = append(out, map[string]any{
			"name":    title,
			"video":   host + "/xnx/vidosik?uri=" + url.QueryEscape(href),
			"picture": img,
			"json":    true,
			"related": true,
			"bookmark": map[string]any{
				"site":  "xnx",
				"href":  href,
				"image": img,
			},
		})
	}
	return out
}

func xnxMenu(host string) []map[string]any {
	base := host + "/xnx"
	return []map[string]any{
		{
			"title":        "Поиск",
			"search_on":    "search_on",
			"playlist_url": base,
		},
	}
}

func parseXhamsterPlaylist(host, html string) []map[string]any {
	if html == "" {
		return []map[string]any{}
	}

	out := parseXhamsterPlaylistFromMarkup(host, html)
	if len(out) > 0 {
		return out
	}
	return parseXhamsterPlaylistFromInitials(host, html)
}

func parseXhamsterPlaylistFromMarkup(host, html string) []map[string]any {
	normalized := strings.ReplaceAll(html, "thumb-list-mobile-item", `<div class="thumb-list__item video-thumb`)
	parts := strings.Split(normalized, `<div class="thumb-list__item video-thumb`)
	if len(parts) <= 1 {
		return []map[string]any{}
	}

	out := make([]map[string]any, 0, len(parts)-1)
	for i := 1; i < len(parts); i++ {
		row := parts[i]
		if strings.Contains(row, "badge_premium") {
			continue
		}

		m := xmrHrefTitleRe.FindStringSubmatch(row)
		if len(m) < 3 {
			continue
		}
		href := strings.TrimLeft(strings.TrimSpace(m[1]), "/")
		title := strings.TrimSpace(m[2])
		if href == "" || title == "" {
			continue
		}

		// Prefer explicit <img src="..."> because xHamster srcset URLs may
		// contain commas inside transform params (e.g. s(w:526,h:296),jpeg/...).
		// A naive srcset split then truncates URL and produces 404 posters.
		img := strings.TrimSpace(submatch1(xmrImageRe, row))
		if !strings.HasPrefix(img, "http") {
			img = strings.TrimSpace(submatch1(xmrNoscriptRe, row))
		}
		if img == "" || strings.Contains(img, "(w:16,h:9)") {
			img = firstSrcsetURL(strings.TrimSpace(submatch1(xmrSrcsetRe, row)))
			if !strings.HasPrefix(img, "http") {
				img = strings.TrimSpace(submatch1(xmrNoscriptRe, row))
			}
		}
		if !strings.HasPrefix(img, "http") {
			continue
		}

		duration := strings.TrimSpace(submatch1(xmrDurationRe, row))
		if duration == "" {
			duration = strings.TrimSpace(submatch1(xmrDatetimeRe, row))
		}

		quality := ""
		if strings.Contains(row, "-uhd") {
			quality = "4K"
		} else if strings.Contains(row, "-hd") {
			quality = "HD"
		}

		item := map[string]any{
			"name":    title,
			"video":   host + "/xmr/vidosik?uri=" + url.QueryEscape(href),
			"picture": img,
			"time":    duration,
			"quality": quality,
			"preview": strings.TrimSpace(submatch1(xmrPreviewRe, row)),
			"json":    true,
			"related": true,
			"bookmark": map[string]any{
				"site":  "xmr",
				"href":  href,
				"image": img,
			},
		}
		out = append(out, item)
	}
	return out
}

func parseXhamsterPlaylistFromInitials(host, html string) []map[string]any {
	const marker = "window.initials="
	start := strings.Index(html, marker)
	if start < 0 {
		return []map[string]any{}
	}
	start += len(marker)

	end := strings.Index(html[start:], ";</script>")
	if end < 0 {
		end = strings.Index(html[start:], "</script>")
	}
	if end < 0 {
		return []map[string]any{}
	}

	raw := strings.TrimSpace(html[start : start+end])
	if raw == "" {
		return []map[string]any{}
	}

	var payload xhamsterInitialsPayload
	if err := stdjson.Unmarshal([]byte(raw), &payload); err != nil {
		return []map[string]any{}
	}
	thumbs := payload.LayoutPage.VideoListProps.VideoThumbProps
	if len(thumbs) == 0 {
		return []map[string]any{}
	}

	out := make([]map[string]any, 0, len(thumbs))
	for _, thumb := range thumbs {
		if thumb.VideoType != "" && thumb.VideoType != "video" {
			continue
		}
		title := strings.TrimSpace(thumb.Title)
		if title == "" {
			continue
		}
		href := xhamsterURIFromPageURL(thumb.PageURL)
		if href == "" {
			continue
		}
		img := strings.TrimSpace(thumb.ThumbURL)
		if img == "" {
			img = strings.TrimSpace(thumb.ImageURL)
		}
		if !strings.HasPrefix(img, "http") {
			continue
		}

		quality := ""
		if thumb.IsUHD {
			quality = "4K"
		}

		item := map[string]any{
			"name":    title,
			"video":   host + "/xmr/vidosik?uri=" + url.QueryEscape(href),
			"picture": img,
			"time":    xhamsterDurationString(thumb.Duration),
			"quality": quality,
			"preview": strings.TrimSpace(thumb.TrailerURL),
			"json":    true,
			"related": true,
			"bookmark": map[string]any{
				"site":  "xmr",
				"href":  href,
				"image": img,
			},
		}
		out = append(out, item)
	}
	return out
}

func xhamsterURIFromPageURL(pageURL string) string {
	pageURL = strings.TrimSpace(pageURL)
	if pageURL == "" {
		return ""
	}

	if parsed, err := url.Parse(pageURL); err == nil {
		path := strings.Trim(strings.TrimSpace(parsed.Path), "/")
		if path != "" {
			return path
		}
	}
	return strings.Trim(pageURL, "/")
}

func xhamsterDurationString(seconds int) string {
	if seconds <= 0 {
		return ""
	}
	h := seconds / 3600
	m := (seconds % 3600) / 60
	s := seconds % 60

	if h > 0 {
		return strconv.Itoa(h) + ":" + twoDigits(m) + ":" + twoDigits(s)
	}
	return strconv.Itoa(m) + ":" + twoDigits(s)
}

func twoDigits(v int) string {
	if v < 10 {
		return "0" + strconv.Itoa(v)
	}
	return strconv.Itoa(v)
}

func firstSrcsetURL(srcset string) string {
	srcset = strings.TrimSpace(srcset)
	if srcset == "" {
		return ""
	}

	start := 0
	depth := 0
	for i := 0; i < len(srcset); i++ {
		switch srcset[i] {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				// In xHamster URLs comma may appear inside transform path
				// (e.g. "...s(w:526,h:296),jpeg/..."). Treat comma as a
				// separator only when it starts the next candidate URL.
				j := i + 1
				for j < len(srcset) && srcset[j] == ' ' {
					j++
				}
				if j < len(srcset) &&
					!strings.HasPrefix(srcset[j:], "http") &&
					!strings.HasPrefix(srcset[j:], "//") &&
					!strings.HasPrefix(srcset[j:], "data:") &&
					!strings.HasPrefix(srcset[j:], "blob:") {
					continue
				}
				part := strings.TrimSpace(srcset[start:i])
				if u := srcsetPartURL(part); u != "" {
					return u
				}
				start = i + 1
			}
		}
	}
	if start < len(srcset) {
		part := strings.TrimSpace(srcset[start:])
		if u := srcsetPartURL(part); u != "" {
			return u
		}
	}

	return ""
}

func srcsetPartURL(part string) string {
	part = strings.TrimSpace(part)
	if part == "" {
		return ""
	}
	part = srcsetTailRe.ReplaceAllString(part, "")
	part = strings.TrimSpace(part)
	if strings.HasPrefix(part, "http://") || strings.HasPrefix(part, "https://") || strings.HasPrefix(part, "//") {
		return part
	}
	return ""
}

func xhamsterMenu(host, plugin, c, q, sortKey string) []map[string]any {
	base := host + "/" + plugin

	sortTitle := "В тренде"
	if sortKey == "newest" {
		sortTitle = "Новинки"
	} else if sortKey == "best" {
		sortTitle = "Лучшие"
	}
	qualityTitle := "Любое"
	if q == "4k" {
		qualityTitle = "2160p"
	}
	orientationTitle := "Гетеро"
	if plugin == "xmrgay" {
		orientationTitle = "Геи"
	} else if plugin == "xmrsml" {
		orientationTitle = "Трансы"
	}
	buildURL := func(category string) string {
		args := make([]string, 0, 3)
		if category != "" {
			args = append(args, "c="+url.QueryEscape(category))
		}
		if q != "" {
			args = append(args, "q="+url.QueryEscape(q))
		}
		if sortKey != "" {
			args = append(args, "sort="+url.QueryEscape(sortKey))
		}
		if len(args) == 0 {
			return base
		}
		return base + "?" + strings.Join(args, "&")
	}

	menu := []map[string]any{
		{
			"title":        "Поиск",
			"search_on":    "search_on",
			"playlist_url": base,
		},
		{
			"title":        "Качество: " + qualityTitle,
			"playlist_url": "submenu",
			"submenu": []map[string]any{
				{"title": "Любое", "playlist_url": base + "?c=" + c + "&sort=" + sortKey},
				{"title": "2160p", "playlist_url": base + "?c=" + c + "&sort=" + sortKey + "&q=4k"},
			},
		},
		{
			"title":        "Сортировка: " + sortTitle,
			"playlist_url": "submenu",
			"submenu": []map[string]any{
				{"title": "В тренде", "playlist_url": base + "?c=" + c + "&q=" + q + "&sort=trend"},
				{"title": "Самые новые", "playlist_url": base + "?c=" + c + "&q=" + q + "&sort=newest"},
				{"title": "Лучшие видео", "playlist_url": base + "?c=" + c + "&q=" + q + "&sort=best"},
			},
		},
		{
			"title":        "Ориентация: " + orientationTitle,
			"playlist_url": "submenu",
			"submenu": []map[string]any{
				{"title": "Гетеро", "playlist_url": host + "/xmr"},
				{"title": "Геи", "playlist_url": host + "/xmrgay"},
				{"title": "Трансы", "playlist_url": host + "/xmrsml"},
			},
		},
	}

	type cat struct {
		title string
		value string
	}
	var cats []cat
	switch plugin {
	case "xmr":
		cats = []cat{
			{title: "Русское", value: "russian"},
			{title: "Анал", value: "anal"},
			{title: "Азиатское", value: "asian"},
			{title: "Большие сиськи", value: "big-tits"},
			{title: "MILF", value: "milf"},
			{title: "Тинейджеры", value: "teen"},
			{title: "Любительское", value: "amateur"},
			{title: "Лесбиянки", value: "lesbian"},
			{title: "Межрасовое", value: "interracial"},
			{title: "Минет", value: "blowjob"},
			{title: "BBW", value: "bbw"},
			{title: "Хентай", value: "hentai"},
		}
	case "xmrgay":
		cats = []cat{
			{title: "Азиатское", value: "asian"},
			{title: "Без презерватива", value: "bareback"},
			{title: "Большой член", value: "big-cock"},
			{title: "Минет", value: "blowjob"},
			{title: "Твинки", value: "twink"},
			{title: "Медведи", value: "bear"},
			{title: "Межрасовое", value: "interracial"},
			{title: "Мастурбация", value: "masturbation"},
			{title: "Соло", value: "solo"},
			{title: "Хентай", value: "hentai"},
		}
	case "xmrsml":
		cats = []cat{
			{title: "Азиатское", value: "asian"},
			{title: "Блондинки", value: "blonde"},
			{title: "Большие жопы", value: "big-ass"},
			{title: "Большой член", value: "big-cock"},
			{title: "Ледибои", value: "ladyboy"},
			{title: "Trap", value: "trap"},
			{title: "Любительское", value: "amateur"},
			{title: "Минет", value: "blowjob"},
			{title: "Межрасовое", value: "interracial"},
			{title: "Тинейджеры", value: "teen"},
			{title: "Хентай", value: "hentai"},
		}
	}
	if len(cats) > 0 {
		active := "все"
		sub := make([]map[string]any, 0, len(cats)+1)
		sub = append(sub, map[string]any{
			"title":        "Все",
			"playlist_url": buildURL(""),
		})
		for _, cat := range cats {
			if cat.value == c {
				active = cat.title
			}
			sub = append(sub, map[string]any{
				"title":        cat.title,
				"playlist_url": buildURL(cat.value),
			})
		}
		menu = append(menu, map[string]any{
			"title":        "Категория: " + active,
			"playlist_url": "submenu",
			"submenu":      sub,
		})
	}
	return menu
}
