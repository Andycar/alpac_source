package dlnahttp

import (
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"lampac-go/internal/config"

	"github.com/rs/zerolog/log"
)

// ---------------------------------------------------------------------------
//  Response models
// ---------------------------------------------------------------------------

type dlnaItem struct {
	Name         string         `json:"name"`
	URI          string         `json:"uri,omitempty"`
	Img          string         `json:"img,omitempty"`
	Preview      string         `json:"preview,omitempty"`
	Subtitles    []dlnaSubtitle `json:"subtitles,omitempty"`
	Path         string         `json:"path"`
	Type         string         `json:"type"` // "folder" | "file"
	Length       int64          `json:"length"`
	CreationTime string         `json:"creationTime"`
	S            int            `json:"s,omitempty"` // season number
	E            int            `json:"e,omitempty"` // episode number
}

type dlnaSubtitle struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// ---------------------------------------------------------------------------
//  Regex helpers
// ---------------------------------------------------------------------------

var (
	dlnaSeasonRe  = regexp.MustCompile(`(?i)S(\d+)`)
	dlnaEpisodeRe = regexp.MustCompile(`(?i)EP?(\d+)`)
	dlnaNumberRe  = regexp.MustCompile(`\d+`)
)

// compiledMediaPattern returns a compiled regex for media file extensions.
func compiledMediaPattern(pattern string) *regexp.Regexp {
	if pattern == "" {
		pattern = `^\.(aac|flac|mp3|m4a|ogg|opus|wav|mp4|mpeg|mpg|mkv|ts|m2ts|ogv|webm|avi|mov)$`
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		log.Warn().Err(err).Msg("dlna: invalid media pattern, using default")
		re = regexp.MustCompile(`^\.(mp4|mkv|avi|mov|ts|webm|mpg)$`)
	}
	return re
}

// ---------------------------------------------------------------------------
//  dlnaIndexHandler — browse directory
// ---------------------------------------------------------------------------

func dlnaIndexHandler(cfg config.Config) http.HandlerFunc {
	mediaRe := compiledMediaPattern(cfg.DLNA.MediaPattern)

	return func(w http.ResponseWriter, r *http.Request) {
		dlnaRoot := resolveDLNARoot(cfg)
		reqPath := strings.TrimSpace(r.URL.Query().Get("path"))

		// Sanitize: prevent path traversal.
		reqPath = filepath.Clean(strings.TrimPrefix(reqPath, "/"))
		if strings.Contains(reqPath, "..") {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid path"})
			return
		}

		absDir := dlnaRoot
		if reqPath != "" && reqPath != "." {
			absDir = filepath.Join(dlnaRoot, reqPath)
		}

		info, err := os.Stat(absDir)
		if err != nil || !info.IsDir() {
			writeJSON(w, http.StatusOK, []dlnaItem{})
			return
		}

		entries, err := os.ReadDir(absDir)
		if err != nil {
			writeJSON(w, http.StatusOK, []dlnaItem{})
			return
		}

		host := schemeHost(r)
		var folders []dlnaItem
		var files []dlnaItem

		for _, entry := range entries {
			name := entry.Name()

			// Skip internal dirs.
			if entry.IsDir() && (name == "thumbs" || name == "tmdb" || name == "temp") {
				continue
			}

			entryPath := name
			if reqPath != "" && reqPath != "." {
				entryPath = filepath.Join(reqPath, name)
			}

			if entry.IsDir() {
				// Only show folders that contain media or subfolders.
				if dlnaDirHasMedia(filepath.Join(absDir, name), mediaRe) {
					item := dlnaItem{
						Name: name,
						URI:  host + "/dlna?path=" + entryPath,
						Path: entryPath,
						Type: "folder",
					}
					// Cover for folder.
					if img := dlnaCoverURL(host, dlnaRoot, name); img != "" {
						item.Img = img
					}
					if fi, err := entry.Info(); err == nil {
						item.CreationTime = fi.ModTime().UTC().Format("2006-01-02T15:04:05Z")
					}
					folders = append(folders, item)
				}
				continue
			}

			ext := strings.ToLower(filepath.Ext(name))
			if !mediaRe.MatchString(ext) {
				continue
			}

			fi, err := entry.Info()
			if err != nil {
				continue
			}

			item := dlnaItem{
				Name:         name,
				URI:          host + "/dlna/stream?path=" + entryPath,
				Path:         entryPath,
				Type:         "file",
				Length:       fi.Size(),
				CreationTime: fi.ModTime().UTC().Format("2006-01-02T15:04:05Z"),
			}

			// Cover thumbnail.
			if img := dlnaCoverURL(host, dlnaRoot, name); img != "" {
				item.Img = img
			}

			// Preview video.
			previewName := dlnaMD5(name) + ".mp4"
			previewPath := filepath.Join(dlnaRoot, "temp", previewName)
			if _, err := os.Stat(previewPath); err == nil {
				item.Preview = host + "/dlna/stream?path=" + filepath.Join("temp", previewName)
			}

			// Subtitles: look for .srt files with matching base name.
			baseName := strings.TrimSuffix(name, ext)
			item.Subtitles = dlnaFindSubtitles(host, absDir, baseName, reqPath)

			// Parse season/episode from filename.
			if m := dlnaSeasonRe.FindStringSubmatch(name); len(m) > 1 {
				item.S, _ = strconv.Atoi(m[1])
			}
			if m := dlnaEpisodeRe.FindStringSubmatch(name); len(m) > 1 {
				item.E, _ = strconv.Atoi(m[1])
			}

			files = append(files, item)
		}

		// Sort folders alphabetically, files by extracted numbers.
		sort.Slice(folders, func(i, j int) bool {
			return strings.ToLower(folders[i].Name) < strings.ToLower(folders[j].Name)
		})
		sort.Slice(files, func(i, j int) bool {
			ni := extractFirstNumber(files[i].Name)
			nj := extractFirstNumber(files[j].Name)
			if ni != nj {
				return ni < nj
			}
			return strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name)
		})

		result := make([]dlnaItem, 0, len(folders)+len(files))
		result = append(result, folders...)
		result = append(result, files...)

		writeJSON(w, http.StatusOK, result)
	}
}

// ---------------------------------------------------------------------------
//  dlnaStreamHandler — serve file with Range support
// ---------------------------------------------------------------------------

func dlnaStreamHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dlnaRoot := resolveDLNARoot(cfg)
		reqPath := strings.TrimSpace(r.URL.Query().Get("path"))

		reqPath = filepath.Clean(strings.TrimPrefix(reqPath, "/"))
		if strings.Contains(reqPath, "..") || reqPath == "" {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}

		absPath := filepath.Join(dlnaRoot, reqPath)
		if _, err := os.Stat(absPath); err != nil {
			http.NotFound(w, r)
			return
		}

		// Let Go's http.ServeFile handle Range requests, Content-Type, etc.
		http.ServeFile(w, r, absPath)
	}
}

// ---------------------------------------------------------------------------
//  dlnaDeleteHandler — delete file or folder
// ---------------------------------------------------------------------------

func dlnaDeleteHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dlnaRoot := resolveDLNARoot(cfg)
		reqPath := strings.TrimSpace(r.URL.Query().Get("path"))

		reqPath = filepath.Clean(strings.TrimPrefix(reqPath, "/"))
		if strings.Contains(reqPath, "..") || reqPath == "" || reqPath == "." {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid path"})
			return
		}

		absPath := filepath.Join(dlnaRoot, reqPath)

		// Safety: ensure we're within DLNA root.
		absPath, _ = filepath.Abs(absPath)
		absRoot, _ := filepath.Abs(dlnaRoot)
		if !strings.HasPrefix(absPath, absRoot) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "access denied"})
			return
		}

		if err := os.RemoveAll(absPath); err != nil {
			log.Warn().Err(err).Str("path", reqPath).Msg("dlna: delete failed")
			writeJSON(w, http.StatusOK, map[string]any{"success": false})
			return
		}

		log.Info().Str("path", reqPath).Msg("dlna: deleted")
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
	}
}

// ---------------------------------------------------------------------------
//  Helpers
// ---------------------------------------------------------------------------

// resolveDLNARoot returns the absolute path to the DLNA root directory.
func resolveDLNARoot(cfg config.Config) string {
	dlnaPath := cfg.DLNA.Path
	if dlnaPath == "" {
		dlnaPath = "dlna"
	}
	root := filepath.Join(cfg.Compat.RepoRoot, dlnaPath)
	_ = os.MkdirAll(root, 0o755)
	return root
}

// dlnaDirHasMedia checks if a directory contains media files (non-recursive, depth 1).
func dlnaDirHasMedia(dir string, mediaRe *regexp.Regexp) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			return true // subfolders count
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if mediaRe.MatchString(ext) {
			return true
		}
	}
	return false
}

// dlnaCoverURL returns the URL for a cover thumbnail if it exists.
func dlnaCoverURL(host, dlnaRoot, name string) string {
	hash := dlnaMD5(name)
	thumbPath := filepath.Join(dlnaRoot, "thumbs", hash+".jpg")
	if _, err := os.Stat(thumbPath); err == nil {
		return host + "/dlna/stream?path=" + filepath.Join("thumbs", hash+".jpg")
	}
	return ""
}

// dlnaFindSubtitles finds .srt subtitle files matching a base filename.
func dlnaFindSubtitles(host, dir, baseName, parentPath string) []dlnaSubtitle {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var subs []dlnaSubtitle
	idx := 1
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".srt" && ext != ".vtt" && ext != ".ass" && ext != ".ssa" {
			continue
		}
		nameNoExt := strings.TrimSuffix(name, ext)
		if !strings.HasPrefix(nameNoExt, baseName) {
			continue
		}

		subPath := name
		if parentPath != "" && parentPath != "." {
			subPath = filepath.Join(parentPath, name)
		}

		label := "Sub #" + strconv.Itoa(idx)
		// Try to extract language from suffix like "file.en.srt"
		suffix := strings.TrimPrefix(nameNoExt, baseName)
		suffix = strings.TrimLeft(suffix, "._- ")
		if suffix != "" {
			label = suffix
		}

		subs = append(subs, dlnaSubtitle{
			Label: label,
			URL:   host + "/dlna/stream?path=" + subPath,
		})
		idx++
	}
	return subs
}

// dlnaMD5 returns hex-encoded MD5 of a string.
func dlnaMD5(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// extractFirstNumber returns the first number found in a string, or 0.
func extractFirstNumber(s string) int {
	m := dlnaNumberRe.FindString(s)
	if m == "" {
		return 0
	}
	n, _ := strconv.Atoi(m)
	return n
}

// schemeHost returns "http(s)://host" from the request.
func schemeHost(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if fwd := r.Header.Get("X-Forwarded-Proto"); fwd != "" {
		scheme = fwd
	}
	return scheme + "://" + r.Host
}
