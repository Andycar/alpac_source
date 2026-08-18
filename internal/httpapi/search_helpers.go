package httpapi

// Shared search-normalization helpers. The canonical originals lived in
// kinobase.go until it moved to internal/litesrc (which carries its own
// copies in helpers.go); these stay for the ~30 in-package source files.

// filmixCDNUserAgent mirrors the litesrc original (moved with filmix.go);
// used by capi.go until the capi core is untangled.
const filmixCDNUserAgent = "Dalvik/2.1.0 (Linux; U; Android 11; Xiaomi Build/RP1A.200720.011)"
