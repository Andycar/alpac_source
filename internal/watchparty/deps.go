// Package watchparty is the "virtual cinema" feature: the /webplayer WS relay
// hub (watchparty_api) plus the /capi/room/* room-metadata endpoints on top of
// it (watchparty_rooms). Extracted from httpapi.
package watchparty

import (
	"strconv"

	"lampac-go/internal/config"
)

// Deps are the host-side hooks. LiveConfig is the hot-reload accessor (room
// endpoints read the live bot name).
type Deps struct {
	LiveConfig func(fallback config.Config) config.Config
}

var deps = Deps{LiveConfig: func(c config.Config) config.Config { return c }}

// SetDeps wires the host seam. Call once before serving /capi/room/* routes.
func SetDeps(d Deps) {
	if d.LiveConfig != nil {
		deps = d
	}
}

// capiInt mirrors httpapi/capi_profiles.go: lenient string→int.
func capiInt(s string) int { n, _ := strconv.Atoi(s); return n }
