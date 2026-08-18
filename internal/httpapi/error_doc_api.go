package httpapi

import (
	"crypto/rand"
	"math/big"
	"net/http"
	"strings"
)

const acbAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

func errorAccsdbHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		shared := randomLowerAlnum(8)
		pw1 := randomLowerAlnum(6)
		pw2 := randomLowerAlnum(8)
		host := hostFromRequest(r)

		html := `<!DOCTYPE html>
<html lang='ru'>
<head>
  <meta charset='UTF-8'>
  <meta name='viewport' content='width=device-width, initial-scale=1.0'>
  <title>Настройка AccsDB</title>
  <link href='/control/npm/bootstrap.min.css' rel='stylesheet'>
</head>
<body>
  <div class='container mt-5'>
    <div class='card mt-4'>
      <div class='card-body'>
        <p class='card-text'>Добавьте в init.conf заменив email/unic_id на свои:</p>
        <pre style='background:#e9ecef;padding:1em;'><code>"accsdb": {
  "accounts": {
    "` + pw1 + `@mail.ru": "2040-10-17T00:00:00",
    "` + pw2 + `": "2040-10-17T00:00:00"
  }
}</code></pre>
        <p class='card-text'>Или через <a href='/admin' target='_blank'>` + host + `/admin</a></p>
      </div>
    </div>
  </div>
  <div class='container mt-5'>
    <div class='card mt-4'>
      <div class='card-body'>
        <p class='card-text'>Пароль общего доступа:</p>
        <pre style='background:#e9ecef;padding:1em;'><code>"accsdb": {
  "shared_passwd": "` + shared + `"
}</code></pre>
      </div>
    </div>
  </div>
</body>
</html>`

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(html))
	}
}

func randomLowerAlnum(n int) string {
	if n <= 0 {
		return ""
	}
	var sb strings.Builder
	sb.Grow(n)
	max := big.NewInt(int64(len(acbAlphabet)))
	for i := range n {
		v, err := rand.Int(rand.Reader, max)
		if err != nil {
			sb.WriteByte(acbAlphabet[i%len(acbAlphabet)])
			continue
		}
		sb.WriteByte(acbAlphabet[v.Int64()])
	}
	return sb.String()
}
