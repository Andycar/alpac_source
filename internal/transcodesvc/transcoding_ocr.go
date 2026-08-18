package transcodesvc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

// ---------------------------------------------------------------------------
// PGS / DVB / DVD subtitle OCR pipeline — P3.M.
//
// Burn-in (P3.C) is a heavyweight workaround for clients that can't render
// bitmap subtitles: it forces full video re-encode with the subs filter
// chain.  CPU cost compounds with every replay because the cache only
// holds segments, not the burn-in result for re-use across sessions.
//
// OCR is a one-time conversion: extract the bitmap sub stream as PNG
// frames with their timestamps, run tesseract over each frame, assemble
// WebVTT with the recovered text + cue timing, write to a content-keyed
// cache.  Subsequent plays of the same source skip burn-in entirely —
// the planner picks StrategyExtract pointed at the cached WebVTT.
//
// Implementation phases:
//
//   M1 (this commit):
//     - Tesseract availability detection at service startup.
//     - OCRCache: content-addressed disk cache keyed by source URL hash
//       + sub stream abs index.  GetOCRWebVTT(srcKey) returns either
//       the cached WebVTT path or the empty string.
//     - Planner check: when bitmap codec + tesseract available + cache
//       hit, return StrategyOCR (a synthetic extract path that uses the
//       cached file).  When cache miss, fall back to burn-in for this
//       session AND queue an OCR job for next time.
//
//   M2 (future): the actual OCR runner — ffmpeg + tesseract subprocess
//     pipeline, written into the cache atomically.  Stubbed in this
//     commit but documented end-to-end so the wiring is clear.
//
// The cache is intentionally simple — disk-only, deduped by hash, read
// on every Start().  No eviction yet (covered by the existing TempRoot
// disk-budget enforcer if/when we want it).
// ---------------------------------------------------------------------------

// ocrCacheDirName is the subdirectory under TempRoot where cached
// OCR-derived WebVTT files live.  Layout: <TempRoot>/ocr_cache/<hash>.vtt
// where <hash> is SHA-256 of (srcURL + ":" + subAbsIndex).
const ocrCacheDirName = "ocr_cache"

// OCRCache provides content-addressed lookup for OCR-derived WebVTT
// subtitle files.  Read path is pure filesystem; write path is by the
// OCR runner (P3.M2) which is wired separately.
type OCRCache struct {
	dir string

	hits   atomic.Int64
	misses atomic.Int64

	// pendingMu guards the in-flight set so a second client clicking
	// the same source doesn't race a second OCR run for the same key.
	pendingMu sync.Mutex
	pending   map[string]chan struct{}
}

// NewOCRCache creates the cache directory under tempRoot.  Returns nil
// when tempRoot is empty (testing) or the directory can't be created.
func NewOCRCache(tempRoot string) *OCRCache {
	if tempRoot == "" {
		return nil
	}
	dir := filepath.Join(tempRoot, ocrCacheDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Warn().Err(err).Str("dir", dir).Msg("ocr cache: failed to create dir")
		return nil
	}
	return &OCRCache{
		dir:     dir,
		pending: make(map[string]chan struct{}),
	}
}

// Key derives the cache key for a (source URL, subtitle abs-index) pair.
// Stable across runs, no collisions on different episodes of the same
// series since we hash the full URL.
func (c *OCRCache) Key(srcURL string, subAbsIndex int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", srcURL, subAbsIndex)))
	return hex.EncodeToString(h[:])
}

// PathFor returns the absolute path for a key's WebVTT — does NOT check
// for file existence.  Use Lookup() for the existence-check version.
func (c *OCRCache) PathFor(key string) string {
	if c == nil || c.dir == "" {
		return ""
	}
	return filepath.Join(c.dir, key+".vtt")
}

// Lookup returns the cached WebVTT file path when it exists, or "" when
// the cache is empty for this key.  Increments hit/miss counters.
func (c *OCRCache) Lookup(srcURL string, subAbsIndex int) string {
	if c == nil {
		return ""
	}
	key := c.Key(srcURL, subAbsIndex)
	path := c.PathFor(key)
	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		c.hits.Add(1)
		return path
	}
	c.misses.Add(1)
	return ""
}

// Stats returns a JSON-friendly snapshot of cache counters for /stats.
func (c *OCRCache) Stats() map[string]any {
	if c == nil {
		return map[string]any{"enabled": false}
	}
	hits := c.hits.Load()
	misses := c.misses.Load()
	total := hits + misses
	rate := 0.0
	if total > 0 {
		rate = float64(hits) / float64(total)
	}
	c.pendingMu.Lock()
	pending := len(c.pending)
	c.pendingMu.Unlock()
	return map[string]any{
		"enabled":  true,
		"hits":     hits,
		"misses":   misses,
		"hit_rate": rate,
		"pending":  pending,
	}
}

// AcquirePending returns true on the first call for a given key; subsequent
// callers see false and can wait on the returned channel.  Used by the
// runner to ensure exactly one OCR job per (srcURL, subIdx) is in flight.
//
// The caller MUST call ReleasePending(key) once done (success OR failure).
func (c *OCRCache) AcquirePending(key string) (acquired bool, wait <-chan struct{}) {
	if c == nil {
		return false, nil
	}
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	if ch, ok := c.pending[key]; ok {
		return false, ch
	}
	ch := make(chan struct{})
	c.pending[key] = ch
	return true, ch
}

// ReleasePending closes the in-flight channel and drops the entry.
// Idempotent — safe to call from a deferred handler even if AcquirePending
// returned false (in that case it's a no-op because the entry isn't ours).
func (c *OCRCache) ReleasePending(key string) {
	if c == nil {
		return
	}
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	if ch, ok := c.pending[key]; ok {
		close(ch)
		delete(c.pending, key)
	}
}

// ---------------------------------------------------------------------------
// Tesseract availability detection
// ---------------------------------------------------------------------------

// TesseractInfo holds the result of startup detection.  When Available
// is false, the planner falls back to burn-in for bitmap subs.
type TesseractInfo struct {
	Available bool
	Path      string
	Version   string // e.g. "5.3.0"
	Reason    string // populated when Available is false
}

// detectTesseract probes the host for a working tesseract binary.  Best-
// effort — failures are silent (Reason is filled for diagnostics).
func detectTesseract() *TesseractInfo {
	path, err := exec.LookPath("tesseract")
	if err != nil {
		return &TesseractInfo{Available: false, Reason: "tesseract not in PATH"}
	}
	cctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, path, "--version").CombinedOutput()
	if err != nil {
		return &TesseractInfo{Available: false, Path: path, Reason: fmt.Sprintf("--version failed: %v", err)}
	}
	// First line is something like "tesseract 5.3.0".
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	parts := strings.Fields(line)
	version := ""
	if len(parts) >= 2 {
		version = parts[1]
	}
	return &TesseractInfo{
		Available: true,
		Path:      path,
		Version:   version,
	}
}

// ---------------------------------------------------------------------------
// Planner integration helpers
// ---------------------------------------------------------------------------

// canPromoteToOCR returns true when a bitmap subtitle can be served
// through the OCR-extract path instead of burn-in.  Three conditions
// must hold:
//
//   - tesseract is available (detected at startup)
//   - the source is HTTP/HTTPS (not pipe:0 — the OCR runner needs to
//     re-read the source for sub extraction; pipe:0 is consumed once)
//   - the OCR cache already has the WebVTT for this stream
//
// The third condition is what makes Phase 1 useful: cache hits skip
// burn-in entirely.  Cache misses still go through burn-in for *this*
// session; Phase 2 will queue the OCR runner so the next session hits
// the cache.
func canPromoteToOCR(tesseract *TesseractInfo, ocrCache *OCRCache, srcURL string, sub *SubtitleStream, sourceIsPipe bool) bool {
	if tesseract == nil || !tesseract.Available {
		return false
	}
	if ocrCache == nil {
		return false
	}
	if sub == nil || !isBitmapSubtitleCodec(sub.Codec) {
		return false
	}
	if sourceIsPipe {
		return false
	}
	if ocrCache.Lookup(srcURL, sub.AbsIndex) == "" {
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// OCR runner stub (P3.M2)
//
// runOCR is the entry point for the background extraction pipeline.  In
// this commit it's a placeholder that simply errors out with "not
// implemented" — wired up so callers can be plumbed today and the actual
// subprocess chain lands in a follow-up.
// ---------------------------------------------------------------------------

// ErrOCRNotImplemented is returned by runOCR until P3.M2 ships.
var ErrOCRNotImplemented = errors.New("ocr runner not yet implemented (P3.M2)")

// runOCRStub is a placeholder for the future subprocess pipeline.  It
// MUST honour the AcquirePending contract — the planner relies on
// ReleasePending firing so concurrent identical sessions don't deadlock.
//
// Concrete Phase 2 plan documented in the function body so the wiring
// stays inline with the contract.
func (svc *TranscodingService) runOCRStub(srcURL string, headers map[string]string, sub *SubtitleStream) error {
	if svc.ocrCache == nil || svc.tesseract == nil || !svc.tesseract.Available {
		return ErrOCRNotImplemented
	}
	key := svc.ocrCache.Key(srcURL, sub.AbsIndex)
	acquired, wait := svc.ocrCache.AcquirePending(key)
	if !acquired {
		// Another goroutine is already running OCR for this source; just
		// wait for it to finish so we don't spawn a duplicate.
		<-wait
		return nil
	}
	defer svc.ocrCache.ReleasePending(key)

	// Phase 2 outline (NOT yet implemented):
	//
	//   1. workDir := filepath.Join(svc.cfg.Transcoding.TempRoot,
	//                                "ocr_work", key)
	//   2. ffmpeg subprocess:
	//        ffmpeg -i <srcURL> -map 0:<absIndex> -c copy
	//               -f rawvideo workDir/sub.bin
	//      OR for sup format:
	//        ffmpeg -i <srcURL> -map 0:s:<relIdx>
	//               -c:s copy -f sup workDir/sub.sup
	//   3. Decode the bitmap stream into PNG frames with timestamps:
	//        ffmpeg -i workDir/sub.sup -an -vn -f image2
	//               -vf "select='eq(pict_type,I)'" workDir/frame_%05d.png
	//      (in practice we use a custom approach because PGS isn't a
	//       standard image stream — use a Go-side decoder or external
	//       sup2image tool)
	//   4. tesseract on each frame to extract text:
	//        tesseract workDir/frame_%05d.png stdout -l rus+eng
	//   5. Assemble WebVTT cues from the timestamps + recovered text.
	//   6. Atomic write: tmp file → rename to svc.ocrCache.PathFor(key)
	//   7. RemoveAll(workDir)
	//
	// Failure handling: any step error → log warning, leave cache empty,
	// return error.  Caller falls back to burn-in for this session.

	return ErrOCRNotImplemented
}

// ---------------------------------------------------------------------------
// WebVTT timestamp helpers (used by Phase 2 runner — exposed now so tests
// can validate the format without waiting for the runner).
// ---------------------------------------------------------------------------

// WebVTTTimestamp formats a duration in WebVTT cue-timing format
// (HH:MM:SS.mmm).  Used by the OCR runner when assembling cues from
// extracted frame timestamps.
func WebVTTTimestamp(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	totalMs := d.Milliseconds()
	hours := totalMs / 3_600_000
	totalMs -= hours * 3_600_000
	minutes := totalMs / 60_000
	totalMs -= minutes * 60_000
	seconds := totalMs / 1_000
	millis := totalMs - seconds*1_000
	return fmt.Sprintf("%02d:%02d:%02d.%03d", hours, minutes, seconds, millis)
}

// AssembleWebVTT takes a list of (start, end, text) cues and returns the
// WebVTT file body.  Empty list → header-only file (still valid VTT).
type OCRCue struct {
	Start time.Duration
	End   time.Duration
	Text  string
}

func AssembleWebVTT(cues []OCRCue) string {
	// Sort cues by start to keep output deterministic even when the OCR
	// runner produces them out of order (e.g. parallel page workers).
	sorted := append([]OCRCue(nil), cues...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })

	var sb strings.Builder
	sb.WriteString("WEBVTT\n\n")
	for i, c := range sorted {
		text := strings.TrimSpace(c.Text)
		if text == "" {
			continue
		}
		fmt.Fprintf(&sb, "%d\n%s --> %s\n%s\n\n",
			i+1,
			WebVTTTimestamp(c.Start),
			WebVTTTimestamp(c.End),
			text,
		)
	}
	return sb.String()
}
