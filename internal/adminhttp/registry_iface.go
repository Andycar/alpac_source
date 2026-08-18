package adminhttp

import (
	"net/http"
	"regexp"
	"strings"
	"time"
)

// DynRoutes is the subset of the host's *DynamicRouteRegistry that moved admin
// handlers use. The concrete registry satisfies it, so the composition root
// passes it straight in — this keeps the registry type in httpapi (no cycle)
// while admin handlers depend only on this interface.
type DynRoutes interface {
	Register(name, targetAddr string)
	RegisterHandler(name string, h http.Handler)
	Unregister(name string)
	Lookup(name string) (http.Handler, bool)
	Names() []string
}

// reCustBalName / isValidCustBalName are copied verbatim from the host
// (admin_constructor.go) — a pure custom-balancer-name validator with no host
// coupling. The host keeps its own copy for the files that stay behind.
var reCustBalName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func isValidCustBalName(name string) bool {
	return reCustBalName.MatchString(strings.TrimSpace(name))
}

// WafState is the subset of the host's *wafState the admin WAF handler uses.
// The concrete *wafState satisfies it (Stats/AddManualBan/RemoveManualBan are
// its own exported methods; CfgAny/CollectManualBansAny are exported `any`
// getters added to the host type) — so the internal wafConfig/manualBanEntry
// types need not be relocated. import "time" for the AddManualBan signature.
type WafState interface {
	CfgAny() any
	Stats() map[string]any
	CollectManualBansAny() any
	AddManualBan(ip, reason string, ttl time.Duration) error
	RemoveManualBan(ip string) error
}

// CustomPlugins is the subset of the host's *CustomPluginRegistry the admin
// custom-plugins handler uses. ListAny() returns the plugin list as `any`
// (serialize-only), so the internal CustomPlugin type need not be relocated.
type CustomPlugins interface {
	ListAny() any
	Register(name, displayName string, content []byte) error
	Unregister(name string)
	SetEnabled(name string, enabled bool)
	SetAutoload(name string, autoload bool)
	UpdateMeta(name string, public bool, descr, author string)
	SaveImage(name string, data []byte, ext string) error
	ImagePath(filename string) string
	DeleteImage(name string)
}

// CommunityCron is the subset of the host's *CommunityUpdateCron the admin
// community-plugins handler uses — all external/primitive returns. The
// catalog-touching CachedCatalog stays host-side inside the injected
// community* closures, so CatalogEntry/CatalogResponse need not be relocated.
type CommunityCron interface {
	PendingCount() int
	LastCheck() time.Time
	LastError() string
	CheckNow() (int, error)
	AutoUpdateNow() (int, error)
}

// ClusterSecrets mirrors the host's ensureClusterSecretsResult so the moved
// admin_cluster handler keeps its `secrets.APIKey`/`.APIKeyGenerated` field
// accesses. Exported so the host closure can build it in the injected
// EnsureClusterSecrets (field-flatten via a mirror struct — avoids relocating the
// internal result type).
type ClusterSecrets struct {
	APIKeyGenerated       bool
	SharedSecretGenerated bool
	APIKey                string
	SharedSecret          string
}
