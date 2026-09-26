package transcodesvc

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/torrbalancer"

	"github.com/rs/zerolog/log"
)

// transcoding_tspick.go — the TS-balancer "pick oracle" between the MAIN server
// and a remote transcode box.
//
// Problem (prod 2026-08-23): PidTor and the web torrent browser hand the box a
// src like https://main/lite/pidtor/s<hash> or https://main/ts/stream/...; the
// box rewrites it onto "its" TorrServer — the static [torrserver] url of the
// box config (the weakest backend in prod) or a stale local pool copy — and
// the main's balancer pool (health, quarantine, HRW spread, admin edits) is
// bypassed entirely. Keeping a second copy of the pool on the box in sync by
// hand is fragile, so instead the box ASKS the main which backend stickily
// owns an infohash:
//
//	GET {main}/transcoding/ts-pick?hash=<infohash>&exp=<unix>&sig=<hmac>
//	→ 200 {"id","name","host","login","password"}   (pool active)
//	→ 204                                           (pool off / in-process)
//	→ 403                                           (bad/expired signature)
//
// signed with the shared [transcoding] remote_secret (same HMAC scheme as the
// start URLs, domain-separated by the "ts-pick\n" prefix). The main origin is
// taken from the src the box was asked to transcode — that src is itself
// HMAC-signed by the main, so it is trusted, and no extra box config is needed.
// ffmpeg/ffprobe on the box then stream straight from the chosen backend; the
// pick is cached briefly so probe + start + trickplay share one round-trip.
// Any oracle failure falls back to the pre-existing local behaviour.

const (
	tsPickTTL         = 60 * time.Second // positive cache: probe→start→trickplay of one play
	tsPickNegativeTTL = 10 * time.Second // don't hammer a main that just failed
	tsPickSignTTL     = 2 * time.Minute  // request signature validity
	tsPickTimeout     = 6 * time.Second
)

// tsPickSign is the request signature: HMAC over "ts-pick\n<hash>\n<exp>".
func tsPickSign(secret, hash string, exp int64) string {
	return transcodeSign(secret, "ts-pick\n"+strings.ToLower(strings.TrimSpace(hash)), exp)
}

func tsPickVerify(secret, hash, expStr, sig string) bool {
	return transcodeVerify(secret, "ts-pick\n"+strings.ToLower(strings.TrimSpace(hash)), expStr, sig)
}

type tsPickReply struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Host     string `json:"host"`
	Login    string `json:"login,omitempty"`
	Password string `json:"password,omitempty"`
}

// transcodingTSPickHandler serves the oracle on the MAIN server. It answers only
// when a remote_secret is configured and the signature checks out; the pool
// pick mirrors TSPoolBackendFor (HRW, health/quarantine gated, fail-open) so
// the box, the /ts proxy and pidtor all agree on the backend for a hash.
func transcodingTSPickHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		live := liveConfig(cfg)
		secret := strings.TrimSpace(live.Transcoding.RemoteSecret)
		q := r.URL.Query()
		hash := strings.ToLower(strings.TrimSpace(q.Get("hash")))
		if secret == "" || !tsPickVerify(secret, hash, q.Get("exp"), q.Get("sig")) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		b := TSPoolBackendFor(hash)
		if b == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		host, _ := b.Target()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(tsPickReply{
			ID:       b.ID,
			Name:     b.Name,
			Host:     host,
			Login:    b.Login,
			Password: b.Password,
		})
	}
}

// --- box side ---

type tsPickCacheEntry struct {
	backend *torrbalancer.Backend // nil = negative entry
	exp     time.Time
}

var tsPickCache = struct {
	sync.Mutex
	m map[string]tsPickCacheEntry
}{m: map[string]tsPickCacheEntry{}}

var tsPickClient = &http.Client{Timeout: tsPickTimeout}

// srcOrigin returns "scheme://host[:port]" of an absolute src URL ("" otherwise).
func srcOrigin(src string) string {
	u, err := url.Parse(strings.TrimSpace(src))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// remoteTSPick asks the main server (origin of the src) which pool backend
// owns infohash. Returns nil unless THIS process is a remote transcode box
// (remote_secret set, no remote_host), the origin is usable, and the main
// answered 200. Results (positive and negative) are cached briefly.
func remoteTSPick(origin, infohash string) *torrbalancer.Backend {
	live := liveConfig(config.Config{})
	if !transcodeIsBox(live) {
		return nil
	}
	secret := strings.TrimSpace(live.Transcoding.RemoteSecret)
	origin = strings.TrimRight(strings.TrimSpace(origin), "/")
	infohash = strings.ToLower(strings.TrimSpace(infohash))
	if secret == "" || origin == "" || infohash == "" {
		return nil
	}
	key := origin + "|" + infohash
	now := time.Now()
	tsPickCache.Lock()
	if e, ok := tsPickCache.m[key]; ok && now.Before(e.exp) {
		tsPickCache.Unlock()
		return e.backend
	}
	tsPickCache.Unlock()

	exp := now.Add(tsPickSignTTL).Unix()
	reqURL := origin + "/transcoding/ts-pick?hash=" + url.QueryEscape(infohash) +
		"&exp=" + strconv.FormatInt(exp, 10) + "&sig=" + tsPickSign(secret, infohash, exp)

	ctx, cancel := context.WithTimeout(context.Background(), tsPickTimeout)
	defer cancel()
	var picked *torrbalancer.Backend
	ttl := tsPickNegativeTTL
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err == nil {
		req.Header.Set("X-Lampac-Go", "1")
		resp, err := tsPickClient.Do(req)
		if err != nil {
			log.Warn().Err(err).Str("origin", origin).Str("hash", infohash).Msg("ts-pick: main unreachable, falling back to local TorrServer")
		} else {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
			switch resp.StatusCode {
			case http.StatusOK:
				var rep tsPickReply
				if json.Unmarshal(body, &rep) == nil && strings.TrimSpace(rep.Host) != "" {
					picked = torrbalancer.NewDetachedBackend(rep.ID, rep.Name, rep.Host, rep.Login, rep.Password)
					ttl = tsPickTTL
					log.Info().Str("hash", infohash).Str("backend", picked.Name).Str("host", picked.Host).Msg("ts-pick: main pool backend")
				} else {
					log.Warn().Str("origin", origin).Str("hash", infohash).Msg("ts-pick: unparsable reply")
				}
			case http.StatusNoContent:
				// Main has no active pool — honest answer, cache a bit longer.
				ttl = tsPickTTL
			default:
				log.Warn().Int("status", resp.StatusCode).Str("origin", origin).Str("hash", infohash).Msg("ts-pick: main refused (secret mismatch?)")
			}
		}
	}

	tsPickCache.Lock()
	// Opportunistic sweep so the map can't grow without bound.
	if len(tsPickCache.m) > 4096 {
		for k, e := range tsPickCache.m {
			if now.After(e.exp) {
				delete(tsPickCache.m, k)
			}
		}
	}
	tsPickCache.m[key] = tsPickCacheEntry{backend: picked, exp: now.Add(ttl)}
	tsPickCache.Unlock()
	return picked
}

// tsPickBackend is TSPoolBackendFor with the remote oracle in front: on a box
// the main's pool decides; everywhere else (and when the oracle is down) the
// local pool does, exactly as before.
func tsPickBackend(origin, infohash string) *torrbalancer.Backend {
	if b := remoteTSPick(origin, infohash); b != nil {
		return b
	}
	return TSPoolBackendFor(infohash)
}

// tsDirectStreamBaseFor resolves the ffmpeg-facing base URL for a /ts stream
// path: oracle/pool backend when one owns the hash, else the static config
// TorrServer when tsAvail, else "" (no rewrite — ffmpeg fetches the public URL).
func tsDirectStreamBaseFor(cfg config.Config, origin, localPath string, tsAvail bool) string {
	if b := tsPickBackend(origin, tsInfohashFromPath(localPath)); b != nil {
		return tsBackendBaseURL(b)
	}
	if !tsAvail {
		return ""
	}
	return tsDirectBaseURL(cfg)
}
