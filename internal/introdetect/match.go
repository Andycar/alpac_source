package introdetect

import (
	"math"
	"math/bits"
)

// Параметры совпадения (значения Intro Skipper): под-отпечатки «равны», если различаются не
// больше чем в maxBitDiff битах из 32; совпавшая область может рваться на паузах, но не
// дольше maxGap итемов; заставка — от minIntroSec до maxIntroSec.
const (
	maxBitDiff  = 6
	maxGapItems = 24 // ≈ 3 с
	minRunFrac  = 0.6
	MinIntroSec = 15.0
	MaxIntroSec = 150.0
	MinOutroSec = 20.0
	MaxOutroSec = 300.0
)

// fpLeadSec — запаздывание отпечатка: первый итем chromaprint описывает окно, которому нужно
// ~16 кадров контекста, поэтому событие на 5.0 с звука меняет итем №19, а не №40 (проверено на
// ffmpeg main: 140 итемов на 20 с, смена на индексе 19). Секунды события ≈ индекс·период + lead.
const fpLeadSec = 2.65

// itemTime — секунда звука по индексу итема.
func itemTime(i int) float64 { return float64(i)*fpSecondsPerItem + fpLeadSec }

// Span — найденный общий фрагмент: секунды от начала окна в A и в B и уверенность 0..1
// (доля совпавших итемов внутри области).
type Span struct {
	StartA, EndA float64
	StartB, EndB float64
	Confidence   float64
	Items        int
}

func (s Span) Len() float64 { return s.EndA - s.StartA }

// bestRun — самая длинная область в маске совпадений (с разрывами ≤ maxGapItems), где доля
// совпадений ≥ minRunFrac. Возвращает [start, end) по индексам и число совпавших итемов.
func bestRun(match []bool) (start, end, hits int) {
	n := len(match)
	i := 0
	for i < n {
		if !match[i] {
			i++
			continue
		}
		// область начинается на совпадении и тянется, пока разрыв не превысит maxGapItems
		s := i
		last := i
		cnt := 0
		j := i
		for j < n {
			if match[j] {
				cnt++
				last = j
			} else if j-last > maxGapItems {
				break
			}
			j++
		}
		e := last + 1
		s, e, cnt = trimSparse(match, s, e)
		if e > s && float64(cnt)/float64(e-s) >= minRunFrac && e-s > end-start {
			start, end, hits = s, e, cnt
		}
		i = last + 1
	}
	return
}

// trimSparse обрезает редкие хвосты области: край — последний итем, у которого в окне из
// trimWin итемов внутрь области не меньше половины совпадений. Одиночное случайное
// совпадение через секунду после заставки не должно сдвигать её конец.
const trimWin = 16

func trimSparse(match []bool, s, e int) (int, int, int) {
	dense := func(from, to int) bool { // [from,to)
		if from < 0 {
			from = 0
		}
		if to > len(match) {
			to = len(match)
		}
		c := 0
		for k := from; k < to; k++ {
			if match[k] {
				c++
			}
		}
		return c*2 >= trimWin
	}
	for e > s && !(match[e-1] && dense(e-trimWin, e)) {
		e--
	}
	for s < e && !(match[s] && dense(s, s+trimWin)) {
		s++
	}
	cnt := 0
	for k := s; k < e; k++ {
		if match[k] {
			cnt++
		}
	}
	return s, e, cnt
}

// Match ищет общий фрагмент двух отпечатков: перебираем сдвиг B относительно A, на каждом
// строим маску совпадений и берём самую длинную область. O(len(A)·len(B)) на popcount — для
// окна в 8 минут это ~15 млн операций, доли секунды.
func Match(a, b Fingerprint, minSec, maxSec float64) (Span, bool) {
	if len(a) == 0 || len(b) == 0 {
		return Span{}, false
	}
	minItems := int(minSec / fpSecondsPerItem)
	best := Span{}
	found := false
	match := make([]bool, len(a))
	for shift := -(len(b) - minItems); shift <= len(a)-minItems; shift++ {
		// a[i] соответствует b[i-shift]
		lo := max(0, shift)
		hi := min(len(a), len(b)+shift)
		if hi-lo < minItems {
			continue
		}
		for i := range match {
			match[i] = false
		}
		for i := lo; i < hi; i++ {
			if bits.OnesCount32(a[i]^b[i-shift]) <= maxBitDiff {
				match[i] = true
			}
		}
		s, e, hits := bestRun(match)
		if e-s < minItems {
			continue
		}
		if e-s > best.Items || (e-s == best.Items && hits > int(best.Confidence*float64(best.Items))) {
			best = Span{
				StartA: itemTime(s), EndA: itemTime(e),
				StartB: itemTime(s - shift), EndB: itemTime(e - shift),
				Confidence: float64(hits) / float64(e-s), Items: e - s,
			}
			found = true
		}
	}
	if !found || best.Len() > maxSec {
		return Span{}, false
	}
	best.StartA = math.Round(best.StartA*10) / 10
	best.EndA = math.Round(best.EndA*10) / 10
	best.StartB = math.Round(best.StartB*10) / 10
	best.EndB = math.Round(best.EndB*10) / 10
	return best, true
}
