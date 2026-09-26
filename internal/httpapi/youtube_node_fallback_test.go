package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"lampac-go/internal/cluster"
)

func TestYtResultVerdict(t *testing.T) {
	cases := []struct {
		name string
		body string
		want ytVerdict
	}{
		{"полная лестница", `{"qualitys":{"360p":"u","1080p":"u","2160p":"u"},"url":"u"}`, ytStrong},
		{"только 360p — сгоревший выход", `{"qualitys":{"360p":"u"},"url":"u"}`, ytWeak},
		{"ошибка экстракции", `{"error":"Нет форматов"}`, ytError},
		{"пустой объект", `{}`, ytError},
		{"accsdb — не наша забота", `{"accsdb":true,"msg":"auth"}`, ytOther},
		{"не json", `<html>`, ytOther},
		{"url без карты качеств", `{"url":"u"}`, ytWeak},
		{"480p — уже сильный", `{"qualitys":{"480p":"u"},"url":"u"}`, ytStrong},
	}
	for _, c := range cases {
		if got := ytResultVerdict([]byte(c.body)); got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}

func ytFbTestHandler(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
}

func newYtFbForTest(localBody string, nodeBodies map[string]string, forwardOK bool) (*ytNodeFallback, *[]string) {
	var asked []string
	nodes := []*cluster.Node{
		{ID: "n1", Name: "[DE]edge-b.example.net", Enabled: true, EdgeURL: "https://edge-b.example.net:2053"},
		{ID: "n2", Name: "🇷🇺RU-SPB", Enabled: true, EdgeURL: "https://edge-a.example.org"},
		{ID: "n3", Name: "[FI]", Enabled: true}, // без edge_url — не кандидат
	}
	fb := &ytNodeFallback{
		next:      ytFbTestHandler(localBody),
		isPrimary: func() bool { return true },
		minHLS:    func() float64 { return 90 },
		now:       time.Now,
		preferred: func() []string { return []string{"[DE]edge-b.example.net"} },
		nodes:     func() []*cluster.Node { return nodes },
		forward: func(w http.ResponseWriter, r *http.Request, n *cluster.Node) bool {
			asked = append(asked, n.Name)
			if !forwardOK {
				return false
			}
			w.Header().Set("X-Lampac-Server", n.Name)
			_, _ = w.Write([]byte(nodeBodies[n.Name]))
			return true
		},
	}
	return fb, &asked
}

func ytFbDo(fb *ytNodeFallback, path string) (*httptest.ResponseRecorder, error) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	fb.ServeHTTP(rec, req)
	return rec, nil
}

// Слабый локальный ответ (360p) и сильный от DE → зритель получает ответ ноды
// со ссылками на её edge_url и заголовком, кто ответил.
func TestYtNodeFallbackPrefersStrongNode(t *testing.T) {
	strong := `{"qualitys":{"360p":"https://edge-b.example.net:2053/x","1080p":"https://edge-b.example.net:2053/y"},"url":"https://edge-b.example.net:2053/y"}`
	fb, asked := newYtFbForTest(`{"qualitys":{"360p":"u"},"url":"u"}`, map[string]string{"[DE]edge-b.example.net": strong}, true)
	rec, _ := ytFbDo(fb, "/lite/youtube?videoID=abc")
	if rec.Body.String() != strong {
		t.Fatalf("expected node's strong result, got %s", rec.Body.String())
	}
	if rec.Header().Get("X-Lampac-Server") != "[DE]edge-b.example.net" {
		t.Fatalf("expected node marker header, got %q", rec.Header().Get("X-Lampac-Server"))
	}
	if len(*asked) != 1 || (*asked)[0] != "[DE]edge-b.example.net" {
		t.Fatalf("expected only the preferred node asked, got %v", *asked)
	}
}

// Нода тоже слаба → остаётся локальный ответ. Нода недоступна → тоже локальный.
func TestYtNodeFallbackKeepsLocalWhenNodeNoBetter(t *testing.T) {
	local := `{"qualitys":{"360p":"u"},"url":"u"}`
	fb, _ := newYtFbForTest(local, map[string]string{"[DE]edge-b.example.net": `{"error":"нет"}`}, true)
	rec, _ := ytFbDo(fb, "/lite/youtube?videoID=abc")
	if rec.Body.String() != local {
		t.Fatalf("expected local result kept, got %s", rec.Body.String())
	}
	fb, _ = newYtFbForTest(local, nil, false)
	rec, _ = ytFbDo(fb, "/lite/youtube?videoID=abc")
	if rec.Body.String() != local {
		t.Fatalf("expected local result when forward fails, got %s", rec.Body.String())
	}
}

// Сильный локальный ответ и не-корневые пути ноду не трогают.
func TestYtNodeFallbackSkipsWhenLocalStrongOrSubpath(t *testing.T) {
	strong := `{"qualitys":{"1080p":"u"},"url":"u"}`
	fb, asked := newYtFbForTest(strong, nil, true)
	if rec, _ := ytFbDo(fb, "/lite/youtube?videoID=abc"); rec.Body.String() != strong || len(*asked) != 0 {
		t.Fatalf("strong local must not ask nodes: asked=%v", *asked)
	}
	fb, asked = newYtFbForTest(`{"error":"x"}`, nil, true)
	if _, _ = ytFbDo(fb, "/lite/youtube/feed"); len(*asked) != 0 {
		t.Fatalf("sub-path must not ask nodes: asked=%v", *asked)
	}
}

// Порядок кандидатов: preferred сначала, без edge_url — никогда.
func TestYtFallbackNodesOrder(t *testing.T) {
	nodes := []*cluster.Node{
		{ID: "a", Name: "A", Enabled: true, EdgeURL: "https://a"},
		{ID: "b", Name: "B", Enabled: true, EdgeURL: "https://b"},
		{ID: "c", Name: "C", Enabled: true},
		{ID: "d", Name: "D", Enabled: false, EdgeURL: "https://d"},
	}
	got := ytFallbackNodes(nodes, []string{"B", "a"})
	if len(got) != 2 || got[0].Name != "B" || got[1].Name != "A" {
		t.Fatalf("unexpected order: %v", got)
	}
	if all := ytFallbackNodes(nodes, nil); len(all) != 2 {
		t.Fatalf("expected 2 nodes with edge_url and enabled, got %d", len(all))
	}
}

// rjson=true: качества лежат в data[0]; пустой data + error — ошибка.
func TestYtInspectRjsonShape(t *testing.T) {
	body := `{"type":"movie","data":[{"duration":2144,"qualitys":{"360p":"h/mux/index.m3u8?key=1","1080p":"h/mux/index.m3u8?key=2"},"url":"h/mux/index.m3u8?key=2"}]}`
	i := ytInspect([]byte(body))
	if i.verdict != ytStrong || i.hls || i.duration != 2144 {
		t.Fatalf("unexpected: %+v", i)
	}
	i = ytInspect([]byte(`{"type":"movie","data":[{"duration":4532,"qualitys":{"1080p":"h/mux/master.m3u8?key=1&v=x"},"url":"h/mux/master.m3u8?key=1&v=x"}]}`))
	if i.verdict != ytStrong || !i.hls {
		t.Fatalf("expected strong+hls, got %+v", i)
	}
	if ytInspect([]byte(`{"data":[],"error":"бот-чек"}`)).verdict != ytError {
		t.Fatal("empty data with error must be error")
	}
	if ytInspect([]byte(`{"type":"movie","data":[]}`)).verdict != ytError {
		t.Fatal("empty data must be error")
	}
}

// Длинный ролик, локально сильный, но DASH (mux) → спрашиваем ноду; берём её
// ответ только если там HLS. Короткий DASH ноду не трогает.
func TestYtNodeFallbackLongDASHWantsHLSFromNode(t *testing.T) {
	localDASH := `{"duration":2144,"qualitys":{"1080p":"h/mux/index.m3u8?key=1"},"url":"h/mux/index.m3u8?key=1"}`
	nodeHLS := `{"duration":2144,"qualitys":{"1080p":"https://edge-b.example.net:2053/lite/youtube/mux/master.m3u8?key=2&v=x"},"url":"https://edge-b.example.net:2053/lite/youtube/mux/master.m3u8?key=2&v=x"}`
	nodeDASH := `{"duration":2144,"qualitys":{"1080p":"https://edge-b.example.net:2053/lite/youtube/mux/index.m3u8?key=3"},"url":"https://edge-b.example.net:2053/lite/youtube/mux/index.m3u8?key=3"}`
	fb, asked := newYtFbForTest(localDASH, map[string]string{"[DE]edge-b.example.net": nodeHLS}, true)
	rec, _ := ytFbDo(fb, "/lite/youtube?videoID=abc")
	if rec.Body.String() != nodeHLS || len(*asked) != 1 {
		t.Fatalf("expected node HLS taken, got %s asked=%v", rec.Body.String(), *asked)
	}
	fb, _ = newYtFbForTest(localDASH, map[string]string{"[DE]edge-b.example.net": nodeDASH}, true)
	if rec, _ = ytFbDo(fb, "/lite/youtube?videoID=abc"); rec.Body.String() != localDASH {
		t.Fatalf("node DASH must not replace local DASH, got %s", rec.Body.String())
	}
	short := `{"duration":45,"qualitys":{"1080p":"h/mux/index.m3u8?key=1"},"url":"h/mux/index.m3u8?key=1"}`
	fb, asked = newYtFbForTest(short, map[string]string{"[DE]edge-b.example.net": nodeHLS}, true)
	if rec, _ = ytFbDo(fb, "/lite/youtube?videoID=abc"); rec.Body.String() != short || len(*asked) != 0 {
		t.Fatalf("short DASH must stay local without asking: asked=%v", *asked)
	}
}

// Предохранитель: после двух слабых локальных ответов нода идёт первой и
// локальная экстракция не запускается; сильный локальный ответ закрывает его;
// по истечении окна — снова локально.
func TestYtNodeFallbackBreaker(t *testing.T) {
	strong := `{"qualitys":{"1080p":"https://edge-b.example.net:2053/x"},"url":"https://edge-b.example.net:2053/x"}`
	localCalls := 0
	clock := time.Unix(1_700_000_000, 0)
	fb, asked := newYtFbForTest(`{"error":"бот-чек"}`, map[string]string{"[DE]edge-b.example.net": strong}, true)
	fb.now = func() time.Time { return clock }
	inner := fb.next
	fb.next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { localCalls++; inner.ServeHTTP(w, r) })
	for i := 0; i < 2; i++ {
		if rec, _ := ytFbDo(fb, "/lite/youtube?videoID=v"); rec.Body.String() != strong {
			t.Fatalf("attempt %d: expected node result", i)
		}
	}
	if localCalls != 2 || len(*asked) != 2 {
		t.Fatalf("expected 2 local attempts and 2 asks, got %d/%d", localCalls, len(*asked))
	}
	// третий запрос — предохранитель открыт: локально не ходим
	if rec, _ := ytFbDo(fb, "/lite/youtube?videoID=v"); rec.Body.String() != strong || localCalls != 2 {
		t.Fatalf("breaker must skip local: localCalls=%d", localCalls)
	}
	// окно истекло — снова локально (и снова слабо → нода)
	clock = clock.Add(ytBreakerFor + time.Second)
	if rec, _ := ytFbDo(fb, "/lite/youtube?videoID=v"); rec.Body.String() != strong || localCalls != 3 {
		t.Fatalf("after window local must run again: localCalls=%d", localCalls)
	}
	// пока окно открыто, локально не ходим — закрыть его может только сильный
	// локальный ответ ПОСЛЕ истечения окна
	fb.next = ytFbTestHandler(strong)
	ytFbDo(fb, "/lite/youtube?videoID=v")
	if !fb.nodeFirstNow() {
		t.Fatal("breaker must stay open until the window passes")
	}
	clock = clock.Add(ytBreakerFor + time.Second)
	ytFbDo(fb, "/lite/youtube?videoID=v")
	if fb.nodeFirstNow() {
		t.Fatal("strong local must close the breaker")
	}
}
