package iptvhttp

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/iptv"
	"lampac-go/internal/proxylink"
	"lampac-go/internal/tgauth"
	"lampac-go/internal/transcodesvc"
)

// Через прокси-балансер "iptv" — как и остальные исходящие запросы модуля:
// хосты логотипов бывают закрыты для дата-центров наравне с потоками.
// Логотип — маленькая картинка, и ждать её дольше пары секунд бессмысленно: экран каналов
// уже нарисован, а зритель смотрит на пустой квадрат. 12 секунд давали ответы по 20+ секунд
// (с учётом DNS и подключения) и держали соединение всё это время — отсюда основная масса 502
// на проде: 216–517 в час в вечерний пик (замер 2026-09-08).
var iptvLogoClient = httpclient.NewForBalancerDynamic("iptv", 4*time.Second)

// logoFail — отрицательный кэш: адрес логотипа, который только что не отдался.
//
// Без него каждый следующий зритель шёл к тому же мёртвому хосту заново и снова ждал таймаут.
// Дисковый кэш спасал только каналы РЕЕСТРА (own_*), а 502 давали донорские каналы из
// плейлистов — их тысячи, и на диск они намеренно не кладутся.
const (
	logoFailFor = 15 * time.Minute
	logoFailMax = 5000
)

var (
	logoFailMu sync.Mutex
	logoFail   = map[string]time.Time{}
)

func logoFailed(url string) bool {
	logoFailMu.Lock()
	defer logoFailMu.Unlock()
	t, ok := logoFail[url]
	return ok && time.Since(t) < logoFailFor
}

func noteLogoFail(url string) {
	now := time.Now()
	logoFailMu.Lock()
	defer logoFailMu.Unlock()
	if len(logoFail) >= logoFailMax {
		for k, v := range logoFail {
			if now.Sub(v) > logoFailFor {
				delete(logoFail, k)
			}
		}
		if len(logoFail) >= logoFailMax {
			for k := range logoFail { // всё ещё полно — освобождаем произвольную
				delete(logoFail, k)
				break
			}
		}
	}
	logoFail[url] = now
}

// ---------------------------------------------------------------------------
//  GET /api/iptv/logo?channel_id=... — server-side fetch of a channel logo.
//  Browsers block a playlist's http:// logos on an https page (mixed content)
//  and some hosts hotlink-protect; fetching server-side and re-serving over the
//  app origin sidesteps both. The URL is the channel's own logo (no SSRF surface).
//  Для каналов реестра (own_*) логотип оседает в дисковом кэше: свежий кэш
//  отдаётся без похода на upstream, а при отказе upstream'а — последняя копия.
// ---------------------------------------------------------------------------

// channelLogoURL подбирает адрес логотипа канала: сначала собственное поле, затем иконка из EPG
// по tvg-id, затем по имени — ровно та же лестница, что у списка каналов (iptv_api.go).
//
// ★Раньше подбор жил ТОЛЬКО в списке, и получалась вилка: список отдавал клиенту логотип, а
// /api/iptv/logo смотрел лишь в сохранённое поле и отвечал 404. Поле пустое у 81% каналов реестра
// (4047 из 4950), тогда как иконку несёт КАЖДЫЙ канал наших XMLTV-источников — отсюда 46 тысяч
// 404 в сутки против 3.4 тысяч удач.
func channelLogoURL(ch *iptv.Channel, epg *iptv.EPGEngine) string {
	if ch == nil {
		return ""
	}
	if ch.Logo != "" {
		return ch.Logo
	}
	if epg == nil {
		return ""
	}
	if ch.TvgID != "" {
		if ec, ok := epg.GetChannel(ch.TvgID); ok && ec.Icon != "" {
			return ec.Icon
		}
	}
	return epg.GetIconByName(ch.Name)
}

func iptvLogoHandler(iptvStore *iptv.Store, tgStore *tgauth.Store, epg *iptv.EPGEngine, logos *logoCache) http.HandlerFunc {
	// noLogo — единый ответ «картинки нет»: 404 с коротким сроком кэширования, чтобы клиент
	// не переспрашивал на каждом экране, но и не запомнил отказ надолго.
	noLogo := func(w http.ResponseWriter) {
		w.Header().Set("Cache-Control", "public, max-age=600")
		http.Error(w, "no logo", http.StatusNotFound)
	}
	serve := func(w http.ResponseWriter, ct string, body []byte) {
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		tgID := iptvTgID(r, tgStore)
		channelID := r.URL.Query().Get("channel_id")
		if channelID == "" {
			http.Error(w, "channel_id required", http.StatusBadRequest)
			return
		}
		ch, _ := iptvStore.GetChannel(tgID, channelID)
		// ★Тот же подбор логотипа из EPG, что делает список каналов.
		//
		// Раньше его здесь НЕ было, и получалась вилка: список говорил клиенту, что логотип у
		// канала есть (там обогащение по tvg-id/имени работает), а этот эндпоинт смотрел только
		// в сохранённое поле и отвечал 404. У 81% каналов реестра поле пустое, зато иконку несёт
		// КАЖДЫЙ канал наших XMLTV-источников — отсюда 46 тысяч 404 в сутки при 3.4 тысячи удач.
		logoURL := channelLogoURL(ch, epg)
		if ch == nil || logoURL == "" {
			// Clients ask for a logo per channel without checking whether the
			// channel list carried one, and most channels carry none — 258 of
			// the 300 registry channels. Answering with a bare 404 made every
			// client re-ask on every screen: ~6700 requests/hour on production,
			// 95% of them this branch. The verdict is cacheable, so say so.
			//
			// Two different TTLs because the two cases age differently: a known
			// channel that simply has no logo stays that way until the playlist
			// is re-ingested, while an unknown id is usually a client holding a
			// list from before a refresh — that one must re-check soon or a
			// newly appearing channel would show no logo for a day.
			if ch != nil {
				w.Header().Set("Cache-Control", "public, max-age=86400")
			} else {
				w.Header().Set("Cache-Control", "public, max-age=300")
			}
			http.Error(w, "no logo", http.StatusNotFound)
			return
		}
		if !strings.HasPrefix(logoURL, "http://") && !strings.HasPrefix(logoURL, "https://") {
			http.Error(w, "bad url", http.StatusBadRequest)
			return
		}

		useCache := logos != nil && iptv.IsRegistryChannelID(channelID)
		if useCache {
			if body, ct, fresh := logos.get(logoURL); fresh {
				serve(w, ct, body)
				return
			}
		}

		// Хост логотипа только что не ответил — не идём к нему снова.
		if logoFailed(logoURL) {
			if useCache {
				if body, ct, _ := logos.get(logoURL); body != nil {
					serve(w, ct, body)
					return
				}
			}
			noLogo(w)
			return
		}

		body, ct, err := fetchLogoUpstream(r, ch, logoURL)
		if err != nil {
			noteLogoFail(logoURL)
			// Upstream умер — для канала реестра отдаём последнюю удачную копию.
			if useCache {
				if body, ct, _ := logos.get(logoURL); body != nil {
					serve(w, ct, body)
					return
				}
			}
			// Отвечаем «логотипа нет», а НЕ 502: для клиента это не авария шлюза, а
			// отсутствующая картинка, и такой ответ он кэширует вместо того, чтобы
			// спрашивать снова на каждом экране.
			noLogo(w)
			return
		}
		if useCache {
			logos.put(logoURL, body, ct)
		}
		serve(w, ct, body)
	}
}

// fetchLogoUpstream downloads the channel's logo with its stream headers
// (hotlink-protected hosts want the same UA/Referer). 4 MB cap.
//
// URL приходит ОТДЕЛЬНЫМ аргументом, а не берётся из ch.Logo: у большинства каналов реестра поле
// пустое, и адрес подобран из EPG вызывающим (см. iptvLogoHandler). Заголовки по-прежнему берём у
// канала — хотлинк-защита смотрит на UA/Referer.
func fetchLogoUpstream(r *http.Request, ch *iptv.Channel, url string) (body []byte, contentType string, err error) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
	if err != nil {
		return nil, "", fmt.Errorf("bad url")
	}
	if ch.UserAgent != "" {
		req.Header.Set("User-Agent", ch.UserAgent)
	}
	if ch.Referer != "" {
		req.Header.Set("Referer", ch.Referer)
	}

	resp, err := iptvLogoClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("fetch failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("upstream %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "image/") {
		ct = "image/png" // some hosts mislabel; treat as an image so the browser still renders it
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, logoMaxBytes))
	if err != nil && len(body) == 0 {
		return nil, "", fmt.Errorf("fetch failed")
	}
	return body, ct, nil
}

// iptvCatchupURL rewrites a channel's LIVE url into an ARCHIVE (catchup/timeshift) url for a program
// that started at `start` (unix seconds) and ran `duration` seconds, per the channel's catchup-type.
// Returns "" when catchup isn't applicable.
func iptvCatchupURL(raw string, c *iptv.Catchup, start, duration int64) string {
	if c == nil || start <= 0 {
		return ""
	}
	if duration <= 0 {
		duration = 3600
	}
	now := time.Now().Unix()
	switch strings.ToLower(strings.TrimSpace(c.Type)) {
	case "flussonic", "fs":
		// .../index.m3u8?token=x  →  .../index-{start}-{duration}.m3u8?token=x
		u, err := url.Parse(raw)
		if err != nil {
			return ""
		}
		dir, file := path.Split(u.Path)
		ext := path.Ext(file)
		name := strings.TrimSuffix(file, ext)
		if name == "" {
			name = "index"
		}
		if ext == "" {
			ext = ".m3u8"
		}
		u.Path = dir + fmt.Sprintf("%s-%d-%d%s", name, start, duration, ext)
		return u.String()
	case "append":
		// append catchup-source with placeholders substituted
		rep := strings.NewReplacer(
			"{utc}", strconv.FormatInt(start, 10),
			"${start}", strconv.FormatInt(start, 10),
			"{start}", strconv.FormatInt(start, 10),
			"{lutc}", strconv.FormatInt(now, 10),
			"${now}", strconv.FormatInt(now, 10),
			"{now}", strconv.FormatInt(now, 10),
			"{duration}", strconv.FormatInt(duration, 10),
			"{offset}", strconv.FormatInt(now-start, 10),
			"{end}", strconv.FormatInt(start+duration, 10),
		)
		return raw + rep.Replace(c.Source)
	default: // "default", "shift", unknown → append utc/lutc query params
		sep := "?"
		if strings.Contains(raw, "?") {
			sep = "&"
		}
		return raw + sep + "utc=" + strconv.FormatInt(start, 10) + "&lutc=" + strconv.FormatInt(now, 10)
	}
}

// iptvResolveStreamURL turns a channel's raw upstream URL into the URL the
// CLIENT is allowed to play. Returns (url, "") on success or ("", errSlug)
// when the stream must be proxied but proxying is unavailable/failed.
//
// Force-proxy when the raw URL can't be played directly by THIS client:
// cleartext http:// is blocked in the browser (mixed content) — but the NATIVE
// apps ship usesCleartextTraffic=true and ask with direct=1, so plain http://
// plays straight from the device. Это принципиально для гео: у провайдера с
// РФ-only плейлистом сервер (зарубежный IP) получал 403, хотя телевизор стоит
// в РФ — прямой стрим с устройства проходит гео сам.
// User-Agent/Referer-gated CDNs still proxy (the /proxy token injects the
// headers); mustProxy also holds for proxy=all playlists (hide the client IP
// from the upstream provider — playlist-sharing protection); in that case we
// must NEVER fall back to the raw URL (it would dump the channel's real CDN
// URL to the client). Fail closed instead.
// iptvPluginFor — имя плагина ссылки канала. Оно решает и маршрут /proxy, и
// то, попадает ли ссылка под TTL / привязку к сети / потоковую сессию, поэтому
// считается в одном месте: разъехавшиеся копии молча выключили бы защиту.
func iptvPluginFor(ch *iptv.Channel) string {
	if ch != nil && ch.NeedsRegionRoute {
		return "iptv-ru"
	}
	return "iptv"
}

func iptvResolveStreamURL(rawURL string, ch *iptv.Channel, pl *iptv.Playlist, proxyLinks *proxylink.Manager, reqIP, host string, direct bool, sid string) (string, string) {
	// own_* — канал НАШЕГО реестра: сырой апстрим не отдаём никогда и никому,
	// даже нативному клиенту с direct=1. Утечка одного raw-URL восстанавливает
	// украденный каталог в обход всей авторизации; личных плейлистов
	// пользователей (их собственные подписки) запрет не касается.
	if strings.HasPrefix(ch.ID, "own_") {
		direct = false
	}
	// UA канала главнее; UA плейлиста — фолбэк (панель, требующая UA на M3U, почти
	// всегда требует его и на стримах).
	ua := ch.UserAgent
	if ua == "" && pl != nil {
		ua = pl.UserAgent
	}
	needHeaders := ua != "" || ch.Referer != ""
	// Без direct=1 сырой URL отдавать нельзя ВООБЩЕ (не только для http://):
	// web-плеер тянет манифест чужого CDN через fetch — IPTV-CDN не отдают
	// Access-Control-Allow-Origin, Shaka падает на CORS ещё до видео, и канал
	// «не работает никогда» при живом потоке. direct=1 шлют только клиенты,
	// играющие нативным плеером с устройства (Android/ExoPlayer) — им CORS не
	// писан, им сырой URL и нужен (гео/нагрузка).
	forceProxy := needHeaders || !direct
	mustProxy := forceProxy || (pl != nil && pl.ProxyMode == "all")
	if !mustProxy {
		return rawURL, ""
	}
	if proxyLinks == nil {
		return "", "stream proxy unavailable"
	}
	headers := make(map[string]string)
	if ua != "" {
		headers["User-Agent"] = ua
	}
	if ch.Referer != "" {
		headers["Referer"] = ch.Referer
	}
	// Плагин ссылки решает, каким прокси /proxy пойдёт за апстримом. Канал с
	// динамическим источником помечаем ОТДЕЛЬНЫМ плагином: у таких вещателей
	// часть запросов (например, ключ AES-128) отдаётся только «своей» стране,
	// и им нужен свой маршрут — а гнать туда же остальные сотни каналов незачем.
	plugin := iptvPluginFor(ch)
	var proxyHash string
	if len(headers) > 0 {
		proxyHash = proxyLinks.EncryptURIWithHeaders(rawURL, reqIP, plugin, headers)
	} else {
		proxyHash = proxyLinks.EncryptURI(rawURL, reqIP, plugin, false, false, false)
	}
	if proxyHash == "" {
		return "", "stream proxy failed"
	}
	out := host + "/proxy/" + proxyHash
	// sid вешается ЗДЕСЬ, в единственной точке выдачи /proxy-ссылок канала:
	// ручек-источников три (/play, /channels?with_play, /stream/{id}), и
	// забытая копия отдала бы ссылку без sid — гейт fail-closed убил бы канал.
	// Саму сессию заводит вызывающий: одна на запрос, а не на канал.
	// Пустой sid = механизм выключен, ссылка остаётся как была.
	if sid != "" {
		sep := "?"
		if strings.Contains(out, "?") {
			sep = "&"
		}
		out += sep + "sid=" + url.QueryEscape(sid)
	}
	return out, ""
}

// ---------------------------------------------------------------------------
//  GET /api/iptv/play?channel_id=...[&start=<unix>&duration=<sec>]
//  Without start → live; with start → archive/catchup. Both proxy-wrapped.
// ---------------------------------------------------------------------------

func iptvPlayHandler(cfg config.Config, iptvStore *iptv.Store, tgStore *tgauth.Store, epg *iptv.EPGEngine, proxyLinks *proxylink.Manager, transSvc *transcodesvc.TranscodingService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID := iptvTgID(r, tgStore)
		channelID := r.URL.Query().Get("channel_id")
		if channelID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "channel_id required"})
			return
		}

		// Канал НАШЕГО реестра — только по-настоящему авторизованным (см.
		// requireRegistryAuth): до этого гейта /play отдавал /proxy-ссылки
		// реестра анониму, и никакая защита export/stream не имела смысла.
		if iptv.IsRegistryChannelID(channelID) && !requireRegistryAuth(r, tgStore) {
			logIPTVDeny(r, "канал реестра, а человек не авторизован")
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}

		ch, pl := iptvStore.GetChannel(tgID, channelID)
		if ch == nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "channel not found"})
			return
		}

		// Archive/catchup: ?start=<unix>&duration=<sec> rewrites the live URL to the archive URL.
		rawURL := ch.URL
		live := true
		if s := r.URL.Query().Get("start"); s != "" {
			start, _ := strconv.ParseInt(s, 10, 64)
			dur, _ := strconv.ParseInt(r.URL.Query().Get("duration"), 10, 64)
			// У канала реестра источников несколько, и обычный выбор берёт первый ЖИВОЙ, не глядя
			// на архив. Для записи спрашиваем источник, который архив умеет: иначе запись не
			// запускалась даже там, где catchup-адрес в списке был.
			if ach, ok := iptvStore.ArchiveSourceFor(channelID); ok {
				if cu := iptvCatchupURL(ach.URL, ach.Catchup, start, dur); cu != "" {
					rawURL = cu
					live = false
					ch = &ach // заголовки и UA берём у того же источника
				}
			}
			if live {
				if cu := iptvCatchupURL(ch.URL, ch.Catchup, start, dur); cu != "" {
					rawURL = cu
					live = false
				}
			}
		}

		// Сессия потока + лимит одновременных просмотров профиля. Исчерпание
		// лимита — НЕ кража: человек должен увидеть причину, а не «поток умер»,
		// поэтому отдельный ответ 429, а не 410 из гейта.
		sid, sessErr := proxyLinks.OpenSession(clientIP(r), tgID, deviceKey(r), deviceLimit(r))
		if sessErr == proxylink.ReasonTooManyStreams {
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"error":          "Слишком много одновременных просмотров",
				"reason":         "too_many_streams",
				"active_streams": proxyLinks.ActiveStreams(tgID),
				"max_streams":    deviceLimit(r),
			})
			return
		}
		streamURL, resolveErr := iptvResolveStreamURL(rawURL, ch, pl, proxyLinks, clientIP(r), hostFromRequest(r),
			r.URL.Query().Get("direct") == "1", sid)
		if resolveErr != "" {
			status := http.StatusBadGateway
			if resolveErr == "stream proxy unavailable" {
				status = http.StatusServiceUnavailable
			}
			writeJSON(w, status, map[string]any{"error": resolveErr})
			return
		}

		catchupDays := 0
		if ch.Catchup != nil {
			catchupDays = ch.Catchup.Days
		}

		// «Эконом»: re-encode the live channel to a lower resolution for a thin pipe. Only for the
		// live edge (never archive/catchup) and only when the client asked (?econom=1). Passes the RAW
		// upstream URL + channel headers (not the client-IP-bound /proxy URL) so the transcode server
		// can actually fetch it. Falls back to the direct stream if the transcode couldn't start.
		if live && wantEconom(r) {
			if econURL := iptvEconomPlaylist(cfg, proxyLinks, ch, rawURL, hostFromRequest(r), transSvc); econURL != "" {
				streamURL = econURL
			}
		}

		payload := map[string]any{
			"url":          streamURL,
			"name":         ch.Name,
			"logo":         channelLogoURL(ch, epg),
			"quality":      ch.Quality,
			"group":        ch.Group,
			"live":         live,
			"catchup_days": catchupDays,
			"econom":       econEnabled(cfg, transSvc), // client shows the «Эконом» option only when honoured
		}
		// What the health probe found INSIDE this channel, when it has been
		// probed. The client knows its own decoder set exactly — the server does
		// not — so it gets the facts and decides for itself whether to warn, to
		// pre-select «Эконом», or to say nothing. Absent field = never probed;
		// the client must treat that as "unknown", not as "fine".
		if info, ok := iptvStore.StreamInfoFor(ch.URL); ok {
			payload["stream_codecs"] = info.Codecs
			payload["stream_tracks"] = info.Tracks
			payload["stream_probed_at"] = info.ProbedAt.Unix()
		}
		writeJSON(w, http.StatusOK, payload)
	}
}
