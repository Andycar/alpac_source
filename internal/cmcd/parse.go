// Package cmcd reads the Common Media Client Data (CTA-5004) that players
// attach to every segment request, and turns it into an answer to the question
// we currently cannot answer at all: how does playback actually FEEL on a given
// source, on a given kind of device, right now.
//
// Why this and not more server-side logging: the server already knows what it
// served and how fast — it does not know that the player's buffer ran dry three
// times, that the ladder collapsed to 480p, or that the first frame took nine
// seconds. Those live in the client, and until now they reached us only as a
// Telegram message saying "тормозит". CMCD is the standard way for the player to
// put them into the request it was making anyway, so the proxy — which already
// sees every manifest and segment fetch — becomes the collection point for free.
//
// The parser is deliberately forgiving: unknown keys are ignored (v2 adds more
// every revision), malformed values are dropped rather than failing the request.
// Nothing here may ever make a segment fetch fail — telemetry is worth strictly
// less than the playback it observes.
package cmcd

import (
	"net/http"
	"strconv"
	"strings"
)

// Object type values (the `ot` key).
const (
	ObjManifest    = "m"
	ObjAudio       = "a"
	ObjVideo       = "v"
	ObjMuxed       = "av"
	ObjInit        = "i"
	ObjCaption     = "c"
	ObjTimedText   = "tt"
	ObjKey         = "k"
	ObjOtherObject = "o"
)

// Report is one request's worth of client data. Zero values mean "not sent" —
// every player picks its own subset of the spec, and most send only a handful.
type Report struct {
	SessionID string // sid — stable for one playback session
	ContentID string // cid — our token/content identifier, player-chosen

	ObjectType      string // ot
	StreamingFormat string // sf: d=DASH, h=HLS, s=Smooth, o=other
	StreamType      string // st: v=VOD, l=live

	EncodedBitrateKbps  int // br  — bitrate of the variant being fetched
	TopBitrateKbps      int // tb  — top rung of the ladder the player sees
	BufferLengthMS      int // bl  — buffer ahead of the playhead
	DeadlineMS          int // dl  — deadline for this request
	ThroughputKbps      int // mtp — throughput the player measured
	RequestedThroughput int // rtp — what the player asks the CDN to sustain
	ObjectDurationMS    int // d
	MediaStartDelayMS   int // msd — v2: time from play() to first frame
	LiveLatencyMS       int // ltc — v2

	PlaybackRate float64 // pr — 0 means "not sent", 1 is normal speed

	Startup    bool // su — this request is blocking playback start
	Starvation bool // bs — the buffer ran dry before this request

	// Any set to true when at least one CMCD key was understood. A request with
	// a CMCD header full of keys we don't know is not a report.
	Any bool
}

// hasKeys is cheap enough to run on every proxied request: two map lookups and
// a substring scan, no allocation, no parsing.
func hasKeys(r *http.Request) bool {
	h := r.Header
	if h.Get("CMCD-Request") != "" || h.Get("CMCD-Object") != "" ||
		h.Get("CMCD-Session") != "" || h.Get("CMCD-Status") != "" {
		return true
	}
	q := r.URL.RawQuery
	return q != "" && (strings.Contains(q, "CMCD=") || strings.Contains(q, "cmcd="))
}

// Parse pulls CMCD out of a request, whether the player sent it as the `CMCD`
// query argument or as the four CMCD-* headers. Returns ok=false when there is
// nothing to report, so callers can skip the collector entirely.
func Parse(r *http.Request) (Report, bool) {
	if !hasKeys(r) {
		return Report{}, false
	}

	var rep Report
	if v := r.URL.Query().Get("CMCD"); v != "" {
		parseInto(v, &rep)
	} else if v := r.URL.Query().Get("cmcd"); v != "" {
		parseInto(v, &rep)
	}
	for _, name := range [...]string{"CMCD-Object", "CMCD-Request", "CMCD-Session", "CMCD-Status"} {
		if v := r.Header.Get(name); v != "" {
			parseInto(v, &rep)
		}
	}
	return rep, rep.Any
}

// ParseString exists for tests and for callers that already hold the raw value.
func ParseString(s string) (Report, bool) {
	var rep Report
	parseInto(s, &rep)
	return rep, rep.Any
}

// splitTop splits a CMCD payload on commas that are not inside a quoted string.
// Content ids legitimately contain commas, so a plain strings.Split corrupts
// them — and a corrupted cid silently splits one session's stats into two.
func splitTop(s string) []string {
	out := make([]string, 0, 8)
	inQuotes, escaped, start := false, false, 0
	for i := 0; i < len(s); i++ {
		switch {
		case escaped:
			escaped = false
		case s[i] == '\\' && inQuotes:
			escaped = true
		case s[i] == '"':
			inQuotes = !inQuotes
		case s[i] == ',' && !inQuotes:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func unquote(v string) string {
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		v = v[1 : len(v)-1]
		if strings.IndexByte(v, '\\') >= 0 {
			v = strings.ReplaceAll(v, `\"`, `"`)
			v = strings.ReplaceAll(v, `\\`, `\`)
		}
	}
	return v
}

// clampStr keeps player-supplied strings from growing the key space or the log
// lines without bound — sid/cid are attacker-controllable in the same sense any
// request header is.
func clampStr(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) > max {
		s = s[:max]
	}
	return s
}

func parseInto(payload string, rep *Report) {
	for _, part := range splitTop(payload) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, val, hasVal := strings.Cut(part, "=")
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)

		// A key with no value is boolean true — that is how `su` and `bs`,
		// the two most interesting keys in the whole spec, are transmitted.
		boolVal := true
		if hasVal {
			switch strings.ToLower(val) {
			case "false":
				boolVal = false
			case "true":
				boolVal = true
			}
		}

		// setStr / setNum only mark the report as usable when the value itself
		// survived validation: a known key carrying garbage (`bl=-5`) tells us
		// nothing, and must not turn an empty report into a recorded one.
		setStr := func(dst *string, max int, lower bool) {
			v := clampStr(unquote(val), max)
			if lower {
				v = strings.ToLower(v)
			}
			if v != "" {
				*dst, rep.Any = v, true
			}
		}
		setNum := func(dst *int) {
			if n, ok := atoiClamp(val); ok {
				*dst, rep.Any = n, true
			}
		}

		switch key {
		case "sid":
			setStr(&rep.SessionID, 64, false)
		case "cid":
			setStr(&rep.ContentID, 64, false)
		case "ot":
			setStr(&rep.ObjectType, 4, true)
		case "sf":
			setStr(&rep.StreamingFormat, 2, true)
		case "st":
			setStr(&rep.StreamType, 2, true)
		case "br":
			setNum(&rep.EncodedBitrateKbps)
		case "tb":
			setNum(&rep.TopBitrateKbps)
		case "bl":
			setNum(&rep.BufferLengthMS)
		case "dl":
			setNum(&rep.DeadlineMS)
		case "mtp":
			setNum(&rep.ThroughputKbps)
		case "rtp":
			setNum(&rep.RequestedThroughput)
		case "d":
			setNum(&rep.ObjectDurationMS)
		case "msd":
			setNum(&rep.MediaStartDelayMS)
		case "ltc":
			setNum(&rep.LiveLatencyMS)
		case "pr":
			if f, err := strconv.ParseFloat(val, 64); err == nil && f > 0 && f < 100 {
				rep.PlaybackRate, rep.Any = f, true
			}
		case "su":
			rep.Startup, rep.Any = boolVal, true
		case "bs":
			rep.Starvation, rep.Any = boolVal, true
		}
	}
}

// atoiClamp drops negatives and absurd magnitudes: a single bogus value must not
// be able to skew an average that an operator will read as "the source is slow".
func atoiClamp(v string) (int, bool) {
	n, err := strconv.Atoi(v)
	if err != nil {
		// Some players emit floats where the spec wants an integer.
		f, ferr := strconv.ParseFloat(v, 64)
		if ferr != nil {
			return 0, false
		}
		n = int(f)
	}
	if n < 0 || n > 1<<30 {
		return 0, false
	}
	return n, true
}
