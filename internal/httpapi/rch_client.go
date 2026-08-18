package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
//  Reverse client hub (rch / "rhub")
//
//  Dispatches an HTTP fetch to the end-user's device (the Lampa app) over the
//  existing /nws WebSocket. The device performs the fetch from its own IP and
//  POSTs the body back to /rch/result. The balancer site therefore sees the
//  client's (residential) IP instead of the server's VPS IP — the point of the
//  feature for sources that geo-block or IP-ban the server.
//
//  This is the high-level glue over the primitives in rch_api.go / nws.go:
//  SendRchRequest -> rchRegisterPending -> rchWaitDone -> rchPendingBytes.
// ---------------------------------------------------------------------------

// globalNwsHubPtr exposes the singleton NWS hub to balancer code. The hub is
// created in server.go; balancers only have the *http.Request, so they reach
// the hub through this package global (same pattern as ytAPIPtr).
var globalNwsHubPtr atomic.Pointer[nwsHub]

// SetGlobalNwsHub publishes the hub for balancer-side rch dispatch.
func SetGlobalNwsHub(h *nwsHub) { globalNwsHubPtr.Store(h) }

func globalNwsHub() *nwsHub { return globalNwsHubPtr.Load() }

var (
	errRchNoClient = errors.New("rch: no connected client")
	errRchTimeout  = errors.New("rch: client timeout")
)

// rchDefaultTimeout bounds a single dispatch (server -> device -> site -> back).
// A balancer's request context deadline, when shorter, takes precedence.
const rchDefaultTimeout = 12 * time.Second

// rchClient is a per-request handle for dispatching fetches to one device.
type rchClient struct {
	hub    *nwsHub
	connID string
	ip     string
	host   string
	info   rchClientInfo
	conn   bool
}

// newRchClient binds to the device identified by ?nws_id on the request, if any.
func newRchClient(r *http.Request) *rchClient {
	return newRchClientWithHub(globalNwsHub(), r)
}

// newRchClientWithHub is the testable form (lets tests inject a hub).
func newRchClientWithHub(hub *nwsHub, r *http.Request) *rchClient {
	c := &rchClient{
		hub:  hub,
		host: streamHostFromRequest(r),
		ip:   clientIP(r),
	}
	connID := strings.TrimSpace(r.URL.Query().Get("nws_id"))
	if connID == "" || hub == nil {
		return c
	}
	info, ok := rchLookupClient(connID)
	if !ok {
		return c
	}
	// IP-match safety: the WS device and the HTTP requester should be the same
	// box. The connID itself is a 128-bit random token (the real secret); this
	// is defence-in-depth, so we only reject on a definite mismatch.
	if info.IP != "" && c.ip != "" && info.IP != c.ip {
		return c
	}
	c.connID = connID
	c.info = info
	c.conn = true
	return c
}

func (c *rchClient) IsConnected() bool    { return c != nil && c.conn }
func (c *rchClient) IsNotConnected() bool { return !c.IsConnected() }

// RchType reports the device transport: "apk", "cors", or "web".
func (c *rchClient) RchType() string {
	if c == nil {
		return ""
	}
	return c.info.RchType
}

// ConnectionMsg is the JSON a balancer returns when no device is connected, so
// the Lampa client opens the hub and re-requests with ?nws_id set.
func (c *rchClient) ConnectionMsg() []byte {
	host := c.host
	proto := "ws"
	hostNoScheme := strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
	if strings.HasPrefix(strings.ToLower(host), "https://") {
		proto = "wss"
	}
	b, _ := json.Marshal(map[string]any{
		"rch": true,
		"ws":  host + "/ws",
		"nws": proto + "://" + hostNoScheme + "/nws",
	})
	return b
}

// rchResponse is the parsed result of a returnHeaders dispatch: the page body
// plus the response headers the device observed. The point is Set-Cookie, which
// the browser fetch API hides from JS but the Android native layer exposes — so
// this is how a balancer drives a cookie-based login (e.g. DLE) over the hub.
type rchResponse struct {
	Body    string
	Headers http.Header
}

// Cookies parses the Set-Cookie headers the device reported.
func (r *rchResponse) Cookies() []*http.Cookie {
	if r == nil || len(r.Headers) == 0 {
		return nil
	}
	return (&http.Response{Header: r.Headers}).Cookies()
}

// CookieHeader renders the returned cookies as a "name=value; ..." string, ready
// to drop straight into a Cookie request header on the next call.
func (r *rchResponse) CookieHeader() string {
	cookies := r.Cookies()
	if len(cookies) == 0 {
		return ""
	}
	parts := make([]string, 0, len(cookies))
	for _, ck := range cookies {
		parts = append(parts, ck.Name+"="+ck.Value)
	}
	return strings.Join(parts, "; ")
}

// Get dispatches a GET to the device and returns the response body.
func (c *rchClient) Get(ctx context.Context, rawurl string, headers map[string]string) (string, error) {
	return c.sendRaw(ctx, rawurl, "", headers, false)
}

// Post dispatches a POST (data is the request body) and returns the response body.
func (c *rchClient) Post(ctx context.Context, rawurl, data string, headers map[string]string) (string, error) {
	return c.sendRaw(ctx, rawurl, data, headers, false)
}

// GetWithHeaders dispatches a GET and asks the device to also report the
// response headers. Mainly useful for "apk" clients — browser (cors/web)
// clients cannot read forbidden response headers such as Set-Cookie.
func (c *rchClient) GetWithHeaders(ctx context.Context, rawurl string, headers map[string]string) (*rchResponse, error) {
	return c.sendEnvelope(ctx, rawurl, "", headers)
}

// PostWithHeaders is the POST form of GetWithHeaders — e.g. a DLE login whose
// auth cookie comes back in Set-Cookie.
func (c *rchClient) PostWithHeaders(ctx context.Context, rawurl, data string, headers map[string]string) (*rchResponse, error) {
	return c.sendEnvelope(ctx, rawurl, data, headers)
}

func (c *rchClient) sendEnvelope(ctx context.Context, rawurl, data string, headers map[string]string) (*rchResponse, error) {
	raw, err := c.sendRaw(ctx, rawurl, data, headers, true)
	if err != nil {
		return nil, err
	}
	return parseRchEnvelope(raw), nil
}

func (c *rchClient) sendRaw(ctx context.Context, rawurl, data string, headers map[string]string, returnHeaders bool) (string, error) {
	if c.IsNotConnected() || c.hub == nil {
		return "", errRchNoClient
	}

	rchID := generateConnID()
	entry := rchRegisterPending(rchID)
	defer rchDeletePending(rchID)

	c.hub.SendRchRequest(c.connID, rchID, rawurl, data, headers, returnHeaders)

	timeout := rchDefaultTimeout
	if dl, ok := ctx.Deadline(); ok {
		if d := time.Until(dl); d > 0 && d < timeout {
			timeout = d
		}
	}
	if !rchWaitDone(entry, timeout) {
		return "", errRchTimeout
	}
	return string(rchPendingBytes(entry)), nil
}

// parseRchEnvelope interprets a returnHeaders result. When returnHeaders is set,
// Lampa's native layer returns {"headers":{...},"body":"..."} and the client JS
// (invc-rch_nws.js) JSON-stringifies it before POSTing to /rch/result.
// "set-cookie" arrives as a JSON array (one entry per cookie); other headers as
// strings. If the payload isn't that envelope (older client, plain body), the
// whole payload is treated as the body with no headers.
func parseRchEnvelope(raw string) *rchResponse {
	resp := &rchResponse{Body: raw, Headers: http.Header{}}

	var top map[string]any
	if err := json.Unmarshal([]byte(raw), &top); err != nil {
		return resp // not JSON: raw is the body
	}
	bodyVal, hasBody := top["body"]
	if !hasBody {
		return resp // valid JSON but not our envelope: keep raw as body
	}

	// body is normally a string; tolerate a structured body by re-encoding it
	// rather than dropping it.
	if s, ok := bodyVal.(string); ok {
		resp.Body = s
	} else if b, err := json.Marshal(bodyVal); err == nil {
		resp.Body = string(b)
	}

	if hdrs, ok := top["headers"].(map[string]any); ok {
		for name, val := range hdrs {
			switch v := val.(type) {
			case string:
				resp.Headers.Add(name, v) // single-valued header
			case []any:
				for _, item := range v { // multi-valued, e.g. set-cookie
					if s, ok := item.(string); ok {
						resp.Headers.Add(name, s)
					}
				}
			}
		}
	}
	return resp
}

// ---------------------------------------------------------------------------
//  Balancer integration helpers
//
//  These are the ~3-line opt-in surface for a balancer. A balancer reads its
//  per-source `rhub` config flag once (into a struct field) and then:
//      if rchGate(w, r, s.rhub) { return }                 // top of the handler
//      body, err := rchFetch(r, s.rhub, url, hdr, direct)  // wrapping each fetch
//  With rhub=false both are no-ops, so behaviour is byte-for-byte unchanged.
// ---------------------------------------------------------------------------

// rchGate implements the "rhub on but no device bound" handshake. When rhub is
// enabled and no connected device is bound to this request (missing/!valid
// nws_id), it writes the ConnectionMsg — telling the Lampa client to open the
// hub and retry with ?nws_id — and returns true so the handler aborts.
// Otherwise it returns false and the handler proceeds (direct fetch, or hub
// fetch once a device is bound).
func rchGate(w http.ResponseWriter, r *http.Request, rhub bool) bool {
	if !rhub {
		return false
	}
	if capiResolveRequest(r) {
		return false // server-side /capi aggregation: do the direct fetch, not the device handshake
	}
	rch := newRchClient(r)
	if rch.IsConnected() {
		return false
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(rch.ConnectionMsg())
	return true
}

// rchFetch routes a single page fetch through the reverse client hub when rhub
// is enabled and a device is connected; otherwise — or if the hub fetch fails
// or comes back empty — it falls back to the supplied direct fetch. The
// fallback keeps a transient hub problem non-fatal and leaves rhub=false
// behaviour identical to before.
func rchFetch(r *http.Request, rhub bool, target string, headers map[string]string, direct func() (string, error)) (string, error) {
	if rhub {
		if rch := newRchClient(r); rch.IsConnected() {
			if body, err := rch.Get(r.Context(), target, headers); err == nil && body != "" {
				return body, nil
			}
		}
	}
	return direct()
}

// rchReqCtxKey carries the inbound *http.Request through a context so balancers
// whose fetch helpers take ctx (not *http.Request) can still reach the hub.
type rchReqCtxKey struct{}

// rchWithRequest returns a context (derived from r.Context()) that carries r.
// Seed it once at the handler — ctx := rchWithRequest(req) — then pass ctx down
// as usual; ctx-based fetch helpers recover it via rchFetchCtx.
func rchWithRequest(r *http.Request) context.Context {
	return context.WithValue(r.Context(), rchReqCtxKey{}, r)
}

// rchFetchCtx is rchFetch for callers that only have a context. It recovers the
// request seeded by rchWithRequest; if absent, it just runs the direct fetch.
func rchFetchCtx(ctx context.Context, rhub bool, target string, headers map[string]string, direct func() (string, error)) (string, error) {
	if rhub {
		if r, ok := ctx.Value(rchReqCtxKey{}).(*http.Request); ok && r != nil {
			return rchFetch(r, rhub, target, headers, direct)
		}
	}
	return direct()
}
