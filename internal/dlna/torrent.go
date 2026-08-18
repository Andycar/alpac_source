// Package dlna provides torrent download/streaming management for the DLNA module.
// It wraps github.com/anacrolix/torrent to provide a simple, safe API.
package dlna

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	"github.com/rs/zerolog/log"
	"golang.org/x/time/rate"
)

// TorrentConfig configures the torrent manager.
type TorrentConfig struct {
	DownloadDir   string   // directory for completed downloads
	DownloadSpeed int      // bytes/sec limit (0 = unlimited)
	UploadSpeed   int      // bytes/sec limit (0 = unlimited)
	Trackers      []string // additional trackers for magnets
}

// speedSnapshot stores bytes-read at a point in time for speed calculation.
type speedSnapshot struct {
	bytesRead int64
	at        time.Time
}

// TorrentManager manages active torrent downloads and provides streaming.
type TorrentManager struct {
	client    *torrent.Client
	cfg       TorrentConfig
	mu        sync.RWMutex
	active    map[metainfo.Hash]*ManagedTorrent
	snapshots map[metainfo.Hash]speedSnapshot
}

// ManagedTorrent wraps an anacrolix/torrent Torrent with metadata.
type ManagedTorrent struct {
	T       *torrent.Torrent
	AddedAt time.Time
	Name    string
}

// TorrentFileInfo describes a file within a torrent.
type TorrentFileInfo struct {
	Index          int    `json:"index"`
	Path           string `json:"path"`
	Length         int64  `json:"length"`
	BytesCompleted int64  `json:"bytesCompleted"`
}

// TorrentMonitor holds real-time download metrics expected by the DLNA JS plugin.
type TorrentMonitor struct {
	DownloadSpeed int64 `json:"downloadSpeed"` // bytes/sec
}

// TorrentStatus describes the state of a managed torrent.
type TorrentStatus struct {
	InfoHash       string            `json:"infoHash"`
	Name           string            `json:"name"`
	Files          []TorrentFileInfo `json:"files"`
	DownloadSpeed  int64             `json:"downloadSpeed"`
	Monitor        TorrentMonitor    `json:"monitor"`
	Progress       float64           `json:"progress"`
	State          string            `json:"state"` // "downloading" | "seeding" | "paused" | "metadata"
	Peers          int               `json:"peers"`
	TotalLength    int64             `json:"totalLength"`
	BytesCompleted int64             `json:"bytesCompleted"`
}

// NewTorrentManager creates a new torrent manager.
func NewTorrentManager(cfg TorrentConfig) (*TorrentManager, error) {
	_ = os.MkdirAll(cfg.DownloadDir, 0o755)

	clientCfg := torrent.NewDefaultClientConfig()
	clientCfg.DataDir = cfg.DownloadDir
	clientCfg.DefaultStorage = storage.NewFileByInfoHash(cfg.DownloadDir)
	clientCfg.Seed = false
	clientCfg.NoUpload = cfg.UploadSpeed == 0
	clientCfg.ListenPort = 0 // random port

	if cfg.DownloadSpeed > 0 {
		clientCfg.DownloadRateLimiter = rate.NewLimiter(rate.Limit(cfg.DownloadSpeed), cfg.DownloadSpeed)
	}
	if cfg.UploadSpeed > 0 {
		clientCfg.UploadRateLimiter = rate.NewLimiter(rate.Limit(cfg.UploadSpeed), cfg.UploadSpeed)
	}

	client, err := torrent.NewClient(clientCfg)
	if err != nil {
		return nil, fmt.Errorf("torrent client: %w", err)
	}

	return &TorrentManager{
		client:    client,
		cfg:       cfg,
		active:    make(map[metainfo.Hash]*ManagedTorrent),
		snapshots: make(map[metainfo.Hash]speedSnapshot),
	}, nil
}

// AddMagnet adds a magnet link and waits for metadata.
// Returns info hash and file list once metadata is available.
func (tm *TorrentManager) AddMagnet(magnet string) (*ManagedTorrent, error) {
	// Add extra trackers if configured.
	if len(tm.cfg.Trackers) > 0 && !strings.Contains(magnet, "&tr=") {
		for _, tr := range tm.cfg.Trackers {
			magnet += "&tr=" + tr
		}
	}

	t, err := tm.client.AddMagnet(magnet)
	if err != nil {
		return nil, fmt.Errorf("add magnet: %w", err)
	}

	mt := &ManagedTorrent{T: t, AddedAt: time.Now()}

	tm.mu.Lock()
	tm.active[t.InfoHash()] = mt
	tm.mu.Unlock()

	return mt, nil
}

// AddTorrentFile adds a torrent from a .torrent file path.
func (tm *TorrentManager) AddTorrentFile(path string) (*ManagedTorrent, error) {
	mi, err := metainfo.LoadFromFile(path)
	if err != nil {
		return nil, fmt.Errorf("load torrent: %w", err)
	}

	t, err := tm.client.AddTorrent(mi)
	if err != nil {
		return nil, fmt.Errorf("add torrent: %w", err)
	}

	mt := &ManagedTorrent{T: t, AddedAt: time.Now(), Name: t.Name()}

	tm.mu.Lock()
	tm.active[t.InfoHash()] = mt
	tm.mu.Unlock()

	return mt, nil
}

// WaitForMetadata blocks until torrent metadata is available or timeout.
func WaitForMetadata(mt *ManagedTorrent, timeout time.Duration) bool {
	select {
	case <-mt.T.GotInfo():
		mt.Name = mt.T.Name()
		return true
	case <-time.After(timeout):
		return false
	}
}

// StartDownload begins downloading all (or selected) files.
func (tm *TorrentManager) StartDownload(infoHash string, fileIndices []int) error {
	mt := tm.Get(infoHash)
	if mt == nil {
		return fmt.Errorf("torrent not found: %s", infoHash)
	}

	<-mt.T.GotInfo()

	files := mt.T.Files()
	if len(fileIndices) > 0 {
		// Only download selected files.
		selected := make(map[int]bool)
		for _, idx := range fileIndices {
			selected[idx] = true
		}
		for i, f := range files {
			if selected[i] {
				f.Download()
			} else {
				f.SetPriority(torrent.PiecePriorityNone)
			}
		}
	} else {
		mt.T.DownloadAll()
	}

	return nil
}

// StreamFile returns a ReadSeeker for a specific file in the torrent,
// prioritizing sequential download for streaming.
func (tm *TorrentManager) StreamFile(infoHash string, fileIndex int) (io.ReadSeeker, int64, error) {
	mt := tm.Get(infoHash)
	if mt == nil {
		return nil, 0, fmt.Errorf("torrent not found: %s", infoHash)
	}

	<-mt.T.GotInfo()
	files := mt.T.Files()
	if fileIndex < 0 || fileIndex >= len(files) {
		return nil, 0, fmt.Errorf("file index %d out of range", fileIndex)
	}

	f := files[fileIndex]
	f.Download()

	reader := f.NewReader()
	reader.SetReadahead(5 << 20) // 5 MB readahead for streaming
	reader.SetResponsive()

	return reader, f.Length(), nil
}

// Get returns a managed torrent by info hash hex string.
func (tm *TorrentManager) Get(infoHash string) *ManagedTorrent {
	var hash metainfo.Hash
	if err := hash.FromHexString(infoHash); err != nil {
		return nil
	}

	tm.mu.RLock()
	mt := tm.active[hash]
	tm.mu.RUnlock()
	return mt
}

// Remove stops and removes a torrent.
func (tm *TorrentManager) Remove(infoHash string) {
	var hash metainfo.Hash
	if err := hash.FromHexString(infoHash); err != nil {
		return
	}

	tm.mu.Lock()
	mt, ok := tm.active[hash]
	delete(tm.active, hash)
	tm.mu.Unlock()

	if ok && mt.T != nil {
		mt.T.Drop()
	}
}

// ListFiles returns file information for a torrent.
func (tm *TorrentManager) ListFiles(infoHash string) []TorrentFileInfo {
	mt := tm.Get(infoHash)
	if mt == nil {
		return nil
	}

	select {
	case <-mt.T.GotInfo():
	default:
		return nil // metadata not yet available
	}

	files := mt.T.Files()
	result := make([]TorrentFileInfo, len(files))
	for i, f := range files {
		result[i] = TorrentFileInfo{
			Index:          i,
			Path:           f.DisplayPath(),
			Length:         f.Length(),
			BytesCompleted: f.BytesCompleted(),
		}
	}
	return result
}

// Stats returns status information for all active torrents.
func (tm *TorrentManager) Stats() []TorrentStatus {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	result := make([]TorrentStatus, 0, len(tm.active))
	for _, mt := range tm.active {
		st := TorrentStatus{
			InfoHash: mt.T.InfoHash().HexString(),
			Name:     mt.Name,
		}

		select {
		case <-mt.T.GotInfo():
			stats := mt.T.Stats()
			st.Peers = stats.ActivePeers
			st.TotalLength = mt.T.Length()
			st.BytesCompleted = mt.T.BytesCompleted()
			if st.TotalLength > 0 {
				st.Progress = float64(st.BytesCompleted) / float64(st.TotalLength) * 100
			}

			if mt.T.Complete().Bool() {
				st.State = "seeding"
			} else {
				st.State = "downloading"
			}

			// Calculate download speed from bytes-read delta.
			hash := mt.T.InfoHash()
			bytesNow := stats.ConnStats.BytesReadUsefulData.Int64()
			now := time.Now()
			if prev, ok := tm.snapshots[hash]; ok {
				dt := now.Sub(prev.at).Seconds()
				if dt > 0.5 {
					st.DownloadSpeed = max(int64(float64(bytesNow-prev.bytesRead)/dt), 0)
					tm.snapshots[hash] = speedSnapshot{bytesRead: bytesNow, at: now}
				} else {
					// Reuse previous speed estimate for very short intervals.
					db := bytesNow - prev.bytesRead
					if db > 0 && prev.at != (time.Time{}) {
						st.DownloadSpeed = int64(float64(db) / dt)
					}
				}
			} else {
				tm.snapshots[hash] = speedSnapshot{bytesRead: bytesNow, at: now}
			}
			st.Monitor = TorrentMonitor{DownloadSpeed: st.DownloadSpeed}

			// File info.
			files := mt.T.Files()
			st.Files = make([]TorrentFileInfo, len(files))
			for i, f := range files {
				st.Files[i] = TorrentFileInfo{
					Index:          i,
					Path:           f.DisplayPath(),
					Length:         f.Length(),
					BytesCompleted: f.BytesCompleted(),
				}
			}
		default:
			st.State = "metadata"
		}

		result = append(result, st)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

// Close shuts down the torrent client and all active torrents.
func (tm *TorrentManager) Close() {
	tm.mu.Lock()
	for hash, mt := range tm.active {
		mt.T.Drop()
		delete(tm.active, hash)
	}
	tm.mu.Unlock()

	if tm.client != nil {
		tm.client.Close()
	}
	log.Info().Msg("dlna-torrent: client closed")
}

// HasActiveTasks returns true if any torrents are downloading.
func (tm *TorrentManager) HasActiveTasks() bool {
	tm.mu.RLock()
	defer tm.mu.RUnlock()

	for _, mt := range tm.active {
		select {
		case <-mt.T.GotInfo():
			if !mt.T.Complete().Bool() {
				return true
			}
		default:
			return true // still fetching metadata
		}
	}
	return false
}

// ---------------------------------------------------------------------------
//  Tracker utilities
// ---------------------------------------------------------------------------

// LoadTrackers reads tracker URLs from a file (one per line).
func LoadTrackers(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var trackers []string
	seen := make(map[string]bool)
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		trackers = append(trackers, line)
	}
	return trackers
}

// SaveTrackers writes tracker URLs to a file (one per line).
func SaveTrackers(path string, trackers []string) error {
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	return os.WriteFile(path, []byte(strings.Join(trackers, "\n")+"\n"), 0o644)
}
