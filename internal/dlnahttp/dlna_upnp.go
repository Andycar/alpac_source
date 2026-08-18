package dlnahttp

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/dms/dlna"
	dms "github.com/anacrolix/dms/dlna/dms"
	"github.com/anacrolix/dms/upnpav"
	"github.com/rs/zerolog/log"

	"lampac-go/internal/config"
	dlnapkg "lampac-go/internal/dlna"
)

// DLNAUPnPServer wraps anacrolix/dms to provide a real UPnP/DLNA MediaServer
// with SSDP discovery. Smart TVs and DLNA clients can auto-discover the server.
type DLNAUPnPServer struct {
	srv        *dms.Server
	cfg        config.Config
	torrentMgr *dlnapkg.TorrentManager
	dlnaRoot   string
	listener   net.Listener
	cancel     context.CancelFunc
	closeOnce  sync.Once
}

// NewDLNAUPnPServer creates and initialises (but does not start) the UPnP server.
func NewDLNAUPnPServer(cfg config.Config, tm *dlnapkg.TorrentManager) (*DLNAUPnPServer, error) {
	dlnaRoot := resolveDLNARoot(cfg)

	port := cfg.DLNA.UPnPPort
	if port <= 0 {
		port = 1338
	}

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return nil, fmt.Errorf("dlna-upnp: listen :%d: %w", port, err)
	}

	name := cfg.DLNA.FriendlyName
	if name == "" {
		name = "Lampac DLNA"
	}

	d := &DLNAUPnPServer{
		cfg:        cfg,
		torrentMgr: tm,
		dlnaRoot:   dlnaRoot,
		listener:   ln,
	}

	d.srv = &dms.Server{
		HTTPConn:               ln,
		FriendlyName:           name,
		RootObjectPath:         dlnaRoot,
		NoTranscode:            true,
		NoProbe:                true,
		IgnoreHidden:           true,
		IgnorePaths:            []string{"thumbs", "tmdb", "temp", "torrents"},
		OnBrowseDirectChildren: d.browseChildren,
		OnBrowseMetadata:       d.browseMetadata,
	}

	if err := d.srv.Init(); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("dlna-upnp: init: %w", err)
	}

	log.Info().
		Str("name", name).
		Int("port", port).
		Str("root", dlnaRoot).
		Msg("dlna-upnp: server initialised")

	return d, nil
}

// Start runs the UPnP SSDP + HTTP server in a background goroutine.
// If the SSDP server panics (e.g. "too many concurrent operations on a single
// file or socket" in anacrolix/dms/ssdp), we recover, log, and restart with
// exponential backoff instead of crashing the whole process.
func (d *DLNAUPnPServer) Start(ctx context.Context) {
	ctx, d.cancel = context.WithCancel(ctx)
	go d.runLoop(ctx)
}

// runLoop keeps restarting the UPnP server on panic/error with backoff.
func (d *DLNAUPnPServer) runLoop(ctx context.Context) {
	const (
		maxBackoff     = 5 * time.Minute
		initialBackoff = 5 * time.Second
		maxRestarts    = 10 // after this many restarts, give up
	)
	backoff := initialBackoff
	restarts := 0

	for {
		if ctx.Err() != nil {
			return
		}

		err := d.runOnce(ctx)

		if ctx.Err() != nil {
			// Normal shutdown.
			return
		}

		restarts++
		if err != nil {
			log.Error().Err(err).Int("restarts", restarts).Msg("dlna-upnp: server stopped")
		}
		if restarts >= maxRestarts {
			log.Error().Int("restarts", restarts).Msg("dlna-upnp: too many restarts, disabling UPnP server")
			return
		}

		log.Warn().Dur("backoff", backoff).Msg("dlna-upnp: restarting after failure")
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// runOnce runs the UPnP server once. Returns error on failure; recovers panics.
func (d *DLNAUPnPServer) runOnce(ctx context.Context) (retErr error) {
	defer func() {
		if r := recover(); r != nil {
			retErr = fmt.Errorf("panic: %v", r)
			log.Error().Interface("panic", r).Msg("dlna-upnp: recovered from panic in SSDP server")
		}
	}()

	// Re-initialise the dms.Server before each attempt so it gets fresh sockets.
	if err := d.reinit(); err != nil {
		return fmt.Errorf("reinit: %w", err)
	}

	errCh := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				errCh <- fmt.Errorf("panic in dms.Server.Run: %v", r)
			}
		}()
		errCh <- d.srv.Run()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		_ = d.srv.Close()
		return nil
	}
}

// reinit recreates the dms.Server with fresh state. Both the TCP listener and
// internal SSDP sockets are rebuilt because dms.Server.Close() closes the
// HTTPConn listener too.
func (d *DLNAUPnPServer) reinit() error {
	// Close old server silently (may already be closed after panic).
	if d.srv != nil {
		_ = d.srv.Close()
	}

	name := d.cfg.DLNA.FriendlyName
	if name == "" {
		name = "Lampac DLNA"
	}

	port := d.cfg.DLNA.UPnPPort
	if port <= 0 {
		port = 1338
	}

	// Always re-open the TCP listener because dms.Server.Close() closes it.
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("listen :%d: %w", port, err)
	}
	d.listener = ln

	d.srv = &dms.Server{
		HTTPConn:               ln,
		FriendlyName:           name,
		RootObjectPath:         d.dlnaRoot,
		NoTranscode:            true,
		NoProbe:                true,
		IgnoreHidden:           true,
		IgnorePaths:            []string{"thumbs", "tmdb", "temp", "torrents"},
		OnBrowseDirectChildren: d.browseChildren,
		OnBrowseMetadata:       d.browseMetadata,
	}

	return d.srv.Init()
}

// Stop gracefully shuts down the UPnP server.
//
// Wrapped in a recover-guard because github.com/anacrolix/dms v1.7.2's
// (*Server).Close() is not internally idempotent — it closes some of
// its own channels twice during a normal shutdown sequence, panicking
// with "close of closed channel". We can't fix upstream from here, but
// we can stop that panic from taking the whole process down.
//
// Before this guard systemd saw lampac exit 2 on every restart/stop,
// flap-restarted every ~2 min, and DLNA never came up clean. With the
// recover the panic is logged but Shutdown completes normally.
func (d *DLNAUPnPServer) Stop() {
	d.closeOnce.Do(func() {
		if d.cancel != nil {
			d.cancel()
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Warn().Interface("panic", r).
						Msg("dlna-upnp: anacrolix/dms Close panicked (suppressed; library bug)")
				}
			}()
			_ = d.srv.Close()
		}()
		log.Info().Msg("dlna-upnp: server stopped")
	})
}

// ---------------------------------------------------------------------------
//  ContentDirectory callbacks
// ---------------------------------------------------------------------------

// browseChildren returns child objects for a given path in the virtual tree.
//
// Virtual tree layout:
//
//	/              → root: "Файлы" + "Торренты"
//	/Files/...     → filesystem under dlnaRoot
//	/Torrents      → one container per active torrent
//	/Torrents/{hash}  → files within that torrent
func (d *DLNAUPnPServer) browseChildren(objPath, rootPath, host, userAgent string) ([]any, error) {
	// Normalise.
	objPath = path.Clean(objPath)
	if objPath == "." || objPath == "" {
		objPath = "/"
	}

	switch {
	case objPath == "/":
		return d.browseRoot(host)

	case objPath == "/Files":
		return d.browseDir("/Files", d.dlnaRoot, host)

	case strings.HasPrefix(objPath, "/Files/"):
		rel := strings.TrimPrefix(objPath, "/Files")
		fsPath := filepath.Join(d.dlnaRoot, filepath.FromSlash(rel))
		return d.browseDir(objPath, fsPath, host)

	case objPath == "/Torrents":
		return d.browseTorrents(host)

	case strings.HasPrefix(objPath, "/Torrents/"):
		hash := strings.TrimPrefix(objPath, "/Torrents/")
		if idx := strings.IndexByte(hash, '/'); idx >= 0 {
			hash = hash[:idx]
		}
		return d.browseTorrentFiles(hash, host)

	default:
		return nil, nil
	}
}

// browseMetadata returns a single object's metadata.
func (d *DLNAUPnPServer) browseMetadata(objPath, rootPath, host, userAgent string) (any, error) {
	objPath = path.Clean(objPath)
	if objPath == "." || objPath == "" {
		objPath = "/"
	}

	switch {
	case objPath == "/":
		return upnpav.Container{
			Object: upnpav.Object{
				ID:         "0",
				ParentID:   "-1",
				Restricted: 1,
				Title:      d.cfg.DLNA.FriendlyName,
				Class:      "object.container.storageFolder",
			},
			ChildCount: 2,
		}, nil

	case objPath == "/Files":
		return upnpav.Container{
			Object: upnpav.Object{
				ID:         url.QueryEscape(objPath),
				ParentID:   "0",
				Restricted: 1,
				Title:      "Файлы",
				Class:      "object.container.storageFolder",
			},
		}, nil

	case objPath == "/Torrents":
		return upnpav.Container{
			Object: upnpav.Object{
				ID:         url.QueryEscape(objPath),
				ParentID:   "0",
				Restricted: 1,
				Title:      "Торренты",
				Class:      "object.container.storageFolder",
			},
		}, nil

	case strings.HasPrefix(objPath, "/Files/"):
		return d.fileMetadata(objPath, host)

	case strings.HasPrefix(objPath, "/Torrents/"):
		hash := strings.TrimPrefix(objPath, "/Torrents/")
		return upnpav.Container{
			Object: upnpav.Object{
				ID:         url.QueryEscape(objPath),
				ParentID:   url.QueryEscape("/Torrents"),
				Restricted: 1,
				Title:      hash,
				Class:      "object.container.storageFolder",
			},
		}, nil

	default:
		return nil, nil
	}
}

// ---------------------------------------------------------------------------
//  Root
// ---------------------------------------------------------------------------

func (d *DLNAUPnPServer) browseRoot(host string) ([]any, error) {
	items := []any{
		upnpav.Container{
			Object: upnpav.Object{
				ID:         url.QueryEscape("/Files"),
				ParentID:   "0",
				Restricted: 1,
				Title:      "Файлы",
				Class:      "object.container.storageFolder",
			},
		},
	}
	if d.torrentMgr != nil {
		items = append(items, upnpav.Container{
			Object: upnpav.Object{
				ID:         url.QueryEscape("/Torrents"),
				ParentID:   "0",
				Restricted: 1,
				Title:      "Торренты",
				Class:      "object.container.storageFolder",
			},
		})
	}
	return items, nil
}

// ---------------------------------------------------------------------------
//  Files
// ---------------------------------------------------------------------------

func (d *DLNAUPnPServer) browseDir(virtualDir, fsPath string, host string) ([]any, error) {
	entries, err := os.ReadDir(fsPath)
	if err != nil {
		return nil, err
	}

	parentID := url.QueryEscape(virtualDir)

	type entry struct {
		name  string
		isDir bool
		info  os.FileInfo
	}
	var sorted []entry
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		lower := strings.ToLower(name)
		if lower == "thumbs" || lower == "tmdb" || lower == "temp" || lower == "torrents" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		sorted = append(sorted, entry{name: name, isDir: e.IsDir(), info: info})
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].isDir != sorted[j].isDir {
			return sorted[i].isDir // folders first
		}
		return strings.ToLower(sorted[i].name) < strings.ToLower(sorted[j].name)
	})

	var result []any
	for _, e := range sorted {
		childPath := virtualDir + "/" + e.name
		childID := url.QueryEscape(childPath)

		if e.isDir {
			result = append(result, upnpav.Container{
				Object: upnpav.Object{
					ID:         childID,
					ParentID:   parentID,
					Restricted: 1,
					Title:      e.name,
					Class:      "object.container.storageFolder",
					Date:       upnpav.Timestamp{Time: e.info.ModTime()},
				},
			})
			continue
		}

		mt := mimeByName(e.name)
		if mt == "" || (!strings.HasPrefix(mt, "video/") && !strings.HasPrefix(mt, "audio/") && !strings.HasPrefix(mt, "image/")) {
			continue
		}

		obj := upnpav.Object{
			ID:         childID,
			ParentID:   parentID,
			Restricted: 1,
			Title:      e.name,
			Date:       upnpav.Timestamp{Time: e.info.ModTime()},
		}

		// Class based on MIME type.
		switch {
		case strings.HasPrefix(mt, "video/"):
			obj.Class = "object.item.videoItem"
		case strings.HasPrefix(mt, "audio/"):
			obj.Class = "object.item.audioItem"
		case strings.HasPrefix(mt, "image/"):
			obj.Class = "object.item.imageItem"
		}

		// Thumbnail.
		thumbURL := d.thumbnailURL(host, e.name)
		if thumbURL != "" {
			obj.AlbumArtURI = thumbURL
			obj.Icon = thumbURL
		}

		// Resource URL — relative path from dlnaRoot for streaming.
		relPath := strings.TrimPrefix(childPath, "/Files")
		resURL := (&url.URL{
			Scheme: "http",
			Host:   host,
			Path:   "/dlna/upnp/res",
			RawQuery: url.Values{
				"path": {relPath},
			}.Encode(),
		}).String()

		item := upnpav.Item{
			Object: obj,
			Res: []upnpav.Resource{{
				URL: resURL,
				ProtocolInfo: fmt.Sprintf("http-get:*:%s:%s", mt, dlna.ContentFeatures{
					SupportRange: true,
				}.String()),
				Size: uint64(e.info.Size()),
			}},
		}
		result = append(result, item)
	}
	return result, nil
}

func (d *DLNAUPnPServer) fileMetadata(objPath, host string) (any, error) {
	rel := strings.TrimPrefix(objPath, "/Files")
	fsPath := filepath.Join(d.dlnaRoot, filepath.FromSlash(rel))

	info, err := os.Stat(fsPath)
	if err != nil {
		return nil, err
	}

	parentPath := path.Dir(objPath)
	parentID := url.QueryEscape(parentPath)
	childID := url.QueryEscape(objPath)

	if info.IsDir() {
		return upnpav.Container{
			Object: upnpav.Object{
				ID:         childID,
				ParentID:   parentID,
				Restricted: 1,
				Title:      info.Name(),
				Class:      "object.container.storageFolder",
				Date:       upnpav.Timestamp{Time: info.ModTime()},
			},
		}, nil
	}

	mt := mimeByName(info.Name())
	if mt == "" {
		mt = "application/octet-stream"
	}
	obj := upnpav.Object{
		ID:         childID,
		ParentID:   parentID,
		Restricted: 1,
		Title:      info.Name(),
		Class:      "object.item.videoItem",
		Date:       upnpav.Timestamp{Time: info.ModTime()},
	}

	resURL := (&url.URL{
		Scheme: "http",
		Host:   host,
		Path:   "/dlna/upnp/res",
		RawQuery: url.Values{
			"path": {rel},
		}.Encode(),
	}).String()

	return upnpav.Item{
		Object: obj,
		Res: []upnpav.Resource{{
			URL: resURL,
			ProtocolInfo: fmt.Sprintf("http-get:*:%s:%s", mt, dlna.ContentFeatures{
				SupportRange: true,
			}.String()),
			Size: uint64(info.Size()),
		}},
	}, nil
}

// ---------------------------------------------------------------------------
//  Torrents
// ---------------------------------------------------------------------------

func (d *DLNAUPnPServer) browseTorrents(host string) ([]any, error) {
	if d.torrentMgr == nil {
		return nil, nil
	}
	stats := d.torrentMgr.Stats()
	var result []any
	for _, st := range stats {
		title := st.Name
		if title == "" {
			title = st.InfoHash
		}
		result = append(result, upnpav.Container{
			Object: upnpav.Object{
				ID:         url.QueryEscape("/Torrents/" + st.InfoHash),
				ParentID:   url.QueryEscape("/Torrents"),
				Restricted: 1,
				Title:      title,
				Class:      "object.container.storageFolder",
			},
			ChildCount: len(st.Files),
		})
	}
	return result, nil
}

func (d *DLNAUPnPServer) browseTorrentFiles(infoHash, host string) ([]any, error) {
	if d.torrentMgr == nil {
		return nil, nil
	}
	files := d.torrentMgr.ListFiles(infoHash)
	parentID := url.QueryEscape("/Torrents/" + infoHash)

	var result []any
	for _, f := range files {
		name := path.Base(f.Path)
		mt := mimeByName(name)
		if mt == "" || (!strings.HasPrefix(mt, "video/") && !strings.HasPrefix(mt, "audio/")) {
			continue
		}

		resURL := (&url.URL{
			Scheme: "http",
			Host:   host,
			Path:   "/dlna/upnp/torrent-stream",
			RawQuery: url.Values{
				"infohash": {infoHash},
				"index":    {fmt.Sprintf("%d", f.Index)},
			}.Encode(),
		}).String()

		obj := upnpav.Object{
			ID:         url.QueryEscape(fmt.Sprintf("/Torrents/%s/%d", infoHash, f.Index)),
			ParentID:   parentID,
			Restricted: 1,
			Title:      name,
			Class:      "object.item.videoItem",
		}

		item := upnpav.Item{
			Object: obj,
			Res: []upnpav.Resource{{
				URL: resURL,
				ProtocolInfo: fmt.Sprintf("http-get:*:%s:%s", mt, dlna.ContentFeatures{
					SupportRange: true,
				}.String()),
				Size: uint64(f.Length),
			}},
		}
		result = append(result, item)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
//  HTTP handlers for resource streaming (registered on main router)
// ---------------------------------------------------------------------------

// dlnaUPnPResHandler serves files from the dlna directory for DLNA clients.
func dlnaUPnPResHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		relPath := r.URL.Query().Get("path")
		if relPath == "" {
			http.Error(w, "missing path", http.StatusBadRequest)
			return
		}
		dlnaRoot := resolveDLNARoot(cfg)
		absPath := filepath.Join(dlnaRoot, filepath.FromSlash(filepath.Clean(relPath)))

		// Path traversal protection.
		if !strings.HasPrefix(absPath, dlnaRoot) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		// DLNA headers.
		w.Header().Set("transferMode.dlna.org", "Streaming")
		w.Header().Set("contentFeatures.dlna.org", dlna.ContentFeatures{
			SupportRange: true,
		}.String())

		http.ServeFile(w, r, absPath)
	}
}

// dlnaUPnPTorrentStreamHandler streams a file from an active torrent for DLNA clients.
func dlnaUPnPTorrentStreamHandler(tm *dlnapkg.TorrentManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if tm == nil {
			http.Error(w, "torrent manager unavailable", http.StatusServiceUnavailable)
			return
		}
		infoHash := r.URL.Query().Get("infohash")
		indexStr := r.URL.Query().Get("index")
		if infoHash == "" || indexStr == "" {
			http.Error(w, "missing infohash or index", http.StatusBadRequest)
			return
		}
		var fileIndex int
		if _, err := fmt.Sscanf(indexStr, "%d", &fileIndex); err != nil {
			http.Error(w, "bad index", http.StatusBadRequest)
			return
		}

		reader, size, err := tm.StreamFile(infoHash, fileIndex)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}

		// Determine filename for content-type.
		files := tm.ListFiles(infoHash)
		name := "stream"
		for _, f := range files {
			if f.Index == fileIndex {
				name = path.Base(f.Path)
				break
			}
		}

		w.Header().Set("transferMode.dlna.org", "Streaming")
		w.Header().Set("contentFeatures.dlna.org", dlna.ContentFeatures{
			SupportRange: true,
		}.String())

		rs, ok := reader.(io.ReadSeeker)
		if !ok {
			http.Error(w, "stream not seekable", http.StatusInternalServerError)
			return
		}
		_ = size
		http.ServeContent(w, r, name, time.Time{}, rs)
	}
}

// ---------------------------------------------------------------------------
//  Helpers
// ---------------------------------------------------------------------------

func (d *DLNAUPnPServer) thumbnailURL(host, fileName string) string {
	hash := dlnaMD5(fileName)
	thumbPath := filepath.Join(d.dlnaRoot, "thumbs", hash+".jpg")
	if _, err := os.Stat(thumbPath); err == nil {
		return (&url.URL{
			Scheme: "http",
			Host:   host,
			Path:   "/dlna/upnp/res",
			RawQuery: url.Values{
				"path": {"/thumbs/" + hash + ".jpg"},
			}.Encode(),
		}).String()
	}
	return ""
}

func mimeByName(name string) string {
	ext := path.Ext(name)
	if ext == "" {
		return ""
	}
	mt := mime.TypeByExtension(ext)
	if mt == "" {
		// Common video types not always in Go's mime DB.
		switch strings.ToLower(ext) {
		case ".mkv":
			return "video/x-matroska"
		case ".avi":
			return "video/avi"
		case ".ts":
			return "video/mp2t"
		case ".m2ts", ".mts":
			return "video/mp2t"
		case ".flac":
			return "audio/flac"
		case ".opus":
			return "audio/opus"
		}
	}
	return mt
}
