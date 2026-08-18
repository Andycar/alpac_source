package httpapi

// wink_handler.go — HTTP surface for Wink live TV:
//
//	GET /wink/playlist.m3u8   full M3U of clear (non-DRM) channels; each entry
//	                          points back at /wink/{id}.m3u8 for fresh resolve
//	GET /wink/{id}.m3u8       resolve one channel's clear HLS now and 302 to the
//	                          proxied (/proxy/?pl=wink) stream
//	GET /wink/stats           clear vs DRM coverage (diagnostics)
//
// Channel CDN URLs are IP-bound to the session egress, so playback only works
// when /proxy/?pl=wink fetches through the same RU residential SOCKS5 (registered
// in newWinkChecker). Resolve is lazy per-play so URLs are always fresh.

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/proxylink"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

// registerWinkRoutes wires /wink/* when the Wink source is enabled with TV on.
func registerWinkRoutes(router chi.Router, cfg config.Config, proxyLinks *proxylink.Manager) {
	if !cfg.Online.Wink.Enable || !cfg.Online.Wink.TV {
		return
	}
	w := sharedWinkChecker(cfg)

	router.Get("/wink/playlist.m3u8", w.handlePlaylist(proxyLinks))
	router.Get("/wink/{id}.m3u8", w.handleChannelResolve(proxyLinks))
	router.Get("/wink/stats", w.handleStats)

	log.Info().Msg("wink: live TV routes enabled (/wink/playlist.m3u8)")
}

// handlePlaylist renders the clear-channel M3U. Each channel points at
// /wink/{id}.m3u8 so the real CDN URL is resolved + proxied at play time.
func (w *winkChecker) handlePlaylist(_ *proxylink.Manager) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
		defer cancel()

		list, err := w.getChannels(ctx)
		if err != nil {
			http.Error(rw, "wink: "+err.Error(), http.StatusBadGateway)
			return
		}
		themes := w.getTvDictionary(ctx)
		host := hostFromRequest(r)

		m3u := buildM3U(list, themes, func(c *winkChannel) string {
			return host + "/wink/" + strconv.Itoa(c.ID) + ".m3u8"
		})

		rw.Header().Set("Content-Type", "application/vnd.apple.mpegurl; charset=utf-8")
		rw.Header().Set("Cache-Control", "no-cache")
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte(m3u))
	}
}

// handleChannelResolve resolves a single channel's clear HLS now and redirects
// to the proxied stream.
func (w *winkChecker) handleChannelResolve(proxyLinks *proxylink.Manager) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		idStr := strings.TrimSuffix(chi.URLParam(r, "id"), ".m3u8")
		id, err := strconv.Atoi(idStr)
		if err != nil {
			http.Error(rw, "wink: bad channel id", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		streamURL, err := w.resolveChannelStream(ctx, id)
		if err != nil {
			http.Error(rw, "wink: "+err.Error(), http.StatusBadGateway)
			return
		}

		target := streamURL
		if proxyLinks != nil {
			headers := map[string]string{"User-Agent": w.ua}
			if hash := proxyLinks.EncryptURIWithHeaders(streamURL, clientIP(r), "wink", headers); hash != "" {
				target = hostFromRequest(r) + "/proxy/" + hash
			}
		}
		http.Redirect(rw, r, target, http.StatusFound)
	}
}

// handleStats reports clear vs DRM channel coverage — handy for verifying how
// much of the lineup is actually playable.
func (w *winkChecker) handleStats(rw http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()

	list, err := w.getChannels(ctx)
	if err != nil {
		writeJSON(rw, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	st := winkComputeStats(list)
	writeJSON(rw, http.StatusOK, map[string]any{
		"total":     st.Total,
		"clear":     st.Clear,
		"drm_only":  st.DRMOnly,
		"no_source": st.NoSource,
		"authed":    w.authed,
	})
}
