package litesrc

// Pure helpers copied from httpapi (the shared-drawer copy pattern: originals
// stay for the ~60 in-monolith sources) + thin forwarders to the litehtml
// leaf so moved source files need zero call-site changes.

import (
	stdjson "encoding/json"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"lampac-go/internal/litehtml"
)

// hostFromRequest mirrors httpapi/plugins.go: scheme://host as seen by the
// client, preferring X-Forwarded-Proto/-Host (reverse-proxy chain).
func hostFromRequest(r *http.Request) string {
	scheme := "http"
	if xf := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); xf != "" {
		scheme = xf
	} else if r.TLS != nil {
		scheme = "https"
	}

	var host string
	if xfh := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); xfh != "" {
		if i := strings.IndexByte(xfh, ','); i > 0 {
			xfh = strings.TrimSpace(xfh[:i])
		}
		host = xfh
	}
	if host == "" {
		host = r.Host
	}
	if host == "" {
		host = r.RemoteAddr
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
	}

	return scheme + "://" + host
}

// parseBoolParam mirrors httpapi/lite_events.go.
func parseBoolParam(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// normalizeSearchTitleReplacer mirrors httpapi/kinobase.go (built once).
var normalizeSearchTitleReplacer = strings.NewReplacer(
	"\"", " ", "'", " ", "`", " ",
	".", " ", ",", " ", ";", " ", ":", " ",
	"(", " ", ")", " ", "[", " ", "]", " ",
	"{", " ", "}", " ", "-", " ", "_", " ",
	"/", " ", "\\", " ", "!", " ", "?", " ",
)

func normalizeSearchTitle(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	v = strings.ReplaceAll(v, "ё", "е")
	v = normalizeSearchTitleReplacer.Replace(v)
	return strings.Join(strings.Fields(v), " ")
}

func submatch1(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// normalizeQualityBadge mirrors httpapi/lite_events.go: raw quality string →
// canonical badge ("4K"/"FHD"/"HD"/"SD"/"").
func normalizeQualityBadge(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	up := strings.ToUpper(raw)

	switch up {
	case "4K", "UHD", "2160P", "2160", "ULTRA HD", "ULTRA":
		return "4K"
	case "2K", "QHD", "WQHD", "1440P", "1440":
		return "2K"
	case "FHD", "FULLHD", "FULL HD", "1080P", "1080":
		return "FHD"
	case "HD", "720P", "720":
		return "HD"
	case "SD", "480P", "360P", "240P", "480", "360", "240":
		return "SD"
	}

	if strings.Contains(up, "2160") || strings.Contains(up, "4K") || strings.Contains(up, "UHD") {
		return "4K"
	}
	if strings.Contains(up, "1440") || strings.Contains(up, "QHD") {
		return "2K"
	}
	if strings.Contains(up, "1080") {
		return "FHD"
	}
	if strings.Contains(up, "720") {
		return "HD"
	}

	if strings.Contains(up, "BDRIP") || strings.Contains(up, "BDREMUX") || strings.Contains(up, "BLURAY") || strings.Contains(up, "BLU-RAY") {
		return "FHD"
	}
	if strings.Contains(up, "HDRIP") || strings.Contains(up, "WEBRIP") || strings.Contains(up, "WEB-DL") || strings.Contains(up, "WEBDL") {
		return "HD"
	}
	if strings.Contains(up, "DVDRIP") || strings.Contains(up, "TVRIP") || strings.Contains(up, "CAMRIP") || strings.Contains(up, "CAM") {
		return "SD"
	}

	return ""
}

// sanitizeQualityBadge mirrors httpapi/lite_events.go.
func sanitizeQualityBadge(raw string) string {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "4K", "2K", "FHD", "HD", "SD":
		return strings.ToUpper(strings.TrimSpace(raw))
	}
	return normalizeQualityBadge(raw)
}

// writeCheckSearchResponse mirrors httpapi/lite_events.go: the standard
// checksearch JSON response; quality sanitized to the canonical badge set.
func writeCheckSearchResponse(w http.ResponseWriter, show bool, quality string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if show {
		q := sanitizeQualityBadge(quality)
		if q != "" {
			_, _ = fmt.Fprintf(w, `{"type":"movie","rch":true,"quality":"%s"}`, q)
		} else {
			_, _ = w.Write([]byte(`{"type":"movie","rch":true}`))
		}
		return
	}
	_, _ = w.Write([]byte(`{"rch":false}`))
}

// writeCheckSearchResponseNoRCH mirrors httpapi/lite_events.go (rch:false —
// balancers that appear in search without auto-recommendation).
func writeCheckSearchResponseNoRCH(w http.ResponseWriter, show bool, quality string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if show {
		q := sanitizeQualityBadge(quality)
		if q != "" {
			_, _ = fmt.Fprintf(w, `{"type":"movie","rch":false,"quality":"%s"}`, q)
		} else {
			_, _ = w.Write([]byte(`{"type":"movie","rch":false}`))
		}
		return
	}
	_, _ = w.Write([]byte(`{"rch":false}`))
}

// qualityBadge mirrors httpapi/hls_quality.go.
func qualityBadge(maxHeight int) string {
	switch {
	case maxHeight >= 2160:
		return "4K"
	case maxHeight >= 1080:
		return "FHD"
	case maxHeight >= 720:
		return "HD"
	default:
		return "SD"
	}
}

// normalizeQualityLabel mirrors httpapi/hls_quality.go.
func normalizeQualityLabel(raw string) string {
	raw = strings.TrimSpace(raw)
	switch strings.ToLower(raw) {
	case "авто", "auto":
		return "auto"
	case "4k", "2160p", "2160", "uhd":
		return "4K"
	case "2k", "1440p", "1440":
		return "2K"
	case "fhd", "1080p", "1080":
		return "FHD"
	case "hd", "720p", "720":
		return "HD"
	case "sd", "480p", "480", "360p", "360":
		return "SD"
	}
	s := strings.TrimSuffix(strings.ToLower(raw), "p")
	if h, err := strconv.Atoi(s); err == nil {
		return qualityBadge(h)
	}
	return raw
}

// toString mirrors httpapi/bookmark_api.go: pure any→string.
func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	default:
		if v == nil {
			return ""
		}
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

// Mirrors of httpapi/hls_quality.go (shared-drawer copies).
var hlsStreamInfRe = regexp.MustCompile(`#EXT-X-STREAM-INF:[^\n]*RESOLUTION=(\d+)x(\d+)[^\n]*\n([^\n\r]+)`)

// parseHLSQualities parses an HLS master playlist body and extracts
// quality variant URLs keyed by resolution label (e.g. "1080p").
// Relative variant URLs are resolved against baseURL.
func parseHLSQualities(body, baseURL string) map[string]string {
	matches := hlsStreamInfRe.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return nil
	}

	basePath := baseURL
	if idx := strings.LastIndex(basePath, "/"); idx >= 0 {
		basePath = basePath[:idx+1]
	}

	result := make(map[string]string, len(matches))
	for _, m := range matches {
		if len(m) < 4 {
			continue
		}
		height, _ := strconv.Atoi(m[2])
		if height <= 0 {
			continue
		}
		variantURL := strings.TrimSpace(m[3])
		if variantURL == "" {
			continue
		}
		if !strings.HasPrefix(variantURL, "http") {
			variantURL = basePath + variantURL
		}
		label := strconv.Itoa(height) + "p"
		// Keep higher bandwidth variant if duplicate resolution.
		if _, exists := result[label]; !exists {
			result[label] = variantURL
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// truncURL mirrors httpapi/balancer_fetch.go (logging helper).
func truncURL(u string) string {
	if len(u) > 120 {
		return u[:120] + "…"
	}
	return u
}

// firstQuery mirrors httpapi/iptvonline.go: first value for a query key.
func firstQuery(q map[string][]string, key string) string {
	vals := q[key]
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

// qualityBadgeRank mirrors httpapi/lite_events.go.
func qualityBadgeRank(badge string) int {
	switch badge {
	case "4K":
		return 5
	case "2K":
		return 4
	case "FHD":
		return 3
	case "HD":
		return 2
	case "SD":
		return 1
	default:
		return 0
	}
}

// checksearchQualityRe + evaluateSearchResult mirror httpapi/lite_events.go.
var checksearchQualityRe = regexp.MustCompile(`"quality"\s*:\s*"([^"]+)"`)

func evaluateSearchResult(body string) (show bool, rch bool, quality string) {
	rch = strings.Contains(body, "\"rch\":true")
	show = rch ||
		strings.Contains(body, "data-json=") ||
		strings.Contains(body, "\"type\":\"movie\"") ||
		strings.Contains(body, "\"type\":\"episode\"") ||
		strings.Contains(body, "\"type\":\"season\"")

	if m := checksearchQualityRe.FindStringSubmatch(body); len(m) >= 2 {
		quality = strings.TrimSpace(m[1])
	}
	return show, rch, quality
}

// bestQualityLabel / maxQualityFromMap mirror httpapi/hls_quality.go.
func bestQualityLabel(quals map[string]string) string {
	best := ""
	bestH := 0
	for label := range quals {
		h, _ := strconv.Atoi(strings.TrimSuffix(label, "p"))
		if h > bestH {
			bestH = h
			best = label
		}
	}
	if best != "" {
		return best
	}
	// Fallback: return any key.
	for label := range quals {
		return label
	}
	return ""
}

func maxQualityFromMap(quals map[string]string) string {
	maxH := 0
	for label := range quals {
		h, _ := strconv.Atoi(strings.TrimSuffix(label, "p"))
		if h > maxH {
			maxH = h
		}
	}
	if maxH == 0 {
		return ""
	}
	return qualityBadge(maxH)
}

// firstNonEmpty mirrors httpapi/servers_api.go.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// loopbackHostPort mirrors httpapi/lite_events.go: normalise a listen addr
// to a dialable 127.0.0.1:port (used by the xsearch loopback adapter).
func loopbackHostPort(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if strings.HasPrefix(addr, ":") {
		return "127.0.0.1" + addr
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	host = strings.TrimSpace(host)
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// flexInt mirrors httpapi/kp_catalog.go: an int decodable from JSON number
// OR string (kino.pub v2.1 returns year as string, v2.2 as number).
type flexInt int

func (fi *flexInt) UnmarshalJSON(data []byte) error {
	var n int
	if err := stdjson.Unmarshal(data, &n); err == nil {
		*fi = flexInt(n)
		return nil
	}
	var s string
	if err := stdjson.Unmarshal(data, &s); err == nil {
		var parsed int
		fmt.Sscanf(s, "%d", &parsed)
		*fi = flexInt(parsed)
		return nil
	}
	return nil
}

// litehtml forwarders — keep moved source files call-site-stable.

func getsTVAppendVoiceHTML(sb *strings.Builder, data map[string]any) {
	litehtml.AppendVoiceHTML(sb, data)
}

func getsTVAppendMovieHTML(sb *strings.Builder, data map[string]any, text string, focused bool, season, episode int) {
	litehtml.AppendMovieHTML(sb, data, text, focused, season, episode)
}

func getsTVAppendSeasonHTML(sb *strings.Builder, data map[string]any, text string, focused bool) {
	litehtml.AppendSeasonHTML(sb, data, text, focused)
}

func writeGetsTVEmpty(w http.ResponseWriter, rjson bool) {
	litehtml.WriteEmpty(w, rjson)
}

func getsTVJoinName(ru, en string) string {
	return litehtml.JoinName(ru, en)
}

func getsTVBool(v bool) string {
	return litehtml.Bool(v)
}

func getsTVAttrJSON(v any) string {
	return litehtml.AttrJSON(v)
}

func getsTVBoolAny(v any) bool {
	return litehtml.BoolAny(v)
}

func getsTVQueryInt(v string) (int, bool) {
	return litehtml.QueryInt(v)
}
