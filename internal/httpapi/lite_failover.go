package httpapi

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	stdjson "encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/cluster"
	"lampac-go/internal/proxyapi"

	"github.com/rs/zerolog/log"
)

// Добор на ноде: main пробует источник сам, и если контента нет — по очереди
// спрашивает ноды, отдавая зрителю первый ответ, где контент есть.
//
// Зачем. У AhueRezka ссылка привязана к адресу, который её добыл, а живые
// ссылки воркер выдаёт не каждому адресу и банит тех, кто спросил много.
// Какой адрес сейчас годится, заранее не знает никто: 21.09.2026 main и WARP
// были в бане, MSK2 — тоже, а DE и SPB получали играющие ссылки. Перебор по
// факту ответа находит рабочий без ручного переключения.
//
// Нода сама проверяет свою ссылку (litesrc: linkAlive) и мёртвую не отдаёт —
// поэтому «есть контент» в её ответе означает «играет», а не «воркер ответил».
// Видео потом отдаёт она же: ссылка подписана её edge_url.

// liteFailoverBalancers — источники, для которых включён добор.
var liteFailoverBalancers = map[string]bool{
	"ahuerezka": true,
}

// liteFailoverWalkNodes — после main обходить ещё и остальные ноды. Нужно,
// когда сам main бывает не в состоянии (AhueRezka: воркер банит его адрес).
var liteFailoverWalkNodes = map[string]bool{"ahuerezka": true}

// liteFailoverMaxNodes — сколько нод пробуем на один запрос: у каждой попытки
// своя задержка, и зритель не должен ждать обхода всего кластера.
const liteFailoverMaxNodes = 4

// liteFailoverWins — когда нода последний раз выручила источник: удачливые
// спрашиваются первыми. Ключ «балансер|id ноды».
var liteFailoverWins sync.Map

func liteFailoverBalancer(raw string) bool {
	seg := raw
	if i := strings.IndexAny(seg, "/?#"); i >= 0 {
		seg = seg[:i]
	}
	return liteFailoverBalancers[seg]
}

// serveLiteWithFailover — отдаёт первый ответ, в котором есть контент. Порядок:
// нода, которую выбрал балансировщик (first; может быть nil), затем сам main,
// затем другие ноды. Если контента нет нигде — честный пустой ответ.
func serveLiteWithFailover(w http.ResponseWriter, r *http.Request, raw string, local http.Handler, cp *cluster.Pool, cf *cluster.Forwarder, first *cluster.Node) {
	bal := raw
	if i := strings.IndexAny(bal, "/?#"); i >= 0 {
		bal = bal[:i]
	}
	var fallback *httptest.ResponseRecorder
	// Что ответила первая нода — для журнала: код, кодировка и длина тела.
	// Без этого «контента нет» не отличить от «пересыл не удался».
	var firstCode int
	var firstEnc string
	var firstLen int
	tryNode := func(n *cluster.Node) bool {
		nrec := httptest.NewRecorder()
		if !cf.Forward(nrec, r.Clone(r.Context()), n) {
			return false
		}
		if n == first {
			firstCode, firstEnc, firstLen = nrec.Code, nrec.Header().Get("Content-Encoding"), nrec.Body.Len()
		}
		// Редирект ноды (пересыл их не следует) — полноценный ответ: плеер уйдёт по Location.
		redirect := nrec.Code >= 300 && nrec.Code < 400 && strings.TrimSpace(nrec.Header().Get("Location")) != ""
		if redirect || (nrec.Code < 400 && liteHasContent(liteBodyForCheck(nrec))) {
			liteFailoverWins.Store(bal+"|"+n.ID, time.Now())
			writeRecorded(w, nrec)
			return true
		}
		if fallback == nil {
			fallback = nrec
		}
		return false
	}

	if first != nil && tryNode(first) {
		return
	}

	rec := httptest.NewRecorder()
	local.ServeHTTP(rec, r)
	if rec.Code < 400 && liteHasContent(liteBodyForCheck(rec)) {
		if first != nil {
			log.Info().Str("bal", bal).Str("node", first.Name).Str("path", r.URL.Path).
				Int("node_code", firstCode).Str("node_enc", firstEnc).Int("node_len", firstLen).
				Msg("lite: у ноды контента нет — отдал main")
		}
		writeRecorded(w, rec)
		return
	}
	if fallback == nil {
		fallback = rec
	}
	if !liteFailoverWalkNodes[bal] {
		writeRecorded(w, fallback)
		return
	}

	nodes := cp.FailoverCandidates(proxyapi.EdgeSkipSet(r))
	sort.SliceStable(nodes, func(i, j int) bool {
		return liteFailoverWon(bal, nodes[i].ID).After(liteFailoverWon(bal, nodes[j].ID))
	})
	tried := 0
	for _, n := range nodes {
		if first != nil && n.ID == first.ID {
			continue
		}
		if tried >= liteFailoverMaxNodes {
			break
		}
		tried++
		if tryNode(n) {
			log.Info().Str("bal", bal).Str("node", n.Name).Str("path", r.URL.Path).
				Msg("lite: у main контента нет — добрали на ноде")
			return
		}
	}
	writeRecorded(w, fallback)
}

func liteFailoverWon(bal, id string) time.Time {
	if v, ok := liteFailoverWins.Load(bal + "|" + id); ok {
		return v.(time.Time)
	}
	return time.Time{}
}

// liteBodyForCheck — тело ответа для проверки на контент: сжатое (нода
// отдаёт gzip клиентам с Accept-Encoding, а пересыл несёт байты как есть)
// распаковывается. Ловушка 21.09.2026: проверка искала «videos__item» в
// gzip-байтах, не находила и отдавала main — все клиенты с gzip (webOS, Tizen,
// Android) получали токены main вместо нод.
// Неизвестную кодировку считаем контентом: лучше отдать ответ ноды, чем
// дёргать main зря.
func liteBodyForCheck(rec *httptest.ResponseRecorder) []byte {
	body := rec.Body.Bytes()
	switch strings.ToLower(strings.TrimSpace(rec.Header().Get("Content-Encoding"))) {
	case "", "identity":
		return body
	case "gzip":
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return []byte("videos__item") // не разобрать — не наказываем ноду
		}
		defer zr.Close()
		out, err := io.ReadAll(io.LimitReader(zr, 8<<20))
		if err != nil && len(out) == 0 {
			return []byte("videos__item")
		}
		return out
	case "deflate":
		zr, err := zlib.NewReader(bytes.NewReader(body))
		if err != nil {
			return []byte("videos__item")
		}
		defer zr.Close()
		out, err := io.ReadAll(io.LimitReader(zr, 8<<20))
		if err != nil && len(out) == 0 {
			return []byte("videos__item")
		}
		return out
	default:
		return []byte("videos__item")
	}
}

// liteHasContent — есть ли в ответе источника что смотреть. JSON-ответ: строки
// в data или ссылка потока в url. HTML-ответ Лампы: хотя бы один элемент списка.
// Пустые ответы у балансеров — «{}», «[]», `{"data":[]}` и пустой HTML.
func liteHasContent(body []byte) bool {
	b := strings.TrimSpace(string(body))
	if b == "" {
		return false
	}
	switch b[0] {
	case '{':
		var m map[string]any
		if err := stdjson.Unmarshal([]byte(b), &m); err != nil {
			return false
		}
		if u, _ := m["url"].(string); strings.TrimSpace(u) != "" {
			return true
		}
		if d, _ := m["data"].([]any); len(d) > 0 {
			return true
		}
		return false
	case '[':
		var a []any
		return stdjson.Unmarshal([]byte(b), &a) == nil && len(a) > 0
	default:
		return strings.Contains(b, "videos__item") || strings.Contains(b, "data-json")
	}
}

func writeRecorded(w http.ResponseWriter, rec *httptest.ResponseRecorder) {
	for k, vv := range rec.Header() {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	code := rec.Code
	if code == 0 {
		code = http.StatusOK
	}
	w.WriteHeader(code)
	_, _ = w.Write(rec.Body.Bytes())
}
