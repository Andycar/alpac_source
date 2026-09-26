package httpapi

import (
	stdjson "encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"lampac-go/internal/geoip"
)

// ---------------------------------------------------------------------------
//  WAF configuration — loaded from init.conf JSON section "WAF"
// ---------------------------------------------------------------------------

type wafConfig struct {
	Enable                  bool                       `json:"enable"`
	BypassLocalIP           bool                       `json:"bypassLocalIP"`
	BruteForceProtection    bool                       `json:"bruteForceProtection"`
	BruteForceLimit         int                        `json:"bruteForceLimit"`
	WhiteIPs                []string                   `json:"whiteIps"`
	LimitReq                int                        `json:"limit_req"`
	LimitMap                map[string]wafLimitMapRule `json:"limit_map"`
	IPsDeny                 []string                   `json:"ipsDeny"`
	IPsAllow                []string                   `json:"ipsAllow"`
	CountryDeny             []string                   `json:"countryDeny"`
	CountryAllow            []string                   `json:"countryAllow"`
	HeadersDeny             map[string]string          `json:"headersDeny"`
	CustomWhitelistPaths    []string                   `json:"customWhitelistPaths"`
	CustomWhitelistPrefixes []string                   `json:"customWhitelistPrefixes"`
}

type wafLimitMapRule struct {
	Limit    int      `json:"limit"`
	Second   int      `json:"second"`
	PathID   bool     `json:"pathId"`
	QueryIDs []string `json:"queryIds"`
}

// ---------------------------------------------------------------------------
//  Compiled runtime state
// ---------------------------------------------------------------------------

type wafState struct {
	cfg wafConfig
	geo *geoip.DB

	ipWhiteSet map[string]bool

	cidrDeny  []*net.IPNet
	cidrAllow []*net.IPNet
	ipDeny    map[string]bool
	ipAllow   map[string]bool

	countryDenySet  map[string]bool
	countryAllowSet map[string]bool

	headerDenyRe map[string]*regexp.Regexp

	limitRules   []wafCompiledLimitRule
	defaultLimit int // fallback limit_req (per 60s)

	customWhitelistPaths    map[string]bool
	customWhitelistPrefixes []string

	counters   sync.Map // key(string) → *rateBucket
	bruteForce sync.Map // ip(string) → *bruteForceEntry

	manualBans sync.Map // ip(string) → *manualBanEntry

	stats *wafStats

	stopCh chan struct{}
}

type wafCompiledLimitRule struct {
	pattern  *regexp.Regexp
	raw      string
	limit    int
	windowNs int64
	pathID   bool
	queryIDs []string
}

// ---------------------------------------------------------------------------
//  Manual bans — persisted to database/waf_bans.json
// ---------------------------------------------------------------------------

type manualBanEntry struct {
	IP       string `json:"ip"`
	Until    int64  `json:"until"` // unix seconds, 0 = permanent
	Reason   string `json:"reason"`
	BannedAt int64  `json:"bannedAt"` // unix seconds
}

const wafBansFile = "database/waf_bans.json"

// ---------------------------------------------------------------------------
//  Stats — rolling 24h reason counters + top paths/IPs in last 1h
// ---------------------------------------------------------------------------

type wafStats struct {
	mu sync.Mutex
	// reasonBuckets[bucketIdx][reason] = count. Ring of 24 hourly buckets.
	reasonBuckets [24]map[string]int
	bucketHour    [24]int64 // unix-hour stamp owning each bucket slot

	// 1h-window top lists, pruned on Record.
	pathCounts map[string]*wafTopEntry
	ipCounts   map[string]*wafTopEntry
}

type wafTopEntry struct {
	count    int
	lastSeen int64 // unix seconds
}

func newWafStats() *wafStats {
	s := &wafStats{
		pathCounts: make(map[string]*wafTopEntry),
		ipCounts:   make(map[string]*wafTopEntry),
	}
	for i := range s.reasonBuckets {
		s.reasonBuckets[i] = make(map[string]int)
	}
	return s
}

func (s *wafStats) Record(reason, ip, path string) {
	now := time.Now().Unix()
	hour := now / 3600
	idx := int(hour % 24)

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.bucketHour[idx] != hour {
		s.reasonBuckets[idx] = make(map[string]int)
		s.bucketHour[idx] = hour
	}
	s.reasonBuckets[idx][reason]++

	cutoff := now - 3600

	if path != "" {
		if e, ok := s.pathCounts[path]; ok {
			e.count++
			e.lastSeen = now
		} else {
			s.pathCounts[path] = &wafTopEntry{count: 1, lastSeen: now}
		}
		for k, v := range s.pathCounts {
			if v.lastSeen < cutoff {
				delete(s.pathCounts, k)
			}
		}
	}

	if ip != "" {
		if e, ok := s.ipCounts[ip]; ok {
			e.count++
			e.lastSeen = now
		} else {
			s.ipCounts[ip] = &wafTopEntry{count: 1, lastSeen: now}
		}
		for k, v := range s.ipCounts {
			if v.lastSeen < cutoff {
				delete(s.ipCounts, k)
			}
		}
	}
}

func (s *wafStats) snapshot() map[string]any {
	now := time.Now().Unix()
	hourNow := now / 3600
	cutoffHour := hourNow - 23

	s.mu.Lock()
	defer s.mu.Unlock()

	reasons := map[string]int{}
	var total24h int
	for i := range s.reasonBuckets {
		if s.bucketHour[i] < cutoffHour {
			continue
		}
		for k, v := range s.reasonBuckets[i] {
			reasons[k] += v
			total24h += v
		}
	}

	type kv struct {
		Key   string `json:"key"`
		Count int    `json:"count"`
	}
	topN := func(m map[string]*wafTopEntry, n int) []kv {
		list := make([]kv, 0, len(m))
		for k, v := range m {
			list = append(list, kv{Key: k, Count: v.count})
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].Count != list[j].Count {
				return list[i].Count > list[j].Count
			}
			return list[i].Key < list[j].Key
		})
		if len(list) > n {
			list = list[:n]
		}
		return list
	}

	return map[string]any{
		"window":     "24h",
		"total_24h":  total24h,
		"by_reason":  reasons,
		"top_paths":  topN(s.pathCounts, 10),
		"top_ips":    topN(s.ipCounts, 10),
		"window_top": "1h",
	}
}

// ---------------------------------------------------------------------------
//  Rate bucket — sliding window counter
// ---------------------------------------------------------------------------

type rateBucket struct {
	mu    sync.Mutex
	times []int64 // UnixNano timestamps
}

// allow checks whether a new request is within the limit.
// It prunes expired entries, then checks count.
func (b *rateBucket) allow(limit int, windowNs int64) bool {
	now := time.Now().UnixNano()
	cutoff := now - windowNs

	b.mu.Lock()
	defer b.mu.Unlock()

	// Prune old entries.
	n := 0
	for _, t := range b.times {
		if t > cutoff {
			b.times[n] = t
			n++
		}
	}
	b.times = b.times[:n]

	if len(b.times) >= limit {
		return false
	}

	b.times = append(b.times, now)
	return true
}

// ---------------------------------------------------------------------------
//  Brute force — unique devices per IP
// ---------------------------------------------------------------------------

type bruteForceEntry struct {
	mu      sync.Mutex
	devices map[string]struct{}
	resetAt int64 // UnixNano when to reset
}

func (e *bruteForceEntry) add(deviceID string, limit int) bool {
	now := time.Now().UnixNano()

	e.mu.Lock()
	defer e.mu.Unlock()

	if now > e.resetAt {
		e.devices = make(map[string]struct{})
		e.resetAt = now + int64(time.Minute)
	}

	e.devices[deviceID] = struct{}{}
	return len(e.devices) <= limit
}

// ---------------------------------------------------------------------------
//  loadWafConfig — reads init.conf WAF section
// ---------------------------------------------------------------------------

// loadWafConfig reads the "WAF" object from init.conf on disk. The on-disk
// file is read directly because readFileAny("init.conf") may return a TOML
// projection that does not include the WAF section (WAF is not TOML-managed).
func loadWafConfig() wafConfig {
	data, err := os.ReadFile(relToRuntime("init.conf"))
	if err != nil {
		return wafConfig{}
	}
	var root map[string]stdjson.RawMessage
	if err := stdjson.Unmarshal(data, &root); err != nil {
		return wafConfig{}
	}
	raw, ok := root["WAF"]
	if !ok {
		return wafConfig{}
	}
	var cfg wafConfig
	if err := stdjson.Unmarshal(raw, &cfg); err != nil {
		return wafConfig{}
	}
	return cfg
}

// saveWafConfig merges the supplied config into init.conf on disk and persists.
func saveWafConfig(cfg wafConfig) error {
	path := relToRuntime("init.conf")
	root := map[string]any{}
	if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) > 0 {
		_ = stdjson.Unmarshal(data, &root)
	}
	root["WAF"] = cfg
	return writePrettyJSON("init.conf", root)
}

// ---------------------------------------------------------------------------
//  newWafState — parse & compile config into runtime state
// ---------------------------------------------------------------------------

func newWafState(cfg wafConfig, geo *geoip.DB) *wafState {
	s := &wafState{
		cfg:                  cfg,
		geo:                  geo,
		ipWhiteSet:           make(map[string]bool, len(cfg.WhiteIPs)),
		ipDeny:               make(map[string]bool),
		ipAllow:              make(map[string]bool),
		countryDenySet:       make(map[string]bool, len(cfg.CountryDeny)),
		countryAllowSet:      make(map[string]bool, len(cfg.CountryAllow)),
		headerDenyRe:         make(map[string]*regexp.Regexp, len(cfg.HeadersDeny)),
		defaultLimit:         cfg.LimitReq,
		customWhitelistPaths: make(map[string]bool, len(cfg.CustomWhitelistPaths)),
		stats:                newWafStats(),
		stopCh:               make(chan struct{}),
	}

	for _, p := range cfg.CustomWhitelistPaths {
		if p = strings.TrimSpace(p); p != "" {
			s.customWhitelistPaths[p] = true
		}
	}
	for _, p := range cfg.CustomWhitelistPrefixes {
		if p = strings.TrimSpace(p); p != "" {
			s.customWhitelistPrefixes = append(s.customWhitelistPrefixes, p)
		}
	}

	// White IPs.
	for _, ip := range cfg.WhiteIPs {
		s.ipWhiteSet[strings.TrimSpace(ip)] = true
	}

	// IP deny/allow — split into exact + CIDR.
	for _, entry := range cfg.IPsDeny {
		entry = strings.TrimSpace(entry)
		if strings.Contains(entry, "/") {
			if _, cidr, err := net.ParseCIDR(entry); err == nil {
				s.cidrDeny = append(s.cidrDeny, cidr)
			}
		} else {
			s.ipDeny[entry] = true
		}
	}
	for _, entry := range cfg.IPsAllow {
		entry = strings.TrimSpace(entry)
		if strings.Contains(entry, "/") {
			if _, cidr, err := net.ParseCIDR(entry); err == nil {
				s.cidrAllow = append(s.cidrAllow, cidr)
			}
		} else {
			s.ipAllow[entry] = true
		}
	}

	// Country sets.
	for _, c := range cfg.CountryDeny {
		s.countryDenySet[strings.ToUpper(strings.TrimSpace(c))] = true
	}
	for _, c := range cfg.CountryAllow {
		s.countryAllowSet[strings.ToUpper(strings.TrimSpace(c))] = true
	}

	// Header deny regexps.
	for header, pattern := range cfg.HeadersDeny {
		if re, err := regexp.Compile("(?i)" + pattern); err == nil {
			s.headerDenyRe[strings.ToLower(strings.TrimSpace(header))] = re
		}
	}

	// Rate limit rules — compile path regexps.
	//
	// The order matters and must not depend on map iteration: checkRateLimit
	// applies the FIRST rule whose pattern matches, and a catch-all like ".*"
	// matches every path. Ranging a Go map gives a random order, so a catch-all
	// landed ahead of the specific rules on some starts and behind them on
	// others — the same server silently enforced 10 req/s on /proxy/ after one
	// restart and the intended 50 after the next. Sorted below: specific rules
	// first (alphabetically, for a stable result), catch-alls last.
	for pattern, rule := range cfg.LimitMap {
		re, err := regexp.Compile(pattern)
		if err != nil {
			continue
		}
		windowSec := rule.Second
		if windowSec <= 0 {
			windowSec = 60
		}
		limit := rule.Limit
		if limit <= 0 {
			limit = 10
		}
		s.limitRules = append(s.limitRules, wafCompiledLimitRule{
			pattern:  re,
			raw:      pattern,
			limit:    limit,
			windowNs: int64(time.Duration(windowSec) * time.Second),
			pathID:   rule.PathID,
			queryIDs: rule.QueryIDs,
		})
	}
	sortWAFLimitRules(s.limitRules)

	if bans, err := loadManualBans(); err == nil {
		now := time.Now().Unix()
		for _, b := range bans {
			if b.Until > 0 && b.Until <= now {
				continue
			}
			entry := b
			s.manualBans.Store(b.IP, &entry)
		}
	}

	return s
}

// ---------------------------------------------------------------------------
//  Hot-reload — atomic.Pointer[wafState] swap
// ---------------------------------------------------------------------------

// wafStateAtomic holds the live wafState. Middleware reads it on every
// request so a swap via ReloadWAF picks up new config without a restart.
var wafStateAtomic atomic.Pointer[wafState]

// wafGeoRef holds the GeoIP DB pointer captured at middleware install time
// so ReloadWAF can rebuild the state without threading the DB through every
// admin call site.
var wafGeoRef atomic.Pointer[geoip.DB]

// ReloadWAF re-reads init.conf, rebuilds the WAF state, and atomically
// publishes it. The previous state's cleanup goroutine is stopped via stopCh.
// Returns the new state for inspection.
func ReloadWAF() *wafState {
	cfg := loadWafConfig()
	state := newWafState(cfg, wafGeoRef.Load())

	if cfg.Enable {
		go state.cleanupLoop()
	}

	if prev := wafStateAtomic.Swap(state); prev != nil {
		select {
		case <-prev.stopCh:
		default:
			close(prev.stopCh)
		}
	}
	return state
}

// currentWafState returns the live WAF state or nil if uninitialised.
func currentWafState() *wafState {
	return wafStateAtomic.Load()
}

// ---------------------------------------------------------------------------
//  wafMiddleware — chi middleware constructor
// ---------------------------------------------------------------------------

func wafMiddleware(geo *geoip.DB) func(http.Handler) http.Handler {
	wafGeoRef.Store(geo)
	cfg := loadWafConfig()
	state := newWafState(cfg, geo)
	wafStateAtomic.Store(state)

	if cfg.Enable {
		go state.cleanupLoop()
		log.Printf("[waf] enabled: %d ip-deny, %d ip-allow, %d country-deny, %d country-allow, %d limit-rules",
			len(cfg.IPsDeny), len(cfg.IPsAllow), len(cfg.CountryDeny), len(cfg.CountryAllow), len(cfg.LimitMap))
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Read the live state on every request so ReloadWAF takes effect
			// without a restart. When WAF is disabled (or uninitialised) this
			// is a passthrough.
			s := wafStateAtomic.Load()
			if s == nil || !s.cfg.Enable {
				next.ServeHTTP(w, r)
				return
			}
			blocked, status, reason := s.check(r)
			if blocked {
				http.Error(w, reason, status)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// wafMiddlewareFromState is used in tests to inject state directly.
func wafMiddlewareFromState(state *wafState) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			blocked, status, reason := state.check(r)
			if blocked {
				http.Error(w, reason, status)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ---------------------------------------------------------------------------
//  check — run all WAF rules against a request
// ---------------------------------------------------------------------------

func (s *wafState) check(r *http.Request) (blocked bool, status int, reason string) {
	ip := clientIP(r)
	defer func() {
		if blocked && s.stats != nil {
			s.stats.Record(reason, ip, r.URL.Path)
		}
	}()

	// 0. Cluster trust: forwarded requests from the primary already passed
	//    its own WAF + auth gate. Per-IP rate-limiting on the node would
	//    incorrectly count all primary→node traffic against the primary's
	//    IP. Skip the WAF for trusted cluster requests.
	if isTrustedClusterRequest(r) {
		return false, 0, ""
	}

	// 1. IP whitelist — skip all checks.
	if s.ipWhiteSet[ip] {
		return false, 0, ""
	}

	// 1b. Manual bans — admin-issued, checked before bypass-local so banning
	//     localhost during dev still works as expected.
	if raw, ok := s.manualBans.Load(ip); ok {
		entry := raw.(*manualBanEntry)
		now := time.Now().Unix()
		if entry.Until > 0 && entry.Until <= now {
			s.manualBans.Delete(ip)
			_ = saveManualBans(s.collectManualBans())
		} else {
			r := "403 IP banned"
			if entry.Reason != "" {
				r = "403 IP banned: " + entry.Reason
			}
			return true, http.StatusForbidden, r
		}
	}

	// 2. Bypass local IP.
	if s.cfg.BypassLocalIP && isLocalIP(ip) {
		return false, 0, ""
	}

	// 3. Brute force protection — unique devices per IP.
	//    Skip for stream paths — external players (VLC, PotPlayer) send
	//    requests without uid/token, each UA counted as a separate device.
	//    Streams are already behind the auth gate.
	//
	//    ALWAYS skip on loopback / RFC1918 addresses regardless of
	//    cfg.BypassLocalIP. Two scenarios collapse all real-user IPs to
	//    a local address and would otherwise produce false-positive bans:
	//      - reverse proxy without proxy_set_header X-Forwarded-For — every
	//        upstream user looks like 127.0.0.1 to the Go process;
	//      - shared NAT (family/office) — 5 family members with 5 different
	//        UIDs hit the default limit in well under a minute.
	//    In both cases the per-IP brute-force model breaks; per-token/UID
	//    auth limits in tgauth still protect against actual abuse.
	if s.cfg.BruteForceProtection && !isStreamPath(r.URL.Path) && !isAuthPath(r.URL.Path) && !isLocalIP(ip) {
		deviceID := buildDeviceID(r)
		limit := s.cfg.BruteForceLimit
		if limit <= 0 {
			limit = 20
		}
		raw, _ := s.bruteForce.LoadOrStore(ip, &bruteForceEntry{
			devices: make(map[string]struct{}),
			resetAt: time.Now().Add(time.Minute).UnixNano(),
		})
		entry := raw.(*bruteForceEntry)
		if !entry.add(deviceID, limit) {
			return true, http.StatusTooManyRequests,
				"429 Too Many Devices — если сервер за reverse proxy (nginx/cloudflare), добавьте в nginx: proxy_set_header X-Forwarded-For $remote_addr; proxy_set_header X-Real-IP $remote_addr;"
		}
	}

	// 4. Country filtering.
	if len(s.countryAllowSet) > 0 || len(s.countryDenySet) > 0 {
		country := ""
		if s.geo != nil {
			country = s.geo.Country(ip)
		}
		countryUpper := strings.ToUpper(country)

		if len(s.countryAllowSet) > 0 {
			if country == "" || !s.countryAllowSet[countryUpper] {
				return true, http.StatusForbidden, "403 Forbidden — country not allowed"
			}
		}
		if len(s.countryDenySet) > 0 && country != "" {
			if s.countryDenySet[countryUpper] {
				return true, http.StatusForbidden, "403 Forbidden — country denied"
			}
		}
	}

	// 5. IP deny/allow (exact + CIDR).
	if s.checkIPDeny(ip) {
		return true, http.StatusForbidden, "403 Forbidden"
	}

	// 6. Header regex deny.
	for headerKey, re := range s.headerDenyRe {
		val := r.Header.Get(headerKey)
		if val != "" && re.MatchString(val) {
			return true, http.StatusForbidden, "403 Forbidden"
		}
	}

	// 7. Rate limiting.
	if blocked, reason := s.checkRateLimit(ip, r); blocked {
		return true, http.StatusTooManyRequests, reason
	}

	return false, 0, ""
}

// ---------------------------------------------------------------------------
//  IP deny/allow check (exact + CIDR)
// ---------------------------------------------------------------------------

func (s *wafState) checkIPDeny(ip string) bool {
	// If allow lists exist — only allowed IPs pass.
	if len(s.ipAllow) > 0 || len(s.cidrAllow) > 0 {
		if s.ipAllow[ip] {
			return false
		}
		parsed := net.ParseIP(ip)
		if parsed != nil {
			for _, cidr := range s.cidrAllow {
				if cidr.Contains(parsed) {
					return false
				}
			}
		}
		return true // not in allow list
	}

	// Deny list check.
	if s.ipDeny[ip] {
		return true
	}
	parsed := net.ParseIP(ip)
	if parsed != nil {
		for _, cidr := range s.cidrDeny {
			if cidr.Contains(parsed) {
				return true
			}
		}
	}
	return false
}

// ---------------------------------------------------------------------------
//  Rate limiting check
// ---------------------------------------------------------------------------

func (s *wafState) checkRateLimit(ip string, r *http.Request) (bool, string) {
	path := r.URL.Path

	// Skip rate limiting entirely for static resources (JS plugins, CSS).
	// Lampa loads ~15 JS plugins simultaneously on startup — they must not
	// be blocked by any rate limit rule (neither limit_map nor default).
	if s.isStaticResource(path) || isAuthPath(path) {
		return false, ""
	}

	// Find matching rule.
	for i := range s.limitRules {
		rule := &s.limitRules[i]
		if !rule.pattern.MatchString(path) {
			continue
		}

		key := buildRateLimitKey(ip, rule.raw, rule.pathID, rule.queryIDs, r)
		raw, _ := s.counters.LoadOrStore(key, &rateBucket{})
		bucket := raw.(*rateBucket)

		if !bucket.allow(rule.limit, rule.windowNs) {
			return true, "429 Too Many Requests"
		}
		return false, ""
	}

	// Default limit (if configured).
	if s.defaultLimit > 0 {
		key := "default:" + ip
		raw, _ := s.counters.LoadOrStore(key, &rateBucket{})
		bucket := raw.(*rateBucket)
		if !bucket.allow(s.defaultLimit, int64(60*time.Second)) {
			return true, "429 Too Many Requests"
		}
	}

	return false, ""
}

func buildRateLimitKey(ip, pattern string, pathID bool, queryIDs []string, r *http.Request) string {
	var sb strings.Builder
	sb.WriteString(ip)
	sb.WriteByte(':')
	sb.WriteString(pattern)

	if pathID {
		sb.WriteByte(':')
		sb.WriteString(r.URL.Path)
	}

	for _, qid := range queryIDs {
		if v := r.URL.Query().Get(qid); v != "" {
			sb.WriteByte(':')
			sb.WriteString(qid)
			sb.WriteByte('=')
			sb.WriteString(v)
		}
	}

	return sb.String()
}

// isStreamPath returns true for paths used by external players (VLC, PotPlayer, etc.).
// These are excluded from brute force device counting because players send
// rapid requests without uid/token query params, and each User-Agent would be
// counted as a separate device, quickly exhausting the limit.
func isStreamPath(path string) bool {
	return strings.HasPrefix(path, "/ts/") ||
		strings.HasPrefix(path, "/proxy/")
}

// isStaticResource returns true for paths that serve static resources or
// match the live config's customWhitelist* lists. These are excluded from
// the default rate limit because Lampa loads many resources simultaneously
// (JS plugins, CSS, images) and they should not trigger 429.
func (s *wafState) isStaticResource(path string) bool {
	if s != nil {
		if s.customWhitelistPaths[path] {
			return true
		}
		for _, p := range s.customWhitelistPrefixes {
			if strings.HasPrefix(path, p) {
				return true
			}
		}
	}
	return staticResourceBuiltin(path)
}

func staticResourceBuiltin(path string) bool {
	return strings.HasSuffix(path, ".js") ||
		strings.HasSuffix(path, ".css") ||
		strings.HasSuffix(path, ".wgt") ||
		strings.HasSuffix(path, ".svg") ||
		strings.HasSuffix(path, ".png") ||
		strings.HasSuffix(path, ".jpg") ||
		strings.HasSuffix(path, ".jpeg") ||
		strings.HasSuffix(path, ".ico") ||
		strings.HasSuffix(path, ".gif") ||
		strings.HasSuffix(path, ".webp") ||
		strings.HasSuffix(path, ".woff") ||
		strings.HasSuffix(path, ".woff2") ||
		strings.HasSuffix(path, ".ttf") ||
		strings.HasSuffix(path, ".eot") ||
		strings.HasSuffix(path, ".map") ||
		strings.HasSuffix(path, ".json") && strings.HasPrefix(path, "/msx/") ||
		strings.HasPrefix(path, "/img/") ||
		// /tmdb/* — the same-origin TMDB proxy: /tmdb/img/t/p/... (posters) AND /tmdb/api/3/...
		// (metadata/search/details). A card grid or a Details page fans out MANY TMDB calls at once
		// for clients that use this proxy (catalog.ts fallbackHost), so the default 20/min limit
		// returned «429 Too Many Requests» for half of them. It's read-only, server-cached metadata
		// proxied with our own key — exempt the whole /tmdb/ proxy from the rate limit (the other WAF
		// checks — bans, country, brute-force device count — still apply).
		strings.HasPrefix(path, "/tmdb/") ||
		strings.HasPrefix(path, "/proxyimg/") ||
		strings.HasPrefix(path, "/proxyimg:") ||
		strings.HasPrefix(path, "/proxy/") ||
		// /lite/youtube/img?id=VIDEOID — thumbnail proxy. Path has no file
		// extension so the suffix checks above miss it; Lampa fans these out
		// (12+ at once on Subscriptions/Feed pages) and would trip the default
		// 20 req/min rate limit, returning 429 for half the tiles.
		path == "/lite/youtube/img" ||
		// /lite/youtube/mux* — the server-side YouTube HLS mux (playlist + a segment every ~4s
		// + init.mp4). Ordinary sources stream through /proxy/ (exempt above); the YT mux is the
		// ONE media stream served under /lite/, so it alone tripped the 20 req/min limit a minute
		// into playback → 429 → ffmpeg/hls.js died with «Server returned 4XX Client Error, but
		// not one of 40{0,1,3,4}» mid-video. Same opaque-hash-key risk profile as /proxy/.
		strings.HasPrefix(path, "/lite/youtube/mux") ||
		// /api/iptv/logo?channel_id=ID — channel-logo proxy. Same story: the Live
		// TV grid fans out ~100 logo requests at once → would 429 half the tiles.
		path == "/api/iptv/logo" ||
		// /api/iptv/play — каждый зэппинг (Ch+/Ch− подряд) и каждый реконнект
		// плеера; 429 здесь = «канал не открылся» и ускоренный выброс из плеера.
		// /api/iptv/preview — сетка Live TV дёргает превью на каждую видимую
		// карточку (до 3 ретраев), /epg/ — той же сеткой. Все — за auth-гейтом.
		path == "/api/iptv/play" ||
		path == "/api/iptv/preview" ||
		strings.HasPrefix(path, "/api/iptv/epg/") ||
		// /capi/quality[/batch] — бейджи качества карточек. Сетка фанаутит их на каждую плитку
		// (старые клиенты — по одному, новые — пачкой на экран); за signed-гейтом capi, read-only,
		// серверный кэш 24ч — лимитировать нечего, а 429 = «бейдж не пришёл» на половине плиток.
		path == "/capi/quality" ||
		path == "/capi/quality/batch"
}

// isAuthPath returns true for auth-related paths that must never be rate-limited.
// Rate limiting /tg/auth/check (iframe polling) or /tg/auth/status (token
// validation on page load) causes an infinite auth loop: the gate JS treats
// 429 as "unauthorized" → clears token → shows gate again → repeat.
func isAuthPath(path string) bool {
	if strings.HasPrefix(path, "/tg/auth/") || strings.HasPrefix(path, "/tg/device/") {
		return true
	}
	// Mirror/cluster control plane. These self-authenticate with the mirror key (see
	// isMirrorOriginRequest) and are already exempt from the auth GATE — but they were still
	// rate-limited, and a mirror validating its users' tokens against the origin blows past
	// 20 req/min on its own. Prod 2026-08-17: 4242 × 429 on /api/auth/validate in three hours,
	// which is the exact infinite-auth-loop this function exists to prevent — the client reads
	// 429 as «unauthorized», drops the token and starts over.
	return path == "/api/auth/validate" ||
		path == "/api/cluster/authgate" ||
		path == "/api/cluster/plugins.tar.gz" ||
		path == "/api/cluster/wwwroot.tar.gz"
}

// ---------------------------------------------------------------------------
//  Helpers
// ---------------------------------------------------------------------------

func buildDeviceID(r *http.Request) string {
	q := r.URL.Query()
	// Combine query params that identify a unique device (same as .NET AccsDbInvk.Args).
	parts := []string{
		q.Get("account_email"),
		q.Get("uid"),
		q.Get("token"),
		q.Get("box_mac"),
		q.Get("nws_id"),
	}
	id := strings.Join(parts, "|")
	if id == "||||" {
		// Fallback: use User-Agent as rough device identifier.
		return "ua:" + r.UserAgent()
	}
	return id
}

var localNets = func() []*net.IPNet {
	cidrs := []string{
		"127.0.0.0/8",
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"::1/128",
		"fc00::/7",
	}
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, _ := net.ParseCIDR(c)
		if n != nil {
			nets = append(nets, n)
		}
	}
	return nets
}()

func isLocalIP(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	for _, n := range localNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
//  Cleanup loop — purge expired rate limit buckets
// ---------------------------------------------------------------------------

func (s *wafState) cleanupLoop() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.purgeExpired()
		case <-s.stopCh:
			return
		}
	}
}

var purgeCounter atomic.Int64

func (s *wafState) purgeExpired() {
	now := time.Now().UnixNano()
	var maxWindow int64
	for i := range s.limitRules {
		if s.limitRules[i].windowNs > maxWindow {
			maxWindow = s.limitRules[i].windowNs
		}
	}
	if maxWindow == 0 {
		maxWindow = int64(60 * time.Second)
	}

	cutoff := now - maxWindow*2

	var deleted int
	s.counters.Range(func(key, value any) bool {
		bucket := value.(*rateBucket)
		bucket.mu.Lock()
		if len(bucket.times) == 0 || bucket.times[len(bucket.times)-1] < cutoff {
			bucket.mu.Unlock()
			s.counters.Delete(key)
			deleted++
		} else {
			bucket.mu.Unlock()
		}
		return true
	})

	s.bruteForce.Range(func(key, value any) bool {
		entry := value.(*bruteForceEntry)
		entry.mu.Lock()
		if now > entry.resetAt {
			entry.mu.Unlock()
			s.bruteForce.Delete(key)
			deleted++
		} else {
			entry.mu.Unlock()
		}
		return true
	})

	if deleted > 0 {
		purgeCounter.Add(int64(deleted))
	}
}

// ---------------------------------------------------------------------------
//  Manual bans — persistence + admin helpers
// ---------------------------------------------------------------------------

func loadManualBans() ([]manualBanEntry, error) {
	path := relToRuntime(wafBansFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}
	var list []manualBanEntry
	if err := stdjson.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	return list, nil
}

func saveManualBans(list []manualBanEntry) error {
	path := relToRuntime(wafBansFile)
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	if list == nil {
		list = []manualBanEntry{}
	}
	data, err := stdjson.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

func (s *wafState) collectManualBans() []manualBanEntry {
	var list []manualBanEntry
	s.manualBans.Range(func(_, v any) bool {
		list = append(list, *v.(*manualBanEntry))
		return true
	})
	sort.Slice(list, func(i, j int) bool {
		return list[i].BannedAt > list[j].BannedAt
	})
	return list
}

// CfgAny / CollectManualBansAny are exported `any`-returning getters so the
// admin WAF handler in internal/adminhttp can read config + manual bans through
// the WafState interface without importing httpapi's wafConfig/manualBanEntry.
func (s *wafState) CfgAny() any { return s.cfg }

func (s *wafState) CollectManualBansAny() any { return s.collectManualBans() }

// AddManualBan stores a new ban (or refreshes an existing one) and persists.
func (s *wafState) AddManualBan(ip, reason string, ttl time.Duration) error {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return errBadIP
	}
	now := time.Now().Unix()
	entry := &manualBanEntry{
		IP:       ip,
		Reason:   strings.TrimSpace(reason),
		BannedAt: now,
	}
	if ttl > 0 {
		entry.Until = now + int64(ttl.Seconds())
	}
	s.manualBans.Store(ip, entry)
	return saveManualBans(s.collectManualBans())
}

// RemoveManualBan deletes a ban and persists.
func (s *wafState) RemoveManualBan(ip string) error {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return errBadIP
	}
	s.manualBans.Delete(ip)
	return saveManualBans(s.collectManualBans())
}

var errBadIP = errBadIPType{}

type errBadIPType struct{}

func (errBadIPType) Error() string { return "ip required" }

// ---------------------------------------------------------------------------
//  Stats accessor
// ---------------------------------------------------------------------------

// Stats returns a snapshot of the rolling stats (reasons, top paths/IPs).
func (s *wafState) Stats() map[string]any {
	if s == nil || s.stats == nil {
		return map[string]any{}
	}
	return s.stats.snapshot()
}
