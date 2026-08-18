package torrs

// ---------------------------------------------------------------------------
//  Shared types — no build tags, used by both torrs and !torrs builds.
// ---------------------------------------------------------------------------

// TorrentDB is the persistent form stored in bbolt.
type TorrentDB struct {
	Hash       string `json:"hash"`
	Title      string `json:"title"`
	Poster     string `json:"poster"`
	Data       string `json:"data"`   // arbitrary JSON from Lampa (card info)
	Magnet     string `json:"magnet"` // original magnet/link
	Timestamp  int64  `json:"timestamp"`
	LastAccess int64  `json:"last_access,omitempty"` // unix seconds; restored to ActiveTorrent.lastAccess so disk-cleanup ordering survives reboot.
	Owner      string `json:"owner,omitempty"`       // lampac_token (sha-prefix) of user who added it; empty for anonymous / ephemeral.
}

// TorrentInfo is the API response format compatible with TorrServer MatriX.
type TorrentInfo struct {
	Title     string     `json:"title"`
	Hash      string     `json:"hash"`
	Poster    string     `json:"poster"`
	Data      string     `json:"data"`
	Timestamp int64      `json:"timestamp"`
	FileStats []FileStat `json:"file_stats"`
	// MatriX-compatible progress / aggregate fields.
	Name           string `json:"name,omitempty"`            // info.Name from .torrent
	TorrentSize    int64  `json:"torrent_size,omitempty"`    // total bytes
	BytesCompleted int64  `json:"bytes_completed,omitempty"` // bytes downloaded
	StatString     string `json:"stat_string,omitempty"`     // "Torrent working" / "Torrent fetching metadata"
	Stat           int    `json:"stat,omitempty"`            // 1=getting info, 2=preload, 3=working, 4=closed
}

// FileStat describes a single file within a torrent.
type FileStat struct {
	ID     int    `json:"id"`
	Path   string `json:"path"`
	Length int64  `json:"length"`
}

// Settings mirrors TorrServer MatriX settings format.
//
// Honored at runtime:
//   - PreloadSize      — used by Preload() and GetCacheStatus()
//   - ReaderReadAHead  — used by Stream() to size the reader's prefetch window
//   - CacheSize        — in RAM mode (torrserver.ram_cache) this is the live
//     memory budget and POST /settings re-caps it; in disk mode it stays a
//     MatriX-compat shim (anacrolix file storage has no piece-cache cap)
//
// MatriX-compat shims (accepted but not effective at runtime; documented so
// the Lampa torrent plugin doesn't break, but mutating them via POST
// /settings produces a warn-log):
//   - UseDisk          — reflects the startup storage mode; not settable
//   - TorrentsSavePath — fixed to homeDir/data at server start; change requires restart
type Settings struct {
	CacheSize        int64  `json:"CacheSize"`
	PreloadSize      int64  `json:"PreloadSize"`
	ReaderReadAHead  int    `json:"ReaderReadAHead"`
	UseDisk          bool   `json:"UseDisk"`
	TorrentsSavePath string `json:"TorrentsSavePath"`
}

// CacheStatus is returned by POST /cache {"action":"get"}.
type CacheStatus struct {
	Torrent *CacheTorrentInfo `json:"Torrent,omitempty"`
}

// CacheTorrentInfo holds real-time torrent download metrics.
type CacheTorrentInfo struct {
	ActivePeers      int   `json:"active_peers"`
	PendingPeers     int   `json:"pending_peers"`
	TotalPeers       int   `json:"total_peers"`
	ConnectedSeeders int   `json:"connected_seeders"`
	PreloadedBytes   int64 `json:"preloaded_bytes"`
	PreloadSize      int64 `json:"preload_size"`
	DownloadSpeed    int64 `json:"download_speed"` // bytes/sec
	UploadSpeed      int64 `json:"upload_speed"`   // bytes/sec
}

// TorrentsRequest is the JSON body for POST /torrents.
type TorrentsRequest struct {
	Action   string `json:"action"` // add, get, list, rem, drop, set
	Link     string `json:"link"`   // magnet URI or torrent URL
	Hash     string `json:"hash"`
	Title    string `json:"title"`
	Poster   string `json:"poster"`
	Data     string `json:"data"`
	SaveToDB bool   `json:"save_to_db"`
}

// SettingsRequest is the JSON body for POST /settings.
type SettingsRequest struct {
	Action string `json:"action"` // get, set
	Settings
}

// CacheRequest is the JSON body for POST /cache.
type CacheRequest struct {
	Action string `json:"action"` // get
	Hash   string `json:"hash"`
}

// CleanupReport summarises a single cleanup pass for /admin/api/torrs/cleanup.
type CleanupReport struct {
	StartMB     int64 `json:"start_mb"`
	EndMB       int64 `json:"end_mb"`
	LimitMB     int64 `json:"limit_mb"`
	AgeRemoved  int   `json:"age_removed"`
	SizeRemoved int   `json:"size_removed"`
}

// HealthInfo is the response body of /admin/api/torrs/health.  Used by the
// admin v2 dashboard and external monitoring/alerting.
type HealthInfo struct {
	NumTorrents     int   `json:"num_torrents"`
	BytesCompleted  int64 `json:"bytes_completed"`
	BytesReadUseful int64 `json:"bytes_read_useful"`
	BytesWritten    int64 `json:"bytes_written"`
	DiskUsageMB     int64 `json:"disk_usage_mb"`
	DiskLimitMB     int64 `json:"disk_limit_mb"`
	// RAM-cache mode (torrserver.ram_cache): pieces live in memory, disk_usage
	// stays 0. Evictions is the running count of pieces dropped to stay inside
	// the budget — a number that climbs fast means the cache is too small for
	// the bitrate and pieces get re-downloaded.
	RAMCache            bool  `json:"ram_cache"`
	RAMUsedMB           int64 `json:"ram_used_mb"`
	RAMBudgetMB         int64 `json:"ram_budget_mb"`
	RAMEvictions        int64 `json:"ram_evictions"`
	DHTNodes            int   `json:"dht_nodes"`
	ListenPort          int   `json:"listen_port"`
	UptimeSec           int64 `json:"uptime_sec"`
	Goroutines          int   `json:"goroutines"`
	MaxActiveTorrents   int   `json:"max_active_torrents"`
	MaxDownloadSpeedMBs int   `json:"max_download_speed_mbs"`
	MaxUploadSpeedMBs   int   `json:"max_upload_speed_mbs"`
	DHTEnabled          bool  `json:"dht_enabled"`
	UploadEnabled       bool  `json:"upload_enabled"`
	CleanupEnabled      bool  `json:"cleanup_enabled"`
	Now                 int64 `json:"now"`
}

// TorrsConfig holds configuration for the in-process torrent server.
type TorrsConfig struct {
	HomeDir string
	// RAMCache switches piece storage from <HomeDir>/data files to memory, with
	// LRU eviction bounded by CacheSizeMB. Nothing torrent-sized is written to
	// disk. Off by default: a busy multi-user server wants the disk cache.
	RAMCache      bool
	CacheSizeMB   int // RAM mode: the memory budget (floor 128 MB). Disk mode: MatriX shim, default 64
	DiskCacheMB   int // disk cache limit, default 1024; 0 ⇒ fall back to CacheCleanupMaxGB.
	PreloadMB     int // preload size, default 5
	DisableDHT    bool
	DisableUpload bool // default true — must be explicitly set false to seed.
	// Rate limits (0 = unlimited).
	MaxDownloadSpeedMBs int // MB/s download cap
	MaxUploadSpeedMBs   int // MB/s upload cap
	// Resource caps.
	MaxActiveTorrents int // 0 = unlimited; on new Add, oldest non-recent torrents are dropped.
	// Reader readahead (bytes); 0 → defaults to ReaderReadAHead % of file size, bounded 16-256 MiB.
	ReaderReadAheadMB int
	// Auto-cleanup tuning (mirrors TorrServerConfig naming).
	CacheCleanupEnable bool // false ⇒ disk-cleanup loop is skipped entirely
	CacheCleanupDays   int  // delete torrents older than N days (by Timestamp); 0 disables age-based cleanup
	CacheCleanupMaxGB  int  // legacy alias: if DiskCacheMB is 0, use this (in GB); ignored otherwise
}
