package transcodesvc

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"lampac-go/internal/transcode"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/torrbalancer"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

// ---------------------------------------------------------------------------
// Content-Type map for transcoding segments.
// ---------------------------------------------------------------------------

var transcodingContentTypes = map[string]string{
	".m4s":  "video/mp4",
	".ts":   "video/mp2t",
	".mp4":  "video/mp4",
	".m2ts": "video/MP2T",
	".m3u8": "application/vnd.apple.mpegurl",
	".vtt":  "text/vtt",
	".ass":  "text/x-ssa; charset=utf-8",
}

var reSegIndex = regexp.MustCompile(`(?i)seg_(\d+)\.(m4s|ts)$`)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func transcodingDisabledError(w http.ResponseWriter) {
	writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Transcoding disabled"})
}

func transcodingHost(r *http.Request) string {
	scheme := requestScheme(r)
	host := r.Host
	full := scheme + "://" + host
	// StreamHostFor is a no-op on a zero Host (no aliases → returns full
	// unchanged), so liveConfig's unwired-server fallback matches the old
	// "leave full untouched" behaviour exactly.
	return liveConfig(config.Config{}).Host.StreamHostFor(full)
}

// ---------------------------------------------------------------------------
// POST /transcoding/start
// ---------------------------------------------------------------------------

// Request marker that routes a transcoding job to the separate capi/TV
// concurrency pool. capiSameOrigin stamps it onto the minted /transcoding URL
// (so it rides the player's media fetch); a header form covers non-URL callers.
const (
	CapiPoolParam = "tcpool"
	CapiPoolValue = "capi"
)

// TranscodingWantsCapiPool reports whether this request should draw from the
// separate capi/TV pool (max_concurrent_capi). Standard-Lampa URLs carry no
// marker → main pool. Harmless when the capi pool isn't configured (schedulerFor
// falls back to the main pool).
func TranscodingWantsCapiPool(r *http.Request) bool {
	if r == nil {
		return false
	}
	if strings.EqualFold(r.URL.Query().Get(CapiPoolParam), CapiPoolValue) {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Transcode-Pool"), CapiPoolValue)
}

// transcodePrivateSrcError validates a CLIENT-supplied transcode source and
// returns a user-facing error when it points at a private/loopback address the
// server can't (or must not) reach — the «домашний TorrServer» case: Lampa
// configured with http://192.168.x.x:8090 sends that LAN URL as src, the VPS
// dials someone's apartment, times out 30-90s, auto-restarts 3×, and the user
// stares at an endless spinner. Fail fast with an explanation instead.
//
// Exemptions keep legit setups working: the configured TorrServer host and
// every TS-balancer backend are allowed even when private (self-hosted LAN
// deployments), and cfg.Transcoding.AllowPrivateSrc disables the check.
// Internal callers (econom loopback /proxy, remote-box signed starts) build
// TranscodingStartRequest directly and never pass through this gate.
//
// r is the incoming request (may be nil) — it carries the two signals that tell
// a public VPS apart from a LAN box, without which this gate broke every
// self-hosted install: see requestIsLANDeployment and srcIsThisServer.
func transcodePrivateSrcError(cfg config.Config, r *http.Request, src string) string {
	if cfg.Transcoding.AllowPrivateSrc {
		return ""
	}
	// A LAN/home install has no «недостижимая чужая квартира» failure mode at
	// all: server and client sit on the same private network, so a private src
	// is the NORMAL case. Only a public deployment needs the gate.
	if requestIsLANDeployment(r) {
		return ""
	}
	u, err := url.Parse(strings.TrimSpace(src))
	if err != nil || u.Hostname() == "" {
		return "" // non-URL / relative sources are validated elsewhere
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil {
		return "" // hostname — public DNS; not the LAN-TorrServer failure mode
	}
	if !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
		return ""
	}
	// The src is US: either the address the client is already talking to, or a
	// /ts//pidtor path the pipeline rewrites onto the local TorrServer before
	// ffmpeg ever dials anything. Never reject the server's own stream.
	if srcIsThisServer(cfg, r, src, u) {
		return ""
	}
	// Same host as the operator's own TorrServer(s)? Then it's a legit
	// self-hosted LAN deployment, not a client's home box.
	host := u.Hostname()
	if cu, err := url.Parse(strings.TrimSpace(cfg.TorrServer.URL)); err == nil && cu.Hostname() == host {
		return ""
	}
	if liveTSBalancerPool() != nil {
		for _, b := range liveTSBalancerPool().Backends() {
			bh, _ := b.Target()
			if bu, err := url.Parse(bh); err == nil && bu.Hostname() == host {
				return ""
			}
		}
	}
	return "источник указывает на локальный адрес (" + host + ") — похоже, ваш TorrServer работает в домашней сети, и серверу он недоступен; транскод возможен только с TorrServer этого сервера"
}

// requestHostname is the hostname the CLIENT used to reach us (X-Forwarded-Host
// behind nginx, else Host), port stripped.
func requestHostname(r *http.Request) string {
	if r == nil {
		return ""
	}
	h := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if h == "" {
		h = strings.TrimSpace(r.Host)
	}
	if i := strings.Index(h, ","); i >= 0 {
		h = strings.TrimSpace(h[:i])
	}
	if hn, _, err := net.SplitHostPort(h); err == nil {
		return hn
	}
	return strings.Trim(h, "[]")
}

// requestClientIP is the caller's IP: XFF/X-Real-IP first (reverse proxy), then
// RemoteAddr, port stripped.
func requestClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	v := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if v == "" {
		v = strings.TrimSpace(r.Header.Get("X-Real-IP"))
	}
	if v == "" {
		v = strings.TrimSpace(r.RemoteAddr)
	}
	if i := strings.Index(v, ","); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	if h, _, err := net.SplitHostPort(v); err == nil {
		v = h
	}
	return strings.Trim(v, "[]")
}

func ipIsLocal(ip net.IP) bool {
	return ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast())
}

// requestIsLANDeployment reports whether this instance is a home/LAN install
// rather than a public server: the client reached us at a private/loopback
// address (or a localhost/.local/.lan name) AND its own IP is private.
//
// Both conditions must hold, so a public VPS keeps the gate even when a
// reverse proxy mangles one of the two: nginx forwarding Host: 127.0.0.1 still
// carries a public X-Forwarded-For, and a public hostname fails the first test
// regardless of who is calling.
func requestIsLANDeployment(r *http.Request) bool {
	host := strings.ToLower(requestHostname(r))
	if host == "" {
		return false
	}
	local := host == "localhost" ||
		strings.HasSuffix(host, ".local") ||
		strings.HasSuffix(host, ".lan") ||
		strings.HasSuffix(host, ".home") ||
		ipIsLocal(net.ParseIP(host))
	if !local {
		return false
	}
	return ipIsLocal(net.ParseIP(requestClientIP(r)))
}

// srcIsThisServer reports whether a client-supplied src actually resolves back
// to this instance, in which case a private address is not a «чужая домашняя
// сеть» at all:
//
//   - same hostname as the request → the client is already talking to that
//     address, so it is trivially reachable;
//   - /ts/ or /lite/pidtor/s path with a TorrServer available → Start() and
//     RunFFProbe rewrite it onto tsDirectStreamBase (127.0.0.1 / the configured
//     TorrServer) before ffmpeg runs, so the private host is never dialled.
//
// This is what broke every local install: the web torrent browser plays
// http://192.168.x.x:PORT/ts/stream?link=… — the box's OWN LAN address — and the
// gate rejected the server's own stream URL.
func srcIsThisServer(cfg config.Config, r *http.Request, src string, u *url.URL) bool {
	if rh := requestHostname(r); rh != "" && u != nil && strings.EqualFold(rh, u.Hostname()) {
		return true
	}
	tsAvail := torrsIsInProcess() || cfg.TorrServer.Port > 0 || strings.TrimSpace(cfg.TorrServer.URL) != ""
	return tsAvail && (strings.Contains(src, "/ts/") || strings.Contains(src, "/lite/pidtor/s"))
}

func transcodingStartHandler(cfg config.Config, svc *TranscodingService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !cfg.Transcoding.Enable {
			transcodingDisabledError(w)
			return
		}
		// A dedicated box is driven only by the main server's signed GET /transcoding/start.m3u8; the
		// unsigned POST start (the standard Lampa-plugin path) would be an open SSRF hole here.
		if transcodeIsBox(cfg) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		var req TranscodingStartRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Request body is required"})
			return
		}

		if msg := transcodePrivateSrcError(cfg, r, req.Src); msg != "" {
			log.Info().Str("src", req.Src).Str("host", requestHostname(r)).Str("client", requestClientIP(r)).
				Str("hint", "self-hosted LAN install? set transcoding.allow_private_src=true").
				Msg("transcoding: rejected private-IP client src")
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": msg})
			return
		}

		// P3.B: stamp the HTTP User-Agent into req.Headers when the client
		// didn't supply one in the JSON body. UA-DB needs it to derive a
		// fallback ClientCaps when req.Client is nil. The body's userAgent
		// (if any) wins — explicit > heuristic.
		if getHeader(req.Headers, "userAgent") == "" {
			if hua := r.Header.Get("User-Agent"); hua != "" {
				if req.Headers == nil {
					req.Headers = map[string]string{}
				}
				req.Headers["userAgent"] = hua
			}
		}

		// Route to the separate capi/TV pool when tagged (no-op if unconfigured).
		req.useCapiPool = TranscodingWantsCapiPool(r)

		// P4: coalescing key for this (src + client + audio + subs + seek).
		dedupKey := transcodingDedupKey(r, &req)

		// Experimental pipe path: for eligible plain-HTTP remux / audio-only
		// sources, serve fragmented-MP4 segments from RAM instead of disk.
		// Falls through to the disk path for everything else. Gated by
		// Transcoding.PipeEnable (default off).
		if svc.pipeMgr != nil {
			if ps, handled, _ := svc.pipeMgr.StartEligible(&req); handled && ps != nil {
				host := transcodingHost(r)
				resp := map[string]any{
					"streamId":            ps.streamID,
					"mode":                "pipe",
					"selectedAudio":       ps.audioIdx,
					"playlistUrl":         fmt.Sprintf("%s/transcoding/pipe/%s/main.m3u8", host, ps.streamID),
					"hls_timeout_seconds": 60,
				}
				if ps.streams != nil {
					resp["streams"] = ps.streams
				}
				writeJSON(w, http.StatusOK, resp)
				return
			}
		}

		// P4: a still-warming-up job for the identical request → reuse it
		// instead of spawning another ffmpeg. This collapses the retry storm
		// (player times out waiting for a slow first segment, re-POSTs /start)
		// onto a single process.
		if job := svc.LookupInflightJob(dedupKey); job != nil {
			svc.Touch(job)
			writeJSON(w, http.StatusOK, transcodingStartResponse(transcodingHost(r), job))
			return
		}

		job, errMsg := svc.Start(&req)
		if job == nil {
			// P3.G: enrich the bare error string with a stable code, a
			// Russian user-facing explanation, and a list of actionable
			// suggestions ("смените балансер", "снизьте качество", …)
			// — exactly the народные обходные пути from research.
			diag := diagnoseStartError(errMsg, req.Src)
			body := map[string]any{
				"error":       errMsg, // legacy field — keep for plugin compatibility
				"errorCode":   diag.Code,
				"userMessage": diag.UserMessage,
			}
			if len(diag.Suggestions) > 0 {
				body["suggestions"] = diag.Suggestions
			}
			// P2.1: scheduler busy → 503 + Retry-After so the client
			// can back off instead of hammering the server.
			if errMsg == ErrSchedulerBusy.Error() {
				w.Header().Set("Retry-After", "5")
				writeJSON(w, http.StatusServiceUnavailable, body)
				return
			}
			if errMsg == ErrTorrentNoData.Error() {
				writeJSON(w, http.StatusBadGateway, body)
				return
			}
			writeJSON(w, http.StatusBadRequest, body)
			return
		}

		// P4: register for coalescing so identical retries during warm-up
		// reuse this job instead of spawning another ffmpeg. Native/direct
		// jobs aren't tracked — they have no process and can't storm.
		if job.Mode != transcode.ModeNative && job.Mode != transcode.ModeDirect {
			svc.RegisterInflightJob(dedupKey, job)
		}
		writeJSON(w, http.StatusOK, transcodingStartResponse(transcodingHost(r), job))
	}
}

// transcodingDedupKey builds the job-coalescing key for a /transcoding/start
// request: same source + same client (IP+UA) + same audio/subs/seek collapse
// onto one job. Different audio track, seek point or client → different key.
func transcodingDedupKey(r *http.Request, req *TranscodingStartRequest) string {
	if req == nil || strings.TrimSpace(req.Src) == "" {
		return ""
	}
	audio := "auto"
	if req.Audio != nil {
		audio = strconv.Itoa(req.Audio.Index)
	}
	subs := "sx"
	if req.Subtitles != nil {
		if *req.Subtitles {
			subs = "s1"
		} else {
			subs = "s0"
		}
	}
	seek := 0
	if req.HLS != nil {
		seek = req.HLS.Seek
	}
	live := "0"
	if req.Live {
		live = "1"
	}
	return strings.Join([]string{
		strings.TrimSpace(req.Src), clientFingerprint(r),
		audio, subs, live, strconv.Itoa(seek),
	}, "|")
}

// transcodingStartResponse builds the /transcoding/start success payload from a
// job. Shared by a fresh start and a coalesced (reused) job so both return an
// identical shape.
func transcodingStartResponse(host string, job *TranscodingJob) map[string]any {
	resp := map[string]any{
		"streamId":            job.StreamID,
		"mode":                string(job.Mode),
		"selectedAudio":       job.SelectedAudio,
		"hls_timeout_seconds": 60,
	}
	if job.ProfileLabel != "" {
		resp["clientProfile"] = job.ProfileLabel
	}
	if len(job.KnownIssues) > 0 {
		resp["knownIssues"] = job.KnownIssues
	}
	if job.Streams != nil {
		resp["streams"] = job.Streams
	}
	if job.Warning != "" {
		resp["warning"] = job.Warning
	}
	if job.Context.SubPlan.Strategy != "" && job.Context.SubPlan.Strategy != StrategyNone {
		resp["subtitlePlan"] = summarizeSubtitlesForResponse(job.Context.SubPlan)
	}

	// Native / direct: plugin plays the original URL, no playlist.
	if job.Mode == transcode.ModeNative || job.Mode == transcode.ModeDirect {
		resp["originalUrl"] = job.OriginalURL
		return resp
	}

	playlistType := "main"
	if job.Context.Live {
		playlistType = "live"
	} else if jobUsesMasterPlaylist(job) {
		// ABR multi-rung: segments live in v0/v1 subdirs, so playlistUrl must
		// point at the master (main.m3u8 would hang). A client that ignores
		// masterUrl and plays playlistUrl (both our web players do) still works.
		playlistType = "master"
	}
	resp["playlistUrl"] = fmt.Sprintf("%s/transcoding/%s/%s.m3u8", host, job.StreamID, playlistType)
	if job.Context.Subtitles {
		resp["subtitlesUrl"] = fmt.Sprintf("%s/transcoding/%s/subtitles", host, job.StreamID)
	}
	hasSubs := job.Context.SubPlan.Strategy == StrategyExtract && len(job.Context.SubPlan.Streams) > 0
	vw, vh := extractVideoDimensions(job.Context.FFProbe)
	hasLadder := len(transcode.PlanABRLadder(vw, vh, job.Mode)) > 1
	if hasSubs || hasLadder {
		resp["masterUrl"] = fmt.Sprintf("%s/transcoding/%s/master.m3u8", host, job.StreamID)
	}
	return resp
}

// jobUsesMasterPlaylist reports whether ffmpeg is writing per-variant subdirs
// (ABR multi-rung), in which case the root main.m3u8 never fills and the client
// must play master.m3u8. Same condition buildMasterPlaylist uses to emit v<N>/
// variant URIs, so the two can't disagree.
func jobUsesMasterPlaylist(job *TranscodingJob) bool {
	return job.Context.MultiRung && len(job.Context.Ladder) > 1
}

// ---------------------------------------------------------------------------
// GET /transcoding/start.m3u8
// ---------------------------------------------------------------------------

// parseClientCapsFromQuery builds an explicit ClientCaps from a compact `caps=` token list the web
// player appends to its signed transcode URL (e.g. caps=h264,hevc,hevc10,aac,opus,flac). It reflects
// what the BROWSER actually probed via MediaSource.isTypeSupported, overriding the UA-DB guess — the
// fix for «4K-торрент крутится и не играет»: macOS Chrome decodes 10-bit HEVC, so it gets a cheap
// audio-only copy instead of a software HEVC→H.264 re-encode that times out. Returns nil when no caps
// param is present, so the UA-DB fallback stays in charge for clients that don't send it.
func parseClientCapsFromQuery(q url.Values) *transcode.ClientCaps {
	return transcode.ParseCapsToken(q.Get("caps"), q.Get("lang"))
}

func transcodingStartM3U8Handler(cfg config.Config, svc *TranscodingService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !cfg.Transcoding.Enable {
			transcodingDisabledError(w)
			return
		}

		src := strings.TrimSpace(r.URL.Query().Get("src"))
		if src == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "src"})
			return
		}
		// Dedicated transcode box: only accept signed starts from the main server (anti-SSRF / abuse).
		if !transcodeRemoteGateGET(cfg, w, r) {
			return
		}
		// A box trusts its signed main-server srcs (they may legitimately point
		// into the main server's private network); everywhere else the src is
		// client-controlled → same private-IP gate as the POST path.
		if !transcodeIsBox(cfg) {
			if msg := transcodePrivateSrcError(cfg, r, src); msg != "" {
				log.Info().Str("src", src).Str("host", requestHostname(r)).Str("client", requestClientIP(r)).
					Str("hint", "self-hosted LAN install? set transcoding.allow_private_src=true").
					Msg("transcoding: rejected private-IP client src")
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": msg})
				return
			}
		}

		a, _ := strconv.Atoi(r.URL.Query().Get("a"))
		s, _ := strconv.Atoi(r.URL.Query().Get("s"))
		subtitles := r.URL.Query().Get("subtitles") == "true"
		live := r.URL.Query().Get("live") == "true"

		req := &TranscodingStartRequest{
			Src:  src,
			Live: live,
			Audio: &reqAudioOpts{
				Index: a,
			},
			HLS: &HLSOpts{
				Seek: s,
			},
		}
		if subtitles {
			b := true
			req.Subtitles = &b
		}
		// P3.B: forward HTTP User-Agent so UA-DB can pick a sane caps profile.
		if hua := r.Header.Get("User-Agent"); hua != "" {
			req.Headers = map[string]string{"userAgent": hua}
		}
		// Client-declared codec caps (web player probes MediaSource.isTypeSupported) override the
		// UA-DB guess — e.g. macOS Chrome CAN decode 10-bit HEVC, so it gets a cheap audio-only copy
		// instead of a doomed software HEVC→H.264 re-encode. nil when the client sent none.
		if c := parseClientCapsFromQuery(r.URL.Query()); c != nil {
			req.Client = c
			// Потолок клиента → пер-джоб MaxHeight (то же поле, что у IPTV «Эконом»):
			// needConvert форсирует re-encode, applyTranscodeDownscale режет до потолка.
			// SelectMode с тем же порогом уже увёл over-cap видео с copy-путей.
			if c.MaxHeight > 0 {
				req.MaxHeight = c.MaxHeight
			}
		}
		// Route to the separate capi/TV pool when tagged (no-op if unconfigured).
		req.useCapiPool = TranscodingWantsCapiPool(r)

		// P4 coalescing for the GET path too (the POST handler had it, this one
		// didn't): a player that fires two starts at once (Shaka load + the
		// extras prefetch) or re-requests while the box is still adding and
		// probing a cold torrent (10–45s) must share ONE job — prod 2026-08-23:
		// 8 ffmpeg for one pidtor click. Identical requests in flight ride the
		// same Start via singleflight; a job still warming up is reused.
		dedupKey := transcodingDedupKey(r, req)
		var job *TranscodingJob
		var errMsg string
		if existing := svc.LookupInflightJob(dedupKey); existing != nil {
			svc.Touch(existing)
			job = existing
		} else if dedupKey == "" {
			job, errMsg = svc.Start(req)
		} else {
			type startOut struct {
				job    *TranscodingJob
				errMsg string
			}
			v, _, _ := svc.startFlight.Do(dedupKey, func() (any, error) {
				if j := svc.LookupInflightJob(dedupKey); j != nil {
					svc.Touch(j)
					return startOut{job: j}, nil
				}
				j, em := svc.Start(req)
				if j != nil && j.Mode != transcode.ModeNative && j.Mode != transcode.ModeDirect {
					svc.RegisterInflightJob(dedupKey, j)
				}
				return startOut{job: j, errMsg: em}, nil
			})
			out, _ := v.(startOut)
			job, errMsg = out.job, out.errMsg
		}
		if job == nil {
			if errMsg == ErrSchedulerBusy.Error() {
				w.Header().Set("Retry-After", "5")
				writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": errMsg})
				return
			}
			if errMsg == ErrTorrentNoData.Error() {
				// Upstream (the torrent swarm) never delivered — 502 so the player
				// treats it as a dead source and moves on, not as a bad request.
				writeJSON(w, http.StatusBadGateway, map[string]any{"error": errMsg, "errorCode": "torrent_no_data",
					"userMessage": "Торрент не отдаёт данные (нет метаданных/сидов) — попробуйте другую раздачу"})
				return
			}
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": errMsg})
			return
		}

		playlistType := "main"
		if live {
			playlistType = "live"
		} else if jobUsesMasterPlaylist(job) {
			// ABR multi-rung writes per-variant v0/v1 subdirs; the root-segment
			// main.m3u8 never fills (handler waits 90s for seg_00000 that only
			// exists under v0/), so hand back the master that lists the variant
			// playlists. Single-rung is unchanged — it still uses main.
			playlistType = "master"
		}
		host := transcodingHost(r)
		uri := fmt.Sprintf("%s/transcoding/%s/%s.m3u8", host, job.StreamID, playlistType)
		http.Redirect(w, r, uri, http.StatusFound)
	}
}

// ---------------------------------------------------------------------------
// GET /transcoding/{streamId}/seek/{ss}
// ---------------------------------------------------------------------------

func transcodingSeekHandler(cfg config.Config, svc *TranscodingService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !cfg.Transcoding.Enable {
			transcodingDisabledError(w)
			return
		}

		streamID := extractPathParam(r, "streamId")
		ssStr := extractPathParam(r, "ss")
		ss, err := strconv.Atoi(ssStr)
		if err != nil || ss < 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "ss must be >= 0"})
			return
		}

		job, ok := svc.TryResolveJob(streamID)
		if !ok {
			http.NotFound(w, r)
			return
		}

		if !job.Context.Live {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Context not live"})
			return
		}

		success, errMsg := svc.SeekAsync(streamID, ss, nil)
		if !success {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": errMsg})
			return
		}

		w.WriteHeader(http.StatusOK)
	}
}

// ---------------------------------------------------------------------------
// GET /transcoding/{streamId}/live.m3u8
// ---------------------------------------------------------------------------

func transcodingLiveHandler(cfg config.Config, svc *TranscodingService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !cfg.Transcoding.Enable {
			transcodingDisabledError(w)
			return
		}

		streamID := extractPathParam(r, "streamId")
		job, ok := svc.TryResolveJob(streamID)
		if !ok {
			http.NotFound(w, r)
			return
		}

		if !job.Context.Live {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Context not live"})
			return
		}

		svc.Touch(job)

		m3u8Path := filepath.Join(job.Context.OutputDir, "index.m3u8")

		// Wait up to 60 seconds for file to appear.
		timeout := 60 * time.Second
		start := time.Now()

		// A client blocked here IS an active viewer — re-Touch every poll so the idle
		// watchdog can't reap the job (and RemoveAll its output dir out from under the
		// live ffmpeg) during a slow first-segment. Live re-encodes (esp. fmp4) can take
		// 10–15s to emit the first segment/index; the idle-live floor is 20s, so without
		// this the job was killed right as it started producing — the «Эконом» playlist
		// died with «rename index.m3u8.tmp: No such file or directory» / live.m3u8 404.
		for !fileExists(m3u8Path) && time.Since(start) < timeout {
			if job.HasExited() {
				break // ffmpeg died for a real reason — stop waiting, fall through to NotFound
			}
			svc.Touch(job)
			time.Sleep(250 * time.Millisecond)
		}
		if !fileExists(m3u8Path) {
			http.NotFound(w, r)
			return
		}

		// Wait for first segment reference.
		var m3u8 string
		reSegRef := regexp.MustCompile(`seg_[0-9]+\.(m4s|ts)`)
		for time.Since(start) < timeout {
			svc.Touch(job) // keep warm while the first segment is still being written
			data, err := os.ReadFile(m3u8Path)
			if err != nil {
				if job.HasExited() {
					break
				}
				time.Sleep(250 * time.Millisecond)
				continue
			}
			content := string(data)
			if reSegRef.MatchString(content) {
				m3u8 = content
				break
			}
			if job.HasExited() {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}

		if m3u8 == "" {
			http.NotFound(w, r)
			return
		}

		// Rewrite init.mp4 and segment URLs (keep relative).
		// No rewriting needed — segments are relative to the streamId path.

		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(m3u8))
	}
}

// ---------------------------------------------------------------------------
// GET /transcoding/{streamId}/main.m3u8
// ---------------------------------------------------------------------------

func transcodingMainHandler(cfg config.Config, svc *TranscodingService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !cfg.Transcoding.Enable {
			transcodingDisabledError(w)
			return
		}

		streamID := extractPathParam(r, "streamId")
		log.Debug().Str("streamId", streamID).Msg("transcoding: main.m3u8 request")

		job, ok := svc.TryResolveJob(streamID)
		if !ok {
			log.Warn().Str("streamId", streamID).Msg("transcoding: main.m3u8 job not found")
			http.NotFound(w, r)
			return
		}

		if job.Context.Live {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Context not playlist"})
			return
		}

		// Get duration from ffprobe.
		duration := ffprobeDuration(job.Context.FFProbe)
		log.Debug().Int("duration_sec", duration).Str("outputDir", job.Context.OutputDir).Msg("transcoding: main.m3u8 ffprobe duration")

		svc.Touch(job)

		// Best-effort path: we don't know the duration, so serve whatever
		// ffmpeg has written to index.m3u8 so far (event-style growing
		// playlist).  hls.js handles playlists without ENDLIST gracefully.
		if duration == 0 && job.Context.BestEffort {
			m3u8Path := filepath.Join(job.Context.OutputDir, "index.m3u8")
			wait := 60 * time.Second
			wStart := time.Now()
			for time.Since(wStart) < wait {
				if job.HasExited() && job.ExitCode() != 0 {
					logs := job.SnapshotLog()
					errMsg := "ffmpeg exited during best-effort wait"
					if len(logs) > 0 {
						errMsg = logs[len(logs)-1]
					}
					diag := diagnoseFFmpegExit(job.ExitCode(), logs)
					body := map[string]any{
						"error":       errMsg,
						"exitCode":    job.ExitCode(),
						"errorCode":   diag.Code,
						"userMessage": diag.UserMessage,
					}
					if len(diag.Suggestions) > 0 {
						body["suggestions"] = diag.Suggestions
					}
					writeJSON(w, http.StatusBadGateway, body)
					return
				}
				if data, err := os.ReadFile(m3u8Path); err == nil && strings.Contains(string(data), "seg_") {
					w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(data)
					return
				}
				time.Sleep(250 * time.Millisecond)
			}
			http.NotFound(w, r)
			return
		}

		if duration == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "duration"})
			return
		}

		// Check if ffmpeg process is alive — if already exited with error, fail fast.
		if job.HasExited() {
			ec := job.ExitCode()
			log.Warn().Int("exitCode", ec).Str("outputDir", job.Context.OutputDir).Msg("transcoding: ffmpeg already exited before m3u8 served")
			if ec != 0 {
				// Get last few lines from ffmpeg log for error details
				logs := job.SnapshotLog()
				errMsg := "ffmpeg exited with error"
				if len(logs) > 0 {
					errMsg = logs[len(logs)-1]
				}
				diag := diagnoseFFmpegExit(ec, logs)
				body := map[string]any{
					"error":       errMsg,
					"exitCode":    ec,
					"log":         logs,
					"errorCode":   diag.Code,
					"userMessage": diag.UserMessage,
				}
				if len(diag.Suggestions) > 0 {
					body["suggestions"] = diag.Suggestions
				}
				writeJSON(w, http.StatusBadGateway, body)
				return
			}
		}

		// Wait for the FIRST VIDEO SEGMENT before serving the m3u8.
		// Subtitle files and init.mp4 may appear quickly, but we must NOT
		// serve the VOD playlist until at least seg_00000 exists — otherwise
		// hls.js requests it, the server blocks for up to 60s waiting for
		// ffmpeg to produce it, and hls.js cancels after its own 20s
		// timeout → fragParsingError → eternal loading.
		firstSegExt := ".m4s"
		if job.Context.HLS.FMP4 {
			// already ".m4s"
		} else {
			firstSegExt = ".ts"
		}
		firstSegName := "seg_00000" + firstSegExt
		firstSegPath := filepath.Join(job.Context.OutputDir, firstSegName)

		timeout := 90 * time.Second
		start := time.Now()
		for time.Since(start) < timeout {
			if job.HasExited() && job.ExitCode() != 0 {
				logs := job.SnapshotLog()
				errMsg := "ffmpeg exited with error during wait"
				if len(logs) > 0 {
					errMsg = logs[len(logs)-1]
				}
				log.Warn().Int("exitCode", job.ExitCode()).Strs("log", logs).Msg("transcoding: ffmpeg died while waiting for segments")
				diag := diagnoseFFmpegExit(job.ExitCode(), logs)
				body := map[string]any{
					"error":       errMsg,
					"exitCode":    job.ExitCode(),
					"log":         logs,
					"errorCode":   diag.Code,
					"userMessage": diag.UserMessage,
				}
				if len(diag.Suggestions) > 0 {
					body["suggestions"] = diag.Suggestions
				}
				writeJSON(w, http.StatusBadGateway, body)
				return
			}

			if fileExists(firstSegPath) {
				entries, _ := os.ReadDir(job.Context.OutputDir)
				var names []string
				for _, e := range entries {
					names = append(names, e.Name())
				}
				log.Debug().Strs("files", names).Dur("waited", time.Since(start)).Msg("transcoding: first segment ready")
				break
			}

			if time.Since(start) > 5*time.Second && int(time.Since(start).Seconds())%10 == 0 {
				log.Debug().Dur("waited", time.Since(start)).Msg("transcoding: still waiting for first segment...")
			}
			time.Sleep(250 * time.Millisecond)
		}

		if !fileExists(firstSegPath) {
			log.Warn().Str("segment", firstSegName).Msg("transcoding: timeout waiting for first segment")
		}

		segDur := job.Context.HLS.SegDur
		segExt := "m4s"
		if !job.Context.HLS.FMP4 {
			segExt = "ts"
		}

		var b strings.Builder
		b.WriteString("#EXTM3U\n")
		b.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
		if job.Context.HLS.FMP4 {
			b.WriteString("#EXT-X-VERSION:7\n")
		} else {
			b.WriteString("#EXT-X-VERSION:3\n")
		}
		b.WriteString(fmt.Sprintf("#EXT-X-TARGETDURATION:%d\n", segDur))
		b.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")

		if job.Context.HLS.FMP4 {
			b.WriteString("#EXT-X-MAP:URI=\"init.mp4\"\n")
		}

		// Stream-copy + карта ключевых кадров: объявляем те границы, которые hlsenc и нарежет
		// (см. keyframes.go). Иначе — прежняя равномерная сетка.
		numSegments := duration / segDur
		if segs := job.segMap(2 * time.Second); segs != nil && videoCopied(job.Context.Mode) && segs.count() > 0 {
			maxDur := 0.0
			for _, d := range segs.Durs {
				maxDur = math.Max(maxDur, d)
			}
			b.Reset()
			b.WriteString("#EXTM3U\n")
			b.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
			if job.Context.HLS.FMP4 {
				b.WriteString("#EXT-X-VERSION:7\n")
			} else {
				b.WriteString("#EXT-X-VERSION:3\n")
			}
			b.WriteString(fmt.Sprintf("#EXT-X-TARGETDURATION:%d\n", int(math.Ceil(maxDur))))
			b.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
			if job.Context.HLS.FMP4 {
				b.WriteString("#EXT-X-MAP:URI=\"init.mp4\"\n")
			}
			for i, d := range segs.Durs {
				b.WriteString(fmt.Sprintf("#EXTINF:%.3f,\n", d))
				b.WriteString(fmt.Sprintf("seg_%05d.%s\n", i, segExt))
			}
			numSegments = segs.count()
		} else {
			for i := range numSegments {
				b.WriteString(fmt.Sprintf("#EXTINF:%d.0,\n", segDur))
				b.WriteString(fmt.Sprintf("seg_%05d.%s\n", i, segExt))
			}
		}

		b.WriteString("#EXT-X-ENDLIST\n")

		log.Debug().Int("numSegments", numSegments).Int("segDur", segDur).Bool("fmp4", job.Context.HLS.FMP4).Bool("keyframeMap", job.segMapNow() != nil).Msg("transcoding: serving main.m3u8")

		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(b.String()))
	}
}

// ---------------------------------------------------------------------------
// GET /transcoding/{streamId}/{file}
// ---------------------------------------------------------------------------

func transcodingSegmentHandler(cfg config.Config, svc *TranscodingService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !cfg.Transcoding.Enable {
			transcodingDisabledError(w)
			return
		}

		streamID := extractPathParam(r, "streamId")
		file := extractPathParam(r, "file")

		// P3.K2: when the URL carries a rung prefix (e.g.
		// /transcoding/{id}/v1/seg_00000.m4s), the file path resolves
		// inside that variant's subdirectory.  All other handler
		// behaviour — wait loops, seek-on-miss, content-type detection
		// — is identical, so we just prepend the prefix once here.
		if rung := extractPathParam(r, "rung"); rung != "" {
			file = filepath.Join("v"+rung, file)
		}

		job, ok := svc.TryResolveJob(streamID)
		if !ok {
			http.NotFound(w, r)
			return
		}

		svc.Touch(job)

		// Parse segment index for VOD seek logic.
		var segmentIndex *int
		if !job.Context.Live && file != "" {
			m := reSegIndex.FindStringSubmatch(file)
			if m != nil {
				if idx, err := strconv.Atoi(m[1]); err == nil {
					segmentIndex = &idx
				}
			}
		}

		timeout := 60 * time.Second
		start := time.Now()

		resolved := svc.GetFilePath(job, file)

		// VOD seek logic — if segment doesn't exist, seek to it.
		if !job.Context.Live && resolved == "" && !strings.Contains(file, ".vtt") && segmentIndex != nil {
			segDur := job.Context.HLS.SegDur
			ss := *segmentIndex * segDur
			// С картой ключевых кадров сегмент начинается ровно в ключевом кадре — туда и -ss.
			exact := 0.0
			if segs := job.segMapNow(); segs != nil && videoCopied(job.Context.Mode) {
				exact = segs.start(*segmentIndex)
				ss = int(exact)
			}
			alreadyAt := job.Context.HLS.Seek == ss
			if exact > 0 || job.Context.HLS.SeekExact > 0 {
				alreadyAt = job.Context.HLS.SeekExact == exact
			}

			goSeek := false
			if job.Context.HLS.Seek == 0 && 30 > ss {
				// First 30 seconds — don't seek.
			} else if alreadyAt {
				// Already at the right position.
			} else {
				goSeek = job.Context.HLS.Seek > ss
				if !goSeek {
					goSeek = true
					ext := filepath.Ext(file)
					segsPerMinute := max(int(float64(30)/float64(segDur)), 1)
					startIdx := max(*segmentIndex-segsPerMinute, 0)
					for i := startIdx; i < *segmentIndex; i++ {
						candidate := fmt.Sprintf("seg_%05d%s", i, ext)
						if svc.GetFilePath(job, candidate) != "" {
							goSeek = false
							break
						}
					}
				}
				if goSeek {
					svc.seekAsyncExact(streamID, ss, exact, segmentIndex)
				}
			}

			// Wait for segment to appear.
			for time.Since(start) < timeout {
				time.Sleep(200 * time.Millisecond)
				resolved = svc.GetFilePath(job, file)
				if resolved != "" {
					break
				}
			}
		}

		if resolved == "" {
			http.NotFound(w, r)
			return
		}

		// Wait for the current segment to be fully written by checking that
		// the NEXT segment has appeared (meaning ffmpeg closed this one).
		// Use a separate, shorter timeout (15s) so we don't eat the full
		// 60s budget that was needed to wait for the segment itself.
		//
		// NOTE: We do NOT use a "file size stable" heuristic because torrent
		// streams can stall ffmpeg for seconds between writes, causing false
		// "stable" signals on incomplete segments → fragParsingError in hls.js.
		if segmentIndex != nil {
			nextSeg := fmt.Sprintf("seg_%05d", *segmentIndex+1)
			ext := ".m4s"
			if !job.Context.HLS.FMP4 {
				ext = ".ts"
			}
			nextPath := filepath.Join(job.OutputDir, nextSeg+ext)

			const nextTimeout = 15 * time.Second
			nextStart := time.Now()
			for time.Since(nextStart) < nextTimeout {
				if fileExists(nextPath) {
					break
				}
				// If ffmpeg exited, the segment is as complete as it'll get.
				if job.HasExited() {
					break
				}
				time.Sleep(200 * time.Millisecond)
			}
		}

		// Open and serve file.
		f, err := os.Open(resolved)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()

		ext := filepath.Ext(resolved)
		ct, ok := transcodingContentTypes[ext]
		if !ok {
			ct = "application/octet-stream"
		}

		stat, _ := f.Stat()
		if stat != nil {
			w.Header().Set("Content-Length", strconv.FormatInt(stat.Size(), 10))
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Accept-Ranges", "bytes")
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, f)

		if segmentIndex != nil {
			svc.ReportSegmentAccess(job, *segmentIndex)
		}
	}
}

// ---------------------------------------------------------------------------
// GET /transcoding/{streamId}/diagnostics — P3.Q
//
// Returns a single JSON snapshot of everything we know about a job: smart
// mode + reasoning, detected client profile + known issues for that
// device family, subtitle plan + chosen burn-in target, ABR ladder, HW
// acceleration state, recent stderr lines, exit code if known, segment
// progress, escalation history.
//
// Operators use this for support tickets ("client says playback fails on
// their TV") and for verifying that the smart-mode policies fired the way
// the user expected.  The response is intentionally verbose — it's a
// debug surface, not a production hot path.
// ---------------------------------------------------------------------------

func transcodingDiagnosticsHandler(cfg config.Config, svc *TranscodingService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !cfg.Transcoding.Enable {
			transcodingDisabledError(w)
			return
		}

		streamID := extractPathParam(r, "streamId")
		job, ok := svc.TryResolveJob(streamID)
		if !ok {
			http.NotFound(w, r)
			return
		}

		// Don't Touch — diagnostics shouldn't extend the idle timeout
		// of an inactive job operators are inspecting after the fact.

		body := buildDiagnosticsSnapshot(job)
		writeJSON(w, http.StatusOK, body)
	}
}

// buildDiagnosticsSnapshot is exposed as a separate function so it can be
// unit-tested without spinning up an HTTP server.
func buildDiagnosticsSnapshot(job *TranscodingJob) map[string]any {
	if job == nil {
		return map[string]any{"error": "no job"}
	}

	out := map[string]any{
		"id":           job.ID,
		"streamId":     job.StreamID,
		"startedUtc":   job.StartedUtc.Unix(),
		"mode":         string(job.Mode),
		"profile":      job.ProfileLabel,
		"hasExited":    job.HasExited(),
		"exitCode":     job.ExitCode(),
		"lastSegment":  job.LastSegmentIndex(),
		"isLive":       job.Context.Live,
		"isBestEffort": job.Context.BestEffort,
		"hasSubtitles": job.Context.Subtitles,
	}

	if job.OriginalURL != "" {
		out["originalUrl"] = job.OriginalURL
	}
	if job.Warning != "" {
		out["warning"] = job.Warning
	}
	if len(job.KnownIssues) > 0 {
		out["knownIssues"] = job.KnownIssues
	}

	// Subtitle plan — what the planner decided and why.
	if job.Context.SubPlan.Strategy != "" {
		out["subtitlePlan"] = summarizeSubtitlesForResponse(job.Context.SubPlan)
	}

	// ABR ladder — prefer the ctx-stored ladder (reflects what the job
	// is *actually* emitting, including operator caps from MaxLadderRungs)
	// and fall back to the pure planner output for legacy / Phase-1
	// jobs that didn't store one.
	w, h := extractVideoDimensions(job.Context.FFProbe)
	var ladder []transcode.ABRRung
	if len(job.Context.Ladder) > 0 {
		ladder = job.Context.Ladder
	} else {
		ladder = transcode.PlanABRLadder(w, h, job.Mode)
	}
	if len(ladder) > 0 {
		rungs := make([]map[string]any, 0, len(ladder))
		for _, r := range ladder {
			rungs = append(rungs, map[string]any{
				"label":       r.Label,
				"width":       r.Width,
				"height":      r.Height,
				"bitrateKbps": r.BitrateKbps,
				"primary":     r.Primary,
			})
		}
		out["abrLadder"] = map[string]any{
			"rungs":     rungs,
			"multiRung": job.Context.MultiRung,
		}
	}

	// Source video summary.
	out["sourceWidth"] = w
	out["sourceHeight"] = h
	if pixFmt := extractPixFmt(job.Context.FFProbe); pixFmt != "" {
		out["sourcePixFmt"] = pixFmt
		out["sourceIs10Bit"] = is10BitPixFmt(pixFmt)
	}

	// HLS context.
	out["hls"] = map[string]any{
		"segDur":    job.Context.HLS.SegDur,
		"winSize":   job.Context.HLS.WinSize,
		"fmp4":      job.Context.HLS.FMP4,
		"seek":      job.Context.HLS.Seek,
		"seekExact": job.Context.HLS.SeekExact,
	}
	// Карта ключевых кадров: откуда, сколько, применена ли к плейлисту.
	if km := job.keyframes(); km != nil {
		kf := map[string]any{"source": km.Source, "keyframes": len(km.Times), "headerEnd": km.HeaderEnd, "applied": videoCopied(job.Context.Mode)}
		if segs := job.segMapNow(); segs != nil {
			kf["segments"] = segs.count()
		}
		out["keyframeMap"] = kf
	}

	// Recent stderr (cap at 20 lines for the snapshot — full history
	// stays available via the existing /transcoding/{id}/status endpoint).
	logs := job.SnapshotLog()
	if len(logs) > 20 {
		logs = logs[len(logs)-20:]
	}
	if len(logs) > 0 {
		out["recentLog"] = logs

		// Run the same classifier the auto-restart cascade uses, so
		// operators can see what error class the system would treat
		// the most recent stderr as.
		class := classifyFFmpegStderr(logs)
		if class != ErrUnknown {
			out["lastErrorClass"] = string(class)
		}
	}

	// HW state.
	out["disableHWForJob"] = job.Context.DisableHW

	// Per-client playback positions (for segment cleanup decisions).
	if minPos, ok := job.MinPosition(); ok {
		out["minClientPosition"] = minPos
	}

	return out
}

// extractPixFmt pulls the pix_fmt from the first video stream of a probe.
// Helper to keep buildDiagnosticsSnapshot readable.
func extractPixFmt(probe map[string]any) string {
	if probe == nil {
		return ""
	}
	streams, _ := probe["streams"].([]any)
	for _, s := range streams {
		sm, _ := s.(map[string]any)
		if sm == nil {
			continue
		}
		if fmt.Sprint(sm["codec_type"]) != "video" {
			continue
		}
		if pf, ok := sm["pix_fmt"].(string); ok {
			return pf
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// GET /transcoding/{streamId}/master.m3u8 — P3.J
//
// Returns a master playlist that wraps the existing main.m3u8 as a single
// video rendition and declares each extracted WebVTT stream as a SUBTITLES
// rendition group.  This is the URL the plugin should hand to the player
// when sub track switching matters; main.m3u8 still works for legacy
// clients that ignore EXT-X-MEDIA tags.
// ---------------------------------------------------------------------------

func transcodingMasterHandler(cfg config.Config, svc *TranscodingService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !cfg.Transcoding.Enable {
			transcodingDisabledError(w)
			return
		}

		streamID := extractPathParam(r, "streamId")
		job, ok := svc.TryResolveJob(streamID)
		if !ok {
			http.NotFound(w, r)
			return
		}

		svc.Touch(job)

		// ABR multi-rung: ffmpeg writes the variant subdirs (v0/, v1/) and needs
		// a few seconds to emit the first segment + variant index. main.m3u8
		// blocks on its first ROOT segment for exactly this reason; do the
		// symmetric wait here so a client that follows the master (now the
		// default landing for multi-rung — see jobUsesMasterPlaylist) doesn't
		// hammer v0/v1 index playlists with cold 404s before ffmpeg produced
		// them. Single-rung master wraps main.m3u8, which self-blocks, so skip.
		if jobUsesMasterPlaylist(job) {
			// Wait for the variant playlist the client will fetch first, not the
			// segment: ffmpeg writes seg_00000 then index.m3u8.tmp → rename, so
			// the index (with its EXT-X-MAP + first #EXTINF) is the last thing to
			// land. Gating on it means the client never sees a half-written or
			// missing v0/index.m3u8.
			firstIdx := filepath.Join(job.Context.OutputDir, "v0", "index.m3u8")
			wStart := time.Now()
			for time.Since(wStart) < 90*time.Second {
				if fileExists(firstIdx) {
					break
				}
				if job.HasExited() && job.ExitCode() != 0 {
					logs := job.SnapshotLog()
					errMsg := "ffmpeg exited before first variant segment"
					if len(logs) > 0 {
						errMsg = logs[len(logs)-1]
					}
					diag := diagnoseFFmpegExit(job.ExitCode(), logs)
					body := map[string]any{
						"error":       errMsg,
						"exitCode":    job.ExitCode(),
						"log":         logs,
						"errorCode":   diag.Code,
						"userMessage": diag.UserMessage,
					}
					if len(diag.Suggestions) > 0 {
						body["suggestions"] = diag.Suggestions
					}
					writeJSON(w, http.StatusBadGateway, body)
					return
				}
				time.Sleep(250 * time.Millisecond)
			}
		}

		base := fmt.Sprintf("%s/transcoding/%s", transcodingHost(r), streamID)
		body := buildMasterPlaylist(job, base)

		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}
}

// ---------------------------------------------------------------------------
// GET /transcoding/{streamId}/subs_{si}.m3u8 — P3.J
//
// Returns a one-segment HLS subtitle playlist that points at the existing
// subs_{si}.vtt file.  Required for clients to consume the WebVTT track as
// part of the master playlist's SUBTITLES rendition group.
// ---------------------------------------------------------------------------

func transcodingSubsRenditionHandler(cfg config.Config, svc *TranscodingService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !cfg.Transcoding.Enable {
			transcodingDisabledError(w)
			return
		}

		streamID := extractPathParam(r, "streamId")
		si := extractPathParam(r, "si")
		_, err := strconv.Atoi(si)
		if err != nil {
			http.Error(w, "invalid si", http.StatusBadRequest)
			return
		}
		job, ok := svc.TryResolveJob(streamID)
		if !ok {
			http.NotFound(w, r)
			return
		}
		svc.Touch(job)

		// Best-effort duration from probe.format.duration.  When unknown
		// we fall back to 9999 inside buildSubtitleRenditionPlaylist.
		durSec := 0.0
		if format, ok := job.Context.FFProbe["format"].(map[string]any); ok {
			if d, ok := format["duration"].(string); ok {
				if v, err := strconv.ParseFloat(strings.TrimSpace(d), 64); err == nil {
					durSec = v
				}
			}
		}

		// Map AbsIndex (in URL) back to the VTT filename.  ffmpeg writes
		// subs_<absIndex>.vtt, matching the index baked into our master
		// playlist URI generator.
		body := buildSubtitleRenditionPlaylist(fmt.Sprintf("subs_%s.vtt", si), durSec)

		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}
}

// ---------------------------------------------------------------------------
// GET /transcoding/{streamId}/subtitles
// ---------------------------------------------------------------------------

func transcodingSubtitlesHandler(cfg config.Config, svc *TranscodingService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !cfg.Transcoding.Enable {
			transcodingDisabledError(w)
			return
		}

		streamID := extractPathParam(r, "streamId")
		job, ok := svc.TryResolveJob(streamID)
		if !ok {
			http.NotFound(w, r)
			return
		}

		svc.Touch(job)

		host := transcodingHost(r)
		tc := cfg.Transcoding

		var subs []map[string]string

		if job.Context.Subtitles {
			streams, _ := job.Context.FFProbe["streams"].([]any)
			for _, s := range streams {
				sm, _ := s.(map[string]any)
				if sm == nil {
					continue
				}
				if fmt.Sprint(sm["codec_type"]) != "subtitle" {
					continue
				}
				codecName := fmt.Sprint(sm["codec_name"])
				if !stringSliceContains(tc.Subtitle.Codec, codecName) {
					continue
				}
				subIdx := toInt(sm["index"])
				if subIdx == 0 {
					continue
				}

				// Try to get subtitle name from tags.
				name := ""
				if tags, ok := sm["tags"].(map[string]any); ok {
					if t, ok := tags["title"].(string); ok && t != "" {
						name = t
					}
				}
				if name == "" {
					name = fmt.Sprintf("sub_%d", subIdx)
				}

				uri := fmt.Sprintf("%s/transcoding/%s/subs_%d.vtt", host, streamID, subIdx)
				entry := map[string]string{
					"label": name,
					"url":   uri,
					"codec": codecName,
				}
				// ASS/SSA: advertise the styled twin (subs_<idx>.ass sibling)
				// plus the embedded-fonts endpoint so a client with an
				// on-device ASS renderer skips the styling-stripped WebVTT.
				// Legacy clients ignore the extra keys and keep using `url`.
				if isAdvancedTextSubtitle(codecName) {
					entry["assUrl"] = fmt.Sprintf("%s/transcoding/%s/subs_%d.ass", host, streamID, subIdx)
					entry["fontsUrl"] = fmt.Sprintf("%s/transcoding/%s/fonts", host, streamID)
				}
				subs = append(subs, entry)
			}
		}

		if subs == nil {
			subs = []map[string]string{}
		}
		writeJSON(w, http.StatusOK, subs)
	}
}

// ---------------------------------------------------------------------------
// GET /transcoding/{streamId}/heartbeat
// ---------------------------------------------------------------------------

func transcodingHeartbeatHandler(cfg config.Config, svc *TranscodingService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !cfg.Transcoding.Enable {
			transcodingDisabledError(w)
			return
		}

		streamID := extractPathParam(r, "streamId")
		job, ok := svc.TryResolveJob(streamID)
		if !ok {
			http.NotFound(w, r)
			return
		}

		svc.Touch(job)

		// P2.3: optional playback position from query (`?pos=123.4`).
		// When provided we record it keyed by the client fingerprint
		// (ip+ua hash) so segmentCleanupLoop can avoid nuking data the
		// slowest active viewer still needs.
		if posStr := r.URL.Query().Get("pos"); posStr != "" {
			if pos, err := strconv.ParseFloat(posStr, 64); err == nil && pos >= 0 {
				job.UpdatePosition(clientFingerprint(r), pos)
			}
		}

		w.WriteHeader(http.StatusOK)
	}
}

// clientFingerprint returns a short stable hash derived from the remote
// IP (preferring X-Forwarded-For when present) and user agent.  Used to
// track per-client playback positions across heartbeats.
func clientFingerprint(r *http.Request) string {
	ip := r.Header.Get("X-Forwarded-For")
	if ip == "" {
		ip = r.RemoteAddr
	}
	if idx := strings.Index(ip, ","); idx >= 0 {
		ip = strings.TrimSpace(ip[:idx])
	}
	// Drop port so reconnects from the same client share a key.
	if idx := strings.LastIndex(ip, ":"); idx >= 0 {
		ip = ip[:idx]
	}
	ua := r.UserAgent()
	if len(ua) > 64 {
		ua = ua[:64]
	}
	return ip + "|" + ua
}

// ---------------------------------------------------------------------------
// GET /transcoding/{streamId}/stop
// ---------------------------------------------------------------------------

func transcodingStopHandler(cfg config.Config, svc *TranscodingService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !cfg.Transcoding.Enable {
			transcodingDisabledError(w)
			return
		}

		streamID := extractPathParam(r, "streamId")
		if svc.StopAsync(streamID) {
			w.WriteHeader(http.StatusOK)
		} else {
			http.NotFound(w, r)
		}
	}
}

// ---------------------------------------------------------------------------
// GET /transcoding/{streamId}/status
// ---------------------------------------------------------------------------

var reOutTimeMs = regexp.MustCompile(`out_time_ms=(\d+)`)

func transcodingStatusHandler(cfg config.Config, svc *TranscodingService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !cfg.Transcoding.Enable {
			transcodingDisabledError(w)
			return
		}

		streamID := extractPathParam(r, "streamId")
		job, ok := svc.TryResolveJob(streamID)
		if !ok {
			http.NotFound(w, r)
			return
		}

		now := time.Now().UTC()
		uptime := now.Sub(job.StartedUtc).Seconds()

		logs := job.SnapshotLog()

		// Parse out_time_ms from log.
		var timeMs uint64
		for i := len(logs) - 1; i >= 0; i-- {
			if !strings.Contains(logs[i], "out_time_ms=") {
				continue
			}
			m := reOutTimeMs.FindStringSubmatch(logs[i])
			if len(m) < 2 {
				continue
			}
			parsed, err := strconv.ParseUint(m[1], 10, 64)
			if err == nil {
				timeMs = parsed
				break
			}
		}

		positionSec := float64(job.Context.HLS.Seek)
		if timeMs > 0 {
			positionSec += float64(timeMs) / 1_000_000.0
		}

		var exitCode *int
		if job.HasExited() {
			ec := job.ExitCode()
			exitCode = &ec
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"streamId":        job.StreamID,
			"state":           job.State(),
			"outputDirectory": job.OutputDir,
			"startedUtc":      job.StartedUtc,
			"lastAccessUtc":   job.LastAccess(),
			"uptime":          uptime,
			"positionSec":     int(positionSec),
			"exitCode":        exitCode,
			"ffprobe":         job.Context.FFProbe,
			"log":             logs,
		})
	}
}

// ---------------------------------------------------------------------------
// GET /transcoding — API documentation
// ---------------------------------------------------------------------------

func transcodingDocHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		endpoints := []map[string]any{
			{
				"path":        "/transcoding/start.m3u8",
				"method":      "GET",
				"description": "Start transcoding with query parameters and redirect to HLS playlist",
			},
			{
				"path":        "/transcoding/start",
				"method":      "POST",
				"description": "Start transcoding by POSTing a JSON body. Returns StreamId and playlist URL",
			},
			{
				"path":        "/transcoding/{streamId}/live.m3u8",
				"method":      "GET",
				"description": "Returns the HLS live playlist for the given transcoding job",
			},
			{
				"path":        "/transcoding/{streamId}/main.m3u8",
				"method":      "GET",
				"description": "Returns the HLS VOD playlist for the given transcoding job",
			},
			{
				"path":        "/transcoding/{streamId}/{file}",
				"method":      "GET",
				"description": "Serves individual segment/init files. Supports range requests.",
			},
			{
				"path":        "/transcoding/{streamId}/seek/{ss}",
				"method":      "GET",
				"description": "Seek to position in seconds (live mode only). Returns 200 on success.",
			},
			{
				"path":        "/transcoding/{streamId}/heartbeat",
				"method":      "GET",
				"description": "Touch the job to keep it alive. Returns 200 if job exists.",
			},
			{
				"path":        "/transcoding/{streamId}/stop",
				"method":      "GET",
				"description": "Stop the transcoding job.",
			},
			{
				"path":        "/transcoding/{streamId}/status",
				"method":      "GET",
				"description": "Return current job status, uptime, position and log.",
			},
		}
		writeJSON(w, http.StatusOK, endpoints)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func ffprobeDuration(probe map[string]any) int {
	if probe == nil {
		return 0
	}
	format, ok := probe["format"].(map[string]any)
	if !ok {
		return 0
	}
	durStr, ok := format["duration"].(string)
	if !ok {
		return 0
	}
	// Duration can be "123.456" or "123,456".
	durStr = strings.ReplaceAll(durStr, ",", ".")
	parts := strings.Split(durStr, ".")
	if len(parts) == 0 {
		return 0
	}
	d, _ := strconv.Atoi(parts[0])
	return d
}

func extractPathParam(r *http.Request, name string) string {
	return chi.URLParam(r, name)
}

// ---------------------------------------------------------------------------
// GET /ffprobe?media=URL — standalone ffprobe endpoint
// ---------------------------------------------------------------------------

func ffprobeHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		media := strings.TrimSpace(r.URL.Query().Get("media"))
		if media == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "media parameter required"})
			return
		}

		log.Debug().Str("media", media).Msg("ffprobe: request received")

		result, errMsg := RunFFProbeStandalone(cfg, media, r.Host)
		if errMsg != "" {
			log.Warn().Str("media", media).Str("error", errMsg).Msg("ffprobe: failed")
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": errMsg})
			return
		}

		log.Debug().Str("media", media).Msg("ffprobe: success")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(result)
	}
}

// RunFFProbeStandalone runs ffprobe on the given URL without requiring TranscodingService.
// requestHost is the Host header from the incoming request (e.g. "beta.example.com").
func RunFFProbeStandalone(cfg config.Config, src string, requestHost string) (map[string]any, string) {
	ffprobePath := "ffprobe"
	tc := cfg.Transcoding
	if tc.FFmpeg != "" && tc.FFmpeg != "ffmpeg" {
		dir := filepath.Dir(tc.FFmpeg)
		candidate := filepath.Join(dir, "ffprobe")
		if _, err := os.Stat(candidate); err == nil {
			ffprobePath = candidate
		}
	}

	// Rewrite public URL to local TorrServer for /ts/ paths, but ONLY
	// when the URL points to THIS lampac instance (same host).  External
	// TorrServer URLs (e.g. fox.root.sx:500/ts/stream/...) must NOT be
	// rewritten — their TorrServer is remote, not on 127.0.0.1.
	//
	// Handles both in-process torrs (→ localhost:{lampac_port}/ts/...)
	// and external TorrServer (→ localhost:{ts_port}/...).
	probeSrc := src
	tsAvailable := torrsIsInProcess() || cfg.TorrServer.Port > 0 || cfg.TorrServer.URL != ""
	isBox := transcodeIsBox(cfg)
	if (tsAvailable || isBox) && strings.Contains(src, "/ts/") {
		if parsed, err := url.Parse(src); err == nil && (isBox || ffprobeIsLocalTS(parsed.Host, requestHost)) {
			if idx := strings.Index(src, "/ts/"); idx >= 0 {
				localPath := src[idx+3:] // strip "/ts" prefix, keep "/stream/..."
				if base := tsDirectStreamBaseFor(cfg, srcOrigin(src), localPath, tsAvailable); base != "" {
					probeSrc = base + localPath
					log.Debug().Str("original", src).Str("rewritten", probeSrc).Msg("ffprobe: rewrite URL to TorrServer")
				}
			}
		}
	}

	// pidtor: pool backend (main's pool via the ts-pick oracle on a box, the
	// local pool otherwise) or the static TorrServer → rewrite via HTTP.
	// In-process torrs without a pool pick: fast path — add the magnet via the
	// Go API and read torrent data directly into a temp file (no HTTP).
	if strings.Contains(src, "/lite/pidtor/s") {
		if rewritten, ok := ffprobeRewritePidtor(cfg, src); ok {
			probeSrc = rewritten
		} else if torrsIsInProcess() {
			if result, errMsg := ffprobeDirectPidtor(ffprobePath, src); result != nil || errMsg != "" {
				return result, errMsg
			}
		}
	}

	// In-process torrent streams: probe directly from torrent reader via temp
	// file.  This avoids HTTP round-trips and seeking issues (AVI has index at
	// EOF which blocks the torrent reader for un-downloaded pieces).
	// Done AFTER all rewrites so it catches both direct /ts/stream URLs and
	// rewritten pidtor URLs.
	if torrsIsInProcess() && strings.Contains(probeSrc, "/stream") {
		if parsed, err := url.Parse(probeSrc); err == nil {
			hash := parsed.Query().Get("link")
			rawIdx, _ := strconv.Atoi(parsed.Query().Get("index"))
			// index in URL is 1-based (MatriX convention); convert to 0-based.
			fileIdx := rawIdx - 1
			if fileIdx < 0 {
				fileIdx = 0
			}
			if hash != "" {
				result, errMsg := ffprobeTorrentDirect(ffprobePath, hash, fileIdx)
				if result != nil || errMsg != "" {
					return result, errMsg
				}
				// Fallback to HTTP probe below if direct failed to get reader.
			}
		}
	}

	args := []string{
		"-v", "error",
		"-print_format", "json",
		"-show_format", "-show_streams",
		"-timeout", "15000000", // 15s I/O timeout in microseconds
		probeSrc,
	}

	log.Debug().Str("ffprobe", ffprobePath).Strs("args", args).Msg("ffprobe: running command")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffprobePath, args...)

	var stderr strings.Builder
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		stderrStr := strings.TrimSpace(stderr.String())
		log.Warn().Err(err).Str("stderr", stderrStr).Str("ffprobe", ffprobePath).Str("src", probeSrc).Msg("ffprobe: command failed")
		errDetail := "ffprobe failed"
		if stderrStr != "" {
			errDetail = "ffprobe: " + stderrStr
		}
		return nil, errDetail
	}

	var result map[string]any
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, "ffprobe: invalid JSON output"
	}

	return result, ""
}

// ffprobeTorrentDirect probes a torrent file directly via the in-process
// torrent reader.  Buffers up to 20 MB in memory and feeds ffprobe via stdin
// (pipe:0) — avoids the disk round-trip the previous temp-file version did.
//
// 20 MB covers container headers + enough frames for ffprobe to identify all
// streams (incl. AVI with large JUNK/filler chunks).  ffprobe over a pipe
// can't seek, but our +ignidx flag already tells AVI not to look for the
// trailing index, and MP4/MKV headers are at the start.
func ffprobeTorrentDirect(ffprobePath, hash string, fileIdx int) (map[string]any, string) {
	srv := getTorrsServer()
	if srv == nil {
		return nil, "" // fallback to HTTP probe
	}

	reader, size, filename, err := srv.Stream(hash, fileIdx)
	if err != nil {
		log.Warn().Err(err).Str("hash", hash).Int("idx", fileIdx).Msg("ffprobe: direct stream failed")
		return nil, "" // fallback to HTTP probe
	}
	if closer, ok := reader.(interface{ Close() error }); ok {
		defer closer.Close()
	}

	probeBytes := int64(20 << 20) // 20 MB
	if probeBytes > size {
		probeBytes = size
	}

	// Pre-fetch the head bytes with a timeout so a slow torrent doesn't hang
	// ffprobe waiting on stdin.  Keeping the buffer in memory eliminates the
	// previous tmp-file write — saves ~20 MB of disk I/O per probe.
	type readResult struct {
		buf []byte
		err error
	}
	readDone := make(chan readResult, 1)
	go func() {
		buf := make([]byte, 0, probeBytes)
		w := bytes.NewBuffer(buf)
		_, err := io.CopyN(w, reader, probeBytes)
		if err == io.EOF {
			err = nil
		}
		readDone <- readResult{buf: w.Bytes(), err: err}
	}()

	var head []byte
	select {
	case res := <-readDone:
		if res.err != nil {
			log.Warn().Err(res.err).Str("hash", hash).Int("bytes", len(res.buf)).Msg("ffprobe: read torrent data failed")
			return nil, fmt.Sprintf("ffprobe: read stream: %s", res.err)
		}
		head = res.buf
	case <-time.After(30 * time.Second):
		log.Warn().Str("hash", hash).Msg("ffprobe: read torrent data timeout (30s)")
		return nil, "ffprobe: torrent data read timeout"
	}

	log.Debug().Str("hash", hash).Str("file", filename).Int("bytes", len(head)).Msg("ffprobe: probing torrent via pipe")

	// Run ffprobe with stdin = our buffered head bytes.  Flags tuned for
	// truncated torrent data on a non-seekable pipe:
	//   -fflags +ignidx  — don't look for AVI index at EOF (input is truncated)
	//   +genpts          — generate PTS from frame data
	//   +discardcorrupt  — skip corrupt frames at the truncation boundary
	//   -probesize       — scan up to the full head buffer
	//   -analyzeduration — analyze up to 10 s of content
	args := []string{
		"-v", "error",
		"-fflags", "+ignidx+genpts+discardcorrupt",
		"-probesize", strconv.Itoa(len(head)),
		"-analyzeduration", "10000000",
		"-print_format", "json",
		"-show_format", "-show_streams",
		"pipe:0",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffprobePath, args...)
	cmd.Stdin = bytes.NewReader(head)

	var stderr strings.Builder
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		stderrStr := strings.TrimSpace(stderr.String())
		log.Warn().Err(err).Str("stderr", stderrStr).Str("file", filename).Msg("ffprobe: direct probe failed")
		errDetail := "ffprobe failed"
		if stderrStr != "" {
			errDetail = "ffprobe: " + stderrStr
		}
		return nil, errDetail
	}

	var result map[string]any
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, "ffprobe: invalid JSON output"
	}

	return result, ""
}

// ffprobeDirectPidtor is a fast path for pidtor + in-process torrs.
// It adds the magnet directly via Go API (no HTTP), then probes the torrent
// data via temp file.  This avoids:
//   - HTTP POST timeout for magnet addition (Add blocks for metadata)
//   - HTTP GET for stream data (reads torrent reader directly)
func ffprobeDirectPidtor(ffprobePath, src string) (map[string]any, string) {
	srv := getTorrsServer()
	if srv == nil {
		return nil, ""
	}

	// Extract hash from URL path: /lite/pidtor/s{hash}
	_, after, ok := strings.Cut(src, "/lite/pidtor/s")
	if !ok {
		return nil, ""
	}

	hash := after
	queryPart := ""
	if before, after, ok := strings.Cut(after, "?"); ok {
		hash = before
		queryPart = after
	}
	if hash == "" {
		return nil, ""
	}

	// Extract tracker params and tsid.
	trQS := pidtorExtractTRFromQuery(queryPart)
	tsid := 1
	for part := range strings.SplitSeq(queryPart, "&") {
		if strings.HasPrefix(part, "tsid=") {
			fmt.Sscanf(part[5:], "%d", &tsid)
		}
	}

	// Build magnet and add directly via Go API (no HTTP round-trip).
	magnet := fmt.Sprintf("magnet:?xt=urn:btih:%s", hash)
	if trQS != "" {
		magnet += "&" + trQS
	}

	// tsid is 1-based (MatriX convention). Convert to 0-based for Stream().
	fileIdx := tsid - 1
	if fileIdx < 0 {
		fileIdx = 0
	}

	log.Info().Str("hash", hash).Int("tsid", tsid).Int("fileIdx", fileIdx).Msg("ffprobe: pidtor direct — adding magnet")

	_, err := srv.Add(magnet, "", "", "", false)
	if err != nil {
		log.Warn().Err(err).Str("hash", hash).Msg("ffprobe: pidtor direct — add magnet failed")
		return nil, ""
	}

	// Probe directly from torrent reader (0-based index).
	return ffprobeTorrentDirect(ffprobePath, hash, fileIdx)
}

// ffprobeRewritePidtor rewrites a pidtor stream URL to a direct TorrServer
// stream URL. It extracts the hash and tracker parameters, adds the magnet to
// TorrServer, and returns a localhost stream URL for ffprobe to probe directly.
func ffprobeRewritePidtor(cfg config.Config, src string) (string, bool) {
	// Extract hash from URL path: /lite/pidtor/s{hash}
	_, after, ok := strings.Cut(src, "/lite/pidtor/s")
	if !ok {
		return "", false
	}

	// Everything after "/lite/pidtor/s"
	rest := after

	// Split hash from query string
	hash := rest
	queryPart := ""
	if before, after, ok := strings.Cut(rest, "?"); ok {
		hash = before
		queryPart = after
	}
	if hash == "" {
		return "", false
	}

	// Extract tracker params and tsid
	trQS := pidtorExtractTRFromQuery(queryPart)
	tsid := 1
	for part := range strings.SplitSeq(queryPart, "&") {
		if strings.HasPrefix(part, "tsid=") {
			fmt.Sscanf(part[5:], "%d", &tsid)
		}
	}

	magnet := fmt.Sprintf("magnet:?xt=urn:btih:%s", hash)
	if trQS != "" {
		magnet += "&" + trQS
	}

	// Get TorrServer host — tsDirectBaseURL handles in-process vs external.
	tsBase := tsDirectBaseURL(cfg)

	// For the magnet API call we need explicit auth headers (the URL
	// may contain userinfo which http.Client strips for non-GET).
	tsHeaders := map[string]string{}
	if torrsIsInProcess() {
		// In-process: use X-Lampac-Go header to bypass auth middleware.
		tsHeaders["X-Lampac-Go"] = "1"
	} else if cfg.TorrServer.Password != "" {
		login := cfg.TorrServer.Login
		if login == "" {
			login = "ts"
		}
		cred := login + ":" + cfg.TorrServer.Password
		tsHeaders["Authorization"] = "Basic " + base64.StdEncoding.EncodeToString([]byte(cred))
	}

	// Use the clean (no-userinfo) host for API calls.
	var tsHostClean string
	if torrsIsInProcess() {
		// In-process: API is on lampac-go's own /ts/torrents endpoint.
		tsHostClean = "http://127.0.0.1" + cfg.Server.Addr + "/ts"
	} else {
		tsHostClean = strings.TrimRight(cfg.TorrServer.URL, "/")
		if tsHostClean == "" {
			port := cfg.TorrServer.Port
			if port <= 0 {
				port = 9080
			}
			tsHostClean = fmt.Sprintf("http://127.0.0.1:%d", port)
		}
	}

	// TS-balancer pool: the magnet ADD and the subsequent STREAM must hit the
	// SAME backend (TorrServer is stateful per torrent) — pick it by infohash,
	// exactly like the /ts proxy would, and override all three targets. On a
	// remote transcode box the pick comes from the MAIN's pool via the ts-pick
	// oracle (origin of the src), so pidtor spreads over the whole balancer
	// pool instead of the box's static TorrServer.
	tsAvail := torrsIsInProcess() || cfg.TorrServer.Port > 0 || strings.TrimSpace(cfg.TorrServer.URL) != ""
	if b := tsPickBackend(srcOrigin(src), hash); b != nil {
		host, authHdr := b.Target()
		tsBase = tsBackendBaseURL(b)
		tsHostClean = strings.TrimRight(host, "/")
		tsHeaders = map[string]string{}
		if authHdr != "" {
			tsHeaders["Authorization"] = authHdr
		}
	} else if !tsAvail || torrsIsInProcess() {
		// No pool pick: with no local TorrServer at all leave the URL alone so
		// ffmpeg fetches the main's resolve URL over HTTP (→ /proxy); with
		// in-process torrs the caller takes its direct (no-HTTP) path.
		return "", false
	}

	// 20s like the main's pidtor path: MatriX "add" blocks until the torrent
	// metadata arrives, which for a cold magnet is routinely >10s.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	client := &http.Client{Timeout: 20 * time.Second}
	tsHash, err := pidtorTSAddMagnet(ctx, client, tsHostClean, tsHeaders, magnet)
	if err != nil {
		log.Warn().Err(err).Str("hash", hash).Str("ts", tsHostClean).Msg("ffprobe: pidtor add magnet failed")
		return "", false
	}

	// Stream URL: tsBase already includes /ts prefix for in-process mode.
	streamURL := fmt.Sprintf("%s/stream?link=%s&index=%d&play", tsBase, tsHash, tsid)
	log.Info().Str("hash", hash).Str("ts", tsHostClean).Int("tsid", tsid).Msg("pidtor: magnet added on TorrServer, ffmpeg reads it directly")
	return streamURL, true
}

// TSPoolBackendFor returns the TS-balancer backend that should serve infohash
// (pool primary when infohash is ""), or nil when the pool is inactive/empty
// or torrs runs in-process — the caller then falls back to the static config
// URL (legacy single-TorrServer deployments, unchanged behaviour).
//
// Server-side fetches carry no user context, so no group filter is applied.
// A group-restricted user's /ts pick may therefore differ; the torrent simply
// gets added on the transcoder's backend too — harmless duplication.
func TSPoolBackendFor(infohash string) *torrbalancer.Backend {
	if torrsIsInProcess() || liveTSBalancerPool() == nil || !liveTSBalancerPool().HasEnabledBackends() {
		return nil
	}
	return liveTSBalancerPool().PickForHash(strings.ToLower(strings.TrimSpace(infohash)), nil)
}

// tsBackendBaseURL renders a pool backend as a base URL with its credentials
// embedded as userinfo for ffmpeg/ffprobe. QueryEscape, NOT PathEscape: a
// password like "pw@!" must become "pw%40%21" — PathEscape leaves '@' bare,
// producing the malformed "login:pw@!@host" userinfo seen in prod logs.
func tsBackendBaseURL(b *torrbalancer.Backend) string {
	host, _ := b.Target()
	base := strings.TrimRight(host, "/")
	if strings.TrimSpace(b.Password) != "" {
		login := strings.TrimSpace(b.Login)
		if login == "" {
			login = "ts" // default TorrServer username
		}
		userinfo := url.QueryEscape(login) + ":" + url.QueryEscape(b.Password)
		base = strings.Replace(base, "://", "://"+userinfo+"@", 1)
	}
	return base
}

// tsInfohashFromPath extracts the sticky routing key (infohash) from a
// "/stream/...?link=X&hash=Y" local path — the same derivation the /ts proxy
// uses, so both land on the same backend.
func tsInfohashFromPath(localPath string) string {
	qIdx := strings.Index(localPath, "?")
	if qIdx < 0 {
		return ""
	}
	q, err := url.ParseQuery(localPath[qIdx+1:])
	if err != nil {
		return ""
	}
	return torrbalancer.ExtractInfohash(q.Get("link"), q.Get("hash"))
}

// tsDirectStreamBase is the pool-aware tsDirectBaseURL for STREAM fetches:
// given the /stream local path it returns the base URL of the backend that
// stickily owns that torrent — honouring health/quarantine, and spreading
// transcode-added torrents across the pool instead of piling them all onto
// the static config host (prod 2026-07-16: 7-0-0 распределение — every
// transcode bypassed the balancer straight to the wedged backend).
func tsDirectStreamBase(cfg config.Config, localPath string) string {
	if b := TSPoolBackendFor(tsInfohashFromPath(localPath)); b != nil {
		return tsBackendBaseURL(b)
	}
	return tsDirectBaseURL(cfg)
}

// tsDirectBaseURL returns the direct TorrServer base URL for ffprobe/ffmpeg
// access.
//
// When torrs is running in-process, routes are served by lampac-go itself
// on /ts/* paths, so returns http://127.0.0.1:{lampac_port}/ts.  The /ts
// prefix is kept because callers append "/stream/..." (already stripped /ts
// from the original URL) and /ts/stream/* is whitelisted in auth middleware.
//
// When using an external TorrServer, returns http://127.0.0.1:{ts_port}
// (or the configured URL) with Basic Auth embedded as userinfo.
func tsDirectBaseURL(cfg config.Config) string {
	// In-process torrs: streams are served by lampac-go itself.
	// No auth embedding needed — /ts/stream/* is whitelisted.
	if torrsIsInProcess() {
		return "http://127.0.0.1" + cfg.Server.Addr + "/ts"
	}

	// External TorrServer.
	var base string
	if u := strings.TrimSpace(cfg.TorrServer.URL); u != "" {
		base = strings.TrimRight(u, "/")
	} else {
		port := cfg.TorrServer.Port
		if port <= 0 {
			port = 9080
		}
		base = fmt.Sprintf("http://127.0.0.1:%d", port)
	}

	// Embed Basic Auth in URL for ffprobe/ffmpeg.
	if pw := strings.TrimSpace(cfg.TorrServer.Password); pw != "" {
		login := strings.TrimSpace(cfg.TorrServer.Login)
		if login == "" {
			login = "ts"
		}
		userinfo := url.PathEscape(login) + ":" + url.PathEscape(pw)
		base = strings.Replace(base, "://", "://"+userinfo+"@", 1)
	}

	return base
}

// ffprobeIsLocalTS checks whether urlHost refers to the same lampac instance
// as requestHost.  Only then is it safe to rewrite /ts/ URLs to 127.0.0.1.
//
// Matches:  "" (relative), "beta.example.com" == "beta.example.com",
//
//	"beta.example.com:443" == "beta.example.com".
//
// Rejects:  "fox.root.sx:500" != "beta.example.com".
func ffprobeIsLocalTS(urlHost, requestHost string) bool {
	if urlHost == "" {
		return true // relative URL — belongs to this server
	}
	// Strip port from both for comparison.
	uh := urlHost
	if i := strings.LastIndex(uh, ":"); i >= 0 {
		uh = uh[:i]
	}
	rh := requestHost
	if i := strings.LastIndex(rh, ":"); i >= 0 {
		rh = rh[:i]
	}
	return strings.EqualFold(uh, rh)
}
