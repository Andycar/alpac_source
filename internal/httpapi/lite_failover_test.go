package httpapi

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

// Добор на ноде срабатывает, только когда в ответе main нет контента. Ошибиться
// в любую сторону плохо: принять пустой ответ за полный — зритель не получит
// видео, принять полный за пустой — лишние запросы к нодам и чужой поток.
func TestLiteHasContent(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{`{}`, false},
		{`[]`, false},
		{`{"data":[]}`, false},
		{`{"rch":false}`, false},
		{``, false},
		{`   `, false},
		{`{"method":"play","url":"https://tv/proxy/tr_x","quality":{"720p":"u"}}`, true},
		{`{"type":"voice","data":[{"name":"Дубляж","url":"https://tv/lite/ahuerezka/movie?t=56"}]}`, true},
		{`{"method":"play","url":""}`, false},
		{`<div class="videos__line"><div class="videos__item" data-json="{}">Дубляж</div></div>`, true},
		{`<div class="videos__line"></div>`, false},
		{`не json и не html`, false},
	}
	for _, c := range cases {
		if got := liteHasContent([]byte(c.body)); got != c.want {
			t.Errorf("liteHasContent(%q) = %v, ожидали %v", c.body, got, c.want)
		}
	}
}

func TestLiteFailoverBalancer(t *testing.T) {
	for raw, want := range map[string]bool{
		"ahuerezka": true, "ahuerezka/movie": true, "ahuerezka/serial": true, "xsmart": true, "xsmart/season": true,
		"rezka": false, "kodik": false, "": false,
	} {
		if got := liteFailoverBalancer(raw); got != want {
			t.Errorf("liteFailoverBalancer(%q) = %v, ожидали %v", raw, got, want)
		}
	}
}

// Нода сжимает ответ клиентам с Accept-Encoding, а пересыл несёт байты как
// есть: проверка на контент обязана смотреть в распакованное тело. 21.09.2026
// на этом все gzip-клиенты (webOS, Tizen, Android) получали токены main
// вместо нод — xsmart стал 69 % байт main.
func TestLiteBodyForCheckDecodesCompressed(t *testing.T) {
	// Страница должна быть достаточно большой, чтобы deflate её РЕАЛЬНО сжал:
	// короткую строку gzip кладёт «как есть» (stored block), и «videos__item»
	// остаётся в сжатых байтах открытым текстом — проверка ничего не докажет.
	var sb strings.Builder
	sb.WriteString(`<div class="videos__line">`)
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&sb, `<div class="videos__item" data-json="{&quot;url&quot;:&quot;https://tv/lite/xsmart/play?tr=%d&quot;}">Перевод №%d студии %c</div>`, i, i, 'A'+i%26)
	}
	sb.WriteString(`</div>`)
	page := sb.String()
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write([]byte(page))
	zw.Close()
	var df bytes.Buffer
	dw := zlib.NewWriter(&df)
	dw.Write([]byte(page))
	dw.Close()

	mk := func(enc string, body []byte) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		if enc != "" {
			rec.Header().Set("Content-Encoding", enc)
		}
		rec.WriteHeader(200)
		rec.Body.Write(body)
		return rec
	}
	if !liteHasContent(liteBodyForCheck(mk("gzip", gz.Bytes()))) {
		t.Fatal("gzip-ответ ноды с контентом принят за пустой")
	}
	if !liteHasContent(liteBodyForCheck(mk("deflate", df.Bytes()))) {
		t.Fatal("deflate-ответ ноды с контентом принят за пустой")
	}
	if liteHasContent(gz.Bytes()) {
		t.Fatal("сырые gzip-байты не должны считаться контентом (иначе тест ничего не проверяет)")
	}
	if !liteHasContent(liteBodyForCheck(mk("", []byte(page)))) {
		t.Fatal("несжатый ответ")
	}
	if liteHasContent(liteBodyForCheck(mk("", []byte(`<div class="videos__line"></div>`)))) {
		t.Fatal("пустой несжатый ответ принят за контент")
	}
	if !liteHasContent(liteBodyForCheck(mk("br", []byte{1, 2, 3}))) {
		t.Fatal("неизвестную кодировку нельзя считать пустотой — ноду наказали бы зря")
	}
	if !liteHasContent(liteBodyForCheck(mk("gzip", []byte("not gzip at all")))) {
		t.Fatal("битый gzip — тоже не повод отдавать main")
	}
}
