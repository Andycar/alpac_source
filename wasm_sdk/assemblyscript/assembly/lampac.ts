// AssemblyScript SDK for lampac-go WASM plugins.
//
// Mirrors wasm_sdk/tinygo/lampac.go and wasm_sdk/rust/src/lib.rs — same ABI,
// same JSON shapes. The host treats every guest the same way.
//
// Build:
//   npx asc index.ts -o plugin.wasm --runtime stub --optimize --use abort=
//
// `--use abort=` strips the abort import; without it, asc emits an `abort`
// import that the host doesn't provide.

// === Host imports ===

@external("lampac", "host_log")
declare function hostLog(level: u32, ptr: usize, len: u32): void;

@external("lampac", "host_http")
declare function hostHTTP(reqPtr: usize, reqLen: u32): u64;

@external("lampac", "host_proxy_url")
declare function hostProxyURL(reqPtr: usize, reqLen: u32): u64;

@external("lampac", "host_cache_get")
declare function hostCacheGet(keyPtr: usize, keyLen: u32): u64;

@external("lampac", "host_cache_set")
declare function hostCacheSet(keyPtr: usize, keyLen: u32, valPtr: usize, valLen: u32, ttlSec: u32): void;

@external("lampac", "host_config")
declare function hostConfig(): u64;

// === alloc / response — required exports ===
//
// asc emits `__new`/`__pin` for managed objects; we expose a flat byte buffer
// allocator instead so the host can write into a stable address. The host
// only ever calls our `alloc` from outside, so a leak-on-exit is fine.

export function alloc(size: u32): usize {
    // ArrayBuffer layout in asc keeps a header before the data; we instead
    // hand the host a raw byte region via heap.alloc, which is plain bump
    // allocation. The region is reachable through `__alloc`-equivalent.
    const ptr = heap.alloc(size);
    return ptr;
}

export function abi_version(): u32 { return 1; }

// === packing helpers ===

@inline
function pack(ptr: usize, len: u32): u64 {
    return ((<u64>ptr) << <u64>32) | <u64>len;
}

@inline
function unpack(packed: u64): StaticArray<u32> {
    const out = new StaticArray<u32>(2);
    out[0] = <u32>(packed >> 32);   // ptr
    out[1] = <u32>(packed & 0xFFFFFFFF); // len
    return out;
}

// === Bytes / strings ===

export function readBytes(ptr: usize, len: u32): Uint8Array {
    const out = new Uint8Array(len);
    for (let i: u32 = 0; i < len; i++) {
        out[i] = load<u8>(ptr + i);
    }
    return out;
}

export function readString(ptr: usize, len: u32): string {
    return String.UTF8.decodeUnsafe(ptr, len);
}

function utf8Encode(s: string): Uint8Array {
    const bytes = String.UTF8.encode(s, false);
    const out = new Uint8Array(bytes.byteLength);
    memory.copy(out.dataStart, changetype<usize>(bytes), bytes.byteLength);
    return out;
}

// Write `bytes` into a fresh `alloc`-ed region and return packed (ptr,len).
// This is what `handle()` returns to the host.
export function writeResponse(bytes: Uint8Array): u64 {
    if (bytes.length == 0) return 0;
    const ptr = alloc(<u32>bytes.length);
    for (let i: i32 = 0; i < bytes.length; i++) {
        store<u8>(ptr + <usize>i, bytes[i]);
    }
    return pack(ptr, <u32>bytes.length);
}

// Convenience: encode a JSON string into the response shape.
export function writeJSON(json: string): u64 {
    return writeResponse(utf8Encode(json));
}

// === Logging ===

export function debug(s: string): void { logAt(0, s); }
export function info(s: string): void { logAt(1, s); }
export function warn(s: string): void { logAt(2, s); }
export function error(s: string): void { logAt(3, s); }

function logAt(level: u32, s: string): void {
    if (s.length == 0) return;
    const buf = utf8Encode(s);
    hostLog(level, buf.dataStart, <u32>buf.length);
}

// === HTTP ===

export class HTTPRequest {
    method: string = "GET";
    url: string = "";
    headers: Map<string, string> = new Map<string, string>();
    body: string = "";
    timeoutMs: i32 = 0;
}

export class HTTPResponse {
    status: i32 = 0;
    headers: Map<string, string> = new Map<string, string>();
    body: Uint8Array = new Uint8Array(0);
    error: string = "";
}

// http builds the JSON request blob, calls hostHTTP, decodes the response.
// We hand-roll JSON because asc's JSON support is awkward; this keeps the
// runtime tiny.
export function http(req: HTTPRequest): HTTPResponse {
    const reqJSON = encodeHTTPRequest(req);
    const reqBytes = utf8Encode(reqJSON);
    const packed = hostHTTP(reqBytes.dataStart, <u32>reqBytes.length);
    const out = new HTTPResponse();
    if (packed == 0) { out.error = "host returned no data"; return out; }
    const parts = unpack(packed);
    const respPtr = <usize>parts[0];
    const respLen = parts[1];
    const respText = readString(respPtr, respLen);
    return decodeHTTPResponse(respText);
}

function encodeHTTPRequest(req: HTTPRequest): string {
    let s = "{";
    s += `"method":${jsonString(req.method == "" ? "GET" : req.method)},`;
    s += `"url":${jsonString(req.url)}`;
    if (req.timeoutMs > 0) s += `,"timeout_ms":${req.timeoutMs}`;
    if (req.body.length > 0) s += `,"body":${jsonString(req.body)}`;
    if (req.headers.size > 0) {
        s += `,"headers":{`;
        const keys = req.headers.keys();
        for (let i = 0; i < keys.length; i++) {
            if (i > 0) s += ",";
            s += `${jsonString(keys[i])}:${jsonString(req.headers.get(keys[i]))}`;
        }
        s += `}`;
    }
    s += "}";
    return s;
}

function decodeHTTPResponse(json: string): HTTPResponse {
    const out = new HTTPResponse();
    out.status = <i32>extractNumber(json, "status");
    out.error = extractString(json, "error");
    const b64 = extractString(json, "body_b64");
    if (b64.length > 0) out.body = base64Decode(b64);
    return out;
}

// === Proxy URL ===

export function proxyURL(uri: string, plugin: string, headers: Map<string, string>): string {
    let s = `{"uri":${jsonString(uri)}`;
    if (plugin.length > 0) s += `,"plugin":${jsonString(plugin)}`;
    if (headers && headers.size > 0) {
        s += `,"headers":{`;
        const keys = headers.keys();
        for (let i = 0; i < keys.length; i++) {
            if (i > 0) s += ",";
            s += `${jsonString(keys[i])}:${jsonString(headers.get(keys[i]))}`;
        }
        s += `}`;
    }
    s += "}";
    const reqBytes = utf8Encode(s);
    const packed = hostProxyURL(reqBytes.dataStart, <u32>reqBytes.length);
    if (packed == 0) return uri;
    const parts = unpack(packed);
    const respText = readString(<usize>parts[0], parts[1]);
    const u = extractString(respText, "url");
    return u.length > 0 ? u : uri;
}

// === Cache ===

export function cacheGet(key: string): Uint8Array | null {
    const kb = utf8Encode(key);
    const packed = hostCacheGet(kb.dataStart, <u32>kb.length);
    if (packed == 0) return null;
    const parts = unpack(packed);
    return readBytes(<usize>parts[0], parts[1]);
}

export function cacheSet(key: string, value: Uint8Array, ttlSec: u32): void {
    const kb = utf8Encode(key);
    hostCacheSet(kb.dataStart, <u32>kb.length, value.dataStart, <u32>value.length, ttlSec);
}

// === Tiny JSON helpers (stringify/extract — not a full parser) ===
//
// These don't try to be a complete JSON impl. They cover the host's
// well-defined response shapes. If your plugin needs richer parsing, pull
// in `as-json` or similar from npm.

function jsonString(s: string): string {
    let out = `"`;
    for (let i = 0; i < s.length; i++) {
        const c = s.charCodeAt(i);
        if (c == 0x22 /* " */) out += `\\"`;
        else if (c == 0x5c /* \ */) out += `\\\\`;
        else if (c == 0x0a) out += `\\n`;
        else if (c == 0x0d) out += `\\r`;
        else if (c == 0x09) out += `\\t`;
        else if (c < 0x20) {
            const hex = c.toString(16);
            out += `\\u${"0000".substring(0, 4 - hex.length)}${hex}`;
        } else out += s.charAt(i);
    }
    return out + `"`;
}

// Minimal "find a top-level key value" extractor — works on the host's
// flat response shapes. Doesn't handle nested escapes inside strings.
function extractString(json: string, key: string): string {
    const needle = `"${key}":"`;
    const i = json.indexOf(needle);
    if (i < 0) return "";
    const start = i + needle.length;
    const end = findStringEnd(json, start);
    if (end < 0) return "";
    return unescapeJSON(json.substring(start, end));
}

function findStringEnd(s: string, start: i32): i32 {
    for (let i = start; i < s.length; i++) {
        const c = s.charCodeAt(i);
        if (c == 0x5c /* \ */) { i++; continue; } // skip escape
        if (c == 0x22 /* " */) return i;
    }
    return -1;
}

function unescapeJSON(s: string): string {
    if (s.indexOf("\\") < 0) return s;
    let out = "";
    for (let i = 0; i < s.length; i++) {
        const c = s.charCodeAt(i);
        if (c == 0x5c && i + 1 < s.length) {
            const n = s.charCodeAt(i + 1);
            if (n == 0x6e) { out += "\n"; i++; }
            else if (n == 0x72) { out += "\r"; i++; }
            else if (n == 0x74) { out += "\t"; i++; }
            else if (n == 0x22) { out += "\""; i++; }
            else if (n == 0x5c) { out += "\\"; i++; }
            else out += s.charAt(i);
        } else out += s.charAt(i);
    }
    return out;
}

function extractNumber(json: string, key: string): f64 {
    const needle = `"${key}":`;
    const i = json.indexOf(needle);
    if (i < 0) return 0;
    let start = i + needle.length;
    while (start < json.length && json.charCodeAt(start) == 0x20) start++;
    let end = start;
    while (end < json.length) {
        const c = json.charCodeAt(end);
        if ((c >= 0x30 && c <= 0x39) || c == 0x2d || c == 0x2e) end++;
        else break;
    }
    return parseFloat(json.substring(start, end));
}

// === Base64 (decode only — host returns body_b64) ===

const B64_TABLE = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

function base64Decode(s: string): Uint8Array {
    let clean = "";
    for (let i = 0; i < s.length; i++) {
        const c = s.charAt(i);
        if (c == "=" || c == "\n" || c == "\r" || c == " ") continue;
        clean += c;
    }
    const len = (clean.length * 3) / 4 | 0;
    const out = new Uint8Array(len);
    let oi = 0;
    for (let i = 0; i + 3 < clean.length + 4; i += 4) {
        const c0 = i < clean.length ? B64_TABLE.indexOf(clean.charAt(i)) : 0;
        const c1 = i + 1 < clean.length ? B64_TABLE.indexOf(clean.charAt(i + 1)) : 0;
        const c2 = i + 2 < clean.length ? B64_TABLE.indexOf(clean.charAt(i + 2)) : 0;
        const c3 = i + 3 < clean.length ? B64_TABLE.indexOf(clean.charAt(i + 3)) : 0;
        if (oi < len) out[oi++] = ((c0 << 2) | (c1 >> 4)) & 0xff;
        if (oi < len) out[oi++] = ((c1 << 4) | (c2 >> 2)) & 0xff;
        if (oi < len) out[oi++] = ((c2 << 6) | c3) & 0xff;
    }
    return out;
}

// === Invocation reading ===

export class Invocation {
    raw: string = "";
    path: string = "";
    requestIP: string = "";
    config: string = "";
    // Query/headers are expensive to parse; we expose accessors instead of
    // pre-decoded maps to keep the runtime small. Plugins that need them can
    // call queryGet(inv, "id").
}

export function readInvocation(ptr: usize, len: u32): Invocation {
    const inv = new Invocation();
    inv.raw = readString(ptr, len);
    inv.path = extractString(inv.raw, "path");
    inv.requestIP = extractString(inv.raw, "requestIP");
    // config is a nested JSON object — extract the substring as-is.
    const ci = inv.raw.indexOf(`"config":`);
    if (ci >= 0) {
        const start = ci + 9;
        // crude: take from { to matching } at depth 0 — host always emits
        // a balanced object here.
        if (start < inv.raw.length && inv.raw.charAt(start) == "{") {
            let depth = 1;
            let j = start + 1;
            while (j < inv.raw.length && depth > 0) {
                const c = inv.raw.charCodeAt(j);
                if (c == 0x7b) depth++;
                else if (c == 0x7d) depth--;
                j++;
            }
            inv.config = inv.raw.substring(start, j);
        }
    }
    return inv;
}

// queryGet picks one value out of inv.raw for plugins that don't want to
// parse the full query map. Falls back to "" on miss.
export function queryGet(inv: Invocation, name: string): string {
    const qi = inv.raw.indexOf(`"query":`);
    if (qi < 0) return "";
    const start = inv.raw.indexOf(`"${name}":"`, qi);
    if (start < 0) return "";
    const valStart = start + name.length + 4;
    const valEnd = findStringEnd(inv.raw, valStart);
    if (valEnd < 0) return "";
    return unescapeJSON(inv.raw.substring(valStart, valEnd));
}
