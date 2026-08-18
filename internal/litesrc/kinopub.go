package litesrc

import (
	"context"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"io"
	"lampac-go/internal/httpclient"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/kit"
	"lampac-go/internal/proxylink"

	"github.com/rs/zerolog/log"
)

// ---------------------------------------------------------------------------
// Data structures
// ---------------------------------------------------------------------------

type kinoPubChecker struct {
	client     *http.Client
	hosts      []string // failover-ordered API mirrors (configured first)
	token      string
	tokens     []string
	filetype   string // "hls" (default), "hls4", "http"
	tokenStore *kinopubTokenStore
	rhub       bool
}

// apiHost returns the sticky-selected API mirror.
func (k *kinoPubChecker) apiHost() string { return kinopubActiveHost(k.hosts) }

type kinoPubSearchResponse struct {
	Items []kinoPubItem `json:"items"`
}

type kinoPubItem struct {
	ID        int               `json:"id"`
	Title     string            `json:"title"`
	Year      int               `json:"year"`
	Kinopoisk int64             `json:"kinopoisk"`
	IMDB      any               `json:"imdb"`
	Type      string            `json:"type"`
	Voice     string            `json:"voice"`
	Posters   map[string]string `json:"posters"`
}

// Item detail structures
type kpRootObject struct {
	Status int    `json:"status"`
	Item   kpItem `json:"item"`
}

type kpItem struct {
	Quality int        `json:"quality"`
	Videos  []kpVideo  `json:"videos"`
	Seasons []kpSeason `json:"seasons"`
}

type kpVideo struct {
	ID        int64        `json:"id"`
	Files     []kpFile     `json:"files"`
	Audios    []kpAudio    `json:"audios"`
	Subtitles []kpSubtitle `json:"subtitles"`
	Watching  *kpWatching  `json:"watching,omitempty"`
}

// kpWatching carries the per-video resume position kino.pub
// remembers server-side. Surfaced to the Lampa frontend so the
// player can seek to it on Play. Populated only when the user has
// previously watched the video on any device.
type kpWatching struct {
	Time   int64 `json:"time"`   // seconds
	Status int   `json:"status"` // 0=new, 1=in progress, -1=done; we just pass through
}

type kpFile struct {
	Quality string `json:"quality"`
	File    string `json:"file"`
	URL     kpURL  `json:"url"`
}

type kpURL struct {
	HTTP string `json:"http"`
	HLS  string `json:"hls"`
	HLS4 string `json:"hls4"`
}

type kpAudio struct {
	Index  int       `json:"index"`
	Lang   string    `json:"lang"`
	Codec  string    `json:"codec"`
	Author *kpAuthor `json:"author"`
	Type   *kpAuthor `json:"type"`
}

type kpAuthor struct {
	ID    *int   `json:"id"`
	Title string `json:"title"`
}

type kpSubtitle struct {
	Lang string `json:"lang"`
	URL  string `json:"url"`
}

type kpSeason struct {
	Number   int         `json:"number"`
	Episodes []kpEpisode `json:"episodes"`
}

type kpEpisode struct {
	ID        int64        `json:"id"`
	Number    int          `json:"number"`
	Title     string       `json:"title"`
	Files     []kpFile     `json:"files"`
	Audios    []kpAudio    `json:"audios"`
	Subtitles []kpSubtitle `json:"subtitles"`
	Watching  *kpWatching  `json:"watching,omitempty"`
}

// ---------------------------------------------------------------------------
// Constructor
// ---------------------------------------------------------------------------

func NewKinoPubChecker(cfg config.Config) *kinoPubChecker {
	hosts := kinopubAPIHosts(cfg.Online.KinoPub.Host)

	token := strings.TrimSpace(cfg.Online.KinoPub.Token)
	var tokens []string
	for _, item := range cfg.Online.KinoPub.Tokens {
		item = strings.TrimSpace(item)
		if item != "" {
			tokens = append(tokens, item)
		}
	}
	if token == "" && len(tokens) > 0 {
		token = tokens[0]
	}

	filetype := strings.TrimSpace(cfg.Online.KinoPub.Filetype)
	if filetype == "" {
		filetype = "hls"
	}

	// Persistent token store: lives at database/kinopub_token.json
	// and holds (access, refresh, expires_at) from the device-flow.
	// Loaded eagerly so a stale-but-refreshable token survives lampac
	// restart without re-running activation. If config.toml has a
	// hand-set token and the store is empty (first boot after migrate),
	// we seed it as a one-way bridge — store overrides config from
	// then on because it can refresh.
	store := newKinopubTokenStore(cfg.Compat.RepoRoot)
	if err := store.Load(); err != nil {
		log.Warn().Err(err).Msg("kinopub: failed to load persisted token; falling back to config")
	}
	if token != "" {
		store.Seed(token)
	}

	return &kinoPubChecker{
		client:     httpclient.NewForBalancer("kinopub", 15*time.Second),
		hosts:      hosts,
		token:      token,
		tokens:     tokens,
		filetype:   filetype,
		tokenStore: store,
		rhub:       cfg.Online.KinoPub.Rhub,
	}
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

func (k *kinoPubChecker) Handle(cfg config.Config, proxyLinks *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show := k.checkSearch(req)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			if show {
				quality := pluginQualityBadgeGet("kinopub")
				if quality != "" {
					_, _ = fmt.Fprintf(w, `{"type":"movie","rch":false,"quality":"%s"}`, quality)
				} else {
					_, _ = w.Write([]byte(`{"type":"movie","rch":false}`))
				}
				return
			}
			_, _ = w.Write([]byte(`{"rch":false}`))
			return
		}

		raw := strings.TrimPrefix(strings.ToLower(req.URL.Path), "/lite/")
		raw = strings.TrimPrefix(raw, "rc/")

		switch {
		case strings.HasPrefix(raw, "kinopub/subtitles.json"):
			k.subtitles(w, req, proxyLinks)
		case strings.HasPrefix(raw, "kinopub/watching/marktime"):
			k.watchingMarktime(w, req)
		case strings.HasPrefix(raw, "kinopubpro"):
			k.deviceActivation(w, req)
		default:
			k.embed(w, req, proxyLinks)
		}
	}
}

// ---------------------------------------------------------------------------
// embed — main endpoint: search → post → render
// ---------------------------------------------------------------------------

func (k *kinoPubChecker) embed(w http.ResponseWriter, req *http.Request, proxyLinks *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	year, _ := strconv.Atoi(strings.TrimSpace(q.Get("year")))
	clarification, _ := strconv.Atoi(q.Get("clarification"))
	imdbID := strings.ToLower(strings.TrimSpace(q.Get("imdb_id")))
	kinopoiskID := strings.TrimSpace(q.Get("kinopoisk_id"))
	postid, _ := strconv.Atoi(strings.TrimSpace(q.Get("postid")))
	s, _ := strconv.Atoi(strings.TrimSpace(q.Get("s")))
	t, _ := strconv.Atoi(strings.TrimSpace(q.Get("t")))
	codec := strings.TrimSpace(q.Get("codec"))
	similar := parseBoolParam(q.Get("similar"))

	// Source-based postid (from Lampa source=kinopub&id=NNN)
	if postid == 0 {
		source := strings.ToLower(strings.TrimSpace(q.Get("source")))
		idParam := strings.TrimSpace(q.Get("id"))
		if source == "kinopub" && idParam != "" {
			postid, _ = strconv.Atoi(idParam)
		}
	}

	token := k.getToken(req.Context())

	// Kit: per-user token override
	if kitToken, ok := kit.TokenOverride(req.Context(), "KinoPub"); ok {
		token = kitToken
	}

	if token == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// With rhub on, the API calls below run on the user's device (RU IP). If no
	// device is bound yet, hand the client the hub details so it reconnects and
	// retries with ?nws_id.
	if rchGate(w, req, k.rhub) {
		return
	}

	// Search if no postid
	if postid == 0 {
		// DIAG (kinopub capi 0-voices): exactly what the handler received + which branch it takes.
		log.Debug().Str("title", title).Str("orig", originalTitle).Int("year", year).Int("clar", clarification).Str("imdb", imdbID).Bool("tok", token != "").Msg("kinopub: embed search in")
		searchResult := k.doSearch(req, token, title, originalTitle, year, clarification, imdbID, kinopoiskID)
		if searchResult == nil {
			log.Debug().Str("title", title).Msg("kinopub: embed → empty (doSearch nil)")
			writeGetsTVEmpty(w, rjson)
			return
		}

		if similar || searchResult.id == 0 {
			log.Debug().Int("items", len(searchResult.items)).Bool("similar", similar).Msg("kinopub: embed → similar/clarification (id=0)")
			if len(searchResult.items) == 0 {
				writeGetsTVEmpty(w, rjson)
				return
			}
			k.writeSimilar(w, req, rjson, searchResult.items, title, originalTitle)
			return
		}
		postid = searchResult.id
		log.Debug().Int("postid", postid).Msg("kinopub: embed → matched, fetching item")
	}

	// Fetch item
	root, ok := k.fetchItem(req, token, postid)
	if !ok {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(req)

	if root.Item.Videos != nil {
		k.renderMovie(w, req, rjson, host, root, title, originalTitle, postid, proxyLinks, token)
	} else if root.Item.Seasons != nil && len(root.Item.Seasons) > 0 {
		if s <= 0 {
			k.renderSeasons(w, rjson, host, root, title, originalTitle, postid)
		} else {
			k.renderEpisodes(w, req, rjson, host, root, title, originalTitle, postid, s, t, codec, proxyLinks, token)
		}
	} else {
		writeGetsTVEmpty(w, rjson)
	}
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

type kinoPubSearchResult struct {
	id    int
	items []kinoPubItem
}

func (k *kinoPubChecker) doSearch(req *http.Request, token, title, originalTitle string, year, clarification int, imdbID, kinopoiskID string) *kinoPubSearchResult {
	if title == "" && originalTitle == "" {
		return nil
	}

	goSearch := func(query string) *kinoPubSearchResult {
		if query == "" {
			return nil
		}
		items, ok := k.searchAPI(req, token, query)
		if !ok || len(items) == 0 {
			return nil
		}

		result := &kinoPubSearchResult{items: items}
		wantTitle := normalizeSearchTitle(query)
		var ids []int

		for _, item := range items {
			if strings.EqualFold(strings.TrimSpace(item.Type), "3d") {
				continue
			}
			// Match by kinopoisk/imdb
			if kinopoiskID != "" && isInt64(kinopoiskID) {
				kp, _ := strconv.ParseInt(kinopoiskID, 10, 64)
				if item.Kinopoisk == kp {
					result.id = item.ID
					return result
				}
			}
			if imdbID != "" && kpIMDBMatch(item.IMDB, imdbID) {
				result.id = item.ID
				return result
			}
			// Match by title+year
			if year > 0 && item.Year > 0 && item.Year != year && item.Year != year-1 && item.Year != year+1 {
				continue
			}
			itemTitle := normalizeSearchTitle(item.Title)
			if itemTitle != "" && wantTitle != "" {
				if strings.HasPrefix(itemTitle, wantTitle) || strings.HasSuffix(itemTitle, wantTitle) {
					ids = append(ids, item.ID)
				}
			}
		}
		if len(ids) == 1 && result.id == 0 {
			result.id = ids[0]
		}
		return result
	}

	if clarification == 1 {
		return goSearch(title)
	}

	if result := goSearch(originalTitle); result != nil {
		return result
	}
	return goSearch(title)
}

func (k *kinoPubChecker) searchAPI(req *http.Request, token, query string) ([]kinoPubItem, bool) {
	qs := url.Values{}
	qs.Set("q", query)
	qs.Set("access_token", token)
	qs.Set("field", "title")
	qs.Set("perpage", "200")

	// Try v1 first (proven in production), fall back to v1.1 from the Android-client lineage.
	// api.srvkp.com is multi-node and throttles high-volume search (capi drills kinopub on EVERY
	// resolve for EVERY viewer), so a single attempt intermittently returns HTTP 200 with an EMPTY
	// item list even for titles that exist (e.g. F1 → 3 items one second, 0 the next). A raw curl
	// rarely trips this; the server under load does. So treat an empty-but-OK v1 result like a miss
	// and fall through to v1.1 instead of returning empty immediately — the alternate endpoint often
	// returns the real hits. Bounded to 2 calls max (same worst case as before) to NOT amplify load
	// on the already-throttled API for genuinely-empty queries. `anyOK` lets the caller tell a valid
	// empty (foreign/untitled query) from a total failure.
	anyOK := false
	for i, path := range []string{"/v1/items/search", "/api2/v1.1/items/search"} {
		var root kinoPubSearchResponse
		if ok := k.kinopubAPIGet(req, path, qs, &root, 2<<20); !ok {
			continue
		}
		anyOK = true
		if len(root.Items) == 0 {
			continue // empty-but-OK → likely throttled/cold node; try the other endpoint
		}
		if i > 0 {
			log.Info().Str("path", path).Msg("kinopub: search recovered via v1.1 fallback")
		}
		log.Debug().Str("q", query).Str("path", path).Bool("hasTok", token != "").Int("items", len(root.Items)).Msg("kinopub: searchAPI items")
		return root.Items, true
	}
	log.Debug().Str("query", query).Bool("anyOK", anyOK).Msg("kinopub: search empty/exhausted")
	return nil, anyOK
}

// ---------------------------------------------------------------------------
// Fetch item detail
// ---------------------------------------------------------------------------

func (k *kinoPubChecker) fetchItem(req *http.Request, token string, postid int) (kpRootObject, bool) {
	qs := url.Values{}
	qs.Set("access_token", token)

	for i, tmpl := range []string{"/v1/items/%d", "/api2/v1.1/items/%d"} {
		path := fmt.Sprintf(tmpl, postid)
		var root kpRootObject
		if ok := k.kinopubAPIGet(req, path, qs, &root, 4<<20); !ok {
			continue
		}
		if root.Item.Seasons == nil && root.Item.Videos == nil {
			// Some "promo / not-available" items return an empty body
			// with 200 status — treat as a soft miss and try the next
			// API version, which sometimes carries the full payload.
			log.Debug().Int("postid", postid).Str("path", path).Msg("kinopub: fetchItem returned empty videos/seasons")
			continue
		}
		if i > 0 {
			log.Info().Int("postid", postid).Str("path", path).Msg("kinopub: fetchItem succeeded via v1.1 fallback")
		}
		return root, true
	}
	return kpRootObject{}, false
}

// kinopubAPIGet is the shared GET helper used by every kino.pub
// endpoint. Encapsulates header injection, status-code check, body
// decode + log line. Returns false on any failure (network, non-2xx,
// JSON decode error) so the caller can fall through to the next URL
// template. maxBody caps the JSON read — 2 MB for search, 4 MB for
// items (4K item payloads with multi-voice are big).
func (k *kinoPubChecker) kinopubAPIGet(req *http.Request, path string, qs url.Values, out any, maxBody int64) bool {
	// Mirror failover: retry the request on the next mirror after a
	// transport-level failure (dead host); an HTTP status from a live
	// mirror (401/404/5xx) is a real API answer — no rotation.
	var (
		body string
		err  error
	)
	attempts := len(k.hosts)
	if attempts < 1 {
		attempts = 1
	}
	for i := 0; i < attempts; i++ {
		host := k.apiHost()
		u, perr := url.Parse(host + path)
		if perr != nil {
			log.Debug().Err(perr).Str("path", path).Msg("kinopub: bad URL")
			return false
		}
		u.RawQuery = qs.Encode()
		target := u.String()

		// With rhub on + device bound, the API call (token is in the query string)
		// runs on the user's device (RU IP); otherwise the normal server-side client.
		body, err = rchFetch(req, k.rhub, target, kinopubHeaderMap(), func() (string, error) {
			httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, target, nil)
			if err != nil {
				return "", err
			}
			k.setHeaders(httpReq)
			resp, err := k.client.Do(httpReq)
			if err != nil {
				kinopubAdvanceHost(k.hosts, host)
				return "", fmt.Errorf("%w: %v", errKinopubTransport, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return "", fmt.Errorf("kinopub: status %d", resp.StatusCode)
			}
			b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
			if err != nil {
				return "", err
			}
			return string(b), nil
		})
		if err == nil && body != "" {
			break
		}
		if !errors.Is(err, errKinopubTransport) {
			break // live mirror answered — retrying elsewhere won't help
		}
	}
	if err != nil || body == "" {
		log.Debug().Err(err).Str("path", path).Msg("kinopub: fetch failed")
		return false
	}
	if err := stdjson.Unmarshal([]byte(body), out); err != nil {
		log.Debug().Err(err).Str("path", path).Msg("kinopub: decode failed")
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// Render movie
// ---------------------------------------------------------------------------

func (k *kinoPubChecker) renderMovie(w http.ResponseWriter, req *http.Request, rjson bool, host string, root kpRootObject, title, originalTitle string, postid int, proxyLinks *proxylink.Manager, token string) {
	baseTitle := getsTVJoinName(title, originalTitle)
	videos := root.Item.Videos
	if len(videos) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	data := make([]map[string]any, 0, 8)
	labels := make([]string, 0, 8)

	if k.filetype == "hls" {
		// HLS mode: one row per audio track
		v := videos[0]
		for _, a := range v.Audios {
			qualMap := make(map[string]string, len(v.Files))
			var bestURL string
			for _, f := range v.Files {
				hlsURL := f.URL.HLS
				if hlsURL == "" {
					continue
				}
				// Replace a1.m3u8 with a{index}.m3u8 for this audio
				hlsURL = strings.Replace(hlsURL, "a1.m3u8", fmt.Sprintf("a%d.m3u8", a.Index), 1)
				proxied := streamProxyURL(req, hlsURL, "kinopub", proxyLinks)
				qualMap[f.Quality] = proxied
				if bestURL == "" {
					bestURL = proxied
				}
			}
			if bestURL == "" {
				continue
			}

			voice := kpAudioVoice(a)

			// Subtitles
			subs := k.buildSubtitles(req, v.Subtitles, proxyLinks)
			var subsCall string
			if len(subs) == 0 {
				subsCall = fmt.Sprintf("%s/lite/kinopub/subtitles.json?mid=%d", host, v.ID)
			}

			row := map[string]any{
				"method":     "play",
				"url":        bestURL,
				"stream":     bestURL,
				"name":       voice,
				"voice_name": a.Codec,
				"title":      baseTitle,
			}
			if len(qualMap) > 0 {
				row["quality"] = qualMap
				row["qualitys"] = qualMap
			}
			if len(subs) > 0 {
				row["subtitles"] = subs
			}
			if subsCall != "" {
				row["subtitles_call"] = subsCall
			}
			// Resume-position hook for the frontend "продолжить
			// просмотр" UX. The plugin reads `timeline.time` and
			// seeks on play; on timeupdate it POSTs back to
			// /lite/kinopub/watching/marktime with the same fields.
			k.kpAttachTimeline(req, row, v.Watching, postid, int(v.ID))

			data = append(data, row)
			labels = append(labels, voice)
		}
	} else {
		// HTTP/HLS4 mode: one row per video (quality)
		for _, v := range videos {
			var voiceName string
			if v.Audios != nil {
				var parts []string
				for _, a := range v.Audios {
					if a.Lang == "eng" {
						if !containsStr(parts, "eng") {
							parts = append(parts, "eng")
						}
					} else {
						name := ""
						if a.Author != nil && a.Author.Title != "" {
							name = a.Author.Title
						} else if a.Type != nil && a.Type.Title != "" {
							name = a.Type.Title
						}
						if name != "" {
							label := fmt.Sprintf("%s (%s)", name, a.Lang)
							if !containsStr(parts, label) {
								parts = append(parts, label)
							}
						}
					}
				}
				voiceName = strings.Join(parts, ", ")
			}

			if len(v.Files) == 0 {
				continue
			}

			qualMap := make(map[string]string, len(v.Files))
			var bestURL string
			for _, f := range v.Files {
				var rawURL string
				if k.filetype == "hls4" {
					rawURL = f.URL.HLS4
				} else {
					rawURL = f.URL.HTTP
				}
				if rawURL == "" {
					continue
				}
				proxied := streamProxyURL(req, rawURL, "kinopub", proxyLinks)
				qualMap[normalizeQualityLabel(f.Quality)] = proxied
				if bestURL == "" {
					bestURL = proxied
				}
			}
			if bestURL == "" {
				continue
			}

			subs := k.buildSubtitles(req, v.Subtitles, proxyLinks)
			var subsCall string
			if len(subs) == 0 {
				subsCall = fmt.Sprintf("%s/lite/kinopub/subtitles.json?mid=%d", host, v.ID)
			}

			name := v.Files[0].Quality
			if voiceName != "" {
				name = voiceName
			}

			row := map[string]any{
				"method":     "play",
				"url":        bestURL,
				"stream":     bestURL,
				"name":       name,
				"voice_name": voiceName,
				"title":      baseTitle,
			}
			if len(qualMap) > 0 {
				row["quality"] = qualMap
				row["qualitys"] = qualMap
			}
			if len(subs) > 0 {
				row["subtitles"] = subs
			}
			if subsCall != "" {
				row["subtitles_call"] = subsCall
			}
			k.kpAttachTimeline(req, row, v.Watching, postid, int(v.ID))

			data = append(data, row)
			labels = append(labels, name)
		}
	}

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
	for i := range data {
		getsTVAppendMovieHTML(&sb, data[i], labels[i], i == 0, 0, 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// ---------------------------------------------------------------------------
// Render seasons
// ---------------------------------------------------------------------------

func (k *kinoPubChecker) renderSeasons(w http.ResponseWriter, rjson bool, host string, root kpRootObject, title, originalTitle string, postid int) {
	encTitle := url.QueryEscape(title)
	encOriginal := url.QueryEscape(originalTitle)

	data := make([]map[string]any, 0, len(root.Item.Seasons))
	sLabels := make([]string, 0, len(root.Item.Seasons))

	var qualLabel string
	if root.Item.Quality > 0 {
		qualLabel = fmt.Sprintf("%dp", root.Item.Quality)
	}
	_ = qualLabel

	for _, season := range root.Item.Seasons {
		link := fmt.Sprintf("%s/lite/kinopub?postid=%d&title=%s&original_title=%s&s=%d",
			host, postid, encTitle, encOriginal, season.Number)

		name := fmt.Sprintf("%d сезон", season.Number)
		row := map[string]any{
			"method": "link",
			"url":    link,
			"name":   name,
			"s":      season.Number,
		}
		data = append(data, row)
		sLabels = append(sLabels, name)
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
		getsTVAppendSeasonHTML(&sb, row, sLabels[i], i == 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// ---------------------------------------------------------------------------
// Render episodes
// ---------------------------------------------------------------------------

func (k *kinoPubChecker) renderEpisodes(w http.ResponseWriter, req *http.Request, rjson bool, host string, root kpRootObject, title, originalTitle string, postid, s, t int, codec string, proxyLinks *proxylink.Manager, token string) {
	encTitle := url.QueryEscape(title)
	encOriginal := url.QueryEscape(originalTitle)
	baseTitle := getsTVJoinName(title, originalTitle)

	// Find the season
	var season *kpSeason
	for i := range root.Item.Seasons {
		if root.Item.Seasons[i].Number == s {
			season = &root.Item.Seasons[i]
			break
		}
	}
	if season == nil || len(season.Episodes) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if k.filetype == "hls" {
		k.renderEpisodesHLS(w, req, rjson, host, baseTitle, encTitle, encOriginal, postid, s, t, codec, season, proxyLinks, token)
	} else {
		k.renderEpisodesHTTP(w, req, rjson, baseTitle, postid, s, season, proxyLinks, token)
	}
}

func (k *kinoPubChecker) renderEpisodesHLS(w http.ResponseWriter, req *http.Request, rjson bool, host, baseTitle, encTitle, encOriginal string, postid, s, t int, codec string, season *kpSeason, proxyLinks *proxylink.Manager, token string) {
	// Build voice buttons from first episode's audios
	firstEp := season.Episodes[0]
	type voiceEntry struct {
		id    int
		codec string
		name  string
	}
	var voices []voiceEntry
	seen := make(map[string]bool)

	for _, a := range firstEp.Audios {
		voice := ""
		var id *int
		if a.Author != nil {
			id = a.Author.ID
			voice = a.Author.Title
		}
		if voice == "" && a.Type != nil {
			id = a.Type.ID
			voice = a.Type.Title
		}

		idt := 0
		if id != nil {
			idt = *id
		} else if a.Lang == "eng" {
			idt = 6
			voice = "Оригинал"
		} else {
			idt = 1
			voice = "По умолчанию"
		}
		if voice == "" {
			continue
		}

		// Default voice selection
		if t <= 0 {
			t = idt
			codec = a.Codec
		}

		key := fmt.Sprintf("%s:%s", voice, a.Codec)
		if !seen[key] {
			seen[key] = true
			voices = append(voices, voiceEntry{id: idt, codec: a.Codec, name: fmt.Sprintf("%s (%s)", voice, a.Codec)})
		}
	}

	// Voice buttons data
	voiceData := make([]map[string]any, 0, len(voices))
	for _, v := range voices {
		link := fmt.Sprintf("%s/lite/kinopub?postid=%d&title=%s&original_title=%s&s=%d&t=%d&codec=%s",
			host, postid, encTitle, encOriginal, s, v.id, url.QueryEscape(v.codec))
		active := v.id == t && (codec == "" || codec == v.codec)
		voiceData = append(voiceData, map[string]any{
			"method": "link",
			"url":    link,
			"name":   v.name,
			"active": active,
		})
	}

	// Episodes
	eData := make([]map[string]any, 0, len(season.Episodes))
	eLabels := make([]string, 0, len(season.Episodes))

	for _, ep := range season.Episodes {
		// Find voice index for this episode
		voiceIndex := -1
		if t == 1 {
			voiceIndex = t
		} else {
			for _, a := range ep.Audios {
				idt := 0
				if a.Author != nil && a.Author.ID != nil {
					idt = *a.Author.ID
				} else if a.Type != nil && a.Type.ID != nil {
					idt = *a.Type.ID
				}
				if (idt != 0 && t == idt && (codec == "" || codec == a.Codec)) || (t == 6 && a.Lang == "eng") {
					voiceIndex = a.Index
					break
				}
			}
			if voiceIndex == -1 {
				break
			}
		}

		qualMap := make(map[string]string, len(ep.Files))
		var bestURL string
		for _, f := range ep.Files {
			hlsURL := f.URL.HLS
			if hlsURL == "" {
				continue
			}
			hlsURL = strings.Replace(hlsURL, "a1.m3u8", fmt.Sprintf("a%d.m3u8", voiceIndex), 1)
			proxied := streamProxyURL(req, hlsURL, "kinopub", proxyLinks)
			qualMap[f.Quality] = proxied
			if bestURL == "" {
				bestURL = proxied
			}
		}
		if bestURL == "" {
			continue
		}

		subs := k.buildSubtitles(req, ep.Subtitles, proxyLinks)
		var subsCall string
		if len(subs) == 0 {
			subsCall = fmt.Sprintf("%s/lite/kinopub/subtitles.json?mid=%d", hostFromRequest(req), ep.ID)
		}

		name := fmt.Sprintf("%d серия", ep.Number)

		row := map[string]any{
			"method": "play",
			"url":    bestURL,
			"stream": bestURL,
			"name":   name,
			"title":  fmt.Sprintf("%s (%s)", baseTitle, name),
			"s":      s,
			"e":      ep.Number,
		}
		if len(qualMap) > 0 {
			row["quality"] = qualMap
			row["qualitys"] = qualMap
		}
		if len(subs) > 0 {
			row["subtitles"] = subs
		}
		if subsCall != "" {
			row["subtitles_call"] = subsCall
		}
		k.kpAttachTimelineEpisode(req, row, ep.Watching, postid, int(ep.ID), s, ep.Number)

		eData = append(eData, row)
		eLabels = append(eLabels, name)
	}

	if len(eData) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type":    "episode",
			"data":    eData,
			"buttons": voiceData,
		})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	// Voice buttons
	for _, v := range voiceData {
		getsTVAppendVoiceHTML(&sb, v)
	}
	// Episodes
	for i := range eData {
		se, _ := eData[i]["s"].(int)
		ep, _ := eData[i]["e"].(int)
		getsTVAppendMovieHTML(&sb, eData[i], eLabels[i], i == 0, se, ep)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (k *kinoPubChecker) renderEpisodesHTTP(w http.ResponseWriter, req *http.Request, rjson bool, baseTitle string, postid, s int, season *kpSeason, proxyLinks *proxylink.Manager, token string) {
	host := hostFromRequest(req)

	eData := make([]map[string]any, 0, len(season.Episodes))
	eLabels := make([]string, 0, len(season.Episodes))

	for _, ep := range season.Episodes {
		// Build voice name
		var voiceName string
		if ep.Audios != nil {
			var parts []string
			for _, a := range ep.Audios {
				name := ""
				if a.Author != nil {
					name = a.Author.Title
				}
				if name == "" {
					name = a.Lang
				}
				if name != "" && name != "rus" && !containsStr(parts, name) {
					parts = append(parts, name)
				}
			}
			voiceName = strings.Join(parts, ", ")
		}

		if len(ep.Files) == 0 {
			continue
		}

		qualMap := make(map[string]string, len(ep.Files))
		var bestURL string
		for _, f := range ep.Files {
			var rawURL string
			if k.filetype == "hls4" {
				rawURL = f.URL.HLS4
			} else {
				rawURL = f.URL.HTTP
			}
			if rawURL == "" {
				continue
			}
			proxied := streamProxyURL(req, rawURL, "kinopub", proxyLinks)
			qualMap[normalizeQualityLabel(f.Quality)] = proxied
			if bestURL == "" {
				bestURL = proxied
			}
		}
		if bestURL == "" {
			continue
		}

		subs := k.buildSubtitles(req, ep.Subtitles, proxyLinks)
		var subsCall string
		if len(subs) == 0 {
			subsCall = fmt.Sprintf("%s/lite/kinopub/subtitles.json?mid=%d", host, ep.ID)
		}

		name := fmt.Sprintf("%d серия", ep.Number)
		row := map[string]any{
			"method":     "play",
			"url":        bestURL,
			"stream":     bestURL,
			"name":       name,
			"voice_name": voiceName,
			"title":      fmt.Sprintf("%s (%s)", baseTitle, name),
			"s":          s,
			"e":          ep.Number,
		}
		if len(qualMap) > 0 {
			row["quality"] = qualMap
			row["qualitys"] = qualMap
		}
		if len(subs) > 0 {
			row["subtitles"] = subs
		}
		if subsCall != "" {
			row["subtitles_call"] = subsCall
		}
		k.kpAttachTimelineEpisode(req, row, ep.Watching, postid, int(ep.ID), s, ep.Number)

		eData = append(eData, row)
		eLabels = append(eLabels, name)
	}

	if len(eData) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "episode",
			"data": eData,
		})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i := range eData {
		se, _ := eData[i]["s"].(int)
		ep, _ := eData[i]["e"].(int)
		getsTVAppendMovieHTML(&sb, eData[i], eLabels[i], i == 0, se, ep)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// ---------------------------------------------------------------------------
// Subtitles endpoint
// ---------------------------------------------------------------------------

func (k *kinoPubChecker) subtitles(w http.ResponseWriter, req *http.Request, proxyLinks *proxylink.Manager) {
	mid, _ := strconv.ParseInt(strings.TrimSpace(req.URL.Query().Get("mid")), 10, 64)
	if mid == 0 {
		writeJSON(w, http.StatusOK, []any{})
		return
	}

	token := k.getToken(req.Context())
	if token == "" {
		writeJSON(w, http.StatusOK, []any{})
		return
	}

	target := fmt.Sprintf("%s/v1/items/media-links?mid=%d&access_token=%s", k.apiHost(), mid, token)
	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, target, nil)
	if err != nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	k.setHeaders(httpReq)

	resp, err := k.client.Do(httpReq)
	if err != nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeJSON(w, http.StatusOK, []any{})
		return
	}

	var result struct {
		Subtitles []kpSubtitle `json:"subtitles"`
	}
	if err := stdjson.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil || len(result.Subtitles) == 0 {
		writeJSON(w, http.StatusOK, []any{})
		return
	}

	subs := k.buildSubtitles(req, result.Subtitles, proxyLinks)
	writeJSON(w, http.StatusOK, subs)
}

// ---------------------------------------------------------------------------
// Watching state ("Продолжить просмотр")
// ---------------------------------------------------------------------------
//
// kino.pub tracks per-video playback position server-side. Two
// surfaces matter for us:
//   - GET  /v1/items/{id}                  → response already carries
//     `videos[].watching{time,status}` for movies and the same shape
//     under each episode for serials. No extra round-trip needed —
//     the existing item fetch surfaces this and the frontend can read
//     it from there.
//   - POST /v1/watching/marktime           → save current position
//     (we expose this via /lite/kinopub/watching/marktime). The Lampa
//     plugin POSTs on timeupdate.
//
// We expose only the write side; reads piggyback on the item fetch
// the player already issues at start.

// watchingMarktime proxies the player's current position into
// kino.pub's watching/marktime endpoint. Query params:
//
//	id        — postid (required)
//	video     — video file index within the item (default 1, movies)
//	time      — seconds, integer
//	season    — for serials (optional)
//	episode   — for serials (optional)
//
// Returns kino.pub's JSON verbatim. Empty token → 401 so the frontend
// can stop polling when the user signs out.
func (k *kinoPubChecker) watchingMarktime(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	id := strings.TrimSpace(q.Get("id"))
	if id == "" {
		id = strings.TrimSpace(q.Get("postid"))
	}
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "id required"})
		return
	}

	// Per-user resolution: kit-bound users save to their own kino.pub
	// account, single-user lampac saves to the global token, anyone
	// else (TG-authenticated but no kinopub bind) is silently no-op'd
	// so they don't pollute the shared account. Lampa's localStorage
	// timeline still works for them.
	token, mode := k.kinopubResolveMarktimeMode(req)
	switch mode {
	case kpMarktimeNoop:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "skipped": "no kinopub binding"})
		return
	case kpMarktimeGlobal:
		token = k.getToken(req.Context())
	}
	if token == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "no kinopub token configured"})
		return
	}

	// Build the upstream URL — kino.pub accepts the same params via
	// query string for GET-style writes. Android client uses POST.
	upstream := k.apiHost() + "/v1/watching/marktime"
	upQs := url.Values{}
	upQs.Set("access_token", token)
	upQs.Set("id", id)
	for _, p := range []string{"video", "time", "season", "episode", "status"} {
		if v := strings.TrimSpace(q.Get(p)); v != "" {
			upQs.Set(p, v)
		}
	}
	upstream += "?" + upQs.Encode()

	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodPost, upstream, nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "build request: " + err.Error()})
		return
	}
	k.setHeaders(httpReq)
	httpReq.Header.Set("Content-Length", "0")

	resp, err := k.client.Do(httpReq)
	if err != nil {
		log.Debug().Err(err).Str("id", id).Msg("kinopub: marktime request failed")
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "kinopub unreachable"})
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

// ---------------------------------------------------------------------------
// Device activation (kinopubpro)
// ---------------------------------------------------------------------------

func (k *kinoPubChecker) deviceActivation(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	code := strings.TrimSpace(q.Get("code"))
	name := strings.TrimSpace(q.Get("name"))

	if code == "" {
		// Step 1: request device code from kino.pub.
		userCode, codeOut, err := KinopubRequestDeviceCode(req.Context(), k.client, k.apiHost())
		if err != nil {
			writeHTML(w, http.StatusOK, fmt.Sprintf("ошибка запроса к %s: %v", k.apiHost(), err))
			return
		}
		html := `1. Откройте <a href="https://kino.watch/device">https://kino.watch/device</a> <br>`
		html += fmt.Sprintf(`2. Введите код активации <b>%s</b><br>`, userCode)
		html += `3. Когда на сайте kino.watch появится "Ожидание устройства", нажмите кнопку "Проверить активацию" которая ниже`
		html += fmt.Sprintf(`<br><br><a href="/lite/kinopubpro?code=%s&name=%s"><button>Проверить активацию</button></a>`, codeOut, url.QueryEscape(name))
		writeHTML(w, http.StatusOK, html)
		return
	}

	// Step 2: exchange device-code for an access/refresh pair.
	tokens, err := KinopubExchangeDeviceToken(req.Context(), k.client, k.apiHost(), code)
	if err != nil {
		writeHTML(w, http.StatusOK, fmt.Sprintf(`%v<br><br><a href="/lite/kinopubpro">Попробуйте ещё раз</a>`, err))
		return
	}

	// Notify device (sets the friendly name shown on kino.pub/devices).
	if name != "" {
		notifyURI := fmt.Sprintf("%s/v1/device/notify?access_token=%s", k.apiHost(), tokens.AccessToken)
		notifyReq, _ := http.NewRequestWithContext(req.Context(), http.MethodPost, notifyURI, strings.NewReader("&title="+url.QueryEscape(name)))
		if notifyReq != nil {
			notifyReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			k.setHeaders(notifyReq)
			if r, e := k.client.Do(notifyReq); e == nil {
				r.Body.Close()
			}
		}
	}

	// Persist the triple — from now on auto-refresh keeps it alive
	// across restarts and TTL boundaries. The user no longer has to
	// hand-edit config.toml.
	persistMsg := "✅ Токен сохранён автоматически (database/kinopub_token.json). Auto-refresh включён — повторная активация не понадобится."
	if k.tokenStore != nil {
		if err := k.tokenStore.Set(tokens); err != nil {
			log.Warn().Err(err).Msg("kinopub: failed to persist activated token")
			persistMsg = fmt.Sprintf("⚠️ Не удалось сохранить токен на диск: %v<br>Положите вручную:<br><br><code>[online.kinopub]<br>token = '%s'</code>", err, tokens.AccessToken)
		}
	}

	html := persistMsg
	if tokens.RefreshToken != "" {
		html += fmt.Sprintf("<br><br>access_token: <code>%s…</code>", safePrefix(tokens.AccessToken, 24))
		html += fmt.Sprintf("<br>refresh_token: <code>%s…</code>", safePrefix(tokens.RefreshToken, 24))
		if !tokens.ExpiresAt.IsZero() {
			html += fmt.Sprintf("<br>expires_at: %s", tokens.ExpiresAt.UTC().Format(time.RFC3339))
		}
	}
	writeHTML(w, http.StatusOK, html)
}

// safePrefix returns the first n characters of s without panicking on
// short strings — used to show a tokenheader teaser in the UI.
func safePrefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ---------------------------------------------------------------------------
// Similar results
// ---------------------------------------------------------------------------

func (k *kinoPubChecker) writeSimilar(w http.ResponseWriter, req *http.Request, rjson bool, items []kinoPubItem, title, originalTitle string) {
	host := hostFromRequest(req)
	encTitle := url.QueryEscape(title)
	encOriginal := url.QueryEscape(originalTitle)

	data := make([]map[string]any, 0, len(items))
	labels := make([]string, 0, len(items))

	for _, item := range items {
		link := fmt.Sprintf("%s/lite/kinopub?postid=%d&title=%s&original_title=%s",
			host, item.ID, encTitle, encOriginal)

		// Second poster if available
		img := ""
		for k, v := range item.Posters {
			if k != "small" && v != "" {
				img = v
				break
			}
		}

		row := map[string]any{
			"method":  "link",
			"url":     link,
			"similar": true,
			"year":    strconv.Itoa(item.Year),
			"details": item.Voice,
			"title":   item.Title,
			"img":     img,
		}
		data = append(data, row)
		labels = append(labels, item.Title)
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "similar",
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

// ---------------------------------------------------------------------------
// checkSearch (kept from original, uses searchAPI now)
// ---------------------------------------------------------------------------

func (k *kinoPubChecker) checkSearch(req *http.Request) bool {
	token := k.getToken(req.Context())
	if token == "" {
		return false
	}
	if k.rhub {
		// The search runs on the user's device at play time; no device is bound
		// to this server-side probe, so advertise availability and let the
		// play-path handshake do the real fetch.
		return true
	}

	q := req.URL.Query()
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	imdbID := strings.ToLower(strings.TrimSpace(q.Get("imdb_id")))
	kinopoiskID := strings.TrimSpace(q.Get("kinopoisk_id"))
	year, _ := strconv.Atoi(strings.TrimSpace(q.Get("year")))

	searches := make([]string, 0, 2)
	if originalTitle != "" {
		searches = append(searches, originalTitle)
	}
	if title != "" && normalizeSearchTitle(title) != normalizeSearchTitle(originalTitle) {
		searches = append(searches, title)
	}
	if len(searches) == 0 {
		return false
	}

	for _, query := range searches {
		items, ok := k.searchAPI(req, token, query)
		if !ok || len(items) == 0 {
			continue
		}

		wantTitle := normalizeSearchTitle(title)
		wantOriginal := normalizeSearchTitle(originalTitle)
		if wantTitle == "" {
			wantTitle = normalizeSearchTitle(query)
		}
		if wantOriginal == "" {
			wantOriginal = wantTitle
		}

		for _, item := range items {
			if strings.EqualFold(strings.TrimSpace(item.Type), "3d") {
				continue
			}

			if imdbID != "" && kpIMDBMatch(item.IMDB, imdbID) {
				return true
			}
			if kinopoiskID != "" && isInt64(kinopoiskID) {
				kp, _ := strconv.ParseInt(kinopoiskID, 10, 64)
				if item.Kinopoisk == kp {
					return true
				}
			}

			if year > 0 && item.Year > 0 && item.Year != year && item.Year != year-1 && item.Year != year+1 {
				continue
			}

			itemTitle := normalizeSearchTitle(item.Title)
			if itemTitle == "" {
				continue
			}
			if strings.Contains(itemTitle, wantTitle) || strings.Contains(itemTitle, wantOriginal) {
				return true
			}
		}
	}

	return false
}

// Kept for backwards compatibility — delegates to searchAPI.
// Resolves the token via getToken so cached/refreshed credentials are
// used; falls back to k.token only when the store is empty (legacy).
func (k *kinoPubChecker) search(req *http.Request, query string) ([]kinoPubItem, bool) {
	return k.searchAPI(req, k.getToken(req.Context()), query)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// getToken returns the bearer token to attach to outbound kino.pub
// requests. Resolution order:
//  1. tokenStore (persistent device-flow result) — auto-refreshes if
//     the access token is within kinopubRefreshLeeway of expiry.
//  2. round-robin over cfg.Online.KinoPub.tokens when multiple are
//     supplied (legacy admin pool for sharing premium across users).
//  3. cfg.Online.KinoPub.token (single legacy token).
//
// A returned empty string means "no usable token at all" — callers
// must surface this via writeGetsTVEmpty so the player shows the
// "premium required" hint instead of a confusing API error.
// kino.pub caps SIMULTANEOUS playback: past the cap the CDN answers a stream
// open with 403 + body "user session limit reached" (verified live 2026-07-29 —
// the edge only relays it, the verdict comes from kino.pub's stateful backend).
// One token serving every viewer of a shared server saturates that cap, which
// reads as "kinopub works, then doesn't". The vendor FAQ puts the budget at 5
// devices watching at once out of up to 10 bound per account, so spreading
// viewers over more tokens is the server-side mitigation — and an explicitly
// configured `tokens` list must therefore actually rotate. See KinoPubSource.Tokens
// for what same-account vs different-account tokens do and do not buy.
//
// The device-flow store holds ONE account's token, so it may not shadow that
// list: it joins the pool as one more entry (and is the sole source when no
// tokens are configured, which is the single-account default).
func (k *kinoPubChecker) getToken(ctx context.Context) string {
	var stored string
	if k.tokenStore != nil {
		if tok, err := k.tokenStore.Get(ctx, k.client, k.apiHost()); err == nil && tok != "" {
			stored = tok
		} else if err != nil {
			log.Debug().Err(err).Msg("kinopub: tokenStore.Get failed; falling back to config token")
		}
	}

	pool := make([]string, 0, len(k.tokens)+1)
	seen := make(map[string]struct{}, len(k.tokens)+1)
	for _, tok := range append([]string{stored}, k.tokens...) {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		if _, dup := seen[tok]; dup {
			continue
		}
		seen[tok] = struct{}{}
		pool = append(pool, tok)
	}

	switch len(pool) {
	case 0:
		return k.token
	case 1:
		return pool[0]
	}
	return pool[int(kinopubTokenIdx.Add(1))%len(pool)]
}

// kinopubTokenIdx round-robins the token pool. Round-robin (not random) so N
// concurrent viewers land on N different accounts instead of colliding by luck.
var kinopubTokenIdx atomic.Int32

// kpIMDBMatch compares a kinopub IMDB field (can be number or string) with a
// Lampa imdb_id like "tt133093".
func kpIMDBMatch(imdbAny any, wantID string) bool {
	var s string
	switch v := imdbAny.(type) {
	case string:
		s = v
	case float64:
		s = strconv.FormatInt(int64(v), 10)
	case int:
		s = strconv.Itoa(v)
	default:
		return false
	}
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return false
	}
	if !strings.HasPrefix(s, "tt") {
		s = "tt" + s
	}
	return strings.EqualFold(s, wantID)
}

// kinopubHeaderMap is the single source of truth for request headers, shared by
// the direct path (setHeaders) and the rch path (rchFetch in kinopubAPIGet).
func kinopubHeaderMap() map[string]string {
	return map[string]string{
		"Accept":      "application/json,text/plain,*/*",
		"User-Agent":  "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36",
		"X-Lampac-Go": "1",
	}
}

func (k *kinoPubChecker) setHeaders(req *http.Request) {
	for hk, hv := range kinopubHeaderMap() {
		req.Header.Set(hk, hv)
	}
}

func (k *kinoPubChecker) buildSubtitles(req *http.Request, subs []kpSubtitle, proxyLinks *proxylink.Manager) []map[string]any {
	if len(subs) == 0 {
		return nil
	}
	result := make([]map[string]any, 0, len(subs))
	for _, sub := range subs {
		if sub.URL == "" {
			continue
		}
		proxied := streamProxyURL(req, sub.URL, "kinopub", proxyLinks)
		result = append(result, map[string]any{
			"label": sub.Lang,
			"url":   proxied,
		})
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func kpAudioVoice(a kpAudio) string {
	voice := ""
	if a.Type != nil && a.Type.Title != "" {
		voice = a.Type.Title
	}
	if voice == "" {
		voice = a.Lang
	}
	if voice == "" {
		voice = "оригинал"
	}
	if a.Author != nil && a.Author.Title != "" {
		voice += " (" + a.Author.Title + ")"
	}
	return voice
}

func containsStr(slice []string, s string) bool {
	return slices.Contains(slice, s)
}

// kpMarktimeMode describes how kinopubResolveMarktimeMode classified
// the current request — used by both the timeline emitter (to decide
// whether to advertise a server callback at all) and the marktime
// handler (to decide what to do with the POST).
type kpMarktimeMode int

const (
	// kpMarktimeNoop: kit context is present but the user has NOT
	// kit-bound their own kino.pub account. We must not write to the
	// shared global account on their behalf — that would smash
	// everyone else's resume points. The frontend keeps using
	// Lampa.Timeline's localStorage, which is per-device anyway.
	kpMarktimeNoop kpMarktimeMode = iota
	// kpMarktimeKit: user has their own kino.pub token via Kit. Saves
	// land in their account; positions sync across all their devices.
	kpMarktimeKit
	// kpMarktimeGlobal: no Kit context at all (single-user lampac
	// deploy with TG auth disabled). Use the global token — that IS
	// the only user.
	kpMarktimeGlobal
)

// kinopubResolveMarktimeMode returns the marktime mode for the current
// request. The token returned is empty unless mode == kit (callers
// substitute the global token for kpMarktimeGlobal themselves).
func (k *kinoPubChecker) kinopubResolveMarktimeMode(req *http.Request) (token string, mode kpMarktimeMode) {
	ctx := req.Context()
	if tok, ok := kit.TokenOverride(ctx, "KinoPub"); ok {
		return tok, kpMarktimeKit
	}
	// Kit context present (TG-authenticated user) but no KinoPub
	// binding → don't pollute shared state.
	if _, hasKit := kit.FromContext(ctx); hasKit {
		return "", kpMarktimeNoop
	}
	return "", kpMarktimeGlobal
}

// kpAttachTimeline writes a Lampa-friendly "timeline" envelope into
// `row` so the frontend kinopub plugin can:
//
//   - seek to `time` when the user hits Play (resume).
//   - POST progress updates to `callback` on timeupdate.
//
// Per-user semantics: the server `callback` is only emitted when this
// particular request has a way to save state under the right account
// (kit-bound user, or single-user-mode global). Otherwise we omit
// `callback` — Lampa falls back to its own localStorage timeline,
// keeping each device's resume point local and avoiding cross-user
// collisions on the shared global token.
//
// `hash` is always emitted so Lampa's localStorage layer still keys
// progress by (postid, video) for that device.
//
// Pass watching == nil safely — the function no-ops on time.
func (k *kinoPubChecker) kpAttachTimeline(req *http.Request, row map[string]any, watching *kpWatching, postid, videoID int) {
	if row == nil {
		return
	}
	tl := map[string]any{
		"hash": fmt.Sprintf("kp_%d_%d", postid, videoID),
	}
	if watching != nil && watching.Time > 0 {
		tl["time"] = watching.Time
	}
	if _, mode := k.kinopubResolveMarktimeMode(req); mode != kpMarktimeNoop {
		cb := fmt.Sprintf("/lite/kinopub/watching/marktime?id=%d&video=%d", postid, videoID)
		tl["callback"] = cb
		row["callback"] = cb
	}
	row["timeline"] = tl
}

// kpAttachTimelineEpisode is the serial-flavoured variant: includes
// season + episode in both the hash (so different episodes don't
// collide) and the callback URL. Same per-user gating as the movie
// variant.
func (k *kinoPubChecker) kpAttachTimelineEpisode(req *http.Request, row map[string]any, watching *kpWatching, postid, videoID, season, episode int) {
	if row == nil {
		return
	}
	tl := map[string]any{
		"hash": fmt.Sprintf("kp_%d_s%d_e%d", postid, season, episode),
	}
	if watching != nil && watching.Time > 0 {
		tl["time"] = watching.Time
	}
	if _, mode := k.kinopubResolveMarktimeMode(req); mode != kpMarktimeNoop {
		cb := fmt.Sprintf("/lite/kinopub/watching/marktime?id=%d&video=%d&season=%d&episode=%d",
			postid, videoID, season, episode)
		tl["callback"] = cb
		row["callback"] = cb
	}
	row["timeline"] = tl
}
