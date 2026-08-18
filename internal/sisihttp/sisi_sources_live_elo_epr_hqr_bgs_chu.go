package sisihttp

import (
	"context"
	stdjson "encoding/json"
	"fmt"
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
	eloSplitRe     = regexp.MustCompile(`(?is)<div class="item(?:\s|")`)
	eloLinkRe      = regexp.MustCompile(`(?is)<a[^>]+href="(?:https?://[^/]+/)?(video/[^"?#]+)`)
	eloAnyLinkRe   = regexp.MustCompile(`(?is)<a[^>]+href="([^"]+)"[^>]*>`)
	eloHrefAnyRe   = regexp.MustCompile(`(?is)<a[^>]+href="([^"]+)"`)
	eloTitleRe     = regexp.MustCompile(`(?is)<div class="item-title"[^>]*>([^<]+)</div>`)
	eloTitleAnyRe  = regexp.MustCompile(`(?is)\b(?:title|aria-label|alt)="([^"]+)"`)
	eloImgSrcRe    = regexp.MustCompile(`(?is)\s+(?:src|data-src|data-original)="([^"]+)"`)
	eloImgDataRe   = regexp.MustCompile(`(?is)(?:data-srcset|srcset)="([^"]+)"`)
	eloTimeRe      = regexp.MustCompile(`(?is)\sdata-eb="([^;"]+);`)
	eloVideoAltRe  = regexp.MustCompile(`(?is)video_alt_url:\s*['"]([^'"]+)`)
	eloVideoURLRe  = regexp.MustCompile(`(?is)video_url:\s*['"]([^'"]+)`)
	eprVidRe       = regexp.MustCompile(`(?is)(?:\bvid\b|EP\.video\.player\.vid)\s*=\s*'([^']+)'`)
	eprHashRe      = regexp.MustCompile(`(?is)(?:\bhash\b|EP\.video\.player\.hash)\s*=\s*'([^']+)'`)
	eprSrcQRe      = regexp.MustCompile(`(?is)"src":\s*"((?:https?:)?\\/\\/[^"]+-([0-9]+p)\.mp4)"`)
	eprURLQRe      = regexp.MustCompile(`(?is)(https?://[^"\\s]+-([0-9]{3,4}p)\.mp4)`)
	eprItemSplitRe = regexp.MustCompile(`(?is)<div class="mb(?: hdy)?"`)
	eprHrefRe      = regexp.MustCompile(`(?is)<p class="mbtit"><a href="/([^"]+)">([^<]+)</a>`)
	eprImgDataRe   = regexp.MustCompile(`(?is)\sdata-src="([^"]+)"`)
	eprImgSrcRe    = regexp.MustCompile(`(?is)<img src="([^"]+)"`)
	eprDataIDRe    = regexp.MustCompile(`(?is)data-id="([^"]+)"`)
	eprQualityRe   = regexp.MustCompile(`(?is)<div class="mvhdico"[^>]*><span>([^"<]+)`)
	eprTimeRe      = regexp.MustCompile(`(?is)<span class="mbtim"[^>]*>([^<]+)</span>`)
	hqrItemSplitRe = regexp.MustCompile(`(?is)<div class="img-container`)
	hqrItemRe      = regexp.MustCompile(`(?is)href="/([^"]+)" class="atfi[^"]*"><img src="//([^"]+)"[^>]+ alt="([^"]+)"`)
	hqrLinkRe      = regexp.MustCompile(`(?is)<a[^>]+href="(?:https?://[^/]+/)?([^"]+)"`)
	hqrImgAnyRe    = regexp.MustCompile(`(?is)(?:data-srcset|srcset|data-src|src|data-original)="([^"]+)"`)
	hqrTitleAnyRe  = regexp.MustCompile(`(?is)\b(?:alt|title)="([^"]+)"`)
	hqrTimeRe      = regexp.MustCompile(`(?is)class="fa fa-clock-o"[^>]*></i>\s*([^<]+)<`)
	hqrIframeRe    = regexp.MustCompile(`(?is)<iframe src="//([^/]+/video/[^/]+/)"`)
	hqrSrcTitleRe  = regexp.MustCompile(`(?is)src=.?"([^"]+)" title=.?"([^"]+)"`)
	chuSplitRe     = regexp.MustCompile(`(?is)display_age`)
	chuPublicRe    = regexp.MustCompile(`(?is)"current_show":"public"`)
	chuUserRe      = regexp.MustCompile(`(?is)"username":"([^"]+)"`)
	chuImgRe       = regexp.MustCompile(`(?is)"img":"([^"]+)"`)
	chuM3URe       = regexp.MustCompile(`(?is)(https?://[^ ]+/playlist\.m3u8)`)

	validEloPlugin = map[string]struct{}{"elo": {}}
	validEprPlugin = map[string]struct{}{"epr": {}}
	validHqrPlugin = map[string]struct{}{"hqr": {}}
	validBgsPlugin = map[string]struct{}{"bgs": {}}
	validChuPlugin = map[string]struct{}{"chu": {}}
)

type sisiEbalovoSource struct {
	cfg           config.Config
	client        *http.Client
	clientDirect  *http.Client
	host          string
	fallbackHosts []string
}

type sisiEpornerSource struct {
	cfg    config.Config
	client *http.Client
	host   string
}

type sisiHQpornerSource struct {
	cfg           config.Config
	client        *http.Client
	clientDirect  *http.Client
	host          string
	fallbackHosts []string
}

type sisiBongaSource struct {
	cfg    config.Config
	client *http.Client
	host   string
}

type sisiChaturbateSource struct {
	cfg    config.Config
	client *http.Client
	host   string
}

func newSisiEbalovoSource(cfg config.Config) *sisiEbalovoSource {
	host := SisiSourceHost("Ebalovo", "https://web.epalovo.com")
	return &sisiEbalovoSource{
		cfg:           cfg,
		client:        httpclient.NewForBalancer("ebalovo", 8*time.Second),
		clientDirect:  httpclient.New(8 * time.Second),
		host:          host,
		fallbackHosts: sisiHostFallbacks(host, "https://www.ebalovo.porn", "https://ebalovo.porn", "https://www.ebalovo.pro", "https://ebalovo.pro", "https://yabalovo.best"),
	}
}

func newSisiEpornerSource(cfg config.Config) *sisiEpornerSource {
	return &sisiEpornerSource{
		cfg:    cfg,
		client: httpclient.NewProxied(12 * time.Second),
		host:   SisiSourceHost("Eporner", "https://www.eporner.com"),
	}
}

func newSisiHQpornerSource(cfg config.Config) *sisiHQpornerSource {
	host := SisiSourceHost("HQporner", "https://m.hqporner.com")
	return &sisiHQpornerSource{
		cfg:           cfg,
		client:        httpclient.NewProxied(8 * time.Second),
		clientDirect:  httpclient.New(8 * time.Second),
		host:          host,
		fallbackHosts: sisiHostFallbacks(host, strings.Replace(host, "m.hqporner.com", "www.hqporner.com", 1), strings.Replace(host, "m.hqporner.com", "hqporner.com", 1), "https://www.hqporner.com", "https://hqporner.com"),
	}
}

func newSisiBongaSource(cfg config.Config) *sisiBongaSource {
	return &sisiBongaSource{
		cfg:    cfg,
		client: httpclient.NewProxied(12 * time.Second),
		host:   SisiSourceHost("BongaCams", "https://ee.bongacams.com"),
	}
}

func newSisiChaturbateSource(cfg config.Config) *sisiChaturbateSource {
	return &sisiChaturbateSource{
		cfg:    cfg,
		client: httpclient.NewProxied(12 * time.Second),
		host:   SisiSourceHost("Chaturbate", "https://chaturbate.com"),
	}
}

func (s *sisiEbalovoSource) listHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := validEloPlugin[sisiSourcePluginFromPath(r.URL.Path)]; !ok {
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
			res := resolveSisiCategory("elo", cat)
			if res.UseSearch {
				search = res.SearchTerm
			} else if res.CategoryValue != "" {
				c = res.CategoryValue
			}
		}

		var (
			list      []map[string]any
			fetchedOK bool
		)
		for _, srcHost := range s.hostCandidates() {
			html, err := s.fetchHTML(r.Context(), s.listURLForHost(srcHost, search, sortKey, c, pg))
			if err != nil {
				continue
			}
			fetchedOK = true
			list = parseEbalovoPlaylist(hostFromRequest(r), html)
			if len(list) > 0 {
				break
			}
		}
		if !fetchedOK {
			writeJSON(w, http.StatusOK, map[string]any{
				"menu":        eloMenu(hostFromRequest(r), sortKey, c),
				"list":        []any{},
				"total_pages": 1,
			})
			return
		}
		resp := map[string]any{"list": list, "total_pages": 1}
		if search == "" {
			resp["menu"] = eloMenu(hostFromRequest(r), sortKey, c)
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func (s *sisiEbalovoSource) viewHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		uri := strings.TrimSpace(r.URL.Query().Get("uri"))
		if uri == "" {
			writeJSON(w, http.StatusOK, map[string]any{"qualitys": map[string]any{}, "recomends": []any{}})
			return
		}

		var (
			recomends []map[string]any
			link      string
		)
		for _, srcHost := range s.hostCandidates() {
			html, err := s.fetchHTML(r.Context(), s.streamURLForHost(srcHost, uri))
			if err != nil {
				continue
			}
			recomends = parseEbalovoPlaylist(hostFromRequest(r), html)
			link = strings.TrimSpace(submatch1(eloVideoAltRe, html))
			if link == "" {
				link = strings.TrimSpace(submatch1(eloVideoURLRe, html))
			}
			link = strings.ReplaceAll(link, `\/`, `/`)
			if link != "" {
				link = resolveRedirectLocation(r.Context(), link)
				break
			}
		}

		if parseBoolParam(r.URL.Query().Get("related")) {
			writeJSON(w, http.StatusOK, map[string]any{
				"list":        recomends,
				"total_pages": 1,
			})
			return
		}

		qualitys := map[string]any{}
		if link != "" {
			qualitys["auto"] = link
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"qualitys":       qualitys,
			"qualitys_proxy": sisiProxyQualitys(r, qualitys),
			"recomends":      recomends,
		})
	}
}

func (s *sisiEpornerSource) listHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := validEprPlugin[sisiSourcePluginFromPath(r.URL.Path)]; !ok {
			sisiListStubHandler(s.cfg).ServeHTTP(w, r)
			return
		}

		search := strings.TrimSpace(r.URL.Query().Get("search"))
		sortKey := strings.TrimSpace(r.URL.Query().Get("sort"))
		c := strings.TrimSpace(r.URL.Query().Get("c"))
		pg := sisiIntOrDefault(r.URL.Query().Get("pg"), 1) + 1
		if pg <= 0 {
			pg = 2
		}

		// Unified category filter.
		if cat := r.URL.Query().Get("cat"); cat != "" {
			res := resolveSisiCategory("epr", cat)
			if res.UseSearch {
				search = res.SearchTerm
			} else if res.CategoryValue != "" {
				c = res.CategoryValue
			}
		}

		html, err := s.fetchHTML(r.Context(), s.listURL(search, sortKey, c, pg))
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"menu":        eprMenu(hostFromRequest(r), search, sortKey, c),
				"list":        []any{},
				"total_pages": 1,
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"menu":        eprMenu(hostFromRequest(r), search, sortKey, c),
			"list":        parseEpornerPlaylist(hostFromRequest(r), html),
			"total_pages": 1,
		})
	}
}

func (s *sisiEpornerSource) viewHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uri := strings.TrimSpace(r.URL.Query().Get("uri"))
		if uri == "" {
			writeJSON(w, http.StatusOK, map[string]any{"qualitys": map[string]any{}, "recomends": []any{}})
			return
		}

		page, err := s.fetchHTML(r.Context(), s.streamURL(uri))
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"qualitys": map[string]any{}, "recomends": []any{}})
			return
		}
		recomends := parseEpornerPlaylist(hostFromRequest(r), page)

		vid := strings.TrimSpace(submatch1(eprVidRe, page))
		hash := strings.TrimSpace(submatch1(eprHashRe, page))
		if vid == "" || hash == "" {
			writeJSON(w, http.StatusOK, map[string]any{"qualitys": map[string]any{}, "recomends": recomends})
			return
		}

		conv := epornerConvertHash(hash)
		if conv == "" {
			writeJSON(w, http.StatusOK, map[string]any{"qualitys": map[string]any{}, "recomends": recomends})
			return
		}
		domain := strings.TrimPrefix(strings.TrimPrefix(strings.TrimRight(s.host, "/"), "https://"), "http://")
		xhrURL := fmt.Sprintf("%s/xhr/video/%s?hash=%s&domain=%s&fallback=false&embed=false&supportedFormats=dash,mp4&_=%d",
			strings.TrimRight(s.host, "/"), vid, conv, domain, time.Now().Unix())

		xhrRaw, err := s.fetchHTML(r.Context(), xhrURL)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"qualitys": map[string]any{}, "recomends": recomends})
			return
		}
		qualitys := parseEpornerQualitys(xhrRaw)
		proxyQualities := sisiProxyQualitys(r, qualitys)
		respQualities := qualitys
		if len(proxyQualities) > 0 && sisiPreferProxyQualitys(r) {
			respQualities = proxyQualities
		}

		if parseBoolParam(r.URL.Query().Get("related")) {
			writeJSON(w, http.StatusOK, map[string]any{"list": recomends, "total_pages": 1})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"qualitys":       respQualities,
			"qualitys_proxy": proxyQualities,
			"recomends":      recomends,
		})
	}
}

func (s *sisiHQpornerSource) listHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := validHqrPlugin[sisiSourcePluginFromPath(r.URL.Path)]; !ok {
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
			res := resolveSisiCategory("hqr", cat)
			if res.UseSearch {
				search = res.SearchTerm
			} else if res.CategoryValue != "" {
				c = res.CategoryValue
			}
		}

		var (
			list      []map[string]any
			fetchedOK bool
		)
		for _, srcHost := range s.hostCandidates() {
			html, err := s.fetchHTML(r.Context(), s.listURLForHost(srcHost, search, sortKey, c, pg))
			if err != nil {
				continue
			}
			fetchedOK = true
			list = parseHQpornerPlaylist(hostFromRequest(r), html)
			if len(list) > 0 {
				break
			}
		}
		if !fetchedOK {
			writeJSON(w, http.StatusOK, map[string]any{
				"menu":        hqrMenu(hostFromRequest(r), sortKey, c),
				"list":        []any{},
				"total_pages": 1,
			})
			return
		}
		resp := map[string]any{"list": list, "total_pages": 1}
		if search == "" {
			resp["menu"] = hqrMenu(hostFromRequest(r), sortKey, c)
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func (s *sisiHQpornerSource) viewHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uri := strings.TrimSpace(r.URL.Query().Get("uri"))
		if uri == "" {
			writeJSON(w, http.StatusOK, map[string]any{})
			return
		}

		iframePage := ""
		iframeBaseURL := ""
		for _, srcHost := range s.hostCandidates() {
			page, err := s.fetchHTML(r.Context(), s.streamURLForHost(srcHost, uri))
			if err != nil {
				continue
			}
			iframePath := strings.TrimSpace(submatch1(hqrIframeRe, page))
			if iframePath == "" {
				continue
			}

			iframeURL := "https://" + iframePath
			pageRaw, err := s.fetchHTML(r.Context(), iframeURL)
			if err != nil {
				continue
			}
			iframePage = pageRaw
			iframeBaseURL = iframeURL
			break
		}
		if iframePage == "" {
			writeJSON(w, http.StatusOK, map[string]any{})
			return
		}

		type pair struct {
			q   string
			url string
		}
		pairs := make([]pair, 0, 6)
		for _, m := range hqrSrcTitleRe.FindAllStringSubmatch(iframePage, -1) {
			if len(m) < 3 {
				continue
			}
			link := strings.TrimSpace(strings.ReplaceAll(m[1], `\`, ""))
			title := strings.TrimSpace(strings.ReplaceAll(m[2], `\`, ""))
			if link == "" || title == "" || strings.Contains(strings.ToLower(title), "default") {
				continue
			}
			if strings.HasPrefix(link, "//") {
				link = "https:" + link
			} else if strings.HasPrefix(link, "/") {
				if u, err := url.Parse(iframeBaseURL); err == nil {
					link = u.Scheme + "://" + u.Host + link
				}
			}
			pairs = append(pairs, pair{q: title, url: link})
		}
		if len(pairs) == 0 {
			writeJSON(w, http.StatusOK, map[string]any{})
			return
		}
		sort.Slice(pairs, func(i, j int) bool { return pairs[i].q > pairs[j].q })
		out := make(map[string]any, len(pairs))
		for _, p := range pairs {
			out[p.q] = p.url
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func (s *sisiBongaSource) listHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := validBgsPlugin[sisiSourcePluginFromPath(r.URL.Path)]; !ok {
			sisiListStubHandler(s.cfg).ServeHTTP(w, r)
			return
		}
		if strings.TrimSpace(r.URL.Query().Get("search")) != "" {
			writeJSON(w, http.StatusOK, map[string]any{"menu": bgsMenu(hostFromRequest(r), ""), "list": []any{}, "total_pages": 1})
			return
		}

		sortKey := strings.TrimSpace(r.URL.Query().Get("sort"))
		pg := sisiIntOrDefault(r.URL.Query().Get("pg"), 1)
		if pg <= 0 {
			pg = 1
		}

		html, err := s.fetchHTML(r.Context(), s.listURL(sortKey, pg))
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"menu":        bgsMenu(hostFromRequest(r), sortKey),
				"list":        []any{},
				"total_pages": 1,
			})
			return
		}

		list, total := parseBongaPlaylist(html)
		if total <= 0 {
			total = 1
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"menu":        bgsMenu(hostFromRequest(r), sortKey),
			"list":        list,
			"total_pages": total,
		})
	}
}

func (s *sisiChaturbateSource) listHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := validChuPlugin[sisiSourcePluginFromPath(r.URL.Path)]; !ok {
			sisiListStubHandler(s.cfg).ServeHTTP(w, r)
			return
		}
		if strings.TrimSpace(r.URL.Query().Get("search")) != "" {
			writeJSON(w, http.StatusOK, map[string]any{"menu": chuMenu(hostFromRequest(r), ""), "list": []any{}, "total_pages": 1})
			return
		}

		sortKey := strings.TrimSpace(r.URL.Query().Get("sort"))
		pg := sisiIntOrDefault(r.URL.Query().Get("pg"), 1)
		if pg <= 0 {
			pg = 1
		}
		html, err := s.fetchHTML(r.Context(), s.listURL(sortKey, pg))
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"menu":        chuMenu(hostFromRequest(r), sortKey),
				"list":        []any{},
				"total_pages": 1,
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"menu":        chuMenu(hostFromRequest(r), sortKey),
			"list":        parseChaturbatePlaylist(hostFromRequest(r), html),
			"total_pages": 1,
		})
	}
}

func (s *sisiChaturbateSource) viewHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		baba := strings.TrimSpace(r.URL.Query().Get("baba"))
		if baba == "" {
			writeJSON(w, http.StatusOK, map[string]any{})
			return
		}
		page, err := s.fetchHTML(r.Context(), s.streamURL(baba))
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{})
			return
		}
		hls := strings.TrimSpace(submatch1(chuM3URe, page))
		if hls == "" {
			writeJSON(w, http.StatusOK, map[string]any{})
			return
		}
		hls = strings.ReplaceAll(strings.ReplaceAll(hls, `\u002D`, "-"), `\`, "")
		writeJSON(w, http.StatusOK, map[string]any{"auto": hls})
	}
}

func (s *sisiEbalovoSource) fetchHTML(ctx context.Context, target string) (string, error) {
	if html, err := sisiFetchHTML(ctx, s.client, target); err == nil {
		return html, nil
	}
	return sisiFetchHTML(ctx, s.clientDirect, target)
}

func (s *sisiEpornerSource) fetchHTML(ctx context.Context, target string) (string, error) {
	return sisiFetchHTML(ctx, s.client, target)
}

func (s *sisiHQpornerSource) fetchHTML(ctx context.Context, target string) (string, error) {
	if html, err := sisiFetchHTML(ctx, s.client, target); err == nil {
		return html, nil
	}
	return sisiFetchHTML(ctx, s.clientDirect, target)
}

func (s *sisiBongaSource) fetchHTML(ctx context.Context, target string) (string, error) {
	return sisiFetchHTML(ctx, s.client, target)
}

func (s *sisiChaturbateSource) fetchHTML(ctx context.Context, target string) (string, error) {
	return sisiFetchHTML(ctx, s.client, target)
}

func (s *sisiEbalovoSource) listURL(search, sortKey, c string, pg int) string {
	return s.listURLForHost(s.host, search, sortKey, c, pg)
}

func (s *sisiEbalovoSource) listURLForHost(host, search, sortKey, c string, pg int) string {
	host = strings.TrimRight(host, "/")
	if search != "" {
		path := host + "/search/" + url.QueryEscape(search) + "/"
		if pg > 1 {
			path += strconv.Itoa(pg) + "/"
		}
		return path
	}

	path := host + "/"
	if c != "" {
		path += "porno/" + url.PathEscape(c)
		if sortKey == "porno-online" || sortKey == "xxx-top" {
			path += "-rating"
		}
		path += "/"
	} else if sortKey != "" {
		path += sortKey + "/"
	}
	if pg > 1 {
		path += strconv.Itoa(pg) + "/"
	}
	return path
}

func (s *sisiEbalovoSource) streamURL(uri string) string {
	return s.streamURLForHost(s.host, uri)
}

func (s *sisiEbalovoSource) streamURLForHost(host, uri string) string {
	return strings.TrimRight(host, "/") + "/" + strings.TrimLeft(strings.TrimSpace(uri), "/")
}

func (s *sisiEpornerSource) listURL(search, sortKey, c string, pg int) string {
	host := strings.TrimRight(s.host, "/")
	if search != "" {
		path := host + "/search/" + url.QueryEscape(search) + "/"
		if pg > 1 {
			path += strconv.Itoa(pg) + "/"
		}
		if sortKey != "" {
			path += sortKey + "/"
		}
		return path
	}
	path := host + "/"
	if c != "" {
		path += "cat/" + url.PathEscape(c) + "/"
		if pg > 1 {
			path += strconv.Itoa(pg) + "/"
		}
		return path
	}
	if pg > 1 {
		path += strconv.Itoa(pg) + "/"
	}
	if sortKey != "" {
		path += sortKey + "/"
	}
	return path
}

func (s *sisiEpornerSource) streamURL(uri string) string {
	return strings.TrimRight(s.host, "/") + "/" + strings.TrimLeft(strings.TrimSpace(uri), "/")
}

func (s *sisiHQpornerSource) listURL(search, sortKey, c string, pg int) string {
	return s.listURLForHost(s.host, search, sortKey, c, pg)
}

func (s *sisiHQpornerSource) listURLForHost(host, search, sortKey, c string, pg int) string {
	host = strings.TrimRight(host, "/")
	if search != "" {
		return host + "/?q=" + url.QueryEscape(search) + "&p=" + strconv.Itoa(pg)
	}
	path := host + "/"
	if c != "" {
		path += "category/" + url.PathEscape(c)
	} else if sortKey != "" {
		path += "top/" + sortKey
	} else {
		path += "hdporn"
	}
	path += "/" + strconv.Itoa(pg)
	return path
}

func (s *sisiHQpornerSource) streamURL(uri string) string {
	return s.streamURLForHost(s.host, uri)
}

func (s *sisiHQpornerSource) streamURLForHost(host, uri string) string {
	return strings.TrimRight(host, "/") + "/" + strings.TrimLeft(strings.TrimSpace(uri), "/")
}

func (s *sisiBongaSource) listURL(sortKey string, pg int) string {
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

func (s *sisiChaturbateSource) listURL(sortKey string, pg int) string {
	host := strings.TrimRight(s.host, "/")
	path := host + "/api/ts/roomlist/room-list/?enable_recommendations=false&limit=90"
	if sortKey != "" {
		path += "&genders=" + url.QueryEscape(sortKey)
	}
	if pg > 1 {
		path += "&offset=" + strconv.Itoa(pg*90)
	}
	return path
}

func (s *sisiChaturbateSource) streamURL(baba string) string {
	return strings.TrimRight(s.host, "/") + "/" + strings.TrimLeft(strings.TrimSpace(baba), "/") + "/"
}

func parseEbalovoPlaylist(host, html string) []map[string]any {
	parts := eloSplitRe.Split(html, -1)
	out := make([]map[string]any, 0, max(len(parts)-1, 0))
	seen := make(map[string]struct{}, len(parts))

	for i := 1; i < len(parts); i++ {
		row := parts[i]
		link := strings.TrimSpace(submatch1(eloLinkRe, row))
		if link == "" {
			link = strings.TrimSpace(submatch1(eloHrefAnyRe, row))
		}
		link = sisiNormalizePathFromHref(link)
		if !isEbalovoVideoPath(link) {
			continue
		}
		title := strings.TrimSpace(submatch1(eloTitleRe, row))
		if title == "" {
			title = strings.TrimSpace(submatch1(eloTitleAnyRe, row))
		}
		if link == "" || title == "" {
			continue
		}
		if _, ok := seen[link]; ok {
			continue
		}
		img := strings.TrimSpace(submatch1(eloImgSrcRe, row))
		if strings.Contains(img, ",") {
			img = firstSrcsetURL(img)
		}
		if img == "" || strings.Contains(strings.ToLower(img), "load.png") {
			img = firstSrcsetURL(strings.TrimSpace(submatch1(eloImgDataRe, row)))
		}
		img = strings.TrimSpace(strings.SplitN(img, " ", 2)[0])
		if img == "" {
			continue
		}
		seen[link] = struct{}{}
		out = append(out, map[string]any{
			"name":    title,
			"video":   host + "/elo/vidosik?uri=" + url.QueryEscape(link),
			"picture": img,
			"time":    strings.TrimSpace(submatch1(eloTimeRe, row)),
			"json":    true,
			"related": true,
			"bookmark": map[string]any{
				"site":  "elo",
				"href":  link,
				"image": img,
			},
		})
	}

	// Fallback for updated markup where old card split/classes disappeared.
	if len(out) == 0 {
		matches := eloAnyLinkRe.FindAllStringSubmatchIndex(html, -1)
		for _, m := range matches {
			if len(m) < 4 {
				continue
			}
			link := sisiNormalizePathFromHref(strings.TrimSpace(html[m[2]:m[3]]))
			if link == "" || !isEbalovoVideoPath(link) {
				continue
			}
			if _, ok := seen[link]; ok {
				continue
			}

			from := max(m[0]-700, 0)
			to := min(m[1]+2200, len(html))
			row := html[from:to]
			title := strings.TrimSpace(submatch1(eloTitleRe, row))
			if title == "" {
				title = strings.TrimSpace(submatch1(eloTitleAnyRe, html[m[0]:m[1]]))
			}
			if title == "" {
				title = strings.TrimSpace(submatch1(eloTitleAnyRe, row))
			}
			img := strings.TrimSpace(submatch1(eloImgSrcRe, row))
			if strings.Contains(img, ",") {
				img = firstSrcsetURL(img)
			}
			if img == "" || strings.Contains(strings.ToLower(img), "load.png") {
				img = firstSrcsetURL(strings.TrimSpace(submatch1(eloImgDataRe, row)))
			}
			img = strings.TrimSpace(strings.SplitN(img, " ", 2)[0])
			if title == "" || img == "" {
				continue
			}
			seen[link] = struct{}{}
			out = append(out, map[string]any{
				"name":    title,
				"video":   host + "/elo/vidosik?uri=" + url.QueryEscape(link),
				"picture": img,
				"time":    strings.TrimSpace(submatch1(eloTimeRe, row)),
				"json":    true,
				"related": true,
				"bookmark": map[string]any{
					"site":  "elo",
					"href":  link,
					"image": img,
				},
			})
		}
	}
	return out
}

func isEbalovoVideoPath(path string) bool {
	path = strings.TrimSpace(strings.ToLower(path))
	if path == "" {
		return false
	}
	if strings.HasPrefix(path, "javascript:") || strings.HasPrefix(path, "#") {
		return false
	}
	if strings.HasPrefix(path, "video/") {
		return true
	}
	// Keep only content-like pages and drop navigational/static paths.
	if strings.HasPrefix(path, "search/") ||
		strings.HasPrefix(path, "porno/") ||
		strings.HasPrefix(path, "tags/") ||
		strings.HasPrefix(path, "category/") ||
		strings.HasPrefix(path, "user/") ||
		strings.HasPrefix(path, "images/") ||
		strings.HasPrefix(path, "img/") ||
		strings.Contains(path, ".jpg") ||
		strings.Contains(path, ".png") ||
		strings.Contains(path, ".webp") {
		return false
	}
	return strings.Contains(path, "/") || strings.Contains(path, "-")
}

func parseEpornerPlaylist(host, html string) []map[string]any {
	section := html
	if i := strings.Index(section, `id="relateddiv"`); i >= 0 {
		section = section[i:]
	} else if i := strings.Index(section, `id="vidresults"`); i >= 0 {
		section = section[i:]
	}
	parts := eprItemSplitRe.Split(section, -1)
	if len(parts) <= 1 {
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(parts)-1)
	for i := 1; i < len(parts); i++ {
		row := parts[i]
		m := eprHrefRe.FindStringSubmatch(row)
		if len(m) < 3 {
			continue
		}
		href := strings.TrimSpace(m[1])
		title := strings.TrimSpace(m[2])
		if href == "" || title == "" {
			continue
		}
		img := strings.TrimSpace(submatch1(eprImgDataRe, row))
		if img == "" {
			img = strings.TrimSpace(submatch1(eprImgSrcRe, row))
		}
		if img == "" {
			img = ""
		}
		preview := ""
		if img != "" {
			base := img
			if idx := strings.LastIndex(base, "/"); idx > 0 {
				base = base[:idx]
			}
			dataID := strings.TrimSpace(submatch1(eprDataIDRe, row))
			if dataID != "" {
				preview = base + "/" + dataID + "-preview.webm"
			}
		}
		out = append(out, map[string]any{
			"name":    title,
			"video":   host + "/epr/vidosik?uri=" + url.QueryEscape(href),
			"picture": img,
			"preview": preview,
			"quality": strings.TrimSpace(submatch1(eprQualityRe, row)),
			"time":    strings.TrimSpace(submatch1(eprTimeRe, row)),
			"json":    true,
			"related": true,
			"bookmark": map[string]any{
				"site":  "epr",
				"href":  href,
				"image": img,
			},
		})
	}
	return out
}

func parseEpornerQualitys(xhrRaw string) map[string]any {
	qualitys := map[string]any{}

	var root map[string]any
	if err := stdjson.Unmarshal([]byte(xhrRaw), &root); err == nil {
		if sources, ok := root["sources"].(map[string]any); ok {
			if mp4, ok := sources["mp4"].(map[string]any); ok {
				for key, raw := range mp4 {
					label := strings.TrimSpace(key)
					link := ""

					if row, ok := raw.(map[string]any); ok {
						if l := strings.TrimSpace(toString(row["labelShort"])); l != "" {
							label = l
						}
						link = strings.TrimSpace(toString(row["src"]))
					} else {
						link = strings.TrimSpace(toString(raw))
					}

					if link == "" {
						continue
					}
					if strings.HasPrefix(link, "//") {
						link = "https:" + link
					}
					if label == "" {
						if m := eprURLQRe.FindStringSubmatch(link); len(m) >= 3 {
							label = strings.TrimSpace(m[2])
						}
					}
					if label == "" {
						label = "auto"
					}
					qualitys[label] = link
				}
			}
		}
	}

	for _, m := range eprSrcQRe.FindAllStringSubmatch(xhrRaw, -1) {
		if len(m) < 3 {
			continue
		}
		link := strings.TrimSpace(strings.ReplaceAll(m[1], `\/`, "/"))
		q := strings.TrimSpace(m[2])
		if link == "" || q == "" {
			continue
		}
		if strings.HasPrefix(link, "//") {
			link = "https:" + link
		}
		qualitys[q] = link
	}
	for _, m := range eprURLQRe.FindAllStringSubmatch(xhrRaw, -1) {
		if len(m) < 3 {
			continue
		}
		link := strings.TrimSpace(m[1])
		q := strings.TrimSpace(m[2])
		if link == "" || q == "" {
			continue
		}
		qualitys[q] = link
	}

	return qualitys
}

func parseHQpornerPlaylist(host, html string) []map[string]any {
	parts := hqrItemSplitRe.Split(html, -1)
	out := make([]map[string]any, 0, max(len(parts)-1, 0))
	seen := make(map[string]struct{}, len(parts))

	for i := 1; i < len(parts); i++ {
		row := parts[i]
		href, img, title := "", "", ""
		if m := hqrItemRe.FindStringSubmatch(row); len(m) >= 4 {
			href = strings.TrimSpace(m[1])
			img = strings.TrimSpace(m[2])
			title = strings.TrimSpace(m[3])
		}
		if href == "" {
			href = strings.TrimSpace(submatch1(hqrLinkRe, row))
		}
		href = sisiNormalizePathFromHref(href)
		if title == "" {
			title = strings.TrimSpace(submatch1(hqrTitleAnyRe, row))
		}
		if img == "" {
			img = strings.TrimSpace(submatch1(hqrImgAnyRe, row))
		}
		if strings.Contains(img, ",") {
			img = firstSrcsetURL(img)
		}
		img = strings.TrimSpace(strings.SplitN(img, " ", 2)[0])
		if href == "" || img == "" || title == "" {
			continue
		}
		if _, ok := seen[href]; ok {
			continue
		}
		if strings.HasPrefix(img, "//") {
			img = "https:" + img
		} else if !strings.HasPrefix(img, "http://") && !strings.HasPrefix(img, "https://") {
			img = "https://" + strings.TrimLeft(img, "/")
		}
		seen[href] = struct{}{}
		out = append(out, map[string]any{
			"name":    title,
			"video":   host + "/hqr/vidosik?uri=" + url.QueryEscape(href),
			"picture": img,
			"time":    strings.TrimSpace(submatch1(hqrTimeRe, row)),
			"json":    true,
			"bookmark": map[string]any{
				"site":  "hqr",
				"href":  href,
				"image": img,
			},
		})
	}

	// Fallback parser for modern layouts with changed classes.
	if len(out) == 0 {
		matches := hqrLinkRe.FindAllStringSubmatchIndex(html, -1)
		for _, m := range matches {
			if len(m) < 4 {
				continue
			}
			href := sisiNormalizePathFromHref(strings.TrimSpace(html[m[2]:m[3]]))
			if href == "" {
				continue
			}
			if _, ok := seen[href]; ok {
				continue
			}

			from := max(m[0]-600, 0)
			to := min(m[1]+1800, len(html))
			row := html[from:to]

			title := strings.TrimSpace(submatch1(hqrTitleAnyRe, row))
			img := strings.TrimSpace(submatch1(hqrImgAnyRe, row))
			if strings.Contains(img, ",") {
				img = firstSrcsetURL(img)
			}
			img = strings.TrimSpace(strings.SplitN(img, " ", 2)[0])
			if title == "" || img == "" {
				continue
			}
			if strings.HasPrefix(img, "//") {
				img = "https:" + img
			} else if !strings.HasPrefix(img, "http://") && !strings.HasPrefix(img, "https://") {
				img = "https://" + strings.TrimLeft(img, "/")
			}
			seen[href] = struct{}{}
			out = append(out, map[string]any{
				"name":    title,
				"video":   host + "/hqr/vidosik?uri=" + url.QueryEscape(href),
				"picture": img,
				"time":    strings.TrimSpace(submatch1(hqrTimeRe, row)),
				"json":    true,
				"bookmark": map[string]any{
					"site":  "hqr",
					"href":  href,
					"image": img,
				},
			})
		}
	}
	return out
}

func (s *sisiEbalovoSource) hostCandidates() []string {
	now := time.Now().UTC()
	dated := []string{
		ebalovoDatedHost(now),
		ebalovoDatedHost(now.AddDate(0, 0, -1)),
		ebalovoDatedHost(now.AddDate(0, 0, 1)),
	}

	ordered := make([]string, 0, 1+len(dated)+len(s.fallbackHosts))
	if ebalovoIsLikelyLegacyHost(s.host) {
		ordered = append(ordered, dated...)
		ordered = append(ordered, s.host)
	} else {
		ordered = append(ordered, s.host)
		ordered = append(ordered, dated...)
	}
	ordered = append(ordered, s.fallbackHosts...)
	return sisiHostFallbacks(ordered[0], ordered[1:]...)
}

func (s *sisiHQpornerSource) hostCandidates() []string {
	return sisiHostFallbacks(s.host, s.fallbackHosts...)
}

func ebalovoDatedHost(day time.Time) string {
	return "https://w" + day.UTC().Format("020106") + ".yabalovo.best"
}

func ebalovoIsLikelyLegacyHost(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "ebalovo.pro", "www.ebalovo.pro", "ebalovo.porn", "www.ebalovo.porn", "yabalovo.best", "www.yabalovo.best":
		return true
	default:
		return false
	}
}

func parseBongaPlaylist(html string) ([]map[string]any, int) {
	list, total := parseRunetkiPlaylist(html)
	if len(list) == 0 {
		return []map[string]any{}, total
	}

	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		video := toString(item["video"])
		if before, ok := strings.CutSuffix(video, "/playlist.m3u8"); ok {
			base := before
			streamName := base[strings.LastIndex(base, "/")+1:]
			video = base + "/public-aac/" + streamName + "/chunks.m3u8"
		}
		out = append(out, map[string]any{
			"name":    toString(item["name"]),
			"quality": toString(item["quality"]),
			"video":   video,
			"picture": toString(item["picture"]),
		})
	}
	return out, total
}

func parseChaturbatePlaylist(host, html string) []map[string]any {
	parts := chuSplitRe.Split(html, -1)
	if len(parts) <= 1 {
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(parts)-1)
	for i := 1; i < len(parts); i++ {
		row := parts[i]
		if !chuPublicRe.MatchString(row) {
			continue
		}
		user := strings.TrimSpace(submatch1(chuUserRe, row))
		img := strings.TrimSpace(submatch1(chuImgRe, row))
		if user == "" || img == "" {
			continue
		}
		out = append(out, map[string]any{
			"name":    user,
			"video":   host + "/chu/potok?baba=" + url.QueryEscape(user),
			"picture": strings.ReplaceAll(img, `\`, ""),
			"json":    true,
		})
	}
	return out
}

func eloMenu(host, sortKey, c string) []map[string]any {
	base := host + "/elo"
	title := "новинки"
	if sortKey != "" {
		title = sortKey
	}
	menu := []map[string]any{
		{
			"title":        "Поиск",
			"search_on":    "search_on",
			"playlist_url": base,
		},
		{
			"title":        "Сортировка: " + title,
			"playlist_url": "submenu",
			"submenu": []map[string]any{
				{"title": "Новинки", "playlist_url": base + "?c=" + c},
				{"title": "Лучшее", "playlist_url": base + "?c=" + c + "&sort=porno-online"},
				{"title": "Популярное", "playlist_url": base + "?c=" + c + "&sort=xxx-top"},
			},
		},
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
	type cat struct {
		title string
		value string
	}
	cats := []cat{
		{title: "Анал", value: "anal-videos"},
		{title: "BDSM", value: "bdsm-porn"},
		{title: "Большие жопы", value: "big-ass"},
		{title: "Большие сиськи", value: "big-tits"},
		{title: "BBC", value: "bbc"},
		{title: "Блондинки", value: "blonde"},
		{title: "Брюнетки", value: "a1-brunette"},
		{title: "MILF", value: "milf"},
		{title: "Зрелые", value: "mature"},
		{title: "Лесбиянки", value: "lesbian"},
		{title: "Тинейджеры", value: "teen"},
		{title: "POV", value: "pov"},
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
	return menu
}

func eprMenu(host, search, sortKey, c string) []map[string]any {
	base := host + "/epr"
	items := []map[string]any{
		{"title": "Поиск", "search_on": "search_on", "playlist_url": base},
	}
	title := "новинки"
	if sortKey != "" {
		title = sortKey
	}
	if search != "" {
		qs := url.QueryEscape(search)
		items = append(items, map[string]any{
			"title":        "Сортировка: " + title,
			"playlist_url": "submenu",
			"submenu": []map[string]any{
				{"title": "Новинки", "playlist_url": base + "?search=" + qs},
				{"title": "Топ просмотра", "playlist_url": base + "?sort=most-viewed&search=" + qs},
				{"title": "Топ рейтинга", "playlist_url": base + "?sort=top-rated&search=" + qs},
				{"title": "Длинные ролики", "playlist_url": base + "?sort=longest&search=" + qs},
				{"title": "Короткие ролики", "playlist_url": base + "?sort=shortest&search=" + qs},
			},
		})
		return items
	}
	items = append(items, map[string]any{
		"title":        "Сортировка: " + title,
		"playlist_url": "submenu",
		"submenu": []map[string]any{
			{"title": "Новинки", "playlist_url": base},
			{"title": "Топ просмотра", "playlist_url": base + "?sort=most-viewed"},
			{"title": "Топ рейтинга", "playlist_url": base + "?sort=top-rated"},
			{"title": "Длинные ролики", "playlist_url": base + "?sort=longest"},
			{"title": "Короткие ролики", "playlist_url": base + "?sort=shortest"},
		},
	})
	type cat struct {
		title string
		value string
	}
	cats := []cat{
		{title: "4K UHD", value: "4k-porn"},
		{title: "60 FPS", value: "60fps"},
		{title: "Amateur", value: "amateur"},
		{title: "Anal", value: "anal"},
		{title: "Asian", value: "asian"},
		{title: "ASMR", value: "asmr"},
		{title: "BBW", value: "bbw"},
		{title: "BDSM", value: "bdsm"},
		{title: "Big Ass", value: "big-ass"},
		{title: "Big Dick", value: "big-dick"},
		{title: "Blowjob", value: "blowjob"},
		{title: "MILF", value: "milf"},
	}
	active := "все"
	sub := make([]map[string]any, 0, len(cats)+1)
	sub = append(sub, map[string]any{
		"title":        "Все",
		"playlist_url": base,
	})
	for _, cat := range cats {
		if cat.value == c {
			active = cat.title
		}
		sub = append(sub, map[string]any{
			"title":        cat.title,
			"playlist_url": base + "?c=" + url.QueryEscape(cat.value),
		})
	}
	items = append(items, map[string]any{
		"title":        "Категория: " + active,
		"playlist_url": "submenu",
		"submenu":      sub,
	})
	return items
}

func hqrMenu(host, sortKey, c string) []map[string]any {
	base := host + "/hqr"
	title := "новинки"
	if sortKey != "" {
		title = sortKey
	}
	menu := []map[string]any{
		{"title": "Поиск", "search_on": "search_on", "playlist_url": base},
		{
			"title":        "Сортировка: " + title,
			"playlist_url": "submenu",
			"submenu": []map[string]any{
				{"title": "Самые новые", "playlist_url": base + "?c=" + c},
				{"title": "Топ недели", "playlist_url": base + "?c=" + c + "&sort=week"},
				{"title": "Топ месяца", "playlist_url": base + "?c=" + c + "&sort=month"},
			},
		},
	}
	type cat struct {
		title string
		value string
	}
	cats := []cat{
		{title: "1080p porn", value: "1080p-porn"},
		{title: "4k porn", value: "4k-porn"},
		{title: "anal", value: "anal-sex-hd"},
		{title: "milf", value: "milf"},
		{title: "lesbian", value: "lesbian"},
		{title: "60fps", value: "60fps-porn"},
		{title: "creampie", value: "creampie"},
		{title: "big tits", value: "big-tits"},
		{title: "teen porn", value: "teen-porn"},
		{title: "pov", value: "pov"},
		{title: "threesome", value: "threesome"},
		{title: "asian", value: "asian"},
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
	return menu
}

func bgsMenu(host, sortKey string) []map[string]any {
	base := host + "/bgs"
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
				{"title": "Русские модели", "playlist_url": base + "?sort=female/tags/russian"},
				{"title": "Парни", "playlist_url": base + "?sort=male"},
				{"title": "Транссексуалы", "playlist_url": base + "?sort=transsexual"},
			},
		},
	}
}

func chuMenu(host, sortKey string) []map[string]any {
	base := host + "/chu"
	title := "Лучшие"
	switch sortKey {
	case "f":
		title = "Девушки"
	case "c":
		title = "Пары"
	case "m":
		title = "Парни"
	case "t":
		title = "Транссексуалы"
	}
	return []map[string]any{
		{
			"title":        "Сортировка: " + title,
			"playlist_url": "submenu",
			"submenu": []map[string]any{
				{"title": "Лучшие", "playlist_url": base},
				{"title": "Девушки", "playlist_url": base + "?sort=f"},
				{"title": "Пары", "playlist_url": base + "?sort=c"},
				{"title": "Парни", "playlist_url": base + "?sort=m"},
				{"title": "Транссексуалы", "playlist_url": base + "?sort=t"},
			},
		},
	}
}

func epornerConvertHash(h string) string {
	h = strings.TrimSpace(h)
	if len(h) < 32 {
		return ""
	}
	var b strings.Builder
	b.Grow(64)
	for i := 0; i+8 <= len(h) && i < 32; i += 8 {
		part := h[i : i+8]
		b.WriteString(base36Hex(part))
	}
	return b.String()
}

func base36Hex(hexPart string) string {
	v, err := strconv.ParseUint(hexPart, 16, 64)
	if err != nil {
		return ""
	}
	if v == 0 {
		return "0"
	}
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	buf := make([]byte, 0, 16)
	for v > 0 {
		buf = append(buf, alphabet[v%36])
		v /= 36
	}
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}
