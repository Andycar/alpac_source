package iptvhttp

import (
	"crypto/md5"
	"encoding/binary"
	"io"
	"net/http"
	"strconv"
	"strings"

	"lampac-go/internal/iptv"
	"lampac-go/internal/proxylink"
	"lampac-go/internal/tgauth"

	"github.com/go-chi/chi/v5"
)

// ---------------------------------------------------------------------------
//  Helper: resolve TG ID for IPTV endpoints.
//  Falls back to a stable pseudo-ID from the lampac_token cookie
//  so web users without TG auth can still manage playlists.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
//  Public DTOs — what the client is ALLOWED to see.
//
//  The internal iptv.Playlist / iptv.Channel structs carry the raw upstream
//  source (playlist .m3u8 URL, x-tvg-url EPG URLs, per-channel CDN stream URL,
//  plus User-Agent / Referer credentials). Those are SECRETS — serialising the
//  internal structs leaked the paid playlist straight into the browser's
//  Network tab (`pl.vivamax.pro/...m3u8`, `epg.vivamax.pro/...`). We can't tag
//  the fields `json:"-"` because the SAME structs are persisted to the on-disk
//  cache, so the API maps to these slim DTOs instead. Playback never needs the
//  raw URL — the client plays via /api/iptv/play?channel_id=… (server-resolved).
// ---------------------------------------------------------------------------

type iptvPublicPlaylist struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ChannelCount int    `json:"channel_count"`
	IsGlobal     bool   `json:"is_global"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
}

type iptvPublicCatchup struct {
	Type string `json:"type,omitempty"`
	Days int    `json:"days,omitempty"`
	// Source is intentionally omitted — it's a URL template that reveals the host.
}

type iptvPublicChannel struct {
	ID         string             `json:"id"`
	Name       string             `json:"name"`
	CleanName  string             `json:"clean_name"`
	Logo       string             `json:"logo,omitempty"`
	Group      string             `json:"group,omitempty"`
	TvgID      string             `json:"tvg_id,omitempty"`
	TvgName    string             `json:"tvg_name,omitempty"`
	Number     int                `json:"number,omitempty"`
	Quality    string             `json:"quality,omitempty"`
	Catchup    *iptvPublicCatchup `json:"catchup,omitempty"`
	PlaylistID string             `json:"playlist_id"`
	// PlayURL (only with ?play=1) is the CLIENT-playable live URL — either the
	// raw https CDN URL (when direct play is safe) or a /proxy/{hash} wrapper.
	// Having it in the listing lets the player build a real channel-zapping
	// playlist without one /api/iptv/play roundtrip per switch.
	PlayURL string `json:"play_url,omitempty"`
	// url / user_agent / referer deliberately absent — never leave the server.
}

func toPublicPlaylist(p iptv.Playlist) iptvPublicPlaylist {
	return iptvPublicPlaylist{
		ID: p.ID, Name: p.Name, ChannelCount: p.ChannelCount,
		IsGlobal: p.IsGlobal, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

func toPublicChannel(c iptv.Channel) iptvPublicChannel {
	pc := iptvPublicChannel{
		ID: c.ID, Name: c.Name, CleanName: c.CleanName, Logo: c.Logo,
		Group: c.Group, TvgID: c.TvgID, TvgName: c.TvgName, Number: c.Number,
		Quality: c.Quality, PlaylistID: c.PlaylistID,
	}
	if c.Catchup != nil {
		pc.Catchup = &iptvPublicCatchup{Type: c.Catchup.Type, Days: c.Catchup.Days}
	}
	return pc
}

// tgResolver отвечает на «кто это». Вынесен параметром, потому что у плейлистов
// теперь ДВА входа с разной авторизацией: web/клиенты приходят по cookie
// lampac_token, а мини-апп бота — по подписанному initData Telegram. Логика
// работы с плейлистами при этом одна, дублировать её было бы нечем оправдать.
type tgResolver func(*http.Request) int64

// kitResolver разбирает авторизацию мини-аппа. Устанавливается хостом (только он
// умеет проверять initData); пока не установлен — возвращает 0, и kit-роуты
// честно отвечают 401 вместо того, чтобы пускать кого угодно.
var kitResolver tgResolver

// SetKitResolver подключает авторизацию мини-аппа бота.
func SetKitResolver(fn func(*http.Request) int64) { kitResolver = fn }

func resolveKit(r *http.Request) int64 {
	if kitResolver == nil {
		return 0
	}
	return kitResolver(r)
}

func iptvTgID(r *http.Request, store *tgauth.Store) int64 {
	if id := calendarTgID(r, store); id != 0 {
		return id
	}
	// Fallback: derive a stable positive int64 from the lampac_token cookie.
	if c, err := r.Cookie("lampac_token"); err == nil && c.Value != "" {
		h := md5.Sum([]byte("iptv:" + c.Value))
		v := int64(binary.BigEndian.Uint64(h[:8]))
		if v < 0 {
			v = -v
		}
		if v == 0 {
			v = 1
		}
		return v
	}
	return 0
}

// ---------------------------------------------------------------------------
//  GET /api/iptv/playlists — list user's playlists (personal + global)
// ---------------------------------------------------------------------------

func iptvPlaylistsHandler(iptvStore *iptv.Store, whoami tgResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID := whoami(r)
		playlists := iptvStore.ListPlaylists(tgID)
		pub := make([]iptvPublicPlaylist, 0, len(playlists))
		for _, p := range playlists {
			pub = append(pub, toPublicPlaylist(p))
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"playlists": pub,
		})
	}
}

// ---------------------------------------------------------------------------
//  POST /api/iptv/playlists — add a new playlist
// ---------------------------------------------------------------------------

func iptvAddPlaylistHandler(iptvStore *iptv.Store, whoami tgResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID := whoami(r)
		if tgID == 0 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, 8192))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
			return
		}

		var req struct {
			Name      string `json:"name"`
			URL       string `json:"url"`
			ProxyMode string `json:"proxy_mode"`
			UserAgent string `json:"user_agent"` // кастомный UA (панели фильтруют по UA)
			EPGURL    string `json:"epg_url"`    // ручной адрес программы (через запятую)
		}
		if err := json.Unmarshal(body, &req); err != nil || strings.TrimSpace(req.URL) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "url is required"})
			return
		}

		pl := iptv.Playlist{
			Name:      strings.TrimSpace(req.Name),
			URL:       strings.TrimSpace(req.URL),
			ProxyMode: req.ProxyMode,
			UserAgent: strings.TrimSpace(req.UserAgent),
		}
		for _, u := range strings.Split(req.EPGURL, ",") {
			if t := strings.TrimSpace(u); t != "" {
				pl.EPGManual = append(pl.EPGManual, t)
			}
		}
		if pl.Name == "" {
			pl.Name = "Playlist"
		}

		if err := iptvStore.AddPlaylist(tgID, pl); err != nil {
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// ---------------------------------------------------------------------------
//  DELETE /api/iptv/playlists/{id} — remove a playlist
// ---------------------------------------------------------------------------

func iptvDeletePlaylistHandler(iptvStore *iptv.Store, whoami tgResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID := whoami(r)
		if tgID == 0 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}

		playlistID := chi.URLParam(r, "id")
		if playlistID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "playlist id required"})
			return
		}

		if err := iptvStore.RemovePlaylist(tgID, playlistID); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// ---------------------------------------------------------------------------
//  POST /api/iptv/playlists/{id}/refresh — re-download and parse
// ---------------------------------------------------------------------------

func iptvRefreshPlaylistHandler(iptvStore *iptv.Store, whoami tgResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID := whoami(r)
		if tgID == 0 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}

		playlistID := chi.URLParam(r, "id")
		if playlistID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "playlist id required"})
			return
		}

		if err := iptvStore.RefreshPlaylist(tgID, playlistID); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "refreshing"})
	}
}

// ---------------------------------------------------------------------------
//  GET /api/iptv/channels — channels with filtering and pagination
// ---------------------------------------------------------------------------

func iptvChannelsHandler(iptvStore *iptv.Store, tgStore *tgauth.Store, epg *iptv.EPGEngine, proxyLinks *proxylink.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID := iptvTgID(r, tgStore)
		q := r.URL.Query()

		playlistID := q.Get("playlist_id")
		group := q.Get("group")
		search := q.Get("search")
		page, _ := strconv.Atoi(q.Get("page"))
		limit, _ := strconv.Atoi(q.Get("limit"))

		if playlistID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "playlist_id required"})
			return
		}

		channels, total := iptvStore.GetChannels(tgID, playlistID, group, search, page, limit)

		// Enrich logos from EPG channel data when M3U didn't provide tvg-logo.
		if epg != nil {
			for i := range channels {
				if channels[i].Logo != "" {
					continue
				}
				// Try by XMLTV ID first.
				if channels[i].TvgID != "" {
					if ec, ok := epg.GetChannel(channels[i].TvgID); ok && ec.Icon != "" {
						channels[i].Logo = ec.Icon
						continue
					}
				}
				// Fallback: match by channel name.
				if icon := epg.GetIconByName(channels[i].Name); icon != "" {
					channels[i].Logo = icon
				}
			}
		}

		// Map to the public DTO — strips raw stream URL + UA/Referer credentials so
		// the paid source never reaches the client (playback goes via /api/iptv/play).
		// With ?play=1 each channel additionally carries its resolved live play_url
		// (proxy-wrapped where required) so the player can zap without extra roundtrips.
		withPlay := q.Get("play") == "1"
		var pl *iptv.Playlist
		if withPlay {
			for _, p := range iptvStore.ListPlaylists(tgID) {
				if p.ID == playlistID {
					cp := p
					pl = &cp
					break
				}
			}
		}
		reqIP := clientIP(r)
		host := hostFromRequest(r)
		// direct=1 — клиент-натив с usesCleartextTraffic: plain http:// играет с устройства
		// (гео проходит на IP зрителя), прокси остаётся для заголовков и proxy=all.
		direct := q.Get("direct") == "1"
		pub := make([]iptvPublicChannel, 0, len(channels))
		for i := range channels {
			out := toPublicChannel(channels[i])
			if withPlay {
				if u, errSlug := iptvResolveStreamURL(channels[i].URL, &channels[i], pl, proxyLinks, reqIP, host, direct); errSlug == "" {
					out.PlayURL = u
				}
			}
			pub = append(pub, out)
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"channels": pub,
			"total":    total,
			"page":     page,
			"limit":    limit,
		})
	}
}

// ---------------------------------------------------------------------------
//  GET /api/iptv/groups — group list for a playlist
// ---------------------------------------------------------------------------

func iptvGroupsHandler(iptvStore *iptv.Store, tgStore *tgauth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID := iptvTgID(r, tgStore)
		playlistID := r.URL.Query().Get("playlist_id")
		if playlistID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "playlist_id required"})
			return
		}

		groups := iptvStore.GetGroups(tgID, playlistID)
		if groups == nil {
			groups = []iptv.GroupInfo{}
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"groups": groups,
		})
	}
}
