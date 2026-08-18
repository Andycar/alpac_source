package watchparty

import (
	stdjson "encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

// TestWatchpartyRelay is a black-box smoke test: two peers join the
// same room and verify that a message from peer A reaches peer B with
// the original payload intact, while peer A does NOT receive its own
// echo.
func TestWatchpartyRelay(t *testing.T) {
	r := chi.NewRouter()
	r.Get("/webplayer/ws/watchparty/{roomId}", WatchpartyHandler)
	srv := httptest.NewServer(r)
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/webplayer/ws/watchparty/testroom"

	dial := func(name string) *websocket.Conn {
		c, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("%s dial: %v", name, err)
		}
		// Drain the server's 'roster' frame sent to every joiner.
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, msg, err := c.ReadMessage(); err != nil {
			t.Fatalf("%s roster read: %v", name, err)
		} else if !strings.Contains(string(msg), `"roster"`) {
			t.Fatalf("%s expected roster frame, got %q", name, msg)
		}
		c.SetReadDeadline(time.Time{})
		return c
	}
	a := dial("A")
	defer a.Close()
	b := dial("B")
	defer b.Close()

	// Park B's read goroutine first with a generous deadline so it's
	// already blocked in ReadMessage when A's broadcast arrives.
	var wg sync.WaitGroup
	wg.Add(1)
	var got string
	bReady := make(chan struct{})
	go func() {
		defer wg.Done()
		b.SetReadDeadline(time.Now().Add(3 * time.Second))
		close(bReady)
		_, msg, err := b.ReadMessage()
		if err != nil {
			t.Errorf("B read err: %v", err)
			return
		}
		got = string(msg)
	}()
	<-bReady
	// A small grace lets the kernel actually park the read.
	time.Sleep(50 * time.Millisecond)

	pkt, _ := stdjson.Marshal(map[string]any{
		"type":   "play",
		"t":      12.5,
		"sentAt": time.Now().UnixMilli(),
		"src":    "alice",
	})
	if err := a.WriteMessage(websocket.TextMessage, pkt); err != nil {
		t.Fatalf("A write: %v", err)
	}
	wg.Wait()

	if !strings.Contains(got, `"alice"`) || !strings.Contains(got, `"play"`) {
		t.Fatalf("unexpected payload at B: %q", got)
	}

	// Verify A did NOT get echoed back.
	a.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if _, _, err := a.ReadMessage(); err == nil {
		t.Fatalf("A should not have received an echo of its own message")
	}
}

// TestWatchpartyHelloAnnotation verifies the relay rewrites peer counts
// in hello/bye frames.
func TestWatchpartyHelloAnnotation(t *testing.T) {
	r := chi.NewRouter()
	r.Get("/webplayer/ws/watchparty/{roomId}", WatchpartyHandler)
	srv := httptest.NewServer(r)
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/webplayer/ws/watchparty/helloroom"

	// Each joiner first receives a 'roster' frame; drain it before the hello test.
	drainRoster := func(name string, c *websocket.Conn) {
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, m, err := c.ReadMessage(); err != nil || !strings.Contains(string(m), `"roster"`) {
			t.Fatalf("%s expected roster, got %q err=%v", name, m, err)
		}
		c.SetReadDeadline(time.Time{})
	}

	a, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("A dial: %v", err)
	}
	defer a.Close()
	drainRoster("A", a)
	b, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("B dial: %v", err)
	}
	defer b.Close()
	drainRoster("B", b)

	// A sends hello with peers=0; relay should annotate to actual count.
	helloFromA, _ := stdjson.Marshal(map[string]any{
		"type": "hello", "nick": "alice", "peers": 0, "src": "alice",
	})
	if err := a.WriteMessage(websocket.TextMessage, helloFromA); err != nil {
		t.Fatalf("A write: %v", err)
	}

	b.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg, err := b.ReadMessage()
	if err != nil {
		t.Fatalf("B read: %v", err)
	}
	var parsed map[string]any
	if err := stdjson.Unmarshal(msg, &parsed); err != nil {
		t.Fatalf("B parse: %v", err)
	}
	if parsed["type"] != "hello" {
		t.Fatalf("expected hello, got %v", parsed["type"])
	}
	if peers, _ := parsed["peers"].(float64); peers < 1 {
		t.Fatalf("expected peers >= 1, got %v", parsed["peers"])
	}
}

// TestWatchpartyRosterOnJoin verifies each joiner is told the live room size
// directly (so the newcomer's own count isn't stuck — it filters its own hello).
func TestWatchpartyRosterOnJoin(t *testing.T) {
	r := chi.NewRouter()
	r.Get("/webplayer/ws/watchparty/{roomId}", WatchpartyHandler)
	srv := httptest.NewServer(r)
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/webplayer/ws/watchparty/rosterroom"

	readRosterPeers := func(c *websocket.Conn) int {
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, m, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("roster read: %v", err)
		}
		var p map[string]any
		if err := stdjson.Unmarshal(m, &p); err != nil || p["type"] != "roster" {
			t.Fatalf("expected roster, got %q err=%v", m, err)
		}
		n, _ := p["peers"].(float64)
		return int(n)
	}

	a, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("A dial: %v", err)
	}
	defer a.Close()
	if n := readRosterPeers(a); n != 1 {
		t.Fatalf("A roster peers = %d, want 1", n)
	}

	b, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("B dial: %v", err)
	}
	defer b.Close()
	if n := readRosterPeers(b); n != 2 {
		t.Fatalf("B roster peers = %d, want 2", n)
	}
}
