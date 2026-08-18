package transcodesvc

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/config"
)

// transcoding_remote.go — offload transcoding to a dedicated box ([transcoding] remote_host). The MAIN
// server mints HMAC-signed /transcoding/start.m3u8 URLs that point at the box; the box (same
// remote_secret) verifies the signature before spawning ffmpeg, so it can be a public endpoint without
// becoming an open SSRF / CPU-abuse hole. Once started, the box serves the HLS playlist + segments from
// its own host straight to the client (CPU + segment traffic leave the main server entirely).

const transcodeRemoteTTL = 6 * time.Hour

// transcodeRemoteCfg returns the box host (no trailing slash) + shared secret, read live so an admin
// edit applies without a restart.
func transcodeRemoteCfg() (host, secret string) {
	tc := liveConfig(config.Config{}).Transcoding
	return strings.TrimRight(strings.TrimSpace(tc.RemoteHost), "/"), strings.TrimSpace(tc.RemoteSecret)
}

// RemoteHost is the public base of the dedicated transcode box ("" = transcode locally).
func RemoteHost() string {
	h, _ := transcodeRemoteCfg()
	return h
}

// transcodeIsBox reports whether THIS process is a remote transcode worker (secret set, no remote_host
// of its own) and must therefore require a valid signature on every /transcoding/start request.
func transcodeIsBox(cfg config.Config) bool {
	return strings.TrimSpace(cfg.Transcoding.RemoteSecret) != "" && strings.TrimSpace(cfg.Transcoding.RemoteHost) == ""
}

// transcodeSign is HMAC-SHA256 (hex) over "src\nexp" — the start-URL signature.
func transcodeSign(secret, src string, exp int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(src + "\n" + strconv.FormatInt(exp, 10)))
	return hex.EncodeToString(mac.Sum(nil))
}

// transcodeVerify checks a start request's signature + expiry in constant time. Used on the box.
func transcodeVerify(secret, src, expStr, sig string) bool {
	if secret == "" || src == "" || expStr == "" || sig == "" {
		return false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	want := transcodeSign(secret, src, exp)
	return subtle.ConstantTimeCompare([]byte(want), []byte(sig)) == 1
}

// transcodeRemoteGateGET enforces the HMAC signature on a remote box's GET /transcoding/start.m3u8
// (src+exp+sig in the query). On the main / local server (not a box) it's a no-op. Returns false (and
// writes 403) when the box receives an unsigned or forged start — keeping the public box from being an
// open SSRF / CPU-abuse endpoint.
func transcodeRemoteGateGET(cfg config.Config, w http.ResponseWriter, r *http.Request) bool {
	if !transcodeIsBox(cfg) {
		return true
	}
	q := r.URL.Query()
	if transcodeVerify(strings.TrimSpace(cfg.Transcoding.RemoteSecret), strings.TrimSpace(q.Get("src")), q.Get("exp"), q.Get("sig")) {
		return true
	}
	http.Error(w, "forbidden", http.StatusForbidden)
	return false
}

// transcodeCORSMiddleware lets the TV web client (page served from tv.alcopa.cc) fetch the dedicated
// box's HLS playlist + segments cross-origin. Harmless on a same-origin (local) transcode too.
func transcodeCORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "Range")
		h.Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// StartURL builds the client-facing /transcoding/start.m3u8?src=… URL for a source. With a
// remote box configured it returns the box's absolute, HMAC-signed URL; otherwise the local
// self-referential URL (unchanged). localHost is "scheme://host" of this server.
func StartURL(localHost, src string) string {
	base := localHost
	extra := ""
	if rh, secret := transcodeRemoteCfg(); rh != "" {
		base = rh
		if secret != "" {
			exp := time.Now().Add(transcodeRemoteTTL).Unix()
			extra = "&exp=" + strconv.FormatInt(exp, 10) + "&sig=" + transcodeSign(secret, src, exp)
		}
	}
	return base + "/transcoding/start.m3u8?src=" + url.QueryEscape(src) + extra
}
