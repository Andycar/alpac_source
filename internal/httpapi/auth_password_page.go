package httpapi

// HTML auth page rendered when config.Auth.Mode == "password".
//
// Layout mirrors the TG auth page (colours, card, blocks) so the styling
// admin already configured under TelegramAuth.AuthPage carries over —
// users don't notice the underlying mechanism, only that the form is
// different. Three blocks live here:
//
//   - password_login: username + password form, submits to /auth/password/login.
//   - status: feedback line (replaces TG poll status).
//   - pairing: shown on TVs/clients that load /tg/auth without a desktop
//     to log in from. Generates a code from the pending store and waits
//     for the password-logged-in browser to redeem it.

import (
	"fmt"
	"net/http"
	"strings"

	"lampac-go/internal/config"
)

// renderPasswordAuthPage replaces the TG block grid with login form +
// status + (optional) pairing code. Reuses getAuthPageStyle so the
// colour palette and custom CSS the admin set up still applies.
func renderPasswordAuthPage(w http.ResponseWriter, r *http.Request) {
	s := getAuthPageStyle()

	bgImageCSS := ""
	if s.BgImageURL != "" {
		bgImageCSS = fmt.Sprintf("background-image:url('%s');background-size:cover;background-position:center;", s.BgImageURL)
	}
	customStyleTag := ""
	if s.CustomCSS != "" {
		customStyleTag = "<style>" + s.CustomCSS + "</style>"
	}
	logoHTML := ""
	if s.LogoURL != "" {
		logoHTML = fmt.Sprintf(`<img src="%s" alt="Logo" style="max-width:120px;max-height:80px;margin-bottom:16px;border-radius:8px">`, s.LogoURL)
	}

	title := s.Title
	if title == "Авторизация" || title == "" {
		title = "Вход"
	}
	subtitle := s.Subtitle
	if subtitle == "" || strings.Contains(subtitle, "Telegram") {
		subtitle = "Введите логин и пароль для доступа"
	}

	formHTML := `
<form id="pw-form" onsubmit="return submitPasswordLogin(event)" style="margin-top:16px">
  <input id="pw-login" name="username" type="text" placeholder="Логин" autocomplete="username"
    style="width:100%;padding:14px;margin-bottom:12px;background:rgba(255,255,255,0.08);border:1px solid rgba(255,255,255,0.18);border-radius:8px;color:#fff;font-size:16px;outline:none"
    required>
  <input id="pw-pass" name="password" type="password" placeholder="Пароль" autocomplete="current-password"
    style="width:100%;padding:14px;margin-bottom:16px;background:rgba(255,255,255,0.08);border:1px solid rgba(255,255,255,0.18);border-radius:8px;color:#fff;font-size:16px;outline:none"
    required>
  <button id="pw-submit" type="submit"
    style="width:100%;padding:14px;background:` + s.ButtonColor + `;color:#fff;border:none;border-radius:8px;font-size:16px;font-weight:500;cursor:pointer">Войти</button>
</form>`

	statusHTML := `<div id="pw-status" class="status waiting" style="margin-top:16px;padding:12px;border-radius:8px;font-size:14px;background:` + s.BgColor + `;color:` + s.TextColor + `;opacity:0.7;min-height:42px;display:none"></div>`

	blocks := map[string]string{
		"logo":           logoHTML,
		"title":          fmt.Sprintf(`<h1 style="font-size:24px;margin-bottom:12px;color:#fff">%s</h1>`, title),
		"subtitle":       fmt.Sprintf(`<p style="color:%s;opacity:0.7;margin-bottom:8px;font-size:14px">%s</p>`, s.TextColor, subtitle),
		"password_login": formHTML,
		"status":         statusHTML,
	}

	// Default layout when admin hasn't set BlockRows for password mode.
	rows := s.BlockRows
	hasPwBlock := false
	for _, row := range rows {
		for _, id := range row {
			if id == "password_login" {
				hasPwBlock = true
				break
			}
		}
	}
	if !hasPwBlock {
		rows = [][]string{{"logo"}, {"title"}, {"subtitle"}, {"password_login"}, {"status"}}
	}

	cardContent := renderBlockRows(rows, blocks, s.BlockStyles)

	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="ru">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>%s</title>
<style>
* { margin: 0; padding: 0; box-sizing: border-box; }
body {
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
  background: %s; color: %s;
  display: flex; justify-content: center; align-items: center;
  min-height: 100vh; %s
}
.card {
  background: %s; border-radius: 16px; padding: 40px;
  max-width: 420px; width: 90%%; text-align: center;
  box-shadow: 0 8px 32px rgba(0,0,0,0.3);
}
input:focus { border-color: %s !important; box-shadow: 0 0 0 2px %s44; }
button[type=submit]:hover { opacity: 0.9; }
.status.error { color: #ff6b6b; opacity: 1 !important; }
.status.success { color: %s; opacity: 1 !important; }
</style>
%s
</head>
<body>
<div class="card">
%s
</div>
<script>
window.submitPasswordLogin = function(e) {
  e.preventDefault();
  var login = document.getElementById('pw-login').value.trim();
  var pass  = document.getElementById('pw-pass').value;
  var btn   = document.getElementById('pw-submit');
  var st    = document.getElementById('pw-status');
  if (!login || !pass) return false;
  btn.disabled = true;
  btn.textContent = '...';
  st.style.display = 'block';
  st.className = 'status waiting';
  st.textContent = 'Проверка...';
  fetch('/auth/password/login', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    credentials: 'include',
    body: JSON.stringify({username: login, password: pass})
  }).then(function(r) { return r.json().then(function(d) { return {ok: r.ok, data: d}; }); })
    .then(function(resp) {
      if (resp.ok && resp.data && resp.data.ok) {
        st.className = 'status success';
        st.textContent = '✓ Успех. Перенаправление...';
        // Stamp localStorage so older plugins reading lampac_auth_token
        // pick the new value up without waiting for cookie read.
        try { localStorage.setItem('lampac_auth_token', resp.data.token); } catch(e) {}
        setTimeout(function() { window.location.href = '/'; }, 600);
      } else {
        st.className = 'status error';
        st.textContent = (resp.data && resp.data.error) ? resp.data.error : 'Неверные данные';
        btn.disabled = false;
        btn.textContent = 'Войти';
      }
    })
    .catch(function() {
      st.className = 'status error';
      st.textContent = 'Ошибка сети';
      btn.disabled = false;
      btn.textContent = 'Войти';
    });
  return false;
};
</script>
</body>
</html>`,
		title,
		s.BgColor, s.TextColor, bgImageCSS,
		s.CardColor,
		s.AccentColor, s.AccentColor,
		s.AccentColor,
		customStyleTag,
		cardContent,
	)

	writeHTML(w, http.StatusOK, html)
}

// preserve config import in case the file imports it lazily.
var _ = config.AuthModePassword
