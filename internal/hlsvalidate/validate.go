// Package hlsvalidate checks HLS playlists for the structural mistakes that
// break players, and — more importantly — checks that OUR rewrite of an
// upstream playlist preserved everything the player needs.
//
// Why a comparison rather than a plain linter: the playlists we serve are not
// authored, they are transformed. Both playlist incidents we shipped were
// transformation losses, not authoring errors — the EXT-X-MAP init URI going
// out without the hash the CDN demands, and EXT-X-START disappearing so the
// YouTube EVENT playlist opened at the live edge instead of at zero. Neither
// changed the manifest's validity: both were "the tag we were given is not the
// tag we handed on". That is what CompareRewrite looks for, and what a unit
// test on the rewriter can now assert on real fixtures.
package hlsvalidate

import (
	"strconv"
	"strings"
)

// Issue is one finding. Rule is a stable identifier so tests can assert on it
// without matching prose.
type Issue struct {
	Rule string `json:"rule"`
	Msg  string `json:"msg"`
	Line int    `json:"line,omitempty"` // 1-based, 0 = whole playlist
}

func (i Issue) String() string {
	if i.Line > 0 {
		return i.Rule + " (line " + strconv.Itoa(i.Line) + "): " + i.Msg
	}
	return i.Rule + ": " + i.Msg
}

// Playlist is a parsed view: enough structure to reason about, not a full
// model. Everything we need is countable.
type Playlist struct {
	Master bool
	// URIs are the payload lines — segments in a media playlist, variant
	// playlists in a master — in file order.
	URIs []string
	// TagCounts is keyed by tag name without the leading '#', e.g. "EXT-X-MAP".
	TagCounts map[string]int
	// Attr holds the first value seen for the single-value tags whose exact
	// value matters to playback start.
	Attr map[string]string
	// MapURIs / KeyURIs / MediaURIs are the tag-embedded URIs a rewrite must not
	// forget: a player fetches them exactly like a segment, with the same
	// headers. MediaURIs in particular point at the alternate audio renditions —
	// forget one and the film plays without the voice the user picked.
	MapURIs   []string
	KeyURIs   []string
	MediaURIs []string
}

// Parse reads a playlist. It never fails: a malformed playlist is a finding,
// not an error.
func Parse(text string) *Playlist {
	p := &Playlist{TagCounts: map[string]int{}, Attr: map[string]string{}}
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "#") {
			p.URIs = append(p.URIs, line)
			continue
		}
		name := tagName(line)
		if name == "" {
			continue // a plain comment
		}
		p.TagCounts[name]++
		switch name {
		case "EXT-X-STREAM-INF":
			p.Master = true
		case "EXT-X-MAP":
			if u := attrStr(line, "URI"); u != "" {
				p.MapURIs = append(p.MapURIs, u)
			}
		case "EXT-X-KEY", "EXT-X-SESSION-KEY":
			if u := attrStr(line, "URI"); u != "" {
				p.KeyURIs = append(p.KeyURIs, u)
			}
		case "EXT-X-MEDIA":
			if u := attrStr(line, "URI"); u != "" {
				p.MediaURIs = append(p.MediaURIs, u)
			}
		case "EXT-X-MEDIA-SEQUENCE", "EXT-X-TARGETDURATION", "EXT-X-PLAYLIST-TYPE", "EXT-X-VERSION":
			if _, seen := p.Attr[name]; !seen {
				p.Attr[name] = tagValue(line)
			}
		case "EXT-X-START":
			if _, seen := p.Attr[name]; !seen {
				p.Attr[name] = attrStr(line, "TIME-OFFSET")
			}
		}
	}
	return p
}

// Lint reports structural problems in a single playlist.
func Lint(text string) []Issue {
	var out []Issue
	lines := strings.Split(text, "\n")

	firstMeaningful := -1
	for i, raw := range lines {
		if strings.TrimSpace(raw) != "" {
			firstMeaningful = i
			break
		}
	}
	if firstMeaningful < 0 {
		return []Issue{{Rule: "empty", Msg: "playlist is empty"}}
	}
	if !strings.HasPrefix(strings.TrimSpace(lines[firstMeaningful]), "#EXTM3U") {
		out = append(out, Issue{Rule: "no-extm3u", Msg: "playlist does not start with #EXTM3U", Line: firstMeaningful + 1})
	}

	p := Parse(text)
	if p.Master && p.TagCounts["EXTINF"] > 0 {
		out = append(out, Issue{Rule: "mixed-playlist", Msg: "master tags and EXTINF in the same playlist"})
	}

	// Every EXTINF and every EXT-X-STREAM-INF must be followed by a URI line.
	// A rewrite that drops a URI leaves the tag dangling and the player either
	// skips content silently or stalls at that point.
	pendingTag, pendingLine := "", 0
	for i, raw := range lines {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			switch tagName(line) {
			case "EXTINF", "EXT-X-STREAM-INF":
				if pendingTag != "" {
					out = append(out, Issue{Rule: "dangling-tag", Msg: pendingTag + " without a following URI", Line: pendingLine})
				}
				pendingTag, pendingLine = tagName(line), i+1
			}
			continue
		}
		pendingTag, pendingLine = "", 0
	}
	if pendingTag != "" {
		out = append(out, Issue{Rule: "dangling-tag", Msg: pendingTag + " without a following URI", Line: pendingLine})
	}

	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		switch tagName(line) {
		case "EXT-X-MAP", "EXT-X-KEY", "EXT-X-SESSION-KEY":
			if strings.Contains(line, "URI=") && attrStr(line, "URI") == "" {
				out = append(out, Issue{Rule: "empty-uri", Msg: tagName(line) + " has an empty URI", Line: i + 1})
			}
		}
	}

	if strings.EqualFold(p.Attr["EXT-X-PLAYLIST-TYPE"], "VOD") && p.TagCounts["EXT-X-ENDLIST"] == 0 {
		// A VOD playlist without ENDLIST is read as live: the player jumps to
		// the end of the window instead of starting at zero.
		out = append(out, Issue{Rule: "vod-without-endlist", Msg: "PLAYLIST-TYPE:VOD without #EXT-X-ENDLIST — players will treat it as live"})
	}
	return out
}

// CompareOpts describes the transformations that are legitimate for the
// rewrite under test. Everything not allowed here is reported.
type CompareOpts struct {
	// ProxyPrefix, when set, requires every URI in the output to either start
	// with it or (see AllowDirectSegments) stay an absolute upstream URL.
	ProxyPrefix string
	// AllowDirectSegments permits segment URIs to pass through unproxied —
	// manifest-only mode, where the video bytes deliberately skip the server.
	AllowDirectSegments bool
	// AllowWindowTrim permits the output to carry fewer segments than the
	// input (live DVR trimming), which also moves EXT-X-MEDIA-SEQUENCE.
	AllowWindowTrim bool
	// AllowedTagLoss lists tags the rewrite is expected to remove, e.g.
	// EXT-X-KEY when the server itself decrypts the segments.
	AllowedTagLoss []string
}

// CompareRewrite reports what a rewrite lost or corrupted. src is the upstream
// playlist, out is what we are about to serve.
func CompareRewrite(src, out string, opts CompareOpts) []Issue {
	var issues []Issue
	in, res := Parse(src), Parse(out)

	allowedLoss := map[string]bool{}
	for _, t := range opts.AllowedTagLoss {
		allowedLoss[strings.ToUpper(strings.TrimPrefix(t, "#"))] = true
	}

	for tag, n := range in.TagCounts {
		got := res.TagCounts[tag]
		if got >= n || allowedLoss[tag] {
			continue
		}
		if opts.AllowWindowTrim && trimmable(tag) {
			continue
		}
		issues = append(issues, Issue{
			Rule: "tag-lost",
			Msg:  "#" + tag + ": " + strconv.Itoa(n) + " in upstream, " + strconv.Itoa(got) + " after rewrite",
		})
	}

	// Segment count. Losing one segment is losing content; the player plays a
	// gap and nobody reports it as a bug, they report "звук уехал".
	if len(res.URIs) < len(in.URIs) && !opts.AllowWindowTrim {
		issues = append(issues, Issue{
			Rule: "uri-lost",
			Msg:  strconv.Itoa(len(in.URIs)) + " URIs in upstream, " + strconv.Itoa(len(res.URIs)) + " after rewrite",
		})
	}

	// Values that decide where playback starts. EXT-X-START going missing is
	// how a from-zero VOD became "opens at the live edge".
	for _, k := range []string{"EXT-X-START", "EXT-X-TARGETDURATION", "EXT-X-PLAYLIST-TYPE"} {
		want, had := in.Attr[k]
		if !had {
			continue
		}
		if got := res.Attr[k]; got != want {
			issues = append(issues, Issue{
				Rule: "attr-changed",
				Msg:  "#" + k + ": upstream " + quote(want) + ", after rewrite " + quote(got),
			})
		}
	}
	if want, had := in.Attr["EXT-X-MEDIA-SEQUENCE"]; had && !opts.AllowWindowTrim {
		if got := res.Attr["EXT-X-MEDIA-SEQUENCE"]; got != want {
			issues = append(issues, Issue{
				Rule: "attr-changed",
				Msg:  "#EXT-X-MEDIA-SEQUENCE: upstream " + quote(want) + ", after rewrite " + quote(got),
			})
		}
	}

	// A tag-embedded URI is fetched exactly like a segment. Leaving one
	// unrewritten sends the player to the CDN without our headers — the
	// hashless EXT-X-MAP 403, in one line.
	if opts.ProxyPrefix != "" {
		for _, u := range res.MapURIs {
			if !proxied(u, opts.ProxyPrefix) && !opts.AllowDirectSegments {
				issues = append(issues, Issue{Rule: "map-not-proxied", Msg: "EXT-X-MAP URI not rewritten: " + trunc(u)})
			}
		}
		for _, u := range res.KeyURIs {
			// Key fetches are never "just bytes": they carry auth and must go
			// through us even when segments do not.
			if !proxied(u, opts.ProxyPrefix) {
				issues = append(issues, Issue{Rule: "key-not-proxied", Msg: "EXT-X-KEY URI not rewritten: " + trunc(u)})
			}
		}
		for _, u := range res.MediaURIs {
			// An alternate rendition is another playlist: it needs the same
			// rewrite one level down, direct-segment mode or not.
			if !proxied(u, opts.ProxyPrefix) {
				issues = append(issues, Issue{Rule: "media-not-proxied", Msg: "EXT-X-MEDIA URI not rewritten: " + trunc(u)})
			}
		}
		for _, u := range res.URIs {
			if proxied(u, opts.ProxyPrefix) {
				continue
			}
			if opts.AllowDirectSegments && (strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")) {
				continue
			}
			issues = append(issues, Issue{Rule: "uri-not-proxied", Msg: "URI not rewritten: " + trunc(u)})
		}
	}

	// A relative URI in the output resolves against OUR host, not the CDN's —
	// always wrong, in every mode.
	relCheck := append(append([]string{}, res.URIs...), res.MapURIs...)
	relCheck = append(relCheck, res.MediaURIs...)
	for _, u := range relCheck {
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "/") {
			issues = append(issues, Issue{Rule: "relative-uri", Msg: "URI stayed relative and will resolve against our host: " + trunc(u)})
		}
	}

	issues = append(issues, Lint(out)...)
	return issues
}

// trimmable tags are the per-segment ones that disappear when a live window is
// shortened; structural tags are never legitimate to lose.
func trimmable(tag string) bool {
	switch tag {
	case "EXTINF", "EXT-X-DISCONTINUITY", "EXT-X-PROGRAM-DATE-TIME", "EXT-X-BYTERANGE":
		return true
	}
	return false
}

func proxied(u, prefix string) bool {
	return strings.HasPrefix(u, prefix) || strings.Contains(u, prefix)
}

func tagName(line string) string {
	if !strings.HasPrefix(line, "#EXT") {
		return ""
	}
	body := line[1:]
	if i := strings.IndexAny(body, ":,"); i >= 0 {
		body = body[:i]
	}
	return strings.ToUpper(strings.TrimSpace(body))
}

func tagValue(line string) string {
	_, v, ok := strings.Cut(line, ":")
	if !ok {
		return ""
	}
	return strings.TrimSpace(v)
}

func attrStr(line, key string) string {
	idx := strings.Index(line, key+"=")
	if idx < 0 {
		return ""
	}
	rest := line[idx+len(key)+1:]
	if strings.HasPrefix(rest, `"`) {
		rest = rest[1:]
		if end := strings.Index(rest, `"`); end >= 0 {
			return rest[:end]
		}
		return rest
	}
	if end := strings.IndexByte(rest, ','); end >= 0 {
		return rest[:end]
	}
	return strings.TrimSpace(rest)
}

func quote(s string) string {
	if s == "" {
		return "(absent)"
	}
	return `"` + s + `"`
}

func trunc(s string) string {
	if len(s) > 90 {
		return s[:90] + "…"
	}
	return s
}
