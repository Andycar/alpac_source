package litesrc

import "testing"

// Замер 2026-08-06 («Обсессия», postid 183311): api-fx отдаёт 5 озвучек без HEVC-рипа, а легаси —
// служебную запись «Заблокировано правообладателем!» и эксклюзивный «HEVC 4K AC3 2CH DUB RU UKR».
func TestFilmixExtraMoviesPicksOnlyMissing(t *testing.T) {
	fx := []fxMovie{
		{Voiceover: "Дубляж [4K, SDR, ru, HDrezka 18+]"},
		{Voiceover: "MVO [4K, SDR, Ukr, DniproFilm]"},
		{Voiceover: "Дубляж [4K, HDR10+, ru, Ukr]"},
	}
	legacy := []filmixMovie{
		{Translation: "Заблокировано правообладателем!"},
		{Translation: "HEVC 4K AC3 2CH DUB RU UKR"},
		{Translation: "дубляж [4K, SDR, ru, HDrezka 18+]"}, // тот же, другой регистр
	}

	got := filmixExtraMovies(fx, legacy)
	if len(got) != 1 || got[0].Translation != "HEVC 4K AC3 2CH DUB RU UKR" {
		t.Fatalf("ожидался только эксклюзивный HEVC, получено %+v", got)
	}
}

func TestFilmixExtraMoviesEdgeCases(t *testing.T) {
	// Служебные записи — не озвучки, показывать их нельзя ни при каких условиях.
	for _, n := range []string{"Заблокировано правообладателем!", "Видео заблокировано!", "  ", ""} {
		if !filmixIsBlockedName(n) {
			t.Errorf("%q должно распознаваться как служебная запись", n)
		}
	}
	if filmixIsBlockedName("HEVC 4K AC3 2CH DUB RU UKR") {
		t.Error("настоящая озвучка не должна отсеиваться")
	}
	// Пустой легаси-ответ (он часто закрыт по правам) не должен ничего добавлять.
	if got := filmixExtraMovies([]fxMovie{{Voiceover: "A"}}, nil); got != nil {
		t.Fatalf("пустой легаси → пусто, получено %+v", got)
	}
	// Если api-fx пуст, легаси-озвучки берём целиком (кроме служебных).
	got := filmixExtraMovies(nil, []filmixMovie{{Translation: "HEVC"}, {Translation: "Заблокировано правообладателем!"}})
	if len(got) != 1 || got[0].Translation != "HEVC" {
		t.Fatalf("ожидалась одна озвучка, получено %+v", got)
	}
}

// ★Легаси-подпись прав не несёт: тот же путь с ней отдаёт заглушку 10 871 558 Б, а с нашей
// api-fx-подписью — настоящие 9 744 877 207 Б (замер на HEVC-рипе «Обсессии» 2026-08-06).
func TestFilmixSignExtraUsesOurHash(t *testing.T) {
	legacy := "https://nl210.cdnsqu.com/s/FHlegacyNORIGHTS.sig/HEVC/Obsession.2025.4K.SDR.WEBDL.HEVC.2160p_[2160,,,,,].mp4"
	got := filmixSignExtra(legacy, "FHours.sig")
	if len(got) != 1 {
		t.Fatalf("ожидалось одно качество, получено %d: %+v", len(got), got)
	}
	want := "https://nl210.cdnsqu.com/s/FHours.sig/HEVC/Obsession.2025.4K.SDR.WEBDL.HEVC.2160p_2160.mp4"
	if got[0]["url"] != want {
		t.Fatalf("подпись не заменена:\n got %s\nwant %s", got[0]["url"], want)
	}
	if got[0]["quality"] != "2160p" {
		t.Fatalf("качество: got %s", got[0]["quality"])
	}

	// Несколько качеств — по убыванию, как в остальной выдаче.
	multi := filmixSignExtra("https://h/s/FHold.s/dir/Film_[480,720,1080].mp4", "FHnew.s")
	if len(multi) != 3 || multi[0]["quality"] != "1080p" || multi[2]["quality"] != "480p" {
		t.Fatalf("порядок качеств нарушен: %+v", multi)
	}

	// Без подписи или без шаблона качеств ссылку не выдумываем.
	if filmixSignExtra(legacy, "") != nil {
		t.Fatal("без нашей подписи строить нечего")
	}
	if filmixSignExtra("https://h/s/FHold.s/dir/Film_2160.mp4", "FHnew.s") != nil {
		t.Fatal("без шаблона качеств имя файла не угадываем")
	}
	if filmixSignExtra("https://h/hls/dir/f.mp4/index.m3u8?hash=X", "FHnew.s") != nil {
		t.Fatal("HLS-форма сюда не относится")
	}
}

func TestFilmixFXHashExtraction(t *testing.T) {
	movies := []fxMovie{
		{Voiceover: "A", Files: []fxFile{{Quality: 1080, URL: "https://h/hls/dir/f_1080.mp4/index.m3u8?hash=FHsig.abc"}}},
	}
	if got := filmixFXHash(movies); got != "FHsig.abc" {
		t.Fatalf("подпись не извлечена: %q", got)
	}
	if got := filmixFXHash(nil); got != "" {
		t.Fatalf("пусто → пусто, получено %q", got)
	}
}

// Пока api-fx лежит (2026-08-06: TLS проходит, HTTP-ответа нет), access протухает и премиум-рипы
// отдают заглушку «купите премиум». Показывать такую строку зрителю нельзя.
func TestHideUnplayableWithoutRights(t *testing.T) {
	noRights := &filmixChecker{fxUser: "u", fxPasswd: "p"} // access пуст → прав нет
	noRights.fxStateLoaded = true

	for _, c := range []struct{ name, link string }{
		{"HEVC 4K AC3 2CH DUB RU UKR", "https://h/s/FH/HEVC/Film_[2160].mp4"},
		{"Дубляж [4K, HDR10+, ru, Ukr]", "https://h/s/FH/HDR10p/Film_[2160].mp4"},
	} {
		if !noRights.filmixHideUnplayable(c.name, c.link) {
			t.Errorf("без прав %q обязан скрываться", c.name)
		}
	}

	// Обычные рипы скрывать нельзя — они играют и без подписки.
	for _, c := range []struct{ name, link string }{
		{"MVO [4K, SDR, ru, HDrezka]", "https://h/s/FH/HD_45/Film_[2160].mp4"},
		{"Дубляж [1080+, ru, Paragraph Media, BDRip]", "https://h/s/FH/hd/Film_[1080].mp4"},
	} {
		if noRights.filmixHideUnplayable(c.name, c.link) {
			t.Errorf("%q скрывать нельзя — он играет без прав", c.name)
		}
	}

	// С подтверждёнными правами не скрываем ничего.
	ok := &filmixChecker{fxUser: "u", fxPasswd: "p", fxHash: "h", fxAccess: "a", fxAccessHash: "h"}
	ok.fxStateLoaded = true
	if ok.filmixHideUnplayable("HEVC 4K AC3 2CH DUB RU UKR", "https://h/s/FH/HEVC/F.mp4") {
		t.Fatal("при живых правах HEVC обязан показываться")
	}
	// Мёртвая сессия = прав нет, даже если токен в памяти остался.
	ok.fxHashDead = true
	if !ok.filmixHideUnplayable("HEVC 4K", "https://h/s/FH/HEVC/F.mp4") {
		t.Fatal("мёртвая сессия не даёт прав")
	}
}
