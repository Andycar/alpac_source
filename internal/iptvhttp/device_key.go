package iptvhttp

import (
	"encoding/base64"
	"hash/fnv"
	"net/http"
	"strings"

	"lampac-go/internal/proxylink"
)

// device_key.go — опознание УСТРОЙСТВА внутри профиля для лимита одновременных
// потоков.
//
// Идеально было бы брать lampac_unic_id, но замер на проде показал: клиенты не
// шлют ?uid= на /api/iptv/play ни разу (0 из 9070 запросов). Поэтому ключ
// собирается из того, что есть в каждом запросе: User-Agent + сеть клиента.
//
// Что это даёт и чего не даёт:
//   - два одинаковых телевизора в ОДНОМ доме сольются в один слот. Это щедро к
//     абоненту и не эксплуатируется удалённо: чтобы попасть в тот же слот, надо
//     сидеть в той же сети, то есть в том же доме.
//   - тот же аккаунт из ДРУГОГО дома — другая сеть, другой слот, и лимит его
//     поймает. Ровно та раздача пароля, ради которой лимит и вводится.
//
// Когда клиенты научатся слать uid на /play, эта функция начнёт брать его —
// точность вырастет, менять ничего больше не придётся.

func deviceKey(r *http.Request) string {
	if uid := strings.TrimSpace(r.URL.Query().Get("uid")); uid != "" {
		return "uid:" + uid
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(r.UserAgent()))
	_, _ = h.Write([]byte{'|'})
	_, _ = h.Write([]byte(proxylink.NetPrefix(clientIP(r))))
	var b [8]byte
	v := h.Sum64()
	for i := 0; i < 8; i++ {
		b[i] = byte(v >> (8 * i))
	}
	return "fp:" + base64.RawURLEncoding.EncodeToString(b[:])
}
