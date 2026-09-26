package transcodesvc

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// Карта ключевых кадров из Matroska — читаем ТОЛЬКО служебные элементы (EBML-заголовок,
// SeekHead, Info, Tracks, Cues), а не файл: у 20-гигабайтного BDRemux это несколько сотен
// килобайт в начале и в хвосте. Приём Jellyfin (Jellyfin.MediaEncoding.Keyframes/Matroska):
// Cues хранят время и байтовое смещение кластера каждого ключевого кадра — ровно то, по чему
// ffmpeg и сам перематывает `-ss`. Зная их заранее, мы (а) объявляем в плейлисте честные
// длительности сегментов, а не «6.0 везде», (б) перематываем ровно в ключевой кадр, а не
// «куда-то до него» (сегмент начинался раньше объявленного и накладывался на предыдущий —
// отсюда щели и рывки на стыках), (в) для превью перемотки читаем один кластер Range-запросом
// вместо ffmpeg-пробы всего файла на каждый кадр.

// EBML element IDs (со «своим» первым байтом, как в спецификации).
const (
	ebmlIDHeader               = 0x1A45DFA3
	ebmlIDSegment              = 0x18538067
	ebmlIDSeekHead             = 0x114D9B74
	ebmlIDSeek                 = 0x4DBB
	ebmlIDSeekID               = 0x53AB
	ebmlIDSeekPosition         = 0x53AC
	ebmlIDInfo                 = 0x1549A966
	ebmlIDTimestampScale       = 0x2AD7B1
	ebmlIDDuration             = 0x4489
	ebmlIDTracks               = 0x1654AE6B
	ebmlIDTrackEntry           = 0xAE
	ebmlIDTrackNumber          = 0xD7
	ebmlIDTrackType            = 0x83
	ebmlIDCues                 = 0x1C53BB6B
	ebmlIDCuePoint             = 0xBB
	ebmlIDCueTime              = 0xB3
	ebmlIDCueTrackPos          = 0xB7
	ebmlIDCueTrack             = 0xF7
	ebmlIDCueClusterPos        = 0xF1
	ebmlIDCueRelativePos       = 0xF0
	ebmlIDCluster              = 0x1F43B675
	ebmlIDVoid                 = 0xEC
	ebmlIDCRC32                = 0xBF
	ebmlUnknownSize      int64 = -1
	mkvTrackTypeVideo          = 1
	// Cues у длинного фильма — сотни тысяч точек; больше 16 МБ не бывает даже у BDRemux.
	mkvMaxCuesBytes = 16 << 20
	// Служебные элементы в голове файла: заголовок, SeekHead, Info, Tracks (+ вложения
	// шрифтов у аниме-релизов могут раздуть голову — тогда просто дочитываем).
	mkvMaxHeadBytes = 8 << 20
)

// ebmlReader читает элементы по смещениям через ReaderAt: ни одного лишнего байта с сети.
type ebmlReader struct {
	r    io.ReaderAt
	size int64 // -1 = неизвестен (пайп/поток)
}

func (e *ebmlReader) readAt(off int64, n int) ([]byte, error) {
	if n <= 0 {
		return nil, nil
	}
	buf := make([]byte, n)
	got, err := e.r.ReadAt(buf, off)
	if got < n {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return buf[:got], err
	}
	return buf, nil
}

// vint читает EBML-число переменной длины по смещению. keepMarker=true — для ID (маркер
// длины остаётся частью значения), false — для размеров (маркер убирается).
func (e *ebmlReader) vint(off int64, keepMarker bool) (val int64, n int, err error) {
	b, err := e.readAt(off, 1)
	if err != nil {
		return 0, 0, err
	}
	first := b[0]
	if first == 0 {
		return 0, 0, errors.New("ebml: bad vint")
	}
	n = 1
	mask := byte(0x80)
	for first&mask == 0 {
		mask >>= 1
		n++
	}
	if n > 8 {
		return 0, 0, errors.New("ebml: vint too long")
	}
	rest, err := e.readAt(off, n)
	if err != nil {
		return 0, 0, err
	}
	if keepMarker {
		for _, c := range rest {
			val = val<<8 | int64(c)
		}
		return val, n, nil
	}
	val = int64(first & (mask - 1))
	allOnes := val == int64(mask-1)
	for _, c := range rest[1:] {
		val = val<<8 | int64(c)
		if c != 0xFF {
			allOnes = false
		}
	}
	if allOnes {
		return ebmlUnknownSize, n, nil
	}
	return val, n, nil
}

// element — заголовок EBML-элемента: id, размер данных, смещение данных.
type ebmlElement struct {
	id       int64
	size     int64 // ebmlUnknownSize = до конца родителя
	dataOff  int64
	headSize int
}

func (e *ebmlReader) element(off int64) (ebmlElement, error) {
	id, n1, err := e.vint(off, true)
	if err != nil {
		return ebmlElement{}, err
	}
	size, n2, err := e.vint(off+int64(n1), false)
	if err != nil {
		return ebmlElement{}, err
	}
	return ebmlElement{id: id, size: size, dataOff: off + int64(n1+n2), headSize: n1 + n2}, nil
}

func (e *ebmlReader) uint(el ebmlElement) (uint64, error) {
	if el.size < 0 || el.size > 8 {
		return 0, fmt.Errorf("ebml: bad uint size %d", el.size)
	}
	b, err := e.readAt(el.dataOff, int(el.size))
	if err != nil {
		return 0, err
	}
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v, nil
}

func (e *ebmlReader) float(el ebmlElement) (float64, error) {
	b, err := e.readAt(el.dataOff, int(el.size))
	if err != nil {
		return 0, err
	}
	switch el.size {
	case 4:
		return float64(math.Float32frombits(binary.BigEndian.Uint32(b))), nil
	case 8:
		return math.Float64frombits(binary.BigEndian.Uint64(b)), nil
	case 0:
		return 0, nil
	}
	return 0, fmt.Errorf("ebml: bad float size %d", el.size)
}

// mkvKeyframes читает карту ключевых кадров видео из Matroska/WebM.
// Стратегия: голова файла последовательно (EBML → Segment → SeekHead/Info/Tracks и, если
// muxer положил их вперёд, Cues) до первого Cluster; если Cues в голове нет — по SeekHead
// прыгаем на их позицию (обычно хвост файла). Возвращает времена в секундах (по
// TimestampScale), смещения кластеров относительно начала файла и байты головы (для
// превью: голова + один кластер = валидный поток для декодера).
func mkvKeyframes(r io.ReaderAt, size int64) (*keyframeMap, error) {
	e := &ebmlReader{r: r, size: size}
	hdr, err := e.element(0)
	if err != nil {
		return nil, fmt.Errorf("mkv: header: %w", err)
	}
	if hdr.id != ebmlIDHeader {
		return nil, errors.New("mkv: not an EBML file")
	}
	segOff := hdr.dataOff + hdr.size
	seg, err := e.element(segOff)
	if err != nil {
		return nil, fmt.Errorf("mkv: segment: %w", err)
	}
	if seg.id != ebmlIDSegment {
		return nil, errors.New("mkv: no Segment")
	}
	segData := seg.dataOff
	segEnd := int64(math.MaxInt64)
	if seg.size >= 0 {
		segEnd = segData + seg.size
	}
	if size > 0 && segEnd > size {
		segEnd = size
	}

	km := &keyframeMap{Source: "mkv-cues", TimestampScale: 1_000_000}
	var cuesPos int64 = -1 // абсолютное смещение элемента Cues (из SeekHead)
	videoTrack := uint64(0)
	firstCluster := int64(-1)
	seenCues := false

	off := segData
	for off < segEnd {
		el, err := e.element(off)
		if err != nil {
			if firstCluster >= 0 || seenCues {
				break
			}
			return nil, fmt.Errorf("mkv: top-level at %d: %w", off, err)
		}
		next := el.dataOff + el.size
		if el.size == ebmlUnknownSize {
			next = segEnd
		}
		switch el.id {
		case ebmlIDSeekHead:
			e.parseSeekHead(el, segData, &cuesPos)
		case ebmlIDInfo:
			e.parseInfo(el, km)
		case ebmlIDTracks:
			videoTrack = e.firstVideoTrack(el)
		case ebmlIDCues:
			if err := e.parseCues(el, videoTrack, segData, km); err != nil {
				return nil, err
			}
			seenCues = true
		case ebmlIDCluster:
			firstCluster = off
		}
		if firstCluster >= 0 {
			break // дальше только данные — их не читаем
		}
		if off-segData > mkvMaxHeadBytes && !seenCues {
			break // голова раздута вложениями — Cues возьмём по SeekHead
		}
		off = next
	}
	if !seenCues {
		if cuesPos < 0 {
			return nil, errors.New("mkv: no Cues (SeekHead has no entry; file may be unfinished)")
		}
		el, err := e.element(cuesPos)
		if err != nil || el.id != ebmlIDCues {
			return nil, fmt.Errorf("mkv: Cues at %d unreadable", cuesPos)
		}
		if el.size > mkvMaxCuesBytes {
			return nil, fmt.Errorf("mkv: Cues too big (%d)", el.size)
		}
		if err := e.parseCues(el, videoTrack, segData, km); err != nil {
			return nil, err
		}
	}
	if len(km.Times) == 0 {
		return nil, errors.New("mkv: Cues carry no video cue points")
	}
	if firstCluster > 0 {
		km.HeaderEnd = firstCluster
	} else if len(km.Offsets) > 0 && km.Offsets[0] > 0 {
		km.HeaderEnd = km.Offsets[0]
	}
	return km, nil
}

func (e *ebmlReader) parseSeekHead(el ebmlElement, segData int64, cuesPos *int64) {
	end := el.dataOff + el.size
	for off := el.dataOff; off < end; {
		child, err := e.element(off)
		if err != nil || child.size < 0 {
			return
		}
		if child.id == ebmlIDSeek {
			var id uint64
			var pos int64 = -1
			cend := child.dataOff + child.size
			for coff := child.dataOff; coff < cend; {
				c, err := e.element(coff)
				if err != nil || c.size < 0 {
					break
				}
				switch c.id {
				case ebmlIDSeekID:
					id, _ = e.uint(c)
				case ebmlIDSeekPosition:
					v, _ := e.uint(c)
					pos = int64(v)
				}
				coff = c.dataOff + c.size
			}
			if id == ebmlIDCues && pos >= 0 {
				*cuesPos = segData + pos
			}
		}
		off = child.dataOff + child.size
	}
}

func (e *ebmlReader) parseInfo(el ebmlElement, km *keyframeMap) {
	end := el.dataOff + el.size
	var dur float64
	for off := el.dataOff; off < end; {
		child, err := e.element(off)
		if err != nil || child.size < 0 {
			return
		}
		switch child.id {
		case ebmlIDTimestampScale:
			if v, err := e.uint(child); err == nil && v > 0 {
				km.TimestampScale = int64(v)
			}
		case ebmlIDDuration:
			dur, _ = e.float(child)
		}
		off = child.dataOff + child.size
	}
	if dur > 0 {
		km.Duration = dur * float64(km.TimestampScale) / 1e9
	}
}

func (e *ebmlReader) firstVideoTrack(el ebmlElement) uint64 {
	end := el.dataOff + el.size
	for off := el.dataOff; off < end; {
		child, err := e.element(off)
		if err != nil || child.size < 0 {
			return 0
		}
		if child.id == ebmlIDTrackEntry {
			var num, typ uint64
			cend := child.dataOff + child.size
			for coff := child.dataOff; coff < cend; {
				c, err := e.element(coff)
				if err != nil || c.size < 0 {
					break
				}
				switch c.id {
				case ebmlIDTrackNumber:
					num, _ = e.uint(c)
				case ebmlIDTrackType:
					typ, _ = e.uint(c)
				}
				coff = c.dataOff + c.size
			}
			if typ == mkvTrackTypeVideo && num > 0 {
				return num
			}
		}
		off = child.dataOff + child.size
	}
	return 0
}

// parseCues читает элемент Cues целиком (одним ReadAt) и вынимает точки видео-дорожки.
func (e *ebmlReader) parseCues(el ebmlElement, videoTrack uint64, segData int64, km *keyframeMap) error {
	if el.size < 0 || el.size > mkvMaxCuesBytes {
		return fmt.Errorf("mkv: bad Cues size %d", el.size)
	}
	buf, err := e.readAt(el.dataOff, int(el.size))
	if err != nil {
		return fmt.Errorf("mkv: read Cues: %w", err)
	}
	mem := &ebmlReader{r: bytesReaderAt(buf), size: int64(len(buf))}
	end := int64(len(buf))
	scale := float64(km.TimestampScale)
	for off := int64(0); off < end; {
		cp, err := mem.element(off)
		if err != nil || cp.size < 0 {
			break
		}
		if cp.id == ebmlIDCuePoint {
			var t uint64
			var haveT bool
			var clusterPos, relPos int64 = -1, -1
			track := uint64(0)
			cend := cp.dataOff + cp.size
			for coff := cp.dataOff; coff < cend; {
				c, err := mem.element(coff)
				if err != nil || c.size < 0 {
					break
				}
				switch c.id {
				case ebmlIDCueTime:
					t, _ = mem.uint(c)
					haveT = true
				case ebmlIDCueTrackPos:
					pend := c.dataOff + c.size
					for poff := c.dataOff; poff < pend; {
						p, err := mem.element(poff)
						if err != nil || p.size < 0 {
							break
						}
						switch p.id {
						case ebmlIDCueTrack:
							track, _ = mem.uint(p)
						case ebmlIDCueClusterPos:
							v, _ := mem.uint(p)
							clusterPos = int64(v)
						case ebmlIDCueRelativePos:
							v, _ := mem.uint(p)
							relPos = int64(v)
						}
						poff = p.dataOff + p.size
					}
				}
				coff = c.dataOff + c.size
			}
			// Без Tracks (обрезанная голова) берём точки любой дорожки — у видео их всё равно
			// больше всего, а дубли по времени схлопываем ниже.
			if haveT && (videoTrack == 0 || track == videoTrack) {
				sec := float64(t) * scale / 1e9
				if n := len(km.Times); n == 0 || sec > km.Times[n-1] {
					km.Times = append(km.Times, sec)
					abs := int64(0)
					if clusterPos >= 0 {
						abs = segData + clusterPos
					}
					km.Offsets = append(km.Offsets, abs)
					km.RelPos = append(km.RelPos, relPos)
				}
			}
		}
		off = cp.dataOff + cp.size
	}
	return nil
}

// bytesReaderAt — ReaderAt поверх среза (Cues читаем в память одним куском).
type bytesReaderAt []byte

func (b bytesReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(len(b)) {
		return 0, io.EOF
	}
	n := copy(p, b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}
