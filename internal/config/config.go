package config

import (
	"path/filepath"
	"strings"
)

// Config is the top-level configuration for lampac-go.
type Config struct {
	// TOMLPath is the actual path of the config.toml that was loaded.
	// Set at runtime by loadTOML, not serialized.
	TOMLPath string `toml:"-" json:"-"`

	Server            ServerConfig        `toml:"server"`
	Compat            CompatConfig        `toml:"compat"`
	Web               WebConfig           `toml:"web"`
	WebSocket         WebSocketConfig     `toml:"websocket"`
	Sisi              SisiConfig          `toml:"sisi"`
	Online            OnlineConfig        `toml:"online"`
	Cub               CubConfig           `toml:"cub"`
	ProxyLink         ProxyLinkConfig     `toml:"proxy_link"`
	ServerProxy       ServerProxyConfig   `toml:"server_proxy"`
	Observability     ObservabilityConfig `toml:"observability"`
	Parser            ParserConfig        `toml:"parser"`
	TorrServer        TorrServerConfig    `toml:"torrserver"`
	TelegramAuth      TelegramAuthConfig  `toml:"telegram"`
	AdminAuth         AdminAuthConfig     `toml:"admin"`
	Auth              AuthConfig          `toml:"auth"`
	Proxy             ProxyConfig         `toml:"proxy"`
	ProxyCore         ProxyCoreConfig     `toml:"proxycore"`
	Sync              SyncConf            `toml:"sync"`
	Mirror            MirrorConfig        `toml:"mirror"`
	TMDBProxy         TMDBProxyConf       `toml:"tmdb_proxy"`
	Transcoding       TranscodingConf     `toml:"transcoding"`
	LLM               LLMConfig           `toml:"llm"`
	Kit               KitConfig           `toml:"kit"`
	BrowserPool       BrowserPoolConfig   `toml:"browser_pool"`
	YouTube           YouTubeConfig       `toml:"youtube"`
	YouTubeOAuth      YouTubeOAuthConfig  `toml:"youtube_oauth"`
	AntiDPI           AntiDPIConfig       `toml:"antidpi"`
	Zapret            ZapretConfig        `toml:"zapret"`
	FileCacheInactive FileCacheConfig     `toml:"file_cache_inactive"`
	DLNA              DLNAConfig          `toml:"dlna"`
	Kinopoisk         KinopoiskConfig     `toml:"kinopoisk"`
	SkipIntro         SkipIntroConfig     `toml:"skip_intro"`
	Calendar          CalendarConfig      `toml:"calendar"`
	XSearch           XSearchConfig       `toml:"xsearch"`
	Collections       CollectionsConfig   `toml:"collections"`
	OpenSubs          OpenSubsConfig      `toml:"opensubs"`
	IPTV              IPTVConfig          `toml:"iptv"`
	Cluster           ClusterConfig       `toml:"cluster"`
	Updater           UpdaterConfig       `toml:"updater"`
	Telemetry         TelemetryConfig     `toml:"telemetry"`
	Security          SecurityConfig      `toml:"security"`
	Host              HostConfig          `toml:"host"`
	HealthCheck       HealthCheckConfig   `toml:"healthcheck"`
	Branding          BrandingConfig      `toml:"branding"`
	Capi              CapiConfig          `toml:"capi"`
}

// ---------------------------------------------------------------------------
//  Branding — public-facing brand strings.
//
//  Runtime overrides written by the admin UI go to
//  database/branding.json — those win over config.toml on boot.
// ---------------------------------------------------------------------------

// BrandingConfig holds the public-facing brand strings used in JS and HTML.
// Empty fields fall back to built-in defaults (Alpac / Alpac Онлайн / …).
//
// Settings here are layered: config.toml provides operator defaults; runtime
// edits via the admin UI write to database/branding.json and supersede the
// TOML values without restarting the server (same pattern as healthcheck).
type BrandingConfig struct {
	Name          string `toml:"name" json:"name"`                       // global brand (replaces "Lampac" in JS)
	Version       string `toml:"version" json:"version"`                 // string in window.lampac_version
	Major         int    `toml:"major" json:"major"`                     // numeric major (>=153)
	Minor         int    `toml:"minor" json:"minor"`                     // numeric minor
	OnlineNameRU  string `toml:"online_name_ru" json:"online_name_ru"`   // title_online["ru"]
	OnlineNameUK  string `toml:"online_name_uk" json:"online_name_uk"`   // title_online["uk"]
	OnlineNameEN  string `toml:"online_name_en" json:"online_name_en"`   // title_online["en"]
	OnlineNameZH  string `toml:"online_name_zh" json:"online_name_zh"`   // title_online["zh"]
	HTMLTitleV2   string `toml:"html_title_v2" json:"html_title_v2"`     // <title> for lampa-v2last/lampa-main
	HTMLTitleLite string `toml:"html_title_lite" json:"html_title_lite"` // <title> for lampa-lite
}

// ---------------------------------------------------------------------------
//
//	Capi — secured first-party client API (/capi/*) for the alpac web/native app.
//
//	Server-side aggregates online sources, strips balancer identity, mints
//	opaque IP-bound /proxy stream tokens, and serves the SPA same-origin so
//	cookies/referer/IP align (kills CORS + streamproxy 403s).
//
// ---------------------------------------------------------------------------
type CapiConfig struct {
	Enable            bool     `toml:"enable" json:"enable"`                           // master switch for /capi/*
	AppKey            string   `toml:"app_key" json:"app_key"`                         // required X-Alpac-App value (first-party app binding)
	VerifyIP          bool     `toml:"verify_ip" json:"verify_ip"`                     // DEPRECATED, ignored: IP-bound stream tokens 403'd on client IP changes (LTE↔WiFi); capi now mints like Lampa (never IP-bound)
	AppDir            string   `toml:"app_dir" json:"app_dir"`                         // dir of the built SPA served under /app (empty = disabled)
	RequirePremium    bool     `toml:"require_premium" json:"require_premium"`         // gate /capi behind active premium/subscription (TG store overlay)
	SocksProxy        string   `toml:"socks_proxy" json:"socks_proxy"`                 // optional SOCKS5 for RU review scraping (irecommend) when the VPS IP is geo/datacenter-blocked
	ResolveSources    []string `toml:"resolve_sources" json:"resolve_sources"`         // allowlist of balancer names /capi/streams may resolve server-side (empty = all). Curate to server-resolvable sources; exclude browser-heavy/broken ones (mirage/alloha).
	TraktClientID     string   `toml:"trakt_client_id" json:"trakt_client_id"`         // Trakt.tv API client_id (free app registration) → unlocks the Trakt chart rows in /capi/collections; empty = CUB-only
	TraktClientSecret string   `toml:"trakt_client_secret" json:"trakt_client_secret"` // Trakt app client_secret — REQUIRED for write-sync (OAuth device flow + scrobble). Empty = read-only charts only.
	TraktSync         bool     `toml:"trakt_sync" json:"trakt_sync"`                   // push a user's finished watches to their linked Trakt account. Needs client_id+secret; users link via /capi/trakt/link.
	PublicHost        string   `toml:"public_host" json:"public_host"`                 // canonical SPA URL (e.g. "https://tv.alcopa.cc") for bot-built links: watch-party room invites → <public_host>/app/#/room/<code>. Falls back to [kit] server_host.
	SoftDeadlineMS    int      `toml:"soft_deadline_ms" json:"soft_deadline_ms"`       // /capi/streams: return the first partial after this long if ≥1 voice landed (0 = default 4500). Fast hosts can shave it to ~3000 — the backfill publisher fills the rest.
	HardDeadlineMS    int      `toml:"hard_deadline_ms" json:"hard_deadline_ms"`       // /capi/streams: absolute wait ceiling when NOTHING has landed yet (0 = default 8000). Must exceed soft_deadline_ms.
}

// ---------------------------------------------------------------------------
//  HealthCheck — periodic balancer host probes + auto-disable.
// ---------------------------------------------------------------------------

// HealthCheckConfig controls the background balancer healthcheck loop.
// Runtime overrides (via admin UI) are layered on top of these defaults and
// persisted to database/healthcheck_settings.json — TOML changes here are
// only honoured on next restart unless the admin overrides are also wiped.
type HealthCheckConfig struct {
	// Enabled toggles the entire healthcheck loop. When false:
	//   - no background probing happens,
	//   - already-recorded AutoDisabled flags continue to be honoured
	//     (they're loaded from database/healthcheck.json on boot) — clear
	//     them by hitting "Reset" in the admin UI if you want to re-enable
	//     auto-disabled balancers without restarting probing.
	Enabled bool `toml:"enabled" json:"enabled"`

	// IntervalSec is the seconds between consecutive probe passes.
	// Range [5, 3600]; default 60.
	IntervalSec int `toml:"interval_sec" json:"interval_sec"`

	// TimeoutSec is the per-probe HTTP timeout. Range [1, 60]; default 10.
	TimeoutSec int `toml:"timeout_sec" json:"timeout_sec"`

	// FailThreshold is the number of consecutive failures before a
	// balancer is auto-disabled. Range [1, 50]; default 3.
	FailThreshold int `toml:"fail_threshold" json:"fail_threshold"`

	// RecoverThreshold is the number of consecutive successes required
	// to auto-re-enable a previously disabled balancer. Range [1, 50];
	// default 2.
	RecoverThreshold int `toml:"recover_threshold" json:"recover_threshold"`
}

// ---------------------------------------------------------------------------
//  Security — TLS strictness, request limits.
// ---------------------------------------------------------------------------

// SecurityConfig governs outbound TLS strictness for two distinct request
// surfaces and request-body limits.
//
// The default values match the legacy behavior (everything insecure) so an
// upgrade does not break working setups. Operators with a controlled upstream
// environment SHOULD enable StrictBalancerTLS to detect MITM.
type SecurityConfig struct {
	// StrictBalancerTLS controls cert validation on outbound balancer
	// requests (SharedTransport / IPv6Transport in internal/httpclient).
	//   false (default): InsecureSkipVerify=true — back-compat with broken
	//     upstreams (videoseed.tv etc. don't send intermediate certs).
	//   true: validate certs normally; broken upstreams will fail.
	// You can also list specific hosts in InsecureHosts that should keep
	// loose validation even when StrictBalancerTLS is enabled.
	StrictBalancerTLS bool `toml:"strict_balancer_tls"`

	// InsecureHosts is the exception list when StrictBalancerTLS is true.
	// Match is by exact host (case-insensitive) or "*.suffix" wildcard.
	InsecureHosts []string `toml:"insecure_hosts"`

	// StrictAdminTLS controls cert validation for admin-grade outbound
	// requests that carry credentials (cub proxy, kit bind, etc.). Default
	// false for back-compat. Enabling protects admin tokens from MITM.
	StrictAdminTLS bool `toml:"strict_admin_tls"`

	// MaxRequestBodyBytes caps the size of incoming request bodies for
	// endpoints that don't have an explicit limit. 0 = unlimited (legacy).
	// Recommended: 8 * 1024 * 1024 (8 MB).
	MaxRequestBodyBytes int64 `toml:"max_request_body_bytes"`

	// TrustedProxies is an ADDITIONAL allowlist of upstream proxy IPs / CIDR
	// ranges from which the server will honour X-Forwarded-For and X-Real-IP.
	//
	// In addition to this list, the server trusts loopback (127.0.0.1, ::1)
	// AND — unless StrictRFC1918 is set — the standard private ranges
	// (10/8, 172.16/12, 192.168/16, 169.254/16, fc00::/7, fe80::/10). This
	// matches the common Lampa deployment behind nginx/traefik on a private
	// network without requiring explicit config.
	//
	// Set entries here when your upstream proxy is on a PUBLIC IP (some
	// CDN/edge setups). Supports plain IPv4/v6 and CIDR.
	TrustedProxies []string `toml:"trusted_proxies"`

	// StrictRFC1918 turns OFF the default trust of private network ranges
	// (10/8, 172.16/12, 192.168/16, etc.) — only loopback and explicit
	// TrustedProxies entries will be honored. Use this when Lampa is exposed
	// directly to the internet and you don't want an attacker on a shared
	// subnet to be able to spoof X-Forwarded-For.
	StrictRFC1918 bool `toml:"strict_rfc1918"`
}

// ---------------------------------------------------------------------------
//  Telemetry — balancer success/failure metrics, alerts, fallback hosts.
// ---------------------------------------------------------------------------
//  Host — public-facing domain aliases (CDN bypass).
// ---------------------------------------------------------------------------

// HostConfig maps a request Host to an alternative host used when emitting
// URLs that must bypass a fronting CDN — typically `/proxy/`, `/transcoding/`,
// and WebSocket (`/nws`, `/ws`) endpoints when CloudFlare proxies the main
// domain but its Free plan can't carry video streams (TOS §2.8) or long-lived
// WebSockets (~100s idle cap).
//
// Example: the main domain `lampa.li` is proxied through CloudFlare (orange);
// add `[host.stream_aliases]` "lampa.li" = "s.lampa.li" so stream URLs the
// server emits to clients point to a DNS-only subdomain that goes direct.
// Requests via other hosts (e.g. beta.l-vid.online direct) are unaffected.
type HostConfig struct {
	// StreamAliases is a map from request Host → stream subdomain. When the
	// server emits a URL for a stream-bound endpoint and the request came in
	// via a key in this map, the value replaces the host in the emitted URL.
	StreamAliases map[string]string `toml:"stream_aliases"`
}

// StreamHostFor takes a "scheme://host[:port]" URL and returns the aliased
// equivalent if the host matches an entry in StreamAliases. If no alias
// matches (or no map configured), the original URL is returned unchanged.
func (h HostConfig) StreamHostFor(origURL string) string {
	if len(h.StreamAliases) == 0 {
		return origURL
	}
	scheme := ""
	rest := origURL
	if i := strings.Index(rest, "://"); i >= 0 {
		scheme = rest[:i]
		rest = rest[i+3:]
	}
	host := rest
	port := ""
	// Find LAST ":" that is not inside a bracketed IPv6 literal.
	if p := strings.LastIndex(rest, ":"); p > 0 && !strings.Contains(rest[p:], "]") {
		host = rest[:p]
		port = rest[p:]
	}
	if alias, ok := h.StreamAliases[host]; ok {
		if scheme != "" {
			return scheme + "://" + alias + port
		}
		return alias + port
	}
	return origURL
}

// ---------------------------------------------------------------------------

// TelemetryConfig configures the per-balancer metrics, TG alerts, and
// fallback-host rotation logic.
type TelemetryConfig struct {
	// Enabled toggles the whole subsystem. If false, balancerFetch records
	// nothing and no alerts fire.
	Enabled bool `toml:"enabled"`

	// AlertsEnabled toggles only the TG alert engine. The recorder still
	// runs (so the dashboard works), but no admin notifications are sent.
	AlertsEnabled bool `toml:"alerts_enabled"`

	// FailRateThreshold: fire an alert when success rate in the window
	// drops below this (0.0–1.0). Default 0.30.
	FailRateThreshold float64 `toml:"fail_rate_threshold"`

	// MinAttempts: do not alert until at least this many attempts in window
	// (avoids noise from low-traffic balancers). Default 20.
	MinAttempts int `toml:"min_attempts"`

	// CooldownMinutes between repeated alerts for the same balancer. Default 30.
	CooldownMinutes int `toml:"cooldown_minutes"`

	// FallbackFailLimit: consecutive fails on the active host before rotating
	// to the next configured fallback. Default 4.
	FallbackFailLimit int `toml:"fallback_fail_limit"`

	// Hosts maps a balancer key to an ordered fallback list. The first entry
	// is the primary; subsequent entries are tried on rotation. Example:
	//   [telemetry.hosts]
	//   collaps = ["api.initem.ws", "api.variyt.ws", "api.bhcesh.me"]
	Hosts map[string][]string `toml:"hosts"`
}

// ---------------------------------------------------------------------------
//  Server
// ---------------------------------------------------------------------------

type ServerConfig struct {
	Addr       string   `toml:"addr"`
	SystemDNS  bool     `toml:"system_dns"`  // true = системный резолвер (glibc) вместо встроенного Go
	DNSServers []string `toml:"dns_servers"` // альтернатива: ["8.8.8.8"] или ["tls://8.8.8.8"] (DoT)
	SetupDone  bool     `toml:"setup_done"`  // true после завершения web-установщика; блокирует /web/installer
}

// ---------------------------------------------------------------------------
//  Compat
// ---------------------------------------------------------------------------

type CompatConfig struct {
	RepoRoot string `toml:"repo_root"`
}

// ---------------------------------------------------------------------------
//  Web plugins
// ---------------------------------------------------------------------------

type WebConfig struct {
	InitPlugins InitPluginsConfig `toml:"plugins"`
	AppReplace  []AppReplaceRule  `toml:"app_replace"`
	CustomCSS   string            `toml:"custom_css"`
	CustomJS    string            `toml:"custom_js"`
	// YandexMetrika is a Yandex.Metrika counter ID (digits only). When set,
	// the counter tag is appended to app.min.js for EVERY client that loads
	// it — web, Android WebView, Samsung Tizen and LG webOS all pull the same
	// bundle, so one ID covers all channels. The injected snippet auto-detects
	// the client platform and reports it as a visit/user param ("channel") so
	// a single counter segments web/android-app/tizen/webos. Empty = disabled.
	// Note: the native Android ExoPlayer runs outside the WebView, so in-player
	// events are not captured here — only the app shell (browse/select/history).
	YandexMetrika string `toml:"yandex_metrika"`
	// WeblogCollect enables the POST /lite/weblog receiver: the ALPAC web/TV client forwards its
	// in-app weblog (errors always, the full action trace when weblog mode is on) and the server
	// writes each line to its log (journalctl) tagged with the client IP/UA — so TV errors that have
	// no devtools become visible in the terminal. Rate-limited per IP. Default true (cheap; only
	// active when a client actually forwards). Set false to drop the endpoint entirely.
	WeblogCollect            bool   `toml:"weblog_collect"`
	CommunityPluginURL       string `toml:"community_plugins_url"`
	CommunityAutoUpdateHours int    `toml:"community_auto_update_hours"` // 0 = disabled
	// PluginShortPath, when set, registers a short alias route for /on.js so the
	// plugin can be installed from e.g. http(s)://host/m instead of /on.js. The
	// value is a bare path segment ("m", "p", "x"); leading/trailing slashes are
	// trimmed. Empty (default) disables the alias. Must not collide with an
	// existing route ("on.js", "ts", "admin", …) — a conflict is logged and skipped.
	PluginShortPath string `toml:"plugin_short_path"`
}

// ShortPluginRoute returns the normalized router path for the optional short
// /on.js alias (PluginShortPath): "m" → "/m", "/m.js" → "/m.js". Returns ""
// when PluginShortPath is unset, which disables the alias.
func (w WebConfig) ShortPluginRoute() string {
	p := strings.Trim(strings.TrimSpace(w.PluginShortPath), "/")
	if p == "" {
		return ""
	}
	return "/" + p
}

type AppReplaceRule struct {
	Name        string `toml:"name" json:"name"`
	Pattern     string `toml:"pattern" json:"pattern"`
	Replacement string `toml:"replacement" json:"replacement"`
	Enabled     bool   `toml:"enabled" json:"enabled"`
}

type InitPluginsConfig struct {
	DLNA        bool `toml:"dlna"`
	Tracks      bool `toml:"tracks"`
	Transcoding bool `toml:"transcoding"`
	TMDBProxy   bool `toml:"tmdb_proxy"`
	Online      bool `toml:"online"`
	Catalog     bool `toml:"catalog"`
	SISI        bool `toml:"sisi"`
	TorrServer  bool `toml:"torrserver"`
	Backup      bool `toml:"backup"`
	Sync        bool `toml:"sync"`
	Bookmark    bool `toml:"bookmark"`
	Timecode    bool `toml:"timecode"`
	// SyncPro enables the unified syncpro.js plugin (favorites + timecodes +
	// view history + torrents + search history + plugin list + manual
	// backup). When true, lampainit auto-loads syncpro.js and SUPPRESSES
	// the legacy quartet (Sync/Bookmark/Timecode/Backup) from the autoload
	// list — they remain reachable at /sync.js, /bookmark.js etc. for
	// users running a custom Lampa build that depends on them. New
	// installs default this to true (config.go DefaultInitConfig); existing
	// installs keep their saved Sync/Bookmark/Timecode/Backup flags unless
	// they opt in via the admin UI.
	SyncPro          bool `toml:"sync_pro"`
	AdsFree          bool `toml:"ads_free"`
	YouTubeFeed      bool `toml:"youtube_feed"`
	Stats            bool `toml:"stats"`
	OpenSubs         bool `toml:"opensubs"`
	Failover         bool `toml:"failover"`
	DualSubs         bool `toml:"dualsubs"`
	PlayerRedesign   bool `toml:"player_redesign"`
	HLSTracks        bool `toml:"hls_tracks"`
	VoiceSwitcher    bool `toml:"voice_switcher"`
	WebPlayer        bool `toml:"web_player"`
	WebPlayerAndroid bool `toml:"web_player_android"`
	// Previously always-on plugins, now toggleable.
	Theme          bool `toml:"theme"`
	Screensaver    bool `toml:"screensaver"`
	Remote         bool `toml:"remote"`
	ExternalPlayer bool `toml:"external_player"`
	Migrate        bool `toml:"migrate"`
	// ServerWidget enables the corner-badge plugin that shows current
	// cluster server + lets the user pick a preferred node. Cluster-aware.
	ServerWidget bool `toml:"server_widget"`

	// KinopubResume serves the kinopub_resume.js bridge that POSTs
	// player progress to /lite/kinopub/watching/marktime. Only useful
	// when at least one user has activated a kino.pub premium token —
	// turn off on servers without kinopub to skip the autoload.
	KinopubResume bool `toml:"kinopub_resume"`
}

// ---------------------------------------------------------------------------
//  WebSocket
// ---------------------------------------------------------------------------

type WebSocketConfig struct {
	Type                 string `toml:"type"`
	InactiveAfterMinutes int    `toml:"inactive_after_minutes"`
}

// ---------------------------------------------------------------------------
//  SISI
// ---------------------------------------------------------------------------

type SisiConfig struct {
	Spider             bool                        `toml:"spider"`
	Component          string                      `toml:"component"`
	IconName           string                      `toml:"icon_name"`
	PushAll            bool                        `toml:"push_all"`
	HistoryEnable      bool                        `toml:"history_enable"`
	ForcedCheckRchType bool                        `toml:"forced_check_rch_type"`
	Sources            map[string]SisiSourceConfig `toml:"sources"`
}

// SisiSourceConfig holds per-source settings within [sisi.sources.SourceName].
type SisiSourceConfig struct {
	Enable       bool   `toml:"enable"`
	RIP          bool   `toml:"rip"`
	Spider       bool   `toml:"spider"`
	DisplayName  string `toml:"display_name"`
	DisplayIndex int    `toml:"display_index"`
	Login        string `toml:"login"`
	Password     string `toml:"password"`
	Cookie       string `toml:"cookie"`
}

// ---------------------------------------------------------------------------
//  Online sources — general + per-source sub-structs
// ---------------------------------------------------------------------------

type OnlineConfig struct {
	CheckOnlineSearch bool     `toml:"check_online_search"`
	WithSearch        []string `toml:"with_search"`
	NoStreamProxy     []string `toml:"no_stream_proxy"` // balancers that bypass /proxy/ (direct CDN URLs)
	// StreamProxyManifestOnly is the middle ground between proxying everything and
	// no_stream_proxy: the HLS/DASH manifest still goes through /proxy (so the
	// server can rewrite it), but the SEGMENT urls inside it are handed to the
	// client as direct CDN links, so the video bytes never transit this server.
	//
	// This exists because filmix needs both halves at once. Its playlists omit
	// ?hash= on the EXT-X-MAP init URI, and the CDN 403s a hashless init — only a
	// server-side rewrite can repair that (HDR/HEVC rips are fMP4, so they are the
	// ones that break). But relaying every segment costs an extra hop plus this
	// server's shared uplink, and a 4K filmix stream averages ~9.5 MiB per 10s
	// segment (~8 Mbps) — measured 2026-08-01, more than a sequential relay
	// sustains. Manifest-only keeps the repair and gives the bytes back to the CDN.
	//
	// Only safe for CDNs whose segment urls are not bound to the resolving IP and
	// that send permissive CORS. Clients that cannot reach the CDN directly
	// (blocked upstream) need full proxying.
	//
	// ★filmix does NOT qualify and is ignored here (see proxyapi.New): measured
	// 2026-08-02, its CDN answers 403 to anything above 480p when the fetching IP
	// differs from the one the hash was issued to — and since api.filmix.tv
	// geo-blocks datacentre IPs, that hash is always minted through a proxy the
	// viewer is not behind. The symptom is one segment retried forever.
	StreamProxyManifestOnly []string `toml:"stream_proxy_manifest_only"`
	CustomOrder             bool     `toml:"custom_order"`           // true = user-defined order (with_search); false (default) = auto-sort by quality
	CDNHTTPProxy            string   `toml:"cdn_http_proxy"`         // HTTP proxy for CDN plugins (e.g. "http://127.0.0.1:8888")
	FlareSolverr            string   `toml:"flaresolverr"`           // FlareSolverr URL for Cloudflare bypass (e.g. "http://127.0.0.1:8191")
	FlareSolverrBalancers   []string `toml:"flaresolverr_balancers"` // balancers that should route through FlareSolverr (e.g. ["kinogo","vdbmovies"])

	// Component overrides the Lampa.Component name registered by online.js
	// (default "lampac"). Useful to avoid collisions with other online plugins
	// (Showy, NNMTV, etc.) that may register the same name. Affects
	// component: 'lampac' literals AND Lampa.Component.add('lampac', ...) calls
	// — server replaces them when serving /online.js. Leave empty (or "lampac")
	// to keep the default.
	Component string `toml:"component"`

	PidoRezka     PidoRezkaSource    `toml:"rezka"`
	Rhsprem       PidoRezkaSource    `toml:"rhsprem"`
	AhueRezka     AhueRezkaSource    `toml:"ahuerezka"`
	KinoPub       KinoPubSource      `toml:"kinopub"`
	Wink          WinkSource         `toml:"wink"`
	Filmix        FilmixSource       `toml:"filmix"`
	FilmixTV      HostSource         `toml:"filmix_tv"`
	FilmixPartner TokenSource        `toml:"filmix_partner"`
	Kinobase      KinobaseSource     `toml:"kinobase"`
	Collaps       CollapsSource      `toml:"collaps"`
	Redheadsound  RedheadsoundSource `toml:"redheadsound"`
	Anilibria     HostSource         `toml:"anilibria"`
	AniLiberty    HostSource         `toml:"aniliberty"`
	Animebesst    HostSource         `toml:"animebesst"`
	Animedia      HostSource         `toml:"animedia"`
	Animevost     HostSource         `toml:"animevost"`
	AnimeGo       HostSource         `toml:"animego"`
	Remux         RemuxSource        `toml:"iremux"`
	Mirkino       MirkinoSource      `toml:"mirkino"`
	AnimeLib      HostTokenSource    `toml:"animelib"`
	Kinoukr       KinoukrSource      `toml:"kinoukr"`
	Kinotochka    KinotochkaSource   `toml:"kinotochka"`
	RutubeMovie   HostSource         `toml:"rutube_movie"`
	Anwap         HostSource         `toml:"anwap"`
	VKMovie       VKMovieSource      `toml:"vk_movie"`
	HDVB          APIHostTokenSource `toml:"hdvb"`
	Plvideo       HostSource         `toml:"plvideo"`
	VDBmovies     HostSource         `toml:"vdbmovies"`
	Kodik         APIHostTokenSource `toml:"kodik"`
	Lumex         LumexSource        `toml:"lumex"`
	VideoCDN      VideoCDNSource     `toml:"videocdn"`
	Alloha        AllohaSource       `toml:"alloha"`
	VeoVeo        VeoVeoSource       `toml:"veoveo"`
	Ashdi         HostSource         `toml:"ashdi"`
	Eneyida       HostSource         `toml:"eneyida"`
	Kinogo        KinogoSource       `toml:"kinogo"`
	UaKino        UaKinoSource       `toml:"uakino"`
	Kinovod       HostSource         `toml:"kinovod"`
	FanCDN        FanCDNSource       `toml:"fancdn"`
	VideoDB       VideoDBSource      `toml:"videodb"`
	Videoseed     HostTokenSource    `toml:"videoseed"`
	Zetflix       ZetflixSource      `toml:"zetflix"`
	ZetflixDB     ZetflixDBSource    `toml:"zetflixdb"`
	CDNmovies     HostSource         `toml:"cdnmovies"`
	CDNvideohub   HostSource         `toml:"cdnvideohub"`
	Kubikvkube    KubikvkubeSource   `toml:"kubikvkube"`
	Vibix         HostTokenSource    `toml:"vibix"`
	Turbo         HostSource         `toml:"turbo"`
	IframeVideo   IframeVideoSource  `toml:"iframe_video"`
	GetsTV        HostTokenSource    `toml:"getstv"`
	Mirage        MirageSource       `toml:"mirage"`
	Aladdin       MirageSource       `toml:"aladdin"`
	PidTor        PidTorSource       `toml:"pidtor"`
	MoonAnime     HostTokenSource    `toml:"moonanime"`
	IptvOnline    HostTokenSource    `toml:"iptv_online"`
	Vokino        VokinoSource       `toml:"vokino"`
	// Ukrainian balancers
	Bamboo       HostSource     `toml:"bamboo"`
	UAFilm       HostSource     `toml:"uafilm"`
	Unimay       HostSource     `toml:"unimay"`
	StarLight    HostSource     `toml:"starlight"`
	KlonFUN      HostSource     `toml:"klonfun"`
	Uaflix       UaflixSource   `toml:"uaflix"`
	AnimeON      HostSource     `toml:"animeon"`
	Mikai        HostSource     `toml:"mikai"`
	LeProduction HostSource     `toml:"leproduction"`
	Gencit       GencitSource   `toml:"gencit"`
	Femd         FemdSource     `toml:"femd"`
	Kinobadi     KinobadiSource `toml:"kinobadi"`
	FlixCDN      FlixCDNSource  `toml:"flixcdn"`
	Zona         ZonaSource     `toml:"zona"`
	Lift         LiftSource     `toml:"lift"`
	SakhTV       SakhTVSource   `toml:"sakhtv"`
	// English balancers (browser-scraped m3u8 via headless Chrome)
	VidLink   HostSource `toml:"vidlink"`
	Videasy   HostSource `toml:"videasy"`
	HydraFlix HostSource `toml:"hydraflix"`
	TwoEmbed  HostSource `toml:"twoembed"`
}

// SakhTVSource configures the SakhTV balancer (api.sakh.tv).
//
//   - host    — API base (default "https://api.sakh.tv")
//   - login   — user login for personal Pro account
//   - passwd  — user password
//   - token   — pre-issued session token (UUID); if empty, login on first use
//   - app_id  — X-App-Id header (default "5" = Android TV)
//   - no_force_login — omit X-Force-Code on POST /v2/users/login. The account is
//     single-session: X-Force-Code is how the APK CLAIMS that session, so with
//     this flag a login can no longer steal the session another viewer is
//     streaming on (the upstream refuses it instead). Off by default = exact
//     APK behaviour.
type SakhTVSource struct {
	Host         string `toml:"host"`
	Login        string `toml:"login"`
	Passwd       string `toml:"passwd"`
	Token        string `toml:"token"`
	AppID        string `toml:"app_id"`
	NoForceLogin bool   `toml:"no_force_login"`
}

// LiftSource configures the Lift balancer.
//
//   - api_host        — explicit override of resolved Lift catalog API host
//     (e.g. "https://api.embandr.ws"). Empty = auto-resolve.
//   - primary_hosts   — Lift API frontend candidates probed in order.
//   - embed_host      — zenithjs embed host used for stream extraction.
//   - consumer_host   — value passed as ?host= to zenithjs (registers our
//     traffic under a known consumer id, default "lift.com").
//   - embed_referer   — Referer header sent on embed fetch + iframe proxy
//     (must match the consumer host's expected origin).
//   - basic_auth_user/pass — credentials for Lift catalog API (default lift1:lift1).
//   - sourcecraft_url — fallback rotating-domains list.
//   - gist_id         — fallback GitHub Gist that mirrors the rotating list.
type LiftSource struct {
	APIHost        string   `toml:"api_host"`
	PrimaryHosts   []string `toml:"primary_hosts"`
	EmbedHost      string   `toml:"embed_host"`
	ConsumerHost   string   `toml:"consumer_host"`
	EmbedReferer   string   `toml:"embed_referer"`
	BasicAuthUser  string   `toml:"basic_auth_user"`
	BasicAuthPass  string   `toml:"basic_auth_pass"`
	SourcecraftURL string   `toml:"sourcecraft_url"`
	GistID         string   `toml:"gist_id"`
}

// --- Common source types ---

type HostSource struct {
	Host string `toml:"host"`
	Rhub bool   `toml:"rhub"` // route fetches through the reverse client hub (server->device->site)
}

// KubikvkubeSource configures the KubikVKube scraper. Host is the DLE site
// (searched by Kinopoisk id); PlayerHost is the tomion embed/CDN host the site
// links to (tomion rotates domains, so keep it overridable).
type KubikvkubeSource struct {
	Host       string `toml:"host"`
	PlayerHost string `toml:"player_host"`
}

type TokenSource struct {
	Token string `toml:"token"`
}

type HostTokenSource struct {
	Host  string `toml:"host"`
	Token string `toml:"token"`
	// PlayerToken is a separate embed/player token for sources whose API key
	// and iframe token diverged (videoseed 2026-08: apiv2.php takes the API
	// key, kinoserial embeds take the player key). Optional — the search API
	// normally embeds the right player token in returned iframe URLs.
	PlayerToken string `toml:"player_token"`
	Rhub        bool   `toml:"rhub"` // route site fetches through the reverse client hub
}

type APIHostTokenSource struct {
	APIHost string `toml:"api_host"`
	Token   string `toml:"token"`
	// PlayerHost is the iframe host used to rewrite stale hosts in iframe_url
	// returned by the API (HDVB rotates CDN hosts but old ones remain in the
	// API response). When empty no rewrite happens.
	PlayerHost string `toml:"player_host"`
}

type GencitSource struct {
	Host  string `toml:"host"`
	Socks string `toml:"socks"` // SOCKS5 proxy for horsez.org (datacenter IPs blocked)
}

// FemdSource configures the femd.ws balancer (videostorage.xyz ecosystem).
// Movie-only embed API at api.femd.ws/embed/kp/{kpID}; CDN is open
// (cdnr.interkh.com) but URL params expire so we always proxy through /proxy/.
type FemdSource struct {
	Host    string `toml:"host"`    // default https://api.femd.ws
	Referer string `toml:"referer"` // default: host root; some consumer hosts can be passed for partner integration
	Socks   string `toml:"socks"`   // optional SOCKS5 — api.femd.ws WAF returns 422 to non-CIS datacenter IPs
}

// KinobadiSource configures the kinobadi balancer (mm.kinobadi.im /
// vip.kinobadi.im). Movie-only: resolves KP via pleer_on.php → id_file →
// api.femd.ws/embed/movie/{id_file}?host=kinotik.top.
//
// Acts as a consumer-bound femd entry: where the plain femd balancer
// (`/embed/kp/{kp}`) sees a narrow partner catalog, kinobadi often unlocks
// wider content because pleer_on.php knows the kinotik partner id_file
// mapping.
type KinobadiSource struct {
	PleerHost string `toml:"pleer_host"` // default https://vip.kinobadi.im
	FemdHost  string `toml:"femd_host"`  // default https://api.femd.ws
	Consumer  string `toml:"consumer"`   // default kinotik.top (host= query param to femd embed)
	Referer   string `toml:"referer"`    // default https://mm.kinobadi.im/ — used when fetching pleer_on.php
	Socks     string `toml:"socks"`      // optional SOCKS5 — same network as femd, same IP rules
}

type VokinoSource struct {
	Host           string `toml:"host"`
	Token          string `toml:"token"`
	SplitBalancers bool   `toml:"split_balancers"`
}

// ZonaSource controls the Zona balancer (https://w1.zona.im).
//
//   - SplitBalancers toggles per-extractor sub-rows (MOBILINK, HDVB, FILMIX,
//     TAKEDWN) similar to VoKino's split mode.
//   - ChromePort is the CDP port of an already-running Chrome instance
//     (default 9222, same as turbo/mirage/vibix). If the port is unreachable
//     zona falls back to launching its own headless Chrome via ExecAllocator.
type ZonaSource struct {
	Host           string `toml:"host"`
	SplitBalancers bool   `toml:"split_balancers"`
	ChromePort     int    `toml:"chrome_port"`
}

// --- Per-source types with unique fields ---

type PidoRezkaSource struct {
	Host       string `toml:"host"`
	Login      string `toml:"login"`
	Password   string `toml:"password"`
	Premium    bool   `toml:"premium"`
	HLS        bool   `toml:"hls"`
	Cookie     string `toml:"cookie"`
	SocksProxy string `toml:"socks_proxy"` // optional SOCKS5 for browser pool (e.g. "127.0.0.1:40005")
}

// AhueRezkaSource configures the second, independent HDRezka balancer, which
// talks to a Kinopoisk-ID-keyed Cloudflare-Worker proxy instead of scraping an
// HDRezka mirror directly. See internal/httpapi/ahuerezka.go.
type AhueRezkaSource struct {
	Host   string `toml:"host"`    // worker base, e.g. "https://rezka.hdbase.workers.dev"
	KpHost string `toml:"kp_host"` // companion kp worker for title->kp fallback
	// Hosts / KpHosts are optional failover mirrors, tried in order after Host /
	// KpHost. The whole balancer hangs off ONE Cloudflare worker, so without a
	// spare there is nothing between a dead worker and a dead source.
	Hosts   []string `toml:"hosts"`
	KpHosts []string `toml:"kp_hosts"`
	Premium bool     `toml:"premium"` // include premium tiers (Ultra/2K/4K) — only real if worker proxies a premium account; otherwise they are ~1min stubs, keep false
	// HLS appends ":hls:manifest.m3u8" to bare quality URLs. Default OFF: that
	// suffix is understood by an HDRezka HLS *gateway*, not by the CDN this
	// worker hands out. Verified against stream.voidboost.one — the bare .mp4
	// answers 302 → apollo.stream.voidboost.one → 206 video/mp4, while the same
	// URL with the suffix is a hard 404 on every host and subdomain. Turn this on
	// only if the configured worker returns gateway URLs that parse it.
	HLS        bool   `toml:"hls"`
	SocksProxy string `toml:"socks_proxy"` // optional SOCKS5 egress
}

type KinoPubSource struct {
	Host  string `toml:"host"`
	Token string `toml:"token"`
	// Tokens are independent kino.pub device tokens; requests round-robin over
	// them (see getToken in internal/litesrc/kinopub.go).
	//
	// Why more than one: kino.pub caps SIMULTANEOUS playback, and the CDN answers
	// a stream open past the cap with 403 + text/plain "user session limit reached"
	// (verified live 2026-07-29). The vendor FAQ frames the budget as 5 devices
	// watching at once out of up to 10 bound per account, so extra tokens of the
	// SAME account plausibly buy slots up to that per-account ceiling, while tokens
	// of DIFFERENT accounts are what lifts the ceiling itself. Which unit the
	// backend actually counts (bound device vs account) is NOT measured — the
	// discriminating test is to saturate with token A and then open a stream minted
	// by token B of the same account.
	Tokens   []string `toml:"tokens"`
	Filetype string   `toml:"filetype"`
	Rhub     bool     `toml:"rhub"` // route API calls through the reverse client hub
}

// WinkSource configures the Wink (Rostelecom "Interactive TV" / Restream/Smotreshka)
// source. Protocol reverse-engineered from Wink_ATV_v.1.38.1.apk.
//
// Caveats baked into the design:
//   - Streams are IP-bound and the API blocks datacenter IPs (HTTP 451
//     "Запрещённый IP адрес"), so a Russian RESIDENTIAL SOCKS5 is required in
//     practice. Empty / "none" / "off" = direct.
//   - Auth is phone+SMS-code or login+password via user/sessions; the session is
//     a single `session_id` header (no request signing).
//   - Premium VOD assets are Widevine-DRM (DASH) and unplayable in Lampa; only
//     isCrypted=false (clear HLS) channels and assets are surfaced.
type WinkSource struct {
	Enable        bool   `toml:"enable"`
	DiscoveryHost string `toml:"discovery_host"` // default https://itv.svc.iptv.rt.ru/api/v2 (serves discover + itv/* bootstrap)
	Login         string `toml:"login"`          // phone (+7...) or email
	Password      string `toml:"password"`       // static account password; for phone+SMS leave empty and run device activation
	LoginType     string `toml:"login_type"`     // PHONE | EMAIL (auto-detected from login when empty)
	SocksProxy    string `toml:"socks_proxy"`    // RU residential SOCKS5 (host:port). Empty/none/off = direct.
	UserAgent     string `toml:"user_agent"`
	Platform      string `toml:"platform"`     // itv/devices platform (default ANDROID)
	DeviceType    string `toml:"device_type"`  // itv/devices type (default ANDROIDTV)
	DeviceModel   string `toml:"device_model"` // itv/devices model (default Nexus 9)
	TV            bool   `toml:"tv"`           // expose live TV channels (M3U playlist + EPG)
	VOD           bool   `toml:"vod"`          // expose the `wink` VOD balancer
}

type FilmixSource struct {
	Host    string   `toml:"host"`
	Token   string   `toml:"token"`
	Tokens  []string `toml:"tokens"`
	Reserve bool     `toml:"reserve"`
	Pro     bool     `toml:"pro"`
	HLS     bool     `toml:"hls"`
	Rhub    bool     `toml:"rhub"` // route API calls through the reverse client hub
	// Filmix account login/password for the api.filmix.tv (api-fx) flow. The legacy
	// filmixapp.cyou /s/ hashes are capped at 720p server-side since 2026-07 — 4K/1080
	// require an api-fx accessToken, which only login+password can obtain (the legacy
	// 32-char device token is not accepted there).
	UserAPITV   string `toml:"user_apitv"`
	PasswdAPITV string `toml:"passwd_apitv"`
	// Progressive turns filmix streams into single-file MP4 links served straight from the CDN
	// instead of proxied HLS. Requires user_apitv/passwd_apitv: only an api-fx *authenticated*
	// hash carries the entitlement — a device-token hash yields a 10 871 558-byte "buy premium"
	// stub above 720p (measured 2026-08-05). Unlike HLS segments, progressive files are NOT
	// IP-bound, so the server can mint and the viewer can download directly: media never touches
	// this server (no buffering, no exit bottleneck), every client just plays an mp4 — including
	// vanilla Lampa, Tizen and webOS — HDR needs no EXT-X-MAP patching, and seeking is Range.
	// Guarded: each title is probed and falls back to HLS when the CDN returns a stub.
	// Nil = enabled whenever credentials exist; set false to disable.
	Progressive *bool `toml:"progressive"`
	// DirectLampa turns on client-side direct-CDN for the Lampa web client (architecture B):
	// the server attaches an api-fx recipe (`fxdirect`) to filmix rows and injects a browser-mint
	// snippet into online.js/player-inner.js, so Lampa mints the hash itself and streams the CDN
	// directly (no proxy, no exit bottleneck → no buffering) where api-fx accepts its IP. Off by
	// default and reversible from the admin panel: if it misbehaves on some client, flip it off and
	// filmix falls back to the normal proxied path with no client redeploy.
	DirectLampa bool `toml:"direct_lampa"`
}

type KinobaseSource struct {
	Host     string `toml:"host"`
	PlayerJS bool   `toml:"playerjs"`
	HDR      bool   `toml:"hdr"`
}

type CollapsSource struct {
	APIHost  string `toml:"api_host"`
	ListHost string `toml:"list_host"`
	Token    string `toml:"token"`
}

type RedheadsoundSource struct {
	Host       string `toml:"host"`
	Login      string `toml:"login"`
	Password   string `toml:"password"`
	SocksProxy string `toml:"socks_proxy"` // Site-fetch SOCKS5 (DDoS-Guard bypass). Empty = direct.
}

type RemuxSource struct {
	Host   string `toml:"host"`
	Cookie string `toml:"cookie"`
}

// MirkinoSource configures the "Мир Кино" balancer — a Jellyfin media server
// (ru.mir-kino.pp.ru) accessed via its REST API with a personal account.
//
//   - host    — server base URL (default "https://ru.mir-kino.pp.ru")
//   - login   — account login (email/username); used only to mint a token once
//   - passwd  — account password
//   - token   — a pre-issued Jellyfin AccessToken. If set, the balancer uses it
//     directly and NEVER calls AuthenticateByName (the server rate-limits/blocks
//     the login endpoint after repeated attempts, but access tokens are
//     long-lived). A token minted from login/passwd is auto-persisted to
//     data/mirkino.json and reused across restarts, so login runs at most once.
//   - user_id — the Jellyfin user id that owns the token; auto-resolved from the
//     token via /Users/Me when left empty.
type MirkinoSource struct {
	Host     string `toml:"host"`
	Login    string `toml:"login"`
	Password string `toml:"passwd"`
	Token    string `toml:"token"`
	UserID   string `toml:"user_id"`
}

// FanCDNSource is the FanCDN (aka xVideoCDN) balancer config. Despite the
// historical name, the actual backend the balancer now hits is lomont.site —
// the underlying CDN that the deprecated fanserial.me iframe and the closed
// r.xsmart.tv API both ultimately forwarded to. lomont.site needs NO auth
// at all: /gt/{kp} renders an HTML page with the full season/episode/voice
// tree in a <div id="inputData">, and /player/responce.php?video_id=X
// returns the signed HLS src. See internal/httpapi/fancdn.go.
//
// Stream URLs are IP-bound — the CDN ties the path signature to the caller
// IP, so configure socks_proxy if your VPS egress IP is geo-blocked by
// lomont (Mac/US/EU IPs see only a fragment of the catalogue, RU IPs see
// the full one).
//
// Legacy fields from the fanserial.me-era config (login/password/cookie/
// accounts/auto_register/register_proxy/use_browser_for_register/etc.) were
// removed in 2026-05-25 — the lomont.site reverse made all of them moot.
// Old TOML configs with those fields still parse (extra keys are ignored)
// but the values do nothing.
type FanCDNSource struct {
	Host       string `toml:"host"`        // default "https://lomont.site"; "fanserial.me" / "r.xsmart.tv" are auto-redirected to lomont
	UserAgent  string `toml:"user_agent"`  // default desktop Chrome 120 UA
	SocksProxy string `toml:"socks_proxy"` // optional; lomont geo-blocks most non-RU IPs and stream-source URLs are IP-bound
}

type KinoukrSource struct {
	Host string `toml:"host"`
}

type KinotochkaSource struct {
	Host         string `toml:"host"`
	Cookie       string `toml:"cookie"`
	ClientStream bool   `toml:"client_stream"` // apk clients play CDN directly (raw URL + headers), skipping /proxy/
}

type VKMovieSource struct {
	Host     string `toml:"host"`
	TokenURL string `toml:"token_url"`
}

type LumexSource struct {
	APIHost    string `toml:"api_host"`
	Token      string `toml:"token"`
	ClientID   string `toml:"client_id"`
	IframeHost string `toml:"iframe_host"`
}

type VideoCDNSource struct {
	IframeHost string `toml:"iframe_host"`
	Token      string `toml:"token"`
}

type VeoVeoSource struct {
	Host     string `toml:"host"`
	DataPath string `toml:"data_path"`
	Token    string `toml:"token"` // affiliate JWT for kp->movieID iframe resolution
}

// DefaultVeoVeoToken is an affiliate JWT harvested from a veoveo webmaster
// (fanfilm4k.media, webSite 823). It carries no expiry and resolves a Kinopoisk
// ID to the internal veoveo movieID for the whole shared catalog via the iframe
// bootstrap (window.MOVIE_ID). The episodes API itself needs no token. Override
// in [online.veoveo] token if it is ever revoked.
const DefaultVeoVeoToken = "eyJhbGciOiJIUzI1NiJ9.eyJ3ZWJTaXRlIjoiODIzIiwiaXNzIjoiYXBpLXdlYm1hc3RlciIsInN1YiI6IjkwNSIsImlhdCI6MTc3ODg0OTYzNywianRpIjoiYTI0MzExNTYtYjdhZC00MGUxLTg2MjktMTgxMzY5YjcyZjZjIiwic2NvcGUiOiJETEUifQ.5ZbaQ2oQ6rFdwm85Pb7Pfm2KCgTk1t9jfU0D7hscpF4"

type VideoDBSource struct {
	Host         string `toml:"host"`
	APIHost      string `toml:"api_host"`
	APIHost2     string `toml:"api_host2"`     // secondary obrut.show site (cjM)
	FallbackHost string `toml:"fallback_host"` // collaps embed API (variyt.ws) for interkh CDN fallback
	VideoDBCloud string `toml:"videodb_cloud"` // videodb.cloud for Georgian/multi-lang dubs (uses TMDB IDs)
	HLS          bool   `toml:"hls"`           // true → HLS (m3u8), false → direct MP4 for obrut CDN
}

type UaflixSource struct {
	Host   string `toml:"host"`
	Login  string `toml:"login"`
	Passwd string `toml:"passwd"`
	Cookie string `toml:"cookie"`
}

type FlixCDNSource struct {
	PlayerHost  string `toml:"player_host"`  // default: player0.flixcdn.space
	RefererHost string `toml:"referer_host"` // default: hdplayer.click
}

type ZetflixSource struct {
	Host        string `toml:"host"`
	HLS         bool   `toml:"hls"`
	CDN         string `toml:"cdn"`
	StreamProxy bool   `toml:"stream_proxy"`
	Proxy       string `toml:"proxy"` // deprecated: use [proxy.vless]
}

// KinogoSource configures the Kinogo scraper. EmbedSocksProxy routes ONLY the
// player-embed leg (api.ortified.ws) through a SOCKS5 egress: ortified answers
// 422 to datacenter IPs, while the site itself (kinogo.la) is reachable directly
// and is NOT proxied — routing both legs the same way breaks one or the other.
type KinogoSource struct {
	Host            string `toml:"host"`
	EmbedSocksProxy string `toml:"embed_socks_proxy"` // e.g. "socks5://127.0.0.1:40000"
}

// UaKinoSource configures the UaKino (uakino.cx) Ukrainian catalogue. The site
// is Cloudflare-gated, so pages go through FlareSolverr; the player it embeds is
// api.ortified.ws, the same backend kinogo uses — hence EmbedSocksProxy, which
// falls back to the kinogo setting when empty.
type UaKinoSource struct {
	Host            string `toml:"host"`
	EmbedSocksProxy string `toml:"embed_socks_proxy"`
}

// ZetflixDBSource configures the ZetflixDB catalogue — the obrut.show site the
// upstream ZetflixDB module points at (site id "AO"). It runs on the videodb
// player engine, so Host here is the obrut API host, not a site to scrape.
type ZetflixDBSource struct {
	Host string `toml:"host"`
	HLS  bool   `toml:"hls"`
}

type IframeVideoSource struct {
	APIHost string `toml:"api_host"`
	CDNHost string `toml:"cdn_host"`
	Token   string `toml:"token"`
}

type AllohaSource struct {
	APIHost    string   `toml:"api_host"`
	LinkHost   string   `toml:"link_host"`
	Token      string   `toml:"token"`
	CDNProxies []string `toml:"cdn_proxies"`
	RotateMin  float64  `toml:"rotate_min"`
	// KeepOpen keeps the browser tab alive after the initial resolve so the
	// player's own WS connection can refresh Borth+AC automatically.
	// KeepAlive is the number of seconds to keep the tab alive (default 600).
	KeepOpen  bool `toml:"keepopen"`
	KeepAlive int  `toml:"keepalive"`
	// IframeMode skips browser-resolve entirely and returns the raw Alloha
	// iframe URL from the API response (https://sansa.stravers.live/?token_movie=...).
	// The Lampa client must embed it as <iframe>; inside the iframe the CDN
	// player runs from its own origin (sansa.stravers.live), so Phantom CDN
	// accepts segment requests without per-IP signing checks (it sees its own
	// Origin). This matches how kinomix.web.app and lampac-nextgen (C#) work
	// for Alloha. Only useful when the client knows how to handle iframe
	// playback (custom plugin or method:"iframe" support).
	IframeMode bool `toml:"iframe_mode"`
	// DirectStream lets clients that can set arbitrary request headers pull the
	// SEGMENTS straight from the CDN instead of through /proxy/. Only the
	// playlists keep flowing through us (kilobytes), so the video bytes — and the
	// per-viewer IP — leave the server entirely.
	//
	// Measured 2026-08-04: Phantom CDN does NOT bind a signed URL to the resolver
	// IP (the same segment downloaded byte-for-byte from three different exits),
	// it only demands a full header set: Accepts-Controls + Authorizations +
	// Referer + Origin (both on the PLAYER host — our own whitelisted domains are
	// rejected) + a non-empty User-Agent. Range requests are refused.
	//
	// A browser therefore CANNOT do this (Origin/Referer are forbidden headers),
	// which is why the mode is opt-in per request (`&direct=1`): a client
	// advertises that it can attach the headers, everything else keeps proxying.
	DirectStream bool `toml:"direct_stream"`
}

type MirageSource struct {
	APIHost    string   `toml:"api_host"`
	LinkHost   string   `toml:"link_host"`
	Token      string   `toml:"token"`
	CDNProxies []string `toml:"cdn_proxies"` // proxy labels or socks5://host:port for CDN rotation
	RotateMin  float64  `toml:"rotate_min"`  // rotation interval in minutes (default 3.5)
}

type PidTorSource struct {
	Enable        bool              `toml:"enable"`
	RedAPI        string            `toml:"redapi"`
	APIKey        string            `toml:"apikey"`
	MinSeeders    int               `toml:"min_sid"`
	MaxSize       int64             `toml:"max_size"`
	MaxSerialSize int64             `toml:"max_serial_size"`
	EmptyVoice    bool              `toml:"empty_voice"`
	ForceAll      bool              `toml:"force_all"`
	Filter        string            `toml:"filter"`
	FilterIgnore  string            `toml:"filter_ignore"`
	Sort          string            `toml:"sort"`
	Torrs         []string          `toml:"torrs"`
	AuthTorrs     []PidTorAuthEntry `toml:"auth_torrs"`
	BaseAuth      *PidTorAuthEntry  `toml:"base_auth"`
	// AutoRemoveDelayMin: minutes after which an added torrent is dropped from
	// TorrServer. Default 60. Set to -1 to disable auto-removal.
	AutoRemoveDelayMin int `toml:"auto_remove_delay_min"`
	// MaxActiveTorrents: when adding a new magnet, drop the oldest torrents on
	// the same TorrServer host so that no more than this many remain. 0 = no
	// limit (only the time-based AutoRemoveDelayMin applies).
	MaxActiveTorrents int `toml:"max_active_torrents"`
	// Transcode: route playback through the server-side transcoder
	// (/transcoding/start.m3u8?src=...). REQUIRED for PidTor to appear in the
	// /capi (alpac) flow — capi only accepts stream URLs ending in a media
	// extension, so PidTor's resolve URL (/lite/pidtor/s...) is otherwise
	// dropped. Also lets incompatible codecs (HEVC/10-bit) play on any client:
	// the transcoder decides native-vs-transcode per client (H.264 passes
	// through untouched; only problem codecs are re-encoded). Needs
	// [transcoding] enable=true + ffmpeg on the host.
	Transcode bool `toml:"transcode"`
}

type PidTorAuthEntry struct {
	Enable    bool              `toml:"enable"`
	Host      string            `toml:"host"`
	Login     string            `toml:"login"`
	Password  string            `toml:"passwd"`
	Country   string            `toml:"country"`
	NoCountry string            `toml:"no_country"`
	Headers   map[string]string `toml:"headers"`
}

// ---------------------------------------------------------------------------
//  CUB
// ---------------------------------------------------------------------------

type CubConfig struct {
	Enable bool   `toml:"enable"`
	Scheme string `toml:"scheme"`
	Domain string `toml:"domain"`
	Mirror string `toml:"mirror"`
	ViewRU bool   `toml:"view_ru"`
}

// ---------------------------------------------------------------------------
//  Proxy link
// ---------------------------------------------------------------------------

type ProxyLinkConfig struct {
	CacheDir   string `toml:"cache_dir"`
	VerifyIP   bool   `toml:"verify_ip"`
	EncryptAES bool   `toml:"encrypt_aes"`
	// SharedSecret is a cluster-wide AES key seed. When non-empty, all nodes
	// (and the primary) derive the same AES key from SHA256(secret), allowing
	// any node to decrypt /proxy/ URLs minted by any other node. Required when
	// cluster forwarding is enabled. Leave empty for standalone instances —
	// each lampac-go generates its own random key in cache_dir.
	SharedSecret string `toml:"shared_secret"`
}

// ---------------------------------------------------------------------------
//  Server proxy
// ---------------------------------------------------------------------------

type ServerProxyConfig struct {
	ResponseContentLength bool                   `toml:"response_content_length"`
	MaxLengthM3U          int64                  `toml:"max_length_m3u"`
	Image                 ServerProxyImageConfig `toml:"image"`
}

type ServerProxyImageConfig struct {
	Cache      bool `toml:"cache"`
	CacheRSize bool `toml:"cache_rsize"`
	CacheTime  int  `toml:"cache_time"`
}

// ---------------------------------------------------------------------------
//  Observability
// ---------------------------------------------------------------------------

type ObservabilityConfig struct {
	MetricsPath string `toml:"metrics_path"`
	HealthPath  string `toml:"health_path"`
	ReadyPath   string `toml:"ready_path"`
	AccessLog   bool   `toml:"access_log"`
	LogLevel    string `toml:"log_level"` // "debug", "info" (default), "warn", "error"

	// MetricsAuth controls access to /metrics and /debug/pprof/*.
	//   ""           — localhost-only (127.0.0.1/::1) (default; safe for internet-facing)
	//   "open"       — no auth (back-compat — only set this for trusted LANs)
	//   "<token>"    — require either Bearer <token> or basic auth "metrics:<token>"
	// When empty/missing, defaults to localhost-only — the safest behavior for
	// the common case where a server is exposed to the internet.
	MetricsAuth string `toml:"metrics_auth"`
}

// ---------------------------------------------------------------------------
//  TorrServer
// ---------------------------------------------------------------------------

type ParserConfig struct {
	JacRedHost string `toml:"jacred_host"`
	JacRedKey  string `toml:"jacred_apikey"`

	// Optional SECOND parser upstream (another JacRed/Jackett-compatible
	// host). Search endpoints are queried on both hosts concurrently and the
	// answers merged (dedup by info-hash, then title+size). Non-search
	// endpoints (/parse/*, /api/v1.0/conf) stay on the primary only.
	JacRedHost2 string `toml:"jacred_host2"`
	JacRedKey2  string `toml:"jacred_apikey2"`

	// Local (self-hosted) jacred-fdb instance managed by lampac-go: the
	// binary is downloaded from github.com/jacred-fdb/jacred, supervised as
	// a child process and used as the parser upstream instead of JacRedHost
	// (which remains the fallback when the local instance is down).
	JacRedLocal     bool   `toml:"jacred_local"`
	JacRedLocalPort int    `toml:"jacred_local_port"` // default 9117, 127.0.0.1 only
	JacRedHome      string `toml:"jacred_home"`       // default {repo_root}/jacred
	// Upstream jacred (opensync=true) to sync the base from. Empty = the
	// local instance parses trackers itself on the built-in schedule.
	JacRedSyncAPI string `toml:"jacred_syncapi"`
	// Download the prebuilt FDB archive on first start (multi-GB, one-off)
	// so search works immediately. nil/default = true.
	JacRedBootstrapDB *bool `toml:"jacred_bootstrap_db"`

	// RuTracker is a native indexer whose rows are merged into the jacred
	// answer. jacred cannot supply rutracker at all: the tracker needs a
	// logged-in account for both search and magnets, so it never lands in a
	// public open-sync base, and JacRed's own parser returns before its first
	// request when no credentials are set.
	RuTracker RuTrackerConfig `toml:"rutracker"`
}

// RuTrackerConfig is the [parser.rutracker] section. Disabled by default: it
// needs an account, and a shared account is a bannable offence on rutracker.
type RuTrackerConfig struct {
	Enable bool   `toml:"enable"`
	Host   string `toml:"host"` // default https://rutracker.org (mirrors: .net/.nl)

	// Login+Password authenticate against /forum/login.php. Cookie is a ready
	// bb_session (or a full "name=value; …" header copied from a browser) and
	// wins over the credentials — the escape hatch when the Cloudflare
	// challenge cannot be solved server-side.
	Login    string `toml:"login"`
	Password string `toml:"password"`
	Cookie   string `toml:"cookie"`

	// UseFlareSolverr allows escalating to [online] flaresolverr when
	// Cloudflare answers with a challenge. Default true (no-op when
	// FlareSolverr is not configured).
	UseFlareSolverr *bool `toml:"use_flaresolverr"`

	// OnlyAuthorized keeps rutracker rows out of anonymous answers. The
	// torrent endpoints are pre-auth by design (Lampa clients), and every
	// anonymous search burns our single account's request budget.
	OnlyAuthorized bool `toml:"only_authorized"`

	TimeoutSec       int `toml:"timeout_sec"`        // default 25
	MaxResults       int `toml:"max_results"`        // default 100
	ResolveTop       int `toml:"resolve_top"`        // default 12 — rows resolved to a real magnet inline
	ResolveBudgetSec int `toml:"resolve_budget_sec"` // default 8 — cap on that inline stage
	SearchTTLMin     int `toml:"search_ttl_min"`     // default 60
	MinIntervalMs    int `toml:"min_interval_ms"`    // default 800 — floor between outgoing requests
}

type TorrServerConfig struct {
	Port                   int    `toml:"port"`
	URL                    string `toml:"url"`
	Login                  string `toml:"login"`
	Password               string `toml:"password"`
	HomeDir                string `toml:"home_dir"`
	ExternalPlayerProtocol string `toml:"external_player_protocol"`
	// In-process torrent server settings (torrs build tag).
	//
	// RAMCache keeps torrent pieces in memory only — nothing torrent-sized is
	// ever written to <home_dir>/data. cache_size_mb becomes the real budget
	// (floor 128 MB) and pieces behind the player are evicted LRU, so a 40 GB
	// remux streams through a 2 GB cache. Off by default: a multi-user server
	// wants the disk cache, where the same 2 GB would thrash across viewers.
	RAMCache      bool `toml:"ram_cache"`      // RAM-only piece storage, default false
	CacheSizeMB   int  `toml:"cache_size_mb"`  // RAM mode: memory budget; disk mode: MatriX shim. default 64
	DiskCacheMB   int  `toml:"disk_cache_mb"`  // disk cache limit, default 1024
	PreloadMB     int  `toml:"preload_mb"`     // preload size, default 5
	DisableDHT    bool `toml:"disable_dht"`    // disable DHT, default false
	DisableUpload bool `toml:"disable_upload"` // disable upload, default true
	// Speed limits (0 = unlimited).
	MaxDownloadSpeedMB int `toml:"max_download_speed_mb"`
	MaxUploadSpeedMB   int `toml:"max_upload_speed_mb"`
	// Torrent limits (0 = unlimited).
	MaxActiveTorrents int `toml:"max_active_torrents"`
	// Auto-cleanup settings.
	CacheCleanupEnable bool `toml:"cache_cleanup_enable"`
	CacheCleanupDays   int  `toml:"cache_cleanup_days"`   // delete torrents older than N days, default 7
	CacheCleanupMaxGB  int  `toml:"cache_cleanup_max_gb"` // cleanup when disk > N GB, default 50
	// ExternalAccess controls third-party client access (TorrServe Android,
	// Vimu, MatriX Desktop UI, etc.). When Enable=true and credentials are
	// set, /ts/* accepts HTTP Basic Auth as an alternative to the lampac
	// cookie session.  Optionally a second dedicated listener can be exposed.
	ExternalAccess TorrServerExternalAccess `toml:"external_access"`
}

// TorrServerExternalAccess governs cross-client access to the in-process
// TorrServer.
type TorrServerExternalAccess struct {
	Enable     bool     `toml:"enable"`
	Login      string   `toml:"login"`
	Password   string   `toml:"password"`
	AllowFrom  []string `toml:"allow_from"`  // CIDR blocks; empty = any source IP
	ListenAddr string   `toml:"listen_addr"` // optional, e.g. ":9080" — extra listener exposing /ts/* with only Basic Auth
}

// ---------------------------------------------------------------------------
//  Telegram auth
// ---------------------------------------------------------------------------

type TelegramAuthConfig struct {
	Enable            bool           `toml:"enable"`
	BotToken          string         `toml:"bot_token"`
	AdminID           int64          `toml:"admin_id"`
	BotName           string         `toml:"bot_name"`
	MaxDevicesPerUser int            `toml:"max_devices_per_user"`
	AutoApprove       bool           `toml:"auto_approve"`
	AutoApproveDays   int            `toml:"auto_approve_days"`
	AuthPage          AuthPageStyle  `toml:"auth_page"`
	RequiredChats     []RequiredChat `toml:"required_chats" json:"required_chats,omitempty"`
	CheckIntervalMin  int            `toml:"check_interval_min" json:"check_interval_min,omitempty"` // membership check interval (default 60)
}

// RequiredChat defines a Telegram channel/group that users must be subscribed to.
// The bot must be an admin in the channel/group to check membership.
type RequiredChat struct {
	ChatID int64  `toml:"chat_id" json:"chat_id"` // channel/group ID (negative)
	Title  string `toml:"title"   json:"title"`   // display name
	Link   string `toml:"link"    json:"link"`    // t.me/telegram.me link for user (normalized to telegram.me on output)
}

// AuthPageStyle controls the appearance of the /tg/auth page.
type AuthPageStyle struct {
	Title       string            `toml:"title" json:"title"`
	Subtitle    string            `toml:"subtitle" json:"subtitle"`
	BgColor     string            `toml:"bg_color" json:"bg_color"`
	CardColor   string            `toml:"card_color" json:"card_color"`
	AccentColor string            `toml:"accent_color" json:"accent_color"`
	ButtonColor string            `toml:"button_color" json:"button_color"`
	TextColor   string            `toml:"text_color" json:"text_color"`
	LogoURL     string            `toml:"logo_url" json:"logo_url"`
	BgImageURL  string            `toml:"bg_image_url" json:"bg_image_url"`
	CustomCSS   string            `toml:"custom_css" json:"custom_css"`
	BlockRows   [][]string        `json:"block_rows,omitempty"`   // 2D: each row is a list of block IDs displayed side-by-side
	BlockStyles map[string]string `json:"block_styles,omitempty"` // per-block inline style overrides, e.g. {"qr":"width:120px","code":"font-size:36px"}
}

// ---------------------------------------------------------------------------
//  Admin auth
// ---------------------------------------------------------------------------

type AdminAuthConfig struct {
	Password string `toml:"password"`
}

// ---------------------------------------------------------------------------
//  Top-level auth mode — alternative login methods (password / none) that
//  coexist with TelegramAuth. The Mode field decides which path /bkit and
//  the auth gate steer anonymous visitors to.
// ---------------------------------------------------------------------------

// AuthMode values for AuthConfig.Mode.
const (
	AuthModeTG       = "tg"       // current TG-bot flow (default)
	AuthModePassword = "password" // username + password
	AuthModeNone     = "none"     // open access, anon UID auto-issued
)

// AuthConfig wraps the alternative user-facing auth methods. Default
// Mode="" is treated as "tg" so untouched configs preserve existing
// behaviour. Admins switch modes via /admin/api/auth-mode (persisted
// in database/auth/auth_mode.json which overrides the TOML on boot).
type AuthConfig struct {
	Mode     string             `toml:"mode" json:"mode"`
	Password PasswordAuthConfig `toml:"password" json:"password"`
	Anon     AnonAuthConfig     `toml:"anon" json:"anon"`
	// CookieSameSite controls the SameSite policy of the auth cookies.
	//   "" / "lax" → SameSite=Lax (DEFAULT). Correct for a same-origin SPA deploy (ALPAC client served
	//                by lampac) and — crucially — accepted by old Android/TV WebViews, which REJECT
	//                SameSite=None outright (login appears to succeed but never persists past a refresh).
	//   "none"     → SameSite=None;Secure. Only for a cross-origin Lampa fork on another domain that
	//                needs the cookie delivered on cross-site XHRs (won't work on pre-Chromium-67 WebViews).
	CookieSameSite string `toml:"cookie_samesite" json:"cookie_samesite"`
}

// PasswordAuthConfig governs the password-login implementation.
//
// Users are created exclusively by the admin via the v2 panel — there is
// no self-service registration. Each user owns a UID (auto-generated on
// create) plus an optional list of pre-bound device UIDs; logging in
// issues a session token that the existing auth middleware accepts in
// place of a TG token.
type PasswordAuthConfig struct {
	Enable            bool `toml:"enable" json:"enable"`                           // gate Mode="password" — admin still has to flip mode for users to see the form
	MinPasswordLen    int  `toml:"min_password_len" json:"min_password_len"`       // default 8
	MaxUsernameLen    int  `toml:"max_username_len" json:"max_username_len"`       // default 32
	MaxLoginAttempts  int  `toml:"max_login_attempts" json:"max_login_attempts"`   // per-IP per LockoutMinutes (default 5)
	LockoutMinutes    int  `toml:"lockout_minutes" json:"lockout_minutes"`         // default 10
	SessionDays       int  `toml:"session_days" json:"session_days"`               // cookie TTL (default 365)
	DefaultExpireDays int  `toml:"default_expire_days" json:"default_expire_days"` // when admin creates a user without explicit expiry (default 365, 0 = unlimited)
	MaxDevicesDefault int  `toml:"max_devices_default" json:"max_devices_default"` // per-user device cap on creation (default 3)
}

// AnonAuthConfig governs the Mode="none" path. Cookie issued silently on
// first visit, persisted in database/tgauth/anon_users.json so process
// restarts don't kick devices out.
type AnonAuthConfig struct {
	SessionDays int `toml:"session_days" json:"session_days"` // anon cookie TTL (default 30)
}

// ---------------------------------------------------------------------------
//  Proxy (VLESS + Direct)
// ---------------------------------------------------------------------------

// ProxyConfig wraps both VLESS sidecar and direct proxy entries.
type ProxyConfig struct {
	Vless  ProxyVlessConfig  `toml:"vless"`
	Direct ProxyDirectConfig `toml:"direct"`
}

type ProxyVlessConfig struct {
	URI       string            `toml:"uri"`
	Balancers []string          `toml:"balancers"`
	Entries   []ProxyVlessEntry `toml:"entries"`
}

type ProxyVlessEntry struct {
	URI       string   `toml:"uri"       json:"uri"`
	Balancers []string `toml:"balancers" json:"balancers"`
	Label     string   `toml:"label"     json:"label"`
	Engine    string   `toml:"engine"    json:"engine"`
}

type ProxyDirectConfig struct {
	Entries []ProxyDirectEntry `toml:"entries"`
}

type ProxyDirectEntry struct {
	URI       string   `toml:"uri"       json:"uri"`
	Balancers []string `toml:"balancers" json:"balancers"`
	Label     string   `toml:"label"     json:"label"`
}

// ---------------------------------------------------------------------------
//  ProxyCore — Built-in proxy engine
// ---------------------------------------------------------------------------

// ProxyCoreConfig configures the built-in proxy engine.
type ProxyCoreConfig struct {
	BasePort int              `toml:"base_port" json:"base_port"`
	Entries  []ProxyCoreEntry `toml:"entries"   json:"entries"`
	Rules    []ProxyCoreRule  `toml:"rules"     json:"rules"`
}

// ProxyCoreEntry is a single proxy endpoint.
type ProxyCoreEntry struct {
	URI       string   `toml:"uri"       json:"uri"`
	Label     string   `toml:"label"     json:"label"`
	Balancers []string `toml:"balancers" json:"balancers"`
	Engine    string   `toml:"engine"    json:"engine"` // "proxycore" (default), "xray", "mihomo"
}

// ProxyCoreRule defines routing between balancers and proxies.
type ProxyCoreRule struct {
	ID        string   `toml:"id"        json:"id"`
	Balancers []string `toml:"balancers" json:"balancers"`
	Proxy     string   `toml:"proxy"     json:"proxy"` // proxy label or ID
	Fallback  string   `toml:"fallback"  json:"fallback"`
	Mode      string   `toml:"mode"      json:"mode"` // "fixed", "latency", "round-robin"
}

// ---------------------------------------------------------------------------
//  Sync
// ---------------------------------------------------------------------------

type SyncConf struct {
	Enable       bool              `toml:"enable"        json:"enable"`
	Type         string            `toml:"type"          json:"type"`
	InitConf     string            `toml:"initconf"      json:"initconf"`
	SyncFull     bool              `toml:"sync_full"     json:"sync_full"`
	APIHost      string            `toml:"api_host"      json:"api_host"`
	APIPasswd    string            `toml:"api_passwd"    json:"api_passwd"`
	OverrideConf map[string]string `toml:"override_conf" json:"override_conf"`
}

// ---------------------------------------------------------------------------
//  Mirror (thin edge → central origin / "beta")
// ---------------------------------------------------------------------------

// MirrorConfig turns this instance into a thin "mirror" edge: a full
// lampac-go that resolves catalog and streams /proxy locally (so video
// bandwidth never touches the origin), but delegates the auth control-plane
// — token validation, the auth gate decision, the bot/pending-code flow,
// and (Фаза 4) user-state — to a central origin ("beta") over HTTP.
//
// This is orthogonal to [cluster]: cluster spreads OUTBOUND balancer fetches
// across backend nodes; mirror centralizes the INBOUND control-plane so new
// public domains/IPs can be spun up as disposable fronts for one brain.
type MirrorConfig struct {
	// Enable turns on mirror mode. When true the local TG auth gate is
	// replaced by a delegating gate that asks APIHost for each decision,
	// and the auth middleware resolves users via APIHost instead of (or in
	// addition to) a local token store.
	Enable bool `toml:"enable" json:"enable"`
	// APIHost is the control-plane base URL of the origin (beta), e.g.
	// "http://10.8.0.1:18118". SHOULD be a stable private/secondary address
	// distinct from beta's public user-facing domain, so blocking beta's
	// public domain does not sever the whole mirror fleet's control-plane.
	APIHost string `toml:"api_host" json:"api_host"`
	// APIPasswd is the shared secret authenticating mirror→origin control
	// calls (sent as the "localrequest" header, same scheme as [sync]).
	// Must match the origin's [sync] api_passwd or [mirror] api_passwd.
	APIPasswd string `toml:"api_passwd" json:"api_passwd"`
	// CacheTTLSec is how long a positive auth decision is cached on the
	// mirror before re-validating against the origin (default 60).
	CacheTTLSec int `toml:"cache_ttl_sec" json:"cache_ttl_sec"`
	// GraceTTLSec is how long a previously-valid (now stale) decision keeps
	// being honored while the origin is unreachable, so a brief origin
	// outage doesn't log the whole fleet out (default 900 = 15 min).
	GraceTTLSec int `toml:"grace_ttl_sec" json:"grace_ttl_sec"`
	// SyncPluginsMin is the interval (minutes) at which the mirror pulls
	// ubuntu/plugins/* from the origin (default 5). 0 disables plugin sync.
	SyncPluginsMin int `toml:"sync_plugins_min" json:"sync_plugins_min"`
	// SyncWwwroot also pulls the wwwroot/ tree (the Lampa web client) from the
	// origin on the same interval. Off by default — wwwroot is large and
	// changes rarely; enable when the origin owns the web build too.
	SyncWwwroot bool `toml:"sync_wwwroot" json:"sync_wwwroot"`
	// CentralizeState (Фаза 4) proxies per-user state — bookmarks, resume
	// timecodes, the Lampa sync storage, IPTV playlists — to the origin, so a
	// user's profile follows them across mirrors. On by default. IPTV /play
	// returns a /proxy/ URL signed with the origin's secret, so for IPTV the
	// mirror and origin must share [proxy_link] shared_secret.
	CentralizeState *bool `toml:"centralize_state" json:"centralize_state"`
}

// CentralizeStateEnabled reports whether per-user state is centralized
// (defaults to true when unset, so existing mirror configs opt in).
func (m MirrorConfig) CentralizeStateEnabled() bool {
	return m.CentralizeState == nil || *m.CentralizeState
}

// Normalize clamps mirror settings to sane values. Safe to call on a
// zero-value (disabled) config.
func (m *MirrorConfig) Normalize() {
	if m.CacheTTLSec <= 0 {
		m.CacheTTLSec = 60
	}
	if m.GraceTTLSec <= 0 {
		m.GraceTTLSec = 900
	}
	m.APIHost = strings.TrimRight(strings.TrimSpace(m.APIHost), "/")
}

// ---------------------------------------------------------------------------
//  Cluster
// ---------------------------------------------------------------------------

// ClusterConfig controls load-balancing across multiple lampac-go instances.
//   - mode "primary" — this server distributes /lite/* requests to nodes
//   - mode "node"    — this server receives forwarded requests from primary
type ClusterConfig struct {
	Enable bool          `toml:"enable"  json:"enable"`
	Mode   string        `toml:"mode"    json:"mode"`    // "primary" | "node"
	APIKey string        `toml:"api_key" json:"api_key"` // shared secret for inter-node auth
	Nodes  []ClusterNode `toml:"nodes"   json:"nodes"`   // backend nodes (primary only)
}

type ClusterNode struct {
	Host   string `toml:"host"   json:"host"`   // e.g. "http://192.168.1.10:8888"
	Weight int    `toml:"weight" json:"weight"` // relative weight for least-connections (default 1)
}

// ---------------------------------------------------------------------------
//  TMDB Proxy
// ---------------------------------------------------------------------------

// TMDBProxyConf controls how Lampa clients reach the TMDB API.
//   - "self"     — requests are proxied through this lampac-go instance (/tmdb/*)
//   - "alcopa"   — requests are proxied through a dedicated host (e.g. tmdb.alcopa.cc)
//   - "disabled" — no proxying; clients connect to TMDB directly
type TMDBProxyConf struct {
	Mode               string `toml:"mode"    json:"mode"`                              // self | alcopa | disabled
	Host               string `toml:"host"    json:"host"`                              // host for "alcopa" mode (default: tmdb.alcopa.cc)
	APIKey             string `toml:"api_key" json:"api_key"`                           // TMDB API key (injected if client doesn't provide one)
	APIHost            string `toml:"api_host" json:"api_host"`                         // upstream API host (default: apitmdb.cub.red)
	IMGHost            string `toml:"img_host" json:"img_host"`                         // upstream image host (default: imagetmdb.com)
	CacheMaxItems      int    `toml:"cache_max_items" json:"cache_max_items"`           // max cached API responses (default 50000)
	CacheTTLMin        int    `toml:"cache_ttl_min" json:"cache_ttl_min"`               // base cache TTL in minutes (default 120)
	UpstreamTimeoutSec int    `toml:"upstream_timeout_sec" json:"upstream_timeout_sec"` // per-upstream timeout in seconds (default 8)
	// Bulkheads: cap concurrent upstream fetches so a slow/geo-blocked TMDB CDN can't pile up unbounded
	// goroutines+sockets+buffers and starve the rest of the server (the "everything got slow" incident —
	// /tmdb/img has no server cache, so every client miss proxies upstream). When the cap is reached,
	// excess requests fail fast (img → 503 no-store so the client retries; api → empty fallback / stale)
	// instead of blocking the ~8s upstream timeout. 0 = default.
	MaxConcurrentIMG int `toml:"max_concurrent_img" json:"max_concurrent_img"` // concurrent image fetches (default 64)
	MaxConcurrentAPI int `toml:"max_concurrent_api" json:"max_concurrent_api"` // concurrent API fetches (default 48)
	// ImgCacheMaxMB enables a bounded on-DISK LRU cache for TMDB images (posters are
	// immutable + requested by many clients). Disk, not RAM, so RSS stays flat. 0 =
	// disabled (default). ImgCacheDir overrides the location (default database/tmdb-img).
	ImgCacheMaxMB int    `toml:"img_cache_max_mb" json:"img_cache_max_mb"`
	ImgCacheDir   string `toml:"img_cache_dir" json:"img_cache_dir"`
}

// ---------------------------------------------------------------------------
//  Transcoding
// ---------------------------------------------------------------------------

type TranscodingConf struct {
	Enable   bool   `toml:"enable"               json:"enable"`
	FFmpeg   string `toml:"ffmpeg"               json:"ffmpeg"`
	TempRoot string `toml:"temp_root"            json:"tempRoot"`

	// RemoteHost offloads transcoding to a SEPARATE dedicated box. On the MAIN server set it to the
	// box's public base URL (e.g. "https://tc.alcopa.cc"); minted /transcoding/start.m3u8 URLs then
	// point at the box and the client fetches HLS straight from it (CPU + segment traffic leave the
	// main server entirely). Empty = transcode locally (default, unchanged). The BOX itself runs with
	// RemoteHost empty + Enable=true + ffmpeg/GPU; it must be publicly reachable over HTTPS with CORS.
	RemoteHost string `toml:"remote_host" json:"remoteHost"`
	// RemoteSecret is the shared HMAC key tying the main server to the box. The main server SIGNS each
	// start URL (over src+exp); the box (same secret) VERIFIES it before spawning ffmpeg — without it
	// the box's /transcoding/start would be an open SSRF + CPU-abuse endpoint. Set the SAME value on
	// both. When set on a box, every start request MUST carry a valid exp+sig or it's 403'd.
	RemoteSecret    string `toml:"remote_secret" json:"-"`
	IdleTimeoutSec  int    `toml:"idle_timeout_sec"     json:"idleTimeoutSec"`
	IdleTimeoutLive int    `toml:"idle_timeout_sec_live" json:"idleTimeoutSec_live"`
	DefaultSubs     bool   `toml:"default_subtitles"    json:"defaultSubtitles"`
	MaxConcurrent   int    `toml:"max_concurrent_jobs"  json:"maxConcurrentJobs"`
	// MinFreeMemMB: when > 0, the scheduler rejects a new job (503 Retry-After)
	// while OS available memory is below this, instead of letting the kernel
	// OOM-kill a running ffmpeg mid-playback. Recommended on transcode boxes
	// handling 4K/HEVC/DoVi (a single such job peaks at 3-4 GB): set to ~1.5×
	// the peak of one job. 0 = off (default; no behaviour change).
	MinFreeMemMB int `toml:"min_free_mem_mb" json:"minFreeMemMB"`
	// MultiAudio ("hls4"): sources with ≥2 compatible audio tracks get every
	// track copied to a per-track shelf by the main job, an EXT-X-MEDIA AUDIO
	// group in master.m3u8, and lazy per-track AAC renditions — the player
	// switches озвучку instantly, without restarting the session or
	// re-reading the source. CPU is only spent on the track being listened
	// to. true by default; set false to fall back to restart-based switching.
	MultiAudio bool `toml:"multi_audio" json:"multiAudio"`
	// MaxConcurrentCapi is a SEPARATE concurrency pool for capi / ALPAC-TV
	// playback (requests tagged tcpool=capi). 0 = capi shares the main
	// max_concurrent_jobs pool (default, no behaviour change). When > 0 the
	// TV clients get their own K slots so standard-Lampa traffic filling the
	// main pool can't starve them with 503s. NOTE: the two pools are
	// independent, so the worst-case ffmpeg count is max_concurrent_jobs +
	// max_concurrent_capi — size the sum for your CPU.
	MaxConcurrentCapi int      `toml:"max_concurrent_capi"  json:"maxConcurrentCapi"`
	AllowHosts        []string `toml:"allow_hosts"          json:"allowHosts"`

	// AllowPrivateSrc permits CLIENT-supplied transcode sources that point at
	// private/loopback IPs. Default off: a Lampa user with a HOME-LAN TorrServer
	// (src=http://192.168.x.x:8090/stream/...) makes the server dial an address
	// it can never reach — each attempt burned ffmpeg + 3 auto-restarts × 30s
	// and showed the user an endless «Загрузка…» instead of an explanation.
	// Rejecting also closes the SSRF corner. Self-hosted LAN deployments whose
	// own TorrServer host is private are exempt automatically; this flag is the
	// escape hatch for anything more exotic.
	AllowPrivateSrc bool `toml:"allow_private_src"    json:"allowPrivateSrc"`

	// PipeEnable turns on the experimental in-memory fMP4 pipe transcoding
	// path (no disk): ffmpeg streams fragmented MP4 to stdout, the server
	// splits it into CMAF segments held in RAM and serves them directly,
	// pacing ffmpeg via pipe backpressure. Opt-in; the disk-based HLS path
	// is unaffected. Routes live under /transcoding/pipe/*.
	PipeEnable bool `toml:"pipe_enable" json:"pipeEnable"`

	// MaxTranscodeHeight caps the output height for SW video re-encodes.
	// When the source is taller than this, the encoder downscales to it
	// (scale=-2:<cap>) — a single 4K libx264 re-encode is ~4× a 1080p one
	// and cannot run realtime on a CPU, so the default keeps the box from
	// drowning. 0 disables the cap (use only with a HW encoder / GPU, where
	// raise it to 2160 for true 4K). Only affects video that is actually
	// re-encoded; remux / audio-only / native paths are untouched.
	MaxTranscodeHeight int `toml:"max_transcode_height" json:"maxTranscodeHeight"`

	// WindowAheadHigh / WindowAheadLow bound how far each VOD transcode runs
	// ahead of the viewer, in segments. When ffmpeg has produced
	// WindowAheadHigh segments past the last one the player fetched, the
	// process is SIGSTOP-paused (0 CPU, and the torrent/HTTP read pauses with
	// it); when the player drains the buffer back down to WindowAheadLow
	// remaining, it is SIGCONT-resumed. This is the big lever for "many
	// users": a paused / stalled / abandoned stream stops burning CPU, disk
	// and bandwidth instead of churning ahead at -readrate. Watched segments
	// are pruned by the existing cleanup loop. WindowAheadHigh <= 0 disables
	// the pacer (falls back to plain -readrate). Default 22 / 12 (≈132s/72s
	// at 6s segments). Unix only (SIGSTOP/SIGCONT); a no-op on Windows.
	WindowAheadHigh int `toml:"window_ahead_high" json:"windowAheadHigh"`
	WindowAheadLow  int `toml:"window_ahead_low"  json:"windowAheadLow"`

	// DisableNativePlayback — opt-out of the mode=native short-circuit.
	// When false (default), if the client reports it can play the source
	// container + codecs natively, Start() returns mode=native and the
	// plugin skips interception entirely, saving CPU.  Set true to force
	// transcoding for every clip regardless of client capabilities.
	DisableNativePlayback bool `toml:"disable_native_playback" json:"disableNativePlayback"`

	// DisableHWAccel — opt-out of hardware acceleration auto-detect.
	// When false (default) the service probes ffmpeg for NVENC/QSV/VAAPI/
	// VideoToolbox/RKMPP on startup and uses the first working backend for
	// video re-encode jobs.  Set true to force software libx264 encoding.
	DisableHWAccel bool `toml:"disable_hw_accel" json:"disableHWAccel"`

	// DiskBudgetMB caps the total size of the transcoding temp root.
	// When the live usage exceeds the budget, the scheduler evicts the
	// oldest completed jobs until usage drops below 80% of the limit.
	// 0 (default) means no limit — useful for hosts with dedicated volumes.
	DiskBudgetMB int `toml:"disk_budget_mb" json:"diskBudgetMB"`

	// P3.O — operator-tunable policy knobs added in the May-2026
	// transcoding rewrite.  All have safe defaults so the zero-value
	// config matches the behaviour shipped earlier.

	// DisableABRLadder turns off the ABR multi-rendition ladder
	// (P3.K + K2 + N).  When true, every job runs single-rendition
	// regardless of source resolution — even on machines with idle
	// HW capacity.  Useful for low-bandwidth deployments where the
	// extra encoding cost would outweigh the player-adaptation benefit.
	DisableABRLadder bool `toml:"disable_abr_ladder" json:"disableABRLadder"`

	// DisableSubtitleBurnIn forces the subtitle planner to never pick
	// the burn-in path (P3.C) — bitmap subs are silently skipped, ASS
	// always extracts to WebVTT regardless of profile.  Saves CPU on
	// VPS deployments where video re-encode is expensive.
	DisableSubtitleBurnIn bool `toml:"disable_subtitle_burn_in" json:"disableSubtitleBurnIn"`

	// DisableFastStart turns off the P3.F probe-tuning + hls_init_time
	// fast-start hacks.  Only useful when chasing playback bugs on
	// exotic sources where the smaller probesize trips up codec
	// detection — for normal CDN sources fast-start is a strict win.
	DisableFastStart bool `toml:"disable_fast_start" json:"disableFastStart"`

	// DisablePrewarmProbe disables the probe pre-warm for next-up URLs
	// declared via TranscodingStartRequest.PrewarmNextURLs (P3.L).
	// Useful when the operator wants tight scheduler-slot accounting
	// without speculative work.
	DisablePrewarmProbe bool `toml:"disable_prewarm_probe" json:"disablePrewarmProbe"`

	// MaxLadderRungs caps how many ABR rungs the planner emits even
	// when the source resolution would warrant more.  0 (default) =
	// no cap, planner picks based on source.  Set to 2 to cap at
	// primary + one downscale; useful for HW backends with limited
	// concurrent encode sessions.
	MaxLadderRungs int `toml:"max_ladder_rungs" json:"maxLadderRungs"`

	// ForceABROnLegacyFFmpeg opts into ABR multi-rendition output on
	// ffmpeg < 5 (Ubuntu 20.04 ships 4.2.7, Debian 11 ships 4.3.x).
	// var_stream_map + per-variant subdir output technically work on
	// 4.x but predate the stability fixes that landed in 5.0; the
	// default-deny gate keeps surprise breakage off legacy hosts.
	// Operators who tested multi-rung against their specific 4.x
	// build can flip this to true.
	ForceABROnLegacyFFmpeg bool `toml:"force_abr_on_legacy_ffmpeg" json:"forceABROnLegacyFFmpeg"`

	HLS      TranscodingHLSOptions      `toml:"hls"                  json:"hlsOptions"`
	Audio    TranscodingAudioOptions    `toml:"audio"                json:"audioOptions"`
	Subtitle TranscodingSubtitleOptions `toml:"subtitle"             json:"subtitleOptions"`
	Playlist TranscodingPlaylistOptions `toml:"playlist"             json:"playlistOptions"`
	Convert  TranscodingConvertOptions  `toml:"convert"              json:"convertOptions"`
	Command  map[string][]string        `toml:"command"              json:"comand"`
}

type TranscodingHLSOptions struct {
	SegDur  int                 `toml:"seg_dur"  json:"segDur"`
	WinSize int                 `toml:"win_size" json:"winSize"`
	FMP4    bool                `toml:"fmp4"     json:"fmp4"`
	Command map[string][]string `toml:"command"  json:"comand"`
}

type TranscodingAudioOptions struct {
	Index         int                 `toml:"index"           json:"index"`
	BitrateKbps   int                 `toml:"bitrate_kbps"    json:"bitrateKbps"`
	Stereo        bool                `toml:"stereo"          json:"stereo"`
	CodecCopy     []string            `toml:"codec_copy"      json:"codec_copy"`
	CommandTransc map[string][]string `toml:"command_transcode" json:"comand_transcode"`
}

type TranscodingPlaylistOptions struct {
	ReadRate       float64 `toml:"readrate"         json:"readrate"`
	Burst          int     `toml:"burst"            json:"burst"`
	DeleteSegments bool    `toml:"delete_segments"  json:"delete_segments"`
}

type TranscodingConvertOptions struct {
	TranscodeVideo bool                `toml:"transcode_video" json:"transcodeVideo"`
	Codec          []string            `toml:"codec"           json:"codec"`
	Command        map[string][]string `toml:"command"         json:"comand"`
}

type TranscodingSubtitleOptions struct {
	Codec   []string `toml:"codec"   json:"codec"`
	Command []string `toml:"command" json:"comand"`
}

// ---------------------------------------------------------------------------
//  LLM
// ---------------------------------------------------------------------------

type LLMConfig struct {
	Endpoint   string  `toml:"endpoint"`
	ApiKey     string  `toml:"api_key"`
	Model      string  `toml:"model"`
	Temp       float64 `toml:"temp"`
	MaxRetries int     `toml:"max_retries"`
}

// ---------------------------------------------------------------------------
//  Kit
// ---------------------------------------------------------------------------

type KitConfig struct {
	Enable         bool   `toml:"enable"`
	CacheToSeconds int    `toml:"cache_to_seconds"`
	ServerHost     string `toml:"server_host"`
	// ServerHostCamel accepts the legacy camelCase key "serverHost" that some
	// installs ended up with (admin UI / older docs wrote this name). Loader
	// merges it into ServerHost so the bot's /kit and /remote handlers — which
	// only read ServerHost — keep working without manual TOML edits.
	ServerHostCamel string   `toml:"serverHost,omitempty"`
	Encrypt         bool     `toml:"encrypt"`         // encrypt sensitive fields (token/cookie) at rest
	HiddenServices  []string `toml:"hidden_services"` // services hidden from Kit UI (e.g. ["rezka"])
	// BindsDisabled hides the "Привязки" tab (user-side source account/token
	// binding) from the /kit and /bkit pages and refuses /api/kit/bind/*
	// requests. The Балансеры and Профиль tabs stay available. Default false
	// (binds allowed) so existing installs keep their current behaviour.
	BindsDisabled bool `toml:"binds_disabled"`
}

// ---------------------------------------------------------------------------
//  Browser Pool (chromedp / headless Chrome)
// ---------------------------------------------------------------------------

type BrowserPoolConfig struct {
	MaxConcurrent   int `toml:"max_concurrent"`     // chromedp semaphore size (parallel browser sessions)
	StreamCacheMax  int `toml:"stream_cache_max"`   // max entries in stream URL cache per balancer
	StreamCacheTTLH int `toml:"stream_cache_ttl_h"` // cache eviction age in hours

	// Engine selects the headless-browser backend used by balancers that
	// go through the internal/browser facade. Valid values: "chromedp"
	// (default), "rod", "playwright". Values not compiled into this
	// binary are ignored — the registry falls back to chromedp.
	Engine string `toml:"engine"`

	// BalancerEngines overrides Engine on a per-balancer basis, e.g.
	// `mirage = "rod"`. Keys are balancer names; values follow the same
	// rules as Engine.
	BalancerEngines map[string]string `toml:"balancer_engines"`
}

// ---------------------------------------------------------------------------
//  YouTube
// ---------------------------------------------------------------------------

type YouTubeConfig struct {
	// Proxy controls whether yt-dlp uses the VLESS/AntiDPI proxy.
	// "auto" (default) — use proxy if available (AntiDPI > VLESS).
	// "none" — never proxy yt-dlp extraction (use when YT is accessible directly).
	// "antidpi" — force AntiDPI only.
	// "vless" — force VLESS only.
	Proxy          string `toml:"proxy"`
	ExtractTimeout int    `toml:"extract_timeout"` // yt-dlp extraction timeout in seconds (default 60)
	// fetch_pot: "auto" (default) — bgutil-плагин сам решает, когда вешать PO Token;
	// "never" — жёстко запретить (старое поведение: до 2026-08-14 POT-URL были ядом,
	// после — CDN отдаёт БЕЗ POT только первые ~20 МиБ файла, и never ломает длинные видео).
	FetchPot string `toml:"fetch_pot"`
	// VOT (voice-over-translation) powers the player's «Перевод» button on YouTube videos. It runs an
	// embedded Node sidecar (vot.js, bundled into the binary) that asks the public Yandex VOT service
	// for a Russian voice-over track; the server then proxies the resulting mp3 to the client. The only
	// requirement is a Node 18+ runtime on the server — enabled by default when `node` is found.
	VotEnable   *bool  `toml:"vot_enable"`    // nil/true = on (when node is present); false = off
	VotNodePath string `toml:"vot_node_path"` // path to the node binary (default: "node" from PATH)
	VotWorker   string `toml:"vot_worker"`    // optional vot-worker host (e.g. "vot-worker.toil.cc"); empty = talk to Yandex directly
	VotLang     string `toml:"vot_lang"`      // source language of the video (from), default "en"
	VotToLang   string `toml:"vot_to_lang"`   // target language, default "ru"
}

// VotEnabled reports whether the YouTube voice-over-translation feature is on (default true; an
// actual node binary still has to be found at runtime for it to work).
func (c YouTubeConfig) VotEnabled() bool {
	return c.VotEnable == nil || *c.VotEnable
}

// ---------------------------------------------------------------------------
//  YouTube OAuth
// ---------------------------------------------------------------------------

type YouTubeOAuthConfig struct {
	ClientID     string `toml:"client_id"`
	ClientSecret string `toml:"client_secret"`
}

// ---------------------------------------------------------------------------
//  AntiDPI (DPI bypass for YouTube in Russia)
// ---------------------------------------------------------------------------

type AntiDPIConfig struct {
	Enable   bool   `toml:"enable"`   // toggle on/off
	Listen   string `toml:"listen"`   // SOCKS5 listen address (default "127.0.0.1:9898")
	Strategy string `toml:"strategy"` // "auto" | "split-1" | "split-2" | "split-sni" | "none"
}

// ---------------------------------------------------------------------------
//
//	Zapret (kernel-level DPI bypass via bol-van/zapret)
//
// ---------------------------------------------------------------------------
//
// When enabled, lampac wires nftables → NFQUEUE → nfqws (the zapret userspace
// daemon) so that any TCP traffic matching the configured ports gets desync'd
// before it leaves the box. This is more aggressive than the [antidpi] SOCKS5
// (which only affects clients we own) and is the recommended path for TSPU on
// Russian VPS.
//
// Requires: nftables (`nft` binary), CAP_NET_ADMIN (effectively root), and
// the `nfqws` binary from https://github.com/bol-van/zapret built/installed
// on the host. Set `binary` if it isn't on PATH.
type ZapretConfig struct {
	Enable       bool     `toml:"enable"`
	Binary       string   `toml:"binary"`        // path to nfqws; "" = auto-locate
	QueueNum     int      `toml:"queue_num"`     // NFQUEUE number; default 200
	TCPPorts     []int    `toml:"tcp_ports"`     // TCP dports to queue; default [443, 80]
	Strategies   []string `toml:"strategies"`    // --dpi-desync values; default ["fake","split2"]
	DesyncTTL    int      `toml:"desync_ttl"`    // TTL for fake packets; default 8
	HostlistPath string   `toml:"hostlist_path"` // optional file with extra hosts
	HostlistOnly bool     `toml:"hostlist_only"` // when true, do NOT merge built-in DefaultHosts
	NFTable      string   `toml:"nft_table"`     // nftables table name; default "lampac_zapret"
	Permissive   bool     `toml:"permissive"`    // soft-fail (log + continue) on setup error; default true
	ExtraArgs    []string `toml:"extra_args"`    // raw nfqws flags appended after defaults
	AutoInstall  *bool    `toml:"auto_install"`  // nil=default (true), false=disable; auto apt-install nftables+build nfqws when missing
}

// ---------------------------------------------------------------------------
//  File Cache Inactive (TTL-based cache cleanup)
// ---------------------------------------------------------------------------

type FileCacheConfig struct {
	FreeDiskSpace int64 `toml:"free_disk_space"` // bytes threshold for emergency cleanup; default 1 GB
	HTML          int   `toml:"html"`            // TTL in minutes; default 5
	Torrent       int   `toml:"torrent"`         // TTL in minutes; default 2880 (48 h)
	HLS           int   `toml:"hls"`             // TTL in minutes; default 90
}

// ---------------------------------------------------------------------------
//  DLNA — File browser, streaming, torrent integration
// ---------------------------------------------------------------------------

type DLNAConfig struct {
	Enable              bool            `toml:"enable"`
	Path                string          `toml:"path"`          // directory for DLNA files; default "dlna"
	MediaPattern        string          `toml:"media_pattern"` // regex for media file extensions
	AutoUpdateTrackers  bool            `toml:"autoupdate_trackers"`
	AddTrackersToMagnet bool            `toml:"add_trackers_to_magnet"`
	IntervalUpdateTrack int             `toml:"interval_update_trackers"` // minutes
	Mode                string          `toml:"mode"`                     // "stream" | "download"
	DownloadSpeed       int             `toml:"download_speed"`           // bytes/sec
	UploadSpeed         int             `toml:"upload_speed"`             // bytes/sec
	UPnP                bool            `toml:"upnp"`                     // enable UPnP/DLNA server (SSDP discovery)
	UPnPPort            int             `toml:"upnp_port"`                // UPnP HTTP port; default 1338
	FriendlyName        string          `toml:"friendly_name"`            // DLNA device name; default "Lampac DLNA"
	Cover               DLNACoverConfig `toml:"cover"`
}

type DLNACoverConfig struct {
	Enable         bool   `toml:"enable"`
	Preview        bool   `toml:"preview"`         // generate short video previews
	Timeout        int    `toml:"timeout"`         // check interval in minutes
	SkipModTime    int    `toml:"skip_mod_time"`   // skip files modified < N minutes ago
	Extension      string `toml:"extension"`       // regex for media files to process
	CoverCommand   string `toml:"cover_command"`   // FFmpeg command template for thumbnails
	PreviewCommand string `toml:"preview_command"` // FFmpeg command template for previews
}

// ---------------------------------------------------------------------------
//  Kinopoisk Unofficial API (alternative catalog source)
// ---------------------------------------------------------------------------

type KinopoiskConfig struct {
	Token string `toml:"token"` // X-API-KEY for kinopoiskapiunofficial.tech
	// KpByTmdb: extra TMDB→Kinopoisk-id resolver endpoints, each a base URL WITH its ?token=... query
	// (the code appends &tmdb=<id>). They answer {"status":"success","data":{"id_kp":N,"category":1|2}}
	// and resolve DIRECTLY by tmdb id — a kp-id tier between Wikidata and kinopoiskapiunofficial.
	// e.g. ["https://upn.stull.xyz/?token=XXX", "https://api.apbugall.org/?token=YYY"]
	KpByTmdb []string `toml:"kp_by_tmdb"`
}

// CalendarConfig controls the content calendar (upcoming episodes + TG notifications).
type CalendarConfig struct {
	Enable           bool      `toml:"enable"`
	CheckIntervalMin int       `toml:"check_interval_min"` // TMDB poll interval; default 120
	NotifyTG         bool      `toml:"notify_tg"`          // send TG notifications; default true
	UpcomingDays     int       `toml:"upcoming_days"`      // show episodes N days ahead; default 7
	RecentDays       int       `toml:"recent_days"`        // show aired episodes N days back; default 3
}

// IPTVConfig controls the server-side IPTV feature.
type IPTVConfig struct {
	Enable          bool     `toml:"enable"`
	EPGUrls         []string `toml:"epg_urls"`         // global EPG sources
	EPGUpdateHours  int      `toml:"epg_update_hours"` // default 6
	MaxPlaylists    int      `toml:"max_playlists"`    // per user, default 10
	DefaultProxy    string   `toml:"default_proxy"`    // "none" | "all"; default "none"
	GlobalPlaylists []string `toml:"global_playlists"` // M3U URLs available to all users
	// MergeGlobal presents all global_playlists as ONE deduplicated list ("Все каналы") instead
	// of N separate playlists — for aggregating several m3u.su category lists. Default true.
	MergeGlobal    *bool `toml:"merge_global"`
	HealthCheck    bool  `toml:"healthcheck"`     // periodically probe global streams, drop dead ones
	HealthCheckMin int   `toml:"healthcheck_min"` // probe interval in minutes; default 30
	// HealthCheckShallow reverts the HLS probe to reading two bytes off the manifest.
	// The default walks the channel to its first segment instead: a dead channel almost
	// always still serves its playlist, so the shallow check marks it alive forever.
	// Costs ~16KB per HLS channel per cycle. Set true only if that traffic is a problem.
	HealthCheckShallow bool `toml:"healthcheck_shallow"`
	// HealthCheckStale additionally re-reads each live playlist after a pause to catch a
	// FROZEN channel (200 forever, picture is a still frame). Off by default: it adds an
	// 8s wait per channel, which does not scale to a few thousand of them.
	HealthCheckStale bool `toml:"healthcheck_stale"`
	// Previews enables live channel-card thumbnails: the server grabs ONE frame from the live
	// stream via ffmpeg on demand (when a card is visible) and caches it. Opt-in — it spawns
	// ffmpeg jobs, so it's off by default to protect the box. Needs ffmpeg (transcoding.ffmpeg).
	Previews           bool `toml:"previews"`
	PreviewConcurrency int  `toml:"preview_concurrency"` // max parallel frame-grabs; default 3
	PreviewTTLSec      int  `toml:"preview_ttl_sec"`     // cached frame lifetime in seconds; default 600
	// Econom: on-demand server-side re-encode of a live channel to a lower resolution/bitrate for
	// viewers whose connection can't sustain the channel's native bitrate (single-bitrate FHD/sports
	// channels ship 12–14 MB segments — a thin pipe can't keep up, no client fix helps). Off by
	// default: it spawns a per-channel ffmpeg (shared across viewers of the same channel) and needs
	// transcoding enabled. The client requests it explicitly (?econom=1) as an «Эконом» quality.
	EconomEnable    bool `toml:"econom_enable"`
	EconomMaxHeight int  `toml:"econom_max_height"` // downscale cap for econom; default 720
}

// MergeGlobalEnabled reports whether global playlists should be merged (default true when unset).
func (c IPTVConfig) MergeGlobalEnabled() bool {
	return c.MergeGlobal == nil || *c.MergeGlobal
}

// SkipIntroConfig controls the intro/outro skip feature.
type SkipIntroConfig struct {
	Enable          bool `toml:"enable"`
	IntroDefaultSec int  `toml:"intro_default_sec"` // 0 = only use DB data, >0 = fallback intro [0, N]
	OutroOffsetSec  int  `toml:"outro_offset_sec"`  // 0 = disabled
	EnableUserMarks bool `toml:"enable_user_marks"` // allow POST /api/skip/mark
}

// CollectionsConfig controls TMDB smart collections (person, genre, studio).
type CollectionsConfig struct {
	Enable      bool `toml:"enable"`
	CacheTTLMin int  `toml:"cache_ttl_min"` // base cache TTL in minutes; default 60
}

// XSearchConfig controls the cross-balancer unified text search.
type XSearchConfig struct {
	Enable      bool `toml:"enable"`
	TimeoutSec  int  `toml:"timeout_sec"`   // per-source timeout; default 8
	MaxResults  int  `toml:"max_results"`   // max merged results; default 30
	TMDBSearch  bool `toml:"tmdb_search"`   // include TMDB in search; default true
	CacheTTLSec int  `toml:"cache_ttl_sec"` // result cache TTL; default 300
}

// ---------------------------------------------------------------------------
//  OpenSubtitles
// ---------------------------------------------------------------------------

// OpenSubsConfig controls the OpenSubtitles.com subtitle search proxy.
type OpenSubsConfig struct {
	Enable      bool   `toml:"enable"`
	APIKey      string `toml:"api_key"`       // REST API key from opensubtitles.com
	UserAgent   string `toml:"user_agent"`    // App name for API header (default: "lampac v1")
	CacheTTLMin int    `toml:"cache_ttl_min"` // search result cache TTL in minutes (default: 60)
}

// UpdaterConfig controls the self-update module (admin panel "Обновления" tab).
//
// Releases are served by an alcopa-site deployment at ServerURL. The updater
// fetches metadata, verifies SHA256 checksums (and optional minisign
// signatures), atomically replaces the running binary, and re-execs the
// process. Docker and `go run` modes are detected automatically and disable
// self-replace (admin UI then shows "copy-this-command" fallbacks).
type UpdaterConfig struct {
	Enable            bool   `toml:"enable"`             // master switch (default: true)
	ServerURL         string `toml:"server_url"`         // alcopa-site base URL, e.g. "https://lampac.site"
	Channel           string `toml:"channel"`            // "stable" (default), "beta", or a custom channel name
	ServerToken       string `toml:"server_token"`       // password of a protected channel (sent as Bearer). Admin-UI override: database/updater/settings.json
	CheckInterval     string `toml:"check_interval"`     // Go duration string, "" disables periodic check (default: "6h")
	AutoInstall       bool   `toml:"auto_install"`       // apply downloaded releases automatically (default: false)
	MaintenanceWindow string `toml:"maintenance_window"` // "HH:MM-HH:MM" local time for auto-install (default: "04:00-05:00")
	AssetOverride     string `toml:"asset_override"`     // force a specific asset name (escape hatch)

	// MinisignPubKey is the public key text used to verify SHA256SUMS.minisig.
	// Either the raw base64 body, or the full minisign pubkey file (with
	// "untrusted comment:" header). When set, Apply() refuses to install
	// releases whose signature does not verify.
	MinisignPubKey   string `toml:"minisign_pubkey"`
	RequireSignature bool   `toml:"require_signature"`

	// PreventDuringStreams, when true, blocks auto-install if any proxy
	// streams are currently active (default: true). Manual apply still works.
	PreventDuringStreams bool `toml:"prevent_during_streams"`
}

// ---------------------------------------------------------------------------
//  Defaults
// ---------------------------------------------------------------------------

// applyDefaults returns a Config populated with all default values.
func applyDefaults() Config {
	return Config{
		Server: ServerConfig{
			Addr: ":18118",
		},
		Compat: CompatConfig{
			RepoRoot: ".",
		},
		Web: WebConfig{
			WeblogCollect: true, // accept forwarded client weblog → terminal (set false to disable)
			InitPlugins: InitPluginsConfig{
				DLNA:        true,
				Tracks:      true,
				Transcoding: true,
				TMDBProxy:   true,
				Online:      true,
				Catalog:     true,
				SISI:        true,
				TorrServer:  true,
				Backup:      true,
				Sync:        true,
				Bookmark:    true,
				Timecode:    true,
				// SyncPro: on by default for new installs. When enabled,
				// lampainit suppresses the legacy quartet from autoload
				// (see buildLampainitInitiale). Existing deployments keep
				// their original flags untouched on upgrade.
				SyncPro:        true,
				AdsFree:        true,
				YouTubeFeed:    true,
				Stats:          true,
				OpenSubs:       true,
				Failover:       true,
				DualSubs:       true,
				PlayerRedesign: true,
				HLSTracks:      true,
				VoiceSwitcher:  true,
				WebPlayer:      true,
				Theme:          true,
				Screensaver:    true,
				Remote:         true,
				ExternalPlayer: true,
				Migrate:        true,
				// KinopubResume is OFF by default: most lampac servers
				// run without a kino.pub premium account, so there's no
				// reason to ship the bridge to every Lampa client. Turn
				// on in config.toml or via admin when activating the
				// token via /lite/kinopubpro.
				KinopubResume: false,
			},
		},
		WebSocket: WebSocketConfig{
			Type: "nws",
		},
		Mirror: MirrorConfig{
			CacheTTLSec:    60,
			GraceTTLSec:    900,
			SyncPluginsMin: 5,
		},
		Sisi: SisiConfig{
			Spider:        true,
			Component:     "sisi",
			PushAll:       true,
			HistoryEnable: true,
		},
		Online: OnlineConfig{
			CheckOnlineSearch: true,
			// no_stream_proxy stays EMPTY by default. It was briefly defaulted to
			// ["filmix","filmixtv","kinopub"] (copying big Lampa) — but that's WRONG for /capi's
			// server-resolve model. filmix's CDN url carries a signed `?hash=…` bound to the IP
			// that RESOLVED the link. In Lampa the CLIENT resolves → hash bound to the client's
			// residential IP → it plays. In /capi the SERVER resolves → hash bound to the SERVER
			// IP → handing that raw url to the client makes it fetch from a DIFFERENT IP → 403
			// (tester: werkecdn.me 403, single clean url, no " or "). Proxying (the default when a
			// source is NOT in no_stream_proxy) makes the SERVER fetch with the same IP the hash
			// was minted for → it plays. Add no_stream_proxy entries only for sources whose tokens
			// are NOT IP-bound and that genuinely need direct-to-client delivery.
			PidoRezka:    PidoRezkaSource{Host: "https://hdrzk.org"},
			Rhsprem:      PidoRezkaSource{Host: "https://hdrzk.org"},
			AhueRezka:    AhueRezkaSource{Host: "https://rezka.hdbase.workers.dev", KpHost: "https://kp.hdbase.workers.dev", Premium: false, HLS: false},
			KinoPub:      KinoPubSource{Host: "https://api.srvkp.com"},
			Wink:         WinkSource{DiscoveryHost: "https://itv.svc.iptv.rt.ru/api/v2", Platform: "ANDROID", DeviceType: "ANDROIDTV", DeviceModel: "Nexus 9", UserAgent: "Wink/1.38.1 (Android 9; ANDROIDTV)", TV: true, VOD: true},
			Kinobase:     KinobaseSource{Host: "https://kinobase.org", PlayerJS: true, HDR: true},
			Redheadsound: RedheadsoundSource{Host: "https://redheadsound.studio"},
			Anilibria:    HostSource{Host: "https://anilibria.top"},
			AniLiberty:   HostSource{Host: "https://api.anilibria.app"},
			Animebesst:   HostSource{Host: "https://anime1.best"},
			Animedia:     HostSource{Host: "https://amd.online"},
			Animevost:    HostSource{Host: "https://animevost.org"},
			AnimeGo:      HostSource{Host: "https://animego.me"},
			Remux:        RemuxSource{Host: "https://megaoblako.com"},
			AnimeLib:     HostTokenSource{Host: "https://api.cdnlibs.org"},
			Kinoukr:      KinoukrSource{Host: "https://kinoukr.tv"},
			Filmix:       FilmixSource{Host: "http://filmixapp.cyou"},
			FilmixTV:     HostSource{Host: "https://api.filmix.tv"},
			Kinotochka:   KinotochkaSource{Host: "https://kinovibe.vip"},
			Collaps:      CollapsSource{APIHost: "https://api.luxembd.ws", ListHost: "https://api.bhcesh.me", Token: "eedefb541aeba871dcfc756e6b31c02e"},
			RutubeMovie:  HostSource{Host: "https://rutube.ru"},
			Anwap:        HostSource{Host: "https://tv.anwap.today"},
			VKMovie:      VKMovieSource{Host: "https://api.vkvideo.ru", TokenURL: "https://login.vk.com/?act=get_anonym_token"},
			HDVB:         APIHostTokenSource{APIHost: "https://apivb.com", Token: "5e2fe4c70bafd9a7414c4f170ee1b192", PlayerHost: "https://vid1733431681.entouaedon.com"},
			Plvideo:      HostSource{Host: "https://api.g1.plvideo.ru"},
			VDBmovies:    HostSource{Host: "https://cdnmovies-stream.online"},
			Kodik:        APIHostTokenSource{APIHost: "https://kodik-api.com"},
			Lumex:        LumexSource{APIHost: "https://portal.lumex.host", IframeHost: "lumex.space"},
			VideoCDN:     VideoCDNSource{IframeHost: "https://portal.lumex.host"},
			Alloha:       AllohaSource{APIHost: "https://apbugall.org"},
			VeoVeo:       VeoVeoSource{Host: "https://global.temptcdn.com", DataPath: filepath.Join("data", "veoveo.json"), Token: DefaultVeoVeoToken},
			Ashdi:        HostSource{Host: "https://base.ashdi.vip"},
			Eneyida:      HostSource{Host: "https://eneyida.tv"},
			Kinogo:       KinogoSource{Host: "https://kinogo.la"},
			UaKino:       UaKinoSource{Host: "https://uakino.cx"},
			Kinovod:      HostSource{Host: "https://kinovod.pro"},
			FanCDN:       FanCDNSource{Host: "https://lomont.site"},
			VideoDB:      VideoDBSource{Host: "https://kinogo.media", APIHost: "https://30bf3790.obrut.show", APIHost2: "https://d6dd387e.obrut.show", FallbackHost: "https://api.variyt.ws"},
			Videoseed:    HostTokenSource{Host: "https://api.videoseed.tv"},
			Zetflix:      ZetflixSource{Host: "https://go.zet-flix.online", StreamProxy: true},
			ZetflixDB:    ZetflixDBSource{Host: "https://54243ba5.obrut.show"},
			CDNmovies:    HostSource{Host: "https://coldcdn.xyz"},
			CDNvideohub:  HostSource{Host: "https://plapi.cdnvideohub.com"},
			Kubikvkube:   KubikvkubeSource{Host: "https://kubikvkube.com", PlayerHost: "https://tomion.org"},
			Vibix:        HostTokenSource{Host: "https://vibix.org"},
			Turbo:        HostSource{Host: "92d73433.obrut.show"},
			IframeVideo:  IframeVideoSource{APIHost: "https://iframe.video", CDNHost: "https://videoframe.space"},
			GetsTV:       HostTokenSource{Host: "https://getstv.com"},
			Mirage:       MirageSource{APIHost: "https://api.apbugall.org", LinkHost: "https://aport-as.allarknow.online"},
			Aladdin:      MirageSource{APIHost: "https://api.apbugall.org", LinkHost: "https://quadrillion-as.allarknow.online"},
			PidTor:       PidTorSource{Enable: true, RedAPI: "http://redapi.cfhttp.top", MinSeeders: 15, EmptyVoice: true, AutoRemoveDelayMin: 60, MaxActiveTorrents: 3},
			MoonAnime:    HostTokenSource{Host: "https://api.moonanime.art"},
			IptvOnline:   HostTokenSource{Host: "https://iptv.online"},
			Vokino:       VokinoSource{Host: "http://api.vokino.org/"},
			// Ukrainian balancers
			Bamboo:       HostSource{Host: "https://bambooua.com"},
			UAFilm:       HostSource{Host: "https://uafilm.me"},
			VidLink:      HostSource{Host: "https://vidlink.pro"},
			Videasy:      HostSource{Host: "https://player.videasy.net"},
			HydraFlix:    HostSource{Host: "https://vidfast.pro"},
			TwoEmbed:     HostSource{Host: "https://embed.su"},
			Unimay:       HostSource{Host: "https://api.unimay.media/v1"},
			StarLight:    HostSource{Host: "https://tp-back.starlight.digital"},
			KlonFUN:      HostSource{Host: "https://klon.fun"},
			Uaflix:       UaflixSource{Host: "https://uafix.net"},
			AnimeON:      HostSource{Host: "https://animeon.club"},
			Mikai:        HostSource{Host: "https://api.mikai.me/v1"},
			LeProduction: HostSource{Host: "https://www.le-production.tv"},
			Gencit:       GencitSource{Host: "https://ylitron.pro"},
			Femd:         FemdSource{Host: "https://api.femd.ws"},
			Kinobadi:     KinobadiSource{PleerHost: "https://vip.kinobadi.im", FemdHost: "https://api.femd.ws", Consumer: "kinotik.top", Referer: "https://mm.kinobadi.im/"},
			FlixCDN:      FlixCDNSource{PlayerHost: "https://player0.flixcdn.space", RefererHost: "hdplayer.click"},
			Zona:         ZonaSource{Host: "https://w1.zona.im"},
			SakhTV:       SakhTVSource{Host: "https://api.sakh.tv", AppID: "5"},
			WithSearch: []string{
				"kinotochka", "kinopub", "lumex", "filmix",
				"filmixtv", "fxapi", "redheadsound", "rezka", "rhsprem",
				"kodik", "remux", "kinoukr", "rc/filmix", "rc/fxapi",
				"rc/rhs", "vcdn", "videocdn", "collaps", "collaps-dash",
				"hdvb", "alloha", "veoveo", "scts", "rutubemovie",
				"vkmovie", "videoseed", "mirage", "aladdin", "pidtor",
				"bamboo", "unimay", "starlight", "klonfun", "uaflix",
				"animeon", "mikai", "leproduction", "gencit", "femd", "kinobadi", "cdnvideohub",
				"kubikvkube", "lift", "sakhtv", "tevas", "zetflixdb", "uakino",
			},
		},
		Cub: CubConfig{
			Enable: true,
			Scheme: "http",
			Domain: "cub.red",
			ViewRU: true,
		},
		ProxyLink: ProxyLinkConfig{
			CacheDir:   "cache",
			VerifyIP:   true,
			EncryptAES: true,
		},
		ServerProxy: ServerProxyConfig{
			ResponseContentLength: true,
			MaxLengthM3U:          5_000_000,
			Image: ServerProxyImageConfig{
				Cache:      true,
				CacheRSize: true,
				CacheTime:  60,
			},
		},
		Observability: ObservabilityConfig{
			MetricsPath: "/metrics",
			HealthPath:  "/healthz",
			ReadyPath:   "/readyz",
			AccessLog:   true,
		},
		Parser: ParserConfig{
			JacRedHost:      "https://jacred.stream",
			JacRedLocalPort: 9117,
			RuTracker: RuTrackerConfig{
				Host:             "https://rutracker.org",
				TimeoutSec:       25,
				MaxResults:       100,
				ResolveTop:       12,
				ResolveBudgetSec: 8,
				SearchTTLMin:     60,
				MinIntervalMs:    800,
			},
		},
		TorrServer: TorrServerConfig{
			Port:               9080,
			CacheSizeMB:        64,
			DiskCacheMB:        1024,
			PreloadMB:          5,
			DisableUpload:      true,
			CacheCleanupEnable: true,
			CacheCleanupDays:   7,
			CacheCleanupMaxGB:  50,
		},
		TelegramAuth: TelegramAuthConfig{
			MaxDevicesPerUser: 3,
			AutoApproveDays:   30,
		},
		Auth: AuthConfig{
			Mode: AuthModeTG,
			Password: PasswordAuthConfig{
				MinPasswordLen:    8,
				MaxUsernameLen:    32,
				MaxLoginAttempts:  5,
				LockoutMinutes:    10,
				SessionDays:       365,
				DefaultExpireDays: 365,
				MaxDevicesDefault: 3,
			},
			Anon: AnonAuthConfig{
				SessionDays: 30,
			},
		},
		TMDBProxy: TMDBProxyConf{
			Mode:               "self",
			Host:               "tmdb.alcopa.cc",
			APIHost:            "apitmdb.cub.red",
			IMGHost:            "imagetmdb.com",
			CacheMaxItems:      50000,
			CacheTTLMin:        120,
			UpstreamTimeoutSec: 8,
		},
		Transcoding: defaultTranscoding(),
		LLM: LLMConfig{
			Model:      "qwen2.5-coder-14b",
			Temp:       0.1,
			MaxRetries: 5,
		},
		Kit: KitConfig{
			CacheToSeconds: 60,
			Encrypt:        true,
		},
		BrowserPool: BrowserPoolConfig{
			MaxConcurrent:   8,
			StreamCacheMax:  2000,
			StreamCacheTTLH: 8,
			Engine:          "chromedp",
		},
		FileCacheInactive: FileCacheConfig{
			FreeDiskSpace: 1 << 30, // 1 GB
			HTML:          5,
			Torrent:       2880, // 48 hours
			HLS:           90,
		},
		Calendar: CalendarConfig{
			CheckIntervalMin: 120,
			NotifyTG:         true,
			UpcomingDays:     7,
			RecentDays:       3,
		},
		XSearch: XSearchConfig{
			TimeoutSec:  8,
			MaxResults:  30,
			TMDBSearch:  true,
			CacheTTLSec: 300,
		},
		Collections: CollectionsConfig{
			CacheTTLMin: 60,
		},
		OpenSubs: OpenSubsConfig{
			UserAgent:   "lampac v1",
			CacheTTLMin: 60,
		},
		DLNA: DLNAConfig{
			Enable:              true,
			Path:                "dlna",
			MediaPattern:        `^\.(aac|flac|mpga|mp2|mp3|m4a|oga|ogg|opus|spx|weba|wav|dif|dv|fli|mp4|mpeg|mpg|mpe|mpv|mkv|ts|m4s|m2ts|mts|ogv|webm|avi|qt|mov)$`,
			AutoUpdateTrackers:  true,
			AddTrackersToMagnet: true,
			IntervalUpdateTrack: 90,
			UploadSpeed:         1250000, // ~1.25 MB/s
			UPnP:                true,
			UPnPPort:            1338,
			FriendlyName:        "Lampac DLNA",
			Cover: DLNACoverConfig{
				Enable:       true,
				Preview:      true,
				Timeout:      20,
				SkipModTime:  60,
				Extension:    `(mp4|mkv|avi|mpg|mpe|mpv)`,
				CoverCommand: `-n -ss 3:00 -i "{file}" -vf "thumbnail=150,scale=400:-2" -frames:v 1 "{thumb}"`,
			},
		},
		Updater: UpdaterConfig{
			Enable:            true,
			ServerURL:         "", // set [updater] server_url to your own update server to enable self-update
			Channel:           "stable",
			CheckInterval:     "6h",
			AutoInstall:       false,
			MaintenanceWindow: "04:00-05:00",
			// Minisign public key (Ed25519). The matching private key lives
			// OFFLINE, outside the server/repo. RequireSignature=true refuses
			// any release without a valid SHA256SUMS.minisig — this blocks a
			// downgrade attack where a compromised origin strips the signature
			// and serves a binary with an attacker-chosen (matching) SHA256.
			// NOTE: every release from now on MUST be signed (release.sh signs
			// by default) or clients will refuse to update.
			MinisignPubKey:       "RWRWMtrq6EvOZlKVrrMMaUl6B/Hq6IWnLh7GdmCEcp+vGzJ0pHNEHP4I",
			RequireSignature:     true,
			PreventDuringStreams: true,
		},
		HealthCheck: HealthCheckConfig{
			Enabled:          true,
			IntervalSec:      60,
			TimeoutSec:       10,
			FailThreshold:    3,
			RecoverThreshold: 2,
		},
	}
}

// defaultTranscoding returns TranscodingConf with all FFmpeg-compatible defaults.
//
// Defaults are tuned to be safe on a typical 2-vCPU VPS:
//   - Enable: true — smart-mode picks native/direct/remux 99% of the time
//     (stream-copy, ~zero CPU). Real re-encode only kicks in for HEVC10 /
//     EAC3 / DTS / unsupported containers — exactly where it's needed.
//   - MaxConcurrent: 5 — caps simultaneous re-encode jobs.
//   - DiskBudgetMB: 5120 — 5 GB cap on cache/transcoding so an idle box
//     can't accidentally fill its disk; scheduler evicts the oldest jobs
//     once usage exceeds 80% of this.
//   - DefaultSubs: true — embedded subs are extracted to WebVTT siblings
//     of the playlist; clients that can render subs get them, the rest
//     ignore the field. Costs almost nothing.
func defaultTranscoding() TranscodingConf {
	return TranscodingConf{
		Enable:             true,
		FFmpeg:             "ffmpeg",
		TempRoot:           filepath.Join("cache", "transcoding"),
		MaxConcurrent:      5,
		MaxTranscodeHeight: 1080,
		WindowAheadHigh:    22,
		WindowAheadLow:     12,
		DiskBudgetMB:       5120,
		DefaultSubs:        true,
		MultiAudio:         true,
		HLS: TranscodingHLSOptions{
			SegDur:  6,
			WinSize: 10,
			FMP4:    true,
			Command: map[string][]string{
				"output":         {"-max_delay 5000000"},
				"segment_mpegts": {},
			},
		},
		Audio: TranscodingAudioOptions{
			BitrateKbps: 192,
			Stereo:      true,
			CodecCopy:   []string{"aac", "mp3", "opus", "vorbis"},
			CommandTransc: map[string][]string{
				"default": {"-c:a aac", "-ac {stereo}", "-b:a {bitrateKbps}"},
			},
		},
		Playlist: TranscodingPlaylistOptions{
			ReadRate:       1.6,
			Burst:          10485760,
			DeleteSegments: true,
		},
		Convert: TranscodingConvertOptions{
			Codec: []string{"mpeg4", "msmpeg4v3", "flv1", "av1"},
			Command: map[string][]string{
				"default": {"-c:v libx264", "-preset veryfast", "-pix_fmt yuv420p"},
				"h264_yuv420p10le": {
					"-vf", "scale=in_color_matrix=bt2020nc:out_color_matrix=bt709:in_range=pc:out_range=tv,format=yuv420p",
					"-c:v libx264", "-preset veryfast", "-pix_fmt yuv420p",
					"-x264-params", "colorprim=bt709:transfer=bt709:colormatrix=bt709",
					"-color_primaries bt709", "-color_trc bt709", "-colorspace bt709", "-color_range tv",
				},
			},
		},
		Subtitle: TranscodingSubtitleOptions{
			Codec: []string{"subrip", "webvtt", "ass", "ssa", "mov_text", "ttml", "sami"},
			Command: []string{
				"-map 0:{subIndex}", "-an -vn", "-c:s webvtt", "-flush_packets 1",
				"-max_interleave_delta 0", "-muxpreload 0", "-muxdelay 0", "-f webvtt", "subs_{subIndex}.vtt",
			},
		},
		Command: map[string][]string{
			"demuxer": {"-threads 0", "-fflags +genpts"},
			"input":   {"-avoid_negative_ts disabled"},
			"output": {
				"-map 0:v:0", "-map 0:a:{audio_index}",
				"-dn",
				"-map_metadata -1", "-map_chapters -1", "-max_muxing_queue_size 2048",
			},
		},
	}
}
