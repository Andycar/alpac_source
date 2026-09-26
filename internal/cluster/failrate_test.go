package cluster

import (
	"math"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 0.02 }

// Пока выборка мала, отказы не влияют на маршрутизацию — иначе один неудачный
// запрос холодной ноды читался бы как «сломана на 100%».
func TestFailRateNeedsSamples(t *testing.T) {
	n := &Node{Weight: 1}
	for i := 0; i < minFailSamples-1; i++ {
		n.observeOutcome(true)
	}
	if got := n.FailRate(); got != 0 {
		t.Fatalf("до %d выборок ставка должна быть 0, получено %v", minFailSamples, got)
	}
	if got := n.failPenalty(); got != 1 {
		t.Fatalf("штраф без данных должен быть 1, получено %v", got)
	}
}

// Постоянно падающая нода доходит до максимума ставки, но штраф остаётся
// конечным — она должна оставаться достижимой для проб восстановления.
func TestFailRateSaturatesAndPenaltyIsBounded(t *testing.T) {
	n := &Node{Weight: 1}
	for i := 0; i < 300; i++ {
		n.observeOutcome(true)
	}
	if got := n.FailRate(); !approx(got, 1) {
		t.Fatalf("ставка при сплошных отказах: %v, ожидалась ~1", got)
	}
	if got := n.failPenalty(); got != 10 {
		t.Fatalf("штраф должен быть ограничен 10, получено %v", got)
	}
}

// Восстановившаяся нода перестаёт быть наказанной — пожизненные счётчики так не умеют.
func TestFailRateRecovers(t *testing.T) {
	n := &Node{Weight: 1}
	for i := 0; i < 100; i++ {
		n.observeOutcome(true)
	}
	for i := 0; i < 200; i++ {
		n.observeOutcome(false)
	}
	if got := n.FailRate(); got > 0.05 {
		t.Fatalf("после восстановления ставка %v, ожидалась близкая к 0", got)
	}
}

// Нода, теряющая половину запросов, обходится вдвое дороже: половину работы
// придётся переделать в другом месте.
func TestFailPenaltyReflectsCostPerSuccess(t *testing.T) {
	n := &Node{Weight: 1}
	for i := 0; i < 200; i++ {
		n.observeOutcome(i%2 == 0)
	}
	if got := n.FailRate(); got < 0.4 || got > 0.6 {
		t.Fatalf("ставка при 50%% отказов: %v", got)
	}
	if got := n.failPenalty(); got < 1.7 || got > 2.3 {
		t.Fatalf("штраф при 50%% отказов: %v, ожидался ~2", got)
	}
}
