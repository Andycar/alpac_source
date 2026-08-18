package rutracker

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"lampac-go/internal/httpclient"

	"github.com/rs/zerolog/log"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/transform"
)

// balancerName is the key under which this source registers with the proxy and
// FlareSolverr registries, so an admin can point rutracker at a SOCKS5 exit the
// same way as any balancer (the site is RKN-blocked, so a Russian box needs it).
const balancerName = "rutracker"

const maxBodyBytes = 6 << 20

var (
	// errChallenge means Cloudflare answered with its managed challenge.
	errChallenge = errors.New("rutracker: cloudflare challenge")
	// errGuest means the page rendered, but we are not logged in.
	errGuest = errors.New("rutracker: not authorized (guest page)")
)

// buildHTTP (re)creates the transport pair and drops the cookie jar. Caller
// must hold c.mu (or be the constructor).
func (c *Client) buildHTTP() {
	timeout := c.cfg.timeout()
	c.jar = newCookieJar()
	c.client = httpclient.NewUTLSForBalancer(balancerName, timeout)
	c.noRedir = httpclient.NewUTLSForBalancerNoRedirect(balancerName, timeout)
}

func (c *Client) clients() (follow, noRedirect *http.Client, jar *cookieJar) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.client, c.noRedir, c.jar
}

type fetchOpts struct {
	method     string // default GET
	form       url.Values
	referer    string
	noRedirect bool
	binary     bool // .torrent — no cp1251 decoding
	noFS       bool // never route through FlareSolverr (used by the FS path itself)
}

type fetchResp struct {
	status    int
	header    http.Header
	body      string // decoded UTF-8 text (empty when binary)
	raw       []byte
	viaFS     bool
	challenge bool
	location  string
}

// fetch performs one rate-limited request against the forum, decoding
// windows-1251 and transparently escalating to FlareSolverr when Cloudflare
// gets in the way.
func (c *Client) fetch(ctx context.Context, target string, opts fetchOpts) (*fetchResp, error) {
	if err := c.limiter.wait(ctx); err != nil {
		return nil, err
	}

	started := time.Now()
	res, err := c.fetchDirect(ctx, target, opts)
	c.st.addRequest(time.Since(started), err)

	cfg := c.Config()
	challenged := err == nil && res.challenge
	if challenged {
		c.st.setChallenge(true)
	}

	// Escalate to FlareSolverr only for the challenge — a transport error is a
	// network/DPI problem that a headless Chrome on the same host won't fix.
	if challenged && !opts.noFS && cfg.UseFlareSolverr {
		fsRes, fsErr := c.fetchViaFlareSolverr(ctx, target, opts)
		if fsErr == nil {
			c.st.setChallenge(false)
			c.st.setViaFS(true)
			c.limiter.success()
			return fsRes, nil
		}
		log.Debug().Err(fsErr).Str("url", target).Msg("rutracker: flaresolverr fallback failed")
	}

	if err != nil {
		c.limiter.failure(false)
		return nil, err
	}
	switch {
	case res.challenge:
		c.limiter.failure(true)
		return res, errChallenge
	case res.status == http.StatusTooManyRequests || res.status == http.StatusServiceUnavailable:
		c.limiter.failure(true)
		return res, fmt.Errorf("rutracker: HTTP %d", res.status)
	case res.status >= 500:
		c.limiter.failure(false)
		return res, fmt.Errorf("rutracker: HTTP %d", res.status)
	}
	c.limiter.success()
	c.st.setChallenge(false)
	c.st.setViaFS(false)
	return res, nil
}

func (c *Client) fetchDirect(ctx context.Context, target string, opts fetchOpts) (*fetchResp, error) {
	follow, noRedirect, jar := c.clients()
	client := follow
	if opts.noRedirect {
		client = noRedirect
	}
	if client == nil {
		return nil, errors.New("rutracker: client not initialized")
	}

	method := strings.ToUpper(strings.TrimSpace(opts.method))
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if opts.form != nil {
		body = strings.NewReader(opts.form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}

	referer := opts.referer
	if referer == "" {
		referer = c.Config().Host + "/forum/index.php"
	}
	req.Header = httpclient.ChromeHeaders(referer)
	// ChromeHeaders advertises br/zstd, which net/http will NOT decode for us
	// (it only auto-decompresses when it set Accept-Encoding itself). Ask for
	// what we can actually decode.
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("User-Agent", jar.userAgent())
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if ck := jar.header(); ck != "" {
		req.Header.Set("Cookie", ck)
	}
	if opts.form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", c.Config().Host)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	jar.absorb(resp)

	var reader io.Reader = io.LimitReader(resp.Body, maxBodyBytes)
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Encoding")), "gzip") {
		gz, gzErr := gzip.NewReader(reader)
		if gzErr != nil {
			return nil, fmt.Errorf("rutracker: gzip: %w", gzErr)
		}
		defer gz.Close()
		reader = gz
	}

	out := &fetchResp{
		status:   resp.StatusCode,
		header:   resp.Header.Clone(),
		location: resp.Header.Get("Location"),
	}

	raw, err := io.ReadAll(io.LimitReader(reader, maxBodyBytes))
	if err != nil {
		return nil, err
	}
	out.raw = raw
	if !opts.binary {
		out.body = decodeCP1251(raw)
	}

	// Challenge detection: the header is authoritative, the body markers cover
	// interstitials served with other status codes.
	if isChallenge(resp.StatusCode, resp.Header, out.body, raw) {
		out.challenge = true
	}
	return out, nil
}

// fetchViaFlareSolverr solves the challenge in a real browser, adopts the
// resulting cf_clearance + User-Agent into our jar (they are bound together —
// a UA mismatch invalidates the cookie instantly) and returns the rendered page.
func (c *Client) fetchViaFlareSolverr(ctx context.Context, target string, opts fetchOpts) (*fetchResp, error) {
	if httpclient.FlareSolverrURL() == "" {
		return nil, errors.New("rutracker: flaresolverr not configured")
	}
	jar := c.jarRef()

	fsOpts := httpclient.FlareSolverrFetchOptions{
		Force:   true,
		Headers: map[string]string{"Referer": opts.referer},
	}
	if opts.method == http.MethodPost && opts.form != nil {
		fsOpts.Method = http.MethodPost
		fsOpts.PostData = opts.form.Encode()
	}
	// Replay what we already have (bb_session survives the challenge solve).
	cookies, _ := jar.snapshot()
	for name, value := range cookies {
		fsOpts.Cookies = append(fsOpts.Cookies, httpclient.FlareSolverrCookie{Name: name, Value: value})
	}

	res, ok := httpclient.FlareSolverrFetch(ctx, balancerName, target, fsOpts)
	if !ok {
		return nil, errors.New("rutracker: flaresolverr request failed")
	}
	if !strings.EqualFold(res.Status, "ok") {
		return nil, fmt.Errorf("rutracker: flaresolverr: %s", res.Message)
	}

	for _, ck := range res.Solution.Cookies {
		jar.set(ck.Name, ck.Value)
	}
	jar.setUserAgent(res.Solution.UserAgent)
	c.saveSession()

	body := res.Solution.Response // already UTF-8: never cp1251-decode it again
	return &fetchResp{
		status: res.Solution.Status,
		header: http.Header{},
		body:   body,
		raw:    []byte(body),
		viaFS:  true,
	}, nil
}

func (c *Client) jarRef() *cookieJar {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.jar
}

// isChallenge recognises Cloudflare's managed challenge. Measured on
// rutracker.org (2026-08): 403 + "cf-mitigated: challenge".
func isChallenge(status int, header http.Header, body string, raw []byte) bool {
	if strings.EqualFold(strings.TrimSpace(header.Get("cf-mitigated")), "challenge") {
		return true
	}
	probe := body
	if probe == "" {
		probe = string(raw)
	}
	if len(probe) > 200_000 {
		probe = probe[:200_000]
	}
	if strings.Contains(probe, "/cdn-cgi/challenge-platform/") ||
		strings.Contains(probe, "<title>Just a moment") ||
		strings.Contains(probe, "cf_chl_opt") {
		return true
	}
	// A bare 403 with a tiny body from the edge is a challenge in practice.
	return status == http.StatusForbidden && len(probe) < 200 && strings.Contains(probe, "Cloudflare")
}

// decodeCP1251 converts a forum page to UTF-8. rutracker declares
// windows-1251 (verified: <meta charset="Windows-1251">). If the payload is
// already valid UTF-8 with Cyrillic in it, it is passed through — that happens
// with FlareSolverr-rendered pages and with mirrors that transcode.
func decodeCP1251(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	if looksUTF8Cyrillic(raw) {
		return string(raw)
	}
	dec := transform.NewReader(strings.NewReader(string(raw)), charmap.Windows1251.NewDecoder())
	out, err := io.ReadAll(dec)
	if err != nil {
		return string(raw)
	}
	return string(out)
}

// looksUTF8Cyrillic reports whether raw is already UTF-8 with Cyrillic in it.
// Both conditions matter: a cp1251 page with Cyrillic is (virtually) never
// valid UTF-8, so validity alone plus at least one U+04xx sequence is a safe
// discriminator — and getting it wrong in either direction produces mojibake.
func looksUTF8Cyrillic(raw []byte) bool {
	if !utf8.Valid(raw) {
		return false
	}
	for i := 0; i < len(raw)-1; i++ {
		if (raw[i] == 0xD0 || raw[i] == 0xD1) && raw[i+1] >= 0x80 && raw[i+1] <= 0xBF {
			return true
		}
	}
	return false
}
