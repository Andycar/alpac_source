package adminhttp

import (
	"archive/zip"
	"bytes"
	"context"
	stdjson "encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/custbal"
	"lampac-go/internal/porter"
	"lampac-go/internal/tgauth"

	"github.com/go-chi/chi/v5"
)

// RegisterConstructorRoutes wires the plugin-constructor + custom-balancer +
// porter admin endpoints. dynRoutes is the host's *DynamicRouteRegistry passed
// as the DynRoutes interface (custbal/porter register live proxy routes).
func RegisterConstructorRoutes(router chi.Router, adminPath string, tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore, custBalPool *custbal.Pool, dynRoutes DynRoutes, cfg config.Config) {
	router.Post("/"+adminPath+"/api/constructor", tgAdminConstructorHandler(tgStore, adminStore))

	custBal := tgAdminCustBalHandler(tgStore, adminStore, custBalPool, dynRoutes)
	router.Get("/"+adminPath+"/api/custbal", custBal)
	router.Post("/"+adminPath+"/api/custbal", custBal)

	router.Post("/"+adminPath+"/api/porter", tgAdminPorterHandler(tgStore, adminStore, custBalPool, dynRoutes, cfg))
	router.Get("/"+adminPath+"/api/porter", tgAdminPorterStatusHandler(tgStore, adminStore))
}

// --- C# balancer constructor: parse .cs and generate Go skeleton ---

// csBalancerInfo holds extracted data from a C# controller file.
type csBalancerInfo struct {
	Name           string            `json:"name"`
	ClassName      string            `json:"class_name"`
	SettingsType   string            `json:"settings_type"`
	Routes         []string          `json:"routes"`
	IndexParams    []csParam         `json:"index_params"`
	APIURLs        []string          `json:"api_urls"`
	HTTPMethods    []string          `json:"http_methods"`
	Headers        map[string]string `json:"headers"`
	RegexPatterns  []string          `json:"regex_patterns"`
	TemplateTypes  []string          `json:"template_types"`
	HasSearch      bool              `json:"has_search"`
	HasSeasons     bool              `json:"has_seasons"`
	HasPlaywright  bool              `json:"has_playwright"`
	HasCookies     bool              `json:"has_cookies"`
	TokenFields    []string          `json:"token_fields"`
	SettingsFields []csSettingsField `json:"settings_fields"`
	CacheKeys      []string          `json:"cache_keys"`
	CacheTTL       []string          `json:"cache_ttl"`
	ContentType    string            `json:"content_type"` // movie, serial, both
	Category       string            `json:"category"`     // dle, api, embed, playerjs
	SearchPattern  string            `json:"search_pattern,omitempty"`
	SourceFiles    []string          `json:"source_file,omitempty"`
	RawCSCode      string            `json:"raw_cs_code"`
	GeneratedGo    string            `json:"generated_go"`
	StandaloneGo   string            `json:"standalone_go,omitempty"`
}

type csParam struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Default string `json:"default,omitempty"`
}

type csSettingsField struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// regex patterns for extracting C# balancer signatures
var (
	reCSClass      = regexp.MustCompile(`class\s+(\w+)\s*:\s*BaseOnlineController(?:<(\w+)>)?`)
	reCSRoute      = regexp.MustCompile(`\[Route\("([^"]+)"\)\]`)
	reCSMethod     = regexp.MustCompile(`\[(HttpGet|HttpPost)\]`)
	reCSIndexSig   = regexp.MustCompile(`(?s)(?:async\s+)?public\s+(?:Task<ActionResult>|ValueTask<ActionResult>)\s+Index\s*\(([^)]+)\)`)
	reCSAPIURL     = regexp.MustCompile(`\$"(https?://[^"]+)"`)
	reCSAPIURLHost = regexp.MustCompile(`\$"\{(?:init\.(?:corsHost\(\)|host|apihost)|apihost)\}(/[^"]+)"`)
	reCSHeader     = regexp.MustCompile(`HeadersModel(?:\.Init)?\s*\(\s*"([^"]+)"\s*,\s*"?([^")]+)"?\s*\)`)
	reCSRegex      = regexp.MustCompile(`Regex\.Match\([^,]+,\s*"([^"]+)"`)
	reCSTemplate   = regexp.MustCompile(`new\s+(MovieTpl|SeasonTpl|EpisodeTpl|VoiceTpl|SimilarTpl|StreamQualityTpl|SubtitleTpl)`)
	reCSCache      = regexp.MustCompile(`InvokeCache(?:Result)?\s*(?:<[^>]+>)?\s*\(\s*\$?"([^"]+)"`)
	reCSCacheTTL   = regexp.MustCompile(`InvokeCache(?:Result)?\s*(?:<[^>]+>)?\s*\([^,]+,\s*(?:TimeSpan\.FromMinutes\()?(\d+)`)
	reCSPlaywright = regexp.MustCompile(`PlaywrightBrowser|NewPageAsync|GotoAsync`)
	reCSCookie     = regexp.MustCompile(`CookieContainer|cookiejar|PHPSESSID`)
	reCSToken      = regexp.MustCompile(`(?:init|_init)\.(token|tokens|apikey|api_key)`)
	reCSSettField  = regexp.MustCompile(`public\s+(string|bool|int|long|string\[\]|int\[\]|bool\?|int\?)\s+(\w+)\s*\{`)
	reCSSeason     = regexp.MustCompile(`SeasonTpl|EpisodeTpl|Season|season|\.s\s*=|int\s+s\s*=`)
	reCSSearch     = regexp.MustCompile(`Search\s*\(|SimilarTpl|similar|PerformSearch`)
	reCSHostProxy  = regexp.MustCompile(`HostStreamProxy\(`)
	reCSConfKey    = regexp.MustCompile(`AppInit\.conf\.(\w+)`)
	reCSConst      = regexp.MustCompile(`(?:private|public|internal)?\s*const\s+string\s+\w+\s*=\s*"(https?://[^"]+)"`)
	reCSRegexRaw   = regexp.MustCompile(`Regex\.Match\w*\([^,]+,\s*@"([^"]+)"`)
	reCSAPIHost    = regexp.MustCompile(`_init\.(apihost|corsHost|host|webcorshost)`)
)

// reCustBalName / isValidCustBalName live in registry_iface.go (shared with the
// drochub/balancers admin slices) — this file uses that shared copy.

func parseCSBalancer(code string) csBalancerInfo {
	// Strip NUL bytes and BOM that may come from C# source files
	code = strings.ReplaceAll(code, "\x00", "")
	code = strings.ReplaceAll(code, "\xef\xbb\xbf", "")

	info := csBalancerInfo{
		Headers:   make(map[string]string),
		RawCSCode: code,
	}

	// Class name and settings type
	if m := reCSClass.FindStringSubmatch(code); len(m) >= 2 {
		info.ClassName = m[1]
		name := strings.ToLower(m[1])
		// Strip common suffixes (e.g., MakhnoController -> makhno)
		name = strings.TrimSuffix(name, "controller")
		name = strings.TrimSuffix(name, "checker")
		info.Name = name
		if len(m) >= 3 && m[2] != "" {
			info.SettingsType = m[2]
		}
	}

	// Routes
	for _, m := range reCSRoute.FindAllStringSubmatch(code, -1) {
		info.Routes = append(info.Routes, m[1])
	}
	// Derive name from first route if not set
	if info.Name == "" && len(info.Routes) > 0 {
		r := info.Routes[0]
		r = strings.TrimPrefix(r, "lite/")
		parts := strings.Split(r, "/")
		info.Name = strings.ToLower(parts[0])
	}
	// Try namespace if still empty
	if info.Name == "" {
		reNS := regexp.MustCompile(`namespace\s+(\w+)`)
		if m := reNS.FindStringSubmatch(code); len(m) >= 2 {
			ns := strings.ToLower(m[1])
			if ns != "online" && ns != "shared" && ns != "controllers" {
				info.Name = ns
			}
		}
	}

	// HTTP methods
	methods := map[string]bool{}
	for _, m := range reCSMethod.FindAllStringSubmatch(code, -1) {
		methods[m[1]] = true
	}
	for m := range methods {
		info.HTTPMethods = append(info.HTTPMethods, m)
	}

	// Index parameters
	if m := reCSIndexSig.FindStringSubmatch(code); len(m) >= 2 {
		info.IndexParams = parseCSParams(m[1])
	}

	// API URLs
	seen := map[string]bool{}
	// Constants with URLs (e.g., const string WormholeHost = "http://...")
	for _, m := range reCSConst.FindAllStringSubmatch(code, -1) {
		u := m[1]
		if !seen[u] {
			info.APIURLs = append(info.APIURLs, u)
			seen[u] = true
		}
	}
	for _, m := range reCSAPIURLHost.FindAllStringSubmatch(code, -1) {
		u := m[1]
		if !seen[u] {
			info.APIURLs = append(info.APIURLs, u)
			seen[u] = true
		}
	}
	for _, m := range reCSAPIURL.FindAllStringSubmatch(code, -1) {
		u := m[1]
		if !seen[u] && len(u) < 200 {
			info.APIURLs = append(info.APIURLs, u)
			seen[u] = true
		}
	}

	// Headers
	for _, m := range reCSHeader.FindAllStringSubmatch(code, -1) {
		info.Headers[m[1]] = m[2]
	}

	// Regex patterns (both regular "..." and raw @"..." strings)
	seenRe := map[string]bool{}
	for _, m := range reCSRegex.FindAllStringSubmatch(code, -1) {
		if !seenRe[m[1]] {
			info.RegexPatterns = append(info.RegexPatterns, m[1])
			seenRe[m[1]] = true
		}
	}
	for _, m := range reCSRegexRaw.FindAllStringSubmatch(code, -1) {
		if !seenRe[m[1]] {
			info.RegexPatterns = append(info.RegexPatterns, m[1])
			seenRe[m[1]] = true
		}
	}

	// Template types
	seenTpl := map[string]bool{}
	for _, m := range reCSTemplate.FindAllStringSubmatch(code, -1) {
		if !seenTpl[m[1]] {
			info.TemplateTypes = append(info.TemplateTypes, m[1])
			seenTpl[m[1]] = true
		}
	}

	// Cache keys and TTL
	for _, m := range reCSCache.FindAllStringSubmatch(code, -1) {
		info.CacheKeys = append(info.CacheKeys, m[1])
	}
	for _, m := range reCSCacheTTL.FindAllStringSubmatch(code, -1) {
		info.CacheTTL = append(info.CacheTTL, m[1]+"min")
	}

	// Capabilities
	info.HasPlaywright = reCSPlaywright.MatchString(code)
	info.HasCookies = reCSCookie.MatchString(code)
	info.HasSearch = reCSSearch.MatchString(code)
	info.HasSeasons = reCSSeason.MatchString(code)

	// Token fields
	seenTok := map[string]bool{}
	for _, m := range reCSToken.FindAllStringSubmatch(code, -1) {
		if !seenTok[m[1]] {
			info.TokenFields = append(info.TokenFields, m[1])
			seenTok[m[1]] = true
		}
	}

	// Content type detection
	hasMovie := false
	hasSeries := false
	for _, t := range info.TemplateTypes {
		switch t {
		case "MovieTpl":
			hasMovie = true
		case "SeasonTpl", "EpisodeTpl":
			hasSeries = true
		}
	}
	if hasMovie && hasSeries {
		info.ContentType = "both"
	} else if hasSeries {
		info.ContentType = "serial"
	} else {
		info.ContentType = "movie"
	}

	// Settings fields (from inline or referenced settings class)
	for _, m := range reCSSettField.FindAllStringSubmatch(code, -1) {
		info.SettingsFields = append(info.SettingsFields, csSettingsField{
			Name: m[2],
			Type: m[1],
		})
	}

	// Category detection
	info.Category = detectCSCategory(info)

	// Search result CSS class for DLE sites
	reSearchClass := regexp.MustCompile(`(?:Rx\.Split|class=")[^"]*?([\w-]*wrap[\w-]*|short-story|sres-wrap)`)
	if m := reSearchClass.FindStringSubmatch(code); len(m) >= 2 {
		info.SearchPattern = m[1]
	}

	// Generate Go code
	info.GeneratedGo = generateGoBalancer(info)

	return info
}

// detectCSCategory classifies a C# balancer by its primary pattern using weighted signals.
func detectCSCategory(info csBalancerInfo) string {
	scores := map[string]int{"dle": 0, "api": 0, "embed": 0, "playerjs": 0}
	raw := info.RawCSCode

	// DLE signals
	if strings.Contains(raw, "do=search") || strings.Contains(raw, "subaction=search") {
		scores["dle"] += 3
	}
	if strings.Contains(raw, "sres-wrap") || strings.Contains(raw, "short-story") {
		scores["dle"] += 2
	}
	if strings.Contains(raw, "index.php?do=search") {
		scores["dle"] += 2
	}

	// API signals
	if strings.Contains(raw, "/api/") || strings.Contains(raw, "/API/") {
		scores["api"] += 3
	}
	for _, f := range info.TokenFields {
		if f == "token" || f == "apikey" || f == "api_key" {
			scores["api"] += 2
		}
	}
	if strings.Contains(raw, "JsonSerializer.Deserialize") || strings.Contains(raw, "JObject") || strings.Contains(raw, "JsonConvert") {
		scores["api"] += 1
	}

	// Embed signals
	if strings.Contains(raw, "/embed/kp/") || strings.Contains(raw, "/embed/imdb/") {
		scores["embed"] += 3
	}
	if strings.Contains(raw, "makePlayer") {
		scores["embed"] += 2
	}
	if strings.Contains(raw, "seasons:") && (strings.Contains(raw, "hls:") || strings.Contains(raw, `hls"`)) {
		scores["embed"] += 2
	}

	// Playerjs signals
	if strings.Contains(raw, "playerjsfile") || strings.Contains(raw, `file:'[`) || strings.Contains(raw, `file:"[`) {
		scores["playerjs"] += 3
	}
	if strings.Contains(raw, "folder") && (strings.Contains(raw, "Season[]") || strings.Contains(raw, "Folder")) {
		scores["playerjs"] += 2
	}

	best := "dle"
	bestScore := 0
	for cat, score := range scores {
		if score > bestScore {
			best = cat
			bestScore = score
		}
	}
	return best
}

func parseCSParams(raw string) []csParam {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := splitCSParams(raw)
	var params []csParam
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// Handle default values: "int s = -1"
		var def string
		if idx := strings.Index(p, "="); idx >= 0 {
			def = strings.TrimSpace(p[idx+1:])
			p = strings.TrimSpace(p[:idx])
		}
		// Handle nullable: "int? s"
		tokens := strings.Fields(p)
		if len(tokens) >= 2 {
			params = append(params, csParam{
				Name:    tokens[len(tokens)-1],
				Type:    strings.Join(tokens[:len(tokens)-1], " "),
				Default: def,
			})
		}
	}
	return params
}

func splitCSParams(s string) []string {
	var result []string
	depth := 0
	start := 0
	for i, c := range s {
		switch c {
		case '<', '(':
			depth++
		case '>', ')':
			depth--
		case ',':
			if depth == 0 {
				result = append(result, s[start:i])
				start = i + 1
			}
		}
	}
	result = append(result, s[start:])
	return result
}

// --- Go code generation ---

func generateGoBalancer(info csBalancerInfo) string {
	if info.Name == "" {
		return "// Error: could not determine balancer name from C# code"
	}
	name := info.Name
	nameTitle := strings.ToUpper(name[:1]) + name[1:]
	configKey := nameTitle
	hasToken := len(info.TokenFields) > 0
	hasRegex := len(info.RegexPatterns) > 0
	hasKPID := false
	hasIMDB := false
	for _, p := range info.IndexParams {
		switch p.Name {
		case "kinopoisk_id":
			hasKPID = true
		case "imdb_id":
			hasIMDB = true
		}
	}

	defaultHost := ""
	mainAPIPath := ""
	if len(info.APIURLs) > 0 {
		defaultHost = info.APIURLs[0]
		mainAPIPath = info.APIURLs[0]
	}
	for _, u := range info.APIURLs {
		if strings.HasPrefix(u, "/") {
			mainAPIPath = u
			break
		}
	}

	var sb strings.Builder
	w := func(s string) { sb.WriteString(s) }
	wl := func(s string) { sb.WriteString(s); sb.WriteByte('\n') }
	wf := func(format string, args ...any) { fmt.Fprintf(&sb, format, args...) }

	wl("package httpapi")
	wl("")
	wl("import (")
	wl("\t\"context\"")
	wl("\t\"io\"")
	wl("\t\"net/http\"")
	wl("\t\"strings\"")
	wl("\t\"time\"")
	if hasRegex {
		wl("\t\"regexp\"")
	}
	wl("\tstdjson \"encoding/json\"")
	wl("")
	wl("\t\"lampac-go/internal/config\"")
	wl("\t\"lampac-go/internal/httpclient\"")
	wl("\t\"lampac-go/internal/proxylink\"")
	wl(")")
	wl("")
	wf("// ============================================================\n")
	wf("// %s balancer — auto-generated from %s.cs\n", name, info.ClassName)
	wf("// ============================================================\n\n")

	// Regex vars — use backtick-safe escaping, skip invalid Go patterns.
	for i, p := range info.RegexPatterns {
		if _, err := regexp.Compile(p); err != nil {
			wf("// var %sRe%d — invalid Go regexp: %s\n", name, i+1, p)
			continue
		}
		if strings.ContainsRune(p, '`') {
			escaped := strings.ReplaceAll(p, `\`, `\\`)
			escaped = strings.ReplaceAll(escaped, `"`, `\"`)
			wf("var %sRe%d = regexp.MustCompile(\"%s\")\n", name, i+1, escaped)
		} else {
			wf("var %sRe%d = regexp.MustCompile(`%s`)\n", name, i+1, p)
		}
	}
	wl("")

	// Struct
	wf("type %sChecker struct {\n", name)
	wl("\tclient *http.Client")
	wl("\thost   string")
	if hasToken {
		wl("\ttoken  string")
	}
	wl("}")
	wl("")

	// Constructor
	wf("func new%sChecker(cfg config.Config) *%sChecker {\n", nameTitle, name)
	wf("\thost := strings.TrimSpace(cfg.Online.%sHost)\n", configKey)
	wl("\tif host == \"\" {")
	wf("\t\thost = \"%s\"\n", defaultHost)
	wl("\t}")
	wl("\thost = strings.TrimRight(host, \"/\")")
	wl("")
	wf("\treturn &%sChecker{\n", name)
	wf("\t\tclient: httpclient.NewForBalancer(\"%s\", 10*time.Second),\n", name)
	wl("\t\thost:   host,")
	if hasToken {
		wf("\t\ttoken:  strings.TrimSpace(cfg.Online.%sToken),\n", configKey)
	}
	wl("\t}")
	wl("}")
	wl("")

	// handle
	wf("func (c *%sChecker) handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {\n", name)
	wl("\treturn func(w http.ResponseWriter, req *http.Request) {")
	wl("\t\tif parseBoolParam(req.URL.Query().Get(\"checksearch\")) {")
	wl("\t\t\tshow := c.checkSearch(req)")
	wl("\t\t\twriteCheckSearchResponse(w, show, \"FHD\")")
	wl("\t\t\treturn")
	wl("\t\t}")
	wl("")
	wl("\t\tc.index(w, req, links)")
	wl("\t}")
	wl("}")
	wl("")

	// checkSearch
	wf("func (c *%sChecker) checkSearch(req *http.Request) bool {\n", name)
	wl("\tq := req.URL.Query()")
	wl("\ttitle := strings.TrimSpace(q.Get(\"title\"))")
	wl("\tif title == \"\" {")
	wl("\t\treturn false")
	wl("\t}")
	wl("")
	wl("\t// TODO: implement search check against API")
	wl("\tctx, cancel := context.WithTimeout(req.Context(), 8*time.Second)")
	wl("\tdefer cancel()")
	wl("\t_ = ctx")
	wl("")
	wl("\treturn false")
	wl("}")
	wl("")

	// index
	wf("func (c *%sChecker) index(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {\n", name)
	wl("\tq := req.URL.Query()")
	wl("\trjson := parseBoolParam(q.Get(\"rjson\"))")
	wl("\ttitle := strings.TrimSpace(q.Get(\"title\"))")
	wl("\toriginalTitle := strings.TrimSpace(q.Get(\"original_title\"))")
	if hasKPID {
		wl("\tkpID := q.Get(\"kinopoisk_id\")")
	}
	if hasIMDB {
		wl("\timdbID := q.Get(\"imdb_id\")")
	}
	if info.HasSeasons {
		wl("\tseason, _ := getsTVQueryInt(q.Get(\"s\"))")
	}
	wl("")
	wl("\tif title == \"\" && originalTitle == \"\" {")
	wl("\t\twriteGetsTVEmpty(w, rjson)")
	wl("\t\treturn")
	wl("\t}")
	wl("")

	// Suppress unused variable warnings
	if hasKPID {
		w("\t_ = kpID\n")
	}
	if hasIMDB {
		w("\t_ = imdbID\n")
	}

	switch info.ContentType {
	case "movie":
		wl("\t// --- Movie ---")
		wl("\tc.writeMovie(w, req, rjson, title, originalTitle, links)")
	case "serial":
		wl("\t// --- Serial ---")
		wl("\tif season == -1 {")
		wl("\t\tc.writeSeasons(w, req, rjson, title, originalTitle, links)")
		wl("\t} else {")
		wl("\t\tc.writeEpisodes(w, req, rjson, title, originalTitle, season, links)")
		wl("\t}")
	default: // "both"
		wl("\t// --- Movie or Serial ---")
		wl("\t// TODO: detect content type from API response")
		wl("\tif season == -1 {")
		wl("\t\tc.writeMovie(w, req, rjson, title, originalTitle, links)")
		wl("\t} else {")
		wl("\t\tc.writeEpisodes(w, req, rjson, title, originalTitle, season, links)")
		wl("\t}")
	}
	wl("}")
	wl("")

	// fetch
	wf("func (c *%sChecker) fetch(ctx context.Context, target string) (string, bool) {\n", name)
	wl("\thttpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)")
	wl("\tif err != nil {")
	wl("\t\treturn \"\", false")
	wl("\t}")
	wl("\thttpReq.Header.Set(\"User-Agent\", \"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36\")")
	wl("\thttpReq.Header.Set(\"X-Lampac-Go\", \"1\")")
	for k, v := range info.Headers {
		wf("\thttpReq.Header.Set(\"%s\", \"%s\")\n", k, v)
	}
	wl("")
	wl("\tresp, err := c.client.Do(httpReq)")
	wl("\tif err != nil {")
	wl("\t\treturn \"\", false")
	wl("\t}")
	wl("\tdefer resp.Body.Close()")
	wl("\tif resp.StatusCode < 200 || resp.StatusCode >= 300 {")
	wl("\t\treturn \"\", false")
	wl("\t}")
	wl("\tbody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))")
	wl("\tif err != nil {")
	wl("\t\treturn \"\", false")
	wl("\t}")
	wl("\treturn string(body), true")
	wl("}")
	wl("")

	// writeMovie
	wf("func (c *%sChecker) writeMovie(w http.ResponseWriter, req *http.Request, rjson bool, title, originalTitle string, links *proxylink.Manager) {\n", name)
	wl("\tctx, cancel := context.WithTimeout(req.Context(), 10*time.Second)")
	wl("\tdefer cancel()")
	wl("")
	wl("\t// TODO: build API URL and fetch content")
	wf("\tapiURL := c.host + \"%s\"\n", mainAPIPath)
	wl("\tbody, ok := c.fetch(ctx, apiURL)")
	wl("\tif !ok || body == \"\" {")
	wl("\t\twriteGetsTVEmpty(w, rjson)")
	wl("\t\treturn")
	wl("\t}")
	wl("")
	wl("\t// TODO: parse response and extract stream URL")
	wl("\t_ = body")
	wl("\tstreamURL := \"\" // TODO: extract from response")
	wl("")
	wl("\tif streamURL == \"\" {")
	wl("\t\twriteGetsTVEmpty(w, rjson)")
	wl("\t\treturn")
	wl("\t}")
	wl("")
	wf("\tproxyURL := streamProxyURL(req, streamURL, \"%s\", links)\n", name)
	wl("")
	wl("\trow := map[string]any{")
	wl("\t\t\"method\": \"play\",")
	wl("\t\t\"url\":    proxyURL,")
	wl("\t\t\"stream\": proxyURL,")
	wl("\t\t\"name\":   title,")
	wl("\t\t\"title\":  getsTVJoinName(title, originalTitle),")
	wl("\t}")
	wl("")
	wl("\tif rjson {")
	wl("\t\twriteJSON(w, http.StatusOK, map[string]any{")
	wl("\t\t\t\"type\": \"movie\",")
	wl("\t\t\t\"data\": []map[string]any{row},")
	wl("\t\t})")
	wl("\t\treturn")
	wl("\t}")
	wl("")
	wl("\tvar htmlSB strings.Builder")
	wl("\thtmlSB.WriteString(`<div class=\"videos__line\">`)")
	wl("\tgetsTVAppendMovieHTML(&htmlSB, row, \"По умолчанию\", true, 0, 0)")
	wl("\thtmlSB.WriteString(`</div>`)")
	wl("\twriteHTML(w, http.StatusOK, htmlSB.String())")
	wl("}")

	if info.HasSeasons || info.ContentType == "both" {
		wl("")
		wf("func (c *%sChecker) writeSeasons(w http.ResponseWriter, req *http.Request, rjson bool, title, originalTitle string, links *proxylink.Manager) {\n", name)
		wl("\t// TODO: fetch seasons list from API")
		wl("\twriteGetsTVEmpty(w, rjson)")
		wl("}")
		wl("")
		wf("func (c *%sChecker) writeEpisodes(w http.ResponseWriter, req *http.Request, rjson bool, title, originalTitle string, season int, links *proxylink.Manager) {\n", name)
		wl("\t// TODO: fetch episodes for season from API")
		wl("\twriteGetsTVEmpty(w, rjson)")
		wl("}")
	}

	// Suppress unused imports
	_ = hasRegex

	return sb.String()
}

// --- Zip/multi-file support ---

// extractCSFromZip reads a zip archive and returns all .cs files as name→content map.
func extractCSFromZip(data []byte) (map[string]string, error) {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	files := make(map[string]string)
	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(f.Name), ".cs") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		content, err := io.ReadAll(io.LimitReader(rc, 1<<20))
		rc.Close()
		if err != nil {
			continue
		}
		files[f.Name] = string(content)
	}
	return files, nil
}

// mergeCSFiles orders and concatenates .cs files for unified parsing.
// Order: Controller.cs → OnlineApi.cs → *Invoke.cs → Models/*.cs → rest.
func mergeCSFiles(files map[string]string) (string, []string) {
	type entry struct {
		name    string
		content string
		order   int
	}

	var entries []entry
	for name, content := range files {
		base := path.Base(name)
		lower := strings.ToLower(base)
		order := 50 // default
		switch {
		case lower == "controller.cs":
			order = 10
		case lower == "onlineapi.cs":
			order = 20
		case strings.HasSuffix(lower, "invoke.cs"):
			order = 30
		case strings.Contains(strings.ToLower(name), "model"):
			order = 40
		case lower == "modinit.cs":
			order = 60
		case lower == "apnhelper.cs":
			order = 70
		}
		entries = append(entries, entry{name: name, content: content, order: order})
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].order != entries[j].order {
			return entries[i].order < entries[j].order
		}
		return entries[i].name < entries[j].name
	})

	var sb strings.Builder
	var names []string
	for _, e := range entries {
		sb.WriteString("// === FILE: " + e.name + " ===\n")
		sb.WriteString(e.content)
		sb.WriteString("\n\n")
		names = append(names, e.name)
	}
	return sb.String(), names
}

// --- Standalone balancer generator (enhanced auto-porter) ---

func generateStandaloneBalancer(info csBalancerInfo, port int) string {
	name := info.Name
	if name == "" {
		return "// Error: could not determine balancer name"
	}
	cat := info.Category
	if cat == "" {
		cat = detectCSCategory(info)
	}

	var sb strings.Builder
	wl := func(s string) { sb.WriteString(s); sb.WriteByte('\n') }
	wf := func(format string, args ...any) { fmt.Fprintf(&sb, format, args...) }

	hasSerial := info.HasSeasons || info.ContentType == "serial" || info.ContentType == "both"

	// Pipeline of emitters
	saEmitImports(&sb, info, cat, hasSerial)
	saEmitConstants(&sb, info, cat)
	saEmitRegexVars(&sb, info, cat)
	saEmitDataStructs(&sb, info, cat, hasSerial)
	saEmitCacheSystem(&sb)
	saEmitServerStruct(&sb, info, cat)
	saEmitMainFunc(&sb, info, port, cat)
	saEmitHandleFunc(&sb, info, cat, hasSerial)
	saEmitCheckSearch(&sb, info, cat)
	saEmitIndexFunc(&sb, info, cat, hasSerial)
	switch cat {
	case "dle":
		saEmitSearchDLE(&sb, info)
	case "api":
		saEmitSearchAPI(&sb, info)
	default:
		saEmitSearchGeneric(&sb, info, cat)
	}
	if info.HasSearch {
		saEmitSimilarHandler(&sb, info, cat)
	}
	saEmitMovieHandler(&sb, info, cat)
	if hasSerial {
		saEmitSerialHandler(&sb, info, cat)
	}
	saEmitFetchHelpers(&sb, info, cat)
	switch cat {
	case "dle":
		saEmitHTMLHelpers(&sb)
	case "playerjs":
		saEmitPlayerjsParser(&sb)
	case "embed":
		saEmitEmbedParser(&sb, info)
	}
	saEmitOutputHelpers(&sb)
	saEmitCSReference(&sb, info)

	// Final unused-import guards
	_ = wl
	_ = wf

	return sb.String()
}

// --- Emitter helpers ---

func saDefaultHost(info csBalancerInfo) string {
	if len(info.APIURLs) > 0 {
		return info.APIURLs[0]
	}
	return ""
}

func saMainAPIPath(info csBalancerInfo) string {
	for _, u := range info.APIURLs {
		if strings.HasPrefix(u, "/") {
			return u
		}
	}
	return ""
}

func saHasValidRegex(info csBalancerInfo) bool {
	for _, p := range info.RegexPatterns {
		if _, err := regexp.Compile(p); err == nil {
			return true
		}
	}
	return false
}

// --- Individual emitters ---

func saEmitImports(sb *strings.Builder, info csBalancerInfo, cat string, hasSerial bool) {
	wl := func(s string) { sb.WriteString(s); sb.WriteByte('\n') }

	wl("package main")
	wl("")
	wl("import (")
	wl("\t\"encoding/json\"")
	wl("\t\"flag\"")
	wl("\t\"fmt\"")
	wl("\t\"io\"")
	wl("\t\"log\"")
	wl("\t\"net/http\"")
	wl("\t\"net/url\"")
	if saHasValidRegex(info) || cat == "dle" || cat == "embed" || cat == "playerjs" {
		wl("\t\"regexp\"")
	}
	if hasSerial {
		wl("\t\"sort\"")
	}
	wl("\t\"strconv\"")
	wl("\t\"strings\"")
	wl("\t\"sync\"")
	wl("\t\"time\"")
	wl(")")
	wl("")
}

func saEmitConstants(sb *strings.Builder, info csBalancerInfo, cat string) {
	wf := func(format string, args ...any) { fmt.Fprintf(sb, format, args...) }
	wl := func(s string) { sb.WriteString(s); sb.WriteByte('\n') }

	wf("const balancerName = %q\n", info.Name)
	wl("")
	wf("var defaultHost = %q\n", saDefaultHost(info))
	wl("")

	if cat == "dle" {
		wl("const searchPath = \"/index.php?do=search\"")
		wl("")
	}
	if p := saMainAPIPath(info); p != "" {
		wf("const mainAPIPath = %q\n", p)
		wl("")
	}
}

func saEmitRegexVars(sb *strings.Builder, info csBalancerInfo, cat string) {
	wf := func(format string, args ...any) { fmt.Fprintf(sb, format, args...) }
	wl := func(s string) { sb.WriteString(s); sb.WriteByte('\n') }

	// C# regex patterns with usage comments
	validCount := 0
	for i, p := range info.RegexPatterns {
		if _, err := regexp.Compile(p); err != nil {
			wf("// var reCS%d — invalid Go regexp (from C#): %s\n", i+1, p)
			continue
		}
		validCount++
		if strings.ContainsRune(p, '`') {
			escaped := strings.ReplaceAll(p, `\`, `\\`)
			escaped = strings.ReplaceAll(escaped, `"`, `\"`)
			wf("var reCS%d = regexp.MustCompile(\"%s\") // C# regex#%d\n", i+1, escaped, i+1)
		} else {
			wf("var reCS%d = regexp.MustCompile(`%s`) // C# regex#%d\n", i+1, p, i+1)
		}
	}
	if validCount > 0 {
		wl("")
	}

	// Standard regexes per category
	switch cat {
	case "dle":
		wl("var (")
		wl("\treResultLink  = regexp.MustCompile(`(?i)href=\"([^\"]+)\"`) ")
		wl("\treResultTitle = regexp.MustCompile(`(?is)class=\"[^\"]*title[^\"]*\"[^>]*>([^<]+)<`)")
		wl("\treYear        = regexp.MustCompile(`\\b((?:19|20)\\d{2})\\b`)")
		wl("\treIframeSrc   = regexp.MustCompile(`(?is)<iframe[^>]+src=\"([^\"]+)\"`)")
		wl("\trePlayerFile  = regexp.MustCompile(`(?i)file\\s*[:=]\\s*[\"']([^\"']+\\.m3u8[^\"']*)[\"']`)")
		wl("\trePlayerFileMP4 = regexp.MustCompile(`(?i)file\\s*[:=]\\s*[\"']([^\"']+\\.mp4[^\"']*)[\"']`)")
		wl("\trePosterImg   = regexp.MustCompile(`(?is)<img[^>]+src=\"([^\"]+)\"[^>]*class=\"[^\"]*poster`)")
		wl(")")
		wl("")
	case "embed":
		wl("var (")
		wl("\treHLS      = regexp.MustCompile(`(?i)hls\\s*[:=]\\s*\"([^\"]+\\.m3u8[^\"]*)\"`) ")
		wl("\treDash     = regexp.MustCompile(`(?i)dash[a]?\\s*[:=]\\s*\"([^\"]+\\.mpd[^\"]*)\"`) ")
		wl("\treSeasons  = regexp.MustCompile(`(?is)seasons\\s*:\\s*(\\[.+?\\])\\s*[,;]`)")
		wl("\treAudioNames = regexp.MustCompile(`(?is)audio\\s*:\\s*\\{[^}]*names\\s*:\\s*(\\[[^\\]]+\\])`)")
		wl(")")
		wl("")
	case "playerjs":
		wl("var (")
		wl("\trePlayerjsFile    = regexp.MustCompile(`(?i)file\\s*[:=]\\s*'(\\[.+?\\])'`)")
		wl("\treQualityFromURL  = regexp.MustCompile(`\\[(\\d+p?)\\]([^\\[,;\\s]+)`)")
		wl(")")
		wl("")
	}
}

func saEmitDataStructs(sb *strings.Builder, info csBalancerInfo, cat string, hasSerial bool) {
	wl := func(s string) { sb.WriteString(s); sb.WriteByte('\n') }

	wl("type searchResult struct {")
	wl("\tTitle     string")
	wl("\tURL       string")
	wl("\tYear      int")
	wl("\tPosterURL string")
	wl("}")
	wl("")

	if hasSerial {
		wl("type voiceInfo struct {")
		wl("\tName       string")
		wl("\tPlayerType string")
		wl("\tSeasons    map[int][]episodeInfo")
		wl("}")
		wl("")
		wl("type episodeInfo struct {")
		wl("\tNumber   int")
		wl("\tTitle    string")
		wl("\tFile     string")
		wl("\tSubtitle string")
		wl("}")
		wl("")
	}

	if cat == "embed" {
		wl("type embedSeason struct {")
		wl("\tSeason   int            `json:\"season\"`")
		wl("\tEpisodes []embedEpisode `json:\"episodes\"`")
		wl("}")
		wl("")
		wl("type embedEpisode struct {")
		wl("\tEpisode string `json:\"episode\"`")
		wl("\tHLS     string `json:\"hls\"`")
		wl("\tDash    string `json:\"dash\"`")
		wl("\tAudio   struct {")
		wl("\t\tNames []string `json:\"names\"`")
		wl("\t} `json:\"audio\"`")
		wl("}")
		wl("")
	}

	if cat == "playerjs" || hasSerial {
		wl("type playerjsItem struct {")
		wl("\tTitle    string          `json:\"title\"`")
		wl("\tFile     string          `json:\"file\"`")
		wl("\tComment  string          `json:\"comment\"`")
		wl("\tSubtitle string          `json:\"subtitle\"`")
		wl("\tFolder   []playerjsItem  `json:\"folder\"`")
		wl("}")
		wl("")
	}

	if cat == "api" {
		wl("// apiSearchResponse — adjust fields to match the actual API response.")
		wl("type apiSearchResponse struct {")
		wl("\tData []apiItem `json:\"data\"`")
		wl("}")
		wl("")
		wl("type apiItem struct {")
		wl("\tID    int    `json:\"id\"`")
		wl("\tTitle string `json:\"title\"`")
		wl("\tYear  int    `json:\"year\"`")
		wl("}")
		wl("")
	}
}

func saEmitCacheSystem(sb *strings.Builder) {
	wl := func(s string) { sb.WriteString(s); sb.WriteByte('\n') }

	wl("// --- Cache system ---")
	wl("")
	wl("type cacheItem struct {")
	wl("\tvalue     any")
	wl("\texpiresAt time.Time")
	wl("}")
	wl("")
	wl("type simpleCache struct {")
	wl("\tmu    sync.RWMutex")
	wl("\titems map[string]cacheItem")
	wl("}")
	wl("")
	wl("func newCache() *simpleCache {")
	wl("\tc := &simpleCache{items: make(map[string]cacheItem)}")
	wl("\tgo c.cleanup()")
	wl("\treturn c")
	wl("}")
	wl("")
	wl("func (c *simpleCache) get(key string) (any, bool) {")
	wl("\tc.mu.RLock()")
	wl("\tdefer c.mu.RUnlock()")
	wl("\titem, ok := c.items[key]")
	wl("\tif !ok || time.Now().After(item.expiresAt) {")
	wl("\t\treturn nil, false")
	wl("\t}")
	wl("\treturn item.value, true")
	wl("}")
	wl("")
	wl("func (c *simpleCache) set(key string, value any, ttl time.Duration) {")
	wl("\tc.mu.Lock()")
	wl("\tc.items[key] = cacheItem{value: value, expiresAt: time.Now().Add(ttl)}")
	wl("\tc.mu.Unlock()")
	wl("}")
	wl("")
	wl("func (c *simpleCache) cleanup() {")
	wl("\tfor {")
	wl("\t\ttime.Sleep(5 * time.Minute)")
	wl("\t\tc.mu.Lock()")
	wl("\t\tnow := time.Now()")
	wl("\t\tfor k, v := range c.items {")
	wl("\t\t\tif now.After(v.expiresAt) {")
	wl("\t\t\t\tdelete(c.items, k)")
	wl("\t\t\t}")
	wl("\t\t}")
	wl("\t\tc.mu.Unlock()")
	wl("\t}")
	wl("}")
	wl("")
}

func saEmitServerStruct(sb *strings.Builder, info csBalancerInfo, cat string) {
	wl := func(s string) { sb.WriteString(s); sb.WriteByte('\n') }

	wl("type server struct {")
	wl("\tclient   *http.Client")
	wl("\thost     string")
	wl("\tmainHost string")
	wl("\tcache    *simpleCache")
	if len(info.TokenFields) > 0 {
		wl("\ttoken    string")
	}
	if info.HasCookies {
		wl("\t// cookieJar *cookiejar.Jar // MANUAL: add if DDoS-Guard/session cookies needed")
	}
	wl("}")
	wl("")
	wl("func (s *server) selfHost() string {")
	wl("\tif s.mainHost != \"\" {")
	wl("\t\treturn s.mainHost + \"/lite/\" + balancerName")
	wl("\t}")
	wl("\treturn \"\"")
	wl("}")
	wl("")
}

func saEmitMainFunc(sb *strings.Builder, info csBalancerInfo, port int, cat string) {
	wf := func(format string, args ...any) { fmt.Fprintf(sb, format, args...) }
	wl := func(s string) { sb.WriteString(s); sb.WriteByte('\n') }

	wl("func main() {")
	wf("\tport := flag.Int(\"port\", %d, \"HTTP listen port\")\n", port)
	wl("\tmainHost := flag.String(\"main-host\", \"\", \"main lampac-go server address\")")
	wl("\thost := flag.String(\"host\", \"\", \"upstream host override\")")
	if len(info.TokenFields) > 0 {
		wl("\ttoken := flag.String(\"token\", \"\", \"API token\")")
	}
	wl("\tflag.Parse()")
	wl("")
	wl("\tupstreamHost := defaultHost")
	wl("\tif *host != \"\" {")
	wl("\t\tupstreamHost = strings.TrimRight(*host, \"/\")")
	wl("\t}")
	wl("")
	wl("\ts := &server{")
	wl("\t\tclient: &http.Client{")
	wl("\t\t\tTimeout: 20 * time.Second,")
	wl("\t\t\tCheckRedirect: func(req *http.Request, via []*http.Request) error {")
	wl("\t\t\t\tif len(via) >= 5 {")
	wl("\t\t\t\t\treturn fmt.Errorf(\"too many redirects\")")
	wl("\t\t\t\t}")
	wl("\t\t\t\treturn nil")
	wl("\t\t\t},")
	wl("\t\t},")
	wl("\t\thost:     upstreamHost,")
	wl("\t\tmainHost: *mainHost,")
	wl("\t\tcache:    newCache(),")
	if len(info.TokenFields) > 0 {
		wl("\t\ttoken:    *token,")
	}
	wl("\t}")
	wl("")
	wl("\tmux := http.NewServeMux()")
	wl("\tmux.HandleFunc(\"/\", s.handle)")
	wl("")
	wl("\taddr := fmt.Sprintf(\"127.0.0.1:%d\", *port)")
	wf("\tlog.Printf(\"%s: listening on %%s (upstream: %%s)\", addr, upstreamHost)\n", info.Name)
	wl("\tlog.Fatal(http.ListenAndServe(addr, mux))")
	wl("}")
	wl("")
}

func saEmitHandleFunc(sb *strings.Builder, info csBalancerInfo, cat string, hasSerial bool) {
	wl := func(s string) { sb.WriteString(s); sb.WriteByte('\n') }

	wl("func (s *server) handle(w http.ResponseWriter, r *http.Request) {")
	wl("\tq := r.URL.Query()")
	wl("")
	wl("\t// Checksearch mode")
	wl("\tif q.Get(\"checksearch\") == \"true\" || q.Get(\"checksearch\") == \"1\" {")
	wl("\t\ts.handleCheckSearch(w, r)")
	wl("\t\treturn")
	wl("\t}")
	wl("")
	wl("\ts.index(w, r)")
	wl("}")
	wl("")
}

func saEmitCheckSearch(sb *strings.Builder, info csBalancerInfo, cat string) {
	wl := func(s string) { sb.WriteString(s); sb.WriteByte('\n') }

	wl("func (s *server) handleCheckSearch(w http.ResponseWriter, r *http.Request) {")
	wl("\tq := r.URL.Query()")
	wl("\ttitle := strings.TrimSpace(q.Get(\"title\"))")
	wl("\tif title == \"\" {")
	wl("\t\trespondJSON(w, map[string]any{\"rch\": false})")
	wl("\t\treturn")
	wl("\t}")
	wl("")
	wl("\tresults := s.search(title)")
	wl("\tif len(results) == 0 {")
	wl("\t\trespondJSON(w, map[string]any{\"rch\": false})")
	wl("\t\treturn")
	wl("\t}")
	wl("")
	wl("\t// Content found")
	wl("\trespondJSON(w, map[string]any{\"rch\": false, \"type\": \"movie\"})")
	wl("}")
	wl("")
}

func saEmitIndexFunc(sb *strings.Builder, info csBalancerInfo, cat string, hasSerial bool) {
	wl := func(s string) { sb.WriteString(s); sb.WriteByte('\n') }

	wl("func (s *server) index(w http.ResponseWriter, r *http.Request) {")
	wl("\tq := r.URL.Query()")
	wl("\trjson := q.Get(\"rjson\") == \"true\" || q.Get(\"rjson\") == \"1\"")
	wl("\ttitle := strings.TrimSpace(q.Get(\"title\"))")
	wl("\toriginalTitle := strings.TrimSpace(q.Get(\"original_title\"))")
	wl("\tkpID := strings.TrimSpace(q.Get(\"kinopoisk_id\"))")
	wl("\thref := strings.TrimSpace(q.Get(\"href\"))")
	if hasSerial {
		wl("\tseasonStr := strings.TrimSpace(q.Get(\"s\"))")
		wl("\tseason := -1")
		wl("\tif seasonStr != \"\" {")
		wl("\t\tseason, _ = strconv.Atoi(seasonStr)")
		wl("\t}")
		wl("\tserialParam := strings.TrimSpace(q.Get(\"serial\"))")
		wl("\tisSerial := serialParam == \"1\"")
	}
	wl("\t_ = kpID")
	wl("")
	wl("\tif title == \"\" && originalTitle == \"\" && href == \"\" {")
	wl("\t\trespondEmpty(w, rjson)")
	wl("\t\treturn")
	wl("\t}")
	wl("")

	wl("\t// If href is provided — show content directly")
	wl("\tif href != \"\" {")
	switch {
	case info.ContentType == "serial":
		wl("\t\ts.handleSerial(w, r, rjson, href, title, originalTitle, season)")
	case hasSerial:
		wl("\t\tif isSerial {")
		wl("\t\t\ts.handleSerial(w, r, rjson, href, title, originalTitle, season)")
		wl("\t\t} else {")
		wl("\t\t\ts.handleMovie(w, r, rjson, href, title, originalTitle)")
		wl("\t\t}")
	default:
		wl("\t\ts.handleMovie(w, r, rjson, href, title, originalTitle)")
	}
	wl("\t\treturn")
	wl("\t}")
	wl("")

	wl("\t// Search for content")
	wl("\tresults := s.search(title)")
	wl("\tif len(results) == 0 && originalTitle != \"\" {")
	wl("\t\tresults = s.search(originalTitle)")
	wl("\t}")
	wl("\tif len(results) == 0 {")
	wl("\t\trespondEmpty(w, rjson)")
	wl("\t\treturn")
	wl("\t}")
	wl("")
	wl("\t// If single result — show content directly")
	wl("\tif len(results) == 1 {")
	switch {
	case info.ContentType == "serial":
		wl("\t\ts.handleSerial(w, r, rjson, results[0].URL, title, originalTitle, season)")
	case hasSerial:
		wl("\t\tif isSerial {")
		wl("\t\t\ts.handleSerial(w, r, rjson, results[0].URL, title, originalTitle, season)")
		wl("\t\t} else {")
		wl("\t\t\ts.handleMovie(w, r, rjson, results[0].URL, title, originalTitle)")
		wl("\t\t}")
	default:
		wl("\t\ts.handleMovie(w, r, rjson, results[0].URL, title, originalTitle)")
	}
	wl("\t\treturn")
	wl("\t}")
	wl("")
	wl("\t// Multiple results — show similar list")
	wl("\ts.showSimilar(w, r, rjson, results, title, originalTitle)")
	wl("}")
	wl("")
}

// --- Category-specific search emitters ---

func saEmitSearchDLE(sb *strings.Builder, info csBalancerInfo) {
	wl := func(s string) { sb.WriteString(s); sb.WriteByte('\n') }
	wf := func(format string, args ...any) { fmt.Fprintf(sb, format, args...) }

	searchClass := info.SearchPattern
	if searchClass == "" {
		searchClass = "short-story"
	}

	wl("func (s *server) search(query string) []searchResult {")
	wl("\tcacheKey := \"search:\" + query")
	wl("\tif cached, ok := s.cache.get(cacheKey); ok {")
	wl("\t\treturn cached.([]searchResult)")
	wl("\t}")
	wl("")
	wl("\tformData := url.Values{}")
	wl("\tformData.Set(\"do\", \"search\")")
	wl("\tformData.Set(\"subaction\", \"search\")")
	wl("\tformData.Set(\"search_start\", \"0\")")
	wl("\tformData.Set(\"full_search\", \"0\")")
	wl("\tformData.Set(\"result_from\", \"1\")")
	wl("\tformData.Set(\"story\", query)")
	wl("")
	wl("\tbody, err := s.fetchPost(s.host+searchPath, formData.Encode(), s.host+\"/\")")
	wl("\tif err != nil {")
	wf("\t\tlog.Printf(\"%s: search error: %%v\", err)\n", info.Name)
	wl("\t\treturn nil")
	wl("\t}")
	wl("")
	wf("\tblocks := splitOnPattern(body, %q)\n", searchClass)
	wl("\tvar results []searchResult")
	wl("\tfor _, block := range blocks {")
	wl("\t\tsr := searchResult{}")
	wl("")
	wl("\t\t// Extract href")
	wl("\t\tif m := reResultLink.FindStringSubmatch(block); len(m) >= 2 {")
	wl("\t\t\tsr.URL = m[1]")
	wl("\t\t\tif !strings.HasPrefix(sr.URL, \"http\") {")
	wl("\t\t\t\tsr.URL = s.host + sr.URL")
	wl("\t\t\t}")
	wl("\t\t}")
	wl("")
	wl("\t\t// Extract title")
	wl("\t\tif m := reResultTitle.FindStringSubmatch(block); len(m) >= 2 {")
	wl("\t\t\tsr.Title = htmlUnescape(strings.TrimSpace(m[1]))")
	wl("\t\t} else {")
	wl("\t\t\t// Fallback: try to extract from link text")
	wl("\t\t\tre := regexp.MustCompile(`(?is)<a[^>]+href=\"[^\"]+\"[^>]*>([^<]+)</a>`)")
	wl("\t\t\tif m := re.FindStringSubmatch(block); len(m) >= 2 {")
	wl("\t\t\t\tsr.Title = htmlUnescape(strings.TrimSpace(m[1]))")
	wl("\t\t\t}")
	wl("\t\t}")
	wl("")
	wl("\t\t// Extract year")
	wl("\t\tif m := reYear.FindStringSubmatch(block); len(m) >= 2 {")
	wl("\t\t\tsr.Year, _ = strconv.Atoi(m[1])")
	wl("\t\t}")
	wl("")
	wl("\t\t// Extract poster")
	wl("\t\tif m := rePosterImg.FindStringSubmatch(block); len(m) >= 2 {")
	wl("\t\t\tsr.PosterURL = m[1]")
	wl("\t\t\tif !strings.HasPrefix(sr.PosterURL, \"http\") {")
	wl("\t\t\t\tsr.PosterURL = s.host + sr.PosterURL")
	wl("\t\t\t}")
	wl("\t\t}")
	wl("")
	wl("\t\tif sr.URL != \"\" && sr.Title != \"\" {")
	wl("\t\t\tresults = append(results, sr)")
	wl("\t\t}")
	wl("\t}")
	wl("")
	wl("\tif len(results) > 0 {")
	wl("\t\ts.cache.set(cacheKey, results, 20*time.Minute)")
	wl("\t}")
	wl("\treturn results")
	wl("}")
	wl("")
}

func saEmitSearchAPI(sb *strings.Builder, info csBalancerInfo) {
	wl := func(s string) { sb.WriteString(s); sb.WriteByte('\n') }
	wf := func(format string, args ...any) { fmt.Fprintf(sb, format, args...) }

	apiPath := saMainAPIPath(info)
	if apiPath == "" {
		apiPath = "/api/search"
	}

	wl("func (s *server) search(query string) []searchResult {")
	wl("\tcacheKey := \"search:\" + query")
	wl("\tif cached, ok := s.cache.get(cacheKey); ok {")
	wl("\t\treturn cached.([]searchResult)")
	wl("\t}")
	wl("")
	wf("\tapiURL := s.host + %q + \"?\" + url.Values{\n", apiPath)
	if len(info.TokenFields) > 0 {
		wl("\t\t\"token\": {s.token},")
	}
	wl("\t\t\"title\": {query},")
	wl("\t}.Encode()")
	wl("")
	wl("\tbody, err := s.fetch(apiURL, \"\")")
	wl("\tif err != nil {")
	wf("\t\tlog.Printf(\"%s: search error: %%v\", err)\n", info.Name)
	wl("\t\treturn nil")
	wl("\t}")
	wl("")
	wl("\t// MANUAL: Adjust apiSearchResponse struct and field mapping below to match actual API.")
	if len(info.APIURLs) > 0 {
		wl("\t// Known API endpoints:")
		for _, u := range info.APIURLs {
			wf("\t//   %s\n", u)
		}
	}
	wl("\t// Common API response formats:")
	wl("\t//   {\"data\": [{\"id\":1, \"title\":\"...\", \"year\":2020}]}")
	wl("\t//   [{\"id\":1, \"title\":\"...\", \"year\":2020}]  (direct array)")
	wl("\t//   {\"results\": [...]}  or  {\"items\": [...]}")
	wl("\tvar resp apiSearchResponse")
	wl("\tif err := json.Unmarshal([]byte(body), &resp); err != nil {")
	wf("\t\tlog.Printf(\"%s: JSON parse error: %%v\", err)\n", info.Name)
	wl("\t\treturn nil")
	wl("\t}")
	wl("")
	wl("\tvar results []searchResult")
	wl("\tfor _, item := range resp.Data {")
	wl("\t\tresults = append(results, searchResult{")
	wl("\t\t\tTitle: item.Title,")
	wl("\t\t\tURL:   fmt.Sprintf(\"%d\", item.ID),")
	wl("\t\t\tYear:  item.Year,")
	wl("\t\t})")
	wl("\t}")
	wl("")
	wl("\tif len(results) > 0 {")
	wl("\t\ts.cache.set(cacheKey, results, 20*time.Minute)")
	wl("\t}")
	wl("\treturn results")
	wl("}")
	wl("")
}

func saEmitSearchGeneric(sb *strings.Builder, info csBalancerInfo, cat string) {
	wl := func(s string) { sb.WriteString(s); sb.WriteByte('\n') }
	wf := func(format string, args ...any) { fmt.Fprintf(sb, format, args...) }

	wl("func (s *server) search(query string) []searchResult {")
	wl("\tcacheKey := \"search:\" + query")
	wl("\tif cached, ok := s.cache.get(cacheKey); ok {")
	wl("\t\treturn cached.([]searchResult)")
	wl("\t}")
	wl("")
	switch cat {
	case "embed":
		wf("\t// MANUAL: Embed balancers usually search by kinopoisk_id, not title.\n")
		wf("\t// Implement search via list API if available, or return single result.\n")
	case "playerjs":
		wf("\t// MANUAL: Implement search against upstream site.\n")
	}
	wl("\t_ = query")
	wl("")
	wl("\t// Placeholder: return empty results")
	wl("\treturn nil")
	wl("}")
	wl("")
}

func saEmitSimilarHandler(sb *strings.Builder, info csBalancerInfo, cat string) {
	wl := func(s string) { sb.WriteString(s); sb.WriteByte('\n') }
	wf := func(format string, args ...any) { fmt.Fprintf(sb, format, args...) }

	wl("func (s *server) showSimilar(w http.ResponseWriter, r *http.Request, rjson bool, results []searchResult, title, originalTitle string) {")
	wl("\tif rjson {")
	wl("\t\tdata := make([]map[string]any, 0, len(results))")
	wl("\t\tfor _, sr := range results {")
	wl("\t\t\tparams := url.Values{}")
	wl("\t\t\tparams.Set(\"title\", title)")
	wl("\t\t\tparams.Set(\"original_title\", originalTitle)")
	wl("\t\t\tparams.Set(\"href\", sr.URL)")
	wl("\t\t\tdata = append(data, map[string]any{")
	wl("\t\t\t\t\"method\": \"link\",")
	wl("\t\t\t\t\"url\":    s.selfHost() + \"?\" + params.Encode(),")
	wl("\t\t\t\t\"name\":   sr.Title,")
	wl("\t\t\t\t\"img\":    sr.PosterURL,")
	wf("\t\t\t\t\"year\":   sr.Year,\n")
	wl("\t\t\t})")
	wl("\t\t}")
	wl("\t\trespondJSON(w, map[string]any{\"type\": \"similar\", \"data\": data})")
	wl("\t\treturn")
	wl("\t}")
	wl("")
	wl("\t// HTML similar list")
	wl("\tvar sb strings.Builder")
	wl("\tsb.WriteString(`<div class=\"videos__line\">`)")
	wl("\tfor _, sr := range results {")
	wl("\t\tparams := url.Values{}")
	wl("\t\tparams.Set(\"title\", title)")
	wl("\t\tparams.Set(\"original_title\", originalTitle)")
	wl("\t\tparams.Set(\"href\", sr.URL)")
	wl("\t\trow := map[string]any{\"method\": \"link\", \"url\": s.selfHost() + \"?\" + params.Encode(), \"name\": sr.Title}")
	wl("\t\tsb.WriteString(`<div class=\"videos__item videos__movie selector\" data-json='` + escapeAttr(row) + `'>`)")
	wl("\t\tsb.WriteString(`<div class=\"videos__item-imgbox videos__movie-imgbox\"></div>`)")
	wl("\t\tsb.WriteString(`<div class=\"videos__item-title\">` + sr.Title + `</div></div>`)")
	wl("\t}")
	wl("\tsb.WriteString(`</div>`)")
	wl("\trespondHTML(w, sb.String())")
	wl("}")
	wl("")
}

func saEmitMovieHandler(sb *strings.Builder, info csBalancerInfo, cat string) {
	wl := func(s string) { sb.WriteString(s); sb.WriteByte('\n') }
	wf := func(format string, args ...any) { fmt.Fprintf(sb, format, args...) }

	wl("func (s *server) handleMovie(w http.ResponseWriter, r *http.Request, rjson bool, filmURL, title, originalTitle string) {")
	wl("\tcacheKey := \"movie:\" + filmURL")
	wl("\tif cached, ok := s.cache.get(cacheKey); ok {")
	wl("\t\tstreamURL := cached.(string)")
	wl("\t\ts.writeMovieOutput(w, r, rjson, streamURL, title, originalTitle)")
	wl("\t\treturn")
	wl("\t}")
	wl("")

	switch cat {
	case "dle":
		wl("\t// Step 1: Fetch the film page")
		wl("\tbody, err := s.fetch(filmURL, s.host+\"/\")")
		wl("\tif err != nil {")
		wf("\t\tlog.Printf(\"%s: fetch film page error: %%v\", err)\n", info.Name)
		wl("\t\trespondEmpty(w, rjson)")
		wl("\t\treturn")
		wl("\t}")
		wl("")
		wl("\t// Step 2: Find iframe URL")
		wl("\tiframeURL := \"\"")
		wl("\tif m := reIframeSrc.FindStringSubmatch(body); len(m) >= 2 {")
		wl("\t\tiframeURL = m[1]")
		wl("\t\tif strings.HasPrefix(iframeURL, \"//\") {")
		wl("\t\t\tiframeURL = \"https:\" + iframeURL")
		wl("\t\t}")
		wl("\t\tiframeURL = strings.ReplaceAll(iframeURL, \"&amp;\", \"&\")")
		wl("\t}")
		wl("\tif iframeURL == \"\" {")
		for i, p := range info.RegexPatterns {
			if _, err := regexp.Compile(p); err == nil {
				wf("\t\t// MANUAL: Try C# regex#%d: `%s`\n", i+1, p)
			}
		}
		wf("\t\tlog.Printf(\"%s: no iframe found on %%s\", filmURL)\n", info.Name)
		wl("\t\trespondEmpty(w, rjson)")
		wl("\t\treturn")
		wl("\t}")
		wl("")
		wl("\t// Step 3: Fetch iframe content and extract stream URL")
		wl("\tiframeBody, err := s.fetch(iframeURL, filmURL)")
		wl("\tif err != nil {")
		wf("\t\tlog.Printf(\"%s: fetch iframe error: %%v\", err)\n", info.Name)
		wl("\t\trespondEmpty(w, rjson)")
		wl("\t\treturn")
		wl("\t}")
		wl("")
		wl("\tstreamURL := \"\"")
		wl("\t// Try m3u8 first, then mp4")
		wl("\tif m := rePlayerFile.FindStringSubmatch(iframeBody); len(m) >= 2 {")
		wl("\t\tstreamURL = m[1]")
		wl("\t} else if m := rePlayerFileMP4.FindStringSubmatch(iframeBody); len(m) >= 2 {")
		wl("\t\tstreamURL = m[1]")
		wl("\t}")
		for i, p := range info.RegexPatterns {
			if _, err := regexp.Compile(p); err == nil && (strings.Contains(p, "file") || strings.Contains(p, "src") || strings.Contains(p, "m3u8")) {
				wf("\t// MANUAL: Also try C# regex#%d: `%s`\n", i+1, p)
			}
		}

	case "api":
		wl("\t// Fetch content via API")
		wl("\tapiURL := s.host + filmURL")
		if len(info.TokenFields) > 0 {
			wl("\tif s.token != \"\" {")
			wl("\t\tif strings.Contains(apiURL, \"?\") {")
			wl("\t\t\tapiURL += \"&token=\" + s.token")
			wl("\t\t} else {")
			wl("\t\t\tapiURL += \"?token=\" + s.token")
			wl("\t\t}")
			wl("\t}")
		}
		wl("\tbody, err := s.fetch(apiURL, \"\")")
		wl("\tif err != nil {")
		wf("\t\tlog.Printf(\"%s: API error: %%v\", err)\n", info.Name)
		wl("\t\trespondEmpty(w, rjson)")
		wl("\t\treturn")
		wl("\t}")
		wl("")
		wl("\t// MANUAL: Parse API JSON response to extract stream URL and quality map.")
		wl("\t// See C# source at the end of this file for the exact response structure.")
		wl("\t//")
		// Emit specific API URLs and regex patterns as hints
		if len(info.APIURLs) > 0 {
			wl("\t// Known API endpoints from C#:")
			for _, u := range info.APIURLs {
				wf("\t//   %s\n", u)
			}
		}
		if len(info.RegexPatterns) > 0 {
			wl("\t// C# regex patterns that may help extract data:")
			for i, p := range info.RegexPatterns {
				if _, err := regexp.Compile(p); err == nil {
					wf("\t//   regex#%d: `%s`\n", i+1, p)
				} else {
					wf("\t//   regex#%d (INVALID in Go): `%s`\n", i+1, p)
				}
			}
		}
		if len(info.SettingsFields) > 0 {
			wl("\t// C# settings fields (may contain tokens, hosts, etc.):")
			for _, f := range info.SettingsFields {
				wf("\t//   %s\n", f)
			}
		}
		wl("\t//")
		wl("\t// Typical steps:")
		wl("\t// 1. json.Unmarshal([]byte(body), &response)")
		wl("\t// 2. Extract stream URL from response (check .file, .url, .hls, .mp4 fields)")
		wl("\t// 3. If quality map needed: parse [720p]url[1080p]url or JSON {\"720\":\"url\"}")
		wl("\tstreamURL := \"\"")
		wl("\t_ = body")

	case "embed":
		wl("\t// Build embed URL")
		wl("\tembedURL := filmURL")
		wl("\tif !strings.HasPrefix(embedURL, \"http\") {")
		wl("\t\tembedURL = s.host + embedURL")
		wl("\t}")
		wl("\tbody, err := s.fetch(embedURL, \"\")")
		wl("\tif err != nil {")
		wf("\t\tlog.Printf(\"%s: embed error: %%v\", err)\n", info.Name)
		wl("\t\trespondEmpty(w, rjson)")
		wl("\t\treturn")
		wl("\t}")
		wl("")
		wl("\t// Extract HLS/DASH from JavaScript response")
		wl("\t// MANUAL: Embed pages typically contain JS like: file:\"https://...m3u8\"")
		wl("\t// or Playerjs configs, DASH manifests, etc.")
		if len(info.RegexPatterns) > 0 {
			wl("\t// C# regex patterns for stream extraction:")
			for i, p := range info.RegexPatterns {
				wf("\t//   regex#%d: `%s`\n", i+1, p)
			}
		}
		if len(info.APIURLs) > 0 {
			wl("\t// Known URLs from C#:")
			for _, u := range info.APIURLs {
				wf("\t//   %s\n", u)
			}
		}
		wl("\tstreamURL := \"\"")
		wl("\tif m := reHLS.FindStringSubmatch(body); len(m) >= 2 {")
		wl("\t\tstreamURL = m[1]")
		wl("\t} else if m := reDash.FindStringSubmatch(body); len(m) >= 2 {")
		wl("\t\tstreamURL = m[1]")
		wl("\t}")

	case "playerjs":
		wl("\t// Fetch page and extract Playerjs file JSON")
		wl("\tbody, err := s.fetch(filmURL, s.host+\"/\")")
		wl("\tif err != nil {")
		wf("\t\tlog.Printf(\"%s: fetch error: %%v\", err)\n", info.Name)
		wl("\t\trespondEmpty(w, rjson)")
		wl("\t\treturn")
		wl("\t}")
		wl("")
		wl("\tstreamURL := \"\"")
		wl("\tif m := rePlayerjsFile.FindStringSubmatch(body); len(m) >= 2 {")
		wl("\t\t// Try parsing as quality list: [720p]url[1080p]url")
		wl("\t\tqualities := extractQualitiesFromFile(m[1])")
		wl("\t\tif len(qualities) > 0 {")
		wl("\t\t\t// Use best quality")
		wl("\t\t\tfor _, q := range []string{\"1080p\", \"720p\", \"480p\", \"360p\"} {")
		wl("\t\t\t\tif u, ok := qualities[q]; ok {")
		wl("\t\t\t\t\tstreamURL = u")
		wl("\t\t\t\t\tbreak")
		wl("\t\t\t\t}")
		wl("\t\t\t}")
		wl("\t\t} else {")
		wl("\t\t\t// Try as direct URL")
		wl("\t\t\tstreamURL = m[1]")
		wl("\t\t}")
		wl("\t} else if m := rePlayerFile.FindStringSubmatch(body); len(m) >= 2 {")
		wl("\t\tstreamURL = m[1]")
		wl("\t}")
	}

	wl("")
	wl("\tif streamURL == \"\" {")
	wf("\t\tlog.Printf(\"%s: no stream URL found for %%s\", filmURL)\n", info.Name)
	wl("\t\trespondEmpty(w, rjson)")
	wl("\t\treturn")
	wl("\t}")
	wl("")
	wl("\ts.cache.set(cacheKey, streamURL, 30*time.Minute)")
	wl("\ts.writeMovieOutput(w, r, rjson, streamURL, title, originalTitle)")
	wl("}")
	wl("")

	// writeMovieOutput
	wl("func (s *server) writeMovieOutput(w http.ResponseWriter, r *http.Request, rjson bool, streamURL, title, originalTitle string) {")
	wl("\tdisplayTitle := joinName(title, originalTitle)")
	wl("")
	wl("\trow := map[string]any{")
	wl("\t\t\"method\": \"play\",")
	wl("\t\t\"url\":    streamURL,")
	wl("\t\t\"stream\": streamURL,")
	wl("\t\t\"name\":   title,")
	wl("\t\t\"title\":  displayTitle,")
	wl("\t}")
	wl("")
	wl("\tif rjson {")
	wl("\t\trespondJSON(w, map[string]any{\"type\": \"movie\", \"data\": []map[string]any{row}})")
	wl("\t\treturn")
	wl("\t}")
	wl("")
	wl("\tvar sb strings.Builder")
	wl("\tsb.WriteString(`<div class=\"videos__line\">`)")
	wl("\tsb.WriteString(`<div class=\"videos__item videos__movie selector\" data-json='` + escapeAttr(row) + `'>`)")
	wl("\tsb.WriteString(`<div class=\"videos__item-imgbox videos__movie-imgbox\"></div>`)")
	wl("\tsb.WriteString(`<div class=\"videos__item-title\">` + displayTitle + `</div></div>`)")
	wl("\tsb.WriteString(`</div>`)")
	wl("\trespondHTML(w, sb.String())")
	wl("}")
	wl("")
}

func saEmitSerialHandler(sb *strings.Builder, info csBalancerInfo, cat string) {
	wl := func(s string) { sb.WriteString(s); sb.WriteByte('\n') }
	wf := func(format string, args ...any) { fmt.Fprintf(sb, format, args...) }

	wl("func (s *server) handleSerial(w http.ResponseWriter, r *http.Request, rjson bool, filmURL, title, originalTitle string, season int) {")
	wl("\t// Fetch serial structure")
	wl("\tvoices := s.getSerialStructure(filmURL)")
	wl("\tif len(voices) == 0 {")
	wf("\t\tlog.Printf(\"%s: no serial structure for %%s\", filmURL)\n", info.Name)
	wl("\t\trespondEmpty(w, rjson)")
	wl("\t\treturn")
	wl("\t}")
	wl("")
	wl("\t// Collect all season numbers across voices")
	wl("\tseasonSet := map[int]struct{}{}")
	wl("\tfor _, v := range voices {")
	wl("\t\tfor sn := range v.Seasons {")
	wl("\t\t\tseasonSet[sn] = struct{}{}")
	wl("\t\t}")
	wl("\t}")
	wl("\tseasons := make([]int, 0, len(seasonSet))")
	wl("\tfor sn := range seasonSet {")
	wl("\t\tseasons = append(seasons, sn)")
	wl("\t}")
	wl("\tsort.Ints(seasons)")
	wl("")
	wl("\tif season < 0 || season == 0 {")
	wl("\t\t// Show season list")
	wl("\t\ts.writeSeasons(w, r, rjson, seasons, title, originalTitle, filmURL)")
	wl("\t\treturn")
	wl("\t}")
	wl("")
	wl("\t// Show episodes for selected season")
	wl("\ts.writeEpisodes(w, r, rjson, voices, season, title, originalTitle, filmURL)")
	wl("}")
	wl("")

	// getSerialStructure
	wl("func (s *server) getSerialStructure(filmURL string) []*voiceInfo {")
	wl("\tcacheKey := \"serial:\" + filmURL")
	wl("\tif cached, ok := s.cache.get(cacheKey); ok {")
	wl("\t\treturn cached.([]*voiceInfo)")
	wl("\t}")
	wl("")

	switch cat {
	case "dle":
		wl("\t// Step 1: Fetch film page")
		wl("\tbody, err := s.fetch(filmURL, s.host+\"/\")")
		wl("\tif err != nil {")
		wl("\t\treturn nil")
		wl("\t}")
		wl("")
		wl("\t// Step 2: Find iframe URL")
		wl("\tiframeURL := \"\"")
		wl("\tif m := reIframeSrc.FindStringSubmatch(body); len(m) >= 2 {")
		wl("\t\tiframeURL = m[1]")
		wl("\t\tif strings.HasPrefix(iframeURL, \"//\") { iframeURL = \"https:\" + iframeURL }")
		wl("\t\tiframeURL = strings.ReplaceAll(iframeURL, \"&amp;\", \"&\")")
		wl("\t}")
		wl("\tif iframeURL == \"\" {")
		if len(info.RegexPatterns) > 0 {
			wl("\t\t// MANUAL: Try alternative iframe extraction using C# regex patterns:")
			for i, p := range info.RegexPatterns {
				if strings.Contains(p, "iframe") || strings.Contains(p, "src") || strings.Contains(p, "player") || strings.Contains(p, "embed") {
					wf("\t\t//   regex#%d: `%s`\n", i+1, p)
				}
			}
		} else {
			wl("\t\t// MANUAL: Try alternative iframe extraction for this site")
		}
		wl("\t\treturn nil")
		wl("\t}")
		wl("")
		wl("\t// Step 3: Fetch player and parse structure")
		wl("\tiframeBody, err := s.fetch(iframeURL, filmURL)")
		wl("\tif err != nil {")
		wl("\t\treturn nil")
		wl("\t}")
		wl("")
		wl("\t// Try Playerjs JSON structure")
		wl("\treFileJSON := regexp.MustCompile(`(?i)file\\s*[:=]\\s*'(\\[.+?\\])'`)")
		wl("\tif m := reFileJSON.FindStringSubmatch(iframeBody); len(m) >= 2 {")
		wl("\t\tvoices := parsePlayerjsSerial(m[1])")
		wl("\t\tif len(voices) > 0 {")
		wl("\t\t\ts.cache.set(cacheKey, voices, 40*time.Minute)")
		wl("\t\t\treturn voices")
		wl("\t\t}")
		wl("\t}")
	case "embed":
		wl("\t// Fetch embed URL")
		wl("\tembedURL := filmURL")
		wl("\tif !strings.HasPrefix(embedURL, \"http\") {")
		wl("\t\tembedURL = s.host + embedURL")
		wl("\t}")
		wl("\tbody, err := s.fetch(embedURL, \"\")")
		wl("\tif err != nil {")
		wl("\t\treturn nil")
		wl("\t}")
		wl("")
		wl("\t// Parse seasons JSON from embed response")
		wl("\tif m := reSeasons.FindStringSubmatch(body); len(m) >= 2 {")
		wl("\t\tvar embedSeasons []embedSeason")
		wl("\t\tif json.Unmarshal([]byte(m[1]), &embedSeasons) == nil {")
		wl("\t\t\tvoices := convertEmbedToVoices(embedSeasons)")
		wl("\t\t\tif len(voices) > 0 {")
		wl("\t\t\t\ts.cache.set(cacheKey, voices, 40*time.Minute)")
		wl("\t\t\t\treturn voices")
		wl("\t\t\t}")
		wl("\t\t}")
		wl("\t}")
	case "playerjs":
		wl("\t// Fetch page and extract Playerjs file JSON")
		wl("\tbody, err := s.fetch(filmURL, s.host+\"/\")")
		wl("\tif err != nil {")
		wl("\t\treturn nil")
		wl("\t}")
		wl("")
		wl("\treFileJSON := regexp.MustCompile(`(?i)file\\s*[:=]\\s*'(\\[.+?\\])'`)")
		wl("\tif m := reFileJSON.FindStringSubmatch(body); len(m) >= 2 {")
		wl("\t\tvoices := parsePlayerjsSerial(m[1])")
		wl("\t\tif len(voices) > 0 {")
		wl("\t\t\ts.cache.set(cacheKey, voices, 40*time.Minute)")
		wl("\t\t\treturn voices")
		wl("\t\t}")
		wl("\t}")
	case "api":
		wl("\t// MANUAL: Implement serial structure fetching via API.")
		wl("\t// Typical API serial flow:")
		wl("\t// 1. GET {host}/api/player/{id}/translations → list of voices/dubs")
		wl("\t// 2. For each voice: GET {host}/api/player/{id}/episodes?translationId={tid} → episodes")
		wl("\t// 3. Build []*voiceInfo with Seasons map[int][]episodeInfo")
		if len(info.APIURLs) > 0 {
			wl("\t// Known API URLs from C#:")
			for _, u := range info.APIURLs {
				wf("\t//   %s\n", u)
			}
		}
		if len(info.RegexPatterns) > 0 {
			wl("\t// C# regex patterns for parsing:")
			for i, p := range info.RegexPatterns {
				wf("\t//   regex#%d: `%s`\n", i+1, p)
			}
		}
		wl("\t_ = filmURL")
	default:
		wl("\t// MANUAL: Implement serial structure fetching for this balancer type")
		wl("\t_ = filmURL")
	}

	wl("")
	wl("\treturn nil")
	wl("}")
	wl("")

	// writeSeasons
	wl("func (s *server) writeSeasons(w http.ResponseWriter, r *http.Request, rjson bool, seasons []int, title, originalTitle, filmURL string) {")
	wl("\tif rjson {")
	wl("\t\tdata := make([]map[string]any, 0, len(seasons))")
	wl("\t\tfor _, sn := range seasons {")
	wl("\t\t\tparams := url.Values{}")
	wl("\t\t\tparams.Set(\"title\", title)")
	wl("\t\t\tparams.Set(\"original_title\", originalTitle)")
	wl("\t\t\tparams.Set(\"href\", filmURL)")
	wl("\t\t\tparams.Set(\"serial\", \"1\")")
	wl("\t\t\tparams.Set(\"s\", strconv.Itoa(sn))")
	wl("\t\t\tdata = append(data, map[string]any{")
	wl("\t\t\t\t\"method\": \"link\",")
	wl("\t\t\t\t\"id\":     sn,")
	wl("\t\t\t\t\"url\":    s.selfHost() + \"?\" + params.Encode(),")
	wl("\t\t\t\t\"name\":   fmt.Sprintf(\"%d сезон\", sn),")
	wl("\t\t\t})")
	wl("\t\t}")
	wl("\t\trespondJSON(w, map[string]any{\"type\": \"season\", \"data\": data})")
	wl("\t\treturn")
	wl("\t}")
	wl("")
	wl("\tvar sb strings.Builder")
	wl("\tsb.WriteString(`<div class=\"videos__line\">`)")
	wl("\tfor _, sn := range seasons {")
	wl("\t\tparams := url.Values{}")
	wl("\t\tparams.Set(\"title\", title)")
	wl("\t\tparams.Set(\"original_title\", originalTitle)")
	wl("\t\tparams.Set(\"href\", filmURL)")
	wl("\t\tparams.Set(\"serial\", \"1\")")
	wl("\t\tparams.Set(\"s\", strconv.Itoa(sn))")
	wl("\t\trow := map[string]any{\"method\": \"link\", \"id\": sn, \"url\": s.selfHost() + \"?\" + params.Encode(), \"name\": fmt.Sprintf(\"%d сезон\", sn)}")
	wl("\t\tsb.WriteString(`<div class=\"videos__season selector\" data-json='` + escapeAttr(row) + `'>`)")
	wl("\t\tsb.WriteString(fmt.Sprintf(`<div class=\"videos__season-title\">%d сезон</div></div>`, sn))")
	wl("\t}")
	wl("\tsb.WriteString(`</div>`)")
	wl("\trespondHTML(w, sb.String())")
	wl("}")
	wl("")

	// writeEpisodes
	wl("func (s *server) writeEpisodes(w http.ResponseWriter, r *http.Request, rjson bool, voices []*voiceInfo, season int, title, originalTitle, filmURL string) {")
	wl("\t// Select voice (from query param or first)")
	wl("\tq := r.URL.Query()")
	wl("\tvoiceName := strings.TrimSpace(q.Get(\"voice\"))")
	wl("")
	wl("\t// Build voice list and find active voice")
	wl("\tvar activeVoice *voiceInfo")
	wl("\tfor _, v := range voices {")
	wl("\t\tif _, ok := v.Seasons[season]; !ok {")
	wl("\t\t\tcontinue")
	wl("\t\t}")
	wl("\t\tif activeVoice == nil {")
	wl("\t\t\tactiveVoice = v")
	wl("\t\t}")
	wl("\t\tif v.Name == voiceName {")
	wl("\t\t\tactiveVoice = v")
	wl("\t\t}")
	wl("\t}")
	wl("\tif activeVoice == nil {")
	wl("\t\trespondEmpty(w, rjson)")
	wl("\t\treturn")
	wl("\t}")
	wl("")
	wl("\tepisodes := activeVoice.Seasons[season]")
	wl("")

	// JSON output
	wl("\tif rjson {")
	wl("\t\tdata := make([]map[string]any, 0, len(episodes))")
	wl("\t\tfor _, ep := range episodes {")
	wl("\t\t\tif ep.File == \"\" { continue }")
	wl("\t\t\tepName := ep.Title")
	wl("\t\t\tif epName == \"\" { epName = fmt.Sprintf(\"%d серия\", ep.Number) }")
	wl("\t\t\tdata = append(data, map[string]any{")
	wl("\t\t\t\t\"method\": \"play\",")
	wl("\t\t\t\t\"url\":    ep.File,")
	wl("\t\t\t\t\"stream\": ep.File,")
	wl("\t\t\t\t\"s\":      season,")
	wl("\t\t\t\t\"e\":      ep.Number,")
	wl("\t\t\t\t\"name\":   epName,")
	wl("\t\t\t\t\"title\":  joinName(title, originalTitle) + fmt.Sprintf(\" (%s)\", epName),")
	wl("\t\t\t})")
	wl("\t\t}")
	wl("")
	wl("\t\t// Voice selector")
	wl("\t\tvar voiceData []map[string]any")
	wl("\t\tfor _, v := range voices {")
	wl("\t\t\tif _, ok := v.Seasons[season]; !ok { continue }")
	wl("\t\t\tparams := url.Values{}")
	wl("\t\t\tparams.Set(\"title\", title)")
	wl("\t\t\tparams.Set(\"original_title\", originalTitle)")
	wl("\t\t\tparams.Set(\"href\", filmURL)")
	wl("\t\t\tparams.Set(\"serial\", \"1\")")
	wl("\t\t\tparams.Set(\"s\", strconv.Itoa(season))")
	wl("\t\t\tparams.Set(\"voice\", v.Name)")
	wl("\t\t\tvoiceData = append(voiceData, map[string]any{")
	wl("\t\t\t\t\"method\": \"link\",")
	wl("\t\t\t\t\"name\":   v.Name,")
	wl("\t\t\t\t\"url\":    s.selfHost() + \"?\" + params.Encode(),")
	wl("\t\t\t\t\"active\": v.Name == activeVoice.Name,")
	wl("\t\t\t})")
	wl("\t\t}")
	wl("")
	wl("\t\tresp := map[string]any{\"type\": \"episode\", \"data\": data}")
	wl("\t\tif len(voiceData) > 1 {")
	wl("\t\t\tresp[\"voice\"] = voiceData")
	wl("\t\t}")
	wl("\t\trespondJSON(w, resp)")
	wl("\t\treturn")
	wl("\t}")
	wl("")

	// HTML output
	wl("\tvar sb strings.Builder")
	wl("\tsb.WriteString(`<div class=\"videos__line\">`)")
	wl("")
	wl("\t// Voice buttons")
	wl("\tvoiceCount := 0")
	wl("\tfor _, v := range voices {")
	wl("\t\tif _, ok := v.Seasons[season]; !ok { continue }")
	wl("\t\tvoiceCount++")
	wl("\t}")
	wl("\tif voiceCount > 1 {")
	wl("\t\tfor _, v := range voices {")
	wl("\t\t\tif _, ok := v.Seasons[season]; !ok { continue }")
	wl("\t\t\tparams := url.Values{}")
	wl("\t\t\tparams.Set(\"title\", title)")
	wl("\t\t\tparams.Set(\"original_title\", originalTitle)")
	wl("\t\t\tparams.Set(\"href\", filmURL)")
	wl("\t\t\tparams.Set(\"serial\", \"1\")")
	wl("\t\t\tparams.Set(\"s\", strconv.Itoa(season))")
	wl("\t\t\tparams.Set(\"voice\", v.Name)")
	wl("\t\t\trow := map[string]any{\"method\": \"link\", \"name\": v.Name, \"url\": s.selfHost() + \"?\" + params.Encode()}")
	wl("\t\t\tactive := \"\"")
	wl("\t\t\tif v.Name == activeVoice.Name { active = \" active\" }")
	wl("\t\t\tsb.WriteString(`<div class=\"videos__button selector` + active + `\" data-json='` + escapeAttr(row) + `'>` + v.Name + `</div>`)")
	wl("\t\t}")
	wl("\t}")
	wl("")
	wl("\t// Episodes")
	wl("\tfor _, ep := range episodes {")
	wl("\t\tif ep.File == \"\" { continue }")
	wl("\t\tepName := ep.Title")
	wl("\t\tif epName == \"\" { epName = fmt.Sprintf(\"%d серия\", ep.Number) }")
	wl("\t\trow := map[string]any{\"method\": \"play\", \"url\": ep.File, \"stream\": ep.File, \"s\": season, \"e\": ep.Number, \"name\": epName, \"title\": joinName(title, originalTitle) + fmt.Sprintf(\" (%s)\", epName)}")
	wl("\t\tsb.WriteString(fmt.Sprintf(`<div class=\"videos__item videos__movie selector\" s=\"%d\" e=\"%d\" data-json='`, season, ep.Number) + escapeAttr(row) + `'>`)")
	wl("\t\tsb.WriteString(`<div class=\"videos__item-imgbox videos__movie-imgbox\"></div>`)")
	wl("\t\tsb.WriteString(`<div class=\"videos__item-title\">` + epName + `</div></div>`)")
	wl("\t}")
	wl("\tsb.WriteString(`</div>`)")
	wl("\trespondHTML(w, sb.String())")
	wl("}")
	wl("")

	// Playerjs serial parser (used by DLE and playerjs categories)
	wl("// parsePlayerjsSerial parses nested folder JSON: [{title,folder:[{title,folder:[{file,title}]}]}]")
	wl("func parsePlayerjsSerial(raw string) []*voiceInfo {")
	wl("\traw = strings.ReplaceAll(raw, \"\\\\'\", \"'\")")
	wl("\traw = strings.ReplaceAll(raw, `\\\"`, `\"`)")
	wl("")
	wl("\tvar items []playerjsItem")
	wl("\tif err := json.Unmarshal([]byte(raw), &items); err != nil {")
	wl("\t\tlog.Printf(\"%s: playerjs parse error: %v\", balancerName, err)")
	wl("\t\treturn nil")
	wl("\t}")
	wl("")
	wl("\tvar voices []*voiceInfo")
	wl("\tfor _, voice := range items {")
	wl("\t\tif len(voice.Folder) == 0 { continue }")
	wl("\t\tv := &voiceInfo{")
	wl("\t\t\tName:    voice.Title,")
	wl("\t\t\tSeasons: make(map[int][]episodeInfo),")
	wl("\t\t}")
	wl("\t\tfor si, season := range voice.Folder {")
	wl("\t\t\tsn := si + 1")
	wl("\t\t\tif season.Title != \"\" {")
	wl("\t\t\t\tif n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(season.Title, \"Сезон\"), \"Season\"))); err == nil && n > 0 {")
	wl("\t\t\t\t\tsn = n")
	wl("\t\t\t\t}")
	wl("\t\t\t}")
	wl("\t\t\tif len(season.Folder) > 0 {")
	wl("\t\t\t\tfor ei, ep := range season.Folder {")
	wl("\t\t\t\t\tv.Seasons[sn] = append(v.Seasons[sn], episodeInfo{")
	wl("\t\t\t\t\t\tNumber:   ei + 1,")
	wl("\t\t\t\t\t\tTitle:    ep.Title,")
	wl("\t\t\t\t\t\tFile:     ep.File,")
	wl("\t\t\t\t\t\tSubtitle: ep.Subtitle,")
	wl("\t\t\t\t\t})")
	wl("\t\t\t\t}")
	wl("\t\t\t} else if season.File != \"\" {")
	wl("\t\t\t\t// Flat structure: voice → episodes (no season folder)")
	wl("\t\t\t\tv.Seasons[1] = append(v.Seasons[1], episodeInfo{")
	wl("\t\t\t\t\tNumber:   si + 1,")
	wl("\t\t\t\t\tTitle:    season.Title,")
	wl("\t\t\t\t\tFile:     season.File,")
	wl("\t\t\t\t\tSubtitle: season.Subtitle,")
	wl("\t\t\t\t})")
	wl("\t\t\t}")
	wl("\t\t}")
	wl("\t\tif v.Name == \"\" { v.Name = \"По умолчанию\" }")
	wl("\t\tif len(v.Seasons) > 0 {")
	wl("\t\t\tvoices = append(voices, v)")
	wl("\t\t}")
	wl("\t}")
	wl("")
	wl("\t// Handle case where file JSON is just seasons without voice wrapper")
	wl("\tif len(voices) == 0 && len(items) > 0 && items[0].Folder != nil && items[0].File == \"\" {")
	wl("\t\tv := &voiceInfo{Name: \"По умолчанию\", Seasons: make(map[int][]episodeInfo)}")
	wl("\t\tfor si, season := range items {")
	wl("\t\t\tsn := si + 1")
	wl("\t\t\tfor ei, ep := range season.Folder {")
	wl("\t\t\t\tv.Seasons[sn] = append(v.Seasons[sn], episodeInfo{")
	wl("\t\t\t\t\tNumber:   ei + 1,")
	wl("\t\t\t\t\tTitle:    ep.Title,")
	wl("\t\t\t\t\tFile:     ep.File,")
	wl("\t\t\t\t\tSubtitle: ep.Subtitle,")
	wl("\t\t\t\t})")
	wl("\t\t\t}")
	wl("\t\t}")
	wl("\t\tif len(v.Seasons) > 0 {")
	wl("\t\t\tvoices = append(voices, v)")
	wl("\t\t}")
	wl("\t}")
	wl("")
	wl("\treturn voices")
	wl("}")
	wl("")

	// Embed to voices converter
	if cat == "embed" {
		wf("func convertEmbedToVoices(embedSeasons []embedSeason) []*voiceInfo {\n")
		wl("\tv := &voiceInfo{Name: \"По умолчанию\", Seasons: make(map[int][]episodeInfo)}")
		wl("\tfor _, s := range embedSeasons {")
		wl("\t\tfor _, ep := range s.Episodes {")
		wl("\t\t\tepNum, _ := strconv.Atoi(ep.Episode)")
		wl("\t\t\tstreamURL := ep.HLS")
		wl("\t\t\tif streamURL == \"\" { streamURL = ep.Dash }")
		wl("\t\t\tv.Seasons[s.Season] = append(v.Seasons[s.Season], episodeInfo{")
		wl("\t\t\t\tNumber: epNum,")
		wl("\t\t\t\tTitle:  fmt.Sprintf(\"%d серия\", epNum),")
		wl("\t\t\t\tFile:   streamURL,")
		wl("\t\t\t})")
		wl("\t\t}")
		wl("\t}")
		wl("\tif len(v.Seasons) == 0 { return nil }")
		wl("\treturn []*voiceInfo{v}")
		wl("}")
		wl("")
	}
}

func saEmitFetchHelpers(sb *strings.Builder, info csBalancerInfo, cat string) {
	wl := func(s string) { sb.WriteString(s); sb.WriteByte('\n') }
	wf := func(format string, args ...any) { fmt.Fprintf(sb, format, args...) }

	wl("// --- HTTP helpers ---")
	wl("")
	wl("func (s *server) fetch(targetURL, referer string) (string, error) {")
	wl("\treq, err := http.NewRequest(\"GET\", targetURL, nil)")
	wl("\tif err != nil {")
	wl("\t\treturn \"\", err")
	wl("\t}")
	wl("\treq.Header.Set(\"User-Agent\", \"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36\")")
	wl("\tif referer != \"\" {")
	wl("\t\treq.Header.Set(\"Referer\", referer)")
	wl("\t}")
	for k, v := range info.Headers {
		wf("\treq.Header.Set(%q, %q)\n", k, v)
	}
	wl("")
	wl("\tresp, err := s.client.Do(req)")
	wl("\tif err != nil {")
	wl("\t\treturn \"\", err")
	wl("\t}")
	wl("\tdefer resp.Body.Close()")
	wl("\tif resp.StatusCode < 200 || resp.StatusCode >= 400 {")
	wl("\t\treturn \"\", fmt.Errorf(\"HTTP %d\", resp.StatusCode)")
	wl("\t}")
	wl("\tdata, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))")
	wl("\tif err != nil {")
	wl("\t\treturn \"\", err")
	wl("\t}")
	wl("\treturn string(data), nil")
	wl("}")
	wl("")

	// POST helper (for DLE and other form-based sites)
	if cat == "dle" || info.HasCookies {
		wl("func (s *server) fetchPost(targetURL, formData, referer string) (string, error) {")
		wl("\treq, err := http.NewRequest(\"POST\", targetURL, strings.NewReader(formData))")
		wl("\tif err != nil {")
		wl("\t\treturn \"\", err")
		wl("\t}")
		wl("\treq.Header.Set(\"Content-Type\", \"application/x-www-form-urlencoded\")")
		wl("\treq.Header.Set(\"User-Agent\", \"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36\")")
		wl("\tif referer != \"\" {")
		wl("\t\treq.Header.Set(\"Referer\", referer)")
		wl("\t}")
		for k, v := range info.Headers {
			wf("\treq.Header.Set(%q, %q)\n", k, v)
		}
		wl("")
		wl("\tresp, err := s.client.Do(req)")
		wl("\tif err != nil {")
		wl("\t\treturn \"\", err")
		wl("\t}")
		wl("\tdefer resp.Body.Close()")
		wl("\tdata, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))")
		wl("\tif err != nil {")
		wl("\t\treturn \"\", err")
		wl("\t}")
		wl("\treturn string(data), nil")
		wl("}")
		wl("")
	}
}

func saEmitHTMLHelpers(sb *strings.Builder) {
	wl := func(s string) { sb.WriteString(s); sb.WriteByte('\n') }

	wl("// --- HTML parsing helpers (DLE sites) ---")
	wl("")
	wl("// splitOnPattern splits HTML into blocks at each occurrence of a CSS class")
	wl("func splitOnPattern(html, pattern string) []string {")
	wl("\tre := regexp.MustCompile(`(?is)<[^>]*class=\"[^\"]*` + regexp.QuoteMeta(pattern) + `[^\"]*\"[^>]*>`)")
	wl("\tlocs := re.FindAllStringIndex(html, -1)")
	wl("\tif len(locs) == 0 {")
	wl("\t\treturn nil")
	wl("\t}")
	wl("\tvar blocks []string")
	wl("\tfor i, loc := range locs {")
	wl("\t\tend := len(html)")
	wl("\t\tif i+1 < len(locs) {")
	wl("\t\t\tend = locs[i+1][0]")
	wl("\t\t}")
	wl("\t\tblocks = append(blocks, html[loc[0]:end])")
	wl("\t}")
	wl("\treturn blocks")
	wl("}")
	wl("")
	wl("func htmlUnescape(s string) string {")
	wl("\ts = strings.ReplaceAll(s, \"&amp;\", \"&\")")
	wl("\ts = strings.ReplaceAll(s, \"&lt;\", \"<\")")
	wl("\ts = strings.ReplaceAll(s, \"&gt;\", \">\")")
	wl("\ts = strings.ReplaceAll(s, \"&quot;\", `\"`)")
	wl("\ts = strings.ReplaceAll(s, \"&#39;\", \"'\")")
	wl("\treturn s")
	wl("}")
	wl("")
}

func saEmitPlayerjsParser(sb *strings.Builder) {
	wl := func(s string) { sb.WriteString(s); sb.WriteByte('\n') }

	wl("// --- Playerjs helpers ---")
	wl("")
	wl("// extractQualitiesFromFile parses [720p]url[1080p]url format")
	wl("func extractQualitiesFromFile(file string) map[string]string {")
	wl("\tqualities := make(map[string]string)")
	wl("\tfor _, m := range reQualityFromURL.FindAllStringSubmatch(file, -1) {")
	wl("\t\tq := m[1]")
	wl("\t\tif !strings.HasSuffix(q, \"p\") { q += \"p\" }")
	wl("\t\tqualities[q] = m[2]")
	wl("\t}")
	wl("\treturn qualities")
	wl("}")
	wl("")
}

func saEmitEmbedParser(sb *strings.Builder, info csBalancerInfo) {
	wl := func(s string) { sb.WriteString(s); sb.WriteByte('\n') }

	wl("// --- Embed parsing helpers ---")
	wl("")
	wl("// parseHLSQualities extracts quality variants from HLS master playlist")
	wl("func parseHLSQualities(body, baseURL string) map[string]string {")
	wl("\tqualities := make(map[string]string)")
	wl("\tre := regexp.MustCompile(`#EXT-X-STREAM-INF:[^\\n]*RESOLUTION=\\d+x(\\d+)[^\\n]*\\n([^\\n]+)`)")
	wl("\tfor _, m := range re.FindAllStringSubmatch(body, -1) {")
	wl("\t\theight := m[1]")
	wl("\t\tstreamURL := strings.TrimSpace(m[2])")
	wl("\t\tif !strings.HasPrefix(streamURL, \"http\") && baseURL != \"\" {")
	wl("\t\t\tidx := strings.LastIndex(baseURL, \"/\")")
	wl("\t\t\tif idx > 0 {")
	wl("\t\t\t\tstreamURL = baseURL[:idx+1] + streamURL")
	wl("\t\t\t}")
	wl("\t\t}")
	wl("\t\tqualities[height+\"p\"] = streamURL")
	wl("\t}")
	wl("\treturn qualities")
	wl("}")
	wl("")
}

func saEmitOutputHelpers(sb *strings.Builder) {
	wl := func(s string) { sb.WriteString(s); sb.WriteByte('\n') }

	wl("// --- Output helpers ---")
	wl("")
	wl("func respondJSON(w http.ResponseWriter, v any) {")
	wl("\tw.Header().Set(\"Content-Type\", \"application/json; charset=utf-8\")")
	wl("\tjson.NewEncoder(w).Encode(v)")
	wl("}")
	wl("")
	wl("func respondHTML(w http.ResponseWriter, html string) {")
	wl("\tw.Header().Set(\"Content-Type\", \"text/html; charset=utf-8\")")
	wl("\tw.Write([]byte(html))")
	wl("}")
	wl("")
	wl("func respondEmpty(w http.ResponseWriter, rjson bool) {")
	wl("\tif rjson {")
	wl("\t\trespondJSON(w, map[string]any{})")
	wl("\t\treturn")
	wl("\t}")
	wl("\tw.Header().Set(\"Content-Type\", \"text/html; charset=utf-8\")")
	wl("\tw.Write([]byte(\"\"))")
	wl("}")
	wl("")
	wl("func joinName(title, orig string) string {")
	wl("\tif orig != \"\" && orig != title {")
	wl("\t\treturn title + \" / \" + orig")
	wl("\t}")
	wl("\treturn title")
	wl("}")
	wl("")
	wl("func escapeAttr(v any) string {")
	wl("\tb, _ := json.Marshal(v)")
	wl("\ts := string(b)")
	wl("\ts = strings.ReplaceAll(s, \"'\", \"&#39;\")")
	wl("\treturn s")
	wl("}")
	wl("")
	wl("// Suppress unused imports")
	wl("var (")
	wl("\t_ = url.Values{}")
	wl("\t_ = strconv.Itoa")
	wl("\t_ = sort.Ints")
	wl("\t_ = sync.RWMutex{}")
	wl("\t_ = fmt.Sprintf")
	wl(")")
	wl("")
}

func saEmitCSReference(sb *strings.Builder, info csBalancerInfo) {
	wl := func(s string) { sb.WriteString(s); sb.WriteByte('\n') }
	wf := func(format string, args ...any) { fmt.Fprintf(sb, format, args...) }

	wl("/*")
	wl("=== ORIGINAL C# SOURCE (for manual porting reference) ===")
	wf("Category: %s\n", info.Category)
	if len(info.SourceFiles) > 0 {
		wf("Source files: %s\n", strings.Join(info.SourceFiles, ", "))
	}
	wl("")

	// Regex cross-reference
	if len(info.RegexPatterns) > 0 {
		wl("C# REGEX CROSS-REFERENCE:")
		for i, p := range info.RegexPatterns {
			valid := "valid"
			if _, err := regexp.Compile(p); err != nil {
				valid = "INVALID in Go: " + err.Error()
			}
			wf("  regex#%d (%s): %s\n", i+1, valid, p)
		}
		wl("")
	}

	// API URLs
	if len(info.APIURLs) > 0 {
		wl("API URLs:")
		for _, u := range info.APIURLs {
			wf("  %s\n", u)
		}
		wl("")
	}

	// Token fields
	if len(info.TokenFields) > 0 {
		wf("Token fields: %s\n", strings.Join(info.TokenFields, ", "))
		wl("")
	}

	// Cache keys
	if len(info.CacheKeys) > 0 {
		wl("Cache keys:")
		for i, k := range info.CacheKeys {
			ttl := ""
			if i < len(info.CacheTTL) {
				ttl = " (TTL: " + info.CacheTTL[i] + ")"
			}
			wf("  %s%s\n", k, ttl)
		}
		wl("")
	}

	// Raw C# source (truncated if very long, BOM stripped)
	raw := info.RawCSCode
	raw = strings.ReplaceAll(raw, "\xef\xbb\xbf", "") // strip BOM
	raw = strings.ReplaceAll(raw, "\x00", "")         // strip NUL bytes
	raw = strings.ReplaceAll(raw, "*/", "* /")        // escape block comment end
	if len(raw) > 50000 {
		raw = raw[:50000] + "\n... (truncated, " + fmt.Sprintf("%d", len(info.RawCSCode)) + " bytes total)"
	}
	wl(raw)
	wl("")
	wl("=== END C# SOURCE ===")
	wl("*/")
}

// --- HTTP handlers ---

func tgAdminConstructorHandler(tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := tgAdminAuthCheck(w, r, tgStore, adminStore); !ok {
			return
		}

		switch r.Method {
		case http.MethodPost:
			handleConstructorParse(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func handleConstructorParse(w http.ResponseWriter, r *http.Request) {
	ct := r.Header.Get("Content-Type")

	if strings.HasPrefix(ct, "multipart/") {
		if err := r.ParseMultipartForm(10 << 20); err != nil { // 10MB limit
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "failed to parse form: " + err.Error()})
			return
		}

		// Try single file first (backward compat).
		file, fh, err := r.FormFile("file")
		if err == nil {
			defer file.Close()
			data, _ := io.ReadAll(io.LimitReader(file, 10<<20))

			// Check if it's a zip file.
			if strings.HasSuffix(strings.ToLower(fh.Filename), ".zip") || isZip(data) {
				files, zerr := extractCSFromZip(data)
				if zerr != nil {
					writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid zip: " + zerr.Error()})
					return
				}
				if len(files) == 0 {
					writeJSON(w, http.StatusBadRequest, map[string]any{"error": "no .cs files in zip"})
					return
				}
				merged, names := mergeCSFiles(files)
				info := parseCSBalancer(merged)
				info.SourceFiles = names
				enrichWithStandalone(&info)
				writeJSON(w, http.StatusOK, info)
				return
			}

			// Single .cs file.
			code := string(data)
			if strings.TrimSpace(code) == "" {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "empty code"})
				return
			}
			info := parseCSBalancer(code)
			info.SourceFiles = []string{fh.Filename}
			enrichWithStandalone(&info)
			writeJSON(w, http.StatusOK, info)
			return
		}

		// Multiple files via files[] field.
		multiFiles := r.MultipartForm.File["files[]"]
		if len(multiFiles) == 0 {
			multiFiles = r.MultipartForm.File["files"]
		}
		if len(multiFiles) > 0 {
			csFiles := make(map[string]string, len(multiFiles))
			for _, fh := range multiFiles {
				f, err := fh.Open()
				if err != nil {
					continue
				}
				data, _ := io.ReadAll(io.LimitReader(f, 2<<20))
				f.Close()
				csFiles[fh.Filename] = string(data)
			}
			if len(csFiles) == 0 {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "no valid files"})
				return
			}
			merged, names := mergeCSFiles(csFiles)
			info := parseCSBalancer(merged)
			info.SourceFiles = names
			enrichWithStandalone(&info)
			writeJSON(w, http.StatusOK, info)
			return
		}

		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "no file uploaded"})
		return
	}

	// JSON body (backward compat).
	var body struct {
		Code string `json:"code"`
	}
	if err := stdjson.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
		return
	}
	if strings.TrimSpace(body.Code) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "empty code"})
		return
	}

	info := parseCSBalancer(body.Code)
	enrichWithStandalone(&info)
	writeJSON(w, http.StatusOK, info)
}

// enrichWithStandalone adds the standalone Go code to a parsed balancer info.
func enrichWithStandalone(info *csBalancerInfo) {
	if info.Name != "" {
		info.StandaloneGo = generateStandaloneBalancer(*info, 0)
	}
}

// isZip checks if data starts with PK zip magic bytes.
func isZip(data []byte) bool {
	return len(data) >= 4 && data[0] == 'P' && data[1] == 'K' && data[2] == 3 && data[3] == 4
}

// --- Custom balancer management API ---

func tgAdminCustBalHandler(tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore, pool *custbal.Pool, dynRoutes DynRoutes) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := tgAdminAuthCheck(w, r, tgStore, adminStore); !ok {
			return
		}

		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, pool.List())

		case http.MethodPost:
			body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			var req struct {
				Action string `json:"action"` // start, stop, restart, compile, remove, deploy
				Name   string `json:"name"`
				GoCode string `json:"go_code,omitempty"`
				Config *struct {
					DisplayName  string `json:"display_name"`
					QualityBadge string `json:"quality_badge"`
					ContentType  string `json:"content_type"`
					Host         string `json:"host"`
				} `json:"config,omitempty"`
			}
			if stdjson.Unmarshal(body, &req) != nil || req.Name == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
				return
			}
			req.Name = strings.TrimSpace(req.Name)
			if !isValidCustBalName(req.Name) {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid balancer name"})
				return
			}

			switch req.Action {
			case "start":
				if err := pool.Start(req.Name); err != nil {
					writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
					return
				}
				if rm := pool.RouteMap(); rm[req.Name] != "" {
					dynRoutes.Register(req.Name, rm[req.Name])
				}
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})

			case "stop":
				if err := pool.Stop(req.Name); err != nil {
					writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
					return
				}
				dynRoutes.Unregister(req.Name)
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})

			case "restart":
				if err := pool.Restart(req.Name); err != nil {
					writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
					return
				}
				if rm := pool.RouteMap(); rm[req.Name] != "" {
					dynRoutes.Register(req.Name, rm[req.Name])
				}
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})

			case "compile":
				output, err := pool.Recompile(req.Name)
				if err != nil {
					writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error(), "output": output})
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": output})

			case "remove":
				dynRoutes.Unregister(req.Name)
				if err := pool.Remove(req.Name); err != nil {
					writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})

			case "deploy":
				if req.GoCode == "" {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "go_code required"})
					return
				}
				cfg := custbal.BalancerConfig{
					Name:      req.Name,
					AutoStart: true,
				}
				if req.Config != nil {
					cfg.DisplayName = req.Config.DisplayName
					cfg.QualityBadge = req.Config.QualityBadge
					cfg.ContentType = req.Config.ContentType
					cfg.Host = req.Config.Host
				}
				output, err := pool.Deploy(req.Name, req.GoCode, cfg)
				if err != nil {
					writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error(), "output": output})
					return
				}
				if rm := pool.RouteMap(); rm[req.Name] != "" {
					dynRoutes.Register(req.Name, rm[req.Name])
				}
				if cfg.QualityBadge != "" {
					pluginQualityBadgeSet(req.Name, cfg.QualityBadge)
				}
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": output})

			default:
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown action"})
			}
		}
	}
}

// porterJob tracks an async LLM porting job.
type porterJob struct {
	mu       sync.Mutex
	status   string // "running", "done", "error"
	step     int
	message  string
	goCode   string
	attempts int
	elapsed  string
	output   string
}

// porterJobs stores active/completed jobs keyed by job ID.
var porterJobs = struct {
	sync.Mutex
	m map[string]*porterJob
}{m: make(map[string]*porterJob)}

// tgAdminPorterHandler handles LLM-powered C# → Go porting (async).
// POST /{adminPath}/api/porter — starts background job, returns {"job_id":"..."}
func tgAdminPorterHandler(tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore, pool *custbal.Pool, dynRoutes DynRoutes, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := tgAdminAuthCheck(w, r, tgStore, adminStore); !ok {
			return
		}

		// Use live config so LLM settings from admin panel take effect immediately.
		liveCfg := liveConfig(cfg)

		// Read LLM config (startup or dynamic from init.conf).
		llmEndpoint := liveCfg.LLM.Endpoint
		llmApiKey := liveCfg.LLM.ApiKey
		llmModel := liveCfg.LLM.Model
		llmTemp := liveCfg.LLM.Temp
		llmMaxRetries := liveCfg.LLM.MaxRetries

		if llmEndpoint == "" {
			if data, ok := readFileAny("init.conf"); ok {
				var initCfg map[string]any
				if stdjson.Unmarshal(data, &initCfg) == nil {
					if llmMap, ok := initCfg["LLM"].(map[string]any); ok {
						if ep, ok := llmMap["endpoint"].(string); ok {
							llmEndpoint = strings.TrimSpace(ep)
						}
						if ak, ok := llmMap["apiKey"].(string); ok {
							llmApiKey = strings.TrimSpace(ak)
						}
						if m, ok := llmMap["model"].(string); ok && m != "" {
							llmModel = m
						}
						if t, ok := llmMap["temp"].(float64); ok {
							llmTemp = t
						}
						if mr, ok := llmMap["maxRetries"].(float64); ok && int(mr) > 0 {
							llmMaxRetries = int(mr)
						}
					}
				}
			}
		}

		if llmEndpoint == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "LLM не настроен. Включите LLM-портировщик в Конфигурации."})
			return
		}

		body, _ := io.ReadAll(io.LimitReader(r.Body, 2<<20))
		var req struct {
			CSCode string `json:"cs_code"`
			Name   string `json:"name"`
			Host   string `json:"host"`
		}
		if stdjson.Unmarshal(body, &req) != nil || req.CSCode == "" || req.Name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cs_code и name обязательны"})
			return
		}

		req.Name = strings.TrimSpace(strings.ToLower(req.Name))
		if !isValidCustBalName(req.Name) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid balancer name"})
			return
		}

		// Ping LLM before starting background job.
		llm := porter.NewLLMClientWithKey(llmEndpoint, llmApiKey, llmModel, llmTemp)
		pingCtx, pingCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer pingCancel()
		if err := llm.Ping(pingCtx); err != nil {
			log.Printf("porter: LLM ping failed: %v", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{
				"error": fmt.Sprintf("LLM сервер недоступен (%s): %v", llmEndpoint, err),
			})
			return
		}

		// Create job.
		jobID := fmt.Sprintf("%s_%d", req.Name, time.Now().UnixMilli())
		job := &porterJob{status: "running", step: 1, message: "Запуск LLM..."}
		porterJobs.Lock()
		porterJobs.m[jobID] = job
		porterJobs.Unlock()

		log.Printf("porter: starting LLM port for %q, job=%s, endpoint=%s", req.Name, jobID, llmEndpoint)

		// Run in background goroutine (not tied to HTTP request context).
		go func() {
			defer func() {
				if rv := recover(); rv != nil {
					log.Printf("porter: PANIC in job %s: %v", jobID, rv)
					job.mu.Lock()
					job.status = "error"
					job.message = fmt.Sprintf("internal panic: %v", rv)
					job.mu.Unlock()
				}
			}()

			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
			defer cancel()

			p := &porter.Porter{
				LLM:        llm,
				MaxRetries: llmMaxRetries,
				Verbose:    true,
				OnProgress: func(step int, message string) {
					log.Printf("porter: [step %d] %s", step, message)
					job.mu.Lock()
					job.step = step
					job.message = message
					job.mu.Unlock()
				},
			}

			portReq := porter.PortRequest{
				CSCode:     req.CSCode,
				CSFileName: req.Name + ".cs",
				Name:       req.Name,
			}

			baseDir := pool.BaseDir()
			buildFn := func(code string) (string, error) {
				dir := filepath.Join(baseDir, req.Name)
				if err := os.MkdirAll(dir, 0755); err != nil {
					return "", fmt.Errorf("mkdir: %w", err)
				}
				if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(code), 0644); err != nil {
					return "", fmt.Errorf("write main.go: %w", err)
				}
				return custbal.Compile(dir, req.Name)
			}

			startTime := time.Now()
			result, err := p.GenerateAndFix(ctx, portReq, buildFn)
			elapsed := time.Since(startTime).Round(time.Second)

			if err != nil || result == nil || !result.BuildOK {
				errMsg := "неизвестная ошибка"
				if err != nil {
					errMsg = err.Error()
				} else if result != nil {
					errMsg = result.Error
				}
				log.Printf("porter: LLM porting failed for %s after %s: %s", req.Name, elapsed, errMsg)
				job.mu.Lock()
				job.status = "error"
				job.message = errMsg
				job.elapsed = elapsed.String()
				job.attempts = safeAttempts(result)
				job.goCode = safeGoCode(result)
				job.mu.Unlock()
				return
			}

			// Deploy.
			// Host is left empty — LLM already sets defaultHost in the generated code.
			// The -host flag is only for manual overrides from the admin UI.
			deployCfg := custbal.BalancerConfig{
				Name:        req.Name,
				AutoStart:   true,
				ContentType: porter.DetectContentType(req.CSCode),
			}
			output, deployErr := pool.Deploy(req.Name, result.GoCode, deployCfg)
			if deployErr != nil {
				log.Printf("porter: deploy failed for %s: %v", req.Name, deployErr)
				job.mu.Lock()
				job.status = "error"
				job.message = fmt.Sprintf("Компиляция OK, но деплой не удался: %v", deployErr)
				job.goCode = result.GoCode
				job.attempts = result.Attempts
				job.elapsed = elapsed.String()
				job.mu.Unlock()
				return
			}

			if rm := pool.RouteMap(); rm[req.Name] != "" {
				dynRoutes.Register(req.Name, rm[req.Name])
			}

			log.Printf("porter: successfully ported %s in %s (%d attempts)", req.Name, elapsed, result.Attempts)
			job.mu.Lock()
			job.status = "done"
			job.goCode = result.GoCode
			job.attempts = result.Attempts
			job.elapsed = elapsed.String()
			job.output = output
			job.mu.Unlock()

			// Cleanup old jobs after 10 min.
			time.AfterFunc(10*time.Minute, func() {
				porterJobs.Lock()
				delete(porterJobs.m, jobID)
				porterJobs.Unlock()
			})
		}()

		// Return job ID immediately.
		writeJSON(w, http.StatusOK, map[string]string{
			"job_id": jobID,
			"status": "started",
		})
	}
}

// tgAdminPorterStatusHandler returns the status of a porter job.
// GET /{adminPath}/api/porter?job_id=xxx
func tgAdminPorterStatusHandler(tgStore *tgauth.Store, adminStore *tgauth.AdminIDStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := tgAdminAuthCheck(w, r, tgStore, adminStore); !ok {
			return
		}

		jobID := r.URL.Query().Get("job_id")
		if jobID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "job_id required"})
			return
		}

		porterJobs.Lock()
		job, ok := porterJobs.m[jobID]
		porterJobs.Unlock()

		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found", "status": "not_found"})
			return
		}

		job.mu.Lock()
		resp := map[string]any{
			"status":   job.status,
			"step":     job.step,
			"message":  job.message,
			"go_code":  job.goCode,
			"attempts": job.attempts,
			"elapsed":  job.elapsed,
			"output":   job.output,
		}
		job.mu.Unlock()

		writeJSON(w, http.StatusOK, resp)
	}
}

func safeAttempts(r *porter.PortResult) int {
	if r == nil {
		return 0
	}
	return r.Attempts
}

func safeGoCode(r *porter.PortResult) string {
	if r == nil {
		return ""
	}
	return r.GoCode
}
