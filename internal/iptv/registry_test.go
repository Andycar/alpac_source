package iptv

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// newRegistryStore builds a store with an enabled registry and a fake global
// playlist cache injected directly (no network).
func newRegistryStore(t *testing.T, donors []Channel) *Store {
	t.Helper()
	url := "http://127.0.0.1:1/donor.m3u"
	s := NewStore(t.TempDir(), StoreConfig{
		GlobalPlaylists: []string{url},
		MergeGlobal:     true,
		Registry:        true,
	})
	id := playlistIDFromURL(url)
	for i := range donors {
		donors[i].PlaylistID = id
	}
	s.mu.Lock()
	s.cache["global_"+id] = &PlaylistCache{
		Playlist: Playlist{ID: id, URL: url, IsGlobal: true},
		Channels: donors,
		ParsedAt: time.Now(),
	}
	s.mu.Unlock()
	return s
}

func donorSet() []Channel {
	return []Channel{
		{ID: "d1", Name: "Первый канал HD", URL: "http://a/first_hd.m3u8", Quality: "HD", TvgID: "1tv.ru", Logo: "http://logo/1.png", Group: "Эфир"},
		{ID: "d2", Name: "Первый канал FHD", URL: "http://b/first_fhd.m3u8", Quality: "FHD", TvgID: "1tv.ru"},
		// иное написание, склейка по tvg-id (по имени «первый» ≠ «первый канал»)
		{ID: "d3", Name: "ПЕРВЫЙ", URL: "http://c/first_sd.m3u8", Quality: "SD", TvgID: "1tv.ru"},
		{ID: "d4", Name: "Матч ТВ", URL: "http://a/match.m3u8", Quality: "HD", TvgID: "matchtv.ru", Group: "Спорт"},
	}
}

// TestRegistryIngestDedup: три написания «Первого» дают ОДИН канал реестра с
// тремя источниками, отсортированными по качеству; незнакомый канал добавляется
// только при addNew.
func TestRegistryIngestDedup(t *testing.T) {
	s := newRegistryStore(t, donorSet())
	st := s.IngestRegistry(true) // seed
	if st.NewAdded != 2 {
		t.Fatalf("expected 2 new channels (Первый + Матч), got %+v", st)
	}
	chans := s.registryChannels()
	if len(chans) != 2 {
		t.Fatalf("expected 2 channels, got %d: %+v", len(chans), chans)
	}
	first, ok := s.registry.Get(regChannelID(normChannelName("Первый канал")))
	if !ok {
		t.Fatalf("first channel not found by stable id")
	}
	if len(first.Auto) != 3 {
		t.Fatalf("expected 3 sources for Первый, got %+v", first.Auto)
	}
	if first.Auto[0].Quality != "FHD" {
		t.Fatalf("expected FHD source first, got %+v", first.Auto[0])
	}
	if first.TvgID != "1tv.ru" || first.Logo == "" || first.Group == "" {
		t.Fatalf("expected tvg/logo/group filled from donors, got %+v", first)
	}
}

// TestRegistryFailover: лучший источник помечен мёртвым → view канала берёт
// следующий живой; все мёртвые → первый (health может быть протухшим).
func TestRegistryFailover(t *testing.T) {
	s := newRegistryStore(t, donorSet())
	s.IngestRegistry(true)
	s.healthOn = true

	id := regChannelID(normChannelName("Первый канал"))
	ch, pl := s.GetChannel(0, id)
	if ch == nil || pl == nil {
		t.Fatalf("registry channel not resolvable via GetChannel")
	}
	if ch.URL != "http://b/first_fhd.m3u8" {
		t.Fatalf("expected FHD source, got %s", ch.URL)
	}
	if pl.ID != RegistryPlaylistID {
		t.Fatalf("expected synthetic registry playlist, got %+v", pl)
	}

	// FHD умер → следующий по качеству.
	s.health.mu.Lock()
	s.health.dead["http://b/first_fhd.m3u8"] = struct{}{}
	s.health.mu.Unlock()
	ch, _ = s.GetChannel(0, id)
	if ch.URL != "http://a/first_hd.m3u8" {
		t.Fatalf("expected failover to HD source, got %s", ch.URL)
	}

	// Все умерли → первый источник (не пустота): пусть плеер покажет реальную ошибку.
	s.health.mu.Lock()
	s.health.dead["http://a/first_hd.m3u8"] = struct{}{}
	s.health.dead["http://c/first_sd.m3u8"] = struct{}{}
	s.health.mu.Unlock()
	ch, _ = s.GetChannel(0, id)
	if ch == nil || ch.URL == "" {
		t.Fatalf("expected some source even when all dead, got %+v", ch)
	}
}

// TestRegistryPinnedWins: закреплённый руками источник раньше auto, но мёртвый
// pinned уступает живому auto.
func TestRegistryPinnedWins(t *testing.T) {
	s := newRegistryStore(t, donorSet())
	s.IngestRegistry(true)
	id := regChannelID(normChannelName("Первый канал"))
	_, err := s.registry.Upsert(RegChannel{
		ID:     id,
		Pinned: []RegSource{{URL: "http://my/own_first.m3u8", Quality: "FHD"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ch, _ := s.GetChannel(0, id)
	if ch.URL != "http://my/own_first.m3u8" {
		t.Fatalf("expected pinned source first, got %s", ch.URL)
	}

	s.healthOn = true
	s.health.mu.Lock()
	s.health.dead["http://my/own_first.m3u8"] = struct{}{}
	s.health.mu.Unlock()
	ch, _ = s.GetChannel(0, id)
	if ch.URL != "http://b/first_fhd.m3u8" {
		t.Fatalf("expected live auto to beat dead pinned, got %s", ch.URL)
	}
}

// TestRegistrySourceSurvivesDonorLoss: источник, пропавший у донора, доживает
// regSourceKeepDays; протухший за окно — выкидывается.
func TestRegistrySourceSurvivesDonorLoss(t *testing.T) {
	s := newRegistryStore(t, donorSet())
	s.IngestRegistry(true)
	id := regChannelID(normChannelName("Первый канал"))

	// Донор потерял FHD-вариант.
	smaller := donorSet()
	smaller = append(smaller[:1], smaller[2:]...) // remove d2 (FHD)
	s.registry.Ingest(smaller, false)
	c, _ := s.registry.Get(id)
	if len(c.Auto) != 3 {
		t.Fatalf("expected disappeared source to survive keep-window, got %+v", c.Auto)
	}

	// Тот же ingest, но LastSeen пропавшего источника старше окна → прощаемся.
	s.registry.mu.Lock()
	for i := range s.registry.byID[id].Auto {
		if s.registry.byID[id].Auto[i].URL == "http://b/first_fhd.m3u8" {
			s.registry.byID[id].Auto[i].LastSeen = time.Now().Unix() - (regSourceKeepDays+1)*86400
		}
	}
	s.registry.mu.Unlock()
	s.registry.Ingest(smaller, false)
	c, _ = s.registry.Get(id)
	if len(c.Auto) != 2 {
		t.Fatalf("expected stale source pruned, got %+v", c.Auto)
	}
}

// TestRegistryPersistence: реестр переживает перезапуск (registry.json).
func TestRegistryPersistence(t *testing.T) {
	dir := t.TempDir()
	url := "http://127.0.0.1:1/donor.m3u"
	cfg := StoreConfig{GlobalPlaylists: []string{url}, MergeGlobal: true, Registry: true}
	s := NewStore(dir, cfg)
	pid := playlistIDFromURL(url)
	donors := donorSet()
	for i := range donors {
		donors[i].PlaylistID = pid
	}
	s.registry.Ingest(donors, true)

	s2 := NewStore(dir, cfg)
	if got := s2.registry.Len(); got != 2 {
		t.Fatalf("expected 2 channels after reload, got %d", got)
	}
	id := regChannelID(normChannelName("Первый канал"))
	if _, ok := s2.registry.Get(id); !ok {
		t.Fatalf("stable channel id lost across restart")
	}
}

// TestRegistryDisabledChannelHidden: выключенный канал уходит из выдачи и не
// резолвится, но остаётся в реестре.
func TestRegistryDisabledChannelHidden(t *testing.T) {
	s := newRegistryStore(t, donorSet())
	s.IngestRegistry(true)
	id := regChannelID(normChannelName("Матч ТВ"))
	if !s.registry.SetDisabled(id, true) {
		t.Fatalf("disable failed")
	}
	if got := len(s.registryChannels()); got != 1 {
		t.Fatalf("expected 1 visible channel, got %d", got)
	}
	if ch, _ := s.GetChannel(0, id); ch != nil {
		t.Fatalf("disabled channel must not resolve, got %+v", ch)
	}
	pls := s.ListPlaylists(0)
	if len(pls) == 0 || pls[0].ID != RegistryPlaylistID || pls[0].ChannelCount != 1 {
		t.Fatalf("expected registry playlist first with 1 enabled channel, got %+v", pls)
	}
}

// TestRegistryOnlyHidesGlobals: registry_only прячет «Все каналы».
func TestRegistryOnlyHidesGlobals(t *testing.T) {
	s := newRegistryStore(t, donorSet())
	s.registryOnly = true
	s.IngestRegistry(true)
	pls := s.ListPlaylists(0)
	if len(pls) != 1 || pls[0].ID != RegistryPlaylistID {
		t.Fatalf("expected only the registry playlist, got %+v", pls)
	}
}

// TestExportM3U: экспорт со стабильными ссылками на свой сервер.
func TestExportM3U(t *testing.T) {
	s := newRegistryStore(t, donorSet())
	s.IngestRegistry(true)
	m3u := s.ExportM3U([]string{"http://epg/x.xml"}, func(ch Channel) string {
		return "http://my.server/api/iptv/stream/" + ch.ID
	})
	want := []string{
		`#EXTM3U url-tvg="http://epg/x.xml"`,
		`tvg-id="1tv.ru"`,
		"http://my.server/api/iptv/stream/" + regChannelID(normChannelName("Первый канал")),
		`group-title="Спорт"`,
	}
	for _, w := range want {
		if !strings.Contains(m3u, w) {
			t.Fatalf("export missing %q in:\n%s", w, m3u)
		}
	}
}

// TestRegistryIngestBracketMarkers: iptv-org-стиль «Первый канал (1080p) [Not 24/7]»
// склеивается с нашим «Первый канал» — статус-маркеры не часть имени.
func TestRegistryIngestBracketMarkers(t *testing.T) {
	s := newRegistryStore(t, donorSet())
	s.IngestRegistry(true)
	id := regChannelID(normChannelName("Первый канал"))

	extra := []Channel{{
		ID: "x1", Name: "Первый канал (1080p) [Not 24/7]",
		URL: "http://iptvorg/first.m3u8", Quality: "FHD", PlaylistID: "iptvorg",
	}}
	st := s.registry.Ingest(append(donorSet(), extra...), false)
	if st.Unmatched != 0 {
		t.Fatalf("bracket-marker name must match existing channel, got %+v", st)
	}
	c, _ := s.registry.Get(id)
	found := false
	for _, src := range c.Auto {
		if src.URL == "http://iptvorg/first.m3u8" {
			found = true
		}
	}
	if !found {
		t.Fatalf("iptv-org source not attached: %+v", c.Auto)
	}
}

// TestHealthGuardRejectsAllDeadCycle: цикл, объявивший мёртвыми ~все источники,
// ОТБРАСЫВАЕТСЯ — прод показал, что это почти всегда проблема на нашей стороне
// (лимит сессий подписочной панели), а не одновременная смерть всех каналов.
func TestHealthGuardRejectsAllDeadCycle(t *testing.T) {
	// Все пробы падают: сервер, отвечающий отказом на любой запрос.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusBadGateway)
	}))
	defer srv.Close()

	donors := make([]Channel, 0, 30)
	for i := 0; i < 30; i++ {
		donors = append(donors, Channel{
			ID:   "d" + strconv.Itoa(i),
			Name: "Канал " + strconv.Itoa(i),
			URL:  srv.URL + "/ch" + strconv.Itoa(i) + ".m3u8",
		})
	}
	s := newRegistryStore(t, donors)
	s.IngestRegistry(true)
	s.healthOn = true
	s.healthClient = srv.Client()

	// Предыдущий цикл нашёл один мёртвый источник — состояние, которое НЕ должно
	// быть затёрто недостоверным результатом.
	s.health.mu.Lock()
	s.health.dead = map[string]struct{}{"http://previously/dead.m3u8": {}}
	s.health.mu.Unlock()

	s.healthCycle(context.Background())

	s.health.mu.RLock()
	dead := len(s.health.dead)
	_, kept := s.health.dead["http://previously/dead.m3u8"]
	s.health.mu.RUnlock()
	if dead != 1 || !kept {
		t.Fatalf("all-dead cycle must be discarded, keeping previous map; got %d entries (kept=%v)", dead, kept)
	}
}

// TestHealthCycleSplitsBudgetAcrossDonors: бюджет проб делится между донорами —
// иначе первый донор съедает cap целиком и остальные не проверяются никогда.
func TestHealthCycleSplitsBudgetAcrossDonors(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	mark := func(host string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			hits[host]++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte("#EXTM3U\n#EXTINF:4,\nseg.ts\n"))
		}
	}
	big := httptest.NewServer(mark("big"))
	defer big.Close()
	small := httptest.NewServer(mark("small"))
	defer small.Close()

	s := NewStore(t.TempDir(), StoreConfig{
		GlobalPlaylists: []string{"http://donor/big.m3u", "http://donor/small.m3u"},
	})
	s.healthClient = big.Client()
	s.SetHealthLimits(10, 2) // бюджет 10 при 25 источниках у первого донора

	mkCache := func(url, base string, n int) {
		chans := make([]Channel, 0, n)
		for i := 0; i < n; i++ {
			chans = append(chans, Channel{ID: base + strconv.Itoa(i), URL: base + "/c" + strconv.Itoa(i) + ".m3u8"})
		}
		s.mu.Lock()
		s.cache["global_"+playlistIDFromURL(url)] = &PlaylistCache{Channels: chans}
		s.mu.Unlock()
	}
	mkCache("http://donor/big.m3u", big.URL, 25)
	mkCache("http://donor/small.m3u", small.URL, 25)

	s.healthCycle(context.Background())

	mu.Lock()
	defer mu.Unlock()
	if hits["small"] == 0 {
		t.Fatalf("second donor never probed — budget was eaten by the first: %+v", hits)
	}
}

// TestRegistryNamePrecedence: имя из конфига ВСЕГДА главнее сохранённого в
// registry.json — включая случай, когда оператор задал ровно дефолтное имя.
// Сравнение с дефолтом вместо признака «задано» роняло бы переименование.
func TestRegistryNamePrecedence(t *testing.T) {
	dir := t.TempDir()

	// Конфиг молчит → дефолтное брендовое имя.
	r := LoadRegistry(dir, "")
	if r.Name() != DefaultRegistryName {
		t.Fatalf("empty config должен дать %q, got %q", DefaultRegistryName, r.Name())
	}
	if _, err := r.Upsert(RegChannel{Name: "Тест", Pinned: []RegSource{{URL: "http://x/1.m3u8"}}}); err != nil {
		t.Fatal(err)
	}

	// Снимок сохранён с прежним именем — эмулируем «переименование в конфиге».
	r.mu.Lock()
	r.name = "Мои каналы"
	r.mu.Unlock()
	r.save()

	// Конфиг задал новое имя → оно побеждает снимок.
	if got := LoadRegistry(dir, "Alpac IPTV").Name(); got != "Alpac IPTV" {
		t.Fatalf("configured name must win over snapshot, got %q", got)
	}
	// Конфиг задал имя, совпадающее с дефолтом → тоже побеждает снимок.
	if got := LoadRegistry(dir, DefaultRegistryName).Name(); got != DefaultRegistryName {
		t.Fatalf("explicitly configured default must win over snapshot, got %q", got)
	}
	// Конфиг молчит → поднимаем имя из снимка.
	if got := LoadRegistry(dir, "").Name(); got != "Мои каналы" {
		t.Fatalf("silent config must keep the saved name, got %q", got)
	}
}

// TestRegistryReleasesRemovedDonorAtOnce: донора убрали из global_playlists — его источники
// уходят с ближайшим ingest, а не через неделю; канал, живший только на нём, пропадает из выдачи.
// У донора, оставшегося в сборщике, пропавший источник по-прежнему доживает окно. Так было с
// платной панелью: после отказа от подписки её заглушка неделю показывалась вместо каналов.
func TestRegistryReleasesRemovedDonorAtOnce(t *testing.T) {
	s := newRegistryStore(t, donorSet())
	first := regChannelID(normChannelName("Первый канал"))

	panelURL := "http://panel.example/list.m3u"
	pid := playlistIDFromURL(panelURL)
	s.mu.Lock()
	s.globalURLs = append(s.globalURLs, panelURL)
	s.cache["global_"+pid] = &PlaylistCache{
		Playlist: Playlist{ID: pid, URL: panelURL, IsGlobal: true},
		Channels: []Channel{
			{ID: "p1", Name: "Первый канал 4K", URL: "http://panel/first_4k.m3u8", Quality: "4K", TvgID: "1tv.ru", PlaylistID: pid},
			{ID: "p2", Name: "Канал только с панели", URL: "http://panel/only.m3u8", Quality: "HD", PlaylistID: pid},
		},
		ParsedAt: time.Now(),
	}
	s.mu.Unlock()
	s.IngestRegistry(true)
	if c, _ := s.registry.Get(first); len(c.Auto) != 4 {
		t.Fatalf("с панелью у Первого должно быть 4 источника, got %+v", c.Auto)
	}
	panelOnly := regChannelID(normChannelName("Канал только с панели"))
	if _, ok := s.registry.Get(panelOnly); !ok {
		t.Fatal("канал с панели не заведён")
	}

	// Панель убрали из сборщика: так делает перечитывание конфига — донор уходит из списка
	// вместе со своим кэшем. Второй донор при этом потерял FHD-вариант (временный сбой).
	s.mu.Lock()
	s.globalURLs = s.globalURLs[:1]
	delete(s.cache, "global_"+pid)
	id := playlistIDFromURL(s.globalURLs[0])
	s.cache["global_"+id].Channels = append(donorSet()[:1], donorSet()[2:]...)
	for i := range s.cache["global_"+id].Channels {
		s.cache["global_"+id].Channels[i].PlaylistID = id
	}
	s.mu.Unlock()
	st := s.IngestRegistry(false)

	c, _ := s.registry.Get(first)
	var urls []string
	for _, src := range c.Auto {
		urls = append(urls, src.URL)
	}
	for _, u := range urls {
		if u == "http://panel/first_4k.m3u8" {
			t.Fatalf("источник убранной панели остался: %v", urls)
		}
	}
	if len(c.Auto) != 3 {
		t.Fatalf("у оставшегося донора пропавший FHD должен дожить окно: %v", urls)
	}
	if st.Released != 2 {
		t.Fatalf("отпущено источников: %d, want 2", st.Released)
	}
	for _, ch := range s.registryChannels() {
		if ch.ID == panelOnly {
			t.Fatal("канал, живший только на панели, остался в выдаче")
		}
	}
}
