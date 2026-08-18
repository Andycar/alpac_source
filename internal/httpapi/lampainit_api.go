package httpapi

import (
	stdjson "encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/auth"
	"lampac-go/internal/config"
	"lampac-go/internal/modules"
)

// resolveJacHost determines the jachost value for client-side configuration.
// The client always talks to this server: the /api/v2.0/indexers proxy
// (jacred_api.go) forwards to Parser.JacRedHost. Routing through the server
// keeps the parser working when the upstream is blocked client-side and lets
// the operator swap upstreams without re-provisioning every client.
func resolveJacHost(host string) string {
	return strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
}

// parserDefaultsHash versions the server-managed parser defaults pushed by
// lampainit.js. The client re-applies the defaults whenever the hash changes
// (new host, changed key/type) and leaves user storage alone otherwise. The
// "p" prefix keeps the value non-numeric — Lampa.Storage.get() coerces pure
// digit strings to numbers, which would break the string comparison.
func parserDefaultsHash(jachost, apikey string) string {
	return "p" + lampaUtilsHash("parser:"+jachost+":"+apikey+":jackett")
}

// jackettAPIKey is the value pushed to the client's `jackett_key` setting.
// Defaults to "1" (JacRed/Jackett accept any non-empty key by default) but
// honours the operator's [parser] jacred_apikey — previously the client was
// hardcoded to "1", so a configured key (e.g. "pp") was silently ignored.
func jackettAPIKey(cfg config.Config) string {
	if k := strings.TrimSpace(cfg.Parser.JacRedKey); k != "" {
		return k
	}
	return "1"
}

type lampainitSettings struct {
	PirateStore      bool
	OnlineName       string
	OnlineVersionOn  bool
	BtnPriorityForce bool
	DomainIDPattern  string
}

func lampainitJSHandler(cfg config.Config, manifest []modules.RootModule, customPlugins *CustomPluginRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Use live config so plugin enable/disable from admin panel takes effect immediately.
		if serverReady() {
			cfg = liveConfig(config.Config{})
		}
		// Live manifest for the same reason — the admin manifest form edits
		// the file on disk, and the boot-time copy captured in this closure
		// would otherwise gate ts.js/online.js on stale data until restart.
		manifest := liveManifest(cfg, manifest)
		lite := parseBoolLike(r.URL.Query().Get("lite"))
		template := "lampainit.js"
		if lite {
			template = "liteinit.js"
		}

		src, err := loadPluginTemplate(template, cfg)
		if err != nil {
			writePlain(w, http.StatusServiceUnavailable, "service unavailable")
			return
		}

		host := hostFromRequest(r)
		settings := loadLampainitSettings()

		invc := ""
		if raw, err := loadPluginTemplate("lampainit-invc.js", cfg); err == nil {
			invc = raw
		}

		// TODO: calendar, collections, failover, skip_intro, xsearch temporarily disabled — needs rework before release.
		initPlugins := cfg.Web.InitPlugins
		initPlugins.Failover = false
		initiale := buildLampainitInitiale(lite, initPlugins, cfg.TMDBProxy.Mode, false, false, false, false, cfg.IPTV.Enable, false, manifest, customPlugins)
		// Cache-buster: append ?v=<commit> to every plugin URL so Lampa
		// treats it as a new plugin entry whenever the server is rebuilt.
		// Without this, Lampa.Plugins keeps the previous code in
		// Lampa.Storage (keyed by URL) — `lampainit` only ADDS new plugins,
		// never reinstalls existing ones, so a release that ships an
		// updated online.js / account.js silently doesn't reach clients.
		// The lampainit.js merge logic (toRemove + plugins_add) does
		// remove old URL → install new URL → reload → fresh code path,
		// which is exactly what we want here.
		// Cache-bust the injected .js URLs by build id (commit → buildDate →
		// version). All accessors return "" when the server isn't wired, so the
		// whole block no-ops without an explicit readiness guard.
		cache := strings.TrimSpace(liveCommit())
		if cache == "" {
			cache = strings.TrimSpace(liveBuildDate())
		}
		if cache == "" {
			cache = strings.TrimSpace(liveVersion())
		}
		if cache != "" {
			initiale = strings.ReplaceAll(initiale, `.js","status"`, `.js?v=`+cache+`","status"`)
		}
		pirateStore := ""
		if settings.PirateStore {
			if raw, err := loadPluginTemplate("pirate_store.js", cfg); err == nil {
				pirateStore = raw
			}
		}

		out := src
		out = strings.ReplaceAll(out, "{lampainit-invc}", invc)
		out = strings.ReplaceAll(out, "{initiale}", initiale)
		// Legacy {deny} inline auth fallback removed — buildAuthGateJS
		// (prepended to on.js/online.js) is the single auth gate now.
		out = strings.ReplaceAll(out, "{deny}", "")
		out = strings.ReplaceAll(out, "{country}", countryFromRequest(r))
		out = strings.ReplaceAll(out, "{localhost}", host)
		out = strings.ReplaceAll(out, "{pirate_store}", pirateStore)
		out = strings.ReplaceAll(out, "{ major: 0, minor: 0 }",
			"{major: "+strconv.Itoa(publicBrandMajor())+", minor: "+strconv.Itoa(publicBrandMinor())+"}")

		jachost := resolveJacHost(host)
		apikey := jackettAPIKey(cfg)
		out = strings.ReplaceAll(out, "{jachost}", jachost)
		out = strings.ReplaceAll(out, "{jackett_key}", apikey)
		out = strings.ReplaceAll(out, "{parser_defaults_hash}", parserDefaultsHash(jachost, apikey))

		onlineVersion := detectOnlineVersion(cfg)
		fullBtnHash := lampaUtilsHash(settings.OnlineName + ":" + onlineVersion)
		out = strings.ReplaceAll(out, "{full_btn_priority_hash}", fullBtnHash)
		out = strings.ReplaceAll(out, "{btn_priority_forced}", strings.ToLower(strconv.FormatBool(settings.BtnPriorityForce)))

		token := lampainitDomainToken(settings.DomainIDPattern, r.Host)
		out = strings.ReplaceAll(out, "{token}", token)

		if !settings.OnlineVersionOn && onlineVersion != "" {
			out = strings.ReplaceAll(out, "v"+onlineVersion, "")
		}
		out = applyPublicBrandingJS(out)

		// Inject __lwpForceEnable for Android WebView when web player is enabled
		// on Android. This tells the web player to activate even when AndroidJS
		// global is present (normally it disables itself on native platforms).
		if initPlugins.WebPlayer && initPlugins.WebPlayerAndroid {
			out = "window.__lwpForceEnable=true;\n" + out
		}

		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		// Bust any stale WebView cache (especially iOS) that may still hold a
		// previous lampainit.js with the legacy {deny} fallback inlined.
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(out))
	}
}

func buildLampainitInitiale(lite bool, init config.InitPluginsConfig, tmdbMode string, skipIntroEnabled bool, calendarEnabled bool, xsearchEnabled bool, collectionsEnabled bool, iptvEnabled bool, audiobotEnabled bool, manifest []modules.RootModule, customPlugins *CustomPluginRegistry) string {
	add := func(list *[]string, value string) {
		*list = append(*list, value)
	}

	items := make([]string, 0, 16)

	if lite {
		if init.Online && hasEnabledModule(manifest, "online.dll") {
			add(&items, `"{localhost}/lite.js"`)
		}
		if init.SISI && hasEnabledModule(manifest, "sisi.dll") {
			add(&items, `"{localhost}/sisi.js?lite=true"`)
		}
		if init.Sync {
			add(&items, `"{localhost}/sync.js?lite=true"`)
		}
		return strings.Join(items, ",")
	}

	if init.DLNA && hasEnabledModule(manifest, "dlna.dll") {
		add(&items, `{"url": "{localhost}/dlna.js","status": 1,"name": "DLNA","author": "lampac"}`)
	}
	if hasEnabledModule(manifest, "tracks.dll") {
		if init.Tracks {
			add(&items, `{"url": "{localhost}/tracks.js","status": 1,"name": "Tracks.js","author": "lampac"}`)
		}
		if init.Transcoding {
			add(&items, `{"url": "{localhost}/transcoding.js","status": 1,"name": "Transcoding video","author": "lampac"}`)
		}
	}
	if init.TMDBProxy && tmdbMode != "disabled" {
		add(&items, `{"url": "{localhost}/tmdbproxy.js","status": 1,"name": "TMDB Proxy","author": "lampac"}`)
	}
	if init.Online && hasEnabledModule(manifest, "online.dll") {
		add(&items, `{"url": "{localhost}/online.js","status": 1,"name": `+jsonStringLiteral(publicOnlineNameRU())+`,"author": "lampac"}`)
	}
	if init.Catalog && hasEnabledModule(manifest, "catalog.dll") {
		add(&items, `{"url": "{localhost}/catalog.js","status": 1,"name": "Альтернативные источники каталога","author": "lampac"}`)
	}
	if init.SISI && hasEnabledModule(manifest, "sisi.dll") {
		add(&items, `{"url": "{localhost}/sisi.js","status": 1,"name": "Клубничка","author": "lampac"}`)
		add(&items, `{"url": "{localhost}/startpage.js","status": 1,"name": "Стартовая страница","author": "lampac"}`)
	}
	// SyncPro is the unified replacement for sync.js + bookmark.js +
	// timecode.js + backup.js + backup_sync_key.js. When it's enabled we
	// inject it instead of the legacy quintet — otherwise both stacks
	// would react to the same Lampa.Favorite events and race to write the
	// 'favorite' localStorage key. The legacy files remain reachable at
	// /sync.js, /bookmark.js, etc. for users running a custom Lampa build
	// that depends on them directly; only the autoload manifest is gated.
	if init.SyncPro {
		add(&items, `{"url": "{localhost}/syncpro.js","status": 1,"name": "Синхронизация (Pro)","author": "lampac-go"}`)
	} else {
		if init.Sync {
			add(&items, `{"url": "{localhost}/sync.js","status": 1,"name": "Синхронизация","author": "lampac"}`)
		}
		if init.Timecode {
			add(&items, `{"url": "{localhost}/timecode.js","status": 1,"name": "Синхронизация тайм-кодов","author": "lampac"}`)
		}
		if init.Bookmark {
			add(&items, `{"url": "{localhost}/bookmark.js","status": 1,"name": "Синхронизация закладок","author": "lampac"}`)
		}
	}
	if init.TorrServer && hasEnabledModule(manifest, "torrserver.dll") {
		add(&items, `{"url": "{localhost}/ts.js","status": 1,"name": "TorrServer","author": "lampac"}`)
	}
	if init.ExternalPlayer {
		add(&items, `{"url": "{localhost}/external_player.js","status": 1,"name": "External Player","author": "lampac"}`)
	}
	// Backup pair is also subsumed by SyncPro (the latter has its own
	// save/restore actions in Settings → Sync). Only inject when SyncPro
	// is off so we don't ship two backup UIs at once.
	if init.Backup && !init.SyncPro {
		add(&items, `{"url": "{localhost}/backup.js","status": 1,"name": "Backup","author": "lampac"}`)
		add(&items, `{"url": "{localhost}/backup_sync_key.js","status": 1,"name": "Backup Sync Key","author": "lampac"}`)
	}
	if init.AdsFree {
		add(&items, `{"url": "{localhost}/ads.js","status": 1,"name": "Ads Free","author": "lampac"}`)
	}
	if init.YouTubeFeed {
		add(&items, `{"url": "{localhost}/youtube_feed.js","status": 1,"name": "YouTube Feed","author": "lampac"}`)
	}
	if init.Migrate {
		add(&items, `{"url": "{localhost}/migrate.js","status": 1,"name": "Migrate","author": "lampac"}`)
	}

	// Skip Intro — overlay button to skip intros/outros/recaps.
	if skipIntroEnabled {
		add(&items, `{"url": "{localhost}/skip_intro.js","status": 1,"name": "Skip Intro","author": "lampac"}`)
	}

	// Content Calendar — upcoming episodes + tracking.
	if calendarEnabled {
		add(&items, `{"url": "{localhost}/calendar.js","status": 1,"name": "Calendar","author": "lampac"}`)
	}

	// XSearch — cross-balancer unified text search.
	if xsearchEnabled {
		add(&items, `{"url": "{localhost}/xsearch.js","status": 1,"name": "XSearch","author": "lampac"}`)
	}

	// Collections — smart TMDB collections (person, genre, studio).
	if collectionsEnabled {
		add(&items, `{"url": "{localhost}/collections.js","status": 1,"name": "Collections","author": "lampac"}`)
	}

	// IPTV — server-side IPTV with EPG.
	if iptvEnabled {
		add(&items, `{"url": "{localhost}/iptv2.js","status": 1,"name": "IPTV","author": "lampac"}`)
	}

	_ = audiobotEnabled // audiobot music plugin is not part of this build

	// Account — Settings → Аккаунт surface (whoami / login / logout).
	// Always installed: registers itself as a Settings tab, queries
	// /api/auth/whoami at render time so it adapts to whichever auth
	// mode the server is running (tg / password / none). Belt-and-
	// suspenders with the buildOnPlugins entry — lampainit gets the
	// plugin into "Установленные в память" for users on our builds
	// (where lampainit.js runs), while buildOnPlugins covers vanilla
	// Lampa installs loading only /on.js.
	add(&items, `{"url": "{localhost}/account.js","status": 1,"name": "Аккаунт","author": "lampac-go"}`)

	if init.Theme {
		add(&items, `{"url": "{localhost}/theme.js","status": 1,"name": "Оформление","author": "lampac"}`)
		add(&items, `{"url": "{localhost}/netflix_ui.js","status": 1,"name": "Netflix UI","author": "lampac"}`)
	}
	if init.Screensaver {
		add(&items, `{"url": "{localhost}/screensaver.js","status": 1,"name": "Apple TV Screensaver","author": "kolian72"}`)
	}
	if init.Remote {
		add(&items, `{"url": "{localhost}/remote.js","status": 1,"name": "Remote","author": "lampac"}`)
	}

	// Player enhancement plugins.
	if init.Stats {
		add(&items, `{"url": "{localhost}/stats.js","status": 1,"name": "Stats Overlay","author": "lampac"}`)
	}
	if init.OpenSubs {
		add(&items, `{"url": "{localhost}/opensubs.js","status": 1,"name": "OpenSubtitles","author": "lampac"}`)
	}
	if init.Failover {
		add(&items, `{"url": "{localhost}/failover.js","status": 1,"name": "Smart Failover","author": "lampac"}`)
	}
	if init.DualSubs {
		add(&items, `{"url": "{localhost}/dualsubs.js","status": 1,"name": "Dual Subtitles","author": "lampac"}`)
	}
	if init.PlayerRedesign {
		add(&items, `{"url": "{localhost}/player_redesign.js","status": 1,"name": "Player Redesign","author": "lampac"}`)
	}
	if init.HLSTracks {
		add(&items, `{"url": "{localhost}/hls_tracks.js","status": 1,"name": "HLS Audio Tracks","author": "lampac"}`)
	}
	if init.VoiceSwitcher {
		add(&items, `{"url": "{localhost}/voice_switcher.js","status": 1,"name": "Voice Switcher","author": "lampac"}`)
	}
	if init.WebPlayer {
		add(&items, `{"url": "{localhost}/webplayer.js","status": 1,"name": "Web Player","author": "lampac"}`)
	}
	add(&items, `{"url": "{localhost}/cdn_direct.js","status": 1,"name": "CDN Direct","author": "lampac"}`)
	add(&items, `{"url": "{localhost}/anti-dmca.js","status": 1,"name": "Anti DMCA","author": "lampac"}`)
	// KinoPub resume-bridge: opt-in via [web.plugins].kinopub_resume.
	// When off the JS is still served at /kinopub_resume.js (so admins
	// can hand-install per client), it just isn't autoloaded by every
	// Lampa instance. The bridge only fires for play-rows carrying
	// timeline.callback, so it's harmless when enabled without
	// kinopub, but unloading reduces noise on plain installs.
	if init.KinopubResume {
		add(&items, `{"url": "{localhost}/kinopub_resume.js","status": 1,"name": "KinoPub Resume","author": "lampac"}`)
	}
	// WASM client runtime — loads every client/both-target plugin advertised
	// by /wasm/index.json. The loader is harmless when no such plugins are
	// installed (it just fetches an empty list), so we keep it always-on.
	add(&items, `{"url": "{localhost}/plugins/wasm_loader.js","status": 1,"name": "WASM Plugins","author": "lampac"}`)

	// Community plugin store — menu item + catalog UI.
	if serverReady() && liveConfig(config.Config{}).Web.CommunityPluginURL != "" {
		add(&items, `{"url": "{localhost}/community_store.js","status": 1,"name": "Community Store","author": "lampac"}`)
	}

	// Append autoloaded custom plugins from admin panel.
	if customPlugins != nil {
		for _, p := range customPlugins.AutoloadPlugins() {
			fsName := strings.ReplaceAll(p.Name, `"`, ``)
			displayName := p.DisplayName
			if displayName == "" {
				displayName = p.Name
			}
			displayName = strings.ReplaceAll(displayName, `"`, ``)
			author := p.Author
			if author == "" {
				author = "lampac"
			}
			author = strings.ReplaceAll(author, `"`, ``)
			add(&items, `{"url": "{localhost}/`+fsName+`.js","status": 1,"name": "`+displayName+`","author": "`+author+`"}`)
		}
	}

	out := strings.Join(items, ",")
	// Keep plugin metadata aligned with the public service brand.
	out = strings.ReplaceAll(out, `"author": "lampac"`, `"author": "`+publicBrandName()+`"`)
	return out
}

func hasEnabledModule(manifest []modules.RootModule, dll string) bool {
	if len(manifest) == 0 {
		return true
	}
	dll = strings.ToLower(strings.TrimSpace(dll))
	for _, m := range manifest {
		if strings.ToLower(strings.TrimSpace(m.Dll)) == dll {
			return m.Enable
		}
	}
	return false
}

func loadLampainitSettings() lampainitSettings {
	out := lampainitSettings{
		OnlineName:       publicBrandName(),
		OnlineVersionOn:  true,
		BtnPriorityForce: true,
	}
	data, ok := readFileAny("init.conf")
	if !ok {
		return out
	}

	var root map[string]any
	if err := stdjson.Unmarshal(data, &root); err != nil {
		return out
	}

	out.PirateStore = toBool(root["pirate_store"])

	if online, ok := root["online"].(map[string]any); ok {
		if name := strings.TrimSpace(toString(online["name"])); name != "" {
			out.OnlineName = name
		}
		if online["version"] != nil {
			out.OnlineVersionOn = toBool(online["version"])
		}
		if online["btn_priority_forced"] != nil {
			out.BtnPriorityForce = toBool(online["btn_priority_forced"])
		}
	}

	if accsdb, ok := root["accsdb"].(map[string]any); ok {
		out.DomainIDPattern = strings.TrimSpace(toString(accsdb["domainId_pattern"]))
	}
	return out
}

func detectOnlineVersion(cfg config.Config) string {
	source, err := loadPluginTemplate("online.js", cfg)
	if err != nil {
		return publicBrandVersion()
	}
	match := regexp.MustCompile(`version:\s*'([^']+)'`).FindStringSubmatch(source)
	if len(match) == 2 {
		return publicBrandVersion()
	}
	return publicBrandVersion()
}

func lampainitDomainToken(pattern, host string) string {
	pattern = strings.TrimSpace(pattern)
	host = strings.TrimSpace(host)
	if pattern == "" || host == "" {
		return ""
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return ""
	}
	m := re.FindStringSubmatch(host)
	if len(m) > 1 {
		return m[1]
	}
	return ""
}

func lampaUtilsHash(input string) string {
	var hash int32
	for _, ch := range input {
		hash = (hash << 5) - hash + int32(ch)
	}
	if hash < 0 {
		hash = -hash
	}
	return strconv.FormatInt(int64(hash), 10)
}

func privateInitJSHandler(cfg config.Config, manifest []modules.RootModule) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := auth.UserFromContext(r.Context())
		if !ok || user == nil || user.Ban || time.Now().UTC().After(user.Expires) {
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(""))
			return
		}

		src, err := loadPluginTemplate("privateinit.js", cfg)
		if err != nil {
			writePlain(w, http.StatusServiceUnavailable, "service unavailable")
			return
		}

		host := hostFromRequest(r)
		out := src
		out = strings.ReplaceAll(out, "{country}", countryFromRequest(r))
		out = strings.ReplaceAll(out, "{localhost}", host)
		out = applyPublicBrandingJS(out)

		out = strings.ReplaceAll(out, "{jachost}", resolveJacHost(host))

		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(out))
	}
}
