//! Rust SDK for lampac-go WASM plugins.
//!
//! Mirror of `wasm_sdk/tinygo/lampac.go`. Plugins compiled with
//! `cargo build --release --target wasm32-wasip1` (or `wasm32-wasi` on
//! older toolchains) and dropped next to a manifest.json get loaded by the
//! lampac-go runtime exactly like TinyGo plugins — same ABI, same host
//! imports.
//!
//! Quick start:
//!
//! ```ignore
//! use lampac_sdk::{handle_export, Invocation, Response};
//!
//! handle_export!(my_handle);
//!
//! fn my_handle(inv: Invocation) -> Response {
//!     lampac_sdk::info(&format!("hello from rust, path={}", inv.path));
//!     Response::json(serde_json::json!({"type": "movie", "data": []}))
//! }
//! ```

use serde::{Deserialize, Serialize};
use std::cell::RefCell;
use std::collections::HashMap;
use std::sync::Mutex;

// === Allocator ===
//
// alloc() is exported so the host can deposit data into our linear memory
// before calling handle(). We back it with a Vec<u8> per call and stash the
// vectors in a thread-local ring so they outlive the immediate caller.

thread_local! {
    static LIVE: RefCell<Vec<Vec<u8>>> = RefCell::new(Vec::new());
}

#[no_mangle]
pub extern "C" fn alloc(size: u32) -> u32 {
    let mut buf = vec![0u8; size as usize];
    let ptr = buf.as_mut_ptr() as u32;
    LIVE.with(|live| {
        let mut live = live.borrow_mut();
        // Cap the ring; we only need recent buffers alive long enough for the
        // host to write a response and the guest to read it.
        if live.len() >= 16 {
            live.remove(0);
        }
        live.push(buf);
    });
    ptr
}

#[no_mangle]
pub extern "C" fn abi_version() -> u32 { 1 }

// === Pointer/length packing ===

#[inline]
pub fn pack(ptr: u32, len: u32) -> u64 {
    ((ptr as u64) << 32) | (len as u64)
}

#[inline]
pub fn unpack(v: u64) -> (u32, u32) {
    ((v >> 32) as u32, (v & 0xFFFF_FFFF) as u32)
}

unsafe fn read_bytes(ptr: u32, len: u32) -> Vec<u8> {
    if ptr == 0 || len == 0 {
        return Vec::new();
    }
    let slice = std::slice::from_raw_parts(ptr as *const u8, len as usize);
    slice.to_vec()
}

fn publish(bytes: Vec<u8>) -> u64 {
    if bytes.is_empty() {
        return 0;
    }
    let len = bytes.len() as u32;
    let ptr = alloc(len);
    unsafe {
        std::ptr::copy_nonoverlapping(bytes.as_ptr(), ptr as *mut u8, len as usize);
    }
    pack(ptr, len)
}

// === Host imports ===

#[link(wasm_import_module = "lampac")]
extern "C" {
    fn host_log(level: u32, ptr: u32, len: u32);
    fn host_http(req_ptr: u32, req_len: u32) -> u64;
    fn host_proxy_url(req_ptr: u32, req_len: u32) -> u64;
    fn host_cache_get(key_ptr: u32, key_len: u32) -> u64;
    fn host_cache_set(key_ptr: u32, key_len: u32, val_ptr: u32, val_len: u32, ttl_sec: u32);
    fn host_config() -> u64;
}

// === Public types ===

#[derive(Default, Debug, Serialize, Deserialize)]
pub struct Invocation {
    #[serde(default)]
    pub query: HashMap<String, String>,
    #[serde(default)]
    pub headers: HashMap<String, String>,
    #[serde(default)]
    pub host: String,
    #[serde(default, rename = "requestIP")]
    pub request_ip: String,
    #[serde(default)]
    pub path: String,
    #[serde(default)]
    pub life: bool,
    #[serde(default)]
    pub checksearch: bool,
    #[serde(default, rename = "userAgent")]
    pub user_agent: String,
    /// Raw config snapshot — manifest defaults + admin overrides + (for
    /// middleware) the upstream-results array under `__upstreams`.
    #[serde(default)]
    pub config: serde_json::Value,
}

impl Invocation {
    pub fn read(ptr: u32, len: u32) -> Self {
        let raw = unsafe { read_bytes(ptr, len) };
        serde_json::from_slice(&raw).unwrap_or_default()
    }

    /// `Upstreams` returns the upstream-balancer responses injected into
    /// `config.__upstreams` for middleware plugins. Empty for plain server.
    pub fn upstreams(&self) -> Vec<UpstreamResult> {
        self.config
            .get("__upstreams")
            .and_then(|v| serde_json::from_value(v.clone()).ok())
            .unwrap_or_default()
    }
}

#[derive(Debug, Serialize, Deserialize, Default)]
pub struct UpstreamResult {
    pub balancer: String,
    pub status: i32,
    #[serde(default)]
    pub body: serde_json::Value,
    #[serde(default)]
    pub error: String,
}

#[derive(Debug)]
pub struct Response(Vec<u8>);

impl Response {
    pub fn raw(body: Vec<u8>) -> Self { Response(body) }
    pub fn json<T: Serialize>(value: T) -> Self {
        Response(serde_json::to_vec(&value).unwrap_or_else(|_| b"{}".to_vec()))
    }
    pub fn into_packed(self) -> u64 { publish(self.0) }
}

/// Macro that wires up the required `handle(ptr,len) -> i64` export by
/// translating to and from the SDK types. Use it once in your plugin's
/// `lib.rs`:
///
/// ```ignore
/// lampac_sdk::handle_export!(my_handler);
/// fn my_handler(inv: lampac_sdk::Invocation) -> lampac_sdk::Response { ... }
/// ```
#[macro_export]
macro_rules! handle_export {
    ($f:ident) => {
        #[no_mangle]
        pub extern "C" fn handle(ptr: u32, len: u32) -> u64 {
            let inv = $crate::Invocation::read(ptr, len);
            $f(inv).into_packed()
        }
    };
}

// === Logging ===

pub fn debug(s: &str) { log_at(0, s) }
pub fn info(s: &str)  { log_at(1, s) }
pub fn warn(s: &str)  { log_at(2, s) }
pub fn error(s: &str) { log_at(3, s) }

fn log_at(level: u32, s: &str) {
    if s.is_empty() { return }
    unsafe { host_log(level, s.as_ptr() as u32, s.len() as u32); }
}

// === HTTP ===

#[derive(Debug, Default, Serialize, Deserialize)]
pub struct HttpRequest {
    pub method: String,
    pub url: String,
    #[serde(default, skip_serializing_if = "HashMap::is_empty")]
    pub headers: HashMap<String, String>,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub body: String,
    #[serde(default, skip_serializing_if = "String::is_empty", rename = "body_b64")]
    pub body_b64: String,
    #[serde(default, skip_serializing_if = "is_zero", rename = "timeout_ms")]
    pub timeout_ms: u32,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub transport: String,
}

fn is_zero(n: &u32) -> bool { *n == 0 }

#[derive(Debug, Default, Serialize, Deserialize)]
pub struct HttpResponse {
    #[serde(default)]
    pub status: i32,
    #[serde(default)]
    pub headers: HashMap<String, String>,
    #[serde(default, rename = "body_b64")]
    pub body_b64: String,
    #[serde(default)]
    pub error: String,
}

impl HttpResponse {
    pub fn body(&self) -> Vec<u8> {
        if self.body_b64.is_empty() {
            return Vec::new();
        }
        use base64::Engine;
        base64::engine::general_purpose::STANDARD
            .decode(&self.body_b64)
            .unwrap_or_default()
    }
}

pub fn http(req: HttpRequest) -> HttpResponse {
    let body = serde_json::to_vec(&req).unwrap_or_default();
    if body.is_empty() {
        return HttpResponse { error: "marshal failed".into(), ..Default::default() };
    }
    let packed = unsafe { host_http(body.as_ptr() as u32, body.len() as u32) };
    if packed == 0 {
        return HttpResponse { error: "host_http: empty response".into(), ..Default::default() };
    }
    let (ptr, len) = unpack(packed);
    let raw = unsafe { read_bytes(ptr, len) };
    serde_json::from_slice(&raw).unwrap_or_default()
}

pub fn http_get(url: &str) -> HttpResponse {
    http(HttpRequest { method: "GET".into(), url: url.into(), ..Default::default() })
}

// === Proxy URL signing ===

#[derive(Debug, Default, Serialize)]
struct ProxyReq<'a> {
    uri: &'a str,
    plugin: &'a str,
    #[serde(skip_serializing_if = "HashMap::is_empty")]
    headers: HashMap<String, String>,
}

#[derive(Debug, Default, Deserialize)]
struct ProxyResp {
    #[serde(default)]
    url: String,
    #[serde(default)]
    error: String,
}

pub fn proxy_url(uri: &str, plugin: &str) -> String {
    proxy_call(uri, plugin, HashMap::new())
}

pub fn proxy_url_with_headers(uri: &str, plugin: &str, headers: HashMap<String, String>) -> String {
    proxy_call(uri, plugin, headers)
}

fn proxy_call(uri: &str, plugin: &str, headers: HashMap<String, String>) -> String {
    let req = ProxyReq { uri, plugin, headers };
    let body = match serde_json::to_vec(&req) {
        Ok(b) if !b.is_empty() => b,
        _ => return uri.to_string(),
    };
    let packed = unsafe { host_proxy_url(body.as_ptr() as u32, body.len() as u32) };
    if packed == 0 { return uri.to_string(); }
    let (ptr, len) = unpack(packed);
    let raw = unsafe { read_bytes(ptr, len) };
    let resp: ProxyResp = serde_json::from_slice(&raw).unwrap_or_default();
    if resp.url.is_empty() { uri.to_string() } else { resp.url }
}

// === Cache ===

pub fn cache_get(key: &str) -> Option<Vec<u8>> {
    if key.is_empty() { return None; }
    let packed = unsafe { host_cache_get(key.as_ptr() as u32, key.len() as u32) };
    if packed == 0 { return None; }
    let (ptr, len) = unpack(packed);
    Some(unsafe { read_bytes(ptr, len) })
}

pub fn cache_set(key: &str, value: &[u8], ttl_seconds: u32) {
    if key.is_empty() || value.is_empty() { return }
    unsafe {
        host_cache_set(
            key.as_ptr() as u32, key.len() as u32,
            value.as_ptr() as u32, value.len() as u32,
            ttl_seconds,
        );
    }
}

// === Config ===

pub fn config() -> Vec<u8> {
    let packed = unsafe { host_config() };
    if packed == 0 { return b"{}".to_vec(); }
    let (ptr, len) = unpack(packed);
    unsafe { read_bytes(ptr, len) }
}

// Avoid an "unused" warning for the Mutex import on builds where caller code
// doesn't use it. The Mutex is here for future-proofing.
#[doc(hidden)]
pub fn _doc_anchor() { let _: Mutex<u8> = Mutex::new(0); }

pub mod client;
