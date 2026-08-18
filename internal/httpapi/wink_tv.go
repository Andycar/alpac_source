package httpapi

// wink_tv.go — Wink live TV: fetch the channel list, keep only clear-HLS
// (isCrypted=false) channels, and render them as an M3U playlist + per-channel
// resolve. Field names follow the app's Gson LOWER_CASE_WITH_UNDERSCORES policy;
// `sources` and `themes` are explicit @SerializedName overrides on the Channel
// model (Kotlin fields _sources / themesUnsafe).

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// Models
// ---------------------------------------------------------------------------

type winkChannelList struct {
	Items      []winkChannel `json:"items"`
	TotalItems int           `json:"total_items"`
}

type winkChannel struct {
	ID             int          `json:"id"`
	Name           string       `json:"name"`
	Number         int          `json:"number"`
	Logo           string       `json:"logo"`
	FullLogo       string       `json:"full_logo"`
	Background     string       `json:"background"`
	Description    string       `json:"description"`
	Sources        []winkSource `json:"sources"` // @SerializedName("sources") -> _sources
	Themes         []int        `json:"themes"`  // @SerializedName("themes")  -> themesUnsafe
	IsErotic       bool         `json:"is_erotic"`
	IsAuthRequired bool         `json:"is_auth_required"`
	IsBarker       bool         `json:"is_barker"` // preview/barker channel
	UsageModel     string       `json:"usage_model"`
}

type winkSource struct {
	URL         string              `json:"url"`
	Urls        *winkContentDrmUrls `json:"urls"`
	Type        string              `json:"type"`
	IsCrypted   bool                `json:"is_crypted"`
	IsOttDvr    bool                `json:"is_ott_dvr"`
	NpvrID      string              `json:"npvr_id"`
	IsPurchased bool                `json:"is_purchased"`
}

type winkContentDrmUrls struct {
	Widevine string `json:"widevine"`
}

// clearStream returns the first non-DRM HLS source URL, or "" if the channel is
// only available DRM-encrypted (Widevine) — which Lampa cannot play.
func (c *winkChannel) clearStream() string {
	for i := range c.Sources {
		s := &c.Sources[i]
		if !s.IsCrypted && s.URL != "" {
			return s.URL
		}
	}
	return ""
}

// winkTvDictionary maps theme ids to human names for M3U group-title.
type winkTvDictionary struct {
	Themes []winkTheme `json:"themes"`
}

type winkTheme struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// ---------------------------------------------------------------------------
// API
// ---------------------------------------------------------------------------

// getChannels fetches the full channel list (with stream details).
func (w *winkChecker) getChannels(ctx context.Context) (*winkChannelList, error) {
	if err := w.ensureSession(ctx); err != nil {
		return nil, err
	}
	u := w.userURL("user/channels") + "?with_details=true"
	data, code, err := w.requestRetry(ctx, http.MethodGet, u, nil, true)
	if err != nil {
		return nil, fmt.Errorf("wink: getChannels: %w", err)
	}
	if e := parseWinkError(data); e != nil {
		return nil, fmt.Errorf("wink: getChannels error %d: %s", e.ErrorCode, e.Description)
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("wink: getChannels http %d: %s", code, snippet(data))
	}
	var list winkChannelList
	if err := stdjson.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("wink: getChannels decode: %w", err)
	}
	return &list, nil
}

// getTvDictionary fetches the theme id→name map for group-title labels. Best
// effort: returns an empty map on any error.
func (w *winkChecker) getTvDictionary(ctx context.Context) map[int]string {
	out := map[int]string{}
	u := w.userURL("user/tv_dictionaries")
	data, code, err := w.requestRetry(ctx, http.MethodGet, u, nil, true)
	if err != nil || code != http.StatusOK {
		return out
	}
	var d winkTvDictionary
	if err := stdjson.Unmarshal(data, &d); err != nil {
		return out
	}
	for _, t := range d.Themes {
		out[t.ID] = t.Name
	}
	return out
}

// ---------------------------------------------------------------------------
// M3U rendering
// ---------------------------------------------------------------------------

// channelStats summarises clear vs DRM coverage for diagnostics.
type winkChannelStats struct {
	Total    int
	Clear    int
	DRMOnly  int
	NoSource int
}

func winkComputeStats(list *winkChannelList) winkChannelStats {
	var st winkChannelStats
	for i := range list.Items {
		c := &list.Items[i]
		st.Total++
		switch {
		case c.clearStream() != "":
			st.Clear++
		case len(c.Sources) == 0:
			st.NoSource++
		default:
			st.DRMOnly++
		}
	}
	return st
}

// buildM3U renders the clear-HLS channels as an M3U playlist. streamURL maps a
// channel to the URL Lampa should request (typically a /wink/<id>.m3u8 resolve
// endpoint so the real CDN URL is fetched + proxied server-side at play time).
func buildM3U(list *winkChannelList, themes map[int]string, streamURL func(c *winkChannel) string) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n")

	chans := make([]*winkChannel, 0, len(list.Items))
	for i := range list.Items {
		c := &list.Items[i]
		if c.IsBarker || c.clearStream() == "" {
			continue // skip preview/barker channels and DRM-only channels
		}
		chans = append(chans, c)
	}
	sort.SliceStable(chans, func(i, j int) bool { return chans[i].Number < chans[j].Number })

	for _, c := range chans {
		group := "Wink"
		if len(c.Themes) > 0 {
			if name, ok := themes[c.Themes[0]]; ok && name != "" {
				group = name
			}
		}
		logo := c.Logo
		if logo == "" {
			logo = c.FullLogo
		}
		fmt.Fprintf(&b, "#EXTINF:-1 tvg-id=\"%d\" tvg-name=\"%s\" tvg-logo=\"%s\" group-title=\"%s\",%s\n",
			c.ID, m3uEscape(c.Name), logo, m3uEscape(group), c.Name)
		b.WriteString(streamURL(c) + "\n")
	}
	return b.String()
}

// m3uEscape neutralises characters that would break #EXTINF attribute parsing.
func m3uEscape(s string) string {
	s = strings.ReplaceAll(s, "\"", "'")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, ",", " ")
	return strings.TrimSpace(s)
}

// resolveChannelStream returns the clear-HLS URL for a single channel id, or ""
// if the channel is DRM-only / not found.
func (w *winkChecker) resolveChannelStream(ctx context.Context, channelID int) (string, error) {
	if err := w.ensureSession(ctx); err != nil {
		return "", err
	}
	u := w.userURL("user/channels/" + strconv.Itoa(channelID))
	data, code, err := w.requestRetry(ctx, http.MethodGet, u, nil, true)
	if err != nil {
		return "", fmt.Errorf("wink: getChannel %d: %w", channelID, err)
	}
	if e := parseWinkError(data); e != nil {
		return "", fmt.Errorf("wink: getChannel %d error %d: %s", channelID, e.ErrorCode, e.Description)
	}
	if code != http.StatusOK {
		return "", fmt.Errorf("wink: getChannel %d http %d: %s", channelID, code, snippet(data))
	}
	var c winkChannel
	if err := stdjson.Unmarshal(data, &c); err != nil {
		return "", fmt.Errorf("wink: getChannel %d decode: %w", channelID, err)
	}
	if url := c.clearStream(); url != "" {
		return url, nil
	}
	return "", fmt.Errorf("wink: channel %d is DRM-only (no clear HLS)", channelID)
}
