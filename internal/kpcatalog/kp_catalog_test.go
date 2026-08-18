package kpcatalog

import "testing"

// kpID must tolerate the two field names the KP API uses: v2.2 → kinopoiskId, v2.1 search → filmId.
// This is what lets capi's kinopoisk_id resolve (imdb→kp / title→kp) feed kp-only sources (hdvb/zetflix).
func TestKpFilmKpID(t *testing.T) {
	cases := []struct {
		name string
		film kpFilm
		want int
	}{
		{"v2.2 kinopoiskId", kpFilm{KinopoiskID: 384680}, 384680},
		{"v2.1 filmId", kpFilm{FilmID: 326}, 326},
		{"both set prefers kinopoiskId", kpFilm{KinopoiskID: 111, FilmID: 222}, 111},
		{"neither set", kpFilm{}, 0},
	}
	for _, c := range cases {
		if got := c.film.kpID(); got != c.want {
			t.Errorf("%s: kpID() = %d, want %d", c.name, got, c.want)
		}
	}
}
