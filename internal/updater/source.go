package updater

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Source abstracts where release metadata comes from. At the moment the
// single production implementation is Client (alcopa-site). The interface
// exists so tests can inject a stub without hitting the network.
type Source interface {
	Latest() (*Release, error)
}

// Client pulls release metadata from an alcopa-site deployment.
//
//	Base URL: https://lampac.site
//	Endpoints:
//	  GET /api/v1/releases/latest?channel=stable
//	  GET /api/v1/releases?channel=stable
//
// An optional token is sent as `Authorization: Bearer <token>` — used when
// the server restricts closed beta channels to authenticated clients.
type Client struct {
	BaseURL string // e.g. "https://lampac.site"
	Token   string // optional; enables private channels
	HTTP    *http.Client
	Channel string // "stable" or "beta"
}

// NewClient builds a Client with sane defaults. baseURL is trimmed of its
// trailing slash so handlers can concatenate paths freely.
func NewClient(baseURL, token, channel string) *Client {
	if channel == "" {
		channel = "stable"
	}
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 20 * time.Second},
		Channel: channel,
	}
}

// Latest returns the newest release visible in the configured channel.
// For any non-stable channel the server already merges in stable releases
// (subscribers still want production fixes), so a single call suffices.
// product=lampac-go pins the request to server binaries — the same endpoint
// also serves TV-app releases under a different product.
func (c *Client) Latest() (*Release, error) {
	if c.BaseURL == "" {
		return nil, errors.New("updater: server_url not configured")
	}
	url := c.BaseURL + "/api/v1/releases/latest?product=lampac-go&channel=" + c.Channel
	var rel Release
	if err := c.getJSON(url, &rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

func (c *Client) getJSON(url string, out any) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "lampac-go-updater")
	req.Header.Set("Accept", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		// Server says there is no published release for this channel yet —
		// surface as a typed error so callers can distinguish "nothing to do"
		// from "server down".
		return errors.New("updater: no published release for channel " + c.Channel)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("update server %s: HTTP %d — %s", url, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
