package sisihttp

import (
	"context"
	stdjson "encoding/json"
	"io"
	"lampac-go/internal/httpclient"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/config"
)

var (
	xvLinkTitleRe1 = regexp.MustCompile(`(?is)<a href="/(video[^"]+|search-video/[^"]+)" title="([^"]+)"`)
	xvLinkTitleRe2 = regexp.MustCompile(`(?is)<a href="/(video[^"]+)"[^>]*>([^<]+)`)
	xvImageRe      = regexp.MustCompile(`(?is)data-src="([^"]+)"`)
	xvTimeRe       = regexp.MustCompile(`(?is)<span class="duration">([^<]+)</span>`)
	xvQualityRe    = regexp.MustCompile(`(?is)<span class="video-hd-mark">([^<]+)</span>`)
	xvModelRe      = regexp.MustCompile(`(?is)href="/([^"]+)"><span class="name">([^<]+)<`)
	xvHLSRe        = regexp.MustCompile(`(?is)html5player\.setVideoHLS\('([^']+)'\);`)
	xvRelatedRe    = regexp.MustCompile(`(?is)video_related=([^\n\r]+);window`)
	xvThumbPathRe  = regexp.MustCompile(`/videos/thumbs([0-9]+)/`)
	xvThumbNumRe   = regexp.MustCompile(`\.THUMBNUM\.(jpg|png)$`)
	xvPreviewPath  = regexp.MustCompile(`/thumbs[^/]+/`)
	xvPreviewTail  = regexp.MustCompile(`/[^/]+$`)
	xvPreviewDash  = regexp.MustCompile(`-[0-9]+$`)

	phVKeyRe1     = regexp.MustCompile(`(?is)(?:-|_)vkey="([^"]+)"`)
	phVKeyRe2     = regexp.MustCompile(`(?is)viewkey=([^"&]+)`)
	phTitleRe1    = regexp.MustCompile(`(?is)href="/[^"]+" title="([^"]+)"`)
	phTitleRe2    = regexp.MustCompile(`(?is)class="videoTitle">([^<]+)<`)
	phTitleRe3    = regexp.MustCompile(`(?is)href="/view_[^"]+" onclick=[^>]+>([^<]+)<`)
	phImageRe1    = regexp.MustCompile(`(?is)data-mediumthumb="(https?://[^"]+)"`)
	phImageRe2    = regexp.MustCompile(`(?is)<img[^>]+src="([^"]+)"`)
	phTimeRe1     = regexp.MustCompile(`(?is)<var class="duration">([^<]+)</var>`)
	phTimeRe2     = regexp.MustCompile(`(?is)class="time">([^<]+)<`)
	phTimeRe3     = regexp.MustCompile(`(?is)class="videoDuration floatLeft">([^<]+)<`)
	phTimeRe4     = regexp.MustCompile(`(?is)time">([^<]+)<`)
	phModelRe1    = regexp.MustCompile(`(?is)href="/model/([^"]+)"[^>]*>([^<]+)<`)
	phModelRe2    = regexp.MustCompile(`(?is)href="/(pornstar/[^"]+)"[^>]*>([^<]+)<`)
	phPageRe      = regexp.MustCompile(`(?is)class="page_number"><a [^>]+>([0-9]+)<`)
	phQualityRe   = regexp.MustCompile(`(?is)"videoUrl":"([^"]+)","quality":"(1080|720|480|240)"`)
	phUnescapeRe  = strings.NewReplacer(`\\`, ``, "///", "//", `\/`, `/`)
	validXvPlugin = map[string]struct{}{"xds": {}, "xdsgay": {}, "xdssml": {}, "xdsred": {}}
	validPhPlugin = map[string]struct{}{"phub": {}, "phubgay": {}, "phubsml": {}, "phubprem": {}}
)

type sisiXvideosSource struct {
	cfg           config.Config
	client        *http.Client
	clientDirect  *http.Client
	host          string
	hostRed       string
	fallbackHosts []string
}

type sisiPornHubSource struct {
	cfg               config.Config
	client            *http.Client
	clientDirect      *http.Client
	host              string
	hostPrem          string
	fallbackHosts     []string
	fallbackPremHosts []string
}

func newSisiXvideosSource(cfg config.Config) *sisiXvideosSource {
	host := SisiSourceHost("Xvideos", "https://www.xvideos.com")
	hostRed := SisiSourceHost("XvideosRED", "https://www.xvideos.red")
	return &sisiXvideosSource{
		cfg:           cfg,
		client:        httpclient.NewProxied(12 * time.Second),
		clientDirect:  httpclient.New(12 * time.Second),
		host:          host,
		hostRed:       hostRed,
		fallbackHosts: sisiHostFallbacks(host, hostRed, "https://www.xv-ru.com", "https://www.xvideos2.com"),
	}
}

func newSisiPornHubSource(cfg config.Config) *sisiPornHubSource {
	host := SisiSourceHost("PornHub", "https://rt.pornhub.com")
	hostPrem := SisiSourceHost("PornHubPremium", "https://rt.pornhubpremium.com")
	return &sisiPornHubSource{
		cfg:               cfg,
		client:            httpclient.NewProxied(12 * time.Second),
		clientDirect:      httpclient.New(12 * time.Second),
		host:              host,
		hostPrem:          hostPrem,
		fallbackHosts:     sisiHostFallbacks(host, strings.Replace(host, "rt.pornhub.com", "www.pornhub.com", 1)),
		fallbackPremHosts: sisiHostFallbacks(hostPrem, strings.Replace(hostPrem, "rt.pornhubpremium.com", "www.pornhubpremium.com", 1)),
	}
}

func (s *sisiXvideosSource) listHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		plugin := sisiSourcePluginFromPath(r.URL.Path)
		if _, ok := validXvPlugin[plugin]; !ok {
			sisiListStubHandler(s.cfg).ServeHTTP(w, r)
			return
		}

		search := strings.TrimSpace(r.URL.Query().Get("search"))
		sortKey := strings.TrimSpace(r.URL.Query().Get("sort"))
		category := strings.TrimSpace(r.URL.Query().Get("c"))
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
				category = res.CategoryValue
			}
		}

		var (
			list      []map[string]any
			fetchedOK bool
		)
		for _, srcHost := range s.hostCandidates(plugin) {
			html, err := s.fetchHTML(r.Context(), s.listURLForHost(srcHost, plugin, search, sortKey, category, pg))
			if err != nil {
				continue
			}
			fetchedOK = true
			list = parseXvideosPlaylist(hostFromRequest(r), plugin, html)
			if len(list) > 0 {
				break
			}
		}

		if !fetchedOK {
			writeJSON(w, http.StatusOK, map[string]any{
				"menu":        xvideosMenu(hostFromRequest(r), plugin, search, sortKey, category),
				"list":        []any{},
				"total_pages": 1,
			})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"menu":        xvideosMenu(hostFromRequest(r), plugin, search, sortKey, category),
			"list":        list,
			"total_pages": 1,
		})
	}
}

func (s *sisiXvideosSource) starsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		plugin := sisiSourcePluginFromPath(r.URL.Path)
		if _, ok := validXvPlugin[plugin]; !ok {
			sisiStarsStubHandler(s.cfg).ServeHTTP(w, r)
			return
		}

		uri := strings.TrimSpace(r.URL.Query().Get("uri"))
		if uri == "" {
			writeJSON(w, http.StatusOK, map[string]any{"list": []any{}, "total_pages": 1})
			return
		}

		sortKey := strings.TrimSpace(r.URL.Query().Get("sort"))
		if sortKey == "" {
			sortKey = "new"
		}
		pg := max(sisiIntOrDefault(r.URL.Query().Get("pg"), 0), 0)

		jsonURL := s.starsURL(plugin, uri, sortKey, pg)
		body, err := s.fetchJSONMap(r.Context(), jsonURL)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"list": []any{}, "total_pages": 1})
			return
		}
		rawVideos, _ := body["videos"].([]any)
		list := parseXvideosStars(hostFromRequest(r), plugin, rawVideos)

		writeJSON(w, http.StatusOK, map[string]any{
			"list":        list,
			"total_pages": 1,
		})
	}
}

func (s *sisiXvideosSource) viewHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		plugin := sisiSourcePluginFromPath(r.URL.Path)
		if _, ok := validXvPlugin[plugin]; !ok {
			sisiViewStubHandler(s.cfg).ServeHTTP(w, r)
			return
		}

		uri := strings.TrimSpace(r.URL.Query().Get("uri"))
		if uri == "" {
			writeJSON(w, http.StatusOK, map[string]any{"qualitys": map[string]any{}, "recomends": []any{}})
			return
		}

		if parseBoolParam(r.URL.Query().Get("related")) {
			for _, srcHost := range s.hostCandidates(plugin) {
				html, err := s.fetchHTML(r.Context(), strings.TrimRight(srcHost, "/")+"/"+strings.TrimLeft(uri, "/"))
				if err != nil {
					continue
				}
				related := parseXvideosRelated(hostFromRequest(r), plugin, html)
				if len(related) > 0 {
					writeJSON(w, http.StatusOK, map[string]any{
						"list":        related,
						"total_pages": 1,
					})
					return
				}
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"list":        []any{},
				"total_pages": 1,
			})
			return
		}

		qualities := map[string]any{}
		recomends := []map[string]any{}
		for _, srcHost := range s.hostCandidates(plugin) {
			html, err := s.fetchHTML(r.Context(), strings.TrimRight(srcHost, "/")+"/"+strings.TrimLeft(uri, "/"))
			if err != nil {
				continue
			}
			recomends = parseXvideosRelated(hostFromRequest(r), plugin, html)
			if m := xvHLSRe.FindStringSubmatch(html); len(m) > 1 {
				qualities["auto"] = m[1]
				break
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"qualitys":       qualities,
			"qualitys_proxy": sisiProxyQualitys(r, qualities),
			"recomends":      recomends,
		})
	}
}

func (s *sisiPornHubSource) listHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		plugin := sisiSourcePluginFromPath(r.URL.Path)
		if _, ok := validPhPlugin[plugin]; !ok {
			sisiListStubHandler(s.cfg).ServeHTTP(w, r)
			return
		}

		search := strings.TrimSpace(r.URL.Query().Get("search"))
		model := strings.TrimSpace(r.URL.Query().Get("model"))
		sortKey := strings.TrimSpace(r.URL.Query().Get("sort"))
		hd := strings.TrimSpace(r.URL.Query().Get("hd"))
		category := sisiIntOrDefault(r.URL.Query().Get("c"), 0)
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
				category = sisiIntOrDefault(res.CategoryValue, 0)
			}
		}

		var (
			list      []map[string]any
			fetchedOK bool
			total     = 1
		)
		for _, srcHost := range s.hostCandidates(plugin) {
			raw, err := s.fetchHTML(r.Context(), phubListURL(srcHost, plugin, search, model, sortKey, category, hd, pg))
			if err != nil {
				continue
			}
			fetchedOK = true
			list = parsePornHubPlaylist(hostFromRequest(r), plugin, raw)
			total = parsePornHubPages(raw)
			if len(list) > 0 {
				break
			}
		}

		if !fetchedOK {
			writeJSON(w, http.StatusOK, map[string]any{
				"menu":        phubMenu(hostFromRequest(r), plugin, search, sortKey, category, hd),
				"list":        []any{},
				"total_pages": 1,
			})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"menu":        phubMenu(hostFromRequest(r), plugin, search, sortKey, category, hd),
			"list":        list,
			"total_pages": total,
		})
	}
}

func (s *sisiPornHubSource) viewHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		plugin := sisiSourcePluginFromPath(r.URL.Path)
		if _, ok := validPhPlugin[plugin]; !ok {
			sisiViewStubHandler(s.cfg).ServeHTTP(w, r)
			return
		}

		vkey := strings.TrimSpace(r.URL.Query().Get("vkey"))
		if vkey == "" {
			writeJSON(w, http.StatusOK, map[string]any{"qualitys": map[string]any{}, "recomends": []any{}})
			return
		}

		if parseBoolParam(r.URL.Query().Get("related")) {
			for _, srcHost := range s.hostCandidates(plugin) {
				html, err := s.fetchHTML(r.Context(), strings.TrimRight(srcHost, "/")+"/view_video.php?viewkey="+url.QueryEscape(vkey))
				if err != nil {
					continue
				}
				related := parsePornHubPlaylist(hostFromRequest(r), plugin, html)
				if len(related) > 0 {
					writeJSON(w, http.StatusOK, map[string]any{
						"list":        related,
						"total_pages": 1,
					})
					return
				}
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"list":        []any{},
				"total_pages": 1,
			})
			return
		}

		qualities := map[string]any{}
		recomends := []map[string]any{}
		for _, srcHost := range s.hostCandidates(plugin) {
			html, err := s.fetchHTML(r.Context(), strings.TrimRight(srcHost, "/")+"/view_video.php?viewkey="+url.QueryEscape(vkey))
			if err != nil {
				continue
			}
			recomends = parsePornHubPlaylist(hostFromRequest(r), plugin, html)
			for _, m := range phQualityRe.FindAllStringSubmatch(html, -1) {
				if len(m) < 3 {
					continue
				}
				qualities[m[2]+"p"] = phUnescapeRe.Replace(m[1])
			}
			if len(qualities) > 0 {
				break
			}
		}

		proxyQualities := sisiProxyQualitys(r, qualities)
		respQualities := qualities
		if len(proxyQualities) > 0 && sisiPreferProxyQualitys(r) {
			respQualities = proxyQualities
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"qualitys":       respQualities,
			"qualitys_proxy": proxyQualities,
			"recomends":      recomends,
		})
	}
}

func (s *sisiXvideosSource) fetchHTML(ctx context.Context, target string) (string, error) {
	if html, err := sisiFetchHTML(ctx, s.client, target); err == nil && html != "" {
		return html, nil
	}
	return sisiFetchHTML(ctx, s.clientDirect, target)
}

func (s *sisiPornHubSource) fetchHTML(ctx context.Context, target string) (string, error) {
	if html, err := sisiFetchHTML(ctx, s.client, target); err == nil && html != "" {
		return html, nil
	}
	return sisiFetchHTML(ctx, s.clientDirect, target)
}

func (s *sisiXvideosSource) fetchJSONMap(ctx context.Context, target string) (map[string]any, error) {
	return sisiFetchJSONMap(ctx, s.client, target)
}

func sisiFetchHTML(ctx context.Context, client *http.Client, target string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/json;q=0.9,*/*;q=0.8")
	req.Header.Set("X-Lampac-Go", "1")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func sisiFetchJSONMap(ctx context.Context, client *http.Client, target string) (map[string]any, error) {
	raw, err := sisiFetchHTML(ctx, client, target)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := stdjson.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func sisiNormalizePathFromHref(href string) string {
	href = strings.TrimSpace(href)
	if href == "" {
		return ""
	}
	low := strings.ToLower(href)
	if strings.HasPrefix(low, "javascript:") || strings.HasPrefix(href, "#") {
		return ""
	}
	if u, err := url.Parse(href); err == nil && u.Scheme != "" {
		href = strings.TrimSpace(u.Path)
		if u.RawQuery != "" {
			href += "?" + u.RawQuery
		}
	}
	return strings.TrimLeft(href, "/")
}

func (s *sisiXvideosSource) listURL(plugin, search, sortKey, c string, pg int) string {
	return s.listURLForHost(s.hostByPlugin(plugin), plugin, search, sortKey, c, pg)
}

func (s *sisiXvideosSource) listURLForHost(host, plugin, search, sortKey, c string, pg int) string {
	host = strings.TrimRight(host, "/")
	if search != "" {
		return host + "/?k=" + url.QueryEscape(search) + "&p=" + strconv.Itoa(pg)
	}
	if c != "" {
		order := "uploaddate"
		if sortKey == "top" {
			order = "rating"
		}
		return host + "/c/s:" + order + "/" + c + "/" + strconv.Itoa(pg)
	}
	if sortKey == "top" {
		base := "best"
		if plugin == "xdsgay" {
			base = "best-of-gay"
		} else if plugin == "xdssml" {
			base = "best-of-shemale"
		}
		return host + "/" + base + "/" + time.Now().AddDate(0, -1, 0).Format("2006-01")
	}
	base := "new"
	if plugin == "xdsgay" {
		base = "gay"
	} else if plugin == "xdssml" {
		base = "shemale"
	}
	return host + "/" + base + "/" + strconv.Itoa(pg)
}

func (s *sisiXvideosSource) starsURL(plugin, uri, sortKey string, pg int) string {
	host := strings.TrimRight(s.hostByPlugin(plugin), "/")
	path := strings.TrimLeft(uri, "/")
	if plugin == "xdsgay" {
		return host + "/" + path + "/videos/" + sortKey + "/gay/" + strconv.Itoa(pg)
	}
	if plugin == "xdssml" {
		return host + "/" + path + "/videos/" + sortKey + "/shemale/" + strconv.Itoa(pg)
	}
	return host + "/" + path + "/videos/" + sortKey + "/" + strconv.Itoa(pg)
}

func parseXvideosPlaylist(host, plugin, html string) []map[string]any {
	videoRoute := "/" + plugin + "/vidosik"

	parts := strings.Split(html, `<div id="video_`)
	if len(parts) <= 1 {
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(parts)-1)
	for i := 1; i < len(parts); i++ {
		row := parts[i]
		href, title := "", ""
		if m := xvLinkTitleRe1.FindStringSubmatch(row); len(m) > 2 {
			href, title = m[1], m[2]
		} else if m := xvLinkTitleRe2.FindStringSubmatch(row); len(m) > 2 {
			href, title = m[1], m[2]
		}
		if href == "" || title == "" {
			continue
		}
		// Remove THUMBNUM placeholder from video href (xvideos redirects anyway)
		href = strings.ReplaceAll(href, "/THUMBNUM/", "/")

		img := submatch1(xvImageRe, row)
		if img == "" {
			continue
		}
		img = xvThumbPathRe.ReplaceAllString(img, "/videos/thumbs$1lll/")
		img = xvThumbNumRe.ReplaceAllString(img, ".1.$1")
		img = strings.ReplaceAll(img, "THUMBNUM", "1")
		img = strings.ReplaceAll(img, "thumbs169l/", "thumbs169lll/")
		img = strings.ReplaceAll(img, "thumbs169ll/", "thumbs169lll/")

		preview := xvPreviewPath.ReplaceAllString(img, "/videopreview/")
		preview = xvPreviewTail.ReplaceAllString(preview, "")
		preview = xvPreviewDash.ReplaceAllString(preview, "")
		preview += "_169.mp4"

		item := map[string]any{
			"name":    title,
			"video":   host + videoRoute + "?uri=" + url.QueryEscape(href),
			"picture": img,
			"preview": preview,
			"quality": submatch1(xvQualityRe, row),
			"time":    strings.TrimSpace(submatch1(xvTimeRe, row)),
			"json":    true,
			"related": true,
			"bookmark": map[string]any{
				"site":  plugin,
				"href":  href,
				"image": img,
			},
		}

		if m := xvModelRe.FindStringSubmatch(row); len(m) > 2 {
			modelPath := m[1]
			if !strings.Contains(modelPath, "/") {
				modelPath = "channels/" + modelPath
			}
			item["model"] = map[string]any{
				"name": m[2],
				"uri":  host + "/" + plugin + "/stars?uri=" + url.QueryEscape(modelPath),
			}
		}
		out = append(out, item)
	}
	return out
}

func parseXvideosStars(host, plugin string, videos []any) []map[string]any {
	videoRoute := "/" + plugin + "/vidosik"

	out := make([]map[string]any, 0, len(videos))
	for _, raw := range videos {
		m, _ := raw.(map[string]any)
		if m == nil {
			continue
		}
		tf := strings.TrimSpace(toString(m["tf"]))
		u := strings.TrimLeft(strings.TrimSpace(toString(m["u"])), "/")
		img := strings.TrimSpace(toString(m["if"]))
		if tf == "" || u == "" || img == "" {
			continue
		}

		preview := xvPreviewPath.ReplaceAllString(img, "/videopreview/")
		preview = xvPreviewTail.ReplaceAllString(preview, "")
		preview = xvPreviewDash.ReplaceAllString(preview, "")
		preview += "_169.mp4"

		item := map[string]any{
			"name":    tf,
			"video":   host + videoRoute + "?uri=" + url.QueryEscape(u),
			"picture": img,
			"preview": preview,
			"time":    strings.TrimSpace(toString(m["d"])),
			"json":    true,
			"related": true,
			"bookmark": map[string]any{
				"site":  plugin,
				"href":  u,
				"image": img,
			},
		}

		p := strings.TrimSpace(toString(m["p"]))
		pn := strings.TrimSpace(toString(m["pn"]))
		if p != "" && pn != "" {
			modelPath := "pornstars/" + p
			if toBool(m["ch"]) {
				modelPath = "channels/" + p
			}
			item["model"] = map[string]any{
				"name": pn,
				"uri":  host + "/" + plugin + "/stars?uri=" + url.QueryEscape(modelPath),
			}
		}
		out = append(out, item)
	}
	return out
}

func parseXvideosRelated(host, plugin, html string) []map[string]any {
	m := xvRelatedRe.FindStringSubmatch(html)
	if len(m) < 2 {
		return []map[string]any{}
	}
	var arr []any
	if err := stdjson.Unmarshal([]byte(m[1]), &arr); err != nil {
		return []map[string]any{}
	}
	return parseXvideosStars(host, plugin, arr)
}

func xvideosMenu(host, plugin, search, sortKey, c string) []map[string]any {
	base := host + "/" + plugin
	menu := []map[string]any{
		{
			"title":        "Поиск",
			"search_on":    "search_on",
			"playlist_url": base,
		},
	}
	sortTitle := "Новое"
	if sortKey == "top" {
		sortTitle = "Лучшие"
	}
	menu = append(menu, map[string]any{
		"title":        "Сортировка: " + sortTitle,
		"playlist_url": "submenu",
		"submenu": []map[string]any{
			{"title": "Новое", "playlist_url": base + "?c=" + c},
			{"title": "Лучшие", "playlist_url": base + "?c=" + c + "&sort=top"},
		},
	})
	if plugin != "xdsred" {
		menu = append(menu, map[string]any{
			"title":        "Ориентация",
			"playlist_url": "submenu",
			"submenu": []map[string]any{
				{"title": "Гетеро", "playlist_url": host + "/xds"},
				{"title": "Геи", "playlist_url": host + "/xdsgay"},
				{"title": "Трансы", "playlist_url": host + "/xdssml"},
			},
		})
	}
	if search == "" && (plugin == "xds" || plugin == "xdsred") {
		type cat struct {
			title string
			value string
		}
		cats := []cat{
			{title: "Азиатки", value: "Asian_Woman-32"},
			{title: "Анал", value: "Anal-12"},
			{title: "Любительское", value: "Amateur-65"},
			{title: "Большие жопы", value: "Big_Ass-24"},
			{title: "Большие сиськи", value: "Big_Tits-23"},
			{title: "Блондинки", value: "Blonde-20"},
			{title: "Брюнетки", value: "Brunette-25"},
			{title: "Минет", value: "Blowjob-15"},
			{title: "Лесбиянки", value: "Lesbian-26"},
			{title: "MILF", value: "Milf-19"},
			{title: "Тинейджеры", value: "Teen-13"},
			{title: "Межрассовое", value: "Interracial-27"},
		}
		buildURL := func(category string) string {
			args := make([]string, 0, 2)
			if sortKey != "" {
				args = append(args, "sort="+url.QueryEscape(sortKey))
			}
			if category != "" {
				args = append(args, "c="+url.QueryEscape(category))
			}
			if len(args) == 0 {
				return base
			}
			return base + "?" + strings.Join(args, "&")
		}
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

func (s *sisiXvideosSource) hostByPlugin(plugin string) string {
	if plugin == "xdsred" {
		return s.hostRed
	}
	return s.host
}

func (s *sisiXvideosSource) hostCandidates(plugin string) []string {
	switch plugin {
	case "xdsred":
		return sisiHostFallbacks(s.hostRed, append([]string{s.host}, s.fallbackHosts...)...)
	default:
		return sisiHostFallbacks(s.host, append([]string{s.hostRed}, s.fallbackHosts...)...)
	}
}

func phubListURL(host, plugin, search, model, sortKey string, c int, hd string, pg int) string {
	base := strings.TrimRight(host, "/") + "/"
	sep := "?"
	if search != "" {
		base += "video/search?search=" + url.QueryEscape(search)
		if sortKey != "" {
			base += "&o=" + url.QueryEscape(sortKey)
		}
		sep = "&"
	} else if model != "" {
		if strings.HasPrefix(model, "pornstar/") {
			base += model + "/videos/upload"
		} else {
			base += "model/" + model + "/videos"
		}
		sep = "?"
	} else {
		switch plugin {
		case "phubgay":
			base += "gay/video"
		case "phubsml":
			base += "transgender"
		default:
			base += "video"
		}
		if sortKey != "" {
			base += sep + "o=" + url.QueryEscape(sortKey)
			sep = "&"
		}
		if hd != "" {
			base += sep + "hd=" + url.QueryEscape(hd)
			sep = "&"
		}
		if c > 0 {
			base += sep + "c=" + strconv.Itoa(c)
			sep = "&"
		}
	}
	if pg > 1 {
		base += sep + "page=" + strconv.Itoa(pg)
	}
	return base
}

func parsePornHubPlaylist(host, plugin, html string) []map[string]any {
	segments := strings.Split(html, "vkey=\"")
	if len(segments) <= 1 {
		return []map[string]any{}
	}
	videoRoute := "/phub/vidosik"
	site := "phub"
	if plugin == "phubprem" {
		videoRoute = "/phubprem/vidosik"
		site = "phubprem"
	}

	out := make([]map[string]any, 0, len(segments)-1)
	for _, row := range segments {
		vkey := strings.TrimSpace(submatch1(phVKeyRe1, row))
		if vkey == "" {
			vkey = strings.TrimSpace(submatch1(phVKeyRe2, row))
		}
		if vkey == "" {
			continue
		}
		title := strings.TrimSpace(submatch1(phTitleRe1, row))
		if title == "" {
			title = strings.TrimSpace(submatch1(phTitleRe2, row))
		}
		if title == "" {
			title = strings.TrimSpace(submatch1(phTitleRe3, row))
		}
		img := strings.TrimSpace(submatch1(phImageRe1, row))
		if img == "" {
			img = strings.TrimSpace(submatch1(phImageRe2, row))
		}
		if title == "" || img == "" {
			continue
		}

		timeLabel := strings.TrimSpace(submatch1(phTimeRe1, row))
		if timeLabel == "" {
			timeLabel = strings.TrimSpace(submatch1(phTimeRe2, row))
		}
		if timeLabel == "" {
			timeLabel = strings.TrimSpace(submatch1(phTimeRe3, row))
		}
		if timeLabel == "" {
			timeLabel = strings.TrimSpace(submatch1(phTimeRe4, row))
		}

		pictureProxy := img
		if strings.Contains(img, "phncdn.com") {
			pictureProxy = host + "/proxy/" + url.QueryEscape(img)
		}

		item := map[string]any{
			"name":    title,
			"video":   host + videoRoute + "?vkey=" + url.QueryEscape(vkey),
			"picture": pictureProxy,
			"time":    timeLabel,
			"json":    true,
			"related": true,
			"bookmark": map[string]any{
				"site":  site,
				"href":  vkey,
				"image": img,
			},
		}

		modelID, modelName := "", ""
		if m := phModelRe1.FindStringSubmatch(row); len(m) > 2 {
			modelID, modelName = m[1], m[2]
		} else if m := phModelRe2.FindStringSubmatch(row); len(m) > 2 {
			modelID, modelName = m[1], m[2]
		}
		if modelID != "" && modelName != "" {
			item["model"] = map[string]any{
				"name": modelName,
				"uri":  host + "/" + plugin + "?model=" + url.QueryEscape(modelID),
			}
		}
		out = append(out, item)
	}
	return out
}

func parsePornHubPages(html string) int {
	if !strings.Contains(html, `class="page_number"`) {
		return 1
	}
	maxPage := 0
	for _, m := range phPageRe.FindAllStringSubmatch(html, -1) {
		if len(m) < 2 {
			continue
		}
		pg, _ := strconv.Atoi(m[1])
		if pg > maxPage {
			maxPage = pg
		}
	}
	if maxPage == 0 {
		return 1
	}
	if maxPage <= 4 {
		return maxPage
	}
	return 0
}

func phubMenu(host, plugin, search, sortKey string, c int, hd string) []map[string]any {
	base := host + "/" + plugin
	menu := []map[string]any{
		{
			"title":        "Поиск",
			"search_on":    "search_on",
			"playlist_url": base,
		},
	}
	if search != "" {
		qs := url.QueryEscape(search)
		menu = append(menu, map[string]any{
			"title":        "Сортировка",
			"playlist_url": "submenu",
			"submenu": []map[string]any{
				{"title": "Наиболее актуальное", "playlist_url": base + "?search=" + qs},
				{"title": "Новейшее", "playlist_url": base + "?search=" + qs + "&sort=mr"},
				{"title": "Лучшие", "playlist_url": base + "?search=" + qs + "&sort=tr"},
				{"title": "Больше просмотров", "playlist_url": base + "?search=" + qs + "&sort=mv"},
			},
		})
		return menu
	}

	buildURL := func(category int, sort, quality string) string {
		args := make([]string, 0, 3)
		if quality != "" {
			args = append(args, "hd="+url.QueryEscape(quality))
		}
		if category > 0 {
			args = append(args, "c="+strconv.Itoa(category))
		}
		if sort != "" {
			args = append(args, "sort="+url.QueryEscape(sort))
		}
		if len(args) == 0 {
			return base
		}
		return base + "?" + strings.Join(args, "&")
	}

	menu = append(menu, map[string]any{
		"title":        "Сортировка",
		"playlist_url": "submenu",
		"submenu": []map[string]any{
			{"title": "Недавно в избранном", "playlist_url": base + "?hd=" + hd + "&c=" + strconv.Itoa(c)},
			{"title": "Новейшее", "playlist_url": base + "?hd=" + hd + "&c=" + strconv.Itoa(c) + "&sort=cm"},
			{"title": "Самые горячие", "playlist_url": base + "?hd=" + hd + "&c=" + strconv.Itoa(c) + "&sort=ht"},
			{"title": "Лучшие", "playlist_url": base + "?hd=" + hd + "&c=" + strconv.Itoa(c) + "&sort=tr"},
		},
	})

	if plugin == "phubprem" {
		qualityTitle := "Все"
		switch hd {
		case "2":
			qualityTitle = "1080p"
		case "3":
			qualityTitle = "1440p"
		case "4":
			qualityTitle = "2160p"
		}
		menu = append(menu, map[string]any{
			"title":        "Качество: " + qualityTitle,
			"playlist_url": "submenu",
			"submenu": []map[string]any{
				{"title": "Все", "playlist_url": buildURL(c, sortKey, "")},
				{"title": "2160p", "playlist_url": buildURL(c, sortKey, "4")},
				{"title": "1440p", "playlist_url": buildURL(c, sortKey, "3")},
				{"title": "1080p", "playlist_url": buildURL(c, sortKey, "2")},
			},
		})
	}

	if plugin != "phubprem" {
		menu = append(menu, map[string]any{
			"title":        "Ориентация",
			"playlist_url": "submenu",
			"submenu": []map[string]any{
				{"title": "Гетеро", "playlist_url": host + "/phub"},
				{"title": "Геи", "playlist_url": host + "/phubgay"},
				{"title": "Трансы", "playlist_url": host + "/phubsml"},
			},
		})
	}

	type cat struct {
		title string
		id    int
	}
	var cats []cat
	switch plugin {
	case "phub", "phubprem":
		cats = []cat{
			{title: "Русское", id: 99},
			{title: "Азиатки", id: 1},
			{title: "Анал", id: 35},
			{title: "BDSM", id: 10},
			{title: "Большая грудь", id: 8},
			{title: "Блондинки", id: 9},
			{title: "Веб-камера", id: 61},
			{title: "Жесткий секс", id: 67},
			{title: "Зрелые", id: 28},
			{title: "Женский выбор", id: 73},
			{title: "60FPS", id: 105},
		}
	case "phubgay":
		cats = []cat{
			{title: "Азиаты", id: 48},
			{title: "Без презерватива", id: 40},
			{title: "Большие члены", id: 58},
			{title: "Грубый секс", id: 312},
			{title: "Кремпай", id: 71},
			{title: "Любительское", id: 252},
			{title: "Медведи", id: 66},
			{title: "Минет", id: 56},
			{title: "Негры", id: 44},
			{title: "На публике", id: 84},
		}
	}
	if len(cats) > 0 {
		active := "все"
		sub := make([]map[string]any, 0, len(cats)+1)
		sub = append(sub, map[string]any{
			"title":        "Все",
			"playlist_url": buildURL(0, sortKey, hd),
		})
		for _, cat := range cats {
			if cat.id == c {
				active = cat.title
			}
			sub = append(sub, map[string]any{
				"title":        cat.title,
				"playlist_url": buildURL(cat.id, sortKey, hd),
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

func (s *sisiPornHubSource) hostByPlugin(plugin string) string {
	if plugin == "phubprem" {
		return s.hostPrem
	}
	return s.host
}

func (s *sisiPornHubSource) hostCandidates(plugin string) []string {
	if plugin == "phubprem" {
		return sisiHostFallbacks(s.hostPrem, s.fallbackPremHosts...)
	}
	return sisiHostFallbacks(s.host, s.fallbackHosts...)
}

func sisiSourcePluginFromPath(path string) string {
	path = strings.Trim(strings.ToLower(path), "/")
	if path == "" {
		return ""
	}
	return strings.Split(path, "/")[0]
}

func sisiHostFallbacks(primary string, extra ...string) []string {
	uniq := make([]string, 0, 1+len(extra))
	seen := map[string]struct{}{}
	add := func(raw string) {
		h := strings.TrimSpace(strings.TrimRight(raw, "/"))
		if h == "" {
			return
		}
		if !strings.HasPrefix(h, "http://") && !strings.HasPrefix(h, "https://") {
			h = "https://" + h
		}
		if _, ok := seen[h]; ok {
			return
		}
		seen[h] = struct{}{}
		uniq = append(uniq, h)
	}

	add(primary)
	for _, h := range extra {
		add(h)
	}
	return uniq
}

func SisiSourceHost(key, fallback string) string {
	envKey := "LAMPAC_GO_SISI_" + strings.ToUpper(key) + "_HOST"
	if v := strings.TrimSpace(os.Getenv(envKey)); v != "" {
		return strings.TrimRight(v, "/")
	}
	data, ok := readFileAny("init.conf")
	if !ok {
		return strings.TrimRight(fallback, "/")
	}
	var root map[string]any
	if err := stdjson.Unmarshal(data, &root); err != nil {
		return strings.TrimRight(fallback, "/")
	}
	node, _ := root[key].(map[string]any)
	host := strings.TrimSpace(toString(node["host"]))
	if host == "" {
		return strings.TrimRight(fallback, "/")
	}
	return strings.TrimRight(host, "/")
}
