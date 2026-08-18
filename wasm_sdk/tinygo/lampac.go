// Package lampac is a thin SDK glue for writing lampac-go server plugins in
// TinyGo. It marshals to/from the host ABI described in
// internal/wasmmodules/abi.go: linear-memory pointers, packed (ptr,len) i64
// returns, and JSON payloads on top.
//
// Build with:
//
//	tinygo build -o plugin.wasm -target wasi -no-debug ./
//
// Use:
//
//	package main
//
//	import "lampac.cc/sdk/lampac"
//
//	func main() {} // required for TinyGo wasi
//
//	//export handle
//	func handle(ptr, length uint32) uint64 {
//	    inv := lampac.ReadInvocation(ptr, length)
//	    body := []byte(`{"type":"movie","data":[{"name":"hello from wasm"}]}`)
//	    return lampac.WriteResponse(body)
//	}
package lampac

import (
	"encoding/base64"
	"encoding/json"
	"unsafe"
)

// === Memory helpers ===

// alloc is the guest-side allocator the host calls to deposit data into our
// linear memory. We back it with a Go slice so the runtime keeps it alive
// until we let it go.
//
// NOTE: we keep alive a small ring of recent buffers so the host can write a
// response, the guest reads it, and only then is the buffer GC'd. A 16-slot
// ring is plenty for the synchronous request/response style this ABI uses.
var liveBufs [16][]byte
var liveCursor int

//export alloc
func alloc(size uint32) uint32 {
	buf := make([]byte, size)
	liveBufs[liveCursor] = buf
	liveCursor = (liveCursor + 1) % len(liveBufs)
	if size == 0 {
		return 0
	}
	return uint32(uintptr(unsafe.Pointer(&buf[0])))
}

//export abi_version
func abiVersion() uint32 { return 1 }

// === Pointer/length packing matches host's UnpackPtrLen ===

func pack(ptr, length uint32) uint64 { return (uint64(ptr) << 32) | uint64(length) }

func unpack(v uint64) (uint32, uint32) {
	return uint32(v >> 32), uint32(v & 0xFFFFFFFF)
}

func readBytes(ptr, length uint32) []byte {
	if ptr == 0 || length == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), length)
}

// publish copies b into a fresh alloc'd buffer (so it survives past the
// caller's stack frame) and returns the packed (ptr,len) the host expects.
func publish(b []byte) uint64 {
	if len(b) == 0 {
		return 0
	}
	ptr := alloc(uint32(len(b)))
	dst := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), len(b))
	copy(dst, b)
	return pack(ptr, uint32(len(b)))
}

// === Host imports (must match HostModuleName="lampac") ===

//go:wasmimport lampac host_log
func hostLog(level uint32, ptr, length uint32)

//go:wasmimport lampac host_http
func hostHTTP(reqPtr, reqLen uint32) uint64

//go:wasmimport lampac host_proxy_url
func hostProxyURL(reqPtr, reqLen uint32) uint64

//go:wasmimport lampac host_cache_get
func hostCacheGet(keyPtr, keyLen uint32) uint64

//go:wasmimport lampac host_cache_set
func hostCacheSet(keyPtr, keyLen, valPtr, valLen, ttlSec uint32)

//go:wasmimport lampac host_config
func hostConfig() uint64

// === Public SDK ===

// Invocation matches internal/wasmmodules.Invocation on the host side.
type Invocation struct {
	Query       map[string]string `json:"query"`
	Headers     map[string]string `json:"headers"`
	Host        string            `json:"host"`
	RequestIP   string            `json:"requestIP"`
	Path        string            `json:"path"`
	Life        bool              `json:"life"`
	Checksearch bool              `json:"checksearch"`
	UserAgent   string            `json:"userAgent"`
	Config      json.RawMessage   `json:"config"`
}

// UpstreamResult is what middleware plugins receive — one per declared
// upstream balancer.
type UpstreamResult struct {
	Balancer string          `json:"balancer"`
	Status   int             `json:"status"`
	Body     json.RawMessage `json:"body"`
	Error    string          `json:"error,omitempty"`
}

// Upstreams extracts the upstream array the host injected into inv.Config
// for middleware plugins. Returns nil for plain server modules.
func Upstreams(inv Invocation) []UpstreamResult {
	if len(inv.Config) == 0 {
		return nil
	}
	var raw struct {
		Upstreams []UpstreamResult `json:"__upstreams"`
	}
	if err := json.Unmarshal(inv.Config, &raw); err != nil {
		return nil
	}
	return raw.Upstreams
}

// ReadInvocation parses the JSON the host wrote at (ptr,length).
func ReadInvocation(ptr, length uint32) Invocation {
	var inv Invocation
	if data := readBytes(ptr, length); len(data) > 0 {
		_ = json.Unmarshal(data, &inv)
	}
	return inv
}

// WriteResponse publishes raw JSON bytes back to the host. Use this to return
// from your handle() function.
func WriteResponse(body []byte) uint64 { return publish(body) }

// WriteJSON is a convenience wrapper around WriteResponse + json.Marshal.
func WriteJSON(v any) uint64 {
	b, err := json.Marshal(v)
	if err != nil {
		return WriteResponse([]byte(`{"error":"marshal failed"}`))
	}
	return WriteResponse(b)
}

// === Logging ===

func Debug(s string) { logAt(0, s) }
func Info(s string)  { logAt(1, s) }
func Warn(s string)  { logAt(2, s) }
func Error(s string) { logAt(3, s) }

func logAt(level uint32, s string) {
	if s == "" {
		return
	}
	b := []byte(s)
	hostLog(level, uint32(uintptr(unsafe.Pointer(&b[0]))), uint32(len(b)))
}

// === HTTP ===

type HTTPRequest struct {
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      string            `json:"body,omitempty"`
	BodyB64   string            `json:"body_b64,omitempty"`
	TimeoutMS int               `json:"timeout_ms,omitempty"`
	Transport string            `json:"transport,omitempty"`
}

type HTTPResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	BodyB64 string            `json:"body_b64"`
	Error   string            `json:"error,omitempty"`
}

// Body returns the decoded response body or nil on error.
func (r *HTTPResponse) Body() []byte {
	if r == nil || r.BodyB64 == "" {
		return nil
	}
	b, err := base64.StdEncoding.DecodeString(r.BodyB64)
	if err != nil {
		return nil
	}
	return b
}

// HTTPDo dispatches a request through the host's HTTP client.
func HTTPDo(req HTTPRequest) HTTPResponse {
	body, err := json.Marshal(req)
	if err != nil {
		return HTTPResponse{Error: err.Error()}
	}
	reqPtr := uint32(uintptr(unsafe.Pointer(&body[0])))
	packed := hostHTTP(reqPtr, uint32(len(body)))
	if packed == 0 {
		return HTTPResponse{Error: "host_http: empty response"}
	}
	ptr, length := unpack(packed)
	var resp HTTPResponse
	_ = json.Unmarshal(readBytes(ptr, length), &resp)
	return resp
}

// HTTPGet is a shortcut for a plain GET with no headers.
func HTTPGet(url string) HTTPResponse {
	return HTTPDo(HTTPRequest{Method: "GET", URL: url})
}

// === Proxy URL signing ===

type proxyReq struct {
	URI     string            `json:"uri"`
	Plugin  string            `json:"plugin"`
	Headers map[string]string `json:"headers,omitempty"`
}

type proxyResp struct {
	URL   string `json:"url"`
	Error string `json:"error,omitempty"`
}

// ProxyURL wraps `uri` through the host's /proxy/ endpoint so the client can
// fetch it without CORS / Origin issues. plugin is the source name used for
// per-balancer rules; pass "" to default to the module ID.
func ProxyURL(uri, plugin string) string {
	return proxyCall(proxyReq{URI: uri, Plugin: plugin})
}

// ProxyURLWithHeaders is like ProxyURL but lets you embed upstream headers
// (Referer, Origin, …) into the AES payload.
func ProxyURLWithHeaders(uri, plugin string, headers map[string]string) string {
	return proxyCall(proxyReq{URI: uri, Plugin: plugin, Headers: headers})
}

func proxyCall(req proxyReq) string {
	b, _ := json.Marshal(req)
	if len(b) == 0 {
		return req.URI
	}
	ptr := uint32(uintptr(unsafe.Pointer(&b[0])))
	packed := hostProxyURL(ptr, uint32(len(b)))
	if packed == 0 {
		return req.URI
	}
	rp, rl := unpack(packed)
	var resp proxyResp
	if err := json.Unmarshal(readBytes(rp, rl), &resp); err != nil || resp.URL == "" {
		return req.URI
	}
	return resp.URL
}

// === Cache ===

// CacheGet returns the cached JSON value at key (or nil on miss).
func CacheGet(key string) []byte {
	if key == "" {
		return nil
	}
	kb := []byte(key)
	packed := hostCacheGet(uint32(uintptr(unsafe.Pointer(&kb[0]))), uint32(len(kb)))
	if packed == 0 {
		return nil
	}
	ptr, length := unpack(packed)
	out := make([]byte, length)
	copy(out, readBytes(ptr, length))
	return out
}

// CacheSet stores `value` under `key` for ttlSeconds (defaults to 5 min).
func CacheSet(key string, value []byte, ttlSeconds uint32) {
	if key == "" || len(value) == 0 {
		return
	}
	kb := []byte(key)
	hostCacheSet(
		uint32(uintptr(unsafe.Pointer(&kb[0]))), uint32(len(kb)),
		uint32(uintptr(unsafe.Pointer(&value[0]))), uint32(len(value)),
		ttlSeconds,
	)
}

// === Config ===

// Config returns the merged manifest defaults + admin overrides as raw JSON.
func Config() []byte {
	packed := hostConfig()
	if packed == 0 {
		return []byte("{}")
	}
	ptr, length := unpack(packed)
	out := make([]byte, length)
	copy(out, readBytes(ptr, length))
	return out
}
