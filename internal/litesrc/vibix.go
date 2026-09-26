package litesrc

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	stdjson "encoding/json"
	"fmt"
	"html"
	"io"
	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/proxylink"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	vibixDecoderKey        = "RySdvcyu5iTUxn97vn4HwoniwgxaCynA"
	vibixDecoderUseReverse = true
	vibixSigningKey        = "RTAJTmjFegZfxynQ95EwdoqYrQ2T5ZJE"
)

var (
	vibixStreamRe = regexp.MustCompile(`(?i)\[(1080|720|480)p?\](?:\{[^}]+\})?(https?://[^,\t\[\;\{ ]+)`)

	// vibixM3U8Cache stores pending m3u8 entries keyed by random hex token.
	// When the player requests /vibix_m3u8/{key}, we use Chrome to fetch the
	// river-* m3u8 (which our Go code can't access due to TLS fingerprinting),
	// rewrite vbx-* segment URLs through /proxy/, and serve the result.
	vibixM3U8Cache sync.Map // key → *vibixM3U8Entry

	vibixM3U8SegmentRe = regexp.MustCompile(`(?m)^(https?://[^\s]+)$`)
)

type vibixChecker struct {
	client     *http.Client // for vibix.org publisher API
	host       string
	token      string
	iframeMode bool // hand iframe-capable clients the vibix player embed page (revenue+stats)
}

type vibixVideo struct {
	IframeURL string `json:"iframe_url"`
	Type      string `json:"type"`
}

type vibixPlaylistResponse struct {
	Data struct {
		Playlist []vibixSeason `json:"playlist"`
	} `json:"data"`
}

// vibixEncryptedResponse is the format returned by the embed API when encrypted.
type vibixEncryptedResponse struct {
	P string `json:"p"`
	V int    `json:"v"`
}

type vibixSeason struct {
	Title  string        `json:"title"`
	Folder []vibixSeason `json:"folder"`
	File   string        `json:"file"`
}

func NewVibixChecker(cfg config.Config) *vibixChecker {
	host := strings.TrimSpace(strings.TrimRight(cfg.Online.Vibix.Host, "/"))
	if host == "" {
		host = "https://vibix.org"
	}
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}

	// No residential SOCKS5 here: the coldfilm/rendex browser resolves the CDN
	// stream from the server's direct IP, and the Kinescope `sign` is IP-bound,
	// so the m3u8 fetch and segment proxy must use that SAME direct IP. Routing
	// vibix through a residential SOCKS5 would break the sign (IP mismatch).

	return &vibixChecker{
		client:     httpclient.NewForBalancer("vibix", 12*time.Second),
		host:       host,
		token:      strings.TrimSpace(cfg.Online.Vibix.Token),
		iframeMode: cfg.Online.Vibix.IframeMode,
	}
}

func (v *vibixChecker) Handle(cfg config.Config, proxyLinks *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		// Deferred playback endpoint (/lite/vibix/stream.m3u8): runs the browser
		// resolve on demand. Used by the /capi deferred path so aggregation isn't
		// blocked by the browser resolve.
		if strings.Contains(req.URL.Path, "/stream") {
			v.stream(w, req, proxyLinks)
			return
		}

		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show, quality := v.checkSearchQuality(req)
			if quality == "" {
				quality = pluginQualityBadgeGet("vibix")
			}
			writeCheckSearchResponseNoRCH(w, show, quality)
			return
		}

		v.index(w, req, proxyLinks)
	}
}

func (v *vibixChecker) checkSearch(req *http.Request) bool {
	show, _ := v.checkSearchQuality(req)
	return show
}

// checkSearchQuality reports availability together with the quality badge the
// publisher API advertises for this title. The API call is the one checkSearch
// already made, so the badge is free — vibix used to report a hardcoded FHD
// even for 4K releases.
func (v *vibixChecker) checkSearchQuality(req *http.Request) (bool, string) {
	probe := func() bool {
		if v.probe(req, http.MethodHead, v.host) {
			return true
		}
		return v.probe(req, http.MethodGet, v.host)
	}

	imdbID := strings.TrimSpace(req.URL.Query().Get("imdb_id"))
	kinopoiskID, _ := strconv.ParseInt(strings.TrimSpace(req.URL.Query().Get("kinopoisk_id")), 10, 64)

	// With a token and an id, ask the publisher API whether the title is in the
	// Vibix catalog — an accurate signal that avoids showing Vibix for titles it
	// doesn't have. If the API can't answer (no token, no id, or a transport /
	// auth error) fall back to a host probe: playback via coldfilm doesn't need
	// the token, so it's better to show the source than to hide a playable title.
	if strings.TrimSpace(v.token) != "" && (imdbID != "" || kinopoiskID != 0) {
		avail, quality := v.apiVideoStatus(req, imdbID, kinopoiskID)
		switch avail {
		case vibixAvailYes:
			return true, vibixQualityBadge(quality)
		case vibixAvailNo:
			return false, ""
		}
	}

	// Host probe only answers "reachable" — no catalogue data, so no badge.
	return probe(), ""
}

// vibixQualityBadge maps the publisher API `quality` string onto a canonical
// badge. Same vocabulary vibixQualityLabels already understands.
func vibixQualityBadge(quality string) string {
	q := strings.ToLower(strings.TrimSpace(quality))
	switch {
	case q == "":
		return ""
	case strings.Contains(q, "2160"), strings.Contains(q, "4k"), strings.Contains(q, "uhd"):
		return "4K"
	case strings.Contains(q, "1440"), strings.Contains(q, "2k"), strings.Contains(q, "qhd"):
		return "2K"
	case strings.Contains(q, "1080"), strings.Contains(q, "fullhd"), strings.Contains(q, "fhd"):
		return "FHD"
	case q == "hd", strings.Contains(q, "720"):
		return "HD"
	case strings.Contains(q, "480"), strings.Contains(q, "360"), strings.Contains(q, "sd"):
		return "SD"
	}
	return normalizeQualityBadge(quality)
}

type vibixAvail int

const (
	vibixAvailUnknown vibixAvail = iota
	vibixAvailYes
	vibixAvailNo
)

// apiVideoStatus reports whether the Vibix catalog contains the given title,
// plus the catalogue's quality string for it — the SAME publisher endpoint
// apiMeta uses, so the badge costs no extra request.
// vibixAvailYes = API returned the video; vibixAvailNo = API 404; anything else
// (auth error, transport error, malformed body) = vibixAvailUnknown.
func (v *vibixChecker) apiVideoStatus(req *http.Request, imdbID string, kinopoiskID int64) (vibixAvail, string) {
	var target string
	switch {
	case kinopoiskID > 0:
		target = strings.TrimRight(v.host, "/") + "/api/v1/publisher/videos/kp/" + strconv.FormatInt(kinopoiskID, 10)
	case strings.TrimSpace(imdbID) != "":
		target = strings.TrimRight(v.host, "/") + "/api/v1/publisher/videos/imdb/" + url.PathEscape(strings.TrimSpace(imdbID))
	default:
		return vibixAvailUnknown, ""
	}

	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, target, nil)
	if err != nil {
		return vibixAvailUnknown, ""
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+v.token)
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	httpReq.Header.Set("X-Lampac-Go", "1")

	resp, err := v.client.Do(httpReq)
	if err != nil {
		return vibixAvailUnknown, ""
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return vibixAvailNo, ""
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return vibixAvailUnknown, ""
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return vibixAvailUnknown, ""
	}
	var meta struct {
		Name      string `json:"name"`
		EmbedCode string `json:"embed_code"`
		Type      string `json:"type"`
		Quality   string `json:"quality"`
	}
	if err := stdjson.Unmarshal(body, &meta); err != nil {
		return vibixAvailUnknown, ""
	}
	if strings.TrimSpace(meta.Name) != "" || strings.TrimSpace(meta.EmbedCode) != "" {
		return vibixAvailYes, strings.TrimSpace(meta.Quality)
	}
	return vibixAvailUnknown, ""
}

// ---------------------------------------------------------------------------
// /capi deferred path: fast voice/quality metadata from the publisher API, with
// the browser resolve deferred to /lite/vibix/stream.m3u8 on playback.
// ---------------------------------------------------------------------------

type vibixAPIMeta struct {
	Type      string
	Quality   string
	Voices    []string
	EmbedCode string // <ins> attributes carrying our publisher id (direct-iframe playback)
}

// apiMeta fetches a title's type, quality and voice list from the publisher API
// (fast, no browser). Needs a token; returns ok=false on any error so the caller
// can fall back to the browser resolve.
func (v *vibixChecker) apiMeta(req *http.Request, imdbID string, kinopoiskID int64) (vibixAPIMeta, bool) {
	if strings.TrimSpace(v.token) == "" {
		return vibixAPIMeta{}, false
	}
	var target string
	switch {
	case kinopoiskID > 0:
		target = strings.TrimRight(v.host, "/") + "/api/v1/publisher/videos/kp/" + strconv.FormatInt(kinopoiskID, 10)
	case strings.TrimSpace(imdbID) != "":
		target = strings.TrimRight(v.host, "/") + "/api/v1/publisher/videos/imdb/" + url.PathEscape(strings.TrimSpace(imdbID))
	default:
		return vibixAPIMeta{}, false
	}

	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, target, nil)
	if err != nil {
		return vibixAPIMeta{}, false
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+v.token)
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	httpReq.Header.Set("X-Lampac-Go", "1")

	resp, err := v.client.Do(httpReq)
	if err != nil {
		return vibixAPIMeta{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return vibixAPIMeta{}, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return vibixAPIMeta{}, false
	}
	var raw struct {
		Type       string `json:"type"`
		Quality    string `json:"quality"`
		EmbedCode  string `json:"embed_code"`
		Voiceovers []struct {
			Name string `json:"name"`
		} `json:"voiceovers"`
	}
	if err := stdjson.Unmarshal(body, &raw); err != nil {
		return vibixAPIMeta{}, false
	}
	voices := make([]string, 0, len(raw.Voiceovers))
	seen := make(map[string]struct{}, len(raw.Voiceovers))
	for _, vo := range raw.Voiceovers {
		name := strings.TrimSpace(vo.Name)
		if name == "" {
			continue
		}
		if _, ok := seen[strings.ToLower(name)]; ok {
			continue
		}
		seen[strings.ToLower(name)] = struct{}{}
		voices = append(voices, name)
	}
	return vibixAPIMeta{
		Type:      strings.ToLower(strings.TrimSpace(raw.Type)),
		Quality:   raw.Quality,
		Voices:    voices,
		EmbedCode: strings.TrimSpace(raw.EmbedCode),
	}, true
}

// vibixCardIsSerial mirrors capi's own card-kind test (capiCardIsSerial in
// internal/httpapi/capi.go) so this source never answers in a shape capi will
// throw away.
//
// vibix classifies titles itself, and it disagrees with the card often enough to
// matter: measured on prod 2026-09-01, capi dropped 66 vibix answers in two
// hours, every one of them "resp_type=movie card_serial=True" — the publisher
// API called the title a film while the client had opened a series card. The
// answer was well-formed and the stream would have played; capi discarded it on
// the type guard, vibix vanished from the list, and the player moved on to the
// next source. Trusting the CARD here, not the upstream's own idea of the
// title, is what keeps the source in the running.
func vibixCardIsSerial(q url.Values) bool {
	switch strings.ToLower(strings.TrimSpace(q.Get("serial"))) {
	case "1", "true":
		return true
	}
	return strings.TrimSpace(q.Get("s")) != "" || strings.TrimSpace(q.Get("e")) != ""
}

// writeCapiMovie emits the deferred /capi movie response (one voice per entry,
// each quality pointing at /lite/vibix/stream.m3u8). Returns false when the title
// is a serial or the API had no usable metadata, so index() falls back to the
// browser resolve.
func (v *vibixChecker) writeCapiMovie(w http.ResponseWriter, req *http.Request, imdbID string, kinopoiskID int64, title, originalTitle string) bool {
	// The CARD decides, not the upstream's classification — see vibixCardIsSerial.
	if vibixCardIsSerial(req.URL.Query()) {
		return false
	}
	meta, ok := v.apiMeta(req, imdbID, kinopoiskID)
	if !ok || meta.Type == "serial" || len(meta.Voices) == 0 {
		return false
	}

	host := hostFromRequest(req)
	labels := vibixQualityLabels(meta.Quality)
	data := make([]map[string]any, 0, len(meta.Voices))
	for _, voice := range meta.Voices {
		byq := vibixDeferredQuality(host, imdbID, kinopoiskID, voice, labels)
		if len(byq) == 0 {
			continue
		}
		// No `title` — capi's wrong-film guard skips title-less rows, and the
		// voice name comes from `name`/`translate`.
		data = append(data, map[string]any{
			"name":      voice,
			"translate": voice,
			"quality":   byq,
		})
	}
	if len(data) == 0 {
		return false
	}
	writeJSON(w, http.StatusOK, map[string]any{"type": "movie", "data": data})
	return true
}

// ---------------------------------------------------------------------------
// Direct-iframe playback: serve the vibix player embed page to the client so
// vibix's player runs client-side under our publisher id (ads + view count →
// revenue + /publisher/statistics). Gated behind config iframe_mode.
// ---------------------------------------------------------------------------

// vibixIframeScheme marks a capi quality URL as an embed-me player page rather
// than an HLS stream (same contract as alloha): the marker travels ON the url so
// it survives capi's flat {label→url} merge. Clients strip the scheme (→ https),
// open the page in a webview/iframe overlay, and never feed it to Shaka/native.
const vibixIframeScheme = "iframe://"

// vibixCapiIframeURL wraps an embed page URL in the iframe:// scheme for capi.
func vibixCapiIframeURL(embedURL string) string {
	return vibixIframeScheme + strings.TrimPrefix(strings.TrimPrefix(embedURL, "https://"), "http://")
}

// writeIframeMovie emits the direct-iframe response for a MOVIE: it reads the
// title's embed_code (the <ins> attributes carrying our publisher id) from the
// fast publisher API and hands back a /vibix_embed/ page URL the client embeds.
// Returns false (caller falls back to server-resolve) when there's no token, the
// title is a serial, or the API had no usable embed_code.
func (v *vibixChecker) writeIframeMovie(w http.ResponseWriter, req *http.Request, capi bool, imdbID string, kinopoiskID int64, title, originalTitle string) bool {
	// The CARD decides, not the upstream's classification — see vibixCardIsSerial.
	if vibixCardIsSerial(req.URL.Query()) {
		return false
	}
	meta, ok := v.apiMeta(req, imdbID, kinopoiskID)
	if !ok || meta.Type == "serial" || vibixSanitizeEmbedCode(meta.EmbedCode) == "" {
		return false
	}

	name := getsTVJoinName(title, originalTitle)
	if strings.TrimSpace(name) == "" {
		name = "Vibix"
	}
	embedURL := vibixEmbedURL(meta.EmbedCode, name, req)

	if capi {
		// No `title` on the row — capi's wrong-film guard skips title-less rows and
		// the voice name comes from name/translate.
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "movie",
			"data": []map[string]any{{
				"name":      "Vibix",
				"translate": "Vibix",
				"quality":   map[string]string{"auto": vibixCapiIframeURL(embedURL)},
			}},
		})
		return true
	}

	// Lampa (rjson): method:"iframe" tells the player to embed the page.
	writeJSON(w, http.StatusOK, map[string]any{
		"type": "movie",
		"data": []map[string]any{{
			"method": "iframe",
			"url":    embedURL,
			"iframe": embedURL,
			"name":   name,
			"title":  name,
		}},
	})
	return true
}

// vibixQualityLabels maps the publisher API `quality` string to descending
// quality labels to advertise. The exact per-voice set is only known after the
// browser resolve; the stream endpoint picks the closest available.
func vibixQualityLabels(quality string) []string {
	q := strings.ToLower(strings.TrimSpace(quality))
	switch {
	case strings.Contains(q, "2160"), strings.Contains(q, "4k"), strings.Contains(q, "uhd"):
		return []string{"2160p", "1080p", "720p", "480p"}
	case strings.Contains(q, "1440"), strings.Contains(q, "2k"):
		return []string{"1440p", "1080p", "720p", "480p"}
	case strings.Contains(q, "1080"), strings.Contains(q, "fullhd"), strings.Contains(q, "fhd"):
		return []string{"1080p", "720p", "480p"}
	case q == "hd", strings.Contains(q, "720"):
		return []string{"720p", "480p"}
	case strings.Contains(q, "480"), q == "sd":
		return []string{"480p"}
	default:
		return []string{"1080p", "720p", "480p"}
	}
}

// vibixDeferredQuality builds the {label → /lite/vibix/stream.m3u8 URL} map for
// one voice. Every label points at the same resolve endpoint; ?q= carries the
// picked label so the stream handler returns that quality (or the closest one).
func vibixDeferredQuality(host, imdbID string, kinopoiskID int64, voice string, labels []string) map[string]string {
	return vibixDeferredQualityAt(host, imdbID, kinopoiskID, voice, labels, 0, 0)
}

// vibixDeferredQualityAt is the same deferred playback URL with an optional
// season/episode, so a serial row points at the episode the caller asked for.
func vibixDeferredQualityAt(host, imdbID string, kinopoiskID int64, voice string, labels []string, s, e int) map[string]string {
	base := host + "/lite/vibix/stream.m3u8?"
	if kinopoiskID > 0 {
		base += "kinopoisk_id=" + strconv.FormatInt(kinopoiskID, 10)
	} else {
		base += "imdb_id=" + url.QueryEscape(imdbID)
	}
	base += "&voice=" + url.QueryEscape(voice)
	if s > 0 && e > 0 {
		base += "&s=" + strconv.Itoa(s) + "&e=" + strconv.Itoa(e)
	}
	byq := make(map[string]string, len(labels))
	for _, l := range labels {
		byq[l] = base + "&q=" + url.QueryEscape(l)
	}
	return byq
}

// writeCapiSerial answers a /capi drill for a SERIAL episode straight from the
// publisher API — voices and quality — with the browser resolve deferred to
// playback (/lite/vibix/stream.m3u8), exactly as writeCapiMovie does for films.
//
// Why: serials are 57% of the traffic here and used to have no API path at all,
// so every drill — including the calendar's new-translation cron, which only
// wants voice NAMES — drove a headless-browser resolve. Measured 2026-09-01:
// 415 successful resolves and 244 failures a day, 46.5 minutes of browser time
// in the failures alone.
//
// Needs a concrete season+episode (same contract as alloha's serial path): the
// row carries s/e so capi's voice merge keeps episodes apart.
func (v *vibixChecker) writeCapiSerial(w http.ResponseWriter, req *http.Request, imdbID string, kinopoiskID int64, title, originalTitle string, s, e int) bool {
	if s <= 0 || e <= 0 {
		return false
	}
	meta, ok := v.apiMeta(req, imdbID, kinopoiskID)
	if !ok || meta.Type != "serial" || len(meta.Voices) == 0 {
		return false
	}
	host := hostFromRequest(req)
	labels := vibixQualityLabels(meta.Quality)
	baseTitle := getsTVJoinName(title, originalTitle)
	if strings.TrimSpace(baseTitle) == "" {
		baseTitle = "Vibix"
	}
	rows := make([]map[string]any, 0, len(meta.Voices))
	for _, voice := range meta.Voices {
		byq := vibixDeferredQualityAt(host, imdbID, kinopoiskID, voice, labels, s, e)
		if len(byq) == 0 {
			continue
		}
		rows = append(rows, map[string]any{
			"translate": voice,
			"quality":   byq,
			"title":     fmt.Sprintf("%s / %d сезон %d серия (%s)", baseTitle, s, e, voice),
			"s":         s,
			"e":         e,
		})
	}
	if len(rows) == 0 {
		return false
	}
	writeJSON(w, http.StatusOK, map[string]any{"type": "serial", "data": rows})
	return true
}

// stream resolves the coldfilm playlist (browser, cached) and serves the m3u8
// for the requested voice+quality. Backs the deferred /capi playback URL.
func (v *vibixChecker) stream(w http.ResponseWriter, req *http.Request, proxyLinks *proxylink.Manager) {
	q := req.URL.Query()
	imdbID := strings.TrimSpace(q.Get("imdb_id"))
	kinopoiskID, _ := strconv.ParseInt(strings.TrimSpace(q.Get("kinopoisk_id")), 10, 64)
	voice := strings.TrimSpace(q.Get("voice"))
	wantQ := strings.TrimSpace(q.Get("q"))
	sParam, _ := getsTVQueryInt(q.Get("s"))
	eParam, _ := getsTVQueryInt(q.Get("e"))

	playlist, ok := vibixResolveViaColdfilm(req.Context(), imdbID, kinopoiskID)
	if !ok || len(playlist) == 0 {
		http.Error(w, "vibix: resolve failed", http.StatusBadGateway)
		return
	}

	file := vibixPickFile(playlist, voice, sParam, eParam)
	if file == "" {
		http.Error(w, "vibix: voice/episode not found", http.StatusNotFound)
		return
	}
	streams := vibixParseStreams(file)
	if len(streams) == 0 {
		http.Error(w, "vibix: no streams", http.StatusNotFound)
		return
	}
	streamURL := vibixPickQuality(streams, wantQ)
	origin := vibixStreamOrigin(streamURL)

	m3u8, fok := vibixFetchM3U8Direct(req, streamURL, origin)
	if !fok {
		http.Error(w, "vibix: m3u8 fetch failed", http.StatusBadGateway)
		return
	}
	rewritten := vibixRewriteM3U8(m3u8, req, proxyLinks, origin)

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(rewritten))
}

// vibixPickFile returns the file string for the requested voice (movie) or
// season/episode (serial) from a resolved playlist.
func vibixPickFile(playlist []vibixSeason, voice string, s, e int) string {
	if s > 0 && e > 0 {
		for i := range playlist {
			id, ok := vibixSeasonNumber(playlist[i].Title)
			if !ok || id != s {
				continue
			}
			for _, ep := range playlist[i].Folder {
				num, _ := vibixEpisodeNumber(ep.Title)
				if num != e {
					continue
				}
				if f := strings.TrimSpace(ep.File); f != "" {
					return f
				}
				// An episode may carry a voice folder. Taking Folder[0]
				// unconditionally (as this did) meant every voice we advertised for a
				// serial played the SAME track — the caller's choice was silently
				// dropped. Match the requested voice first, and only fall back to the
				// first entry when it is absent or unnamed.
				if voice != "" {
					for _, vo := range ep.Folder {
						if strings.EqualFold(strings.TrimSpace(vo.Title), strings.TrimSpace(voice)) {
							if f := strings.TrimSpace(vo.File); f != "" {
								return f
							}
						}
					}
				}
				if len(ep.Folder) > 0 {
					return strings.TrimSpace(ep.Folder[0].File)
				}
				return ""
			}
		}
		return ""
	}
	if voice != "" {
		for _, it := range playlist {
			if strings.TrimSpace(it.File) != "" && strings.EqualFold(strings.TrimSpace(it.Title), voice) {
				return it.File
			}
		}
	}
	for _, it := range playlist {
		if strings.TrimSpace(it.File) != "" {
			return it.File
		}
	}
	return ""
}

// vibixPickQuality returns the stream URL for the requested quality label, or
// the highest available (streams are sorted descending) when it's absent.
func vibixPickQuality(streams []map[string]string, wantQ string) string {
	if wantQ != "" {
		for _, s := range streams {
			if strings.EqualFold(s["quality"], wantQ) {
				return s["url"]
			}
		}
	}
	return streams[0]["url"]
}

func (v *vibixChecker) index(w http.ResponseWriter, req *http.Request, proxyLinks *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	imdbID := strings.TrimSpace(q.Get("imdb_id"))
	kinopoiskID, _ := strconv.ParseInt(strings.TrimSpace(q.Get("kinopoisk_id")), 10, 64)
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	s, sOK := getsTVQueryInt(q.Get("s"))
	if !sOK {
		s = -1
	}

	// Direct-iframe (movies only): when iframe_mode is on, hand iframe-capable
	// clients the vibix player embed page instead of the server-proxied stream, so
	// vibix's player runs client-side under OUR publisher id — its ads render, the
	// view is counted and /publisher/statistics + revenue register. capi advertises
	// the webview capability with iframe=1; Lampa clients consume method:"iframe".
	// Serials embed the whole title behind vibix's own episode picker (which the
	// client season UI can't drive), so they stay on the server-resolve path.
	{
		capiFlag := capiResolveRequest(req)
		wantIframe := (capiFlag && parseBoolParam(q.Get("iframe"))) || (!capiFlag && rjson)
		taken := false
		if v.iframeMode && wantIframe {
			taken = v.writeIframeMovie(w, req, capiFlag, imdbID, kinopoiskID, title, originalTitle)
		}
		// Why this is logged: the direct-iframe path looks enabled from the config
		// alone, yet in production it fires for a minority of drills. Measured
		// 2026-09-01: 76% of capi drills reach vibix with NO iframe param even
		// though clients set it on 97% of /capi/streams — the calendar's
		// new-translation cron (calendar_voices.go) builds its own synthetic
		// request and never carries it. Keep the inputs visible so the next
		// "iframe mode is on but nothing embeds" question is one grep, not a day.
		log.Debug().Bool("iframe_mode", v.iframeMode).Bool("capi", capiFlag).
			Str("iframe_param", q.Get("iframe")).Bool("rjson", rjson).
			Bool("want_iframe", wantIframe).Bool("taken", taken).
			Str("imdb", imdbID).Int64("kp", kinopoiskID).
			Msg("vibix: iframe decision")
		if taken {
			return
		}
	}

	// /capi aggregation drill: for MOVIES, return the voices + qualities from the
	// fast publisher API and defer the browser resolve to playback
	// (/lite/vibix/stream.m3u8). This keeps the drill within capi's soft deadline
	// instead of blocking it on a multi-second Chrome resolve (same pattern as
	// alloha). Serials need per-episode voices that only the browser knows, so
	// they fall through to the browser resolve below.
	if capiResolveRequest(req) {
		if v.writeCapiMovie(w, req, imdbID, kinopoiskID, title, originalTitle) {
			return
		}
		if e, eOK := getsTVQueryInt(q.Get("e")); eOK {
			if v.writeCapiSerial(w, req, imdbID, kinopoiskID, title, originalTitle, s, e) {
				return
			}
		}
	}

	var videoType string
	var iframeOrigin string
	var playlist []vibixSeason

	// Path 1 (legacy / direct API): if the publisher API still returns a real
	// iframe_url, use it with the sig-authenticated embed API. Vibix currently
	// returns an empty iframe_url for all titles, so in production this path is
	// skipped and we fall through to path 2 — but it stays wired for the case
	// the API restores iframe_url (and it is what the unit tests exercise).
	if video, ok := v.search(req, imdbID, kinopoiskID); ok && video.IframeURL != "" {
		videoType = strings.ToLower(strings.TrimSpace(video.Type))
		iframeOrigin = vibixOriginFromIframeURL(video.IframeURL)
		if pl, plOK := v.fetchPlaylist(req, video.IframeURL); plOK {
			playlist = pl
		}
	}

	// Path 2 (current production): resolve the PlayerJS playlist through the
	// coldfilm.ink + rendex SDK browser flow, keyed directly on the kinopoisk/
	// imdb id. The browser computes the Kinescope sig itself. movie vs serial
	// is decided below by folder presence.
	if len(playlist) == 0 {
		if pl, ok := vibixResolveViaColdfilm(req.Context(), imdbID, kinopoiskID); ok {
			playlist = pl
			videoType = ""
			iframeOrigin = ""
		}
	}

	if len(playlist) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	hasFolder := false
	for _, item := range playlist {
		if len(item.Folder) > 0 {
			hasFolder = true
			break
		}
	}
	if videoType == "movie" || (videoType != "serial" && !hasFolder) {
		v.writeMovie(w, req, rjson, title, originalTitle, playlist, proxyLinks, iframeOrigin)
	} else {
		v.writeSerial(w, req, rjson, imdbID, kinopoiskID, title, originalTitle, s, playlist, proxyLinks, iframeOrigin)
	}
}

func (v *vibixChecker) search(req *http.Request, imdbID string, kinopoiskID int64) (vibixVideo, bool) {
	buildURI := func() string {
		if kinopoiskID > 0 {
			return strings.TrimRight(v.host, "/") + "/api/v1/publisher/videos/kp/" + strconv.FormatInt(kinopoiskID, 10)
		}
		if strings.TrimSpace(imdbID) != "" {
			return strings.TrimRight(v.host, "/") + "/api/v1/publisher/videos/imdb/" + url.PathEscape(strings.TrimSpace(imdbID))
		}
		return ""
	}

	target := buildURI()
	if target == "" {
		return vibixVideo{}, false
	}

	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, target, nil)
	if err != nil {
		return vibixVideo{}, false
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+v.token)
	httpReq.Header.Set("X-CSRF-TOKEN", "")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	httpReq.Header.Set("X-Lampac-Go", "1")

	resp, err := v.client.Do(httpReq)
	if err != nil {
		return vibixVideo{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return vibixVideo{}, false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return vibixVideo{}, false
	}
	var video vibixVideo
	if err := stdjson.Unmarshal(body, &video); err != nil {
		return vibixVideo{}, false
	}
	if strings.TrimSpace(video.IframeURL) == "" || strings.TrimSpace(video.Type) == "" {
		return vibixVideo{}, false
	}
	return video, true
}

// ---------------------------------------------------------------------------
// Vibix API signature computation (deobfuscated from embed.js)
// ---------------------------------------------------------------------------

// vibixDjb2ChunkHash computes a djb2 hash with chunk-sum encoding.
func vibixDjb2ChunkHash(s string) string {
	if len(s) == 0 {
		return "0"
	}
	var hash int32
	for _, c := range s {
		hash = ((hash << 5) - hash) + int32(c)
	}
	if hash < 0 {
		hash = -hash
	}
	hexStr := strconv.FormatInt(int64(hash), 16)
	for len(hexStr) < 8 {
		hexStr = "0" + hexStr
	}
	var sb strings.Builder
	sb.WriteString(hexStr)
	for i := 0; i < len(s); i += 3 {
		end := i + 3
		if end > len(s) {
			end = len(s)
		}
		var sum int
		for j := i; j < end; j++ {
			sum += int(s[j])
		}
		sb.WriteString(strconv.FormatInt(int64(sum), 16))
	}
	return sb.String()
}

// vibixHmacSign computes HMAC-like signature using djb2ChunkHash.
func vibixHmacSign(key, data string) string {
	if len(key) > 64 {
		key = vibixDjb2ChunkHash(key)
	}
	for len(key)%64 != 0 {
		key += "\x00"
	}
	var ipadKey, opadKey strings.Builder
	for i := 0; i < 64; i++ {
		ipadKey.WriteByte(key[i] ^ 0x36)
		opadKey.WriteByte(key[i] ^ 0x5c)
	}
	inner := vibixDjb2ChunkHash(ipadKey.String() + data)
	return vibixDjb2ChunkHash(opadKey.String() + inner)
}

// vibixComputeSig generates sig, ts, nonce parameters for the embed API.
func vibixComputeSig(path, domain string) (sig string, ts int64, nonce string) {
	ts = time.Now().Unix()
	// Generate random nonce (base36-like)
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	nonce = hex.EncodeToString(b)[:11]
	data := path + "|" + domain + "|" + strconv.FormatInt(ts, 10) + "|" + nonce
	sig = vibixHmacSign(vibixSigningKey, data)
	return
}

func (v *vibixChecker) fetchPlaylist(req *http.Request, iframeURL string) ([]vibixSeason, bool) {
	iframeURL = strings.TrimSpace(iframeURL)
	if iframeURL == "" {
		return nil, false
	}

	apiURL := strings.Replace(iframeURL, "/embed/", "/api/v1/embed/", 1)
	apiURL = strings.Replace(apiURL, "/embed-serials/", "/api/v1/embed-serials/", 1)

	// Compute sig for authenticated API call — returns CDN URLs with valid signs.
	embedPath := "/" + strings.SplitN(strings.TrimPrefix(iframeURL, "https://"), "/", 2)[1]
	sig, ts, nonce := vibixComputeSig(embedPath, "kinomix.web.app")

	apiURL += "?domain=kinomix.web.app"
	apiURL += "&iframe_url=" + url.QueryEscape(iframeURL)
	apiURL += "&sig=" + url.QueryEscape(sig)
	apiURL += "&ts=" + strconv.FormatInt(ts, 10)
	apiURL += "&nonce=" + url.QueryEscape(nonce)
	apiURL += "&a=0"

	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, false
	}
	httpReq.Header.Set("accept", "*/*")
	httpReq.Header.Set("accept-language", "ru-RU,ru;q=0.9,en-US;q=0.8,en;q=0.7")
	httpReq.Header.Set("sec-fetch-dest", "empty")
	httpReq.Header.Set("sec-fetch-mode", "cors")
	httpReq.Header.Set("sec-fetch-site", "same-origin")
	httpReq.Header.Set("referer", iframeURL)
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	httpReq.Header.Set("X-Lampac-Go", "1")

	// Use uTLS DIRECT (not SOCKS5) for the API call.
	// The CDN sign is IP-bound — API and m3u8 fetch must use the SAME IP.
	// Chrome DIRECT uses server IP for m3u8, so API must also use server IP.
	directUTLS := httpclient.NewUTLS(15 * time.Second)
	resp, err := directUTLS.Do(httpReq)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, false
	}

	// Try decoding encrypted response first.
	decoded, ok := vibixDecodeAPIResponse(body)
	if ok {
		body = decoded
	}

	var root vibixPlaylistResponse
	if err := stdjson.Unmarshal(body, &root); err != nil {
		return nil, false
	}
	if len(root.Data.Playlist) == 0 {
		return nil, false
	}

	// Debug: log CDN hostnames to verify residential proxy routes to vbx-* (not river-*).
	for _, item := range root.Data.Playlist {
		for _, m := range vibixStreamRe.FindAllStringSubmatch(item.File, 2) {
			if len(m) == 3 {
				if u, err := url.Parse(strings.TrimSpace(m[2])); err == nil {
					sign := u.Query().Get("sign")
					log.Info().Str("cdn_host", u.Host).Str("quality", m[1]).Int("sign_len", len(sign)).Msg("vibix: CDN URL from API")
				}
			}
		}
	}

	return root.Data.Playlist, true
}

// vibixDecodeAPIResponse decodes the encrypted embed API response {p, v}.
// Returns the decoded JSON bytes and true if the response was encrypted, or nil/false otherwise.
func vibixDecodeAPIResponse(body []byte) ([]byte, bool) {
	var enc vibixEncryptedResponse
	if err := stdjson.Unmarshal(body, &enc); err != nil || enc.P == "" {
		return nil, false
	}

	switch enc.V {
	case 0:
		// v=0: payload is plain JSON string
		return []byte(enc.P), true
	case 1:
		// v=1: reverse + base64 + XOR
		data := enc.P
		if vibixDecoderUseReverse {
			data = vibixReverseString(data)
		}
		// Pad base64 if needed
		if mod := len(data) % 4; mod != 0 {
			data += strings.Repeat("=", 4-mod)
		}
		decoded, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return nil, false
		}
		key := []byte(vibixDecoderKey)
		for i := range decoded {
			decoded[i] ^= key[i%len(key)]
		}
		return decoded, true
	default:
		return nil, false
	}
}

func vibixReverseString(s string) string {
	runes := []rune(s)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}

// vibixOriginFromIframeURL extracts the origin (scheme + host) from an iframe URL.
func vibixOriginFromIframeURL(iframeURL string) string {
	u, err := url.Parse(iframeURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// ---------------------------------------------------------------------------
// vibix iframe embed: serve an HTML page hosting vibix's own player.
//
// The page carries the rendex SDK + the publisher API's <ins> embed_code (which
// encodes OUR publisher id). The client loads this page in a webview/iframe; the
// SDK builds the vibix/Kinescope player, which runs its ads and counts the view
// against our publisher account — so /publisher/statistics + revenue register.
// rendex validates the embed's serving domain against the publisher account, so
// this only monetizes when THIS server's public host is registered and active in
// the vibix publisher dashboard.
// ---------------------------------------------------------------------------

// vibixRendexSDK is the rendex loader the publisher <ins> widget needs to build
// the player (same SDK the coldfilm resolve path drives, here under our id).
const vibixRendexSDK = "https://graphicslab.io/sdk/v2/rendex-sdk.min.js"

// vibixEmbedAttrRe extracts the known data-* attributes from the publisher API
// embed_code so we can rebuild a sanitized <ins> tag (never inject the raw API
// string into HTML).
var vibixEmbedAttrRe = regexp.MustCompile(`data-(publisher-id|type|id)="([A-Za-z0-9_-]+)"`)

var (
	vibixEmbedCache      sync.Map // key → *vibixEmbedEntry
	vibixJanitorOnce     sync.Once
	vibixEmbedCacheTTL   = 4 * time.Hour
	vibixM3U8CacheTTL    = 2 * time.Hour
	vibixJanitorInterval = 15 * time.Minute
)

type vibixEmbedEntry struct {
	embedCode string // sanitized data-* attributes for the <ins> widget
	title     string
	createdAt time.Time
}

// vibixSanitizeEmbedCode pulls publisher-id / type / id out of the API embed_code
// and re-emits them in a fixed order (values are restricted to [A-Za-z0-9_-]), so
// the string is safe to inject into the <ins> tag. Returns "" if the required
// publisher-id or id is missing.
func vibixSanitizeEmbedCode(embedCode string) string {
	found := make(map[string]string, 3)
	for _, m := range vibixEmbedAttrRe.FindAllStringSubmatch(embedCode, -1) {
		found[m[1]] = m[2]
	}
	if found["publisher-id"] == "" || found["id"] == "" {
		return ""
	}
	typ := found["type"]
	if typ == "" {
		typ = "movie"
	}
	return fmt.Sprintf(`data-publisher-id="%s" data-type="%s" data-id="%s"`, found["publisher-id"], typ, found["id"])
}

// vibixStartJanitor periodically sweeps expired entries from both vibix caches.
// Without this, orphaned entries (playback started but never requested) accumulate.
func vibixStartJanitor() {
	vibixJanitorOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(vibixJanitorInterval)
			defer ticker.Stop()
			for range ticker.C {
				now := time.Now()
				vibixEmbedCache.Range(func(k, v any) bool {
					if e, ok := v.(*vibixEmbedEntry); ok && now.Sub(e.createdAt) > vibixEmbedCacheTTL {
						vibixEmbedCache.Delete(k)
					}
					return true
				})
				vibixM3U8Cache.Range(func(k, v any) bool {
					if e, ok := v.(*vibixM3U8Entry); ok && now.Sub(e.createdAt) > vibixM3U8CacheTTL {
						vibixM3U8Cache.Delete(k)
					}
					return true
				})
			}
		}()
	})
}

// vibixEmbedURL caches the sanitized embed_code and returns a local
// /vibix_embed/{key} page URL for the client to embed. Returns "" when the
// embed_code has no usable publisher/id attributes.
func vibixEmbedURL(embedCode, title string, req *http.Request) string {
	sanitized := vibixSanitizeEmbedCode(embedCode)
	if sanitized == "" {
		return ""
	}
	vibixStartJanitor()
	key := vibixRandomHex(16)
	vibixEmbedCache.Store(key, &vibixEmbedEntry{
		embedCode: sanitized,
		title:     title,
		createdAt: time.Now(),
	})
	return hostFromRequest(req) + "/vibix_embed/" + key
}

// VibixEmbedHandler serves the full-screen vibix player page: the rendex SDK plus
// the <ins> widget carrying our publisher id. The client's webview loads it and
// vibix's player runs (and monetizes) client-side.
func VibixEmbedHandler(w http.ResponseWriter, req *http.Request) {
	key := strings.TrimPrefix(req.URL.Path, "/vibix_embed/")
	key = strings.TrimSuffix(key, "/")
	if key == "" {
		http.Error(w, "missing key", http.StatusBadRequest)
		return
	}
	val, ok := vibixEmbedCache.Load(key)
	if !ok {
		http.Error(w, "expired or unknown", http.StatusNotFound)
		return
	}
	entry := val.(*vibixEmbedEntry)
	if time.Since(entry.createdAt) > vibixEmbedCacheTTL {
		vibixEmbedCache.Delete(key)
		http.Error(w, "expired", http.StatusGone)
		return
	}
	ins := vibixSanitizeEmbedCode(entry.embedCode)
	if ins == "" {
		http.Error(w, "bad embed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="ru"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,maximum-scale=1">
<title>%s</title>
<script src="%s"></script>
<style>
*{margin:0;padding:0;box-sizing:border-box}
html,body{width:100%%;height:100%%;overflow:hidden;background:#000}
ins,iframe{display:block;width:100%%;height:100%%;border:none}
</style>
</head><body>
<ins %s></ins>
</body></html>`, html.EscapeString(entry.title), vibixRendexSDK, ins)
}

// ---------------------------------------------------------------------------
// vibix m3u8 Chrome-proxy: river-* m3u8 → Chrome fetch → rewrite vbx-* → serve
// ---------------------------------------------------------------------------

type vibixM3U8Entry struct {
	riverURL     string
	iframeOrigin string
	proxyLinks   *proxylink.Manager
	createdAt    time.Time
}

func vibixRandomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// vibixStreamOrigin returns the scheme://host origin of a stream URL.
func vibixStreamOrigin(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// vibixM3U8URL creates a cache entry for the river-* m3u8 URL and returns
// a local /vibix_m3u8/{key} URL that the player will request.
func vibixM3U8URL(riverURL string, req *http.Request, links *proxylink.Manager, iframeOrigin string) string {
	vibixStartJanitor()
	// The Kinescope CDN validates Origin/Referer against the stream's own host,
	// so when the caller didn't supply an origin (coldfilm/rendex path) derive it
	// from the stream URL itself.
	if strings.TrimSpace(iframeOrigin) == "" {
		iframeOrigin = vibixStreamOrigin(riverURL)
	}
	key := vibixRandomHex(16)
	vibixM3U8Cache.Store(key, &vibixM3U8Entry{
		riverURL:     riverURL,
		iframeOrigin: iframeOrigin,
		proxyLinks:   links,
		createdAt:    time.Now(),
	})
	return hostFromRequest(req) + "/vibix_m3u8/" + key
}

// VibixM3U8Handler handles /vibix_m3u8/{key} requests. It fetches the river-*
// m3u8 directly (uTLS Chrome fingerprint, from the same server IP the browser
// resolved the IP-bound sign with), decodes the {p,v} XOR envelope when
// present, rewrites vbx-* segment URLs through /proxy/, and serves the result.
func VibixM3U8Handler(w http.ResponseWriter, req *http.Request) {
	key := strings.TrimPrefix(req.URL.Path, "/vibix_m3u8/")
	key = strings.TrimSuffix(key, "/")
	if key == "" {
		http.Error(w, "missing key", http.StatusBadRequest)
		return
	}

	val, ok := vibixM3U8Cache.Load(key)
	if !ok {
		http.Error(w, "expired or unknown key", http.StatusNotFound)
		return
	}
	entry := val.(*vibixM3U8Entry)

	// Expire old entries.
	if time.Since(entry.createdAt) > 2*time.Hour {
		vibixM3U8Cache.Delete(key)
		http.Error(w, "expired", http.StatusGone)
		return
	}

	m3u8Content, fetchOK := vibixFetchM3U8Direct(req, entry.riverURL, entry.iframeOrigin)
	if !fetchOK {
		http.Error(w, "failed to fetch m3u8", http.StatusBadGateway)
		return
	}

	// Rewrite segment URLs through /proxy/ with CDN origin/referer headers.
	rewritten := vibixRewriteM3U8(m3u8Content, req, entry.proxyLinks, entry.iframeOrigin)

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(rewritten))
}

// vibixFetchM3U8Direct fetches a river-* m3u8 URL with a Chrome-like uTLS
// fingerprint (direct — same IP as the resolving browser, so the IP-bound
// sign stays valid), decoding the {p,v} XOR envelope if the CDN returns it.
func vibixFetchM3U8Direct(req *http.Request, streamURL, origin string) (string, bool) {
	streamURL = strings.TrimSpace(streamURL)
	if streamURL == "" {
		return "", false
	}
	if origin == "" {
		origin = vibixStreamOrigin(streamURL)
	}

	httpReq, err := http.NewRequestWithContext(req.Context(), http.MethodGet, streamURL, nil)
	if err != nil {
		return "", false
	}
	httpReq.Header.Set("accept", "*/*")
	httpReq.Header.Set("accept-language", "ru-RU,ru;q=0.9,en-US;q=0.8,en;q=0.7")
	httpReq.Header.Set("origin", origin)
	httpReq.Header.Set("referer", origin+"/")
	httpReq.Header.Set("sec-fetch-dest", "empty")
	httpReq.Header.Set("sec-fetch-mode", "cors")
	httpReq.Header.Set("sec-fetch-site", "same-site")
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")

	client := httpclient.NewUTLS(15 * time.Second)
	resp, err := client.Do(httpReq)
	if err != nil {
		log.Warn().Err(err).Msg("vibix: m3u8 direct fetch failed")
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Warn().Int("status", resp.StatusCode).Msg("vibix: m3u8 direct non-2xx")
		return "", false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", false
	}

	// The stream m3u8 may be wrapped in the same {p,v} XOR envelope as the embed
	// API. Decode it when present; otherwise it's already plain m3u8 text.
	if decoded, ok := vibixDecodeAPIResponse(body); ok {
		body = decoded
	}

	text := string(body)
	if !strings.Contains(text, "#EXTM3U") {
		log.Warn().Int("len", len(text)).Msg("vibix: m3u8 direct got non-m3u8 response")
		return "", false
	}
	return text, true
}

// vibixRewriteM3U8 rewrites absolute segment URLs in the m3u8 through /proxy/.
func vibixRewriteM3U8(m3u8 string, req *http.Request, links *proxylink.Manager, iframeOrigin string) string {
	if links == nil {
		return m3u8
	}
	host := hostFromRequest(req)
	ip := clientIP(req)
	hdrs := map[string]string{
		"Origin":  iframeOrigin,
		"Referer": iframeOrigin + "/",
	}
	return vibixM3U8SegmentRe.ReplaceAllStringFunc(m3u8, func(line string) string {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "http") {
			return line
		}
		return host + "/proxy/" + links.EncryptURIWithHeaders(line, ip, "vibix", hdrs)
	})
}

// vibixProxyStream wraps a stream URL through /proxy/ with CDN headers.
func vibixProxyStream(streamURL string, req *http.Request, links *proxylink.Manager, iframeOrigin string) string {
	if links == nil || streamURL == "" {
		return streamURL
	}
	hdrs := map[string]string{
		"Origin":  iframeOrigin,
		"Referer": iframeOrigin + "/",
	}
	return hostFromRequest(req) + "/proxy/" + links.EncryptURIWithHeaders(streamURL, clientIP(req), "vibix", hdrs)
}

func (v *vibixChecker) writeMovie(w http.ResponseWriter, req *http.Request, rjson bool, title, originalTitle string, playlist []vibixSeason, proxyLinks *proxylink.Manager, iframeOrigin string) {
	baseTitle := getsTVJoinName(title, originalTitle)
	data := make([]map[string]any, 0, len(playlist))
	labels := make([]string, 0, len(playlist))

	for _, movie := range playlist {
		groups := vibixParseVoiceGroups(movie.File)
		if len(groups) == 0 {
			continue
		}

		for _, g := range groups {
			streams := g.streams

			// Route river-* m3u8 URLs through /vibix_m3u8/ — the handler fetches
			// the m3u8 (uTLS, same server IP the browser resolved the sign with),
			// decodes it and rewrites vbx-* segment URLs through /proxy/.
			for _, s := range streams {
				s["url"] = vibixM3U8URL(s["url"], req, proxyLinks, iframeOrigin)
			}

			first := streams[0]
			name := strings.TrimSpace(g.voice)
			if name == "" {
				name = strings.TrimSpace(movie.Title)
			}
			if name == "" {
				name = "По умолчанию"
			}
			row := map[string]any{
				"method":        "play",
				"url":           first["url"],
				"stream":        first["url"],
				"name":          name,
				"title":         baseTitle + " (" + name + ")",
				"streamquality": streams,
			}
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
	for i, row := range data {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, 0, 0)
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

func (v *vibixChecker) writeSerial(
	w http.ResponseWriter,
	req *http.Request,
	rjson bool,
	imdbID string,
	kinopoiskID int64,
	title string,
	originalTitle string,
	s int,
	playlist []vibixSeason,
	proxyLinks *proxylink.Manager,
	iframeOrigin string,
) {
	host := hostFromRequest(req)
	encTitle := url.QueryEscape(title)
	encOriginal := url.QueryEscape(originalTitle)

	if s == -1 {
		type seasonRow struct {
			id int
		}
		rows := make([]seasonRow, 0, len(playlist))
		for _, season := range playlist {
			id, ok := vibixSeasonNumber(season.Title)
			if !ok {
				continue
			}
			rows = append(rows, seasonRow{id: id})
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].id < rows[j].id })
		if len(rows) == 0 {
			writeGetsTVEmpty(w, rjson)
			return
		}

		data := make([]map[string]any, 0, len(rows))
		labels := make([]string, 0, len(rows))
		for _, row := range rows {
			name := strconv.Itoa(row.id) + " сезон"
			link := host + "/lite/vibix?rjson=" + getsTVBool(rjson) +
				"&kinopoisk_id=" + strconv.FormatInt(kinopoiskID, 10) +
				"&imdb_id=" + url.QueryEscape(imdbID) +
				"&title=" + encTitle +
				"&original_title=" + encOriginal +
				"&s=" + strconv.Itoa(row.id)
			data = append(data, map[string]any{
				"method": "link",
				"id":     row.id,
				"url":    link,
				"name":   name,
			})
			labels = append(labels, name)
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

	var seasonNode *vibixSeason
	for i := range playlist {
		if id, ok := vibixSeasonNumber(playlist[i].Title); ok && id == s {
			seasonNode = &playlist[i]
			break
		}
	}
	if seasonNode == nil || len(seasonNode.Folder) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	baseTitle := getsTVJoinName(title, originalTitle)
	type episodeRow struct {
		data  map[string]any
		label string
		num   int
	}
	episodes := make([]episodeRow, 0, len(seasonNode.Folder))
	for _, ep := range seasonNode.Folder {
		name := strings.TrimSpace(ep.Title)
		if name == "" {
			continue
		}

		file := strings.TrimSpace(ep.File)
		if file == "" && len(ep.Folder) > 0 {
			file = strings.TrimSpace(ep.Folder[0].File)
		}
		streams := vibixParseStreams(file)
		if len(streams) == 0 {
			continue
		}

		// Route river-* m3u8 through /vibix_m3u8/ (Chrome DIRECT fetches, rewrites vbx-*).
		for _, s := range streams {
			s["url"] = vibixM3U8URL(s["url"], req, proxyLinks, iframeOrigin)
		}

		first := streams[0]
		epNum, _ := vibixEpisodeNumber(name)
		row := map[string]any{
			"method":        "play",
			"url":           first["url"],
			"stream":        first["url"],
			"s":             s,
			"e":             epNum,
			"name":          name,
			"title":         baseTitle + " (" + name + ")",
			"streamquality": streams,
		}
		episodes = append(episodes, episodeRow{data: row, label: name, num: epNum})
	}
	sort.Slice(episodes, func(i, j int) bool {
		if episodes[i].num == episodes[j].num {
			return episodes[i].label < episodes[j].label
		}
		return episodes[i].num < episodes[j].num
	})
	if len(episodes) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	data := make([]map[string]any, 0, len(episodes))
	labels := make([]string, 0, len(episodes))
	seasons := make([]int, 0, len(episodes))
	episodeNums := make([]int, 0, len(episodes))
	for _, ep := range episodes {
		data = append(data, ep.data)
		labels = append(labels, ep.label)
		seasons = append(seasons, s)
		episodeNums = append(episodeNums, ep.num)
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "episode",
			"data": data,
		})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range data {
		getsTVAppendMovieHTML(&sb, row, labels[i], i == 0, seasons[i], episodeNums[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// vibixParsePlayerFile parses the PlayerJS `#playerjsfile` playlist read from
// the Kinescope iframe. It is either a JSON array of items (serial: items with
// `folder`; movie: one item with `file`) or, as a fallback, a bare movie
// `file` string.
func vibixParsePlayerFile(s string) ([]vibixSeason, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, false
	}
	if strings.HasPrefix(s, "[") {
		var pl []vibixSeason
		if err := stdjson.Unmarshal([]byte(s), &pl); err == nil && len(pl) > 0 {
			return pl, true
		}
	}
	if strings.Contains(s, "http") {
		return []vibixSeason{{Title: "", File: s}}, true
	}
	return nil, false
}

// vibixVoiceGroup is one translation (озвучка) with its per-quality streams.
type vibixVoiceGroup struct {
	voice   string
	streams []map[string]string
}

var (
	// vibixQualityBlockRe splits a movie `file` into per-quality blocks:
	//   [1080p]{VoiceA}urlA{VoiceB}urlB,[720p]{VoiceA}urlC...
	// items run up to the next quality bracket (URLs never contain '[').
	vibixQualityBlockRe = regexp.MustCompile(`\[(480|720|1080)p?\]([^\[]*)`)
	// vibixVoicePairRe matches a {voice}url pair inside a quality block.
	vibixVoicePairRe = regexp.MustCompile(`\{([^}]+)\}(https?://[^,\t\[\;{ ]+)`)
	// vibixBareURLRe matches a voiceless url inside a quality block.
	vibixBareURLRe = regexp.MustCompile(`(https?://[^,\t\[\;{ ]+)`)
)

func vibixQualityRank(q string) int {
	switch q {
	case "1080":
		return 1080
	case "720":
		return 720
	case "480":
		return 480
	default:
		return 0
	}
}

// vibixParseVoiceGroups parses a movie `file` into per-voice quality lists.
// Format: [1080p]{Voice}url... with voices grouped under each quality. Files
// without {voice} markers collapse into a single default group (voice "").
func vibixParseVoiceGroups(file string) []vibixVoiceGroup {
	file = strings.TrimSpace(file)
	if file == "" {
		return nil
	}

	type qURL struct {
		q, url string
	}
	order := make([]string, 0, 3)         // voice order of first appearance
	byVoice := make(map[string][]qURL, 3) // voice → quality/url list
	seen := make(map[string]struct{}, 6)  // voice|quality dedup (first wins)

	add := func(voice, q, u string) {
		if strings.HasPrefix(u, "//") {
			u = "https:" + u
		}
		key := voice + "|" + q
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		if _, ok := byVoice[voice]; !ok {
			order = append(order, voice)
		}
		byVoice[voice] = append(byVoice[voice], qURL{q: q, url: u})
	}

	for _, block := range vibixQualityBlockRe.FindAllStringSubmatch(file, -1) {
		q := strings.TrimSpace(block[1])
		items := block[2]
		pairs := vibixVoicePairRe.FindAllStringSubmatch(items, -1)
		if len(pairs) > 0 {
			for _, p := range pairs {
				voice := strings.TrimSpace(p[1])
				add(voice, q, strings.TrimSpace(p[2]))
			}
			continue
		}
		if u := vibixBareURLRe.FindString(items); u != "" {
			add("", q, strings.TrimSpace(u))
		}
	}

	if len(order) == 0 {
		return nil
	}

	groups := make([]vibixVoiceGroup, 0, len(order))
	for _, voice := range order {
		list := byVoice[voice]
		sort.SliceStable(list, func(i, j int) bool {
			return vibixQualityRank(list[i].q) > vibixQualityRank(list[j].q)
		})
		streams := make([]map[string]string, 0, len(list))
		for _, s := range list {
			streams = append(streams, map[string]string{"url": s.url, "quality": s.q + "p"})
		}
		if len(streams) > 0 {
			groups = append(groups, vibixVoiceGroup{voice: voice, streams: streams})
		}
	}
	return groups
}

func vibixParseStreams(file string) []map[string]string {
	file = strings.TrimSpace(file)
	if file == "" {
		return nil
	}
	type stream struct {
		url     string
		quality string
		rank    int
	}

	tmp := make([]stream, 0, 3)
	seen := make(map[string]struct{}, 3)
	for _, m := range vibixStreamRe.FindAllStringSubmatch(file, -1) {
		if len(m) != 3 {
			continue
		}
		q := strings.TrimSpace(m[1])
		u := strings.TrimSpace(m[2])
		if q == "" || u == "" {
			continue
		}
		if strings.HasPrefix(u, "//") {
			u = "https:" + u
		}
		// Dedup by quality — the first url per quality wins (a title may carry
		// several voices, but the quality picker should list each quality once).
		if _, ok := seen[q]; ok {
			continue
		}
		seen[q] = struct{}{}
		tmp = append(tmp, stream{
			url:     u,
			quality: q + "p",
			rank:    vibixQualityRank(q),
		})
	}
	sort.Slice(tmp, func(i, j int) bool { return tmp[i].rank > tmp[j].rank })
	if len(tmp) == 0 {
		return nil
	}
	out := make([]map[string]string, 0, len(tmp))
	for _, s := range tmp {
		out = append(out, map[string]string{
			"url":     s.url,
			"quality": s.quality,
		})
	}
	return out
}

func vibixSeasonNumber(title string) (int, bool) {
	title = strings.TrimSpace(title)
	m := regexp.MustCompile(`([0-9]+)$`).FindStringSubmatch(title)
	if len(m) != 2 {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

func vibixEpisodeNumber(title string) (int, bool) {
	title = strings.TrimSpace(title)
	m := regexp.MustCompile(`([0-9]+)`).FindStringSubmatch(title)
	if len(m) != 2 {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

func (v *vibixChecker) probe(req *http.Request, method, target string) bool {
	httpReq, err := http.NewRequestWithContext(req.Context(), method, target, nil)
	if err != nil {
		return false
	}
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")
	httpReq.Header.Set("Accept", "text/html,application/json,*/*")
	httpReq.Header.Set("X-Lampac-Go", "1")

	resp, err := v.client.Do(httpReq)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if method == http.MethodHead && resp.StatusCode == http.StatusMethodNotAllowed {
		return false
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return false
	}
	if method == http.MethodHead {
		return true
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(body))) > 0
}
