package transcode

import (
	"strconv"
	"strings"
)

// ParseCapsToken разбирает компактный список возможностей клиента — `caps=h264,hevc,hevc10,aac,
// maxh1080` (что ПРОБОВАЛ сам клиент: MediaSource.isTypeSupported в браузере, MediaCodecList на
// Android TV). Один разбор для транскодера (transcodesvc) и для пометок «не сыграет» на
// /capi/streams — иначе два клиента и два сервиса разъезжаются в названиях токенов.
// Пустая строка → nil (клиент ничего не сообщил, остаётся угадывание по UA).
func ParseCapsToken(raw, lang string) *ClientCaps {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	has := map[string]bool{}
	maxHeight := 0
	for _, t := range strings.Split(raw, ",") {
		if t = strings.TrimSpace(strings.ToLower(t)); t == "" {
			continue
		}
		// maxh<N> — потолок высоты видео (физический режим вывода дисплея клиента):
		// выше него видео не отдаётся stream-copy, см. ClientCaps.MaxHeight.
		if n, ok := strings.CutPrefix(t, "maxh"); ok {
			if v, err := strconv.Atoi(n); err == nil && v >= 240 && v <= 4320 {
				maxHeight = v
			}
			continue
		}
		has[t] = true
	}
	if lang = strings.TrimSpace(lang); lang == "" {
		lang = "ru"
	}
	return &ClientCaps{
		MaxHeight:     maxHeight,
		Lang:          lang,
		Platform:      "browser",
		CanPlayMKV:    has["mkv"],
		CanPlayH264:   has["h264"],
		CanPlayHEVC:   has["hevc"],
		CanPlayHEVC10: has["hevc10"],
		CanPlayAV1:    has["av1"],
		CanPlayVP9:    has["vp9"],
		CanPlayAAC:    has["aac"],
		CanPlayAC3:    has["ac3"],
		CanPlayEAC3:   has["eac3"],
		CanPlayDTS:    has["dts"],
		CanPlayTrueHD: has["truehd"],
		CanPlayFLAC:   has["flac"],
		CanPlayOpus:   has["opus"],
		CanPlayMP3:    has["mp3"],
	}
}

// UnplayableReason — почему поток с такими маркерами этот клиент не сыграет нативно:
// "" = сыграет (или неизвестно). codec — маркер из названия/качества ("hevc","av1","vp9",""),
// height — высота качества, hdr — HDR/DV (на практике HEVC Main10). Это пометка для выбора
// качества по умолчанию, а не запрет: клиент может всё равно попросить и уйти в транскод.
func (c *ClientCaps) UnplayableReason(codec string, height int, hdr bool) string {
	if c == nil {
		return ""
	}
	switch codec {
	case "hevc":
		if !c.CanPlayHEVC {
			return "hevc"
		}
	case "av1":
		if !c.CanPlayAV1 {
			return "av1"
		}
	case "vp9":
		if !c.CanPlayVP9 {
			return "vp9"
		}
	}
	if hdr && height >= 2160 && !c.CanPlayHEVC10 {
		if !c.CanPlayHEVC {
			return "hevc"
		}
		return "hevc10"
	}
	if c.MaxHeight > 0 && height > c.MaxHeight {
		return "maxh"
	}
	return ""
}
