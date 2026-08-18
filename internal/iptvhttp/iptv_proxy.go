package iptvhttp

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/iptv"
	"lampac-go/internal/proxylink"
	"lampac-go/internal/tgauth"
	"lampac-go/internal/transcodesvc"
)

var iptvLogoClient = &http.Client{Timeout: 12 * time.Second}

// ---------------------------------------------------------------------------
//  GET /api/iptv/logo?channel_id=... — server-side fetch of a channel logo.
//  Browsers block a playlist's http:// logos on an https page (mixed content)
//  and some hosts hotlink-protect; fetching server-side and re-serving over the
//  app origin sidesteps both. The URL is the channel's own logo (no SSRF surface).
// ---------------------------------------------------------------------------

func iptvLogoHandler(iptvStore *iptv.Store, tgStore *tgauth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID := iptvTgID(r, tgStore)
		channelID := r.URL.Query().Get("channel_id")
		if channelID == "" {
			http.Error(w, "channel_id required", http.StatusBadRequest)
			return
		}
		ch, _ := iptvStore.GetChannel(tgID, channelID)
		if ch == nil || ch.Logo == "" {
			http.Error(w, "no logo", http.StatusNotFound)
			return
		}
		if !strings.HasPrefix(ch.Logo, "http://") && !strings.HasPrefix(ch.Logo, "https://") {
			http.Error(w, "bad url", http.StatusBadRequest)
			return
		}

		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, ch.Logo, nil)
		if err != nil {
			http.Error(w, "bad url", http.StatusBadRequest)
			return
		}
		if ch.UserAgent != "" {
			req.Header.Set("User-Agent", ch.UserAgent)
		}
		if ch.Referer != "" {
			req.Header.Set("Referer", ch.Referer)
		}

		resp, err := iptvLogoClient.Do(req)
		if err != nil {
			http.Error(w, "fetch failed", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			http.Error(w, "upstream "+strconv.Itoa(resp.StatusCode), http.StatusBadGateway)
			return
		}

		ct := resp.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "image/") {
			ct = "image/png" // some hosts mislabel; treat as an image so the browser still renders it
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.WriteHeader(http.StatusOK)
		io.Copy(w, io.LimitReader(resp.Body, 4<<20)) // 4 MB cap
	}
}

// iptvCatchupURL rewrites a channel's LIVE url into an ARCHIVE (catchup/timeshift) url for a program
// that started at `start` (unix seconds) and ran `duration` seconds, per the channel's catchup-type.
// Returns "" when catchup isn't applicable.
func iptvCatchupURL(raw string, c *iptv.Catchup, start, duration int64) string {
	if c == nil || start <= 0 {
		return ""
	}
	if duration <= 0 {
		duration = 3600
	}
	now := time.Now().Unix()
	switch strings.ToLower(strings.TrimSpace(c.Type)) {
	case "flussonic", "fs":
		// .../index.m3u8?token=x  →  .../index-{start}-{duration}.m3u8?token=x
		u, err := url.Parse(raw)
		if err != nil {
			return ""
		}
		dir, file := path.Split(u.Path)
		ext := path.Ext(file)
		name := strings.TrimSuffix(file, ext)
		if name == "" {
			name = "index"
		}
		if ext == "" {
			ext = ".m3u8"
		}
		u.Path = dir + fmt.Sprintf("%s-%d-%d%s", name, start, duration, ext)
		return u.String()
	case "append":
		// append catchup-source with placeholders substituted
		rep := strings.NewReplacer(
			"{utc}", strconv.FormatInt(start, 10),
			"${start}", strconv.FormatInt(start, 10),
			"{start}", strconv.FormatInt(start, 10),
			"{lutc}", strconv.FormatInt(now, 10),
			"${now}", strconv.FormatInt(now, 10),
			"{now}", strconv.FormatInt(now, 10),
			"{duration}", strconv.FormatInt(duration, 10),
			"{offset}", strconv.FormatInt(now-start, 10),
			"{end}", strconv.FormatInt(start+duration, 10),
		)
		return raw + rep.Replace(c.Source)
	default: // "default", "shift", unknown → append utc/lutc query params
		sep := "?"
		if strings.Contains(raw, "?") {
			sep = "&"
		}
		return raw + sep + "utc=" + strconv.FormatInt(start, 10) + "&lutc=" + strconv.FormatInt(now, 10)
	}
}

// iptvResolveStreamURL turns a channel's raw upstream URL into the URL the
// CLIENT is allowed to play. Returns (url, "") on success or ("", errSlug)
// when the stream must be proxied but proxying is unavailable/failed.
//
// Force-proxy when the raw URL can't be played directly by THIS client:
// cleartext http:// is blocked in the browser (mixed content) — but the NATIVE
// apps ship usesCleartextTraffic=true and ask with direct=1, so plain http://
// plays straight from the device. Это принципиально для гео: у провайдера с
// РФ-only плейлистом сервер (зарубежный IP) получал 403, хотя телевизор стоит
// в РФ — прямой стрим с устройства проходит гео сам.
// User-Agent/Referer-gated CDNs still proxy (the /proxy token injects the
// headers); mustProxy also holds for proxy=all playlists (hide the client IP
// from the upstream provider — playlist-sharing protection); in that case we
// must NEVER fall back to the raw URL (it would dump the channel's real CDN
// URL to the client). Fail closed instead.
func iptvResolveStreamURL(rawURL string, ch *iptv.Channel, pl *iptv.Playlist, proxyLinks *proxylink.Manager, reqIP, host string, direct bool) (string, string) {
	// UA канала главнее; UA плейлиста — фолбэк (панель, требующая UA на M3U, почти
	// всегда требует его и на стримах).
	ua := ch.UserAgent
	if ua == "" && pl != nil {
		ua = pl.UserAgent
	}
	needHeaders := ua != "" || ch.Referer != ""
	cleartext := strings.HasPrefix(strings.ToLower(rawURL), "http://")
	forceProxy := needHeaders || (cleartext && !direct)
	mustProxy := forceProxy || (pl != nil && pl.ProxyMode == "all")
	if !mustProxy {
		return rawURL, ""
	}
	if proxyLinks == nil {
		return "", "stream proxy unavailable"
	}
	headers := make(map[string]string)
	if ua != "" {
		headers["User-Agent"] = ua
	}
	if ch.Referer != "" {
		headers["Referer"] = ch.Referer
	}
	var proxyHash string
	if len(headers) > 0 {
		proxyHash = proxyLinks.EncryptURIWithHeaders(rawURL, reqIP, "iptv", headers)
	} else {
		proxyHash = proxyLinks.EncryptURI(rawURL, reqIP, "iptv", false, false, false)
	}
	if proxyHash == "" {
		return "", "stream proxy failed"
	}
	return host + "/proxy/" + proxyHash, ""
}

// ---------------------------------------------------------------------------
//  GET /api/iptv/play?channel_id=...[&start=<unix>&duration=<sec>]
//  Without start → live; with start → archive/catchup. Both proxy-wrapped.
// ---------------------------------------------------------------------------

func iptvPlayHandler(cfg config.Config, iptvStore *iptv.Store, tgStore *tgauth.Store, proxyLinks *proxylink.Manager, transSvc *transcodesvc.TranscodingService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID := iptvTgID(r, tgStore)
		channelID := r.URL.Query().Get("channel_id")
		if channelID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "channel_id required"})
			return
		}

		ch, pl := iptvStore.GetChannel(tgID, channelID)
		if ch == nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "channel not found"})
			return
		}

		// Archive/catchup: ?start=<unix>&duration=<sec> rewrites the live URL to the archive URL.
		rawURL := ch.URL
		live := true
		if s := r.URL.Query().Get("start"); s != "" {
			start, _ := strconv.ParseInt(s, 10, 64)
			dur, _ := strconv.ParseInt(r.URL.Query().Get("duration"), 10, 64)
			if cu := iptvCatchupURL(ch.URL, ch.Catchup, start, dur); cu != "" {
				rawURL = cu
				live = false
			}
		}

		streamURL, resolveErr := iptvResolveStreamURL(rawURL, ch, pl, proxyLinks, clientIP(r), hostFromRequest(r),
			r.URL.Query().Get("direct") == "1")
		if resolveErr != "" {
			status := http.StatusBadGateway
			if resolveErr == "stream proxy unavailable" {
				status = http.StatusServiceUnavailable
			}
			writeJSON(w, status, map[string]any{"error": resolveErr})
			return
		}

		catchupDays := 0
		if ch.Catchup != nil {
			catchupDays = ch.Catchup.Days
		}

		// «Эконом»: re-encode the live channel to a lower resolution for a thin pipe. Only for the
		// live edge (never archive/catchup) and only when the client asked (?econom=1). Passes the RAW
		// upstream URL + channel headers (not the client-IP-bound /proxy URL) so the transcode server
		// can actually fetch it. Falls back to the direct stream if the transcode couldn't start.
		if live && wantEconom(r) {
			if econURL := iptvEconomPlaylist(cfg, proxyLinks, ch, rawURL, hostFromRequest(r), transSvc); econURL != "" {
				streamURL = econURL
			}
		}

		payload := map[string]any{
			"url":          streamURL,
			"name":         ch.Name,
			"logo":         ch.Logo,
			"quality":      ch.Quality,
			"group":        ch.Group,
			"live":         live,
			"catchup_days": catchupDays,
			"econom":       econEnabled(cfg, transSvc), // client shows the «Эконом» option only when honoured
		}
		// What the health probe found INSIDE this channel, when it has been
		// probed. The client knows its own decoder set exactly — the server does
		// not — so it gets the facts and decides for itself whether to warn, to
		// pre-select «Эконом», or to say nothing. Absent field = never probed;
		// the client must treat that as "unknown", not as "fine".
		if info, ok := iptvStore.StreamInfoFor(ch.URL); ok {
			payload["stream_codecs"] = info.Codecs
			payload["stream_tracks"] = info.Tracks
			payload["stream_probed_at"] = info.ProbedAt.Unix()
		}
		writeJSON(w, http.StatusOK, payload)
	}
}
