package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// dialNWS starts a test HTTP server with the given hub and returns a ws conn.
func dialNWS(t *testing.T, hub *nwsHub, queryParams string) *websocket.Conn {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(hub.HandleNWS))
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/nws"
	if queryParams != "" {
		wsURL += "?" + queryParams
	}

	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial nws: %v", err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	return ws
}

// readMsg reads the next JSON message from the ws.
func readMsg(t *testing.T, ws *websocket.Conn) nwsSendMessage {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))

	_, raw, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("read ws message: %v", err)
	}

	var msg nwsSendMessage
	if err := stdjson.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("unmarshal ws message %q: %v", raw, err)
	}
	return msg
}

// sendMsg writes a JSON method call.
func sendMsg(t *testing.T, ws *websocket.Conn, method string, args ...any) {
	t.Helper()
	if args == nil {
		args = []any{}
	}
	payload, err := stdjson.Marshal(nwsSendMessage{Method: method, Args: args})
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatalf("send msg: %v", err)
	}
}

// TestNwsConnected verifies the initial Connected message.
func TestNwsConnected(t *testing.T) {
	hub := newNwsHub()

	ws := dialNWS(t, hub, "")
	msg := readMsg(t, ws)

	if msg.Method != "Connected" {
		t.Fatalf("expected Connected, got %s", msg.Method)
	}
	if len(msg.Args) != 1 {
		t.Fatalf("expected 1 arg, got %d", len(msg.Args))
	}

	// Connection ID should be a non-empty string.
	connID, ok := msg.Args[0].(string)
	if !ok || connID == "" {
		t.Fatalf("expected non-empty string connectionId, got %v", msg.Args[0])
	}

	if hub.CountConnections() != 1 {
		t.Fatalf("expected 1 connection, got %d", hub.CountConnections())
	}
}

// TestNwsReconnectWithID verifies session resume via ?id= parameter.
func TestNwsReconnectWithID(t *testing.T) {
	hub := newNwsHub()

	ws1 := dialNWS(t, hub, "id=myconn123")
	msg1 := readMsg(t, ws1)
	if msg1.Method != "Connected" {
		t.Fatalf("expected Connected, got %s", msg1.Method)
	}

	connID, ok := msg1.Args[0].(string)
	if !ok || connID != "myconn123" {
		t.Fatalf("expected connectionId myconn123, got %v", msg1.Args[0])
	}
}

// TestNwsPingPong verifies ping→pong.
func TestNwsPingPong(t *testing.T) {
	hub := newNwsHub()

	ws := dialNWS(t, hub, "")
	_ = readMsg(t, ws) // Connected

	sendMsg(t, ws, "ping")

	msg := readMsg(t, ws)
	if msg.Method != "pong" {
		t.Fatalf("expected pong, got %s", msg.Method)
	}
}

// TestNwsEventsBroadcast verifies event broadcasting between clients with same uid.
func TestNwsEventsBroadcast(t *testing.T) {
	hub := newNwsHub()

	ws1 := dialNWS(t, hub, "")
	_ = readMsg(t, ws1) // Connected

	ws2 := dialNWS(t, hub, "")
	_ = readMsg(t, ws2) // Connected

	// Both register with the same uid.
	sendMsg(t, ws1, "RegistryEvent", "user42")
	sendMsg(t, ws2, "RegistryEvent", "user42")

	// Give time for registration to be processed.
	time.Sleep(50 * time.Millisecond)

	if hub.CountEventClients() != 2 {
		t.Fatalf("expected 2 event clients, got %d", hub.CountEventClients())
	}

	// ws1 sends event → ws2 should receive it.
	sendMsg(t, ws1, "events", "user42", "bookmark", `{"id":1}`)

	msg := readMsg(t, ws2)
	if msg.Method != "event" {
		t.Fatalf("expected event, got %s", msg.Method)
	}
	if len(msg.Args) < 3 {
		t.Fatalf("expected 3+ args, got %d", len(msg.Args))
	}
	if uid, ok := msg.Args[0].(string); !ok || uid != "user42" {
		t.Fatalf("expected uid user42, got %v", msg.Args[0])
	}
	if name, ok := msg.Args[1].(string); !ok || name != "bookmark" {
		t.Fatalf("expected name bookmark, got %v", msg.Args[1])
	}
}

// TestNwsRchResultViaWS verifies RCH result delivery through WebSocket.
func TestNwsRchResultViaWS(t *testing.T) {
	hub := newNwsHub()

	ws := dialNWS(t, hub, "")
	_ = readMsg(t, ws) // Connected

	// Register a pending RCH request.
	rchID := "test-rch-ws-001"
	entry := rchRegisterPending(rchID)
	defer rchDeletePending(rchID)

	// Client sends result via WS.
	sendMsg(t, ws, "RchResult", rchID, "hello-from-client")

	if !rchWaitDone(entry, 2*time.Second) {
		t.Fatal("rch pending did not complete via WS")
	}

	got := string(rchPendingBytes(entry))
	if got != "hello-from-client" {
		t.Fatalf("expected hello-from-client, got %q", got)
	}
}

// TestNwsWeblogRegistration verifies WebLog client registration.
func TestNwsWeblogRegistration(t *testing.T) {
	hub := newNwsHub()
	// Override settings to enable weblog without token.
	hub.weblogSettings = func() weblogSettings {
		return weblogSettings{Enable: true, Token: ""}
	}

	ws := dialNWS(t, hub, "")
	_ = readMsg(t, ws) // Connected

	sendMsg(t, ws, "RegistryWebLog", "")
	time.Sleep(50 * time.Millisecond)

	if hub.CountWeblogClients() != 1 {
		t.Fatalf("expected 1 weblog client, got %d", hub.CountWeblogClients())
	}
}

// TestNwsWeblogTokenRequired verifies token validation for WebLog.
func TestNwsWeblogTokenRequired(t *testing.T) {
	hub := newNwsHub()
	hub.weblogSettings = func() weblogSettings {
		return weblogSettings{Enable: true, Token: "secret"}
	}

	ws := dialNWS(t, hub, "")
	_ = readMsg(t, ws) // Connected

	// Wrong token — should NOT register.
	sendMsg(t, ws, "RegistryWebLog", "wrong")
	time.Sleep(50 * time.Millisecond)

	if hub.CountWeblogClients() != 0 {
		t.Fatalf("expected 0 weblog clients with wrong token, got %d", hub.CountWeblogClients())
	}

	// Correct token — should register.
	sendMsg(t, ws, "RegistryWebLog", "secret")
	time.Sleep(50 * time.Millisecond)

	if hub.CountWeblogClients() != 1 {
		t.Fatalf("expected 1 weblog client with correct token, got %d", hub.CountWeblogClients())
	}
}

// TestNwsCleanupOnDisconnect verifies cleanup when client disconnects.
func TestNwsCleanupOnDisconnect(t *testing.T) {
	hub := newNwsHub()
	hub.weblogSettings = func() weblogSettings {
		return weblogSettings{Enable: true}
	}

	ws := dialNWS(t, hub, "id=cleanup-test")
	_ = readMsg(t, ws) // Connected

	sendMsg(t, ws, "RegistryWebLog", "")
	sendMsg(t, ws, "RegistryEvent", "uid1")
	time.Sleep(50 * time.Millisecond)

	if hub.CountConnections() != 1 {
		t.Fatalf("expected 1 connection, got %d", hub.CountConnections())
	}

	// Close the WebSocket — triggers cleanup.
	_ = ws.Close()
	time.Sleep(100 * time.Millisecond)

	if hub.CountConnections() != 0 {
		t.Fatalf("expected 0 connections after close, got %d", hub.CountConnections())
	}
	if hub.CountWeblogClients() != 0 {
		t.Fatalf("expected 0 weblog clients after close, got %d", hub.CountWeblogClients())
	}
	if hub.CountEventClients() != 0 {
		t.Fatalf("expected 0 event clients after close, got %d", hub.CountEventClients())
	}
}

// TestNwsSendEventsPublic verifies the public SendEvents method used by
// bookmark/storage/timecode handlers for real-time sync.
func TestNwsSendEventsPublic(t *testing.T) {
	hub := newNwsHub()

	// Connect two clients registered to the same UID.
	ws1 := dialNWS(t, hub, "id=sender1")
	connected1 := readMsg(t, ws1) // Connected
	_ = connected1

	ws2 := dialNWS(t, hub, "id=receiver2")
	connected2 := readMsg(t, ws2) // Connected
	_ = connected2

	// Both register for events with same UID.
	sendMsg(t, ws1, "RegistryEvent", "user42")
	sendMsg(t, ws2, "RegistryEvent", "user42")
	time.Sleep(50 * time.Millisecond)

	// Call public SendEvents (simulates what bookmark/storage handlers do).
	hub.SendEvents("sender1", "user42", "bookmark", `{"type":"add","data":{"where":"history"}}`)

	// ws2 (receiver) should get the event.
	msg := readMsg(t, ws2)
	if msg.Method != "event" {
		t.Fatalf("expected method 'event', got %q", msg.Method)
	}

	// Event args: [uid, name, data]
	if len(msg.Args) < 3 {
		t.Fatalf("expected >=3 args, got %d", len(msg.Args))
	}

	var name string
	if err := stdjson.Unmarshal(mustMarshal(msg.Args[1]), &name); err != nil {
		t.Fatalf("unmarshal event name: %v", err)
	}
	if name != "bookmark" {
		t.Fatalf("expected event name 'bookmark', got %q", name)
	}

	// ws1 (sender) should NOT get its own event — verify by read timeout.
	_ = ws1.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	_, _, err := ws1.ReadMessage()
	if err == nil {
		t.Fatal("sender should NOT receive its own event")
	}
}

// TestNwsBroadcastHelper verifies the nwsBroadcast convenience function.
func TestNwsBroadcastHelper(t *testing.T) {
	hub := newNwsHub()

	// Set up serverRef so nwsBroadcast can find the hub.
	oldRef := serverRef
	serverRef = &Server{nwsHub: hub}
	defer func() { serverRef = oldRef }()

	// Connect a receiver.
	ws := dialNWS(t, hub, "id=rcv1")
	_ = readMsg(t, ws) // Connected
	sendMsg(t, ws, "RegistryEvent", "testuser")
	time.Sleep(50 * time.Millisecond)

	// Broadcast from a different sender connection.
	nwsBroadcast("other-sender", "testuser", "storage", map[string]any{
		"path":     "backup",
		"pathfile": "settings",
	})

	msg := readMsg(t, ws)
	if msg.Method != "event" {
		t.Fatalf("expected method 'event', got %q", msg.Method)
	}

	var eventName string
	if err := stdjson.Unmarshal(mustMarshal(msg.Args[1]), &eventName); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if eventName != "storage" {
		t.Fatalf("expected event name 'storage', got %q", eventName)
	}

	// nwsBroadcast with nil hub should not panic.
	serverRef = nil
	nwsBroadcast("x", "y", "z", "data") // no-op
	serverRef = &Server{nwsHub: hub}
}

// mustMarshal converts any to JSON for test assertions.
func mustMarshal(v any) []byte {
	raw, _ := stdjson.Marshal(v)
	return raw
}
