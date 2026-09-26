package transcodesvc

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

// ---------------------------------------------------------------------------
// Transcoding scheduler — P2.1.
//
// Two knobs limit resource consumption:
//
//  1. Concurrency semaphore (MaxConcurrent).  Start() Acquires a slot
//     before spawning ffmpeg; cleanup() Releases it.  When the pool is
//     full subsequent Start() calls return a typed "busy" error, which
//     the HTTP handler translates to 503 Service Unavailable + Retry-After.
//
//  2. Disk budget (DiskBudgetMB).  A background loop walks TempRoot every
//     30 seconds; if total usage exceeds the configured budget, the oldest
//     completed jobs are evicted until we drop below 80% of the limit.
//
// When MaxConcurrent <= 0 we derive a default of max(1, CPUs/2) on startup
// — this is sane for SW encoding and won't thermally park the machine.
// ---------------------------------------------------------------------------

// ErrSchedulerBusy is returned by Start() when the concurrency slot pool
// is exhausted.  The HTTP layer turns this into a 503 + Retry-After: 5.
var ErrSchedulerBusy = errors.New("scheduler busy: max concurrent transcoding jobs reached")

// ErrTorrentNoData: a TorrServer-backed source (pidtor / /ts) delivered no bytes
// within the patient probe budget — the torrent's metadata/pieces never arrived
// from the swarm. Surfaced as 502 by the start handlers so players fail over
// instead of waiting on a hung best-effort ffmpeg.
var ErrTorrentNoData = errors.New("torrent gave no data: metadata/seeds unavailable")

// availableMemBytesFn is the memory probe, indirected so tests can inject a
// value (the real availableMemBytes is /proc/meminfo, Linux-only).
var availableMemBytesFn = availableMemBytes

// evictMinAge protects freshly-created job dirs from disk-budget eviction. A
// starting job is not yet in svc.jobs (so not in activeDirs) for a brief
// window; without this gate the eviction could delete its dir mid-launch →
// ffmpeg "Failed to open segment 'init.mp4'". 2 min comfortably covers ffmpeg
// startup (probe + many subtitle outputs) before it writes the first segment.
const evictMinAge = 2 * time.Minute

// constructorWipeMinAge gates the NewTranscodingService startup sweep the same way: only job
// dirs untouched for this long are treated as orphans of a dead run. Anything younger may be a
// LIVE job of another lampac-go instance sharing the runtime dir (or a job racing a config
// reload) — an active job refreshes its dir mtime with every segment write, so it never ages
// past this. Truly orphaned dirs just wait one gate period and are reaped on the next start.
const constructorWipeMinAge = 15 * time.Minute

// transcodingScheduler owns the concurrency semaphore and the disk
// budget enforcement goroutine.  One instance per TranscodingService.
type transcodingScheduler struct {
	// slots is a counting semaphore implemented as a buffered channel.
	// Send = acquire, receive = release.  Closed in Stop().
	slots chan struct{}

	// Capacity is the effective concurrency limit after applying defaults.
	Capacity int

	// diskBudgetBytes is the hard cap on TempRoot size.  Zero disables
	// budget enforcement entirely.
	diskBudgetBytes int64
	tempRoot        string

	// minFreeMemBytes: reject a new job (503) when OS available memory is
	// below this. Turns a global OOM (kernel reaps a random running ffmpeg
	// mid-playback → death spiral of 404s + client restarts) into a clean
	// admission refusal. Zero disables the gate.
	minFreeMemBytes int64

	// Counters exposed via /transcoding/stats.
	acquired  int64 // atomic
	released  int64 // atomic
	queueHit  int64 // atomic (number of 503 rejections — slot pool exhausted)
	evicted   int64 // atomic (number of disk-budget evictions)
	memReject int64 // atomic (number of 503 rejections — low memory)
}

func newTranscodingScheduler(maxConcurrent, diskBudgetMB, minFreeMemMB int, tempRoot string) *transcodingScheduler {
	cap := maxConcurrent
	if cap <= 0 {
		cap = runtime.NumCPU() / 2
		if cap < 1 {
			cap = 1
		}
	}
	log.Info().Int("capacity", cap).Int("disk_budget_mb", diskBudgetMB).Int("min_free_mem_mb", minFreeMemMB).Msg("transcoding: scheduler initialised")
	return &transcodingScheduler{
		slots:           make(chan struct{}, cap),
		Capacity:        cap,
		minFreeMemBytes: int64(minFreeMemMB) * 1024 * 1024,
		diskBudgetBytes: int64(diskBudgetMB) * 1024 * 1024,
		tempRoot:        tempRoot,
	}
}

// Acquire takes one concurrency slot.  Non-blocking: returns ErrSchedulerBusy
// immediately when the pool is exhausted.  Callers MUST pair successful
// Acquire with Release().
func (s *transcodingScheduler) Acquire() error {
	// Memory admission gate — refuse cleanly before the kernel OOM-reaps a
	// running ffmpeg. A single 4K-HEVC/DoVi decode+tonemap+ABR job peaks at
	// several GB, so on a box without headroom a slot being free doesn't mean
	// RAM is. Checked before taking the slot so a rejection costs nothing.
	if s.minFreeMemBytes > 0 {
		if avail := availableMemBytesFn(); avail >= 0 && avail < s.minFreeMemBytes {
			atomic.AddInt64(&s.memReject, 1)
			log.Warn().Int64("avail_mb", avail/(1024*1024)).Int64("min_mb", s.minFreeMemBytes/(1024*1024)).Msg("transcoding: low memory, rejecting job")
			return ErrSchedulerBusy
		}
	}
	select {
	case s.slots <- struct{}{}:
		atomic.AddInt64(&s.acquired, 1)
		return nil
	default:
		atomic.AddInt64(&s.queueHit, 1)
		return ErrSchedulerBusy
	}
}

// Release returns one slot to the pool.  Safe to call even if the
// underlying channel is closed (we recover from the panic).
func (s *transcodingScheduler) Release() {
	defer func() { _ = recover() }()
	select {
	case <-s.slots:
		atomic.AddInt64(&s.released, 1)
	default:
		// Slot imbalance — should never happen.  Log and move on.
		log.Warn().Msg("transcoding: scheduler release without matching acquire")
	}
}

// Available returns (free, capacity).
func (s *transcodingScheduler) Available() (int, int) {
	return cap(s.slots) - len(s.slots), cap(s.slots)
}

// Stats returns a JSON-shaped counter snapshot for /transcoding/stats.
func (s *transcodingScheduler) Stats() map[string]any {
	free, total := s.Available()
	return map[string]any{
		"capacity":     total,
		"in_use":       total - free,
		"available":    free,
		"acquired":     atomic.LoadInt64(&s.acquired),
		"released":     atomic.LoadInt64(&s.released),
		"rejections":   atomic.LoadInt64(&s.queueHit),
		"mem_rejected": atomic.LoadInt64(&s.memReject),
		"mem_available": func() int64 {
			if a := availableMemBytes(); a >= 0 {
				return a
			}
			return 0
		}(),
		"disk_evicted": atomic.LoadInt64(&s.evicted),
		"disk_budget":  s.diskBudgetBytes,
	}
}

// ---------------------------------------------------------------------------
// Disk budget enforcement
// ---------------------------------------------------------------------------

// diskBudgetLoop periodically measures TempRoot usage and evicts the
// oldest top-level job directories when we blow the budget.  Runs until
// stopCh closes.
func (svc *TranscodingService) diskBudgetLoop() {
	sched := svc.scheduler
	if sched == nil || sched.diskBudgetBytes <= 0 || sched.tempRoot == "" {
		return
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-svc.stopCh:
			return
		case <-ticker.C:
			svc.enforceDiskBudget()
		}
	}
}

func (svc *TranscodingService) enforceDiskBudget() {
	sched := svc.scheduler
	if sched == nil || sched.diskBudgetBytes <= 0 {
		return
	}
	used, err := dirSize(sched.tempRoot)
	if err != nil {
		log.Debug().Err(err).Str("path", sched.tempRoot).Msg("transcoding: disk budget measure failed")
		return
	}
	if used < sched.diskBudgetBytes {
		return
	}

	log.Warn().
		Int64("used_mb", used/1024/1024).
		Int64("budget_mb", sched.diskBudgetBytes/1024/1024).
		Msg("transcoding: disk budget exceeded, evicting oldest completed jobs")

	// Enumerate completed jobs (those no longer in the active map) by
	// scanning TempRoot and filtering out directories that still own an
	// active TranscodingJob.
	svc.mu.RLock()
	activeDirs := make(map[string]bool, len(svc.jobs))
	for _, j := range svc.jobs {
		activeDirs[filepath.Base(j.OutputDir)] = true
	}
	svc.mu.RUnlock()

	type candidate struct {
		path    string
		modTime time.Time
		size    int64
	}
	var candidates []candidate
	entries, _ := os.ReadDir(sched.tempRoot)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if activeDirs[e.Name()] {
			continue
		}
		if e.Name() == probeCacheDirName || e.Name() == ocrCacheDirName {
			// Never evict the long-lived cache directories.
			continue
		}
		// Evict ONLY our own job dirs (newJobID = 32 hex chars). When temp_root points at a
		// shared dir like /tmp, this scan sees FOREIGN dirs — prod 2026-07-02: the YouTube
		// muxer's /tmp/yt-mux-* (huge 4K/8K muxes blew the disk budget) got evicted ~2 min
		// after completion, right under the playing client → every segment 404 → «после N
		// сегментов 404, потом 502/503». Not ours → not our business.
		if !isTranscodeJobDirName(e.Name()) {
			continue
		}
		full := filepath.Join(sched.tempRoot, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		// Never evict a freshly-created dir: a job whose ffmpeg has launched but
		// hasn't yet been registered in svc.jobs (the brief window between
		// MkdirAll and svc.jobs[id]=job) is NOT in activeDirs, so without this it
		// could be deleted out from under a starting ffmpeg → "Failed to open
		// segment 'init.mp4' / No such file or directory" → 502. The mtime gate
		// also makes us agree with mtime-based external cleaners.
		if time.Since(info.ModTime()) < evictMinAge {
			continue
		}
		sz, _ := dirSize(full)
		candidates = append(candidates, candidate{full, info.ModTime(), sz})
	}

	// Oldest first.
	for i := 0; i < len(candidates); i++ {
		for j := i + 1; j < len(candidates); j++ {
			if candidates[j].modTime.Before(candidates[i].modTime) {
				candidates[i], candidates[j] = candidates[j], candidates[i]
			}
		}
	}

	// Target: drop below 80% of the budget.
	target := int64(float64(sched.diskBudgetBytes) * 0.80)
	for _, c := range candidates {
		if used < target {
			break
		}
		if err := os.RemoveAll(c.path); err != nil {
			continue
		}
		used -= c.size
		atomic.AddInt64(&sched.evicted, 1)
		log.Info().Str("path", c.path).Int64("freed_mb", c.size/1024/1024).Msg("transcoding: evicted stale job dir")
	}

	// Still over budget with nothing safe to evict → the active/young jobs alone
	// exceed the cap. We deliberately DON'T nuke them (that's the init.mp4 ENOENT
	// bug). Warn loudly so the operator raises disk_budget_mb, lowers
	// max_concurrent_jobs, or enables 4K→1080p downscale instead.
	if used >= sched.diskBudgetBytes {
		log.Warn().
			Int64("used_mb", used/1024/1024).
			Int64("budget_mb", sched.diskBudgetBytes/1024/1024).
			Int("evictable_dirs", len(candidates)).
			Msg("transcoding: over disk budget but active/young jobs hold it — raise disk_budget_mb or lower concurrency (NOT evicting live jobs)")
	}
}

// dirSize returns the aggregate size of all regular files under root.
// Returns 0 on error.
func dirSize(root string) (int64, error) {
	var total int64
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // best-effort: skip unreadable entries
		}
		if info != nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

// pendingReleaseOnce ensures a scheduled job only releases its slot once
// even if both cleanup() and the finalizer fire.  Zero value is usable.
type pendingReleaseOnce struct {
	once sync.Once
	sch  *transcodingScheduler
}

func (p *pendingReleaseOnce) Release() {
	p.once.Do(func() {
		if p.sch != nil {
			p.sch.Release()
		}
	})
}
