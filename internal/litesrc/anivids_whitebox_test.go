package litesrc

import (
	"fmt"
	"strings"
	"testing"
)

// Фикстуры плеера «Best player» (player.ladonyvesna2005.info): одна механика на
// все площадки, которые на нём сидят — RuDub, AniDub.

// Сезон на умершем плеере: iframe есть, но это cdnmovies, не anivids.
const anividsDeadPlayerFixture = `<div class="page__player tabs-block sect">
<div class="tabs-block__content"><iframe src="//cdnmovies.nl/vod/70751" width="610" height="370" allowfullscreen></iframe></div>
</div>`

const anividsMasterFixture = `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=3139300,RESOLUTION=1280x720
https://cdn5.anivids.link/vod9/s10/82ed926165ca970566b1515f5231079a/fhd.mp4/chunk.m3u8?md5=TAB6wjZfWrqkivyEvI9AGA&expires=1789468379
#EXT-X-STREAM-INF:BANDWIDTH=1639300,RESOLUTION=854x480
https://cdn5.anivids.link/vod9/s10/82ed926165ca970566b1515f5231079a/sd.mp4/chunk.m3u8?md5=F4_ceqHf3fyyXNdjZ8ZR0w&expires=1789468379
`

func anividsPlaylistFixture(season int) string {
	return fmt.Sprintf(`<script defer data-domain="player.example" event-filename="Vigil.s%02de01.HD1080p.WEBRip.Rus.RuDub.tv.mkv" src="/js/script.js"></script>
<div class="tabs-sel series-tab active">`+
		`<span data="s10/82ed926165ca970566b1515f5231079a" class="ourspan current" onclick=selser("s10/82ed926165ca970566b1515f5231079a") >1 серия</span>`+
		`<span data="s11/535c1be3d128c3ade2fe63e947e708e2" class="ourspan " onclick=selser("s11/535c1be3d128c3ade2fe63e947e708e2") >2 серия</span>`+
		`<span data="s9/e1f7ab6635e101208d087cb911fde35c" class="ourspan " onclick=selser("s9/e1f7ab6635e101208d087cb911fde35c") >3 серия</span>`+
		`</div>`, season)
}

func TestAnividsParseIframe(t *testing.T) {
	html := `<div><iframe src="https://player.example.info/index.php?v=/s12/21809713df7c4d7590bbe0bba92e419a" width="610"></iframe></div>`
	player, hash, ok := anividsParseIframe(html)
	if !ok || player != "https://player.example.info" || hash != "s12/21809713df7c4d7590bbe0bba92e419a" {
		t.Fatalf("iframe -> %q %q ok=%v", player, hash, ok)
	}

	// Мёртвые плееры отсеиваются: играть по ним нечего.
	if _, _, ok := anividsParseIframe(anividsDeadPlayerFixture); ok {
		t.Fatal("cdnmovies iframe accepted")
	}
	if _, _, ok := anividsParseIframe(`<iframe src="http://hdgo.cc/video/t/5722j5rm82pxdlfzn"></iframe>`); ok {
		t.Fatal("hdgo iframe accepted")
	}
}

func TestAnividsParsePlaylist(t *testing.T) {
	eps := anividsParsePlaylist(anividsPlaylistFixture(3))
	if len(eps) != 3 {
		t.Fatalf("episodes = %d, want 3", len(eps))
	}
	if eps[0].num != 1 || eps[0].hash != "s10/82ed926165ca970566b1515f5231079a" {
		t.Fatalf("first episode = %+v", eps[0])
	}
	if eps[2].num != 3 || eps[2].hash != "s9/e1f7ab6635e101208d087cb911fde35c" {
		t.Fatalf("third episode = %+v", eps[2])
	}
}

func TestAnividsQualityFromRelease(t *testing.T) {
	cases := map[string]string{
		"Vigil.s03e06.HD1080p.WEBRip.Rus.RuDub.tv.mkv": "1080p",
		"Destan.s01e28.HD720p.WEBRip.Rus.RuDub.tv.mkv": "720p",
		"Show.s01e01.WEBRip.XviD.Rus.RuDub.tv.avi":     "",
		"Show.s02e02.HD2160p.WEB-DL.Rus.RuDub.tv.mkv":  "2160p",
	}
	for in, want := range cases {
		if got := anividsQualityFromRelease(in); got != want {
			t.Fatalf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestAnividsParseVariants(t *testing.T) {
	// RESOLUTION в плейлисте занижен (1280x720 у файла, который на деле
	// 1920x1080) — метка обязана прийти из релиза, а не из манифеста.
	vs := anividsParseVariants(anividsMasterFixture, "1080p")
	if len(vs) != 2 {
		t.Fatalf("variants = %d, want 2", len(vs))
	}
	if vs[0].label != "1080p" || !strings.Contains(vs[0].url, "/fhd.mp4/") {
		t.Fatalf("top variant = %+v", vs[0])
	}
	if vs[1].label != "480p" || !strings.Contains(vs[1].url, "/sd.mp4/") {
		t.Fatalf("second variant = %+v", vs[1])
	}

	// Релиз 720p: верхний вариант не должен называться 1080p.
	vs = anividsParseVariants(anividsMasterFixture, "720p")
	if vs[0].label != "720p" {
		t.Fatalf("720p release top variant = %+v", vs[0])
	}
}
