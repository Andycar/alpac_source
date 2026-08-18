package httpapi

import (
	"regexp"
	"strconv"
	"strings"
)

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

// qualityBadge converts a max video height to a display badge string.
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

// maxQualityFromMap returns the badge string for the highest quality in a map.
// Keys should be like "1080p", "720p", etc.
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

// normalizeQualityLabel converts raw quality labels (p-notation or text)
// to standardized badge format: 4K / 2K / FHD / HD / SD.
// Used by videodb, kinopub, zetflix, etc. to show consistent quality labels.
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
	// Unknown — try to parse numeric height.
	s := strings.TrimSuffix(strings.ToLower(raw), "p")
	if h, err := strconv.Atoi(s); err == nil {
		return qualityBadge(h)
	}
	return raw
}

// bestQualityLabel returns the key with the highest resolution from a quality map.
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
