package httpapi

import "strings"

// drochubRouteKey returns the DynamicRouteRegistry key used to expose a drochub
// source subprocess. Used by the composition root (server.go) to wire the sisi
// DrochubRouteKey dep. The admin drochub handler that also needs it moved to
// internal/adminhttp and carries its own copy of this pure helper.
func drochubRouteKey(name string) string {
	return "drochub:" + strings.TrimSpace(name)
}
