package iptvhttp

import (
	"net/http"
	"sync"

	"lampac-go/internal/config"
	"lampac-go/internal/iptv"
	"lampac-go/internal/proxylink"
	"lampac-go/internal/transcode"
	"lampac-go/internal/transcodesvc"

	"github.com/rs/zerolog/log"
)

// iptv_econom.go — «Эконом-режим» for live IPTV: an on-demand server-side re-encode
// of a channel to a lower resolution/bitrate. Single-bitrate FHD/sports channels ship
// 12–14 MB segments (~10 Mbps); a viewer whose pipe can't sustain that gets constant
// rebuffering/reconnects that NO client-side change can fix (there's no lower variant to
// switch to). This spawns one ffmpeg per channel — SHARED across everyone watching that
// channel — capped to cfg.IPTV.EconomMaxHeight, and hands back a /transcoding live playlist.
//
// Gated behind cfg.IPTV.EconomEnable AND cfg.Transcoding.Enable; the client opts in per play
// with ?econom=1, so a normal channel is never transcoded unless the user picks «Эконом».

// One ffmpeg per channel, shared by all its viewers. LookupInflightJob can't do this:
// it stops coalescing once a job starts streaming (a viewer joining after the first
// segment would spawn a second ffmpeg). This registry keys the LIVE session by source
// URL and reuses it as long as the job is alive.
var (
	econSessMu sync.Mutex
	econSess   = map[string]string{} // source stream URL → transcoding streamID
)

const econDefaultMaxHeight = 720

// iptvEconomPlaylist returns a /transcoding live playlist URL that re-encodes streamURL to
// a lower resolution, reusing a running session for the same source when one exists. Empty
// string = econom unavailable (disabled, no transcode service, or the scheduler is busy) —
// the caller then falls back to the direct stream.
func iptvEconomPlaylist(cfg config.Config, proxyLinks *proxylink.Manager, ch *iptv.Channel, rawURL, host string, transSvc *transcodesvc.TranscodingService) string {
	if rawURL == "" || ch == nil || !cfg.IPTV.EconomEnable || !cfg.Transcoding.Enable || transSvc == nil {
		return ""
	}

	econSessMu.Lock()
	defer econSessMu.Unlock()

	// Reuse a live session for this channel if it's still running. Keyed by the RAW upstream URL,
	// which is stable per channel (the /proxy hash rotates per-request).
	if sid, ok := econSess[rawURL]; ok {
		if job, alive := transSvc.TryResolveJob(sid); alive && !job.HasExited() && job.Context.Live {
			transSvc.Touch(job) // keep the shared session warm while anyone watches
			return host + "/transcoding/" + sid + "/live.m3u8"
		}
		delete(econSess, rawURL) // the previous session died — spawn a fresh one below
	}

	maxH := cfg.IPTV.EconomMaxHeight
	if maxH <= 0 {
		maxH = econDefaultMaxHeight
	}
	// ffmpeg's source: OUR OWN /proxy over loopback — the exact pipeline the client provably plays
	// through (uTLS Chrome fingerprint, UA/Referer injection, redirects, manifest rewriting). A
	// DIRECT upstream fetch fails on WAF'd providers (ffmpeg's openssl TLS fingerprint ≠ browser),
	// which is why the raw-URL variant died silently → job cleanup → live.m3u8 «404 page not found».
	// IPTV proxy tokens carry no VerifyIP flag, so the server's own fetch passes; loopback skips
	// nginx/TLS entirely. Fallback: raw URL + channel headers when the local address is unknown.
	src := ""
	ffHeaders := map[string]string{}
	if proxyLinks != nil && serverReady() {
		if lb := loopbackHostPort(liveConfig(config.Config{}).Server.Addr); lb != "" {
			proxyHeaders := map[string]string{}
			if ch.UserAgent != "" {
				proxyHeaders["User-Agent"] = ch.UserAgent
			}
			if ch.Referer != "" {
				proxyHeaders["Referer"] = ch.Referer
			}
			var hash string
			if len(proxyHeaders) > 0 {
				hash = proxyLinks.EncryptURIWithHeaders(rawURL, "", "iptv", proxyHeaders)
			} else {
				hash = proxyLinks.EncryptURI(rawURL, "", "iptv", false, false, false)
			}
			if hash != "" {
				src = "http://" + lb + "/proxy/" + hash
			}
		}
	}
	if src == "" {
		src = rawURL
		if ch.UserAgent != "" {
			ffHeaders["userAgent"] = ch.UserAgent
		}
		if ch.Referer != "" {
			ffHeaders["referer"] = ch.Referer
		}
	}
	// ForceTranscode makes selectMode pick a full re-encode regardless of the (browser) caps —
	// the goal here is fewer bytes, not codec compatibility. MaxHeight caps the downscale for
	// THIS job only (leaving the global VOD cap untouched).
	// 4s segments (paired with the live keyframe-forcing in createProcess): small files that load
	// fast on a thin pipe, a first segment within ~4s (short «нет картинки» + a small window before
	// the first viewer fetch keeps the job warm), and low live latency.
	job, errMsg := transSvc.Start(&transcodesvc.TranscodingStartRequest{
		Src:       src,
		Live:      true,
		MaxHeight: maxH,
		Headers:   ffHeaders,
		HLS:       &transcodesvc.HLSOpts{SegDur: 4},
		Client:    &transcode.ClientCaps{ForceTranscode: true, Platform: "browser"},
	})
	if job == nil {
		log.Warn().Str("err", errMsg).Msg("iptv econom: transcode start failed → direct stream")
		return ""
	}
	econSess[rawURL] = job.StreamID
	log.Info().Str("streamId", job.StreamID).Int("maxHeight", maxH).Bool("viaProxy", src != rawURL).Msg("iptv econom: live re-encode started")
	return host + "/transcoding/" + job.StreamID + "/live.m3u8"
}

// econEnabled reports whether the client may offer «Эконом» — surfaced in the /play payload
// so the picker only shows the option when the server can actually honour it.
func econEnabled(cfg config.Config, transSvc *transcodesvc.TranscodingService) bool {
	return cfg.IPTV.EconomEnable && cfg.Transcoding.Enable && transSvc != nil
}

// wantEconom reads the per-request opt-in.
func wantEconom(r *http.Request) bool {
	v := r.URL.Query().Get("econom")
	return v == "1" || v == "true"
}
