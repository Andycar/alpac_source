package iptvhttp

import (
	"net/http"
	"strings"
	"time"

	"lampac-go/internal/iptv"
)

// resolveEPGID превращает id, которым оперирует КЛИЕНТ, в id, которым оперирует
// XMLTV. Для канала реестра это его tvg-id, а когда его нет (панели сплошь без
// tvg-id, а латиничный донор «Russia 1» не склеивается с «Россия 1») — id
// одноимённого EPG-канала. Прочие id возвращаются как есть.
func resolveEPGID(id string, epg *iptv.EPGEngine, store *iptv.Store) string {
	if store == nil || store.Registry() == nil || !iptv.IsRegistryChannelID(id) {
		return id
	}
	rc, ok := store.Registry().Get(id)
	if !ok {
		return id
	}
	// ★Порядок именно такой: сперва tvg-id, но ТОЛЬКО если по нему есть передачи.
	// Плейлисты массово кладут в tvg-id имя канала («41 Регион (Камчатка)»), и
	// прежняя проверка «непустой → берём» отдавала такой id как есть — гид
	// оставался пустым, хотя тот же канал прекрасно находился по имени. На проде
	// это половина витрины: tvg-id заполнен у 18% каналов, а программа доезжала
	// до 48% — остальное теряли ровно здесь.
	if rc.TvgID != "" && epg.HasPrograms(rc.TvgID) {
		return rc.TvgID
	}
	if byName := epg.ResolveIDByName(rc.Name); byName != "" {
		return byName
	}
	// Имя не помогло — возвращаем исходный tvg-id: вдруг программы подъедут
	// со следующим обновлением XMLTV, а id окажется верным.
	if rc.TvgID != "" {
		return rc.TvgID
	}
	return id
}

// ---------------------------------------------------------------------------
//  GET /api/iptv/epg/now?channel_ids=id1,id2,...
//  Returns current + next program for each channel.
// ---------------------------------------------------------------------------

func iptvEPGNowHandler(epg *iptv.EPGEngine, store *iptv.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := r.URL.Query().Get("channel_ids")
		if raw == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "channel_ids required"})
			return
		}

		ids := splitCSV(raw)
		if len(ids) > 200 {
			ids = ids[:200] // cap to avoid abuse
		}

		// Клиент шлёт «tvg_id ИЛИ id канала». У канала реестра без tvg-id это
		// own_*, которого XMLTV не знает — гид был бы пуст. Резолвим такой id по
		// ИМЕНИ канала (панели сплошь без tvg-id), а в ответе возвращаем
		// исходный id: клиент матчит строки гида именно по нему.
		lookup := make([]string, len(ids))
		for i, id := range ids {
			lookup[i] = resolveEPGID(id, epg, store)
		}

		result := epg.NowNext(lookup, time.Now().UTC())
		for i := range result {
			if i < len(ids) {
				result[i].ChannelID = ids[i] // отвечаем тем id, о котором спросили
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"epg": result,
		})
	}
}

// ---------------------------------------------------------------------------
//  GET /api/iptv/epg/timeline?channel_id=X&from=UNIX&to=UNIX
//  Returns programs for a channel within a time range.
// ---------------------------------------------------------------------------

func iptvEPGTimelineHandler(epg *iptv.EPGEngine, store *iptv.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		channelID := q.Get("channel_id")
		if channelID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "channel_id required"})
			return
		}
		// Тот же резолв, что и в /epg/now: own_* → tvg-id, а без него — по имени.
		channelID = resolveEPGID(channelID, epg, store)

		// Default: today +-12h.
		now := time.Now().UTC()
		from := now.Add(-12 * time.Hour)
		to := now.Add(12 * time.Hour)

		if v := q.Get("from"); v != "" {
			if t, err := parseUnixOrRFC3339(v); err == nil {
				from = t
			}
		}
		if v := q.Get("to"); v != "" {
			if t, err := parseUnixOrRFC3339(v); err == nil {
				to = t
			}
		}

		// Cap max range to 7 days. Anchor the clamp on `to` (keep the most RECENT 7 days), not on
		// `from`: a catchup guide looks BACKWARDS from now, and the EPG engine only retains a window
		// around the present — clamping to the oldest 7 days (from..from+7d) of a wide request pushed
		// the window entirely into the dropped past and returned nothing on every channel.
		maxRange := 7 * 24 * time.Hour
		if to.Sub(from) > maxRange {
			from = to.Add(-maxRange)
		}

		programs := epg.Timeline(channelID, from, to)
		if programs == nil {
			programs = []iptv.EPGProgram{}
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"programs":   programs,
			"channel_id": channelID,
			"from":       from.Unix(),
			"to":         to.Unix(),
		})
	}
}

// ---------------------------------------------------------------------------
//  GET /api/iptv/epg/range?channel_ids=a,b,c&from=UNIX&to=UNIX
//  Программа СРАЗУ ДЛЯ НЕСКОЛЬКИХ каналов в окне времени — источник данных для
//  двумерной сетки телегида.
//
//  Почему не /epg/timeline в цикле: сетка показывает десяток строк одновременно,
//  и десять запросов на один экран — это десять резолвов, десять ответов и
//  видимая «лесенка» заполнения. Почему не /epg/now: там только текущая и
//  следующая передача, а сетке нужен весь ряд.
// ---------------------------------------------------------------------------

const (
	epgRangeMaxChannels = 80             // ~в 8 раз больше, чем видно на экране: хватает на упреждающую подгрузку
	epgRangeMaxWindow   = 26 * time.Hour // сутки с запасом; больше в одну выдачу не нужно и вредно по объёму
	epgRangeDescLimit   = 220            // описание в сетке — на две строки панели, полный текст берётся из /timeline
)

func iptvEPGRangeHandler(epg *iptv.EPGEngine, store *iptv.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		raw := q.Get("channel_ids")
		if raw == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "channel_ids required"})
			return
		}
		ids := splitCSV(raw)
		if len(ids) > epgRangeMaxChannels {
			ids = ids[:epgRangeMaxChannels]
		}

		now := time.Now().UTC()
		from := now.Add(-1 * time.Hour)
		to := now.Add(12 * time.Hour)
		if v := q.Get("from"); v != "" {
			if t, err := parseUnixOrRFC3339(v); err == nil {
				from = t
			}
		}
		if v := q.Get("to"); v != "" {
			if t, err := parseUnixOrRFC3339(v); err == nil {
				to = t
			}
		}
		if !to.After(from) {
			to = from.Add(time.Hour)
		}
		// Обрезаем по КОНЦУ окна, как в /epg/timeline: движок держит около суток
		// прошлого, и обрезка по началу увела бы широкий запрос целиком в
		// выброшенное прошлое — сетка оказалась бы пустой на всех каналах.
		if to.Sub(from) > epgRangeMaxWindow {
			from = to.Add(-epgRangeMaxWindow)
		}

		out := make(map[string][]iptv.EPGProgram, len(ids))
		for _, id := range ids {
			progs := epg.Timeline(resolveEPGID(id, epg, store), from, to)
			if len(progs) == 0 {
				continue // пустые каналы не возвращаем: клиент и так рисует «нет данных»
			}
			trimmed := make([]iptv.EPGProgram, len(progs))
			for i, p := range progs {
				p.Description = truncateRunes(p.Description, epgRangeDescLimit)
				p.Icon = "" // в сетке не показывается, а весит больше самой передачи
				trimmed[i] = p
			}
			out[id] = trimmed // отвечаем ТЕМ id, о котором спросили (см. /epg/now)
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"epg":  out,
			"from": from.Unix(),
			"to":   to.Unix(),
		})
	}
}

// truncateRunes режет по РУНАМ, а не по байтам: обрезка кириллицы по байту
// оставляет «хвост» в виде битого символа, и клиент показывает ромб с вопросом.
func truncateRunes(s string, limit int) string {
	if limit <= 0 || s == "" {
		return s
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return strings.TrimRight(string(runes[:limit]), " ,.;:—-") + "…"
}

// ---------------------------------------------------------------------------
//  GET /api/iptv/epg/status
//  Diagnostics for an empty guide: per-source fetch result, channel/programme
//  counts, and whether the tvg-id filter stranded everything. No server log needed.
// ---------------------------------------------------------------------------

func iptvEPGStatusHandler(epg *iptv.EPGEngine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, epg.Status())
	}
}

// ---------------------------------------------------------------------------
//  GET /api/iptv/epg/search?q=...&limit=50
//  Search programs by title/description.
// ---------------------------------------------------------------------------

func iptvEPGSearchHandler(epg *iptv.EPGEngine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("q")
		if query == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "q required"})
			return
		}

		limit := 50
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := parseInt(v); err == nil && n > 0 && n <= 200 {
				limit = n
			}
		}

		results := epg.Search(query, limit)
		if results == nil {
			results = []iptv.EPGProgram{}
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"programs": results,
			"query":    query,
		})
	}
}

// ---------------------------------------------------------------------------
//  Helpers
// ---------------------------------------------------------------------------

func splitCSV(s string) []string {
	var result []string
	for part := range strings.SplitSeq(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func parseUnixOrRFC3339(s string) (time.Time, error) {
	// Try unix timestamp first.
	if n, err := parseInt(s); err == nil {
		return time.Unix(int64(n), 0).UTC(), nil
	}
	// Try RFC3339.
	return time.Parse(time.RFC3339, s)
}

func parseInt(s string) (int, error) {
	n := 0
	neg := false
	i := 0
	if len(s) > 0 && s[0] == '-' {
		neg = true
		i = 1
	}
	for ; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, &parseError{s}
		}
		n = n*10 + int(c-'0')
	}
	if neg {
		n = -n
	}
	if i == 0 || (neg && i == 1) {
		return 0, &parseError{s}
	}
	return n, nil
}

type parseError struct {
	s string
}

func (e *parseError) Error() string { return "invalid number: " + e.s }
