//! Client-side host bridge — used by plugins that run inside Lampa via
//! wasm_loader.js. Same shape as the TinyGo client SDK; imports come from
//! the "lampac_client" module instead of "lampac".

use crate::{pack, read_bytes, unpack};

#[link(wasm_import_module = "lampac_client")]
extern "C" {
    fn host_log(level: u32, ptr: u32, len: u32);
    fn host_storage_get(key_ptr: u32, key_len: u32) -> u64;
    fn host_storage_set(key_ptr: u32, key_len: u32, val_ptr: u32, val_len: u32);
    fn host_listener_emit(event_ptr: u32, event_len: u32, data_ptr: u32, data_len: u32);
    fn host_noty(msg_ptr: u32, msg_len: u32);
    fn host_activity_push(json_ptr: u32, json_len: u32);
}

pub fn debug(s: &str) { log_at(0, s) }
pub fn info(s: &str)  { log_at(1, s) }
pub fn warn(s: &str)  { log_at(2, s) }
pub fn error(s: &str) { log_at(3, s) }

fn log_at(level: u32, s: &str) {
    if s.is_empty() { return }
    unsafe { host_log(level, s.as_ptr() as u32, s.len() as u32); }
}

pub fn storage_get(key: &str) -> String {
    if key.is_empty() { return String::new(); }
    let packed = unsafe { host_storage_get(key.as_ptr() as u32, key.len() as u32) };
    if packed == 0 { return String::new(); }
    let (ptr, len) = unpack(packed);
    String::from_utf8(unsafe { read_bytes(ptr, len) }).unwrap_or_default()
}

pub fn storage_set(key: &str, value: &str) {
    if key.is_empty() { return; }
    let (vp, vl) = if value.is_empty() { (0u32, 0u32) } else { (value.as_ptr() as u32, value.len() as u32) };
    unsafe { host_storage_set(key.as_ptr() as u32, key.len() as u32, vp, vl); }
}

pub fn emit_event(event: &str, json_data: &str) {
    if event.is_empty() { return; }
    let (dp, dl) = if json_data.is_empty() { (0u32, 0u32) } else { (json_data.as_ptr() as u32, json_data.len() as u32) };
    unsafe { host_listener_emit(event.as_ptr() as u32, event.len() as u32, dp, dl); }
}

pub fn noty(msg: &str) {
    if msg.is_empty() { return; }
    unsafe { host_noty(msg.as_ptr() as u32, msg.len() as u32); }
}

pub fn activity_push(activity_json: &str) {
    if activity_json.is_empty() { return; }
    unsafe { host_activity_push(activity_json.as_ptr() as u32, activity_json.len() as u32); }
}

// Re-export pack/unpack via the client module too for symmetry.
pub use crate::{pack as _pack, unpack as _unpack};

// Used by SDK macros so callers don't import `pack` from the root.
pub fn _publish(bytes: Vec<u8>) -> u64 {
    if bytes.is_empty() { return 0; }
    let len = bytes.len() as u32;
    let ptr = crate::alloc(len);
    unsafe { std::ptr::copy_nonoverlapping(bytes.as_ptr(), ptr as *mut u8, len as usize); }
    pack(ptr, len)
}
