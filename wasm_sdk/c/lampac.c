// Implementation of the lampac C SDK. See lampac.h for the API contract.

#include "lampac.h"
#include <string.h>
#include <stdint.h>
#include <stdlib.h>
#include <stdio.h>

// memmem is a GNU extension; wasi-libc doesn't ship it. Roll our own —
// O(n*m) is fine for our short JSON responses.
static const void *lampac_memmem(const void *hay, size_t hay_len,
                                  const void *needle, size_t needle_len) {
    if (needle_len == 0) return hay;
    if (needle_len > hay_len) return NULL;
    const unsigned char *h = (const unsigned char *)hay;
    const unsigned char *n = (const unsigned char *)needle;
    for (size_t i = 0; i + needle_len <= hay_len; i++) {
        if (h[i] == n[0] && memcmp(h + i, n, needle_len) == 0) {
            return h + i;
        }
    }
    return NULL;
}

// === Host imports (module name "lampac") ===

__attribute__((import_module("lampac"), import_name("host_log")))
extern void host_log(uint32_t level, const char *ptr, uint32_t len);

__attribute__((import_module("lampac"), import_name("host_http")))
extern uint64_t host_http(const char *req_ptr, uint32_t req_len);

__attribute__((import_module("lampac"), import_name("host_proxy_url")))
extern uint64_t host_proxy_url(const char *req_ptr, uint32_t req_len);

__attribute__((import_module("lampac"), import_name("host_cache_get")))
extern uint64_t host_cache_get(const char *key_ptr, uint32_t key_len);

__attribute__((import_module("lampac"), import_name("host_cache_set")))
extern void host_cache_set(const char *key_ptr, uint32_t key_len,
                            const uint8_t *val_ptr, uint32_t val_len,
                            uint32_t ttl_sec);

__attribute__((import_module("lampac"), import_name("host_config")))
extern uint64_t host_config(void);

// === Bump arena ===
//
// Plugins that need a "real" allocator can fall back to malloc(); we keep
// our own arena so the host's `alloc()` is fast and the plugin doesn't pull
// in libc's heap manager just to copy bytes.

#define LAMPAC_ARENA_SIZE (1024 * 64)
static uint8_t lampac_arena[LAMPAC_ARENA_SIZE];
static uint32_t lampac_arena_pos = 0;

void lampac_arena_reset(void) { lampac_arena_pos = 0; }

void *lampac_alloc(uint32_t size) {
    // 8-byte alignment so callers can store packed structs without trapping.
    uint32_t pos = (lampac_arena_pos + 7) & ~7u;
    if (pos + size > LAMPAC_ARENA_SIZE) {
        // Out of arena — fall back to libc malloc. Caller frees if needed,
        // but most plugin patterns are arena-only.
        return malloc(size);
    }
    void *ptr = &lampac_arena[pos];
    lampac_arena_pos = pos + size;
    return ptr;
}

// === Pack helpers ===

static inline uint64_t pack(const void *ptr, uint32_t len) {
    return ((uint64_t)(uintptr_t)ptr << 32) | (uint64_t)len;
}

static inline uint32_t unpack_ptr(uint64_t v) { return (uint32_t)(v >> 32); }
static inline uint32_t unpack_len(uint64_t v) { return (uint32_t)(v & 0xFFFFFFFFu); }

// === Logging ===

void lampac_log(lampac_log_level level, const char *msg, uint32_t len) {
    if (msg == NULL || len == 0) return;
    host_log((uint32_t)level, msg, len);
}

void lampac_debug(const char *msg) { if (msg) lampac_log(LAMPAC_DEBUG, msg, (uint32_t)strlen(msg)); }
void lampac_info(const char *msg)  { if (msg) lampac_log(LAMPAC_INFO,  msg, (uint32_t)strlen(msg)); }
void lampac_warn(const char *msg)  { if (msg) lampac_log(LAMPAC_WARN,  msg, (uint32_t)strlen(msg)); }
void lampac_error(const char *msg) { if (msg) lampac_log(LAMPAC_ERROR, msg, (uint32_t)strlen(msg)); }

// === Response ===

uint64_t lampac_write_response(const char *bytes, uint32_t len) {
    if (bytes == NULL || len == 0) return 0;
    void *buf = lampac_alloc(len);
    if (!buf) return 0;
    memcpy(buf, bytes, len);
    return pack(buf, len);
}

// === Tiny JSON helpers ===

static void append_str(char **dst, uint32_t *cap, uint32_t *pos, const char *s, uint32_t n) {
    if (*pos + n + 1 >= *cap) {
        uint32_t newcap = (*cap == 0 ? 256 : *cap) * 2;
        while (*pos + n + 1 >= newcap) newcap *= 2;
        char *nbuf = (char *)lampac_alloc(newcap);
        if (*dst) memcpy(nbuf, *dst, *pos);
        *dst = nbuf;
        *cap = newcap;
    }
    memcpy(*dst + *pos, s, n);
    *pos += n;
    (*dst)[*pos] = 0;
}

static void append_json_string(char **dst, uint32_t *cap, uint32_t *pos, const char *s) {
    if (s == NULL) { append_str(dst, cap, pos, "null", 4); return; }
    append_str(dst, cap, pos, "\"", 1);
    for (const unsigned char *p = (const unsigned char *)s; *p; p++) {
        char esc[8];
        switch (*p) {
            case '"':  append_str(dst, cap, pos, "\\\"", 2); break;
            case '\\': append_str(dst, cap, pos, "\\\\", 2); break;
            case '\n': append_str(dst, cap, pos, "\\n", 2); break;
            case '\r': append_str(dst, cap, pos, "\\r", 2); break;
            case '\t': append_str(dst, cap, pos, "\\t", 2); break;
            default:
                if (*p < 0x20) {
                    int n = snprintf(esc, sizeof(esc), "\\u%04x", *p);
                    append_str(dst, cap, pos, esc, (uint32_t)n);
                } else {
                    char c = (char)*p;
                    append_str(dst, cap, pos, &c, 1);
                }
        }
    }
    append_str(dst, cap, pos, "\"", 1);
}

// Find the value of a top-level "key":"..." in a flat JSON object.
const char *lampac_json_string(const char *json, uint32_t json_len, const char *key, uint32_t *out_len) {
    if (out_len) *out_len = 0;
    if (!json || !key) return NULL;
    char needle[256];
    int n = snprintf(needle, sizeof(needle), "\"%s\":\"", key);
    if (n <= 0 || (uint32_t)n >= sizeof(needle)) return NULL;
    const char *p = (const char *)lampac_memmem(json, json_len, needle, (size_t)n);
    if (!p) return NULL;
    p += n;
    const char *end = p;
    while ((uint32_t)(end - json) < json_len) {
        if (*end == '\\') { end += 2; continue; }
        if (*end == '"') break;
        end++;
    }
    if (out_len) *out_len = (uint32_t)(end - p);
    return p;
}

// === HTTP ===

lampac_http_response lampac_http(lampac_http_request req) {
    lampac_http_response out = {0};
    out.err = "";

    char *buf = NULL;
    uint32_t cap = 0, pos = 0;
    append_str(&buf, &cap, &pos, "{\"method\":", 10);
    append_json_string(&buf, &cap, &pos, req.method ? req.method : "GET");
    append_str(&buf, &cap, &pos, ",\"url\":", 7);
    append_json_string(&buf, &cap, &pos, req.url);
    if (req.timeout_ms > 0) {
        char tmp[32];
        int n = snprintf(tmp, sizeof(tmp), ",\"timeout_ms\":%u", req.timeout_ms);
        append_str(&buf, &cap, &pos, tmp, (uint32_t)n);
    }
    if (req.body && *req.body) {
        append_str(&buf, &cap, &pos, ",\"body\":", 8);
        append_json_string(&buf, &cap, &pos, req.body);
    }
    append_str(&buf, &cap, &pos, "}", 1);

    uint64_t packed = host_http(buf, pos);
    if (packed == 0) { out.err = "host returned no data"; return out; }

    const char *resp = (const char *)(uintptr_t)unpack_ptr(packed);
    uint32_t resp_len = unpack_len(packed);

    // status
    {
        const char *needle = "\"status\":";
        const char *p = (const char *)lampac_memmem(resp, resp_len, needle, strlen(needle));
        if (p) {
            p += strlen(needle);
            out.status = atoi(p);
        }
    }
    // error
    {
        uint32_t l = 0;
        const char *e = lampac_json_string(resp, resp_len, "error", &l);
        if (e && l > 0) {
            char *copy = (char *)lampac_alloc(l + 1);
            memcpy(copy, e, l); copy[l] = 0;
            out.err = copy;
        } else {
            out.err = "";
        }
    }
    // body_b64 — base64 decode
    {
        uint32_t l = 0;
        const char *b = lampac_json_string(resp, resp_len, "body_b64", &l);
        if (b && l > 0) {
            // base64 decode into arena
            uint32_t outcap = (l * 3) / 4 + 1;
            uint8_t *outbuf = (uint8_t *)lampac_alloc(outcap);
            uint32_t oi = 0;
            uint32_t bits = 0; int8_t shift = -8;
            for (uint32_t i = 0; i < l; i++) {
                char c = b[i];
                int v;
                if (c >= 'A' && c <= 'Z') v = c - 'A';
                else if (c >= 'a' && c <= 'z') v = c - 'a' + 26;
                else if (c >= '0' && c <= '9') v = c - '0' + 52;
                else if (c == '+') v = 62;
                else if (c == '/') v = 63;
                else continue;
                bits = (bits << 6) | (uint32_t)v;
                shift += 6;
                if (shift >= 0) {
                    outbuf[oi++] = (uint8_t)((bits >> shift) & 0xff);
                    shift -= 8;
                }
            }
            out.body = (const char *)outbuf;
            out.body_len = oi;
        }
    }
    return out;
}

// === Proxy URL ===

const char *lampac_proxy_url(const char *uri, const char *plugin) {
    if (!uri || !*uri) return uri;
    char *buf = NULL;
    uint32_t cap = 0, pos = 0;
    append_str(&buf, &cap, &pos, "{\"uri\":", 7);
    append_json_string(&buf, &cap, &pos, uri);
    if (plugin && *plugin) {
        append_str(&buf, &cap, &pos, ",\"plugin\":", 10);
        append_json_string(&buf, &cap, &pos, plugin);
    }
    append_str(&buf, &cap, &pos, "}", 1);

    uint64_t packed = host_proxy_url(buf, pos);
    if (packed == 0) return uri;
    const char *resp = (const char *)(uintptr_t)unpack_ptr(packed);
    uint32_t resp_len = unpack_len(packed);
    uint32_t out_len = 0;
    const char *url = lampac_json_string(resp, resp_len, "url", &out_len);
    if (!url || out_len == 0) return uri;
    char *copy = (char *)lampac_alloc(out_len + 1);
    memcpy(copy, url, out_len); copy[out_len] = 0;
    return copy;
}

// === Cache ===

const uint8_t *lampac_cache_get(const char *key, uint32_t key_len, uint32_t *out_len) {
    if (out_len) *out_len = 0;
    uint64_t packed = host_cache_get(key, key_len);
    if (packed == 0) return NULL;
    uint32_t ptr_int = unpack_ptr(packed);
    uint32_t len = unpack_len(packed);
    if (len == 0) return NULL;
    if (out_len) *out_len = len;
    return (const uint8_t *)(uintptr_t)ptr_int;
}

void lampac_cache_set(const char *key, uint32_t key_len,
                      const uint8_t *value, uint32_t value_len, uint32_t ttl_sec) {
    host_cache_set(key, key_len, value, value_len, ttl_sec);
}

// === Config ===

const char *lampac_config(uint32_t *out_len) {
    uint64_t packed = host_config();
    if (out_len) *out_len = 0;
    if (packed == 0) return "{}";
    uint32_t ptr_int = unpack_ptr(packed);
    uint32_t len = unpack_len(packed);
    if (out_len) *out_len = len;
    return (const char *)(uintptr_t)ptr_int;
}

// === Invocation ===

lampac_invocation lampac_read_invocation(uint32_t ptr, uint32_t length) {
    lampac_invocation inv = {0};
    inv.raw = (const char *)(uintptr_t)ptr;
    inv.raw_len = length;
    inv.path        = lampac_json_string(inv.raw, inv.raw_len, "path",       &inv.path_len);
    inv.request_ip  = lampac_json_string(inv.raw, inv.raw_len, "requestIP",  &inv.request_ip_len);
    // config is a nested object — find its substring as-is.
    {
        const char *needle = "\"config\":";
        const char *p = (const char *)lampac_memmem(inv.raw, inv.raw_len, needle, strlen(needle));
        if (p) {
            p += strlen(needle);
            if (*p == '{') {
                int depth = 1;
                const char *q = p + 1;
                while (q < inv.raw + inv.raw_len && depth > 0) {
                    if (*q == '{') depth++;
                    else if (*q == '}') depth--;
                    q++;
                }
                inv.config_json = p;
                inv.config_len = (uint32_t)(q - p);
            }
        }
    }
    return inv;
}

const char *lampac_query_get(const lampac_invocation *inv, const char *name, uint32_t *out_len) {
    if (out_len) *out_len = 0;
    if (!inv || !inv->raw) return NULL;
    // Restrict scan to the "query":{...} section if present.
    const char *q = (const char *)lampac_memmem(inv->raw, inv->raw_len, "\"query\":", 8);
    const char *base = q ? q : inv->raw;
    uint32_t base_len = q ? (inv->raw_len - (uint32_t)(q - inv->raw)) : inv->raw_len;
    return lampac_json_string(base, base_len, name, out_len);
}
