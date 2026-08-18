package playbackstats

// Turning the collected failures back into a decision.
//
// Until now this package only accumulated: clients reported what their player
// choked on and nobody read it. The aggregate answers a question the client
// cannot answer on its own — «devices like yours fail on this codec» — because
// one device only ever learns that by failing first, in front of the user.

import (
	"sort"
	"strings"
)

// Advice is what a client should steer away from on this hardware.
type Advice struct {
	AvoidAudio []string `json:"avoid_audio"`
	AvoidVideo []string `json:"avoid_video"`
	// PreferTranscode is set only for VIDEO gaps. An audio gap is fixable on the
	// device (software decoders ship in the app, passthrough can be turned off);
	// a missing video decoder is not, and re-encoding on the server is the only
	// way out. Transcoding because of an audio track would be a cannon at a fly.
	PreferTranscode bool `json:"prefer_transcode"`
	// Evidence is how many failing reports backed the advice — surfaced so the
	// client can log WHY it steered, and so a human can sanity-check it.
	Evidence int `json:"evidence"`
	// BlockTunneling: на этой модели tunneled-видеопуть даёт «звук идёт, кадров
	// нет» (no_frames с tunneling=true), и НИ ОДНОГО ok с туннелем не было.
	// Новая установка той же модели выключает туннель до первого чёрного экрана.
	// Свойство прошивки+декодера, НЕ звуковой обвязки — поэтому по модели можно
	// (в отличие от passthrough-квирков, которые зависят от AVR за HDMI и
	// флотом НЕ раздаются — см. AudioQuirks на устройстве).
	BlockTunneling bool `json:"block_tunneling"`
}

const (
	// adviceMinFailures — how many failures on one device+codec pair before we
	// act. Two is noise (a bad file, a flaky mirror); three is a pattern.
	adviceMinFailures = 3
)

// AdviceFor answers "what should this device avoid".
//
// Deliberately per-DEVICE, never per-platform: a codec that dies on a cheap MTK
// box works fine on a Shield, and a platform-wide "avoid" would push everyone to
// the transcoder — turning a niche hardware gap into a server-load problem.
// An unknown device therefore gets empty advice, not a guess.
func (s *Store) AdviceFor(platform, device string) Advice {
	platform = norm(platform, 16)
	device = norm(device, 48)
	if device == "" {
		return Advice{AvoidAudio: []string{}, AvoidVideo: []string{}}
	}

	type tally struct{ ok, bad int }
	audio := map[string]*tally{}
	video := map[string]*tally{}

	get := func(m map[string]*tally, mime string) *tally {
		if mime == "" {
			return nil
		}
		t := m[mime]
		if t == nil {
			t = &tally{}
			m[mime] = t
		}
		return t
	}

	tunOK, tunBad := 0, 0

	s.mu.Lock()
	for _, r := range s.rows {
		if platform != "" && r.Platform != platform {
			continue
		}
		n := r.Devices[device]
		if n == 0 {
			continue
		}
		if r.Outcome == OutcomeOK {
			if t := get(audio, r.AudioMime); t != nil {
				t.ok += n
			}
			if t := get(video, r.VideoMime); t != nil {
				t.ok += n
			}
			if r.Tunneling {
				tunOK += n
			}
			continue
		}
		if r.Outcome == OutcomeNoFrames && r.Tunneling {
			tunBad += n
		}
		if t := get(audio, r.AudioMime); t != nil {
			t.bad += n
		}
		if t := get(video, r.VideoMime); t != nil {
			t.bad += n
		}
	}
	s.mu.Unlock()

	// A codec that ever played on this device is NOT avoided, however many times
	// it also failed: the failures then come from the file or the network, and
	// blacklisting it would cost the user quality for someone else's bad release.
	pick := func(m map[string]*tally) ([]string, int) {
		out := make([]string, 0, 4)
		evidence := 0
		for mime, t := range m {
			if t.ok == 0 && t.bad >= adviceMinFailures {
				out = append(out, mime)
				evidence += t.bad
			}
		}
		sort.Strings(out) // stable response — clients cache it
		return out, evidence
	}

	av, ae := pick(audio)
	vv, ve := pick(video)

	// Туннель блокируем той же логикой «ни разу не сыграло»: один ok с туннелем
	// на этой модели — и фейлы объясняются чем-то другим (форматом, сетью).
	blockTun := tunOK == 0 && tunBad >= adviceMinFailures
	ev := ae + ve
	if blockTun {
		ev += tunBad
	}

	return Advice{
		AvoidAudio:      av,
		AvoidVideo:      vv,
		PreferTranscode: len(vv) > 0,
		Evidence:        ev,
		BlockTunneling:  blockTun,
	}
}

// KnownDevices lists the device models we have any data for, most reported
// first. Used by the admin page to show whose advice is actually backed.
func (s *Store) KnownDevices(limit int) []string {
	s.mu.Lock()
	counts := map[string]int{}
	for _, r := range s.rows {
		for d, n := range r.Devices {
			counts[d] += n
		}
	}
	s.mu.Unlock()

	out := make([]string, 0, len(counts))
	for d := range counts {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		if counts[out[i]] != counts[out[j]] {
			return counts[out[i]] > counts[out[j]]
		}
		return out[i] < out[j]
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// DeviceLabel builds the key clients send as `device`. Kept here so the server
// and every client agree on one spelling — «Xiaomi MiBox» and «xiaomi mibox»
// splitting into two devices would halve the evidence for both.
func DeviceLabel(manufacturer, model string) string {
	// strings.Fields, а не TrimSpace: у части устройств manufacturer или model
	// приходят с лишними пробелами, и «xiaomi  mibox» с двойным пробелом стало бы
	// ОТДЕЛЬНЫМ устройством — доказательная база разделилась бы пополам, и ни одна
	// половина не дотянула бы до порога.
	return norm(strings.Join(strings.Fields(manufacturer+" "+model), " "), 48)
}
