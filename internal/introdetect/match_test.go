package introdetect

import (
	"math"
	"math/rand"
	"testing"
)

// Синтетика: у двух «серий» разный случайный звук, но общий 40-секундный кусок на разных
// позициях (в A с 30 с, в B с 12 с), с лёгким шумом в 1–2 бита — Match должен найти его с
// точностью до секунды и не найти ничего у несвязанных отпечатков.
func synth(r *rand.Rand, n int) Fingerprint {
	fp := make(Fingerprint, n)
	for i := range fp {
		fp[i] = r.Uint32()
	}
	return fp
}

func TestMatchFindsSharedIntro(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	items := func(sec float64) int { return int(sec / fpSecondsPerItem) }
	intro := synth(r, items(40))
	a := synth(r, items(300))
	b := synth(r, items(300))
	copy(a[items(30):], intro)
	copy(b[items(12):], intro)
	// шум: 1–2 бита в четверти итемов
	for i := range intro {
		if r.Intn(4) == 0 {
			b[items(12)+i] ^= 1 << uint(r.Intn(32))
		}
	}
	sp, ok := Match(a, b, MinIntroSec, MaxIntroSec)
	if !ok {
		t.Fatal("shared intro not found")
	}
	if math.Abs(sp.StartA-fpLeadSec-30) > 1 || math.Abs(sp.EndA-fpLeadSec-70) > 1 || math.Abs(sp.StartB-fpLeadSec-12) > 1 {
		t.Fatalf("span = %+v", sp)
	}
	if sp.Confidence < 0.9 {
		t.Fatalf("confidence = %.2f", sp.Confidence)
	}
	// несвязанные — ничего
	if _, ok := Match(synth(r, items(200)), synth(r, items(200)), MinIntroSec, MaxIntroSec); ok {
		t.Fatal("false positive on unrelated fingerprints")
	}
	// слишком короткое общее (8 с) — не заставка
	c := synth(r, items(200))
	d := synth(r, items(200))
	copy(d[items(50):], c[items(20):items(28)])
	if _, ok := Match(c, d, MinIntroSec, MaxIntroSec); ok {
		t.Fatal("8-second overlap must not count as an intro")
	}
}

func TestBestRunToleratesGaps(t *testing.T) {
	m := make([]bool, 400)
	for i := 100; i < 300; i++ {
		m[i] = i%7 != 0 // дырки чаще, чем maxGap, но короткие
	}
	m[310] = true
	s, e, hits := bestRun(m)
	if s != 100 || e < 299 || e > 300 || hits < 160 {
		t.Fatalf("run = [%d,%d) hits=%d", s, e, hits)
	}
}
