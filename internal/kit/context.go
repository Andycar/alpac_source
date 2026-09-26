package kit

import (
	"context"
	"encoding/json"
	"strings"
)

type contextKey string

const kitCtxKey contextKey = "lampac.kit"

// WithConfig stores the user's kit config in the request context.
func WithConfig(ctx context.Context, cfg map[string]json.RawMessage) context.Context {
	return context.WithValue(ctx, kitCtxKey, cfg)
}

// FromContext retrieves the user's kit config from context.
func FromContext(ctx context.Context) (map[string]json.RawMessage, bool) {
	v, ok := ctx.Value(kitCtxKey).(map[string]json.RawMessage)
	return v, ok && v != nil
}

// balancerSection is a minimal struct for parsing enable/token/cookie from a kit section.
type balancerSection struct {
	Enable bool   `json:"enable"`
	Token  string `json:"token"`
	Cookie string `json:"cookie"`
	Pro    bool   `json:"pro"`
}

func parseSection(ctx context.Context, key string) (*balancerSection, bool) {
	m, ok := FromContext(ctx)
	if !ok {
		return nil, false
	}
	raw, exists := m[key]
	if !exists || len(raw) == 0 {
		return nil, false
	}
	var s balancerSection
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, false
	}
	return &s, true
}

// TokenOverride returns the user's personal token for a balancer if configured and enabled.
func TokenOverride(ctx context.Context, balancerKey string) (string, bool) {
	s, ok := parseSection(ctx, balancerKey)
	if !ok || !s.Enable {
		return "", false
	}
	token := strings.TrimSpace(s.Token)
	if token == "" {
		return "", false
	}
	return token, true
}

// CookieOverride returns the user's personal cookie for a balancer if configured and enabled.
func CookieOverride(ctx context.Context, balancerKey string) (string, bool) {
	s, ok := parseSection(ctx, balancerKey)
	if !ok || !s.Enable {
		return "", false
	}
	cookie := strings.TrimSpace(s.Cookie)
	if cookie == "" {
		return "", false
	}
	return cookie, true
}

// BalancerVisible checks if the user has explicitly configured visibility for a balancer.
// Returns (visible, configured). If configured=false, caller should use the global default.
//
// Two modes inferred from the user's `_balancerVisibility` map:
//
//	whitelist mode  — at least one entry has value=true. The user is opting
//	                  IN to specific balancers; everything else is hidden.
//	blacklist mode  — all entries have value=false. The user is opting OUT
//	                  of specific balancers; everything else uses the default.
//
// The mode is auto-detected; the user just toggles switches in /bkit, and the
// behaviour matches what they visually expect (turning on some = show only
// those; turning off some = hide only those).
func BalancerVisible(ctx context.Context, pluginKey string) (visible bool, configured bool) {
	m, ok := FromContext(ctx)
	if !ok {
		return false, false
	}
	raw, exists := m["_balancerVisibility"]
	if !exists || len(raw) == 0 {
		return false, false
	}
	var vis map[string]bool
	if err := json.Unmarshal(raw, &vis); err != nil {
		return false, false
	}
	pluginKey = strings.ToLower(strings.TrimSpace(pluginKey))
	if v, found := vis[pluginKey]; found {
		return v, true
	}
	// Не в карте. Сначала проверяем, а был ли этот источник вообще, когда
	// пользователь сохранял набор: чего он не видел, того он и не выключал.
	// Новый источник — «не настроен», решает глобальный дефолт (см. catalog.go).
	if !knownAtSave(m, pluginKey) {
		return false, false
	}

	// Not in map. Detect mode: any value=true → whitelist mode → hide.
	for _, v := range vis {
		if v {
			return false, true
		}
	}
	// All entries are false (or map empty) → blacklist mode → use default.
	return false, false
}

// BalancersExplicitlyEnabled returns the set of balancer keys the user has
// explicitly enabled in their kit visibility map. Used by the lite events
// handler to additively include balancers that the user wants but which are
// not in the server's global with_search list.
func BalancersExplicitlyEnabled(ctx context.Context) map[string]struct{} {
	m, ok := FromContext(ctx)
	if !ok {
		return nil
	}
	raw, exists := m["_balancerVisibility"]
	if !exists || len(raw) == 0 {
		return nil
	}
	var vis map[string]bool
	if err := json.Unmarshal(raw, &vis); err != nil {
		return nil
	}
	if len(vis) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(vis))
	for k, v := range vis {
		if !v {
			continue
		}
		out[strings.ToLower(strings.TrimSpace(k))] = struct{}{}
	}
	return out
}

// BalancerExplicitlyHidden reports whether the user set this balancer to false
// in their _balancerVisibility map. Unlike BalancerVisible, a key that is
// ABSENT from the map is NOT treated as hidden — absence means "no opinion",
// not "deny". This is the visibility check for dynamically-registered sources
// (JS modules, custom balancers): such a source may have been installed AFTER
// the user last saved their /bkit preferences, so whitelist-mode auto-hiding
// (any true → hide everything unlisted) would wrongly bury a source the user
// just enabled in the admin panel. Those sources stay visible unless the user
// explicitly turns them off.
func BalancerExplicitlyHidden(ctx context.Context, pluginKey string) bool {
	m, ok := FromContext(ctx)
	if !ok {
		return false
	}
	raw, exists := m["_balancerVisibility"]
	if !exists || len(raw) == 0 {
		return false
	}
	var vis map[string]bool
	if err := json.Unmarshal(raw, &vis); err != nil {
		return false
	}
	v, found := vis[strings.ToLower(strings.TrimSpace(pluginKey))]
	return found && !v
}

// BoolField returns a boolean field from the balancer's kit section.
func BoolField(ctx context.Context, balancerKey, field string) (bool, bool) {
	s, ok := parseSection(ctx, balancerKey)
	if !ok {
		return false, false
	}
	switch field {
	case "pro":
		return s.Pro, true
	case "enable":
		return s.Enable, true
	default:
		return false, false
	}
}
