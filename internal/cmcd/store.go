package cmcd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// What the aggregate is for: comparing SOURCES, not users. Two balancers can
// both answer 200 to every probe while one of them starves a third of its
// viewers — that difference only exists in the player, and this is where it
// lands. So everything is keyed by (plugin, platform) and nothing is keyed by
// anything that identifies a person.
//
// Sessions are counted on eviction rather than on first sight: "how many
// sessions rebuffered" is only knowable once the session is over, and counting
// on arrival would inflate every number by the length of the session.

const (
	// A session idle this long is over. Segment requests come every few seconds
	// while playing; a two-minute gap means paused-and-abandoned or finished.
	sessionIdle = 2 * time.Minute
	// Hard caps: a public endpoint must not let anyone grow our heap by minting
	// session ids. Oldest sessions fold into the aggregate early when hit.
	maxSessions = 8000
	maxBuckets  = 600
	// Below this the player is one hiccup away from stalling.
	lowBufferMS = 4000

	saveInterval  = 30 * time.Second
	sweepInterval = 20 * time.Second
)

type sessionState struct {
	plugin   string
	platform string

	firstSeen time.Time
	lastSeen  time.Time

	requests    int
	starvations int
	lowBuffer   int
	lowQuality  int

	minBufferMS int // -1 until the first bl arrives
	sumMTP      int
	nMTP        int
	sumBR       int
	nBR         int
	topBitrate  int
	msd         int

	format string
	stype  string
}

// Bucket is the persisted aggregate for one (plugin, platform) pair. Sums are
// stored rather than averages so that merging a new session never needs the old
// sample count to be re-derived.
type Bucket struct {
	Plugin   string `json:"plugin"`
	Platform string `json:"platform"`

	Sessions   int `json:"sessions"`
	Rebuffered int `json:"rebuffered"` // sessions with at least one starvation
	Rebuffers  int `json:"rebuffers"`  // starvation events in total
	Requests   int `json:"requests"`

	LowBufferReq  int `json:"low_buffer_req"`
	LowQualityReq int `json:"low_quality_req"`

	SumMTP int `json:"sum_mtp"`
	NMTP   int `json:"n_mtp"`
	SumBR  int `json:"sum_br"`
	NBR    int `json:"n_br"`
	SumMSD int `json:"sum_msd"`
	NMSD   int `json:"n_msd"`

	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// Row is the readable view: averages computed, sums hidden.
type Row struct {
	Plugin   string `json:"plugin"`
	Platform string `json:"platform"`

	Sessions   int `json:"sessions"`
	Rebuffered int `json:"rebuffered"`
	Rebuffers  int `json:"rebuffers"`
	Requests   int `json:"requests"`

	// RebufferRate is the headline number: share of sessions that stalled.
	RebufferRate float64 `json:"rebuffer_rate"`
	// LowBufferRate — share of requests made with a dangerously short buffer.
	LowBufferRate float64 `json:"low_buffer_rate"`
	// LowQualityRate — share of requests spent well below the top rung, i.e.
	// how often the ladder collapsed on this source.
	LowQualityRate float64 `json:"low_quality_rate"`

	AvgThroughputKbps int `json:"avg_throughput_kbps"`
	AvgBitrateKbps    int `json:"avg_bitrate_kbps"`
	AvgStartupMS      int `json:"avg_startup_ms"`

	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// LiveSession is one session still in flight — the "what is happening right
// now" view an operator wants while a user is complaining in Telegram.
type LiveSession struct {
	Session  string `json:"session"`
	Plugin   string `json:"plugin"`
	Platform string `json:"platform"`

	AgeSec      int `json:"age_sec"`
	Requests    int `json:"requests"`
	Starvations int `json:"starvations"`

	MinBufferMS    int `json:"min_buffer_ms"`
	ThroughputKbps int `json:"throughput_kbps"`
	BitrateKbps    int `json:"bitrate_kbps"`
	TopBitrateKbps int `json:"top_bitrate_kbps"`

	Format     string `json:"format,omitempty"`
	StreamType string `json:"stream_type,omitempty"`
}

// Store keeps in-flight sessions in memory and folds finished ones into a
// persisted aggregate.
type Store struct {
	mu       sync.Mutex
	sessions map[string]*sessionState
	buckets  map[string]*Bucket

	path      string
	dirty     bool
	lastSave  time.Time
	lastSweep time.Time
}

func New(repoRoot string) *Store {
	dir := filepath.Join(repoRoot, "database")
	_ = os.MkdirAll(dir, 0o755)
	s := &Store{
		sessions: map[string]*sessionState{},
		buckets:  map[string]*Bucket{},
		path:     filepath.Join(dir, "cmcd_stats.json"),
	}
	s.load()
	return s
}

// PlatformFromUA buckets a user agent into the handful of client families we
// actually ship. Coarse on purpose: the question is "does this source stall on
// TVs", not "which Chrome build".
func PlatformFromUA(ua string) string {
	l := strings.ToLower(ua)
	switch {
	case l == "":
		return "unknown"
	case strings.Contains(l, "exoplayer") || strings.Contains(l, "androidtv") || strings.Contains(l, "android"):
		return "android"
	case strings.Contains(l, "applecoremedia") || strings.Contains(l, "appletv") || strings.Contains(l, "tvos") ||
		strings.Contains(l, "iphone") || strings.Contains(l, "ipad"):
		return "apple"
	case strings.Contains(l, "web0s") || strings.Contains(l, "webos"):
		return "webos"
	case strings.Contains(l, "tizen"):
		return "tizen"
	case strings.Contains(l, "vidaa") || strings.Contains(l, "hisense"):
		return "vidaa"
	case strings.Contains(l, "mozilla"):
		return "web"
	default:
		return "other"
	}
}

func normKey(s string, max int) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "-"
	}
	if len(s) > max {
		s = s[:max]
	}
	return s
}

// Add records one CMCD-carrying request. Called from the proxy hot path: it
// takes one lock, touches two maps and returns — no IO, no allocation beyond a
// new session's state.
func (s *Store) Add(plugin, ua string, rep Report) {
	if !rep.Any {
		return
	}
	sid := rep.SessionID
	if sid == "" {
		// Without a session id there is nothing to aggregate over time; count
		// it as a one-request session keyed by content so it is not lost.
		sid = "anon:" + rep.ContentID
	}
	plugin = normKey(plugin, 24)
	platform := PlatformFromUA(ua)
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	st := s.sessions[sid]
	if st == nil {
		if len(s.sessions) >= maxSessions {
			s.foldOldestLocked(now, maxSessions/8)
		}
		st = &sessionState{
			plugin: plugin, platform: platform,
			firstSeen: now, minBufferMS: -1,
		}
		s.sessions[sid] = st
	}
	// The plugin can legitimately change mid-session (a failover to another
	// balancer inside one playback); the last one seen is the one that owns the
	// stalls that follow.
	if plugin != "-" {
		st.plugin = plugin
	}
	st.lastSeen = now
	st.requests++

	if rep.Starvation {
		st.starvations++
	}
	if rep.BufferLengthMS > 0 {
		if st.minBufferMS < 0 || rep.BufferLengthMS < st.minBufferMS {
			st.minBufferMS = rep.BufferLengthMS
		}
		// Manifest requests are made with whatever buffer happens to exist and
		// say nothing about starvation risk; only media requests count.
		if rep.BufferLengthMS < lowBufferMS && rep.ObjectType != ObjManifest && rep.ObjectType != "" {
			st.lowBuffer++
		}
	}
	if rep.ThroughputKbps > 0 {
		st.sumMTP += rep.ThroughputKbps
		st.nMTP++
	}
	if rep.EncodedBitrateKbps > 0 {
		st.sumBR += rep.EncodedBitrateKbps
		st.nBR++
	}
	if rep.TopBitrateKbps > st.topBitrate {
		st.topBitrate = rep.TopBitrateKbps
	}
	if rep.EncodedBitrateKbps > 0 && rep.TopBitrateKbps > 0 && rep.EncodedBitrateKbps*2 < rep.TopBitrateKbps {
		st.lowQuality++
	}
	if rep.MediaStartDelayMS > 0 && st.msd == 0 {
		st.msd = rep.MediaStartDelayMS
	}
	if rep.StreamingFormat != "" {
		st.format = rep.StreamingFormat
	}
	if rep.StreamType != "" {
		st.stype = rep.StreamType
	}

	if now.Sub(s.lastSweep) > sweepInterval {
		s.sweepLocked(now)
		s.lastSweep = now
	}
	if s.dirty && now.Sub(s.lastSave) > saveInterval {
		s.saveLocked()
	}
}

// sweepLocked folds every session that has gone quiet into the aggregate.
func (s *Store) sweepLocked(now time.Time) {
	for sid, st := range s.sessions {
		if now.Sub(st.lastSeen) > sessionIdle {
			s.foldLocked(st)
			delete(s.sessions, sid)
		}
	}
}

// foldOldestLocked is the pressure valve for the session cap: fold the n least
// recently used sessions early rather than refuse to record new ones.
func (s *Store) foldOldestLocked(now time.Time, n int) {
	type ent struct {
		sid  string
		seen time.Time
	}
	all := make([]ent, 0, len(s.sessions))
	for sid, st := range s.sessions {
		all = append(all, ent{sid, st.lastSeen})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].seen.Before(all[j].seen) })
	if n > len(all) {
		n = len(all)
	}
	for _, e := range all[:n] {
		s.foldLocked(s.sessions[e.sid])
		delete(s.sessions, e.sid)
	}
}

func (s *Store) foldLocked(st *sessionState) {
	if st == nil || st.requests == 0 {
		return
	}
	k := st.plugin + "|" + st.platform
	b := s.buckets[k]
	if b == nil {
		if len(s.buckets) >= maxBuckets {
			return
		}
		b = &Bucket{Plugin: st.plugin, Platform: st.platform, FirstSeen: st.firstSeen.UTC()}
		s.buckets[k] = b
	}
	b.Sessions++
	b.Requests += st.requests
	b.Rebuffers += st.starvations
	if st.starvations > 0 {
		b.Rebuffered++
	}
	b.LowBufferReq += st.lowBuffer
	b.LowQualityReq += st.lowQuality
	b.SumMTP += st.sumMTP
	b.NMTP += st.nMTP
	b.SumBR += st.sumBR
	b.NBR += st.nBR
	if st.msd > 0 {
		b.SumMSD += st.msd
		b.NMSD++
	}
	b.LastSeen = st.lastSeen.UTC()
	s.dirty = true
}

func rate(n, total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(n) / float64(total)
}

func avg(sum, n int) int {
	if n <= 0 {
		return 0
	}
	return sum / n
}

// Rows returns the aggregate, worst sources first — "worst" being the rebuffer
// rate, with volume as the tie-break so a single unlucky session cannot top the
// table.
func (s *Store) Rows(limit int) []Row {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Row, 0, len(s.buckets))
	for _, b := range s.buckets {
		out = append(out, Row{
			Plugin: b.Plugin, Platform: b.Platform,
			Sessions: b.Sessions, Rebuffered: b.Rebuffered,
			Rebuffers: b.Rebuffers, Requests: b.Requests,
			RebufferRate:      rate(b.Rebuffered, b.Sessions),
			LowBufferRate:     rate(b.LowBufferReq, b.Requests),
			LowQualityRate:    rate(b.LowQualityReq, b.Requests),
			AvgThroughputKbps: avg(b.SumMTP, b.NMTP),
			AvgBitrateKbps:    avg(b.SumBR, b.NBR),
			AvgStartupMS:      avg(b.SumMSD, b.NMSD),
			FirstSeen:         b.FirstSeen, LastSeen: b.LastSeen,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RebufferRate != out[j].RebufferRate {
			return out[i].RebufferRate > out[j].RebufferRate
		}
		return out[i].Sessions > out[j].Sessions
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Live lists sessions still in flight, the ones in trouble first.
func (s *Store) Live(limit int) []LiveSession {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	out := make([]LiveSession, 0, len(s.sessions))
	for sid, st := range s.sessions {
		if now.Sub(st.lastSeen) > sessionIdle {
			continue
		}
		minBuf := st.minBufferMS
		if minBuf < 0 {
			minBuf = 0
		}
		out = append(out, LiveSession{
			Session: clampStr(sid, 16), Plugin: st.plugin, Platform: st.platform,
			AgeSec: int(now.Sub(st.firstSeen).Seconds()), Requests: st.requests,
			Starvations: st.starvations, MinBufferMS: minBuf,
			ThroughputKbps: avg(st.sumMTP, st.nMTP), BitrateKbps: avg(st.sumBR, st.nBR),
			TopBitrateKbps: st.topBitrate, Format: st.format, StreamType: st.stype,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Starvations != out[j].Starvations {
			return out[i].Starvations > out[j].Starvations
		}
		return out[i].AgeSec > out[j].AgeSec
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Summary is the headline pair of numbers: how much playback we observed and
// what share of it stalled.
func (s *Store) Summary() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()

	sessions, rebuffered, requests := 0, 0, 0
	for _, b := range s.buckets {
		sessions += b.Sessions
		rebuffered += b.Rebuffered
		requests += b.Requests
	}
	live := 0
	now := time.Now()
	for _, st := range s.sessions {
		if now.Sub(st.lastSeen) <= sessionIdle {
			live++
		}
	}
	return map[string]any{
		"sessions":      sessions,
		"rebuffered":    rebuffered,
		"rebuffer_rate": rate(rebuffered, sessions),
		"requests":      requests,
		"live_sessions": live,
		"buckets":       len(s.buckets),
	}
}

// Reset clears the aggregate — the admin "start fresh after shipping a fix"
// button. In-flight sessions are dropped too, so the next numbers are clean.
func (s *Store) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buckets = map[string]*Bucket{}
	s.sessions = map[string]*sessionState{}
	s.dirty = true
	s.saveLocked()
}

// Flush folds every in-flight session and persists; call on shutdown so a
// restart does not throw away everything watched since the last save.
func (s *Store) Flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sid, st := range s.sessions {
		s.foldLocked(st)
		delete(s.sessions, sid)
	}
	s.saveLocked()
}

func (s *Store) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var buckets []Bucket
	if json.Unmarshal(data, &buckets) != nil {
		return
	}
	for i := range buckets {
		b := buckets[i]
		s.buckets[b.Plugin+"|"+b.Platform] = &b
	}
}

func (s *Store) saveLocked() {
	if !s.dirty {
		return
	}
	rows := make([]Bucket, 0, len(s.buckets))
	for _, b := range s.buckets {
		rows = append(rows, *b)
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
