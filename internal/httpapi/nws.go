package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	stdjson "encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// ---------------------------------------------------------------------------
//  nwsHub — manages all Native WebSocket connections.
// ---------------------------------------------------------------------------

type nwsHub struct {
	mu          sync.RWMutex
	connections map[string]*nwsConn // connectionId -> conn

	weblogMu      sync.RWMutex
	weblogClients map[string]struct{} // connectionId set

	eventMu      sync.RWMutex
	eventClients map[string]string // connectionId -> uid

	weblogSettings func() weblogSettings // lazy config reader
	stopCh         chan struct{}
}

func newNwsHub() *nwsHub {
	return &nwsHub{
		connections:    make(map[string]*nwsConn),
		weblogClients:  make(map[string]struct{}),
		eventClients:   make(map[string]string),
		weblogSettings: loadWeblogSettings,
		stopCh:         make(chan struct{}),
	}
}

// nwsConn represents a single WebSocket connection.
type nwsConn struct {
	id        string
	ws        *websocket.Conn
	ip        string
	host      string
	userAgent string

	sendMu     sync.Mutex
	lastActive atomic.Int64 // UnixNano
	lastSend   atomic.Int64

	done chan struct{}
}

func (c *nwsConn) touch()     { c.lastActive.Store(time.Now().UnixNano()) }
func (c *nwsConn) touchSend() { c.lastSend.Store(time.Now().UnixNano()) }

// ---------------------------------------------------------------------------
//  Wire format
// ---------------------------------------------------------------------------

type nwsMessage struct {
	Method string               `json:"method"`
	Args   []stdjson.RawMessage `json:"args"`
}

type nwsSendMessage struct {
	Method string `json:"method"`
	Args   []any  `json:"args"`
}

// ---------------------------------------------------------------------------
//  WebSocket upgrader
// ---------------------------------------------------------------------------

var nwsUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// ---------------------------------------------------------------------------
//  HandleNWS — HTTP handler for /nws
// ---------------------------------------------------------------------------

func (h *nwsHub) HandleNWS(w http.ResponseWriter, r *http.Request) {
	ws, err := nwsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade writes the error response.
	}

	// Connection ID: reuse client-supplied id for session resume, or generate.
	connID := strings.TrimSpace(r.URL.Query().Get("id"))
	if connID != "" {
		h.cleanup(connID)
	} else {
		connID = generateConnID()
	}

	conn := &nwsConn{
		id:        connID,
		ws:        ws,
		ip:        requestIP(r),
		host:      hostFromRequest(r),
		userAgent: r.UserAgent(),
		done:      make(chan struct{}),
	}
	conn.touch()
	conn.touchSend()

	h.mu.Lock()
	h.connections[connID] = conn
	h.mu.Unlock()

	// Notify client of its connectionId.
	h.send(conn, "Connected", connID)

	// Blocking read loop; returns when socket closes.
	h.receiveLoop(conn)

	// Cleanup on disconnect.
	h.cleanup(connID)
}

// ---------------------------------------------------------------------------
//  Receive loop
// ---------------------------------------------------------------------------

const nwsMaxMessageSize = 10 * 1024 * 1024 // 10 MB

func (h *nwsHub) receiveLoop(conn *nwsConn) {
	conn.ws.SetReadLimit(nwsMaxMessageSize)

	for {
		_, raw, err := conn.ws.ReadMessage()
		if err != nil {
			return
		}

		conn.touch()

		text := strings.TrimSpace(string(raw))
		if len(text) < 2 {
			continue
		}

		// Fast-path: plain "ping" text (no JSON).
		if text == "ping" {
			continue // Client keepalive; .NET ignores these too.
		}

		var msg nwsMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}

		h.invoke(conn, msg.Method, msg.Args)
	}
}

// ---------------------------------------------------------------------------
//  invoke — route client→server method calls
// ---------------------------------------------------------------------------

func (h *nwsHub) invoke(conn *nwsConn, method string, args []stdjson.RawMessage) {
	if method == "" {
		return
	}

	switch strings.ToLower(method) {

	// ------ RCH ------

	case "rchregistry":
		infoJSON := nwsStringArg(args, 0)
		rchRegisterClient(conn.id, conn.ip, conn.host, infoJSON)
		h.send(conn, "RchRegistry", conn.ip)

	case "rchresult":
		id := nwsStringArg(args, 0)
		value := nwsStringArg(args, 1)
		if id == "" {
			return
		}

		entry := rchGetPending(id)
		if entry == nil {
			return
		}

		entry.mu.Lock()
		entry.buf.Reset()
		entry.buf.WriteString(value)
		entry.mu.Unlock()

		rchCompletePending(entry)

	// ------ WebLog ------

	case "registryweblog":
		settings := h.weblogSettings()
		if !settings.Enable {
			return
		}
		token := nwsStringArg(args, 0)
		if settings.Token != "" && settings.Token != token {
			return
		}
		h.weblogMu.Lock()
		h.weblogClients[conn.id] = struct{}{}
		h.weblogMu.Unlock()

	case "weblog":
		h.SendLog(nwsStringArg(args, 0), nwsStringArg(args, 1))

	// ------ Events / Sync v2 ------

	case "registryevent":
		uid := nwsStringArg(args, 0)
		if uid == "" {
			return
		}
		h.eventMu.Lock()
		h.eventClients[conn.id] = uid
		h.eventMu.Unlock()

	case "events":
		uid := nwsStringArg(args, 0)
		name := nwsStringArg(args, 1)
		data := nwsStringArg(args, 2)

		if uid == "" || name == "" {
			return
		}

		// Special: "devices" query — list connected peers.
		if name == "devices" {
			h.sendDevicesList(conn, uid)
			return
		}

		h.sendEvents(conn.id, uid, name, data)

	case "eventsid":
		targetID := nwsStringArg(args, 0)
		uid := nwsStringArg(args, 1)
		name := nwsStringArg(args, 2)
		data := nwsStringArg(args, 3)

		if targetID == "" || name == "" {
			return
		}

		h.sendEventToConnection(targetID, uid, name, data)

	// ------ Keepalive ------

	case "ping":
		h.send(conn, "pong")
	}
}

// ---------------------------------------------------------------------------
//  send — write a JSON message to a single connection
// ---------------------------------------------------------------------------

func (h *nwsHub) send(conn *nwsConn, method string, args ...any) {
	if method == "" {
		return
	}

	if args == nil {
		args = []any{}
	}

	payload, err := json.Marshal(nwsSendMessage{Method: method, Args: args})
	if err != nil {
		return
	}

	conn.sendMu.Lock()
	defer conn.sendMu.Unlock()

	_ = conn.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_ = conn.ws.WriteMessage(websocket.TextMessage, payload)

	conn.touch()
	conn.touchSend()
}

// ---------------------------------------------------------------------------
//  SendLog — broadcast log message to weblog clients
// ---------------------------------------------------------------------------

func (h *nwsHub) SendLog(message, plugin string) {
	if message == "" || plugin == "" || len(message) > 4_000_000 {
		return
	}

	h.weblogMu.RLock()
	ids := make([]string, 0, len(h.weblogClients))
	for id := range h.weblogClients {
		ids = append(ids, id)
	}
	h.weblogMu.RUnlock()

	if len(ids) == 0 {
		return
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, id := range ids {
		if c, ok := h.connections[id]; ok {
			h.send(c, "Receive", message, plugin)
		}
	}
}

// ---------------------------------------------------------------------------
//  SendEvents — broadcast to all connections with matching uid (except sender).
//  Public wrapper so other handlers (bookmark, storage, timecode) can push
//  real-time sync notifications through the WebSocket hub.
// ---------------------------------------------------------------------------

func (h *nwsHub) SendEvents(senderID, uid, name, data string) {
	h.sendEvents(senderID, uid, name, data)
}

// sendEvents — internal implementation.
func (h *nwsHub) sendEvents(senderID, uid, name, data string) {
	h.eventMu.RLock()
	var targets []string
	for cid, u := range h.eventClients {
		if u == uid && cid != senderID {
			targets = append(targets, cid)
		}
	}
	h.eventMu.RUnlock()

	if len(targets) == 0 {
		return
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, cid := range targets {
		if c, ok := h.connections[cid]; ok {
			h.send(c, "event", uid, name, data)
		}
	}
}

// SendEventsToUIDs broadcasts an event to all connections registered with any
// UID in the provided list, excluding the sender connection. Used by the
// storage/bookmark/timecode handlers to fan-out a single TG-user broadcast
// into per-device deliveries (since NWS clients register with their per-device
// lampac_unic_id, not the shared tg:{tg_id} userID).
func (h *nwsHub) SendEventsToUIDs(senderID string, uids []string, name, data string) {
	if len(uids) == 0 || name == "" {
		return
	}

	uidSet := make(map[string]struct{}, len(uids))
	for _, u := range uids {
		if u != "" {
			uidSet[u] = struct{}{}
		}
	}
	if len(uidSet) == 0 {
		return
	}

	type target struct {
		connID string
		uid    string
	}

	h.eventMu.RLock()
	var targets []target
	for cid, u := range h.eventClients {
		if cid == senderID {
			continue
		}
		if _, ok := uidSet[u]; ok {
			targets = append(targets, target{connID: cid, uid: u})
		}
	}
	h.eventMu.RUnlock()

	if len(targets) == 0 {
		return
	}

	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, t := range targets {
		if c, ok := h.connections[t.connID]; ok {
			h.send(c, "event", t.uid, name, data)
		}
	}
}

// SendEventToUIDs sends an event to all NWS connections whose registered uid
// is in the provided set. Used by Alice push to target all devices of a user.
// Returns the number of connections that received the event.
func (h *nwsHub) SendEventToUIDs(uids []string, name string, data string) int {
	if len(uids) == 0 || name == "" {
		return 0
	}

	uidSet := make(map[string]struct{}, len(uids))
	for _, u := range uids {
		uidSet[u] = struct{}{}
	}

	h.eventMu.RLock()
	var targets []string
	for cid, u := range h.eventClients {
		if _, ok := uidSet[u]; ok {
			targets = append(targets, cid)
		}
	}
	h.eventMu.RUnlock()

	if len(targets) == 0 {
		return 0
	}

	h.mu.RLock()
	defer h.mu.RUnlock()
	sent := 0
	for _, cid := range targets {
		if c, ok := h.connections[cid]; ok {
			h.send(c, "event", "", name, data)
			sent++
		}
	}
	return sent
}

// sendEventToConnection — send event to one specific connection.
func (h *nwsHub) sendEventToConnection(targetID, uid, name, data string) {
	h.mu.RLock()
	c, ok := h.connections[targetID]
	h.mu.RUnlock()
	if ok {
		h.send(c, "event", uid, name, data)
	}
}

// sendDevicesList — respond with list of connected peers sharing the same uid.
func (h *nwsHub) sendDevicesList(sender *nwsConn, uid string) {
	h.eventMu.RLock()
	h.mu.RLock()

	type deviceInfo struct {
		UID          string `json:"uid"`
		ConnectionID string `json:"ConnectionId"`
		UserAgent    string `json:"UserAgent"`
	}

	var devices []deviceInfo
	for cid, u := range h.eventClients {
		if cid == sender.id {
			continue
		}
		if c, ok := h.connections[cid]; ok {
			if u == uid || c.ip == sender.ip {
				devices = append(devices, deviceInfo{
					UID:          u,
					ConnectionID: cid,
					UserAgent:    c.userAgent,
				})
			}
		}
	}

	h.mu.RUnlock()
	h.eventMu.RUnlock()

	if devices == nil {
		devices = []deviceInfo{}
	}
	h.send(sender, "event", uid, "devices", devices)
}

// ---------------------------------------------------------------------------
//  SendRchRequest — ask a connected client to fetch a URL
// ---------------------------------------------------------------------------

func (h *nwsHub) SendRchRequest(connectionID, rchID, url, data string, headers map[string]string, returnHeaders bool) {
	h.mu.RLock()
	c, ok := h.connections[connectionID]
	h.mu.RUnlock()
	if !ok {
		return
	}
	h.send(c, "RchClient", rchID, url, data, headers, returnHeaders)
}

// ---------------------------------------------------------------------------
//  cleanup — remove a connection from all maps
// ---------------------------------------------------------------------------

func (h *nwsHub) cleanup(connID string) {
	h.mu.Lock()
	conn, ok := h.connections[connID]
	delete(h.connections, connID)
	h.mu.Unlock()

	if ok {
		_ = conn.ws.Close()
	}

	h.weblogMu.Lock()
	delete(h.weblogClients, connID)
	h.weblogMu.Unlock()

	h.eventMu.Lock()
	delete(h.eventClients, connID)
	h.eventMu.Unlock()

	rchOnDisconnected(connID)
}

// ---------------------------------------------------------------------------
//  monitorConnections — periodic stale-connection reaper
// ---------------------------------------------------------------------------

func (h *nwsHub) monitorConnections(inactiveMinutes int) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			h.reapStale(inactiveMinutes)
		case <-h.stopCh:
			return
		}
	}
}

func (h *nwsHub) reapStale(inactiveMinutes int) {
	now := time.Now().UnixNano()
	// Client pings every 40s; 125s cutoff matches .NET (3 missed pings).
	activityCutoff := now - int64(125*time.Second)

	var inactiveCutoff int64
	if inactiveMinutes > 0 {
		inactiveCutoff = now - int64(time.Duration(inactiveMinutes)*time.Minute)
	}

	h.mu.RLock()
	var stale []string
	for id, c := range h.connections {
		if c.lastActive.Load() < activityCutoff {
			stale = append(stale, id)
			continue
		}
		if inactiveCutoff > 0 && c.lastSend.Load() < inactiveCutoff {
			stale = append(stale, id)
		}
	}
	h.mu.RUnlock()

	for _, id := range stale {
		h.cleanup(id)
	}

	if len(stale) > 0 {
		log.Printf("[nws] reaped %d stale connection(s), active=%d", len(stale), h.CountConnections())
	}
}

// ---------------------------------------------------------------------------
//  Counts
// ---------------------------------------------------------------------------

func (h *nwsHub) CountConnections() int {
	h.mu.RLock()
	n := len(h.connections)
	h.mu.RUnlock()
	return n
}

func (h *nwsHub) CountWeblogClients() int {
	h.weblogMu.RLock()
	n := len(h.weblogClients)
	h.weblogMu.RUnlock()
	return n
}

func (h *nwsHub) CountEventClients() int {
	h.eventMu.RLock()
	n := len(h.eventClients)
	h.eventMu.RUnlock()
	return n
}

// ---------------------------------------------------------------------------
//  Helpers
// ---------------------------------------------------------------------------

func nwsStringArg(args []stdjson.RawMessage, index int) string {
	if index >= len(args) {
		return ""
	}

	raw := args[index]

	// Try to unmarshal as JSON string first.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}

	// Fallback: return raw text (may be number/object serialized).
	return strings.Trim(string(raw), " \t\n\r")
}

func generateConnID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// requestIP is the WebSocket-side equivalent of clientIP — same gating on
// the trusted-proxy allowlist. Without it, an attacker could open a WS to
// /nws with a spoofed X-Forwarded-For and contaminate session-binding logic
// downstream (we use this IP for device fingerprints).
func requestIP(r *http.Request) string {
	directIP := ""
	addr := strings.TrimSpace(r.RemoteAddr)
	if idx := strings.LastIndex(addr, ":"); idx != -1 {
		directIP = addr[:idx]
	} else {
		directIP = addr
	}

	if isRemoteFromTrustedProxy(directIP) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if parts := strings.SplitN(xff, ",", 2); len(parts) > 0 {
				if ip := strings.TrimSpace(parts[0]); ip != "" {
					return ip
				}
			}
		}
		if xri := r.Header.Get("X-Real-Ip"); xri != "" {
			return strings.TrimSpace(xri)
		}
	}
	return directIP
}
