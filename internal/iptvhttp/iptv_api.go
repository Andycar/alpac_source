package iptvhttp

import (
	"crypto/md5"
	"encoding/binary"
	"io"
	"net/http"
	"strconv"
	"strings"

	"lampac-go/internal/auth"
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

// defaultCatchupDays — глубина архива, когда плейлист её не объявил.
const defaultCatchupDays = 7

type iptvPublicCatchup struct {
	Type string `json:"type,omitempty"`
	Days int    `json:"days,omitempty"`
	// Source is intentionally omitted — it's a URL template that reveals the host.
}

type iptvPublicChannel struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	CleanName   string             `json:"clean_name"`
	Logo        string             `json:"logo,omitempty"`
	Group       string             `json:"group,omitempty"` // legacy: страна, иначе жанр
	Country     string             `json:"country,omitempty"`
	CountryCode string             `json:"country_code,omitempty"`
	Genre       string             `json:"genre,omitempty"`
	TvgID       string             `json:"tvg_id,omitempty"`
	TvgName     string             `json:"tvg_name,omitempty"`
	Number      int                `json:"number,omitempty"`
	Quality     string             `json:"quality,omitempty"`
	Catchup     *iptvPublicCatchup `json:"catchup,omitempty"`
	// StreamStatus — что показала проба живости: "ok" | "dead" | "frozen". Пусто = не проверяли.
	StreamStatus string `json:"stream_status,omitempty"`
	PlaylistID  string             `json:"playlist_id"`
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
		Group: c.Group, Country: c.Country, CountryCode: c.CountryCode, Genre: c.Genre,
		TvgID: c.TvgID, TvgName: c.TvgName, Number: c.Number,
		Quality: c.Quality, PlaylistID: c.PlaylistID,
	}
	if c.Catchup != nil {
		// Глубину знают не все плейлисты: у 3583 каналов базы тип архива указан, а `catchup-days`
		// нет. Клиент по нулю считал, что архива нет вовсе, и прятал запись — хотя она работает.
		// Отдаём осторожный дефолт: неделя (типичная глубина flussonic-панелей).
		days := c.Catchup.Days
		if days <= 0 {
			days = defaultCatchupDays
		}
		pc.Catchup = &iptvPublicCatchup{Type: c.Catchup.Type, Days: days}
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
	// Fallback: стабильный положительный int64 из самого токена. Источник
	// токена здесь ВАЖЕН: считать хеш от одной куки нельзя — уцелей на
	// перезапуске другая, и у человека молча появился бы второй набор
	// плейлистов. Берём общий разбор, он отдаёт одну и ту же строку.
	if tok := auth.ExtractToken(r); tok != "" {
		h := md5.Sum([]byte("iptv:" + tok))
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

func iptvPlaylistsHandler(iptvStore *iptv.Store, whoami tgResolver, prefs *iptvPrefStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID := whoami(r)
		playlists := iptvStore.ListPlaylists(tgID)
		// «Игнорировать встроенные плейлисты»: человеку со своим списком наши доноры
		// и реестр только мешают — он их не выбирал. Прячем ТОЛЬКО когда у него
		// действительно есть свой список: иначе настройка оставила бы пустой экран,
		// и выглядело бы это как поломка, а не как выбор.
		if prefs.get(tgID).HideBuiltin {
			own := 0
			for _, p := range playlists {
				if !p.IsGlobal {
					own++
				}
			}
			if own > 0 {
				kept := playlists[:0]
				for _, p := range playlists {
					if !p.IsGlobal {
						kept = append(kept, p)
					}
				}
				playlists = kept
			}
		}
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
			logIPTVDeny(r, "не опознали пользователя")
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
			logIPTVDeny(r, "не опознали пользователя")
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
			logIPTVDeny(r, "не опознали пользователя")
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

		// Список НАШЕГО реестра (тем более с ?play=1 — готовыми /proxy-ссылками)
		// анониму не отдаём: это и был путь массовой кражи каталога.
		if playlistID == iptv.RegistryPlaylistID && !requireRegistryAuth(r, tgStore) {
			logIPTVDeny(r, "канал реестра, а человек не авторизован")
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
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
		// ОДНА сессия на весь список: зритель один, поток один. Сессия на
		// канал дала бы две тысячи штук за одно открытие списка — и заодно
		// сломала бы саму идею сверки, ведь делят именно список целиком.
		listSID := ""
		if withPlay {
			listSID, _ = proxyLinks.OpenSession(reqIP, tgID, deviceKey(r), deviceLimit(r))
		}
		pub := make([]iptvPublicChannel, 0, len(channels))
		for i := range channels {
			out := toPublicChannel(channels[i])
			// Статус живости — из health-check'а сервера: он и так ходит по адресам, а зритель
			// иначе узнаёт о сломанном канале только чёрным экраном после OK.
			out.StreamStatus = iptvStore.StreamHealth(channels[i].URL)
			if withPlay {
				if u, errSlug := iptvResolveStreamURL(channels[i].URL, &channels[i], pl, proxyLinks, reqIP, host, direct, listSID); errSlug == "" {
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

		// Группы реестра — под тем же гейтом, что и его каналы.
		if playlistID == iptv.RegistryPlaylistID && !requireRegistryAuth(r, tgStore) {
			logIPTVDeny(r, "канал реестра, а человек не авторизован")
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}

		// ?by=country|genre — одна ось таксономии. Без параметра поведение
		// прежнее (единый смешанный список), чтобы не сломать текущих клиентов.
		by := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("by")))
		switch by {
		case iptv.GroupByCountry, iptv.GroupByGenre, iptv.GroupByLegacy:
		default:
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "by must be country or genre",
			})
			return
		}

		groups := iptvStore.GetGroupsBy(tgID, playlistID, by)
		if groups == nil {
			groups = []iptv.GroupInfo{}
		}

		resp := map[string]any{"groups": groups}
		if by != iptv.GroupByLegacy {
			resp["by"] = by
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// ---------------------------------------------------------------------------
//  PATCH /api/iptv/playlists/{id} — настройки уже добавленного плейлиста
// ---------------------------------------------------------------------------

// iptvPatchPlaylistHandler меняет режим прокси личного плейлиста.
//
// Зачем отдельная ручка: режим задавался ТОЛЬКО при добавлении, а клиенты его
// не передавали вовсе — поле оставалось пустым, и сервер подставлял свой
// default_proxy ("all"). Личный плейлист пользователя всегда шёл через нас, и
// выключить это было нечем.
//
// Оговорка, которую надо понимать: "none" отдаёт прямой адрес только тем
// клиентам, что играют нативным плеером и просят ?direct=1 (Android, tvOS).
// Браузеру прямой адрес не поможет — IPTV-CDN не отдают CORS-заголовки, и
// манифест не загрузится вовсе; там прокси остаётся всегда. То же для каналов,
// которым нужен свой User-Agent или Referer: без нашего прокси их некому
// проставить.
func iptvPatchPlaylistHandler(iptvStore *iptv.Store, whoami tgResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID := whoami(r)
		if tgID == 0 {
			logIPTVDeny(r, "не опознали пользователя")
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		id := chi.URLParam(r, "id")
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<10))
		var req struct {
			ProxyMode string `json:"proxy_mode"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad json"})
			return
		}
		if err := iptvStore.SetPlaylistProxyMode(tgID, id, req.ProxyMode); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "proxy_mode": strings.ToLower(strings.TrimSpace(req.ProxyMode))})
	}
}
