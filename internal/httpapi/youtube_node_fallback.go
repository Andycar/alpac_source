package httpapi

import (
	"bytes"
	encjson "encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"lampac-go/internal/cluster"
	"lampac-go/internal/config"
)

// youtubeWithNodeFallback — «YouTube дополнительно с нод». liteMainNodeOnly держит
// youtube на primary (кэш форматов и mux-задачи живут в его памяти), поэтому
// обычный форвард по правилам кластера сюда не дотягивается. Обёртка делает
// иначе: сначала своя экстракция; если она провалилась или дала только ≤360p
// (так выглядит сгоревший выход — 429/бот-чек, 2026-09-04), тот же запрос
// уходит на ноду с edge_url. Нода подписывает ссылки своим адресом (ytLinkHost),
// зритель смотрит с неё напрямую — IP-привязанные ссылки googlevideo main
// отдать не смог бы. Берём сильнейший из двух ответов.
func youtubeWithNodeFallback(next http.Handler, _ []string) http.Handler {
	fb := &ytNodeFallback{
		next: next,
		isPrimary: func() bool {
			return serverReady() && strings.EqualFold(liveConfig(config.Config{}).Cluster.Mode, "primary")
		},
		preferred: func() []string { return liveConfig(config.Config{}).YouTube.NodeFallback },
		nodes: func() []*cluster.Node {
			cp := liveClusterPool()
			if cp == nil {
				return nil
			}
			return cp.Nodes()
		},
		forward: func(w http.ResponseWriter, r *http.Request, n *cluster.Node) bool {
			cf := liveClusterFwd()
			return cf != nil && cf.Forward(w, r, n)
		},
		minHLS: func() float64 {
			v := liveConfig(config.Config{}).YouTube.HLSMinDuration
			if v == 0 {
				return 90
			}
			return float64(v)
		},
		now: time.Now,
	}
	return fb
}

// ytNodeFallback — зависимости вынесены в поля, чтобы ветку «нода сильнее»
// можно было проверить тестом без живого кластера. Список нод читается из
// live-конфига на каждый запрос: [youtube] node_fallback меняется по SIGHUP.
type ytNodeFallback struct {
	next      http.Handler
	isPrimary func() bool
	preferred func() []string
	nodes     func() []*cluster.Node
	forward   func(w http.ResponseWriter, r *http.Request, n *cluster.Node) bool
	minHLS    func() float64 // порог [youtube] hls_min_duration; <0 — DASH для длинных не считаем слабостью
	now       func() time.Time

	// Предохранитель: подряд идущие слабые/ошибочные локальные ответы — признак
	// сгоревшего выхода (бот-чек/429). Каждая новая локальная попытка — это ещё
	// 4 клиента × загрузка страницы с того же IP, то есть продление бана и
	// ~20 с ожидания зрителю. После ytBreakerAfter подряд на ytBreakerFor
	// спрашиваем ноду ПЕРВОЙ; сильный локальный ответ сбрасывает счётчик.
	mu        sync.Mutex
	badStreak int
	nodeFirst time.Time // до этого момента — нода первой
}

const (
	ytBreakerAfter = 2
	ytBreakerFor   = 3 * time.Minute
)

func (fb *ytNodeFallback) nodeFirstNow() bool {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	return fb.now().Before(fb.nodeFirst)
}

func (fb *ytNodeFallback) noteLocal(good bool) {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	if good {
		fb.badStreak = 0
		fb.nodeFirst = time.Time{}
		return
	}
	fb.badStreak++
	if fb.badStreak >= ytBreakerAfter {
		fb.nodeFirst = fb.now().Add(ytBreakerFor)
	}
}

func (fb *ytNodeFallback) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.Path != "/lite/youtube" || !fb.isPrimary() {
		fb.next.ServeHTTP(w, r) // только корневой вызов и только на primary — от петель
		return
	}
	videoID := r.URL.Query().Get("videoID")
	if fb.nodeFirstNow() {
		if remote, rv, node := fb.askNodes(r, func(rv ytInspection) bool { return rv.verdict == ytStrong }); remote != nil {
			log.Info().Str("videoID", videoID).Str("node", node.Name).Str("node_result", rv.label()).
				Msg("youtube: breaker open — served by node without local attempt")
			remote.flushTo(w)
			return
		}
	}
	local := newBufferedResponse()
	fb.next.ServeHTTP(local, r)
	lv := ytInspect(local.body.Bytes())
	// «Сильный, но DASH для длинного ролика» — тоже повод спросить ноду: DASH-URL
	// упирается в стену через ~60 с медиа, а HLS-лестницу main не достал (её
	// отдают web-клиенты, первыми попадающие под бот-чек выхода). Нода с чистым
	// IP обычно приносит master.m3u8 — и только им локальный ответ заменяем.
	longDASH := lv.verdict == ytStrong && !lv.hls && fb.minHLS() >= 0 && lv.duration >= fb.minHLS()
	if lv.verdict == ytStrong || lv.verdict == ytOther {
		fb.noteLocal(true)
	} else {
		fb.noteLocal(false)
	}
	if (lv.verdict == ytStrong && !longDASH) || lv.verdict == ytOther {
		local.flushTo(w)
		return
	}
	remote, rv, node := fb.askNodes(r, func(rv ytInspection) bool { return rv.verdict == ytStrong && (!longDASH || rv.hls) })
	if node != nil {
		log.Info().Str("videoID", videoID).Str("node", node.Name).Str("local", lv.label()).Str("node_result", rv.label()).Bool("taken", remote != nil).
			Msg("youtube: local result weak — asked a node")
	}
	if remote != nil {
		remote.flushTo(w)
		return
	}
	local.flushTo(w)
}

// askNodes обходит кандидатов, пока accept не примет ответ. Возвращает принятый
// ответ (или nil), вердикт и ноду последней попытки (nil — никто не ответил).
func (fb *ytNodeFallback) askNodes(r *http.Request, accept func(ytInspection) bool) (*bufferedResponse, ytInspection, *cluster.Node) {
	var lastNode *cluster.Node
	var lastRV ytInspection
	for _, node := range ytFallbackNodes(fb.nodes(), fb.preferred()) {
		fr := r.Clone(r.Context())
		fr.Header = r.Header.Clone()
		fr.Header.Del("Accept-Encoding") // ответ нужен читаемым — мы его разбираем
		remote := newBufferedResponse()
		if !fb.forward(remote, fr, node) {
			continue
		}
		lastNode, lastRV = node, ytInspect(remote.body.Bytes())
		if accept(lastRV) {
			return remote, lastRV, node
		}
	}
	return nil, lastRV, lastNode
}

// ytFallbackNodes — ноды-кандидаты: из [youtube] node_fallback (имя или id) в
// заданном порядке, иначе любые с edge_url. Без edge_url нода бесполезна:
// её ссылки зрителю не достать.
func ytFallbackNodes(all []*cluster.Node, preferred []string) []*cluster.Node {
	var out []*cluster.Node
	for _, n := range all {
		if n == nil || !n.Enabled || strings.TrimSpace(n.EdgeURL) == "" {
			continue
		}
		if len(preferred) == 0 {
			out = append(out, n)
			continue
		}
		for _, p := range preferred {
			if strings.EqualFold(strings.TrimSpace(p), n.ID) || strings.EqualFold(strings.TrimSpace(p), n.Name) {
				out = append(out, n)
				break
			}
		}
	}
	if len(preferred) > 1 && len(out) > 1 {
		rank := map[string]int{}
		for i, p := range preferred {
			rank[strings.ToLower(strings.TrimSpace(p))] = i
		}
		key := func(n *cluster.Node) int {
			if i, ok := rank[strings.ToLower(n.ID)]; ok {
				return i
			}
			if i, ok := rank[strings.ToLower(n.Name)]; ok {
				return i
			}
			return len(preferred)
		}
		for i := 1; i < len(out); i++ {
			for j := i; j > 0 && key(out[j]) < key(out[j-1]); j-- {
				out[j], out[j-1] = out[j-1], out[j]
			}
		}
	}
	return out
}

type ytVerdict string

const (
	ytStrong ytVerdict = "strong" // есть качество ≥480p
	ytWeak   ytVerdict = "weak"   // только ≤360p — почерк сгоревшего выхода (format 18)
	ytError  ytVerdict = "error"  // {"error": …} или пустая выдача
	ytOther  ytVerdict = "other"  // не JSON / accsdb / что-то, во что не лезем
)

var ytHeightRe = regexp.MustCompile(`(\d{3,4})p`)

type ytInspection struct {
	verdict  ytVerdict
	hls      bool    // среди ссылок есть master.m3u8 — HLS-лестница, стены нет
	duration float64 // секунды, 0 если неизвестно
}

func (i ytInspection) label() string {
	if i.verdict == ytStrong && i.hls {
		return "strong+hls"
	}
	return string(i.verdict)
}

// ytInspect классифицирует ответ /lite/youtube. Две формы: плоская
// {qualitys, url, duration} и rjson=true — {type, data:[{qualitys, url, duration}]}
// (так спрашивают приложения). Порог 480p: format 18 (360p) — единственное, что
// отдаёт android-клиент, когда всё остальное легло.
func ytInspect(body []byte) ytInspection {
	var d map[string]encjson.RawMessage
	if err := encjson.Unmarshal(bytes.TrimSpace(body), &d); err != nil || d == nil {
		return ytInspection{verdict: ytOther}
	}
	if _, ok := d["accsdb"]; ok {
		return ytInspection{verdict: ytOther}
	}
	if raw, ok := d["error"]; ok {
		var msg string
		if encjson.Unmarshal(raw, &msg) == nil && strings.TrimSpace(msg) != "" {
			return ytInspection{verdict: ytError}
		}
	}
	item := d
	if raw, ok := d["data"]; ok {
		var list []map[string]encjson.RawMessage
		if encjson.Unmarshal(raw, &list) == nil {
			if len(list) == 0 {
				return ytInspection{verdict: ytError}
			}
			item = list[0]
		}
	}
	var out ytInspection
	_ = encjson.Unmarshal(item["duration"], &out.duration)
	var qs map[string]string
	if encjson.Unmarshal(item["qualitys"], &qs) != nil || len(qs) == 0 {
		_ = encjson.Unmarshal(item["quality"], &qs)
	}
	best := 0
	for label, u := range qs {
		if strings.Contains(u, "master.m3u8") {
			out.hls = true
		}
		if m := ytHeightRe.FindStringSubmatch(label); m != nil {
			if h, _ := strconv.Atoi(m[1]); h > best {
				best = h
			}
		}
	}
	var u string
	_ = encjson.Unmarshal(item["url"], &u)
	if strings.Contains(u, "master.m3u8") {
		out.hls = true
	}
	switch {
	case best == 0 && strings.TrimSpace(u) == "":
		out.verdict = ytError
	case best < 480:
		out.verdict = ytWeak
	default:
		out.verdict = ytStrong
	}
	return out
}

// ytResultVerdict — краткая форма для тестов и логов.
func ytResultVerdict(body []byte) ytVerdict { return ytInspect(body).verdict }

// bufferedResponse копит ответ обработчика, чтобы его можно было разобрать и
// при необходимости заменить ответом ноды.
type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newBufferedResponse() *bufferedResponse {
	return &bufferedResponse{header: http.Header{}, status: http.StatusOK}
}

func (b *bufferedResponse) Header() http.Header         { return b.header }
func (b *bufferedResponse) WriteHeader(code int)        { b.status = code }
func (b *bufferedResponse) Write(p []byte) (int, error) { return b.body.Write(p) }
func (b *bufferedResponse) Flush()                      {}

func (b *bufferedResponse) flushTo(w http.ResponseWriter) {
	for k, v := range b.header {
		if strings.EqualFold(k, "Content-Length") {
			continue
		}
		w.Header()[k] = v
	}
	w.WriteHeader(b.status)
	_, _ = w.Write(b.body.Bytes())
}
