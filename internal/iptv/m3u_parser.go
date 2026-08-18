package iptv

import (
	"bufio"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
//  Streaming M3U parser
// ---------------------------------------------------------------------------

// ParseM3U parses an M3U playlist from r, calling callback for each channel.
// Never loads the entire file into memory — supports 100K+ channel playlists.
func ParseM3U(r io.Reader, callback func(Channel)) (M3UHeader, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // up to 1MB lines

	var header M3UHeader
	var pending *extinf // parsed #EXTINF waiting for URL line
	num := 0

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		// --- #EXTM3U header ---
		if strings.HasPrefix(line, "#EXTM3U") {
			header = parseM3UHeader(line)
			continue
		}

		// --- #EXTINF line ---
		if strings.HasPrefix(line, "#EXTINF:") {
			pending = parseEXTINF(line)
			continue
		}

		// --- #EXTVLCOPT / #EXTHTTP — extra directives ---
		if pending != nil {
			if strings.HasPrefix(line, "#EXTVLCOPT:") {
				parseVLCOpt(line, pending)
				continue
			}
			if strings.HasPrefix(line, "#EXTHTTP:") {
				parseExtHTTP(line, pending)
				continue
			}
			// #EXTGRP: — group on its OWN line (some providers use this INSTEAD of the inline
			// group-title="..." attribute; without it the whole playlist fell into one bucket →
			// no category split). group-title= wins if both are present.
			if strings.HasPrefix(line, "#EXTGRP:") {
				if pending.group == "" {
					pending.group = strings.TrimSpace(line[len("#EXTGRP:"):])
				}
				continue
			}
			// Skip other comments
			if strings.HasPrefix(line, "#") {
				continue
			}
		} else if strings.HasPrefix(line, "#") {
			continue
		}

		// --- URL line (non-comment, non-directive) ---
		if pending == nil {
			continue // orphan URL without #EXTINF
		}

		url := line
		if url == "" || (!strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "rtsp://") && !strings.HasPrefix(url, "rtp://")) {
			pending = nil
			continue
		}

		num++
		ch := Channel{
			ID:        channelID(url, pending.name),
			Name:      pending.name,
			CleanName: cleanChannelName(pending.name),
			URL:       url,
			Logo:      pending.logo,
			Group:     pending.group,
			TvgID:     pending.tvgID,
			TvgName:   pending.tvgName,
			Number:    num,
			Quality:   detectQuality(pending.name),
			UserAgent: pending.userAgent,
			Referer:   pending.referer,
		}

		if pending.catchupType != "" {
			ch.Catchup = &Catchup{
				Type:   pending.catchupType,
				Days:   pending.catchupDays,
				Source: pending.catchupSource,
			}
		}

		callback(ch)
		pending = nil
	}

	return header, scanner.Err()
}

// ---------------------------------------------------------------------------
//  Internal types and parsers
// ---------------------------------------------------------------------------

type extinf struct {
	name          string
	logo          string
	group         string
	tvgID         string
	tvgName       string
	userAgent     string
	referer       string
	catchupType   string
	catchupDays   int
	catchupSource string
}

// parseM3UHeader parses attributes from the #EXTM3U line.
func parseM3UHeader(line string) M3UHeader {
	h := M3UHeader{}
	if u := extractAttr(line, "x-tvg-url"); u != "" {
		for url := range strings.SplitSeq(u, ",") {
			url = strings.TrimSpace(url)
			if url != "" {
				h.EPGUrls = append(h.EPGUrls, url)
			}
		}
	}
	if u := extractAttr(line, "url-tvg"); u != "" && len(h.EPGUrls) == 0 {
		for url := range strings.SplitSeq(u, ",") {
			url = strings.TrimSpace(url)
			if url != "" {
				h.EPGUrls = append(h.EPGUrls, url)
			}
		}
	}
	h.CatchupType = extractAttr(line, "catchup")
	if d := extractAttr(line, "catchup-days"); d != "" {
		h.CatchupDays, _ = strconv.Atoi(d)
	}
	return h
}

// parseEXTINF parses a #EXTINF:-1 tvg-id="..." line.
func parseEXTINF(line string) *extinf {
	e := &extinf{}

	e.tvgID = extractAttr(line, "tvg-id")
	e.tvgName = extractAttr(line, "tvg-name")
	e.logo = extractAttr(line, "tvg-logo")
	e.group = extractAttr(line, "group-title")
	e.userAgent = extractAttr(line, "user-agent")
	e.referer = extractAttr(line, "referer")
	e.catchupType = extractAttr(line, "catchup")
	if e.catchupType == "" {
		e.catchupType = extractAttr(line, "catchup-type")
	}
	if d := extractAttr(line, "catchup-days"); d != "" {
		e.catchupDays, _ = strconv.Atoi(d)
	}
	e.catchupSource = extractAttr(line, "catchup-source")

	// Channel name is after the last comma.
	if idx := strings.LastIndex(line, ","); idx >= 0 {
		e.name = strings.TrimSpace(line[idx+1:])
	}

	return e
}

// parseVLCOpt handles #EXTVLCOPT:key=value directives.
func parseVLCOpt(line string, e *extinf) {
	val := strings.TrimPrefix(line, "#EXTVLCOPT:")
	val = strings.TrimSpace(val)
	if after, ok := strings.CutPrefix(val, "http-user-agent="); ok {
		e.userAgent = after
	} else if after, ok := strings.CutPrefix(val, "http-referrer="); ok {
		e.referer = after
	} else if after, ok := strings.CutPrefix(val, "http-referer="); ok {
		e.referer = after
	}
}

// parseExtHTTP handles #EXTHTTP:{"User-Agent":"..."} directives.
func parseExtHTTP(line string, e *extinf) {
	val := strings.TrimPrefix(line, "#EXTHTTP:")
	val = strings.TrimSpace(val)
	// Simple key-value extraction without full JSON parser.
	if ua := extractJSONValue(val, "User-Agent"); ua != "" {
		e.userAgent = ua
	}
	if ref := extractJSONValue(val, "Referer"); ref != "" {
		e.referer = ref
	}
	if ref := extractJSONValue(val, "Referrer"); ref != "" && e.referer == "" {
		e.referer = ref
	}
}

// ---------------------------------------------------------------------------
//  Attribute extraction
// ---------------------------------------------------------------------------

// extractAttr extracts value from key="value" or key='value' in a line.
func extractAttr(line, key string) string {
	search := key + "=\""
	idx := strings.Index(strings.ToLower(line), strings.ToLower(search))
	if idx >= 0 {
		start := idx + len(search)
		end := strings.IndexByte(line[start:], '"')
		if end >= 0 {
			return line[start : start+end]
		}
	}
	// Try single quotes.
	search = key + "='"
	idx = strings.Index(strings.ToLower(line), strings.ToLower(search))
	if idx >= 0 {
		start := idx + len(search)
		end := strings.IndexByte(line[start:], '\'')
		if end >= 0 {
			return line[start : start+end]
		}
	}
	return ""
}

// extractJSONValue does a naive extraction of "key":"value" from a JSON-ish string.
func extractJSONValue(s, key string) string {
	search := fmt.Sprintf(`"%s":"`, key)
	idx := strings.Index(s, search)
	if idx < 0 {
		search = fmt.Sprintf(`"%s": "`, key) // with space
		idx = strings.Index(s, search)
	}
	if idx < 0 {
		return ""
	}
	start := idx + len(search)
	end := strings.IndexByte(s[start:], '"')
	if end < 0 {
		return ""
	}
	return s[start : start+end]
}

// ---------------------------------------------------------------------------
//  Channel name helpers
// ---------------------------------------------------------------------------

var reQualityTag = regexp.MustCompile(`(?i)\s*[\(\[]*\s*(4K|UHD|2160[pi]?|FHD|1080[pi]?|HD|720[pi]?|SD|480[pi]?|360[pi]?|LQ|HQ)\s*[\)\]]*\s*`)

func cleanChannelName(name string) string {
	clean := reQualityTag.ReplaceAllString(name, " ")
	return strings.TrimSpace(clean)
}

func detectQuality(name string) string {
	upper := strings.ToUpper(name)
	switch {
	case strings.Contains(upper, "4K") || strings.Contains(upper, "UHD") || strings.Contains(upper, "2160"):
		return "4K"
	case strings.Contains(upper, "FHD") || strings.Contains(upper, "1080"):
		return "FHD"
	case strings.Contains(upper, "HD") || strings.Contains(upper, "720"):
		return "HD"
	case strings.Contains(upper, "SD") || strings.Contains(upper, "480") || strings.Contains(upper, "360"):
		return "SD"
	}
	return ""
}

// channelID generates a deterministic ID for a channel.
func channelID(url, name string) string {
	h := md5.Sum([]byte(url + "|" + name))
	return hex.EncodeToString(h[:8]) // 16-char hex
}
