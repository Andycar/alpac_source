package litesrc

import (
	"bytes"
	"crypto/md5"
	stdjson "encoding/json"
	"fmt"
	"lampac-go/internal/httpclient"
	"net/http"
	"strings"
	"time"

	"lampac-go/internal/config"
)

type getsTVBindResponse struct {
	Token string `json:"token"`
}

func GetsTVBindHandler(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		login := strings.TrimSpace(r.URL.Query().Get("login"))
		pass := strings.TrimSpace(r.URL.Query().Get("pass"))

		if login == "" || pass == "" {
			writeHTML(w, http.StatusOK, `Введите данные аккаунта getstv.com <br> <br><form method="get" action="/lite/getstv/bind"><input type="text" name="login" placeholder="email"> &nbsp; &nbsp; <input type="text" name="pass" placeholder="пароль"><br><br><button>Авторизоваться</button></form>`)
			return
		}

		host := strings.TrimSpace(strings.TrimRight(cfg.Online.GetsTV.Host, "/"))
		if host == "" {
			http.Error(w, "GetsTV host is not configured", http.StatusBadGateway)
			return
		}
		if !strings.Contains(host, "://") {
			host = "https://" + host
		}

		fingerprint := fmt.Sprintf("%x", md5.Sum(fmt.Appendf(nil, "%d-%s", time.Now().UnixNano(), login)))
		payload := map[string]any{
			"email":       login,
			"password":    pass,
			"fingerprint": fingerprint,
			"device":      map[string]any{},
		}
		body, err := stdjson.Marshal(payload)
		if err != nil {
			http.Error(w, "serialize error", http.StatusInternalServerError)
			return
		}

		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, host+"/api/login", bytes.NewReader(body))
		if err != nil {
			http.Error(w, "request error", http.StatusBadGateway)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/plain, */*")
		req.Header.Set("User-Agent", "Mozilla/5.0")
		req.Header.Set("X-Lampac-Go", "1")

		client := httpclient.New(15 * time.Second)
		resp, err := client.Do(req)
		if err != nil {
			writeHTML(w, http.StatusOK, "Ошибка авторизации ;(")
			return
		}
		defer resp.Body.Close()

		var parsed getsTVBindResponse
		raw := map[string]any{}
		decoder := stdjson.NewDecoder(resp.Body)
		if err := decoder.Decode(&raw); err != nil {
			writeHTML(w, http.StatusOK, "Ошибка авторизации ;(")
			return
		}
		parsed.Token = strings.TrimSpace(toString(raw["token"]))

		if parsed.Token == "" {
			out, _ := stdjson.MarshalIndent(raw, "", "  ")
			writeHTML(w, http.StatusOK, string(out))
			return
		}

		writeHTML(w, http.StatusOK, `Добавьте в init.conf<br><br>"GetsTV": {<br>&nbsp;&nbsp;"enable": true,<br>&nbsp;&nbsp;"token": "`+parsed.Token+`"<br>}`)
	}
}
