package litesrc

import (
	stdjson "encoding/json"
	"strconv"
	"testing"
)

// A trimmed but structurally faithful slice of a real tomion /lat/<id> SPA embed
// (2026-06 format): the search-result anchor, the iframe data-src, the kp_id, and
// the window.playerData object (voices map + serial.list[season][episode]).
const kubikvkubeFixture = `
<a href="https://kubikvkube.com/series/322-half_man.html" class="result">Half Man</a>
<iframe data-src="//tomion.org/lat/21207?season=1&episode=1&voice=10&adult_mode=2" frameborder="0"></iframe>
<script>window.adsConfig={"kp_id":7919289,"imdb_id":32767294};</script>
<script>window.playerData = {"voices":{"10":"Кубик в Кубе","4":"ColdFilm"},"config":{"video":"https:\/\/cdn8.tomion.org\/x\/aeBGvlgh\/1782818758\/2053296\/index.m3u8","video_id":2053296},"playlist":{"current":{"id":21207,"serialName":"Получеловек","contentType":0},"serial":{"list":[[{"num":1,"voices":[{"video_id":2053296,"voice_id":10},{"video_id":2021530,"voice_id":4}]},{"num":2,"voices":[{"video_id":2137150,"voice_id":10},{"video_id":2061829,"voice_id":4}]}]]}}};</script>
`

func TestKubikvkubeSearchLinkRe(t *testing.T) {
	m := kubikvkubeSearchLinkRe.FindStringSubmatch(kubikvkubeFixture)
	if len(m) < 2 {
		t.Fatalf("search link not matched")
	}
	if got, want := m[1], "https://kubikvkube.com/series/322-half_man.html"; got != want {
		t.Fatalf("link: got %q want %q", got, want)
	}
}

func TestKubikvkubeTomionIDRe(t *testing.T) {
	if got := submatch1(kubikvkubeTomionIDRe, kubikvkubeFixture); got != "21207" {
		t.Fatalf("tomion id: got %q want 21207", got)
	}
}

func TestKubikvkubeHLSExtract(t *testing.T) {
	raw := submatch1(kubikvkubeHLSRe, kubikvkubeFixture)
	got := kubikvkubeUnescapeURL(raw)
	want := "https://cdn8.tomion.org/x/aeBGvlgh/1782818758/2053296/index.m3u8"
	if got != want {
		t.Fatalf("hls: got %q want %q", got, want)
	}
}

func TestKubikvkubeKpID(t *testing.T) {
	if got := submatch1(kubikvkubeKpIDRe, kubikvkubeFixture); got != "7919289" {
		t.Fatalf("kp_id: got %q want 7919289", got)
	}
}

func TestKubikvkubePlayerDataParse(t *testing.T) {
	raw := kubikvkubeExtractPlayerData(kubikvkubeFixture)
	if raw == "" {
		t.Fatalf("playerData not extracted")
	}
	var pd kubikvkubePlayerData
	if err := stdjson.Unmarshal([]byte(raw), &pd); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	pl := kubikvkubePlaylist{seasons: map[int]map[int][]kubikvkubeItem{}}
	for sIdx, eps := range pd.Playlist.Serial.List {
		season := sIdx + 1
		epMap := map[int][]kubikvkubeItem{}
		for _, ep := range eps {
			var items []kubikvkubeItem
			for _, v := range ep.Voices {
				items = append(items, kubikvkubeItem{
					VideoID: v.VideoID, Season: season, Episode: ep.Num,
					VoiceID: v.VoiceID, VoiceName: pd.Voices[strconv.Itoa(v.VoiceID)],
				})
			}
			epMap[ep.Num] = items
		}
		pl.seasons[season] = epMap
	}

	if seasons := pl.sortedSeasons(); len(seasons) != 1 || seasons[0] != 1 {
		t.Fatalf("seasons: %v", seasons)
	}
	if eps := pl.sortedEpisodes(1); len(eps) != 2 || eps[0] != 1 || eps[1] != 2 {
		t.Fatalf("episodes: %v", eps)
	}
	order, names := pl.voicesForSeason(1)
	if len(order) != 2 || order[0] != 10 || names[10] != "Кубик в Кубе" || names[4] != "ColdFilm" {
		t.Fatalf("voices: order=%v names=%v", order, names)
	}
	if !pl.hasVoice(1, 2, 4) || pl.hasVoice(1, 2, 999) {
		t.Fatalf("hasVoice wrong")
	}
}
