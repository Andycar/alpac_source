package sisihttp

import (
	"context"
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
	ptxHrefTitleRe  = regexp.MustCompile(`(?is)<a href="https?://[^/]+/(video/[^"]+)" title="([^"]+)"`)
	ptxImageRe      = regexp.MustCompile(`(?is)data-src="(https?:)?//(((ptx|statics)\.cdntrex\.com/contents/videos_screenshots/[0-9]+/[0-9]+)[^"]+)"`)
	ptxQualityRe    = regexp.MustCompile(`(?is)<span class="quality">([^<]+)</span>`)
	ptxTimeRe       = regexp.MustCompile(`(?is)<i class="fa fa-clock-o"></i>([^<]+)</div>`)
	ptxQualityURLRe = regexp.MustCompile(`(?is)(https?://[^"'\s]+/get_file/[^"'\s]+?_([0-9]{3,4}p)\.mp4(?:\?[^"'\s]+)?)`)
	ptxAutoURLRe    = regexp.MustCompile(`(?is)(https?://[^"'\s]+/get_file/[^"'\s]+?\.mp4(?:\?[^"'\s]+)?)`)
	ptxURLQualityRe = regexp.MustCompile(`(?is)_([0-9]{3,4}p)\.mp4`)

	sbgItemSplitRe    = regexp.MustCompile(`(?is)(?:data-testid="video-item"|class="(?:[^"]*\s)?video-item(?:\s[^"]*)?")`)
	sbgHrefTitleRe    = regexp.MustCompile(`(?is)<a href="/([^"]+)" title="([^"]+)"`)
	sbgHrefRe         = regexp.MustCompile(`(?is)<a[^>]+href="/([^"]+)"[^>]*>`)
	sbgAnyAnchorRe    = regexp.MustCompile(`(?is)<a[^>]+href="/([^"]+)"[^>]*>`)
	sbgAnyTitleRe     = regexp.MustCompile(`(?is)\b(?:title|aria-label|alt)="([^"]+)"`)
	sbgImgRe          = regexp.MustCompile(`(?is)\s+src="([^"]+)"`)
	sbgDataSrcRe      = regexp.MustCompile(`(?is)data-src="([^"]+)"`)
	sbgDataSrcsetRe   = regexp.MustCompile(`(?is)data-srcset="([^"]+)"`)
	sbgPreviewRe      = regexp.MustCompile(`(?is)data-preview="([^"]+)"`)
	sbgSourceDataRe   = regexp.MustCompile(`(?is)<source data-src="([^"]+)"`)
	sbgQualityRe      = regexp.MustCompile(`(?is)"video-item-resolution">([^<]+)</span>`)
	sbgTimeRe         = regexp.MustCompile(`(?is)"video-item-length">([^<]+)</span>`)
	sbgStreamLinksRe  = regexp.MustCompile(`(?is)'([0-9]+)(p|k)': ?\['(https?://[^']+)`)
	sbgWidthReplaceRe = regexp.MustCompile(`/w:[0-9]00/`)

	runetkiRowSplitRe    = regexp.MustCompile(`(?is)"gender"`)
	runetkiUsernameRe    = regexp.MustCompile(`(?is)"username":"([^"]+)"`)
	runetkiEsidRe        = regexp.MustCompile(`(?is)"esid":"([^"]+)"`)
	runetkiThumbImageRe  = regexp.MustCompile(`(?is)"thumb_image":"([^"]+)"`)
	runetkiDisplayNameRe = regexp.MustCompile(`(?is)"display_name":"([^"]+)"`)
	runetkiQualityRe     = regexp.MustCompile(`(?is)"vq":"([^"]+)"`)
	runetkiTotalCountRe  = regexp.MustCompile(`(?is)"total_count":([0-9]+),`)

	validPtxPlugin     = map[string]struct{}{"ptx": {}}
	validSbgPlugin     = map[string]struct{}{"sbg": {}}
	validRunetkiPlugin = map[string]struct{}{"runetki": {}}
)

type sisiPorntrexSource struct {
	cfg    config.Config
	client *http.Client
	host   string
}

type sisiSpankbangSource struct {
	cfg           config.Config
	client        *http.Client
	clientDirect  *http.Client
	host          string
	fallbackHosts []string
}

type sisiRunetkiSource struct {
	cfg    config.Config
	client *http.Client
	host   string
}

func newSisiPorntrexSource(cfg config.Config) *sisiPorntrexSource {
	return &sisiPorntrexSource{
		cfg:    cfg,
		client: httpclient.NewProxied(12 * time.Second),
		host:   SisiSourceHost("Porntrex", "https://www.porntrex.com"),
	}
}

func newSisiSpankbangSource(cfg config.Config) *sisiSpankbangSource {
	host := SisiSourceHost("Spankbang", "https://ru.spankbang.com")
	return &sisiSpankbangSource{
		cfg:           cfg,
		client:        httpclient.NewProxied(8 * time.Second),
		clientDirect:  httpclient.New(8 * time.Second),
		host:          host,
		fallbackHosts: sisiHostFallbacks(host, strings.Replace(host, "ru.spankbang.com", "spankbang.com", 1), strings.Replace(host, "ru.spankbang.com", "www.spankbang.com", 1), "https://spankbang.com", "https://www.spankbang.com"),
	}
}

func newSisiRunetkiSource(cfg config.Config) *sisiRunetkiSource {
	return &sisiRunetkiSource{
		cfg:    cfg,
		client: httpclient.NewProxied(12 * time.Second),
		host:   SisiSourceHost("Runetki", "https://rus.runetki5.com"),
	}
}

func (s *sisiPorntrexSource) listHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		plugin := sisiSourcePluginFromPath(r.URL.Path)
		if _, ok := validPtxPlugin[plugin]; !ok {
			sisiListStubHandler(s.cfg).ServeHTTP(w, r)
			return
		}

		search := strings.TrimSpace(r.URL.Query().Get("search"))
		sortKey := strings.TrimSpace(r.URL.Query().Get("sort"))
		c := strings.TrimSpace(r.URL.Query().Get("c"))
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

		html, err := s.fetchHTML(r.Context(), s.listURL(search, sortKey, c, pg))
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"menu":        ptxMenu(hostFromRequest(r), search, sortKey, c),
				"list":        []any{},
				"total_pages": 1,
			})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"menu":        ptxMenu(hostFromRequest(r), search, sortKey, c),
			"list":        parsePorntrexPlaylist(hostFromRequest(r), html),
			"total_pages": 1,
		})
	}
}

func (s *sisiPorntrexSource) viewHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		uri := strings.TrimSpace(r.URL.Query().Get("uri"))
		if uri == "" {
			writeJSON(w, http.StatusOK, map[string]any{})
			return
		}

		html, err := s.fetchHTML(r.Context(), s.streamURL(uri))
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{})
			return
		}

		links := parsePorntrexStreamLinks(html)
		if len(links) == 0 {
			writeJSON(w, http.StatusOK, map[string]any{})
			return
		}

		out := make(map[string]any, len(links))
		for q, link := range links {
			out[q] = hostFromRequest(r) + "/ptx/strem?link=" + url.QueryEscape(link)
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func (s *sisiPorntrexSource) streamHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		link := strings.TrimSpace(r.URL.Query().Get("link"))
		if link == "" {
			http.Error(w, "missing link", http.StatusBadRequest)
			return
		}
		link = normalizePorntrexURL(link)
		link = resolveRedirectLocation(r.Context(), link)

		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, link, nil)
		if err != nil {
			http.Error(w, "bad link", http.StatusBadRequest)
			return
		}

		ua := strings.TrimSpace(r.Header.Get("User-Agent"))
		if ua == "" {
			ua = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
		}
		req.Header.Set("User-Agent", ua)
		req.Header.Set("Accept", "*/*")
		req.Header.Set("Referer", strings.TrimRight(s.host, "/")+"/")
		if u, err := url.Parse(strings.TrimRight(s.host, "/")); err == nil && u.Scheme != "" && u.Host != "" {
			req.Header.Set("Origin", u.Scheme+"://"+u.Host)
		}
		if rng := strings.TrimSpace(r.Header.Get("Range")); rng != "" {
			req.Header.Set("Range", rng)
		}
		req.Header.Set("X-Lampac-Go", "1")

		var (
			resp  *http.Response
			doErr error
		)
		for _, client := range []*http.Client{
			httpclient.NewProxied(0),
			httpclient.New(0),
		} {
			resp, doErr = client.Do(req.Clone(r.Context()))
			if doErr == nil && resp != nil {
				break
			}
		}
		if doErr != nil || resp == nil {
			// Final fallback: let existing /proxy pipeline try this URL.
			http.Redirect(w, r, hostFromRequest(r)+"/proxy/"+url.QueryEscape(link), http.StatusFound)
			return
		}
		defer resp.Body.Close()

		for _, k := range []string{
			"Content-Type",
			"Content-Length",
			"Content-Range",
			"Accept-Ranges",
			"Cache-Control",
			"ETag",
			"Last-Modified",
		} {
			if v := strings.TrimSpace(resp.Header.Get(k)); v != "" {
				w.Header().Set(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}
}

func (s *sisiSpankbangSource) listHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		plugin := sisiSourcePluginFromPath(r.URL.Path)
		if _, ok := validSbgPlugin[plugin]; !ok {
			sisiListStubHandler(s.cfg).ServeHTTP(w, r)
			return
		}

		search := strings.TrimSpace(r.URL.Query().Get("search"))
		sortKey := strings.TrimSpace(r.URL.Query().Get("sort"))
		pg := sisiIntOrDefault(r.URL.Query().Get("pg"), 1)
		if pg <= 0 {
			pg = 1
		}

		// Unified category filter (search fallback).
		if cat := r.URL.Query().Get("cat"); cat != "" {
			res := resolveSisiCategory(plugin, cat)
			if res.SearchTerm != "" {
				search = res.SearchTerm
			}
		}

		var (
			list      []map[string]any
			fetchedOK bool
		)
		for _, srcHost := range s.hostCandidates() {
			html, err := s.fetchHTML(r.Context(), s.listURLForHost(srcHost, search, sortKey, pg))
			if err != nil {
				continue
			}
			fetchedOK = true
			list = parseSpankbangPlaylist(hostFromRequest(r), html)
			if len(list) > 0 {
				break
			}
		}
		if !fetchedOK {
			writeJSON(w, http.StatusOK, map[string]any{
				"menu":        sbgMenu(hostFromRequest(r), sortKey),
				"list":        []any{},
				"total_pages": 1,
			})
			return
		}

		resp := map[string]any{
			"list":        list,
			"total_pages": 1,
		}
		if search == "" {
			resp["menu"] = sbgMenu(hostFromRequest(r), sortKey)
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func (s *sisiSpankbangSource) viewHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		uri := strings.TrimSpace(r.URL.Query().Get("uri"))
		if uri == "" {
			writeJSON(w, http.StatusOK, map[string]any{"qualitys": map[string]any{}, "recomends": []any{}})
			return
		}

		var (
			html string
			err  error
		)
		for _, srcHost := range s.hostCandidates() {
			html, err = s.fetchHTML(r.Context(), s.streamURLForHost(srcHost, uri))
			if err == nil && html != "" {
				break
			}
		}
		if err != nil || html == "" {
			writeJSON(w, http.StatusOK, map[string]any{"qualitys": map[string]any{}, "recomends": []any{}})
			return
		}

		if parseBoolParam(r.URL.Query().Get("related")) {
			writeJSON(w, http.StatusOK, map[string]any{
				"list":        parseSpankbangPlaylist(hostFromRequest(r), html),
				"total_pages": 1,
			})
			return
		}

		qualitys := parseSpankbangQualityLinks(html)
		writeJSON(w, http.StatusOK, map[string]any{
			"qualitys":       qualitys,
			"qualitys_proxy": sisiProxyQualitys(r, qualitys),
			"recomends":      parseSpankbangPlaylist(hostFromRequest(r), html),
		})
	}
}

func (s *sisiRunetkiSource) listHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		plugin := sisiSourcePluginFromPath(r.URL.Path)
		if _, ok := validRunetkiPlugin[plugin]; !ok {
			sisiListStubHandler(s.cfg).ServeHTTP(w, r)
			return
		}

		search := strings.TrimSpace(r.URL.Query().Get("search"))
		sortKey := strings.TrimSpace(r.URL.Query().Get("sort"))
		pg := sisiIntOrDefault(r.URL.Query().Get("pg"), 1)
		if pg <= 0 {
			pg = 1
		}

		if search != "" {
			writeJSON(w, http.StatusOK, map[string]any{
				"menu":        runetkiMenu(hostFromRequest(r), sortKey),
				"list":        []any{},
				"total_pages": 1,
			})
			return
		}

		html, err := s.fetchHTML(r.Context(), s.listURL(sortKey, pg))
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"menu":        runetkiMenu(hostFromRequest(r), sortKey),
				"list":        []any{},
				"total_pages": 1,
			})
			return
		}

		list, totalPages := parseRunetkiPlaylist(html)
		if totalPages <= 0 {
			totalPages = 1
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"menu":        runetkiMenu(hostFromRequest(r), sortKey),
			"list":        list,
			"total_pages": totalPages,
		})
	}
}

func (s *sisiPorntrexSource) fetchHTML(ctx context.Context, target string) (string, error) {
	return sisiFetchHTML(ctx, s.client, target)
}

func (s *sisiSpankbangSource) fetchHTML(ctx context.Context, target string) (string, error) {
	if html, err := sisiFetchHTML(ctx, s.client, target); err == nil {
		return html, nil
	}
	return sisiFetchHTML(ctx, s.clientDirect, target)
}

func (s *sisiRunetkiSource) fetchHTML(ctx context.Context, target string) (string, error) {
	return sisiFetchHTML(ctx, s.client, target)
}

func (s *sisiPorntrexSource) listURL(search, sortKey, c string, pg int) string {
	host := strings.TrimRight(s.host, "/")
	if search != "" {
		path := host + "/search/" + url.QueryEscape(search) + "/"
		if sortKey != "" {
			path += sortKey + "/"
		}
		return path + "?from_videos=" + strconv.Itoa(pg)
	}
	if c != "" {
		path := host + "/categories/" + url.PathEscape(c) + "/"
		if sortKey == "most-popular" {
			path += "top-rated/"
		}
		return path + "?from4=" + strconv.Itoa(pg)
	}
	if sortKey == "" {
		return host + "/latest-updates/" + strconv.Itoa(pg) + "/"
	}
	return host + "/" + sortKey + "/weekly/?from4=" + strconv.Itoa(pg)
}

func (s *sisiPorntrexSource) streamURL(uri string) string {
	return strings.TrimRight(s.host, "/") + "/" + strings.TrimLeft(strings.TrimSpace(uri), "/")
}

func (s *sisiSpankbangSource) listURL(search, sortKey string, pg int) string {
	return s.listURLForHost(s.host, search, sortKey, pg)
}

func (s *sisiSpankbangSource) listURLForHost(host, search, sortKey string, pg int) string {
	host = strings.TrimRight(host, "/")
	if search != "" {
		return host + "/s/" + url.QueryEscape(search) + "/" + strconv.Itoa(pg) + "/"
	}
	if sortKey == "" {
		sortKey = "new_videos"
	}
	base := host + "/" + sortKey + "/" + strconv.Itoa(pg) + "/"
	if sortKey == "most_popular" {
		return base + "?p=m"
	}
	return base
}

func (s *sisiSpankbangSource) streamURL(uri string) string {
	return s.streamURLForHost(s.host, uri)
}

func (s *sisiSpankbangSource) streamURLForHost(host, uri string) string {
	return strings.TrimRight(host, "/") + "/" + strings.TrimLeft(strings.TrimSpace(uri), "/")
}

func (s *sisiRunetkiSource) listURL(sortKey string, pg int) string {
	host := strings.TrimRight(s.host, "/")
	if sortKey == "" {
		sortKey = "all"
	}
	offset := 0
	if pg > 1 {
		offset = (pg - 1) * 72
	}
	return host + "/tools/listing_v3.php?livetab=" + url.QueryEscape(sortKey) + "&offset=" + strconv.Itoa(offset) + "&limit=72"
}

func parsePorntrexPlaylist(host, html string) []map[string]any {
	parts := strings.Split(html, `<div class="video-preview-screen`)
	if len(parts) <= 1 {
		return []map[string]any{}
	}

	out := make([]map[string]any, 0, len(parts)-1)
	for i := 1; i < len(parts); i++ {
		row := parts[i]
		if strings.Contains(row, `<span class="line-private">`) {
			continue
		}
		m := ptxHrefTitleRe.FindStringSubmatch(row)
		if len(m) < 3 {
			continue
		}
		uri := strings.TrimSpace(m[1])
		title := strings.TrimSpace(m[2])
		if uri == "" || title == "" {
			continue
		}

		imgm := ptxImageRe.FindStringSubmatch(row)
		if len(imgm) < 3 {
			continue
		}
		img := "https://" + strings.TrimSpace(imgm[2])
		out = append(out, map[string]any{
			"video":   host + "/ptx/vidosik?uri=" + url.QueryEscape(uri),
			"name":    title,
			"picture": img,
			"quality": strings.TrimSpace(submatch1(ptxQualityRe, row)),
			"time":    strings.TrimSpace(submatch1(ptxTimeRe, row)),
			"json":    true,
			"bookmark": map[string]any{
				"site":  "ptx",
				"href":  uri,
				"image": img,
			},
		})
	}
	return out
}

func parsePorntrexStreamLinks(html string) map[string]string {
	html = strings.ReplaceAll(html, `\/`, `/`)
	html = strings.ReplaceAll(html, `\\u0026`, "&")
	html = strings.ReplaceAll(html, `\u0026`, "&")

	out := map[string]string{}
	for _, m := range ptxQualityURLRe.FindAllStringSubmatch(html, -1) {
		if len(m) < 3 {
			continue
		}
		link := normalizePorntrexURL(m[1])
		q := strings.TrimSpace(m[2])
		if link == "" || q == "" {
			continue
		}
		out[q] = link
	}
	if len(out) == 0 {
		for _, m := range ptxAutoURLRe.FindAllStringSubmatch(html, -1) {
			if len(m) < 2 {
				continue
			}
			link := normalizePorntrexURL(m[1])
			if link == "" {
				continue
			}
			if qm := ptxURLQualityRe.FindStringSubmatch(link); len(qm) >= 2 {
				out[strings.ToLower(strings.TrimSpace(qm[1]))] = link
				continue
			}
			out["auto"] = link
		}
		if len(out) == 0 {
			if auto := normalizePorntrexURL(submatch1(ptxAutoURLRe, html)); auto != "" {
				out["auto"] = auto
			}
		}
	}
	return out
}

func normalizePorntrexURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	raw = strings.Trim(raw, `"'`)
	raw = strings.ReplaceAll(raw, `\/`, `/`)
	raw = strings.ReplaceAll(raw, `\\u0026`, "&")
	raw = strings.ReplaceAll(raw, `\u0026`, "&")
	return raw
}

func ptxMenu(host, search, sortKey, c string) []map[string]any {
	base := host + "/ptx"
	items := []map[string]any{
		{
			"title":        "Поиск",
			"search_on":    "search_on",
			"playlist_url": base,
		},
	}

	if search != "" {
		qs := url.QueryEscape(search)
		title := "Most Relevant"
		if sortKey != "" {
			title = sortKey
		}
		items = append(items, map[string]any{
			"title":        "Сортировка: " + title,
			"playlist_url": "submenu",
			"submenu": []map[string]any{
				{"title": "Most Relevant", "playlist_url": base + "?c=" + c + "&search=" + qs},
				{"title": "Новинки", "playlist_url": base + "?c=" + c + "&sort=latest-updates&search=" + qs},
				{"title": "Топ просмотров", "playlist_url": base + "?c=" + c + "&sort=most-popular&search=" + qs},
			},
		})
		return items
	}

	title := "новинки"
	if sortKey != "" {
		title = sortKey
	}
	items = append(items, map[string]any{
		"title":        "Сортировка: " + title,
		"playlist_url": "submenu",
		"submenu": []map[string]any{
			{"title": "Новинки", "playlist_url": base + "?c=" + c},
			{"title": "Топ просмотров", "playlist_url": base + "?c=" + c + "&sort=most-popular"},
		},
	})

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
	type cat struct {
		title string
		value string
	}
	cats := []cat{
		{title: "4K UHD", value: "4k-porn"},
		{title: "Anal", value: "anal"},
		{title: "Asian", value: "asian"},
		{title: "Big Ass", value: "big-ass"},
		{title: "Big Tits", value: "big-tits"},
		{title: "Blowjob", value: "blowjob"},
		{title: "MILF", value: "milf"},
		{title: "Lesbian", value: "lesbian"},
		{title: "Teen", value: "teen"},
		{title: "BBW", value: "bbw"},
		{title: "Hentai", value: "hentai"},
		{title: "Threesome", value: "threesome"},
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
	items = append(items, map[string]any{
		"title":        "Категория: " + active,
		"playlist_url": "submenu",
		"submenu":      sub,
	})

	return items
}

func parseSpankbangPlaylist(host, html string) []map[string]any {
	parts := sbgItemSplitRe.Split(html, -1)
	out := make([]map[string]any, 0, max(len(parts)-1, 0))
	seen := make(map[string]struct{}, len(parts))
	for i := 1; i < len(parts); i++ {
		row := parts[i]
		m := sbgHrefTitleRe.FindStringSubmatch(row)
		href, title := "", ""
		if len(m) >= 3 {
			href = strings.TrimSpace(m[1])
			title = strings.TrimSpace(m[2])
		}
		if href == "" {
			href = strings.TrimSpace(submatch1(sbgHrefRe, row))
		}
		href = sisiNormalizePathFromHref(href)
		if title == "" {
			title = strings.TrimSpace(submatch1(sbgAnyTitleRe, row))
		}
		if href == "" || title == "" {
			continue
		}
		if _, ok := seen[href]; ok {
			continue
		}

		img := strings.TrimSpace(submatch1(sbgImgRe, row))
		if !strings.Contains(img, "/w:") {
			img = strings.TrimSpace(submatch1(sbgDataSrcRe, row))
		}
		if img == "" {
			img = firstSrcsetURL(strings.TrimSpace(submatch1(sbgDataSrcsetRe, row)))
		}
		if img == "" {
			continue
		}
		img = sbgWidthReplaceRe.ReplaceAllString(img, "/w:300/")
		img = strings.ReplaceAll(img, "http://", "https://")

		preview := strings.TrimSpace(submatch1(sbgPreviewRe, row))
		if preview == "" {
			preview = strings.TrimSpace(submatch1(sbgSourceDataRe, row))
		}
		seen[href] = struct{}{}

		out = append(out, map[string]any{
			"name":    title,
			"video":   host + "/sbg/vidosik?uri=" + url.QueryEscape(href),
			"quality": strings.TrimSpace(submatch1(sbgQualityRe, row)),
			"picture": img,
			"preview": preview,
			"time":    strings.TrimSpace(submatch1(sbgTimeRe, row)),
			"json":    true,
			"related": true,
			"bookmark": map[string]any{
				"site":  "sbg",
				"href":  href,
				"image": img,
			},
		})
	}

	// Fallback for pages where video cards no longer expose data-testid="video-item".
	if len(out) == 0 {
		matches := sbgAnyAnchorRe.FindAllStringSubmatchIndex(html, -1)
		for _, m := range matches {
			if len(m) < 4 {
				continue
			}
			href := sisiNormalizePathFromHref(strings.TrimSpace(html[m[2]:m[3]]))
			if href == "" || strings.Contains(href, "javascript:") || strings.HasPrefix(href, "#") {
				continue
			}
			if _, ok := seen[href]; ok {
				continue
			}

			from := max(m[0]-450, 0)
			to := min(m[1]+1800, len(html))
			row := html[from:to]

			title := strings.TrimSpace(submatch1(sbgAnyTitleRe, html[m[0]:m[1]]))
			if title == "" {
				title = strings.TrimSpace(submatch1(sbgAnyTitleRe, row))
			}
			if title == "" {
				continue
			}

			img := strings.TrimSpace(submatch1(sbgImgRe, row))
			if !strings.Contains(img, "/w:") {
				img = strings.TrimSpace(submatch1(sbgDataSrcRe, row))
			}
			if img == "" {
				img = firstSrcsetURL(strings.TrimSpace(submatch1(sbgDataSrcsetRe, row)))
			}
			if img == "" {
				continue
			}
			img = sbgWidthReplaceRe.ReplaceAllString(img, "/w:300/")
			img = strings.ReplaceAll(img, "http://", "https://")

			preview := strings.TrimSpace(submatch1(sbgPreviewRe, row))
			if preview == "" {
				preview = strings.TrimSpace(submatch1(sbgSourceDataRe, row))
			}

			seen[href] = struct{}{}
			out = append(out, map[string]any{
				"name":    title,
				"video":   host + "/sbg/vidosik?uri=" + url.QueryEscape(href),
				"quality": strings.TrimSpace(submatch1(sbgQualityRe, row)),
				"picture": img,
				"preview": preview,
				"time":    strings.TrimSpace(submatch1(sbgTimeRe, row)),
				"json":    true,
				"related": true,
				"bookmark": map[string]any{
					"site":  "sbg",
					"href":  href,
					"image": img,
				},
			})
		}
	}
	return out
}

func (s *sisiSpankbangSource) hostCandidates() []string {
	return sisiHostFallbacks(s.host, s.fallbackHosts...)
}

func parseSpankbangQualityLinks(html string) map[string]any {
	type qitem struct {
		q    int
		link string
	}
	uniq := map[int]string{}
	for _, m := range sbgStreamLinksRe.FindAllStringSubmatch(html, -1) {
		if len(m) < 4 {
			continue
		}
		n, _ := strconv.Atoi(strings.TrimSpace(m[1]))
		sfx := strings.TrimSpace(m[2])
		if sfx == "k" {
			n = 2160
		}
		if n <= 0 {
			continue
		}
		uniq[n] = strings.TrimSpace(m[3])
	}
	if len(uniq) == 0 {
		return map[string]any{}
	}

	items := make([]qitem, 0, len(uniq))
	for q, link := range uniq {
		items = append(items, qitem{q: q, link: link})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].q > items[j].q })

	out := make(map[string]any, len(items))
	for _, it := range items {
		out[strconv.Itoa(it.q)+"p"] = it.link
	}
	return out
}

func sbgMenu(host, sortKey string) []map[string]any {
	base := host + "/sbg"
	sortTitle := "новое"
	if sortKey != "" {
		sortTitle = sortKey
	}
	return []map[string]any{
		{
			"title":        "Поиск",
			"search_on":    "search_on",
			"playlist_url": base,
		},
		{
			"title":        "Сортировка: " + sortTitle,
			"playlist_url": "submenu",
			"submenu": []map[string]any{
				{"title": "Новое", "playlist_url": base},
				{"title": "Трендовое", "playlist_url": base + "?sort=trending_videos"},
				{"title": "Популярное", "playlist_url": base + "?sort=most_popular"},
			},
		},
	}
}

func parseRunetkiPlaylist(html string) ([]map[string]any, int) {
	parts := runetkiRowSplitRe.Split(html, -1)
	if len(parts) <= 1 {
		return []map[string]any{}, 1
	}

	out := make([]map[string]any, 0, len(parts)-1)
	for i := 1; i < len(parts); i++ {
		row := parts[i]
		username := strings.TrimSpace(submatch1(runetkiUsernameRe, row))
		esid := strings.TrimSpace(submatch1(runetkiEsidRe, row))
		img := strings.TrimSpace(submatch1(runetkiThumbImageRe, row))
		if username == "" || esid == "" || img == "" {
			continue
		}
		title := strings.TrimSpace(submatch1(runetkiDisplayNameRe, row))
		if title == "" {
			title = username
		}
		img = "https:" + strings.ReplaceAll(strings.ReplaceAll(img, `\`, ""), "{ext}", "jpg")

		out = append(out, map[string]any{
			"name":    title,
			"quality": strings.TrimSpace(submatch1(runetkiQualityRe, row)),
			"video":   "https://" + esid + ".bcvcdn.com/hls/stream_" + username + "/playlist.m3u8",
			"picture": img,
		})
	}

	totalPages := 1
	if m := strings.TrimSpace(submatch1(runetkiTotalCountRe, html)); m != "" {
		if total, err := strconv.Atoi(m); err == nil && total > 0 {
			if total <= 72 {
				totalPages = 1
			} else {
				totalPages = (total / 72) + 1
			}
		}
	}
	return out, totalPages
}

func runetkiMenu(host, sortKey string) []map[string]any {
	base := host + "/runetki"
	title := "выбрать"
	if sortKey != "" {
		title = sortKey
	}
	return []map[string]any{
		{
			"title":        "Сортировка: " + title,
			"playlist_url": "submenu",
			"submenu": []map[string]any{
				{"title": "Новые", "playlist_url": base + "?sort=new"},
				{"title": "Пары", "playlist_url": base + "?sort=couples"},
				{"title": "Девушки", "playlist_url": base + "?sort=female"},
				{"title": "Парни", "playlist_url": base + "?sort=male"},
				{"title": "Транссексуалы", "playlist_url": base + "?sort=transsexual"},
			},
		},
	}
}

func resolveRedirectLocation(ctx context.Context, link string) string {
	client := httpclient.NewProxiedNoRedirect(10 * time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return link
	}
	resp, err := client.Do(req)
	if err != nil {
		return link
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		if loc := strings.TrimSpace(resp.Header.Get("Location")); loc != "" {
			if u, err := resp.Request.URL.Parse(loc); err == nil {
				return u.String()
			}
			return loc
		}
	}
	return link
}
