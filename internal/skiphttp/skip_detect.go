package skiphttp

import (
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/introdetect"
	"lampac-go/internal/skipdb"

	"github.com/rs/zerolog/log"
)

// newDetector поднимает introdetect на ffmpeg транскодера (у него есть muxer chromaprint —
// проверяем; без него детектор выключен и ручка не регистрируется).
func newDetector(cfg config.Config, db *skipdb.DB) *introdetect.Detector {
	if !cfg.SkipIntro.Enable {
		return nil
	}
	ffmpeg := strings.TrimSpace(cfg.Transcoding.FFmpeg)
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	if !introdetect.HasChromaprint(ffmpeg) {
		// Настроенный ffmpeg без chromaprint (jellyfin-сборка его несёт, дистрибутивный — тоже,
		// но не всякая статическая) — пробуем системный.
		if ffmpeg == "ffmpeg" || !introdetect.HasChromaprint("ffmpeg") {
			log.Info().Str("ffmpeg", ffmpeg).Msg("skip: ffmpeg without chromaprint — auto intro detection off")
			return nil
		}
		ffmpeg = "ffmpeg"
	}
	ffprobe := "ffprobe"
	if ffmpeg != "ffmpeg" {
		ffprobe = filepath.Join(filepath.Dir(ffmpeg), "ffprobe")
	}
	save := func(imdbID string, season, episode int, segs []introdetect.Segment) {
		// Ручные/чужие метки не трогаем: авто-результат — только там, где пусто.
		if len(db.Lookup(imdbID, season, episode)) > 0 {
			return
		}
		out := make([]skipdb.Segment, 0, len(segs))
		for _, sg := range segs {
			out = append(out, skipdb.Segment{Type: sg.Type, Start: sg.Start, End: sg.End, Provider: "auto-chromaprint", Confidence: sg.Confidence})
		}
		if err := db.Set(imdbID, season, episode, out); err != nil {
			log.Warn().Err(err).Str("imdb", imdbID).Int("s", season).Int("e", episode).Msg("skip: auto segments not saved")
			return
		}
		log.Info().Str("imdb", imdbID).Int("s", season).Int("e", episode).Int("segments", len(out)).Msg("skip: auto segments saved")
	}
	log.Info().Str("ffmpeg", ffmpeg).Msg("skip: auto intro detection (chromaprint) on")
	return introdetect.New(ffmpeg, ffprobe, save)
}

// skipDetectHandler — POST /api/skip/detect {imdb_id, season, episode, src, others:[{episode,src}]}.
// Отвечает сразу: {"queued":true} — задание принято, {"segments":[…]} — уже известно,
// {"queued":false} — сезон уже разбирается. Клиент перечитывает /api/skip через минуту-две.
func skipDetectHandler(db *skipdb.DB, det *introdetect.Detector) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req introdetect.Request
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad json"})
			return
		}
		req.ImdbID = strings.TrimSpace(req.ImdbID)
		if req.ImdbID == "" || req.Season <= 0 || req.Episode <= 0 || !streamURLAllowed(req.Src) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "imdb_id, season, episode, src required"})
			return
		}
		others := req.Others[:0]
		for _, o := range req.Others {
			if o.Episode > 0 && streamURLAllowed(o.Src) && len(others) < 4 {
				others = append(others, o)
			}
		}
		req.Others = others
		if segs := db.Lookup(req.ImdbID, req.Season, req.Episode); len(segs) > 0 {
			writeJSON(w, http.StatusOK, map[string]any{"queued": false, "segments": segs})
			return
		}
		if !detectAllowed(remoteIP(r)) {
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"queued": false, "error": "rate limited"})
			return
		}
		req.UA = r.Header.Get("User-Agent")
		queued := det.Enqueue(req)
		writeJSON(w, http.StatusAccepted, map[string]any{"queued": queued})
	}
}

// streamURLAllowed — детектор читает файлы только по http(s) и только с внешних адресов: без
// file://, без loopback/приватных сетей (ffmpeg от имени сервера не должен становиться SSRF-щупом).
func streamURLAllowed(raw string) bool {
	raw = strings.TrimSpace(raw)
	if len(raw) >= 2048 || !(strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://")) {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() {
			return false
		}
	}
	return true
}

// detectLimiter — не больше detectPerIP заданий в час с одного адреса: каждое задание = до трёх
// ffmpeg по нескольким сотням мегабайт. Дедупликация по сезону есть в детекторе, это второй барьер.
const detectPerIP = 12

var (
	detectMu   sync.Mutex
	detectHits = map[string][]time.Time{}
)

func detectAllowed(ip string) bool {
	now := time.Now()
	detectMu.Lock()
	defer detectMu.Unlock()
	hits := detectHits[ip][:0]
	for _, t := range detectHits[ip] {
		if now.Sub(t) < time.Hour {
			hits = append(hits, t)
		}
	}
	if len(hits) >= detectPerIP {
		detectHits[ip] = hits
		return false
	}
	detectHits[ip] = append(hits, now)
	if len(detectHits) > 5000 { // не копим адреса вечно
		for k, v := range detectHits {
			if len(v) == 0 || now.Sub(v[len(v)-1]) > time.Hour {
				delete(detectHits, k)
			}
		}
	}
	return true
}

func remoteIP(r *http.Request) string {
	if v := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); v != "" {
		return v
	}
	if v := strings.TrimSpace(r.Header.Get("X-Real-IP")); v != "" {
		return v
	}
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}
