package litesrc

import (
	"context"
	"testing"
)

func TestFilmixToProgressive(t *testing.T) {
	in := "https://nl107.cdnsqu.com/hls/uhd_018/HomeAlone.1990.BDRip.SDR.2160p.ru_2160.mp4/index.m3u8?hash=FHabc.def"
	want := "https://nl107.cdnsqu.com/s/FHabc.def/uhd_018/HomeAlone.1990.BDRip.SDR.2160p.ru_2160.mp4"
	if got := filmixToProgressive(in); got != want {
		t.Fatalf("деривация:\n got %s\nwant %s", got, want)
	}
	// Не та форма — пусто: ссылку не выдумываем, остаёмся на HLS.
	for _, bad := range []string{"https://x/s/H/dir/f.mp4", "https://x/hls/dir/f.mp4/index.m3u8", ""} {
		if got := filmixToProgressive(bad); got != "" {
			t.Fatalf("ожидалась пустая строка для %q, got %s", bad, got)
		}
	}
	// Маркеры заглушек: CDN отвечает 206 + video/mp4, отличить можно только по размеру.
	if !filmixIsStubSize(10871558) || !filmixIsStubSize(729935706) {
		t.Fatal("заглушки должны распознаваться")
	}
	if filmixIsStubSize(5585896082) {
		t.Fatal("настоящий HDR10+ файл принят за заглушку")
	}
}

func TestProgressiveRequiresCredsAndExplicitFlag(t *testing.T) {
	if (&filmixChecker{}).progressiveEnabled() {
		t.Fatal("без кредов режим невозможен: анонимный hash даёт заглушку")
	}
	f := &filmixChecker{fxUser: "u", fxPasswd: "p"}
	if f.progressiveEnabled() {
		t.Fatal("по умолчанию выключен")
	}
	on := true
	f.fxProgressive = &on
	if !f.progressiveEnabled() {
		t.Fatal("явное true включает")
	}
	if (&filmixChecker{fxProgressive: &on}).progressiveEnabled() {
		t.Fatal("без кредов не включается даже явным true")
	}
}

// ★Ключевое свойство: сервер НЕ обращается к отдаваемым ссылкам. Обращение забрало бы выдачу себе —
// CDN отдаёт подписанный файл первому обратившемуся, а остальным 403/429. Именно так probe ломал
// воспроизведение у зрителей. Тест это фиксирует: клиент не сконфигурирован, и любой сетевой вызов
// уронил бы его паникой на nil-транспорте.
func TestProgressiveStreamsNeverTouchesCDN(t *testing.T) {
	on := true
	f := &filmixChecker{fxUser: "u", fxPasswd: "p", fxProgressive: &on} // client == nil

	streams := []map[string]string{
		{"quality": "2160p", "url": "https://nl215.werkecdn.me/hls/HDR10p/Obsession_2160.mp4/index.m3u8?hash=FH1.sig"},
		{"quality": "1080p", "url": "https://nl215.werkecdn.me/hls/HDR10p/Obsession_1080.mp4/index.m3u8?hash=FH1.sig"},
	}
	out, ok := f.progressiveStreams(context.Background(), streams)
	if !ok || len(out) != 2 {
		t.Fatalf("ожидалась конверсия обоих качеств, got ok=%v n=%d", ok, len(out))
	}
	for i, s := range out {
		if got := s["url"]; got != filmixToProgressive(streams[i]["url"]) {
			t.Fatalf("ссылка %d не сконвертирована: %s", i, got)
		}
	}

	// Хоть одна ссылка не той формы — откат на HLS целиком, без частичной выдачи.
	mixed := []map[string]string{streams[0], {"quality": "720p", "url": "https://host/s/FH/dir/f.mp4"}}
	if _, ok := f.progressiveStreams(context.Background(), mixed); ok {
		t.Fatal("смешанный набор должен целиком оставаться на HLS")
	}
}

// Прогрессив теперь для ВСЕХ рипов (как у чужого плагина): у fMP4 это единственный рабочий путь,
// у TS — просто лучше (один файл, seek по Range).
func TestProgressiveCoversAllRips(t *testing.T) {
	on := true
	f := &filmixChecker{fxUser: "u", fxPasswd: "p", fxProgressive: &on}

	for _, u := range []string{
		"https://nl107.cdnsqu.com/hls/uhd_018/Film.2160p.SDR_2160.mp4/index.m3u8?hash=FH.sig",
		"https://nl215.werkecdn.me/hls/Silo-2023-HDrezka/s03e05_1080.mp4/index.m3u8?hash=FH.sig",
		"https://nl215.werkecdn.me/hls/HDR10p/Obsession.4K.WEBDL.HDR10p.DVP8.2160p_2160.mp4/index.m3u8?hash=FH.sig",
		"https://nl221.werkecdn.me/hls/hdr_018_rus/Project.Hail.Mary.2160p.HDR_2160.mp4/index.m3u8?hash=FH.sig",
		"https://nl107.cdnsqu.com/hls/HEVC/Some.Film_2160.mp4/index.m3u8?hash=FH.sig",
	} {
		if _, ok := f.progressiveStreams(context.Background(), []map[string]string{{"quality": "2160p", "url": u}}); !ok {
			t.Fatalf("рип обязан идти прогрессивом: %s", u)
		}
	}
}
