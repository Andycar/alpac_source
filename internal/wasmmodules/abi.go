package wasmmodules

// ABI between host (this process) and guest (the .wasm plugin).
//
// # Memory model
//
// All buffers live in the guest's linear memory (module 0). The host never
// allocates inside the guest — when the host needs to hand the guest a buffer
// (e.g. an HTTP response body), it calls the guest-exported `alloc(size i32)`
// to reserve `size` bytes and writes there. The guest is responsible for
// freeing/reusing that region; for simplicity we don't require dealloc.
//
// # Pointer/length packing
//
// Functions that need to return both a pointer and a length use a packed i64:
//
//   high32(result) = ptr   (offset into guest memory)
//   low32(result)  = len   (byte count)
//
// PackPtrLen / UnpackPtrLen below match the helpers in the SDK glue that
// plugins import.
//
// # Required guest exports
//
//   alloc(size i32) i32          — reserve `size` bytes, return ptr.
//   handle(ptr i32, len i32) i64 — main entry. Input: JSON Invocation at
//                                  ptr/len. Output: PackPtrLen(ptr,len) of the
//                                  JSON Response written into guest memory
//                                  (allocated via alloc).
//
// Optional exports:
//
//   abi_version() i32            — guest's ABI revision; if missing, the host
//                                  trusts the manifest field.
//   _start, _initialize          — WASI lifecycle (TinyGo emits these).
//
// # Host imports (module name "lampac")
//
//   host_log(level i32, ptr i32, len i32)
//       level: 0=debug 1=info 2=warn 3=error
//
//   host_http(req_ptr i32, req_len i32) i64
//       Input:  JSON {"method":"GET","url":"…","headers":{…},
//                     "body":"…","body_b64":"…","timeout_ms":15000,
//                     "transport":"default|utls|socks5|flaresolverr"}
//       Output: JSON {"status":200,"headers":{…},"body_b64":"…","error":""}
//       Returned i64 is PackPtrLen(ptr,len) of the response in guest memory.
//
//   host_proxy_url(req_ptr i32, req_len i32) i64
//       Input:  JSON {"uri":"…","plugin":"…","headers":{…}}
//       Output: JSON {"url":"https://host/proxy/<enc>"} or {"error":"…"}
//
//   host_cache_get(key_ptr i32, key_len i32) i64
//       Output: PackPtrLen for the cached value (0 if miss).
//
//   host_cache_set(key_ptr i32, key_len i32, val_ptr i32, val_len i32, ttl_sec i32)
//
//   host_config(out_ptr_out i32) i64 — JSON dump of the module's config
//       snapshot. Returned as PackPtrLen.
//
// # Invocation / Response shape
//
// Invocation matches the JS modules' invocation object:
//
//   {
//     "query":     {"id":"123","s":"1"},
//     "headers":   {…},
//     "host":      "https://example.com",
//     "requestIP": "1.2.3.4",
//     "path":      "kinotochka",
//     "life":      false,
//     "checksearch": false,
//     "userAgent": "…",
//     "config":    {…}
//   }
//
// Response is whatever the JS modules return — typically:
//
//   {"type":"movie","data":[{…}],"voice":[…]}
//   {"method":"play","url":"…","title":"…"}

const (
	HostModuleName = "lampac"

	LogLevelDebug = 0
	LogLevelInfo  = 1
	LogLevelWarn  = 2
	LogLevelError = 3
)

// PackPtrLen folds a (ptr,len) pair into a single i64 for cross-ABI returns.
// Top 32 bits are the pointer; bottom 32 bits are the length.
func PackPtrLen(ptr, length uint32) uint64 {
	return (uint64(ptr) << 32) | uint64(length)
}

// UnpackPtrLen splits a packed i64 result back into (ptr, len).
func UnpackPtrLen(v uint64) (ptr, length uint32) {
	return uint32(v >> 32), uint32(v & 0xFFFFFFFF)
}
