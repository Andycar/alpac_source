package transcodesvc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
)

// Превью перемотки по карте ключевых кадров (keyframes_mkv.go).
//
// Старый способ — на каждый кадр отдельный `ffmpeg -ss T -i url`: ffmpeg открывает источник,
// пробует его (probesize/analyzeduration с начала файла), читает Cues с хвоста и только потом
// прыгает к кластеру — на сотню кадров это сотня проб и сотни мегабайт «лишнего» трафика, а на
// pipe:0 (торрент в процессе) невозможно вовсе. С картой всё известно заранее: байты головы
// файла (EBML/Segment/Tracks до первого кластера) читаем один раз, а на каждый кадр берём один
// Range-запрос кластера с нужным ключевым кадром и кормим декодеру «голова + кластер» через
// stdin — валидный Matroska-поток для ffmpeg, ни проб, ни перемоток. Приём тот же, что у
// Jellyfin (keyframe-only extraction), но без чтения файла целиком.
const (
	trickplayHeaderCap  = 8 << 20  // голова больше — это вложения (шрифты); тогда старый путь
	trickplayClusterCap = 4 << 20  // читаем от начала кластера столько — первый блок и есть ключевой
	trickplayBlockWin   = 2 << 20  // если известна позиция блока внутри кластера — столько после неё
	trickplayHardCap    = 24 << 20 // абсолютный потолок одного чтения (BDRemux-кластеры бывают 20+ МБ)
)

type cueGrabber struct {
	km     *keyframeMap
	ra     io.ReaderAt
	closer io.Closer
	header []byte
	bytes  int64 // прочитано всего — для лога
}

// newCueGrabber готовит захват по карте или возвращает nil, если карты/смещений нет.
func newCueGrabber(job *TranscodingJob) *cueGrabber {
	km := job.keyframes()
	if km == nil || km.Source != "mkv-cues" || km.HeaderEnd <= 0 || km.HeaderEnd > trickplayHeaderCap || len(km.Offsets) == 0 {
		return nil
	}
	ctx := job.Context
	var ra io.ReaderAt
	var closer io.Closer
	switch {
	case ctx.torrentHash != "" && torrsIsInProcess():
		srv := getTorrsServer()
		if srv == nil {
			return nil
		}
		rs, _, _, err := srv.Stream(ctx.torrentHash, ctx.torrentFileIdx)
		if err != nil {
			return nil
		}
		ra = &seekerReaderAt{rs: rs}
		if c, ok := rs.(io.Closer); ok {
			closer = c
		}
	case strings.HasPrefix(ctx.Source, "http://") || strings.HasPrefix(ctx.Source, "https://"):
		ra = newHTTPRangeReader(ctx.Source, ctx.UserAgent, ctx.Referer)
	case ctx.Source == "pipe:0" || ctx.Source == "":
		return nil
	default:
		f, err := os.Open(ctx.Source)
		if err != nil {
			return nil
		}
		ra, closer = f, f
	}
	header := make([]byte, km.HeaderEnd)
	if n, err := ra.ReadAt(header, 0); err != nil && int64(n) < km.HeaderEnd {
		if closer != nil {
			closer.Close()
		}
		return nil
	}
	return &cueGrabber{km: km, ra: ra, closer: closer, header: header, bytes: km.HeaderEnd}
}

func (g *cueGrabber) Close() {
	if g != nil && g.closer != nil {
		g.closer.Close()
	}
}

// nearest — индекс ключевого кадра, ближайшего к секунде ts.
func (g *cueGrabber) nearest(ts float64) int {
	t := g.km.Times
	i := sort.SearchFloat64s(t, ts)
	if i >= len(t) {
		return len(t) - 1
	}
	if i > 0 && ts-t[i-1] < t[i]-ts {
		return i - 1
	}
	return i
}

// clusterBytes читает кластер с ключевым кадром i: заголовок элемента даёт размер, дальше —
// не больше, чем нужно первому (ключевому) блоку.
func (g *cueGrabber) clusterBytes(i int) ([]byte, error) {
	off := g.km.Offsets[i]
	if off <= 0 {
		return nil, errors.New("no cluster offset")
	}
	head := make([]byte, 16)
	if n, err := g.ra.ReadAt(head, off); err != nil && n < 12 {
		return nil, err
	}
	el, err := (&ebmlReader{r: bytesReaderAt(head), size: int64(len(head))}).element(0)
	if err != nil || el.id != ebmlIDCluster {
		return nil, fmt.Errorf("offset %d is not a Cluster", off)
	}
	want := int64(trickplayClusterCap)
	if el.size > 0 && int64(el.headSize)+el.size < want {
		want = int64(el.headSize) + el.size
	}
	if rel := g.km.RelPos[i]; rel > 0 && rel+trickplayBlockWin > want {
		want = rel + trickplayBlockWin
		if el.size > 0 && int64(el.headSize)+el.size < want {
			want = int64(el.headSize) + el.size
		}
	}
	if want > trickplayHardCap {
		want = trickplayHardCap
	}
	buf := make([]byte, want)
	n, err := g.ra.ReadAt(buf, off)
	if n == 0 {
		return nil, err
	}
	g.bytes += int64(n)
	return buf[:n], nil
}

// grab декодирует один ключевой кадр около секунды ts в JPEG [out].
func (g *cueGrabber) grab(ctx context.Context, ffmpegBin string, ts float64, out string) error {
	i := g.nearest(ts)
	cluster, err := g.clusterBytes(i)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, ffmpegBin,
		"-y", "-hide_banner", "-loglevel", "error",
		"-f", "matroska", "-skip_frame", "nokey", "-an", "-sn",
		"-i", "pipe:0",
		"-frames:v", "1",
		"-vf", fmt.Sprintf("scale=%d:-2", trickplayThumbWidth),
		"-qscale:v", "5", out)
	cmd.Stdin = io.MultiReader(bytes.NewReader(g.header), bytes.NewReader(cluster))
	return cmd.Run()
}
