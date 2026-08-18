// C SDK for lampac-go WASM plugins.
//
// Single-header style: declarations live here, lampac.c has the bodies.
// Plugin authors:
//
//   #include "lampac.h"
//
//   uint64_t handle(uint32_t ptr, uint32_t length) {
//       lampac_invocation inv = lampac_read_invocation(ptr, length);
//       lampac_info("hello from C");
//       return lampac_write_response("{\"type\":\"movie\",\"data\":[]}", 27);
//   }
//
// Build:
//
//   $WASI_SDK/bin/clang \
//     --target=wasm32-wasi -mexec-model=reactor \
//     -O2 -nostartfiles \
//     -Wl,--no-entry -Wl,--export=alloc -Wl,--export=handle -Wl,--export=abi_version \
//     -o plugin.wasm \
//     ../../wasm_sdk/c/lampac.c main.c
//
// `-mexec-model=reactor` is the magic that makes _initialize the entry
// point instead of _start; the host calls _initialize once after instantiate.

#ifndef LAMPAC_SDK_H
#define LAMPAC_SDK_H

#include <stdint.h>
#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

/* === Allocator (must be exported by the plugin's main.c) === */

void *lampac_alloc(uint32_t size);

/* === Logging === */

typedef enum {
    LAMPAC_DEBUG = 0,
    LAMPAC_INFO  = 1,
    LAMPAC_WARN  = 2,
    LAMPAC_ERROR = 3,
} lampac_log_level;

void lampac_log(lampac_log_level level, const char *msg, uint32_t len);

/* Convenience wrappers — pass a NUL-terminated string. */
void lampac_debug(const char *msg);
void lampac_info(const char *msg);
void lampac_warn(const char *msg);
void lampac_error(const char *msg);

/* === Response === */

/* Pack (ptr, len) into the u64 the host expects from handle(). The bytes
 * are copied into a fresh `lampac_alloc`-ed region so the caller's stack
 * buffers can go out of scope safely. */
uint64_t lampac_write_response(const char *bytes, uint32_t len);

/* === HTTP === */

typedef struct {
    const char *method;       /* "GET", "POST", … (NUL-terminated; NULL → "GET") */
    const char *url;          /* required, NUL-terminated */
    const char *body;         /* optional, NUL-terminated */
    uint32_t timeout_ms;      /* 0 = host default (15 s) */
} lampac_http_request;

typedef struct {
    int32_t status;           /* HTTP status; 0 if request failed pre-flight */
    const char *body;         /* base64-decoded body (in arena) */
    uint32_t body_len;
    const char *err;          /* error message; "" on success */
} lampac_http_response;

/* http() does a synchronous request. Body in resp lives in the same arena
 * as everything else allocated during this handle() call. */
lampac_http_response lampac_http(lampac_http_request req);

/* === Proxy URL === */

/* Returns a /proxy/-signed URL (or the original `uri` on failure / no proxy
 * setup). Pointer is valid for the lifetime of the current handle() call. */
const char *lampac_proxy_url(const char *uri, const char *plugin);

/* === Cache === */

/* Returns a pointer to a fresh copy of the cached value (out_len populated)
 * or NULL on miss. Empty value is treated as miss. */
const uint8_t *lampac_cache_get(const char *key, uint32_t key_len, uint32_t *out_len);
void lampac_cache_set(const char *key, uint32_t key_len,
                      const uint8_t *value, uint32_t value_len, uint32_t ttl_sec);

/* === Config === */

/* Returns JSON-encoded config snapshot. Pointer valid until next handle(). */
const char *lampac_config(uint32_t *out_len);

/* === Invocation accessors === */

typedef struct {
    const char *raw;          /* full invocation JSON */
    uint32_t raw_len;
    const char *path;         /* substring inside raw — NOT NUL-terminated */
    uint32_t path_len;
    const char *request_ip;
    uint32_t request_ip_len;
    const char *config_json;
    uint32_t config_len;
} lampac_invocation;

lampac_invocation lampac_read_invocation(uint32_t ptr, uint32_t length);

/* Pull one query parameter out of inv.raw. Returns NULL on miss. */
const char *lampac_query_get(const lampac_invocation *inv, const char *name, uint32_t *out_len);

/* Pull one top-level string field from a flat JSON object. The host's
 * config() returns flat objects (string scalars only at the top level), so
 * this is enough for most plugins. */
const char *lampac_json_string(const char *json, uint32_t json_len, const char *key, uint32_t *out_len);

/* === Memory arena === */

/* Reset the per-call arena to zero — call once at the top of handle() if
 * you want to reuse memory across many invocations. The host calls
 * lampac_alloc() to write the next invocation; not resetting just costs
 * memory, doesn't break correctness. */
void lampac_arena_reset(void);

#ifdef __cplusplus
}
#endif

#endif /* LAMPAC_SDK_H */
