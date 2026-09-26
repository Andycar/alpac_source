package transcodesvc

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// Карта ключевых кадров источника и синтетический HLS-плейлист по ней.
//
// Зачем (приём Jellyfin, см. keyframes_mkv.go): при stream-copy видео ffmpeg режет сегменты
// только по ключевым кадрам, а наш VOD-плейлист объявлял «SegDur секунд» на каждый. Проверено
// на ffmpeg 9 с нашими флагами (`-hls_time 6 -hls_init_time 1 -hls_list_size 10
// -hls_playlist_type vod`): hlsenc берёт init_time целевой длиной на ВЕСЬ ролик (recording_time
// выставляется на старте, пока list_size ещё > 0, а VOD потом обнуляет list_size и сброса не
// происходит), так что copy-сегменты выходят по одному GOP — 2 с при объявленных 6. Плеер живёт
// по PTS, поэтому играет, но буфер считает по плейлисту (втрое больше настоящего), а перемотка
// попадает не туда и заставляет перезапускать ffmpeg. С картой плейлист объявляет ровно те
// границы, которые hlsenc и нарежет, а -ss при перемотке бьёт точно в ключевой кадр.
type keyframeMap struct {
	Times          []float64 // секунды по контейнеру, монотонно
	Offsets        []int64   // байтовое смещение кластера/сэмпла (0 = неизвестно)
	RelPos         []int64   // MKV: смещение блока внутри кластера (-1 = нет)
	Duration       float64   // секунды (из контейнера; 0 = неизвестно)
	TimestampScale int64     // MKV: нс на тик
	HeaderEnd      int64     // MKV: байты [0, HeaderEnd) — заголовки до первого кластера
	Source         string    // mkv-cues | ffprobe
}

// hlsSegmentMap — объявленные границы сегментов синтетического плейлиста.
type hlsSegmentMap struct {
	Starts []float64 // начало сегмента i (секунды, относительно первого ключевого кадра)
	Durs   []float64 // длительность сегмента i
}

func (m *hlsSegmentMap) count() int { return len(m.Starts) }

// start возвращает начало сегмента idx; за пределами карты — экстраполяция последней
// длительностью (плеер может спросить индекс чуть дальше объявленного из-за округлений).
func (m *hlsSegmentMap) start(idx int) float64 {
	if idx < 0 || m.count() == 0 {
		return 0
	}
	if idx < m.count() {
		return m.Starts[idx]
	}
	last := m.count() - 1
	return m.Starts[last] + m.Durs[last]*float64(idx-last)
}

// indexAt — сегмент, в который попадает время t.
func (m *hlsSegmentMap) indexAt(t float64) int {
	n := m.count()
	if n == 0 || t <= 0 {
		return 0
	}
	lo, hi := 0, n-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if m.Starts[mid] <= t {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}

// hlsGrid эмулирует нарезку hlsenc для stream-copy: целевая длина recording_time = init_time,
// если он задан (проверено на ffmpeg 9: с -hls_init_time 1 ВСЕ сегменты VOD режутся по цели
// 1 с независимо от hls_list_size — сброс на hls_time происходит только у скользящего окна),
// иначе hls_time; граница — первый ключевой кадр с (t - t0) >= recording_time * number,
// number растёт на единицу за разрез. Времена относительно первого ключевого кадра (ffmpeg с
// -copyts -start_at_zero отсчитывает от начала файла — то же с точностью до опережения аудио).
func hlsGrid(km *keyframeMap, segTime, initTime float64, totalDur float64) *hlsSegmentMap {
	if km == nil || len(km.Times) == 0 || segTime <= 0 {
		return nil
	}
	rec := segTime
	if initTime > 0 {
		rec = initTime
	}
	t0 := km.Times[0]
	out := &hlsSegmentMap{}
	last := 0.0
	number := 1
	for _, t := range km.Times[1:] {
		rel := t - t0
		if rel-last <= 0 {
			continue
		}
		if rel >= rec*float64(number) {
			out.Starts = append(out.Starts, last)
			out.Durs = append(out.Durs, rel-last)
			last = rel
			number++
		}
	}
	end := totalDur
	if km.Duration > 0 && (end <= 0 || km.Duration < end) {
		end = km.Duration
	}
	end -= t0
	if end > last+0.05 {
		out.Starts = append(out.Starts, last)
		out.Durs = append(out.Durs, end-last)
	} else if len(out.Starts) == 0 {
		return nil
	}
	return out
}

// ── источники байтов ──

// httpRangeReader — ReaderAt поверх HTTP Range с теми же заголовками, что у ffmpeg
// (UA/Referer: CDN режут «чужие» запросы). Размер — из Content-Range первого ответа.
type httpRangeReader struct {
	url     string
	ua, ref string
	client  *http.Client
	mu      sync.Mutex
	size    int64
	bytes   int64 // прочитано всего — для лога
}

func newHTTPRangeReader(url, ua, ref string) *httpRangeReader {
	return &httpRangeReader{url: url, ua: ua, ref: ref, size: -1,
		client: &http.Client{Timeout: 20 * time.Second}}
}

func (h *httpRangeReader) ReadAt(p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	req, err := http.NewRequest(http.MethodGet, h.url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, off+int64(len(p))-1))
	if h.ua != "" {
		req.Header.Set("User-Agent", h.ua)
	}
	if h.ref != "" {
		req.Header.Set("Referer", h.ref)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
			return 0, io.EOF
		}
		return 0, fmt.Errorf("range %d: HTTP %d", off, resp.StatusCode)
	}
	if cr := resp.Header.Get("Content-Range"); cr != "" {
		if i := strings.LastIndex(cr, "/"); i > 0 {
			if sz, err := strconv.ParseInt(cr[i+1:], 10, 64); err == nil && sz > 0 {
				h.mu.Lock()
				h.size = sz
				h.mu.Unlock()
			}
		}
	}
	n, err := io.ReadFull(resp.Body, p)
	h.mu.Lock()
	h.bytes += int64(n)
	h.mu.Unlock()
	if err == io.ErrUnexpectedEOF {
		return n, io.EOF
	}
	return n, err
}

func (h *httpRangeReader) Size() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.size
}

// seekerReaderAt — ReaderAt поверх ReadSeeker (торрент-ридер в процессе): чтения
// сериализуются, позиция после каждого не важна — ffmpeg получает свой собственный ридер.
type seekerReaderAt struct {
	mu sync.Mutex
	rs io.ReadSeeker
}

func (s *seekerReaderAt) ReadAt(p []byte, off int64) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.rs.Seek(off, io.SeekStart); err != nil {
		return 0, err
	}
	n, err := io.ReadFull(s.rs, p)
	if err == io.ErrUnexpectedEOF {
		return n, io.EOF
	}
	return n, err
}

// ── извлечение ──

const (
	keyframeExtractTimeout = 12 * time.Second
	// ffprobe-фолбэк читает файл целиком — только для локальных/небольших источников.
	keyframeFFProbeMaxBytes = 400 << 20
)

// probeFormatName — "matroska,webm" и т.п. из ffprobe.
func probeFormatName(probe map[string]any) string {
	if probe == nil {
		return ""
	}
	f, _ := probe["format"].(map[string]any)
	if f == nil {
		return ""
	}
	return strings.ToLower(fmt.Sprint(f["format_name"]))
}

func probeSizeBytes(probe map[string]any) int64 {
	if probe == nil {
		return 0
	}
	f, _ := probe["format"].(map[string]any)
	if f == nil {
		return 0
	}
	v, _ := strconv.ParseInt(fmt.Sprint(f["size"]), 10, 64)
	return v
}

func probeStartTime(probe map[string]any) float64 {
	if probe == nil {
		return 0
	}
	f, _ := probe["format"].(map[string]any)
	if f == nil {
		return 0
	}
	v, _ := strconv.ParseFloat(strings.ReplaceAll(fmt.Sprint(f["start_time"]), ",", "."), 64)
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	return v
}

// extractKeyframes строит карту для источника: MKV — по Cues (HTTP Range / файл / торрент-
// ридер), остальное — ffprobe по ключевым кадрам, но только когда это не значит скачать
// многогигабайтный файл ради плейлиста. nil = карты нет, работаем как раньше.
func extractKeyframes(ctx context.Context, source, ua, ref string, probe map[string]any, torrent io.ReadSeeker, ffprobePath string) (*keyframeMap, error) {
	format := probeFormatName(probe)
	isMKV := strings.Contains(format, "matroska") || strings.Contains(format, "webm")
	start := time.Now()
	if isMKV {
		var ra io.ReaderAt
		var size int64 = -1
		switch {
		case torrent != nil:
			ra = &seekerReaderAt{rs: torrent}
			if end, err := torrent.Seek(0, io.SeekEnd); err == nil {
				size = end
			}
		case strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://"):
			hr := newHTTPRangeReader(source, ua, ref)
			ra = hr
			if sz := probeSizeBytes(probe); sz > 0 {
				size = sz
			}
		default:
			f, err := os.Open(source)
			if err != nil {
				return nil, err
			}
			defer f.Close()
			if st, err := f.Stat(); err == nil {
				size = st.Size()
			}
			ra = f
		}
		type res struct {
			km  *keyframeMap
			err error
		}
		ch := make(chan res, 1)
		go func() {
			km, err := mkvKeyframes(ra, size)
			ch <- res{km, err}
		}()
		select {
		case r := <-ch:
			if r.err != nil {
				return nil, r.err
			}
			if hr, ok := ra.(*httpRangeReader); ok {
				log.Debug().Int64("bytes", hr.bytes).Int("cues", len(r.km.Times)).Dur("took", time.Since(start)).Msg("transcoding: keyframe map from MKV cues")
			}
			return r.km, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	// ffprobe по ключевым кадрам читает весь файл: локальный — всегда, HTTP — только маленький.
	if torrent != nil {
		return nil, errors.New("keyframes: no cue index for this container on a torrent pipe")
	}
	if strings.HasPrefix(source, "http") {
		if sz := probeSizeBytes(probe); sz <= 0 || sz > keyframeFFProbeMaxBytes {
			return nil, errors.New("keyframes: source too large for ffprobe scan")
		}
	}
	km, err := ffprobeKeyframes(ctx, ffprobePath, source, ua, ref)
	if err == nil {
		log.Debug().Int("keyframes", len(km.Times)).Dur("took", time.Since(start)).Msg("transcoding: keyframe map from ffprobe")
	}
	return km, err
}

// ffprobeKeyframes — тот же вызов, что у Jellyfin (FfProbeKeyframeExtractor): пакеты видео
// с флагом K и длительность из stream/format.
func ffprobeKeyframes(ctx context.Context, ffprobePath, source, ua, ref string) (*keyframeMap, error) {
	if ffprobePath == "" {
		ffprobePath = "ffprobe"
	}
	args := []string{"-v", "error", "-fflags", "+genpts", "-skip_frame", "nokey"}
	if ua != "" && strings.HasPrefix(source, "http") {
		args = append(args, "-user_agent", ua)
	}
	if ref != "" && strings.HasPrefix(source, "http") {
		args = append(args, "-headers", "Referer: "+ref+"\r\n")
	}
	args = append(args, "-show_entries", "format=duration", "-show_entries", "stream=duration",
		"-show_entries", "packet=pts_time,flags", "-select_streams", "v:0", "-of", "csv", source)
	cmd := exec.CommandContext(ctx, ffprobePath, args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	km := &keyframeMap{Source: "ffprobe", TimestampScale: 1_000_000}
	var streamDur, formatDur float64
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "packet,"):
			parts := strings.Split(line, ",")
			if len(parts) >= 3 && strings.HasPrefix(parts[2], "K") {
				if t, err := strconv.ParseFloat(parts[1], 64); err == nil {
					if n := len(km.Times); n == 0 || t > km.Times[n-1] {
						km.Times = append(km.Times, t)
						km.Offsets = append(km.Offsets, 0)
						km.RelPos = append(km.RelPos, -1)
					}
				}
			}
		case strings.HasPrefix(line, "stream,"):
			streamDur, _ = strconv.ParseFloat(strings.TrimPrefix(line, "stream,"), 64)
		case strings.HasPrefix(line, "format,"):
			formatDur, _ = strconv.ParseFloat(strings.TrimPrefix(line, "format,"), 64)
		}
	}
	_ = cmd.Wait()
	if len(km.Times) == 0 {
		return nil, errors.New("ffprobe: no keyframes")
	}
	km.Duration = streamDur
	if km.Duration <= 0 {
		km.Duration = formatDur
	}
	return km, nil
}

// fmtSeek печатает секунды для -ss: миллисекундная точность, без хвоста нулей.
func fmtSeek(sec float64) string {
	return strconv.FormatFloat(math.Round(sec*1000)/1000, 'f', -1, 64)
}
