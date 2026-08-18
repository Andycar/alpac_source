// Client-side host bridge for plugins running inside Lampa via the
// wasm_loader.js runtime. Mirrors wasm_sdk/tinygo/lampac.go, but the import
// module is "lampac_client" and the surface is Lampa-specific (Storage,
// Activity, Listener, Noty) instead of HTTP/proxy.
//
// Use:
//
//   //export on_load
//   func onLoad() { lampac.NotyClient("plugin loaded") }
//
//   //export on_event
//   func onEvent(ptr, length uint32) { … }
package lampac

import (
	"unsafe"
)

//go:wasmimport lampac_client host_log
func clientHostLog(level uint32, ptr, length uint32)

//go:wasmimport lampac_client host_storage_get
func clientHostStorageGet(keyPtr, keyLen uint32) uint64

//go:wasmimport lampac_client host_storage_set
func clientHostStorageSet(keyPtr, keyLen, valPtr, valLen uint32)

//go:wasmimport lampac_client host_listener_emit
func clientHostListenerEmit(eventPtr, eventLen, dataPtr, dataLen uint32)

//go:wasmimport lampac_client host_noty
func clientHostNoty(msgPtr, msgLen uint32)

//go:wasmimport lampac_client host_activity_push
func clientHostActivityPush(jsonPtr, jsonLen uint32)

// === Logging ===

func ClientDebug(s string) { clientLogAt(0, s) }
func ClientInfo(s string)  { clientLogAt(1, s) }
func ClientWarn(s string)  { clientLogAt(2, s) }
func ClientError(s string) { clientLogAt(3, s) }

func clientLogAt(level uint32, s string) {
	if s == "" {
		return
	}
	b := []byte(s)
	clientHostLog(level, uint32(uintptr(unsafe.Pointer(&b[0]))), uint32(len(b)))
}

// === Storage ===

// StorageGet returns the value stored by Lampa.Storage under "wasm_plugin_<id>:key"
// or "" if missing. The host scopes keys per plugin so plugins can't collide.
func StorageGet(key string) string {
	if key == "" {
		return ""
	}
	kb := []byte(key)
	packed := clientHostStorageGet(uint32(uintptr(unsafe.Pointer(&kb[0]))), uint32(len(kb)))
	if packed == 0 {
		return ""
	}
	ptr, length := unpack(packed)
	return string(readBytes(ptr, length))
}

// StorageSet writes a string into the plugin's namespaced Lampa.Storage.
func StorageSet(key, value string) {
	if key == "" {
		return
	}
	kb := []byte(key)
	vb := []byte(value)
	var vPtr, vLen uint32
	if len(vb) > 0 {
		vPtr = uint32(uintptr(unsafe.Pointer(&vb[0])))
		vLen = uint32(len(vb))
	}
	clientHostStorageSet(uint32(uintptr(unsafe.Pointer(&kb[0]))), uint32(len(kb)), vPtr, vLen)
}

// === Listener / Lampa events ===

// EmitEvent pushes a Lampa.Listener.send(event, data) — Lampa plugins react
// to events by calling Lampa.Listener.follow.
func EmitEvent(event, jsonData string) {
	if event == "" {
		return
	}
	eb := []byte(event)
	db := []byte(jsonData)
	var dPtr, dLen uint32
	if len(db) > 0 {
		dPtr = uint32(uintptr(unsafe.Pointer(&db[0])))
		dLen = uint32(len(db))
	}
	clientHostListenerEmit(uint32(uintptr(unsafe.Pointer(&eb[0]))), uint32(len(eb)), dPtr, dLen)
}

// === Toasts and navigation ===

// NotyClient pops a toast via Lampa.Noty.show.
func NotyClient(msg string) {
	if msg == "" {
		return
	}
	b := []byte(msg)
	clientHostNoty(uint32(uintptr(unsafe.Pointer(&b[0]))), uint32(len(b)))
}

// ActivityPush opens a new screen via Lampa.Activity.push. activityJSON should
// match Lampa's activity descriptor (component, title, source, …).
func ActivityPush(activityJSON string) {
	if activityJSON == "" {
		return
	}
	b := []byte(activityJSON)
	clientHostActivityPush(uint32(uintptr(unsafe.Pointer(&b[0]))), uint32(len(b)))
}
