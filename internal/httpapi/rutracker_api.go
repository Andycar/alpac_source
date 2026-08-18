package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/config"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

// rutrackerParseHandler resolves a topic to something playable. It is the
// target of the parselink we put on unresolved rows, and it is registered
// BEFORE the generic /parse/* jacred proxy so it wins the route.
//
// Default answer is the .torrent file, not a magnet redirect: consumers of
// this link are TorrServer instances, which fetch an HTTP link and expect a
// torrent — a 302 to a magnet: URI makes their HTTP client fail with
// "unsupported protocol scheme". Clients that do want a magnet ask for one
// with ?magnet=1 (or Accept-less browsers hitting the fallback below when the
// .torrent cannot be downloaded).
func rutrackerParseHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		liveCfg := liveConfig(cfg)
		client := rutrackerReady(liveCfg)
		if client == nil {
			http.Error(w, "rutracker disabled", http.StatusServiceUnavailable)
			return
		}

		idStr := strings.TrimSpace(chi.URLParam(r, "id"))
		if idStr == "" {
			// Also accept /parse/rutracker?id=123 (jacred's parsemagnet style).
			idStr = strings.TrimSpace(r.URL.Query().Get("id"))
		}
		topicID, err := strconv.Atoi(strings.TrimSuffix(idStr, ".torrent"))
		if err != nil || topicID <= 0 {
			http.Error(w, "bad topic id", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		wantMagnet := r.URL.Query().Get("magnet") == "1"
		if !wantMagnet {
			if data, err := client.TorrentFile(ctx, topicID); err == nil {
				w.Header().Set("Content-Type", "application/x-bittorrent")
				w.Header().Set("Content-Disposition", "attachment; filename=\""+strconv.Itoa(topicID)+".torrent\"")
				w.Header().Set("Access-Control-Allow-Origin", "*")
				_, _ = w.Write(data)
				return
			} else {
				log.Debug().Err(err).Int("topic", topicID).Msg("rutracker: .torrent download failed, falling back to magnet")
			}
		}

		magnet, err := client.ResolveMagnet(ctx, topicID)
		if err != nil || magnet == "" {
			log.Warn().Err(err).Int("topic", topicID).Msg("rutracker: resolve failed")
			http.Error(w, "resolve failed", http.StatusBadGateway)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		http.Redirect(w, r, magnet, http.StatusFound)
	}
}
