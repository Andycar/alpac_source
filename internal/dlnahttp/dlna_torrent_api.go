package dlnahttp

import (
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/dlna"

	"github.com/rs/zerolog/log"
)

// dlnaTorrentManagersHandler returns list of active torrent downloads.
// GET /dlna/tracker/managers
func dlnaTorrentManagersHandler(cfg config.Config, tm *dlna.TorrentManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if tm == nil {
			writeJSON(w, http.StatusOK, []any{})
			return
		}
		writeJSON(w, http.StatusOK, tm.Stats())
	}
}

// dlnaTorrentShowHandler returns files in a torrent (from magnet or URL).
// GET /dlna/tracker/show?path={magnet_or_url}
func dlnaTorrentShowHandler(cfg config.Config, tm *dlna.TorrentManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if tm == nil {
			writeJSON(w, http.StatusOK, map[string]any{"error": "torrent manager not available"})
			return
		}

		path := strings.TrimSpace(r.URL.Query().Get("path"))
		if path == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "path required"})
			return
		}

		mt, err := tm.AddMagnet(path)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
			return
		}

		if !dlna.WaitForMetadata(mt, 3*time.Minute) {
			writeJSON(w, http.StatusOK, map[string]any{"error": "metadata timeout"})
			return
		}

		files := tm.ListFiles(mt.T.InfoHash().HexString())
		writeJSON(w, http.StatusOK, files)
	}
}

// dlnaTorrentDownloadHandler starts downloading a torrent with optional file selection.
// GET /dlna/tracker/download?path={magnet}&indexs=0,1,2
func dlnaTorrentDownloadHandler(cfg config.Config, tm *dlna.TorrentManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if tm == nil {
			writeJSON(w, http.StatusOK, map[string]any{"error": "torrent manager not available"})
			return
		}

		path := strings.TrimSpace(r.URL.Query().Get("path"))
		if path == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "path required"})
			return
		}

		// Parse file indices.
		var indices []int
		if idxStr := strings.TrimSpace(r.URL.Query().Get("indexs")); idxStr != "" {
			for s := range strings.SplitSeq(idxStr, ",") {
				if idx, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
					indices = append(indices, idx)
				}
			}
		}

		mt, err := tm.AddMagnet(path)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
			return
		}

		if !dlna.WaitForMetadata(mt, 3*time.Minute) {
			writeJSON(w, http.StatusOK, map[string]any{"error": "metadata timeout"})
			return
		}

		infoHash := mt.T.InfoHash().HexString()
		if err := tm.StartDownload(infoHash, indices); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
			return
		}

		log.Info().
			Str("name", mt.Name).
			Str("hash", infoHash).
			Int("files", len(indices)).
			Msg("dlna-torrent: download started")

		writeJSON(w, http.StatusOK, map[string]any{
			"success":  true,
			"infoHash": infoHash,
			"name":     mt.Name,
		})
	}
}

// dlnaTorrentStreamHandler streams a specific file from a torrent.
// GET /dlna/tracker/stream?infohash={hash}&index={fileIndex}
func dlnaTorrentStreamHandler(cfg config.Config, tm *dlna.TorrentManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if tm == nil {
			http.Error(w, "torrent manager not available", http.StatusServiceUnavailable)
			return
		}

		infoHash := strings.TrimSpace(r.URL.Query().Get("infohash"))
		indexStr := strings.TrimSpace(r.URL.Query().Get("index"))
		if infoHash == "" || indexStr == "" {
			http.Error(w, "infohash and index required", http.StatusBadRequest)
			return
		}

		fileIndex, err := strconv.Atoi(indexStr)
		if err != nil {
			http.Error(w, "invalid index", http.StatusBadRequest)
			return
		}

		reader, size, err := tm.StreamFile(infoHash, fileIndex)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}

		// Determine filename for Content-Disposition.
		files := tm.ListFiles(infoHash)
		name := "stream"
		if fileIndex < len(files) {
			name = filepath.Base(files[fileIndex].Path)
		}

		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", "inline; filename=\""+name+"\"")
		http.ServeContent(w, r, name, time.Time{}, reader)
		_ = size // ServeContent uses Seek to determine size
	}
}

// dlnaTorrentDeleteHandler stops and removes a torrent.
// GET /dlna/tracker/stop?infohash={hash}
// GET /dlna/tracker/delete?infohash={hash}
func dlnaTorrentDeleteHandler(cfg config.Config, tm *dlna.TorrentManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if tm == nil {
			writeJSON(w, http.StatusOK, map[string]any{"error": "torrent manager not available"})
			return
		}

		infoHash := strings.TrimSpace(r.URL.Query().Get("infohash"))
		if infoHash == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "infohash required"})
			return
		}

		tm.Remove(infoHash)
		log.Info().Str("hash", infoHash).Msg("dlna-torrent: removed")
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
	}
}
