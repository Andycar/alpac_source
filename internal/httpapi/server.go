// Package httpapi is the HTTP front-end of lampac-go: chi router, all
// /lite/<balancer> handlers, the admin panel, the kit-config API, TG auth,
// the WebSocket hub, the cluster fan-out, and the dynamic plugin registry.
//
// Sub-systems:
//
//   - /lite/events + /lite/<balancer>: the search / playback resolver per
//     online source (see lite_events.go, kinotochka.go, collaps.go, mirage.go,
//     etc.). 30+ source-specific files all share writeCheckSearchResponse +
//     proxylink + checksearch cache.
//   - admin_*.go: Telegram-auth-gated admin panel — telemetry dashboard,
//     cluster control, plugin store, config editor, broadcast tools.
//   - WebSocket: nws.go (Lampa native WS hub for sync v2, bookmarks, weblog,
//     Alice push) + mirage_ws.go (CDN edge_hash refresh).
//   - Telemetry + healthcheck: balancer_fetch.go + healthcheck.go (every
//     outbound balancer probe recorded in balancerstats.Manager rings; admin
//     dashboard renders them; alert engine pings TG on degradation).
//   - Security: trusted_proxies.go (X-Forwarded-For allowlist), middleware.go
//     (panic recovery, request-ID, CORS), server_routes_system.go
//     (/metrics + pprof guard).
//
// Globals:
//
//   - deps.go: accessors (liveConfig/serverReady/live*…) the handlers use to
//     reach the running Server for hot-config-reload — no handler touches the
//     bound singleton directly.
//   - globalBalancerStats / globalHealthChecker / globalYTChecker: atomic
//     pointers exposing process-wide singletons; set once during NewServer.
package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"

	"lampac-go/internal/adminhttp"
	"lampac-go/internal/antidpi"
	"lampac-go/internal/auth"
	"lampac-go/internal/balancerhealth"
	"lampac-go/internal/balancerstats"
	"lampac-go/internal/browser"
	"strconv"

	"lampac-go/internal/calendar"
	"lampac-go/internal/calendarhttp"
	"lampac-go/internal/cluster"
	"lampac-go/internal/collectionshttp"
	"lampac-go/internal/config"
	"lampac-go/internal/custbal"
	"lampac-go/internal/dlna"
	"lampac-go/internal/dlnahttp"
	"lampac-go/internal/envpresets"
	"lampac-go/internal/geoip"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/iptvhttp"
	"lampac-go/internal/jacred"
	"lampac-go/internal/jsmodules"
	"lampac-go/internal/kit"
	"lampac-go/internal/kpcatalog"
	"lampac-go/internal/litesrc"
	"lampac-go/internal/logbuf"
	"lampac-go/internal/modules"
	"lampac-go/internal/opensubs"
	"lampac-go/internal/profile"
	"lampac-go/internal/proxyapi"
	"lampac-go/internal/proxycore"
	"lampac-go/internal/proxyimg"
	"lampac-go/internal/proxylink"
	"lampac-go/internal/rutracker"
	"lampac-go/internal/sidecar"
	sidecaruri "lampac-go/internal/sidecar/uri"
	"lampac-go/internal/sisihttp"
	"lampac-go/internal/skiphttp"
	"lampac-go/internal/tgauth"
	"lampac-go/internal/tmdbcache"
	"lampac-go/internal/torrbalancer"
	"lampac-go/internal/transcodesvc"
	"lampac-go/internal/updater"
	"lampac-go/internal/uproxy"
	"lampac-go/internal/userdata"
	"lampac-go/internal/wasmmodules"
	"lampac-go/internal/ytauth"
	"lampac-go/internal/zapret"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	jsoniter "github.com/json-iterator/go"
	"github.com/rs/zerolog/log"
)

var json = jsoniter.ConfigCompatibleWithStandardLibrary

// groupStoreRef holds a reference to the user groups store.
// Used by lite_events, ts_api, auth for group-based access control.
var groupStoreRef *tgauth.GroupStore

// tgTokenStoreRef holds a reference to the TG token store for group resolution.
var tgTokenStoreRef *tgauth.Store

// currentPremiumGroupID returns the configured premium group id. The paid
// premium overlay is not part of this build, so the answer is always "" —
// group resolution in lite_events / kit / ts then falls through to the
// user's plain group.
func currentPremiumGroupID() string { return "" }

type Options struct {
	Manifest  []modules.RootModule
	LogBuffer *logbuf.Buffer
	Version   string // e.g. "0.3", set via ldflags at build time
	Commit    string // short git sha, set via ldflags at build time
	BuildDate string // RFC3339 build timestamp, set via ldflags at build time
}

type Server struct {
	httpServer    *http.Server
	tsExternalSrv *http.Server // optional dedicated listener for third-party clients
	addr          string
	version       string
	commit        string
	buildDate     string
	updater       *updater.Service
	cfg           config.Config                 // initial config snapshot (used by handler closures captured at startup)
	cfgPtr        atomic.Pointer[config.Config] // hot-reloadable config pointer
	localRoutes   []LocalRouteSpec
	tgBot         *tgauth.Bot
	proxyLinks    *proxylink.Manager
	proxyMgr      *sidecar.Manager // first manager (backward compat)
	proxyPool     *sidecar.Pool    // all proxy sidecar processes
	syncCron      *SyncCron
	transSvc      *transcodesvc.TranscodingService
	healthChecker *balancerhealth.HealthChecker
	custBalPool   *custbal.Pool
	jsModules     *jsmodules.Manager
	sisiSources   *jsmodules.Manager
	musicSources  *jsmodules.Manager
	wasmModules   *wasmmodules.Manager
	dynamicRoutes *DynamicRouteRegistry
	nwsHub        *nwsHub
	cacheCron     *CacheCron
	lampaCron     *LampaCron
	dlnaCoverGen  *dlnahttp.DLNACoverGenerator
	dlnaTorrent   *dlna.TorrentManager
	dlnaUPnP      *dlnahttp.DLNAUPnPServer
	tmdbCache     *tmdbcache.Cache
	tmdbPool      *tmdbcache.Pool
	calendarCron  *calendar.Cron
	clusterPool   *cluster.Pool
	clusterFwd    *cluster.Forwarder
	clusterStore  *cluster.Store

	tsBalancerPool  *torrbalancer.Pool
	tsBalancerStore *torrbalancer.Store
	antiDPI         *antidpi.Server
	zapret          *zapret.Manager
	jacredMgr       *jacred.Manager
	rutracker       *rutracker.Client
	memberChecker   *tgauth.MembershipChecker
	proxyCorePool   *proxycore.Pool
	balancerStats   *balancerstats.Manager
	alertEngine     *balancerstats.AlertEngine
	adminIDStore    *tgauth.AdminIDStore
}

// Cfg returns the current (possibly hot-reloaded) configuration.
// Handlers that need fresh config on every request should use liveConfig()
// instead of the closure-captured cfg value.
func (s *Server) Cfg() config.Config {
	if p := s.cfgPtr.Load(); p != nil {
		return *p
	}
	return s.cfg
}

// setupSystemDNS forces Go to use the system (cgo) DNS resolver instead of
// the built-in pure-Go resolver. Fixes issues with local DNS forwarders
// (MikroTik, CHR). Requires CGO_ENABLED=1 at build time.
func setupSystemDNS() {
	// PreferGo=false tells net.Resolver to prefer cgo (system) resolver.
	// This works when binary is built with CGO_ENABLED=1.
	// With CGO_ENABLED=0, Go ignores this and always uses its own resolver —
	// in that case use dns_servers as fallback.
	net.DefaultResolver = &net.Resolver{PreferGo: false}
	os.Setenv("GODEBUG", "netdns=cgo")
	log.Info().Msg("dns: system_dns=true — using system (cgo) DNS resolver")
}

// dnsEntry holds a parsed DNS server address.
type dnsEntry struct {
	addr string // host:port
	tls  bool   // DNS-over-TLS (port 853)
}

// setupCustomDNS overrides net.DefaultResolver to use specified DNS servers,
// bypassing the system /etc/resolv.conf. Fixes issues with local DNS
// forwarders (MikroTik, CHR) that break Go's built-in DNS resolver.
//
// Supported formats:
//   - "8.8.8.8"          → plain UDP/TCP on port 53
//   - "tls://8.8.8.8"    → DNS-over-TLS on port 853 (bypasses port-53 hijacking)
//   - "tls://1.1.1.1:853" → explicit DoT with port
func setupCustomDNS(servers []string) {
	var entries []dnsEntry
	for _, s := range servers {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		var e dnsEntry
		if strings.HasPrefix(s, "tls://") {
			e.tls = true
			s = strings.TrimPrefix(s, "tls://")
			if !strings.Contains(s, ":") {
				s += ":853"
			}
		} else {
			if !strings.Contains(s, ":") {
				s += ":53"
			}
		}
		e.addr = s
		entries = append(entries, e)
	}
	if len(entries) == 0 {
		return
	}

	idx := 0
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			e := entries[idx%len(entries)]
			idx++
			d := net.Dialer{Timeout: 5 * time.Second}
			if e.tls {
				// DNS-over-TLS: TCP+TLS on port 853.
				// Go's resolver uses TCP framing (2-byte length prefix)
				// which is the standard for DNS-over-TLS (RFC 7858).
				tcpConn, err := d.DialContext(ctx, "tcp", e.addr)
				if err != nil {
					return nil, err
				}
				host, _, _ := net.SplitHostPort(e.addr)
				tlsConn := tls.Client(tcpConn, &tls.Config{
					ServerName:         host,
					InsecureSkipVerify: false,
				})
				if err := tlsConn.HandshakeContext(ctx); err != nil {
					tcpConn.Close()
					return nil, err
				}
				return tlsConn, nil
			}
			return d.DialContext(ctx, "udp", e.addr)
		},
	}

	labels := make([]string, len(entries))
	for i, e := range entries {
		if e.tls {
			labels[i] = "tls://" + e.addr
		} else {
			labels[i] = e.addr
		}
	}
	log.Info().Strs("servers", labels).Msg("dns: using custom DNS servers")
}

func NewServer(opts Options) (*Server, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}

	// Apply DNS settings from config.
	if cfg.Server.SystemDNS {
		setupSystemDNS()
	} else if len(cfg.Server.DNSServers) > 0 {
		setupCustomDNS(cfg.Server.DNSServers)
	}

	// Apply TLS strictness setting from config.
	// Default behavior is unchanged (InsecureSkipVerify=true everywhere) —
	// only when admin opts in do we switch to verifying TLS. The opt-in is
	// safe because admins can also list known-broken upstreams as exceptions.
	if cfg.Security.StrictBalancerTLS {
		httpclient.EnableStrictBalancerTLS(cfg.Security.InsecureHosts)
		log.Info().
			Strs("insecure_hosts", cfg.Security.InsecureHosts).
			Msg("security: strict balancer TLS enabled (cert chain validation)")
	} else {
		log.Warn().Msg("security: balancer TLS validation disabled (InsecureSkipVerify=true). Set [security] strict_balancer_tls=true to enable cert validation.")
	}

	// Publish trusted-proxy list. Loopback is always trusted. Without this,
	// Loopback + RFC1918 trusted by default (covers Lampa-behind-nginx
	// deployments without config). Public-IP proxies must be listed in
	// [security] trusted_proxies. Paranoid setups can flip
	// [security] strict_rfc1918=true to require explicit allowlisting.
	SetTrustedProxies(cfg.Security.TrustedProxies)
	SetStrictRFC1918(cfg.Security.StrictRFC1918)
	if len(cfg.Security.TrustedProxies) > 0 {
		log.Info().Strs("trusted_proxies", cfg.Security.TrustedProxies).Msg("security: extra trusted proxy entries active")
	}
	if cfg.Security.StrictRFC1918 {
		log.Info().Msg("security: strict_rfc1918=true — only loopback + explicit proxies trusted")
	}

	// Wire browser-engine registry from config, then layer on any
	// admin-UI persisted overrides (database/browser_engine.json).
	browser.Apply(cfg.BrowserPool.Engine, cfg.BrowserPool.BalancerEngines)
	applyBrowserEnginePersist(cfg.Compat.RepoRoot)

	// Apply public branding from TOML [branding], then layer the admin-UI
	// override file (database/branding.json) on top. See branding_store.go.
	applyBrandingFromConfig(cfg)

	// Auto-generate config.toml with defaults if it doesn't exist.
	tomlPath := config.TOMLFilePath(cfg.Compat.RepoRoot)
	if _, err := os.Stat(tomlPath); os.IsNotExist(err) {
		annotated := adminhttp.GenerateAnnotatedTOML(cfg)
		if werr := os.WriteFile(tomlPath, []byte(annotated), 0o644); werr != nil {
			log.Warn().Err(werr).Msg("config: failed to auto-generate config.toml")
		} else {
			log.Info().Str("path", tomlPath).Msg("config: auto-generated config.toml")
		}
	}

	manifest := opts.Manifest
	if len(manifest) == 0 {
		// Same candidate order as liveManifest (runtime dir first) so the
		// boot copy and the hot-reloaded copy come from the same file.
		var lastErr error
		for _, manifestPath := range manifestCandidatePaths(cfg) {
			loaded, lerr := modules.LoadManifest(manifestPath)
			if lerr == nil {
				manifest = loaded
				log.Info().Int("modules", len(manifest)).Str("path", manifestPath).Msg("module manifest loaded")
				lastErr = nil
				break
			}
			if !os.IsNotExist(lerr) {
				lastErr = lerr
			}
		}
		if len(manifest) == 0 {
			if lastErr != nil {
				log.Warn().Err(lastErr).Msg("failed to load module manifest")
			} else {
				log.Debug().Msg("module manifest is not found")
			}
		}
	}

	var proxyLinks *proxylink.Manager
	proxyLinks, err = proxylink.New(proxylink.Options{
		CacheDir:     cfg.ProxyLink.CacheDir,
		VerifyIP:     cfg.ProxyLink.VerifyIP,
		EncryptAES:   cfg.ProxyLink.EncryptAES,
		SharedSecret: cfg.ProxyLink.SharedSecret,
	})
	if err != nil {
		log.Warn().Err(err).Str("cache_dir", cfg.ProxyLink.CacheDir).Msg("proxylink manager disabled")
	}

	// --- Telegram Auth ---
	var tgBot *tgauth.Bot
	var tgPending *tgauth.PendingStore
	var tgTokenStore *tgauth.Store
	groupStoreRef = tgauth.NewGroupStore(cfg.Compat.RepoRoot)
	var authOpts []auth.Option

	var tgDevicePending *tgauth.DevicePendingStore
	var tgLangStore *tgauth.LangStore
	var memberChecker *tgauth.MembershipChecker
	if cfg.TelegramAuth.Enable && cfg.TelegramAuth.BotToken != "" && cfg.TelegramAuth.AdminID > 0 {
		tgTokenStore = tgauth.NewStore(cfg.Compat.RepoRoot)
		tgTokenStoreRef = tgTokenStore
		tgPending = tgauth.NewPendingStore()
		tgDevicePending = tgauth.NewDevicePendingStore()
		tgLangStore = tgauth.NewLangStore(cfg.Compat.RepoRoot)
		tgBot = tgauth.NewBot(tgauth.BotConfig{
			Token:             cfg.TelegramAuth.BotToken,
			AdminID:           cfg.TelegramAuth.AdminID,
			BotName:           cfg.TelegramAuth.BotName,
			MaxDevicesPerUser: cfg.TelegramAuth.MaxDevicesPerUser,
			KitServerHost:     cfg.Kit.ServerHost,
			PublicHost:        firstNonEmpty(cfg.Capi.PublicHost, cfg.Kit.ServerHost),
			AutoApprove:       cfg.TelegramAuth.AutoApprove,
			AutoApproveDays:   cfg.TelegramAuth.AutoApproveDays,
		}, tgTokenStore, tgPending, tgDevicePending, tgLangStore)
		authOpts = append(authOpts, auth.WithTGStore(tgTokenStore))
		// Membership checker for required channel/group subscriptions.
		// Always created so admin panel can hot-reload chats without restart.
		memberChecker = tgauth.NewMembershipChecker(
			cfg.TelegramAuth.BotToken, tgTokenStore,
			cfg.TelegramAuth.RequiredChats, cfg.TelegramAuth.CheckIntervalMin,
		)
		memberChecker.SetNotifyFn(func(tgID int64, text string) {
			tgBot.SendToUser(tgID, text)
		})
		tgBot.SetMembershipChecker(memberChecker)
		tgBot.SetGroupStore(groupStoreRef)
		log.Info().Str("bot", cfg.TelegramAuth.BotName).Str("kit_host", cfg.Kit.ServerHost).Bool("kit_enable", cfg.Kit.Enable).Int("required_chats", len(cfg.TelegramAuth.RequiredChats)).Msg("telegram auth enabled")
	}

	// --- Promo Code Store ---
	promoStore := tgauth.NewPromoStore(cfg.Compat.RepoRoot)

	// --- Ban Store & GeoIP ---
	var banStore *tgauth.BanStore
	var geoDB *geoip.DB
	banStore = tgauth.NewBanStore(cfg.Compat.RepoRoot)
	geoDBPath := filepath.Join(cfg.Compat.RepoRoot, "database", "GeoLite2-Country.mmdb")
	geoDB = geoip.Open(geoDBPath)

	// --- Password Admin Auth (fallback when TG auth is not configured) ---
	var pwStore *tgauth.PasswordAuthStore
	var sessionSecret []byte
	if tgTokenStore == nil && cfg.AdminAuth.Password != "" {
		pwStore = tgauth.NewPasswordAuthStore(cfg.Compat.RepoRoot)
		// First boot: hash plaintext password from config.
		if !pwStore.IsConfigured() {
			if err := pwStore.SetPasswordFromPlaintext(cfg.AdminAuth.Password); err != nil {
				log.Error().Err(err).Msg("password auth: failed to hash password")
			} else {
				log.Info().Msg("password auth: admin password hashed and saved")
			}
		}
		// Generate ephemeral session signing key (restart = invalidate all sessions).
		sessionSecret = make([]byte, 32)
		if _, err := rand.Read(sessionSecret); err != nil {
			log.Fatal().Err(err).Msg("password auth: failed to generate session key")
		}
		log.Info().Msg("password auth: admin authentication enabled")
	}

	// --- WebAuthn / Passkeys (optional, alongside password auth) ---
	var webauthnStore *tgauth.WebAuthnStore
	if pwStore != nil {
		webauthnStore = tgauth.NewWebAuthnStore(cfg.Compat.RepoRoot)
		if webauthnStore.HasCredentials() {
			log.Info().Msg("webauthn: passkey credentials loaded")
		}
	}

	// --- User-facing password auth + anon issuer + mode store ---
	// All three are always instantiated so the admin v2 panel can flip
	// auth.mode at runtime without a restart. They're cheap (one JSON file
	// each, in-memory map indices) so this isn't gated on the initial
	// config.Auth.Mode.
	pwUserStore := tgauth.NewPasswordUserStore(cfg.Compat.RepoRoot)
	anonIssuer := tgauth.NewAnonIssuer(cfg.Compat.RepoRoot, cfg.Auth.Anon.SessionDays)
	authModeStore := tgauth.NewAuthModeStore(cfg.Compat.RepoRoot, cfg.Auth.Mode)
	// Auth-cookie SameSite policy: default Lax (old Android/TV WebViews reject SameSite=None, so the
	// TG login "succeeded" but never persisted); only a cross-origin Lampa-fork deploy sets "none".
	authCookieSameSiteNone = strings.EqualFold(cfg.Auth.CookieSameSite, "none")
	// Stash package-level refs so the legacy tg auth-page handler can
	// route to the right surface (password form / anon redirect) when the
	// admin switches modes at runtime.
	SetPasswordAuthState(pwUserStore, anonIssuer, authModeStore)

	authOpts = append(authOpts,
		auth.WithPasswordStore(pwUserStore),
		auth.WithAnonAuth(
			anonIssuer,
			anonIssuer.Issue,
			func(w http.ResponseWriter, r *http.Request, token string, _ time.Time) {
				setAuthCookies(w, r, token)
			},
			authModeStore.Get,
		),
	)
	log.Info().Str("mode", authModeStore.Get()).Msg("user auth: alternative modes wired (password + anon)")

	// --- Proxy sidecar(s) (xray / mihomo) ---
	var proxyPool *sidecar.Pool
	var proxyMgr *sidecar.Manager // backward compat: first manager for Server.proxyMgr
	if len(cfg.Proxy.Vless.Entries) > 0 {
		binDir := filepath.Join(cfg.Compat.RepoRoot, "bin")
		var poolConfigs []sidecar.PoolConfig
		for _, entry := range cfg.Proxy.Vless.Entries {
			poolConfigs = append(poolConfigs, sidecar.PoolConfig{
				URI:       entry.URI,
				Balancers: entry.Balancers,
				Label:     entry.Label,
				Engine:    entry.Engine,
			})
		}
		engines := map[sidecar.EngineType]sidecar.Engine{
			sidecar.EngineXray:   &sidecar.XrayEngine{},
			sidecar.EngineMihomo: &sidecar.MihomoEngine{},
		}
		pool, xerr := sidecar.NewPool(poolConfigs, binDir, 40000, engines, sidecaruri.Parse)
		if xerr != nil {
			log.Warn().Err(xerr).Msg("sidecar: proxy pool start failed")
		} else {
			proxyPool = pool
			for _, pe := range pool.Entries() {
				httpclient.RegisterProxiedBalancer(pe.Manager.SOCKSAddr(), pe.Balancers)
				if pe.Label != "" {
					httpclient.RegisterProxyLabel(pe.Label, pe.Manager.SOCKSAddr())
				}
				if proxyMgr == nil {
					proxyMgr = pe.Manager
				}
			}
			log.Info().Int("count", len(pool.Entries())).Msg("proxy pool started")
		}
	}

	// --- Direct proxies (SOCKS5 / HTTP) ---
	for _, entry := range cfg.Proxy.Direct.Entries {
		if entry.Label != "" {
			if u, err := url.Parse(entry.URI); err == nil && strings.HasPrefix(strings.ToLower(u.Scheme), "socks5") {
				httpclient.RegisterProxyLabel(entry.Label, u.Host)
			}
		}
		if err := httpclient.RegisterDirectProxy(entry.URI, entry.Balancers); err != nil {
			log.Warn().Err(err).Str("uri", entry.URI).Msg("direct proxy registration failed")
		} else {
			label := entry.Label
			if label == "" {
				label = entry.URI
			}
			log.Info().Str("proxy", label).Strs("balancers", entry.Balancers).Msg("direct proxy registered")
		}
	}

	// --- ProxyCore: built-in proxy engine ---
	var proxyCorePool *proxycore.Pool
	if len(cfg.ProxyCore.Entries) > 0 {
		var poolEntries []proxycore.PoolEntry
		for _, entry := range cfg.ProxyCore.Entries {
			poolEntries = append(poolEntries, proxycore.PoolEntry{
				URI:       entry.URI,
				Label:     entry.Label,
				Balancers: entry.Balancers,
				Engine:    entry.Engine,
			})
		}
		basePort := cfg.ProxyCore.BasePort
		if basePort == 0 {
			basePort = 41000
		}
		pcBinDir := filepath.Join(cfg.Compat.RepoRoot, "bin")
		pcPool, pcErr := proxycore.NewPool(poolEntries, basePort, pcBinDir)
		if pcErr != nil {
			log.Warn().Err(pcErr).Msg("proxycore: pool start failed")
		} else {
			proxyCorePool = pcPool
			// Resolve GeoIP for each instance in background.
			go pcResolveGeo(pcPool)
			log.Info().Int("count", len(pcPool.Entries())).Msg("proxycore: pool started")
		}
	}

	// --- Custom balancers subprocess pool ---
	dynRoutes := NewDynamicRouteRegistry()
	custBalDir := filepath.Join(cfg.Compat.RepoRoot, "custom_balancers")
	mainHost := "http://127.0.0.1" + cfg.Server.Addr
	custBalPool := custbal.NewPool(custBalDir, 50000, mainHost)

	// --- JS modules (user-editable Lite sources) ---
	jsModulesDir := filepath.Join(cfg.Compat.RepoRoot, "modules")
	jsLogger := log.With().Str("pkg", "jsmodules").Logger()
	jsMgr, jsErr := jsmodules.NewManager(jsModulesDir, http.DefaultClient, proxyLinks, &jsLogger)
	if jsErr != nil {
		log.Warn().Err(jsErr).Msg("jsmodules: init failed")
	} else {
		// Wire global FlareSolverr URL so modules can request transport:"flaresolverr"
		// from http.* and bypass Cloudflare/DDoS-Guard challenges.
		jsMgr.FlareSolverr = strings.TrimSpace(cfg.Online.FlareSolverr)
		// Mirror the same URL into the httpclient registry so built-in
		// balancers can call FlareSolverrFetch without threading config.
		httpclient.SetFlareSolverrURL(strings.TrimSpace(cfg.Online.FlareSolverr))
		// Replace the whole user-managed set in one call so config edits that
		// *remove* a balancer actually take effect — looping Register would
		// only ever add, never drop. System balancers (turbo, etc.) registered
		// from code in package init paths are untouched.
		httpclient.ApplyUserFlareSolverrBalancers(cfg.Online.FlareSolverrBalancers)
		jsMgr.OnChange(func(m *jsmodules.Module, event string) {
			switch event {
			case "loaded", "reloaded", "installed":
				dynRoutes.RegisterHandler(m.PluginKey(), jsMgr.Handler(m.PluginKey()))
				if q := strings.TrimSpace(m.Manifest.Quality); q != "" {
					pluginQualityBadgeSet(m.PluginKey(), q)
				}
			case "removed":
				dynRoutes.Unregister(m.PluginKey())
				pluginQualityBadgeSet(m.PluginKey(), "")
			case "toggled":
				if m.Enabled {
					dynRoutes.RegisterHandler(m.PluginKey(), jsMgr.Handler(m.PluginKey()))
				} else {
					dynRoutes.Unregister(m.PluginKey())
				}
			}
		})
	}

	// --- SISI JS sources (goja-based adult-content sources) ---
	sisiSourcesDir := filepath.Join(cfg.Compat.RepoRoot, "sisi_sources")
	sisiLogger := log.With().Str("pkg", "sisi-sources").Logger()
	sisiSrcMgr, sisiSrcErr := jsmodules.NewManager(sisiSourcesDir, http.DefaultClient, proxyLinks, &sisiLogger)
	if sisiSrcErr != nil {
		log.Warn().Err(sisiSrcErr).Msg("sisi-sources: init failed")
	} else {
		// SISI-источники тоже могут просить transport:"flaresolverr" из http.* —
		// прокидываем тот же URL что и для modules/.
		sisiSrcMgr.FlareSolverr = strings.TrimSpace(cfg.Online.FlareSolverr)
	}

	// --- Music sources (goja-based music backends, sibling to audiobot) ---
	musicSourcesDir := filepath.Join(cfg.Compat.RepoRoot, "music_sources")
	musicLogger := log.With().Str("pkg", "music-sources").Logger()
	musicSrcMgr, musicSrcErr := jsmodules.NewManager(musicSourcesDir, http.DefaultClient, proxyLinks, &musicLogger)
	if musicSrcErr != nil {
		log.Warn().Err(musicSrcErr).Msg("music-sources: init failed")
	} else {
		musicSrcMgr.FlareSolverr = strings.TrimSpace(cfg.Online.FlareSolverr)
	}

	// --- Environment presets (PAC-файлы, systemd-сервисы, helper-скрипты с
	// template-параметрами; ставятся через hub.alcopa.cc/hub/api/env-presets-catalog.json).
	envPresetsDir := filepath.Join(cfg.Compat.RepoRoot, "env_presets")
	envLogger := log.With().Str("pkg", "envpresets").Logger()
	lampacOSUser := ""
	if u := os.Getenv("USER"); u != "" {
		lampacOSUser = u
	}
	envPresetsMgr, envPresetsErr := envpresets.NewManager(envPresetsDir, cfg.Compat.RepoRoot, lampacOSUser, &envLogger)
	if envPresetsErr != nil {
		log.Warn().Err(envPresetsErr).Msg("envpresets: init failed")
	}

	// --- WASM modules (language-agnostic plugins, sibling to jsmodules) ---
	wasmModulesDir := filepath.Join(cfg.Compat.RepoRoot, "wasm_modules")
	wasmLogger := log.With().Str("pkg", "wasmmodules").Logger()
	wasmMgr, wasmErr := wasmmodules.NewManager(wasmModulesDir, http.DefaultClient, proxyLinks, &wasmLogger)
	if wasmErr != nil {
		log.Warn().Err(wasmErr).Msg("wasmmodules: init failed")
	} else {
		// Middleware plugins need to call other balancers — wire the resolver
		// to the dynamic route registry, which knows about every balancer
		// (built-in, JS, WASM-server, custbal sidecars).
		wasmMgr.SetUpstreamResolver(func(name string) (http.Handler, bool) {
			return dynRoutes.Lookup(name)
		})
		wasmMgr.OnChange(func(m *wasmmodules.Module, event string) {
			if !m.Manifest.HasServer() {
				return
			}
			pickHandler := func() http.Handler {
				if m.Manifest.IsMiddleware() {
					return wasmMgr.MiddlewareHandler(m.PluginKey())
				}
				return wasmMgr.Handler(m.PluginKey())
			}
			switch event {
			case "loaded", "reloaded", "installed":
				dynRoutes.RegisterHandler(m.PluginKey(), pickHandler())
				if q := strings.TrimSpace(m.Manifest.Quality); q != "" {
					pluginQualityBadgeSet(m.PluginKey(), q)
				}
			case "removed":
				dynRoutes.Unregister(m.PluginKey())
				pluginQualityBadgeSet(m.PluginKey(), "")
			case "toggled":
				if m.Enabled {
					dynRoutes.RegisterHandler(m.PluginKey(), pickHandler())
				} else {
					dynRoutes.Unregister(m.PluginKey())
				}
			}
		})
	}

	// --- Profile Store (self-hosted username+password sync identity) ---
	// Always created so /api/profile/* responds even when nobody has
	// registered yet. The auth middleware only consults it when a
	// `lampac_profile_session` cookie is present, so the cost is zero for
	// non-profile requests.
	profileStore := profile.NewStore(cfg.Compat.RepoRoot)
	SetProfileStore(profileStore)
	authOpts = append(authOpts, auth.WithProfileStore(ProfileSessionChecker(profileStore)))

	// Bot integration: lets TG users manage their owned sync profiles via
	// /profiles in the chat. Safe to call when tgBot is nil — happens when
	// Telegram auth is disabled in config, in which case profile management
	// is only available via the HTTP `/api/profile/owned/*` endpoints
	// (those still require a TG cookie, so they're effectively dormant).
	if tgBot != nil {
		tgBot.SetProfileManager(NewProfileBotAdapter(profileStore))
	}

	// Background janitor: purge expired sessions / sync codes every hour.
	// Without this, dead entries accumulate in profiles.json / sync_codes.json
	// forever and the load() scan slows down linearly with deployment age.
	go func() {
		t := time.NewTicker(1 * time.Hour)
		defer t.Stop()
		for range t.C {
			profileStore.PurgeExpired()
		}
	}()

	// Mirror edge mode: resolve identity (layer 1) via the origin instead of
	// a local token store. mirrorCl is also used below to install the
	// delegating auth gate (layer 2) in place of the local TG gate.
	var mirrorCl *mirrorClient
	if cfg.Mirror.Enable {
		mirrorCl = newMirrorClient(func() config.MirrorConfig {
			return liveConfig(cfg).Mirror
		})
		mirrorClientRef = mirrorCl // enables delegated group filtering in lite_events
		// Appended last so it wins over any local WithTGStore — on a mirror
		// the local TG store is normally absent anyway.
		authOpts = append(authOpts, auth.WithTGStore(mirrorAuthChecker{c: mirrorCl}))
		log.Info().Str("origin", cfg.Mirror.APIHost).Int("cache_ttl_sec", cfg.Mirror.CacheTTLSec).Int("grace_ttl_sec", cfg.Mirror.GraceTTLSec).Msg("mirror mode enabled: delegating auth control-plane to origin")

		// Фаза 3: pull plugins (and optionally wwwroot) from the origin.
		if cfg.Mirror.SyncPluginsMin != 0 {
			newMirrorAssetSyncer("plugins", "/api/cluster/plugins.tar.gz",
				updater.ResolvePluginsDir(cfg.Compat.RepoRoot), mirrorCl.cfgFn).Start()
		}
		if cfg.Mirror.SyncWwwroot {
			newMirrorAssetSyncer("wwwroot", "/api/cluster/wwwroot.tar.gz",
				updater.ResolveWwwrootDir(cfg.Compat.RepoRoot), mirrorCl.cfgFn).Start()
		}
	}

	authMiddleware := auth.New(authOpts...)
	proxyHandler := uproxy.New(proxyLinks)
	proxyAPIHandler := proxyapi.New(cfg, proxyLinks)
	proxyAPIHandler.EdgeHashLookup = func(_ string) string {
		return litesrc.GetAnyEdgeHash()
	}
	proxyapi.TrafficRecorder = func(plugin string, bytes int64, isError bool) {
		runtimeTrafficStats.RecordProxy(plugin, bytes, isError)
	}
	proxyapi.ActiveStreamCounter = runtimeTrafficStats.BeginProxy
	proxyapi.LatencyRecorder = func(plugin string, elapsed time.Duration, status int) {
		runtimeRequestStats.observeProxyPL(plugin, status, elapsed)
	}
	proxyImgHandler, err := proxyimg.New(cfg, proxyLinks)
	if err != nil {
		log.Warn().Err(err).Msg("proxyimg handler disabled")
	}

	router := chi.NewRouter()
	router.Use(normalizeRequestPathMiddleware)
	router.Use(globalCORSMiddleware)
	// Gzip compression for JSON/HTML/JS responses. Skips binary streams
	// (proxy handler sets Content-Type to video/* / application/octet-stream).
	compressor := chimw.NewCompressor(5, "application/json", "text/html", "text/javascript", "application/javascript", "text/css", "text/plain")
	router.Use(compressor.Handler)
	router.Use(requestIDMiddleware)
	if cfg.Observability.AccessLog {
		router.Use(loggingMiddleware)
	}
	router.Use(recoverMiddleware)
	router.Use(wafMiddleware(geoDB))
	router.Use(authMiddleware.Handler)
	// Build the local TG auth gate once so it can be reused both as the
	// installed middleware AND by the mirror-origin authgate RPC (which runs
	// this exact gate against a synthetic request to produce an authoritative
	// verdict for mirror edges).
	var gateMW func(http.Handler) http.Handler
	if tgTokenStore != nil {
		gateMW = tgAuthGateMiddleware(tgTokenStore, tgDevicePending, tgPending, tgBot, banStore, geoDB, memberChecker, cfg)
	}
	switch {
	case mirrorCl != nil:
		// Mirror edge: proxy the auth control-plane (+ centralized user state)
		// to the origin, then delegate every gate decision to the origin, then
		// inject the origin-delegated personal kit visibility into context.
		router.Use(mirrorProxyMiddleware(mirrorCl))
		router.Use(mirrorGateMiddleware(mirrorCl))
		router.Use(mirrorKitContextMiddleware(mirrorCl))
	case gateMW != nil:
		router.Use(gateMW)
	}
	router.Use(proxyCompatMiddleware(proxyHandler, proxyImgHandler, proxyAPIHandler))
	router.Use(xLampacServerMiddleware)

	// Installer is available at /web/installer (no forced redirect).
	// Users navigate there manually or via admin panel link.

	// --- Kit (per-user balancer settings via TG WebApp) ---
	var kitStore *kit.Store
	if cfg.Kit.Enable {
		kitStore = kit.NewStore(cfg.Compat.RepoRoot, time.Duration(cfg.Kit.CacheToSeconds)*time.Second, cfg.Kit.Encrypt)
		router.Use(kitMiddleware(kitStore, cfg))
		log.Info().Msg("kit: per-user balancer settings enabled")
	}

	// --- Browser Pool settings from config ---
	if bp := cfg.BrowserPool; bp.MaxConcurrent > 0 {
		litesrc.SetMirageBrowserLimit(bp.MaxConcurrent)
	}
	if bp := cfg.BrowserPool; bp.StreamCacheMax > 0 {
		litesrc.SetMirageStreamCacheMax(bp.StreamCacheMax)
	}
	if bp := cfg.BrowserPool; bp.StreamCacheTTLH > 0 {
		litesrc.SetMirageStreamCacheTTL(bp.StreamCacheTTLH)
	}

	// --- Custom JS plugins (hot-loadable via admin panel) ---
	customPlugins := NewCustomPluginRegistry(filepath.Join(cfg.Compat.RepoRoot, "plugins", "custom"))

	// --- Community plugins auto-updater ---
	commCron := NewCommunityUpdateCron(
		customPlugins,
		func() string {
			return liveConfig(cfg).Web.CommunityPluginURL
		},
		func() int {
			return liveConfig(cfg).Web.CommunityAutoUpdateHours
		},
	)
	commCron.Start(context.Background())

	// --- Web Installer routes ---
	// All routes registered unconditionally; handlers check live config dynamically.
	{
		var installerPwStore *tgauth.PasswordAuthStore
		if pwStore != nil {
			installerPwStore = pwStore
		}
		blocked := installerBlockedHandler()
		page := installerPageHandler()
		defaults := installerDefaultsHandler()
		apply := installerApplyHandler(&installerPwStore)
		validate := installerValidateHandler()

		guardPage := func(w http.ResponseWriter, r *http.Request) {
			if liveConfig(config.Config{}).Server.SetupDone {
				blocked.ServeHTTP(w, r)
			} else {
				page.ServeHTTP(w, r)
			}
		}
		guardAPI := func(active http.HandlerFunc) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				if liveConfig(config.Config{}).Server.SetupDone {
					blocked.ServeHTTP(w, r)
				} else {
					active.ServeHTTP(w, r)
				}
			}
		}
		router.Get("/web/installer", http.HandlerFunc(guardPage))
		router.Get("/web/installer/api/defaults", guardAPI(defaults))
		router.Post("/web/installer/api/apply", guardAPI(apply))
		router.Post("/web/installer/api/validate", guardAPI(validate))
		if !cfg.Server.SetupDone {
			log.Info().Msg("installer: setup not done, /web/installer is active — all other routes redirect")
		}
	}

	registerSystemRoutes(router, cfg)

	// --- AntiDPI (YouTube DPI bypass) ---
	var antiDPIServer *antidpi.Server
	if cfg.AntiDPI.Enable {
		listen := cfg.AntiDPI.Listen
		if listen == "" {
			listen = "127.0.0.1:9898"
		}
		strategy := cfg.AntiDPI.Strategy
		if strategy == "" {
			strategy = "auto"
		}
		antiDPIServer = antidpi.New(listen, strategy, antidpi.DefaultHosts)
		if err := antiDPIServer.Start(); err != nil {
			log.Error().Err(err).Msg("antidpi: failed to start")
			antiDPIServer = nil
		} else {
			// Register as SOCKS5 proxy for YouTube balancer.
			httpclient.RegisterProxiedBalancer(antiDPIServer.Addr(), []string{"youtube"})
			log.Info().Str("addr", antiDPIServer.Addr()).Str("strategy", strategy).Msg("antidpi: registered for YouTube")
		}
	}

	// --- Zapret (kernel-level DPI bypass via bol-van/zapret nfqws) ---
	// Runs in parallel with antidpi: zapret affects ALL traffic to the host
	// list (so even our uTLS-direct YouTube path benefits), antidpi continues
	// to be the SOCKS5 endpoint registered for `youtube`. When zapret works,
	// admins can disable antidpi for cleanliness.
	var zapretMgr *zapret.Manager
	if cfg.Zapret.Enable {
		zapretRunDir := filepath.Join(cfg.Compat.RepoRoot, "database", "zapret")
		// Default auto_install=true — explicit `auto_install = false` opts out.
		autoInstall := true
		if cfg.Zapret.AutoInstall != nil {
			autoInstall = *cfg.Zapret.AutoInstall
		}
		zCfg := zapret.Config{
			Enable:       true,
			Binary:       cfg.Zapret.Binary,
			QueueNum:     cfg.Zapret.QueueNum,
			TCPPorts:     cfg.Zapret.TCPPorts,
			Strategies:   cfg.Zapret.Strategies,
			DesyncTTL:    cfg.Zapret.DesyncTTL,
			HostlistPath: cfg.Zapret.HostlistPath,
			HostlistOnly: cfg.Zapret.HostlistOnly,
			NFTable:      cfg.Zapret.NFTable,
			Permissive:   cfg.Zapret.Permissive,
			ExtraArgs:    cfg.Zapret.ExtraArgs,
			AutoInstall:  autoInstall,
		}
		// Default permissive=true if not explicitly set in TOML.
		if !cfg.Zapret.Permissive {
			zCfg.Permissive = true
		}
		zapretMgr = zapret.New(zCfg, zapretRunDir)
		if err := zapretMgr.Start(context.Background()); err != nil {
			log.Error().Err(err).Msg("zapret: start failed")
			zapretMgr = nil
		} else {
			log.Info().Strs("strategies", zCfg.Strategies).Int("queue", zCfg.QueueNum).Msg("zapret: nfqws supervised, nftables installed")
		}
	}

	// --- Local JacRed (self-hosted torrent parser, github.com/jacred-fdb/jacred) ---
	// Managed sidecar: the binary and FDB base are downloaded on first start,
	// the process is supervised and the built-in scheduler replaces the host
	// crontab. The /api/v2.0/indexers/* proxy prefers this instance and falls
	// back to Parser.JacRedHost while it is down or still bootstrapping.
	var jacredMgr *jacred.Manager
	if cfg.Parser.JacRedLocal {
		jacredHome := strings.TrimSpace(cfg.Parser.JacRedHome)
		if jacredHome == "" {
			jacredHome = filepath.Join(cfg.Compat.RepoRoot, "jacred")
		}
		bootstrapDB := true
		if cfg.Parser.JacRedBootstrapDB != nil {
			bootstrapDB = *cfg.Parser.JacRedBootstrapDB
		}
		jacredMgr = jacred.New(jacred.Config{
			Enable:      true,
			HomeDir:     jacredHome,
			Port:        cfg.Parser.JacRedLocalPort,
			APIKey:      cfg.Parser.JacRedKey,
			SyncAPI:     cfg.Parser.JacRedSyncAPI,
			BootstrapDB: bootstrapDB,
		})
		if err := jacredMgr.Start(context.Background()); err != nil {
			log.Error().Err(err).Msg("jacred: start failed")
			jacredMgr = nil
		} else {
			log.Info().Int("port", cfg.Parser.JacRedLocalPort).Str("home", jacredHome).Msg("jacred: local parser supervised")
		}
	}

	// --- Native RuTracker indexer ---
	// Always constructed (it is inert without credentials and touches no
	// network until the first search), so enabling it in the admin panel takes
	// effect without a restart — SetConfig on each request picks the live
	// values up.
	rutrackerClient := rutracker.New(rutrackerConfigFrom(cfg))
	if rutrackerClient.Enabled() {
		// Opt the source into FlareSolverr as a *system* entry so it survives
		// a config reload, exactly like the balancers that need it.
		httpclient.RegisterFlareSolverrBalancerSystem("rutracker")
		log.Info().Str("host", cfg.Parser.RuTracker.Host).Msg("rutracker: native indexer enabled")
	}

	// --- Cluster ---
	var clusterPool *cluster.Pool
	var clusterFwd *cluster.Forwarder
	var clusterStore *cluster.Store
	router.Get("/api/cluster/ping", clusterPingHandler())
	router.Post("/api/cluster/userdata/delete", clusterUserdataDeleteHandler())
	router.Get("/api/servers/info", publicServerInfoHandler())
	router.Get("/api/servers/list", publicServerListHandler())
	router.Get("/servers", publicServersPageHandler())

	// Mirror-origin control-plane (server-to-server, self-authenticated by the
	// [mirror]/[sync] secret; both paths are on gatePreAuthAllowed's allowlist).
	// validate (layer 1) is always available — returns ok:false without a local
	// token store. authgate (layer 2) reuses the local gate for an authoritative
	// verdict, so it's only registered on an origin that has one.
	router.Get("/api/auth/validate", mirrorOriginValidateHandler(tgTokenStore, kitStore))
	if gateMW != nil {
		router.Post("/api/cluster/authgate", mirrorOriginAuthGateHandler(gateMW))
	}
	// Origin asset feeds (Фаза 3): mirrors pull plugins/wwwroot from here.
	mirrorRepoRoot := cfg.Compat.RepoRoot
	router.Get("/api/cluster/plugins.tar.gz", mirrorOriginAssetHandler(&assetTarballCache{
		name: "plugins", dirFn: func() string { return updater.ResolvePluginsDir(mirrorRepoRoot) },
	}))
	router.Get("/api/cluster/wwwroot.tar.gz", mirrorOriginAssetHandler(&assetTarballCache{
		name: "wwwroot", dirFn: func() string { return updater.ResolveWwwrootDir(mirrorRepoRoot) },
	}))

	// Profile API (self-hosted username+password sync identity). These are
	// always registered — handlers return 503 when the store wiring failed
	// so the client can render a friendly "profile feature unavailable"
	// state instead of a generic 404. See profile_api.go for the contract.
	router.Post("/api/profile/register", profileRegisterHandler())
	router.Post("/api/profile/login", profileLoginHandler())
	router.Post("/api/profile/logout", profileLogoutHandler())
	router.Get("/api/profile/me", profileMeHandler())
	router.Post("/api/profile/change-password", profileChangePasswordHandler())
	router.Post("/api/profile/sync-code/issue", profileIssueSyncCodeHandler())
	router.Post("/api/profile/sync-code/redeem", profileRedeemSyncCodeHandler())
	// PIN login + TG-owned profile management — see profile_api.go.
	// /pin-login is public + rate-limited; /owned/* require a TG session
	// (the auth middleware populates User{ID:"tg:NN"}; the handlers verify
	// ownership before mutating).
	router.Post("/api/profile/pin-login", profilePinLoginHandler())
	router.Get("/api/profile/owned", profileOwnedListHandler())
	router.Post("/api/profile/owned/create", profileOwnedCreateHandler())
	router.Post("/api/profile/owned/set-pin", profileOwnedSetPINHandler())
	router.Post("/api/profile/owned/delete", profileOwnedDeleteHandler())
	if cfg.Cluster.Enable {
		validateClusterConfig(cfg)
	}
	if cfg.Cluster.Enable && cfg.Cluster.Mode == "primary" {
		var err error
		clusterStore, err = cluster.NewStore(cfg.Compat.RepoRoot, cfg.Cluster)
		if err != nil {
			log.Error().Err(err).Msg("cluster: store init failed, falling back to config-only")
			clusterPool = cluster.NewPool(cfg.Cluster, nil)
		} else {
			clusterPool = cluster.NewPool(cfg.Cluster, clusterStore)
		}
		clusterFwd = cluster.NewForwarder(clusterPool)
		clusterPool.Start()
		router.Get("/api/cluster/status", clusterStatusHandler(clusterPool))
		log.Info().Int("nodes", len(clusterPool.Nodes())).Msg("cluster: primary mode enabled")
	}

	router.Get("/_lampac/modules", modulesHandler(manifest))
	router.Get("/on.js", onJSHandler(cfg, manifest, customPlugins, tgPending, tgTokenStore))
	router.Get("/on/js/{token}", onJSHandler(cfg, manifest, customPlugins, tgPending, tgTokenStore))
	router.Get("/on/h/{token}", onJSHandler(cfg, manifest, customPlugins, tgPending, tgTokenStore))
	router.Get("/on/{token}", onJSHandler(cfg, manifest, customPlugins, tgPending, tgTokenStore))
	// Optional short alias for /on.js (config: [web] plugin_short_path = "m" → /m).
	// Wrapped in a recover so a value that collides with an existing route logs an
	// error instead of panicking the whole server at boot.
	if route := cfg.Web.ShortPluginRoute(); route != "" && route != "/on.js" {
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error().Interface("recover", rec).Str("route", route).Msg("web: plugin_short_path conflicts with an existing route, alias skipped")
				}
			}()
			router.Get(route, onJSHandler(cfg, manifest, customPlugins, tgPending, tgTokenStore))
			log.Info().Str("route", route).Msg("web: short /on.js alias enabled")
		}()
	}
	router.Get("/online.js", onlineJSHandler(cfg, tgPending, tgTokenStore))
	router.Get("/online/js/{token}", onlineJSHandler(cfg, tgPending, tgTokenStore))
	router.Get("/lite.js", liteJSHandler(cfg))
	router.Get("/lite/js", liteJSHandler(cfg))
	router.Get("/lite/js/{token}", liteJSHandler(cfg))
	// ALPAC web/TV client forwards its in-app weblog here → terminal (journalctl). See weblog_collect.go.
	router.Post("/lite/weblog", weblogCollectHandler(cfg))
	// Playback capability telemetry: one structured report per playback attempt,
	// aggregated so "no sound in torrents" becomes a codec/device histogram
	// instead of a weblog archaeology exercise.
	initPlaybackStats(cfg)
	router.Post("/lite/playback-report", playbackReportHandler())
	router.Get("/lite/playback-advice", playbackAdviceHandler())
	// CMCD: the player's own view of playback, riding along on the segment
	// requests the proxy already serves. See cmcd_stats.go.
	initCMCDStats(cfg)
	// «Сообщить о проблеме»: device snapshot + recent weblog → correlated with server logs (incident.go).
	// tgBot/tgTokenStore enable the Telegram alert + admin→user reply (NotifyUserContext).
	router.Post("/lite/incident", incidentReceiveHandler(cfg, opts.LogBuffer, tgBot, tgTokenStore))
	router.Get("/lite/incident/status", incidentStatusHandler(cfg)) // «Мои обращения»: status of the caller's own ids
	// Lampac plugin templates expect {localhost} to be the server origin.
	dlnaTM, dlnaCG, dlnaUPnP := dlnahttp.RegisterRoutes(router, cfg, dlnahttp.Deps{PluginJS: genericPluginJSHandler})
	transSvc := transcodesvc.RegisterRoutes(router, cfg, transcodesvc.Deps{
		LiveConfig:        func() config.Config { return liveConfig(config.Config{}) },
		PluginJS:          genericPluginJSHandler,
		TSBalancerPool:    liveTSBalancerPool,
		TorrsInProcess:    torrsIsInProcess,
		GetTorrsServer:    getTorrsServer,
		PidtorExtractTR:   pidtorExtractTRFromQuery,
		PidtorTSAddMagnet: pidtorTSAddMagnet,
	})
	registerPluginJSRoutes(router, cfg)

	// Legacy /admin auth routes removed — use TG admin panel at /{adminPath}/ instead.
	// Config endpoints kept for backward compat.
	router.Get("/admin/init/example", adminInitExampleHandler())
	router.Post("/admin/init/save", adminInitSaveHandler())
	router.HandleFunc("/admin/manifest/install", adminhttp.AdminManifestInstallHandler())
	router.Get("/admin/sync/init", adminSyncInitHandler())
	router.Post("/admin/sync/init/save", adminSyncSaveHandler())
	// Self-hosted, quota-free kp-id resolver store (capi → kp-only sources). Works without a
	// [kinopoisk] token — Wikidata is the free primary; see kpid_resolver.go.
	initKpIDStore(filepath.Join(cfg.Compat.RepoRoot, "database", "kp_id_map.json"))
	setKpByTmdbEndpoints(cfg.Kinopoisk.KpByTmdb) // extra TMDB→kp resolver endpoints (upn.stull.xyz/apbugall/…)
	if token := strings.TrimSpace(cfg.Kinopoisk.Token); token != "" {
		kpClientSingleton = kpcatalog.NewKPClient(token)
		log.Info().Msg("kinopoisk: catalog source enabled")
	}
	router.Get("/catalog", catalogIndexHandler())
	router.Get("/catalog/catalog", catalogIndexHandler())
	router.Get("/catalog/list", catalogListHandler())
	router.Get("/catalog/card", catalogCardHandler())
	tsTokenStoreRef = tgTokenStore // allow TS middleware to check per-user access
	// TorrServer balancer: a pool of backend TorrServers selected sticky-by-
	// infohash, with per-group access. Seeds backend #1 from the legacy
	// [torrserver] config so existing single-server deployments are unchanged.
	// The package ref MUST be set before registerTSRoutes so the proxy built
	// inside captures the pool.
	var tsBalancerStore *torrbalancer.Store
	var tsBalancerPool *torrbalancer.Pool
	if st, tbErr := torrbalancer.NewStore(cfg.Compat.RepoRoot, cfg.TorrServer); tbErr != nil {
		log.Error().Err(tbErr).Msg("torrbalancer: store init failed; falling back to legacy single upstream")
	} else {
		tsBalancerStore = st
		tsBalancerPool = torrbalancer.NewPool(st)
		tsBalancerPoolRef = tsBalancerPool
		tsBalancerPool.OnZombie = tsZombieHandler(tsBalancerPool) // alert + optional auto-restart on stream circuit-breaker trip
		tsBalancerPool.Start()
	}
	registerTSRoutes(router, cfg)
	// CUB account validator — resolves CUB tokens to stable user ids for the
	// CUB recovery anchor (see cub_auth.go). Shared by the /cub/ proxy
	// (passive link learning) and /tg/auth/status (recovery rung).
	cubAuthVal := newCubAuthValidator(cfg)
	router.Handle("/cub/*", cubProxyHandler(cfg, customPlugins, tgTokenStore, cubAuthVal))
	router.Handle("/cubproxy/cub/*", cubProxyHandler(cfg, customPlugins, tgTokenStore, cubAuthVal))
	// TMDB proxy with in-memory cache + upstream pool + healthcheck.
	tmdbCacheInst := tmdbcache.NewCache(cfg.TMDBProxy.CacheMaxItems)
	tmdbCacheInst.StartCleanup()
	tmdbPoolInst := tmdbcache.NewPool(tmdbcache.PoolConfig{
		CustomAPIHost:    cfg.TMDBProxy.APIHost,
		CustomIMGHost:    cfg.TMDBProxy.IMGHost,
		APIKey:           cfg.TMDBProxy.APIKey,
		TimeoutSec:       cfg.TMDBProxy.UpstreamTimeoutSec,
		MaxConcurrentIMG: cfg.TMDBProxy.MaxConcurrentIMG,
		MaxConcurrentAPI: cfg.TMDBProxy.MaxConcurrentAPI,
	})
	tmdbPoolInst.StartHealthcheck()
	imgCacheDir := cfg.TMDBProxy.ImgCacheDir
	if imgCacheDir == "" {
		imgCacheDir = filepath.Join(cfg.Compat.RepoRoot, "database", "tmdb-img")
	}
	tmdbImgCache := tmdbcache.NewImageCache(imgCacheDir, cfg.TMDBProxy.ImgCacheMaxMB)
	router.Handle("/tmdb/*", tmdbProxyHandler(tmdbCacheInst, tmdbPoolInst, tmdbImgCache, cfg.TMDBProxy.APIKey, cfg.TMDBProxy.CacheTTLMin))
	router.Post("/bookmark/add", userdata.BookmarkAddHandler(false))
	router.HandleFunc("/bookmark/added", userdata.BookmarkAddHandler(true))
	router.Get("/bookmark/list", userdata.BookmarkListHandler())
	router.Post("/bookmark/remove", userdata.BookmarkRemoveHandler())
	router.Post("/bookmark/set", userdata.BookmarkSetHandler())
	router.Get("/timecode/all", userdata.TimecodeAllHandler())
	router.Post("/timecode/add", userdata.TimecodeAddHandler())
	router.Get("/storage/get", userdata.StorageGetHandler())
	router.Post("/storage/set", userdata.StorageSetHandler())
	// Self-service «снести мои бэкапы/настройки со всех серверов» (syncpro → Действия).
	// expand: авторизованному tg-юзеру добавляем его device-UID'ы (легаси-плагины
	// писали бэкапы под ?uid=<lampac_unic_id>) и sub-профили; fanout — те же
	// кластер-ноды, что у админской чистки.
	router.Post("/storage/wipe", userdata.StorageWipeHandler(
		func(uid string) (extra []string, profiles []string) {
			if tgTokenStore != nil && strings.HasPrefix(uid, "tg:") {
				if id, err := strconv.ParseInt(strings.TrimPrefix(uid, "tg:"), 10, 64); err == nil {
					if tok := tgTokenStore.FindByTelegramID(id); tok != nil {
						for _, d := range tok.Devices {
							if d.UID != "" {
								extra = append(extra, d.UID)
							}
						}
					}
				}
			}
			return extra, profiles
		},
		adminhttp.FanoutStorageDelete,
	))
	router.Get("/storage/temp/{key}", userdata.StorageTempGetHandler())
	router.Post("/storage/temp/{key}", userdata.StorageTempSetHandler())
	router.Get("/migrate/check", userdata.MigrateCheckHandler())
	router.Post("/migrate/from-uid", userdata.MigrateFromUIDHandler())
	router.Post("/migrate/from-server", userdata.MigrateFromServerHandler())
	nws := newNwsHub()
	SetGlobalNwsHub(nws)
	go nws.monitorConnections(cfg.WebSocket.InactiveAfterMinutes)
	router.HandleFunc("/nws", nws.HandleNWS)
	router.Get("/rch/check/connected", rchCheckConnectedHandler())
	router.Post("/rch/result", rchResultHandler())
	router.Post("/rch/gzresult", rchGzipResultHandler())
	router.Get("/cors/check", corsCheckHandler())
	router.Get("/corseu", corseuGetHandler())
	router.Post("/corseu", corseuPostHandler())
	router.Get("/corseu/{token}/*", corseuTokenGetHandler())
	router.Get("/cmd/{key}/*", cmdHandler())
	router.Get("/media", userdata.MediaGetHandler(proxyLinks))
	router.Post("/media", userdata.MediaPostHandler(proxyLinks))
	router.Get("/media/rsize/{token}/{width}/{height}/*", userdata.MediaRsizeHandler(proxyLinks))
	router.Get("/media/{type}/{token}/*", userdata.MediaTypeHandler(proxyLinks))

	// Internal API for custom balancer subprocesses to create proxy stream URLs.
	router.Post("/api/proxystream", internalProxyStreamHandler(proxyLinks))
	router.Get("/player-inner/*", playerInnerHandler())
	router.Get("/stats/browser/context", openstatBrowserContextHandler())
	router.Get("/stats/rch", openstatRchHandler())
	router.Get("/stats/request", openstatRequestsHandler())
	router.Get("/stats/tempdb", openstatTempDBHandler())
	router.Get("/weblog", weblogHandler())
	router.Get("/extensions", extensionsHandler(cfg.Compat.RepoRoot, customPlugins))
	router.Get("/externalids", externalIDsHandler(cfg.Compat.RepoRoot))
	router.Get("/app.min.js", appMinJSHandler(cfg, manifest))
	router.Get("/css/app.css", appCSSHandler(cfg))
	router.Get("/personal.lampa", personalLampaHandler())
	router.HandleFunc("/lampa-main/personal.lampa", personalLampaHandler())
	router.HandleFunc("/{myfolder}/personal.lampa", personalLampaHandler())
	router.HandleFunc("/{type}/app.min.js", appMinJSHandler(cfg, manifest))
	router.HandleFunc("/{type}/css/app.css", appCSSHandler(cfg))
	router.Get("/lampainit.js", lampainitJSHandler(cfg, manifest, customPlugins))
	router.Get("/privateinit.js", privateInitJSHandler(cfg, manifest))
	router.Get("/startpage.js", startpageJSHandler(cfg))
	router.Get("/msx/start.json", msxStartJSONHandler(cfg))

	// Community store plugin (client-side Lampa plugin).
	router.Get("/community_store.js", genericPluginJSHandler("community_store.js", "", cfg))

	// Community plugins public API for Lampa client.
	router.Get("/api/community-plugins", communityStoreListHandler(customPlugins, commCron))
	router.Post("/api/community-plugins", communityStoreActionHandler(customPlugins, commCron, tgTokenStore))

	// Community plugins catalog (local file, served for self-hosted catalogs).
	router.Get("/community-plugins.json", func(w http.ResponseWriter, r *http.Request) {
		candidates := []string{
			filepath.Join(cfg.Compat.RepoRoot, "community-plugins.json"),
			"community-plugins.json",
			filepath.Join(cfg.Compat.RepoRoot, "config", "community-plugins.json"),
		}
		for _, p := range candidates {
			data, err := os.ReadFile(p)
			if err == nil {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.Header().Set("Cache-Control", "public, max-age=300")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(data)
				return
			}
		}
		writePlain(w, http.StatusNotFound, "catalog not found")
	})
	router.Get("/samsung.wgt", samsungWGTHandler(cfg))
	router.Get("/webos.ipk", webosIPKHandler(cfg))
	router.Get("/testaccsdb", testAccsdbHandler())
	router.Post("/testaccsdb", testAccsdbHandler())
	router.Get("/invc-rch.js", invcRchJSHandler(cfg))
	router.Get("/invc-ws.js", invcWsJSHandler(cfg))
	router.HandleFunc("/invc-ws/js/{token}", invcWsJSHandler(cfg))
	router.Get("/nws-client-es5.js", nwsClientJSHandler(cfg))
	router.HandleFunc("/js/nws-client-es5.js", nwsClientJSHandler(cfg))
	router.Get("/signalr-6.0.25_es5.js", signalrES5Handler(cfg))
	router.Get("/e/acb", errorAccsdbHandler())
	sisihttp.RegisterRoutes(router, cfg, sisihttp.Deps{
		LiveConfig:     liveConfig,
		ServerReady:    serverReady,
		KitAuth:        kitAuthFromRequest,
		ClientIP:       clientIP,
		ReadFileAny:    readFileAny,
		SisiSources:    liveSisiSources,
		ProxyLinks:     liveProxyLinks,
		GetTorrsServer: getTorrsServer,
	}, kitStore, tgBot, sisiSrcMgr, proxyLinks)
	bkitStore := registerKitRoutes(router, cfg, kitStore, tgTokenStore, tgLangStore)
	registerRemoteControlRoutes(router, cfg, tgTokenStore)
	registerTGAuthRoutes(router, cfg, tgPending, tgDevicePending, tgTokenStore, promoStore, banStore, opts.Version, cubAuthVal)
	// Password / anon-mode endpoints — always registered; the handlers
	// themselves enforce the active auth.mode at request time.
	registerPasswordAuthRoutes(router, pwUserStore, func() *config.Config {
		if c := liveConfigGen(); c != nil {
			return c
		}
		c := cfg
		return &c
	})

	// /api/auth/whoami — generic auth-status probe used by the account.js
	// client plugin to render the "Settings → Аккаунт" tab. Returns the
	// resolved user from the middleware regardless of source (TG / pw /
	// anon) plus the active auth mode.
	router.Get("/api/auth/whoami", apiAuthWhoamiHandler(authModeStore.Get))

	// /api/auth/logout — full logout for the account.js "Выйти" button.
	// Removes the device record server-side so a subsequent ?uid=<bound>
	// request doesn't silently re-auth. Accepts both GET and POST so
	// older WebViews that block credentialed POSTs still work.
	logoutFullFn := apiAuthLogoutFullHandler(tgTokenStore, pwUserStore, anonIssuer)
	router.Get("/api/auth/logout", logoutFullFn)
	router.Post("/api/auth/logout", logoutFullFn)

	// /api/auth/cub/unlink + /api/auth/cub/link — manage the CUB recovery
	// anchor (account.js "Привязка CUB" row; the bot profile button uses the
	// same store calls). Unlink sets an opt-out so the passive learner
	// doesn't immediately re-link; link is explicit and clears it.
	cubUnlinkFn := apiAuthCubUnlinkHandler(tgTokenStore)
	router.Get("/api/auth/cub/unlink", cubUnlinkFn)
	router.Post("/api/auth/cub/unlink", cubUnlinkFn)
	cubLinkFn := apiAuthCubLinkHandler(tgTokenStore, cubAuthVal)
	router.Get("/api/auth/cub/link", cubLinkFn)
	router.Post("/api/auth/cub/link", cubLinkFn)

	// /account.js — the client-side Settings → Аккаунт plugin. Embedded
	// in the on.js plugin list (see buildOnPlugins). Plain static
	// substitution like audiobot.js — {localhost} → host.
	router.Get("/account.js", genericPluginJSHandler("account.js", "", cfg))

	skipDB := skiphttp.RegisterRoutes(router, cfg, skiphttp.Deps{AdminAuthCheck: tgAdminAuthCheck})
	// Авторизация мини-аппа для /api/kit/calendar/* — то же, что у IPTV: подпись
	// initData проверяет только хост.
	calendarhttp.SetKitResolver(func(r *http.Request) int64 {
		tgID, _, err := kitAuthFromRequest(r, liveConfig(cfg))
		if err != nil {
			return 0
		}
		id, perr := strconv.ParseInt(tgID, 10, 64)
		if perr != nil {
			return 0
		}
		return id
	})
	calStore, calCron := calendarhttp.RegisterRoutes(router, cfg, calendarhttp.Deps{AdminAuthCheck: tgAdminAuthCheck}, tmdbPoolInst, tgTokenStore, tgBot)
	if calCron != nil {
		calCron.SetVoiceLister(calendarVoiceLister(cfg, proxyLinks, dynRoutes))
		// Live delivery on top of the persisted inbox. nwsBroadcast already knows
		// how to expand a "tg:<id>" pseudo-uid into every device bound to that
		// Telegram account, which is exactly the fan-out a notification wants —
		// no separate tgID→uid mapping needed.
		calCron.SetAppDelivery(calendarhttp.Inbox(), func(tgID int64, n calendar.Notification) {
			nwsBroadcast("", "tg:"+strconv.FormatInt(tgID, 10), "calendar_notify", n)
		})
	}
	_ = registerXSearchRoutes(router, cfg, tmdbPoolInst)
	collectionshttp.RegisterRoutes(router, cfg, tmdbPoolInst)
	// Авторизация мини-аппа для /api/kit/iptv/* — проверять подпись initData умеет
	// только хост, поэтому iptvhttp получает её хуком. Ставим ДО регистрации роутов,
	// чтобы между ними не было окна, когда они отвечают 401.
	iptvhttp.SetKitResolver(func(r *http.Request) int64 {
		tgID, _, err := kitAuthFromRequest(r, liveConfig(cfg))
		if err != nil {
			return 0
		}
		// Мини-апп всегда открыт из Telegram, поэтому tgID — число. Гостевые
		// bkit:-сессии сюда не подходят: их id не отображается в тот же ключ,
		// которым подписаны плейлисты.
		id, perr := strconv.ParseInt(tgID, 10, 64)
		if perr != nil {
			return 0
		}
		return id
	})
	iptvhttp.RegisterRoutes(router, cfg, iptvhttp.Deps{LiveConfig: liveConfig, ServerReady: serverReady, ClientIP: clientIP}, tgTokenStore, proxyLinks, transSvc)
	registerWinkRoutes(router, cfg, proxyLinks)
	opensubs.RegisterRoutes(router, cfg)

	// Public homepage + privacy policy (/about, /privacy). Required by
	// Google OAuth consent-screen verification for the YouTube scope —
	// the reviewer needs a login-free homepage that links to a privacy
	// policy. See oauth_verification_pages.go.
	registerOAuthVerificationRoutes(router)

	// Generic music JS source dispatcher (peer to /sisi/cust/{name}). Each
	// goja module rooted at music_sources/{name}/ handles its own search /
	// charts / play actions — community-supplied modules.
	musicSourceProxy := musicCustomHandler(musicSrcMgr, proxyLinks)
	router.Get("/music/{name}", musicSourceProxy)
	router.Get("/music/{name}/*", musicSourceProxy)

	_, _, adminIDStore := registerAdminRoutes(router, adminRouteDeps{
		cfg:           cfg,
		tgTokenStore:  tgTokenStore,
		tgBot:         tgBot,
		kitStore:      kitStore,
		custBalPool:   custBalPool,
		dynRoutes:     dynRoutes,
		jsMgr:         jsMgr,
		sisiSources:   sisiSrcMgr,
		musicSources:  musicSrcMgr,
		envPresets:    envPresetsMgr,
		customPlugins: customPlugins,
		commCron:      commCron,
		groupStore:    groupStoreRef,
		promoStore:    promoStore,
		banStore:      banStore,
		memberChecker: memberChecker,
		bkitStore:     bkitStore,
		skipDB:        skipDB,
		calStore:      calStore,
		calCron:       calCron,
		pwStore:       pwStore,
		pwUserStore:   pwUserStore,
		authModeStore: authModeStore,
		anonIssuer:    anonIssuer,
		webauthnStore: webauthnStore,
		sessionSecret: sessionSecret,
		tmdbCacheInst: tmdbCacheInst,
		tmdbPoolInst:  tmdbPoolInst,
		proxyLinks:    proxyLinks,
		proxyAPI:      proxyAPIHandler,
		logBuffer:     opts.LogBuffer,
		hasLLM:        cfg.LLM.Endpoint != "",
	})
	adminIDStoreRef = adminIDStore // resolver unmasks «Сервер N» → balancer name for admins only

	// Public route for custom plugin images (no auth needed for Lampa clients).
	router.Get("/customplugins/image/{filename}", adminhttp.CustomPluginImageHandler(customPlugins))

	router.Get("/", lampaIndexHandler(cfg))

	// Reverse proxy for CDN player API calls (/bnsi/*, /ws/*).
	// The iframe CDN player (stloadi.live) calls these absolute paths,
	// and since the iframe loads from our origin, requests come to us.
	// CDN stream proxy — rewrites Origin/Referer for CDN segment requests.
	// Used by the iframe CDN player when segment URLs are rewritten to go
	// through our server (to fix Origin header mismatch).
	router.Route("/cdn-proxy", func(r chi.Router) {
		cdnProxy := litesrc.NewCDNStreamProxy(cfg)
		r.Get("/*", cdnProxy)
		r.Post("/*", cdnProxy)
		r.Options("/*", cdnProxy)
	})

	allohaPlayerProxy := http.HandlerFunc(litesrc.NewAllohaPlayerAPIProxy(cfg))
	router.Mount("/bnsi", http.StripPrefix("/bnsi", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = "/bnsi" + r.URL.Path // restore full path
		allohaPlayerProxy.ServeHTTP(w, r)
	})))
	router.Mount("/images", http.StripPrefix("/images", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = "/images" + r.URL.Path
		allohaPlayerProxy.ServeHTTP(w, r)
	})))

	router.Get("/lite/events", liteEventsHandler(cfg, proxyLinks, dynRoutes))
	router.Get("/lifeevents", lifeEventsHandler(cfg))
	liteH := liteSourceHandler(cfg, proxyLinks, dynRoutes)
	router.Get("/lite/*", liteH)
	// HLS players (and some CDNs) probe segments/manifests with HEAD; without this they get 405 and
	// playback stalls (e.g. /lite/youtube/mux/seg). Net/http strips the body for HEAD automatically.
	router.Head("/lite/*", liteH)

	// Host hooks for the userdata handlers (/timecode, /bookmark, /storage).
	userdata.SetDeps(userdata.Deps{
		ClientIP:     clientIP,
		ReadFileAny:  readFileAny,
		NwsBroadcast: nwsBroadcast,
	})

	// /wasm/* serves the Lampa-side runtime catalogue + .wasm assets so the
	// Lampa loader plugin can fetch client/both modules at runtime.
	if wasmMgr != nil {
		router.Get("/wasm/*", wasmMgr.ClientAssetHandler().ServeHTTP)
		// WASM Studio — admin UI compiles plugins in-place via tinygo/cargo and
		// loads the resulting .wasm into the running server. These endpoints
		// therefore MUST be admin-gated. They are NOT under the /{adminPath}
		// namespace, so they get NO auth from registerAdminRoutes — we wrap
		// each with tgAdminAuthCheck here explicitly. (A prior comment wrongly
		// claimed the admin router covered them; it did not → unauth RCE.)
		if studio, sErr := wasmmodules.NewStudio(wasmMgr); sErr == nil {
			studioH := studio.StudioHTTP()
			studioGuard := func(w http.ResponseWriter, r *http.Request) {
				if _, _, ok := tgAdminAuthCheck(w, r, tgTokenStore, adminIDStore); !ok {
					return // tgAdminAuthCheck already wrote the 401/redirect
				}
				studioH.ServeHTTP(w, r)
			}
			router.Post("/admin/api/wasm-studio/build", studioGuard)
			router.Post("/admin/api/wasm-studio/install", studioGuard)
			router.Get("/admin/api/wasm-studio/template", studioGuard)
		} else {
			log.Warn().Err(sErr).Msg("wasmmodules: studio init failed")
		}
	}
	router.Get("/vibix_m3u8/*", http.HandlerFunc(litesrc.VibixM3U8Handler))
	router.Get("/vibix_embed/*", http.HandlerFunc(litesrc.VibixEmbedHandler))

	// Wire YouTube OAuth if configured.
	if cfg.YouTubeOAuth.ClientID != "" && cfg.YouTubeOAuth.ClientSecret != "" {
		oauthCfg := ytauth.OAuthConfig{
			ClientID:     cfg.YouTubeOAuth.ClientID,
			ClientSecret: cfg.YouTubeOAuth.ClientSecret,
		}
		ytStore := ytauth.NewStore(cfg.Compat.RepoRoot)
		ytAPI := ytauth.NewAPIClient(ytStore, oauthCfg)
		ytProvider := ytauth.NewProvider(ytStore, ytAPI, oauthCfg)
		if yt := litesrc.GetGlobalYTChecker(); yt != nil {
			yt.SetAuth(ytAPI, tgTokenStore)
		}
		if tgBot != nil {
			tgBot.SetYouTubeAuth(ytProvider)
		}
		// Same linking flow as the bot's /youtube_auth, driven by a button in
		// the mini-app so nobody has to type a command and copy a code out of
		// a chat message.
		registerKitYouTubeRoutes(router, cfg, ytProvider)
		log.Info().Msg("youtube oauth: enabled")
	}

	// ProxyCore bot integration — set after the server is bound (via lazy bridge).
	if tgBot != nil {
		tgBot.SetProxyCoreManager(&proxyCoreManagerBridge{})
	}

	// JacRed torrent search proxy (routes requests to external jacred API).
	// Always registered: lampainit points every client's jackett_url at THIS
	// server (resolveJacHost), so these paths must answer regardless of
	// whether Parser.JacRedHost is customised — the handler falls back to the
	// default upstream when the config value is empty. Registering them
	// conditionally used to leave clients with a 404 parser.
	{
		jacProxy := jacredProxyHandler(cfg)
		jacCORS := jacredCORSHandler()
		router.Get("/api/v2.0/indexers/{status}/results", jacProxy)
		router.Get("/api/v1.0/torrents", jacProxy)
		router.Get("/api/v1.0/conf", jacProxy)

		// Native rutracker resolver — registered before the generic /parse/*
		// pass-through so our own parselinks are served locally instead of
		// being forwarded to jacred (which knows nothing about them).
		rtParse := rutrackerParseHandler(cfg)
		router.HandleFunc("/parse/rutracker/{id}", rtParse)
		router.HandleFunc("/parse/rutracker", rtParse)

		router.HandleFunc("/parse/*", jacProxy)
		router.Method("OPTIONS", "/api/v2.0/indexers/{status}/results", jacCORS)
		router.Method("OPTIONS", "/api/v1.0/torrents", jacCORS)
		router.Method("OPTIONS", "/api/v1.0/conf", jacCORS)

		// Web UI of the *local* jacred instance (search/stats pages) — same
		// idea as /ts for TorrServer. Deliberately NOT on gatePreAuthAllowed,
		// so it sits behind the auth gate; answers 503 when jacred_local off.
		jacWeb := jacredWebUIHandler()
		router.HandleFunc("/jacred", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/jacred/", http.StatusMovedPermanently)
		})
		router.HandleFunc("/jacred/*", jacWeb)
	}

	// CUB secuses endpoint — Lampa calls this during init.
	// Return valid stub so app.min.js sendSecuses() doesn't crash.
	// Lampa beta expects "secuses" as array, "lgbt" as array (content filter by law).
	secusesResp := map[string]any{"secuses": []any{}, "lgbt": []any{}, "translations": map[string]any{}}
	router.Get("/api/v1.0/events/secuses", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, secusesResp)
	})
	router.Post("/api/v1.0/events/secuses", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, secusesResp)
	})

	router.Get("/version", versionHandler)
	router.Get("/ping", pingHandler)

	// Fallback strategy for parity migration.
	router.NotFound(notFoundHandler(cfg, customPlugins))
	router.MethodNotAllowed(methodNotAllowedHandler())

	// Collect after route registration to expose the actual local surface.
	localRoutes := collectLocalRoutes(router)
	router.Get("/_lampac/routes/local", localRoutesHandler(localRoutes))

	httpServer := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           router,
		ReadTimeout:       30 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    8192, // 8 KB — reject oversized headers early
	}

	// --- Self-update service (admin panel "Обновления" tab) ---
	updaterSvc := updater.New(
		buildUpdaterConfig(cfg, opts.Version),
		opts.Version,
		opts.Commit,
		opts.BuildDate,
	)

	s := &Server{
		httpServer:    httpServer,
		addr:          cfg.Server.Addr,
		version:       opts.Version,
		commit:        opts.Commit,
		buildDate:     opts.BuildDate,
		updater:       updaterSvc,
		cfg:           cfg,
		localRoutes:   localRoutes,
		tgBot:         tgBot,
		proxyLinks:    proxyLinks,
		proxyMgr:      proxyMgr,
		proxyPool:     proxyPool,
		syncCron:      NewSyncCron(cfg),
		transSvc:      transSvc,
		healthChecker: balancerhealth.NewHealthChecker(),
		balancerStats: newBalancerStatsManager(cfg),
		adminIDStore:  adminIDStore,
		custBalPool:   custBalPool,
		jsModules:     jsMgr,
		sisiSources:   sisiSrcMgr,
		musicSources:  musicSrcMgr,
		wasmModules:   wasmMgr,
		dynamicRoutes: dynRoutes,
		nwsHub:        nws,
		cacheCron:     NewCacheCron(cfg),
		lampaCron:     NewLampaCron(cfg),
		dlnaCoverGen:  dlnaCG,
		dlnaTorrent:   dlnaTM,
		dlnaUPnP:      dlnaUPnP,
		tmdbCache:     tmdbCacheInst,
		tmdbPool:      tmdbPoolInst,
		calendarCron:  calCron,
		clusterPool:   clusterPool,
		clusterFwd:    clusterFwd,
		clusterStore:  clusterStore,

		tsBalancerPool:  tsBalancerPool,
		tsBalancerStore: tsBalancerStore,
		antiDPI:         antiDPIServer,
		zapret:          zapretMgr,
		jacredMgr:       jacredMgr,
		rutracker:       rutrackerClient,
		memberChecker:   memberChecker,
		proxyCorePool:   proxyCorePool,
	}
	s.cfgPtr.Store(&cfg) // initialize atomic config pointer
	// Apply TOML-configured healthcheck tunables, then layer admin-saved
	// overrides on top. Restore auto-disable state from the last run BEFORE
	// publishing the checker as global — otherwise a tiny window exists
	// where a previously-disabled balancer would briefly serve traffic.
	s.healthChecker.ApplySettings(balancerhealth.HealthSettings{
		Enabled:          cfg.HealthCheck.Enabled,
		IntervalSec:      cfg.HealthCheck.IntervalSec,
		TimeoutSec:       cfg.HealthCheck.TimeoutSec,
		FailThreshold:    cfg.HealthCheck.FailThreshold,
		RecoverThreshold: cfg.HealthCheck.RecoverThreshold,
	})
	if override, ok := balancerhealth.LoadHealthcheckSettingsOverride(cfg.Compat.RepoRoot); ok {
		s.healthChecker.ApplySettings(override)
	}
	s.healthChecker.LoadFromDisk(cfg.Compat.RepoRoot)
	// Inject the probe-target resolver (reads the live config + plugin registry);
	// the checker itself is a leaf package with no httpapi dependency.
	s.healthChecker.SetTargets(healthcheckTargets)
	balancerhealth.SetGlobalHealthChecker(s.healthChecker)
	SetGlobalBalancerStats(s.balancerStats)
	bindServer(s) // publish the running Server to the deps.go accessors

	// If "telegram" is registered as a proxied balancer, route TG bots through it.
	s.updateTelegramProxy()

	// External-access Basic Auth state (used by tsExternalAuthMiddleware).
	reloadTSExternalAuth(cfg)

	// Optional dedicated listener for third-party TorrServer clients.  When
	// configured it shares the same chi router so the routes table is the
	// canonical one.  Operators expose this port over LAN only — the cookie-
	// based admin / lampa frontend stays on the main port.
	if addr := strings.TrimSpace(cfg.TorrServer.ExternalAccess.ListenAddr); addr != "" {
		s.tsExternalSrv = &http.Server{
			Addr:              addr,
			Handler:           router,
			ReadTimeout:       30 * time.Second,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
			MaxHeaderBytes:    8192,
		}
	}

	// Let the self-updater flush in-flight HTTP responses before it re-execs.
	// Wired here (before StartBackground launches RunAutoCheck) so the write
	// happens-before any background restart — no data race on the hook.
	if s.updater != nil {
		s.updater.SetRestartDrainer(s.drainHTTPForRestart)
	}

	return s, nil
}

// drainHTTPForRestart flushes in-flight HTTP requests just before the
// self-updater re-execs the process. execve replaces the image without waiting
// for active connections, so a client mid-download of a plugin (e.g.
// wasm_loader.js) would get a truncated body — and a partial gzip/chunked
// stream decompresses to garbage, making old WebKit (LG webOS) throw
// "Unexpected token ILLEGAL". We only flush the HTTP listeners here; execve
// tears down everything else anyway. New requests briefly 502 at the nginx
// upstream during the swap, which is a clean, retryable failure unlike corrupt
// JS. (A future improvement could hand the listening socket to the child to
// erase even that window, but updates are gated to the maintenance window and
// PreventDuringStreams, so the simple drain is sufficient.)
func (s *Server) drainHTTPForRestart() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.httpServer.Shutdown(ctx); err != nil {
		log.Warn().Err(err).Msg("selfupdate: HTTP drain timed out before re-exec")
	}
	if s.tsExternalSrv != nil {
		tctx, tcancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer tcancel()
		_ = s.tsExternalSrv.Shutdown(tctx)
	}
}

func (s *Server) ListenAndServe() error {
	if s.tsExternalSrv != nil {
		go func() {
			log.Info().Str("addr", s.tsExternalSrv.Addr).
				Msg("ts-external: dedicated listener up")
			if err := s.tsExternalSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Error().Err(err).Msg("ts-external: listener crashed")
			}
		}()
	}
	return s.httpServer.ListenAndServe()
}

// StartBackground launches background goroutines (e.g. Telegram bot).
func (s *Server) StartBackground(ctx context.Context) {
	// Browser profiles left in /tmp by a previous run: a hard stop (restart,
	// OOM kill) skips every deferred cleanup, so each one strands a Chrome
	// profile worth tens to hundreds of MB. Sweep the orphans now and keep
	// sweeping hourly for anything a wedged browser leaves behind.
	startBrowserTmpSweeper(ctx)

	if s.tgBot != nil {
		s.tgBot.Start(ctx)
	}
	if s.memberChecker != nil {
		s.memberChecker.Start(ctx)
	}
	if s.syncCron != nil {
		s.syncCron.Start()
	}
	if s.healthChecker != nil {
		s.healthChecker.Start(ctx)
	}
	if s.balancerStats != nil {
		go s.balancerStats.Run(ctx)
	}
	// Build the alert engine now that the server is bound and s.adminIDStore is set.
	if s.alertEngine == nil {
		wireBalancerStatsAlerts(s, s.Cfg(), s.tgBot, s.adminIDStore)
	}
	if s.alertEngine != nil {
		s.alertEngine.Start(ctx)
	}
	if s.cacheCron != nil {
		s.cacheCron.Start()
	}
	if s.lampaCron != nil {
		s.lampaCron.Start()
	}
	if s.updater != nil {
		stop := make(chan struct{})
		go func() {
			<-ctx.Done()
			close(stop)
		}()
		go s.updater.RunAutoCheck(stop)
	}
	if s.dlnaCoverGen != nil {
		s.dlnaCoverGen.Start()
	}
	if s.dlnaUPnP != nil {
		s.dlnaUPnP.Start(ctx)
	}
	if s.cfg.DLNA.Enable && s.cfg.DLNA.AutoUpdateTrackers {
		go s.trackerUpdateLoop(ctx)
	}
	if s.calendarCron != nil {
		s.calendarCron.Start(ctx)
	}
	if s.clusterPool != nil {
		s.clusterPool.Start()
	}
	s.startCleanupWorker(ctx)

	// Start custom balancer subprocesses and register their routes.
	if s.custBalPool != nil {
		if err := s.custBalPool.ScanAndStart(); err != nil {
			log.Warn().Err(err).Msg("custom balancers: scan failed")
		}
		for name, addr := range s.custBalPool.RouteMap() {
			s.dynamicRoutes.Register(name, addr)
		}
		// Register quality badges for custom balancers.
		for _, st := range s.custBalPool.List() {
			badge := strings.TrimSpace(st.QualityBadge)
			if badge != "" && badge != "<nil>" {
				pluginQualityBadgeSet(st.Name, badge)
			}
		}
	}

	// Scan JS modules from disk; each module auto-registers via OnChange.
	if s.jsModules != nil {
		if err := s.jsModules.Scan(); err != nil {
			log.Warn().Err(err).Msg("jsmodules: scan failed")
		} else {
			log.Info().Int("count", len(s.jsModules.List())).Msg("jsmodules: loaded")
		}
	}

	// Scan SISI JS sources.
	if s.sisiSources != nil {
		if err := s.sisiSources.Scan(); err != nil {
			log.Warn().Err(err).Msg("sisi-sources: scan failed")
		} else {
			log.Info().Int("count", len(s.sisiSources.List())).Msg("sisi-sources: loaded")
		}
	}

	// Scan music JS sources.
	if s.musicSources != nil {
		if err := s.musicSources.Scan(); err != nil {
			log.Warn().Err(err).Msg("music-sources: scan failed")
		} else {
			log.Info().Int("count", len(s.musicSources.List())).Msg("music-sources: loaded")
		}
	}

	// Scan WASM modules; behaves identically to jsmodules above.
	if s.wasmModules != nil {
		if err := s.wasmModules.Scan(); err != nil {
			log.Warn().Err(err).Msg("wasmmodules: scan failed")
		} else {
			log.Info().Int("count", len(s.wasmModules.List())).Msg("wasmmodules: loaded")
		}
		// Hot-reload: re-Scan whenever a manifest or .wasm file changes on
		// disk. Lifetime tied to the server (no explicit Close — process exit
		// reaps it).
		if _, err := wasmmodules.NewWatcher(s.wasmModules); err != nil {
			log.Warn().Err(err).Msg("wasmmodules: watcher failed (hot-reload disabled)")
		}
	}
}

// trackerUpdateLoop periodically fetches best tracker lists from public sources
// and saves them to cache/trackers.txt for DLNA torrent magnet enrichment.
func (s *Server) trackerUpdateLoop(ctx context.Context) {
	interval := time.Duration(s.cfg.DLNA.IntervalUpdateTrack) * time.Minute
	if interval < 30*time.Minute {
		interval = 90 * time.Minute
	}

	// Initial fetch after short delay.
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()

	trackerPath := filepath.Join(s.cfg.Compat.RepoRoot, "cache", "trackers.txt")

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			trackers := dlna.FetchBestTrackers(30 * time.Second)
			if len(trackers) > 0 {
				if err := dlna.SaveTrackers(trackerPath, trackers); err != nil {
					log.Warn().Err(err).Msg("dlna-trackers: save failed")
				}
			}
			timer.Reset(interval)
		}
	}
}

// startCleanupWorker periodically cleans up proxylink cache and returns
// memory to the OS to keep RSS low under sustained load.
func (s *Server) startCleanupWorker(ctx context.Context) {
	go func() {
		cleanTicker := time.NewTicker(60 * time.Second)
		freeTicker := time.NewTicker(5 * time.Minute)
		defer cleanTicker.Stop()
		defer freeTicker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-cleanTicker.C:
				if s.proxyLinks != nil {
					removed := s.proxyLinks.Cleanup()
					if removed > 0 {
						log.Debug().Int("removed", removed).Int("remaining", s.proxyLinks.Len()).Msg("proxylink cleanup")
					}
				}
			case <-freeTicker.C:
				debug.FreeOSMemory()
			}
		}
	}()
}

func (s *Server) Shutdown(ctx context.Context) error {
	// Fold in-flight playback sessions before anything else: a restart during
	// prime time would otherwise discard every session currently being watched.
	FlushCMCDStats()
	if s.alertEngine != nil {
		s.alertEngine.Stop()
	}
	if s.healthChecker != nil {
		s.healthChecker.Stop()
	}
	if s.transSvc != nil {
		s.transSvc.Stop()
	}
	if s.syncCron != nil {
		s.syncCron.Stop()
	}
	if s.cacheCron != nil {
		s.cacheCron.Stop()
	}
	if s.lampaCron != nil {
		s.lampaCron.Stop()
	}
	if s.dlnaCoverGen != nil {
		s.dlnaCoverGen.Stop()
	}
	if s.dlnaUPnP != nil {
		s.dlnaUPnP.Stop()
	}
	if s.dlnaTorrent != nil {
		s.dlnaTorrent.Close()
	}
	if s.tmdbCache != nil {
		s.tmdbCache.Stop()
	}
	if s.tmdbPool != nil {
		s.tmdbPool.Stop()
	}
	if s.calendarCron != nil {
		s.calendarCron.Stop()
	}
	if s.clusterPool != nil {
		s.clusterPool.Stop()
	}
	if s.tsBalancerPool != nil {
		s.tsBalancerPool.Stop()
	}
	if s.antiDPI != nil {
		s.antiDPI.Stop()
	}
	if s.zapret != nil {
		s.zapret.Stop()
	}
	if s.jacredMgr != nil {
		s.jacredMgr.Stop()
	}
	if s.memberChecker != nil {
		s.memberChecker.Stop()
	}
	if s.tgBot != nil {
		s.tgBot.Stop()
	}
	if s.proxyPool != nil {
		s.proxyPool.StopAll()
	} else if s.proxyMgr != nil {
		s.proxyMgr.Stop()
	}
	if s.custBalPool != nil {
		s.custBalPool.StopAll()
	}
	shutdownTSRoutes()
	if s.tsExternalSrv != nil {
		_ = s.tsExternalSrv.Shutdown(ctx)
	}
	return s.httpServer.Shutdown(ctx)
}

func (s *Server) Addr() string {
	return s.addr
}

// ReloadProxies stops the current proxy pool, re-reads config, and starts new
// sidecar processes. This allows changing proxy URIs / balancers without restarting
// the entire server.
func (s *Server) ReloadProxies() error {
	// 1. Stop existing pool.
	if s.proxyPool != nil {
		s.proxyPool.StopAll()
		s.proxyPool = nil
	} else if s.proxyMgr != nil {
		s.proxyMgr.Stop()
	}
	s.proxyMgr = nil

	// 2. Clear httpclient registry.
	httpclient.ClearRegistry()

	// 3. Re-read config.
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("reload proxies: load config: %w", err)
	}
	s.cfg = cfg
	s.cfgPtr.Store(&cfg)

	// 4. Start new pool.
	if len(cfg.Proxy.Vless.Entries) > 0 {
		binDir := filepath.Join(cfg.Compat.RepoRoot, "bin")
		var poolConfigs []sidecar.PoolConfig
		for _, entry := range cfg.Proxy.Vless.Entries {
			poolConfigs = append(poolConfigs, sidecar.PoolConfig{
				URI:       entry.URI,
				Balancers: entry.Balancers,
				Label:     entry.Label,
				Engine:    entry.Engine,
			})
		}
		engines := map[sidecar.EngineType]sidecar.Engine{
			sidecar.EngineXray:   &sidecar.XrayEngine{},
			sidecar.EngineMihomo: &sidecar.MihomoEngine{},
		}
		pool, xerr := sidecar.NewPool(poolConfigs, binDir, 40000, engines, sidecaruri.Parse)
		if xerr != nil {
			return fmt.Errorf("reload proxies: start pool: %w", xerr)
		}
		s.proxyPool = pool
		for _, pe := range pool.Entries() {
			httpclient.RegisterProxiedBalancer(pe.Manager.SOCKSAddr(), pe.Balancers)
			if pe.Label != "" {
				httpclient.RegisterProxyLabel(pe.Label, pe.Manager.SOCKSAddr())
			}
			if s.proxyMgr == nil {
				s.proxyMgr = pe.Manager
			}
		}
		log.Info().Int("count", len(pool.Entries())).Msg("reload proxies: new pool started")
	} else {
		log.Info().Msg("reload proxies: no proxy entries configured")
	}

	// 5. Re-register direct proxies (SOCKS5/HTTP).
	for _, entry := range cfg.Proxy.Direct.Entries {
		if entry.Label != "" {
			if u, err := url.Parse(entry.URI); err == nil && strings.HasPrefix(strings.ToLower(u.Scheme), "socks5") {
				httpclient.RegisterProxyLabel(entry.Label, u.Host)
			}
		}
		if err := httpclient.RegisterDirectProxy(entry.URI, entry.Balancers); err != nil {
			log.Warn().Err(err).Str("uri", entry.URI).Msg("reload proxies: direct proxy failed")
		}
	}

	return nil
}

// ReloadProxyCore stops and restarts the built-in proxy engine pool.
func (s *Server) ReloadProxyCore() error {
	// 1. Stop existing proxycore pool.
	if s.proxyCorePool != nil {
		s.proxyCorePool.StopAll()
		s.proxyCorePool = nil
	}

	// 2. Re-read config.
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("reload proxycore: load config: %w", err)
	}
	s.cfgPtr.Store(&cfg)

	// 3. Start new proxycore pool if entries exist.
	if len(cfg.ProxyCore.Entries) > 0 {
		var poolEntries []proxycore.PoolEntry
		for _, entry := range cfg.ProxyCore.Entries {
			poolEntries = append(poolEntries, proxycore.PoolEntry{
				URI:       entry.URI,
				Label:     entry.Label,
				Balancers: entry.Balancers,
				Engine:    entry.Engine,
			})
		}

		basePort := cfg.ProxyCore.BasePort
		if basePort == 0 {
			basePort = 41000 // separate range from sidecar (40000+)
		}
		pcBinDir := filepath.Join(cfg.Compat.RepoRoot, "bin")
		pool, err := proxycore.NewPool(poolEntries, basePort, pcBinDir)
		if err != nil {
			return fmt.Errorf("reload proxycore: start pool: %w", err)
		}
		s.proxyCorePool = pool

		// Set routing rules if configured.
		if len(cfg.ProxyCore.Rules) > 0 {
			var rules []proxycore.RoutingRule
			for _, r := range cfg.ProxyCore.Rules {
				rules = append(rules, proxycore.RoutingRule{
					ID:        r.ID,
					Balancers: r.Balancers,
					ProxyID:   r.Proxy,
					Fallback:  r.Fallback,
					Mode:      proxycore.RoutingMode(r.Mode),
				})
			}
			pool.SetRules(rules)
		}

		// Resolve GeoIP in background.
		go pcResolveGeo(pool)

		log.Info().Int("count", len(pool.Entries())).Msg("proxycore: pool started")
	} else {
		log.Info().Msg("proxycore: no entries configured")
	}

	// Update TG bot proxy after ProxyCore reload.
	s.updateTelegramProxy()

	return nil
}

// updateTelegramProxy sets or clears a proxy transport on both TG bots based on
// whether "telegram" is registered as a proxied balancer.
func (s *Server) updateTelegramProxy() {
	t := httpclient.TransportForBalancer("telegram")
	if t != nil {
		client := &http.Client{Transport: t, Timeout: 60 * time.Second}
		if s.tgBot != nil {
			s.tgBot.SetHTTPClient(client)
		}
		log.Info().Msg("telegram: bot traffic routed through proxy")
	} else {
		client := httpclient.New(60 * time.Second)
		if s.tgBot != nil {
			s.tgBot.SetHTTPClient(client)
		}
	}
}

// Reload re-reads the configuration from all sources and restarts subsystems
// whose settings have changed. This is the primary hot-reload entry point,
// triggered by SIGHUP or POST /admin/api/reload.
//
// Subsystems restarted:
//   - Proxy pool (VLESS sidecar + direct proxies)
//   - Sync cron (if sync settings changed)
//   - Transcoding service (if ffmpeg/enable changed)
//
// Subsystems NOT restarted (would require server restart):
//   - HTTP listener address
//   - TG bot token / admin ID
//   - Auth middleware
func (s *Server) Reload() error {
	newCfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("reload: load config: %w", err)
	}

	oldCfg := s.Cfg()

	// Swap config atomically — handlers calling liveConfig() will see new values.
	s.cfg = newCfg
	s.cfgPtr.Store(&newCfg)

	// Re-apply browser engine selection. Live-swap of pools is not
	// supported in v1 — the new selection takes effect for new sessions,
	// running browsers keep their original engine until they exit.
	if newCfg.BrowserPool.Engine != oldCfg.BrowserPool.Engine ||
		!stringMapEqual(newCfg.BrowserPool.BalancerEngines, oldCfg.BrowserPool.BalancerEngines) {
		browser.Apply(newCfg.BrowserPool.Engine, newCfg.BrowserPool.BalancerEngines)
		log.Info().Str("engine", newCfg.BrowserPool.Engine).Msg("browser: engine selection reloaded")
	}
	applyBrowserEnginePersist(newCfg.Compat.RepoRoot)
	applyBrandingFromConfig(newCfg)

	// IPTV global playlists: the store is built once at startup, so push the
	// reloaded [iptv] global_playlists / merge_global into it here — otherwise
	// the client (which reads live config to decide IPTV is enabled) would show
	// "IPTV не настроен" until a full restart. No-op if IPTV was off at startup.
	iptvhttp.ApplyConfigReload(newCfg)

	log.Info().Msg("config reloaded")

	// --- FlareSolverr (URL + user-managed balancer list) ---
	// These have to be re-applied on every Reload because the httpclient
	// registry is process-global state, not derived from cfg on demand. If we
	// skipped this, an admin saving any other setting (proxy, TOML editor)
	// would silently revert FlareSolverr balancers and URL to whatever was on
	// disk at startup — the "при обновлении список слетает" bug.
	httpclient.SetFlareSolverrURL(strings.TrimSpace(newCfg.Online.FlareSolverr))
	httpclient.ApplyUserFlareSolverrBalancers(newCfg.Online.FlareSolverrBalancers)
	if s.jsModules != nil {
		s.jsModules.FlareSolverr = strings.TrimSpace(newCfg.Online.FlareSolverr)
	}
	if s.sisiSources != nil {
		s.sisiSources.FlareSolverr = strings.TrimSpace(newCfg.Online.FlareSolverr)
	}
	if s.musicSources != nil {
		s.musicSources.FlareSolverr = strings.TrimSpace(newCfg.Online.FlareSolverr)
	}

	// Push new updater config.
	if s.updater != nil {
		s.updater.Reload(buildUpdaterConfig(newCfg, s.version))
	}

	// --- proxylink AES key rotation ---
	// If shared_secret changed (typical: empty → set after first cluster
	// node add), re-derive AES key/IV so /proxy/<enc> tokens minted by
	// cluster peers can be decoded here. Existing tokens become invalid,
	// but active streams will re-resolve.
	if oldCfg.ProxyLink.SharedSecret != newCfg.ProxyLink.SharedSecret && s.proxyLinks != nil {
		if err := s.proxyLinks.Reload(proxylink.Options{
			CacheDir:     newCfg.ProxyLink.CacheDir,
			VerifyIP:     newCfg.ProxyLink.VerifyIP,
			EncryptAES:   newCfg.ProxyLink.EncryptAES,
			SharedSecret: newCfg.ProxyLink.SharedSecret,
		}); err != nil {
			log.Error().Err(err).Msg("reload: proxylink key reload failed")
		} else {
			log.Info().Bool("shared_secret_set", newCfg.ProxyLink.SharedSecret != "").Msg("reload: proxylink AES key rotated")
		}
	}

	// --- Proxy pool ---
	if proxyConfigChanged(oldCfg, newCfg) {
		log.Info().Msg("reload: proxy config changed, restarting proxy pool")
		// Stop existing pool.
		if s.proxyPool != nil {
			s.proxyPool.StopAll()
			s.proxyPool = nil
		} else if s.proxyMgr != nil {
			s.proxyMgr.Stop()
		}
		s.proxyMgr = nil
		httpclient.ClearRegistry()

		// Start new pool.
		if len(newCfg.Proxy.Vless.Entries) > 0 {
			binDir := filepath.Join(newCfg.Compat.RepoRoot, "bin")
			var poolConfigs []sidecar.PoolConfig
			for _, entry := range newCfg.Proxy.Vless.Entries {
				poolConfigs = append(poolConfigs, sidecar.PoolConfig{
					URI:       entry.URI,
					Balancers: entry.Balancers,
					Label:     entry.Label,
					Engine:    entry.Engine,
				})
			}
			engines := map[sidecar.EngineType]sidecar.Engine{
				sidecar.EngineXray:   &sidecar.XrayEngine{},
				sidecar.EngineMihomo: &sidecar.MihomoEngine{},
			}
			pool, xerr := sidecar.NewPool(poolConfigs, binDir, 40000, engines, sidecaruri.Parse)
			if xerr != nil {
				log.Error().Err(xerr).Msg("reload: proxy pool start failed")
			} else {
				s.proxyPool = pool
				for _, pe := range pool.Entries() {
					httpclient.RegisterProxiedBalancer(pe.Manager.SOCKSAddr(), pe.Balancers)
					if pe.Label != "" {
						httpclient.RegisterProxyLabel(pe.Label, pe.Manager.SOCKSAddr())
					}
					if s.proxyMgr == nil {
						s.proxyMgr = pe.Manager
					}
				}
				log.Info().Int("count", len(pool.Entries())).Msg("reload: proxy pool restarted")
			}
		}
		// Re-register direct proxies.
		for _, entry := range newCfg.Proxy.Direct.Entries {
			if entry.Label != "" {
				if u, err := url.Parse(entry.URI); err == nil && strings.HasPrefix(strings.ToLower(u.Scheme), "socks5") {
					httpclient.RegisterProxyLabel(entry.Label, u.Host)
				}
			}
			if err := httpclient.RegisterDirectProxy(entry.URI, entry.Balancers); err != nil {
				log.Warn().Err(err).Str("uri", entry.URI).Msg("reload: direct proxy failed")
			}
		}

		// Update TG bot proxy after all proxy registrations.
		s.updateTelegramProxy()
	}

	// --- AntiDPI ---
	if oldCfg.AntiDPI.Enable != newCfg.AntiDPI.Enable ||
		oldCfg.AntiDPI.Listen != newCfg.AntiDPI.Listen ||
		oldCfg.AntiDPI.Strategy != newCfg.AntiDPI.Strategy {
		log.Info().Msg("reload: antidpi config changed")
		if s.antiDPI != nil {
			s.antiDPI.Stop()
			s.antiDPI = nil
		}
		if newCfg.AntiDPI.Enable {
			listen := newCfg.AntiDPI.Listen
			if listen == "" {
				listen = "127.0.0.1:9898"
			}
			strategy := newCfg.AntiDPI.Strategy
			if strategy == "" {
				strategy = "auto"
			}
			srv := antidpi.New(listen, strategy, antidpi.DefaultHosts)
			if err := srv.Start(); err != nil {
				log.Error().Err(err).Msg("reload: antidpi failed to restart")
			} else {
				s.antiDPI = srv
				httpclient.RegisterProxiedBalancer(srv.Addr(), []string{"youtube"})
				log.Info().Str("addr", srv.Addr()).Msg("reload: antidpi restarted")
			}
		}
	}

	// --- Cluster pool ---
	if clusterConfigChanged(oldCfg, newCfg) {
		log.Info().Msg("reload: cluster config changed, restarting cluster pool")
		if s.clusterPool != nil {
			s.clusterPool.Stop()
			s.clusterPool = nil
			s.clusterFwd = nil
			s.clusterStore = nil
		}
		if newCfg.Cluster.Enable {
			validateClusterConfig(newCfg)
		}
		if newCfg.Cluster.Enable && newCfg.Cluster.Mode == "primary" {
			store, err := cluster.NewStore(newCfg.Compat.RepoRoot, newCfg.Cluster)
			if err != nil {
				log.Error().Err(err).Msg("reload: cluster store init failed")
			} else {
				s.clusterStore = store
			}
			s.clusterPool = cluster.NewPool(newCfg.Cluster, s.clusterStore)
			s.clusterFwd = cluster.NewForwarder(s.clusterPool)
			s.clusterPool.Start()
			log.Info().Int("nodes", len(s.clusterPool.Nodes())).Msg("reload: cluster pool restarted")
		}
	}

	// --- Sync cron ---
	if syncConfigChanged(oldCfg, newCfg) {
		log.Info().Msg("reload: sync config changed, restarting sync cron")
		if s.syncCron != nil {
			s.syncCron.Stop()
		}
		s.syncCron = NewSyncCron(newCfg)
		s.syncCron.Start()
	}

	// --- TorrServer proxy ---
	if torrserverConfigChanged(oldCfg, newCfg) {
		reloadTSProxy(newCfg)

		// Local↔external mode switch. The mode is decided once at startup
		// (initTorrs / initTorrServerProcess gate on TorrServer.URL) and
		// reloadTSProxy only rebuilds the proxy target — so without this a
		// local server that was started when URL was empty keeps running and
		// keeps stealing the transcode path (torrsIsInProcess() stays true).
		// When an external URL appears, stop the local server so external
		// actually takes over live; the /ts/* handlers were registered as
		// direct at startup (they're nil-safe → 503), so a restart is still
		// needed to convert them into proxy routes for external clients.
		wasExternal := strings.TrimSpace(oldCfg.TorrServer.URL) != ""
		isExternal := strings.TrimSpace(newCfg.TorrServer.URL) != ""
		switch {
		case !wasExternal && isExternal:
			shutdownTSRoutes() // Close in-process server + stop local binary.
			log.Warn().Str("url", strings.TrimSpace(newCfg.TorrServer.URL)).
				Msg("torrs: switched to external TorrServer — local server stopped; restart to convert /ts routes to proxy")
		case wasExternal && !isExternal:
			log.Warn().Msg("torrs: switched to local TorrServer — restart required to start the local server")
		}
	}
	// External-access creds / CIDR / listener changes are always re-applied
	// on reload — cheap and avoids edge cases with comparison helpers.
	reloadTSExternalAuth(newCfg)

	// --- Transcoding ---
	if transcodingConfigChanged(oldCfg, newCfg) {
		log.Info().Msg("reload: transcoding config changed")
		if s.transSvc != nil {
			s.transSvc.Stop()
			s.transSvc = nil
		}
		if newCfg.Transcoding.Enable {
			s.transSvc = transcodesvc.NewTranscodingService(newCfg)
			log.Info().Str("ffmpeg", newCfg.Transcoding.FFmpeg).Msg("reload: transcoding service restarted")
		}
	}

	return nil
}

// proxyConfigChanged returns true if proxy settings differ between old and new configs.
func proxyConfigChanged(old, new config.Config) bool {
	if len(old.Proxy.Vless.Entries) != len(new.Proxy.Vless.Entries) {
		return true
	}
	for i := range old.Proxy.Vless.Entries {
		if old.Proxy.Vless.Entries[i].URI != new.Proxy.Vless.Entries[i].URI {
			return true
		}
	}
	if len(old.Proxy.Direct.Entries) != len(new.Proxy.Direct.Entries) {
		return true
	}
	for i := range old.Proxy.Direct.Entries {
		if old.Proxy.Direct.Entries[i].URI != new.Proxy.Direct.Entries[i].URI {
			return true
		}
	}
	return false
}

// syncConfigChanged returns true if sync settings differ.
func syncConfigChanged(old, new config.Config) bool {
	return old.Sync.Enable != new.Sync.Enable ||
		old.Sync.Type != new.Sync.Type ||
		old.Sync.APIHost != new.Sync.APIHost
}

// transcodingConfigChanged returns true if transcoding settings differ.
func transcodingConfigChanged(old, new config.Config) bool {
	return old.Transcoding.Enable != new.Transcoding.Enable ||
		old.Transcoding.FFmpeg != new.Transcoding.FFmpeg ||
		old.Transcoding.MaxConcurrent != new.Transcoding.MaxConcurrent
}

func clusterConfigChanged(old, new config.Config) bool {
	if old.Cluster.Enable != new.Cluster.Enable ||
		old.Cluster.Mode != new.Cluster.Mode ||
		old.Cluster.APIKey != new.Cluster.APIKey ||
		len(old.Cluster.Nodes) != len(new.Cluster.Nodes) {
		return true
	}
	for i := range old.Cluster.Nodes {
		if old.Cluster.Nodes[i].Host != new.Cluster.Nodes[i].Host ||
			old.Cluster.Nodes[i].Weight != new.Cluster.Nodes[i].Weight {
			return true
		}
	}
	return false
}

func torrserverConfigChanged(old, new config.Config) bool {
	return old.TorrServer.URL != new.TorrServer.URL ||
		old.TorrServer.Port != new.TorrServer.Port ||
		old.TorrServer.Login != new.TorrServer.Login ||
		old.TorrServer.Password != new.TorrServer.Password
}

func collectLocalRoutes(router chi.Routes) []LocalRouteSpec {
	uniq := map[string]LocalRouteSpec{}
	_ = chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		method = strings.ToUpper(strings.TrimSpace(method))
		route = strings.TrimSpace(route)
		if method == "" || route == "" {
			return nil
		}
		key := method + " " + route
		uniq[key] = LocalRouteSpec{Method: method, Path: route}
		return nil
	})

	out := make([]LocalRouteSpec, 0, len(uniq))
	for _, r := range uniq {
		out = append(out, r)
	}
	return out
}

// buildUpdaterConfig maps the `[updater]` TOML section onto the runtime
// updater.Config, resolving the history file path against Compat.RepoRoot.
func buildUpdaterConfig(cfg config.Config, _ string) updater.Config {
	uc := cfg.Updater
	interval := 6 * time.Hour
	if uc.CheckInterval != "" {
		if d, err := time.ParseDuration(uc.CheckInterval); err == nil {
			interval = d
		}
	}
	root := cfg.Compat.RepoRoot
	if root == "" {
		root = "."
	}
	out := updater.Config{
		Enabled:              uc.Enable,
		ServerURL:            uc.ServerURL,
		Channel:              strings.ToLower(strings.TrimSpace(uc.Channel)),
		ServerToken:          uc.ServerToken,
		CheckInterval:        interval,
		AutoInstall:          uc.AutoInstall,
		MaintenanceWindow:    uc.MaintenanceWindow,
		AssetOverride:        uc.AssetOverride,
		HistoryPath:          filepath.Join(root, "database", "updater", "history.json"),
		MinisignPubKey:       uc.MinisignPubKey,
		RequireSignature:     uc.RequireSignature,
		PreventDuringStreams: uc.PreventDuringStreams,
		ActiveStreams: func() int64 {
			return runtimeTrafficStats.activeProxy.Load()
		},
		PluginsDir: updater.ResolvePluginsDir(root),
		WwwrootDir: updater.ResolveWwwrootDir(root),
	}
	// Admin-UI channel override (database/updater/settings.json) wins over
	// config.toml — same pattern as the browser-engine override.
	if s, err := updater.LoadSettings(updater.SettingsPath(out.HistoryPath)); err == nil {
		s.ApplyTo(&out)
	} else {
		log.Warn().Err(err).Msg("updater: failed to load channel override settings")
	}
	return out
}

// browserEnginePersistPath returns the on-disk path of the admin-UI
// override file (database/browser_engine.json under the repo root).
func browserEnginePersistPath(repoRoot string) string {
	return filepath.Join(repoRoot, "database", "browser_engine.json")
}

// applyBrowserEnginePersist layers admin-UI overrides on top of the
// config.toml selection. Missing file is not an error — we just keep
// the config-derived choice. Errors are logged but not fatal.
func applyBrowserEnginePersist(repoRoot string) {
	engine, balancerEngines, err := browser.LoadPersisted(browserEnginePersistPath(repoRoot))
	if err != nil {
		log.Warn().Err(err).Msg("browser: failed to load engine override")
		return
	}
	if engine == "" && len(balancerEngines) == 0 {
		return
	}
	if engine != "" {
		browser.SetDefault(engine)
	}
	if balancerEngines != nil {
		browser.SetBalancerOverrides(balancerEngines)
	}
	log.Info().Str("engine", engine).Int("overrides", len(balancerEngines)).
		Msg("browser: applied admin-UI engine override")
}

// stringMapEqual compares two string→string maps for deep equality.
func stringMapEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}
