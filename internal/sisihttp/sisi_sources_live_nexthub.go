package sisihttp

import (
	"bytes"
	"context"
	stdjson "encoding/json"
	"io"
	"lampac-go/internal/httpclient"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"

	"lampac-go/internal/config"

	"github.com/antchfx/htmlquery"
	"golang.org/x/text/encoding/charmap"
	"gopkg.in/yaml.v3"
)

var nextHubPluginSanitizeRe = regexp.MustCompile(`[^a-z0-9\-]+`)

type nextHubSource struct {
	cfg    config.Config
	client *http.Client
}

type nextHubSettings struct {
	Enable          bool                `yaml:"enable"`
	DisplayName     string              `yaml:"displayname"`
	Host            string              `yaml:"host"`
	IgnoreNoPicture bool                `yaml:"ignore_no_picture"`
	Menu            nextHubMenu         `yaml:"menu"`
	List            nextHubList         `yaml:"list"`
	Search          nextHubList         `yaml:"search"`
	Model           nextHubList         `yaml:"model"`
	ContentParse    nextHubContentParse `yaml:"contentParse"`
	View            nextHubView         `yaml:"view"`
	Headers         map[string]string   `yaml:"headers"`
	Timeout         int                 `yaml:"timeout"`
	CacheTime       int                 `yaml:"cache_time"`
	HTTPVersion     int                 `yaml:"httpversion"`
	Route           map[string]any      `yaml:"route"`
	Raw             map[string]any      `yaml:",inline"`
}

type nextHubMenu struct {
	Bind       bool                `yaml:"bind"`
	Route      map[string]string   `yaml:"route"`
	Sort       map[string]string   `yaml:"sort"`
	Categories map[string]string   `yaml:"categories"`
	Customs    []nextHubCustomMenu `yaml:"customs"`
	Raw        map[string]any      `yaml:",inline"`
}

type nextHubCustomMenu struct {
	Name    string            `yaml:"name"`
	Arg     string            `yaml:"arg"`
	Format  string            `yaml:"format"`
	Submenu map[string]string `yaml:"submenu"`
}

type nextHubList struct {
	TotalPages       int                 `yaml:"total_pages"`
	FirstPage        string              `yaml:"firstpage"`
	URI              string              `yaml:"uri"`
	Data             string              `yaml:"data"`
	EncodingRequest  string              `yaml:"encodingRequest"`
	EncodingResponse string              `yaml:"encodingResponse"`
	Format           string              `yaml:"format"`
	RouteEval        string              `yaml:"routeEval"`
	ContentParse     nextHubContentParse `yaml:"contentParse"`
}

type nextHubContentParse struct {
	Nodes    string            `yaml:"nodes"`
	Name     nextHubSingleNode `yaml:"name"`
	Href     nextHubSingleNode `yaml:"href"`
	Img      nextHubSingleNode `yaml:"img"`
	Duration nextHubSingleNode `yaml:"duration"`
	Quality  nextHubSingleNode `yaml:"quality"`
	Preview  nextHubSingleNode `yaml:"preview"`
	Model    nextHubModelParse `yaml:"model"`
	JSON     *bool             `yaml:"json"`
	Eval     string            `yaml:"eval"`
}

type nextHubSingleNode struct {
	Node       string   `yaml:"node"`
	Attribute  string   `yaml:"attribute"`
	Attributes []string `yaml:"attributes"`
	Format     string   `yaml:"format"`
}

type nextHubModelParse struct {
	Name nextHubSingleNode `yaml:"name"`
	Href nextHubSingleNode `yaml:"href"`
}

type nextHubView struct {
	InitURLEval  string              `yaml:"initUrlEval"`
	Eval         string              `yaml:"eval"`
	RegexMatch   nextHubRegexMatch   `yaml:"regexMatch"`
	NodeFile     nextHubSingleNode   `yaml:"nodeFile"`
	Related      bool                `yaml:"related"`
	RelatedParse nextHubContentParse `yaml:"relatedParse"`
	RouteEval    string              `yaml:"routeEval"`
}

type nextHubRegexMatch struct {
	Pattern string   `yaml:"pattern"`
	Matches []string `yaml:"matches"`
	Format  string   `yaml:"format"`
}

func newNextHubSource(cfg config.Config) *nextHubSource {
	return &nextHubSource{
		cfg:    cfg,
		client: httpclient.NewProxied(20 * time.Second),
	}
}

func (s *nextHubSource) listHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if parseBoolParam(r.URL.Query().Get("checksearch")) {
			writeRawJSON(w, []byte(`{"rch":false}`))
			return
		}

		plugin := sanitizeNextHubPlugin(r.URL.Query().Get("plugin"))
		if plugin == "" {
			writeJSON(w, http.StatusOK, map[string]any{
				"menu":        []any{},
				"list":        []any{},
				"total_pages": 1,
			})
			return
		}

		if !nextHubPluginAllowed(plugin) {
			writeJSON(w, http.StatusOK, map[string]any{
				"menu":        []any{},
				"list":        []any{},
				"total_pages": 1,
			})
			return
		}

		settings, ok := loadNextHubSettings(plugin)
		if !ok || !settings.Enable {
			writeJSON(w, http.StatusOK, map[string]any{
				"menu":        []any{},
				"list":        []any{},
				"total_pages": 1,
			})
			return
		}
		if strings.TrimSpace(settings.ContentParse.Eval) != "" || strings.TrimSpace(settings.List.RouteEval) != "" || strings.TrimSpace(settings.Search.RouteEval) != "" {
			sisiListStubHandler(s.cfg).ServeHTTP(w, r)
			return
		}

		search := strings.TrimSpace(r.URL.Query().Get("search"))
		sortKey := strings.TrimSpace(r.URL.Query().Get("sort"))
		cat := strings.TrimSpace(r.URL.Query().Get("cat"))
		model := strings.TrimSpace(r.URL.Query().Get("model"))
		pg := sisiIntOrDefault(r.URL.Query().Get("pg"), 1)
		if pg <= 0 {
			pg = 1
		}

		targetURL, method, body, timeoutSec, responseEncoding := nextHubBuildListRequest(settings, r.URL.Query(), plugin, pg, search, sortKey, cat, model)
		html, err := s.fetch(targetURL, method, body, settings.Headers, timeoutSec, responseEncoding)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"menu":        nextHubBuildMenu(hostFromRequest(r), plugin, settings, r.URL.Query(), search, sortKey, cat, model),
				"list":        []any{},
				"total_pages": maxInt(nextHubTotalPages(settings, search, model), 1),
			})
			return
		}

		parseCfg := settings.ContentParse
		if search != "" && settings.Search.ContentParse.Nodes != "" {
			parseCfg = settings.Search.ContentParse
		}
		if model != "" && settings.Model.ContentParse.Nodes != "" {
			parseCfg = settings.Model.ContentParse
		}

		list := nextHubParsePlaylist(html, hostFromRequest(r), plugin, settings, parseCfg)
		writeJSON(w, http.StatusOK, map[string]any{
			"menu":        nextHubBuildMenu(hostFromRequest(r), plugin, settings, r.URL.Query(), search, sortKey, cat, model),
			"list":        list,
			"total_pages": maxInt(nextHubTotalPages(settings, search, model), 1),
		})
	}
}

func (s *nextHubSource) viewHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if parseBoolParam(r.URL.Query().Get("checksearch")) {
			writeRawJSON(w, []byte(`{"rch":false}`))
			return
		}

		uri := strings.TrimSpace(r.URL.Query().Get("uri"))
		plugin, targetURL, ok := parseNextHubURI(uri)
		if !ok {
			writeJSON(w, http.StatusOK, map[string]any{"qualitys": map[string]any{}, "recomends": []any{}})
			return
		}
		if !nextHubPluginAllowed(plugin) {
			writeJSON(w, http.StatusOK, map[string]any{"qualitys": map[string]any{}, "recomends": []any{}})
			return
		}
		settings, ok := loadNextHubSettings(plugin)
		if !ok || !settings.Enable {
			writeJSON(w, http.StatusOK, map[string]any{"qualitys": map[string]any{}, "recomends": []any{}})
			return
		}
		if strings.TrimSpace(settings.View.Eval) != "" || strings.TrimSpace(settings.View.InitURLEval) != "" || strings.TrimSpace(settings.View.RouteEval) != "" {
			sisiViewStubHandler(s.cfg).ServeHTTP(w, r)
			return
		}

		targetURL = nextHubAbsURL(settings.Host, targetURL)
		html, err := s.fetch(targetURL, http.MethodGet, "", settings.Headers, settings.Timeout, settings.List.EncodingResponse)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"qualitys": map[string]any{}, "recomends": []any{}})
			return
		}

		qualitys := nextHubParseViewQualitys(html, settings)
		if parseBoolParam(r.URL.Query().Get("related")) {
			var related []map[string]any
			if settings.View.Related && settings.View.RelatedParse.Nodes != "" {
				related = nextHubParsePlaylist(html, hostFromRequest(r), plugin, settings, settings.View.RelatedParse)
			} else {
				related = []map[string]any{}
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"list":        related,
				"total_pages": 1,
			})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"qualitys":       qualitys,
			"qualitys_proxy": sisiProxyQualitys(r, qualitys),
			"recomends":      []any{},
		})
	}
}

func (s *nextHubSource) fetch(targetURL, method, body string, headers map[string]string, timeoutSec int, responseEncoding string) (string, error) {
	if method == "" {
		method = http.MethodGet
	}
	if timeoutSec <= 0 {
		timeoutSec = 20
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	var reqBody *bytes.Reader
	if method == http.MethodPost {
		reqBody = bytes.NewReader([]byte(body))
	} else {
		reqBody = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, targetURL, reqBody)
	if err != nil {
		return "", err
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range headers {
		if strings.TrimSpace(k) == "" || strings.TrimSpace(v) == "" {
			continue
		}
		req.Header.Set(k, v)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return decodeTextWithEncoding(data, responseEncoding), nil
}

func loadNextHubSettings(plugin string) (nextHubSettings, bool) {
	var out nextHubSettings
	path := relToRuntime(filepath.Join("NextHUB", "sites", plugin+".yaml"))
	data, err := readConfigFile(path)
	if err != nil {
		return out, false
	}
	if err := yaml.Unmarshal(data, &out); err != nil {
		return out, false
	}
	out.Host = strings.TrimRight(strings.TrimSpace(out.Host), "/")
	if out.Host == "" {
		return out, false
	}
	if !out.IgnoreNoPicture {
		// C# default is true when omitted.
		var raw map[string]any
		if err := yaml.Unmarshal(data, &raw); err == nil {
			if _, exists := raw["ignore_no_picture"]; !exists {
				out.IgnoreNoPicture = true
			}
			menuRaw, _ := raw["menu"].(map[string]any)
			if _, exists := menuRaw["bind"]; !exists {
				out.Menu.Bind = true
			}
		}
	}
	return out, true
}

func nextHubBuildListRequest(s nextHubSettings, q url.Values, plugin string, pg int, search, sortKey, cat, model string) (targetURL, method, body string, timeoutSec int, responseEncoding string) {
	method = http.MethodGet
	timeoutSec = s.Timeout
	if timeoutSec <= 0 {
		timeoutSec = 10
	}

	pageURI := s.List.URI
	if pg == 1 && strings.TrimSpace(s.List.FirstPage) != "" {
		pageURI = s.List.FirstPage
	}
	searchCfg := s.Search
	if strings.TrimSpace(searchCfg.URI) == "" {
		searchCfg.URI = pageURI
	}
	encodingRequest := strings.TrimSpace(s.List.EncodingRequest)
	responseEncoding = strings.TrimSpace(s.List.EncodingResponse)

	if search != "" {
		if strings.TrimSpace(searchCfg.EncodingRequest) != "" {
			encodingRequest = strings.TrimSpace(searchCfg.EncodingRequest)
		}
		if strings.TrimSpace(searchCfg.EncodingResponse) != "" {
			responseEncoding = strings.TrimSpace(searchCfg.EncodingResponse)
		}
		searchURI := searchCfg.URI
		if pg == 1 && strings.TrimSpace(searchCfg.FirstPage) != "" {
			searchURI = searchCfg.FirstPage
		}
		targetURL = nextHubRenderTemplate(searchURI, s.Host, plugin, pg, nextHubQueryEscape(search, encodingRequest), sortKey, cat, model, q)
	} else if model != "" {
		if route := strings.TrimSpace(nextHubMenuRouteValue(s.Menu.Route, "model")); route != "" {
			targetURL = nextHubRenderTemplate(route, s.Host, plugin, pg, search, sortKey, cat, model, q)
		} else {
			targetURL = nextHubRenderTemplate(model, s.Host, plugin, pg, search, sortKey, cat, model, q)
		}
	} else if cat != "" && sortKey != "" {
		if route := strings.TrimSpace(nextHubMenuRouteValue(s.Menu.Route, "catsort")); route != "" {
			targetURL = nextHubRenderTemplate(route, s.Host, plugin, pg, search, sortKey, cat, model, q)
		}
	} else if cat != "" {
		if route := strings.TrimSpace(nextHubMenuRouteValue(s.Menu.Route, "cat")); route != "" {
			targetURL = nextHubRenderTemplate(route, s.Host, plugin, pg, search, sortKey, cat, model, q)
		} else {
			formatted := cat
			if format := strings.TrimSpace(s.Menu.Categories["format"]); format != "" {
				formatted = strings.ReplaceAll(format, "{cat}", cat)
			}
			targetURL = nextHubRenderTemplate(formatted, s.Host, plugin, pg, search, sortKey, cat, model, q)
		}
	} else if sortKey != "" {
		if route := strings.TrimSpace(nextHubMenuRouteValue(s.Menu.Route, "sort")); route != "" {
			targetURL = nextHubRenderTemplate(route, s.Host, plugin, pg, search, sortKey, cat, model, q)
		} else {
			targetURL = nextHubRenderTemplate(sortKey, s.Host, plugin, pg, search, sortKey, cat, model, q)
		}
	} else if len(s.Menu.Customs) > 0 {
		for _, custom := range s.Menu.Customs {
			arg := strings.TrimSpace(custom.Arg)
			if arg == "" {
				continue
			}
			val := strings.TrimSpace(q.Get(arg))
			if val == "" {
				continue
			}
			formatted := strings.TrimSpace(strings.ReplaceAll(custom.Format, "{value}", val))
			targetURL = nextHubRenderTemplate(formatted, s.Host, plugin, pg, search, sortKey, cat, model, q)
		}
	}

	if len(s.Menu.Route) > 0 {
		routeName := "-"
		switch {
		case cat != "" && sortKey != "":
			routeName = "catsort"
		case model != "" && sortKey != "":
			routeName = "modelsort"
		case model != "":
			routeName = "model"
		case cat != "":
			routeName = "cat"
		case sortKey != "":
			routeName = "sort"
		}
		if route := strings.TrimSpace(nextHubMenuRouteValue(s.Menu.Route, routeName)); route != "" {
			targetURL = nextHubRenderTemplate(route, s.Host, plugin, pg, search, sortKey, cat, model, q)
		}
	}

	if targetURL == "" {
		targetURL = nextHubRenderTemplate(pageURI, s.Host, plugin, pg, search, sortKey, cat, model, q)
	}
	if !strings.HasPrefix(strings.ToLower(targetURL), "http://") && !strings.HasPrefix(strings.ToLower(targetURL), "https://") {
		targetURL = strings.TrimRight(s.Host, "/") + "/" + strings.TrimLeft(targetURL, "/")
	}

	if strings.TrimSpace(s.List.Data) != "" && search == "" {
		method = http.MethodPost
		body = nextHubRenderTemplate(s.List.Data, s.Host, plugin, pg, search, sortKey, cat, model, q)
	}
	if strings.TrimSpace(searchCfg.Data) != "" && search != "" {
		method = http.MethodPost
		body = nextHubRenderTemplate(searchCfg.Data, s.Host, plugin, pg, nextHubQueryEscape(search, encodingRequest), sortKey, cat, model, q)
	}
	return targetURL, method, body, timeoutSec, responseEncoding
}

func nextHubBuildMenu(host, plugin string, s nextHubSettings, q url.Values, search, sortKey, cat, model string) []map[string]any {
	menu := make([]map[string]any, 0, 3)
	base := host + "/nexthub?plugin=" + url.QueryEscape(plugin)
	usedRoute := len(s.Menu.Route) > 0 || len(s.Route) > 0

	if strings.TrimSpace(s.Search.URI) != "" && model == "" {
		menu = append(menu, map[string]any{
			"title":        "Поиск",
			"search_on":    "search_on",
			"playlist_url": base,
		})
	}

	if search == "" && len(s.Menu.Sort) > 0 {
		type item struct {
			name string
			val  string
		}
		items := make([]item, 0, len(s.Menu.Sort))
		for name, val := range s.Menu.Sort {
			items = append(items, item{name: name, val: strings.TrimSpace(val)})
		}
		sort.Slice(items, func(i, j int) bool { return items[i].name < items[j].name })

		sortTitle := ""
		for _, it := range items {
			if it.val == sortKey {
				sortTitle = it.name
				break
			}
		}
		if sortTitle == "" && len(items) > 0 {
			sortTitle = items[0].name
		}

		sub := make([]map[string]any, 0, len(items))
		for _, it := range items {
			u := base + "&sort=" + url.QueryEscape(it.val)
			if usedRoute && s.Menu.Bind {
				if model != "" {
					u += "&model=" + url.QueryEscape(model)
				}
				if cat != "" {
					u += "&cat=" + url.QueryEscape(cat)
				}
			}
			sub = append(sub, map[string]any{"title": it.name, "playlist_url": u})
		}

		menu = append(menu, map[string]any{
			"title":        "Сортировка: " + sortTitle,
			"playlist_url": "submenu",
			"submenu":      sub,
		})
	}

	if search == "" && model == "" && len(s.Menu.Categories) > 0 {
		type item struct {
			name string
			val  string
		}
		items := make([]item, 0, len(s.Menu.Categories))
		for name, val := range s.Menu.Categories {
			if strings.EqualFold(name, "format") {
				continue
			}
			items = append(items, item{name: name, val: strings.TrimSpace(val)})
		}
		sort.Slice(items, func(i, j int) bool { return items[i].name < items[j].name })

		catTitle := "Выбрать"
		for _, it := range items {
			if it.val == cat {
				catTitle = it.name
				break
			}
		}

		sub := make([]map[string]any, 0, len(items))
		for _, it := range items {
			u := base + "&cat=" + url.QueryEscape(it.val)
			if usedRoute && s.Menu.Bind && sortKey != "" {
				u += "&sort=" + url.QueryEscape(sortKey)
			}
			sub = append(sub, map[string]any{"title": it.name, "playlist_url": u})
		}

		menu = append(menu, map[string]any{
			"title":        "Категории: " + catTitle,
			"playlist_url": "submenu",
			"submenu":      sub,
		})
	}

	if search == "" && model == "" && len(s.Menu.Customs) > 0 {
		for _, custom := range s.Menu.Customs {
			if strings.TrimSpace(custom.Name) == "" || strings.TrimSpace(custom.Arg) == "" || len(custom.Submenu) == 0 {
				continue
			}
			current := strings.TrimSpace(q.Get(custom.Arg))
			title := "Выбрать"
			sub := make([]map[string]any, 0, len(custom.Submenu))

			names := make([]string, 0, len(custom.Submenu))
			for name := range custom.Submenu {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				val := strings.TrimSpace(custom.Submenu[name])
				if val == "" {
					continue
				}
				if val == current {
					title = name
				}
				u := base + "&" + url.QueryEscape(custom.Arg) + "=" + url.QueryEscape(val)
				sub = append(sub, map[string]any{"title": name, "playlist_url": u})
			}
			menu = append(menu, map[string]any{
				"title":        strings.TrimSpace(custom.Name) + ": " + title,
				"playlist_url": "submenu",
				"submenu":      sub,
			})
		}
	}
	return menu
}

func nextHubParsePlaylist(html, host, plugin string, s nextHubSettings, cp nextHubContentParse) []map[string]any {
	if strings.TrimSpace(cp.Nodes) == "" || strings.TrimSpace(cp.Eval) != "" {
		return []map[string]any{}
	}
	doc, err := htmlquery.Parse(strings.NewReader(html))
	if err != nil {
		return []map[string]any{}
	}
	nodes, err := htmlquery.QueryAll(doc, cp.Nodes)
	if err != nil || len(nodes) == 0 {
		return []map[string]any{}
	}
	jsonFlag := true
	if cp.JSON != nil {
		jsonFlag = *cp.JSON
	}

	out := make([]map[string]any, 0, len(nodes))
	for _, n := range nodes {
		name := strings.TrimSpace(nextHubNodeValue(n, cp.Name, s.Host))
		href := strings.TrimSpace(nextHubNodeValue(n, cp.Href, s.Host))
		img := strings.TrimSpace(nextHubNodeValue(n, cp.Img, s.Host))
		dur := strings.TrimSpace(nextHubNodeValue(n, cp.Duration, s.Host))
		quality := strings.TrimSpace(nextHubNodeValue(n, cp.Quality, s.Host))
		preview := strings.TrimSpace(nextHubNodeValue(n, cp.Preview, s.Host))

		if href == "" {
			continue
		}
		if s.IgnoreNoPicture && img == "" {
			continue
		}
		if name == "" {
			name = href
		}

		href = nextHubAbsURL(s.Host, href)
		if img != "" {
			img = nextHubAbsURL(s.Host, img)
		}
		if preview != "" {
			preview = nextHubAbsURL(s.Host, preview)
		}

		item := map[string]any{
			"name":    name,
			"video":   host + "/nexthub/vidosik?uri=" + url.QueryEscape(plugin+"_-:-_"+href),
			"picture": img,
			"time":    dur,
			"quality": quality,
			"preview": preview,
			"json":    jsonFlag,
			"bookmark": map[string]any{
				"site":  plugin,
				"href":  href,
				"image": img,
			},
		}

		if cp.Model.Name.Node != "" || cp.Model.Href.Node != "" {
			mn := strings.TrimSpace(nextHubNodeValue(n, cp.Model.Name, s.Host))
			mh := strings.TrimSpace(nextHubNodeValue(n, cp.Model.Href, s.Host))
			if mn != "" && mh != "" {
				mh = nextHubAbsURL(s.Host, mh)
				item["model"] = map[string]any{
					"name": mn,
					"uri":  "nexthub?plugin=" + url.QueryEscape(plugin) + "&model=" + url.QueryEscape(mh),
				}
			}
		}
		out = append(out, item)
	}
	return out
}

func nextHubParseViewQualitys(html string, s nextHubSettings) map[string]any {
	if html == "" {
		return map[string]any{}
	}
	doc, err := htmlquery.Parse(strings.NewReader(html))
	if err != nil {
		return map[string]any{}
	}

	if strings.TrimSpace(s.View.NodeFile.Node) != "" {
		file := strings.TrimSpace(nextHubNodeValue(doc, s.View.NodeFile, s.Host))
		if file != "" {
			return map[string]any{"auto": nextHubAbsURL(s.Host, file)}
		}
	}

	pattern := strings.TrimSpace(s.View.RegexMatch.Pattern)
	if pattern == "" {
		return map[string]any{}
	}

	out := map[string]any{}
	if len(s.View.RegexMatch.Matches) > 0 {
		for _, m := range s.View.RegexMatch.Matches {
			m = strings.TrimSpace(m)
			if m == "" {
				continue
			}
			p := strings.ReplaceAll(pattern, "{value}", regexp.QuoteMeta(m))
			re, err := regexp.Compile("(?is)" + p)
			if err != nil {
				continue
			}
			g := re.FindStringSubmatch(html)
			if len(g) < 2 || strings.TrimSpace(g[1]) == "" {
				continue
			}
			link := strings.TrimSpace(g[1])
			if fmt := strings.TrimSpace(s.View.RegexMatch.Format); fmt != "" {
				link = strings.ReplaceAll(fmt, "{value}", link)
			}
			out[m] = nextHubAbsURL(s.Host, link)
		}
		if len(out) > 0 {
			return out
		}
	}

	re, err := regexp.Compile("(?is)" + pattern)
	if err != nil {
		return map[string]any{}
	}
	g := re.FindStringSubmatch(html)
	if len(g) < 2 || strings.TrimSpace(g[1]) == "" {
		return map[string]any{}
	}
	link := strings.TrimSpace(g[1])
	if fmt := strings.TrimSpace(s.View.RegexMatch.Format); fmt != "" {
		link = strings.ReplaceAll(fmt, "{value}", link)
	}
	return map[string]any{"auto": nextHubAbsURL(s.Host, link)}
}

func nextHubNodeValue(root *html.Node, cfg nextHubSingleNode, host string) string {
	node := root
	if strings.TrimSpace(cfg.Node) != "" {
		found, err := htmlquery.Query(node, cfg.Node)
		if err != nil || found == nil {
			return ""
		}
		node = found
	}
	var value string
	switch {
	case len(cfg.Attributes) > 0:
		for _, a := range cfg.Attributes {
			a = strings.TrimSpace(a)
			if a == "" {
				continue
			}
			v := strings.TrimSpace(htmlquery.SelectAttr(node, a))
			if v != "" {
				value = v
				break
			}
		}
	case strings.TrimSpace(cfg.Attribute) != "":
		value = strings.TrimSpace(htmlquery.SelectAttr(node, strings.TrimSpace(cfg.Attribute)))
	default:
		value = strings.TrimSpace(htmlquery.InnerText(node))
	}
	if value == "" {
		return ""
	}
	value = strings.ReplaceAll(value, "\\/", "/")
	if strings.TrimSpace(cfg.Format) != "" {
		value = strings.ReplaceAll(cfg.Format, "{value}", value)
		value = strings.ReplaceAll(value, "{host}", strings.TrimRight(host, "/"))
	}
	return strings.TrimSpace(value)
}

func parseNextHubURI(raw string) (plugin, target string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", false
	}
	parts := strings.SplitN(raw, "_-:-_", 2)
	if len(parts) == 2 {
		plugin = sanitizeNextHubPlugin(parts[0])
		target = strings.TrimSpace(parts[1])
		return plugin, target, plugin != "" && target != ""
	}
	return "", "", false
}

func sanitizeNextHubPlugin(plugin string) string {
	plugin = strings.ToLower(strings.TrimSpace(plugin))
	plugin = nextHubPluginSanitizeRe.ReplaceAllString(plugin, "")
	return plugin
}

func nextHubPluginAllowed(plugin string) bool {
	data, ok := readFileAny("init.conf")
	if !ok {
		return true
	}
	var root map[string]any
	if err := stdjson.Unmarshal(data, &root); err != nil {
		return true
	}
	sisi, _ := root["sisi"].(map[string]any)
	raw := sisi["NextHUB_sites_enabled"]
	if raw == nil {
		return true
	}
	entries, ok := raw.([]any)
	if !ok {
		return true
	}
	if len(entries) == 0 {
		return false
	}
	for _, v := range entries {
		if sanitizeNextHubPlugin(toString(v)) == plugin {
			return true
		}
	}
	return false
}

func nextHubRenderTemplate(tpl, host, plugin string, pg int, search, sortKey, cat, model string, q url.Values) string {
	out := strings.TrimSpace(tpl)
	if out == "" {
		return out
	}
	replace := map[string]string{
		"{host}":   strings.TrimRight(host, "/"),
		"{plugin}": plugin,
		"{page}":   strconv.Itoa(pg),
		"{search}": search,
		"{sort}":   sortKey,
		"{cat}":    cat,
		"{model}":  model,
	}
	for k, v := range replace {
		out = strings.ReplaceAll(out, k, v)
	}
	for key, vals := range q {
		if len(vals) == 0 {
			continue
		}
		out = strings.ReplaceAll(out, "{"+key+"}", vals[0])
	}
	return out
}

func nextHubAbsURL(host, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw
	}
	if strings.HasPrefix(raw, "//") {
		return "https:" + raw
	}
	if strings.HasPrefix(strings.ToLower(raw), "http://") || strings.HasPrefix(strings.ToLower(raw), "https://") {
		return raw
	}
	base, err := url.Parse(strings.TrimRight(host, "/") + "/")
	if err != nil {
		return raw
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return base.ResolveReference(ref).String()
}

func nextHubTotalPages(s nextHubSettings, search, model string) int {
	if search != "" && s.Search.TotalPages > 0 {
		return s.Search.TotalPages
	}
	if model != "" && s.Model.TotalPages > 0 {
		return s.Model.TotalPages
	}
	if s.List.TotalPages > 0 {
		return s.List.TotalPages
	}
	return 1
}

func readConfigFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func nextHubMenuRouteValue(route map[string]string, name string) string {
	if len(route) == 0 {
		return ""
	}
	if v := strings.TrimSpace(route[name]); v != "" {
		return v
	}
	return strings.TrimSpace(route["-"])
}

func nextHubQueryEscape(value, encodingName string) string {
	if value == "" {
		return ""
	}
	name := strings.ToLower(strings.TrimSpace(encodingName))
	switch name {
	case "windows-1251", "cp1251", "win1251":
		if b, err := charmap.Windows1251.NewEncoder().Bytes([]byte(value)); err == nil {
			return url.QueryEscape(string(b))
		}
	}
	return url.QueryEscape(value)
}

func decodeTextWithEncoding(data []byte, encodingName string) string {
	if len(data) == 0 {
		return ""
	}
	name := strings.ToLower(strings.TrimSpace(encodingName))
	switch name {
	case "", "utf-8", "utf8":
		return string(data)
	case "windows-1251", "cp1251", "win1251":
		if decoded, err := charmap.Windows1251.NewDecoder().Bytes(data); err == nil {
			return string(decoded)
		}
	}
	return string(data)
}
