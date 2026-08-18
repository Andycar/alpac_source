package litesrc

import (
	"strings"
	"testing"
)

// Real payloads captured from the live site (2026-07-27).

// anwapFilmPayload: a film page. Its junk tokens are all contiguous, so even a
// naive single-pass strip happens to decode it — which is exactly why a
// single-pass port looks correct until it meets a serial.
const anwapFilmPayload = `<div><script>var pl={"file":"#2aHR0cHM6Ly9vMS5hbndhcC5iZS9vaGxzL0FBQS8xLzIubTN1OCBvciBodHRwczovL28xLmFud2FwLmJlL29uL0FBQS8xLzIubXA0"};</script></div>`

// anwapEpisodePayload: a serial episode. Here "RmtpVTdoRw==" is SPLIT by an
// inner "RXJTdzNBc2k=" — removing the inner token is what makes the outer one
// contiguous. Stripping once leaves the outer token behind, it decodes as
// payload, and the URL comes out truncated.
const anwapEpisodePayload = `"file":"#2a//WXQ2cmpGZA==HR0cHM6Ly9tLmFud2FwLm1lZG//UlRkM1M2NUZnlhL3Nlcmlh//Rmt//RXJTdzNBc2k=pVTdoRw==bHMvcGxheWxpc3RfOTkwOTFfaC50eHQ="`

func TestAnwapDecodeFilm(t *testing.T) {
	got := anwapDecodeFile(anwapFilmPayload)
	if len(got) != 2 {
		t.Fatalf("want 2 mirrors, got %d: %v", len(got), got)
	}
	if !strings.HasSuffix(got[0], ".m3u8") {
		t.Errorf("first mirror should be the HLS one, got %q", got[0])
	}
	if !strings.HasSuffix(got[1], ".mp4") {
		t.Errorf("second mirror should be the progressive one, got %q", got[1])
	}
}

// TestAnwapDecodeSplitJunkToken is the regression that matters: the strip must
// run to a fixed point, otherwise serial episodes silently yield a truncated
// URL (".../seriaFkiU7hG") that 404s.
func TestAnwapDecodeSplitJunkToken(t *testing.T) {
	got := anwapDecodeFile(anwapEpisodePayload)
	if len(got) != 1 {
		t.Fatalf("want 1 mirror, got %d: %v", len(got), got)
	}
	const want = "https://m.anwap.media/serials/playlist_99091_h.txt"
	if got[0] != want {
		t.Fatalf("split junk token not resolved:\n got %q\nwant %q", got[0], want)
	}
	if strings.Contains(got[0], "FkiU7hG") {
		t.Error("decoded URL still carries a junk token as payload")
	}
}

func TestAnwapDecodeRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "<html>no file here</html>", `"file":"#2!!!!not base64!!!!"`} {
		if got := anwapDecodeFile(in); got != nil {
			t.Errorf("expected nil for %q, got %v", in, got)
		}
	}
}

// anwapSearchFixture mirrors the card markup of a real search page.
const anwapSearchFixture = `<div class="my_razdel film">
	<a href="/films/45981"><div class="screenfilm"><img src="/films/prew/45981.jpg" alt="Секс-файлы / Sex Files" class="screenf"/></div>
	<div class="opisfilm"><div class="namefilm"> Секс-файлы</div> <span class="in green">DVDRip</span> <span class="in year">2000</span></div></div>
	<div class="my_razdel film">
	<a href="/films/17066"><div class="screenfilm"><img src="/films/prew/17066.jpg" alt="Матрица времени / Before I Fall" class="screenf"/></div>
	<div class="opisfilm"><div class="namefilm"> Матрица времени</div> <span class="in green">BDRip</span> <span class="in year">2017</span></div></div>`

func TestAnwapParseCards(t *testing.T) {
	cards := anwapParseCards(anwapSearchFixture)
	if len(cards) != 2 {
		t.Fatalf("want 2 cards, got %d: %+v", len(cards), cards)
	}
	byID := map[string]anwapCard{}
	for _, c := range cards {
		byID[c.id] = c
	}
	c, ok := byID["17066"]
	if !ok {
		t.Fatalf("card 17066 missing: %+v", cards)
	}
	if c.year != 2017 {
		t.Errorf("year = %d, want 2017", c.year)
	}
	if c.serial {
		t.Error("film card flagged as serial")
	}
	if !strings.Contains(c.name, "Матрица времени") {
		t.Errorf("name = %q", c.name)
	}
}

// TestAnwapPickCardUsesYear: the catalogue is large and title-only matching hits
// unrelated rows — "Матрица" alone returns 11, including adult titles. A wrong
// year must never be accepted.
func TestAnwapPickCardUsesYear(t *testing.T) {
	cards := anwapParseCards(anwapSearchFixture)

	got, ok := anwapPickCard(cards, "Матрица времени", "Before I Fall", 2017)
	if !ok || got.id != "17066" {
		t.Fatalf("exact title+year should pick 17066, got %+v ok=%v", got, ok)
	}

	// Same title, wrong year — the only year-compatible row is a different film,
	// so nothing may be returned.
	if got, ok := anwapPickCard(cards, "Матрица времени", "Before I Fall", 1999); ok {
		t.Fatalf("wrong year must not match, got %+v", got)
	}

	// Matching by the original title alone also works.
	if got, ok := anwapPickCard(cards, "", "Before I Fall", 2017); !ok || got.id != "17066" {
		t.Fatalf("original-title match failed: %+v ok=%v", got, ok)
	}
}

func TestAnwapPickCardNoYearNeedsTitleMatch(t *testing.T) {
	cards := anwapParseCards(anwapSearchFixture)
	// With no year to lean on, an unrelated title must not fall through to the
	// first row of the result page.
	if got, ok := anwapPickCard(cards, "Совершенно другой фильм", "", 0); ok {
		t.Fatalf("unrelated title matched without a year: %+v", got)
	}
}
