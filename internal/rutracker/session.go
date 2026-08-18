package rutracker

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// defaultUA is the browser identity we present. It MUST stay in sync with the
// UA inside httpclient.ChromeHeaders: a cf_clearance cookie is bound to the
// (IP, User-Agent) pair, so a mismatch silently invalidates the solved
// challenge. When FlareSolverr solves one for us we adopt ITS user agent
// instead (see fetch).
const defaultUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// cookieJar is a deliberately minimal single-origin cookie store. rutracker is
// one host and the reference parser also just sends a fixed
// "bb_ssl=1; bb_session=…" header, so net/http/cookiejar's domain/path
// machinery would only get in the way of persisting and replaying a session.
type cookieJar struct {
	mu sync.RWMutex
	m  map[string]string
	ua string
}

func newCookieJar() *cookieJar {
	return &cookieJar{m: map[string]string{"bb_ssl": "1"}, ua: defaultUA}
}

func (j *cookieJar) set(name, value string) {
	name, value = strings.TrimSpace(name), strings.TrimSpace(value)
	if name == "" || value == "" {
		return
	}
	j.mu.Lock()
	j.m[name] = value
	j.mu.Unlock()
}

func (j *cookieJar) get(name string) string {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.m[name]
}

func (j *cookieJar) userAgent() string {
	j.mu.RLock()
	defer j.mu.RUnlock()
	if j.ua == "" {
		return defaultUA
	}
	return j.ua
}

func (j *cookieJar) setUserAgent(ua string) {
	ua = strings.TrimSpace(ua)
	if ua == "" {
		return
	}
	j.mu.Lock()
	j.ua = ua
	j.mu.Unlock()
}

// header renders the Cookie header value with a stable ordering.
func (j *cookieJar) header() string {
	j.mu.RLock()
	defer j.mu.RUnlock()
	if len(j.m) == 0 {
		return ""
	}
	names := make([]string, 0, len(j.m))
	for k := range j.m {
		names = append(names, k)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, k := range names {
		parts = append(parts, k+"="+j.m[k])
	}
	return strings.Join(parts, "; ")
}

// absorb stores Set-Cookie values from a response. Deleted cookies
// (empty value) are dropped so an expired bb_session cannot linger.
func (j *cookieJar) absorb(resp *http.Response) {
	if resp == nil {
		return
	}
	for _, c := range resp.Cookies() {
		if c == nil || c.Name == "" {
			continue
		}
		if c.Value == "" || (!c.Expires.IsZero() && c.Expires.Before(time.Now())) {
			j.mu.Lock()
			delete(j.m, c.Name)
			j.mu.Unlock()
			continue
		}
		j.set(c.Name, c.Value)
	}
}

func (j *cookieJar) snapshot() (map[string]string, string) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	out := make(map[string]string, len(j.m))
	for k, v := range j.m {
		out[k] = v
	}
	return out, j.ua
}

func (j *cookieJar) restore(m map[string]string, ua string) {
	j.mu.Lock()
	for k, v := range m {
		if k != "" && v != "" {
			j.m[k] = v
		}
	}
	if strings.TrimSpace(ua) != "" {
		j.ua = ua
	}
	j.mu.Unlock()
}

// ---------------------------------------------------------------------------
//  persistence
// ---------------------------------------------------------------------------

type sessionFile struct {
	Cookies  map[string]string `json:"cookies"`
	UA       string            `json:"user_agent"`
	CredsKey string            `json:"creds_key"` // session belongs to these credentials
	SavedAt  time.Time         `json:"saved_at"`
}

func (c *Client) sessionPath() string {
	dir := c.Config().DataDir
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "session.json")
}

// loadSession restores cookies from disk so a restart does not cost a fresh
// login (and, more importantly, a fresh Cloudflare challenge).
func (c *Client) loadSession() {
	path := c.sessionPath()
	if path == "" {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var sf sessionFile
	if json.Unmarshal(b, &sf) != nil {
		return
	}
	// A session belongs to one credential set; reusing it after the account
	// changed would silently keep searching under the old login.
	if sf.CredsKey != "" && sf.CredsKey != c.credsKey {
		return
	}
	if len(sf.Cookies) == 0 {
		return
	}
	c.jar.restore(sf.Cookies, sf.UA)
	if sf.Cookies["bb_session"] != "" {
		c.authMu.Lock()
		c.loggedIn = true
		c.loginAt = sf.SavedAt
		c.authMu.Unlock()
	}
	log.Debug().Str("path", path).Msg("rutracker: session restored")
}

func (c *Client) saveSession() {
	path := c.sessionPath()
	if path == "" {
		return
	}
	cookies, ua := c.jar.snapshot()
	if len(cookies) == 0 {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	b, err := json.MarshalIndent(sessionFile{
		Cookies:  cookies,
		UA:       ua,
		CredsKey: c.credsKey,
		SavedAt:  time.Now(),
	}, "", "  ")
	if err != nil {
		return
	}
	// Credentials-bearing file: owner-only, written atomically.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
	}
}
