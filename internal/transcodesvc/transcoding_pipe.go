package transcodesvc

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"lampac-go/internal/transcode"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"lampac-go/internal/config"

	"github.com/rs/zerolog/log"
)

// ---------------------------------------------------------------------------
// In-memory fMP4 pipe transcoding (opt-in, gated by Transcoding.PipeEnable).
//
// Instead of letting ffmpeg's HLS muxer write seg_*.m4s files to disk and
// then serving them, this path runs
//
//	ffmpeg -i <src> -c:v copy -c:a aac -f mp4 -movflags +frag_keyframe+empty_moov+default_base_moof
//	       -min_frag_duration <segDur> pipe:1
//
// reads the fragmented-MP4 stream off stdout, splits it into CMAF segments in
// RAM (mp4Segmenter), and serves them straight to the client.  Three wins over
// the disk path, all matching the original-Lampac GStreamer prototype but on
// our existing stack:
//
//   - No disk: segments live in a small RAM ring; nothing to write/clean.
//   - Backpressure pacing: ffmpeg is throttled to ~produceAhead segments past
//     what the client has fetched; a paused client freezes the pipeline (the
//     ffmpeg stdout write blocks).  No -readrate, no pause-polling.
//   - Native A+V mux: ffmpeg's mp4 muxer is multi-stream, so video+audio land
//     in one segment series / one playlist — none of the GStreamer
//     single-stream-cmafmux / master-playlist / h264timestamper headaches.
//
// Seek reuses our battle-tested model: a forward jump (or rewind past the RAM
// ring) restarts ffmpeg with -ss + -copyts -start_at_zero so the fragment
// timestamps line up with the synthetic VOD playlist slot — the ffmpeg-native
// answer to the GStreamer flush-seek tfdt-reset problem.
//
// The disk-based path in transcoding_service.go is untouched; this is a
// sibling module selected by a separate set of /transcoding/pipe/* routes.
// ---------------------------------------------------------------------------

const (
	// pipeProduceAhead is how many segments ffmpeg may run past the client's
	// last fetched segment before backpressure freezes it.  This is the
	// appsink-max-buffers analogue.
	pipeProduceAhead = 3
	// pipeRetain is how many recently produced segments stay in the RAM ring
	// for re-fetch / short in-buffer rewind (players fetch a few ahead and
	// occasionally re-request).  Must be >= pipeProduceAhead.
	pipeRetain = 12
)

var (
	errPipeClosed = errors.New("pipe session closed")
	errPipeStale  = errors.New("pipe generation superseded")
)

type pipeRingSeg struct {
	idx  int
	data []byte
}

// pipeSession owns one ffmpeg process and the RAM segment ring for it.
type pipeSession struct {
	svc       *TranscodingService
	id        string
	streamID  string
	slotOwned bool
	sched     *transcodingScheduler // pool that owns this session's slot (main or capi/TV)

	source    string
	userAgent string
	referer   string
	audioIdx  int
	audioCopy bool
	segDur    int
	duration  int            // seconds; > 0 (probed up front)
	streams   map[string]any // compact audio/video summary for the plugin menu

	mu       sync.Mutex
	cond     *sync.Cond
	init     []byte
	ring     []*pipeRingSeg
	highest  int // highest produced index, -1 if none yet
	nextIdx  int // index to assign to the next produced fragment
	consumed int // highest index the client has fetched
	seekGen  int // bumped on every (re)start; stale generations are dropped
	closed   bool
	errMsg   string
	lastErr  string // last ffmpeg stderr line (for diagnostics)

	cmd       *exec.Cmd
	startedAt time.Time
	lastAcc   atomic.Value // time.Time

	stopOnce sync.Once
}

func (s *pipeSession) touch() { s.lastAcc.Store(time.Now()) }
func (s *pipeSession) lastAccess() time.Time {
	if v := s.lastAcc.Load(); v != nil {
		return v.(time.Time)
	}
	return s.startedAt
}

// buildArgs returns the ffmpeg argv for this session at the given seek point.
func (s *pipeSession) buildArgs(seekSec int) []string {
	a := []string{"-hide_banner", "-nostats", "-loglevel", "error"}
	if s.userAgent != "" {
		a = append(a, "-user_agent", s.userAgent)
	}
	if s.referer != "" {
		a = append(a, "-headers", fmt.Sprintf("Referer: %s\r\n", s.referer))
	}
	// Generate PTS where the demuxer left gaps; keeps the mp4 muxer happy.
	a = append(a, "-fflags", "+genpts")
	if seekSec > 0 {
		// Input seek (before -i) — fast keyframe seek over HTTP.
		a = append(a, "-ss", strconv.Itoa(seekSec), "-noaccurate_seek")
	}
	a = append(a, "-i", s.source)
	if seekSec > 0 {
		// Preserve original PTS so fragment tfdt matches the playlist slot
		// (the ffmpeg-native fix for the seek-jumps-to-start problem).
		a = append(a, "-copyts", "-start_at_zero")
		if s.svc.ffmpegMajorVersion > 0 && s.svc.ffmpegMajorVersion < 5 {
			a = append(a, "-avoid_negative_ts", "make_zero")
		}
	}
	a = append(a, "-map", "0:v:0", "-map", fmt.Sprintf("0:a:%d", s.audioIdx))
	a = append(a, "-c:v", "copy")
	if s.audioCopy {
		a = append(a, "-c:a", "copy")
	} else {
		a = append(a, "-c:a", "aac", "-ac", "2", "-b:a", "192k")
	}
	a = append(a, "-sn", "-dn", "-map_metadata", "-1", "-map_chapters", "-1")
	a = append(a, "-f", "mp4")
	a = append(a, "-movflags", "+frag_keyframe+empty_moov+default_base_moof")
	// min_frag_duration groups short GOPs up to ~segDur while still cutting
	// only at keyframes — so each fragment is an independent ~segDur segment.
	a = append(a, "-min_frag_duration", strconv.Itoa(s.segDur*1_000_000))
	a = append(a, "pipe:1")
	return a
}

// launch starts an ffmpeg process for the given seek point and segment base
// index.  Caller MUST hold s.mu.
func (s *pipeSession) launch(seekSec, baseIdx, gen int) string {
	ffmpeg := s.svc.cfg.Transcoding.FFmpeg
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	cmd := exec.Command(ffmpeg, s.buildArgs(seekSec)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "stdout pipe: " + err.Error()
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "stderr pipe: " + err.Error()
	}
	if err := cmd.Start(); err != nil {
		return "start ffmpeg: " + err.Error()
	}
	s.cmd = cmd
	s.nextIdx = baseIdx
	go s.pump(stdout, gen)
	go s.drainStderr(stderr, gen)
	go func() { _ = cmd.Wait() }() // reap the process
	return ""
}

// pump reads the fragmented-MP4 stream off ffmpeg stdout and feeds the
// segmenter.  Runs in its own goroutine; one per generation.
func (s *pipeSession) pump(stdout io.ReadCloser, gen int) {
	defer stdout.Close()
	seg := &mp4Segmenter{
		onInit: func(init []byte) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.closed {
				return errPipeClosed
			}
			if gen != s.seekGen {
				return errPipeStale
			}
			if s.init == nil {
				s.init = append([]byte(nil), init...)
				s.cond.Broadcast()
			}
			return nil
		},
		onSegment: func(data []byte) error {
			return s.addSegment(gen, data)
		},
	}
	_, _ = io.Copy(seg, stdout)

	// io.Copy returned: EOS (transcode finished), kill, or a callback abort.
	// Surface a hard failure (produced nothing) to anyone waiting.
	s.mu.Lock()
	if gen == s.seekGen && !s.closed && s.highest < 0 && s.init == nil {
		s.errMsg = "ffmpeg produced no output"
		if s.lastErr != "" {
			s.errMsg += ": " + s.lastErr
		}
		s.cond.Broadcast()
	}
	s.mu.Unlock()
}

// addSegment stores a freshly produced fragment, applying backpressure: it
// blocks (freezing ffmpeg via the stdout pipe) while the segment index would
// run more than pipeProduceAhead past what the client has fetched.
func (s *pipeSession) addSegment(gen int, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		if s.closed {
			return errPipeClosed
		}
		if gen != s.seekGen {
			return errPipeStale
		}
		idx := s.nextIdx
		if idx > s.consumed+pipeProduceAhead {
			s.cond.Wait()
			continue
		}
		buf := append([]byte(nil), data...)
		s.ring = append(s.ring, &pipeRingSeg{idx: idx, data: buf})
		if len(s.ring) > pipeRetain {
			s.ring = s.ring[len(s.ring)-pipeRetain:]
		}
		s.highest = idx
		s.nextIdx = idx + 1
		s.cond.Broadcast()
		return nil
	}
}

func (s *pipeSession) drainStderr(stderr io.ReadCloser, gen int) {
	sc := bufio.NewScanner(stderr)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		s.mu.Lock()
		if gen == s.seekGen {
			s.lastErr = line
		}
		s.mu.Unlock()
	}
}

// ringGet returns the segment bytes for idx, or nil. Caller holds s.mu.
func (s *pipeSession) ringGet(idx int) []byte {
	for _, r := range s.ring {
		if r.idx == idx {
			return r.data
		}
	}
	return nil
}

// ringLowest returns the lowest index currently retained, or -1 if empty.
// Caller holds s.mu.
func (s *pipeSession) ringLowest() int {
	if len(s.ring) == 0 {
		return -1
	}
	return s.ring[0].idx
}

// doSeekLocked restarts ffmpeg so production resumes at segment `idx`.
// Caller holds s.mu.
func (s *pipeSession) doSeekLocked(idx int) {
	s.seekGen++
	gen := s.seekGen
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	s.ring = nil
	s.highest = idx - 1
	s.consumed = idx - 1
	if err := s.launch(idx*s.segDur, idx, gen); err != "" {
		s.errMsg = err
	}
	s.cond.Broadcast()
}

// getInit waits for the init section (ftyp+moov) and returns it.
func (s *pipeSession) getInit(timeout time.Duration) ([]byte, string) {
	s.touch()
	s.mu.Lock()
	defer s.mu.Unlock()
	t := time.AfterFunc(timeout, func() { s.mu.Lock(); s.cond.Broadcast(); s.mu.Unlock() })
	defer t.Stop()
	deadline := time.Now().Add(timeout)
	for s.init == nil {
		if s.closed {
			return nil, "session closed"
		}
		if s.errMsg != "" {
			return nil, s.errMsg
		}
		if time.Now().After(deadline) {
			return nil, "timeout waiting for init segment"
		}
		s.cond.Wait()
	}
	return s.init, ""
}

// getSegment returns the bytes for segment idx, seeking (restarting ffmpeg) if
// the index is a forward gap or a rewind past the RAM ring, then waiting for
// production.
func (s *pipeSession) getSegment(idx int, timeout time.Duration) ([]byte, string) {
	s.touch()
	s.mu.Lock()
	defer s.mu.Unlock()

	if idx > s.consumed {
		s.consumed = idx
		s.cond.Broadcast() // unblock backpressure now that the client advanced
	}

	if d := s.ringGet(idx); d != nil {
		return d, ""
	}

	// Decide whether to seek. Only once at least one segment has been
	// produced — during cold start we just wait for sequential production.
	if s.highest >= 0 {
		forwardGap := idx > s.highest+pipeProduceAhead+1
		rewindPastRing := idx < s.ringLowest()
		if forwardGap || rewindPastRing {
			s.doSeekLocked(idx)
		}
	}

	t := time.AfterFunc(timeout, func() { s.mu.Lock(); s.cond.Broadcast(); s.mu.Unlock() })
	defer t.Stop()
	deadline := time.Now().Add(timeout)
	for {
		if d := s.ringGet(idx); d != nil {
			return d, ""
		}
		if s.closed {
			return nil, "session closed"
		}
		if s.errMsg != "" {
			return nil, s.errMsg
		}
		if time.Now().After(deadline) {
			return nil, "timeout waiting for segment"
		}
		s.cond.Wait()
	}
}

func (s *pipeSession) stop() {
	s.stopOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		if s.cmd != nil && s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		s.cond.Broadcast()
		s.mu.Unlock()
		if s.slotOwned {
			rel := s.sched
			if rel == nil {
				rel = s.svc.scheduler
			}
			if rel != nil {
				rel.Release()
			}
		}
	})
}

// status returns a small JSON-able snapshot for the /status endpoint.
func (s *pipeSession) status() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]any{
		"streamId":   s.streamID,
		"mode":       "pipe",
		"audioIdx":   s.audioIdx,
		"audioCopy":  s.audioCopy,
		"segDur":     s.segDur,
		"duration":   s.duration,
		"highest":    s.highest,
		"consumed":   s.consumed,
		"ringLow":    s.ringLowest(),
		"ringLen":    len(s.ring),
		"initReady":  s.init != nil,
		"seekGen":    s.seekGen,
		"uptimeSec":  int(time.Since(s.startedAt).Seconds()),
		"lastFFmpeg": s.lastErr,
		"error":      s.errMsg,
	}
}

// ---------------------------------------------------------------------------
// Manager
// ---------------------------------------------------------------------------

type transcodingPipeManager struct {
	svc      *TranscodingService
	cfg      config.Config
	mu       sync.Mutex
	sessions map[string]*pipeSession
	stopCh   chan struct{}
}

func newTranscodingPipeManager(svc *TranscodingService, cfg config.Config) *transcodingPipeManager {
	m := &transcodingPipeManager{
		svc:      svc,
		cfg:      cfg,
		sessions: make(map[string]*pipeSession),
		stopCh:   make(chan struct{}),
	}
	go m.reaper()
	return m
}

func (m *transcodingPipeManager) resolve(streamID string) (*pipeSession, bool) {
	id, ok := m.svc.parseToken(streamID)
	if !ok {
		return nil, false
	}
	m.mu.Lock()
	s, found := m.sessions[id]
	m.mu.Unlock()
	return s, found
}

// Start probes the source, spawns ffmpeg in pipe mode and registers the
// session.  Returns (session, "") on success or (nil, errMsg) on failure.
func (m *transcodingPipeManager) Start(req *TranscodingStartRequest) (*pipeSession, string) {
	tc := m.cfg.Transcoding
	if !tc.Enable || !tc.PipeEnable {
		return nil, "Pipe transcoding disabled"
	}
	if req == nil || strings.TrimSpace(req.Src) == "" {
		return nil, "Source URL is required"
	}
	srcURL, err := url.Parse(strings.TrimSpace(req.Src))
	if err != nil || (srcURL.Scheme != "http" && srcURL.Scheme != "https") {
		return nil, "Only http/https URLs are allowed"
	}
	if len(tc.AllowHosts) > 0 {
		allowed := false
		for _, h := range tc.AllowHosts {
			if strings.EqualFold(h, srcURL.Host) {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, "Source host is not allowed"
		}
	}

	// Scheduler slot — pipe sessions spawn ffmpeg, so they count against the
	// concurrency budget like disk jobs do. capi/TV requests draw from their
	// own pool (schedulerFor) when configured.
	sched := m.svc.schedulerFor(req)
	slotOwned := false
	if sched != nil {
		if err := sched.Acquire(); err != nil {
			return nil, err.Error()
		}
		slotOwned = true
	}
	releaseOnFail := func() {
		if slotOwned && sched != nil {
			sched.Release()
		}
	}

	probe, perr := m.svc.runFFProbe(req.Src, req.Headers)
	if perr != "" {
		releaseOnFail()
		return nil, "probe failed: " + perr
	}
	duration := ffprobeDuration(probe)
	if duration <= 0 {
		releaseOnFail()
		return nil, "could not determine source duration (pipe mode needs a seekable VOD source)"
	}

	lang := ""
	if req.Client != nil {
		lang = req.Client.Lang
	}
	audioIdx, audioCodec := pickPipeAudio(probe, lang)
	if req.Audio != nil && req.Audio.Index > 0 {
		audioIdx = req.Audio.Index
		audioCodec = "" // explicit index — re-encode to be safe
	}

	segDur := 6
	if tc.HLS.SegDur > 0 {
		segDur = tc.HLS.SegDur
	}
	if req.HLS != nil && req.HLS.SegDur > 0 {
		segDur = req.HLS.SegDur
	}

	id := newJobID()
	s := &pipeSession{
		svc:       m.svc,
		id:        id,
		streamID:  m.svc.buildToken(id),
		slotOwned: slotOwned,
		sched:     sched,
		source:    srcURL.String(),
		userAgent: sanitizeHeader(getHeader(req.Headers, "userAgent"), "Mozilla/5.0"),
		referer:   sanitizeHeader(getHeader(req.Headers, "referer"), ""),
		audioIdx:  audioIdx,
		audioCopy: audioCodec == "aac",
		segDur:    segDur,
		duration:  duration,
		streams:   transcode.SummarizeStreams(probe),
		highest:   -1,
		startedAt: time.Now(),
	}
	s.cond = sync.NewCond(&s.mu)
	s.touch()

	s.mu.Lock()
	launchErr := s.launch(0, 0, 0)
	s.mu.Unlock()
	if launchErr != "" {
		releaseOnFail()
		return nil, launchErr
	}

	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()

	log.Info().
		Str("streamId", s.streamID).
		Int("audioIdx", audioIdx).
		Bool("audioCopy", s.audioCopy).
		Int("segDur", segDur).
		Int("duration", duration).
		Msg("transcoding: pipe session started")
	return s, ""
}

// StartEligible decides whether a /transcoding/start request is a good fit for
// the pipe path and, if so, spawns a pipe session.  Returns handled=true only
// when a session was started — otherwise the caller falls back to the disk
// path.  Pipe mode is taken for plain-HTTP remux / audio-only sources (video
// stream-copy, audio copy-or-AAC); everything else (native/direct short-
// circuits, video re-encode, torrent stdin, live, subtitles) stays on disk.
func (m *transcodingPipeManager) StartEligible(req *TranscodingStartRequest) (s *pipeSession, handled bool, errMsg string) {
	tc := m.cfg.Transcoding
	if !tc.Enable || !tc.PipeEnable || req == nil {
		return nil, false, ""
	}
	src := strings.TrimSpace(req.Src)
	if src == "" || req.Live {
		return nil, false, ""
	}
	// Subtitles (burn-in / WebVTT extract) are disk-path only.
	subsWanted := tc.DefaultSubs
	if req.Subtitles != nil {
		subsWanted = *req.Subtitles
	}
	if subsWanted {
		return nil, false, ""
	}
	// Torrent / pidtor / in-process TorrServer sources feed ffmpeg via stdin
	// on the disk path; pipe mode needs a seekable plain-HTTP input.
	if strings.Contains(src, "/lite/pidtor/") ||
		strings.Contains(src, "/ts/") ||
		(strings.Contains(src, "/stream") && strings.Contains(src, "link=")) {
		return nil, false, ""
	}

	// Probe (cache-hit shared with the disk path) and gate on the chosen mode.
	probe, perr := m.svc.runFFProbe(src, req.Headers)
	if perr != "" || probe == nil || ffprobeDuration(probe) <= 0 {
		return nil, false, ""
	}
	caps, _, _ := transcode.EnrichCapsFromUA(req.Client, getHeader(req.Headers, "userAgent"))
	decision := transcode.SelectMode(probe, caps, !tc.DisableNativePlayback)
	if decision.Mode != transcode.ModeRemux && decision.Mode != transcode.ModeAudioOnly {
		// native/direct → disk short-circuits to the original URL;
		// sw/hw transcode → disk (pipe mode copies video, can't re-encode).
		return nil, false, ""
	}

	sess, em := m.Start(req)
	if sess == nil {
		// Pipe start failed — fall back to the disk path rather than erroring.
		return nil, false, em
	}
	return sess, true, ""
}

func (m *transcodingPipeManager) reaper() {
	idleSec := m.cfg.Transcoding.IdleTimeoutSec
	if idleSec < 180 {
		idleSec = 180
	}
	idle := time.Duration(idleSec) * time.Second
	tk := time.NewTicker(5 * time.Second)
	defer tk.Stop()
	for {
		select {
		case <-m.stopCh:
			return
		case <-tk.C:
			now := time.Now()
			var dead []*pipeSession
			m.mu.Lock()
			for id, s := range m.sessions {
				if now.Sub(s.lastAccess()) > idle {
					dead = append(dead, s)
					delete(m.sessions, id)
				}
			}
			m.mu.Unlock()
			for _, s := range dead {
				log.Info().Str("streamId", s.streamID).Msg("transcoding: pipe session idle-reaped")
				s.stop()
			}
		}
	}
}

func (m *transcodingPipeManager) stopSession(streamID string) bool {
	id, ok := m.svc.parseToken(streamID)
	if !ok {
		return false
	}
	m.mu.Lock()
	s, found := m.sessions[id]
	if found {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	if found {
		s.stop()
	}
	return found
}

func (m *transcodingPipeManager) StopAll() {
	select {
	case m.stopCh <- struct{}{}:
	default:
	}
	m.mu.Lock()
	sessions := make([]*pipeSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.sessions = make(map[string]*pipeSession)
	m.mu.Unlock()
	for _, s := range sessions {
		s.stop()
	}
}

// pickPipeAudio chooses an audio stream relative-index by language → default →
// first, mirroring selectMode()'s policy, and returns its codec name.
func pickPipeAudio(probe map[string]any, lang string) (relIdx int, codec string) {
	streams, _ := probe["streams"].([]any)
	type ai struct {
		rel   int
		codec string
		lang  string
		def   bool
	}
	var audios []ai
	rel := 0
	for _, st := range streams {
		sm, _ := st.(map[string]any)
		if sm == nil || fmt.Sprint(sm["codec_type"]) != "audio" {
			continue
		}
		a := ai{rel: rel, codec: strings.ToLower(fmt.Sprint(sm["codec_name"]))}
		if tags, ok := sm["tags"].(map[string]any); ok {
			if l, ok := tags["language"].(string); ok {
				a.lang = strings.ToLower(l)
			}
		}
		if disp, ok := sm["disposition"].(map[string]any); ok {
			if d, ok := disp["default"].(float64); ok && d > 0 {
				a.def = true
			}
		}
		audios = append(audios, a)
		rel++
	}
	if len(audios) == 0 {
		return 0, ""
	}
	for _, w := range transcode.NormalizeLangCodes(lang) {
		for _, a := range audios {
			if a.lang == w {
				return a.rel, a.codec
			}
		}
	}
	for _, a := range audios {
		if a.def {
			return a.rel, a.codec
		}
	}
	return audios[0].rel, audios[0].codec
}
