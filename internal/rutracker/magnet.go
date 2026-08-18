package rutracker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/anacrolix/torrent/metainfo"
)

// ResolveMagnet returns the magnet URI for a topic, using the permanent cache
// first. Exported for the /parse/rutracker/{id} resolver.
func (c *Client) ResolveMagnet(ctx context.Context, topicID int) (string, error) {
	if topicID <= 0 {
		return "", errors.New("rutracker: bad topic id")
	}
	if !c.Enabled() {
		return "", errors.New("rutracker: disabled")
	}
	return c.resolveMagnet(ctx, topicID)
}

// resolveMagnet: topic page first (one request, magnet complete with trackers),
// .torrent second. The listing has no hash at all and rutracker's public JSON
// API (api.rutracker.cc/v1/get_tor_hash) answers
// {"error":{"code":1,"text":"Temporarily disabled"}} as of 2026-08, so there is
// no batch shortcut — hence the permanent on-disk cache.
func (c *Client) resolveMagnet(ctx context.Context, topicID int) (string, error) {
	if m := c.hashes.get(topicID); m != "" {
		return m, nil
	}
	if err := c.ensureSession(ctx); err != nil {
		return "", err
	}
	cfg := c.Config()

	magnet, err := c.magnetFromTopic(ctx, cfg, topicID)
	if err == nil && magnet != "" {
		c.hashes.put(topicID, magnet)
		return magnet, nil
	}

	data, terr := c.torrentFile(ctx, cfg, topicID)
	if terr != nil {
		if err == nil {
			err = terr
		}
		return "", err
	}
	magnet, terr = torrentToMagnet(data)
	if terr != nil {
		return "", terr
	}
	c.hashes.put(topicID, magnet)
	return magnet, nil
}

func (c *Client) magnetFromTopic(ctx context.Context, cfg Config, topicID int) (string, error) {
	target := cfg.Host + "/forum/viewtopic.php?t=" + strconv.Itoa(topicID)
	resp, err := c.fetch(ctx, target, fetchOpts{referer: cfg.Host + "/forum/tracker.php"})
	if err != nil {
		return "", err
	}
	if !loggedInPage(resp.body) {
		c.invalidateSession()
		return "", errGuest
	}
	magnet := parseMagnetLink(resp.body)
	if magnet == "" {
		return "", fmt.Errorf("rutracker: no magnet on topic %d", topicID)
	}
	return magnet, nil
}

// TorrentFile downloads the .torrent for a topic. TorrServer accepts an HTTP
// link to a .torrent, which is what the lazy /parse/rutracker path serves.
func (c *Client) TorrentFile(ctx context.Context, topicID int) ([]byte, error) {
	if topicID <= 0 {
		return nil, errors.New("rutracker: bad topic id")
	}
	if !c.Enabled() {
		return nil, errors.New("rutracker: disabled")
	}
	if err := c.ensureSession(ctx); err != nil {
		return nil, err
	}
	return c.torrentFile(ctx, c.Config(), topicID)
}

func (c *Client) torrentFile(ctx context.Context, cfg Config, topicID int) ([]byte, error) {
	target := cfg.Host + "/forum/dl.php?t=" + strconv.Itoa(topicID)
	// dl.php needs a same-origin Referer and answers a 302 to login.php when
	// the session is gone — following it would hand us an HTML login page
	// masquerading as a torrent.
	resp, err := c.fetch(ctx, target, fetchOpts{
		referer:    cfg.Host + "/forum/viewtopic.php?t=" + strconv.Itoa(topicID),
		noRedirect: true,
		binary:     true,
	})
	if err != nil {
		return nil, err
	}
	if resp.status >= 300 && resp.status < 400 {
		c.invalidateSession()
		return nil, fmt.Errorf("rutracker: dl.php redirected to %s (session expired?)", resp.location)
	}
	if resp.status != http.StatusOK {
		return nil, fmt.Errorf("rutracker: dl.php HTTP %d", resp.status)
	}
	if !looksBencoded(resp.raw) {
		return nil, errors.New("rutracker: dl.php returned a page, not a torrent (session expired or challenge)")
	}
	return resp.raw, nil
}

// looksBencoded sniffs a bencoded dictionary, so an HTML error page never gets
// parsed as a torrent.
func looksBencoded(b []byte) bool {
	trimmed := bytes.TrimLeft(b, " \r\n\t")
	return len(trimmed) > 1 && trimmed[0] == 'd'
}

// torrentToMagnet mirrors plabTorrentToMagnet (internal/sisihttp/
// sisi_sources_pornlab.go:713): announce URLs are kept because pidtor builds
// its TorrServer stream URL from the magnet's &tr list.
func torrentToMagnet(data []byte) (string, error) {
	mi, err := metainfo.Load(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("rutracker: parse torrent: %w", err)
	}
	var sb strings.Builder
	sb.WriteString("magnet:?xt=urn:btih:")
	sb.WriteString(mi.HashInfoBytes().HexString())

	if info, err := mi.UnmarshalInfo(); err == nil && info.Name != "" {
		sb.WriteString("&dn=")
		sb.WriteString(url.QueryEscape(info.Name))
	}
	seen := map[string]bool{}
	addTracker := func(tr string) {
		tr = strings.TrimSpace(tr)
		if tr == "" || seen[tr] {
			return
		}
		seen[tr] = true
		sb.WriteString("&tr=")
		sb.WriteString(url.QueryEscape(tr))
	}
	addTracker(mi.Announce)
	for _, tier := range mi.AnnounceList {
		for _, tr := range tier {
			addTracker(tr)
		}
	}
	return sb.String(), nil
}
