package torrbalancer

import (
	"crypto/md5"
	"encoding/base64"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// DirectLink строит подписанную ссылку, по которой зритель забирает поток с
// СОБСТВЕННОГО TLS-фронта бэкенда (nginx), а не тянет его через main.
//
// Зачем. Каждый байт торрента сегодня пересекает main дважды: бэкенд→main→зритель.
// Замер 2026-09-14: ~107 Мбит/с с бэкендов и столько же наружу — 9% трафика main при
// том, что канал уже согнут /proxy (1.7 Гбит/с, 0.86% ретрансмитов). Прямая отдача
// снимает двойной транзит целиком; на бэкендах TorrServer остаётся на 127.0.0.1:8090
// за nginx, наружу торчит только подписанный /stream.
//
// Контракт с nginx на бэкенде (deploy/ts-direct.conf):
//
//	secure_link     $arg_md5,$arg_expires;
//	secure_link_md5 "$secure_link_expires$uri$arg_link$arg_index <secret>";
//
// md5 → base64url без '='. Две ловушки, из-за которых подпись легко разъехать:
//   - $uri у nginx — ДЕКОДИРОВАННЫЙ путь («/stream/Film Name.mkv»), а не то, что было
//     в строке запроса. Поэтому подписываем path как есть (в Go r.URL.Path уже
//     раскодирован), а в саму ссылку кладём его закодированным;
//   - $arg_link / $arg_index — СЫРЫЕ значения из строки запроса, без раскодирования.
//     Поэтому подписываем ровно те закодированные байты, которые сами же и пишем в
//     URL, а не исходный магнет.
//
// Подписаны путь, магнет и индекс файла: владелец ссылки не может подменить торрент
// или файл, оставив подпись. Привязки к IP нет намеренно — у мобильных адрес меняется
// посреди фильма (см. proxylink). Срок — expires (unix), задаётся вызывающим.
func DirectLink(base, secret, path string, q url.Values, expires int64) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	link := url.QueryEscape(q.Get("link"))
	index := url.QueryEscape(q.Get("index"))

	sum := md5.Sum([]byte(strconv.FormatInt(expires, 10) + path + link + index + " " + secret))
	sig := base64.RawURLEncoding.EncodeToString(sum[:])

	var sb strings.Builder
	sb.WriteString(base)
	sb.WriteString((&url.URL{Path: path}).EscapedPath())
	sb.WriteByte('?')

	first := true
	add := func(k, v string, bare bool) {
		if !first {
			sb.WriteByte('&')
		}
		first = false
		sb.WriteString(url.QueryEscape(k))
		if !bare {
			sb.WriteByte('=')
			sb.WriteString(v)
		}
	}
	// link и index — первыми и в том виде, в каком подписаны.
	if q.Has("link") {
		add("link", link, false)
	}
	if q.Has("index") {
		add("index", index, false)
	}
	// Остальное (play, preload, …) — как пришло, в стабильном порядке. Служебные
	// параметры и наш auth-токен наружу не уезжают.
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		switch k {
		case "link", "index", "md5", "expires", "token":
			continue
		}
		for _, v := range q[k] {
			if v == "" {
				add(k, "", true) // &play — голый флаг, как шлёт клиент
			} else {
				add(k, url.QueryEscape(v), false)
			}
		}
	}
	add("md5", sig, false)
	add("expires", strconv.FormatInt(expires, 10), false)
	return sb.String()
}
