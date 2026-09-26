package torrbalancer

import "testing"

// Неизмеренный сервер обязан получать заметную долю раздач: иначе он никогда не наберёт замеров,
// чтобы доказать свой канал. Замер на проде 2026-09-09 показал именно это — две коробки стояли
// с нулём раздач при исправном канале, потому что нейтральная ступень была вчетверо ниже, чем у
// уже обстрелянных серверов.
func TestUnmeasuredBackendGetsExplorationShare(t *testing.T) {
	fast := &Backend{ID: "fast", Weight: 2}
	fast.observeRate(20 << 20) // 20 МБ/с → верхняя ступень
	fresh := &Backend{ID: "fresh", Weight: 2}

	if fast.SpeedTier() != 8 {
		t.Fatalf("измеренный быстрый сервер должен быть на ступени 8, получили %d", fast.SpeedTier())
	}
	if fresh.SpeedTier() != unmeasuredTier {
		t.Fatalf("неизмеренный должен быть на разведочной ступени %d, получили %d", unmeasuredTier, fresh.SpeedTier())
	}

	// Доля считается по эффективному весу: разведка обязана быть заметной, но не подавляющей.
	share := float64(fresh.effectiveWeight()) / float64(fast.effectiveWeight())
	if share < 0.4 {
		t.Fatalf("разведочная доля слишком мала (%.2f от измеренного) — сервер не успеет измериться", share)
	}
	if share > 0.75 {
		t.Fatalf("разведочная доля слишком велика (%.2f) — неизвестный сервер теснит проверенных", share)
	}

	// Первый же замер снимает разведку и ставит сервер на своё место.
	fresh.observeRate(300 << 10) // 300 КБ/с — канал не тянет видео
	if fresh.SpeedTier() != 1 {
		t.Fatalf("после замера медленный сервер должен упасть на ступень 1, получили %d", fresh.SpeedTier())
	}
	if fresh.effectiveWeight() >= fast.effectiveWeight() {
		t.Fatal("измеренный медленный сервер не должен весить как быстрый")
	}
}
