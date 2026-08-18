package ytauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	ytAPIBase    = "https://www.googleapis.com/youtube/v3"
	ytFeedTTL    = 5 * time.Minute
	maxSubResult = 50 // max subscriptions to fetch
)

// YTVideo represents a video card in the feed.
type YTVideo struct {
	VideoID     string `json:"video_id"`
	Title       string `json:"title"`
	Channel     string `json:"channel"`
	ChannelID   string `json:"channel_id"`
	Thumbnail   string `json:"thumbnail"`
	PublishedAt string `json:"published_at"`
	Duration    string `json:"duration"`
}

// YTPlaylist represents a user playlist.
type YTPlaylist struct {
	PlaylistID string `json:"playlist_id"`
	Title      string `json:"title"`
	Thumbnail  string `json:"thumbnail"`
	ItemCount  int    `json:"item_count"`
}

type cachedFeed struct {
	Videos  []YTVideo
	Expires time.Time
}

type cachedPlaylists struct {
	Items   []YTPlaylist
	Expires time.Time
}

// YTChannel is a subscribed channel — for the sidebar «Каналы» list (id + title + avatar).
type YTChannel struct {
	ChannelID string `json:"channel_id"`
	Title     string `json:"title"`
	Thumbnail string `json:"thumbnail"`
}

type cachedChannels struct {
	Items   []YTChannel
	Expires time.Time
}

// APIClient wraps YouTube Data API v3 using per-user OAuth tokens.
type APIClient struct {
	store  *Store
	oauth  OAuthConfig
	client *http.Client

	subMu    sync.RWMutex
	subCache map[int64]*cachedFeed // tgID → subscriptions feed

	plMu    sync.RWMutex
	plCache map[int64]*cachedPlaylists

	chMu    sync.RWMutex
	chCache map[int64]*cachedChannels // tgID → subscribed-channels list
}

// NewAPIClient creates a new YouTube API client.
func NewAPIClient(store *Store, oauth OAuthConfig) *APIClient {
	return &APIClient{
		store:    store,
		oauth:    oauth,
		client:   &http.Client{Timeout: 15 * time.Second},
		subCache: make(map[int64]*cachedFeed),
		plCache:  make(map[int64]*cachedPlaylists),
		chCache:  make(map[int64]*cachedChannels),
	}
}

// GetSubscriptions returns the user's subscribed channels (id + title + avatar) for the sidebar.
// Cached 10 min per user — the channel list changes far less often than its videos.
func (a *APIClient) GetSubscriptions(ctx context.Context, tgID int64) ([]YTChannel, error) {
	a.chMu.RLock()
	cached, ok := a.chCache[tgID]
	a.chMu.RUnlock()
	if ok && time.Now().Before(cached.Expires) {
		return cached.Items, nil
	}

	token, err := a.ensureValidToken(tgID)
	if err != nil {
		return nil, err
	}
	url := fmt.Sprintf("%s/subscriptions?part=snippet&mine=true&maxResults=%d&order=alphabetical", ytAPIBase, maxSubResult)
	body, err := a.apiGet(ctx, token, url)
	if err != nil {
		return nil, fmt.Errorf("subscriptions list: %w", err)
	}

	var resp struct {
		Items []struct {
			Snippet struct {
				Title      string `json:"title"`
				ResourceID struct {
					ChannelID string `json:"channelId"`
				} `json:"resourceId"`
				Thumbnails map[string]struct {
					URL string `json:"url"`
				} `json:"thumbnails"`
			} `json:"snippet"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}

	out := make([]YTChannel, 0, len(resp.Items))
	for _, it := range resp.Items {
		cid := it.Snippet.ResourceID.ChannelID
		if cid == "" {
			continue
		}
		thumb := ""
		for _, key := range []string{"medium", "default", "high"} {
			if t, ok := it.Snippet.Thumbnails[key]; ok && t.URL != "" {
				thumb = t.URL
				break
			}
		}
		out = append(out, YTChannel{ChannelID: cid, Title: it.Snippet.Title, Thumbnail: thumb})
	}

	a.chMu.Lock()
	a.chCache[tgID] = &cachedChannels{Items: out, Expires: time.Now().Add(10 * time.Minute)}
	a.chMu.Unlock()
	return out, nil
}

// IsLinked returns true if user has a YouTube token.
func (a *APIClient) IsLinked(tgID int64) bool {
	return a.store.Get(tgID) != nil
}

// ensureValidToken refreshes the access token if needed and returns it.
func (a *APIClient) ensureValidToken(tgID int64) (string, error) {
	ut := a.store.Get(tgID)
	if ut == nil {
		return "", fmt.Errorf("youtube not linked for tg:%d", tgID)
	}

	// If token expires within 60 seconds, refresh.
	if time.Now().Add(60 * time.Second).After(ut.ExpiresAt) {
		tr, err := RefreshAccessToken(a.oauth, ut.RefreshToken)
		if err != nil {
			// Revoked on Google's side (or aged out): erase the credential
			// instead of keeping a token that can never work again.
			if errors.Is(err, ErrRefreshRevoked) {
				_ = a.store.Delete(tgID)
			}
			return "", fmt.Errorf("refresh: %w", err)
		}
		ut.AccessToken = tr.AccessToken
		ut.RefreshToken = tr.RefreshToken
		ut.ExpiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
		_ = a.store.Put(*ut)
	}

	return ut.AccessToken, nil
}

// apiGet performs an authenticated GET request.
func (a *APIClient) apiGet(ctx context.Context, accessToken, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("youtube API %d: %s", resp.StatusCode, truncate(body, 200))
	}
	return body, nil
}

// GetChannelTitle fetches the authenticated user's channel title.
func (a *APIClient) GetChannelTitle(ctx context.Context, tgID int64) (string, error) {
	token, err := a.ensureValidToken(tgID)
	if err != nil {
		return "", err
	}
	body, err := a.apiGet(ctx, token, ytAPIBase+"/channels?part=snippet&mine=true")
	if err != nil {
		return "", err
	}

	var resp struct {
		Items []struct {
			Snippet struct {
				Title string `json:"title"`
			} `json:"snippet"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", err
	}
	if len(resp.Items) == 0 {
		return "", nil
	}
	return resp.Items[0].Snippet.Title, nil
}

// GetSubscriptionsFeed returns recent videos from the user's subscriptions.
func (a *APIClient) GetSubscriptionsFeed(ctx context.Context, tgID int64) ([]YTVideo, error) {
	// Check cache.
	a.subMu.RLock()
	cached, ok := a.subCache[tgID]
	a.subMu.RUnlock()
	if ok && time.Now().Before(cached.Expires) {
		return cached.Videos, nil
	}

	token, err := a.ensureValidToken(tgID)
	if err != nil {
		return nil, err
	}

	// Step 1: Get user's subscriptions.
	subsURL := fmt.Sprintf("%s/subscriptions?part=snippet&mine=true&maxResults=%d&order=relevance", ytAPIBase, maxSubResult)
	body, err := a.apiGet(ctx, token, subsURL)
	if err != nil {
		return nil, fmt.Errorf("subscriptions list: %w", err)
	}

	var subsResp struct {
		Items []struct {
			Snippet struct {
				ResourceID struct {
					ChannelID string `json:"channelId"`
				} `json:"resourceId"`
				Title string `json:"title"`
			} `json:"snippet"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &subsResp); err != nil {
		return nil, err
	}

	if len(subsResp.Items) == 0 {
		return nil, nil
	}

	// Step 2: For each channel, get uploads playlist and latest video.
	// Batch channel IDs to get their uploads playlists.
	channelIDs := make([]string, 0, len(subsResp.Items))
	channelTitles := make(map[string]string) // channelID → title
	for _, item := range subsResp.Items {
		cid := item.Snippet.ResourceID.ChannelID
		channelIDs = append(channelIDs, cid)
		channelTitles[cid] = item.Snippet.Title
	}

	// Fetch channels in batches of 50.
	uploadsPlaylists := make(map[string]string) // channelID → uploadsPlaylistID
	for i := 0; i < len(channelIDs); i += 50 {
		end := min(i+50, len(channelIDs))
		batch := channelIDs[i:end]
		var ids strings.Builder
		for j, id := range batch {
			if j > 0 {
				ids.WriteString(",")
			}
			ids.WriteString(id)
		}
		chURL := fmt.Sprintf("%s/channels?part=contentDetails&id=%s", ytAPIBase, ids.String())
		chBody, err := a.apiGet(ctx, token, chURL)
		if err != nil {
			continue
		}
		var chResp struct {
			Items []struct {
				ID             string `json:"id"`
				ContentDetails struct {
					RelatedPlaylists struct {
						Uploads string `json:"uploads"`
					} `json:"relatedPlaylists"`
				} `json:"contentDetails"`
			} `json:"items"`
		}
		if json.Unmarshal(chBody, &chResp) == nil {
			for _, ch := range chResp.Items {
				uploadsPlaylists[ch.ID] = ch.ContentDetails.RelatedPlaylists.Uploads
			}
		}
	}

	// Step 3: Fetch latest 2 videos from each channel's uploads playlist (concurrently).
	type vidResult struct {
		Channel string
		Videos  []YTVideo
	}
	vch := make(chan vidResult, len(uploadsPlaylists))
	sem := make(chan struct{}, 5) // limit concurrency

	for cid, plID := range uploadsPlaylists {
		go func(channelID, playlistID string) {
			sem <- struct{}{}
			defer func() { <-sem }()

			plURL := fmt.Sprintf("%s/playlistItems?part=snippet&playlistId=%s&maxResults=5", ytAPIBase, playlistID)
			plBody, err := a.apiGet(ctx, token, plURL)
			if err != nil {
				vch <- vidResult{Channel: channelTitles[channelID]}
				return
			}
			var plResp struct {
				Items []struct {
					Snippet struct {
						Title      string `json:"title"`
						ResourceID struct {
							VideoID string `json:"videoId"`
						} `json:"resourceId"`
						Thumbnails struct {
							Medium struct {
								URL string `json:"url"`
							} `json:"medium"`
						} `json:"thumbnails"`
						PublishedAt string `json:"publishedAt"`
					} `json:"snippet"`
				} `json:"items"`
			}
			if json.Unmarshal(plBody, &plResp) != nil {
				vch <- vidResult{Channel: channelTitles[channelID]}
				return
			}
			vids := make([]YTVideo, 0, len(plResp.Items))
			for _, item := range plResp.Items {
				vids = append(vids, YTVideo{
					VideoID:     item.Snippet.ResourceID.VideoID,
					Title:       item.Snippet.Title,
					Channel:     channelTitles[channelID],
					ChannelID:   channelID,
					Thumbnail:   item.Snippet.Thumbnails.Medium.URL,
					PublishedAt: item.Snippet.PublishedAt,
				})
			}
			vch <- vidResult{Channel: channelTitles[channelID], Videos: vids}
		}(cid, plID)
	}

	// Collect results.
	allVideos := make([]YTVideo, 0)
	for range uploadsPlaylists {
		result := <-vch
		allVideos = append(allVideos, result.Videos...)
	}

	// Sort by published date (newest first).
	sortVideosByDate(allVideos)

	// Limit to 100 videos.
	if len(allVideos) > 100 {
		allVideos = allVideos[:100]
	}

	// Cache.
	a.subMu.Lock()
	a.subCache[tgID] = &cachedFeed{Videos: allVideos, Expires: time.Now().Add(ytFeedTTL)}
	a.subMu.Unlock()

	return allVideos, nil
}

// GetPlaylists returns the user's playlists.
func (a *APIClient) GetPlaylists(ctx context.Context, tgID int64) ([]YTPlaylist, error) {
	// Check cache.
	a.plMu.RLock()
	cached, ok := a.plCache[tgID]
	a.plMu.RUnlock()
	if ok && time.Now().Before(cached.Expires) {
		return cached.Items, nil
	}

	token, err := a.ensureValidToken(tgID)
	if err != nil {
		return nil, err
	}

	body, err := a.apiGet(ctx, token, ytAPIBase+"/playlists?part=snippet,contentDetails&mine=true&maxResults=50")
	if err != nil {
		return nil, err
	}

	var resp struct {
		Items []struct {
			ID      string `json:"id"`
			Snippet struct {
				Title      string `json:"title"`
				Thumbnails struct {
					Medium struct {
						URL string `json:"url"`
					} `json:"medium"`
				} `json:"thumbnails"`
			} `json:"snippet"`
			ContentDetails struct {
				ItemCount int `json:"itemCount"`
			} `json:"contentDetails"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}

	playlists := make([]YTPlaylist, 0, len(resp.Items))
	for _, item := range resp.Items {
		playlists = append(playlists, YTPlaylist{
			PlaylistID: item.ID,
			Title:      item.Snippet.Title,
			Thumbnail:  item.Snippet.Thumbnails.Medium.URL,
			ItemCount:  item.ContentDetails.ItemCount,
		})
	}

	// Cache.
	a.plMu.Lock()
	a.plCache[tgID] = &cachedPlaylists{Items: playlists, Expires: time.Now().Add(ytFeedTTL)}
	a.plMu.Unlock()

	return playlists, nil
}

// GetPlaylistItems returns videos from a specific playlist.
func (a *APIClient) GetPlaylistItems(ctx context.Context, tgID int64, playlistID string) ([]YTVideo, error) {
	token, err := a.ensureValidToken(tgID)
	if err != nil {
		return nil, err
	}

	body, err := a.apiGet(ctx, token, fmt.Sprintf("%s/playlistItems?part=snippet&playlistId=%s&maxResults=50", ytAPIBase, playlistID))
	if err != nil {
		return nil, err
	}

	var resp struct {
		Items []struct {
			Snippet struct {
				Title      string `json:"title"`
				ResourceID struct {
					VideoID string `json:"videoId"`
				} `json:"resourceId"`
				ChannelTitle string `json:"channelTitle"`
				// channelTitle above is the playlist OWNER; the video's actual channel
				// lives in videoOwnerChannel* (absent for private/deleted items).
				VideoOwnerChannelTitle string `json:"videoOwnerChannelTitle"`
				VideoOwnerChannelID    string `json:"videoOwnerChannelId"`
				Thumbnails             struct {
					Medium struct {
						URL string `json:"url"`
					} `json:"medium"`
				} `json:"thumbnails"`
				PublishedAt string `json:"publishedAt"`
			} `json:"snippet"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}

	videos := make([]YTVideo, 0, len(resp.Items))
	for _, item := range resp.Items {
		if item.Snippet.ResourceID.VideoID == "" {
			continue
		}
		channel := item.Snippet.VideoOwnerChannelTitle
		if channel == "" {
			channel = item.Snippet.ChannelTitle
		}
		videos = append(videos, YTVideo{
			VideoID:     item.Snippet.ResourceID.VideoID,
			Title:       item.Snippet.Title,
			Channel:     channel,
			ChannelID:   item.Snippet.VideoOwnerChannelID,
			Thumbnail:   item.Snippet.Thumbnails.Medium.URL,
			PublishedAt: item.Snippet.PublishedAt,
		})
	}

	return videos, nil
}

// sortVideosByDate sorts videos by published date descending.
func sortVideosByDate(videos []YTVideo) {
	for i := 1; i < len(videos); i++ {
		for j := i; j > 0 && videos[j].PublishedAt > videos[j-1].PublishedAt; j-- {
			videos[j], videos[j-1] = videos[j-1], videos[j]
		}
	}
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
