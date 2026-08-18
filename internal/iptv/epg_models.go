package iptv

import "time"

// ---------------------------------------------------------------------------
//  EPG models
// ---------------------------------------------------------------------------

// EPGProgram represents a single TV program entry from XMLTV.
type EPGProgram struct {
	Start       time.Time `json:"start"`
	Stop        time.Time `json:"stop"`
	ChannelID   string    `json:"channel_id"`   // XMLTV channel attribute
	Title       string    `json:"title"`
	Description string    `json:"desc,omitempty"`
	Category    string    `json:"category,omitempty"`
	Icon        string    `json:"icon,omitempty"`
}

// EPGNowNext holds the currently airing and next program for a channel.
type EPGNowNext struct {
	ChannelID string      `json:"channel_id"`
	Now       *EPGProgram `json:"now,omitempty"`
	Next      *EPGProgram `json:"next,omitempty"`
}

// EPGChannel holds metadata about a channel from the XMLTV <channel> element.
type EPGChannel struct {
	ID       string   `json:"id"`
	Name     string   `json:"name,omitempty"`
	Icon     string   `json:"icon,omitempty"`
	AltNames []string `json:"-"` // all display-name values (for name-based matching)
}
