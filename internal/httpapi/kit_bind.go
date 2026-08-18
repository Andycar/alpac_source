package httpapi

import (
	"crypto/md5"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"lampac-go/internal/httpclient"
	"lampac-go/internal/litesrc"
	"lampac-go/internal/sisihttp"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"

	"lampac-go/internal/config"
	"lampac-go/internal/kit"

	"github.com/rs/zerolog/log"
)

// ---- Filmix Bind ----

// kitBindFilmixStartHandler requests a device code from Filmix.
// POST /api/kit/bind/filmix/start
func kitBindFilmixStartHandler(store *kit.Store, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, err := kitAuthFromRequest(r, cfg)
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusUnauthorized)
			return
		}

		client := httpclient.New(10 * time.Second)

		// Зеркала гаснут поодиночке — единственный захардкоженный хост превращал частный сбой в
		// «filmix сломался» для всех. Идём по тем же зеркалам, что и балансёр.
		var (
			result   map[string]any
			lastErr  string
			upstream string
		)
		for _, host := range litesrc.FilmixAPIHosts(cfg.Online.Filmix.Host) {
			resp, err := client.Get(host + "/api/v2/token_request?user_dev_apk=2.2.0&user_dev_id=&user_dev_name=Xiaomi&user_dev_os=11&user_dev_vendor=Xiaomi&user_dev_token=")
			if err != nil {
				lastErr = "unavailable"
				continue
			}
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
			resp.Body.Close()

			var parsed map[string]any
			if err := json.Unmarshal(body, &parsed); err != nil {
				lastErr = "invalid response"
				continue
			}
			if uc, _ := parsed["user_code"].(string); uc != "" {
				result, upstream = parsed, host
				break
			}
			// Апстрим ответил 200 и текстом ошибки — доносим его до пользователя дословно,
			// иначе «did not return a code» не отличить от нашей поломки.
			if msg, _ := parsed["message"].(string); msg != "" {
				lastErr = msg
			}
		}

		if result == nil {
			log.Warn().Str("upstream_error", lastErr).Msg("kit: filmix token_request не выдал код")
			writeKitJSON(w, http.StatusBadGateway, fmt.Sprintf(`{"error":"filmix did not return a code","upstream":%q}`, lastErr))
			return
		}
		_ = upstream

		userCode, _ := result["user_code"].(string)
		code, _ := result["code"].(string)
		if code == "" {
			writeKitJSON(w, http.StatusBadGateway, `{"error":"filmix did not return a code"}`)
			return
		}

		writeKitJSON(w, http.StatusOK, fmt.Sprintf(`{"user_code":%q,"code":%q}`, userCode, code))
	}
}

// kitBindFilmixFinishHandler verifies the Filmix code and saves the token.
// POST /api/kit/bind/filmix/finish  body: {"code":"..."}
func kitBindFilmixFinishHandler(store *kit.Store, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID, _, err := kitAuthFromRequest(r, cfg)
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusUnauthorized)
			return
		}

		var req struct {
			Code string `json:"code"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil || req.Code == "" {
			writeKitJSON(w, http.StatusBadRequest, `{"error":"missing code"}`)
			return
		}

		client := httpclient.New(10 * time.Second)
		resp, err := client.Get("http://filmixapp.vip/api/v2/user_profile?app_lang=ru_RU&user_dev_apk=2.2.0&user_dev_id=&user_dev_name=Xiaomi&user_dev_os=11&user_dev_vendor=Xiaomi&user_dev_token=" + req.Code)
		if err != nil {
			writeKitJSON(w, http.StatusBadGateway, `{"error":"filmixapp.vip unavailable"}`)
			return
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		var profile map[string]any
		if err := json.Unmarshal(body, &profile); err != nil {
			writeKitJSON(w, http.StatusBadGateway, `{"error":"invalid filmix response"}`)
			return
		}

		if _, ok := profile["user_data"]; !ok {
			writeKitJSON(w, http.StatusBadRequest, fmt.Sprintf(`{"error":"token %s not found"}`, req.Code))
			return
		}

		pro := false
		if ud, ok := profile["user_data"].(map[string]any); ok {
			if p, _ := ud["is_pro"].(bool); p {
				pro = true
			}
			if p, _ := ud["is_pro_plus"].(bool); p {
				pro = true
			}
		}

		section := map[string]any{
			"enable": true,
			"token":  req.Code,
			"pro":    pro,
		}

		if err := store.UpdateSection(tgID, "Filmix", section); err != nil {
			log.Warn().Err(err).Str("tg_id", tgID).Msg("kit: save filmix bind failed")
			writeKitJSON(w, http.StatusInternalServerError, `{"error":"save failed"}`)
			return
		}

		writeKitJSON(w, http.StatusOK, fmt.Sprintf(`{"success":true,"pro":%t}`, pro))
	}
}

// ---- KinoPub Bind ----

// kitBindKinopubStartHandler requests a device code from KinoPub.
// POST /api/kit/bind/kinopub/start
//
// Uses the android-client OAuth credentials (see kinopub_token.go for
// rationale). Since v1.34-derived migration the same helpers drive
// both the web-UI activation page and this per-user bind endpoint, so
// the OAuth flow lives in one place and the response always includes
// refresh_token when the user finishes the dance.
func kitBindKinopubStartHandler(store *kit.Store, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _, err := kitAuthFromRequest(r, cfg)
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusUnauthorized)
			return
		}

		host := litesrc.KinopubAPIHost(cfg.Online.KinoPub.Host)
		client := httpclient.New(10 * time.Second)
		userCode, code, err := litesrc.KinopubRequestDeviceCode(r.Context(), client, host)
		if err != nil {
			writeKitJSON(w, http.StatusBadGateway, fmt.Sprintf(`{"error":%q}`, err.Error()))
			return
		}
		writeKitJSON(w, http.StatusOK, fmt.Sprintf(`{"user_code":%q,"code":%q}`, userCode, code))
	}
}

// kitBindKinopubFinishHandler verifies the KinoPub code and saves the
// token triple (access + refresh + expires_at) into the user's Kit
// section. The kinopub handler reads this section via
// kit.TokenOverride and refreshes the access_token transparently when
// it gets close to expiry — the user activates once and forgets.
//
// POST /api/kit/bind/kinopub/finish  body: {"code":"..."}
func kitBindKinopubFinishHandler(store *kit.Store, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID, _, err := kitAuthFromRequest(r, cfg)
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusUnauthorized)
			return
		}

		var req struct {
			Code string `json:"code"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil || req.Code == "" {
			writeKitJSON(w, http.StatusBadRequest, `{"error":"missing code"}`)
			return
		}

		host := litesrc.KinopubAPIHost(cfg.Online.KinoPub.Host)
		client := httpclient.New(10 * time.Second)
		tokens, err := litesrc.KinopubExchangeDeviceToken(r.Context(), client, host, req.Code)
		if err != nil {
			writeKitJSON(w, http.StatusBadRequest, fmt.Sprintf(`{"error":%q}`, err.Error()))
			return
		}

		section := map[string]any{
			"enable":        true,
			"token":         tokens.AccessToken,
			"refresh_token": tokens.RefreshToken,
		}
		if !tokens.ExpiresAt.IsZero() {
			section["expires_at"] = tokens.ExpiresAt.UTC().Format(time.RFC3339)
		}

		if err := store.UpdateSection(tgID, "KinoPub", section); err != nil {
			writeKitJSON(w, http.StatusInternalServerError, `{"error":"save failed"}`)
			return
		}

		writeKitJSON(w, http.StatusOK, `{"success":true}`)
	}
}

// ---- Rezka Bind ----

var rezkaCookieRe = regexp.MustCompile(`(dle_user_id|dle_password)=[^;]+`)

// kitBindRezkaHandler authenticates with HDRezka and saves cookies.
// POST /api/kit/bind/rezka  body: {"login":"...","password":"..."}
func kitBindRezkaHandler(store *kit.Store, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID, _, err := kitAuthFromRequest(r, cfg)
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusUnauthorized)
			return
		}

		var req struct {
			Login    string `json:"login"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil || req.Login == "" || req.Password == "" {
			writeKitJSON(w, http.StatusBadRequest, `{"error":"login and password required"}`)
			return
		}

		pidorezkaHost := strings.TrimSpace(cfg.Online.PidoRezka.Host)
		if pidorezkaHost == "" {
			pidorezkaHost = "https://hdrzk.org"
		}

		cookie, premium, err := kitRezkaLogin(pidorezkaHost, req.Login, req.Password)
		if err != nil {
			writeKitJSON(w, http.StatusBadGateway, fmt.Sprintf(`{"error":%q}`, err.Error()))
			return
		}

		sectionKey := "Rezka"
		if premium {
			sectionKey = "RezkaPrem"
		}

		section := map[string]any{
			"enable": true,
			"cookie": cookie,
		}

		if err := store.UpdateSection(tgID, sectionKey, section); err != nil {
			writeKitJSON(w, http.StatusInternalServerError, `{"error":"save failed"}`)
			return
		}

		// If premium, disable regular Rezka; if regular, disable premium.
		if premium {
			_ = store.UpdateSection(tgID, "Rezka", map[string]any{"enable": false})
		} else {
			_ = store.DeleteSection(tgID, "RezkaPrem")
		}

		writeKitJSON(w, http.StatusOK, fmt.Sprintf(`{"success":true,"premium":%t}`, premium))
	}
}

// kitBindRezkaCookieHandler saves manually provided Rezka cookies.
// POST /api/kit/bind/rezka/cookie  body: {"cookie":"dle_user_id=...;dle_password=...","premium":true}
func kitBindRezkaCookieHandler(store *kit.Store, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID, _, err := kitAuthFromRequest(r, cfg)
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusUnauthorized)
			return
		}

		var req struct {
			Cookie  string `json:"cookie"`
			Premium bool   `json:"premium"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil || req.Cookie == "" {
			writeKitJSON(w, http.StatusBadRequest, `{"error":"cookie required"}`)
			return
		}

		// Validate that cookie contains dle_user_id and dle_password.
		if !strings.Contains(req.Cookie, "dle_user_id") || !strings.Contains(req.Cookie, "dle_password") {
			writeKitJSON(w, http.StatusBadRequest, `{"error":"cookie must contain dle_user_id and dle_password"}`)
			return
		}

		sectionKey := "Rezka"
		if req.Premium {
			sectionKey = "RezkaPrem"
		}

		section := map[string]any{
			"enable": true,
			"cookie": req.Cookie,
		}

		if err := store.UpdateSection(tgID, sectionKey, section); err != nil {
			writeKitJSON(w, http.StatusInternalServerError, `{"error":"save failed"}`)
			return
		}

		if req.Premium {
			_ = store.UpdateSection(tgID, "Rezka", map[string]any{"enable": false})
		} else {
			_ = store.DeleteSection(tgID, "RezkaPrem")
		}

		writeKitJSON(w, http.StatusOK, fmt.Sprintf(`{"success":true,"premium":%t}`, req.Premium))
	}
}

func kitRezkaLogin(host, login, password string) (cookie string, premium bool, err error) {
	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Timeout: 20 * time.Second,
		Jar:     jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	form := url.Values{
		"login_name":     {login},
		"login_password": {password},
		"login_not_save": {"0"},
	}

	req, _ := http.NewRequest("POST", host+"/ajax/login/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("rezka login request failed")
	}
	defer resp.Body.Close()

	var parts []string
	for _, sc := range resp.Header.Values("Set-Cookie") {
		if strings.Contains(sc, "=deleted;") {
			continue
		}
		if strings.Contains(sc, "dle_user_id") || strings.Contains(sc, "dle_password") {
			seg := strings.SplitN(sc, ";", 2)[0]
			parts = append(parts, seg)
		}
	}

	if len(parts) < 2 {
		return "", false, fmt.Errorf("authentication failed — check login/password")
	}

	cookie = strings.Join(parts, "; ")

	// Check if premium.
	req2, _ := http.NewRequest("GET", host, nil)
	req2.Header.Set("Cookie", cookie)
	req2.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	resp2, err := client.Do(req2)
	if err == nil {
		defer resp2.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp2.Body, 256*1024))
		html := string(body)
		if strings.Contains(html, "b-premium_user__body") || strings.Contains(html, "b-hd_prem") {
			premium = true
		}
	}

	return cookie, premium, nil
}

// ---- VoKino Bind ----

// kitBindVokinoHandler authenticates with VoKino and saves token.
// POST /api/kit/bind/vokino  body: {"login":"...","password":"..."}
func kitBindVokinoHandler(store *kit.Store, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID, _, err := kitAuthFromRequest(r, cfg)
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusUnauthorized)
			return
		}

		var req struct {
			Login    string `json:"login"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil || req.Login == "" || req.Password == "" {
			writeKitJSON(w, http.StatusBadRequest, `{"error":"login and password required"}`)
			return
		}

		client := httpclient.New(10 * time.Second)
		deviceID := fmt.Sprintf("%x", md5.Sum([]byte(time.Now().String())))[:8]
		apiURL := fmt.Sprintf("http://api.vokino.org/v2/auth?email=%s&passwd=%s&deviceid=%s",
			url.QueryEscape(req.Login), url.QueryEscape(req.Password), deviceID)

		hreq, _ := http.NewRequest("GET", apiURL, nil)
		hreq.Header.Set("User-Agent", "lampac")
		resp, err := client.Do(hreq)
		if err != nil {
			log.Warn().Err(err).Msg("kit: vokino auth request failed")
			writeKitJSON(w, http.StatusBadGateway, `{"error":"api.vokino.org недоступен"}`)
			return
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))

		// VoKino may return plain text token instead of JSON.
		bodyStr := strings.TrimSpace(string(body))

		var result map[string]any
		if err := json.Unmarshal(body, &result); err != nil {
			// If response is a non-empty plain string, treat it as the auth token.
			if len(bodyStr) > 0 && !strings.HasPrefix(bodyStr, "<") && !strings.Contains(bodyStr, " ") && len(bodyStr) < 256 {
				result = map[string]any{"authToken": bodyStr}
			} else {
				log.Warn().Int("status", resp.StatusCode).Int("body_len", len(bodyStr)).Msg("kit: vokino returned non-JSON response")
				writeKitJSON(w, http.StatusBadGateway, fmt.Sprintf(`{"error":"vokino вернул некорректный ответ (HTTP %d)"}`, resp.StatusCode))
				return
			}
		}

		authToken, _ := result["authToken"].(string)
		if authToken == "" {
			errMsg := "токен не получен"
			if e, ok := result["error"].(string); ok && e != "" {
				errMsg = e
			}
			if e, ok := result["message"].(string); ok && e != "" {
				errMsg = e
			}
			log.Warn().Str("error", errMsg).Msg("kit: vokino auth — no token in response")
			writeKitJSON(w, http.StatusBadRequest, fmt.Sprintf(`{"error":%q}`, errMsg))
			return
		}

		section := map[string]any{
			"enable": true,
			"token":  authToken,
		}
		if err := store.UpdateSection(tgID, "VoKino", section); err != nil {
			writeKitJSON(w, http.StatusInternalServerError, `{"error":"save failed"}`)
			return
		}

		writeKitJSON(w, http.StatusOK, `{"success":true}`)
	}
}

// ---- GetsTV Bind ----

// kitBindGetsTVHandler authenticates with GetsTV and saves token.
// POST /api/kit/bind/getstv  body: {"login":"...","password":"..."}
func kitBindGetsTVHandler(store *kit.Store, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID, _, err := kitAuthFromRequest(r, cfg)
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusUnauthorized)
			return
		}

		var req struct {
			Login    string `json:"login"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil || req.Login == "" || req.Password == "" {
			writeKitJSON(w, http.StatusBadRequest, `{"error":"login and password required"}`)
			return
		}

		fingerprint := fmt.Sprintf("%x", md5.Sum([]byte(time.Now().String())))
		postBody := fmt.Sprintf(`{"email":%q,"password":%q,"fingerprint":%q,"device":{}}`,
			req.Login, req.Password, fingerprint)

		getstvHost := "https://api.getsTv.cc"
		client := httpclient.New(10 * time.Second)
		hreq, _ := http.NewRequest("POST", getstvHost+"/api/login", strings.NewReader(postBody))
		hreq.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(hreq)
		if err != nil {
			writeKitJSON(w, http.StatusBadGateway, `{"error":"getstv unavailable"}`)
			return
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		var result map[string]any
		if err := json.Unmarshal(body, &result); err != nil {
			writeKitJSON(w, http.StatusBadGateway, `{"error":"invalid getstv response"}`)
			return
		}

		token, _ := result["token"].(string)
		if token == "" {
			writeKitJSON(w, http.StatusBadRequest, `{"error":"token not received"}`)
			return
		}

		section := map[string]any{
			"enable": true,
			"token":  token,
		}
		if err := store.UpdateSection(tgID, "GetsTV", section); err != nil {
			writeKitJSON(w, http.StatusInternalServerError, `{"error":"save failed"}`)
			return
		}

		writeKitJSON(w, http.StatusOK, `{"success":true}`)
	}
}

// ---- iptv.online Bind ----

// kitBindIptvOnlineHandler saves iptv.online API credentials.
// POST /api/kit/bind/iptvonline  body: {"api_key":"...","api_id":"..."}
func kitBindIptvOnlineHandler(store *kit.Store, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID, _, err := kitAuthFromRequest(r, cfg)
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusUnauthorized)
			return
		}

		var req struct {
			APIKey string `json:"api_key"`
			APIID  string `json:"api_id"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil || req.APIKey == "" || req.APIID == "" {
			writeKitJSON(w, http.StatusBadRequest, `{"error":"api_key and api_id required"}`)
			return
		}

		section := map[string]any{
			"enable": true,
			"token":  req.APIKey + ":" + req.APIID,
		}
		if err := store.UpdateSection(tgID, "IptvOnline", section); err != nil {
			writeKitJSON(w, http.StatusInternalServerError, `{"error":"save failed"}`)
			return
		}

		writeKitJSON(w, http.StatusOK, `{"success":true}`)
	}
}

// ---- Helpers ----

func writeKitJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// ---- PornLab Bind ----

// kitBindPornLabHandler authenticates with PornLab using login/password and stores bb_data cookie.
// POST /api/kit/bind/pornlab  body: {"login":"...", "password":"..."}
func kitBindPornLabHandler(store *kit.Store, cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tgID, _, err := kitAuthFromRequest(r, cfg)
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusUnauthorized)
			return
		}

		var req struct {
			Login    string `json:"login"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil || req.Login == "" || req.Password == "" {
			writeKitJSON(w, http.StatusBadRequest, `{"error":"login and password required"}`)
			return
		}

		host := sisihttp.SisiSourceHost("PornLab", "https://pornolab.net")
		cookie, username, err := kitPornLabLogin(host, req.Login, req.Password)
		if err != nil {
			writeKitJSON(w, http.StatusBadGateway, fmt.Sprintf(`{"error":%q}`, err.Error()))
			return
		}

		section := map[string]any{
			"enable": true,
			"cookie": cookie,
		}
		if err := store.UpdateSection(tgID, "PornLab", section); err != nil {
			writeKitJSON(w, http.StatusInternalServerError, `{"error":"save failed"}`)
			return
		}

		writeKitJSON(w, http.StatusOK, fmt.Sprintf(`{"success":true,"username":%q}`, username))
	}
}

// kitPornLabLogin authenticates with PornLab and returns bb_data cookie value and username.
func kitPornLabLogin(host, login, password string) (cookie string, username string, err error) {
	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Timeout: 20 * time.Second,
		Jar:     jar,
	}

	form := url.Values{
		"login_username": {login},
		"login_password": {password},
		"login":          {"\xC2\xF5\xEE\xE4"}, // "Вход" in windows-1251
	}

	loginURL := strings.TrimRight(host, "/") + "/forum/login.php"
	req, err := http.NewRequest(http.MethodPost, loginURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("login request failed: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	// Extract bb_data cookie.
	u, _ := url.Parse(host + "/forum/")
	for _, c := range jar.Cookies(u) {
		if c.Name == "bb_data" && len(c.Value) > 10 {
			return c.Value, login, nil
		}
	}

	return "", "", fmt.Errorf("login failed: bb_data cookie not set (wrong credentials?)")
}

// md5hex returns lowercase hex MD5 of input.
func md5hex(s string) string {
	h := md5.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}
