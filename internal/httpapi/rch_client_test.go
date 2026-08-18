package httpapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func rchTestRequest(nwsID, remoteAddr string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/lite/kinovod?nws_id="+nwsID, nil)
	req.RemoteAddr = remoteAddr
	return req
}

// registerDevice connects a fake device over /nws and registers it for rch.
// The returned ws lets the test play the device (read dispatches, post results).
func registerDevice(t *testing.T, hub *nwsHub, connID string) *websocket.Conn {
	t.Helper()
	ws := dialNWS(t, hub, "id="+connID)
	if msg := readMsg(t, ws); msg.Method != "Connected" {
		t.Fatalf("expected Connected, got %s", msg.Method)
	}
	sendMsg(t, ws, "RchRegistry", `{"rchtype":"apk","apkVersion":1}`)
	// Server acks RchRegistry only after rchRegisterClient ran — reading it here
	// guarantees the registry is populated before we build the rchClient.
	if msg := readMsg(t, ws); msg.Method != "RchRegistry" {
		t.Fatalf("expected RchRegistry ack, got %s", msg.Method)
	}
	return ws
}

func argString(args []any, i int) string {
	if i >= len(args) {
		return ""
	}
	s, _ := args[i].(string)
	return s
}

// readDispatch reads the next RchClient dispatch and returns (rchID, url).
func readDispatch(t *testing.T, ws *websocket.Conn) (string, string) {
	t.Helper()
	msg := readMsg(t, ws)
	if msg.Method != "RchClient" {
		t.Fatalf("expected RchClient dispatch, got %s", msg.Method)
	}
	return argString(msg.Args, 0), argString(msg.Args, 1)
}

// TestRchDispatchHTTPResult covers the full loop: dispatch over WS, device POSTs
// the body to /rch/result, Get returns it.
func TestRchDispatchHTTPResult(t *testing.T) {
	hub := newNwsHub()
	ws := registerDevice(t, hub, "devconn1")

	c := newRchClientWithHub(hub, rchTestRequest("devconn1", "127.0.0.1:55555"))
	if c.IsNotConnected() {
		t.Fatal("expected connected rchClient")
	}

	type result struct {
		body string
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		body, err := c.Get(context.Background(), "https://site.test/page",
			map[string]string{"Referer": "https://site.test/"})
		resCh <- result{body, err}
	}()

	rchID, url := readDispatch(t, ws)
	if rchID == "" || url != "https://site.test/page" {
		t.Fatalf("bad dispatch args: id=%q url=%q", rchID, url)
	}

	rec := httptest.NewRecorder()
	postReq := httptest.NewRequest(http.MethodPost, "/rch/result?id="+rchID,
		bytes.NewReader([]byte("<html>ok</html>")))
	rchResultHandler()(rec, postReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("rch result POST status %d", rec.Code)
	}

	select {
	case r := <-resCh:
		if r.err != nil {
			t.Fatalf("Get error: %v", r.err)
		}
		if r.body != "<html>ok</html>" {
			t.Fatalf("unexpected body: %q", r.body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Get did not return")
	}
}

// TestRchDispatchGzipResult covers the gzip result path (/rch/gzresult).
func TestRchDispatchGzipResult(t *testing.T) {
	hub := newNwsHub()
	ws := registerDevice(t, hub, "devconn-gz")

	c := newRchClientWithHub(hub, rchTestRequest("devconn-gz", "127.0.0.1:55555"))

	resCh := make(chan string, 1)
	go func() {
		body, _ := c.Get(context.Background(), "https://site.test/x", nil)
		resCh <- body
	}()

	rchID, _ := readDispatch(t, ws)

	var gzbuf bytes.Buffer
	gw := gzip.NewWriter(&gzbuf)
	_, _ = gw.Write([]byte("gzipped-body"))
	_ = gw.Close()

	rec := httptest.NewRecorder()
	postReq := httptest.NewRequest(http.MethodPost, "/rch/gzresult?id="+rchID, &gzbuf)
	rchGzipResultHandler()(rec, postReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("rch gzresult POST status %d", rec.Code)
	}

	select {
	case body := <-resCh:
		if body != "gzipped-body" {
			t.Fatalf("unexpected gzip body: %q", body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Get did not return")
	}
}

// TestRchDispatchWithHeaders covers the returnHeaders path: the dispatch carries
// returnHeaders=true, the device returns the {headers,body} envelope (set-cookie
// as an array, as Lampa's native() produces), and PostWithHeaders parses body +
// cookies back out.
func TestRchDispatchWithHeaders(t *testing.T) {
	hub := newNwsHub()
	ws := registerDevice(t, hub, "devconn-hdr")

	c := newRchClientWithHub(hub, rchTestRequest("devconn-hdr", "127.0.0.1:55555"))

	type result struct {
		resp *rchResponse
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		resp, err := c.PostWithHeaders(context.Background(), "https://dle.test/ajax/login/",
			"login=foo&password=bar", map[string]string{"User-Agent": "x"})
		resCh <- result{resp, err}
	}()

	// Read the dispatch and assert returnHeaders (arg 4) is true.
	msg := readMsg(t, ws)
	if msg.Method != "RchClient" {
		t.Fatalf("expected RchClient dispatch, got %s", msg.Method)
	}
	rchID := argString(msg.Args, 0)
	if rchID == "" {
		t.Fatal("empty rchID in dispatch")
	}
	if len(msg.Args) < 5 {
		t.Fatalf("expected 5 dispatch args, got %d", len(msg.Args))
	}
	if rh, ok := msg.Args[4].(bool); !ok || !rh {
		t.Fatalf("expected returnHeaders=true, got %v", msg.Args[4])
	}

	envelope := `{"headers":{"set-cookie":["sid=abc123; Path=/; HttpOnly","csrf=xyz; Path=/"],"content-type":"text/html"},"body":"<html>logged-in</html>"}`
	rec := httptest.NewRecorder()
	postReq := httptest.NewRequest(http.MethodPost, "/rch/result?id="+rchID,
		bytes.NewReader([]byte(envelope)))
	rchResultHandler()(rec, postReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("rch result POST status %d", rec.Code)
	}

	select {
	case r := <-resCh:
		if r.err != nil {
			t.Fatalf("PostWithHeaders error: %v", r.err)
		}
		if r.resp.Body != "<html>logged-in</html>" {
			t.Fatalf("unexpected body: %q", r.resp.Body)
		}
		cookies := r.resp.Cookies()
		if len(cookies) != 2 {
			t.Fatalf("expected 2 cookies, got %d: %+v", len(cookies), cookies)
		}
		if ch := r.resp.CookieHeader(); !strings.Contains(ch, "sid=abc123") || !strings.Contains(ch, "csrf=xyz") {
			t.Fatalf("bad CookieHeader: %q", ch)
		}
		if ct := r.resp.Headers.Get("Content-Type"); ct != "text/html" {
			t.Fatalf("expected content-type text/html, got %q", ct)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PostWithHeaders did not return")
	}
}

// TestRchGate covers the handler-level handshake helper: off => never gates;
// on + no device => gates with ConnectionMsg; on + connected => proceeds.
func TestRchGate(t *testing.T) {
	hub := newNwsHub()
	SetGlobalNwsHub(hub) // rchGate uses newRchClient -> globalNwsHub
	defer SetGlobalNwsHub(nil)

	// rhub off: never gates, writes nothing.
	rec := httptest.NewRecorder()
	if rchGate(rec, httptest.NewRequest(http.MethodGet, "/lite/x", nil), false) {
		t.Fatal("rhub=false must not gate")
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("rhub=false must not write, got %q", rec.Body.String())
	}

	// rhub on, no device: gates with ConnectionMsg.
	rec = httptest.NewRecorder()
	if !rchGate(rec, httptest.NewRequest(http.MethodGet, "/lite/x", nil), true) {
		t.Fatal("rhub on + no device must gate")
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"rch":true`)) || !bytes.Contains(rec.Body.Bytes(), []byte("/nws")) {
		t.Fatalf("expected ConnectionMsg, got %q", rec.Body.String())
	}

	// rhub on, device connected: does not gate.
	_ = registerDevice(t, hub, "gate-dev")
	rec = httptest.NewRecorder()
	if rchGate(rec, rchTestRequest("gate-dev", "127.0.0.1:55555"), true) {
		t.Fatal("connected device must not gate")
	}
}

// TestRchFetch covers the fetch wrapper: off / no-device => direct; connected =>
// via hub without calling direct.
func TestRchFetch(t *testing.T) {
	hub := newNwsHub()
	SetGlobalNwsHub(hub)
	defer SetGlobalNwsHub(nil)

	directCalled := false
	direct := func() (string, error) { directCalled = true; return "DIRECT", nil }

	// rhub off => direct.
	if body, _ := rchFetch(httptest.NewRequest(http.MethodGet, "/x", nil), false, "https://s/p", nil, direct); body != "DIRECT" || !directCalled {
		t.Fatalf("rhub=false must use direct, got %q called=%v", body, directCalled)
	}

	// rhub on, no device => direct fallback.
	directCalled = false
	if body, _ := rchFetch(httptest.NewRequest(http.MethodGet, "/x", nil), true, "https://s/p", nil, direct); body != "DIRECT" || !directCalled {
		t.Fatalf("no device must fall back to direct, got %q called=%v", body, directCalled)
	}

	// rhub on, device connected => via hub, direct not called.
	ws := registerDevice(t, hub, "fetch-dev")
	directCalled = false
	resCh := make(chan string, 1)
	go func() {
		body, _ := rchFetch(rchTestRequest("fetch-dev", "127.0.0.1:55555"), true, "https://site.test/p", nil, direct)
		resCh <- body
	}()

	rchID, gotURL := readDispatch(t, ws)
	if gotURL != "https://site.test/p" {
		t.Fatalf("unexpected dispatch url: %q", gotURL)
	}
	rec := httptest.NewRecorder()
	rchResultHandler()(rec, httptest.NewRequest(http.MethodPost, "/rch/result?id="+rchID,
		bytes.NewReader([]byte("HUB-BODY"))))

	select {
	case body := <-resCh:
		if body != "HUB-BODY" {
			t.Fatalf("expected hub body, got %q", body)
		}
		if directCalled {
			t.Fatal("direct must not be called when the hub succeeds")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("rchFetch did not return")
	}
}

// TestRchNotConnected: no nws_id => not connected, Get errors, ConnectionMsg
// tells the client how to open the hub.
func TestRchNotConnected(t *testing.T) {
	hub := newNwsHub()
	c := newRchClientWithHub(hub, httptest.NewRequest(http.MethodGet, "/lite/kinovod", nil))
	if c.IsConnected() {
		t.Fatal("expected not connected without nws_id")
	}
	if _, err := c.Get(context.Background(), "https://x", nil); err != errRchNoClient {
		t.Fatalf("expected errRchNoClient, got %v", err)
	}
	msg := c.ConnectionMsg()
	if !bytes.Contains(msg, []byte(`"rch":true`)) || !bytes.Contains(msg, []byte(`/nws`)) {
		t.Fatalf("bad ConnectionMsg: %s", msg)
	}
}

// TestRchIPMismatch: a request from a different IP than the registered device
// must not bind to it (defence-in-depth against nws_id reuse).
func TestRchIPMismatch(t *testing.T) {
	hub := newNwsHub()
	_ = registerDevice(t, hub, "devconn-ip") // device IP is loopback

	c := newRchClientWithHub(hub, rchTestRequest("devconn-ip", "8.8.8.8:1"))
	if c.IsConnected() {
		t.Fatal("expected not connected on IP mismatch")
	}
}

// TestRchTimeout: connected device that never replies => Get times out, bounded
// by the request context deadline.
func TestRchTimeout(t *testing.T) {
	hub := newNwsHub()
	_ = registerDevice(t, hub, "devconn-to") // connected, but will not answer

	c := newRchClientWithHub(hub, rchTestRequest("devconn-to", "127.0.0.1:55555"))
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := c.Get(ctx, "https://x", nil); err != errRchTimeout {
		t.Fatalf("expected errRchTimeout, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("timeout took too long: %v", elapsed)
	}
}
