// Package introdetect находит заставки и титры сериала БЕЗ внешней базы — по звуку.
//
// Приём плагина Intro Skipper для Jellyfin: у эпизодов одного сезона заставка — один и тот же
// звук. Снимаем chromaprint-отпечатки первых минут двух-трёх серий (ffmpeg -f chromaprint,
// он же алгоритм AcoustID/Shazam), находим самый длинный общий фрагмент с постоянным сдвигом
// — это и есть intro; для титров — то же по хвосту файла. Работает для любого языка и любого
// релиза, потому что сравнивает сами файлы, а не таймкоды из чужой базы: у web-rip без
// «предыдущей серии» и у BDRemux с логотипом дистрибьютора заставка сдвинута на разное время,
// и только звук знает, где она в ЭТОМ файле.
package introdetect

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// fpSecondsPerItem — период одного под-отпечатка chromaprint: шаг 1365 сэмплов на 11025 Гц.
// Проверено на ffmpeg main: тон, начатый на 5.0 с, меняет отпечаток на индексе ≈ 5/0.1238.
const fpSecondsPerItem = 1365.0 / 11025.0

// Fingerprint — последовательность 32-битных под-отпечатков chromaprint (algorithm 1 muxer'а
// ffmpeg; важно лишь, чтобы обе стороны считались одинаково).
type Fingerprint []uint32

// fingerprintCmd собирает команду: аудио без видео, моно 11025 Гц — то, чего ждёт chromaprint.
// [offset] < 0 — от конца файла (-sseof), для титров.
func fingerprintCmd(ctx context.Context, ffmpeg, src, ua, referer string, offset, dur float64) *exec.Cmd {
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error"}
	if strings.HasPrefix(src, "http") {
		if ua != "" {
			args = append(args, "-user_agent", ua)
		}
		if referer != "" {
			args = append(args, "-headers", "Referer: "+referer+"\r\n")
		}
		// Заголовки уже известны — не читать 5 МБ на пробу.
		args = append(args, "-probesize", "2000000", "-analyzeduration", "2000000")
	}
	if offset < 0 {
		args = append(args, "-sseof", strconv.FormatFloat(offset, 'f', 1, 64))
	} else if offset > 0 {
		args = append(args, "-ss", strconv.FormatFloat(offset, 'f', 1, 64))
	}
	args = append(args, "-t", strconv.FormatFloat(dur, 'f', 1, 64), "-i", src,
		"-vn", "-sn", "-dn", "-map", "0:a:0", "-ac", "1", "-ar", "11025", "-c:a", "pcm_s16le",
		"-f", "chromaprint", "-fp_format", "raw", "pipe:1")
	return exec.CommandContext(ctx, ffmpeg, args...)
}

// Extract снимает отпечаток окна [offset, offset+dur) источника.
func Extract(ctx context.Context, ffmpeg, src, ua, referer string, offset, dur float64) (Fingerprint, error) {
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	cmd := fingerprintCmd(ctx, ffmpeg, src, ua, referer, offset, dur)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if len(msg) > 200 {
			msg = msg[len(msg)-200:]
		}
		return nil, fmt.Errorf("chromaprint: %w: %s", err, msg)
	}
	raw := out.Bytes()
	if len(raw) < 4*80 { // меньше 10 с — не с чем сравнивать
		return nil, errors.New("chromaprint: fingerprint too short")
	}
	fp := make(Fingerprint, len(raw)/4)
	for i := range fp {
		fp[i] = binary.LittleEndian.Uint32(raw[i*4:])
	}
	return fp, nil
}

// HasChromaprint — умеет ли этот ffmpeg muxer chromaprint (jellyfin-ffmpeg и BtbN-сборки — да,
// дистрибутивный Debian — тоже; отсутствие = детектор молча выключен).
func HasChromaprint(ffmpeg string) bool {
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-formats").Output()
	return err == nil && bytes.Contains(out, []byte("chromaprint"))
}
