package transcode

import (
	"fmt"
	"strconv"
	"strings"
)

// DoviFromProbe reads Dolby Vision metadata (profile/level/BL-compat/codec) from
// the first video stream's side_data_list. ok=false when no DoVi is present.
// Carved out of the httpapi HLS-codec file with a local probeIntField copy so
// this package stays self-contained.
func DoviFromProbe(probe map[string]any) (profile, level, compat int, codec string, ok bool) {
	if probe == nil {
		return 0, 0, 0, "", false
	}
	streams, sok := probe["streams"].([]any)
	if !sok {
		return 0, 0, 0, "", false
	}
	for _, s := range streams {
		sm, _ := s.(map[string]any)
		if sm == nil || fmt.Sprint(sm["codec_type"]) != "video" {
			continue
		}
		codec = strings.ToLower(fmt.Sprint(sm["codec_name"]))
		sdl, _ := sm["side_data_list"].([]any)
		for _, sd := range sdl {
			sdm, _ := sd.(map[string]any)
			if sdm == nil {
				continue
			}
			if !strings.Contains(strings.ToUpper(fmt.Sprint(sdm["side_data_type"])), "DOVI") {
				continue
			}
			return probeIntField(sdm, "dv_profile"),
				probeIntField(sdm, "dv_level"),
				probeIntField(sdm, "dv_bl_signal_compatibility_id"),
				codec, true
		}
		break // only inspect the first video stream
	}
	return 0, 0, 0, "", false
}

func probeIntField(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return 0
}
