package litesrc

import (
	stdjson "encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/proxylink"
)

// mikaiChecker implements the Mikai Ukrainian anime balancer.
// Uses mikai.club REST API for anime search, details with relations,
// provider-based voice building, and moonanime/ashdi stream resolution.
type mikaiChecker struct {
	client *http.Client
	host   string

	detailsCache sync.Map // id -> *mikaiAnime
}

// --- JSON API models ---

type mikaiSearchResponse struct {
	Ok     bool         `json:"ok"`
	Total  int          `json:"total"`
	Result []mikaiAnime `json:"result"`
}

type mikaiDetailResponse struct {
	Ok     bool       `json:"ok"`
	Result mikaiAnime `json:"result"`
}

type mikaiAnime struct {
	ID        int             `json:"id"`
	Slug      string          `json:"slug"`
	Format    string          `json:"format"`
	Year      int             `json:"year"`
	StartDate string          `json:"startDate"`
	Players   []mikaiPlayer   `json:"players"`
	Relations []mikaiRelation `json:"relations"`
	Details   *mikaiDetails   `json:"details"`

	cachedAt time.Time `json:"-"`
}

type mikaiDetails struct {
	Names *mikaiNames `json:"names"`
}

type mikaiNames struct {
	Name        string `json:"name"`
	NameNative  string `json:"nameNative"`
	NameEnglish string `json:"nameEnglish"`
}

type mikaiPlayer struct {
	Team      *mikaiTeam      `json:"team"`
	IsSubs    bool            `json:"isSubs"`
	Providers []mikaiProvider `json:"providers"`
}

type mikaiTeam struct {
	Name string `json:"name"`
}

type mikaiProvider struct {
	Name     string                 `json:"name"`
	Episodes []mikaiProviderEpisode `json:"episodes"`
}

type mikaiProviderEpisode struct {
	Number   int    `json:"number"`
	PlayLink string `json:"playLink"`
}

type mikaiRelation struct {
	RelationType string              `json:"relationType"`
	Anime        *mikaiRelationAnime `json:"anime"`
}

type mikaiRelationAnime struct {
	ID int `json:"id"`
}

// --- Voice structure ---

type mikaiVoiceInfo struct {
	displayName  string
	providerName string
	isSubs       bool
	seasons      map[int][]mikaiEpisodeInfo
}

type mikaiEpisodeInfo struct {
	number int
	title  string
	url    string
}

func NewMikaiChecker(cfg config.Config) *mikaiChecker {
	host := strings.TrimSpace(cfg.Online.Mikai.Host)
	if host == "" {
		host = "https://api.mikai.me/v1"
	}
	host = strings.TrimRight(host, "/")

	return &mikaiChecker{
		client: httpclient.NewForBalancer("mikai", 14*time.Second),
		host:   host,
	}
}

func (c *mikaiChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		raw := strings.TrimPrefix(strings.ToLower(req.URL.Path), "/lite/")

		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show := c.checkSearch(req)
			writeCheckSearchResponse(w, show, pluginQualityBadgeGet("mikai"))
			return
		}

		if raw == "mikai/play" {
			c.play(w, req, links)
			return
		}

		c.index(w, req, links)
	}
}

func (c *mikaiChecker) checkSearch(req *http.Request) bool {
	q := req.URL.Query()
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	year, _ := getsTVQueryInt(q.Get("year"))
	results := c.search(req, title, originalTitle, year)
	return len(results) > 0
}

// --- Search ---

func (c *mikaiChecker) search(req *http.Request, title, originalTitle string, year int) []mikaiAnime {
	find := func(query string) []mikaiAnime {
		if query == "" {
			return nil
		}
		searchURL := fmt.Sprintf("%s/anime/search?page=1&limit=24&sort=year&order=desc&name=%s",
			c.host, url.QueryEscape(query))
		httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, searchURL, nil)
		if err != nil {
			return nil
		}
		httpReq.Header.Set("User-Agent", "Mozilla/5.0")
		httpReq.Header.Set("Referer", c.host)
		httpReq.Header.Set("Accept", "application/json")

		resp, err := c.client.Do(httpReq)
		if err != nil {
			return nil
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		if err != nil {
			return nil
		}
		var sr mikaiSearchResponse
		if err := stdjson.Unmarshal(body, &sr); err != nil {
			return nil
		}
		return sr.Result
	}

	results := find(title)
	if len(results) == 0 {
		results = find(originalTitle)
	}
	if len(results) == 0 {
		return nil
	}

	// Filter by year if provided
	if year > 0 {
		var byYear []mikaiAnime
		for _, r := range results {
			if r.Year == year {
				byYear = append(byYear, r)
			}
		}
		if len(byYear) > 0 {
			return byYear
		}
	}

	return results
}

// --- Details ---

func (c *mikaiChecker) getDetails(req *http.Request, id int) *mikaiAnime {
	cacheKey := fmt.Sprintf("details:%d", id)
	if v, ok := c.detailsCache.Load(cacheKey); ok {
		if d, ok := v.(*mikaiAnime); ok && time.Since(d.cachedAt) < 20*time.Minute {
			return d
		}
	}

	apiURL := fmt.Sprintf("%s/anime/%d", c.host, id)
	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, apiURL, nil)
	if err != nil {
		return nil
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0")
	httpReq.Header.Set("Referer", c.host)
	httpReq.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil
	}
	var dr mikaiDetailResponse
	if err := stdjson.Unmarshal(body, &dr); err != nil {
		return nil
	}
	dr.Result.cachedAt = time.Now()
	c.detailsCache.Store(cacheKey, &dr.Result)
	return &dr.Result
}

// --- Collect season details (relations) ---

func (c *mikaiChecker) collectSeasonDetails(req *http.Request, details *mikaiAnime) []*mikaiAnime {
	seasonDetails := []*mikaiAnime{details}

	if len(details.Relations) == 0 {
		return seasonDetails
	}

	seen := map[int]struct{}{details.ID: {}}
	for _, rel := range details.Relations {
		if !mikaiShouldIncludeRelation(rel.RelationType) {
			continue
		}
		if rel.Anime == nil || rel.Anime.ID == 0 {
			continue
		}
		if _, ok := seen[rel.Anime.ID]; ok {
			continue
		}
		seen[rel.Anime.ID] = struct{}{}

		relDetails := c.getDetails(req, rel.Anime.ID)
		if relDetails == nil || len(relDetails.Players) == 0 {
			continue
		}
		seasonDetails = append(seasonDetails, relDetails)
	}

	return mikaiOrderSeasonDetails(seasonDetails)
}

func mikaiShouldIncludeRelation(relationType string) bool {
	switch strings.ToLower(strings.TrimSpace(relationType)) {
	case "other", "parent", "sequel", "prequel":
		return true
	}
	return false
}

func mikaiOrderSeasonDetails(details []*mikaiAnime) []*mikaiAnime {
	seen := make(map[int]struct{})
	var unique []*mikaiAnime
	for _, d := range details {
		if d == nil {
			continue
		}
		if _, ok := seen[d.ID]; ok {
			continue
		}
		seen[d.ID] = struct{}{}
		unique = append(unique, d)
	}

	sort.Slice(unique, func(i, j int) bool {
		yi, yj := unique[i].Year, unique[j].Year
		if yi == 0 {
			yi = 9999
		}
		if yj == 0 {
			yj = 9999
		}
		if yi != yj {
			return yi < yj
		}
		return unique[i].ID < unique[j].ID
	})
	return unique
}

// --- Build voices ---

func (c *mikaiChecker) buildVoices(seasonDetails []*mikaiAnime) map[string]*mikaiVoiceInfo {
	voices := make(map[string]*mikaiVoiceInfo)
	voiceKeyMap := make(map[string]string)
	seasonNumber := 1

	for _, details := range seasonDetails {
		if details == nil || len(details.Players) == 0 {
			seasonNumber++
			continue
		}

		totalProviders := 0
		for _, p := range details.Players {
			totalProviders += len(p.Providers)
		}

		for _, player := range details.Players {
			if len(player.Providers) == 0 {
				continue
			}

			teamName := "Озвучка"
			if player.Team != nil && strings.TrimSpace(player.Team.Name) != "" {
				teamName = player.Team.Name
			}

			baseName := teamName
			if player.IsSubs {
				baseName = teamName + " (Субтитри)"
			}

			providerIndex := 0
			for _, provider := range player.Providers {
				providerIndex++
				if len(provider.Episodes) == 0 {
					continue
				}

				displayName := baseName
				if totalProviders > 1 && strings.TrimSpace(provider.Name) != "" {
					displayName = fmt.Sprintf("[%s] %s", provider.Name, baseName)
				}

				providerKey := provider.Name
				if strings.TrimSpace(providerKey) == "" {
					providerKey = fmt.Sprintf("provider-%d", providerIndex)
				}
				voiceKey := fmt.Sprintf("%s|%s|%v", providerKey, teamName, player.IsSubs)

				voiceName, exists := voiceKeyMap[voiceKey]
				if !exists {
					displayName = mikaiEnsureUniqueName(voices, displayName)
					voiceKeyMap[voiceKey] = displayName
					voices[displayName] = &mikaiVoiceInfo{
						displayName:  displayName,
						providerName: provider.Name,
						isSubs:       player.IsSubs,
						seasons:      make(map[int][]mikaiEpisodeInfo),
					}
					voiceName = displayName
				}

				voice := voices[voiceName]

				var episodes []mikaiEpisodeInfo
				fallbackIdx := 1
				for _, ep := range provider.Episodes {
					if strings.TrimSpace(ep.PlayLink) == "" {
						continue
					}
					num := ep.Number
					if num <= 0 {
						num = fallbackIdx
						fallbackIdx++
					}
					episodes = append(episodes, mikaiEpisodeInfo{
						number: num,
						title:  fmt.Sprintf("Епізод %d", num),
						url:    ep.PlayLink,
					})
				}

				sort.Slice(episodes, func(i, j int) bool {
					return episodes[i].number < episodes[j].number
				})

				if len(episodes) > 0 {
					voice.seasons[seasonNumber] = episodes
				}
			}
		}
		seasonNumber++
	}

	return voices
}

func mikaiEnsureUniqueName(voices map[string]*mikaiVoiceInfo, name string) string {
	if _, ok := voices[name]; !ok {
		return name
	}
	idx := 2
	for {
		candidate := fmt.Sprintf("%s %d", name, idx)
		if _, ok := voices[candidate]; !ok {
			return candidate
		}
		idx++
	}
}

func mikaiNeedsResolve(providerName, streamLink string) bool {
	pn := strings.ToLower(providerName)
	if pn == "ashdi" || pn == "moonanime" {
		return true
	}
	lower := strings.ToLower(streamLink)
	return strings.Contains(lower, "ashdi.vip") || strings.Contains(lower, "moonanime.art")
}

func mikaiGetSeasonSet(voice *mikaiVoiceInfo) map[int]struct{} {
	set := make(map[int]struct{})
	if voice == nil {
		return set
	}
	for sn, eps := range voice.seasons {
		for _, ep := range eps {
			if ep.url != "" {
				set[sn] = struct{}{}
				break
			}
		}
	}
	return set
}

func mikaiSameSeasonSet(a, b map[int]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

// --- Index ---

func (c *mikaiChecker) index(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	year, _ := getsTVQueryInt(q.Get("year"))
	serial, _ := getsTVQueryInt(q.Get("serial"))
	s, _ := getsTVQueryInt(q.Get("s"))
	t := strings.TrimSpace(q.Get("t"))
	host := hostFromRequest(req)

	results := c.search(req, title, originalTitle, year)
	if len(results) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	selected := results[0]
	details := c.getDetails(req, selected.ID)
	if details == nil || len(details.Players) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	isSerial := serial == 1 || (serial == -1 && !strings.EqualFold(details.Format, "movie"))
	seasonDetails := c.collectSeasonDetails(req, details)
	voices := c.buildVoices(seasonDetails)
	if len(voices) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	displayTitle := title
	if displayTitle == "" && details.Details != nil && details.Details.Names != nil {
		displayTitle = details.Details.Names.Name
	}
	if displayTitle == "" {
		displayTitle = originalTitle
	}

	if isSerial {
		c.indexSerial(w, req, rjson, host, title, originalTitle, year, s, t, voices, links, displayTitle)
	} else {
		c.indexMovie(w, req, rjson, host, displayTitle, originalTitle, voices, links)
	}
}

func (c *mikaiChecker) indexSerial(w http.ResponseWriter, req *http.Request, rjson bool,
	host, title, originalTitle string, year, s int, t string,
	voices map[string]*mikaiVoiceInfo, links *proxylink.Manager, displayTitle string) {

	q := req.URL.Query()
	imdbID := q.Get("imdb_id")
	kpID := q.Get("kinopoisk_id")

	// Build sorted voice list
	type voiceEntry struct {
		key  string
		info *mikaiVoiceInfo
	}
	var voiceList []voiceEntry
	for k, v := range voices {
		voiceList = append(voiceList, voiceEntry{key: k, info: v})
	}
	sort.Slice(voiceList, func(i, j int) bool {
		return voiceList[i].key < voiceList[j].key
	})

	// Get season numbers based on voice restriction
	var restrictedVoice *mikaiVoiceInfo
	if t != "" {
		if v, ok := voices[t]; ok {
			restrictedVoice = v
		}
	}

	seasonSet := make(map[int]struct{})
	if restrictedVoice != nil {
		seasonSet = mikaiGetSeasonSet(restrictedVoice)
	} else {
		for _, v := range voices {
			for sn := range mikaiGetSeasonSet(v) {
				seasonSet[sn] = struct{}{}
			}
		}
	}

	var seasonNumbers []int
	for sn := range seasonSet {
		seasonNumbers = append(seasonNumbers, sn)
	}
	sort.Ints(seasonNumbers)

	if len(seasonNumbers) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if s == -1 || s == 0 {
		// Season selector
		var sb strings.Builder
		for _, sn := range seasonNumbers {
			link := fmt.Sprintf("%s/lite/mikai?imdb_id=%s&kinopoisk_id=%s&title=%s&original_title=%s&year=%d&serial=1&s=%d",
				host, url.QueryEscape(imdbID), url.QueryEscape(kpID),
				url.QueryEscape(title), url.QueryEscape(originalTitle), year, sn)
			if restrictedVoice != nil {
				link += "&t=" + url.QueryEscape(t)
			}
			getsTVAppendSeasonHTML(&sb, map[string]any{
				"method": "link",
				"url":    link,
				"s":      sn, // season number for capi's season scan (data-json is all it parses)
			}, strconv.Itoa(sn), false)
		}
		if rjson {
			writeJSON(w, http.StatusOK, map[string]any{"html": sb.String()})
			return
		}
		writeHTML(w, http.StatusOK, sb.String())
		return
	}

	// Voice + episode for selected season
	voicesForSeason := make([]voiceEntry, 0)
	for _, v := range voiceList {
		if _, ok := v.info.seasons[s]; ok {
			voicesForSeason = append(voicesForSeason, v)
		}
	}
	if len(voicesForSeason) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Auto-select voice
	if t == "" {
		t = voicesForSeason[0].key
	} else if _, ok := voices[t]; !ok {
		t = voicesForSeason[0].key
	}

	selectedVoice := voices[t]
	if selectedVoice == nil || selectedVoice.seasons[s] == nil {
		writeGetsTVEmpty(w, rjson)
		return
	}

	selectedSeasonSet := mikaiGetSeasonSet(selectedVoice)

	var sb strings.Builder

	// Voice buttons
	for _, v := range voicesForSeason {
		targetSeasonSet := mikaiGetSeasonSet(v.info)
		sameSet := mikaiSameSeasonSet(targetSeasonSet, selectedSeasonSet)

		var voiceLink string
		if sameSet {
			voiceLink = fmt.Sprintf("%s/lite/mikai?imdb_id=%s&kinopoisk_id=%s&title=%s&original_title=%s&year=%d&serial=1&s=%d&t=%s",
				host, url.QueryEscape(imdbID), url.QueryEscape(kpID),
				url.QueryEscape(title), url.QueryEscape(originalTitle), year, s, url.QueryEscape(v.key))
		} else {
			voiceLink = fmt.Sprintf("%s/lite/mikai?imdb_id=%s&kinopoisk_id=%s&title=%s&original_title=%s&year=%d&serial=1&s=-1&t=%s",
				host, url.QueryEscape(imdbID), url.QueryEscape(kpID),
				url.QueryEscape(title), url.QueryEscape(originalTitle), year, url.QueryEscape(v.key))
		}

		getsTVAppendVoiceHTML(&sb, map[string]any{
			"name":   v.key,
			"url":    voiceLink,
			"active": v.key == t,
		})
	}

	// Episodes
	for _, ep := range selectedVoice.seasons[s] {
		if ep.url == "" {
			continue
		}
		epName := ep.title
		if epName == "" {
			epName = fmt.Sprintf("Епізод %d", ep.number)
		}

		if mikaiNeedsResolve(selectedVoice.providerName, ep.url) {
			callURL := fmt.Sprintf("%s/lite/mikai/play?url=%s&title=%s",
				host, url.QueryEscape(ep.url), url.QueryEscape(displayTitle))
			getsTVAppendMovieHTML(&sb, map[string]any{
				"method": "call",
				"url":    callURL,
				"title":  fmt.Sprintf("%s / %s", displayTitle, epName),
			}, epName, false, s, ep.number)
		} else {
			proxyURL := streamProxyURL(req, ep.url, "mikai", links)
			getsTVAppendMovieHTML(&sb, map[string]any{
				"method": "play",
				"url":    proxyURL,
				"title":  fmt.Sprintf("%s / %s", displayTitle, epName),
			}, epName, false, s, ep.number)
		}
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"html": sb.String()})
		return
	}
	writeHTML(w, http.StatusOK, sb.String())
}

func (c *mikaiChecker) indexMovie(w http.ResponseWriter, req *http.Request, rjson bool,
	host, displayTitle, originalTitle string,
	voices map[string]*mikaiVoiceInfo, links *proxylink.Manager) {

	var sb strings.Builder
	count := 0

	// Sort voice keys for deterministic order
	var voiceKeys []string
	for k := range voices {
		voiceKeys = append(voiceKeys, k)
	}
	sort.Strings(voiceKeys)

	for _, vk := range voiceKeys {
		voice := voices[vk]
		// Get first episode across all seasons
		var firstEp *mikaiEpisodeInfo
		var seasonNums []int
		for sn := range voice.seasons {
			seasonNums = append(seasonNums, sn)
		}
		sort.Ints(seasonNums)
		for _, sn := range seasonNums {
			eps := voice.seasons[sn]
			if len(eps) > 0 {
				firstEp = &eps[0]
				break
			}
		}

		if firstEp == nil || firstEp.url == "" {
			continue
		}

		if mikaiNeedsResolve(voice.providerName, firstEp.url) {
			// Try ashdi VOD expansion
			if strings.Contains(strings.ToLower(firstEp.url), "ashdi.vip/vod") {
				ashdiChk := &animeonChecker{client: c.client, host: c.host}
				ashdiStreams := ashdiChk.parseAshdiPageStreams(req, firstEp.url)
				if len(ashdiStreams) > 0 {
					for _, as := range ashdiStreams {
						optionName := fmt.Sprintf("%s %s", voice.displayName, as.title)
						callURL := fmt.Sprintf("%s/lite/mikai/play?url=%s&title=%s",
							host, url.QueryEscape(as.link), url.QueryEscape(displayTitle))
						getsTVAppendMovieHTML(&sb, map[string]any{
							"method": "call",
							"url":    callURL,
							"title":  displayTitle,
						}, optionName, count == 0, 0, 0)
						count++
					}
					continue
				}
			}

			callURL := fmt.Sprintf("%s/lite/mikai/play?url=%s&title=%s",
				host, url.QueryEscape(firstEp.url), url.QueryEscape(displayTitle))
			getsTVAppendMovieHTML(&sb, map[string]any{
				"method": "call",
				"url":    callURL,
				"title":  displayTitle,
			}, voice.displayName, count == 0, 0, 0)
		} else {
			proxyURL := streamProxyURL(req, firstEp.url, "mikai", links)
			getsTVAppendMovieHTML(&sb, map[string]any{
				"method": "play",
				"url":    proxyURL,
				"title":  displayTitle,
			}, voice.displayName, count == 0, 0, 0)
		}
		count++
	}

	if count == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"html": sb.String()})
		return
	}
	writeHTML(w, http.StatusOK, sb.String())
}

// --- Play sub-route ---

func (c *mikaiChecker) play(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	rawURL := strings.TrimSpace(q.Get("url"))
	title := strings.TrimSpace(q.Get("title"))

	if rawURL == "" {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	streamLink := c.resolveVideoURL(req, rawURL)
	if streamLink == "" {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}

	var proxyURL string
	if strings.Contains(strings.ToLower(streamLink), "ashdi.vip") {
		proxyURL = streamProxyURLWithHeaders(req, streamLink, "mikai", links, map[string]string{
			"User-Agent": "Mozilla/5.0",
			"Referer":    "https://ashdi.vip/",
		})
	} else {
		proxyURL = streamProxyURL(req, streamLink, "mikai", links)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"method": "play",
		"url":    proxyURL,
		"title":  title,
	})
}

func (c *mikaiChecker) resolveVideoURL(req *http.Request, rawURL string) string {
	if rawURL == "" {
		return ""
	}
	lower := strings.ToLower(rawURL)
	if strings.Contains(lower, "moonanime.art") {
		return c.parseMoonAnimePage(req, rawURL)
	}
	if strings.Contains(lower, "ashdi.vip") {
		ashdiChk := &animeonChecker{client: c.client, host: c.host}
		return ashdiChk.parseAshdiPage(req, rawURL)
	}
	return rawURL
}

func (c *mikaiChecker) parseMoonAnimePage(req *http.Request, rawURL string) string {
	requestURL := rawURL
	if !strings.Contains(requestURL, "player=") {
		if strings.Contains(requestURL, "?") {
			requestURL += "&player=mikai.me"
		} else {
			requestURL += "?player=mikai.me"
		}
	}

	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, requestURL, nil)
	if err != nil {
		return ""
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0")
	httpReq.Header.Set("Referer", c.host)

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ""
	}

	m := animeonMoonFileRe.FindSubmatch(body)
	if len(m) >= 2 {
		return string(m[1])
	}
	return ""
}
