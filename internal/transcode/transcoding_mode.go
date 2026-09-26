package transcode

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/rs/zerolog/log"
)

// ---------------------------------------------------------------------------
// Smart mode selection for transcoding.
//
// selectMode() chooses the cheapest viable path based on:
//   - Source probe data (video/audio/subtitle codecs)
//   - Client capabilities (what the player natively supports)
//
// Ordering from cheapest to most expensive:
//
//	native       → client can play the source as-is, skip transcoding entirely
//	direct       → URL is already HLS/playable MP4, short-circuit to original URL
//	remux        → video + audio stream-copy into HLS (no re-encoding)
//	audio-only   → video stream-copy, audio re-encoded to AAC
//	hw-transcode → video re-encoded via HW accelerator (P1, falls through to SW here)
//	sw-transcode → video re-encoded via libx264 (CPU)
//
// The goal is to pick the weakest intervention that still plays on the client.
// ---------------------------------------------------------------------------

// TranscodingMode is the selected pipeline.
type TranscodingMode string

const (
	ModeNative      TranscodingMode = "native"
	ModeDirect      TranscodingMode = "direct"
	ModeRemux       TranscodingMode = "remux"
	ModeAudioOnly   TranscodingMode = "audio-only"
	ModeHWTranscode TranscodingMode = "hw-transcode"
	ModeSWTranscode TranscodingMode = "sw-transcode"
)

// ClientCaps describes what the calling client/player can decode natively.
// Sent by the plugin via the /transcoding/start body.
type ClientCaps struct {
	// Preferred audio language (Lampa.Storage language), e.g. "ru", "uk", "en".
	Lang string `json:"lang"`

	// Container support.
	CanPlayMKV bool `json:"canPlayMKV"`

	// Codec support.
	CanPlayH264   bool `json:"canPlayH264"`
	CanPlayHEVC   bool `json:"canPlayHEVC"`
	CanPlayHEVC10 bool `json:"canPlayHEVC10"` // HEVC main10 (10-bit)
	CanPlayAV1    bool `json:"canPlayAV1"`
	CanPlayVP9    bool `json:"canPlayVP9"`
	CanPlayAAC    bool `json:"canPlayAAC"`
	CanPlayAC3    bool `json:"canPlayAC3"`
	CanPlayEAC3   bool `json:"canPlayEAC3"`
	CanPlayDTS    bool `json:"canPlayDTS"`
	CanPlayTrueHD bool `json:"canPlayTrueHD"`
	CanPlayFLAC   bool `json:"canPlayFLAC"`
	CanPlayOpus   bool `json:"canPlayOpus"`
	CanPlayMP3    bool `json:"canPlayMP3"`

	// Platform hint ("android-tv" | "webos" | "tizen" | "browser" | "").
	Platform string `json:"platform"`

	// MaxHeight — потолок высоты видео для этого клиента (физический режим вывода его
	// дисплея; caps-токен maxh<N>). Видео ВЫШЕ потолка нельзя отдавать stream-copy:
	// выше режима вывода панель всё равно не покажет, а часть SoC (MStar) на
	// 4K-directplay «берёт» дорожку декодером и рисует чёрный экран без единой ошибки.
	// 0 = потолка нет (клиент не сообщил) — поведение как раньше.
	MaxHeight int `json:"maxHeight,omitempty"`

	// Opt-out escape hatches.
	ForceTranscode bool `json:"forceTranscode"` // always transcode, ignore caps
	PreferRemux    bool `json:"preferRemux"`    // remux even when native is possible
}

// isOmnivorous reports whether the client can natively play MKV plus the full
// codec matrix (HEVC/HEVC10/AV1 + AC3/EAC3/DTS) — i.e. a software-decoding
// player like VLC / Kodi / mpv for which any server-side transcoding is wasted
// CPU.  Used to keep these clients on the native (direct-URL) path and to
// suppress the subtitle burn-in override that would otherwise force a re-encode.
func (c ClientCaps) IsOmnivorous() bool {
	return c.CanPlayMKV && c.CanPlayHEVC && c.CanPlayHEVC10 &&
		c.CanPlayAC3 && c.CanPlayEAC3 && c.CanPlayDTS
}

// ModeDecision is the result of selectMode().
type ModeDecision struct {
	Mode          TranscodingMode
	Reason        string // human-readable explanation
	AudioRelIndex int    // 0-based index of audio stream (relative to audio streams only)
	AudioIsCopy   bool   // true if audio is stream-copied (only relevant for remux/audio-only)
	VideoIsCopy   bool   // true if video is stream-copied
	Warning       string // non-empty when best-effort (probe failed)
}

// selectMode is a pure function: given probe data + client caps + config,
// returns the chosen mode and reason.
func SelectMode(probe map[string]any, caps ClientCaps, preferNative bool) ModeDecision {
	// Best-effort path: probe missing or empty → fall back to sw-transcode.
	// Audio index defaults to 0, caller may override.
	if probe == nil {
		return ModeDecision{
			Mode:          ModeSWTranscode,
			Reason:        "probe unavailable, best-effort sw transcode",
			AudioRelIndex: 0,
			Warning:       "probe failed, best-effort mode",
		}
	}

	streams, _ := probe["streams"].([]any)
	if len(streams) == 0 {
		return ModeDecision{
			Mode:          ModeSWTranscode,
			Reason:        "probe returned no streams, best-effort sw transcode",
			AudioRelIndex: 0,
			Warning:       "probe returned no streams",
		}
	}

	// Inspect video stream (take first).
	var (
		vCodec  string
		vPixFmt string
		vHeight int
	)
	for _, s := range streams {
		sm, _ := s.(map[string]any)
		if sm == nil || fmt.Sprint(sm["codec_type"]) != "video" {
			continue
		}
		vCodec = strings.ToLower(fmt.Sprint(sm["codec_name"]))
		vPixFmt = strings.ToLower(fmt.Sprint(sm["pix_fmt"]))
		if h, ok := sm["height"].(float64); ok {
			vHeight = int(h)
		}
		break
	}

	// Build audio summary (all audio streams with their metadata).
	type audioInfo struct {
		relIdx    int
		codec     string
		lang      string
		channels  int
		title     string
		isDefault bool
	}
	var audios []audioInfo
	rel := 0
	for _, s := range streams {
		sm, _ := s.(map[string]any)
		if sm == nil || fmt.Sprint(sm["codec_type"]) != "audio" {
			continue
		}
		info := audioInfo{
			relIdx: rel,
			codec:  strings.ToLower(fmt.Sprint(sm["codec_name"])),
		}
		if ch, ok := sm["channels"].(float64); ok {
			info.channels = int(ch)
		}
		if tags, ok := sm["tags"].(map[string]any); ok {
			if l, ok := tags["language"].(string); ok {
				info.lang = strings.ToLower(l)
			}
			if t, ok := tags["title"].(string); ok {
				info.title = t
			}
		}
		if disp, ok := sm["disposition"].(map[string]any); ok {
			if d, ok := disp["default"].(float64); ok && d > 0 {
				info.isDefault = true
			}
		}
		audios = append(audios, info)
		rel++
	}

	// Pick audio track by language → default → first.
	audioIdx := 0
	if len(audios) > 0 {
		picked := -1
		wanted := NormalizeLangCodes(caps.Lang)
		if len(wanted) > 0 {
			for _, a := range audios {
				if a.lang == "" {
					continue
				}
				for _, w := range wanted {
					if a.lang == w {
						picked = a.relIdx
						break
					}
				}
				if picked >= 0 {
					break
				}
			}
		}
		if picked < 0 {
			for _, a := range audios {
				if a.isDefault {
					picked = a.relIdx
					break
				}
			}
		}
		if picked < 0 {
			picked = 0
		}
		audioIdx = picked
	}

	// Look up the picked audio codec.
	var pickedAudioCodec string
	if audioIdx < len(audios) {
		pickedAudioCodec = audios[audioIdx].codec
	}

	// Explicit force-transcode escape hatch.
	if caps.ForceTranscode {
		return ModeDecision{
			Mode:          ModeSWTranscode,
			Reason:        "forceTranscode requested by client",
			AudioRelIndex: audioIdx,
		}
	}

	videoOK := clientCanPlayVideo(caps, vCodec, vPixFmt)
	audioOK := clientCanPlayAudio(caps, pickedAudioCodec)

	// Клиентский потолок разрешения (caps=maxh<N>, ClientCaps.MaxHeight): видео выше него
	// обязано пройти re-encode с даунскейлом — stream-copy 4K на 1080p-вывод в лучшем случае
	// сжигает канал зря, а на MStar-классе рисует чёрный экран «успешно» (декодер берёт,
	// first frame формально отрисован, ни одной ошибки). Гейт снимает и native, и
	// remux/audio-only ниже: все copy-пути сохраняют исходное разрешение.
	overCap := caps.MaxHeight > 0 && vHeight > caps.MaxHeight
	if overCap {
		videoOK = false
	}

	// Dolby Vision diagnostics. Profiles 7/8 carry an HDR10/HLG-compatible base layer — copying is
	// fine (the master playlist signals dvh1 + SUPPLEMENTAL-CODECS). Profile 5 (IPTPQc2) has NO
	// compatible base: without a DV decoder the copy shows the notorious green/purple tint, and the
	// zscale tonemap can't decode the DV RPU either. We keep the current behaviour (no silent heavy
	// re-encode) but log it loudly so «фиолетовое видео» reports map straight to this line.
	if prof, _, compat, _, ok := DoviFromProbe(probe); ok && prof == 5 && compat == 0 {
		log.Warn().Str("codec", vCodec).Int("dv_profile", prof).
			Msg("transcoding: Dolby Vision profile 5 source — non-DV clients will show tinted colors (no HDR10-compatible base layer)")
	}

	// native: client can play everything as-is → skip transcoding entirely.
	// We still require caps.CanPlayMKV since most sources here are MKV/AVI.
	if preferNative && !caps.PreferRemux && caps.CanPlayMKV && videoOK && audioOK {
		return ModeDecision{
			Mode:          ModeNative,
			Reason:        fmt.Sprintf("native playback: %s + %s", vCodec, pickedAudioCodec),
			AudioRelIndex: audioIdx,
			VideoIsCopy:   true,
			AudioIsCopy:   true,
		}
	}

	// Can the video be stream-copied into fMP4/HLS instead of re-encoded? Yes when it's a muxable
	// codec AND either it's broadly HLS-safe OR the CLIENT explicitly told us it can decode it. The
	// static list conservatively excludes 10-bit HEVC, but e.g. macOS Chrome plays it fine (declared
	// via caps) — copying it avoids the brutal software HEVC→H.264 re-encode (~0.25x on a CPU-only
	// box, which just times out before the first segment). The client owns the truth about what it
	// can decode; we only also require the codec be carriable in fMP4.
	videoCopyable := !overCap && fmp4MuxableVideo(vCodec) &&
		(hlsCompatVideoCodec(vCodec, vPixFmt) || clientCanPlayVideo(caps, vCodec, vPixFmt))

	// remux: video copyable + audio HLS-compatible → copy both (cheapest server-side path).
	if videoCopyable && hlsCompatAudioCodec(pickedAudioCodec) {
		return ModeDecision{
			Mode:          ModeRemux,
			Reason:        fmt.Sprintf("remux: %s + %s → HLS copy", vCodec, pickedAudioCodec),
			AudioRelIndex: audioIdx,
			VideoIsCopy:   true,
			AudioIsCopy:   true,
		}
	}

	// audio-only: video copyable but audio is not → re-encode ONLY the audio (light even at 4K).
	if videoCopyable {
		return ModeDecision{
			Mode:          ModeAudioOnly,
			Reason:        fmt.Sprintf("audio-only: video %s copy, audio %s → aac", vCodec, pickedAudioCodec),
			AudioRelIndex: audioIdx,
			VideoIsCopy:   true,
			AudioIsCopy:   false,
		}
	}

	// Fall through: video needs re-encoding. HW path will be chosen in P1 when
	// an accelerator is available; for P0 we always use SW.
	return ModeDecision{
		Mode:          ModeSWTranscode,
		Reason:        fmt.Sprintf("sw-transcode: video %s → h264", vCodec),
		AudioRelIndex: audioIdx,
		VideoIsCopy:   false,
		AudioIsCopy:   false,
	}
}

// fmp4MuxableVideo reports whether a video codec can be stream-copied into an fMP4/HLS segment at
// all (independent of whether a given client can DECODE it). Used together with client caps to decide
// when copying is safe. VP9 is excluded — its fMP4/HLS carriage is poorly supported.
func fmp4MuxableVideo(codec string) bool {
	switch codec {
	case "h264", "avc", "avc1", "hevc", "h265", "hev1", "hvc1", "av1":
		return true
	}
	return false
}

// hlsCompatVideoCodec lists video codecs that Safari/hls.js can play inside
// an HLS container without transcoding.  10-bit HEVC is excluded because many
// clients fail to decode it.
func hlsCompatVideoCodec(codec, pixFmt string) bool {
	switch codec {
	case "h264", "avc", "avc1":
		return true
	case "hevc", "h265", "hev1", "hvc1":
		// 10-bit HEVC (yuv420p10le / yuv422p10le) is commonly broken on
		// browsers and many set-top boxes → force re-encode.
		return !strings.Contains(pixFmt, "10")
	case "av1":
		return true
	case "vp9":
		// VP9 in HLS is supported only by a subset of clients; be conservative.
		return false
	}
	return false
}

// hlsCompatAudioCodec lists audio codecs safe to stream-copy into HLS.
// EAC3/AC3 are playable on Safari/tvOS but many web players choke → we
// re-encode them to be safe (audio-only path is cheap).
func hlsCompatAudioCodec(codec string) bool {
	switch codec {
	case "aac", "mp4a":
		return true
	case "mp3":
		return true
	}
	return false
}

// clientCanPlayVideo checks whether the client's reported capabilities
// cover the source video codec+pixel format.
func clientCanPlayVideo(caps ClientCaps, codec, pixFmt string) bool {
	is10bit := strings.Contains(pixFmt, "10")
	switch codec {
	case "h264", "avc", "avc1":
		return caps.CanPlayH264
	case "hevc", "h265", "hev1", "hvc1":
		if is10bit {
			return caps.CanPlayHEVC10
		}
		return caps.CanPlayHEVC
	case "av1":
		return caps.CanPlayAV1
	case "vp9":
		return caps.CanPlayVP9
	}
	// Unknown / exotic codec (mpeg4, xvid, vp8, etc.) — assume no.
	return false
}

// clientCanPlayAudio checks audio codec against client capabilities.
func clientCanPlayAudio(caps ClientCaps, codec string) bool {
	switch codec {
	case "aac", "mp4a":
		return caps.CanPlayAAC
	case "ac3":
		return caps.CanPlayAC3
	case "eac3":
		return caps.CanPlayEAC3
	case "dts", "dca":
		return caps.CanPlayDTS
	case "truehd":
		return caps.CanPlayTrueHD
	case "flac":
		return caps.CanPlayFLAC
	case "opus":
		return caps.CanPlayOpus
	case "mp3":
		return caps.CanPlayMP3
	}
	return false
}

// normalizeLangCodes maps a short ISO 639-1 language code (e.g. "ru", "uk")
// into the set of ISO 639-2/3 codes commonly found in container metadata.
func NormalizeLangCodes(shortCode string) []string {
	switch strings.ToLower(strings.TrimSpace(shortCode)) {
	case "ru", "rus":
		return []string{"rus", "ru", "russian"}
	case "uk", "ua", "ukr":
		return []string{"ukr", "uk", "ukrainian"}
	case "en", "eng":
		return []string{"eng", "en", "english"}
	case "be", "bel":
		return []string{"bel", "be", "belarusian"}
	case "kk", "kaz":
		return []string{"kaz", "kk", "kazakh"}
	case "de", "deu", "ger":
		return []string{"deu", "ger", "de", "german"}
	case "fr", "fra", "fre":
		return []string{"fra", "fre", "fr", "french"}
	case "es", "spa":
		return []string{"spa", "es", "spanish"}
	case "it", "ita":
		return []string{"ita", "it", "italian"}
	case "ja", "jpn":
		return []string{"jpn", "ja", "japanese"}
	case "zh", "chi", "zho":
		return []string{"chi", "zho", "zh", "chinese"}
	case "pl", "pol":
		return []string{"pol", "pl", "polish"}
	case "tr", "tur":
		return []string{"tur", "tr", "turkish"}
	}
	return nil
}

// summarizeStreams converts the raw ffprobe streams array into a compact
// shape suitable for the /transcoding/start response.  Used by the plugin
// to populate the player's audio track menu without re-probing.
// hdrKind — какой это HDR, если это вообще он. Сигналы перечислены по УБЫВАНИЮ достоверности,
// потому что ни один из них не обязателен: контейнер может нести полный набор, а может — только
// матрицу цвета.
//
//	side_data «DOVI configuration record»          → Dolby Vision (профиль лежит там же)
//	side_data «HDR Dynamic Metadata SMPTE2094-40»  → HDR10+
//	color_transfer smpte2084                       → HDR10 (PQ)
//	color_transfer arib-std-b67                    → HLG
//	bt2020 + 10 бит                                → просто «HDR»: это тот случай, когда кривая
//	                                                 передачи в файле не проставлена, но широкий
//	                                                 охват с 10 битами SDR-ом не бывает
//
// Проверено на файле, собранном ffmpeg: у него доезжает только color_space=bt2020nc с
// yuv420p10le — то есть последняя ветка, и именно она чаще всего и спасает.
func hdrKind(sm map[string]any) string {
	if sd, ok := sm["side_data_list"].([]any); ok {
		for _, it := range sd {
			m, _ := it.(map[string]any)
			t, _ := m["side_data_type"].(string)
			switch {
			case strings.Contains(t, "DOVI"), strings.Contains(strings.ToLower(t), "dolby vision"):
				if p, ok := m["dv_profile"].(float64); ok {
					return fmt.Sprintf("Dolby Vision %d", int(p))
				}
				return "Dolby Vision"
			case strings.Contains(t, "2094-40"):
				return "HDR10+"
			}
		}
	}
	trc, _ := sm["color_transfer"].(string)
	switch trc {
	case "smpte2084":
		return "HDR10"
	case "arib-std-b67":
		return "HLG"
	}
	prim, _ := sm["color_primaries"].(string)
	space, _ := sm["color_space"].(string)
	pix, _ := sm["pix_fmt"].(string)
	wide := strings.HasPrefix(prim, "bt2020") || strings.HasPrefix(space, "bt2020")
	deep := strings.Contains(pix, "10le") || strings.Contains(pix, "10be") || strings.Contains(pix, "12le")
	if wide && deep {
		return "HDR"
	}
	return ""
}

// dispositionFlags — те флаги дорожки, которые НЕСУТ СМЫСЛ ДЛЯ ЗРИТЕЛЯ. В Matroska единственное
// человекочитаемое имя дорожки — элемент Name (он же tags.title), и заполняют его далеко не все
// муксеры: на 4K-ремуксах меню озвучек нередко вырождалось в список языков («Русская 1, Русская 2,
// Русская 3»). Флаги — второй и последний источник смысла внутри контейнера: dub = дубляж,
// original = язык оригинала, comment = комментарии режиссёра/актёров. Клиент подписывает ими
// безымянные дорожки, а default/forced использует, чтобы выбрать дорожку по умолчанию.
// Отдаём списком строк, а не картой: он короче в JSON и его удобно проверять на клиенте.
func dispositionFlags(sm map[string]any) []string {
	d, _ := sm["disposition"].(map[string]any)
	if d == nil {
		return nil
	}
	out := make([]string, 0, 3)
	for _, k := range []string{"dub", "original", "comment", "default", "forced", "hearing_impaired", "visual_impaired"} {
		if v, ok := d[k].(float64); ok && v != 0 {
			out = append(out, k)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// streamBitrate — bit_rate самой дорожки, а если ffprobe его не вывел (частый случай для MKV, где
// битрейт не хранится в заголовке), то из тегов статистики mkvmerge (BPS / BPS-eng). Нужен, чтобы
// одинаково названные дорожки различались хотя бы качеством: «Русская · 5.1 · 640 кбит/с».
func streamBitrate(sm map[string]any) int {
	if s, ok := sm["bit_rate"].(string); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n > 0 {
			return n
		}
	}
	if f, ok := sm["bit_rate"].(float64); ok && f > 0 {
		return int(f)
	}
	tags, _ := sm["tags"].(map[string]any)
	for k, v := range tags {
		if !strings.EqualFold(k, "BPS") && !strings.HasPrefix(strings.ToUpper(k), "BPS-") {
			continue
		}
		if s, ok := v.(string); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}

func SummarizeStreams(probe map[string]any) map[string]any {
	if probe == nil {
		return nil
	}
	streams, _ := probe["streams"].([]any)
	if len(streams) == 0 {
		return nil
	}

	var video map[string]any
	audios := make([]map[string]any, 0, 4)
	subs := make([]map[string]any, 0, 2)

	audioRel := 0
	for _, s := range streams {
		sm, _ := s.(map[string]any)
		if sm == nil {
			continue
		}
		ct := fmt.Sprint(sm["codec_type"])
		switch ct {
		case "video":
			if video == nil {
				entry := map[string]any{
					"codec":   sm["codec_name"],
					"pix_fmt": sm["pix_fmt"],
				}
				if br := streamBitrate(sm); br > 0 {
					entry["bit_rate"] = br
				}
				if h := hdrKind(sm); h != "" {
					entry["hdr"] = h
				}
				if w, ok := sm["width"].(float64); ok {
					entry["width"] = int(w)
				}
				if h, ok := sm["height"].(float64); ok {
					entry["height"] = int(h)
				}
				video = entry
			}
		case "audio":
			entry := map[string]any{
				"rel_index": audioRel,
				"codec":     sm["codec_name"],
			}
			if ch, ok := sm["channels"].(float64); ok {
				entry["channels"] = int(ch)
			}
			if br := streamBitrate(sm); br > 0 {
				entry["bit_rate"] = br
			}
			if f := dispositionFlags(sm); len(f) > 0 {
				entry["flags"] = f
			}
			// profile у аудио — это и есть признак объектного звука: декодер ffmpeg ставит сюда
			// «Dolby Digital Plus + Dolby Atmos» для E-AC-3 с JOC и «Dolby TrueHD + Dolby Atmos»
			// для TrueHD. По кодеку и числу каналов Atmos не отличить: тот же eac3 6 каналов
			// бывает и обычным.
			if pr, ok := sm["profile"].(string); ok && pr != "" && pr != "unknown" {
				entry["profile"] = pr
				if strings.Contains(strings.ToLower(pr), "atmos") {
					entry["atmos"] = true
				}
			}
			if tags, ok := sm["tags"].(map[string]any); ok {
				if l, ok := tags["language"].(string); ok {
					entry["lang"] = l
				}
				if t, ok := tags["title"].(string); ok {
					entry["title"] = t
				}
				// …а если декодер профиль не выставил (частый случай на «холодном» пробе, когда
				// кадр не декодировался), Atmos обычно назван в имени самой дорожки.
				if t, ok := tags["title"].(string); ok && strings.Contains(strings.ToLower(t), "atmos") {
					entry["atmos"] = true
				}
			}
			audios = append(audios, entry)
			audioRel++
		case "subtitle":
			entry := map[string]any{
				"codec": sm["codec_name"],
			}
			if f := dispositionFlags(sm); len(f) > 0 {
				entry["flags"] = f
			}
			if tags, ok := sm["tags"].(map[string]any); ok {
				if l, ok := tags["language"].(string); ok {
					entry["lang"] = l
				}
				if t, ok := tags["title"].(string); ok {
					entry["title"] = t
				}
			}
			subs = append(subs, entry)
		}
	}

	out := map[string]any{}
	if video != nil {
		out["video"] = video
	}
	if len(audios) > 0 {
		out["audio"] = audios
	}
	if len(subs) > 0 {
		out["subtitles"] = subs
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
