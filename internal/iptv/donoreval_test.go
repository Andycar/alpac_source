package iptv

import "testing"

// Реестр на четыре канала: у одного оба источника от уходящей панели, у
// другого рядом с панелью есть бесплатный донор, третий выключен, четвёртый
// держится на ручном источнике.
func fitRegistry(t *testing.T) *Registry {
	t.Helper()
	r := LoadRegistry(t.TempDir(), "тест")
	add := func(name, tvg string, disabled bool, srcs ...RegSource) {
		key := normChannelName(name)
		c := &RegChannel{ID: regChannelID(key), Name: name, NormKey: key, TvgID: tvg, Disabled: disabled}
		for _, s := range srcs {
			if s.From == "manual" {
				c.Pinned = append(c.Pinned, s)
			} else {
				c.Auto = append(c.Auto, s)
			}
		}
		r.channels = append(r.channels, c)
	}
	add("Пятый канал", "5tv.ru", false,
		RegSource{URL: "http://panel/5-hd", From: "panel"},
		RegSource{URL: "http://panel/5-sd", From: "panel"})
	add("Первый канал", "1tv.ru", false,
		RegSource{URL: "http://panel/1", From: "panel"},
		RegSource{URL: "http://free/1", From: "iptvorg"})
	add("Brazzers", "", false, RegSource{URL: "http://panel/xxx", From: "panel"})
	add("Спрятанный", "", true, RegSource{URL: "http://panel/hidden", From: "panel"})
	add("Наш ручной", "", false, RegSource{URL: "http://manual/x", From: "manual"})
	r.reindexLocked()
	return r
}

func TestOrphansIfDroppedCountsOnlyVisibleChannels(t *testing.T) {
	r := fitRegistry(t)
	orph := r.OrphansIfDropped(map[string]bool{"panel": true})
	got := map[string]bool{}
	for _, c := range orph {
		got[c.Name] = true
	}
	if len(orph) != 2 || !got["Пятый канал"] || !got["Brazzers"] {
		t.Fatalf("осиротевшие: %v", got)
	}
	// Канал с бесплатным донором рядом, выключенный канал и ручной источник —
	// не сироты: первый переживёт уход панели, второго никто не видит,
	// третьего панель и не кормила.
	if got["Первый канал"] || got["Спрятанный"] || got["Наш ручной"] {
		t.Fatalf("лишние в сиротах: %v", got)
	}
	if n := len(r.OrphansIfDropped(nil)); n != 0 {
		t.Fatalf("без ухода сирот быть не может, получили %d", n)
	}
}

func TestFitDonorMatchesLikeIngest(t *testing.T) {
	r := fitRegistry(t)
	orph := r.OrphansIfDropped(map[string]bool{"panel": true})
	fit := r.FitDonor([]Channel{
		// по tvg-id, хотя написание другое
		{Name: "5 канал FHD", TvgID: "5tv.ru", URL: "http://cand/5", Quality: "FHD"},
		// по нормализованному имени (регистр и тег качества роли не играют)
		{Name: "ПЕРВЫЙ КАНАЛ HD", URL: "http://cand/1"},
		// два потока одного незнакомого канала — одно новое имя, не два
		{Name: "Че!", URL: "http://cand/che1"},
		{Name: "Че! HD", URL: "http://cand/che2"},
		// запись без ссылки не считается ничем
		{Name: "Пустая", URL: ""},
	}, orph)

	if fit.Parsed != 5 || fit.Matched != 2 || fit.Channels != 2 {
		t.Fatalf("примерка: %+v", fit)
	}
	if fit.New != 1 || len(fit.NewNames) != 1 {
		t.Fatalf("новых имён: %v", fit.NewNames)
	}
	if len(fit.Rescued) != 1 || fit.Rescued[0].Name != "Пятый канал" || fit.Rescued[0].URL != "http://cand/5" {
		t.Fatalf("спасённые: %+v", fit.Rescued)
	}
	// «Первый канал» кандидат тоже закрывает, но сиротой он не был — в спасённые
	// попадать не должен, иначе замена выглядела бы выгоднее, чем есть.
	for _, s := range fit.Rescued {
		if s.Name == "Первый канал" {
			t.Fatal("в спасённых канал, который и так жил")
		}
	}
}

// Примерка обязана оставаться чтением: ни одного нового канала и ни одного
// источника в реестре после неё появиться не может.
func TestFitDonorDoesNotMutateRegistry(t *testing.T) {
	r := fitRegistry(t)
	before := len(r.List())
	srcs := 0
	for _, c := range r.List() {
		srcs += len(c.Sources())
	}
	r.FitDonor([]Channel{{Name: "Совсем новый", URL: "http://x/1"}, {Name: "Пятый канал", URL: "http://x/5"}},
		r.OrphansIfDropped(map[string]bool{"panel": true}))
	after := 0
	for _, c := range r.List() {
		after += len(c.Sources())
	}
	if len(r.List()) != before || after != srcs {
		t.Fatalf("реестр изменился: каналов %d→%d, источников %d→%d", before, len(r.List()), srcs, after)
	}
}
