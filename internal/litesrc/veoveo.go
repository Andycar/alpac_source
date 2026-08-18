package litesrc

import (
	"compress/gzip"
	"context"
	stdjson "encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"

	"lampac-go/internal/proxylink"

	"github.com/rs/zerolog/log"
)

type veoVeoChecker struct {
	client   *http.Client
	host     string
	token    string
	dataPath string

	loadOnce sync.Once
	movies   []veoVeoMovie

	idCache      sync.Map // kp string -> veoVeoIDCacheEntry (online kp->movieID resolution)
	qualityCache sync.Map // movieID int64 -> string badge (parsed from master playlist)
}

// veoVeoIDCacheEntry caches the result of an online kp->movieID resolution.
// Positive entries (id>0) never expire — the mapping is stable. Negative
// entries (id==0) are re-checked after veoVeoNegCacheTTL so transient failures
// or newly-added titles get another chance.
type veoVeoIDCacheEntry struct {
	id int64
	at time.Time
}

const veoVeoNegCacheTTL = 10 * time.Minute

// veoVeoMaxQualityProbes caps how many variant master playlists are fetched when
// determining the quality badge, bounding the work for titles with many dubs.
const veoVeoMaxQualityProbes = 6

// veoVeoMovieIDRe extracts the internal movieID from the iframe bootstrap
// script, e.g. `window.MOVIE_ID=162545;`.
var veoVeoMovieIDRe = regexp.MustCompile(`window\.MOVIE_ID\s*=\s*(\d+)`)

// veoVeoResolutionRe captures the vertical resolution of each variant stream in
// an HLS master playlist, e.g. `RESOLUTION=1280x720`.
var veoVeoResolutionRe = regexp.MustCompile(`RESOLUTION=\d+x(\d+)`)

// veoVeoBadgeFromMaster maps the highest vertical resolution advertised in an
// HLS master playlist to a quality badge (4K/FHD/HD/SD), or "" if none found.
func veoVeoBadgeFromMaster(master string) string {
	best := ""
	for _, m := range veoVeoResolutionRe.FindAllStringSubmatch(master, -1) {
		badge := normalizeQualityBadge(m[1])
		if qualityBadgeRank(badge) > qualityBadgeRank(best) {
			best = badge
		}
	}
	return best
}

type veoVeoMovie struct {
	ID            int64  `json:"id"`
	Year          int    `json:"year"`
	KinopoiskID   *int64 `json:"kinopoiskId"`
	IMDBID        string `json:"imdbId"`
	OriginalTitle string `json:"originalTitle"`
	Title         string `json:"title"`
}

// veoVeoEpisode represents a single episode from the VeoVeo API.
type veoVeoEpisode struct {
	Season          veoVeoSeason    `json:"season"`
	Order           int             `json:"order"`
	Title           string          `json:"title"`
	EpisodeVariants []veoVeoVariant `json:"episodeVariants"`
}

type veoVeoSeason struct {
	Order int `json:"order"`
}

type veoVeoVariant struct {
	Filepath string `json:"filepath"`
	Title    string `json:"title"` // dub / studio name, e.g. "hdrezka studio"
}

func NewVeoVeoChecker(cfg config.Config) *veoVeoChecker {
	host := strings.TrimSpace(cfg.Online.VeoVeo.Host)
	if host == "" {
		host = "https://global.temptcdn.com"
	}
	host = strings.TrimRight(host, "/")

	token := strings.TrimSpace(cfg.Online.VeoVeo.Token)
	if token == "" {
		token = config.DefaultVeoVeoToken
	}

	dataPath := strings.TrimSpace(cfg.Online.VeoVeo.DataPath)
	if dataPath == "" {
		dataPath = filepath.Join(cfg.Compat.RepoRoot, "data", "veoveo.json")
	} else if !filepath.IsAbs(dataPath) {
		dataPath = filepath.Join(cfg.Compat.RepoRoot, dataPath)
	}

	return &veoVeoChecker{
		client:   httpclient.NewForBalancer("veoveo", 10*time.Second),
		host:     host,
		token:    token,
		dataPath: dataPath,
	}
}

func (v *veoVeoChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show, badge := v.checkSearch(req)
			if badge == "" {
				badge = pluginQualityBadgeGet("veoveo")
			}
			if badge == "" {
				badge = "FHD"
			}
			writeCheckSearchResponse(w, show, badge)
			return
		}
		v.index(w, req, links)
	}
}

func (v *veoVeoChecker) index(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))

	movieID := v.lookupMovieID(req.Context(), q)
	if movieID == 0 {
		log.Debug().Str("imdb", q.Get("imdb_id")).Str("kp", q.Get("kinopoisk_id")).Str("title", title).Int("db_size", len(v.loadMovies())).Msg("veoveo: movie not resolved (local DB + iframe)")
		writeGetsTVEmpty(w, rjson)
		return
	}

	log.Debug().Int64("movieID", movieID).Msg("veoveo: found movie, fetching episodes")
	episodes, ok := v.fetchEpisodes(req.Context(), movieID)
	if !ok || len(episodes) == 0 {
		log.Debug().Int64("movieID", movieID).Bool("ok", ok).Msg("veoveo: no episodes from API")
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Movie: season.order == 0
	if episodes[0].Season.Order == 0 {
		v.writeMovie(w, req, rjson, title, originalTitle, episodes, links)
		return
	}

	// Serial
	s, sSet := getsTVQueryInt(q.Get("s"))
	if !sSet {
		s = -1
	}
	t, tSet := getsTVQueryInt(q.Get("t"))
	if !tSet {
		t = -1
	}
	v.writeSerial(w, req, rjson, movieID, title, originalTitle, episodes, s, t, links)
}

func (v *veoVeoChecker) fetchEpisodes(ctx context.Context, movieID int64) ([]veoVeoEpisode, bool) {
	target := v.host + "/balancer-api/proxy/playlists/catalog-api/episodes?content-id=" + strconv.FormatInt(movieID, 10)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, false
	}
	httpReq.Header.Set("Accept", "application/json,text/plain,*/*")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	httpReq.Header.Set("X-Lampac-Go", "1")

	resp, err := v.client.Do(httpReq)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false
	}

	var episodes []veoVeoEpisode
	if err := stdjson.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&episodes); err != nil {
		return nil, false
	}
	return episodes, len(episodes) > 0
}

// bestFilepath picks the best stream URL from episode variants, preferring .m3u8.
func veoVeoBestFilepath(variants []veoVeoVariant) string {
	if len(variants) == 0 {
		return ""
	}
	// Prefer m3u8 (HLS)
	for _, v := range variants {
		if strings.Contains(v.Filepath, ".m3u8") {
			return strings.TrimSpace(v.Filepath)
		}
	}
	// Fallback to first non-empty
	for _, v := range variants {
		if fp := strings.TrimSpace(v.Filepath); fp != "" {
			return fp
		}
	}
	return ""
}

// veoVeoVoiceNames returns the distinct dub/studio names across the given
// episodes' variants, in first-seen order. Empty titles are ignored — when
// fewer than two named voices exist there is nothing to choose between, so the
// caller falls back to the single best stream.
func veoVeoVoiceNames(episodes []veoVeoEpisode) []string {
	seen := make(map[string]struct{}, 4)
	names := make([]string, 0, 4)
	for _, ep := range episodes {
		for _, variant := range ep.EpisodeVariants {
			name := strings.TrimSpace(variant.Title)
			if name == "" {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			names = append(names, name)
		}
	}
	return names
}

// veoVeoVariantFile returns the stream URL for the variant whose dub name equals
// voice (preferring .m3u8), or "" if that voice is absent from these variants.
func veoVeoVariantFile(variants []veoVeoVariant, voice string) string {
	matched := make([]veoVeoVariant, 0, 2)
	for _, variant := range variants {
		if strings.TrimSpace(variant.Title) == voice {
			matched = append(matched, variant)
		}
	}
	return veoVeoBestFilepath(matched)
}

func (v *veoVeoChecker) writeMovie(
	w http.ResponseWriter,
	req *http.Request,
	rjson bool,
	title, originalTitle string,
	episodes []veoVeoEpisode,
	links *proxylink.Manager,
) {
	baseTitle := getsTVJoinName(title, originalTitle)

	// One play row per dub variant; the variant's own title is the voice label.
	rows := make([]map[string]any, 0, len(episodes[0].EpisodeVariants))
	for _, variant := range episodes[0].EpisodeVariants {
		file := strings.TrimSpace(variant.Filepath)
		if file == "" {
			continue
		}
		stream := streamProxyURL(req, file, "veoveo", links)
		name := strings.TrimSpace(variant.Title)
		if name == "" {
			name = "1080p"
		}
		rows = append(rows, map[string]any{
			"method": "play",
			"url":    stream,
			"stream": stream,
			"name":   name,
			"title":  baseTitle,
		})
	}

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
	for i, row := range rows {
		getsTVAppendMovieHTML(&sb, row, toString(row["name"]), i == 0, 0, 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (v *veoVeoChecker) writeSerial(
	w http.ResponseWriter,
	req *http.Request,
	rjson bool,
	movieID int64,
	title, originalTitle string,
	episodes []veoVeoEpisode,
	s int,
	t int,
	links *proxylink.Manager,
) {
	host := hostFromRequest(req)
	encTitle := url.QueryEscape(title)
	encOriginal := url.QueryEscape(originalTitle)

	if s == -1 {
		// Season list
		seen := map[int]struct{}{}
		seasons := make([]int, 0, 8)
		for _, ep := range episodes {
			sn := ep.Season.Order
			if _, ok := seen[sn]; ok {
				continue
			}
			seen[sn] = struct{}{}
			seasons = append(seasons, sn)
		}
		sort.Ints(seasons)

		if len(seasons) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}

		data := make([]map[string]any, 0, len(seasons))
		labels := make([]string, 0, len(seasons))
		for _, sn := range seasons {
			link := fmt.Sprintf(
				"%s/lite/veoveo?rjson=%s&movieid=%d&title=%s&original_title=%s&s=%d",
				host, getsTVBool(rjson), movieID, encTitle, encOriginal, sn,
			)
			label := fmt.Sprintf("%d сезон", sn)
			data = append(data, map[string]any{
				"method": "link",
				"id":     sn,
				"url":    link,
				"name":   label,
			})
			labels = append(labels, label)
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
			getsTVAppendSeasonHTML(&sb, row, labels[i], i == 0)
		}
		sb.WriteString(`</div>`)
		writeHTML(w, http.StatusOK, sb.String())
		return
	}

	// Episode list for season s.
	filtered := make([]veoVeoEpisode, 0, len(episodes))
	for _, ep := range episodes {
		if ep.Season.Order == s {
			filtered = append(filtered, ep)
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Order < filtered[j].Order
	})

	if len(filtered) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	baseTitle := getsTVJoinName(title, originalTitle)

	// Voice selection: a "voice" is a distinct dub name across this season's
	// variants. With two or more, render a voice picker and bind episodes to the
	// active dub (t); with fewer, fall back to the single best stream per episode.
	voices := veoVeoVoiceNames(filtered)
	multiVoice := len(voices) >= 2

	var voiceRows []map[string]any
	activeVoice := ""
	if multiVoice {
		if t < 0 || t >= len(voices) {
			t = 0
		}
		activeVoice = voices[t]
		voiceRows = make([]map[string]any, 0, len(voices))
		for i, name := range voices {
			link := fmt.Sprintf(
				"%s/lite/veoveo?rjson=%s&movieid=%d&title=%s&original_title=%s&s=%d&t=%d",
				host, getsTVBool(rjson), movieID, encTitle, encOriginal, s, i,
			)
			voiceRows = append(voiceRows, map[string]any{
				"method": "link",
				"name":   name,
				"active": i == t,
				"url":    link,
			})
		}
	}

	data := make([]map[string]any, 0, len(filtered))
	labels := make([]string, 0, len(filtered))
	seasons := make([]int, 0, len(filtered))
	episodesN := make([]int, 0, len(filtered))

	for _, ep := range filtered {
		var file string
		if multiVoice {
			file = veoVeoVariantFile(ep.EpisodeVariants, activeVoice)
		} else {
			file = veoVeoBestFilepath(ep.EpisodeVariants)
		}
		if file == "" {
			continue
		}

		stream := streamProxyURL(req, file, "veoveo", links)
		name := strings.TrimSpace(ep.Title)
		if name == "" {
			name = fmt.Sprintf("%d серия", ep.Order)
		}

		row := map[string]any{
			"method": "play",
			"url":    stream,
			"stream": stream,
			"s":      s,
			"e":      ep.Order,
			"name":   name,
			"title":  fmt.Sprintf("%s (%s)", baseTitle, name),
		}
		if multiVoice {
			row["voice_name"] = activeVoice
		}

		data = append(data, row)
		labels = append(labels, name)
		seasons = append(seasons, s)
		episodesN = append(episodesN, ep.Order)
	}

	if len(data) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		payload := map[string]any{
			"type": "episode",
			"data": data,
		}
		if len(voiceRows) > 0 {
			payload["voice"] = voiceRows
		}
		writeJSON(w, http.StatusOK, payload)
		return
	}

	var sb strings.Builder
	if len(voiceRows) > 0 {
		sb.WriteString(`<div class="videos__line">`)
		for _, row := range voiceRows {
			getsTVAppendVoiceHTML(&sb, row)
		}
		sb.WriteString(`</div>`)
	}
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range data {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, seasons[i], episodesN[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// checkSearch reports whether veoveo has this title and, when it does, the
// quality badge derived from the actual stream (parsed from the HLS master
// playlist) rather than a static guess.
func (v *veoVeoChecker) checkSearch(req *http.Request) (bool, string) {
	movieID := v.lookupMovieID(req.Context(), req.URL.Query())
	if movieID == 0 {
		return false, ""
	}
	episodes, ok := v.fetchEpisodes(req.Context(), movieID)
	if !ok || len(episodes) == 0 {
		return false, ""
	}
	return true, v.detectQualityBadge(req.Context(), movieID, episodes)
}

// detectQualityBadge returns the quality badge for a title: the highest
// resolution across the first episode's variant streams (dubs can be encoded at
// different qualities, so probe each and keep the best). Cached per movieID;
// best-effort — returns "" on any failure so the caller can fall back.
func (v *veoVeoChecker) detectQualityBadge(ctx context.Context, movieID int64, episodes []veoVeoEpisode) string {
	if ent, ok := v.qualityCache.Load(movieID); ok {
		return ent.(string)
	}

	// Distinct HLS stream URLs across the first episode's variants, capped.
	seen := make(map[string]struct{})
	files := make([]string, 0, len(episodes[0].EpisodeVariants))
	for _, variant := range episodes[0].EpisodeVariants {
		f := strings.TrimSpace(variant.Filepath)
		if f == "" || !strings.Contains(f, ".m3u8") {
			continue
		}
		if _, ok := seen[f]; ok {
			continue
		}
		seen[f] = struct{}{}
		files = append(files, f)
		if len(files) >= veoVeoMaxQualityProbes {
			break
		}
	}
	if len(files) == 0 {
		return ""
	}

	// Bound the extra probes so they never inflate checksearch latency much.
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	badges := make([]string, len(files))
	var wg sync.WaitGroup
	for i, f := range files {
		wg.Add(1)
		go func(i int, f string) {
			defer wg.Done()
			badges[i] = veoVeoBadgeFromMaster(v.fetchMaster(qctx, f))
		}(i, f)
	}
	wg.Wait()

	best := ""
	for _, b := range badges {
		if qualityBadgeRank(b) > qualityBadgeRank(best) {
			best = b
		}
	}
	if best != "" {
		v.qualityCache.Store(movieID, best)
	}
	return best
}

// fetchMaster GETs an HLS master playlist, manually following redirects because
// the balancer client is configured no-redirect (the content-router URL 307s to
// a rotating mvapspdmpg.com edge that actually serves the playlist body).
func (v *veoVeoChecker) fetchMaster(ctx context.Context, rawURL string) string {
	cur := rawURL
	for i := 0; i < 4; i++ {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, cur, nil)
		if err != nil {
			return ""
		}
		httpReq.Header.Set("Accept", "application/vnd.apple.mpegurl,application/x-mpegURL,*/*")
		httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")

		resp, err := v.client.Do(httpReq)
		if err != nil {
			return ""
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			loc := strings.TrimSpace(resp.Header.Get("Location"))
			resp.Body.Close()
			base, perr := url.Parse(cur)
			ref, rerr := url.Parse(loc)
			if loc == "" || perr != nil || rerr != nil {
				return ""
			}
			cur = base.ResolveReference(ref).String()
			continue
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		if rerr != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return ""
		}
		return string(body)
	}
	return ""
}

// lookupMovieID resolves the internal veoveo movieID from request params.
// Resolution order: explicit movieid param → local catalog DB (data/veoveo.json,
// keyed by kp/imdb/title) → online iframe bootstrap (kp only). The online step
// makes the balancer self-sufficient without a pre-scraped local DB.
func (v *veoVeoChecker) lookupMovieID(ctx context.Context, q url.Values) int64 {
	if raw := strings.TrimSpace(q.Get("movieid")); raw != "" && isInt64(raw) {
		id, _ := strconv.ParseInt(raw, 10, 64)
		return id
	}

	kp := strings.TrimSpace(q.Get("kinopoisk_id"))
	if id := v.findMovieID(
		strings.TrimSpace(q.Get("imdb_id")),
		kp,
		strings.TrimSpace(q.Get("title")),
		strings.TrimSpace(q.Get("original_title")),
	); id != 0 {
		return id
	}

	// Fall back to live kp->movieID resolution via the iframe bootstrap.
	if kp != "" && isInt64(kp) {
		return v.resolveMovieIDOnline(ctx, kp)
	}
	return 0
}

// resolveMovieIDOnline resolves a Kinopoisk ID to the internal veoveo movieID
// by fetching the iframe player and reading window.MOVIE_ID. Results are cached
// (positive forever, negative for veoVeoNegCacheTTL).
func (v *veoVeoChecker) resolveMovieIDOnline(ctx context.Context, kp string) int64 {
	if v.token == "" {
		return 0
	}
	if ent, ok := v.idCache.Load(kp); ok {
		e := ent.(veoVeoIDCacheEntry)
		if e.id > 0 || time.Since(e.at) < veoVeoNegCacheTTL {
			return e.id
		}
	}

	id := v.fetchMovieIDFromIframe(ctx, kp)
	v.idCache.Store(kp, veoVeoIDCacheEntry{id: id, at: time.Now()})
	if id > 0 {
		log.Debug().Str("kp", kp).Int64("movieID", id).Msg("veoveo: resolved kp via iframe")
	}
	return id
}

func (v *veoVeoChecker) fetchMovieIDFromIframe(ctx context.Context, kp string) int64 {
	target := v.host + "/balancer-api/iframe?kp=" + url.QueryEscape(kp) +
		"&lang_order=rus&token=" + url.QueryEscape(v.token)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0
	}
	httpReq.Header.Set("Accept", "text/html,application/xhtml+xml,*/*")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	httpReq.Header.Set("Referer", v.host+"/")

	resp, err := v.client.Do(httpReq)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if err != nil {
		return 0
	}
	m := veoVeoMovieIDRe.FindSubmatch(body)
	if m == nil {
		return 0
	}
	id, _ := strconv.ParseInt(string(m[1]), 10, 64)
	return id
}

func (v *veoVeoChecker) findMovieID(imdbID, kinopoiskID, title, originalTitle string) int64 {
	movies := v.loadMovies()
	if len(movies) == 0 {
		return 0
	}

	if kinopoiskID != "" && isInt64(kinopoiskID) {
		kp, _ := strconv.ParseInt(kinopoiskID, 10, 64)
		for _, item := range movies {
			if item.KinopoiskID != nil && *item.KinopoiskID == kp {
				return item.ID
			}
		}
	}

	if imdbID != "" {
		want := strings.ToLower(strings.TrimSpace(imdbID))
		for _, item := range movies {
			if strings.ToLower(strings.TrimSpace(item.IMDBID)) == want {
				return item.ID
			}
		}
	}

	wantOriginal := normalizeSearchTitle(originalTitle)
	if wantOriginal != "" {
		for _, item := range movies {
			if normalizeSearchTitle(item.OriginalTitle) == wantOriginal {
				return item.ID
			}
		}
	}

	wantTitle := normalizeSearchTitle(title)
	if wantTitle != "" {
		for _, item := range movies {
			if normalizeSearchTitle(item.Title) == wantTitle {
				return item.ID
			}
		}
	}

	return 0
}

func (v *veoVeoChecker) loadMovies() []veoVeoMovie {
	v.loadOnce.Do(func() {
		file, err := os.Open(v.dataPath)
		if err != nil {
			log.Warn().Err(err).Str("path", v.dataPath).Msg("veoveo: failed to open data file")
			return
		}
		defer file.Close()

		// Try gzip first, fall back to plain JSON.
		var data []byte
		if reader, err := gzip.NewReader(file); err == nil {
			data, _ = io.ReadAll(io.LimitReader(reader, 64<<20))
			reader.Close()
			log.Debug().Int("bytes", len(data)).Msg("veoveo: loaded as gzip")
		}
		if len(data) == 0 {
			// Rewind and read as plain JSON.
			file.Seek(0, io.SeekStart)
			data, err = io.ReadAll(io.LimitReader(file, 64<<20))
			if err != nil {
				log.Warn().Err(err).Msg("veoveo: failed to read data file")
				return
			}
			log.Debug().Int("bytes", len(data)).Msg("veoveo: loaded as plain JSON")
		}

		var items []veoVeoMovie
		if err := stdjson.Unmarshal(data, &items); err != nil {
			log.Warn().Err(err).Int("data_len", len(data)).Msg("veoveo: failed to parse JSON")
			return
		}
		log.Info().Int("movies", len(items)).Str("path", v.dataPath).Msg("veoveo: movie database loaded")
		v.movies = items
	})

	return v.movies
}
