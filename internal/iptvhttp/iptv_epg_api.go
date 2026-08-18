package iptvhttp

import (
	"net/http"
	"strings"
	"time"

	"lampac-go/internal/iptv"
)

// ---------------------------------------------------------------------------
//  GET /api/iptv/epg/now?channel_ids=id1,id2,...
//  Returns current + next program for each channel.
// ---------------------------------------------------------------------------

func iptvEPGNowHandler(epg *iptv.EPGEngine) http.HandlerFunc {
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

		result := epg.NowNext(ids, time.Now().UTC())
		writeJSON(w, http.StatusOK, map[string]any{
			"epg": result,
		})
	}
}

// ---------------------------------------------------------------------------
//  GET /api/iptv/epg/timeline?channel_id=X&from=UNIX&to=UNIX
//  Returns programs for a channel within a time range.
// ---------------------------------------------------------------------------

func iptvEPGTimelineHandler(epg *iptv.EPGEngine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		channelID := q.Get("channel_id")
		if channelID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "channel_id required"})
			return
		}

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
