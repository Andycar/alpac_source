// Zig SDK for lampac-go WASM plugins.
//
// Build (per-plugin):
//   zig build-lib src/main.zig -target wasm32-wasi -O ReleaseSmall \
//       -dynamic --import-symbols -rdynamic -fno-entry
//
// Or use a build.zig — see wasm_modules/echo_zig/build.zig.

const std = @import("std");

// === Host imports (module name "lampac") ===

extern "lampac" fn host_log(level: u32, ptr: [*]const u8, len: u32) void;
extern "lampac" fn host_http(req_ptr: [*]const u8, req_len: u32) u64;
extern "lampac" fn host_proxy_url(req_ptr: [*]const u8, req_len: u32) u64;
extern "lampac" fn host_cache_get(key_ptr: [*]const u8, key_len: u32) u64;
extern "lampac" fn host_cache_set(key_ptr: [*]const u8, key_len: u32, val_ptr: [*]const u8, val_len: u32, ttl_sec: u32) void;
extern "lampac" fn host_config() u64;

// === Allocator ===
//
// We use a single fixed-buffer arena for everything the plugin allocates
// during one handle() call. This is simpler than wiring std.heap.GeneralPurpose
// and keeps the .wasm small. The arena is reset between calls by re-creating
// it inside handle(); the host owns memory across calls anyway.

pub var arena_buf: [1024 * 64]u8 = undefined;
pub var arena_fba: std.heap.FixedBufferAllocator = std.heap.FixedBufferAllocator.init(&arena_buf);
pub fn arena() std.mem.Allocator {
    return arena_fba.allocator();
}

// alloc(size) is the host-facing allocator. It must be exported by the
// plugin's main.zig as a wrapper around this:
//
//   export fn alloc(size: u32) [*]u8 { return lampac.alloc(size); }
//
// We bump-allocate from arena_buf — the host writes into the returned
// pointer and is responsible for not re-using stale ones.
pub fn alloc(size: u32) [*]u8 {
    // OOM here is unrecoverable from the host's perspective — bump arena
    // exhaustion means the plugin tried to allocate more than 64 KiB in
    // one handle() call. Most plugins shouldn't ever hit this; if you do,
    // raise LAMPAC_ARENA_SIZE in lampac.zig.
    const slice = arena().alloc(u8, size) catch @panic("lampac arena OOM");
    return slice.ptr;
}

// === Packing helpers ===

pub inline fn pack(ptr: [*]const u8, len: u32) u64 {
    return (@as(u64, @intFromPtr(ptr)) << 32) | @as(u64, len);
}

pub inline fn unpack(packed_val: u64) struct { ptr: [*]u8, len: u32 } {
    return .{
        .ptr = @ptrFromInt(@as(usize, @intCast(packed_val >> 32))),
        .len = @as(u32, @intCast(packed_val & 0xFFFFFFFF)),
    };
}

// === Logging ===

pub const Level = enum(u32) { debug = 0, info = 1, warn = 2, err = 3 };

pub fn log(level: Level, msg: []const u8) void {
    if (msg.len == 0) return;
    host_log(@intFromEnum(level), msg.ptr, @intCast(msg.len));
}

pub fn debug(msg: []const u8) void { log(.debug, msg); }
pub fn info(msg: []const u8) void { log(.info, msg); }
pub fn warn(msg: []const u8) void { log(.warn, msg); }
pub fn err(msg: []const u8) void { log(.err, msg); }

// === Response ===

// writeResponse copies `bytes` into a freshly allocated buffer (so the host
// can safely read it after handle() returns) and packs (ptr, len) into u64.
pub fn writeResponse(bytes: []const u8) u64 {
    if (bytes.len == 0) return 0;
    const buf = arena().alloc(u8, bytes.len) catch return 0;
    @memcpy(buf, bytes);
    return pack(buf.ptr, @intCast(buf.len));
}

// === HTTP ===

pub const HTTPRequest = struct {
    method: []const u8 = "GET",
    url: []const u8,
    body: []const u8 = "",
    timeout_ms: u32 = 0,
};

pub const HTTPResponse = struct {
    status: u32,
    body: []const u8,
    err: []const u8,
};

// http() builds the JSON request, calls host_http, parses the response.
// Body is base64-decoded into the arena.
pub fn http(req: HTTPRequest) HTTPResponse {
    var fbs = std.ArrayList(u8).init(arena());
    defer fbs.deinit();
    fbs.appendSlice("{\"method\":") catch return errResp("oom");
    appendJSONString(&fbs, req.method);
    fbs.appendSlice(",\"url\":") catch return errResp("oom");
    appendJSONString(&fbs, req.url);
    if (req.timeout_ms > 0) {
        fbs.writer().print(",\"timeout_ms\":{}", .{req.timeout_ms}) catch return errResp("oom");
    }
    if (req.body.len > 0) {
        fbs.appendSlice(",\"body\":") catch return errResp("oom");
        appendJSONString(&fbs, req.body);
    }
    fbs.append('}') catch return errResp("oom");

    const packed_val = host_http(fbs.items.ptr, @intCast(fbs.items.len));
    if (packed_val == 0) return errResp("host returned no data");
    const u = unpack(packed_val);
    const resp_text = u.ptr[0..u.len];
    return parseHTTPResponse(resp_text);
}

fn parseHTTPResponse(json: []const u8) HTTPResponse {
    const status = parseTopLevelInt(json, "status");
    const e = parseTopLevelString(json, "error");
    const b64 = parseTopLevelString(json, "body_b64");
    var body: []const u8 = "";
    if (b64.len > 0) {
        body = base64Decode(b64) catch "";
    }
    return .{ .status = @intCast(status), .body = body, .err = e };
}

fn errResp(reason: []const u8) HTTPResponse {
    return .{ .status = 0, .body = "", .err = reason };
}

// === Proxy URL ===

pub fn proxyURL(uri: []const u8, plugin: []const u8) []const u8 {
    var fbs = std.ArrayList(u8).init(arena());
    defer fbs.deinit();
    fbs.appendSlice("{\"uri\":") catch return uri;
    appendJSONString(&fbs, uri);
    if (plugin.len > 0) {
        fbs.appendSlice(",\"plugin\":") catch return uri;
        appendJSONString(&fbs, plugin);
    }
    fbs.append('}') catch return uri;

    const packed_val = host_proxy_url(fbs.items.ptr, @intCast(fbs.items.len));
    if (packed_val == 0) return uri;
    const u = unpack(packed_val);
    const resp_text = u.ptr[0..u.len];
    const url = parseTopLevelString(resp_text, "url");
    if (url.len == 0) return uri;
    return url;
}

// === Cache ===

pub fn cacheGet(key: []const u8) ?[]const u8 {
    const packed_val = host_cache_get(key.ptr, @intCast(key.len));
    if (packed_val == 0) return null;
    const u = unpack(packed_val);
    return u.ptr[0..u.len];
}

pub fn cacheSet(key: []const u8, value: []const u8, ttl_sec: u32) void {
    host_cache_set(key.ptr, @intCast(key.len), value.ptr, @intCast(value.len), ttl_sec);
}

// === Config ===

pub fn config() []const u8 {
    const packed_val = host_config();
    if (packed_val == 0) return "{}";
    const u = unpack(packed_val);
    return u.ptr[0..u.len];
}

// === Tiny JSON helpers ===

pub fn appendJSONString(buf: *std.ArrayList(u8), s: []const u8) void {
    buf.append('"') catch return;
    for (s) |c| {
        switch (c) {
            '"' => buf.appendSlice("\\\"") catch return,
            '\\' => buf.appendSlice("\\\\") catch return,
            '\n' => buf.appendSlice("\\n") catch return,
            '\r' => buf.appendSlice("\\r") catch return,
            '\t' => buf.appendSlice("\\t") catch return,
            else => {
                if (c < 0x20) {
                    buf.writer().print("\\u{x:0>4}", .{c}) catch return;
                } else {
                    buf.append(c) catch return;
                }
            },
        }
    }
    buf.append('"') catch return;
}

// parseTopLevelString finds the value of "key": "..." at the top level of
// a flat JSON object. Doesn't recurse and doesn't unescape.
pub fn parseTopLevelString(json: []const u8, key: []const u8) []const u8 {
    var needle_buf: [128]u8 = undefined;
    if (key.len + 4 > needle_buf.len) return "";
    var fbs = std.io.fixedBufferStream(&needle_buf);
    fbs.writer().print("\"{s}\":\"", .{key}) catch return "";
    const needle = fbs.getWritten();
    const idx = std.mem.indexOf(u8, json, needle) orelse return "";
    const start = idx + needle.len;
    var i = start;
    while (i < json.len) : (i += 1) {
        if (json[i] == '\\') { i += 1; continue; }
        if (json[i] == '"') return json[start..i];
    }
    return "";
}

pub fn parseTopLevelInt(json: []const u8, key: []const u8) i64 {
    var needle_buf: [128]u8 = undefined;
    if (key.len + 3 > needle_buf.len) return 0;
    var fbs = std.io.fixedBufferStream(&needle_buf);
    fbs.writer().print("\"{s}\":", .{key}) catch return 0;
    const needle = fbs.getWritten();
    const idx = std.mem.indexOf(u8, json, needle) orelse return 0;
    var start = idx + needle.len;
    while (start < json.len and json[start] == ' ') : (start += 1) {}
    var end = start;
    while (end < json.len) : (end += 1) {
        const c = json[end];
        if ((c >= '0' and c <= '9') or c == '-') continue;
        break;
    }
    return std.fmt.parseInt(i64, json[start..end], 10) catch 0;
}

// === Base64 decode (host returns body_b64 base64) ===

const B64_PAD: u8 = '=';

pub fn base64Decode(s: []const u8) ![]const u8 {
    // Strip whitespace and padding.
    var clean = std.ArrayList(u8).init(arena());
    defer clean.deinit();
    for (s) |c| {
        if (c == B64_PAD or c == '\n' or c == '\r' or c == ' ') continue;
        try clean.append(c);
    }
    const out_len: usize = (clean.items.len * 3) / 4;
    const out = try arena().alloc(u8, out_len);
    var oi: usize = 0;
    var i: usize = 0;
    while (i + 3 < clean.items.len + 4) : (i += 4) {
        const c0: u8 = if (i < clean.items.len) b64idx(clean.items[i]) else 0;
        const c1: u8 = if (i + 1 < clean.items.len) b64idx(clean.items[i + 1]) else 0;
        const c2: u8 = if (i + 2 < clean.items.len) b64idx(clean.items[i + 2]) else 0;
        const c3: u8 = if (i + 3 < clean.items.len) b64idx(clean.items[i + 3]) else 0;
        if (oi < out_len) { out[oi] = (c0 << 2) | (c1 >> 4); oi += 1; }
        if (oi < out_len) { out[oi] = (c1 << 4) | (c2 >> 2); oi += 1; }
        if (oi < out_len) { out[oi] = (c2 << 6) | c3; oi += 1; }
    }
    return out[0..oi];
}

fn b64idx(c: u8) u8 {
    return switch (c) {
        'A'...'Z' => c - 'A',
        'a'...'z' => c - 'a' + 26,
        '0'...'9' => c - '0' + 52,
        '+' => 62,
        '/' => 63,
        else => 0,
    };
}

// queryGet picks one value out of an Invocation JSON without parsing the
// full structure. Cheap-and-cheerful; for richer access, parse with std.json.
pub fn queryGet(inv_raw: []const u8, name: []const u8) []const u8 {
    var needle_buf: [128]u8 = undefined;
    var fbs = std.io.fixedBufferStream(&needle_buf);
    fbs.writer().print("\"{s}\":\"", .{name}) catch return "";
    return parseTopLevelStringFromOffset(inv_raw, fbs.getWritten(), "\"query\":");
}

fn parseTopLevelStringFromOffset(json: []const u8, needle: []const u8, after: []const u8) []const u8 {
    const after_idx = std.mem.indexOf(u8, json, after) orelse return "";
    const sub = json[after_idx..];
    const idx = std.mem.indexOf(u8, sub, needle) orelse return "";
    const start = after_idx + idx + needle.len;
    var i = start;
    while (i < json.len) : (i += 1) {
        if (json[i] == '\\') { i += 1; continue; }
        if (json[i] == '"') return json[start..i];
    }
    return "";
}
