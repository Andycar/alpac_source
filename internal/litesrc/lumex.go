package litesrc

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"io"
	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/proxylink"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

const lumexUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36"

type lumexChecker struct {
	client     *http.Client
	apiHost    string
	token      string
	clientID   string
	iframeHost string
}

func NewLumexChecker(cfg config.Config) *lumexChecker {
	apiHost := strings.TrimSpace(cfg.Online.Lumex.APIHost)
	if apiHost == "" {
		apiHost = "https://portal.lumex.host"
	}
	apiHost = strings.TrimRight(apiHost, "/")

	iframeHost := strings.TrimSpace(cfg.Online.Lumex.IframeHost)
	if iframeHost == "" {
		iframeHost = "lumex.space"
	}

	return &lumexChecker{
		client:     httpclient.NewForBalancer("lumex", 15*time.Second),
		apiHost:    apiHost,
		token:      strings.TrimSpace(cfg.Online.Lumex.Token),
		clientID:   strings.TrimSpace(cfg.Online.Lumex.ClientID),
		iframeHost: iframeHost,
	}
}

func (l *lumexChecker) Handle(cfg config.Config, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()

		if parseBoolParam(q.Get("checksearch")) {
			show := l.checkSearch(req.Context(), q)
			writeCheckSearchResponse(w, show, pluginQualityBadgeGet("lumex"))
			return
		}

		l.index(w, req, links)
	}
}

// ---------------------------------------------------------------------------
// Content API response types
// ---------------------------------------------------------------------------

// lumexContentResponse is the top-level /content response.
type lumexContentResponse struct {
	Player lumexPlayer `json:"player"`
	Meta   string      `json:"meta"`
}

type lumexPlayer struct {
	ContentType string             `json:"content_type"`
	ContentID   int64              `json:"content_id"`
	KinopoiskID int64              `json:"kinopoisk_id"`
	Poster      string             `json:"poster"`
	Media       stdjson.RawMessage `json:"media"`
}

// lumexVoice is a voice/translation entry (movie mode).
type lumexVoice struct {
	TranslationID   int          `json:"translation_id"`
	TranslationName string       `json:"translation_name"`
	MaxQuality      int          `json:"max_quality"`
	Playlist        string       `json:"playlist"`
	Tracks          []lumexTrack `json:"tracks"`
}

// lumexSeason is a season entry (tv-series mode).
type lumexSeason struct {
	SeasonID   int            `json:"season_id"`
	SeasonName string         `json:"season_name"`
	Episodes   []lumexEpisode `json:"episodes"`
}

// lumexEpisode is an episode entry within a season.
type lumexEpisode struct {
	EpisodeID int          `json:"episode_id"`
	Name      string       `json:"name"`
	Poster    string       `json:"poster"`
	Media     []lumexVoice `json:"media"`
}

// lumexTrack is a subtitle/audio track.
type lumexTrack struct {
	Label string `json:"label"`
	Src   string `json:"src"`
}

// lumexValidateResponse is the response from POST /validate/{encrypted}.
type lumexValidateResponse struct {
	URL string `json:"url"`
}

// lumexContentResult holds parsed content data + CSRF token for validate calls.
type lumexContentResult struct {
	Media       stdjson.RawMessage
	ContentType string
	CSRF        string // x-csrf-token cookie value (full, URL-encoded)
	CSRFHeader  string // x-csrf-token header value (before %7C)
}

// ---------------------------------------------------------------------------
// Main handler
// ---------------------------------------------------------------------------

func (l *lumexChecker) index(w http.ResponseWriter, req *http.Request, links *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	kpID := strings.TrimSpace(q.Get("kinopoisk_id"))
	s := -1
	if sv := strings.TrimSpace(q.Get("s")); sv != "" {
		if si, err := strconv.Atoi(sv); err == nil {
			s = si
		}
	}

	if l.clientID == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	// Fetch content via short→redirect flow (returns media + CSRF token).
	cr := l.fetchContent(req.Context(), kpID)
	if cr == nil {
		writeGetsTVEmpty(w, rjson)
		return
	}

	switch cr.ContentType {
	case "movie", "anime":
		l.writeMovie(w, req, rjson, title, originalTitle, cr, links)
	case "tv-series":
		l.writeSerial(w, req, rjson, title, originalTitle, kpID, s, cr, links)
	default:
		writeGetsTVEmpty(w, rjson)
	}
}

// ---------------------------------------------------------------------------
// Content fetching
// ---------------------------------------------------------------------------

// lumexSecFetchHeaders sets the required sec-fetch-* headers on a request.
// Without these the Lumex API returns just "OK" instead of JSON/redirect.
func lumexSecFetchHeaders(req *http.Request, playerOrigin string) {
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en-US;q=0.6,en;q=0.5")
	req.Header.Set("Origin", playerOrigin)
	req.Header.Set("Referer", playerOrigin+"/")
	req.Header.Set("User-Agent", lumexUA)
	req.Header.Set("Sec-Fetch-Site", "same-site")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Dest", "empty")
}

// lumexClientWithJar creates an http.Client with cookie jar + SOCKS5 transport
// for the "lumex" balancer (if registered). Falls back to default transport.
// The client follows redirects and copies headers on redirect.
func lumexClientWithJar() *http.Client {
	jar, _ := cookiejar.New(nil)
	t := httpclient.TransportForBalancer("lumex")
	if t == nil {
		t = httpclient.TransportForBalancer("")
	}
	c := &http.Client{
		Timeout: 20 * time.Second,
		Jar:     jar,
		// Preserve custom headers on redirect (sec-fetch-*, Origin, etc.).
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			if len(via) > 0 {
				for key, values := range via[0].Header {
					if _, exists := req.Header[key]; !exists {
						req.Header[key] = values
					}
				}
			}
			return nil
		},
	}
	if t != nil {
		c.Transport = t
	}
	return c
}

// fetchContent uses the short→redirect flow:
//  1. GET /content?contentType=short&clientId=X&kpId=Y  (with sec-fetch headers)
//     → 302 redirect to /content?contentId=N&contentType=movie&clientId=X
//  2. Follow redirect → JSON response + Set-Cookie: x-csrf-token=...
//  3. Parse response, extract CSRF token, return lumexContentResult.
func (l *lumexChecker) fetchContent(ctx context.Context, kpID string) *lumexContentResult {
	contentBase := l.resolveContentAPIBase()
	if contentBase == "" {
		return nil
	}

	client := lumexClientWithJar()
	playerOrigin := l.resolvePlayerOrigin()

	// Build the short lookup URL.
	u, err := url.Parse(contentBase + "/content")
	if err != nil {
		return nil
	}
	qs := url.Values{}
	qs.Set("clientId", l.clientID)
	qs.Set("contentType", "short")
	qs.Set("kpId", kpID)
	u.RawQuery = qs.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil
	}
	lumexSecFetchHeaders(req, playerOrigin)

	resp, err := client.Do(req) // follows 302 automatically
	if err != nil {
		log.Debug().Err(err).Msg("lumex: content fetch failed")
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Debug().Int("status", resp.StatusCode).Msg("lumex: content API non-2xx")
		return nil
	}

	// Cap at 4 MB — content API returns a JSON index; never legitimately larger.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil
	}

	// Parse the JSON content response.
	var cr lumexContentResponse
	if err := stdjson.Unmarshal(body, &cr); err != nil {
		log.Debug().Err(err).Msg("lumex: failed to parse content response")
		return nil
	}
	if len(cr.Player.Media) == 0 || string(cr.Player.Media) == "null" || string(cr.Player.Media) == "[]" {
		return nil
	}

	// Extract CSRF token from Set-Cookie header.
	csrfFull, csrfHdr := lumexExtractCSRF(resp)

	ct := cr.Player.ContentType
	if ct == "" {
		ct = "movie"
	}

	return &lumexContentResult{
		Media:       cr.Player.Media,
		ContentType: ct,
		CSRF:        csrfFull,
		CSRFHeader:  csrfHdr,
	}
}

// lumexExtractCSRF reads the x-csrf-token from Set-Cookie headers.
// Returns (fullValue, headerValue). headerValue is the part before %7C (for the header).
func lumexExtractCSRF(resp *http.Response) (string, string) {
	for _, c := range resp.Cookies() {
		if c.Name == "x-csrf-token" {
			full := c.Value
			hdr := full
			// The cookie value is URL-encoded; the header value is the part before %7C (|).
			if idx := strings.Index(full, "|"); idx > 0 {
				hdr = full[:idx]
			}
			return full, hdr
		}
	}
	// Fallback: try raw Set-Cookie header parsing.
	for _, sc := range resp.Header.Values("Set-Cookie") {
		if !strings.Contains(sc, "x-csrf-token=") {
			continue
		}
		val := strings.TrimPrefix(sc, "x-csrf-token=")
		if idx := strings.Index(val, ";"); idx > 0 {
			val = val[:idx]
		}
		decoded, err := url.QueryUnescape(val)
		if err != nil {
			decoded = val
		}
		hdr := decoded
		if idx := strings.Index(decoded, "|"); idx > 0 {
			hdr = decoded[:idx]
		}
		return decoded, hdr
	}
	return "", ""
}

// ---------------------------------------------------------------------------
// Validate (get stream URL)
// ---------------------------------------------------------------------------

// lumexValidate calls POST /validate/{encrypted} with CSRF token to get HLS stream URL.
func (l *lumexChecker) lumexValidate(ctx context.Context, playlist string, cr *lumexContentResult) (string, error) {
	contentBase := l.resolveContentAPIBase()
	if contentBase == "" {
		return "", fmt.Errorf("no content API base")
	}

	client := lumexClientWithJar()
	playerOrigin := l.resolvePlayerOrigin()

	// POST validate.
	validateURL := contentBase + playlist
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, validateURL, nil)
	if err != nil {
		return "", err
	}
	lumexSecFetchHeaders(req, playerOrigin)

	// Set CSRF token as header and cookie.
	if cr.CSRFHeader != "" {
		req.Header.Set("X-Csrf-Token", cr.CSRFHeader)
	}
	if cr.CSRF != "" {
		req.AddCookie(&http.Cookie{Name: "x-csrf-token", Value: cr.CSRF})
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("lumex validate: status %d", resp.StatusCode)
	}

	var vr lumexValidateResponse
	if err := stdjson.NewDecoder(resp.Body).Decode(&vr); err != nil {
		return "", err
	}

	streamURL := vr.URL
	if strings.HasPrefix(streamURL, "//") {
		streamURL = "https:" + streamURL
	}
	return streamURL, nil
}

// ---------------------------------------------------------------------------
// Movie output
// ---------------------------------------------------------------------------

func (l *lumexChecker) writeMovie(w http.ResponseWriter, req *http.Request, rjson bool, title, originalTitle string, cr *lumexContentResult, links *proxylink.Manager) {
	var voices []lumexVoice
	if err := stdjson.Unmarshal(cr.Media, &voices); err != nil || len(voices) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	baseTitle := getsTVJoinName(title, originalTitle)
	data := make([]map[string]any, 0, len(voices))
	labels := make([]string, 0, len(voices))

	for _, v := range voices {
		if v.Playlist == "" {
			continue
		}

		streamURL, err := l.lumexValidate(req.Context(), v.Playlist, cr)
		if err != nil || streamURL == "" {
			log.Debug().Err(err).Str("voice", v.TranslationName).Msg("lumex: validate failed")
			continue
		}
		log.Info().Str("voice", v.TranslationName).Str("stream_url", streamURL).Msg("lumex: validate OK")

		qualityBadge := lumexQualityBadge(v.MaxQuality)
		name := strings.TrimSpace(v.TranslationName)
		if name == "" {
			name = "По умолчанию"
		}

		proxyURL := lumexProxyURL(streamURL, req, links, l.resolvePlayerOrigin())

		row := map[string]any{
			"method": "play",
			"url":    proxyURL,
			"stream": proxyURL,
			"name":   name,
			"title":  baseTitle + " (" + name + ")",
		}
		if qualityBadge != "" {
			row["quality"] = qualityBadge
		}

		data = append(data, row)
		labels = append(labels, name)
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
	for i, row := range data {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, 0, 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// ---------------------------------------------------------------------------
// Serial output
// ---------------------------------------------------------------------------

func (l *lumexChecker) writeSerial(
	w http.ResponseWriter,
	req *http.Request,
	rjson bool,
	title, originalTitle, kpID string,
	s int,
	cr *lumexContentResult,
	links *proxylink.Manager,
) {
	var seasons []lumexSeason
	if err := stdjson.Unmarshal(cr.Media, &seasons); err != nil || len(seasons) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(req)
	encTitle := url.QueryEscape(title)
	encOriginal := url.QueryEscape(originalTitle)

	// No season selected — show season list.
	if s == -1 {
		l.writeSeasons(w, rjson, host, seasons, kpID, encTitle, encOriginal)
		return
	}

	// Season selected — find it and show episodes.
	var season *lumexSeason
	for i := range seasons {
		if seasons[i].SeasonID == s {
			season = &seasons[i]
			break
		}
	}
	if season == nil {
		writeGetsTVEmpty(w, rjson)
		return
	}

	l.writeEpisodes(w, req, rjson, host, season, title, originalTitle, kpID, encTitle, encOriginal, s, cr, links)
}

func (l *lumexChecker) writeSeasons(
	w http.ResponseWriter,
	rjson bool,
	host string,
	seasons []lumexSeason,
	kpID, encTitle, encOriginal string,
) {
	data := make([]map[string]any, 0, len(seasons))
	labels := make([]string, 0, len(seasons))

	for _, season := range seasons {
		name := strings.TrimSpace(season.SeasonName)
		if name == "" {
			name = strconv.Itoa(season.SeasonID) + " сезон"
		}
		link := host + "/lite/lumex?rjson=" + getsTVBool(rjson) +
			"&kinopoisk_id=" + kpID +
			"&title=" + encTitle +
			"&original_title=" + encOriginal +
			"&s=" + strconv.Itoa(season.SeasonID)
		data = append(data, map[string]any{
			"method": "link",
			"url":    link,
			"name":   name,
		})
		labels = append(labels, name)
	}

	if len(data) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
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
}

func (l *lumexChecker) writeEpisodes(
	w http.ResponseWriter,
	req *http.Request,
	rjson bool,
	host string,
	season *lumexSeason,
	title, originalTitle, kpID, encTitle, encOriginal string,
	s int,
	cr *lumexContentResult,
	links *proxylink.Manager,
) {
	baseTitle := getsTVJoinName(title, originalTitle)
	data := make([]map[string]any, 0)
	labels := make([]string, 0)

	for _, ep := range season.Episodes {
		if len(ep.Media) == 0 {
			continue
		}

		// Use the first available voice for each episode.
		// TODO: voice selection via query param.
		voice := ep.Media[0]
		if voice.Playlist == "" {
			continue
		}

		streamURL, err := l.lumexValidate(req.Context(), voice.Playlist, cr)
		if err != nil || streamURL == "" {
			log.Debug().Err(err).Str("ep", ep.Name).Msg("lumex: validate episode failed")
			continue
		}

		qualityBadge := lumexQualityBadge(voice.MaxQuality)
		epName := strings.TrimSpace(ep.Name)
		if epName == "" {
			epName = strconv.Itoa(ep.EpisodeID) + " серия"
		}
		voiceName := strings.TrimSpace(voice.TranslationName)
		if voiceName == "" {
			voiceName = "По умолчанию"
		}

		proxyURL := lumexProxyURL(streamURL, req, links, l.resolvePlayerOrigin())

		row := map[string]any{
			"method": "play",
			"url":    proxyURL,
			"stream": proxyURL,
			"name":   epName,
			"title":  fmt.Sprintf("%s / %d сезон %s (%s)", baseTitle, s, epName, voiceName),
		}
		if qualityBadge != "" {
			row["quality"] = qualityBadge
		}

		data = append(data, row)
		labels = append(labels, epName)
	}

	if len(data) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type":    "episode",
			"data":    data,
			"seasons": nil,
		})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range data {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, s, ep0Num(i, season))
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func lumexProxyURL(streamURL string, req *http.Request, links *proxylink.Manager, playerOrigin string) string {
	if links == nil {
		return streamURL
	}
	if isStreamProxyDisabled("lumex") {
		return streamURL
	}
	reqIP := clientIP(req)
	encrypted := links.EncryptURIWithHeaders(streamURL, reqIP, "lumex", map[string]string{
		"Origin":  playerOrigin,
		"Referer": playerOrigin + "/",
	})
	if encrypted == "" {
		return streamURL
	}
	return hostFromRequest(req) + "/proxy/" + encrypted
}

// ep0Num returns the episode number for index i within a season.
func ep0Num(i int, season *lumexSeason) int {
	if i < len(season.Episodes) {
		return season.Episodes[i].EpisodeID
	}
	return i + 1
}

func lumexQualityBadge(maxQ int) string {
	switch {
	case maxQ >= 2160:
		return "4K"
	case maxQ >= 1080:
		return "FHD"
	case maxQ >= 720:
		return "HD"
	default:
		return "SD"
	}
}

// ---------------------------------------------------------------------------
// checksearch (existing)
// ---------------------------------------------------------------------------

type lumexShortResponse struct {
	Data []stdjson.RawMessage `json:"data"`
}

func (l *lumexChecker) checkSearch(ctx context.Context, q url.Values) bool {
	// 1) Preferred path for Lumex API token mode.
	if l.token != "" {
		title := strings.TrimSpace(q.Get("title"))
		if title != "" && l.checkShortAPI(ctx, title) {
			return true
		}
	}

	// 2) Fallback: content API with clientId.
	if l.clientID != "" {
		if kp := strings.TrimSpace(q.Get("kinopoisk_id")); kp != "" {
			if _, err := strconv.ParseInt(kp, 10, 64); err == nil {
				return l.checkContentAPI(ctx, kp)
			}
		}
	}

	return false
}

func (l *lumexChecker) checkShortAPI(ctx context.Context, title string) bool {
	u, err := url.Parse(l.apiHost + "/api/short")
	if err != nil {
		return false
	}
	qs := url.Values{}
	qs.Set("api_token", l.token)
	qs.Set("title", title)
	u.RawQuery = qs.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return false
	}
	req.Header.Set("Referer", l.apiHost+"/")
	req.Header.Set("User-Agent", lumexUA)
	req.Header.Set("X-Lampac-Go", "1")

	resp, err := l.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false
	}

	var root lumexShortResponse
	if err := stdjson.NewDecoder(resp.Body).Decode(&root); err != nil {
		return false
	}
	return len(root.Data) > 0
}

func (l *lumexChecker) checkContentAPI(ctx context.Context, kpID string) bool {
	contentBase := l.resolveContentAPIBase()
	if contentBase == "" {
		return false
	}

	client := lumexClientWithJar()
	playerOrigin := l.resolvePlayerOrigin()

	u, err := url.Parse(contentBase + "/content")
	if err != nil {
		return false
	}
	qs := url.Values{}
	qs.Set("clientId", l.clientID)
	qs.Set("contentType", "short")
	qs.Set("kpId", kpID)
	u.RawQuery = qs.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return false
	}
	lumexSecFetchHeaders(req, playerOrigin)

	resp, err := client.Do(req) // follows 302 redirect
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false
	}

	var root map[string]stdjson.RawMessage
	if err := stdjson.NewDecoder(resp.Body).Decode(&root); err != nil {
		return false
	}
	_, ok := root["player"]
	return ok || len(root) > 0
}

// ---------------------------------------------------------------------------
// URL resolution helpers
// ---------------------------------------------------------------------------

func (l *lumexChecker) resolveContentAPIBase() string {
	host := strings.TrimSpace(l.iframeHost)
	if host == "" {
		return ""
	}
	if strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://") {
		u, err := url.Parse(host)
		if err != nil || u.Host == "" {
			return ""
		}
		return u.Scheme + "://" + u.Host
	}
	if strings.Contains(host, ":") {
		return "https://" + host
	}
	return "https://api." + host
}

func (l *lumexChecker) resolvePlayerOrigin() string {
	host := strings.TrimSpace(l.iframeHost)
	if host == "" {
		return "https://p.lumex.space"
	}
	if strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://") {
		u, err := url.Parse(host)
		if err != nil || u.Host == "" {
			return "https://p.lumex.space"
		}
		return u.Scheme + "://p." + u.Host
	}
	if strings.Contains(host, ":") {
		return "https://" + host
	}
	return "https://p." + host
}
