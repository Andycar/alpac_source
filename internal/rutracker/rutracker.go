// Package rutracker is a native RuTracker.org indexer.
//
// Why it exists: jacred (both the public bases we proxy and a self-hosted
// instance) never returns rutracker rows. That is structural, not a bug —
// rutracker requires a logged-in account for both search and magnets, so an
// open-sync base cannot carry it, and JacRed's own parser bails out before the
// first request when no cookie/login is configured
// (JacRed/Controllers/RutrackerController.cs:12). On top of that rutracker now
// sits behind a Cloudflare managed challenge, which jacred has no way around.
//
// So we do it ourselves: live search under our own session, with the results
// merged into the existing /api/v1.0/torrents, /api/v2.0/indexers/* and
// /lite/pidtor answers. The parsing contract is a port of the reference
// parser in this same tree (JacRed/Controllers/RutrackerController.cs) — the
// selectors below are deliberately identical to it.
//
// Cost model: a search is one listing request; magnets are NOT in the listing,
// so the top rows (by seeders) are resolved to a real btih synchronously and
// the rest carry a parselink that resolves on demand. Resolved hashes are
// cached forever on disk (a topic's hash never changes — a re-upload is a new
// topic), so the price is paid once per release, not per search.
package rutracker

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Config mirrors the [parser.rutracker] TOML section. The zero value is
// disabled; applyDefaults fills the knobs a user did not set.
type Config struct {
	Enable bool

	// Host is the forum origin, e.g. https://rutracker.org. Mirrors
	// (rutracker.net / rutracker.nl) serve the same content.
	Host string

	// Login/Password authenticate via /forum/login.php. Cookie is a ready
	// bb_session value and takes priority — it is the escape hatch when the
	// Cloudflare challenge cannot be solved server-side: the owner logs in
	// with a browser and pastes the cookie.
	Login    string
	Password string
	Cookie   string

	// UseFlareSolverr allows falling back to the configured FlareSolverr
	// instance when Cloudflare answers with a challenge.
	UseFlareSolverr bool

	// OnlyAuthorized restricts merging into the public (pre-auth) torrent
	// endpoints. Our session is a single account: anonymous traffic through
	// it is what gets accounts banned.
	OnlyAuthorized bool

	TimeoutSec int // per HTTP request, default 25

	// MaxResults caps rows taken from one listing page (rutracker returns 50).
	MaxResults int

	// ResolveTop is how many top rows (by seeders) get a real magnet before
	// the search returns. The rest are resolved lazily via /parse/rutracker.
	ResolveTop int

	// ResolveBudgetSec bounds the synchronous resolve stage so a slow forum
	// can never hold a client request hostage.
	ResolveBudgetSec int

	// SearchTTLMin is the listing cache TTL.
	SearchTTLMin int

	// MinIntervalMs is the floor between two outgoing requests (plus jitter).
	// This is the only real protection against "session blocked for flooding".
	MinIntervalMs int

	// DataDir holds the persisted session + hash cache.
	DataDir string
}

func (c *Config) applyDefaults() {
	if strings.TrimSpace(c.Host) == "" {
		c.Host = "https://rutracker.org"
	}
	c.Host = strings.TrimRight(strings.TrimSpace(c.Host), "/")
	if c.TimeoutSec <= 0 {
		c.TimeoutSec = 25
	}
	if c.MaxResults <= 0 {
		c.MaxResults = 100
	}
	if c.ResolveTop < 0 {
		c.ResolveTop = 0
	} else if c.ResolveTop == 0 {
		c.ResolveTop = 12
	}
	if c.ResolveBudgetSec <= 0 {
		c.ResolveBudgetSec = 8
	}
	if c.SearchTTLMin <= 0 {
		c.SearchTTLMin = 60
	}
	if c.MinIntervalMs <= 0 {
		c.MinIntervalMs = 800
	}
}

func (c Config) timeout() time.Duration { return time.Duration(c.TimeoutSec) * time.Second }

// credsKey identifies the credential set so a config edit can invalidate a
// live session without restarting lampac.
func (c Config) credsKey() string {
	return c.Host + "\x00" + c.Login + "\x00" + c.Password + "\x00" + c.Cookie
}

// Release is one parsed listing row. Hash is empty until resolved.
type Release struct {
	TopicID   int       `json:"topic_id"`
	ForumID   int       `json:"forum_id"`
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	SizeName  string    `json:"size_name"`
	SizeBytes int64     `json:"size"`
	Seeders   int       `json:"sid"`
	Leechers  int       `json:"pir"`
	CreatedAt time.Time `json:"create_time"`
	Types     []string  `json:"types"`

	// Magnet is the full magnet URI (with &dn and &tr — pidtor builds its
	// stream URL from the trackers, internal/httpapi/pidtor.go:816). Empty
	// means "not resolved yet": the row still ships, carrying a parselink.
	Magnet string `json:"magnet,omitempty"`
}

// Resolved reports whether the row already carries a real magnet.
func (r Release) Resolved() bool { return strings.HasPrefix(r.Magnet, "magnet:") }

// Status is the admin-panel snapshot.
type Status struct {
	Enabled       bool   `json:"enabled"`
	Host          string `json:"host"`
	HasCreds      bool   `json:"has_creds"`
	LoggedIn      bool   `json:"logged_in"`
	LoginAt       string `json:"login_at,omitempty"`
	Challenge     bool   `json:"challenge"`           // last fetch hit a Cloudflare challenge
	FlareSolverr  bool   `json:"flaresolverr_used"`   // last successful fetch went through FS
	BannedUntil   string `json:"banned_until,omitempty"`
	LastError     string `json:"last_error,omitempty"`
	Searches      int64  `json:"searches"`
	Requests      int64  `json:"requests"`
	RowsLast      int    `json:"rows_last"`
	HashCacheSize int    `json:"hash_cache_size"`
	SearchCache   int    `json:"search_cache"`
	AvgMs         int64  `json:"avg_ms"`
}

// Client is the indexer. One per process; safe for concurrent use.
type Client struct {
	mu  sync.RWMutex
	cfg Config

	jar      *cookieJar
	client   *http.Client // follows redirects
	noRedir  *http.Client // login / dl.php — a 302 to login.php means "session died"
	credsKey string

	limiter  *limiter
	topicSem chan struct{} // caps parallel viewtopic/dl.php fetches

	// group collapses identical concurrent searches onto one network call.
	group  singleflight.Group
	search *ttlCache
	hashes *hashStore

	authMu   sync.Mutex
	loggedIn bool
	loginAt  time.Time
	authErr  error
	authErrAt time.Time

	st stats
}

// New builds a client. It never touches the network — the first search logs in.
func New(cfg Config) *Client {
	cfg.applyDefaults()
	c := &Client{
		cfg:      cfg,
		limiter:  newLimiter(time.Duration(cfg.MinIntervalMs) * time.Millisecond),
		topicSem: make(chan struct{}, 3),
		search:   newTTLCache(),
		hashes:   newHashStore(cfg.DataDir),
		credsKey: cfg.credsKey(),
	}
	c.buildHTTP()
	c.loadSession()
	return c
}

// Config returns the current config snapshot.
func (c *Client) Config() Config {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cfg
}

// Enabled reports whether the source is on and has something to authenticate
// with. Without credentials rutracker answers nothing at all, so an enabled
// but credential-less config is treated as off (and surfaced in Status).
func (c *Client) Enabled() bool {
	cfg := c.Config()
	return cfg.Enable && (strings.TrimSpace(cfg.Cookie) != "" ||
		(strings.TrimSpace(cfg.Login) != "" && strings.TrimSpace(cfg.Password) != ""))
}

// SetConfig applies a live config edit. Credential/host changes drop the
// current session and the listing cache; tuning knobs apply in place.
func (c *Client) SetConfig(cfg Config) {
	cfg.applyDefaults()
	if cfg.DataDir == "" {
		cfg.DataDir = c.Config().DataDir
	}

	c.mu.Lock()
	same := c.credsKey == cfg.credsKey()
	oldInterval := c.cfg.MinIntervalMs
	c.cfg = cfg
	c.credsKey = cfg.credsKey()
	c.mu.Unlock()

	if oldInterval != cfg.MinIntervalMs {
		c.limiter.setInterval(time.Duration(cfg.MinIntervalMs) * time.Millisecond)
	}
	if same {
		return
	}

	// Credentials changed: forget the session, keep the (immutable) hash cache.
	c.mu.Lock()
	c.buildHTTP()
	c.mu.Unlock()

	c.authMu.Lock()
	c.loggedIn = false
	c.authErr = nil
	c.authErrAt = time.Time{}
	c.authMu.Unlock()

	c.search.purge()
	c.st.setChallenge(false)
}

// Snapshot reports the current state for the admin panel.
func (c *Client) Snapshot() Status {
	cfg := c.Config()
	c.authMu.Lock()
	loggedIn, loginAt, authErr := c.loggedIn, c.loginAt, c.authErr
	c.authMu.Unlock()

	st := c.st.snapshot()
	out := Status{
		Enabled:       cfg.Enable,
		Host:          cfg.Host,
		HasCreds:      c.Enabled(),
		LoggedIn:      loggedIn,
		Challenge:     st.challenge,
		FlareSolverr:  st.viaFS,
		Searches:      st.searches,
		Requests:      st.requests,
		RowsLast:      st.rowsLast,
		HashCacheSize: c.hashes.len(),
		SearchCache:   c.search.len(),
		AvgMs:         st.avgMs(),
	}
	if !loginAt.IsZero() {
		out.LoginAt = loginAt.UTC().Format(time.RFC3339)
	}
	if until := c.limiter.bannedUntil(); !until.IsZero() && time.Now().Before(until) {
		out.BannedUntil = until.UTC().Format(time.RFC3339)
	}
	if authErr != nil {
		out.LastError = authErr.Error()
	} else if st.lastErr != "" {
		out.LastError = st.lastErr
	}
	return out
}

// Flush persists the hash cache and session. Callers that mutate state in
// tests must call it before asserting on disk contents.
func (c *Client) Flush() {
	c.hashes.flush()
	c.saveSession()
}
