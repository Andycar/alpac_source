package httpapi

import "testing"

// Реальные ссылки из прод-инцидента 2026-08-05: HDR отдавался прямым и ловил 403 у зрителя,
// SDR-рипы при этом работали прямыми месяцами.
func TestForceStreamProxyOnlyForFilmixFMP4(t *testing.T) {
	hdr := []string{
		"https://nl221.werkecdn.me/hls/hdr_018_rus/Project.Hail.Mary.2026.WEB-DL.2160p.HDR.dub.rus.mvo.ukr_2160.mp4/index.m3u8?hash=FH",
		"https://nl215.werkecdn.me/hls/HDR10p/Obsession.2025.D.ru.4K.WEBDL.HDR10p.DVP8.2160p_2160.mp4/init-v1-a1.mp4",
		"https://nl107.cdnsqu.com/hls/HEVC/Some.Film_2160.mp4/index.m3u8?hash=FH",
	}
	for _, u := range hdr {
		if !forceStreamProxy("filmix", u) {
			t.Errorf("HDR/fMP4 обязан проксироваться принудительно: %s", u)
		}
	}

	sdr := []string{
		"https://nl107.cdnsqu.com/hls/uhd_018/HomeAlone.1990.BDRip.SDR.2160p.ru_2160.mp4/index.m3u8?hash=FH",
		"https://nl107.cdnsqu.com/s/FH/uhd_018/Obsession.2025.WEB-DL.1080p.mp4",
	}
	for _, u := range sdr {
		if forceStreamProxy("filmix", u) {
			t.Errorf("SDR должен оставаться прямым (иначе теряем смысл no_stream_proxy): %s", u)
		}
	}

	// ★Ложное срабатывание из прод-прогона: озвучка «HDrezka» содержит hdr, но это обычный SDR-рип.
	if forceStreamProxy("filmix", "https://nl215.werkecdn.me/hls/Silo-2023-HDrezka/s03e05_720.mp4/index.m3u8?hash=FH") {
		t.Error("«HDrezka» в имени не делает рип HDR — не должен уходить через сервер")
	}

	// Правило узкое: только filmix, чужие балансёры не трогаем.
	if forceStreamProxy("vibix", hdr[0]) || forceStreamProxy("", hdr[0]) {
		t.Error("правило не должно распространяться на другие плагины")
	}
	if !forceStreamProxy("FiLmIx", hdr[0]) {
		t.Error("имя плагина сравнивается без учёта регистра")
	}
}

// 2026-08-22: раздел UHD_1313 отдал 940 сегментов, и НИ У ОДНОГО не было `?hash=` — плеер шёл без
// него и ловил 403. Сегмент с дописанным hash при этом отдавал 206, но дописать его можно лишь
// переписав манифест, а CDN привязывает сегменты к IP, который манифест скачал. Значит такие
// разделы обязаны идти через прокси целиком.
func TestForceStreamProxyForHashLessDirs(t *testing.T) {
	broken := "https://nl105.cdnsqu.com/hls/UHD_1313/Project.Hail.Mary.2026.MVO.ru.LostFilm.WEBDL.1080p_1080.mp4/index.m3u8?hash=FH.sig"
	if !forceStreamProxy("filmix", broken) {
		t.Fatal("UHD_1313 обязан идти через прокси")
	}
	// Исправные разделы того же тайтла остаются прямыми — они дописывают hash сами.
	for _, ok := range []string{
		"https://nl221.werkecdn.me/hls/hd_ukr/Film_1080.mp4/index.m3u8?hash=FH.sig",
		"https://nl105.cdnsqu.com/hls/HD_45/Film_2160.mp4/index.m3u8?hash=FH.sig",
	} {
		if forceStreamProxy("filmix", ok) {
			t.Errorf("исправный раздел не должен уходить через сервер: %s", ok)
		}
	}
	// Правило по-прежнему только для filmix.
	if forceStreamProxy("vibix", broken) {
		t.Error("чужие балансёры не трогаем")
	}
}
