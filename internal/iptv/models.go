package iptv

import "time"

// ---------------------------------------------------------------------------
//  Playlist
// ---------------------------------------------------------------------------

// Playlist represents a user-added M3U playlist.
type Playlist struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	URL          string   `json:"url"`
	ProxyMode    string   `json:"proxy_mode"` // "none" | "all"
	// UserAgent — кастомный UA плейлиста: им скачивается сам M3U (панели фильтруют по UA —
	// «не читает плейлист», особенно ссылки без .m3u вида get.php), и он же — заголовок
	// стримов, когда у канала нет собственного user-agent.
	UserAgent string   `json:"user_agent,omitempty"`
	EPGUrls   []string `json:"epg_urls"` // действующие EPG-адреса (см. EPGManual)
	// EPGManual — адреса программы, заданные РУКАМИ при добавлении; выигрывают у url-tvg
	// из заголовка M3U и переживают каждый refresh.
	EPGManual    []string `json:"epg_manual,omitempty"`
	ChannelCount int      `json:"channel_count"`
	IsGlobal     bool     `json:"is_global"`
	CreatedAt    int64    `json:"created_at"`
	UpdatedAt    int64    `json:"updated_at"`
}

// ---------------------------------------------------------------------------
//  Channel
// ---------------------------------------------------------------------------

// Channel is a single entry parsed from an M3U playlist.
type Channel struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	CleanName  string  `json:"clean_name"` // without quality tags like HD/FHD/4K
	URL        string  `json:"url"`
	Logo       string  `json:"logo,omitempty"`
	Group      string  `json:"group,omitempty"`
	TvgID      string  `json:"tvg_id,omitempty"`
	TvgName    string  `json:"tvg_name,omitempty"`
	Number     int     `json:"number,omitempty"`
	Quality    string  `json:"quality,omitempty"` // "SD" | "HD" | "FHD" | "4K"
	UserAgent  string  `json:"user_agent,omitempty"`
	Referer    string  `json:"referer,omitempty"`
	Catchup    *Catchup `json:"catchup,omitempty"`
	PlaylistID string  `json:"playlist_id"`
}

// Catchup describes catchup/timeshift parameters for a channel.
type Catchup struct {
	Type   string `json:"type,omitempty"`   // "default" | "flussonic" | "shift" | "append"
	Days   int    `json:"days,omitempty"`
	Source string `json:"source,omitempty"`
}

// ---------------------------------------------------------------------------
//  M3U Header
// ---------------------------------------------------------------------------

// M3UHeader holds metadata parsed from the #EXTM3U line.
type M3UHeader struct {
	EPGUrls  []string // x-tvg-url values
	CatchupType string
	CatchupDays int
}

// ---------------------------------------------------------------------------
//  Playlist Cache (stored on disk)
// ---------------------------------------------------------------------------

// PlaylistCache is the full parse result of a playlist, cached for fast access.
type PlaylistCache struct {
	Playlist Playlist  `json:"playlist"`
	Channels []Channel `json:"channels"`
	Groups   []string  `json:"groups"`
	ParsedAt time.Time `json:"parsed_at"`
}

// ---------------------------------------------------------------------------
//  Groups summary
// ---------------------------------------------------------------------------

// GroupInfo holds summary info for a channel group.
type GroupInfo struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
	Logo  string `json:"logo,omitempty"` // first channel's logo
}
