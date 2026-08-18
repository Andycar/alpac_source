// Package playbackstats aggregates what the built-in players can and cannot
// actually play.
//
// The weblog already carries every diagnostic line a client emits, but it is a
// firehose into journalctl: perfect for reconstructing one user's incident,
// useless for the question that drives roadmap decisions — "which codec, on
// which devices, silently fails, and for how many people?"
//
// So clients post one small structured report per playback, and this keeps
// counters per distinct (outcome, codec, decoder-present, source) combination.
// Nothing here identifies a user: the point is a histogram, not a session log.
package playbackstats

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Outcomes a client reports. Anything unknown is coerced to OutcomeOther so a
// newer client can't silently create unbounded key space.
const (
	OutcomeOK              = "ok"               // played, audio and video fine
	OutcomeNoAudio         = "no_audio"         // picture plays, no sound decoded
	OutcomePassthroughFail = "passthrough_fail" // AudioTrack refused the encoding; fell back
	OutcomeDecodeError     = "decode_error"     // decoder initialisation/decoding failed
	OutcomeUnsupported     = "unsupported"      // no decoder at all for the track
	OutcomeLoadError       = "load_error"       // never got as far as decoding (manifest/network)
	OutcomeNoFrames        = "no_frames"        // decoder up, position advances, no frame rendered
	OutcomeSilentSource    = "silent_source"    // stream carries NO audio track at all — broken source, not a device gap
	OutcomeOther           = "other"
)

var knownOutcomes = map[string]bool{
	OutcomeOK: true, OutcomeNoAudio: true, OutcomePassthroughFail: true,
	OutcomeDecodeError: true, OutcomeUnsupported: true, OutcomeLoadError: true,
	OutcomeNoFrames: true, OutcomeSilentSource: true,
	OutcomeOther: true,
}

// Report is one playback attempt as the client saw it.
type Report struct {
	Outcome string `json:"outcome"`
	// Source of the stream: "torrent" | "balancer" | "iptv" | "youtube" | "offline".
	// Torrent files are the interesting bucket — they carry whatever the release
	// group used, unlike balancer streams which are already normalised.
	Source string `json:"source"`

	VideoMime string `json:"video_mime"`
	AudioMime string `json:"audio_mime"`
	Channels  int    `json:"channels"`

	// AudioDecoder reports whether the device has ANY decoder for AudioMime.
	// This is the field that turns "no sound" from a mystery into an answer.
	AudioDecoder bool `json:"audio_decoder"`
	VideoDecoder bool `json:"video_decoder"`

	// Tunneling — был ли включён tunneled-видеопуть в этой сессии. Единственный
	// способ отличить «no_frames из-за туннеля» (чинится его отключением) от
	// «no_frames потому что железо не показывает формат» (чинится транскодом).
	// ok-отчёты с tunneling=true ОПРОВЕРГАЮТ квирк: у кого-то на этой модели
	// туннель работает — значит, дело не в нём.
	Tunneling bool `json:"tunneling"`

	Platform string `json:"platform"` // "androidtv" | "web" | "tvos"
	Device   string `json:"device"`   // model / UA family — coarse on purpose
	OSVer    string `json:"os_ver"`
	AppVer   string `json:"app_ver"`
}

// Row is an aggregated bucket.
type Row struct {
	Outcome      string         `json:"outcome"`
	Source       string         `json:"source"`
	AudioMime    string         `json:"audio_mime"`
	VideoMime    string         `json:"video_mime"`
	AudioDecoder bool           `json:"audio_decoder"`
	Tunneling    bool           `json:"tunneling,omitempty"`
	Platform     string         `json:"platform"`
	Count        int            `json:"count"`
	Devices      map[string]int `json:"devices,omitempty"` // model → count, capped
	FirstSeen    time.Time      `json:"first_seen"`
	LastSeen     time.Time      `json:"last_seen"`
}

const (
	maxRows          = 4000 // distinct buckets; far above any real codec matrix
	maxDevicesPerRow = 40
)

// Store is the aggregate, persisted as a flat row list.
type Store struct {
	mu   sync.Mutex
	rows map[string]*Row
	path string

	dirty    bool
	lastSave time.Time
}

func New(repoRoot string) *Store {
	dir := filepath.Join(repoRoot, "database")
	_ = os.MkdirAll(dir, 0o755)
	s := &Store{
		rows: map[string]*Row{},
		path: filepath.Join(dir, "playback_stats.json"),
	}
	s.load()
	return s
}

// norm keeps the key space bounded and stable: clients differ in casing and
// occasionally send a full codec string where a mime is expected.
func norm(s string, max int) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) > max {
		s = s[:max]
	}
	return s
}

func key(r Report) string {
	return strings.Join([]string{
		r.Outcome, r.Source, r.AudioMime, r.VideoMime,
		map[bool]string{true: "1", false: "0"}[r.AudioDecoder],
		map[bool]string{true: "1", false: "0"}[r.Tunneling],
		r.Platform,
	}, "|")
}

// Add records one report.
func (s *Store) Add(r Report) {
	r.Outcome = norm(r.Outcome, 24)
	if !knownOutcomes[r.Outcome] {
		r.Outcome = OutcomeOther
	}
	r.Source = norm(r.Source, 16)
	r.AudioMime = norm(r.AudioMime, 48)
	r.VideoMime = norm(r.VideoMime, 48)
	r.Platform = norm(r.Platform, 16)
	device := norm(r.Device, 48)

	s.mu.Lock()
	defer s.mu.Unlock()

	k := key(r)
	row := s.rows[k]
	if row == nil {
		if len(s.rows) >= maxRows {
			return // saturated: drop rather than grow without bound
		}
		row = &Row{
			Outcome: r.Outcome, Source: r.Source,
			AudioMime: r.AudioMime, VideoMime: r.VideoMime,
			AudioDecoder: r.AudioDecoder, Tunneling: r.Tunneling, Platform: r.Platform,
			Devices:   map[string]int{},
			FirstSeen: time.Now().UTC(),
		}
		s.rows[k] = row
	}
	row.Count++
	row.LastSeen = time.Now().UTC()
	if device != "" && (len(row.Devices) < maxDevicesPerRow || row.Devices[device] > 0) {
		row.Devices[device]++
	}
	s.dirty = true

	// Throttled persistence: playback reports arrive in bursts (every episode
	// start), and rewriting the file per report would be pure IO churn.
	if time.Since(s.lastSave) > 30*time.Second {
		s.saveLocked()
	}
}

// Problems returns the buckets worth acting on — everything that is not a clean
// "ok" — most frequent first. This is the answer to "what should we fix next".
func (s *Store) Problems(limit int) []Row {
	return s.list(limit, func(r *Row) bool { return r.Outcome != OutcomeOK })
}

// All returns every bucket, most frequent first.
func (s *Store) All(limit int) []Row {
	return s.list(limit, func(*Row) bool { return true })
}

func (s *Store) list(limit int, keep func(*Row) bool) []Row {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Row, 0, len(s.rows))
	for _, r := range s.rows {
		if !keep(r) {
			continue
		}
		cp := *r
		cp.Devices = make(map[string]int, len(r.Devices))
		for k, v := range r.Devices {
			cp.Devices[k] = v
		}
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].LastSeen.After(out[j].LastSeen)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Summary is the headline: how many playbacks, and what share failed.
func (s *Store) Summary() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()

	total, bad := 0, 0
	byOutcome := map[string]int{}
	for _, r := range s.rows {
		total += r.Count
		byOutcome[r.Outcome] += r.Count
		if r.Outcome != OutcomeOK {
			bad += r.Count
		}
	}
	return map[string]any{
		"total":      total,
		"problems":   bad,
		"buckets":    len(s.rows),
		"by_outcome": byOutcome,
	}
}

// Reset clears the aggregate (admin "start fresh" after shipping a fix).
func (s *Store) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = map[string]*Row{}
	s.dirty = true
	s.saveLocked()
}

// Flush persists pending changes; call on shutdown.
func (s *Store) Flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saveLocked()
}

func (s *Store) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var rows []Row
	if json.Unmarshal(data, &rows) != nil {
		return
	}
	for i := range rows {
		r := rows[i]
		if r.Devices == nil {
			r.Devices = map[string]int{}
		}
		s.rows[key(Report{
			Outcome: r.Outcome, Source: r.Source, AudioMime: r.AudioMime,
			VideoMime: r.VideoMime, AudioDecoder: r.AudioDecoder, Platform: r.Platform,
		})] = &r
	}
}

func (s *Store) saveLocked() {
	if !s.dirty {
		return
	}
	rows := make([]Row, 0, len(s.rows))
	for _, r := range s.rows {
		rows = append(rows, *r)
	}
	data, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, data, 0o644) == nil {
		_ = os.Rename(tmp, s.path)
	}
	s.dirty = false
	s.lastSave = time.Now()
}
