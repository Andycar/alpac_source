package transcodesvc

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"

	"github.com/rs/zerolog/log"
)

// Multi-audio HLS ("hls4") — hot audio-track switching without restarting the
// session or re-reading the source.
//
// The classic pain: switching озвучка meant killing the whole ffmpeg job
// (including a 4K video transcode) and re-reading the source from the seek
// position — for a torrent that can mean re-downloading gigabytes. The scheme
// (suggested by the upstream author for his planned "hls4"):
//
//  1. The MAIN job — which reads the source exactly once anyway — additionally
//     demuxes EVERY audio track in -c copy into per-track "shelf" segments
//     (aud_shelf_<abs>/s_%05d.ts). Copy = zero CPU; MPEG-TS because raw TS
//     concatenates losslessly, which the reader below depends on.
//  2. A tiny on-demand READER per active track: a feeder goroutine tails the
//     shelf segments into ffmpeg stdin (concatenated TS), which re-encodes
//     JUST that one track to AAC HLS fMP4 (aud_<abs>/seg_%05d.m4s) with
//     -copyts, so rendition timestamps align with the video timeline.
//  3. master.m3u8 declares an EXT-X-MEDIA AUDIO group: the track muxed into
//     the video variant is the URI-less DEFAULT (audio-in-variant per HLS
//     spec), every other shelvable track points at its lazy rendition. The
//     player switches tracks itself — no session restart, and CPU is spent
//     only on the ONE track someone is actually listening to.
//
// Readers spawn lazily on the first GET of a rendition file, follow playback
// via the shelves, and die with the job (or when idle). Seek keeps its
// existing restart semantics: shelves restart from the seek segment together
// with the video, and stale readers drain out and respawn on the next GET.

const (
	audioShelfDirPrefix = "aud_shelf_" // internal copy shelves (reader input)
	audioRendDirPrefix  = "aud_"       // public AAC renditions (player-facing)
	audioReaderIdleTTL  = 90 * time.Second
)

// shelvableAudioCodec lists codecs that survive `-c copy` into MPEG-TS and
// back out of a concatenated TS read. Covers the overwhelming majority of
// real releases; anything else (flac/truehd/pcm/vorbis) simply isn't offered
// for hot switching — selecting it falls back to the legacy restart path.
func shelvableAudioCodec(codec string) bool {
	switch strings.ToLower(strings.TrimSpace(codec)) {
	case "aac", "ac3", "eac3", "dts", "dca", "mp3", "mp2":
		return true
	}
	return false
}

// shelfAudioTrack describes one hot-switchable audio track.
type shelfAudioTrack struct {
	AbsIndex int // absolute ffmpeg stream index (-map 0:<abs>)
	RelIndex int // audio-only index (matches ctx.Audio.Index semantics)
	Codec    string
	Lang     string
	Title    string
}

// displayName builds the EXT-X-MEDIA NAME attribute.
func (t shelfAudioTrack) displayName() string {
	name := strings.TrimSpace(t.Title)
	if name == "" {
		if t.Lang != "" {
			name = t.Lang
		} else {
			name = fmt.Sprintf("Audio %d", t.RelIndex+1)
		}
	}
	return name
}

// listShelfAudioTracks extracts the shelvable audio tracks from ffprobe output,
// preserving stream order.
func listShelfAudioTracks(probe map[string]any) []shelfAudioTrack {
	if probe == nil {
		return nil
	}
	streams, ok := probe["streams"].([]any)
	if !ok {
		return nil
	}
	var out []shelfAudioTrack
	rel := 0
	for _, s := range streams {
		sm, _ := s.(map[string]any)
		if sm == nil || fmt.Sprint(sm["codec_type"]) != "audio" {
			continue
		}
		t := shelfAudioTrack{
			AbsIndex: toInt(sm["index"]),
			RelIndex: rel,
			Codec:    fmt.Sprint(sm["codec_name"]),
		}
		rel++
		if tags, ok := sm["tags"].(map[string]any); ok {
			if l, ok := tags["language"].(string); ok {
				t.Lang = strings.TrimSpace(l)
			}
			if ti, ok := tags["title"].(string); ok {
				t.Title = strings.TrimSpace(ti)
			}
		}
		if !shelvableAudioCodec(t.Codec) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// appendAudioShelfOutputs adds one copy-segmenter output per shelf track to
// the MAIN ffmpeg command. Rides the same source read — zero extra CPU/IO. The
// ACTIVE track is skipped: its audio is already muxed into the video variant,
// so it needs no shelf and no reader (the player plays it in-variant).
func appendAudioShelfOutputs(args []string, ctx transcodingContext, startNum int) []string {
	segDur := max(ctx.HLS.SegDur, 1)
	for _, t := range ctx.ShelfTracks {
		if t.AbsIndex == ctx.ActiveAudioAbs {
			continue
		}
		dir := fmt.Sprintf("%s%d", audioShelfDirPrefix, t.AbsIndex)
		args = append(args,
			"-map", fmt.Sprintf("0:%d", t.AbsIndex),
			"-c", "copy", "-copyts",
			"-muxdelay", "0", "-muxpreload", "0",
			"-f", "segment",
			"-segment_time", strconv.Itoa(segDur),
			"-segment_start_number", strconv.Itoa(startNum),
			"-segment_format", "mpegts",
			filepath.Join(dir, "s_%05d.ts"),
		)
	}
	return args
}

// ensureAudioShelfDirs pre-creates the shelf + rendition dirs so neither
// ffmpeg nor the reader trips on a missing directory at first segment.
func ensureAudioShelfDirs(outputDir string, tracks []shelfAudioTrack) {
	for _, t := range tracks {
		_ = os.MkdirAll(filepath.Join(outputDir, fmt.Sprintf("%s%d", audioShelfDirPrefix, t.AbsIndex)), 0o755)
		_ = os.MkdirAll(filepath.Join(outputDir, fmt.Sprintf("%s%d", audioRendDirPrefix, t.AbsIndex)), 0o755)
	}
}

// ---------------------------------------------------------------------------
// Reader manager
// ---------------------------------------------------------------------------

type audioReader struct {
	cancel   context.CancelFunc
	startSeg int
	done     chan struct{}
	lastUse  int64 // unix seconds, atomic via mutex below
}

type multiAudioState struct {
	mu      sync.Mutex
	readers map[int]*audioReader // track AbsIndex → reader
}

var (
	maStatesMu sync.Mutex
	maStates   = map[string]*multiAudioState{} // OutputDir → state
)

func maStateFor(outputDir string) *multiAudioState {
	maStatesMu.Lock()
	defer maStatesMu.Unlock()
	// Opportunistic prune (same pattern as trickplay): job cleanup removes the
	// OutputDir, so a state whose dir vanished is dead weight.
	if len(maStates) > 32 {
		for dir, st := range maStates {
			if _, err := os.Stat(dir); os.IsNotExist(err) {
				st.stopAll()
				delete(maStates, dir)
			}
		}
	}
	st, ok := maStates[outputDir]
	if !ok {
		st = &multiAudioState{readers: map[int]*audioReader{}}
		maStates[outputDir] = st
	}
	return st
}

func (st *multiAudioState) stopAll() {
	st.mu.Lock()
	defer st.mu.Unlock()
	for abs, r := range st.readers {
		r.cancel()
		delete(st.readers, abs)
	}
}

// stopAudioReaders kills every reader of a job — called from cleanup().
func stopAudioReaders(outputDir string) {
	maStatesMu.Lock()
	st := maStates[outputDir]
	delete(maStates, outputDir)
	maStatesMu.Unlock()
	if st != nil {
		st.stopAll()
	}
}

// readerAlive reports whether the reader finished (proc exited / feeder done).
func (r *audioReader) alive() bool {
	select {
	case <-r.done:
		return false
	default:
		return true
	}
}

// ensureAudioReader guarantees a reader for the given track is running and
// covers wantSeg. Restarts the reader when the request is BEHIND its start
// (backward seek / track re-selected earlier position) — same restart
// semantics the video path uses. Forward gaps are left to the running reader:
// it transcodes shelf segments far faster than realtime and the segment
// handler's wait loop absorbs the catch-up.
func ensureAudioReader(cfg config.Config, job *TranscodingJob, track shelfAudioTrack, wantSeg int) {
	st := maStateFor(job.OutputDir)
	st.mu.Lock()
	defer st.mu.Unlock()

	r := st.readers[track.AbsIndex]
	if r != nil && r.alive() && r.startSeg <= wantSeg {
		r.lastUse = time.Now().Unix()
		return
	}
	if r != nil {
		r.cancel()
	}
	if wantSeg < 0 {
		wantSeg = 0
	}
	nr := &audioReader{startSeg: wantSeg, done: make(chan struct{}), lastUse: time.Now().Unix()}
	rctx, cancel := context.WithCancel(context.Background())
	nr.cancel = cancel
	st.readers[track.AbsIndex] = nr
	go runAudioReader(rctx, cfg, job, track, wantSeg, nr)
	log.Info().Str("stream", job.StreamID).Int("track", track.AbsIndex).Int("startSeg", wantSeg).Str("codec", track.Codec).Msg("transcoding: audio reader started")
}

// runAudioReader spawns the AAC transcoder and feeds it shelf segments.
func runAudioReader(ctx context.Context, cfg config.Config, job *TranscodingJob, track shelfAudioTrack, startSeg int, r *audioReader) {
	defer close(r.done)

	outDir := filepath.Join(job.OutputDir, fmt.Sprintf("%s%d", audioRendDirPrefix, track.AbsIndex))
	shelfDir := filepath.Join(job.OutputDir, fmt.Sprintf("%s%d", audioShelfDirPrefix, track.AbsIndex))
	_ = os.MkdirAll(outDir, 0o755)

	ffmpegBin := strings.TrimSpace(cfg.Transcoding.FFmpeg)
	if ffmpegBin == "" {
		ffmpegBin = "ffmpeg"
	}
	segDur := max(job.Context.HLS.SegDur, 1)
	bitrate := clamp(job.Context.Audio.BitrateKbps, 32, 512)
	if bitrate == 0 {
		bitrate = 192
	}

	args := []string{
		"-y", "-hide_banner", "-loglevel", "error",
		"-f", "mpegts", "-i", "pipe:0",
		"-map", "0:a:0",
		"-c:a", "aac", "-b:a", fmt.Sprintf("%dk", bitrate),
	}
	if job.Context.Audio.Stereo {
		args = append(args, "-ac", "2")
	}
	args = append(args,
		// Keep source timestamps so the rendition's fMP4 tfdt lines up with
		// the video variant — the player syncs the two by timeline, not by
		// segment numbering.
		"-copyts", "-muxdelay", "0", "-muxpreload", "0",
		"-f", "hls",
		"-hls_time", strconv.Itoa(segDur),
		"-hls_list_size", "0",
		"-hls_segment_type", "fmp4",
		"-hls_fmp4_init_filename", "init.mp4",
		"-start_number", strconv.Itoa(startSeg),
		"-hls_segment_filename", filepath.Join(outDir, "seg_%05d.m4s"),
		filepath.Join(outDir, "live.m3u8"),
	)

	cmd := exec.CommandContext(ctx, ffmpegBin, args...)
	cmd.Dir = job.OutputDir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		log.Warn().Err(err).Msg("transcoding: audio reader stdin pipe failed")
		return
	}
	if err := cmd.Start(); err != nil {
		log.Warn().Err(err).Msg("transcoding: audio reader start failed")
		return
	}

	// Feeder: stream shelf segments sequentially into ffmpeg stdin. A shelf
	// segment is complete once its successor exists (the segment muxer opens
	// N+1 only after closing N) or the main job exited (final segment).
	feedErr := func() error {
		defer stdin.Close()
		for i := startSeg; ; i++ {
			p := filepath.Join(shelfDir, fmt.Sprintf("s_%05d.ts", i))
			next := filepath.Join(shelfDir, fmt.Sprintf("s_%05d.ts", i+1))
			for {
				if fileExists(next) {
					break
				}
				if job.HasExited() {
					break
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(250 * time.Millisecond):
				}
			}
			if !fileExists(p) {
				// Main job gone and this segment never landed → end of stream.
				return nil
			}
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			_, err = io.Copy(stdin, f)
			_ = f.Close()
			if err != nil {
				return err // reader died / ctx cancelled mid-write
			}
		}
	}()

	werr := cmd.Wait()
	if ctx.Err() == nil && (werr != nil || feedErr != nil) {
		log.Debug().Err(werr).AnErr("feed", feedErr).Int("track", track.AbsIndex).Msg("transcoding: audio reader finished with error")
	}
}

// idleAudioReaderSweep cancels readers nobody fetched from for a while — the
// player keeps exactly one rendition hot, so after a switch the old track's
// reader would otherwise transcode the rest of the movie for nothing.
func idleAudioReaderSweep() {
	now := time.Now().Unix()
	maStatesMu.Lock()
	states := make([]*multiAudioState, 0, len(maStates))
	for _, st := range maStates {
		states = append(states, st)
	}
	maStatesMu.Unlock()
	for _, st := range states {
		st.mu.Lock()
		for abs, r := range st.readers {
			if r.alive() && now-r.lastUse > int64(audioReaderIdleTTL/time.Second) {
				r.cancel()
				delete(st.readers, abs)
			}
		}
		st.mu.Unlock()
	}
}

// ---------------------------------------------------------------------------
// HTTP: /transcoding/{streamId}/aud_{aidx}/{file}
// ---------------------------------------------------------------------------

// buildAudioRenditionPlaylist returns the synthetic full-VOD playlist for one
// audio rendition — same philosophy as main.m3u8: list everything up front,
// the segment handler blocks until the reader produces the requested file.
func buildAudioRenditionPlaylist(durationSec, segDur int) string {
	segDur = max(segDur, 1)
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-VERSION:7\n")
	b.WriteString(fmt.Sprintf("#EXT-X-TARGETDURATION:%d\n", segDur))
	b.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
	b.WriteString("#EXT-X-MAP:URI=\"init.mp4\"\n")
	for i := range durationSec / segDur {
		b.WriteString(fmt.Sprintf("#EXTINF:%d.0,\n", segDur))
		b.WriteString(fmt.Sprintf("seg_%05d.m4s\n", i))
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

// jobShelfTrackByAbs finds the shelf track with the given absolute index.
func jobShelfTrackByAbs(job *TranscodingJob, abs int) (shelfAudioTrack, bool) {
	for _, t := range job.Context.ShelfTracks {
		if t.AbsIndex == abs {
			return t, true
		}
	}
	return shelfAudioTrack{}, false
}

// jobPositionSegEstimate guesses the segment the viewer is around: newest
// heartbeat position wins, else the write head minus a small lookahead.
func jobPositionSegEstimate(svc *TranscodingService, job *TranscodingJob) int {
	segDur := max(job.Context.HLS.SegDur, 1)
	if pos, ok := job.MaxPosition(); ok {
		return int(pos) / segDur
	}
	if high := svc.highestSegmentOnDisk(job); high > 3 {
		return high - 3
	}
	return 0
}

func transcodingAudioRenditionHandler(cfg config.Config, svc *TranscodingService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !cfg.Transcoding.Enable {
			transcodingDisabledError(w)
			return
		}
		streamID := extractPathParam(r, "streamId")
		job, ok := svc.TryResolveJob(streamID)
		if !ok {
			http.NotFound(w, r)
			return
		}
		svc.Touch(job)

		abs, err := strconv.Atoi(extractPathParam(r, "aidx"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		// The active track's audio is muxed into the video variant (its group
		// member is URI-less), so it has no shelf and no rendition. A compliant
		// player never fetches it; a stray request would otherwise spin a reader
		// over an empty shelf and hang — refuse it up front.
		if abs == job.Context.ActiveAudioAbs {
			http.NotFound(w, r)
			return
		}
		track, ok := jobShelfTrackByAbs(job, abs)
		if !ok {
			http.NotFound(w, r)
			return
		}
		file := extractPathParam(r, "file")
		rendDir := filepath.Join(job.OutputDir, fmt.Sprintf("%s%d", audioRendDirPrefix, abs))

		switch {
		case file == "index.m3u8":
			// Synthetic VOD playlist; also pre-warm the reader near the
			// viewer's position so init.mp4/first segment follow quickly.
			ensureAudioReader(cfg, job, track, jobPositionSegEstimate(svc, job))
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			w.Header().Set("Cache-Control", "no-cache")
			_, _ = w.Write([]byte(buildAudioRenditionPlaylist(ffprobeDuration(job.Context.FFProbe), job.Context.HLS.SegDur)))
			return

		case file == "init.mp4":
			ensureAudioReader(cfg, job, track, jobPositionSegEstimate(svc, job))

		default:
			m := reSegIndex.FindStringSubmatch(file)
			if m == nil {
				http.NotFound(w, r)
				return
			}
			idx, _ := strconv.Atoi(m[1])
			ensureAudioReader(cfg, job, track, idx)
		}

		// Wait-serve: the reader transcodes far faster than realtime, so the
		// file lands within a segment-duration or two of the shelves having it.
		path := filepath.Join(rendDir, file)
		if !strings.HasPrefix(path, rendDir+string(os.PathSeparator)) {
			http.NotFound(w, r)
			return
		}
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			if st, err := os.Stat(path); err == nil && !st.IsDir() && st.Size() > 0 {
				// Same completeness gate as the shelves: a segment is done
				// when its successor exists or the writer moved past it.
				if strings.HasSuffix(file, ".m4s") {
					if next := nextSegName(file); next != "" && !fileExists(filepath.Join(rendDir, next)) && !job.HasExited() {
						time.Sleep(150 * time.Millisecond)
						continue
					}
				}
				if ct := transcodingContentTypes[filepath.Ext(file)]; ct != "" {
					w.Header().Set("Content-Type", ct)
				}
				http.ServeFile(w, r, path)
				return
			}
			if job.HasExited() && job.ExitCode() != 0 {
				http.Error(w, "transcoder exited", http.StatusBadGateway)
				return
			}
			time.Sleep(250 * time.Millisecond)
		}
		http.NotFound(w, r)
	}
}

// nextSegName returns seg_%05d.m4s of index+1 for a seg_%05d.m4s name.
func nextSegName(file string) string {
	m := reSegIndex.FindStringSubmatch(file)
	if m == nil {
		return ""
	}
	idx, err := strconv.Atoi(m[1])
	if err != nil {
		return ""
	}
	return fmt.Sprintf("seg_%05d.%s", idx+1, m[2])
}
