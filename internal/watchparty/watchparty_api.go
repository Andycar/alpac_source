package watchparty

import (
	stdjson "encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

// ---------------------------------------------------------------------------
//  Watchparty — WebSocket relay for synchronized playback rooms.
//
//  Client side: player/src/lib/engine/features/WatchpartySync.ts
//  Endpoint:    GET /webplayer/ws/watchparty/{roomId}
//
//  Per-room hub keeps a slice of *wpConn. When a peer sends a JSON frame
//  we re-broadcast it to all other peers in the same room. We never echo
//  to the original sender (the client tags every frame with `src` so it
//  could filter, but skipping the round-trip is cheaper).
//
//  hello/bye frames are special: the relay annotates them with the
//  current peer count before forwarding so all peers see consistent
//  membership numbers.
//
//  Idle eviction: rooms with no traffic for 30 minutes are dropped.
// ---------------------------------------------------------------------------

const (
	watchpartyMaxMessageSize = 8 * 1024 // small JSON, generous cap
	watchpartyMaxRoomPeers   = 32       // avoid noisy neighbors
	watchpartyMaxRoomIDLen   = 64
	watchpartyIdleTimeout    = 30 * time.Minute
	watchpartyPingInterval   = 25 * time.Second
	watchpartyReadDeadline   = 60 * time.Second
)

var watchpartyUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// Same-origin only — relay is reached via the player page itself.
	// Lampac may be embedded into Lampa pages from other origins, so we
	// fall back to permissive: any origin. Auth (if needed) lives at the
	// router level.
	CheckOrigin: func(r *http.Request) bool { return true },
}

type wpConn struct {
	ws       *websocket.Conn
	sendMu   sync.Mutex
	lastSeen atomic.Int64 // UnixNano
	done     chan struct{}
}

func (c *wpConn) send(msg []byte) bool {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	c.ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := c.ws.WriteMessage(websocket.TextMessage, msg); err != nil {
		return false
	}
	return true
}

type wpRoom struct {
	mu       sync.Mutex
	peers    []*wpConn
	lastSeen atomic.Int64
}

type wpHub struct {
	mu    sync.Mutex
	rooms map[string]*wpRoom
}

var watchparty = &wpHub{rooms: make(map[string]*wpRoom)}

// init runs an idle-room sweeper as a goroutine on package load.
func init() {
	go watchparty.sweepLoop()
}

func (h *wpHub) sweepLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		h.evictIdleRooms()
	}
}

func (h *wpHub) evictIdleRooms() {
	cutoff := time.Now().Add(-watchpartyIdleTimeout).UnixNano()
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, room := range h.rooms {
		if room.lastSeen.Load() < cutoff {
			room.mu.Lock()
			peers := room.peers
			room.peers = nil
			room.mu.Unlock()
			for _, p := range peers {
				_ = p.ws.Close()
			}
			delete(h.rooms, id)
			log.Printf("[watchparty] evicted idle room %s", id)
		}
	}
}

func (h *wpHub) join(roomID string, c *wpConn) (*wpRoom, int, bool) {
	h.mu.Lock()
	room, ok := h.rooms[roomID]
	if !ok {
		room = &wpRoom{}
		h.rooms[roomID] = room
	}
	h.mu.Unlock()

	room.mu.Lock()
	defer room.mu.Unlock()
	if len(room.peers) >= watchpartyMaxRoomPeers {
		return nil, 0, false
	}
	room.peers = append(room.peers, c)
	room.lastSeen.Store(time.Now().UnixNano())
	return room, len(room.peers), true
}

func (h *wpHub) leave(roomID string, c *wpConn) int {
	h.mu.Lock()
	room, ok := h.rooms[roomID]
	h.mu.Unlock()
	if !ok {
		return 0
	}
	room.mu.Lock()
	defer room.mu.Unlock()
	for i, p := range room.peers {
		if p == c {
			room.peers = append(room.peers[:i], room.peers[i+1:]...)
			break
		}
	}
	count := len(room.peers)
	if count == 0 {
		// Drop empty room from the hub.
		h.mu.Lock()
		delete(h.rooms, roomID)
		h.mu.Unlock()
	}
	return count
}

// broadcast forwards `msg` to every peer in the room except `from`.
// Returns the receiver count (excluding sender).
func (room *wpRoom) broadcast(msg []byte, from *wpConn) int {
	room.mu.Lock()
	peers := append([]*wpConn(nil), room.peers...) // snapshot
	room.lastSeen.Store(time.Now().UnixNano())
	room.mu.Unlock()

	count := 0
	for _, p := range peers {
		if p == from {
			continue
		}
		if p.send(msg) {
			count++
		}
	}
	return count
}

func (room *wpRoom) peerCount() int {
	room.mu.Lock()
	defer room.mu.Unlock()
	return len(room.peers)
}

// WatchpartyHealthHandler is a lightweight HTTP probe the client can hit
// before attempting the WS upgrade. When it's present the server has the
// watchparty route compiled in; when it 404s, the deployed binary is too
// old. Also useful for reverse-proxy sanity checks.
func WatchpartyHealthHandler(w http.ResponseWriter, r *http.Request) {
	h := watchparty
	h.mu.Lock()
	rooms := len(h.rooms)
	peers := 0
	for _, room := range h.rooms {
		peers += room.peerCount()
	}
	h.mu.Unlock()

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	// For HEAD, net/http discards the body automatically, but explicit is
	// clearer for readers and avoids the Write() syscall.
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write([]byte(`{"ok":true,"route":"/webplayer/ws/watchparty/{roomId}","rooms":` +
		itoa(rooms) + `,"peers":` + itoa(peers) + `}`))
}

// Small allocation-free itoa so we don't need a full JSON encoder here.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// WatchpartyHandler is the chi handler for the WS upgrade.
func WatchpartyHandler(w http.ResponseWriter, r *http.Request) {
	roomID := strings.TrimSpace(chi.URLParam(r, "roomId"))
	if roomID == "" || len(roomID) > watchpartyMaxRoomIDLen {
		http.Error(w, "invalid room id", http.StatusBadRequest)
		return
	}

	ws, err := watchpartyUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade writes the error response.
	}
	conn := &wpConn{ws: ws, done: make(chan struct{})}
	conn.lastSeen.Store(time.Now().UnixNano())

	room, count, ok := watchparty.join(roomID, conn)
	if !ok {
		_ = ws.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "room full"),
			time.Now().Add(2*time.Second),
		)
		_ = ws.Close()
		return
	}
	log.Printf("[watchparty] room %s joined (peers=%d)", roomID, count)

	// Tell the JOINER the live roster size: its own hello is broadcast to the
	// OTHERS (annotated), so without this the newcomer never learns the count
	// (it filters out frames tagged with its own src). 'roster' is server-sent.
	if data, err := stdjson.Marshal(wpMessage{Type: "roster", Peers: count, Src: "server"}); err == nil {
		conn.send(data)
	}

	// Set up read deadline + pong handler so half-open conns clean up.
	ws.SetReadLimit(watchpartyMaxMessageSize)
	ws.SetReadDeadline(time.Now().Add(watchpartyReadDeadline))
	ws.SetPongHandler(func(string) error {
		conn.lastSeen.Store(time.Now().UnixNano())
		ws.SetReadDeadline(time.Now().Add(watchpartyReadDeadline))
		return nil
	})

	// Server-side ping ticker.
	go func() {
		t := time.NewTicker(watchpartyPingInterval)
		defer t.Stop()
		for {
			select {
			case <-conn.done:
				return
			case <-t.C:
				conn.sendMu.Lock()
				_ = ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
				err := ws.WriteMessage(websocket.PingMessage, nil)
				conn.sendMu.Unlock()
				if err != nil {
					return
				}
			}
		}
	}()

	defer func() {
		close(conn.done)
		left := watchparty.leave(roomID, conn)
		_ = ws.Close()
		log.Printf("[watchparty] room %s left (peers=%d)", roomID, left)

		// Notify remaining peers of updated count via a synthetic 'bye'.
		if left > 0 {
			byeMsg := wpMessage{
				Type:  "bye",
				Nick:  "",
				Peers: left,
				Src:   "server",
			}
			if data, err := stdjson.Marshal(byeMsg); err == nil {
				room.broadcast(data, conn)
			}
		}
	}()

	for {
		_, raw, err := ws.ReadMessage()
		if err != nil {
			return
		}
		conn.lastSeen.Store(time.Now().UnixNano())
		ws.SetReadDeadline(time.Now().Add(watchpartyReadDeadline))

		// Parse minimally to figure out if it's a hello/bye we should
		// annotate. Anything else passes through verbatim.
		out := raw
		var msg wpMessage
		if err := stdjson.Unmarshal(raw, &msg); err == nil {
			if msg.Type == "hello" || msg.Type == "bye" {
				msg.Peers = room.peerCount()
				if rewritten, err := stdjson.Marshal(msg); err == nil {
					out = rewritten
				}
			}
		}
		room.broadcast(out, conn)
	}
}

// wpMessage mirrors the client's PartyCmd shape just enough for the
// hello/bye annotation. Any unknown fields are preserved by passing the
// original bytes through unchanged.
type wpMessage struct {
	Type   string `json:"type"`
	Nick   string `json:"nick,omitempty"`
	Peers  int    `json:"peers,omitempty"`
	Src    string `json:"src,omitempty"`
	SentAt int64  `json:"sentAt,omitempty"`
	T      any    `json:"t,omitempty"`
	Index  any    `json:"index,omitempty"`
	URL    string `json:"url,omitempty"`
	Text   string `json:"text,omitempty"`
}
