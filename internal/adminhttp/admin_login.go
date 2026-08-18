package adminhttp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	stdjson "encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"lampac-go/internal/tgauth"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

// RegisterLoginRoutes wires the password-login + TOTP-2FA + logout admin routes.
// secret is the session-cookie HMAC key. Split out of the host's admin route
// setup so the auth handlers live in internal/adminhttp.
func RegisterLoginRoutes(router chi.Router, adminPath string, pwStore *tgauth.PasswordAuthStore, secret []byte) {
	router.Get("/"+adminPath+"/login", adminLoginPageHandler(pwStore))
	router.Post("/"+adminPath+"/api/auth/login", adminLoginHandler(pwStore, secret, adminPath))
	router.Post("/"+adminPath+"/api/auth/verify-totp", adminTOTPVerifyHandler(pwStore, secret, adminPath))
	router.Get("/"+adminPath+"/setup-2fa", adminTOTPSetupPageHandler(pwStore, secret, adminPath))
	router.Get("/"+adminPath+"/api/auth/setup-totp", adminTOTPSetupDataHandler(pwStore, secret))
	router.Post("/"+adminPath+"/api/auth/setup-totp", adminTOTPEnableHandler(pwStore, secret, adminPath))
	router.Get("/"+adminPath+"/api/auth/logout", adminLogoutHandler(adminPath))
}

// --- Rate Limiter ---

type loginRateLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time // IP → timestamps of failed attempts
}

var loginLimiter = &loginRateLimiter{
	attempts: make(map[string][]time.Time),
}

const (
	loginMaxAttempts = 5
	loginWindowDur   = 15 * time.Minute
)

func (rl *loginRateLimiter) isBlocked(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-loginWindowDur)
	var recent []time.Time
	for _, t := range rl.attempts[ip] {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}
	rl.attempts[ip] = recent
	return len(recent) >= loginMaxAttempts
}

func (rl *loginRateLimiter) recordFailure(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.attempts[ip] = append(rl.attempts[ip], time.Now())
}

// --- Session Helpers ---

const (
	AdminSessionCookie = "lampac_admin_session"
	sessionFullTTL     = 24 * time.Hour
	sessionPartialTTL  = 5 * time.Minute
)

type sessionPayload struct {
	Exp     int64 `json:"exp"`
	Partial bool  `json:"partial,omitempty"`
}

func createAdminSession(secret []byte, partial bool) string {
	ttl := sessionFullTTL
	if partial {
		ttl = sessionPartialTTL
	}
	payload := sessionPayload{
		Exp:     time.Now().Add(ttl).Unix(),
		Partial: partial,
	}
	data, _ := stdjson.Marshal(payload)
	b64 := base64.RawURLEncoding.EncodeToString(data)
	mac := hmac.New(sha256.New, secret)
	mac.Write(data)
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return b64 + "." + sig
}

func ValidateAdminSession(cookie string, secret []byte) (partial bool, ok bool) {
	parts := strings.SplitN(cookie, ".", 2)
	if len(parts) != 2 {
		return false, false
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false, false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false, false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(data)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return false, false
	}
	var p sessionPayload
	if stdjson.Unmarshal(data, &p) != nil {
		return false, false
	}
	if time.Now().Unix() > p.Exp {
		return false, false
	}
	return p.Partial, true
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	http.SetCookie(w, &http.Cookie{
		Name:     AdminSessionCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	})
}

// --- Handlers ---

func adminLoginPageHandler(pwStore *tgauth.PasswordAuthStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !pwStore.IsConfigured() {
			writeHTML(w, http.StatusOK, adminLoginNotConfiguredHTML)
			return
		}
		needTOTP := pwStore.IsTOTPEnabled()
		html := adminLoginHTML
		if needTOTP {
			html = strings.Replace(html, "/*TOTP_BLOCK*/", "", 1)
			html = strings.Replace(html, "display:none", "display:block", 1)
		}
		// Show passkey button if WebAuthn credentials exist.
		if waStore != nil && waStore.HasCredentials() {
			html = strings.Replace(html, "/*WEBAUTHN_BLOCK*/", "", 1)
			html = strings.Replace(html, "/*WEBAUTHN_VISIBLE*/display:none", "display:flex", 1)
		}
		writeHTML(w, http.StatusOK, html)
	}
}

func adminLoginHandler(pwStore *tgauth.PasswordAuthStore, secret []byte, adminPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		if loginLimiter.isBlocked(ip) {
			log.Warn().Str("ip", ip).Str("auth", "blocked").Msg("admin login: rate limit exceeded")
			writeJSON(w, http.StatusTooManyRequests, map[string]any{
				"ok": false, "error": "Слишком много попыток. Подождите 15 минут.",
			})
			return
		}

		body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		var req struct {
			Password string `json:"password"`
			TOTPCode string `json:"totp_code"`
		}
		if stdjson.Unmarshal(body, &req) != nil || req.Password == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Неверный запрос"})
			return
		}

		if !pwStore.CheckPassword(req.Password) {
			loginLimiter.recordFailure(ip)
			log.Warn().Str("ip", ip).Str("auth", "failed").Msg("admin login: invalid password")
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"ok": false, "error": "Неверный пароль",
			})
			return
		}

		// Password correct.
		if !pwStore.IsTOTPEnabled() {
			// TOTP not set up — 2FA is optional, grant a full session and let
			// the user enable it later from settings if they want. The
			// /setup-2fa page remains reachable for opt-in enrollment.
			session := createAdminSession(secret, false)
			setSessionCookie(w, r, session, int(sessionFullTTL.Seconds()))
			writeJSON(w, http.StatusOK, map[string]any{
				"ok": true, "status": "ok",
				"redirect": "/" + adminPath,
			})
			return
		}

		// TOTP is enabled — need code.
		if req.TOTPCode == "" {
			// First step: password OK, need TOTP.
			session := createAdminSession(secret, true)
			setSessionCookie(w, r, session, int(sessionPartialTTL.Seconds()))
			writeJSON(w, http.StatusOK, map[string]any{
				"ok": true, "status": "need_totp",
			})
			return
		}

		// Verify TOTP.
		if !pwStore.VerifyTOTP(req.TOTPCode) {
			loginLimiter.recordFailure(ip)
			log.Warn().Str("ip", ip).Str("auth", "failed").Msg("admin login: invalid TOTP code")
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"ok": false, "error": "Неверный код 2FA",
			})
			return
		}

		// Full auth — issue full session.
		session := createAdminSession(secret, false)
		setSessionCookie(w, r, session, int(sessionFullTTL.Seconds()))
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "status": "ok",
			"redirect": "/" + adminPath,
		})
	}
}

func adminTOTPVerifyHandler(pwStore *tgauth.PasswordAuthStore, secret []byte, adminPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Require partial session (password already verified).
		cookie, err := r.Cookie(AdminSessionCookie)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "Сессия истекла"})
			return
		}
		partial, ok := ValidateAdminSession(cookie.Value, secret)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "Сессия истекла"})
			return
		}
		if !partial {
			// Already fully authenticated.
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "redirect": "/" + adminPath})
			return
		}

		body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		var req struct {
			Code string `json:"code"`
		}
		if stdjson.Unmarshal(body, &req) != nil || req.Code == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Введите код"})
			return
		}

		if !pwStore.VerifyTOTP(req.Code) {
			ip := clientIP(r)
			loginLimiter.recordFailure(ip)
			log.Warn().Str("ip", ip).Str("auth", "failed").Msg("admin login: invalid TOTP code (verify)")
			writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "Неверный код 2FA"})
			return
		}

		session := createAdminSession(secret, false)
		setSessionCookie(w, r, session, int(sessionFullTTL.Seconds()))
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "status": "ok",
			"redirect": "/" + adminPath,
		})
	}
}

func adminTOTPSetupPageHandler(pwStore *tgauth.PasswordAuthStore, secret []byte, adminPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Require partial session.
		cookie, err := r.Cookie(AdminSessionCookie)
		if err != nil {
			http.Redirect(w, r, "/"+adminPath+"/login", http.StatusFound)
			return
		}
		partial, ok := ValidateAdminSession(cookie.Value, secret)
		if !ok || !partial {
			http.Redirect(w, r, "/"+adminPath+"/login", http.StatusFound)
			return
		}
		writeHTML(w, http.StatusOK, strings.Replace(adminTOTPSetupHTML, "ADMIN_PATH_PLACEHOLDER", adminPath, -1))
	}
}

func adminTOTPSetupDataHandler(pwStore *tgauth.PasswordAuthStore, secret []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Require partial session.
		cookie, err := r.Cookie(AdminSessionCookie)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "Сессия истекла"})
			return
		}
		partial, ok := ValidateAdminSession(cookie.Value, secret)
		if !ok || !partial {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "Сессия истекла"})
			return
		}

		totpSecret, provURI, err := pwStore.GenerateTOTP()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     true,
			"secret": totpSecret,
			"uri":    provURI,
		})
	}
}

func adminTOTPEnableHandler(pwStore *tgauth.PasswordAuthStore, secret []byte, adminPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Require partial session.
		cookie, err := r.Cookie(AdminSessionCookie)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "Сессия истекла"})
			return
		}
		partial, ok := ValidateAdminSession(cookie.Value, secret)
		if !ok || !partial {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "Сессия истекла"})
			return
		}

		body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		var req struct {
			Code string `json:"code"`
		}
		if stdjson.Unmarshal(body, &req) != nil || req.Code == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Введите код"})
			return
		}

		if err := pwStore.EnableTOTP(req.Code); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Неверный код. Проверьте время на устройстве."})
			return
		}

		// TOTP enabled — issue full session.
		session := createAdminSession(secret, false)
		setSessionCookie(w, r, session, int(sessionFullTTL.Seconds()))
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "status": "ok",
			"redirect": "/" + adminPath,
		})
	}
}

func adminLogoutHandler(adminPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setSessionCookie(w, r, "", -1)
		http.Redirect(w, r, "/"+adminPath+"/login", http.StatusFound)
	}
}

// --- Inline HTML ---

const adminLoginNotConfiguredHTML = `<!DOCTYPE html>
<html lang="ru"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1.0">
<title>Alpac — Админ-панель</title>
<style>*{margin:0;padding:0;box-sizing:border-box}body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;background:#1a1a2e;color:#e0e0e0;display:flex;justify-content:center;align-items:center;min-height:100vh}.card{background:#16213e;border-radius:16px;padding:40px;max-width:420px;width:90%;text-align:center;box-shadow:0 8px 32px rgba(0,0,0,0.3)}h1{font-size:24px;margin-bottom:12px;color:#fff}.subtitle{color:#8892b0;font-size:14px}</style>
</head><body><div class="card"><h1>Админ-панель</h1><p class="subtitle">Пароль администратора не настроен.<br>Задайте AdminAuth.password в init.json и перезапустите сервер.</p></div></body></html>`

const adminLoginHTML = `<!DOCTYPE html>
<html lang="ru"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1.0">
<title>Alpac — Вход в админ-панель</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;background:#1a1a2e;color:#e0e0e0;display:flex;justify-content:center;align-items:center;min-height:100vh}
.card{background:#16213e;border-radius:16px;padding:40px;max-width:420px;width:90%;text-align:center;box-shadow:0 8px 32px rgba(0,0,0,0.3)}
h1{font-size:24px;margin-bottom:8px;color:#fff}
.subtitle{color:#8892b0;margin-bottom:24px;font-size:14px}
input{width:100%;padding:12px 16px;border:1px solid #2a2a4a;border-radius:8px;background:#0f0f1a;color:#e0e0e0;font-size:16px;outline:none;margin-bottom:12px;transition:border 0.2s}
input:focus{border-color:#64ffda}
button{width:100%;padding:12px;border:none;border-radius:8px;background:#64ffda;color:#0f0f1a;font-size:16px;font-weight:600;cursor:pointer;transition:opacity 0.2s}
button:hover{opacity:0.9}
button:disabled{opacity:0.5;cursor:not-allowed}
.error{color:#ff6b6b;font-size:13px;margin-bottom:12px;min-height:18px}
.totp-section{/*TOTP_BLOCK*/display:none;margin-top:8px}
.webauthn-section{/*WEBAUTHN_BLOCK*//*WEBAUTHN_VISIBLE*/display:none;flex-direction:column;align-items:center;margin-top:16px}
.divider{display:flex;align-items:center;width:100%;margin:4px 0 12px}
.divider::before,.divider::after{content:'';flex:1;border-bottom:1px solid #2a2a4a}
.divider span{padding:0 12px;color:#8892b0;font-size:13px}
.passkey-btn{width:100%;padding:12px;border:2px solid #64ffda;border-radius:8px;background:transparent;color:#64ffda;font-size:15px;font-weight:600;cursor:pointer;transition:all 0.2s;display:flex;align-items:center;justify-content:center;gap:8px}
.passkey-btn:hover{background:rgba(100,255,218,0.1)}
.passkey-btn:disabled{opacity:0.5;cursor:not-allowed}
.passkey-btn svg{width:20px;height:20px;fill:currentColor}
.brand{color:#64ffda;font-size:12px;margin-top:20px;opacity:0.5}
</style>
</head><body>
<div class="card">
<h1>Вход</h1>
<p class="subtitle">Админ-панель Alpac</p>
<div class="error" id="error"></div>
<form id="loginForm" onsubmit="return doLogin(event)">
<input type="password" id="password" placeholder="Пароль" autocomplete="current-password" autofocus>
<div class="totp-section" id="totpSection">
<input type="text" id="totpCode" placeholder="Код 2FA (6 цифр)" autocomplete="one-time-code" inputmode="numeric" maxlength="6" pattern="[0-9]{6}">
</div>
<button type="submit" id="submitBtn">Войти</button>
</form>
<div class="webauthn-section" id="webauthnSection">
<div class="divider"><span>или</span></div>
<button type="button" class="passkey-btn" id="passkeyBtn" onclick="doPasskey()">
<svg viewBox="0 0 24 24"><path d="M2 6c0-1.1.9-2 2-2h4v2H4v4H2V6zm18-2h-4v2h4v4h2V6c0-1.1-.9-2-2-2zM4 18h4v2H4c-1.1 0-2-.9-2-2v-4h2v4zm16 0h-4v2h4c1.1 0 2-.9 2-2v-4h-2v4zm-8-6c1.66 0 3-1.34 3-3s-1.34-3-3-3-3 1.34-3 3 1.34 3 3 3zm0 2c-2 0-6 1-6 3v2h12v-2c0-2-4-3-6-3z"/></svg>
Войти с Passkey
</button>
</div>
<div class="brand">Alpac</div>
</div>
<script>
var step='password';
function doLogin(e){
  e.preventDefault();
  var pw=document.getElementById('password').value;
  if(!pw){document.getElementById('error').textContent='Введите пароль';return false}
  var btn=document.getElementById('submitBtn');
  btn.disabled=true;
  var body={password:pw};
  if(step==='totp'){
    body.totp_code=document.getElementById('totpCode').value;
    if(!body.totp_code){document.getElementById('error').textContent='Введите код 2FA';btn.disabled=false;return false}
  }
  fetch(location.pathname.replace('/login','/api/auth/login'),{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)})
  .then(function(r){return r.json()})
  .then(function(d){
    btn.disabled=false;
    if(!d.ok){document.getElementById('error').textContent=d.error||'Ошибка';return}
    if(d.status==='need_totp'){
      step='totp';
      document.getElementById('totpSection').style.display='block';
      document.getElementById('totpCode').focus();
      document.getElementById('error').textContent='';
      return;
    }
    if(d.status==='ok'&&d.redirect){location.href=d.redirect;return}
  })
  .catch(function(){btn.disabled=false;document.getElementById('error').textContent='Ошибка сети'});
  return false;
}
function bufToBase64url(buf){
  var bytes=new Uint8Array(buf);
  var str='';
  for(var i=0;i<bytes.length;i++)str+=String.fromCharCode(bytes[i]);
  return btoa(str).replace(/\+/g,'-').replace(/\//g,'_').replace(/=+$/,'');
}
function base64urlToBuf(s){
  s=s.replace(/-/g,'+').replace(/_/g,'/');
  while(s.length%4)s+='=';
  var bin=atob(s);
  var buf=new Uint8Array(bin.length);
  for(var i=0;i<bin.length;i++)buf[i]=bin.charCodeAt(i);
  return buf.buffer;
}
function doPasskey(){
  var btn=document.getElementById('passkeyBtn');
  var errEl=document.getElementById('error');
  btn.disabled=true;
  errEl.textContent='';
  var basePath=location.pathname.replace('/login','');
  fetch(basePath+'/api/webauthn/login/begin',{method:'POST'})
  .then(function(r){return r.json()})
  .then(function(opts){
    if(opts.error){errEl.textContent=opts.error;btn.disabled=false;return Promise.reject('abort')}
    var publicKey=opts.publicKey||opts.response||opts;
    if(publicKey.challenge){
      if(typeof publicKey.challenge==='string')publicKey.challenge=base64urlToBuf(publicKey.challenge);
    }
    if(publicKey.allowCredentials){
      publicKey.allowCredentials=publicKey.allowCredentials.map(function(c){
        if(typeof c.id==='string')c.id=base64urlToBuf(c.id);
        return c;
      });
    }
    return navigator.credentials.get({publicKey:publicKey});
  })
  .then(function(assertion){
    if(!assertion)return;
    var body={
      id:assertion.id,
      rawId:bufToBase64url(assertion.rawId),
      type:assertion.type,
      response:{
        authenticatorData:bufToBase64url(assertion.response.authenticatorData),
        clientDataJSON:bufToBase64url(assertion.response.clientDataJSON),
        signature:bufToBase64url(assertion.response.signature),
        userHandle:assertion.response.userHandle?bufToBase64url(assertion.response.userHandle):''
      }
    };
    return fetch(basePath+'/api/webauthn/login/complete',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});
  })
  .then(function(r){if(r)return r.json()})
  .then(function(d){
    btn.disabled=false;
    if(!d)return;
    if(!d.ok){errEl.textContent=d.error||'Ошибка аутентификации';return}
    if(d.redirect)location.href=d.redirect;
  })
  .catch(function(e){
    btn.disabled=false;
    if(e==='abort')return;
    if(e&&e.name==='NotAllowedError'){errEl.textContent='Аутентификация отменена';return}
    errEl.textContent='Ошибка Passkey';
  });
}
</script>
</body></html>`

const adminTOTPSetupHTML = `<!DOCTYPE html>
<html lang="ru"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1.0">
<title>Alpac — Настройка 2FA</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;background:#1a1a2e;color:#e0e0e0;display:flex;justify-content:center;align-items:center;min-height:100vh}
.card{background:#16213e;border-radius:16px;padding:40px;max-width:480px;width:90%;text-align:center;box-shadow:0 8px 32px rgba(0,0,0,0.3)}
h1{font-size:22px;margin-bottom:8px;color:#fff}
.subtitle{color:#8892b0;margin-bottom:20px;font-size:14px}
.steps{text-align:left;margin:16px 0;color:#ccd6f6;font-size:14px;line-height:1.8}
.steps b{color:#64ffda}
.secret-box{background:#0f0f1a;border:1px solid #2a2a4a;border-radius:8px;padding:14px;margin:12px 0;font-family:'Courier New',monospace;font-size:16px;letter-spacing:2px;color:#64ffda;word-break:break-all;cursor:pointer;position:relative}
.secret-box:hover{border-color:#64ffda}
.secret-box .copy-hint{position:absolute;top:4px;right:8px;font-size:10px;color:#8892b0;font-family:sans-serif;letter-spacing:0}
.qr-link{display:inline-block;margin:8px 0 16px;color:#64ffda;font-size:13px;text-decoration:underline}
input{width:100%;padding:12px 16px;border:1px solid #2a2a4a;border-radius:8px;background:#0f0f1a;color:#e0e0e0;font-size:20px;outline:none;margin:12px 0;text-align:center;letter-spacing:8px;transition:border 0.2s}
input:focus{border-color:#64ffda}
button{width:100%;padding:12px;border:none;border-radius:8px;background:#64ffda;color:#0f0f1a;font-size:16px;font-weight:600;cursor:pointer}
button:hover{opacity:0.9}
button:disabled{opacity:0.5;cursor:not-allowed}
.error{color:#ff6b6b;font-size:13px;margin:8px 0;min-height:18px}
.loading{color:#8892b0;font-size:14px;margin:20px 0}
.brand{color:#64ffda;font-size:12px;margin-top:20px;opacity:0.5}
</style>
</head><body>
<div class="card">
<h1>Настройка двухфакторной аутентификации</h1>
<p class="subtitle">Обязательный шаг для безопасного доступа</p>
<div id="loading" class="loading">Загрузка...</div>
<div id="setup" style="display:none">
<div class="steps">
<b>1.</b> Установите Google Authenticator или Authy<br>
<b>2.</b> Добавьте аккаунт вручную с ключом ниже:<br>
</div>
<div class="secret-box" id="secretBox" onclick="copySecret()">
<span class="copy-hint">нажмите чтобы скопировать</span>
<span id="secretText"></span>
</div>
<a href="#" id="otpauthLink" class="qr-link">Открыть в приложении (otpauth://)</a>
<div class="steps"><b>3.</b> Введите 6-значный код из приложения:</div>
<div class="error" id="error"></div>
<form onsubmit="return doVerify(event)">
<input type="text" id="code" placeholder="000000" autocomplete="one-time-code" inputmode="numeric" maxlength="6" pattern="[0-9]{6}" autofocus>
<button type="submit" id="submitBtn">Подтвердить и включить 2FA</button>
</form>
</div>
<div class="brand">Alpac</div>
</div>
<script>
var adminPath='ADMIN_PATH_PLACEHOLDER';
function load(){
  fetch('/'+adminPath+'/api/auth/setup-totp').then(function(r){return r.json()}).then(function(d){
    if(!d.ok){document.getElementById('loading').textContent='Ошибка: '+(d.error||'');return}
    document.getElementById('loading').style.display='none';
    document.getElementById('setup').style.display='block';
    document.getElementById('secretText').textContent=d.secret;
    document.getElementById('otpauthLink').href=d.uri;
  }).catch(function(){document.getElementById('loading').textContent='Ошибка сети'});
}
function copySecret(){
  var t=document.getElementById('secretText').textContent;
  if(navigator.clipboard)navigator.clipboard.writeText(t);
  var box=document.getElementById('secretBox');
  box.style.borderColor='#64ffda';
  setTimeout(function(){box.style.borderColor='#2a2a4a'},1000);
}
function doVerify(e){
  e.preventDefault();
  var code=document.getElementById('code').value;
  if(!code||code.length!==6){document.getElementById('error').textContent='Введите 6-значный код';return false}
  var btn=document.getElementById('submitBtn');
  btn.disabled=true;
  fetch('/'+adminPath+'/api/auth/setup-totp',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({code:code})})
  .then(function(r){return r.json()}).then(function(d){
    btn.disabled=false;
    if(!d.ok){document.getElementById('error').textContent=d.error||'Ошибка';return}
    if(d.redirect)location.href=d.redirect;
  }).catch(function(){btn.disabled=false;document.getElementById('error').textContent='Ошибка сети'});
  return false;
}
load();
</script>
</body></html>`
