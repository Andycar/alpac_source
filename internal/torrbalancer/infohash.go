package torrbalancer

import (
	"net/url"
	"regexp"
	"strings"
)

var (
	// 40-char hex SHA-1 infohash.
	hex40Re = regexp.MustCompile(`^[0-9a-f]{40}$`)
	// 32-char base32 SHA-1 infohash.
	base32Re = regexp.MustCompile(`^[a-z2-7]{32}$`)
	// btih component inside a magnet URI.
	btihRe = regexp.MustCompile(`(?i)urn:btih:([0-9a-z]+)`)
)

// ExtractInfohash derives a stable routing key from a TorrServer request's
// `hash` and `link` parameters. The key is what binds a torrent to a single
// backend (TorrServer is stateful per torrent), so it must be identical across
// the add → playlist → stream → seek lifecycle of one torrent.
//
// Resolution order:
//  1. `hash` if it looks like a 40-hex or 32-base32 infohash.
//  2. `link`:
//     - magnet:?xt=urn:btih:<H>  → the btih value (lowercased).
//     - a bare hex/base32 infohash → as-is (lowercased).
//     - a .torrent (or any) URL  → the URL itself (fragment stripped) — stable
//     per torrent even though it isn't a real infohash.
//  3. otherwise a non-empty `hash` as-is, else "".
//
// Returns "" when neither parameter yields anything usable; the caller then
// routes the request to the pool's primary backend (PickPrimary).
func ExtractInfohash(link, hash string) string {
	hash = strings.ToLower(strings.TrimSpace(hash))
	if hex40Re.MatchString(hash) || base32Re.MatchString(hash) {
		return hash
	}

	link = strings.TrimSpace(link)
	if link == "" {
		// hash wasn't a recognizable infohash but may still be a usable opaque
		// key (e.g. a TorrServer-internal id) — use it if present.
		return hash
	}

	low := strings.ToLower(link)
	if strings.HasPrefix(low, "magnet:") {
		if m := btihRe.FindStringSubmatch(link); m != nil {
			return strings.ToLower(m[1])
		}
		// magnet without a btih component (rare) — key on the whole URI.
		return low
	}
	if hex40Re.MatchString(low) || base32Re.MatchString(low) {
		return low
	}

	// .torrent URL or any other link: key on the URL (fragment stripped so
	// player-appended #t=... etc. doesn't fork the routing key).
	if u, err := url.Parse(link); err == nil && u.Host != "" {
		u.Fragment = ""
		return strings.ToLower(u.String())
	}
	return low
}
