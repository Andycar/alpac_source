package iptv

import "testing"

func TestGenreOf(t *testing.T) {
	cases := map[string]string{
		"Музыка": "Музыка", "музыка": "Музыка", "Music": "Музыка",
		"детские": "Детские", "Kids": "Детские", "спорт": "Спорт", "Sport": "Спорт",
		"новости": "Новости", "News": "Новости", "кино": "Кино", "Movies": "Кино",
		"познавательные": "Познавательные", "развлекательные": "Развлекательные",
		"взрослые": "Взрослые", "Эротика": "Взрослые",
		// Составные метки доноров — берём первый распознанный кусок.
		"Animation;Kids": "Детские", "Comedy;Family;Movies": "Развлекательные",
		// Мусор жанром не является: качество живёт в Quality, а «другие»
		// и «Undefined» не значат ничего.
		"HD": "", "HD Orig": "", "4K": "", "другие": "", "Undefined": "",
		"Ungrouped": "", "": "",
		// Страна — не жанр.
		"Русские": "", "Итальянские": "",
	}
	for in, want := range cases {
		if got := GenreOf(in); got != want {
			t.Errorf("GenreOf(%q) = %q, ожидалось %q", in, got, want)
		}
	}
}

func TestCountryByTvgID(t *testing.T) {
	cases := map[string]string{
		"BBCNews.uk@SD":    "Британские",
		"ChannelOne.ru@SD": "Русские",
		"Balapan.kz@HD":    "Казахские",
		"AtomicTV.ro@SD":   "Румынские",
		"BiznetKids.id@SD": "Индонезийские",
		"FirstMusic.by@SD": "Белорусские",
		"NoSuffix":         "",
		"Weird.zz@SD":      "", // неизвестный код — молчим, а не выдумываем
		"":                 "",
		"Trailing.":        "",
		"PlainName.us":     "Американские", // без @-суффикса тоже валидно
	}
	for in, want := range cases {
		if got := CountryByTvgID(in); got != want {
			t.Errorf("CountryByTvgID(%q) = %q, ожидалось %q", in, got, want)
		}
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name                   string
		group, tvg, chanName   string
		wantCountry, wantGenre string
	}{
		{
			name:  "страновая группа остаётся страной",
			group: "Русские", tvg: "", chanName: "Первый канал",
			wantCountry: "Русские", wantGenre: "",
		},
		{
			// Ради чего всё затевалось: канал сидел в жанровой группе и терял
			// страну, потому что поле было одно.
			name:  "жанровая группа + страна из tvg-id",
			group: "Детские", tvg: "Balapan.kz@HD", chanName: "Balapan HD",
			wantCountry: "Казахские", wantGenre: "Детские",
		},
		{
			name:  "жанр угадан по имени, когда группа мусорная",
			group: "другие", tvg: "ChannelOne.ru@SD", chanName: "Матч! Боец",
			wantCountry: "Русские", wantGenre: "Спорт",
		},
		{
			name:  "страновая группа + жанр из имени",
			group: "Русские", tvg: "", chanName: "Детский мир",
			wantCountry: "Русские", wantGenre: "Детские",
		},
		{
			name:  "ничего не известно — оба пустые",
			group: "Undefined", tvg: "", chanName: "Нечто",
			wantCountry: "", wantGenre: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, g := Classify(tc.group, tc.tvg, tc.chanName)
			if c != tc.wantCountry || g != tc.wantGenre {
				t.Errorf("Classify(%q,%q,%q) = (%q,%q), ожидалось (%q,%q)",
					tc.group, tc.tvg, tc.chanName, c, g, tc.wantCountry, tc.wantGenre)
			}
		})
	}
}

func TestGroupForPrefersCountry(t *testing.T) {
	if got := GroupFor("Русские", "Спорт"); got != "Русские" {
		t.Errorf("GroupFor = %q, ожидалась страна", got)
	}
	if got := GroupFor("", "Спорт"); got != "Спорт" {
		t.Errorf("GroupFor без страны = %q, ожидался жанр", got)
	}
	if got := GroupFor("", ""); got != "" {
		t.Errorf("GroupFor пустых = %q, ожидалась пустая строка", got)
	}
}

// Жанровая ось не должна содержать стран, а страновая — жанров: ровно та
// жалоба, с которой началась переделка.
func TestGetGroupsByAxesDoNotMix(t *testing.T) {
	s := &Store{}
	chans := []Channel{
		{Name: "Первый канал", Country: "Русские"},
		{Name: "Матч ТВ", Country: "Русские", Genre: "Спорт"},
		{Name: "Rai 1", Country: "Итальянские"},
		{Name: "MTV", Genre: "Музыка"},
		{Name: "Без всего"},
	}
	countries := s.groupsFrom(chans, GroupByCountry)
	genres := s.groupsFrom(chans, GroupByGenre)

	countryNames := map[string]int{}
	for _, g := range countries {
		countryNames[g.Name] = g.Count
	}
	if countryNames["Русские"] != 2 || countryNames["Итальянские"] != 1 {
		t.Errorf("страны посчитаны неверно: %v", countryNames)
	}
	if _, bad := countryNames["Спорт"]; bad {
		t.Error("жанр «Спорт» попал в список стран")
	}
	if _, bad := countryNames["Музыка"]; bad {
		t.Error("жанр «Музыка» попал в список стран")
	}

	genreNames := map[string]int{}
	for _, g := range genres {
		genreNames[g.Name] = g.Count
	}
	if genreNames["Спорт"] != 1 || genreNames["Музыка"] != 1 {
		t.Errorf("жанры посчитаны неверно: %v", genreNames)
	}
	if _, bad := genreNames["Русские"]; bad {
		t.Error("страна «Русские» попала в список жанров")
	}
	// Канал без классификации не создаёт графу «пусто» ни на одной оси.
	if len(genres) != 2 || len(countries) != 2 {
		t.Errorf("лишние группы: стран %d, жанров %d", len(countries), len(genres))
	}
}

// Записи, созданные до разделения осей, должны получить страну и жанр при
// загрузке — и уже разобранные каналы при этом не перетираться.
func TestMigrateTaxonomyLocked(t *testing.T) {
	r := &Registry{channels: []*RegChannel{
		{Name: "Первый канал", Group: "Русские"},
		{Name: "Balapan HD", Group: "Детские", TvgID: "Balapan.kz@HD"},
		{Name: "Нечто", Group: "Undefined"},
		// Оператор уже задал страну — её не перетираем, но жанр дозаполняем.
		{Name: "Ручной Sport", Group: "другие", Country: "Литовские"},
		// Прошлый разбор дал жанр, но страну взять было неоткуда; теперь она
		// видна по хосту источника — ось должна дозаполниться.
		{Name: "Карусель", Genre: "Детские",
			Pinned: []RegSource{{URL: "http://zabava-htlive.cdn.ngenix.net/hls/CH_KARUSEL/x.m3u8"}}},
	}}
	if n := r.migrateTaxonomyLocked(); n != 4 {
		t.Fatalf("изменено %d каналов, ожидалось 4", n)
	}
	if c := r.channels[0]; c.Country != "Русские" || c.Genre != "" {
		t.Errorf("Первый канал: (%q,%q)", c.Country, c.Genre)
	}
	if c := r.channels[1]; c.Country != "Казахские" || c.Genre != "Детские" {
		t.Errorf("Balapan: (%q,%q), ожидалось (Казахские,Детские)", c.Country, c.Genre)
	}
	if c := r.channels[2]; c.Country != "" || c.Genre != "" {
		t.Errorf("неклассифицируемый канал получил (%q,%q)", c.Country, c.Genre)
	}
	if c := r.channels[3]; c.Country != "Литовские" || c.Genre != "Спорт" {
		t.Errorf("ручной: (%q,%q), ожидалось (Литовские,Спорт)", c.Country, c.Genre)
	}
	if c := r.channels[4]; c.Country != "Русские" || c.Genre != "Детские" {
		t.Errorf("Карусель: (%q,%q), ожидалось (Русские,Детские)", c.Country, c.Genre)
	}
	// Повторный прогон ничего не меняет — миграция идемпотентна.
	if n := r.migrateTaxonomyLocked(); n != 0 {
		t.Errorf("повторная миграция изменила %d каналов", n)
	}
}

// Для части каналов домен вещателя — ЕДИНСТВЕННЫЙ признак страны: донор отдал
// их с жанровой группой и без tvg-id (НТВ, Карусель, Матч ТВ).
func TestCountryByHost(t *testing.T) {
	cases := map[string]string{
		"http://zabava-htlive.cdn.ngenix.net/hls/CH_MUZTV/variant.m3u8": "Русские",
		"https://edge1.1internet.tv/dash-live2/streams/1tv/x.mpd":       "Русские",
		"https://m3u8.video.matchtv.ru/media/playlist/x.m3u8":           "Русские",
		"http://edge50.dc.beltelecom.by/live/x.m3u8":                    "Белорусские",
		"http://stream8.cinerama.uz/x/index.m3u8":                       "Узбекские",
		"https://cdn.example.pl/live.m3u8":                              "Польские",
		// Домены, по которым страну определять НЕЛЬЗЯ: .tv это телевидение
		// вообще, .co и .me — «красивые» домены, а не Колумбия с Черногорией.
		"https://foo.example.tv/live.m3u8": "",
		"https://foo.example.co/live.m3u8": "",
		"https://foo.example.me/live.m3u8": "",
		"https://1.2.3.4:8080/live":        "",
		"":                                 "",
	}
	for in, want := range cases {
		if got := CountryByHost(in); got != want {
			t.Errorf("CountryByHost(%q) = %q, ожидалось %q", in, got, want)
		}
	}
}

func TestClassifyChannelUsesSources(t *testing.T) {
	// Матч ТВ: группа жанровая, tvg-id нет, URL пустой — страну выдаёт резолвер.
	c := &RegChannel{Name: "Матч ТВ", Group: "Спорт",
		Pinned: []RegSource{{Resolver: "matchtv"}}}
	if country, genre := ClassifyChannel(c); country != "Русские" || genre != "Спорт" {
		t.Errorf("Матч ТВ = (%q,%q), ожидалось (Русские,Спорт)", country, genre)
	}
	// Карусель: страна только по хосту Ростелекома.
	c = &RegChannel{Name: "Карусель", Group: "детские",
		Pinned: []RegSource{{URL: "http://zabava-htlive.cdn.ngenix.net/hls/CH_KARUSEL/variant.m3u8"}}}
	if country, genre := ClassifyChannel(c); country != "Русские" || genre != "Детские" {
		t.Errorf("Карусель = (%q,%q), ожидалось (Русские,Детские)", country, genre)
	}
	// Страновая группа сильнее хоста: оператор мог закрепить зеркало в другой стране.
	c = &RegChannel{Name: "Что-то", Group: "Литовские",
		Pinned: []RegSource{{URL: "http://x.cdn.ngenix.net/a.m3u8"}}}
	if country, _ := ClassifyChannel(c); country != "Литовские" {
		t.Errorf("страновая группа перебита хостом: %q", country)
	}
}
