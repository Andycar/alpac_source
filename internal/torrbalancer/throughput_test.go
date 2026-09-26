package torrbalancer

import (
	"testing"
	"time"
)

// Слишком мелкие выборки игнорируются: 200-байтовое тело за 3 мс — не «66 КБ/с».
func TestRecordThroughputIgnoresTinySamples(t *testing.T) {
	p := &Pool{}
	b := &Backend{Weight: 1}
	p.RecordThroughput(b, 200, 3*time.Millisecond)
	p.RecordThroughput(b, 10<<20, 10*time.Millisecond) // мало времени
	if got := b.RateBytesPerSec(); got != 0 {
		t.Fatalf("мелкие выборки не должны учитываться, получено %v", got)
	}
}

// Реальные цифры с прода: медленный бэкенд должен получить нижнюю ступень,
// быстрый — верхнюю, и разница в эффективном весе должна быть кратной.
func TestSpeedTiersFromProductionRates(t *testing.T) {
	p := &Pool{}
	slow := &Backend{Weight: 1} // ts-villa: ~170 КБ/с
	fast := &Backend{Weight: 1} // TS-NA:    ~10 МБ/с
	for i := 0; i < 20; i++ {
		p.RecordThroughput(slow, 2<<20, 12*time.Second)       // ~174 КБ/с
		p.RecordThroughput(fast, 2<<20, 200*time.Millisecond) // ~10 МБ/с
	}
	if slow.SpeedTier() != 1 {
		t.Errorf("медленный бэкенд: ступень %d, ожидалась 1 (%.0f Б/с)", slow.SpeedTier(), slow.RateBytesPerSec())
	}
	if fast.SpeedTier() != 8 {
		t.Errorf("быстрый бэкенд: ступень %d, ожидалась 8 (%.0f Б/с)", fast.SpeedTier(), fast.RateBytesPerSec())
	}
	if fast.effectiveWeight() != 8*slow.effectiveWeight() {
		t.Errorf("эффективные веса: быстрый %d против медленного %d", fast.effectiveWeight(), slow.effectiveWeight())
	}
}

// Бэкенд без замеров получает РАЗВЕДОЧНУЮ ступень, а не нейтральную.
//
// Было 2, стало 4 (см. unmeasuredTier): при нейтральной двойке неизмеренный сервер получал вчетверо
// меньше раздач, чем обстрелянный, и потому не набирал замеров — замкнутый круг, проверенный на
// проде 2026-09-09. Он всё ещё уступает измеренному быстрому (8), но уже не выпадает из ротации.
func TestUnmeasuredBackendGetsExplorationTier(t *testing.T) {
	b := &Backend{Weight: 3}
	if b.SpeedTier() != unmeasuredTier {
		t.Fatalf("без данных ступень %d, ожидалась %d", b.SpeedTier(), unmeasuredTier)
	}
	if b.effectiveWeight() != 3*unmeasuredTier {
		t.Fatalf("эффективный вес %d, ожидался %d (3×%d)", b.effectiveWeight(), 3*unmeasuredTier, unmeasuredTier)
	}
	// Разведка не должна обгонять проверенный быстрый сервер того же веса.
	fast := &Backend{Weight: 3}
	fast.observeRate(20 << 20)
	if b.effectiveWeight() >= fast.effectiveWeight() {
		t.Fatal("неизмеренный сервер не должен весить больше измеренного быстрого")
	}
}

// Гистерезис: колебание вокруг границы не должно дёргать ступень,
// иначе торренты будут прыгать между бэкендами и терять прогретый кэш.
func TestTierHysteresisPreventsFlapping(t *testing.T) {
	p := &Pool{}
	b := &Backend{Weight: 1}
	// разгоняем до уверенной 4-й ступени (~4 МБ/с)
	for i := 0; i < 30; i++ {
		p.RecordThroughput(b, 4<<20, time.Second)
	}
	if b.SpeedTier() != 4 {
		t.Fatalf("подготовка: ступень %d, ожидалась 4", b.SpeedTier())
	}
	// одна выборка чуть ниже границы 2 МБ/с не должна ронять ступень
	p.RecordThroughput(b, 2<<20, 1050*time.Millisecond) // ~2.0 МБ/с
	if b.SpeedTier() != 4 {
		t.Fatalf("ступень упала до %d от одной пограничной выборки", b.SpeedTier())
	}
}

// Устойчивое падение скорости всё-таки должно понижать ступень.
func TestTierDropsOnSustainedSlowdown(t *testing.T) {
	p := &Pool{}
	b := &Backend{Weight: 1}
	for i := 0; i < 30; i++ {
		p.RecordThroughput(b, 10<<20, time.Second) // 10 МБ/с
	}
	if b.SpeedTier() != 8 {
		t.Fatalf("подготовка: ступень %d, ожидалась 8", b.SpeedTier())
	}
	for i := 0; i < 40; i++ {
		p.RecordThroughput(b, 2<<20, 12*time.Second) // ~174 КБ/с
	}
	if b.SpeedTier() != 1 {
		t.Fatalf("после устойчивого падения ступень %d, ожидалась 1 (%.0f Б/с)", b.SpeedTier(), b.RateBytesPerSec())
	}
}
