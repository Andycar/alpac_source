package httpapi

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"

	"github.com/rs/zerolog/log"
)

// json is the package-level jsoniter instance (declared in server.go) — reuse it instead of
// importing encoding/json (which would shadow that package-level var).

// Client weblog receiver. The ALPAC web/TV client (ddd-client) forwards its in-app weblog here so
// errors (and, in weblog mode, the full action trace) from devtools-less TVs land in the server
// terminal / journalctl. Best-effort + rate-limited: logging must never become an attack surface
// or a noise firehose. Gated by [web] weblog_collect (default true).

type weblogEntry struct {
	T    int64  `json:"t"`
	Lvl  string `json:"lvl"`
	Tag  string `json:"tag"`
	Msg  string `json:"msg"`
	Data string `json:"data"`
}

type weblogPayload struct {
	Sid  string        `json:"sid"`
	UA   string        `json:"ua"`
	Logs []weblogEntry `json:"logs"`
}

// StoredWebLog is a received client log line enriched with who/where, kept in a server-side ring
// buffer so the admin panel (/{adminPath}/capilog) can show ALL clients' web activity in one place
// without grepping journalctl.
type StoredWebLog struct {
	T   int64  `json:"t"`   // client timestamp (ms)
	RT  int64  `json:"rt"`  // server receive time (ms)
	Lvl string `json:"lvl"` // event|info|warn|error
	Tag string `json:"tag"`
	Msg string `json:"msg"`
	Dat string `json:"data,omitempty"`
	IP  string `json:"ip"`
	Sid string `json:"sid"` // per-page-load id (groups one TV session)
	UA  string `json:"ua"`
}

const webLogStoreMax = 3000

var (
	webLogStoreMu  sync.Mutex
	webLogStoreBuf []StoredWebLog
	webLogSeq      int64 // monotonic id so the admin page can long-poll only what's new
)

func webLogStoreAppend(e StoredWebLog) {
	webLogStoreMu.Lock()
	defer webLogStoreMu.Unlock()
	webLogStoreBuf = append(webLogStoreBuf, e)
	if len(webLogStoreBuf) > webLogStoreMax {
		webLogStoreBuf = webLogStoreBuf[len(webLogStoreBuf)-webLogStoreMax:]
	}
	webLogSeq++
}

// webLogSnapshot returns up to `limit` most-recent entries (optionally filtered), newest last.
func webLogSnapshot(limit int, lvl, tag, ip string) ([]StoredWebLog, int64) {
	webLogStoreMu.Lock()
	defer webLogStoreMu.Unlock()
	out := make([]StoredWebLog, 0, limit)
	for i := len(webLogStoreBuf) - 1; i >= 0 && len(out) < limit; i-- {
		e := webLogStoreBuf[i]
		if lvl != "" && lvl != "all" && e.Lvl != lvl {
			continue
		}
		if tag != "" && e.Tag != tag {
			continue
		}
		if ip != "" && e.IP != ip {
			continue
		}
		out = append(out, e)
	}
	// reverse → chronological (newest last) for the viewer
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, webLogSeq
}

func webLogClear() {
	webLogStoreMu.Lock()
	webLogStoreBuf = nil
	webLogStoreMu.Unlock()
}

// per-IP token bucket so one chatty/malicious client can't flood the log.
type weblogLimiter struct {
	mu      sync.Mutex
	buckets map[string]*weblogBucket
}
type weblogBucket struct {
	tokens float64
	last   time.Time
}

const (
	weblogBurst  = 200.0 // lines a client may burst (covers a page of trace on open)
	weblogPerSec = 5.0   // sustained lines/sec/IP afterwards
	weblogMaxLog = 120   // max entries accepted per request
)

func newWeblogLimiter() *weblogLimiter { return &weblogLimiter{buckets: map[string]*weblogBucket{}} }

func (l *weblogLimiter) allow(ip string, n int) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b := l.buckets[ip]
	if b == nil {
		b = &weblogBucket{tokens: weblogBurst, last: now}
		l.buckets[ip] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * weblogPerSec
	if b.tokens > weblogBurst {
		b.tokens = weblogBurst
	}
	b.last = now
	if len(l.buckets) > 5000 { // bound the map
		for k, v := range l.buckets {
			if now.Sub(v.last) > 10*time.Minute {
				delete(l.buckets, k)
			}
		}
	}
	allowed := n
	if float64(allowed) > b.tokens {
		allowed = int(b.tokens)
	}
	b.tokens -= float64(allowed)
	return allowed
}

func weblogCollectHandler(cfg config.Config) http.HandlerFunc {
	limiter := newWeblogLimiter()
	return func(w http.ResponseWriter, r *http.Request) {
		if !cfg.Web.WeblogCollect {
			http.NotFound(w, r)
			return
		}
		var p weblogPayload
		// cap the body so a huge POST can't OOM us
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256*1024)).Decode(&p); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		ip := clientIP(r)
		n := len(p.Logs)
		if n > weblogMaxLog {
			n = weblogMaxLog
		}
		allowed := limiter.allow(ip, n)
		ua := shortUA(p.UA)
		now := time.Now().UnixMilli()
		sid := e2sid(p.Sid)
		for i := 0; i < allowed; i++ {
			e := p.Logs[i]
			// 1) terminal (journalctl)
			ev := log.Info()
			switch e.Lvl {
			case "error":
				ev = log.Error()
			case "warn":
				ev = log.Warn()
			}
			ev.Str("src", "webclient").Str("ip", ip).Str("sid", sid).Str("ua", ua).Str("tag", e.Tag)
			if e.Data != "" {
				ev = ev.Str("data", e.Data)
			}
			ev.Msg("weblog: " + e.Msg)
			// 2) in-memory store for the admin /capilog page
			webLogStoreAppend(StoredWebLog{T: e.T, RT: now, Lvl: e.Lvl, Tag: e.Tag, Msg: e.Msg, Dat: e.Data, IP: ip, Sid: sid, UA: ua})
		}
		if allowed < len(p.Logs) {
			log.Warn().Str("ip", ip).Int("dropped", len(p.Logs)-allowed).Msg("weblog: rate-limited client logs")
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func e2sid(s string) string {
	if len(s) > 16 {
		return s[:16]
	}
	return s
}

// shortUA condenses a User-Agent to the bits that identify a TV model/runtime, dropping the long
// boilerplate so the terminal line stays readable.
func shortUA(ua string) string {
	ua = strings.TrimSpace(ua)
	if ua == "" {
		return ""
	}
	for _, kw := range []string{"Tizen", "Web0S", "webOS", "VIDAA", "HbbTV", "Hisense", "BRAVIA", "GoogleTV", "AndroidTV", "AFT", "CrKey", "DuneHD"} {
		if i := strings.Index(ua, kw); i >= 0 {
			end := i + len(kw) + 12
			if end > len(ua) {
				end = len(ua)
			}
			return ua[i:end]
		}
	}
	if len(ua) > 60 {
		return ua[:60]
	}
	return ua
}
