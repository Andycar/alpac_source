package transcodesvc

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"

	"github.com/rs/zerolog/log"
)

// Embedded-font extraction for client-side ASS rendering (ported from
// Jellyfin's AttachmentExtractor). MKV releases with ASS subtitles usually
// bundle the typefaces as attachment streams; without them a client-side
// renderer (JASSUB/libass) falls back to a default font and the styling the
// author intended is lost — which is why we historically burned ASS into the
// video on TVs. Serving the ASS text (subs_<idx>.ass sibling, produced by the
// main extract job) plus these fonts lets capable clients render subtitles
// on-device: no video re-encode, full styling.
//
// Extraction is lazy (first GET /fonts) and cheap: attachments live in the
// Matroska header, so ffmpeg reads only the head of the source. Results land
// in <OutputDir>/fonts/ and share the job's cleanup lifecycle.

// fontAttachment describes one font attachment stream from ffprobe.
type fontAttachment struct {
	AbsIndex int    `json:"index"`
	Name     string `json:"name"`
	Mime     string `json:"mime"`
}

// fontMimeAllowed reports whether an attachment mimetype/filename looks like a
// font. Mirrors Jellyfin's allow-list (fonts only — cover art is mjpeg and is
// skipped by the codec check anyway).
func fontMimeAllowed(mime, name string) bool {
	m := strings.ToLower(strings.TrimSpace(mime))
	switch {
	case strings.HasPrefix(m, "font/"),
		m == "application/x-truetype-font",
		m == "application/x-font-ttf",
		m == "application/x-font-otf",
		m == "application/vnd.ms-opentype",
		strings.HasPrefix(m, "application/font-"):
		return true
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".ttf", ".otf", ".ttc", ".woff", ".woff2":
		return true
	}
	return false
}

// fontAttachmentsFromProbe lists font attachment streams from raw ffprobe
// output. Non-font attachments (cover art etc.) are filtered out.
func fontAttachmentsFromProbe(probe map[string]any) []fontAttachment {
	if probe == nil {
		return nil
	}
	streams, ok := probe["streams"].([]any)
	if !ok {
		return nil
	}
	var out []fontAttachment
	for _, s := range streams {
		sm, _ := s.(map[string]any)
		if sm == nil || fmt.Sprint(sm["codec_type"]) != "attachment" {
			continue
		}
		name, mime := "", ""
		if tags, ok := sm["tags"].(map[string]any); ok {
			if v, ok := tags["filename"].(string); ok {
				name = strings.TrimSpace(v)
			}
			if v, ok := tags["mimetype"].(string); ok {
				mime = strings.TrimSpace(v)
			}
		}
		if name == "" || !fontMimeAllowed(mime, name) {
			continue
		}
		out = append(out, fontAttachment{
			AbsIndex: toInt(sm["index"]),
			Name:     sanitizeFontFilename(name),
			Mime:     mime,
		})
	}
	return out
}

// sanitizeFontFilename strips any path components and characters that could
// escape the fonts dir; the result is used both as the on-disk name and the
// URL path segment.
func sanitizeFontFilename(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', 0:
			return '_'
		}
		return r
	}, name)
	if name == "" || name == "." || name == ".." {
		return "_font"
	}
	return name
}

// fontExtractLocks serialises extraction per output dir so concurrent /fonts
// requests spawn one ffmpeg, not N.
var fontExtractLocks sync.Map // outputDir → *sync.Mutex

// ensureFontsExtracted dumps all font attachments of the job's source into
// <OutputDir>/fonts/ once. Subsequent calls are no-ops (marker file). Returns
// the attachment list (possibly empty) — missing files after a successful run
// mean the source lied about an attachment; those entries are dropped.
func ensureFontsExtracted(cfg config.Config, job *TranscodingJob) []fontAttachment {
	fonts := fontAttachmentsFromProbe(job.Context.FFProbe)
	if len(fonts) == 0 {
		return nil
	}
	dir := filepath.Join(job.OutputDir, "fonts")
	marker := filepath.Join(dir, ".done")

	lockAny, _ := fontExtractLocks.LoadOrStore(job.OutputDir, &sync.Mutex{})
	mu := lockAny.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()

	if _, err := os.Stat(marker); err != nil {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Warn().Err(err).Str("dir", dir).Msg("fonts: mkdir failed")
			return nil
		}
		ffmpegBin := strings.TrimSpace(cfg.Transcoding.FFmpeg)
		if ffmpegBin == "" {
			ffmpegBin = "ffmpeg"
		}
		// One process, one -dump_attachment per font. -t 0 -f null: open the
		// input (dumping happens at open time), decode nothing.
		args := []string{"-y", "-hide_banner", "-loglevel", "error"}
		for _, f := range fonts {
			args = append(args, fmt.Sprintf("-dump_attachment:%d", f.AbsIndex), filepath.Join(dir, f.Name))
		}
		args = append(args, "-i", job.Context.Source, "-t", "0", "-f", "null", "-")

		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, ffmpegBin, args...).CombinedOutput()
		if err != nil {
			// ffmpeg exits non-zero after -dump_attachment with "At least one
			// output file must be specified" on some versions even when the
			// dump succeeded — so check for the files instead of failing hard.
			log.Debug().Err(err).Str("out", truncateStr(string(out), 200)).Msg("fonts: ffmpeg dump finished with error (checking files)")
		}
		_ = os.WriteFile(marker, []byte("ok"), 0o644)
	}

	kept := fonts[:0]
	for _, f := range fonts {
		if st, err := os.Stat(filepath.Join(dir, f.Name)); err == nil && st.Size() > 0 {
			kept = append(kept, f)
		}
	}
	return kept
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ---------------------------------------------------------------------------
// GET /transcoding/{streamId}/fonts        — JSON list
// GET /transcoding/{streamId}/fonts/{name} — font file
// ---------------------------------------------------------------------------

func transcodingFontsListHandler(cfg config.Config, svc *TranscodingService) http.HandlerFunc {
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

		host := transcodingHost(r)
		fonts := ensureFontsExtracted(cfg, job)
		type fontOut struct {
			Name string `json:"name"`
			Mime string `json:"mime,omitempty"`
			URL  string `json:"url"`
		}
		out := make([]fontOut, 0, len(fonts))
		for _, f := range fonts {
			out = append(out, fontOut{
				Name: f.Name,
				Mime: f.Mime,
				URL:  fmt.Sprintf("%s/transcoding/%s/fonts/%s", host, streamID, f.Name),
			})
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func transcodingFontFileHandler(cfg config.Config, svc *TranscodingService) http.HandlerFunc {
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

		name := sanitizeFontFilename(extractPathParam(r, "name"))
		dir := filepath.Join(job.OutputDir, "fonts")
		path := filepath.Join(dir, name)
		// Containment: sanitize already blocks traversal, this is belt+braces.
		if !strings.HasPrefix(path, dir+string(os.PathSeparator)) {
			http.NotFound(w, r)
			return
		}
		st, err := os.Stat(path)
		if err != nil || st.IsDir() {
			http.NotFound(w, r)
			return
		}
		mime := "application/octet-stream"
		switch strings.ToLower(filepath.Ext(name)) {
		case ".ttf":
			mime = "font/ttf"
		case ".otf":
			mime = "font/otf"
		case ".ttc":
			mime = "font/collection"
		case ".woff":
			mime = "font/woff"
		case ".woff2":
			mime = "font/woff2"
		}
		w.Header().Set("Content-Type", mime)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		http.ServeFile(w, r, path)
	}
}
