package transcodesvc

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"lampac-go/internal/transcode"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"lampac-go/internal/config"

	"github.com/rs/zerolog/log"
	"golang.org/x/sync/singleflight"
)

// ---------------------------------------------------------------------------
// Job state
// ---------------------------------------------------------------------------

const (
	JobStateRunning = "running"
	JobStateIdle    = "idle"
	JobStateStopped = "stopped"
)

// TranscodingJob represents a single FFmpeg transcoding job.
type TranscodingJob struct {
	ID        string
	StreamID  string
	OutputDir string
	Cmd       *exec.Cmd
	Context   transcodingContext

	// Smart mode metadata — filled in during Start().
	// Mode == ModeNative means no ffmpeg was spawned; the handler should
	// return OriginalURL back to the client untouched.
	Mode          transcode.TranscodingMode
	OriginalURL   string
	Warning       string         // set when probe failed and we went best-effort
	Streams       map[string]any // compact streams summary for the plugin (nil if probe failed)
	SelectedAudio int            // 0-based rel-index of the picked audio stream

	// UA-DB enrichment results (P3.B). ProfileLabel is the device family
	// detected from User-Agent ("tizen-5", "apple-tv-15", "browser-chrome",
	// …) and surfaced in /transcoding/start so the plugin/admin panel can
	// explain why smart-mode picked what it picked.  KnownIssues lists
	// platform-specific caveats relevant to the chosen pipeline.
	ProfileLabel string
	KnownIssues  []string

	// sched is the concurrency pool that owns this job's slot (main or the
	// optional capi/TV pool). cleanup() releases back to it. nil → main pool.
	sched *transcodingScheduler

	StartedUtc   time.Time
	lastAccess   atomic.Value // time.Time
	lastSegIndex int64        // atomic
	exitCode     int32        // atomic; -1 = still running
	cancelOnce   sync.Once
	cancelCh     chan struct{}
	stderrPipe   io.ReadCloser

	// P2.4: auto-restart-on-crash bookkeeping.
	// stopRequested flips to 1 when cleanup/stopJob runs intentionally,
	// so waitExit knows the exit wasn't a crash and skips restart.
	// The restart budget itself lives on the service keyed by streamID,
	// so it survives SeekAsync (which replaces the underlying job struct).
	stopRequested int32

	// P4: windowPaused is true while the producer is SIGSTOP-frozen because
	// it ran too far ahead of the viewer (see windowWatchdog). Used so
	// killProcess SIGCONT-resumes before SIGKILL, and to track the toggle.
	windowPaused atomic.Bool

	// P2.3: per-client playback positions (ip+ua hash → seconds).
	// segmentCleanupLoop uses min(positions) - safetyWindow to avoid
	// nuking segments that a slower client hasn't played yet.
	posMu     sync.Mutex
	positions map[string]float64

	// dedupKey ties this job to the per-(src+client+audio) coalescing entry
	// in TranscodingService.inflight so cleanup() can drop it. Empty when the
	// job isn't deduplicated (native/direct short-circuits).
	dedupKey string

	logMu  sync.Mutex
	logBuf []string
	maxLog int

	// Карта ключевых кадров (keyframes.go): строится в фоне после старта, kfDone закрывается по
	// завершении (успех или нет). Плейлист и seek ждут её недолго и берут точные границы, пока
	// карта есть, и прежнюю равномерную сетку, пока нет. Рестарты (seek/respawn) делят её.
	kfMu   sync.Mutex
	kf     *keyframeMap
	kfSegs *hlsSegmentMap
	kfDone chan struct{}
	kfOnce *sync.Once
}

type transcodingContext struct {
	Source    string
	UserAgent string
	Referer   string
	HLS       transcodingHLSCtx
	Audio     transcodingAudioCtx
	Live      bool
	Subtitles bool
	OutputDir string
	MaxHeight int // per-job downscale cap (0 = fall back to cfg.Transcoding.MaxTranscodeHeight)
	StartNum  *int
	FFProbe   map[string]any // parsed ffprobe JSON

	// SrcIsTS marks a TorrServer input (/ts/stream rewrite or pidtor). Cold
	// torrents legitimately take >30s to produce the first bytes (metadata +
	// buffering on a slow swarm), so createProcess widens -rw_timeout for them.
	SrcIsTS bool

	// Mode chosen by selectMode().  Controls whether video/audio streams are
	// copied, audio-only transcoded, or fully re-encoded.  Zero value is
	// legacy behavior (probe-driven decisions inside appendVideoCodec).
	Mode transcode.TranscodingMode

	// BestEffort is true when probe failed and we're running blind.  The
	// VOD playlist handler switches to reading ffmpeg's own index.m3u8
	// (growing event playlist) instead of generating one from probe duration.
	BestEffort bool

	// SubPlan is the per-session subtitle decision (P3.C).  When the
	// strategy is StrategyBurnIn, createProcess emits a -filter_complex
	// chain that renders the chosen subtitle stream into the video frame
	// — necessary for PGS / DVD / DVB bitmap subs and for ASS on profiles
	// that strip styling (Tizen ≤ 5, WebOS ≤ 3, unknown clients).
	// Zero value (StrategyNone) means "do nothing extra" — falls back to
	// the legacy WebVTT-extract loop driven by Subtitles=true.
	SubPlan SubtitlePlan

	// DisableHW is the per-session HW-encoder opt-out flag set by the
	// auto-escalation cascade (P3.E) when a previous run crashed in the
	// HW path.  appendVideoCodec checks this before consulting
	// svc.hwAccel.Active() so a single bad NVENC/VAAPI surface alloc
	// doesn't poison the global state for unrelated jobs.
	DisableHW bool

	// MultiRung enables ABR Phase 2 (P3.K2) multi-output ffmpeg.  When
	// true, createProcess routes through buildMultiRungArgs which emits
	// one HLS variant per entry in Ladder, sharing the audio/subtitle
	// pipelines.  Output files land in v<N>/ subdirs of OutputDir; the
	// segment route handler `/transcoding/{id}/v{N}/{file}` serves them.
	//
	// Only enabled when smart-mode picked sw-transcode (Phase 2 is SW
	// only — HW multi-session lands separately), the probe succeeded
	// (not best-effort), subtitles aren't burn-in (filter graph already
	// owns the video output), and the ladder has more than one rung.
	MultiRung bool
	Ladder    []transcode.ABRRung

	// Multi-audio HLS (hls4): shelvable audio tracks copied to per-track
	// shelves by the main job; lazily AAC-transcoded per rendition on demand.
	// Empty = feature off for this job. See transcoding_multiaudio.go.
	ShelfTracks    []shelfAudioTrack
	ActiveAudioAbs int // absolute index of the track muxed into the video variant

	// Torrent pipe: feed data directly to ffmpeg stdin (Source = "pipe:0").
	// Bypasses HTTP entirely — avoids auth/routing issues with localhost.
	StdinPipe      io.ReadCloser // torrent reader → ffmpeg stdin
	torrentHash    string        // info-hash (for re-opening pipe on seek)
	torrentFileIdx int           // 0-based file index
}

type transcodingHLSCtx struct {
	Seek    int
	SegDur  int
	WinSize int
	FMP4    bool
	// SeekExact — точное время -ss в секундах (ключевой кадр из карты); 0 = взять Seek.
	// Seek остаётся номинальным целым: по нему живут dedup, start_number и старая логика.
	SeekExact float64
}

// seekArg печатает значение для -ss. Входной seek (-ss до -i) с -noaccurate_seek приземляется
// на ключевой кадр НЕ ПОЗЖЕ цели, поэтому к точному времени добавляем 50 мс — иначе округление
// вниз уводит на предыдущий кадр. Выходной seek (pipe:0, -ss после -i) отбрасывает пакеты с
// pts < цели, там нужна ровно метка ключевого кадра, чтобы он не выпал сам.
func (c transcodingContext) seekArg(outputSeek bool) string {
	if c.HLS.SeekExact > 0 {
		if outputSeek {
			return fmtSeek(c.HLS.SeekExact)
		}
		return fmtSeek(c.HLS.SeekExact + 0.05)
	}
	return strconv.Itoa(c.HLS.Seek)
}

// videoCopied — видео идёт stream-copy (remux / только звук): сегменты режутся по ключевым
// кадрам источника, и только тут карта определяет плейлист.
func videoCopied(mode transcode.TranscodingMode) bool {
	return mode == transcode.ModeRemux || mode == transcode.ModeAudioOnly
}

type transcodingAudioCtx struct {
	Index       int
	BitrateKbps int
	Stereo      bool
	CodecCopy   []string
}

func newTranscodingJob(id, streamID, outputDir string, cmd *exec.Cmd, ctx transcodingContext) *TranscodingJob {
	j := &TranscodingJob{
		ID:         id,
		StreamID:   streamID,
		OutputDir:  outputDir,
		Cmd:        cmd,
		Context:    ctx,
		StartedUtc: time.Now().UTC(),
		exitCode:   -1,
		cancelCh:   make(chan struct{}),
		maxLog:     200,
		kfDone:     make(chan struct{}),
		kfOnce:     &sync.Once{},
	}
	j.lastAccess.Store(j.StartedUtc)
	atomic.StoreInt64(&j.lastSegIndex, -1)
	return j
}

func (j *TranscodingJob) Touch()                { j.lastAccess.Store(time.Now().UTC()) }
func (j *TranscodingJob) LastAccess() time.Time { return j.lastAccess.Load().(time.Time) }
func (j *TranscodingJob) LastSegmentIndex() int { return int(atomic.LoadInt64(&j.lastSegIndex)) }
func (j *TranscodingJob) ExitCode() int         { return int(atomic.LoadInt32(&j.exitCode)) }

// UpdatePosition records the playback position (seconds) reported by a
// given client fingerprint.  Used by segmentCleanupLoop to avoid trimming
// segments the slowest client still needs (P2.3).
func (j *TranscodingJob) UpdatePosition(clientID string, seconds float64) {
	if clientID == "" || seconds < 0 {
		return
	}
	j.posMu.Lock()
	if j.positions == nil {
		j.positions = make(map[string]float64, 2)
	}
	// Positions only advance — a client jumping backwards in the same
	// session keeps its high-water mark so we don't pull segments out
	// from under a buffered rewind.
	if cur, ok := j.positions[clientID]; !ok || seconds > cur {
		j.positions[clientID] = seconds
	}
	j.posMu.Unlock()
}

// MinPosition returns the slowest reported playback position across all
// active clients.  Returns (0, false) when no heartbeats have arrived yet
// — callers should fall back to the default cleanup policy in that case.
func (j *TranscodingJob) MinPosition() (float64, bool) {
	j.posMu.Lock()
	defer j.posMu.Unlock()
	if len(j.positions) == 0 {
		return 0, false
	}
	minPos := math.Inf(1)
	for _, p := range j.positions {
		if p < minPos {
			minPos = p
		}
	}
	return minPos, true
}

// MaxPosition returns the FURTHEST client playback position — used by the
// multi-audio rendition warm-up to guess where the (usually single) viewer is.
func (j *TranscodingJob) MaxPosition() (float64, bool) {
	j.posMu.Lock()
	defer j.posMu.Unlock()
	if len(j.positions) == 0 {
		return 0, false
	}
	maxPos := 0.0
	for _, p := range j.positions {
		if p > maxPos {
			maxPos = p
		}
	}
	return maxPos, true
}

func (j *TranscodingJob) UpdateLastSegmentIndex(idx int) {
	if idx < 0 {
		return
	}
	for {
		cur := atomic.LoadInt64(&j.lastSegIndex)
		if int64(idx) <= cur {
			return
		}
		if atomic.CompareAndSwapInt64(&j.lastSegIndex, cur, int64(idx)) {
			return
		}
	}
}

func (j *TranscodingJob) AppendLog(line string) {
	if strings.TrimSpace(line) == "" {
		return
	}
	j.logMu.Lock()
	defer j.logMu.Unlock()
	for part := range strings.SplitSeq(line, "\n") {
		part = strings.TrimRight(part, "\r")
		if strings.TrimSpace(part) == "" {
			continue
		}
		if len(part) > 2000 {
			part = part[:2000]
		}
		j.logBuf = append(j.logBuf, part)
		if len(j.logBuf) > j.maxLog {
			j.logBuf = j.logBuf[1:]
		}
	}
}

func (j *TranscodingJob) SnapshotLog() []string {
	j.logMu.Lock()
	defer j.logMu.Unlock()
	cp := make([]string, len(j.logBuf))
	copy(cp, j.logBuf)
	return cp
}

func (j *TranscodingJob) Cancel() {
	j.cancelOnce.Do(func() { close(j.cancelCh) })
}

func (j *TranscodingJob) Cancelled() <-chan struct{} { return j.cancelCh }

func (j *TranscodingJob) HasExited() bool {
	if j.Cmd == nil || j.Cmd.Process == nil {
		return true
	}
	return j.Cmd.ProcessState != nil
}

func (j *TranscodingJob) State() string {
	if j.HasExited() {
		return JobStateStopped
	}
	return JobStateRunning
}

// ---------------------------------------------------------------------------
// TranscodingService — singleton that manages all transcoding jobs.
// ---------------------------------------------------------------------------

type TranscodingService struct {
	cfg     config.Config
	hmacKey []byte

	mu   sync.RWMutex
	jobs map[string]*TranscodingJob // keyed by job ID (not streamID)

	safeFileRe    *regexp.Regexp
	segmentFileRe *regexp.Regexp

	ffmpegMajorVersion int // 0 = unknown, 7 = ffmpeg 7.x, etc.

	// P1: hardware acceleration — detected once at start-up.
	hwAccel *HWAccelInfo

	// P1: probe cache — hit on repeat clicks of the same source.
	probeCache *probeCache

	// P3.M: tesseract availability + OCR cache.  Populated at startup;
	// when tesseract is missing the OCR planner path silently no-ops and
	// bitmap subs continue to flow through the burn-in pipeline (P3.C).
	tesseract *TesseractInfo
	ocrCache  *OCRCache

	// P2.1: concurrency + disk budget scheduler.
	scheduler *transcodingScheduler

	// schedulerCapi is an OPTIONAL second concurrency pool dedicated to
	// capi / ALPAC-TV playback (max_concurrent_capi > 0). nil → capi shares
	// the main scheduler. Disk-budget enforcement is owned by the main
	// scheduler alone (it sweeps the whole temp root), so this one has none.
	schedulerCapi *transcodingScheduler

	// P2.4: auto-restart budget keyed by streamID.  Survives SeekAsync
	// because streamID is the stable identifier while *TranscodingJob
	// gets replaced on every restart.
	restartMu    sync.Mutex
	restartTimes map[string][]time.Time

	// Зомби-ffmpeg, замеченные прошлым проходом сторожа сирот (transcoding_orphans_unix.go).
	// Трогает только горутина deadJobReaper — без замка.
	orphanZombies map[int]struct{}

	// P4: job-coalescing.  Keyed by (src+client-fingerprint+audio+subs+seek);
	// value is the jobID of a still-warming-up job.  A retry from the same
	// client for the same stream reuses that job instead of spawning another
	// ffmpeg — kills the "12 ffmpeg for one stuck 4K stream" storm.
	inflightMu sync.Mutex
	inflight   map[string]string

	// probeFlight coalesces concurrent ffprobes of the same src: a player
	// retrying /transcoding/start while the first probe is still waiting on a
	// cold torrent (up to ~45s for TorrServer inputs) must NOT spawn its own
	// ffprobe + magnet add per retry — prod 2026-08-23: one TV issued 8 starts
	// in a minute, each adding the torrent again and probing the swarm again.
	probeFlight singleflight.Group
	// startFlight coalesces identical concurrent GET /transcoding/start.m3u8
	// requests (same dedup key) onto one Start — see transcodingStartM3U8Handler.
	startFlight singleflight.Group
	// noDataMu/noDataAt: torrent srcs that just failed the patient probe with
	// no bytes (ErrTorrentNoData). A retry inside torrentNoDataTTL fails
	// instantly instead of burning another ~45s probe per click.
	noDataMu sync.Mutex
	noDataAt map[string]time.Time

	segCleanupRunning int32
	stopCh            chan struct{}

	// pipeMgr is the optional in-memory fMP4 pipe manager (transcoding_pipe.go),
	// wired by registerTranscodingRoutes when PipeEnable is set. Held here so
	// Stop() tears down its sessions (and child ffmpeg) on shutdown/reload.
	pipeMgr *transcodingPipeManager
}

func NewTranscodingService(cfg config.Config) *TranscodingService {
	key := make([]byte, 32)
	_, _ = rand.Read(key)

	svc := &TranscodingService{
		cfg:     cfg,
		hmacKey: key,
		jobs:    make(map[string]*TranscodingJob),
		// safeFileRe whitelists the characters allowed in resolved paths.
		// `/` is included so P3.K2 multi-rung variants like
		// `v0/seg_00001.m4s` validate while still rejecting `..`, `\\`,
		// and absolute paths.  The HasPrefix(job.OutputDir) check below
		// is the actual containment guard against directory traversal.
		safeFileRe:    regexp.MustCompile(`^[A-Za-z0-9_.\-/]+$`),
		segmentFileRe: regexp.MustCompile(`^seg_(\d+)\.(m4s|ts)$`),
		restartTimes:  make(map[string][]time.Time),
		inflight:      make(map[string]string),
		stopCh:        make(chan struct{}),
	}

	// Init temp directory.
	tc := cfg.Transcoding
	if tc.TempRoot == "" {
		tc.TempRoot = filepath.Join("cache", "transcoding")
	}
	root := relToRuntime(tc.TempRoot)
	if err := os.MkdirAll(root, 0o755); err != nil {
		log.Warn().Err(err).Str("path", root).Msg("transcoding: failed to create temp root")
	} else {
		// Clean leftover JOB dirs from previous runs — only names matching newJobID (32 hex).
		// This constructor also runs on every transcoding config reload, and temp_root may be
		// a shared dir like /tmp: an unscoped wipe deleted foreign dirs (the YouTube muxer's
		// yt-mux-*) and even our own long-lived probe_cache/ocr_cache on every admin save.
		//
		// STALE-ONLY: skip dirs modified recently. A SECOND lampac-go instance sharing this
		// runtime dir (prod 2026-07-16: two full instances on /opt/lampac, one in a restart
		// loop) wiped the OTHER instance's LIVE job dirs on every boot — its ffmpeg died
		// mid-write («failed to rename index.m3u8.tmp: No such file or directory», exit 254;
		// the IPTV «Эконом» 404). An active job's dir mtime refreshes with every segment
		// write, so the age gate never matches a живой job; genuinely orphaned dirs age past
		// it and get reaped on the next start (and by the disk-budget sweep meanwhile).
		entries, _ := os.ReadDir(root)
		for _, e := range entries {
			if e.IsDir() && isTranscodeJobDirName(e.Name()) {
				if info, err := e.Info(); err == nil && time.Since(info.ModTime()) < constructorWipeMinAge {
					log.Debug().Str("dir", e.Name()).Msg("transcoding: startup sweep skipping fresh job dir (possibly another instance's live job)")
					continue
				}
				_ = os.RemoveAll(filepath.Join(root, e.Name()))
			}
		}
	}

	// Detect ffmpeg version for feature gating.
	svc.detectFFmpegVersion()

	// Probe zscale/tonemap availability once — gates the REAL HDR→SDR tonemap
	// chain vs the legacy matrix-only fallback (see buildTonemapPrefilter).
	{
		ff := cfg.Transcoding.FFmpeg
		if ff == "" {
			ff = "ffmpeg"
		}
		detectTonemapFilters(ff)
	}

	// P3.R: log a one-shot compatibility report so operators see
	// immediately which advanced features won't activate on their
	// specific ffmpeg build (Ubuntu 20.04 ships 4.2.7 — common case).
	LogCompatibilityReport(svc.ffmpegMajorVersion)

	// P1: detect hardware acceleration once at startup (lazy cached).
	ffmpegPath := cfg.Transcoding.FFmpeg
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	if !cfg.Transcoding.DisableHWAccel {
		svc.hwAccel = getOrDetectHWAccel(ffmpegPath)
	} else {
		svc.hwAccel = &HWAccelInfo{Kind: HWNone}
		log.Info().Msg("transcoding: HW accel disabled by config")
	}

	// P1: probe cache — keyed by source URL + HTTP HEAD metadata.
	svc.probeCache = newProbeCache(relToRuntime(tc.TempRoot))

	// P3.M: tesseract OCR for bitmap subs.  Detection is best-effort —
	// missing tesseract just means we keep using burn-in for PGS/DVB/DVD.
	svc.tesseract = detectTesseract()
	if svc.tesseract.Available {
		log.Info().Str("path", svc.tesseract.Path).Str("version", svc.tesseract.Version).
			Msg("transcoding: tesseract available — bitmap subs can be OCR-extracted")
		svc.ocrCache = NewOCRCache(relToRuntime(tc.TempRoot))
	} else {
		log.Info().Str("reason", svc.tesseract.Reason).
			Msg("transcoding: tesseract not available — bitmap subs will continue to use burn-in")
	}

	// P2.1: scheduler — concurrency semaphore + disk budget.
	svc.scheduler = newTranscodingScheduler(tc.MaxConcurrent, tc.DiskBudgetMB, tc.MinFreeMemMB, relToRuntime(tc.TempRoot))
	// Optional separate pool for capi / ALPAC-TV (disk budget 0 — the main
	// scheduler's diskBudgetLoop already sweeps the shared temp root).
	if tc.MaxConcurrentCapi > 0 {
		svc.schedulerCapi = newTranscodingScheduler(tc.MaxConcurrentCapi, 0, tc.MinFreeMemMB, relToRuntime(tc.TempRoot))
		log.Info().Int("capi_capacity", svc.schedulerCapi.Capacity).Msg("transcoding: separate capi/TV pool enabled")
	}

	// Start segment cleanup timer.
	go svc.segmentCleanupLoop()
	go svc.diskBudgetLoop()

	// Start dead job reaper — auto-cleans exited VOD jobs after grace period.
	go svc.deadJobReaper()

	return svc
}

func (svc *TranscodingService) Stop() {
	select {
	case svc.stopCh <- struct{}{}:
	default:
	}
	if svc.pipeMgr != nil {
		svc.pipeMgr.StopAll()
	}
	svc.StopAll()
}

// StatsSnapshot returns a JSON-serializable overview of transcoding state
// for the admin stats panel.
//
// P2.5: the snapshot now includes HW accel status, probe cache hit rate,
// scheduler pressure, and per-job smart-mode metadata so operators can
// tell at a glance which sessions are running hot and why.
func (svc *TranscodingService) StatsSnapshot() map[string]any {
	svc.mu.RLock()
	defer svc.mu.RUnlock()

	// Concurrency ceiling — prefer the scheduler's effective capacity.
	maxJobs := svc.cfg.Transcoding.MaxConcurrent
	if svc.scheduler != nil {
		maxJobs = svc.scheduler.Capacity
	}
	if maxJobs < 1 {
		maxJobs = 1
	}

	jobList := make([]map[string]any, 0, len(svc.jobs))
	for _, j := range svc.jobs {
		pid := 0
		if j.Cmd != nil && j.Cmd.Process != nil {
			pid = j.Cmd.Process.Pid
		}
		entry := map[string]any{
			"id":             j.ID,
			"stream_id":      j.StreamID,
			"pid":            pid,
			"started":        j.StartedUtc.Unix(),
			"source":         j.Context.Source,
			"live":           j.Context.Live,
			"exit_code":      j.ExitCode(),
			"mode":           string(j.Mode),
			"best_effort":    j.Context.BestEffort,
			"last_seg_index": j.LastSegmentIndex(),
			"selected_audio": j.SelectedAudio,
		}
		if j.ProfileLabel != "" {
			entry["client_profile"] = j.ProfileLabel
		}
		if len(j.KnownIssues) > 0 {
			entry["known_issues"] = j.KnownIssues
		}
		if j.Context.SubPlan.Strategy != "" && j.Context.SubPlan.Strategy != StrategyNone {
			entry["subtitle_strategy"] = string(j.Context.SubPlan.Strategy)
			if j.Context.SubPlan.BurnIn != nil {
				entry["subtitle_burnin_codec"] = j.Context.SubPlan.BurnIn.Codec
			}
		}
		if j.Warning != "" {
			entry["warning"] = j.Warning
		}
		if minPos, ok := j.MinPosition(); ok {
			entry["min_client_pos"] = minPos
		}
		jobList = append(jobList, entry)
	}

	out := map[string]any{
		"enabled":     svc.cfg.Transcoding.Enable,
		"active_jobs": len(svc.jobs),
		"max_jobs":    maxJobs,
		"jobs":        jobList,
	}

	// HW accel status block.
	if svc.hwAccel != nil {
		out["hw_accel"] = map[string]any{
			"kind":     string(svc.hwAccel.Kind),
			"device":   svc.hwAccel.Device,
			"detected": svc.hwAccel.Detected,
			"active":   svc.hwAccel.Active(),
		}
	}

	// Probe cache counters.
	if svc.probeCache != nil {
		out["probe_cache"] = svc.probeCache.Stats()
	}

	// Scheduler counters — queue depth, rejections, disk evictions.
	if svc.scheduler != nil {
		out["scheduler"] = svc.scheduler.Stats()
	}
	// Separate capi/TV pool counters when enabled.
	if svc.schedulerCapi != nil {
		out["scheduler_capi"] = svc.schedulerCapi.Stats()
	}

	// ffmpeg version (populated by detectFFmpegVersion at startup).
	if svc.ffmpegMajorVersion > 0 {
		out["ffmpeg_major"] = svc.ffmpegMajorVersion
		// P3.R: surface the compatibility table per-job so the admin
		// panel can show "ABR multi-rung: degraded (single-rendition)"
		// without re-deriving the version logic on the JS side.
		report := CompatibilityReport(svc.ffmpegMajorVersion)
		entries := make([]map[string]any, 0, len(report))
		for _, s := range report {
			entry := map[string]any{
				"name":        s.Feature.Name,
				"min_major":   s.Feature.MinMajor,
				"description": s.Feature.Description,
				"active":      s.Active,
			}
			if !s.Active && s.Fallback != "" {
				entry["fallback"] = s.Fallback
			}
			entries = append(entries, entry)
		}
		out["ffmpeg_compat"] = entries
	}

	// P3.M: tesseract / OCR cache stats.
	if svc.tesseract != nil {
		out["tesseract"] = map[string]any{
			"available": svc.tesseract.Available,
			"version":   svc.tesseract.Version,
		}
	}
	if svc.ocrCache != nil {
		out["ocr_cache"] = svc.ocrCache.Stats()
	}

	return out
}

// ---------------------------------------------------------------------------
// Token (HMAC-SHA256)
// ---------------------------------------------------------------------------

func (svc *TranscodingService) buildToken(id string) string {
	mac := svc.computeHMAC(id)
	b64 := base64.RawURLEncoding.EncodeToString(mac)
	return id + "." + b64
}

func (svc *TranscodingService) parseToken(streamID string) (string, bool) {
	parts := strings.SplitN(streamID, ".", 2)
	if len(parts) != 2 || len(parts[0]) != 32 {
		return "", false
	}
	id := parts[0]
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false
	}
	expected := svc.computeHMAC(id)
	if !hmac.Equal(sig, expected) {
		return "", false
	}
	return id, true
}

func (svc *TranscodingService) computeHMAC(id string) []byte {
	h := hmac.New(sha256.New, svc.hmacKey)
	h.Write([]byte(id))
	return h.Sum(nil)
}

func newJobID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

// isTranscodeJobDirName reports whether a directory name under temp_root is one of OUR job
// dirs (exactly the newJobID shape: 32 lowercase hex chars). Sweeps and disk-budget eviction
// must use this — temp_root can be a shared dir (/tmp), where unscoped removal killed foreign
// dirs (the YouTube muxer's yt-mux-*) under active playback.
func isTranscodeJobDirName(name string) bool {
	if len(name) != 32 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Start
// ---------------------------------------------------------------------------

type TranscodingStartRequest struct {
	Src       string            `json:"src"`
	Audio     *reqAudioOpts     `json:"audio"`
	HLS       *HLSOpts          `json:"hls"`
	Headers   map[string]string `json:"headers"`
	Live      bool              `json:"live"`
	Subtitles *bool             `json:"subtitles"`
	// MaxHeight overrides cfg.Transcoding.MaxTranscodeHeight for THIS job only (0 = use config).
	// The IPTV «Эконом» path sets it to force a low-res downscale of a live channel regardless of
	// the global cap. Unexported callers set it directly; not part of the public JSON surface.
	MaxHeight int `json:"-"`

	// Client is the capability payload from the plugin (P0 smart mode).
	// When nil, we fall back to always-transcode behaviour for backwards
	// compatibility (batch pre-warming, CLI callers, etc.).
	Client *transcode.ClientCaps `json:"client"`

	// useCapiPool routes this job to the separate capi/TV concurrency pool
	// (schedulerCapi) when it's configured. Set by the HTTP handler from the
	// tcpool=capi request marker, never from the JSON body (unexported).
	useCapiPool bool

	// PrewarmNextURLs is an optional list of upstream sources the plugin
	// expects the user to click next (typically the next 1-3 episodes of
	// a series).  P3.L: when set, the server fires off a background
	// ffprobe for each URL after the main job spawns successfully so the
	// next /transcoding/start sees a probe-cache hit and skips the 3-5s
	// cold probe delay.  Lightweight — does NOT pre-spawn ffmpeg, just
	// warms the probe cache.  Honours the existing AllowHosts policy.
	PrewarmNextURLs []string `json:"prewarmNextUrls"`
}

type reqAudioOpts struct {
	Index       int   `json:"index"`
	BitrateKbps int   `json:"bitrateKbps"`
	Stereo      *bool `json:"stereo"`
}

type HLSOpts struct {
	Seek    int   `json:"seek"`
	SegDur  int   `json:"segDur"`
	WinSize int   `json:"winSize"`
	FMP4    *bool `json:"fmp4"`
}

// schedulerFor picks the concurrency pool for a request: the separate capi/TV
// pool when the request is tagged AND that pool is configured, else the main
// pool. Centralised so Start() and the pipe path agree on the choice.
func (svc *TranscodingService) schedulerFor(req *TranscodingStartRequest) *transcodingScheduler {
	if req != nil && req.useCapiPool && svc.schedulerCapi != nil {
		return svc.schedulerCapi
	}
	return svc.scheduler
}

const torrentNoDataTTL = 90 * time.Second

// markTorrentNoData remembers that src just failed the patient probe with no
// bytes, so immediate retries fail fast (see torrentNoDataRecent).
func (svc *TranscodingService) markTorrentNoData(src string) {
	svc.noDataMu.Lock()
	defer svc.noDataMu.Unlock()
	if svc.noDataAt == nil {
		svc.noDataAt = map[string]time.Time{}
	}
	now := time.Now()
	for k, t := range svc.noDataAt { // bounded: expire on write
		if now.Sub(t) > torrentNoDataTTL {
			delete(svc.noDataAt, k)
		}
	}
	svc.noDataAt[src] = now
}

// torrentNoDataRecent reports whether src failed with ErrTorrentNoData within
// torrentNoDataTTL.
func (svc *TranscodingService) torrentNoDataRecent(src string) bool {
	svc.noDataMu.Lock()
	defer svc.noDataMu.Unlock()
	t, ok := svc.noDataAt[src]
	return ok && time.Since(t) <= torrentNoDataTTL
}

func (svc *TranscodingService) Start(req *TranscodingStartRequest) (*TranscodingJob, string) {
	tc := svc.cfg.Transcoding
	if !tc.Enable {
		return nil, "Transcoding disabled"
	}
	if req == nil || strings.TrimSpace(req.Src) == "" {
		return nil, "Source URL is required"
	}

	// P2.1: scheduler slot acquisition.  Non-blocking — if the pool is
	// full we return a busy error and the HTTP handler maps it to 503.
	// On successful job spawn the slot is transferred to the job and
	// released by cleanup(); on any error path below we release here.
	// capi/TV requests draw from their own pool (schedulerFor) so a full
	// main pool can't starve them — and vice-versa.
	sched := svc.schedulerFor(req)
	slotOwned := false
	if sched != nil {
		if err := sched.Acquire(); err != nil {
			return nil, err.Error()
		}
		slotOwned = true
		defer func() {
			if slotOwned {
				sched.Release()
			}
		}()
	}

	// Validate source.
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

	id := newJobID()
	streamID := svc.buildToken(id)

	// Run ffprobe — NEVER fatal.  If it fails, we go best-effort with
	// synthetic probe data and a warning, which surfaces back to the
	// plugin in the /transcoding/start response.
	tsInput := strings.Contains(req.Src, "/lite/pidtor/s") || strings.Contains(req.Src, "/ts/")
	if tsInput && svc.torrentNoDataRecent(req.Src) {
		return nil, ErrTorrentNoData.Error()
	}
	ffprobeData, probeErr := svc.runFFProbe(req.Src, req.Headers)
	bestEffort := false
	probeWarning := ""
	if probeErr != "" {
		// TorrServer inputs (pidtor / /ts) get a patient probe (~45s); when even
		// that saw no bytes the torrent has no metadata/seeds reachable from the
		// backend (prod 2026-08-23: "Torrent getting info" for minutes on EVERY
		// pool backend). A best-effort ffmpeg would only hang on the swarm —
		// 8 zombie processes per click — while the player spins. Fail fast with
		// a clear error so the client's recovery ladder moves on.
		if tsInput && probeErr == "ffprobe failed" {
			log.Warn().Str("src", req.Src).Msg("transcoding: torrent source gave no data within the probe budget — refusing best-effort (no metadata/seeds?)")
			svc.markTorrentNoData(req.Src)
			return nil, ErrTorrentNoData.Error()
		}
		log.Warn().Str("src", req.Src).Str("err", probeErr).Msg("transcoding: probe failed, continuing best-effort")
		bestEffort = true
		probeWarning = probeErr
		ffprobeData = nil
	}

	// Choose the cheapest viable pipeline (native/remux/audio-only/sw).
	//
	// P3.B: when the request omits an explicit `client` capability payload
	// (older Lampa builds, web clients without smart-mode integration, CLI
	// callers, …) we derive a conservative fallback from the User-Agent
	// header.  Explicit caps from the request still win — UA detection
	// only fills the gap.  The detected profile label is surfaced in the
	// response so the plugin / admin panel can show *why* a particular
	// pipeline was chosen.
	preferNative := !tc.DisableNativePlayback
	uaForCaps := getHeader(req.Headers, "userAgent")
	clientCaps, profileLabel, knownIssues := transcode.EnrichCapsFromUA(req.Client, uaForCaps)
	decision := transcode.SelectMode(ffprobeData, clientCaps, preferNative)
	if bestEffort && decision.Warning == "" {
		decision.Warning = probeWarning
	}

	// P3.C: subtitle planning must happen BEFORE the native short-circuit.
	// Reason: a fully native-playable source can still carry PGS / DVD /
	// DVB bitmap subs that no client renders — or ASS subs that some
	// profiles butcher.  Burn-in requires re-encoding video, so we have
	// to override the native decision when the planner picks burn-in.
	//
	// We pass sourceIsPipe=false here because pipe:0 substitution happens
	// further down only on the non-native path; if mode stays Native we
	// don't open a pipe.  When the pipe path is taken later we re-validate.
	subtitlesRequested := tc.DefaultSubs
	if req.Subtitles != nil {
		subtitlesRequested = *req.Subtitles
	}
	subPlan := planSubtitles(ffprobeData, clientCaps.Lang, profileLabel, subtitlesRequested, false)
	// P3.M: when bitmap burn-in is selected AND tesseract is available
	// AND the OCR cache already has a WebVTT for this stream, promote
	// to extract-from-OCR.  Skips video re-encode entirely on repeat
	// plays of the same source.  Cache misses keep the burn-in path
	// but we kick off the OCR runner asynchronously so the next play
	// hits the cache.
	if subPlan.Strategy == StrategyBurnIn && subPlan.BurnIn != nil &&
		canPromoteToOCR(svc.tesseract, svc.ocrCache, req.Src, subPlan.BurnIn, false) {
		log.Info().
			Str("orig_reason", subPlan.Reason).
			Int("subAbsIndex", subPlan.BurnIn.AbsIndex).
			Msg("transcoding: OCR cache hit — promoting burn-in to extract")
		subPlan.Strategy = StrategyExtract
		subPlan.Reason = fmt.Sprintf("OCR cache hit for %s — serving as WebVTT extract instead of burn-in", subPlan.BurnIn.Codec)
	}
	// P3.O: operator can force burn-in OFF (cheaper CPU profile) — the
	// planner returns BurnIn for clients that can't render WebVTT, but
	// when the operator opts out we override to "none".  Bitmap subs
	// will be silently dropped; text subs still extract via WebVTT.
	if tc.DisableSubtitleBurnIn && subPlan.Strategy == StrategyBurnIn {
		log.Info().Str("orig_reason", subPlan.Reason).Msg("transcoding: subtitle burn-in disabled by config — degrading to none")
		subPlan = SubtitlePlan{
			Strategy: StrategyNone,
			Streams:  subPlan.Streams,
			Reason:   "burn-in disabled by config — bitmap subs dropped",
		}
	}
	// Burn-in forces a video re-encode, so never apply it to omnivorous
	// players (VLC/Kodi/mpv) — they render every subtitle format natively,
	// and a forced re-encode here is exactly the wasted CPU we're cutting.
	if subPlan.Strategy == StrategyBurnIn && !clientCaps.IsOmnivorous() {
		switch decision.Mode {
		case transcode.ModeNative, transcode.ModeDirect, transcode.ModeRemux, transcode.ModeAudioOnly:
			decision.Mode = transcode.ModeSWTranscode
			decision.VideoIsCopy = false
			decision.Reason = decision.Reason + " + burn-in subtitles"
		}
	}

	// Mode == native: no ffmpeg, no output dir, no process.  The handler
	// will see Mode/OriginalURL and return the source URL straight back.
	if decision.Mode == transcode.ModeNative {
		// Native short-circuit never consumes a slot — release it so
		// the defer above treats this as a non-cleanup exit.
		// slotOwned is set to false AFTER Release to avoid a double-release
		// through the deferred cleanup.
		if slotOwned {
			sched.Release()
			slotOwned = false
		}
		log.Info().
			Str("mode", string(decision.Mode)).
			Str("reason", decision.Reason).
			Str("src", req.Src).
			Str("profile", profileLabel).
			Msg("transcoding: native playback, skipping ffmpeg")
		return &TranscodingJob{
			ID:            id,
			StreamID:      streamID,
			Mode:          decision.Mode,
			OriginalURL:   req.Src,
			Streams:       transcode.SummarizeStreams(ffprobeData),
			SelectedAudio: decision.AudioRelIndex,
			ProfileLabel:  profileLabel,
			KnownIssues:   knownIssues,
			StartedUtc:    time.Now().UTC(),
			exitCode:      0,
		}, ""
	}

	outputDir := filepath.Join(relToRuntime(tc.TempRoot), id)
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, "Failed to create output directory"
	}

	// For best-effort we inject a synthetic probe so downstream code
	// (main.m3u8 handler, codec selectors) does not blow up on nil.
	// The real segments/playlist come from ffmpeg's own index.m3u8 file
	// (read directly by the handler when BestEffort=true).
	if bestEffort {
		ffprobeData = map[string]any{
			"format":  map[string]any{"duration": "0.0"},
			"streams": []any{},
		}
	}

	hlsOpts := svc.mergeHLS(req.HLS)
	audioOpts := svc.mergeAudio(req.Audio)

	// Override audio index with the language-aware pick from selectMode().
	// Explicit req.Audio.Index from the client still wins if provided.
	if req.Audio == nil || req.Audio.Index <= 0 {
		audioOpts.Index = decision.AudioRelIndex
	}

	// (P3.C re-validate happens below, after the pipe-opening block has
	// settled the final source form.)
	subtitles := subtitlesRequested

	// Rewrite public URL to direct TorrServer for ffmpeg input.
	// Handles in-process torrs (→ localhost/ts/...) and external TorrServer.
	source := srcURL.String()
	tsAvail := torrsIsInProcess() || svc.cfg.TorrServer.Port > 0 || svc.cfg.TorrServer.URL != ""
	isBox := transcodeIsBox(svc.cfg)
	srcIsTS := false // TorrServer input → longer ffmpeg rw_timeout (cold-torrent start is legitimately slow)
	if (tsAvail || isBox) && strings.Contains(source, "/ts/") {
		if idx := strings.Index(source, "/ts/"); idx >= 0 {
			localPath := source[idx+3:]
			// Pool backend (main's pool via the ts-pick oracle on a box) or
			// the static TorrServer; "" = nothing local to rewrite onto.
			if base := tsDirectStreamBaseFor(svc.cfg, srcOrigin(source), localPath, tsAvail); base != "" {
				source = base + localPath
				srcIsTS = true
				log.Debug().Str("original", srcURL.String()).Str("rewritten", source).Msg("transcoding: rewrite source to TorrServer")
			}
		}
	}

	// Track torrent pipe for feeding ffmpeg via stdin (bypasses HTTP).
	var stdinPipe io.ReadCloser
	var torrentHash string
	var torrentFileIdx int

	// Rewrite pidtor stream URLs to direct TorrServer stream for ffmpeg input:
	// pool backend (oracle/local) or static TorrServer over HTTP; in-process
	// torrs without a pool pick opens the torrent reader directly.
	if strings.Contains(source, "/lite/pidtor/s") {
		if rewritten, ok := ffprobeRewritePidtor(svc.cfg, source); ok {
			source = rewritten
			srcIsTS = true
		} else if torrsIsInProcess() {
			// In-process: open pipe directly — bypasses HTTP entirely.
			// This avoids auth/routing/middleware issues with localhost requests.
			if pipe, h, fi := transcodingPidtorOpenPipe(source); pipe != nil {
				stdinPipe = pipe
				torrentHash = h
				torrentFileIdx = fi
				source = "pipe:0"
			} else if rewritten := transcodingDirectPidtorRewrite(svc.cfg, source); rewritten != "" {
				// Fallback to HTTP URL if pipe failed.
				source = rewritten
			}
		}
	}

	// For in-process torrent streams not yet piped (direct /ts/stream URLs),
	// also try to open a pipe to bypass HTTP.
	if torrsIsInProcess() && stdinPipe == nil && strings.Contains(source, "/stream") && strings.Contains(source, "link=") {
		if pipe, h, fi := torrentStreamOpenPipe(source); pipe != nil {
			stdinPipe = pipe
			torrentHash = h
			torrentFileIdx = fi
			source = "pipe:0"
		}
	}

	// P3.C re-validate: pipe:0 sources can't be re-read by the subtitles
	// filter, so a burn-in plan made earlier (when we didn't yet know the
	// source would be a pipe) must downgrade rather than crash ffmpeg.
	// Cheap audit — only re-runs the planner when stdinPipe was opened.
	if stdinPipe != nil && subPlan.Strategy == StrategyBurnIn {
		subPlan = planSubtitles(ffprobeData, clientCaps.Lang, profileLabel, subtitlesRequested, true)
		log.Info().
			Str("strategy", string(subPlan.Strategy)).
			Str("reason", subPlan.Reason).
			Msg("transcoding: pipe:0 source — re-evaluated subtitle plan")
	}

	ctx := transcodingContext{
		Source:         source,
		UserAgent:      sanitizeHeader(getHeader(req.Headers, "userAgent"), "Mozilla/5.0"),
		Referer:        sanitizeHeader(getHeader(req.Headers, "referer"), ""),
		HLS:            hlsOpts,
		Audio:          audioOpts,
		Live:           req.Live,
		Subtitles:      subtitles,
		OutputDir:      outputDir,
		MaxHeight:      req.MaxHeight,
		SrcIsTS:        srcIsTS,
		FFProbe:        ffprobeData,
		Mode:           decision.Mode,
		BestEffort:     bestEffort,
		SubPlan:        subPlan,
		StdinPipe:      stdinPipe,
		torrentHash:    torrentHash,
		torrentFileIdx: torrentFileIdx,
	}

	// P3.K2 / P3.N: ABR multi-output decision.  shouldMultiRung looks at
	// mode + ladder + HW capability — eligible jobs get per-rung subdirs
	// pre-created here so ffmpeg's HLS muxer doesn't fail at first segment.
	if ladder := svc.shouldMultiRung(ctx); ladder != nil {
		if err := ensureMultiRungDirs(outputDir, ladder); err != nil {
			log.Warn().Err(err).Msg("transcoding: failed to mkdir multi-rung subdirs, falling back to single-rung")
		} else {
			ctx.MultiRung = true
			ctx.Ladder = ladder
			log.Info().
				Int("rungs", len(ladder)).
				Str("primary", ladder[0].Label).
				Msg("transcoding: ABR multi-rung enabled")
		}
	}

	// Multi-audio HLS (hls4): when the source carries ≥2 shelvable audio
	// tracks, the main job additionally copies each one to a per-track shelf,
	// enabling hot track switching via lazy AAC renditions — no session
	// restart, no source re-read. Copy outputs are ~free; the AAC readers
	// spawn only when a viewer actually switches. Skipped for live (sliding
	// windows don't fit synthetic rendition playlists) and pipe sources
	// (stdin can't be re-read; shelves would be the only copy — fine — but
	// keep the first iteration simple).
	if cfg := svc.cfg.Transcoding; cfg.MultiAudio && !ctx.Live && ctx.Source != "pipe:0" &&
		ctx.Mode != transcode.ModeNative && ctx.Mode != transcode.ModeDirect {
		if tracks := listShelfAudioTracks(ctx.FFProbe); len(tracks) >= 2 {
			ctx.ShelfTracks = tracks
			rel := max(ctx.Audio.Index, 0)
			ctx.ActiveAudioAbs = -1
			for _, t := range tracks {
				if t.RelIndex == rel {
					ctx.ActiveAudioAbs = t.AbsIndex
					break
				}
			}
			ensureAudioShelfDirs(outputDir, tracks)
			log.Info().Int("tracks", len(tracks)).Int("activeAbs", ctx.ActiveAudioAbs).Msg("transcoding: multi-audio shelves enabled")
		}
	}

	// ffmpeg 4.x compat: force mpegts instead of fmp4 BEFORE creating process,
	// so the job context also reflects the downgrade (affects playlist generation).
	if svc.ffmpegMajorVersion > 0 && svc.ffmpegMajorVersion < 5 && ctx.HLS.FMP4 {
		log.Info().Int("ffmpeg_version", svc.ffmpegMajorVersion).Msg("transcoding: downgrading fmp4 → mpegts for old ffmpeg")
		ctx.HLS.FMP4 = false
	}

	cmd := svc.createProcess(ctx)

	// Feed torrent data directly to ffmpeg via stdin pipe.
	if ctx.StdinPipe != nil {
		cmd.Stdin = ctx.StdinPipe
	}

	stderrPipe, pipeErr := cmd.StderrPipe()
	if pipeErr != nil {
		if ctx.StdinPipe != nil {
			ctx.StdinPipe.Close()
		}
		_ = os.RemoveAll(outputDir)
		return nil, fmt.Sprintf("Failed to create stderr pipe: %v", pipeErr)
	}

	// Re-ensure the working dir exists immediately before launch. ffmpeg's HLS
	// muxer opens init.mp4 in cmd.Dir; if anything removed the dir between the
	// MkdirAll above and now (disk-budget eviction window, external cleaner),
	// it would die with "Failed to open segment 'init.mp4' / No such file or
	// directory". Cheap to redo; closes that gap.
	_ = os.MkdirAll(ctx.OutputDir, 0o755)

	if err := cmd.Start(); err != nil {
		if ctx.StdinPipe != nil {
			ctx.StdinPipe.Close()
		}
		_ = os.RemoveAll(outputDir)
		return nil, fmt.Sprintf("Failed to start ffmpeg: %v", err)
	}

	job := newTranscodingJob(id, streamID, outputDir, cmd, ctx)
	job.sched = sched // release this job's slot back to the pool it came from
	svc.startKeyframeMap(job)
	job.stderrPipe = stderrPipe
	job.Mode = decision.Mode
	job.Warning = decision.Warning
	job.SelectedAudio = decision.AudioRelIndex
	job.ProfileLabel = profileLabel
	job.KnownIssues = knownIssues
	// Stream summary is derived from the ORIGINAL probe, not the synthetic one.
	if !bestEffort {
		job.Streams = transcode.SummarizeStreams(ctx.FFProbe)
	}

	svc.mu.Lock()
	svc.jobs[id] = job
	svc.mu.Unlock()

	// Transfer slot ownership to the job: cleanup() will release it when
	// the ffmpeg process is fully torn down.  From this point forward the
	// deferred Release above must NOT fire.
	slotOwned = false

	log.Info().
		Str("streamId", job.StreamID).
		Str("mode", string(decision.Mode)).
		Str("reason", decision.Reason).
		Int("audioIdx", decision.AudioRelIndex).
		Bool("bestEffort", bestEffort).
		Str("profile", profileLabel).
		Msg("transcoding: job started")

	// P3.L: probe pre-warm for the requested next-up URLs.  Fires async
	// after the main job is fully registered so the user-facing latency
	// of /transcoding/start isn't affected.  Only the probe cache is
	// touched — ffmpeg is not spawned, so there's no scheduler-slot
	// contention with concurrent users.
	//
	// P3.O: operator can opt out for tighter scheduler-slot accounting.
	if len(req.PrewarmNextURLs) > 0 && !tc.DisablePrewarmProbe {
		go svc.prewarmProbe(req.PrewarmNextURLs, req.Headers)
	}

	// Pump stderr.
	go svc.pumpStderr(job)
	// Idle watchdog.
	go svc.idleWatchdog(job)
	// Produce-ahead window pacer (P4).
	go svc.windowWatchdog(job)
	// Wait for process exit.
	go svc.waitExit(job)

	return job, ""
}

// ---------------------------------------------------------------------------
// Resolve / Touch / GetFilePath
// ---------------------------------------------------------------------------

func (svc *TranscodingService) TryResolveJob(streamID string) (*TranscodingJob, bool) {
	id, ok := svc.parseToken(streamID)
	if !ok {
		return nil, false
	}
	svc.mu.RLock()
	job, found := svc.jobs[id]
	svc.mu.RUnlock()
	return job, found
}

func (svc *TranscodingService) Touch(job *TranscodingJob) {
	job.Touch()
}

func (svc *TranscodingService) GetFilePath(job *TranscodingJob, file string) string {
	if file == "" || !svc.safeFileRe.MatchString(file) {
		return ""
	}
	// Belt-and-suspenders for the multi-rung path-with-`/` allowance:
	// reject any `..` segment outright before letting filepath.Join near
	// it.  The HasPrefix(job.OutputDir) check below is the real safety
	// net but explicit `..` rejection makes traversal attempts noisier
	// in logs and avoids relying on platform-specific Clean behaviour.
	if strings.Contains(file, "..") || strings.HasPrefix(file, "/") {
		return ""
	}
	candidate := filepath.Join(job.OutputDir, file)
	if !strings.HasPrefix(candidate, job.OutputDir) {
		return ""
	}
	if _, err := os.Stat(candidate); err != nil {
		return ""
	}
	return candidate
}

func (svc *TranscodingService) ReportSegmentAccess(job *TranscodingJob, idx int) {
	job.UpdateLastSegmentIndex(idx)
}

// ---------------------------------------------------------------------------
// Job coalescing (P4) — collapse a storm of identical /transcoding/start
// retries onto a single ffmpeg while it is still warming up.
// ---------------------------------------------------------------------------

// LookupInflightJob returns a still-warming-up job for the given coalescing
// key, or nil.  A job is reusable only while it hasn't served a segment yet:
// once it's streaming, a fresh request gets its own job so independent viewers
// never share a process (and thus never disrupt each other on seek).
func (svc *TranscodingService) LookupInflightJob(key string) *TranscodingJob {
	if key == "" {
		return nil
	}
	svc.inflightMu.Lock()
	jobID, ok := svc.inflight[key]
	svc.inflightMu.Unlock()
	if !ok {
		return nil
	}
	svc.mu.RLock()
	job, found := svc.jobs[jobID]
	svc.mu.RUnlock()
	if !found {
		svc.inflightMu.Lock()
		if svc.inflight[key] == jobID {
			delete(svc.inflight, key)
		}
		svc.inflightMu.Unlock()
		return nil
	}
	// Don't reuse a crashed job, or one that has already started streaming.
	if job.HasExited() || job.LastSegmentIndex() >= 0 {
		return nil
	}
	return job
}

// RegisterInflightJob records a freshly spawned job under its coalescing key so
// concurrent identical retries reuse it. No-op for empty keys.
func (svc *TranscodingService) RegisterInflightJob(key string, job *TranscodingJob) {
	if key == "" || job == nil {
		return
	}
	job.dedupKey = key
	svc.inflightMu.Lock()
	svc.inflight[key] = job.ID
	svc.inflightMu.Unlock()
}

func (svc *TranscodingService) dropInflight(job *TranscodingJob) {
	if job == nil || job.dedupKey == "" {
		return
	}
	svc.inflightMu.Lock()
	if svc.inflight[job.dedupKey] == job.ID {
		delete(svc.inflight, job.dedupKey)
	}
	svc.inflightMu.Unlock()
}

// ---------------------------------------------------------------------------
// Seek
// ---------------------------------------------------------------------------

func (svc *TranscodingService) SeekAsync(streamID string, seconds int, startSegment *int) (bool, string) {
	return svc.seekAsyncExact(streamID, seconds, 0, startSegment)
}

// seekAsyncExact — перезапуск ffmpeg с позиции; exact > 0 — точное время ключевого кадра из
// карты (см. keyframes.go), seconds — его номинал для start_number и старой логики.
func (svc *TranscodingService) seekAsyncExact(streamID string, seconds int, exact float64, startSegment *int) (bool, string) {
	if seconds < 0 {
		return false, "ss must be >= 0"
	}
	job, ok := svc.TryResolveJob(streamID)
	if !ok {
		return false, "Job not found"
	}

	newCtx := job.Context
	newCtx.HLS.Seek = seconds
	newCtx.HLS.SeekExact = exact
	if startSegment != nil {
		sn := *startSegment
		newCtx.StartNum = &sn
	}

	// Kill old process (also closes old stdin pipe).
	svc.killProcess(job)

	// Re-open torrent pipe for the new process if needed.
	if newCtx.torrentHash != "" && torrsIsInProcess() {
		newCtx.StdinPipe = nil // old pipe was closed by killProcess
		if pipe, _, _ := torrentOpenPipeByHash(newCtx.torrentHash, newCtx.torrentFileIdx); pipe != nil {
			newCtx.StdinPipe = pipe
			newCtx.Source = "pipe:0"
		} else {
			log.Warn().Str("hash", newCtx.torrentHash).Msg("transcoding: seek — failed to re-open torrent pipe")
			return false, "Failed to re-open torrent stream for seek"
		}
	}

	// Create new process.
	if err := os.MkdirAll(newCtx.OutputDir, 0o755); err != nil {
		return false, "Failed to create output directory"
	}

	cmd := svc.createProcess(newCtx)
	if newCtx.StdinPipe != nil {
		cmd.Stdin = newCtx.StdinPipe
	}
	stderrPipe, pipeErr := cmd.StderrPipe()
	if pipeErr != nil {
		if newCtx.StdinPipe != nil {
			newCtx.StdinPipe.Close()
		}
		return false, fmt.Sprintf("Failed to create stderr pipe: %v", pipeErr)
	}
	if err := cmd.Start(); err != nil {
		if newCtx.StdinPipe != nil {
			newCtx.StdinPipe.Close()
		}
		return false, fmt.Sprintf("Failed to start ffmpeg: %v", err)
	}

	newJob := newTranscodingJob(job.ID, job.StreamID, job.OutputDir, cmd, newCtx)
	newJob.copyKeyframesFrom(job) // карта та же — источник не менялся
	newJob.stderrPipe = stderrPipe
	newJob.sched = job.sched       // reuses the same slot → release to the same pool
	newJob.dedupKey = job.dedupKey // carry coalescing entry across the restart

	svc.mu.Lock()
	svc.jobs[job.ID] = newJob
	svc.mu.Unlock()

	go svc.pumpStderr(newJob)
	go svc.idleWatchdog(newJob)
	go svc.windowWatchdog(newJob)
	go svc.waitExit(newJob)

	return true, ""
}

// ---------------------------------------------------------------------------
// Stop
// ---------------------------------------------------------------------------

func (svc *TranscodingService) StopAsync(streamID string) bool {
	job, ok := svc.TryResolveJob(streamID)
	if !ok {
		return false
	}
	svc.stopJob(job, true)
	return true
}

func (svc *TranscodingService) StopAll() {
	svc.mu.RLock()
	jobs := make([]*TranscodingJob, 0, len(svc.jobs))
	for _, j := range svc.jobs {
		jobs = append(jobs, j)
	}
	svc.mu.RUnlock()

	for _, j := range jobs {
		svc.stopJob(j, true)
	}
}

func (svc *TranscodingService) stopJob(job *TranscodingJob, cleanup bool) {
	// P2.4: mark the stop as intentional so waitExit doesn't try to
	// auto-restart the process.
	atomic.StoreInt32(&job.stopRequested, 1)
	svc.killProcess(job)
	if cleanup {
		svc.cleanup(job)
	}
}

func (svc *TranscodingService) killProcess(job *TranscodingJob) {
	job.Cancel()
	// A window-paused (SIGSTOP'd) process must be SIGCONT'd first so SIGKILL
	// reaps it cleanly and the torrent reader unblocks.
	if job.windowPaused.Load() {
		resumeProc(job.Cmd)
		job.windowPaused.Store(false)
	}
	// Close stdin pipe to unblock torrent reader and signal EOF to ffmpeg.
	if job.Context.StdinPipe != nil {
		job.Context.StdinPipe.Close()
	}
	if job.Cmd != nil && job.Cmd.Process != nil && !job.HasExited() {
		_ = job.Cmd.Process.Kill()
	}
	// Wait briefly.
	if job.Cmd != nil {
		done := make(chan struct{})
		go func() {
			_ = job.Cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	}
	if job.HasExited() && job.Cmd.ProcessState != nil {
		atomic.StoreInt32(&job.exitCode, int32(job.Cmd.ProcessState.ExitCode()))
	}
}

func (svc *TranscodingService) cleanup(job *TranscodingJob) {
	svc.mu.Lock()
	cur, existed := svc.jobs[job.ID]
	if existed && cur != job {
		// Под этим id уже новая версия задания (перемотка или автоперезапуск кладут новую структуру
		// под тот же id, с той же папкой и тем же слотом). Уборка старой не должна стирать новую из
		// учёта, отдавать её слот, глушить её дорожки звука и удалять её папку: иначе новый ffmpeg
		// остаётся работать вне учёта — сироты на боксе 26.09 все были в удалённых папках.
		svc.mu.Unlock()
		return
	}
	delete(svc.jobs, job.ID)
	svc.mu.Unlock()

	// Multi-audio readers die with the job (their shelves are about to go).
	stopAudioReaders(job.OutputDir)

	// P2.4: drop the restart budget entry for this stream so the map
	// doesn't grow forever.
	svc.restartMu.Lock()
	delete(svc.restartTimes, job.StreamID)
	svc.restartMu.Unlock()

	// P4: drop the job-coalescing entry so the next request spawns fresh.
	svc.dropInflight(job)

	// P2.1: return the concurrency slot to the pool it came from (main or
	// the separate capi/TV pool).  Only on jobs that actually owned a slot.
	if existed {
		rel := job.sched
		if rel == nil {
			rel = svc.scheduler
		}
		if rel != nil {
			rel.Release()
		}
	}

	if !existed {
		return
	}

	_ = os.RemoveAll(job.OutputDir)
}

// ---------------------------------------------------------------------------
// FFProbe
// ---------------------------------------------------------------------------

func (svc *TranscodingService) runFFProbe(src string, headers map[string]string) (map[string]any, string) {
	// P1: probe cache lookup — saves seconds on repeat clicks, especially
	// for torrent sources where the alternative is reading 20 MB from the
	// swarm again.
	if cached, ok := svc.probeCache.Get(src, headers); ok {
		return cached, ""
	}
	type probeOut struct {
		result map[string]any
		errMsg string
	}
	v, _, _ := svc.probeFlight.Do(src, func() (any, error) {
		result, errMsg := svc.runFFProbeUncached(src, headers)
		if errMsg == "" && result != nil {
			svc.probeCache.Put(src, headers, result)
		}
		return probeOut{result: result, errMsg: errMsg}, nil
	})
	out, _ := v.(probeOut)
	return out.result, out.errMsg
}

// prewarmProbe runs ffprobe in the background for each URL the plugin
// declared as "next-up" (P3.L).  Populates probeCache so the user's next
// /transcoding/start hits a cache and skips the 3-5s cold probe.
//
// Constraints:
//   - cap at 3 URLs total to avoid runaway when a plugin sends a whole
//     season's worth of next-episode hints
//   - skip URLs already cached (cheap — Get() is the same path Start uses)
//   - honour AllowHosts so a malicious plugin can't make the server
//     fetch arbitrary URLs through the prewarm
//   - silent — failures here only mean "next click won't have cache",
//     they're not user-visible errors
//
// The function is intentionally non-blocking: callers spawn it via
// `go svc.prewarmProbe(...)`.
func (svc *TranscodingService) prewarmProbe(urls []string, headers map[string]string) {
	if len(urls) == 0 {
		return
	}
	tc := svc.cfg.Transcoding

	// Hard cap so a misbehaving plugin can't queue a full season.
	if len(urls) > 3 {
		urls = urls[:3]
	}

	for _, raw := range urls {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			continue
		}
		// AllowHosts policy mirror — same gate as Start().
		if len(tc.AllowHosts) > 0 {
			allowed := false
			for _, h := range tc.AllowHosts {
				if strings.EqualFold(h, u.Host) {
					allowed = true
					break
				}
			}
			if !allowed {
				log.Debug().Str("host", u.Host).Msg("transcoding: prewarm skipped (host not in allow list)")
				continue
			}
		}

		// Cache hit — nothing to do.
		if cached, ok := svc.probeCache.Get(raw, headers); ok && cached != nil {
			continue
		}

		log.Info().Str("url", raw[:min(len(raw), 80)]).Msg("transcoding: prewarming probe for next-up source")
		// runFFProbe handles cache write on success.
		_, errMsg := svc.runFFProbe(raw, headers)
		if errMsg != "" {
			log.Debug().Str("url", raw[:min(len(raw), 80)]).Str("err", errMsg).Msg("transcoding: prewarm probe failed (cache miss for next click)")
		}
	}
}

func (svc *TranscodingService) runFFProbeUncached(src string, headers map[string]string) (map[string]any, string) {
	ffprobePath := "ffprobe"
	// Try to find ffprobe next to ffmpeg.
	tc := svc.cfg.Transcoding
	if tc.FFmpeg != "" && tc.FFmpeg != "ffmpeg" {
		dir := filepath.Dir(tc.FFmpeg)
		candidate := filepath.Join(dir, "ffprobe")
		if _, err := os.Stat(candidate); err == nil {
			ffprobePath = candidate
		}
	}

	// Rewrite public URL to direct TorrServer for /ts/ paths.
	// Handles in-process torrs (→ localhost/ts/...) and external TorrServer.
	probeSrc := src
	tsAvail := torrsIsInProcess() || svc.cfg.TorrServer.Port > 0 || svc.cfg.TorrServer.URL != ""
	if (tsAvail || transcodeIsBox(svc.cfg)) && strings.Contains(src, "/ts/") {
		if idx := strings.Index(src, "/ts/"); idx >= 0 {
			localPath := src[idx+3:] // strip "/ts" prefix, keep "/stream/..."
			if base := tsDirectStreamBaseFor(svc.cfg, srcOrigin(src), localPath, tsAvail); base != "" {
				probeSrc = base + localPath
				log.Debug().Str("original", src).Str("rewritten", probeSrc).Msg("transcoding: ffprobe rewrite URL to TorrServer")
			}
		}
	}

	// Rewrite pidtor stream URLs to direct TorrServer stream (pool backend via
	// the ts-pick oracle on a box / local pool / static); in-process torrs
	// without a pool pick: add magnet directly, probe via temp file.
	if strings.Contains(src, "/lite/pidtor/s") {
		if rewritten, ok := ffprobeRewritePidtor(svc.cfg, src); ok {
			probeSrc = rewritten
		} else if torrsIsInProcess() {
			if result, errMsg := ffprobeDirectPidtor(ffprobePath, src); result != nil || errMsg != "" {
				return result, errMsg
			}
		}
	}

	// In-process torrent streams: probe directly from torrent reader via temp
	// file to avoid seeking issues (AVI index at EOF).
	// Done AFTER all rewrites so it catches pidtor rewritten URLs too.
	if torrsIsInProcess() && strings.Contains(probeSrc, "/stream") {
		if parsed, err := url.Parse(probeSrc); err == nil {
			hash := parsed.Query().Get("link")
			rawIdx, _ := strconv.Atoi(parsed.Query().Get("index"))
			// index in URL is 1-based (MatriX convention); convert to 0-based.
			fileIdx := rawIdx - 1
			if fileIdx < 0 {
				fileIdx = 0
			}
			if hash != "" {
				result, errMsg := ffprobeTorrentDirect(ffprobePath, hash, fileIdx)
				if result != nil || errMsg != "" {
					return result, errMsg
				}
			}
		}
	}

	// P1.5: multi-stage probe.
	//
	// Stage 1 (lite): small analyzeduration/probesize + tight 4s budget.
	// Most well-formed files report all their streams in under a second,
	// and the short timeout means a dead URL surfaces quickly instead of
	// stalling the user for 15-30s waiting for ffmpeg to give up.
	//
	// Stage 2 (full): 10M/20M scan with a 12s budget.  Only invoked when
	// lite returned no streams or no format block — typically MPEG-TS or
	// other containers where ffmpeg needs to read several seconds of data
	// before codec detection resolves.
	ua := sanitizeHeader(getHeader(headers, "userAgent"), "Mozilla/5.0")
	ref := sanitizeHeader(getHeader(headers, "referer"), "")

	buildArgs := func(analyzeDuration, probeSize string, rwTimeoutUsec int) []string {
		args := []string{
			"-v", "error",
			"-print_format", "json",
			"-show_format", "-show_streams",
			"-analyzeduration", analyzeDuration,
			"-probesize", probeSize,
			"-rw_timeout", strconv.Itoa(rwTimeoutUsec),
		}
		if ua != "" {
			args = append(args, "-user_agent", ua)
		}
		if ref != "" {
			args = append(args, "-headers", fmt.Sprintf("Referer: %s\r\n", ref))
		}
		return append(args, probeSrc)
	}

	runStage := func(name string, args []string, timeout time.Duration) (map[string]any, string) {
		log.Debug().Str("stage", name).Str("ffprobe", ffprobePath).Strs("args", args).Msg("transcoding: probing")
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, ffprobePath, args...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			stderrStr := strings.TrimSpace(stderr.String())
			log.Warn().Str("stage", name).Err(err).Str("stderr", stderrStr).Str("src", probeSrc).Msg("transcoding: ffprobe failed")
			errDetail := "ffprobe failed"
			if stderrStr != "" {
				errDetail = "ffprobe: " + stderrStr
			}
			return nil, errDetail
		}
		var result map[string]any
		if err := json.Unmarshal(out, &result); err != nil {
			return nil, "ffprobe: invalid JSON"
		}
		return result, ""
	}

	// Stage 1 — lite (2 MB / 2 seconds / 4s wall timeout).
	liteArgs := buildArgs("2000000", "2000000", 4000000)
	result, errMsg := runStage("lite", liteArgs, 4*time.Second)
	if errMsg == "" && result != nil && probeHasStreams(result) {
		return result, ""
	}
	liteErr := errMsg

	// Stage 2 — full (10 MB / 10 seconds / 15s wall timeout).
	//
	// TorrServer inputs (pidtor / /ts rewrites onto a backend's /stream) get
	// a patient 40s budget: a COLD torrent legitimately needs ~20-30s before
	// the first bytes arrive (metadata + preload from the swarm — prod
	// 2026-08-23: every pidtor probe died at 4s+15s while ffmpeg got data at
	// ~25s), and a failed probe forces "best-effort sw-transcode" — the most
	// expensive mode and a dead end for 4K HEVC on a GPU-less box — where a
	// real probe would have picked native/remux/audio-only. The bytes are
	// awaited either way (ffmpeg waits for the same swarm), so the extra
	// patience costs nothing overall and the probe returns the moment data
	// flows.
	fullBudget := 15 * time.Second
	if tsInput := (strings.Contains(probeSrc, "/stream") && strings.Contains(probeSrc, "link=")) ||
		strings.Contains(src, "/lite/pidtor/s") || strings.Contains(src, "/ts/"); tsInput {
		fullBudget = 40 * time.Second
	}
	fullArgs := buildArgs("10000000", "20000000", int(fullBudget/time.Microsecond))
	result, errMsg = runStage("full", fullArgs, fullBudget)
	if errMsg == "" && result != nil {
		if _, ok := result["format"]; ok {
			return result, ""
		}
	}
	if errMsg == "" && liteErr != "" {
		errMsg = liteErr
	}
	if errMsg == "" {
		errMsg = "ffprobe: no format info"
	}
	return nil, errMsg
}

// probeHasStreams returns true when the probe JSON advertises at least
// one video or audio stream — the bar for skipping the slow full probe.
func probeHasStreams(probe map[string]any) bool {
	streams, _ := probe["streams"].([]any)
	for _, s := range streams {
		sm, _ := s.(map[string]any)
		if sm == nil {
			continue
		}
		switch toStr(sm["codec_type"]) {
		case "video", "audio":
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Create FFmpeg process
// ---------------------------------------------------------------------------

func (svc *TranscodingService) createProcess(ctx transcodingContext) *exec.Cmd {
	tc := svc.cfg.Transcoding

	var args []string

	args = append(args, "-hide_banner")

	// -user_agent and -headers are HTTP-specific options.
	// Skip them when reading from stdin pipe (pipe:0).
	if ctx.StdinPipe == nil {
		// Strip any CR/LF from UA/Referer before they go into ffmpeg header
		// lines — otherwise an injected \r\n could smuggle extra request
		// headers (or extra ffmpeg -headers lines) into the upstream fetch.
		if ua := sanitizeHeader(ctx.UserAgent, ""); ua != "" {
			args = append(args, "-user_agent", ua)
		}
		if ref := sanitizeHeader(ctx.Referer, ""); ref != "" {
			// Real CRLF, not the literal `\r\n` text — the double-escaped version sent a
			// malformed header, so referer-gated CDNs 403'd the TRANSCODE while the probe
			// (which always used proper CRLF) succeeded → «ffmpeg_io_error» right after start.
			args = append(args, "-headers", fmt.Sprintf("Referer: %s\r\n", ref))
		}
		// Input resilience — critical for the REMOTE box, whose input crosses the public
		// internet (box → main /proxy → upstream): without these, any transient reset kills
		// ffmpeg with an io error and forces a full job restart from the last segment (a
		// visible stall for the viewer), and a silently stalled connection hangs the job
		// until the idle reaper. Reconnect handles resets/5xx in place; 4xx is NOT retried
		// (auth/expiry — a restart can't fix it either). 30s per-io timeout unsticks stalls.
		if strings.HasPrefix(ctx.Source, "http://") || strings.HasPrefix(ctx.Source, "https://") {
			// TorrServer inputs get a wider per-io timeout: a cold torrent needs
			// metadata + first pieces from the swarm before /stream returns any
			// bytes — 30s killed legitimate slow starts (prod 2026-07-16: every
			// cold/slow torrent died with «Error opening input: Connection timed
			// out», then burned 3 auto-restarts on the same wall). CDN sources
			// keep the tight 30s so dead URLs still fail fast.
			rwTimeout := "30000000" // 30s in microseconds
			if ctx.SrcIsTS {
				rwTimeout = "90000000" // 90s
			}
			args = append(args,
				"-reconnect", "1",
				"-reconnect_streamed", "1",
				"-reconnect_delay_max", "5",
				"-rw_timeout", rwTimeout,
			)
			if svc.ffmpegMajorVersion >= 5 {
				// 4.3+ only — Ubuntu 20.04 ships 4.2.7 where an unknown option is a hard error.
				args = append(args, "-reconnect_on_network_error", "1")
			}
		}
	}

	// P3.K2: ABR multi-output dispatch.  When Start() decided this job is
	// eligible for multi-rung, route through the dedicated argv builder
	// — it owns the entire post-input flag chain (per-rung video maps,
	// var_stream_map, per-variant output paths).
	if ctx.MultiRung && len(ctx.Ladder) > 1 {
		args = append(args, svc.buildMultiRungArgs(ctx)...)
		log.Debug().Strs("args", args).Int("rungs", len(ctx.Ladder)).Msg("transcoding: ffmpeg multi-rung args")
		cmd := exec.Command(tc.FFmpeg, args...)
		cmd.Dir = ctx.OutputDir
		cmd.Stdout = nil
		return cmd
	}

	// P1: HW decode input args — must be placed BEFORE -i so the decoder
	// lands frames in GPU memory.  Only emitted when mode is hw-transcode
	// or sw-transcode with an active HW backend; remux/audio-only paths
	// don't decode video at all so HW is a no-op.
	if (ctx.Mode == transcode.ModeHWTranscode || ctx.Mode == transcode.ModeSWTranscode) && (!ctx.DisableHW && svc.hwAccel.Active()) {
		args = append(args, svc.hwAccel.buildHWInputArgs()...)
	}

	// For pipe:0 (torrent pipe): -ss before -i triggers input seeking which fails on
	// non-seekable pipes ("Seek to desired resync point failed"). Instead, place -ss
	// after -i so ffmpeg reads and discards frames up to the seek point (output seek).
	// This is slower but the only option for unseekable inputs.
	if ctx.HLS.Seek > 0 && ctx.StdinPipe == nil {
		args = append(args, "-ss", ctx.seekArg(false), "-noaccurate_seek")
	}

	args = append(args, "-nostats", "-progress", "pipe:2")
	// -stats_period requires ffmpeg 5.0+
	if svc.ffmpegMajorVersion >= 5 {
		if ctx.Live {
			args = append(args, "-stats_period", "1")
		} else {
			args = append(args, "-stats_period", "5")
		}
	}

	// Demuxer commands.
	args = appendCommandArgs(args, tc.Command, "demuxer")

	// Read rate: -readrate requires ffmpeg 4.4+, -readrate_initial_burst requires 7.0+.
	// Skip read rate throttling for pipe:0 — torrent data arrives at variable speed,
	// throttling causes ffmpeg to produce segments too slowly and player times out.
	if ctx.Live {
		args = append(args, "-re")
	} else if tc.Playlist.ReadRate > 0 && svc.ffmpegMajorVersion >= 5 && ctx.StdinPipe == nil {
		args = append(args, "-readrate", strconv.FormatFloat(tc.Playlist.ReadRate, 'f', -1, 64))
		if tc.Playlist.Burst > 0 && svc.ffmpegMajorVersion >= 7 {
			args = append(args, "-readrate_initial_burst", strconv.Itoa(tc.Playlist.Burst))
		}
	}

	// For torrent streams (in-process pipe or HTTP), add input flags that help
	// with containers where seeking is slow/impossible (AVI index at EOF).
	// -fflags +ignidx  — don't seek for AVI index, scan sequentially
	// +genpts          — generate PTS from frame data
	// +discardcorrupt  — skip corrupt frames
	// -seekable 0      — force non-seekable mode so ffmpeg reads sequentially
	isTorrentStream := ctx.StdinPipe != nil || (strings.Contains(ctx.Source, "/stream") && strings.Contains(ctx.Source, "link="))
	if isTorrentStream {
		// -seekable is an HTTP protocol option — only valid for http/https inputs.
		// pipe:0 is inherently non-seekable, so ffmpeg already knows.
		if ctx.StdinPipe == nil {
			args = append(args, "-seekable", "0")
		}
		args = append(args,
			"-fflags", "+ignidx+genpts+discardcorrupt",
			"-probesize", "20000000", // 20 MB — same as ffprobeTorrentDirect
			"-analyzeduration", "10000000", // 10 seconds
		)
	} else {
		// P3.F: fast-start for HTTP / CDN sources.
		//
		// ffmpeg defaults (probesize=5 MB, analyzeduration=5 s) cause the
		// "крутится 30 секунд → финиш" reports on cheap Android TV / TCL —
		// the player times out before ffmpeg writes seg_0.  We already ran
		// ffprobe in Start() with the user's headers; the codec/audio
		// inventory is known.  Cutting ffmpeg's own probe down to 2 MB / 2 s
		// trims 3-5 s off cold start without sacrificing reliability for
		// well-formed CDN sources.
		//
		// `-fflags +nobuffer` tells the demuxer to flush as soon as the
		// first packet arrives instead of waiting for the input buffer to
		// fill.  Best-effort mode keeps the bigger buffer because we may
		// need to retry codec detection.
		//
		// P3.O: operators chasing exotic-source bugs can disable this
		// to fall back to ffmpeg defaults (5 MB / 5 s probe).
		if !ctx.BestEffort && !tc.DisableFastStart {
			args = append(args,
				"-fflags", "+nobuffer+genpts+discardcorrupt",
				"-probesize", "2000000", // 2 MB
				"-analyzeduration", "2000000", // 2 seconds
			)
		}
	}

	args = append(args, "-i", ctx.Source)

	// For pipe:0 inputs: output-seek (after -i). ffmpeg reads and discards frames
	// up to the seek point. Slower than input-seek but works with non-seekable streams.
	if ctx.HLS.Seek > 0 && ctx.StdinPipe != nil {
		args = append(args, "-ss", ctx.seekArg(true), "-noaccurate_seek")
	}

	// CRITICAL: when seeking, we MUST preserve original PTS so that the
	// segments produced by the post-seek ffmpeg process have timestamps
	// matching the master playlist.  Without -copyts, ffmpeg normalizes
	// PTS to start at 0, and the player reacts to "seg_00600 starts at
	// t=0" by jumping back to the beginning — exactly the "возвращает в
	// начало" bug.  Previously -copyts was only added when subtitles
	// were requested, which is why seek silently worked with subs but
	// broke without them.
	//
	// P3.I — seek-drift hardening on Tizen / WebOS:
	//
	//   `-avoid_negative_ts make_zero` is a belt-and-suspenders for older
	//   ffmpeg (< 5) where -start_at_zero alone occasionally lets B-frame
	//   PTS go briefly negative.  Tizen and WebOS muxers reject negative
	//   timestamps and the player reports "видео не найдено или повреждено"
	//   30-60 seconds after a seek — exactly the drift symptom in 4PDA.
	//   Override the global `-avoid_negative_ts disabled` from input cmds.
	//
	//   `-fps_mode cfr` (`-vsync cfr` on ffmpeg < 5) emits a constant frame
	//   rate after seek when the encoder re-encodes — without it some HW
	//   encoders (NVENC, VAAPI) produce variable-rate output that drifts
	//   against the audio track, manifesting as the "после перемотки звук
	//   опережает видео" reports on Samsung Q-series.  Skipped for stream-
	//   copy modes (Remux / AudioOnly) where the encoder doesn't run.
	if ctx.HLS.Seek > 0 {
		args = append(args, "-copyts", "-start_at_zero")
		if svc.ffmpegMajorVersion > 0 && svc.ffmpegMajorVersion < 5 {
			args = append(args, "-avoid_negative_ts", "make_zero")
		}
		// Video re-encoded → enforce CFR to keep A/V aligned post-seek.
		if ctx.Mode == transcode.ModeSWTranscode || ctx.Mode == transcode.ModeHWTranscode {
			if svc.ffmpegMajorVersion >= 5 {
				args = append(args, "-fps_mode", "cfr")
			} else {
				args = append(args, "-vsync", "cfr")
			}
		}
	}

	// Input commands.
	args = appendCommandArgs(args, tc.Command, "input")

	// P3.C: subtitle handling — three mutually exclusive paths driven by
	// the SubPlan computed in Start():
	//
	//   StrategyBurnIn  → -filter_complex renders the chosen subtitle
	//                     stream into the video frame.  The default
	//                     -map 0:v:0 from tc.Command["output"] is later
	//                     rewritten to -map [vout] so ffmpeg emits the
	//                     filtered stream instead of the raw video.
	//                     Forces video re-encode (Mode was bumped in
	//                     Start() so appendVideoCodec picks a real codec).
	//
	//   StrategyExtract → existing per-stream WebVTT extraction.  Each
	//                     subtitle becomes a sibling subs_<idx>.vtt file
	//                     consumed by the /transcoding/{id}/subtitles
	//                     endpoint.  This is the path that survives
	//                     unchanged for clients that render WebVTT.
	//
	//   StrategyNone    → no subtitle work at all; -sn below suppresses
	//                     any default ffmpeg subtitle output.
	mapVideoLabel := ""
	if ctx.SubPlan.Strategy == StrategyBurnIn {
		filterComplex, label := buildBurnInVideoFilter(ctx.SubPlan, ctx.Source)
		if filterComplex != "" {
			args = append(args, "-filter_complex", filterComplex)
			mapVideoLabel = label
		}
	}

	// Subtitle extract maps — only when StrategyExtract.
	if ctx.SubPlan.Strategy == StrategyExtract && !ctx.Live {
		if streams, ok := ctx.FFProbe["streams"].([]any); ok {
			// Only add -copyts if we didn't already add it for the seek path.
			if ctx.HLS.Seek == 0 {
				args = append(args, "-copyts")
			}
			for _, s := range streams {
				sm, _ := s.(map[string]any)
				if sm == nil {
					continue
				}
				if fmt.Sprint(sm["codec_type"]) != "subtitle" {
					continue
				}
				codecName := fmt.Sprint(sm["codec_name"])
				if !stringSliceContains(tc.Subtitle.Codec, codecName) {
					continue
				}
				subIdx := toInt(sm["index"])
				if subIdx == 0 {
					continue
				}
				for _, c := range tc.Subtitle.Command {
					for a := range strings.FieldsSeq(c) {
						args = append(args, strings.ReplaceAll(a, "{subIndex}", strconv.Itoa(subIdx)))
					}
				}
				// ASS/SSA: also emit the raw track as a subs_<idx>.ass
				// sibling. The WebVTT twin above loses all styling; a client
				// with an on-device ASS renderer (JASSUB/libass — see
				// transcoding_fonts.go) fetches this instead and renders
				// with full styling + embedded fonts, no burn-in re-encode.
				// Rides the same process, so zero extra source reads.
				if isAdvancedTextSubtitle(codecName) {
					args = append(args,
						"-map", fmt.Sprintf("0:%d", subIdx), "-an", "-vn",
						"-c:s", "ass", "-flush_packets", "1",
						"-max_interleave_delta", "0", "-muxpreload", "0", "-muxdelay", "0",
						"-f", "ass", fmt.Sprintf("subs_%d.ass", subIdx))
				}
			}
		}
	}

	// Multi-audio shelves: copy every shelvable audio track into per-track
	// TS segments (reader input for hot switching). Same start numbering as
	// the video segments so shelf segment K covers playback segment K.
	if len(ctx.ShelfTracks) > 0 {
		shelfStart := 0
		if ctx.StartNum != nil {
			shelfStart = *ctx.StartNum
		} else if ctx.HLS.Seek > 0 {
			shelfStart = ctx.HLS.Seek / max(ctx.HLS.SegDur, 1)
		}
		args = appendAudioShelfOutputs(args, ctx, shelfStart)
	}

	// Output map commands.  When burn-in is active, rewrite the default
	// `-map 0:v:0` to `-map [vout]` so the filter_complex output reaches
	// the muxer instead of the raw video stream.
	audioIndex := max(ctx.Audio.Index, 0)
	if cmds, ok := tc.Command["output"]; ok {
		for _, c := range cmds {
			for a := range strings.FieldsSeq(c) {
				piece := strings.ReplaceAll(a, "{audio_index}", strconv.Itoa(audioIndex))
				if mapVideoLabel != "" && piece == "0:v:0" {
					piece = mapVideoLabel
				}
				args = append(args, piece)
			}
		}
	}

	// Suppress subtitle output streams unless we're explicitly extracting
	// them as separate WebVTT siblings.  Burn-in subs are already inside
	// the video frame; emitting them again as a sub stream would confuse
	// the player.
	if ctx.SubPlan.Strategy != StrategyExtract {
		args = append(args, "-sn")
	}

	// Video codec.
	args = svc.appendVideoCodec(args, ctx)

	// Cap output resolution for SW re-encodes (4K→1080p by default) so a
	// single libx264 job can sustain realtime on a CPU.
	args = svc.applyTranscodeDownscale(args, ctx)

	// Live re-encode: force keyframes at segment boundaries so ffmpeg can actually cut short
	// segments. Without a forced GOP, libx264 defaults to a ~250-frame (≈10s) GOP and the FIRST
	// segment can't close until the first later keyframe — a ~10s «нет картинки» startup AND a
	// ~10s window in which the idle-live watchdog would reap a not-yet-watched job (its output
	// dir RemoveAll'd out from under the live ffmpeg → «rename index.m3u8.tmp: No such file» /
	// live.m3u8 404). Aligned keyframes every SegDur also keep segments small — exactly what a
	// thin pipe needs (the whole point of IPTV «Эконом»). Only when we're actually re-encoding
	// video (a -c:v copy stream keeps the source's own GOP; we can't move its keyframes).
	// ★VOD тоже: без принудительных ключевых кадров x264 режет по своему GOP (250 кадров =
	// 10 с), а синтетический плейлист объявляет SegDur — плеер считает буфер по плейлисту и
	// промахивается перемоткой. Проверено на ffmpeg 9: с expr сегменты ровно SegDur, а после
	// рестарта с -ss отсчёт t в выражении идёт от точки seek, так что сетка не съезжает.
	if !sliceContainsPair(args, "-c:v", "copy") {
		seg := max(ctx.HLS.SegDur, 1)
		args = append(args, "-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%d)", seg))
	}

	// Audio codec.
	args = svc.appendAudioCodec(args, ctx)

	// HLS output.
	args = append(args, "-f", "hls")

	// HLS output commands.
	if cmds, ok := tc.HLS.Command["output"]; ok {
		for _, c := range cmds {
			args = append(args, strings.Fields(c)...)
		}
	}

	// Segment type.
	args = append(args, "-hls_segment_type")
	if ctx.HLS.FMP4 {
		args = append(args, "fmp4")
	} else {
		args = append(args, "mpegts")
		if cmds, ok := tc.HLS.Command["segment_mpegts"]; ok {
			for _, c := range cmds {
				args = append(args, strings.Fields(c)...)
			}
		}
		// Auto-detect bitstream filter for MPEG-TS based on video codec.
		// If video is being re-encoded (not copied), encoder outputs
		// Annex B format directly — no bsf needed.
		videoReencoded := !sliceContainsPair(args, "-c:v", "copy")
		args = appendMPEGTSBitstreamFilter(args, ctx.FFProbe, videoReencoded)
	}

	args = append(args, "-hls_time", strconv.Itoa(ctx.HLS.SegDur))

	// P3.F: fast-start hack — emit a short first segment so the player
	// gets enough bytes to start decoding within ~1 s instead of waiting
	// for a full hls_time-sized chunk.  hls_init_time landed in ffmpeg
	// 4.4; older builds ignore it without complaint, so the gate is just
	// "version high enough to know the flag exists".  Live streams keep
	// uniform segments because they use a sliding window and the first
	// segment churns out within seconds anyway.
	if !ctx.Live && !tc.DisableFastStart && svc.ffmpegMajorVersion >= 5 && ctx.HLS.SegDur > 1 {
		args = append(args, "-hls_init_time", "1")
	}

	if ctx.Live {
		args = append(args, "-hls_flags", "append_list+omit_endlist+delete_segments")
	}

	args = append(args, "-hls_list_size", strconv.Itoa(ctx.HLS.WinSize))

	// Start number.
	startNum := ctx.StartNum
	if startNum == nil && ctx.HLS.Seek > 0 {
		segDur := max(ctx.HLS.SegDur, 1)
		sn := ctx.HLS.Seek / segDur
		startNum = &sn
	}
	if startNum != nil {
		args = append(args, "-start_number", strconv.Itoa(*startNum))
	}

	// -master_pl_name writes a MASTER playlist named index.m3u8 — but the media output below is ALSO
	// index.m3u8, so for a SINGLE-rung job the master overwrites the media playlist and references
	// ITSELF (#EXT-X-STREAM-INF → index.m3u8). VOD is served via a generated/main.m3u8 so it never
	// reads this file, but the LIVE handler serves index.m3u8 DIRECTLY → a dead self-referential
	// master = the «live.m3u8 404 / нет картинки» in IPTV Эконом. Single-rung needs no master at all.
	if !ctx.Live {
		args = append(args, "-master_pl_name", "index.m3u8")
	}

	if ctx.HLS.FMP4 {
		args = append(args, "-hls_fmp4_init_filename", "init.mp4")
	} else if !ctx.Live {
		// «vod» finalizes the playlist (adds ENDLIST, disables segment deletion) — correct for VOD,
		// WRONG for an endless live channel (it would never rotate segments → unbounded growth).
		args = append(args, "-hls_playlist_type", "vod")
	}

	if ctx.HLS.FMP4 {
		args = append(args, "-hls_segment_filename", "seg_%05d.m4s")
	} else {
		args = append(args, "-hls_segment_filename", "seg_%05d.ts")
	}

	args = append(args, "-y", "index.m3u8")

	log.Debug().Strs("args", args).Msg("transcoding: ffmpeg args")

	cmd := exec.Command(tc.FFmpeg, args...)
	cmd.Dir = ctx.OutputDir
	// We need stderr for log pumping. Stdin is unused but keep it available for "q".
	cmd.Stdout = nil

	return cmd
}

// hlsCompatibleCodecs lists video codecs that can be stream-copied into HLS.
// Anything else must be re-encoded to h264 for browser playback.
var hlsCompatibleCodecs = map[string]bool{
	"h264": true, "hevc": true, "h265": true,
	"vp9": true, "av1": true,
}

func (svc *TranscodingService) appendVideoCodec(args []string, ctx transcodingContext) []string {
	tc := svc.cfg.Transcoding

	// Smart mode override — P0: remux / audio-only → always copy video.
	// sw/hw transcode → continue to the codec-aware logic below.
	switch ctx.Mode {
	case transcode.ModeRemux, transcode.ModeAudioOnly:
		return append(args, "-c:v", "copy")
	}

	// Probe codec + pixfmt up front so the 10-bit / HDR pre-flight in P3.D
	// can see what it's dealing with before the HW fast-path runs.
	codecName := ""
	pixFmt := ""
	streams, ok := ctx.FFProbe["streams"].([]any)
	if ok {
		for _, s := range streams {
			sm, _ := s.(map[string]any)
			if sm == nil || fmt.Sprint(sm["codec_type"]) != "video" {
				continue
			}
			codecName = fmt.Sprint(sm["codec_name"])
			pixFmt = fmt.Sprint(sm["pix_fmt"])
			break
		}
	}

	// P3.D: HEVC 10-bit / HDR auto-tonemap.  When the source is 10-/12-bit
	// (yuv420p10le, p010le, …) we MUST downconvert to 8-bit BT.709 before
	// any h264 encoder — h264 is 8-bit-only on every consumer device, and
	// many HW encoders silently produce green-tinted / black output if fed
	// 10-bit frames.  Skip when the smart-mode selector chose to copy
	// video (Native/Direct/Remux/AudioOnly): those paths bypass the encoder.
	is10Bit := is10BitPixFmt(pixFmt)
	transcodingVideo := ctx.Mode == transcode.ModeHWTranscode || ctx.Mode == transcode.ModeSWTranscode
	if is10Bit && transcodingVideo {
		w, h := extractVideoDimensions(ctx.FFProbe)
		br := transcode.PickVideoBitrate(w, h)
		if !ctx.DisableHW && svc.hwAccel.Active() {
			args = append(args, buildTonemapPrefilter(svc.hwAccel.Kind)...)
			if hwArgs := svc.hwAccel.buildHWEncoderArgsForTonemappedInput(br); len(hwArgs) > 0 {
				log.Debug().
					Str("kind", string(svc.hwAccel.Kind)).
					Str("pix_fmt", pixFmt).
					Int("width", w).Int("height", h).Int("bitrateKbps", br).
					Msg("transcoding: 10-bit input → CPU tonemap + HW encode")
				return append(args, hwArgs...)
			}
		}
		// SW tonemap fallback.  Use the operator-tunable Convert config
		// when available so deployments can swap in libplacebo / zscale
		// based filtergraphs without touching code.
		if tc.Convert.Command != nil {
			if cmds, ok := tc.Convert.Command["h264_yuv420p10le"]; ok && len(cmds) > 0 {
				log.Debug().Str("pix_fmt", pixFmt).Msg("transcoding: 10-bit input → SW tonemap (config-driven)")
				for _, c := range cmds {
					args = append(args, strings.Fields(c)...)
				}
				return args
			}
		}
		log.Debug().Str("pix_fmt", pixFmt).Msg("transcoding: 10-bit input → SW tonemap (built-in)")
		args = append(args, buildTonemapPrefilter("")...)
		return append(args,
			"-c:v", "libx264",
			"-preset", "veryfast",
			"-pix_fmt", "yuv420p",
			"-color_primaries", "bt709",
			"-color_trc", "bt709",
			"-colorspace", "bt709",
			"-color_range", "tv",
		)
	}

	// P1: HW encode fast path.  When HW is detected and active and the
	// smart mode selector chose to transcode video, emit the HW encoder
	// block with a bitrate derived from the source resolution.
	if transcodingVideo && (!ctx.DisableHW && svc.hwAccel.Active()) {
		w, h := extractVideoDimensions(ctx.FFProbe)
		// Econom: the picture is downscaled to ctx.MaxHeight below, so size the encoder bitrate for
		// the SMALLER output, not the source — otherwise a 480p econom stream carries a 6 Mbps 1080p
		// bitrate and defeats the whole point (fewer bytes for a thin pipe).
		if ctx.MaxHeight > 0 && h > ctx.MaxHeight {
			if h > 0 {
				w = w * ctx.MaxHeight / h
			}
			h = ctx.MaxHeight
		}
		br := transcode.PickVideoBitrate(w, h)
		if hwArgs := svc.hwAccel.buildHWEncoderArgs(br); len(hwArgs) > 0 {
			log.Debug().
				Str("kind", string(svc.hwAccel.Kind)).
				Int("width", w).Int("height", h).Int("bitrateKbps", br).
				Msg("transcoding: emitting HW encoder args")
			return append(args, hwArgs...)
		}
	}

	if !ok || len(streams) == 0 {
		if !tc.Convert.TranscodeVideo || tc.Convert.Codec == nil || tc.Convert.Command == nil {
			// Best-effort / missing probe: if the smart mode selector
			// explicitly asked for sw-transcode, do it even without probe.
			if ctx.Mode == transcode.ModeSWTranscode || ctx.Mode == transcode.ModeHWTranscode {
				return append(args, "-c:v", "libx264", "-preset", "veryfast", "-crf", "23")
			}
			return append(args, "-c:v", "copy")
		}
	}

	// Force re-encoding for codecs incompatible with HLS
	// (mpeg4/XVID, vp8, mpeg2, wmv, etc.) — browsers can't play them.
	needConvert := false
	if codecName != "" && !hlsCompatibleCodecs[codecName] {
		needConvert = true
		log.Info().Str("codec", codecName).Msg("transcoding: codec not HLS-compatible, forcing re-encode to h264")
	}

	// IPTV Эконом: a per-job MaxHeight means we WANT to shrink the picture for a thin pipe. Even a
	// HLS-compatible codec (h264 — most IPTV) must be RE-ENCODED, else -c:v copy keeps the source
	// resolution/bitrate and the downscale below no-ops → «Эконом» does nothing.
	if ctx.MaxHeight > 0 {
		if _, h := extractVideoDimensions(ctx.FFProbe); h <= 0 || h > ctx.MaxHeight {
			needConvert = true
		}
	}

	// Also check user-configured convert rules.
	if tc.Convert.TranscodeVideo && tc.Convert.Codec != nil && tc.Convert.Command != nil {
		if stringSliceContains(tc.Convert.Codec, codecName) {
			needConvert = true
		}
		if stringSliceContains(tc.Convert.Codec, pixFmt) {
			needConvert = true
		}
		if stringSliceContains(tc.Convert.Codec, codecName+"_"+pixFmt) {
			needConvert = true
		}
	}

	if !needConvert {
		return append(args, "-c:v", "copy")
	}

	// Find best matching convert command from config.
	if tc.Convert.Command != nil {
		cmds := tc.Convert.Command["default"]
		for _, key := range []string{codecName + "_" + pixFmt, pixFmt, codecName} {
			if c, ok := tc.Convert.Command[key]; ok {
				cmds = c
				break
			}
		}
		if len(cmds) > 0 {
			for _, c := range cmds {
				args = append(args, strings.Fields(c)...)
			}
			return args
		}
	}

	// Fallback: basic h264 encoding if no convert commands configured.
	return append(args, "-c:v", "libx264", "-preset", "veryfast", "-crf", "23")
}

// applyTranscodeDownscale caps the output height of a SW video re-encode at
// Transcoding.MaxTranscodeHeight (default 1080).  A single 4K libx264 encode
// is ~4× a 1080p one and cannot run realtime on a CPU — left uncapped it
// starves every other job, the player times out, retries, and the box melts.
//
// It is a post-step over the args built by appendVideoCodec so it works for
// every re-encode path (built-in libx264, the config-driven HDR-tonemap
// command, …).  It is a no-op when:
//   - the cap is 0 (disabled — only sensible with a HW encoder),
//   - video is stream-copied (no -c:v copy job re-encodes),
//   - the source is already at/below the cap,
//   - a -filter_complex (subtitle burn-in) owns the video graph.
//
// Injection rules:
//   - existing `-vf scale=<opts>` without explicit dimensions (the HDR path's
//     scale=in_color_matrix=…) → splice the target resolution in:
//     scale=-2:<cap>:<opts>,
//   - existing `-vf` not starting with scale= → prepend scale=-2:<cap>,
//   - no `-vf` → append one.
func (svc *TranscodingService) applyTranscodeDownscale(args []string, ctx transcodingContext) []string {
	capH := svc.cfg.Transcoding.MaxTranscodeHeight
	if ctx.MaxHeight > 0 {
		capH = ctx.MaxHeight // per-job override (IPTV «Эконом»)
	}
	if capH <= 0 {
		return args
	}
	if sliceContainsPair(args, "-c:v", "copy") {
		return args // stream-copy — no encoder to scale
	}
	if slices.Contains(args, "-filter_complex") {
		return args // burn-in owns the video filtergraph
	}
	_, h := extractVideoDimensions(ctx.FFProbe)
	if h <= 0 || h <= capH {
		return args
	}
	scale := "scale=-2:" + strconv.Itoa(capH)

	for i := 0; i+1 < len(args); i++ {
		if args[i] != "-vf" {
			continue
		}
		v := args[i+1]
		rest, isScale := strings.CutPrefix(v, "scale=")
		if isScale {
			// Already dimensioned (starts with a digit or -1/-2)? leave it.
			if len(rest) > 0 && (rest[0] == '-' || (rest[0] >= '0' && rest[0] <= '9')) {
				return args
			}
			args[i+1] = scale + ":" + rest
		} else {
			args[i+1] = scale + "," + v
		}
		log.Debug().Int("cap", capH).Int("src_h", h).Str("vf", args[i+1]).
			Msg("transcoding: downscaling SW re-encode to height cap")
		return args
	}

	log.Debug().Int("cap", capH).Int("src_h", h).Msg("transcoding: downscaling SW re-encode to height cap")
	return append(args, "-vf", scale)
}

// appendMPEGTSBitstreamFilter adds the correct -bsf:v filter for MPEG-TS
// segments based on the video codec detected by ffprobe.
//
// Only h264 and hevc need bitstream filters to convert from mp4 to Annex B
// format.  Other codecs (mpeg4/XVID, vp8, etc.) are either:
//   - re-encoded to h264 (encoder outputs Annex B directly), or
//   - copied without needing a bsf (already compatible with mpegts).
func appendMPEGTSBitstreamFilter(args []string, probe map[string]any, videoIsReencoded bool) []string {
	// If video is being re-encoded (not copied), the encoder outputs
	// Annex B format directly — no bsf needed.
	if videoIsReencoded {
		return args
	}

	// Check if a -bsf:v was already set (e.g. from segment_mpegts config).
	if slices.Contains(args, "-bsf:v") {
		return args
	}

	// Determine source video codec from ffprobe.
	codec := ""
	if streams, ok := probe["streams"].([]any); ok {
		for _, s := range streams {
			sm, _ := s.(map[string]any)
			if sm == nil || fmt.Sprint(sm["codec_type"]) != "video" {
				continue
			}
			codec = fmt.Sprint(sm["codec_name"])
			break
		}
	}

	switch codec {
	case "h264", "":
		// h264 from mp4/mkv container needs Annex B conversion.
		// Empty codec (no probe data) — assume h264 as most common.
		return append(args, "-bsf:v", "h264_mp4toannexb")
	case "hevc", "h265":
		return append(args, "-bsf:v", "hevc_mp4toannexb")
	default:
		// mpeg4 (XVID/DivX), vp8, vp9, etc. — no bsf needed for mpegts.
		return args
	}
}

// findAudioStream returns the ffprobe stream map for the Nth audio stream
// (0-based relative index among codec_type=="audio" streams).
func findAudioStream(streams []any, relIdx int) map[string]any {
	audioCount := 0
	for _, s := range streams {
		sm, _ := s.(map[string]any)
		if sm == nil {
			continue
		}
		if fmt.Sprint(sm["codec_type"]) != "audio" {
			continue
		}
		if audioCount == relIdx {
			return sm
		}
		audioCount++
	}
	return nil
}

func (svc *TranscodingService) appendAudioCodec(args []string, ctx transcodingContext) []string {
	tc := svc.cfg.Transcoding

	audioIdx := max(ctx.Audio.Index, 0)

	// Smart mode override — P0.
	switch ctx.Mode {
	case transcode.ModeRemux:
		return append(args, "-c:a", "copy")
	case transcode.ModeAudioOnly, transcode.ModeHWTranscode, transcode.ModeSWTranscode:
		// Fall through to the transcoding code below, skipping the
		// "should we copy?" check — smart mode already decided.
		bitrate := fmt.Sprintf("%dk", clamp(ctx.Audio.BitrateKbps, 32, 512))
		args = append(args, "-c:a", "aac", "-b:a", bitrate)
		if ctx.Audio.Stereo {
			args = append(args, "-ac", "2")
		}
		return args
	}

	streams, _ := ctx.FFProbe["streams"].([]any)
	audioStream := findAudioStream(streams, audioIdx)

	// Check if we should copy audio.
	needConvert := true
	if audioStream != nil {
		codecName := fmt.Sprint(audioStream["codec_name"])
		channels := fmt.Sprint(audioStream["channels"])

		if stringSliceContains(ctx.Audio.CodecCopy, codecName) {
			needConvert = false
		}
		if stringSliceContains(ctx.Audio.CodecCopy, channels) {
			needConvert = false
		}
		if stringSliceContains(ctx.Audio.CodecCopy, codecName+"_"+channels) {
			needConvert = false
		}
	}

	if !needConvert {
		return append(args, "-c:a", "copy")
	}

	// Audio transcode commands.
	cmds := tc.Audio.CommandTransc["default"]

	// Try to find codec/channel-specific command.
	if audioStream != nil {
		codecName := fmt.Sprint(audioStream["codec_name"])
		channels := fmt.Sprint(audioStream["channels"])

		for _, key := range []string{codecName + "_" + channels, channels, codecName} {
			if c, ok := tc.Audio.CommandTransc[key]; ok {
				cmds = c
				break
			}
		}
	}

	stereoStr := "2"
	if !ctx.Audio.Stereo {
		stereoStr = "1"
	}
	bitrateStr := fmt.Sprintf("%dk", clamp(ctx.Audio.BitrateKbps, 32, 512))

	for _, c := range cmds {
		for a := range strings.FieldsSeq(c) {
			a = strings.ReplaceAll(a, "{stereo}", stereoStr)
			a = strings.ReplaceAll(a, "{bitrateKbps}", bitrateStr)
			args = append(args, a)
		}
	}
	return args
}

// ---------------------------------------------------------------------------
// Merge options
// ---------------------------------------------------------------------------

func (svc *TranscodingService) mergeHLS(req *HLSOpts) transcodingHLSCtx {
	tc := svc.cfg.Transcoding

	opts := transcodingHLSCtx{
		SegDur:  tc.HLS.SegDur,
		WinSize: tc.HLS.WinSize,
		FMP4:    tc.HLS.FMP4,
	}

	if req != nil {
		if req.Seek > 0 {
			opts.Seek = req.Seek
		}
		if req.SegDur > 1 {
			opts.SegDur = req.SegDur
		}
		if req.WinSize > 5 {
			opts.WinSize = req.WinSize
		}
		if req.FMP4 != nil {
			opts.FMP4 = *req.FMP4
		}
	}

	if opts.SegDur < 1 {
		opts.SegDur = 1
	}
	if opts.WinSize < 5 {
		opts.WinSize = 5
	}

	return opts
}

func (svc *TranscodingService) mergeAudio(req *reqAudioOpts) transcodingAudioCtx {
	tc := svc.cfg.Transcoding

	opts := transcodingAudioCtx{
		Index:       0,
		BitrateKbps: tc.Audio.BitrateKbps,
		Stereo:      tc.Audio.Stereo,
		CodecCopy:   tc.Audio.CodecCopy,
	}

	if req != nil {
		if req.Index >= 0 {
			opts.Index = req.Index
		}
		if req.BitrateKbps > 0 && req.BitrateKbps <= 512 {
			opts.BitrateKbps = req.BitrateKbps
		}
		if req.Stereo != nil {
			opts.Stereo = *req.Stereo
		}
	}

	return opts
}

// ---------------------------------------------------------------------------
// Background goroutines
// ---------------------------------------------------------------------------

func (svc *TranscodingService) pumpStderr(job *TranscodingJob) {
	if job.stderrPipe == nil {
		return
	}
	scanner := bufio.NewScanner(job.stderrPipe)
	for scanner.Scan() {
		line := scanner.Text()
		job.AppendLog(line)
		// Log errors from ffmpeg to server log
		if strings.Contains(line, "Error") || strings.Contains(line, "error") ||
			strings.Contains(line, "Invalid") || strings.Contains(line, "No such") ||
			strings.Contains(line, "Permission denied") || strings.Contains(line, "failed") {
			log.Warn().Str("streamId", job.StreamID).Str("line", line).Msg("transcoding: ffmpeg stderr error")
		}
		// P1: HW-specific failure patterns → disable HW accel for the rest
		// of the session so subsequent jobs don't retry the same broken
		// path (e.g. user unplugged dGPU, ran out of VRAM, stale driver).
		if svc.hwAccel != nil && !job.Context.DisableHW && svc.hwAccel.Active() && isHWFailureLine(line) {
			svc.hwAccel.Disable(line)
		}
	}
}

// isHWFailureLine returns true when an ffmpeg stderr line looks like a
// HW-acceleration specific failure that should disable the HW path.
func isHWFailureLine(line string) bool {
	l := strings.ToLower(line)
	patterns := []string{
		"cannot load libva",
		"cannot load cuda",
		"device creation failed",
		"failed to create vaapi",
		"no nvenc capable devices",
		"nvenc: cannot load nvEncodeAPI",
		"h264_nvenc.*initialization failed",
		"h264_qsv.*initialization failed",
		"mfx session: failed",
		"videotoolbox: session create failed",
		"h264_videotoolbox: could not initialize",
	}
	for _, p := range patterns {
		if strings.Contains(l, p) {
			return true
		}
	}
	return false
}

func (svc *TranscodingService) waitExit(job *TranscodingJob) {
	if job.Cmd == nil {
		return
	}
	_ = job.Cmd.Wait()
	// Close torrent stdin pipe to release reader resources.
	if job.Context.StdinPipe != nil {
		job.Context.StdinPipe.Close()
	}
	exitCode := -1
	if job.Cmd.ProcessState != nil {
		exitCode = job.Cmd.ProcessState.ExitCode()
		atomic.StoreInt32(&job.exitCode, int32(exitCode))
	}
	log.Info().Str("streamId", job.StreamID).Int("exitCode", exitCode).Str("outputDir", job.OutputDir).Msg("transcoding: ffmpeg process exited")

	// P2.4: auto-restart on unplanned crashes.
	//
	// If ffmpeg died with a non-zero exit code and the cleanup path did
	// not intentionally stop it (stopRequested==0), resume from the last
	// segment we produced.  This masks transient decoder glitches, CDN
	// hiccups on HTTP sources, and torrent read stalls — the client sees
	// a brief pause on the next segment instead of a fatal playback error.
	//
	// Guard: no restart for live jobs (they're torn down on every stop),
	// no restart when the process exited cleanly (0), no restart when we
	// already burned through 3 restarts in the last 60 seconds.
	planned := atomic.LoadInt32(&job.stopRequested) == 1
	if !planned && !job.Context.Live && exitCode != 0 && svc.tryAutoRestart(job) {
		return
	}

	// For live jobs, auto-cleanup on process exit.
	if job.Context.Live {
		svc.cleanup(job)
	}
}

// inputNeverOpened reports whether ffmpeg's stderr shows the INPUT failed to
// open at all ("Error opening input[ file[s]]: ..." — connect/read timeout,
// refused, DNS). Distinct from a mid-stream death: an input that opened and
// later died IS worth an auto-restart resume; one that never opened is not.
func inputNeverOpened(logLines []string) bool {
	if len(logLines) > 40 {
		logLines = logLines[len(logLines)-40:]
	}
	for _, ln := range logLines {
		if strings.Contains(ln, "Error opening input") {
			return true
		}
	}
	return false
}

// tryAutoRestart rehydrates a crashed VOD job at the last delivered
// segment.  Returns true if a new ffmpeg process was successfully spawned
// and the caller should skip normal post-exit cleanup.
//
// Budget: 3 restarts per 60-second sliding window, keyed by streamID so
// the counter survives SeekAsync (which replaces the underlying struct).
//
// P3.E: this is also where escalation lands.  Before deciding to retry
// with the same mode, we classify the captured stderr (HW failure, codec
// refused, color-space mismatch, IO blip) and possibly bump the pipeline
// to a more expensive but more reliable mode.  The cascade is
//
//	remux  → audio-only  → sw-transcode
//	hw-transcode + HW fail → sw-transcode (with DisableHW for this job)
//
// without escalation, identical-mode retries would burn the whole 3-call
// budget on the same broken pipeline.
func (svc *TranscodingService) tryAutoRestart(job *TranscodingJob) bool {
	svc.restartMu.Lock()
	now := time.Now()
	// Drop restart marks older than 60s.
	kept := svc.restartTimes[job.StreamID][:0]
	for _, t := range svc.restartTimes[job.StreamID] {
		if now.Sub(t) < 60*time.Second {
			kept = append(kept, t)
		}
	}
	if len(kept) >= 3 {
		svc.restartTimes[job.StreamID] = kept
		svc.restartMu.Unlock()
		log.Warn().Str("streamId", job.StreamID).Msg("transcoding: auto-restart budget exhausted, giving up")
		return false
	}
	kept = append(kept, now)
	svc.restartTimes[job.StreamID] = kept
	attempts := len(kept)
	svc.restartMu.Unlock()

	// Compute resume seek from the last segment ffmpeg wrote.
	lastIdx := job.LastSegmentIndex()
	if lastIdx < 0 {
		// The INPUT never opened AND nothing was produced: a retry re-dials the
		// same dead/unreachable source and burns another full rw_timeout (now up
		// to 90s for TorrServer srcs) — three of those in a row is 4.5 minutes
		// of a hung spinner (prod 2026-07-16: LAN TorrServers + dead torrents
		// stacked 9 parallel retry-storms). Mode escalation can't fix a source
		// that won't answer — fail the job immediately so the playlist handler
		// surfaces an error the player can act on.
		if inputNeverOpened(job.SnapshotLog()) {
			log.Warn().Str("streamId", job.StreamID).Msg("transcoding: input never opened and no segments written — not restarting (source dead/unreachable)")
			return false
		}
		// Never produced a single segment → nothing to resume from.
		// Still useful when the failure was at startup time (HW init,
		// codec refusal) — skip the resume seek but keep the escalation.
		log.Warn().Str("streamId", job.StreamID).Msg("transcoding: no segments written, auto-restart will start from 0")
	}
	segDur := max(job.Context.HLS.SegDur, 1)
	resumeSec := 0
	resumeSeg := 0
	resumeExact := 0.0
	if lastIdx >= 0 {
		resumeSec = (lastIdx + 1) * segDur
		resumeSeg = lastIdx + 1
		if segs := job.segMapNow(); segs != nil && videoCopied(job.Context.Mode) {
			resumeExact = segs.start(resumeSeg)
			resumeSec = int(resumeExact)
		}
	}

	// P3.E: classify last 200 stderr lines and decide whether to escalate.
	logLines := job.SnapshotLog()
	if len(logLines) > 200 {
		logLines = logLines[len(logLines)-200:]
	}
	errClass := classifyFFmpegStderr(logLines)
	decision := escalateMode(job.Context.Mode, errClass)

	log.Info().
		Str("streamId", job.StreamID).
		Int("lastSeg", lastIdx).
		Int("resumeSec", resumeSec).
		Int("attempts", attempts).
		Str("errorClass", string(errClass)).
		Str("currentMode", string(job.Context.Mode)).
		Str("nextMode", string(decision.NewMode)).
		Bool("disableHW", decision.DisableHWForJob).
		Str("reason", decision.Reason).
		Msg("transcoding: auto-restart after crash")

	if decision.NewMode != job.Context.Mode || decision.DisableHWForJob {
		// Mode escalation path — rebuild ctx with the new mode + HW gate,
		// then respawn ffmpeg from the resume point.
		ok, errMsg := svc.respawnWithMode(job, resumeSec, resumeSeg, decision.NewMode, decision.DisableHWForJob)
		if !ok {
			log.Warn().Str("streamId", job.StreamID).Str("err", errMsg).Msg("transcoding: escalated restart failed")
			return false
		}
		return true
	}

	// No escalation — fall through to the existing same-mode resume.
	ok, errMsg := svc.seekAsyncExact(job.StreamID, resumeSec, resumeExact, &resumeSeg)
	if !ok {
		log.Warn().Str("streamId", job.StreamID).Str("err", errMsg).Msg("transcoding: auto-restart failed")
		return false
	}
	return true
}

// respawnWithMode is SeekAsync's escalation-aware sibling: it rebuilds the
// job context with a new pipeline mode (and optional per-job HW disable)
// before spawning the new ffmpeg process.  Used by tryAutoRestart when
// the previous run's failure indicates the *mode* itself is at fault.
//
// Mirrors SeekAsync's lifecycle exactly — kill old process, re-open
// torrent pipe, mkdir, createProcess, register — only newCtx differs.
func (svc *TranscodingService) respawnWithMode(job *TranscodingJob, resumeSec, resumeSeg int, newMode transcode.TranscodingMode, disableHW bool) (bool, string) {
	newCtx := job.Context
	newCtx.HLS.Seek = resumeSec
	newCtx.HLS.SeekExact = 0
	// Плейлист уже объявлен по карте — продолжать надо ровно с границы сегмента resumeSeg,
	// иначе стык после рестарта наложится на уже отданное.
	if segs := job.segMapNow(); segs != nil && resumeSeg > 0 && videoCopied(job.Context.Mode) {
		newCtx.HLS.SeekExact = segs.start(resumeSeg)
		newCtx.HLS.Seek = int(newCtx.HLS.SeekExact)
	}
	if resumeSeg > 0 {
		sn := resumeSeg
		newCtx.StartNum = &sn
	}
	newCtx.Mode = newMode
	if disableHW {
		newCtx.DisableHW = true
	}

	svc.killProcess(job)

	if newCtx.torrentHash != "" && torrsIsInProcess() {
		newCtx.StdinPipe = nil
		if pipe, _, _ := torrentOpenPipeByHash(newCtx.torrentHash, newCtx.torrentFileIdx); pipe != nil {
			newCtx.StdinPipe = pipe
			newCtx.Source = "pipe:0"
		} else {
			log.Warn().Str("hash", newCtx.torrentHash).Msg("transcoding: respawn — failed to re-open torrent pipe")
			return false, "Failed to re-open torrent stream"
		}
	}

	if err := os.MkdirAll(newCtx.OutputDir, 0o755); err != nil {
		return false, "Failed to create output directory"
	}

	cmd := svc.createProcess(newCtx)
	if newCtx.StdinPipe != nil {
		cmd.Stdin = newCtx.StdinPipe
	}
	stderrPipe, pipeErr := cmd.StderrPipe()
	if pipeErr != nil {
		if newCtx.StdinPipe != nil {
			newCtx.StdinPipe.Close()
		}
		return false, fmt.Sprintf("Failed to create stderr pipe: %v", pipeErr)
	}
	if err := cmd.Start(); err != nil {
		if newCtx.StdinPipe != nil {
			newCtx.StdinPipe.Close()
		}
		return false, fmt.Sprintf("Failed to start ffmpeg: %v", err)
	}

	newJob := newTranscodingJob(job.ID, job.StreamID, job.OutputDir, cmd, newCtx)
	newJob.copyKeyframesFrom(job) // карта та же — источник не менялся
	newJob.stderrPipe = stderrPipe
	newJob.sched = job.sched // reuses the same slot → release to the same pool
	newJob.Mode = newMode
	newJob.ProfileLabel = job.ProfileLabel
	newJob.KnownIssues = job.KnownIssues

	svc.mu.Lock()
	svc.jobs[job.ID] = newJob
	svc.mu.Unlock()

	go svc.pumpStderr(newJob)
	go svc.idleWatchdog(newJob)
	go svc.windowWatchdog(newJob)
	go svc.waitExit(newJob)
	return true, ""
}

func (svc *TranscodingService) idleWatchdog(job *TranscodingJob) {
	tc := svc.cfg.Transcoding

	idleTimeout := time.Duration(math.Max(180, float64(tc.IdleTimeoutSec))) * time.Second
	idleLive := time.Duration(math.Max(20, float64(tc.IdleTimeoutLive))) * time.Second

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-job.Cancelled():
			return
		case <-svc.stopCh:
			return
		case <-ticker.C:
			if job.HasExited() {
				return
			}

			elapsed := time.Since(job.LastAccess())

			if job.Context.Live {
				if tc.IdleTimeoutLive == -1 {
					continue
				}
				if elapsed > idleLive {
					log.Debug().Str("stream", job.StreamID).Msg("transcoding: idle live job stopped")
					svc.stopJob(job, true)
					return
				}
			} else {
				if tc.IdleTimeoutSec == -1 {
					continue
				}
				if elapsed > idleTimeout {
					log.Debug().Str("stream", job.StreamID).Msg("transcoding: idle VOD job stopped")
					svc.stopJob(job, true)
					return
				}
			}
		}
	}
}

// windowAction is the pure decision of the produce-ahead pacer. lookahead is
// how many segments the producer is ahead of the viewer (producedIdx -
// servedIdx). When not paused and that exceeds high → pause; when paused and
// the viewer has drained it back to low or below → resume. high <= 0 disables.
func windowAction(lookahead, high, low int, paused bool) (pause, resume bool) {
	if high <= 0 {
		return false, false
	}
	if paused {
		return false, lookahead <= low
	}
	return lookahead >= high, false
}

// highestSegmentOnDisk returns the largest seg_NNNNN index ffmpeg has written
// to the job's output dir, or -1 when none exist (e.g. multi-rung variants
// live in v<N>/ subdirs and aren't paced here — the window simply stays off).
func (svc *TranscodingService) highestSegmentOnDisk(job *TranscodingJob) int {
	entries, err := os.ReadDir(job.OutputDir)
	if err != nil {
		return -1
	}
	highest := -1
	for _, e := range entries {
		m := svc.segmentFileRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		if idx, err := strconv.Atoi(m[1]); err == nil && idx > highest {
			highest = idx
		}
	}
	return highest
}

// windowWatchdog (P4) bounds how far a VOD transcode runs ahead of the viewer.
// Steady-state playback stays under the high watermark on its own (-readrate
// caps fill speed), so this fires precisely when the viewer is NOT consuming —
// paused, buffering, seeking, or gone — and SIGSTOP-freezes ffmpeg there: 0
// CPU, and the torrent/HTTP read pauses with it (no wasted disk/bandwidth).
// When the viewer drains the buffer back to the low watermark it SIGCONT's.
// The huge gap between watermarks (default 22/12 ≈ 132s/72s) guarantees the
// paused, possibly half-written frontier segment is never requested while the
// process is frozen.
func (svc *TranscodingService) windowWatchdog(job *TranscodingJob) {
	tc := svc.cfg.Transcoding
	high := tc.WindowAheadHigh
	if high <= 0 || job.Context.Live {
		return // disabled, or live (already paced by -re + sliding window)
	}
	low := tc.WindowAheadLow
	if low < 0 || low >= high {
		low = high / 2
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-job.Cancelled():
			return
		case <-svc.stopCh:
			return
		case <-ticker.C:
			if job.HasExited() {
				return
			}
			produced := svc.highestSegmentOnDisk(job)
			if produced < 0 {
				continue
			}
			// LastSegmentIndex() is the highest segment the player has
			// fetched — a heartbeat-free proxy for its position. While the
			// player keeps fetching, this advances and the lookahead stays
			// small; the moment it pauses, the frontier outruns it.
			lookahead := produced - job.LastSegmentIndex()
			pause, resume := windowAction(lookahead, high, low, job.windowPaused.Load())
			if pause {
				if pauseProc(job.Cmd) {
					job.windowPaused.Store(true)
					log.Debug().Str("stream", job.StreamID).Int("ahead", lookahead).Msg("transcoding: produce-window full → ffmpeg paused")
				}
			} else if resume {
				if resumeProc(job.Cmd) {
					job.windowPaused.Store(false)
					log.Debug().Str("stream", job.StreamID).Int("ahead", lookahead).Msg("transcoding: produce-window drained → ffmpeg resumed")
				}
			}
		}
	}
}

func (svc *TranscodingService) segmentCleanupLoop() {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	// Wait 1 minute before first cleanup.
	select {
	case <-time.After(1 * time.Minute):
	case <-svc.stopCh:
		return
	}

	for {
		select {
		case <-svc.stopCh:
			return
		case <-ticker.C:
			svc.cleanupSegments()
		}
	}
}

func (svc *TranscodingService) cleanupSegments() {
	if !atomic.CompareAndSwapInt32(&svc.segCleanupRunning, 0, 1) {
		return
	}
	defer atomic.StoreInt32(&svc.segCleanupRunning, 0)

	tc := svc.cfg.Transcoding
	if !tc.Playlist.DeleteSegments {
		return
	}

	svc.mu.RLock()
	jobs := make([]*TranscodingJob, 0, len(svc.jobs))
	for _, j := range svc.jobs {
		jobs = append(jobs, j)
	}
	svc.mu.RUnlock()

	for _, job := range jobs {
		if job.Context.Live {
			continue
		}
		lastIdx := job.LastSegmentIndex()
		if lastIdx <= 0 {
			continue
		}

		// P2.3: respect the slowest client's playback position.  Without
		// this the cleanup loop can delete a segment that a client on a
		// slow link hasn't fetched yet, causing a fragLoadError and a
		// visible rebuffer.  We keep a safety window of 6 segments
		// behind minPos so the player's backward-seek buffer survives.
		segDur := max(job.Context.HLS.SegDur, 1)
		cutoffIdx := lastIdx - 1
		if minPos, have := job.MinPosition(); have {
			minSeg := int(minPos) / segDur
			safetyWindow := 6
			posCutoff := minSeg - safetyWindow
			if posCutoff < cutoffIdx {
				cutoffIdx = posCutoff
			}
		}
		if cutoffIdx <= 0 {
			continue
		}

		entries, err := os.ReadDir(job.OutputDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			// Multi-audio dirs: trim shelf (s_%05d.ts) and rendition
			// (seg_%05d.m4s) segments with the same watched-cutoff as the
			// video — otherwise a 9-track BDRip accumulates shelves forever.
			if e.IsDir() && (strings.HasPrefix(name, audioShelfDirPrefix) || strings.HasPrefix(name, audioRendDirPrefix)) {
				sub := filepath.Join(job.OutputDir, name)
				if subEntries, err := os.ReadDir(sub); err == nil {
					for _, se := range subEntries {
						sn := se.Name()
						var idxStr string
						if m := svc.segmentFileRe.FindStringSubmatch(sn); m != nil {
							idxStr = m[1]
						} else if strings.HasPrefix(sn, "s_") && strings.HasSuffix(sn, ".ts") {
							idxStr = strings.TrimSuffix(strings.TrimPrefix(sn, "s_"), ".ts")
						}
						if idxStr == "" {
							continue
						}
						if idx, err := strconv.Atoi(idxStr); err == nil && idx < cutoffIdx {
							_ = os.Remove(filepath.Join(sub, sn))
						}
					}
				}
				continue
			}
			m := svc.segmentFileRe.FindStringSubmatch(name)
			if m == nil {
				continue
			}
			idx, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			if idx >= cutoffIdx {
				continue
			}
			_ = os.Remove(filepath.Join(job.OutputDir, name))
		}
	}

	// Cancel AAC readers nobody is fetching from (viewer switched away).
	idleAudioReaderSweep()
}

// ---------------------------------------------------------------------------
// Dead job reaper — auto-cleanup of exited (dead) VOD jobs
// ---------------------------------------------------------------------------

// deadJobReaper periodically scans for exited VOD jobs and removes them after
// a grace period (60 seconds). This prevents "Не отвечает" ghost entries in
// the admin dashboard.
func (svc *TranscodingService) deadJobReaper() {
	const gracePeriod = 60 * time.Second
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	tick := 0
	for {
		select {
		case <-svc.stopCh:
			return
		case <-ticker.C:
			svc.reapDeadJobs(gracePeriod)
			// Раз в минуту — ffmpeg, чьих заданий больше нет в учёте (transcoding_orphans_unix.go).
			if tick++; tick%4 == 0 {
				svc.reapOrphanProcs()
			}
		}
	}
}

func (svc *TranscodingService) reapDeadJobs(grace time.Duration) {
	svc.mu.RLock()
	var dead []*TranscodingJob
	for _, j := range svc.jobs {
		if j.HasExited() && time.Since(j.LastAccess()) > grace {
			dead = append(dead, j)
		}
	}
	svc.mu.RUnlock()

	for _, j := range dead {
		log.Info().Str("jobId", j.ID).Str("streamId", j.StreamID).
			Int("exitCode", j.ExitCode()).
			Msg("transcoding: reaping dead job")
		svc.cleanup(j)
	}
}

// CleanupDead removes all exited jobs from the jobs map immediately
// (no grace period). Returns the count of removed jobs.
func (svc *TranscodingService) CleanupDead() int {
	svc.mu.RLock()
	var dead []*TranscodingJob
	for _, j := range svc.jobs {
		if j.HasExited() {
			dead = append(dead, j)
		}
	}
	svc.mu.RUnlock()

	for _, j := range dead {
		log.Info().Str("jobId", j.ID).Str("streamId", j.StreamID).
			Msg("transcoding: admin cleanup dead job")
		svc.cleanup(j)
	}
	return len(dead)
}

// StopJobByID kills and cleans up a specific job by ID. Returns true if found.
func (svc *TranscodingService) StopJobByID(jobID string) bool {
	svc.mu.RLock()
	job, ok := svc.jobs[jobID]
	svc.mu.RUnlock()
	if !ok {
		return false
	}
	svc.stopJob(job, true)
	return true
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func appendCommandArgs(args []string, cmds map[string][]string, key string) []string {
	if cmds == nil {
		return args
	}
	cc, ok := cmds[key]
	if !ok {
		return args
	}
	for _, c := range cc {
		args = append(args, strings.Fields(c)...)
	}
	return args
}

func getHeader(headers map[string]string, key string) string {
	if headers == nil {
		return ""
	}
	for k, v := range headers {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}

func sanitizeHeader(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	clean := strings.ReplaceAll(value, "\r", "")
	clean = strings.ReplaceAll(clean, "\n", "")
	if strings.TrimSpace(clean) == "" {
		return fallback
	}
	return clean
}

func stringSliceContains(slice []string, s string) bool {
	for _, item := range slice {
		if strings.EqualFold(item, s) {
			return true
		}
	}
	return false
}

// sliceContainsPair checks if args contain consecutive elements [key, value].
// transcodingDirectPidtorRewrite adds the magnet directly via Go API (no HTTP)
// and returns a stream URL for ffmpeg.  Used in in-process mode to avoid the
// HTTP POST timeout that blocks on metadata resolution.
func transcodingDirectPidtorRewrite(cfg config.Config, source string) string {
	srv := getTorrsServer()
	if srv == nil {
		return ""
	}

	_, after, ok := strings.Cut(source, "/lite/pidtor/s")
	if !ok {
		return ""
	}

	hash := after
	queryPart := ""
	if before, after, ok := strings.Cut(after, "?"); ok {
		hash = before
		queryPart = after
	}
	if hash == "" {
		return ""
	}

	trQS := pidtorExtractTRFromQuery(queryPart)
	tsid := 1
	for part := range strings.SplitSeq(queryPart, "&") {
		if strings.HasPrefix(part, "tsid=") {
			fmt.Sscanf(part[5:], "%d", &tsid)
		}
	}

	magnet := fmt.Sprintf("magnet:?xt=urn:btih:%s", hash)
	if trQS != "" {
		magnet += "&" + trQS
	}

	_, err := srv.Add(magnet, "", "", "", false)
	if err != nil {
		log.Warn().Err(err).Str("hash", hash).Msg("transcoding: pidtor direct add magnet failed")
		return ""
	}

	streamURL := fmt.Sprintf("%s/stream?link=%s&index=%d&play", tsDirectBaseURL(cfg), hash, tsid)
	log.Info().Str("hash", hash).Str("url", streamURL).Msg("transcoding: pidtor direct rewrite")
	return streamURL
}

// transcodingPidtorOpenPipe opens a direct torrent reader for pidtor URLs,
// bypassing HTTP entirely. Returns (reader, hash, fileIdx) on success.
// The reader feeds ffmpeg via stdin (pipe:0).
func transcodingPidtorOpenPipe(source string) (io.ReadCloser, string, int) {
	srv := getTorrsServer()
	if srv == nil {
		return nil, "", 0
	}

	_, after, ok := strings.Cut(source, "/lite/pidtor/s")
	if !ok {
		return nil, "", 0
	}

	hash := after
	queryPart := ""
	if before, after, ok := strings.Cut(after, "?"); ok {
		hash = before
		queryPart = after
	}
	if hash == "" {
		return nil, "", 0
	}

	trQS := pidtorExtractTRFromQuery(queryPart)
	tsid := 1
	for part := range strings.SplitSeq(queryPart, "&") {
		if strings.HasPrefix(part, "tsid=") {
			fmt.Sscanf(part[5:], "%d", &tsid)
		}
	}

	magnet := fmt.Sprintf("magnet:?xt=urn:btih:%s", hash)
	if trQS != "" {
		magnet += "&" + trQS
	}

	fileIdx := tsid - 1
	if fileIdx < 0 {
		fileIdx = 0
	}

	log.Info().Str("hash", hash).Int("tsid", tsid).Int("fileIdx", fileIdx).Msg("transcoding: pidtor pipe — adding magnet")

	_, err := srv.Add(magnet, "", "", "", false)
	if err != nil {
		log.Warn().Err(err).Str("hash", hash).Msg("transcoding: pidtor pipe — add magnet failed")
		return nil, "", 0
	}

	return torrentOpenPipeByHash(hash, fileIdx)
}

// torrentStreamOpenPipe opens a direct torrent reader for a /stream URL,
// bypassing HTTP. Extracts hash and index from URL query params.
func torrentStreamOpenPipe(source string) (io.ReadCloser, string, int) {
	srv := getTorrsServer()
	if srv == nil {
		return nil, "", 0
	}

	parsed, err := url.Parse(source)
	if err != nil {
		return nil, "", 0
	}

	hash := parsed.Query().Get("link")
	if hash == "" {
		return nil, "", 0
	}

	rawIdx, _ := strconv.Atoi(parsed.Query().Get("index"))
	fileIdx := rawIdx - 1
	if fileIdx < 0 {
		fileIdx = 0
	}

	return torrentOpenPipeByHash(hash, fileIdx)
}

// torrentOpenPipeByHash opens a torrent reader by hash and 0-based file index.
// The returned ReadCloser feeds ffmpeg via stdin.
func torrentOpenPipeByHash(hash string, fileIdx int) (io.ReadCloser, string, int) {
	srv := getTorrsServer()
	if srv == nil {
		return nil, "", 0
	}

	reader, size, filename, err := srv.Stream(hash, fileIdx)
	if err != nil {
		log.Warn().Err(err).Str("hash", hash).Int("fileIdx", fileIdx).Msg("transcoding: torrent pipe — stream failed")
		return nil, "", 0
	}

	log.Info().Str("hash", hash).Str("file", filename).Int64("size", size).Int("fileIdx", fileIdx).Msg("transcoding: torrent pipe — reader opened")

	// Wrap as ReadCloser. anacrolix/torrent Reader implements io.Closer.
	if rc, ok := reader.(io.ReadCloser); ok {
		return rc, hash, fileIdx
	}
	return io.NopCloser(reader), hash, fileIdx
}

func sliceContainsPair(args []string, key, value string) bool {
	for i := range len(args) - 1 {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}

func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case string:
		i, _ := strconv.Atoi(n)
		return i
	}
	return 0
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// detectFFmpegVersion runs `ffmpeg -version` once and parses the major version.
//
// Three formats handled:
//  1. Stable release:  "ffmpeg version 7.1.1 Copyright …"    → 7
//  2. Distro patch:    "ffmpeg version 4.2.7-0ubuntu0.1 …"  → 4
//  3. Master nightly:  "ffmpeg version N-118527-g0fabc9876b-20240315 …" → fallback
//
// The third format (BtbN nightlies, https://github.com/BtbN/FFmpeg-Builds)
// has no semantic major in the version string at all — the leading token
// is `N-<commit>-<build>`.  We fall back to mapping the libavutil major
// version printed in the subsequent lines: 56→4, 57→5, 58→6, 59→7, 60→8.
// libavutil bumps its major exactly once per ffmpeg release, so this
// mapping is stable and lets us run on master builds without losing the
// compatibility audit.
func (svc *TranscodingService) detectFFmpegVersion() {
	ffmpegPath := svc.cfg.Transcoding.FFmpeg
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, ffmpegPath, "-version").Output()
	if err != nil {
		log.Warn().Err(err).Msg("transcoding: failed to detect ffmpeg version")
		return
	}

	body := string(out)
	firstLine := strings.SplitN(body, "\n", 2)[0]
	log.Info().Str("version_line", firstLine).Msg("transcoding: ffmpeg version detected")

	// Path 1: stable release / distro patch — first digit after "version ".
	if v := parseFFmpegStableMajor(firstLine); v > 0 {
		svc.ffmpegMajorVersion = v
		log.Info().Int("major", v).Str("source", "version_string").
			Msg("transcoding: ffmpeg major version")
		return
	}

	// Path 2: master nightly — derive from libavutil major.
	if v := parseFFmpegMajorFromLibavutil(body); v > 0 {
		svc.ffmpegMajorVersion = v
		log.Info().Int("major", v).Str("source", "libavutil_mapping").
			Msg("transcoding: ffmpeg major version (mapped from libavutil)")
		return
	}

	log.Warn().Str("first_line", firstLine).
		Msg("transcoding: could not infer ffmpeg major version — compatibility report will treat as unknown")
}

// parseFFmpegStableMajor extracts the major version from a "ffmpeg version
// X.Y.Z" / "ffmpeg version X.Y.Z-distro" first line.  Also handles BtbN's
// release-tag prefix `n7.1-...` which carries the major number after a
// single `n` letter.  Returns 0 when the version token starts with
// neither (e.g. nightly `N-<commit>-<build>`).
func parseFFmpegStableMajor(firstLine string) int {
	// Order matters: try `n<digits>` (BtbN release tag) before plain
	// `<digits>` (stable release / distro patch) so we don't accidentally
	// pick up a stray digit elsewhere on the line.
	re := regexp.MustCompile(`(?i)\bversion\s+n?(\d+)\b`)
	m := re.FindStringSubmatch(firstLine)
	if len(m) >= 2 {
		if v, err := strconv.Atoi(m[1]); err == nil {
			return v
		}
	}
	return 0
}

// parseFFmpegMajorFromLibavutil maps libavutil's major number to the
// corresponding ffmpeg major.  Mapping table reflects the canonical
// libavutil bumps over the ffmpeg release history:
//
//	libavutil 56 → ffmpeg 4 (4.0 in 2018)
//	libavutil 57 → ffmpeg 5 (5.0 in 2022)
//	libavutil 58 → ffmpeg 6 (6.0 in 2023)
//	libavutil 59 → ffmpeg 7 (7.0 in 2024)
//	libavutil 60 → ffmpeg 8 (8.0 in 2025+)
//
// Callers receive 0 when the body doesn't carry a libavutil line.
func parseFFmpegMajorFromLibavutil(body string) int {
	re := regexp.MustCompile(`(?m)^\s*libavutil\s+(\d+)\.`)
	m := re.FindStringSubmatch(body)
	if len(m) < 2 {
		return 0
	}
	libutil, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	// Map: libutil = libavMajor; ffmpegMajor = libutil - 52 for libutil ≥ 56.
	// (56→4, 57→5, 58→6, 59→7, 60→8.)
	if libutil < 56 {
		// Pre-4.0 ffmpeg — caller treats anything below 5 as legacy
		// anyway, so flatten to "very old" (1) so the gates fire correctly.
		return 1
	}
	return libutil - 52
}

// ── карта ключевых кадров (keyframes.go) ──

// ffprobePath — ffprobe рядом с настроенным ffmpeg, иначе из PATH (то же правило, что у runFFProbeUncached).
func (svc *TranscodingService) ffprobePath() string {
	tc := svc.cfg.Transcoding
	if tc.FFmpeg != "" && tc.FFmpeg != "ffmpeg" {
		candidate := filepath.Join(filepath.Dir(tc.FFmpeg), "ffprobe")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "ffprobe"
}

// fastStartInitTime — включён ли -hls_init_time 1 для этого контекста (та же формула, что в
// createProcess): от него зависит целевая длина сегментов hlsenc, см. hlsGrid.
func (svc *TranscodingService) fastStartInitTime(ctx transcodingContext) float64 {
	tc := svc.cfg.Transcoding
	if !ctx.Live && !tc.DisableFastStart && svc.ffmpegMajorVersion >= 5 && ctx.HLS.SegDur > 1 {
		return 1
	}
	return 0
}

// startKeyframeMap строит карту в фоне: VOD, одиночный rung, с пробой. Для stream-copy по ней
// строятся плейлист и seek, для re-encode она нужна только превью перемотки (trickplay).
func (svc *TranscodingService) startKeyframeMap(job *TranscodingJob) {
	ctx := job.Context
	if ctx.Live || ctx.MultiRung || ctx.BestEffort || ctx.FFProbe == nil {
		job.setKeyframes(nil, nil)
		return
	}
	go func() {
		var torrent io.ReadSeeker
		if ctx.torrentHash != "" && torrsIsInProcess() {
			if srv := getTorrsServer(); srv != nil {
				if rs, _, _, err := srv.Stream(ctx.torrentHash, ctx.torrentFileIdx); err == nil {
					torrent = rs
					if c, ok := rs.(io.Closer); ok {
						defer c.Close()
					}
				}
			}
		}
		cctx, cancel := context.WithTimeout(context.Background(), keyframeExtractTimeout)
		defer cancel()
		km, err := extractKeyframes(cctx, ctx.Source, ctx.UserAgent, ctx.Referer, ctx.FFProbe, torrent, svc.ffprobePath())
		if err != nil {
			log.Debug().Err(err).Str("streamId", job.StreamID).Msg("transcoding: keyframe map unavailable — uniform playlist")
			job.setKeyframes(nil, nil)
			return
		}
		segs := hlsGrid(km, float64(max(ctx.HLS.SegDur, 1)), svc.fastStartInitTime(ctx), float64(ffprobeDuration(ctx.FFProbe)))
		job.setKeyframes(km, segs)
		n := 0
		if segs != nil {
			n = segs.count()
		}
		log.Info().Str("streamId", job.StreamID).Str("source", km.Source).Int("keyframes", len(km.Times)).
			Int("segments", n).Bool("copy", videoCopied(ctx.Mode)).Msg("transcoding: keyframe map ready")
	}()
}

func (j *TranscodingJob) setKeyframes(km *keyframeMap, segs *hlsSegmentMap) {
	j.kfMu.Lock()
	j.kf, j.kfSegs = km, segs
	done, once := j.kfDone, j.kfOnce
	j.kfMu.Unlock()
	if once != nil && done != nil {
		once.Do(func() { close(done) })
	}
}

// keyframes — карта без ожидания (nil, пока строится).
func (j *TranscodingJob) keyframes() *keyframeMap {
	j.kfMu.Lock()
	defer j.kfMu.Unlock()
	return j.kf
}

// segMapNow — границы сегментов без ожидания.
func (j *TranscodingJob) segMapNow() *hlsSegmentMap {
	j.kfMu.Lock()
	defer j.kfMu.Unlock()
	return j.kfSegs
}

// segMap ждёт карту не дольше wait: плейлист отдаётся после первого сегмента (секунды), к этому
// моменту Cues обычно давно прочитаны; не успела — работаем по равномерной сетке.
func (j *TranscodingJob) segMap(wait time.Duration) *hlsSegmentMap {
	j.kfMu.Lock()
	done := j.kfDone
	j.kfMu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-time.After(wait):
			return nil
		}
	}
	return j.segMapNow()
}

// copyKeyframesFrom делит карту (и её канал готовности) с предыдущим воплощением джобы.
func (j *TranscodingJob) copyKeyframesFrom(o *TranscodingJob) {
	o.kfMu.Lock()
	km, segs, done, once := o.kf, o.kfSegs, o.kfDone, o.kfOnce
	o.kfMu.Unlock()
	j.kfMu.Lock()
	j.kf, j.kfSegs, j.kfDone, j.kfOnce = km, segs, done, once
	j.kfMu.Unlock()
}
