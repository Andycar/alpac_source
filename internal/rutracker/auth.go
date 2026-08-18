package rutracker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// sessionTTL — bb_session outlives a day in practice; the reference parser
// caches it until midnight (RutrackerController.cs:435). We re-login on the
// signal that actually matters (a page without loggedInMarker) and use the TTL
// only as a backstop.
const sessionTTL = 12 * time.Hour

// authRetryAfter throttles login attempts after a failure so a wrong password
// cannot turn into a login flood (the reference uses 20s).
const authRetryAfter = 60 * time.Second

// loginFormValue is "Вход" in windows-1251 — the forum renders its login form
// in cp1251, so this is byte-for-byte what a browser submits.
const loginFormValue = "\xC2\xF5\xEE\xE4"

// ensureSession makes sure we hold a usable bb_session. Concurrent callers
// collapse onto one login: ten parallel searches logging in ten times is the
// straightest path to a blocked account.
func (c *Client) ensureSession(ctx context.Context) error {
	cfg := c.Config()
	if !c.Enabled() {
		return errors.New("rutracker: disabled or no credentials configured")
	}

	c.authMu.Lock()
	defer c.authMu.Unlock()

	// A manually supplied cookie short-circuits the login entirely — it is the
	// escape hatch when the challenge cannot be solved server-side.
	if raw := strings.TrimSpace(cfg.Cookie); raw != "" {
		c.applyCookieString(raw)
		c.loggedIn = true
		c.loginAt = time.Now()
		return nil
	}

	if c.loggedIn && time.Since(c.loginAt) < sessionTTL && c.jarRef().get("bb_session") != "" {
		return nil
	}
	if c.authErr != nil && time.Since(c.authErrAt) < authRetryAfter {
		return c.authErr
	}

	err := c.login(ctx, cfg)
	if err != nil {
		c.loggedIn = false
		c.authErr = err
		c.authErrAt = time.Now()
		c.st.setErr(err)
		return err
	}
	c.loggedIn = true
	c.loginAt = time.Now()
	c.authErr = nil
	c.st.setErr(nil)
	c.saveSession()
	return nil
}

// invalidateSession marks the session dead so the next ensureSession logs in
// again. Called when a page comes back without the logged-in marker.
func (c *Client) invalidateSession() {
	c.authMu.Lock()
	c.loggedIn = false
	c.authMu.Unlock()
	c.jarRef().set("bb_session", "")
}

// applyCookieString accepts either a bare bb_session value or a full
// "name=value; name=value" cookie header, which is what a user copies out of
// devtools.
func (c *Client) applyCookieString(raw string) {
	jar := c.jarRef()
	if !strings.Contains(raw, "=") {
		jar.set("bb_session", raw)
		return
	}
	for _, part := range strings.Split(raw, ";") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		jar.set(strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1]))
	}
}

func (c *Client) login(ctx context.Context, cfg Config) error {
	form := url.Values{
		"login_username": {cfg.Login},
		"login_password": {cfg.Password},
		"login":          {loginFormValue},
	}

	// Redirects are not followed: the session cookie rides on the 302 itself
	// (RutrackerController.cs:397).
	resp, err := c.fetch(ctx, cfg.Host+"/forum/login.php", fetchOpts{
		method:     http.MethodPost,
		form:       form,
		referer:    cfg.Host + "/forum/login.php",
		noRedirect: true,
	})
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}

	if session := c.jarRef().get("bb_session"); session != "" {
		log.Info().Str("host", cfg.Host).Bool("via_fs", resp.viaFS).Msg("rutracker: logged in")
		return nil
	}

	// No cookie: either wrong credentials or a captcha (rutracker shows one
	// after repeated failures). Both are surfaced verbatim in the admin panel.
	if strings.Contains(resp.body, "cap_sid") || strings.Contains(resp.body, "captcha") {
		return errors.New("login: captcha requested — log in with a browser and paste the bb_session cookie into [parser.rutracker] cookie")
	}
	return errors.New("login: no bb_session cookie (wrong login/password?)")
}

// loggedInPage reports whether a fetched page was rendered for our account.
func loggedInPage(body string) bool { return strings.Contains(body, loggedInMarker) }
