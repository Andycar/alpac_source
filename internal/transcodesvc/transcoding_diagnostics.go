package transcodesvc

import (
	"strings"
)

// ---------------------------------------------------------------------------
// User-friendly diagnostics — P3.G.
//
// Lampa shows whatever string the server returns under `error` as part of
// its "видео не найдено или повреждено" overlay.  Users on 4PDA / Telegram
// then have nothing to act on — every failure looks the same regardless of
// cause: bad codec, blocked CDN, full disk, missing ffmpeg.  This module
// enriches the error reply with three extra fields:
//
//   errorCode    — stable machine-readable identifier ("scheduler_busy",
//                   "ffmpeg_not_found", "source_blocked", …) so the plugin
//                   can match-and-react without parsing free-form text.
//   userMessage  — short Russian explanation suitable for the player overlay.
//   suggestions  — array of next-step ideas the user can try ("сменить
//                   балансер", "снизить качество до 720p") — this is
//                   exactly the "народные обходные пути" list that the
//                   research pass surfaced.
//
// The classifier is best-effort regex/substring matching; when nothing
// matches we fall through to a generic "transcoding error" code rather
// than guessing.
// ---------------------------------------------------------------------------

// DiagnosticReport is the user-facing enrichment of an error message.
type DiagnosticReport struct {
	Code        string   `json:"errorCode"`
	UserMessage string   `json:"userMessage"`
	Suggestions []string `json:"suggestions,omitempty"`
}

// diagnoseStartError pattern-matches the raw error string returned from
// Start() and returns a user-friendly report.  When `src` is non-empty
// it's used to refine the diagnostic (e.g. distinguish 403 on torrent vs
// 403 on CDN).
func diagnoseStartError(errMsg, src string) DiagnosticReport {
	low := strings.ToLower(errMsg)

	// ---- Operator / configuration errors -------------------------------

	if strings.Contains(low, "transcoding disabled") {
		return DiagnosticReport{
			Code:        "transcoding_disabled",
			UserMessage: "Транскодинг выключен на сервере.",
			Suggestions: []string{
				"Включите транскодинг в админ-панели → Транскодинг → Enabled.",
				"Если на VPS, проверьте лимит CPU и max_concurrent_jobs.",
			},
		}
	}
	if strings.Contains(low, "host is not allowed") {
		return DiagnosticReport{
			Code:        "host_not_allowed",
			UserMessage: "Источник не разрешён политикой сервера.",
			Suggestions: []string{
				"Добавьте хост в transcoding.allow_hosts либо очистите список.",
			},
		}
	}
	if strings.Contains(low, "only http/https") {
		return DiagnosticReport{
			Code:        "invalid_scheme",
			UserMessage: "Поддерживаются только HTTP/HTTPS источники.",
			Suggestions: []string{"Проверьте URL в плагине балансера."},
		}
	}

	// ---- Scheduler / resource pressure --------------------------------

	if strings.Contains(low, "all transcoding slots are busy") || strings.Contains(low, "scheduler busy") {
		return DiagnosticReport{
			Code:        "scheduler_busy",
			UserMessage: "Все слоты транскодинга заняты, попробуйте через несколько секунд.",
			Suggestions: []string{
				"Подождите 5–10 секунд и нажмите Play повторно.",
				"Админу: увеличьте transcoding.max_concurrent_jobs (если позволяет CPU).",
			},
		}
	}
	if strings.Contains(low, "failed to create output directory") {
		return DiagnosticReport{
			Code:        "disk_unavailable",
			UserMessage: "Не удалось создать рабочую папку для транскодинга.",
			Suggestions: []string{
				"Проверьте свободное место на диске и права на cache/transcoding.",
				"Уменьшите transcoding.disk_budget_mb или переместите temp_root на свободный том.",
			},
		}
	}

	// ---- ffmpeg process startup ---------------------------------------

	if strings.Contains(low, "failed to start ffmpeg") || strings.Contains(low, "executable file not found") {
		return DiagnosticReport{
			Code:        "ffmpeg_not_found",
			UserMessage: "ffmpeg не найден или не запускается.",
			Suggestions: []string{
				"Установите ffmpeg 5.0+ (apt install ffmpeg / brew install ffmpeg).",
				"Если ffmpeg в нестандартном пути — задайте transcoding.ffmpeg в config.toml.",
				"Проверьте права на исполнение бинарника.",
			},
		}
	}
	if strings.Contains(low, "failed to create stderr pipe") {
		return DiagnosticReport{
			Code:        "os_pipe_failure",
			UserMessage: "Не удалось создать pipe для ffmpeg.",
			Suggestions: []string{
				"Проверьте свободные файловые дескрипторы (ulimit -n).",
				"Перезапустите lampac-go.",
			},
		}
	}

	// ---- Source / network errors ---------------------------------------

	if strings.Contains(low, "403") || strings.Contains(low, "forbidden") {
		return DiagnosticReport{
			Code:        "source_blocked",
			UserMessage: "Источник заблокирован (403 Forbidden) — обычно гео-блок или анти-бот.",
			Suggestions: []string{
				"Смените балансер (Источник → другой).",
				"Включите SOCKS5/VLESS прокси для этого балансера.",
				"Подождите 1–2 минуты — может быть временная блокировка.",
			},
		}
	}
	if strings.Contains(low, "404") || strings.Contains(low, "not found") {
		return DiagnosticReport{
			Code:        "source_missing",
			UserMessage: "Источник не найден (404).",
			Suggestions: []string{
				"Смените балансер — конкретно на этом фильм мог быть удалён.",
				"Проверьте, что выбранное качество ещё доступно.",
			},
		}
	}
	if strings.Contains(low, "timeout") || strings.Contains(low, "timed out") {
		return DiagnosticReport{
			Code:        "source_timeout",
			UserMessage: "Источник не отвечает (таймаут).",
			Suggestions: []string{
				"Проверьте интернет-соединение сервера.",
				"Смените балансер — текущий CDN может быть перегружен.",
			},
		}
	}
	if strings.Contains(low, "connection reset") || strings.Contains(low, "connection refused") {
		return DiagnosticReport{
			Code:        "source_unreachable",
			UserMessage: "Источник недоступен (соединение сброшено).",
			Suggestions: []string{
				"Смените балансер.",
				"Если используется VLESS/SOCKS5 прокси — проверьте, что сайдкар запущен.",
			},
		}
	}

	// ---- Probe-time issues --------------------------------------------

	if strings.Contains(low, "probe failed") || strings.Contains(low, "could not find codec") || strings.Contains(low, "invalid data found") {
		return DiagnosticReport{
			Code:        "probe_failed",
			UserMessage: "Не удалось разобрать поток (повреждён или нестандартный кодек).",
			Suggestions: []string{
				"Смените балансер.",
				"Снизьте качество до 1080p или 720p.",
				"Проверьте, что в админке включено transcoding.disk_budget_mb (для best-effort режима).",
			},
		}
	}

	// Fallback — generic transcoding error.  We still return a code so the
	// plugin can branch on "any-failure" without parsing message text.
	return DiagnosticReport{
		Code:        "transcoding_error",
		UserMessage: "Ошибка транскодинга. Подробности в логах сервера.",
		Suggestions: []string{
			"Смените балансер.",
			"Если устройство — Samsung Tizen или старый LG, попробуйте снизить качество.",
			"Откройте админ-панель → Транскодинг → Логи для подробностей.",
		},
	}
}

// diagnoseFFmpegExit classifies a non-zero ffmpeg exit code (or the captured
// stderr context that came with it) into the same DiagnosticReport shape
// the start handler returns.  Used by the playlist endpoint when ffmpeg
// died after Start() succeeded — surfaces the cause to the player overlay
// instead of the empty "видео повреждено" we return today.
func diagnoseFFmpegExit(exitCode int, stderrLines []string) DiagnosticReport {
	class := classifyFFmpegStderr(stderrLines)
	switch class {
	case ErrHWUnavailable:
		return DiagnosticReport{
			Code:        "ffmpeg_hw_failed",
			UserMessage: "Сбой аппаратного ускорителя — попробуйте снова, сервер автоматически переключится на CPU.",
			Suggestions: []string{
				"Подождите 5 секунд и нажмите Play повторно.",
				"Админу: проверьте /dev/dri права и нагрузку GPU.",
			},
		}
	case ErrColorSpace:
		return DiagnosticReport{
			Code:        "ffmpeg_color_failed",
			UserMessage: "Ошибка преобразования HDR/10-bit. Сервер автоматически переключится на программный режим.",
			Suggestions: []string{
				"Подождите 5 секунд и нажмите Play повторно.",
				"Если проблема повторяется, снизьте качество до 1080p (без HDR).",
			},
		}
	case ErrCodecRefused:
		return DiagnosticReport{
			Code:        "ffmpeg_codec_refused",
			UserMessage: "Кодек источника несовместим с HLS — сервер сейчас перекодирует.",
			Suggestions: []string{
				"Подождите 5–10 секунд — пересобирается пайплайн.",
			},
		}
	case ErrOutputWrite:
		// NOT a codec problem: ffmpeg couldn't create its segment/init file
		// (working dir gone, disk full, or read-only mount). Misreporting this
		// as "перекодирует" sent users into an endless retry loop.
		return DiagnosticReport{
			Code:        "ffmpeg_output_unwritable",
			UserMessage: "Серверу не удалось записать видео на диск (нет места или рабочая папка недоступна).",
			Suggestions: []string{
				"Админу: проверьте свободное место и inodes (df -h / df -i) на томе transcoding.temp_root.",
				"Админу: убедитесь, что cache/transcoding не чистится внешним кроном/systemd-tmpfiles, и поднимите transcoding.disk_budget_mb.",
				"Подождите 5–10 секунд и нажмите Play повторно.",
			},
		}
	case ErrIO:
		return DiagnosticReport{
			Code:        "ffmpeg_io_error",
			UserMessage: "Сбой связи с источником посередине воспроизведения.",
			Suggestions: []string{
				"Сервер пытается возобновить с последнего сегмента.",
				"Если не помогает — смените балансер.",
			},
		}
	}
	return DiagnosticReport{
		Code:        "ffmpeg_unknown_exit",
		UserMessage: "ffmpeg завершился с ошибкой (код " + intToStr(exitCode) + ").",
		Suggestions: []string{
			"Откройте админ-панель → Транскодинг → Логи для подробностей.",
			"Смените балансер или снизьте качество.",
		},
	}
}

// intToStr is a tiny strconv.Itoa wrapper so the diagnostics module doesn't
// pull strconv just for one call.  Keeps imports tight.
func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	negative := n < 0
	if negative {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
