// Package hlsprobe answers the question a HEAD request cannot: does this
// stream actually play?
//
// Every source we ever lost was "up" the whole time. Alloha answered 200 while
// its WS contract had changed; Filmix served manifests and 403'd every segment
// once its hash expired; videoseed returned a playlist with no segments in it.
// A probe that stops at the manifest sees none of that — the failure lives one
// fetch deeper, on the first segment, which is exactly the fetch a player makes
// next and a health check never did.
//
// The package does no networking of its own: the caller supplies a Fetch that
// goes through whatever transport, headers and per-plugin quirks the real proxy
// path uses. A probe that fetches differently from the player is a probe that
// lies.
package hlsprobe

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/mediaprobe"
)

// Stage names how far the probe got. On failure it is the answer to "what
// broke": manifest = the source is down or blocking us, variant = the master
// points at something we cannot fetch, segment = the classic "plays for the
// server, 403s for everyone" hash/geo failure.
type Stage string

const (
	StageManifest Stage = "manifest"
	StageVariant  Stage = "variant"
	StageSegment  Stage = "segment"
	StageDirect   Stage = "direct" // not HLS: a plain media URL, byte-range checked
)

// Fetch performs one GET. rangeHdr is empty for whole-body fetches. finalURL is
// the post-redirect URL, needed to resolve relative playlist entries.
type Fetch func(ctx context.Context, rawURL, rangeHdr string) (status int, body []byte, finalURL string, err error)

// Result is a probe verdict. It is meant to be shown to an operator as-is.
type Result struct {
	OK    bool   `json:"ok"`
	Stage Stage  `json:"stage"`
	Err   string `json:"err,omitempty"`

	ManifestStatus int `json:"manifest_status"`
	VariantStatus  int `json:"variant_status,omitempty"`
	SegmentStatus  int `json:"segment_status,omitempty"`

	Variants  int    `json:"variants,omitempty"`
	Bandwidth int    `json:"bandwidth,omitempty"`
	Codecs    string `json:"codecs,omitempty"` // what the playlist CLAIMS
	Live      bool   `json:"live,omitempty"`

	// Media is what the segment bytes actually contain. Playlists lie by
	// omission more often than by error — no CODECS at all, or video listed and
	// audio left out — and "no sound on this TV" is always an audio codec the
	// device cannot decode. Knowing it before anyone watches is the point.
	Media mediaprobe.Tracks `json:"media,omitzero"`

	Segments     int    `json:"segments,omitempty"`
	SegmentBytes int    `json:"segment_bytes,omitempty"`
	SegmentURL   string `json:"segment_url,omitempty"`

	// Window — подпись живого окна (media-sequence + последний сегмент) на
	// момент пробы. Пустая, если плейлист не медийный. Сравнение подписей
	// РАЗНЫХ проб одного канала — дешёвый детект заморозки: см. WindowSig.
	Window string `json:"window,omitempty"`

	// Stale is set by ProbeLive when a live window stopped advancing — the
	// channel that answers 200 forever while showing a frozen frame.
	Stale bool `json:"stale,omitempty"`

	ElapsedMS int64 `json:"elapsed_ms"`

	// playlistURL is the media playlist the walk ended on (the variant, when
	// the entry point was a master). ProbeLive re-reads THIS, not the master —
	// a master never changes and would look frozen by definition.
	playlistURL string
}

const (
	maxManifestBytes = 2 << 20
	segmentRange     = "bytes=0-65535"
)

// Probe walks manifest → variant → first segment and reports where it stopped.
func Probe(ctx context.Context, manifestURL string, fetch Fetch) Result {
	started := time.Now()
	res := Result{Stage: StageManifest}
	defer func() { res.ElapsedMS = time.Since(started).Milliseconds() }()

	status, body, finalURL, err := fetch(ctx, manifestURL, "")
	res.ManifestStatus = status
	if err != nil {
		res.Err = err.Error()
		return res
	}
	if !ok2xx(status) {
		res.Err = "manifest status " + strconv.Itoa(status)
		return res
	}
	if len(body) > maxManifestBytes {
		body = body[:maxManifestBytes]
	}
	text := string(body)

	if !isPlaylist(text) {
		// Not HLS (progressive mp4, an mpd, or an error page served as 200).
		// Byte-range check is all we can do, and it is still worth doing: a
		// zero-length or HTML "body" behind a 200 is a dead source.
		res.Stage = StageDirect
		res.SegmentStatus = status
		res.SegmentBytes = len(body)
		res.Media = mediaprobe.Detect(body)
		res.OK = len(body) > 0 && !looksLikeHTML(text)
		if !res.OK {
			res.Err = "not a playlist and no media bytes"
		}
		return res
	}

	base := parseBase(finalURL, manifestURL)
	res.playlistURL = manifestURL

	if variants := ParseMaster(text, base); len(variants) > 0 {
		res.Variants = len(variants)
		v := cheapest(variants)
		res.Bandwidth, res.Codecs = v.Bandwidth, v.Codecs
		res.Stage = StageVariant

		status, body, finalURL, err = fetch(ctx, v.URL, "")
		res.VariantStatus = status
		if err != nil {
			res.Err = err.Error()
			return res
		}
		if !ok2xx(status) {
			res.Err = "variant status " + strconv.Itoa(status)
			return res
		}
		text = string(body)
		base = parseBase(finalURL, v.URL)
		res.playlistURL = v.URL
		if ParseMaster(text, base) != nil {
			// A master pointing at another master is legal but rare; one level
			// is as deep as a probe needs to go.
			res.Err = "nested master playlist"
			return res
		}
	}

	media := ParseMedia(text, base)
	res.Live = media.Live
	res.Segments = media.Count
	res.Stage = StageSegment
	// Подпись текущего окна — из УЖЕ скачанного манифеста, даром. Одна проба не
	// скажет, движется ли эфир; но вызывающий может сравнить подпись с прошлым
	// циклом и поймать замороженный канал без второго запроса и паузы, как в
	// ProbeLive.
	res.Window = WindowSig(text)

	// An empty media playlist is a real failure mode, not an edge case: it is
	// what a source returns when its session died but its web server did not.
	if media.First == "" {
		res.Err = "playlist has no segments"
		return res
	}
	res.SegmentURL = media.First

	status, body, _, err = fetch(ctx, media.First, segmentRange)
	// Some origins reject Range on segments (405/416) — retry whole-body once
	// rather than call a working source broken.
	if err == nil && (status == 405 || status == 416) {
		status, body, _, err = fetch(ctx, media.First, "")
	}
	res.SegmentStatus = status
	res.SegmentBytes = len(body)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	if !ok2xx(status) {
		res.Err = "segment status " + strconv.Itoa(status)
		return res
	}
	if len(body) == 0 {
		res.Err = "segment returned no bytes"
		return res
	}
	res.Media = mediaprobe.Detect(body)
	res.OK = true
	return res
}

// ProbeLive adds the check a single fetch cannot make: that a live stream is
// still MOVING. A frozen channel keeps serving the same playlist forever —
// every status is 200, every segment downloads, and the picture is a still
// frame. The only way to see it is to look twice.
//
// wait should be at least one target duration; the caller pays that latency, so
// this is for on-demand diagnosis and opt-in sweeps, not for probing thousands
// of channels on a timer.
func ProbeLive(ctx context.Context, manifestURL string, fetch Fetch, wait time.Duration) Result {
	res := Probe(ctx, manifestURL, fetch)
	if !res.OK || !res.Live {
		return res
	}
	// Re-read the same playlist we ended on (the variant, if there was one).
	target := res.playlistURL
	if target == "" {
		target = manifestURL
	}

	_, first, _, err := fetch(ctx, target, "")
	if err != nil {
		return res
	}
	select {
	case <-ctx.Done():
		return res
	case <-time.After(wait):
	}
	_, second, _, err := fetch(ctx, target, "")
	if err != nil {
		return res
	}

	if Stale(string(first), string(second)) {
		res.OK = false
		res.Stale = true
		res.Err = "live playlist did not advance in " + wait.String()
	}
	return res
}

// Stale reports whether two reads of the same live playlist describe the same
// window — same media sequence and same last segment. Either one alone gives
// false positives: some origins never send MEDIA-SEQUENCE, others repeat a
// segment name across a discontinuity.
func Stale(first, second string) bool {
	s1, u1 := windowOf(first)
	s2, u2 := windowOf(second)
	if s1 >= 0 && s2 >= 0 && s1 != s2 {
		return false
	}
	return u1 == u2
}

// WindowSig collapses a media playlist to a compact signature of its window:
// media-sequence + last segment. Two probes of the same channel taken far
// enough apart must differ — a live window that has not moved is a frozen
// channel. Callers keep the signature (a few bytes), not the manifest.
//
// Пустая строка = судить не о чем (не медиа-плейлист / нет ни sequence, ни
// сегментов); вызывающий обязан трактовать это как «неизвестно», а не «мёртв».
func WindowSig(text string) string {
	seq, last := windowOf(text)
	if seq < 0 && last == "" {
		return ""
	}
	return strconv.Itoa(seq) + "|" + last
}

func windowOf(text string) (seq int, last string) {
	seq = -1
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:") {
			if n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"))); err == nil {
				seq = n
			}
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		last = line
	}
	return seq, last
}

// Variant is one rung of a master playlist's ladder.
type Variant struct {
	URL        string
	Bandwidth  int
	Codecs     string
	Resolution string
}

// ParseMaster returns the variants of a master playlist, or nil if this is not
// one. URLs are resolved against base.
func ParseMaster(text string, base *url.URL) []Variant {
	if !strings.Contains(text, "#EXT-X-STREAM-INF") {
		return nil
	}
	var out []Variant
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "#EXT-X-STREAM-INF") {
			continue
		}
		v := Variant{
			Bandwidth:  attrInt(line, "BANDWIDTH"),
			Codecs:     attrStr(line, "CODECS"),
			Resolution: attrStr(line, "RESOLUTION"),
		}
		// The URI is the next non-comment, non-empty line.
		for j := i + 1; j < len(lines); j++ {
			next := strings.TrimSpace(lines[j])
			if next == "" || strings.HasPrefix(next, "#") {
				continue
			}
			v.URL = resolve(base, next)
			i = j
			break
		}
		if v.URL != "" {
			out = append(out, v)
		}
	}
	return out
}

// Media is what a probe needs from a media playlist.
type Media struct {
	First string // init segment if present, else the first media segment
	Count int
	Live  bool
}

// ParseMedia finds the first fetchable object of a media playlist. The init
// segment (EXT-X-MAP) comes first when present: it is what a player fetches
// first, and it is the one Filmix's hash rewriting kept breaking.
func ParseMedia(text string, base *url.URL) Media {
	m := Media{Live: !strings.Contains(text, "#EXT-X-ENDLIST")}
	var firstSegment string
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-MAP") {
			if uri := attrStr(line, "URI"); uri != "" && m.First == "" {
				m.First = resolve(base, uri)
			}
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		m.Count++
		if firstSegment == "" {
			firstSegment = resolve(base, line)
		}
	}
	if m.First == "" {
		m.First = firstSegment
	}
	return m
}

func isPlaylist(text string) bool {
	head := text
	if len(head) > 512 {
		head = head[:512]
	}
	return strings.Contains(head, "#EXTM3U")
}

func looksLikeHTML(text string) bool {
	head := strings.ToLower(strings.TrimSpace(text))
	if len(head) > 256 {
		head = head[:256]
	}
	return strings.HasPrefix(head, "<!doctype html") || strings.HasPrefix(head, "<html")
}

func ok2xx(status int) bool { return status >= 200 && status < 300 }

// cheapest picks the lowest-bandwidth rung: the probe is about reachability,
// and pulling 64 KB of a 4K rung on every check is a bandwidth bill, not a
// better answer.
func cheapest(vs []Variant) Variant {
	best := vs[0]
	for _, v := range vs[1:] {
		if best.Bandwidth == 0 || (v.Bandwidth > 0 && v.Bandwidth < best.Bandwidth) {
			best = v
		}
	}
	return best
}

func parseBase(finalURL, fallback string) *url.URL {
	raw := finalURL
	if raw == "" {
		raw = fallback
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	return u
}

func resolve(base *url.URL, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if base == nil {
		return ref
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return base.ResolveReference(u).String()
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
	if end := strings.IndexAny(rest, ","); end >= 0 {
		return rest[:end]
	}
	return rest
}

func attrInt(line, key string) int {
	n, err := strconv.Atoi(strings.TrimSpace(attrStr(line, key)))
	if err != nil {
		return 0
	}
	return n
}
