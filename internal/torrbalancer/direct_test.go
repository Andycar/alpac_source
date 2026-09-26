package torrbalancer

import (
	"net/url"
	"strings"
	"testing"
)

// Золотой вектор посчитан независимо от кода (openssl md5 -binary | base64 | tr '+/' '-_'):
//
//	printf '1800000000/stream/Film Name.mkvmagnet%%3A%%3Fxt%%3Durn%%3Abtih%%3Aabc2 s3cret'
//
// Именно эту строку соберёт nginx из "$secure_link_expires$uri$arg_link$arg_index s3cret":
// $uri — раскодированный путь с пробелом, $arg_link — сырое закодированное значение.
// Если тест сломался — разъехался контракт с nginx, а не «просто хеш».
func TestDirectLinkMatchesNginxSecureLink(t *testing.T) {
	q := url.Values{}
	q.Set("link", "magnet:?xt=urn:btih:abc")
	q.Set("index", "2")
	q.Set("play", "")

	got := DirectLink("https://torr.example.com/", "s3cret", "/stream/Film Name.mkv", q, 1800000000)

	want := "https://torr.example.com/stream/Film%20Name.mkv?link=magnet%3A%3Fxt%3Durn%3Abtih%3Aabc&index=2&play&md5=n5FNTxZGPRvFm5P2YTMsEQ&expires=1800000000"
	if got != want {
		t.Fatalf("ссылка разошлась с контрактом nginx:\n got  %s\n want %s", got, want)
	}
}

// Подпись покрывает и путь, и торрент, и файл — подмена любого из них ломает её.
func TestDirectLinkSignatureBindsPathLinkAndIndex(t *testing.T) {
	base := func(mod func(q url.Values) (string, url.Values)) string {
		q := url.Values{}
		q.Set("link", "magnet:?xt=urn:btih:abc")
		q.Set("index", "2")
		path, q := mod(q)
		u, err := url.Parse(DirectLink("https://x", "k", path, q, 42))
		if err != nil {
			t.Fatal(err)
		}
		return u.Query().Get("md5")
	}
	ref := base(func(q url.Values) (string, url.Values) { return "/stream/a.mkv", q })
	if ref == "" {
		t.Fatal("подписи нет")
	}
	if s := base(func(q url.Values) (string, url.Values) { return "/stream/b.mkv", q }); s == ref {
		t.Fatal("смена пути не изменила подпись")
	}
	if s := base(func(q url.Values) (string, url.Values) {
		q.Set("link", "magnet:?xt=urn:btih:zzz")
		return "/stream/a.mkv", q
	}); s == ref {
		t.Fatal("смена торрента не изменила подпись")
	}
	if s := base(func(q url.Values) (string, url.Values) { q.Set("index", "3"); return "/stream/a.mkv", q }); s == ref {
		t.Fatal("смена файла не изменила подпись")
	}
}

// Наш auth-токен (?token= у webOS) и служебные параметры наружу не уезжают, а
// прочие флаги клиента (play, preload) доезжают до TorrServer как были.
func TestDirectLinkDropsTokenKeepsPlayerFlags(t *testing.T) {
	q := url.Values{}
	q.Set("link", "m")
	q.Set("index", "1")
	q.Set("token", "SECRET-DEVICE-TOKEN")
	q.Set("md5", "stale")
	q.Set("expires", "1")
	q.Set("preload", "")
	got := DirectLink("https://x", "k", "/stream/a.mkv", q, 99)
	if strings.Contains(got, "SECRET-DEVICE-TOKEN") || strings.Contains(got, "token=") {
		t.Fatalf("токен зрителя утёк в прямую ссылку: %s", got)
	}
	if strings.Contains(got, "md5=stale") || strings.Contains(got, "expires=1&") {
		t.Fatalf("старая подпись из запроса пролезла в ссылку: %s", got)
	}
	if !strings.Contains(got, "&preload&") && !strings.HasSuffix(got, "&preload") && !strings.Contains(got, "&preload&md5=") {
		t.Fatalf("флаг preload потерян: %s", got)
	}
	if !strings.HasSuffix(got, "&expires=99") {
		t.Fatalf("expires не последний или не тот: %s", got)
	}
}
