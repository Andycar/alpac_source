package torrbalancer

import (
	"strings"
	"testing"
)

func TestExtractInfohash(t *testing.T) {
	const hexA = "0123456789abcdef0123456789abcdef01234567"
	const hexB = "fedcba9876543210fedcba9876543210fedcba98"
	b32 := strings.Repeat("a", 32) // valid base32 infohash

	cases := []struct {
		name string
		link string
		hash string
		want string
	}{
		{"magnet hex", "magnet:?xt=urn:btih:" + hexA + "&dn=movie", "", hexA},
		{"magnet hex uppercase", "magnet:?xt=urn:btih:" + strings.ToUpper(hexA), "", hexA},
		{"magnet base32", "magnet:?xt=urn:btih:" + strings.ToUpper(b32), "", b32},
		{"raw hex hash", "", hexA, hexA},
		{"raw hex hash uppercase", "", strings.ToUpper(hexA), hexA},
		{"hash beats link", "magnet:?xt=urn:btih:" + hexB, hexA, hexA},
		{"raw hex in link", hexA, "", hexA},
		{"torrent url", "http://site.example/file.torrent", "", "http://site.example/file.torrent"},
		{"torrent url strips fragment", "http://site.example/f.torrent#t=10", "", "http://site.example/f.torrent"},
		{"empty", "", "", ""},
		{"opaque hash passthrough", "", "weird-internal-id", "weird-internal-id"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ExtractInfohash(c.link, c.hash); got != c.want {
				t.Fatalf("ExtractInfohash(%q,%q) = %q, want %q", c.link, c.hash, got, c.want)
			}
		})
	}
}
