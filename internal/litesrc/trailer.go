package litesrc

import (
	stdjson "encoding/json"
	"net/http"
	"strings"
	"time"

	"lampac-go/internal/proxylink"

	"github.com/rs/zerolog/log"
)

// TrailerHandler proxies YouTube trailers through the server.
// Lampa sends the YouTube video ID (from TMDB /movie/{id}/videos response)
// and this handler extracts the stream URL via yt-dlp and returns a proxied URL.
//
// GET /lite/trailer?id={youtubeVideoID}
// → {"method":"play","url":"/proxy/{encrypted}","title":"Trailer"}
func TrailerHandler(ytChecker *YoutubeChecker, links *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		videoID := strings.TrimSpace(r.URL.Query().Get("id"))
		if videoID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing id param"})
			return
		}

		log.Debug().Str("videoID", videoID).Msg("trailer: extracting formats")

		// Use youtube checker to extract formats via yt-dlp.
		formats, _, usedProxy, ok := ytChecker.extractFormats(videoID)
		if !ok || len(formats) == 0 {
			log.Warn().Str("videoID", videoID).Msg("trailer: no formats extracted")
			writeJSON(w, http.StatusOK, map[string]any{"method": "play", "url": "", "title": "Trailer"})
			return
		}

		// Pick best combined format (video+audio), preferring H.264 for compatibility.
		var bestURL string
		var bestHeight int
		for _, f := range formats {
			if f.ACodec == "none" || f.VCodec == "none" {
				continue // skip video-only or audio-only
			}
			// Prefer H.264
			isH264 := strings.HasPrefix(f.VCodec, "avc")
			h := f.Height
			if isH264 && h >= bestHeight {
				bestURL = f.URL
				bestHeight = h
			} else if bestURL == "" && h >= bestHeight {
				bestURL = f.URL
				bestHeight = h
			}
		}

		// Fallback: if no combined format, pick best video-only and hope player handles it.
		if bestURL == "" {
			for _, f := range formats {
				if f.VCodec == "none" {
					continue
				}
				if f.Height >= bestHeight {
					bestURL = f.URL
					bestHeight = f.Height
				}
			}
		}

		if bestURL == "" {
			log.Warn().Str("videoID", videoID).Msg("trailer: no suitable format found")
			writeJSON(w, http.StatusOK, map[string]any{"method": "play", "url": "", "title": "Trailer"})
			return
		}

		// Proxy the URL through /proxy/ endpoint.
		var proxyURL string
		if links != nil {
			headers := map[string]string{
				"Referer": "https://www.youtube.com/",
			}
			_ = usedProxy // headers are enough for YouTube CDN
			proxyURL = links.EncryptURIWithHeaders(bestURL, clientIP(r), "trailer", headers)
		} else {
			proxyURL = bestURL
		}

		log.Info().Str("videoID", videoID).Int("height", bestHeight).Msg("trailer: serving")

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age="+strings.TrimSpace(time.Duration(30*time.Minute).String()))
		stdjson.NewEncoder(w).Encode(map[string]any{
			"method": "play",
			"url":    proxyURL,
			"title":  "Trailer",
		})
	}
}
