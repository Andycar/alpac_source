package litesrc

import (
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"
	"golang.org/x/net/proxy"
)

// Playback position ("current_time" in the WS heartbeat) is tracked PER WS
// CLIENT, on mirageWSClient.currentSec / lastSec.
//
// In Spectre (Service.cs:92-122) it's derived from the segment number in the
// most recent CDN request URL rather than wallclock elapsed time. That matters:
// the CDN uses "playing" messages as a liveness signal for the session, and if
// the reported time drifts away from what the player is actually fetching, the
// session is flagged and the edge_hash stops rotating.
//
// It used to be one pair of process-wide globals. With a WS session per stream
// that is plainly wrong: two viewers' segment requests interleave into a single
// counter, every session reports someone else's position, and the resulting
// >90s jumps fire spurious "seeked" messages on every connection at once —
// exactly the divergence the CDN is watching for.

// mirageSegmentIDRe matches Spectre's regex exactly: /seg-([0-9]+)- anywhere
// in the URL. Capture group 1 is the segment index (0-based).
var mirageSegmentIDRe = regexp.MustCompile(`/seg-([0-9]+)-`)

// mirageEdgeHashRe mirrors Spectre's edge_hash extraction (Service.cs:200): a
// plain regex over the raw WS message, NOT gated on a specific message "type".
var mirageEdgeHashRe = regexp.MustCompile(`"edge_hash":"([^"]+)"`)

// updateCurrentTimeFromURL parses a segment URL and updates THIS client's
// playback position, so its next WS heartbeat reports where its own player
// actually is. Mirrors Spectre's Service.cs:92-122:
//
//	seg = parseInt(match[1])
//	if (seg <= 25) { current_time = 0; last_time = 0 }
//	else {
//	    current_time = (seg - 25) * 6
//	    if (last_time == 0) last_time = current_time      // suppress first-sample false positive
//	    if (current_time - last_time > 90) sendSeeked()   // notify CDN of player seek
//	    last_time = current_time
//	}
//
// The constants 25 (segments of buffer), 6 (seconds per segment), and 90
// (seek detection threshold) are Spectre's values; changing them
// desynchronizes us from the CDN's expected playback curve.
func (c *mirageWSClient) updateCurrentTimeFromURL(rawURL string) {
	m := mirageSegmentIDRe.FindStringSubmatch(rawURL)
	if len(m) < 2 {
		return
	}
	seg, err := strconv.Atoi(m[1])
	if err != nil {
		return
	}

	if seg <= 25 {
		c.currentSec.Store(0)
		c.lastSec.Store(0)
		return
	}

	newTime := int32((seg - 25) * 6)
	c.currentSec.Store(newTime)

	oldLast := c.lastSec.Load()
	if oldLast == 0 {
		// First sample after a seg<=25 reset (or session start): initialize
		// last_time without firing a false-positive "seeked".
		c.lastSec.Store(newTime)
		return
	}

	if newTime-oldLast > 90 {
		// This player seeked forward more than 90s of content: tell THIS
		// session's heartbeat to fire "seeked" before the next "playing".
		select {
		case c.seekCh <- struct{}{}:
		default:
		}
	}
	c.lastSec.Store(newTime)
}

// mirageUpdateCurrentTimeForKeys routes a segment URL to the WS session that
// owns the stream, looked up by the same keys it was registered under. A stream
// whose session isn't registered simply has no heartbeat to update.
func mirageUpdateCurrentTimeForKeys(rawURL string, keys ...string) {
	edgeHashRegistryMu.RLock()
	var c *mirageWSClient
	for _, k := range keys {
		if k == "" {
			continue
		}
		if found, ok := edgeHashRegistry[k]; ok {
			c = found
			break
		}
	}
	edgeHashRegistryMu.RUnlock()
	if c != nil {
		c.updateCurrentTimeFromURL(rawURL)
	}
}

// ---------------------------------------------------------------------------
// Global edge_hash registry — shared between mirage/alloha/aladdin (same CDN).
// Multiple keys may point to the same WS client. The proxy handler looks up
// pc_hash by any available key.
// ---------------------------------------------------------------------------

var (
	edgeHashRegistryMu sync.RWMutex
	edgeHashRegistry   = make(map[string]*mirageWSClient) // key → WS client

	// globalEdgeHash is the last edge_hash seen from ANY WS config_update (or
	// from the browser-observed WS in mirage_browser). It is only a FALLBACK:
	// a live client in edgeHashRegistry is always preferred, because this value
	// is never cleared when the session that produced it dies.
	globalEdgeHash atomic.Value // string
	// globalEdgeHashAt is when globalEdgeHash was last written. The CDN rotates
	// edge_hash every ~2 min, so a value older than globalEdgeHashTTL belongs to
	// a dead session and the CDN rejects it — sending it as Accepts-Controls 403s
	// EVERY request until some WS reconnects. Past that age we return "" so the
	// caller falls back to the Guard token, which is re-minted on demand.
	globalEdgeHashAt atomic.Int64 // unix nano
)

// globalEdgeHashTTL bounds how long a hash with no live WS client behind it may
// still be used. Generous relative to the ~2 min rotation so a brief WS blip
// doesn't drop us to the Guard token, but short enough that a permanently dead
// WS (Run() gives up after 30 failed reconnects) can't poison every request.
const globalEdgeHashTTL = 5 * time.Minute

// RegisterEdgeHashClient stores a WS client in the global registry.
//
// The FIRST key is the client's identity (tokenMovie — the stream this WS
// session belongs to); the rest are lookup aliases (e.g. linkHost, shared by
// every stream of that balancer). Only an identity collision closes the previous
// client, because that really is the same stream being re-resolved.
//
// An alias collision must NOT close it. linkHost is the same constant for every
// alloha stream, so closing on it meant any new resolve — another viewer, another
// episode, a /capi drill — tore down the WS session of a stream that was PLAYING.
// That session then stops sending "playing" heartbeats and stops receiving
// edge_hash rotations while its player keeps pulling segments, which is exactly
// the state the CDN answers with 403 a few minutes later.
//
// If a previous client held a valid edge_hash, it is seeded into the new client
// so that GetEdgeHash never returns "" in the gap between re-resolve and the
// first WS config_update on the new connection.
func RegisterEdgeHashClient(c *mirageWSClient, keys ...string) {
	edgeHashRegistryMu.Lock()
	defer edgeHashRegistryMu.Unlock()
	// Drop entries whose client is closed. Nothing else removes them, and with
	// alias collisions no longer closing the previous client the map would
	// otherwise keep every dead session's key for the life of the process.
	for k, old := range edgeHashRegistry {
		if old == c {
			continue
		}
		select {
		case <-old.done:
			delete(edgeHashRegistry, k)
		default:
		}
	}
	var seedHash string
	for i, k := range keys {
		if k == "" {
			continue
		}
		if old, ok := edgeHashRegistry[k]; ok && old != c {
			if h := old.EdgeHash(); h != "" && seedHash == "" {
				seedHash = h
			}
			if i == 0 {
				old.Close()
			}
		}
		edgeHashRegistry[k] = c
	}
	// Seed the new client with the last known hash so requests during WS
	// connection setup don't fall back to stale headers.
	if seedHash != "" {
		if c.EdgeHash() == "" {
			c.edgeHash.Store(seedHash)
		}
	}
}

// GetEdgeHash returns the latest edge_hash for any matching key, or "".
func GetEdgeHash(keys ...string) string {
	edgeHashRegistryMu.RLock()
	defer edgeHashRegistryMu.RUnlock()
	for _, k := range keys {
		if c, ok := edgeHashRegistry[k]; ok {
			if h := c.EdgeHash(); h != "" {
				return h
			}
		}
	}
	return ""
}

// storeGlobalEdgeHash records a freshly observed edge_hash together with the
// time it was seen, so GetAnyEdgeHash can tell a live hash from a dead one.
func storeGlobalEdgeHash(h string) {
	if h == "" {
		return
	}
	globalEdgeHash.Store(h)
	globalEdgeHashAt.Store(time.Now().UnixNano())
}

// GetAnyEdgeHash returns an edge_hash that is still plausibly valid, or "".
//
// A LIVE registered client wins: mirageWSClient.EdgeHash() returns "" once the
// client is closed, which is exactly the staleness signal we need. Only if no
// live client has one do we fall back to the last globally seen hash, and only
// while it is younger than globalEdgeHashTTL.
//
// Reading the raw global unconditionally (as this used to) meant that after a
// WS session died for good, its last hash was sent as Accepts-Controls forever —
// the CDN 403s every manifest and segment, which looks exactly like "the token
// expired and everything got blocked".
func GetAnyEdgeHash() string {
	edgeHashRegistryMu.RLock()
	for _, c := range edgeHashRegistry {
		if h := c.EdgeHash(); h != "" {
			edgeHashRegistryMu.RUnlock()
			return h
		}
	}
	edgeHashRegistryMu.RUnlock()

	v := globalEdgeHash.Load()
	if v == nil {
		return ""
	}
	at := globalEdgeHashAt.Load()
	if at == 0 || time.Since(time.Unix(0, at)) > globalEdgeHashTTL {
		return ""
	}
	return v.(string)
}

// mirageWSClient maintains a WebSocket connection to the Mirage player backend,
// receiving config_update messages with fresh edge_hash tokens. The edge_hash is
// used as the pc_hash HTTP header on CDN segment requests, replacing the need
// for SOCKS5 proxy rotation.
type mirageWSClient struct {
	wsURL      string // wss://linkHost/ws/
	sid        string // session ID from /movies/ response
	origin     string // Origin header (https://linkHost)
	socksProxy string // optional SOCKS5 "host:port" — empty = direct dial

	edgeHash atomic.Value // latest edge_hash (string)
	writeMu  sync.Mutex   // protects concurrent WS writes (ping + playing)

	// currentSec is this session's playback position in seconds, derived from
	// the segment numbers its own player is fetching; lastSec is the previous
	// sample, used for seek detection. See updateCurrentTimeFromURL.
	currentSec atomic.Int32
	lastSec    atomic.Int32

	// seekCh is signaled (1-slot buffer) by updateCurrentTimeFromURL when a
	// seek > 90s is detected, telling heartbeat() to fire a "seeked" message
	// before the next "playing" tick.
	seekCh chan struct{}

	done      chan struct{}
	closeOnce sync.Once
}

// mirageWSMessage is the JSON structure for outgoing WS messages.
type mirageWSMessage struct {
	Type        string `json:"type"`
	CurrentTime int    `json:"current_time"`
	Resolution  string `json:"resolution"`
	TrackID     string `json:"track_id"`
	Speed       int    `json:"speed"`
	Subtitle    int    `json:"subtitle"`
	TS          int64  `json:"ts"`
}

// mirageWSConfigUpdate is the JSON structure for incoming config_update messages.
type mirageWSConfigUpdate struct {
	Type         string `json:"type"`
	EdgeHash     string `json:"edge_hash"`
	EdgePriority int    `json:"edge_priority"`
	TTL          int    `json:"ttl"`
	TS           int64  `json:"ts"`
}

// newMirageWSClient creates a new WS client. Call Run() to start the connection.
// socksProxy is an optional "host:port" SOCKS5 to dial through; pass "" for
// a direct connection. Each balancer chooses its own routing — mirage dials
// direct, alloha dials through its registered SOCKS5 (so its WS shares the
// same exit IP as its browser session).
func newMirageWSClient(wsURL, sid, linkHost, socksProxy string) *mirageWSClient {
	// Derive origin from linkHost
	origin := linkHost
	if u, err := url.Parse(linkHost); err == nil {
		origin = "https://" + u.Host
	}

	c := &mirageWSClient{
		wsURL:      wsURL,
		sid:        sid,
		origin:     origin,
		socksProxy: socksProxy,
		seekCh:     make(chan struct{}, 1),
		done:       make(chan struct{}),
	}
	return c
}

// EdgeHash returns the latest edge_hash from config_update, or "" if the client
// is closed or hasn't received a config_update yet. Closed clients return ""
// to prevent GetAnyEdgeHash() from returning stale hashes from dead WS sessions.
func (c *mirageWSClient) EdgeHash() string {
	select {
	case <-c.done:
		return "" // client is closed, hash is stale
	default:
	}
	v := c.edgeHash.Load()
	if v == nil {
		return ""
	}
	return v.(string)
}

// Close stops the WS client goroutine.
func (c *mirageWSClient) Close() {
	c.closeOnce.Do(func() {
		close(c.done)
	})
}

// Run connects to the WS endpoint and maintains the connection with heartbeats.
// Blocks until Close() is called or max reconnect attempts exhausted.
// Should be called as a goroutine.
//
// IMPORTANT: defer c.Close() so when Run() exits for ANY reason (max attempts,
// panic recovered up the stack, etc.) the client is marked closed. That is what
// makes EdgeHash() return "" for a dead session, and what lets
// RegisterEdgeHashClient prune its keys from the registry.
func (c *mirageWSClient) Run() {
	defer c.Close()

	const maxAttempts = 30

	for attempt := 0; attempt < maxAttempts; attempt++ {
		select {
		case <-c.done:
			return
		default:
		}

		if attempt > 0 {
			delay := time.Duration(math.Min(float64(time.Second)*math.Pow(2, float64(attempt-1)), float64(30*time.Second)))
			log.Debug().Int("attempt", attempt).Dur("delay", delay).Msg("mirage-ws: reconnecting")
			select {
			case <-time.After(delay):
			case <-c.done:
				return
			}
		}

		err := c.connectAndRun()
		if err != nil {
			log.Warn().Err(err).Int("attempt", attempt+1).Msg("mirage-ws: connection ended")
		}

		select {
		case <-c.done:
			return
		default:
		}
	}

	log.Warn().Msg("mirage-ws: max reconnect attempts reached, giving up")
}

// connectAndRun establishes one WS connection and runs until it closes.
func (c *mirageWSClient) connectAndRun() error {
	// Build WS URL with sid and timestamp
	wsURL := c.wsURL + "?sid=" + url.QueryEscape(c.sid) + "&v=2.1&t=" + strconv.FormatInt(time.Now().UnixMilli(), 10)

	dialer := websocket.Dialer{
		HandshakeTimeout:  10 * time.Second,
		EnableCompression: true, // browser sends permessage-deflate
	}

	// Route WS through SOCKS5 proxy if THIS client was created with one.
	// Per-client (not global) so different balancers (mirage direct, alloha
	// via its SOCKS5) don't contaminate each other's exit IP.
	if c.socksProxy != "" {
		proxyDialer, proxyErr := proxy.SOCKS5("tcp", c.socksProxy, nil, proxy.Direct)
		if proxyErr == nil {
			if cd, ok := proxyDialer.(proxy.ContextDialer); ok {
				dialer.NetDialContext = cd.DialContext
				log.Debug().Str("socks", c.socksProxy).Msg("mirage-ws: using SOCKS5 proxy")
			}
		}
	}

	header := http.Header{
		"Origin":        {c.origin},
		"User-Agent":    {"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"},
		"Pragma":        {"no-cache"},
		"Cache-Control": {"no-cache"},
	}

	conn, _, err := dialer.Dial(wsURL, header)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	log.Info().Str("wsURL", c.wsURL).Msg("mirage-ws: connected")

	// Set up pong handler to reset read deadline on server pong responses.
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(3 * time.Minute))
		return nil
	})

	// Initial handshake: just playback_start + init, matching Spectre
	// Controller.cs:800-802. The old code also sent resumed/paused/resumed
	// to mimic the real browser player's Charles-captured behaviour, but
	// Spectre proves the CDN is happy with the minimal pair — so we drop
	// the extra noise that the bot-detection heuristics might use.
	now := time.Now().UnixMilli()
	if err := c.sendMsg(conn, "playback_start", 0, now); err != nil {
		return fmt.Errorf("send playback_start: %w", err)
	}
	if err := c.sendMsg(conn, "init", 0, now+1); err != nil {
		return fmt.Errorf("send init: %w", err)
	}

	// Start heartbeat goroutine
	heartbeatDone := make(chan struct{})
	go c.heartbeat(conn, heartbeatDone)

	// Read loop
	err = c.readLoop(conn)

	close(heartbeatDone)
	return err
}

// sendMsg sends a typed WS message (thread-safe via writeMu).
//
// Resolution is hardcoded to "1080" — Spectre sets it from the quality
// actually selected by the browser resolve (Controller.cs:644 resolution
// = q.Name), but since our player request isn't tied to a specific
// quality and the CDN only uses this field for telemetry, "1080" is a
// safe plausible default for 4K-capable streams.
func (c *mirageWSClient) sendMsg(conn *websocket.Conn, msgType string, currentTime int, ts int64) error {
	msg := mirageWSMessage{
		Type:        msgType,
		CurrentTime: currentTime,
		Resolution:  "1080",
		TrackID:     "1",
		Speed:       1,
		Subtitle:    -1,
		TS:          ts,
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return conn.WriteJSON(msg)
}

// readLoop reads incoming WS messages, looking for config_update.
func (c *mirageWSClient) readLoop(conn *websocket.Conn) error {
	for {
		select {
		case <-c.done:
			return nil
		default:
		}

		conn.SetReadDeadline(time.Now().Add(3 * time.Minute))

		// Read raw JSON first to log ALL fields (CDN may send fields we don't parse).
		_, rawMsg, err := conn.ReadMessage()
		if err != nil {
			select {
			case <-c.done:
				return nil
			default:
				return fmt.Errorf("read: %w", err)
			}
		}

		log.Info().RawJSON("raw", rawMsg).Msg("mirage-ws: received message")

		// Spectre (Service.cs:200) pulls edge_hash from ANY incoming message
		// via a plain regex — it is NOT gated on message "type". The CDN can
		// carry the hash on messages other than "config_update"; gating on
		// type risks never storing it, so Accepts-Controls goes stale → CDN 403.
		hm := mirageEdgeHashRe.FindSubmatch(rawMsg)
		if len(hm) < 2 || len(hm[1]) == 0 {
			continue
		}
		newHash := string(hm[1])

		old := c.EdgeHash()
		c.edgeHash.Store(newHash)
		// Update the global edge_hash — always the freshest from ANY WS.
		storeGlobalEdgeHash(newHash)
		if old != newHash {
			// priority/ttl are best-effort (only present on config_update).
			var meta mirageWSConfigUpdate
			_ = json.Unmarshal(rawMsg, &meta)
			log.Info().Str("edge_hash", newHash).Int("priority", meta.EdgePriority).Int("ttl", meta.TTL).Msg("mirage-ws: edge_hash updated")
			// A rotation deliberately does NOT force a re-resolve of every active
			// stream. Accepts-Controls is read from here per request, so the fresh
			// hash is already in use the moment it lands; re-resolving bought
			// nothing but churn — a new player session per active stream every
			// ~2 min, from one IP, which is itself the kind of traffic the CDN
			// pushes back on. The 30 min timer and the reactive 403 path (which
			// also re-mints Borth) cover anything that genuinely goes stale, and
			// alloha's own refresh interval is set on the observation that a URL
			// survives hours as long as the hash keeps rotating.
		}
	}
}

// heartbeat sends periodic "playing" messages to keep the WS session alive,
// mirroring Spectre's Controller.cs:744-776.
//
// Key difference from the old wallclock-based implementation: the
// current_time value comes from THIS client's own currentSec counter, which
// updateCurrentTimeFromURL() advances on every segment this session's player
// fetches through our proxy. This means:
//
//   - During active playback: current_time advances as the client
//     fetches sequential segments (seg-N-...) via our proxy, exactly
//     matching the CDN's expected playback curve.
//   - During pause/buffering: current_time stays put (no segments being
//     fetched), so the CDN doesn't see "phantom" playback progress.
//   - During seek: current_time jumps to the new position immediately,
//     because the client's next segment request carries the new seg-N.
//
// Without this, the old wallclock-based heartbeat drifted away from the
// actual playback position, which the CDN used as a bot-detection signal
// and killed the session after a few minutes.
//
// The message pattern itself is kept minimal (playback_start + init on
// connect, already sent in connectAndRun; then just "playing" on 30s
// ticker) to match Spectre. No extra resumed/paused/ping noise.
func (c *mirageWSClient) heartbeat(conn *websocket.Conn, stop chan struct{}) {
	playingTicker := time.NewTicker(30 * time.Second)
	defer playingTicker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-c.done:
			return
		case <-c.seekCh:
			// Player seek detected (>90s jump in segment-derived current_time).
			// Fire "seeked" event before the next "playing" tick so the CDN
			// updates its expected playback curve. Matches Spectre Service.cs:117-121.
			currentTime := int(c.currentSec.Load())
			ts := time.Now().UnixMilli()
			if err := c.sendMsg(conn, "seeked", currentTime, ts); err != nil {
				log.Debug().Err(err).Msg("mirage-ws: seeked send failed")
				return
			}
			log.Info().Int("current_time", currentTime).Msg("mirage-ws: sent seeked")
		case <-playingTicker.C:
			// Segment-tracked playback position. Matches Spectre exactly.
			currentTime := int(c.currentSec.Load())
			ts := time.Now().UnixMilli()
			if err := c.sendMsg(conn, "playing", currentTime, ts); err != nil {
				log.Debug().Err(err).Msg("mirage-ws: heartbeat send failed")
				return
			}
		}
	}
}
