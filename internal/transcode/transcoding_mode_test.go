package transcode

import "testing"

// Потолок разрешения клиента (caps=maxh<N>): видео выше него нельзя отдавать stream-copy —
// панель выше своего режима вывода не покажет, а MStar-класс на 4K-directplay рисует чёрный
// экран без единой ошибки. Ниже/на потолке copy-пути обязаны сохраняться (дёшево).
func TestSelectModeMaxHeightGate(t *testing.T) {
	video := map[string]any{
		"codec_type": "video", "codec_name": "h264", "pix_fmt": "yuv420p",
		"height": float64(2072),
	}
	probe := map[string]any{
		"streams": []any{
			video,
			map[string]any{"codec_type": "audio", "codec_name": "aac", "channels": float64(2)},
		},
	}
	caps := ClientCaps{CanPlayMKV: true, CanPlayH264: true, CanPlayAAC: true}

	// Без потолка: h264+aac при preferNative — нетронутый native-путь.
	if d := SelectMode(probe, caps, true); d.Mode != ModeNative {
		t.Fatalf("no cap: want native, got %v (%s)", d.Mode, d.Reason)
	}

	// Потолок 1080, источник 2072: видео обязано пройти re-encode (даунскейл).
	caps.MaxHeight = 1080
	if d := SelectMode(probe, caps, true); d.VideoIsCopy {
		t.Fatalf("maxh1080 over 4K source: video must be re-encoded, got %v (%s)", d.Mode, d.Reason)
	}

	// Источник ровно на потолке — copy-путь сохраняется.
	video["height"] = float64(1080)
	if d := SelectMode(probe, caps, true); d.Mode != ModeNative {
		t.Fatalf("1080p at maxh1080: want native, got %v (%s)", d.Mode, d.Reason)
	}
}
