package litesrc

// ahuerezka — second, independent HDRezka balancer.
//
// Unlike the legacy "rezka" (PidoRezka) balancer, which scrapes an HDRezka
// mirror directly (browser pool, auto-login, captcha, base64 trash decoding),
// AhueRezka talks to a Cloudflare-Worker proxy (rezka.hdbase.workers.dev) that
// is keyed by *Kinopoisk ID* and returns clean JSON:
//
//	GET /info?id={kp}                                  -> translators + hasSeasons
//	GET /episodes?id={kp}&translator_id={t}            -> seasons + episodes
//	GET /movie-stream?id={kp}&translator_id={t}        -> "[q]url or url,..." qualities
//	GET /episode-stream?id={kp}&translator_id={t}&s&e  -> same qualities string
//
// The qualities string is byte-for-byte the native HDRezka format (including the
// premium "<span ...>1080p Ultra/2K/4K<img></span>" labels), so we reuse the
// proven pidorezka* quality primitives to turn it into Lampa streams.
//
// NOTE on quality: the worker has no real premium HDRezka account, so the
// premium tiers (1080p Ultra / 2K / 4K) all resolve to a short "buy premium"
// stub clip. We therefore default premium=false and serve real content up to
// 1080p. Only enable premium if the configured worker proxies a premium account.
//
// A companion worker (kp.hdbase.workers.dev) resolves a Kinopoisk ID from a
// title when Lampa does not send kinopoisk_id (rare for RU content):
//
//	GET /search?q={title} -> {films:[{filmId,nameRu,nameEn,year}]}
//
// IMPORTANT: this file must NOT change the behaviour of rezka.go. It only reuses
// package-level helpers (pidorezkaQualities, pidorezkaExtractQualityURL,
// pidorezkaExtractAnyURLs, fixVoidboostURL, pidorezkaParseSubtitles) and the
// shared getsTV*/write* response writers.

import (
	"context"
	"crypto/tls"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/proxyapi"
	"lampac-go/internal/proxylink"

	"github.com/rs/zerolog/log"
	"golang.org/x/net/proxy"
)

const (
	ahueRezkaDefaultHost   = "https://rezka.hdbase.workers.dev"
	ahueRezkaDefaultKPHost = "https://kp.hdbase.workers.dev"
	ahueRezkaUA            = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
)

type ahueRezkaChecker struct {
	client       *http.Client
	clientDirect *http.Client // no-SOCKS twin — fallback for the globally-reachable kp worker
	host         string       // primary worker base, e.g. https://rezka.hdbase.workers.dev
	kpHost       string       // primary kp worker, e.g. https://kp.hdbase.workers.dev
	hosts        []string     // failover-ordered worker mirrors (primary first)
	kpHosts      []string     // failover-ordered kp worker mirrors (primary first)
	premium      bool         // include premium qualities (1080p Ultra / 2K / 4K)
	hls          bool         // append :hls:manifest.m3u8 when a quality URL lacks .m3u8
	links        *proxylink.Manager
}

// Sticky mirror selection, same shape as kinopub's: stay on one host until it
// fails at the TRANSPORT level, then advance. A live host answering 404/5xx is a
// real answer about the content, not a dead mirror — rotating on that would
// silently walk off a working worker.
var (
	ahueRezkaHostIdx   atomic.Int32
	ahueRezkaKPHostIdx atomic.Int32
)

// ahueRezkaErrTransport marks a host that could not be reached at all (DNS, TCP,
// TLS, timeout), as opposed to one that answered with an HTTP error status.
var ahueRezkaErrTransport = errors.New("ahuerezka: transport failure")

// ahueRezkaHostList builds the failover order: primary first, then the
// configured spares, then the built-in default. Blanks and duplicates dropped.
func ahueRezkaHostList(primary string, extra []string, fallback string) []string {
	seen := make(map[string]bool, len(extra)+2)
	out := make([]string, 0, len(extra)+2)
	add := func(h string) {
		h = strings.TrimRight(strings.TrimSpace(h), "/")
		if h == "" || seen[h] {
			return
		}
		seen[h] = true
		out = append(out, h)
	}
	add(primary)
	for _, h := range extra {
		add(h)
	}
	add(fallback)
	return out
}

func ahueRezkaPickHost(hosts []string, idx *atomic.Int32, fallback string) string {
	if len(hosts) == 0 {
		return fallback
	}
	return hosts[int(idx.Load())%len(hosts)]
}

// ahueRezkaAdvanceHost rotates past a dead mirror. The CAS guard means that when
// several requests see the same host fail at once, only one of them advances —
// otherwise a burst of failures would skip over healthy mirrors.
func ahueRezkaAdvanceHost(hosts []string, idx *atomic.Int32, failed, label string) {
	if len(hosts) < 2 {
		return
	}
	cur := idx.Load()
	if hosts[int(cur)%len(hosts)] != failed {
		return // someone already advanced past it
	}
	if idx.CompareAndSwap(cur, cur+1) {
		log.Warn().Str("failed", failed).Str("next", hosts[int(cur+1)%len(hosts)]).
			Msg("ahuerezka: " + label + " mirror failover")
	}
}

// hlsEnabled reads the `hls` flag from the LIVE config rather than the copy
// captured when this checker was built.
//
// The checker is constructed once, at route registration (liteSourceHandler is
// called from the server's route setup with cfg by value). Reload() swaps the
// config pointer but does NOT rebuild the router, so a flag read from a.hls can
// never change without a full process restart — editing config.toml and sending
// SIGHUP appeared to do nothing. /lite/events re-reads liveConfig per request,
// which is why the source list DID update on reload while this flag did not.
// serverReady() guards the unwired case (direct-handler unit tests), where the
// value captured at construction is the only one available.
func (a *ahueRezkaChecker) hlsEnabled() bool {
	if !serverReady() {
		return a.hls
	}
	return liveConfig(config.Config{}).Online.AhueRezka.HLS
}

// premiumEnabled reads `premium` live too, for the same reason as hlsEnabled:
// a value captured in the constructor cannot change without a full restart.
func (a *ahueRezkaChecker) premiumEnabled() bool {
	if !serverReady() {
		return a.premium
	}
	return liveConfig(config.Config{}).Online.AhueRezka.Premium
}

func (a *ahueRezkaChecker) activeHost() string {
	return ahueRezkaPickHost(a.hosts, &ahueRezkaHostIdx, a.host)
}

func (a *ahueRezkaChecker) activeKPHost() string {
	return ahueRezkaPickHost(a.kpHosts, &ahueRezkaKPHostIdx, a.kpHost)
}

// ---------------------------------------------------------------------------
// Worker JSON shapes
// ---------------------------------------------------------------------------

type ahueRezkaInfo struct {
	Translators []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"translators"`
	DefaultTranslator string `json:"defaultTranslator"`
	HasSeasons        bool   `json:"hasSeasons"`
}

type ahueRezkaEpisodes struct {
	Seasons []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"seasons"`
	Episodes map[string][]struct {
		ID    int    `json:"id"`
		Title string `json:"title"`
	} `json:"episodes"`
}

type ahueRezkaStream struct {
	Stream     string             `json:"stream"`
	Thumbnails string             `json:"thumbnails"`
	Subtitle   stdjson.RawMessage `json:"subtitle"`
}

type ahueRezkaKPSearch struct {
	Films []struct {
		FilmID int64  `json:"filmId"`
		NameRu string `json:"nameRu"`
		NameEn string `json:"nameEn"`
		Year   string `json:"year"`
	} `json:"films"`
}

// ---------------------------------------------------------------------------
// Construction
// ---------------------------------------------------------------------------

func NewAhueRezkaChecker(cfg config.Config) *ahueRezkaChecker {
	src := cfg.Online.AhueRezka

	host := strings.TrimRight(strings.TrimSpace(src.Host), "/")
	if host == "" {
		host = ahueRezkaDefaultHost
	}
	kpHost := strings.TrimRight(strings.TrimSpace(src.KpHost), "/")
	if kpHost == "" {
		kpHost = ahueRezkaDefaultKPHost
	}

	r := &ahueRezkaChecker{
		host:    host,
		kpHost:  kpHost,
		hosts:   ahueRezkaHostList(host, src.Hosts, ahueRezkaDefaultHost),
		kpHosts: ahueRezkaHostList(kpHost, src.KpHosts, ahueRezkaDefaultKPHost),
		premium: src.Premium,
		hls:     src.HLS,
	}

	r.client = &http.Client{Timeout: 25 * time.Second}
	r.clientDirect = &http.Client{Timeout: 25 * time.Second} // never proxied — kp-worker fallback

	// Optional SOCKS5 egress (the worker itself is globally reachable, but this
	// future-proofs the rare case where the upstream needs RU egress).
	if socksAddr := strings.TrimSpace(src.SocksProxy); socksAddr != "" {
		if dialer, err := proxy.SOCKS5("tcp", socksAddr, nil, proxy.Direct); err == nil {
			t := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
			if cd, ok := dialer.(proxy.ContextDialer); ok {
				t.DialContext = cd.DialContext
			}
			r.client.Transport = t
			log.Info().Str("socks", socksAddr).Msg("ahuerezka: HTTP client using SOCKS5 proxy")
		} else {
			log.Warn().Err(err).Str("socks", socksAddr).Msg("ahuerezka: failed to create SOCKS5 dialer")
		}
	}

	// Voidboost stream CDN expects an HDRezka referer/origin. The proxy handler
	// applies these to every /proxy/<url>?pl=ahuerezka fetch.
	proxyapi.RegisterPluginHeaderProvider("ahuerezka", ahueRezkaStreamHeaders)

	return r
}

// ahueRezkaIsPremiumTier reports whether a pidorezkaQualities key is a premium
// (paid-account) HDRezka tier. Via the hdbase worker these always resolve to a
// short "buy premium" stub, so they're gated behind the premium config flag.
func ahueRezkaIsPremiumTier(key string) bool {
	switch key {
	case "1080p Ultra", "1440p", "2160p":
		return true
	default:
		return false
	}
}

func ahueRezkaStreamHeaders() map[string]string {
	return map[string]string{
		"user-agent": ahueRezkaUA,
		"referer":    "https://hdrezka.me/",
		"origin":     "https://hdrezka.me",
		"accept":     "*/*",
	}
}

// ---------------------------------------------------------------------------
// HTTP entry point
// ---------------------------------------------------------------------------

func (a *ahueRezkaChecker) Handle(cfg config.Config, plugin string, proxyLinks *proxylink.Manager) http.HandlerFunc {
	a.links = proxyLinks

	return func(w http.ResponseWriter, req *http.Request) {
		if parseBoolParam(req.URL.Query().Get("checksearch")) {
			show := a.checkSearch(req)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			if show {
				if quality := pluginQualityBadgeGet(plugin); quality != "" {
					_, _ = fmt.Fprintf(w, `{"type":"movie","rch":true,"quality":"%s"}`, quality)
				} else {
					_, _ = w.Write([]byte(`{"rch":true}`))
				}
				return
			}
			_, _ = w.Write([]byte(`{"rch":false}`))
			return
		}

		raw := strings.TrimPrefix(strings.ToLower(req.URL.Path), "/lite/")
		raw = strings.TrimPrefix(raw, "rc/")

		switch {
		case strings.HasPrefix(raw, "ahuerezka/movie"):
			a.movie(w, req, plugin, proxyLinks)
		case strings.HasPrefix(raw, "ahuerezka/serial"):
			a.serial(w, req, plugin, proxyLinks)
		default:
			a.embed(w, req, plugin, proxyLinks)
		}
	}
}

// ---------------------------------------------------------------------------
// checkSearch — does the worker have any translator for this Kinopoisk ID?
// ---------------------------------------------------------------------------

func (a *ahueRezkaChecker) checkSearch(req *http.Request) bool {
	kp := a.resolveKPID(req)
	if kp == "" {
		return false
	}
	info, ok := a.fetchInfo(req.Context(), kp)
	if !ok {
		return false
	}
	return len(info.Translators) > 0
}

// ---------------------------------------------------------------------------
// embed — translator (voice) picker
// ---------------------------------------------------------------------------

func (a *ahueRezkaChecker) embed(w http.ResponseWriter, req *http.Request, plugin string, _ *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	seasonParam := strings.TrimSpace(q.Get("s"))

	kp := a.resolveKPID(req)
	if kp == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	info, ok := a.fetchInfo(req.Context(), kp)
	if !ok || len(info.Translators) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(req)
	encTitle := url.QueryEscape(title)
	encOriginal := url.QueryEscape(originalTitle)
	encKP := url.QueryEscape(kp)

	data := make([]map[string]any, 0, len(info.Translators))
	labels := make([]string, 0, len(info.Translators))

	for _, tr := range info.Translators {
		name := strings.TrimSpace(tr.Name)
		if name == "" {
			name = "Оригинал"
		}

		var link, method string
		if info.HasSeasons {
			method = "link"
			link = fmt.Sprintf("%s/lite/%s/serial?title=%s&original_title=%s&kinopoisk_id=%s&t=%s",
				host, plugin, encTitle, encOriginal, encKP, url.QueryEscape(tr.ID))
			if seasonParam != "" {
				link += "&s=" + url.QueryEscape(seasonParam)
			}
			if rjson {
				link += "&rjson=true"
			}
		} else {
			method = "call"
			link = fmt.Sprintf("%s/lite/%s/movie?title=%s&original_title=%s&kinopoisk_id=%s&t=%s&voice_name=%s&rjson=true&call=true",
				host, plugin, encTitle, encOriginal, encKP, url.QueryEscape(tr.ID), url.QueryEscape(name))
		}

		data = append(data, map[string]any{
			"method": method,
			"url":    link,
			"name":   name,
			"active": tr.ID == info.DefaultTranslator,
		})
		labels = append(labels, name)
	}

	if rjson {
		writeJSON(w, http.StatusOK, map[string]any{"type": "voice", "data": data})
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
// serial — seasons / episodes navigation
// ---------------------------------------------------------------------------

func (a *ahueRezkaChecker) serial(w http.ResponseWriter, req *http.Request, plugin string, _ *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	t := strings.TrimSpace(q.Get("t"))
	s, sOK := getsTVQueryInt(q.Get("s"))

	kp := a.resolveKPID(req)
	if kp == "" || t == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	eps, ok := a.fetchEpisodes(req.Context(), kp, t)
	if !ok || len(eps.Seasons) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	host := hostFromRequest(req)
	encTitle := url.QueryEscape(title)
	encOriginal := url.QueryEscape(originalTitle)
	encKP := url.QueryEscape(kp)

	// Voice (translator) selector — only shown when more than one exists.
	voiceURLs := a.buildVoiceList(req, host, plugin, kp, t, s, sOK, rjson, encTitle, encOriginal, encKP)

	// No season selected — show the season list.
	if !sOK || s <= 0 {
		sData := make([]map[string]any, 0, len(eps.Seasons))
		sLabels := make([]string, 0, len(eps.Seasons))
		for _, se := range eps.Seasons {
			name := strings.TrimSpace(se.Name)
			if name == "" {
				name = fmt.Sprintf("Сезон %d", se.ID)
			}
			link := fmt.Sprintf("%s/lite/%s/serial?title=%s&original_title=%s&kinopoisk_id=%s&t=%s&s=%d",
				host, plugin, encTitle, encOriginal, encKP, url.QueryEscape(t), se.ID)
			if rjson {
				link += "&rjson=true"
			}
			sData = append(sData, map[string]any{"method": "link", "url": link, "name": name})
			sLabels = append(sLabels, name)
		}

		if rjson {
			payload := map[string]any{"type": "season", "data": sData}
			if len(voiceURLs) > 0 {
				payload["voice"] = voiceURLs
			}
			writeJSON(w, http.StatusOK, payload)
			return
		}

		var sb strings.Builder
		if len(voiceURLs) > 0 {
			sb.WriteString(`<div class="videos__line">`)
			for _, row := range voiceURLs {
				getsTVAppendVoiceHTML(&sb, row)
			}
			sb.WriteString(`</div>`)
		}
		sb.WriteString(`<div class="videos__line">`)
		for i, row := range sData {
			getsTVAppendSeasonHTML(&sb, row, sLabels[i], i == 0)
		}
		sb.WriteString(`</div>`)
		writeHTML(w, http.StatusOK, sb.String())
		return
	}

	// Season selected — show its episodes.
	baseTitle := getsTVJoinName(title, originalTitle)
	list := eps.Episodes[strconv.Itoa(s)]

	eData := make([]map[string]any, 0, len(list))
	eLabels := make([]string, 0, len(list))
	eNums := make([]int, 0, len(list))

	for _, ep := range list {
		epName := strings.TrimSpace(ep.Title)
		if epName == "" {
			epName = fmt.Sprintf("Серия %d", ep.ID)
		}
		link := fmt.Sprintf("%s/lite/%s/movie?title=%s&original_title=%s&kinopoisk_id=%s&t=%s&s=%d&e=%d&voice_name=%s&rjson=true&call=true",
			host, plugin, encTitle, encOriginal, encKP, url.QueryEscape(t), s, ep.ID, url.QueryEscape(epName))

		eData = append(eData, map[string]any{
			"method": "call",
			"url":    link,
			"s":      s,
			"e":      ep.ID,
			"name":   epName,
			"title":  fmt.Sprintf("%s (%s)", baseTitle, epName),
		})
		eLabels = append(eLabels, epName)
		eNums = append(eNums, ep.ID)
	}

	if len(eData) == 0 {
		writeGetsTVEmpty(w, rjson)
		return
	}

	if rjson {
		payload := map[string]any{"type": "episode", "data": eData}
		if len(voiceURLs) > 0 {
			payload["voice"] = voiceURLs
		}
		writeJSON(w, http.StatusOK, payload)
		return
	}

	var sb strings.Builder
	if len(voiceURLs) > 0 {
		sb.WriteString(`<div class="videos__line">`)
		for _, row := range voiceURLs {
			getsTVAppendVoiceHTML(&sb, row)
		}
		sb.WriteString(`</div>`)
	}
	sb.WriteString(`<div class="videos__line">`)
	for i, row := range eData {
		getsTVAppendMovieHTML(&sb, row, eLabels[i], i == 0, s, eNums[i])
	}
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// buildVoiceList returns one navigation row per translator (re-entering /serial
// with a different t), or nil when there is only one translator.
func (a *ahueRezkaChecker) buildVoiceList(req *http.Request, host, plugin, kp, currentT string, s int, sOK, rjson bool, encTitle, encOriginal, encKP string) []map[string]any {
	info, ok := a.fetchInfo(req.Context(), kp)
	if !ok || len(info.Translators) < 2 {
		return nil
	}

	out := make([]map[string]any, 0, len(info.Translators))
	for _, tr := range info.Translators {
		name := strings.TrimSpace(tr.Name)
		if name == "" {
			name = "Оригинал"
		}
		link := fmt.Sprintf("%s/lite/%s/serial?title=%s&original_title=%s&kinopoisk_id=%s&t=%s",
			host, plugin, encTitle, encOriginal, encKP, url.QueryEscape(tr.ID))
		if sOK && s > 0 {
			link += "&s=" + strconv.Itoa(s)
		}
		if rjson {
			link += "&rjson=true"
		}
		out = append(out, map[string]any{
			"method": "link",
			"name":   name,
			"active": tr.ID == currentT,
			"url":    link,
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// movie — resolve the playable stream
// ---------------------------------------------------------------------------

func (a *ahueRezkaChecker) movie(w http.ResponseWriter, req *http.Request, plugin string, _ *proxylink.Manager) {
	q := req.URL.Query()
	rjson := parseBoolParam(q.Get("rjson"))
	title := strings.TrimSpace(q.Get("title"))
	originalTitle := strings.TrimSpace(q.Get("original_title"))
	t := strings.TrimSpace(q.Get("t"))
	s, _ := getsTVQueryInt(q.Get("s"))
	e, _ := getsTVQueryInt(q.Get("e"))

	kp := a.resolveKPID(req)
	if kp == "" || t == "" {
		writeGetsTVEmpty(w, rjson)
		return
	}

	var path string
	if s > 0 && e > 0 {
		path = fmt.Sprintf("/episode-stream?id=%s&translator_id=%s&s=%d&e=%d",
			url.QueryEscape(kp), url.QueryEscape(t), s, e)
	} else {
		path = fmt.Sprintf("/movie-stream?id=%s&translator_id=%s",
			url.QueryEscape(kp), url.QueryEscape(t))
	}

	streamResp, ok := a.fetchStream(req.Context(), path)
	if !ok || strings.TrimSpace(streamResp.Stream) == "" {
		log.Warn().Str("kp", kp).Str("t", t).Int("s", s).Int("e", e).Msg("ahuerezka: empty stream response")
		writeGetsTVEmpty(w, rjson)
		return
	}

	streams, qualMap := a.extractStreams(req, streamResp.Stream, plugin)
	if len(streams) == 0 {
		log.Warn().Str("kp", kp).Str("t", t).Msg("ahuerezka: extractStreams returned 0 streams")
		writeGetsTVEmpty(w, rjson)
		return
	}

	var subs []map[string]any
	if subStr := ahueRezkaSubtitleString(streamResp.Subtitle); subStr != "" {
		subs = pidorezkaParseSubtitles(subStr)
	}

	baseTitle := getsTVJoinName(title, originalTitle)
	bestURL, _ := streams[0]["url"].(string)
	voiceName := strings.TrimSpace(q.Get("voice_name"))
	if voiceName == "" {
		voiceName = "Оригинал"
	}

	row := map[string]any{
		"method": "play",
		"url":    bestURL,
		"stream": bestURL,
		"name":   voiceName,
		"title":  baseTitle,
	}
	if len(qualMap) > 0 {
		row["quality"] = qualMap
		row["qualitys"] = qualMap
	}
	if len(subs) > 0 {
		row["subtitles"] = subs
	}
	if s > 0 {
		row["s"] = s
	}
	if e > 0 {
		row["e"] = e
	}

	data := []map[string]any{row}

	if rjson {
		// method:"call" from embed/episode expects a flat play-object.
		if parseBoolParam(q.Get("call")) {
			writeJSON(w, http.StatusOK, data[0])
			return
		}
		tp := "movie"
		if s > 0 && e > 0 {
			tp = "episode"
		}
		writeJSON(w, http.StatusOK, map[string]any{"type": tp, "data": data})
		return
	}

	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	getsTVAppendMovieHTML(&sb, row, voiceName, true, s, e)
	sb.WriteString(`</div>`)
	writeHTML(w, http.StatusOK, sb.String())
}

// extractStreams turns the worker's "[q]url or url,..." qualities string into
// Lampa streams. It mirrors pidorezkaChecker.extractQualities but stays fully
// decoupled from the rezka.go struct — it only calls package-level primitives.
// ahueRezkaApplyHLSForm makes the `hls` flag SYMMETRIC.
//
// The worker hands each quality out as a pair — "<url>:hls:manifest.m3u8 or
// <same url>.mp4" — and pidorezkaPickBestStreamURL scores .m3u8 at +10 against
// .mp4 at +6, so the HLS spelling always wins. That spelling is a hard 404 on
// stream.voidboost.* (verified on fresh tokens across every host and subdomain);
// the bare .mp4 answers 302 → apollo.stream.voidboost.one → 206 video/mp4.
//
// The flag used to only ever APPEND the suffix, so it could never undo an
// upstream one and `hls = false` silently did nothing. Now off means off: strip
// the suffix when present. Both spellings point at the same file, so trimming is
// exactly equivalent to picking the .mp4 side of the pair.
func ahueRezkaApplyHLSForm(rawURL string, hlsOn bool) string {
	const suffix = ":hls:manifest.m3u8"
	if !hlsOn {
		return strings.TrimSuffix(rawURL, suffix)
	}
	if !strings.HasSuffix(rawURL, ".m3u8") {
		return rawURL + suffix
	}
	return rawURL
}

// ahueRezkaTrueLabel corrects the worker's quality labels, which are inflated by
// exactly one step across the whole free ladder.
//
// Measured with ffprobe on the delivered files, two unrelated titles (kp 1382256
// and kp 386), 8/8 consistent:
//
//	worker "360p"  -> 426x240
//	worker "480p"  -> 640x360
//	worker "720p"  -> 854x480
//	worker "1080p" -> 1280x720
//
// So HDRezka's free ladder actually tops out at 720p and everything above it is
// premium (which this worker can only serve as the dead "buy premium" stub).
// Without this the picker offers 1080p and plays 720p.
//
// Premium tier labels are deliberately NOT remapped: they are skipped entirely
// unless premium is on, and if a deployment ever points at a worker with a real
// premium account their true resolutions are unknown to us.
var ahueRezkaTrueLabel = map[string]string{
	"1080p": "720p",
	"720p":  "480p",
	"480p":  "360p",
	"360p":  "240p",
}

// ahueRezkaLooksLikeMedia reports whether a URL scraped out of the worker payload
// is plausibly a video stream.
//
// The bare-URL fallback uses pidorezkaExtractAnyURLs, which grabs EVERY http(s)
// URL in the string — including the premium badge image embedded in a tier label
// (`<img src="https://static.hdrezka.ac/…prem-icon.svg">`). Without this filter a
// premium-only title handed the client an .svg as its stream.
func ahueRezkaLooksLikeMedia(u string) bool {
	lu := strings.ToLower(ahueRezkaApplyHLSForm(strings.TrimSpace(u), false))
	if lu == "" {
		return false
	}
	if i := strings.IndexAny(lu, "?#"); i >= 0 {
		lu = lu[:i]
	}
	for _, ext := range []string{".mp4", ".m3u8", ".m3u", ".ts", ".mkv", ".webm", ".mpd"} {
		if strings.HasSuffix(lu, ext) {
			return true
		}
	}
	return false
}

func (a *ahueRezkaChecker) extractStreams(req *http.Request, decoded, plugin string) ([]map[string]any, map[string]string) {
	streams := make([]map[string]any, 0, 6)
	qualMap := make(map[string]string, 6)
	// Read once per call so both loops below agree even if a reload lands mid-flight.
	hlsOn := a.hlsEnabled()
	premiumOn := a.premiumEnabled()

	// URLs belonging to premium tiers we deliberately skipped. Recorded so the
	// bare-URL fallback below cannot resurface them: it takes ANY url in the
	// payload, and on a title whose only tagged tiers are premium that means
	// serving the dead stub instead of returning nothing and letting the client
	// move on to another source.
	premiumSkipped := map[string]struct{}{}

	for _, qd := range pidorezkaQualities {
		rawURL := pidorezkaExtractQualityURL(decoded, qd.key)
		if rawURL == "" && qd.altKey != "" {
			rawURL = pidorezkaExtractQualityURL(decoded, qd.altKey)
		}
		if rawURL == "" {
			continue
		}
		// The hdbase worker has no real premium HDRezka account, so every premium
		// tier resolves to the same ~1-minute "buy premium" stub. Verified live on
		// kp 1382256: 1080p Ultra / 2K / 4K all point at one shared rhtie.mp4 in a
		// different directory from the title's real files, and that stub answers
		// 404. Skip them unless premium is on — otherwise the highest tier becomes
		// the default and the user gets the stub instead of the film.
		if ahueRezkaIsPremiumTier(qd.key) && !premiumOn {
			premiumSkipped[ahueRezkaApplyHLSForm(rawURL, false)] = struct{}{}
			continue
		}
		// Reject a URL already claimed by a skipped premium tier, whatever label
		// we are currently under. pidorezkaExtractQualityURL matches the tier key
		// as a SUBSTRING of the bracket ("1080p" also matches the premium span
		// "<span …>1080p Ultra<img …></span>"), so on a title with no plain
		// [1080p] the stub came out labelled "1080p" and bypassed the check above
		// entirely. Premium entries sort before the plain ones in
		// pidorezkaQualities, so premiumSkipped is already populated here.
		if _, isPremiumStub := premiumSkipped[ahueRezkaApplyHLSForm(rawURL, false)]; isPremiumStub {
			continue
		}
		rawURL = ahueRezkaApplyHLSForm(rawURL, hlsOn)
		rawURL = fixVoidboostURL(rawURL)
		// Correct the inflated label — free tiers only. The premium "1080p Ultra"
		// entry carries label "1080p" too, and remapping it would collide with the
		// corrected real 1080p (both would land on "720p").
		label := qd.label
		if !ahueRezkaIsPremiumTier(qd.key) {
			if trueLabel, ok := ahueRezkaTrueLabel[label]; ok {
				label = trueLabel
			}
		}
		proxied := streamProxyDirectURL(req, rawURL, plugin)
		streams = append(streams, map[string]any{"url": proxied, "label": label})
		qualMap[label] = proxied
	}

	// Fallback: a bare URL list without [quality] tags.
	if len(streams) == 0 {
		seen := map[string]struct{}{}
		for i, rawURL := range pidorezkaExtractAnyURLs(decoded) {
			rawURL = strings.TrimSpace(rawURL)
			if rawURL == "" {
				continue
			}
			if !ahueRezkaLooksLikeMedia(rawURL) {
				continue
			}
			if _, isPremiumStub := premiumSkipped[ahueRezkaApplyHLSForm(rawURL, false)]; isPremiumStub {
				continue
			}
			rawURL = ahueRezkaApplyHLSForm(rawURL, hlsOn)
			rawURL = fixVoidboostURL(rawURL)
			proxied := streamProxyDirectURL(req, rawURL, plugin)
			if _, ok := seen[proxied]; ok {
				continue
			}
			seen[proxied] = struct{}{}
			label := "auto"
			if i > 0 {
				label = fmt.Sprintf("alt-%d", i+1)
			}
			streams = append(streams, map[string]any{"url": proxied, "label": label})
			qualMap[label] = proxied
		}
	}

	return streams, qualMap
}

// ---------------------------------------------------------------------------
// Kinopoisk ID resolution
// ---------------------------------------------------------------------------

// resolveKPID extracts the Kinopoisk ID from the request, falling back to a
// title search via the companion kp worker when Lampa did not send one.
func (a *ahueRezkaChecker) resolveKPID(req *http.Request) string {
	q := req.URL.Query()

	// Only trust kinopoisk_id: Lampa's bare "id" is its own card id, not a KP id.
	// Our own continuation links always carry kinopoisk_id, so this stays stable
	// across embed -> serial -> movie navigation.
	for _, key := range []string{"kinopoisk_id", "kinopoisk"} {
		v := strings.TrimSpace(q.Get(key))
		if v == "" {
			continue
		}
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return v
		}
	}

	return a.searchKPID(req)
}

func (a *ahueRezkaChecker) searchKPID(req *http.Request) string {
	if a.kpHost == "" {
		return ""
	}
	q := req.URL.Query()
	year := strings.TrimSpace(q.Get("year"))

	// Try the localized title first, then the original (English) one. A Hollywood film often indexes
	// under only one of them in the kp worker; capi sends id+imdb+title (and our enrichment adds
	// original_title), so without this retry rezka — which is keyed on the KP id — silently resolves
	// to nothing for some titles → empty body → 0 voices.
	seen := map[string]bool{}
	var tried []string
	for _, t := range []string{q.Get("title"), q.Get("original_title")} {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		tried = append(tried, t)
		if kp := a.kpSearchOne(req.Context(), t, year); kp != "" {
			return kp
		}
	}
	if len(tried) > 0 {
		// Greppable: rezka resolves nothing when the kp worker can't map title→FilmID (worker
		// unreachable from the server, or no match) — the usual cause of "rezka 0 voices in capi".
		log.Debug().Str("kp_host", a.kpHost).Strs("tried", tried).Str("year", year).
			Msg("ahuerezka: kp title search → no FilmID")
	}
	return ""
}

// kpSearchOne queries the kp worker for one title and returns the best FilmID (exact-year match
// preferred), or "" on transport failure / no result.
func (a *ahueRezkaChecker) kpSearchOne(ctx context.Context, title, year string) string {
	// kpGetJSONPath already retries direct (bypassing SOCKS) before rotating —
	// the kp worker is globally reachable, so an unreachable workers.dev is
	// normally our egress, not the mirror. This was the usual cause of
	// "rezka 0 voices in capi".
	body, ok := a.kpGetJSONPath(ctx, "/search?q="+url.QueryEscape(title))
	if !ok {
		log.Debug().Str("kp_host", a.activeKPHost()).Str("title", title).Msg("ahuerezka: kp worker fetch FAILED")
		return ""
	}
	var res ahueRezkaKPSearch
	if err := stdjson.Unmarshal(body, &res); err != nil || len(res.Films) == 0 {
		return ""
	}
	if year != "" {
		for _, f := range res.Films {
			if f.FilmID > 0 && strings.TrimSpace(f.Year) == year {
				return strconv.FormatInt(f.FilmID, 10)
			}
		}
	}
	if res.Films[0].FilmID > 0 {
		return strconv.FormatInt(res.Films[0].FilmID, 10)
	}
	return ""
}

// ---------------------------------------------------------------------------
// Worker fetch helpers
// ---------------------------------------------------------------------------

func (a *ahueRezkaChecker) fetchInfo(ctx context.Context, kp string) (ahueRezkaInfo, bool) {
	var info ahueRezkaInfo
	body, ok := a.getJSONPath(ctx, "/info?id="+url.QueryEscape(kp))
	if !ok {
		return info, false
	}
	if err := stdjson.Unmarshal(body, &info); err != nil {
		log.Debug().Err(err).Str("kp", kp).Msg("ahuerezka: info unmarshal failed")
		return info, false
	}
	return info, true
}

func (a *ahueRezkaChecker) fetchEpisodes(ctx context.Context, kp, t string) (ahueRezkaEpisodes, bool) {
	var eps ahueRezkaEpisodes
	body, ok := a.getJSONPath(ctx, "/episodes?id="+url.QueryEscape(kp)+"&translator_id="+url.QueryEscape(t))
	if !ok {
		return eps, false
	}
	if err := stdjson.Unmarshal(body, &eps); err != nil {
		log.Debug().Err(err).Str("kp", kp).Str("t", t).Msg("ahuerezka: episodes unmarshal failed")
		return eps, false
	}
	return eps, true
}

func (a *ahueRezkaChecker) fetchStream(ctx context.Context, path string) (ahueRezkaStream, bool) {
	var st ahueRezkaStream
	body, ok := a.getJSONPath(ctx, path)
	if !ok {
		return st, false
	}
	if err := stdjson.Unmarshal(body, &st); err != nil {
		log.Debug().Err(err).Str("path", path).Msg("ahuerezka: stream unmarshal failed")
		return st, false
	}
	return st, true
}

// getJSONPath fetches a worker path (e.g. "/info?id=301"), walking the mirror
// list on transport failures.
func (a *ahueRezkaChecker) getJSONPath(ctx context.Context, path string) ([]byte, bool) {
	attempts := len(a.hosts)
	if attempts < 1 {
		attempts = 1
	}
	for i := 0; i < attempts; i++ {
		host := a.activeHost()
		body, err := a.getJSONVia(ctx, host+path, a.client)
		if err == nil {
			return body, true
		}
		if !errors.Is(err, ahueRezkaErrTransport) {
			// The mirror is alive and told us something (404 = no such title,
			// 5xx = worker error). Another mirror would answer the same.
			return nil, false
		}
		ahueRezkaAdvanceHost(a.hosts, &ahueRezkaHostIdx, host, "worker")
	}
	return nil, false
}

// kpGetJSONPath is the same for the companion kp worker, with one extra step:
// on a transport failure it retries the SAME host without the SOCKS proxy
// before rotating. The kp worker is globally reachable, so an unreachable
// workers.dev usually means our RU egress can't get there — not a dead mirror.
func (a *ahueRezkaChecker) kpGetJSONPath(ctx context.Context, path string) ([]byte, bool) {
	attempts := len(a.kpHosts)
	if attempts < 1 {
		attempts = 1
	}
	for i := 0; i < attempts; i++ {
		host := a.activeKPHost()
		if host == "" {
			return nil, false
		}
		target := host + path
		body, err := a.getJSONVia(ctx, target, a.client)
		if err == nil {
			return body, true
		}
		if !errors.Is(err, ahueRezkaErrTransport) {
			return nil, false
		}
		log.Debug().Str("kp_host", host).Msg("ahuerezka: kp via socks failed → retrying direct")
		if directBody, directErr := a.getJSONVia(ctx, target, a.clientDirect); directErr == nil {
			return directBody, true
		}
		ahueRezkaAdvanceHost(a.kpHosts, &ahueRezkaKPHostIdx, host, "kp")
	}
	return nil, false
}

// getJSONVia returns ahueRezkaErrTransport when the host could not be reached,
// so callers can tell "this mirror is dead, try the next" from "this mirror
// answered, and the answer was an error".
func (a *ahueRezkaChecker) getJSONVia(ctx context.Context, target string, client *http.Client) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(cctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ahueRezkaUA)
	req.Header.Set("Accept", "application/json, text/plain, */*")

	resp, err := client.Do(req)
	if err != nil {
		log.Debug().Err(err).Str("url", target).Msg("ahuerezka: request failed")
		return nil, fmt.Errorf("%w: %v", ahueRezkaErrTransport, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Debug().Int("status", resp.StatusCode).Str("url", target).Msg("ahuerezka: bad status")
		return nil, fmt.Errorf("ahuerezka: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ahueRezkaErrTransport, err)
	}
	return body, nil
}

// ahueRezkaSubtitleString normalizes the worker's "subtitle" field, which is
// either boolean false or an HDRezka "[lang]url,..." string.
func ahueRezkaSubtitleString(raw stdjson.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "false" || s == "null" || s == `""` {
		return ""
	}
	var str string
	if err := stdjson.Unmarshal(raw, &str); err == nil {
		return strings.TrimSpace(str)
	}
	return ""
}
