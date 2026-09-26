package litesrc

import (
	"regexp"
	"strings"
)

// videodbVoiceTagRe matches the tag block videodb/zetflixdb prefixes a voice
// with: an optional language in parentheses, then a short dub-type code, then a
// pipe — "(RU) DUB | ", "(BE) DUB | ", "MVO | ".
//
// Only the FIRST pipe is consumed: studio names contain pipes of their own
// ("(RU) DVO | Кубик в Кубе | Kubik³"), and cutting at the last one would leave
// «Kubik³» alone.
var videodbVoiceTagRe = regexp.MustCompile(`^(?:\(([A-Za-z]{2,3})\)\s*)?([A-Za-z0-9]{2,6})\s*\|\s*`)

// videodbCleanVoiceNames drops the tag block so the picker shows the studio
// rather than "(RU) DUB | Studio", and returns the names in input order.
//
// The catch is that the tag is not always decoration. Obsession (2026) lists
// both "(RU) DUB | HDrezka Studio" and "(RU) MVO | HDrezka Studio" — a dub and
// a multi-voice-over of the same studio, two different tracks. Stripping both
// down to "HDrezka Studio" would collapse them into one, because /capi merges
// voices by name. So a name is shortened only while it stays unique; where two
// would collide, the dub-type code is kept on both and the pair stays
// distinguishable.
func videodbCleanVoiceNames(names []string) []string {
	stripped := make([]string, len(names))
	withCode := make([]string, len(names))
	counts := make(map[string]int, len(names))

	for i, raw := range names {
		name := strings.TrimSpace(raw)
		m := videodbVoiceTagRe.FindStringSubmatch(name)
		if m == nil {
			stripped[i], withCode[i] = name, name
			counts[name]++
			continue
		}
		rest := strings.TrimSpace(name[len(m[0]):])
		if rest == "" {
			// Nothing but a tag — keep the original rather than emit "".
			stripped[i], withCode[i] = name, name
			counts[name]++
			continue
		}
		stripped[i] = rest
		withCode[i] = m[2] + " | " + rest
		counts[rest]++
	}

	out := make([]string, len(names))
	for i := range names {
		if counts[stripped[i]] > 1 {
			out[i] = withCode[i]
			continue
		}
		out[i] = stripped[i]
	}
	return out
}
