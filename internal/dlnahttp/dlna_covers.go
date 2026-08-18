package dlnahttp

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"lampac-go/internal/config"

	"github.com/rs/zerolog/log"
)

// DLNACoverGenerator runs a background loop that generates thumbnails
// and optional preview clips for media files using FFmpeg.
// Mirrors .NET DLNA ModInit cover generation.
type DLNACoverGenerator struct {
	cfg     config.Config
	running int32
	stop    chan struct{}
}

// NewDLNACoverGenerator creates a new cover generator.
func NewDLNACoverGenerator(cfg config.Config) *DLNACoverGenerator {
	return &DLNACoverGenerator{
		cfg:  cfg,
		stop: make(chan struct{}),
	}
}

// Start begins the background cover generation loop.
func (cg *DLNACoverGenerator) Start() {
	dlnaCfg := cg.cfg.DLNA
	if !dlnaCfg.Enable || !dlnaCfg.Cover.Enable {
		log.Info().Msg("dlna-covers: disabled")
		return
	}

	// Verify ffmpeg is available.
	ffmpeg := cg.cfg.Transcoding.FFmpeg
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	if _, err := exec.LookPath(ffmpeg); err != nil {
		log.Warn().Msg("dlna-covers: ffmpeg not found, cover generation disabled")
		return
	}

	timeout := dlnaCfg.Cover.Timeout
	if timeout < 1 {
		timeout = 20
	}

	log.Info().Int("interval_min", timeout).Msg("dlna-covers: starting")
	go cg.loop(timeout)
}

// Stop signals the background loop to exit.
func (cg *DLNACoverGenerator) Stop() {
	select {
	case cg.stop <- struct{}{}:
	default:
	}
}

func (cg *DLNACoverGenerator) loop(intervalMin int) {
	// Initial delay: 1 minute.
	timer := time.NewTimer(1 * time.Minute)
	defer timer.Stop()

	for {
		select {
		case <-cg.stop:
			return
		case <-timer.C:
			cg.scan()
			timer.Reset(time.Duration(intervalMin) * time.Minute)
		}
	}
}

func (cg *DLNACoverGenerator) scan() {
	if !atomic.CompareAndSwapInt32(&cg.running, 0, 1) {
		return
	}
	defer atomic.StoreInt32(&cg.running, 0)

	dlnaRoot := resolveDLNARoot(cg.cfg)
	thumbsDir := filepath.Join(dlnaRoot, "thumbs")
	tempDir := filepath.Join(dlnaRoot, "temp")
	_ = os.MkdirAll(thumbsDir, 0o755)
	_ = os.MkdirAll(tempDir, 0o755)

	coverCfg := cg.cfg.DLNA.Cover
	extPattern := coverCfg.Extension
	if extPattern == "" {
		extPattern = `(mp4|mkv|avi|mpg|mpe|mpv)`
	}
	extRe, err := regexp.Compile(`(?i)\.` + extPattern + `$`)
	if err != nil {
		log.Warn().Err(err).Msg("dlna-covers: invalid extension pattern")
		return
	}

	skipModTime := time.Duration(coverCfg.SkipModTime) * time.Minute
	if skipModTime == 0 {
		skipModTime = 60 * time.Minute
	}
	now := time.Now()

	ffmpeg := cg.cfg.Transcoding.FFmpeg
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}

	var generated int

	_ = filepath.Walk(dlnaRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			// Skip internal directories.
			if info != nil && info.IsDir() {
				name := info.Name()
				if name == "thumbs" || name == "tmdb" || name == "temp" {
					return filepath.SkipDir
				}
			}
			return nil
		}

		if !extRe.MatchString(info.Name()) {
			return nil
		}

		// Skip recently modified files.
		if now.Sub(info.ModTime()) < skipModTime {
			return nil
		}

		nameHash := dlnaMD5(info.Name())

		// Check if thumbnail already exists.
		thumbPath := filepath.Join(thumbsDir, nameHash+".jpg")
		if _, err := os.Stat(thumbPath); err == nil {
			return nil // already generated
		}

		// Check for lock file (another process generating).
		lockPath := filepath.Join(tempDir, nameHash+"-ffmpeg.lock")
		if _, err := os.Stat(lockPath); err == nil {
			return nil
		}

		// Create lock file.
		_ = os.WriteFile(lockPath, []byte{}, 0o644)
		defer os.Remove(lockPath)

		// Generate thumbnail using FFmpeg.
		cmd := coverCfg.CoverCommand
		if cmd == "" {
			cmd = `-n -ss 3:00 -i "{file}" -vf "thumbnail=150,scale=400:-2" -frames:v 1 "{thumb}"`
		}
		cmd = strings.ReplaceAll(cmd, "{file}", path)
		cmd = strings.ReplaceAll(cmd, "{thumb}", thumbPath)

		args := splitFFmpegArgs(cmd)
		if err := runFFmpegQuiet(ffmpeg, args); err != nil {
			log.Debug().Err(err).Str("file", info.Name()).Msg("dlna-covers: ffmpeg thumbnail failed")
			return nil
		}

		generated++

		// Generate preview if enabled.
		if coverCfg.Preview {
			previewPath := filepath.Join(tempDir, nameHash+".mp4")
			if _, err := os.Stat(previewPath); os.IsNotExist(err) {
				previewCmd := buildPreviewCommand(ffmpeg, path, previewPath)
				if err := runFFmpegQuiet(ffmpeg, previewCmd); err != nil {
					log.Debug().Err(err).Str("file", info.Name()).Msg("dlna-covers: ffmpeg preview failed")
				}
			}
		}

		return nil
	})

	if generated > 0 {
		log.Info().Int("generated", generated).Msg("dlna-covers: thumbnails generated")
	}
}

// buildPreviewCommand creates FFmpeg args for a short preview clip.
// Extracts a 5-second clip starting at 10% of the video.
func buildPreviewCommand(ffmpeg, inputFile, outputFile string) []string {
	return splitFFmpegArgs(
		`-n -ss 60 -i "` + inputFile + `" -t 5 -c:v libx264 -preset ultrafast -an -vf "scale=320:-2" "` + outputFile + `"`,
	)
}

// splitFFmpegArgs splits a command string respecting quotes.
func splitFFmpegArgs(cmd string) []string {
	var args []string
	var current strings.Builder
	inQuote := false
	quoteChar := byte(0)

	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case !inQuote && (c == '"' || c == '\''):
			inQuote = true
			quoteChar = c
		case inQuote && c == quoteChar:
			inQuote = false
		case !inQuote && c == ' ':
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		default:
			current.WriteByte(c)
		}
	}
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	return args
}

// runFFmpegQuiet runs ffmpeg with given args, suppressing output.
func runFFmpegQuiet(ffmpeg string, args []string) error {
	cmd := exec.Command(ffmpeg, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}
