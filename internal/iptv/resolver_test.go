package iptv

import (
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// registerTestResolver ставит временный резолвер и убирает его после теста.
func registerTestResolver(t *testing.T, name string, ttl time.Duration, fn ResolverFunc) {
	t.Helper()
	resolversMu.Lock()
	resolvers[name] = resolverEntry{fn: fn, ttl: ttl}
	resolversMu.Unlock()
	t.Cleanup(func() {
		resolversMu.Lock()
		delete(resolvers, name)
		resolversMu.Unlock()
		resolvedMu.Lock()
		delete(resolved, name)
		resolvedMu.Unlock()
	})
}

// TestResolverCachedWithinTTL: резолвер зовут один раз на TTL, а не на каждый
// запрос канала — иначе сайт вещателя получал бы обращение от каждого зрителя.
func TestResolverCachedWithinTTL(t *testing.T) {
	var calls atomic.Int32
	registerTestResolver(t, "t_cache", time.Hour, func(*http.Client) (string, error) {
		calls.Add(1)
		return "https://cdn/live.m3u8", nil
	})
	s := NewStore(t.TempDir(), StoreConfig{Registry: true})
	for i := 0; i < 5; i++ {
		if u, ok := s.ResolveSource("t_cache"); !ok || u != "https://cdn/live.m3u8" {
			t.Fatalf("итерация %d: got %q %v", i, u, ok)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("резолвер вызван %d раз, ожидался 1", calls.Load())
	}
}

// TestResolverKeepsLastGoodOnFailure: сбой резолва НЕ должен гасить канал —
// прошлый адрес живёт дольше, чем длится моргание сайта.
func TestResolverKeepsLastGoodOnFailure(t *testing.T) {
	var fail atomic.Bool
	registerTestResolver(t, "t_fail", time.Nanosecond, func(*http.Client) (string, error) {
		if fail.Load() {
			return "", errResolve("сайт недоступен")
		}
		return "https://cdn/good.m3u8", nil
	})
	s := NewStore(t.TempDir(), StoreConfig{Registry: true})
	if u, ok := s.ResolveSource("t_fail"); !ok || u == "" {
		t.Fatalf("первый резолв: %q %v", u, ok)
	}
	fail.Store(true)
	time.Sleep(2 * time.Millisecond) // TTL истёк → пойдёт повторный резолв
	u, ok := s.ResolveSource("t_fail")
	if !ok || u != "https://cdn/good.m3u8" {
		t.Fatalf("при сбое обязан отдаваться прошлый адрес, got %q %v", u, ok)
	}
}

// TestResolverUnknownAndChannelView: неизвестный резолвер не выдаёт адрес, а
// канал с рабочим резолвером отдаёт клиенту ДОБЫТЫЙ url, а не запасной.
func TestResolverUnknownAndChannelView(t *testing.T) {
	s := NewStore(t.TempDir(), StoreConfig{Registry: true})
	if _, ok := s.ResolveSource("нет-такого"); ok {
		t.Fatal("неизвестный резолвер не должен давать адрес")
	}
	registerTestResolver(t, "t_view", time.Hour, func(*http.Client) (string, error) {
		return "https://cdn/fresh.m3u8", nil
	})
	ch, err := s.Registry().Upsert(RegChannel{
		Name:   "Тестовый канал",
		Pinned: []RegSource{{Resolver: "t_view", URL: "https://cdn/stale.m3u8"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetChannel(0, ch.ID)
	if got == nil || got.URL != "https://cdn/fresh.m3u8" {
		t.Fatalf("канал обязан отдавать добытый адрес, got %+v", got)
	}
	// Источник с резолвером не попадает в health-пробы: пробовать нечего.
	for _, p := range s.registrySourceProbes() {
		if p.URL == "https://cdn/stale.m3u8" {
			t.Fatal("динамический источник не должен уходить в health-пробы")
		}
	}
}
